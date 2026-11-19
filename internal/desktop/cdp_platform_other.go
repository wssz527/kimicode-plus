//go:build !darwin && !windows

package desktop

import "context"

// hostPlatformSupported gates the surfaces that would otherwise take over a
// host process. Unsupported platforms (such as Linux) remain compile-only and
// refuse without signalling any process.
const (
	hostPlatformSupported = false
	hostPlatformName      = "other"
	hostAutoAttachKind    = "none"
)

func verifyRuntimeImage(context.Context, ProcessInfo) error {
	return ErrUnsupportedPlatform
}

func activateInspector(int) error {
	return ErrUnsupportedPlatform
}

func discoverHostPlatform(context.Context, string, string) (map[string]any, error) {
	return nil, ErrUnsupportedPlatform
}

func attachNativePlatform(context.Context, AttachOptions) (*AttachResult, error) {
	return nil, ErrUnsupportedPlatform
}

func autoAttachStatusPlatform(string) LaunchAgentState {
	return LaunchAgentState{Error: ErrUnsupportedPlatform.Error()}
}

func enableAutoAttachPlatform(AttachConfig, bool) (LaunchAgentState, error) {
	return LaunchAgentState{}, ErrUnsupportedPlatform
}

func disableAutoAttachPlatform(string) (LaunchAgentState, error) {
	return LaunchAgentState{}, ErrUnsupportedPlatform
}

// RunWindowsLauncherPlatform is only available on Windows hosts.
func RunWindowsLauncherPlatform(context.Context, WindowsLaunchOptions) (*WindowsLaunchResult, error) {
	return nil, ErrUnsupportedPlatform
}
