package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"enhancedkimi/internal/storage"
)

type PreviewChange struct {
	ProviderID string        `json:"providerId"`
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	State      string        `json:"state"`
	Fields     []fieldChange `json:"fields"`
	Before     *Model        `json:"before,omitempty"`
	After      Model         `json:"after"`
}

type PreviewResult struct {
	Revision int64           `json:"revision"`
	Changes  []PreviewChange `json:"changes"`
	Summary  changeSummary   `json:"summary"`
}

type ImportResult struct {
	Revision  int64 `json:"revision"`
	ChangeID  int64 `json:"changeId"`
	Imported  int   `json:"imported"`
	Created   int   `json:"created"`
	Updated   int   `json:"updated"`
	Unchanged int   `json:"unchanged"`
}

type OverrideResult struct {
	Revision int64      `json:"revision"`
	ChangeID int64      `json:"changeId"`
	Model    Model      `json:"model"`
	Field    string     `json:"field"`
	Before   Capability `json:"before"`
	After    Capability `json:"after"`
}

type ModelChange struct {
	ID               int64  `json:"id"`
	Kind             string `json:"kind"`
	BaseRevision     int64  `json:"baseRevision"`
	Revision         int64  `json:"revision"`
	CreatedAt        string `json:"createdAt"`
	Summary          string `json:"summary"`
	RolledBack       bool   `json:"rolledBack"`
	RollbackRevision *int64 `json:"rollbackRevision,omitempty"`
}

type ModelChangeDetail struct {
	ModelChange
	Models    []ModelChangeModel `json:"models"`
	Providers []ProviderSnapshot `json:"providers"`
}

type ModelChangeModel struct {
	ProviderID string `json:"providerId"`
	ID         string `json:"id"`
	State      string `json:"state"`
	Before     *Model `json:"before,omitempty"`
	After      *Model `json:"after,omitempty"`
}

func (s *Service) Preview(ctx context.Context, inputs []modelInput) (PreviewResult, error) {
	records, err := prepareRecords(inputs)
	if err != nil {
		return PreviewResult{}, err
	}
	var revision int64
	var current map[modelKey]*modelRecord
	keys := modelKeys(records)
	err = storage.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		var err error
		revision, err = s.readRevision(tx)
		if err != nil {
			return err
		}
		current, err = s.loadModels(tx, keys)
		return err
	})
	if err != nil {
		return PreviewResult{}, err
	}

	merged := applyRecords(current, records)
	deltas, summary := computeDeltas(current, merged, keys)
	changes := make([]PreviewChange, 0, len(deltas))
	for _, delta := range deltas {
		after := recordToModel(delta.after)
		change := PreviewChange{ProviderID: delta.key.ProviderID, ID: delta.key.ModelID, Name: delta.after.DisplayName, State: delta.state, Fields: delta.fields, After: after}
		if delta.before != nil {
			before := recordToModel(delta.before)
			change.Before = &before
		}
		changes = append(changes, change)
	}
	return PreviewResult{Revision: revision, Changes: changes, Summary: summary}, nil
}

func (s *Service) Import(ctx context.Context, inputs []modelInput, revision int64, kind string) (ImportResult, error) {
	records, err := prepareRecords(inputs)
	if err != nil {
		return ImportResult{}, err
	}
	if kind == "" {
		kind = "import"
	}
	var result ImportResult
	err = storage.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		current, err := s.readRevision(tx)
		if err != nil {
			return err
		}
		if current != revision {
			return conflict("STALE_REVISION", "revision %d 已过期，当前为 %d", revision, current)
		}
		keys := modelKeys(records)
		existing, err := s.loadModels(tx, keys)
		if err != nil {
			return err
		}
		if err := s.ensureProviders(tx, keys); err != nil {
			return err
		}
		merged := applyRecords(existing, records)
		deltas, summary := computeDeltas(existing, merged, keys)
		if len(deltas) == 0 {
			result = ImportResult{Revision: current, Unchanged: summary.Unchanged}
			return nil
		}
		newRevision, err := s.bumpRevision(tx)
		if err != nil {
			return err
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		changeID, err := s.writeChange(tx, kind, revision, newRevision, now, summaryText(kind, summary, ""), deltas)
		if err != nil {
			return err
		}
		for _, delta := range deltas {
			if err := s.writeModel(tx, delta.after); err != nil {
				return err
			}
		}
		result = ImportResult{Revision: newRevision, ChangeID: changeID, Imported: len(deltas), Created: summary.Created, Updated: summary.Updated, Unchanged: summary.Unchanged}
		return nil
	})
	if err != nil {
		return ImportResult{}, err
	}
	return result, nil
}

