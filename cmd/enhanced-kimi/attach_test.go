package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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

func TestResolveAttachRuntimeAlwaysProbesHealthEvenWhenFallbackMatches(t *testing.T) {
	ctx := context.Background()
	token := strings.Repeat("z", 48)
	var healthHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/enhance/v1/health" && r.Header.Get("Authorization") == "Bearer "+token {
			healthHits++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"OK"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	dir := t.TempDir()
	payload, err := json.Marshal(map[string]any{
		"url":          srv.URL,
		"pairingToken": token,
		"version":      "0.2.10",
		"pid":          os.Getpid(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	// Even when CLI fallback matches runtime.json exactly, /enhance/v1/health must be probed.
	gotURL, gotToken, gotVersion := resolveAttachRuntime(ctx, dir, srv.URL, token)
	if gotURL != srv.URL || gotToken != token || gotVersion != "0.2.10" {
		t.Fatalf("live matching runtime.json failed: got (%s, %s, %s)", gotURL, gotToken, gotVersion)
	}
	if healthHits != 1 {
		t.Fatalf("resolveAttachRuntime skipped /enhance/v1/health probe when fallback matched: hits=%d", healthHits)
	}

	// Once the core exits (server closed) leaving stale runtime.json on disk, matching CLI flags must NOT adopt it.
	srv.Close()
	gotURL, gotToken, gotVersion = resolveAttachRuntime(ctx, dir, srv.URL, token)
	if gotURL != srv.URL || gotToken != token || gotVersion != "" {
		t.Fatalf("dead core stale runtime.json was wrongly adopted: got (%s, %s, %q)", gotURL, gotToken, gotVersion)
	}
}

func TestRunAttachPersistFailedDoesNotReRecordOutOfLock(t *testing.T) {
	dir := t.TempDir()
	token := strings.Repeat("p", 48)
	initialRaw, err := json.Marshal(map[string]any{
		"status":    "installed",
		"hostPid":   9001,
		"attachKey": "untouched-state",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "attach-state.json"), initialRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	prevAttach := attachOnce
	prevStdout := attachStdout
	attachStdout = io.Discard
	attachOnce = func(_ context.Context, _ desktop.AttachOptions) (*desktop.AttachResult, error) {
		return nil, desktop.MarkAttachPersistFailed(errors.New("写入接入状态文件失败：rename blocked"))
	}
	t.Cleanup(func() {
		attachOnce = prevAttach
		attachStdout = prevStdout
	})

	err = runAttach([]string{"--data-dir", dir, "--core-url", "http://127.0.0.1:8321", "--pairing-token", token})
	if err == nil || !desktop.IsAttachPersistFailed(err) {
		t.Fatalf("expected persist-failed error from runAttach, got: %v", err)
	}
	if desktop.IsAttachStateRecorded(err) {
		t.Fatalf("persist-failed error must not be marked stateRecorded: %v", err)
	}
	state := desktop.ReadAttachState(dir)
	if state["status"] != "installed" || state["attachKey"] != "untouched-state" {
		t.Fatalf("runAttach performed out-of-lock RecordAttach despite persist-failed marker: %+v", state)
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

func TestRunAttachDefaultsSkipCurrentAndEchoesStateWhenSkipped(t *testing.T) {
	dir := t.TempDir()
	token := strings.Repeat("c", 48)
	existingPayload := map[string]any{
		"status":      "installed",
		"hostPid":     float64(9001),
		"appVersion":  "1.4.3",
		"attachKey":   "stable-key",
		"hostStarted": "Thu Nov 26 10:00:00 2026",
	}
	rawState, err := json.Marshal(existingPayload)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "attach-state.json")
	if err := os.WriteFile(statePath, rawState, 0o600); err != nil {
		t.Fatal(err)
	}

	var gotOpts []desktop.AttachOptions
	prevAttach := attachOnce
	prevStdout := attachStdout
	attachOnce = func(_ context.Context, opts desktop.AttachOptions) (*desktop.AttachResult, error) {
		gotOpts = append(gotOpts, opts)
		if opts.SkipCurrent {
			return nil, nil
		}
		return &desktop.AttachResult{Status: "installed", HostPID: 9001, AppVersion: "1.4.3"}, nil
	}
	t.Cleanup(func() {
		attachOnce = prevAttach
		attachStdout = prevStdout
	})

	// Foreground without --force: SkipCurrent=true, outputs existing state JSON, does not overwrite file.
	var fgOut bytes.Buffer
	attachStdout = &fgOut
	if err := runAttach([]string{"--data-dir", dir, "--core-url", "http://127.0.0.1:8321", "--pairing-token", token}); err != nil {
		t.Fatalf("foreground skipped attach: %v", err)
	}
	if len(gotOpts) != 1 || !gotOpts[0].SkipCurrent {
		t.Fatalf("foreground attach did not default SkipCurrent=true: %+v", gotOpts)
	}
	var decoded map[string]any
	if err := json.Unmarshal(fgOut.Bytes(), &decoded); err != nil {
		t.Fatalf("foreground skipped stdout not valid JSON (%q): %v", fgOut.String(), err)
	}
	if decoded["status"] != "installed" || decoded["attachKey"] != "stable-key" {
		t.Fatalf("foreground skipped stdout = %+v", decoded)
	}
	afterSkip, err := os.ReadFile(statePath)
	if err != nil || string(afterSkip) != string(rawState) {
		t.Fatalf("attach-state.json changed on skip: got %q, want %q", afterSkip, rawState)
	}

	// Background without --force: SkipCurrent=true, silent stdout.
	var bgOut bytes.Buffer
	attachStdout = &bgOut
	if err := runAttach([]string{"--background", "--data-dir", dir, "--core-url", "http://127.0.0.1:8321", "--pairing-token", token}); err != nil {
		t.Fatalf("background skipped attach: %v", err)
	}
	if len(gotOpts) != 2 || !gotOpts[1].SkipCurrent {
		t.Fatalf("background attach did not set SkipCurrent=true: %+v", gotOpts)
	}
	if bgOut.Len() != 0 {
		t.Fatalf("background skipped attach wrote to stdout: %q", bgOut.String())
	}

	// Explicit --force: SkipCurrent=false, performs fresh attach.
	var forceOut bytes.Buffer
	attachStdout = &forceOut
	if err := runAttach([]string{"--force", "--data-dir", dir, "--core-url", "http://127.0.0.1:8321", "--pairing-token", token}); err != nil {
		t.Fatalf("forced attach: %v", err)
	}
	if len(gotOpts) != 3 || gotOpts[2].SkipCurrent {
		t.Fatalf("--force did not set SkipCurrent=false: %+v", gotOpts)
	}
	decoded = nil
	if err := json.Unmarshal(forceOut.Bytes(), &decoded); err != nil || decoded["status"] != "installed" {
		t.Fatalf("forced attach stdout = %q, err=%v", forceOut.String(), err)
	}
}

func TestRunAttachForceBridgeTornDownUpdatesErrorAndNextAttachReinjects(t *testing.T) {
	dir := t.TempDir()
	token := strings.Repeat("d", 48)
	selfPID := os.Getpid()
	hostStarted := "Thu Nov 26 10:00:00 2026"
	canVerifyLiveProcess := false
	if runtime.GOOS == "darwin" {
		if info, err := desktop.ProcessIdentity(context.Background(), selfPID); err == nil && info.Started != "" {
			hostStarted = info.Started
			canVerifyLiveProcess = true
		}
	}
	runtimeRaw, err := json.Marshal(map[string]any{"pid": selfPID})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime.json"), runtimeRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	initialState := map[string]any{
		"status":      "installed",
		"corePid":     selfPID,
		"hostPid":     selfPID,
		"hostStarted": hostStarted,
		"appVersion":  "1.4.2",
		"attachKey":   "stable-key",
	}
	rawInitial, err := json.Marshal(initialState)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "attach-state.json"), rawInitial, 0o600); err != nil {
		t.Fatal(err)
	}

	prevAttach := attachOnce
	prevStdout := attachStdout
	t.Cleanup(func() {
		attachOnce = prevAttach
		attachStdout = prevStdout
	})

	// 1. Pre-touch lock/precheck error (not marked torn-down) preserves healthy installed state.
	attachOnce = func(_ context.Context, _ desktop.AttachOptions) (*desktop.AttachResult, error) {
		return nil, errors.New("等待其他 KimiCode+ 接入流程完成超时")
	}
	var out bytes.Buffer
	attachStdout = &out
	if err := runAttach([]string{"--force", "--data-dir", dir, "--core-url", "http://127.0.0.1:8321", "--pairing-token", token}); err == nil {
		t.Fatal("expected precheck error from --force attach")
	}
	if canVerifyLiveProcess {
		state := desktop.ReadAttachState(dir)
		if state["status"] != "installed" || state["attachKey"] != "stable-key" {
			t.Fatalf("pre-touch failure overwrote healthy installed state: %+v", state)
		}
	}

	// 2. --force fails after tearing down the old bridge: attach-state.json must become error and drop attachKey.
	attachOnce = func(_ context.Context, opts desktop.AttachOptions) (*desktop.AttachResult, error) {
		if opts.SkipCurrent {
			t.Fatal("--force unexpectedly set SkipCurrent=true")
		}
		return nil, desktop.MarkBridgeTornDown(errors.New("宿主没有确认正式 RPC 已挂载"))
	}
	if err := runAttach([]string{"--force", "--data-dir", dir, "--core-url", "http://127.0.0.1:8321", "--pairing-token", token}); err == nil {
		t.Fatal("expected torn-down error from --force attach")
	}
	state := desktop.ReadAttachState(dir)
	if state["status"] != "error" || state["attachKey"] != nil {
		t.Fatalf("--force teardown failure did not update attach-state.json to error: %+v", state)
	}

	// 3. Subsequent ordinary attach (SkipCurrent=true) must not skip because attach-state.json is now error.
	reinjected := 0
	attachOnce = func(_ context.Context, opts desktop.AttachOptions) (*desktop.AttachResult, error) {
		if !opts.SkipCurrent {
			t.Fatal("ordinary attach did not set SkipCurrent=true")
		}
		if existing := desktop.ReadAttachState(opts.DataDir); existing["status"] == "installed" && existing["attachKey"] == "stable-key" {
			return nil, nil
		}
		reinjected++
		return &desktop.AttachResult{Status: "installed", HostPID: selfPID, AppVersion: "1.4.2"}, nil
	}
	out.Reset()
	if err := runAttach([]string{"--data-dir", dir, "--core-url", "http://127.0.0.1:8321", "--pairing-token", token}); err != nil {
		t.Fatalf("ordinary recovery attach failed: %v", err)
	}
	if reinjected != 1 {
		t.Fatalf("ordinary attach after torn-down failure skipped reinjection: reinjected=%d", reinjected)
	}
	state = desktop.ReadAttachState(dir)
	if state["status"] != "installed" {
		t.Fatalf("ordinary recovery attach did not restore installed state: %+v", state)
	}
}

func TestRunAttachInterleavingNeverOverwritesOutOfLock(t *testing.T) {
	token := strings.Repeat("e", 48)
	selfPID := os.Getpid()

	t.Run("A torn-down error in lock then B installed in lock: A return must not overwrite B", func(t *testing.T) {
		dir := t.TempDir()
		runtimeRaw, err := json.Marshal(map[string]any{"pid": selfPID})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "runtime.json"), runtimeRaw, 0o600); err != nil {
			t.Fatal(err)
		}

		aCommitted := make(chan struct{})
		bCommitted := make(chan struct{})
		prevAttach := attachOnce
		prevStdout := attachStdout
		attachStdout = io.Discard
		attachOnce = func(_ context.Context, opts desktop.AttachOptions) (*desktop.AttachResult, error) {
			if !opts.SkipCurrent {
				// Process A (--force): records torn-down error inside lock, releases lock, then pauses before returning to CLI.
				errA := desktop.MarkBridgeTornDown(errors.New("A tore down old bridge"))
				if recErr := desktop.RecordAttach(opts.DataDir, errA); recErr != nil {
					return nil, recErr
				}
				recordedErrA := desktop.MarkAttachStateRecorded(errA)
				close(aCommitted)
				<-bCommitted
				return nil, recordedErrA
			}
			// Process B: waits for A to commit & release lock, then records installed inside lock.
			<-aCommitted
			resB := &desktop.AttachResult{Status: "installed", HostPID: selfPID, AppVersion: "1.4.2", Transport: "main-loopback-rpc"}
			if recErr := desktop.RecordAttach(opts.DataDir, resB); recErr != nil {
				return nil, recErr
			}
			desktop.MarkAttachStateRecordedResult(resB)
			close(bCommitted)
			return resB, nil
		}
		t.Cleanup(func() {
			attachOnce = prevAttach
			attachStdout = prevStdout
		})

		errChA := make(chan error, 1)
		errChB := make(chan error, 1)
		go func() {
			errChA <- runAttach([]string{"--force", "--data-dir", dir, "--core-url", "http://127.0.0.1:8321", "--pairing-token", token})
		}()
		go func() {
			errChB <- runAttach([]string{"--data-dir", dir, "--core-url", "http://127.0.0.1:8321", "--pairing-token", token})
		}()

		errA := <-errChA
		errB := <-errChB
		if errA == nil || !desktop.IsBridgeTornDown(errA) {
			t.Fatalf("expected torn-down error from A, got: %v", errA)
		}
		if errB != nil {
			t.Fatalf("expected B to succeed, got: %v", errB)
		}
		state := desktop.ReadAttachState(dir)
		if state["status"] != "installed" || state["hostPid"] != float64(selfPID) {
			t.Fatalf("A returning to CLI overwrote B's installed state: %+v", state)
		}
	})

	t.Run("B installed in lock then A torn-down error in lock: B return must not restore installed", func(t *testing.T) {
		dir := t.TempDir()
		runtimeRaw, err := json.Marshal(map[string]any{"pid": selfPID})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "runtime.json"), runtimeRaw, 0o600); err != nil {
			t.Fatal(err)
		}

		bCommitted := make(chan struct{})
		aCommitted := make(chan struct{})
		prevAttach := attachOnce
		prevStdout := attachStdout
		attachStdout = io.Discard
		attachOnce = func(_ context.Context, opts desktop.AttachOptions) (*desktop.AttachResult, error) {
			if opts.SkipCurrent {
				// Process B: records installed inside lock, releases lock, then pauses before returning to CLI.
				resB := &desktop.AttachResult{Status: "installed", HostPID: selfPID, AppVersion: "1.4.2", Transport: "main-loopback-rpc"}
				if recErr := desktop.RecordAttach(opts.DataDir, resB); recErr != nil {
					return nil, recErr
				}
				desktop.MarkAttachStateRecordedResult(resB)
				close(bCommitted)
				<-aCommitted
				return resB, nil
			}
			// Process A (--force): waits for B to commit & release lock, then tears down and records error inside lock.
			<-bCommitted
			errA := desktop.MarkBridgeTornDown(errors.New("A force teardown failed"))
			if recErr := desktop.RecordAttach(opts.DataDir, errA); recErr != nil {
				return nil, recErr
			}
			recordedErrA := desktop.MarkAttachStateRecorded(errA)
			close(aCommitted)
			return nil, recordedErrA
		}
		t.Cleanup(func() {
			attachOnce = prevAttach
			attachStdout = prevStdout
		})

		errChB := make(chan error, 1)
		errChA := make(chan error, 1)
		go func() {
			errChB <- runAttach([]string{"--data-dir", dir, "--core-url", "http://127.0.0.1:8321", "--pairing-token", token})
		}()
		go func() {
			errChA <- runAttach([]string{"--force", "--data-dir", dir, "--core-url", "http://127.0.0.1:8321", "--pairing-token", token})
		}()

		errB := <-errChB
		errA := <-errChA
		if errB != nil {
			t.Fatalf("expected B to succeed, got: %v", errB)
		}
		if errA == nil || !desktop.IsBridgeTornDown(errA) {
			t.Fatalf("expected torn-down error from A, got: %v", errA)
		}
		state := desktop.ReadAttachState(dir)
		if state["status"] != "error" || state["attachKey"] != nil {
			t.Fatalf("B returning to CLI overwrote A's error state back to installed: %+v", state)
		}
	})
}
