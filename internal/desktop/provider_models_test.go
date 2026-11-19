package desktop

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
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
			baseURL, _ := raw["base_url"].(string)
			s.provider.BaseURL = baseURL
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
		model.AdaptiveThinking = boolPtr(flag)
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
	if actual.AdaptiveThinking != nil && *actual.AdaptiveThinking {
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
	if actual.AdaptiveThinking == nil || !*actual.AdaptiveThinking {
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
	alpha.AdaptiveThinking = boolPtr(false)
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
	if got := mergeModel(base, ModelEdit{Thinking: "always", AdaptiveThinking: boolPtr(true)}).AdaptiveThinking; got != nil && *got {
		t.Fatal("adaptive thinking survived always-on mode")
	}
	if got := mergeModel(base, ModelEdit{Thinking: "toggle", AdaptiveThinking: boolPtr(true)}).AdaptiveThinking; got == nil || !*got {
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
		Capabilities: []string{"image_in"}, SupportEfforts: []string{"low"}, AdaptiveThinking: boolPtr(true),
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
		SupportEfforts: []string{"low"}, AdaptiveThinking: boolPtr(true),
	}
	if err := compareModel(base, base); err != nil {
		t.Fatalf("identical models did not match: %v", err)
	}
	other := int64(20)
	mismatches := map[string]modelConfig{
		"capabilities":      {MaxContextSize: 100, MaxOutputSize: &output, SupportEfforts: []string{"low"}, AdaptiveThinking: boolPtr(true)},
		"adaptive_thinking": {Capabilities: []string{"image_in"}, MaxContextSize: 100, MaxOutputSize: &output, SupportEfforts: []string{"low"}},
		"max_context_size":  {Capabilities: []string{"image_in"}, MaxContextSize: 1, MaxOutputSize: &output, SupportEfforts: []string{"low"}, AdaptiveThinking: boolPtr(true)},
		"max_output_size":   {Capabilities: []string{"image_in"}, MaxContextSize: 100, MaxOutputSize: &other, SupportEfforts: []string{"low"}, AdaptiveThinking: boolPtr(true)},
		"support_efforts":   {Capabilities: []string{"image_in"}, MaxContextSize: 100, MaxOutputSize: &output, AdaptiveThinking: boolPtr(true)},
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

func TestUpdateProviderModelAcceptsBareStoredModelEntry(t *testing.T) {
	store := newOfficialModelStore(map[string]modelConfig{
		"alpha": {
			Provider: "r2-inspect", Model: "alpha", DisplayName: "Bare Alpha",
			MaxContextSize: 128000, Capabilities: []string{"tool_use"},
		},
	})
	service, _ := newAutomationTestService(t, store.handler())
	value, err := updateModelAction(t, service, `{"providerId":"r2-inspect","model":"r2-inspect/alpha","edit":{
		"capabilities":["image_in","tool_use"],"thinking":"toggle","adaptive_thinking":true,
		"max_context_size":200000,"support_efforts":["medium","high"]}}`)
	if err != nil {
		t.Fatalf("updateModel on bare stored key: %v", err)
	}
	actual := savedModel(t, value)
	if actual.AdaptiveThinking == nil || !*actual.AdaptiveThinking || actual.MaxContextSize != 200000 {
		t.Fatalf("unexpected saved model: %#v", actual)
	}
}

func TestConfigGetAndSavePreserveSecondaryModelKeyOrder(t *testing.T) {
	const orderedPatch = `{"secondary_model":{"defaultModel":"p/z-first","models":{"p/z-first":"1st","p/a-second":"2nd","p/m-third":"3rd"}}}`
	var receivedBody string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/config"):
			raw, _ := io.ReadAll(r.Body)
			receivedBody = string(raw)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":` + orderedPatch + `}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":` + orderedPatch + `}`))
		default:
			http.NotFound(w, r)
		}
	})
	service, _ := newAutomationTestService(t, handler)
	saved, err := service.Action(context.Background(), "config.save", json.RawMessage(`{"patch":`+orderedPatch+`}`))
	if err != nil {
		t.Fatalf("config.save: %v", err)
	}
	if !strings.Contains(receivedBody, `"p/z-first":"1st","p/a-second":"2nd","p/m-third":"3rd"`) {
		t.Fatalf("config.save reordered keys before forwarding: %s", receivedBody)
	}
	savedBytes, err := json.Marshal(saved)
	if err != nil {
		t.Fatalf("marshal saved: %v", err)
	}
	if !strings.Contains(string(savedBytes), `"p/z-first":"1st","p/a-second":"2nd","p/m-third":"3rd"`) {
		t.Fatalf("config.save response reordered keys: %s", string(savedBytes))
	}
	got, err := service.Action(context.Background(), "config.get", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("config.get: %v", err)
	}
	gotBytes, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	if !strings.Contains(string(gotBytes), `"p/z-first":"1st","p/a-second":"2nd","p/m-third":"3rd"`) {
		t.Fatalf("config.get response reordered keys: %s", string(gotBytes))
	}
}

func TestUpdateProviderModelPreservesSnakeCaseBaseURL(t *testing.T) {
	store := newOfficialModelStore(map[string]modelConfig{
		"r2-inspect/m-alpha": {
			Provider:       "r2-inspect",
			Model:          "m-alpha",
			MaxContextSize: 256000,
			Capabilities:   []string{"tool_use"},
		},
	})
	service, _ := newAutomationTestService(t, store.handler())
	_, err := updateModelAction(t, service, `{
		"providerId":"r2-inspect",
		"model":"r2-inspect/m-alpha",
		"edit":{
			"capabilities":["tool_use"],
			"thinking":"toggle",
			"adaptive_thinking":false,
			"max_context_size":512000,
			"support_efforts":["medium","high"]
		}
	}`)
	if err != nil {
		t.Fatalf("providers.updateModel: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.provider.BaseURL != "https://example.invalid/v1" {
		t.Fatalf("provider base_url was wiped: got %q", store.provider.BaseURL)
	}
}

func TestConfigRoundTripPreservesAllProviderAndModelFields(t *testing.T) {
	type storedProvider struct {
		raw       map[string]any
		apiKey    string
		apiKeyEnv string
	}

	var mu sync.Mutex
	providers := map[string]*storedProvider{
		"snake-prov": {
			apiKey: "sk-snake-secret",
			raw: map[string]any{
				"type":           "anthropic",
				"base_url":       "https://anthropic.example.invalid/v1",
				"default_model":  "snake-prov/claude-opus-4-6",
				"custom_headers": map[string]any{"X-Tenant": "team-a", "X-Trace": "1"},
				"env":            map[string]any{"HTTP_PROXY": "http://127.0.0.1:7890"},
				"model_source":   "static",
				"x_provider_tag": "preserve-me",
				"has_api_key":    true,
			},
		},
		"camel-prov": {
			apiKeyEnv: "CAMEL_PROVIDER_KEY",
			raw: map[string]any{
				"type":          "openai_responses",
				"baseUrl":       "https://openai.example.invalid/v1",
				"defaultModel":  "camel-prov/gpt-budget",
				"api_key_env":   "CAMEL_PROVIDER_KEY",
				"customHeaders": map[string]any{"X-Org": "org-camel"},
				"env":           map[string]any{"NO_PROXY": "localhost"},
				"x_camel_extra": map[string]any{"tier": "pro"},
				"has_api_key":   true,
			},
		},
	}
	models := map[string]map[string]any{
		"snake-prov/claude-opus-4-6": {
			"provider":          "snake-prov",
			"model":             "claude-opus-4-6",
			"display_name":      "Claude Opus 4.6",
			"max_context_size":  json.Number("200000"),
			"max_output_size":   json.Number("32768"),
			"capabilities":      []any{"image_in", "tool_use", "thinking", "dynamically_loaded_tools"},
			"adaptive_thinking": true,
			"support_efforts":   []any{"low", "medium", "high", "max"},
			"default_effort":    "high",
			"custom_headers":    map[string]any{"X-Beta": "adaptive-1"},
			"env":               map[string]any{"MODEL_FLAG": "on"},
			"protocol":          "anthropic",
			"betaApi":           true,
			"x_model_extra":     "keep-snake",
		},
		"snake-prov/claude-default-adaptive": {
			"provider":         "snake-prov",
			"model":            "claude-default-adaptive",
			"display_name":     "Claude Omitted Adaptive",
			"max_context_size": json.Number("200000"),
			"capabilities":     []any{"tool_use", "thinking"},
			"support_efforts":  []any{"low", "high"},
		},
		"camel-prov/gpt-budget": {
			"provider":         "camel-prov",
			"model":            "gpt-budget",
			"displayName":      "GPT Budget",
			"maxContextSize":   json.Number("262144"),
			"maxOutputSize":    json.Number("16384"),
			"capabilities":     []any{"image_in", "video_in", "tool_use", "thinking"},
			"adaptiveThinking": false,
			"supportEfforts":   []any{"low", "medium", "high"},
			"defaultEffort":    "medium",
			"offEffort":        "none",
			"customHeaders":    map[string]any{"X-Mode": "budget"},
			"env":              map[string]any{"REGION": "us"},
			"reasoningKey":     "reasoning_content",
			"x_camel_flag":     true,
		},
	}

	var putBodies []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
			mu.Lock()
			provResp := map[string]any{}
			for id, item := range providers {
				provResp[id] = item.raw
			}
			modelResp := map[string]any{}
			for key, item := range models {
				modelResp[key] = item
			}
			mu.Unlock()
			writeNativeTestEnvelope(w, map[string]any{"providers": provResp, "models": modelResp})

		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/providers/"):
			_, providerID, _ := strings.Cut(r.URL.Path, "/providers/")
			bodyBytes, _ := io.ReadAll(r.Body)
			decoder := json.NewDecoder(strings.NewReader(string(bodyBytes)))
			decoder.UseNumber()
			raw := map[string]any{}
			if err := decoder.Decode(&raw); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if _, hasKey := raw["api_key"]; hasKey {
				http.Error(w, "api_key must not be sent", http.StatusBadRequest)
				return
			}
			if _, hasEnv := raw["api_key_env"]; hasEnv {
				http.Error(w, "api_key_env must not be sent", http.StatusBadRequest)
				return
			}

			mu.Lock()
			defer mu.Unlock()
			putBodies = append(putBodies, string(bodyBytes))
			existing, ok := providers[providerID]
			if !ok {
				http.NotFound(w, r)
				return
			}

			submittedModels := rawSlice(raw["models"])
			modelSet := map[string]bool{}
			for _, item := range submittedModels {
				id, _ := item["model"].(string)
				modelSet[id] = true
			}
			if dm, ok := raw["default_model"].(string); ok && dm != "" {
				if !modelSet[dm] {
					http.Error(w, "default_model must be one of models[].model", http.StatusBadRequest)
					return
				}
			}

			// Strict replace: only fields present in PUT survive (except server-side credentials).
			replacedProv := map[string]any{"has_api_key": existing.apiKey != "" || existing.apiKeyEnv != ""}
			if existing.apiKeyEnv != "" {
				replacedProv["api_key_env"] = existing.apiKeyEnv
			}
			for key, val := range raw {
				if key == "models" {
					continue
				}
				if key == "default_model" {
					if dm, ok := val.(string); ok && dm != "" {
						replacedProv["default_model"] = providerID + "/" + dm
					}
					continue
				}
				replacedProv[key] = val
			}
			existing.raw = replacedProv

			for key, item := range models {
				if item["provider"] == providerID {
					delete(models, key)
				}
			}
			for _, item := range submittedModels {
				next := map[string]any{"provider": providerID}
				for k, v := range item {
					next[k] = v
				}
				models[providerID+"/"+item["model"].(string)] = next
			}
			writeNativeTestEnvelope(w, map[string]any{"provider": providerID})

		default:
			http.NotFound(w, r)
		}
	})

	service, _ := newAutomationTestService(t, handler)
	before, err := service.readConfig(context.Background())
	if err != nil {
		t.Fatalf("readConfig before: %v", err)
	}

	// Save every model of every provider unchanged using the exact fields read from /config.
	for _, key := range []string{
		"snake-prov/claude-opus-4-6",
		"snake-prov/claude-default-adaptive",
		"camel-prov/gpt-budget",
	} {
		providerID, _ := splitModelKey(key)
		model := before.Models[key]
		thinking := "none"
		for _, capToken := range model.Capabilities {
			if capToken == "always_thinking" {
				thinking = "always"
				break
			}
			if capToken == "thinking" {
				thinking = "toggle"
			}
		}
		if thinking == "none" && ((model.AdaptiveThinking != nil && *model.AdaptiveThinking) || len(model.SupportEfforts) > 0) {
			thinking = "toggle"
		}
		edit := map[string]any{
			"capabilities":     model.Capabilities,
			"thinking":         thinking,
			"max_context_size": model.MaxContextSize,
			"support_efforts":  model.SupportEfforts,
		}
		if model.MaxOutputSize != nil {
			edit["max_output_size"] = *model.MaxOutputSize
		}
		if model.AdaptiveThinking != nil {
			edit["adaptive_thinking"] = *model.AdaptiveThinking
		}
		payload, err := json.Marshal(map[string]any{
			"providerId": providerID,
			"model":      key,
			"edit":       edit,
		})
		if err != nil {
			t.Fatalf("marshal payload for %s: %v", key, err)
		}
		if _, err := service.Action(context.Background(), "providers.updateModel", payload); err != nil {
			t.Fatalf("round-trip updateModel(%s): %v", key, err)
		}
	}

	after, err := service.readConfig(context.Background())
	if err != nil {
		t.Fatalf("readConfig after: %v", err)
	}
	if !reflect.DeepEqual(before.Providers, after.Providers) {
		t.Fatalf("providers changed after round-trip:\nbefore=%#v\nafter =%#v", before.Providers, after.Providers)
	}
	if !reflect.DeepEqual(before.Models, after.Models) {
		t.Fatalf("models changed after round-trip:\nbefore=%#v\nafter =%#v", before.Models, after.Models)
	}

	mu.Lock()
	defer mu.Unlock()
	if providers["snake-prov"].apiKey != "sk-snake-secret" {
		t.Fatalf("snake-prov apiKey was modified: %q", providers["snake-prov"].apiKey)
	}
	if providers["camel-prov"].apiKeyEnv != "CAMEL_PROVIDER_KEY" {
		t.Fatalf("camel-prov apiKeyEnv was modified: %q", providers["camel-prov"].apiKeyEnv)
	}
	if len(putBodies) != 3 {
		t.Fatalf("expected 3 PUT calls, got %d", len(putBodies))
	}
	for i, body := range putBodies {
		if strings.Contains(body, "api_key") || strings.Contains(body, "sk-snake-secret") {
			t.Fatalf("PUT[%d] leaked credentials: %s", i, body)
		}
	}
}

func TestThinkingNoDisablesThinkingAndNeverWritesLiteralNo(t *testing.T) {
	models := map[string]modelConfig{
		"r2-inspect/claude": {
			Provider:         "r2-inspect",
			Model:            "claude",
			DisplayName:      "Claude Opus",
			MaxContextSize:   200000,
			Capabilities:     []string{"image_in", "tool_use", "thinking", "dynamically_loaded_tools"},
			AdaptiveThinking: boolPtr(true),
			SupportEfforts:   []string{"low", "medium", "high"},
		},
	}
	store := newOfficialModelStore(models)
	service, _ := newAutomationTestService(t, store.handler())

	// 1. Omitting adaptive_thinking in toggle mode preserves stored adaptive_thinking=true and strips literal "no".
	value, err := updateModelAction(t, service, `{
		"providerId":"r2-inspect",
		"model":"r2-inspect/claude",
		"edit":{
			"capabilities":["image_in","tool_use","thinking"],
			"thinking":"toggle",
			"max_context_size":200000,
			"support_efforts":["no","high","NO","max"]
		}
	}`)
	if err != nil {
		t.Fatalf("toggle update with preserved adaptive_thinking: %v", err)
	}
	toggled := savedModel(t, value)
	if toggled.AdaptiveThinking == nil || !*toggled.AdaptiveThinking {
		t.Fatalf("stored adaptive_thinking=true was wiped: %#v", toggled)
	}
	if !sameStrings(toggled.SupportEfforts, []string{"high", "max"}) {
		t.Fatalf("literal no leaked into support_efforts: %v", toggled.SupportEfforts)
	}
	if !sameStrings(toggled.Capabilities, []string{"image_in", "tool_use", "thinking", "dynamically_loaded_tools"}) {
		t.Fatalf("extra capability lost: %v", toggled.Capabilities)
	}

	// 2. Selecting "no" (thinking=none) disables thinking completely: removes thinking/always_thinking,
	// clears adaptive_thinking=true, clears support_efforts, and never writes literal "no".
	value, err = updateModelAction(t, service, `{
		"providerId":"r2-inspect",
		"model":"r2-inspect/claude",
		"edit":{
			"capabilities":["image_in","tool_use","thinking"],
			"thinking":"none",
			"max_context_size":200000,
			"support_efforts":["no","high"]
		}
	}`)
	if err != nil {
		t.Fatalf("disable thinking (no): %v", err)
	}
	disabled := savedModel(t, value)
	if !sameStrings(disabled.Capabilities, []string{"image_in", "tool_use", "dynamically_loaded_tools"}) {
		t.Fatalf("thinking capability was not removed: %v", disabled.Capabilities)
	}
	if disabled.AdaptiveThinking != nil && *disabled.AdaptiveThinking {
		t.Fatalf("adaptive_thinking=true survived thinking=none: %#v", disabled)
	}
	if len(disabled.SupportEfforts) != 0 {
		t.Fatalf("support_efforts was not cleared for thinking=none: %v", disabled.SupportEfforts)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	lastBody := store.bodies[len(store.bodies)-1]
	if strings.Contains(lastBody, `"support_efforts"`) || strings.Contains(lastBody, `"no"`) {
		t.Fatalf("PUT body wrote support_efforts or literal no when thinking is disabled: %s", lastBody)
	}
	if strings.Contains(lastBody, `"adaptive_thinking":true`) {
		t.Fatalf("PUT body kept adaptive_thinking=true when thinking is disabled: %s", lastBody)
	}
}
