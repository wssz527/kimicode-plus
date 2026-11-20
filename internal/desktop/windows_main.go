package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"enhancedkimi/internal/webassets"

	"github.com/coder/websocket"
)

var windowsNodeTargetPath = regexp.MustCompile(`^/[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

func windowsNodeTarget(targets []WindowsCDPTarget, port int) (string, error) {
	if len(targets) == 1 && targets[0].Type == "node" {
		u, err := url.Parse(targets[0].WebSocketDebuggerURL)
		if err == nil && u.Scheme == "ws" && u.Host == "127.0.0.1:"+strconv.Itoa(port) &&
			u.User == nil && windowsNodeTargetPath.MatchString(u.EscapedPath()) && u.RawQuery == "" && u.Fragment == "" {
			return u.String(), nil
		}
	}
	return "", errors.New("Windows 主进程没有提供可验证的本机 Node 调试目标")
}

func windowsInspectorClosed(port int) bool {
	conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 250*time.Millisecond)
	if err != nil {
		return true
	}
	conn.Close()
	return false
}

func attachWindowsMain(ctx context.Context, opts WindowsCDPAttachOptions, dataDir string, port int, verify func(context.Context) error) (attached *AttachResult, attachErr error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if err := verify(ctx); err != nil {
		return nil, err
	}
	var targets []WindowsCDPTarget
	if err := fetchWindowsCDPJSON(ctx, port, "/json/list", &targets); err != nil {
		return nil, err
	}
	endpoint, err := windowsNodeTarget(targets, port)
	if err != nil {
		return nil, err
	}
	if err := verify(ctx); err != nil {
		return nil, err
	}
	client := &http.Client{Transport: &http.Transport{}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	conn, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(32 << 20)
	cdp := &CDP{pid: opts.HostPID, conn: conn, pending: map[int]chan cdpResponse{}, closed: make(chan struct{})}
	go cdp.read()
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer closeCancel()
		_, _ = cdp.Evaluate(closeCtx, closeInspectorExpression(opts.HostPID))
		_ = conn.CloseNow()
		for !windowsInspectorClosed(port) {
			select {
			case <-closeCtx.Done():
				attached = nil
				attachErr = errors.Join(attachErr, errors.New("Windows 临时主进程调试端口未关闭"))
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	if _, err := cdp.Evaluate(ctx, inspectorDeadlineExpression(opts.HostPID)); err != nil {
		return nil, err
	}
	var identity struct {
		AppVersion string `json:"appVersion"`
		Electron   string `json:"electron"`
		Ready      bool   `json:"ready"`
	}
	for {
		raw, err := cdp.Evaluate(ctx, `(()=>{const load=typeof require==='function'?require:process.mainModule.require.bind(process.mainModule);const{app,webContents}=load('electron');const isMain=url=>{if(!url.startsWith('app://renderer/')||/screenshot|browser-overlay|browser-control/.test(url))return false;const p=url.slice('app://renderer/'.length).split(/[?#]/)[0];return p==='index.html'||!p.endsWith('.html');};return{appVersion:app.getVersion(),electron:process.versions.electron,ready:webContents.getAllWebContents().some(w=>!w.isDestroyed()&&isMain(w.getURL())&&!w.isLoadingMainFrame())};})()`)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &identity); err != nil {
			return nil, err
		}
		if identity.Ready {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	assets := webassets.FS()
	module, err := fs.ReadFile(assets, "host/enhancement.js")
	if err != nil {
		return nil, err
	}
	css, err := fs.ReadFile(assets, "host/enhancement.css")
	if err != nil {
		return nil, err
	}
	runtimePath, err := filepath.Abs(filepath.Join(dataDir, "runtime.json"))
	if err != nil {
		return nil, err
	}
	appVersion := identity.AppVersion
	if opts.Profile != "" && opts.AppVersionOverride != "" {
		appVersion = opts.AppVersionOverride
	}
	config, err := json.Marshal(map[string]any{
		"coreURL": opts.CoreURL, "pairingToken": opts.PairingToken, "runtimePath": runtimePath,
		"expectedApp": identity.AppVersion, "expectedElectron": identity.Electron, "reportedApp": appVersion,
		"moduleSource": string(module), "cssSource": string(css), "rendererSource": rendererBundle,
	})
	if err != nil {
		return nil, err
	}
	raw, err := cdp.Evaluate(ctx, "("+bootstrapSource+")("+string(config)+")")
	if err != nil {
		return nil, fmt.Errorf("Windows 主进程增强桥接入失败：%w", err)
	}
	var result AttachResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	if opts.AuditClients != nil {
		if alerts, err := opts.AuditClients(ctx, opts.DebugPort, opts.HostPID); err == nil {
			result.DebugPortWarning = FormatDebugClientAlerts(alerts)
		}
	}
	return windowsMainResult(result, opts)
}

func windowsMainResult(result AttachResult, opts WindowsCDPAttachOptions) (*AttachResult, error) {
	if result.Status != "installed" || result.Transport != "main-loopback-rpc" || result.Protocol != 1 {
		return nil, errors.New("Windows 宿主未确认主进程桥已安装")
	}
	if opts.Profile != "" && opts.AppVersionOverride != "" {
		result.AppVersion = opts.AppVersionOverride
	}
	result.Verified = IsVerifiedHost(result.AppVersion, result.Electron)
	result.VersionNote = HostVersionNote(result.AppVersion, result.Electron)
	result.HostPID = opts.HostPID
	result.Platform = "windows"
	result.Daily = opts.Daily
	result.DebugPort = opts.DebugPort
	result.DOMRevision = 1
	return &result, nil
}
