package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"enhancedkimi/internal/storage"
)

var migrations = []string{
	// Migration 1 on a fresh database. An installation upgraded from an earlier
	// release already has this module at version 1 because that release created
	// `plugin_states` there, so the table is created only when it is missing and
	// the legacy row shape is carried forward by migration 2.
	`CREATE TABLE IF NOT EXISTS plugin_installs (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		version TEXT NOT NULL,
		manifest BLOB NOT NULL,
		capabilities TEXT NOT NULL,
		source TEXT NOT NULL,
		desired INTEGER NOT NULL DEFAULT 0,
		state TEXT NOT NULL DEFAULT 'DISABLED',
		error TEXT NOT NULL DEFAULT '',
		tombstone INTEGER NOT NULL DEFAULT 0,
		installed_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS plugin_storage (
		plugin_id TEXT NOT NULL,
		key TEXT NOT NULL,
		value BLOB NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY(plugin_id, key)
	)`,
	`CREATE TABLE IF NOT EXISTS plugin_activity (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		plugin_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		detail TEXT NOT NULL,
		at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS plugin_activity_recent ON plugin_activity(plugin_id, id DESC)`,
	// `desired` is the user's enabled intent, separate from `state`, which also
	// reports why a plugin is not running. A restart keeps the intent so the next
	// renderer can adopt the plugin; it never implies a running resource.
	//
	// This step must be able to run against a database created by the previous
	// release, where migration 1 created `plugin_states` instead. Such a database
	// records this module at version 1, so only this statement runs; it creates
	// `plugin_installs` when absent and adds the column when the earlier release
	// of this branch already created the table without it.
	`CREATE TABLE IF NOT EXISTS plugin_installs (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		version TEXT NOT NULL,
		manifest BLOB NOT NULL,
		capabilities TEXT NOT NULL,
		source TEXT NOT NULL,
		desired INTEGER NOT NULL DEFAULT 0,
		state TEXT NOT NULL DEFAULT 'DISABLED',
		error TEXT NOT NULL DEFAULT '',
		tombstone INTEGER NOT NULL DEFAULT 0,
		installed_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
}

// record is the persisted half of an install. The manifest is stored verbatim so
// a later scan can tell "the files changed" from "the store is stale", and the
// granted capabilities are stored separately from the declared ones: a user may
// confirm fewer capabilities than the manifest asks for, and only the granted
// list is ever consulted for authorization.
type record struct {
	id           string
	name         string
	version      string
	manifest     []byte
	capabilities []string
	source       string
	desired      bool
	state        string
	err          string
	tombstone    bool
	installedAt  time.Time
	updatedAt    time.Time
}

func migrate(db *sql.DB) error {
	if err := storage.Migrate(db, "plugins", migrations); err != nil {
		return fmt.Errorf("plugins migrations: %w", err)
	}
	return nil
}

func loadRecords(ctx context.Context, db *sql.DB) ([]record, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,name,version,manifest,capabilities,source,desired,state,error,tombstone,installed_at,updated_at FROM plugin_installs ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("load plugin installs: %w", err)
	}
	defer rows.Close()
	var out []record
	for rows.Next() {
		var item record
		var capabilities string
		var desired, tombstone int
		var installed, updated string
		if err := rows.Scan(&item.id, &item.name, &item.version, &item.manifest, &capabilities, &item.source, &desired, &item.state, &item.err, &tombstone, &installed, &updated); err != nil {
			return nil, fmt.Errorf("scan plugin install: %w", err)
		}
		item.desired = desired == 1
		item.tombstone = tombstone == 1
		if err := json.Unmarshal([]byte(capabilities), &item.capabilities); err != nil {
			return nil, fmt.Errorf("plugin %s capability list is unreadable", item.id)
		}
		item.installedAt, _ = storage.ParseSortableTime(installed)
		item.updatedAt, _ = storage.ParseSortableTime(updated)
		out = append(out, item)
	}
	return out, rows.Err()
}

func saveRecord(ctx context.Context, db *sql.DB, item record) error {
	capabilities, err := json.Marshal(item.capabilities)
	if err != nil {
		return err
	}
	desired, tombstone := 0, 0
	if item.desired {
		desired = 1
	}
	if item.tombstone {
		tombstone = 1
	}
	_, err = db.ExecContext(ctx, `INSERT INTO plugin_installs(id,name,version,manifest,capabilities,source,desired,state,error,tombstone,installed_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,version=excluded.version,manifest=excluded.manifest,capabilities=excluded.capabilities,
			source=excluded.source,desired=excluded.desired,state=excluded.state,error=excluded.error,tombstone=excluded.tombstone,
			installed_at=excluded.installed_at,updated_at=excluded.updated_at`,
		item.id, item.name, item.version, item.manifest, string(capabilities), item.source, desired, item.state, item.err, tombstone,
		storage.SortableTime(item.installedAt), storage.SortableTime(item.updatedAt))
	if err != nil {
		return fmt.Errorf("save plugin install: %w", err)
	}
	return nil
}

func deleteRecord(ctx context.Context, db *sql.DB, id string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM plugin_installs WHERE id=?`, id)
	return err
}

