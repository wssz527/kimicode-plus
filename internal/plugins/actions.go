package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"enhancedkimi/internal/credentials"
	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/redact"
)

const (
	storageKeyLimit   = 200
	storageValueLimit = 32 << 10
	storageListLimit  = 500
)

// actionEnvelope is the body of every plugin action call. The plugin id comes
// from the path and the token from a header; neither can be supplied by the body.
type actionEnvelope struct {
	Params json.RawMessage `json:"params,omitempty"`
	Token  string          `json:"token,omitempty"`
}

// IdentityHeader carries the renderer identity token. It is a header rather than
// a body field so an action payload written by plugin code cannot impersonate
// another plugin.
const IdentityHeader = "X-Plugin-Token"

func (m *Manager) handleAction(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	action := strings.TrimSpace(r.PathValue("action"))
	if action == "" {
		httpx.Fail(w, http.StatusBadRequest, "PLUGIN_ACTION_REQUIRED", "缺少插件动作名")
		return
	}
	value, err := m.Call(r.Context(), id, action, r.Header.Get(IdentityHeader), r)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"value": value})
}

// Call authorizes and runs one plugin action addressed by plugin id. The caller's
// token must match the token this manager issued to that plugin.
func (m *Manager) Call(ctx context.Context, id, action, token string, r *http.Request) (any, error) {
	id = strings.TrimSpace(id)
	action = strings.TrimSpace(action)
	m.mu.Lock()
	_, ok := m.entries[id]
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return nil, m.closedError()
	}
	if !ok {
		return nil, apiError(http.StatusNotFound, "PLUGIN_NOT_FOUND", "插件未安装")
	}
	if !m.Verify(id, token) {
		return nil, apiError(http.StatusUnauthorized, "PLUGIN_IDENTITY_REJECTED", "插件身份令牌无效")
	}
	return m.runAction(ctx, id, action, nil, r)
}

// runAction is the single authorization gate. Both the id-addressed action route
// and the token-addressed runtime route end here, so the capability table, the
// credential injection and the activity rules cannot diverge between them.
func (m *Manager) runAction(ctx context.Context, id, action string, params json.RawMessage, r *http.Request) (any, error) {
	// Everything the action needs is copied while the lock is held. Reading the
	// entry afterwards would race with Enable/Disable/Uninstall, which rewrite the
	// same fields under a different lock acquisition.
	m.mu.Lock()
	item, ok := m.entries[id]
	closed := m.closed
	core := m.core
	vault := m.vault
	var state string
	var grants Grants
	var info map[string]any
	if ok {
		state = item.state
		grants = item.grants
		info = m.runtimeInfoLocked(item)
	}
	m.mu.Unlock()
	if closed {
		return nil, m.closedError()
	}
	if !ok {
		return nil, apiError(http.StatusNotFound, "PLUGIN_NOT_FOUND", "插件未安装")
	}
	if state != StateEnabled {
		return nil, apiError(http.StatusConflict, "PLUGIN_DISABLED", "插件未启用")
	}
	if r != nil && len(params) > 0 {
		r = r.WithContext(context.WithValue(r.Context(), paramsKey{}, params))
	}

	switch {
	case action == "runtime.info":
		return info, nil
	case action == "log":
		var input struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		}
		if err := decodeInto(ctx, r, params, &input); err != nil {
			return nil, err
		}
		level := strings.ToLower(strings.TrimSpace(input.Level))
		if level != "info" && level != "warn" && level != "error" {
			level = "info"
		}
		message := truncate(redact.Text(input.Message), 300)
		_ = recordActivity(ctx, m.db, id, ActivityError, level+": "+message, m.now())
		return map[string]any{"logged": true}, nil
	case action == "report.error":
		return m.reportError(ctx, id, r, params)
	case strings.HasPrefix(action, "net."):
		return m.proxy(ctx, id, grants, vault, action, r, params)
	case strings.HasPrefix(action, "credentials."):
		// Credentials are handled here rather than forwarded to the core: the
		// plaintext lives in this process's vault, and a core adapter round trip
		// would only widen the surface that can see it.
		if err := grants.Authorize(action); err != nil {
			return nil, err
		}
		return m.credentialsAction(ctx, id, grants, action, r, params)
	case action == "events.poll":
		return m.pollEvents(ctx, grants, r, params)
	case strings.HasPrefix(action, "storage."):
		// Storage is a capability like any other: dispatching on the action prefix
		// before the grant check would let a plugin use storage it never confirmed.
		if err := grants.Authorize(action); err != nil {
			return nil, err
		}
		return m.storage(ctx, id, action, r, params)
	}

	if err := grants.Authorize(action); err != nil {
		return nil, err
	}
	if core == nil {
		return nil, apiError(http.StatusServiceUnavailable, "PLUGIN_CORE_UNAVAILABLE", "核心服务尚未就绪，无法执行该动作")
	}
	payload, err := resolveParams(r, params)
	if err != nil {
		return nil, err
	}
	value, err := core.Action(ctx, action, payload)
	if err != nil {
		return nil, coreFailure(action, err)
	}
	switch action {
	case "sessions.send", "sessions.create":
		_ = recordActivity(ctx, m.db, id, ActivitySession, "插件经核心发送会话动作："+action, m.now())
	case "config.save":
		_ = recordActivity(ctx, m.db, id, ActivityConfig, "插件修改了 Kimi Code 配置", m.now())
	}
	return value, nil
}

