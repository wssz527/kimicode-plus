package usage

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"enhancedkimi/internal/storage"
)

func newTestService(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := New(db)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return s, db
}

func raw(t *testing.T, value map[string]any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal raw: %v", err)
	}
	return encoded
}

func request(id string, at string) EventInput {
	return EventInput{
		ProviderID: "p", Model: "m", RequestID: id, AttemptID: "a1",
		SessionID: "s1", ProjectID: "p1", AgentID: "main", Role: "main",
		Protocol: "anthropic", Source: "official", At: at,
	}
}

func ptr(value int64) *int64 { return &value }

func TestNormalizationNeverZeroFillsUnknown(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		raw      map[string]any
		want     Counts
	}{
		{
			name:     "openai cached subset not added twice",
			protocol: "openai",
			raw:      map[string]any{"prompt_tokens": float64(100), "completion_tokens": float64(20), "prompt_tokens_details": map[string]any{"cached_tokens": float64(40)}},
			want:     Counts{Fresh: ptr(60), Input: ptr(100), Output: ptr(20), Read: ptr(40), Write: nil},
		},
		{
			name:     "responses same semantics",
			protocol: "responses",
			raw:      map[string]any{"input_tokens": float64(100), "output_tokens": float64(5), "input_tokens_details": map[string]any{"cached_tokens": float64(30)}},
			want:     Counts{Fresh: ptr(70), Input: ptr(100), Output: ptr(5), Read: ptr(30)},
		},
		{
			name:     "anthropic sums cache into input",
			protocol: "anthropic",
			raw:      map[string]any{"input_tokens": float64(100), "output_tokens": float64(20), "cache_read_input_tokens": float64(50), "cache_creation_input_tokens": float64(10)},
			want:     Counts{Fresh: ptr(100), Input: ptr(160), Output: ptr(20), Read: ptr(50), Write: ptr(10)},
		},
		{
			name:     "anthropic missing cache write keeps input unknown",
			protocol: "anthropic",
			raw:      map[string]any{"input_tokens": float64(100), "output_tokens": float64(20), "cache_read_input_tokens": float64(50)},
			want:     Counts{Fresh: ptr(100), Input: nil, Output: ptr(20), Read: ptr(50), Write: nil},
		},
		{
			name:     "empty stays unknown",
			protocol: "openai",
			raw:      map[string]any{},
			want:     Counts{},
		},
		{
			name:     "negative and string counts unknown but valid zero kept",
			protocol: "openai",
			raw:      map[string]any{"prompt_tokens": float64(0), "completion_tokens": float64(-1)},
			want:     Counts{Input: ptr(0), Fresh: nil},
		},
		{
			name:     "string counts rejected",
			protocol: "openai",
			raw:      map[string]any{"prompt_tokens": "12"},
			want:     Counts{},
		},
		{
			name:     "gemini thoughts only when reported",
			protocol: "google",
			raw:      map[string]any{"promptTokenCount": float64(80), "cachedContentTokenCount": float64(20), "candidatesTokenCount": float64(10), "thoughtsTokenCount": float64(5)},
			want:     Counts{Fresh: ptr(60), Input: ptr(80), Output: ptr(15), Read: ptr(20)},
		},
		{
			name:     "gemini missing thoughts is unknown output",
			protocol: "google",
			raw:      map[string]any{"candidatesTokenCount": float64(10)},
			want:     Counts{Output: nil},
		},
		{
			name:     "unknown protocol stays unknown",
			protocol: "cohere",
			raw:      map[string]any{"prompt_tokens": float64(10)},
			want:     Counts{},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := Normalize(testCase.protocol, testCase.raw)
			if !countsEqual(got, testCase.want) {
				t.Fatalf("normalize = %+v, want %+v", got, testCase.want)
			}
		})
	}
}

func countsEqual(a, b Counts) bool {
	same := func(x, y *int64) bool {
		if x == nil || y == nil {
			return x == y
		}
		return *x == *y
	}
	return same(a.Fresh, b.Fresh) && same(a.Input, b.Input) && same(a.Output, b.Output) && same(a.Read, b.Read) && same(a.Write, b.Write)
}