func setState(ctx context.Context, db *sql.DB, id, state string, desired bool, message string, now time.Time) error {
	flag := 0
	if desired {
		flag = 1
	}
	_, err := db.ExecContext(ctx, `UPDATE plugin_installs SET state=?,desired=?,error=?,updated_at=? WHERE id=?`,
		state, flag, message, storage.SortableTime(now), id)
	return err
}

func storageGet(ctx context.Context, db *sql.DB, pluginID, key string) ([]byte, bool, error) {
	var value []byte
	err := db.QueryRowContext(ctx, `SELECT value FROM plugin_storage WHERE plugin_id=? AND key=?`, pluginID, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return value, true, nil
}

func storageSet(ctx context.Context, db *sql.DB, pluginID, key string, value []byte, now time.Time) error {
	_, err := db.ExecContext(ctx, `INSERT INTO plugin_storage(plugin_id,key,value,updated_at) VALUES(?,?,?,?)
		ON CONFLICT(plugin_id,key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`,
		pluginID, key, value, storage.SortableTime(now))
	return err
}

func storageDelete(ctx context.Context, db *sql.DB, pluginID, key string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM plugin_storage WHERE plugin_id=? AND key=?`, pluginID, key)
	return err
}

func storageList(ctx context.Context, db *sql.DB, pluginID, prefix string, limit int) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT key FROM plugin_storage WHERE plugin_id=? AND key>=? AND key<? ORDER BY key LIMIT ?`,
		pluginID, prefix, prefix+"\uffff", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func storageClear(ctx context.Context, db *sql.DB, pluginID string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM plugin_storage WHERE plugin_id=?`, pluginID)
	return err
}

// PluginStorage and SetPluginStorage are the same rows the storage actions
// serve, exposed so a host service can seed or read a plugin's settings without
// an HTTP round trip. The key still has to satisfy the renderer's key rules.
func (m *Manager) PluginStorage(ctx context.Context, id, key string) ([]byte, bool, error) {
	if err := validateStorageKey(key); err != nil {
		return nil, false, err
	}
	return storageGet(ctx, m.db, id, key)
}

func (m *Manager) SetPluginStorage(ctx context.Context, id, key string, value []byte) error {
	if err := validateStorageKey(key); err != nil {
		return err
	}
	if len(value) > storageValueLimit {
		return apiError(http.StatusRequestEntityTooLarge, "PLUGIN_STORAGE_LIMIT", fmt.Sprintf("单个值不超过 %d 字节", storageValueLimit))
	}
	return storageSet(ctx, m.db, id, key, value, m.now())
}

// ActivityKind names the audited event classes. The set is closed so the UI can
// label every row without a fallback for unknown kinds.
const (
	ActivityInstall    = "INSTALL"
	ActivityEnable     = "ENABLE"
	ActivityDisable    = "DISABLE"
	ActivityUninstall  = "UNINSTALL"
	ActivityError      = "ERROR"
	ActivitySession    = "SESSIONS_SEND"
	ActivityConfig     = "CONFIG_WRITE"
	ActivityNetwork    = "NETWORK"
	ActivityCorrupt    = "CORRUPT"
	ActivityVersion    = "VERSION_BLOCKED"
	ActivityBlocked    = "ENABLE_BLOCKED"
	ActivitySafeMode   = "SAFE_MODE"
	ActivityUpgrade    = "UPGRADE"
	ActivityRestore    = "RESTORE"
	ActivityCredential = "CREDENTIAL"
	ActivityCommand    = "COMMAND"
	ActivityHandoff    = "HANDOFF"
	ActivityPlanner    = "PLANNER"
)

type Activity struct {
	ID       int64  `json:"id"`
	PluginID string `json:"pluginId"`
	Kind     string `json:"kind"`
	Detail   string `json:"detail"`
	At       string `json:"at"`
}

func recordActivity(ctx context.Context, db *sql.DB, pluginID, kind, detail string, now time.Time) error {
	_, err := db.ExecContext(ctx, `INSERT INTO plugin_activity(plugin_id,kind,detail,at) VALUES(?,?,?,?)`,
		pluginID, kind, truncate(detail, 400), storage.SortableTime(now))
	return err
}

func loadActivity(ctx context.Context, db *sql.DB, pluginID string, before int64, limit int) ([]Activity, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,plugin_id,kind,detail,at FROM plugin_activity
		WHERE plugin_id=? AND (?<=0 OR id<?) ORDER BY id DESC LIMIT ?`, pluginID, before, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Activity{}
	for rows.Next() {
		var item Activity
		if err := rows.Scan(&item.ID, &item.PluginID, &item.Kind, &item.Detail, &item.At); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:limit], "") + "…"
}
