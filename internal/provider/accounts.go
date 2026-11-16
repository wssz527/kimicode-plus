package provider

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"enhancedkimi/internal/credentials"
	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/security"
	"enhancedkimi/internal/storage"
)

// Account is one credential slot under a provider. Several accounts can share a
// provider endpoint; each carries its own secret reference, so rotating one
// account never rewrites provider configuration.
//
// SecretRef is never serialized: callers see RedactedRef instead, keeping the
// same non-disclosure rule as the credential API.
type Account struct {
	ID          string `json:"id"`
	ProviderID  string `json:"providerId"`
	Label       string `json:"label"`
	SecretRef   string `json:"-"`
	RedactedRef string `json:"secretRef"`
	Enabled     bool   `json:"enabled"`
	Priority    int    `json:"priority"`
	Weight      int    `json:"weight"`
	Imported    bool   `json:"imported"`
	Revision    int    `json:"revision"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

var accountIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Service owns provider account storage. It shares the database with Client so
// account resolution and credential use read the same rows.
type Service struct {
	db  *sql.DB
	now func() time.Time
}

func NewService(db *sql.DB) (*Service, error) {
	s := &Service{db: db, now: func() time.Time { return time.Now().UTC() }}
	if err := s.initAccounts(); err != nil {
		return nil, err
	}
	return s, nil
}

// validSecretRef rejects a value that looks like a raw credential instead of a
// vault reference, so the API cannot be used to bypass the vault.
func validSecretRef(ref string) error {
	if !credentials.ValidRef(ref) || looksLikeSecret(ref) || strings.Contains(ref, "..") {
		return badRequest("INVALID_SECRET_REF", "secretRef 必须是凭据库引用，不能内联密钥或包含路径穿越")
	}
	return nil
}

func (s *Service) initAccounts() error {
	return storage.Migrate(s.db, "provider_accounts", []string{
		`CREATE TABLE IF NOT EXISTS provider_accounts(
			id TEXT PRIMARY KEY,
			provider_id TEXT NOT NULL REFERENCES registry_providers(id) ON DELETE CASCADE,
			label TEXT NOT NULL,
			secret_ref TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			priority INTEGER NOT NULL,
			weight INTEGER NOT NULL,
			imported INTEGER NOT NULL DEFAULT 0,
			revision INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS provider_accounts_ref ON provider_accounts(secret_ref)`,
		`CREATE INDEX IF NOT EXISTS provider_accounts_order ON provider_accounts(provider_id, priority, created_at)`,
	})
}

func (s *Service) clock() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now()
}

// accountsMissing reports whether the accounts migration never ran, which can
// happen for a database opened by an older build. Such a provider keeps the
// legacy provider.secret_ref path instead of failing every call.
func accountsMissing(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

func (s *Service) accountRows(ctx context.Context, providerID string) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,provider_id,label,secret_ref,enabled,priority,weight,imported,revision,created_at,updated_at FROM provider_accounts WHERE provider_id=? ORDER BY priority,created_at,id`, providerID)
	if accountsMissing(err) {
		return []Account{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		var a Account
		var enabled, imported int
		if err := rows.Scan(&a.ID, &a.ProviderID, &a.Label, &a.SecretRef, &enabled, &a.Priority, &a.Weight, &imported, &a.Revision, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		a.Enabled = enabled == 1
		a.Imported = imported == 1
		a.RedactedRef = a.SecretRef
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Service) loadAccount(ctx context.Context, providerID, id string) (Account, bool, error) {
	var a Account
	var enabled, imported int
	err := s.db.QueryRowContext(ctx, `SELECT id,provider_id,label,secret_ref,enabled,priority,weight,imported,revision,created_at,updated_at FROM provider_accounts WHERE provider_id=? AND id=?`, providerID, id).
		Scan(&a.ID, &a.ProviderID, &a.Label, &a.SecretRef, &enabled, &a.Priority, &a.Weight, &imported, &a.Revision, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, false, nil
	}
	if err != nil {
		return Account{}, false, err
	}
	a.Enabled = enabled == 1
	a.Imported = imported == 1
	a.RedactedRef = a.SecretRef
	return a, true, nil
}

// EffectiveAccount is the account a call should use. A provider without any
// account row still works through its legacy provider.secret_ref, so existing
// configuration keeps behaving exactly as before.
type EffectiveAccount struct {
	AccountID string  `json:"accountId"`
	SecretRef *string `json:"secretRef"`
	Source    string  `json:"source"`
}

const (
	AccountSourceExplicit = "ACCOUNT"
	AccountSourceLegacy   = "PROVIDER_SECRET_REF"
	AccountSourceNone     = "NONE"
)

type accountInput struct {
	ID        string  `json:"id"`
	Label     string  `json:"label"`
	SecretRef *string `json:"secretRef"`
	Enabled   *bool   `json:"enabled"`
	Priority  *int    `json:"priority"`
	Weight    *int    `json:"weight"`
	Revision  *int    `json:"revision"`
}

func normalizeAccountInput(in accountInput) (Account, error) {
	label := strings.TrimSpace(in.Label)
	if label == "" || len(label) > 100 || strings.ContainsAny(label, "\r\n\x00") {
		return Account{}, badRequest("INVALID_LABEL", "账号名称不能为空、不能含换行且最长 100 字符")
	}
	if in.SecretRef == nil {
		return Account{}, badRequest("MISSING_SECRET_REF", "必须提供凭据库引用")
	}
	ref := strings.TrimSpace(*in.SecretRef)
	if err := validSecretRef(ref); err != nil {
		return Account{}, err
	}
	priority := 0
	if in.Priority != nil {
		priority = *in.Priority
	}
	if priority < 0 || priority > 1000 {
		return Account{}, badRequest("INVALID_PRIORITY", "priority 必须在 0–1000")
	}
	weight := 100
	if in.Weight != nil {
		weight = *in.Weight
	}
	if weight < 0 || weight > 10000 {
		return Account{}, badRequest("INVALID_WEIGHT", "weight 必须在 0–10000")
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	return Account{Label: label, SecretRef: ref, Enabled: enabled, Priority: priority, Weight: weight}, nil
}

// Accounts lists one provider's accounts plus how a call resolves today.
func (s *Service) Accounts(ctx context.Context, providerID string) ([]Account, EffectiveAccount, error) {
	if _, ok, err := loadProviderRow(ctx, s.db, providerID); err != nil {
		return nil, EffectiveAccount{}, err
	} else if !ok {
		return nil, EffectiveAccount{}, notFound("PROVIDER_NOT_FOUND", "供应商 %q 不存在", providerID)
	}
	items, err := s.accountRows(ctx, providerID)
	if err != nil {
		return nil, EffectiveAccount{}, err
	}
	effective, err := s.resolveAccount(ctx, providerID)
	if err != nil {
		return nil, EffectiveAccount{}, err
	}
	return items, effective, nil
}

// resolveAccount picks the account for a provider. Disabled accounts are never
// used. Selection is deterministic: lowest priority, then creation order.
// Multi-account scheduling and failover are a later stage; ordering here only
// makes the single-account choice explicit and stable.
func (s *Service) resolveAccount(ctx context.Context, providerID string) (EffectiveAccount, error) {
	items, err := s.accountRows(ctx, providerID)
	if err != nil {
		return EffectiveAccount{}, err
	}
	if len(items) == 0 {
		provider, ok, err := loadProviderRow(ctx, s.db, providerID)
		if err != nil {
			return EffectiveAccount{}, err
		}
		if !ok {
			return EffectiveAccount{}, notFound("PROVIDER_NOT_FOUND", "供应商 %q 不存在", providerID)
		}
		if provider.SecretRef != nil && *provider.SecretRef != "" {
			ref := *provider.SecretRef
			return EffectiveAccount{SecretRef: &ref, Source: AccountSourceLegacy}, nil
		}
		return EffectiveAccount{Source: AccountSourceNone}, nil
	}
	for _, item := range items {
		if item.Enabled {
			ref := item.SecretRef
			return EffectiveAccount{AccountID: item.ID, SecretRef: &ref, Source: AccountSourceExplicit}, nil
		}
	}
	// Every account exists but none is enabled: this is not the same as "no
	// credential configured", so it must not silently fall back to the legacy
	// provider reference.
	return EffectiveAccount{Source: AccountSourceNone}, nil
}

// ResolveAccount returns the credential reference for one provider, preferring
// an explicit account. The boolean reports whether any reference was found.
func (c *Client) ResolveAccount(ctx context.Context, id string) (EffectiveAccount, error) {
	s := &Service{db: c.DB}
	return s.resolveAccount(ctx, id)
}

// EffectiveConfig loads a provider config already pointed at the account that
// should serve the call. Callers that need the raw provider row use Config.
func (c *Client) EffectiveConfig(ctx context.Context, id string) (Config, EffectiveAccount, error) {
	p, err := c.Config(ctx, id)
	if err != nil {
		return Config{}, EffectiveAccount{}, err
	}
	effective, err := c.ResolveAccount(ctx, id)
	if err != nil {
		return Config{}, EffectiveAccount{}, err
	}
	return p.WithSecret(effective.SecretRef), effective, nil
}

func (s *Service) CreateAccount(ctx context.Context, providerID string, in accountInput) (Account, error) {
	if _, ok, err := loadProviderRow(ctx, s.db, providerID); err != nil {
		return Account{}, err
	} else if !ok {
		return Account{}, notFound("PROVIDER_NOT_FOUND", "供应商 %q 不存在", providerID)
	}
	prepared, err := normalizeAccountInput(in)
	if err != nil {
		return Account{}, err
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		id = "acct-" + security.Random()[:16]
	}
	if !accountIDPattern.MatchString(id) {
		return Account{}, badRequest("INVALID_ACCOUNT_ID", "账号 ID 只能包含小写字母、数字、点、下划线和短横线")
	}
	now := s.clock().UTC().Format(time.RFC3339Nano)
	prepared.ID = id
	prepared.ProviderID = providerID
	prepared.Revision = 1
	prepared.CreatedAt = now
	prepared.UpdatedAt = now
	prepared.RedactedRef = prepared.SecretRef
	err = storage.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO provider_accounts(id,provider_id,label,secret_ref,enabled,priority,weight,imported,revision,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			prepared.ID, providerID, prepared.Label, prepared.SecretRef, boolInt(prepared.Enabled), prepared.Priority, prepared.Weight, boolInt(prepared.Imported), prepared.Revision, now, now)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "PRIMARY KEY") {
				return conflict("ACCOUNT_CONFLICT", "账号 ID 或凭据引用已被占用")
			}
			return err
		}
		return nil
	})
	if err != nil {
		return Account{}, err
	}
	return prepared, nil
}

