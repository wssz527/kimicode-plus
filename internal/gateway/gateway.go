package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"enhancedkimi/internal/config"
	"enhancedkimi/internal/provider"
	"enhancedkimi/internal/usage"
)

// Gateway is the OpenAI-compatible boundary that Kimi Code speaks to this
// process. It never forwards the upstream supplier credential: Kimi Code
// authenticates with the local gateway token, and the provider key stays in the
// vault.
type Gateway struct {
	client   *provider.Client
	usage    *usage.Service
	dispatch func(func() error) error
	token    string
	started  time.Time
}

const (
	DefaultTokenHeader = "Authorization"
	MaxRequestBytes    = 1 << 20
	MaxMessages        = 200
	MaxMessageBytes    = 256 << 10
	MaxTools           = 128
	MaxToolCalls       = 64
	MaxModels          = 500
)

func New(client *provider.Client, ledger *usage.Service, dispatch func(func() error) error, token string) *Gateway {
	return &Gateway{client: client, usage: ledger, dispatch: dispatch, token: token, started: time.Now().UTC()}
}

// Token returns the local gateway credential. Callers must show it to the user
// once and never log it.
func (g *Gateway) Token() string { return g.token }

// Register mounts the surface. Paths are served on the same loopback listener
// as the enhancement API, so Host/Origin checks stay shared; the gateway does
// its own token check because Kimi Code is not a paired UI client.
func (g *Gateway) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/models", g.authorize(g.models))
	mux.HandleFunc("GET /v1/models/{id}", g.authorize(g.modelByID))
	mux.HandleFunc("POST /v1/chat/completions", g.authorize(g.completions))
}

