package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

func TestUpdateProviderModelClearsExplicitNullOutput(t *testing.T) {
	models := inspectModels()
	alpha := models["r2-inspect/alpha"]
	output := int64(2048)
	alpha.MaxOutputSize = &output
	models["r2-inspect/alpha"] = alpha
	store := newOfficialModelStore(models)
	service, _ := newAutomationTestService(t, store.handler())
	value, err := updateModelAction(t, service, `{"providerId":"r2-inspect","model":"r2-inspect/alpha","edit":{
		"capabilities":["thinking"],"thinking":"none","max_context_size":131072,"max_output_size":null}}`)
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

func TestMergeModelPreservesOnlySupportedDefaultEffort(t *testing.T) {
	base := modelConfig{DefaultEffort: "high", SupportEfforts: []string{"low", "high"}}
	if got := mergeModel(base, ModelEdit{SupportEfforts: []string{"medium", "high"}}); got.DefaultEffort != "high" {
		t.Fatalf("supported default effort was cleared: %#v", got)
	}
	for _, edit := range []ModelEdit{{SupportEfforts: []string{"medium"}}, {Thinking: "none"}} {
		if got := mergeModel(base, edit); got.DefaultEffort != "" {
			t.Fatalf("unsupported default effort survived: %#v", got)
		}
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
	if edit.MaxOutputSize != nil || !edit.OutputChanged || edit.MaxContextSize != 1024 || edit.Thinking != "" {
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

func TestUpdateProviderModelPartialEditPreservesContextAndOtherFields(t *testing.T) {
	models := inspectModels()
	alpha := models["r2-inspect/alpha"]
	size := int64(8192)
	alpha.MaxOutputSize = &size
	alpha.AdaptiveThinking = boolPtr(true)
	alpha.SupportEfforts = []string{"medium", "high"}
	alpha.Capabilities = append(alpha.Capabilities, "audio_in")
	models["r2-inspect/alpha"] = alpha
	store := newOfficialModelStore(models)
	service, _ := newAutomationTestService(t, store.handler())
	value, err := updateModelAction(t, service, `{"providerId":"r2-inspect","model":"alpha","edit":{"capabilities":["tool_use"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	got := savedModel(t, value)
	if got.MaxContextSize != alpha.MaxContextSize || !sameOptionalInt(got.MaxOutputSize, alpha.MaxOutputSize) || !sameStrings(got.SupportEfforts, alpha.SupportEfforts) || !sameOptionalBool(got.AdaptiveThinking, alpha.AdaptiveThinking) || !sameCapabilitySet(got.Capabilities, []string{"tool_use", "thinking", "audio_in"}) {
		t.Fatalf("partial edit overwrote official or custom fields: %#v", got)
	}
	if _, err := updateModelAction(t, service, `{"providerId":"r2-inspect","model":"alpha","edit":{"max_output_size":131073}}`); err == nil || !strings.Contains(err.Error(), "不能超过") {
		t.Fatalf("accepted an output above saved context: %v", err)
	}
	value, err = updateModelAction(t, service, `{"providerId":"r2-inspect","model":"alpha","edit":{"max_context_size":131073}}`)
	if err != nil || savedModel(t, value).MaxContextSize != 131073 || !sameOptionalInt(savedModel(t, value).MaxOutputSize, &size) {
		t.Fatalf("context edit changed output = %v, %v", value, err)
	}
	value, err = updateModelAction(t, service, `{"providerId":"r2-inspect","model":"alpha","edit":{"max_context_size":200000,"max_output_size":150000}}`)
	if err != nil {
		t.Fatal(err)
	}
	got = savedModel(t, value)
	if got.MaxContextSize != 200000 || got.MaxOutputSize == nil || *got.MaxOutputSize != 150000 {
		t.Fatalf("combined limits = %#v", got)
	}
	value, err = updateModelAction(t, service, `{"providerId":"r2-inspect","model":"alpha","edit":{"max_output_size":null}}`)
	if err != nil || savedModel(t, value).MaxOutputSize != nil {
		t.Fatalf("clear output = %v, %v", value, err)
	}
}

func TestUpdateProviderModelsValidatesWholeBatchBeforeWriting(t *testing.T) {
	store := newOfficialModelStore(inspectModels())
	service, _ := newAutomationTestService(t, store.handler())
	update := func(models string) (any, error) {
		return service.Action(context.Background(), "providers.updateModels", json.RawMessage(`{"providerId":"r2-inspect","models":`+models+`}`))
	}
	for _, invalid := range []string{
		`[{"model":"alpha","edit":{"thinking":"none"}},{"model":"beta","edit":{"max_output_size":65537}}]`,
		`[{"model":"alpha","edit":{"thinking":"none"}},{"model":"r2-inspect/alpha","edit":{"capabilities":[]}}]`,
		`[{"model":"alpha","edit":{"thinking":"none"}},{"model":"other/gamma","edit":{"thinking":"none"}}]`,
		`[{"model":"alpha","edit":{"thinking":"none"}},{"model":"missing","edit":{"thinking":"none"}}]`,
	} {
		if _, err := update(invalid); err == nil || len(store.bodies) != 0 {
			t.Fatalf("invalid batch attempted a write: %s, %v", invalid, err)
		}
	}
	value, err := update(`[{"model":"beta","edit":{"capabilities":["video_in"]}},{"model":"alpha","edit":{"capabilities":["tool_use"]}}]`)
	if err != nil {
		t.Fatal(err)
	}
	response := value.(map[string]any)["models"].([]providerModelUpdate)
	if len(response) != 2 || response[0].Key != "r2-inspect/beta" || response[1].Key != "r2-inspect/alpha" {
		t.Fatalf("unexpected batch readback order: %#v", response)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.bodies) != 1 || !sameCapabilitySet(store.models["r2-inspect/beta"].Capabilities, []string{"video_in"}) || !sameCapabilitySet(store.models["r2-inspect/alpha"].Capabilities, []string{"tool_use", "thinking"}) || store.models["other/gamma"].MaxContextSize != 8192 {
		t.Fatalf("batch write count or stored models incorrect: %d, %#v", len(store.bodies), store.models)
	}
}

func TestCompareModelIgnoresCapabilityOrderAndDuplicates(t *testing.T) {
	expected := modelConfig{Capabilities: []string{"image_in", "tool_use", "audio_in"}}
	actual := modelConfig{Capabilities: []string{"audio_in", "tool_use", "tool_use", "image_in"}}
	if err := compareModel(expected, actual); err != nil {
		t.Fatal(err)
	}
	actual.Capabilities[0] = "video_in"
	if err := compareModel(expected, actual); err == nil {
		t.Fatal("capability changes were ignored")
	}
}

func TestConfigActionsRedactCredentialsAndRetainModelOrder(t *testing.T) {
	response := `{"providers":{"env":{"type":"openai","api_key":"fixture-alpha","apiKey":"fixture-beta","custom_headers":{"X-Custom":"fixture-header"},"env":{"VARIABLE":"fixture-env"},"oauth":{"access_token":"fixture-oauth"}}},"models":{"env/model":{"maxContextSize":8192,"extra":[{"clientSecret":"fixture-secret","enabled":true}]}},"secondary_model":{"models":{"env/z":"first","env/a":"second"}}}`
	service, _ := newAutomationTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":`+response+`}`)
	}))
	for _, input := range []struct{ action, payload string }{
		{"config.get", `{}`},
		{"config.save", `{"patch":{"default_model":"env/model"}}`},
	} {
		value, err := service.Action(context.Background(), input.action, json.RawMessage(input.payload))
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		want := `{"providers":{"env":{"type":"openai"}},"models":{"env/model":{"maxContextSize":8192,"extra":[{"enabled":true}]}},"secondary_model":{"models":{"env/z":"first","env/a":"second"}}}`
		if string(body) != want {
			t.Fatalf("%s config response = %s", input.action, body)
		}
	}
}

func TestSaveOfficialProviderFormSerializesWithUpdateModelClearMaxOutput(t *testing.T) {
	runCase := func(t *testing.T, updateFirst bool) {
		t.Helper()
		size := int64(1024)
		models := inspectModels()
		alpha := models["r2-inspect/alpha"]
		alpha.MaxOutputSize = &size
		models["r2-inspect/alpha"] = alpha
		store := newOfficialModelStore(models)
		baseHandler := store.handler()

		firstPutEntered := make(chan struct{})
		releaseFirstPut := make(chan struct{})
		var putCount atomic.Int32
		var getWhilePutHeld atomic.Int32
		var putInFlight atomic.Bool

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config") && putInFlight.Load() {
				getWhilePutHeld.Add(1)
			}
			if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/providers/") {
				if putCount.Add(1) == 1 {
					putInFlight.Store(true)
					close(firstPutEntered)
					<-releaseFirstPut
					putInFlight.Store(false)
				}
			}
			baseHandler.ServeHTTP(w, r)
		})
		service, _ := newAutomationTestService(t, handler)

		saveFormPayload := json.RawMessage(fmt.Sprintf(`{
			"providerId": "r2-inspect",
			"expectedOrigin": %q,
			"payload": {
				"type": "openai",
				"base_url": "https://example.invalid/v2",
				"models": [
					{"model": "alpha", "max_context_size": 131072, "display_name": "R2 Alpha"},
					{"model": "beta", "max_context_size": 65536, "display_name": "R2 Beta"}
				]
			},
			"opening": {}
		}`, service.kap.base))
		clearOutputPayload := json.RawMessage(`{"providerId":"r2-inspect","model":"alpha","edit":{"max_output_size":null}}`)

		errCh1 := make(chan error, 1)
		errCh2 := make(chan error, 1)
		if updateFirst {
			go func() {
				_, err := service.Action(context.Background(), "providers.updateModel", clearOutputPayload)
				errCh1 <- err
			}()
			<-firstPutEntered
			go func() {
				_, err := service.Action(context.Background(), "providers.saveOfficialForm", saveFormPayload)
				errCh2 <- err
			}()
		} else {
			go func() {
				_, err := service.Action(context.Background(), "providers.saveOfficialForm", saveFormPayload)
				errCh1 <- err
			}()
			<-firstPutEntered
			go func() {
				_, err := service.Action(context.Background(), "providers.updateModel", clearOutputPayload)
				errCh2 <- err
			}()
		}

		// Give the second goroutine a window to attempt /config if locking were broken.
		time.Sleep(40 * time.Millisecond)
		close(releaseFirstPut)

		if err := <-errCh1; err != nil {
			t.Fatalf("first action failed: %v", err)
		}
		if err := <-errCh2; err != nil {
			t.Fatalf("second action failed: %v", err)
		}
		if getWhilePutHeld.Load() != 0 {
			t.Fatalf("concurrent GET /config ran while providerModelsMu PUT was in flight: %d", getWhilePutHeld.Load())
		}
		store.mu.Lock()
		defer store.mu.Unlock()
		if got := store.models["r2-inspect/alpha"].MaxOutputSize; got != nil {
			t.Fatalf("cleared max_output_size was resurrected: %d (bodies=%v)", *got, store.bodies)
		}
		if len(store.bodies) != 2 {
			t.Fatalf("expected 2 serialized PUT bodies, got %d", len(store.bodies))
		}
	}

	t.Run("updateModel clears before saveOfficialForm", func(t *testing.T) {
		runCase(t, true)
	})
	t.Run("saveOfficialForm runs before updateModel clears", func(t *testing.T) {
		runCase(t, false)
	})
}

