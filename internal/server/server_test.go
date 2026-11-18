package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"enhancedkimi/internal/security"
	"enhancedkimi/internal/storage"
)

func TestRealLoopbackCoreLifecycle(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "integration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	guard := security.New(listener.Addr().String())
	// A real plugin root so the bundled plugin is released and the safe-mode
	// check runs against an install that actually exists.
	app, err := NewWithNative(db, guard, false, "", NativeOptions{DataDir: t.TempDir()})
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	defer app.Close()
	ts := httptest.NewUnstartedServer(app.Handler)
	ts.Listener.Close()
	ts.Listener = listener
	ts.Start()
	defer ts.Close()
	request := func(method, path string, body any, token string) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			b, e := json.Marshal(body)
			if e != nil {
				t.Fatal(e)
			}
			reader = bytes.NewReader(b)
		}
		r, e := http.NewRequest(method, ts.URL+"/enhance/v1"+path, reader)
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		res, e := ts.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	code, _ := request("GET", "/health", nil, "")
	if code != 401 {
		t.Fatalf("unauthenticated health %d", code)
	}
	code, b := request("GET", "/health", nil, guard.PairingToken())
	if code != 200 {
		t.Fatal(string(b))
	}
	var health struct {
		Status  string   `json:"status"`
		Modules []Module `json:"modules"`
	}
	if err = json.Unmarshal(b, &health); err != nil {
		t.Fatal(err)
	}
	if health.Status != "OK" {
		t.Fatal(health.Status)
	}
	for _, m := range health.Modules {
		if m.State != "READY" {
			t.Fatalf("module not ready %+v", m)
		}
	}
	code, b = request("GET", "/settings", nil, guard.PairingToken())
	if code != 200 {
		t.Fatal(string(b))
	}
	var settings map[string]any
	json.Unmarshal(b, &settings)
	settings["safeMode"] = true
	code, b = request("PUT", "/settings", settings, guard.PairingToken())
	if code != 200 {
		t.Fatal(string(b))
	}
	if !app.Settings.Safe() {
		t.Fatal("safe mode not applied")
	}
	code, _ = request("PUT", "/settings", settings, guard.PairingToken())
	if code != 409 {
		t.Fatal("settings CAS not enforced")
	}
	code, b = request("POST", "/plugins/kimicode-plus.monitor/enable", nil, guard.PairingToken())
	if code != 409 {
		t.Fatalf("safe mode permitted plugin activation: %d %s", code, b)
	}
	_, b = request("GET", "/settings", nil, guard.PairingToken())
	json.Unmarshal(b, &settings)
	settings["safeMode"] = false
	code, b = request("PUT", "/settings", settings, guard.PairingToken())
	if code != 200 {
		t.Fatal(string(b))
	}
	code, b = request("POST", "/plugins/kimicode-plus.monitor/enable", nil, guard.PairingToken())
	if code != 200 {
		t.Fatalf("leaving safe mode did not restore plugin activation: %d %s", code, b)
	}
	code, b = request("POST", "/pair", map[string]any{"pairingToken": guard.PairingToken(), "permissions": []string{"models:read"}}, "")
	if code != 200 {
		t.Fatal(string(b))
	}
	var grant struct {
		Token string `json:"sessionToken"`
	}
	json.Unmarshal(b, &grant)
	code, _ = request("GET", "/models", nil, grant.Token)
	if code != 200 {
		t.Fatal("scoped read failed")
	}
	code, _ = request("GET", "/chat/threads", nil, grant.Token)
	if code != 403 {
		t.Fatal("scope isolation failed")
	}
}
func TestModuleMigrationFailureIsIsolated(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "fault.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = storage.Migrate(db, "registry", []string{`SELECT 1;`}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE schema_versions SET version=999 WHERE module='registry'`); err != nil {
		t.Fatal(err)
	}
	app, err := New(db, security.New("127.0.0.1:1"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	found := false
	for _, m := range app.modules {
		if m.ID == "registry" {
			found = m.State == "FAULTED"
		}
	}
	if !found {
		t.Fatal("newer registry DB was not isolated")
	}
	if _, err = app.Settings.Get(); err != nil {
		t.Fatal("core settings unavailable after registry fault")
	}
}