func (g *Gateway) authorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameOriginRequest(r) {
			gatewayFail(w, 403, "ORIGIN_DENIED", "网关只接受本机回环请求")
			return
		}
		if !g.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="ekd-gateway"`)
			gatewayFail(w, 401, "GATEWAY_TOKEN_REQUIRED", "缺少或无效的本地网关令牌")
			return
		}
		next(w, r)
	}
}

// sameOriginRequest enforces the loopback boundary here as well as in the
// listener: a proxied or forwarded request must not be treated as local.
func sameOriginRequest(r *http.Request) bool {
	if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Forwarded") != "" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (g *Gateway) authorized(r *http.Request) bool {
	if g.token == "" {
		return false
	}
	header := r.Header.Get(DefaultTokenHeader)
	value := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if value == header && header != "" {
		// Accept a bare token only when it is not a scheme-looking header.
		value = strings.TrimSpace(header)
	}
	if value == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(value), []byte(g.token)) == 1
}

// gatewayError is the OpenAI-shaped error envelope. Kimi Code parses it, so the
// shape is stable even though the codes come from this process.
type gatewayError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

func gatewayFail(w http.ResponseWriter, status int, code, message string) {
	var envelope gatewayError
	envelope.Error.Message = message
	envelope.Error.Code = code
	switch {
	case status == 401 || status == 403:
		envelope.Error.Type = "authentication_error"
	case status == 429:
		envelope.Error.Type = "rate_limit_error"
	case status >= 500:
		envelope.Error.Type = "api_error"
	default:
		envelope.Error.Type = "invalid_request_error"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(envelope)
}

// upstreamFailure maps a provider failure onto a gateway status without
// inventing an outcome. An unexpected interruption is reported as unknown,
// never as a successful empty reply.
func upstreamFailure(w http.ResponseWriter, g *Gateway, err error) {
	var status *provider.StatusError
	switch {
	case errors.As(err, &status):
		switch status.Status {
		case 401, 403:
			// The upstream key is the problem, not the caller's gateway token.
			gatewayFail(w, 502, "UPSTREAM_AUTH", "上游拒绝了本地凭据，请检查账号的 secretRef")
		case 429:
			gatewayFail(w, 429, "UPSTREAM_RATE_LIMIT", "上游限流或额度不足；未自动重试")
		case 404:
			gatewayFail(w, 404, "UPSTREAM_MODEL_NOT_FOUND", "上游没有该模型或端点")
		default:
			gatewayFail(w, 502, "UPSTREAM_ERROR", "上游返回错误，详情未透传")
		}
	case errors.Is(err, config.ErrSafeMode):
		gatewayFail(w, 409, "SAFE_MODE", "安全模式禁止外发请求")
	case errors.Is(err, context.DeadlineExceeded):
		gatewayFail(w, 504, "UPSTREAM_TIMEOUT", "上游在预算内没有完成；未自动重试")
	case errors.Is(err, context.Canceled):
		// The client hung up; nothing useful can be written.
		gatewayFail(w, 499, "CLIENT_CANCELLED", "客户端已取消")
	default:
		var api *provider.APIError
		if errors.As(err, &api) {
			gatewayFail(w, api.Status(), api.Code, api.Message)
			return
		}
		gatewayFail(w, 502, "UPSTREAM_UNAVAILABLE", "无法连接上游或上游响应无效")
	}
	_ = g
}

// modelEntry is the OpenAI model object. Prices and capabilities stay out.
type modelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func (g *Gateway) models(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := g.client.DB.QueryContext(ctx, `SELECT id,name FROM registry_providers ORDER BY name,id`)
	if err != nil {
		gatewayFail(w, 503, "GATEWAY_STORAGE", "无法读取供应商配置")
		return
	}
	providers := []string{}
	for rows.Next() {
		var providerID, name string
		if err := rows.Scan(&providerID, &name); err != nil {
			rows.Close()
			gatewayFail(w, 503, "GATEWAY_STORAGE", "无法读取供应商配置")
			return
		}
		providers = append(providers, providerID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		gatewayFail(w, 503, "GATEWAY_STORAGE", "无法读取供应商配置")
		return
	}
	out := []modelEntry{}
	seen := map[string]bool{}
	truncated := false
	// The connection pool holds one connection, so the provider list is read
	// fully before any per-provider query starts.
	for _, providerID := range providers {
		entries, err := g.client.CatalogModels(ctx, providerID)
		if err != nil {
			// A broken provider must not hide the healthy ones; the caller can
			// still route by model ID.
			continue
		}
		for _, entry := range entries {
			// Kimi Code addresses models by bare ID. A duplicate ID across
			// providers stays ambiguous on purpose instead of silently picking
			// one supplier.
			if seen[entry.ID] {
				continue
			}
			if len(out) >= MaxModels {
				truncated = true
				break
			}
			seen[entry.ID] = true
			out = append(out, modelEntry{ID: entry.ID, Object: "model", Created: g.started.Unix(), OwnedBy: providerID})
		}
	}
	payload := map[string]any{"object": "list", "data": out}
	if truncated {
		payload["has_more"] = true
	}
	writeJSON(w, 200, payload)
}

// modelByID answers the retrieve form some clients probe before generation.
func (g *Gateway) modelByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var owner string
	err := g.client.DB.QueryRowContext(ctx, `SELECT provider_id FROM registry_models WHERE model_id=? ORDER BY provider_id LIMIT 1`, id).Scan(&owner)
	if err != nil {
		gatewayFail(w, 404, "MODEL_NOT_FOUND", "没有供应商声明该模型")
		return
	}
	writeJSON(w, 200, modelEntry{ID: id, Object: "model", Created: g.started.Unix(), OwnedBy: owner})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

// completionRequest accepts the fields Kimi Code actually sends, including its
// own extensions. Unknown fields are tolerated rather than rejected: a client
// upgrade must not be turned into a hard failure by this boundary. Fields that
// are accepted are either validated or forwarded, never silently dropped.
type completionRequest struct {
	Model               string            `json:"model"`
	Messages            []completionInput `json:"messages"`
	Stream              bool              `json:"stream"`
	StreamOptions       json.RawMessage   `json:"stream_options"`
	Temperature         *float64          `json:"temperature"`
	TopP                *float64          `json:"top_p"`
	MaxTokens           *int              `json:"max_tokens"`
	MaxCompletionTokens *int              `json:"max_completion_tokens"`
	Stop                json.RawMessage   `json:"stop"`
	User                string            `json:"user"`
	Thinking            json.RawMessage   `json:"thinking"`
	BuiltinFunction     json.RawMessage   `json:"builtin_function"`
	ReasoningEffort     string            `json:"reasoning_effort"`
	ToolChoice          json.RawMessage   `json:"tool_choice"`
	Tools               json.RawMessage   `json:"tools"`
	ParallelToolCall    *bool             `json:"parallel_tool_calls"`
	SessionID           string            `json:"session_id"`
	ProjectID           string            `json:"project_id"`
	AgentID             string            `json:"agent_id"`
}

type completionInput struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content"`
	Name             string          `json:"name"`
	ReasoningContent *string         `json:"reasoning_content"`
	ToolCallID       string          `json:"tool_call_id"`
	ToolCalls        []toolCallInput `json:"tool_calls"`
}

