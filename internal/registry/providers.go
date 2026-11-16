package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"enhancedkimi/internal/storage"
)

type Service struct {
	catalogMu sync.Mutex
	db        *sql.DB
	now       func() time.Time
}

func New(db *sql.DB) (*Service, error) {
	if db == nil {
		return nil, notFound("NO_DATABASE", "数据库未初始化")
	}
	s := &Service{db: db, now: func() time.Time { return time.Now().UTC() }}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

var providerIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type providerPreset struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"baseUrl"`
	Note     string `json:"note"`
	Official bool   `json:"official"`
}

type Provider struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Protocol  string  `json:"protocol"`
	BaseURL   string  `json:"baseUrl"`
	SecretRef *string `json:"secretRef"`
	Preset    bool    `json:"preset"`
	CreatedAt string  `json:"createdAt"`
}

type providerInput struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Protocol  string  `json:"protocol"`
	BaseURL   string  `json:"baseUrl"`
	SecretRef *string `json:"secretRef"`
}

var protocolNames = []string{"openai", "responses", "anthropic", "gemini"}

var providerPresets = []providerPreset{
	{ID: "kimi", Name: "Kimi", Protocol: "openai", BaseURL: "https://api.moonshot.cn/v1", Note: "OpenAI 兼容协议；具体模型 ID 由 Mock 目录或手动导入", Official: true},
	{ID: "anthropic", Name: "Anthropic", Protocol: "anthropic", BaseURL: "https://api.anthropic.com", Note: "Anthropic Messages 协议", Official: true},
	{ID: "openai", Name: "OpenAI", Protocol: "openai", BaseURL: "https://api.openai.com/v1", Note: "Chat Completions；Responses 需另建 responses 预设", Official: true},
	{ID: "openai-responses", Name: "OpenAI Responses", Protocol: "responses", BaseURL: "https://api.openai.com/v1", Note: "Responses API 协议", Official: true},
	{ID: "google", Name: "Google Gemini", Protocol: "gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta", Note: "Gemini generateContent 协议", Official: true},
	{ID: "local", Name: "本地推理/网关", Protocol: "openai", BaseURL: "http://127.0.0.1:11434/v1", Note: "仅保存地址；明确允许 loopback，不自动外联", Official: false},
}

func (s *Service) ProviderPresets() []providerPreset {
	out := make([]providerPreset, len(providerPresets))
	copy(out, providerPresets)
	return out
}

func (s *Service) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,protocol,base_url,secret_ref,created_at FROM registry_providers ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Provider{}
	presetIDs := map[string]bool{}
	for _, preset := range providerPresets {
		presetIDs[preset.ID] = true
	}
	for rows.Next() {
		var p Provider
		var secret sql.NullString
		if err := rows.Scan(&p.ID, &p.Name, &p.Protocol, &p.BaseURL, &secret, &p.CreatedAt); err != nil {
			return nil, err
		}
		if secret.Valid {
			p.SecretRef = &secret.String
		}
		p.Preset = presetIDs[p.ID]
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Service) CreateProvider(ctx context.Context, in providerInput) (Provider, error) {
	if err := validateIdentity(in.ID, in.ID); err != nil {
		return Provider{}, badRequest("INVALID_PROVIDER_ID", "供应商 ID %q 只能包含小写字母、数字、点、下划线和短横线", in.ID)
	}
	p, err := s.prepareProvider(in, in.ID)
	if err != nil {
		return Provider{}, err
	}
	p.CreatedAt = s.now().UTC().Format(time.RFC3339Nano)
	for _, preset := range providerPresets {
		if preset.ID == p.ID {
			p.Preset = true
		}
	}
	err = storage.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		base, err := s.readRevision(tx)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO registry_providers(id,name,protocol,base_url,secret_ref,created_at) VALUES(?,?,?,?,?,?)`, p.ID, p.Name, p.Protocol, p.BaseURL, nullableString(p.SecretRef), p.CreatedAt); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") || strings.Contains(err.Error(), "PRIMARY KEY") {
				return conflict("PROVIDER_EXISTS", "供应商 %q 已存在", p.ID)
			}
			return err
		}
		revision, err := s.bumpRevision(tx)
		if err != nil {
			return err
		}
		changeID, err := s.writeChange(tx, "provider-create", base, revision, p.CreatedAt, fmt.Sprintf("创建供应商 %s", p.ID), nil)
		if err != nil {
			return err
		}
		after, err := json.Marshal(p)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO registry_provider_changes(change_id,provider_id,before_json,after_json) VALUES(?,?,NULL,?)`, changeID, p.ID, string(after))
		return err
	})
	if err != nil {
		return Provider{}, err
	}
	return p, nil
}

func looksLikeSecret(value string) bool {
	if len(value) < 16 {
		return false
	}
	for _, prefix := range sensitiveValuePrefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func validateBaseURL(raw string) error {
	if raw == "" {
		return badRequest("INVALID_BASE_URL", "baseUrl 不能为空")
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return badRequest("INVALID_SOURCE_URL", "端点必须使用小写 http/https 协议")
	}
	if len(raw) > 500 {
		return badRequest("INVALID_BASE_URL", "baseUrl 最长 500 字符")
	}
	if err := validateReferenceURL(raw); err != nil {
		return err
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.Opaque != "" || strings.ContainsAny(raw, " \t\r\n\\") {
		return badRequest("INVALID_BASE_URL", "请输入完整 HTTPS 或 loopback HTTP 地址")
	}
	if u.User != nil {
		return badRequest("CREDENTIALS_IN_URL", "地址不能包含凭据")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return badRequest("INVALID_BASE_URL", "端点不能包含查询参数或 fragment")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return badRequest("INVALID_BASE_URL", "仅接受 HTTPS 或 loopback HTTP")
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return badRequest("INVALID_BASE_URL", "端口必须为 1–65535")
		}
	}

	return nil
}

// ListModels reads the page and its capabilities in one read transaction, so a
// concurrent rollback or import cannot delete a key between the two steps and
// leave a nil record to render.
func (s *Service) ListModels(ctx context.Context, offset, limit int) ([]Model, int, error) {
	var items []Model
	var total int
	err := storage.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM registry_models`).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT provider_id, model_id FROM registry_models ORDER BY provider_id, model_id LIMIT ? OFFSET ?`, limit, offset)
		if err != nil {
			return err
		}
		var keys []modelKey
		for rows.Next() {
			var key modelKey
			if err := rows.Scan(&key.ProviderID, &key.ModelID); err != nil {
				rows.Close()
				return err
			}
			keys = append(keys, key)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		records, err := s.loadModels(tx, keys)
		if err != nil {
			return err
		}
		items = make([]Model, 0, len(keys))
		for _, key := range keys {
			items = append(items, recordToModel(records[key]))
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

type ProviderSnapshot struct {
	ProviderID string                `json:"providerId"`
	Before     *Provider             `json:"before"`
	After      Provider              `json:"after"`
	Fields     []ProviderFieldChange `json:"fields"`
}

type ProviderFieldChange struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

type ProviderUpdateResult struct {
	Revision int64            `json:"revision"`
	ChangeID int64            `json:"changeId"`
	Provider Provider         `json:"provider"`
	Snapshot ProviderSnapshot `json:"snapshot"`
}

func (s *Service) prepareProvider(in providerInput, id string) (Provider, error) {
	name := strings.TrimSpace(in.Name)
	protocol := strings.TrimSpace(in.Protocol)
	baseURL := strings.TrimSpace(in.BaseURL)
	if name == "" || len(name) > 100 {
		return Provider{}, badRequest("INVALID_NAME", "供应商名称不能为空且最长 100 字符")
	}
	validProtocol := false
	for _, candidate := range protocolNames {
		if candidate == protocol {
			validProtocol = true
			break
		}
	}
	if !validProtocol {
		return Provider{}, badRequest("INVALID_PROTOCOL", "protocol 只能是 %s", strings.Join(protocolNames, "/"))
	}
	if err := validateBaseURL(baseURL); err != nil {
		return Provider{}, err
	}
	var secretRef *string
	if in.SecretRef != nil {
		trimmed := strings.TrimSpace(*in.SecretRef)
		if trimmed != "" {
			if len(trimmed) > 200 || strings.ContainsAny(trimmed, "\n\r") {
				return Provider{}, badRequest("INVALID_SECRET_REF", "secretRef 只是凭据库引用，不能包含换行且最长 200 字符")
			}
			if looksLikeSecret(trimmed) {
				return Provider{}, badRequest("SECRET_INLINE", "secretRef 必须是凭据库引用，不接受内联密钥")
			}
			secretRef = &trimmed
		}
	}
	return Provider{ID: id, Name: name, Protocol: protocol, BaseURL: baseURL, SecretRef: secretRef}, nil
}

func (s *Service) loadProvider(q queryer, id string) (Provider, bool, error) {
	var provider Provider
	var secret sql.NullString
	err := q.QueryRow(`SELECT id,name,protocol,base_url,secret_ref,created_at FROM registry_providers WHERE id=?`, id).
		Scan(&provider.ID, &provider.Name, &provider.Protocol, &provider.BaseURL, &secret, &provider.CreatedAt)
	if err == sql.ErrNoRows {
		return Provider{}, false, nil
	}
	if err != nil {
		return Provider{}, false, err
	}
	if secret.Valid {
		provider.SecretRef = &secret.String
	}
	for _, preset := range providerPresets {
		if preset.ID == provider.ID {
			provider.Preset = true
		}
	}
	return provider, true, nil
}

func providerFieldDiffs(before, after Provider) []ProviderFieldChange {
	out := []ProviderFieldChange{}
	add := func(field string, oldValue, newValue any) {
		if oldValue != newValue {
			out = append(out, ProviderFieldChange{Field: field, Before: oldValue, After: newValue})
		}
	}
	add("name", before.Name, after.Name)
	add("protocol", before.Protocol, after.Protocol)
	add("baseUrl", before.BaseURL, after.BaseURL)
	add("secretRef", derefString(before.SecretRef), derefString(after.SecretRef))
	return out
}

// UpdateProvider replaces the editable provider fields after validating the
// same rules as create. It is a CAS write keyed on the registry revision, and
// it also repairs implicit placeholders created by catalog imports, so an
// imported provider can be pointed at a real endpoint without losing models.
func (s *Service) UpdateProvider(ctx context.Context, id string, in providerInput, revision int64) (ProviderUpdateResult, error) {
	id = strings.TrimSpace(id)
	if err := validateIdentity(id, id); err != nil {
		return ProviderUpdateResult{}, badRequest("INVALID_PROVIDER_ID", "供应商 ID %q 只能包含小写字母、数字、点、下划线和短横线", id)
	}
	if strings.TrimSpace(in.ID) != "" && strings.TrimSpace(in.ID) != id {
		return ProviderUpdateResult{}, badRequest("PROVIDER_ID_IMMUTABLE", "供应商 ID 不能通过更新修改")
	}
	candidate, err := s.prepareProvider(in, id)
	if err != nil {
		return ProviderUpdateResult{}, err
	}
	var result ProviderUpdateResult
	err = storage.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		current, err := s.readRevision(tx)
		if err != nil {
			return err
		}
		if current != revision {
			return conflict("STALE_REVISION", "revision %d 已过期，当前为 %d", revision, current)
		}
		before, ok, err := s.loadProvider(tx, id)
		if err != nil {
			return err
		}
		if !ok {
			return notFound("PROVIDER_NOT_FOUND", "供应商 %q 不存在；导入目录会自动创建占位，若从未引用该 ID 请先 POST /providers", id)
		}
		candidate.CreatedAt = before.CreatedAt
		candidate.Preset = before.Preset
		snapshot := ProviderSnapshot{ProviderID: id, Before: &before, After: candidate, Fields: providerFieldDiffs(before, candidate)}
		if len(snapshot.Fields) == 0 {
			result = ProviderUpdateResult{Revision: current, Provider: candidate, Snapshot: snapshot}
			return nil
		}
		newRevision, err := s.bumpRevision(tx)
		if err != nil {
			return err
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.Exec(`UPDATE registry_providers SET name=?,protocol=?,base_url=?,secret_ref=? WHERE id=?`,
			candidate.Name, candidate.Protocol, candidate.BaseURL, nullableString(candidate.SecretRef), id); err != nil {
			return err
		}
		beforeJSON, err := json.Marshal(before)
		if err != nil {
			return err
		}
		afterJSON, err := json.Marshal(candidate)
		if err != nil {
			return err
		}
		summary := fmt.Sprintf("更新供应商 %s", id)
		changeID, err := s.writeChange(tx, "provider", revision, newRevision, now, summary, nil)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO registry_provider_changes(change_id,provider_id,before_json,after_json) VALUES(?,?,?,?)`,
			changeID, id, string(beforeJSON), string(afterJSON)); err != nil {
			return err
		}
		result = ProviderUpdateResult{Revision: newRevision, ChangeID: changeID, Provider: candidate, Snapshot: snapshot}
		return nil
	})
	if err != nil {
		return ProviderUpdateResult{}, err
	}
	return result, nil
}

