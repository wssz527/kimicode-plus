package future

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enhancedkimi/internal/config"
	"enhancedkimi/internal/credentials"
	"enhancedkimi/internal/storage"
)

func browserFixture(t *testing.T) (*sql.DB, *credentials.Vault, *BrowserStore, *http.ServeMux) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "browser.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	vault, err := credentials.NewVault(db)
	if err != nil {
		t.Fatal(err)
	}
	if err = vault.Unlock(context.Background(), "synthetic-test-passphrase"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(vault.Lock)
	cfg, err := config.New(db)
	if err != nil {
		t.Fatal(err)
	}
	browser, err := NewBrowserStore(db, vault.BrowserStore(), cfg.Dispatch)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	browser.Register(mux)
	vault.Register(mux)
	return db, vault, browser, mux
}

func browserCall(t *testing.T, mux http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, "/enhance/v1"+path, bytes.NewReader(raw))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func previewCookies(t *testing.T, mux http.Handler, cookies []Cookie) Migration {
	t.Helper()
	w := browserCall(t, mux, "POST", "/browser/migrations/preview", map[string]any{"browser": "export", "target": "isolated", "mode": "ALL", "consent": true, "cookies": cookies})
	if w.Code != 200 {
		t.Fatalf("preview HTTP %d: %s", w.Code, w.Body.String())
	}
	var p Migration
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBrowserPreviewSupportsExportAndKeepsInternalEntriesPrivate(t *testing.T) {
	db, vault, browser, mux := browserFixture(t)
	cookies := make([]Cookie, 150)
	for i := range cookies {
		cookies[i] = Cookie{Domain: ".example.com", Path: "/", Name: fmt.Sprintf("c%d", i), Value: strings.Repeat("x", 240)}
	}
	p := previewCookies(t, mux, cookies)
	if p.Count != len(cookies) {
		t.Fatalf("preview count %d", p.Count)
	}
	ref := "migration/" + p.ID
	if _, err := vault.Get(context.Background(), ref); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("browser preview accessible as provider credential: %v", err)
	}
	w := browserCall(t, mux, "GET", "/credentials", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), ref) {
		t.Fatalf("internal reference listed: %s", w.Body.String())
	}
	w = browserCall(t, mux, "DELETE", "/credentials/"+ref, nil)
	if w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	if _, err := browser.vault.Get(context.Background(), ref); err != nil {
		t.Fatalf("credentials deletion affected browser preview: %v", err)
	}
	w = browserCall(t, mux, "POST", "/browser/migrations/"+p.ID+"/apply", map[string]string{"conflict": "KEEP"})
	if w.Code != 200 {
		t.Fatalf("apply HTTP %d: %s", w.Code, w.Body.String())
	}
	w = browserCall(t, mux, "DELETE", "/credentials/browser/isolated", nil)
	if w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	raw, err := browser.vault.Get(context.Background(), "browser/isolated")
	if err != nil {
		t.Fatal("credentials deletion affected isolated store", err)
	}
	defer clear(raw)
	var stored []Cookie
	if json.Unmarshal(raw, &stored) != nil || len(stored) != len(cookies) {
		t.Fatal("cookie export was not preserved")
	}
	var cipher []byte
	if err = db.QueryRow(`SELECT value FROM vault_browser_entries WHERE ref='browser/isolated'`).Scan(&cipher); err != nil || bytes.Contains(cipher, []byte(cookies[0].Value)) {
		t.Fatal("browser values were not encrypted", err)
	}
	w = browserCall(t, mux, "GET", "/credentials", nil)
	if strings.Contains(w.Body.String(), "browser/isolated") {
		t.Fatal("isolated store listed as credential")
	}
}

