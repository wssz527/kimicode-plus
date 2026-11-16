package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"enhancedkimi/internal/storage"
)

type Settings struct {
	Revision              int             `json:"revision"`
	SafeMode              bool            `json:"safeMode"`
	CompactionIdleMinutes int             `json:"compactionIdleMinutes"`
	DefaultChatSource     string          `json:"defaultChatSource"`
	Modules               map[string]bool `json:"modules"`
}
type Store struct {
	db   *sql.DB
	mu   sync.RWMutex
	safe atomic.Bool
}

var ErrConflict = errors.New("settings revision conflict")
var ErrSafeMode = errors.New("safe mode blocks dispatch")

func New(db *sql.DB) (*Store, error) {
	if err := storage.Migrate(db, "config", []string{`CREATE TABLE settings (id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL, data TEXT NOT NULL);`}); err != nil {
		return nil, err
	}
	initial := Settings{0, false, 25, "kimi", map[string]bool{"registry": true, "usage": true, "bridge": true, "plugins": true}}
	b, _ := json.Marshal(initial)
	if _, err := db.Exec(`INSERT OR IGNORE INTO settings VALUES(1,0,?)`, string(b)); err != nil {
		return nil, err
	}
	s := &Store{db: db}
	v, err := s.Get()
	if err != nil {
		return nil, err
	}
	s.safe.Store(v.SafeMode)
	return s, nil
}
func (s *Store) Safe() bool { return s.safe.Load() }
func (s *Store) Get() (Settings, error) {
	var v Settings
	var data string
	err := s.db.QueryRow(`SELECT data FROM settings WHERE id=1`).Scan(&data)
	if err != nil {
		return v, err
	}
	err = json.Unmarshal([]byte(data), &v)
	return v, err
}
func (s *Store) Put(v Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v.CompactionIdleMinutes < 1 || v.CompactionIdleMinutes > 1440 {
		return fmt.Errorf("idle minutes must be 1..1440")
	}
	if v.DefaultChatSource == "" {
		return fmt.Errorf("chat source is required")
	}
	for k := range v.Modules {
		if k != "registry" && k != "usage" && k != "bridge" && k != "plugins" {
			return fmt.Errorf("unknown module: %s", k)
		}
	}
	if len(v.Modules) != 4 {
		return fmt.Errorf("all four module settings are required")
	}
	err := storage.Transaction(context.Background(), s.db, func(tx *sql.Tx) error {
		var rev int
		if err := tx.QueryRow(`SELECT revision FROM settings WHERE id=1`).Scan(&rev); err != nil {
			return err
		}
		if rev != v.Revision {
			return ErrConflict
		}
		v.Revision++
		b, _ := json.Marshal(v)
		_, err := tx.Exec(`UPDATE settings SET revision=?,data=? WHERE id=1`, v.Revision, string(b))
		return err
	})
	if err == nil {
		s.safe.Store(v.SafeMode)
	}
	return err
}

func (s *Store) Dispatch(fn func() error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.Safe() {
		return ErrSafeMode
	}
	return fn()
}
