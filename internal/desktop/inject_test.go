package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// The macOS host renews its twelve-hour session from the core's runtime record.
// The path is derived from the data directory and must reach the injected
// bootstrap, because a host that never received it could not renew at all — the
// exact failure this fix exists for.
func TestBootstrapConfigCarriesRuntimePath(t *testing.T) {
	dir := t.TempDir()
	want, err := filepath.Abs(filepath.Join(dir, "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	config, err := bootstrapConfig(AttachOptions{
		CoreURL: "http://127.0.0.1:9", PairingToken: strings.Repeat("t", 48), DataDir: dir,
	}, "1.3.7", "1.3.7", SupportedElectron, "module", "css")
	if err != nil {
		t.Fatal(err)
	}
	if config["runtimePath"] != want {
		t.Fatalf("runtimePath: got %v, want %s", config["runtimePath"], want)
	}
	if _, present := config["windowsMainBridge"]; present {
		t.Fatal("the macOS payload must not claim to be the Windows main bridge")
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["runtimePath"] != want {
		t.Fatalf("runtimePath did not survive the injection payload: %#v", decoded)
	}
}

// A host whose data directory is unknown cannot be given a renewal path, and the
// payload must then omit it rather than carry an empty string the host would try
// to read on every renewal.
func TestBootstrapConfigOmitsRuntimePathWithoutDataDir(t *testing.T) {
	config, err := bootstrapConfig(AttachOptions{
		CoreURL: "http://127.0.0.1:9", PairingToken: strings.Repeat("t", 48),
	}, "1.3.7", "1.3.7", SupportedElectron, "module", "css")
	if err != nil {
		t.Fatal(err)
	}
	if _, present := config["runtimePath"]; present {
		t.Fatalf("runtimePath was sent without a data directory: %#v", config)
	}
	if config["coreURL"] != "http://127.0.0.1:9" || config["pairingToken"] == nil {
		t.Fatalf("the pairing inputs were dropped: %#v", config)
	}
}

// The two options used to be one field, which made a renderer with a runtime path
// look like a Windows main process. They are now independent, and the host reads
// each one for exactly its own purpose.
func TestBootstrapSeparatesRuntimePathFromMainBridge(t *testing.T) {
	if !strings.Contains(bootstrapSource, "if (options.windowsMainBridge && (typeof BrowserWindow") {
		t.Fatal("the Electron interface check no longer follows windowsMainBridge")
	}
	if strings.Contains(bootstrapSource, "options.runtimePath && (typeof BrowserWindow") {
		t.Fatal("the Electron interface check is still tied to runtimePath")
	}
	if !strings.Contains(bootstrapSource, "coreURL: options.windowsMainBridge ? options.coreURL : ''") {
		t.Fatal("the renderer coreURL no longer follows windowsMainBridge")
	}
	if strings.Contains(bootstrapSource, "coreURL: options.runtimePath ? options.coreURL") {
		t.Fatal("the renderer coreURL is still tied to runtimePath")
	}
}

func TestBootstrapConfigCarriesCoreVersion(t *testing.T) {
	withVersion, err := bootstrapConfig(AttachOptions{
		CoreURL: "http://127.0.0.1:9", PairingToken: strings.Repeat("t", 48), CoreVersion: "0.2.7",
	}, "1.3.7", "1.3.7", SupportedElectron, "module", "css")
	if err != nil {
		t.Fatal(err)
	}
	if withVersion["coreVersion"] != "0.2.7" {
		t.Fatalf("coreVersion: got %v, want 0.2.7", withVersion["coreVersion"])
	}
	withoutVersion, err := bootstrapConfig(AttachOptions{
		CoreURL: "http://127.0.0.1:9", PairingToken: strings.Repeat("t", 48),
	}, "1.3.7", "1.3.7", SupportedElectron, "module", "css")
	if err != nil {
		t.Fatal(err)
	}
	if _, present := withoutVersion["coreVersion"]; present {
		t.Fatalf("empty coreVersion should be omitted: %#v", withoutVersion)
	}
}

// Writes the runtime record the way the core does, so other tests read a real
// file rather than a fixture string.
func writeCoreRuntime(t *testing.T, path, coreURL, pairingToken string) {
	t.Helper()
	raw, err := json.Marshal(CoreRuntimeRecord{URL: coreURL, PairingToken: pairingToken})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

type fakeDarwinCDP struct {
	evalFn       func(ctx context.Context, expr string) (json.RawMessage, error)
	closeErr     error
	cleanupCalls int
	closeCalls   int
}

func (f *fakeDarwinCDP) Evaluate(ctx context.Context, expr string) (json.RawMessage, error) {
	if strings.Contains(expr, "globalThis.__ekdNative?.cleanup") {
		f.cleanupCalls++
		return json.RawMessage(`true`), nil
	}
	return f.evalFn(ctx, expr)
}

func (f *fakeDarwinCDP) Close(context.Context) error {
	f.closeCalls++
	return f.closeErr
}

func TestRunDarwinCDPAttachPreBootstrapFailurePreservesBridge(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(`{"pid":4711}`), 0o600); err != nil {
		t.Fatal(err)
	}
	info := ProcessInfo{PID: 8801, UID: 501, Started: "Thu Nov 26 10:00:00 2026", Executable: KimiExecutable}
	opts := AttachOptions{
		DataDir:            dir,
		CoreURL:            "http://127.0.0.1:8321",
		PairingToken:       strings.Repeat("t", 48),
		CoreVersion:        "0.2.10",
		AllowDailyInstance: true,
	}
	key := darwinAttachKey(info, opts)
	if err := RecordAttach(dir, &AttachResult{
		Status:      "installed",
		HostPID:     info.PID,
		AppVersion:  "1.4.2",
		Electron:    SupportedElectron,
		Transport:   "main-loopback-rpc",
		attachKey:   key,
		hostStarted: info.Started,
	}); err != nil {
		t.Fatal(err)
	}
	prevAlive := attachHealthyInstanceAlive
	attachHealthyInstanceAlive = func(_ context.Context, corePid, hostPid int, hostStarted string) bool {
		return corePid == 4711 && hostPid == info.PID && hostStarted == info.Started
	}
	t.Cleanup(func() { attachHealthyInstanceAlive = prevAlive })

	cdp := &fakeDarwinCDP{
		evalFn: func(_ context.Context, expr string) (json.RawMessage, error) {
			if strings.Contains(expr, "appVersion:req('electron').app.getVersion()") {
				return json.RawMessage(`{"pid":8801,"type":"renderer","appVersion":"1.4.2","electron":"45.1.0"}`), nil
			}
			t.Fatalf("unexpected evaluation after Contract() failure: %s", expr)
			return nil, nil
		},
	}

	res, err := runDarwinCDPAttach(context.Background(), cdp, info, opts, key)
	if res != nil || err == nil {
		t.Fatalf("expected Contract() error, got res=%+v err=%v", res, err)
	}
	if cdp.cleanupCalls != 0 {
		t.Fatalf("pre-bootstrap failure tore down healthy bridge: cleanupCalls=%d", cdp.cleanupCalls)
	}
	if cdp.closeCalls != 1 {
		t.Fatalf("CDP session was not closed: closeCalls=%d", cdp.closeCalls)
	}
	if IsBridgeTornDown(err) {
		t.Fatalf("pre-bootstrap error was wrongly marked torn-down: %v", err)
	}
	if recErr := RecordAttach(dir, err); recErr != nil {
		t.Fatal(recErr)
	}
	state := ReadAttachState(dir)
	if state["status"] != "installed" || state["attachKey"] != key {
		t.Fatalf("healthy installed state was overwritten by pre-bootstrap failure: %+v", state)
	}
}

func TestRunDarwinCDPAttachPostBootstrapFailureTearsDownAndRecordsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(`{"pid":4711}`), 0o600); err != nil {
		t.Fatal(err)
	}
	info := ProcessInfo{PID: 8801, UID: 501, Started: "Thu Nov 26 10:00:00 2026", Executable: KimiExecutable}
	opts := AttachOptions{
		DataDir:            dir,
		CoreURL:            "http://127.0.0.1:8321",
		PairingToken:       strings.Repeat("t", 48),
		CoreVersion:        "0.2.10",
		AllowDailyInstance: true,
	}
	key := darwinAttachKey(info, opts)
	if err := RecordAttach(dir, &AttachResult{
		Status:      "installed",
		HostPID:     info.PID,
		AppVersion:  "1.4.2",
		Electron:    SupportedElectron,
		Transport:   "main-loopback-rpc",
		attachKey:   key,
		hostStarted: info.Started,
	}); err != nil {
		t.Fatal(err)
	}
	prevAlive := attachHealthyInstanceAlive
	attachHealthyInstanceAlive = func(_ context.Context, corePid, hostPid int, hostStarted string) bool {
		return corePid == 4711 && hostPid == info.PID && hostStarted == info.Started
	}
	t.Cleanup(func() { attachHealthyInstanceAlive = prevAlive })

	cdp := &fakeDarwinCDP{
		evalFn: func(_ context.Context, expr string) (json.RawMessage, error) {
			switch {
			case strings.Contains(expr, "appVersion:req('electron').app.getVersion()"):
				return json.RawMessage(`{"pid":8801,"type":"browser","appVersion":"1.4.2","electron":"45.1.0"}`), nil
			case strings.Contains(expr, "ptShell:!!document.querySelector"):
				return json.RawMessage(`{"appMounted":true,"ptShell":true,"ptBody":true,"sideColumn":true,"sideFooter":true}`), nil
			default:
				return nil, errors.New("bootstrap rejected")
			}
		},
	}

	res, err := runDarwinCDPAttach(context.Background(), cdp, info, opts, key)
	if res != nil || err == nil {
		t.Fatalf("expected bootstrap failure, got res=%+v err=%v", res, err)
	}
	if cdp.cleanupCalls != 1 {
		t.Fatalf("post-bootstrap failure did not roll back bridge: cleanupCalls=%d", cdp.cleanupCalls)
	}
	if !IsBridgeTornDown(err) {
		t.Fatalf("post-bootstrap failure was not marked torn-down: %v", err)
	}
	state := ReadAttachState(dir)
	if state["status"] != "error" || state["attachKey"] != nil {
		t.Fatalf("torn-down failure did not clear installed attachKey: %+v", state)
	}
	if attachStateMatchesCurrentCore(dir, state, key) {
		t.Fatal("subsequent SkipCurrent attach would wrongly skip after teardown failure")
	}
}

