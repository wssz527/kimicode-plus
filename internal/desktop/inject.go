package desktop

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"enhancedkimi/internal/webassets"
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
	// CoreVersion names the version of the enhancement core performing this attach
	// so bootstrap.js can distinguish a same-version core restart from an upgrade.
	CoreVersion string
	SkipCurrent bool
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
	attachKey         string
	hostStarted       string
	stateRecorded     bool
}

// ErrConsentRequired is returned when the only running client is the user's daily
// instance and no explicit consent was given.
var ErrConsentRequired = errors.New("检测到日常客户端正在运行；接入会改变它的界面，需要你显式授权后才会继续")

const (
	darwinInspectorFuse = 12 * time.Second
	darwinDOMWaitBudget = 6 * time.Second
	darwinMountedGrace  = 1200 * time.Millisecond
)

// bootstrapConfig assembles the payload the host receives. The macOS path learns
// the core's runtime record so it can renew its session; without a data
// directory the record path is unknown and is omitted rather than sent empty.
func bootstrapConfig(options AttachOptions, expectedApp, reportedApp, electron, moduleSource, cssSource string) (map[string]any, error) {
	config := map[string]any{
		"coreURL": options.CoreURL, "pairingToken": options.PairingToken,
		"expectedApp": expectedApp, "expectedElectron": electron,
		"reportedApp":  reportedApp,
		"moduleSource": moduleSource, "cssSource": cssSource, "rendererSource": rendererBundle,
	}
	if options.CoreVersion != "" {
		config["coreVersion"] = options.CoreVersion
	}
	if options.DataDir != "" {
		runtimePath, err := filepath.Abs(filepath.Join(options.DataDir, "runtime.json"))
		if err != nil {
			return nil, err
		}
		config["runtimePath"] = runtimePath
	}
	return config, nil
}

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

func resolveHost(ctx context.Context, options AttachOptions) (ProcessInfo, error) {
	pid, err := FindMainPID(ctx, options.Profile)
	if err != nil {
		return ProcessInfo{}, err
	}
	info, err := ProcessIdentity(ctx, pid)
	if err != nil {
		return ProcessInfo{}, err
	}
	if !slices.Contains(kimiExecutables(), info.Executable) {
		return ProcessInfo{}, errors.New("目标进程不是 Kimi Code 主进程")
	}
	command, err := CommandLine(ctx, pid)
	if err != nil {
		return ProcessInfo{}, err
	}
	if IsDebugInstance(command) && options.Profile == "" {
		return ProcessInfo{}, errors.New("检测到调试或测试实例，增强不会自动接入")
	}
	if options.Profile == "" && !options.AllowDailyInstance {
		return ProcessInfo{}, ErrConsentRequired
	}
	return info, nil
}

func AttachNative(ctx context.Context, options AttachOptions) (*AttachResult, error) {
	return attachNativePlatform(ctx, options)
}

type bridgeTornDownError struct {
	err error
}

func (e *bridgeTornDownError) Error() string {
	return e.err.Error()
}

func (e *bridgeTornDownError) Unwrap() error {
	return e.err
}

func markBridgeTornDown(err error) error {
	if err == nil || isBridgeTornDown(err) {
		return err
	}
	return &bridgeTornDownError{err: err}
}

func isBridgeTornDown(err error) bool {
	var target *bridgeTornDownError
	return errors.As(err, &target)
}

func MarkBridgeTornDown(err error) error {
	return markBridgeTornDown(err)
}

func IsBridgeTornDown(err error) bool {
	return isBridgeTornDown(err)
}

type attachStateRecordedError struct {
	err error
}

func (e *attachStateRecordedError) Error() string {
	return e.err.Error()
}

func (e *attachStateRecordedError) Unwrap() error {
	return e.err
}

func markAttachStateRecorded(err error) error {
	if err == nil || isAttachStateRecorded(err) {
		return err
	}
	return &attachStateRecordedError{err: err}
}

func isAttachStateRecorded(err error) bool {
	var target *attachStateRecordedError
	return errors.As(err, &target)
}

func MarkAttachStateRecorded(err error) error {
	return markAttachStateRecorded(err)
}

func IsAttachStateRecorded(err error) bool {
	return isAttachStateRecorded(err)
}

