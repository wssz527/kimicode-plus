//go:build windows

package desktop

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"enhancedkimi/internal/platform"

	"golang.org/x/sys/windows"
)

func init() {
	runHostAttachWatcher = watchWindowsAttach
	reopenHost = reopenWindowsHost
}

func windowsAutoAttachMarker(dir string) string {
	return filepath.Join(dir, "windows-auto-attach")
}

func windowsAutoAttachTask(dir string) string {
	return "KimiCodePlus-" + sha256Hex([]byte(strings.ToLower(filepath.Clean(dir))))[:16]
}

func windowsAutoAttachEnabled(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(windowsAutoAttachMarker(dir))
	return err == nil
}

const windowsResidentVersion = "silent-v1\n"

func windowsResidentScript(dir string) string {
	return filepath.Join(dir, "windows-resident.vbs")
}

func installWindowsResidentScript(config AttachConfig) error {
	args := []string{config.Executable, "--resident", "--data-dir", config.DataDir, "--kimi-home", config.KimiHome}
	if config.Profile != "" {
		args = append(args, "--host-profile", config.Profile)
	}
	for i := range args {
		args[i] = syscall.EscapeArg(args[i])
	}
	script := "Option Explicit\r\nDim shell\r\nSet shell = CreateObject(\"WScript.Shell\")\r\n" +
		"shell.Environment(\"Process\")(\"EKD_RESIDENT_COMMAND\") = \"" + escapeVBScriptString(strings.Join(args, " ")) + "\"\r\n" +
		"WScript.Quit shell.Run(\"%EKD_RESIDENT_COMMAND%\", 0, True)\r\n"
	text, err := windows.UTF16FromString(script)
	if err != nil {
		return err
	}
	data := []byte{0xff, 0xfe}
	for _, code := range text[:len(text)-1] {
		data = binary.LittleEndian.AppendUint16(data, code)
	}
	return platform.AtomicPrivateFile(windowsResidentScript(config.DataDir), data)
}

func windowsResidentNeedsMigration(dir string) bool {
	data, err := os.ReadFile(windowsAutoAttachMarker(dir))
	return err == nil && string(data) != windowsResidentVersion
}

func windowsResidentTask(config AttachConfig, install bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	const script = `
$ErrorActionPreference='Stop'
$ProgressPreference='SilentlyContinue'
$name=$env:EKD_TASK_NAME
if ($env:EKD_TASK_ENABLE -ne 'yes') {
  if (Get-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue) {
    Unregister-ScheduledTask -TaskName $name -Confirm:$false
  }
  return
}
$user=[Security.Principal.WindowsIdentity]::GetCurrent().Name
$hostExe=Join-Path $env:SystemRoot 'System32\wscript.exe'
if (!(Test-Path -LiteralPath $hostExe -PathType Leaf)) { throw 'Windows Script Host unavailable' }
$action=New-ScheduledTaskAction -Execute $hostExe -Argument $env:EKD_TASK_ARGS -WorkingDirectory $env:EKD_TASK_DIR
$trigger=New-ScheduledTaskTrigger -AtLogOn -User $user
$principal=New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited
$settings=New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -MultipleInstances IgnoreNew -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
Register-ScheduledTask -TaskName $name -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
`
	args := "//B //Nologo " + syscall.EscapeArg(windowsResidentScript(config.DataDir))
	enable := "no"
	if install {
		enable = "yes"
	}
	_, err := runWindowsPowerShell(ctx, script, "EKD_TASK_NAME="+windowsAutoAttachTask(config.DataDir),
		"EKD_TASK_ENABLE="+enable, "EKD_TASK_ARGS="+args, "EKD_TASK_DIR="+config.DataDir)
	return err
}

func installWindowsResident(config AttachConfig) (LaunchAgentState, error) {
	if config.DataDir == "" || config.KimiHome == "" || !filepath.IsAbs(config.Executable) {
		return LaunchAgentState{}, errors.New("后台接入缺少增强核心或 Kimi Code 数据目录")
	}
	if err := installWindowsResidentScript(config); err != nil {
		return LaunchAgentState{}, fmt.Errorf("无法写入无窗口启动项：%w", err)
	}
	if err := windowsResidentTask(config, true); err != nil {
		return LaunchAgentState{}, fmt.Errorf("无法注册 KimiCode+ 登录任务：%w", err)
	}
	marker := windowsAutoAttachMarker(config.DataDir)
	if err := platform.AtomicPrivateFile(marker, []byte(windowsResidentVersion)); err != nil {
		return LaunchAgentState{}, errors.Join(err, windowsResidentTask(config, false))
	}
	return LaunchAgentState{Enabled: true, Path: marker}, nil
}

