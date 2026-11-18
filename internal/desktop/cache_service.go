package desktop

import (
	"context"
	"net/http"
	"time"

	"enhancedkimi/internal/cache"
)

// cacheStore returns the cache store, creating it lazily so the desktop service
// still starts when the records directory does not exist yet (for example before
// the official client has ever run). It reuses the service's own *sql.DB: the
// store lives in the same SQLite file under its own schema module, so there is no
// second connection pool to keep consistent.
func (s *NativeService) cacheStore() (*cache.Store, error) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.cacheErr != nil {
		return nil, s.cacheErr
	}
	if s.store != nil {
		s.store.SetHome(s.kap.Home())
		return s.store, nil
	}
	store, err := cache.NewStore(s.db, s.kap.Home())
	if err != nil {
		s.cacheErr = err
		return nil, err
	}
	s.store = store
	return store, nil
}

// cacheScan refreshes cache_steps from the official records. It is called on the
// automation tick, which is already at the 5 second floor the plan requires.
func (s *NativeService) cacheScan(ctx context.Context, now time.Time, force bool) (cache.ScanResult, error) {
	store, err := s.cacheStore()
	if err != nil {
		return cache.ScanResult{}, err
	}
	return store.Scan(ctx, now, force)
}

// cacheSnapshot builds the payload the "缓存与额度" page needs in one round trip:
// the summary plus quota state, sessions, and the current policy settings.
func (s *NativeService) cacheSummary(ctx context.Context, now time.Time) (cache.Summary, error) {
	store, err := s.cacheStore()
	if err != nil {
		return cache.Summary{}, err
	}
	if _, err = store.Scan(ctx, now, false); err != nil {
		return cache.Summary{}, err
	}
	summary, err := store.Summary(ctx, now)
	if err != nil {
		return cache.Summary{}, err
	}
	summary.Quota = s.quotaState(ctx)
	return summary, nil
}

// quotaState reads the official allowance. Anything unusable stays UNKNOWN rather
// than being reported as zero.
func (s *NativeService) quotaState(ctx context.Context) cache.Quota {
	raw, err := s.kap.Request(ctx, http.MethodGet, "/oauth/usage", nil)
	if err != nil {
		return cache.Quota{State: "UNKNOWN", Source: "官方 /oauth/usage", Note: "额度暂不可用，稍后重试"}
	}
	value := rawAsAny(raw)
	usage, parseErr := parseManagedUsage(raw)
	if parseErr != nil {
		return cache.Quota{State: "UNKNOWN", Source: "官方 /oauth/usage", Note: "额度响应格式无法识别", Value: value}
	}
	available, resetAt, known := managedQuotaAvailability(usage, time.Now().UTC())
	if !known {
		return cache.Quota{State: "UNKNOWN", Source: "官方 /oauth/usage", Note: "官方未返回可用的额度窗口", Value: value}
	}
	quota := cache.Quota{State: "AVAILABLE", Source: "官方 /oauth/usage", Value: value}
	if !available {
		quota.State = "EXHAUSTED"
		quota.Note = "额度已用尽，重置时间 " + resetAt.Format("15:04")
	}
	return quota
}

func (s *NativeService) cacheSessions(ctx context.Context, now time.Time, limit int) ([]cache.SessionRow, error) {
	store, err := s.cacheStore()
	if err != nil {
		return nil, err
	}
	if _, err = store.Scan(ctx, now, false); err != nil {
		return nil, err
	}
	return store.Sessions(ctx, now, limit)
}

func (s *NativeService) cacheHistory(ctx context.Context, now time.Time, span string) (cache.History, error) {
	store, err := s.cacheStore()
	if err != nil {
		return cache.History{}, err
	}
	if _, err = store.Scan(ctx, now, false); err != nil {
		return cache.History{}, err
	}
	return store.History(ctx, now, span)
}

// cacheTurns answers `cache.turns`: the newest completed steps of one session,
// which the panel draws as a per-turn line. It rescans first for the same reason
// the other cache reads do: a step written after the last tick must still appear.
func (s *NativeService) cacheTurns(ctx context.Context, now time.Time, sessionID string, limit int, before time.Time) ([]cache.TurnStep, error) {
	store, err := s.cacheStore()
	if err != nil {
		return nil, err
	}
	if _, err = store.Scan(ctx, now, false); err != nil {
		return nil, err
	}
	return store.TurnSteps(ctx, sessionID, limit, before)
}

func (s *NativeService) cacheRetention(ctx context.Context, now time.Time) (cache.Retention, error) {
	store, err := s.cacheStore()
	if err != nil {
		return cache.Retention{}, err
	}
	if _, err = store.Scan(ctx, now, false); err != nil {
		return cache.Retention{}, err
	}
	return store.Retention(ctx, now)
}

// cacheReceipts lists recent automation executions, which is what the page's
// "最近执行记录" group shows. Keepalive, compaction and recovery are reported with
// their distinct states so "官方已接收" is never confused with "官方已完成".
func (s *NativeService) cacheReceipts(ctx context.Context, limit int) ([]automationReceipt, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT kind,session_id,state,result,updated_at FROM automation_intents ORDER BY updated_at DESC,rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	receipts := []automationReceipt{}
	for rows.Next() {
		item := automationReceipt{}
		if err = rows.Scan(&item.Kind, &item.SessionID, &item.State, &item.Note, &item.At); err != nil {
			return nil, err
		}
		item.SessionTitle = s.cachedTitle(item.SessionID)
		receipts = append(receipts, item)
	}
	return receipts, rows.Err()
}

// automationReceipt is one execution record. State is deliberately explicit about
// the difference between an ACK and a completion (plan M2 section 5).
type automationReceipt struct {
	Kind         string `json:"kind"`
	SessionID    string `json:"sessionId"`
	SessionTitle string `json:"sessionTitle,omitempty"`
	State        string `json:"state"`
	Note         string `json:"note"`
	At           string `json:"at"`
}

func (s *NativeService) cachedTitle(sessionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cache, ok := s.cache[sessionID]; ok {
		return cache.session.Title
	}
	return ""
}

func parseCacheLimit(input map[string]any) int {
	raw, ok := input["limit"].(float64)
	if !ok || raw <= 0 || raw > 100 {
		return 20
	}
	return int(raw)
}
