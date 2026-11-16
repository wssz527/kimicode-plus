package usage

import "testing"

func TestClearDemoPreservesOtherObservations(t *testing.T) {
	s, db := newTestService(t)
	if _, err := s.Demo(); err != nil {
		t.Fatal(err)
	}
	mixed := demoEvents()[0]
	mixed.Observation = "independent-observation"
	mixed.Source = "manual"
	if _, err := s.Ingest([]EventInput{mixed}); err != nil {
		t.Fatal(err)
	}
	other := request("not-demo", "2026-11-11T00:00:00Z")
	if _, err := s.Ingest([]EventInput{other}); err != nil {
		t.Fatal(err)
	}
	out, err := s.ClearDemo()
	if err != nil || out["preserved"] != 1 || out["events"] != 5 || out["quotas"] != 2 || out["unlinked"] != 1 {
		t.Fatalf("cleanup %+v %v", out, err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM usage_events`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("lost non-demo events %d %v", count, err)
	}
	if _, err = s.ClearDemo(); err != nil {
		t.Fatal(err)
	}
}
