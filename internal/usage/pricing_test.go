package usage

import (
	"testing"
)

// pricedEvent builds an event with explicit counts and a per-million price so
// cost assertions do not depend on normalization.
func pricedEvent(id, protocol string, counts Counts, price *Price) EventInput {
	event := request(id, "2026-11-10T00:00:00Z")
	event.Protocol = protocol
	event.Counts = &counts
	event.Price = price
	return event
}

func usdPrice(read, write *float64) *Price {
	return &Price{Input: 1, Output: 3, Read: read, Write: write, Currency: "USD", Source: "fixture", FetchedAt: stringPtr("2026-11-10T00:00:00Z"), Unit: priceUnit}
}

func TestAnthropicMissingCacheFieldsIsUnknownNotCheap(t *testing.T) {
	s, _ := newTestService(t)
	event := pricedEvent("r1", "anthropic", Counts{Fresh: ptr(100), Input: ptr(100), Output: ptr(20)}, usdPrice(numberPtr(0.1), numberPtr(1.2)))
	event.Raw = nil
	// Deliberately override the derived input total so the cache fields stay
	// absent while fresh/output are known: an incomplete Anthropic record.
	event.Counts = &Counts{Fresh: ptr(100), Output: ptr(20)}
	if _, err := s.Ingest([]EventInput{event}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	summary, err := s.Summary(listFilter{})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Estimate.Kind != "UNKNOWN" || summary.Estimate.Amount != nil {
		t.Fatalf("estimate = %+v, want UNKNOWN without cache accounting", summary.Estimate)
	}
	if summary.Estimate.UnknownUsage != 1 || summary.Estimate.PricedEvents != 0 {
		t.Fatalf("estimate tally = %+v", summary.Estimate)
	}
}

func TestOpenAIMissingCachedTokensDoesNotFallBackToInputPrice(t *testing.T) {
	s, _ := newTestService(t)
	// input=100, output=20, cached_tokens unknown. Pricing the whole 100 at the
	// fresh rate would overstate cost because some input may be cheap cache hits.
	event := pricedEvent("r1", "openai", Counts{Input: ptr(100), Output: ptr(20)}, usdPrice(numberPtr(0.1), numberPtr(1.2)))
	event.Raw = nil
	if _, err := s.Ingest([]EventInput{event}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	summary, err := s.Summary(listFilter{})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Estimate.Kind != "UNKNOWN" || summary.Estimate.Amount != nil {
		t.Fatalf("estimate = %+v, want UNKNOWN when cached tokens are unknown", summary.Estimate)
	}
}

func TestCompleteUsageIsPricedExactly(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		counts   Counts
		price    *Price
		want     float64
	}{
		{
			name:     "anthropic fresh output read write",
			protocol: "anthropic",
			counts:   Counts{Fresh: ptr(100), Input: ptr(160), Output: ptr(20), Read: ptr(50), Write: ptr(10)},
			price:    &Price{Input: 1, Output: 3, Read: numberPtr(0.1), Write: numberPtr(1.2), Currency: "USD", Source: "fixture", FetchedAt: stringPtr("2026-11-10T00:00:00Z"), Unit: priceUnit},
			want:     0.000177,
		},
		{
			name:     "openai fresh output read, write not applicable",
			protocol: "openai",
			counts:   Counts{Fresh: ptr(60), Input: ptr(100), Output: ptr(20), Read: ptr(40)},
			price:    &Price{Input: 1, Output: 3, Read: numberPtr(0.1), Currency: "USD", Source: "fixture", FetchedAt: stringPtr("2026-11-10T00:00:00Z"), Unit: priceUnit},
			want:     0.000124,
		},
		{
			name:     "gemini thoughts included in output with no cache",
			protocol: "gemini",
			counts:   Counts{Fresh: ptr(80), Input: ptr(80), Output: ptr(15), Read: ptr(0)},
			price:    &Price{Input: 1, Output: 3, Read: numberPtr(0.1), Currency: "USD", Source: "fixture", FetchedAt: stringPtr("2026-11-10T00:00:00Z"), Unit: priceUnit},
			want:     0.000125,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			s, _ := newTestService(t)
			event := pricedEvent("r1", testCase.protocol, testCase.counts, testCase.price)
			event.Raw = nil
			if _, err := s.Ingest([]EventInput{event}); err != nil {
				t.Fatalf("ingest: %v", err)
			}
			summary, err := s.Summary(listFilter{})
			if err != nil {
				t.Fatalf("summary: %v", err)
			}
			if summary.Estimate.Kind != "ESTIMATE" || summary.Estimate.Amount == nil {
				t.Fatalf("estimate = %+v", summary.Estimate)
			}
			if !almostEqual(*summary.Estimate.Amount, testCase.want) {
				t.Fatalf("amount = %v, want %v", *summary.Estimate.Amount, testCase.want)
			}
			if !summary.Estimate.Complete {
				t.Fatalf("estimate should be complete: %+v", summary.Estimate)
			}
			if summary.Estimate.CostBasis != "event_price" {
				t.Fatalf("cost basis = %q", summary.Estimate.CostBasis)
			}
		})
	}
}

