package cache

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"enhancedkimi/internal/storage"
)

const (
	// scanInterval is the minimum allowed gap between record scans. The plan
	// forbids a shorter interval and forbids adding fsnotify: go.mod has no
	// fsnotify dependency, so scanning is the only mechanism.
	scanInterval = 5 * time.Second
	// scanSessionBudget bounds how many sessions one pass touches so a large
	// history does not turn a background tick into a full disk walk.
	scanSessionBudget = 200
)

// Store owns the cache_steps table and the scan positions.
//
// Everything below is reached concurrently: the automation tick, the settings page's
// four parallel cache reads and the keepalive closed loop all call into one Store. The
// database has its own concurrency contract, so the locks here cover only the Go state
// and never wrap a query: a lock that spans a query would serialize every reader and,
// worse, deadlock callers that hold another lock across the same calls.
type Store struct {
	db *sql.DB

	// mu covers home, files and lastScan.
	mu   sync.Mutex
	home string
	// files keeps one incremental reader per wire.jsonl path.
	files map[string]*fileState
	// lastScan guards the scan interval across callers.
	lastScan time.Time

	// scanMu keeps two concurrent scans from reading the same file offsets and
	// inserting the same steps twice. It is separate from mu because a scan is long
	// and mu must stay available to Home/SetHome while it runs.
	scanMu sync.Mutex
}

// NewStore migrates the schema and returns a store bound to home.
func NewStore(db *sql.DB, home string) (*Store, error) {
	if err := storage.Migrate(db, "cache_metrics", []string{
		`CREATE TABLE cache_steps(
			session_id TEXT NOT NULL,
			agent TEXT NOT NULL,
			step_uuid TEXT NOT NULL,
			model TEXT NOT NULL,
			time TEXT NOT NULL,
			turn_id TEXT NOT NULL DEFAULT '',
			step_index INTEGER NOT NULL DEFAULT 0,
			finish_reason TEXT NOT NULL DEFAULT '',
			input_other INTEGER NOT NULL DEFAULT 0,
			input_cache_read INTEGER NOT NULL DEFAULT 0,
			input_cache_creation INTEGER NOT NULL DEFAULT 0,
			output INTEGER NOT NULL DEFAULT 0,
			first_token_ms INTEGER NOT NULL DEFAULT -1,
			decode_ms INTEGER NOT NULL DEFAULT -1,
			PRIMARY KEY(session_id,agent,step_uuid))`,
		`CREATE INDEX cache_steps_time ON cache_steps(time)`,
		`CREATE INDEX cache_steps_session_time ON cache_steps(session_id,agent,time)`,
		`CREATE TABLE cache_sessions(
			session_id TEXT PRIMARY KEY,
			cwd TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT '',
			agent_count INTEGER NOT NULL DEFAULT 0,
			scanned_at TEXT NOT NULL DEFAULT '')`,
		// cache_live holds the two volatile signals keepalive needs. They are not
		// derived from cache_steps because "a subagent is still running" and "the
		// main agent is blocked waiting" are states, not completed steps.
		`CREATE TABLE cache_live(
			session_id TEXT PRIMARY KEY,
			open_background INTEGER NOT NULL DEFAULT 0,
			blocked INTEGER NOT NULL DEFAULT 0,
			last_user_prompt TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT '')`,
	}); err != nil {
		return nil, err
	}
	// cache_steps.time is compared and ordered as text, so any row written with a
	// variable-width timestamp keeps sorting wrongly until it is rewritten.
	if err := storage.BackfillSortableTimes(context.Background(), db, []storage.TimeColumn{
		{Table: "cache_steps", Column: "time"},
		{Table: "cache_sessions", Column: "created_at"},
		{Table: "cache_sessions", Column: "updated_at"},
		{Table: "cache_sessions", Column: "scanned_at"},
		{Table: "cache_live", Column: "last_user_prompt"},
		{Table: "cache_live", Column: "updated_at"},
	}); err != nil {
		return nil, err
	}
	return &Store{db: db, home: home, files: map[string]*fileState{}}, nil
}

// Home returns the resolved KIMI_CODE_HOME root the store reads from.
func (s *Store) Home() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.home
}

// SetHome repoints the store, dropping cached file offsets: a different home has
// different inodes and offsets mean nothing across roots.
func (s *Store) SetHome(home string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if home == s.home {
		return
	}
	s.home = home
	s.files = map[string]*fileState{}
}

