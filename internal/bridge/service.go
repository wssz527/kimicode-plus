package bridge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"enhancedkimi/internal/desktop"
	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/redact"
	"enhancedkimi/internal/storage"
)

const (
	StateInflight  = "INFLIGHT"
	StateDelivered = "DELIVERED"
	StateUnknown   = "UNKNOWN"
	StateRejected  = "REJECTED"
)

// liveStates are the outbox states that keep a checkpoint and its parent slot
// occupied. REJECTED is deliberately excluded so a refused dispatch can be
// retried, but UNKNOWN stays occupied until a valid reconcile advances it.
const liveStates = `('INFLIGHT','DELIVERED','UNKNOWN')`

var migrations = []string{
	`CREATE TABLE task_outbox (
		id TEXT PRIMARY KEY,
		intent TEXT NOT NULL UNIQUE,
		payload_hash TEXT NOT NULL,
		state TEXT NOT NULL,
		task_id TEXT NOT NULL,
		direction TEXT NOT NULL,
		session_id TEXT NOT NULL,
		project_id TEXT NOT NULL,
		checkpoint INTEGER NOT NULL,
		parent_checkpoint INTEGER NOT NULL,
		parent_hash TEXT NOT NULL DEFAULT '',
		task_json TEXT NOT NULL,
		receipt_json TEXT NOT NULL DEFAULT '',
		findings_json TEXT NOT NULL DEFAULT '[]',
		checkpoint_hash TEXT NOT NULL DEFAULT '',
		checkpoint_advanced INTEGER NOT NULL DEFAULT 0,
		note TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE UNIQUE INDEX task_outbox_delivered_checkpoint ON task_outbox(task_id, session_id, checkpoint) WHERE state='DELIVERED'`,
	`CREATE INDEX task_outbox_task ON task_outbox(task_id, session_id, state)`,
	`CREATE TABLE task_previews (
		digest TEXT PRIMARY KEY,
		findings_json TEXT NOT NULL DEFAULT '[]',
		created_at TEXT NOT NULL
	)`,
	`ALTER TABLE task_outbox ADD COLUMN chain_task_id TEXT NOT NULL DEFAULT ''`,
	`CREATE TABLE task_chain_slots (
		chain_task_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		slot INTEGER NOT NULL,
		task_row_id TEXT NOT NULL,
		PRIMARY KEY (chain_task_id, session_id, kind, slot)
	)`,
	`CREATE TABLE attachment_blobs(ref TEXT PRIMARY KEY,sha256 TEXT NOT NULL UNIQUE,size INTEGER NOT NULL,mime TEXT NOT NULL,data BLOB NOT NULL,created_at TEXT NOT NULL)`,
	`CREATE TABLE task_attachment_refs(task_row_id TEXT NOT NULL REFERENCES task_outbox(id) ON DELETE CASCADE,blob_ref TEXT NOT NULL REFERENCES attachment_blobs(ref),PRIMARY KEY(task_row_id,blob_ref))`,
}

type Service struct {
	db       *sql.DB
	adapter  desktop.Adapter
	safe     func() bool
	mu       sync.Mutex
	dispatch func(func() error) error
	web      desktop.WebTransport
}

func New(db *sql.DB, adapter desktop.Adapter, safe func() bool) (*Service, error) {
	if db == nil {
		return nil, errors.New("bridge requires a database")
	}
	if adapter == nil {
		return nil, errors.New("bridge requires a desktop adapter")
	}
	if safe == nil {
		safe = func() bool { return false }
	}
	if err := storage.Migrate(db, "bridge", migrations); err != nil {
		return nil, fmt.Errorf("bridge migrations: %w", err)
	}
	service := &Service{db: db, adapter: adapter, safe: safe, web: desktop.NewWebMock()}
	service.dispatch = func(fn func() error) error {
		if service.safe() {
			return desktop.ErrSafeMode
		}
		return fn()
	}
	if err := service.backfillChainState(); err != nil {
		return nil, err
	}
	if err := service.recoverInflight(); err != nil {
		return nil, err
	}
	return service, nil
}