func TestRunDarwinCDPAttachCloseFailureAfterBootstrapMarksTornDown(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(`{"pid":4711}`), 0o600); err != nil {
		t.Fatal(err)
	}
	info := ProcessInfo{PID: 8801, UID: 501, Started: "Thu Nov 26 10:00:00 2026", Executable: KimiExecutable}
	opts := AttachOptions{
		DataDir:            dir,
		CoreURL:            "http://127.0.0.1:8321",
		PairingToken:       strings.Repeat("t", 48),
		CoreVersion:        "0.2.10",
		AllowDailyInstance: true,
	}
	key := darwinAttachKey(info, opts)
	if err := RecordAttach(dir, &AttachResult{
		Status:      "installed",
		HostPID:     info.PID,
		attachKey:   key,
		hostStarted: info.Started,
	}); err != nil {
		t.Fatal(err)
	}
	prevAlive := attachHealthyInstanceAlive
	attachHealthyInstanceAlive = func(_ context.Context, _, _ int, _ string) bool { return true }
	t.Cleanup(func() { attachHealthyInstanceAlive = prevAlive })

	cdp := &fakeDarwinCDP{
		closeErr: errors.New("port 9229 still open"),
		evalFn: func(_ context.Context, expr string) (json.RawMessage, error) {
			switch {
			case strings.Contains(expr, "appVersion:req('electron').app.getVersion()"):
				return json.RawMessage(`{"pid":8801,"type":"browser","appVersion":"1.4.2","electron":"45.1.0"}`), nil
			case strings.Contains(expr, "ptShell:!!document.querySelector"):
				return json.RawMessage(`{"appMounted":true,"ptShell":true,"ptBody":true,"sideColumn":true,"sideFooter":true}`), nil
			default:
				return json.RawMessage(`{"status":"installed","transport":"main-loopback-rpc","protocol":1}`), nil
			}
		},
	}

	res, err := runDarwinCDPAttach(context.Background(), cdp, info, opts, key)
	if res != nil || err == nil {
		t.Fatalf("expected close error, got res=%+v err=%v", res, err)
	}
	if !IsBridgeTornDown(err) {
		t.Fatalf("close error after bootstrap was not marked torn-down: %v", err)
	}
	state := ReadAttachState(dir)
	if state["status"] != "error" || state["attachKey"] != nil {
		t.Fatalf("close failure after bootstrap kept stale installed state: %+v", state)
	}
}