func removeWindowsResident(dir string) error {
	if dir == "" {
		return errors.New("缺少增强数据目录")
	}
	if err := windowsResidentTask(AttachConfig{DataDir: dir}, false); err != nil {
		return fmt.Errorf("无法移除 KimiCode+ 登录任务：%w", err)
	}
	var failures []error
	if err := os.Remove(windowsResidentScript(dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		failures = append(failures, err)
	}
	// The marker is the only enablement source of truth, so always clear it even
	// when the helper script was already missing or could not be deleted.
	if err := os.Remove(windowsAutoAttachMarker(dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

// windowsAttachTargetFor identifies one process lifetime from the snapshot.
func windowsAttachTargetFor(pid uint32, started windows.Filetime) windowsAttachTarget {
	return windowsAttachTarget{PID: int(pid), Started: fmt.Sprintf("%d:%d", started.HighDateTime, started.LowDateTime)}
}

func windowsAttachTargets(candidates map[uint32]windows.Filetime) map[windowsAttachTarget]bool {
	targets := make(map[windowsAttachTarget]bool, len(candidates))
	for pid, started := range candidates {
		targets[windowsAttachTargetFor(pid, started)] = true
	}
	return targets
}

func watchWindowsAttach(s *NativeService) {
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil || session == 0 {
		return
	}
	ticker := time.NewTicker(6 * time.Second)
	defer ticker.Stop()
	var retries *windowsAttachRetries
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
		cfg := s.attachConfig()
		if !windowsAutoAttachEnabled(cfg.DataDir) || s.isSafe() {
			retries = nil
			continue
		}
		candidates, err := windowsHostCandidates(session)
		if err != nil {
			continue
		}
		corePID := currentCorePID(cfg.DataDir)
		if retries == nil {
			retries = newWindowsAttachRetries(corePID)
		} else {
			retries.observeCore(corePID)
		}
		targets := windowsAttachTargets(candidates)
		retries.prune(targets)
		// The retry schedule, not a snapshot comparison, decides when the next
		// attempt is allowed: a failed process must still be revisited once its
		// backoff window elapses even though the process set never changed.
		if !retries.discoveryAllowed(time.Now()) || !retries.dueAny(targets, time.Now()) {
			continue
		}
		if !s.attachOpMu.TryLock() {
			continue
		}
		tryWindowsBackgroundAttach(s, cfg, candidates, retries, corePID)
		s.attachOpMu.Unlock()
	}
}

func tryWindowsBackgroundAttach(s *NativeService, cfg AttachConfig, candidates map[uint32]windows.Filetime, retries *windowsAttachRetries, corePID int) {
	if !windowsAutoAttachEnabled(cfg.DataDir) || s.isSafe() {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 24*time.Second)
	defer cancel()
	runWindowsBackgroundAttachStep(ctx, cfg, retries, corePID, windowsBackgroundAttachDeps{
		now:          time.Now,
		serviceAlive: func() bool { return s.ctx.Err() == nil },
		discoverHost: func(ctx context.Context) (WindowsProcessInfo, error) {
			return findWindowsAttachHost(ctx, cfg.DataDir, "", cfg.Profile)
		},
		targetForPID: func(pid int) (windowsAttachTarget, bool) {
			started, found := candidates[uint32(pid)]
			if !found {
				return windowsAttachTarget{}, false
			}
			return windowsAttachTargetFor(uint32(pid), started), true
		},
		readLease:       func() *WindowsLauncherState { return ReadWindowsLauncherState(cfg.DataDir) },
		readAttachState: func() map[string]any { return ReadAttachState(cfg.DataDir) },
		attach:          AttachNative,
		recordInstalled: func(proc WindowsProcessInfo, result *AttachResult) error {
			return recordWindowsAttachInstalled(cfg.DataDir, proc, result)
		},
		recordFailure: func(proc WindowsProcessInfo, err error) error {
			return recordWindowsAttachFailure(cfg.DataDir, proc, err)
		},
	})
}