func (s *Service) Register(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("GET "+APIPrefix+"/tasks", s.handleList)
	mux.HandleFunc("POST "+APIPrefix+"/attachments", s.handleUpload)
	mux.HandleFunc("POST "+APIPrefix+"/attachments/collect", s.handleCollectAttachments)
	mux.HandleFunc("GET "+APIPrefix+"/tasks/{id}/attachments/{blob}", s.handleAttachment)
	mux.HandleFunc("GET "+APIPrefix+"/web-targets", func(w http.ResponseWriter, r *http.Request) {
		items, err := s.web.Targets(r.Context())
		if err != nil {
			fail(w, err)
			return
		}
		httpx.JSON(w, 200, map[string]any{"items": items, "total": len(items), "mock": true})
	})
	mux.HandleFunc("POST "+APIPrefix+"/tasks/preview", s.handlePreview)
	mux.HandleFunc("POST "+APIPrefix+"/tasks", s.handleSubmit)
	mux.HandleFunc("POST "+APIPrefix+"/tasks/{id}/reconcile", s.handleReconcile)
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (s *Service) recoverInflight() error {
	if _, err := s.db.Exec(`UPDATE task_outbox SET state=?,note=?,updated_at=? WHERE state=?`,
		StateUnknown, "应用重启时任务仍在处理中；结果未知，只能手动核对，绝不自动重投。", now(), StateInflight); err != nil {
		return fmt.Errorf("recover outbox: %w", err)
	}
	return nil
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	offset, limit := httpx.Page(r)
	records, total, err := s.List(r.Context(), offset, limit)
	if err != nil {
		httpx.Fail(w, http.StatusInternalServerError, "TASKS_UNAVAILABLE", "无法读取任务记录")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": records, "total": total})
}

func (s *Service) List(ctx context.Context, offset, limit int) ([]Record, int, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM task_outbox`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,intent,state,task_json,receipt_json,findings_json,payload_hash,checkpoint_hash,checkpoint_advanced,note,created_at,updated_at
		FROM task_outbox ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	records := make([]Record, 0, limit)
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, 0, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return records, total, nil
}

type rowScanner interface{ Scan(dest ...any) error }

func scanRecord(row rowScanner) (Record, error) {
	var record Record
	var taskJSON, receiptJSON, findingsJSON string
	var advanced int
	if err := row.Scan(&record.ID, &record.Intent, &record.State, &taskJSON, &receiptJSON, &findingsJSON,
		&record.ConfirmationHash, &record.CheckpointHash, &advanced, &record.Note, &record.CreatedAt, &record.UpdatedAt); err != nil {
		return Record{}, err
	}
	record.CheckpointAdvanced = advanced == 1
	if err := json.Unmarshal([]byte(taskJSON), &record.Task); err != nil {
		return Record{}, fmt.Errorf("decode task %s: %w", record.ID, err)
	}
	record.Findings = []string{}
	if receiptJSON != "" {
		var receipt ReceiptRecord
		if err := json.Unmarshal([]byte(receiptJSON), &receipt); err != nil {
			return Record{}, fmt.Errorf("decode receipt %s: %w", record.ID, err)
		}
		record.Receipt = &receipt
	}
	if findingsJSON != "" {
		if err := json.Unmarshal([]byte(findingsJSON), &record.Findings); err != nil {
			return Record{}, fmt.Errorf("decode findings %s: %w", record.ID, err)
		}
	}
	return record, nil
}

func (s *Service) loadRecord(ctx context.Context, id string) (Record, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,intent,state,task_json,receipt_json,findings_json,payload_hash,checkpoint_hash,checkpoint_advanced,note,created_at,updated_at FROM task_outbox WHERE id=?`, id)
	return scanRecord(row)
}

func (s *Service) handlePreview(w http.ResponseWriter, r *http.Request) {
	var req PreviewRequest
	if !httpx.Decode(w, r, &req) {
		return
	}
	result, err := s.Preview(r.Context(), req.Task)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}

func (s *Service) Preview(ctx context.Context, task TaskPackage) (PreviewResult, error) {
	task, err := normalizeTask(task)
	if err != nil {
		return PreviewResult{}, err
	}
	verified, findings, err := s.resolveAttachments(ctx, task.Attachments)
	if err != nil {
		return PreviewResult{}, err
	}
	task.Attachments = verified
	redactedTask, textFindings := redactTask(task)
	findings = redactJoin(findings, textFindings)
	_, digest, err := canonicalTask(redactedTask)
	if err != nil {
		return PreviewResult{}, apiError(500, "TASK_ENCODE_FAILED", "无法规范化任务包")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO task_previews(digest,findings_json,created_at) VALUES(?,?,?) ON CONFLICT(digest) DO NOTHING`,
		digest, mustJSON(findings), now()); err != nil {
		return PreviewResult{}, apiError(500, "PREVIEW_UNAVAILABLE", "无法保存预览摘要")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM task_previews WHERE digest NOT IN (SELECT digest FROM task_previews ORDER BY created_at DESC LIMIT 1000)`); err != nil {
		return PreviewResult{}, apiError(500, "PREVIEW_UNAVAILABLE", "无法整理预览摘要")
	}
	result := PreviewResult{
		Task:                 redactedTask,
		Redacted:             redactedTask,
		Findings:             findings,
		RequiresConfirmation: true,
		ConfirmationHash:     digest,
		PreviewID:            "preview-" + digest[:16],
	}
	if missing := unverifiedAttachmentNames(task.Attachments); len(missing) > 0 {
		result.Warning = fmt.Sprintf("附件 %s 没有提供可校验字节，状态为 UNVERIFIED；默认阻止提交，除非明确删除该附件，或显式确认带缺口继续。", strings.Join(missing, "、"))
	}
	if len(task.Attachments) > 0 {
		result.Warning += " 附件保存在本地；文本扫描最多 256 KiB，不保证附件安全。确认将授权把完整原字节交给所选 transport。"
	}
	if len(findings) > 0 {
		result.Warning = strings.TrimSpace(result.Warning + " 检测到可能的敏感内容，已遮蔽；正则不能保证全量识别，请逐项核对预览后再确认。")
	}
	return result, nil
}

func (s *Service) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var req SubmitRequest
	if !httpx.Decode(w, r, &req) {
		return
	}
	record, err := s.Submit(r.Context(), req)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, record)
}