// runtimeInfoLocked builds the identity answer from an entry the caller already
// holds the lock for, so a snapshot taken under the lock is consistent.
func (m *Manager) runtimeInfoLocked(item *entry) map[string]any {
	return map[string]any{
		"id":           item.manifest.ID,
		"name":         item.manifest.Name,
		"version":      item.manifest.Version,
		"apiVersion":   APIVersion,
		"hostVersion":  m.host,
		"capabilities": item.grants.Names(),
		"slots":        grantedSlots(item.grants),
	}
}

// reportError records a runtime failure the plugin could not handle itself.
//
// A fatal report is the renderer telling the core that this plugin's activation
// threw, which is a different event from a plugin that misbehaves occasionally:
// the plugin must be stopped before its next poll, the reason must survive for
// the plugin page, and its identity token must die with it. A non-fatal report is
// just an activity row; the renderer applies its own rate window before it
// escalates one of those to fatal, so the core never accumulates a threshold of
// its own on top of the renderer's.
//
// The failure goes through fail() rather than Disable(): Disable clears errText,
// which would leave a faulted plugin listed as "disabled" with no explanation.
func (m *Manager) reportError(ctx context.Context, id string, r *http.Request, params json.RawMessage) (any, error) {
	var input struct {
		Message string `json:"message"`
		Code    string `json:"code"`
		Fatal   bool   `json:"fatal"`
	}
	if err := decodeInto(ctx, r, params, &input); err != nil {
		return nil, err
	}
	message := truncate(redact.Text(strings.TrimSpace(input.Message)), 300)
	code := truncate(redact.Text(strings.TrimSpace(input.Code)), 60)
	if message == "" {
		message = "插件报告了未说明的异常"
	}
	detail := message
	if code != "" {
		detail = code + ": " + message
	}
	if !input.Fatal {
		_ = recordActivity(ctx, m.db, id, ActivityError, detail, m.now())
		return map[string]any{"recorded": true}, nil
	}
	// A plugin that is already stopped keeps its recorded reason: a late report
	// from a dying renderer must not overwrite the first, more specific one.
	m.mu.Lock()
	item := m.entries[id]
	alreadyStopped := item == nil || (item.state != StateEnabled && item.errText != "")
	m.mu.Unlock()
	if alreadyStopped {
		_ = recordActivity(ctx, m.db, id, ActivityError, detail, m.now())
		return map[string]any{"recorded": true, "stopped": false}, nil
	}
	now := m.now()
	m.fail(ctx, id, StateFaulted, detail, ActivityError, now)
	return map[string]any{"recorded": true, "stopped": true, "state": StateFaulted}, nil
}

