package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"enhancedkimi/internal/storage"
)

var ErrNotFound = errors.New("定时任务或运行记录不存在")

var migrations = []string{
	`CREATE TABLE IF NOT EXISTS scheduler_tasks (
		id TEXT PRIMARY KEY,
		plugin_id TEXT NOT NULL,
		name TEXT NOT NULL,
		prompt TEXT NOT NULL,
		cwd TEXT NOT NULL,
		mode TEXT NOT NULL,
		session_id TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		effort TEXT NOT NULL DEFAULT '',
		permission_mode TEXT NOT NULL DEFAULT 'manual',
		schedule_json TEXT NOT NULL,
		missed_policy TEXT NOT NULL DEFAULT 'catchup',
		enabled INTEGER NOT NULL DEFAULT 1,
		next_run_at TEXT NOT NULL DEFAULT '',
		last_run_at TEXT NOT NULL DEFAULT '',
		last_status TEXT NOT NULL DEFAULT '',
		last_summary TEXT NOT NULL DEFAULT '',
		last_session_id TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS scheduler_tasks_due ON scheduler_tasks(plugin_id, enabled, next_run_at)`,
	`CREATE TABLE IF NOT EXISTS scheduler_runs (
		id TEXT PRIMARY KEY,
		task_id TEXT NOT NULL,
		plugin_id TEXT NOT NULL,
		task_name TEXT NOT NULL,
		trigger_kind TEXT NOT NULL,
		mode TEXT NOT NULL,
		session_id TEXT NOT NULL DEFAULT '',
		session_title TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		effort TEXT NOT NULL DEFAULT '',
		permission_mode TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,
		message TEXT NOT NULL DEFAULT '',
		unread INTEGER NOT NULL DEFAULT 1,
		scheduled_for TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL,
		finished_at TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS scheduler_runs_recent ON scheduler_runs(plugin_id, started_at DESC)`,
	`CREATE INDEX IF NOT EXISTS scheduler_runs_task ON scheduler_runs(task_id, started_at DESC)`,
}

type Store struct {
	db      *sql.DB
	prefsMu sync.Mutex
	prefs   map[string]Prefs
}

func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("scheduler store requires a database")
	}
	if err := storage.Migrate(db, "scheduler", migrations); err != nil {
		return nil, fmt.Errorf("scheduler migrations: %w", err)
	}
	return &Store{db: db, prefs: map[string]Prefs{}}, nil
}

