package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// officialModelStore stands in for the official KAP /config endpoint. It stores
// models the way the official service does: a PUT replaces the whole array of
// that provider, and a field the body omits is gone afterwards.
type officialModelStore struct {
	mu       sync.Mutex
	provider providerConfig
	models   map[string]modelConfig
	bodies   []string
}

func newOfficialModelStore(models map[string]modelConfig) *officialModelStore {
	return &officialModelStore{
		provider: providerConfig{Type: "openai", BaseURL: "https://example.invalid/v1"},
		models:   models,
	}
}

func (s *officialModelStore) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
			s.mu.Lock()
			models := map[string]modelConfig{}
			for key, model := range s.models {
				models[key] = model
			}
			providers := map[string]providerConfig{"r2-inspect": s.provider}
			s.mu.Unlock()
			writeNativeTestEnvelope(w, map[string]any{"providers": providers, "models": models})
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/providers/"):
			raw := map[string]any{}
			decoder := json.NewDecoder(r.Body)
			decoder.UseNumber()
			if err := decoder.Decode(&raw); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			encoded, _ := json.Marshal(raw)
			s.mu.Lock()
			s.bodies = append(s.bodies, string(encoded))
			replaced := map[string]modelConfig{}
			for key, model := range s.models {
				if model.Provider != "r2-inspect" {
					replaced[key] = model
				}
			}
			for _, item := range rawSlice(raw["models"]) {
				replaced["r2-inspect/"+item["model"].(string)] = modelFromBody(item)
			}
			s.models = replaced
			s.mu.Unlock()
			writeNativeTestEnvelope(w, map[string]any{"provider": "r2-inspect"})
		default:
			http.NotFound(w, r)
		}
	})
}

// modelFromBody mirrors the official mapping: optional fields the body leaves out
// do not come back on the next read.
func modelFromBody(item map[string]any) modelConfig {
	model := modelConfig{Provider: "r2-inspect", Model: item["model"].(string)}
	if size, ok := item["max_context_size"].(json.Number); ok {
		model.MaxContextSize, _ = size.Int64()
	}
	if name, ok := item["display_name"].(string); ok {
		model.DisplayName = name
	}
	if size, ok := item["max_output_size"].(json.Number); ok {
		value, _ := size.Int64()
		model.MaxOutputSize = &value
	}
	if values, ok := item["capabilities"].([]any); ok {
		model.Capabilities = []string{}
		for _, value := range values {
			model.Capabilities = append(model.Capabilities, value.(string))
		}
	}
	if values, ok := item["support_efforts"].([]any); ok {
		model.SupportEfforts = []string{}
		for _, value := range values {
			model.SupportEfforts = append(model.SupportEfforts, value.(string))
		}
	}
	if flag, ok := item["adaptive_thinking"].(bool); ok {
		model.AdaptiveThinking = flag
	}
	return model
}

func rawSlice(raw any) []map[string]any {
	items, _ := raw.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			out = append(out, object)
		}
	}
	return out
}

func inspectModels() map[string]modelConfig {
	return map[string]modelConfig{
		"r2-inspect/alpha": {
			Provider: "r2-inspect", Model: "alpha", DisplayName: "R2 Alpha",
			MaxContextSize: 131072, Capabilities: []string{"image_in", "thinking"},
		},
		"r2-inspect/beta": {
			Provider: "r2-inspect", Model: "beta", DisplayName: "R2 Beta",
			MaxContextSize: 65536, SupportEfforts: []string{"low"},
		},
		"other/gamma": {Provider: "other", Model: "gamma", MaxContextSize: 8192},
	}
}

func updateModelAction(t *testing.T, service *NativeService, payload string) (any, error) {
	t.Helper()
	return service.Action(context.Background(), "providers.updateModel", json.RawMessage(payload))
}

// savedModel decodes the action's result through JSON, so the assertion also
// covers the field names the renderer reads.
func savedModel(t *testing.T, value any) modelConfig {
	t.Helper()
	encoded, err := json.Marshal(value.(map[string]any)["model"])
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	var model modelConfig
	if err = json.Unmarshal(encoded, &model); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return model
}

