//go:build darwin

package desktop

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func withFakeDarwinLauncher(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := darwinLauncherDir
	darwinLauncherDir = dir
	t.Cleanup(func() { darwinLauncherDir = prev })
	return dir
}

func TestInstallDarwinLauncherAppCreatesExecutableBundleAndRefusesIsolatedProfile(t *testing.T) {
	withFakeDarwinLauncher(t)
	if _, err := InstallDarwinLauncherApp(LaunchAgentConfig{
		DataDir:    "/tmp/isolated-data",
		Profile:    "/tmp/isolated-profile",
		Executable: "/tmp/isolated-data/resident/enhanced-kimi",
	}); err == nil {
		t.Fatal("isolated profile was allowed to write the daily launcher app")
	}

	dataDir := filepath.Join(t.TempDir(), "Application Support", "EnhancedKimiDesktop")
	kimiHome := filepath.Join(t.TempDir(), "kimi's home")
	core := filepath.Join(dataDir, "resident", "enhanced-kimi")
	appPath, err := InstallDarwinLauncherApp(LaunchAgentConfig{
		DataDir:    dataDir,
		KimiHome:   kimiHome,
		Executable: core,
	})
	if err != nil {
		t.Fatalf("InstallDarwinLauncherApp: %v", err)
	}

	plistRaw, err := os.ReadFile(filepath.Join(appPath, "Contents", "Info.plist"))
	if err != nil {
		t.Fatalf("read Info.plist: %v", err)
	}
	if !strings.Contains(string(plistRaw), "<key>LSUIElement</key>") || !strings.Contains(string(plistRaw), "<string>KimiCode+</string>") {
		t.Fatalf("unexpected Info.plist: %s", plistRaw)
	}

	scriptPath := darwinLauncherExecutablePath(appPath)
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("stat launcher script: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("launcher script is not executable: %v", info.Mode())
	}
	if out, err := exec.Command("/bin/sh", "-n", scriptPath).CombinedOutput(); err != nil {
		t.Fatalf("launcher script failed sh -n syntax check: %v (%s)", err, string(out))
	}
	scriptRaw, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read launcher script: %v", err)
	}
	if !strings.Contains(string(scriptRaw), "DATA_DIR="+shellSingleQuote(dataDir)) {
		t.Fatalf("DATA_DIR not quoted properly in script:\n%s", scriptRaw)
	}
	if !strings.Contains(string(scriptRaw), "KIMI_HOME="+shellSingleQuote(kimiHome)) {
		t.Fatalf("KIMI_HOME not quoted properly in script:\n%s", scriptRaw)
	}
	if got := darwinLauncherInstalledPath(dataDir); got != appPath {
		t.Fatalf("darwinLauncherInstalledPath(%q) = %q, want %q", dataDir, got, appPath)
	}
	if got := darwinLauncherInstalledPath(t.TempDir()); got != "" {
		t.Fatalf("unrelated dataDir claimed launcher path: %q", got)
	}
}

func TestUninstallDailyManagedCoreHandsOffBeforeRemovingAgents(t *testing.T) {
	fake := withFakeLaunch(t)
	withFakeDarwinLauncher(t)
	prevDelay := selfBootoutDelay
	selfBootoutDelay = 10 * time.Millisecond
	t.Cleanup(func() { selfBootoutDelay = prevDelay })
	t.Setenv(managedCoreEnv, "1")

	dataDir := t.TempDir()
	kimiHome := t.TempDir()
	core := managedCoreExecutable(dataDir)
	cfg := LaunchAgentConfig{
		DataDir:            dataDir,
		KimiHome:           kimiHome,
		AllowDailyInstance: true,
		Executable:         core,
	}
	if _, err := InstallLaunchAgent(cfg); err != nil {
		t.Fatalf("InstallLaunchAgent: %v", err)
	}
	corePlistPath, _ := LaunchAgentPath(managedCoreLabel)
	corePlistRaw, err := os.ReadFile(corePlistPath)
	if err != nil {
		t.Fatalf("read managed core plist: %v", err)
	}
	if !strings.Contains(string(corePlistRaw), "<key>AbandonProcessGroup</key><true/>") {
		t.Fatalf("managed core plist missing AbandonProcessGroup: %s", corePlistRaw)
	}

	// Failure in handoff must abort before either LaunchAgent is removed.
	handoffManagedCore = func(LaunchAgentConfig) error {
		return errors.New("synthetic handoff failure")
	}
	if _, err := UninstallLaunchAgentForDataDir(dataDir); err == nil {
		t.Fatal("expected uninstall to fail when handoff fails")
	}
	if !fake.loadedLabels[LaunchAgentLabel] || !fake.loadedLabels[managedCoreLabel] {
		t.Fatalf("LaunchAgents were unloaded despite handoff failure: %+v", fake.loadedLabels)
	}

	// Successful handoff runs while managedCoreLabel is still loaded and passes the parsed config.
	var handedOff LaunchAgentConfig
	var loadedDuringHandoff bool
	handoffManagedCore = func(got LaunchAgentConfig) error {
		handedOff = got
		loadedDuringHandoff = fake.loadedLabels[managedCoreLabel]
		return nil
	}
	if _, err := UninstallLaunchAgentForDataDir(dataDir); err != nil {
		t.Fatalf("UninstallLaunchAgentForDataDir: %v", err)
	}
	if !loadedDuringHandoff {
		t.Fatal("handoff did not run before unloading managedCoreLabel")
	}
	if handedOff.DataDir != filepath.Clean(dataDir) || handedOff.KimiHome != kimiHome || handedOff.Executable != core || handedOff.Profile != "" {
		t.Fatalf("unexpected handoff config: %+v", handedOff)
	}
	if fake.loadedLabels[LaunchAgentLabel] {
		t.Fatal("attach LaunchAgent remained loaded")
	}
	if _, err := os.Stat(corePlistPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed core plist was not removed: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for fake.loaded(managedCoreLabel) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if fake.loaded(managedCoreLabel) {
		t.Fatal("managedCoreLabel was not asynchronously booted out after handoff")
	}
}

func TestUninstallIsolatedManagedCoreSkipsHandoff(t *testing.T) {
	fake := withFakeLaunch(t)
	withFakeDarwinLauncher(t)
	prevDelay := selfBootoutDelay
	selfBootoutDelay = 10 * time.Millisecond
	t.Cleanup(func() { selfBootoutDelay = prevDelay })
	t.Setenv(managedCoreEnv, "1")

	dataDir := t.TempDir()
	cfg := LaunchAgentConfig{
		DataDir:    dataDir,
		KimiHome:   t.TempDir(),
		Profile:    t.TempDir(),
		Executable: managedCoreExecutable(dataDir),
	}
	if _, err := InstallLaunchAgent(cfg); err != nil {
		t.Fatalf("InstallLaunchAgent: %v", err)
	}
	called := false
	handoffManagedCore = func(LaunchAgentConfig) error {
		called = true
		return nil
	}
	if _, err := UninstallLaunchAgentForDataDir(dataDir); err != nil {
		t.Fatalf("UninstallLaunchAgentForDataDir: %v", err)
	}
	if called {
		t.Fatal("isolated profile unexpectedly triggered daily launcher handoff")
	}
	deadline := time.Now().Add(time.Second)
	for fake.loaded(managedCoreLabel) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}
