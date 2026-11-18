package desktop

import (
	"strings"
	"time"
)

// activityEvent is one sanitized session event for the Monitor panel. It carries
// the shape the upstream widget expects (a KAP-style `turn.step.completed`
// payload) and nothing else: no message text, no tool input, no tool output and
// no reasoning. The panel draws counters and an animation from these fields
// only, and a plugin holding `sessions:read` must not receive conversation
// bodies through the event stream it was granted for counters.
type activityEvent struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id"`
	AgentID   string          `json:"agent_id,omitempty"`
	TurnID    string          `json:"turn_id,omitempty"`
	Payload   map[string]any  `json:"payload,omitempty"`
}

// toolPhases are the item stages that mean "a tool is being called right now".
// The panel's rule is that the pet keeps its loading animation and its clock
// through a tool call, so only the start and the settling stages are published.
var toolPhases = map[string]string{
	"started":    "tool.call.started",
	"streaming":  "tool.call.started",
	"running":    "tool.call.started",
	"ended":      "tool.result",
	"completed":  "tool.result",
	"failed":     "tool.result",
	"cancelled":  "tool.result",
	"aborted":    "tool.result",
}

// activityForFrame translates one official frame into a sanitized event, or
// returns nil when the frame carries nothing the panel counts. The bus sequence
// is not copied here: the event envelope already carries it, and the adapter adds
// it back as `seq` so a duplicate replay is dropped the same way the upstream
// widget drops one.
func activityForFrame(frame NativeFrame) *activityEvent {
	if frame.SessionID == "" {
		return nil
	}
	agent := frame.AgentID
	if agent == "" {
		agent = "main"
	}
	base := activityEvent{SessionID: frame.SessionID, AgentID: agent, TurnID: frame.TurnID}
	switch frame.Type {
	case "turn.started":
		if agent != "main" {
			base.Type = "subagent.started"
			base.Payload = map[string]any{"subagentId": agent}
			return &base
		}
		base.Type = "turn.started"
		return &base
	case "turn.ended":
		if agent != "main" {
			base.Type = "subagent.completed"
			if frame.Turn.Error != nil {
				base.Type = "subagent.failed"
			}
			base.Payload = map[string]any{"subagentId": agent}
			return &base
		}
		base.Type = "turn.ended"
		if frame.Turn.Error != nil {
			base.Payload = map[string]any{"state": "failed", "errorCode": frame.Turn.Error.Code}
		} else {
			base.Payload = map[string]any{"state": "completed"}
		}
		return &base
	case "item.started", "item.updated", "item.ended":
		// Only a tool call's lifecycle is published. Assistant text, reasoning and
		// notifications all arrive as item frames too, and none of them belong in a
		// counter event.
		if frame.Item.Type != "tool_call" {
			return nil
		}
		kind, failed, ok := toolActivityKind(frame)
		if !ok {
			return nil
		}
		base.Type = kind
		base.Payload = map[string]any{"toolName": sanitizeToolName(frame.Item.Name)}
		if kind == "tool.result" && failed {
			base.Payload["ok"] = false
		}
		return &base
	}
	return nil
}

// toolActivityKind maps a tool_call item frame to its activity event type.
// KAP 1.3.7 sends status ("running"/"ended") and outcome ("completed"/"failed"/
// "interrupted") while stage may be "input" or empty; checking both status and
// stage avoids missing live tool calls and prevents item.updated patches from
// double-counting activeToolCalls in the panel.
func toolActivityKind(frame NativeFrame) (kind string, failed bool, ok bool) {
	stage := strings.ToLower(frame.Item.Stage)
	status := strings.ToLower(frame.Item.Status)
	outcome := strings.ToLower(frame.Item.Outcome)
	phaseKind, phaseKnown := toolPhases[stage]
	isFailed := frame.Item.Error != nil ||
		outcome == "failed" || outcome == "interrupted" ||
		stage == "failed" || stage == "cancelled" || stage == "aborted"
	if frame.Type == "item.ended" || status == "ended" || (phaseKnown && phaseKind == "tool.result") {
		return "tool.result", isFailed, true
	}
	if frame.Type == "item.started" || status == "running" || (phaseKnown && phaseKind == "tool.call.started") {
		return "tool.call.started", false, true
	}
	return "", false, false
}

// sanitizeToolName keeps a tool name short and printable. A tool name comes from
// the model's own tool call, so it is data, not a label the core chose.
func sanitizeToolName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) > 40 {
		runes = runes[:40]
	}
	for index, r := range runes {
		if r < 0x20 || r == 0x7f {
			runes[index] = '_'
		}
	}
	return string(runes)
}

// publishActivity sends the sanitized event on the `activity` scope, which is
// mapped to `metrics:read` and never carries conversation text. Keeping it off
// `session:<id>` lets the panel watch turn and tool lifecycles without holding
// `sessions:read`.
func (s *NativeService) publishActivity(frame NativeFrame) {
	event := activityForFrame(frame)
	if event == nil {
		return
	}
	s.bus.Publish("activity", event)
}

// activityScan reports the newest visible turn for a session so the panel can
// tell a live turn from a replay without reading the transcript.
func (s *NativeService) activityScan(sessionID string) (map[string]any, bool) {
	s.mu.Lock()
	cache, ok := s.cache[sessionID]
	s.mu.Unlock()
	if !ok {
		return nil, false
	}
	return map[string]any{
		"busy":        cache.session.Busy,
		"revision":    cache.revision,
		"lastTouched": cache.lastActivity.UTC().Format(time.RFC3339Nano),
	}, true
}