func MarkAttachStateRecordedResult(result *AttachResult) *AttachResult {
	if result != nil {
		result.stateRecorded = true
	}
	return result
}

func IsAttachStateRecordedResult(result *AttachResult) bool {
	return result != nil && result.stateRecorded
}

type attachStatePersistFailedError struct {
	err error
}

func (e *attachStatePersistFailedError) Error() string {
	return e.err.Error()
}

func (e *attachStatePersistFailedError) Unwrap() error {
	return e.err
}

func markAttachPersistFailed(err error) error {
	if err == nil || isAttachPersistFailed(err) {
		return err
	}
	return &attachStatePersistFailedError{err: err}
}

func isAttachPersistFailed(err error) bool {
	var target *attachStatePersistFailedError
	return errors.As(err, &target)
}

func MarkAttachPersistFailed(err error) error {
	return markAttachPersistFailed(err)
}

func IsAttachPersistFailed(err error) bool {
	return isAttachPersistFailed(err)
}

func commitAttachOutcomeUnlocked(dataDir string, result *AttachResult, retErr error) (*AttachResult, error) {
	return commitAttachOutcomeWithPreviousUnlocked(dataDir, nil, result, retErr)
}

func commitAttachOutcomeWithPreviousUnlocked(dataDir string, prevInstalled map[string]any, result *AttachResult, retErr error) (*AttachResult, error) {
	if dataDir == "" {
		return result, retErr
	}
	if retErr != nil {
		if !isAttachStateRecorded(retErr) && !isAttachPersistFailed(retErr) {
			var recordErr error
			if !isBridgeTornDown(retErr) {
				currentCore := corePID(dataDir)
				if shouldPreserveInstalledAttach(dataDir, currentCore) {
					recordErr = nil
				} else if prevInstalled != nil && shouldPreserveInstalledAttachState(prevInstalled, currentCore) {
					recordErr = writeAttachState(dataDir, prevInstalled)
				} else {
					recordErr = recordAttachUnlocked(dataDir, retErr)
				}
			} else {
				recordErr = recordAttachUnlocked(dataDir, retErr)
			}
			if recordErr != nil {
				persistErr := fmt.Errorf("写入接入状态文件失败：%w", recordErr)
				if removeErr := os.Remove(filepath.Join(dataDir, "attach-state.json")); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
					persistErr = errors.Join(persistErr, fmt.Errorf("移除旧接入状态文件失败：%w", removeErr))
				}
				retErr = markAttachPersistFailed(errors.Join(retErr, persistErr))
			} else {
				retErr = markAttachStateRecorded(retErr)
			}
		}
		return nil, retErr
	}
	if result != nil && !result.stateRecorded {
		if recordErr := recordAttachUnlocked(dataDir, result); recordErr != nil {
			return nil, markAttachPersistFailed(fmt.Errorf("写入接入状态文件失败：%w", recordErr))
		}
		result.stateRecorded = true
	}
	return result, nil
}

func attachStateMatchesCurrentCore(dataDir string, state map[string]any, key string) bool {
	if dataDir == "" || state == nil || state["status"] != "installed" || key == "" || state["attachKey"] != key {
		return false
	}
	recordedCore, ok := attachStatePID(state["corePid"])
	if !ok {
		return false
	}
	runtimePID, err := readCoreRuntimePID(dataDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return recordedCore == os.Getpid()
		}
		return false
	}
	return recordedCore == runtimePID
}

var (
	coreProcessAlive      = defaultCoreProcessAlive
	verifyLiveCoreForSkip = defaultVerifyLiveCoreForSkip
)

func defaultCoreProcessAlive(ctx context.Context, pid int) bool {
	if pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	if runtime.GOOS == "darwin" {
		info, err := ProcessIdentity(ctx, pid)
		return err == nil && info.PID == pid
	}
	return false
}

func isStrictLoopbackHTTPURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Port() == "" ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return false
	}
	host := parsed.Hostname()
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func defaultVerifyLiveCoreForSkip(ctx context.Context, corePID int, coreURL, pairingToken string) bool {
	if corePID <= 0 {
		return false
	}
	cleanURL := strings.TrimRight(strings.TrimSpace(coreURL), "/")
	cleanToken := strings.TrimSpace(pairingToken)
	if !isStrictLoopbackHTTPURL(cleanURL) || len(cleanToken) < AttachTokenMinLength {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if corePID != os.Getpid() && !coreProcessAlive(probeCtx, corePID) {
		return false
	}
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, cleanURL+"/enhance/v1/health", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+cleanToken)
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return false
	}
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		var payload struct {
			Status string `json:"status"`
		}
		if json.Unmarshal([]byte(trimmed), &payload) == nil && strings.EqualFold(strings.TrimSpace(payload.Status), "error") {
			return false
		}
	}
	return true
}

func attachStateMatchesLiveCore(ctx context.Context, dataDir string, state map[string]any, key string, options AttachOptions) bool {
	if !attachStateMatchesCurrentCore(dataDir, state, key) {
		return false
	}
	recordedCore, ok := attachStatePID(state["corePid"])
	if !ok {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return verifyLiveCoreForSkip(ctx, recordedCore, options.CoreURL, options.PairingToken)
}

type darwinCDPSession interface {
	Evaluate(ctx context.Context, expression string) (json.RawMessage, error)
	Close(ctx context.Context) error
}

type darwinAttachDeps struct {
	checkFuse     func(framework string) error
	verifyProcess func(ctx context.Context, info ProcessInfo) error
	attachCDP     func(ctx context.Context, pid int) (darwinCDPSession, error)
}

func attachNativeDarwin(ctx context.Context, options AttachOptions) (*AttachResult, error) {
	if options.CoreURL == "" || len(options.PairingToken) < AttachTokenMinLength {
		return nil, errors.New("缺少增强核心地址或配对凭据")
	}
	info, err := resolveHost(ctx, options)
	if err != nil {
		return nil, err
	}
	return attachDarwinHost(ctx, info, options, darwinAttachDeps{
		checkFuse:     CheckInspectorFuse,
		verifyProcess: VerifyLiveProcess,
		attachCDP: func(attachCtx context.Context, pid int) (darwinCDPSession, error) {
			return Attach(attachCtx, pid)
		},
	})
}

func attachDarwinHost(ctx context.Context, info ProcessInfo, options AttachOptions, deps darwinAttachDeps) (result *AttachResult, retErr error) {
	gate, err := acquireHostAttachLock(ctx, options.DataDir)
	if err != nil {
		return nil, err
	}
	if gate != nil {
		defer gate.Close()
	}
	var prevInstalled map[string]any
	if options.DataDir != "" {
		defer func() {
			result, retErr = commitAttachOutcomeWithPreviousUnlocked(options.DataDir, prevInstalled, result, retErr)
		}()
	}
	checkFuse := deps.checkFuse
	if checkFuse == nil {
		checkFuse = CheckInspectorFuse
	}
	if err := checkFuse(kimiFrameworkFor(info.Executable)); err != nil {
		return nil, err
	}
	verifyProcess := deps.verifyProcess
	if verifyProcess == nil {
		verifyProcess = VerifyLiveProcess
	}
	if err := verifyProcess(ctx, info); err != nil {
		return nil, err
	}
	key := darwinAttachKey(info, options)
	if options.SkipCurrent && attachStateMatchesLiveCore(ctx, options.DataDir, ReadAttachState(options.DataDir), key, options) {
		return nil, nil
	}
	prevInstalled = readInstalledAttachState(options.DataDir)
	if invalidateErr := invalidateInstalledAttachBeforeInjectUnlocked(options.DataDir); invalidateErr != nil {
		return nil, markAttachPersistFailed(fmt.Errorf("无法更新接入状态文件，已中止接入以免破坏现有增强桥：%w", invalidateErr))
	}
	attachCtx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	attachCDP := deps.attachCDP
	if attachCDP == nil {
		attachCDP = func(c context.Context, pid int) (darwinCDPSession, error) {
			return Attach(c, pid)
		}
	}
	cdp, err := attachCDP(attachCtx, info.PID)
	if err != nil {
		return nil, err
	}
	return runDarwinCDPAttachWithPrevious(attachCtx, cdp, info, options, key, prevInstalled)
}

func runDarwinCDPAttach(attachCtx context.Context, cdp darwinCDPSession, info ProcessInfo, options AttachOptions, key string) (result *AttachResult, retErr error) {
	return runDarwinCDPAttachWithPrevious(attachCtx, cdp, info, options, key, nil)
}

func runDarwinCDPAttachWithPrevious(attachCtx context.Context, cdp darwinCDPSession, info ProcessInfo, options AttachOptions, key string, prevInstalled map[string]any) (result *AttachResult, retErr error) {
	pid := info.PID
	succeeded := false
	bridgeTouched := false
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer closeCancel()
		if !succeeded && bridgeTouched {
			_, _ = cdp.Evaluate(closeCtx, `(async()=>{if(globalThis.__ekdNative?.cleanup){await globalThis.__ekdNative.cleanup();return true;}return false;})()`)
		}
		// A failed detach leaves 9229 listening on the client. That is a
		// debugging endpoint nobody asked to keep, so it is reported instead of
		// swallowed: the result must not claim success while the port is open.
		if closeErr := cdp.Close(closeCtx); closeErr != nil {
			result = nil
			retErr = errors.Join(retErr, fmt.Errorf("未能确认关闭宿主临时调试端口，请正常退出并重新打开 Kimi Code：%w", closeErr))
		}
		if retErr != nil && bridgeTouched {
			retErr = markBridgeTornDown(retErr)
		}
		if retErr != nil || (succeeded && result != nil) {
			result, retErr = commitAttachOutcomeWithPreviousUnlocked(options.DataDir, prevInstalled, result, retErr)
		}
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
	config, err := bootstrapConfig(options, realAppVersion, contract.AppVersion, contract.Electron, string(moduleSource), string(cssSource))
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	bridgeTouched = true
	raw, err = cdp.Evaluate(attachCtx, "("+bootstrapSource+")("+string(encoded)+")")
	if err != nil {
		return nil, err
	}
	var attached AttachResult
	if err = json.Unmarshal(raw, &attached); err != nil {
		return nil, err
	}
	if attached.Status != "installed" || attached.Transport != "main-loopback-rpc" || attached.Protocol != 1 {
		return nil, errors.New("宿主没有确认正式 RPC 已挂载")
	}
	attached.AppVersion = contract.AppVersion
	attached.Electron = contract.Electron
	attached.Verified = contract.Verified
	attached.VersionNote = HostVersionNote(contract.AppVersion, contract.Electron)
	attached.HostPID = pid
	attached.DOMRevision = contract.DOMRevision
	attached.Daily = options.Profile == ""
	attached.Platform = "darwin"
	attached.attachKey = key
	attached.hostStarted = info.Started
	succeeded = true
	return &attached, nil
}

var (
	injectBundleHashOnce sync.Once
	injectBundleHashVal  string
)

func injectBundleHash() string {
	injectBundleHashOnce.Do(func() {
		h := sha256.New()
		h.Write([]byte(bootstrapSource))
		h.Write([]byte{0})
		h.Write([]byte(rendererBundle))
		h.Write([]byte{0})
		assets := webassets.FS()
		for _, name := range []string{"host/enhancement.js", "host/enhancement.css"} {
			if data, err := fs.ReadFile(assets, name); err == nil {
				h.Write(data)
			}
			h.Write([]byte{0})
		}
		injectBundleHashVal = fmt.Sprintf("%x", h.Sum(nil))
	})
	return injectBundleHashVal
}

func darwinAttachKey(info ProcessInfo, options AttachOptions) string {
	appOverride := ""
	if options.Profile != "" {
		appOverride = strings.TrimSpace(options.AppVersionOverride)
	}
	identity := fmt.Sprintf(
		"%d\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		info.PID,
		info.UID,
		info.Started,
		info.Executable,
		options.CoreURL,
		options.PairingToken,
		options.CoreVersion,
		appOverride,
		injectBundleHash(),
	)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))
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
	info, err := ProcessIdentity(ctx, pid)
	if err != nil {
		return map[string]any{"running": false, "reason": err.Error()}, nil
	}
	command, _ := CommandLine(ctx, pid)
	debug := IsDebugInstance(command)
	fuse := "ready"
	if err := CheckInspectorFuse(kimiFrameworkFor(info.Executable)); err != nil {
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
