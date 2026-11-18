package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enhancedkimi/internal/storage"
)

type fakeExecutor struct {
	mu           sync.Mutex
	connected    bool
	models       []ModelInfo
	sessions     map[string]SessionInfo
	turns        map[string][]TurnInfo
	cacheStats   map[string]SessionCacheStat
	keepAliveOn  bool
	keepAliveMin int
	events       []string
	activities   []string
	createdSeq   int
	sentPrompts  []string

	onCreate func(ctx context.Context, cwd, title, model, effort, permissionMode string)
	onPrompt func(ctx context.Context, sessionID, text, model, effort, permissionMode string)
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{
		connected: true,
		models: []ModelInfo{
			{ID: "kimi/k2", Name: "Kimi K2", SupportEfforts: []string{"low", "medium", "high"}},
		},
		sessions:     map[string]SessionInfo{},
		turns:        map[string][]TurnInfo{},
		cacheStats:   map[string]SessionCacheStat{},
		keepAliveOn:  true,
		keepAliveMin: 25,
	}
}

func (f *fakeExecutor) KAPConnected(context.Context) (bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.connected {
		return false, "KAP 未连接"
	}
	return true, ""
}

func (f *fakeExecutor) Models(context.Context) ([]ModelInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ModelInfo(nil), f.models...), nil
}

func (f *fakeExecutor) Sessions(context.Context) ([]SessionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]SessionInfo, 0, len(f.sessions))
	for _, s := range f.sessions {
		out = append(out, s)
	}
	return out, nil
}

func (f *fakeExecutor) GetSession(_ context.Context, id string) (SessionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return SessionInfo{}, errors.New("session not found")
	}
	return s, nil
}

func (f *fakeExecutor) GetSessionTurns(_ context.Context, id string, _ int) ([]TurnInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]TurnInfo(nil), f.turns[id]...), nil
}

func (f *fakeExecutor) CreateSession(ctx context.Context, cwd, title, model, effort, permissionMode string) (SessionInfo, error) {
	if f.onCreate != nil {
		f.onCreate(ctx, cwd, title, model, effort, permissionMode)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createdSeq++
	id := "sess_" + string(rune('0'+f.createdSeq))
	s := SessionInfo{ID: id, Title: title, CWD: cwd, PendingInteraction: "none"}
	f.sessions[id] = s
	return s, nil
}

func (f *fakeExecutor) SendPrompt(ctx context.Context, sessionID, text, model, effort, permissionMode string) error {
	if f.onPrompt != nil {
		f.onPrompt(ctx, sessionID, text, model, effort, permissionMode)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentPrompts = append(f.sentPrompts, sessionID+":"+text+"|"+model+"|"+effort+"|"+permissionMode)
	if _, ok := f.turns[sessionID]; !ok {
		f.turns[sessionID] = []TurnInfo{{TurnID: "turn_1", HasAssistantMsg: true}}
	}
	return nil
}

func (f *fakeExecutor) SessionCacheStats(_ context.Context, sessionIDs []string) (map[string]SessionCacheStat, bool, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]SessionCacheStat{}
	for _, id := range sessionIDs {
		if st, ok := f.cacheStats[id]; ok {
			out[id] = st
		}
	}
	return out, f.keepAliveOn, f.keepAliveMin
}

func (f *fakeExecutor) PublishEvent(scope string, _ any) {
	f.mu.Lock()
	f.events = append(f.events, scope)
	f.mu.Unlock()
}

func (f *fakeExecutor) RecordPluginActivity(_ context.Context, pluginID, detail string) {
	f.mu.Lock()
	f.activities = append(f.activities, pluginID+":"+detail)
	f.mu.Unlock()
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "scheduler.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func waitUntil(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

func TestEngineMissedCatchupAndSkip(t *testing.T) {
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	exec := newFakeExecutor()
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	engine, err := NewEngine(store, exec,
		WithNow(func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return now
		}),
		WithLocation(time.UTC),
		WithPollInterval(5*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	cwd := t.TempDir()
	viewCatchup, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name:           "补跑任务",
		Prompt:         "检查变更",
		CWD:            cwd,
		Mode:           ModeNewSession,
		Model:          "kimi/k2",
		Effort:         "high",
		PermissionMode: "manual",
		Schedule:       ScheduleRule{Kind: "hourly", Minute: 5},
		MissedPolicy:   MissedCatchup,
	})
	if err != nil {
		t.Fatal(err)
	}
	viewSkip, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name:           "跳过任务",
		Prompt:         "跳过检查",
		CWD:            cwd,
		Mode:           ModeNewSession,
		PermissionMode: "manual",
		Schedule:       ScheduleRule{Kind: "hourly", Minute: 5},
		MissedPolicy:   MissedSkip,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Jump forward 3 hours: 09:05, 10:05, 11:05 were all missed.
	mu.Lock()
	now = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	mu.Unlock()
	if err := engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}

	waitUntil(t, func() bool {
		runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", "", 20)
		if len(runs) != 2 {
			return false
		}
		for _, r := range runs {
			if r.Status == RunQueued || r.Status == RunRunning {
				return false
			}
		}
		return true
	})

	catchupRuns, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", viewCatchup.ID, 10)
	if len(catchupRuns) != 1 || catchupRuns[0].TriggerKind != TriggerCatchup || catchupRuns[0].Status != RunCompleted {
		t.Fatalf("expected 1 merged catchup run, got %+v", catchupRuns)
	}
	skipRuns, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", viewSkip.ID, 10)
	if len(skipRuns) != 1 || skipRuns[0].Status != RunSkipped {
		t.Fatalf("expected 1 skipped run, got %+v", skipRuns)
	}
	exec.mu.Lock()
	if len(exec.sentPrompts) != 1 || len(exec.activities) < 2 {
		t.Fatalf("expected 1 prompt sent and session/prompt activities, prompts=%v activities=%v", exec.sentPrompts, exec.activities)
	}
	exec.mu.Unlock()
}

