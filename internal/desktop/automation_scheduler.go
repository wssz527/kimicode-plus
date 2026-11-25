package desktop

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"enhancedkimi/internal/cache"
	"time"

	"enhancedkimi/internal/security"
	"enhancedkimi/internal/storage"
)

const automationCooldown = 5 * time.Minute

type automationSnapshot struct {
	session      NativeSession
	revision     uint64
	lastActivity time.Time
	approvals    int
	quota        bool
	goal         json.RawMessage
	goalKnown    bool
}

func snapshotFromCache(cache cachedSession) automationSnapshot {
	quota := false
	if len(cache.turns) > 0 {
		last := cache.turns[len(cache.turns)-1]
		quota = last.Error != nil && last.Error.StatusCode == http.StatusTooManyRequests
	}
	return automationSnapshot{
		session: cache.session, revision: cache.revision, lastActivity: cache.lastActivity,
		approvals: len(turnApprovals(cache.turns)), quota: quota, goal: append(json.RawMessage(nil), cache.goal...), goalKnown: cache.goalKnown,
	}
}

func (s *NativeService) automationSnapshot(id string) (automationSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cache, ok := s.cache[id]
	if !ok {
		return automationSnapshot{}, false
	}
	return snapshotFromCache(cache), true
}

func (s *NativeService) automationSessionIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.cache))
	for id := range s.cache {
		ids = append(ids, id)
	}
	return ids
}

func (s *NativeService) touchSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cache, ok := s.cache[id]
	if !ok {
		return
	}
	cache.revision++
	cache.lastActivity = time.Now().UTC()
	s.cache[id] = cache
}

func contextRevision(snapshot automationSnapshot) string {
	if snapshot.session.UpdatedAt != "" {
		return snapshot.session.UpdatedAt
	}
	return "runtime:" + strconv.FormatUint(snapshot.revision, 10)
}

func goalBlocksAutomation(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var goal struct {
		Status string `json:"status"`
		Budget struct {
			RemainingTokens        *float64 `json:"remainingTokens"`
			RemainingTurns         *float64 `json:"remainingTurns"`
			RemainingWallClockMs   *float64 `json:"remainingWallClockMs"`
			TokenBudgetReached     bool     `json:"tokenBudgetReached"`
			TurnBudgetReached      bool     `json:"turnBudgetReached"`
			WallClockBudgetReached bool     `json:"wallClockBudgetReached"`
			OverBudget             bool     `json:"overBudget"`
		} `json:"budget"`
	}
	if json.Unmarshal(raw, &goal) != nil {
		return true
	}
	if goal.Status != "" && goal.Status != "active" {
		return true
	}
	budget := goal.Budget
	if budget.OverBudget || budget.TokenBudgetReached || budget.TurnBudgetReached || budget.WallClockBudgetReached {
		return true
	}
	for _, remaining := range []*float64{budget.RemainingTokens, budget.RemainingTurns, budget.RemainingWallClockMs} {
		if remaining != nil && *remaining <= 0 {
			return true
		}
	}
	return false
}

// goalAutomationBlocked reports whether a goal's budget forbids automation.
//
// An unread goal counts as blocking. That is the safe answer for recovery, which
// must not resume a spent budget: "cannot confirm the budget" has to mean "do not
// spend". Idle automation does not share that trade-off and uses goalBlocksIdle
// instead, because a cache entry created by the KAP session list has goalKnown=false
// until something fetches the goal, and treating that as blocked silently disabled
// keepalive and compaction for every such session with no receipt explaining why.
func goalAutomationBlocked(snapshot automationSnapshot) bool {
	return !snapshot.goalKnown || goalBlocksAutomation(snapshot.goal)
}

// goalBlocksIdle is the goal test for keepalive and compaction: only a goal that was
// actually read and is stopped or over budget forbids the action.
func goalBlocksIdle(snapshot automationSnapshot) bool {
	return snapshot.goalKnown && goalBlocksAutomation(snapshot.goal)
}

func pendingNativeInteraction(snapshot automationSnapshot) bool {
	pending := snapshot.session.PendingInteraction
	return snapshot.approvals > 0 || pending != "" && pending != "none"
}

