package plugins

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"enhancedkimi/internal/chat"
	"enhancedkimi/internal/desktop"
	"enhancedkimi/internal/provider"
	"enhancedkimi/internal/storage"
)

// streamGate is a planner generator that starts streaming and waits, so a test
// can observe a thread that would keep running until something stops it.
type streamGate struct {
	release chan struct{}
	once    sync.Once
	started chan struct{}
}

func newStreamGate() *streamGate {
	return &streamGate{release: make(chan struct{}), started: make(chan struct{})}
}

func (g *streamGate) Stream(ctx context.Context, model string, messages []provider.Message, tools []map[string]any, session string, emit func(provider.StreamEvent) error) error {
	if err := emit(provider.StreamEvent{Text: "开始"}); err != nil {
		return err
	}
	g.once.Do(func() { close(g.started) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.release:
		return emit(provider.StreamEvent{Text: "结束", Finish: "stop"})
	}
}

// plannerHarness wires a real planner into a real manager through the same hook
// the server attaches, so these tests exercise the production path rather than a
// counter standing in for it.
type plannerHarness struct {
	manager *Manager
	planner *chat.PlannerService
	gate    *streamGate
}

func newPlannerHarness(t *testing.T) *plannerHarness {
	t.Helper()
	root := t.TempDir()
	db, err := storage.Open(root + "/plugins.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	manager, err := New(db, func() bool { return false }, WithDataDir(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	manager.UseCore(&pluginCoreDouble{})
	gate := newStreamGate()
	planner, err := chat.NewPlanner(db, gate, root, desktop.NewEventBus())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(planner.Close)
	manager.UseStopPluginWork(func(ctx context.Context, id string) error {
		_, stopErr := planner.StopPluginThreads(ctx, id)
		return stopErr
	})
	return &plannerHarness{manager: manager, planner: planner, gate: gate}
}

func (h *plannerHarness) call(t *testing.T, action string, payload map[string]any) any {
	t.Helper()
	data, _ := json.Marshal(payload)
	value, err := h.planner.PlannerAction(context.Background(), action, data)
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	return value
}

func (h *plannerHarness) createThread(t *testing.T, owner string) string {
	t.Helper()
	payload := map[string]any{"model": "synthetic", "options": map[string]any{}}
	if owner != "" {
		payload["pluginOwner"] = owner
	}
	return h.call(t, "planner.create", payload).(chat.PlannerThread).ID
}

func (h *plannerHarness) thread(t *testing.T, id string) chat.PlannerThread {
	t.Helper()
	return h.call(t, "planner.get", map[string]any{"id": id}).(chat.PlannerThread)
}

func (h *plannerHarness) waitRunning(t *testing.T, id string) chat.PlannerThread {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		thread := h.thread(t, id)
		if thread.Status == "running" {
			return thread
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("thread %s never started running", id)
	return chat.PlannerThread{}
}

// A faulted plugin must not keep planning. The fatal path is the only stop a
// plugin gets when its activation throws, so the planner thread it started has
// to be marked stopped and its history kept, exactly like a disable.
func TestFatalReportStopsThePluginsPlannerThread(t *testing.T) {
	harness := newPlannerHarness(t)
	bundles := map[string]map[string][]byte{
		"test.official":  officialBundle("test.official", []string{CapPlannerRun}),
		"test.bystander": officialBundle("test.bystander", []string{CapPlannerRun}),
	}
	publishOfficial(t, harness.manager, bundles)
	token := enableOfficial(t, harness.manager, "test.official")

	pluginThread := harness.createThread(t, "test.official")
	bystanderThread := harness.createThread(t, "test.bystander")
	builtinThread := harness.createThread(t, "")
	for _, id := range []string{pluginThread, bystanderThread, builtinThread} {
		harness.call(t, "planner.send", map[string]any{"id": id, "text": "规划这个文件"})
	}
	harness.waitRunning(t, pluginThread)
	harness.waitRunning(t, bystanderThread)

	if _, err := harness.manager.Call(context.Background(), "test.official", "report.error", token, reportRequest(t, map[string]any{
		"message": "activate blew up", "code": "PLUGIN_ACTIVATION_FAILED", "fatal": true,
	})); err != nil {
		t.Fatalf("fatal report: %v", err)
	}

	stopped := harness.thread(t, pluginThread)
	if stopped.Status != "stopped" {
		t.Fatalf("the faulted plugin's thread kept running: %+v", stopped)
	}
	if stopped.Error == "" || !strings.Contains(stopped.Error, "插件已停止") {
		t.Fatalf("the stopped thread kept no reason: %q", stopped.Error)
	}
	// History is kept: a stopped thread is not a deleted one.
	if len(stopped.Messages) < 2 {
		t.Fatalf("stopping dropped the conversation: %+v", stopped.Messages)
	}
	// The other plugin's thread and the built-in thread are not this stop's to end.
	for _, id := range []string{bystanderThread, builtinThread} {
		if live := harness.thread(t, id); live.Status != "running" {
			t.Fatalf("the fault stopped a thread it did not own (%s): %+v", id, live)
		}
	}
	close(harness.gate.release)
}

// Safe mode stops every running plugin, so the planner work those plugins
// started ends too — and only theirs.
func TestSafeModeStopsThePlannerWorkOfThePluginsItStops(t *testing.T) {
	safe := false
	root := t.TempDir()
	db, err := storage.Open(root + "/plugins.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	manager, err := New(db, func() bool { return safe }, WithDataDir(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	manager.UseCore(&pluginCoreDouble{})
	gate := newStreamGate()
	planner, err := chat.NewPlanner(db, gate, root, desktop.NewEventBus())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(planner.Close)
	manager.UseStopPluginWork(func(ctx context.Context, id string) error {
		_, stopErr := planner.StopPluginThreads(ctx, id)
		return stopErr
	})
	harness := &plannerHarness{manager: manager, planner: planner, gate: gate}

	publishOfficial(t, manager, map[string]map[string][]byte{
		"test.running": officialBundle("test.running", []string{CapPlannerRun}),
		"test.other":   officialBundle("test.other", []string{CapPlannerRun}),
	})
	enableOfficial(t, manager, "test.running")
	enableOfficial(t, manager, "test.other")

	stoppedThread := harness.createThread(t, "test.other")
	harness.call(t, "planner.send", map[string]any{"id": stoppedThread, "text": "旧线程"})
	harness.waitRunning(t, stoppedThread)
	// A plugin that stops itself ends the thread it owns; safe mode then finds
	// nothing live there.
	if err := manager.Disable(context.Background(), "test.other"); err != nil {
		t.Fatal(err)
	}
	before := harness.thread(t, stoppedThread)

	// A thread the running plugin started, plus a built-in one nothing owns.
	runningThread := harness.createThread(t, "test.running")
	builtinThread := harness.createThread(t, "")
	harness.call(t, "planner.send", map[string]any{"id": runningThread, "text": "仍在运行"})
	harness.call(t, "planner.send", map[string]any{"id": builtinThread, "text": "内置"})
	harness.waitRunning(t, runningThread)

	safe = true
	manager.EnforceSafeMode()

	if live := harness.thread(t, runningThread); live.Status != "stopped" {
		t.Fatalf("safe mode left the stopped plugin's thread running: %+v", live)
	}
	if live := harness.thread(t, builtinThread); live.Status != "running" {
		t.Fatalf("safe mode stopped a built-in thread: %+v", live)
	}
	after := harness.thread(t, stoppedThread)
	if after.Status != before.Status || after.Error != before.Error || len(after.Messages) != len(before.Messages) {
		t.Fatalf("safe mode touched a thread whose plugin was already stopped: before=%+v after=%+v", before, after)
	}
	close(gate.release)
}
