//go:build windows

package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"enhancedkimi/internal/platform"
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
$action=New-ScheduledTaskAction -Execute $env:EKD_TASK_EXE -Argument $env:EKD_TASK_ARGS -WorkingDirectory $env:EKD_TASK_DIR
$trigger=New-ScheduledTaskTrigger -AtLogOn -User $user
$principal=New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited
$settings=New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -MultipleInstances IgnoreNew -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
Register-ScheduledTask -TaskName $name -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
`
	args := []string{"--resident", "--data-dir", config.DataDir, "--kimi-home", config.KimiHome}
	if config.Profile != "" {
		args = append(args, "--host-profile", config.Profile)
	}
	for i := range args {
		args[i] = syscall.EscapeArg(args[i])
	}
	enable := "no"
	if install {
		enable = "yes"
	}
	_, err := runWindowsPowerShell(ctx, script, "EKD_TASK_NAME="+windowsAutoAttachTask(config.DataDir),
		"EKD_TASK_ENABLE="+enable, "EKD_TASK_EXE="+config.Executable, "EKD_TASK_ARGS="+strings.Join(args, " "), "EKD_TASK_DIR="+config.DataDir)
	return err
}

func installWindowsResident(config AttachConfig) (LaunchAgentState, error) {
	if config.DataDir == "" || config.KimiHome == "" || !filepath.IsAbs(config.Executable) {
		return LaunchAgentState{}, errors.New("后台接入缺少增强核心或 Kimi Code 数据目录")
	}
	if err := windowsResidentTask(config, true); err != nil {
		return LaunchAgentState{}, fmt.Errorf("无法注册 KimiCode+ 登录任务：%w", err)
	}
	marker := windowsAutoAttachMarker(config.DataDir)
	if err := platform.AtomicPrivateFile(marker, []byte("enabled\n")); err != nil {
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
	if err := os.Remove(windowsAutoAttachMarker(dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func watchWindowsAttach(s *NativeService) {
	ticker := time.NewTicker(6 * time.Second)
	defer ticker.Stop()
	var attached WindowsProcessInfo
	var retryAfter time.Time
	var tried WindowsProcessInfo
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
		cfg := s.attachConfig()
		if !windowsAutoAttachEnabled(cfg.DataDir) || s.safe() {
			attached = WindowsProcessInfo{}
			continue
		}
		ctx, cancel := context.WithTimeout(s.ctx, 24*time.Second)
		proc, err := findWindowsAttachHost(ctx, cfg.DataDir, "", cfg.Profile)
		if err == nil && VerifyWindowsProcessIdentity(attached, proc) != nil &&
			(VerifyWindowsProcessIdentity(tried, proc) != nil || time.Now().After(retryAfter)) {
			tried, retryAfter = proc, time.Now().Add(time.Minute)
			_, hasDebugPort := extractDebugPortArg(SplitWindowsCommandLine(proc.CommandLine))
			if hasDebugPort || (cfg.Profile == "" && IsWindowsDebugInstance(proc, nil)) {
				cancel()
				continue
			}
			if !s.attachOpMu.TryLock() {
				retryAfter = time.Now()
				cancel()
				continue
			}
			if !windowsAutoAttachEnabled(cfg.DataDir) || s.safe() {
				s.attachOpMu.Unlock()
				cancel()
				continue
			}
			result, attachErr := attachRunningWindowsMain(ctx, AttachOptions{
				DataDir: cfg.DataDir, CoreURL: cfg.CoreURL, PairingToken: cfg.PairingToken, Profile: cfg.Profile,
			}, proc)
			s.attachOpMu.Unlock()
			if attachErr == nil {
				attached = proc
				_ = RecordAttach(cfg.DataDir, result)
			} else if ctx.Err() == nil {
				_ = RecordAttach(cfg.DataDir, attachErr)
			}
		}
		cancel()
	}
}
