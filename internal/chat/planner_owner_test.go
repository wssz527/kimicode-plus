package chat

import (
	"context"
	"encoding/json"
	"testing"

	"enhancedkimi/internal/provider"
)

// A planner thread created by one plugin belongs to it. Every action that
// rewrites a thread is checked against the caller's owner, so a second plugin
// that learns an id from `planner.threads` — or guesses one — cannot configure,
// send to, retry, stop, approve or delete it. The check lives in the service
// rather than in the plugin layer, because that is where the thread and the
// caller's owner actually meet.
func TestPlannerChangesStayWithTheThreadOwner(t *testing.T) {
	generator := plannerGeneratorFunc(func(ctx context.Context, model string, messages []provider.Message, tools []map[string]any, session string, emit func(provider.StreamEvent) error) error {
		return emit(provider.StreamEvent{Text: "ok", Finish: "stop"})
	})
	planner, _, _ := newTestPlanner(t, generator)
	ownerThread := createOwnedPlannerThread(t, planner, "kimicode-plus.owner")
	otherThread := createOwnedPlannerThread(t, planner, "kimicode-plus.other")
	builtinThread := createOwnedPlannerThread(t, planner, "")

	// Each payload is what a plugin would send for that action. A refusal must
	// arrive before anything changed, so the same call as the real owner still
	// finds the thread untouched.
	changes := []struct {
		action  string
		payload map[string]any
	}{
		{"planner.configure", map[string]any{"model": "synthetic", "options": map[string]any{}}},
		{"planner.rename", map[string]any{"title": "不能替别人改名"}},
		{"planner.send", map[string]any{"text": "替别的插件发起规划"}},
		{"planner.retry", map[string]any{"revision": 1}},
		{"planner.stop", map[string]any{}},
		{"planner.approve", map[string]any{"approvalId": "approval-1", "allow": true}},
		{"planner.delete", map[string]any{}},
	}
	for _, change := range changes {
		for _, target := range []struct{ label, id string }{
			{"another plugin's thread", otherThread},
			{"a built-in thread", builtinThread},
		} {
			payload := map[string]any{"id": target.id, "pluginOwner": "kimicode-plus.owner"}
			for key, value := range change.payload {
				payload[key] = value
			}
			_, err := planner.PlannerAction(context.Background(), change.action, []byte(mustPlannerJSON(payload)))
			if err == nil {
				t.Fatalf("%s changed %s", change.action, target.label)
			}
			if err.Error() != "规划线程不属于该插件" {
				t.Fatalf("%s on %s failed for another reason: %v", change.action, target.label, err)
			}
		}
	}
	// Neither foreign thread noticed any of the attempts.
	for _, id := range []string{otherThread, builtinThread} {
		thread := plannerThread(t, planner, id)
		if thread.Status != "idle" || len(thread.Messages) != 0 || thread.Error != "" {
			t.Fatalf("a refused change left a trace on %s: %+v", id, thread)
		}
	}

	// The same actions work for the plugin that owns the thread.
	if _, err := planner.PlannerAction(context.Background(), "planner.configure", []byte(`{"id":"`+ownerThread+`","pluginOwner":"kimicode-plus.owner","model":"synthetic","options":{}}`)); err != nil {
		t.Fatalf("the owner could not configure its own thread: %v", err)
	}
	if _, err := planner.PlannerAction(context.Background(), "planner.send", []byte(`{"id":"`+ownerThread+`","pluginOwner":"kimicode-plus.owner","text":"规划这个文件"}`)); err != nil {
		t.Fatalf("the owner could not send to its own thread: %v", err)
	}
	waitPlanner(t, planner, ownerThread, func(thread PlannerThread) bool { return thread.Status == "idle" && len(thread.Messages) == 2 })
	if _, err := planner.PlannerAction(context.Background(), "planner.delete", []byte(`{"id":"`+ownerThread+`","pluginOwner":"kimicode-plus.owner"}`)); err != nil {
		t.Fatalf("the owner could not delete its own thread: %v", err)
	}
	if _, err := planner.PlannerAction(context.Background(), "planner.get", []byte(`{"id":"`+ownerThread+`","pluginOwner":"kimicode-plus.owner"}`)); err == nil {
		t.Fatal("the deleted thread was still readable")
	}

	// A built-in caller sends no owner and keeps its older reach across threads.
	if _, err := planner.PlannerAction(context.Background(), "planner.configure", []byte(`{"id":"`+otherThread+`","model":"synthetic","options":{}}`)); err != nil {
		t.Fatalf("the built-in caller lost access to a plugin thread: %v", err)
	}
	if _, err := planner.PlannerAction(context.Background(), "planner.delete", []byte(`{"id":"`+builtinThread+`"}`)); err != nil {
		t.Fatalf("the built-in caller lost access to its own thread: %v", err)
	}

	// Discovery is not a change: a plugin still sees every thread it may act on,
	// which is what makes the refusal above reachable only through the id.
	listed, err := planner.PlannerThreads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	threads, ok := listed.([]PlannerThread)
	if !ok {
		t.Fatalf("planner.threads no longer answers with threads: %T", listed)
	}
	seen := map[string]bool{}
	for _, thread := range threads {
		seen[thread.ID] = true
	}
	if !seen[otherThread] {
		t.Fatalf("planner.threads hid a thread the plugin can see: %#v", threads)
	}
}

func createOwnedPlannerThread(t *testing.T, planner *PlannerService, owner string) string {
	t.Helper()
	payload := map[string]any{"model": "synthetic", "options": PlannerOptions{}}
	if owner != "" {
		payload["pluginOwner"] = owner
	}
	value, err := planner.PlannerAction(context.Background(), "planner.create", []byte(mustPlannerJSON(payload)))
	if err != nil {
		t.Fatal(err)
	}
	thread := value.(PlannerThread)
	if thread.PluginOwner != owner {
		t.Fatalf("thread owner = %q, want %q", thread.PluginOwner, owner)
	}
	return thread.ID
}

// The guard runs before the action reads or writes anything, so an unauthorized
// caller cannot learn a thread's state from how a later step failed.
func TestPlannerRefusesForeignThreadBeforeDoingAnything(t *testing.T) {
	planner, db, _ := newTestPlanner(t, plannerGeneratorFunc(func(ctx context.Context, model string, messages []provider.Message, tools []map[string]any, session string, emit func(provider.StreamEvent) error) error {
		return nil
	}))
	id := createOwnedPlannerThread(t, planner, "kimicode-plus.owner")
	before, err := planner.PlannerAction(context.Background(), "planner.get", []byte(`{"id":"`+id+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"planner.configure", "planner.rename", "planner.send", "planner.retry", "planner.stop", "planner.approve", "planner.delete"} {
		payload, _ := json.Marshal(map[string]any{"id": id, "pluginOwner": "kimicode-plus.stranger", "model": "synthetic", "text": "x", "revision": 1})
		if _, err := planner.PlannerAction(context.Background(), action, payload); err == nil {
			t.Fatalf("%s reached a thread owned by another plugin", action)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM planner_threads WHERE id=?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("a refused change removed the thread: rows = %d", count)
	}
	after, err := planner.PlannerAction(context.Background(), "planner.get", []byte(`{"id":"`+id+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if before.(PlannerThread).Revision != after.(PlannerThread).Revision || len(after.(PlannerThread).Messages) != 0 {
		t.Fatalf("a refused change modified the thread: before=%+v after=%+v", before, after)
	}
}
