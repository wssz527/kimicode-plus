package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"enhancedkimi/internal/webassets"
)

// WindowsCDPTarget mirrors one entry returned by Chromium's /json/list endpoint.
type WindowsCDPTarget struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	Attached             bool   `json:"attached,omitempty"`
}

// WindowsCDPVersion mirrors Chromium's /json/version payload.
type WindowsCDPVersion struct {
	Browser              string `json:"Browser"`
	ProtocolVersion      string `json:"Protocol-Version"`
	UserAgent            string `json:"User-Agent"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// WindowsCDPAttachOptions configures one renderer-direct CDP injection on Windows.
type WindowsCDPAttachOptions struct {
	DebugPort          int
	HostPID            int
	HostStarted        string
	CoreURL            string
	PairingToken       string
	CoreVersion        string
	Profile            string
	Daily              bool
	AppVersionOverride string
	// Optional post-inject audit hook (tests inject deterministic connection tables).
	AuditClients func(ctx context.Context, debugPort, hostPID int) ([]WindowsDebugClientAlert, error)
}

var (
	uaElectronPattern = regexp.MustCompile(`Electron/([0-9][0-9A-Za-z._-]*)`)
	uaKimiPattern     = regexp.MustCompile(`(?i)(?:kimi-code-app|kimi\s*code)/([0-9][0-9A-Za-z._-]*)`)
)

// ParseUserAgentVersions extracts Electron and app version hints from Chromium's
// User-Agent string when main-process Node APIs are not attached directly.
func ParseUserAgentVersions(userAgent string) (appVersion, electron string) {
	if m := uaElectronPattern.FindStringSubmatch(userAgent); len(m) == 2 {
		electron = strings.TrimSpace(m[1])
	}
	if m := uaKimiPattern.FindStringSubmatch(userAgent); len(m) == 2 {
		appVersion = strings.TrimSpace(m[1])
	}
	return appVersion, electron
}

func isMainRendererURL(raw string) bool {
	if !strings.HasPrefix(raw, "app://renderer/") {
		return false
	}
	if strings.Contains(raw, "screenshot") || strings.Contains(raw, "browser-overlay") || strings.Contains(raw, "browser-control") {
		return false
	}
	rest := strings.TrimPrefix(raw, "app://renderer/")
	if idx := strings.IndexAny(rest, "?#"); idx >= 0 {
		rest = rest[:idx]
	}
	return rest == "" || rest == "index.html" || !strings.HasSuffix(rest, ".html")
}

// FilterWindowsRendererTargets selects only main Kimi Code renderer pages served
// over app://renderer/ whose WebSocket URL stays strictly on 127.0.0.1:<port>.
func FilterWindowsRendererTargets(targets []WindowsCDPTarget, debugPort int) []WindowsCDPTarget {
	portText := strconv.Itoa(debugPort)
	var out []WindowsCDPTarget
	for _, target := range targets {
		if target.Type != "page" {
			continue
		}
		if !isMainRendererURL(target.URL) {
			continue
		}
		if target.WebSocketDebuggerURL == "" {
			continue
		}
		u, err := url.Parse(target.WebSocketDebuggerURL)
		if err != nil {
			continue
		}
		if (u.Scheme != "ws" && u.Scheme != "wss") || u.Hostname() != "127.0.0.1" || u.Port() != portText {
			continue
		}
		out = append(out, target)
	}
	return out
}

// mintWindowsBridgeKey pairs with Go Core and requests a renderer-scoped
// X-EKD-Bridge key so the renderer page never receives the core pairingToken or
// sessionToken.
func mintWindowsBridgeKey(ctx context.Context, coreURL, pairingToken string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
	pairBody, _ := json.Marshal(map[string]any{
		"pairingToken": pairingToken,
		"permissions":  []string{"adapter:write"},
	})
	pairReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(coreURL, "/")+"/enhance/v1/pair", bytes.NewReader(pairBody))
	if err != nil {
		return "", err
	}
	pairReq.Header.Set("Content-Type", "application/json")
	pairResp, err := client.Do(pairReq)
	if err != nil {
		return "", fmt.Errorf("无法连接增强核心进行配对：%w", err)
	}
	defer pairResp.Body.Close()
	var pairResult struct {
		SessionToken string `json:"sessionToken"`
		Error        *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(pairResp.Body).Decode(&pairResult)
	if pairResp.StatusCode >= 400 || pairResult.SessionToken == "" {
		msg := "增强核心配对失败"
		if pairResult.Error != nil && pairResult.Error.Message != "" {
			msg = pairResult.Error.Message
		}
		return "", errors.New(msg)
	}

	bridgeReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(coreURL, "/")+"/enhance/v1/desktop/bridge-session", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return "", err
	}
	bridgeReq.Header.Set("Content-Type", "application/json")
	bridgeReq.Header.Set("Authorization", "Bearer "+pairResult.SessionToken)
	bridgeResp, err := client.Do(bridgeReq)
	if err != nil {
		return "", fmt.Errorf("无法签发 Windows 增强桥凭据：%w", err)
	}
	defer bridgeResp.Body.Close()
	var bridgeResult struct {
		BridgeKey string `json:"bridgeKey"`
		Error     *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(bridgeResp.Body).Decode(&bridgeResult)
	if bridgeResp.StatusCode >= 400 || bridgeResult.BridgeKey == "" {
		msg := "签发 Windows 增强桥凭据失败"
		if bridgeResult.Error != nil && bridgeResult.Error.Message != "" {
			msg = bridgeResult.Error.Message
		}
		return "", errors.New(msg)
	}
	return bridgeResult.BridgeKey, nil
}

func fetchWindowsCDPJSON(ctx context.Context, debugPort int, path string, out any) error {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d%s", debugPort, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	client := &http.Client{
		Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("CDP 端点 %s 返回 HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func buildWindowsRendererBootstrap(optionsJSON string) string {
	return fmt.Sprintf(`(async () => {
  const cfg = %s;
  const ua = navigator.userAgent || '';
  const ui = window.__ekdNativeUI;
  if (ui?.mainBridgeCore === cfg.endpoint) {
    return {status: 'installed', transport: 'main-loopback-rpc', userAgent: ua};
  }
  if (!cfg.appVersion) {
    const m = ua.match(/(?:kimi-code-app|kimi\s*code)\/([0-9][0-9A-Za-z._-]*)/i);
    if (m) cfg.appVersion = m[1];
  }
  if (!cfg.electron) {
    const m = ua.match(/Electron\/([0-9][0-9A-Za-z._-]*)/);
    if (m) cfg.electron = m[1];
  }
  const run = (%s);
  const installed = await run(cfg);
  return {
    status: installed?.status || (window.__ekdNativeUI ? 'installed' : 'failed'),
    userAgent: ua,
  };
})()`, optionsJSON, rendererBundle)
}

// AttachWindowsCDP connects to a verified loopback Chromium debugging port on
// Windows, injects the shared rendererBundle into each app://renderer/ page,
// verifies installation, and audits for unexpected external debugging clients.
func AttachWindowsCDP(ctx context.Context, opts WindowsCDPAttachOptions) (*AttachResult, error) {
	if opts.DebugPort <= 0 || opts.DebugPort > 65535 {
		return nil, errors.New("缺少有效的 Windows 调试端口")
	}
	if opts.CoreURL == "" || len(opts.PairingToken) < AttachTokenMinLength {
		return nil, errors.New("缺少增强核心地址或配对凭据")
	}

	var version WindowsCDPVersion
	_ = fetchWindowsCDPJSON(ctx, opts.DebugPort, "/json/version", &version)

	var allTargets []WindowsCDPTarget
	deadline := time.Now().Add(20 * time.Second)
	var targets []WindowsCDPTarget
	var lastErr error
	for {
		allTargets = nil
		if err := fetchWindowsCDPJSON(ctx, opts.DebugPort, "/json/list", &allTargets); err != nil {
			lastErr = err
		} else {
			targets = FilterWindowsRendererTargets(allTargets, opts.DebugPort)
			if len(targets) > 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return nil, fmt.Errorf("无法连接调试端口 127.0.0.1:%d：%w", opts.DebugPort, lastErr)
			}
			return nil, errors.New("未在调试端口发现 Kimi Code 主渲染页面（app://renderer/）")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}

	// Check if any renderer page already reports an attached external debugger
	// before we open our own WebSocket session.
	var preAttachAlerts []WindowsDebugClientAlert
	for _, t := range targets {
		if t.Attached {
			preAttachAlerts = append(preAttachAlerts, WindowsDebugClientAlert{
				DebugPort: opts.DebugPort,
				Reason:    fmt.Sprintf("页面 %s 在注入前已被其他调试客户端附着", t.URL),
			})
		}
	}

	bridgeKey, err := mintWindowsBridgeKey(ctx, opts.CoreURL, opts.PairingToken)
	if err != nil {
		return nil, err
	}

	assets := webassets.FS()
	moduleSource, err := fs.ReadFile(assets, "host/enhancement.js")
	if err != nil {
		return nil, err
	}
	cssSource, err := fs.ReadFile(assets, "host/enhancement.css")
	if err != nil {
		return nil, err
	}

	appVersion, electronVersion := ParseUserAgentVersions(version.UserAgent)
	overrideApp := ""
	if opts.Profile != "" && strings.TrimSpace(opts.AppVersionOverride) != "" {
		overrideApp = strings.TrimSpace(opts.AppVersionOverride)
	}
	configAppVersion := appVersion
	if overrideApp != "" {
		configAppVersion = overrideApp
	}

	rendererConfig, err := json.Marshal(map[string]any{
		"endpoint":     strings.TrimRight(opts.CoreURL, "/"),
		"bridgeKey":    bridgeKey,
		"surface":      "host",
		"transport":    "windows-cdp-loopback",
		"appVersion":   configAppVersion,
		"electron":     electronVersion,
		"moduleSource": string(moduleSource),
		"cssSource":    string(cssSource),
	})
	if err != nil {
		return nil, err
	}
	script := buildWindowsRendererBootstrap(string(rendererConfig))

	transport := "windows-cdp-loopback"
	var detectedUA string
	injectedCount := 0
	for _, target := range targets {
		pageRes, evalErr := injectWindowsPageTarget(ctx, target.WebSocketDebuggerURL, script)
		if evalErr != nil {
			return nil, fmt.Errorf("Windows 渲染页面注入失败（%s）：%w", target.URL, evalErr)
		}
		if pageRes.Status != "installed" {
			return nil, fmt.Errorf("Windows 渲染页面未确认挂载成功（状态：%s）", pageRes.Status)
		}
		injectedCount++
		if pageRes.Transport == "main-loopback-rpc" {
			transport = pageRes.Transport
		}
		if pageRes.UserAgent != "" {
			detectedUA = pageRes.UserAgent
		}
	}
	if injectedCount == 0 {
		return nil, errors.New("没有成功注入任何 Kimi Code 渲染页面")
	}

	if uaApp, uaElec := ParseUserAgentVersions(detectedUA); uaApp != "" || uaElec != "" {
		if appVersion == "" {
			appVersion = uaApp
		}
		if electronVersion == "" {
			electronVersion = uaElec
		}
	}
	if overrideApp != "" {
		appVersion = overrideApp
	}
	if appVersion == "" {
		appVersion = "unknown"
	}
	if electronVersion == "" {
		electronVersion = "unknown"
	}

	alerts := append([]WindowsDebugClientAlert(nil), preAttachAlerts...)
	if opts.AuditClients != nil {
		if extra, auditErr := opts.AuditClients(ctx, opts.DebugPort, opts.HostPID); auditErr == nil && len(extra) > 0 {
			alerts = append(alerts, extra...)
		}
	}

	verified := IsVerifiedHost(appVersion, electronVersion)
	return &AttachResult{
		Status:           "installed",
		Transport:        transport,
		Protocol:         1,
		AppVersion:       appVersion,
		Electron:         electronVersion,
		HostPID:          opts.HostPID,
		DOMRevision:      1,
		Verified:         verified,
		VersionNote:      HostVersionNote(appVersion, electronVersion),
		Daily:            opts.Daily,
		Platform:         "windows",
		DebugPort:        opts.DebugPort,
		DebugPortWarning: FormatDebugClientAlerts(alerts),
		hostStarted:      opts.HostStarted,
	}, nil
}

type windowsPageInjectResult struct {
	Status    string `json:"status"`
	Transport string `json:"transport"`
	UserAgent string `json:"userAgent"`
}

func injectWindowsPageTarget(ctx context.Context, wsURL, script string) (windowsPageInjectResult, error) {
	client := &http.Client{
		Transport: &http.Transport{},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		return windowsPageInjectResult{}, err
	}
	conn.SetReadLimit(32 << 20)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	session := &CDP{conn: conn, pending: map[int]chan cdpResponse{}, closed: make(chan struct{})}
	go session.read()

	if _, err := session.call(ctx, "Runtime.enable", map[string]any{}); err != nil {
		return windowsPageInjectResult{}, err
	}
	if _, err := session.call(ctx, "Page.enable", map[string]any{}); err != nil {
		return windowsPageInjectResult{}, err
	}
	raw, err := session.Evaluate(ctx, script)
	if err != nil {
		return windowsPageInjectResult{}, err
	}
	var res windowsPageInjectResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return windowsPageInjectResult{}, err
	}
	if res.Transport != "main-loopback-rpc" {
		_, _ = session.call(ctx, "Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": script})
	}
	return res, nil
}
