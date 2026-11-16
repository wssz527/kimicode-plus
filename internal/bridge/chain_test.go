package bridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"enhancedkimi/internal/desktop"
	"enhancedkimi/internal/storage"
)

func chainDelivered(t *testing.T, service *Service, intent string, checkpoint, parent int, parentHash string, goal string) Record {
	t.Helper()
	task := baseTask(func(task *TaskPackage) {
		task.Goal = goal
		task.Checkpoint = checkpoint
		task.ParentCheckpoint = parent
		task.ParentHash = parentHash
		if parent > 0 {
			task.Direction = "CHAT_TO_CODE_CONTINUE"
			task.LinkedTaskID = "task-alpha"
			task.Source.MessageIDs = []string{"m1"}
			task.Messages = []Message{{ID: "m1", Role: "user", Text: goal}}
		}
	})
	record := submit(t, service, intent, task, "")
	if record.State != StateDelivered {
		t.Fatalf("checkpoint %d not delivered: %#v", checkpoint, record)
	}
	return record
}

// A newer checkpoint must retire its predecessors as valid parents; otherwise a
// stale parentHash silently forks the main chain.
func TestStaleParentCannotBranchAfterNewerDelivered(t *testing.T) {
	service := testDB(t)
	cp1 := chainDelivered(t, service, "chain/cp1", 1, 0, "", "第一个检查点")
	cp2 := chainDelivered(t, service, "chain/cp2", 2, 1, cp1.CheckpointHash, "第二个检查点")

	stale := baseTask(func(task *TaskPackage) {
		task.Direction = "CHAT_TO_CODE_CONTINUE"
		task.LinkedTaskID = "task-alpha"
		task.Checkpoint = 3
		task.ParentCheckpoint = 1
		task.ParentHash = cp1.CheckpointHash
	})
	_, err := service.Submit(context.Background(), SubmitRequest{Intent: "chain/cp3-stale", Task: stale, Confirmed: true})
	mustCode(t, err, "CHECKPOINT_BRANCH")

	record, _, listErr := service.List(context.Background(), 0, 20)
	if listErr != nil {
		t.Fatalf("list: %v", listErr)
	}
	for _, item := range record {
		if item.Intent == "chain/cp3-stale" {
			t.Fatalf("stale branch was persisted: %#v", item)
		}
	}
	if cp2.Receipt == nil || cp2.CheckpointHash == "" {
		t.Fatalf("cp2 receipt missing")
	}
}

// Two concurrent submissions with different checkpoints but the same parent
// must not both dispatch.
func TestConcurrentSuccessorsOfSameParentOnlyOneWins(t *testing.T) {
	service := testDB(t)
	cp1 := chainDelivered(t, service, "chain/cp1", 1, 0, "", "第一个检查点")

	const contenders = 8
	var wg sync.WaitGroup
	results := make([]Record, contenders)
	errs := make([]error, contenders)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			task := baseTask(func(task *TaskPackage) {
				task.Direction = "CHAT_TO_CODE_CONTINUE"
				task.LinkedTaskID = "task-alpha"
				task.Checkpoint = index + 2
				task.ParentCheckpoint = 1
				task.ParentHash = cp1.CheckpointHash
				task.Goal = "并发后继"
			})
			results[index], errs[index] = service.Submit(context.Background(), SubmitRequest{
				Intent: "chain/concurrent", Task: task, Confirmed: true,
			})
		}(i)
	}
	wg.Wait()
	delivered := 0
	for i, err := range errs {
		if err == nil && results[i].State == StateDelivered {
			delivered++
		}
	}
	if delivered != 1 {
		t.Fatalf("exactly one successor may be delivered, got %d", delivered)
	}
	var successors int
	if err := service.db.QueryRow(`SELECT COUNT(1) FROM task_chain_slots WHERE chain_task_id='task-alpha' AND session_id='mock-session-alpha' AND kind='PARENT' AND slot=1`).Scan(&successors); err != nil {
		t.Fatalf("count: %v", err)
	}
	if successors != 1 {
		t.Fatalf("parent slot must hold one successor, got %d", successors)
	}
}

