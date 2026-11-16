package credentials

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
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
