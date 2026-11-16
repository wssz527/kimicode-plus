package config

import (
	"path/filepath"
	"testing"
	"time"

	"enhancedkimi/internal/storage"
)

func TestDispatchAllowsConcurrentCalls(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan struct{}, 2)
	defer func() {
		close(release)
		for range 2 {
			<-done
		}
	}()
	for range 2 {
		go func() {
			defer func() { done <- struct{}{} }()
			if err := s.Dispatch(func() error {
				entered <- struct{}{}
				<-release
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("independent dispatches were serialized")
		}
	}
}
