package registry

import (
	"context"
	"testing"
)

func TestProviderCreateAdvancesRevisionAndCanRollback(t *testing.T) {
	s, db := newTestService(t)
	ctx := context.Background()
	before := revision(t, s)
	p, err := s.CreateProvider(ctx, providerInput{ID: "created", Name: "Created", Protocol: "openai", BaseURL: "https://example.com/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := revision(t, s); got != before+1 {
		t.Fatalf("creation revision %d, want %d", got, before+1)
	}
	changes, total, err := s.ListChanges(ctx, 0, 100)
	if err != nil || total != 1 {
		t.Fatalf("creation history %d: %v", total, err)
	}
	detail, err := s.ChangeDetail(ctx, changes[0].ID)
	if err != nil || len(detail.Providers) != 1 || detail.Providers[0].After.ID != p.ID {
		t.Fatalf("creation snapshot %+v: %v", detail, err)
	}
	if _, err = s.Rollback(ctx, changes[0].ID, revision(t, s)); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := s.loadProvider(db, p.ID); err != nil || exists {
		t.Fatal("creation rollback did not remove provider", err)
	}
}

func TestProviderNoopUpdateKeepsRevisionAndHistory(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	input := providerInput{ID: "unchanged", Name: "Unchanged", Protocol: "openai", BaseURL: "https://example.com/v1"}
	if _, err := s.CreateProvider(ctx, input); err != nil {
		t.Fatal(err)
	}
	before := revision(t, s)
	_, count, err := s.ListChanges(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.UpdateProvider(ctx, input.ID, input, before)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != before || result.ChangeID != 0 || result.Snapshot.Fields == nil || len(result.Snapshot.Fields) != 0 {
		t.Fatalf("no-op recorded change: %+v", result)
	}
	if got := revision(t, s); got != before {
		t.Fatalf("no-op revision %d, want %d", got, before)
	}
	if _, after, err := s.ListChanges(ctx, 0, 100); err != nil || after != count {
		t.Fatal("no-op changed history", after, err)
	}
	if _, err = s.CreateProvider(ctx, input); err == nil {
		t.Fatal("duplicate provider accepted")
	}
	if got := revision(t, s); got != before {
		t.Fatal("rejected create advanced revision", got)
	}
}

func TestProviderCreateRollbackPreservesLaterModels(t *testing.T) {
	s, db := newTestService(t)
	ctx := context.Background()
	if _, err := s.CreateProvider(ctx, providerInput{ID: "referenced", Name: "Referenced", Protocol: "openai", BaseURL: "https://example.com/v1"}); err != nil {
		t.Fatal(err)
	}
	changes, _, err := s.ListChanges(ctx, 0, 100)
	if err != nil || len(changes) != 1 {
		t.Fatalf("missing creation history: %+v %v", changes, err)
	}
	if _, err = db.Exec(`INSERT INTO registry_models(provider_id,model_id,display_name) VALUES('referenced','later','Later')`); err != nil {
		t.Fatal(err)
	}
	before := revision(t, s)
	_, err = s.Rollback(ctx, changes[0].ID, before)
	if _, code, _ := errorResponse(err); code != "ROLLBACK_CONFLICT" {
		t.Fatal("creation rollback did not protect later model", err)
	}
	if got := revision(t, s); got != before {
		t.Fatal("rejected rollback advanced revision", got)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM registry_models WHERE provider_id='referenced'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("rollback removed later model", err)
	}
}
