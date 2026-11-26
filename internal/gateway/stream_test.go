package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"enhancedkimi/internal/provider"
)

// The registry planner path reads only what production writes: the per-model
// `output` and `thinking` capability rows. metadata_json never carries planner
// fields, so no effort level or custom header can reach this path.
func TestInternalPlannerStreamUsesRouteAndLedger(t *testing.T) {
	fixture := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"choices":[{"delta":{"content":"第一段"}}]}`,
			`{"choices":[{"delta":{"content":"第二段"},"finish_reason":"length"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":8,"completion_tokens":4}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", event)
			w.(http.Flusher).Flush()
		}
		io.WriteString(w, "data: [DONE]\n\n")
	})
	fixture.declareModel(t, "kimi", "kimi-k2")
	if _, err := fixture.db.Exec(`CREATE TABLE registry_capabilities(provider_id TEXT,model_id TEXT,field TEXT,value_number REAL,value_boolean INTEGER);
		INSERT INTO registry_capabilities VALUES('kimi','kimi-k2','output',12000,NULL),('kimi','kimi-k2','thinking',NULL,1)`); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var finish string
	err := fixture.gateway.Stream(context.Background(), "kimi/kimi-k2", []provider.Message{{Role: "user", Content: "test"}}, nil, "planner-test", func(event provider.StreamEvent) error {
		text.WriteString(event.Text)
		if event.Finish != "" {
			finish = event.Finish
		}
		return nil
	})
	if err != nil || text.String() != "第一段第二段" || finish != "length" {
		t.Fatalf("stream %q: %v", text.String(), err)
	}
	upstream := *fixture.lastBody
	// The output capability row drives max_tokens. The thinking capability row
	// is honored only on Kimi/Moonshot endpoints, and this fixture host is not
	// one: the private parameter must not leave it, and the registry has no
	// effort source, so reasoning_effort stays away too.
	if upstream["model"] != "kimi-k2" || upstream["max_tokens"] != float64(12000) {
		t.Fatalf("planner output limit was not sent: %#v", upstream)
	}
	for _, key := range []string{"thinking", "reasoning_effort"} {
		if _, ok := upstream[key]; ok {
			t.Fatalf("%s reached a non-Kimi endpoint: %#v", key, upstream)
		}
	}
	var sessionID string
	var output int64
	if err := fixture.db.QueryRow(`SELECT session_id,output FROM usage_events WHERE agent_id='planner'`).Scan(&sessionID, &output); err != nil || sessionID != "planner-test" || output != 4 {
		t.Fatalf("ledger %q %d: %v", sessionID, output, err)
	}
}

// Buffered and synthetic-SSE share one finish mapping: an output-limit cut
// must surface as `length` on both, never as a plain stop.
func TestSyntheticCompletionMapsOutputLimitToLength(t *testing.T) {
	cases := []struct{ protocol, model, path, reply string }{
		{"anthropic", "claude-x", "/v1/messages", `{"content":[{"text":"截断"}],"stop_reason":"max_tokens","usage":{"input_tokens":6,"output_tokens":3}}`},
		{"gemini", "gemini-x", "/models/gemini-x:generateContent", `{"candidates":[{"content":{"parts":[{"text":"截断"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3}}`},
		{"responses", "responses-x", "/responses", `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"content":[{"text":"截断"}]}],"usage":{"input_tokens":7,"output_tokens":3}}`},
	}
	for _, tc := range cases {
		t.Run(tc.protocol, func(t *testing.T) {
			f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tc.reply)
			})
			if _, err := f.db.Exec(`UPDATE registry_providers SET protocol=? WHERE id='kimi'`, tc.protocol); err != nil {
				t.Fatal(err)
			}
			f.declareModel(t, "kimi", tc.model)
			buffered := f.completion(t, fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"x"}]}`, tc.model))
			if buffered.Code != http.StatusOK || !strings.Contains(buffered.Body.String(), `"finish_reason":"length"`) {
				t.Fatalf("buffered truncation lost: %d %s", buffered.Code, buffered.Body.String())
			}
			stream := f.completion(t, fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"x"}],"stream":true}`, tc.model))
			if stream.Code != http.StatusOK || !strings.Contains(stream.Body.String(), `"finish_reason":"length"`) {
				t.Fatalf("synthetic truncation lost: %d %s", stream.Code, stream.Body.String())
			}
		})
	}
}

// A native tool stop whose tool calls the buffered parser never extracts must
// not be answered with tool_calls: the client would wait for tool results that
// do not exist.
func TestSyntheticCompletionDoesNotInventToolCalls(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"content":[{"text":"部分"},{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":6,"output_tokens":3}}`)
	})
	if _, err := f.db.Exec(`UPDATE registry_providers SET protocol='anthropic' WHERE id='kimi'`); err != nil {
		t.Fatal(err)
	}
	f.declareModel(t, "kimi", "claude-x")
	w := f.completion(t, `{"model":"claude-x","messages":[{"role":"user","content":"x"}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("completion %d: %s", w.Code, w.Body.String())
	}
	var payload struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []any  `json:"tool_calls"`
			} `json:"message"`
			Finish string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Choices[0].Finish != "stop" || len(payload.Choices[0].Message.ToolCalls) != 0 {
		t.Fatalf("unparsed tool stop became a tool turn: %s", w.Body.String())
	}
	if payload.Choices[0].Message.Content != "部分" {
		t.Fatalf("text lost: %s", w.Body.String())
	}
}
