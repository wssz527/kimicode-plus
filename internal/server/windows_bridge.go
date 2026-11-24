package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/plugins"
	"enhancedkimi/internal/security"
)

var bridgeActionPattern = regexp.MustCompile(`^(?:[a-z]+\.){0,4}[a-zA-Z][a-zA-Z0-9]*$`)

const (
	windowsBridgeTTL         = 12 * time.Hour
	windowsBridgeRenewWindow = 3 * time.Hour
)

type windowsBridgeSession struct {
	key          string
	sessionToken string
	expiresAt    time.Time
}

type windowsBridge struct {
	mu       sync.Mutex
	sessions map[string]windowsBridgeSession
	dialogMu sync.Mutex
	// ttl, renewWindow and now are the lifecycle knobs. They are fields rather
	// than constants so a test can drive twelve hours of use in microseconds;
	// zero values fall back to the production defaults.
	ttl         time.Duration
	renewWindow time.Duration
	now         func() time.Time
}

func newWindowsBridge() *windowsBridge {
	return &windowsBridge{sessions: map[string]windowsBridgeSession{}}
}

func (b *windowsBridge) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

func (b *windowsBridge) lifetime() time.Duration {
	if b.ttl > 0 {
		return b.ttl
	}
	return windowsBridgeTTL
}

func (b *windowsBridge) renewalWindow() time.Duration {
	if b.renewWindow > 0 {
		return b.renewWindow
	}
	return windowsBridgeRenewWindow
}

func (b *windowsBridge) issue(guard *security.Guard) (string, time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock()
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
	expiresAt := now.Add(b.lifetime())
	b.sessions[key] = windowsBridgeSession{
		key:          key,
		sessionToken: sessionToken,
		expiresAt:    expiresAt,
	}
	return key, expiresAt
}