func (s *Service) Submit(ctx context.Context, req SubmitRequest) (Record, error) {
	if !req.Confirmed {
		return Record{}, apiError(422, "CONFIRMATION_REQUIRED", "提交前必须显式确认脱敏预览")
	}
	if s.safe() {
		return Record{}, apiError(409, "SAFE_MODE", "安全模式已阻止任务派发；本地历史仍可查看。")
	}
	intent := strings.TrimSpace(req.Intent)
	if intent == "" || len(intent) > 200 {
		return Record{}, apiError(422, "INTENT_INVALID", "intent 必须存在且不超过 200 字符")
	}
	behavior, err := parseBehavior(req.MockBehavior)
	if err != nil {
		return Record{}, err
	}
	if blocked, reason := s.adapterBlock(); blocked {
		return Record{}, apiError(409, "ADAPTER_DEGRADED", reason)
	}
	task, err := normalizeTask(req.Task)
	if err != nil {
		return Record{}, err
	}
	verified, attachmentFindings, err := s.resolveAttachments(ctx, task.Attachments)
	if err != nil {
		return Record{}, err
	}
	task.Attachments = verified
	if missing := unverifiedAttachmentNames(task.Attachments); len(missing) > 0 && !req.AcknowledgeUnverified {
		return Record{}, apiError(422, "ATTACHMENT_UNVERIFIED", fmt.Sprintf("附件 %s 缺少可校验字节；请删除该附件或显式确认带缺口继续", strings.Join(missing, "、")))
	}
	redactedTask, textFindings := redactTask(task)
	findings := redactJoin(attachmentFindings, textFindings)
	payload, digest, err := canonicalTask(redactedTask)
	if err != nil {
		return Record{}, apiError(500, "TASK_ENCODE_FAILED", "无法规范化任务包")
	}

	if existing, found, err := s.lookupIntent(ctx, intent); err != nil {
		return Record{}, apiError(500, "OUTBOX_UNAVAILABLE", "无法读取 Outbox")
	} else if found {
		if existing.payloadHash != digest {
			return Record{}, apiError(422, "IDEMPOTENCY_CONFLICT", "相同 intent 已用于不同的任务内容；请使用新的交接意图")
		}
		record, err := s.loadRecord(ctx, existing.id)
		if err != nil {
			return Record{}, apiError(500, "OUTBOX_UNAVAILABLE", "无法读取 Outbox 记录")
		}
		record.Replayed = true
		switch record.State {
		case StateInflight:
			record.Note = "相同交接意图正在处理中，本次未再次派发。"
		case StateUnknown:
			record.Note = "该交接意图此前结果未知；不会自动重投，请手动核对。"
		}
		return record, nil
	}

	if len(task.Attachments) > 0 && req.ConfirmationHash == "" {
		return Record{}, apiError(422, "ATTACHMENT_CONFIRMATION_REQUIRED", "带附件的交接必须提交预览摘要")
	}
	if req.ConfirmationHash != "" {
		if strings.ToLower(strings.TrimSpace(req.ConfirmationHash)) != digest {
			return Record{}, apiError(409, "CONFIRMATION_STALE", "任务内容在预览确认后发生变化；请重新预览并确认")
		}
	} else if len(findings) > 0 || len(unverifiedAttachmentNames(task.Attachments)) > 0 {
		previewed, err := s.previewed(ctx, digest)
		if err != nil {
			return Record{}, apiError(500, "OUTBOX_UNAVAILABLE", "无法核对预览摘要")
		}
		if !previewed {
			return Record{}, apiError(422, "PREVIEW_REQUIRED", "包含敏感内容或未校验附件时必须先预览，并提交与预览一致的 confirmationHash")
		}
	}

	session, err := s.session(ctx, task.SessionID)
	if err != nil {
		return Record{}, err
	}
	if err := checkOwnership(session.Owner); err != nil {
		return Record{}, err
	}
	if err := s.validateTarget(ctx, task, session); err != nil {
		return Record{}, err
	}

	id, created, conflict, err := s.claim(ctx, intent, digest, redactedTask, findings)
	if err != nil {
		return Record{}, err
	}
	if conflict != nil {
		if record, ok := s.replayIfSameIntent(ctx, intent, digest); ok {
			return record, nil
		}
		return Record{}, conflict
	}
	if !created {
		record, err := s.loadRecord(ctx, id)
		if err != nil {
			return Record{}, apiError(500, "OUTBOX_UNAVAILABLE", "无法读取 Outbox 记录")
		}
		record.Replayed = true
		return record, nil
	}

	submission := desktop.Submission{
		Intent:         intent,
		TaskID:         task.TaskID,
		SessionID:      task.SessionID,
		Digest:         digest,
		Direction:      task.Direction,
		Checkpoint:     task.Checkpoint,
		Payload:        payload,
		Behavior:       behavior,
		HostID:         session.HostID,
		ProjectID:      task.ProjectID,
		WebTarget:      task.WebTarget,
		Attachments:    transportAttachments(task.Attachments),
		ReadAttachment: s.attachmentReader(id, task.Attachments),
	}
	var receipt desktop.Receipt
	submitErr := s.dispatch(func() error {
		if s.safe() {
			return desktop.ErrSafeMode
		}
		var err error
		if task.Direction == "CODE_TO_CHAT" {
			receipt, err = safeWebSubmit(ctx, s.web, submission)
		} else {
			receipt, err = safeSubmit(ctx, s.adapter, submission)
		}
		return err
	})
	if submitErr != nil {
		return s.recordFailure(ctx, id, submitErr)
	}
	receipt = normalizeReceipt(receipt)
	if err := validateReceipt(receipt, submission); err != nil {
		return s.recordFailure(ctx, id, err)
	}
	return s.recordDelivered(ctx, id, receipt)
}

func (s *Service) replayIfSameIntent(ctx context.Context, intent, digest string) (Record, bool) {
	existing, found, err := s.lookupIntent(ctx, intent)
	if err != nil || !found || existing.payloadHash != digest {
		return Record{}, false
	}
	record, err := s.loadRecord(ctx, existing.id)
	if err != nil {
		return Record{}, false
	}
	record.Replayed = true
	if record.State == StateInflight {
		record.Note = "相同交接意图正在处理中，本次未再次派发。"
	}
	return record, true
}