func (s *NativeService) beginAutomation(ctx context.Context, kind, sessionID, revision string, now time.Time) (automationIntent, bool, error) {
	intent := automationIntent{
		ID: security.Random()[:24], Kind: kind, HostID: "native", SessionID: sessionID,
		ContextRevision: revision, State: "pending", CreatedAt: now, UpdatedAt: now,
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO automation_intents(id,kind,host_id,session_id,model_id,context_revision,state,created_at,updated_at,result) VALUES(?,?,?,?,?,?,?,?,?,'')`,
		intent.ID, intent.Kind, intent.HostID, intent.SessionID, intent.ModelID, intent.ContextRevision, intent.State, storage.SortableTime(intent.CreatedAt), storage.SortableTime(intent.UpdatedAt))
	if err != nil {
		return intent, false, err
	}
	count, err := result.RowsAffected()
	return intent, count == 1, err
}

func (s *NativeService) automationAllowed(ctx context.Context, kind, sessionID, revision string, now time.Time) (bool, error) {
	var previousRevision, state, updated string
	err := s.db.QueryRowContext(ctx, `SELECT context_revision,state,updated_at FROM automation_intents WHERE kind=? AND session_id=? ORDER BY created_at DESC,rowid DESC LIMIT 1`, kind, sessionID).Scan(&previousRevision, &state, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if kind == "compaction" && previousRevision == revision && state != "failed" {
		return false, nil
	}
	at, err := time.Parse(time.RFC3339Nano, updated)
	return err == nil && !now.Before(at.Add(automationCooldown)), err
}

func (s *NativeService) updateAutomation(ctx context.Context, id, state, result string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE automation_intents SET state=?,result=?,updated_at=? WHERE id=?`, state, result, storage.SortableTime(now), id)
	return err
}

func (s *NativeService) publishAutomationReceipt(kind, sessionID, state, note string) {
	s.bus.Publish("automation", map[string]any{"type": kind + "." + state, "kind": kind, "sessionId": sessionID, "state": state, "note": note})
}

func (s *NativeService) finishCompaction(frame NativeFrame) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM automation_intents WHERE kind='compaction' AND session_id=? AND state='acknowledged' ORDER BY updated_at DESC,rowid DESC LIMIT 1`, frame.SessionID).Scan(&id)
	if err != nil {
		return
	}
	state := map[string]string{"compaction.completed": "completed", "compaction.cancelled": "cancelled", "compaction.blocked": "blocked"}[frame.Type]
	note := map[string]string{"compaction.completed": "官方确认压缩完成", "compaction.cancelled": "官方取消了压缩，未重发", "compaction.blocked": "官方阻止了压缩，未重发"}[frame.Type]
	if s.updateAutomation(ctx, id, state, note, time.Now().UTC()) == nil {
		s.publishAutomationReceipt("compaction", frame.SessionID, state, note)
	}
}

func (s *NativeService) expireCompactions(ctx context.Context, now time.Time) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,session_id FROM automation_intents WHERE kind='compaction' AND state='acknowledged' AND updated_at<?`, storage.SortableTime(now.Add(-2*time.Minute)))
	if err != nil {
		return err
	}
	type expired struct{ id, sessionID string }
	items := []expired{}
	for rows.Next() {
		var item expired
		if err = rows.Scan(&item.id, &item.sessionID); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, item := range items {
		if err = s.updateAutomation(ctx, item.id, "unknown", "压缩 ACK 后两分钟内未收到完成事件，结果 UNKNOWN，未自动重发", now); err != nil {
			return err
		}
		s.publishAutomationReceipt("compaction", item.sessionID, "unknown", "未收到官方完成事件，结果 UNKNOWN")
	}
	return nil
}

func (s *NativeService) refreshAutomationSnapshot(ctx context.Context, id string, now time.Time) (automationSnapshot, bool, error) {
	current, ok := s.automationSnapshot(id)
	if !ok {
		return automationSnapshot{}, false, nil
	}
	loaded, err := s.readSession(ctx, id)
	if err != nil {
		return current, false, err
	}
	s.mu.Lock()
	latest, ok := s.cache[id]
	if !ok || latest.revision != current.revision {
		s.mu.Unlock()
		return automationSnapshot{}, false, nil
	}
	loaded.revision = latest.revision
	loaded.lastActivity = latest.lastActivity
	if loaded.session.UpdatedAt != latest.session.UpdatedAt {
		loaded.revision++
		loaded.lastActivity = now
	}
	s.cache[id] = loaded
	s.mu.Unlock()
	return snapshotFromCache(loaded), true, nil
}