// coreFailure keeps a coded plugin error as-is and hides everything else. The
// core adapter in package server is responsible for translating native service
// errors into *Error before they get here, so a raw Go error never reaches the
// renderer with its internal text.
func coreFailure(action string, err error) error {
	var pluginErr *Error
	if errors.As(err, &pluginErr) {
		return pluginErr
	}
	return apiError(http.StatusUnprocessableEntity, "PLUGIN_ACTION_FAILED", action+" 执行失败")
}

// resolveParams returns the action payload. The id-addressed route carries the
// params in the request body; the token-addressed route already has them. Both
// end at the same value, so a plugin cannot get a different payload shape
// depending on which route it used.
func resolveParams(r *http.Request, params json.RawMessage) (json.RawMessage, error) {
	if len(params) > 0 {
		if params[0] != '{' && params[0] != '[' && string(params) != "null" {
			return nil, apiError(http.StatusBadRequest, "PLUGIN_PARAMS_INVALID", "动作参数必须是 JSON 对象")
		}
		return params, nil
	}
	if r == nil {
		return json.RawMessage("{}"), nil
	}
	if stored, ok := r.Context().Value(paramsKey{}).(json.RawMessage); ok {
		return stored, nil
	}
	body := http.MaxBytesReader(nil, r.Body, httpx.BodyLimit(r))
	decoder := json.NewDecoder(body)
	envelope := actionEnvelope{}
	if err := decoder.Decode(&envelope); err != nil {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_PARAMS_INVALID", "请求格式无效或超过大小限制")
	}
	if len(envelope.Params) == 0 {
		return json.RawMessage("{}"), nil
	}
	if envelope.Params[0] != '{' && envelope.Params[0] != '[' && string(envelope.Params) != "null" {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_PARAMS_INVALID", "动作参数必须是 JSON 对象")
	}
	return envelope.Params, nil
}

func decodeInto(ctx context.Context, r *http.Request, params json.RawMessage, out any) error {
	payload, err := resolveParams(r, params)
	if err != nil {
		return err
	}
	if len(payload) == 0 || string(payload) == "null" {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return apiError(http.StatusBadRequest, "PLUGIN_PARAMS_INVALID", "动作参数格式无效")
	}
	return nil
}

type paramsKey struct{}

func (m *Manager) storage(ctx context.Context, id, action string, r *http.Request, params json.RawMessage) (any, error) {
	var input struct {
		Key    string          `json:"key"`
		Prefix string          `json:"prefix"`
		Value  json.RawMessage `json:"value"`
	}
	switch action {
	case "storage.get":
		if err := decodeInto(ctx, r, params, &input); err != nil {
			return nil, err
		}
		if err := validateStorageKey(input.Key); err != nil {
			return nil, err
		}
		value, ok, err := storageGet(ctx, m.db, id, input.Key)
		if err != nil {
			return nil, apiError(http.StatusInternalServerError, "PLUGIN_STORAGE_FAILED", "无法读取插件存储")
		}
		if !ok {
			return map[string]any{"value": nil, "found": false}, nil
		}
		return map[string]any{"value": string(value), "found": true}, nil
	case "storage.set":
		if err := decodeInto(ctx, r, params, &input); err != nil {
			return nil, err
		}
		if err := validateStorageKey(input.Key); err != nil {
			return nil, err
		}
		if len(input.Value) == 0 {
			input.Value = json.RawMessage("null")
		}
		if len(input.Value) > storageValueLimit {
			return nil, apiError(http.StatusRequestEntityTooLarge, "PLUGIN_STORAGE_LIMIT", fmt.Sprintf("单个值不超过 %d 字节", storageValueLimit))
		}
		if err := storageSet(ctx, m.db, id, input.Key, input.Value, m.now()); err != nil {
			return nil, apiError(http.StatusInternalServerError, "PLUGIN_STORAGE_FAILED", "无法写入插件存储")
		}
		return map[string]any{"saved": true}, nil
	case "storage.delete":
		if err := decodeInto(ctx, r, params, &input); err != nil {
			return nil, err
		}
		if err := validateStorageKey(input.Key); err != nil {
			return nil, err
		}
		if err := storageDelete(ctx, m.db, id, input.Key); err != nil {
			return nil, apiError(http.StatusInternalServerError, "PLUGIN_STORAGE_FAILED", "无法删除插件存储项")
		}
		return map[string]any{"deleted": true}, nil
	case "storage.list":
		if err := decodeInto(ctx, r, params, &input); err != nil {
			return nil, err
		}
		keys, err := storageList(ctx, m.db, id, input.Prefix, storageListLimit)
		if err != nil {
			return nil, apiError(http.StatusInternalServerError, "PLUGIN_STORAGE_FAILED", "无法列出插件存储")
		}
		return map[string]any{"keys": keys}, nil
	}
	return nil, apiError(http.StatusBadRequest, "PLUGIN_ACTION_UNKNOWN", "未知的存储动作")
}