func safeStatus(adapter desktop.Adapter) (status desktop.Status, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("adapter status panic isolated: %v", recovered)
		}
	}()
	return adapter.Status(context.Background()), nil
}

func (s *Service) adapterBlock() (bool, string) {
	status, err := safeStatus(s.adapter)
	if err != nil {
		return true, "模拟适配器状态读取失败；任务不会派发，本地历史仍可查看。"
	}
	switch status.Mode {
	case desktop.ModeUnavailable:
		return true, "模拟适配器当前不可用；任务不会派发，本地历史仍可查看。"
	case desktop.ModeChanged:
		return true, "模拟站点结构已变化，提交能力被禁用；请等待适配器恢复后重试。"
	}
	if !status.Capabilities.Submit {
		return true, "模拟适配器目前不具备提交能力。"
	}
	return false, ""
}

func parseBehavior(raw string) (desktop.Behavior, error) {
	behavior := desktop.Behavior(strings.ToUpper(strings.TrimSpace(raw)))
	if behavior == "" {
		return desktop.BehaviorOK, nil
	}
	if !behavior.Valid() {
		return "", apiError(422, "MOCK_BEHAVIOR_INVALID", "mockBehavior 只能是 OK、TIMEOUT、EMPTY、OCCUPIED 或 PANIC")
	}
	return behavior, nil
}

type existingIntent struct {
	id          string
	payloadHash string
	state       string
}

func (s *Service) lookupIntent(ctx context.Context, intent string) (existingIntent, bool, error) {
	var row existingIntent
	err := s.db.QueryRowContext(ctx, `SELECT id,payload_hash,state FROM task_outbox WHERE intent=?`, intent).
		Scan(&row.id, &row.payloadHash, &row.state)
	if errors.Is(err, sql.ErrNoRows) {
		return row, false, nil
	}
	if err != nil {
		return row, false, err
	}
	return row, true, nil
}

