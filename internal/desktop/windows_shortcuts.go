package desktop

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"enhancedkimi/internal/platform"
)

const windowsShortcutManifestName = "manifest.json"

// WindowsShortcutInfo describes an existing .lnk shortcut discovered on Windows.
type WindowsShortcutInfo struct {
	Path             string `json:"path"`
	TargetPath       string `json:"targetPath"`
	Arguments        string `json:"arguments"`
	WorkingDirectory string `json:"workingDirectory"`
	IconLocation     string `json:"iconLocation"`
	Location         string `json:"location"` // "start_menu" | "desktop"
}

// WindowsShortcutBackup records one backed-up original Kimi Code .lnk shortcut.
type WindowsShortcutBackup struct {
	OriginalPath    string `json:"originalPath"`
	BackupPath      string `json:"backupPath"`
	SHA256          string `json:"sha256"`
	OriginalTarget  string `json:"originalTarget"`
	OriginalArgs    string `json:"originalArgs"`
	OriginalWorkDir string `json:"originalWorkDir"`
	OriginalIcon    string `json:"originalIcon"`
	Location        string `json:"location"`
}

// WindowsShortcutManifest tracks the fixed launcher and every backed-up shortcut
// so one-click restore can put every Start Menu / Desktop shortcut back verbatim.
type WindowsShortcutManifest struct {
	Version        int                     `json:"version"`
	Enabled        bool                    `json:"enabled"`
	LauncherScript string                  `json:"launcherScript"`
	Executable     string                  `json:"executable"`
	KimiExecutable string                  `json:"kimiExecutable"`
	KimiHome       string                  `json:"kimiHome,omitempty"`
	DebugPort      int                     `json:"debugPort,omitempty"`
	UpdatedAt      string                  `json:"updatedAt"`
	Entries        []WindowsShortcutBackup `json:"entries"`
}

// WindowsLauncherConfig is persisted alongside the fixed-path launcher shim so
// shortcut launches pick up the configured Kimi executable and debug port even
// after Kimi Code updates itself.
type WindowsLauncherConfig struct {
	DataDir            string `json:"dataDir"`
	Executable         string `json:"executable"`
	KimiExecutable     string `json:"kimiExecutable"`
	KimiHome           string `json:"kimiHome,omitempty"`
	DebugPort          int    `json:"debugPort,omitempty"`
	AllowDailyInstance bool   `json:"allowDailyInstance"`
	UpdatedAt          string `json:"updatedAt"`
}

// WindowsShortcutBackend abstracts reading and retargeting Windows .lnk files.
// On Windows this delegates to WScript.Shell via PowerShell; tests inject a fake.
type WindowsShortcutBackend interface {
	ListKimiShortcuts(kimiExecutable string) ([]WindowsShortcutInfo, error)
	RetargetShortcut(lnkPath, targetPath, arguments, workingDir, iconLocation string) error
}

func windowsShortcutBackupDir(dataDir string) string {
	return filepath.Join(dataDir, "shortcut-backups")
}

func windowsShortcutManifestPath(dataDir string) string {
	return filepath.Join(windowsShortcutBackupDir(dataDir), windowsShortcutManifestName)
}

func escapeVBScriptString(value string) string {
	return strings.ReplaceAll(value, `"`, `""`)
}