func TestExpiredBrowserPreviewIsCollectedWhileLocked(t *testing.T) {
	db, vault, _, mux := browserFixture(t)
	p := previewCookies(t, mux, []Cookie{{Domain: "example.com", Path: "/", Name: "expired", Value: "synthetic"}})
	p.At = time.Now().Add(-25 * time.Hour).UTC().Format(time.RFC3339Nano)
	raw, _ := json.Marshal(p)
	if _, err := db.Exec(`UPDATE browser_migrations SET data=? WHERE id=?`, string(raw), p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE vault_browser_entries SET expires_at=? WHERE ref=?`, time.Now().Add(-time.Hour).Unix(), "migration/"+p.ID); err != nil {
		t.Fatal(err)
	}
	vault.Lock()
	w := browserCall(t, mux, "GET", "/browser/migrations", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"EXPIRED"`) {
		t.Fatalf("expired preview state: %s", w.Body.String())
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM vault_browser_entries WHERE ref=?`, "migration/"+p.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("expired ciphertext remains", remaining, err)
	}
	w = browserCall(t, mux, "POST", "/browser/migrations/"+p.ID+"/apply", map[string]string{"conflict": "KEEP"})
	if w.Code != 410 || !strings.Contains(w.Body.String(), "MIGRATION_EXPIRED") {
		t.Fatalf("expired apply HTTP %d: %s", w.Code, w.Body.String())
	}
}

func TestBrowserLegacyMigrationPreservesCiphertextAndUnrelatedCredentials(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := credentials.NewVault(db)
	if err != nil {
		t.Fatal(err)
	}
	if err = vault.Unlock(context.Background(), "synthetic-test-passphrase"); err != nil {
		t.Fatal(err)
	}
	defer vault.Lock()
	if err = storage.Migrate(db, "browser_store", []string{`CREATE TABLE browser_migrations(id TEXT PRIMARY KEY,data TEXT NOT NULL); CREATE TABLE browser_permissions(origin TEXT PRIMARY KEY,data TEXT NOT NULL);`}); err != nil {
		t.Fatal(err)
	}
	p := Migration{ID: "legacy", Target: "old", At: time.Now().UTC().Format(time.RFC3339Nano), State: "PREVIEW"}
	raw, _ := json.Marshal(p)
	if _, err = db.Exec(`INSERT INTO browser_migrations VALUES(?,?)`, p.ID, string(raw)); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"migration/legacy", "browser/old", "browser/unrelated-provider"} {
		if err = vault.Put(context.Background(), ref, []byte(`[]`)); err != nil {
			t.Fatal(err)
		}
	}
	var before []byte
	if err = db.QueryRow(`SELECT value FROM vault_entries WHERE ref='browser/old'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	vault.Lock()
	browser, err := NewBrowserStore(db, vault.BrowserStore(), func(fn func() error) error { return fn() })
	if err != nil {
		t.Fatal("locked upgrade failed", err)
	}
	var after []byte
	if err = db.QueryRow(`SELECT value FROM vault_browser_entries WHERE ref='browser/old'`).Scan(&after); err != nil || !bytes.Equal(before, after) {
		t.Fatal("upgrade changed ciphertext", err)
	}
	if err = vault.Unlock(context.Background(), "synthetic-test-passphrase"); err != nil {
		t.Fatal(err)
	}
	if raw, err = browser.vault.Get(context.Background(), "browser/old"); err != nil || string(raw) != `[]` {
		t.Fatal("upgraded data cannot be decrypted", err)
	}
	if _, err = vault.Get(context.Background(), "browser/unrelated-provider"); err != nil {
		t.Fatal("unrelated provider credential moved", err)
	}
	if _, err = vault.Get(context.Background(), "migration/legacy"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatal("legacy browser preview remains accessible to providers", err)
	}
	if _, err = NewBrowserStore(db, vault.BrowserStore(), func(fn func() error) error { return fn() }); err != nil {
		t.Fatal("upgrade is not restartable", err)
	}
}

func TestSideEffectsUseDispatchGateDespiteSafeSnapshot(t *testing.T) {
	db, vault, _, _ := browserFixture(t)
	calls := 0
	deny := func(func() error) error { calls++; return config.ErrSafeMode }
	workspace, err := NewWorkspace(db, func() bool { return false }, deny)
	if err != nil {
		t.Fatal(err)
	}
	browser, err := NewBrowserStore(db, vault.BrowserStore(), deny)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	workspace.Register(mux)
	browser.Register(mux)
	for _, tc := range []struct {
		path string
		body any
	}{
		{"/workspace/worktrees", map[string]string{"repository": t.TempDir(), "name": "safe", "ref": "HEAD"}},
		{"/browser/migrations/unknown/apply", map[string]string{"conflict": "KEEP"}},
	} {
		w := browserCall(t, mux, "POST", tc.path, tc.body)
		if w.Code != 409 || !strings.Contains(w.Body.String(), "SAFE_MODE") {
			t.Fatalf("%s bypassed gate: HTTP %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	if calls != 2 {
		t.Fatalf("dispatch gate calls %d", calls)
	}
}

func TestBrowserStorageLimitsRemainSeparateFromCredentials(t *testing.T) {
	_, vault, browser, mux := browserFixture(t)
	ctx := context.Background()
	if err := vault.Put(ctx, "provider/large", bytes.Repeat([]byte("x"), 32769)); err == nil {
		t.Fatal("credential limit unexpectedly changed")
	}
	if err := browser.vault.Put(ctx, "browser/large", bytes.Repeat([]byte("x"), credentials.BrowserValueLimit+1)); !errors.Is(err, credentials.ErrTooLarge) {
		t.Fatal("browser store limit missing", err)
	}
	w := httptest.NewRecorder()
	browserVaultFailure(w, credentials.ErrTooLarge)
	if w.Code != 413 || !strings.Contains(w.Body.String(), "BROWSER_STORE_FULL") {
		t.Fatal("capacity error reported as vault lock", w.Body.String())
	}
	body := map[string]any{"browser": "export", "target": "large", "mode": "ALL", "consent": true, "cookies": []Cookie{{Domain: "example.com", Path: "/", Name: "large", Value: strings.Repeat("<", 900000)}}}
	var request bytes.Buffer
	encoder := json.NewEncoder(&request)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(body); err != nil {
		t.Fatal(err)
	}
	if request.Len() >= 1<<20 {
		t.Fatal("fixture exceeds advertised export limit")
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/enhance/v1/browser/migrations/preview", &request))
	if w.Code != 200 {
		t.Fatalf("sub-MiB export rejected after JSON normalization: %d %s", w.Code, w.Body.String())
	}
}
