//go:build darwin

package updater

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// launchdController drives launchd through launchctl and reports the job's
// current process so the supervisor can prove the candidate belongs to it.
type launchdController interface {
	kickstart(label string) error
	bootstrap(plist string) error
	bootout(label string) error
	servicePID(label string) (int, error)
	kill(label, signal string) error
}

type launchdSystem struct{}

var launchdRunner launchdController = launchdSystem{}

// launchdDefaultLabel is the login item that keeps the resident core alive. It
// is also the fallback when Options.Launchd does not name one.
const launchdDefaultLabel = "com.user.enhanced-kimi.core-managed"

// launchdStopTimeout bounds how long a rollback waits for the candidate process
// to disappear before restoring files.
const launchdStopTimeout = 10 * time.Second

func launchdDomain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func launchdService(label string) string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
}

func (launchdSystem) kickstart(label string) error {
	if err := exec.Command("/bin/launchctl", "kickstart", "-k", launchdService(label)).Run(); err == nil {
		return nil
	}
	// `kickstart -k` fails when the job is no longer loaded. Rebuild the job from
	// the plist so the update still completes after an unload.
	path, err := launchAgentPlistPath(label)
	if err != nil {
		return err
	}
	if err := exec.Command("/bin/launchctl", "bootstrap", launchdDomain(), path).Run(); err != nil {
		return fmt.Errorf("无法重新加载登录项：%w", err)
	}
	return exec.Command("/bin/launchctl", "kickstart", "-k", launchdService(label)).Run()
}

func (launchdSystem) bootstrap(plist string) error {
	return exec.Command("/bin/launchctl", "bootstrap", launchdDomain(), plist).Run()
}

func (launchdSystem) bootout(label string) error {
	return exec.Command("/bin/launchctl", "bootout", launchdService(label)).Run()
}

// servicePID reads the job's current process from `launchctl print`. A job that
// is loaded but not running reports pid 0 without an error.
func (launchdSystem) servicePID(label string) (int, error) {
	output, err := exec.Command("/bin/launchctl", "print", launchdService(label)).Output()
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "pid = ") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "pid = ")))
		if err != nil {
			return 0, err
		}
		return pid, nil
	}
	return 0, nil
}

func (launchdSystem) kill(label, signal string) error {
	return exec.Command("/bin/launchctl", "kill", signal, launchdService(label)).Run()
}

func launchAgentPlistPath(label string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

// defaultLaunchdManaged reports whether the installed login item runs the
// managed copy in this data directory. The plist is the authority: it is what
// launchd re-runs on KeepAlive.
func defaultLaunchdManaged(dataDir, targetExe string) bool {
	if dataDir == "" || targetExe == "" || !filepath.IsAbs(targetExe) {
		return false
	}
	expected := filepath.Join(dataDir, "resident", "enhanced-kimi")
	if filepath.Clean(targetExe) != filepath.Clean(expected) {
		return false
	}
	path, err := launchAgentPlistPath(launchdDefaultLabel)
	if err != nil {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	plist := string(data)
	if !strings.Contains(plist, "<key>ProgramArguments</key><array><string>"+xmlEscapeUpdate(expected)+"</string>") {
		return false
	}
	return plistDataDir(plist, dataDir)
}

func xmlEscapeUpdate(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return replacer.Replace(value)
}

// plistDataDir verifies the plist belongs to dataDir through WorkingDirectory or
// the --data-dir argument.
func plistDataDir(plist, dataDir string) bool {
	if working := plistValue(plist, "WorkingDirectory"); working != "" {
		if filepath.Clean(working) == filepath.Clean(dataDir) {
			return true
		}
	}
	args := plistArray(plist, "ProgramArguments")
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "--data-dir" {
			return filepath.Clean(args[index+1]) == filepath.Clean(dataDir)
		}
	}
	return false
}

func plistValue(plist, key string) string {
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
	return unescapePlist(rest[:end])
}

func plistArray(plist, key string) []string {
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
		values = append(values, unescapePlist(rest[:close]))
		rest = rest[close+len("</string>"):]
	}
}

func unescapePlist(value string) string {
	replacer := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&")
	return replacer.Replace(value)
}

// prepareRestartMarker clears the stale runtime record and persists the restart
// marker the candidate reads. It runs before launchd is asked for a process, so
// the health check can never see the previous core's record.
func (job supervisorJob) prepareRestartMarker() error {
	if err := writeUpdateJSON(filepath.Join(job.DataDir, restartName), job.Restart); err != nil {
		return fmt.Errorf("无法保存核心重启状态：%w", err)
	}
	return removeUpdateFile(filepath.Join(job.DataDir, "runtime.json"))
}

func defaultKickstart(label string) error { return launchdRunner.kickstart(label) }

func defaultLaunchdKill(label, signal string) error { return launchdRunner.kill(label, signal) }

func defaultLaunchdBootout(label string) error { return launchdRunner.bootout(label) }

func defaultLaunchdServicePID(label string) (int, error) { return launchdRunner.servicePID(label) }

var launchdProcessAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func runtimeCorePID(dataDir string) int {
	data, err := os.ReadFile(filepath.Join(dataDir, "runtime.json"))
	if err != nil {
		return 0
	}
	var runtime struct {
		PID int `json:"pid"`
	}
	if json.Unmarshal(data, &runtime) != nil || runtime.PID <= 0 {
		return 0
	}
	return runtime.PID
}

// stopLaunchdCore terminates the job's current process and waits for both the
// launchd job and the captured process to disappear, so restored files cannot be
// replaced while the old candidate is still closing its databases. The
// job is unloaded as well: with KeepAlive=true launchd would otherwise start the
// unhealthy candidate again while the previous release is being restored. The
// plist stays installed, so the later kickstart reloads the same definition.
func (job supervisorJob) stopLaunchdCore() error {
	pid, err := job.launchdServicePID()
	if err != nil || pid <= 0 {
		pid = runtimeCorePID(job.DataDir)
	}
	_ = job.launchdKill("SIGTERM")
	_ = job.launchdBootout()
	deadline := time.Now().Add(launchdStopTimeout)
	escalated := false
	for time.Now().Before(deadline) {
		servicePID, serviceErr := job.launchdServicePID()
		processStopped := pid <= 0 || !launchdProcessAlive(pid)
		if (serviceErr != nil || servicePID <= 0) && processStopped {
			return nil
		}
		// A candidate core that deadlocks on a database lock ignores SIGTERM.
		// Escalate once to SIGKILL so rollback is never blocked forever.
		if !escalated && time.Until(deadline) < launchdStopTimeout/2 {
			escalated = true
			_ = job.launchdKill("SIGKILL")
			if pid > 1 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("等待登录项核心退出超时")
}

func stopDetachedProcess(pid int) error {
	if pid <= 0 {
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
