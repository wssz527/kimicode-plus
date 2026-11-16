package registry

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestReviewSharedProviderPolicy(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/provider-policy.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		IDs, URLs []struct {
			Value string
			Valid bool
		}
	}
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures.IDs {
		if got := validateIdentity(f.Value, "m") == nil; got != f.Valid {
			t.Errorf("id %q got %v", f.Value, got)
		}
	}
	for _, f := range fixtures.URLs {
		if got := validateBaseURL(f.Value) == nil; got != f.Valid {
			t.Errorf("url %q got %v", f.Value, got)
		}
	}
}

func TestReviewSnapshotsGapAndTwoEditors(t *testing.T) {
	s, db := newTestService(t)
	ctx := context.Background()
	first := mustImport(t, s, []modelInput{{ProviderID: "review", ID: "m", Name: "Upstream"}})
	if _, err := db.Exec(`INSERT INTO registry_revisions(id) VALUES(7)`); err != nil {
		t.Fatal(err)
	}
	a, err := s.Snapshot(ctx, "review", "m")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Snapshot(ctx, "review", "m")
	if err != nil {
		t.Fatal(err)
	}
	if a.Revision != 7 || a.Model.Name != "Upstream" {
		t.Fatalf("snapshot %+v", a)
	}
	if _, err = s.Override(ctx, "review", "m", "tools", true, a.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Override(ctx, "review", "m", "tools", false, b.Revision); err == nil {
		t.Fatal("second editor overwrote first")
	}
	if _, err = s.UpdateProvider(ctx, "review", providerInput{Name: "Old", Protocol: "openai", BaseURL: "https://example.com"}, b.Revision); err == nil {
		t.Fatal("provider stale baseline accepted")
	}
	if _, err = s.Rollback(ctx, first.ChangeID, revision(t, s)); err == nil {
		t.Fatal("new revision authorized destructive rollback")
	}
}

func TestReviewAutomaticRestoreAndNames(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	first := mustImport(t, s, []modelInput{{ProviderID: "review", ID: "m", Name: "Upstream", Capabilities: map[string]Capability{"context": capNumber(100, SourceModelsDev, "2026-11-10T00:00:00Z")}}})
	if _, err := s.Override(ctx, "review", "m", "context", nil, first.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.editMetadata(ctx, "review", "m", "displayName", "My alias", false, revision(t, s)); err != nil {
		t.Fatal(err)
	}
	mustImport(t, s, []modelInput{{ProviderID: "review", ID: "m", Name: "New upstream", Capabilities: map[string]Capability{"context": capNumber(200, SourceModelsDev, "2026-11-11T00:00:00Z")}}})
	m := getModel(t, s, "review", "m")
	if m.Name != "My alias" || m.AutoName != "New upstream" || m.Capabilities["context"].Value != nil {
		t.Fatalf("manual data lost %+v", m)
	}
	if _, err := s.editMetadata(ctx, "review", "m", "context", nil, true, revision(t, s)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.editMetadata(ctx, "review", "m", "displayName", nil, true, revision(t, s)); err != nil {
		t.Fatal(err)
	}
	m = getModel(t, s, "review", "m")
	if m.Name != "New upstream" || m.Capabilities["context"].Value != float64(200) || m.Capabilities["context"].Source != SourceModelsDev {
		t.Fatalf("automatic restore failed %+v", m)
	}
}
