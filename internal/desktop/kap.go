package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const NativeAPI = "/api/v1"

type NativeUsage struct {
	ContextTokens int64  `json:"context_tokens"`
	ContextLimit  *int64 `json:"context_limit"`
}

type NativeSession struct {
	ID                 string       `json:"id"`
	Title              string       `json:"title"`
	CWD                string       `json:"cwd,omitempty"`
	Model              string       `json:"model,omitempty"`
	Status             string       `json:"status,omitempty"`
	Archived           bool         `json:"archived,omitempty"`
	ArchivedAt         string       `json:"archived_at,omitempty"`
	UpdatedAt          string       `json:"updated_at,omitempty"`
	LastTurnReason     string       `json:"last_turn_reason,omitempty"`
	Busy               bool         `json:"busy"`
	MainTurnActive     bool         `json:"main_turn_active"`
	PendingInteraction string       `json:"pending_interaction"`
	Usage              *NativeUsage `json:"usage,omitempty"`
}

type NativeAttachment struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
}

type NativeMessage struct {
	ID          string             `json:"id"`
	Role        string             `json:"role"`
	Text        string             `json:"text"`
	Tool        string             `json:"tool,omitempty"`
	At          string             `json:"at,omitempty"`
	Attachments []NativeAttachment `json:"attachments,omitempty"`
}

type NativeOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type NativeApproval struct {
	ID      string         `json:"id"`
	Title   string         `json:"title"`
	Detail  string         `json:"detail,omitempty"`
	Options []NativeOption `json:"options,omitempty"`
}

type SessionView struct {
	Session   NativeSession    `json:"session"`
	Messages  []NativeMessage  `json:"messages"`
	Approvals []NativeApproval `json:"approvals"`
	Goal      json.RawMessage  `json:"goal,omitempty"`
}

type nativeEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"msg"`
	Data    json.RawMessage `json:"data"`
}

type NativeError struct {
	Code    string
	Message string
	Status  int
}

func (e *NativeError) Error() string { return e.Message }

// kapTransportError keeps the user-facing message while preserving the cause so
// the retry policy can tell a refused connection from a mid-flight break.
type kapTransportError struct {
	message string
	cause   error
	// sent records whether the request may have reached the server.
	sent bool
}

func (e *kapTransportError) Error() string { return e.message }
func (e *kapTransportError) Unwrap() error { return e.cause }

type KAPClient struct {
	home   string
	client *http.Client

	mu      sync.Mutex
	base    string
	token   string
	watched map[string]bool
	writeMu sync.Mutex
	socket  *websocket.Conn
	// watchHook, when set, decides whether a subscribe succeeds. Tests install it
	// to exercise the rollback a refused subscribe triggers; production leaves it
	// nil and only the official side can refuse.
	watchHook func(string) error

	// resolved marks base/token as read from the official instance record, so
	// only a disk-derived endpoint is invalidated. Endpoints injected directly
	// (tests, diagnostics) are owned by the caller.
	resolved   bool
	backoffMin time.Duration
	backoffMax time.Duration
}

// Home reports the KIMI_CODE_HOME this client reads instance records from. The
// cache layer roots its session scan at the same directory.
func (k *KAPClient) Home() string { return k.home }

func NewKAPClient(home string) *KAPClient {
	return &KAPClient{
		home: filepath.Clean(home), watched: map[string]bool{}, client: &http.Client{Transport: &http.Transport{Proxy: nil}},
		backoffMin: 500 * time.Millisecond, backoffMax: 30 * time.Second,
	}
}

// invalidate forgets the resolved endpoint so the next call re-reads the
// instance record and token. The official client writes a new port and token on
// every launch, so a cached pair only stays valid for the process that wrote it.
// An endpoint injected by the caller is not disk state and must survive.
func (k *KAPClient) invalidate() {
	k.mu.Lock()
	if k.resolved {
		k.base = ""
		k.token = ""
		k.resolved = false
	}
	k.mu.Unlock()
}

func (k *KAPClient) resolve() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.base != "" && k.token != "" {
		return nil
	}
	base, token, err := readKAPEndpoint(k.home, 0)
	if err != nil {
		return err
	}
	k.base, k.token, k.resolved = base, token, true
	return nil
}

func readKAPEndpoint(home string, expectedPID int) (string, string, error) {
	if home == "" || !filepath.IsAbs(home) {
		return "", "", errors.New("必须指定独立的 KIMI_CODE_HOME 绝对路径")
	}
	entries, err := os.ReadDir(filepath.Join(home, "server", "instances"))
	if err != nil {
		return "", "", errors.New("未找到官方服务实例，请确认隔离客户端已启动")
	}
	type instance struct {
		Port        int    `json:"port"`
		Host        string `json:"host"`
		PID         int    `json:"pid"`
		HeartbeatAt int64  `json:"heartbeat_at"`
	}
	instances := []instance{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(home, "server", "instances", entry.Name()))
		if err != nil || len(data) > 65536 {
			continue
		}
		var item instance
		if json.Unmarshal(data, &item) == nil && item.Port > 0 && item.Port <= 65535 && (expectedPID == 0 || item.PID == expectedPID) {
			instances = append(instances, item)
		}
	}
	if len(instances) == 0 {
		return "", "", errors.New("未找到对应的官方服务实例记录")
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].HeartbeatAt > instances[j].HeartbeatAt })
	host := instances[0].Host
	if host == "" {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", "", errors.New("官方服务地址不是本机回环")
	}
	secret, err := os.ReadFile(filepath.Join(home, "server.token"))
	if err != nil {
		return "", "", errors.New("官方服务访问凭据不可用，请重新打开隔离客户端")
	}
	token := strings.TrimSpace(string(secret))
	if token == "" || len(token) > 4096 {
		return "", "", errors.New("官方服务访问凭据格式无效")
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(instances[0].Port)), token, nil
}

func (k *KAPClient) endpoint() (string, string, error) {
	if err := k.resolve(); err != nil {
		return "", "", err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.base, k.token, nil
}

func (k *KAPClient) Request(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	return k.requestWithPrefix(ctx, NativeAPI, method, path, body)
}

func (k *KAPClient) requestWithPrefix(ctx context.Context, prefix, method, path string, body any) (json.RawMessage, error) {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return nil, errors.New("官方接口路径无效")
	}
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = encoded
	}
	raw, err := k.sendWithPrefix(ctx, prefix, method, path, payload)
	if err == nil {
		return raw, nil
	}
	if !retryableRequest(ctx, method, err) {
		return nil, err
	}
	k.invalidate()
	return k.sendWithPrefix(ctx, prefix, method, path, payload)
}

// retryableRequest allows exactly one re-resolve-and-retry. A rejected token or
// a connection that never opened cannot have executed anything, so resending is
// side-effect free. Anything that may have reached the server is retried only
// for idempotent methods: a truncated response must not duplicate a prompt.
func retryableRequest(ctx context.Context, method string, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var native *NativeError
	if errors.As(err, &native) {
		return native.Status == http.StatusUnauthorized
	}
	var transport *kapTransportError
	if !errors.As(err, &transport) {
		return false
	}
	if !transport.sent {
		return true
	}
	return method == http.MethodGet || method == http.MethodHead
}

func (k *KAPClient) send(ctx context.Context, method, path string, payload []byte) (json.RawMessage, error) {
	return k.sendWithPrefix(ctx, NativeAPI, method, path, payload)
}

func (k *KAPClient) sendWithPrefix(ctx context.Context, prefix, method, path string, payload []byte) (json.RawMessage, error) {
	base, token, err := k.endpoint()
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, base+prefix+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := k.client.Do(req)
	if err != nil {
		return nil, &kapTransportError{message: "无法连接官方会话服务", cause: err, sent: !unsentFailure(err)}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, &kapTransportError{message: "官方会话服务在响应中断开", cause: err, sent: true}
	}
	if response.StatusCode == http.StatusUnauthorized {
		return nil, &NativeError{Code: "NATIVE_UNAUTHORIZED", Message: "官方会话服务拒绝了当前访问凭据，请重新打开客户端", Status: response.StatusCode}
	}
	var envelope nativeEnvelope
	if len(data) > 0 && json.Unmarshal(data, &envelope) == nil && (envelope.Code != 0 || envelope.Message != "" || envelope.Data != nil) {
		if envelope.Code != 0 {
			return nil, &NativeError{Code: "NATIVE_ERROR", Message: "官方接口返回：" + envelope.Message, Status: response.StatusCode}
		}
		if response.StatusCode >= 400 {
			return nil, &NativeError{Code: "NATIVE_HTTP", Message: "官方接口返回 HTTP " + strconv.Itoa(response.StatusCode), Status: response.StatusCode}
		}
		return envelope.Data, nil
	}
	if response.StatusCode >= 400 {
		return nil, &NativeError{Code: "NATIVE_HTTP", Message: "官方接口返回 HTTP " + strconv.Itoa(response.StatusCode), Status: response.StatusCode}
	}
	if len(data) == 0 {
		return json.RawMessage("null"), nil
	}
	return json.RawMessage(data), nil
}

// unsentFailure reports connection failures raised while dialing, where the
// request never reached the server.
func unsentFailure(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

func decodeNative[T any](raw json.RawMessage, label string) (T, error) {
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return value, fmt.Errorf("%s的响应格式与当前适配器不兼容", label)
	}
	return value, nil
}

func mapNativeSession(raw json.RawMessage) (NativeSession, error) {
	var value struct {
		ID                 string          `json:"id"`
		Title              string          `json:"title"`
		CWD                string          `json:"cwd"`
		Model              string          `json:"model"`
		Status             string          `json:"status"`
		Archived           bool            `json:"archived"`
		ArchivedAt         string          `json:"archived_at"`
		UpdatedAt          string          `json:"updated_at"`
		LastTurnReason     string          `json:"last_turn_reason"`
		LastTurnOutcome    string          `json:"last_turn_outcome"`
		Busy               bool            `json:"busy"`
		MainTurnActive     bool            `json:"main_turn_active"`
		PendingInteraction string          `json:"pending_interaction"`
		Usage              *NativeUsage    `json:"usage"`
		Activity           json.RawMessage `json:"activity"`
		AgentConfig        struct {
			Model string `json:"model"`
		} `json:"agent_config"`
		Meta struct {
			Archived   bool   `json:"archived"`
			ArchivedAt string `json:"archived_at"`
		} `json:"meta"`
		Metadata struct {
			CWD string `json:"cwd"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return NativeSession{}, errors.New("会话的响应格式与当前适配器不兼容")
	}
	if value.ID == "" {
		return NativeSession{}, errors.New("会话响应缺少有效 ID")
	}
	pending := value.PendingInteraction
	if pending == "" {
		pending = "none"
	}
	model := strings.TrimSpace(value.AgentConfig.Model)
	if model == "" {
		model = strings.TrimSpace(value.Model)
	}
	var activity string
	if len(value.Activity) != 0 && json.Unmarshal(value.Activity, &activity) != nil {
		var legacy struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(value.Activity, &legacy); err != nil {
			return NativeSession{}, errors.New("会话活动状态的格式与当前适配器不兼容")
		}
		if value.Status == "" {
			value.Status = legacy.Status
		}
	}
	if value.Status == "" {
		value.Status = activity
	}
	if value.CWD == "" {
		value.CWD = value.Metadata.CWD
	}
	if value.LastTurnReason == "" {
		value.LastTurnReason = value.LastTurnOutcome
		if value.LastTurnOutcome == "interrupted" {
			value.LastTurnReason = "cancelled"
		}
	}
	archived := value.Archived || value.Meta.Archived
	archivedAt := strings.TrimSpace(value.ArchivedAt)
	if archivedAt == "" {
		archivedAt = strings.TrimSpace(value.Meta.ArchivedAt)
	}
	return NativeSession{
		ID:                 value.ID,
		Title:              value.Title,
		CWD:                value.CWD,
		Model:              model,
		Status:             strings.TrimSpace(value.Status),
		Archived:           archived,
		ArchivedAt:         archivedAt,
		UpdatedAt:          value.UpdatedAt,
		LastTurnReason:     value.LastTurnReason,
		Busy:               value.Busy || (activity != "" && activity != "idle"),
		MainTurnActive:     value.MainTurnActive || activity == "turn",
		PendingInteraction: pending,
		Usage:              value.Usage,
	}, nil
}

