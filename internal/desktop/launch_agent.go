package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"enhancedkimi/internal/platform"
)

const (
	LaunchAgentLabel = "com.user.enhanced-kimi.attach"
	managedCoreLabel = "com.user.enhanced-kimi.core-managed"
	legacyCoreLabel  = "com.user.enhanced-kimi.core"
	managedCoreEnv   = "KIMICODE_PLUS_MANAGED_CORE"
)

// WatchPaths are the directories launchd watches. The official client writes
// into its profile continuously while running and stops on exit, so a change
// notification is a real lifecycle event rather than a poll. The instance record
// and token live in KIMI_CODE_HOME, which is rewritten on every launch — that is
// the edit that re-triggers the agent after a restart.
func WatchPaths(home string) []string {
	paths := []string{}
	if home != "" {
		paths = append(paths,
			filepath.Join(home, "server", "instances"),
			filepath.Join(home, "server.token"),
		)
	}
	return paths
}

type LaunchAgentConfig struct {
	// DataDir holds the agent's state and log.
	DataDir string
	// KimiHome is the KIMI_CODE_HOME the core reads instance records from.
	KimiHome string
	// Profile narrows discovery to one isolated instance; empty means the running
	// client, which requires AllowDailyInstance.
	Profile            string
	AllowDailyInstance bool
	// Executable is the core binary the agent re-runs.
	Executable string
}

// launchAgentDir overrides where the plist lives. Tests point it at a temporary
// directory so no test can install an agent for the user running the suite.
var launchAgentDir = ""

