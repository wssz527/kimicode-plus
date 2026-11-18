package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"enhancedkimi/internal/plugins"
)

// The Monitor panel draws counters and an animation from these events. It must
// never receive conversation bodies through the stream it was granted for
// counters, so activityForFrame is the only place a frame is reduced to an
// event, and this file pins what survives that reduction.

func TestActivityPublishesOnlyTurnAndToolLifecycle(t *testing.T) {
	frame := NativeFrame{
		Type:      "turn.started",
		SessionID: "s1",
		TurnID:    "t1",
	}
	event := activityForFrame(frame)
	if event == nil || event.Type != "turn.started" {
		t.Fatalf("turn.started dropped: %#v", event)
	}
	if event.AgentID != "main" {
		t.Fatalf("an empty agent must be recorded as main, got %q", event.AgentID)
	}

	ended := activityForFrame(NativeFrame{Type: "turn.ended", SessionID: "s1", TurnID: "t1"})
	if ended == nil || ended.Payload["state"] != "completed" {
		t.Fatalf("turn.ended payload = %#v", ended)
	}
	failed := activityForFrame(NativeFrame{
		Type: "turn.ended", SessionID: "s1",
		Turn: NativeTurn{Error: &NativeTurnError{Code: "provider.rate_limit"}},
	})
	if failed == nil || failed.Payload["state"] != "failed" || failed.Payload["errorCode"] != "provider.rate_limit" {
		t.Fatalf("a failed turn must carry its code, got %#v", failed)
	}
}

func TestActivityDropsAssistantTextAndReasoning(t *testing.T) {
	// Every one of these frames carries text the panel has no use for. An item
	// frame that is not a tool call must produce nothing at all: a single leak
	// here would put assistant output into a counters-only stream.
	for _, itemType := range []string{"assistant_message", "reasoning", "notification", "error"} {
		frame := NativeFrame{
			Type:      "item.ended",
			SessionID: "s1",
			Item:      NativeItem{Type: itemType, Stage: "completed", Text: "机密会话正文"},
		}
		if event := activityForFrame(frame); event != nil {
			t.Fatalf("%s produced an activity event: %#v", itemType, event)
		}
	}
}

func TestActivityToolEventsCarryNoInputOrOutput(t *testing.T) {
	frame := NativeFrame{
		Type:      "item.started",
		SessionID: "s1",
		Item: NativeItem{
			Type:  "tool_call",
			Stage: "started",
			Name:  "Bash",
			Input: map[string]any{"command": "cat ~/.ssh/id_rsa"},
			// Output 与 Input 都是会话内容，事件里不能出现
			Output: "private-key-material",
		},
	}
	event := activityForFrame(frame)
	if event == nil || event.Type != "tool.call.started" {
		t.Fatalf("tool start dropped: %#v", event)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"id_rsa", "private-key-material", "command"} {
		if strings.Contains(string(encoded), leaked) {
			t.Fatalf("activity event leaked %q: %s", leaked, encoded)
		}
	}
	if event.Payload["toolName"] != "Bash" {
		t.Fatalf("toolName = %v", event.Payload["toolName"])
	}
}

func TestActivityToolResultReportsFailureWithoutTheError(t *testing.T) {
	frame := NativeFrame{
		Type:      "item.ended",
		SessionID: "s1",
		Item: NativeItem{
			Type:  "tool_call",
			Stage: "failed",
			Name:  "Read",
			Error: "permission denied: /Users/someone/.env",
		},
	}
	event := activityForFrame(frame)
	if event == nil || event.Type != "tool.result" {
		t.Fatalf("tool result dropped: %#v", event)
	}
	if ok, present := event.Payload["ok"]; !present || ok != false {
		t.Fatalf("a failed tool result must say so, got %#v", event.Payload)
	}
	encoded, _ := json.Marshal(event)
	if strings.Contains(string(encoded), ".env") {
		t.Fatalf("tool error text leaked into the event: %s", encoded)
	}
}

func TestActivityToolNameIsBoundedAndPrintable(t *testing.T) {
	long := strings.Repeat("工具", 40)
	event := activityForFrame(NativeFrame{
		Type:      "item.started",
		SessionID: "s1",
		Item:      NativeItem{Type: "tool_call", Stage: "started", Name: long + "\x00\x07"},
	})
	name, _ := event.Payload["toolName"].(string)
	if len([]rune(name)) > 40 {
		t.Fatalf("toolName not truncated: %d runes", len([]rune(name)))
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("toolName kept a control character: %q", name)
		}
	}
}

func TestActivityNeedsASession(t *testing.T) {
	// A frame with no session cannot be routed to a panel, and publishing it on
	// "session::activity" would make it visible to every subscriber.
	if event := activityForFrame(NativeFrame{Type: "turn.started"}); event != nil {
		t.Fatalf("unsessioned frame produced %#v", event)
	}
}

