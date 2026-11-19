package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func collectNotifications(exec *fakeExecutor) []NotificationEvent {
	exec.mu.Lock()
	defer exec.mu.Unlock()
	var out []NotificationEvent
	for _, raw := range exec.rawEvents {
		m, ok := raw.(map[string]any)
		if !ok || m["kind"] != "run.notify" {
			continue
		}
		if n, ok := m["notification"].(NotificationEvent); ok {
			out = append(out, n)
		}
	}
	return out
}

func TestN5TemplateExpansionAndTitlePrefix(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 11, 19, 14, 35, 0, 0, loc)
	lastAt := time.Date(2026, 11, 18, 9, 15, 0, 0, loc).UTC().Format(time.RFC3339Nano)

	got := ExpandPrompt(
		"日期={{date}} 时间={{time}} 上次={{last_run_at}} 摘要={{last_run_summary}} 保留={{unknown}}",
		now, loc, lastAt, "包含 {{date}} 不应二次展开",
	)
	want := "日期=2026-11-19 时间=14:35 上次=2026-11-18 09:15 摘要=包含 {{date}} 不应二次展开 保留={{unknown}}"
	if got != want {
		t.Fatalf("ExpandPrompt:\n got  %q\n want %q", got, want)
	}

	empty := ExpandPrompt("上次={{last_run_at}} 摘要={{last_run_summary}}", now, loc, "", "")
	if empty != "上次=暂无 摘要=暂无" {
		t.Fatalf("empty fallback = %q", empty)
	}

	if title := FormatScheduledSessionTitle("每日巡检"); title != "[定时] 每日巡检" {
		t.Fatalf("title = %q", title)
	}
	if title := FormatScheduledSessionTitle("[定时] 每日巡检"); title != "[定时] 每日巡检" {
		t.Fatalf("double prefix title = %q", title)
	}
}

func TestN5NotificationFilterMatrix(t *testing.T) {
	cases := []struct {
		name      string
		run       RunRecord
		wantOk    bool
		wantEvent string
	}{
		{
			name:   "completed never notifies (handled by official)",
			run:    RunRecord{ID: "r1", TaskName: "T", Status: RunCompleted, Message: "已完成"},
			wantOk: false,
		},
		{
			name:   "awaiting_approval never notifies (handled by official)",
			run:    RunRecord{ID: "r2", TaskName: "T", Status: RunAwaitingApproval, Message: "等待批准"},
			wantOk: false,
		},
		{
			name:   "cancelled skipped never notifies",
			run:    RunRecord{ID: "r3", TaskName: "T", Status: RunSkipped, FailureKind: FailureCancelled, Message: "已取消"},
			wantOk: false,
		},
		{
			name:      "preflight failure notifies",
			run:       RunRecord{ID: "r4", TaskName: "T", Status: RunFailed, FailureKind: FailurePreflight, Message: "模型不存在"},
			wantOk:    true,
			wantEvent: NotifyPreflightFailed,
		},
		{
			name:      "execution failure notifies",
			run:       RunRecord{ID: "r5", TaskName: "T", Status: RunFailed, FailureKind: FailureTimeout, Message: "超时"},
			wantOk:    true,
			wantEvent: NotifyRunFailed,
		},
		{
			name:      "quota skipped notifies",
			run:       RunRecord{ID: "r6", TaskName: "T", Status: RunSkipped, FailureKind: FailureQuotaInsufficient, Message: "跳过（额度不足）"},
			wantOk:    true,
			wantEvent: NotifyRunSkipped,
		},
		{
			name:      "concurrency skipped notifies",
			run:       RunRecord{ID: "r7", TaskName: "T", Status: RunSkipped, FailureKind: FailureConcurrencyLimit, Message: "并发上限"},
			wantOk:    true,
			wantEvent: NotifyRunSkipped,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, ok := BuildNotification(tc.run)
			if ok != tc.wantOk {
				t.Fatalf("ok = %v, want %v (%+v)", ok, tc.wantOk, ev)
			}
			if ok && ev.EventKind != tc.wantEvent {
				t.Fatalf("eventKind = %q, want %q", ev.EventKind, tc.wantEvent)
			}
		})
	}
}

