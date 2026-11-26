package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlannerNativeProtocolsContinueSignedToolTurn(t *testing.T) {
	cases := []struct {
		protocol, model, path, firstReply, lastReply string
		requestParts, continuationParts              []string
	}{
		{
			"anthropic", "claude-test", "/v1/messages",
			`{"content":[{"type":"thinking","thinking":"检查项目","signature":"signature-a"},{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"go.mod"}}],"stop_reason":"tool_use","usage":{"input_tokens":5}}`,
			`{"content":[{"type":"text","text":"实施步骤"}],"stop_reason":"max_tokens","usage":{"output_tokens":12}}`,
			[]string{`"system":"系统提示"`, `"max_tokens":14000`, `"thinking":{"type":"adaptive"}`, `"output_config":{"effort":"high"}`, `"input_schema":`},
			[]string{`"signature":"signature-a"`, `"tool_use_id":"toolu_1"`, `"type":"tool_result"`},
		},
		{
			"gemini", "gemini-3-pro", "/v1beta/models/gemini-3-pro:generateContent",
			`{"candidates":[{"content":{"parts":[{"text":"检查项目","thought":true},{"functionCall":{"name":"read_file","args":{"path":"go.mod"}},"thoughtSignature":"signature-g"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5}}`,
			`{"candidates":[{"content":{"parts":[{"text":"实施步骤"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"candidatesTokenCount":12}}`,
			[]string{`"systemInstruction":`, `"maxOutputTokens":14000`, `"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"HIGH"}`, `"functionDeclarations":`},
			[]string{`"thoughtSignature":"signature-g"`, `"functionResponse":{"name":"read_file"`, `"role":"model"`},
		},
		{
			"responses", "gpt-test", "/responses",
			`{"status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"检查项目"}],"encrypted_content":"signature-r"},{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"go.mod\"}"}],"usage":{"input_tokens":5}}`,
			`{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"实施步骤"}]}],"usage":{"output_tokens":12}}`,
			[]string{`"instructions":"系统提示"`, `"max_output_tokens":14000`, `"reasoning":{"effort":"high","summary":"auto"}`, `"strict":false`},
			[]string{`"encrypted_content":"signature-r"`, `"call_id":"call_1"`, `"type":"function_call_output"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.protocol, func(t *testing.T) {
			requests := make(chan string, 2)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path || r.Header.Get("X-Planner") != "planner" {
					t.Errorf("unexpected planner request %s: %s", r.URL.Path, r.Header.Get("X-Planner"))
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request: %v", err)
				}
				if strings.Contains(string(body), "module example") {
					fmt.Fprint(w, tc.lastReply)
				} else {
					fmt.Fprint(w, tc.firstReply)
				}
				requests <- string(body)
			}))
			defer upstream.Close()
			base := upstream.URL
			if tc.protocol == "gemini" {
				base += "/v1beta"
			}
			cfg := Config{ID: tc.protocol, Protocol: tc.protocol, BaseURL: base, CustomHeaders: map[string]string{"X-Planner": "planner"}}
			options := PlannerModelOptions{Thinking: "toggle", AdaptiveThinking: true, DefaultEffort: "high", MaxOutput: 14000}
			messages := []Message{{Role: "system", Content: "系统提示"}, {Role: "user", Content: "先看代码"}}
			tools := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}}
			ctx := context.Background()
			client := New(nil, nil)
			var event StreamEvent
			emit := func(value StreamEvent) error { event = value; return nil }
			if err := client.StreamPlanner(ctx, cfg, tc.model, options, messages, tools, emit); err != nil {
				t.Fatal(err)
			}
			assertPlannerRequestParts(t, <-requests, tc.requestParts)
			if event.Finish != "tool_calls" || event.Reasoning != "检查项目" || event.PlannerContext == nil || len(event.ToolCalls) != 1 || len(event.Usage) == 0 {
				t.Fatalf("planner tool turn could not be read: %+v", event)
			}
			call := event.ToolCalls[0]
			var args map[string]string
			if call.Function == nil || *call.Function.Name != "read_file" || json.Unmarshal([]byte(*call.Function.Arguments), &args) != nil || args["path"] != "go.mod" {
				t.Fatalf("invalid tool call: %+v", call)
			}
			messages = append(messages,
				Message{Role: "assistant", PlannerContext: event.PlannerContext, ToolCalls: []ToolCall{{ID: call.ID, Type: "function", Function: ToolCallFunction{Name: *call.Function.Name, Arguments: *call.Function.Arguments}}}},
				Message{Role: "tool", ToolCallID: call.ID, Content: "module example"},
			)
			if err := client.StreamPlanner(ctx, cfg, tc.model, options, messages, tools, emit); err != nil {
				t.Fatal(err)
			}
			assertPlannerRequestParts(t, <-requests, tc.continuationParts)
			if event.Finish != "length" || event.Text != "实施步骤" || event.PlannerContext != nil {
				t.Fatalf("unexpected final planner turn: %+v", event)
			}
		})
	}
}

// The strict-endpoint contract: an OpenAI-protocol planner request carries only
// parameters the model config verified. The adopted constraints:
//   - max_tokens is sent only from a configured output limit (official model
//     readback, registry `output` capability); the 8192 default exists solely
//     on Kimi/Moonshot endpoints where the parameter is documented.
//   - the Kimi-private `thinking` parameter never leaves Kimi/Moonshot hosts,
//     decided by endpoint identity, not by model ID.
//   - reasoning_effort is sent only for an effort the model config declares.
func TestPlannerOpenAIRequestOnlySendsVerifiedCapabilities(t *testing.T) {
	messages := []Message{{Role: "user", Content: "检查"}}
	tools := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}}
	cases := []struct {
		name, baseURL string
		options       PlannerModelOptions
		present       []string
		absent        []string
	}{
		{"generic host omits unverified limit and private thinking", "https://api.openai.com/v1",
			PlannerModelOptions{Thinking: "toggle", AdaptiveThinking: true, DefaultEffort: "high", SupportEfforts: []string{"high"}},
			[]string{`"reasoning_effort":"high"`},
			[]string{"max_tokens", "thinking"}},
		{"configured output limit is sent on a strict endpoint", "https://api.openai.com/v1",
			PlannerModelOptions{Thinking: "toggle", AdaptiveThinking: true, MaxOutput: 4096},
			[]string{`"max_tokens":4096`},
			[]string{"thinking", "reasoning_effort"}},
		{"no declared effort, no effort parameter", "https://api.openai.com/v1",
			PlannerModelOptions{Thinking: "toggle"},
			nil,
			[]string{"max_tokens", "thinking", "reasoning_effort"}},
		{"kimi keeps the verified default limit and thinking", "https://api.moonshot.cn/v1",
			PlannerModelOptions{Thinking: "toggle"},
			[]string{`"max_tokens":8192`, `"thinking":{"type":"enabled"}`},
			[]string{"reasoning_effort"}},
		{"kimi thinking off suppresses thinking and effort", "https://api.moonshot.ai/v1",
			PlannerModelOptions{Thinking: "none", DefaultEffort: "high", SupportEfforts: []string{"high"}},
			nil,
			[]string{"thinking", "reasoning_effort"}},
		{"kimi configured limit wins over the default", "https://api.kimi.com/coding/v1",
			PlannerModelOptions{Thinking: "toggle", AdaptiveThinking: true, MaxOutput: 12000},
			[]string{`"max_tokens":12000`, `"thinking":{"type":"enabled"}`},
			nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := plannerOpenAIRequest(Config{ID: "p", Protocol: "openai", BaseURL: tc.baseURL}, "m", tc.options, messages, tools)
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range tc.present {
				if !strings.Contains(string(encoded), field) {
					t.Errorf("request missing %s: %s", field, encoded)
				}
			}
			for _, field := range tc.absent {
				if strings.Contains(string(encoded), field) {
					t.Errorf("request carried unverified %s: %s", field, encoded)
				}
			}
		})
	}
}

func TestPlannerOpenAIStreamKeepsStrictEndpointsUntouched(t *testing.T) {
	var body map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"部分\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	options := PlannerModelOptions{Thinking: "toggle", AdaptiveThinking: true}
	var event StreamEvent
	if err := New(nil, nil).StreamPlanner(context.Background(), Config{ID: "p", Protocol: "openai", BaseURL: upstream.URL}, "gpt-test", options, []Message{{Role: "user", Content: "检查"}}, nil, func(value StreamEvent) error {
		event.Text += value.Text
		if value.Finish != "" {
			event.Finish = value.Finish
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"max_tokens", "thinking", "reasoning_effort", "tools", "tool_choice"} {
		if _, ok := body[key]; ok {
			t.Fatalf("unverified %s sent to a strict endpoint: %v", key, body)
		}
	}
	if event.Text != "部分" || event.Finish != "length" {
		t.Fatalf("stream not relayed: %+v", event)
	}
}

func assertPlannerRequestParts(t *testing.T, request string, fields []string) {
	t.Helper()
	for _, field := range fields {
		if !strings.Contains(request, field) {
			t.Errorf("request missing %s: %s", field, request)
		}
	}
}

func TestPlannerGeminiReconstructedToolResultDoesNotSendUnpairedID(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "检查项目"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "from-another-provider", Function: ToolCallFunction{Name: "read_file", Arguments: `{"path":"go.mod"}`}}}},
		{Role: "tool", ToolCallID: "from-another-provider", Content: "module example"},
	}
	contents := plannerNativeInput(Config{Protocol: "gemini"}, "gemini-3-pro", messages)
	raw, err := json.Marshal(contents)
	if err != nil || !strings.Contains(string(raw), `"functionResponse":{"name":"read_file"`) || strings.Contains(string(raw), "from-another-provider") {
		t.Fatalf("Gemini continuation has a mismatched tool ID: %s: %v", raw, err)
	}
}
