package cache

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"enhancedkimi/internal/storage"
)

// TestConcurrentRealCallersMatchProduction drives one Store the way production does.
//
// The settings page fires cache.summary, cache.sessions, cache.retention and
// cache.receipts in one Promise.all, the automation tick scans every five seconds and
// the keepalive closed loop reads LastMainStep/StepAfter from its own goroutine. All
// of that reaches one Store. Before the store synchronized home/files/lastScan and
// serialized scans, -race reported a write on lastScan, and concurrent scans could
// both read the same offset and insert the same steps twice.
func TestConcurrentRealCallersMatchProduction(t *testing.T) {
	home := t.TempDir()
	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	lines := []string{}
	for index := 0; index < 12; index++ {
		at := base.Add(time.Duration(index) * time.Minute)
		usage := sampleUsage(100+index, 40, 10, 0)
		lines = append(lines,
			plainLine(usageRecord("synthetic/concurrent", usage, "turn", at)),
			plainLine(stepEnd(fmt.Sprintf("uuid-c%d", index), "0", index+1, usage, 500, 1200, "tool_use", at)))
	}
	writeWire(t, home, "session_concurrent", "main", lines...)
	store := newTestStore(t, home)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	var wait sync.WaitGroup
	errs := make(chan error, 64)
	report := func(err error) {
		if err != nil {
			select {
			case errs <- err:
			default:
			}
		}
	}
	// Two scanners, the way the tick and each cache.* action both call Scan.
	for worker := 0; worker < 2; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for round := 0; round < 12 && ctx.Err() == nil; round++ {
				_, err := store.Scan(ctx, base.Add(time.Duration(round)*time.Second), false)
				report(err)
				_, err = store.Scan(ctx, base.Add(time.Duration(round)*time.Second), true)
				report(err)
			}
		}()
	}
	// Readers, mirroring the four parallel page requests plus keepalive's two reads.
	readers := []func(context.Context, time.Time) error{
		func(ctx context.Context, now time.Time) error { _, err := store.Summary(ctx, now); return err },
		func(ctx context.Context, now time.Time) error { _, err := store.Sessions(ctx, now, 20); return err },
		func(ctx context.Context, now time.Time) error { _, err := store.Retention(ctx, now); return err },
		func(ctx context.Context, now time.Time) error { _, err := store.History(ctx, now, "24h"); return err },
		func(ctx context.Context, _ time.Time) error {
			_, _, err := store.LastMainStep(ctx, "session_concurrent")
			return err
		},
		func(ctx context.Context, _ time.Time) error {
			_, _, err := store.StepAfter(ctx, "session_concurrent", base)
			return err
		},
		func(ctx context.Context, _ time.Time) error {
			_, err := store.Live(ctx, "session_concurrent")
			return err
		},
		func(ctx context.Context, _ time.Time) error {
			_, err := store.AllSessions(ctx, time.Now().UTC())
			return err
		},
		func(_ context.Context, _ time.Time) error { _ = store.Home(); return nil },
	}
	for _, reader := range readers {
		reader := reader
		wait.Add(1)
		go func() {
			defer wait.Done()
			for round := 0; round < 12 && ctx.Err() == nil; round++ {
				report(reader(ctx, time.Now().UTC()))
			}
		}()
	}
	// SetHome runs concurrently too: production calls it on every cacheStore(), so a
	// repoint must never race a scan that is already reading.
	wait.Add(1)
	go func() {
		defer wait.Done()
		for round := 0; round < 40 && ctx.Err() == nil; round++ {
			store.SetHome(home)
			store.SetHome(home + "/nested")
			store.SetHome(home)
		}
	}()
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	final := newTestStore(t, home)
	if _, err := final.Scan(context.Background(), time.Now().UTC(), true); err != nil {
		t.Fatal(err)
	}
	steps, err := final.MainSteps(context.Background(), "session_concurrent")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 12 {
		t.Fatalf("steps = %d, want 12: concurrent scans duplicated or dropped rows", len(steps))
	}
	seen := map[string]bool{}
	for _, step := range steps {
		if seen[step.StepUUID] {
			t.Fatalf("duplicate step %s", step.StepUUID)
		}
		seen[step.StepUUID] = true
	}
}