func (k *KAPClient) UndoSession(ctx context.Context, sessionID, turnID, agentID string) ([]string, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(turnID) == "" {
		return nil, errors.New("撤回回合缺少会话 ID 或回合 ID")
	}
	body := map[string]any{"turn_id": turnID}
	if strings.TrimSpace(agentID) != "" {
		body["agent_id"] = strings.TrimSpace(agentID)
	}
	raw, err := k.Request(ctx, http.MethodPost, "/sessions/"+url.PathEscape(sessionID)+":undo", body)
	if err != nil {
		return nil, err
	}
	var out struct {
		RemovedTurnIDs []string `json:"removed_turn_ids"`
	}
	if len(raw) > 0 && string(raw) != "null" {
		_ = json.Unmarshal(raw, &out)
	}
	return out.RemovedTurnIDs, nil
}

func (k *KAPClient) ArchiveSession(ctx context.Context, sessionID string) error {
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return errors.New("归档会话缺少会话 ID")
	}
	_, err := k.Request(ctx, http.MethodPost, "/sessions/"+url.PathEscape(id)+":archive", map[string]any{})
	return err
}

func (k *KAPClient) ArchiveSessions(ctx context.Context, sessionIDs []string) error {
	cleaned := make([]string, 0, len(sessionIDs))
	seen := map[string]bool{}
	for _, raw := range sessionIDs {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		cleaned = append(cleaned, id)
	}
	if len(cleaned) == 0 {
		return nil
	}
	if len(cleaned) == 1 {
		return k.ArchiveSession(ctx, cleaned[0])
	}
	_, err := k.requestWithPrefix(ctx, "/api/v2", http.MethodPost, "/sessions:archive", map[string]any{"ids": cleaned})
	if err == nil {
		return nil
	}
	var native *NativeError
	if errors.As(err, &native) && (native.Status == http.StatusNotFound || native.Status == http.StatusMethodNotAllowed || native.Status == http.StatusNotImplemented) {
		var firstErr error
		for _, id := range cleaned {
			if singleErr := k.ArchiveSession(ctx, id); singleErr != nil && firstErr == nil {
				firstErr = singleErr
			}
		}
		return firstErr
	}
	return err
}

