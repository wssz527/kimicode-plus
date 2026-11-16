package registry

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"enhancedkimi/internal/storage"
)

const schemaModule = "registry"

var migrations = []string{
	`CREATE TABLE IF NOT EXISTS registry_providers (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		protocol TEXT NOT NULL,
		base_url TEXT NOT NULL,
		secret_ref TEXT,
		created_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS registry_models (
		provider_id TEXT NOT NULL REFERENCES registry_providers(id) ON DELETE CASCADE,
		model_id TEXT NOT NULL,
		display_name TEXT NOT NULL,
		PRIMARY KEY (provider_id, model_id)
	)`,
	`CREATE TABLE IF NOT EXISTS registry_capabilities (
		provider_id TEXT NOT NULL,
		model_id TEXT NOT NULL,
		field TEXT NOT NULL,
		value_kind TEXT NOT NULL,
		value_number REAL,
		value_boolean INTEGER,
		source TEXT NOT NULL,
		fetched_at TEXT NOT NULL,
		observed_ms INTEGER NOT NULL,
		source_url TEXT,
		PRIMARY KEY (provider_id, model_id, field),
		FOREIGN KEY (provider_id, model_id) REFERENCES registry_models(provider_id, model_id) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS registry_revisions (
		id INTEGER PRIMARY KEY AUTOINCREMENT
	)`,
	`CREATE TABLE IF NOT EXISTS registry_changes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		kind TEXT NOT NULL,
		base_revision INTEGER NOT NULL,
		revision INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		summary TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS registry_change_models (
		change_id INTEGER NOT NULL REFERENCES registry_changes(id) ON DELETE CASCADE,
		provider_id TEXT NOT NULL,
		model_id TEXT NOT NULL,
		state TEXT NOT NULL,
		before_json TEXT,
		after_json TEXT,
		PRIMARY KEY (change_id, provider_id, model_id)
	)`,
	`CREATE TABLE IF NOT EXISTS registry_provider_changes (
		change_id INTEGER NOT NULL REFERENCES registry_changes(id) ON DELETE CASCADE,
		provider_id TEXT NOT NULL,
		before_json TEXT,
		after_json TEXT,
		PRIMARY KEY (change_id, provider_id)
	)`,
	`CREATE TABLE IF NOT EXISTS registry_rollbacks (
		change_id INTEGER PRIMARY KEY REFERENCES registry_changes(id) ON DELETE CASCADE,
		revision INTEGER NOT NULL,
		rolled_back_at TEXT NOT NULL
	)`,
	`ALTER TABLE registry_models ADD COLUMN metadata_json TEXT NOT NULL DEFAULT '{}'`,
	`ALTER TABLE registry_capabilities ADD COLUMN source_revision TEXT`,
	`CREATE TABLE registry_catalog(id INTEGER PRIMARY KEY CHECK(id=1),raw BLOB NOT NULL,retrieved_at TEXT NOT NULL,checked_at TEXT NOT NULL,etag TEXT NOT NULL,last_modified TEXT NOT NULL,sha256 TEXT NOT NULL,last_error TEXT NOT NULL)`,
	`INSERT INTO registry_catalog VALUES(1,x'','','','','','','')`,
}

