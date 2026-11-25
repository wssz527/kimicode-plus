package provider

import "strings"

// StreamingMode describes how the HTTP gateway can stream a supported protocol.
func StreamingMode(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "openai":
		return "native"
	case "responses", "anthropic", "gemini":
		return "synthetic"
	default:
		return ""
	}
}

func (r Reply) CompletionFinish() string {
	if len(r.ToolCalls) > 0 && (r.FinishReason == "" || r.FinishReason == "stop") {
		return "tool_calls"
	}
	if r.FinishReason == "" {
		return "stop"
	}
	return r.FinishReason
}

func (r Reply) CompletionDelta() StreamEvent {
	event := StreamEvent{Text: r.Text, Reasoning: r.Reasoning}
	for index, call := range r.ToolCalls {
		name, arguments := call.Function.Name, call.Function.Arguments
		event.ToolCalls = append(event.ToolCalls, ToolCallDelta{
			Index: &index, ID: call.ID, Type: call.Type,
			Function: &FunctionDelta{Name: &name, Arguments: &arguments},
		})
	}
	return event
}
