package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"enhancedkimi/internal/provider"
)

func TestInternalPlannerStreamUsesRouteAndLedger(t *testing.T) {
	var customHeader string
	fixture := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		customHeader = r.Header.Get("X-Planner-Test")
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
	if _, err := fixture.db.Exec(`ALTER TABLE registry_models ADD COLUMN metadata_json TEXT NOT NULL DEFAULT '{}';
		CREATE TABLE registry_capabilities(provider_id TEXT,model_id TEXT,field TEXT,value_number REAL,value_boolean INTEGER);
		UPDATE registry_models SET metadata_json='{"defaultEffort":"medium","supportEfforts":["low","medium"],"customHeaders":{"X-Planner-Test":"registry"}}';
		INSERT INTO registry_capabilities VALUES('kimi','kimi-k2','thinking',NULL,1),('kimi','kimi-k2','output',12000,NULL)`); err != nil {
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
	if upstream["model"] != "kimi-k2" || upstream["max_tokens"] != float64(12000) || upstream["reasoning_effort"] != "medium" ||
		!reflect.DeepEqual(upstream["thinking"], map[string]any{"type": "enabled"}) || customHeader != "registry" {
		t.Fatalf("planner model options were not sent: %#v header=%q", upstream, customHeader)
	}
	var sessionID string
	var output int64
	if err := fixture.db.QueryRow(`SELECT session_id,output FROM usage_events WHERE agent_id='planner'`).Scan(&sessionID, &output); err != nil || sessionID != "planner-test" || output != 4 {
		t.Fatalf("ledger %q %d: %v", sessionID, output, err)
	}
}
