//go:build darwin

package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// hostPlatformSupported gates the surfaces that would otherwise take over a
// host process. macOS and Windows have native host integration paths.
const (
	hostPlatformSupported = true
	hostPlatformName      = "darwin"
	hostAutoAttachKind    = "launch_agent"
)

func discoverHostPlatform(ctx context.Context, profile, _ string) (map[string]any, error) {
	return discoverHostDarwin(ctx, profile)
}

func attachNativePlatform(ctx context.Context, options AttachOptions) (*AttachResult, error) {
	return attachNativeDarwin(ctx, options)
}

func autoAttachStatusPlatform(dataDir string) LaunchAgentState {
	state := LaunchAgentStatusForDataDir(dataDir)
	if state.Enabled && !managedCoreRegistered(dataDir) {
		state.NeedsMigration = true
	}
	return state
}

func enableAutoAttachPlatform(config AttachConfig, allowDaily bool) (LaunchAgentState, error) {
	if config.Profile == "" && !allowDaily {
		return LaunchAgentState{}, ErrConsentRequired
	}
	if _, err := installManagedCore(config.Executable, config.DataDir); err != nil {
		return LaunchAgentState{}, err
	}
	dataDir, err := filepath.EvalSymlinks(config.DataDir)
	if err != nil {
		return LaunchAgentState{}, err
	}
	return InstallLaunchAgent(LaunchAgentConfig{
		DataDir: dataDir, KimiHome: config.KimiHome, Profile: config.Profile,
		AllowDailyInstance: allowDaily, Executable: managedCoreExecutable(dataDir),
	})
}

func disableAutoAttachPlatform(dataDir string) (LaunchAgentState, error) {
	return UninstallLaunchAgentForDataDir(dataDir)
}

// RunWindowsLauncherPlatform is only available on Windows hosts.
func RunWindowsLauncherPlatform(context.Context, WindowsLaunchOptions) (*WindowsLaunchResult, error) {
	return nil, ErrUnsupportedPlatform
}

// verifyRuntimeImage compares the mapped text image of the running host with the
// executable on disk. lsof reports inode numbers, so a replaced application
// bundle is caught even when its path is unchanged.
func verifyRuntimeImage(ctx context.Context, info ProcessInfo) error {
	output, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", "-a", "-p", strconv.Itoa(info.PID), "-d", "txt", "-Fin").Output()
	if err != nil {
		return errors.New("无法读取宿主进程的内存映像，拒绝接入")
	}
	st, err := os.Stat(info.Executable)
	if err != nil {
		return errors.New("宿主可执行文件不可读，拒绝接入")
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("无法读取宿主可执行文件标识，拒绝接入")
	}
	want := fmt.Sprintf("i%d\nn%s\n", sys.Ino, info.Executable)
	if !strings.Contains(string(output), want) {
		return errors.New("正在运行的宿主与已校验的应用不一致，拒绝接入")
	}
	return nil
}

// activateInspector asks the host to open its temporary inspector. SIGUSR1 is the
// only trigger Electron exposes for the fuse-gated node inspector.
func activateInspector(pid int) error {
	return syscall.Kill(pid, syscall.SIGUSR1)
}
