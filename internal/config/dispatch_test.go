package config

import (
	"context"
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

// Turning safe mode on must stop new admissions immediately, while work that was
// already admitted keeps running to completion.
func TestSafeModeAdmissionGateAndInflightSemantics(t *testing.T) {
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

	admitted := make(chan struct{})
	release := make(chan struct{})
	admittedDone := make(chan error, 1)
	go func() {
		admittedDone <- s.Dispatch(func() error {
			close(admitted)
			<-release
			return nil
		})
	}()
	<-admitted

	v.SafeMode = true
	if err := s.Put(v); err != nil {
		t.Fatal(err)
	}

	// A new dispatch is refused as soon as the flag is stored.
	called := false
	if err := s.Dispatch(func() error { called = true; return nil }); !errors.Is(err, ErrSafeMode) || called {
		t.Fatalf("dispatch after safe mode = %v called=%v", err, called)
	}

	// The in-flight dispatch is not cancelled by the switch; WaitIdle reports it.
	idleCtx, idleCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	waitErr := s.WaitIdle(idleCtx)
	idleCancel()
	if !errors.Is(waitErr, context.DeadlineExceeded) {
		t.Fatalf("WaitIdle with work in flight = %v", waitErr)
	}
	close(release)
	if err := <-admittedDone; err != nil {
		t.Fatalf("admitted dispatch was disturbed by safe mode: %v", err)
	}
	if err := s.WaitIdle(context.Background()); err != nil {
		t.Fatalf("WaitIdle after completion: %v", err)
	}
}
