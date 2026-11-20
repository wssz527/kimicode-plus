package desktop

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/coder/websocket"
)

func syntheticWindowsPE(machine uint16, electron bool, fuses string) []byte {
	var buf bytes.Buffer
	header := make([]byte, 0x100)
	header[0] = 'M'
	header[1] = 'Z'
	binary.LittleEndian.PutUint32(header[0x3c:0x40], 0x80)
	copy(header[0x80:0x84], []byte("PE\x00\x00"))
	binary.LittleEndian.PutUint16(header[0x84:0x86], machine)
	buf.Write(header)
	if electron {
		buf.WriteString("Electron/45.1.0 Chromium/134.0.0.0")
	}
	if fuses != "" {
		buf.WriteString(fuseSentinelPrefix)
		buf.WriteString(fuseSentinelSuffix)
		buf.WriteByte(1)
		buf.WriteByte(byte(len(fuses)))
		buf.WriteString(fuses)
	}
	return buf.Bytes()
}

func TestVerifyWindowsPEAndFuses(t *testing.T) {
	amd64Bin := syntheticWindowsPE(windowsPEMachineAMD64, true, "101100001")
	info, err := VerifyWindowsPEAndFuses(amd64Bin)
	if err != nil {
		t.Fatalf("valid x64 PE rejected: %v", err)
	}
	if info.Arch != "amd64" || !info.ElectronPresent || !info.InspectorFuseOn {
		t.Fatalf("unexpected PE info: %+v", info)
	}

	arm64Bin := syntheticWindowsPE(windowsPEMachineARM64, true, "101100001")
	info, err = VerifyWindowsPEAndFuses(arm64Bin)
	if err != nil || info.Arch != "arm64" {
		t.Fatalf("valid arm64 PE rejected: info=%+v err=%v", info, err)
	}

	if _, err := VerifyWindowsPEAndFuses(syntheticWindowsPE(0x014c, true, "101100001")); !errors.Is(err, ErrWindowsInvalidPE) {
		t.Fatalf("32-bit x86 PE should be rejected: %v", err)
	}
	if _, err := VerifyWindowsPEAndFuses(syntheticWindowsPE(windowsPEMachineAMD64, false, "101100001")); !errors.Is(err, ErrWindowsNotElectron) {
		t.Fatalf("non-Electron PE should be rejected: %v", err)
	}
	if _, err := VerifyWindowsPEAndFuses(syntheticWindowsPE(windowsPEMachineAMD64, true, "101000001")); !errors.Is(err, ErrInspectorDisabled) {
		t.Fatalf("disabled fuse should be rejected: %v", err)
	}

	// Verify streaming file scan matches in-memory check.
	exePath := filepath.Join(t.TempDir(), "Kimi Code.exe")
	if err := os.WriteFile(exePath, amd64Bin, 0o600); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := VerifyWindowsBinaryFile(exePath)
	if err != nil || fileInfo.Arch != "amd64" || !fileInfo.InspectorFuseOn {
		t.Fatalf("streaming binary check failed: info=%+v err=%v", fileInfo, err)
	}
}