type toolCallInput struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

const (
	maxTokensLimit        = 1048576
	maxToolNameBytes      = 200
	maxToolArgumentsBytes = 256 << 10
)

func (g *Gateway) completions(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > MaxRequestBytes {
		gatewayFail(w, 413, "REQUEST_TOO_LARGE", "请求超过 1 MiB")
		return
	}
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxRequestBytes))
	if err := decoder.Decode(&raw); err != nil {
		gatewayFail(w, 400, "INVALID_JSON", "请求不是有效 JSON")
		return
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		gatewayFail(w, 400, "INVALID_JSON", "请求不是有效 JSON")
		return
	}
	var in completionRequest
	if err := json.Unmarshal(encoded, &in); err != nil {
		gatewayFail(w, 400, "INVALID_REQUEST", "请求字段类型不正确")
		return
	}
	messages, err := g.normalize(in)
	if err != nil {
		gatewayFail(w, 400, "INVALID_REQUEST", err.Error())
		return
	}
	model := strings.TrimSpace(in.Model)
	if model == "" || len(model) > 200 || strings.ContainsAny(model, "\x00\r\n") {
		gatewayFail(w, 400, "INVALID_MODEL", "model 不能为空")
		return
	}
	limit := in.MaxTokens
	if limit == nil {
		limit = in.MaxCompletionTokens
	}
	if limit != nil && (*limit < 1 || *limit > maxTokensLimit) {
		gatewayFail(w, 400, "INVALID_MAX_TOKENS", "max_tokens/max_completion_tokens 必须在 1–1048576")
		return
	}
	if in.Temperature != nil && (*in.Temperature < 0 || *in.Temperature > 2) {
		gatewayFail(w, 400, "INVALID_TEMPERATURE", "temperature 必须在 0–2")
		return
	}
	if in.TopP != nil && (*in.TopP < 0 || *in.TopP > 1) {
		gatewayFail(w, 400, "INVALID_TOP_P", "top_p 必须在 0–1")
		return
	}
	if err := validateTools(in); err != nil {
		gatewayFail(w, 400, "INVALID_TOOLS", err.Error())
		return
	}
	providerID, err := g.route(r.Context(), model)
	if err != nil {
		g.routeFailure(w, err)
		return
	}
	if in.SessionID == "" {
		in.SessionID = "gateway-" + shortID()
	}
	if in.AgentID == "" {
		in.AgentID = "gateway"
	}
	requestID := shortID()
	body, err := g.upstreamBody(in, model, messages)
	if err != nil {
		gatewayFail(w, 400, "INVALID_REQUEST", err.Error())
		return
	}
	effective, account, err := g.client.EffectiveConfig(r.Context(), providerID)
	if err != nil {
		gatewayFail(w, 502, "PROVIDER_CONFIG", "无法读取供应商配置")
		return
	}
	attribution := usage.EventInput{
		ProviderID: providerID, Model: model, RequestID: requestID, AttemptID: "1",
		SessionID: in.SessionID, ProjectID: in.ProjectID, AgentID: in.AgentID,
		Role: "main", RequestKind: "gateway", Protocol: effective.Protocol,
		Source: "gateway", At: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if in.Stream {
		g.streamCompletion(w, r, effective, model, body, attribution, account.AccountID)
		return
	}
	g.bufferedCompletion(w, r, effective, model, body, messages, attribution, account.AccountID)
}

