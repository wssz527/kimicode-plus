package usage

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
			t.Fatalf("marshal: %v", err)
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
			t.Fatalf("decode %s %s: %v (%s)", method, path, err, recorder.Body.String())
		}
	}
	return recorder.Code, payload
}

func TestHTTPUsageIngestListSummary(t *testing.T) {
	_, mux := newMux(t)
	event := map[string]any{
		"providerId": "anthropic", "model": "claude-opus-5-5", "requestId": "req-1", "attemptId": "a1",
		"sessionId": "session-1", "projectId": "project-1", "agentId": "main", "role": "main",
		"requestKind": "coding", "protocol": "anthropic", "source": "official", "at": "2026-11-10T00:00:00Z",
		"raw": map[string]any{"input_tokens": 100, "output_tokens": 20, "cache_read_input_tokens": 50, "cache_creation_input_tokens": 10},
	}
	status, result := call(t, mux, "POST", "/enhance/v1/usage", map[string]any{"events": []any{event, event}})
	if status != http.StatusOK || result["added"].(float64) != 1 || result["duplicates"].(float64) != 1 {
		t.Fatalf("ingest = %d %+v", status, result)
	}
	status, list := call(t, mux, "GET", "/enhance/v1/usage?sessionId=session-1&limit=10", nil)
	if status != http.StatusOK || list["total"].(float64) != 1 {
		t.Fatalf("list = %d %+v", status, list)
	}
	item := list["items"].([]any)[0].(map[string]any)
	if item["input"].(float64) != 160 || item["output"].(float64) != 20 {
		t.Fatalf("event = %+v", item)
	}
	if item["cacheRead"].(float64) != 50 || item["cacheWrite"].(float64) != 10 {
		t.Fatalf("cache fields = %+v", item)
	}
	provenance := list["provenance"].(map[string]any)
	if len(provenance) != 1 {
		t.Fatalf("provenance = %+v", provenance)
	}
	status, summary := call(t, mux, "GET", "/enhance/v1/usage/summary?sessionId=session-1", nil)
	if status != http.StatusOK {
		t.Fatalf("summary = %d %+v", status, summary)
	}
	if summary["input"].(float64) != 160 || summary["requests"].(float64) != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary["estimate"].(map[string]any)["kind"] != "UNKNOWN" {
		t.Fatalf("estimate = %+v", summary["estimate"])
	}
	status, empty := call(t, mux, "GET", "/enhance/v1/usage/summary?sessionId=missing", nil)
	if status != http.StatusOK || empty["input"] != nil {
		t.Fatalf("empty summary = %d %+v", status, empty)
	}
}

func TestHTTPUsageFiltersValidateTimes(t *testing.T) {
	_, mux := newMux(t)
	status, payload := call(t, mux, "GET", "/enhance/v1/usage?from=yesterday", nil)
	if status != http.StatusBadRequest || payload["error"].(map[string]any)["code"] != "INVALID_TIME_FILTER" {
		t.Fatalf("bad from = %d %+v", status, payload)
	}
	status, payload = call(t, mux, "GET", "/enhance/v1/usage?from=2026-11-11T00:00:00Z&to=2026-11-10T00:00:00Z", nil)
	if status != http.StatusBadRequest || payload["error"].(map[string]any)["code"] != "INVALID_RANGE" {
		t.Fatalf("bad range = %d %+v", status, payload)
	}
	status, payload = call(t, mux, "POST", "/enhance/v1/usage", map[string]any{"events": []any{
		map[string]any{"providerId": "p", "model": "m", "requestId": "r", "at": "2026-11-10T00:00:00Z", "attemptId": "a", "sessionId": "s", "agentId": "main"},
	}})
	if status != http.StatusOK {
		t.Fatalf("valid ingest = %d %+v", status, payload)
	}
}

func TestHTTPDemoAndQuotas(t *testing.T) {
	_, mux := newMux(t)
	status, first := call(t, mux, "POST", "/enhance/v1/usage/demo", nil)
	if status != http.StatusOK || first["added"].(float64) == 0 {
		t.Fatalf("demo = %d %+v", status, first)
	}
	status, second := call(t, mux, "POST", "/enhance/v1/usage/demo", nil)
	if status != http.StatusOK || second["added"].(float64) != 0 {
		t.Fatalf("demo repeat = %d %+v", status, second)
	}
	status, quotas := call(t, mux, "GET", "/enhance/v1/quotas", nil)
	if status != http.StatusOK || len(quotas["ledgers"].([]any)) != 4 {
		t.Fatalf("quotas = %d %+v", status, quotas)
	}
	for _, item := range quotas["items"].([]any) {
		snapshot := item.(map[string]any)
		if snapshot["remaining"] != nil {
			t.Fatalf("mock quota claims a balance: %+v", snapshot)
		}
	}
	status, actual := call(t, mux, "POST", "/enhance/v1/usage/actual", map[string]any{
		"observationId": "bill-1", "providerId": "anthropic", "amount": 1.25, "currency": "USD", "source": "official", "observedAt": "2026-11-10T12:00:00Z",
	})
	if status != http.StatusCreated || actual["amount"].(float64) != 1.25 {
		t.Fatalf("actual = %d %+v", status, actual)
	}
	status, summary := call(t, mux, "GET", "/enhance/v1/usage/summary", nil)
	if status != http.StatusOK {
		t.Fatalf("summary = %d", status)
	}
	if summary["actual"].(map[string]any)["amount"].(float64) != 1.25 {
		t.Fatalf("actual ledger = %+v", summary["actual"])
	}
	if summary["estimate"].(map[string]any)["kind"] != "ESTIMATE" {
		t.Fatalf("demo should be estimable: %+v", summary["estimate"])
	}
}

func TestHTTPUnlinkedAndUnknownCounts(t *testing.T) {
	_, mux := newMux(t)
	status, _ := call(t, mux, "POST", "/enhance/v1/usage", map[string]any{"events": []any{
		map[string]any{"providerId": "p", "model": "m", "requestId": "wire-1", "at": "2026-11-10T00:00:00Z", "source": "official", "raw": map[string]any{"input_tokens": 12}},
	}})
	if status != http.StatusOK {
		t.Fatalf("ingest = %d", status)
	}
	status, unlinked := call(t, mux, "GET", "/enhance/v1/usage/unlinked", nil)
	if status != http.StatusOK || unlinked["total"].(float64) != 1 {
		t.Fatalf("unlinked = %d %+v", status, unlinked)
	}
	_, summary := call(t, mux, "GET", "/enhance/v1/usage/summary", nil)
	if summary["input"] != nil {
		t.Fatalf("unlinked leaked into totals: %+v", summary["input"])
	}
}