func TestSaveOfficialProviderFormHandlesOmittedKeysExplicitEditsAndRedaction(t *testing.T) {
	size := int64(1024)
	stored := map[string]modelConfig{
		"team/alpha": {
			Provider:         "team",
			Model:            "alpha",
			DisplayName:      "Alpha",
			MaxContextSize:   131072,
			MaxOutputSize:    &size,
			Capabilities:     []string{"video_in", "thinking"},
			SupportEfforts:   []string{"low", "high"},
			AdaptiveThinking: boolPtr(true),
			DefaultEffort:    "high",
			OffEffort:        "off",
		},
		"team/beta": {
			Provider:       "team",
			Model:          "beta",
			DisplayName:    "Beta",
			MaxContextSize: 65536,
			Capabilities:   []string{"video_in"},
			SupportEfforts: []string{"medium"},
		},
	}

	var mu sync.Mutex
	var putBodies []map[string]any
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
			mu.Lock()
			snapshot := map[string]any{
				"providers": map[string]providerConfig{
					"team": {Type: "openai", BaseURL: "https://example.invalid/v1"},
				},
				"models": stored,
			}
			mu.Unlock()
			writeNativeTestEnvelope(w, snapshot)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/providers/team"):
			var body map[string]any
			decoder := json.NewDecoder(r.Body)
			decoder.UseNumber()
			if err := decoder.Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			putBodies = append(putBodies, body)
			mu.Unlock()
			writeNativeTestEnvelope(w, map[string]any{
				"provider": map[string]any{
					"id":          "team-renamed",
					"type":        "openai",
					"api_key":     "sk-must-not-leak",
					"apiKey":      "sk-camel-must-not-leak",
					"has_api_key": true,
				},
			})
		default:
			http.NotFound(w, r)
		}
	})
	service, _ := newAutomationTestService(t, handler)

	t.Run("omitted keys preserve stored capabilities and thinking even with mismatched or empty opening", func(t *testing.T) {
		res, err := service.Action(context.Background(), "providers.saveOfficialForm", json.RawMessage(fmt.Sprintf(`{
			"providerId": "team",
			"expectedOrigin": %q,
			"payload": {
				"type": "openai",
				"new_id": "team-renamed",
				"api_key": "sk-user-entered",
				"base_url": "https://example.invalid/v2",
				"default_model": "alpha",
				"models": [
					{"model": "alpha", "max_context_size": 131072, "display_name": "Alpha"},
					{"model": "beta", "max_context_size": 65536, "display_name": "Beta"}
				]
			},
			"opening": {
				"alpha": {
					"raw": {
						"capabilities": ["image_in"],
						"support_efforts": ["low"],
						"adaptive_thinking": false,
						"default_effort": "low",
						"off_effort": "none"
					},
					"merged": null
				}
			}
		}`, service.kap.base)))
		if err != nil {
			t.Fatalf("saveOfficialForm failed: %v", err)
		}

		mu.Lock()
		if len(putBodies) != 1 {
			mu.Unlock()
			t.Fatalf("expected 1 PUT body, got %d", len(putBodies))
		}
		putBody := putBodies[0]
		mu.Unlock()

		if putBody["new_id"] != "team-renamed" || putBody["api_key"] != "sk-user-entered" || putBody["base_url"] != "https://example.invalid/v2" || putBody["default_model"] != "alpha" {
			t.Fatalf("top-level official fields dropped: %#v", putBody)
		}
		models := rawSlice(putBody["models"])
		if len(models) != 2 {
			t.Fatalf("expected 2 models in PUT, got %#v", models)
		}
		alphaBody := models[0]
		if !reflect.DeepEqual(cleanWireStringList(alphaBody["capabilities"]), []string{"video_in", "thinking"}) {
			t.Fatalf("omitted capabilities were not preserved: %#v", alphaBody["capabilities"])
		}
		if !reflect.DeepEqual(cleanWireStringList(alphaBody["support_efforts"]), []string{"low", "high"}) {
			t.Fatalf("omitted support_efforts were not preserved: %#v", alphaBody["support_efforts"])
		}
		if alphaBody["adaptive_thinking"] != true {
			t.Fatalf("omitted adaptive_thinking was not preserved: %#v", alphaBody["adaptive_thinking"])
		}
		if alphaBody["default_effort"] != "high" || alphaBody["off_effort"] != "off" {
			t.Fatalf("omitted effort defaults were not preserved: %#v", alphaBody)
		}
		if got, err := numberValue(alphaBody["max_output_size"]); err != nil || got != 1024 {
			t.Fatalf("omitted max_output_size was not preserved: %#v (%v)", alphaBody["max_output_size"], err)
		}
		betaBody := models[1]
		if !reflect.DeepEqual(cleanWireStringList(betaBody["capabilities"]), []string{"video_in"}) {
			t.Fatalf("beta omitted capabilities were not preserved: %#v", betaBody["capabilities"])
		}
		if !reflect.DeepEqual(cleanWireStringList(betaBody["support_efforts"]), []string{"medium"}) {
			t.Fatalf("beta omitted support_efforts were not preserved: %#v", betaBody["support_efforts"])
		}
		if _, hasOutput := betaBody["max_output_size"]; hasOutput {
			t.Fatalf("beta nil max_output_size should not be injected: %#v", betaBody)
		}

		encoded, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "sk-must-not-leak") || strings.Contains(string(encoded), "sk-camel-must-not-leak") {
			t.Fatalf("response leaked plaintext api key: %s", encoded)
		}
		var decoded struct {
			Data struct {
				Provider map[string]any `json:"provider"`
			} `json:"data"`
			MergedModels map[string]wireCapabilitySnapshot `json:"mergedModels"`
		}
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Data.Provider["id"] != "team-renamed" || decoded.Data.Provider["has_api_key"] != true {
			t.Fatalf("unexpected sanitized data: %#v", decoded.Data)
		}
		alphaSnap := decoded.MergedModels["alpha"]
		if !reflect.DeepEqual(alphaSnap.Capabilities, []string{"video_in", "thinking"}) ||
			!reflect.DeepEqual(alphaSnap.SupportEfforts, []string{"low", "high"}) ||
			alphaSnap.AdaptiveThinking == nil || !*alphaSnap.AdaptiveThinking ||
			alphaSnap.DefaultEffort != "high" || alphaSnap.OffEffort != "off" {
			t.Fatalf("unexpected mergedModels[alpha]: %#v", alphaSnap)
		}
		betaSnap := decoded.MergedModels["beta"]
		if betaSnap.AdaptiveThinking != nil {
			t.Fatalf("expected beta adaptive_thinking omitted/nil, got %#v", *betaSnap.AdaptiveThinking)
		}
	})

	t.Run("explicit edit differing from opening is respected", func(t *testing.T) {
		res, err := service.Action(context.Background(), "providers.saveOfficialForm", json.RawMessage(fmt.Sprintf(`{
			"providerId": "team",
			"expectedOrigin": %q,
			"payload": {
				"type": "openai",
				"models": [
					{
						"model": "alpha",
						"max_context_size": 512,
						"capabilities": ["image_in"],
						"support_efforts": ["medium"],
						"adaptive_thinking": false,
						"default_effort": "medium",
						"off_effort": "none"
					}
				]
			},
			"opening": {
				"alpha": {
					"raw": {
						"capabilities": ["video_in", "thinking"],
						"support_efforts": ["low", "high"],
						"adaptive_thinking": true,
						"default_effort": "high",
						"off_effort": "off"
					},
					"merged": null
				}
			}
		}`, service.kap.base)))
		if err != nil {
			t.Fatalf("saveOfficialForm explicit edit failed: %v", err)
		}
		mu.Lock()
		putBody := putBodies[len(putBodies)-1]
		mu.Unlock()
		alphaBody := rawSlice(putBody["models"])[0]
		if !reflect.DeepEqual(cleanWireStringList(alphaBody["capabilities"]), []string{"image_in"}) {
			t.Fatalf("explicit capabilities edit was overwritten: %#v", alphaBody["capabilities"])
		}
		if !reflect.DeepEqual(cleanWireStringList(alphaBody["support_efforts"]), []string{"medium"}) {
			t.Fatalf("explicit support_efforts edit was overwritten: %#v", alphaBody["support_efforts"])
		}
		if alphaBody["adaptive_thinking"] != false {
			t.Fatalf("explicit adaptive_thinking edit was overwritten: %#v", alphaBody["adaptive_thinking"])
		}
		if alphaBody["default_effort"] != "medium" || alphaBody["off_effort"] != "none" {
			t.Fatalf("explicit effort edit was overwritten: %#v", alphaBody)
		}
		if _, hasOutput := alphaBody["max_output_size"]; hasOutput {
			t.Fatalf("oversized stored max_output_size (1024 > 512) must not be injected: %#v", alphaBody)
		}
		merged := res.(map[string]any)["mergedModels"].(map[string]wireCapabilitySnapshot)["alpha"]
		if !reflect.DeepEqual(merged.Capabilities, []string{"image_in"}) {
			t.Fatalf("mergedModels did not reflect explicit edit: %#v", merged)
		}
	})
}

