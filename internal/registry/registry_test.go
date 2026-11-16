package registry

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"enhancedkimi/internal/storage"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newTestService(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	db := newTestDB(t)
	s, err := New(db)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return s, db
}

func capNumber(value float64, source Source, at string) Capability {
	return Capability{Value: value, Source: source, FetchedAt: &at}
}

func capBool(value bool, source Source, at string) Capability {
	return Capability{Value: value, Source: source, FetchedAt: &at}
}

func input(provider, id string, caps map[string]Capability) modelInput {
	return modelInput{ProviderID: provider, ID: id, Capabilities: caps}
}

func revision(t *testing.T, s *Service) int64 {
	t.Helper()
	rev, err := s.readRevision(s.db)
	if err != nil {
		t.Fatalf("revision: %v", err)
	}
	return rev
}

func getModel(t *testing.T, s *Service, provider, id string) *Model {
	t.Helper()
	records, err := s.loadModels(s.db, []modelKey{{ProviderID: provider, ModelID: id}})
	if err != nil {
		t.Fatalf("load model: %v", err)
	}
	record, ok := records[modelKey{ProviderID: provider, ModelID: id}]
	if !ok {
		return nil
	}
	model := recordToModel(record)
	return &model
}

func mustImport(t *testing.T, s *Service, inputs []modelInput) ImportResult {
	t.Helper()
	result, err := s.Import(context.Background(), inputs, revision(t, s), "import")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	return result
}

func TestMergePriorityIndependentOfArrivalOrder(t *testing.T) {
	old := "2026-11-10T00:00:00Z"
	newer := "2026-11-09T00:00:00Z"
	records := []*modelRecord{}
	for _, cap := range []Capability{
		capNumber(100, SourceOfficial, old),
		capNumber(50, SourceModelsDev, old),
		capNumber(80, SourceManual, newer),
	} {
		rec := newModelRecord("p", "m")
		rec.Capabilities["context"] = cap
		records = append(records, rec)
	}
	forward := applyRecords(nil, records)
	reversed := []*modelRecord{records[2], records[1], records[0]}
	backward := applyRecords(nil, reversed)
	key := modelKey{ProviderID: "p", ModelID: "m"}
	if got := forward[key].Capabilities["context"].Value; got != float64(80) {
		t.Fatalf("forward merge = %v, want manual 80", got)
	}
	if got := backward[key].Capabilities["context"].Value; got != float64(80) {
		t.Fatalf("reverse merge = %v, want manual 80", got)
	}
}

func TestSameSourceUsesParsedTimeNotStringOrder(t *testing.T) {
	// Lexically "2026-11-10T09:00:00Z" sorts before "2026-11-10T10:00:00+09:00",
	// but the second instant (01:00Z) is earlier. The later real instant must win.
	earlyText := "2026-11-10T10:00:00+09:00"
	lateText := "2026-11-10T09:00:00Z"
	early := Capability{Value: float64(10), Source: SourceModelsDev, FetchedAt: &earlyText}
	late := Capability{Value: float64(20), Source: SourceModelsDev, FetchedAt: &lateText}
	if err := early.normalize("output"); err != nil {
		t.Fatalf("validate early: %v", err)
	}
	if err := late.normalize("output"); err != nil {
		t.Fatalf("validate late: %v", err)
	}
	if earlyText < lateText {
		t.Fatalf("fixture precondition failed: %q should sort before %q", earlyText, lateText)
	}
	base := newModelRecord("p", "m")
	base.Capabilities["output"] = early
	next := newModelRecord("p", "m")
	next.Capabilities["output"] = late
	merged := applyRecords(map[modelKey]*modelRecord{{ProviderID: "p", ModelID: "m"}: base}, []*modelRecord{next})
	if got := merged[modelKey{ProviderID: "p", ModelID: "m"}].Capabilities["output"].Value; got != float64(20) {
		t.Fatalf("merged value = %v, want 20 from the later instant", got)
	}
	// Applying in the opposite order must not regress the later observation.
	mergedBack := applyRecords(map[modelKey]*modelRecord{{ProviderID: "p", ModelID: "m"}: next}, []*modelRecord{next})
	if got := mergedBack[modelKey{ProviderID: "p", ModelID: "m"}].Capabilities["output"].Value; got != float64(20) {
		t.Fatalf("merged value = %v, want 20", got)
	}
	stale := newModelRecord("p", "m")
	stale.Capabilities["output"] = early
	mergedStale := applyRecords(merged, []*modelRecord{stale})
	if got := mergedStale[modelKey{ProviderID: "p", ModelID: "m"}].Capabilities["output"].Value; got != float64(20) {
		t.Fatalf("stale observation overwrote newer: %v", got)
	}
}

