package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The buffered gateway path relays Complete's finish reason, so an output-limit
// cut that arrives as a native completion reason must surface as `length`
// instead of being flattened into a normal stop. Only documented reasons are
// translated: a normal end keeps its plain stop, and anything the buffered
// parser does not extract (native tool calls) is never guessed into tool_calls.
func TestCompleteMapsNativeCompletionReasons(t *testing.T) {
	cases := []struct {
		name, protocol, reply string
		want                  string
	}{
		{"responses output limit", "responses", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"content":[{"type":"output_text","text":"部分"}]}],"usage":{"output_tokens":9}}`, "length"},
		{"responses completed stays stop", "responses", `{"status":"completed","output":[{"content":[{"type":"output_text","text":"完成"}]}]}`, ""},
		{"responses incomplete without the output-limit reason stays untranslated", "responses", `{"status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[{"content":[{"type":"output_text","text":"部分"}]}]}`, ""},
		{"anthropic output limit", "anthropic", `{"content":[{"type":"text","text":"部分"}],"stop_reason":"max_tokens","usage":{"output_tokens":9}}`, "length"},
		{"anthropic normal stop", "anthropic", `{"content":[{"type":"text","text":"完成"}],"stop_reason":"end_turn"}`, ""},
		{"gemini output limit", "gemini", `{"candidates":[{"content":{"parts":[{"text":"部分"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"candidatesTokenCount":9}}`, "length"},
		{"gemini normal stop", "gemini", `{"candidates":[{"content":{"parts":[{"text":"完成"}]},"finishReason":"STOP"}]}`, ""},
		{"openai finish reason passes through", "openai", `{"choices":[{"message":{"content":"部分"},"finish_reason":"length"}]}`, "length"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(tc.reply))
			}))
			defer remote.Close()
			reply, err := New(nil, nil).Complete(context.Background(), Config{ID: "p", Protocol: tc.protocol, BaseURL: remote.URL}, "m", nil, []Message{{Role: "user", Content: "x"}})
			if err != nil {
				t.Fatal(err)
			}
			if reply.FinishReason != tc.want {
				t.Fatalf("FinishReason = %q, want %q", reply.FinishReason, tc.want)
			}
			wantFinish := tc.want
			if wantFinish == "" {
				wantFinish = "stop"
			}
			if got := reply.CompletionFinish(); got != wantFinish {
				t.Fatalf("CompletionFinish = %q, want %q", got, wantFinish)
			}
		})
	}
}

// Complete does not extract native tool calls, so a tool stop must be reported
// as a plain stop rather than as tool_calls: a client told tool_calls waits for
// tool results that will never exist. A tool-only native reply keeps failing
// with "no text" — only the OpenAI protocol parses tool calls here.
func TestCompleteDoesNotInventToolCallsFromUnparsedToolStops(t *testing.T) {
	cases := []struct{ protocol, reply string }{
		{"responses", `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"部分"}]},{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"}]}`},
		{"anthropic", `{"content":[{"type":"text","text":"部分"},{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}],"stop_reason":"tool_use"}`},
		{"gemini", `{"candidates":[{"content":{"parts":[{"text":"部分"},{"functionCall":{"name":"lookup","args":{}}}]},"finishReason":"STOP"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.protocol, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(tc.reply))
			}))
			defer remote.Close()
			reply, err := New(nil, nil).Complete(context.Background(), Config{ID: "p", Protocol: tc.protocol, BaseURL: remote.URL}, "m", nil, []Message{{Role: "user", Content: "x"}})
			if err != nil {
				t.Fatal(err)
			}
			if len(reply.ToolCalls) != 0 {
				t.Fatalf("tool calls invented from an unparsed tool stop: %+v", reply.ToolCalls)
			}
			if got := reply.CompletionFinish(); got != "stop" {
				t.Fatalf("CompletionFinish = %q, want stop", got)
			}
		})
	}
}

// A reasoning-only or tool-only reply is still a real answer: the existing
// OpenAI tool path must keep working without text, and its tool_calls finish
// must survive.
func TestCompleteToolOnlyReplyKeepsItsFinish(t *testing.T) {
	var body map[string]any
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`))
	}))
	defer remote.Close()
	reply, err := New(nil, nil).Complete(context.Background(), Config{ID: "p", Protocol: "openai", BaseURL: remote.URL}, "m", nil, []Message{{Role: "user", Content: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reply.ToolCalls) != 1 || body["stream"] != false {
		t.Fatalf("tool-only reply rejected: %+v sent %+v", reply, body)
	}
	if got := reply.CompletionFinish(); got != "tool_calls" {
		t.Fatalf("CompletionFinish = %q, want tool_calls", got)
	}
}