func TestN5TimeoutAbortsSessionAndNotifies(t *testing.T) {
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	exec := newFakeExecutor()
	exec.onPrompt = func(_ context.Context, sid, _, _, _, _ string) {
		exec.mu.Lock()
		s := exec.sessions[sid]
		s.Busy = true
		s.MainTurnActive = true
		exec.sessions[sid] = s
		exec.mu.Unlock()
	}

	engine, err := NewEngine(store, exec,
		WithPollInterval(10*time.Millisecond),
		WithTimeoutScale(50*time.Millisecond), // 1 minute = 50ms in test
	)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	timeout := 1
	task, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name:           "超时测试",
		Prompt:         "run long {{date}}",
		CWD:            t.TempDir(),
		Mode:           ModeNewSession,
		PermissionMode: "manual",
		TimeoutMinutes: &timeout,
		Schedule:       ScheduleRule{Kind: "daily", Time: "09:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", task.ID); err != nil {
		t.Fatal(err)
	}

	waitUntil(t, func() bool {
		runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
		return len(runs) == 1 && runs[0].Status == RunFailed
	})
	runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
	if runs[0].FailureKind != FailureTimeout || !strings.Contains(runs[0].Message, "运行超时") {
		t.Fatalf("expected timeout failure, got %+v", runs[0])
	}
	exec.mu.Lock()
	aborted := append([]string(nil), exec.abortedIDs...)
	exec.mu.Unlock()
	if len(aborted) == 0 {
		t.Fatalf("expected AbortSession to be called on timeout")
	}
	notifs := collectNotifications(exec)
	if len(notifs) != 1 || notifs[0].EventKind != NotifyRunFailed {
		t.Fatalf("expected 1 run_failed notification, got %+v", notifs)
	}
}

