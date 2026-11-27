//go:build darwin

package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"enhancedkimi/internal/updater"
)

const darwinLauncherAppName = "KimiCode+.app"

// darwinLauncherDir overrides ~/Applications in tests so unit tests never touch
// the user's real Applications directory.
var darwinLauncherDir = ""

var handoffManagedCore = defaultHandoffManagedCore

// DarwinLauncherAppPath returns the user-scoped .app bundle path on macOS.
func DarwinLauncherAppPath() (string, error) {
	if darwinLauncherDir != "" {
		return filepath.Join(darwinLauncherDir, darwinLauncherAppName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Applications", darwinLauncherAppName), nil
}

func darwinLauncherExecutablePath(appPath string) string {
	return filepath.Join(appPath, "Contents", "MacOS", "KimiCode+")
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func renderDarwinLauncherPlist() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDevelopmentRegion</key>
  <string>zh_CN</string>
  <key>CFBundleDisplayName</key>
  <string>KimiCode+</string>
  <key>CFBundleExecutable</key>
  <string>KimiCode+</string>
  <key>CFBundleIdentifier</key>
  <string>com.user.enhanced-kimi.launcher</string>
  <key>CFBundleInfoDictionaryVersion</key>
  <string>6.0</string>
  <key>CFBundleName</key>
  <string>KimiCode+</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleShortVersionString</key>
  <string>1.0</string>
  <key>LSMinimumSystemVersion</key>
  <string>12.0</string>
  <key>LSUIElement</key>
  <true/>
</dict>
</plist>
`
}

func renderDarwinLauncherScript(config LaunchAgentConfig) string {
	corePath := shellSingleQuote(config.Executable)
	dataDir := shellSingleQuote(config.DataDir)
	kimiHome := shellSingleQuote(config.KimiHome)
	return fmt.Sprintf(`#!/bin/sh
set -u
CORE=%s
DATA_DIR=%s
KIMI_HOME=%s
HANDOFF="${EKD_LAUNCHER_HANDOFF:-0}"
if [ "${1:-}" = "--handoff" ]; then
  HANDOFF=1
fi

if [ ! -x "$CORE" ]; then
  if [ "$HANDOFF" != "1" ]; then
    /usr/bin/osascript -e 'display alert "未找到 KimiCode+ 核心程序" message "请重新运行增强核心或在设置中重新启用自动接入后再试。"' >/dev/null 2>&1 || true
  fi
  exit 1
fi

mkdir -p "$DATA_DIR"
LOG="$DATA_DIR/launcher.log"
RESIDENT_LOG="$DATA_DIR/resident-core.log"
RUNTIME="$DATA_DIR/runtime.json"

start_core() {
  if [ -n "$KIMI_HOME" ]; then
    /usr/bin/nohup "$CORE" --resident --data-dir "$DATA_DIR" --kimi-home "$KIMI_HOME" >>"$RESIDENT_LOG" 2>&1 </dev/null &
  else
    /usr/bin/nohup "$CORE" --resident --data-dir "$DATA_DIR" >>"$RESIDENT_LOG" 2>&1 </dev/null &
  fi
}

if [ "$HANDOFF" = "1" ]; then
  start_core
  exit 0
fi

core_running() {
  [ -r "$RUNTIME" ] || return 1
  pid=$(/usr/bin/sed -n 's/.*"pid": *\([0-9][0-9]*\).*/\1/p' "$RUNTIME" | /usr/bin/head -n 1)
  case "$pid" in
    ''|*[!0-9]*) return 1 ;;
  esac
  [ "$pid" -gt 0 ] || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  /bin/ps -p "$pid" -o comm= 2>/dev/null | /usr/bin/grep -q 'enhanced-kimi'
}

find_kimi_app() {
  if [ -d "/Applications/Kimi Code.app" ]; then
    printf '%%s' "/Applications/Kimi Code.app"
    return 0
  fi
  if [ -d "$HOME/Applications/Kimi Code.app" ]; then
    printf '%%s' "$HOME/Applications/Kimi Code.app"
    return 0
  fi
  return 1
}

kimi_running() {
  /usr/bin/pgrep -f '/Kimi Code\.app/Contents/MacOS/Kimi Code' >/dev/null 2>&1
}

KIMI_APP=$(find_kimi_app || true)
if [ -n "$KIMI_APP" ]; then
  /usr/bin/open -a "$KIMI_APP" >/dev/null 2>&1 || true
fi

if ! core_running; then
  start_core
  i=0
  while [ "$i" -lt 75 ]; do
    if core_running; then
      break
    fi
    /bin/sleep 0.2
    i=$((i + 1))
  done
fi

if [ -n "$KIMI_APP" ] && ! kimi_running; then
  i=0
  while [ "$i" -lt 75 ]; do
    if kimi_running; then
      break
    fi
    /bin/sleep 0.2
    i=$((i + 1))
  done
fi

run_attach() {
  if [ -n "$KIMI_HOME" ]; then
    "$CORE" attach --data-dir "$DATA_DIR" --kimi-home "$KIMI_HOME" --allow-daily-instance >>"$LOG" 2>&1
  else
    "$CORE" attach --data-dir "$DATA_DIR" --allow-daily-instance >>"$LOG" 2>&1
  fi
}

if ! run_attach; then
  /bin/sleep 2
  run_attach || true
fi
`, corePath, dataDir, kimiHome)
}

// InstallDarwinLauncherApp creates or updates ~/Applications/KimiCode+.app so
// macOS users always have a Spotlight/Finder entry point to reopen KimiCode+.
func InstallDarwinLauncherApp(config LaunchAgentConfig) (string, error) {
	if config.Profile != "" {
		return "", errors.New("隔离宿主配置不会写入日常启动器")
	}
	if config.Executable == "" || config.DataDir == "" {
		return "", errors.New("缺少增强核心路径或数据目录，无法创建启动器")
	}
	appPath, err := DarwinLauncherAppPath()
	if err != nil {
		return "", err
	}
	macosDir := filepath.Join(appPath, "Contents", "MacOS")
	if err := os.MkdirAll(macosDir, 0o755); err != nil {
		return "", fmt.Errorf("无法创建启动器目录：%w", err)
	}
	plistPath := filepath.Join(appPath, "Contents", "Info.plist")
	if err := atomicWriteModeFile(plistPath, []byte(renderDarwinLauncherPlist()), 0o644); err != nil {
		return "", fmt.Errorf("无法写入启动器清单：%w", err)
	}
	scriptPath := darwinLauncherExecutablePath(appPath)
	if err := atomicWriteModeFile(scriptPath, []byte(renderDarwinLauncherScript(config)), 0o755); err != nil {
		return "", fmt.Errorf("无法写入启动器脚本：%w", err)
	}
	return appPath, nil
}

func atomicWriteModeFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".launcher-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func darwinLauncherInstalledPath(dataDir string) string {
	appPath, err := DarwinLauncherAppPath()
	if err != nil {
		return ""
	}
	scriptPath := darwinLauncherExecutablePath(appPath)
	info, err := os.Stat(scriptPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o100 == 0 {
		return ""
	}
	if dataDir == "" {
		return appPath
	}
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		return ""
	}
	owner := launcherScriptDataDir(string(raw))
	if owner == "" || sameDataDir(owner, dataDir) {
		return appPath
	}
	return ""
}

func launcherScriptDataDir(script string) string {
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "DATA_DIR=") {
			continue
		}
		raw := strings.TrimPrefix(line, "DATA_DIR=")
		if len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'' {
			return strings.ReplaceAll(raw[1:len(raw)-1], `'\''`, `'`)
		}
		return strings.Trim(raw, `"`)
	}
	return ""
}

