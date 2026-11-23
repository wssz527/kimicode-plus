package config

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"enhancedkimi/internal/storage"
)

func TestDispatchDoesNotBlockSettingsWrite(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	v.SafeMode = true
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.Dispatch(func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	putDone := make(chan error, 1)
	go func() { putDone <- s.Put(v) }()
	select {
	case err = <-putDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("settings write was blocked by an in-flight dispatch")
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	called := false
	err = s.Dispatch(func() error { called = true; return nil })
	if !errors.Is(err, ErrSafeMode) || called {
		t.Fatalf("post-safety dispatch got through: %v %v", err, called)
	}
}
