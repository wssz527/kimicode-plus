package credentials

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"enhancedkimi/internal/storage"
)

func vaultDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func openVault(t *testing.T, db *sql.DB, password string) *Vault {
	t.Helper()
	v, err := NewVault(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Unlock(context.Background(), password); err != nil {
		t.Fatalf("unlock %q: %v", password, err)
	}
	return v
}

func TestSealOpenRoundTripIsBoundToReference(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	secret := []byte("synthetic-secret-value")
	sealed, err := seal(key, secret, "provider/kimi/account")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, secret) {
		t.Fatal("ciphertext contains the plaintext")
	}
	opened, err := open(key, sealed, "provider/kimi/account")
	if err != nil || !bytes.Equal(opened, secret) {
		t.Fatalf("round trip %q %v", opened, err)
	}
	// The reference is authenticated data: moving ciphertext between refs must
	// fail instead of silently authenticating a different account.
	if _, err := open(key, sealed, "provider/kimi/other"); err == nil {
		t.Fatal("ciphertext accepted under a different reference")
	}
	other := bytes.Repeat([]byte{8}, 32)
	if _, err := open(other, sealed, "provider/kimi/account"); err == nil {
		t.Fatal("ciphertext accepted under a different key")
	}
	sealed2, err := seal(key, secret, "provider/kimi/account")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(sealed, sealed2) {
		t.Fatal("nonce is not random per seal")
	}
}

func TestSealRejectsWrongKeyLengthAndShortCiphertext(t *testing.T) {
	if _, err := seal([]byte("short"), []byte("x"), "ref"); err == nil {
		t.Fatal("short AES key accepted")
	}
	key := bytes.Repeat([]byte{1}, 32)
	gcm, err := func() (cipher.AEAD, error) {
		block, e := aes.NewCipher(key)
		if e != nil {
			return nil, e
		}
		return cipher.NewGCM(block)
	}()
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{0, 1, gcm.NonceSize() - 1, gcm.NonceSize()} {
		if _, err := open(key, make([]byte, size), "ref"); err == nil {
			t.Fatalf("ciphertext of %d bytes accepted", size)
		}
	}
	tampered, _ := seal(key, []byte("value"), "ref")
	tampered[len(tampered)-1] ^= 0xff
	if _, err := open(key, tampered, "ref"); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}

