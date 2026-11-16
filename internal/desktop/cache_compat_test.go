package desktop

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"enhancedkimi/internal/storage"
)

// monitorService builds a service whose official client answers /oauth/usage with a
// fixed body and whose cache layer reads an isolated home.
func monitorService(t *testing.T, home string) (*NativeService, *sql.DB) {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/oauth/usage" {
			writeNativeTestEnvelope(w, map[string]any{"kind": "ok", "quota": map[string]any{"usages": map[string]any{
				"limit5h": map[string]any{"usedRatio": 0.25, "resetAt": "2026-11-16T20:00:00Z"},
			}}})
			return
		}
		writeNativeTestEnvelope(w, map[string]any{"items": []any{}})
	})
	return newAutomationTestServiceWithHome(t, handler, home)
}

// TestMonitorSummaryKeepsReceiptInCacheValue is the P2-1 regression.
//
// MonitorSurface reads the automation receipt out of `data.cache.value`
// (`asRecord(data?.cache?.value)`), and the pre-M3 action put it exactly there. The
// M3 compatibility layer moved it to a top-level `receipt` and put a hit rate in
// `value`, which silently blanked the receipt block on the built-in Monitor. The old
// key has to keep carrying the receipt object.
func TestMonitorSummaryKeepsReceiptInCacheValue(t *testing.T) {
	service, db := monitorService(t, t.TempDir())
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_intents(id,kind,host_id,session_id,model_id,context_revision,state,created_at,updated_at,result) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		"intent-1", "keepalive", "native", "s1", "", "r1", "verified", storage.SortableTime(now), storage.SortableTime(now), "主代理下一步命中缓存"); err != nil {
		t.Fatal(err)
	}

	summary := service.monitorSummaryCompat(ctx)
	cacheField, ok := summary["cache"].(map[string]any)
	if !ok {
		t.Fatalf("cache field type = %T", summary["cache"])
	}
	receipt, ok := cacheField["value"].(map[string]any)
	if !ok {
		t.Fatalf("cache.value = %#v, want the receipt object (MonitorSurface reads kind/state/at/note from it)", cacheField["value"])
	}
	if receipt["kind"] != "keepalive" {
		t.Fatalf("receipt kind = %v", receipt["kind"])
	}
	if receipt["state"] != "verified" {
		t.Fatalf("receipt state = %v", receipt["state"])
	}
	if _, exists := receipt["note"]; !exists {
		t.Fatal("receipt note missing")
	}
	if _, exists := receipt["at"]; !exists {
		t.Fatal("receipt at missing")
	}
	// The legacy surface uppercases the state in cache.state; keep that.
	if cacheField["state"] != "VERIFIED" {
		t.Fatalf("cache.state = %v, want VERIFIED", cacheField["state"])
	}
	// No new top-level receipt key: the old contract is the only one in play while
	// Monitor is still the built-in surface.
	if _, exists := summary["receipt"]; exists {
		t.Fatal("top-level receipt key reappeared; MonitorSurface never reads it")
	}
}

// TestMonitorSummaryWithoutReceiptDoesNotClaimValueSlot makes sure the numbers only
// appear when the reserved slot is free.
func TestMonitorSummaryWithoutReceiptDoesNotClaimValueSlot(t *testing.T) {
	service, _ := monitorService(t, t.TempDir())
	summary := service.monitorSummaryCompat(context.Background())
	cacheField, ok := summary["cache"].(map[string]any)
	if !ok {
		t.Fatalf("cache field type = %T", summary["cache"])
	}
	if _, exists := cacheField["value"]; exists {
		t.Fatalf("cache.value = %#v with no receipt; the slot is reserved for the receipt", cacheField["value"])
	}
	// Quota still maps through the old shape.
	quota, ok := summary["quota"].(map[string]any)
	if !ok || quota["state"] != "AVAILABLE" {
		t.Fatalf("quota = %#v, want AVAILABLE", summary["quota"])
	}
}

// TestAutomationIntentsLegacyTimestampsAreBackfilled covers the table the reviewer
// asked about: its cooldown, supersede and expire queries all compare timestamps as
// text, so a legacy variable-width row must not survive a restart.
func TestAutomationIntentsLegacyTimestampsAreBackfilled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "intents.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewNativeService(db, NewKAPClient(""), t.TempDir(), func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	service.Close()
	db.Close()

	// Reopen and write a legacy row by hand, then construct a service again.
	db, err = storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	whole := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	frac := whole.Add(359 * time.Millisecond)
	for index, at := range []time.Time{whole, frac} {
		if _, err = db.Exec(`INSERT INTO automation_intents(id,kind,host_id,session_id,model_id,context_revision,state,created_at,updated_at,result) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			"legacy-"+string(rune('a'+index)), "keepalive", "native", "s1", "", "r1", "verified", at.UTC().Format(time.RFC3339Nano), at.UTC().Format(time.RFC3339Nano), ""); err != nil {
			t.Fatal(err)
		}
	}
	var first string
	if err = db.QueryRow(`SELECT id FROM automation_intents ORDER BY created_at ASC LIMIT 1`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if first != "legacy-b" {
		t.Fatalf("precondition: legacy order already correct (%s); the fixture no longer reproduces the bug", first)
	}

	reopened, err := NewNativeService(db, NewKAPClient(""), t.TempDir(), func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = db.QueryRow(`SELECT id FROM automation_intents ORDER BY created_at ASC LIMIT 1`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if first != "legacy-a" {
		t.Fatalf("after backfill the oldest intent is %s, want legacy-a", first)
	}
	rows, err := db.Query(`SELECT length(created_at),length(updated_at) FROM automation_intents`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var created, updated int
		if err = rows.Scan(&created, &updated); err != nil {
			t.Fatal(err)
		}
		if created != len(storage.SortableLayout) || updated != len(storage.SortableLayout) {
			t.Fatalf("widths = %d/%d, want %d", created, updated, len(storage.SortableLayout))
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}