// RenderWindowsLauncherVBS builds a silent VBScript launcher at a fixed path so
// Start Menu and Desktop shortcuts open Kimi Code without flashing a console.
func RenderWindowsLauncherVBS(executable, dataDir, kimiHome, kimiExe string, debugPort int) (string, error) {
	if strings.TrimSpace(executable) == "" {
		return "", errors.New("缺少增强核心可执行文件路径，无法生成 Windows 启动器")
	}
	if strings.TrimSpace(dataDir) == "" {
		return "", errors.New("缺少增强数据目录，无法生成 Windows 启动器")
	}
	// Construct a VBScript command line string with doubled quotes inside quotes.
	var cmd strings.Builder
	cmd.WriteString(`"""" & "`)
	cmd.WriteString(escapeVBScriptString(executable))
	cmd.WriteString(`" & """ launch --data-dir """ & "`)
	cmd.WriteString(escapeVBScriptString(dataDir))
	cmd.WriteString(`" & """ --allow-daily-instance`)
	if strings.TrimSpace(kimiHome) != "" {
		cmd.WriteString(` --kimi-home """ & "`)
		cmd.WriteString(escapeVBScriptString(kimiHome))
		cmd.WriteString(`" & """`)
	}
	if strings.TrimSpace(kimiExe) != "" {
		cmd.WriteString(` --kimi-exe """ & "`)
		cmd.WriteString(escapeVBScriptString(kimiExe))
		cmd.WriteString(`" & """`)
	}
	if debugPort > 0 && debugPort <= 65535 {
		cmd.WriteString(` --debug-port `)
		cmd.WriteString(strconv.Itoa(debugPort))
	}
	cmd.WriteString(`"`)
	return fmt.Sprintf(
		"Option Explicit\r\nDim shell, cmd\r\nSet shell = CreateObject(\"WScript.Shell\")\r\ncmd = %s\r\nshell.Run cmd, 0, False\r\n",
		cmd.String(),
	), nil
}