func (s *Store) SaveTask(ctx context.Context, task Task) error {
	raw, err := json.Marshal(task.Schedule)
	if err != nil {
		return err
	}
	enabled := 0
	if task.Enabled {
		enabled = 1
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO scheduler_tasks(
		id, plugin_id, name, prompt, cwd, mode, session_id, model, effort, permission_mode,
		schedule_json, missed_policy, enabled, next_run_at, last_run_at, last_status, last_summary, last_session_id,
		created_at, updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(id) DO UPDATE SET
		name=excluded.name,
		prompt=excluded.prompt,
		cwd=excluded.cwd,
		mode=excluded.mode,
		session_id=excluded.session_id,
		model=excluded.model,
		effort=excluded.effort,
		permission_mode=excluded.permission_mode,
		schedule_json=excluded.schedule_json,
		missed_policy=excluded.missed_policy,
		enabled=excluded.enabled,
		next_run_at=excluded.next_run_at,
		last_run_at=excluded.last_run_at,
		last_status=excluded.last_status,
		last_summary=excluded.last_summary,
		last_session_id=excluded.last_session_id,
		updated_at=excluded.updated_at`,
		task.ID, task.PluginID, task.Name, task.Prompt, task.CWD, task.Mode, task.SessionID,
		task.Model, task.Effort, task.PermissionMode, string(raw), task.MissedPolicy, enabled,
		task.NextRunAt, task.LastRunAt, task.LastStatus, task.LastSummary, task.LastSessionID,
		task.CreatedAt, task.UpdatedAt,
	)
	return err
}

func (s *Store) GetTask(ctx context.Context, pluginID, id string) (Task, error) {
	row := s.db.QueryRowContext(ctx, `SELECT
		id, plugin_id, name, prompt, cwd, mode, session_id, model, effort, permission_mode,
		schedule_json, missed_policy, enabled, next_run_at, last_run_at, last_status, last_summary, last_session_id,
		created_at, updated_at
		FROM scheduler_tasks WHERE id=? AND (?='' OR plugin_id=?)`, id, pluginID, pluginID)
	task, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return task, err
}

func (s *Store) ListTasks(ctx context.Context, pluginID string) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, plugin_id, name, prompt, cwd, mode, session_id, model, effort, permission_mode,
		schedule_json, missed_policy, enabled, next_run_at, last_run_at, last_status, last_summary, last_session_id,
		created_at, updated_at
		FROM scheduler_tasks WHERE (?='' OR plugin_id=?) ORDER BY created_at DESC, rowid DESC`, pluginID, pluginID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

func (s *Store) DueTasks(ctx context.Context, now time.Time) ([]Task, error) {
	cutoff := storage.SortableTime(now)
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, plugin_id, name, prompt, cwd, mode, session_id, model, effort, permission_mode,
		schedule_json, missed_policy, enabled, next_run_at, last_run_at, last_status, last_summary, last_session_id,
		created_at, updated_at
		FROM scheduler_tasks WHERE enabled=1 AND next_run_at<>'' AND next_run_at<=?
		ORDER BY next_run_at ASC, rowid ASC`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

func (s *Store) UpdateTaskSchedule(ctx context.Context, id string, enabled bool, nextRunAt string, updatedAt time.Time) error {
	flag := 0
	if enabled {
		flag = 1
	}
	res, err := s.db.ExecContext(ctx, `UPDATE scheduler_tasks SET enabled=?, next_run_at=?, updated_at=? WHERE id=?`,
		flag, nextRunAt, storage.SortableTime(updatedAt), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateTaskLastResult(ctx context.Context, id, lastRunAt, lastStatus, lastSummary, lastSessionID string, updatedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE scheduler_tasks SET
		last_run_at=?, last_status=?, last_summary=?, last_session_id=CASE WHEN ?<>'' THEN ? ELSE last_session_id END, updated_at=?
		WHERE id=?`,
		lastRunAt, lastStatus, lastSummary, lastSessionID, lastSessionID, storage.SortableTime(updatedAt), id)
	return err
}

func (s *Store) DeleteTask(ctx context.Context, pluginID, id string) error {
	return storage.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM scheduler_tasks WHERE id=? AND plugin_id=?`, id, pluginID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM scheduler_runs WHERE task_id=? AND plugin_id=?`, id, pluginID)
		return err
	})
}

func (s *Store) DeletePluginData(ctx context.Context, pluginID string) error {
	err := storage.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM scheduler_runs WHERE plugin_id=?`, pluginID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM scheduler_tasks WHERE plugin_id=?`, pluginID)
		return err
	})
	if err != nil {
		return err
	}
	s.prefsMu.Lock()
	delete(s.prefs, pluginID)
	s.prefsMu.Unlock()
	return nil
}

