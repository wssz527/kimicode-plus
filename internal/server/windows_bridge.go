package server

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"time"

	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/security"
)

var bridgeActionPattern = regexp.MustCompile(`^(?:[a-z]+\.){0,4}[a-zA-Z][a-zA-Z0-9]*$`)

type windowsBridgeSession struct {
	key          string
	sessionToken string
	expiresAt    time.Time
}

type windowsBridge struct {
	mu       sync.Mutex
	sessions map[string]windowsBridgeSession
}

func newWindowsBridge() *windowsBridge {
	return &windowsBridge{sessions: map[string]windowsBridgeSession{}}
}

func (b *windowsBridge) issue(guard *security.Guard) (string, time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	for k, s := range b.sessions {
		if now.After(s.expiresAt) {
			delete(b.sessions, k)
		}
	}
	if len(b.sessions) >= 32 {
		// Evict the oldest session so repeated re-attaches never exhaust the table.
		var oldestKey string
		var oldestAt time.Time
		for k, s := range b.sessions {
			if oldestKey == "" || s.expiresAt.Before(oldestAt) {
				oldestKey = k
				oldestAt = s.expiresAt
			}
		}
		if oldestKey != "" {
			delete(b.sessions, oldestKey)
		}
	}
	key := security.Random()
	sessionToken := guard.IssueSessionToken(security.Permissions)
	expiresAt := now.Add(12 * time.Hour)
	b.sessions[key] = windowsBridgeSession{
		key:          key,
		sessionToken: sessionToken,
		expiresAt:    expiresAt,
	}
	return key, expiresAt
}

func (b *windowsBridge) lookup(key string) (windowsBridgeSession, bool) {
	if len(key) < 32 {
		return windowsBridgeSession{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	for candidate, sess := range b.sessions {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(key)) == 1 {
			if now.After(sess.expiresAt) {
				delete(b.sessions, candidate)
				return windowsBridgeSession{}, false
			}
			return sess, true
		}
	}
	return windowsBridgeSession{}, false
}

func (a *App) registerWindowsBridgeRoutes(mux *http.ServeMux, guard *security.Guard) {
	if a.winBridge == nil {
		a.winBridge = newWindowsBridge()
	}
	mux.HandleFunc("POST /enhance/v1/desktop/bridge-session", func(w http.ResponseWriter, r *http.Request) {
		key, expiresAt := a.winBridge.issue(guard)
		httpx.JSON(w, 200, map[string]any{
			"bridgeKey": key,
			"expiresAt": expiresAt.UTC().Format(time.RFC3339),
		})
	})
}

func (a *App) wrapWindowsBridge(guard *security.Guard, coreHandler http.Handler) http.Handler {
	if a.winBridge == nil {
		a.winBridge = newWindowsBridge()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ekd/native/v1" {
			coreHandler.ServeHTTP(w, r)
			return
		}
		a.handleWindowsBridgeRPC(w, r, guard, coreHandler)
	})
}

func writeBridgeJSON(w http.ResponseWriter, status int, origin string, value any) {
	if origin == "" {
		origin = "null"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Headers", "content-type,x-ekd-bridge")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Private-Network", "true")
	w.Header().Set("Vary", "Origin")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func allowedBridgeOrigin(origin, host string) bool {
	if origin == "" || origin == "app://renderer" || origin == "http://"+host {
		return true
	}
	return false
}

func (a *App) handleWindowsBridgeRPC(w http.ResponseWriter, r *http.Request, guard *security.Guard, coreHandler http.Handler) {
	origin := r.Header.Get("Origin")
	if r.Host != guard.Host() {
		writeBridgeJSON(w, 403, "null", map[string]any{"error": map[string]any{"code": "HOST_DENIED", "message": "本地服务 Host 不匹配"}})
		return
	}
	trustedOrigin := allowedBridgeOrigin(origin, guard.Host())
	if r.Method == http.MethodOptions {
		if !trustedOrigin {
			writeBridgeJSON(w, 403, "null", map[string]any{})
			return
		}
		writeBridgeJSON(w, 204, origin, map[string]any{})
		return
	}
	sess, ok := a.winBridge.lookup(r.Header.Get("X-EKD-Bridge"))
	if !trustedOrigin || !ok || r.Method != http.MethodPost {
		writeBridgeJSON(w, 403, origin, map[string]any{"error": map[string]any{"code": "RPC_DENIED", "message": "增强桥来源或凭据无效"}})
		return
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 24*1024*1024+1))
	if err != nil || len(bodyBytes) > 24*1024*1024 {
		writeBridgeJSON(w, 413, origin, map[string]any{"error": map[string]any{"code": "RPC_TOO_LARGE", "message": "RPC 请求过大"}})
		return
	}
	var req struct {
		Protocol int             `json:"protocol"`
		ID       string          `json:"id"`
		Action   string          `json:"action"`
		Payload  json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeBridgeJSON(w, 200, origin, map[string]any{"protocol": 1, "id": "", "error": map[string]any{"code": "NATIVE_RPC", "message": "增强桥请求格式无效"}})
		return
	}
	if req.Protocol != 1 || req.ID == "" || len(req.ID) > 100 || req.Action == "" || len(req.Action) > 80 || !bridgeActionPattern.MatchString(req.Action) {
		writeBridgeJSON(w, 200, origin, map[string]any{"protocol": 1, "id": req.ID, "error": map[string]any{"code": "NATIVE_RPC", "message": "增强桥请求格式无效"}})
		return
	}
	result, rpcCode, rpcErr := a.dispatchWindowsBridge(r, guard, coreHandler, sess.sessionToken, req.Action, req.Payload)
	if rpcErr != nil {
		if rpcCode == "" {
			rpcCode = "NATIVE_RPC"
		}
		writeBridgeJSON(w, 200, origin, map[string]any{
			"protocol": 1,
			"id":       req.ID,
			"error":    map[string]any{"code": rpcCode, "message": rpcErr.Error()},
		})
		return
	}
	writeBridgeJSON(w, 200, origin, map[string]any{
		"protocol": 1,
		"id":       req.ID,
		"result":   result,
	})
}

