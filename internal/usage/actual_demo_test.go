package usage

import (
	"encoding/json"
	"math"
	"net/http"
	"testing"
)

func actualBody(id, amount, currency string) string {
	obs := `"observationId":"` + id + `"`
	currencyField := ""
	if currency != "" {
		currencyField = `,"currency":"` + currency + `"`
	}
	amountField := ""
	if amount != "" {
		amountField = `,"amount":` + amount
	}
	return `{"providerId":"anthropic",` + obs + amountField + currencyField + `,"source":"official","observedAt":"2026-11-10T12:00:00Z"}`
}

// Problem 2: a missing or null amount decodes to 0 with a plain float64 field,
// which would be recorded as a zero-amount bill.
func TestHTTPActualAmountMustBePresent(t *testing.T) {
	_, mux := newMux(t)
	cases := []struct{ name, body string }{
		{"omitted", actualBody("b1", "", "USD")},
		{"null", actualBody("b2", "null", "USD")},
		{"string", actualBody("b3", `"12.5"`, "USD")},
		{"negative", actualBody("b4", "-1", "USD")},
		{"overflow", actualBody("b5", "1e400", "USD")},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, payload := postJSON(t, mux, "/enhance/v1/usage/actual", testCase.body)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%+v)", status, payload)
			}
		})
	}
	_, summary := call(t, mux, "GET", "/enhance/v1/usage/summary", nil)
	actual := summary["actual"].(map[string]any)
	if len(actual["observations"].([]any)) != 0 {
		t.Fatalf("invalid amounts were persisted: %+v", actual)
	}
}

func TestHTTPActualZeroAndSmallAmountsAreValid(t *testing.T) {
	_, mux := newMux(t)
	status, payload := postJSON(t, mux, "/enhance/v1/usage/actual", actualBody("zero", "0", "USD"))
	if status != http.StatusCreated || payload["amount"].(float64) != 0 {
		t.Fatalf("zero amount = %d %+v", status, payload)
	}
	status, payload = postJSON(t, mux, "/enhance/v1/usage/actual", actualBody("small", "0.000001", "USD"))
	if status != http.StatusCreated || payload["amount"].(float64) != 0.000001 {
		t.Fatalf("small amount = %d %+v", status, payload)
	}
	status, payload = postJSON(t, mux, "/enhance/v1/usage/actual", actualBody("normal", "12.345678", "USD"))
	if status != http.StatusCreated || payload["amount"].(float64) != 12.345678 {
		t.Fatalf("normal amount = %d %+v", status, payload)
	}
}

func TestActualAmountOverflowingMicrosIsRejected(t *testing.T) {
	_, mux := newMux(t)
	// 9.3e12 USD is finite as a float64 but overflows int64 micros.
	status, payload := postJSON(t, mux, "/enhance/v1/usage/actual", actualBody("huge", "9.3e12", "USD"))
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%+v)", status, payload)
	}
	if payload["error"].(map[string]any)["code"] != "INVALID_AMOUNT" {
		t.Fatalf("code = %+v", payload["error"])
	}
}

