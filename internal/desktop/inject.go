package desktop

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"enhancedkimi/internal/webassets"
	"strings"
)

//go:embed assets/bootstrap.js
var bootstrapSource string

//go:embed assets/renderer.js
var rendererSource string

// providerEnhanceSource is concatenated in front of rendererSource, so the
// enhancement ships as one injected script without extra host plumbing. Keeping
// it in its own file is what lets a parallel change to the entry injection edit
// renderer.js without touching the provider page work.
//
//go:embed assets/provider_enhance.js
var providerEnhanceSource string

// rendererBundle wraps both scripts in a single function expression because
// bootstrap.js evaluates `'(' + options.rendererSource + ')(config)'`.
var rendererBundle = "function(options) {\n" + providerEnhanceSource + "\n" + rendererSource + "\nreturn installNativeRenderer(options);\n}"

type AttachOptions struct {
	// Profile narrows discovery to one isolated instance launched with this
	// --user-data-dir. An empty profile accepts the single running instance, which
	// is how a client started from the Dock or Start Menu is addressed.
	Profile      string
	CoreURL      string
	PairingToken string
	// DataDir identifies the enhancement core's data directory (used on Windows
	// to read launcher leases and shortcut metadata).
	DataDir string
	// KimiExecutable optionally pins the Windows Kimi Code.exe path.
	KimiExecutable string
	// DebugPort optionally overrides the loopback debugging port on Windows.
	DebugPort int
	// AllowDailyInstance is the user's explicit consent to attach to the client
	// they actually use. Without it, an instance with no isolated profile is
	// discovered but reported as需要授权 instead of being attached to.
	AllowDailyInstance bool
	// AppVersionOverride overrides the reported appVersion during isolated-host
	// contract acceptance (e.g. simulating an unknown future version "1.9.9").
	// It is ignored whenever Profile is empty.
	AppVersionOverride string
}

type AttachResult struct {
	Status      string `json:"status"`
	Transport   string `json:"transport"`
	Protocol    int    `json:"protocol"`
	AppVersion  string `json:"appVersion"`
	Electron    string `json:"electron"`
	HostPID     int    `json:"hostPid"`
	DOMRevision int    `json:"domRevision"`
	Verified    bool   `json:"verified"`
	VersionNote string `json:"versionNote,omitempty"`
	// Daily is true when the attached instance is the user's normal client rather
	// than a disposable isolated host.
	Daily             bool   `json:"daily"`
	Platform          string `json:"platform,omitempty"`
	DebugPort         int    `json:"debugPort,omitempty"`
	DebugPortWarning  string `json:"debugPortWarning,omitempty"`
	MainBridgeWarning string `json:"mainBridgeWarning,omitempty"`
}

// ErrConsentRequired is returned when the only running client is the user's daily
// instance and no explicit consent was given.
var ErrConsentRequired = errors.New("检测到日常客户端正在运行；接入会改变它的界面，需要你显式授权后才会继续")

const (
	darwinInspectorFuse = 12 * time.Second
	darwinDOMWaitBudget = 6 * time.Second
	darwinMountedGrace  = 1200 * time.Millisecond
)

func missingNativeCapability(capabilities map[string]bool) string {
	for _, capability := range []string{"ptShell", "ptBody", "sideColumn", "sideFooter"} {
		if !capabilities[capability] {
			return capability
		}
	}
	return ""
}

func domReadyForNativeAttach(capabilities map[string]bool, mountedElapsed time.Duration) bool {
	if missingNativeCapability(capabilities) == "" {
		return true
	}
	return capabilities["appMounted"] && mountedElapsed >= darwinMountedGrace
}

