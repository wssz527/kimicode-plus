package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"enhancedkimi/internal/storage"
)

// The pickers show provider, capabilities and reasoning efforts next to the
// display name. Losing any of them turns a rich option into a bare model id, so
// this covers the whole catalog entry rather than just its identifier.
func TestModelsCarriesTheFieldsThePickersDisplay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeNativeTestEnvelope(w, map[string]any{"models": map[string]any{
			"team/alpha": map[string]any{
				"provider":          "team",
				"model":             "alpha",
				"display_name":      "Alpha",
				"max_context_size":  200000,
				"capabilities":      []any{"image_in", "tool_use"},
				"support_efforts":   []any{"low", "high"},
				"adaptive_thinking": true,
			},
			// An entry that repeats the provider prefix in its own model field must
			// still collapse to one key, not become team/team/alpha.
			"team/beta": map[string]any{"model": "team/beta"},
			// A bare key from an older config keeps working.
			"legacy": map[string]any{"display_name": "Legacy"},
		}})
	}))
	t.Cleanup(server.Close)

	kap := NewKAPClient("")
	kap.base, kap.token = server.URL, "synthetic"
	models, err := kap.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]NativeModel{}
	for _, model := range models {
		byID[model.ID] = model
	}
	alpha, ok := byID["team/alpha"]
	if !ok {
		t.Fatalf("team/alpha missing from %#v", models)
	}
	if alpha.Provider != "team" || alpha.Model != "alpha" || alpha.Name != "Alpha" {
		t.Fatalf("alpha identity = %#v", alpha)
	}
	if alpha.MaxContextSize != 200000 {
		t.Fatalf("alpha max context = %d", alpha.MaxContextSize)
	}
	if len(alpha.Capabilities) != 2 || alpha.Capabilities[0] != "image_in" {
		t.Fatalf("alpha capabilities = %#v", alpha.Capabilities)
	}
	if len(alpha.SupportEfforts) != 2 || alpha.SupportEfforts[1] != "high" {
		t.Fatalf("alpha support efforts = %#v", alpha.SupportEfforts)
	}
	if !alpha.AdaptiveThinking {
		t.Fatal("alpha lost adaptive_thinking")
	}

	beta, ok := byID["team/beta"]
	if !ok {
		t.Fatalf("the model key that already carried the prefix was renamed: %#v", models)
	}
	if beta.Provider != "team" || beta.Model != "beta" {
		t.Fatalf("beta identity = %#v", beta)
	}

	legacy, ok := byID["legacy"]
	if !ok || legacy.Name != "Legacy" || legacy.Provider != "" {
		t.Fatalf("legacy entry = %#v", byID)
	}
}

// A richer catalog still has to answer models.list without a planner or the
// enhancement's config reader.
func TestModelsListPublishesTheWholeCatalogEntry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeNativeTestEnvelope(w, map[string]any{"models": map[string]any{
			"team/alpha": map[string]any{
				"display_name":    "Alpha",
				"capabilities":    []any{"image_in"},
				"support_efforts": []any{"low"},
			},
		}})
	}))
	t.Cleanup(server.Close)

	db, err := storage.Open(filepath.Join(t.TempDir(), "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	kap := NewKAPClient("")
	kap.base, kap.token = server.URL, "synthetic"
	service, err := NewNativeService(db, kap, t.TempDir(), func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)

	value, err := service.Action(context.Background(), "models.list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	items := value.(map[string]any)["items"].([]NativeModel)
	if len(items) != 1 {
		t.Fatalf("models.list = %#v", items)
	}
	item := items[0]
	if item.Provider != "team" || item.Capabilities[0] != "image_in" || item.SupportEfforts[0] != "low" {
		t.Fatalf("models.list dropped catalog fields: %#v", item)
	}
}
