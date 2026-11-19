//go:build windows

package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	hostPlatformSupported = true
	hostPlatformName      = "windows"
	hostAutoAttachKind    = "windows_launcher"
)

const (
	winCreateNewProcessGroup = 0x00000200
	winDetachedProcess       = 0x00000008
)

func verifyRuntimeImage(context.Context, ProcessInfo) error {
	return ErrUnsupportedPlatform
}

func activateInspector(int) error {
	return ErrUnsupportedPlatform
}

func windowsPowerShellPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
}

func runWindowsPowerShell(ctx context.Context, script string, extraEnv ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, windowsPowerShellPath(),
		"-NoLogo", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	env := os.Environ()
	env = append(env, extraEnv...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("PowerShell 执行失败：%w (%s)", err, strings.TrimSpace(string(out)))
	}
	// Strip UTF-8 BOM if emitted by PowerShell.
	return []byte(strings.TrimPrefix(strings.TrimSpace(string(out)), "\ufeff")), nil
}

type windowsDiscoverPayload struct {
	SID       string               `json:"sid"`
	Processes []WindowsProcessInfo `json:"processes"`
}

func windowsDiscoverRaw(ctx context.Context) (windowsDiscoverPayload, error) {
	const script = `
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$sid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$procs = @(Get-CimInstance Win32_Process -Filter "Name = 'Kimi Code.exe'" | ForEach-Object {
  $owner = Invoke-CimMethod -InputObject $_ -MethodName GetOwnerSid
  if ($owner.ReturnValue -eq 0 -and $owner.Sid -eq $sid) {
    [pscustomobject]@{
      pid = [int]$_.ProcessId
      ownerSid = $owner.Sid
      executable = [string]$_.ExecutablePath
      commandLine = [string]$_.CommandLine
      started = $_.CreationDate.ToUniversalTime().ToString('o')
    }
  }
})
@{ sid = $sid; processes = $procs } | ConvertTo-Json -Depth 5 -Compress
`
	raw, err := runWindowsPowerShell(ctx, script)
	if err != nil {
		return windowsDiscoverPayload{}, err
	}
	var payload windowsDiscoverPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return windowsDiscoverPayload{}, fmt.Errorf("解析 Windows 进程列表失败：%w", err)
	}
	return payload, nil
}

func windowsCurrentSID(ctx context.Context) (string, error) {
	payload, err := windowsDiscoverRaw(ctx)
	if err != nil {
		return "", err
	}
	return payload.SID, nil
}

func windowsListProcesses(ctx context.Context) ([]WindowsProcessInfo, error) {
	payload, err := windowsDiscoverRaw(ctx)
	if err != nil {
		return nil, err
	}
	return payload.Processes, nil
}

func windowsListListeners(ctx context.Context, port int) ([]WindowsPortListener, error) {
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$items = @(Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue | Where-Object { $_.LocalPort -eq %d } | ForEach-Object {
  [pscustomobject]@{
    pid = [int]$_.OwningProcess
    localAddress = [string]$_.LocalAddress
    localPort = [int]$_.LocalPort
  }
})
ConvertTo-Json -InputObject $items -Compress
`, port)
	raw, err := runWindowsPowerShell(ctx, script)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var list []WindowsPortListener
	if err := json.Unmarshal(raw, &list); err != nil {
		var single WindowsPortListener
		if sErr := json.Unmarshal(raw, &single); sErr == nil {
			return []WindowsPortListener{single}, nil
		}
		return nil, err
	}
	return list, nil
}

func windowsListConnections(ctx context.Context, port int) ([]WindowsTCPConnection, error) {
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$items = @(Get-NetTCPConnection -State Established -ErrorAction SilentlyContinue | Where-Object { $_.LocalPort -eq %d -or $_.RemotePort -eq %d } | ForEach-Object {
  [pscustomobject]@{
    pid = [int]$_.OwningProcess
    localAddress = [string]$_.LocalAddress
    localPort = [int]$_.LocalPort
    remoteAddress = [string]$_.RemoteAddress
    remotePort = [int]$_.RemotePort
    state = [string]$_.State
  }
})
ConvertTo-Json -InputObject $items -Compress
`, port, port)
	raw, err := runWindowsPowerShell(ctx, script)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var list []WindowsTCPConnection
	if err := json.Unmarshal(raw, &list); err != nil {
		var single WindowsTCPConnection
		if sErr := json.Unmarshal(raw, &single); sErr == nil {
			return []WindowsTCPConnection{single}, nil
		}
		return nil, err
	}
	return list, nil
}