// resolveHost finds the instance to attach to and completes every identity check
// that can run before a signal is sent. The command line is returned separately:
// it is discovery input, not part of the identity that must stay identical.
func resolveHost(ctx context.Context, options AttachOptions) (ProcessInfo, string, error) {
	pid, err := FindMainPID(ctx, options.Profile)
	if err != nil {
		return ProcessInfo{}, "", err
	}
	info, err := ProcessIdentity(ctx, pid)
	if err != nil {
		return ProcessInfo{}, "", err
	}
	if info.Executable != KimiExecutable {
		return ProcessInfo{}, "", errors.New("目标进程不是 Kimi Code 主进程")
	}
	command, err := CommandLine(ctx, pid)
	if err != nil {
		return ProcessInfo{}, "", err
	}
	// A debug or test instance is never taken over automatically: its inspector
	// may be opened by whoever launched it, and the port check cannot tell them
	// apart.
	if IsDebugInstance(command) && options.Profile == "" {
		return ProcessInfo{}, "", errors.New("检测到调试或测试实例，增强不会自动接入")
	}
	if options.Profile == "" && !options.AllowDailyInstance {
		return ProcessInfo{}, "", ErrConsentRequired
	}
	if err = CheckInspectorFuse(KimiFramework); err != nil {
		return ProcessInfo{}, "", err
	}
	if err = VerifyLiveProcess(ctx, info); err != nil {
		return ProcessInfo{}, "", err
	}
	return info, command, nil
}

func AttachNative(ctx context.Context, options AttachOptions) (*AttachResult, error) {
	return attachNativePlatform(ctx, options)
}