func (s *Service) ensureProviders(tx *sql.Tx, keys []modelKey) error {
	needed := map[string]bool{}
	for _, key := range keys {
		needed[key.ProviderID] = true
	}
	if len(needed) == 0 {
		return nil
	}
	ids := make([]string, 0, len(needed))
	for id := range needed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rows, err := tx.Query(`SELECT id FROM registry_providers WHERE id IN (`+placeholders(len(ids))+`)`, stringsToAny(ids)...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		delete(needed, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range ids {
		if !needed[id] {
			continue
		}
		if err := s.insertImplicitProvider(tx, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) insertImplicitProvider(tx *sql.Tx, id string) error {
	name := id
	protocol := "openai"
	baseURL := "urn:unconfigured"
	note := false
	for _, preset := range providerPresets {
		if preset.ID == id {
			if preset.Official {
				name = preset.Name
				protocol = preset.Protocol
				baseURL = preset.BaseURL
			}
			note = true
			break
		}
	}
	if !note {
		name = id
	}
	_, err := tx.Exec(`INSERT INTO registry_providers(id,name,protocol,base_url,secret_ref,created_at) VALUES(?,?,?,?,NULL,?)`,
		id, name, protocol, baseURL, s.now().UTC().Format(time.RFC3339Nano))
	return err
}

func stringsToAny(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func (s *Service) Override(ctx context.Context, providerID, modelID, field string, value any, revision int64) (OverrideResult, error) {
	providerID = strings.TrimSpace(providerID)
	modelID = strings.TrimSpace(modelID)
	field = strings.TrimSpace(field)
	if err := validateIdentity(providerID, modelID); err != nil {
		return OverrideResult{}, err
	}
	if !isKnownField(field) {
		return OverrideResult{}, badRequest("UNKNOWN_FIELD", "未知模型能力字段 %q", field)
	}
	capability := Capability{Value: value, Source: SourceManual, FetchedAt: strPtr(s.now().UTC().Format(time.RFC3339Nano))}
	if err := capability.normalize(field); err != nil {
		return OverrideResult{}, err
	}
	var result OverrideResult
	err := storage.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		current, err := s.readRevision(tx)
		if err != nil {
			return err
		}
		if current != revision {
			return conflict("STALE_REVISION", "revision %d 已过期，当前为 %d", revision, current)
		}
		key := modelKey{ProviderID: providerID, ModelID: modelID}
		existing, err := s.loadModels(tx, []modelKey{key})
		if err != nil {
			return err
		}
		record, ok := existing[key]
		if !ok {
			return notFound("MODEL_NOT_FOUND", "模型 %s/%s 不存在，先用 preview/import 添加", providerID, modelID)
		}
		before := record.Capabilities[field]
		record.Capabilities[field] = capability
		newRevision, err := s.bumpRevision(tx)
		if err != nil {
			return err
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		delta := modelDelta{key: key, state: "updated", before: record.clone(), after: record.clone(), fields: []fieldChange{{Field: field, Before: before, After: capability}}}
		delta.before.Capabilities[field] = before
		summary := changeSummary{Updated: 1}
		changeID, err := s.writeChange(tx, "override", revision, newRevision, now, summaryText("手动覆盖 "+field, summary, ""), []modelDelta{delta})
		if err != nil {
			return err
		}
		if err := s.writeModel(tx, record); err != nil {
			return err
		}
		result = OverrideResult{Revision: newRevision, ChangeID: changeID, Model: recordToModel(record), Field: field, Before: before, After: capability}
		return nil
	})
	if err != nil {
		return OverrideResult{}, err
	}
	return result, nil
}

func (s *Service) ListChanges(ctx context.Context, offset, limit int) ([]ModelChange, int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM registry_changes`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.id,c.kind,c.base_revision,c.revision,c.created_at,c.summary,
		r.change_id IS NOT NULL, r.revision
		FROM registry_changes c LEFT JOIN registry_rollbacks r ON r.change_id=c.id
		ORDER BY c.id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []ModelChange{}
	for rows.Next() {
		var change ModelChange
		var rolled bool
		var rollbackRevision sql.NullInt64
		if err := rows.Scan(&change.ID, &change.Kind, &change.BaseRevision, &change.Revision, &change.CreatedAt, &change.Summary, &rolled, &rollbackRevision); err != nil {
			return nil, 0, err
		}
		change.RolledBack = rolled
		if rollbackRevision.Valid {
			value := rollbackRevision.Int64
			change.RollbackRevision = &value
		}
		out = append(out, change)
	}
	return out, total, rows.Err()
}

func (s *Service) ChangeDetail(ctx context.Context, id int64) (ModelChangeDetail, error) {
	var detail ModelChangeDetail
	var rolled bool
	var rollbackRevision sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT c.id,c.kind,c.base_revision,c.revision,c.created_at,c.summary,
		r.change_id IS NOT NULL, r.revision
		FROM registry_changes c LEFT JOIN registry_rollbacks r ON r.change_id=c.id WHERE c.id=?`, id).
		Scan(&detail.ID, &detail.Kind, &detail.BaseRevision, &detail.Revision, &detail.CreatedAt, &detail.Summary, &rolled, &rollbackRevision)
	if err == sql.ErrNoRows {
		return ModelChangeDetail{}, notFound("CHANGE_NOT_FOUND", "变更 %d 不存在", id)
	}
	if err != nil {
		return ModelChangeDetail{}, err
	}
	detail.RolledBack = rolled
	if rollbackRevision.Valid {
		value := rollbackRevision.Int64
		detail.RollbackRevision = &value
	}
	rows, err := s.db.QueryContext(ctx, `SELECT provider_id,model_id,state,before_json,after_json FROM registry_change_models WHERE change_id=? ORDER BY provider_id,model_id`, id)
	if err != nil {
		return ModelChangeDetail{}, err
	}
	defer rows.Close()
	detail.Models = []ModelChangeModel{}
	for rows.Next() {
		var item ModelChangeModel
		var beforeJSON, afterJSON sql.NullString
		if err := rows.Scan(&item.ProviderID, &item.ID, &item.State, &beforeJSON, &afterJSON); err != nil {
			return ModelChangeDetail{}, err
		}
		if beforeJSON.Valid {
			rec, err := decodeModel([]byte(beforeJSON.String))
			if err != nil {
				return ModelChangeDetail{}, err
			}
			model := recordToModel(rec)
			item.Before = &model
		}
		if afterJSON.Valid {
			rec, err := decodeModel([]byte(afterJSON.String))
			if err != nil {
				return ModelChangeDetail{}, err
			}
			model := recordToModel(rec)
			item.After = &model
		}
		detail.Models = append(detail.Models, item)
	}
	if err := rows.Err(); err != nil {
		return ModelChangeDetail{}, err
	}
	providers, err := s.ChangeProviders(ctx, id)
	if err != nil {
		return ModelChangeDetail{}, err
	}
	detail.Providers = providers
	return detail, nil
}

type RollbackResult struct {
	Revision          int64 `json:"revision"`
	RollbackChangeID  int64 `json:"rollbackChangeId"`
	Restored          int   `json:"restored"`
	Deleted           int   `json:"deleted"`
	RestoredProviders int   `json:"restoredProviders"`
	DeletedProviders  int   `json:"deletedProviders"`
}

func (s *Service) Rollback(ctx context.Context, changeID int64, revision int64) (RollbackResult, error) {
	var result RollbackResult
	err := storage.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		current, err := s.readRevision(tx)
		if err != nil {
			return err
		}
		if current != revision {
			return conflict("STALE_REVISION", "revision %d 已过期，当前为 %d", revision, current)
		}
		var rolled bool
		err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM registry_rollbacks WHERE change_id=?)`, changeID).Scan(&rolled)
		if err != nil {
			return err
		}
		if rolled {
			return conflict("ALREADY_ROLLED_BACK", "变更 %d 已回滚，不能重复回滚", changeID)
		}
		rows, err := tx.Query(`SELECT provider_id,model_id,state,before_json,after_json FROM registry_change_models WHERE change_id=? ORDER BY provider_id,model_id`, changeID)
		if err != nil {
			return err
		}
		type row struct {
			key    modelKey
			state  string
			before *modelRecord
			after  *modelRecord
		}
		var items []row
		for rows.Next() {
			var item row
			var beforeJSON, afterJSON sql.NullString
			if err := rows.Scan(&item.key.ProviderID, &item.key.ModelID, &item.state, &beforeJSON, &afterJSON); err != nil {
				rows.Close()
				return err
			}
			if beforeJSON.Valid {
				rec, err := decodeModel([]byte(beforeJSON.String))
				if err != nil {
					rows.Close()
					return err
				}
				item.before = rec
			}
			if afterJSON.Valid {
				rec, err := decodeModel([]byte(afterJSON.String))
				if err != nil {
					rows.Close()
					return err
				}
				item.after = rec
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		providers, err := s.loadProviderSnapshots(tx, changeID)
		if err != nil {
			return err
		}
		if len(items) == 0 && len(providers) == 0 {
			return notFound("CHANGE_NOT_FOUND", "变更 %d 不存在或没有可回滚快照", changeID)
		}
		keys := make([]modelKey, 0, len(items))
		for _, item := range items {
			keys = append(keys, item.key)
		}
		live, err := s.loadModels(tx, keys)
		if err != nil {
			return err
		}
		for _, item := range items {
			if !recordEqual(live[item.key], item.after) {
				return conflict("ROLLBACK_CONFLICT", "模型 %s/%s 在该变更之后已修改；请比较历史与当前快照", item.key.ProviderID, item.key.ModelID)
			}
		}
		for _, snap := range providers {
			conflicted, err := s.providerRollbackConflict(tx, snap)
			if err != nil {
				return err
			}
			if conflicted {
				return conflict("ROLLBACK_CONFLICT", "供应商 %s 有后续修改或模型引用", snap.ProviderID)
			}
		}
		var deltas []modelDelta
		var summary changeSummary
		for _, item := range items {
			now := live[item.key]
			if item.before == nil {
				if now != nil {
					if _, err := tx.Exec(`DELETE FROM registry_models WHERE provider_id=? AND model_id=?`, item.key.ProviderID, item.key.ModelID); err != nil {
						return err
					}
					deltas = append(deltas, modelDelta{key: item.key, state: "deleted", before: now, after: nil})
					result.Deleted++
					summary.Updated++
				}
				continue
			}
			delta, changed := rollbackDelta(item.key, now, item.before)
			if !changed {
				summary.Unchanged++
				continue
			}
			if err := s.writeModel(tx, item.before); err != nil {
				return err
			}
			deltas = append(deltas, delta)
			result.Restored++
			summary.Updated++
		}
		for _, snapshot := range providers {
			if snapshot.Before == nil {
				if _, err := tx.Exec(`DELETE FROM registry_providers WHERE id=?`, snapshot.ProviderID); err != nil {
					return err
				}
				result.DeletedProviders++
			} else {
				if _, err := tx.Exec(`UPDATE registry_providers SET name=?,protocol=?,base_url=?,secret_ref=? WHERE id=?`,
					snapshot.Before.Name, snapshot.Before.Protocol, snapshot.Before.BaseURL, nullableString(snapshot.Before.SecretRef), snapshot.ProviderID); err != nil {
					return err
				}
				result.RestoredProviders++
			}
		}
		newRevision, err := s.bumpRevision(tx)
		if err != nil {
			return err
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.Exec(`INSERT INTO registry_rollbacks(change_id,revision,rolled_back_at) VALUES(?,?,?)`, changeID, newRevision, now); err != nil {
			return err
		}
		summaryTextValue := fmt.Sprintf("回滚变更 %d: 恢复模型 %d，删除模型 %d，恢复供应商 %d，删除供应商 %d，未变化 %d", changeID, result.Restored, result.Deleted, result.RestoredProviders, result.DeletedProviders, summary.Unchanged)
		rollbackChangeID, err := s.writeChange(tx, "rollback", revision, newRevision, now, summaryTextValue, deltas)
		if err != nil {
			return err
		}
		result.Revision = newRevision
		result.RollbackChangeID = rollbackChangeID
		return nil
	})
	if err != nil {
		return RollbackResult{}, err
	}
	return result, nil
}

func rollbackDelta(key modelKey, live, target *modelRecord) (modelDelta, bool) {
	if live == nil {
		delta := modelDelta{key: key, state: "created", after: target}
		for _, field := range knownFields {
			if target.Capabilities[field].Source != SourceUnknown || target.Capabilities[field].Value != nil {
				delta.fields = append(delta.fields, fieldChange{Field: field, Before: Capability{Source: SourceUnknown}, After: target.Capabilities[field]})
			}
		}
		return delta, true
	}
	delta := modelDelta{key: key, state: "updated", before: live, after: target}
	changed := !recordEqual(live, target)
	if live.DisplayName != target.DisplayName {
		changed = true
	}
	for _, field := range knownFields {
		oldCapability := live.Capabilities[field]
		newCapability := target.Capabilities[field]
		if !capabilityEqual(oldCapability, newCapability) {
			delta.fields = append(delta.fields, fieldChange{Field: field, Before: oldCapability, After: newCapability})
			changed = true
		}
	}
	return delta, changed
}

// Discovery returns the saved mock catalog for a provider. It never performs a
// network request; the catalog is intentionally small but provenance-complete.
func (s *Service) Discovery(ctx context.Context, providerID string) ([]modelInput, error) {
	providerID = strings.TrimSpace(providerID)
	if err := validateIdentity(providerID, providerID); err != nil {
		return nil, badRequest("INVALID_PROVIDER_ID", "providerId %q 无效", providerID)
	}
	exists, err := s.providerExists(s.db, providerID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, notFound("PROVIDER_NOT_FOUND", "供应商 %q 不存在，请先创建或使用预设", providerID)
	}
	catalog, ok := mockCatalog[providerID]
	if !ok {
		return nil, notFound("NO_MOCK_CATALOG", "供应商 %q 没有本地 Mock 目录；本接口不会外联，请使用 modelsdev 导入或手动指定模型", providerID)
	}
	fetchedAt := s.now().UTC().Format(time.RFC3339Nano)
	out := make([]modelInput, 0, len(catalog))
	for _, entry := range catalog {
		caps := make(map[string]Capability, len(entry.capabilities))
		for field, value := range entry.capabilities {
			caps[field] = Capability{Value: value, Source: SourceRegistry, FetchedAt: &fetchedAt}
		}
		out = append(out, modelInput{ProviderID: providerID, ID: entry.id, Name: entry.name, Capabilities: caps})
	}
	return out, nil
}

type catalogEntry struct {
	id           string
	name         string
	capabilities map[string]any
}

const maxModelContext = 1048576

var mockCatalog = map[string][]catalogEntry{
	"kimi": {
		{id: "kimi-k2.5", name: "Kimi K2.5 (Mock)", capabilities: map[string]any{"context": float64(262144), "output": float64(32768), "vision": true, "tools": true, "thinking": true, "cacheTTL": float64(25)}},
		{id: "kimi-k2-coder", name: "Kimi K2 Coder (Mock)", capabilities: map[string]any{"context": float64(131072), "output": float64(16384), "vision": false, "tools": true, "thinking": false}},
	},
	"anthropic": {
		{id: "claude-opus-5-5", name: "Claude Opus 5.5 (Mock)", capabilities: map[string]any{"context": float64(1000000), "output": float64(128000), "vision": true, "video": false, "audio": false, "tools": true, "thinking": true}},
		{id: "claude-opus-4-7", name: "Claude Opus 4.7 (Mock)", capabilities: map[string]any{"context": float64(1000000), "output": float64(128000), "tools": true, "thinking": true}},
	},
	"openai": {
		{id: "gpt-5.2-pro", name: "GPT-5.2 Pro (Mock)", capabilities: map[string]any{"context": float64(400000), "input": float64(272000), "output": float64(128000), "vision": true, "tools": true, "thinking": true}},
		{id: "gpt-5.1", name: "GPT-5.1 (Mock)", capabilities: map[string]any{"context": float64(400000), "output": float64(128000), "vision": true, "tools": true}},
	},
	"openai-responses": {
		{id: "gpt-5.2-pro", name: "GPT-5.2 Pro (Responses Mock)", capabilities: map[string]any{"context": float64(400000), "output": float64(128000), "vision": true, "tools": true, "thinking": true}},
	},
	"google": {
		{id: "gemini-3-pro-image", name: "Nano Banana Pro (Mock)", capabilities: map[string]any{"context": float64(65536), "output": float64(32768), "vision": true, "video": true, "audio": true, "tools": false, "thinking": true}},
	},
	"local": {
		{id: "local-mock-7b", name: "Local Mock 7B", capabilities: map[string]any{"context": float64(32768), "output": float64(4096), "tools": false, "thinking": false}},
	},
}

type modelsDevProvider struct {
	ID     string                    `json:"id"`
	Name   string                    `json:"name"`
	API    string                    `json:"api"`
	Models map[string]modelsDevModel `json:"models"`
}

type modelsDevModel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ToolCall   *bool  `json:"tool_call"`
	Reasoning  *bool  `json:"reasoning"`
	Attachment *bool  `json:"attachment"`
	Modalities *struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Limit *struct {
		Context int `json:"context"`
		Input   int `json:"input"`
		Output  int `json:"output"`
	} `json:"limit"`
}

type ModelsDevCatalog struct {
	Source        string                       `json:"source"`
	RetrievedAt   string                       `json:"retrieved_at"`
	ProviderCount int                          `json:"provider_count"`
	Providers     map[string]modelsDevProvider `json:"providers"`
}

type ModelsDevResult struct {
	Preview  bool            `json:"preview"`
	Revision int64           `json:"revision"`
	Changes  []PreviewChange `json:"changes,omitempty"`
	Summary  changeSummary   `json:"summary"`
	Import   *ImportResult   `json:"import,omitempty"`
}

func parseModelsDev(catalog ModelsDevCatalog) ([]modelInput, error) {
	if len(catalog.Providers) == 0 {
		return nil, badRequest("EMPTY_CATALOG", "catalog.providers 不能为空")
	}
	if catalog.RetrievedAt == "" {
		return nil, badRequest("MISSING_PROVENANCE", "models.dev catalog 必须包含 retrieved_at 作为 fetchedAt")
	}
	fetched, err := time.Parse(time.RFC3339Nano, catalog.RetrievedAt)
	if err != nil {
		return nil, badRequest("INVALID_FETCHED_AT", "retrieved_at 必须是 RFC3339 时间")
	}
	fetchedAt := fetched.UTC().Format(time.RFC3339Nano)
	var sourceURL *string
	if catalog.Source != "" {
		if err := validateReferenceURL(catalog.Source); err != nil {
			return nil, err
		}
		value := catalog.Source
		sourceURL = &value
	}
	var out []modelInput
	for providerKey, provider := range catalog.Providers {
		providerID := strings.TrimSpace(provider.ID)
		if providerID == "" {
			providerID = strings.TrimSpace(providerKey)
		}
		if err := validateIdentity(providerID, providerID); err != nil {
			return nil, badRequest("INVALID_PROVIDER_ID", "models.dev provider %q 不是合法的供应商 ID", providerKey)
		}
		for modelKey, model := range provider.Models {
			modelID := strings.TrimSpace(model.ID)
			if modelID == "" {
				modelID = strings.TrimSpace(modelKey)
			}
			if modelID == "" {
				return nil, badRequest("INVALID_MODEL_ID", "models.dev provider %q 有模型缺少 ID", providerID)
			}
			caps := map[string]Capability{}
			add := func(field string, value any) {
				caps[field] = Capability{Value: value, Source: SourceModelsDev, FetchedAt: &fetchedAt, SourceURL: sourceURL}
			}
			if model.Limit != nil {
				if model.Limit.Context > 0 {
					add("context", float64(model.Limit.Context))
				}
				if model.Limit.Input > 0 {
					add("input", float64(model.Limit.Input))
				}
				if model.Limit.Output > 0 {
					add("output", float64(model.Limit.Output))
				}
			}
			if model.Modalities != nil && len(model.Modalities.Input) > 0 {
				has := func(target string) bool {
					for _, value := range model.Modalities.Input {
						if value == target {
							return true
						}
					}
					return false
				}
				add("vision", has("image"))
				add("video", has("video"))
				add("audio", has("audio"))
			}
			if model.ToolCall != nil {
				add("tools", *model.ToolCall)
			}
			if model.Reasoning != nil {
				add("thinking", *model.Reasoning)
			}
			name := strings.TrimSpace(model.Name)

			out = append(out, modelInput{ProviderID: providerID, ID: modelID, Name: name, Capabilities: caps})
		}
	}
	if len(out) == 0 {
		return nil, badRequest("EMPTY_CATALOG", "catalog 没有可用模型记录")
	}
	if len(out) > 5000 {
		return nil, badRequest("TOO_MANY_RECORDS", "单次最多导入 5000 条模型记录")
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProviderID != out[j].ProviderID {
			return out[i].ProviderID < out[j].ProviderID
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *Service) ImportModelsDev(ctx context.Context, catalog ModelsDevCatalog, revision int64, preview bool) (ModelsDevResult, error) {
	inputs, err := parseModelsDev(catalog)
	if err != nil {
		return ModelsDevResult{}, err
	}
	if preview {
		result, err := s.Preview(ctx, inputs)
		if err != nil {
			return ModelsDevResult{}, err
		}
		return ModelsDevResult{Preview: true, Revision: result.Revision, Changes: result.Changes, Summary: result.Summary}, nil
	}
	imported, err := s.Import(ctx, inputs, revision, "modelsdev")
	if err != nil {
		return ModelsDevResult{}, err
	}
	return ModelsDevResult{Preview: false, Revision: imported.Revision, Summary: changeSummary{Created: imported.Created, Updated: imported.Updated, Unchanged: imported.Unchanged}, Import: &imported}, nil
}

func (s *Service) decodeCatalog(raw json.RawMessage) (ModelsDevCatalog, error) {
	var catalog ModelsDevCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return ModelsDevCatalog{}, badRequest("INVALID_CATALOG", "catalog 必须是 models.dev providers 映射对象")
	}
	return catalog, nil
}
