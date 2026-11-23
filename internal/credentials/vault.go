package credentials

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/storage"
)

var ErrLocked = errors.New("credential vault is locked")

var errVaultPassword = errors.New("cannot unlock vault")

var refPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,199}$`)

// unlockMu serializes the expensive unlock/reset paths (600k PBKDF2 rounds and
// OS key store calls). It is separate from mu so Status and Unlocked never wait
// behind a slow Keychain prompt, which is what used to stall /health.
type Vault struct {
	db        *sql.DB
	mu        sync.RWMutex
	unlockMu  sync.Mutex
	key       []byte
	keyStore  OSKeyStore
	onReset   func(context.Context) error
	osManaged bool
}

type VaultStatus struct {
	Unlocked            bool     `json:"unlocked"`
	Refs                []string `json:"refs"`
	Backend             string   `json:"backend"`
	OSKeychain          bool     `json:"osKeychain"`
	OSKeychainAvailable bool     `json:"osKeychainAvailable"`
	OSKeychainBackend   string   `json:"osKeychainBackend"`
	LegacyPasswordVault bool     `json:"legacyPasswordVault"`
}

func NewVault(db *sql.DB) (*Vault, error) {
	if err := storage.Migrate(db, "vault", []string{
		`CREATE TABLE vault_meta(id INTEGER PRIMARY KEY CHECK(id=1),salt BLOB NOT NULL,check_value BLOB NOT NULL); CREATE TABLE vault_entries(ref TEXT PRIMARY KEY,value BLOB NOT NULL);`,
		`CREATE TABLE vault_browser_entries(ref TEXT PRIMARY KEY,value BLOB NOT NULL,expires_at INTEGER NOT NULL DEFAULT 0); CREATE INDEX vault_browser_expiry ON vault_browser_entries(expires_at) WHERE expires_at > 0;`,
		`CREATE TABLE vault_kimi_accounts(ref TEXT PRIMARY KEY,value BLOB NOT NULL);`,
	}); err != nil {
		return nil, err
	}
	return &Vault{db: db}, nil
}
func seal(key, data []byte, ref string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, data, []byte(ref)), nil
}
func open(key, data []byte, ref string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(data) < gcm.NonceSize() {
		return nil, errors.New("invalid ciphertext")
	}
	return gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], []byte(ref))
}
func validVaultSecret(secret string) bool { return len(secret) >= 12 && len(secret) <= 1024 }

func (v *Vault) Unlock(ctx context.Context, password string) error {
	v.unlockMu.Lock()
	defer v.unlockMu.Unlock()
	if err := v.unlockLocked(ctx, password); err != nil {
		return err
	}
	// A manual password is the only way into a vault that predates OS key
	// management, so remembering it here is what retires the password prompt.
	// A key store failure must not turn a successful unlock into an error.
	if store := v.keyStoreRef(); store != nil && store.Available() {
		if err := store.Save(ctx, password); err == nil {
			v.mu.Lock()
			v.osManaged = true
			v.mu.Unlock()
		}
	}
	return nil
}

func (v *Vault) keyStoreRef() OSKeyStore {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.keyStore
}

func (v *Vault) unlockLocked(ctx context.Context, password string) error {
	if !validVaultSecret(password) {
		return errors.New("vault password must contain 12–1024 bytes")
	}
	var salt, check []byte
	err := v.db.QueryRowContext(ctx, `SELECT salt,check_value FROM vault_meta WHERE id=1`).Scan(&salt, &check)
	create := errors.Is(err, sql.ErrNoRows)
	if err != nil && !create {
		return err
	}
	if create {
		salt = make([]byte, 32)
		if _, err = rand.Read(salt); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, 600000, 32)
	if err != nil {
		return err
	}
	if create {
		check, err = seal(key, []byte("ekd-vault-v1"), "check")
		if err == nil {
			_, err = v.db.ExecContext(ctx, `INSERT INTO vault_meta VALUES(1,?,?)`, salt, check)
		}
	} else {
		_, err = open(key, check, "check")
	}
	if err != nil {
		clear(key)
		return errVaultPassword
	}
	v.mu.Lock()
	clear(v.key)
	v.key = key
	v.mu.Unlock()
	return nil
}

func (v *Vault) Lock() {
	v.mu.Lock()
	defer v.mu.Unlock()
	clear(v.key)
	v.key = nil
}

func (v *Vault) UseKeyStore(store OSKeyStore) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.keyStore = store
}

func (v *Vault) UseOnReset(callback func(context.Context) error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.onReset = callback
}

func (v *Vault) metaExists(ctx context.Context) (bool, error) {
	var one int
	err := v.db.QueryRowContext(ctx, `SELECT 1 FROM vault_meta WHERE id=1`).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// AutoUnlock opens the vault with the key kept by the OS. It is safe to call on
// every start: without a usable key store, or when the stored key does not open
// an existing vault, the vault stays locked and waits for a manual password.
func (v *Vault) AutoUnlock(ctx context.Context) error {
	v.unlockMu.Lock()
	defer v.unlockMu.Unlock()
	if v.Unlocked() {
		return nil
	}
	store := v.keyStoreRef()
	if store == nil || !store.Available() {
		return nil
	}
	initialized, err := v.metaExists(ctx)
	if err != nil {
		return err
	}
	secret, found, loadErr := store.Load(ctx)
	if !initialized {
		if loadErr != nil || !found || !validVaultSecret(secret) {
			if secret, err = randomVaultSecret(); err != nil {
				return err
			}
			if err = store.Save(ctx, secret); err != nil {
				return err
			}
		}
		if err = v.unlockLocked(ctx, secret); err != nil {
			return err
		}
		v.mu.Lock()
		v.osManaged = true
		v.mu.Unlock()
		return nil
	}
	if loadErr != nil || !found || !validVaultSecret(secret) {
		return nil
	}
	if err = v.unlockLocked(ctx, secret); err != nil {
		if errors.Is(err, errVaultPassword) {
			return nil
		}
		return err
	}
	v.mu.Lock()
	v.osManaged = true
	v.mu.Unlock()
	return nil
}

// Reset discards every stored secret and starts a fresh key, which is the only
// recovery path for a forgotten manual password. With an OS key store the new
// vault is unlocked again in the same call.
func (v *Vault) Reset(ctx context.Context) (err error) {
	v.unlockMu.Lock()
	defer v.unlockMu.Unlock()
	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, stmt := range []string{
		`DELETE FROM vault_meta`,
		`DELETE FROM vault_entries`,
		`DELETE FROM vault_browser_entries`,
		`DELETE FROM vault_kimi_accounts`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	v.mu.RLock()
	onReset := v.onReset
	v.mu.RUnlock()
	// Once encrypted rows are gone, dependent account records must not survive
	// even if recreating the OS-managed key later fails.
	defer func() {
		if onReset != nil {
			err = errors.Join(err, onReset(ctx))
		}
	}()
	v.mu.Lock()
	clear(v.key)
	v.key = nil
	v.osManaged = false
	store := v.keyStore
	v.mu.Unlock()
	if store != nil {
		_ = store.Delete(ctx)
	}
	if store == nil || !store.Available() {
		return nil
	}
	secret, err := randomVaultSecret()
	if err != nil {
		return err
	}
	if err := store.Save(ctx, secret); err != nil {
		return err
	}
	if err := v.unlockLocked(ctx, secret); err != nil {
		return err
	}
	v.mu.Lock()
	v.osManaged = true
	v.mu.Unlock()
	return nil
}

// Status reports vault metadata without disclosing secrets. LegacyPasswordVault
// marks an initialized vault that stays locked because no OS key opened it, so
// the UI can offer the password prompt or the reset path.
func (v *Vault) Status(ctx context.Context) VaultStatus {
	status, _ := v.status(ctx)
	return status
}

func (v *Vault) status(ctx context.Context) (VaultStatus, error) {
	v.mu.RLock()
	unlocked := len(v.key) > 0
	osManaged := v.osManaged
	store := v.keyStore
	v.mu.RUnlock()
	refs := []string{}
	rows, err := v.db.QueryContext(ctx, `SELECT ref FROM vault_entries ORDER BY ref`)
	if err != nil {
		return VaultStatus{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var ref string
		if err = rows.Scan(&ref); err != nil {
			return VaultStatus{}, err
		}
		refs = append(refs, ref)
	}
	if err = rows.Err(); err != nil {
		return VaultStatus{}, err
	}
	initialized, err := v.metaExists(ctx)
	if err != nil {
		return VaultStatus{}, err
	}
	backend := "NONE"
	if store != nil {
		backend = store.BackendName()
	}
	return VaultStatus{
		Unlocked:            unlocked,
		Refs:                refs,
		Backend:             "AES256_GCM_PBKDF2",
		OSKeychain:          osManaged,
		OSKeychainAvailable: store != nil && store.Available(),
		OSKeychainBackend:   backend,
		LegacyPasswordVault: initialized && !unlocked && !osManaged,
	}, nil
}

// Unlocked reports whether this process currently holds the vault key. It says
// nothing about which entries exist, so it is safe to show a plugin.
func (v *Vault) Unlocked() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.key) > 0
}

// keyCopy returns a private copy of the vault key so sealing and opening never
// hold mu across a caller's database work or a Keychain round trip.
func (v *Vault) keyCopy() []byte {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if len(v.key) == 0 {
		return nil
	}
	key := make([]byte, len(v.key))
	copy(key, v.key)
	return key
}

// Exists reports whether an entry is stored, without decrypting it and without
// requiring the vault to be unlocked: a reference name is not a secret, and a
// locked vault must still be able to tell a plugin that its credential is
// present but unusable until the user unlocks.
func (v *Vault) Exists(ctx context.Context, ref string) (bool, error) {
	var one int
	err := v.db.QueryRowContext(ctx, `SELECT 1 FROM vault_entries WHERE ref=?`, ref).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
func (v *Vault) Put(ctx context.Context, ref string, value []byte) error {
	return v.put(ctx, ref, value, true)
}
func (v *Vault) PutNew(ctx context.Context, ref string, value []byte) error {
	return v.put(ctx, ref, value, false)
}
func (v *Vault) put(ctx context.Context, ref string, value []byte, replace bool) error {
	key := v.keyCopy()
	if len(key) == 0 {
		return ErrLocked
	}
	defer clear(key)
	if !refPattern.MatchString(ref) || len(value) == 0 || len(value) > 32768 {
		return errors.New("invalid credential")
	}
	data, err := seal(key, value, ref)
	if err != nil {
		return err
	}
	if replace {
		_, err = v.db.ExecContext(ctx, `INSERT INTO vault_entries VALUES(?,?) ON CONFLICT(ref) DO UPDATE SET value=excluded.value`, ref, data)
		return err
	}
	_, err = v.db.ExecContext(ctx, `INSERT INTO vault_entries VALUES(?,?)`, ref, data)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrAlreadyExists
	}
	return err
}
func (v *Vault) Get(ctx context.Context, ref string) ([]byte, error) {
	key := v.keyCopy()
	if len(key) == 0 {
		return nil, ErrLocked
	}
	defer clear(key)
	var data []byte
	err := v.db.QueryRowContext(ctx, `SELECT value FROM vault_entries WHERE ref=?`, ref).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return open(key, data, ref)
}
func (v *Vault) Delete(ctx context.Context, ref string) error {
	if !v.Unlocked() {
		return ErrLocked
	}
	_, err := v.db.ExecContext(ctx, `DELETE FROM vault_entries WHERE ref=?`, ref)
	return err
}
func (v *Vault) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /enhance/v1/credentials", func(w http.ResponseWriter, r *http.Request) {
		status, err := v.status(r.Context())
		if err != nil {
			httpx.Fail(w, 500, "VAULT_STORAGE", "无法读取凭据库")
			return
		}
		httpx.JSON(w, 200, status)
	})
	mux.HandleFunc("POST /enhance/v1/credentials/unlock", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Password string `json:"password"`
		}
		if !httpx.Decode(w, r, &in) {
			return
		}
		if err := v.Unlock(r.Context(), in.Password); err != nil {
			httpx.Fail(w, 422, "VAULT_LOCKED", "无法解锁。首次设置需至少 12 字节口令，已有库需原口令。")
			return
		}
		httpx.JSON(w, 200, map[string]bool{"unlocked": true})
	})
	mux.HandleFunc("POST /enhance/v1/credentials/lock", func(w http.ResponseWriter, r *http.Request) {
		v.Lock()
		httpx.JSON(w, 200, map[string]bool{"unlocked": false})
	})
	mux.HandleFunc("POST /enhance/v1/credentials/reset", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Confirm bool `json:"confirm"`
		}
		if !httpx.Decode(w, r, &in) {
			return
		}
		if !in.Confirm {
			httpx.Fail(w, 400, "VAULT_RESET_CONFIRM_REQUIRED", "重置凭据库会删除全部已存密钥，请确认后重试")
			return
		}
		if err := v.Reset(r.Context()); err != nil {
			httpx.Fail(w, 500, "VAULT_RESET", "无法重置凭据库")
			return
		}
		status, err := v.status(r.Context())
		if err != nil {
			httpx.Fail(w, 500, "VAULT_STORAGE", "无法读取凭据库")
			return
		}
		httpx.JSON(w, 200, status)
	})
	mux.HandleFunc("PUT /enhance/v1/credentials", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Ref   string `json:"ref"`
			Value string `json:"value"`
		}
		if !httpx.Decode(w, r, &in) {
			return
		}
		if err := v.Put(r.Context(), in.Ref, []byte(in.Value)); err != nil {
			httpx.Fail(w, 422, "VAULT_WRITE", "请先解锁，并提供有效引用和非空密钥")
			return
		}
		httpx.JSON(w, 200, map[string]string{"ref": in.Ref})
	})
	mux.HandleFunc("DELETE /enhance/v1/credentials/{ref...}", func(w http.ResponseWriter, r *http.Request) {
		if err := v.Delete(r.Context(), r.PathValue("ref")); err != nil {
			httpx.Fail(w, 423, "VAULT_LOCKED", "请先解锁凭据库")
			return
		}
		w.WriteHeader(204)
	})
}