func attachNativeDarwin(ctx context.Context, options AttachOptions) (*AttachResult, error) {
	if options.CoreURL == "" || len(options.PairingToken) < AttachTokenMinLength {
		return nil, errors.New("缺少增强核心地址或配对凭据")
	}
	info, _, err := resolveHost(ctx, options)
	if err != nil {
		return nil, err
	}
	pid := info.PID
	attachCtx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	cdp, err := Attach(attachCtx, pid)
	if err != nil {
		return nil, err
	}
	succeeded := false
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer closeCancel()
		if !succeeded {
			_, _ = cdp.Evaluate(closeCtx, `(async()=>{if(globalThis.__ekdNative?.cleanup){await globalThis.__ekdNative.cleanup();return true;}return false;})()`)
		}
		_ = cdp.Close(closeCtx)
	}()
	raw, err := cdp.Evaluate(attachCtx, `(()=>{const req=typeof require==='function'?require:process.mainModule.require.bind(process.mainModule);return{pid:process.pid,type:process.type,appVersion:req('electron').app.getVersion(),electron:process.versions.electron}})()`)
	if err != nil {
		return nil, err
	}
	var identity HostIdentity
	if err = json.Unmarshal(raw, &identity); err != nil {
		return nil, err
	}
	if identity.PID != pid {
		return nil, errors.New("宿主进程身份在附着时发生变化")
	}
	realAppVersion := strings.TrimSpace(identity.AppVersion)
	if options.Profile != "" && strings.TrimSpace(options.AppVersionOverride) != "" {
		identity.AppVersion = strings.TrimSpace(options.AppVersionOverride)
	}
	contract, err := identity.Contract()
	if err != nil {
		return nil, err
	}
	var capabilities map[string]bool
	domDeadline := time.Now().Add(darwinDOMWaitBudget)
	var mountedAt time.Time
	for {
		domRaw, domErr := cdp.Evaluate(attachCtx, `(async()=>{const req=typeof require==='function'?require:process.mainModule.require.bind(process.mainModule);const{webContents}=req('electron');const isMain=u=>{if(typeof u!=='string'||!u.startsWith('app://renderer/'))return false;if(/screenshot|browser-overlay|browser-control/.test(u))return false;const p=u.slice('app://renderer/'.length).split(/[?#]/)[0];return p===''||p==='index.html'||!p.endsWith('.html');};const wc=webContents.getAllWebContents().find(item=>!item.isDestroyed()&&isMain(item.getURL()));if(!wc)throw new Error('隔离主窗口未找到');return wc.executeJavaScript("(()=>({appMounted:!!(document.querySelector('#app')&&document.querySelector('#app').childElementCount>0),ptShell:!!document.querySelector('.pt-shell'),ptBody:!!document.querySelector('.pt-body'),sideColumn:!!document.querySelector('.side .col'),sideFooter:!!document.querySelector('.side-footer'),chat:!!document.querySelector('.chat'),composer:!!document.querySelector('.composer'),sideChat:[...document.querySelectorAll('button.sit-link')].some(button=>button.textContent.trim()==='侧边聊天')}))()")})()`)
		missing := ""
		if domErr == nil {
			if json.Unmarshal(domRaw, &capabilities) == nil {
				missing = missingNativeCapability(capabilities)
				if capabilities["appMounted"] && mountedAt.IsZero() {
					mountedAt = time.Now()
				}
				mountedElapsed := time.Duration(0)
				if !mountedAt.IsZero() {
					mountedElapsed = time.Since(mountedAt)
				}
				if domReadyForNativeAttach(capabilities, mountedElapsed) {
					break
				}
			}
		}
		if time.Now().After(domDeadline) {
			if domErr != nil {
				return nil, domErr
			}
			if capabilities["appMounted"] {
				break
			}
			return nil, fmt.Errorf("宿主 DOM 契约缺少 %s，增强保持停用", missing)
		}
		select {
		case <-attachCtx.Done():
			return nil, attachCtx.Err()
		case <-time.After(100 * time.Millisecond):
		}
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
	config := map[string]any{
		"coreURL": options.CoreURL, "pairingToken": options.PairingToken,
		"expectedApp": realAppVersion, "expectedElectron": contract.Electron,
		"reportedApp":  contract.AppVersion,
		"moduleSource": string(moduleSource), "cssSource": string(cssSource), "rendererSource": rendererBundle,
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	raw, err = cdp.Evaluate(attachCtx, "("+bootstrapSource+")("+string(encoded)+")")
	if err != nil {
		return nil, err
	}
	var result AttachResult
	if err = json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	if result.Status != "installed" || result.Transport != "main-loopback-rpc" || result.Protocol != 1 {
		return nil, errors.New("宿主没有确认正式 RPC 已挂载")
	}
	result.AppVersion = contract.AppVersion
	result.Electron = contract.Electron
	result.Verified = contract.Verified
	result.VersionNote = HostVersionNote(contract.AppVersion, contract.Electron)
	result.HostPID = pid
	result.DOMRevision = contract.DOMRevision
	result.Daily = options.Profile == ""
	result.Platform = "darwin"
	succeeded = true
	return &result, nil
}

// DiscoverHost reports the running instance without attaching to it, so the
// settings surface can show the current state and offer the consent prompt.
func DiscoverHost(ctx context.Context, profile string) (map[string]any, error) {
	return DiscoverHostWithDataDir(ctx, profile, "")
}

// DiscoverHostWithDataDir discovers the running host using optional dataDir
// context (required on Windows to correlate launcher-owned debug ports).
func DiscoverHostWithDataDir(ctx context.Context, profile, dataDir string) (map[string]any, error) {
	return discoverHostPlatform(ctx, profile, dataDir)
}

func discoverHostDarwin(ctx context.Context, profile string) (map[string]any, error) {
	pid, err := FindMainPID(ctx, profile)
	if err != nil {
		return map[string]any{"running": false, "reason": err.Error()}, nil
	}
	if _, err = ProcessIdentity(ctx, pid); err != nil {
		return map[string]any{"running": false, "reason": err.Error()}, nil
	}
	command, _ := CommandLine(ctx, pid)
	debug := IsDebugInstance(command)
	fuse := "ready"
	if err := CheckInspectorFuse(KimiFramework); err != nil {
		if errors.Is(err, ErrInspectorDisabled) {
			fuse = "signal-disabled"
		} else {
			fuse = "unknown-layout"
		}
	}
	inspectorBusy := inspectorOpen(ctx)
	return map[string]any{
		"running":         true,
		"pid":             pid,
		"profile":         profile,
		"isolated":        profile != "",
		"debugInstance":   debug,
		"dailyInstance":   profile == "",
		"fuse":            fuse,
		"inspectorBusy":   inspectorBusy,
		"consentRequired": profile == "",
	}, nil
}
