package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"enhancedkimi/internal/provider"
)

func TestInternalPlannerStreamUsesRouteAndLedger(t *testing.T) {
	fixture := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"choices":[{"delta":{"content":"第一段"}}]}`,
			`{"choices":[{"delta":{"content":"第二段"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":8,"completion_tokens":4}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", event)
			w.(http.Flusher).Flush()
		}
		io.WriteString(w, "data: [DONE]\n\n")
	})
	fixture.declareModel(t, "kimi", "kimi-k2")
	var text strings.Builder
	err := fixture.gateway.Stream(context.Background(), "kimi-k2", []provider.Message{{Role: "user", Content: "test"}}, nil, "planner-test", func(event provider.StreamEvent) error {
		text.WriteString(event.Text)
		return nil
	})
	if err != nil || text.String() != "第一段第二段" {
		t.Fatalf("stream %q: %v", text.String(), err)
	}
	var sessionID string
	var output int64
	if err := fixture.db.QueryRow(`SELECT session_id,output FROM usage_events WHERE agent_id='planner'`).Scan(&sessionID, &output); err != nil || sessionID != "planner-test" || output != 4 {
		t.Fatalf("ledger %q %d: %v", sessionID, output, err)
	}
}