func TestSaveOfficialProviderFormAbortsOnReadConfigFailureAndInvalidInput(t *testing.T) {
	var getCalls, putCalls atomic.Int32
	service, _ := newAutomationTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			getCalls.Add(1)
		}
		if r.Method == http.MethodPut {
			putCalls.Add(1)
			writeNativeTestEnvelope(w, map[string]any{"provider": "team"})
			return
		}
		http.Error(w, "kap config down", http.StatusInternalServerError)
	}))

	_, err := service.Action(context.Background(), "providers.saveOfficialForm", json.RawMessage(fmt.Sprintf(`{
		"providerId": "team",
		"expectedOrigin": %q,
		"payload": {"type": "openai", "models": [{"model": "alpha", "max_context_size": 8192}]}
	}`, service.kap.base)))
	if err == nil {
		t.Fatal("expected error when readConfig fails")
	}
	if putCalls.Load() != 0 {
		t.Fatalf("PUT was sent despite readConfig failure: %d", putCalls.Load())
	}

	getCallsBeforeInvalid := getCalls.Load()
	for _, bad := range []string{
		fmt.Sprintf(`{"providerId":"","expectedOrigin":%q,"payload":{"models":[]}}`, service.kap.base),
		fmt.Sprintf(`{"providerId":"managed:kimi-code","expectedOrigin":%q,"payload":{"models":[]}}`, service.kap.base),
		fmt.Sprintf(`{"providerId":"team","expectedOrigin":%q}`, service.kap.base),
		fmt.Sprintf(`{"providerId":"team","expectedOrigin":%q,"payload":{"type":"openai"}}`, service.kap.base),
		`{"providerId":"team","payload":{"type":"openai","models":[]}}`,
		`{"providerId":"team","expectedOrigin":"","payload":{"type":"openai","models":[]}}`,
		`{"providerId":"team","expectedOrigin":"http://example.com:8080","payload":{"type":"openai","models":[]}}`,
		`{"providerId":"team","expectedOrigin":"http://127.0.0.1","payload":{"type":"openai","models":[]}}`,
		`{"providerId":"team","expectedOrigin":"http://user:pass@127.0.0.1:8080","payload":{"type":"openai","models":[]}}`,
		`{"providerId":"team","expectedOrigin":"http://127.0.0.1:8080/api/v1","payload":{"type":"openai","models":[]}}`,
		`{"providerId":"team","expectedOrigin":"http://127.0.0.1:8080?a=1","payload":{"type":"openai","models":[]}}`,
		`{"providerId":"team","expectedOrigin":"http://127.0.0.1:8080#x","payload":{"type":"openai","models":[]}}`,
	} {
		if _, err := service.Action(context.Background(), "providers.saveOfficialForm", json.RawMessage(bad)); err == nil {
			t.Fatalf("expected validation error for %s", bad)
		}
	}
	if getCalls.Load() != getCallsBeforeInvalid || putCalls.Load() != 0 {
		t.Fatalf("invalid inputs triggered KAP requests: getDelta=%d put=%d", getCalls.Load()-getCallsBeforeInvalid, putCalls.Load())
	}
}