func (s *Service) previewed(ctx context.Context, digest string) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM task_previews WHERE digest=?`, digest).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// recordID derives a stable id from both intent and payload. Deriving from the
// payload alone would make an honest retry after a REJECTED dispatch collide on
// the primary key instead of creating a new record.
func recordID(intent, digest string) string {
	sum := sha256.Sum256([]byte("bridge-record-v1\x00" + intent + "\x00" + digest))
	return "task-" + hex.EncodeToString(sum[:])[:16]
}

func chainIdentity(task TaskPackage) string {
	if task.LinkedTaskID != "" {
		return task.LinkedTaskID
	}
	return task.TaskID
}

// backfillChainState fills chain_task_id for rows written by earlier builds and
// rebuilds slot occupancy for rows that are still live. It runs once per start,
// is idempotent, and never rewrites task state.
func (s *Service) backfillChainState() error {
	type legacy struct {
		id       string
		taskID   string
		state    string
		taskJSON string
		updated  string
	}
	rows, err := s.db.Query(`SELECT id,task_id,state,task_json,updated_at FROM task_outbox WHERE chain_task_id=''`)
	if err != nil {
		return fmt.Errorf("scan legacy outbox: %w", err)
	}
	var pending []legacy
	for rows.Next() {
		var row legacy
		if err := rows.Scan(&row.id, &row.taskID, &row.state, &row.taskJSON, &row.updated); err != nil {
			rows.Close()
			return fmt.Errorf("scan legacy outbox: %w", err)
		}
		pending = append(pending, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate legacy outbox: %w", err)
	}
	rows.Close()

	return storage.Transaction(context.Background(), s.db, func(tx *sql.Tx) error {
		for _, row := range pending {
			chain := row.taskID
			var task TaskPackage
			if err := json.Unmarshal([]byte(row.taskJSON), &task); err == nil && strings.TrimSpace(task.LinkedTaskID) != "" {
				chain = strings.TrimSpace(task.LinkedTaskID)
			}
			if _, err := tx.Exec(`UPDATE task_outbox SET chain_task_id=? WHERE id=?`, chain, row.id); err != nil {
				return err
			}
		}
		live, err := tx.Query(`SELECT id,chain_task_id,session_id,checkpoint,parent_checkpoint FROM task_outbox
			WHERE state IN ` + liveStates + ` ORDER BY updated_at DESC, id DESC`)
		if err != nil {
			return err
		}
		type slot struct {
			chain      string
			session    string
			checkpoint int
			parent     int
			id         string
		}
		var slots []slot
		for live.Next() {
			var item slot
			if err := live.Scan(&item.id, &item.chain, &item.session, &item.checkpoint, &item.parent); err != nil {
				live.Close()
				return err
			}
			slots = append(slots, item)
		}
		if err := live.Err(); err != nil {
			live.Close()
			return err
		}
		live.Close()
		for _, item := range slots {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO task_chain_slots(chain_task_id,session_id,kind,slot,task_row_id) VALUES(?,?,?,?,?)`,
				item.chain, item.session, "CHECKPOINT", item.checkpoint, item.id); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO task_chain_slots(chain_task_id,session_id,kind,slot,task_row_id) VALUES(?,?,?,?,?)`,
				item.chain, item.session, "PARENT", item.parent, item.id); err != nil {
				return err
			}
		}
		return nil
	})
}

// checkChainLocked validates the increment against the newest delivered
// checkpoint. It runs before the claim transaction; the slot table is the
// cross-process guarantee for concurrent claims.
func (s *Service) checkChainLocked(ctx context.Context, task TaskPackage, chainID string) *Error {
	if task.ParentCheckpoint == 0 {
		var existing int
		err := s.db.QueryRowContext(ctx, `SELECT slot FROM task_chain_slots
			WHERE chain_task_id=? AND session_id=? AND kind='CHECKPOINT' ORDER BY slot DESC LIMIT 1`, chainID, task.SessionID).Scan(&existing)
		if err == nil {
			if existing == task.Checkpoint {
				return apiError(409, "CHECKPOINT_IN_USE", "该任务会话的检查点已交付、处理中或结果未知；不得复用同一检查点")
			}
			return apiError(409, "CHECKPOINT_BRANCH", "该任务链已有活动或已交付检查点；首任务不能与既有链并存")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return apiError(500, "OUTBOX_UNAVAILABLE", "无法核对任务检查点")
		}
		return nil
	}

	var latestDelivered int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(checkpoint),0) FROM task_outbox
		WHERE chain_task_id=? AND session_id=? AND state=?`, chainID, task.SessionID, StateDelivered).Scan(&latestDelivered); err != nil {
		return apiError(500, "OUTBOX_UNAVAILABLE", "无法核对父检查点")
	}
	if latestDelivered == 0 {
		var known int
		err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM task_outbox WHERE chain_task_id=?`, chainID).Scan(&known)
		if err != nil {
			return apiError(500, "OUTBOX_UNAVAILABLE", "无法核对父检查点")
		}
		if known == 0 {
			return apiError(409, "PARENT_CHECKPOINT_UNKNOWN", "找不到同会话、同任务的父检查点；拒绝把分叉或未交付内容写为后继")
		}
		return apiError(409, "PARENT_NOT_DELIVERED", "父检查点尚未 confirmed-delivered；检查点不能提前推进")
	}
	if task.ParentCheckpoint > latestDelivered {
		return apiError(409, "PARENT_NOT_DELIVERED", "父检查点尚未 confirmed-delivered；检查点不能提前推进")
	}
	if task.ParentCheckpoint < latestDelivered {
		return apiError(409, "CHECKPOINT_BRANCH", "父检查点不是最新已交付检查点；旧父会形成分叉")
	}

	var parentHash string
	err := s.db.QueryRowContext(ctx, `SELECT checkpoint_hash FROM task_outbox
		WHERE chain_task_id=? AND session_id=? AND checkpoint=? AND state=?`,
		chainID, task.SessionID, task.ParentCheckpoint, StateDelivered).Scan(&parentHash)
	if errors.Is(err, sql.ErrNoRows) {
		return apiError(409, "PARENT_NOT_DELIVERED", "父检查点尚未 confirmed-delivered；检查点不能提前推进")
	}
	if err != nil {
		return apiError(500, "OUTBOX_UNAVAILABLE", "无法核对父检查点")
	}
	if parentHash == "" || task.ParentHash != parentHash {
		return apiError(409, "PARENT_HASH_MISMATCH", "parentHash 与已交付的父检查点不符；拒绝分叉污染")
	}
	if task.Checkpoint <= task.ParentCheckpoint {
		return apiError(409, "CHECKPOINT_ORDER", "checkpoint 必须大于父检查点")
	}

	var occupied int
	err = s.db.QueryRowContext(ctx, `SELECT slot FROM task_chain_slots
		WHERE chain_task_id=? AND session_id=? AND kind='PARENT' AND slot=?`, chainID, task.SessionID, task.ParentCheckpoint).Scan(&occupied)
	if err == nil {
		return apiError(409, "CHECKPOINT_BRANCH", "该父检查点已有活动或已交付的后继；拒绝产生第二分支")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return apiError(500, "OUTBOX_UNAVAILABLE", "无法核对检查点后继")
	}
	err = s.db.QueryRowContext(ctx, `SELECT slot FROM task_chain_slots
		WHERE chain_task_id=? AND session_id=? AND kind='CHECKPOINT' AND slot=?`, chainID, task.SessionID, task.Checkpoint).Scan(&occupied)
	if err == nil {
		return apiError(409, "CHECKPOINT_IN_USE", "该任务会话的检查点已交付、处理中或结果未知；不得复用同一检查点")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return apiError(500, "OUTBOX_UNAVAILABLE", "无法核对检查点占用")
	}
	return nil
}

func (s *Service) claim(ctx context.Context, intent, digest string, task TaskPackage, findings []string) (string, bool, *Error, error) {
	timestamp := now()
	id := recordID(intent, digest)
	chainID := chainIdentity(task)

	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, found, err := s.lookupIntent(ctx, intent); err != nil {
		return "", false, nil, apiError(500, "OUTBOX_UNAVAILABLE", "无法读取 Outbox")
	} else if found {
		if existing.payloadHash != digest {
			return "", false, apiError(422, "IDEMPOTENCY_CONFLICT", "相同 intent 已用于不同的任务内容；请使用新的交接意图"), nil
		}
		return existing.id, false, nil, nil
	}

	if conflict := s.checkChainLocked(ctx, task, chainID); conflict != nil {
		return "", false, conflict, nil
	}

	// The transaction owns the single connection, so every diagnostic query must
	// wait until it is explicitly rolled back. A deferred rollback followed by a
	// query on *sql.DB would deadlock with MaxOpenConns(1).
	claimErr := s.insertClaim(ctx, id, intent, digest, task, findings, chainID, timestamp)
	if claimErr == nil {
		return id, true, nil, nil
	}
	return "", false, nil, s.claimErrorLocked(ctx, claimErr, intent, digest, task, chainID)
}

func (s *Service) insertClaim(ctx context.Context, id, intent, digest string, task TaskPackage, findings []string, chainID, timestamp string) error {
	taskJSON, err := json.Marshal(task)
	if err != nil {
		return apiError(500, "TASK_ENCODE_FAILED", "无法保存任务包")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return apiError(500, "OUTBOX_UNAVAILABLE", "无法开始 Outbox 事务")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_outbox
		(id,intent,payload_hash,state,task_id,direction,session_id,project_id,checkpoint,parent_checkpoint,parent_hash,
		 task_json,receipt_json,findings_json,checkpoint_hash,checkpoint_advanced,note,created_at,updated_at,chain_task_id)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,'',?,'',0,?,?,?,?)`,
		id, intent, digest, StateInflight, task.TaskID, task.Direction, task.SessionID, task.ProjectID,
		task.Checkpoint, task.ParentCheckpoint, task.ParentHash, string(taskJSON), mustJSON(findings),
		"已原子抢占并派发；等待回执。", timestamp, timestamp, chainID); err != nil {
		tx.Rollback()
		return err
	}
	for _, att := range task.Attachments {
		if att.Status == "STORED" {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO task_attachment_refs(task_row_id,blob_ref) VALUES(?,?)`, id, att.BlobRef); err != nil {
				tx.Rollback()
				return err
			}
		}
	}
	for _, holder := range []struct {
		kind string
		slot int
	}{
		{"CHECKPOINT", task.Checkpoint},
		{"PARENT", task.ParentCheckpoint},
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_chain_slots(chain_task_id,session_id,kind,slot,task_row_id) VALUES(?,?,?,?,?)`,
			chainID, task.SessionID, holder.kind, holder.slot, id); err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// claimErrorLocked re-reads state after a rolled-back transaction to report the
// precise reason. The slot primary key is what actually prevents the duplicate.
func (s *Service) claimErrorLocked(ctx context.Context, cause error, intent, digest string, task TaskPackage, chainID string) *Error {
	existing, found, lookupErr := s.lookupIntent(ctx, intent)
	if lookupErr == nil && found {
		if existing.payloadHash != digest {
			return apiError(422, "IDEMPOTENCY_CONFLICT", "相同 intent 已用于不同的任务内容；请使用新的交接意图")
		}
		return nil
	}
	if isUniqueViolation(cause) {
		var slot int
		if err := s.db.QueryRowContext(ctx, `SELECT slot FROM task_chain_slots
			WHERE chain_task_id=? AND session_id=? AND kind='CHECKPOINT' AND slot=?`, chainID, task.SessionID, task.Checkpoint).Scan(&slot); err == nil {
			return apiError(409, "CHECKPOINT_IN_USE", "该任务会话的检查点已交付、处理中或结果未知；不得复用同一检查点")
		}
		if err := s.db.QueryRowContext(ctx, `SELECT slot FROM task_chain_slots
			WHERE chain_task_id=? AND session_id=? AND kind='PARENT' AND slot=?`, chainID, task.SessionID, task.ParentCheckpoint).Scan(&slot); err == nil {
			return apiError(409, "CHECKPOINT_BRANCH", "该父检查点已有活动或已交付的后继；拒绝产生第二分支")
		}
		return apiError(409, "CHECKPOINT_BRANCH", "检查点冲突；请刷新任务链后重试")
	}
	return apiError(500, "OUTBOX_UNAVAILABLE", "无法原子抢占 Outbox 记录")
}

func safeSessions(ctx context.Context, adapter desktop.Adapter) (sessions []desktop.Session, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			sessions, err = nil, fmt.Errorf("adapter sessions panic isolated: %v", recovered)
		}
	}()
	return adapter.Sessions(ctx)
}

