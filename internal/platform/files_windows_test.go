package platform

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestAtomicPrivateFileWaitsForExistingReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.json")
	if err := AtomicPrivateFile(path, []byte("old")); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan error, 1)
	time.AfterFunc(500*time.Millisecond, func() { released <- windows.CloseHandle(handle) })
	writeErr := AtomicPrivateFile(path, []byte("updated"))
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "updated" {
		t.Fatalf("replacement = %q, %v", data, err)
	}
}
