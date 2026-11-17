package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enhancedkimi/internal/desktop"
	"enhancedkimi/internal/provider"
	"enhancedkimi/internal/storage"
)

type plannerGeneratorFunc func(context.Context, string, []provider.Message, []map[string]any, string, func(provider.StreamEvent) error) error

func (f plannerGeneratorFunc) Stream(ctx context.Context, model string, messages []provider.Message, tools []map[string]any, session string, emit func(provider.StreamEvent) error) error {
	return f(ctx, model, messages, tools, session, emit)
}

func newTestPlanner(t *testing.T, generator PlannerGenerator) (*PlannerService, *sql.DB, string) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "planner.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	root := t.TempDir()
	planner, err := NewPlanner(db, generator, root, desktop.NewEventBus())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(planner.Close)
	return planner, db, root
}

func plannerCall(name, arguments string) provider.ToolCallDelta {
	index := 0
	return provider.ToolCallDelta{Index: &index, ID: "call-1", Type: "function", Function: &provider.FunctionDelta{Name: &name, Arguments: &arguments}}
}

func createPlannerThread(t *testing.T, planner *PlannerService, options PlannerOptions) string {
	t.Helper()
	value, err := planner.PlannerAction(context.Background(), "planner.create", []byte(`{"model":"synthetic","options":`+mustPlannerJSON(options)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	return value.(PlannerThread).ID
}

func mustPlannerJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func plannerThread(t *testing.T, planner *PlannerService, id string) PlannerThread {
	t.Helper()
	value, err := planner.PlannerAction(context.Background(), "planner.get", []byte(`{"id":"`+id+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	return value.(PlannerThread)
}

func waitPlanner(t *testing.T, planner *PlannerService, id string, predicate func(PlannerThread) bool) PlannerThread {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		thread := plannerThread(t, planner, id)
		if predicate(thread) {
			return thread
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("planner state timeout: " + mustPlannerJSON(plannerThread(t, planner, id)))
	return PlannerThread{}
}

func sendPlanner(t *testing.T, planner *PlannerService, id string) {
	t.Helper()
	_, err := planner.PlannerAction(context.Background(), "planner.send", []byte(`{"id":"`+id+`","text":"规划这个文件"}`))
	if err != nil {
		t.Fatal(err)
	}
}

func TestPlannerStreamsAndApprovesEveryWrite(t *testing.T) {
	var calls atomic.Int32
	generator := plannerGeneratorFunc(func(ctx context.Context, model string, messages []provider.Message, tools []map[string]any, session string, emit func(provider.StreamEvent) error) error {
		if calls.Add(1) == 1 {
			if err := emit(provider.StreamEvent{Text: "准备写入", Finish: "tool_calls", ToolCalls: []provider.ToolCallDelta{plannerCall("write_file", `{"path":"note.txt","content":"from tool"}`)}}); err != nil {
				return err
			}
			return nil
		}
		var last provider.Message
		for _, message := range messages {
			if message.Role == "tool" {
				last = message
			}
		}
		if !strings.Contains(last.Content, "已按用户批准写入") {
			t.Errorf("approved tool result not returned to model: %+v", last)
		}
		return emit(provider.StreamEvent{Text: "已完成", Finish: "stop"})
	})
	planner, _, root := newTestPlanner(t, generator)
	id := createPlannerThread(t, planner, PlannerOptions{Tools: true})
	sendPlanner(t, planner, id)
	pending := waitPlanner(t, planner, id, func(thread PlannerThread) bool { return thread.Approval != nil })
	if _, err := os.Stat(filepath.Join(root, "note.txt")); !os.IsNotExist(err) {
		t.Fatal("write happened before approval")
	}
	_, err := planner.PlannerAction(context.Background(), "planner.approve", []byte(`{"id":"`+id+`","approvalId":"`+pending.Approval.ID+`","allow":true}`))
	if err != nil {
		t.Fatal(err)
	}
	thread := waitPlanner(t, planner, id, func(thread PlannerThread) bool { return thread.Status == "idle" })
	if len(thread.Messages) != 4 || thread.Messages[1].Text != "准备写入" || thread.Messages[3].Text != "已完成" {
		t.Fatalf("unexpected persisted messages: %+v", thread.Messages)
	}
	if data, err := os.ReadFile(filepath.Join(root, "note.txt")); err != nil || string(data) != "from tool" {
		t.Fatalf("approved write missing: %q %v", data, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("unexpected model calls: %d", calls.Load())
	}
}

func TestPlannerReadonlyRefusesWrite(t *testing.T) {
	var calls atomic.Int32
	generator := plannerGeneratorFunc(func(ctx context.Context, model string, messages []provider.Message, tools []map[string]any, session string, emit func(provider.StreamEvent) error) error {
		for _, tool := range tools {
			if tool["function"].(map[string]any)["name"] == "write_file" {
				t.Error("readonly planner received write tool")
			}
		}
		if calls.Add(1) == 1 {
			return emit(provider.StreamEvent{Finish: "tool_calls", ToolCalls: []provider.ToolCallDelta{plannerCall("write_file", `{"path":"blocked.txt","content":"no"}`)}})
		}
		return emit(provider.StreamEvent{Text: "已停止写入", Finish: "stop"})
	})
	planner, _, root := newTestPlanner(t, generator)
	id := createPlannerThread(t, planner, PlannerOptions{Tools: true, ReadOnly: true})
	sendPlanner(t, planner, id)
	waitPlanner(t, planner, id, func(thread PlannerThread) bool { return thread.Status == "idle" })
	if _, err := os.Stat(filepath.Join(root, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatal("readonly write was not blocked")
	}
}

func TestPlannerStopAndRestartDoNotReplay(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	var once sync.Once
	generator := plannerGeneratorFunc(func(ctx context.Context, model string, messages []provider.Message, tools []map[string]any, session string, emit func(provider.StreamEvent) error) error {
		calls.Add(1)
		once.Do(func() { close(started) })
		if err := emit(provider.StreamEvent{Text: "部分回复"}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	})
	planner, db, root := newTestPlanner(t, generator)
	id := createPlannerThread(t, planner, PlannerOptions{})
	sendPlanner(t, planner, id)
	<-started
	restarted, err := NewPlanner(db, generator, root, desktop.NewEventBus())
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	recovered := plannerThread(t, restarted, id)
	if recovered.Status != "error" || recovered.Approval != nil || !strings.Contains(recovered.Error, "不会自动重发") {
		t.Fatalf("unsafe recovery: %+v", recovered)
	}
	if calls.Load() != 1 {
		t.Fatalf("recovery replayed provider call: %d", calls.Load())
	}
	if _, err := planner.stop(context.Background(), mustPlannerState(t, planner, id)); err != nil {
		t.Fatal(err)
	}
	stopped := plannerThread(t, planner, id)
	if stopped.Status != "idle" || !strings.Contains(stopped.Error, "已停止") || stopped.Messages[1].Text != "部分回复" {
		t.Fatalf("stop lost partial state: %+v", stopped)
	}
}

func mustPlannerState(t *testing.T, planner *PlannerService, id string) *plannerState {
	t.Helper()
	state, err := planner.state(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestPlannerRetryPreservesFailureWithoutDuplicateUserMessage(t *testing.T) {
	var calls atomic.Int32
	generator := plannerGeneratorFunc(func(ctx context.Context, model string, messages []provider.Message, tools []map[string]any, session string, emit func(provider.StreamEvent) error) error {
		if calls.Add(1) == 1 {
			if err := emit(provider.StreamEvent{Text: "部分回复"}); err != nil {
				return err
			}
			return &provider.StatusError{Status: http.StatusTooManyRequests}
		}
		users := 0
		for _, message := range messages {
			if message.Role == "user" {
				users++
			}
			if message.Role == "assistant" && strings.Contains(message.Content, "部分回复") {
				t.Error("failed partial assistant message was sent back as complete history")
			}
		}
		if users != 1 {
			t.Errorf("retry duplicated user messages: %+v", messages)
		}
		return emit(provider.StreamEvent{Text: "恢复完成", Finish: "stop"})
	})
	planner, db, root := newTestPlanner(t, generator)
	id := createPlannerThread(t, planner, PlannerOptions{})
	sendPlanner(t, planner, id)
	failed := waitPlanner(t, planner, id, func(thread PlannerThread) bool { return thread.Status == "quota" })
	if len(failed.Messages) != 2 || !failed.Messages[1].Failed || failed.Messages[1].Text != "部分回复" {
		t.Fatalf("failed partial reply not preserved: %+v", failed.Messages)
	}
	if _, err := planner.PlannerAction(context.Background(), "planner.retry", []byte(`{"id":"`+id+`","revision":1}`)); err == nil {
		t.Fatal("stale recovery revision was accepted")
	}
	restarted, err := NewPlanner(db, generator, root, desktop.NewEventBus())
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if _, err = restarted.PlannerAction(context.Background(), "planner.retry", []byte(`{"id":"`+id+`","revision":`+strconv.FormatUint(failed.Revision, 10)+`}`)); err != nil {
		t.Fatal(err)
	}
	completed := waitPlanner(t, restarted, id, func(thread PlannerThread) bool { return thread.Status == "idle" })
	if calls.Load() != 2 || len(completed.Messages) != 3 || completed.Messages[2].Text != "恢复完成" {
		t.Fatalf("retry did not continue original request: calls=%d messages=%+v", calls.Load(), completed.Messages)
	}
	if completed.Workspace != root {
		t.Fatalf("retry lost original workspace: %q", completed.Workspace)
	}
}

func TestPlannerPathRejectsEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside.txt", outside} {
		if _, err := plannerPath(root, path, false); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err == nil {
		if _, err := plannerPath(root, "link.txt", false); err == nil {
			t.Fatal("accepted symlink escape")
		}
	}
}

// A thread remembers the plugin that created it, and stopping that plugin ends
// only its own in-flight work: built-in threads and other plugins keep streaming.
func TestStopPluginThreadsOnlyTouchesItsOwnWork(t *testing.T) {
	release := make(chan struct{})
	var started atomic.Int32
	generator := plannerGeneratorFunc(func(ctx context.Context, model string, messages []provider.Message, tools []map[string]any, session string, emit func(provider.StreamEvent) error) error {
		if err := emit(provider.StreamEvent{Text: "开始"}); err != nil {
			return err
		}
		started.Add(1)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return emit(provider.StreamEvent{Text: "结束", Finish: "stop"})
		}
	})
	planner, _, _ := newTestPlanner(t, generator)

	create := func(owner string) string {
		payload := map[string]any{"model": "synthetic", "options": PlannerOptions{}}
		if owner != "" {
			payload["pluginOwner"] = owner
		}
		value, err := planner.PlannerAction(context.Background(), "planner.create", []byte(mustPlannerJSON(payload)))
		if err != nil {
			t.Fatal(err)
		}
		return value.(PlannerThread).ID
	}
	pluginThread := create("kimicode-plus.probe")
	otherThread := create("kimicode-plus.other")
	builtinThread := create("")

	for _, id := range []string{pluginThread, otherThread, builtinThread} {
		sendPlanner(t, planner, id)
	}
	waitPlanner(t, planner, pluginThread, func(thread PlannerThread) bool { return thread.Status == "running" })
	waitPlanner(t, planner, otherThread, func(thread PlannerThread) bool { return thread.Status == "running" })

	stopped, err := planner.StopPluginThreads(context.Background(), "kimicode-plus.probe")
	if err != nil {
		t.Fatal(err)
	}
	if stopped != 1 {
		t.Fatalf("StopPluginThreads stopped %d threads, want 1", stopped)
	}
	thread := plannerThread(t, planner, pluginThread)
	if thread.Status != "stopped" || thread.PluginOwner != "kimicode-plus.probe" {
		t.Fatalf("plugin thread after stop = %+v", thread)
	}
	if thread.Error == "" {
		t.Fatal("a stopped thread kept no reason")
	}
	// History is kept: the partial reply is still there.
	if len(thread.Messages) < 2 {
		t.Fatalf("stopping dropped the conversation: %+v", thread.Messages)
	}
	for _, id := range []string{otherThread, builtinThread} {
		if live := plannerThread(t, planner, id); live.Status != "running" {
			t.Fatalf("stopping one plugin stopped a thread it did not own (%s): %+v", id, live)
		}
	}
	close(release)
}

// The owner field is what Disable acts on, so a plugin cannot claim another
// plugin's thread by naming its owner: the plugin layer overwrites the field.
func TestPlannerOwnerComesFromTheCallingPlugin(t *testing.T) {
	generator := plannerGeneratorFunc(func(ctx context.Context, model string, messages []provider.Message, tools []map[string]any, session string, emit func(provider.StreamEvent) error) error {
		return emit(provider.StreamEvent{Text: "ok", Finish: "stop"})
	})
	planner, _, _ := newTestPlanner(t, generator)
	value, err := planner.PlannerAction(context.Background(), "planner.create", []byte(`{"model":"synthetic","options":{},"pluginOwner":"kimicode-plus.claims"}`))
	if err != nil {
		t.Fatal(err)
	}
	if owner := value.(PlannerThread).PluginOwner; owner != "kimicode-plus.claims" {
		t.Fatalf("explicit owner was not stored: %q", owner)
	}
	if stopped, err := planner.StopPluginThreads(context.Background(), ""); err != nil || stopped != 0 {
		t.Fatalf("an empty owner stopped %d threads (%v)", stopped, err)
	}
}