func validateStorageKey(key string) error {
	if key == "" || len(key) > storageKeyLimit {
		return apiError(http.StatusBadRequest, "PLUGIN_STORAGE_KEY", fmt.Sprintf("存储键必须为 1–%d 个字符", storageKeyLimit))
	}
	if strings.ContainsAny(key, "\x00\n\r") {
		return apiError(http.StatusBadRequest, "PLUGIN_STORAGE_KEY", "存储键不能包含控制字符")
	}
	return nil
}

// pollEvents forwards only the scopes the grant covers. A plugin cannot widen
// its subscription by asking for a scope it was not granted: the denied scopes
// are dropped and the granted ones still work.
func (m *Manager) pollEvents(ctx context.Context, grants Grants, r *http.Request, params json.RawMessage) (any, error) {
	var input struct {
		After  int64    `json:"after"`
		Scopes []string `json:"scopes"`
		WaitMS int      `json:"waitMs"`
	}
	if err := decodeInto(ctx, r, params, &input); err != nil {
		return nil, err
	}
	if input.WaitMS < 0 {
		input.WaitMS = 0
	}
	if input.WaitMS > 10000 {
		input.WaitMS = 10000
	}
	if len(input.Scopes) > 32 {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_EVENT_SCOPE", "订阅的 scope 数量过多")
	}
	allowed := make([]string, 0, len(input.Scopes))
	for _, scope := range input.Scopes {
		if EventScopeAllowed(grants, scope) && len(scope) <= 200 {
			allowed = append(allowed, scope)
		}
	}
	if len(allowed) == 0 {
		return EventPage{Events: []Event{}, Next: input.After}, nil
	}
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if core == nil {
		return EventPage{Events: []Event{}, Next: input.After}, nil
	}
	page, err := core.Events().Poll(ctx, input.After, allowed, time.Duration(input.WaitMS)*time.Millisecond)
	if err != nil {
		return nil, apiError(http.StatusServiceUnavailable, "PLUGIN_EVENTS_UNAVAILABLE", "事件通道暂不可用")
	}
	return page, nil
}

// credentialValue resolves one vault entry for injection. The plaintext is read
// only here, used immediately and never returned to the caller; a locked vault
// and a missing entry are distinguished so the plugin page can tell the user
// which one to fix.
func credentialValue(ctx context.Context, vault *credentials.Vault, name string) (string, error) {
	if vault == nil {
		return "", apiError(http.StatusServiceUnavailable, "PLUGIN_CREDENTIAL_UNAVAILABLE", "凭据库不可用")
	}
	value, err := vault.Get(ctx, name)
	if errors.Is(err, credentials.ErrLocked) {
		return "", apiError(http.StatusLocked, "PLUGIN_CREDENTIAL_LOCKED", "凭据库已锁定，请在凭据页解锁后重试")
	}
	if errors.Is(err, credentials.ErrNotFound) {
		return "", apiError(http.StatusNotFound, "PLUGIN_CREDENTIAL_MISSING", "凭据库中没有 "+name)
	}
	if err != nil {
		return "", apiError(http.StatusServiceUnavailable, "PLUGIN_CREDENTIAL_UNAVAILABLE", "无法读取凭据")
	}
	return string(value), nil
}
