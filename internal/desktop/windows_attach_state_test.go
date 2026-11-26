package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func windowsAttachTestProc(pid int, started string) WindowsProcessInfo {
	return WindowsProcessInfo{
		PID: pid, Started: started, SessionID: 1, OwnerSID: "S-1-5-21-1",
		Executable:  `C:\Users\me\AppData\Local\Programs\Kimi Code\Kimi Code.exe`,
		CommandLine: `"C:\Users\me\AppData\Local\Programs\Kimi Code\Kimi Code.exe"`,
	}
}

func writeAttachCoreRuntime(t *testing.T, dir string, pid int) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"url": "http://127.0.0.1:1234", "pid": pid})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsCurrentAttachStateRejectsPreviousProcess(t *testing.T) {
	proc := WindowsProcessInfo{PID: 42, Started: "current"}
	last := map[string]any{"hostPid": float64(42), "hostStarted": "old", "status": "installed", "transport": "main-loopback-rpc"}
	if got := windowsCurrentAttachState(proc, last, false); got != "unattached" {
		t.Fatalf("PID reuse reported %s", got)
	}
	last["hostStarted"] = proc.Started
	if got := windowsCurrentAttachState(proc, last, false); got != "attached" {
		t.Fatalf("main bridge reported %s", got)
	}
	last["transport"] = "windows-loopback-rpc"
	if got := windowsCurrentAttachState(proc, last, true); got != "compatibility" {
		t.Fatalf("renderer transport reported %s", got)
	}
	last["status"] = "error"
	if got := windowsCurrentAttachState(proc, last, false); got != "failed" {
		t.Fatalf("failed attach reported %s", got)
	}
}

func TestWindowsCurrentAttachStateRejectsStaleCoreCompatibility(t *testing.T) {
	proc := windowsAttachTestProc(42, "current")
	last := map[string]any{
		"hostPid": float64(42), "hostStarted": "current", "status": "installed",
		"transport": "windows-cdp-loopback", "corePid": float64(1000),
	}
	if got := windowsCurrentAttachStateForCore(proc, last, true, 1000); got != "compatibility" {
		t.Fatalf("same core reported %s", got)
	}
	// The core restarted: the bridge session lived in its memory, so the same
	// host process is no longer attached even though the file says installed.
	if got := windowsCurrentAttachStateForCore(proc, last, true, 2000); got != "unattached" {
		t.Fatalf("stale core compatibility reported %s", got)
	}
	last["transport"] = "main-loopback-rpc"
	if got := windowsCurrentAttachStateForCore(proc, last, false, 2000); got != "unattached" {
		t.Fatalf("stale core main bridge reported %s", got)
	}
	if got := windowsCurrentAttachStateForCore(proc, last, false, 1000); got != "attached" {
		t.Fatalf("matching core main bridge reported %s", got)
	}
}

func TestWindowsCurrentAttachStateRejectsStaleCoreFailure(t *testing.T) {
	proc := windowsAttachTestProc(42, "current")
	last := map[string]any{
		"hostPid": float64(42), "hostStarted": "current", "status": "error",
		"corePid": float64(1000),
	}
	if got := windowsCurrentAttachStateForCore(proc, last, false, 2000); got != "unattached" {
		t.Fatalf("error from a previous core reported %s", got)
	}
	// A fresh core that failed after package 2 rewrote the state keeps the new
	// pid, so the failure must still surface.
	last["corePid"] = float64(2000)
	if got := windowsCurrentAttachStateForCore(proc, last, false, 2000); got != "failed" {
		t.Fatalf("failure of the running core reported %s", got)
	}
}

func TestWindowsCurrentAttachStateWithoutCoreIdentityStaysCompatible(t *testing.T) {
	proc := windowsAttachTestProc(42, "current")
	last := map[string]any{
		"hostPid": float64(42), "hostStarted": "current", "status": "installed",
		"transport": "windows-cdp-loopback",
	}
	if got := windowsCurrentAttachStateForCore(proc, last, true, 0); got != "compatibility" {
		t.Fatalf("missing core identity reported %s", got)
	}
	last["corePid"] = "not-a-number"
	if got := windowsCurrentAttachStateForCore(proc, last, true, 2000); got != "unattached" {
		t.Fatalf("invalid core identity reported %s", got)
	}
}

func TestRecordWindowsAttachStateWritesCoreIdentity(t *testing.T) {
	dir := t.TempDir()
	proc := windowsAttachTestProc(42, "current")
	writeAttachCoreRuntime(t, dir, 777)
	if err := recordWindowsAttachState(dir, proc, "reopening", nil); err != nil {
		t.Fatal(err)
	}
	last := ReadAttachState(dir)
	if last["corePid"] != float64(777) || last["status"] != "reopening" {
		t.Fatalf("unexpected state: %+v", last)
	}
	if err := recordWindowsAttachFailure(dir, proc, os.ErrPermission); err != nil {
		t.Fatal(err)
	}
	last = ReadAttachState(dir)
	if last["corePid"] != float64(777) || last["status"] != "error" {
		t.Fatalf("failure lost the core identity: %+v", last)
	}
	if got := windowsCurrentAttachStateForCore(proc, last, false, 778); got != "unattached" {
		t.Fatalf("error from an old core reported %s", got)
	}
}

func TestRecordWindowsAttachStateFallsBackToOwnPID(t *testing.T) {
	dir := t.TempDir()
	proc := windowsAttachTestProc(42, "current")
	writeAttachCoreRuntime(t, dir, 0)
	if err := recordWindowsAttachState(dir, proc, "reopening", nil); err != nil {
		t.Fatal(err)
	}
	if got := ReadAttachState(dir)["corePid"]; got != float64(os.Getpid()) {
		t.Fatalf("fallback core pid = %v", got)
	}
}

func TestRecordWindowsAttachInstalledKeepsCoreIdentity(t *testing.T) {
	dir := t.TempDir()
	proc := windowsAttachTestProc(42, "current")
	writeAttachCoreRuntime(t, dir, 900)
	result := &AttachResult{Status: "installed", Transport: "windows-cdp-loopback", HostPID: 42, Platform: "windows"}
	if err := recordWindowsAttachInstalled(dir, proc, result); err != nil {
		t.Fatal(err)
	}
	last := ReadAttachState(dir)
	if last["status"] != "installed" || last["transport"] != "windows-cdp-loopback" || last["corePid"] != float64(900) {
		t.Fatalf("unexpected installed state: %+v", last)
	}
	if got := windowsCurrentAttachStateForCore(proc, last, true, 900); got != "compatibility" {
		t.Fatalf("same core reported %s", got)
	}
	if got := windowsCurrentAttachStateForCore(proc, last, true, 901); got != "unattached" {
		t.Fatalf("new core reported %s", got)
	}
}

func TestWindowsAttachRetriesBackOffPerProcess(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	retries := newWindowsAttachRetries(500)
	target := windowsAttachTarget{PID: 42, Started: "current"}
	if !retries.due(target, now) {
		t.Fatal("a new candidate must be attempted immediately")
	}
	for attempt := 1; attempt <= windowsAttachMaxAttempts; attempt++ {
		retries.failure(target, now)
		want := windowsAttachRetryDelays[attempt-1]
		if got := retries.entries[target].NextRetryAt.Sub(now); got != want {
			t.Fatalf("attempt %d delay = %s, want %s", attempt, got, want)
		}
		if retries.due(target, now.Add(want-time.Millisecond)) {
			t.Fatalf("attempt %d fired before its retry window", attempt)
		}
		if !retries.due(target, now.Add(want)) {
			t.Fatalf("attempt %d never became due", attempt)
		}
	}
	retries.failure(target, now)
	if retries.due(target, now.Add(time.Hour)) {
		t.Fatal("exhausted retries kept firing")
	}
	if retries.dueAny(map[windowsAttachTarget]bool{target: true}, now.Add(time.Hour)) {
		t.Fatal("exhausted retries still reported as due")
	}
}

func TestWindowsAttachRetriesResetOnNewProcessOrCore(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	retries := newWindowsAttachRetries(500)
	old := windowsAttachTarget{PID: 42, Started: "current"}
	for i := 0; i <= windowsAttachMaxAttempts; i++ {
		retries.failure(old, now)
	}
	if retries.due(old, now.Add(time.Hour)) {
		t.Fatal("old process kept retrying")
	}
	// A restarted host has a different start time and therefore a fresh budget.
	restarted := windowsAttachTarget{PID: 42, Started: "restarted"}
	if !retries.due(restarted, now) {
		t.Fatal("restarted process did not get a fresh budget")
	}
	retries.failure(restarted, now)
	retries.observeCore(600)
	if !retries.due(restarted, now) {
		t.Fatal("core restart did not reset the retry budget")
	}
	retries.success(restarted)
	if retries.due(restarted, now.Add(time.Hour)) {
		t.Fatal("attached process was retried again")
	}
	retries.observeCore(700)
	if !retries.due(restarted, now.Add(time.Hour)) {
		t.Fatal("new core did not invalidate the successful attach")
	}
}

func TestWindowsAttachRetriesKeepPendingVisibleWhileCandidatesUnchanged(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	retries := newWindowsAttachRetries(500)
	target := windowsAttachTarget{PID: 42, Started: "current"}
	targets := map[windowsAttachTarget]bool{target: true}
	if !retries.dueAny(targets, now) {
		t.Fatal("an untried candidate was not due")
	}
	retries.failure(target, now)
	if retries.dueAny(targets, now.Add(time.Second)) {
		t.Fatal("retry fired before the backoff elapsed")
	}
	if !retries.dueAny(targets, now.Add(windowsAttachRetryDelays[0])) {
		t.Fatal("retry did not become due after the backoff")
	}
	retries.block(windowsAttachTarget{PID: 43, Started: "debug"})
	if retries.dueAny(map[windowsAttachTarget]bool{{PID: 43, Started: "debug"}: true}, now.Add(time.Hour)) {
		t.Fatal("blocked debug instance became due")
	}
	retries.prune(map[windowsAttachTarget]bool{{PID: 99, Started: "gone"}: true})
	if len(retries.entries) != 0 {
		t.Fatalf("stale entries were kept: %+v", retries.entries)
	}
}