func TestDuplicateObservationKeepsOneAuthoritativeRow(t *testing.T) {
	s, _ := newTestService(t)
	monitor := request("r1", "2026-11-10T00:00:00Z")
	monitor.Source = "monitor"
	monitor.Raw = raw(t, map[string]any{"input_tokens": float64(100), "output_tokens": float64(20), "cache_read_input_tokens": float64(50), "cache_creation_input_tokens": float64(10)})
	first, err := s.Ingest([]EventInput{monitor})
	if err != nil {
		t.Fatalf("ingest monitor: %v", err)
	}
	if first.Added != 1 {
		t.Fatalf("first = %+v", first)
	}
	official := monitor
	official.Source = "official"
	official.Raw = raw(t, map[string]any{"input_tokens": float64(100), "output_tokens": float64(25), "cache_read_input_tokens": float64(50), "cache_creation_input_tokens": float64(10)})
	second, err := s.Ingest([]EventInput{official})
	if err != nil {
		t.Fatalf("ingest official: %v", err)
	}
	if second.Updated != 1 || second.Added != 0 {
		t.Fatalf("second = %+v", second)
	}
	third, err := s.Ingest([]EventInput{official})
	if err != nil {
		t.Fatalf("re-ingest official: %v", err)
	}
	if third.Duplicates != 1 || third.Updated != 0 {
		t.Fatalf("third = %+v", third)
	}
	events, total, err := s.ListEvents(listFilter{}, 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(events) != 1 {
		t.Fatalf("events = %d/%d", len(events), total)
	}
	if events[0].Source != "official" || events[0].Output == nil || *events[0].Output != 25 {
		t.Fatalf("authoritative row = %+v", events[0])
	}
	provenance, err := s.ProvenanceFor([]int64{events[0].ID})
	if err != nil {
		t.Fatalf("provenance: %v", err)
	}
	if len(provenance[events[0].ID]) < 2 {
		t.Fatalf("provenance links = %+v", provenance)
	}
}

func TestRealRetryCountsTwiceButRepeatedAttemptDoesNot(t *testing.T) {
	s, _ := newTestService(t)
	first := request("r1", "2026-11-10T00:00:00Z")
	first.Raw = raw(t, map[string]any{"input_tokens": float64(100), "output_tokens": float64(20), "cache_read_input_tokens": float64(50), "cache_creation_input_tokens": float64(10)})
	retry := first
	retry.AttemptID = "a2"
	if _, err := s.Ingest([]EventInput{first, retry, retry}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	events, total, err := s.ListEvents(listFilter{}, 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 {
		t.Fatalf("retry events = %d, want 2", total)
	}
	var sum int64
	for _, event := range events {
		if event.Input != nil {
			sum += *event.Input
		}
	}
	if sum != 320 {
		t.Fatalf("sum input = %d, want 320", sum)
	}
}

func TestConcurrentDedupIngest(t *testing.T) {
	s, _ := newTestService(t)
	event := request("r-concurrent", "2026-11-10T00:00:00Z")
	event.Raw = raw(t, map[string]any{"input_tokens": float64(10), "output_tokens": float64(2), "cache_read_input_tokens": float64(0), "cache_creation_input_tokens": float64(0)})
	const workers = 8
	var wg sync.WaitGroup
	added := make([]int, workers)
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			result, err := s.Ingest([]EventInput{event})
			added[index] = result.Added
			errs[index] = err
		}(i)
	}
	wg.Wait()
	for index := 0; index < workers; index++ {
		if errs[index] != nil {
			t.Fatalf("worker %d: %v", index, errs[index])
		}
	}
	_, total, err := s.ListEvents(listFilter{}, 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 {
		t.Fatalf("concurrent dedup produced %d rows, want 1", total)
	}
	summary, err := s.Summary(listFilter{})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Input == nil || *summary.Input != 10 {
		t.Fatalf("summary input = %v, want 10 (not multiplied)", summary.Input)
	}
}

func TestSnapshotsAreNotBillable(t *testing.T) {
	s, _ := newTestService(t)
	snapshot := request("state-1", "2026-11-10T00:00:00Z")
	snapshot.Kind = "snapshot"
	snapshot.Raw = raw(t, map[string]any{"input_tokens": float64(900), "output_tokens": float64(0)})
	result, err := s.Ingest([]EventInput{snapshot})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if result.Snapshots != 1 || result.Added != 0 {
		t.Fatalf("result = %+v", result)
	}
	_, total, err := s.ListEvents(listFilter{}, 0, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 0 {
		t.Fatalf("snapshot entered the ledger: %d", total)
	}
}

func TestUnknownUsageIsNotZeroAndFiltersKeepNull(t *testing.T) {
	s, _ := newTestService(t)
	known := request("r-known", "2026-11-10T00:00:00Z")
	known.Raw = raw(t, map[string]any{"input_tokens": float64(100), "output_tokens": float64(20), "cache_read_input_tokens": float64(50), "cache_creation_input_tokens": float64(10)})
	partial := request("r-partial", "2026-11-10T00:02:00Z")
	partial.Raw = raw(t, map[string]any{"input_tokens": float64(30), "output_tokens": float64(3)})
	unknown := request("r-unknown", "2026-11-10T00:01:00Z")
	unknown.Raw = raw(t, map[string]any{})
	if _, err := s.Ingest([]EventInput{known, partial, unknown}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	summary, err := s.Summary(listFilter{})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Input != nil {
		t.Fatalf("input = %v, want null because one row lacks the input total", summary.Input)
	}
	if summary.PartialKnown.Input.Known != 1 || summary.PartialKnown.Input.Total != 3 {
		t.Fatalf("coverage = %+v", summary.PartialKnown.Input)
	}
	if summary.KnownRequests != 1 || summary.PartialKnownRequests != 1 || summary.UnknownRequests != 1 {
		t.Fatalf("known/partial/unknown = %d/%d/%d", summary.KnownRequests, summary.PartialKnownRequests, summary.UnknownRequests)
	}
	if summary.PartialKnown.Output.Known != 2 || summary.PartialKnown.Output.Sum == nil || *summary.PartialKnown.Output.Sum != 23 {
		t.Fatalf("output partial = %+v", summary.PartialKnown.Output)
	}
	if summary.Coverage["output"] <= 0 || summary.Coverage["output"] >= 1 {
		t.Fatalf("coverage ratio = %v", summary.Coverage["output"])
	}
	empty, err := s.Summary(listFilter{SessionID: "none"})
	if err != nil {
		t.Fatalf("empty summary: %v", err)
	}
	if empty.Input != nil || empty.Output != nil || empty.Requests != 0 {
		t.Fatalf("empty summary = %+v", empty)
	}
	if empty.Estimate.Kind != "UNKNOWN" {
		t.Fatalf("empty estimate = %+v", empty.Estimate)
	}
}

func TestUnlinkedWireRecordsAreNeitherGuessedNorSummed(t *testing.T) {
	s, _ := newTestService(t)
	unlinked := request("r-wire", "2026-11-10T00:00:00Z")
	unlinked.AttemptID = ""
	unlinked.SessionID = ""
	unlinked.AgentID = ""
	unlinked.Raw = raw(t, map[string]any{"input_tokens": float64(50), "output_tokens": float64(5)})
	result, err := s.Ingest([]EventInput{unlinked})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if result.Unresolved != 1 || result.Added != 0 {
		t.Fatalf("result = %+v", result)
	}
	items, total, err := s.ListUnlinked(0, 50)
	if err != nil {
		t.Fatalf("unlinked: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("unlinked = %+v", items)
	}
	if items[0].Reason == "" {
		t.Fatal("unlinked record has no reason")
	}
	summary, err := s.Summary(listFilter{})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Input != nil {
		t.Fatalf("unlinked record leaked into totals: %v", summary.Input)
	}
}

func TestAttributionFiltersRemainDistinct(t *testing.T) {
	s, _ := newTestService(t)
	base := request("r1", "2026-11-10T00:00:00Z")
	base.Raw = raw(t, map[string]any{"input_tokens": float64(100), "output_tokens": float64(20), "cache_read_input_tokens": float64(50), "cache_creation_input_tokens": float64(10)})
	worker := base
	worker.RequestID = "r2"
	worker.AgentID = "worker1"
	worker.Role = "worker"
	reviewer := base
	reviewer.RequestID = "r3"
	reviewer.AgentID = "reviewer1"
	reviewer.Role = "reviewer"
	compaction := base
	compaction.RequestID = "r4"
	compaction.RequestKind = "compaction"
	compaction.AgentID = "main"
	if _, err := s.Ingest([]EventInput{base, worker, reviewer, compaction}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	workerSummary, err := s.Summary(listFilter{Role: "worker"})
	if err != nil {
		t.Fatalf("worker summary: %v", err)
	}
	if workerSummary.Output == nil || *workerSummary.Output != 20 || workerSummary.Requests != 1 {
		t.Fatalf("worker summary = %+v", workerSummary)
	}
	compactionSummary, err := s.Summary(listFilter{RequestKind: "compaction"})
	if err != nil {
		t.Fatalf("compaction summary: %v", err)
	}
	if compactionSummary.Input == nil || *compactionSummary.Input != 160 {
		t.Fatalf("compaction input = %v", compactionSummary.Input)
	}
	list, total, err := s.ListEvents(listFilter{SessionID: "s1", ProjectID: "p1"}, 0, 100)
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if total != 4 || len(list) != 4 {
		t.Fatalf("filtered events = %d", len(list))
	}
}

func TestFourLedgersStaySeparate(t *testing.T) {
	s, _ := newTestService(t)
	priced := request("r-priced", "2026-11-10T00:00:00Z")
	priced.Raw = raw(t, map[string]any{"input_tokens": float64(100), "output_tokens": float64(20), "cache_read_input_tokens": float64(50), "cache_creation_input_tokens": float64(10)})
	priced.Price = &Price{Input: 1, Output: 3, Read: numberPtr(0.1), Write: numberPtr(1.2), Currency: "USD", Source: "fixture", FetchedAt: stringPtr("2026-11-10T00:00:00Z"), Unit: priceUnit}
	unpriced := priced
	unpriced.RequestID = "r-unpriced"
	unpriced.Price = nil
	unpriced.ProviderID = "other"
	unpriced.Model = "other-model"
	if _, err := s.Ingest([]EventInput{priced, unpriced}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if _, err := s.RecordActualBilling(ActualBillingInput{ObservationID: "bill-1", ProviderID: "p", Amount: 0.9, Currency: "usd", Source: "official", ObservedAt: "2026-11-10T12:00:00Z", Note: "invoice fixture"}); err != nil {
		t.Fatalf("actual billing: %v", err)
	}
	if _, err := s.RecordQuota(QuotaInput{Ledger: "SUBSCRIPTION", ProviderID: "kimi", Scope: "plan", Remaining: nil, Source: "official", ObservedAt: "2026-11-10T12:00:00Z", Note: "remaining unknown"}); err != nil {
		t.Fatalf("quota: %v", err)
	}
	summary, err := s.Summary(listFilter{})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Estimate.Kind != "ESTIMATE" || summary.Estimate.Amount == nil {
		t.Fatalf("estimate = %+v", summary.Estimate)
	}
	if expected := 0.000177; !almostEqual(*summary.Estimate.Amount, expected) {
		t.Fatalf("estimate amount = %v, want %v", *summary.Estimate.Amount, expected)
	}
	if summary.Estimate.Currency == nil || *summary.Estimate.Currency != "USD" || len(summary.Estimate.PriceSources) != 1 {
		t.Fatalf("estimate provenance = %+v", summary.Estimate)
	}
	if !summary.Estimate.PartialKnown || summary.Estimate.UnpricedEvents != 1 {
		t.Fatalf("estimate coverage = %+v", summary.Estimate)
	}
	if summary.Actual.Kind != "PROVIDER_LEDGER" || summary.Actual.Amount == nil || *summary.Actual.Amount != 0.9 {
		t.Fatalf("actual = %+v", summary.Actual)
	}
	if summary.Actual.Attribution != "PROVIDER_LEVEL" || summary.Actual.ProviderLedger == nil {
		t.Fatalf("actual attribution = %+v", summary.Actual)
	}
	if summary.Subscription.Kind != "SUBSCRIPTION" || summary.Subscription.Remaining != nil {
		t.Fatalf("subscription = %+v", summary.Subscription)
	}
	if summary.Input == nil || *summary.Input != 320 {
		t.Fatalf("token input = %v, want 320", summary.Input)
	}
	quotas, err := s.Quotas()
	if err != nil {
		t.Fatalf("quotas: %v", err)
	}
	if len(quotas) != 1 || quotas[0].Remaining != nil {
		t.Fatalf("quotas = %+v", quotas)
	}
}

func almostEqual(a, b float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-12
}

func TestEstimateUnknownWithoutPrice(t *testing.T) {
	s, _ := newTestService(t)
	event := request("r1", "2026-11-10T00:00:00Z")
	event.Raw = raw(t, map[string]any{"input_tokens": float64(100), "output_tokens": float64(20), "cache_read_input_tokens": float64(0), "cache_creation_input_tokens": float64(0)})
	if _, err := s.Ingest([]EventInput{event}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	summary, err := s.Summary(listFilter{})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Estimate.Kind != "UNKNOWN" || summary.Estimate.Amount != nil {
		t.Fatalf("estimate = %+v, want UNKNOWN/null (never free)", summary.Estimate)
	}
	if summary.Actual.Kind != "UNKNOWN" || summary.Actual.Amount != nil {
		t.Fatalf("actual = %+v, want UNKNOWN", summary.Actual)
	}
}

func TestDemoIsIdempotentAndKeepsUnknowns(t *testing.T) {
	s, _ := newTestService(t)
	first, err := s.Demo()
	if err != nil {
		t.Fatalf("demo: %v", err)
	}
	added, _ := first["added"].(int)
	if added == 0 {
		t.Fatalf("first demo added nothing: %+v", first)
	}
	if snapshots, _ := first["snapshots"].(int); snapshots != 1 {
		t.Fatalf("demo snapshots = %v", snapshots)
	}
	if unresolved, _ := first["unresolved"].(int); unresolved != 1 {
		t.Fatalf("demo unresolved = %v", unresolved)
	}
	second, err := s.Demo()
	if err != nil {
		t.Fatalf("demo again: %v", err)
	}
	if added, _ := second["added"].(int); added != 0 {
		t.Fatalf("second demo added %d events", added)
	}
	before, _ := first["eventsAfter"].(int)
	after, _ := second["eventsAfter"].(int)
	if before != after {
		t.Fatalf("demo changed event count %d -> %d", before, after)
	}
	summary, err := s.Summary(listFilter{})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Requests == 0 || summary.UnknownRequests == 0 {
		t.Fatalf("demo summary = %+v", summary)
	}
	if summary.Coverage["output"] == 1 {
		t.Fatalf("demo claims complete output coverage: %+v", summary.PartialKnown.Output)
	}
}

func TestRestartKeepsEventsProvenanceAndPrices(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "restart.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	first, err := New(db)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	event := request("r1", "2026-11-10T00:00:00Z")
	event.Raw = raw(t, map[string]any{"input_tokens": float64(100), "output_tokens": float64(20), "cache_read_input_tokens": float64(50), "cache_creation_input_tokens": float64(10)})
	event.Price = &Price{Input: 1, Output: 3, Read: numberPtr(0.1), Write: numberPtr(1.2), Currency: "USD", Source: "fixture", FetchedAt: stringPtr("2026-11-10T00:00:00Z"), Unit: priceUnit}
	if _, err := first.Ingest([]EventInput{event}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	second, err := New(db)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	events, total, err := second.ListEvents(listFilter{}, 0, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || events[0].Price == nil || events[0].Price.Source != "fixture" {
		t.Fatalf("restored event = %+v", events[0])
	}
	summary, err := second.Summary(listFilter{})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Estimate.Amount == nil || !almostEqual(*summary.Estimate.Amount, 0.000177) {
		t.Fatalf("restored estimate = %+v", summary.Estimate)
	}
	duplicate, err := second.Ingest([]EventInput{event})
	if err != nil {
		t.Fatalf("re-ingest after restart: %v", err)
	}
	if duplicate.Duplicates != 1 || duplicate.Added != 0 {
		t.Fatalf("restart dedup = %+v", duplicate)
	}
}

func TestInvalidEventsRejectedAtomically(t *testing.T) {
	s, _ := newTestService(t)
	good := request("r1", "2026-11-10T00:00:00Z")
	bad := good
	bad.RequestID = ""
	if _, err := s.Ingest([]EventInput{good, bad}); err == nil {
		t.Fatal("expected invalid batch rejection")
	}
	_, total, err := s.ListEvents(listFilter{}, 0, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 0 {
		t.Fatalf("invalid batch wrote %d events", total)
	}
	badTime := good
	badTime.RequestID = "r2"
	badTime.At = "2026-11-10 00:00"
	if _, err := s.Ingest([]EventInput{badTime}); err == nil {
		t.Fatal("expected invalid time rejection")
	}
}
