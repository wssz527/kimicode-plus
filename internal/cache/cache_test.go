package cache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enhancedkimi/internal/storage"
)

// line builds one wire.jsonl line in the real shape. Fixtures are synthesised
// because the official records must never be modified, and because the interesting
// cases (truncation, replacement, incomplete lines) are hard to catch live.
func stepEnd(uuid, turn string, step int, usage map[string]int, firstToken, decode int64, finish string, at time.Time) string {
	return fmt.Sprintf(`{"type":"context.append_loop_event","agentId":"main","event":{"type":"step.end","uuid":%q,"turnId":%q,"step":%d,"finishReason":%q,"usage":{"inputOther":%d,"output":%d,"inputCacheRead":%d,"inputCacheCreation":%d},"llmFirstTokenLatencyMs":%d,"llmStreamDurationMs":%d,"llmServerDecodeMs":%d,"llmClientConsumeMs":4,"messageId":"cmb-%s"},"time":%d}`,
		uuid, turn, step, finish,
		usage["inputOther"], usage["output"], usage["inputCacheRead"], usage["inputCacheCreation"],
		firstToken, decode+4, decode, uuid, at.UnixMilli())
	// hold the decode value so a caller can assert it round-trips
}

func usageRecord(model string, usage map[string]int, scope string, at time.Time) string {
	return fmt.Sprintf(`{"type":"usage.record","agentId":"main","model":%q,"usage":{"inputOther":%d,"output":%d,"inputCacheRead":%d,"inputCacheCreation":%d},"usageScope":%q,"time":%d}`,
		model, usage["inputOther"], usage["output"], usage["inputCacheRead"], usage["inputCacheCreation"], scope, at.UnixMilli())
}

func plainLine(value string) string { return value + "\n" }

