package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"enhancedkimi/internal/provider"
)

func TestStreamKAPModelStreamsOfficialCatalogModelWithoutPersistingSecret(t *testing.T) {
	var seenAuth, seenModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		seenModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"1\",\"model\":\"synthetic-fast\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"# 规划摘要\\n- 步骤一\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"1\",\"model\":\"synthetic-fast\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()

	kapHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
			writeNativeTestEnvelope(w, map[string]any{
				"providers": map[string]any{
					"synthetic_official": map[string]any{
						"type":     "openai",
						"base_url": upstream.URL + "/v1",
					},
				},
				"models": map[string]any{
					"synthetic_official/synthetic-fast": map[string]any{
						"provider":       "synthetic_official",
						"model":          "synthetic-fast",
						"maxContextSize": 262144,
					},
				},
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/providers/synthetic_official"):
			writeNativeTestEnvelope(w, map[string]any{"api_key": "sk-ephemeral-test"})
		default:
			http.NotFound(w, r)
		}
	})

	service, _ := newAutomationTestService(t, kapHandler)
	var collected strings.Builder
	handled, err := service.StreamKAPModel(
		context.Background(),
		"synthetic_official/synthetic-fast",
		[]provider.Message{{Role: "user", Content: "请归纳这段规划对话"}},
		nil,
		func(ev provider.StreamEvent) error {
			collected.WriteString(ev.Text)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("StreamKAPModel: %v", err)
	}
	if !handled {
		t.Fatal("expected KAP model to be handled")
	}
	if seenAuth != "Bearer sk-ephemeral-test" {
		t.Fatalf("unexpected Authorization header: %q", seenAuth)
	}
	if seenModel != "synthetic-fast" {
		t.Fatalf("unexpected upstream model: %q", seenModel)
	}
	if !strings.Contains(collected.String(), "规划摘要") {
		t.Fatalf("unexpected streamed text: %q", collected.String())
	}

	unknownHandled, unknownErr := service.StreamKAPModel(
		context.Background(),
		"unknown-bare-model",
		[]provider.Message{{Role: "user", Content: "hi"}},
		nil,
		func(provider.StreamEvent) error { return nil },
	)
	if unknownErr != nil || unknownHandled {
		t.Fatalf("expected unknown bare model to fall through: handled=%v err=%v", unknownHandled, unknownErr)
	}
}