func (s *Service) session(ctx context.Context, id string) (desktop.Session, error) {
	sessions, err := safeSessions(ctx, s.adapter)
	if err != nil {
		return desktop.Session{}, apiError(503, "ADAPTER_UNAVAILABLE", "无法读取模拟会话列表；未派发")
	}
	for _, session := range sessions {
		if session.ID == id {
			return session, nil
		}
	}
	return desktop.Session{}, apiError(404, "SESSION_NOT_FOUND", "目标会话不存在；桥接只引用已有会话，不创建或接管官方会话")
}

func checkOwnership(owner string) error {
	if owner != desktop.OwnerOfficialHost {
		return apiError(409, "SESSION_OWNER_MISMATCH", "目标会话不属于 mock official host；桥接只引用，不抢占所有权")
	}
	return nil
}

func (s *Service) recordFailure(ctx context.Context, id string, cause error) (Record, error) {
	status, code, message, state := classifyFailure(cause)
	if state == "" {
		state = StateUnknown
	}
	if err := s.finishFailure(ctx, id, state, message); err != nil {
		return Record{}, apiError(500, "OUTBOX_UNAVAILABLE", "无法保存提交结果")
	}
	record, err := s.loadRecord(ctx, id)
	if err != nil {
		return Record{}, apiError(500, "OUTBOX_UNAVAILABLE", "无法读取提交结果")
	}
	if state == StateUnknown {
		return record, nil
	}
	return record, apiError(status, code, message)
}