// upstreamBody builds the request for the supplier from the accepted fields.
// Every accepted field is carried through with its original semantics.
func (g *Gateway) upstreamBody(in completionRequest, model string, messages []provider.Message) (map[string]any, error) {
	body := map[string]any{"model": model, "messages": messages, "stream": in.Stream}
	if in.Temperature != nil {
		body["temperature"] = *in.Temperature
	}
	if in.TopP != nil {
		body["top_p"] = *in.TopP
	}
	if in.MaxTokens != nil {
		body["max_tokens"] = *in.MaxTokens
	}
	if in.MaxCompletionTokens != nil {
		body["max_completion_tokens"] = *in.MaxCompletionTokens
	}
	if len(in.Stop) > 0 && string(in.Stop) != "null" {
		var stop any
		if err := json.Unmarshal(in.Stop, &stop); err != nil {
			return nil, errors.New("stop 必须是字符串或字符串数组")
		}
		body["stop"] = stop
	}
	if in.User != "" {
		body["user"] = in.User
	}
	if in.ReasoningEffort != "" {
		body["reasoning_effort"] = in.ReasoningEffort
	}
	for name, value := range map[string]json.RawMessage{
		"thinking":         in.Thinking,
		"builtin_function": in.BuiltinFunction,
		"tools":            in.Tools,
		"tool_choice":      in.ToolChoice,
	} {
		if len(value) == 0 || string(value) == "null" {
			continue
		}
		var decoded any
		if err := json.Unmarshal(value, &decoded); err != nil {
			return nil, fmt.Errorf("%s 不是有效值", name)
		}
		body[name] = decoded
	}
	if in.ParallelToolCall != nil {
		body["parallel_tool_calls"] = *in.ParallelToolCall
	}
	if in.Stream {
		options := map[string]any{}
		if len(in.StreamOptions) > 0 && string(in.StreamOptions) != "null" {
			if err := json.Unmarshal(in.StreamOptions, &options); err != nil {
				return nil, errors.New("stream_options 必须是对象")
			}
		}
		options["include_usage"] = true
		body["stream_options"] = options
	}
	return body, nil
}