func (s *Store) InsertRun(ctx context.Context, run RunRecord) error {
	unread := 0
	if run.Unread {
		unread = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO scheduler_runs(
		id, task_id, plugin_id, task_name, trigger_kind, mode, session_id, session_title,
		model, effort, permission_mode, status, message, unread, scheduled_for, started_at, finished_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		run.ID, run.TaskID, run.PluginID, run.TaskName, run.TriggerKind, run.Mode, run.SessionID, run.SessionTitle,
		run.Model, run.Effort, run.PermissionMode, run.Status, run.Message, unread, run.ScheduledFor, run.StartedAt, run.FinishedAt,
	)
	return err
}

func (s *Store) UpdateRun(ctx context.Context, run RunRecord) error {
	unread := 0
	if run.Unread {
		unread = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE scheduler_runs SET
		session_id=?, session_title=?, status=?, message=?, unread=?, finished_at=?
		WHERE id=?`,
		run.SessionID, run.SessionTitle, run.Status, run.Message, unread, run.FinishedAt, run.ID)
	return err
}

func (s *Store) ListRuns(ctx context.Context, pluginID, taskID string, limit int) ([]RunRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, task_id, plugin_id, task_name, trigger_kind, mode, session_id, session_title,
		model, effort, permission_mode, status, message, unread, scheduled_for, started_at, finished_at
		FROM scheduler_runs
		WHERE plugin_id=? AND (?='' OR task_id=?)
		ORDER BY started_at DESC, rowid DESC
		LIMIT ?`, pluginID, taskID, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunRecord{}
	for rows.Next() {
		item, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) UnreadCount(ctx context.Context, pluginID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduler_runs WHERE plugin_id=? AND unread=1`, pluginID).Scan(&count)
	return count, err
}

func (s *Store) MarkRunRead(ctx context.Context, pluginID, runID string, read bool) error {
	unread := 1
	if read {
		unread = 0
	}
	res, err := s.db.ExecContext(ctx, `UPDATE scheduler_runs SET unread=? WHERE id=? AND plugin_id=?`, unread, runID, pluginID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) MarkAllRead(ctx context.Context, pluginID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE scheduler_runs SET unread=0 WHERE plugin_id=? AND unread=1`, pluginID)
	return err
}

func (s *Store) RecoverInFlight(ctx context.Context, now time.Time) (int64, error) {
	finished := storage.SortableTime(now)
	res, err := s.db.ExecContext(ctx, `UPDATE scheduler_runs
		SET status=?, message=CASE WHEN message='' THEN ? ELSE message || '；核心重启，运行已中断' END, finished_at=?, unread=1
		WHERE status IN (?,?)`,
		RunFailed, "核心重启，上次运行已中断", finished, RunQueued, RunRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) GetPrefs(ctx context.Context, pluginID string) Prefs {
	if s.hasPluginStorage(ctx) {
		var raw []byte
		err := s.db.QueryRowContext(ctx, `SELECT value FROM plugin_storage WHERE plugin_id=? AND key='scheduler.prefs'`, pluginID).Scan(&raw)
		if err == nil {
			var prefs Prefs
			if json.Unmarshal(raw, &prefs) == nil {
				return prefs
			}
		}
	}
	s.prefsMu.Lock()
	defer s.prefsMu.Unlock()
	return s.prefs[pluginID]
}

func (s *Store) SetPrefs(ctx context.Context, pluginID string, prefs Prefs) error {
	s.prefsMu.Lock()
	s.prefs[pluginID] = prefs
	s.prefsMu.Unlock()
	if s.hasPluginStorage(ctx) {
		data, err := json.Marshal(prefs)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO plugin_storage(plugin_id,key,value,updated_at) VALUES(?,?,?,?)
			ON CONFLICT(plugin_id,key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`,
			pluginID, "scheduler.prefs", data, storage.SortableTime(time.Now().UTC()))
		return err
	}
	return nil
}

func (s *Store) hasPluginStorage(ctx context.Context) bool {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='plugin_storage'`).Scan(&name)
	return err == nil && name == "plugin_storage"
}

type scanner interface {
	Scan(dest ...any) error
}

func scanTask(row scanner) (Task, error) {
	var task Task
	var scheduleRaw string
	var enabled int
	err := row.Scan(
		&task.ID, &task.PluginID, &task.Name, &task.Prompt, &task.CWD, &task.Mode,
		&task.SessionID, &task.Model, &task.Effort, &task.PermissionMode,
		&scheduleRaw, &task.MissedPolicy, &enabled, &task.NextRunAt, &task.LastRunAt,
		&task.LastStatus, &task.LastSummary, &task.LastSessionID, &task.CreatedAt, &task.UpdatedAt,
	)
	if err != nil {
		return Task{}, err
	}
	task.Enabled = enabled == 1
	if strings.TrimSpace(scheduleRaw) != "" {
		if err := json.Unmarshal([]byte(scheduleRaw), &task.Schedule); err != nil {
			return Task{}, fmt.Errorf("task %s schedule is unreadable: %w", task.ID, err)
		}
	}
	return task, nil
}

func scanRun(row scanner) (RunRecord, error) {
	var run RunRecord
	var unread int
	err := row.Scan(
		&run.ID, &run.TaskID, &run.PluginID, &run.TaskName, &run.TriggerKind, &run.Mode,
		&run.SessionID, &run.SessionTitle, &run.Model, &run.Effort, &run.PermissionMode,
		&run.Status, &run.Message, &unread, &run.ScheduledFor, &run.StartedAt, &run.FinishedAt,
	)
	if err != nil {
		return RunRecord{}, err
	}
	run.Unread = unread == 1
	return run, nil
}
