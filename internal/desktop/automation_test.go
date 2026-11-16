package desktop

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enhancedkimi/internal/storage"
)

func writeNativeTestEnvelope(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "", "data": value})
}

func nativeTestSession(id string, contextTokens int64, contextLimit *int64) map[string]any {
	usage := map[string]any{"context_tokens": contextTokens}
	if contextLimit != nil {
		usage["context_limit"] = *contextLimit
	}
	return map[string]any{
		"id": id, "title": "合成会话", "updated_at": "2026-11-16T00:00:00Z",
		"busy": false, "main_turn_active": false, "pending_interaction": "none",
		"usage": usage, "metadata": map[string]any{"cwd": "/tmp/synthetic"},
	}
}

func newAutomationTestService(t *testing.T, handler http.Handler) (*NativeService, *sql.DB) {
	t.Helper()
	return newAutomationTestServiceWithHome(t, handler, "")
}

// newAutomationTestServiceWithHome injects the isolated KIMI_CODE_HOME before the
// service starts. The client's home must not be written after construction: the
// background event loop and the automation scheduler are already running and read it,
// so a post-start assignment is a data race and, with -race, a test failure.
func newAutomationTestServiceWithHome(t *testing.T, handler http.Handler, home string) (*NativeService, *sql.DB) {
	t.Helper()
	server := httptest.NewServer(handler)
	kap := NewKAPClient(home)
	kap.base, kap.token = server.URL, "synthetic"
	db, err := storage.Open(filepath.Join(t.TempDir(), "automation.db"))
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	service, err := NewNativeService(db, kap, t.TempDir(), func() bool { return false })
	if err != nil {
		db.Close()
		server.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		service.Close()
		db.Close()
		server.Close()
	})
	return service, db
}

func automationTestSessionHandler(t *testing.T, session map[string]any, turns []NativeTurn, goal any, usage any, onPost func(string, map[string]any)) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			onPost(r.URL.Path, body)
			writeNativeTestEnvelope(w, map[string]any{})
			return
		}
		switch r.URL.Path {
		case "/api/v1/sessions":
			writeNativeTestEnvelope(w, map[string]any{"items": []any{session}})
		case "/api/v1/sessions/s1":
			writeNativeTestEnvelope(w, session)
		case "/api/v1/sessions/s1/agents/main/turns":
			writeNativeTestEnvelope(w, map[string]any{"turns": turns})
		case "/api/v1/sessions/s1/goal":
			writeNativeTestEnvelope(w, goal)
		case "/api/v1/oauth/usage":
			writeNativeTestEnvelope(w, usage)
		default:
			writeNativeTestEnvelope(w, map[string]any{})
		}
	})
}

func automationTestCache(session NativeSession, now time.Time) cachedSession {
	return cachedSession{session: session, turns: []NativeTurn{}, goalKnown: true, revision: 1, lastActivity: now.Add(-time.Hour)}
}

func TestManagedUsageMonitorRequiresStructuredOK(t *testing.T) {
	cases := []struct{ raw, want string }{
		{`{}`, "UNKNOWN"},
		{`{"kind":"error","message":"Authorization failed.","status":401}`, "ERROR"},
		{`{"kind":"ok"}`, "UNKNOWN"},
		{`{"kind":"ok","quota":{"usages":{"limit5h":{"usedRatio":0.3}}}}`, "AVAILABLE"},
	}
	for _, testCase := range cases {
		if got := managedUsageMonitor(json.RawMessage(testCase.raw))["state"]; got != testCase.want {
			t.Fatalf("usage %s produced %v, want %s", testCase.raw, got, testCase.want)
		}
	}
}

