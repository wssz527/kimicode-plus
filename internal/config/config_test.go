package config

import (
	"enhancedkimi/internal/storage"
	"errors"
	"path/filepath"
	"testing"
)

func TestSettingsCASAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, e := storage.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	s, e := New(db)
	if e != nil {
		t.Fatal(e)
	}
	v, e := s.Get()
	if e != nil {
		t.Fatal(e)
	}
	v.SafeMode = true
	if e = s.Put(v); e != nil {
		t.Fatal(e)
	}
	if !errors.Is(s.Put(v), ErrConflict) {
		t.Fatal("stale overwrite accepted")
	}
	db.Close()
	db, e = storage.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s, e = New(db)
	if e != nil {
		t.Fatal(e)
	}
	v, e = s.Get()
	if e != nil || !s.Safe() || v.CompactionIdleMinutes != 25 || v.Revision != 1 {
		t.Fatalf("restored %+v %v", v, e)
	}
}