func (k *KAPClient) AbortSession(ctx context.Context, sessionID string) error {
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return errors.New("中止会话缺少会话 ID")
	}
	_, err := k.Request(ctx, http.MethodPost, "/sessions/"+url.PathEscape(id)+":abort", map[string]any{})
	return err
}

func (k *KAPClient) Sessions(ctx context.Context) ([]NativeSession, error) {
	items := []NativeSession{}
	seenIDs := map[string]bool{}
	seenTokens := map[string]bool{}
	query := url.Values{"page_size": {"100"}}
	for {
		raw, err := k.Request(ctx, http.MethodGet, "/sessions?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		page, err := decodeNative[struct {
			Items         []json.RawMessage `json:"items"`
			HasMore       bool              `json:"has_more"`
			NextPageToken string            `json:"next_page_token"`
		}](raw, "会话列表")
		if err != nil {
			return nil, err
		}
		for _, raw := range page.Items {
			item, err := mapNativeSession(raw)
			if err != nil {
				return nil, err
			}
			if !seenIDs[item.ID] {
				items = append(items, item)
				seenIDs[item.ID] = true
			}
		}
		if !page.HasMore {
			return items, nil
		}
		if page.NextPageToken == "" || seenTokens[page.NextPageToken] {
			return nil, errors.New("官方会话列表返回无效的分页标记")
		}
		seenTokens[page.NextPageToken] = true
		query.Set("page_token", page.NextPageToken)
	}
}

