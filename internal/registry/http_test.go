package registry

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newMux(t *testing.T) (*Service, *http.ServeMux) {
	t.Helper()
	s, _ := newTestService(t)
	mux := http.NewServeMux()
	s.Register(mux)
	return s, mux
}

func call(t *testing.T, mux *http.ServeMux, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	var payload map[string]any
	if recorder.Body.Len() > 0 {
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode %s %s response: %v (%s)", method, path, err, recorder.Body.String())
		}
	}
	return recorder.Code, payload
}

func TestHTTPRegistryFlow(t *testing.T) {
	_, mux := newMux(t)
	status, payload := call(t, mux, "GET", "/enhance/v1/provider-presets", nil)
	if status != http.StatusOK || payload["total"].(float64) == 0 {
		t.Fatalf("presets = %d %+v", status, payload)
	}
	status, provider := call(t, mux, "POST", "/enhance/v1/providers", map[string]any{
		"id": "kimi", "name": "Kimi", "protocol": "openai", "baseUrl": "https://api.moonshot.cn/v1", "secretRef": "keychain://kimi",
	})
	if status != http.StatusCreated || provider["id"] != "kimi" {
		t.Fatalf("create provider = %d %+v", status, provider)
	}
	status, _ = call(t, mux, "POST", "/enhance/v1/providers", map[string]any{
		"id": "evil", "name": "Evil", "protocol": "openai", "baseUrl": "https://user:pass@evil.example/v1?token=abc",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("credential URL status = %d", status)
	}
	status, models := call(t, mux, "GET", "/enhance/v1/models", nil)
	if status != http.StatusOK || models["total"].(float64) != 0 {
		t.Fatalf("models = %d %+v", status, models)
	}
	status, discovery := call(t, mux, "POST", "/enhance/v1/models/discovery", map[string]any{"providerId": "kimi"})
	if status != http.StatusOK || discovery["mock"] != true || discovery["external"] != false {
		t.Fatalf("discovery = %d %+v", status, discovery)
	}
	records := discovery["records"].([]any)
	status, preview := call(t, mux, "POST", "/enhance/v1/models/preview", map[string]any{"records": records})
	if status != http.StatusOK {
		t.Fatalf("preview = %d %+v", status, preview)
	}
	rev := preview["revision"].(float64)
	status, imported := call(t, mux, "POST", "/enhance/v1/models/import", map[string]any{"records": records, "revision": rev})
	if status != http.StatusOK || imported["imported"].(float64) == 0 {
		t.Fatalf("import = %d %+v", status, imported)
	}
	status, again := call(t, mux, "POST", "/enhance/v1/models/import", map[string]any{"records": records, "revision": rev})
	if status != http.StatusConflict || again["error"].(map[string]any)["code"] != "STALE_REVISION" {
		t.Fatalf("stale import = %d %+v", status, again)
	}
	newRev := imported["revision"].(float64)
	status, override := call(t, mux, "POST", "/enhance/v1/models/override", map[string]any{
		"providerId": "kimi", "id": "kimi-k2.5", "field": "context", "value": nil, "revision": newRev,
	})
	if status != http.StatusOK {
		t.Fatalf("override = %d %+v", status, override)
	}
	after := override["after"].(map[string]any)
	if after["source"] != "manual" || after["value"] != nil {
		t.Fatalf("override after = %+v", after)
	}
	status, missing := call(t, mux, "POST", "/enhance/v1/models/override", map[string]any{
		"providerId": "kimi", "id": "kimi-k2.5", "field": "context", "revision": override["revision"],
	})
	if status != http.StatusBadRequest || missing["error"].(map[string]any)["code"] != "MISSING_VALUE" {
		t.Fatalf("missing value = %d %+v", status, missing)
	}
	status, changes := call(t, mux, "GET", "/enhance/v1/model-changes?limit=10&offset=0", nil)
	if status != http.StatusOK || changes["total"].(float64) < 2 {
		t.Fatalf("changes = %d %+v", status, changes)
	}
	first := changes["items"].([]any)[0].(map[string]any)
	status, detail := call(t, mux, "GET", "/enhance/v1/model-changes/"+jsonNumber(first["id"]), nil)
	if status != http.StatusOK || len(detail["models"].([]any)) == 0 {
		t.Fatalf("detail = %d %+v", status, detail)
	}
	status, gateways := call(t, mux, "GET", "/enhance/v1/gateways", nil)
	if status != http.StatusOK || gateways["items"].([]any)[0].(map[string]any)["balance"] != nil {
		t.Fatalf("gateways = %d %+v", status, gateways)
	}
}

func TestHTTPModelsDevPreviewAndApply(t *testing.T) {
	_, mux := newMux(t)
	catalog := loadFixture(t)
	status, preview := call(t, mux, "POST", "/enhance/v1/models/modelsdev", map[string]any{"catalog": catalog, "revision": 0, "preview": true})
	if status != http.StatusOK || preview["preview"] != true {
		t.Fatalf("modelsdev preview = %d %+v", status, preview)
	}
	if len(preview["changes"].([]any)) == 0 {
		t.Fatalf("preview changes empty: %+v", preview)
	}
	status, applied := call(t, mux, "POST", "/enhance/v1/models/modelsdev", map[string]any{"catalog": catalog, "revision": 0})
	if status != http.StatusOK || applied["preview"] != false {
		t.Fatalf("modelsdev apply = %d %+v", status, applied)
	}
	status, models := call(t, mux, "GET", "/enhance/v1/models?limit=100", nil)
	if status != http.StatusOK || models["total"].(float64) < 8 {
		t.Fatalf("models after modelsdev = %d %+v", status, models)
	}
	items := models["items"].([]any)
	first := items[0].(map[string]any)
	caps := first["capabilities"].(map[string]any)
	if caps["cacheTTL"].(map[string]any)["source"] != "UNKNOWN" {
		t.Fatalf("cacheTTL should be UNKNOWN: %+v", caps["cacheTTL"])
	}
	fetched := caps["context"].(map[string]any)["fetchedAt"].(string)
	if fetched != "2026-11-10T08:12:17.608929Z" {
		t.Fatalf("fetchedAt = %q, want fixture retrieved_at in UTC", fetched)
	}
}

func jsonNumber(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
