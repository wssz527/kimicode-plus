package desktop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeLaunch struct {
	installedPaths map[string]bool
	loadedLabels   map[string]bool
	bootstrapped   []string
	bootedOut      []string
	bootstrapErr   error
}

func (f *fakeLaunch) installed(path string) bool { return f.installedPaths[path] }
func (f *fakeLaunch) loaded(label string) bool   { return f.loadedLabels[label] }
func (f *fakeLaunch) bootstrap(plist string) error {
	f.bootstrapped = append(f.bootstrapped, plist)
	if f.bootstrapErr != nil {
		return f.bootstrapErr
	}
	f.installedPaths[plist] = true
	return nil
}
func (f *fakeLaunch) bootout(label string) error {
	f.bootedOut = append(f.bootedOut, label)
	delete(f.loadedLabels, label)
	return nil
}

func withFakeLaunch(t *testing.T) *fakeLaunch {
	t.Helper()
	fake := &fakeLaunch{installedPaths: map[string]bool{}, loadedLabels: map[string]bool{}}
	previous := launches
	launches = fake
	t.Cleanup(func() { launches = previous })
	return fake
}

func TestLaunchAgentPlistIsEventDrivenNotPolling(t *testing.T) {
	plist, err := LaunchAgentPlist(LaunchAgentConfig{
		DataDir:    "/tmp/ekd",
		KimiHome:   "/tmp/kimi-home",
		Profile:    "/tmp/isolated",
		Executable: "/tmp/ekd/enhanced-kimi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plist, "<key>WatchPaths</key>") {
		t.Fatal("agent is not watch-driven")
	}
	if strings.Contains(plist, "<key>StartInterval</key>") {
		t.Fatal("agent polls on a timer")
	}
	if !strings.Contains(plist, "<key>RunAtLoad</key><true/>") {
		t.Fatal("agent does not run at load")
	}
	if !strings.Contains(plist, "<key>ThrottleInterval</key><integer>5</integer>") {
		t.Fatal("agent has no throttle")
	}
	for _, path := range WatchPaths("/tmp/kimi-home") {
		if !strings.Contains(plist, path) {
			t.Fatalf("watch path missing: %s", path)
		}
	}
	// The program must run the attach subcommand, not the whole server.
	if !strings.Contains(plist, "<string>attach</string>") {
		t.Fatal("agent does not run the attach subcommand")
	}
	if !strings.Contains(plist, "<string>--host-profile</string>") {
		t.Fatal("isolated profile not passed to the agent")
	}
	if strings.Contains(plist, "--allow-daily-instance") {
		t.Fatal("agent silently allowed the daily instance")
	}
}

func TestLaunchAgentPlistRequiresTheDailyConsentFlag(t *testing.T) {
	plist, err := LaunchAgentPlist(LaunchAgentConfig{DataDir: "/tmp/ekd", Executable: "/tmp/ekd/enhanced-kimi", AllowDailyInstance: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plist, "<string>--allow-daily-instance</string>") {
		t.Fatal("explicit daily consent was not carried to the agent")
	}
	if strings.Contains(plist, "--host-profile") {
		t.Fatal("daily agent carried a stray profile flag")
	}
}

func TestLaunchAgentPlistEscapesValues(t *testing.T) {
	plist, err := LaunchAgentPlist(LaunchAgentConfig{DataDir: `/tmp/a&b<c>"d"`, Executable: "/tmp/x"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plist, "a&b<c>") {
		t.Fatal("path was not XML-escaped")
	}
	if !strings.Contains(plist, "a&amp;b&lt;c&gt;") {
		t.Fatal("escaped path missing")
	}
}

func TestLaunchAgentPlistRefusesMissingExecutable(t *testing.T) {
	if _, err := LaunchAgentPlist(LaunchAgentConfig{DataDir: "/tmp/ekd"}); err == nil {
		t.Fatal("agent built without an executable")
	}
}

func TestLaunchAgentStatusRequiresInstalledAndLoaded(t *testing.T) {
	fake := withFakeLaunch(t)
	path, err := LaunchAgentPath(LaunchAgentLabel)
	if err != nil {
		t.Fatal(err)
	}
	if state := LaunchAgentStatus(); state.Enabled {
		t.Fatal("agent reported enabled before installation")
	}
	fake.installedPaths[path] = true
	if state := LaunchAgentStatus(); state.Enabled {
		t.Fatal("an installed-but-unloaded agent reported enabled")
	}
	fake.loadedLabels[LaunchAgentLabel] = true
	if state := LaunchAgentStatus(); !state.Enabled {
		t.Fatal("installed and loaded agent reported disabled")
	}
}

func TestInstallLaunchAgentIsAtomicAndReloads(t *testing.T) {
	fake := withFakeLaunch(t)
	path, err := LaunchAgentPath(LaunchAgentLabel)
	if err != nil {
		t.Fatal(err)
	}
	fake.loadedLabels[LaunchAgentLabel] = true
	// A pre-existing plist must survive a failed load so the user is not left
	// with neither the old nor the new agent.
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		_ = os.WriteFile(path, []byte("old"), 0o600)
	}
	fake.bootstrapErr = errors.New("synthetic launchd failure")
	state, err := InstallLaunchAgent(LaunchAgentConfig{DataDir: t.TempDir(), Executable: "/tmp/ekd/enhanced-kimi"})
	if err == nil {
		t.Fatal("synthetic launchd failure was ignored")
	}
	if state.Path != path {
		t.Fatalf("failure did not report the plist path: %+v", state)
	}
	if len(fake.bootedOut) != 1 || fake.bootedOut[0] != LaunchAgentLabel {
		t.Fatalf("previous job was not unloaded before bootstrap: %v", fake.bootedOut)
	}
	if len(fake.bootstrapped) != 1 {
		t.Fatalf("bootstrap attempts: %v", fake.bootstrapped)
	}
	// No temporary file may be left behind.
	if _, err = os.Stat(path + ".next"); err == nil {
		t.Fatal("temporary plist survived a failed install")
	}
	if data, readErr := os.ReadFile(path); readErr != nil || !strings.Contains(string(data), "WatchPaths") {
		t.Fatal("the new plist was not written atomically over the old one")
	}
	t.Cleanup(func() { _ = os.Remove(path) })
}

func TestUninstallLaunchAgentIsIdempotent(t *testing.T) {
	fake := withFakeLaunch(t)
	path, err := LaunchAgentPath(LaunchAgentLabel)
	if err != nil {
		t.Fatal(err)
	}
	fake.loadedLabels[LaunchAgentLabel] = true
	if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		_ = os.WriteFile(path, []byte("plist"), 0o600)
	}
	state, err := UninstallLaunchAgent()
	if err != nil || state.Enabled {
		t.Fatalf("uninstall failed: %+v %v", state, err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("plist was not removed")
	}
	// A second call is harmless.
	if _, err = UninstallLaunchAgent(); err != nil {
		t.Fatalf("second uninstall failed: %v", err)
	}
}

func TestRecordAttachExplainsConsentRequirement(t *testing.T) {
	dir := t.TempDir()
	if err := RecordAttach(dir, ErrConsentRequired); err != nil {
		t.Fatal(err)
	}
	state := ReadAttachState(dir)
	if state == nil || state["status"] != "error" {
		t.Fatalf("consent failure not recorded: %+v", state)
	}
	if state["consentRequired"] != true {
		t.Fatalf("consent requirement not flagged: %+v", state)
	}
	if err := RecordAttach(dir, &AttachResult{Status: "installed", HostPID: 42, AppVersion: "1.3.5"}); err != nil {
		t.Fatal(err)
	}
	state = ReadAttachState(dir)
	if state["status"] != "installed" || state["hostPid"] != float64(42) {
		t.Fatalf("successful attach not recorded: %+v", state)
	}
	if state["daily"] != false {
		t.Fatalf("daily flag invented: %+v", state)
	}
}

func TestDiscoverHostReportsWithoutAttaching(t *testing.T) {
	// No Kimi Code instance is running in the test environment, so discovery must
	// report that plainly instead of failing the call.
	value, err := DiscoverHost(context.Background(), "/tmp/definitely-absent-profile")
	if err != nil {
		t.Fatalf("discovery refused to report: %v", err)
	}
	if value["running"] != false {
		t.Fatalf("discovery invented a running host: %+v", value)
	}
	if value["reason"] == nil || value["reason"] == "" {
		t.Fatal("discovery did not explain why nothing was found")
	}
}

func TestWatchPathsCoverTheInstanceRecord(t *testing.T) {
	paths := WatchPaths("/tmp/kimi-home")
	joined := strings.Join(paths, "\n")
	if !strings.Contains(joined, filepath.Join("server", "instances")) {
		t.Fatal("instance records are not watched")
	}
	if !strings.Contains(joined, "server.token") {
		t.Fatal("the token file is not watched")
	}
	if len(WatchPaths("")) != 0 {
		t.Fatal("an empty home produced watch paths")
	}
}