func TestWindowsCurrentAttachStateFallsBackToFileTimes(t *testing.T) {
	dir := t.TempDir()
	proc := windowsAttachTestProc(42, "current")
	last := map[string]any{
		"hostPid": float64(42), "hostStarted": "current", "status": "installed",
		"transport": "windows-cdp-loopback",
	}
	runtimePath := filepath.Join(dir, "runtime.json")
	statePath := filepath.Join(dir, "attach-state.json")
	writeAttachCoreRuntime(t, dir, 900)
	raw, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// The state was written before this core started, so it belongs to the
	// previous one even though it carries no corePid.
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(statePath, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(runtimePath, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := windowsCurrentAttachStateForDir(dir, proc, last, true, 900); got != "unattached" {
		t.Fatalf("state older than the running core reported %s", got)
	}
	// A state written after the core started describes this core.
	if err := os.Chtimes(statePath, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(runtimePath, old, old); err != nil {
		t.Fatal(err)
	}
	if got := windowsCurrentAttachStateForDir(dir, proc, last, true, 900); got != "compatibility" {
		t.Fatalf("state written after the core started reported %s", got)
	}
	last["corePid"] = float64(900)
	if got := windowsCurrentAttachStateForDir(dir, proc, last, true, 900); got != "compatibility" {
		t.Fatalf("explicit core identity was ignored: %s", got)
	}
}

func TestWindowsLauncherLeaseExpiryAndClear(t *testing.T) {
	dir := t.TempDir()
	proc := windowsAttachTestProc(4444, "2026-11-21T10:00:00Z")
	lease := WindowsLauncherState{
		PID: proc.PID, DebugPort: 9222, OwnerSID: proc.OwnerSID, Executable: proc.Executable,
		Started: proc.Started, LaunchedByUs: true, LaunchedAt: "2026-11-21T09:59:00Z",
	}
	if err := WriteWindowsLauncherState(dir, lease); err != nil {
		t.Fatal(err)
	}
	if windowsLauncherLeaseExpired(&lease, []WindowsProcessInfo{proc}) {
		t.Fatal("live lease reported as expired")
	}
	reused := proc
	reused.Started = "2026-11-21T11:00:00Z"
	if !windowsLauncherLeaseExpired(&lease, []WindowsProcessInfo{reused}) {
		t.Fatal("pid reuse kept a stale lease alive")
	}
	if !windowsLauncherOwnsProcess(&lease, proc) {
		t.Fatal("launcher lease was not recognized for its own process")
	}
	if windowsLauncherOwnsProcess(&lease, reused) {
		t.Fatal("launcher lease matched a reused pid")
	}
	if err := ClearWindowsLauncherState(dir); err != nil {
		t.Fatal(err)
	}
	if ReadWindowsLauncherState(dir) != nil {
		t.Fatal("cleared lease was still readable")
	}
	if err := ClearWindowsLauncherState(dir); err != nil {
		t.Fatalf("clearing an absent lease failed: %v", err)
	}
	if err := ClearWindowsLauncherState(""); err == nil {
		t.Fatal("clearing without a data dir was accepted")
	}
}

func TestWindowsBackgroundAttachOrchestration(t *testing.T) {
	now := time.Date(2026, 11, 21, 14, 0, 0, 0, time.UTC)
	baseProc := WindowsProcessInfo{
		PID:         4242,
		Executable:  `C:\Program Files\Kimi Code\Kimi Code.exe`,
		CommandLine: `"C:\Program Files\Kimi Code\Kimi Code.exe"`,
		OwnerSID:    "S-1-5-21-1000",
		Started:     "2026-11-21T10:00:00Z",
	}
	target := windowsAttachTarget{PID: baseProc.PID, Started: "ft-1"}

	t.Run("launcher-owned compatibility host reattaches after core restart", func(t *testing.T) {
		dir := t.TempDir()
		proc := baseProc
		proc.CommandLine = `"C:\Program Files\Kimi Code\Kimi Code.exe" --remote-debugging-port=9222`
		lease := &WindowsLauncherState{
			PID: proc.PID, DebugPort: 9222, OwnerSID: proc.OwnerSID,
			Executable: proc.Executable, Started: proc.Started, LaunchedByUs: true,
		}
		retries := newWindowsAttachRetries(200)
		var attachCalls int
		var allowDaily bool
		var installedRecorded bool
		runWindowsBackgroundAttachStep(t.Context(), AttachConfig{DataDir: dir, CoreURL: "http://127.0.0.1:54194", PairingToken: "tok"}, retries, 200, windowsBackgroundAttachDeps{
			now:          func() time.Time { return now },
			serviceAlive: func() bool { return true },
			discoverHost: func(context.Context) (WindowsProcessInfo, error) { return proc, nil },
			targetForPID: func(int) (windowsAttachTarget, bool) { return target, true },
			readLease:    func() *WindowsLauncherState { return lease },
			// Old core (100) had compatibility mode; current core is 200.
			readAttachState: func() map[string]any {
				return map[string]any{"status": "installed", "hostPid": float64(proc.PID), "hostStarted": proc.Started, "corePid": float64(100)}
			},
			attach: func(_ context.Context, opts AttachOptions) (*AttachResult, error) {
				attachCalls++
				allowDaily = opts.AllowDailyInstance
				return &AttachResult{Status: "installed", HostPID: proc.PID, Transport: "windows-cdp-loopback"}, nil
			},
			recordInstalled: func(WindowsProcessInfo, *AttachResult) error { installedRecorded = true; return nil },
			recordFailure:   func(WindowsProcessInfo, error) error { t.Fatal("unexpected failure record"); return nil },
		})
		if attachCalls != 1 || !allowDaily || !installedRecorded {
			t.Fatalf("attachCalls=%d allowDaily=%v installedRecorded=%v", attachCalls, allowDaily, installedRecorded)
		}
		if retries.due(target, now.Add(time.Hour)) {
			t.Fatal("attached launcher host remained due for current core")
		}
	})

	t.Run("external debug port host is blocked permanently without attaching", func(t *testing.T) {
		dir := t.TempDir()
		proc := baseProc
		proc.CommandLine = `"C:\Program Files\Kimi Code\Kimi Code.exe" --remote-debugging-port=9222`
		retries := newWindowsAttachRetries(200)
		runWindowsBackgroundAttachStep(t.Context(), AttachConfig{DataDir: dir}, retries, 200, windowsBackgroundAttachDeps{
			now:             func() time.Time { return now },
			serviceAlive:    func() bool { return true },
			discoverHost:    func(context.Context) (WindowsProcessInfo, error) { return proc, nil },
			targetForPID:    func(int) (windowsAttachTarget, bool) { return target, true },
			readLease:       func() *WindowsLauncherState { return nil },
			readAttachState: func() map[string]any { return nil },
			attach: func(context.Context, AttachOptions) (*AttachResult, error) {
				t.Fatal("external debug host must not be attached")
				return nil, nil
			},
			recordInstalled: func(WindowsProcessInfo, *AttachResult) error { return nil },
			recordFailure:   func(WindowsProcessInfo, error) error { return nil },
		})
		if retries.due(target, now.Add(24*time.Hour)) {
			t.Fatal("external debug host was not blocked")
		}
	})

	t.Run("reopening state skips attach without consuming retry budget", func(t *testing.T) {
		dir := t.TempDir()
		retries := newWindowsAttachRetries(200)
		runWindowsBackgroundAttachStep(t.Context(), AttachConfig{DataDir: dir}, retries, 200, windowsBackgroundAttachDeps{
			now:          func() time.Time { return now },
			serviceAlive: func() bool { return true },
			discoverHost: func(context.Context) (WindowsProcessInfo, error) { return baseProc, nil },
			targetForPID: func(int) (windowsAttachTarget, bool) { return target, true },
			readLease:    func() *WindowsLauncherState { return nil },
			readAttachState: func() map[string]any {
				return map[string]any{"status": "reopening", "at": time.Now().UTC().Format(time.RFC3339Nano), "corePid": float64(200)}
			},
			attach: func(context.Context, AttachOptions) (*AttachResult, error) {
				t.Fatal("reopening host must not be attached")
				return nil, nil
			},
			recordInstalled: func(WindowsProcessInfo, *AttachResult) error { return nil },
			recordFailure:   func(WindowsProcessInfo, error) error { return nil },
		})
		if !retries.due(target, now) {
			t.Fatal("reopening skip consumed retry budget")
		}
	})

	t.Run("failure records error and backs off then retries", func(t *testing.T) {
		dir := t.TempDir()
		retries := newWindowsAttachRetries(200)
		var failures int
		var attachCalls int
		current := now
		deps := windowsBackgroundAttachDeps{
			now:             func() time.Time { return current },
			serviceAlive:    func() bool { return true },
			discoverHost:    func(context.Context) (WindowsProcessInfo, error) { return baseProc, nil },
			targetForPID:    func(int) (windowsAttachTarget, bool) { return target, true },
			readLease:       func() *WindowsLauncherState { return nil },
			readAttachState: func() map[string]any { return nil },
			attach: func(context.Context, AttachOptions) (*AttachResult, error) {
				attachCalls++
				return nil, errors.New("temporary attach failure")
			},
			recordInstalled: func(WindowsProcessInfo, *AttachResult) error { return nil },
			recordFailure:   func(WindowsProcessInfo, error) error { failures++; return nil },
		}
		runWindowsBackgroundAttachStep(t.Context(), AttachConfig{DataDir: dir}, retries, 200, deps)
		if attachCalls != 1 || failures != 1 || retries.due(target, current.Add(time.Second)) {
			t.Fatalf("after first failure: calls=%d failures=%d due=%v", attachCalls, failures, retries.due(target, current.Add(time.Second)))
		}
		current = current.Add(windowsAttachRetryDelays[0])
		runWindowsBackgroundAttachStep(t.Context(), AttachConfig{DataDir: dir}, retries, 200, deps)
		if attachCalls != 2 || failures != 2 {
			t.Fatalf("after backoff elapsed: calls=%d failures=%d", attachCalls, failures)
		}
	})

	t.Run("discovery error defers next discovery", func(t *testing.T) {
		retries := newWindowsAttachRetries(200)
		runWindowsBackgroundAttachStep(t.Context(), AttachConfig{DataDir: t.TempDir()}, retries, 200, windowsBackgroundAttachDeps{
			now:             func() time.Time { return now },
			discoverHost:    func(context.Context) (WindowsProcessInfo, error) { return WindowsProcessInfo{}, errors.New("no host") },
			targetForPID:    func(int) (windowsAttachTarget, bool) { return target, true },
			readLease:       func() *WindowsLauncherState { return nil },
			readAttachState: func() map[string]any { return nil },
			attach: func(context.Context, AttachOptions) (*AttachResult, error) {
				t.Fatal("should not attach on discovery error")
				return nil, nil
			},
		})
		if retries.discoveryAllowed(now.Add(time.Second)) {
			t.Fatal("discovery was not deferred after error")
		}
		if !retries.discoveryAllowed(now.Add(windowsAttachRetryDelays[0])) {
			t.Fatal("discovery did not resume after deferral")
		}
	})

	t.Run("concurrent foreground attach skip counts as success without recording failure", func(t *testing.T) {
		dir := t.TempDir()
		retries := newWindowsAttachRetries(200)
		state := map[string]any(nil)
		var sawSkipCurrent bool
		runWindowsBackgroundAttachStep(t.Context(), AttachConfig{DataDir: dir, CoreURL: "http://127.0.0.1:54194", PairingToken: strings.Repeat("t", 48)}, retries, 200, windowsBackgroundAttachDeps{
			now:             func() time.Time { return now },
			serviceAlive:    func() bool { return true },
			discoverHost:    func(context.Context) (WindowsProcessInfo, error) { return baseProc, nil },
			targetForPID:    func(int) (windowsAttachTarget, bool) { return target, true },
			readLease:       func() *WindowsLauncherState { return nil },
			readAttachState: func() map[string]any { return state },
			attach: func(_ context.Context, opts AttachOptions) (*AttachResult, error) {
				sawSkipCurrent = opts.SkipCurrent
				// Simulate foreground attach finishing while background waited on lock.
				state = map[string]any{
					"status":      "installed",
					"transport":   "main-loopback-rpc",
					"hostPid":     float64(baseProc.PID),
					"hostStarted": baseProc.Started,
					"corePid":     float64(200),
				}
				return nil, nil
			},
			recordInstalled: func(WindowsProcessInfo, *AttachResult) error { return nil },
			recordFailure: func(WindowsProcessInfo, error) error {
				t.Fatal("unexpected failure record on concurrent skip")
				return nil
			},
		})
		if !sawSkipCurrent {
			t.Fatal("background attach did not set SkipCurrent=true")
		}
		if retries.due(target, now.Add(time.Hour)) {
			t.Fatal("concurrently attached target was not marked successful")
		}
	})
}

func TestWindowsCDPAttachKeyCoversIdentityFields(t *testing.T) {
	proc := windowsAttachTestProc(4242, "2026-11-26T10:00:00Z")
	base := AttachOptions{
		CoreURL:            "http://127.0.0.1:8321",
		PairingToken:       strings.Repeat("t", 48),
		CoreVersion:        "0.2.10",
		Profile:            `C:\temp\iso`,
		AppVersionOverride: "1.4.2",
	}
	want := windowsCDPAttachKey(proc, 54321, base)
	if got := windowsCDPAttachKey(proc, 54321, base); got != want {
		t.Fatalf("non-deterministic key: %s vs %s", got, want)
	}

	variants := map[string]string{
		"pid":            windowsCDPAttachKey(windowsAttachTestProc(4243, proc.Started), 54321, base),
		"started":        windowsCDPAttachKey(windowsAttachTestProc(4242, "2026-11-26T10:00:01Z"), 54321, base),
		"debugPort":      windowsCDPAttachKey(proc, 54322, base),
		"coreURL":        windowsCDPAttachKey(proc, 54321, AttachOptions{CoreURL: "http://127.0.0.1:8322", PairingToken: base.PairingToken, CoreVersion: base.CoreVersion, Profile: base.Profile, AppVersionOverride: base.AppVersionOverride}),
		"pairingToken":   windowsCDPAttachKey(proc, 54321, AttachOptions{CoreURL: base.CoreURL, PairingToken: strings.Repeat("x", 48), CoreVersion: base.CoreVersion, Profile: base.Profile, AppVersionOverride: base.AppVersionOverride}),
		"coreVersion":    windowsCDPAttachKey(proc, 54321, AttachOptions{CoreURL: base.CoreURL, PairingToken: base.PairingToken, CoreVersion: "0.2.11", Profile: base.Profile, AppVersionOverride: base.AppVersionOverride}),
		"versionChanged": windowsCDPAttachKey(proc, 54321, AttachOptions{CoreURL: base.CoreURL, PairingToken: base.PairingToken, CoreVersion: base.CoreVersion, Profile: base.Profile, AppVersionOverride: "1.9.9"}),
	}
	for name, got := range variants {
		if got == want {
			t.Fatalf("windowsCDPAttachKey ignored %s change", name)
		}
	}
}

func TestAttachWindowsCompatCDPIdempotentSkipAndTornDownRecovery(t *testing.T) {
	dir := t.TempDir()
	selfPID := os.Getpid()
	token := strings.Repeat("t", 48)
	coreSrv, _ := startTestCoreHealthServer(t, token)
	writeAttachCoreRuntime(t, dir, selfPID)
	proc := windowsAttachTestProc(4242, "2026-11-26T10:00:00Z")
	opts := AttachOptions{
		DataDir:            dir,
		CoreURL:            coreSrv.URL,
		PairingToken:       token,
		CoreVersion:        "0.2.10",
		AllowDailyInstance: true,
		SkipCurrent:        true,
	}

	prevAlive := attachHealthyInstanceAlive
	attachHealthyInstanceAlive = func(_ context.Context, corePid, hostPid int, hostStarted string) bool {
		return corePid == selfPID && hostPid == proc.PID && hostStarted == proc.Started
	}
	t.Cleanup(func() { attachHealthyInstanceAlive = prevAlive })

	var verifyCalls, attachCalls int
	deps := windowsCompatCDPDeps{
		verifyReady: func(context.Context) error {
			verifyCalls++
			return nil
		},
		attachCDP: func(_ context.Context, cdpOpts WindowsCDPAttachOptions) (*AttachResult, error) {
			attachCalls++
			return &AttachResult{
				Status:      "installed",
				Transport:   "windows-cdp-loopback",
				Protocol:    1,
				AppVersion:  "1.4.2",
				Electron:    SupportedElectron,
				HostPID:     cdpOpts.HostPID,
				Platform:    "windows",
				DebugPort:   cdpOpts.DebugPort,
				hostStarted: cdpOpts.HostStarted,
			}, nil
		},
	}

	// First compat attach injects and persists attachKey + hostStarted.
	res, err := attachWindowsCompatCDP(context.Background(), opts, proc, 54321, false, deps)
	if err != nil || res == nil || res.Status != "installed" {
		t.Fatalf("first compat attach failed: res=%+v err=%v", res, err)
	}
	wantKey := windowsCDPAttachKey(proc, 54321, opts)
	state := ReadAttachState(dir)
	if state["status"] != "installed" || state["attachKey"] != wantKey || state["hostStarted"] != proc.Started || state["corePid"] != float64(selfPID) {
		t.Fatalf("unexpected persisted compat state: %+v", state)
	}

	// Second attach with SkipCurrent=true verifies readiness and live core before skipping AttachWindowsCDP.
	res, err = attachWindowsCompatCDP(context.Background(), opts, proc, 54321, false, deps)
	if err != nil || res != nil {
		t.Fatalf("second compat attach did not skip: res=%+v err=%v", res, err)
	}
	if verifyCalls != 2 || attachCalls != 1 {
		t.Fatalf("second compat attach verify/attach mismatch: verify=%d attach=%d", verifyCalls, attachCalls)
	}

	// Lock contention while another attach holds host-attach.lock fails cleanly without tearing down.
	held, err := acquireHostAttachLock(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	lockCtx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	_, lockErr := attachWindowsCompatCDP(lockCtx, opts, proc, 54321, false, deps)
	cancel()
	_ = held.Close()
	if lockErr == nil || IsBridgeTornDown(lockErr) {
		t.Fatalf("expected non-torn-down lock error, got: %v", lockErr)
	}

	// Force reattach failing before touching renderer preserves healthy installed state.
	forceOpts := opts
	forceOpts.SkipCurrent = false
	preTouchErrDeps := deps
	preTouchErrDeps.attachCDP = func(context.Context, WindowsCDPAttachOptions) (*AttachResult, error) {
		return nil, errors.New("无法连接调试端口 127.0.0.1:54321：connection refused")
	}
	_, err = attachWindowsCompatCDP(context.Background(), forceOpts, proc, 54321, false, preTouchErrDeps)
	if err == nil || IsBridgeTornDown(err) {
		t.Fatalf("pre-touch compat error should not be marked torn-down: %v", err)
	}
	if err := RecordAttach(dir, err); err != nil {
		t.Fatal(err)
	}
	state = ReadAttachState(dir)
	if state["status"] != "installed" || state["attachKey"] != wantKey {
		t.Fatalf("pre-touch compat error overwrote healthy state: %+v", state)
	}

	// Force reattach failing during renderer injection marks torn-down and clears attachKey.
	tornDownDeps := deps
	tornDownDeps.attachCDP = func(context.Context, WindowsCDPAttachOptions) (*AttachResult, error) {
		return nil, errors.New("Windows 渲染页面注入失败（app://renderer/index.html）：eval error")
	}
	_, err = attachWindowsCompatCDP(context.Background(), forceOpts, proc, 54321, false, tornDownDeps)
	if err == nil || !IsBridgeTornDown(err) {
		t.Fatalf("renderer injection failure was not marked torn-down: %v", err)
	}
	state = ReadAttachState(dir)
	if state["status"] != "error" || state["attachKey"] != nil {
		t.Fatalf("renderer injection failure did not clear installed state: %+v", state)
	}

	// Next SkipCurrent=true attach does not skip and recovers cleanly.
	res, err = attachWindowsCompatCDP(context.Background(), opts, proc, 54321, false, deps)
	if err != nil || res == nil || res.Status != "installed" {
		t.Fatalf("recovery compat attach failed: res=%+v err=%v", res, err)
	}
	if attachCalls != 2 {
		t.Fatalf("expected second successful attach after teardown, attachCalls=%d", attachCalls)
	}

	// Core restart invalidates SkipCurrent even for identical host and debugPort.
	writeAttachCoreRuntime(t, dir, selfPID+1000)
	res, err = attachWindowsCompatCDP(context.Background(), opts, proc, 54321, false, deps)
	if err != nil || res == nil || attachCalls != 3 {
		t.Fatalf("core restart did not re-inject compat bridge: res=%+v err=%v attachCalls=%d", res, err, attachCalls)
	}
}

func TestRecordWindowsAttachFailurePreservesHealthyUnlessTornDown(t *testing.T) {
	dir := t.TempDir()
	proc := windowsAttachTestProc(4242, "2026-11-26T10:00:00.123456Z")
	writeAttachCoreRuntime(t, dir, 5100)
	installed := &AttachResult{
		Status:      "installed",
		Transport:   "windows-cdp-loopback",
		HostPID:     proc.PID,
		Platform:    "windows",
		DebugPort:   54321,
		attachKey:   "compat-key",
		hostStarted: proc.Started,
	}
	if err := recordWindowsAttachInstalled(dir, proc, installed); err != nil {
		t.Fatal(err)
	}

	alive := true
	prevAlive := attachHealthyInstanceAlive
	attachHealthyInstanceAlive = func(_ context.Context, corePid, hostPid int, hostStarted string) bool {
		return alive && corePid == 5100 && hostPid == proc.PID && hostStarted == proc.Started
	}
	t.Cleanup(func() { attachHealthyInstanceAlive = prevAlive })

	// Background transient failure after foreground already installed must not overwrite installed state.
	if err := recordWindowsAttachFailure(dir, proc, errors.New("等待其他 KimiCode+ 接入流程完成超时")); err != nil {
		t.Fatal(err)
	}
	state := ReadAttachState(dir)
	if state["status"] != "installed" || state["attachKey"] != "compat-key" {
		t.Fatalf("transient failure overwrote installed state: %+v", state)
	}

	// Different host lifetime (PID reuse) must not be preserved as healthy for the new process.
	reused := proc
	reused.Started = "2026-11-26T10:05:00.000000Z"
	if err := recordWindowsAttachFailure(dir, reused, errors.New("reused host failed")); err != nil {
		t.Fatal(err)
	}
	state = ReadAttachState(dir)
	if state["status"] != "error" || state["hostStarted"] != reused.Started {
		t.Fatalf("PID reuse failure wrongly preserved previous host state: %+v", state)
	}

	// Restore installed state and confirm torn-down error overwrites even while host & core are alive.
	if err := recordWindowsAttachInstalled(dir, proc, installed); err != nil {
		t.Fatal(err)
	}
	if err := recordWindowsAttachFailure(dir, proc, MarkBridgeTornDown(errors.New("Windows 渲染页面未确认挂载成功"))); err != nil {
		t.Fatal(err)
	}
	state = ReadAttachState(dir)
	if state["status"] != "error" || state["attachKey"] != nil {
		t.Fatalf("torn-down failure did not overwrite installed state: %+v", state)
	}

	// Restore installed state and confirm dead host/core allows error write.
	if err := recordWindowsAttachInstalled(dir, proc, installed); err != nil {
		t.Fatal(err)
	}
	alive = false
	if err := recordWindowsAttachFailure(dir, proc, errors.New("host exited")); err != nil {
		t.Fatal(err)
	}
	state = ReadAttachState(dir)
	if state["status"] != "error" {
		t.Fatalf("dead host failure did not overwrite installed state: %+v", state)
	}
}

func TestAttachWindowsCompatCDPVerifyReadyFailureDoesNotSkipAndRecordsError(t *testing.T) {
	dir := t.TempDir()
	selfPID := os.Getpid()
	token := strings.Repeat("t", 48)
	coreSrv, _ := startTestCoreHealthServer(t, token)
	writeAttachCoreRuntime(t, dir, selfPID)
	proc := windowsAttachTestProc(4242, "2026-11-26T10:00:00Z")
	opts := AttachOptions{
		DataDir:            dir,
		CoreURL:            coreSrv.URL,
		PairingToken:       token,
		CoreVersion:        "0.2.10",
		AllowDailyInstance: true,
		SkipCurrent:        true,
	}

	alive := true
	prevAlive := attachHealthyInstanceAlive
	attachHealthyInstanceAlive = func(_ context.Context, corePid, hostPid int, hostStarted string) bool {
		return alive && corePid == selfPID && hostPid == proc.PID && hostStarted == proc.Started
	}
	t.Cleanup(func() { attachHealthyInstanceAlive = prevAlive })

	attachCalls := 0
	deps := windowsCompatCDPDeps{
		verifyReady: func(context.Context) error { return nil },
		attachCDP: func(_ context.Context, cdpOpts WindowsCDPAttachOptions) (*AttachResult, error) {
			attachCalls++
			return &AttachResult{
				Status:      "installed",
				Transport:   "windows-cdp-loopback",
				Protocol:    1,
				AppVersion:  "1.4.2",
				Electron:    SupportedElectron,
				HostPID:     cdpOpts.HostPID,
				Platform:    "windows",
				DebugPort:   cdpOpts.DebugPort,
				hostStarted: cdpOpts.HostStarted,
			}, nil
		},
	}

	res, err := attachWindowsCompatCDP(context.Background(), opts, proc, 54321, false, deps)
	if err != nil || res == nil || !IsAttachStateRecordedResult(res) {
		t.Fatalf("initial compat attach failed: res=%+v err=%v", res, err)
	}

	// Host exited before lock acquisition: verifyReady fails -> must NOT skip and must record error in lock.
	alive = false
	exitedDeps := deps
	exitedDeps.verifyReady = func(context.Context) error {
		return errors.New("Windows 宿主进程在接入过程中退出")
	}
	res, err = attachWindowsCompatCDP(context.Background(), opts, proc, 54321, false, exitedDeps)
	if res != nil || err == nil {
		t.Fatalf("compat attach skipped after verifyReady failure: res=%+v err=%v", res, err)
	}
	if attachCalls != 1 {
		t.Fatalf("attachCDP should not run after verifyReady failure: attachCalls=%d", attachCalls)
	}
	if !IsAttachStateRecorded(err) {
		t.Fatalf("verifyReady failure inside lock was not marked stateRecorded: %v", err)
	}
	state := ReadAttachState(dir)
	if state["status"] != "error" || state["attachKey"] != nil {
		t.Fatalf("verifyReady failure did not record error and clear attachKey: %+v", state)
	}
}

func TestAttachWindowsCompatCDPMissingRuntimeOldCoreDoesNotSkip(t *testing.T) {
	dir := t.TempDir()
	token := strings.Repeat("t", 48)
	coreSrv, _ := startTestCoreHealthServer(t, token)
	proc := windowsAttachTestProc(4242, "2026-11-26T10:00:00Z")
	opts := AttachOptions{
		DataDir:            dir,
		CoreURL:            coreSrv.URL,
		PairingToken:       token,
		CoreVersion:        "0.2.10",
		AllowDailyInstance: true,
		SkipCurrent:        true,
	}
	key := windowsCDPAttachKey(proc, 54321, opts)
	if err := writeAttachState(dir, map[string]any{
		"status":      "installed",
		"transport":   "windows-cdp-loopback",
		"hostPid":     proc.PID,
		"hostStarted": proc.Started,
		"attachKey":   key,
		"corePid":     os.Getpid() + 99999,
	}); err != nil {
		t.Fatal(err)
	}

	attachCalls := 0
	res, err := attachWindowsCompatCDP(context.Background(), opts, proc, 54321, false, windowsCompatCDPDeps{
		verifyReady: func(context.Context) error { return nil },
		attachCDP: func(_ context.Context, cdpOpts WindowsCDPAttachOptions) (*AttachResult, error) {
			attachCalls++
			return &AttachResult{
				Status:      "installed",
				Transport:   "windows-cdp-loopback",
				Protocol:    1,
				HostPID:     cdpOpts.HostPID,
				Platform:    "windows",
				DebugPort:   cdpOpts.DebugPort,
				hostStarted: cdpOpts.HostStarted,
			}, nil
		},
	})
	if err != nil || res == nil || attachCalls != 1 {
		t.Fatalf("missing runtime.json with old corePid wrongly skipped: res=%+v err=%v attachCalls=%d", res, err, attachCalls)
	}
	state := ReadAttachState(dir)
	if state["status"] != "installed" || state["corePid"] != float64(os.Getpid()) || state["attachKey"] != key {
		t.Fatalf("unexpected refreshed compat state: %+v", state)
	}
}

func TestRunWindowsBackgroundAttachStepSkipsDuplicateRecordWhenAlreadyRecorded(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	proc := windowsAttachTestProc(4242, "2026-11-26T10:00:00Z")
	target := windowsAttachTarget{PID: proc.PID, Started: "1:2"}

	t.Run("already recorded result does not call recordInstalled", func(t *testing.T) {
		retries := newWindowsAttachRetries(200)
		runWindowsBackgroundAttachStep(t.Context(), AttachConfig{DataDir: t.TempDir(), CoreURL: "http://127.0.0.1:54194", PairingToken: strings.Repeat("t", 48)}, retries, 200, windowsBackgroundAttachDeps{
			now:             func() time.Time { return now },
			serviceAlive:    func() bool { return true },
			discoverHost:    func(context.Context) (WindowsProcessInfo, error) { return proc, nil },
			targetForPID:    func(int) (windowsAttachTarget, bool) { return target, true },
			readLease:       func() *WindowsLauncherState { return nil },
			readAttachState: func() map[string]any { return nil },
			attach: func(context.Context, AttachOptions) (*AttachResult, error) {
				return MarkAttachStateRecordedResult(&AttachResult{Status: "installed", HostPID: proc.PID, Transport: "main-loopback-rpc"}), nil
			},
			recordInstalled: func(WindowsProcessInfo, *AttachResult) error {
				t.Fatal("recordInstalled called out-of-lock for already-recorded result")
				return nil
			},
			recordFailure: func(WindowsProcessInfo, error) error {
				t.Fatal("unexpected recordFailure")
				return nil
			},
		})
		if retries.due(target, now.Add(time.Hour)) {
			t.Fatal("already-recorded attach did not mark target successful")
		}
	})

	t.Run("already recorded error does not call recordFailure", func(t *testing.T) {
		retries := newWindowsAttachRetries(200)
		runWindowsBackgroundAttachStep(t.Context(), AttachConfig{DataDir: t.TempDir(), CoreURL: "http://127.0.0.1:54194", PairingToken: strings.Repeat("t", 48)}, retries, 200, windowsBackgroundAttachDeps{
			now:             func() time.Time { return now },
			serviceAlive:    func() bool { return true },
			discoverHost:    func(context.Context) (WindowsProcessInfo, error) { return proc, nil },
			targetForPID:    func(int) (windowsAttachTarget, bool) { return target, true },
			readLease:       func() *WindowsLauncherState { return nil },
			readAttachState: func() map[string]any { return nil },
			attach: func(context.Context, AttachOptions) (*AttachResult, error) {
				return nil, MarkAttachStateRecorded(MarkBridgeTornDown(errors.New("torn down in lock")))
			},
			recordInstalled: func(WindowsProcessInfo, *AttachResult) error {
				t.Fatal("unexpected recordInstalled")
				return nil
			},
			recordFailure: func(WindowsProcessInfo, error) error {
				t.Fatal("recordFailure called out-of-lock for already-recorded error")
				return nil
			},
		})
		if retries.due(target, now.Add(time.Second)) {
			t.Fatal("already-recorded failure did not schedule backoff")
		}
	})

	t.Run("persist-failed error does not call recordFailure out of lock", func(t *testing.T) {
		retries := newWindowsAttachRetries(200)
		runWindowsBackgroundAttachStep(t.Context(), AttachConfig{DataDir: t.TempDir(), CoreURL: "http://127.0.0.1:54194", PairingToken: strings.Repeat("t", 48)}, retries, 200, windowsBackgroundAttachDeps{
			now:             func() time.Time { return now },
			serviceAlive:    func() bool { return true },
			discoverHost:    func(context.Context) (WindowsProcessInfo, error) { return proc, nil },
			targetForPID:    func(int) (windowsAttachTarget, bool) { return target, true },
			readLease:       func() *WindowsLauncherState { return nil },
			readAttachState: func() map[string]any { return nil },
			attach: func(context.Context, AttachOptions) (*AttachResult, error) {
				return nil, MarkAttachPersistFailed(errors.New("写入接入状态文件失败：disk full"))
			},
			recordInstalled: func(WindowsProcessInfo, *AttachResult) error {
				t.Fatal("unexpected recordInstalled")
				return nil
			},
			recordFailure: func(WindowsProcessInfo, error) error {
				t.Fatal("recordFailure called out-of-lock for persist-failed error")
				return nil
			},
		})
		if retries.due(target, now.Add(time.Second)) {
			t.Fatal("persist-failed attach did not schedule backoff")
		}
	})
}

func TestAttachWindowsWithFallbackSingleLockTransaction(t *testing.T) {
	t.Run("live teardown fallback holds single lock so concurrent B cannot be overwritten", func(t *testing.T) {
		dir := t.TempDir()
		selfPID := os.Getpid()
		token := strings.Repeat("f", 48)
		coreSrv, _ := startTestCoreHealthServer(t, token)
		writeAttachCoreRuntime(t, dir, selfPID)
		proc := windowsAttachTestProc(4242, "2026-11-26T10:00:00Z")
		opts := AttachOptions{
			DataDir:            dir,
			CoreURL:            coreSrv.URL,
			PairingToken:       token,
			CoreVersion:        "0.2.10",
			AllowDailyInstance: true,
			SkipCurrent:        true,
		}

		prevAlive := attachHealthyInstanceAlive
		attachHealthyInstanceAlive = func(_ context.Context, corePid, hostPid int, hostStarted string) bool {
			return corePid == selfPID && hostPid == proc.PID && hostStarted == proc.Started
		}
		t.Cleanup(func() { attachHealthyInstanceAlive = prevAlive })

		aInLive := make(chan struct{})
		bLaunched := make(chan struct{})
		var aCompatDone atomic.Bool
		var bEnteredLock atomic.Bool
		var bSawAError atomic.Bool

		errChA := make(chan error, 1)
		resChB := make(chan *AttachResult, 1)
		errChB := make(chan error, 1)

		go func() {
			_, err := attachWindowsWithFallback(context.Background(), opts, proc, 54321, windowsAttachFallbackDeps{
				tryLive: true,
				liveAttach: func(context.Context, AttachOptions, WindowsProcessInfo) (*AttachResult, error) {
					close(aInLive)
					<-bLaunched
					if ReadAttachState(dir) != nil {
						t.Errorf("unexpected premature attach-state.json during liveAttach")
					}
					return nil, errors.New("Windows 主进程增强桥接入失败：main teardown")
				},
				compat: windowsCompatCDPDeps{
					verifyReady: func(context.Context) error {
						// Must still hold the exact same host-attach.lock without releasing or writing mid-flight.
						if ReadAttachState(dir) != nil {
							t.Errorf("liveAttach failure wrote attach-state.json before compat fallback")
						}
						time.Sleep(80 * time.Millisecond)
						if bEnteredLock.Load() {
							t.Errorf("concurrent B acquired host-attach.lock between liveAttach and compat fallback")
						}
						aCompatDone.Store(true)
						return errors.New("verifyReady failed after teardown")
					},
					attachCDP: func(context.Context, WindowsCDPAttachOptions) (*AttachResult, error) {
						t.Errorf("attachCDP should not run after verifyReady failure")
						return nil, nil
					},
				},
			})
			errChA <- err
		}()

		<-aInLive
		go func() {
			close(bLaunched)
			res, err := attachWindowsCompatCDP(context.Background(), opts, proc, 54321, false, windowsCompatCDPDeps{
				verifyReady: func(context.Context) error {
					bEnteredLock.Store(true)
					if !aCompatDone.Load() {
						t.Errorf("B entered lock before A finished compat fallback")
					}
					if st := ReadAttachState(dir); st != nil && st["status"] == "error" {
						bSawAError.Store(true)
					}
					return nil
				},
				attachCDP: func(_ context.Context, cdpOpts WindowsCDPAttachOptions) (*AttachResult, error) {
					return &AttachResult{
						Status:      "installed",
						Transport:   "windows-cdp-loopback",
						Protocol:    1,
						AppVersion:  "1.4.2",
						Electron:    SupportedElectron,
						HostPID:     cdpOpts.HostPID,
						Platform:    "windows",
						DebugPort:   cdpOpts.DebugPort,
						hostStarted: cdpOpts.HostStarted,
					}, nil
				},
			})
			resChB <- res
			errChB <- err
		}()

		errA := <-errChA
		resB := <-resChB
		errB := <-errChB

		if errA == nil || !IsBridgeTornDown(errA) || !IsAttachStateRecorded(errA) {
			t.Fatalf("expected recorded torn-down error from A, got: %v", errA)
		}
		if errB != nil || resB == nil || resB.Status != "installed" || !IsAttachStateRecordedResult(resB) {
			t.Fatalf("expected recorded installed result from B, got res=%+v err=%v", resB, errB)
		}
		if !bSawAError.Load() {
			t.Fatal("B did not observe A's committed error state after acquiring the lock")
		}
		// Even if an outer fallback invokes recordWindowsAttachFailure with errA, B's installed state stays intact.
		if err := recordWindowsAttachFailure(dir, proc, errA); err != nil {
			t.Fatal(err)
		}
		wantKey := windowsCDPAttachKey(proc, 54321, opts)
		state := ReadAttachState(dir)
		if state["status"] != "installed" || state["attachKey"] != wantKey {
			t.Fatalf("B's installed state was overwritten after A fallback failure: %+v", state)
		}
	})

	t.Run("live teardown failure followed by compat CDP success commits compat installed once", func(t *testing.T) {
		dir := t.TempDir()
		selfPID := os.Getpid()
		token := strings.Repeat("g", 48)
		coreSrv, _ := startTestCoreHealthServer(t, token)
		writeAttachCoreRuntime(t, dir, selfPID)
		proc := windowsAttachTestProc(4242, "2026-11-26T10:00:00Z")
		opts := AttachOptions{
			DataDir:            dir,
			CoreURL:            coreSrv.URL,
			PairingToken:       token,
			CoreVersion:        "0.2.10",
			AllowDailyInstance: true,
			SkipCurrent:        true,
		}

		res, err := attachWindowsWithFallback(context.Background(), opts, proc, 54321, windowsAttachFallbackDeps{
			tryLive: true,
			liveAttach: func(context.Context, AttachOptions, WindowsProcessInfo) (*AttachResult, error) {
				return nil, errors.New("Windows 宿主未确认主进程桥已安装")
			},
			compat: windowsCompatCDPDeps{
				verifyReady: func(context.Context) error {
					if ReadAttachState(dir) != nil {
						t.Errorf("liveAttach failure prematurely persisted attach-state.json before compat fallback")
					}
					return nil
				},
				attachCDP: func(_ context.Context, cdpOpts WindowsCDPAttachOptions) (*AttachResult, error) {
					return &AttachResult{
						Status:      "installed",
						Transport:   "windows-cdp-loopback",
						Protocol:    1,
						AppVersion:  "1.4.2",
						Electron:    SupportedElectron,
						HostPID:     cdpOpts.HostPID,
						Platform:    "windows",
						DebugPort:   cdpOpts.DebugPort,
						hostStarted: cdpOpts.HostStarted,
					}, nil
				},
			},
		})
		if err != nil || res == nil || res.Status != "installed" || res.Transport != "windows-cdp-loopback" {
			t.Fatalf("compat fallback after live teardown failed: res=%+v err=%v", res, err)
		}
		if !IsAttachStateRecordedResult(res) {
			t.Fatal("compat fallback result was not marked stateRecorded")
		}
		wantKey := windowsCDPAttachKey(proc, 54321, opts)
		state := ReadAttachState(dir)
		if state["status"] != "installed" || state["transport"] != "windows-cdp-loopback" || state["attachKey"] != wantKey {
			t.Fatalf("final persisted state mismatch after compat fallback success: %+v", state)
		}
	})
}

func TestAttachWindowsCompatCDPAndFallbackPersistFailure(t *testing.T) {
	proc := windowsAttachTestProc(4242, "2026-11-26T10:00:00Z")
	token := strings.Repeat("h", 48)

	t.Run("compat CDP success with persist failure returns error without stateRecorded", func(t *testing.T) {
		dir := t.TempDir()
		opts := AttachOptions{
			DataDir:            dir,
			CoreURL:            "http://127.0.0.1:8321",
			PairingToken:       token,
			CoreVersion:        "0.2.10",
			AllowDailyInstance: true,
		}
		res, err := attachWindowsCompatCDP(context.Background(), opts, proc, 54321, false, windowsCompatCDPDeps{
			verifyReady: func(context.Context) error { return nil },
			attachCDP: func(_ context.Context, cdpOpts WindowsCDPAttachOptions) (*AttachResult, error) {
				if mkErr := os.MkdirAll(filepath.Join(dir, "attach-state.json", "blocker"), 0o700); mkErr != nil {
					t.Errorf("mkdir blocker: %v", mkErr)
				}
				return &AttachResult{
					Status:      "installed",
					Transport:   "windows-cdp-loopback",
					Protocol:    1,
					HostPID:     cdpOpts.HostPID,
					Platform:    "windows",
					DebugPort:   cdpOpts.DebugPort,
					hostStarted: cdpOpts.HostStarted,
				}, nil
			},
		})
		if res != nil || err == nil {
			t.Fatalf("expected persist error on compat success write failure, got res=%+v err=%v", res, err)
		}
		if !IsAttachPersistFailed(err) || IsAttachStateRecorded(err) {
			t.Fatalf("unexpected error markers: persistFailed=%v stateRecorded=%v err=%v", IsAttachPersistFailed(err), IsAttachStateRecorded(err), err)
		}
		if !strings.Contains(err.Error(), "写入接入状态文件失败") {
			t.Fatalf("expected post-CDP commit persist error, got: %v", err)
		}
	})

	t.Run("compat CDP torn-down failure with persist failure merges errors without stateRecorded", func(t *testing.T) {
		dir := t.TempDir()
		opts := AttachOptions{
			DataDir:            dir,
			CoreURL:            "http://127.0.0.1:8321",
			PairingToken:       token,
			CoreVersion:        "0.2.10",
			AllowDailyInstance: true,
		}
		res, err := attachWindowsCompatCDP(context.Background(), opts, proc, 54321, false, windowsCompatCDPDeps{
			verifyReady: func(context.Context) error { return nil },
			attachCDP: func(context.Context, WindowsCDPAttachOptions) (*AttachResult, error) {
				if mkErr := os.MkdirAll(filepath.Join(dir, "attach-state.json", "blocker"), 0o700); mkErr != nil {
					t.Errorf("mkdir blocker: %v", mkErr)
				}
				return nil, errors.New("Windows 渲染页面注入失败（app://renderer/index.html）：boom")
			},
		})
		if res != nil || err == nil {
			t.Fatalf("expected merged torn-down + persist error, got res=%+v err=%v", res, err)
		}
		if !IsBridgeTornDown(err) || !IsAttachPersistFailed(err) || IsAttachStateRecorded(err) {
			t.Fatalf("unexpected error markers: tornDown=%v persistFailed=%v stateRecorded=%v err=%v", IsBridgeTornDown(err), IsAttachPersistFailed(err), IsAttachStateRecorded(err), err)
		}
		if !strings.Contains(err.Error(), "Windows 渲染页面注入失败") || !strings.Contains(err.Error(), "写入接入状态文件失败") {
			t.Fatalf("merged error missing expected details: %v", err)
		}
	})

	t.Run("RunWindowsLauncher success with persist failure returns persist error", func(t *testing.T) {
		dir := t.TempDir()
		rawRuntime, err := json.Marshal(CoreRuntimeRecord{
			URL:          "http://127.0.0.1:8321",
			PairingToken: token,
			PID:          os.Getpid(),
			Version:      "0.2.10",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "runtime.json"), rawRuntime, 0o600); err != nil {
			t.Fatal(err)
		}
		reusedProc := proc
		reusedProc.CommandLine = `"C:\Users\me\AppData\Local\Programs\Kimi Code\Kimi Code.exe" --remote-debugging-port=54321 --remote-debugging-address=127.0.0.1`
		if err := WriteWindowsLauncherState(dir, WindowsLauncherState{
			PID:          reusedProc.PID,
			DebugPort:    54321,
			OwnerSID:     reusedProc.OwnerSID,
			Executable:   reusedProc.Executable,
			Started:      reusedProc.Started,
			LaunchedByUs: true,
		}); err != nil {
			t.Fatal(err)
		}
		launchRes, launchErr := RunWindowsLauncher(context.Background(), WindowsLaunchOptions{
			DataDir:            dir,
			KimiExecutable:     reusedProc.Executable,
			AllowDailyInstance: true,
		}, WindowsLauncherDeps{
			CurrentSID:    func(context.Context) (string, error) { return reusedProc.OwnerSID, nil },
			ListProcesses: func(context.Context) ([]WindowsProcessInfo, error) { return []WindowsProcessInfo{reusedProc}, nil },
			ListListeners: func(context.Context, int) ([]WindowsPortListener, error) {
				return []WindowsPortListener{{PID: reusedProc.PID, LocalAddress: "127.0.0.1", LocalPort: 54321}}, nil
			},
			VerifyBinary: func(string) (WindowsBinaryInfo, error) {
				return WindowsBinaryInfo{ElectronPresent: true, InspectorFuseOn: true}, nil
			},
			SpawnDetached:   func(string, []string) (int, error) { t.Fatal("unexpected SpawnDetached"); return 0, nil },
			CheckCoreHealth: func(context.Context, string) bool { return true },
			SelfPID:         os.Getpid,
			AttachCDP: func(_ context.Context, cdpOpts WindowsCDPAttachOptions) (*AttachResult, error) {
				if mkErr := os.MkdirAll(filepath.Join(dir, "attach-state.json", "blocker"), 0o700); mkErr != nil {
					t.Errorf("mkdir blocker: %v", mkErr)
				}
				return &AttachResult{
					Status:      "installed",
					Transport:   "windows-cdp-loopback",
					Protocol:    1,
					HostPID:     cdpOpts.HostPID,
					Platform:    "windows",
					DebugPort:   cdpOpts.DebugPort,
					hostStarted: cdpOpts.HostStarted,
				}, nil
			},
		})
		if launchRes != nil || launchErr == nil {
			t.Fatalf("expected persist failure from RunWindowsLauncher, got res=%+v err=%v", launchRes, launchErr)
		}
		if !IsAttachPersistFailed(launchErr) || IsAttachStateRecorded(launchErr) {
			t.Fatalf("unexpected RunWindowsLauncher error markers: persistFailed=%v stateRecorded=%v err=%v", IsAttachPersistFailed(launchErr), IsAttachStateRecorded(launchErr), launchErr)
		}
		if !strings.Contains(launchErr.Error(), "写入接入状态文件失败") {
			t.Fatalf("expected post-AttachCDP commit persist error, got: %v", launchErr)
		}
	})
}

func TestAttachWindowsCompatCDPSkipCurrentRequiresLiveCore(t *testing.T) {
	proc := windowsAttachTestProc(4242, "2026-11-26T10:00:00Z")
	token := strings.Repeat("m", 48)
	newDeps := func(attachCalls *int) windowsCompatCDPDeps {
		return windowsCompatCDPDeps{
			verifyReady: func(context.Context) error { return nil },
			attachCDP: func(_ context.Context, cdpOpts WindowsCDPAttachOptions) (*AttachResult, error) {
				*attachCalls++
				return &AttachResult{
					Status:      "installed",
					Transport:   "windows-cdp-loopback",
					Protocol:    1,
					HostPID:     cdpOpts.HostPID,
					Platform:    "windows",
					DebugPort:   cdpOpts.DebugPort,
					hostStarted: cdpOpts.HostStarted,
				}, nil
			},
		}
	}

	t.Run("closed core server with stale runtime.json does not skip", func(t *testing.T) {
		dir := t.TempDir()
		selfPID := os.Getpid()
		coreSrv, _ := startTestCoreHealthServer(t, token)
		opts := AttachOptions{
			DataDir:            dir,
			CoreURL:            coreSrv.URL,
			PairingToken:       token,
			CoreVersion:        "0.2.10",
			AllowDailyInstance: true,
			SkipCurrent:        true,
		}
		rawRuntime, _ := json.Marshal(map[string]any{"url": coreSrv.URL, "pairingToken": token, "pid": selfPID})
		if err := os.WriteFile(filepath.Join(dir, "runtime.json"), rawRuntime, 0o600); err != nil {
			t.Fatal(err)
		}
		key := windowsCDPAttachKey(proc, 54321, opts)
		if err := writeAttachState(dir, map[string]any{
			"status":      "installed",
			"transport":   "windows-cdp-loopback",
			"hostPid":     proc.PID,
			"hostStarted": proc.Started,
			"attachKey":   key,
			"corePid":     selfPID,
		}); err != nil {
			t.Fatal(err)
		}

		attachCalls := 0
		deps := newDeps(&attachCalls)
		res, err := attachWindowsCompatCDP(context.Background(), opts, proc, 54321, false, deps)
		if res != nil || err != nil || attachCalls != 0 {
			t.Fatalf("live compat core did not skip: res=%+v err=%v attachCalls=%d", res, err, attachCalls)
		}

		coreSrv.Close()
		res, err = attachWindowsCompatCDP(context.Background(), opts, proc, 54321, false, deps)
		if err != nil || res == nil || attachCalls != 1 {
			t.Fatalf("dead compat core with stale runtime.json wrongly skipped: res=%+v err=%v attachCalls=%d", res, err, attachCalls)
		}
	})

	t.Run("bearer token mismatch does not skip", func(t *testing.T) {
		dir := t.TempDir()
		selfPID := os.Getpid()
		coreSrv, _ := startTestCoreHealthServer(t, strings.Repeat("n", 48))
		opts := AttachOptions{
			DataDir:            dir,
			CoreURL:            coreSrv.URL,
			PairingToken:       token,
			CoreVersion:        "0.2.10",
			AllowDailyInstance: true,
			SkipCurrent:        true,
		}
		writeAttachCoreRuntime(t, dir, selfPID)
		key := windowsCDPAttachKey(proc, 54321, opts)
		if err := writeAttachState(dir, map[string]any{
			"status":      "installed",
			"transport":   "windows-cdp-loopback",
			"hostPid":     proc.PID,
			"hostStarted": proc.Started,
			"attachKey":   key,
			"corePid":     selfPID,
		}); err != nil {
			t.Fatal(err)
		}

		attachCalls := 0
		res, err := attachWindowsCompatCDP(context.Background(), opts, proc, 54321, false, newDeps(&attachCalls))
		if err != nil || res == nil || attachCalls != 1 {
			t.Fatalf("401 compat core wrongly skipped: res=%+v err=%v attachCalls=%d", res, err, attachCalls)
		}
	})

	t.Run("exited corePID does not skip", func(t *testing.T) {
		dir := t.TempDir()
		deadPID := exitedTestPID(t)
		coreSrv, _ := startTestCoreHealthServer(t, token)
		opts := AttachOptions{
			DataDir:            dir,
			CoreURL:            coreSrv.URL,
			PairingToken:       token,
			CoreVersion:        "0.2.10",
			AllowDailyInstance: true,
			SkipCurrent:        true,
		}
		writeAttachCoreRuntime(t, dir, deadPID)
		key := windowsCDPAttachKey(proc, 54321, opts)
		if err := writeAttachState(dir, map[string]any{
			"status":      "installed",
			"transport":   "windows-cdp-loopback",
			"hostPid":     proc.PID,
			"hostStarted": proc.Started,
			"attachKey":   key,
			"corePid":     deadPID,
		}); err != nil {
			t.Fatal(err)
		}

		attachCalls := 0
		res, err := attachWindowsCompatCDP(context.Background(), opts, proc, 54321, false, newDeps(&attachCalls))
		if err != nil || res == nil || attachCalls != 1 {
			t.Fatalf("exited corePID compat state wrongly skipped: res=%+v err=%v attachCalls=%d", res, err, attachCalls)
		}
	})
}

func TestAttachWindowsCompatCDPReadOnlyDirInvalidatesBeforeCDP(t *testing.T) {
	requireReadOnlyDirEnforced(t)

	proc := windowsAttachTestProc(4242, "2026-11-26T10:00:00Z")
	token := strings.Repeat("w", 48)
	selfPID := os.Getpid()
	newSuccessDeps := func(attachCalls *int) windowsCompatCDPDeps {
		return windowsCompatCDPDeps{
			verifyReady: func(context.Context) error { return nil },
			attachCDP: func(_ context.Context, cdpOpts WindowsCDPAttachOptions) (*AttachResult, error) {
				*attachCalls++
				return &AttachResult{
					Status:      "installed",
					Transport:   "windows-cdp-loopback",
					Protocol:    1,
					HostPID:     cdpOpts.HostPID,
					Platform:    "windows",
					DebugPort:   cdpOpts.DebugPort,
					hostStarted: cdpOpts.HostStarted,
				}, nil
			},
		}
	}

	t.Run("read-only dir with existing installed state aborts force compat attach before CDP", func(t *testing.T) {
		dir := t.TempDir()
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		coreSrv, _ := startTestCoreHealthServer(t, token)
		writeAttachCoreRuntime(t, dir, selfPID)
		opts := AttachOptions{
			DataDir:            dir,
			CoreURL:            coreSrv.URL,
			PairingToken:       token,
			CoreVersion:        "0.2.10",
			AllowDailyInstance: true,
			SkipCurrent:        true,
		}
		attachCalls := 0
		deps := newSuccessDeps(&attachCalls)
		res, err := attachWindowsCompatCDP(context.Background(), opts, proc, 54321, false, deps)
		if err != nil || res == nil || res.Status != "installed" || attachCalls != 1 {
			t.Fatalf("initial compat attach failed: res=%+v err=%v attachCalls=%d", res, err, attachCalls)
		}

		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}

		forceOpts := opts
		forceOpts.SkipCurrent = false
		attachCalls = 0
		res, err = attachWindowsCompatCDP(context.Background(), forceOpts, proc, 54321, false, deps)
		if res != nil || err == nil {
			t.Fatalf("expected pre-CDP persist failure on read-only dir, got res=%+v err=%v", res, err)
		}
		if attachCalls != 0 {
			t.Fatalf("attachCDP ran despite pre-invalidation failure: attachCalls=%d", attachCalls)
		}
		if !IsAttachPersistFailed(err) || IsAttachStateRecorded(err) || IsBridgeTornDown(err) {
			t.Fatalf("unexpected markers: persistFailed=%v stateRecorded=%v tornDown=%v err=%v",
				IsAttachPersistFailed(err), IsAttachStateRecorded(err), IsBridgeTornDown(err), err)
		}
		if state := ReadAttachState(dir); state == nil || state["status"] != "installed" {
			t.Fatalf("existing installed state changed before compat CDP: %+v", state)
		}
	})

	t.Run("mid-attach read-only dir plus teardown leaves attaching state so next SkipCurrent recovers", func(t *testing.T) {
		dir := t.TempDir()
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		coreSrv, _ := startTestCoreHealthServer(t, token)
		writeAttachCoreRuntime(t, dir, selfPID)
		opts := AttachOptions{
			DataDir:            dir,
			CoreURL:            coreSrv.URL,
			PairingToken:       token,
			CoreVersion:        "0.2.10",
			AllowDailyInstance: true,
			SkipCurrent:        true,
		}
		attachCalls := 0
		successDeps := newSuccessDeps(&attachCalls)
		res, err := attachWindowsCompatCDP(context.Background(), opts, proc, 54321, false, successDeps)
		if err != nil || res == nil || res.Status != "installed" || attachCalls != 1 {
			t.Fatalf("initial compat attach failed: res=%+v err=%v attachCalls=%d", res, err, attachCalls)
		}

		forceOpts := opts
		forceOpts.SkipCurrent = false
		tearDownDeps := windowsCompatCDPDeps{
			verifyReady: func(context.Context) error { return nil },
			attachCDP: func(context.Context, WindowsCDPAttachOptions) (*AttachResult, error) {
				attachCalls++
				if state := ReadAttachState(dir); state == nil || state["status"] != "attaching" {
					t.Errorf("expected attaching state before compat CDP, got: %+v", state)
				}
				if chmodErr := os.Chmod(dir, 0o500); chmodErr != nil {
					t.Errorf("chmod 0500 failed: %v", chmodErr)
				}
				return nil, errors.New("Windows 渲染页面注入失败（app://renderer/index.html）：boom")
			},
		}
		res, err = attachWindowsCompatCDP(context.Background(), forceOpts, proc, 54321, false, tearDownDeps)
		if res != nil || err == nil {
			t.Fatalf("expected teardown + persist failure, got res=%+v err=%v", res, err)
		}
		if !IsAttachPersistFailed(err) || !IsBridgeTornDown(err) || IsAttachStateRecorded(err) {
			t.Fatalf("unexpected markers: persistFailed=%v tornDown=%v stateRecorded=%v err=%v",
				IsAttachPersistFailed(err), IsBridgeTornDown(err), IsAttachStateRecorded(err), err)
		}
		if !strings.Contains(err.Error(), "移除旧接入状态文件失败") {
			t.Fatalf("expected joined remove error in persist failure, got: %v", err)
		}

		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if state := ReadAttachState(dir); state == nil || state["status"] != "attaching" {
			t.Fatalf("expected attaching state after read-only teardown failure, got: %+v", state)
		}

		// Next normal SkipCurrent attach must NOT skip and must re-inject cleanly.
		res, err = attachWindowsCompatCDP(context.Background(), opts, proc, 54321, false, successDeps)
		if err != nil || res == nil || res.Status != "installed" || attachCalls != 3 {
			t.Fatalf("next SkipCurrent compat attach did not recover: res=%+v err=%v attachCalls=%d", res, err, attachCalls)
		}
		if state := ReadAttachState(dir); state == nil || state["status"] != "installed" {
			t.Fatalf("expected installed state after recovery, got: %+v", state)
		}
	})

	t.Run("read-only dir with existing installed state aborts RunWindowsLauncher before AttachCDP", func(t *testing.T) {
		dir := t.TempDir()
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		rawRuntime, err := json.Marshal(CoreRuntimeRecord{
			URL:          "http://127.0.0.1:8321",
			PairingToken: token,
			PID:          selfPID,
			Version:      "0.2.10",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "runtime.json"), rawRuntime, 0o600); err != nil {
			t.Fatal(err)
		}
		reusedProc := proc
		reusedProc.CommandLine = `"C:\Users\me\AppData\Local\Programs\Kimi Code\Kimi Code.exe" --remote-debugging-port=54321 --remote-debugging-address=127.0.0.1`
		if err := WriteWindowsLauncherState(dir, WindowsLauncherState{
			PID:          reusedProc.PID,
			DebugPort:    54321,
			OwnerSID:     reusedProc.OwnerSID,
			Executable:   reusedProc.Executable,
			Started:      reusedProc.Started,
			LaunchedByUs: true,
		}); err != nil {
			t.Fatal(err)
		}
		// Seed host-attach.lock and installed attach-state.json before making dir read-only.
		gate, err := acquireHostAttachLock(context.Background(), dir)
		if err != nil {
			t.Fatal(err)
		}
		_ = gate.Close()
		if err := writeAttachState(dir, map[string]any{
			"status":      "installed",
			"transport":   "windows-cdp-loopback",
			"hostPid":     reusedProc.PID,
			"hostStarted": reusedProc.Started,
			"corePid":     selfPID,
			"attachKey":   "existing-key",
		}); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}

		attachCalls := 0
		res, launchErr := RunWindowsLauncher(context.Background(), WindowsLaunchOptions{
			DataDir:            dir,
			KimiExecutable:     reusedProc.Executable,
			AllowDailyInstance: true,
		}, WindowsLauncherDeps{
			CurrentSID:    func(context.Context) (string, error) { return reusedProc.OwnerSID, nil },
			ListProcesses: func(context.Context) ([]WindowsProcessInfo, error) { return []WindowsProcessInfo{reusedProc}, nil },
			ListListeners: func(context.Context, int) ([]WindowsPortListener, error) {
				return []WindowsPortListener{{PID: reusedProc.PID, LocalAddress: "127.0.0.1", LocalPort: 54321}}, nil
			},
			VerifyBinary: func(string) (WindowsBinaryInfo, error) {
				return WindowsBinaryInfo{ElectronPresent: true, InspectorFuseOn: true}, nil
			},
			SpawnDetached:   func(string, []string) (int, error) { t.Fatal("unexpected SpawnDetached"); return 0, nil },
			CheckCoreHealth: func(context.Context, string) bool { return true },
			SelfPID:         os.Getpid,
			AttachCDP: func(context.Context, WindowsCDPAttachOptions) (*AttachResult, error) {
				attachCalls++
				return nil, nil
			},
		})
		if res != nil || launchErr == nil {
			t.Fatalf("expected pre-AttachCDP persist failure, got res=%+v err=%v", res, launchErr)
		}
		if attachCalls != 0 {
			t.Fatalf("RunWindowsLauncher called AttachCDP despite pre-invalidation failure: %d", attachCalls)
		}
		if !IsAttachPersistFailed(launchErr) || IsAttachStateRecorded(launchErr) {
			t.Fatalf("unexpected RunWindowsLauncher markers: persistFailed=%v stateRecorded=%v err=%v",
				IsAttachPersistFailed(launchErr), IsAttachStateRecorded(launchErr), launchErr)
		}
	})
}

func TestWindowsFallbackPreservesCompatSkipUnlessTornDown(t *testing.T) {
	selfPID := os.Getpid()
	proc := windowsAttachTestProc(4242, "2026-11-26T10:00:00Z")
	token := strings.Repeat("w", 48)

	prevAlive := attachHealthyInstanceAlive
	attachHealthyInstanceAlive = func(_ context.Context, corePid, hostPid int, hostStarted string) bool {
		return corePid == selfPID && hostPid == proc.PID && hostStarted == proc.Started
	}
	t.Cleanup(func() { attachHealthyInstanceAlive = prevAlive })

	seedCompatInstalled := func(t *testing.T, dir string, opts AttachOptions) string {
		t.Helper()
		res, err := attachWindowsCompatCDP(context.Background(), opts, proc, 54321, false, windowsCompatCDPDeps{
			verifyReady: func(context.Context) error { return nil },
			attachCDP: func(_ context.Context, cdpOpts WindowsCDPAttachOptions) (*AttachResult, error) {
				return &AttachResult{
					Status:      "installed",
					Transport:   "windows-cdp-loopback",
					Protocol:    1,
					AppVersion:  "1.4.2",
					Electron:    SupportedElectron,
					HostPID:     cdpOpts.HostPID,
					Platform:    "windows",
					DebugPort:   cdpOpts.DebugPort,
					hostStarted: cdpOpts.HostStarted,
				}, nil
			},
		})
		if err != nil || res == nil || res.Status != "installed" {
			t.Fatalf("initial compat attach failed: res=%+v err=%v", res, err)
		}
		wantKey := windowsCDPAttachKey(proc, 54321, opts)
		if st := ReadAttachState(dir); st == nil || st["status"] != "installed" || st["attachKey"] != wantKey {
			t.Fatalf("unexpected seeded compat state: %+v", st)
		}
		return wantKey
	}

	t.Run("pre-teardown live failure after pre-invalidation skips compat attachCDP and restores installed state", func(t *testing.T) {
		dir := t.TempDir()
		coreSrv, _ := startTestCoreHealthServer(t, token)
		writeAttachCoreRuntime(t, dir, selfPID)
		opts := AttachOptions{
			DataDir:            dir,
			CoreURL:            coreSrv.URL,
			PairingToken:       token,
			CoreVersion:        "0.2.10",
			AllowDailyInstance: true,
			SkipCurrent:        true,
		}
		wantKey := seedCompatInstalled(t, dir, opts)

		var compatAttachCalls int
		res, err := attachWindowsWithFallback(context.Background(), opts, proc, 54321, windowsAttachFallbackDeps{
			tryLive: true,
			liveAttach: func(_ context.Context, liveOpts AttachOptions, _ WindowsProcessInfo) (*AttachResult, error) {
				// Simulate live path pre-invalidating attach-state.json before failing pre-teardown.
				if invErr := invalidateInstalledAttachBeforeInjectUnlocked(liveOpts.DataDir); invErr != nil {
					t.Errorf("pre-invalidation unexpectedly failed: %v", invErr)
				}
				if st := ReadAttachState(dir); st == nil || st["status"] != "attaching" {
					t.Errorf("expected attaching state after live pre-invalidation, got: %+v", st)
				}
				return nil, errors.New("无法开启 Windows 客户端的临时调试端口：spawn blocked")
			},
			compat: windowsCompatCDPDeps{
				verifyReady: func(context.Context) error { return nil },
				attachCDP: func(_ context.Context, _ WindowsCDPAttachOptions) (*AttachResult, error) {
					compatAttachCalls++
					return nil, errors.New("unexpected compat reinject")
				},
			},
		})
		if err != nil || res != nil {
			t.Fatalf("expected compat skip (nil, nil), got res=%+v err=%v", res, err)
		}
		if compatAttachCalls != 0 {
			t.Fatalf("compat attachCDP was called despite pre-teardown live failure and matching compat state: %d", compatAttachCalls)
		}
		state := ReadAttachState(dir)
		if state == nil || state["status"] != "installed" || state["attachKey"] != wantKey || state["transport"] != "windows-cdp-loopback" {
			t.Fatalf("compat installed state was not restored after live pre-teardown skip: %+v", state)
		}
	})

	t.Run("post-teardown live failure forces compat reinject", func(t *testing.T) {
		dir := t.TempDir()
		coreSrv, _ := startTestCoreHealthServer(t, token)
		writeAttachCoreRuntime(t, dir, selfPID)
		opts := AttachOptions{
			DataDir:            dir,
			CoreURL:            coreSrv.URL,
			PairingToken:       token,
			CoreVersion:        "0.2.10",
			AllowDailyInstance: true,
			SkipCurrent:        true,
		}
		wantKey := seedCompatInstalled(t, dir, opts)

		var compatAttachCalls int
		res, err := attachWindowsWithFallback(context.Background(), opts, proc, 54321, windowsAttachFallbackDeps{
			tryLive: true,
			liveAttach: func(_ context.Context, liveOpts AttachOptions, _ WindowsProcessInfo) (*AttachResult, error) {
				if invErr := invalidateInstalledAttachBeforeInjectUnlocked(liveOpts.DataDir); invErr != nil {
					t.Errorf("pre-invalidation unexpectedly failed: %v", invErr)
				}
				return nil, markBridgeTornDown(errors.New("live bridge torn down"))
			},
			compat: windowsCompatCDPDeps{
				verifyReady: func(context.Context) error { return nil },
				attachCDP: func(_ context.Context, cdpOpts WindowsCDPAttachOptions) (*AttachResult, error) {
					compatAttachCalls++
					return &AttachResult{
						Status:      "installed",
						Transport:   "windows-cdp-loopback",
						Protocol:    1,
						AppVersion:  "1.4.2",
						Electron:    SupportedElectron,
						HostPID:     cdpOpts.HostPID,
						Platform:    "windows",
						DebugPort:   cdpOpts.DebugPort,
						hostStarted: cdpOpts.HostStarted,
					}, nil
				},
			},
		})
		if err != nil || res == nil || res.Status != "installed" {
			t.Fatalf("expected compat reinject success after teardown, got res=%+v err=%v", res, err)
		}
		if compatAttachCalls != 1 {
			t.Fatalf("expected compat attachCDP to be called once after teardown, got %d", compatAttachCalls)
		}
		state := ReadAttachState(dir)
		if state == nil || state["status"] != "installed" || state["attachKey"] != wantKey {
			t.Fatalf("unexpected persisted state after compat reinject: %+v", state)
		}
	})
}