func TestRunDarwinCDPAttachSuccessPersistsAttachKeyWithoutCleanup(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(`{"pid":4711}`), 0o600); err != nil {
		t.Fatal(err)
	}
	info := ProcessInfo{PID: 8801, UID: 501, Started: "Thu Nov 26 10:00:00 2026", Executable: KimiExecutable}
	opts := AttachOptions{
		DataDir:            dir,
		CoreURL:            "http://127.0.0.1:8321",
		PairingToken:       strings.Repeat("t", 48),
		CoreVersion:        "0.2.10",
		AllowDailyInstance: true,
	}
	key := darwinAttachKey(info, opts)
	cdp := &fakeDarwinCDP{
		evalFn: func(_ context.Context, expr string) (json.RawMessage, error) {
			switch {
			case strings.Contains(expr, "appVersion:req('electron').app.getVersion()"):
				return json.RawMessage(`{"pid":8801,"type":"browser","appVersion":"1.4.2","electron":"45.1.0"}`), nil
			case strings.Contains(expr, "ptShell:!!document.querySelector"):
				return json.RawMessage(`{"appMounted":true,"ptShell":true,"ptBody":true,"sideColumn":true,"sideFooter":true}`), nil
			default:
				return json.RawMessage(`{"status":"installed","transport":"main-loopback-rpc","protocol":1}`), nil
			}
		},
	}
	res, err := runDarwinCDPAttach(context.Background(), cdp, info, opts, key)
	if err != nil || res == nil || res.Status != "installed" {
		t.Fatalf("unexpected result: res=%+v err=%v", res, err)
	}
	if cdp.cleanupCalls != 0 || cdp.closeCalls != 1 {
		t.Fatalf("unexpected cdp calls: cleanup=%d close=%d", cdp.cleanupCalls, cdp.closeCalls)
	}
	state := ReadAttachState(dir)
	if state["status"] != "installed" || state["attachKey"] != key || state["hostStarted"] != info.Started {
		t.Fatalf("persisted attach state mismatch: %+v", state)
	}
	if !IsAttachStateRecordedResult(res) {
		t.Fatal("successful runDarwinCDPAttach result was not marked stateRecorded")
	}
}

