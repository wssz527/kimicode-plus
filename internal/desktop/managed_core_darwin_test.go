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