// LaunchAgentPath is where the plist is installed for the current user.
func LaunchAgentPath(label string) (string, error) {
	if launchAgentDir != "" {
		return filepath.Join(launchAgentDir, label+".plist"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return replacer.Replace(value)
}

func LaunchAgentPlist(config LaunchAgentConfig) (string, error) {
	if config.Executable == "" || config.DataDir == "" {
		return "", errors.New("缺少增强核心路径或数据目录，无法安装后台接入")
	}
	var watch strings.Builder
	paths := append(WatchPaths(config.KimiHome), filepath.Join(config.DataDir, "runtime.json"))
	for _, path := range paths {
		watch.WriteString("<string>" + xmlEscape(path) + "</string>")
	}
	settings := "<key>WatchPaths</key><array>" + watch.String() + "</array>"
	return renderLaunchAgent(config, LaunchAgentLabel, AttachArguments(config), "attach.log", settings), nil
}

func managedCorePlist(config LaunchAgentConfig) string {
	args := append([]string{"--resident"}, launchHostArguments(config)...)
	settings := "<key>KeepAlive</key><true/>\n<key>EnvironmentVariables</key><dict><key>" + managedCoreEnv + "</key><string>1</string></dict>"
	return renderLaunchAgent(config, managedCoreLabel, args, "resident-core.log", settings)
}

func renderLaunchAgent(config LaunchAgentConfig, label string, args []string, logName, settings string) string {
	var arguments strings.Builder
	for _, value := range append([]string{config.Executable}, args...) {
		arguments.WriteString("<string>" + xmlEscape(value) + "</string>")
	}
	logPath := xmlEscape(filepath.Join(config.DataDir, logName))
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array>%s</array>
%s
<key>RunAtLoad</key><true/>
<key>ThrottleInterval</key><integer>5</integer>
<key>ProcessType</key><string>Background</string>
<key>WorkingDirectory</key><string>%s</string>
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, xmlEscape(label), arguments.String(), settings, xmlEscape(config.DataDir), logPath, logPath)
}

// AttachTokenMinLength is the shortest pairing token the attach command accepts.
// The core issues 32+ byte tokens; a shorter value means the caller has none.
const AttachTokenMinLength = 32

// AttachArguments renders the attach subcommand arguments for one agent run. The
// plist and any programmatic installer share this function so the rendered
// command can never drift from the CLI contract.
func AttachArguments(config LaunchAgentConfig) []string {
	args := append([]string{"attach", "--background"}, launchHostArguments(config)...)
	if config.AllowDailyInstance {
		args = append(args, "--allow-daily-instance")
	}
	return args
}

func launchHostArguments(config LaunchAgentConfig) []string {
	args := []string{"--data-dir", config.DataDir}
	if config.KimiHome != "" {
		args = append(args, "--kimi-home", config.KimiHome)
	}
	if config.Profile != "" {
		args = append(args, "--host-profile", config.Profile)
	}
	return args
}

type LaunchAgentState struct {
	Enabled           bool   `json:"enabled"`
	Path              string `json:"path,omitempty"`
	CorePath          string `json:"corePath,omitempty"`
	Error             string `json:"error,omitempty"`
	ShortcutCount     int    `json:"shortcutCount,omitempty"`
	BackupDir         string `json:"backupDir,omitempty"`
	NeedsMigration    bool   `json:"needsMigration,omitempty"`
	LegacyCorePresent bool   `json:"legacyCorePresent,omitempty"`
}

type launchController interface {
	installed(path string) bool
	loaded(label string) bool
	bootstrap(plist string) error
	bootout(label string) error
}

type systemLaunch struct{}

func (systemLaunch) installed(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (systemLaunch) loaded(label string) bool {
	domain := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
	err := exec.Command("/bin/launchctl", "print", domain).Run()
	return err == nil
}

func (systemLaunch) bootstrap(plist string) error {
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	return exec.Command("/bin/launchctl", "bootstrap", domain, plist).Run()
}

func (systemLaunch) bootout(label string) error {
	domain := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
	return exec.Command("/bin/launchctl", "bootout", domain).Run()
}

var launches launchController = systemLaunch{}

func managedCoreExecutable(dataDir string) string {
	return filepath.Join(dataDir, "resident", "enhanced-kimi")
}

func managedCoreRegistered(dataDir string) bool {
	if !filepath.IsAbs(dataDir) {
		return false
	}
	path, err := LaunchAgentPath(managedCoreLabel)
	if err != nil {
		return false
	}
	data, err := os.ReadFile(path)
	prefix := "<key>ProgramArguments</key><array><string>" + xmlEscape(managedCoreExecutable(dataDir)) + "</string>"
	return err == nil && strings.Contains(string(data), prefix)
}

func LaunchAgentStatus() LaunchAgentState {
	path, err := LaunchAgentPath(LaunchAgentLabel)
	if err != nil {
		return LaunchAgentState{Error: err.Error()}
	}
	corePath, _ := LaunchAgentPath(managedCoreLabel)
	legacyPath, _ := LaunchAgentPath(legacyCoreLabel)
	attached := launches.installed(path) && launches.loaded(LaunchAgentLabel)
	core := launches.installed(corePath) && launches.loaded(managedCoreLabel)
	return LaunchAgentState{
		Enabled: attached || core, Path: path, CorePath: corePath,
		NeedsMigration:    attached != core,
		LegacyCorePresent: launches.installed(legacyPath) || launches.loaded(legacyCoreLabel),
	}
}

func InstallLaunchAgent(config LaunchAgentConfig) (LaunchAgentState, error) {
	plist, err := LaunchAgentPlist(config)
	if err != nil {
		return LaunchAgentState{}, err
	}
	if err := os.MkdirAll(config.DataDir, 0o700); err != nil {
		return LaunchAgentState{}, err
	}
	undoAttach, err := replaceLaunchAgent(LaunchAgentLabel, plist)
	if err != nil {
		return LaunchAgentStatus(), err
	}
	if _, err := replaceLaunchAgent(managedCoreLabel, managedCorePlist(config)); err != nil {
		err = errors.Join(err, undoAttach())
		return LaunchAgentStatus(), err
	}
	return LaunchAgentStatus(), nil
}

func replaceLaunchAgent(label, plist string) (func() error, error) {
	path, err := LaunchAgentPath(label)
	if err != nil {
		return nil, err
	}
	previous, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	existed := err == nil
	loaded := launches.loaded(label)
	if loaded && string(previous) == plist {
		return func() error { return nil }, nil
	}
	if loaded && label == managedCoreLabel && os.Getenv(managedCoreEnv) == "1" {
		return nil, errors.New("请先关闭旧的后台接入，再从更新后的核心重新开启")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := platform.AtomicPrivateFile(path, []byte(plist)); err != nil {
		return nil, fmt.Errorf("无法写入后台接入定义：%w", err)
	}
	restore := func() error {
		if existed {
			return platform.AtomicPrivateFile(path, previous)
		}
		return os.Remove(path)
	}
	reload := func() error {
		if err := restore(); err != nil || !loaded || !existed {
			return err
		}
		return launches.bootstrap(path)
	}
	if loaded {
		if err := launches.bootout(label); err != nil {
			return nil, errors.Join(fmt.Errorf("无法卸载旧后台接入任务：%w", err), restore())
		}
	}
	if err := launches.bootstrap(path); err != nil {
		return nil, errors.Join(fmt.Errorf("无法加载后台接入任务：%w", err), reload())
	}
	return func() error {
		if err := launches.bootout(label); err != nil {
			return err
		}
		return reload()
	}, nil
}

func UninstallLaunchAgent() (LaunchAgentState, error) {
	if err := removeLaunchAgent(LaunchAgentLabel); err != nil {
		return LaunchAgentStatus(), err
	}
	if err := removeLaunchAgent(managedCoreLabel); err != nil {
		return LaunchAgentStatus(), err
	}
	return LaunchAgentStatus(), nil
}

func removeLaunchAgent(label string) error {
	path, err := LaunchAgentPath(label)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("无法移除后台接入定义：%w", err)
	}
	if !launches.loaded(label) {
		return nil
	}
	if label == managedCoreLabel && os.Getenv(managedCoreEnv) == "1" {
		// bootout stops the server itself. Let it finish serving this request.
		controller := launches
		time.AfterFunc(time.Second, func() {
			if err := controller.bootout(label); err != nil {
				fmt.Fprintln(os.Stderr, "无法卸载核心登录项：", err)
			}
		})
		return nil
	}
	return launches.bootout(label)
}

// AttachOnce bounds an attachment initiated by a background job or restart.
func AttachOnce(ctx context.Context, options AttachOptions) (*AttachResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	return AttachNative(ctx, options)
}

// RecordAttach writes the last attach result next to the agent so the settings
// surface can explain why it is not attached without reading the log.
func RecordAttach(dataDir string, value any) error {
	if dataDir == "" {
		return nil
	}
	payload := map[string]any{"at": time.Now().UTC().Format(time.RFC3339Nano)}
	if result, ok := value.(*AttachResult); ok && result != nil {
		payload["status"] = result.Status
		payload["hostPid"] = result.HostPID
		payload["appVersion"] = result.AppVersion
		payload["electron"] = result.Electron
		payload["verified"] = result.Verified
		if result.Transport != "" {
			payload["transport"] = result.Transport
		}
		if result.hostStarted != "" {
			payload["hostStarted"] = result.hostStarted
		}
		if result.attachKey != "" {
			payload["attachKey"] = result.attachKey
		}
		if result.VersionNote != "" {
			payload["versionNote"] = result.VersionNote
		}
		if result.Platform != "" {
			payload["platform"] = result.Platform
		}
		if result.DebugPort > 0 {
			payload["debugPort"] = result.DebugPort
		}
		if result.DebugPortWarning != "" {
			payload["debugPortWarning"] = result.DebugPortWarning
		}
		if result.MainBridgeWarning != "" {
			payload["mainBridgeWarning"] = result.MainBridgeWarning
		}
		payload["daily"] = result.Daily
	} else if err, ok := value.(error); ok && err != nil {
		payload["status"] = "error"
		payload["error"] = err.Error()
		payload["consentRequired"] = errors.Is(err, ErrConsentRequired)
	}
	return writeAttachState(dataDir, payload)
}

func writeAttachState(dataDir string, payload map[string]any) error {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return platform.AtomicPrivateFile(filepath.Join(dataDir, "attach-state.json"), data)
}

// ReadAttachState returns the last recorded attach outcome, or nil when the
// agent has not run yet.
func ReadAttachState(dataDir string) map[string]any {
	if dataDir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dataDir, "attach-state.json"))
	if err != nil {
		return nil
	}
	var value map[string]any
	if json.Unmarshal(data, &value) != nil {
		return nil
	}
	return value
}
