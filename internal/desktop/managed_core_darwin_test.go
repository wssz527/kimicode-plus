//go:build darwin

package desktop

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallManagedCoreRefreshesWhenUnloadedAndPreservesWhenLoaded(t *testing.T) {
	fake := withFakeLaunch(t)
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	srcDir := t.TempDir()
	v1 := filepath.Join(srcDir, "v1")
	v2 := filepath.Join(srcDir, "v2")
	v3 := filepath.Join(srcDir, "v3")
	for path, body := range map[string]string{v1: "v1-binary", v2: "v2-binary", v3: "v3-binary"} {
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	dest, err := installManagedCore(v1, dataDir)
	if err != nil {
		t.Fatalf("initial install: %v", err)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "v1-binary" {
		t.Fatalf("initial copy = %q, %v", got, err)
	}

	// Re-enabling after disable with a newer downloaded binary must refresh the managed copy.
	if _, err := installManagedCore(v2, dataDir); err != nil {
		t.Fatalf("refresh install: %v", err)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "v2-binary" {
		t.Fatalf("refreshed copy = %q, %v", got, err)
	}

	// While the managed Login Item is loaded, another binary must not silently overwrite it.
	fake.loadedLabels[managedCoreLabel] = true
	if _, err := installManagedCore(v3, dataDir); err != nil {
		t.Fatalf("loaded install: %v", err)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "v2-binary" {
		t.Fatalf("loaded copy was overwritten: %q, %v", got, err)
	}
}

func TestInstallManagedCoreRejectsSymlinkDestination(t *testing.T) {
	withFakeLaunch(t)
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "core")
	if err := os.WriteFile(src, []byte("core"), 0o755); err != nil {
		t.Fatal(err)
	}
	dest := managedCoreExecutable(dataDir)
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := installManagedCore(src, dataDir); err == nil {
		t.Fatal("symlink destination was accepted")
	}
}

// The first resident start runs from the managed copy, so the release documents
// must land next to it. A login item installed by an older build is completed on
// re-enable without touching the running binary.
func TestInstallManagedCoreShipsReleaseDocuments(t *testing.T) {
	fake := withFakeLaunch(t)
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	srcDir := t.TempDir()
	source := filepath.Join(srcDir, "enhanced-kimi")
	if err := os.WriteFile(source, []byte("core"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "THIRD_PARTY_NOTICES.txt"), []byte("notices"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "LICENSE"), []byte("license"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest, err := installManagedCore(source, dataDir)
	if err != nil {
		t.Fatalf("initial install: %v", err)
	}
	for name, want := range map[string]string{"THIRD_PARTY_NOTICES.txt": "notices", "LICENSE": "license"} {
		data, err := os.ReadFile(filepath.Join(filepath.Dir(dest), name))
		if err != nil || string(data) != want {
			t.Fatalf("%s = %q, %v", name, data, err)
		}
		if info, err := os.Stat(filepath.Join(filepath.Dir(dest), name)); err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("%s mode = %v, %v", name, info.Mode().Perm(), err)
		}
	}
	// An existing managed copy from an older build is completed even while loaded.
	if err := os.Remove(filepath.Join(filepath.Dir(dest), "LICENSE")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(filepath.Dir(dest), "THIRD_PARTY_NOTICES.txt")); err != nil {
		t.Fatal(err)
	}
	fake.loadedLabels[managedCoreLabel] = true
	if _, err := installManagedCore(source, dataDir); err != nil {
		t.Fatalf("completing an existing install: %v", err)
	}
	for _, name := range []string{"THIRD_PARTY_NOTICES.txt", "LICENSE"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(dest), name)); err != nil {
			t.Fatalf("%s was not restored on the existing copy: %v", name, err)
		}
	}
	if data, err := os.ReadFile(dest); err != nil || string(data) != "core" {
		t.Fatalf("binary changed while the login item was loaded: %q, %v", data, err)
	}
	// Completing an older install must not rewrite documents that already exist.
	if err := os.WriteFile(filepath.Join(filepath.Dir(dest), "LICENSE"), []byte("installed license"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := installManagedCore(source, dataDir); err != nil {
		t.Fatalf("re-running an existing install: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(filepath.Dir(dest), "LICENSE")); err != nil || string(data) != "installed license" {
		t.Fatalf("existing license was overwritten: %q, %v", data, err)
	}
}
