package storage

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func Open(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	path = absolute
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("database must not be a symbolic link")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	return openSQLite(path, false)
}

// OpenReadOnly opens an existing database for snapshot queries while the core
// continues to write through the WAL connection.
func OpenReadOnly(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("database must be a regular file")
	}
	return openSQLite(absolute, true)
}

func openSQLite(path string, readOnly bool) (*sql.DB, error) {
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" {
		uriPath = "/" + uriPath
	}
	u := &url.URL{Scheme: "file", Path: uriPath}
	options := "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	if readOnly {
		options += "&mode=ro&_pragma=query_only(1)"
	} else {
		options += "&_pragma=journal_mode(WAL)"
	}
	db, err := sql.Open("sqlite", u.String()+options)
	if err != nil {
		return nil, err
	}
	if !readOnly {
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(4)
	}
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func Transaction(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func Migrate(db *sql.DB, module string, migrations []string) error {
	return Transaction(context.Background(), db, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_versions (module TEXT PRIMARY KEY, version INTEGER NOT NULL)`); err != nil {
			return err
		}
		var version int
		err := tx.QueryRow(`SELECT version FROM schema_versions WHERE module=?`, module).Scan(&version)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if version > len(migrations) {
			return fmt.Errorf("database module %s is newer than this application", module)
		}
		for i := version; i < len(migrations); i++ {
			if _, err := tx.Exec(migrations[i]); err != nil {
				return fmt.Errorf("migration %s/%d: %w", module, i+1, err)
			}
		}
		_, err = tx.Exec(`INSERT INTO schema_versions(module,version) VALUES(?,?) ON CONFLICT(module) DO UPDATE SET version=excluded.version`, module, len(migrations))
		return err
	})
}