func TestManagedQuotaAvailabilityHonorsResetAndUnknown(t *testing.T) {
	now := time.Date(2026, 11, 16, 1, 0, 0, 0, time.UTC)
	var usage managedUsage
	usage.Kind = "ok"
	usage.Quota = &struct {
		Usages map[string]managedUsageWindow `json:"usages"`
	}{Usages: map[string]managedUsageWindow{}}
	if available, _, known := managedQuotaAvailability(usage, now); available || known {
		t.Fatal("empty quota windows must stay unknown")
	}
	ratio := 1.0
	usage.Quota.Usages["limit5h"] = managedUsageWindow{UsedRatio: &ratio, ResetAt: now.Add(time.Hour).Format(time.RFC3339)}
	if available, next, known := managedQuotaAvailability(usage, now); available || !known || !next.Equal(now.Add(time.Hour)) {
		t.Fatalf("future reset was not honored: %v %v %v", available, next, known)
	}
	usage.Quota.Usages["limit5h"] = managedUsageWindow{UsedRatio: &ratio, ResetAt: now.Add(-time.Minute).Format(time.RFC3339)}
	if available, _, known := managedQuotaAvailability(usage, now); !available || !known {
		t.Fatal("elapsed reset should permit a controlled retry")
	}
}

func TestCompactionUsesOfficialContextAndWaitsForCompletion(t *testing.T) {
	var compactions atomic.Int32
	limit := int64(1000)
	session := nativeTestSession("s1", 800, &limit)
	handler := automationTestSessionHandler(t, session, []NativeTurn{}, nil, nil, func(path string, body map[string]any) {
		if path == "/api/v1/sessions/s1:compact" {
			compactions.Add(1)
		}
	})
	service, _ := newAutomationTestService(t, handler)
	now := time.Date(2026, 11, 16, 1, 0, 0, 0, time.UTC)
	mapped, err := mapNativeSession(json.RawMessage(`{"id":"s1","title":"合成会话","updated_at":"2026-11-16T00:00:00Z","usage":{"context_tokens":800,"context_limit":1000}}`))
	if err != nil {
		t.Fatal(err)
	}
	service.cache["s1"] = automationTestCache(mapped, now)
	settings := defaultAutomationSettings()
	settings.Compaction.Enabled = true
	settings.Compaction.IdleMinutes = 5
	if err = service.storeAutomationSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if err = service.automationTick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if compactions.Load() != 1 {
		t.Fatalf("compact calls = %d", compactions.Load())
	}
	var state string
	if err = service.db.QueryRow(`SELECT state FROM automation_intents WHERE kind='compaction' AND session_id='s1'`).Scan(&state); err != nil || state != "acknowledged" {
		t.Fatalf("ACK was treated as completion: %s %v", state, err)
	}
	if err = service.automationTick(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if compactions.Load() != 1 {
		t.Fatal("same revision compacted twice")
	}
	var before string
	if err = service.db.QueryRow(`SELECT state FROM automation_intents WHERE kind='compaction' AND session_id='s1'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	t.Logf("state before completion = %s", before)
	service.applyFrame(NativeFrame{Type: "compaction.completed", SessionID: "s1"})
	if err = service.db.QueryRow(`SELECT state FROM automation_intents WHERE kind='compaction' AND session_id='s1'`).Scan(&state); err != nil || state != "completed" {
		t.Fatalf("completion event not persisted: %s %v", state, err)
	}
}

func TestCompactionWithUnknownContextNeverRequests(t *testing.T) {
	var calls atomic.Int32
	session := nativeTestSession("s1", 800, nil)
	handler := automationTestSessionHandler(t, session, []NativeTurn{}, nil, nil, func(path string, body map[string]any) { calls.Add(1) })
	service, _ := newAutomationTestService(t, handler)
	now := time.Now().UTC()
	mapped, _ := mapNativeSession(json.RawMessage(`{"id":"s1","updated_at":"2026-11-16T00:00:00Z","usage":{"context_tokens":800}}`))
	service.cache["s1"] = automationTestCache(mapped, now)
	settings := defaultAutomationSettings()
	settings.Compaction.Enabled = true
	settings.Compaction.IdleMinutes = 5
	if err := service.storeAutomationSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if err := service.automationTick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("missing context limit must not trigger compact")
	}
}

func TestCompactionRestartDoesNotReplayAcknowledged(t *testing.T) {
	var calls atomic.Int32
	limit := int64(1000)
	session := nativeTestSession("s1", 800, &limit)
	handler := automationTestSessionHandler(t, session, []NativeTurn{}, nil, nil, func(path string, body map[string]any) { calls.Add(1) })
	server := httptest.NewServer(handler)
	defer server.Close()
	db, err := storage.Open(filepath.Join(t.TempDir(), "restart.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	newService := func() *NativeService {
		kap := NewKAPClient("")
		kap.base, kap.token = server.URL, "synthetic"
		service, serviceErr := NewNativeService(db, kap, t.TempDir(), func() bool { return false })
		if serviceErr != nil {
			t.Fatal(serviceErr)
		}
		return service
	}
	service := newService()
	now := time.Now().UTC()
	mapped, _ := mapNativeSession(json.RawMessage(`{"id":"s1","updated_at":"2026-11-16T00:00:00Z","usage":{"context_tokens":800,"context_limit":1000}}`))
	service.cache["s1"] = automationTestCache(mapped, now)
	settings := defaultAutomationSettings()
	settings.Compaction.Enabled = true
	settings.Compaction.IdleMinutes = 5
	if err = service.storeAutomationSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if err = service.automationTick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	service.Close()
	restarted := newService()
	defer restarted.Close()
	restarted.cache["s1"] = automationTestCache(mapped, now)
	if err = restarted.automationTick(context.Background(), now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("restart replayed acknowledged compaction: %d", calls.Load())
	}
	var state string
	if err = db.QueryRow(`SELECT state FROM automation_intents WHERE kind='compaction'`).Scan(&state); err != nil || state != "unknown" {
		t.Fatalf("restart did not preserve UNKNOWN: %s %v", state, err)
	}
}

// TestKeepAliveSendsOnceAndPreservesMode keeps the original guarantees of the
// pre-M2 keepalive (send once, never change plan/tower mode, respect approvals)
// while satisfying the D8 preconditions: a running session with an open background
// subagent and a quiet main agent.
func TestKeepAliveSendsOnceAndPreservesMode(t *testing.T) {
	home := t.TempDir()
	keepAliveFixture{home: home, mainAge: 40 * time.Minute, background: true}.write(t, "synthetic/model")
	var prompts atomic.Int32
	var bodyMu sync.Mutex
	bodies := []map[string]any{}
	session := nativeTestSession("s1", 0, nil)
	session["busy"] = true
	session["main_turn_active"] = true
	handler := &syncHandler{session: session}
	handler.inner = automationTestSessionHandler(t, session, []NativeTurn{}, nil, nil, func(path string, body map[string]any) {
		if path == "/api/v1/sessions/s1/prompts" {
			prompts.Add(1)
			bodyMu.Lock()
			bodies = append(bodies, body)
			bodyMu.Unlock()
		}
	})
	service, _ := newAutomationTestServiceWithHome(t, handler, home)
	service.verifyPoll = 20 * time.Millisecond
	service.verifyWindow = 500 * time.Millisecond
	now := time.Now().UTC()
	mapped, _ := mapNativeSession(json.RawMessage(`{"id":"s1","updated_at":"2026-11-16T00:00:00Z","busy":true,"main_turn_active":true,"pending_interaction":"none"}`))
	service.cache["s1"] = automationTestCache(mapped, now)
	settings := defaultAutomationSettings()
	settings.KeepAlive.Enabled = true
	settings.KeepAlive.Minutes = 5
	if err := service.storeAutomationSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	// The session's own activity is recent, so only the main agent's request time can
	// justify a keepalive.
	service.mu.Lock()
	recent := service.cache["s1"]
	recent.lastActivity = now
	service.cache["s1"] = recent
	service.mu.Unlock()

	if err := service.automationTick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	// Wait for the asynchronous closed-loop verifier to settle the intent.
	waitForKeepAliveState(t, service, keepAliveInvalid)
	if prompts.Load() != 1 || len(bodies) != 1 {
		t.Fatalf("keepalive calls = %d", prompts.Load())
	}
	bodyMu.Lock()
	first := bodies[0]
	bodyMu.Unlock()
	if _, exists := first["plan_mode"]; exists {
		t.Fatal("keepalive changed plan mode")
	}
	if _, exists := first["tower_mode"]; exists {
		t.Fatal("keepalive changed tower mode")
	}
	// A pending approval must block the next candidate even after the cooldown. The
	// official session is the source of truth here: automationTick re-reads it, so the
	// approval has to be visible in the synthetic provider too.
	handler.setPending("approval")
	if err := service.automationTick(context.Background(), now.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if prompts.Load() != 1 {
		t.Fatal("pending approval did not block keepalive")
	}
}

type recoveryPlannerStub struct {
	thread  plannerRecoveryThread
	retries atomic.Int32
}

func (p *recoveryPlannerStub) PlannerThreads(context.Context) (any, error) {
	return []plannerRecoveryThread{p.thread}, nil
}
func (p *recoveryPlannerStub) PlannerModels(context.Context) (any, error) { return []any{}, nil }
func (p *recoveryPlannerStub) PlannerAction(_ context.Context, action string, payload json.RawMessage) (any, error) {
	if action == "planner.retry" {
		p.retries.Add(1)
		p.thread.Status = "running"
		p.thread.Revision++
	}
	return p.thread, nil
}

func TestPlannerRecoveryTickRetriesOnce(t *testing.T) {
	session := nativeTestSession("s1", 0, nil)
	handler := automationTestSessionHandler(t, session, []NativeTurn{}, nil, nil, func(path string, body map[string]any) {})
	service, _ := newAutomationTestService(t, handler)
	planner := &recoveryPlannerStub{thread: plannerRecoveryThread{ID: "p1", Title: "合成规划", Model: "synthetic", Status: "quota", Revision: 3}}
	service.UsePlanner(planner)
	settings := defaultAutomationSettings()
	settings.Recovery.Enabled = true
	if err := service.storeAutomationSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := service.automationTick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if planner.retries.Load() != 0 {
		t.Fatal("recovery retried before its scheduled interval")
	}
	if err := service.automationTick(context.Background(), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if planner.retries.Load() != 1 {
		t.Fatalf("planner retries = %d", planner.retries.Load())
	}
	if err := service.automationTick(context.Background(), now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if planner.retries.Load() != 1 {
		t.Fatal("planner recovery replayed an accepted retry")
	}
}

func TestRecoveryRestartDoesNotReplayPending(t *testing.T) {
	var prompts atomic.Int32
	session := nativeTestSession("s1", 0, nil)
	session["last_turn_reason"] = "failed"
	turns := []NativeTurn{{TurnID: "t1", Error: &NativeTurnError{StatusCode: 429}}}
	usage := map[string]any{"kind": "ok", "quota": map[string]any{"usages": map[string]any{"limit5h": map[string]any{"usedRatio": 0.1}}}}
	handler := automationTestSessionHandler(t, session, turns, nil, usage, func(path string, body map[string]any) { prompts.Add(1) })
	service, db := newAutomationTestService(t, handler)
	if _, err := service.watchRecovery(context.Background(), map[string]any{"id": "s1", "kind": "session"}); err != nil {
		t.Fatal(err)
	}
	settings, err := service.automationSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.Recovery.Enabled = true
	if err = service.storeAutomationSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if err = saveRecoveryIntent(context.Background(), service, "session", "s1", "", session["updated_at"].(string), "pending", "synthetic pending before restart", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	service.Close()
	restarted, err := NewNativeService(db, service.kap, t.TempDir(), func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err = restarted.automationTick(context.Background(), time.Now().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if prompts.Load() != 0 {
		t.Fatal("restart replayed pending recovery")
	}
	updated, err := restarted.automationSettings(context.Background())
	if err != nil || len(updated.Recovery.Entries) != 1 || updated.Recovery.Entries[0].Status != "UNKNOWN" {
		t.Fatalf("pending recovery did not remain UNKNOWN: %+v %v", updated.Recovery.Entries, err)
	}
}

func TestNativeRecoveryHonorsGoalBudget(t *testing.T) {
	var prompts atomic.Int32
	session := nativeTestSession("s1", 0, nil)
	session["last_turn_reason"] = "failed"
	turns := []NativeTurn{{TurnID: "t1", Error: &NativeTurnError{StatusCode: 429, Message: "quota"}}}
	goal := map[string]any{"status": "blocked", "budget": map[string]any{"overBudget": true}}
	handler := automationTestSessionHandler(t, session, turns, goal, nil, func(path string, body map[string]any) { prompts.Add(1) })
	service, _ := newAutomationTestService(t, handler)
	if _, err := service.watchRecovery(context.Background(), map[string]any{"id": "s1", "kind": "session"}); err != nil {
		t.Fatal(err)
	}
	settings, err := service.automationSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.Recovery.Enabled = true
	if err := service.storeAutomationSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if err := service.automationTick(context.Background(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if prompts.Load() != 0 {
		t.Fatal("recovery overrode goal hard budget")
	}
	updated, err := service.automationSettings(context.Background())
	if err != nil || len(updated.Recovery.Entries) != 1 || updated.Recovery.Entries[0].Status != "BLOCKED" {
		t.Fatalf("budget block not visible: %+v %v", updated.Recovery.Entries, err)
	}
}

func TestNativeRecoveryBlocksWhenGoalIsUnknown(t *testing.T) {
	var prompts atomic.Int32
	session := nativeTestSession("s1", 0, nil)
	session["last_turn_reason"] = "failed"
	turns := []NativeTurn{{TurnID: "t1", Error: &NativeTurnError{StatusCode: 429}}}
	base := automationTestSessionHandler(t, session, turns, nil, nil, func(path string, body map[string]any) { prompts.Add(1) })
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/sessions/s1/goal" {
			http.Error(w, "synthetic goal outage", http.StatusServiceUnavailable)
			return
		}
		base.ServeHTTP(w, r)
	})
	service, _ := newAutomationTestService(t, handler)
	if _, err := service.watchRecovery(context.Background(), map[string]any{"id": "s1", "kind": "session"}); err != nil {
		t.Fatal(err)
	}
	settings, err := service.automationSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.Recovery.Enabled = true
	if err = service.storeAutomationSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if err = service.automationTick(context.Background(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if prompts.Load() != 0 {
		t.Fatal("unknown goal budget did not block recovery")
	}
	updated, err := service.automationSettings(context.Background())
	if err != nil || len(updated.Recovery.Entries) != 1 || updated.Recovery.Entries[0].Status != "BLOCKED" {
		t.Fatalf("unknown goal was not visible: %+v %v", updated.Recovery.Entries, err)
	}
}

func TestRecoveryUnwatchSuppressesSameRevisionUntilExplicitWatch(t *testing.T) {
	session := nativeTestSession("s1", 0, nil)
	session["last_turn_reason"] = "failed"
	turns := []NativeTurn{{TurnID: "t1", Error: &NativeTurnError{StatusCode: 429}}}
	handler := automationTestSessionHandler(t, session, turns, nil, nil, func(string, map[string]any) {})
	service, _ := newAutomationTestService(t, handler)
	if _, err := service.watchRecovery(context.Background(), map[string]any{"id": "s1", "kind": "session"}); err != nil {
		t.Fatal(err)
	}
	state, err := service.unwatchRecovery(context.Background(), map[string]any{"id": "s1", "kind": "session"})
	if err != nil || len(state.Entries) != 0 {
		t.Fatalf("watch was not removed: %+v %v", state.Entries, err)
	}
	if err = service.automationTick(context.Background(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	state, err = service.recoveryCheck(context.Background())
	if err != nil || len(state.Entries) != 0 {
		t.Fatalf("cancelled quota state reappeared without a new revision: %+v %v", state.Entries, err)
	}
	state, err = service.watchRecovery(context.Background(), map[string]any{"id": "s1", "kind": "session"})
	if err != nil || len(state.Entries) != 1 || state.Entries[0].Status != "WAITING" {
		t.Fatalf("explicit watch did not restore the waiter: %+v %v", state.Entries, err)
	}
}

func TestNativeRecoveryUsesOfficialQuotaAndRemovesWaiter(t *testing.T) {
	var prompts atomic.Int32
	var body map[string]any
	session := nativeTestSession("s1", 0, nil)
	session["last_turn_reason"] = "failed"
	turns := []NativeTurn{{TurnID: "t1", Error: &NativeTurnError{StatusCode: 429, Message: "quota"}}}
	usage := map[string]any{"kind": "ok", "quota": map[string]any{"usages": map[string]any{"limit5h": map[string]any{"usedRatio": 0.2}}}}
	handler := automationTestSessionHandler(t, session, turns, nil, usage, func(path string, value map[string]any) {
		if path == "/api/v1/sessions/s1/prompts" {
			prompts.Add(1)
			body = value
		}
	})
	service, _ := newAutomationTestService(t, handler)
	if _, err := service.watchRecovery(context.Background(), map[string]any{"id": "s1", "kind": "session"}); err != nil {
		t.Fatal(err)
	}
	settings, err := service.automationSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.Recovery.Enabled = true
	if err := service.storeAutomationSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	if err := service.automationTick(context.Background(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if prompts.Load() != 1 {
		t.Fatalf("available quota did not resume once: %d", prompts.Load())
	}
	if _, exists := body["plan_mode"]; exists {
		t.Fatal("recovery changed plan mode")
	}
	updated, err := service.automationSettings(context.Background())
	if err != nil || len(updated.Recovery.Entries) != 0 {
		t.Fatalf("successful waiter was not removed: %+v %v", updated.Recovery.Entries, err)
	}
	var state string
	if err = service.db.QueryRow(`SELECT state FROM automation_intents WHERE id='recovery:session:s1'`).Scan(&state); err != nil || state != "completed" {
		t.Fatalf("recovery receipt = %s %v", state, err)
	}
}

// syncHandler lets a test change the synthetic official session between ticks, so the
// service's re-read sees the same state a real client would report.
type syncHandler struct {
	mu      sync.Mutex
	inner   http.Handler
	session map[string]any
}

func (h *syncHandler) setPending(value string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.session["pending_interaction"] = value
}

func (h *syncHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	pending := h.session["pending_interaction"]
	h.mu.Unlock()
	_ = pending
	h.inner.ServeHTTP(w, r)
}

// TestAutomationIntentsLegacyTimestampsAreBackfilled covers the table the reviewer
// asked about: its cooldown, supersede and expire queries all compare timestamps as
// text, so a legacy variable-width row must not survive a restart.
func TestAutomationIntentsLegacyTimestampsAreBackfilled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "intents.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewNativeService(db, NewKAPClient(""), t.TempDir(), func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	service.Close()
	db.Close()

	// Reopen and write a legacy row by hand, then construct a service again.
	db, err = storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	whole := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	frac := whole.Add(359 * time.Millisecond)
	for index, at := range []time.Time{whole, frac} {
		if _, err = db.Exec(`INSERT INTO automation_intents(id,kind,host_id,session_id,model_id,context_revision,state,created_at,updated_at,result) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			"legacy-"+string(rune('a'+index)), "keepalive", "native", "s1", "", "r1", "verified", at.UTC().Format(time.RFC3339Nano), at.UTC().Format(time.RFC3339Nano), ""); err != nil {
			t.Fatal(err)
		}
	}
	var first string
	if err = db.QueryRow(`SELECT id FROM automation_intents ORDER BY created_at ASC LIMIT 1`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if first != "legacy-b" {
		t.Fatalf("precondition: legacy order already correct (%s); the fixture no longer reproduces the bug", first)
	}

	reopened, err := NewNativeService(db, NewKAPClient(""), t.TempDir(), func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = db.QueryRow(`SELECT id FROM automation_intents ORDER BY created_at ASC LIMIT 1`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if first != "legacy-a" {
		t.Fatalf("after backfill the oldest intent is %s, want legacy-a", first)
	}
	rows, err := db.Query(`SELECT length(created_at),length(updated_at) FROM automation_intents`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var created, updated int
		if err = rows.Scan(&created, &updated); err != nil {
			t.Fatal(err)
		}
		if created != len(storage.SortableLayout) || updated != len(storage.SortableLayout) {
			t.Fatalf("widths = %d/%d, want %d", created, updated, len(storage.SortableLayout))
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}