func TestSaveOfficialProviderFormOriginPinningAndMultiInstance(t *testing.T) {
	t.Run("mismatched expectedOrigin on injected endpoint returns 409 KAP_ORIGIN_MISMATCH with zero requests", func(t *testing.T) {
		var requests atomic.Int32
		service, _ := newAutomationTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			writeNativeTestEnvelope(w, map[string]any{"providers": map[string]any{}, "models": map[string]any{}})
		}))
		wrongOrigin := "http://127.0.0.1:1"
		if service.kap.base == wrongOrigin {
			wrongOrigin = "http://127.0.0.1:2"
		}
		payload := fmt.Sprintf(`{"providerId":"team","expectedOrigin":%q,"payload":{"type":"openai","models":[{"model":"alpha","max_context_size":8192}]}}`, wrongOrigin)
		_, err := service.Action(context.Background(), "providers.saveOfficialForm", json.RawMessage(payload))
		var native *NativeError
		if !errors.As(err, &native) || native.Code != "KAP_ORIGIN_MISMATCH" || native.Status != http.StatusConflict {
			t.Fatalf("expected KAP_ORIGIN_MISMATCH 409, got %#v (%v)", native, err)
		}
		// Verify HTTP action handler preserves 409 + KAP_ORIGIN_MISMATCH.
		mux := http.NewServeMux()
		service.Register(mux)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/enhance/v1/desktop/actions/providers.saveOfficialForm", bytes.NewReader([]byte(payload)))
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "KAP_ORIGIN_MISMATCH") {
			t.Fatalf("HTTP handler did not preserve 409 KAP_ORIGIN_MISMATCH: code=%d body=%s", rec.Code, rec.Body.String())
		}
		if requests.Load() != 0 {
			t.Fatalf("mismatched origin issued KAP requests: %d", requests.Load())
		}
	})

	t.Run("multi-instance pins GET /config and PUT to daemon A even when daemon B has newer heartbeat", func(t *testing.T) {
		home := t.TempDir()
		sizeA := int64(2048)
		var getA, putA, callsB atomic.Int32
		var putBodyA map[string]any

		daemonA := kapServer(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
				getA.Add(1)
				writeNativeTestEnvelope(w, map[string]any{
					"providers": map[string]providerConfig{"team": {Type: "openai"}},
					"models": map[string]modelConfig{
						"team/alpha": {
							Provider:       "team",
							Model:          "alpha",
							MaxContextSize: 32768,
							MaxOutputSize:  &sizeA,
							Capabilities:   []string{"video_in"},
						},
					},
				})
			case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/providers/team"):
				putA.Add(1)
				dec := json.NewDecoder(r.Body)
				dec.UseNumber()
				_ = dec.Decode(&putBodyA)
				writeNativeTestEnvelope(w, map[string]any{"provider": map[string]any{"id": "team"}})
			default:
				http.NotFound(w, r)
			}
		})
		daemonB := kapServer(t, func(w http.ResponseWriter, r *http.Request) {
			callsB.Add(1)
			writeNativeTestEnvelope(w, map[string]any{"providers": map[string]any{}, "models": map[string]any{}})
		})

		writeInstanceWithHeartbeat(t, home, "a.json", "127.0.0.1", portOf(t, daemonA), 100, "tok-1")
		writeInstanceWithHeartbeat(t, home, "b.json", "127.0.0.1", portOf(t, daemonB), 999999, "tok-1")

		kap := NewKAPClient(home)
		// Prime default cache to daemon B first so we prove pinned requests ignore it.
		if cached, _, err := kap.endpoint(); err != nil || cached != daemonB.URL {
			t.Fatalf("expected cached endpoint to be daemon B, got %q err=%v", cached, err)
		}
		service := &NativeService{kap: kap, bus: NewEventBus(), cache: map[string]cachedSession{}}

		_, err := service.Action(context.Background(), "providers.saveOfficialForm", json.RawMessage(fmt.Sprintf(`{
			"providerId": "team",
			"expectedOrigin": %q,
			"payload": {"type": "openai", "models": [{"model": "alpha", "max_context_size": 32768}]},
			"opening": {}
		}`, daemonA.URL)))
		if err != nil {
			t.Fatalf("pinned saveOfficialForm to daemon A failed: %v", err)
		}
		if getA.Load() != 1 || putA.Load() != 1 || callsB.Load() != 0 {
			t.Fatalf("expected getA=1 putA=1 callsB=0, got getA=%d putA=%d callsB=%d", getA.Load(), putA.Load(), callsB.Load())
		}
		alphaBody := rawSlice(putBodyA["models"])[0]
		if got, err := numberValue(alphaBody["max_output_size"]); err != nil || got != 2048 {
			t.Fatalf("daemon A max_output_size not preserved: %#v (%v)", alphaBody["max_output_size"], err)
		}

		// Unknown port must be rejected with 409 KAP_ORIGIN_MISMATCH and 0 calls.
		unknownPort := 1
		for unknownPort == portOf(t, daemonA) || unknownPort == portOf(t, daemonB) {
			unknownPort++
		}
		_, err = service.Action(context.Background(), "providers.saveOfficialForm", json.RawMessage(fmt.Sprintf(`{
			"providerId": "team",
			"expectedOrigin": "http://127.0.0.1:%d",
			"payload": {"type": "openai", "models": [{"model": "alpha", "max_context_size": 32768}]},
			"opening": {}
		}`, unknownPort)))
		var native *NativeError
		if !errors.As(err, &native) || native.Code != "KAP_ORIGIN_MISMATCH" || native.Status != http.StatusConflict {
			t.Fatalf("expected KAP_ORIGIN_MISMATCH 409 for unknown port, got %#v (%v)", native, err)
		}
		if getA.Load() != 1 || putA.Load() != 1 || callsB.Load() != 0 {
			t.Fatalf("unknown port sent unexpected requests: getA=%d putA=%d callsB=%d", getA.Load(), putA.Load(), callsB.Load())
		}
	})

	t.Run("401 token refresh retry during saveOfficialForm stays pinned to daemon A", func(t *testing.T) {
		home := t.TempDir()
		var getA, putA, callsB atomic.Int32
		daemonA := kapServer(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
				if getA.Add(1) == 1 {
					if err := os.WriteFile(filepath.Join(home, "server.token"), []byte("tok-fresh"), 0o600); err != nil {
						t.Errorf("rotate token: %v", err)
					}
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if got := r.Header.Get("Authorization"); got != "Bearer tok-fresh" {
					t.Errorf("GET retry used wrong token: %q", got)
				}
				writeNativeTestEnvelope(w, map[string]any{
					"providers": map[string]providerConfig{"team": {Type: "openai"}},
					"models":    map[string]modelConfig{},
				})
			case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/providers/team"):
				if putA.Add(1) == 1 {
					if err := os.WriteFile(filepath.Join(home, "server.token"), []byte("tok-fresh-2"), 0o600); err != nil {
						t.Errorf("rotate token for PUT: %v", err)
					}
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if got := r.Header.Get("Authorization"); got != "Bearer tok-fresh-2" {
					t.Errorf("PUT retry used wrong token: %q", got)
				}
				writeNativeTestEnvelope(w, map[string]any{"provider": map[string]any{"id": "team"}})
			default:
				http.NotFound(w, r)
			}
		})
		daemonB := kapServer(t, func(w http.ResponseWriter, r *http.Request) {
			callsB.Add(1)
			writeNativeTestEnvelope(w, map[string]any{"providers": map[string]any{}, "models": map[string]any{}})
		})
		writeInstanceWithHeartbeat(t, home, "a.json", "127.0.0.1", portOf(t, daemonA), 100, "tok-stale")
		writeInstanceWithHeartbeat(t, home, "b.json", "127.0.0.1", portOf(t, daemonB), 999999, "tok-stale")

		service := &NativeService{kap: NewKAPClient(home), bus: NewEventBus(), cache: map[string]cachedSession{}}
		_, err := service.Action(context.Background(), "providers.saveOfficialForm", json.RawMessage(fmt.Sprintf(`{
			"providerId": "team",
			"expectedOrigin": %q,
			"payload": {"type": "openai", "models": [{"model": "alpha", "max_context_size": 8192}]},
			"opening": {}
		}`, daemonA.URL)))
		if err != nil {
			t.Fatalf("saveOfficialForm with 401 token refresh failed: %v", err)
		}
		if getA.Load() != 2 || putA.Load() != 2 || callsB.Load() != 0 {
			t.Fatalf("401 retries drifted from daemon A: getA=%d putA=%d callsB=%d", getA.Load(), putA.Load(), callsB.Load())
		}
	})
}

