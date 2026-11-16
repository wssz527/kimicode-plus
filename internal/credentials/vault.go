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
var refPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,199}$`)

type Vault struct {
	db  *sql.DB
	mu  sync.Mutex
	key []byte
}

func NewVault(db *sql.DB) (*Vault, error) {
	if err := storage.Migrate(db, "vault", []string{
		`CREATE TABLE vault_meta(id INTEGER PRIMARY KEY CHECK(id=1),salt BLOB NOT NULL,check_value BLOB NOT NULL); CREATE TABLE vault_entries(ref TEXT PRIMARY KEY,value BLOB NOT NULL);`,
		`CREATE TABLE vault_browser_entries(ref TEXT PRIMARY KEY,value BLOB NOT NULL,expires_at INTEGER NOT NULL DEFAULT 0); CREATE INDEX vault_browser_expiry ON vault_browser_entries(expires_at) WHERE expires_at > 0;`,
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
func (v *Vault) Unlock(ctx context.Context, password string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(password) < 12 || len(password) > 1024 {
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
		return errors.New("cannot unlock vault")
	}
	clear(v.key)
	v.key = key
	return nil
}
func (v *Vault) Lock() { v.mu.Lock(); defer v.mu.Unlock(); clear(v.key); v.key = nil }
func (v *Vault) Put(ctx context.Context, ref string, value []byte) error {
	return v.put(ctx, ref, value, true)
}
func (v *Vault) PutNew(ctx context.Context, ref string, value []byte) error {
	return v.put(ctx, ref, value, false)
}
func (v *Vault) put(ctx context.Context, ref string, value []byte, replace bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.key) == 0 {
		return ErrLocked
	}
	if !refPattern.MatchString(ref) || len(value) == 0 || len(value) > 32768 {
		return errors.New("invalid credential")
	}
	data, err := seal(v.key, value, ref)
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
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.key) == 0 {
		return nil, ErrLocked
	}
	var data []byte
	err := v.db.QueryRowContext(ctx, `SELECT value FROM vault_entries WHERE ref=?`, ref).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return open(v.key, data, ref)
}
func (v *Vault) Delete(ctx context.Context, ref string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.key) == 0 {
		return ErrLocked
	}
	_, err := v.db.ExecContext(ctx, `DELETE FROM vault_entries WHERE ref=?`, ref)
	return err
}
func (v *Vault) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /enhance/v1/credentials", func(w http.ResponseWriter, r *http.Request) {
		v.mu.Lock()
		defer v.mu.Unlock()
		refs := []string{}
		rows, err := v.db.QueryContext(r.Context(), `SELECT ref FROM vault_entries ORDER BY ref`)
		if err != nil {
			httpx.Fail(w, 500, "VAULT_STORAGE", "无法读取凭据库")
			return
		}
		defer rows.Close()
		for rows.Next() {
			var ref string
			if err = rows.Scan(&ref); err != nil {
				break
			}
			refs = append(refs, ref)
		}
		if err != nil || rows.Err() != nil {
			httpx.Fail(w, 500, "VAULT_STORAGE", "无法读取凭据库")
			return
		}
		httpx.JSON(w, 200, map[string]any{"unlocked": len(v.key) > 0, "refs": refs, "backend": "AES256_GCM_PBKDF2", "osKeychain": false})
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