func validateTools(in completionRequest) error {
	if len(in.Tools) == 0 || string(in.Tools) == "null" {
		return nil
	}
	var tools []json.RawMessage
	if err := json.Unmarshal(in.Tools, &tools); err != nil {
		return errors.New("tools 必须是数组")
	}
	if len(tools) > MaxTools {
		return fmt.Errorf("tools 最多 %d 个", MaxTools)
	}
	for index, raw := range tools {
		var tool struct {
			Type     string `json:"type"`
			Function struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		if err := json.Unmarshal(raw, &tool); err != nil {
			return fmt.Errorf("tools[%d] 结构无效", index)
		}
		if tool.Type != "function" && tool.Type != "builtin_function" {
			return fmt.Errorf("tools[%d].type 目前只支持 function 或 builtin_function", index)
		}
		if strings.TrimSpace(tool.Function.Name) == "" || len(tool.Function.Name) > maxToolNameBytes {
			return fmt.Errorf("tools[%d].function.name 不能为空且最长 %d 字节", index, maxToolNameBytes)
		}
	}
	return nil
}

// normalize turns client messages into provider messages, preserving role,
// reasoning, assistant tool calls and tool results.
func (g *Gateway) normalize(in completionRequest) ([]provider.Message, error) {
	if len(in.Messages) == 0 {
		return nil, errors.New("messages 不能为空")
	}
	if len(in.Messages) > MaxMessages {
		return nil, fmt.Errorf("messages 最多 %d 条", MaxMessages)
	}
	out := make([]provider.Message, 0, len(in.Messages))
	for index, message := range in.Messages {
		role := strings.TrimSpace(message.Role)
		switch role {
		case "system", "developer":
			role = "system"
		case "user", "assistant", "tool":
		default:
			return nil, fmt.Errorf("messages[%d].role 不受支持", index)
		}
		text, err := contentText(message.Content)
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", index, err)
		}
		if len(text) > MaxMessageBytes {
			return nil, fmt.Errorf("messages[%d] 超过 256 KiB", index)
		}
		if len(message.ToolCalls) > MaxToolCalls {
			return nil, fmt.Errorf("messages[%d].tool_calls 最多 %d 个", index, MaxToolCalls)
		}
		converted := provider.Message{Role: role, Content: text, Name: message.Name}
		if message.ReasoningContent != nil {
			converted.ReasoningContent = *message.ReasoningContent
		}
		for callIndex, call := range message.ToolCalls {
			if role != "assistant" {
				return nil, fmt.Errorf("messages[%d].tool_calls 只允许出现在 assistant 消息", index)
			}
			if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Function.Name) == "" {
				return nil, fmt.Errorf("messages[%d].tool_calls[%d] 需要 id 与 function.name", index, callIndex)
			}
			callType := call.Type
			if callType == "" {
				callType = "function"
			}
			if callType != "function" {
				return nil, fmt.Errorf("messages[%d].tool_calls[%d].type 只支持 function", index, callIndex)
			}
			if len(call.Function.Arguments) > maxToolArgumentsBytes {
				return nil, fmt.Errorf("messages[%d].tool_calls[%d] 参数超过 256 KiB", index, callIndex)
			}
			converted.ToolCalls = append(converted.ToolCalls, provider.ToolCall{
				ID: call.ID, Type: callType,
				Function: provider.ToolCallFunction{Name: call.Function.Name, Arguments: call.Function.Arguments},
			})
		}
		if role == "tool" {
			if strings.TrimSpace(message.ToolCallID) == "" {
				return nil, fmt.Errorf("messages[%d].tool_call_id 不能为空", index)
			}
			converted.ToolCallID = message.ToolCallID
		}
		// An assistant turn may be empty text when it carries tool calls; a
		// tool result may legitimately be empty, which the protocol permits.
		if text == "" && message.ReasoningContent == nil && len(converted.ToolCalls) == 0 && role != "tool" {
			return nil, fmt.Errorf("messages[%d] 内容为空", index)
		}
		out = append(out, converted)
	}
	return out, nil
}

// contentText accepts the string form and the text-part array form. Any
// non-text part is an explicit error, never a silent truncation.
func contentText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", errors.New("content 字符串无效")
		}
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", errors.New("content 必须是字符串或文本片段数组")
	}
	var builder strings.Builder
	for _, part := range parts {
		if part.Type != "text" && part.Type != "input_text" {
			return "", errors.New("网关只接受文本内容，图片/文件输入未实现")
		}
		builder.WriteString(part.Text)
	}
	return builder.String(), nil
}

// route picks the provider that owns a model ID. A model listed by more than
// one provider is an explicit ambiguity error: v0 has no failover, so guessing
// would silently bill a different account.
func (g *Gateway) route(ctx context.Context, model string) (string, error) {
	rows, err := g.client.DB.QueryContext(ctx, `SELECT DISTINCT provider_id FROM registry_models WHERE model_id=? ORDER BY provider_id`, model)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	candidates := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		candidates = append(candidates, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch len(candidates) {
	case 0:
		return "", errRoute{code: "MODEL_NOT_FOUND", message: "没有供应商声明该模型；请先导入目录", status: 404}
	case 1:
		return candidates[0], nil
	default:
		return "", errRoute{code: "AMBIGUOUS_MODEL", message: "多个供应商声明该模型，网关不自动选择", status: 409}
	}
}

type errRoute struct {
	status  int
	code    string
	message string
}

func (e errRoute) Error() string { return e.code + ": " + e.message }

func (g *Gateway) routeFailure(w http.ResponseWriter, err error) {
	var route errRoute
	if errors.As(err, &route) {
		gatewayFail(w, route.status, route.code, route.message)
		return
	}
	gatewayFail(w, 503, "GATEWAY_STORAGE", "无法解析模型路由")
}

func shortID() string {
	return strings.ToLower(fmt.Sprintf("%x", time.Now().UnixNano()))
}