func TestProviderWritesRejectIncompleteConfigResponses(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{name: "null root", data: `null`},
		{name: "empty root object", data: `{}`},
		{name: "missing models", data: `{"providers":{}}`},
		{name: "null models", data: `{"providers":{},"models":null}`},
		{name: "null providers", data: `{"providers":null,"models":{}}`},
		{name: "array models", data: `{"providers":{},"models":[]}`},
		{name: "null model entry", data: `{"providers":{},"models":{"team/alpha":null}}`},
		{name: "null provider entry", data: `{"providers":{"team":null},"models":{}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var putCalls atomic.Int32
			service, _ := newAutomationTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut {
					putCalls.Add(1)
					writeNativeTestEnvelope(w, map[string]any{"provider": "team"})
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"code":0,"msg":"","data":` + tc.data + `}`))
			}))

			_, formErr := service.Action(context.Background(), "providers.saveOfficialForm", json.RawMessage(fmt.Sprintf(`{
				"providerId": "team",
				"expectedOrigin": %q,
				"payload": {"type": "openai", "models": [{"model": "alpha", "max_context_size": 8192}]},
				"opening": {}
			}`, service.kap.base)))
			if formErr == nil {
				t.Fatalf("saveOfficialForm unexpectedly succeeded on incomplete /config %s", tc.data)
			}

			_, updateErr := service.Action(context.Background(), "providers.updateModel", json.RawMessage(`{
				"providerId": "team",
				"model": "alpha",
				"edit": {"capabilities": ["image_in"]}
			}`))
			if updateErr == nil {
				t.Fatalf("updateModel unexpectedly succeeded on incomplete /config %s", tc.data)
			}

			_, batchErr := service.Action(context.Background(), "providers.updateModels", json.RawMessage(`{
				"providerId": "team",
				"models": [{"model": "alpha", "edit": {"capabilities": ["image_in"]}}]
			}`))
			if batchErr == nil {
				t.Fatalf("updateModels unexpectedly succeeded on incomplete /config %s", tc.data)
			}

			if putCalls.Load() != 0 {
				t.Fatalf("incomplete /config %s triggered %d PUT call(s)", tc.data, putCalls.Load())
			}
		})
	}
}

