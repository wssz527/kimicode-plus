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