func (a *App) dispatchWindowsBridge(
	orig *http.Request,
	guard *security.Guard,
	coreHandler http.Handler,
	sessionToken string,
	action string,
	payload json.RawMessage,
) (any, string, error) {
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	switch action {
	case "core.fetch":
		var input struct {
			Path       string            `json:"path"`
			Method     string            `json:"method"`
			Headers    map[string]string `json:"headers"`
			BodyBase64 string            `json:"bodyBase64"`
		}
		if err := json.Unmarshal(payload, &input); err != nil {
			return nil, "NATIVE_RPC", errors.New("核心请求参数无效")
		}
		if !strings.HasPrefix(input.Path, "/enhance/v1/") || strings.Contains(input.Path, "..") || strings.Contains(input.Path, `\`) {
			return nil, "NATIVE_RPC", errors.New("核心请求路径不被允许")
		}
		method := strings.ToUpper(strings.TrimSpace(input.Method))
		if method == "" {
			method = http.MethodGet
		}
		switch method {
		case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		default:
			return nil, "NATIVE_RPC", errors.New("核心请求方法不被允许")
		}
		var body []byte
		if input.BodyBase64 != "" {
			decoded, err := base64.StdEncoding.DecodeString(input.BodyBase64)
			if err != nil {
				return nil, "NATIVE_RPC", errors.New("核心请求体编码无效")
			}
			if len(decoded) > 16*1024*1024 {
				return nil, "NATIVE_RPC", errors.New("核心请求体过大")
			}
			body = decoded
		}
		innerReq := httptest.NewRequest(method, "http://"+guard.Host()+input.Path, bytes.NewReader(body)).WithContext(orig.Context())
		innerReq.Host = guard.Host()
		innerReq.Header.Set("Accept", "application/json")
		innerReq.Header.Set("Authorization", "Bearer "+sessionToken)
		for k, v := range input.Headers {
			switch strings.ToLower(k) {
			case "accept", "content-type", "idempotency-key", "x-attachment-sha256", "x-attachment-type":
				innerReq.Header.Set(k, v)
			}
		}
		if len(body) > 0 && innerReq.Header.Get("Content-Type") == "" {
			innerReq.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		coreHandler.ServeHTTP(rec, innerReq)
		res := rec.Result()
		defer res.Body.Close()
		respBytes, _ := io.ReadAll(res.Body)
		ct := res.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/json"
		}
		return map[string]any{
			"status":     res.StatusCode,
			"headers":    [][]string{{"Content-Type", ct}},
			"bodyBase64": base64.StdEncoding.EncodeToString(respBytes),
		}, "", nil

	case "host.syncOverlayEntries":
		return map[string]any{"count": 0}, "", nil
	case "window.ensureWidth", "window.close":
		return map[string]any{}, "", nil
	case "host.pickDirectory", "host.pickFiles":
		return nil, "HOST_DIALOG_UNAVAILABLE", errors.New("当前 Windows 渲染层直连模式请直接使用客户端文件选择能力")
	default:
		innerReq := httptest.NewRequest(http.MethodPost, "http://"+guard.Host()+"/enhance/v1/desktop/actions/"+action, bytes.NewReader(payload)).WithContext(orig.Context())
		innerReq.Host = guard.Host()
		innerReq.Header.Set("Accept", "application/json")
		innerReq.Header.Set("Content-Type", "application/json")
		innerReq.Header.Set("Authorization", "Bearer "+sessionToken)
		rec := httptest.NewRecorder()
		coreHandler.ServeHTTP(rec, innerReq)
		res := rec.Result()
		defer res.Body.Close()
		var body struct {
			Value any `json:"value"`
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(res.Body).Decode(&body)
		if res.StatusCode >= 400 || body.Error != nil {
			code := "CORE_ERROR"
			msg := "核心请求失败"
			if body.Error != nil {
				if body.Error.Code != "" {
					code = body.Error.Code
				}
				if body.Error.Message != "" {
					msg = body.Error.Message
				}
			}
			return nil, code, errors.New(msg)
		}
		return body.Value, "", nil
	}
}