func TestProviderModelSaveLockQueueRespectsContextCancellation(t *testing.T) {
	t.Run("queued save returns immediately on ctx cancel with zero GET or PUT", func(t *testing.T) {
		store := newOfficialModelStore(inspectModels())
		baseHandler := store.handler()

		firstPutEntered := make(chan struct{})
		releaseFirstPut := make(chan struct{})
		var getCount, putCount atomic.Int32

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config") {
				getCount.Add(1)
			}
			if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/providers/") {
				if putCount.Add(1) == 1 {
					close(firstPutEntered)
					<-releaseFirstPut
				}
			}
			baseHandler.ServeHTTP(w, r)
		})
		service, _ := newAutomationTestService(t, handler)

		saveFormPayload := json.RawMessage(fmt.Sprintf(`{
			"providerId": "r2-inspect",
			"expectedOrigin": %q,
			"payload": {
				"type": "openai",
				"models": [
					{"model": "alpha", "max_context_size": 131072, "display_name": "R2 Alpha"},
					{"model": "beta", "max_context_size": 65536, "display_name": "R2 Beta"}
				]
			},
			"opening": {}
		}`, service.kap.base))

		firstDone := make(chan error, 1)
		go func() {
			_, err := service.Action(context.Background(), "providers.saveOfficialForm", saveFormPayload)
			firstDone <- err
		}()
		<-firstPutEntered

		queuedCtx, cancelQueued := context.WithCancel(context.Background())
		queuedDone := make(chan error, 1)
		go func() {
			_, err := service.Action(queuedCtx, "providers.saveOfficialForm", saveFormPayload)
			queuedDone <- err
		}()

		queuedUpdateCtx, cancelQueuedUpdate := context.WithCancel(context.Background())
		queuedUpdateDone := make(chan error, 1)
		go func() {
			_, err := service.Action(queuedUpdateCtx, "providers.updateModel", json.RawMessage(`{"providerId":"r2-inspect","model":"alpha","edit":{"capabilities":["image_in"]}}`))
			queuedUpdateDone <- err
		}()

		time.Sleep(25 * time.Millisecond)
		cancelQueued()
		cancelQueuedUpdate()

		select {
		case err := <-queuedDone:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context.Canceled for queued saveOfficialForm, got %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("queued saveOfficialForm did not abort promptly on context cancellation")
		}
		select {
		case err := <-queuedUpdateDone:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context.Canceled for queued updateModel, got %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("queued updateModel did not abort promptly on context cancellation")
		}

		close(releaseFirstPut)
		if err := <-firstDone; err != nil {
			t.Fatalf("first saveOfficialForm failed: %v", err)
		}
		// Only save #1 should have issued GET /config (1) and PUT /providers (1).
		if getCount.Load() != 1 || putCount.Load() != 1 {
			t.Fatalf("cancelled queued saves issued unexpected requests: get=%d put=%d", getCount.Load(), putCount.Load())
		}
	})

	t.Run("ctx cancelled after GET /config aborts before PUT", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var putCount atomic.Int32
		service, _ := newAutomationTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config") {
				writeNativeTestEnvelope(w, map[string]any{
					"providers": map[string]providerConfig{"team": {Type: "openai"}},
					"models": map[string]modelConfig{
						"team/alpha": {Provider: "team", Model: "alpha", MaxContextSize: 8192},
					},
				})
				cancel()
				return
			}
			if r.Method == http.MethodPut {
				putCount.Add(1)
				writeNativeTestEnvelope(w, map[string]any{"provider": "team"})
				return
			}
			http.NotFound(w, r)
		}))

		_, err := service.Action(ctx, "providers.saveOfficialForm", json.RawMessage(fmt.Sprintf(`{
			"providerId": "team",
			"expectedOrigin": %q,
			"payload": {"type": "openai", "models": [{"model": "alpha", "max_context_size": 8192}]},
			"opening": {}
		}`, service.kap.base)))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled before PUT, got %v", err)
		}
		if putCount.Load() != 0 {
			t.Fatalf("PUT ran despite ctx cancellation after GET /config: %d", putCount.Load())
		}
	})
}

