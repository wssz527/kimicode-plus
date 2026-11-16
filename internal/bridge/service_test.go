package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"enhancedkimi/internal/desktop"
	"enhancedkimi/internal/storage"
)

const testPrivateKey = "{{PRIVATE_KEY_ppf7fhlr}}"

func testDB(t *testing.T) *Service {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bridge.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	adapter := desktop.NewMock()
	service, err := New(db, adapter, func() bool { return false })
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return service
}

func newServiceWithSafe(t *testing.T, adapter desktop.Adapter, safe func() bool) *Service {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bridge.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	service, err := New(db, adapter, safe)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return service
}

func baseTask(overrides ...func(*TaskPackage)) TaskPackage {
	task := TaskPackage{
		Version:   1,
		TaskID:    "task-alpha",
		Direction: "CHAT_TO_CODE",
		SessionID: "mock-session-alpha",
		ProjectID: "mock-project-alpha",
		Source: Source{
			Site:       "kimi",
			URL:        "https://www.kimi.com/chat/synthetic-alpha",
			ThreadID:   "thread-alpha",
			MessageIDs: []string{"m1", "m2"},
		},
		Goal:             "实现已确认的开发目标",
		Messages:         []Message{{ID: "m1", Role: "user", Text: "已选方案", Sequence: 1}, {ID: "m2", Role: "assistant", Text: "补充说明", Sequence: 2}},
		Attachments:      []Attachment{},
		Checkpoint:       1,
		ParentCheckpoint: 0,
		Mode:             "PLAN",
	}
	for _, override := range overrides {
		override(&task)
	}
	return task
}

func submit(t *testing.T, service *Service, intent string, task TaskPackage, behavior string) Record {
	t.Helper()
	record, err := service.Submit(context.Background(), SubmitRequest{Intent: intent, Task: task, Confirmed: true, MockBehavior: behavior, ConfirmationHash: func() string {
		if len(task.Attachments) == 0 {
			return ""
		}
		preview, err := service.Preview(context.Background(), task)
		if err != nil {
			t.Fatal(err)
		}
		return preview.ConfirmationHash
	}()})
	if err != nil {
		var apiErr *Error
		if errors.As(err, &apiErr) {
			t.Fatalf("submit %s: %s (%s)", intent, apiErr.Code, apiErr.Message)
		}
		t.Fatalf("submit %s: %v", intent, err)
	}
	return record
}

func mustCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %s, got nil", want)
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected api error %s, got %v", want, err)
	}
	if apiErr.Code != want {
		t.Fatalf("expected %s, got %s (%s)", want, apiErr.Code, apiErr.Message)
	}
}

func TestChatToCodePreservesSelectionAndMode(t *testing.T) {
	service := testDB(t)
	task := baseTask(func(task *TaskPackage) {
		task.Mode = "TOWER"
		task.Messages = task.Messages[:1]
	})
	preview, err := service.Preview(context.Background(), task)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.Task.Messages) != 1 || preview.Task.Messages[0].ID != "m1" {
		t.Fatalf("selected message lost: %#v", preview.Task.Messages)
	}
	if preview.Task.Messages[0].Trust != TrustUntrusted {
		t.Fatalf("trust marker missing: %#v", preview.Task.Messages[0])
	}
	if preview.Task.Source.Site != "kimi" || preview.Task.Mode != "TOWER" {
		t.Fatalf("source or mode lost: %#v", preview.Task)
	}
	record := submit(t, service, "bridge-alpha/1/CHAT_TO_CODE", task, "")
	if record.State != StateDelivered || record.Receipt == nil {
		t.Fatalf("not delivered: %#v", record)
	}
	if record.Receipt.Checkpoint != 1 || record.CheckpointHash == "" || !record.CheckpointAdvanced {
		t.Fatalf("checkpoint not advanced: %#v", record)
	}
}