func TestActivityScopeNeedsMetricsReadNotSessionsRead(t *testing.T) {
	// The panel holds metrics:read only. If the activity scope ever required
	// sessions:read it would also hand the panel conversation bodies it does not
	// render, so the gate itself is part of the contract.
	metrics, err := plugins.ParseCapabilities([]string{plugins.CapMetricsRead})
	if err != nil {
		t.Fatal(err)
	}
	if !plugins.EventScopeAllowed(metrics, "activity") {
		t.Fatal("metrics:read must open the activity scope")
	}
	if plugins.EventScopeAllowed(plugins.Grants{}, "activity") {
		t.Fatal("a plugin without the grant must not read activity")
	}
	// 完整会话流仍然需要 sessions:read：活动作用域不能成为绕过它的门
	if plugins.EventScopeAllowed(metrics, "session:abc") {
		t.Fatal("the activity grant must not open a session transcript scope")
	}
}

func TestActivitySubagentLifecycle(t *testing.T) {
	started := activityForFrame(NativeFrame{
		Type:      "turn.started",
		SessionID: "s1",
		AgentID:   "agent-1",
		TurnID:    "t2",
	})
	if started == nil || started.Type != "subagent.started" || started.AgentID != "agent-1" {
		t.Fatalf("subagent start = %#v", started)
	}
	if started.Payload["subagentId"] != "agent-1" {
		t.Fatalf("subagent start payload = %#v", started.Payload)
	}

	completed := activityForFrame(NativeFrame{
		Type:      "turn.ended",
		SessionID: "s1",
		AgentID:   "agent-1",
		TurnID:    "t2",
	})
	if completed == nil || completed.Type != "subagent.completed" || completed.Payload["subagentId"] != "agent-1" {
		t.Fatalf("subagent completed = %#v", completed)
	}

	failed := activityForFrame(NativeFrame{
		Type:      "turn.ended",
		SessionID: "s1",
		AgentID:   "agent-1",
		TurnID:    "t2",
		Turn:      NativeTurn{Error: &NativeTurnError{Code: "upstream_error"}},
	})
	if failed == nil || failed.Type != "subagent.failed" || failed.Payload["subagentId"] != "agent-1" {
		t.Fatalf("subagent failed = %#v", failed)
	}
}

func TestApplyFramePublishesSubagentActivity(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeNativeTestEnvelope(w, map[string]any{"items": []any{}})
	})
	service, _ := newAutomationTestServiceWithHome(t, handler, t.TempDir())
	service.applyFrame(NativeFrame{
		Type:      "turn.started",
		SessionID: "s-sub",
		AgentID:   "agent-7",
		TurnID:    "t-sub",
	})
	service.applyFrame(NativeFrame{
		Type:      "turn.ended",
		SessionID: "s-sub",
		AgentID:   "agent-7",
		TurnID:    "t-sub",
	})
	page := service.bus.Poll(context.Background(), 0, []string{"activity"}, 10*time.Millisecond)
	if len(page.Events) != 2 {
		t.Fatalf("activity events = %d, want 2: %#v", len(page.Events), page.Events)
	}
	first, _ := page.Events[0].Value.(*activityEvent)
	second, _ := page.Events[1].Value.(*activityEvent)
	if first == nil || first.Type != "subagent.started" || first.Payload["subagentId"] != "agent-7" {
		t.Fatalf("first event = %#v", page.Events[0])
	}
	if second == nil || second.Type != "subagent.completed" || second.Payload["subagentId"] != "agent-7" {
		t.Fatalf("second event = %#v", page.Events[1])
	}
}

func TestActivityHandlesKAPToolStatusAndOutcome(t *testing.T) {
	started := activityForFrame(NativeFrame{
		Type:      "item.started",
		SessionID: "s1",
		TurnID:    "t1",
		Item: NativeItem{
			Type:   "tool_call",
			ItemID: "tc1",
			Name:   "Glob",
			Status: "running",
			Stage:  "input",
		},
	})
	if started == nil || started.Type != "tool.call.started" || started.Payload["toolName"] != "Glob" {
		t.Fatalf("started event = %#v", started)
	}

	if patchOnly := activityForFrame(NativeFrame{
		Type:      "item.updated",
		SessionID: "s1",
		TurnID:    "t1",
		ItemID:    "tc1",
		Set:       json.RawMessage(`{"stage":"awaiting_approval"}`),
	}); patchOnly != nil {
		t.Fatalf("patch-only item.updated must not emit a duplicate start: %#v", patchOnly)
	}

	completed := activityForFrame(NativeFrame{
		Type:      "item.ended",
		SessionID: "s1",
		TurnID:    "t1",
		Item: NativeItem{
			Type:    "tool_call",
			ItemID:  "tc1",
			Name:    "Glob",
			Status:  "ended",
			Outcome: "completed",
		},
	})
	if completed == nil || completed.Type != "tool.result" {
		t.Fatalf("completed event = %#v", completed)
	}
	if _, hasOK := completed.Payload["ok"]; hasOK {
		t.Fatalf("completed tool result should not carry ok=false: %#v", completed.Payload)
	}

	failed := activityForFrame(NativeFrame{
		Type:      "item.ended",
		SessionID: "s1",
		TurnID:    "t1",
		Item: NativeItem{
			Type:    "tool_call",
			ItemID:  "tc2",
			Name:    "Bash",
			Status:  "ended",
			Outcome: "failed",
		},
	})
	if failed == nil || failed.Type != "tool.result" || failed.Payload["ok"] != false {
		t.Fatalf("failed outcome = %#v", failed)
	}
}