// finishFailure moves an INFLIGHT row to its terminal state. A REJECTED row
// releases its chain slots so the same checkpoint can be retried; UNKNOWN keeps
// them, which is what blocks an unreconciled retry from slipping through.
func (s *Service) finishFailure(ctx context.Context, id, state, message string) error {
	return storage.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE task_outbox SET state=?,note=?,updated_at=? WHERE id=? AND state=?`,
			state, message, now(), id, StateInflight); err != nil {
			return err
		}
		if state == StateRejected {
			if _, err := tx.ExecContext(ctx, `DELETE FROM task_attachment_refs WHERE task_row_id=?`, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM task_chain_slots WHERE task_row_id=?`, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func classifyFailure(cause error) (int, string, string, string) {
	switch {
	case errors.Is(cause, desktop.ErrSafeMode):
		return http.StatusConflict, "SAFE_MODE", "安全模式在跨越派发边界前生效；未发送", StateRejected
	case errors.Is(cause, desktop.ErrOccupied):
		return http.StatusConflict, "SESSION_OCCUPIED", "目标会话被其他客户端占用；桥接不接管，也不重投。", StateRejected
	case errors.Is(cause, desktop.ErrChanged):
		return http.StatusConflict, "ADAPTER_CHANGED", "模拟站点结构已变化，提交被拒绝。", StateRejected
	case errors.Is(cause, desktop.ErrUnavailable):
		return http.StatusServiceUnavailable, "ADAPTER_UNAVAILABLE", "模拟适配器不可用，未派发。", StateRejected
	case errors.Is(cause, desktop.ErrSessionUnknown):
		return http.StatusNotFound, "SESSION_NOT_FOUND", "模拟适配器不认识该会话。", StateRejected
	case errors.Is(cause, desktop.ErrNotOwner):
		return http.StatusConflict, "SESSION_OWNER_MISMATCH", "会话不属于 mock official host；桥接不抢占所有权。", StateRejected
	case errors.Is(cause, desktop.ErrTimeout):
		return http.StatusGatewayTimeout, "UNKNOWN_RESULT", "提交超时，结果未知；不会自动重投，请手动核对。", StateUnknown
	case errors.Is(cause, desktop.ErrEmptyReceipt):
		return http.StatusBadGateway, "RECEIPT_EMPTY", "适配器返回空回执，不能视为成功；结果未知，请手动核对。", StateUnknown
	case errors.Is(cause, desktop.ErrReceiptMismatch):
		return http.StatusBadGateway, "RECEIPT_MISMATCH", "回执与任务或有效状态不匹配，不能视为成功；结果未知，请手动核对。", StateUnknown
	case errors.Is(cause, desktop.ErrReceiptConflict):
		return http.StatusConflict, "RECEIPT_CONFLICT", "适配器回执与更早意图冲突；结果未知。", StateUnknown
	default:
		return http.StatusBadGateway, "ADAPTER_FAILURE", "适配器返回错误，结果未知；不会自动重投，请手动核对。", StateUnknown
	}
}

func (s *Service) recordDelivered(ctx context.Context, id string, receipt desktop.Receipt) (Record, error) {
	hash := chainHash(receipt.TaskID, receipt.SessionID, receipt.Checkpoint, receipt.Digest)
	acceptedAt := ""
	if !receipt.AcceptedAt.IsZero() {
		acceptedAt = receipt.AcceptedAt.UTC().Format(time.RFC3339Nano)
	}
	transport := "MOCK_CODE"
	recordBefore, err := s.loadRecord(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if recordBefore.Task.Direction == "CODE_TO_CHAT" {
		transport = "MOCK_WEB"
	}
	stored := ReceiptRecord{Attachments: receipt.Attachments, Transport: transport,
		ReceiptID:      receipt.ReceiptID,
		TaskID:         receipt.TaskID,
		SessionID:      receipt.SessionID,
		Digest:         receipt.Digest,
		Checkpoint:     receipt.Checkpoint,
		State:          StateDelivered,
		AcceptedAt:     acceptedAt,
		CheckpointHash: hash,
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE task_outbox SET state=?,receipt_json=?,checkpoint_hash=?,checkpoint_advanced=1,note=?,updated_at=? WHERE id=? AND state IN (?,?)`,
		StateDelivered, mustJSON(stored), hash, "回执已匹配 task/session/digest/receiptId 且状态为 DELIVERED；检查点推进。", now(), id, StateInflight, StateUnknown); err != nil {
		return Record{}, apiError(500, "OUTBOX_UNAVAILABLE", "无法保存有效回执")
	}
	record, err := s.loadRecord(ctx, id)
	if err != nil {
		return Record{}, apiError(500, "OUTBOX_UNAVAILABLE", "无法读取任务记录")
	}
	return record, nil
}

func normalizeReceipt(receipt desktop.Receipt) desktop.Receipt {
	receipt.State = strings.ToUpper(strings.TrimSpace(receipt.State))
	return receipt
}

func validateReceipt(receipt desktop.Receipt, submission desktop.Submission) error {
	if strings.TrimSpace(receipt.ReceiptID) == "" {
		return desktop.ErrEmptyReceipt
	}
	if receipt.TaskID != submission.TaskID || receipt.SessionID != submission.SessionID || receipt.Digest != submission.Digest || receipt.Checkpoint != submission.Checkpoint {
		return desktop.ErrReceiptMismatch
	}
	// A receipt without an explicit DELIVERED state is not evidence of success.
	if normalizeReceipt(receipt).State != StateDelivered {
		return desktop.ErrReceiptMismatch
	}
	if len(receipt.Attachments) != len(submission.Attachments) {
		return desktop.ErrReceiptMismatch
	}
	for i, b := range submission.Attachments {
		b.Status = "UPLOADED"
		if receipt.Attachments[i] != b {
			return desktop.ErrReceiptMismatch
		}
	}
	return nil
}

func safeReconcile(ctx context.Context, adapter desktop.Adapter, request desktop.ReconcileRequest) (receipt desktop.Receipt, found bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			receipt, found, err = desktop.Receipt{}, false, fmt.Errorf("adapter reconcile panic isolated: %v", recovered)
		}
	}()
	return adapter.Reconcile(ctx, request)
}

func safeSubmit(ctx context.Context, adapter desktop.Adapter, submission desktop.Submission) (receipt desktop.Receipt, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("adapter panic isolated: %v", recovered)
		}
	}()
	return adapter.Submit(ctx, submission)
}

func (s *Service) Reconcile(ctx context.Context, id string) (Record, error) {
	record, err := s.loadRecord(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, apiError(404, "TASK_NOT_FOUND", "任务记录不存在")
		}
		return Record{}, apiError(500, "OUTBOX_UNAVAILABLE", "无法读取任务记录")
	}
	if record.State == StateDelivered {
		return record, nil
	}
	if record.State == StateRejected {
		record.Note = "该任务在派发阶段已被拒绝，没有可核对的回执；未重投。"
		return record, nil
	}
	request := desktop.ReconcileRequest{
		Intent:     record.Intent,
		TaskID:     record.Task.TaskID,
		SessionID:  record.Task.SessionID,
		Digest:     record.ConfirmationHash,
		Checkpoint: record.Task.Checkpoint,
	}
	if record.Receipt != nil {
		request.ReceiptID = record.Receipt.ReceiptID
	}
	var receipt desktop.Receipt
	var found bool
	if record.Task.Direction == "CODE_TO_CHAT" {
		receipt, found, err = s.web.Reconcile(ctx, request)
	} else {
		receipt, found, err = safeReconcile(ctx, s.adapter, request)
	}
	if err != nil {
		return record, apiError(503, "RECONCILE_UNAVAILABLE", "当前无法核对回执；任务未被重投")
	}
	if !found {
		note := "核对未找到有效回执；状态保持不变，未重投。"
		if _, err := s.db.ExecContext(ctx, `UPDATE task_outbox SET note=?,updated_at=? WHERE id=?`, note, now(), id); err != nil {
			return Record{}, apiError(500, "OUTBOX_UNAVAILABLE", "无法保存核对结果")
		}
		record.Note = note
		return record, nil
	}
	receipt = normalizeReceipt(receipt)
	submission := desktop.Submission{Intent: record.Intent, TaskID: record.Task.TaskID, SessionID: record.Task.SessionID, Digest: record.ConfirmationHash, Checkpoint: record.Task.Checkpoint, Attachments: transportAttachments(record.Task.Attachments)}
	if err := validateReceipt(receipt, submission); err != nil {
		return record, apiError(502, "RECEIPT_INVALID", "Mock 返回的回执与任务不匹配或缺少 DELIVERED 状态，已忽略；任务未被重投")
	}
	return s.recordDelivered(ctx, id, receipt)
}

func (s *Service) handleReconcile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		httpx.Fail(w, http.StatusBadRequest, "TASK_ID_REQUIRED", "缺少任务 ID")
		return
	}
	var body struct{}
	if !httpx.Decode(w, r, &body) {
		return
	}
	record, err := s.Reconcile(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, record)
}

func fail(w http.ResponseWriter, err error) {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		httpx.Fail(w, apiErr.Status, apiErr.Code, apiErr.Message)
		return
	}
	httpx.Fail(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误")
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT")
}

func mustJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(raw)
}

func redactJoin(groups ...[]string) []string {
	var all []string
	for _, group := range groups {
		all = append(all, group...)
	}
	return redact.JoinFindings(all)
}

func (s *Service) UseDispatchGate(gate func(func() error) error) { s.dispatch = gate }
func safeWebSubmit(ctx context.Context, web desktop.WebTransport, submission desktop.Submission) (receipt desktop.Receipt, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("web transport panic isolated")
		}
	}()
	return web.Submit(ctx, submission)
}
func (s *Service) validateTarget(ctx context.Context, task TaskPackage, session desktop.Session) error {
	status, err := safeStatus(s.adapter)
	if err != nil {
		return desktop.ErrUnavailable
	}
	if session.ProjectID != task.ProjectID {
		return apiError(409, "PROJECT_MISMATCH", "目标会话与项目不匹配")
	}
	if session.HostID == "" || session.HostID != status.HostID || (task.HostID != "" && task.HostID != session.HostID) {
		return apiError(409, "HOST_MISMATCH", "目标会话与宿主不匹配")
	}
	if task.Direction == "CODE_TO_CHAT" {
		if task.WebTarget == nil {
			return apiError(422, "WEB_TARGET_REQUIRED", "请选择已验证的网页 tab 与 thread 目标；网页 URL 本身不是授权目标")
		}
		targets, err := s.web.Targets(ctx)
		if err != nil {
			return desktop.ErrUnavailable
		}
		for _, target := range targets {
			if target == *task.WebTarget {
				return nil
			}
		}
		return apiError(409, "WEB_TARGET_MISMATCH", "site/origin/tab/thread 与已授权目标不匹配")
	}
	return nil
}