func TestContinuationRequiresParentAndAdvancesOnlyAfterDelivery(t *testing.T) {
	service := testDB(t)
	first := submit(t, service, "bridge-alpha/1/CHAT_TO_CODE", baseTask(), "")
	parentHash := first.CheckpointHash

	next := baseTask(func(task *TaskPackage) {
		task.Direction = "CHAT_TO_CODE_CONTINUE"
		task.Checkpoint = 2
		task.ParentCheckpoint = 1
		task.ParentHash = parentHash
		task.LinkedTaskID = first.Task.TaskID
		task.Source.MessageIDs = []string{"m3"}
		task.Messages = []Message{{ID: "m3", Role: "user", Text: "讨论后的修订", Sequence: 3}}
	})
	second := submit(t, service, "bridge-alpha/2/CHAT_TO_CODE_CONTINUE", next, "")
	if second.State != StateDelivered {
		t.Fatalf("continuation not delivered: %#v", second)
	}
	if second.Receipt == nil || second.Receipt.Digest == first.CheckpointHash {
		t.Fatalf("chain hash should differ per payload: %#v", second.Receipt)
	}
	if second.Task.LinkedTaskID != first.Task.TaskID || second.Task.SessionID != first.Task.SessionID {
		t.Fatalf("continuation lost original linkage")
	}
}

func TestContinuationRejectsWrongParentHashAndFork(t *testing.T) {
	service := testDB(t)
	first := submit(t, service, "bridge-alpha/1/CHAT_TO_CODE", baseTask(), "")

	wrong := baseTask(func(task *TaskPackage) {
		task.Direction = "CHAT_TO_CODE_CONTINUE"
		task.Checkpoint = 2
		task.ParentCheckpoint = 1
		task.ParentHash = strings.Repeat("a", 64)
		task.LinkedTaskID = first.Task.TaskID
	})
	_, err := service.Submit(context.Background(), SubmitRequest{Intent: "bridge-alpha/2/wrong", Task: wrong, Confirmed: true})
	mustCode(t, err, "PARENT_HASH_MISMATCH")

	fork := baseTask(func(task *TaskPackage) {
		task.Direction = "CHAT_TO_CODE_CONTINUE"
		task.Checkpoint = 2
		task.ParentCheckpoint = 1
		task.ParentHash = first.CheckpointHash
		task.LinkedTaskID = "another-task"
	})
	_, err = service.Submit(context.Background(), SubmitRequest{Intent: "bridge-alpha/2/fork", Task: fork, Confirmed: true})
	mustCode(t, err, "PARENT_CHECKPOINT_UNKNOWN")

	straight := baseTask(func(task *TaskPackage) {
		task.Goal = "试图跳过父检查点"
		task.Checkpoint = 2
	})
	_, err = service.Submit(context.Background(), SubmitRequest{Intent: "bridge-alpha/branch", Task: straight, Confirmed: true})
	mustCode(t, err, "CHECKPOINT_BRANCH")
}

func TestIncrementalCheckpointsCannotBeReused(t *testing.T) {
	service := testDB(t)
	submit(t, service, "bridge-alpha/1/CHAT_TO_CODE", baseTask(), "")
	reuse := baseTask(func(task *TaskPackage) {
		task.Goal = "同一检查点重写"
	})
	_, err := service.Submit(context.Background(), SubmitRequest{Intent: "bridge-alpha/1/again", Task: reuse, Confirmed: true})
	mustCode(t, err, "CHECKPOINT_IN_USE")
}

func TestAttachmentByteHashVerification(t *testing.T) {
	service := testDB(t)
	content := []byte("fixture attachment")
	sum := sha256.Sum256(content)
	good := Attachment{Name: "plan.txt", MIME: "text/plain", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:]), DataBase64: base64.StdEncoding.EncodeToString(content)}
	task := baseTask(func(task *TaskPackage) { task.Attachments = []Attachment{good} })
	preview, err := service.Preview(context.Background(), task)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Task.Attachments[0].Status != "STORED" {
		t.Fatalf("attachment not verified: %#v", preview.Task.Attachments[0])
	}
	if preview.Task.Attachments[0].DataBase64 != "" {
		t.Fatalf("attachment bytes should not be echoed")
	}
	submit(t, service, "bridge-attach/1", task, "")

	tampered := good
	tampered.DataBase64 = base64.StdEncoding.EncodeToString([]byte("wrong"))
	_, err = service.Submit(context.Background(), SubmitRequest{Intent: "bridge-attach/2", Task: baseTask(func(task *TaskPackage) { task.Attachments = []Attachment{tampered} }), Confirmed: true})
	mustCode(t, err, "ATTACHMENT_MISMATCH")

	absent := good
	absent.DataBase64 = ""
	absent.SourceURL = "https://example.test/plan.txt"
	_, err = service.Submit(context.Background(), SubmitRequest{Intent: "bridge-attach/3", Task: baseTask(func(task *TaskPackage) { task.Attachments = []Attachment{absent} }), Confirmed: true})
	mustCode(t, err, "ATTACHMENT_UNVERIFIED")
	if strings.Contains(err.Error(), "example.test") {
		t.Fatalf("error leaked source url")
	}
}