// Changing the payload and the intent after a timeout must not bypass the
// unknown checkpoint: the checkpoint stays occupied until a reconcile advances it.
func TestUnknownCheckpointBlocksRetryWithNewIntent(t *testing.T) {
	adapter := desktop.NewMock()
	service := newServiceWithSafe(t, adapter, func() bool { return false })
	record := submit(t, service, "unknown/original", baseTask(), string(desktop.BehaviorTimeout))
	if record.State != StateUnknown {
		t.Fatalf("expected UNKNOWN, got %s", record.State)
	}

	changed := baseTask(func(task *TaskPackage) {
		task.Goal = "超时后改内容再试"
	})
	_, err := service.Submit(context.Background(), SubmitRequest{Intent: "unknown/retry-new-intent", Task: changed, Confirmed: true})
	mustCode(t, err, "CHECKPOINT_IN_USE")

	if submits, _ := adapter.Counters(); submits != 1 {
		t.Fatalf("unknown checkpoint was redelivered: %d submits", submits)
	}

	reconciled, err := service.Reconcile(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if reconciled.State != StateDelivered || reconciled.CheckpointHash == "" {
		t.Fatalf("reconcile did not advance: %#v", reconciled)
	}
	advanced := baseTask(func(task *TaskPackage) {
		task.Direction = "CHAT_TO_CODE_CONTINUE"
		task.LinkedTaskID = "task-alpha"
		task.Checkpoint = 2
		task.ParentCheckpoint = 1
		task.ParentHash = reconciled.CheckpointHash
	})
	next, err := service.Submit(context.Background(), SubmitRequest{Intent: "unknown/continue", Task: advanced, Confirmed: true})
	if err != nil {
		t.Fatalf("continuation after reconcile: %v", err)
	}
	if next.State != StateDelivered {
		t.Fatalf("continuation not delivered: %#v", next)
	}
}

// A rejected dispatch never reached the host, so releasing its checkpoint must
// allow an honest retry with a new intent.
func TestRejectedDispatchReleasesCheckpointForRetry(t *testing.T) {
	adapter := desktop.NewMock()
	service := newServiceWithSafe(t, adapter, func() bool { return false })
	_, err := service.Submit(context.Background(), SubmitRequest{
		Intent: "rejected/1", Task: baseTask(), Confirmed: true, MockBehavior: string(desktop.BehaviorOccupied),
	})
	mustCode(t, err, "SESSION_OCCUPIED")

	retry := submit(t, service, "rejected/2", baseTask(), "")
	if retry.State != StateDelivered {
		t.Fatalf("retry after rejection failed: %#v", retry)
	}
	if submits, _ := adapter.Counters(); submits != 2 {
		t.Fatalf("expected two attempts (one rejected, one ok), got %d", submits)
	}
}

type stateOmittingAdapter struct{ *desktop.Mock }

func (a *stateOmittingAdapter) Submit(ctx context.Context, submission desktop.Submission) (desktop.Receipt, error) {
	receipt, err := a.Mock.Submit(ctx, submission)
	receipt.State = ""
	return receipt, err
}

func (a *stateOmittingAdapter) Reconcile(ctx context.Context, request desktop.ReconcileRequest) (desktop.Receipt, bool, error) {
	receipt, found, err := a.Mock.Reconcile(ctx, request)
	receipt.State = ""
	return receipt, found, err
}

// A receipt without an explicit DELIVERED state is not proof of delivery.
func TestReceiptWithoutDeliveredStateIsUnknown(t *testing.T) {
	service := newServiceWithSafe(t, &stateOmittingAdapter{Mock: desktop.NewMock()}, func() bool { return false })
	record := submit(t, service, "state/missing", baseTask(), "")
	if record.State != StateUnknown {
		t.Fatalf("missing receipt state must stay UNKNOWN, got %s", record.State)
	}
	if record.Receipt != nil || record.CheckpointAdvanced {
		t.Fatalf("missing state advanced the checkpoint: %#v", record)
	}
	if _, err := service.Reconcile(context.Background(), record.ID); err == nil {
		t.Fatalf("reconcile must reject a receipt without DELIVERED state")
	}
	reloaded, err := service.loadRecord(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.State != StateUnknown || reloaded.Receipt != nil {
		t.Fatalf("invalid receipt mutated the record: %#v", reloaded)
	}
}

type zeroTimeAdapter struct{ *desktop.Mock }

func (a *zeroTimeAdapter) Submit(ctx context.Context, submission desktop.Submission) (desktop.Receipt, error) {
	receipt, err := a.Mock.Submit(ctx, submission)
	receipt.AcceptedAt = time.Time{}
	return receipt, err
}

// Zero accepted time must not be rendered as a fabricated year-1 timestamp.
func TestZeroAcceptedTimeIsNotFabricated(t *testing.T) {
	service := newServiceWithSafe(t, &zeroTimeAdapter{Mock: desktop.NewMock()}, func() bool { return false })
	record := submit(t, service, "time/zero", baseTask(), "")
	if record.Receipt == nil {
		t.Fatalf("missing receipt")
	}
	if record.Receipt.AcceptedAt != "" {
		t.Fatalf("unexpected accepted time: %q", record.Receipt.AcceptedAt)
	}
	var raw string
	if err := service.db.QueryRow(`SELECT receipt_json FROM task_outbox WHERE id=?`, record.ID).Scan(&raw); err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(raw, "0001-01-01") {
		t.Fatalf("zero time persisted: %s", raw)
	}
}

// A valid receipt from the mock carries a real timestamp, which must survive.
func TestDeliveredReceiptKeepsRealTimestamp(t *testing.T) {
	service := testDB(t)
	record := submit(t, service, "time/real", baseTask(), "")
	if record.Receipt == nil || record.Receipt.AcceptedAt == "" {
		t.Fatalf("expected accepted time: %#v", record.Receipt)
	}
	if _, err := time.Parse(time.RFC3339Nano, record.Receipt.AcceptedAt); err != nil {
		t.Fatalf("accepted time not RFC3339: %v", err)
	}
}

// List must page in SQL rather than loading the whole outbox.
func TestListUsesBoundedQuery(t *testing.T) {
	service := testDB(t)
	for i := 0; i < 25; i++ {
		task := baseTask(func(task *TaskPackage) {
			task.TaskID = "task-list-" + string(rune('a'+i))
			task.Checkpoint = 1
		})
		submit(t, service, "list/"+string(rune('a'+i)), task, "")
	}
	first, total, err := service.List(context.Background(), 0, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 25 || len(first) != 10 {
		t.Fatalf("total=%d len=%d", total, len(first))
	}
	second, _, err := service.List(context.Background(), 10, 10)
	if err != nil {
		t.Fatalf("list page 2: %v", err)
	}
	if len(second) != 10 || second[0].ID == first[0].ID {
		t.Fatalf("pagination did not advance")
	}
	empty, secondTotal, err := service.List(context.Background(), 100, 10)
	if err != nil {
		t.Fatalf("list tail: %v", err)
	}
	if len(empty) != 0 || secondTotal != 25 {
		t.Fatalf("tail wrong: len=%d total=%d", len(empty), secondTotal)
	}
}

// Legacy rows written before chain_task_id existed must be adopted, and live
// ones must keep occupying their slots.
func TestLegacyInflightRowAdoptsChainAndOccupiesSlot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := storage.Migrate(db, "bridge", migrations[:4]); err != nil {
		t.Fatalf("legacy migrate: %v", err)
	}
	legacyTask, _, err := canonicalTask(baseTask())
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO task_outbox
		(id,intent,payload_hash,state,task_id,direction,session_id,project_id,checkpoint,parent_checkpoint,parent_hash,
		 task_json,receipt_json,findings_json,checkpoint_hash,checkpoint_advanced,note,created_at,updated_at)
		VALUES ('task-legacy','legacy/1','deadbeef','INFLIGHT','task-alpha','CHAT_TO_CODE','mock-session-alpha','mock-project-alpha',1,0,'',?,'','[]','',0,'','t','t')`,
		string(legacyTask)); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	db.Close()

	reopen, err := storage.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopen.Close()
	service, err := New(reopen, desktop.NewMock(), func() bool { return false })
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	var chain string
	if err := reopen.QueryRow(`SELECT chain_task_id FROM task_outbox WHERE id='task-legacy'`).Scan(&chain); err != nil {
		t.Fatalf("chain: %v", err)
	}
	if chain != "task-alpha" {
		t.Fatalf("legacy chain not adopted: %q", chain)
	}
	record, err := service.loadRecord(context.Background(), "task-legacy")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if record.State != StateUnknown {
		t.Fatalf("legacy INFLIGHT must become UNKNOWN, got %s", record.State)
	}
	_, err = service.Submit(context.Background(), SubmitRequest{
		Intent: "legacy/retry", Task: baseTask(func(task *TaskPackage) { task.Goal = "旧记录占用" }), Confirmed: true,
	})
	mustCode(t, err, "CHECKPOINT_IN_USE")
}

// The chain hash must change when the checkpoint advances, so a stale hash can
// never be reused across checkpoints.
func TestCheckpointHashesAreUniquePerCheckpoint(t *testing.T) {
	service := testDB(t)
	cp1 := chainDelivered(t, service, "chain/cp1", 1, 0, "", "第一个检查点")
	cp2 := chainDelivered(t, service, "chain/cp2", 2, 1, cp1.CheckpointHash, "第二个检查点")
	if cp1.CheckpointHash == cp2.CheckpointHash {
		t.Fatalf("checkpoint hash must differ")
	}
	reuseCP1 := baseTask(func(task *TaskPackage) {
		task.Direction = "CHAT_TO_CODE_CONTINUE"
		task.LinkedTaskID = "task-alpha"
		task.Checkpoint = 3
		task.ParentCheckpoint = 2
		task.ParentHash = cp1.CheckpointHash
	})
	_, err := service.Submit(context.Background(), SubmitRequest{Intent: "chain/reuse", Task: reuseCP1, Confirmed: true})
	mustCode(t, err, "PARENT_HASH_MISMATCH")
}

// Reconcile on a rejected task must not query the adapter or resurrect it.
func TestReconcileOfRejectedRecordDoesNotQuery(t *testing.T) {
	adapter := desktop.NewMock()
	service := newServiceWithSafe(t, adapter, func() bool { return false })
	_, err := service.Submit(context.Background(), SubmitRequest{
		Intent: "reconcile/rejected", Task: baseTask(), Confirmed: true, MockBehavior: string(desktop.BehaviorOccupied),
	})
	mustCode(t, err, "SESSION_OCCUPIED")
	records, _, err := service.List(context.Background(), 0, 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("list: %v %#v", err, records)
	}
	_ = adapter
	before, _ := adapter.Counters()
	record, err := service.Reconcile(context.Background(), records[0].ID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	after, _ := adapter.Counters()
	if after != before {
		t.Fatalf("reconcile queried a rejected task")
	}
	if record.State != StateRejected {
		t.Fatalf("rejected record changed: %s", record.State)
	}
}

// The HTTP list endpoint must agree with the SQL total.
func TestHTTPListReportsSQLTotal(t *testing.T) {
	service := testDB(t)
	for i := 0; i < 5; i++ {
		task := baseTask(func(task *TaskPackage) {
			task.TaskID = "task-http-" + string(rune('a'+i))
			task.Checkpoint = 1
		})
		submit(t, service, "http-list/"+string(rune('a'+i)), task, "")
	}
	mux := http.NewServeMux()
	service.Register(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/enhance/v1/tasks?offset=1&limit=2", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d", recorder.Code)
	}
	var body struct {
		Items []Record `json:"items"`
		Total int      `json:"total"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 5 || len(body.Items) != 2 {
		t.Fatalf("total=%d items=%d", body.Total, len(body.Items))
	}
}

var _ = sql.ErrNoRows
var _ = errors.New
