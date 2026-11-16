package registry

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

// Problem 1: validate is a value receiver, and callers store the original
// capability back. A padded source therefore persists with whitespace and a
// non-UTC fetchedAt, which then loses the priority lookup.
func TestCapabilityNormalizationIsPersisted(t *testing.T) {
	s, _ := newTestService(t)
	result, err := s.Preview(context.Background(), []modelInput{
		input("p", "m", map[string]Capability{
			"context": {Value: float64(128000), Source: Source(" manual "), FetchedAt: strPtr("2026-11-10T10:00:00+09:00")},
			"vision":  {Value: true, Source: Source(" manual "), FetchedAt: strPtr("2026-11-10T10:00:00+09:00")},
		}),
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(result.Changes) != 1 {
		t.Fatalf("changes = %d", len(result.Changes))
	}
	after := result.Changes[0].After.Capabilities
	contextCap := after["context"]
	if contextCap.Source != SourceManual {
		t.Fatalf("source = %q, want normalized manual", contextCap.Source)
	}
	if contextCap.FetchedAt == nil || *contextCap.FetchedAt != "2026-11-10T01:00:00Z" {
		t.Fatalf("fetchedAt = %v, want UTC normalized 2026-11-10T01:00:00Z", contextCap.FetchedAt)
	}
	if after["vision"].Source != SourceManual {
		t.Fatalf("vision source = %q", after["vision"].Source)
	}

	// Padded manual must still outrank a later modelsdev refresh. Import it
	// first, so the test exercises persistence rather than only preview.
	if _, err := s.Import(context.Background(), []modelInput{
		input("p", "m", map[string]Capability{
			"context": {Value: float64(128000), Source: Source(" manual "), FetchedAt: strPtr("2026-11-10T10:00:00+09:00")},
		}),
	}, revision(t, s), "import"); err != nil {
		t.Fatalf("import padded manual: %v", err)
	}
	if _, err := s.Import(context.Background(), []modelInput{
		input("p", "m", map[string]Capability{"context": {Value: float64(4096), Source: SourceModelsDev, FetchedAt: strPtr("2027-01-01T00:00:00Z")}}),
	}, revision(t, s), "import"); err != nil {
		t.Fatalf("modelsdev import: %v", err)
	}
	model := getModel(t, s, "p", "m")
	if model.Capabilities["context"].Value != float64(128000) {
		t.Fatalf("padded manual lost priority: %+v", model.Capabilities["context"])
	}
	if model.Capabilities["context"].Source != SourceManual {
		t.Fatalf("source after reopen = %q", model.Capabilities["context"].Source)
	}
}

// Padded import must persist normalized values, including through a restart.
func TestCapabilityNormalizationSurvivesRestart(t *testing.T) {
	db := newTestDB(t)
	first, err := New(db)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, err := first.Import(context.Background(), []modelInput{
		input("p", "m", map[string]Capability{
			"context": {Value: float64(1000), Source: Source(" official "), FetchedAt: strPtr("2026-11-10T10:00:00+09:00")},
		}),
	}, 0, "import"); err != nil {
		t.Fatalf("import: %v", err)
	}
	second, err := New(db)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	model := getModel(t, second, "p", "m")
	cap := model.Capabilities["context"]
	if cap.Source != SourceOfficial {
		t.Fatalf("persisted source = %q", cap.Source)
	}
	if cap.FetchedAt == nil || *cap.FetchedAt != "2026-11-10T01:00:00Z" {
		t.Fatalf("persisted fetchedAt = %v", cap.FetchedAt)
	}
}

// A legitimate UNKNOWN observation must keep working: no provenance, and it
// must never be stored as a guess.
func TestUnknownObservationStillAccepted(t *testing.T) {
	s, _ := newTestService(t)
	if _, err := s.Import(context.Background(), []modelInput{
		input("p", "m", map[string]Capability{
			"context": {Source: SourceUnknown},
		}),
	}, 0, "import"); err != nil {
		t.Fatalf("import unknown: %v", err)
	}
	model := getModel(t, s, "p", "m")
	if cap := model.Capabilities["context"]; cap.Source != SourceUnknown || cap.Value != nil || cap.FetchedAt != nil {
		t.Fatalf("UNKNOWN changed: %+v", cap)
	}
}

// decodeModel also reads persisted snapshots; padded provenance inside a stored
// snapshot must normalize on read without rejecting the row.
func TestDecodeModelNormalizesPaddedProvenance(t *testing.T) {
	raw := []byte(`{"providerId":"p","id":"m","name":"M","capabilities":{
		"context":{"value":1000,"source":" manual ","fetchedAt":"2026-11-10T10:00:00+09:00"},
		"tools":{"value":false,"source":"official","fetchedAt":"2026-11-10T00:00:00Z"}}}`)
	record, err := decodeModel(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	cap := record.Capabilities["context"]
	if cap.Source != SourceManual || *cap.FetchedAt != "2026-11-10T01:00:00Z" {
		t.Fatalf("decoded capability = %+v", cap)
	}
	model := recordToModel(record)
	encoded, err := json.Marshal(model.Capabilities["context"])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !containsText(string(encoded), `"source":"manual"`) || !containsText(string(encoded), `"2026-11-10T01:00:00Z"`) {
		t.Fatalf("marshaled capability = %s", encoded)
	}
}

func containsText(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// Problem 3: ListModels reads page keys, releases the connection, then reloads
// them. A concurrent rollback deleting a page model must not panic.
func TestListModelsToleratesConcurrentRollback(t *testing.T) {
	s, _ := newTestService(t)
	if _, err := s.Import(context.Background(), []modelInput{
		input("p", "a", map[string]Capability{"context": capNumber(1, SourceRegistry, "2026-11-10T00:00:00Z")}),
		input("p", "b", map[string]Capability{"context": capNumber(2, SourceRegistry, "2026-11-10T00:00:00Z")}),
		input("p", "c", map[string]Capability{"context": capNumber(3, SourceRegistry, "2026-11-10T00:00:00Z")}),
	}, 0, "import"); err != nil {
		t.Fatalf("import: %v", err)
	}
	applied := mustImport(t, s, []modelInput{
		input("p", "d", map[string]Capability{"context": capNumber(4, SourceRegistry, "2026-11-10T00:00:00Z")}),
	})
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				models, _, err := s.ListModels(context.Background(), 0, 100)
				if err != nil {
					errs <- err
					return
				}
				for _, model := range models {
					if model.ID == "" {
						errs <- context.Canceled
						return
					}
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 10; j++ {
			rev, err := s.readRevision(s.db)
			if err != nil {
				errs <- err
				return
			}
			if _, err := s.Rollback(context.Background(), applied.ChangeID, rev); err != nil {
				if _, code, _ := errorResponse(err); code != "ALREADY_ROLLED_BACK" {
					errs <- err
					return
				}
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent list/rollback: %v", err)
	}
}