// fileStateFor returns the incremental reader for one wire path, creating it when it
// is new. The returned state is owned by the running scan, which holds scanMu.
func (s *Store) fileStateFor(path, agent string) *fileState {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.files[path]
	if !ok || state.parser == nil {
		state = &fileState{parser: newParser(agent)}
		s.files[path] = state
	}
	return state
}

// beginScan enforces the scan interval and claims the current home. A caller that is
// told to skip must not read records at all.
func (s *Store) beginScan(now time.Time, force bool) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !force && !s.lastScan.IsZero() && now.Sub(s.lastScan) < scanInterval {
		return "", false
	}
	s.lastScan = now
	return s.home, true
}

// Scan reads new record lines and persists them. A scan that runs sooner than
// scanInterval after the previous one is skipped, which is what enforces the
// "not shorter than 5 seconds" rule no matter how often a caller ticks.
func (s *Store) Scan(ctx context.Context, now time.Time, force bool) (ScanResult, error) {
	home, ok := s.beginScan(now, force)
	if !ok {
		// Another caller may still be scanning records and committing steps to SQLite;
		// wait for scanMu so parallel cache.* readers see the committed rows.
		s.scanMu.Lock()
		s.scanMu.Unlock()
		return ScanResult{Skipped: true}, nil
	}
	// Two concurrent scans would both read the same offsets and both insert the same
	// steps; serialize them instead of racing on the shared parsers.
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if home == "" {
		return ScanResult{}, nil
	}
	sessions, err := discover(home)
	if err != nil {
		return ScanResult{}, err
	}
	result := ScanResult{Sessions: len(sessions)}
	if len(sessions) > scanSessionBudget {
		sessions = sessions[:scanSessionBudget]
		result.BudgetLimited = true
	}
	for _, session := range sessions {
		if err = s.scanSession(ctx, session, now, &result); err != nil {
			return result, err
		}
	}
	if err = s.prune(ctx); err != nil {
		return result, err
	}
	// A budget-limited pass only saw part of the sessions directory, so it cannot
	// prove any session outside the window was deleted.
	if !result.BudgetLimited {
		if err = s.pruneMissing(ctx, home, sessions); err != nil {
			return result, err
		}
	}
	return result, nil
}

// ScanResult reports what one scan did, for diagnostics and tests.
type ScanResult struct {
	Sessions      int
	Files         int
	Steps         int
	Restarts      int
	Skipped       bool
	BudgetLimited bool
}

func (s *Store) scanSession(ctx context.Context, session sessionDir, now time.Time, result *ScanResult) error {
	steps := []Step{}
	openBackground := 0
	blocked := false
	lastUserPrompt := int64(0)
	agentCount := 0
	for _, agent := range session.Agents {
		path := session.Dir + "/agents/" + agent + "/wire.jsonl"
		state := s.fileStateFor(path, agent)
		read, restarted, err := readIncremental(path, state)
		if err != nil {
			continue
		}
		result.Files++
		if restarted {
			result.Restarts++
		}
		steps = append(steps, read...)
		agentCount++
		if agent == "main" {
			// A background subagent is open if either signal says so. subagent.spawned
			// with runInBackground is the authoritative start; task.started covers the
			// detached process/question tasks. Taking the max keeps one signal from
			// hiding the other.
			openBackground = state.parser.openTasks
			if count := len(state.parser.backgroundSubagents); count > openBackground {
				openBackground = count
			}
			blocked = state.parser.blocked
			lastUserPrompt = state.parser.lastUserPrompt
		}
	}
	if len(steps) > 0 {
		if err := s.insertSteps(ctx, session.ID, steps); err != nil {
			return err
		}
		result.Steps += len(steps)
	}
	if err := s.upsertSession(ctx, session, agentCount, now); err != nil {
		return err
	}
	return s.upsertLive(ctx, session.ID, openBackground, blocked, lastUserPrompt, now)
}