func runtimePort(dataDir string) int {
	if dataDir == "" {
		return 0
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, "runtime.json"))
	if err != nil {
		return 0
	}
	var record struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &record) != nil || record.URL == "" {
		return 0
	}
	parsed, err := url.Parse(record.URL)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return 0
	}
	return port
}

// defaultHandoffManagedCore ensures the user has ~/Applications/KimiCode+.app
// and launches a detached resident core through LaunchServices before launchd
// boots out the managed login item, keeping the current Kimi Code session alive.
func defaultHandoffManagedCore(config LaunchAgentConfig) error {
	if config.Profile != "" || config.DataDir == "" {
		return nil
	}
	if config.Executable == "" {
		config.Executable = managedCoreExecutable(config.DataDir)
	}
	if config.KimiHome == "" {
		if envHome := os.Getenv("KIMI_CODE_HOME"); envHome != "" {
			config.KimiHome = envHome
		} else if home, err := os.UserHomeDir(); err == nil {
			config.KimiHome = filepath.Join(home, ".kimi-code")
		}
	}
	appPath, err := InstallDarwinLauncherApp(config)
	if err != nil {
		return err
	}
	if port := runtimePort(config.DataDir); port > 0 {
		last := ReadAttachState(config.DataDir)
		state := updater.RestartState{Port: port}
		if last != nil && last["status"] == "installed" {
			state.Reattach = true
			state.ReattachDaily = last["daily"] == true
		}
		_ = updater.WriteRestartState(config.DataDir, state)
	}
	cmd := exec.Command("/usr/bin/open", "-g", "-j", "-n", "-a", appPath, "--args", "--handoff")
	if out, err := cmd.CombinedOutput(); err != nil {
		updater.ClearRestartState(config.DataDir)
		return fmt.Errorf("无法平滑交接增强核心：%w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
