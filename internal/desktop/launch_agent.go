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
)

// LaunchAgentLabel is the agent that re-attaches the enhancement after the
// official client restarts. It is user-scoped and disabled by default: attaching
// changes the client's interface, so the user has to turn it on explicitly.
const LaunchAgentLabel = "com.user.enhanced-kimi.attach"

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
	// CoreURL and PairingToken let the agent report to an already-running core.
	CoreURL      string
	PairingToken string
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

// LaunchAgentPlist renders the agent definition. WatchPaths makes it event
// driven, RunAtLoad covers the case where the client is already running when the
// user enables it, and ThrottleInterval keeps a burst of profile writes from
// spawning a storm of runs.
//
// The rendered command line must be exactly what `enhanced-kimi attach` accepts
// for a run. A launchd run has no other way to learn the core address or the
// pairing token, so leaving either out makes the agent fail on every wake while
// the settings surface still reports it as enabled. AttachArguments is the single
// source of truth for that contract and is exercised by tests against the real
// flag set.
func LaunchAgentPlist(config LaunchAgentConfig) (string, error) {
	if config.Executable == "" {
		return "", errors.New("缺少增强核心路径，无法安装后台接入")
	}
	if config.DataDir == "" {
		return "", errors.New("缺少增强数据目录，无法安装后台接入")
	}
	// The agent is useless without somewhere to report, so refuse to render an
	// agent that would only fail at wake-up time.
	if config.CoreURL == "" || len(config.PairingToken) < AttachTokenMinLength {
		return "", errors.New("后台接入需要核心地址和配对凭据，无法生成接入定义")
	}
	program := append([]string{config.Executable}, AttachArguments(config)...)
	arguments := strings.Builder{}
	for _, value := range program {
		arguments.WriteString("<string>" + xmlEscape(value) + "</string>")
	}
	watch := strings.Builder{}
	for _, path := range WatchPaths(config.KimiHome) {
		watch.WriteString("<string>" + xmlEscape(path) + "</string>")
	}
	logPath := filepath.Join(config.DataDir, "attach.log")
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array>%s</array>
<key>WatchPaths</key><array>%s</array>
<key>RunAtLoad</key><true/>
<key>ThrottleInterval</key><integer>5</integer>
<key>ProcessType</key><string>Background</string>
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, xmlEscape(LaunchAgentLabel), arguments.String(), watch.String(), xmlEscape(logPath), xmlEscape(logPath)), nil
}

// AttachTokenMinLength is the shortest pairing token the attach command accepts.
// The core issues 32+ byte tokens; a shorter value means the caller has none.
const AttachTokenMinLength = 32

// AttachArguments renders the attach subcommand arguments for one agent run. The
// plist and any programmatic installer share this function so the rendered
// command can never drift from the CLI contract.
func AttachArguments(config LaunchAgentConfig) []string {
	args := []string{"attach", "--data-dir", config.DataDir}
	if config.KimiHome != "" {
		args = append(args, "--kimi-home", config.KimiHome)
	}
	if config.Profile != "" {
		args = append(args, "--host-profile", config.Profile)
	}
	if config.AllowDailyInstance {
		args = append(args, "--allow-daily-instance")
	}
	// Rendered last so a token containing "=" or "-" cannot be misread as a flag.
	args = append(args, "--core-url", config.CoreURL, "--pairing-token", config.PairingToken)
	return args
}

// LaunchAgentState is what the settings surface reports (for macOS LaunchAgent
// and Windows fixed-path launcher + shortcut management).
type LaunchAgentState struct {
	Enabled       bool   `json:"enabled"`
	Path          string `json:"path,omitempty"`
	Error         string `json:"error,omitempty"`
	ShortcutCount int    `json:"shortcutCount,omitempty"`
	BackupDir     string `json:"backupDir,omitempty"`
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

// LaunchAgentStatus reports whether the agent is installed and loaded. Both must
// hold for the client to recover on its own after a restart.
func LaunchAgentStatus() LaunchAgentState {
	path, err := LaunchAgentPath(LaunchAgentLabel)
	if err != nil {
		return LaunchAgentState{Error: err.Error()}
	}
	installed := launches.installed(path)
	return LaunchAgentState{Enabled: installed && launches.loaded(LaunchAgentLabel), Path: path}
}

// InstallLaunchAgent writes the plist and loads it. The write is atomic so a
// crash mid-install cannot leave a half-written agent that launchd refuses.
func InstallLaunchAgent(config LaunchAgentConfig) (LaunchAgentState, error) {
	path, err := LaunchAgentPath(LaunchAgentLabel)
	if err != nil {
		return LaunchAgentState{}, err
	}
	plist, err := LaunchAgentPlist(config)
	if err != nil {
		return LaunchAgentState{}, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return LaunchAgentState{}, errors.New("无法创建 LaunchAgents 目录")
	}
	tmp := path + ".next"
	if err = os.WriteFile(tmp, []byte(plist), 0o600); err != nil {
		return LaunchAgentState{}, fmt.Errorf("无法写入后台接入定义：%w", err)
	}
	if err = os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return LaunchAgentState{}, fmt.Errorf("无法安装后台接入：%w", err)
	}
	// A previous load must be removed first, otherwise bootstrap reports EEXIST.
	if launches.loaded(LaunchAgentLabel) {
		_ = launches.bootout(LaunchAgentLabel)
	}
	if err = launches.bootstrap(path); err != nil {
		return LaunchAgentState{Path: path}, fmt.Errorf("后台接入已写入，但 launchd 未能加载：%w", err)
	}
	return LaunchAgentState{Enabled: true, Path: path}, nil
}

// UninstallLaunchAgent removes the agent and its plist. Removal is idempotent so
// a repeated request from the settings surface is harmless.
func UninstallLaunchAgent() (LaunchAgentState, error) {
	path, err := LaunchAgentPath(LaunchAgentLabel)
	if err != nil {
		return LaunchAgentState{}, err
	}
	if launches.loaded(LaunchAgentLabel) {
		_ = launches.bootout(LaunchAgentLabel)
	}
	if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return LaunchAgentState{Path: path}, errors.New("无法移除后台接入定义")
	}
	return LaunchAgentState{Enabled: false, Path: path}, nil
}

// AttachOnce is the work the agent performs per launchd event. It is idempotent:
// the bootstrap already replaces a previous hook, and a second run on an
// already-attached client is a no-op re-install rather than a second injection.
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
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, "attach-state.json"), data, 0o600)
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