// TestStepAfterBoundaryInsideOneSecond is the P2-2 regression for StepAfter and the
// keepalive closed loop.
//
// time.RFC3339Nano trims trailing zeros, so a whole second was stored as
// "…:40Z" and a fractional one as "…:40.359Z". Text order puts 'Z' (0x5A) after
// '.' (0x2E), so the whole second sorted AFTER the fraction and a keepalive sent at
// 40.1 could pick up the step at exactly 40.0 as its "next" step.
func TestStepAfterBoundaryInsideOneSecond(t *testing.T) {
	home := t.TempDir()
	second := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	usage := sampleUsage(1000, 40, 0, 0)
	hit := sampleUsage(1000, 40, 900, 0)
	writeWire(t, home, "session_order", "main",
		// whole second, no fraction
		plainLine(usageRecord("synthetic/order", usage, "turn", second)),
		plainLine(stepEnd("uuid-whole", "0", 1, usage, 500, 1200, "tool_use", second)),
		// same second, 359ms later
		plainLine(usageRecord("synthetic/order", hit, "turn", second.Add(359*time.Millisecond))),
		plainLine(stepEnd("uuid-frac", "0", 2, hit, 500, 1200, "end_turn", second.Add(359*time.Millisecond))),
		// the following whole second
		plainLine(usageRecord("synthetic/order", usage, "turn", second.Add(time.Second))),
		plainLine(stepEnd("uuid-next", "0", 3, usage, 500, 1200, "end_turn", second.Add(time.Second))),
	)
	store := newTestStore(t, home)
	if _, err := store.Scan(context.Background(), second.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}

	all, err := store.MainSteps(context.Background(), "session_order")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"uuid-whole", "uuid-frac", "uuid-next"}
	if len(all) != len(want) {
		t.Fatalf("steps = %d, want %d", len(all), len(want))
	}
	for index, uuid := range want {
		if all[index].StepUUID != uuid {
			t.Fatalf("order[%d] = %s, want %s (stored times must be text-sortable)", index, all[index].StepUUID, uuid)
		}
	}

	// LastMainStep must return the truly newest row, which is the next whole second.
	last, found, err := store.LastMainStep(context.Background(), "session_order")
	if err != nil || !found {
		t.Fatalf("LastMainStep = %+v, %v, %v", last, found, err)
	}
	if last.StepUUID != "uuid-next" {
		t.Fatalf("LastMainStep = %s, want uuid-next", last.StepUUID)
	}

	// A keepalive sent at 40.100 must not read the step at exactly 40.000.
	step, found, err := store.StepAfter(context.Background(), "session_order", second.Add(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("StepAfter found nothing after 40.100")
	}
	if step.StepUUID != "uuid-frac" {
		t.Fatalf("StepAfter(40.100) = %s, want uuid-frac", step.StepUUID)
	}

	// Exactly on a stored instant is not "after" it.
	step, found, err = store.StepAfter(context.Background(), "session_order", second)
	if err != nil {
		t.Fatal(err)
	}
	if !found || step.StepUUID != "uuid-frac" {
		t.Fatalf("StepAfter(40.000) = %s, want uuid-frac", step.StepUUID)
	}
}

