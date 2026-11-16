package usage

import "testing"

// Prepared only: run under the original project's declared Go toolchain.
// Before fixing, define whether partial observations are rejected or versioned/updated.
func TestReviewLaterUsageCompletionNotIgnored(t *testing.T) {
	s, db := newTestService(t)
	first := request("review-request", "2026-11-10T00:00:00Z")
	first.Protocol = "openai"
	first.Counts = &Counts{Input: ptr(100), Read: ptr(90), Fresh: ptr(10)}
	if _, err := s.Ingest([]EventInput{first}); err != nil {
		t.Fatal(err)
	}
	later := first
	later.At = "2026-11-10T00:00:01Z"
	later.Counts = &Counts{Input: ptr(100), Read: ptr(90), Fresh: ptr(10), Output: ptr(20)}
	if _, err := s.Ingest([]EventInput{later}); err != nil {
		t.Fatal(err)
	}
	var value *int64
	if err := db.QueryRow("SELECT output FROM usage_events WHERE request_id=?", "review-request").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value == nil || *value != 20 {
		t.Fatalf("same-source completion discarded: output=%v", value)
	}
}