func TestN5MaxTurnsGuardrailAbortsSession(t *testing.T) {
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	exec := newFakeExecutor()
	exec.onPrompt = func(_ context.Context, sid, _, _, _, _ string) {
		exec.mu.Lock()
		s := exec.sessions[sid]
		s.Busy = true
		exec.sessions[sid] = s
		exec.turns[sid] = []TurnInfo{
			{TurnID: "t1", HasAssistantMsg: true},
			{TurnID: "t2", HasAssistantMsg: true},
			{TurnID: "t3", HasAssistantMsg: false},
		}
		exec.mu.Unlock()
	}

	engine, err := NewEngine(store, exec, WithPollInterval(10*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	maxTurns := 2
	task, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name:           "回合护栏测试",
		Prompt:         "loop",
		CWD:            t.TempDir(),
		Mode:           ModeNewSession,
		PermissionMode: "manual",
		MaxTurns:       &maxTurns,
		Schedule:       ScheduleRule{Kind: "daily", Time: "09:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", task.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
		return len(runs) == 1 && runs[0].Status == RunFailed
	})
	runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
	if runs[0].FailureKind != FailureMaxTurns || !strings.Contains(runs[0].Message, "最大回合数") {
		t.Fatalf("expected max_turns failure, got %+v", runs[0])
	}
	exec.mu.Lock()
	abortedCount := len(exec.abortedIDs)
	exec.mu.Unlock()
	if abortedCount == 0 {
		t.Fatalf("expected AbortSession on maxTurns exceeded")
	}
}

func TestN5QuotaSkipAndConcurrencyLimitSkip(t *testing.T) {
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	exec := newFakeExecutor()
	exec.quotaKnown = true
	exec.quotaRem = 5 // Below default 10% threshold
	exec.quotaDetail = "5 小时窗口"

	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	engine, err := NewEngine(store, exec,
		WithNow(func() time.Time { return now }),
		WithLocation(time.UTC),
		WithPollInterval(10*time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	cwd := t.TempDir()
	task, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name: "额度保护任务", Prompt: "check", CWD: cwd, Mode: ModeNewSession, PermissionMode: "manual",
		Schedule: ScheduleRule{Kind: "daily", Time: "10:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", task.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
		return len(runs) == 1 && runs[0].Status == RunSkipped
	})
	runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
	if runs[0].FailureKind != FailureQuotaInsufficient || !strings.Contains(runs[0].Message, "跳过（额度不足）") {
		t.Fatalf("expected quota skip message, got %+v", runs[0])
	}
	notifs := collectNotifications(exec)
	if len(notifs) != 1 || notifs[0].EventKind != NotifyRunSkipped {
		t.Fatalf("expected run_skipped notification for quota skip, got %+v", notifs)
	}

	// Now raise quota and test scheduled concurrency limit skip (global limit 2).
	exec.mu.Lock()
	exec.quotaRem = 80
	exec.rawEvents = nil
	exec.mu.Unlock()
	gate := make(chan struct{})
	exec.onPrompt = func(ctx context.Context, _, _, _, _, _ string) {
		select {
		case <-gate:
		case <-ctx.Done():
		}
	}

	for _, name := range []string{"并发A", "并发B", "并发C"} {
		_, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
			Name: name, Prompt: "go", CWD: cwd, Mode: ModeNewSession, PermissionMode: "manual",
			Schedule: ScheduleRule{Kind: "daily", Time: "09:05"},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	now = time.Date(2026, 11, 19, 9, 5, 1, 0, time.UTC)
	if err := engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(gate)
	waitUntil(t, func() bool {
		all, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", "", 20)
		// 1 earlier quota run + 3 concurrent tasks
		if len(all) < 4 {
			return false
		}
		for _, r := range all {
			if r.Status == RunQueued || r.Status == RunRunning {
				return false
			}
		}
		return true
	})
	all, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", "", 20)
	concurrencySkipped := 0
	completed := 0
	for _, r := range all {
		if r.Status == RunSkipped && r.FailureKind == FailureConcurrencyLimit {
			concurrencySkipped++
		}
		if r.Status == RunCompleted {
			completed++
		}
	}
	if concurrencySkipped != 1 || completed != 2 {
		t.Fatalf("expected 2 completed and 1 concurrency skipped, got completed=%d skipped=%d runs=%+v", completed, concurrencySkipped, all)
	}
	// Only the 1 skipped run should notify (the 2 completed runs must NOT notify!).
	notifs = collectNotifications(exec)
	if len(notifs) != 1 || notifs[0].EventKind != NotifyRunSkipped || notifs[0].FailureKind != FailureConcurrencyLimit {
		t.Fatalf("expected exactly 1 concurrency skip notification and 0 completed notifications, got %+v", notifs)
	}
}

func TestN5PreflightValidationFailures(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(exec *fakeExecutor, taskDir string)
		wantText string
	}{
		{
			name: "disconnected host",
			mutate: func(exec *fakeExecutor, _ string) {
				exec.connected = false
			},
			wantText: "官方客户端尚未接入",
		},
		{
			name: "invalid account",
			mutate: func(exec *fakeExecutor, _ string) {
				exec.accountValid = false
				exec.accountErr = "登录令牌已过期"
			},
			wantText: "当前账号不可用",
		},
		{
			name: "removed model",
			mutate: func(exec *fakeExecutor, _ string) {
				exec.models = []ModelInfo{{ID: "kimi/other", Name: "Other"}}
			},
			wantText: "已不在官方可用模型列表中",
		},
		{
			name: "missing project directory",
			mutate: func(_ *fakeExecutor, taskDir string) {
				_ = os.RemoveAll(taskDir)
			},
			wantText: "不存在或不是文件夹",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			store, err := NewStore(db)
			if err != nil {
				t.Fatal(err)
			}
			exec := newFakeExecutor()
			engine, err := NewEngine(store, exec, WithPollInterval(5*time.Millisecond))
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()

			taskDir := filepath.Join(t.TempDir(), "proj")
			if err := os.MkdirAll(taskDir, 0o755); err != nil {
				t.Fatal(err)
			}
			task, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
				Name:           "校验任务",
				Prompt:         "check",
				CWD:            taskDir,
				Mode:           ModeNewSession,
				Model:          "kimi/k2",
				PermissionMode: "manual",
				Schedule:       ScheduleRule{Kind: "daily", Time: "09:00"},
			})
			if err != nil {
				t.Fatal(err)
			}

			tc.mutate(exec, taskDir)

			if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", task.ID); err != nil {
				t.Fatal(err)
			}
			waitUntil(t, func() bool {
				runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
				return len(runs) == 1 && runs[0].Status == RunFailed
			})
			runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", task.ID, 5)
			if runs[0].FailureKind != FailurePreflight || !strings.Contains(runs[0].Message, tc.wantText) || !strings.Contains(runs[0].Message, "下一步") {
				t.Fatalf("unexpected preflight run: %+v", runs[0])
			}
			notifs := collectNotifications(exec)
			if len(notifs) != 1 || notifs[0].EventKind != NotifyPreflightFailed {
				t.Fatalf("expected 1 preflight_failed notification, got %+v", notifs)
			}
		})
	}
}