// ChangeProviders loads provider snapshots for a change, if any.
func (s *Service) ChangeProviders(ctx context.Context, changeID int64) ([]ProviderSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider_id,before_json,after_json FROM registry_provider_changes WHERE change_id=? ORDER BY provider_id`, changeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProviderSnapshot{}
	for rows.Next() {
		var id string
		var beforeJSON, afterJSON sql.NullString
		if err := rows.Scan(&id, &beforeJSON, &afterJSON); err != nil {
			return nil, err
		}
		snapshot := ProviderSnapshot{ProviderID: id}
		if beforeJSON.Valid {
			if err := json.Unmarshal([]byte(beforeJSON.String), &snapshot.Before); err != nil {
				return nil, err
			}
		}
		if afterJSON.Valid {
			if err := json.Unmarshal([]byte(afterJSON.String), &snapshot.After); err != nil {
				return nil, err
			}
		}
		before := Provider{}
		if snapshot.Before != nil {
			before = *snapshot.Before
		}
		snapshot.Fields = providerFieldDiffs(before, snapshot.After)
		out = append(out, snapshot)
	}
	return out, rows.Err()
}

func (s *Service) providerRollbackConflict(q queryer, snapshot ProviderSnapshot) (bool, error) {
	live, exists, err := s.loadProvider(q, snapshot.ProviderID)
	if err != nil {
		return false, err
	}
	if !exists || len(providerFieldDiffs(live, snapshot.After)) > 0 || live.CreatedAt != snapshot.After.CreatedAt {
		return true, nil
	}
	if snapshot.Before == nil {
		var models int
		if err := q.QueryRow(`SELECT COUNT(*) FROM registry_models WHERE provider_id=?`, snapshot.ProviderID).Scan(&models); err != nil {
			return false, err
		}
		return models > 0, nil
	}
	return false, nil
}

func (s *Service) loadProviderSnapshots(tx *sql.Tx, changeID int64) ([]ProviderSnapshot, error) {
	rows, err := tx.Query(`SELECT provider_id,before_json,after_json FROM registry_provider_changes WHERE change_id=? ORDER BY provider_id`, changeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProviderSnapshot{}
	for rows.Next() {
		var id string
		var beforeJSON, afterJSON sql.NullString
		if err := rows.Scan(&id, &beforeJSON, &afterJSON); err != nil {
			return nil, err
		}
		snapshot := ProviderSnapshot{ProviderID: id}
		if beforeJSON.Valid {
			if err := json.Unmarshal([]byte(beforeJSON.String), &snapshot.Before); err != nil {
				return nil, err
			}
		}
		if afterJSON.Valid {
			if err := json.Unmarshal([]byte(afterJSON.String), &snapshot.After); err != nil {
				return nil, err
			}
		}
		out = append(out, snapshot)
	}
	return out, rows.Err()
}