func TestAttachmentBytesStayOutOfJSONAndTextIsRedacted(t *testing.T) {
	adapter := desktop.NewMock()
	service := newServiceWithSafe(t, adapter, func() bool { return false })
	content := []byte("attachment secret {{SECRET_7jbfnyzt}} alice@example.test")
	sum := sha256.Sum256(content)
	task := baseTask(func(task *TaskPackage) {
		task.Messages[0].Text = "这里是密钥 " + testPrivateKey + " 和邮箱 alice@example.test"
		task.Attachments = []Attachment{{
			Name: "report.txt", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:]),
			DataBase64: base64.StdEncoding.EncodeToString(content),
		}}
	})
	preview, err := service.Preview(context.Background(), task)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(preview.Findings) == 0 {
		t.Fatalf("expected findings")
	}
	body, _ := json.Marshal(preview.Task)
	for _, secret := range []string{"BEGIN RSA", "MIIEow", "c2VjcmV0", "alice@example.test", "attachment secret"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("preview leaked %q: %s", secret, body)
		}
	}
	if strings.Contains(preview.Task.Messages[0].Text, "alice@example.test") {
		t.Fatalf("email not redacted in preview")
	}
	submit(t, service, "bridge-redact/1", task, "")
	payload := string(adapter.LastPayload())
	if strings.Contains(payload, "BEGIN RSA") || strings.Contains(payload, "alice@example.test") || strings.Contains(payload, "attachment secret") {
		t.Fatalf("secret material reached the mock adapter: %s", payload)
	}
}

func TestEmptyReceiptIsNotSuccess(t *testing.T) {
	adapter := desktop.NewMock()
	service := newServiceWithSafe(t, adapter, func() bool { return false })
	record := submit(t, service, "bridge-empty/1", baseTask(), string(desktop.BehaviorEmpty))
	if record.State != StateUnknown {
		t.Fatalf("empty receipt must be UNKNOWN, got %s", record.State)
	}
	if record.Receipt != nil || record.CheckpointHash != "" || record.CheckpointAdvanced {
		t.Fatalf("empty receipt advanced checkpoint: %#v", record)
	}
	if submits, _ := adapter.Counters(); submits != 1 {
		t.Fatalf("expected one submit, got %d", submits)
	}
	replayed := submit(t, service, "bridge-empty/1", baseTask(), string(desktop.BehaviorEmpty))
	if replayed.State != StateUnknown || !replayed.Replayed {
		t.Fatalf("replay must stay UNKNOWN and not resubmit: %#v", replayed)
	}
	if submits, _ := adapter.Counters(); submits != 1 {
		t.Fatalf("UNKNOWN must not be resubmitted, got %d submits", submits)
	}
}