func TestUpdateProviderModelMergesOntoTheStoredModel(t *testing.T) {
	store := newOfficialModelStore(inspectModels())
	service, _ := newAutomationTestService(t, store.handler())
	value, err := updateModelAction(t, service, `{"providerId":"r2-inspect","model":"r2-inspect/alpha","edit":{
		"capabilities":["tool_use","image_in"],"thinking":"always","adaptive_thinking":true,
		"max_context_size":200000,"support_efforts":["high"," high ","","low"]}}`)
	if err != nil {
		t.Fatalf("updateModel: %v", err)
	}
	actual := savedModel(t, value)
	if actual.DisplayName != "R2 Alpha" {
		t.Fatalf("display name was not preserved: %#v", actual)
	}
	if !sameStrings(actual.Capabilities, []string{"image_in", "tool_use", "always_thinking"}) {
		t.Fatalf("capabilities not normalised: %v", actual.Capabilities)
	}
	if actual.AdaptiveThinking {
		t.Fatalf("adaptive thinking survived a non-toggle mode: %#v", actual)
	}
	if !sameStrings(actual.SupportEfforts, []string{"high", "low"}) {
		t.Fatalf("efforts not deduplicated: %v", actual.SupportEfforts)
	}
	if actual.MaxContextSize != 200000 {
		t.Fatalf("context size not stored: %#v", actual)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.bodies) != 1 {
		t.Fatalf("expected exactly one PUT, saw %d", len(store.bodies))
	}
	if strings.Contains(store.bodies[0], "api_key") {
		t.Fatalf("PUT carried the api key: %s", store.bodies[0])
	}
	if !strings.Contains(store.bodies[0], `"base_url":"https://example.invalid/v1"`) {
		t.Fatalf("PUT dropped the base url: %s", store.bodies[0])
	}
	if got := store.models["r2-inspect/beta"].DisplayName; got != "R2 Beta" {
		t.Fatalf("sibling model was damaged: %q", got)
	}
	if got := store.models["other/gamma"].MaxContextSize; got != 8192 {
		t.Fatalf("model of another provider was touched: %d", got)
	}
}

func TestUpdateProviderModelKeepsTheStoredThinkingWhenTheDialogSaysToggle(t *testing.T) {
	models := inspectModels()
	alpha := models["r2-inspect/alpha"]
	alpha.Capabilities = []string{"thinking"}
	models["r2-inspect/alpha"] = alpha
	store := newOfficialModelStore(models)
	service, _ := newAutomationTestService(t, store.handler())
	value, err := updateModelAction(t, service, `{"providerId":"r2-inspect","model":"r2-inspect/alpha","edit":{
		"capabilities":[],"thinking":"toggle","adaptive_thinking":true,
		"max_context_size":131072,"max_output_size":4096}}`)
	if err != nil {
		t.Fatalf("updateModel: %v", err)
	}
	actual := savedModel(t, value)
	// Toggle and always-on are mutually exclusive: the stored always_thinking
	// capability was replaced by a switchable one.
	if !sameStrings(actual.Capabilities, []string{"thinking"}) {
		t.Fatalf("toggle mode did not store a switchable thinking capability: %v", actual.Capabilities)
	}
	if !actual.AdaptiveThinking {
		t.Fatalf("adaptive thinking was dropped in toggle mode: %#v", actual)
	}
	if actual.MaxOutputSize == nil || *actual.MaxOutputSize != 4096 {
		t.Fatalf("output cap not stored: %#v", actual)
	}
}

func TestUpdateProviderModelClearsOmittedLimits(t *testing.T) {
	models := inspectModels()
	alpha := models["r2-inspect/alpha"]
	output := int64(2048)
	alpha.MaxOutputSize = &output
	models["r2-inspect/alpha"] = alpha
	store := newOfficialModelStore(models)
	service, _ := newAutomationTestService(t, store.handler())
	value, err := updateModelAction(t, service, `{"providerId":"r2-inspect","model":"r2-inspect/alpha","edit":{
		"capabilities":["thinking"],"thinking":"none","max_context_size":131072}}`)
	if err != nil {
		t.Fatalf("updateModel: %v", err)
	}
	actual := savedModel(t, value)
	if actual.MaxOutputSize != nil {
		t.Fatalf("output cap was not cleared: %#v", actual)
	}
	if len(actual.Capabilities) != 0 {
		t.Fatalf("capabilities were not emptied: %v", actual.Capabilities)
	}
}