func (s *NativeService) runCompaction(ctx context.Context, settings AutomationSettings, id string, now time.Time) {
	snapshot, ok := s.automationSnapshot(id)
	if !ok || sessionIsArchivedOrDone(snapshot.session) || goalBlocksIdle(snapshot) || compactionBlocked(snapshot, s.automationLive(ctx, snapshot)) || s.isIdleKeepAliveProtecting(ctx, settings, id, now) || now.Sub(snapshot.lastActivity) < time.Duration(settings.Compaction.IdleMinutes)*time.Minute {
		return
	}
	// Check the cheap local signals before refreshing: refreshAutomationSnapshot
	// makes official requests, and running it for every historical idle session
	// every tick caused a request storm.
	if !compactionContextOverThreshold(snapshot.session.Usage, settings.Compaction.ContextThreshold) {
		return
	}
	revision := contextRevision(snapshot)
	allowed, err := s.automationAllowed(ctx, "compaction", id, revision, now)
	if err != nil || !allowed {
		return
	}
	snapshot, ok, err = s.refreshAutomationSnapshot(ctx, id, now)
	if err != nil || !ok || sessionIsArchivedOrDone(snapshot.session) || goalBlocksIdle(snapshot) || compactionBlocked(snapshot, s.automationLive(ctx, snapshot)) || s.isIdleKeepAliveProtecting(ctx, settings, id, now) || now.Sub(snapshot.lastActivity) < time.Duration(settings.Compaction.IdleMinutes)*time.Minute {
		return
	}
	if !compactionContextOverThreshold(snapshot.session.Usage, settings.Compaction.ContextThreshold) {
		return
	}
	revision = contextRevision(snapshot)
	allowed, err = s.automationAllowed(ctx, "compaction", id, revision, now)
	if err != nil || !allowed {
		return
	}
	intent, _, err := s.beginAutomation(ctx, "compaction", id, revision, now)
	if err != nil {
		return
	}
	latest, ok := s.automationSnapshot(id)
	if !ok || latest.revision != snapshot.revision || latest.session.Busy || latest.session.MainTurnActive || pendingNativeInteraction(latest) || contextRevision(latest) != revision {
		_ = s.updateAutomation(ctx, intent.ID, "cancelled", "压缩前出现新活动或审批，候选已失效", now)
		return
	}
	if err = s.WatchSessionInternal(id); err != nil {
		_ = s.updateAutomation(ctx, intent.ID, "failed", "无法订阅压缩完成事件："+err.Error(), now)
		return
	}
	_, err = s.kap.Request(ctx, http.MethodPost, "/sessions/"+url.PathEscape(id)+":compact", map[string]any{})
	if err != nil {
		note := "官方未接受压缩请求：" + err.Error()
		_ = s.updateAutomation(ctx, intent.ID, "failed", note, now)
		s.publishAutomationReceipt("compaction", id, "failed", note)
		return
	}
	_ = s.updateAutomation(ctx, intent.ID, "acknowledged", "官方已接受压缩请求，等待完成事件", now)
	s.publishAutomationReceipt("compaction", id, "acknowledged", "ACK 不是完成，正在等待官方事件")
}

// compactionContextOverThreshold answers the local half of the compaction
// policy: does this session report a context ratio at or above the threshold?
// It reads only cached session data, so it can be checked before any refresh.
func compactionContextOverThreshold(usage *NativeUsage, threshold int) bool {
	if usage == nil || usage.ContextLimit == nil || *usage.ContextLimit <= 0 || usage.ContextTokens < 0 || threshold <= 0 {
		return false
	}
	return float64(usage.ContextTokens)/float64(*usage.ContextLimit)*100 >= float64(threshold)
}

func compactionBlocked(snapshot automationSnapshot, live cache.LiveSignals) bool {
	status := strings.ToLower(strings.TrimSpace(snapshot.session.Status))
	return snapshot.session.Busy || snapshot.session.MainTurnActive || status == "background" || status == "turn" || pendingNativeInteraction(snapshot) || live.OpenBackground > 0 || live.Blocked
}

func (s *NativeService) automationLive(ctx context.Context, snapshot automationSnapshot) cache.LiveSignals {
	// A daemon restart can leave a background counter open in the session log.
	if strings.EqualFold(strings.TrimSpace(snapshot.session.Status), "idle") {
		return cache.LiveSignals{}
	}
	store, err := s.cacheStore()
	if err != nil {
		return cache.LiveSignals{}
	}
	live, _ := store.Live(ctx, snapshot.session.ID)
	return live
}

