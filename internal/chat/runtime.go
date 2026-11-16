package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"enhancedkimi/internal/config"
	"enhancedkimi/internal/credentials"
	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/provider"
	"enhancedkimi/internal/security"
	"enhancedkimi/internal/storage"
	"enhancedkimi/internal/usage"
)

// RequestState is the thread-visible view of one dispatched provider request.
// Resolution is only set when a user explicitly checked an UNKNOWN request;
// the underlying audit state stays UNKNOWN.
type RequestState struct {
	ID             string `json:"id"`
	State          string `json:"state"`
	Note           string `json:"note"`
	Resolution     string `json:"resolution,omitempty"`
	ResolvedAt     string `json:"resolvedAt,omitempty"`
	ResolutionNote string `json:"resolutionNote,omitempty"`
	AccountID      string `json:"accountId,omitempty"`
}

// User-asserted outcomes for an UNKNOWN request. They record what the user
// verified upstream; they never rewrite the request into a success or a
// failure and they never cause a resend.
const (
	ResolutionNotFound = "NOT_FOUND_UPSTREAM"
	ResolutionSeen     = "SEEN_UPSTREAM"
)

const (
	stateInflight  = "INFLIGHT"
	stateUnknown   = "UNKNOWN"
	stateCompleted = "COMPLETED"
	stateRejected  = "REJECTED"
)

func validResolution(value string) bool {
	return value == ResolutionNotFound || value == ResolutionSeen
}

func resolutionNote(value string) string {
	if value == ResolutionNotFound {
		return "用户已核对：供应商侧未收到该请求。请求本身仍为 UNKNOWN，未自动重发。"
	}
	return "用户已核对：供应商侧可见该请求。请求本身仍为 UNKNOWN；本地未记录回复，未自动重发。"
}

type Runtime struct {
	client   *provider.Client
	usage    *usage.Service
	dispatch func(func() error) error
}

