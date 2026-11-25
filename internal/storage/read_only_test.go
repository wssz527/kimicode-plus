package storage

import (
	"path/filepath"
	"testing"
)

func TestReadOnlySnapshotCanReadDuringWriterTransaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.sqlite")
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Close() })
	if _, err := writer.Exec(`CREATE TABLE items(value INTEGER); INSERT INTO items VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	tx, err := writer.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO items VALUES (2)`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := reader.QueryRow(`SELECT count(*) FROM items`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("snapshot = %d, %v", count, err)
	}
	if _, err := reader.Exec(`INSERT INTO items VALUES (3)`); err == nil {
		t.Fatal("read-only database accepted a write")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := reader.QueryRow(`SELECT count(*) FROM items`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("snapshot after commit = %d, %v", count, err)
	}
}
