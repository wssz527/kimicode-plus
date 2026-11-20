package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CoreRuntimeRecord mirrors the runtime.json written by enhanced-kimi core.
type CoreRuntimeRecord struct {
	URL          string `json:"url"`
	PairingToken string `json:"pairingToken"`
	GatewayURL   string `json:"gatewayUrl"`
	PID          int    `json:"pid"`
	Version      string `json:"version"`
}

// WindowsLaunchOptions configures `enhanced-kimi launch`.
type WindowsLaunchOptions struct {
	DataDir            string
	KimiHome           string
	KimiExecutable     string
	Executable         string
	Profile            string
	DebugPort          int
	AllowDailyInstance bool
	AppVersionOverride string
}

// WindowsLaunchResult summarizes one execution of the Windows launcher.
type WindowsLaunchResult struct {
	CoreReused bool          `json:"coreReused"`
	CoreURL    string        `json:"coreUrl"`
	KimiReused bool          `json:"kimiReused"`
	KimiPID    int           `json:"kimiPid"`
	DebugPort  int           `json:"debugPort"`
	Attach     *AttachResult `json:"attach,omitempty"`
}

// WindowsLauncherDeps holds the OS operations required by RunWindowsLauncher so
// the full launcher workflow is unit-testable on non-Windows hosts as well.
type WindowsLauncherDeps struct {
	CurrentSID          func(ctx context.Context) (string, error)
	ListProcesses       func(ctx context.Context) ([]WindowsProcessInfo, error)
	ListListeners       func(ctx context.Context, port int) ([]WindowsPortListener, error)
	ListConnections     func(ctx context.Context, port int) ([]WindowsTCPConnection, error)
	VerifyBinary        func(executable string) (WindowsBinaryInfo, error)
	SpawnDetached       func(executable string, args []string) (int, error)
	PickPort            func() (int, error)
	AttachCDP           func(ctx context.Context, opts WindowsCDPAttachOptions) (*AttachResult, error)
	AttachRunning       func(context.Context, AttachOptions, WindowsProcessInfo) (*AttachResult, error)
	SpawnInspectable    func(executable string, args []string) (*os.Process, error)
	CheckCoreHealth     func(ctx context.Context, coreURL string) bool
	SelfPID             func() int
	CoreReadyTimeout    time.Duration
	ProcessReadyTimeout time.Duration
	PollInterval        time.Duration
}

func readLoopbackCoreRuntime(dataDir string) (*CoreRuntimeRecord, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, "runtime.json"))
	if err != nil {
		return nil, err
	}
	var rec CoreRuntimeRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, err
	}
	u, err := url.Parse(rec.URL)
	if err != nil || u.Scheme != "http" || !isLoopbackIPString(u.Hostname()) {
		return nil, errors.New("核心 runtime.json 中的地址不是本机环回地址")
	}
	if len(rec.PairingToken) < AttachTokenMinLength {
		return nil, errors.New("核心 runtime.json 缺少有效的配对凭据")
	}
	return &rec, nil
}

