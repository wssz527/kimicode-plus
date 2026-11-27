package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"enhancedkimi/internal/registry"
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

type testPublicModelCatalog func(context.Context, string, string, string) (map[string]registry.Capability, error)

func (lookup testPublicModelCatalog) FindPublicModel(ctx context.Context, provider, endpoint, model string) (map[string]registry.Capability, error) {
	return lookup(ctx, provider, endpoint, model)
}

func TestModelSuggestionUsesSavedProviderEndpointAndExactModel(t *testing.T) {
	store := newOfficialModelStore(map[string]modelConfig{
		"r2-inspect/family/beta": {Provider: "r2-inspect", Model: "family/beta", MaxContextSize: 65536},
	})
	service, _ := newAutomationTestService(t, store.handler())
	called := 0
	service.UsePublicModelCatalog(testPublicModelCatalog(func(ctx context.Context, provider, endpoint, model string) (map[string]registry.Capability, error) {
		called++
		if provider != "r2-inspect" || endpoint != "https://example.invalid/v1" || model != "family/beta" {
			t.Fatalf("catalog lookup received %q %q %q", provider, endpoint, model)
		}
		return map[string]registry.Capability{
			"vision": {Value: true}, "video": {Value: false}, "tools": {Value: true},
			"thinking": {Value: true}, "output": {Value: float64(4096)}, "context": {Value: float64(8192)},
		}, nil
	}))
	for _, model := range []string{"other/family/beta", "r2-inspect/beta", "r2-inspect/family/BETA"} {
		_, err := service.Action(context.Background(), "providers.modelSuggestion", json.RawMessage(`{"providerId":"r2-inspect","model":"`+model+`"}`))
		if err == nil {
			t.Fatalf("accepted inexact model %q", model)
		}
	}
	value, err := service.Action(context.Background(), "providers.modelSuggestion", json.RawMessage(`{"providerId":"r2-inspect","model":"r2-inspect/family/beta","baseUrl":"https://ignored.invalid/v1"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil || string(raw) != `{"suggestion":{"image_in":true,"max_context_size":8192,"max_output_size":4096,"thinking":true,"tool_use":true,"video_in":false}}` || called != 1 || len(store.bodies) != 0 {
		t.Fatalf("catalog result = %s, requests = %d, writes = %d, error = %v", raw, called, len(store.bodies), err)
	}
}

type testPublicModelBatchCatalog struct {
	single func(context.Context, string, string, string) (map[string]registry.Capability, error)
	batch  func(context.Context, string, string, []string) (map[string]map[string]registry.Capability, error)
}

func (c testPublicModelBatchCatalog) FindPublicModel(ctx context.Context, provider, endpoint, model string) (map[string]registry.Capability, error) {
	return c.single(ctx, provider, endpoint, model)
}

func (c testPublicModelBatchCatalog) FindPublicModels(ctx context.Context, provider, endpoint string, models []string) (map[string]map[string]registry.Capability, error) {
	return c.batch(ctx, provider, endpoint, models)
}

func TestModelSuggestionBatchUsesBatchCatalogAndReturnsContextAndCapabilities(t *testing.T) {
	store := newOfficialModelStore(map[string]modelConfig{
		"r2-inspect/alpha": {Provider: "r2-inspect", Model: "alpha", MaxContextSize: 256000},
		"r2-inspect/beta":  {Provider: "r2-inspect", Model: "beta", MaxContextSize: 256000},
	})
	service, _ := newAutomationTestService(t, store.handler())
	singleCalls := 0
	batchCalls := 0
	service.UsePublicModelCatalog(testPublicModelBatchCatalog{
		single: func(ctx context.Context, provider, endpoint, model string) (map[string]registry.Capability, error) {
			singleCalls++
			return nil, nil
		},
		batch: func(ctx context.Context, provider, endpoint string, models []string) (map[string]map[string]registry.Capability, error) {
			batchCalls++
			if provider != "r2-inspect" || endpoint != "https://example.invalid/v1" {
				t.Fatalf("unexpected provider/endpoint %q %q", provider, endpoint)
			}
			if len(models) != 2 || models[0] != "alpha" || models[1] != "beta" {
				t.Fatalf("batch received models = %#v, want [alpha beta]", models)
			}
			return map[string]map[string]registry.Capability{
				"alpha": {
					"vision":   {Value: true},
					"tools":    {Value: true},
					"thinking": {Value: false},
					"context":  {Value: float64(128000)},
					"output":   {Value: float64(16384)},
				},
			}, nil
		},
	})

	if _, err := service.Action(context.Background(), "providers.modelSuggestion", json.RawMessage(`{"providerId":"r2-inspect","models":["other/alpha"]}`)); err == nil {
		t.Fatal("accepted foreign model key in batch suggestion")
	}
	if _, err := service.Action(context.Background(), "providers.modelSuggestion", json.RawMessage(`{"providerId":"managed:kimi-code","models":["managed:kimi-code/alpha"]}`)); err == nil {
		t.Fatal("accepted managed provider in batch suggestion")
	}
	if _, err := service.Action(context.Background(), "providers.modelSuggestion", json.RawMessage(`{"providerId":"r2-inspect","models":[]}`)); err == nil {
		t.Fatal("accepted empty batch models")
	}

	value, err := service.Action(context.Background(), "providers.modelSuggestion", json.RawMessage(`{
		"providerId": "r2-inspect",
		"models": ["r2-inspect/alpha", "r2-inspect/unsaved", "r2-inspect/beta", "r2-inspect/alpha"]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"suggestions":{"alpha":{"image_in":true,"max_context_size":128000,"max_output_size":16384,"thinking":false,"tool_use":true}}}`
	if string(raw) != want || batchCalls != 1 || singleCalls != 0 || len(store.bodies) != 0 {
		t.Fatalf("batch result = %s (want %s), batchCalls = %d, singleCalls = %d, writes = %d", raw, want, batchCalls, singleCalls, len(store.bodies))
	}
}