func TestAbsentFieldIsUnknownAndExplicitFalseSurvives(t *testing.T) {
	s, _ := newTestService(t)
	mustImport(t, s, []modelInput{input("p", "m", map[string]Capability{
		"tools": capBool(false, SourceOfficial, "2026-11-10T00:00:00Z"),
	})})
	model := getModel(t, s, "p", "m")
	if model.Capabilities["vision"].Source != SourceUnknown || model.Capabilities["vision"].Value != nil {
		t.Fatalf("absent vision = %+v, want UNKNOWN/null", model.Capabilities["vision"])
	}
	if model.Capabilities["tools"].Value != false || model.Capabilities["tools"].Source != SourceOfficial {
		t.Fatalf("tools = %+v, want official false", model.Capabilities["tools"])
	}
}

func TestManualUnknownBlocksCatalogGuess(t *testing.T) {
	s, _ := newTestService(t)
	mustImport(t, s, []modelInput{input("p", "m", map[string]Capability{
		"context": capNumber(999, SourceModelsDev, "2026-11-10T00:00:00Z"),
	})})
	result, err := s.Override(context.Background(), "p", "m", "context", nil, revision(t, s))
	if err != nil {
		t.Fatalf("override null: %v", err)
	}
	if result.After.Source != SourceManual || result.After.Value != nil {
		t.Fatalf("override after = %+v, want manual/null", result.After)
	}
	// A later catalog refresh must not fill the manual UNKNOWN.
	mustImport(t, s, []modelInput{input("p", "m", map[string]Capability{
		"context": capNumber(4096, SourceModelsDev, "2026-11-11T00:00:00Z"),
	})})
	model := getModel(t, s, "p", "m")
	contextCap := model.Capabilities["context"]
	if contextCap.Value != nil || contextCap.Source != SourceManual {
		t.Fatalf("manual UNKNOWN was overwritten: %+v", contextCap)
	}
}