func (s *Store) insertSteps(ctx context.Context, sessionID string, steps []Step) error {
	return storage.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		statement, err := tx.PrepareContext(ctx, `INSERT INTO cache_steps(session_id,agent,step_uuid,model,time,turn_id,step_index,finish_reason,input_other,input_cache_read,input_cache_creation,output,first_token_ms,decode_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(session_id,agent,step_uuid) DO NOTHING`)
		if err != nil {
			return err
		}
		defer statement.Close()
		for _, step := range steps {
			if step.At.IsZero() {
				continue
			}
			if _, err = statement.ExecContext(ctx, sessionID, step.Agent, step.StepUUID, step.Model, storage.SortableTime(step.At), step.TurnID, step.StepIndex, step.Finish, step.InputOther, step.InputCacheRead, step.InputCacheCreation, step.Output, step.FirstTokenMS, step.DecodeMS); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) upsertSession(ctx context.Context, session sessionDir, agents int, now time.Time) error {
	created, updated := "", ""
	if session.Meta.CreatedAt > 0 {
		created = storage.SortableTime(millis(session.Meta.CreatedAt))
	}
	if session.Meta.UpdatedAt > 0 {
		updated = storage.SortableTime(millis(session.Meta.UpdatedAt))
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO cache_sessions(session_id,cwd,title,created_at,updated_at,agent_count,scanned_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(session_id) DO UPDATE SET cwd=excluded.cwd,title=excluded.title,created_at=excluded.created_at,updated_at=excluded.updated_at,agent_count=excluded.agent_count,scanned_at=excluded.scanned_at`,
		session.ID, session.Meta.CWD, session.Meta.Title, created, updated, agents, storage.SortableTime(now))
	return err
}

func (s *Store) upsertLive(ctx context.Context, sessionID string, openBackground int, blocked bool, lastUserPrompt int64, now time.Time) error {
	prompt := ""
	if lastUserPrompt > 0 {
		prompt = storage.SortableTime(millis(lastUserPrompt))
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO cache_live(session_id,open_background,blocked,last_user_prompt,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(session_id) DO UPDATE SET open_background=excluded.open_background,blocked=excluded.blocked,last_user_prompt=excluded.last_user_prompt,updated_at=excluded.updated_at`,
		sessionID, openBackground, boolInt(blocked), prompt, storage.SortableTime(now))
	return err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// prune drops step rows older than the longest history range so the database
// cannot grow without bound. 30 days matches cache.history's longest range.
func (s *Store) prune(ctx context.Context) error {
	cutoff := storage.SortableTime(time.Now().UTC().Add(-31 * 24 * time.Hour))
	_, err := s.db.ExecContext(ctx, `DELETE FROM cache_steps WHERE time < ?`, cutoff)
	return err
}

// pruneMissing deletes the rows of sessions that no longer exist on disk. It must
// only run after a complete discovery pass: when a scan is budget-limited, the
// session list says nothing about the sessions outside it.
func (s *Store) pruneMissing(ctx context.Context, home string, sessions []sessionDir) error {
	// A missing sessions root is not evidence that every session was deleted; it is
	// usually a wrong or not-yet-populated KIMI_CODE_HOME, and the derived history
	// must survive that.
	root := filepath.Join(home, "sessions")
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil
	}
	live := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		live[session.ID] = true
	}
	rows, err := s.db.QueryContext(ctx, `SELECT session_id FROM cache_sessions UNION SELECT session_id FROM cache_steps UNION SELECT session_id FROM cache_live`)
	if err != nil {
		return err
	}
	stale := []string{}
	for rows.Next() {
		var sessionID string
		if err = rows.Scan(&sessionID); err != nil {
			rows.Close()
			return err
		}
		if !live[sessionID] {
			stale = append(stale, sessionID)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	// The pool holds a single connection, so the cursor must be closed before the
	// delete transaction can take it.
	rows.Close()
	if len(stale) > 0 {
		if err = storage.Transaction(ctx, s.db, func(tx *sql.Tx) error {
			for _, table := range []string{"cache_steps", "cache_sessions", "cache_live"} {
				if err := deleteSessionRows(ctx, tx, table, stale); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	s.forgetStaleFiles(sessions)
	return nil
}

func deleteSessionRows(ctx context.Context, tx *sql.Tx, table string, sessionIDs []string) error {
	statement, err := tx.PrepareContext(ctx, `DELETE FROM `+table+` WHERE session_id=?`)
	if err != nil {
		return err
	}
	defer statement.Close()
	for _, sessionID := range sessionIDs {
		if _, err = statement.ExecContext(ctx, sessionID); err != nil {
			return err
		}
	}
	return nil
}

// forgetStaleFiles drops incremental readers the discovered sessions no longer
// cover: without it a deleted session keeps a parser and its pending usage list
// alive forever.
func (s *Store) forgetStaleFiles(sessions []sessionDir) {
	live := make(map[string]bool)
	for _, session := range sessions {
		for _, agent := range session.Agents {
			live[session.Dir+"/agents/"+agent+"/wire.jsonl"] = true
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for path := range s.files {
		if !live[path] {
			delete(s.files, path)
		}
	}
}

// LiveSignals is what keepalive needs about one session's current state.
type LiveSignals struct {
	OpenBackground int
	Blocked        bool
	LastUserPrompt time.Time
}

// Live reads the volatile signals recorded by the last scan.
func (s *Store) Live(ctx context.Context, sessionID string) (LiveSignals, error) {
	signals := LiveSignals{}
	var open, blocked int
	var prompt string
	err := s.db.QueryRowContext(ctx, `SELECT open_background,blocked,last_user_prompt FROM cache_live WHERE session_id=?`, sessionID).Scan(&open, &blocked, &prompt)
	if errors.Is(err, sql.ErrNoRows) {
		return signals, nil
	}
	if err != nil {
		return signals, err
	}
	signals.OpenBackground = open
	signals.Blocked = blocked == 1
	if prompt != "" {
		signals.LastUserPrompt, _ = time.Parse(time.RFC3339Nano, prompt)
	}
	return signals, nil
}

// LastMainStep returns the newest persisted main-agent step for a session. It is
// the anchor keepalive uses both for "time since the main agent last called a
// model" and for verifying the next step's input_cache_read.
func (s *Store) LastMainStep(ctx context.Context, sessionID string) (Step, bool, error) {
	step := Step{}
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT session_id,agent,step_uuid,model,time,turn_id,input_other,input_cache_read,input_cache_creation,output,first_token_ms,decode_ms FROM cache_steps WHERE session_id=? AND agent='main' ORDER BY time DESC,rowid DESC LIMIT 1`, sessionID).
		Scan(&step.SessionID, &step.Agent, &step.StepUUID, &step.Model, &at, &step.TurnID, &step.InputOther, &step.InputCacheRead, &step.InputCacheCreation, &step.Output, &step.FirstTokenMS, &step.DecodeMS)
	if errors.Is(err, sql.ErrNoRows) {
		return step, false, nil
	}
	if err != nil {
		return step, false, err
	}
	step.At, _ = time.Parse(time.RFC3339Nano, at)
	return step, true, nil
}

// StepAfter returns the first main-agent step newer than after. keepalive uses it
// to read the step that followed a keepalive message.
func (s *Store) StepAfter(ctx context.Context, sessionID string, after time.Time) (Step, bool, error) {
	step := Step{}
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT session_id,agent,step_uuid,model,time,turn_id,input_other,input_cache_read,input_cache_creation,output,first_token_ms,decode_ms FROM cache_steps WHERE session_id=? AND agent='main' AND time>? ORDER BY time ASC,rowid ASC LIMIT 1`, sessionID, storage.SortableTime(after)).
		Scan(&step.SessionID, &step.Agent, &step.StepUUID, &step.Model, &at, &step.TurnID, &step.InputOther, &step.InputCacheRead, &step.InputCacheCreation, &step.Output, &step.FirstTokenMS, &step.DecodeMS)
	if errors.Is(err, sql.ErrNoRows) {
		return step, false, nil
	}
	if err != nil {
		return step, false, err
	}
	step.At, _ = time.Parse(time.RFC3339Nano, at)
	return step, true, nil
}

func (s *Store) sessionTitle(ctx context.Context, sessionID string) string {
	var title string
	if err := s.db.QueryRowContext(ctx, `SELECT title FROM cache_sessions WHERE session_id=?`, sessionID).Scan(&title); err != nil {
		return ""
	}
	return title
}

var errNoDatabase = errors.New("缓存数据层未初始化")

func (s *Store) ready() error {
	if s == nil || s.db == nil {
		return errNoDatabase
	}
	return nil
}

func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return at
}

// MainSteps returns every persisted main-agent step for a session in time order.
// Verification uses it to compare the stored set against the raw record file.
func (s *Store) MainSteps(ctx context.Context, sessionID string) ([]Step, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT session_id,agent,step_uuid,model,time,input_other,input_cache_read,input_cache_creation,output,first_token_ms,decode_ms FROM cache_steps WHERE session_id=? AND agent='main' ORDER BY time ASC,rowid ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	steps := []Step{}
	for rows.Next() {
		step := Step{}
		var at string
		if err = rows.Scan(&step.SessionID, &step.Agent, &step.StepUUID, &step.Model, &at, &step.InputOther, &step.InputCacheRead, &step.InputCacheCreation, &step.Output, &step.FirstTokenMS, &step.DecodeMS); err != nil {
			return nil, err
		}
		step.At = parseTime(at)
		steps = append(steps, step)
	}
	return steps, rows.Err()
}
