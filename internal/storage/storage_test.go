package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestTransactionFailureRollsBack(t *testing.T) {
	db, e := Open(filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = Migrate(db, "test", []string{`CREATE TABLE example (id INTEGER PRIMARY KEY, value TEXT)`}); e != nil {
		t.Fatal(e)
	}
	e = Transaction(context.Background(), db, func(tx *sql.Tx) error {
		if _, e := tx.Exec(`INSERT INTO example VALUES(1,'first')`); e != nil {
			return e
		}
		_, e := tx.Exec(`INSERT INTO example VALUES(1,'duplicate')`)
		return e
	})
	if e == nil {
		t.Fatal("expected constraint failure")
	}
	var n int
	if e = db.QueryRow(`SELECT count(*) FROM example`).Scan(&n); e != nil || n != 0 {
		t.Fatalf("rollback n=%d err=%v", n, e)
	}
}
func TestFailedMigrationDoesNotAdvanceVersion(t *testing.T) {
	db, e := Open(filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = Migrate(db, "bad", []string{`CREATE TABLE partial(id INTEGER); INVALID SQL`}); e == nil {
		t.Fatal("expected failure")
	}
	var n int
	if e = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='partial'`).Scan(&n); e != nil || n != 0 {
		t.Fatal("partial table survived")
	}
	if e = Migrate(db, "good", []string{`CREATE TABLE valid(id INTEGER)`}); e != nil {
		t.Fatal(e)
	}
	if e = Migrate(db, "good", nil); e == nil {
		t.Fatal("downgrade must fail")
	}
	if !errors.Is(Transaction(context.Background(), db, func(*sql.Tx) error { return context.Canceled }), context.Canceled) {
		t.Fatal("transaction error lost")
	}
}