// writeWire writes a wire.jsonl under a synthesised home and returns its path.
func writeWire(t *testing.T, home, session, agent string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", "wd_test", session, "agents", agent)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "wire.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(home, "sessions", "wd_test", session, "state.json")
	state := fmt.Sprintf(`{"id":%q,"cwd":"/tmp/synthetic","createdAt":%d,"updatedAt":%d,"agents":{"main":{"type":"main"}}}`, session, time.Now().Add(-time.Hour).UnixMilli(), time.Now().UnixMilli())
	if err := os.WriteFile(statePath, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestStore(t *testing.T, home string) *Store {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := NewStore(db, home)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func sampleUsage(other, output, read, creation int) map[string]int {
	return map[string]int{"inputOther": other, "output": output, "inputCacheRead": read, "inputCacheCreation": creation}
}

// TestIncrementalAppend reads the same file three times and asserts each scan only
// picks up the newly appended step.
func TestIncrementalAppend(t *testing.T) {
	home := t.TempDir()
	base := time.Now().Add(-30 * time.Minute).UTC().Truncate(time.Millisecond)
	first := plainLine(usageRecord("synthetic/model", sampleUsage(100, 50, 0, 0), "turn", base)) +
		plainLine(stepEnd("uuid-1", "0", 1, sampleUsage(100, 50, 0, 0), 900, 2000, "tool_use", base))
	path := writeWire(t, home, "session_append", "main", first)
	store := newTestStore(t, home)

	result, err := store.Scan(context.Background(), base.Add(time.Minute), true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Steps != 1 {
		t.Fatalf("first scan steps = %d, want 1", result.Steps)
	}

	handle, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	second := base.Add(30 * time.Second)
	_, err = handle.WriteString(plainLine(usageRecord("synthetic/model", sampleUsage(200, 60, 150, 0), "turn", second)) +
		plainLine(stepEnd("uuid-2", "0", 2, sampleUsage(200, 60, 150, 0), 400, 1500, "end_turn", second)))
	handle.Close()
	if err != nil {
		t.Fatal(err)
	}

	result, err = store.Scan(context.Background(), base.Add(2*time.Minute), true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Steps != 1 {
		t.Fatalf("append scan steps = %d, want 1 (only the new line)", result.Steps)
	}

	latest, ok, err := store.LastMainStep(context.Background(), "session_append")
	if err != nil || !ok {
		t.Fatalf("LastMainStep: ok=%v err=%v", ok, err)
	}
	if latest.StepUUID != "uuid-2" || latest.InputCacheRead != 150 || latest.Model != "synthetic/model" {
		t.Fatalf("latest step = %+v", latest)
	}
}

// TestIncrementalTruncate rewrites a shorter file and asserts the reader restarts
// instead of seeking past the new end.
func TestIncrementalTruncate(t *testing.T) {
	home := t.TempDir()
	base := time.Now().Add(-30 * time.Minute).UTC().Truncate(time.Millisecond)
	usage := sampleUsage(100, 50, 0, 0)
	lines := plainLine(usageRecord("synthetic/model", usage, "turn", base)) +
		plainLine(stepEnd("uuid-1", "0", 1, usage, 900, 2000, "tool_use", base))
	path := writeWire(t, home, "session_truncate", "main", lines)
	store := newTestStore(t, home)
	if _, err := store.Scan(context.Background(), base.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}

	// Truncate to something genuinely shorter than what was already read. The
	// rewritten file must be smaller than the previous offset, otherwise size alone
	// cannot reveal the truncation.
	short := base.Add(10 * time.Minute)
	replaced := plainLine(stepEnd("uuid-9", "0", 1, sampleUsage(9, 8, 0, 0), 800, 1800, "end_turn", short))
	if err := os.WriteFile(path, []byte(replaced), 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() >= 552 {
		t.Fatalf("fixture is not shorter than the first read: size=%v err=%v", info, err)
	}
	result, err := store.Scan(context.Background(), base.Add(2*time.Minute), true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Restarts == 0 {
		t.Fatalf("truncate did not report a restart: %+v", result)
	}
	latest, ok, _ := store.LastMainStep(context.Background(), "session_truncate")
	if !ok || latest.StepUUID != "uuid-9" {
		t.Fatalf("after truncate latest = %+v ok=%v", latest, ok)
	}
}

// TestIncrementalReplace swaps in a new file with a different inode. Size and
// mtime are irrelevant: replacement is decided by identity.
func TestIncrementalReplace(t *testing.T) {
	home := t.TempDir()
	base := time.Now().Add(-30 * time.Minute).UTC().Truncate(time.Millisecond)
	usage := sampleUsage(10, 40, 5, 0)
	path := writeWire(t, home, "session_replace", "main",
		plainLine(usageRecord("synthetic/model", usage, "turn", base)),
		plainLine(stepEnd("uuid-old", "0", 1, usage, 500, 1200, "end_turn", base)))
	store := newTestStore(t, home)
	if _, err := store.Scan(context.Background(), base.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}

	// Replace atomically: write a temp file and rename it over the original, so the
	// inode changes while the path stays the same.
	temp := path + ".tmp"
	fresh := base.Add(20 * time.Minute)
	body := plainLine(usageRecord("synthetic/model", usage, "turn", fresh)) +
		plainLine(stepEnd("uuid-new", "0", 1, usage, 500, 1200, "end_turn", fresh))
	if err := os.WriteFile(temp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temp, path); err != nil {
		t.Fatal(err)
	}
	result, err := store.Scan(context.Background(), base.Add(3*time.Minute), true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Restarts == 0 {
		t.Fatalf("replacement did not report a restart: %+v", result)
	}
	latest, ok, _ := store.LastMainStep(context.Background(), "session_replace")
	if !ok || latest.StepUUID != "uuid-new" {
		t.Fatalf("after replace latest = %+v ok=%v", latest, ok)
	}
}

// TestIncompleteLineKept asserts a half-written trailing line is not parsed and is
// completed on the next scan.
func TestIncompleteLineKept(t *testing.T) {
	home := t.TempDir()
	base := time.Now().Add(-30 * time.Minute).UTC().Truncate(time.Millisecond)
	usage := sampleUsage(100, 50, 0, 0)
	complete := plainLine(usageRecord("synthetic/model", usage, "turn", base)) +
		plainLine(stepEnd("uuid-1", "0", 1, usage, 900, 2000, "tool_use", base))
	path := writeWire(t, home, "session_partial", "main", complete)
	store := newTestStore(t, home)
	if _, err := store.Scan(context.Background(), base.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}

	// Append a line cut in the middle, exactly as a mid-flush read would see it.
	second := base.Add(30 * time.Second)
	full := plainLine(usageRecord("synthetic/model", usage, "turn", second)) +
		plainLine(stepEnd("uuid-2", "0", 2, usage, 400, 1500, "end_turn", second))
	cut := len(full) / 2
	handle, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = handle.WriteString(full[:cut]); err != nil {
		t.Fatal(err)
	}
	handle.Close()

	result, err := store.Scan(context.Background(), base.Add(2*time.Minute), true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Steps != 0 {
		t.Fatalf("partial line produced %d steps, want 0", result.Steps)
	}

	handle, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = handle.WriteString(full[cut:]); err != nil {
		t.Fatal(err)
	}
	handle.Close()

	result, err = store.Scan(context.Background(), base.Add(3*time.Minute), true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Steps != 1 {
		t.Fatalf("completed line produced %d steps, want 1", result.Steps)
	}
	latest, ok, _ := store.LastMainStep(context.Background(), "session_partial")
	if !ok || latest.StepUUID != "uuid-2" {
		t.Fatalf("latest after completing line = %+v ok=%v", latest, ok)
	}
}

// TestScanIntervalFloor asserts two scans closer than 5 seconds do not re-read.
func TestScanIntervalFloor(t *testing.T) {
	home := t.TempDir()
	base := time.Now().UTC().Truncate(time.Millisecond)
	usage := sampleUsage(10, 40, 5, 0)
	writeWire(t, home, "session_interval", "main",
		plainLine(usageRecord("synthetic/model", usage, "turn", base)),
		plainLine(stepEnd("uuid-1", "0", 1, usage, 500, 1200, "end_turn", base)))
	store := newTestStore(t, home)
	if _, err := store.Scan(context.Background(), base, false); err != nil {
		t.Fatal(err)
	}
	again, err := store.Scan(context.Background(), base.Add(2*time.Second), false)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Skipped {
		t.Fatalf("scan 2s later was not skipped: %+v", again)
	}
	later, err := store.Scan(context.Background(), base.Add(scanInterval+time.Second), false)
	if err != nil {
		t.Fatal(err)
	}
	if later.Skipped {
		t.Fatal("scan after the floor was skipped")
	}
}

// TestModelPairingAndUnknown covers the pairing rules: usageScope=session is
// ignored, an unmatched step stays UNKNOWN, and a usage-empty error step is not
// stored at all.
func TestModelPairingAndUnknown(t *testing.T) {
	home := t.TempDir()
	base := time.Now().Add(-20 * time.Minute).UTC().Truncate(time.Millisecond)
	matched := sampleUsage(100, 50, 30, 0)
	orphan := sampleUsage(400, 70, 20, 0)
	empty := sampleUsage(0, 0, 0, 0)
	writeWire(t, home, "session_pair", "main",
		plainLine(usageRecord("synthetic/paired", matched, "turn", base)),
		// A session-scoped record is a running total and must never be paired.
		plainLine(usageRecord("synthetic/session-scope", sampleUsage(9, 9, 9, 9), "session", base)),
		plainLine(stepEnd("uuid-matched", "0", 1, matched, 700, 1600, "tool_use", base)),
		plainLine(stepEnd("uuid-orphan", "0", 2, orphan, 700, 1600, "tool_use", base.Add(time.Second))),
		plainLine(stepEnd("uuid-empty", "0", 3, empty, 0, 0, "error", base.Add(2*time.Second))),
	)
	store := newTestStore(t, home)
	result, err := store.Scan(context.Background(), base.Add(time.Minute), true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Steps != 2 {
		t.Fatalf("steps = %d, want 2 (the usage-empty error step must be skipped)", result.Steps)
	}

	ctx := context.Background()
	var model string
	if err = store.db.QueryRowContext(ctx, `SELECT model FROM cache_steps WHERE step_uuid='uuid-matched'`).Scan(&model); err != nil {
		t.Fatal(err)
	}
	if model != "synthetic/paired" {
		t.Fatalf("matched step model = %q, want synthetic/paired", model)
	}
	if err = store.db.QueryRowContext(ctx, `SELECT model FROM cache_steps WHERE step_uuid='uuid-orphan'`).Scan(&model); err != nil {
		t.Fatal(err)
	}
	if model != ModelUnknown {
		t.Fatalf("orphan step model = %q, want %q", model, ModelUnknown)
	}
	var count int
	if err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cache_steps WHERE step_uuid='uuid-empty'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("a usage-empty error step was persisted")
	}
}

// TestSpeedUsesServerDecode pins the decode field choice. On the real records
// llmFirstTokenLatencyMs is a cumulative latency that often exceeds
// llmStreamDurationMs, so stream-minus-first-token goes negative; the decode
// duration must be llmServerDecodeMs.
func TestSpeedUsesServerDecode(t *testing.T) {
	home := t.TempDir()
	base := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Millisecond)
	usage := sampleUsage(1000, 800, 0, 0)
	// firstToken=9000 > streamDuration=2004: the subtraction case that must not be
	// used. decode=2000, so the honest rate is 800/2.0 = 400 tok/s.
	writeWire(t, home, "session_speed", "main",
		plainLine(usageRecord("synthetic/speed", usage, "turn", base)),
		plainLine(stepEnd("uuid-speed", "0", 1, usage, 9000, 2000, "end_turn", base)))
	store := newTestStore(t, home)
	if _, err := store.Scan(context.Background(), base, true); err != nil {
		t.Fatal(err)
	}
	summary, err := store.Summary(context.Background(), base.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if summary.Speed.State != Reported || summary.Speed.TokensPerSecond == nil {
		t.Fatalf("speed state = %+v", summary.Speed)
	}
	if got := *summary.Speed.TokensPerSecond; got < 399 || got > 401 {
		t.Fatalf("speed = %.2f tok/s, want 400", got)
	}
}

// TestNotReportedVersusLowHitRate separates "provider reports nothing" from "cache
// really missed". Both produce read=0/creation=0 vs read>0.
func TestNotReportedVersusLowHitRate(t *testing.T) {
	home := t.TempDir()
	base := time.Now().Add(-5 * time.Minute).UTC().Truncate(time.Millisecond)
	silent := sampleUsage(5000, 100, 0, 0)
	lowHit := sampleUsage(9700, 100, 300, 0)
	writeWire(t, home, "session_report", "main",
		plainLine(usageRecord("synthetic/silent", silent, "turn", base)),
		plainLine(stepEnd("uuid-s1", "0", 1, silent, 500, 1200, "tool_use", base)),
		plainLine(usageRecord("synthetic/lowhit", lowHit, "turn", base.Add(time.Second))),
		plainLine(stepEnd("uuid-l1", "0", 1, lowHit, 500, 1200, "tool_use", base.Add(time.Second))),
	)
	store := newTestStore(t, home)
	if _, err := store.Scan(context.Background(), base.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}
	summary, err := store.Summary(context.Background(), base.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	models := map[string]ModelBreakdown{}
	for _, model := range summary.Models {
		models[model.Model] = model
	}
	silentRow, ok := models["synthetic/silent"]
	if !ok {
		t.Fatal("synthetic/silent missing from summary")
	}
	if silentRow.Reported != NotReported || silentRow.HitRate != nil {
		t.Fatalf("silent model reported=%q hitRate=%v, want NOT_REPORTED and nil (never 0%%)", silentRow.Reported, silentRow.HitRate)
	}
	if silentRow.Note == "" {
		t.Fatal("silent model has no explanation note")
	}
	lowHitRow, ok := models["synthetic/lowhit"]
	if !ok {
		t.Fatal("synthetic/lowhit missing from summary")
	}
	if lowHitRow.Reported != Reported || lowHitRow.HitRate == nil {
		t.Fatalf("low-hit model reported=%q hitRate=%v, want a real number", lowHitRow.Reported, lowHitRow.HitRate)
	}
	if got := *lowHitRow.HitRate; got < 0.0299 || got > 0.0301 {
		t.Fatalf("low-hit rate = %.4f, want 0.03", got)
	}
}

// TestHitRateFormula checks input_cache_read over all input tokens.
func TestHitRateFormula(t *testing.T) {
	home := t.TempDir()
	base := time.Now().Add(-3 * time.Minute).UTC().Truncate(time.Millisecond)
	usage := sampleUsage(1000, 200, 3000, 0)
	writeWire(t, home, "session_rate", "main",
		plainLine(usageRecord("synthetic/rate", usage, "turn", base)),
		plainLine(stepEnd("uuid-rate", "0", 1, usage, 500, 1200, "end_turn", base)))
	store := newTestStore(t, home)
	if _, err := store.Scan(context.Background(), base.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}
	summary, err := store.Summary(context.Background(), base.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if summary.Today.HitRate == nil {
		t.Fatal("today hit rate is nil")
	}
	// 3000 / (1000 + 3000 + 0) = 0.75
	if got := *summary.Today.HitRate; got < 0.7499 || got > 0.7501 {
		t.Fatalf("hit rate = %.4f, want 0.75", got)
	}
	if summary.Today.InputCacheRead != 3000 || summary.Today.InputOther != 1000 || summary.Today.Output != 200 {
		t.Fatalf("totals = %+v", summary.Today)
	}
}

// TestRetentionBuckets checks the idle-gap buckets are measured from main-agent
// steps and that the first request after each gap is the one sampled.
func TestRetentionBuckets(t *testing.T) {
	home := t.TempDir()
	start := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Millisecond)
	hit := sampleUsage(400, 90, 600, 0)
	miss := sampleUsage(900, 90, 0, 0)
	writeWire(t, home, "session_retention", "main",
		plainLine(usageRecord("synthetic/r", hit, "turn", start)),
		plainLine(stepEnd("uuid-r1", "0", 1, hit, 500, 1200, "tool_use", start)),
		// 1 minute later -> 0-2 bucket, hit
		plainLine(usageRecord("synthetic/r", hit, "turn", start.Add(time.Minute))),
		plainLine(stepEnd("uuid-r2", "0", 2, hit, 500, 1200, "tool_use", start.Add(time.Minute))),
		// 20 minutes later -> 10-30 bucket, miss (read 0, creation 0 is NOT_REPORTED)
		plainLine(usageRecord("synthetic/r", miss, "turn", start.Add(21*time.Minute))),
		plainLine(stepEnd("uuid-r3", "0", 3, miss, 500, 1200, "end_turn", start.Add(21*time.Minute))),
	)
	writeWire(t, home, "session_retention_other", "main",
		plainLine(usageRecord("synthetic/a", hit, "turn", start)),
		plainLine(stepEnd("uuid-r4", "0", 1, hit, 500, 1200, "end_turn", start)),
		plainLine(usageRecord("synthetic/b", hit, "turn", start.Add(time.Minute))),
		plainLine(stepEnd("uuid-r5", "0", 2, hit, 500, 1200, "end_turn", start.Add(time.Minute))),
		plainLine(usageRecord("synthetic/b", miss, "turn", start.Add(4*time.Minute))),
		plainLine(stepEnd("uuid-r6", "0", 3, miss, 500, 1200, "end_turn", start.Add(4*time.Minute))),
	)
	store := newTestStore(t, home)
	if _, err := store.Scan(context.Background(), start.Add(time.Hour), true); err != nil {
		t.Fatal(err)
	}
	retention, err := store.Retention(context.Background(), start.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(retention.IdleBuckets) != len(retentionBounds) {
		t.Fatalf("buckets = %d, want %d", len(retention.IdleBuckets), len(retentionBounds))
	}
	byLabel := map[string]RetentionBucket{}
	for _, bucket := range retention.IdleBuckets {
		byLabel[bucket.Label] = bucket
	}
	first := byLabel["0–2 分钟"]
	if first.Samples != 2 || first.HitRate == nil || first.Reported != Reported {
		t.Fatalf("0-2 bucket = %+v", first)
	}
	if len(retention.ByModel) != 2 || retention.ByModel[0].Model != "synthetic/b" || retention.ByModel[1].Model != "synthetic/r" {
		t.Fatalf("unexpected retention models: %+v", retention.ByModel)
	}
	if buckets := retention.ByModel[0].IdleBuckets; buckets[0].Samples != 0 || buckets[1].Samples != 1 || buckets[1].Reported != NotReported {
		t.Fatalf("switching models must not count as that model's retention sample: %+v", buckets)
	}
	if buckets := retention.ByModel[1].IdleBuckets; buckets[0].Samples != 1 || buckets[0].HitRate == nil || *buckets[0].HitRate != 0.6 {
		t.Fatalf("other model polluted same-model samples: %+v", buckets)
	}
	if got := *first.HitRate; got < 0.5999 || got > 0.6001 {
		t.Fatalf("0-2 hit rate = %.4f, want 0.6", got)
	}
	twenty := byLabel["10–30 分钟"]
	if twenty.Samples != 1 || twenty.Reported != NotReported || twenty.HitRate != nil {
		t.Fatalf("10-30 bucket = %+v, want one NOT_REPORTED sample", twenty)
	}
	if buckets := byLabel["30 分钟以上"]; buckets.Samples != 0 || buckets.HitRate != nil {
		t.Fatalf("30m+ bucket = %+v, want empty", buckets)
	}
}

// TestSessionsIgnoreSubagentActivity is the core D8 rule: "time since the last
// request" must follow the main agent, and a busy subagent must not reset it.
func TestSessionsIgnoreSubagentActivity(t *testing.T) {
	home := t.TempDir()
	mainAt := time.Now().Add(-45 * time.Minute).UTC().Truncate(time.Millisecond)
	usage := sampleUsage(1000, 100, 0, 0)
	// The subagent produced a step much more recently than the main agent.
	subAt := time.Now().Add(-1 * time.Minute).UTC().Truncate(time.Millisecond)
	writeWire(t, home, "session_idle", "main",
		plainLine(usageRecord("synthetic/main", usage, "turn", mainAt)),
		plainLine(stepEnd("uuid-main", "0", 1, usage, 500, 1200, "end_turn", mainAt)))
	subDir := filepath.Join(home, "sessions", "wd_test", "session_idle", "agents", "agent-0")
	if err := os.MkdirAll(subDir, 0o700); err != nil {
		t.Fatal(err)
	}
	subStep := strings.ReplaceAll(plainLine(stepEnd("uuid-sub", "0", 1, usage, 500, 1200, "end_turn", subAt)), `"agentId":"main"`, `"agentId":"agent-0"`)
	if err := os.WriteFile(filepath.Join(subDir, "wire.jsonl"), []byte(subStep), 0o600); err != nil {
		t.Fatal(err)
	}

	store := newTestStore(t, home)
	now := time.Now().UTC()
	if _, err := store.Scan(context.Background(), now, true); err != nil {
		t.Fatal(err)
	}
	sessions, err := store.Sessions(context.Background(), now, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	row := sessions[0]
	if row.LastRequestAt == nil {
		t.Fatal("last request time is nil")
	}
	if row.IdleMinutes == nil || *row.IdleMinutes < 44 || *row.IdleMinutes > 46 {
		t.Fatalf("idle minutes = %v, want ~45 from the main agent", row.IdleMinutes)
	}
	if row.LastRequestAt.Sub(mainAt) > time.Second {
		t.Fatalf("last request at %v, want the main step at %v (subagent step was newer)", row.LastRequestAt, mainAt)
	}
}

// TestBackgroundAgentSignals asserts task.started/terminated bookkeeping, which is
// what lets keepalive tell "a subagent is still running" apart from session noise.
func TestBackgroundAgentSignals(t *testing.T) {
	home := t.TempDir()
	base := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Millisecond)
	usage := sampleUsage(100, 50, 0, 0)
	started := fmt.Sprintf(`{"type":"task.started","agentId":"main","info":{"taskId":"agent-abc","status":"running","detached":true,"startedAt":%d,"kind":"agent"},"time":%d}`+"\n", base.UnixMilli(), base.UnixMilli())
	// A real blocking window starts at the WaitFor tool.call, not at task.waitDelivered.
	waitCall := fmt.Sprintf(`{"type":"context.append_loop_event","agentId":"main","event":{"type":"tool.call","name":"WaitFor","toolCallId":"call-wait","uuid":"tc-1"},"time":%d}`+"\n", base.Add(time.Second).UnixMilli())
	// task.waitDelivered is the delivery of a wait that already finished: on a real record
	// it is followed by tool.result and then the next llm.request.
	waitDelivered := fmt.Sprintf(`{"type":"task.waitDelivered","agentId":"main","keys":["agent-abc\u0000completed"],"time":%d}`+"\n", base.Add(2*time.Second).UnixMilli())
	writeWire(t, home, "session_bg", "main",
		plainLine(usageRecord("synthetic/model", usage, "turn", base)),
		plainLine(stepEnd("uuid-1", "0", 1, usage, 500, 1200, "end_turn", base)),
		started, waitCall, waitDelivered)
	store := newTestStore(t, home)
	if _, err := store.Scan(context.Background(), base.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}
	signals, err := store.Live(context.Background(), "session_bg")
	if err != nil {
		t.Fatal(err)
	}
	if signals.OpenBackground != 1 {
		t.Fatalf("open background = %d, want 1", signals.OpenBackground)
	}
	// The WaitFor call is still outstanding, so the session is inside a blocking window.
	if !signals.Blocked {
		t.Fatal("an outstanding WaitFor call did not mark the session as blocked")
	}
	// task.waitDelivered alone must not be read as "blocking started": it is the
	// delivery of a finished wait. A record with only that event is not blocked.
	if !strings.Contains(string(mustRead(t, home, "session_bg")), "task.waitDelivered") {
		t.Fatal("fixture lost the waitDelivered event")
	}

	// The wait result and the task termination both clear the signals.
	terminated := fmt.Sprintf(`{"type":"task.terminated","agentId":"main","info":{"taskId":"agent-abc","status":"completed","detached":true,"startedAt":%d,"endedAt":%d,"kind":"agent"},"time":%d}`+"\n", base.UnixMilli(), base.Add(2*time.Minute).UnixMilli(), base.Add(2*time.Minute).UnixMilli())
	handle, err := os.OpenFile(filepath.Join(home, "sessions", "wd_test", "session_bg", "agents", "main", "wire.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	waitResult := fmt.Sprintf(`{"type":"context.append_loop_event","agentId":"main","event":{"type":"tool.result","toolCallId":"call-wait","uuid":"tr-1"},"time":%d}`+"\n", base.Add(90*time.Second).UnixMilli())
	if _, err = handle.WriteString(waitResult + terminated); err != nil {
		t.Fatal(err)
	}
	handle.Close()
	if _, err = store.Scan(context.Background(), base.Add(3*time.Minute), true); err != nil {
		t.Fatal(err)
	}
	signals, err = store.Live(context.Background(), "session_bg")
	if err != nil {
		t.Fatal(err)
	}
	if signals.OpenBackground != 0 || signals.Blocked {
		t.Fatalf("signals after termination = %+v, want both cleared", signals)
	}
}

// TestHistoryBuckets checks 24h is hourly and 7d/30d are daily, and an invalid span
// is rejected rather than silently defaulting.
func TestHistoryBuckets(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC().Truncate(time.Minute)
	usage := sampleUsage(100, 50, 20, 0)
	writeWire(t, home, "session_history", "main",
		plainLine(usageRecord("synthetic/model", usage, "turn", now.Add(-30*time.Minute))),
		plainLine(stepEnd("uuid-h1", "0", 1, usage, 500, 1200, "end_turn", now.Add(-30*time.Minute))))
	store := newTestStore(t, home)
	if _, err := store.Scan(context.Background(), now, true); err != nil {
		t.Fatal(err)
	}
	hourly, err := store.History(context.Background(), now, "24h")
	if err != nil {
		t.Fatal(err)
	}
	if hourly.Unit != "hour" || len(hourly.Points) < 24 {
		t.Fatalf("24h history unit=%q points=%d", hourly.Unit, len(hourly.Points))
	}
	daily, err := store.History(context.Background(), now, "7d")
	if err != nil {
		t.Fatal(err)
	}
	if daily.Unit != "day" || len(daily.Points) < 7 || len(daily.Points) > 9 {
		t.Fatalf("7d history unit=%q points=%d", daily.Unit, len(daily.Points))
	}
	if _, err = store.History(context.Background(), now, "90d"); err == nil {
		t.Fatal("90d should be rejected")
	}
}

// TestReadsPastOneBuffer is a regression test for a real defect found during
// acceptance. The official records contain individual lines of several hundred
// kilobytes (one observed line was 360962 bytes), so a file larger than the read
// buffer stopped being ingested at the first buffer boundary: a 21.8 MB file
// yielded only 14 steps instead of 257. The reader must keep reading within one
// call and leave only a trailing partial line unread.
func TestReadsPastOneBuffer(t *testing.T) {
	home := t.TempDir()
	base := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Millisecond)
	usage := sampleUsage(1000, 200, 500, 0)
	// A padding line longer than the 1 MiB read buffer.
	padding := strings.Repeat("x", 1<<20+4096)
	lines := []string{
		fmt.Sprintf(`{"type":"context.append_message","agentId":"main","message":{"role":"tool","content":[{"type":"text","text":%q}]},"time":%d}`+"\n", padding, base.UnixMilli()),
	}
	const wantSteps = 12
	for index := 0; index < wantSteps; index++ {
		at := base.Add(time.Duration(index) * time.Second)
		lines = append(lines,
			plainLine(usageRecord("synthetic/big", usage, "turn", at)),
			plainLine(stepEnd(fmt.Sprintf("uuid-big-%d", index), "0", index+1, usage, 500, 1200, "tool_use", at)),
		)
	}
	writeWire(t, home, "session_big", "main", lines...)
	store := newTestStore(t, home)

	result, err := store.Scan(context.Background(), base.Add(time.Hour), true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Steps != wantSteps {
		t.Fatalf("steps = %d, want %d: a file larger than the read buffer must be read to the end in one scan", result.Steps, wantSteps)
	}
	steps, err := store.MainSteps(context.Background(), "session_big")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != wantSteps {
		t.Fatalf("stored steps = %d, want %d", len(steps), wantSteps)
	}
}

// mustRead returns a record file's content for fixture assertions.
func mustRead(t *testing.T, home, session string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "sessions", "wd_test", session, "agents", "main", "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestWaitDeliveredIsNotBlockingStart pins the corrected semantics. On a real record
// the sequence around a finished wait is:
//
//	WaitFor tool.call -> tool.result -> task.waitDelivered -> tool.result -> llm.request
//
// so task.waitDelivered marks the END of a wait and must never set the blocked flag.
func TestWaitDeliveredIsNotBlockingStart(t *testing.T) {
	home := t.TempDir()
	base := time.Now().Add(-5 * time.Minute).UTC().Truncate(time.Millisecond)
	usage := sampleUsage(100, 50, 0, 0)
	delivered := fmt.Sprintf(`{"type":"task.waitDelivered","agentId":"main","keys":["x\u0000completed"],"time":%d}`+"\n", base.UnixMilli())
	writeWire(t, home, "session_waitdelivered", "main",
		plainLine(usageRecord("synthetic/model", usage, "turn", base)),
		plainLine(stepEnd("uuid-1", "0", 1, usage, 500, 1200, "end_turn", base)),
		delivered)
	store := newTestStore(t, home)
	if _, err := store.Scan(context.Background(), base.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}
	signals, err := store.Live(context.Background(), "session_waitdelivered")
	if err != nil {
		t.Fatal(err)
	}
	if signals.Blocked {
		t.Fatal("task.waitDelivered must not be treated as the start of a blocking wait")
	}
}

// TestBlockingWindowFollowsToolLifecycle asserts the blocked flag is raised by the
// blocking tool call and cleared by its result, including across a non-blocking call.
func TestBlockingWindowFollowsToolLifecycle(t *testing.T) {
	home := t.TempDir()
	base := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Millisecond)
	usage := sampleUsage(100, 50, 0, 0)
	line := func(value string) string { return value + "\n" }
	call := func(name, id string, at time.Time) string {
		return fmt.Sprintf(`{"type":"context.append_loop_event","agentId":"main","event":{"type":"tool.call","name":%q,"toolCallId":%q},"time":%d}`, name, id, at.UnixMilli())
	}
	result := func(id string, at time.Time) string {
		return fmt.Sprintf(`{"type":"context.append_loop_event","agentId":"main","event":{"type":"tool.result","toolCallId":%q},"time":%d}`, id, at.UnixMilli())
	}
	path := writeWire(t, home, "session_block", "main",
		plainLine(usageRecord("synthetic/model", usage, "turn", base)),
		plainLine(stepEnd("uuid-1", "0", 1, usage, 500, 1200, "tool_use", base)))
	store := newTestStore(t, home)
	ctx := context.Background()
	if _, err := store.Scan(ctx, base.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}
	appendLines := func(lines ...string) {
		handle, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = handle.WriteString(strings.Join(lines, "")); err != nil {
			handle.Close()
			t.Fatal(err)
		}
		handle.Close()
	}

	// A Bash call does not block the main agent.
	appendLines(line(call("Bash", "call-bash", base.Add(2*time.Minute))))
	if _, err := store.Scan(ctx, base.Add(3*time.Minute), true); err != nil {
		t.Fatal(err)
	}
	if signals, _ := store.Live(ctx, "session_block"); signals.Blocked {
		t.Fatal("a Bash call must not mark the session as blocked")
	}

	// A WaitFor call opens the window.
	appendLines(line(call("WaitFor", "call-wait", base.Add(4*time.Minute))))
	if _, err := store.Scan(ctx, base.Add(5*time.Minute), true); err != nil {
		t.Fatal(err)
	}
	if signals, _ := store.Live(ctx, "session_block"); !signals.Blocked {
		t.Fatal("an outstanding WaitFor call must mark the session as blocked")
	}

	// Its result closes the window.
	appendLines(line(result("call-wait", base.Add(6*time.Minute))))
	if _, err := store.Scan(ctx, base.Add(7*time.Minute), true); err != nil {
		t.Fatal(err)
	}
	if signals, _ := store.Live(ctx, "session_block"); signals.Blocked {
		t.Fatal("the WaitFor result must clear the blocked flag")
	}
}

// TestSubagentSpawnedTracksBackgroundSubagent pins the authoritative background
// subagent signal.
//
// Verified against a real record from the isolated host: a background Agent tool call
// produces
//
//	subagent.spawned  {subagentId, runInBackground: true}
//	task.started      {kind: agent, detached: true, agentId}
//	turn.ended        (main)
//	subagent.completed{subagentId}
//	task.terminated   {agentId}
//
// and a rejected Agent call produces no subagent or task events at all. Detection
// therefore cannot rely on task.started alone.
func TestSubagentSpawnedTracksBackgroundSubagent(t *testing.T) {
	home := t.TempDir()
	base := time.Now().Add(-40 * time.Minute).UTC().Truncate(time.Millisecond)
	usage := sampleUsage(1000, 200, 800, 0)
	spawned := fmt.Sprintf(`{"type":"subagent.spawned","subagentId":"agent-0","subagentName":"coder","parentToolCallId":"call_bg","runInBackground":true,"taskId":"agent-x","time":%d}`+"\n", base.UnixMilli())
	started := fmt.Sprintf(`{"type":"task.started","agentId":"main","info":{"taskId":"agent-x","kind":"agent","detached":true,"status":"running","startedAt":%d,"agentId":"agent-0"},"time":%d}`+"\n", base.UnixMilli(), base.UnixMilli())
	writeWire(t, home, "session_sub", "main",
		plainLine(usageRecord("synthetic/model", usage, "turn", base)),
		plainLine(stepEnd("uuid-main", "0", 1, usage, 500, 1200, "end_turn", base)),
		spawned, started)
	store := newTestStore(t, home)
	ctx := context.Background()
	if _, err := store.Scan(ctx, base.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}
	signals, err := store.Live(ctx, "session_sub")
	if err != nil {
		t.Fatal(err)
	}
	if signals.OpenBackground != 1 {
		t.Fatalf("open background = %d, want 1 while the background subagent runs", signals.OpenBackground)
	}

	// Completion closes it.
	completed := fmt.Sprintf(`{"type":"subagent.completed","subagentId":"agent-0","time":%d}`+"\n", base.Add(2*time.Minute).UnixMilli())
	terminated := fmt.Sprintf(`{"type":"task.terminated","agentId":"main","info":{"taskId":"agent-x","status":"completed","detached":true,"agentId":"agent-0","endedAt":%d},"time":%d}`+"\n", base.Add(2*time.Minute).UnixMilli(), base.Add(2*time.Minute).UnixMilli())
	handle, err := os.OpenFile(filepath.Join(home, "sessions", "wd_test", "session_sub", "agents", "main", "wire.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = handle.WriteString(completed + terminated); err != nil {
		handle.Close()
		t.Fatal(err)
	}
	handle.Close()
	if _, err = store.Scan(ctx, base.Add(3*time.Minute), true); err != nil {
		t.Fatal(err)
	}
	if signals, err = store.Live(ctx, "session_sub"); err != nil {
		t.Fatal(err)
	}
	if signals.OpenBackground != 0 {
		t.Fatalf("open background = %d after completion, want 0", signals.OpenBackground)
	}
}

// TestForegroundSubagentIsNotBackgroundCounted asserts a foreground spawn does not
// count as an unreturned background subagent.
func TestForegroundSubagentIsNotBackgroundCounted(t *testing.T) {
	home := t.TempDir()
	base := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Millisecond)
	usage := sampleUsage(1000, 200, 0, 0)
	spawned := fmt.Sprintf(`{"type":"subagent.spawned","subagentId":"agent-1","subagentName":"coder","runInBackground":false,"time":%d}`+"\n", base.UnixMilli())
	writeWire(t, home, "session_fg", "main",
		plainLine(usageRecord("synthetic/model", usage, "turn", base)),
		plainLine(stepEnd("uuid-main", "0", 1, usage, 500, 1200, "end_turn", base)),
		spawned)
	store := newTestStore(t, home)
	if _, err := store.Scan(context.Background(), base.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}
	signals, err := store.Live(context.Background(), "session_fg")
	if err != nil {
		t.Fatal(err)
	}
	if signals.OpenBackground != 0 {
		t.Fatalf("foreground subagent counted as background: %d", signals.OpenBackground)
	}
}

func TestHistorySplitsSubagentTotals(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC().Truncate(time.Minute)
	earlier := now.Add(-90 * time.Minute)
	recent := now.Add(-30 * time.Minute)
	mainUsage := sampleUsage(100, 40, 200, 0)
	subUsage := sampleUsage(50, 25, 100, 0)

	writeWire(t, home, "session_hist_sub", "main",
		plainLine(usageRecord("synthetic/main", mainUsage, "turn", earlier)),
		plainLine(stepEnd("uuid-m1", "0", 1, mainUsage, 400, 1000, "end_turn", earlier)),
		plainLine(usageRecord("synthetic/main", mainUsage, "turn", recent)),
		plainLine(stepEnd("uuid-m2", "1", 1, mainUsage, 400, 1000, "end_turn", recent)),
	)
	subDir := filepath.Join(home, "sessions", "wd_test", "session_hist_sub", "agents", "agent-1")
	if err := os.MkdirAll(subDir, 0o700); err != nil {
		t.Fatal(err)
	}
	subContent := plainLine(usageRecord("synthetic/sub", subUsage, "turn", recent)) +
		plainLine(stepEnd("uuid-s1", "1", 1, subUsage, 300, 800, "end_turn", recent))
	if err := os.WriteFile(filepath.Join(subDir, "wire.jsonl"), []byte(subContent), 0o600); err != nil {
		t.Fatal(err)
	}

	store := newTestStore(t, home)
	if _, err := store.Scan(context.Background(), now, true); err != nil {
		t.Fatal(err)
	}

	hourly, err := store.History(context.Background(), now, "24h")
	if err != nil {
		t.Fatal(err)
	}
	earlierBucket := earlier.In(now.Location()).Format("2006-01-02T15:00")
	recentBucket := recent.In(now.Location()).Format("2006-01-02T15:00")
	byBucket := map[string]HistoryPoint{}
	for _, point := range hourly.Points {
		byBucket[point.Bucket] = point
	}
	if p := byBucket[earlierBucket]; p.Totals.Output != 40 || p.Sub != nil {
		t.Fatalf("earlier bucket = %+v, want main output=40 and Sub=nil", p)
	}
	if p := byBucket[recentBucket]; p.Totals.Output != 65 || p.Sub == nil || p.Sub.Output != 25 || p.Sub.InputCacheRead != 100 {
		t.Fatalf("recent bucket = %+v, want total output=65 and Sub output=25 cacheRead=100", p)
	}

	daily, err := store.History(context.Background(), now, "7d")
	if err != nil {
		t.Fatal(err)
	}
	todayBucket := recent.In(now.Location()).Format("2006-01-02")
	var todayPoint *HistoryPoint
	for i := range daily.Points {
		if daily.Points[i].Bucket == todayBucket {
			todayPoint = &daily.Points[i]
			break
		}
	}
	if todayPoint == nil || todayPoint.Sub == nil || todayPoint.Sub.Output != 25 {
		t.Fatalf("daily today point = %+v, want Sub.Output=25", todayPoint)
	}
}

func TestScanSkippedWaitsForInFlightScan(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	usage := sampleUsage(120, 40, 300, 0)
	writeWire(t, home, "session_inflight", "main",
		plainLine(usageRecord("synthetic/inflight", usage, "turn", now)),
		plainLine(stepEnd("uuid-inf-1", "0", 1, usage, 400, 1000, "end_turn", now)),
	)
	store := newTestStore(t, home)

	// Simulate an active scan holding scanMu after claiming lastScan via beginScan.
	if _, ok := store.beginScan(now, false); !ok {
		t.Fatal("initial beginScan unexpectedly skipped")
	}
	store.scanMu.Lock()
	entered := make(chan struct{})
	done := make(chan ScanResult, 1)
	go func() {
		close(entered)
		res, err := store.Scan(context.Background(), now.Add(100*time.Millisecond), false)
		if err != nil {
			t.Errorf("concurrent skipped scan failed: %v", err)
		}
		done <- res
	}()
	<-entered
	select {
	case res := <-done:
		store.scanMu.Unlock()
		t.Fatalf("skipped scan returned early while scanMu held: %+v", res)
	case <-time.After(40 * time.Millisecond):
	}
	store.scanMu.Unlock()
	res := <-done
	if !res.Skipped {
		t.Fatalf("second scan result = %+v, want Skipped=true", res)
	}
}
