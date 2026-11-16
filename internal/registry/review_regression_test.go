package registry

import (
	"context"
	"testing"
)

func TestReviewNoopImportKeepsRevision(t *testing.T) {
	s, _ := newTestService(t)
	records := []modelInput{{ProviderID: "review", ID: "m", Name: "Stable alias"}}
	first := mustImport(t, s, records)
	second, err := s.Import(context.Background(), records, first.Revision, "import")
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != first.Revision {
		t.Fatalf("no-op advanced revision: %d -> %d", first.Revision, second.Revision)
	}
}

func TestReviewIDOnlyRefreshPreservesName(t *testing.T) {
	s, _ := newTestService(t)
	mustImport(t, s, []modelInput{{ProviderID: "review", ID: "m", Name: "Stable alias"}})
	mustImport(t, s, []modelInput{{ProviderID: "review", ID: "m"}})
	if got := getModel(t, s, "review", "m").Name; got != "Stable alias" {
		t.Fatalf("ID-only refresh lost name: %q", got)
	}
}

func TestReviewRollbackRejectsLaterEdit(t *testing.T) {
	s, _ := newTestService(t)
	first := mustImport(t, s, []modelInput{{ProviderID: "review", ID: "m", Name: "Original"}})
	_, err := s.Override(context.Background(), "review", "m", "tools", true, first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Rollback(context.Background(), first.ChangeID, revision(t, s))
	if err == nil {
		t.Fatal("rollback silently deleted a subsequently edited model")
	}
	if getModel(t, s, "review", "m") == nil {
		t.Fatal("rollback damaged live model")
	}
}

func TestReviewEndpointPolicy(t *testing.T) {
	for _, raw := range []string{"http://example.com/v1", "https://example.com/v1?region=x", "https://example.com/v1#key", "https://user:pass@example.com/v1"} {
		if err := validateBaseURL(raw); err == nil {
			t.Errorf("accepted unsafe endpoint %q", raw)
		}
	}
	for _, raw := range []string{"http://127.0.0.1:11434/v1", "http://[::1]:11434/v1", "https://api.example.com/v1"} {
		if err := validateBaseURL(raw); err != nil {
			t.Errorf("rejected endpoint %q: %v", raw, err)
		}
	}
}
