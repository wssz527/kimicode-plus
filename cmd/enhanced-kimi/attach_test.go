package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enhancedkimi/internal/desktop"
)

func agentConfig() desktop.LaunchAgentConfig {
	return desktop.LaunchAgentConfig{
		DataDir:    "/tmp/ekd",
		KimiHome:   "/tmp/kimi-home",
		Profile:    "/tmp/isolated",
		Executable: "/tmp/ekd/enhanced-kimi",
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
		if !strings.Contains(plist, filepath.Join(config.DataDir, "runtime.json")) || strings.Contains(plist, "--pairing-token") {
			t.Fatalf("%s: plist does not use the live runtime file", name)
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
	err := runAttach([]string{"--install"})
	if err == nil || !strings.Contains(err.Error(), "--data-dir") {
		t.Fatalf("failure does not name the missing input: %v", err)
	}
	if installs != 0 {
		t.Fatalf("install wrote an agent before validating its arguments: %d installs", installs)
	}
	if err = runAttach([]string{"--install", "--data-dir", t.TempDir(), "--host-profile", t.TempDir(), "--kimi-home", t.TempDir()}); err != nil || installs != 1 {
		t.Fatalf("install without fixed credentials failed: installs=%d, error=%v", installs, err)
	}
}

func TestResolveAttachCredentialsPrefersLiveRuntime(t *testing.T) {
	ctx := context.Background()
	staleToken := strings.Repeat("a", 48)
	liveToken := strings.Repeat("b", 48)
	staleURL := "http://127.0.0.1:1"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/enhance/v1/health" && r.Header.Get("Authorization") == "Bearer "+liveToken {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	dir := t.TempDir()
	if gotURL, gotToken := resolveAttachCredentials(ctx, dir, staleURL, staleToken); gotURL != staleURL || gotToken != staleToken {
		t.Fatalf("missing runtime.json did not fall back: %s %s", gotURL, gotToken)
	}

	writeRuntime := func(rawURL, token, version string) {
		t.Helper()
		payload, err := json.Marshal(map[string]any{"url": rawURL, "pairingToken": token, "version": version})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "runtime.json"), payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeRuntime(server.URL, liveToken, "0.2.7")
	if gotURL, gotToken, gotVersion := resolveAttachRuntime(ctx, dir, staleURL, staleToken); gotURL != server.URL || gotToken != liveToken || gotVersion != "0.2.7" {
		t.Fatalf("live runtime.json was not preferred: got (%s, %s, %s), want (%s, %s, 0.2.7)", gotURL, gotToken, gotVersion, server.URL, liveToken)
	}

	writeRuntime("https://remote.example", liveToken, "0.2.7")
	if gotURL, gotToken := resolveAttachCredentials(ctx, dir, staleURL, staleToken); gotURL != staleURL || gotToken != staleToken {
		t.Fatalf("non-loopback runtime.json was accepted: %s", gotURL)
	}

	writeRuntime(server.URL, strings.Repeat("x", 48), "0.2.7")
	if gotURL, gotToken := resolveAttachCredentials(ctx, dir, staleURL, staleToken); gotURL != staleURL || gotToken != staleToken {
		t.Fatalf("unauthorized runtime.json did not fall back: %s", gotURL)
	}
}

// A recorded, successful attach is replayed with the exact consent it was
// recorded with. A Windows launcher lease must not widen that consent.
func TestRestartStateReplaysRecordedAttach(t *testing.T) {
	dir := t.TempDir()
	payload, err := json.Marshal(map[string]any{"status": "installed", "daily": false})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "attach-state.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	state := restartStateForCore(dir, 41234, "", false)
	if state.Port != 41234 || !state.Reattach || state.ReattachDaily {
		t.Fatalf("recorded attach was not replayed: %+v", state)
	}
	payload, err = json.Marshal(map[string]any{"status": "installed", "daily": true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "attach-state.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	state = restartStateForCore(dir, 41234, "", false)
	if !state.Reattach || !state.ReattachDaily {
		t.Fatalf("daily consent recorded by the user was dropped: %+v", state)
	}
}

// A failed reattach has to be visible: the old installed record would otherwise
// keep claiming the interface is attached after a restart.
func TestRestartStateIgnoresFailedAttachRecord(t *testing.T) {
	dir := t.TempDir()
	payload, err := json.Marshal(map[string]any{"status": "error", "error": "宿主已退出", "daily": true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "attach-state.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if state := restartStateForCore(dir, 41000, "", false); state.Reattach || state.ReattachDaily {
		t.Fatalf("a failed attach record asked for a reattach: %+v", state)
	}
}

func TestRestartStateIgnoresExpiredWindowsLease(t *testing.T) {
	dir := t.TempDir()
	if state := restartStateForCore(dir, 41000, "", true); state.Reattach || state.ReattachDaily {
		t.Fatalf("a missing lease asked for a reattach: %+v", state)
	}
}
