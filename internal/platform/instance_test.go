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