func (s *Service) migrate() error {
	if err := storage.Migrate(s.db, schemaModule, migrations); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO registry_revisions(id) SELECT 0 WHERE NOT EXISTS (SELECT 1 FROM registry_revisions)`)
	return err
}

func (s *Service) readRevision(q queryer) (int64, error) {
	var rev int64
	if err := q.QueryRow(`SELECT COALESCE(MAX(id),0) FROM registry_revisions`).Scan(&rev); err != nil {
		return 0, err
	}
	return rev, nil
}

func (s *Service) bumpRevision(tx *sql.Tx) (int64, error) {
	res, err := tx.Exec(`INSERT INTO registry_revisions DEFAULT VALUES`)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

type queryer interface {
	QueryRow(string, ...any) *sql.Row
	Query(string, ...any) (*sql.Rows, error)
	Exec(string, ...any) (sql.Result, error)
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func (s *Service) providerExists(q queryer, id string) (bool, error) {
	var one int
	err := q.QueryRow(`SELECT 1 FROM registry_providers WHERE id=?`, id).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) loadAll(q queryer) (map[modelKey]*modelRecord, error) {
	rows, err := q.Query(`SELECT m.provider_id, m.model_id, m.display_name, m.metadata_json,
		c.field, c.value_kind, c.value_number, c.value_boolean, c.source, c.fetched_at, c.observed_ms, c.source_url, c.source_revision
		FROM registry_models m
		LEFT JOIN registry_capabilities c ON c.provider_id=m.provider_id AND c.model_id=m.model_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[modelKey]*modelRecord{}
	for rows.Next() {
		var key modelKey
		var displayName, metadata string
		var field, kind, source, fetchedAt sql.NullString
		var number sql.NullFloat64
		var boolean sql.NullInt64
		var observed sql.NullInt64
		var sourceURL, sourceRevision sql.NullString
		if err := rows.Scan(&key.ProviderID, &key.ModelID, &displayName, &metadata,
			&field, &kind, &number, &boolean, &source, &fetchedAt, &observed, &sourceURL, &sourceRevision); err != nil {
			return nil, err
		}
		rec, ok := out[key]
		if !ok {
			rec = newModelRecord(key.ProviderID, key.ModelID)
			rec.DisplayName = displayName
			if err := json.Unmarshal([]byte(metadata), &struct {
				AutoName         *string                `json:"autoName"`
				ManualName       **string               `json:"manualName"`
				AutoCapabilities *map[string]Capability `json:"autoCapabilities"`
			}{&rec.AutoName, &rec.ManualName, &rec.AutoCapabilities}); err != nil {
				return nil, err
			}
			out[key] = rec
		}
		if !field.Valid {
			continue
		}
		capability, err := scanCapability(field.String, kind.String, number, boolean, source.String, fetchedAt.String, observed.Int64, sourceURL)
		if err != nil {
			return nil, err
		}
		if sourceRevision.Valid {
			capability.SourceRevision = &sourceRevision.String
		}
		rec.Capabilities[field.String] = capability
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, rec := range out {
		rec.restoreMetadata()
	}
	return out, nil
}

func (s *Service) loadModels(q queryer, keys []modelKey) (map[modelKey]*modelRecord, error) {
	out := map[modelKey]*modelRecord{}
	if len(keys) == 0 {
		return out, nil
	}
	seen := map[modelKey]bool{}
	unique := make([]modelKey, 0, len(keys))
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, key)
	}
	sort.Slice(unique, func(i, j int) bool {
		if unique[i].ProviderID != unique[j].ProviderID {
			return unique[i].ProviderID < unique[j].ProviderID
		}
		return unique[i].ModelID < unique[j].ModelID
	})
	args := make([]any, 0, len(unique)*2)
	for _, key := range unique {
		args = append(args, key.ProviderID, key.ModelID)
	}
	rows, err := q.Query(`SELECT m.provider_id, m.model_id, m.display_name, m.metadata_json,
		c.field, c.value_kind, c.value_number, c.value_boolean, c.source, c.fetched_at, c.observed_ms, c.source_url, c.source_revision
		FROM registry_models m
		LEFT JOIN registry_capabilities c ON c.provider_id=m.provider_id AND c.model_id=m.model_id
		WHERE (m.provider_id, m.model_id) IN (VALUES `+tuplePlaceholders(len(unique))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key modelKey
		var displayName, metadata string
		var field, kind, source, fetchedAt sql.NullString
		var number sql.NullFloat64
		var boolean sql.NullInt64
		var observed sql.NullInt64
		var sourceURL, sourceRevision sql.NullString
		if err := rows.Scan(&key.ProviderID, &key.ModelID, &displayName, &metadata,
			&field, &kind, &number, &boolean, &source, &fetchedAt, &observed, &sourceURL, &sourceRevision); err != nil {
			return nil, err
		}
		rec, ok := out[key]
		if !ok {
			rec = newModelRecord(key.ProviderID, key.ModelID)
			out[key] = rec
		}
		rec.DisplayName = displayName
		if err := json.Unmarshal([]byte(metadata), &struct {
			AutoName         *string                `json:"autoName"`
			ManualName       **string               `json:"manualName"`
			AutoCapabilities *map[string]Capability `json:"autoCapabilities"`
		}{&rec.AutoName, &rec.ManualName, &rec.AutoCapabilities}); err != nil {
			return nil, err
		}
		if !field.Valid {
			continue
		}
		capability, err := scanCapability(field.String, kind.String, number, boolean, source.String, fetchedAt.String, observed.Int64, sourceURL)
		if err != nil {
			return nil, err
		}
		if sourceRevision.Valid {
			capability.SourceRevision = &sourceRevision.String
		}
		rec.Capabilities[field.String] = capability
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, rec := range out {
		rec.restoreMetadata()
	}
	return out, nil
}

func tuplePlaceholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("(?,?),", n), ",")
}

func scanCapability(field, kind string, number sql.NullFloat64, boolean sql.NullInt64, source, fetchedAt string, observed int64, sourceURL sql.NullString) (Capability, error) {
	out := Capability{Source: Source(source), FetchedAt: strPtr(fetchedAt)}
	if sourceURL.Valid && sourceURL.String != "" {
		out.SourceURL = &sourceURL.String
	}
	switch kind {
	case kindNumber:
		out.Value = number.Float64
		if !number.Valid {
			return out, fmt.Errorf("capability %s/%s missing numeric value", field, source)
		}
	case kindBool:
		out.Value = boolean.Int64 == 1
		if !boolean.Valid {
			return out, fmt.Errorf("capability %s/%s missing boolean value", field, source)
		}
	case kindNull:
		out.Value = nil
	default:
		return out, fmt.Errorf("capability %s/%s has invalid value kind %q", field, source, kind)
	}
	return out, nil
}

func (s *Service) writeModel(tx *sql.Tx, rec *modelRecord) error {
	metadata, err := json.Marshal(struct {
		AutoName         string                `json:"autoName"`
		ManualName       *string               `json:"manualName"`
		AutoCapabilities map[string]Capability `json:"autoCapabilities"`
	}{rec.AutoName, rec.ManualName, rec.AutoCapabilities})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO registry_models(provider_id,model_id,display_name,metadata_json) VALUES(?,?,?,?)
		ON CONFLICT(provider_id,model_id) DO UPDATE SET display_name=excluded.display_name,metadata_json=excluded.metadata_json`,
		rec.Key.ProviderID, rec.Key.ModelID, rec.DisplayName, string(metadata)); err != nil {
		return err
	}
	fields := make([]string, 0, len(rec.Capabilities))
	for field := range rec.Capabilities {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	for _, field := range fields {
		capability := rec.Capabilities[field]
		kind := kindNumber
		if capability.IsBool() {
			kind = kindBool
		}
		var number, boolean any
		switch {
		case capability.Value == nil:
			kind = kindNull
		case capability.IsBool():
			boolean = boolToInt(capability.Value.(bool))
		default:
			number = capability.Value.(float64)
		}
		if _, err := tx.Exec(`INSERT INTO registry_capabilities(provider_id,model_id,field,value_kind,value_number,value_boolean,source,fetched_at,observed_ms,source_url,source_revision)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(provider_id,model_id,field) DO UPDATE SET
				value_kind=excluded.value_kind, value_number=excluded.value_number, value_boolean=excluded.value_boolean,
				source=excluded.source, fetched_at=excluded.fetched_at, observed_ms=excluded.observed_ms, source_url=excluded.source_url,source_revision=excluded.source_revision`,
			rec.Key.ProviderID, rec.Key.ModelID, field, kind, number, boolean, string(capability.Source),
			emptyString(capability.FetchedAt), capability.observedMs, nullableString(capability.SourceURL), nullableString(capability.SourceRevision)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) writeChange(tx *sql.Tx, kind string, base, revision int64, createdAt, summary string, deltas []modelDelta) (int64, error) {
	res, err := tx.Exec(`INSERT INTO registry_changes(kind,base_revision,revision,created_at,summary) VALUES(?,?,?,?,?)`,
		kind, base, revision, createdAt, summary)
	if err != nil {
		return 0, err
	}
	changeID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, delta := range deltas {
		var beforeJSON, afterJSON any
		if delta.before != nil {
			raw, err := marshalModel(delta.before)
			if err != nil {
				return 0, err
			}
			beforeJSON = raw
		}
		if delta.after != nil {
			raw, err := marshalModel(delta.after)
			if err != nil {
				return 0, err
			}
			afterJSON = raw
		}
		if _, err := tx.Exec(`INSERT INTO registry_change_models(change_id,provider_id,model_id,state,before_json,after_json)
			VALUES(?,?,?,?,?,?)`, changeID, delta.key.ProviderID, delta.key.ModelID, delta.state, beforeJSON, afterJSON); err != nil {
			return 0, err
		}
	}
	return changeID, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func strPtr(v string) *string {
	if v == "" {
		return nil
	}
	out := v
	return &out
}

func nullableString(v *string) any {
	if v == nil || *v == "" {
		return nil
	}
	return *v
}

// emptyString stores an explicit UNKNOWN row: the column is NOT NULL, and an
// empty value maps back to a nil *string on read.
func emptyString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func derefString(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}
