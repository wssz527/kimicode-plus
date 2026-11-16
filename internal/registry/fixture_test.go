package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func loadFixture(t *testing.T) ModelsDevCatalog {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "models-dev-sample.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var catalog ModelsDevCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return catalog
}