func TestAttachStateMatchesCurrentCoreStrictCorePID(t *testing.T) {
	key := "key-strict-core"
	selfPID := os.Getpid()
	oldCorePID := selfPID + 99999

	// Missing runtime.json: old corePid must NOT match; missing corePid must NOT match; selfPID matches.
	missingDir := t.TempDir()
	if attachStateMatchesCurrentCore(missingDir, map[string]any{
		"status":    "installed",
		"attachKey": key,
		"corePid":   float64(oldCorePID),
	}, key) {
		t.Fatal("missing runtime.json wrongly matched an old corePid")
	}
	if attachStateMatchesCurrentCore(missingDir, map[string]any{
		"status":    "installed",
		"attachKey": key,
	}, key) {
		t.Fatal("missing runtime.json wrongly matched missing corePid")
	}
	if !attachStateMatchesCurrentCore(missingDir, map[string]any{
		"status":    "installed",
		"attachKey": key,
		"corePid":   float64(selfPID),
	}, key) {
		t.Fatal("missing runtime.json failed to match current process corePid")
	}

	// Corrupt or non-positive runtime.json must never match even when corePid equals os.Getpid().
	corruptDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(corruptDir, "runtime.json"), []byte(`{corrupt`), 0o600); err != nil {
		t.Fatal(err)
	}
	if attachStateMatchesCurrentCore(corruptDir, map[string]any{
		"status":    "installed",
		"attachKey": key,
		"corePid":   float64(selfPID),
	}, key) {
		t.Fatal("corrupt runtime.json wrongly matched installed state")
	}
	if err := os.WriteFile(filepath.Join(corruptDir, "runtime.json"), []byte(`{"pid":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if attachStateMatchesCurrentCore(corruptDir, map[string]any{
		"status":    "installed",
		"attachKey": key,
		"corePid":   float64(selfPID),
	}, key) {
		t.Fatal("zero-pid runtime.json wrongly matched installed state")
	}

	// Valid runtime.json requires exact PID equality.
	validDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(validDir, "runtime.json"), []byte(`{"pid":4711}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if attachStateMatchesCurrentCore(validDir, map[string]any{
		"status":    "installed",
		"attachKey": key,
		"corePid":   float64(5822),
	}, key) {
		t.Fatal("valid runtime.json matched mismatched corePid")
	}
	if !attachStateMatchesCurrentCore(validDir, map[string]any{
		"status":    "installed",
		"attachKey": key,
		"corePid":   float64(4711),
	}, key) {
		t.Fatal("valid runtime.json did not match identical corePid")
	}
}

func startTestCoreHealthServer(t *testing.T, expectedToken string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/enhance/v1/health" && r.Header.Get("Authorization") == "Bearer "+expectedToken {
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"OK"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func exitedTestPID(t *testing.T) int {
	t.Helper()
	if runtime.GOOS == "darwin" {
		cmd := exec.Command("/usr/bin/true")
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		return cmd.Process.Pid
	}
	return os.Getpid() + 999999
}

func TestAttachDarwinHostVerifiesProcessBeforeSkipAndChecksCorePID(t *testing.T) {
	dir := t.TempDir()
	selfPID := os.Getpid()
	token := strings.Repeat("t", 48)
	coreSrv, healthHits := startTestCoreHealthServer(t, token)
	if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(fmt.Sprintf(`{"pid":%d,"url":%q,"pairingToken":%q}`, selfPID, coreSrv.URL, token)), 0o600); err != nil {
		t.Fatal(err)
	}
	info := ProcessInfo{PID: 8801, UID: 501, Started: "Thu Nov 26 10:00:00 2026", Executable: KimiExecutable}
	opts := AttachOptions{
		DataDir:            dir,
		CoreURL:            coreSrv.URL,
		PairingToken:       token,
		CoreVersion:        "0.2.10",
		AllowDailyInstance: true,
		SkipCurrent:        true,
	}
	key := darwinAttachKey(info, opts)
	if err := RecordAttach(dir, &AttachResult{
		Status:      "installed",
		HostPID:     info.PID,
		AppVersion:  "1.4.2",
		Electron:    SupportedElectron,
		Transport:   "main-loopback-rpc",
		attachKey:   key,
		hostStarted: info.Started,
	}); err != nil {
		t.Fatal(err)
	}

	alive := true
	prevAlive := attachHealthyInstanceAlive
	attachHealthyInstanceAlive = func(_ context.Context, corePid, hostPid int, hostStarted string) bool {
		return alive && corePid == selfPID && hostPid == info.PID && hostStarted == info.Started
	}
	t.Cleanup(func() { attachHealthyInstanceAlive = prevAlive })

	// 1. Live process with matching live corePid skips after fuse + live process + core health checks.
	var fuseCalls, verifyCalls, cdpCalls int
	deps := darwinAttachDeps{
		checkFuse: func(string) error {
			fuseCalls++
			return nil
		},
		verifyProcess: func(context.Context, ProcessInfo) error {
			verifyCalls++
			return nil
		},
		attachCDP: func(context.Context, int) (darwinCDPSession, error) {
			cdpCalls++
			return &fakeDarwinCDP{
				evalFn: func(_ context.Context, expr string) (json.RawMessage, error) {
					switch {
					case strings.Contains(expr, "appVersion:req('electron').app.getVersion()"):
						return json.RawMessage(`{"pid":8801,"type":"browser","appVersion":"1.4.2","electron":"45.1.0"}`), nil
					case strings.Contains(expr, "ptShell:!!document.querySelector"):
						return json.RawMessage(`{"appMounted":true,"ptShell":true,"ptBody":true,"sideColumn":true,"sideFooter":true}`), nil
					default:
						return json.RawMessage(`{"status":"installed","transport":"main-loopback-rpc","protocol":1}`), nil
					}
				},
			}, nil
		},
	}
	res, err := attachDarwinHost(context.Background(), info, opts, deps)
	if res != nil || err != nil {
		t.Fatalf("expected skip on live matching host, got res=%+v err=%v", res, err)
	}
	if fuseCalls != 1 || verifyCalls != 1 || cdpCalls != 0 || healthHits.Load() != 1 {
		t.Fatalf("expected pre-skip fuse+verify+health without CDP: fuse=%d verify=%d cdp=%d health=%d", fuseCalls, verifyCalls, cdpCalls, healthHits.Load())
	}

	// 2. Host exited before acquiring lock: VerifyLiveProcess fails -> must NOT skip and must record error inside lock.
	alive = false
	deadDeps := deps
	deadDeps.verifyProcess = func(context.Context, ProcessInfo) error {
		verifyCalls++
		return errors.New("宿主进程在附着前已退出")
	}
	res, err = attachDarwinHost(context.Background(), info, opts, deadDeps)
	if res != nil || err == nil {
		t.Fatalf("expected error when host exited before lock, got res=%+v err=%v", res, err)
	}
	if !IsAttachStateRecorded(err) {
		t.Fatalf("in-lock VerifyLiveProcess error was not marked stateRecorded: %v", err)
	}
	if cdpCalls != 0 {
		t.Fatalf("CDP should not run when VerifyLiveProcess fails: cdp=%d", cdpCalls)
	}
	state := ReadAttachState(dir)
	if state["status"] != "error" || state["attachKey"] != nil {
		t.Fatalf("dead host pre-skip failure did not record error: %+v", state)
	}

	// 3. Missing runtime.json with old corePid in attach-state.json must NOT skip; re-injects and updates corePid.
	if err := os.Remove(filepath.Join(dir, "runtime.json")); err != nil {
		t.Fatal(err)
	}
	staleState := map[string]any{
		"status":      "installed",
		"hostPid":     info.PID,
		"hostStarted": info.Started,
		"attachKey":   key,
		"corePid":     os.Getpid() + 99999,
	}
	if err := writeAttachState(dir, staleState); err != nil {
		t.Fatal(err)
	}
	res, err = attachDarwinHost(context.Background(), info, opts, deps)
	if err != nil || res == nil || res.Status != "installed" {
		t.Fatalf("missing runtime.json with old corePid did not re-inject: res=%+v err=%v", res, err)
	}
	if cdpCalls != 1 || !IsAttachStateRecordedResult(res) {
		t.Fatalf("expected fresh recorded attach, cdpCalls=%d recorded=%v", cdpCalls, IsAttachStateRecordedResult(res))
	}
	state = ReadAttachState(dir)
	if state["status"] != "installed" || state["corePid"] != float64(os.Getpid()) || state["attachKey"] != key {
		t.Fatalf("unexpected refreshed state after missing runtime.json: %+v", state)
	}
}

func TestAttachDarwinHostSkipCurrentRequiresLiveCore(t *testing.T) {
	info := ProcessInfo{PID: 8801, UID: 501, Started: "Thu Nov 26 10:00:00 2026", Executable: KimiExecutable}
	token := strings.Repeat("k", 48)
	newSuccessDeps := func(cdpCalls *int) darwinAttachDeps {
		return darwinAttachDeps{
			checkFuse:     func(string) error { return nil },
			verifyProcess: func(context.Context, ProcessInfo) error { return nil },
			attachCDP: func(context.Context, int) (darwinCDPSession, error) {
				*cdpCalls++
				return &fakeDarwinCDP{
					evalFn: func(_ context.Context, expr string) (json.RawMessage, error) {
						switch {
						case strings.Contains(expr, "appVersion:req('electron').app.getVersion()"):
							return json.RawMessage(`{"pid":8801,"type":"browser","appVersion":"1.4.2","electron":"45.1.0"}`), nil
						case strings.Contains(expr, "ptShell:!!document.querySelector"):
							return json.RawMessage(`{"appMounted":true,"ptShell":true,"ptBody":true,"sideColumn":true,"sideFooter":true}`), nil
						default:
							return json.RawMessage(`{"status":"installed","transport":"main-loopback-rpc","protocol":1}`), nil
						}
					},
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
		if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(fmt.Sprintf(`{"pid":%d,"url":%q,"pairingToken":%q}`, selfPID, coreSrv.URL, token)), 0o600); err != nil {
			t.Fatal(err)
		}
		key := darwinAttachKey(info, opts)
		if err := writeAttachState(dir, map[string]any{
			"status":      "installed",
			"hostPid":     info.PID,
			"hostStarted": info.Started,
			"attachKey":   key,
			"corePid":     selfPID,
		}); err != nil {
			t.Fatal(err)
		}

		cdpCalls := 0
		deps := newSuccessDeps(&cdpCalls)
		// While core is alive, SkipCurrent skips.
		res, err := attachDarwinHost(context.Background(), info, opts, deps)
		if res != nil || err != nil || cdpCalls != 0 {
			t.Fatalf("live core did not skip: res=%+v err=%v cdpCalls=%d", res, err, cdpCalls)
		}
		// Core exits leaving stale runtime.json on disk, caller passes identical CoreURL and PairingToken.
		coreSrv.Close()
		res, err = attachDarwinHost(context.Background(), info, opts, deps)
		if err != nil || res == nil || cdpCalls != 1 {
			t.Fatalf("dead core with stale runtime.json wrongly skipped: res=%+v err=%v cdpCalls=%d", res, err, cdpCalls)
		}
	})

	t.Run("bearer token rejected by health endpoint does not skip", func(t *testing.T) {
		dir := t.TempDir()
		selfPID := os.Getpid()
		coreSrv, _ := startTestCoreHealthServer(t, strings.Repeat("x", 48))
		opts := AttachOptions{
			DataDir:            dir,
			CoreURL:            coreSrv.URL,
			PairingToken:       token,
			CoreVersion:        "0.2.10",
			AllowDailyInstance: true,
			SkipCurrent:        true,
		}
		if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(fmt.Sprintf(`{"pid":%d}`, selfPID)), 0o600); err != nil {
			t.Fatal(err)
		}
		key := darwinAttachKey(info, opts)
		if err := writeAttachState(dir, map[string]any{
			"status":      "installed",
			"hostPid":     info.PID,
			"hostStarted": info.Started,
			"attachKey":   key,
			"corePid":     selfPID,
		}); err != nil {
			t.Fatal(err)
		}

		cdpCalls := 0
		res, err := attachDarwinHost(context.Background(), info, opts, newSuccessDeps(&cdpCalls))
		if err != nil || res == nil || cdpCalls != 1 {
			t.Fatalf("401 health response wrongly skipped: res=%+v err=%v cdpCalls=%d", res, err, cdpCalls)
		}
	})

	t.Run("exited corePID in runtime.json does not skip even if port responds 200", func(t *testing.T) {
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
		if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(fmt.Sprintf(`{"pid":%d,"url":%q,"pairingToken":%q}`, deadPID, coreSrv.URL, token)), 0o600); err != nil {
			t.Fatal(err)
		}
		key := darwinAttachKey(info, opts)
		if err := writeAttachState(dir, map[string]any{
			"status":      "installed",
			"hostPid":     info.PID,
			"hostStarted": info.Started,
			"attachKey":   key,
			"corePid":     deadPID,
		}); err != nil {
			t.Fatal(err)
		}

		cdpCalls := 0
		res, err := attachDarwinHost(context.Background(), info, opts, newSuccessDeps(&cdpCalls))
		if err != nil || res == nil || cdpCalls != 1 {
			t.Fatalf("exited corePID wrongly skipped: res=%+v err=%v cdpCalls=%d", res, err, cdpCalls)
		}
	})

	t.Run("health endpoint returning error status does not skip", func(t *testing.T) {
		dir := t.TempDir()
		selfPID := os.Getpid()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"error"}`))
		}))
		defer srv.Close()
		opts := AttachOptions{
			DataDir:            dir,
			CoreURL:            srv.URL,
			PairingToken:       token,
			CoreVersion:        "0.2.10",
			AllowDailyInstance: true,
			SkipCurrent:        true,
		}
		if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(fmt.Sprintf(`{"pid":%d}`, selfPID)), 0o600); err != nil {
			t.Fatal(err)
		}
		key := darwinAttachKey(info, opts)
		if err := writeAttachState(dir, map[string]any{
			"status":      "installed",
			"hostPid":     info.PID,
			"hostStarted": info.Started,
			"attachKey":   key,
			"corePid":     selfPID,
		}); err != nil {
			t.Fatal(err)
		}

		cdpCalls := 0
		res, err := attachDarwinHost(context.Background(), info, opts, newSuccessDeps(&cdpCalls))
		if err != nil || res == nil || cdpCalls != 1 {
			t.Fatalf("error health JSON wrongly skipped: res=%+v err=%v cdpCalls=%d", res, err, cdpCalls)
		}
	})
}

func TestAttachDarwinHostPersistFailurePropagatesError(t *testing.T) {
	info := ProcessInfo{PID: 8801, UID: 501, Started: "Thu Nov 26 10:00:00 2026", Executable: KimiExecutable}
	token := strings.Repeat("p", 48)

	t.Run("successful attach returns persist error and is not marked stateRecorded", func(t *testing.T) {
		dir := t.TempDir()
		opts := AttachOptions{
			DataDir:            dir,
			CoreURL:            "http://127.0.0.1:8321",
			PairingToken:       token,
			CoreVersion:        "0.2.10",
			AllowDailyInstance: true,
		}
		deps := darwinAttachDeps{
			checkFuse:     func(string) error { return nil },
			verifyProcess: func(context.Context, ProcessInfo) error { return nil },
			attachCDP: func(context.Context, int) (darwinCDPSession, error) {
				// Create the blocking directory during CDP so pre-invalidation passes and commit fails deterministically.
				if err := os.MkdirAll(filepath.Join(dir, "attach-state.json", "blocker"), 0o700); err != nil {
					t.Errorf("mkdir blocker: %v", err)
				}
				return &fakeDarwinCDP{
					evalFn: func(_ context.Context, expr string) (json.RawMessage, error) {
						switch {
						case strings.Contains(expr, "appVersion:req('electron').app.getVersion()"):
							return json.RawMessage(`{"pid":8801,"type":"browser","appVersion":"1.4.2","electron":"45.1.0"}`), nil
						case strings.Contains(expr, "ptShell:!!document.querySelector"):
							return json.RawMessage(`{"appMounted":true,"ptShell":true,"ptBody":true,"sideColumn":true,"sideFooter":true}`), nil
						default:
							return json.RawMessage(`{"status":"installed","transport":"main-loopback-rpc","protocol":1}`), nil
						}
					},
				}, nil
			},
		}
		res, err := attachDarwinHost(context.Background(), info, opts, deps)
		if res != nil || err == nil {
			t.Fatalf("expected persist error on success write failure, got res=%+v err=%v", res, err)
		}
		if !IsAttachPersistFailed(err) {
			t.Fatalf("error was not marked persist-failed: %v", err)
		}
		if IsAttachStateRecorded(err) {
			t.Fatalf("persist-failed error was wrongly marked stateRecorded: %v", err)
		}
		if strings.Count(err.Error(), "写入接入状态文件失败") != 1 {
			t.Fatalf("expected single persist error wrapper, got: %v", err)
		}
	})

	t.Run("torn-down failure with persist failure merges both errors without marking stateRecorded", func(t *testing.T) {
		dir := t.TempDir()
		opts := AttachOptions{
			DataDir:            dir,
			CoreURL:            "http://127.0.0.1:8321",
			PairingToken:       token,
			CoreVersion:        "0.2.10",
			AllowDailyInstance: true,
		}
		deps := darwinAttachDeps{
			checkFuse:     func(string) error { return nil },
			verifyProcess: func(context.Context, ProcessInfo) error { return nil },
			attachCDP: func(context.Context, int) (darwinCDPSession, error) {
				if err := os.MkdirAll(filepath.Join(dir, "attach-state.json", "blocker"), 0o700); err != nil {
					t.Errorf("mkdir blocker: %v", err)
				}
				return &fakeDarwinCDP{
					evalFn: func(_ context.Context, expr string) (json.RawMessage, error) {
						switch {
						case strings.Contains(expr, "appVersion:req('electron').app.getVersion()"):
							return json.RawMessage(`{"pid":8801,"type":"browser","appVersion":"1.4.2","electron":"45.1.0"}`), nil
						case strings.Contains(expr, "ptShell:!!document.querySelector"):
							return json.RawMessage(`{"appMounted":true,"ptShell":true,"ptBody":true,"sideColumn":true,"sideFooter":true}`), nil
						default:
							return nil, errors.New("bootstrap injected boom")
						}
					},
				}, nil
			},
		}
		res, err := attachDarwinHost(context.Background(), info, opts, deps)
		if res != nil || err == nil {
			t.Fatalf("expected merged torn-down + persist error, got res=%+v err=%v", res, err)
		}
		if !IsBridgeTornDown(err) {
			t.Fatalf("merged error lost bridgeTornDown marker: %v", err)
		}
		if !IsAttachPersistFailed(err) {
			t.Fatalf("merged error missing persistFailed marker: %v", err)
		}
		if IsAttachStateRecorded(err) {
			t.Fatalf("persist-failed error must not be marked stateRecorded: %v", err)
		}
		if !strings.Contains(err.Error(), "bootstrap injected boom") || strings.Count(err.Error(), "写入接入状态文件失败") != 1 {
			t.Fatalf("unexpected merged error message: %v", err)
		}
	})

	t.Run("failure overwrite error best-effort removes blocking stale path when removable", func(t *testing.T) {
		dir := t.TempDir()
		statePath := filepath.Join(dir, "attach-state.json")
		// An empty directory makes os.Rename(file, dir) fail on Unix while os.Remove(dir) succeeds.
		if err := os.Mkdir(statePath, 0o700); err != nil {
			t.Fatal(err)
		}
		opts := AttachOptions{
			DataDir:            dir,
			CoreURL:            "http://127.0.0.1:8321",
			PairingToken:       token,
			CoreVersion:        "0.2.10",
			AllowDailyInstance: true,
		}
		deps := darwinAttachDeps{
			checkFuse: func(string) error { return errors.New("fuse blocked") },
		}
		_, err := attachDarwinHost(context.Background(), info, opts, deps)
		if err == nil || !IsAttachPersistFailed(err) || IsAttachStateRecorded(err) {
			t.Fatalf("expected persist-failed error, got: %v", err)
		}
		if _, statErr := os.Stat(statePath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("stale blocking attach-state.json was not best-effort removed: statErr=%v", statErr)
		}
	})
}

func requireReadOnlyDirEnforced(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions are not enforced on Windows")
	}
	probeDir := filepath.Join(t.TempDir(), "ro-probe")
	if err := os.Mkdir(probeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(probeDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(probeDir, 0o700) })
	if err := os.WriteFile(filepath.Join(probeDir, "probe"), []byte("x"), 0o600); err == nil {
		t.Skip("current user can write into 0500 directory (likely root)")
	}
}

func TestAttachDarwinHostReadOnlyDirInvalidatesBeforeCDP(t *testing.T) {
	requireReadOnlyDirEnforced(t)

	info := ProcessInfo{PID: 8801, UID: 501, Started: "Thu Nov 26 10:00:00 2026", Executable: KimiExecutable}
	token := strings.Repeat("r", 48)
	selfPID := os.Getpid()

	newSuccessCDP := func() darwinCDPSession {
		return &fakeDarwinCDP{
			evalFn: func(_ context.Context, expr string) (json.RawMessage, error) {
				switch {
				case strings.Contains(expr, "appVersion:req('electron').app.getVersion()"):
					return json.RawMessage(`{"pid":8801,"type":"browser","appVersion":"1.4.2","electron":"45.1.0"}`), nil
				case strings.Contains(expr, "ptShell:!!document.querySelector"):
					return json.RawMessage(`{"appMounted":true,"ptShell":true,"ptBody":true,"sideColumn":true,"sideFooter":true}`), nil
				default:
					return json.RawMessage(`{"status":"installed","transport":"main-loopback-rpc","protocol":1}`), nil
				}
			},
		}
	}

	t.Run("read-only dir with existing installed state aborts force attach before CDP", func(t *testing.T) {
		dir := t.TempDir()
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		coreSrv, _ := startTestCoreHealthServer(t, token)
		if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(fmt.Sprintf(`{"pid":%d,"url":%q,"pairingToken":%q}`, selfPID, coreSrv.URL, token)), 0o600); err != nil {
			t.Fatal(err)
		}
		opts := AttachOptions{
			DataDir:            dir,
			CoreURL:            coreSrv.URL,
			PairingToken:       token,
			CoreVersion:        "0.2.10",
			AllowDailyInstance: true,
			SkipCurrent:        true,
		}
		var cdpCalls int
		deps := darwinAttachDeps{
			checkFuse:     func(string) error { return nil },
			verifyProcess: func(context.Context, ProcessInfo) error { return nil },
			attachCDP: func(context.Context, int) (darwinCDPSession, error) {
				cdpCalls++
				return newSuccessCDP(), nil
			},
		}
		res, err := attachDarwinHost(context.Background(), info, opts, deps)
		if err != nil || res == nil || res.Status != "installed" || cdpCalls != 1 {
			t.Fatalf("initial attach failed: res=%+v err=%v cdpCalls=%d", res, err, cdpCalls)
		}

		// Lock down the directory once host-attach.lock and attach-state.json exist.
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}

		forceOpts := opts
		forceOpts.SkipCurrent = false
		cdpCalls = 0
		res, err = attachDarwinHost(context.Background(), info, forceOpts, deps)
		if res != nil || err == nil {
			t.Fatalf("expected pre-CDP persist error on read-only dir, got res=%+v err=%v", res, err)
		}
		if cdpCalls != 0 {
			t.Fatalf("attachCDP was called despite pre-invalidation failure: cdpCalls=%d", cdpCalls)
		}
		if !IsAttachPersistFailed(err) || IsAttachStateRecorded(err) || IsBridgeTornDown(err) {
			t.Fatalf("unexpected error markers: persistFailed=%v stateRecorded=%v tornDown=%v err=%v",
				IsAttachPersistFailed(err), IsAttachStateRecorded(err), IsBridgeTornDown(err), err)
		}
		if state := ReadAttachState(dir); state["status"] != "installed" {
			t.Fatalf("existing installed state unexpectedly changed before CDP: %+v", state)
		}
	})

	t.Run("mid-attach read-only dir plus teardown leaves attaching state so next SkipCurrent recovers", func(t *testing.T) {
		dir := t.TempDir()
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		coreSrv, _ := startTestCoreHealthServer(t, token)
		if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(fmt.Sprintf(`{"pid":%d,"url":%q,"pairingToken":%q}`, selfPID, coreSrv.URL, token)), 0o600); err != nil {
			t.Fatal(err)
		}
		opts := AttachOptions{
			DataDir:            dir,
			CoreURL:            coreSrv.URL,
			PairingToken:       token,
			CoreVersion:        "0.2.10",
			AllowDailyInstance: true,
			SkipCurrent:        true,
		}
		var cdpCalls int
		successDeps := darwinAttachDeps{
			checkFuse:     func(string) error { return nil },
			verifyProcess: func(context.Context, ProcessInfo) error { return nil },
			attachCDP: func(context.Context, int) (darwinCDPSession, error) {
				cdpCalls++
				return newSuccessCDP(), nil
			},
		}
		res, err := attachDarwinHost(context.Background(), info, opts, successDeps)
		if err != nil || res == nil || res.Status != "installed" || cdpCalls != 1 {
			t.Fatalf("initial attach failed: res=%+v err=%v cdpCalls=%d", res, err, cdpCalls)
		}

		forceOpts := opts
		forceOpts.SkipCurrent = false
		tearDownDeps := darwinAttachDeps{
			checkFuse:     func(string) error { return nil },
			verifyProcess: func(context.Context, ProcessInfo) error { return nil },
			attachCDP: func(context.Context, int) (darwinCDPSession, error) {
				cdpCalls++
				if state := ReadAttachState(dir); state == nil || state["status"] != "attaching" {
					t.Errorf("expected attaching state on disk before CDP, got %+v", state)
				}
				if chmodErr := os.Chmod(dir, 0o500); chmodErr != nil {
					t.Errorf("chmod 0500 failed: %v", chmodErr)
				}
				return &fakeDarwinCDP{
					evalFn: func(_ context.Context, expr string) (json.RawMessage, error) {
						switch {
						case strings.Contains(expr, "appVersion:req('electron').app.getVersion()"):
							return json.RawMessage(`{"pid":8801,"type":"browser","appVersion":"1.4.2","electron":"45.1.0"}`), nil
						case strings.Contains(expr, "ptShell:!!document.querySelector"):
							return json.RawMessage(`{"appMounted":true,"ptShell":true,"ptBody":true,"sideColumn":true,"sideFooter":true}`), nil
						default:
							return nil, errors.New("bootstrap teardown boom")
						}
					},
				}, nil
			},
		}

		res, err = attachDarwinHost(context.Background(), info, forceOpts, tearDownDeps)
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
		state := ReadAttachState(dir)
		if state == nil || state["status"] != "attaching" {
			t.Fatalf("expected attaching state on disk after read-only teardown failure, got: %+v", state)
		}

		// Next normal SkipCurrent attach must NOT skip and must re-inject cleanly.
		res, err = attachDarwinHost(context.Background(), info, opts, successDeps)
		if err != nil || res == nil || res.Status != "installed" || cdpCalls != 3 {
			t.Fatalf("next SkipCurrent attach did not recover cleanly: res=%+v err=%v cdpCalls=%d", res, err, cdpCalls)
		}
		if state := ReadAttachState(dir); state == nil || state["status"] != "installed" {
			t.Fatalf("expected installed state after recovery, got: %+v", state)
		}
	})

	t.Run("unreadable 0000 attach-state.json in 0500 dir aborts force attach before CDP and preserves installed state", func(t *testing.T) {
		dir := t.TempDir()
		statePath := filepath.Join(dir, "attach-state.json")
		t.Cleanup(func() {
			_ = os.Chmod(dir, 0o700)
			_ = os.Chmod(statePath, 0o600)
		})
		coreSrv, _ := startTestCoreHealthServer(t, token)
		if err := os.WriteFile(filepath.Join(dir, "runtime.json"), []byte(fmt.Sprintf(`{"pid":%d,"url":%q,"pairingToken":%q}`, selfPID, coreSrv.URL, token)), 0o600); err != nil {
			t.Fatal(err)
		}
		opts := AttachOptions{
			DataDir:            dir,
			CoreURL:            coreSrv.URL,
			PairingToken:       token,
			CoreVersion:        "0.2.10",
			AllowDailyInstance: true,
			SkipCurrent:        true,
		}
		var cdpCalls int
		deps := darwinAttachDeps{
			checkFuse:     func(string) error { return nil },
			verifyProcess: func(context.Context, ProcessInfo) error { return nil },
			attachCDP: func(context.Context, int) (darwinCDPSession, error) {
				cdpCalls++
				return newSuccessCDP(), nil
			},
		}
		res, err := attachDarwinHost(context.Background(), info, opts, deps)
		if err != nil || res == nil || res.Status != "installed" || cdpCalls != 1 {
			t.Fatalf("initial attach failed: res=%+v err=%v cdpCalls=%d", res, err, cdpCalls)
		}
		initialKey, _ := ReadAttachState(dir)["attachKey"].(string)

		if err := os.Chmod(statePath, 0o000); err != nil {
			t.Fatal(err)
		}
		if _, readErr := os.ReadFile(statePath); readErr == nil {
			t.Skip("current user can read 0000 file (likely root)")
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}

		forceOpts := opts
		forceOpts.SkipCurrent = false
		cdpCalls = 0
		res, err = attachDarwinHost(context.Background(), info, forceOpts, deps)
		if res != nil || err == nil {
			t.Fatalf("expected pre-CDP persist failure for unreadable attach-state.json, got res=%+v err=%v", res, err)
		}
		if cdpCalls != 0 {
			t.Fatalf("attachCDP was called despite unreadable attach-state.json: cdpCalls=%d", cdpCalls)
		}
		if !IsAttachPersistFailed(err) || IsAttachStateRecorded(err) || IsBridgeTornDown(err) {
			t.Fatalf("unexpected markers: persistFailed=%v stateRecorded=%v tornDown=%v err=%v",
				IsAttachPersistFailed(err), IsAttachStateRecorded(err), IsBridgeTornDown(err), err)
		}

		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(statePath, 0o600); err != nil {
			t.Fatal(err)
		}
		state := ReadAttachState(dir)
		if state == nil || state["status"] != "installed" || state["attachKey"] != initialKey {
			t.Fatalf("original installed state was lost or torn down: %+v", state)
		}
	})
}