func windowsSpawnDetached(executable string, args []string) (int, error) {
	cmd := exec.Command(executable, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: winCreateNewProcessGroup | winDetachedProcess,
		HideWindow:    true,
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

type windowsShortcutPSBackend struct{}

func (windowsShortcutPSBackend) ListKimiShortcuts(kimiExecutable string) ([]WindowsShortcutInfo, error) {
	const script = `
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$targetExe = $env:EKD_TARGET_EXE
$shell = New-Object -ComObject WScript.Shell
$roots = @(
  @{ dir = Join-Path $env:APPDATA 'Microsoft\Windows\Start Menu\Programs'; loc = 'start_menu' },
  @{ dir = Join-Path $env:USERPROFILE 'Desktop'; loc = 'desktop' }
)
$found = @()
foreach ($r in $roots) {
  if (-not $r.dir -or -not (Test-Path -LiteralPath $r.dir)) { continue }
  Get-ChildItem -LiteralPath $r.dir -Filter '*.lnk' -Recurse -ErrorAction SilentlyContinue | ForEach-Object {
    try {
      $sc = $shell.CreateShortcut($_.FullName)
      $tp = [string]$sc.TargetPath
      $args = [string]$sc.Arguments
      $isKimi = ($targetExe -and $tp.Equals($targetExe, [System.StringComparison]::OrdinalIgnoreCase)) -or
                ([System.IO.Path]::GetFileName($tp).Equals('Kimi Code.exe', [System.StringComparison]::OrdinalIgnoreCase)) -or
                ($_.Name -match 'Kimi\s*Code' -and $args -match 'enhanced-kimi-launcher\.vbs')
      if ($isKimi) {
        $found += [pscustomobject]@{
          path = $_.FullName
          targetPath = $tp
          arguments = $args
          workingDirectory = [string]$sc.WorkingDirectory
          iconLocation = [string]$sc.IconLocation
          location = $r.loc
        }
      }
    } catch {}
  }
}
ConvertTo-Json -InputObject $found -Depth 4 -Compress
`
	raw, err := runWindowsPowerShell(context.Background(), script, "EKD_TARGET_EXE="+kimiExecutable)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var list []WindowsShortcutInfo
	if err := json.Unmarshal(raw, &list); err != nil {
		var single WindowsShortcutInfo
		if sErr := json.Unmarshal(raw, &single); sErr == nil {
			return []WindowsShortcutInfo{single}, nil
		}
		return nil, err
	}
	return list, nil
}

func (windowsShortcutPSBackend) RetargetShortcut(lnkPath, targetPath, arguments, workingDir, iconLocation string) error {
	const script = `
$ErrorActionPreference = 'Stop'
$shell = New-Object -ComObject WScript.Shell
$sc = $shell.CreateShortcut($env:EKD_SC_PATH)
$sc.TargetPath = $env:EKD_SC_TARGET
$sc.Arguments = $env:EKD_SC_ARGS
if ($env:EKD_SC_WORKDIR) { $sc.WorkingDirectory = $env:EKD_SC_WORKDIR }
if ($env:EKD_SC_ICON) { $sc.IconLocation = $env:EKD_SC_ICON }
$sc.Save()
`
	_, err := runWindowsPowerShell(
		context.Background(),
		script,
		"EKD_SC_PATH="+lnkPath,
		"EKD_SC_TARGET="+targetPath,
		"EKD_SC_ARGS="+arguments,
		"EKD_SC_WORKDIR="+workingDir,
		"EKD_SC_ICON="+iconLocation,
	)
	return err
}

func resolveWindowsKimiExecutable(dataDir, explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return DefaultWindowsKimiExecutable(os.Getenv("LOCALAPPDATA"), explicit)
	}
	if cfg, err := ReadWindowsLauncherConfig(dataDir); err == nil && cfg != nil && cfg.KimiExecutable != "" {
		return cfg.KimiExecutable, nil
	}
	return DefaultWindowsKimiExecutable(os.Getenv("LOCALAPPDATA"), "")
}

func discoverHostPlatform(ctx context.Context, profile, dataDir string) (map[string]any, error) {
	payload, err := windowsDiscoverRaw(ctx)
	if err != nil {
		return map[string]any{"running": false, "reason": err.Error()}, nil
	}
	kimiExe, _ := resolveWindowsKimiExecutable(dataDir, "")
	matches, err := SelectWindowsMainProcesses(payload.SID, payload.Processes, kimiExe, profile)
	if err != nil {
		return map[string]any{"running": false, "reason": err.Error()}, nil
	}
	if len(matches) != 1 {
		return map[string]any{"running": false, "reason": fmt.Sprintf("需要且只能找到一个 Kimi Code 主进程，实际找到 %d 个", len(matches))}, nil
	}
	proc := matches[0]
	lease := ReadWindowsLauncherState(dataDir)
	debug := IsWindowsDebugInstance(proc, lease)
	port, hasPort := extractDebugPortArg(SplitWindowsCommandLine(proc.CommandLine))
	fuse := "ready"
	if _, err := VerifyWindowsBinaryFile(proc.Executable); err != nil {
		if errors.Is(err, ErrInspectorDisabled) {
			fuse = "signal-disabled"
		} else {
			fuse = "unknown-layout"
		}
	}
	portWarning := ""
	if hasPort {
		if conns, err := windowsListConnections(ctx, port); err == nil {
			alerts := DetectForeignDebugClients(conns, port, proc.PID, os.Getpid())
			portWarning = FormatDebugClientAlerts(alerts)
		}
	}
	res := map[string]any{
		"running":         true,
		"pid":             proc.PID,
		"profile":         profile,
		"isolated":        profile != "",
		"debugInstance":   debug,
		"dailyInstance":   profile == "",
		"fuse":            fuse,
		"inspectorBusy":   false,
		"consentRequired": profile == "",
		"platform":        hostPlatformName,
	}
	if hasPort {
		res["debugPort"] = port
	}
	if portWarning != "" {
		res["debugPortWarning"] = portWarning
	}
	return res, nil
}

func attachNativePlatform(ctx context.Context, options AttachOptions) (*AttachResult, error) {
	if options.CoreURL == "" || len(options.PairingToken) < AttachTokenMinLength {
		return nil, errors.New("缺少增强核心地址或配对凭据")
	}
	payload, err := windowsDiscoverRaw(ctx)
	if err != nil {
		return nil, err
	}
	kimiExe, _ := resolveWindowsKimiExecutable(options.DataDir, options.KimiExecutable)
	matches, err := SelectWindowsMainProcesses(payload.SID, payload.Processes, kimiExe, options.Profile)
	if err != nil {
		return nil, err
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("需要且只能找到一个 Kimi Code 主进程，实际找到 %d 个", len(matches))
	}
	proc := matches[0]
	lease := ReadWindowsLauncherState(options.DataDir)
	if options.Profile == "" && IsWindowsDebugInstance(proc, lease) {
		return nil, ErrWindowsDebugInstance
	}
	if options.Profile == "" && !options.AllowDailyInstance {
		return nil, ErrConsentRequired
	}
	if _, err := VerifyWindowsBinaryFile(proc.Executable); err != nil {
		return nil, err
	}
	debugPort := options.DebugPort
	if debugPort <= 0 {
		if port, ok := extractDebugPortArg(SplitWindowsCommandLine(proc.CommandLine)); ok {
			debugPort = port
		}
	}
	if debugPort <= 0 {
		return nil, errors.New("当前 Kimi Code 未使用调试端口启动，请通过 KimiCode+ Windows 启动器打开客户端")
	}
	listeners, err := windowsListListeners(ctx, debugPort)
	if err != nil {
		return nil, err
	}
	if err := VerifyWindowsDebugPortOwner(listeners, debugPort, proc.PID); err != nil {
		return nil, err
	}
	// Re-check process identity right before connecting.
	recheck, err := windowsDiscoverRaw(ctx)
	if err != nil {
		return nil, err
	}
	recheckMatches, err := SelectWindowsMainProcesses(recheck.SID, recheck.Processes, kimiExe, options.Profile)
	if err != nil || len(recheckMatches) != 1 {
		return nil, errors.New("Windows 宿主进程在接入过程中退出")
	}
	if err := VerifyWindowsProcessIdentity(proc, recheckMatches[0]); err != nil {
		return nil, err
	}
	return AttachWindowsCDP(ctx, WindowsCDPAttachOptions{
		DebugPort:          debugPort,
		HostPID:            proc.PID,
		CoreURL:            options.CoreURL,
		PairingToken:       options.PairingToken,
		Profile:            options.Profile,
		Daily:              options.Profile == "",
		AppVersionOverride: options.AppVersionOverride,
		AuditClients: func(auditCtx context.Context, port, hostPID int) ([]WindowsDebugClientAlert, error) {
			conns, cErr := windowsListConnections(auditCtx, port)
			if cErr != nil {
				return nil, cErr
			}
			return DetectForeignDebugClients(conns, port, hostPID, os.Getpid()), nil
		},
	})
}

func autoAttachStatusPlatform(dataDir string) LaunchAgentState {
	return WindowsLauncherStatus(dataDir)
}

func enableAutoAttachPlatform(config AttachConfig, allowDaily bool) (LaunchAgentState, error) {
	kimiExe, err := resolveWindowsKimiExecutable(config.DataDir, "")
	if err != nil {
		return LaunchAgentState{}, err
	}
	state, _, err := InstallWindowsLauncherAndShortcuts(WindowsLauncherConfig{
		DataDir:            config.DataDir,
		Executable:         config.Executable,
		KimiExecutable:     kimiExe,
		KimiHome:           config.KimiHome,
		AllowDailyInstance: allowDaily,
	}, windowsShortcutPSBackend{})
	return state, err
}

func disableAutoAttachPlatform(dataDir string) (LaunchAgentState, error) {
	state, _, err := RestoreWindowsShortcuts(dataDir)
	return state, err
}

// RunWindowsLauncherPlatform executes the Windows launcher with real OS deps.
func RunWindowsLauncherPlatform(ctx context.Context, opts WindowsLaunchOptions) (*WindowsLaunchResult, error) {
	kimiExe, err := resolveWindowsKimiExecutable(opts.DataDir, opts.KimiExecutable)
	if err != nil {
		return nil, err
	}
	opts.KimiExecutable = kimiExe
	return RunWindowsLauncher(ctx, opts, WindowsLauncherDeps{
		CurrentSID:      windowsCurrentSID,
		ListProcesses:   windowsListProcesses,
		ListListeners:   windowsListListeners,
		ListConnections: windowsListConnections,
		VerifyBinary:    VerifyWindowsBinaryFile,
		SpawnDetached:   windowsSpawnDetached,
		PickPort:        PickFreeLoopbackPort,
		AttachCDP:       AttachWindowsCDP,
		CheckCoreHealth: defaultCheckCoreHealth,
		SelfPID:         os.Getpid,
	})
}

// Ensure strconv is referenced cleanly if needed.
var _ = strconv.Itoa