// NativeModel is one entry of the official model catalog. It carries the fields
// the pickers display (provider, capabilities, reasoning efforts), so a caller
// never has to re-read /config to describe a model the user is choosing.
type NativeModel struct {
	ID               string   `json:"id"`
	Provider         string   `json:"provider"`
	Model            string   `json:"model"`
	Name             string   `json:"name"`
	MaxContextSize   int64    `json:"max_context_size,omitempty"`
	Capabilities     []string `json:"capabilities,omitempty"`
	SupportEfforts   []string `json:"support_efforts,omitempty"`
	AdaptiveThinking bool     `json:"adaptive_thinking,omitempty"`
}

// Models reads the official catalog. The config map is keyed by the
// provider-qualified name, and a model entry may either repeat that prefix in
// its own `model` field or carry the bare id; both forms collapse to one key.
func (k *KAPClient) Models(ctx context.Context) ([]NativeModel, error) {
	raw, err := k.Request(ctx, http.MethodGet, "/config", nil)
	if err != nil {
		return nil, err
	}
	config, err := decodeNative[map[string]any](raw, "模型配置")
	if err != nil {
		return nil, err
	}
	models, _ := config["models"].(map[string]any)
	items := []NativeModel{}
	for id, raw := range models {
		provider, model := splitModelKey(id)
		if provider == "" {
			// An older config can key a model by its bare id. The entry itself may
			// still name the provider.
			provider, model = "", id
		}
		item := NativeModel{ID: id, Provider: provider, Model: model, Name: id}
		value, ok := raw.(map[string]any)
		if !ok {
			items = append(items, item)
			continue
		}
		if entryProvider, entryModel := modelStrings(value); entryProvider != "" || entryModel != "" {
			if entryProvider != "" {
				item.Provider = entryProvider
			}
			if entryModel != "" {
				item.Model = modelPart(entryModel)
				// The catalog key is authoritative: a stale entry must not make one
				// model appear under two names.
				if provider != "" && item.ID == provider+"/"+entryModel {
					item.ID = provider + "/" + item.Model
				}
			}
		}
		for _, key := range []string{"display_name", "displayName", "name"} {
			if candidate, ok := value[key].(string); ok && candidate != "" {
				item.Name = candidate
				break
			}
		}
		item.MaxContextSize = intField(value, "max_context_size", "maxContextSize")
		item.Capabilities = stringList(value["capabilities"])
		item.SupportEfforts = stringList(value["support_efforts"], value["supportEfforts"])
		item.AdaptiveThinking = value["adaptive_thinking"] == true || value["adaptiveThinking"] == true
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		return items[i].ID < items[j].ID
	})
	return items, nil
}

// modelPart drops a provider prefix the entry repeated in its own model field.
func modelPart(value string) string {
	if index := strings.Index(value, "/"); index > 0 && index < len(value)-1 {
		return value[index+1:]
	}
	return value
}

func modelStrings(entry map[string]any) (string, string) {
	provider, _ := entry["provider"].(string)
	model, _ := entry["model"].(string)
	return strings.TrimSpace(provider), strings.TrimSpace(model)
}

func intField(entry map[string]any, keys ...string) int64 {
	for _, key := range keys {
		switch value := entry[key].(type) {
		case float64:
			return int64(value)
		case int64:
			return value
		case int:
			return int64(value)
		}
	}
	return 0
}

func stringList(values ...any) []string {
	out := []string{}
	for _, raw := range values {
		items, ok := raw.([]any)
		if !ok {
			continue
		}
		for _, item := range items {
			if value, ok := item.(string); ok {
				out = append(out, value)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return out
}

func (k *KAPClient) websocketURL() (string, string, error) {
	base, token, err := k.endpoint()
	if err != nil {
		return "", "", err
	}
	u, err := url.Parse(base + NativeAPI + "/ws")
	if err != nil {
		return "", "", err
	}
	u.Scheme = "ws"
	return u.String(), token, nil
}