func TestSelectWindowsMainProcessesAndLauncherDebugExemption(t *testing.T) {
	sid := "S-1-5-21-1000-2000-1001"
	exe := `C:\Users\Alice\AppData\Local\Programs\Kimi Code\Kimi Code.exe`
	started := "2026-11-19T06:00:00.123456Z"
	procs := []WindowsProcessInfo{
		// Different user SID -> ignored
		{PID: 101, OwnerSID: "S-1-5-21-9999-1002", Executable: exe, CommandLine: `"` + exe + `"`, Started: started},
		// Renderer helper -> ignored
		{PID: 102, OwnerSID: sid, Executable: exe, CommandLine: `"` + exe + `" --type=renderer`, Started: started},
		// Internal helper script -> ignored
		{PID: 103, OwnerSID: sid, Executable: exe, CommandLine: `"` + exe + `" ./bin/kimi-datasource.mjs`, Started: started},
		// Real launcher-managed main process -> matched
		{PID: 104, OwnerSID: sid, Executable: exe, CommandLine: `"` + exe + `" --remote-debugging-port=54321 --remote-debugging-address=127.0.0.1`, Started: started},
	}

	selected, err := SelectWindowsMainProcesses(sid, procs, exe, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(selected) != 1 || selected[0].PID != 104 {
		t.Fatalf("expected only PID 104, got %+v", selected)
	}

	// Without a matching launcher lease, --remote-debugging-port is treated as a debug instance.
	if !IsWindowsDebugInstance(selected[0], nil) {
		t.Fatal("unleased --remote-debugging-port must be treated as debug instance")
	}

	// With a matching launcher lease (PID + start time + executable + port), it is a daily instance!
	lease := &WindowsLauncherState{
		PID:          104,
		DebugPort:    54321,
		OwnerSID:     sid,
		Executable:   exe,
		Started:      started,
		LaunchedByUs: true,
	}
	if IsWindowsDebugInstance(selected[0], lease) {
		t.Fatal("launcher-owned process with exact matching port must be treated as daily instance")
	}

	// Mismatched port, extra --inspect, or non-loopback address still counts as debug instance!
	for name, cmd := range map[string]string{
		"wrong port":       `"` + exe + `" --remote-debugging-port=9222`,
		"extra inspect":    `"` + exe + `" --remote-debugging-port=54321 --inspect=9229`,
		"all interfaces":   `"` + exe + `" --remote-debugging-port=54321 --remote-debugging-address=0.0.0.0`,
		"alternate profile": `"` + exe + `" --remote-debugging-port=54321 --user-data-dir=C:\tmp\alt`,
	} {
		p := selected[0]
		p.CommandLine = cmd
		if !IsWindowsDebugInstance(p, lease) {
			t.Fatalf("%s: must still be treated as debug instance", name)
		}
	}

	// PID reuse (different start time) invalidates the lease and identity check!
	reused := selected[0]
	reused.Started = "2026-11-19T07:00:00.000000Z"
	if !IsWindowsDebugInstance(reused, lease) {
		t.Fatal("PID reuse with different start time must invalidate launcher lease")
	}
	if err := VerifyWindowsProcessIdentity(selected[0], reused); err == nil {
		t.Fatal("VerifyWindowsProcessIdentity accepted changed start time")
	}
}

func TestWindowsDebugPortOwnerAndForeignClientDetection(t *testing.T) {
	// Reject 0.0.0.0 listener even if PID matches.
	err := VerifyWindowsDebugPortOwner([]WindowsPortListener{
		{PID: 500, LocalAddress: "0.0.0.0", LocalPort: 51234},
	}, 51234, 500)
	if !errors.Is(err, ErrWindowsPortNotLocal) {
		t.Fatalf("expected non-loopback rejection, got %v", err)
	}

	// Reject wrong PID on 127.0.0.1.
	err = VerifyWindowsDebugPortOwner([]WindowsPortListener{
		{PID: 999, LocalAddress: "127.0.0.1", LocalPort: 51234},
	}, 51234, 500)
	if !errors.Is(err, ErrWindowsPortNotOwned) {
		t.Fatalf("expected wrong PID rejection, got %v", err)
	}

	// Accept exact PID on 127.0.0.1.
	if err := VerifyWindowsDebugPortOwner([]WindowsPortListener{
		{PID: 500, LocalAddress: "127.0.0.1", LocalPort: 51234},
	}, 51234, 500); err != nil {
		t.Fatalf("valid loopback listener rejected: %v", err)
	}

	// Detect foreign local client connected to debugging port alongside our own PID.
	conns := []WindowsTCPConnection{
		// Our own connection (selfPID=700 <-> hostPID=500)
		{PID: 500, LocalAddress: "127.0.0.1", LocalPort: 51234, RemoteAddress: "127.0.0.1", RemotePort: 60001, State: "Established"},
		{PID: 700, LocalAddress: "127.0.0.1", LocalPort: 60001, RemoteAddress: "127.0.0.1", RemotePort: 51234, State: "Established"},
		// Foreign process (PID=888 <-> hostPID=500)
		{PID: 500, LocalAddress: "127.0.0.1", LocalPort: 51234, RemoteAddress: "127.0.0.1", RemotePort: 60002, State: "Established"},
		{PID: 888, LocalAddress: "127.0.0.1", LocalPort: 60002, RemoteAddress: "127.0.0.1", RemotePort: 51234, State: "Established"},
	}
	alerts := DetectForeignDebugClients(conns, 51234, 500, 700)
	if len(alerts) != 1 || alerts[0].PeerPID != 888 {
		t.Fatalf("expected 1 foreign alert for PID 888, got %+v", alerts)
	}
	if !strings.Contains(FormatDebugClientAlerts(alerts), "PID 888") {
		t.Fatalf("unexpected alert text: %s", FormatDebugClientAlerts(alerts))
	}
}

type fakeShortcutBackend struct {
	shortcuts  []WindowsShortcutInfo
	failOnPath string
	retargeted map[string]struct{ target, args, workDir, icon string }
}

func (f *fakeShortcutBackend) ListKimiShortcuts(string) ([]WindowsShortcutInfo, error) {
	return append([]WindowsShortcutInfo(nil), f.shortcuts...), nil
}

func (f *fakeShortcutBackend) RetargetShortcut(lnkPath, targetPath, arguments, workingDir, iconLocation string) error {
	if f.failOnPath != "" && lnkPath == f.failOnPath {
		return errors.New("simulated shortcut write failure")
	}
	if f.retargeted == nil {
		f.retargeted = map[string]struct{ target, args, workDir, icon string }{}
	}
	f.retargeted[lnkPath] = struct{ target, args, workDir, icon string }{targetPath, arguments, workingDir, iconLocation}
	return os.WriteFile(lnkPath, []byte("MODIFIED-LNK:"+targetPath+"|"+arguments), 0o600)
}

func TestWindowsShortcutBackupRetargetRollbackAndRestore(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "ekd-data")
	startLnk := filepath.Join(dir, "StartMenu", "Kimi Code.lnk")
	deskLnk := filepath.Join(dir, "Desktop", "Kimi Code.lnk")
	if err := os.MkdirAll(filepath.Dir(startLnk), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(deskLnk), 0o755); err != nil {
		t.Fatal(err)
	}
	origStart := []byte("ORIGINAL-START-MENU-LNK-BYTES")
	origDesk := []byte("ORIGINAL-DESKTOP-LNK-BYTES")
	if err := os.WriteFile(startLnk, origStart, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deskLnk, origDesk, 0o600); err != nil {
		t.Fatal(err)
	}

	kimiExe := `C:\Users\Alice\AppData\Local\Programs\Kimi Code\Kimi Code.exe`
	baseCfg := WindowsLauncherConfig{
		DataDir:            dataDir,
		Executable:         `C:\Tools\enhanced-kimi.exe`,
		KimiExecutable:     kimiExe,
		AllowDailyInstance: true,
	}
	shortcuts := []WindowsShortcutInfo{
		{Path: startLnk, TargetPath: kimiExe, WorkingDirectory: filepath.Dir(kimiExe), IconLocation: kimiExe + ",0", Location: "start_menu"},
		{Path: deskLnk, TargetPath: kimiExe, WorkingDirectory: filepath.Dir(kimiExe), IconLocation: "", Location: "desktop"},
	}

	// Consent required when AllowDailyInstance is false.
	noConsent := baseCfg
	noConsent.AllowDailyInstance = false
	if _, _, err := InstallWindowsLauncherAndShortcuts(noConsent, &fakeShortcutBackend{shortcuts: shortcuts}); !errors.Is(err, ErrConsentRequired) {
		t.Fatalf("expected ErrConsentRequired, got %v", err)
	}

	// Failure on second shortcut must roll back the first shortcut to exact original bytes!
	failingBackend := &fakeShortcutBackend{shortcuts: shortcuts, failOnPath: deskLnk}
	if _, _, err := InstallWindowsLauncherAndShortcuts(baseCfg, failingBackend); err == nil {
		t.Fatal("expected retarget failure")
	}
	gotStart, _ := os.ReadFile(startLnk)
	if !bytes.Equal(gotStart, origStart) {
		t.Fatalf("first shortcut was not rolled back on mid-way failure: got %q", string(gotStart))
	}

	// Clean install succeeds, backs up both, retargets both, and preserves icon!
	okBackend := &fakeShortcutBackend{shortcuts: shortcuts}
	state, manifest, err := InstallWindowsLauncherAndShortcuts(baseCfg, okBackend)
	if err != nil {
		t.Fatalf("install failed: %v", err)
	}
	if !state.Enabled || state.ShortcutCount != 2 || len(manifest.Entries) != 2 {
		t.Fatalf("unexpected install state: state=%+v manifest=%+v", state, manifest)
	}
	if !WindowsLauncherStatus(dataDir).Enabled {
		t.Fatal("WindowsLauncherStatus should report enabled")
	}
	if okBackend.retargeted[deskLnk].icon != kimiExe+",0" {
		t.Fatalf("desktop shortcut did not preserve Kimi icon: %+v", okBackend.retargeted[deskLnk])
	}

	// Re-installing after modification must NOT overwrite the original backup bytes!
	if _, _, err := InstallWindowsLauncherAndShortcuts(baseCfg, okBackend); err != nil {
		t.Fatalf("re-install failed: %v", err)
	}

	// One-click restore returns both shortcuts verbatim and marks launcher disabled.
	restoredState, count, err := RestoreWindowsShortcuts(dataDir)
	if err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if restoredState.Enabled || count != 2 {
		t.Fatalf("unexpected restore state: state=%+v count=%d", restoredState, count)
	}
	gotStart, _ = os.ReadFile(startLnk)
	gotDesk, _ := os.ReadFile(deskLnk)
	if !bytes.Equal(gotStart, origStart) || !bytes.Equal(gotDesk, origDesk) {
		t.Fatalf("restored shortcuts do not match originals verbatim")
	}
	if WindowsLauncherStatus(dataDir).Enabled {
		t.Fatal("WindowsLauncherStatus should report disabled after restore")
	}
}

