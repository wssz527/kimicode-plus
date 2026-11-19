package main

import (
	"os"
	"path/filepath"
	"testing"

	"enhancedkimi/internal/updater"
)

func TestUpdateRestartReadsLoopbackPort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update-restart.json")
	if got := updater.ReadRestartState(dir); got != (updater.RestartState{}) {
		t.Fatalf("missing restart marker produced state %+v", got)
	}
	for _, entry := range []struct {
		data string
		want updater.RestartState
	}{
		{`{"port":36239,"reattach":true,"reattachDaily":true}`, updater.RestartState{Port: 36239, Reattach: true, ReattachDaily: true}},
		{`{"port":65536,"reattach":true}`, updater.RestartState{}},
		{`{"port":-1}`, updater.RestartState{}},
		{`{"port":`, updater.RestartState{}},
	} {
		if err := os.WriteFile(path, []byte(entry.data), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := updater.ReadRestartState(dir); got != entry.want {
			t.Errorf("restart marker %s = %+v, want %+v", entry.data, got, entry.want)
		}
	}
}
