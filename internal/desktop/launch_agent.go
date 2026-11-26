package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"enhancedkimi/internal/platform"

	"github.com/gofrs/flock"
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
	// OwnerDataDir names the data directory the installed login item belongs
	// to when it is not the directory being asked about.
	OwnerDataDir string `json:"ownerDataDir,omitempty"`
	Note         string `json:"note,omitempty"`
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

// LaunchdManagedCore reports whether the resident copy in dataDir is the login
// item launchd keeps alive. Only then must an update hand the candidate to
// launchd instead of starting a second detached --resident process. The loaded
// job's own program path is checked too: another installation may have a job
// with the same label, and handing the candidate to it would start the wrong
// binary.
func LaunchdManagedCore(dataDir string) bool {
	if !managedCoreRegistered(dataDir) {
		return false
	}
	path, err := LaunchAgentPath(managedCoreLabel)
	if err != nil {
		return false
	}
	if !launches.installed(path) || !launches.loaded(managedCoreLabel) {
		return false
	}
	program, err := loadedLaunchProgram(managedCoreLabel)
	if err != nil {
		return false
	}
	return program == "" || program == managedCoreExecutable(dataDir)
}

// loadedLaunchProgram reads the program path of a loaded job from launchctl.
func loadedLaunchProgram(label string) (string, error) {
	output, err := exec.Command("/bin/launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)).Output()
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "program = ") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, "program = ")), nil
	}
	return "", nil
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
	return LaunchAgentStatusForDataDir("")
}

// LaunchAgentStatusForDataDir reports the login items as they apply to one data
// directory. A plist that belongs to a different directory is not this core's
// login item, so the isolated directory reports it as disabled and says whose
// it is instead of claiming it is already enabled.
func LaunchAgentStatusForDataDir(dataDir string) LaunchAgentState {
	path, err := LaunchAgentPath(LaunchAgentLabel)
	if err != nil {
		return LaunchAgentState{Error: err.Error()}
	}
	corePath, _ := LaunchAgentPath(managedCoreLabel)
	legacyPath, _ := LaunchAgentPath(legacyCoreLabel)
	attached := launches.installed(path) && launches.loaded(LaunchAgentLabel)
	core := launches.installed(corePath) && launches.loaded(managedCoreLabel)
	state := LaunchAgentState{
		Enabled: attached || core, Path: path, CorePath: corePath,
		NeedsMigration:    attached != core,
		LegacyCorePresent: launches.installed(legacyPath) || launches.loaded(legacyCoreLabel),
	}
	if dataDir == "" || !state.Enabled {
		return state
	}
	owner, known := launchAgentOwner()
	if known && owner != "" && !sameDataDir(owner, dataDir) {
		state.Enabled = false
		state.OwnerDataDir = owner
		state.Note = "当前登录项属于其他数据目录（" + owner + "），未在此目录启用"
	}
	return state
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
		return LaunchAgentStatusForDataDir(config.DataDir), err
	}
	if _, err := replaceLaunchAgent(managedCoreLabel, managedCorePlist(config)); err != nil {
		err = errors.Join(err, undoAttach())
		return LaunchAgentStatusForDataDir(config.DataDir), err
	}
	return LaunchAgentStatusForDataDir(config.DataDir), nil
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
	return UninstallLaunchAgentForDataDir("")
}

// UninstallLaunchAgentForDataDir removes the login items only when they belong
// to dataDir. Disabling auto-attach from an isolated data directory must never
// delete the login item the main configuration relies on.
func UninstallLaunchAgentForDataDir(dataDir string) (LaunchAgentState, error) {
	if err := ensureLaunchAgentOwner(dataDir); err != nil {
		return LaunchAgentStatusForDataDir(dataDir), err
	}
	if err := removeLaunchAgent(LaunchAgentLabel); err != nil {
		return LaunchAgentStatusForDataDir(dataDir), err
	}
	if err := removeLaunchAgent(managedCoreLabel); err != nil {
		return LaunchAgentStatusForDataDir(dataDir), err
	}
	return LaunchAgentStatusForDataDir(dataDir), nil
}

// launchAgentOwner reads the data directory an installed plist belongs to. known
// is false when the plist records no data directory at all (an older install).
func launchAgentOwner() (owner string, known bool) {
	for _, label := range []string{LaunchAgentLabel, managedCoreLabel} {
		path, err := LaunchAgentPath(label)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if owner := plistDataDir(string(data)); owner != "" {
			return owner, true
		}
	}
	return "", false
}

func ensureLaunchAgentOwner(dataDir string) error {
	if dataDir == "" {
		return nil
	}
	owner, known := launchAgentOwner()
	if !known {
		return nil
	}
	if sameDataDir(owner, dataDir) {
		return nil
	}
	return errors.New("当前登录项属于其他数据目录，已拒绝从隔离目录移除")
}