// Problem 4: a duplicate observation ID silently keeps the old row but returns
// the new input as if it had been persisted.
func TestDuplicateActualObservationDoesNotFakeReceipt(t *testing.T) {
	s, _ := newTestService(t)
	if _, err := s.RecordActualBilling(ActualBillingInput{ObservationID: "b1", ProviderID: "anthropic", Amount: 1.5, Currency: "USD", Source: "official", ObservedAt: "2026-11-10T12:00:00Z"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	status, payload := postJSON(t, newMuxWith(t, s), "/enhance/v1/usage/actual", actualBody("b1", "99", "USD"))
	if status != http.StatusConflict {
		t.Fatalf("conflicting duplicate status = %d (%+v)", status, payload)
	}
	errorBody := payload["error"].(map[string]any)
	if errorBody["code"] != "OBSERVATION_CONFLICT" {
		t.Fatalf("code = %+v", errorBody)
	}
	// An identical retry is accepted idempotently and reports the stored row.
	status, payload = postJSON(t, newMuxWith(t, s), "/enhance/v1/usage/actual",
		`{"observationId":"b1","providerId":"anthropic","amount":1.5,"currency":"USD","source":"official","observedAt":"2026-11-10T12:00:00Z"}`)
	if status != http.StatusOK {
		t.Fatalf("identical retry status = %d (%+v)", status, payload)
	}
	if payload["amount"].(float64) != 1.5 || payload["providerId"] != "anthropic" {
		t.Fatalf("retry receipt = %+v", payload)
	}
	if duplicate, ok := payload["duplicate"].(bool); !ok || !duplicate {
		t.Fatalf("retry should mark duplicate: %+v", payload)
	}
	event := request("r1", "2026-11-10T12:00:00Z")
	event.ProviderID = "anthropic"
	event.Raw = raw(t, map[string]any{"input_tokens": float64(10), "output_tokens": float64(2), "cache_read_input_tokens": float64(0), "cache_creation_input_tokens": float64(0)})
	if _, err := s.Ingest([]EventInput{event}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	summary, err := s.Summary(listFilter{ProviderID: "anthropic"})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Actual.Amount == nil || *summary.Actual.Amount != 1.5 {
		t.Fatalf("duplicate changed the ledger: %+v", summary.Actual)
	}
	if len(summary.Actual.Observations) != 1 {
		t.Fatalf("duplicate created rows: %+v", summary.Actual.Observations)
	}
}

// Problem 5: demo repeated must not grow the quota snapshot table.
func TestRepeatedDemoDoesNotGrowQuotaSnapshots(t *testing.T) {
	s, db := newTestService(t)
	if _, err := s.Demo(); err != nil {
		t.Fatalf("demo: %v", err)
	}
	quotasAfterFirst, err := s.Quotas()
	if err != nil {
		t.Fatalf("quotas: %v", err)
	}
	if len(quotasAfterFirst) != 2 {
		t.Fatalf("first demo quotas = %d, want 2", len(quotasAfterFirst))
	}
	for i := 0; i < 3; i++ {
		result, err := s.Demo()
		if err != nil {
			t.Fatalf("demo %d: %v", i, err)
		}
		if added := result["added"].(int); added != 0 {
			t.Fatalf("repeat demo %d added %d events", i, added)
		}
	}
	quotasAfterRepeats, err := s.Quotas()
	if err != nil {
		t.Fatalf("quotas: %v", err)
	}
	if len(quotasAfterRepeats) != len(quotasAfterFirst) {
		t.Fatalf("demo grew quota snapshots %d -> %d", len(quotasAfterFirst), len(quotasAfterRepeats))
	}
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_quota_snapshots`).Scan(&total); err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 2 {
		t.Fatalf("quota rows = %d, want 2", total)
	}
}

func TestDemoDoesNotRewriteUserQuotaSnapshots(t *testing.T) {
	s, _ := newTestService(t)
	userSnapshot, err := s.RecordQuota(QuotaInput{
		Ledger: "SUBSCRIPTION", ProviderID: "kimi", Scope: "user-plan",
		Remaining: stringPtr("42"), Source: "manual", ObservedAt: "2026-11-01T00:00:00Z", Note: "user entered",
	})
	if err != nil {
		t.Fatalf("user quota: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.Demo(); err != nil {
			t.Fatalf("demo: %v", err)
		}
	}
	quotas, err := s.Quotas()
	if err != nil {
		t.Fatalf("quotas: %v", err)
	}
	found := false
	for _, quota := range quotas {
		if quota.ID == userSnapshot.ID {
			found = true
			if quota.Remaining == nil || *quota.Remaining != "42" || quota.Note != "user entered" {
				t.Fatalf("demo rewrote user snapshot: %+v", quota)
			}
		}
	}
	if !found {
		t.Fatal("demo removed the user snapshot")
	}
}

// A duplicate actual-billing request that repeats identical values must not be
// mistaken for a new charge, and the JSON must stay encodable.
func TestActualBillingResponseIsJSONEncodable(t *testing.T) {
	s, _ := newTestService(t)
	observation, err := s.RecordActualBilling(ActualBillingInput{ObservationID: "b1", ProviderID: "p", Amount: 0, Currency: "USD", Source: "official", ObservedAt: "2026-11-10T00:00:00Z"})
	if err != nil {
		t.Fatalf("billing: %v", err)
	}
	if _, err := json.Marshal(observation); err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if math.IsInf(observation.Amount, 0) || math.IsNaN(observation.Amount) {
		t.Fatalf("non-finite amount: %v", observation.Amount)
	}
}

func newMuxWith(t *testing.T, s *Service) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	s.Register(mux)
	return mux
}