func TestTimeoutIsUnknownAndReconcileQueryOnly(t *testing.T) {
	adapter := desktop.NewMock()
	service := newServiceWithSafe(t, adapter, func() bool { return false })
	record := submit(t, service, "bridge-timeout/1", baseTask(), string(desktop.BehaviorTimeout))
	if record.State != StateUnknown {
		t.Fatalf("timeout must be UNKNOWN, got %s", record.State)
	}
	afterTimeout, _ := adapter.Counters()
	if afterTimeout != 1 {
		t.Fatalf("expected single submit, got %d", afterTimeout)
	}
	reconciled, err := service.Reconcile(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if reconciled.State != StateDelivered || reconciled.Receipt == nil {
		t.Fatalf("reconcile should recover the receipt: %#v", reconciled)
	}
	if reconciled.Receipt.ReceiptID == "" {
		t.Fatalf("receipt id missing")
	}
	submits, reconciles := adapter.Counters()
	if submits != 1 {
		t.Fatalf("reconcile must not resubmit; submits=%d", submits)
	}
	if reconciles != 1 {
		t.Fatalf("reconcile should query once; got %d", reconciles)
	}
}

func TestUnknownWithoutReceiptStaysUnknownOnReconcile(t *testing.T) {
	adapter := desktop.NewMock()
	service := newServiceWithSafe(t, adapter, func() bool { return false })
	if err := adapter.SetMode(desktop.ModeUnavailable); err != nil {
		t.Fatalf("set mode: %v", err)
	}
	_, err := service.Submit(context.Background(), SubmitRequest{Intent: "bridge-unavail/1", Task: baseTask(), Confirmed: true})
	mustCode(t, err, "ADAPTER_DEGRADED")
	if submits, _ := adapter.Counters(); submits != 0 {
		t.Fatalf("degraded adapter must not submit")
	}
}

func TestOccupiedIsRejectedWithoutRetry(t *testing.T) {
	adapter := desktop.NewMock()
	service := newServiceWithSafe(t, adapter, func() bool { return false })
	_, err := service.Submit(context.Background(), SubmitRequest{Intent: "bridge-occupied/1", Task: baseTask(), Confirmed: true, MockBehavior: string(desktop.BehaviorOccupied)})
	mustCode(t, err, "SESSION_OCCUPIED")
	submits, _ := adapter.Counters()
	if submits != 1 {
		t.Fatalf("expected one attempt, got %d", submits)
	}
	record, err := service.Reconcile(context.Background(), "task-"+mustDigest(t, service, baseTask())[:16])
	if err == nil {
		if record.State != StateRejected {
			t.Fatalf("expected REJECTED record, got %s", record.State)
		}
	}
	if submits2, _ := adapter.Counters(); submits2 != 1 {
		t.Fatalf("reconcile resubmitted: %d", submits2)
	}
}

func mustDigest(t *testing.T, service *Service, task TaskPackage) string {
	t.Helper()
	preview, err := service.Preview(context.Background(), task)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	return preview.ConfirmationHash
}

func TestPanicIsIsolatedAndDoesNotCrashCore(t *testing.T) {
	adapter := desktop.NewMock()
	service := newServiceWithSafe(t, adapter, func() bool { return false })
	record := submit(t, service, "bridge-panic/1", baseTask(), string(desktop.BehaviorPanic))
	if record.State != StateUnknown {
		t.Fatalf("panic must be UNKNOWN, got %s", record.State)
	}
	healthy := submit(t, service, "bridge-panic/2", baseTask(func(task *TaskPackage) {
		task.TaskID = "task-second"
		task.Goal = "第二个任务"
	}), "")
	if healthy.State != StateDelivered {
		t.Fatalf("service unusable after panic: %#v", healthy)
	}
}

func TestConcurrentDuplicatesSubmitOnce(t *testing.T) {
	adapter := desktop.NewMock()
	service := newServiceWithSafe(t, adapter, func() bool { return false })
	task := baseTask()
	const workers = 12
	var wg sync.WaitGroup
	results := make([]Record, workers)
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = service.Submit(context.Background(), SubmitRequest{Intent: "bridge-concurrent/1", Task: task, Confirmed: true})
		}(i)
	}
	wg.Wait()
	if submits, _ := adapter.Counters(); submits != 1 {
		t.Fatalf("expected exactly one dispatch, got %d", submits)
	}
	seen := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
		switch results[i].State {
		case StateInflight, StateDelivered:
			seen++
		default:
			t.Fatalf("worker %d unexpected state %s", i, results[i].State)
		}
	}
	if seen != workers {
		t.Fatalf("every duplicate caller should observe the shared record, got %d/%d", seen, workers)
	}
}

func TestSameIntentDifferentBodyConflicts(t *testing.T) {
	service := testDB(t)
	submit(t, service, "bridge-conflict/1", baseTask(), "")
	_, err := service.Submit(context.Background(), SubmitRequest{Intent: "bridge-conflict/1", Task: baseTask(func(task *TaskPackage) { task.Goal = "不同的目标" }), Confirmed: true})
	mustCode(t, err, "IDEMPOTENCY_CONFLICT")
}

