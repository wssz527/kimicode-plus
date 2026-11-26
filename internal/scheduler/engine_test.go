package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enhancedkimi/internal/storage"
)

type fakeExecutor struct {
	mu           sync.Mutex
	connected    bool
	accountValid bool
	accountErr   string
	quotaRem     float64
	quotaKnown   bool
	quotaDetail  string
	autoAttach   AutoAttachInfo
	models       []ModelInfo
	sessions     map[string]SessionInfo
	turns        map[string][]TurnInfo
	cacheStats   map[string]SessionCacheStat
	keepAliveOn  bool
	keepAliveMin int
	events       []string
	rawEvents    []any
	activities   []string
	createdSeq   int
	sentPrompts  []string
	receipt      SendPromptReceipt
	archivedIDs  []string
	batchArchIDs [][]string
	abortedIDs   []string
	archiveErr   error

	onCreate func(ctx context.Context, cwd, title, model, effort, permissionMode string)
	onPrompt func(ctx context.Context, sessionID, text, model, effort, permissionMode string)
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{
		connected:    true,
		accountValid: true,
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

func (f *fakeExecutor) SendPrompt(ctx context.Context, sessionID, text, model, effort, permissionMode string) (SendPromptReceipt, error) {
	if f.onPrompt != nil {
		f.onPrompt(ctx, sessionID, text, model, effort, permissionMode)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentPrompts = append(f.sentPrompts, sessionID+":"+text+"|"+model+"|"+effort+"|"+permissionMode)
	if _, ok := f.turns[sessionID]; !ok {
		f.turns[sessionID] = []TurnInfo{{TurnID: "turn_1", HasAssistantMsg: true}}
	}
	return f.receipt, nil
}

func (f *fakeExecutor) ArchiveSession(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.archiveErr != nil {
		return f.archiveErr
	}
	f.archivedIDs = append(f.archivedIDs, sessionID)
	return nil
}

func (f *fakeExecutor) ArchiveSessions(_ context.Context, sessionIDs []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.archiveErr != nil {
		return f.archiveErr
	}
	cp := append([]string(nil), sessionIDs...)
	f.batchArchIDs = append(f.batchArchIDs, cp)
	f.archivedIDs = append(f.archivedIDs, cp...)
	return nil
}

func (f *fakeExecutor) AbortSession(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.abortedIDs = append(f.abortedIDs, sessionID)
	return nil
}

func (f *fakeExecutor) CheckAccount(context.Context) (bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accountValid, f.accountErr
}

func (f *fakeExecutor) QuotaRemainingPercent(context.Context) (float64, bool, string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.quotaRem, f.quotaKnown, "fake", f.quotaDetail
}

func (f *fakeExecutor) AutoAttachStatus(context.Context) AutoAttachInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.autoAttach
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

func (f *fakeExecutor) PublishEvent(scope string, value any) {
	f.mu.Lock()
	f.events = append(f.events, scope)
	f.rawEvents = append(f.rawEvents, value)
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

func TestEngineBoundSessionDoesNotEarlyCompleteOnPriorAssistantMsg(t *testing.T) {
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	base := newFakeExecutor()
	base.sessions["s_prior"] = SessionInfo{ID: "s_prior", Title: "已有回复会话", CWD: t.TempDir(), Busy: false, PendingInteraction: "none"}
	base.turns["s_prior"] = []TurnInfo{{TurnID: "turn_prev", HasAssistantMsg: true}}

	var polls atomic.Int32
	exec := &delayedTurnExecutor{
		fakeExecutor: base,
		onGetSession: func(id string) {
			if id != "s_prior" || len(base.sentPrompts) == 0 {
				return
			}
			n := polls.Add(1)
			// Poll 1: simulate KAP still reporting idle with only the prior turn.
			// Poll 2+: session transitions to busy + new turn waiting for approval.
			if n >= 2 {
				base.mu.Lock()
				base.sessions[id] = SessionInfo{ID: id, Title: "已有回复会话", Busy: true, PendingInteraction: "approval"}
				base.turns[id] = []TurnInfo{
					{TurnID: "turn_prev", HasAssistantMsg: true},
					{TurnID: "turn_new", HasApproval: true, ApprovalTitle: "Bash"},
				}
				base.mu.Unlock()
			}
		},
	}

	engine, err := NewEngine(store, exec, WithPollInterval(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	task, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name: "二次追问审批拦截", Prompt: "run bash", Mode: ModeBoundSession, SessionID: "s_prior",
		PermissionMode: "manual", Schedule: ScheduleRule{Kind: "daily", Time: "09:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", task.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
		return len(runs) == 1 && runs[0].Status != RunQueued && runs[0].Status != RunRunning
	})
	runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
	if runs[0].Status != RunAwaitingApproval {
		t.Fatalf("expected awaiting_approval instead of early completion on prior assistant turn, got %+v", runs[0])
	}
}

func TestEngineBoundSessionIgnoresHistoricalApproval(t *testing.T) {
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	base := newFakeExecutor()
	base.sessions["s_hist"] = SessionInfo{ID: "s_hist", Title: "历史审批会话", CWD: t.TempDir(), Busy: false, PendingInteraction: "none"}
	base.turns["s_hist"] = []TurnInfo{{TurnID: "turn_old", HasApproval: true, ApprovalTitle: "WriteFile", HasAssistantMsg: true}}

	var polls atomic.Int32
	exec := &delayedTurnExecutor{
		fakeExecutor: base,
		onGetSession: func(id string) {
			if id != "s_hist" || len(base.sentPrompts) == 0 {
				return
			}
			switch n := polls.Add(1); {
			case n >= 4:
				base.mu.Lock()
				base.sessions[id] = SessionInfo{ID: id, Title: "历史审批会话", Busy: false, PendingInteraction: "none"}
				base.turns[id] = []TurnInfo{
					{TurnID: "turn_old", HasApproval: true, ApprovalTitle: "WriteFile", HasAssistantMsg: true},
					{TurnID: "turn_new", HasAssistantMsg: true},
				}
				base.mu.Unlock()
			case n >= 2:
				base.mu.Lock()
				base.sessions[id] = SessionInfo{ID: id, Title: "历史审批会话", Busy: true, PendingInteraction: "none"}
				base.turns[id] = []TurnInfo{
					{TurnID: "turn_old", HasApproval: true, ApprovalTitle: "WriteFile", HasAssistantMsg: true},
					{TurnID: "turn_new", HasAssistantMsg: true},
				}
				base.mu.Unlock()
			}
		},
	}

	engine, err := NewEngine(store, exec, WithPollInterval(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	task, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name: "历史审批不误杀", Prompt: "go", Mode: ModeBoundSession, SessionID: "s_hist",
		PermissionMode: "manual", Schedule: ScheduleRule{Kind: "daily", Time: "09:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", task.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
		return len(runs) == 1 && runs[0].Status != RunQueued && runs[0].Status != RunRunning
	})
	runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
	if runs[0].Status != RunCompleted {
		t.Fatalf("historical approval killed the run: %+v", runs[0])
	}
}

func TestEngineCancelledRunAbortsRemoteSession(t *testing.T) {
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	exec := newFakeExecutor()
	exec.sessions["s_cancel"] = SessionInfo{ID: "s_cancel", Title: "取消会话", CWD: t.TempDir(), Busy: false, PendingInteraction: "none"}
	exec.onPrompt = func(_ context.Context, sid, _, _, _, _ string) {
		exec.mu.Lock()
		exec.sessions[sid] = SessionInfo{ID: sid, Title: "取消会话", Busy: true, PendingInteraction: "none"}
		exec.mu.Unlock()
	}

	engine, err := NewEngine(store, exec, WithPollInterval(200*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	task, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name: "取消中止", Prompt: "work", Mode: ModeBoundSession, SessionID: "s_cancel",
		PermissionMode: "manual", Schedule: ScheduleRule{Kind: "daily", Time: "09:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", task.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		exec.mu.Lock()
		defer exec.mu.Unlock()
		return len(exec.sentPrompts) == 1
	})
	if err := engine.StopPlugin(context.Background(), "kimicode-plus.scheduler"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		exec.mu.Lock()
		defer exec.mu.Unlock()
		for _, id := range exec.abortedIDs {
			if id == "s_cancel" {
				return true
			}
		}
		return false
	})
	runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
	if len(runs) != 1 || runs[0].Status != RunSkipped {
		t.Fatalf("expected cancelled run, got %+v", runs)
	}
}

func TestEngineRunNowRejectsWhenPaused(t *testing.T) {
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	exec := newFakeExecutor()
	exec.sessions["s_pause"] = SessionInfo{ID: "s_pause", Title: "暂停会话", CWD: t.TempDir(), PendingInteraction: "none"}
	engine, err := NewEngine(store, exec, WithPollInterval(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	task, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name: "暂停拒绝", Prompt: "x", Mode: ModeBoundSession, SessionID: "s_pause",
		PermissionMode: "manual", Schedule: ScheduleRule{Kind: "daily", Time: "09:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resume := engine.Pause()
	defer resume()
	if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", task.ID); err == nil {
		t.Fatal("RunNow started while the engine was paused")
	}
	resume()
	if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", task.ID); err != nil {
		t.Fatalf("RunNow after resume: %v", err)
	}
}

func TestResolveGitExecutableFallsThroughToAbsolutePath(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed on this machine")
	}
	originalBins := gitExecutables
	originalPath := executablePath
	defer func() {
		gitExecutables = originalBins
		executablePath = originalPath
	}()
	// The bare name no longer resolves, exactly like a GUI-launched core whose
	// PATH lacks git. The absolute fallback must still be reached.
	executablePath = func(name string) (string, error) {
		return "", exec.ErrNotFound
	}
	gitExecutables = []string{"git", realGit}
	resolved, resolveErr := resolveGitExecutable()
	if resolveErr != nil {
		t.Fatalf("absolute fallback was not reached: %v", resolveErr)
	}
	if resolved != realGit {
		t.Fatalf("resolved %q, want %q", resolved, realGit)
	}
	// A non-executable absolute path must be rejected rather than returned.
	plain := filepath.Join(t.TempDir(), "not-executable-git")
	if err := os.WriteFile(plain, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitExecutables = []string{plain}
	if _, err := resolveGitExecutable(); err == nil {
		t.Fatal("accepted a non-executable git candidate")
	}
}

func TestCheckGitWorktreeFailsClosed(t *testing.T) {
	root := t.TempDir()
	if status := CheckGitWorktree(context.Background(), root); status.IsGitRepo || !status.Clean {
		t.Fatalf("plain directory reported as repo: %+v", status)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dirty.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := gitExecutables
	gitExecutables = []string{"/nonexistent/git-for-test"}
	defer func() { gitExecutables = original }()
	status := CheckGitWorktree(context.Background(), root)
	if !status.IsGitRepo || status.Clean || status.Error == "" {
		t.Fatalf("git failure with a .git marker must fail closed: %+v", status)
	}
}

type delayedTurnExecutor struct {
	*fakeExecutor
	onGetSession func(id string)
}

func (d *delayedTurnExecutor) GetSession(ctx context.Context, id string) (SessionInfo, error) {
	if d.onGetSession != nil {
		d.onGetSession(id)
	}
	return d.fakeExecutor.GetSession(ctx, id)
}

// flakyTurnsExecutor fails every turns read until the first prompt went out,
// reproducing a broken pre-send baseline.
type flakyTurnsExecutor struct {
	*delayedTurnExecutor
	failTurnsUntilPrompt bool
}

func (f *flakyTurnsExecutor) GetSessionTurns(ctx context.Context, id string, pageSize int) ([]TurnInfo, error) {
	if f.failTurnsUntilPrompt {
		f.mu.Lock()
		sent := len(f.sentPrompts) > 0
		f.mu.Unlock()
		if !sent {
			return nil, errors.New("官方回合接口暂时不可用")
		}
	}
	return f.fakeExecutor.GetSessionTurns(ctx, id, pageSize)
}

// pagingTurnsExecutor mimics the official turns endpoint: it only ever returns
// the newest pageSize turns.
type pagingTurnsExecutor struct {
	*delayedTurnExecutor
	pageSizes []int
}

func (p *pagingTurnsExecutor) GetSessionTurns(ctx context.Context, id string, pageSize int) ([]TurnInfo, error) {
	p.mu.Lock()
	p.pageSizes = append(p.pageSizes, pageSize)
	p.mu.Unlock()
	turns, err := p.fakeExecutor.GetSessionTurns(ctx, id, pageSize)
	if err != nil {
		return nil, err
	}
	if len(turns) > pageSize {
		turns = turns[len(turns)-pageSize:]
	}
	return turns, nil
}

func TestObserveTurnOutcomeRequiresProgressEvidence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		baseline sendOutcome
	}{
		{"unchanged history", sendOutcome{baselineKnown: true, anchor: "old"}},
		{"unknown history baseline", sendOutcome{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := newFakeExecutor()
			executor.sessions["s1"] = SessionInfo{ID: "s1", PendingInteraction: "none"}
			executor.turns["s1"] = []TurnInfo{{TurnID: "old", HasAssistantMsg: true}}
			e := &Engine{exec: executor, pollInterval: time.Millisecond, now: time.Now}
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			status, failure, _ := e.observeTurnOutcome(ctx, context.Background(), "s1", tc.baseline, 1, 0)
			if status != RunFailed || failure != FailureTimeout || len(executor.abortedIDs) != 1 {
				t.Fatalf("idle observation = %s/%s, aborts=%v", status, failure, executor.abortedIDs)
			}
		})
	}
}

// setBusy flips a fake session's busy flag.
func setBusy(base *fakeExecutor, sessionID string, busy bool) {
	base.mu.Lock()
	s := base.sessions[sessionID]
	s.Busy = busy
	base.sessions[sessionID] = s
	base.mu.Unlock()
}

// TestEngineFailedTurnStillFailsWithoutBaseline is the regression for the
// confirmed misattribution bug: when the pre-send history read fails the run
// has no baseline, and a short busy/idle cycle used to be reported as
// RunCompleted even though the ACK-attributed turn carries an error.
func TestEngineFailedTurnStillFailsWithoutBaseline(t *testing.T) {
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	base := newFakeExecutor()
	base.receipt = SendPromptReceipt{UserMessageID: "um_bug"}
	base.onPrompt = func(_ context.Context, sid, _, _, _, _ string) {
		base.mu.Lock()
		s := base.sessions[sid]
		s.Busy = true
		base.sessions[sid] = s
		base.turns[sid] = []TurnInfo{
			{TurnID: "turn_new", UserMessageIDs: []string{"um_bug"}, ErrorMessage: "boom", HasAssistantMsg: true},
		}
		base.mu.Unlock()
	}
	var reads atomic.Int32
	exec := &flakyTurnsExecutor{delayedTurnExecutor: &delayedTurnExecutor{
		fakeExecutor: base,
		onGetSession: func(id string) {
			if reads.Add(1) >= 2 {
				setBusy(base, id, false)
			}
		},
	}, failTurnsUntilPrompt: true}

	engine, err := NewEngine(store, exec, WithPollInterval(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	task, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name: "坏基线回归", Prompt: "work", CWD: t.TempDir(), Mode: ModeNewSession, PermissionMode: "manual",
		Schedule: ScheduleRule{Kind: "daily", Time: "09:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", task.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
		return len(runs) == 1 && runs[0].Status != RunQueued && runs[0].Status != RunRunning
	})
	runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
	if runs[0].Status != RunFailed || runs[0].FailureKind != FailureExecution {
		t.Fatalf("ACK-attributed error turn must fail the run, got %+v", runs[0])
	}
	if !strings.Contains(runs[0].Message, "boom") {
		t.Fatalf("expected the turn error in the message, got %q", runs[0].Message)
	}
	exec.mu.Lock()
	aborted := len(exec.abortedIDs)
	exec.mu.Unlock()
	if aborted != 0 {
		t.Fatalf("execution failures must not abort the session, aborts=%d", aborted)
	}
}

// TestEngineUnprovableRunIsNotReportedCompleted pins the other half of the
// contract: without an ACK receipt and without a baseline the run must not be
// declared completed just because the session was briefly busy.
func TestEngineUnprovableRunIsNotReportedCompleted(t *testing.T) {
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	base := newFakeExecutor()
	base.onPrompt = func(_ context.Context, sid, _, _, _, _ string) {
		base.mu.Lock()
		s := base.sessions[sid]
		s.Busy = true
		base.sessions[sid] = s
		base.turns[sid] = []TurnInfo{{TurnID: "turn_ghost", ErrorMessage: "无人认领的错误", HasAssistantMsg: true}}
		base.mu.Unlock()
	}
	var reads atomic.Int32
	exec := &flakyTurnsExecutor{delayedTurnExecutor: &delayedTurnExecutor{
		fakeExecutor: base,
		onGetSession: func(id string) {
			if reads.Add(1) >= 2 {
				setBusy(base, id, false)
			}
		},
	}, failTurnsUntilPrompt: true}

	engine, err := NewEngine(store, exec, WithPollInterval(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	task, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name: "无证据不判成功", Prompt: "work", CWD: t.TempDir(), Mode: ModeNewSession, PermissionMode: "manual",
		Schedule: ScheduleRule{Kind: "daily", Time: "09:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", task.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
		return len(runs) == 1 && runs[0].Status != RunQueued && runs[0].Status != RunRunning
	})
	runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
	if runs[0].Status == RunCompleted {
		t.Fatalf("unprovable run must not be completed: %+v", runs[0])
	}
	if runs[0].Status != RunFailed || runs[0].FailureKind != FailureExecution || !strings.Contains(runs[0].Message, "无法确认") {
		t.Fatalf("expected explicit unverifiable failure, got %+v", runs[0])
	}
	exec.mu.Lock()
	aborted := len(exec.abortedIDs)
	exec.mu.Unlock()
	if aborted != 0 {
		t.Fatalf("unverifiable runs must not abort the session, aborts=%d", aborted)
	}
}

// TestEngineACKAttributionIgnoresUnrelatedTurn proves the ACK scoping: a
// concurrent turn (from the user) that errors before the run's own turn must
// not fail the scheduled run.
func TestEngineACKAttributionIgnoresUnrelatedTurn(t *testing.T) {
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	base := newFakeExecutor()
	base.sessions["s_ack"] = SessionInfo{ID: "s_ack", Title: "并发会话", CWD: t.TempDir(), PendingInteraction: "none"}
	base.turns["s_ack"] = []TurnInfo{{TurnID: "turn_old", HasAssistantMsg: true}}
	base.receipt = SendPromptReceipt{UserMessageID: "um_ack"}
	base.onPrompt = func(_ context.Context, sid, _, _, _, _ string) {
		base.mu.Lock()
		s := base.sessions[sid]
		s.Busy = true
		base.sessions[sid] = s
		base.turns[sid] = []TurnInfo{
			{TurnID: "turn_old", HasAssistantMsg: true},
			{TurnID: "turn_user", ErrorMessage: "用户自己触发的错误", HasAssistantMsg: true},
			{TurnID: "turn_ours", UserMessageIDs: []string{"um_ack"}, HasAssistantMsg: true},
		}
		base.mu.Unlock()
	}
	var reads atomic.Int32
	exec := &delayedTurnExecutor{fakeExecutor: base, onGetSession: func(id string) {
		if reads.Add(1) >= 2 {
			setBusy(base, id, false)
		}
	}}

	engine, err := NewEngine(store, exec, WithPollInterval(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	task, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name: "并发回合不误杀", Prompt: "work", Mode: ModeBoundSession, SessionID: "s_ack",
		PermissionMode: "manual", Schedule: ScheduleRule{Kind: "daily", Time: "09:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", task.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
		return len(runs) == 1 && runs[0].Status != RunQueued && runs[0].Status != RunRunning
	})
	runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
	if runs[0].Status != RunCompleted {
		t.Fatalf("the unrelated error turn must not fail the run, got %+v", runs[0])
	}
}

// TestEngineConcurrentRunsAttributeIndependently runs two tasks at once and
// requires each run to be judged only by its own session's turns.
func TestEngineConcurrentRunsAttributeIndependently(t *testing.T) {
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	base := newFakeExecutor()
	base.sessions["s1"] = SessionInfo{ID: "s1", Title: "失败会话", CWD: t.TempDir(), PendingInteraction: "none"}
	base.sessions["s2"] = SessionInfo{ID: "s2", Title: "成功会话", CWD: t.TempDir(), PendingInteraction: "none"}
	base.turns["s1"] = []TurnInfo{{TurnID: "t1_old", HasAssistantMsg: true}}
	base.turns["s2"] = []TurnInfo{{TurnID: "t2_old", HasAssistantMsg: true}}
	base.onPrompt = func(_ context.Context, sid, _, _, _, _ string) {
		base.mu.Lock()
		s := base.sessions[sid]
		s.Busy = true
		base.sessions[sid] = s
		switch sid {
		case "s1":
			base.turns[sid] = append(base.turns[sid], TurnInfo{TurnID: "t1_new", ErrorMessage: "s1 执行失败", HasAssistantMsg: true})
		case "s2":
			base.turns[sid] = append(base.turns[sid], TurnInfo{TurnID: "t2_new", HasAssistantMsg: true})
		}
		base.mu.Unlock()
	}
	var r1, r2 atomic.Int32
	exec := &delayedTurnExecutor{fakeExecutor: base, onGetSession: func(id string) {
		switch id {
		case "s1":
			if r1.Add(1) >= 2 {
				setBusy(base, id, false)
			}
		case "s2":
			if r2.Add(1) >= 2 {
				setBusy(base, id, false)
			}
		}
	}}

	engine, err := NewEngine(store, exec, WithPollInterval(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	task1, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name: "并发失败方", Prompt: "work", Mode: ModeBoundSession, SessionID: "s1",
		PermissionMode: "manual", Schedule: ScheduleRule{Kind: "daily", Time: "09:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	task2, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name: "并发成功方", Prompt: "work", Mode: ModeBoundSession, SessionID: "s2",
		PermissionMode: "manual", Schedule: ScheduleRule{Kind: "daily", Time: "09:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", task1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", task2.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", "", 10)
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
	runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", "", 10)
	statusByTask := map[string]RunRecord{}
	for _, r := range runs {
		statusByTask[r.TaskID] = r
	}
	if r := statusByTask[task1.ID]; r.Status != RunFailed || !strings.Contains(r.Message, "s1 执行失败") {
		t.Fatalf("task1 must fail with its own error, got %+v", r)
	}
	if r := statusByTask[task2.ID]; r.Status != RunCompleted {
		t.Fatalf("task2 must complete independently, got %+v", r)
	}
}

// TestEngineLongHistoryManyTurnsStillAttributed covers the silent-completion
// variant of the bug: with more new turns than fit in the old fixed 20-turn
// page the baseline anchor rotated out and any error went unnoticed.
func TestEngineLongHistoryManyTurnsStillAttributed(t *testing.T) {
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	base := newFakeExecutor()
	base.sessions["s_long"] = SessionInfo{ID: "s_long", Title: "长历史会话", CWD: t.TempDir(), PendingInteraction: "none"}
	pre := make([]TurnInfo, 0, 45)
	for i := 1; i <= 45; i++ {
		pre = append(pre, TurnInfo{TurnID: fmt.Sprintf("t%02d", i), HasAssistantMsg: true})
	}
	base.turns["s_long"] = pre
	base.onPrompt = func(_ context.Context, sid, _, _, _, _ string) {
		base.mu.Lock()
		s := base.sessions[sid]
		s.Busy = true
		base.sessions[sid] = s
		for i := 46; i <= 66; i++ {
			turn := TurnInfo{TurnID: fmt.Sprintf("t%02d", i), HasAssistantMsg: true}
			if i == 66 {
				turn.ErrorMessage = "迟到失败"
			}
			base.turns[sid] = append(base.turns[sid], turn)
		}
		base.mu.Unlock()
	}
	var reads atomic.Int32
	exec := &pagingTurnsExecutor{delayedTurnExecutor: &delayedTurnExecutor{
		fakeExecutor: base,
		onGetSession: func(id string) {
			if reads.Add(1) >= 2 {
				setBusy(base, id, false)
			}
		},
	}}

	engine, err := NewEngine(store, exec, WithPollInterval(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	task, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name: "长历史归属", Prompt: "work", Mode: ModeBoundSession, SessionID: "s_long",
		PermissionMode: "manual", Schedule: ScheduleRule{Kind: "daily", Time: "09:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", task.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
		return len(runs) == 1 && runs[0].Status != RunQueued && runs[0].Status != RunRunning
	})
	runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
	if runs[0].Status != RunFailed || !strings.Contains(runs[0].Message, "迟到失败") {
		t.Fatalf("the errored turn must be attributed despite page rotation, got %+v", runs[0])
	}
	exec.mu.Lock()
	defer exec.mu.Unlock()
	largest := 0
	for _, size := range exec.pageSizes {
		if size > largest {
			largest = size
		}
	}
	if largest <= 20 {
		t.Fatalf("observation must enlarge the turns page beyond the old fixed 20, got max %d", largest)
	}
}

// TestEngineStateDelayKeepsWaitingForACKTurn ensures an idle report that
// arrives before the run's turn is visible in the history does not complete the
// run without evidence; the turn lands one poll later and carries the verdict.
func TestEngineStateDelayKeepsWaitingForACKTurn(t *testing.T) {
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	base := newFakeExecutor()
	base.sessions["s_late"] = SessionInfo{ID: "s_late", Title: "延迟会话", CWD: t.TempDir(), PendingInteraction: "none"}
	base.turns["s_late"] = []TurnInfo{{TurnID: "turn_old", HasAssistantMsg: true}}
	base.receipt = SendPromptReceipt{UserMessageID: "um_late"}
	base.onPrompt = func(_ context.Context, sid, _, _, _, _ string) {
		base.mu.Lock()
		s := base.sessions[sid]
		s.Busy = true
		base.sessions[sid] = s
		base.mu.Unlock()
	}
	var reads atomic.Int32
	exec := &delayedTurnExecutor{fakeExecutor: base, onGetSession: func(id string) {
		switch n := reads.Add(1); {
		case n >= 3:
			setBusy(base, id, false)
			base.mu.Lock()
			base.turns[id] = []TurnInfo{
				{TurnID: "turn_old", HasAssistantMsg: true},
				{TurnID: "turn_late", UserMessageIDs: []string{"um_late"}, ErrorMessage: "延迟失败", HasAssistantMsg: true},
			}
			base.mu.Unlock()
		case n == 2:
			setBusy(base, id, false)
		}
	}}

	engine, err := NewEngine(store, exec, WithPollInterval(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	task, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name: "状态延迟不早判", Prompt: "work", Mode: ModeBoundSession, SessionID: "s_late",
		PermissionMode: "manual", Schedule: ScheduleRule{Kind: "daily", Time: "09:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", task.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
		return len(runs) == 1 && runs[0].Status != RunQueued && runs[0].Status != RunRunning
	})
	runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
	if runs[0].Status != RunFailed || !strings.Contains(runs[0].Message, "延迟失败") {
		t.Fatalf("the delayed ACK turn must decide the verdict, got %+v", runs[0])
	}
}
