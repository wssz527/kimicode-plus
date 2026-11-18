package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enhancedkimi/internal/storage"
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
	if _, ok := set["kimicode-plus.example-hello"]; ok {
		t.Fatal("example-hello must stay a template in the repo and must not be bundled")
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
	for _, host := range []string{"edge.microsoft.com", "translate.googleapis.com", "127.0.0.1", "::1"} {
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
	for _, name := range []string{"LICENSE", "NOTICE.md"} {
		if _, ok := files[name]; !ok {
			t.Fatalf("KCT ships without %s", name)
		}
	}
}

func TestExampleHelloRemovedFromBundledKeepsExistingInstallAndRefusesRestore(t *testing.T) {
	ctx := context.Background()
	bundled, err := bundle.BundledPlugins()
	if err != nil {
		t.Fatalf("bundled plugins: %v", err)
	}
	if _, ok := bundled["kimicode-plus.example-hello"]; ok {
		t.Fatal("example-hello should not be bundled")
	}

	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "plugins.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	oldExample := map[string][]byte{
		"plugin.json": []byte(`{
  "id": "kimicode-plus.example-hello",
  "name": "示例插件",
  "version": "1.0.0",
  "apiVersion": 1,
  "hostVersions": ["1.*"],
  "entry": {"renderer": "renderer.js"},
  "capabilities": ["ui:sidebar", "storage"]
}`),
		"renderer.js": []byte("export function activate() {}\n"),
		"LICENSE":     []byte("MIT\n"),
	}

	prev, err := New(db, func() bool { return false }, WithDataDir(root), WithBundled(func() (map[string]map[string][]byte, error) {
		return map[string]map[string][]byte{"kimicode-plus.example-hello": oldExample}, nil
	}))
	if err != nil {
		t.Fatalf("old manager: %v", err)
	}
	if err := prev.ReleaseBundled(ctx, map[string]map[string][]byte{"kimicode-plus.example-hello": oldExample}); err != nil {
		t.Fatalf("release old example: %v", err)
	}
	prev.Close()

	// Upgrading to the current bundled set must not uninstall the previously
	// installed example-hello plugin, while bundled plugins start disabled.
	cur, err := New(db, func() bool { return false }, WithDataDir(root), WithBundled(bundle.BundledPlugins), WithOfficial(bundle.OfficialDigests))
	if err != nil {
		t.Fatalf("upgraded manager: %v", err)
	}
	defer cur.Close()
	if err := cur.ReleaseBundled(ctx, bundled); err != nil {
		t.Fatalf("release current bundle: %v", err)
	}
	if _, ok := cur.Get("kimicode-plus.example-hello"); !ok {
		t.Fatal("upgrade should preserve previously installed example-hello")
	}
	for _, id := range []string{"kimicode-plus.monitor", "kimicode-plus.planner", "kimicode-plus.kct"} {
		item, ok := cur.Get(id)
		if !ok {
			t.Fatalf("missing bundled plugin %s", id)
		}
		if item.Enabled {
			t.Fatalf("bundled plugin %s should start disabled", id)
		}
	}

	// Once uninstalled, example-hello must not appear as restorable or be restored.
	if err := cur.Uninstall(ctx, "kimicode-plus.example-hello"); err != nil {
		t.Fatalf("uninstall example-hello: %v", err)
	}
	for _, item := range cur.List() {
		if item.ID == "kimicode-plus.example-hello" {
			t.Fatalf("uninstalled example-hello should not be restorable: %+v", item)
		}
	}
	mux := http.NewServeMux()
	cur.Register(mux)
	req := httptest.NewRequest(http.MethodPost, "/enhance/v1/plugins/kimicode-plus.example-hello/restore", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "PLUGIN_NOT_BUNDLED") {
		t.Fatalf("restore should fail with PLUGIN_NOT_BUNDLED, got %d %s", rec.Code, rec.Body.String())
	}
}
