package usage

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"enhancedkimi/internal/storage"
)

const schemaModule = "usage"

var migrations = []string{
	`CREATE TABLE IF NOT EXISTS usage_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		canonical_key TEXT NOT NULL UNIQUE,
		duplicate_key TEXT NOT NULL,
		provider_id TEXT NOT NULL,
		model TEXT NOT NULL,
		request_id TEXT NOT NULL,
		attempt_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		project_id TEXT NOT NULL,
		agent_id TEXT NOT NULL,
		role TEXT NOT NULL,
		request_kind TEXT NOT NULL,
		protocol TEXT NOT NULL,
		source TEXT NOT NULL,
		authority INTEGER NOT NULL,
		kind TEXT NOT NULL,
		at TEXT NOT NULL,
		at_ms INTEGER NOT NULL,
		fresh INTEGER,
		input INTEGER,
		output INTEGER,
		cache_read INTEGER,
		cache_write INTEGER,
		raw_json TEXT,
		price_json TEXT,
		observed_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS usage_events_at ON usage_events(at_ms DESC, id DESC)`,
	`CREATE INDEX IF NOT EXISTS usage_events_session ON usage_events(session_id, at_ms DESC)`,
	`CREATE TABLE IF NOT EXISTS usage_provenance (
		event_id INTEGER NOT NULL REFERENCES usage_events(id) ON DELETE CASCADE,
		observation_id TEXT NOT NULL,
		source TEXT NOT NULL,
		authority INTEGER NOT NULL,
		role TEXT NOT NULL,
		at TEXT NOT NULL,
		raw_json TEXT,
		recorded_at TEXT NOT NULL,
		PRIMARY KEY (event_id, observation_id)
	)`,
	`CREATE TABLE IF NOT EXISTS usage_unlinked (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		observation_id TEXT NOT NULL UNIQUE,
		source TEXT NOT NULL,
		provider_id TEXT NOT NULL,
		model TEXT NOT NULL,
		wire_agent TEXT NOT NULL,
		record_position TEXT NOT NULL,
		protocol TEXT NOT NULL,
		at TEXT NOT NULL,
		at_ms INTEGER NOT NULL,
		counts_json TEXT NOT NULL,
		raw_json TEXT,
		reason TEXT NOT NULL,
		recorded_at TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS usage_actual_billing (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		observation_id TEXT NOT NULL UNIQUE,
		provider_id TEXT NOT NULL,
		amount_micros INTEGER NOT NULL,
		currency TEXT NOT NULL,
		source TEXT NOT NULL,
		observed_at TEXT NOT NULL,
		observed_ms INTEGER NOT NULL DEFAULT 0,
		note TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS usage_actual_billing_scope ON usage_actual_billing(provider_id, observed_ms)`,
	`CREATE TABLE IF NOT EXISTS usage_quota_snapshots (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		ledger TEXT NOT NULL,
		provider_id TEXT NOT NULL,
		scope TEXT NOT NULL,
		remaining TEXT,
		limit_value TEXT,
		unit TEXT,
		reset_at TEXT,
		currency TEXT,
		source TEXT NOT NULL,
		observed_at TEXT NOT NULL,
		note TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS usage_prices (
		provider_id TEXT NOT NULL,
		model TEXT NOT NULL,
		currency TEXT NOT NULL,
		input_per_million REAL NOT NULL,
		output_per_million REAL NOT NULL,
		read_per_million REAL,
		write_per_million REAL,
		source TEXT NOT NULL,
		fetched_at TEXT NOT NULL,
		fetched_ms INTEGER NOT NULL,
		observed_at TEXT NOT NULL,
		PRIMARY KEY (provider_id, model)
	)`,
	`ALTER TABLE usage_events ADD COLUMN observation_state TEXT NOT NULL DEFAULT 'PARTIAL'`,
	`ALTER TABLE usage_events ADD COLUMN sequence INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE usage_events ADD COLUMN conflict_status TEXT NOT NULL DEFAULT ''`,
	`UPDATE usage_events SET observation_state=CASE WHEN output IS NULL THEN 'PARTIAL' ELSE 'FINAL' END,sequence=at_ms*1000000`,
	`CREATE TABLE usage_observations(event_id INTEGER NOT NULL REFERENCES usage_events(id),observation_id TEXT NOT NULL,source TEXT NOT NULL,lifecycle TEXT NOT NULL,sequence INTEGER NOT NULL,counts_json TEXT NOT NULL,decision TEXT NOT NULL,observed_at TEXT NOT NULL,PRIMARY KEY(event_id,observation_id))`,
}

type Service struct {
	db *sql.DB
}

func New(db *sql.DB) (*Service, error) {
	if db == nil {
		return nil, errf(http.StatusNotFound, "NO_DATABASE", "数据库未初始化")
	}
	s := &Service{db: db}
	if err := storage.Migrate(db, schemaModule, migrations); err != nil {
		return nil, err
	}
	return s, nil
}

type apiError struct {
	status  int
	code    string
	message string
}

func (e *apiError) Error() string { return fmt.Sprintf("%s: %s", e.code, e.message) }

func errf(status int, code, format string, args ...any) error {
	return &apiError{status: status, code: code, message: fmt.Sprintf(format, args...)}
}

func badRequest(code, format string, args ...any) error {
	return errf(http.StatusBadRequest, code, format, args...)
}

func unprocessable(code, format string, args ...any) error {
	return errf(http.StatusUnprocessableEntity, code, format, args...)
}

func conflict(code, format string, args ...any) error {
	return errf(http.StatusConflict, code, format, args...)
}

func notFound(code, format string, args ...any) error {
	return errf(http.StatusNotFound, code, format, args...)
}

func errorResponse(err error) (int, string, string) {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.status, ae.code, ae.message
	}
	return http.StatusInternalServerError, "INTERNAL", "内部错误"
}

func numberPtr(value float64) *float64 { return &value }

func stringPtr(value string) *string { return &value }

func parseTime(raw string) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	return parsed.UTC().Format(time.RFC3339Nano), nil
}