func TestSaveOfficialProviderFormRejectsRedirectsWithoutLeakingAPIKey(t *testing.T) {
	t.Run("307 Temporary Redirect on PUT fails with 502 NATIVE_REDIRECT and leaks zero requests", func(t *testing.T) {
		home := t.TempDir()
		var getCalls, putCalls, targetCalls atomic.Int32
		target := kapServer(t, func(w http.ResponseWriter, r *http.Request) {
			targetCalls.Add(1)
			writeNativeTestEnvelope(w, map[string]any{"provider": map[string]any{"id": "team"}})
		})
		pinned := kapServer(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
				getCalls.Add(1)
				writeNativeTestEnvelope(w, map[string]any{
					"providers": map[string]providerConfig{"team": {Type: "openai"}},
					"models": map[string]modelConfig{
						"team/alpha": {Provider: "team", Model: "alpha", MaxContextSize: 32768},
					},
				})
			case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/providers/team"):
				putCalls.Add(1)
				w.Header().Set("Location", target.URL+r.URL.Path)
				w.WriteHeader(http.StatusTemporaryRedirect)
			default:
				http.NotFound(w, r)
			}
		})
		writeInstanceWithHeartbeat(t, home, "pinned.json", "127.0.0.1", portOf(t, pinned), 100, "secret-kap-token")

		service := &NativeService{kap: NewKAPClient(home), bus: NewEventBus(), cache: map[string]cachedSession{}}
		payload := fmt.Sprintf(`{
			"providerId": "team",
			"expectedOrigin": %q,
			"payload": {
				"type": "openai",
				"api_key": "sk-synthetic-secret-do-not-leak",
				"models": [{"model": "alpha", "max_context_size": 32768}]
			},
			"opening": {}
		}`, pinned.URL)

		_, err := service.Action(context.Background(), "providers.saveOfficialForm", json.RawMessage(payload))
		var native *NativeError
		if !errors.As(err, &native) || native.Code != "NATIVE_REDIRECT" || native.Status != http.StatusBadGateway {
			t.Fatalf("expected NATIVE_REDIRECT 502 on PUT 307, got %#v (%v)", native, err)
		}
		if getCalls.Load() != 1 || putCalls.Load() != 1 {
			t.Fatalf("expected getCalls=1 putCalls=1 (no retry), got get=%d put=%d", getCalls.Load(), putCalls.Load())
		}
		if targetCalls.Load() != 0 {
			t.Fatalf("redirect target received %d request(s) on PUT 307", targetCalls.Load())
		}

		// Verify HTTP action handler preserves 502 + NATIVE_REDIRECT and still leaks zero requests.
		mux := http.NewServeMux()
		service.Register(mux)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/enhance/v1/desktop/actions/providers.saveOfficialForm", bytes.NewReader([]byte(payload)))
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "NATIVE_REDIRECT") {
			t.Fatalf("HTTP handler did not preserve 502 NATIVE_REDIRECT: code=%d body=%s", rec.Code, rec.Body.String())
		}
		if targetCalls.Load() != 0 {
			t.Fatalf("redirect target received %d request(s) via HTTP handler", targetCalls.Load())
		}
	})

	t.Run("307 Temporary Redirect on GET /config aborts before PUT and leaks zero requests", func(t *testing.T) {
		home := t.TempDir()
		var getCalls, putCalls, targetCalls atomic.Int32
		target := kapServer(t, func(w http.ResponseWriter, r *http.Request) {
			targetCalls.Add(1)
			writeNativeTestEnvelope(w, map[string]any{
				"providers": map[string]providerConfig{"team": {Type: "openai"}},
				"models":    map[string]modelConfig{},
			})
		})
		pinned := kapServer(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
				getCalls.Add(1)
				w.Header().Set("Location", target.URL+r.URL.Path)
				w.WriteHeader(http.StatusTemporaryRedirect)
			case r.Method == http.MethodPut:
				putCalls.Add(1)
				writeNativeTestEnvelope(w, map[string]any{"provider": map[string]any{"id": "team"}})
			default:
				http.NotFound(w, r)
			}
		})
		writeInstanceWithHeartbeat(t, home, "pinned.json", "127.0.0.1", portOf(t, pinned), 100, "secret-kap-token")

		service := &NativeService{kap: NewKAPClient(home), bus: NewEventBus(), cache: map[string]cachedSession{}}
		_, err := service.Action(context.Background(), "providers.saveOfficialForm", json.RawMessage(fmt.Sprintf(`{
			"providerId": "team",
			"expectedOrigin": %q,
			"payload": {
				"type": "openai",
				"api_key": "sk-synthetic-secret-do-not-leak",
				"models": [{"model": "alpha", "max_context_size": 32768}]
			},
			"opening": {}
		}`, pinned.URL)))
		var native *NativeError
		if !errors.As(err, &native) || native.Code != "NATIVE_REDIRECT" || native.Status != http.StatusBadGateway {
			t.Fatalf("expected NATIVE_REDIRECT 502 on GET /config 307, got %#v (%v)", native, err)
		}
		if getCalls.Load() != 1 || putCalls.Load() != 0 {
			t.Fatalf("expected getCalls=1 putCalls=0, got get=%d put=%d", getCalls.Load(), putCalls.Load())
		}
		if targetCalls.Load() != 0 {
			t.Fatalf("redirect target received %d request(s) on GET 307", targetCalls.Load())
		}
	})

	t.Run("injected http.Client without CheckRedirect still blocks 307 on PUT", func(t *testing.T) {
		var targetCalls atomic.Int32
		target := kapServer(t, func(w http.ResponseWriter, r *http.Request) {
			targetCalls.Add(1)
			writeNativeTestEnvelope(w, map[string]any{"provider": map[string]any{"id": "team"}})
		})
		pinned := kapServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config") {
				writeNativeTestEnvelope(w, map[string]any{
					"providers": map[string]providerConfig{"team": {Type: "openai"}},
					"models":    map[string]modelConfig{},
				})
				return
			}
			w.Header().Set("Location", target.URL+r.URL.Path)
			w.WriteHeader(http.StatusTemporaryRedirect)
		})
		service := &NativeService{
			kap:   &KAPClient{client: &http.Client{}, base: pinned.URL, token: "secret-kap-token"},
			bus:   NewEventBus(),
			cache: map[string]cachedSession{},
		}
		_, err := service.Action(context.Background(), "providers.saveOfficialForm", json.RawMessage(fmt.Sprintf(`{
			"providerId": "team",
			"expectedOrigin": %q,
			"payload": {
				"type": "openai",
				"api_key": "sk-synthetic-secret-do-not-leak",
				"models": [{"model": "alpha", "max_context_size": 32768}]
			},
			"opening": {}
		}`, pinned.URL)))
		var native *NativeError
		if !errors.As(err, &native) || native.Code != "NATIVE_REDIRECT" || native.Status != http.StatusBadGateway {
			t.Fatalf("expected NATIVE_REDIRECT 502 with default http.Client, got %#v (%v)", native, err)
		}
		if targetCalls.Load() != 0 {
			t.Fatalf("redirect target received %d request(s) with injected http.Client", targetCalls.Load())
		}
	})
}