// TestRetentionGapAcrossWholeSecondBoundary checks the same layout on the retention
// path: gaps are computed from parsed times, so the bucket a sample lands in must not
// depend on whether its timestamp happened to have a fractional part.
func TestRetentionGapAcrossWholeSecondBoundary(t *testing.T) {
	home := t.TempDir()
	start := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	// Two steps 90 seconds apart: the second lands in the 0–2 minute bucket. Both
	// timestamps are whole seconds, which is exactly where the old layout inverted.
	hit := sampleUsage(400, 90, 600, 0)
	writeWire(t, home, "session_gap", "main",
		plainLine(usageRecord("synthetic/gap", hit, "turn", start)),
		plainLine(stepEnd("uuid-g1", "0", 1, hit, 500, 1200, "tool_use", start)),
		plainLine(usageRecord("synthetic/gap", hit, "turn", start.Add(90*time.Second))),
		plainLine(stepEnd("uuid-g2", "0", 2, hit, 500, 1200, "end_turn", start.Add(90*time.Second))),
	)
	store := newTestStore(t, home)
	if _, err := store.Scan(context.Background(), start.Add(time.Hour), true); err != nil {
		t.Fatal(err)
	}
	retention, err := store.Retention(context.Background(), start.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, bucket := range retention.IdleBuckets {
		total += bucket.Samples
	}
	if total != 1 {
		t.Fatalf("retention samples = %d, want 1 across all buckets", total)
	}
	byLabel := map[string]RetentionBucket{}
	for _, bucket := range retention.IdleBuckets {
		byLabel[bucket.Label] = bucket
	}
	if bucket := byLabel["0–2 分钟"]; bucket.Samples != 1 {
		t.Fatalf("0-2 minute bucket = %+v, want the 90s gap", bucket)
	}
}

// TestLegacyTimestampRowsAreBackfilledAndSortCorrectly covers data this task already
// wrote: rows created before the layout was unified keep variable-width values and
// would keep sorting wrongly unless NewStore rewrites them.
func TestLegacyTimestampRowsAreBackfilledAndSortCorrectly(t *testing.T) {
	home := t.TempDir()
	second := time.Date(2026, 5, 28, 20, 26, 40, 0, time.UTC)
	db, err := storage.Open(filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Build the schema, then insert legacy rows by hand with the old layout.
	if _, err = NewStore(db, home); err != nil {
		t.Fatal(err)
	}
	legacy := []struct {
		uuid string
		at   time.Time
	}{
		{"legacy-whole", second},
		{"legacy-frac", second.Add(359 * time.Millisecond)},
	}
	for index, item := range legacy {
		if _, err = db.Exec(`INSERT INTO cache_steps(session_id,agent,step_uuid,model,time,turn_id,step_index,finish_reason,input_other,input_cache_read,input_cache_creation,output,first_token_ms,decode_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			"session_legacy", "main", item.uuid, "synthetic/legacy", item.at.UTC().Format(time.RFC3339Nano), "0", index+1, "end_turn", 1000, 0, 0, 40, 500, 1200); err != nil {
			t.Fatal(err)
		}
	}
	// Before backfill the text order is wrong: the whole second sorts last.
	var first string
	if err = db.QueryRow(`SELECT step_uuid FROM cache_steps WHERE session_id='session_legacy' ORDER BY time ASC,rowid ASC LIMIT 1`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if first != "legacy-frac" {
		t.Fatalf("precondition: legacy order already correct (%s); the fixture no longer reproduces the bug", first)
	}

	// A new store on the same database backfills both rows.
	store, err := NewStore(db, home)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := store.MainSteps(context.Background(), "session_legacy")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[0].StepUUID != "legacy-whole" || steps[1].StepUUID != "legacy-frac" {
		t.Fatalf("backfilled order = %+v, want legacy-whole then legacy-frac", steps)
	}
	var widths []int
	rows, err := db.Query(`SELECT length(time) FROM cache_steps WHERE session_id='session_legacy'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var width int
		if err = rows.Scan(&width); err != nil {
			t.Fatal(err)
		}
		widths = append(widths, width)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, width := range widths {
		if width != len(storage.SortableLayout) {
			t.Fatalf("row width = %d, want %d", width, len(storage.SortableLayout))
		}
	}
}

// TestUnparseableLegacyTimestampIsLeftAlone makes sure the backfill does not invent a
// time for a value it cannot read.
func TestUnparseableLegacyTimestampIsLeftAlone(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "broken.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = NewStore(db, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO cache_steps(session_id,agent,step_uuid,model,time,turn_id,step_index,finish_reason,input_other,input_cache_read,input_cache_creation,output,first_token_ms,decode_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"session_broken", "main", "uuid-broken", "synthetic/legacy", "not-a-timestamp", "0", 1, "end_turn", 1, 0, 0, 1, -1, -1); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var value string
	if err = db.QueryRow(`SELECT time FROM cache_steps WHERE step_uuid='uuid-broken'`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "not-a-timestamp" {
		t.Fatalf("unparseable timestamp was rewritten to %q", value)
	}
	// The row is still readable; it simply has no usable time.
	steps, err := store.MainSteps(context.Background(), "session_broken")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || !steps[0].At.IsZero() {
		t.Fatalf("steps = %+v, want one row with no time", steps)
	}
}

// TestScanDoesNotLoseStepsUnderConcurrentSetHome covers the repoint path: production
// calls SetHome on every cacheStore(), and a scan that started before the repoint must
// still finish without inserting a partial session.
func TestScanDoesNotLoseStepsUnderConcurrentSetHome(t *testing.T) {
	home := t.TempDir()
	base := time.Now().Add(-30 * time.Minute).UTC().Truncate(time.Millisecond)
	usage := sampleUsage(10, 40, 5, 0)
	writeWire(t, home, "session_repoint", "main",
		plainLine(usageRecord("synthetic/repoint", usage, "turn", base)),
		plainLine(stepEnd("uuid-repoint", "0", 1, usage, 500, 1200, "end_turn", base)))
	store := newTestStore(t, home)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for round := 0; round < 20; round++ {
			store.SetHome(home)
		}
	}()
	if _, err := store.Scan(context.Background(), base.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}
	<-done
	steps, err := store.MainSteps(context.Background(), "session_repoint")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].StepUUID != "uuid-repoint" {
		t.Fatalf("steps = %+v, want the single repoint step", steps)
	}
}

// TestBackfillIsIdempotent keeps the migration from rewriting already-correct rows on
// every construction.
func TestBackfillIsIdempotent(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "idem.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := NewStore(db, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 5, 28, 20, 26, 40, 0, time.UTC)
	if _, err = db.Exec(`INSERT INTO cache_steps(session_id,agent,step_uuid,model,time,turn_id,step_index,finish_reason,input_other,input_cache_read,input_cache_creation,output,first_token_ms,decode_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"session_idem", "main", "uuid-idem", "synthetic/legacy", at.UTC().Format(time.RFC3339Nano), "0", 1, "end_turn", 1, 0, 0, 1, -1, -1); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 3; round++ {
		if _, err = NewStore(db, t.TempDir()); err != nil {
			t.Fatal(err)
		}
	}
	var value string
	if err = db.QueryRow(`SELECT time FROM cache_steps WHERE step_uuid='uuid-idem'`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if want := storage.SortableTime(at); value != want {
		t.Fatalf("time = %q, want %q", value, want)
	}
}

// TestScanSkipsWhenHomeIsEmpty keeps the empty-home path from reading "/".
func TestScanSkipsWhenHomeIsEmpty(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewStore(db, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Scan(context.Background(), time.Now().UTC(), true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Sessions != 0 || result.Files != 0 {
		t.Fatalf("scan of an empty home read something: %+v", result)
	}
}
