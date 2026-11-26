package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"enhancedkimi/internal/security"
	"enhancedkimi/internal/storage"
)

func TestWindowsBridgeSessionAndRPCDispatch(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	host := "127.0.0.1:18444"
	guard := security.New(host)
	app, err := NewWithNative(db, guard, false, "", NativeOptions{Home: t.TempDir(), DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	// 1. Pair to get the Windows bridge bootstrap permission.
	pairBody, _ := json.Marshal(map[string]any{
		"pairingToken": guard.PairingToken(),
		"permissions":  []string{security.WindowsBridgePermission},
	})
	pairReq := httptest.NewRequest(http.MethodPost, "http://"+host+"/enhance/v1/pair", bytes.NewReader(pairBody))
	pairReq.Host = host
	pairReq.Header.Set("Content-Type", "application/json")
	pairRec := httptest.NewRecorder()
	app.Handler.ServeHTTP(pairRec, pairReq)
	if pairRec.Code != http.StatusOK {
		t.Fatalf("pair failed: %d %s", pairRec.Code, pairRec.Body.String())
	}
	var pairRes struct {
		SessionToken string `json:"sessionToken"`
	}
	_ = json.Unmarshal(pairRec.Body.Bytes(), &pairRes)

	adapterReq := httptest.NewRequest(http.MethodPost, "http://"+host+"/enhance/v1/desktop/bridge-session", strings.NewReader(`{}`))
	adapterReq.Header.Set("Content-Type", "application/json")
	adapterReq.Header.Set("Authorization", "Bearer "+guard.IssueSessionToken([]string{"adapter:write"}))
	adapterRec := httptest.NewRecorder()
	app.Handler.ServeHTTP(adapterRec, adapterReq)
	if adapterRec.Code != http.StatusForbidden {
		t.Fatalf("adapter permission minted a Windows bridge session: %d %s", adapterRec.Code, adapterRec.Body)
	}

	// 2. Mint Windows bridgeKey.
	mintReq := httptest.NewRequest(http.MethodPost, "http://"+host+"/enhance/v1/desktop/bridge-session", bytes.NewReader([]byte(`{}`)))
	mintReq.Host = host
	mintReq.Header.Set("Content-Type", "application/json")
	mintReq.Header.Set("Authorization", "Bearer "+pairRes.SessionToken)
	mintRec := httptest.NewRecorder()
	app.Handler.ServeHTTP(mintRec, mintReq)
	if mintRec.Code != http.StatusOK {
		t.Fatalf("bridge-session failed: %d %s", mintRec.Code, mintRec.Body.String())
	}
	var mintRes struct {
		BridgeKey string `json:"bridgeKey"`
	}
	_ = json.Unmarshal(mintRec.Body.Bytes(), &mintRes)
	if len(mintRes.BridgeKey) < 32 {
		t.Fatalf("bridge key too short: %q", mintRes.BridgeKey)
	}

	// 3. Untrusted origin must be rejected on both preflight and POST.
	badOpt := httptest.NewRequest(http.MethodOptions, "http://"+host+"/ekd/native/v1", nil)
	badOpt.Host = host
	badOpt.Header.Set("Origin", "https://evil.example")
	badOptRec := httptest.NewRecorder()
	app.Handler.ServeHTTP(badOptRec, badOpt)
	if badOptRec.Code != http.StatusForbidden {
		t.Fatalf("evil origin preflight accepted: %d", badOptRec.Code)
	}

	// 4. Trusted app://renderer preflight succeeds with PNA + CORS headers.
	goodOpt := httptest.NewRequest(http.MethodOptions, "http://"+host+"/ekd/native/v1", nil)
	goodOpt.Host = host
	goodOpt.Header.Set("Origin", "app://renderer")
	goodOptRec := httptest.NewRecorder()
	app.Handler.ServeHTTP(goodOptRec, goodOpt)
	if goodOptRec.Code != http.StatusNoContent || goodOptRec.Header().Get("Access-Control-Allow-Origin") != "app://renderer" {
		t.Fatalf("renderer preflight failed: %d %+v", goodOptRec.Code, goodOptRec.Header())
	}

	// 5. Missing/invalid bridge key must be reported as a renewable session, not
	// as a permanent denial: the table is per-process, so a core restart empties
	// it while the page keeps its key.
	badKeyReq := httptest.NewRequest(http.MethodPost, "http://"+host+"/ekd/native/v1", bytes.NewReader([]byte(`{"protocol":1,"id":"1","action":"attach.status"}`)))
	badKeyReq.Host = host
	badKeyReq.Header.Set("Origin", "app://renderer")
	badKeyReq.Header.Set("Content-Type", "application/json")
	badKeyReq.Header.Set("X-EKD-Bridge", "invalid-key")
	badKeyRec := httptest.NewRecorder()
	app.Handler.ServeHTTP(badKeyRec, badKeyReq)
	if badKeyRec.Code != http.StatusUnauthorized || !bytes.Contains(badKeyRec.Body.Bytes(), []byte(`"BRIDGE_SESSION_EXPIRED"`)) || !bytes.Contains(badKeyRec.Body.Bytes(), []byte("等待重新接入")) {
		t.Fatalf("invalid bridge key: %d %s", badKeyRec.Code, badKeyRec.Body.String())
	}

	// 5b. An untrusted origin is refused for its origin, and a non-POST method is
	// refused for its method, so a client can tell the two apart from a stale key.
	foreignOrigin := httptest.NewRequest(http.MethodPost, "http://"+host+"/ekd/native/v1", bytes.NewReader([]byte(`{"protocol":1,"id":"1","action":"attach.status"}`)))
	foreignOrigin.Host = host
	foreignOrigin.Header.Set("Origin", "https://evil.example")
	foreignOrigin.Header.Set("Content-Type", "application/json")
	foreignOrigin.Header.Set("X-EKD-Bridge", mintRes.BridgeKey)
	foreignRec := httptest.NewRecorder()
	app.Handler.ServeHTTP(foreignRec, foreignOrigin)
	if foreignRec.Code != http.StatusForbidden || !bytes.Contains(foreignRec.Body.Bytes(), []byte(`"ORIGIN_DENIED"`)) {
		t.Fatalf("foreign origin: %d %s", foreignRec.Code, foreignRec.Body.String())
	}
	wrongMethod := httptest.NewRequest(http.MethodGet, "http://"+host+"/ekd/native/v1", nil)
	wrongMethod.Host = host
	wrongMethod.Header.Set("Origin", "app://renderer")
	wrongMethod.Header.Set("X-EKD-Bridge", mintRes.BridgeKey)
	wrongRec := httptest.NewRecorder()
	app.Handler.ServeHTTP(wrongRec, wrongMethod)
	if wrongRec.Code != http.StatusMethodNotAllowed || !bytes.Contains(wrongRec.Body.Bytes(), []byte(`"METHOD_DENIED"`)) {
		t.Fatalf("non-POST bridge request: %d %s", wrongRec.Code, wrongRec.Body.String())
	}

	// 6. Valid bridge POST for core.fetch reaches /enhance/v1/settings using server-held token.
	rpcReq := httptest.NewRequest(http.MethodPost, "http://"+host+"/ekd/native/v1", bytes.NewReader([]byte(`{"protocol":1,"id":"req-1","action":"core.fetch","payload":{"path":"/enhance/v1/settings","method":"GET"}}`)))
	rpcReq.Host = host
	rpcReq.Header.Set("Origin", "app://renderer")
	rpcReq.Header.Set("Content-Type", "application/json")
	rpcReq.Header.Set("X-EKD-Bridge", mintRes.BridgeKey)
	rpcRec := httptest.NewRecorder()
	app.Handler.ServeHTTP(rpcRec, rpcReq)
	if rpcRec.Code != http.StatusOK {
		t.Fatalf("bridge core.fetch failed: %d %s", rpcRec.Code, rpcRec.Body.String())
	}
	var rpcRes struct {
		Protocol int    `json:"protocol"`
		ID       string `json:"id"`
		Result   struct {
			Status     int    `json:"status"`
			BodyBase64 string `json:"bodyBase64"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rpcRec.Body.Bytes(), &rpcRes); err != nil {
		t.Fatal(err)
	}
	if rpcRes.Error != nil || rpcRes.ID != "req-1" || rpcRes.Result.Status != 200 {
		t.Fatalf("unexpected core.fetch RPC response: %+v", rpcRes)
	}
	decoded, err := base64.StdEncoding.DecodeString(rpcRes.Result.BodyBase64)
	if err != nil || !bytes.Contains(decoded, []byte(`"modules"`)) {
		t.Fatalf("unexpected core.fetch body: %q err=%v", string(decoded), err)
	}

	// 7. Native action dispatch (attach.status) works transparently over the bridge.
	actReq := httptest.NewRequest(http.MethodPost, "http://"+host+"/ekd/native/v1", bytes.NewReader([]byte(`{"protocol":1,"id":"req-2","action":"attach.status","payload":{}}`)))
	actReq.Host = host
	actReq.Header.Set("Origin", "app://renderer")
	actReq.Header.Set("Content-Type", "application/json")
	actReq.Header.Set("X-EKD-Bridge", mintRes.BridgeKey)
	actRec := httptest.NewRecorder()
	app.Handler.ServeHTTP(actRec, actReq)
	if actRec.Code != http.StatusOK || !bytes.Contains(actRec.Body.Bytes(), []byte(`"available"`)) {
		t.Fatalf("bridge attach.status failed: %d %s", actRec.Code, actRec.Body.String())
	}
}

func TestWindowsBridgeUnsupportedPluginFeatures(t *testing.T) {
	guard := security.New("127.0.0.1:18444")
	app := &App{winBridge: newWindowsBridge()}
	key, _ := app.winBridge.issue(guard)
	handler := app.wrapWindowsBridge(guard, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unsupported action was forwarded")
	}))
	for _, action := range []string{"host.syncOverlayEntries", "plugin.window.open", "window.close", "browser.view.open", "browser.view.close"} {
		request := httptest.NewRequest(http.MethodPost, "http://"+guard.Host()+"/ekd/native/v1", strings.NewReader(`{"protocol":1,"id":"test","action":"`+action+`"}`))
		request.Header.Set("Origin", "app://renderer")
		request.Header.Set("X-EKD-Bridge", key)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var result struct {
			Error struct{ Code, Message string }
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK || result.Error.Code != "PLATFORM_UNSUPPORTED" || !strings.Contains(result.Error.Message, "从 KimiCode+ 入口重新打开 Kimi Code") {
			t.Fatalf("%s => %d %s (%v)", action, response.Code, response.Body, err)
		}
	}
}

func TestWindowsBridgeFilePickerOptions(t *testing.T) {
	picker, err := parseWindowsPickerOptions(false, json.RawMessage(`{"title":"选择 ZIP 插件包","defaultPath":"C:\\Users\\测试","single":true,"filters":[{"name":"ZIP 插件包","extensions":["zip"]}]}`))
	if err != nil || picker.Title != "选择 ZIP 插件包" || picker.DefaultPath != `C:\Users\测试` || !picker.Single || picker.Directory || picker.Filter != "ZIP 插件包|*.zip" {
		t.Fatalf("picker: %+v (%v)", picker, err)
	}
	folder, err := parseWindowsPickerOptions(true, json.RawMessage(`{}`))
	if err != nil || !folder.Directory || folder.Title != "选择项目文件夹" {
		t.Fatalf("picker: %+v (%v)", folder, err)
	}
	if _, err := parseWindowsPickerOptions(false, json.RawMessage(`{"filters":[{"name":"ZIP","extensions":["zip;*.exe"]}]}`)); err == nil {
		t.Fatal("accepted an invalid extension")
	}
}

func TestWindowsPickerRejectsNoninteractiveDesktop(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("EKD_TEST_NONINTERACTIVE_DESKTOP") != "1" {
		t.Skip("requires the Windows OpenSSH session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, code, err := showWindowsPicker(ctx, windowsPickerOptions{})
	if code != "HOST_DIALOG_UNAVAILABLE" || err == nil || !strings.Contains(err.Error(), "交互桌面") {
		t.Fatalf("noninteractive picker: %s (%v)", code, err)
	}
}

// A compatible-mode client keeps one bridge key for as long as the core runs, so
// a fixed twelve-hour expiry made every request fail at the twelve-hour mark even
// though nothing had changed. The session now slides forward inside its last
// hours; the token handed to the core is replaced, and the key stays valid.
func TestWindowsBridgeRenewsSessionBeforeExpiry(t *testing.T) {
	guard := security.New("127.0.0.1:18445")
	bridge := newWindowsBridge()
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	bridge.now = func() time.Time { return now }
	key, issuedExpiry := bridge.issue(guard)
	first, ok := bridge.lookup(key, guard)
	if !ok || !first.expiresAt.Equal(issuedExpiry) {
		t.Fatalf("fresh session: %+v %v", first, ok)
	}

	// Nine hours in, three hours remain: outside the renewal window, so the
	// session must be untouched — a client must not get a new token on every
	// single call.
	now = now.Add(9 * time.Hour)
	steady, ok := bridge.lookup(key, guard)
	if !ok || steady.sessionToken != first.sessionToken || !steady.expiresAt.Equal(first.expiresAt) {
		t.Fatalf("session renewed too early: %+v %v", steady, ok)
	}

	// Ten hours in, two hours remain: inside the window, so the token is
	// reissued and the expiry slides a full lifetime forward.
	now = now.Add(time.Hour)
	renewed, ok := bridge.lookup(key, guard)
	if !ok {
		t.Fatal("session vanished inside its lifetime")
	}
	if renewed.sessionToken == first.sessionToken {
		t.Fatal("session was not reissued inside the renewal window")
	}
	if want := now.Add(windowsBridgeTTL); !renewed.expiresAt.Equal(want) {
		t.Fatalf("expiry did not slide: got %s, want %s", renewed.expiresAt, want)
	}
	if stored, ok := bridge.lookup(key, guard); !ok || stored.sessionToken != renewed.sessionToken {
		t.Fatalf("renewed token was not written back: %+v %v", stored, ok)
	}

	// The reissued token is a real grant the core accepts for bridge traffic.
	if _, ok := bridge.lookup(key, guard); !ok {
		t.Fatal("bridge key stopped resolving after renewal")
	}

	// Past the absolute lifetime the session is gone, and the answer is a
	// renewable failure rather than an unexpired-looking one.
	now = now.Add(windowsBridgeTTL + time.Second)
	if _, ok := bridge.lookup(key, guard); ok {
		t.Fatal("expired session still resolved")
	}
}

func TestWindowsBridgeRenewalWindowIsInjectable(t *testing.T) {
	guard := security.New("127.0.0.1:18446")
	bridge := newWindowsBridge()
	bridge.ttl = time.Minute
	bridge.renewWindow = 10 * time.Second
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	bridge.now = func() time.Time { return now }
	key, _ := bridge.issue(guard)
	before, ok := bridge.lookup(key, guard)
	if !ok {
		t.Fatal("fresh session missing")
	}
	now = now.Add(45 * time.Second)
	if steady, ok := bridge.lookup(key, guard); !ok || steady.sessionToken != before.sessionToken {
		t.Fatalf("renewed outside an injected window: %+v %v", steady, ok)
	}
	now = now.Add(10 * time.Second)
	if renewed, ok := bridge.lookup(key, guard); !ok || renewed.sessionToken == before.sessionToken {
		t.Fatalf("did not renew inside an injected window: %+v %v", renewed, ok)
	}
}

// Renewal is only useful if the token it mints is a live core grant. The handler
// below forwards each bridge call through the real guard, so a renewal that
// produced an unknown token would come back 401 instead of the core's answer.
func TestWindowsBridgeRenewedTokenStillAuthorizesCoreCalls(t *testing.T) {
	guard := security.New("127.0.0.1:18447")
	bridge := newWindowsBridge()
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	bridge.now = func() time.Time { return now }
	app := &App{winBridge: bridge}
	key, _ := bridge.issue(guard)
	var seen []string
	core := http.NewServeMux()
	core.HandleFunc("/enhance/v1/settings", func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	handler := app.wrapWindowsBridge(guard, guard.Wrap(core))
	call := func() int {
		t.Helper()
		body := `{"protocol":1,"id":"renew","action":"core.fetch","payload":{"path":"/enhance/v1/settings","method":"GET"}}`
		r := httptest.NewRequest(http.MethodPost, "http://"+guard.Host()+"/ekd/native/v1", strings.NewReader(body))
		r.Host = guard.Host()
		r.Header.Set("Origin", "app://renderer")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-EKD-Bridge", key)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		var answer struct {
			Result struct {
				Status int `json:"status"`
			} `json:"result"`
			Error any `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil || answer.Error != nil || rec.Code != http.StatusOK {
			t.Fatalf("bridge call: %d %s (%v)", rec.Code, rec.Body, err)
		}
		return answer.Result.Status
	}
	if status := call(); status != http.StatusOK {
		t.Fatalf("core refused the issued token: %d", status)
	}
	now = now.Add(10 * time.Hour)
	if status := call(); status != http.StatusOK {
		t.Fatalf("core refused the renewed token: %d", status)
	}
	if len(seen) != 2 || seen[0] == "" || seen[1] == "" || seen[0] == seen[1] {
		t.Fatalf("expected two distinct live tokens, got %q", seen)
	}
}

func TestWindowsBridgeRateLimitAndAllowlist(t *testing.T) {
	guard := security.New("127.0.0.1:18444")
	bridge := newWindowsBridge()
	now := time.Now()
	bridge.now = func() time.Time { return now }
	key, _ := bridge.issue(guard)
	app := &App{winBridge: bridge}
	var dispatched int
	handler := app.wrapWindowsBridge(guard, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dispatched++
		if r.URL.Path != "/enhance/v1/plugins/test.example/activity" || r.URL.Query().Get("limit") != "1" {
			t.Errorf("unexpected forwarding: %s", r.URL)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	rpc := func(action string, payload any) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]any{"protocol": 1, "id": "test", "action": action, "payload": payload})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "http://"+guard.Host()+"/ekd/native/v1", bytes.NewReader(body))
		req.Header.Set("Origin", "app://renderer")
		req.Header.Set("X-EKD-Bridge", key)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	for _, tc := range []struct{ method, path string }{
		{"POST", "/enhance/v1/desktop/bridge-session"},
		{"POST", "/enhance/v1/desktop/actions/attach.status"},
		{"GET", "/enhance/v1/credentials"},
		{"PUT", "/enhance/v1/plugins/test.example/activity"},
		{"GET", "/enhance/v1/plugins/test.example/assets/%2e%2e/activity"},
		{"GET", "/enhance/v1/plugins%2ftest.example%2factivity"},
	} {
		rec := rpc("core.fetch", map[string]string{"method": tc.method, "path": tc.path})
		if !strings.Contains(rec.Body.String(), "BRIDGE_ROUTE_DENIED") {
			t.Fatalf("%s %s = %d %s", tc.method, tc.path, rec.Code, rec.Body)
		}
	}
	if rec := rpc("schedule.runNow", nil); !strings.Contains(rec.Body.String(), "BRIDGE_ACTION_DENIED") {
		t.Fatalf("unknown action = %d %s", rec.Code, rec.Body)
	}
	for _, action := range []string{"providers.updateModel", "providers.updateModels", "providers.modelSuggestion", "providers.saveOfficialForm"} {
		if !allowedWindowsBridgeAction(action) {
			t.Fatalf("expected %s in Windows bridge allowlist", action)
		}
	}
	if dispatched != 0 {
		t.Fatalf("denied requests were forwarded: %d", dispatched)
	}
	payload := map[string]string{"path": "/enhance/v1/plugins/test.example/activity?limit=1"}
	rec := rpc("core.fetch", payload)
	if rec.Code != http.StatusOK || dispatched != 1 {
		t.Fatalf("allowed route = %d %s", rec.Code, rec.Body)
	}
	for range windowsBridgeRequestsPerSecond {
		rec = rpc("window.ensureWidth", nil)
	}
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "RATE_LIMIT") {
		t.Fatalf("limit = %d %s", rec.Code, rec.Body)
	}
	now = now.Add(time.Second)
	if rec := rpc("core.fetch", payload); rec.Code != http.StatusOK || dispatched != 2 {
		t.Fatalf("next window = %d %s", rec.Code, rec.Body)
	}
}

func TestWindowsBridgeSurfaceTokensDoNotSharePermissions(t *testing.T) {
	guard := security.New("127.0.0.1:18444")
	bridge := newWindowsBridge()
	key, _ := bridge.issue(guard)
	session, ok := bridge.lookup(key, guard)
	if !ok {
		t.Fatal("bridge session missing")
	}
	handler := guard.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, tc := range []struct {
		method, path, token string
		want                int
	}{
		{"GET", "/enhance/v1/plugins", session.sessionToken, http.StatusNoContent},
		{"GET", "/enhance/v1/plugins", session.nativeToken, http.StatusForbidden},
		{"POST", "/enhance/v1/desktop/actions/accounts.delete", session.sessionToken, http.StatusForbidden},
		{"POST", "/enhance/v1/desktop/actions/accounts.delete", session.nativeToken, http.StatusNoContent},
		{"POST", "/enhance/v1/desktop/actions/providers.modelSuggestion", session.nativeToken, http.StatusNoContent},
		{"POST", "/enhance/v1/desktop/actions/providers.updateModels", session.nativeToken, http.StatusNoContent},
		{"POST", "/enhance/v1/desktop/actions/providers.saveOfficialForm", session.nativeToken, http.StatusNoContent},
		{"POST", "/enhance/v1/desktop/bridge-session", session.nativeToken, http.StatusForbidden},
		{"POST", "/enhance/v1/desktop/bridge-session", session.sessionToken, http.StatusForbidden},
	} {
		r := httptest.NewRequest(tc.method, "http://"+guard.Host()+tc.path, strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, w.Code, tc.want)
		}
	}
}