func sameDataDir(left, right string) bool {
	if filepath.Clean(left) == filepath.Clean(right) {
		return true
	}
	resolvedLeft, leftErr := filepath.EvalSymlinks(left)
	resolvedRight, rightErr := filepath.EvalSymlinks(right)
	return leftErr == nil && rightErr == nil && resolvedLeft == resolvedRight
}

// plistDataDir resolves the data directory a rendered plist belongs to. The
// WorkingDirectory is authoritative; the --data-dir argument is the fallback for
// agents rendered without one.
func plistDataDir(plist string) string {
	if working := plistStringValue(plist, "WorkingDirectory"); working != "" {
		return filepath.Clean(working)
	}
	args := plistStringArray(plist, "ProgramArguments")
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "--data-dir" {
			return filepath.Clean(args[index+1])
		}
	}
	return ""
}

func plistStringValue(plist, key string) string {
	marker := "<key>" + key + "</key><string>"
	start := strings.Index(plist, marker)
	if start < 0 {
		return ""
	}
	rest := plist[start+len(marker):]
	end := strings.Index(rest, "</string>")
	if end < 0 {
		return ""
	}
	return unescapePlistText(rest[:end])
}

func plistStringArray(plist, key string) []string {
	marker := "<key>" + key + "</key><array>"
	start := strings.Index(plist, marker)
	if start < 0 {
		return nil
	}
	rest := plist[start+len(marker):]
	end := strings.Index(rest, "</array>")
	if end < 0 {
		return nil
	}
	rest = rest[:end]
	var values []string
	for {
		open := strings.Index(rest, "<string>")
		if open < 0 {
			return values
		}
		rest = rest[open+len("<string>"):]
		close := strings.Index(rest, "</string>")
		if close < 0 {
			return values
		}
		values = append(values, unescapePlistText(rest[:close]))
		rest = rest[close+len("</string>"):]
	}
}

func unescapePlistText(value string) string {
	replacer := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&")
	return replacer.Replace(value)
}

func removeLaunchAgent(label string) error {
	path, err := LaunchAgentPath(label)
	if err != nil {
		return err
	}
	self := label == managedCoreLabel && os.Getenv(managedCoreEnv) == "1"
	if !self && launches.loaded(label) {
		// Unload first: deleting the plist while the job is loaded leaves launchd
		// running a definition the user can no longer see or change.
		if err := launches.bootout(label); err != nil {
			return fmt.Errorf("无法卸载后台接入任务：%w", err)
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("无法移除后台接入定义：%w", err)
	}
	if self && launches.loaded(label) {
		// bootout stops the server itself. Let it finish serving this request.
		controller := launches
		time.AfterFunc(time.Second, func() {
			if err := controller.bootout(label); err != nil {
				fmt.Fprintln(os.Stderr, "无法卸载核心登录项：", err)
			}
		})
	}
	return nil
}

// AttachOnce bounds an attachment initiated by a background job or restart.
func AttachOnce(ctx context.Context, options AttachOptions) (*AttachResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	return AttachNative(ctx, options)
}

var attachHealthyInstanceAlive = defaultAttachHealthyInstanceAlive

func readInstalledAttachState(dataDir string) map[string]any {
	state := ReadAttachState(dataDir)
	if state == nil || state["status"] != "installed" {
		return nil
	}
	return state
}

func shouldPreserveInstalledAttachState(prev map[string]any, currentCore int) bool {
	if currentCore <= 0 || prev == nil || prev["status"] != "installed" {
		return false
	}
	prevCore, ok := attachStatePID(prev["corePid"])
	if !ok || prevCore != currentCore {
		return false
	}
	hostPid, ok := attachStatePID(prev["hostPid"])
	if !ok || hostPid <= 0 {
		return false
	}
	hostStarted, _ := prev["hostStarted"].(string)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return attachHealthyInstanceAlive(ctx, currentCore, hostPid, strings.TrimSpace(hostStarted))
}

func shouldPreserveInstalledAttach(dataDir string, currentCore int) bool {
	if currentCore <= 0 {
		return false
	}
	return shouldPreserveInstalledAttachState(ReadAttachState(dataDir), currentCore)
}

func invalidateInstalledAttachBeforeInjectUnlocked(dataDir string) error {
	if dataDir == "" {
		return nil
	}
	target := filepath.Join(dataDir, "attach-state.json")
	raw, readErr := os.ReadFile(target)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return nil
		}
	} else {
		var state map[string]any
		if json.Unmarshal(raw, &state) == nil && state != nil {
			if status, ok := state["status"].(string); ok && status != "" && status != "installed" {
				return nil
			}
		}
	}
	writeErr := writeAttachState(dataDir, map[string]any{
		"at":      time.Now().UTC().Format(time.RFC3339Nano),
		"status":  "attaching",
		"corePid": corePID(dataDir),
	})
	if writeErr == nil {
		return nil
	}
	removeErr := os.Remove(target)
	if removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
		return nil
	}
	if readErr != nil {
		return errors.Join(fmt.Errorf("读取旧接入状态文件失败：%w", readErr), writeErr, fmt.Errorf("移除旧接入状态文件失败：%w", removeErr))
	}
	return errors.Join(writeErr, fmt.Errorf("移除旧接入状态文件失败：%w", removeErr))
}