func TestN5ArchiveOnReadAndPerTaskOptOut(t *testing.T) {
	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	exec := newFakeExecutor()
	engine, err := NewEngine(store, exec, WithPollInterval(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	cwd := t.TempDir()
	// Task 1: default ArchiveOnRead = true
	taskArchive, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name: "归档任务", Prompt: "summary {{date}}", CWD: cwd, Mode: ModeNewSession, PermissionMode: "manual",
		Schedule: ScheduleRule{Kind: "daily", Time: "09:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !taskArchive.ArchiveOnRead {
		t.Fatalf("expected ArchiveOnRead default true for new_session")
	}

	// Task 2: ArchiveOnRead explicitly disabled
	falseVal := false
	taskKeep, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name: "不归档任务", Prompt: "keep", CWD: cwd, Mode: ModeNewSession, PermissionMode: "manual",
		ArchiveOnRead: &falseVal,
		Schedule:      ScheduleRule{Kind: "daily", Time: "09:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if taskKeep.ArchiveOnRead {
		t.Fatalf("expected ArchiveOnRead false when opted out")
	}

	run1, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", taskArchive.ID)
	if err != nil {
		t.Fatal(err)
	}
	run2, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", taskKeep.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", "", 10)
		return len(runs) == 2 && runs[0].Status == RunCompleted && runs[1].Status == RunCompleted
	})

	// Check session title prefix has "[定时] " and no emoji.
	exec.mu.Lock()
	for _, s := range exec.sessions {
		if !strings.HasPrefix(s.Title, "[定时] ") {
			t.Fatalf("expected [定时] prefix on session title, got %q", s.Title)
		}
	}
	exec.mu.Unlock()

	// Mark run1 read -> should archive its session.
	rawPayload, _ := json.Marshal(map[string]any{"id": run1.ID, "read": true})
	res, err := engine.Action(context.Background(), "kimicode-plus.scheduler", "schedule.markRead", rawPayload)
	if err != nil {
		t.Fatal(err)
	}
	resMap := res.(map[string]any)
	if resMap["archived"] != true {
		t.Fatalf("expected archived=true, got %+v", resMap)
	}

	// Mark run2 read -> should NOT archive because task opted out.
	rawPayload2, _ := json.Marshal(map[string]any{"id": run2.ID, "read": true})
	res2, err := engine.Action(context.Background(), "kimicode-plus.scheduler", "schedule.markRead", rawPayload2)
	if err != nil {
		t.Fatal(err)
	}
	if res2.(map[string]any)["archived"] != false {
		t.Fatalf("expected opt-out task not archived, got %+v", res2)
	}
	exec.mu.Lock()
	if len(exec.archivedIDs) != 1 {
		t.Fatalf("expected 1 archived session, got %v", exec.archivedIDs)
	}
	exec.mu.Unlock()

	// Test batch archive on markAllRead + error tolerance.
	run3, _ := engine.RunNow(context.Background(), "kimicode-plus.scheduler", taskArchive.ID)
	waitUntil(t, func() bool {
		r, _ := store.GetRun(context.Background(), "kimicode-plus.scheduler", run3.ID)
		return r.Status == RunCompleted
	})
	exec.archiveErr = errors.New("官方服务暂时繁忙")
	allRes, err := engine.Action(context.Background(), "kimicode-plus.scheduler", "schedule.markAllRead", nil)
	if err != nil {
		t.Fatalf("markAllRead must not fail when archive errors: %v", err)
	}
	allMap := allRes.(map[string]any)
	if allMap["unreadCount"] != 0 || !strings.Contains(allMap["archiveError"].(string), "官方服务暂时繁忙") {
		t.Fatalf("expected unread=0 with recorded archiveError, got %+v", allMap)
	}
}

