package desktop

import (
	"context"
	"errors"
	"runtime"
)

func ManagedUpdateExecutable(dataDir string) string {
	if runtime.GOOS == "darwin" && managedCoreRegistered(dataDir) {
		return managedCoreExecutable(dataDir)
	}
	return ""
}

// AttachConfig carries the core and host identities for native integration.
type AttachConfig struct {
	DataDir      string
	KimiHome     string
	Profile      string
	CoreURL      string
	PairingToken string
	CoreVersion  string
	Executable   string
}

// UseAttach records where the agent should report and which host it targets.
func (s *NativeService) UseAttach(config AttachConfig) {
	s.mu.Lock()
	s.attach = config
	s.mu.Unlock()
}

func (s *NativeService) isSafe() bool {
	return s.safe != nil && s.safe()
}

func (s *NativeService) attachConfig() AttachConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attach
}

// attachStatus reports the discovery result, the agent state and the last run
// outcome in one object so the settings surface needs a single request.
func (s *NativeService) attachStatus(ctx context.Context) map[string]any {
	config := s.attachConfig()
	host := map[string]any{"running": false, "reason": "未配置 Kimi Code 数据目录，无法发现宿主"}
	if !hostPlatformSupported {
		// Discovery itself depends on platform host tools, so an unsupported
		// platform reports the support boundary instead of pretending a probe ran.
		host = map[string]any{"running": false, "reason": ErrUnsupportedPlatform.Error()}
	} else if config.KimiHome != "" || hostPlatformName == "windows" {
		if discovered, err := DiscoverHostWithDataDir(ctx, config.Profile, config.DataDir); err == nil {
			host = discovered
		} else {
			host = map[string]any{"running": false, "reason": err.Error()}
		}
	}
	if s.isSafe() {
		host["canReopen"] = false
	}
	agent := autoAttachStatusPlatform(config.DataDir)
	payload := map[string]any{
		"host":           host,
		"agent":          agent,
		"platform":       hostPlatformName,
		"autoAttachKind": hostAutoAttachKind,
	}
	if state := ReadAttachState(config.DataDir); state != nil {
		payload["last"] = state
	}
	// The agent cannot be offered when the core does not know its own binary or
	// the pairing details a background run needs. It is also never offered on a
	// platform whose host runtime has not been verified.
	payload["available"] = hostPlatformSupported && config.Executable != "" && (hostPlatformName == "windows" || (config.CoreURL != "" && len(config.PairingToken) >= 32))
	if !payload["available"].(bool) {
		payload["unavailableReason"] = "后台接入需要在带核心地址和配对凭据的增强核心中启用"
		if !hostPlatformSupported {
			payload["unavailableReason"] = ErrUnsupportedPlatform.Error()
		}
	}
	return payload
}

// enableAttach installs the platform login tasks after explicit consent.
func (s *NativeService) enableAttach(ctx context.Context, input map[string]any) (map[string]any, error) {
	if !hostPlatformSupported {
		return nil, ErrUnsupportedPlatform
	}
	config := s.attachConfig()
	if config.Executable == "" || (hostPlatformName != "windows" && (config.CoreURL == "" || len(config.PairingToken) < AttachTokenMinLength)) {
		return nil, errors.New("后台接入需要核心地址和配对凭据，请从完整的增强核心中启用")
	}
	allowDaily, _ := input["allowDailyInstance"].(bool)
	// A daily instance requires its own explicit confirmation; auto-attach alone
	// is not consent to change the interface the user is currently working in.
	if config.Profile == "" && !allowDaily {
		return nil, ErrConsentRequired
	}
	state, err := enableAutoAttachPlatform(config, allowDaily)
	if err != nil {
		return nil, err
	}
	return map[string]any{"agent": state}, nil
}

func (s *NativeService) disableAttach() (map[string]any, error) {
	state, err := disableAutoAttachPlatform(s.attachConfig().DataDir)
	if err != nil {
		return nil, err
	}
	return map[string]any{"agent": state}, nil
}

// HostPlatformName reports the compiled native attach target ("darwin", "windows", or "other").
func HostPlatformName() string {
	return hostPlatformName
}

// AutoAttachStatus reports the current platform login and attach state.
func AutoAttachStatus(dataDir string) LaunchAgentState {
	return autoAttachStatusPlatform(dataDir)
}

// EnableAutoAttach installs the platform auto-attach mechanism.
func EnableAutoAttach(config LaunchAgentConfig) (LaunchAgentState, error) {
	return enableAutoAttachPlatform(AttachConfig{
		DataDir:    config.DataDir,
		KimiHome:   config.KimiHome,
		Profile:    config.Profile,
		Executable: config.Executable,
	}, config.AllowDailyInstance)
}

// DisableAutoAttach removes the platform login tasks and restores older launcher shortcuts when present.
func DisableAutoAttach(dataDir string) (LaunchAgentState, error) {
	return disableAutoAttachPlatform(dataDir)
}
