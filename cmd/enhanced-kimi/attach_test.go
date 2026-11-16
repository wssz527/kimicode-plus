package main

import (
	"strings"
	"testing"

	"enhancedkimi/internal/desktop"
)

func agentConfig() desktop.LaunchAgentConfig {
	return desktop.LaunchAgentConfig{
		DataDir:      "/tmp/ekd",
		KimiHome:     "/tmp/kimi-home",
		Profile:      "/tmp/isolated",
		CoreURL:      "http://127.0.0.1:8321",
		PairingToken: strings.Repeat("t", 48),
		Executable:   "/tmp/ekd/enhanced-kimi",
	}
}

// TestValidateAgentArgumentsAcceptsTheRenderedAgent is the anti-drift guard: the
// exact argument list the plist is rendered from must satisfy the same flag set
// the agent runs with.
func TestValidateAgentArgumentsAcceptsTheRenderedAgent(t *testing.T) {
	for name, config := range map[string]desktop.LaunchAgentConfig{
		"isolated profile": agentConfig(),
		"daily instance": func() desktop.LaunchAgentConfig {
			c := agentConfig()
			c.Profile = ""
			c.AllowDailyInstance = true
			return c
		}(),
		"no kimi home": func() desktop.LaunchAgentConfig {
			c := agentConfig()
			c.KimiHome = ""
			return c
		}(),
	} {
		if err := validateAgentArguments(desktop.AttachArguments(config)); err != nil {
			t.Fatalf("%s: rendered agent does not parse: %v", name, err)
		}
		// The plist must render the same arguments the validator accepted.
		plist, err := desktop.LaunchAgentPlist(config)
		if err != nil {
			t.Fatalf("%s: plist failed: %v", name, err)
		}
		if !strings.Contains(plist, "<string>"+config.PairingToken+"</string>") {
			t.Fatalf("%s: plist does not carry the token the agent needs", name)
		}
	}
}

func TestValidateAgentArgumentsRejectsAnAgentThatCannotRun(t *testing.T) {
	cases := map[string][]string{
		"missing core url": {"attach", "--data-dir", "/tmp/ekd", "--pairing-token", strings.Repeat("t", 48)},
		"short token":      {"attach", "--data-dir", "/tmp/ekd", "--core-url", "http://127.0.0.1:8321", "--pairing-token", "short"},
		"missing data dir": {"attach", "--core-url", "http://127.0.0.1:8321", "--pairing-token", strings.Repeat("t", 48)},
		"no subcommand":    {"--data-dir", "/tmp/ekd", "--core-url", "http://127.0.0.1:8321", "--pairing-token", strings.Repeat("t", 48)},
		"empty list":       {},
		"unknown flag":     {"attach", "--data-dir", "/tmp/ekd", "--core-url", "http://127.0.0.1:8321", "--pairing-token", strings.Repeat("t", 48), "--nope"},
	}
	for name, args := range cases {
		if err := validateAgentArguments(args); err == nil {
			t.Fatalf("%s: an unusable agent was accepted: %v", name, args)
		}
	}
}

// TestAttachInstallValidatesBeforeWriting proves --install refuses to install a
// definition that would fail on every wake-up, and that nothing is written when
// it does.
func TestAttachInstallValidatesBeforeWriting(t *testing.T) {
	installs := 0
	previous := installLaunchAgent
	installLaunchAgent = func(config desktop.LaunchAgentConfig) (desktop.LaunchAgentState, error) {
		installs++
		return desktop.LaunchAgentState{Enabled: true}, nil
	}
	t.Cleanup(func() { installLaunchAgent = previous })
	err := runAttach([]string{"--install", "--data-dir", t.TempDir()})
	if err == nil {
		t.Fatal("install ran without a core URL")
	}
	if !strings.Contains(err.Error(), "--core-url") {
		t.Fatalf("failure does not name the missing input: %v", err)
	}
	if installs != 0 {
		t.Fatalf("install wrote an agent before validating its arguments: %d installs", installs)
	}
	// A complete definition does reach the installer, with its credentials intact.
	var received desktop.LaunchAgentConfig
	installLaunchAgent = func(config desktop.LaunchAgentConfig) (desktop.LaunchAgentState, error) {
		received = config
		return desktop.LaunchAgentState{Enabled: true, Path: "/tmp/plist"}, nil
	}
	token := strings.Repeat("t", 48)
	if err = runAttach([]string{"--install", "--data-dir", t.TempDir(),
		"--core-url", "http://127.0.0.1:8321", "--pairing-token", token}); err != nil {
		t.Fatalf("complete install failed: %v", err)
	}
	if received.CoreURL != "http://127.0.0.1:8321" || received.PairingToken != token {
		t.Fatalf("credentials did not reach the installer: %+v", received)
	}
	if err = validateAgentArguments(desktop.AttachArguments(received)); err != nil {
		t.Fatalf("the installed definition does not parse: %v", err)
	}
}
