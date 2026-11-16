package config

import (
	"errors"
	"path/filepath"
	"testing"

	"enhancedkimi/internal/storage"
)

func TestDispatchAndSafetySwitchAreOrdered(t *testing.T) {
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
			if s.Safe() {
				t.Error("safe flag changed inside admitted dispatch")
			}
			return nil
		})
	}()
	<-entered
	putStarted := make(chan struct{})
	putDone := make(chan error, 1)
	go func() { close(putStarted); putDone <- s.Put(v) }()
	<-putStarted
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = <-putDone; err != nil {
		t.Fatal(err)
	}
	called := false
	err = s.Dispatch(func() error { called = true; return nil })
	if !errors.Is(err, ErrSafeMode) || called {
		t.Fatalf("post-safety dispatch got through: %v %v", err, called)
	}
}
