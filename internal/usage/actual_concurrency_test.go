package usage

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestActualBillingRejectsUnknownJSONKeys(t *testing.T) {
	_, mux := newMux(t)
	cases := []struct{ name, body string }{
		{"unknown top field", `{"providerId":"p","observationId":"u1","amount":1,"currency":"USD","source":"official","observedAt":"2026-11-10T12:00:00Z","extra":"x"}`},
		{"typo amount", `{"providerId":"p","observationId":"u2","amout":1,"currency":"USD","source":"official"}`},
		{"nested object field", `{"providerId":"p","observationId":"u3","amount":1,"currency":"USD","source":"official","meta":{"a":1}}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, payload := postJSON(t, mux, "/enhance/v1/usage/actual", testCase.body)
			if status != 400 {
				t.Fatalf("status = %d, want 400 (%+v)", status, payload)
			}
		})
	}
	_, summary := call(t, mux, "GET", "/enhance/v1/usage/summary", nil)
	actual := summary["actual"].(map[string]any)
	if len(actual["observations"].([]any)) != 0 {
		t.Fatalf("invalid observations were persisted: %+v", actual)
	}
}

func TestActualBillingOmittedObservedAtRetryIsIdempotent(t *testing.T) {
	s, db := newTestService(t)
	first, err := s.RecordActualBilling(ActualBillingInput{ObservationID: "b1", ProviderID: "anthropic", Amount: 1.5, Currency: "USD", Source: "official"})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	second, err := s.RecordActualBilling(ActualBillingInput{ObservationID: "b1", ProviderID: "anthropic", Amount: 1.5, Currency: "USD", Source: "official"})
	if err != nil {
		t.Fatalf("identical retry without observedAt: %v", err)
	}
	if !second.Duplicate {
		t.Fatalf("retry not marked duplicate: %+v", second)
	}
	if second.ObservedAt != first.ObservedAt {
		t.Fatalf("retry observedAt = %q, want stored %q", second.ObservedAt, first.ObservedAt)
	}
	var rows, micros int64
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(amount_micros),0) FROM usage_actual_billing`).Scan(&rows, &micros); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || micros != 1500000 {
		t.Fatalf("rows = %d micros = %d", rows, micros)
	}
	// An explicitly different time is still a conflict.
	if _, err := s.RecordActualBilling(ActualBillingInput{ObservationID: "b1", ProviderID: "anthropic", Amount: 1.5, Currency: "USD", Source: "official", ObservedAt: "2026-01-01T00:00:00Z"}); err == nil {
		t.Fatal("explicit different observedAt must conflict")
	}
}

func TestActualBillingConcurrentOmittedObservedAt(t *testing.T) {
	s, db := newTestService(t)
	const workers = 8
	var wg sync.WaitGroup
	results := make([]ActualObservation, workers)
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = s.RecordActualBilling(ActualBillingInput{ObservationID: "b1", ProviderID: "anthropic", Amount: 2, Currency: "USD", Source: "official"})
		}(i)
	}
	wg.Wait()
	var stored string
	for index := 0; index < workers; index++ {
		if errs[index] != nil {
			t.Fatalf("worker %d: %v", index, errs[index])
		}
		if results[index].ObservedAt == "" {
			t.Fatalf("worker %d returned empty observedAt", index)
		}
		if stored == "" {
			stored = results[index].ObservedAt
		} else if results[index].ObservedAt != stored {
			t.Fatalf("workers disagree on observedAt: %q vs %q", results[index].ObservedAt, stored)
		}
	}
	var rows int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_actual_billing`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("concurrent retries created %d rows", rows)
	}
}

func TestActualBillingAmountBoundary(t *testing.T) {
	s, db := newTestService(t)
	// The documented ceiling is 1e12, chosen because float64 cannot represent the
	// int64 micro edge distinctly. Just below it is accepted, at it is rejected.
	// 1e12-0.001 is exactly representable in micro units at this magnitude.
	receipt, err := s.RecordActualBilling(ActualBillingInput{ObservationID: "max", ProviderID: "p", Amount: 999999999999.999, Currency: "USD", Source: "official"})
	if err != nil {
		t.Fatalf("max representable rejected: %v", err)
	}
	if _, err := s.RecordActualBilling(ActualBillingInput{ObservationID: "over", ProviderID: "p", Amount: 1e12, Currency: "USD", Source: "official"}); err == nil {
		t.Fatal("amount at the ceiling must be rejected")
	}
	if _, err := s.RecordActualBilling(ActualBillingInput{ObservationID: "over2", ProviderID: "p", Amount: 9.3e12, Currency: "USD", Source: "official"}); err == nil {
		t.Fatal("amount above the ceiling must be rejected")
	}
	if _, err := s.RecordActualBilling(ActualBillingInput{ObservationID: "inf", ProviderID: "p", Amount: math.Inf(1), Currency: "USD", Source: "official"}); err == nil {
		t.Fatal("Inf must be rejected")
	}
	if _, err := s.RecordActualBilling(ActualBillingInput{ObservationID: "nan", ProviderID: "p", Amount: math.NaN(), Currency: "USD", Source: "official"}); err == nil {
		t.Fatal("NaN must be rejected")
	}
	var rows int64
	var micros int64
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(amount_micros),0) FROM usage_actual_billing`).Scan(&rows, &micros); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("rows = %d, want 1", rows)
	}
	// float64 has ~1e-4 resolution at this magnitude, so the assertion is that
	// storage and receipt agree on the same integer micro value, not a literal.
	if micros <= 0 || micros >= 1000000000000000000 {
		t.Fatalf("micros out of range: %d", micros)
	}
	if int64(math.Round(receipt.Amount*1e6)) != micros {
		t.Fatalf("receipt micros %d != stored %d", int64(math.Round(receipt.Amount*1e6)), micros)
	}
}

func TestConcurrentDemoKeepsTwoSnapshots(t *testing.T) {
	s, db := newTestService(t)
	const workers = 6
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, errs[index] = s.Demo()
		}(i)
	}
	wg.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("demo %d: %v", index, err)
		}
	}
	var snapshots, events int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_quota_snapshots`).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if snapshots != 2 {
		t.Fatalf("concurrent demo left %d quota snapshots, want 2", snapshots)
	}
	if events != 6 {
		t.Fatalf("concurrent demo left %d events, want 6", events)
	}
}
