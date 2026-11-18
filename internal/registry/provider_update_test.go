package registry

import (
	"context"
	"testing"
)

func importedProvider(t *testing.T, s *Service) int64 {
	t.Helper()
	catalog := ModelsDevCatalog{
		Source:      "https://models.dev/api.json",
		RetrievedAt: "2026-11-10T08:12:17Z",
		Providers: map[string]modelsDevProvider{
			"acme": {
				ID:   "acme",
				Name: "Acme",
				Models: map[string]modelsDevModel{
					"acme-large": {ID: "acme-large", Name: "Acme Large"},
					"acme-small": {ID: "acme-small", Name: "Acme Small"},
				},
			},
		},
	}
	result, err := s.ImportModelsDev(context.Background(), catalog, revision(t, s), false)
	if err != nil {
		t.Fatalf("modelsdev import: %v", err)
	}
	return result.Revision
}

func TestPlaceholderProviderCanBeConfiguredWithoutLosingModels(t *testing.T) {
	s, db := newTestService(t)
	rev := importedProvider(t, s)
	placeholder, ok, err := s.loadProvider(db, "acme")
	if err != nil || !ok {
		t.Fatalf("placeholder missing: %v", err)
	}
	if placeholder.BaseURL != "urn:unconfigured" {
		t.Fatalf("placeholder base url = %q", placeholder.BaseURL)
	}
	modelsBefore, totalBefore, err := s.ListModels(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	if totalBefore != 2 {
		t.Fatalf("models before = %d", totalBefore)
	}
	updated, err := s.UpdateProvider(context.Background(), "acme", providerInput{
		Name: "Acme Production", Protocol: "anthropic", BaseURL: "https://api.acme.example/v1", SecretRef: strPtr("keychain://acme"),
	}, rev)
	if err != nil {
		t.Fatalf("update provider: %v", err)
	}
	if updated.Provider.Name != "Acme Production" || updated.Provider.Protocol != "anthropic" || updated.Provider.BaseURL != "https://api.acme.example/v1" {
		t.Fatalf("updated provider = %+v", updated.Provider)
	}
	if updated.Provider.SecretRef == nil || *updated.Provider.SecretRef != "keychain://acme" {
		t.Fatalf("secretRef = %v", updated.Provider.SecretRef)
	}
	if updated.Revision <= rev || updated.ChangeID == 0 {
		t.Fatalf("update receipt = %+v", updated)
	}
	modelsAfter, totalAfter, err := s.ListModels(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("list models: %v", err)
	}
	if totalAfter != totalBefore {
		t.Fatalf("model count changed %d -> %d", totalBefore, totalAfter)
	}
	for index, model := range modelsAfter {
		if model.ProviderID != "acme" || model.ID != modelsBefore[index].ID {
			t.Fatalf("model association changed: %+v", model)
		}
	}
	detail, err := s.ChangeDetail(context.Background(), updated.ChangeID)
	if err != nil {
		t.Fatalf("change detail: %v", err)
	}
	if len(detail.Providers) != 1 {
		t.Fatalf("provider snapshots = %+v", detail.Providers)
	}
	snapshot := detail.Providers[0]
	if snapshot.Before.BaseURL != "urn:unconfigured" || snapshot.After.BaseURL != "https://api.acme.example/v1" {
		t.Fatalf("snapshot urls = %+v", snapshot)
	}
	if len(snapshot.Fields) != 4 {
		t.Fatalf("field diff = %+v", snapshot.Fields)
	}
}

func TestProviderUpdateRequiresCurrentRevision(t *testing.T) {
	s, _ := newTestService(t)
	if _, err := s.CreateProvider(context.Background(), providerInput{ID: "p", Name: "P", Protocol: "openai", BaseURL: "https://api.example.com/v1"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	rev := revision(t, s)
	if _, err := s.UpdateProvider(context.Background(), "p", providerInput{Name: "P2", Protocol: "openai", BaseURL: "https://api2.example.com/v1"}, rev); err != nil {
		t.Fatalf("first update: %v", err)
	}
	_, err := s.UpdateProvider(context.Background(), "p", providerInput{Name: "P3", Protocol: "openai", BaseURL: "https://api3.example.com/v1"}, rev)
	if _, code, _ := errorResponse(err); code != "STALE_REVISION" {
		t.Fatalf("stale update error = %v", err)
	}
	provider, _, err := s.loadProvider(s.db, "p")
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name != "P2" {
		t.Fatalf("stale update overwrote: %+v", provider)
	}
}

func TestProviderUpdateReusesValidationAndKeepsID(t *testing.T) {
	s, _ := newTestService(t)
	if _, err := s.CreateProvider(context.Background(), providerInput{ID: "p", Name: "P", Protocol: "openai", BaseURL: "https://api.example.com/v1"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	rev := revision(t, s)
	cases := []struct {
		name  string
		input providerInput
		code  string
	}{
		{"credentials in url", providerInput{Name: "P", Protocol: "openai", BaseURL: "https://user:pass@api.example.com/v1"}, "CREDENTIALS_IN_URL"},
		{"query token", providerInput{Name: "P", Protocol: "openai", BaseURL: "https://api.example.com/v1?token=abc"}, "CREDENTIALS_IN_URL"},
		{"bad protocol", providerInput{Name: "P", Protocol: "grpc", BaseURL: "https://api.example.com/v1"}, "INVALID_PROTOCOL"},
		{"inline secret", providerInput{Name: "P", Protocol: "openai", BaseURL: "https://api.example.com/v1", SecretRef: strPtr("sk-abcdefghijklmnopqrstuvwxyz")}, "SECRET_INLINE"},
		{"empty name", providerInput{Name: "  ", Protocol: "openai", BaseURL: "https://api.example.com/v1"}, "INVALID_NAME"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := s.UpdateProvider(context.Background(), "p", testCase.input, rev)
			if err == nil {
				t.Fatal("expected rejection")
			}
			if _, code, _ := errorResponse(err); code != testCase.code {
				t.Fatalf("code = %s, want %s (%v)", code, testCase.code, err)
			}
		})
	}
	if _, err := s.UpdateProvider(context.Background(), "p", providerInput{ID: "other", Name: "P", Protocol: "openai", BaseURL: "https://api.example.com/v1"}, rev); err == nil {
		t.Fatal("expected ID change to be rejected")
	}
	_, err := s.UpdateProvider(context.Background(), "missing", providerInput{Name: "P", Protocol: "openai", BaseURL: "https://api.example.com/v1"}, rev)
	if _, code, _ := errorResponse(err); code != "PROVIDER_NOT_FOUND" {
		t.Fatalf("missing provider = %v", err)
	}
	if got := revision(t, s); got != rev {
		t.Fatalf("rejected updates changed revision to %d", got)
	}
}

func TestProviderUpdateRollbackRestoresPreviousConfiguration(t *testing.T) {
	s, _ := newTestService(t)
	if _, err := s.CreateProvider(context.Background(), providerInput{ID: "p", Name: "P", Protocol: "openai", BaseURL: "https://api.example.com/v1", SecretRef: strPtr("keychain://p")}); err != nil {
		t.Fatalf("create: %v", err)
	}
	rev := revision(t, s)
	updated, err := s.UpdateProvider(context.Background(), "p", providerInput{Name: "P2", Protocol: "anthropic", BaseURL: "https://api2.example.com/v1", SecretRef: strPtr("keychain://p2")}, rev)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	rollback, err := s.Rollback(context.Background(), updated.ChangeID, revision(t, s))
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rollback.RestoredProviders != 1 || rollback.Restored != 0 {
		t.Fatalf("rollback = %+v", rollback)
	}
	provider, _, err := s.loadProvider(s.db, "p")
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name != "P" || provider.Protocol != "openai" || provider.BaseURL != "https://api.example.com/v1" {
		t.Fatalf("restored provider = %+v", provider)
	}
	if provider.SecretRef == nil || *provider.SecretRef != "keychain://p" {
		t.Fatalf("restored secretRef = %v", provider.SecretRef)
	}
}