func TestEngineRestartPreservesTasksAndNextRun(t *testing.T) {
	db := openTestDB(t)
	store1, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	exec := newFakeExecutor()
	fixedNow := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	engine1, err := NewEngine(store1, exec, WithNow(func() time.Time { return fixedNow }), WithLocation(time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := engine1.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name:           "重启持久化",
		Prompt:         "巡检",
		CWD:            t.TempDir(),
		Mode:           ModeNewSession,
		PermissionMode: "manual",
		Schedule:       ScheduleRule{Kind: "daily", Time: "09:30"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Insert an in-flight run to confirm recovery marks it failed on restart.
	_ = store1.InsertRun(context.Background(), RunRecord{
		ID: "run_inflight", TaskID: saved.ID, PluginID: "kimicode-plus.scheduler",
		TaskName: saved.Name, TriggerKind: TriggerScheduled, Mode: ModeNewSession,
		Status: RunRunning, StartedAt: storage.SortableTime(fixedNow), Unread: true,
	})
	engine1.Close()

	store2, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	engine2, err := NewEngine(store2, exec, WithNow(func() time.Time { return fixedNow.Add(10 * time.Minute) }), WithLocation(time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	defer engine2.Close()

	reloaded, err := store2.GetTask(context.Background(), "kimicode-plus.scheduler", saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.NextRunAt != saved.NextRunAt {
		t.Fatalf("nextRunAt changed across restart: %s -> %s", saved.NextRunAt, reloaded.NextRunAt)
	}
	runs, _ := store2.ListRuns(context.Background(), "kimicode-plus.scheduler", saved.ID, 10)
	if len(runs) != 1 || runs[0].Status != RunFailed {
		t.Fatalf("expected in-flight run marked failed, got %+v", runs)
	}
}

func TestEngineConcurrencyLimitAndNonOverlap(t *testing.T) {
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	exec := newFakeExecutor()
	gate := make(chan struct{})
	var inflight atomic.Int32
	var maxSeen atomic.Int32
	exec.onPrompt = func(ctx context.Context, _, _, _, _, _ string) {
		cur := inflight.Add(1)
		for {
			prev := maxSeen.Load()
			if cur <= prev || maxSeen.CompareAndSwap(prev, cur) {
				break
			}
		}
		select {
		case <-gate:
		case <-ctx.Done():
		}
		inflight.Add(-1)
	}

	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	engine, err := NewEngine(store, exec,
		WithNow(func() time.Time { return now }),
		WithLocation(time.UTC),
		WithPollInterval(5*time.Millisecond),
		WithMaxConcurrent(2),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	cwd := t.TempDir()
	var ids []string
	for _, name := range []string{"T1", "T2", "T3"} {
		v, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
			Name: name, Prompt: "p", CWD: cwd, Mode: ModeNewSession, PermissionMode: "manual",
			Schedule: ScheduleRule{Kind: "daily", Time: "10:00"},
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, v.ID)
	}

	for _, id := range ids {
		if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", id); err != nil {
			t.Fatal(err)
		}
	}

	waitUntil(t, func() bool { return inflight.Load() == 2 })
	// Same task cannot overlap while active.
	if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", ids[0]); err == nil {
		t.Fatal("expected overlap runNow to be rejected")
	}

	close(gate)
	waitUntil(t, func() bool {
		runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", "", 10)
		if len(runs) != 3 {
			return false
		}
		for _, r := range runs {
			if r.Status != RunCompleted {
				return false
			}
		}
		return true
	})
	if maxSeen.Load() > 2 {
		t.Fatalf("exceeded global max concurrent limit 2: saw %d", maxSeen.Load())
	}
}

func TestEngineDisabledPluginDoesNotTrigger(t *testing.T) {
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	exec := newFakeExecutor()
	pluginOn := false
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	engine, err := NewEngine(store, exec,
		WithNow(func() time.Time { return now }),
		WithLocation(time.UTC),
		WithPluginEnabled(func(string) bool { return pluginOn }),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	_, err = engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name: "停用不触发", Prompt: "test", CWD: t.TempDir(), Mode: ModeNewSession, PermissionMode: "manual",
		Schedule: ScheduleRule{Kind: "daily", Time: "09:05"},
	})
	if err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 11, 18, 9, 6, 0, 0, time.UTC)
	if err := engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", "", 10)
	if len(runs) != 0 {
		t.Fatalf("expected 0 runs while plugin disabled, got %+v", runs)
	}

	// Re-enabling restores triggering.
	pluginOn = true
	if err := engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", "", 10)
		return len(runs) == 1 && runs[0].Status == RunCompleted
	})
}

