package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRejectInsecureDataDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ACL requires native acceptance")
	}
	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if instance, err := Acquire(dir); err == nil {
		instance.Close()
		t.Fatal("insecure directory accepted")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	instance, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	instance.Close()
}

func TestDataDirectoryHasOneOwner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	a, e := Acquire(dir)
	if e != nil {
		t.Fatal(e)
	}
	if b, e := Acquire(dir); e == nil {
		b.Close()
		t.Fatal("second owner acquired lock")
	}
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	b, e := Acquire(dir)
	if e != nil {
		t.Fatal(e)
	}
	b.Close()
}

func TestRejectSymlinkAndFileDataDirectory(t *testing.T) {
	base := t.TempDir()
	filePath := filepath.Join(base, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if inst, err := Acquire(filePath); err == nil {
		inst.Close()
		t.Fatal("file path accepted as data directory")
	}
	if runtime.GOOS != "windows" {
		target := filepath.Join(base, "real")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(base, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if inst, err := Acquire(link); err == nil {
			inst.Close()
			t.Fatal("symlink accepted as data directory")
		}
	}
}

func TestAtomicPrivateFileWritesAndReplacesPrivately(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "runtime.json")
	if err := AtomicPrivateFile(target, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := AtomicPrivateFile(target, []byte(`{"v":2}`)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"v":2}` {
		t.Fatalf("unexpected file content: %q", string(data))
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0077 != 0 {
			t.Fatalf("file permissions not private: %o", info.Mode().Perm())
		}
	}
}

func TestBuildWindowsACLArgs(t *testing.T) {
	args, err := buildWindowsACLArgs(`C:\Users\Alice\AppData\Local\EnhancedKimiDesktop`, "S-1-5-21-12345-67890-1001")
	if err != nil {
		t.Fatalf("valid Windows ACL rejected: %v", err)
	}
	if len(args) != 4 || args[1] != "/inheritance:r" || args[2] != "/grant:r" || args[3] != "*S-1-5-21-12345-67890-1001:(OI)(CI)F" {
		t.Fatalf("unexpected icacls args: %#v", args)
	}
	for name, tc := range map[string]struct{ dir, sid string }{
		"relative path": {dir: `relative\dir`, sid: "S-1-5-21-1001"},
		"empty sid":     {dir: `C:\Users\Alice\EKD`, sid: ""},
		"bad sid":       {dir: `C:\Users\Alice\EKD`, sid: "Everyone"},
		"injected sid":  {dir: `C:\Users\Alice\EKD`, sid: "S-1-5-21-1001 & whoami"},
	} {
		if _, err := buildWindowsACLArgs(tc.dir, tc.sid); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}