func TestAttachWindowsCDPAndRunWindowsLauncherEndToEnd(t *testing.T) {
	// 1. Mock Core server handling /health, /enhance/v1/pair, /enhance/v1/desktop/bridge-session.
	pairToken := strings.Repeat("p", 48)
	coreMux := http.NewServeMux()
	coreMux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"OK"}`))
	})
	coreMux.HandleFunc("POST /enhance/v1/pair", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"sessionToken": "sess-token-123"})
	})
	coreMux.HandleFunc("POST /enhance/v1/desktop/bridge-session", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sess-token-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"bridgeKey": strings.Repeat("b", 48)})
	})
	coreSrv := httptest.NewServer(coreMux)
	defer coreSrv.Close()

	// 2. Mock Chromium CDP server serving /json/version, /json/list, and page WS.
	var cdpPort int
	var gotMethods []string
	var cdpSrv *httptest.Server
	cdpSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/version":
			_ = json.NewEncoder(w).Encode(WindowsCDPVersion{
				Browser:   "Chrome/134.0.0.0",
				UserAgent: "Mozilla/5.0 KimiCode/1.3.7 Electron/45.1.0 Safari/537.36",
			})
		case "/json/list":
			wsURL := "ws://127.0.0.1:" + strconv.Itoa(cdpPort) + "/devtools/page/main-1"
			_ = json.NewEncoder(w).Encode([]WindowsCDPTarget{
				{ID: "overlay", Type: "page", URL: "app://renderer/browser-overlay", WebSocketDebuggerURL: "ws://127.0.0.1:" + strconv.Itoa(cdpPort) + "/devtools/page/ov"},
				{ID: "main-1", Type: "page", URL: "app://renderer/sessions/s1", WebSocketDebuggerURL: wsURL},
			})
		case "/devtools/page/main-1":
			if r.Header.Get("Origin") != "" {
				http.Error(w, "browser origin rejected", http.StatusForbidden)
				return
			}
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			conn.SetReadLimit(32 << 20)
			defer conn.Close(websocket.StatusNormalClosure, "")
			for {
				_, raw, err := conn.Read(r.Context())
				if err != nil {
					return
				}
				var msg struct {
					ID     int    `json:"id"`
					Method string `json:"method"`
				}
				if json.Unmarshal(raw, &msg) != nil {
					return
				}
				gotMethods = append(gotMethods, msg.Method)
				var resp any
				if msg.Method == "Runtime.evaluate" {
					resp = map[string]any{
						"id": msg.ID,
						"result": map[string]any{
							"result": map[string]any{
								"value": map[string]any{
									"status":        "installed",
									"userAgent":     "Mozilla/5.0 KimiCode/1.3.7 Electron/45.1.0",
									"serverVersion": "1.3.7",
								},
							},
						},
					}
				} else {
					resp = map[string]any{"id": msg.ID, "result": map[string]any{}}
				}
				out, _ := json.Marshal(resp)
				_ = conn.Write(r.Context(), websocket.MessageText, out)
			}
		}
	}))
	defer cdpSrv.Close()
	parsedCDP, _ := url.Parse(cdpSrv.URL)
	cdpPort, _ = strconv.Atoi(parsedCDP.Port())

	// 3. Prepare dataDir with existing running core runtime.json so launcher reuses it!
	dataDir := t.TempDir()
	runtimeJSON, _ := json.Marshal(CoreRuntimeRecord{
		URL:          coreSrv.URL,
		PairingToken: pairToken,
		PID:          9001,
		Version:      "0.2.0",
	})
	if err := os.WriteFile(filepath.Join(dataDir, "runtime.json"), runtimeJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	sid := "S-1-5-21-1000-2000-1001"
	kimiExe := `C:\Users\Alice\AppData\Local\Programs\Kimi Code\Kimi Code.exe`
	started := time.Now().UTC().Format(time.RFC3339Nano)
	spawnedKimi := false

	res, err := RunWindowsLauncher(context.Background(), WindowsLaunchOptions{
		DataDir:            dataDir,
		KimiExecutable:     kimiExe,
		Executable:         `C:\Tools\enhanced-kimi.exe`,
		AllowDailyInstance: true,
	}, WindowsLauncherDeps{
		CurrentSID: func(context.Context) (string, error) { return sid, nil },
		VerifyBinary: func(string) (WindowsBinaryInfo, error) {
			return WindowsBinaryInfo{Arch: "amd64", ElectronPresent: true, FuseVersion: 1, FuseCount: 9, InspectorFuseOn: true}, nil
		},
		PickPort: func() (int, error) { return cdpPort, nil },
		SpawnDetached: func(exe string, args []string) (int, error) {
			if exe != kimiExe {
				t.Fatalf("core should be reused, not spawned: %s %v", exe, args)
			}
			spawnedKimi = true
			return 4444, nil
		},
		ListProcesses: func(context.Context) ([]WindowsProcessInfo, error) {
			if !spawnedKimi {
				return nil, nil
			}
			return []WindowsProcessInfo{{
				PID:         4444,
				OwnerSID:    sid,
				Executable:  kimiExe,
				CommandLine: `"` + kimiExe + `" --remote-debugging-port=` + strconv.Itoa(cdpPort) + ` --remote-debugging-address=127.0.0.1`,
				Started:     started,
			}}, nil
		},
		ListListeners: func(context.Context, int) ([]WindowsPortListener, error) {
			return []WindowsPortListener{{PID: 4444, LocalAddress: "127.0.0.1", LocalPort: cdpPort}}, nil
		},
		ListConnections: func(context.Context, int) ([]WindowsTCPConnection, error) {
			// Simulate one foreign debug client PID 9876 to prove diagnostics alert is populated.
			return []WindowsTCPConnection{
				{PID: 4444, LocalAddress: "127.0.0.1", LocalPort: cdpPort, RemoteAddress: "127.0.0.1", RemotePort: 61111, State: "Established"},
				{PID: 9876, LocalAddress: "127.0.0.1", LocalPort: 61111, RemoteAddress: "127.0.0.1", RemotePort: cdpPort, State: "Established"},
			}, nil
		},
		SelfPID:      func() int { return 1234 },
		PollInterval: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("RunWindowsLauncher failed: %v", err)
	}
	if !res.CoreReused || res.KimiReused || res.KimiPID != 4444 || res.DebugPort != cdpPort {
		t.Fatalf("unexpected launcher result: %+v", res)
	}
	if res.Attach == nil || res.Attach.Status != "installed" || res.Attach.Transport != "windows-cdp-loopback" || !res.Attach.Verified {
		t.Fatalf("unexpected attach result: %+v", res.Attach)
	}
	if !strings.Contains(res.Attach.DebugPortWarning, "PID 9876") {
		t.Fatalf("expected foreign debug client alert, got %q", res.Attach.DebugPortWarning)
	}
	if lease := ReadWindowsLauncherState(dataDir); lease == nil || lease.PID != 4444 || lease.DebugPort != cdpPort {
		t.Fatalf("launcher lease was not saved: %+v", lease)
	}
	if state := ReadAttachState(dataDir); state == nil || state["debugPortWarning"] == "" {
		t.Fatalf("attach-state.json missing debugPortWarning: %+v", state)
	}
	wantMethods := "Runtime.enable,Page.enable,Page.addScriptToEvaluateOnNewDocument,Runtime.evaluate"
	if strings.Join(gotMethods, ",") != wantMethods {
		t.Fatalf("unexpected CDP call sequence: got %v, want %s", gotMethods, wantMethods)
	}
}

func TestEncodeWindowsPowerShellCommandRoundTrip(t *testing.T) {
	script := "$ErrorActionPreference = 'Stop'\n" +
		"$procs = @(Get-CimInstance Win32_Process -Filter \"Name = 'Kimi Code.exe'\")\n" +
		"Write-Output '中文路径测试'"
	encoded := EncodeWindowsPowerShellCommand(script)
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode failed: %v", err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("UTF-16LE payload must have even length, got %d", len(raw))
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(raw[i*2:])
	}
	decoded := string(utf16.Decode(units))
	if decoded != script {
		t.Fatalf("decoded script mismatch:\ngot:  %q\nwant: %q", decoded, script)
	}
}

func TestParseUserAgentVersionsAndWindowsVersionForwarding(t *testing.T) {
	cases := []struct {
		ua           string
		wantApp      string
		wantElectron string
	}{
		{
			ua:           "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) kimi-code-app/1.3.2 Chrome/156.0.8078.49 Electron/45.1.0 Safari/537.36",
			wantApp:      "1.3.2",
			wantElectron: "45.1.0",
		},
		{
			ua:           "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) kimi-code-app/1.3.7 Chrome/156.0.8078.49 Electron/45.1.0 Safari/537.36",
			wantApp:      "1.3.7",
			wantElectron: "45.1.0",
		},
		{
			ua:           "Mozilla/5.0 KimiCode/1.3.7 Electron/45.1.0 Safari/537.36",
			wantApp:      "1.3.7",
			wantElectron: "45.1.0",
		},
		{
			ua:           "Mozilla/5.0 Kimi Code/1.9.9-beta.1 Electron/45.1.0 Safari/537.36",
			wantApp:      "1.9.9-beta.1",
			wantElectron: "45.1.0",
		},
	}
	for _, tc := range cases {
		gotApp, gotElec := ParseUserAgentVersions(tc.ua)
		if gotApp != tc.wantApp || gotElec != tc.wantElectron {
			t.Fatalf("ParseUserAgentVersions(%q) = (%q, %q), want (%q, %q)", tc.ua, gotApp, gotElec, tc.wantApp, tc.wantElectron)
		}
	}

	pairToken := strings.Repeat("p", 48)
	coreMux := http.NewServeMux()
	coreMux.HandleFunc("POST /enhance/v1/pair", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"sessionToken": "sess-token-123"})
	})
	coreMux.HandleFunc("POST /enhance/v1/desktop/bridge-session", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"bridgeKey": strings.Repeat("b", 48)})
	})
	coreSrv := httptest.NewServer(coreMux)
	defer coreSrv.Close()

	var cdpPort int
	var evaluatedExpr string
	cdpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json/version":
			_ = json.NewEncoder(w).Encode(WindowsCDPVersion{
				Browser:   "Chrome/156.0.8078.49",
				UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) kimi-code-app/1.3.7 Chrome/156.0.8078.49 Electron/45.1.0 Safari/537.36",
			})
		case "/json/list":
			wsURL := "ws://127.0.0.1:" + strconv.Itoa(cdpPort) + "/devtools/page/main-1"
			_ = json.NewEncoder(w).Encode([]WindowsCDPTarget{
				{ID: "main-1", Type: "page", URL: "app://renderer/sessions/s1", WebSocketDebuggerURL: wsURL},
			})
		case "/devtools/page/main-1":
			if r.Header.Get("Origin") != "" {
				http.Error(w, "browser origin rejected", http.StatusForbidden)
				return
			}
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			conn.SetReadLimit(32 << 20)
			defer conn.Close(websocket.StatusNormalClosure, "")
			for {
				_, raw, err := conn.Read(r.Context())
				if err != nil {
					return
				}
				var msg struct {
					ID     int             `json:"id"`
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
				}
				if json.Unmarshal(raw, &msg) != nil {
					return
				}
				var resp any
				if msg.Method == "Runtime.evaluate" {
					var p struct {
						Expression string `json:"expression"`
					}
					_ = json.Unmarshal(msg.Params, &p)
					evaluatedExpr = p.Expression
					resp = map[string]any{
						"id": msg.ID,
						"result": map[string]any{
							"result": map[string]any{
								"value": map[string]any{
									"status":    "installed",
									"userAgent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) kimi-code-app/1.3.7 Chrome/156.0.8078.49 Electron/45.1.0 Safari/537.36",
								},
							},
						},
					}
				} else {
					resp = map[string]any{"id": msg.ID, "result": map[string]any{}}
				}
				out, _ := json.Marshal(resp)
				_ = conn.Write(r.Context(), websocket.MessageText, out)
			}
		}
	}))
	defer cdpSrv.Close()
	parsedCDP, _ := url.Parse(cdpSrv.URL)
	cdpPort, _ = strconv.Atoi(parsedCDP.Port())

	res137, err := AttachWindowsCDP(context.Background(), WindowsCDPAttachOptions{
		DebugPort:    cdpPort,
		HostPID:      5001,
		CoreURL:      coreSrv.URL,
		PairingToken: pairToken,
		Profile:      `C:\Temp\iso-profile`,
	})
	if err != nil {
		t.Fatalf("AttachWindowsCDP 1.3.7 failed: %v", err)
	}
	if res137.AppVersion != "1.3.7" || res137.Electron != "45.1.0" || !res137.Verified || res137.VersionNote != "" {
		t.Fatalf("unexpected 1.3.7 result: %+v", res137)
	}
	if !strings.Contains(evaluatedExpr, `"appVersion":"1.3.7"`) || !strings.Contains(evaluatedExpr, `"electron":"45.1.0"`) {
		t.Fatalf("renderer config missing forwarded appVersion/electron")
	}

	res199, err := AttachWindowsCDP(context.Background(), WindowsCDPAttachOptions{
		DebugPort:          cdpPort,
		HostPID:            5002,
		CoreURL:            coreSrv.URL,
		PairingToken:       pairToken,
		Profile:            `C:\Temp\iso-profile`,
		AppVersionOverride: "1.9.9",
	})
	if err != nil {
		t.Fatalf("AttachWindowsCDP 1.9.9 override failed: %v", err)
	}
	if res199.AppVersion != "1.9.9" || res199.Verified || res199.VersionNote != UnverifiedVersionNote {
		t.Fatalf("unexpected 1.9.9 override result: %+v", res199)
	}
	if !strings.Contains(evaluatedExpr, `"appVersion":"1.9.9"`) {
		t.Fatalf("renderer config missing overridden appVersion 1.9.9")
	}
}
