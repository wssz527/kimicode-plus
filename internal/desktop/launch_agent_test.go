package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	f.loadedLabels[strings.TrimSuffix(filepath.Base(plist), ".plist")] = true
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
	previousDir := launchAgentDir
	launches = fake
	launchAgentDir = t.TempDir()
	t.Cleanup(func() { launches = previous; launchAgentDir = previousDir })
	return fake
}

func testAgentConfig() LaunchAgentConfig {
	return LaunchAgentConfig{
		DataDir:    "/tmp/ekd",
		KimiHome:   "/tmp/kimi-home",
		Profile:    "/tmp/isolated",
		Executable: "/tmp/ekd/enhanced-kimi",
	}
}

func TestLaunchAgentPlistIsEventDrivenNotPolling(t *testing.T) {
	plist, err := LaunchAgentPlist(testAgentConfig())
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
	config := testAgentConfig()
	config.Profile = ""
	config.AllowDailyInstance = true
	plist, err := LaunchAgentPlist(config)
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
	config := testAgentConfig()
	config.DataDir = `/tmp/a&b<c>"d"`
	config.Executable = "/tmp/x"
	plist, err := LaunchAgentPlist(config)
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
	config := testAgentConfig()
	config.Executable = ""
	if _, err := LaunchAgentPlist(config); err == nil {
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
	if state := LaunchAgentStatus(); !state.Enabled || !state.NeedsMigration {
		t.Fatalf("legacy attach-only install was not recognized: %+v", state)
	}
	corePath, _ := LaunchAgentPath(managedCoreLabel)
	fake.installedPaths[corePath], fake.loadedLabels[managedCoreLabel] = true, true
	if state := LaunchAgentStatus(); !state.Enabled || state.NeedsMigration {
		t.Fatalf("paired agents did not report ready: %+v", state)
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
	config := testAgentConfig()
	config.DataDir = t.TempDir()
	state, err := InstallLaunchAgent(config)
	if err == nil {
		t.Fatal("synthetic launchd failure was ignored")
	}
	if state.Path != path {
		t.Fatalf("failure did not report the plist path: %+v", state)
	}
	if len(fake.bootedOut) != 1 || fake.bootedOut[0] != LaunchAgentLabel {
		t.Fatalf("previous job was not unloaded before bootstrap: %v", fake.bootedOut)
	}
	if len(fake.bootstrapped) != 2 {
		t.Fatalf("expected new load and rollback load attempts: %v", fake.bootstrapped)
	}
	if data, readErr := os.ReadFile(path); readErr != nil || string(data) != "old" {
		t.Fatalf("failed load did not restore the old plist: %q, %v", data, readErr)
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
	legacyPath, _ := LaunchAgentPath(legacyCoreLabel)
	if err := os.WriteFile(legacyPath, []byte("manual"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake.loadedLabels[legacyCoreLabel] = true
	corePath, _ := LaunchAgentPath(managedCoreLabel)
	if err := os.WriteFile(corePath, []byte("managed"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake.loadedLabels[managedCoreLabel] = true
	state, err := UninstallLaunchAgent()
	if err != nil || state.Enabled {
		t.Fatalf("uninstall failed: %+v %v", state, err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("plist was not removed")
	}
	if _, err := os.Stat(corePath); !errors.Is(err, os.ErrNotExist) || fake.loadedLabels[managedCoreLabel] {
		t.Fatal("managed core login item was not removed")
	}
	if data, err := os.ReadFile(legacyPath); err != nil || string(data) != "manual" || !fake.loadedLabels[legacyCoreLabel] {
		t.Fatal("manual core login item was changed")
	}
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
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		if value != nil || !errors.Is(err, ErrUnsupportedPlatform) {
			t.Fatalf("discovery on %s returned %+v, %v", runtime.GOOS, value, err)
		}
		return
	}
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

// Disabling auto-attach from an isolated data directory must never delete the
// login item the main configuration relies on.
func TestDisableAutoAttachRefusesAnotherDataDirectory(t *testing.T) {
	fake := withFakeLaunch(t)
	mainDir := t.TempDir()
	isolated := t.TempDir()
	config := testAgentConfig()
	config.DataDir = mainDir
	config.Executable = filepath.Join(mainDir, "enhanced-kimi")
	if _, err := InstallLaunchAgent(config); err != nil {
		t.Fatal(err)
	}
	path, err := LaunchAgentPath(LaunchAgentLabel)
	if err != nil {
		t.Fatal(err)
	}
	corePath, err := LaunchAgentPath(managedCoreLabel)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	state, err := UninstallLaunchAgentForDataDir(isolated)
	if err == nil || !strings.Contains(err.Error(), "其他数据目录") {
		t.Fatalf("isolated uninstall was not refused: %+v %v", state, err)
	}
	if state.Enabled || state.OwnerDataDir != filepath.Clean(mainDir) {
		t.Fatalf("isolated status did not report the real owner: %+v", state)
	}
	if after, readErr := os.ReadFile(path); readErr != nil || string(after) != string(before) {
		t.Fatalf("main plist changed after a refused uninstall: %v", readErr)
	}
	if !fake.loadedLabels[LaunchAgentLabel] || !fake.loadedLabels[managedCoreLabel] {
		t.Fatalf("main login items were unloaded: %+v", fake.loadedLabels)
	}
	if _, statErr := os.Stat(corePath); statErr != nil {
		t.Fatalf("managed core plist was removed: %v", statErr)
	}
	// The owning directory can still remove them.
	if _, err := UninstallLaunchAgentForDataDir(mainDir); err != nil {
		t.Fatalf("owning directory was refused: %v", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("owning uninstall did not remove the plist: %v", statErr)
	}
}

func TestAutoAttachStatusForIsolatedDirectoryReportsOwner(t *testing.T) {
	withFakeLaunch(t)
	mainDir := t.TempDir()
	config := testAgentConfig()
	config.DataDir = mainDir
	config.Executable = filepath.Join(mainDir, "enhanced-kimi")
	if _, err := InstallLaunchAgent(config); err != nil {
		t.Fatal(err)
	}
	state := LaunchAgentStatusForDataDir(t.TempDir())
	if state.Enabled {
		t.Fatalf("an isolated directory claimed the main login item: %+v", state)
	}
	if state.OwnerDataDir != filepath.Clean(mainDir) || state.Note == "" {
		t.Fatalf("owner was not reported: %+v", state)
	}
	if own := LaunchAgentStatusForDataDir(mainDir); !own.Enabled {
		t.Fatalf("the owning directory lost its enabled state: %+v", own)
	}
}

// The unload must happen before the plist disappears, otherwise launchd keeps
// running a definition the user can no longer see.
func TestRemoveLaunchAgentBootsOutBeforeDeletingThePlist(t *testing.T) {
	fake := withFakeLaunch(t)
	path, err := LaunchAgentPath(LaunchAgentLabel)
	if err != nil {
		t.Fatal(err)
	}
	fake.loadedLabels[LaunchAgentLabel] = true
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("plist"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeLaunchAgent(LaunchAgentLabel); err != nil {
		t.Fatal(err)
	}
	if len(fake.bootedOut) != 1 || fake.bootedOut[0] != LaunchAgentLabel {
		t.Fatalf("job was not unloaded: %v", fake.bootedOut)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("plist was not removed after the unload")
	}
}

func TestRecordAttachRecordsTheRunningCorePID(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(`{"pid":4711}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RecordAttach(dir, &AttachResult{Status: "installed"}); err != nil {
		t.Fatal(err)
	}
	if state := ReadAttachState(dir); state["corePid"] != float64(4711) {
		t.Fatalf("corePid was not recorded: %+v", state)
	}
	empty := t.TempDir()
	if err := RecordAttach(empty, &AttachResult{Status: "installed"}); err != nil {
		t.Fatal(err)
	}
	if state := ReadAttachState(empty); state["corePid"] != float64(os.Getpid()) {
		t.Fatalf("missing runtime.json did not fall back to the current process: %+v", state)
	}
}

func TestRecordAttachPreservesHealthyInstalledStateOnTransientError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(`{"pid":4711}`), 0o600); err != nil {
		t.Fatal(err)
	}
	installed := &AttachResult{
		Status:      "installed",
		HostPID:     8801,
		AppVersion:  "1.4.3",
		Electron:    SupportedElectron,
		Verified:    true,
		Transport:   "main-loopback-rpc",
		attachKey:   "key-live-host",
		hostStarted: "Thu Nov 26 10:00:00 2026",
	}
	if err := RecordAttach(dir, installed); err != nil {
		t.Fatal(err)
	}

	alive := true
	prevInspector := attachHealthyInstanceAlive
	attachHealthyInstanceAlive = func(_ context.Context, corePid, hostPid int, hostStarted string) bool {
		return alive && corePid == 4711 && hostPid == 8801 && hostStarted == "Thu Nov 26 10:00:00 2026"
	}
	t.Cleanup(func() { attachHealthyInstanceAlive = prevInspector })

	if err := RecordAttach(dir, errors.New("lock contention")); err != nil {
		t.Fatal(err)
	}
	state := ReadAttachState(dir)
	if state["status"] != "installed" || state["attachKey"] != "key-live-host" || state["hostStarted"] != "Thu Nov 26 10:00:00 2026" {
		t.Fatalf("healthy installed state was overwritten by transient error: %+v", state)
	}

	alive = false
	if err := RecordAttach(dir, errors.New("host exited")); err != nil {
		t.Fatal(err)
	}
	state = ReadAttachState(dir)
	if state["status"] != "error" || state["error"] != "host exited" {
		t.Fatalf("error was not recorded after host exited: %+v", state)
	}
}

func TestRecordAttachOverwritesInstalledStateWhenCoreRestarted(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(`{"pid":4711}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RecordAttach(dir, &AttachResult{
		Status:      "installed",
		HostPID:     8801,
		attachKey:   "key-old-core",
		hostStarted: "Thu Nov 26 10:00:00 2026",
	}); err != nil {
		t.Fatal(err)
	}

	prevInspector := attachHealthyInstanceAlive
	attachHealthyInstanceAlive = func(_ context.Context, _, _ int, _ string) bool { return true }
	t.Cleanup(func() { attachHealthyInstanceAlive = prevInspector })

	if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(`{"pid":5822}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RecordAttach(dir, errors.New("reattach failed")); err != nil {
		t.Fatal(err)
	}
	state := ReadAttachState(dir)
	if state["status"] != "error" || state["corePid"] != float64(5822) {
		t.Fatalf("stale installed state survived core restart: %+v", state)
	}
}

func TestDarwinAttachKeyIncludesCoreVersion(t *testing.T) {
	info := ProcessInfo{PID: 100, UID: 501, Started: "Thu Nov 26 10:00:00 2026", Executable: KimiExecutable}
	base := AttachOptions{CoreURL: "http://127.0.0.1:8321", PairingToken: strings.Repeat("t", 48), CoreVersion: "0.2.9"}
	same := darwinAttachKey(info, base)
	if got := darwinAttachKey(info, base); got != same {
		t.Fatalf("darwinAttachKey not deterministic: %s vs %s", got, same)
	}
	upgraded := base
	upgraded.CoreVersion = "0.2.10"
	if got := darwinAttachKey(info, upgraded); got == same {
		t.Fatal("darwinAttachKey ignored CoreVersion change")
	}
}

func TestRecordAttachOverwritesInstalledStateWhenBridgeTornDown(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(`{"pid":4711}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RecordAttach(dir, &AttachResult{
		Status:      "installed",
		HostPID:     8801,
		AppVersion:  "1.4.2",
		Electron:    SupportedElectron,
		Transport:   "main-loopback-rpc",
		attachKey:   "key-live-host",
		hostStarted: "Thu Nov 26 10:00:00 2026",
	}); err != nil {
		t.Fatal(err)
	}

	prevInspector := attachHealthyInstanceAlive
	attachHealthyInstanceAlive = func(_ context.Context, corePid, hostPid int, hostStarted string) bool {
		return corePid == 4711 && hostPid == 8801 && hostStarted == "Thu Nov 26 10:00:00 2026"
	}
	t.Cleanup(func() { attachHealthyInstanceAlive = prevInspector })

	// Precheck / lock contention error (bridge untouched) keeps installed state.
	if err := RecordAttach(dir, errors.New("等待其他 KimiCode+ 接入流程完成超时")); err != nil {
		t.Fatal(err)
	}
	state := ReadAttachState(dir)
	if state["status"] != "installed" || state["attachKey"] != "key-live-host" {
		t.Fatalf("precheck error overwrote healthy installed state: %+v", state)
	}

	// Torn-down error wrapped via fmt.Errorf / errors.Join still forces status=error.
	root := MarkBridgeTornDown(errors.New("宿主没有确认正式 RPC 已挂载"))
	joined := errors.Join(fmt.Errorf("reinject: %w", root), errors.New("cleanup detail"))
	if !IsBridgeTornDown(joined) {
		t.Fatal("IsBridgeTornDown failed through wrapped/joined error")
	}
	if err := RecordAttach(dir, joined); err != nil {
		t.Fatal(err)
	}
	state = ReadAttachState(dir)
	if state["status"] != "error" || state["attachKey"] != nil {
		t.Fatalf("bridge torn-down error preserved stale installed state: %+v", state)
	}
	if !strings.Contains(state["error"].(string), "宿主没有确认正式 RPC 已挂载") {
		t.Fatalf("unexpected error text: %+v", state)
	}
}

func TestRecordAttachInterleavingNoOutOfLockOverwrite(t *testing.T) {
	t.Run("torn-down failure recorded in lock does not overwrite later installed state", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(`{"pid":4711}`), 0o600); err != nil {
			t.Fatal(err)
		}
		prevAlive := attachHealthyInstanceAlive
		attachHealthyInstanceAlive = func(_ context.Context, corePid, hostPid int, hostStarted string) bool {
			return corePid == 4711 && hostPid == 8801 && hostStarted == "Thu Nov 26 10:00:00 2026"
		}
		t.Cleanup(func() { attachHealthyInstanceAlive = prevAlive })

		// Process A holds lock, tears down old bridge, records error in lock, releases lock.
		gateA, err := acquireHostAttachLock(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		errA := MarkBridgeTornDown(errors.New("A tore down bridge"))
		if err := recordAttachUnlocked(dir, errA); err != nil {
			_ = gateA.Close()
			t.Fatal(err)
		}
		errA = MarkAttachStateRecorded(errA)
		if err := gateA.Close(); err != nil {
			t.Fatal(err)
		}

		// Process B acquires lock immediately, installs cleanly, records installed in lock, releases lock.
		gateB, err := acquireHostAttachLock(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		resB := &AttachResult{
			Status:      "installed",
			HostPID:     8801,
			AppVersion:  "1.4.2",
			Transport:   "main-loopback-rpc",
			attachKey:   "key-B",
			hostStarted: "Thu Nov 26 10:00:00 2026",
		}
		if err := recordAttachUnlocked(dir, resB); err != nil {
			_ = gateB.Close()
			t.Fatal(err)
		}
		MarkAttachStateRecordedResult(resB)
		if err := gateB.Close(); err != nil {
			t.Fatal(err)
		}

		// Process A now reaches outer RecordAttach: must NOT overwrite B's installed state.
		if err := RecordAttach(dir, errA); err != nil {
			t.Fatal(err)
		}
		state := ReadAttachState(dir)
		if state["status"] != "installed" || state["attachKey"] != "key-B" {
			t.Fatalf("stale torn-down error from A overwrote B's installed state: %+v", state)
		}
	})

	t.Run("installed result recorded in lock does not overwrite later torn-down error", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(`{"pid":4711}`), 0o600); err != nil {
			t.Fatal(err)
		}

		// Process B holds lock, succeeds, records installed in lock, releases lock.
		gateB, err := acquireHostAttachLock(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		resB := &AttachResult{
			Status:      "installed",
			HostPID:     8801,
			AppVersion:  "1.4.2",
			Transport:   "main-loopback-rpc",
			attachKey:   "key-B",
			hostStarted: "Thu Nov 26 10:00:00 2026",
		}
		if err := recordAttachUnlocked(dir, resB); err != nil {
			_ = gateB.Close()
			t.Fatal(err)
		}
		MarkAttachStateRecordedResult(resB)
		if err := gateB.Close(); err != nil {
			t.Fatal(err)
		}

		// Process A (--force) acquires lock, tears down B's bridge, records error in lock, releases lock.
		gateA, err := acquireHostAttachLock(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		errA := MarkBridgeTornDown(errors.New("A force teardown failed"))
		if err := recordAttachUnlocked(dir, errA); err != nil {
			_ = gateA.Close()
			t.Fatal(err)
		}
		MarkAttachStateRecorded(errA)
		if err := gateA.Close(); err != nil {
			t.Fatal(err)
		}

		// Process B now reaches outer RecordAttach: must NOT mark torn-down bridge back as installed.
		if err := RecordAttach(dir, resB); err != nil {
			t.Fatal(err)
		}
		state := ReadAttachState(dir)
		if state["status"] != "error" || state["attachKey"] != nil {
			t.Fatalf("stale installed result from B overwrote A's error state: %+v", state)
		}
	})

	t.Run("unrecorded fallback RecordAttach yields while host-attach.lock is held", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(`{"pid":4711}`), 0o600); err != nil {
			t.Fatal(err)
		}
		gate, err := acquireHostAttachLock(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := recordAttachUnlocked(dir, &AttachResult{
			Status:    "installed",
			HostPID:   8801,
			attachKey: "active-lock-holder",
		}); err != nil {
			_ = gate.Close()
			t.Fatal(err)
		}

		// Contended outer fallback write yields without blocking or overwriting.
		if err := RecordAttach(dir, ErrConsentRequired); err != nil {
			_ = gate.Close()
			t.Fatal(err)
		}
		state := ReadAttachState(dir)
		if state["status"] != "installed" || state["attachKey"] != "active-lock-holder" {
			_ = gate.Close()
			t.Fatalf("fallback RecordAttach overwrote state while lock was held: %+v", state)
		}
		if err := gate.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("persist-failed error from in-lock commit does not re-record out of lock", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(`{"pid":4711}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeAttachState(dir, map[string]any{
			"status":    "installed",
			"hostPid":   8801,
			"attachKey": "untouched-by-persist-failed",
		}); err != nil {
			t.Fatal(err)
		}
		if err := RecordAttach(dir, MarkAttachPersistFailed(errors.New("写入接入状态文件失败：permission denied"))); err != nil {
			t.Fatal(err)
		}
		state := ReadAttachState(dir)
		if state["status"] != "installed" || state["attachKey"] != "untouched-by-persist-failed" {
			t.Fatalf("persist-failed error triggered out-of-lock RecordAttach overwrite: %+v", state)
		}
	})
}

func TestInvalidateInstalledAttachBeforeInjectUnlocked(t *testing.T) {
	t.Run("missing file and non-installed states are untouched", func(t *testing.T) {
		dir := t.TempDir()
		if err := invalidateInstalledAttachBeforeInjectUnlocked(dir); err != nil {
			t.Fatalf("missing file returned error: %v", err)
		}
		if ReadAttachState(dir) != nil {
			t.Fatal("missing file unexpectedly created attach-state.json")
		}
		if err := writeAttachState(dir, map[string]any{"status": "error", "error": "prev"}); err != nil {
			t.Fatal(err)
		}
		if err := invalidateInstalledAttachBeforeInjectUnlocked(dir); err != nil {
			t.Fatalf("error state returned error: %v", err)
		}
		if state := ReadAttachState(dir); state["status"] != "error" {
			t.Fatalf("non-installed error state was overwritten: %+v", state)
		}
	})

	t.Run("installed and corrupt files are replaced with attaching", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(`{"pid":5123}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeAttachState(dir, map[string]any{"status": "installed", "attachKey": "k1"}); err != nil {
			t.Fatal(err)
		}
		if err := invalidateInstalledAttachBeforeInjectUnlocked(dir); err != nil {
			t.Fatalf("installed state invalidation failed: %v", err)
		}
		state := ReadAttachState(dir)
		if state == nil || state["status"] != "attaching" || state["corePid"] != float64(5123) || state["attachKey"] != nil {
			t.Fatalf("unexpected attaching state: %+v", state)
		}

		if err := os.WriteFile(filepath.Join(dir, "attach-state.json"), []byte(`{corrupt`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := invalidateInstalledAttachBeforeInjectUnlocked(dir); err != nil {
			t.Fatalf("corrupt state invalidation failed: %v", err)
		}
		if state := ReadAttachState(dir); state == nil || state["status"] != "attaching" {
			t.Fatalf("corrupt file was not replaced with attaching: %+v", state)
		}
	})

	t.Run("read-only dir with installed file returns joined write and remove error", func(t *testing.T) {
		requireReadOnlyDirEnforced(t)
		dir := t.TempDir()
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		if err := writeAttachState(dir, map[string]any{"status": "installed", "attachKey": "k2"}); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		err := invalidateInstalledAttachBeforeInjectUnlocked(dir)
		if err == nil {
			t.Fatal("expected error when both replace and remove fail on read-only dir")
		}
		if !strings.Contains(err.Error(), "移除旧接入状态文件失败") {
			t.Fatalf("expected joined remove error, got: %v", err)
		}
	})

	t.Run("unreadable 0000 attach-state.json in 0500 dir returns non-nil error without silently allowing attach", func(t *testing.T) {
		requireReadOnlyDirEnforced(t)
		dir := t.TempDir()
		statePath := filepath.Join(dir, "attach-state.json")
		t.Cleanup(func() {
			_ = os.Chmod(dir, 0o700)
			_ = os.Chmod(statePath, 0o600)
		})
		if err := writeAttachState(dir, map[string]any{"status": "installed", "attachKey": "k-unreadable"}); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(statePath, 0o000); err != nil {
			t.Fatal(err)
		}
		if _, readErr := os.ReadFile(statePath); readErr == nil {
			t.Skip("current user can read 0000 file (likely root)")
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		err := invalidateInstalledAttachBeforeInjectUnlocked(dir)
		if err == nil {
			t.Fatal("expected error when attach-state.json is unreadable (0000) inside 0500 dir")
		}
		if !strings.Contains(err.Error(), "读取旧接入状态文件失败") || !strings.Contains(err.Error(), "移除旧接入状态文件失败") {
			t.Fatalf("expected joined read/write/remove error, got: %v", err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(statePath, 0o600); err != nil {
			t.Fatal(err)
		}
		if state := ReadAttachState(dir); state == nil || state["status"] != "installed" || state["attachKey"] != "k-unreadable" {
			t.Fatalf("original installed state was unexpectedly altered: %+v", state)
		}
	})
}