func TestEngineBoundSessionBusyWaitAndApprovalIronRule(t *testing.T) {
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	exec := newFakeExecutor()
	exec.sessions["s_busy"] = SessionInfo{ID: "s_busy", Title: "忙碌会话", CWD: t.TempDir(), Busy: true, PendingInteraction: "none"}
	exec.sessions["s_approve"] = SessionInfo{ID: "s_approve", Title: "审批会话", CWD: t.TempDir(), Busy: false, PendingInteraction: "none"}

	engine, err := NewEngine(store, exec,
		WithBusyWaitTimeout(60*time.Millisecond),
		WithPollInterval(10*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	busyTask, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name: "等待超时", Prompt: "hello", Mode: ModeBoundSession, SessionID: "s_busy",
		PermissionMode: "manual", Schedule: ScheduleRule{Kind: "daily", Time: "09:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", busyTask.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", busyTask.ID, 5)
		return len(runs) == 1 && runs[0].Status == RunSkipped
	})

	// Approval iron rule: when turn enters awaiting_approval, never auto-approve and mark awaiting_approval.
	exec.onPrompt = func(_ context.Context, sid, _, _, _, _ string) {
		if sid == "s_approve" {
			exec.mu.Lock()
			exec.sessions[sid] = SessionInfo{ID: sid, Title: "审批会话", Busy: true, PendingInteraction: "approval"}
			exec.turns[sid] = []TurnInfo{{TurnID: "t_app", HasApproval: true, ApprovalTitle: "WriteFile"}}
			exec.mu.Unlock()
		}
	}
	appTask, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name: "审批测试", Prompt: "write", Mode: ModeBoundSession, SessionID: "s_approve",
		PermissionMode: "auto", Schedule: ScheduleRule{Kind: "daily", Time: "09:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", appTask.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", appTask.ID, 5)
		return len(runs) == 1 && runs[0].Status == RunAwaitingApproval
	})

	// Test prefs & markRead via Action
	if _, err := engine.Action(context.Background(), "kimicode-plus.scheduler", "schedule.prefs", json.RawMessage(`{"autoMarkRead":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Action(context.Background(), "kimicode-plus.scheduler", "schedule.markAllRead", nil); err != nil {
		t.Fatal(err)
	}
	unread, _ := store.UnreadCount(context.Background(), "kimicode-plus.scheduler")
	if unread != 0 {
		t.Fatalf("expected 0 unread after markAllRead, got %d", unread)
	}
}