func TestWindowsBridgeForwardsPluginAssetRevisionQuery(t *testing.T) {
	guard := security.New("127.0.0.1:18444")
	bridge := newWindowsBridge()
	key, _ := bridge.issue(guard)
	app := &App{winBridge: bridge}

	var gotPath, gotVersion, gotUpdatedAt, gotCaps string
	var gotCapsPresent bool
	handler := app.wrapWindowsBridge(guard, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		q := r.URL.Query()
		gotVersion = q.Get("version")
		gotUpdatedAt = q.Get("updatedAt")
		gotCaps = q.Get("capabilities")
		gotCapsPresent = q.Has("capabilities")
		_, _ = w.Write([]byte(`{"path":"settings.js","source":"export function renderSettings(){}","version":"1.0.1","updatedAt":"2026-11-26 22:00:00 +0800 CST","capabilities":["ui:settings","ui:thinking","credentials:api..token"]}`))
	}))

	targetPath := "/enhance/v1/plugins/kimicode-plus.kct/assets/settings.js?version=1.0.1&updatedAt=2026-11-26%2022%3A00%3A00%20%2B0800%20CST&capabilities=ui%3Asettings%2Cui%3Athinking%2Ccredentials%3Aapi..token"
	if !allowedWindowsBridgeFetch(http.MethodGet, targetPath) {
		t.Fatalf("expected asset revision query path to pass allowlist: %s", targetPath)
	}
	body, err := json.Marshal(map[string]any{
		"protocol": 1,
		"id":       "asset-rev",
		"action":   "core.fetch",
		"payload": map[string]string{
			"method": "GET",
			"path":   targetPath,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://"+guard.Host()+"/ekd/native/v1", bytes.NewReader(body))
	req.Header.Set("Origin", "app://renderer")
	req.Header.Set("X-EKD-Bridge", key)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bridge asset fetch = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "BRIDGE_ROUTE_DENIED") {
		t.Fatalf("bridge unexpectedly denied asset revision query: %s", rec.Body.String())
	}
	if gotPath != "/enhance/v1/plugins/kimicode-plus.kct/assets/settings.js" ||
		gotVersion != "1.0.1" ||
		gotUpdatedAt != "2026-11-26 22:00:00 +0800 CST" ||
		!gotCapsPresent ||
		gotCaps != "ui:settings,ui:thinking,credentials:api..token" {
		t.Fatalf("asset query not forwarded intact: path=%q version=%q updatedAt=%q caps=%q hasCaps=%v", gotPath, gotVersion, gotUpdatedAt, gotCaps, gotCapsPresent)
	}
}
