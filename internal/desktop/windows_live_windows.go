//go:build windows

package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func attachRunningWindowsMain(ctx context.Context, options AttachOptions, proc WindowsProcessInfo) (result *AttachResult, attachErr error) {
	binary, err := VerifyWindowsBinaryFile(proc.Executable)
	if err != nil || !binary.supportsLiveAttach() {
		return nil, errors.New("当前 Windows 客户端不支持运行中接入")
	}
	if options.DataDir == "" {
		return nil, errors.New("运行中接入需要增强数据目录")
	}
	gate, err := acquireHostAttachLock(ctx, options.DataDir)
	if err != nil {
		return nil, err
	}
	if gate != nil {
		defer gate.Close()
	}
	prevInstalled := readInstalledAttachState(options.DataDir)
	defer func() {
		result, attachErr = commitWindowsAttachOutcomeWithPreviousUnlocked(options.DataDir, proc, prevInstalled, result, attachErr)
	}()
	return attachRunningWindowsMainUnlocked(ctx, options, proc)
}

func attachRunningWindowsMainUnlocked(ctx context.Context, options AttachOptions, proc WindowsProcessInfo) (result *AttachResult, attachErr error) {
	if options.DataDir == "" {
		return nil, errors.New("运行中接入需要增强数据目录")
	}
	defer func() {
		if attachErr != nil && isWindowsMainBridgeTornDownErr(attachErr) {
			attachErr = markBridgeTornDown(attachErr)
		}
	}()
	if err := validateLiveWindowsHost(ctx, options, proc); err != nil {
		return nil, err
	}
	key := windowsLiveAttachKey(proc, options)
	if options.SkipCurrent {
		state := ReadAttachState(options.DataDir)
		if recordedKey, _ := state["attachKey"].(string); recordedKey != "" {
			if state["transport"] == "main-loopback-rpc" && attachStateMatchesLiveCore(ctx, options.DataDir, state, key, options) {
				return nil, nil
			}
		} else {
			runtimePID, runtimeErr := readCoreRuntimePID(options.DataDir)
			if runtimeErr == nil || errors.Is(runtimeErr, os.ErrNotExist) {
				corePID := runtimePID
				if runtimeErr != nil {
					corePID = os.Getpid()
				}
				if windowsCurrentAttachStateForDir(options.DataDir, proc, state, false, corePID) == "attached" &&
					verifyLiveCoreForSkip(ctx, corePID, options.CoreURL, options.PairingToken) {
					return nil, nil
				}
			}
		}
	}
	if !windowsInspectorClosed(InspectorPort) {
		return nil, errors.New("本机临时调试端口已被占用，请关闭其他调试会话后重试")
	}
	if invalidateErr := invalidateInstalledAttachBeforeInjectUnlocked(options.DataDir); invalidateErr != nil {
		return nil, markAttachPersistFailed(fmt.Errorf("无法更新接入状态文件，已中止接入以免破坏现有增强桥：%w", invalidateErr))
	}
	// Electron's enabled RunAsNode mode uses the same Node build and architecture as its host.
	triggerCtx, stopTrigger := context.WithTimeout(ctx, 4*time.Second)
	defer stopTrigger()
	cmd := exec.CommandContext(triggerCtx, proc.Executable, "-e", "process._debugProcess(Number(process.argv[1]))", strconv.Itoa(proc.PID))
	cmd.Env = append(os.Environ(), "ELECTRON_RUN_AS_NODE=1", "NODE_OPTIONS=")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if _, err := cmd.Output(); err != nil {
		return nil, fmt.Errorf("无法开启 Windows 客户端的临时调试端口：%w", err)
	}
	verify := func(verifyCtx context.Context) error {
		listeners, err := windowsListListeners(verifyCtx, InspectorPort)
		if err != nil {
			return err
		}
		return VerifyWindowsDebugPortOwner(listeners, InspectorPort, proc.PID)
	}
	// From this point a debugging port exists on the client. Any failure below —
	// the port never becoming ready, the target lookup, the ownership check or the
	// WebSocket handshake — has to close it, or it stays open until the host is
	// restarted. The successful path closes it through its own session, so the
	// guard only acts when this function does not return an attach result.
	portClosed := false
	defer func() {
		if portClosed {
			return
		}
		if closeErr := ensureWindowsInspectorClosed(InspectorPort, proc.PID, verify); closeErr != nil {
			attachErr = errors.Join(attachErr, closeErr)
		}
	}()
	for attempt := 0; windowsInspectorClosed(InspectorPort); attempt++ {
		if attempt == 30 {
			return nil, errors.New("Windows 客户端的临时调试端口未就绪")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	attached, err := attachWindowsMain(ctx, WindowsCDPAttachOptions{
		HostPID: proc.PID, CoreURL: options.CoreURL, PairingToken: options.PairingToken, CoreVersion: options.CoreVersion,
		Profile: options.Profile, Daily: options.Profile == "", AppVersionOverride: options.AppVersionOverride,
	}, options.DataDir, InspectorPort, verify)
	if err != nil {
		return nil, err
	}
	portClosed = true
	if attached != nil {
		attached.attachKey = key
		attached.hostStarted = proc.Started
	}
	return attached, nil
}

func validateLiveWindowsHost(ctx context.Context, options AttachOptions, proc WindowsProcessInfo) error {
	payload, err := windowsDiscoverRaw(ctx)
	if err != nil {
		return err
	}
	matches, err := SelectWindowsMainProcesses(payload.SID, payload.desktopProcesses(), proc.Executable, options.Profile)
	if err != nil || len(matches) != 1 {
		return errors.New("无法确认唯一的 Kimi Code 主进程，请稍后重试")
	}
	if err := VerifyWindowsProcessIdentity(proc, matches[0]); err != nil {
		return err
	}
	if proc.SessionID == 0 || proc.SessionID != payload.SessionID {
		return errors.New("增强核心与 Kimi Code 必须在同一个 Windows 桌面会话运行")
	}
	return nil
}