// ReadWindowsShortcutManifest loads the saved shortcut backup manifest, if any.
func ReadWindowsShortcutManifest(dataDir string) (*WindowsShortcutManifest, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(windowsShortcutManifestPath(dataDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var manifest WindowsShortcutManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

// ReadWindowsLauncherConfig loads the persisted launcher config if present.
func ReadWindowsLauncherConfig(dataDir string) (*WindowsLauncherConfig, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, nil
	}
	_, _, cfgPath, _ := WindowsLauncherPaths(dataDir)
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var cfg WindowsLauncherConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// InstallWindowsLauncherAndShortcuts writes the fixed-path launcher shim,
// backs up every discovered Start Menu / Desktop Kimi Code .lnk shortcut with
// SHA-256 verification before mutating anything, and retargets them to launch
// Kimi Code through the enhanced launcher. If retargeting fails partway through,
// already-modified shortcuts are rolled back from their backups.
func InstallWindowsLauncherAndShortcuts(
	cfg WindowsLauncherConfig,
	backend WindowsShortcutBackend,
) (LaunchAgentState, *WindowsShortcutManifest, error) {
	if strings.TrimSpace(cfg.DataDir) == "" {
		return LaunchAgentState{}, nil, errors.New("缺少增强数据目录，无法安装 Windows 启动器")
	}
	if strings.TrimSpace(cfg.Executable) == "" {
		return LaunchAgentState{}, nil, errors.New("缺少增强核心可执行文件路径，无法安装 Windows 启动器")
	}
	if !cfg.AllowDailyInstance {
		return LaunchAgentState{}, nil, ErrConsentRequired
	}
	if backend == nil {
		return LaunchAgentState{}, nil, errors.New("当前平台不支持修改 Windows 快捷方式")
	}
	launcherDir, scriptPath, configPath, _ := WindowsLauncherPaths(cfg.DataDir)
	if err := os.MkdirAll(launcherDir, 0o700); err != nil {
		return LaunchAgentState{}, nil, fmt.Errorf("无法创建启动器目录：%w", err)
	}
	script, err := RenderWindowsLauncherVBS(cfg.Executable, cfg.DataDir, cfg.KimiHome, cfg.KimiExecutable, cfg.DebugPort)
	if err != nil {
		return LaunchAgentState{}, nil, err
	}
	if err := platform.AtomicPrivateFile(scriptPath, []byte(script)); err != nil {
		return LaunchAgentState{}, nil, fmt.Errorf("无法写入 Windows 启动器脚本：%w", err)
	}
	cfg.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	cfgBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return LaunchAgentState{}, nil, err
	}
	if err := platform.AtomicPrivateFile(configPath, append(cfgBytes, '\n')); err != nil {
		return LaunchAgentState{}, nil, fmt.Errorf("无法写入 Windows 启动器配置：%w", err)
	}

	shortcuts, err := backend.ListKimiShortcuts(cfg.KimiExecutable)
	if err != nil {
		return LaunchAgentState{Path: scriptPath}, nil, fmt.Errorf("无法扫描 Kimi Code 快捷方式：%w", err)
	}

	backupDir := windowsShortcutBackupDir(cfg.DataDir)
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return LaunchAgentState{Path: scriptPath}, nil, fmt.Errorf("无法创建快捷方式备份目录：%w", err)
	}

	existingManifest, _ := ReadWindowsShortcutManifest(cfg.DataDir)
	existingByPath := map[string]WindowsShortcutBackup{}
	if existingManifest != nil {
		for _, entry := range existingManifest.Entries {
			existingByPath[strings.ToLower(cleanWindowsPath(entry.OriginalPath))] = entry
		}
	}

	entries := make([]WindowsShortcutBackup, 0, len(shortcuts))
	for _, sc := range shortcuts {
		key := strings.ToLower(cleanWindowsPath(sc.Path))
		if prev, ok := existingByPath[key]; ok {
			// Preserve the original unmodified backup on re-install.
			if raw, readErr := os.ReadFile(prev.BackupPath); readErr == nil && sha256Hex(raw) == prev.SHA256 {
				entries = append(entries, prev)
				continue
			}
		}
		raw, err := os.ReadFile(sc.Path)
		if err != nil {
			return LaunchAgentState{Path: scriptPath}, nil, fmt.Errorf("备份快捷方式 %s 失败：%w", sc.Path, err)
		}
		digest := sha256Hex(raw)
		backupName := fmt.Sprintf("%s-%s", digest[:12], filepath.Base(sc.Path))
		backupPath := filepath.Join(backupDir, backupName)
		if err := platform.AtomicPrivateFile(backupPath, raw); err != nil {
			return LaunchAgentState{Path: scriptPath}, nil, fmt.Errorf("写入快捷方式备份失败：%w", err)
		}
		entries = append(entries, WindowsShortcutBackup{
			OriginalPath:    sc.Path,
			BackupPath:      backupPath,
			SHA256:          digest,
			OriginalTarget:  sc.TargetPath,
			OriginalArgs:    sc.Arguments,
			OriginalWorkDir: sc.WorkingDirectory,
			OriginalIcon:    sc.IconLocation,
			Location:        sc.Location,
		})
	}

	manifest := &WindowsShortcutManifest{
		Version:        1,
		Enabled:        true,
		LauncherScript: scriptPath,
		Executable:     cfg.Executable,
		KimiExecutable: cfg.KimiExecutable,
		KimiHome:       cfg.KimiHome,
		DebugPort:      cfg.DebugPort,
		UpdatedAt:      time.Now().UTC().Format(time.RFC3339),
		Entries:        entries,
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return LaunchAgentState{Path: scriptPath}, nil, err
	}
	if err := platform.AtomicPrivateFile(windowsShortcutManifestPath(cfg.DataDir), append(manifestBytes, '\n')); err != nil {
		return LaunchAgentState{Path: scriptPath}, nil, fmt.Errorf("写入快捷方式备份清单失败：%w", err)
	}

	// Retarget each shortcut to wscript.exe invoking the fixed-path .vbs shim,
	// preserving Kimi Code's original icon. Roll back any modified shortcuts on error.
	wscriptTarget := `C:\Windows\System32\wscript.exe`
	wscriptArgs := fmt.Sprintf(`//B //Nologo "%s"`, scriptPath)
	modified := make([]WindowsShortcutBackup, 0, len(entries))
	for _, entry := range entries {
		icon := entry.OriginalIcon
		if strings.TrimSpace(icon) == "" && strings.TrimSpace(entry.OriginalTarget) != "" {
			icon = entry.OriginalTarget + ",0"
		}
		workDir := entry.OriginalWorkDir
		if strings.TrimSpace(workDir) == "" && strings.TrimSpace(entry.OriginalTarget) != "" {
			workDir = filepath.Dir(entry.OriginalTarget)
		}
		if err := backend.RetargetShortcut(entry.OriginalPath, wscriptTarget, wscriptArgs, workDir, icon); err != nil {
			for _, prev := range modified {
				if raw, rErr := os.ReadFile(prev.BackupPath); rErr == nil {
					_ = writeFileDirect(prev.OriginalPath, raw)
				}
			}
			manifest.Enabled = false
			if rollbackBytes, mErr := json.MarshalIndent(manifest, "", "  "); mErr == nil {
				_ = platform.AtomicPrivateFile(windowsShortcutManifestPath(cfg.DataDir), append(rollbackBytes, '\n'))
			}
			return LaunchAgentState{Path: scriptPath, Error: err.Error()}, nil, fmt.Errorf("更新快捷方式 %s 失败（已回滚全部改动）：%w", entry.OriginalPath, err)
		}
		modified = append(modified, entry)
	}

	return LaunchAgentState{
		Enabled:       true,
		Path:          scriptPath,
		ShortcutCount: len(entries),
		BackupDir:     backupDir,
	}, manifest, nil
}

func writeFileDirect(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return platform.AtomicPrivateFile(path, data)
}

// RestoreWindowsShortcuts restores every backed-up Start Menu / Desktop .lnk
// shortcut verbatim after verifying each backup's SHA-256 digest.
func RestoreWindowsShortcuts(dataDir string) (LaunchAgentState, int, error) {
	if strings.TrimSpace(dataDir) == "" {
		return LaunchAgentState{}, 0, errors.New("缺少增强数据目录，无法恢复快捷方式")
	}
	_, scriptPath, _, _ := WindowsLauncherPaths(dataDir)
	manifest, err := ReadWindowsShortcutManifest(dataDir)
	if err != nil {
		return LaunchAgentState{Path: scriptPath}, 0, fmt.Errorf("无法读取快捷方式备份清单：%w", err)
	}
	if manifest == nil {
		_ = os.Remove(scriptPath)
		return LaunchAgentState{Enabled: false, Path: scriptPath}, 0, nil
	}
	restored := 0
	for _, entry := range manifest.Entries {
		raw, err := os.ReadFile(entry.BackupPath)
		if err != nil {
			return LaunchAgentState{Path: scriptPath, Error: err.Error()}, restored, fmt.Errorf("无法读取快捷方式备份 %s：%w", entry.BackupPath, err)
		}
		if entry.SHA256 != "" && sha256Hex(raw) != entry.SHA256 {
			return LaunchAgentState{Path: scriptPath}, restored, fmt.Errorf("快捷方式备份校验和不匹配，拒绝覆盖 %s", entry.OriginalPath)
		}
		if err := writeFileDirect(entry.OriginalPath, raw); err != nil {
			return LaunchAgentState{Path: scriptPath}, restored, fmt.Errorf("恢复快捷方式 %s 失败：%w", entry.OriginalPath, err)
		}
		restored++
	}
	manifest.Enabled = false
	manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if encoded, err := json.MarshalIndent(manifest, "", "  "); err == nil {
		_ = platform.AtomicPrivateFile(windowsShortcutManifestPath(dataDir), append(encoded, '\n'))
	}
	_ = os.Remove(scriptPath)
	return LaunchAgentState{
		Enabled:       false,
		Path:          scriptPath,
		ShortcutCount: restored,
		BackupDir:     windowsShortcutBackupDir(dataDir),
	}, restored, nil
}

// WindowsLauncherStatus reports whether the Windows fixed-path launcher and
// shortcut takeover are currently active.
func WindowsLauncherStatus(dataDir string) LaunchAgentState {
	if strings.TrimSpace(dataDir) == "" {
		return LaunchAgentState{}
	}
	_, scriptPath, _, _ := WindowsLauncherPaths(dataDir)
	manifest, err := ReadWindowsShortcutManifest(dataDir)
	if err != nil {
		return LaunchAgentState{Path: scriptPath, Error: err.Error()}
	}
	_, scriptErr := os.Stat(scriptPath)
	scriptExists := scriptErr == nil
	if manifest == nil {
		return LaunchAgentState{Enabled: false, Path: scriptPath}
	}
	return LaunchAgentState{
		Enabled:       manifest.Enabled && scriptExists,
		Path:          scriptPath,
		ShortcutCount: len(manifest.Entries),
		BackupDir:     windowsShortcutBackupDir(dataDir),
	}
}