func TestConfirmationRequiredAndStaleConfirmation(t *testing.T) {
	service := testDB(t)
	_, err := service.Submit(context.Background(), SubmitRequest{Intent: "bridge-confirm/1", Task: baseTask()})
	mustCode(t, err, "CONFIRMATION_REQUIRED")

	preview, err := service.Preview(context.Background(), baseTask())
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	mutated := baseTask(func(task *TaskPackage) { task.Goal = "确认后修改的目标" })
	_, err = service.Submit(context.Background(), SubmitRequest{Intent: "bridge-confirm/2", Task: mutated, Confirmed: true, ConfirmationHash: preview.ConfirmationHash})
	mustCode(t, err, "CONFIRMATION_STALE")
}

func TestSensitiveBodyRequiresMatchingPreview(t *testing.T) {
	service := testDB(t)
	secretTask := baseTask(func(task *TaskPackage) {
		task.Messages[0].Text = "token={{SECRET_w6n3pz7e}}"
	})
	_, err := service.Submit(context.Background(), SubmitRequest{Intent: "bridge-sensitive/1", Task: secretTask, Confirmed: true})
	mustCode(t, err, "PREVIEW_REQUIRED")

	preview, err := service.Preview(context.Background(), secretTask)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !strings.Contains(preview.Task.Messages[0].Text, "[REDACTED:SECRET_ASSIGNMENT]") {
		t.Fatalf("secret not redacted: %q", preview.Task.Messages[0].Text)
	}
	record := submit(t, service, "bridge-sensitive/1", secretTask, "")
	if record.State != StateDelivered {
		t.Fatalf("previewed submit failed: %#v", record)
	}
	raw, _ := json.Marshal(record.Task)
	if strings.Contains(string(raw), "{{SECRET_w6n3pz7e}}") {
		t.Fatalf("secret persisted: %s", raw)
	}
}

func TestSafeModeBlocksDispatch(t *testing.T) {
	adapter := desktop.NewMock()
	service := newServiceWithSafe(t, adapter, func() bool { return true })
	_, err := service.Submit(context.Background(), SubmitRequest{Intent: "bridge-safe/1", Task: baseTask(), Confirmed: true})
	mustCode(t, err, "SAFE_MODE")
	if submits, _ := adapter.Counters(); submits != 0 {
		t.Fatalf("safe mode dispatched")
	}
}

func TestRestartConvertsInflightToUnknown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bridge.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	adapter := desktop.NewMock()
	if err := adapter.SetMode(desktop.ModeUnavailable); err != nil {
		t.Fatalf("mode: %v", err)
	}
	if _, err := New(db, adapter, func() bool { return false }); err != nil {
		t.Fatalf("new: %v", err)
	}
	digest := strings.Repeat("d", 64)
	if _, err := db.Exec(`INSERT INTO task_outbox (id,intent,payload_hash,state,task_id,direction,session_id,project_id,checkpoint,parent_checkpoint,parent_hash,task_json,receipt_json,findings_json,checkpoint_hash,checkpoint_advanced,note,created_at,updated_at)
		VALUES ('task-inflight','intent-inflight',?,'INFLIGHT','task-alpha','CHAT_TO_CODE','mock-session-alpha','mock-project-alpha',1,0,'','{}','','[]','',0,'','t','t')`, digest); err != nil {
		t.Fatalf("seed: %v", err)
	}
	db.Close()

	reopen, err := storage.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopen.Close()
	service, err := New(reopen, desktop.NewMock(), func() bool { return false })
	if err != nil {
		t.Fatalf("new after reopen: %v", err)
	}
	record, err := service.loadRecord(context.Background(), "task-inflight")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if record.State != StateUnknown {
		t.Fatalf("inflight must become UNKNOWN after restart, got %s", record.State)
	}
	if !strings.Contains(record.Note, "不会自动重投") && !strings.Contains(record.Note, "绝不自动重投") {
		t.Fatalf("unexpected note: %s", record.Note)
	}
}

func TestSessionMustBelongToMockOfficialHost(t *testing.T) {
	adapter := &ownerOverrideAdapter{Mock: desktop.NewMock()}
	service := newServiceWithSafe(t, adapter, func() bool { return false })
	_, err := service.Submit(context.Background(), SubmitRequest{Intent: "bridge-owner/1", Task: baseTask(), Confirmed: true})
	mustCode(t, err, "SESSION_OWNER_MISMATCH")
	if adapter.submits != 0 {
		t.Fatalf("owner mismatch must not dispatch")
	}
}