func defaultCheckCoreHealth(ctx context.Context, coreURL string) bool {
	u, err := url.Parse(coreURL)
	if err != nil || u.Scheme != "http" || !isLoopbackIPString(u.Hostname()) {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(coreURL, "/")+"/health", nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// RunWindowsLauncher ensures Enhanced Kimi Core is running (reusing an active
// instance if healthy), starts or reuses Kimi Code on a loopback debugging port,
// verifies SID/executable/start-time/PE/fuse/port-owner, attaches via CDP, and
// persists attach state.
func RunWindowsLauncher(ctx context.Context, opts WindowsLaunchOptions, deps WindowsLauncherDeps) (*WindowsLaunchResult, error) {
	if strings.TrimSpace(opts.DataDir) == "" {
		return nil, errors.New("Windows 启动器缺少 --data-dir")
	}
	if strings.TrimSpace(opts.KimiExecutable) == "" {
		return nil, errors.New("Windows 启动器缺少 Kimi Code.exe 路径")
	}
	if opts.Profile == "" && !opts.AllowDailyInstance {
		return nil, ErrConsentRequired
	}
	if deps.CurrentSID == nil || deps.ListProcesses == nil || deps.ListListeners == nil || deps.SpawnDetached == nil {
		return nil, errors.New("Windows 启动器缺少平台依赖")
	}
	if deps.VerifyBinary == nil {
		deps.VerifyBinary = VerifyWindowsBinaryFile
	}
	if deps.PickPort == nil {
		deps.PickPort = PickFreeLoopbackPort
	}
	if deps.AttachCDP == nil {
		deps.AttachCDP = AttachWindowsCDP
	}
	if deps.CheckCoreHealth == nil {
		deps.CheckCoreHealth = defaultCheckCoreHealth
	}
	if deps.SelfPID == nil {
		deps.SelfPID = os.Getpid
	}
	if deps.CoreReadyTimeout <= 0 {
		deps.CoreReadyTimeout = 20 * time.Second
	}
	if deps.ProcessReadyTimeout <= 0 {
		deps.ProcessReadyTimeout = 25 * time.Second
	}
	if deps.PollInterval <= 0 {
		deps.PollInterval = 150 * time.Millisecond
	}

	// Step 1: Verify Kimi Code.exe PE header + Electron + fuse before spawning anything.
	binary, err := deps.VerifyBinary(opts.KimiExecutable)
	if err != nil && !errors.Is(err, ErrInspectorDisabled) {
		return nil, err
	}

	// Step 2: Ensure Enhanced Kimi Core is running; reuse existing healthy core.
	coreReused := false
	coreRec, err := readLoopbackCoreRuntime(opts.DataDir)
	if err == nil && deps.CheckCoreHealth(ctx, coreRec.URL) {
		coreReused = true
	} else {
		if strings.TrimSpace(opts.Executable) == "" {
			return nil, errors.New("核心未运行且缺少增强核心可执行文件路径，无法自动拉起")
		}
		coreArgs := []string{"--data-dir", opts.DataDir}
		effectiveHome := strings.TrimSpace(opts.KimiHome)
		if effectiveHome == "" && opts.Profile == "" {
			if envHome := strings.TrimSpace(os.Getenv("KIMI_CODE_HOME")); envHome != "" {
				effectiveHome = envHome
			} else if homeDir, hErr := os.UserHomeDir(); hErr == nil && homeDir != "" {
				effectiveHome = filepath.Join(homeDir, ".kimi-code")
			}
		}
		if effectiveHome != "" {
			coreArgs = append(coreArgs, "--kimi-home", effectiveHome)
		}
		if opts.Profile != "" {
			coreArgs = append(coreArgs, "--host-profile", opts.Profile)
		}
		if _, err := deps.SpawnDetached(opts.Executable, coreArgs); err != nil {
			return nil, fmt.Errorf("无法拉起增强核心：%w", err)
		}
		deadline := time.Now().Add(deps.CoreReadyTimeout)
		for {
			if rec, rErr := readLoopbackCoreRuntime(opts.DataDir); rErr == nil && deps.CheckCoreHealth(ctx, rec.URL) {
				coreRec = rec
				break
			}
			if time.Now().After(deadline) {
				return nil, errors.New("等待增强核心启动超时")
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(deps.PollInterval):
			}
		}
	}

	// Step 3: Check whether a Kimi Code instance is already running for this user.
	sid, err := deps.CurrentSID(ctx)
	if err != nil {
		return nil, fmt.Errorf("无法读取当前 Windows 用户 SID：%w", err)
	}
	existingLease := ReadWindowsLauncherState(opts.DataDir)
	rawProcs, err := deps.ListProcesses(ctx)
	if err != nil {
		return nil, fmt.Errorf("无法枚举运行中的 Kimi Code 进程：%w", err)
	}
	matches, err := SelectWindowsMainProcesses(sid, rawProcs, opts.KimiExecutable, opts.Profile)
	if err != nil {
		return nil, err
	}

	var targetProc WindowsProcessInfo
	var debugPort, inspectorPort int
	var spawned *os.Process
	var inspectorClosed bool
	defer func() {
		if spawned != nil {
			if !inspectorClosed {
				_ = spawned.Kill()
			}
			_ = spawned.Release()
		}
	}()
	kimiReused := false

	if len(matches) > 1 {
		return nil, fmt.Errorf("检测到 %d 个 Kimi Code 主进程，无法确定接入目标", len(matches))
	}
	if len(matches) == 1 {
		candidate := matches[0]
		if opts.Profile == "" && IsWindowsDebugInstance(candidate, existingLease) {
			return nil, ErrWindowsDebugInstance
		}
		port, hasPort := extractDebugPortArg(SplitWindowsCommandLine(candidate.CommandLine))
		if !hasPort {
			if deps.AttachRunning == nil {
				return nil, errors.New("当前客户端无法在运行中接入，请从 KimiCode+ 入口重新打开 Kimi Code")
			}
			result, err := deps.AttachRunning(ctx, AttachOptions{
				DataDir: opts.DataDir, Profile: opts.Profile, CoreURL: coreRec.URL, PairingToken: coreRec.PairingToken,
				AllowDailyInstance: opts.AllowDailyInstance, AppVersionOverride: opts.AppVersionOverride,
			}, candidate)
			if err != nil {
				_ = RecordAttach(opts.DataDir, err)
				return nil, err
			}
			_ = RecordAttach(opts.DataDir, result)
			return &WindowsLaunchResult{CoreReused: coreReused, CoreURL: coreRec.URL, KimiReused: true, KimiPID: candidate.PID, Attach: result}, nil
		}
		targetProc = candidate
		debugPort = port
		kimiReused = true
	} else {
		port := opts.DebugPort
		if port <= 0 {
			port, err = deps.PickPort()
			if err != nil {
				return nil, err
			}
		}
		kimiArgs := []string{
			fmt.Sprintf("--remote-debugging-port=%d", port),
			"--remote-debugging-address=127.0.0.1",
		}
		if opts.Profile != "" {
			kimiArgs = append(kimiArgs, "--user-data-dir="+opts.Profile)
		}
		if deps.SpawnInspectable != nil && binary.supportsInspector() {
			inspectorPort, err = deps.PickPort()
			if err != nil {
				return nil, err
			}
			if inspectorPort == port {
				return nil, errors.New("无法为 Windows 主进程和渲染进程分配独立端口")
			}
			kimiArgs = append(kimiArgs, fmt.Sprintf("--inspect=127.0.0.1:%d", inspectorPort))
			spawned, err = deps.SpawnInspectable(opts.KimiExecutable, kimiArgs)
		} else {
			_, err = deps.SpawnDetached(opts.KimiExecutable, kimiArgs)
		}
		if err != nil {
			return nil, fmt.Errorf("无法启动 Kimi Code：%w", err)
		}
		debugPort = port
		deadline := time.Now().Add(deps.ProcessReadyTimeout)
		for {
			procs, listErr := deps.ListProcesses(ctx)
			if listErr == nil {
				found, selErr := SelectWindowsMainProcesses(sid, procs, opts.KimiExecutable, opts.Profile)
				if selErr == nil && len(found) == 1 && (spawned == nil || spawned.Pid == found[0].PID) {
					listeners, lErr := deps.ListListeners(ctx, debugPort)
					if lErr == nil && VerifyWindowsDebugPortOwner(listeners, debugPort, found[0].PID) == nil {
						targetProc = found[0]
						break
					}
				}
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("等待 Kimi Code 在 127.0.0.1:%d 就绪超时", debugPort)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(deps.PollInterval):
			}
		}
		lease := WindowsLauncherState{
			PID:           targetProc.PID,
			DebugPort:     debugPort,
			InspectorPort: inspectorPort,
			OwnerSID:      targetProc.OwnerSID,
			Executable:    targetProc.Executable,
			Started:       targetProc.Started,
			LaunchedByUs:  true,
			LaunchedAt:    time.Now().UTC().Format(time.RFC3339),
		}
		if err := WriteWindowsLauncherState(opts.DataDir, lease); err != nil {
			return nil, err
		}
	}

	// Step 4: Re-verify process identity and loopback port ownership immediately before attaching.
	recheckProcs, err := deps.ListProcesses(ctx)
	if err != nil {
		return nil, err
	}
	recheckMatches, err := SelectWindowsMainProcesses(sid, recheckProcs, opts.KimiExecutable, opts.Profile)
	if err != nil || len(recheckMatches) != 1 {
		return nil, errors.New("Windows 宿主进程在接入前发生变化或退出")
	}
	if err := VerifyWindowsProcessIdentity(targetProc, recheckMatches[0]); err != nil {
		return nil, err
	}
	listeners, err := deps.ListListeners(ctx, debugPort)
	if err != nil {
		return nil, fmt.Errorf("无法检查调试端口监听属主：%w", err)
	}
	if err := VerifyWindowsDebugPortOwner(listeners, debugPort, targetProc.PID); err != nil {
		return nil, err
	}

	// Step 5: Attach the native bridge, with Chromium CDP as the compatibility path.
	var auditFn func(ctx context.Context, port, hostPID int) ([]WindowsDebugClientAlert, error)
	if deps.ListConnections != nil {
		auditFn = func(auditCtx context.Context, port, hostPID int) ([]WindowsDebugClientAlert, error) {
			conns, cErr := deps.ListConnections(auditCtx, port)
			if cErr != nil {
				return nil, cErr
			}
			return DetectForeignDebugClients(conns, port, hostPID, deps.SelfPID()), nil
		}
	}

	attachOptions := WindowsCDPAttachOptions{
		DebugPort:          debugPort,
		HostPID:            targetProc.PID,
		CoreURL:            coreRec.URL,
		PairingToken:       coreRec.PairingToken,
		Profile:            opts.Profile,
		Daily:              opts.Profile == "",
		AppVersionOverride: opts.AppVersionOverride,
		AuditClients:       auditFn,
	}
	var attachResult *AttachResult
	var attachErr error
	var mainWarning string
	if inspectorPort != 0 {
		attachResult, attachErr = attachWindowsMain(ctx, attachOptions, opts.DataDir, inspectorPort, func(verifyCtx context.Context) error {
			listeners, err := deps.ListListeners(verifyCtx, inspectorPort)
			if err != nil {
				return err
			}
			return VerifyWindowsDebugPortOwner(listeners, inspectorPort, targetProc.PID)
		})
		inspectorClosed = windowsInspectorClosed(inspectorPort)
		if !inspectorClosed {
			err := errors.Join(attachErr, errors.New("Windows 临时主进程调试端口未关闭，已停止本次启动"))
			_ = RecordAttach(opts.DataDir, err)
			return nil, err
		}
		if attachErr != nil {
			mainWarning = attachErr.Error()
		}
	}
	if attachResult == nil {
		attachResult, attachErr = deps.AttachCDP(ctx, attachOptions)
	}
	if attachErr != nil {
		_ = RecordAttach(opts.DataDir, attachErr)
		return nil, attachErr
	}
	attachResult.MainBridgeWarning = mainWarning
	attachResult.hostStarted = targetProc.Started
	_ = RecordAttach(opts.DataDir, attachResult)

	return &WindowsLaunchResult{
		CoreReused: coreReused,
		CoreURL:    coreRec.URL,
		KimiReused: kimiReused,
		KimiPID:    targetProc.PID,
		DebugPort:  debugPort,
		Attach:     attachResult,
	}, nil
}
