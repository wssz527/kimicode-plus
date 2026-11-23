package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"enhancedkimi/internal/plugins"
	bundledPlugins "enhancedkimi/plugins"
)

func TestPackageAndVerifyBundledPlugins(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	bundled, err := bundledPlugins.BundledPlugins()
	if err != nil {
		t.Fatalf("bundled plugins: %v", err)
	}
	outDir := t.TempDir()
	if err := packagePlugins(bundled, outDir, plugins.DefaultOfficialKeyID, priv, "0.2.8", "Fix KCT style", "2026-11-23T00:00:00Z"); err != nil {
		t.Fatalf("packagePlugins: %v", err)
	}
	keys := map[string]ed25519.PublicKey{plugins.DefaultOfficialKeyID: pub}
	if err := verifyReleaseDir(outDir, keys); err != nil {
		t.Fatalf("verifyReleaseDir: %v", err)
	}

	// Tampering any generated zip must fail verifyReleaseDir.
	kctZip := filepath.Join(outDir, "kimicode-plus.kct-1.0.5.zip")
	data, err := os.ReadFile(kctZip)
	if err != nil {
		t.Fatalf("read kct zip: %v", err)
	}
	data[len(data)-1] ^= 0xff
	if err := os.WriteFile(kctZip, data, 0o644); err != nil {
		t.Fatalf("corrupt kct zip: %v", err)
	}
	if err := verifyReleaseDir(outDir, keys); err == nil {
		t.Fatal("expected verifyReleaseDir to reject corrupted zip")
	}
}

func TestPackagePluginsPrefersPerPluginMinCoreVersion(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	manifest := func(id, version, minCore string) []byte {
		value := `{"id":"` + id + `","name":"` + id + `","version":"` + version + `","apiVersion":1,"hostVersions":["1.*"],"entry":{"renderer":"renderer.js"},"capabilities":[]`
		if minCore != "" {
			value += `,"minCoreVersion":"` + minCore + `"`
		}
		return []byte(value + `}`)
	}
	bundled := map[string]map[string][]byte{
		"kimicode-plus.alpha": {
			"plugin.json": manifest("kimicode-plus.alpha", "1.0.0", "0.3.0"),
			"renderer.js": []byte("export function activate() {}\n"),
		},
		"kimicode-plus.beta": {
			"plugin.json": manifest("kimicode-plus.beta", "1.0.0", ""),
			"renderer.js": []byte("export function activate() {}\n"),
		},
	}
	outDir := t.TempDir()
	if err := packagePlugins(bundled, outDir, plugins.DefaultOfficialKeyID, priv, "9.9.9", "", "2026-11-23T00:00:00Z"); err != nil {
		t.Fatalf("packagePlugins: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, plugins.PluginManifestAssetName))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	catalog, err := plugins.VerifySignedPluginManifest(raw, map[string]ed25519.PublicKey{plugins.DefaultOfficialKeyID: pub})
	if err != nil {
		t.Fatalf("verify manifest: %v", err)
	}
	got := map[string]string{}
	for _, entry := range catalog.Plugins {
		got[entry.ID] = entry.MinCoreVersion
	}
	if got["kimicode-plus.alpha"] != "0.3.0" {
		t.Fatalf("per-plugin minCoreVersion was ignored: %#v", got)
	}
	if got["kimicode-plus.beta"] != "9.9.9" {
		t.Fatalf("--min-core fallback was not applied: %#v", got)
	}
}
