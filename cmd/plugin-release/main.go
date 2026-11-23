package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"enhancedkimi/internal/plugins"
	bundledPlugins "enhancedkimi/plugins"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = runKeygen(os.Args[2:])
	case "package":
		err = runPackage(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "plugin-release: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: plugin-release <keygen|package|verify> [flags]")
}

func runKeygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	outPath := fs.String("out", "", "Path to write base64-encoded Ed25519 private seed (0600)")
	keyID := fs.String("key-id", plugins.DefaultOfficialKeyID, "Key identifier")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*outPath) == "" {
		return errors.New("--out is required so the private key is never printed to stdout")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	seedEncoded := base64.StdEncoding.EncodeToString(priv.Seed()) + "\n"
	if err := os.MkdirAll(filepath.Dir(*outPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(*outPath, []byte(seedEncoded), 0o600); err != nil {
		return err
	}
	pubEncoded := base64.StdEncoding.EncodeToString(pub)
	fmt.Printf("keyId=%s\npublicKey=%s\nprivateKeyFile=%s\n", strings.TrimSpace(*keyID), pubEncoded, *outPath)
	return nil
}

func runPackage(args []string) error {
	fs := flag.NewFlagSet("package", flag.ContinueOnError)
	outDir := fs.String("out", "dist/plugins-release", "Output directory for signed manifest and plugin zip archives")
	keyID := fs.String("key-id", plugins.DefaultOfficialKeyID, "Signing key identifier")
	keyEnv := fs.String("key-env", "PLUGIN_SIGNING_PRIVATE_KEY", "Environment variable holding base64 Ed25519 private seed")
	keyFile := fs.String("key-file", "", "Optional file holding base64 Ed25519 private seed (overrides --key-env)")
	minCore := fs.String("min-core", "", "Minimum KimiCode+ core version required by these plugin packages")
	notes := fs.String("notes", "", "Release notes attached to catalog entries")
	publishedAt := fs.String("published-at", "", "RFC3339 timestamp for publishedAt/generatedAt (defaults to current UTC time)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	privKey, err := loadPrivateKey(*keyFile, *keyEnv)
	if err != nil {
		return err
	}
	stamp := strings.TrimSpace(*publishedAt)
	if stamp == "" {
		stamp = time.Now().UTC().Format(time.RFC3339)
	}
	bundled, err := bundledPlugins.BundledPlugins()
	if err != nil {
		return fmt.Errorf("load bundled plugins: %w", err)
	}
	if err := packagePlugins(bundled, *outDir, strings.TrimSpace(*keyID), privKey, strings.TrimSpace(*minCore), strings.TrimSpace(*notes), stamp); err != nil {
		return err
	}
	fmt.Printf("Packaged %d official plugins into %s\n", len(bundled), *outDir)
	return nil
}

func loadPrivateKey(keyFile, keyEnv string) (ed25519.PrivateKey, error) {
	var raw string
	if strings.TrimSpace(keyFile) != "" {
		data, err := os.ReadFile(strings.TrimSpace(keyFile))
		if err != nil {
			return nil, fmt.Errorf("read private key file: %w", err)
		}
		raw = string(data)
	} else {
		raw = os.Getenv(strings.TrimSpace(keyEnv))
		if strings.TrimSpace(raw) == "" {
			return nil, fmt.Errorf("signing key missing: set %s or pass --key-file", keyEnv)
		}
	}
	return plugins.ParseOfficialPrivateKey(raw)
}

func packagePlugins(
	bundled map[string]map[string][]byte,
	outDir string,
	keyID string,
	privKey ed25519.PrivateKey,
	minCore string,
	notes string,
	publishedAt string,
) error {
	if len(bundled) == 0 {
		return errors.New("no bundled plugins found")
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return err
	}
	ids := make([]string, 0, len(bundled))
	for id := range bundled {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	entries := make([]plugins.OfficialPluginEntry, 0, len(ids))
	for _, id := range ids {
		files := bundled[id]
		rawManifest, ok := files["plugin.json"]
		if !ok {
			return fmt.Errorf("plugin %s missing plugin.json", id)
		}
		manifest, err := plugins.ParseManifest(rawManifest)
		if err != nil {
			return fmt.Errorf("plugin %s invalid manifest: %w", id, err)
		}
		if manifest.ID != id {
			return fmt.Errorf("plugin %s manifest id mismatch: %s", id, manifest.ID)
		}
		archiveName := fmt.Sprintf("%s-%s.zip", manifest.ID, manifest.Version)
		archiveBytes, err := buildDeterministicPluginZip(files)
		if err != nil {
			return fmt.Errorf("zip plugin %s: %w", id, err)
		}
		archivePath := filepath.Join(outDir, archiveName)
		if err := os.WriteFile(archivePath, archiveBytes, 0o644); err != nil {
			return err
		}
		archiveSum := sha256.Sum256(archiveBytes)
		contentDigest := plugins.ContentDigest(files)
		entries = append(entries, plugins.OfficialPluginEntry{
			ID:             manifest.ID,
			Name:           manifest.Name,
			Version:        manifest.Version,
			APIVersion:     manifest.APIVersion,
			Description:    manifest.Description,
			HostVersions:   manifest.HostVersions,
			MinCoreVersion: minCore,
			Capabilities:   manifest.CapabilityNames(),
			Archive:        archiveName,
			ArchiveBytes:   int64(len(archiveBytes)),
			ArchiveSHA256:  hex.EncodeToString(archiveSum[:]),
			ContentDigest:  contentDigest,
			PublishedAt:    publishedAt,
			ReleaseNotes:   notes,
		})
	}

	catalog := plugins.OfficialPluginCatalog{
		GeneratedAt: publishedAt,
		Plugins:     entries,
	}
	signedBytes, err := plugins.SignPluginCatalog(catalog, keyID, privKey)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(outDir, plugins.PluginManifestAssetName)
	return os.WriteFile(manifestPath, signedBytes, 0o644)
}

func buildDeterministicPluginZip(files map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fixedTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, name := range names {
		header := &zip.FileHeader{
			Name:     name,
			Method:   zip.Deflate,
			Modified: fixedTime,
		}
		header.SetMode(0o644)
		w, err := zw.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(files[name]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	dir := fs.String("dir", "dist/plugins-release", "Directory containing plugins-manifest.json and plugin zip archives")
	if err := fs.Parse(args); err != nil {
		return err
	}
	keys := bundledPlugins.OfficialPublicKeys()
	if err := verifyReleaseDir(*dir, keys); err != nil {
		return err
	}
	fmt.Printf("Verified signed plugin release in %s\n", *dir)
	return nil
}

func verifyReleaseDir(dir string, keys map[string]ed25519.PublicKey) error {
	raw, err := os.ReadFile(filepath.Join(dir, plugins.PluginManifestAssetName))
	if err != nil {
		return fmt.Errorf("read %s: %w", plugins.PluginManifestAssetName, err)
	}
	catalog, err := plugins.VerifySignedPluginManifest(raw, keys)
	if err != nil {
		return err
	}
	if len(catalog.Plugins) == 0 {
		return errors.New("signed catalog contains no plugins")
	}
	for _, entry := range catalog.Plugins {
		archivePath := filepath.Join(dir, entry.Archive)
		data, err := os.ReadFile(archivePath)
		if err != nil {
			return fmt.Errorf("missing archive %s: %w", entry.Archive, err)
		}
		if int64(len(data)) != entry.ArchiveBytes {
			return fmt.Errorf("archive %s byte size mismatch: got %d want %d", entry.Archive, len(data), entry.ArchiveBytes)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != entry.ArchiveSHA256 {
			return fmt.Errorf("archive %s sha256 mismatch", entry.Archive)
		}
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return fmt.Errorf("open zip %s: %w", entry.Archive, err)
		}
		unpacked := make(map[string][]byte, len(zr.File))
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			var b bytes.Buffer
			_, copyErr := b.ReadFrom(rc)
			rc.Close()
			if copyErr != nil {
				return copyErr
			}
			unpacked[f.Name] = b.Bytes()
		}
		if actual := plugins.ContentDigest(unpacked); actual != entry.ContentDigest {
			return fmt.Errorf("archive %s contentDigest mismatch: got %s want %s", entry.Archive, actual, entry.ContentDigest)
		}
		manifest, err := plugins.ParseManifest(unpacked["plugin.json"])
		if err != nil {
			return fmt.Errorf("archive %s invalid plugin.json: %w", entry.Archive, err)
		}
		if manifest.ID != entry.ID || manifest.Version != entry.Version {
			return fmt.Errorf("archive %s manifest metadata mismatch", entry.Archive)
		}
	}
	return nil
}