func (s *NativeService) openBackground(ctx context.Context, id string) int {
	store, err := s.cacheStore()
	if err != nil {
		return 0
	}
	live, _ := store.Live(ctx, id)
	return live.OpenBackground
}

const automationIntentRetention = 30 * 24 * time.Hour

func (s *NativeService) pruneAutomationIntents(ctx context.Context, now time.Time) error {
	if !s.intentPrunedAt.IsZero() && now.Sub(s.intentPrunedAt) < time.Hour {
		return nil
	}
	// Recovery rows are persistent state. Old compaction revisions also prevent
	// repeating an already completed compaction until the session changes.
	_, err := s.db.ExecContext(ctx, `DELETE FROM automation_intents AS old
		WHERE old.updated_at < ? AND old.kind IN ('keepalive','idle_keepalive','compaction')
		AND (old.kind <> 'compaction' OR old.rowid <> (
			SELECT newer.rowid FROM automation_intents newer WHERE newer.kind=old.kind AND newer.session_id=old.session_id
			ORDER BY newer.created_at DESC,newer.rowid DESC LIMIT 1
		))`, storage.SortableTime(now.Add(-automationIntentRetention)))
	if err == nil {
		s.intentPrunedAt = now
	}
	return err
}

func (s *NativeService) syncAutomationSessions(ctx context.Context, now time.Time) error {
	s.mu.Lock()
	revisions := make(map[string]uint64, len(s.cache))
	for id, cache := range s.cache {
		revisions[id] = cache.revision
	}
	s.mu.Unlock()
	sessions, err := s.kap.Sessions(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, session := range sessions {
		activity := now
		if parsed, parseErr := time.Parse(time.RFC3339Nano, session.UpdatedAt); parseErr == nil {
			activity = parsed
		}
		cache, exists := s.cache[session.ID]
		if exists && cache.revision != revisions[session.ID] {
			continue
		}
		if !exists {
			s.cache[session.ID] = cachedSession{session: session, revision: 1, lastActivity: activity}
			continue
		}
		updated := session.UpdatedAt != cache.session.UpdatedAt
		stateChanged := session.Busy != cache.session.Busy || session.MainTurnActive != cache.session.MainTurnActive || session.PendingInteraction != cache.session.PendingInteraction
		cache.session = session
		if updated || stateChanged {
			cache.revision++
			if stateChanged && !updated {
				activity = now
			}
			cache.lastActivity = activity
		}
		s.cache[session.ID] = cache
	}
	return nil
}

func (s *NativeService) automationTick(ctx context.Context, now time.Time) error {
	s.automationMu.Lock()
	defer s.automationMu.Unlock()
	settings, err := s.automationSettings(ctx)
	if err != nil {
		return err
	}
	if s.isSafe() {
		return nil
	}
	if s.isAutomationPausedForSwitch() {
		return nil
	}
	var syncErr error
	if settings.IdleKeepAlive.Enabled || settings.Compaction.Enabled || settings.Recovery.Enabled {
		// Refresh the session-record metrics first: keepalive's clock and its
		// background-subagent condition both come from cache_steps/cache_live, so they
		// must be current before any policy decision this tick. Creating the store here
		// also means the scheduler works without waiting for the settings page to read
		// cache.summary once.
		if _, scanErr := s.cacheScan(ctx, now, false); scanErr != nil {
			syncErr = scanErr
		}
		if syncErr == nil {
			syncErr = s.syncAutomationSessions(ctx, now)
		}
	}
	if err = s.expireCompactions(ctx, now); err != nil {
		return err
	}
	if syncErr == nil && (settings.IdleKeepAlive.Enabled || settings.Compaction.Enabled) {
		for _, id := range s.automationSessionIDs() {
			if settings.IdleKeepAlive.Enabled {
				s.runIdleKeepAliveOnce(ctx, settings, id, now)
			}
			if settings.Compaction.Enabled {
				s.runCompaction(ctx, settings, id, now)
			}
		}
	}
	return errors.Join(syncErr, s.pruneAutomationIntents(ctx, now), s.recoveryTick(ctx, settings, now))
}

func (s *NativeService) runAutomation() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-ticker.C:
			ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
			if err := s.automationTick(ctx, now.UTC()); err != nil && ctx.Err() == nil {
				s.bus.Publish("automation", map[string]any{"type": "scheduler.error", "state": "failed", "note": err.Error()})
			}
			cancel()
		}
	}
}