// lookup resolves a bridge key and slides its session forward. A compatible
// client keeps one bridge key for as long as the core runs, so a fixed expiry
// made every compatible-mode request fail at the twelve-hour mark even though
// nothing had actually changed; renewing inside the last three hours keeps a
// long-running client connected without ever handing out an expired token.
func (b *windowsBridge) lookup(key string, guard *security.Guard) (windowsBridgeSession, bool) {
	if len(key) < 32 {
		return windowsBridgeSession{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock()
	for candidate, sess := range b.sessions {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(key)) == 1 {
			if now.After(sess.expiresAt) {
				delete(b.sessions, candidate)
				return windowsBridgeSession{}, false
			}
			if sess.expiresAt.Sub(now) < b.renewalWindow() {
				sess.sessionToken = guard.IssueSessionToken(security.Permissions)
				sess.expiresAt = now.Add(b.lifetime())
				b.sessions[candidate] = sess
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

func bridgeFailure(w http.ResponseWriter, status int, origin, code, message string) {
	writeBridgeJSON(w, status, origin, map[string]any{"error": map[string]any{"code": code, "message": message}})
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
		bridgeFailure(w, http.StatusForbidden, "null", "HOST_DENIED", "本地服务 Host 不匹配")
		return
	}
	trustedOrigin := allowedBridgeOrigin(origin, guard.Host())
	if r.Method == http.MethodOptions {
		if !trustedOrigin {
			writeBridgeJSON(w, http.StatusForbidden, "null", map[string]any{})
			return
		}
		writeBridgeJSON(w, http.StatusNoContent, origin, map[string]any{})
		return
	}
	// A request from a page that is not the renderer is refused before its
	// credential is looked at: the answer must not confirm whether a key exists.
	if !trustedOrigin {
		bridgeFailure(w, http.StatusForbidden, origin, "ORIGIN_DENIED", "增强桥来源未获授权")
		return
	}
	if r.Method != http.MethodPost {
		bridgeFailure(w, http.StatusMethodNotAllowed, origin, "METHOD_DENIED", "增强桥只接受 POST 请求")
		return
	}
	sess, ok := a.winBridge.lookup(r.Header.Get("X-EKD-Bridge"), guard)
	if !ok {
		// The bridge table lives in this process, so a core restart empties it
		// while the page keeps its key. Answering a renewable condition as
		// RPC_DENIED left the client retrying a credential that can never work;
		// this code tells it to fetch a fresh bridge session instead.
		bridgeFailure(w, http.StatusUnauthorized, origin, "BRIDGE_SESSION_EXPIRED", "增强桥会话已失效，正在等待重新接入")
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
	result, rpcCode, rpcErr := a.dispatchWindowsBridge(w, r, guard, coreHandler, sess.sessionToken, req.Action, req.Payload)
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
	w http.ResponseWriter,
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
		return windowsFeatureUnavailable(plugins.FeatureNativeOverlay)
	case "plugin.window.open", "window.close":
		return windowsFeatureUnavailable(plugins.FeaturePopout)
	case "browser.view.open", "browser.view.navigate", "browser.view.history", "browser.view.external",
		"browser.view.bounds", "browser.view.extract", "browser.view.adapter", "browser.view.close":
		return windowsFeatureUnavailable(plugins.FeatureBrowserView)
	case "window.ensureWidth":
		return map[string]any{}, "", nil
	case "host.pickDirectory", "host.pickFiles":
		// The dialog is interactive and can legitimately stay open for minutes.
		// Without extending the deadline the server's write timeout would answer
		// the RPC with a broken pipe long before the user picks anything.
		_ = ExtendWriteDeadline(w, 4*time.Minute)
		return a.winBridge.pickPaths(orig.Context(), action == "host.pickDirectory", payload)
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

func windowsFeatureUnavailable(feature string) (any, string, error) {
	limitation := plugins.CheckPlatformFeature("windows", feature)
	return nil, limitation.Code, errors.New(limitation.Message)
}

type windowsPickerOptions struct {
	Directory   bool   `json:"directory"`
	Single      bool   `json:"single"`
	Title       string `json:"title"`
	DefaultPath string `json:"defaultPath"`
	Filter      string `json:"filter"`
}

var dialogExtension = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+-]{0,15}$`)

func parseWindowsPickerOptions(directory bool, payload json.RawMessage) (windowsPickerOptions, error) {
	var input struct {
		Title       string `json:"title"`
		DefaultPath string `json:"defaultPath"`
		Single      bool   `json:"single"`
		Filters     []struct {
			Name       string   `json:"name"`
			Extensions []string `json:"extensions"`
		} `json:"filters"`
	}
	if err := json.Unmarshal(payload, &input); err != nil || len(input.Title) > 512 || len(input.DefaultPath) > 32768 || len(input.Filters) > 16 {
		return windowsPickerOptions{}, errors.New("文件选择参数无效")
	}
	options := windowsPickerOptions{Directory: directory, Single: input.Single, Title: strings.TrimSpace(input.Title), DefaultPath: strings.TrimSpace(input.DefaultPath)}
	if options.Title == "" {
		options.Title = "添加附件"
		if directory {
			options.Title = "选择项目文件夹"
		}
	}
	var filters []string
	for _, filter := range input.Filters {
		if filter.Name == "" || len(filter.Name) > 128 || strings.ContainsAny(filter.Name, "|\x00") || len(filter.Extensions) == 0 || len(filter.Extensions) > 16 {
			return windowsPickerOptions{}, errors.New("文件类型筛选无效")
		}
		var patterns []string
		for _, extension := range filter.Extensions {
			if extension != "*" && !dialogExtension.MatchString(extension) {
				return windowsPickerOptions{}, errors.New("文件类型筛选无效")
			}
			patterns = append(patterns, "*."+extension)
		}
		filters = append(filters, filter.Name, strings.Join(patterns, ";"))
	}
	options.Filter = strings.Join(filters, "|")
	return options, nil
}

func (b *windowsBridge) pickPaths(ctx context.Context, directory bool, payload json.RawMessage) (any, string, error) {
	options, err := parseWindowsPickerOptions(directory, payload)
	if err != nil {
		return nil, "NATIVE_RPC", err
	}
	if runtime.GOOS != "windows" {
		return nil, "HOST_DIALOG_UNAVAILABLE", errors.New("当前核心无法打开 Windows 文件选择器，请手动输入路径。")
	}
	if !b.dialogMu.TryLock() {
		return nil, "HOST_DIALOG_BUSY", errors.New("已有一个文件选择器，请先完成或取消。")
	}
	defer b.dialogMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	paths, code, err := showWindowsPicker(ctx, options)
	if err != nil {
		return nil, code, err
	}
	if directory {
		path := ""
		if len(paths) > 0 {
			path = paths[0]
		}
		return map[string]string{"path": path}, "", nil
	}
	files := make([]map[string]string, 0, len(paths))
	for _, path := range paths {
		files = append(files, map[string]string{"path": path, "name": filepath.Base(path)})
	}
	return files, "", nil
}

const windowsPickerScript = `
$ErrorActionPreference = 'Stop'
[Console]::InputEncoding = [Text.UTF8Encoding]::new($false)
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
if (-not [Environment]::UserInteractive -or [Diagnostics.Process]::GetCurrentProcess().SessionId -eq 0) {
  [Console]::WriteLine('{"interactive":false}')
  exit 0
}
$options = [Console]::In.ReadToEnd() | ConvertFrom-Json
Add-Type -AssemblyName System.Windows.Forms
$paths = @()
if ($options.directory) {
  $dialog = New-Object System.Windows.Forms.FolderBrowserDialog
  $dialog.Description = $options.title
  $dialog.ShowNewFolderButton = $true
  if ($options.defaultPath) { $dialog.SelectedPath = $options.defaultPath }
} else {
  $dialog = New-Object System.Windows.Forms.OpenFileDialog
  $dialog.Title = $options.title
  $dialog.Multiselect = -not $options.single
  $dialog.CheckFileExists = $true
  if ($options.filter) { $dialog.Filter = $options.filter }
  if ($options.defaultPath) { $dialog.InitialDirectory = $options.defaultPath }
}
try {
  if ($dialog.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
    if ($options.directory) { $paths = @($dialog.SelectedPath) } else { $paths = @($dialog.FileNames) }
  }
} finally {
  $dialog.Dispose()
}
[Console]::WriteLine((@{interactive = $true; paths = $paths} | ConvertTo-Json -Compress))
`

func showWindowsPicker(ctx context.Context, options windowsPickerOptions) ([]string, string, error) {
	root := os.Getenv("SystemRoot")
	if !filepath.IsAbs(root) {
		return nil, "HOST_DIALOG_UNAVAILABLE", errors.New("无法启动 Windows 文件选择器，请手动输入路径。")
	}
	powershell := filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	script := utf16.Encode([]rune(windowsPickerScript))
	encoded := make([]byte, 2*len(script))
	for i, unit := range script {
		binary.LittleEndian.PutUint16(encoded[2*i:], unit)
	}
	cmd := exec.CommandContext(ctx, powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-STA", "-EncodedCommand", base64.StdEncoding.EncodeToString(encoded))
	input, _ := json.Marshal(options)
	cmd.Stdin = bytes.NewReader(input)
	output, err := cmd.Output()
	var result struct {
		Interactive bool     `json:"interactive"`
		Paths       []string `json:"paths"`
	}
	if err != nil || json.Unmarshal(output, &result) != nil {
		return nil, "HOST_DIALOG_UNAVAILABLE", errors.New("无法打开 Windows 文件选择器，请手动输入路径。")
	}
	if !result.Interactive {
		return nil, "HOST_DIALOG_UNAVAILABLE", errors.New("增强核心未在 Windows 交互桌面运行。请从客户端选择，或手动输入完整路径。")
	}
	return result.Paths, "", nil
}