func defaultAttachHealthyInstanceAlive(ctx context.Context, corePid, hostPid int, hostStarted string) bool {
	if corePid <= 0 || hostPid <= 0 {
		return false
	}
	switch runtime.GOOS {
	case "darwin":
		if corePid != os.Getpid() {
			if _, err := ProcessIdentity(ctx, corePid); err != nil {
				return false
			}
		}
		info, err := ProcessIdentity(ctx, hostPid)
		if err != nil || info.PID != hostPid {
			return false
		}
		if hostStarted != "" {
			return info.Started == hostStarted
		}
		return slices.Contains(kimiExecutables(), info.Executable)
	case "windows":
		if hostStarted == "" {
			script := fmt.Sprintf(
				"$ErrorActionPreference='Stop';$c=Get-CimInstance Win32_Process -Filter 'ProcessId=%d';if(-not $c){exit 1};$h=Get-CimInstance Win32_Process -Filter 'ProcessId=%d';if(-not $h -or $h.Name -ine '%s'){exit 1}",
				corePid, hostPid, WindowsKimiExecutableName,
			)
			return exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", EncodeWindowsPowerShellCommand(script)).Run() == nil
		}
		script := fmt.Sprintf(
			"$ErrorActionPreference='Stop';$c=Get-CimInstance Win32_Process -Filter 'ProcessId=%d';if(-not $c){exit 1};$h=Get-CimInstance Win32_Process -Filter 'ProcessId=%d';if(-not $h){exit 1};if($h.CreationDate){Write-Output $h.CreationDate.ToUniversalTime().ToString('o')}",
			corePid, hostPid,
		)
		out, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", EncodeWindowsPowerShellCommand(script)).Output()
		if err != nil {
			return false
		}
		actualStarted := strings.TrimSpace(string(out))
		return hostStartedEqual(hostStarted, actualStarted)
	default:
		return false
	}
}

func hostStartedEqual(expected, actual string) bool {
	expected = strings.TrimSpace(expected)
	actual = strings.TrimSpace(actual)
	if expected == actual {
		return true
	}
	expectedTime, errA := time.Parse(time.RFC3339Nano, expected)
	actualTime, errB := time.Parse(time.RFC3339Nano, actual)
	return errA == nil && errB == nil && expectedTime.Truncate(time.Microsecond).Equal(actualTime.Truncate(time.Microsecond))
}

func tryAcquireHostAttachLockForFallback(dataDir string) (func(), bool, error) {
	if dataDir == "" {
		return func() {}, true, nil
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, false, err
	}
	gate := flock.New(filepath.Join(dataDir, hostAttachLockFile))
	ok, err := gate.TryLock()
	if err != nil {
		_ = gate.Close()
		return nil, false, err
	}
	if !ok {
		_ = gate.Close()
		return nil, false, nil
	}
	return func() { _ = gate.Close() }, true, nil
}

func buildAttachPayload(currentCore int, value any) map[string]any {
	payload := map[string]any{"at": time.Now().UTC().Format(time.RFC3339Nano), "corePid": currentCore}
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
	return payload
}

func recordAttachUnlocked(dataDir string, value any) error {
	if dataDir == "" {
		return nil
	}
	currentCore := corePID(dataDir)
	if err, ok := value.(error); ok && err != nil {
		if !isBridgeTornDown(err) && shouldPreserveInstalledAttach(dataDir, currentCore) {
			return nil
		}
	}
	return writeAttachState(dataDir, buildAttachPayload(currentCore, value))
}

// RecordAttach writes the last attach result next to the agent so the settings
// surface can explain why it is not attached without reading the log.
func RecordAttach(dataDir string, value any) error {
	if dataDir == "" {
		return nil
	}
	if result, ok := value.(*AttachResult); ok && result != nil && result.stateRecorded {
		return nil
	}
	if err, ok := value.(error); ok && err != nil && (isAttachStateRecorded(err) || isAttachPersistFailed(err)) {
		return nil
	}
	release, acquired, err := tryAcquireHostAttachLockForFallback(dataDir)
	if err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	defer release()
	return recordAttachUnlocked(dataDir, value)
}

// corePID lets a later reader tell whether the recorded state belongs to the
// core that is running now, instead of trusting a stale file.
func corePID(dataDir string) int {
	data, err := os.ReadFile(filepath.Join(dataDir, "runtime.json"))
	if err == nil {
		var record struct{ PID int }
		if json.Unmarshal(data, &record) == nil && record.PID > 0 {
			return record.PID
		}
	}
	return os.Getpid()
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