func (s *Service) UseProvider(client *provider.Client, ledger *usage.Service, dispatch func(func() error) error) {
	s.runtime = &Runtime{client, ledger, dispatch}
}
func (s *Service) initRequests() error {
	if err := storage.Migrate(s.db, "chat_runtime", []string{
		`CREATE TABLE chat_requests(thread_id TEXT NOT NULL,intent TEXT NOT NULL,digest TEXT NOT NULL,id TEXT NOT NULL,state TEXT NOT NULL,usage_json TEXT,at TEXT NOT NULL,PRIMARY KEY(thread_id,intent));`,
		`ALTER TABLE chat_requests ADD COLUMN resolution TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE chat_requests ADD COLUMN resolved_at TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE chat_requests ADD COLUMN resolution_note TEXT NOT NULL DEFAULT ''`,
	}); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE chat_requests SET state='UNKNOWN' WHERE state='INFLIGHT'`)
	return err
}

// pendingClause is the single definition of "this thread cannot accept a new
// message". An UNKNOWN request blocks until a user asserts what they verified;
// after that it stays UNKNOWN for audit but no longer blocks. INFLIGHT is never
// resolvable and always blocks.
const pendingClause = `state='INFLIGHT' OR (state='UNKNOWN' AND resolution='')`

func (s *Service) sendProvider(w http.ResponseWriter, r *http.Request, id, intent, digest, text string) {
	if s.runtime == nil {
		httpx.Fail(w, 503, "PROVIDER_UNAVAILABLE", "供应商连接未启用")
		return
	}
	var existing int
	if err := s.db.QueryRow(`SELECT count(*) FROM chat_requests WHERE thread_id=? AND intent=?`, id, intent).Scan(&existing); err != nil {
		httpx.Fail(w, 500, "CHAT_STORAGE", "无法读取请求状态")
		return
	}
	var providerConfig provider.Config
	var effective provider.EffectiveAccount
	if existing == 0 {
		current, err := s.load(id)
		if err != nil {
			httpx.Fail(w, 404, "THREAD_NOT_FOUND", "聊天不存在")
			return
		}
		if err := s.runtime.client.Ready(r.Context(), current.ProviderID); err != nil {
			if errors.Is(err, provider.ErrProviderNotFound) {
				httpx.Fail(w, 404, "PROVIDER_NOT_FOUND", "线程引用的供应商已不存在，消息尚未发送。")
				return
			}
			if !errors.Is(err, credentials.ErrLocked) && !errors.Is(err, credentials.ErrNotFound) {
				httpx.Fail(w, 422, "INVALID_PROVIDER", "供应商配置不可读，消息尚未发送。")
				return
			}
			httpx.Fail(w, 423, "PROVIDER_NOT_READY", "请解锁凭据库并核对供应商密钥引用，消息尚未发送。")
			return
		}
		// Resolve the account before any state is written. A request row is the
		// thread's blocking record, so it may only exist for a send that can
		// actually be dispatched.
		var configErr error
		providerConfig, effective, configErr = s.runtime.client.EffectiveConfig(r.Context(), current.ProviderID)
		if configErr != nil {
			httpx.Fail(w, 422, "INVALID_PROVIDER", "供应商配置不可读，消息尚未发送。")
			return
		}
	}
	var thread Thread
	var requestID, at string
	duplicate := false
	err := storage.Transaction(r.Context(), s.db, func(tx *sql.Tx) error {
		var raw string
		if err := tx.QueryRow(`SELECT data FROM chat_threads WHERE id=?`, id).Scan(&raw); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(raw), &thread); err != nil {
			return err
		}
		var oldDigest, state string
		err := tx.QueryRow(`SELECT digest,state,id,at FROM chat_requests WHERE thread_id=? AND intent=?`, id, intent).Scan(&oldDigest, &state, &requestID, &at)
		if err == nil {
			if oldDigest != digest {
				return errors.New("intent conflict")
			}
			duplicate = true
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		var pending int
		if err = tx.QueryRow(`SELECT count(*) FROM chat_requests WHERE thread_id=? AND (`+pendingClause+`)`, id).Scan(&pending); err != nil {
			return err
		}
		if pending > 0 {
			return errors.New("request unresolved")
		}
		requestID = security.Random()[:24]
		at = time.Now().UTC().Format(time.RFC3339Nano)
		thread.Messages = append(thread.Messages, Message{ID: security.Random()[:16], Role: "user", Text: text, At: at})
		thread.LastRequest = &RequestState{ID: requestID, State: stateInflight, Note: "等待供应商响应"}
		data, err := json.Marshal(thread)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE chat_threads SET data=? WHERE id=?`, string(data), id); err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO chat_requests(thread_id,intent,digest,id,state,usage_json,at) VALUES(?,?,?,?,?,NULL,?)`, id, intent, digest, requestID, stateInflight, at)
		return err
	})
	if err != nil {
		code := "CHAT_BUSY"
		status := 409
		if errors.Is(err, sql.ErrNoRows) {
			status = 404
			code = "THREAD_NOT_FOUND"
		}
		httpx.Fail(w, status, code, "消息意图冲突，或前一请求尚未确认。请查看线程请求状态，系统不会自动重发。")
		return
	}
	if duplicate {
		s.refreshProviderUsage(&thread)
		httpx.JSON(w, 200, thread)
		return
	}
	messages := []provider.Message{}
	start := max(0, len(thread.Messages)-12)
	for _, m := range thread.Messages[start:] {
		messages = append(messages, provider.Message{Role: m.Role, Content: m.Text})
	}
	var reply provider.Reply
	err = s.runtime.dispatch(func() error {
		s.recordProviderUsage(thread, requestID, at, stateInflight, nil)
		var err error
		reply, err = s.runtime.client.ChatWith(r.Context(), providerConfig, thread.Model, messages)
		return err
	})
	state, note := stateCompleted, "供应商已返回文本"
	if err != nil {
		state = stateUnknown
		note = "请求未确认，未自动重发。请核对供应商记录后解除该请求，或新建线程。"
	}
	if errors.Is(err, config.ErrSafeMode) {
		state = stateRejected
		note = "安全模式阻止发送，供应商未收到请求。"
	}
	thread.LastRequest = &RequestState{ID: requestID, State: state, Note: note, AccountID: effective.AccountID}
	if err == nil {
		thread.Messages = append(thread.Messages, Message{ID: security.Random()[:16], Role: "assistant", Text: reply.Text, At: time.Now().UTC().Format(time.RFC3339Nano)})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	saveErr := storage.Transaction(ctx, s.db, func(tx *sql.Tx) error {
		data, e := json.Marshal(thread)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(`UPDATE chat_threads SET data=? WHERE id=?`, string(data), id); e != nil {
			return e
		}
		_, e = tx.Exec(`UPDATE chat_requests SET state=?,usage_json=? WHERE thread_id=? AND intent=?`, state, string(reply.Usage), id, intent)
		return e
	})
	if saveErr != nil {
		httpx.Fail(w, 500, "CHAT_SAVE_UNKNOWN", "供应商请求已发出，但本地结果写入失败。请核对，勿重复发送。")
		return
	}
	s.recordProviderUsage(thread, requestID, at, state, reply.Usage)
	httpx.JSON(w, 200, thread)
}

// resolveRequest records a user's explicit check of an UNKNOWN request. It is
// idempotent for the same outcome and refuses INFLIGHT or terminal requests, so
// it cannot be used to skip a request that is still running.
func (s *Service) resolveRequest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	requestID := r.PathValue("requestId")
	var in struct {
		Resolution string `json:"resolution"`
		Note       string `json:"note"`
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	in.Resolution = strings.TrimSpace(in.Resolution)
	in.Note = strings.TrimSpace(in.Note)
	if !validResolution(in.Resolution) {
		httpx.Fail(w, 422, "INVALID_RESOLUTION", "resolution 只能是 NOT_FOUND_UPSTREAM 或 SEEN_UPSTREAM")
		return
	}
	if len(in.Note) > 500 {
		httpx.Fail(w, 422, "INVALID_RESOLUTION", "核对说明最长 500 字符")
		return
	}
	var state, current, resolvedAt, storedNote string
	err := s.db.QueryRow(`SELECT state,resolution,resolved_at,resolution_note FROM chat_requests WHERE thread_id=? AND id=?`, id, requestID).Scan(&state, &current, &resolvedAt, &storedNote)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.Fail(w, 404, "REQUEST_NOT_FOUND", "该线程没有此请求")
		return
	}
	if err != nil {
		httpx.Fail(w, 500, "CHAT_STORAGE", "无法读取请求状态")
		return
	}
	switch {
	case state == stateInflight:
		httpx.Fail(w, 409, "REQUEST_IN_FLIGHT", "请求仍在进行中，不能解除。请等待其结束。")
		return
	case state != stateUnknown:
		httpx.Fail(w, 409, "REQUEST_TERMINAL", "该请求已有确定结果，无需解除。")
		return
	case current != "" && current != in.Resolution:
		httpx.Fail(w, 409, "RESOLUTION_CONFLICT", "该请求已按另一种结论核对过，不能改写。")
		return
	}
	if current == "" {
		at := time.Now().UTC().Format(time.RFC3339Nano)
		result, err := s.db.Exec(`UPDATE chat_requests SET resolution=?,resolved_at=?,resolution_note=? WHERE thread_id=? AND id=? AND state='UNKNOWN' AND resolution=''`, in.Resolution, at, in.Note, id, requestID)
		if err != nil {
			httpx.Fail(w, 500, "CHAT_STORAGE", "无法保存核对结果")
			return
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			// A concurrent request may have written the same outcome first. That
			// is a retry, not a conflict: report the row that is now stored.
			stored, storedAt, note, readErr := s.storedResolution(id, requestID)
			if readErr != nil {
				httpx.Fail(w, 500, "CHAT_STORAGE", "无法读取核对结果")
				return
			}
			if stored != in.Resolution {
				httpx.Fail(w, 409, "REQUEST_STATE_CHANGED", "请求状态已变化，请刷新后重试")
				return
			}
			current, resolvedAt, storedNote = stored, storedAt, note
		} else {
			storedNote = in.Note
			resolvedAt = at
		}
	}
	thread, err := s.requestView(id)
	if err != nil {
		httpx.Fail(w, 500, "CHAT_STORAGE", "无法读取线程")
		return
	}
	httpx.JSON(w, 200, map[string]any{"thread": thread, "request": s.requestState(requestID, state, in.Resolution, resolvedAt, storedNote)})
}

// storedResolution reads the audit columns for one request, so a concurrent
// resolve that wrote the same outcome can be reported instead of rejected.
func (s *Service) storedResolution(threadID, requestID string) (string, string, string, error) {
	var resolved, resolvedAt, note string
	err := s.db.QueryRow(`SELECT resolution,resolved_at,resolution_note FROM chat_requests WHERE thread_id=? AND id=?`, threadID, requestID).Scan(&resolved, &resolvedAt, &note)
	return resolved, resolvedAt, note, err
}

func (s *Service) requestState(id, state, resolved, resolvedAt, note string) RequestState {
	out := RequestState{ID: id, State: state, Note: requestNote(state)}
	if resolved != "" {
		out.Resolution = resolved
		out.ResolvedAt = resolvedAt
		out.ResolutionNote = note
		out.Note = resolutionNote(resolved)
	}
	return out
}

func requestNote(state string) string {
	switch state {
	case stateInflight:
		return "等待供应商响应"
	case stateUnknown:
		return "请求未确认，未自动重发。请核对供应商记录后解除该请求，或新建线程。"
	case stateRejected:
		return "安全模式阻止发送，供应商未收到请求。"
	default:
		return "供应商已返回文本"
	}
}

// requestView loads a thread with the latest request states applied, so both
// GET and the resolve response show one consistent audit view.
func (s *Service) requestView(id string) (Thread, error) {
	thread, err := s.load(id)
	if err != nil {
		return Thread{}, err
	}
	s.refreshProviderUsage(&thread)
	return thread, nil
}

// listRequests returns the full dispatch history of a thread. Resolved UNKNOWN
// requests remain visible with their user-asserted resolution.
func (s *Service) listRequests(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.load(id); err != nil {
		httpx.Fail(w, 404, "THREAD_NOT_FOUND", "聊天不存在")
		return
	}
	rows, err := s.db.Query(`SELECT id,intent,state,at,resolution,resolved_at,resolution_note,COALESCE(usage_json,'') FROM chat_requests WHERE thread_id=? ORDER BY at,rowid`, id)
	if err != nil {
		httpx.Fail(w, 500, "CHAT_STORAGE", "无法读取请求历史")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var requestID, intent, state, at, resolved, resolvedAt, note, raw string
		if err = rows.Scan(&requestID, &intent, &state, &at, &resolved, &resolvedAt, &note, &raw); err != nil {
			break
		}
		view := s.requestState(requestID, state, resolved, resolvedAt, note)
		item := map[string]any{"id": view.ID, "intent": intent, "state": view.State, "note": view.Note, "at": at}
		if view.Resolution != "" {
			item["resolution"] = view.Resolution
			item["resolvedAt"] = view.ResolvedAt
			item["resolutionNote"] = view.ResolutionNote
		}
		items = append(items, item)
	}
	if err != nil || rows.Err() != nil {
		httpx.Fail(w, 500, "CHAT_STORAGE", "无法读取请求历史")
		return
	}
	httpx.JSON(w, 200, map[string]any{"items": items, "total": len(items)})
}

func (s *Service) recordProviderUsage(t Thread, id, at, state string, raw json.RawMessage) {
	if s.runtime == nil || s.runtime.usage == nil || state == stateRejected {
		return
	}
	p, err := s.runtime.client.Config(context.Background(), t.ProviderID)
	if err != nil {
		return
	}
	sequence := int64(1)
	lifecycle := "PARTIAL"
	if state == stateCompleted {
		sequence = 2
		lifecycle = "FINAL"
	}
	_, _ = s.runtime.usage.Ingest([]usage.EventInput{{ProviderID: t.ProviderID, Model: t.Model, RequestID: id, AttemptID: "1", SessionID: t.ID, ProjectID: t.ProjectID, AgentID: "light-chat", Role: "main", RequestKind: "chat", Protocol: p.Protocol, Source: "gateway", At: at, Raw: raw, ObservationState: lifecycle, Sequence: &sequence}})
}

// refreshProviderUsage replays the audit rows into the ledger and mirrors the
// newest request state onto the thread. A resolved UNKNOWN keeps its UNKNOWN
// state and gains the resolution fields.
func (s *Service) refreshProviderUsage(t *Thread) {
	if t.ProviderID == "" {
		return
	}
	rows, err := s.db.Query(`SELECT id,at,state,COALESCE(usage_json,''),resolution,resolved_at,resolution_note FROM chat_requests WHERE thread_id=? ORDER BY at,rowid`, t.ID)
	if err != nil {
		return
	}
	type saved struct{ id, at, state, raw, resolved, resolvedAt, note string }
	items := []saved{}
	for rows.Next() {
		var v saved
		if rows.Scan(&v.id, &v.at, &v.state, &v.raw, &v.resolved, &v.resolvedAt, &v.note) == nil {
			items = append(items, v)
		}
	}
	rows.Close()
	for _, v := range items {
		s.recordProviderUsage(*t, v.id, v.at, v.state, json.RawMessage(v.raw))
		if t.LastRequest != nil && t.LastRequest.ID == v.id {
			*t.LastRequest = s.requestState(v.id, v.state, v.resolved, v.resolvedAt, v.note)
		}
	}
	if t.LastRequest != nil && len(items) > 0 {
		last := items[len(items)-1]
		if last.id != t.LastRequest.ID && last.state == stateUnknown && last.resolved == "" {
			t.LastRequest = ptrRequestState(s.requestState(last.id, last.state, last.resolved, last.resolvedAt, last.note))
		}
	}
}

func ptrRequestState(v RequestState) *RequestState { return &v }