// UpdateAccount is a full replacement guarded by revision. The account ID and
// provider are immutable; the secret reference can be rotated in place.
func (s *Service) UpdateAccount(ctx context.Context, providerID, id string, in accountInput) (Account, error) {
	prepared, err := normalizeAccountInput(in)
	if err != nil {
		return Account{}, err
	}
	if in.Revision == nil {
		return Account{}, badRequest("MISSING_REVISION", "更新账号必须提供当前 revision")
	}
	if strings.TrimSpace(in.ID) != "" && strings.TrimSpace(in.ID) != id {
		return Account{}, badRequest("ACCOUNT_ID_IMMUTABLE", "账号 ID 不能修改")
	}
	var result Account
	err = storage.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		var revision int
		var createdAt string
		err := tx.QueryRowContext(ctx, `SELECT revision,created_at FROM provider_accounts WHERE provider_id=? AND id=?`, providerID, id).Scan(&revision, &createdAt)
		if errors.Is(err, sql.ErrNoRows) {
			return notFound("ACCOUNT_NOT_FOUND", "账号不存在")
		}
		if err != nil {
			return err
		}
		if revision != *in.Revision {
			return conflict("STALE_REVISION", "revision %d 已过期，当前为 %d", *in.Revision, revision)
		}
		now := s.clock().UTC().Format(time.RFC3339Nano)
		_, err = tx.ExecContext(ctx, `UPDATE provider_accounts SET label=?,secret_ref=?,enabled=?,priority=?,weight=?,revision=?,updated_at=? WHERE provider_id=? AND id=?`,
			prepared.Label, prepared.SecretRef, boolInt(prepared.Enabled), prepared.Priority, prepared.Weight, revision+1, now, providerID, id)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return conflict("ACCOUNT_CONFLICT", "凭据引用已被其他账号占用")
			}
			return err
		}
		result = prepared
		result.ID = id
		result.ProviderID = providerID
		result.Revision = revision + 1
		result.CreatedAt = createdAt
		result.UpdatedAt = now
		result.RedactedRef = prepared.SecretRef
		return nil
	})
	if err != nil {
		return Account{}, err
	}
	return result, nil
}