func TestUpdateProviderModelRefusesManagedAndForeignModels(t *testing.T) {
	store := newOfficialModelStore(inspectModels())
	service, _ := newAutomationTestService(t, store.handler())
	for _, payload := range []string{
		`{"providerId":"managed:kimi-code","model":"managed:kimi-code/k2","edit":{"thinking":"none","max_context_size":1}}`,
		`{"providerId":"r2-inspect","model":"other/gamma","edit":{"thinking":"none","max_context_size":1}}`,
		`{"providerId":"r2-inspect","model":"r2-inspect/missing","edit":{"thinking":"none","max_context_size":1}}`,
	} {
		if _, err := updateModelAction(t, service, payload); err == nil {
			t.Fatalf("payload was accepted: %s", payload)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.bodies) != 0 {
		t.Fatalf("a rejected edit still reached the official endpoint: %#v", store.bodies)
	}
}

func TestUpdateProviderModelReportsAFieldTheServerSilentlyDropped(t *testing.T) {
	models := inspectModels()
	alpha := models["r2-inspect/alpha"]
	alpha.AdaptiveThinking = false
	models["r2-inspect/alpha"] = alpha
	store := newOfficialModelStore(models)
	service, _ := newAutomationTestService(t, store.handler())
	// The edit asks for adaptive thinking through a mode that does not carry it,
	// so the response only differs if the readback drops a field it was sent.
	_, err := updateModelAction(t, service, `{"providerId":"r2-inspect","model":"r2-inspect/alpha","edit":{
		"capabilities":["thinking"],"thinking":"toggle","adaptive_thinking":true,
		"max_context_size":131072}}`)
	if err != nil {
		t.Fatalf("updateModel: %v", err)
	}
	if err = compareModel(modelConfig{MaxContextSize: 1}, alpha); err == nil {
		t.Fatal("a differing context size was reported as matching")
	}
}

func TestMergeModelThinkingModes(t *testing.T) {
	base := modelConfig{Capabilities: []string{"image_in", "tool_use", "thinking", "always_thinking"}}
	cases := []struct {
		mode     string
		expected []string
	}{
		{"none", []string{"image_in", "tool_use"}},
		{"toggle", []string{"image_in", "tool_use", "thinking"}},
		{"always", []string{"image_in", "tool_use", "always_thinking"}},
	}
	for _, test := range cases {
		merged := mergeModel(base, ModelEdit{Thinking: test.mode, Capabilities: []string{"tool_use", "image_in"}})
		if !sameStrings(merged.Capabilities, test.expected) {
			t.Fatalf("%s: got %v, want %v", test.mode, merged.Capabilities, test.expected)
		}
	}
	// Adaptive thinking is a property of a switchable model only.
	if mergeModel(base, ModelEdit{Thinking: "always", AdaptiveThinking: true}).AdaptiveThinking {
		t.Fatal("adaptive thinking survived always-on mode")
	}
	if !mergeModel(base, ModelEdit{Thinking: "toggle", AdaptiveThinking: true}).AdaptiveThinking {
		t.Fatal("adaptive thinking was dropped in toggle mode")
	}
}

func TestMergeModelIgnoresUnknownCapabilities(t *testing.T) {
	merged := mergeModel(modelConfig{}, ModelEdit{Thinking: "none", Capabilities: []string{"telepathy", "image_in"}})
	if !sameStrings(merged.Capabilities, []string{"image_in"}) {
		t.Fatalf("unknown capability reached the list: %v", merged.Capabilities)
	}
}

func TestModelBodyOmitsWhatClearsAField(t *testing.T) {
	body := modelBody(modelConfig{Model: "alpha", MaxContextSize: 8192})
	if _, present := body["display_name"]; present {
		t.Fatalf("empty display name was sent as a string: %#v", body)
	}
	if _, present := body["max_output_size"]; present {
		t.Fatalf("absent output cap was sent: %#v", body)
	}
	if _, present := body["adaptive_thinking"]; present {
		t.Fatalf("false adaptive thinking was sent: %#v", body)
	}
	if body["max_context_size"] != int64(8192) {
		t.Fatalf("context size missing: %#v", body)
	}
	output := int64(1024)
	full := modelBody(modelConfig{
		Model: "alpha", DisplayName: "Alpha", MaxContextSize: 8192, MaxOutputSize: &output,
		Capabilities: []string{"image_in"}, SupportEfforts: []string{"low"}, AdaptiveThinking: true,
	})
	if full["display_name"] != "Alpha" || full["max_output_size"] != int64(1024) || full["adaptive_thinking"] != true {
		t.Fatalf("complete model was not rendered: %#v", full)
	}
	if capabilities, ok := full["capabilities"].([]string); !ok || len(capabilities) != 1 {
		t.Fatalf("capabilities missing: %#v", full)
	}
}

func TestDecodeModelEditRejectsBadShapes(t *testing.T) {
	if _, err := decodeModelEdit(map[string]any{"thinking": "sometimes"}); err == nil {
		t.Fatal("unknown thinking mode was accepted")
	}
	if _, err := decodeModelEdit(map[string]any{"max_context_size": 1.5}); err == nil {
		t.Fatal("fractional context size was accepted")
	}
	if _, err := decodeModelEdit(map[string]any{"max_output_size": "large"}); err == nil {
		t.Fatal("non-numeric output cap was accepted")
	}
	if _, err := decodeModelEdit(nil); err == nil {
		t.Fatal("missing edit was accepted")
	}
	edit, err := decodeModelEdit(map[string]any{"max_context_size": float64(1024), "max_output_size": nil})
	if err != nil {
		t.Fatalf("nil output cap should mean unset: %v", err)
	}
	if edit.MaxOutputSize != nil || edit.MaxContextSize != 1024 || edit.Thinking != "none" {
		t.Fatalf("defaults not applied: %#v", edit)
	}
}

func TestCompareModelNamesEachMismatch(t *testing.T) {
	output := int64(10)
	base := modelConfig{
		Capabilities: []string{"image_in"}, MaxContextSize: 100, MaxOutputSize: &output,
		SupportEfforts: []string{"low"}, AdaptiveThinking: true,
	}
	if err := compareModel(base, base); err != nil {
		t.Fatalf("identical models did not match: %v", err)
	}
	other := int64(20)
	mismatches := map[string]modelConfig{
		"capabilities":      {MaxContextSize: 100, MaxOutputSize: &output, SupportEfforts: []string{"low"}, AdaptiveThinking: true},
		"adaptive_thinking": {Capabilities: []string{"image_in"}, MaxContextSize: 100, MaxOutputSize: &output, SupportEfforts: []string{"low"}},
		"max_context_size":  {Capabilities: []string{"image_in"}, MaxContextSize: 1, MaxOutputSize: &output, SupportEfforts: []string{"low"}, AdaptiveThinking: true},
		"max_output_size":   {Capabilities: []string{"image_in"}, MaxContextSize: 100, MaxOutputSize: &other, SupportEfforts: []string{"low"}, AdaptiveThinking: true},
		"support_efforts":   {Capabilities: []string{"image_in"}, MaxContextSize: 100, MaxOutputSize: &output, AdaptiveThinking: true},
	}
	for field, actual := range mismatches {
		err := compareModel(base, actual)
		if err == nil || !strings.Contains(err.Error(), field) {
			t.Fatalf("%s: mismatch not reported: %v", field, err)
		}
	}
}

func TestModelKeysRoundTrip(t *testing.T) {
	if got := providerModelKey("r2-inspect", "alpha"); got != "r2-inspect/alpha" {
		t.Fatalf("unexpected key: %s", got)
	}
	for _, key := range []string{"alpha", "/alpha", "alpha/", "", "/"} {
		if provider, model := splitModelKey(key); provider != "" || model != "" {
			t.Fatalf("%q was split into %q / %q", key, provider, model)
		}
	}
	provider, model := splitModelKey("r2-inspect/alpha")
	if provider != "r2-inspect" || model != "alpha" {
		t.Fatalf("unexpected split: %q / %q", provider, model)
	}
}
