package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	bundle "enhancedkimi/plugins"
)

// The bundled set is what a fresh install receives, so every entry has to pass
// the same parser an install does. A manifest that only fails at first launch
// would ship a plugin the page cannot enable.
func TestBundledPluginsPassManifestValidation(t *testing.T) {
	set, err := bundle.BundledPlugins()
	if err != nil {
		t.Fatalf("bundled plugins: %v", err)
	}
	if len(set) == 0 {
		t.Fatal("bundled set is empty")
	}
	for id, files := range set {
		raw, ok := files["plugin.json"]
		if !ok {
			t.Fatalf("%s has no plugin.json", id)
		}
		manifest, err := ParseManifest(raw)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if manifest.ID != id {
			t.Fatalf("bundle key %s does not match manifest id %s", id, manifest.ID)
		}
		// ValidateFiles reads the same bytes a release writes, so an entry that
		// points outside the directory or at a missing module fails here.
		dir := t.TempDir()
		for name, data := range files {
			target := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatalf("%s: %v", id, err)
			}
			if err := os.WriteFile(target, data, 0o600); err != nil {
				t.Fatalf("%s: %v", id, err)
			}
		}
		if err := manifest.ValidateFiles(dir); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
}

func TestKCTAdapterManifest(t *testing.T) {
	set, err := bundle.BundledPlugins()
	if err != nil {
		t.Fatalf("bundled plugins: %v", err)
	}
	files, ok := set["kimicode-plus.kct"]
	if !ok {
		t.Fatal("the KCT adapter is not bundled")
	}
	var header struct {
		Capabilities []string `json:"capabilities"`
		License      string   `json:"license"`
		Upstream     struct {
			Repo string `json:"repo"`
		} `json:"upstream"`
	}
	if err := json.Unmarshal(files["plugin.json"], &header); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	grants, err := ParseCapabilities(header.Capabilities)
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	for _, name := range []string{CapUIThinking, CapUISettings, CapStorage} {
		if !grants.Has(name) {
			t.Fatalf("KCT does not declare %s", name)
		}
	}
	// The adapter reaches two public endpoints and a local one; a credential is
	// only useful for the local endpoint, where the core injects it.
	for _, host := range []string{"edge.microsoft.com", "translate.googleapis.com", "localhost"} {
		if !grants.AllowsHost(host) {
			t.Fatalf("KCT cannot reach %s", host)
		}
	}
	if !grants.AllowsCredential("translator") {
		t.Fatal("KCT cannot use the translator credential")
	}
	// It must not ask for anything that would let it read the vault itself.
	if grants.Has(CapConfigWrite) || grants.Has(CapSessionsSend) {
		t.Fatalf("KCT asked for an unrelated capability: %v", header.Capabilities)
	}
	if header.License == "" || header.Upstream.Repo == "" {
		t.Fatalf("KCT manifest does not attribute its upstream license")
	}
	if _, ok := files["LICENSE"]; !ok {
		t.Fatal("KCT ships without its license")
	}
}
