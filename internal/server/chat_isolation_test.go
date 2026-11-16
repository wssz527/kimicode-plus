package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"enhancedkimi/internal/security"
	"enhancedkimi/internal/storage"
)

func TestChatFailureStillAllowsSafeMode(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "chat-fault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = storage.Migrate(db, "chat", []string{`SELECT 1;`}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE schema_versions SET version=999 WHERE module='chat'`); err != nil {
		t.Fatal(err)
	}
	guard := security.New("127.0.0.1:1")
	app, err := New(db, guard, false)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	settings, err := app.Settings.Get()
	if err != nil {
		t.Fatal(err)
	}
	settings.SafeMode = true
	b, _ := json.Marshal(settings)
	r := httptest.NewRequest("PUT", "http://127.0.0.1:1/enhance/v1/settings", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+guard.PairingToken())
	w := httptest.NewRecorder()
	app.Handler.ServeHTTP(w, r)
	if w.Code != 200 || !app.Settings.Safe() {
		t.Fatalf("chat fault prevented recovery: %d %s", w.Code, w.Body)
	}
}