func TestSaveOfficialProviderFormPreserveCatalogImport(t *testing.T) {
	maxOut := int64(32768)
	adaptiveFalse := false
	var capturedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
			writeNativeTestEnvelope(w, map[string]any{
				"providers": map[string]providerConfig{
					"deepseek": {
						Type:         "openai",
						BaseURL:      "https://api.deepseek.com",
						DefaultModel: "deepseek/deepseek-reasoner",
					},
				},
				"models": map[string]modelConfig{
					"deepseek/deepseek-reasoner": {
						Provider:         "deepseek",
						Model:            "deepseek-reasoner",
						MaxContextSize:   131072,
						MaxOutputSize:    &maxOut,
						Capabilities:     []string{"image_in", "tool_use", "always_thinking"},
						SupportEfforts:   []string{"low", "medium", "high"},
						AdaptiveThinking: &adaptiveFalse,
						DefaultEffort:    "medium",
						OffEffort:        "minimal",
					},
					"deepseek/deepseek-chat": {
						Provider:       "deepseek",
						Model:          "deepseek-chat",
						MaxContextSize: 131072,
						Capabilities:   []string{"tool_use"},
					},
				},
			})
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/providers/deepseek"):
			if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
				t.Errorf("decode PUT body: %v", err)
			}
			writeNativeTestEnvelope(w, map[string]any{"provider": map[string]any{"id": "deepseek"}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	service := &NativeService{
		kap:   &KAPClient{client: server.Client(), base: server.URL, token: "secret-kap-token"},
		bus:   NewEventBus(),
		cache: map[string]cachedSession{},
	}

	// Simulate 1.5.0 AddProviderPage catalog follow-up PUT: omits base_url, default_model,
	// max_output_size, support_efforts, default_effort, off_effort and hardcodes adaptive_thinking: true.
	// Also simulates stale opening metadata so preserveCatalogImport must ignore opening diffs.
	_, err := service.Action(context.Background(), "providers.saveOfficialForm", json.RawMessage(fmt.Sprintf(`{
		"providerId": "deepseek",
		"expectedOrigin": %q,
		"preserveCatalogImport": true,
		"opening": {
			"deepseek-reasoner": {
				"raw": {"capabilities": ["tool_use"], "support_efforts": [], "adaptive_thinking": false}
			}
		},
		"payload": {
			"type": "openai",
			"models": [
				{
					"model": "deepseek-reasoner",
					"display_name": "DeepSeek R1 Custom",
					"max_context_size": 131072,
					"capabilities": ["tool_use", "thinking"],
					"adaptive_thinking": true
				},
				{
					"model": "custom-added",
					"max_context_size": 65536,
					"capabilities": ["tool_use"]
				}
			]
		}
	}`, server.URL)))
	if err != nil {
		t.Fatalf("saveOfficialForm preserveCatalogImport failed: %v", err)
	}
	if capturedBody["base_url"] != "https://api.deepseek.com" {
		t.Fatalf("base_url = %#v, want https://api.deepseek.com", capturedBody["base_url"])
	}
	if capturedBody["default_model"] != "deepseek-reasoner" {
		t.Fatalf("default_model = %#v, want deepseek-reasoner", capturedBody["default_model"])
	}
	models, ok := capturedBody["models"].([]any)
	if !ok || len(models) != 2 {
		t.Fatalf("models = %#v, want 2 models (removed deepseek-chat, kept reasoner + custom-added)", capturedBody["models"])
	}
	reasoner := models[0].(map[string]any)
	if reasoner["display_name"] != "DeepSeek R1 Custom" {
		t.Fatalf("display_name = %#v, want DeepSeek R1 Custom", reasoner["display_name"])
	}
	if int64(reasoner["max_output_size"].(float64)) != 32768 {
		t.Fatalf("max_output_size = %#v, want 32768", reasoner["max_output_size"])
	}
	if reasoner["adaptive_thinking"] != false {
		t.Fatalf("adaptive_thinking = %#v, want false (preserved from catalog import)", reasoner["adaptive_thinking"])
	}
	if reasoner["default_effort"] != "medium" || reasoner["off_effort"] != "minimal" {
		t.Fatalf("efforts = %v / %v, want medium / minimal", reasoner["default_effort"], reasoner["off_effort"])
	}
	capsRaw, _ := json.Marshal(reasoner["capabilities"])
	if string(capsRaw) != `["image_in","tool_use","always_thinking"]` {
		t.Fatalf("capabilities = %s, want catalog capabilities", capsRaw)
	}
	effortsRaw, _ := json.Marshal(reasoner["support_efforts"])
	if string(effortsRaw) != `["low","medium","high"]` {
		t.Fatalf("support_efforts = %s, want [low medium high]", effortsRaw)
	}
}

func TestRoutedProviderModelKeyMatchesExplicitProviderWithoutCrossContamination(t *testing.T) {
	var mu sync.Mutex
	outputLimit := int64(65536)
	models := map[string]modelConfig{
		"magpie/auto/gemini-4-neon": {
			Provider:       "magpie",
			Model:          "auto/gemini-4-neon",
			MaxContextSize: 500000,
			Capabilities:   []string{"thinking", "image_in", "tool_use"},
		},
		"magpie/group/glm-5-3-flash": {
			Provider:       "magpie-route-chat",
			Model:          "group/glm-5-3-flash",
			MaxContextSize: 500000,
			MaxOutputSize:  &outputLimit,
			Capabilities:   []string{"thinking", "image_in", "tool_use"},
		},
	}
	providers := map[string]providerConfig{
		"magpie":            {Type: "kimi", BaseURL: "http://127.0.0.1:3425/v1"},
		"magpie-route-chat": {Type: "openai", BaseURL: "http://127.0.0.1:3425/v1"},
	}
	var lastPutProvider string
	var lastPutModels []string
	service, _ := newAutomationTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
			writeNativeTestEnvelope(w, map[string]any{"providers": providers, "models": models})
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/providers/"):
			providerID := strings.TrimPrefix(r.URL.Path, "/api/v1/providers/")
			var body struct {
				Models []modelConfig `json:"models"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			lastPutProvider = providerID
			lastPutModels = nil
			for key, existing := range models {
				if existing.Provider == providerID {
					delete(models, key)
				}
			}
			for _, item := range body.Models {
				item.Provider = providerID
				lastPutModels = append(lastPutModels, item.Model)
				models[providerID+"/"+item.Model] = item
			}
			writeNativeTestEnvelope(w, map[string]any{"provider": map[string]any{"id": providerID}})
		default:
			http.NotFound(w, r)
		}
	}))

	// Updating magpie-route-chat resolves models["magpie/group/glm-5-3-flash"] via item.Provider.
	if _, err := service.Action(context.Background(), "providers.updateModel", json.RawMessage(`{
		"providerId": "magpie-route-chat",
		"model": "magpie-route-chat/group/glm-5-3-flash",
		"edit": {"capabilities": ["image_in", "video_in", "tool_use"], "thinking": "toggle"}
	}`)); err != nil {
		t.Fatalf("updateModel on routed provider failed: %v", err)
	}
	if lastPutProvider != "magpie-route-chat" || !reflect.DeepEqual(lastPutModels, []string{"group/glm-5-3-flash"}) {
		t.Fatalf("unexpected routed PUT: provider=%q models=%v", lastPutProvider, lastPutModels)
	}

	// Updating magpie must NOT sweep in models owned by magpie-route-chat even when their TOML key starts with "magpie/".
	mu.Lock()
	models["magpie/group/deepseek-v4-1-flash"] = modelConfig{
		Provider:       "magpie-route-chat",
		Model:          "group/deepseek-v4-1-flash",
		MaxContextSize: 500000,
		Capabilities:   []string{"thinking", "tool_use"},
	}
	mu.Unlock()
	if _, err := service.Action(context.Background(), "providers.updateModel", json.RawMessage(`{
		"providerId": "magpie",
		"model": "magpie/auto/gemini-4-neon",
		"edit": {"capabilities": ["image_in", "tool_use"], "thinking": "always"}
	}`)); err != nil {
		t.Fatalf("updateModel on magpie failed: %v", err)
	}
	if lastPutProvider != "magpie" || !reflect.DeepEqual(lastPutModels, []string{"auto/gemini-4-neon"}) {
		t.Fatalf("magpie PUT leaked routed models: provider=%q models=%v", lastPutProvider, lastPutModels)
	}
}