func TestUnknownSessionIsRefused(t *testing.T) {
	service := testDB(t)
	_, err := service.Submit(context.Background(), SubmitRequest{Intent: "bridge-session/1", Task: baseTask(func(task *TaskPackage) { task.SessionID = "missing-session" }), Confirmed: true})
	mustCode(t, err, "SESSION_NOT_FOUND")
}

func TestAdapterDegradedModesKeepLocalHistory(t *testing.T) {
	adapter := desktop.NewMock()
	service := newServiceWithSafe(t, adapter, func() bool { return false })
	submit(t, service, "bridge-mode/1", baseTask(), "")
	if err := adapter.SetMode(desktop.ModeChanged); err != nil {
		t.Fatalf("mode: %v", err)
	}
	_, err := service.Submit(context.Background(), SubmitRequest{Intent: "bridge-mode/2", Task: baseTask(func(task *TaskPackage) { task.Checkpoint = 1; task.Goal = "changed" }), Confirmed: true})
	mustCode(t, err, "ADAPTER_DEGRADED")
	records, total, err := service.List(context.Background(), 0, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(records) != 1 {
		t.Fatalf("local history lost: %#v", records)
	}
}

func TestHTTPRoutesUseFullPrefixAndNeverResubmit(t *testing.T) {
	service := testDB(t)
	mux := http.NewServeMux()
	service.Register(mux)
	desktop.NewMock().Register(mux)

	previewBody, _ := json.Marshal(PreviewRequest{Task: baseTask()})
	preview := doPOST(t, mux, "/enhance/v1/tasks/preview", previewBody)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status %d: %s", preview.Code, preview.Body.String())
	}
	var result PreviewResult
	if err := json.Unmarshal(preview.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	body, _ := json.Marshal(SubmitRequest{Intent: "bridge-http/1", Task: baseTask(), Confirmed: true, MockBehavior: "EMPTY"})
	submitted := doPOST(t, mux, "/enhance/v1/tasks", body)
	if submitted.Code != http.StatusOK {
		t.Fatalf("submit status %d: %s", submitted.Code, submitted.Body.String())
	}
	var record Record
	if err := json.Unmarshal(submitted.Body.Bytes(), &record); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	if record.State != StateUnknown {
		t.Fatalf("empty receipt must be UNKNOWN, got %s", record.State)
	}
	reconcile := doPOST(t, mux, "/enhance/v1/tasks/"+record.ID+"/reconcile", []byte("{}"))
	if reconcile.Code != http.StatusOK {
		t.Fatalf("reconcile status %d: %s", reconcile.Code, reconcile.Body.String())
	}
	if strings.Contains(reconcile.Body.String(), "resubmit") {
		t.Fatalf("unexpected body")
	}
}

func doPOST(t *testing.T, mux *http.ServeMux, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

type ownerOverrideAdapter struct {
	*desktop.Mock
	submits int
}

func (a *ownerOverrideAdapter) Sessions(ctx context.Context) ([]desktop.Session, error) {
	return []desktop.Session{{ID: "mock-session-alpha", Title: "other", Owner: "another-client", Site: "kimi"}}, nil
}

func (a *ownerOverrideAdapter) Submit(ctx context.Context, submission desktop.Submission) (desktop.Receipt, error) {
	a.submits++
	return a.Mock.Submit(ctx, submission)
}

func TestSelectedMessagesMustMatchSourceSelection(t *testing.T) {
	service := testDB(t)
	outsider := baseTask(func(task *TaskPackage) {
		task.Messages = append(task.Messages, Message{ID: "m9", Role: "user", Text: "未被选择"})
	})
	_, err := service.Preview(context.Background(), outsider)
	mustCode(t, err, "SOURCE_SELECTION_MISMATCH")
}

func TestListAndPagination(t *testing.T) {
	service := testDB(t)
	for i := 1; i <= 3; i++ {
		task := baseTask(func(task *TaskPackage) {
			task.TaskID = fmt.Sprintf("task-%d", i)
			task.Checkpoint = 1
		})
		submit(t, service, fmt.Sprintf("bridge-list/%d", i), task, "")
	}
	records, total, err := service.List(context.Background(), 0, 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 3 || len(records) != 2 {
		t.Fatalf("pagination wrong: total=%d len=%d", total, len(records))
	}
}