// DeleteAccount removes one account and reports the resulting resolution. The
// vault entry is intentionally left in place: deleting configuration must not
// destroy ciphertext the user may still need to audit.
func (s *Service) DeleteAccount(ctx context.Context, providerID, id string) (EffectiveAccount, []Account, error) {
	var remaining []Account
	var effective EffectiveAccount
	err := storage.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM provider_accounts WHERE provider_id=? AND id=?`, providerID, id)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return notFound("ACCOUNT_NOT_FOUND", "账号不存在")
		}
		return nil
	})
	if err != nil {
		return EffectiveAccount{}, nil, err
	}
	remaining, err = s.accountRows(ctx, providerID)
	if err != nil {
		return EffectiveAccount{}, nil, err
	}
	effective, err = s.resolveAccount(ctx, providerID)
	if err != nil {
		return EffectiveAccount{}, nil, err
	}
	return effective, remaining, nil
}

// AccountInputForTest builds a minimal valid create request. Tests in other
// packages use it instead of duplicating the unexported input type.
func AccountInputForTest(secretRef string) accountInput {
	return accountInput{Label: "synthetic", SecretRef: &secretRef}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// ImportContract documents the only official-login path this build supports:
// the user names a file, the core reads it, and nothing is written back.
type ImportContract struct {
	Source         string   `json:"source"`
	Mode           string   `json:"mode"`
	MaxBytes       int      `json:"maxBytes"`
	AcceptedKeys   []string `json:"acceptedKeys"`
	IgnoredKeys    []string `json:"ignoredKeys"`
	WritesOfficial bool     `json:"writesOfficial"`
	ReadsAmbient   bool     `json:"readsAmbientCredentials"`
	Note           string   `json:"note"`
}

func importContract() ImportContract {
	return ImportContract{
		Source:         "EXPLICIT_USER_FILE",
		Mode:           "READ_ONLY",
		MaxBytes:       importMaxBytes,
		AcceptedKeys:   []string{"apiKey", "api_key", "accessToken", "access_token", "refreshToken", "refresh_token", "expiresAt", "expires_at"},
		IgnoredKeys:    []string{"account", "email", "userId", "deviceId", "region", "baseUrl"},
		WritesOfficial: false,
		ReadsAmbient:   false,
		Note:           "只读取用户显式给出的文件；不访问官方存储、不读取环境变量、不回写。刷新令牌仅记录存在性，其语义未验证。",
	}
}

// ImportPreview is the parsed result. Values are never included, only whether a
// field was present, so a preview cannot leak a credential.
type ImportPreview struct {
	Path         string   `json:"path"`
	Bytes        int      `json:"bytes"`
	Fingerprint  string   `json:"fingerprint"`
	Fields       []string `json:"fields"`
	Unknown      []string `json:"unknownFields"`
	HasAPIKey    bool     `json:"hasApiKey"`
	HasTokenPair bool     `json:"hasTokenPair"`
	ExpiresAt    *string  `json:"expiresAt"`
}

func (p ImportPreview) validate() error {
	if p.Path == "" || !strings.HasPrefix(p.Path, "/") {
		return badRequest("INVALID_PATH", "必须提供绝对文件路径")
	}
	if len(p.Path) > 1024 || strings.ContainsAny(p.Path, "\r\n\x00") {
		return badRequest("INVALID_PATH", "路径过长或包含控制字符")
	}
	return nil
}

func (s *Service) importAccounts(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, 200, importContract())
}

func (s *Service) listAccounts(w http.ResponseWriter, r *http.Request) {
	items, effective, err := s.Accounts(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"items": items, "total": len(items), "effective": effective})
}

func (s *Service) createAccount(w http.ResponseWriter, r *http.Request) {
	var in accountInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	account, err := s.CreateAccount(r.Context(), r.PathValue("id"), in)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, 201, account)
}

func (s *Service) updateAccount(w http.ResponseWriter, r *http.Request) {
	var in accountInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	account, err := s.UpdateAccount(r.Context(), r.PathValue("id"), r.PathValue("accountId"), in)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, 200, account)
}

func (s *Service) deleteAccount(w http.ResponseWriter, r *http.Request) {
	effective, remaining, err := s.DeleteAccount(r.Context(), r.PathValue("id"), r.PathValue("accountId"))
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"effective": effective, "items": remaining, "total": len(remaining)})
}

// RegisterAccounts exposes provider accounts and the read-only import
// boundary. It needs the vault, because an imported credential is stored there
// and never in provider configuration.
func (s *Service) RegisterAccounts(mux *http.ServeMux, secrets credentials.InsertStore) {
	if err := s.initAccounts(); err != nil {
		return
	}
	mux.HandleFunc("GET /enhance/v1/providers/{id}/accounts", s.listAccounts)
	mux.HandleFunc("POST /enhance/v1/providers/{id}/accounts", s.createAccount)
	mux.HandleFunc("PUT /enhance/v1/providers/{id}/accounts/{accountId}", s.updateAccount)
	mux.HandleFunc("DELETE /enhance/v1/providers/{id}/accounts/{accountId}", s.deleteAccount)
	mux.HandleFunc("GET /enhance/v1/accounts/import-contract", s.importAccounts)
	mux.HandleFunc("POST /enhance/v1/accounts/import/preview", s.previewImport)
	mux.HandleFunc("POST /enhance/v1/providers/{id}/accounts/import", s.applyImport(secrets))
}