func TestPreviewDoesNotWriteAndImportIsOneTransaction(t *testing.T) {
	s, db := newTestService(t)
	before := revision(t, s)
	preview, err := s.Preview(context.Background(), []modelInput{
		input("p", "m", map[string]Capability{"context": capNumber(1000, SourceRegistry, "2026-11-10T00:00:00Z")}),
		input("p", "n", map[string]Capability{"vision": capBool(true, SourceRegistry, "2026-11-10T00:00:00Z")}),
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.Changes) != 2 || preview.Revision != before {
		t.Fatalf("preview = %+v", preview)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM registry_models`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("preview wrote %d models", count)
	}
	result := mustImport(t, s, []modelInput{
		input("p", "m", map[string]Capability{"context": capNumber(1000, SourceRegistry, "2026-11-10T00:00:00Z")}),
		input("p", "n", map[string]Capability{"vision": capBool(true, SourceRegistry, "2026-11-10T00:00:00Z")}),
	})
	if result.Imported != 2 || result.Created != 2 || result.Revision <= before {
		t.Fatalf("import = %+v", result)
	}
}

func TestMidTransactionFailureRollsBackEverything(t *testing.T) {
	s, db := newTestService(t)
	mustImport(t, s, []modelInput{input("p", "keep", map[string]Capability{
		"context": capNumber(100, SourceRegistry, "2026-11-10T00:00:00Z"),
	})})
	modelsBefore, changesBefore := countRows(t, db, "registry_models"), countRows(t, db, "registry_changes")
	revBefore := revision(t, s)

	// Simulated failure after the revision bump and the first model write.
	failure := storage.Transaction(context.Background(), db, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT provider_id,model_id FROM registry_models ORDER BY provider_id,model_id`)
		if err != nil {
			return err
		}
		var keys []modelKey
		for rows.Next() {
			var key modelKey
			if err := rows.Scan(&key.ProviderID, &key.ModelID); err != nil {
				rows.Close()
				return err
			}
			keys = append(keys, key)
		}
		rows.Close()
		existing, err := s.loadModels(tx, keys)
		if err != nil {
			return err
		}
		records, err := prepareRecords([]modelInput{
			input("p", "new-one", map[string]Capability{"context": capNumber(1, SourceRegistry, "2026-11-10T00:00:00Z")}),
			input("p", "new-two", map[string]Capability{"output": capNumber(2, SourceRegistry, "2026-11-10T00:00:00Z")}),
			input("p", "keep", map[string]Capability{"context": capNumber(200, SourceManual, "2026-11-10T00:00:00Z")}),
		})
		if err != nil {
			return err
		}
		merged := applyRecords(existing, records)
		deltas, summary := computeDeltas(existing, merged, modelKeys(records))
		rev, err := s.bumpRevision(tx)
		if err != nil {
			return err
		}
		if _, err := s.writeChange(tx, "import", revBefore, rev, time.Now().UTC().Format(time.RFC3339Nano), summaryText("import", summary, ""), deltas); err != nil {
			return err
		}
		for index, delta := range deltas {
			if err := s.writeModel(tx, delta.after); err != nil {
				return err
			}
			if index == 1 {
				return context.Canceled
			}
		}
		return nil
	})
	if failure == nil {
		t.Fatal("expected the simulated transaction to fail")
	}
	if got := countRows(t, db, "registry_models"); got != modelsBefore {
		t.Fatalf("models after rollback = %d, want %d", got, modelsBefore)
	}
	if got := countRows(t, db, "registry_changes"); got != changesBefore {
		t.Fatalf("changes after rollback = %d, want %d", got, changesBefore)
	}
	if got := revision(t, s); got != revBefore {
		t.Fatalf("revision after rollback = %d, want %d", got, revBefore)
	}
	if model := getModel(t, s, "p", "new-one"); model != nil {
		t.Fatal("partially imported model survived rollback")
	}
	if got := getModel(t, s, "p", "keep").Capabilities["context"].Value; got != float64(100) {
		t.Fatalf("existing model changed: %v", got)
	}
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func TestStaleRevisionConflictsAndRollbackRefusesOverwrite(t *testing.T) {
	s, _ := newTestService(t)
	staleResult, err := s.Preview(context.Background(), []modelInput{
		input("p", "stale", map[string]Capability{"context": capNumber(1, SourceRegistry, "2026-11-10T00:00:00Z")}),
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	current, err := s.Preview(context.Background(), []modelInput{
		input("p", "current", map[string]Capability{"context": capNumber(2, SourceRegistry, "2026-11-10T00:00:00Z")}),
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	applied := mustImport(t, s, []modelInput{
		input("p", "current", map[string]Capability{"context": capNumber(2, SourceRegistry, "2026-11-10T00:00:00Z")}),
	})
	if applied.Revision != current.Revision+1 {
		t.Fatalf("apply revision = %d", applied.Revision)
	}
	_, err = s.Import(context.Background(), []modelInput{
		input("p", "stale", map[string]Capability{"context": capNumber(1, SourceRegistry, "2026-11-10T00:00:00Z")}),
	}, staleResult.Revision, "import")
	if _, code, _ := errorResponse(err); code != "STALE_REVISION" {
		t.Fatalf("stale apply error = %v", err)
	}
	if _, err := s.Import(context.Background(), []modelInput{
		input("p", "external", map[string]Capability{"context": capNumber(3, SourceRegistry, "2026-11-10T00:00:00Z")}),
	}, applied.Revision, "import"); err != nil {
		t.Fatalf("external import: %v", err)
	}
	_, err = s.Rollback(context.Background(), applied.ChangeID, applied.Revision)
	if _, code, _ := errorResponse(err); code != "STALE_REVISION" {
		t.Fatalf("stale rollback error = %v", err)
	}
	for _, id := range []string{"current", "external"} {
		if getModel(t, s, "p", id) == nil {
			t.Fatalf("model %s disappeared after refused rollback", id)
		}
	}
}

func TestRollbackRestoresSnapshotAndDeletesCreatedModels(t *testing.T) {
	s, _ := newTestService(t)
	mustImport(t, s, []modelInput{input("p", "m", map[string]Capability{
		"context": capNumber(1000, SourceRegistry, "2026-11-10T00:00:00Z"),
		"vision":  capBool(true, SourceRegistry, "2026-11-10T00:00:00Z"),
	})})
	applied := mustImport(t, s, []modelInput{
		input("p", "m", map[string]Capability{"context": capNumber(2000, SourceOfficial, "2026-11-11T00:00:00Z")}),
		input("p", "brand-new", map[string]Capability{"output": capNumber(512, SourceRegistry, "2026-11-10T00:00:00Z")}),
	})
	if applied.Updated != 1 || applied.Created != 1 {
		t.Fatalf("applied = %+v", applied)
	}
	if got := getModel(t, s, "p", "m").Capabilities["context"].Value; got != float64(2000) {
		t.Fatalf("updated context = %v", got)
	}
	rollback, err := s.Rollback(context.Background(), applied.ChangeID, revision(t, s))
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rollback.Restored != 1 || rollback.Deleted != 1 {
		t.Fatalf("rollback = %+v", rollback)
	}
	restored := getModel(t, s, "p", "m")
	if restored.Capabilities["context"].Value != float64(1000) || restored.Capabilities["vision"].Value != true {
		t.Fatalf("restored = %+v", restored.Capabilities)
	}
	if getModel(t, s, "p", "brand-new") != nil {
		t.Fatal("rollback did not delete the created model")
	}
	_, err = s.Rollback(context.Background(), applied.ChangeID, revision(t, s))
	if _, code, _ := errorResponse(err); code != "ALREADY_ROLLED_BACK" {
		t.Fatalf("second rollback error = %v", err)
	}
}

func TestProvidersRejectCredentialsInURLAndInlineSecrets(t *testing.T) {
	s, _ := newTestService(t)
	cases := []struct {
		name  string
		input providerInput
		code  string
	}{
		{"userinfo", providerInput{ID: "p1", Name: "P1", Protocol: "openai", BaseURL: "https://user:pass@api.example.com/v1"}, "CREDENTIALS_IN_URL"},
		{"api_key query", providerInput{ID: "p2", Name: "P2", Protocol: "openai", BaseURL: "https://api.example.com/v1?api_key=abc"}, "CREDENTIALS_IN_URL"},
		{"token value", providerInput{ID: "p3", Name: "P3", Protocol: "openai", BaseURL: "https://api.example.com/v1?x=sk-supersecretvalue"}, "CREDENTIALS_IN_URL"},
		{"bad protocol", providerInput{ID: "p4", Name: "P4", Protocol: "grpc", BaseURL: "https://api.example.com"}, "INVALID_PROTOCOL"},
		{"non http", providerInput{ID: "p5", Name: "P5", Protocol: "openai", BaseURL: "ftp://api.example.com"}, "INVALID_SOURCE_URL"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := s.CreateProvider(context.Background(), testCase.input)
			if err == nil {
				t.Fatal("expected rejection")
			}
			if _, code, _ := errorResponse(err); code != testCase.code {
				t.Fatalf("code = %s, want %s (%v)", code, testCase.code, err)
			}
		})
	}
	created, err := s.CreateProvider(context.Background(), providerInput{ID: "loopback", Name: "Local", Protocol: "openai", BaseURL: "http://127.0.0.1:11434/v1", SecretRef: strPtr("keychain://local/mock")})
	if err != nil {
		t.Fatalf("create loopback provider: %v", err)
	}
	if created.SecretRef == nil || *created.SecretRef != "keychain://local/mock" {
		t.Fatalf("secretRef = %v", created.SecretRef)
	}
	if _, err := s.CreateProvider(context.Background(), providerInput{ID: "loopback", Name: "Local", Protocol: "openai", BaseURL: "http://127.0.0.1:11434/v1"}); err == nil {
		t.Fatal("expected duplicate provider conflict")
	}
	providers, err := s.ListProviders(context.Background())
	if err != nil {
		t.Fatalf("list providers: %v", err)
	}
	if len(providers) != 1 || providers[0].ID != "loopback" {
		t.Fatalf("providers = %+v", providers)
	}
}

func TestDiscoveryIsLocalMockAndProvenanced(t *testing.T) {
	s, _ := newTestService(t)
	if _, err := s.CreateProvider(context.Background(), providerInput{ID: "kimi", Name: "Kimi", Protocol: "openai", BaseURL: "https://api.moonshot.cn/v1"}); err != nil {
		t.Fatalf("create provider: %v", err)
	}
	records, err := s.Discovery(context.Background(), "kimi")
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("empty mock catalog")
	}
	for _, record := range records {
		if record.ProviderID != "kimi" {
			t.Fatalf("record provider = %q", record.ProviderID)
		}
		for field, capability := range record.Capabilities {
			if capability.Source != SourceRegistry || capability.FetchedAt == nil {
				t.Fatalf("field %s provenance = %+v", field, capability)
			}
		}
	}
	if _, err := s.Discovery(context.Background(), "missing"); err == nil {
		t.Fatal("expected missing provider error")
	}
}

func TestModelsDevFixtureImportAndPriority(t *testing.T) {
	s, _ := newTestService(t)
	catalog := loadFixture(t)
	preview, err := s.ImportModelsDev(context.Background(), catalog, revision(t, s), true)
	if err != nil {
		t.Fatalf("preview modelsdev: %v", err)
	}
	if !preview.Preview || len(preview.Changes) == 0 {
		t.Fatalf("preview = %+v", preview)
	}
	if got := countRows(t, s.db, "registry_models"); got != 0 {
		t.Fatalf("preview persisted %d models", got)
	}
	imported, err := s.ImportModelsDev(context.Background(), catalog, revision(t, s), false)
	if err != nil {
		t.Fatalf("import modelsdev: %v", err)
	}
	if imported.Import == nil || imported.Import.Created == 0 {
		t.Fatalf("import = %+v", imported)
	}
	model := getModel(t, s, "anthropic", "claude-opus-5-5")
	if model == nil {
		t.Fatal("fixture model missing")
	}
	if model.Capabilities["context"].Value != float64(1000000) {
		t.Fatalf("context = %v", model.Capabilities["context"].Value)
	}
	if model.Capabilities["context"].Source != SourceModelsDev || model.Capabilities["context"].FetchedAt == nil {
		t.Fatalf("context provenance = %+v", model.Capabilities["context"])
	}
	if model.Capabilities["cacheTTL"].Source != SourceUnknown {
		t.Fatalf("cacheTTL should be UNKNOWN: %+v", model.Capabilities["cacheTTL"])
	}
	// models.dev file has video/audio inputs for google; explicit false for gemini-3-pro-image tools.
	google := getModel(t, s, "google", "gemini-3-pro-image")
	if google.Capabilities["video"].Value != false || google.Capabilities["audio"].Value != false {
		t.Fatalf("google video/audio = %+v", google.Capabilities)
	}
	if google.Capabilities["tools"].Value != false {
		t.Fatalf("google tools = %+v", google.Capabilities["tools"])
	}
	// Official refresh beats models.dev only for its own field.
	official := "2026-11-01T00:00:00Z"
	if _, err := s.Import(context.Background(), []modelInput{
		input("anthropic", "claude-opus-5-5", map[string]Capability{"context": capNumber(200000, SourceOfficial, official)}),
	}, revision(t, s), "import"); err != nil {
		t.Fatalf("official import: %v", err)
	}
	model = getModel(t, s, "anthropic", "claude-opus-5-5")
	if model.Capabilities["context"].Value != float64(200000) || model.Capabilities["context"].Source != SourceOfficial {
		t.Fatalf("official did not win: %+v", model.Capabilities["context"])
	}
	if model.Capabilities["output"].Source != SourceModelsDev {
		t.Fatalf("unrelated field changed: %+v", model.Capabilities["output"])
	}
}

func TestInvalidCapabilityAndProvenanceRejected(t *testing.T) {
	s, _ := newTestService(t)
	cases := []struct {
		name string
		caps map[string]Capability
		code string
	}{
		{"negative", map[string]Capability{"context": {Value: float64(-1), Source: SourceRegistry, FetchedAt: strPtr("2026-11-10T00:00:00Z")}}, "INVALID_CAPABILITY"},
		{"fractional", map[string]Capability{"context": {Value: 1.5, Source: SourceRegistry, FetchedAt: strPtr("2026-11-10T00:00:00Z")}}, "INVALID_CAPABILITY"},
		{"string bool", map[string]Capability{"vision": {Value: "yes", Source: SourceRegistry, FetchedAt: strPtr("2026-11-10T00:00:00Z")}}, "INVALID_CAPABILITY"},
		{"missing time", map[string]Capability{"context": {Value: float64(1), Source: SourceRegistry}}, "MISSING_PROVENANCE"},
		{"bad source", map[string]Capability{"context": {Value: float64(1), Source: "vendor-blog", FetchedAt: strPtr("2026-11-10T00:00:00Z")}}, "INVALID_SOURCE"},
		{"bad time", map[string]Capability{"context": {Value: float64(1), Source: SourceRegistry, FetchedAt: strPtr("10/11/2026")}}, "INVALID_FETCHED_AT"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := s.Preview(context.Background(), []modelInput{input("p", "m", testCase.caps)})
			if err == nil {
				t.Fatal("expected rejection")
			}
			if _, code, _ := errorResponse(err); code != testCase.code {
				t.Fatalf("code = %s, want %s (%v)", code, testCase.code, err)
			}
		})
	}
	if count := countRows(t, s.db, "registry_models"); count != 0 {
		t.Fatalf("invalid input persisted %d models", count)
	}
}

func TestRestartKeepsRevisionsSnapshotsAndProvenance(t *testing.T) {
	db := newTestDB(t)
	first, err := New(db)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	mustImport(t, first, []modelInput{input("p", "m", map[string]Capability{
		"context": capNumber(100, SourceRegistry, "2026-11-10T00:00:00Z"),
	})})
	applied := mustImport(t, first, []modelInput{input("p", "m", map[string]Capability{
		"output": capNumber(50, SourceManual, "2026-11-12T00:00:00Z"),
	})})
	revBefore := revision(t, first)

	second, err := New(db)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := revision(t, second); got != revBefore {
		t.Fatalf("revision after restart = %d, want %d", got, revBefore)
	}
	model := getModel(t, second, "p", "m")
	if model.Capabilities["context"].Value != float64(100) || model.Capabilities["output"].Value != float64(50) {
		t.Fatalf("restored capabilities = %+v", model.Capabilities)
	}
	detail, err := second.ChangeDetail(context.Background(), applied.ChangeID)
	if err != nil {
		t.Fatalf("change detail: %v", err)
	}
	if len(detail.Models) != 1 || detail.Models[0].Before == nil || detail.Models[0].After == nil {
		t.Fatalf("snapshot after restart = %+v", detail)
	}
	if detail.Models[0].Before.Capabilities["output"].Value != nil {
		t.Fatalf("snapshot before value = %+v", detail.Models[0].Before.Capabilities["output"])
	}
}