func TestN5YoloDirtyGitRequiresConfirmation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not found")
	}
	repoDir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v (%s)", args, err, string(out))
		}
	}
	runGit("init")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("clean\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "init")

	db := openTestDB(t)
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	execFake := newFakeExecutor()
	engine, err := NewEngine(store, execFake)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	// Clean repo allows yolo without confirmation.
	cleanTask, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name: "干净仓库免审批", Prompt: "check", CWD: repoDir, Mode: ModeNewSession, PermissionMode: "yolo",
		Schedule: ScheduleRule{Kind: "daily", Time: "09:00"},
	})
	if err != nil {
		t.Fatalf("clean repo should allow yolo: %v", err)
	}

	// Dirty the worktree.
	if err := os.WriteFile(filepath.Join(repoDir, "dirty.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Saving yolo without ConfirmDirtyYolo must fail.
	_, err = engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name: "脏仓库免审批", Prompt: "check", CWD: repoDir, Mode: ModeNewSession, PermissionMode: "yolo",
		Schedule: ScheduleRule{Kind: "daily", Time: "09:00"},
	})
	if err == nil || !strings.Contains(err.Error(), "未提交改动") {
		t.Fatalf("expected dirty git error without confirmation, got %v", err)
	}

	// Preflight on previously saved cleanTask (AllowDirtyYolo=false) must catch dirty worktree at runtime!
	if _, err := engine.RunNow(context.Background(), "kimicode-plus.scheduler", cleanTask.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", cleanTask.ID, 5)
		return len(runs) == 1 && runs[0].Status == RunFailed
	})
	runs, _ := store.ListRuns(context.Background(), "kimicode-plus.scheduler", cleanTask.ID, 5)
	if runs[0].FailureKind != FailurePreflight || !strings.Contains(runs[0].Message, "未提交改动") {
		t.Fatalf("expected runtime preflight failure on dirty yolo repo, got %+v", runs[0])
	}

	// Saving with ConfirmDirtyYolo=true succeeds and marks AllowDirtyYolo.
	confirmed, err := engine.SaveTask(context.Background(), "kimicode-plus.scheduler", SaveTaskInput{
		Name: "确认风险免审批", Prompt: "check", CWD: repoDir, Mode: ModeNewSession, PermissionMode: "yolo",
		ConfirmDirtyYolo: true,
		Schedule:         ScheduleRule{Kind: "daily", Time: "09:00"},
	})
	if err != nil {
		t.Fatalf("confirmed dirty yolo should succeed: %v", err)
	}
	if !confirmed.AllowDirtyYolo {
		t.Fatalf("expected AllowDirtyYolo=true on confirmed task")
	}
}
