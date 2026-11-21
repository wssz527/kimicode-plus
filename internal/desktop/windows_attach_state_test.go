package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
}
