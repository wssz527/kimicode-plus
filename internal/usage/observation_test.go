package usage

import (
	"encoding/json"
	"math"
	"testing"
)

func TestObservationLifecycleOrderingConflictsAndRetry(t *testing.T) {
	s, db := newTestService(t)
	event := request("life", "2026-11-11T00:00:00Z")
	event.Source = "gateway"
	event.ObservationState = "PARTIAL"
	event.Sequence = ptr(1)
	event.Counts = &Counts{Input: ptr(100)}
	ingest := func(e EventInput) IngestResult {
		t.Helper()
		out, err := s.Ingest([]EventInput{e})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	ingest(event)
	event.Sequence = ptr(2)
	event.ObservationState = "FINAL"
	event.Counts = &Counts{Input: ptr(100), Output: ptr(20)}
	if r := ingest(event); r.Updated != 1 {
		t.Fatalf("not completed %+v", r)
	}
	if r := ingest(event); r.Duplicates != 1 {
		t.Fatal("terminal not idempotent")
	}
	stale := event
	stale.Sequence = ptr(1)
	stale.ObservationState = "PARTIAL"
	stale.Counts = &Counts{Input: ptr(80)}
	ingest(stale)
	partial := event
	partial.Source = "official"
	partial.Sequence = ptr(3)
	partial.ObservationState = "PARTIAL"
	partial.Counts = &Counts{Input: ptr(120)}
	if r := ingest(partial); r.Conflicts != 1 || r.Updated != 0 {
		t.Fatalf("high-authority partial erased data %+v", r)
	}
	var output int64
	var source string
	if err := db.QueryRow(`SELECT output,source FROM usage_events WHERE request_id='life'`).Scan(&output, &source); err != nil || output != 20 || source != "gateway" {
		t.Fatalf("canonical damaged %d %s %v", output, source, err)
	}
	correction := event
	correction.Sequence = ptr(4)
	correction.Counts = &Counts{Input: ptr(100), Output: ptr(18)}
	if r := ingest(correction); r.Conflicts != 1 {
		t.Fatal("changed final needs explicit correction")
	}
	correction.Sequence = ptr(5)
	correction.ObservationState = "CORRECTION"
	if r := ingest(correction); r.Updated != 1 {
		t.Fatal("correction not accepted")
	}
	retry := event
	retry.AttemptID = "a2"
	ingest(retry)
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM usage_events`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("retry identity lost %d %v", n, err)
	}
	var observations int
	if err := db.QueryRow(`SELECT count(*) FROM usage_observations`).Scan(&observations); err != nil || observations < 6 {
		t.Fatalf("observations lost %d %v", observations, err)
	}
}

func TestNormalizeOverflowStaysUnknown(t *testing.T) {
	counts := Normalize("anthropic", map[string]any{"input_tokens": json.Number("9223372036854775807"), "cache_read_input_tokens": json.Number("1"), "cache_creation_input_tokens": json.Number("1")})
	if counts.Input != nil {
		t.Fatalf("overflow became %d", *counts.Input)
	}
	if sumCounts(ptr(math.MaxInt64), ptr(1)) != nil {
		t.Fatal("integer wrap accepted")
	}
}