func TestAnthropicMissingCachePlusReadablePriceStaysUnknown(t *testing.T) {
	s, _ := newTestService(t)
	// Read known, write missing: still unknown for Anthropic, and definitely not
	// computed by adding read twice.
	event := pricedEvent("r1", "anthropic", Counts{Fresh: ptr(100), Input: ptr(150), Output: ptr(20), Read: ptr(50)}, usdPrice(numberPtr(0.1), nil))
	event.Raw = nil
	if _, err := s.Ingest([]EventInput{event}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	summary, err := s.Summary(listFilter{})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Estimate.Kind != "UNKNOWN" || summary.Estimate.Amount != nil {
		t.Fatalf("estimate = %+v", summary.Estimate)
	}
}

func TestInconsistentCacheCountsAreRejectedNotDoubleCounted(t *testing.T) {
	s, _ := newTestService(t)
	// cached_read > input cannot be produced by a correct normalizer, but a
	// hand-built record or a bad upstream report must not be priced.
	event := pricedEvent("r1", "openai", Counts{Fresh: ptr(60), Input: ptr(100), Output: ptr(20), Read: ptr(140)}, usdPrice(numberPtr(0.1), nil))
	event.Raw = nil
	if _, err := s.Ingest([]EventInput{event}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	summary, err := s.Summary(listFilter{})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Estimate.Kind != "UNKNOWN" || summary.Estimate.Amount != nil {
		t.Fatalf("estimate = %+v", summary.Estimate)
	}
	if summary.Estimate.InconsistentUsage != 1 {
		t.Fatalf("inconsistent tally = %+v", summary.Estimate)
	}
}

func TestUnknownProtocolUsageIsNotPriced(t *testing.T) {
	s, _ := newTestService(t)
	event := pricedEvent("r1", "cohere", Counts{Fresh: ptr(10), Input: ptr(10), Output: ptr(2)}, usdPrice(numberPtr(0.1), nil))
	event.Raw = nil
	if _, err := s.Ingest([]EventInput{event}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	summary, err := s.Summary(listFilter{})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Estimate.Kind != "UNKNOWN" {
		t.Fatalf("estimate = %+v", summary.Estimate)
	}
	if summary.Estimate.UnknownProtocolEvents != 1 {
		t.Fatalf("protocol tally = %+v", summary.Estimate)
	}
}

func TestActualBillingIsNeverAttributedToASessionFilter(t *testing.T) {
	s, _ := newTestService(t)
	event := request("r1", "2026-11-10T10:00:00Z")
	event.Raw = raw(t, map[string]any{"input_tokens": float64(10), "output_tokens": float64(2), "cache_read_input_tokens": float64(0), "cache_creation_input_tokens": float64(0)})
	event.ProviderID = "anthropic"
	other := event
	other.RequestID = "r2"
	other.ProviderID = "openai"
	other.AgentID = "worker"
	if _, err := s.Ingest([]EventInput{event, other}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	for _, bill := range []ActualBillingInput{
		{ObservationID: "b1", ProviderID: "anthropic", Amount: 10, Currency: "USD", Source: "official", ObservedAt: "2026-11-10T12:00:00Z"},
		{ObservationID: "b2", ProviderID: "openai", Amount: 4, Currency: "USD", Source: "official", ObservedAt: "2026-11-10T12:00:00Z"},
	} {
		if _, err := s.RecordActualBilling(bill); err != nil {
			t.Fatalf("billing: %v", err)
		}
	}
	// A session-scoped summary cannot attribute provider billing to the session.
	scoped, err := s.Summary(listFilter{SessionID: "s1"})
	if err != nil {
		t.Fatalf("scoped summary: %v", err)
	}
	if scoped.Actual.Kind == "PROVIDER_LEDGER" || scoped.Actual.Amount != nil {
		t.Fatalf("session summary fabricated actual cost: %+v", scoped.Actual)
	}
	if scoped.Actual.ProviderLedger == nil || scoped.Actual.ProviderLedger.Amount == nil || *scoped.Actual.ProviderLedger.Amount != 14 {
		t.Fatalf("provider ledger = %+v", scoped.Actual.ProviderLedger)
	}
	if scoped.Actual.Attribution != "PROVIDER_LEVEL" {
		t.Fatalf("attribution = %q", scoped.Actual.Attribution)
	}
	// A project-only filter is equally unattributable.
	project, err := s.Summary(listFilter{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("project summary: %v", err)
	}
	if project.Actual.Amount != nil {
		t.Fatalf("project summary fabricated actual cost: %+v", project.Actual)
	}
	// Provider filter is the one dimension bills genuinely carry.
	provider, err := s.Summary(listFilter{ProviderID: "anthropic"})
	if err != nil {
		t.Fatalf("provider summary: %v", err)
	}
	if provider.Actual.Kind != "PROVIDER_LEDGER" || provider.Actual.Amount == nil || *provider.Actual.Amount != 10 {
		t.Fatalf("provider summary = %+v", provider.Actual)
	}
	if len(provider.Actual.Observations) != 1 {
		t.Fatalf("provider observations = %+v", provider.Actual.Observations)
	}
}

func TestActualBillingTimeWindowIsApplied(t *testing.T) {
	s, _ := newTestService(t)
	event := request("r1", "2026-11-10T10:00:00Z")
	event.Raw = raw(t, map[string]any{"input_tokens": float64(10), "output_tokens": float64(2), "cache_read_input_tokens": float64(0), "cache_creation_input_tokens": float64(0)})
	event.ProviderID = "anthropic"
	if _, err := s.Ingest([]EventInput{event}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	for _, bill := range []ActualBillingInput{
		{ObservationID: "b-inside", ProviderID: "anthropic", Amount: 2, Currency: "USD", Source: "official", ObservedAt: "2026-11-10T12:00:00Z"},
		{ObservationID: "b-outside", ProviderID: "anthropic", Amount: 100, Currency: "USD", Source: "official", ObservedAt: "2026-11-09T12:00:00Z"},
	} {
		if _, err := s.RecordActualBilling(bill); err != nil {
			t.Fatalf("billing: %v", err)
		}
	}
	from := int64(1)
	to := int64(1)
	parsedFrom, _ := parseTimestampForTest(t, "2026-11-10T00:00:00Z")
	parsedTo, _ := parseTimestampForTest(t, "2026-11-10T23:59:59Z")
	from, to = parsedFrom, parsedTo
	summary, err := s.Summary(listFilter{ProviderID: "anthropic", FromMs: &from, ToMs: &to})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Actual.Amount == nil || *summary.Actual.Amount != 2 {
		t.Fatalf("windowed actual = %+v", summary.Actual)
	}
}

func TestLatePriceFallbackIsMarkedAsRetrospective(t *testing.T) {
	s, _ := newTestService(t)
	withPrice := pricedEvent("r1", "openai", Counts{Fresh: ptr(100), Input: ptr(100), Output: ptr(20), Read: ptr(0)}, usdPrice(numberPtr(0.1), nil))
	withPrice.Raw = nil
	if _, err := s.Ingest([]EventInput{withPrice}); err != nil {
		t.Fatalf("ingest priced: %v", err)
	}
	// Same model, later event with no price: only the saved current price can be
	// used, which is a retrospective estimate.
	unpriced := pricedEvent("r2", "openai", Counts{Fresh: ptr(100), Input: ptr(100), Output: ptr(20), Read: ptr(0)}, nil)
	unpriced.Raw = nil
	unpriced.At = "2026-11-12T00:00:00Z"
	if _, err := s.Ingest([]EventInput{unpriced}); err != nil {
		t.Fatalf("ingest unpriced: %v", err)
	}
	summary, err := s.Summary(listFilter{})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Estimate.CostBasis != "CURRENT_PRICE_FALLBACK" {
		t.Fatalf("cost basis = %q", summary.Estimate.CostBasis)
	}
	if summary.Estimate.LateFallbackEvents != 1 {
		t.Fatalf("fallback count = %d", summary.Estimate.LateFallbackEvents)
	}
	if summary.Estimate.Complete {
		t.Fatalf("fallback estimate must not claim completeness: %+v", summary.Estimate)
	}
	if summary.Estimate.Note == "" {
		t.Fatal("fallback estimate needs an explicit note")
	}
}

func parseTimestampForTest(t *testing.T, raw string) (int64, error) {
	t.Helper()
	value, err := parseTimestamp(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	if value == nil {
		t.Fatalf("parse %s returned nil", raw)
	}
	return *value, nil
}
