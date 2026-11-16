package usage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postJSON(t *testing.T, mux *http.ServeMux, path, body string) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest("POST", path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	var payload map[string]any
	if recorder.Body.Len() > 0 {
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode %s: %v (%s)", path, err, recorder.Body.String())
		}
	}
	return recorder.Code, payload
}

func eventBody(price string) string {
	return `{"events":[{"providerId":"p","model":"m","requestId":"r","attemptId":"a","sessionId":"s","agentId":"main","at":"2026-11-10T00:00:00Z","protocol":"openai","raw":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":0}}` + price + `}]}`
}

func TestHTTPPriceInputOutputMustBePresent(t *testing.T) {
	_, mux := newMux(t)
	cases := []struct {
		name  string
		price string
	}{
		{"both omitted", `,"price":{"currency":"USD","source":"fixture","fetchedAt":"2026-11-10T00:00:00Z"}`},
		{"input omitted", `,"price":{"output":3,"currency":"USD","source":"fixture","fetchedAt":"2026-11-10T00:00:00Z"}`},
		{"output omitted", `,"price":{"input":1,"currency":"USD","source":"fixture","fetchedAt":"2026-11-10T00:00:00Z"}`},
		{"input null", `,"price":{"input":null,"output":3,"currency":"USD","source":"fixture","fetchedAt":"2026-11-10T00:00:00Z"}`},
		{"output null", `,"price":{"input":1,"output":null,"currency":"USD","source":"fixture","fetchedAt":"2026-11-10T00:00:00Z"}`},
		{"unknown nested field", `,"price":{"input":1,"output":3,"cacheread":1,"currency":"USD","source":"fixture","fetchedAt":"2026-11-10T00:00:00Z"}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, payload := postJSON(t, mux, "/enhance/v1/usage", eventBody(testCase.price))
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%+v)", status, payload)
			}
			if payload["error"].(map[string]any)["code"] != "INVALID_JSON" {
				t.Fatalf("code = %+v", payload["error"])
			}
			assertNoEvents(t, mux)
		})
	}
}

func TestHTTPExplicitZeroInputOutputIsValid(t *testing.T) {
	_, mux := newMux(t)
	status, payload := postJSON(t, mux, "/enhance/v1/usage",
		eventBody(`,"price":{"input":0,"output":0,"currency":"USD","source":"fixture","fetchedAt":"2026-11-10T00:00:00Z"}`))
	if status != http.StatusOK {
		t.Fatalf("status = %d (%+v)", status, payload)
	}
	status, summary := call(t, mux, "GET", "/enhance/v1/usage/summary", nil)
	if status != http.StatusOK {
		t.Fatalf("summary status = %d", status)
	}
	estimate := summary["estimate"].(map[string]any)
	if estimate["kind"] != "ESTIMATE" {
		t.Fatalf("explicit zero price should be a known free estimate: %+v", estimate)
	}
	if estimate["amount"].(float64) != 0 {
		t.Fatalf("amount = %+v", estimate["amount"])
	}
}

func TestHTTPPriceRejectsNonFinite(t *testing.T) {
	_, mux := newMux(t)
	// JSON cannot carry NaN/Inf literally, but a huge exponent can overflow on
	// decode. It must be rejected rather than accepted as +Inf.
	for _, body := range []string{
		eventBody(`,"price":{"input":1e400,"output":3,"currency":"USD","source":"fixture","fetchedAt":"2026-11-10T00:00:00Z"}`),
		eventBody(`,"price":{"input":1,"output":1e400,"currency":"USD","source":"fixture","fetchedAt":"2026-11-10T00:00:00Z"}`),
	} {
		status, payload := postJSON(t, mux, "/enhance/v1/usage", body)
		if status != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%+v)", status, payload)
		}
	}
}

func TestProtocolNAWriteDoesNotPanicAndIsNotPriced(t *testing.T) {
	price := &Price{Input: 1, Output: 3, Read: numberPtr(0.1), Currency: "USD", Source: "fixture", FetchedAt: stringPtr("2026-11-10T00:00:00Z"), Unit: priceUnit}
	for _, protocol := range []string{"openai", "responses", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			event := summaryEvent{protocol: protocol, fresh: ptr(100), input: ptr(100), output: ptr(20), read: ptr(0), write: ptr(5)}
			// Must not dereference a nil cacheWrite price for a protocol where
			// cache writes are N/A; the contradictory count is INCONSISTENT.
			cost, result := computeCost(event, price)
			if result != costInvalid {
				t.Fatalf("result = %s cost = %v, want %s", result, cost, costInvalid)
			}
		})
	}
	// Anthropic does report cache writes; a missing rate is a price gap, not a panic.
	anthropic := summaryEvent{protocol: "anthropic", fresh: ptr(100), input: ptr(115), output: ptr(20), read: ptr(0), write: ptr(15)}
	cost, result := computeCost(anthropic, price)
	if result != costNoPrice || cost != 0 {
		t.Fatalf("anthropic result = %s cost = %v, want NO_PRICE", result, cost)
	}
}

func TestKnownZeroCacheReadPricesInputAndOutputWithoutCacheRate(t *testing.T) {
	// read is a reported 0 and the price has no cacheRead rate at all: the
	// ordinary input/output cost is still exact.
	price := &Price{Input: 1, Output: 3, Currency: "USD", Source: "fixture", FetchedAt: stringPtr("2026-11-10T00:00:00Z"), Unit: priceUnit}
	event := summaryEvent{protocol: "openai", fresh: ptr(100), input: ptr(100), output: ptr(20), read: ptr(0)}
	cost, result := computeCost(event, price)
	if result != costOK {
		t.Fatalf("result = %s", result)
	}
	want := 100.0/1e6*1 + 20.0/1e6*3
	if !almostEqual(cost, want) {
		t.Fatalf("cost = %v, want %v", cost, want)
	}
	// The same holds for Gemini.
	gemini := summaryEvent{protocol: "gemini", fresh: ptr(80), input: ptr(80), output: ptr(15), read: ptr(0)}
	cost, result = computeCost(gemini, price)
	if result != costOK {
		t.Fatalf("gemini result = %s", result)
	}
	if wantGemini := 80.0/1e6*1 + 15.0/1e6*3; !almostEqual(cost, wantGemini) {
		t.Fatalf("gemini cost = %v, want %v", cost, wantGemini)
	}
}

func TestNonZeroCacheReadStillRequiresCacheRate(t *testing.T) {
	price := &Price{Input: 1, Output: 3, Currency: "USD", Source: "fixture", FetchedAt: stringPtr("2026-11-10T00:00:00Z"), Unit: priceUnit}
	event := summaryEvent{protocol: "openai", fresh: ptr(60), input: ptr(100), output: ptr(20), read: ptr(40)}
	if _, result := computeCost(event, price); result != costNoPrice {
		t.Fatalf("result = %s, want NO_PRICE for nonzero cached read without a rate", result)
	}
}

func TestOverflowingEstimateNeverEmitsInfinity(t *testing.T) {
	// A technically finite price multiplied by a large count can exceed float64.
	// The event must be refused, and the summary must still serialize.
	s, _ := newTestService(t)
	huge := 1e308
	event := request("r1", "2026-11-10T00:00:00Z")
	event.Protocol = "openai"
	event.Counts = &Counts{Fresh: ptr(1 << 40), Input: ptr(1 << 40), Output: ptr(0), Read: ptr(0)}
	event.Price = &Price{Input: huge, Output: huge, Currency: "USD", Source: "fixture", FetchedAt: stringPtr("2026-11-10T00:00:00Z"), Unit: priceUnit}
	if _, err := s.Ingest([]EventInput{event}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	summary, err := s.Summary(listFilter{})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Estimate.Kind == "ESTIMATE" {
		t.Fatalf("overflowing estimate was reported as a number: %+v", summary.Estimate)
	}
	if summary.Estimate.InconsistentUsage != 1 {
		t.Fatalf("overflow tally = %+v", summary.Estimate)
	}
	if _, err := json.Marshal(summary); err != nil {
		t.Fatalf("summary must stay JSON-encodable: %v", err)
	}
}

func TestPriceUnmarshalKeepsExplicitZero(t *testing.T) {
	var price Price
	if err := json.Unmarshal([]byte(`{"input":0,"output":0,"currency":"USD","source":"fixture","fetchedAt":"2026-11-10T00:00:00Z"}`), &price); err != nil {
		t.Fatalf("explicit zero should decode: %v", err)
	}
	if price.Input != 0 || price.Output != 0 {
		t.Fatalf("price = %+v", price)
	}
	if err := json.Unmarshal([]byte(`{"currency":"USD"}`), &price); err == nil {
		t.Fatal("missing input/output must fail")
	}
	if err := json.Unmarshal([]byte(`{"input":1,"output":2,"extra":3}`), &price); err == nil {
		t.Fatal("unknown nested price field must fail")
	}
}

func assertNoEvents(t *testing.T, mux *http.ServeMux) {
	t.Helper()
	status, payload := call(t, mux, "GET", "/enhance/v1/usage?limit=1000", nil)
	if status != http.StatusOK {
		t.Fatalf("list status = %d", status)
	}
	if total := int(payload["total"].(float64)); total != 0 {
		t.Fatalf("invalid price wrote %d events", total)
	}
}

func TestHTTPZeroCacheReadWithoutCacheRateIsPricedExactly(t *testing.T) {
	_, mux := newMux(t)
	body := `{"events":[{"providerId":"openai","model":"gpt-5.1","requestId":"r1","attemptId":"a1","sessionId":"s1","agentId":"main","protocol":"openai","source":"official","at":"2026-11-10T00:00:00Z",
		"raw":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":0}},
		"price":{"input":1,"output":3,"currency":"USD","source":"fixture","fetchedAt":"2026-11-10T00:00:00Z"}}]}`
	status, payload := postJSON(t, mux, "/enhance/v1/usage", body)
	if status != http.StatusOK || payload["added"].(float64) != 1 {
		t.Fatalf("ingest = %d %+v", status, payload)
	}
	status, summary := call(t, mux, "GET", "/enhance/v1/usage/summary", nil)
	if status != http.StatusOK {
		t.Fatalf("summary = %d", status)
	}
	estimate := summary["estimate"].(map[string]any)
	if estimate["kind"] != "ESTIMATE" {
		t.Fatalf("estimate = %+v", estimate)
	}
	// 100 fresh / 1e6 * 1 + 20 output / 1e6 * 3
	if want := 0.00016; estimate["amount"].(float64) != want {
		t.Fatalf("amount = %v, want %v", estimate["amount"], want)
	}
	if estimate["costBasis"] != "event_price" {
		t.Fatalf("cost basis = %v", estimate["costBasis"])
	}
}

func TestHTTPAnthropicMissingCacheFieldsNotPriced(t *testing.T) {
	_, mux := newMux(t)
	body := `{"events":[{"providerId":"anthropic","model":"claude-opus-5-5","requestId":"r1","attemptId":"a1","sessionId":"s1","agentId":"main","protocol":"anthropic","source":"official","at":"2026-11-10T00:00:00Z",
		"raw":{"input_tokens":100,"output_tokens":20},
		"price":{"input":1,"output":3,"cacheRead":0.1,"cacheWrite":1.2,"currency":"USD","source":"fixture","fetchedAt":"2026-11-10T00:00:00Z"}}]}`
	status, payload := postJSON(t, mux, "/enhance/v1/usage", body)
	if status != http.StatusOK {
		t.Fatalf("ingest = %d %+v", status, payload)
	}
	status, summary := call(t, mux, "GET", "/enhance/v1/usage/summary", nil)
	if status != http.StatusOK {
		t.Fatalf("summary = %d", status)
	}
	estimate := summary["estimate"].(map[string]any)
	if estimate["kind"] != "UNKNOWN" || estimate["amount"] != nil {
		t.Fatalf("incomplete Anthropic usage was priced: %+v", estimate)
	}
	if estimate["unknownUsageEvents"].(float64) != 1 {
		t.Fatalf("unknown tally = %+v", estimate)
	}
}
