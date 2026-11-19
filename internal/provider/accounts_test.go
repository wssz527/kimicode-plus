package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"enhancedkimi/internal/credentials"
	"enhancedkimi/internal/storage"
)

type accountFixture struct {
	service *Service
	client  *Client
	vault   *credentials.Vault
	mux     *http.ServeMux
	dbPath  string
}

func newAccountFixture(t *testing.T) *accountFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "accounts.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE registry_providers(id TEXT PRIMARY KEY,name TEXT,protocol TEXT,base_url TEXT,secret_ref TEXT,created_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	vault, err := credentials.NewVault(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.Unlock(context.Background(), "account-fixture-password"); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	client := New(db, vault)
	mux := http.NewServeMux()
	client.Register(mux)
	return &accountFixture{service: service, client: client, vault: vault, mux: mux, dbPath: path}
}

func (f *accountFixture) createProvider(t *testing.T, id, secretRef string) {
	t.Helper()
	var ref any
	if secretRef != "" {
		ref = secretRef
	}
	if _, err := f.service.db.Exec(`INSERT INTO registry_providers(id,name,protocol,base_url,secret_ref,created_at) VALUES(?,?,?,?,?,?)`,
		id, id, "openai", "https://example.invalid/v1", ref, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
}

func (f *accountFixture) call(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/enhance/v1"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func TestProviderWithoutAccountsKeepsLegacySecretRef(t *testing.T) {
	f := newAccountFixture(t)
	f.createProvider(t, "legacy", "provider/legacy/default")
	items, effective, err := f.service.Accounts(context.Background(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("unexpected accounts %+v", items)
	}
	if effective.Source != AccountSourceLegacy || effective.SecretRef == nil || *effective.SecretRef != "provider/legacy/default" {
		t.Fatalf("legacy provider.secret_ref not used: %+v", effective)
	}
	w := f.call(t, "GET", "/providers/legacy/accounts", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"source":"PROVIDER_SECRET_REF"`) {
		t.Fatalf("accounts view %d %s", w.Code, w.Body.String())
	}
}

func TestMultipleAccountsResolveDeterministically(t *testing.T) {
	f := newAccountFixture(t)
	f.createProvider(t, "kimi", "")
	for _, body := range []string{
		`{"id":"acct-b","label":"B","secretRef":"provider/kimi/b","priority":5}`,
		`{"id":"acct-a","label":"A","secretRef":"provider/kimi/a","priority":1}`,
		`{"id":"acct-disabled","label":"Off","secretRef":"provider/kimi/off","priority":0,"enabled":false}`,
	} {
		if w := f.call(t, "POST", "/providers/kimi/accounts", body); w.Code != 201 {
			t.Fatalf("create account %d: %s", w.Code, w.Body.String())
		}
	}
	items, effective, err := f.service.Accounts(context.Background(), "kimi")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("account count %d", len(items))
	}
	// Priority order is the visible order, and the disabled priority-0 account
	// is skipped even though it sorts first.
	if items[0].ID != "acct-disabled" {
		t.Fatalf("order not by priority: %+v", items)
	}
	if effective.Source != AccountSourceExplicit || effective.AccountID != "acct-a" || *effective.SecretRef != "provider/kimi/a" {
		t.Fatalf("resolution %+v", effective)
	}
	// Secret references are visible as references; raw values never are.
	w := f.call(t, "GET", "/providers/kimi/accounts", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "provider/kimi/a") {
		t.Fatalf("list %d %s", w.Code, w.Body.String())
	}
}

func TestAccountCRUDIsRevisionGuarded(t *testing.T) {
	f := newAccountFixture(t)
	f.createProvider(t, "kimi", "")
	w := f.call(t, "POST", "/providers/kimi/accounts", `{"id":"one","label":"One","secretRef":"provider/kimi/one"}`)
	if w.Code != 201 {
		t.Fatalf("create %d: %s", w.Code, w.Body.String())
	}
	var account Account
	json.Unmarshal(w.Body.Bytes(), &account)
	if account.Revision != 1 || !account.Enabled || account.Weight != 100 {
		t.Fatalf("defaults %+v", account)
	}
	if got := f.call(t, "PUT", "/providers/kimi/accounts/one", `{"label":"Renamed","secretRef":"provider/kimi/one","revision":1}`); got.Code != 200 {
		t.Fatalf("update %d: %s", got.Code, got.Body.String())
	}
	if got := f.call(t, "PUT", "/providers/kimi/accounts/one", `{"label":"Stale","secretRef":"provider/kimi/one","revision":1}`); got.Code != 409 {
		t.Fatalf("stale update %d: %s", got.Code, got.Body.String())
	}
	if got := f.call(t, "PUT", "/providers/kimi/accounts/one", `{"label":"NoRev","secretRef":"provider/kimi/one"}`); got.Code != 400 {
		t.Fatalf("missing revision %d: %s", got.Code, got.Body.String())
	}
	if got := f.call(t, "PUT", "/providers/kimi/accounts/one", `{"id":"other","label":"x","secretRef":"provider/kimi/one","revision":2}`); got.Code != 400 {
		t.Fatalf("id change %d: %s", got.Code, got.Body.String())
	}
	if got := f.call(t, "DELETE", "/providers/kimi/accounts/missing", ""); got.Code != 404 {
		t.Fatalf("delete missing %d", got.Code)
	}
	if got := f.call(t, "DELETE", "/providers/kimi/accounts/one", ""); got.Code != 200 {
		t.Fatalf("delete %d: %s", got.Code, got.Body.String())
	}
	if got := f.call(t, "DELETE", "/providers/kimi/accounts/one", ""); got.Code != 404 {
		t.Fatalf("second delete %d", got.Code)
	}
}

func TestAccountValidationRejectsUnsafeInput(t *testing.T) {
	f := newAccountFixture(t)
	f.createProvider(t, "kimi", "")
	for _, body := range []string{
		`{"label":"x","secretRef":"sk-syntheticsecretvalue1234"}`,
		`{"label":"x","secretRef":"provider/kimi/../x"}`,
		`{"label":"x","secretRef":""}`,
		`{"label":"x"}`,
		`{"label":"","secretRef":"provider/kimi/x"}`,
		`{"label":"` + strings.Repeat("a", 101) + `","secretRef":"provider/kimi/x"}`,
		`{"label":"x","secretRef":"provider/kimi/x","priority":-1}`,
		`{"label":"x","secretRef":"provider/kimi/x","priority":1001}`,
		`{"label":"x","secretRef":"provider/kimi/x","weight":-5}`,
		`{"label":"x","secretRef":"provider/kimi/x","weight":10001}`,
		`{"label":"x","secretRef":"provider/kimi/x","unknownField":true}`,
	} {
		if got := f.call(t, "POST", "/providers/kimi/accounts", body); got.Code != 400 && got.Code != 422 {
			t.Fatalf("unsafe account accepted (%d): %s", got.Code, got.Body.String())
		}
	}
	if got := f.call(t, "POST", "/providers/missing/accounts", `{"label":"x","secretRef":"provider/missing/x"}`); got.Code != 404 {
		t.Fatalf("unknown provider %d: %s", got.Code, got.Body.String())
	}
	if got := f.call(t, "POST", "/providers/kimi/accounts", `{"id":"UPPER","label":"x","secretRef":"provider/kimi/x"}`); got.Code != 400 {
		t.Fatalf("invalid id %d: %s", got.Code, got.Body.String())
	}
}

func TestDuplicateSecretReferenceIsRejected(t *testing.T) {
	f := newAccountFixture(t)
	f.createProvider(t, "kimi", "")
	f.createProvider(t, "other", "")
	if got := f.call(t, "POST", "/providers/kimi/accounts", `{"label":"a","secretRef":"shared/ref"}`); got.Code != 201 {
		t.Fatalf("first %d: %s", got.Code, got.Body.String())
	}
	if got := f.call(t, "POST", "/providers/kimi/accounts", `{"label":"b","secretRef":"shared/ref"}`); got.Code != 409 {
		t.Fatalf("duplicate in provider %d: %s", got.Code, got.Body.String())
	}
	if got := f.call(t, "POST", "/providers/other/accounts", `{"label":"b","secretRef":"shared/ref"}`); got.Code != 409 {
		t.Fatalf("duplicate across providers %d: %s", got.Code, got.Body.String())
	}
}

// Accounts are authored data: a duplicate reference must not be created even
// under concurrent writes.
func TestConcurrentAccountCreationKeepsReferenceUnique(t *testing.T) {
	f := newAccountFixture(t)
	f.createProvider(t, "kimi", "")
	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = f.call(t, "POST", "/providers/kimi/accounts", `{"label":"same","secretRef":"provider/kimi/same"}`).Code
		}(i)
	}
	wg.Wait()
	created := 0
	for _, code := range codes {
		if code == 201 {
			created++
		} else if code != 409 && code != 500 {
			t.Fatalf("unexpected status %d", code)
		}
	}
	if created != 1 {
		t.Fatalf("created %d accounts for one reference", created)
	}
}

// Chat must authenticate with the selected account, not the provider default.
func TestEffectiveConfigUsesSelectedAccount(t *testing.T) {
	f := newAccountFixture(t)
	f.createProvider(t, "kimi", "provider/kimi/default")
	var seen string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer remote.Close()
	if _, err := f.service.db.Exec(`UPDATE registry_providers SET base_url=? WHERE id='kimi'`, remote.URL); err != nil {
		t.Fatal(err)
	}
	if err := f.vault.Put(context.Background(), "provider/kimi/default", []byte("default-key")); err != nil {
		t.Fatal(err)
	}
	if err := f.vault.Put(context.Background(), "provider/kimi/primary", []byte("account-key")); err != nil {
		t.Fatal(err)
	}
	if got := f.call(t, "POST", "/providers/kimi/accounts", `{"id":"primary","label":"Primary","secretRef":"provider/kimi/primary"}`); got.Code != 201 {
		t.Fatalf("create %d: %s", got.Code, got.Body.String())
	}
	effectiveConfig, effective, err := f.client.EffectiveConfig(context.Background(), "kimi")
	if err != nil {
		t.Fatal(err)
	}
	if effective.AccountID != "primary" {
		t.Fatalf("resolution %+v", effective)
	}
	if _, err := f.client.ChatWith(context.Background(), effectiveConfig, "m", []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if seen != "Bearer account-key" {
		t.Fatalf("wrong credential on the wire: %q", seen)
	}
	// Disabling the account falls back rather than silently reusing it.
	var account Account
	_ = json.Unmarshal(f.call(t, "GET", "/providers/kimi/accounts", "").Body.Bytes(), &struct {
		Items *[]Account `json:"items"`
	}{&[]Account{}})
	list := f.call(t, "GET", "/providers/kimi/accounts", "")
	var payload struct {
		Items []Account `json:"items"`
	}
	json.Unmarshal(list.Body.Bytes(), &payload)
	account = payload.Items[0]
	if got := f.call(t, "PUT", "/providers/kimi/accounts/primary", `{"label":"Primary","secretRef":"provider/kimi/primary","enabled":false,"revision":`+itoa(account.Revision)+`}`); got.Code != 200 {
		t.Fatalf("disable %d: %s", got.Code, got.Body.String())
	}
	_, effective, err = f.client.EffectiveConfig(context.Background(), "kimi")
	if err != nil {
		t.Fatal(err)
	}
	if effective.Source != AccountSourceNone {
		t.Fatalf("disabled account still selected: %+v", effective)
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	out := ""
	for v > 0 {
		out = string(rune('0'+v%10)) + out
		v /= 10
	}
	return out
}

func TestAccountSurvivesRestartAndProviderDeleteCascades(t *testing.T) {
	f := newAccountFixture(t)
	f.createProvider(t, "kimi", "")
	if got := f.call(t, "POST", "/providers/kimi/accounts", `{"id":"kept","label":"Kept","secretRef":"provider/kimi/kept"}`); got.Code != 201 {
		t.Fatalf("create %d", got.Code)
	}
	db, err := storage.Open(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	restarted, err := NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	items, _, err := restarted.Accounts(context.Background(), "kimi")
	if err != nil || len(items) != 1 || items[0].ID != "kept" {
		t.Fatalf("accounts not durable: %+v %v", items, err)
	}
	if _, err := db.Exec(`DELETE FROM registry_providers WHERE id='kimi'`); err != nil {
		t.Fatal(err)
	}
	items, _, err = restarted.Accounts(context.Background(), "kimi")
	if err == nil || len(items) != 0 {
		t.Fatalf("orphan accounts readable after provider delete: %+v %v", items, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM provider_accounts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cascade did not remove accounts: %d %v", count, err)
	}
}

func writeImportFile(t *testing.T, document string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic-login.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func importRequest(t *testing.T, path string, fields map[string]string) string {
	t.Helper()
	if fields == nil {
		fields = make(map[string]string)
	}
	fields["path"] = path
	payload, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func TestImportPreviewIsReadOnlyAndNeverReturnsValues(t *testing.T) {
	f := newAccountFixture(t)
	f.createProvider(t, "kimi", "")
	path := writeImportFile(t, `{"apiKey":"sk-synthetic-import-value","refreshToken":"synthetic-refresh","expiresAt":"2026-12-01T00:00:00Z","email":"synthetic@example.invalid"}`)
	w := f.call(t, "POST", "/accounts/import/preview", importRequest(t, path, nil))
	if w.Code != 200 {
		t.Fatalf("preview %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, secret := range []string{"sk-synthetic-import-value", "synthetic-refresh", "synthetic@example.invalid"} {
		if strings.Contains(body, secret) {
			t.Fatalf("preview leaked %q: %s", secret, body)
		}
	}
	var payload struct {
		Preview struct {
			Fingerprint  string   `json:"fingerprint"`
			HasAPIKey    bool     `json:"hasApiKey"`
			HasTokenPair bool     `json:"hasTokenPair"`
			Unknown      []string `json:"unknownFields"`
			ExpiresAt    *string  `json:"expiresAt"`
		} `json:"preview"`
		Contract ImportContract `json:"contract"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Preview.HasAPIKey || payload.Preview.HasTokenPair || payload.Preview.ExpiresAt == nil {
		t.Fatalf("preview flags %+v", payload.Preview)
	}
	if len(payload.Preview.Unknown) != 1 || payload.Preview.Unknown[0] != "email" {
		t.Fatalf("unknown fields %+v", payload.Preview.Unknown)
	}
	if payload.Contract.WritesOfficial || payload.Contract.ReadsAmbient || payload.Contract.Mode != "READ_ONLY" {
		t.Fatalf("contract %+v", payload.Contract)
	}
	var accounts int
	if err := f.service.db.QueryRow(`SELECT count(*) FROM provider_accounts`).Scan(&accounts); err != nil || accounts != 0 {
		t.Fatalf("preview wrote accounts: %d %v", accounts, err)
	}
	// Applying with a stale fingerprint must not store anything.
	if got := f.call(t, "POST", "/providers/kimi/accounts/import", importRequest(t, path, map[string]string{"fingerprint": "sha256:stale"})); got.Code != 400 {
		t.Fatalf("stale fingerprint %d: %s", got.Code, got.Body.String())
	}
	var refs []string
	if err := f.service.db.QueryRow(`SELECT count(*) FROM vault_entries`).Scan(&accounts); err != nil || accounts != 0 {
		t.Fatalf("vault written before apply: %d %v", accounts, err)
	}
	_ = refs
}

func TestImportApplyCreatesDisabledAccount(t *testing.T) {
	f := newAccountFixture(t)
	f.createProvider(t, "kimi", "")
	path := writeImportFile(t, `{"apiKey":"sk-synthetic-import-value"}`)
	w := f.call(t, "POST", "/accounts/import/preview", importRequest(t, path, nil))
	var payload struct {
		Preview struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"preview"`
	}
	json.Unmarshal(w.Body.Bytes(), &payload)
	w = f.call(t, "POST", "/providers/kimi/accounts/import", importRequest(t, path, map[string]string{"label": "Imported", "fingerprint": payload.Preview.Fingerprint}))
	if w.Code != 201 {
		t.Fatalf("apply %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "sk-synthetic-import-value") {
		t.Fatal("apply response leaked the credential")
	}
	var account struct {
		Account Account `json:"account"`
	}
	json.Unmarshal(w.Body.Bytes(), &account)
	if account.Account.Enabled {
		t.Fatal("imported account must start disabled")
	}
	if account.Account.RedactedRef == "" || strings.Contains(account.Account.RedactedRef, "sk-synthetic") {
		t.Fatalf("secret reference is wrong: %q", account.Account.RedactedRef)
	}
	value, err := f.vault.Get(context.Background(), account.Account.RedactedRef)
	if err != nil || string(value) != "sk-synthetic-import-value" {
		t.Fatalf("imported credential not stored: %q %v", value, err)
	}
	// The file itself is untouched: read-only means read-only.
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "sk-synthetic-import-value") {
		t.Fatalf("source file modified: %v", err)
	}
	// A second apply with the same id is a conflict, not a silent overwrite.
	if again := f.call(t, "POST", "/providers/kimi/accounts/import", importRequest(t, path, map[string]string{"label": "Imported", "accountId": account.Account.ID, "fingerprint": payload.Preview.Fingerprint})); again.Code != 409 {
		t.Fatalf("duplicate apply %d: %s", again.Code, again.Body.String())
	}
	value, err = f.vault.Get(context.Background(), account.Account.RedactedRef)
	if err != nil || string(value) != "sk-synthetic-import-value" {
		t.Fatalf("duplicate import destroyed the existing credential: %q %v", value, err)
	}
}

func TestImportRejectsAmbientAndUnsafePaths(t *testing.T) {
	f := newAccountFixture(t)
	f.createProvider(t, "kimi", "")
	dir := t.TempDir()
	if got := f.call(t, "POST", "/accounts/import/preview", `{"path":"relative.json"}`); got.Code != 400 {
		t.Fatalf("relative path %d", got.Code)
	}
	if got := f.call(t, "POST", "/accounts/import/preview", importRequest(t, filepath.Join(dir, "missing.json"), nil)); got.Code != 400 {
		t.Fatalf("missing file %d: %s", got.Code, got.Body.String())
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(writeImportFile(t, `{"apiKey":"sk-x"}`), link); err != nil {
		t.Fatal(err)
	}
	if got := f.call(t, "POST", "/accounts/import/preview", importRequest(t, link, nil)); got.Code != 400 || !strings.Contains(got.Body.String(), "SYMLINK_DENIED") {
		t.Fatalf("symlink %d: %s", got.Code, got.Body.String())
	}
	if got := f.call(t, "POST", "/accounts/import/preview", `{"path":"/etc/hosts"}`); got.Code != 400 {
		t.Fatalf("non-JSON file %d: %s", got.Code, got.Body.String())
	}
	if got := f.call(t, "POST", "/accounts/import/preview", importRequest(t, dir, nil)); got.Code != 400 {
		t.Fatalf("directory %d: %s", got.Code, got.Body.String())
	}
	big := writeImportFile(t, `{"apiKey":"`+strings.Repeat("a", importMaxBytes)+`"}`)
	if got := f.call(t, "POST", "/accounts/import/preview", importRequest(t, big, nil)); got.Code != 400 {
		t.Fatalf("oversized file %d: %s", got.Code, got.Body.String())
	}
	if got := f.call(t, "POST", "/accounts/import/preview", importRequest(t, writeImportFile(t, `{"account":"nothing"}`), nil)); got.Code != 400 {
		t.Fatalf("file without a credential %d: %s", got.Code, got.Body.String())
	}
}

// An official-looking directory is not itself a reason to refuse: explicit
// consent is the boundary. The fixture builds its own fake official tree under
// a temp dir, so no real client storage is ever touched.
func TestExplicitOfficialPathIsReadableWithoutAmbientScan(t *testing.T) {
	f := newAccountFixture(t)
	f.createProvider(t, "kimi", "")
	fakeHome := filepath.Join(t.TempDir(), "fakehome")
	official := filepath.Join(fakeHome, ".kimi-code", "credentials.json")
	if err := os.MkdirAll(filepath.Dir(official), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(official, []byte(`{"apiKey":"sk-synthetic-official","expires_at":1767225600}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w := f.call(t, "POST", "/accounts/import/preview", importRequest(t, official, nil))
	if w.Code != 200 {
		t.Fatalf("explicit official path denied: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "sk-synthetic-official") {
		t.Fatal("preview leaked the credential")
	}
	var payload struct {
		Preview struct {
			ExpiresAt *string `json:"expiresAt"`
			HasAPIKey bool    `json:"hasApiKey"`
		} `json:"preview"`
	}
	json.Unmarshal(w.Body.Bytes(), &payload)
	if !payload.Preview.HasAPIKey || payload.Preview.ExpiresAt == nil || *payload.Preview.ExpiresAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("preview %+v", payload.Preview)
	}
	// The file is only read, never rewritten.
	data, err := os.ReadFile(official)
	if err != nil || !strings.Contains(string(data), "sk-synthetic-official") {
		t.Fatalf("source file changed: %v", err)
	}
	// Nothing outside the named path is discovered: a sibling file with a
	// credential must not appear anywhere in the response.
	sibling := filepath.Join(filepath.Dir(official), "other.json")
	if err := os.WriteFile(sibling, []byte(`{"apiKey":"sk-sibling-not-requested"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w = f.call(t, "POST", "/accounts/import/preview", importRequest(t, official, nil))
	if strings.Contains(w.Body.String(), "sibling") || strings.Contains(w.Body.String(), "other.json") {
		t.Fatalf("importer scanned beyond the named path: %s", w.Body.String())
	}
	var accounts int
	if err := f.service.db.QueryRow(`SELECT count(*) FROM provider_accounts`).Scan(&accounts); err != nil || accounts != 0 {
		t.Fatalf("preview created an account: %d %v", accounts, err)
	}
}

// The official client stores expires_at as Unix seconds. Both the numeric form
// and an RFC3339 string normalize to the same UTC instant; nonsense stays
// absent instead of becoming a guessed timestamp.
func TestImportExpiryNormalizesSecondsAndRFC3339(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{"unix seconds", `1767225600`, "2026-01-01T00:00:00Z", true},
		{"unix seconds as float-free integer", `1767225601`, "2026-01-01T00:00:01Z", true},
		{"rfc3339 string", `"2026-01-01T08:00:00+08:00"`, "2026-01-01T00:00:00Z", true},
		{"zero", `0`, "", false},
		{"negative", `-100`, "", false},
		{"milliseconds", `1767225600000`, "", false},
		{"fractional", `1767225600.5`, "", false},
		{"past year 2101", `4133980800`, "", false},
		{"garbage string", `"tomorrow"`, "", false},
		{"object", `{"seconds":1}`, "", false},
		{"null", `null`, "", false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parseExpiry(json.RawMessage(test.raw))
			if ok != test.ok || got != test.want {
				t.Fatalf("parseExpiry(%s) = %q,%v want %q,%v", test.raw, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestImportApplyRequiresUnlockedVault(t *testing.T) {
	f := newAccountFixture(t)
	f.createProvider(t, "kimi", "")
	path := writeImportFile(t, `{"apiKey":"sk-synthetic-import-value"}`)
	w := f.call(t, "POST", "/accounts/import/preview", importRequest(t, path, nil))
	var payload struct {
		Preview struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"preview"`
	}
	json.Unmarshal(w.Body.Bytes(), &payload)
	f.vault.Lock()
	got := f.call(t, "POST", "/providers/kimi/accounts/import", importRequest(t, path, map[string]string{"fingerprint": payload.Preview.Fingerprint}))
	if got.Code != 423 || !strings.Contains(got.Body.String(), "VAULT_LOCKED") {
		t.Fatalf("locked apply %d: %s", got.Code, got.Body.String())
	}
	var accounts int
	if err := f.service.db.QueryRow(`SELECT count(*) FROM provider_accounts`).Scan(&accounts); err != nil || accounts != 0 {
		t.Fatalf("locked apply wrote an account: %d %v", accounts, err)
	}
}