func TestWrongPasswordCannotUnlockExistingVault(t *testing.T) {
	db := vaultDB(t)
	v := openVault(t, db, "correct-horse-battery")
	if err := v.Put(context.Background(), "provider/kimi", []byte("synthetic-secret")); err != nil {
		t.Fatal(err)
	}
	locked := openVault(t, db, "correct-horse-battery")
	if value, err := locked.Get(context.Background(), "provider/kimi"); err != nil || string(value) != "synthetic-secret" {
		t.Fatalf("reopened vault lost the secret: %q %v", value, err)
	}
	other, err := NewVault(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, password := range []string{"correct-horse-batterY", "another-long-password", strings.Repeat("a", 12)} {
		if err := other.Unlock(context.Background(), password); err == nil {
			t.Fatalf("wrong password %q unlocked the vault", password)
		}
		if err := other.Put(context.Background(), "provider/x", []byte("value")); !errors.Is(err, ErrLocked) {
			t.Fatalf("failed unlock left the vault usable: %v", err)
		}
	}
	if err := other.Unlock(context.Background(), "correct-horse-battery"); err != nil {
		t.Fatalf("correct password rejected after failures: %v", err)
	}
}

func TestPasswordLengthBoundary(t *testing.T) {
	db := vaultDB(t)
	v, err := NewVault(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, password := range []string{"", "short", strings.Repeat("a", 11), strings.Repeat("a", 1025)} {
		if err := v.Unlock(context.Background(), password); err == nil {
			t.Fatalf("password of %d bytes accepted", len(password))
		}
	}
	for _, password := range []string{strings.Repeat("a", 12), strings.Repeat("a", 1024)} {
		fresh, err := NewVault(vaultDB(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := fresh.Unlock(context.Background(), password); err != nil {
			t.Fatalf("password of %d bytes rejected: %v", len(password), err)
		}
	}
}

// A missing vault row is created once; a concurrent unlock must not race two
// different check values into the table.
func TestFirstUnlockCreatesSingleMetaRow(t *testing.T) {
	db := vaultDB(t)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := NewVault(db)
			if err != nil {
				errs[i] = err
				return
			}
			errs[i] = v.Unlock(context.Background(), "concurrent-unlock-password")
		}(i)
	}
	wg.Wait()
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM vault_meta`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("meta rows %d: %v", rows, err)
	}
	var ok int
	for _, err := range errs {
		if err == nil {
			ok++
		}
	}
	if ok == 0 {
		t.Fatal("no concurrent unlock succeeded")
	}
	reopened, err := NewVault(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Unlock(context.Background(), "concurrent-unlock-password"); err != nil {
		t.Fatalf("vault unusable after concurrent creation: %v", err)
	}
}

func TestSecretReferenceValidation(t *testing.T) {
	v := openVault(t, vaultDB(t), "reference-validation")
	good := []string{"a", "provider/kimi/account-1", "a.b_c/d-e", strings.Repeat("a", 200), "0start"}
	for _, ref := range good {
		if err := v.Put(context.Background(), ref, []byte("value")); err != nil {
			t.Fatalf("valid ref %q rejected: %v", ref, err)
		}
		if _, err := v.Get(context.Background(), ref); err != nil {
			t.Fatalf("valid ref %q unreadable: %v", ref, err)
		}
	}
	bad := []string{"", ".", "-a", "_a", "a b", "a\nb", "a\x00b", "../etc/passwd", "/abs", strings.Repeat("a", 201), "a\\b"}
	for _, ref := range bad {
		if err := v.Put(context.Background(), ref, []byte("value")); err == nil {
			t.Fatalf("invalid ref %q accepted", ref)
		}
	}
}

func TestCapacityBoundaries(t *testing.T) {
	v := openVault(t, vaultDB(t), "capacity-boundaries")
	if err := v.Put(context.Background(), "provider/empty", nil); err == nil {
		t.Fatal("empty secret accepted")
	}
	if err := v.Put(context.Background(), "provider/empty", []byte{}); err == nil {
		t.Fatal("zero-length secret accepted")
	}
	if err := v.Put(context.Background(), "provider/big", bytes.Repeat([]byte{1}, 32768)); err != nil {
		t.Fatalf("32 KiB secret rejected: %v", err)
	}
	if err := v.Put(context.Background(), "provider/too-big", bytes.Repeat([]byte{1}, 32769)); err == nil {
		t.Fatal("33 KiB secret accepted")
	}
	// A failed oversized write must not damage the previous value.
	value, err := v.Get(context.Background(), "provider/big")
	if err != nil || len(value) != 32768 {
		t.Fatalf("existing secret damaged: %d %v", len(value), err)
	}
	if _, err := v.Get(context.Background(), "provider/too-big"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected write persisted: %v", err)
	}
}

func TestVaultLockClearsKeyAndBlocksAccess(t *testing.T) {
	db := vaultDB(t)
	v := openVault(t, db, "lock-behaviour-password")
	if err := v.Put(context.Background(), "provider/kimi", []byte("synthetic")); err != nil {
		t.Fatal(err)
	}
	v.Lock()
	if len(v.key) != 0 {
		t.Fatal("lock did not clear the key")
	}
	if _, err := v.Get(context.Background(), "provider/kimi"); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked get %v", err)
	}
	if err := v.Put(context.Background(), "provider/kimi", []byte("overwrite")); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked put %v", err)
	}
	if err := v.Delete(context.Background(), "provider/kimi"); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked delete %v", err)
	}
	// Restart is locked and the ciphertext is unreadable, not lost.
	if _, err := NewVault(db); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewVault(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Get(context.Background(), "provider/kimi"); !errors.Is(err, ErrLocked) {
		t.Fatalf("restarted vault not locked: %v", err)
	}
	if err := restarted.Unlock(context.Background(), "lock-behaviour-password"); err != nil {
		t.Fatal(err)
	}
	value, err := restarted.Get(context.Background(), "provider/kimi")
	if err != nil || string(value) != "synthetic" {
		t.Fatalf("secret lost across lock cycle: %q %v", value, err)
	}
}

func TestUnlockRotatesKeyAcrossPasswords(t *testing.T) {
	db := vaultDB(t)
	v := openVault(t, db, "first-strong-password")
	if err := v.Put(context.Background(), "provider/a", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := v.Unlock(context.Background(), "second-strong-password"); err == nil {
		t.Fatal("vault accepted a second password without rekey")
	}
	if _, err := v.Get(context.Background(), "provider/a"); err != nil {
		t.Fatalf("failed re-unlock cleared the working key: %v", err)
	}
}

func TestDeleteRemovesOnlyTargetReference(t *testing.T) {
	v := openVault(t, vaultDB(t), "delete-behaviour")
	for _, ref := range []string{"provider/a", "provider/b"} {
		if err := v.Put(context.Background(), ref, []byte("value-"+ref)); err != nil {
			t.Fatal(err)
		}
	}
	if err := v.Delete(context.Background(), "provider/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get(context.Background(), "provider/a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted ref readable: %v", err)
	}
	if value, err := v.Get(context.Background(), "provider/b"); err != nil || string(value) != "value-provider/b" {
		t.Fatalf("unrelated ref affected: %q %v", value, err)
	}
	if err := v.Delete(context.Background(), "provider/missing"); err != nil {
		t.Fatalf("deleting a missing ref must be a no-op: %v", err)
	}
}

// The HTTP surface must never return secret values, and must report lock state
// rather than leaking ciphertext or an internal error.
func TestCredentialAPIHidesValuesAndLockState(t *testing.T) {
	v := openVault(t, vaultDB(t), "api-surface-password")
	if err := v.Put(context.Background(), "provider/kimi", []byte("synthetic-secret")); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	v.Register(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := call("GET", "/enhance/v1/credentials", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "synthetic-secret") || !strings.Contains(w.Body.String(), `"unlocked":true`) {
		t.Fatalf("list leaked or wrong: %d %s", w.Code, w.Body.String())
	}
	v.Lock()
	w = call("GET", "/enhance/v1/credentials", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"unlocked":false`) {
		t.Fatalf("locked list %d %s", w.Code, w.Body.String())
	}
	if w = call("PUT", "/enhance/v1/credentials", `{"ref":"provider/kimi","value":"x"}`); w.Code != 422 {
		t.Fatalf("locked write %d", w.Code)
	}
	if w = call("DELETE", "/enhance/v1/credentials/provider/kimi", ""); w.Code != 423 {
		t.Fatalf("locked delete %d", w.Code)
	}
}

func TestMemoryStoreIsolatesCallerBuffers(t *testing.T) {
	store := NewMemory()
	ctx := context.Background()
	value := []byte("synthetic")
	if err := store.Put(ctx, "ref", value); err != nil {
		t.Fatal(err)
	}
	value[0] = 'X'
	got, err := store.Get(ctx, "ref")
	if err != nil || string(got) != "synthetic" {
		t.Fatalf("store aliased the caller buffer: %q %v", got, err)
	}
	got[0] = 'Y'
	again, _ := store.Get(ctx, "ref")
	if string(again) != "synthetic" {
		t.Fatal("get returned an aliased buffer")
	}
	if err := store.Delete(ctx, "ref"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "ref"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted entry readable: %v", err)
	}
}

func TestVaultCiphertextDoesNotContainPlaintext(t *testing.T) {
	db := vaultDB(t)
	v := openVault(t, db, "ciphertext-password")
	secret := []byte("synthetic-plaintext-marker")
	if err := v.Put(context.Background(), "provider/kimi", secret); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := db.QueryRow(`SELECT value FROM vault_entries WHERE ref='provider/kimi'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, secret) {
		t.Fatal("vault stored plaintext")
	}
}

type fakeKeyStore struct {
	mu        sync.Mutex
	secret    string
	found     bool
	available bool
	backend   string
	loadErr   error
	saveErr   error
	deleteErr error
	saves     int
	deletes   int
}

func newFakeKeyStore() *fakeKeyStore {
	return &fakeKeyStore{available: true, backend: "TEST_KEYCHAIN"}
}

func (f *fakeKeyStore) Available() bool     { return f.available }
func (f *fakeKeyStore) BackendName() string { return f.backend }

func (f *fakeKeyStore) Load(context.Context) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadErr != nil {
		return "", false, f.loadErr
	}
	return f.secret, f.found, nil
}

func (f *fakeKeyStore) Save(_ context.Context, secret string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saves++
	if f.saveErr != nil {
		return f.saveErr
	}
	f.secret, f.found = secret, true
	return nil
}

func (f *fakeKeyStore) Delete(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.secret, f.found = "", false
	return nil
}

func (f *fakeKeyStore) seed(secret string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secret, f.found = secret, true
}

func (f *fakeKeyStore) storedSecret(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.found {
		t.Fatal("key store holds no secret")
	}
	return f.secret
}

func (f *fakeKeyStore) saveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.saves
}

func (f *fakeKeyStore) deleteCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deletes
}

func keyStoreVault(t *testing.T, db *sql.DB, store OSKeyStore) *Vault {
	t.Helper()
	v, err := NewVault(db)
	if err != nil {
		t.Fatal(err)
	}
	v.UseKeyStore(store)
	return v
}

func TestAutoUnlockCreatesOSManagedVault(t *testing.T) {
	ctx := context.Background()
	store := newFakeKeyStore()
	v := keyStoreVault(t, vaultDB(t), store)
	if err := v.AutoUnlock(ctx); err != nil {
		t.Fatalf("auto unlock: %v", err)
	}
	if !v.Unlocked() {
		t.Fatal("auto unlock left the vault locked")
	}
	secret := store.storedSecret(t)
	if len(secret) != 64 {
		t.Fatalf("generated key is %d characters", len(secret))
	}
	if _, err := hex.DecodeString(secret); err != nil {
		t.Fatalf("generated key is not hex: %v", err)
	}
	if err := v.Put(ctx, "provider/kimi", []byte("synthetic-secret")); err != nil {
		t.Fatalf("write through auto-unlocked vault: %v", err)
	}
	status := v.Status(ctx)
	if !status.Unlocked || !status.OSKeychain || !status.OSKeychainAvailable || status.OSKeychainBackend != "TEST_KEYCHAIN" {
		t.Fatalf("status after auto unlock: %+v", status)
	}
	if status.LegacyPasswordVault || status.Backend != "AES256_GCM_PBKDF2" {
		t.Fatalf("status flags after auto unlock: %+v", status)
	}
	if len(status.Refs) != 1 || status.Refs[0] != "provider/kimi" {
		t.Fatalf("status refs %v", status.Refs)
	}
	v.Lock()
	if err := v.AutoUnlock(ctx); err != nil || !v.Unlocked() {
		t.Fatalf("re-unlock through the stored key: %v", err)
	}
}

func TestAutoUnlockReusesStoredKeyForNewVault(t *testing.T) {
	ctx := context.Background()
	store := newFakeKeyStore()
	stored := "{{SECRET_2ccjjsxo}}"
	store.seed(stored)
	v := keyStoreVault(t, vaultDB(t), store)
	if err := v.AutoUnlock(ctx); err != nil {
		t.Fatal(err)
	}
	if got := store.storedSecret(t); got != stored {
		t.Fatalf("auto unlock replaced the existing key with %q", got)
	}
	if store.saveCount() != 0 {
		t.Fatalf("auto unlock saved %d times for an existing key", store.saveCount())
	}
	if err := v.Put(ctx, "provider/kimi", []byte("synthetic")); err != nil {
		t.Fatal(err)
	}
	if value, err := v.Get(ctx, "provider/kimi"); err != nil || string(value) != "synthetic" {
		t.Fatalf("reused key cannot open its own vault: %q %v", value, err)
	}
}

func TestAutoUnlockCreatesFreshKeyWhenStoreCannotBeRead(t *testing.T) {
	ctx := context.Background()
	store := newFakeKeyStore()
	store.loadErr = errors.New("keychain locked")
	v := keyStoreVault(t, vaultDB(t), store)
	if err := v.AutoUnlock(ctx); err != nil {
		t.Fatalf("auto unlock with unreadable key store: %v", err)
	}
	if !v.Unlocked() || len(store.storedSecret(t)) != 64 {
		t.Fatal("unreadable key store did not produce a fresh OS-managed vault")
	}
}

func TestAutoUnlockReopensVaultAfterRestart(t *testing.T) {
	ctx := context.Background()
	db := vaultDB(t)
	store := newFakeKeyStore()
	first := keyStoreVault(t, db, store)
	if err := first.AutoUnlock(ctx); err != nil {
		t.Fatal(err)
	}
	if err := first.Put(ctx, "provider/kimi", []byte("synthetic-secret")); err != nil {
		t.Fatal(err)
	}

	restarted := keyStoreVault(t, db, store)
	if restarted.Unlocked() {
		t.Fatal("a new Vault is unlocked before AutoUnlock")
	}
	if err := restarted.AutoUnlock(ctx); err != nil {
		t.Fatalf("auto unlock after restart: %v", err)
	}
	value, err := restarted.Get(ctx, "provider/kimi")
	if err != nil || string(value) != "synthetic-secret" {
		t.Fatalf("secret after restart: %q %v", value, err)
	}
	status := restarted.Status(ctx)
	if !status.Unlocked || !status.OSKeychain || status.LegacyPasswordVault {
		t.Fatalf("status after restart: %+v", status)
	}
}

func TestAutoUnlockLeavesLegacyPasswordVaultLocked(t *testing.T) {
	ctx := context.Background()
	db := vaultDB(t)
	legacy := openVault(t, db, "legacy-manual-password")
	if err := legacy.Put(ctx, "provider/kimi", []byte("legacy-secret")); err != nil {
		t.Fatal(err)
	}

	store := newFakeKeyStore()
	v := keyStoreVault(t, db, store)
	if err := v.AutoUnlock(ctx); err != nil {
		t.Fatalf("legacy vault broke auto unlock: %v", err)
	}
	if v.Unlocked() {
		t.Fatal("legacy password vault was unlocked without its password")
	}
	if _, err := v.Get(ctx, "provider/kimi"); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked legacy vault served a secret: %v", err)
	}
	status := v.Status(ctx)
	if !status.LegacyPasswordVault || status.OSKeychain || !status.OSKeychainAvailable {
		t.Fatalf("legacy status: %+v", status)
	}
	if store.saveCount() != 0 {
		t.Fatalf("auto unlock wrote %d keys into a legacy vault's store", store.saveCount())
	}
}

func TestAutoUnlockTreatsKeyStoreReadFailureAsManualVault(t *testing.T) {
	ctx := context.Background()
	db := vaultDB(t)
	openVault(t, db, "legacy-manual-password")

	store := newFakeKeyStore()
	store.loadErr = errors.New("keychain locked")
	v := keyStoreVault(t, db, store)
	if err := v.AutoUnlock(ctx); err != nil {
		t.Fatalf("key store read failure broke startup: %v", err)
	}
	if v.Unlocked() {
		t.Fatal("key store read failure unlocked the vault")
	}
	if !v.Status(ctx).LegacyPasswordVault {
		t.Fatal("read failure did not fall back to the password path")
	}
}

func TestAutoUnlockTreatsMismatchedStoredKeyAsLegacy(t *testing.T) {
	ctx := context.Background()
	db := vaultDB(t)
	openVault(t, db, "legacy-manual-password")

	store := newFakeKeyStore()
	store.seed("different-manual-password")
	v := keyStoreVault(t, db, store)
	if err := v.AutoUnlock(ctx); err != nil {
		t.Fatalf("mismatched key broke auto unlock: %v", err)
	}
	if v.Unlocked() {
		t.Fatal("mismatched key unlocked the vault")
	}
	if !v.Status(ctx).LegacyPasswordVault {
		t.Fatal("vault with a mismatched stored key is not marked legacy")
	}
}

func TestAutoUnlockWithoutKeyStoreStaysManual(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		store OSKeyStore
	}{
		{"absent", nil},
		{"unavailable", &fakeKeyStore{available: false, backend: "NONE"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := keyStoreVault(t, vaultDB(t), tc.store)
			if err := v.AutoUnlock(ctx); err != nil {
				t.Fatalf("auto unlock reported an error: %v", err)
			}
			if v.Unlocked() {
				t.Fatal("vault unlocked without a usable key store")
			}
			status := v.Status(ctx)
			if status.Unlocked || status.OSKeychain || status.LegacyPasswordVault || status.OSKeychainAvailable || status.OSKeychainBackend != "NONE" {
				t.Fatalf("status without a key store: %+v", status)
			}
			if err := v.Unlock(ctx, "manual-fallback-password"); err != nil {
				t.Fatalf("manual unlock path broken: %v", err)
			}
		})
	}
}

func TestManualUnlockUpgradesLegacyVaultToOSKey(t *testing.T) {
	ctx := context.Background()
	db := vaultDB(t)
	legacy := openVault(t, db, "legacy-manual-password")
	if err := legacy.Put(ctx, "provider/kimi", []byte("legacy-secret")); err != nil {
		t.Fatal(err)
	}

	store := newFakeKeyStore()
	v := keyStoreVault(t, db, store)
	if err := v.AutoUnlock(ctx); err != nil {
		t.Fatal(err)
	}
	if err := v.Unlock(ctx, "legacy-manual-password"); err != nil {
		t.Fatalf("manual unlock: %v", err)
	}
	if got := store.storedSecret(t); got != "legacy-manual-password" {
		t.Fatalf("verified password not stored: %q", got)
	}
	if !v.Status(ctx).OSKeychain {
		t.Fatal("vault does not report OS management after upgrading")
	}

	restarted := keyStoreVault(t, db, store)
	if err := restarted.AutoUnlock(ctx); err != nil {
		t.Fatalf("auto unlock after upgrade: %v", err)
	}
	value, err := restarted.Get(ctx, "provider/kimi")
	if err != nil || string(value) != "legacy-secret" {
		t.Fatalf("secret after upgrade: %q %v", value, err)
	}
	if status := restarted.Status(ctx); status.LegacyPasswordVault || !status.OSKeychain {
		t.Fatalf("status after upgrade: %+v", status)
	}
}

func TestManualUnlockSurvivesKeyStoreSaveFailure(t *testing.T) {
	ctx := context.Background()
	db := vaultDB(t)
	openVault(t, db, "legacy-manual-password")

	store := newFakeKeyStore()
	store.saveErr = errors.New("keychain unavailable")
	v := keyStoreVault(t, db, store)
	if err := v.AutoUnlock(ctx); err != nil {
		t.Fatal(err)
	}
	if err := v.Unlock(ctx, "legacy-manual-password"); err != nil {
		t.Fatalf("unlock failed because the key store is down: %v", err)
	}
	if !v.Unlocked() {
		t.Fatal("vault not unlocked")
	}
	if v.Status(ctx).OSKeychain {
		t.Fatal("vault claims OS management after a failed save")
	}
}

func TestResetRebuildsOSManagedVault(t *testing.T) {
	ctx := context.Background()
	db := vaultDB(t)
	store := newFakeKeyStore()
	v := keyStoreVault(t, db, store)
	if err := v.AutoUnlock(ctx); err != nil {
		t.Fatal(err)
	}
	oldKey := store.storedSecret(t)
	if err := v.Put(ctx, "provider/kimi", []byte("synthetic-secret")); err != nil {
		t.Fatal(err)
	}
	if err := v.BrowserStore().Put(ctx, "browser/session", []byte("browser-data")); err != nil {
		t.Fatal(err)
	}
	if err := v.KimiAccountStore().Put(ctx, KimiAccountRefPrefix+"primary", []byte("account-data")); err != nil {
		t.Fatal(err)
	}

	if err := v.Reset(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if newKey := store.storedSecret(t); newKey == oldKey || len(newKey) != 64 {
		t.Fatalf("reset kept the old key or wrote %q", newKey)
	}
	if !v.Unlocked() {
		t.Fatal("reset did not unlock the rebuilt vault")
	}
	for table, want := range map[string]int{
		"vault_meta": 1, "vault_entries": 0, "vault_browser_entries": 0, "vault_kimi_accounts": 0,
	} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != want {
			t.Fatalf("%s holds %d rows (want %d): %v", table, count, want, err)
		}
	}
	if _, err := v.Get(ctx, "provider/kimi"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old entry survived reset: %v", err)
	}
	status := v.Status(ctx)
	if !status.Unlocked || !status.OSKeychain || len(status.Refs) != 0 || status.LegacyPasswordVault {
		t.Fatalf("status after reset: %+v", status)
	}
	if err := v.Put(ctx, "provider/new", []byte("fresh")); err != nil {
		t.Fatalf("rebuilt vault rejected a write: %v", err)
	}
	restarted := keyStoreVault(t, db, store)
	if err := restarted.AutoUnlock(ctx); err != nil || !restarted.Unlocked() {
		t.Fatalf("rebuilt vault does not survive a restart: %v", err)
	}
}

func TestResetRecoversLockedLegacyVault(t *testing.T) {
	ctx := context.Background()
	db := vaultDB(t)
	legacy := openVault(t, db, "forgotten-manual-password")
	if err := legacy.Put(ctx, "provider/kimi", []byte("synthetic")); err != nil {
		t.Fatal(err)
	}
	store := newFakeKeyStore()
	v := keyStoreVault(t, db, store)
	if err := v.AutoUnlock(ctx); err != nil {
		t.Fatal(err)
	}
	if v.Unlocked() {
		t.Fatal("legacy vault unlocked before reset")
	}
	if err := v.Reset(ctx); err != nil {
		t.Fatalf("reset of a locked legacy vault: %v", err)
	}
	if !v.Unlocked() || !v.Status(ctx).OSKeychain {
		t.Fatalf("reset did not hand the vault to the OS key store: %+v", v.Status(ctx))
	}
	if _, err := v.Get(ctx, "provider/kimi"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("forgotten-password reset kept an entry: %v", err)
	}
}

func TestResetWithoutKeyStoreLocksUntilManualUnlock(t *testing.T) {
	ctx := context.Background()
	db := vaultDB(t)
	v := openVault(t, db, "manual-reset-password")
	if err := v.Put(ctx, "provider/kimi", []byte("synthetic")); err != nil {
		t.Fatal(err)
	}
	if err := v.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if v.Unlocked() {
		t.Fatal("reset without a key store left the vault unlocked")
	}
	if _, err := v.Get(ctx, "provider/kimi"); !errors.Is(err, ErrLocked) {
		t.Fatalf("reset vault served a secret while locked: %v", err)
	}
	status := v.Status(ctx)
	if status.Unlocked || status.LegacyPasswordVault || len(status.Refs) != 0 {
		t.Fatalf("status after manual reset: %+v", status)
	}
	if err := v.Unlock(ctx, "replacement-password"); err != nil {
		t.Fatalf("fresh vault cannot be initialized: %v", err)
	}
	if err := v.Put(ctx, "provider/kimi", []byte("replacement")); err != nil {
		t.Fatal(err)
	}
}

func TestResetFailureLeavesManuallyRecoverableVault(t *testing.T) {
	ctx := context.Background()
	store := newFakeKeyStore()
	v := keyStoreVault(t, vaultDB(t), store)
	if err := v.AutoUnlock(ctx); err != nil {
		t.Fatal(err)
	}
	if err := v.Put(ctx, "provider/kimi", []byte("synthetic")); err != nil {
		t.Fatal(err)
	}
	store.saveErr = errors.New("keychain unavailable")
	if err := v.Reset(ctx); err == nil {
		t.Fatal("reset reported success without persisting a new key")
	}
	if v.Unlocked() {
		t.Fatal("failed reset left the vault unlocked")
	}
	if err := v.Unlock(ctx, "manual-recovery-password"); err != nil {
		t.Fatalf("failed reset removed the manual recovery path: %v", err)
	}
}

func TestCredentialStatusAndResetAPIRoutes(t *testing.T) {
	ctx := context.Background()
	db := vaultDB(t)
	store := newFakeKeyStore()
	v := keyStoreVault(t, db, store)
	if err := v.AutoUnlock(ctx); err != nil {
		t.Fatal(err)
	}
	if err := v.Put(ctx, "provider/kimi", []byte("synthetic-secret")); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	v.Register(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := call("GET", "/enhance/v1/credentials", "")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var status VaultStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatalf("status json: %v", err)
	}
	if !status.Unlocked || !status.OSKeychain || !status.OSKeychainAvailable || status.OSKeychainBackend != "TEST_KEYCHAIN" || status.LegacyPasswordVault {
		t.Fatalf("served status: %+v", status)
	}
	for _, body := range []string{``, `{}`, `{"confirm":false}`} {
		if w = call("POST", "/enhance/v1/credentials/reset", body); w.Code != 400 {
			t.Fatalf("reset with %q answered %d: %s", body, w.Code, w.Body.String())
		}
	}
	if !v.Unlocked() {
		t.Fatal("an unconfirmed reset changed the vault")
	}
	if value, err := v.Get(ctx, "provider/kimi"); err != nil || string(value) != "synthetic-secret" {
		t.Fatalf("unconfirmed reset damaged the vault: %q %v", value, err)
	}
	w = call("POST", "/enhance/v1/credentials/reset", `{"confirm":true}`)
	if w.Code != 200 {
		t.Fatalf("confirmed reset %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "synthetic-secret") {
		t.Fatal("reset response leaked a secret")
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatalf("reset json: %v", err)
	}
	if !status.Unlocked || !status.OSKeychain || len(status.Refs) != 0 || status.LegacyPasswordVault {
		t.Fatalf("status served after reset: %+v", status)
	}
	if _, err := v.Get(ctx, "provider/kimi"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reset kept the entry: %v", err)
	}
	if store.deleteCount() == 0 {
		t.Fatal("reset did not clear the stale key store entry")
	}
}
