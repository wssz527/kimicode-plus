package security

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProtectedLoopbackAndResourceScopes(t *testing.T) {
	g := New("127.0.0.1:4321")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /enhance/v1/pair", g.Pair)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	h := g.Wrap(mux)
	req := func(method, path, body, host, origin, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
		r.Host = host
		if method != "GET" {
			r.Header.Set("Content-Type", "application/json")
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, test := range []struct {
		name, host, origin, token string
		status                    int
	}{{"no credential", "127.0.0.1:4321", "", "", 401}, {"DNS rebind", "evil.example:4321", "", g.pairing, 403}, {"foreign origin", "127.0.0.1:4321", "https://evil.example", g.pairing, 403}, {"null origin", "127.0.0.1:4321", "null", g.pairing, 403}, {"authorized", "127.0.0.1:4321", "http://127.0.0.1:4321", g.pairing, 204}} {
		t.Run(test.name, func(t *testing.T) {
			w := req("GET", "/enhance/v1/health", "", test.host, test.origin, test.token)
			if w.Code != test.status {
				t.Fatalf("got %d: %s", w.Code, w.Body)
			}
		})
	}
	body := `{"pairingToken":"` + g.pairing + `","permissions":["models:read"]}`
	w := req("POST", "/enhance/v1/pair", body, g.host, "http://"+g.host, "")
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	var result struct {
		SessionToken string `json:"sessionToken"`
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	if req("GET", "/enhance/v1/models", "", g.host, "", result.SessionToken).Code != 204 {
		t.Fatal("read scope rejected")
	}
	if req("POST", "/enhance/v1/models/import", "{}", g.host, "", result.SessionToken).Code != 403 {
		t.Fatal("write scope permitted")
	}
	if req("GET", "/enhance/v1/usage", "", g.host, "", result.SessionToken).Code != 403 {
		t.Fatal("cross resource read permitted")
	}
	if w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("referrer protection missing")
	}
	// Bodyless DELETE carries no JSON payload, so it must not require Content-Type.
	delReq := httptest.NewRequest(http.MethodDelete, "http://"+g.host+"/enhance/v1/credentials/translator", nil)
	delReq.Host = g.host
	delReq.Header.Set("Authorization", "Bearer "+g.pairing)
	delW := httptest.NewRecorder()
	h.ServeHTTP(delW, delReq)
	if delW.Code != 204 {
		t.Fatalf("bodyless DELETE got %d: %s", delW.Code, delW.Body.String())
	}
}
func TestNativeActionPermissionsFollowIntent(t *testing.T) {
	for action, expected := range map[string]string{
		"bootstrap": "core:read", "events.poll": "core:read", "session.view": "tasks:read",
		"session.watch": "tasks:read", "session.unwatch": "tasks:read", "config.get": "core:read",
		"diagnostics.get": "core:read", "contracts.status": "core:read", "contracts.report": "adapter:write",
		"config.save": "settings:write", "managed.usage": "usage:read", "cache.summary": "usage:read",
		"automation.get": "core:read", "automation.set": "settings:write",
		"recovery.get": "core:read", "recovery.set": "settings:write", "recovery.watch": "tasks:write",
		"recovery.check": "tasks:write", "planner.get": "chat:read", "planner.send": "chat:write",
		"update.status": "update:read", "update.check": "update:write", "update.apply": "update:write", "update.autoCheck": "update:write",
		"accounts.list": "credentials:read", "accounts.switchPreflight": "credentials:read",
		"accounts.saveCurrent": "credentials:write", "accounts.beginAdd": "credentials:write",
		"accounts.completeAdd": "credentials:write", "accounts.rename": "credentials:write",
		"accounts.delete": "credentials:write", "accounts.refreshQuotas": "credentials:write",
		"accounts.switch": "credentials:write", "accounts.cancelPending": "credentials:write",
		"accounts.resetVault": "credentials:write",
		"session.send":        "tasks:write", "session.create": "tasks:write", "session.abort": "tasks:write",
		"session.reply": "tasks:write", "handoff": "tasks:write",
	} {
		r := httptest.NewRequest(http.MethodPost, "/enhance/v1/desktop/actions/"+action, nil)
		if actual := permission(r); actual != expected {
			t.Errorf("%s: got %s, want %s", action, actual, expected)
		}
	}
}

// The update routes used to fall through to core:write, which no pairing holds,
// so a client could never read or start an update even with every documented
// scope. Each route is now classified explicitly, and the desktop actions share
// the same scopes.
func TestUpdateRoutesCarryTheirOwnPermission(t *testing.T) {
	cases := map[string]string{
		"GET /enhance/v1/update/status": "update:read",
		"POST /enhance/v1/update/check": "update:write",
		"POST /enhance/v1/update/apply": "update:write",
	}
	for route, expected := range cases {
		method, path, _ := strings.Cut(route, " ")
		r := httptest.NewRequest(method, path, nil)
		if actual := permission(r); actual != expected {
			t.Errorf("%s: got %s, want %s", route, actual, expected)
		}
	}
	for _, wanted := range []string{"update:read", "update:write"} {
		found := false
		for _, p := range Permissions {
			if p == wanted {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is missing from Permissions, so pairing can never grant it", wanted)
		}
	}
}

// An unknown or expired session token and a valid token without the resource
// scope are different failures: the first can be fixed by renewing the pairing,
// the second cannot. They used to share 403 PERMISSION_DENIED, which sent a
// client whose core had restarted into a permanent denial instead of a renew.
func TestUnknownOrExpiredTokenIsRenewable(t *testing.T) {
	g := New("127.0.0.1:4321")
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	h := g.Wrap(mux)
	call := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "http://"+g.host+"/enhance/v1/models", nil)
		r.Host = g.host
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	unknown := call("no-such-session-token")
	if unknown.Code != http.StatusUnauthorized || !strings.Contains(unknown.Body.String(), "TOKEN_EXPIRED") || !strings.Contains(unknown.Body.String(), "正在重新连接") {
		t.Fatalf("unknown token: %d %s", unknown.Code, unknown.Body)
	}
	scoped := g.IssueSessionToken([]string{"models:read"})
	if res := call(scoped); res.Code != 204 {
		t.Fatalf("scoped token rejected on its own resource: %d %s", res.Code, res.Body)
	}
	denied := call(g.IssueSessionToken([]string{"tasks:write"}))
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "PERMISSION_DENIED") || !strings.Contains(denied.Body.String(), "没有此资源权限") {
		t.Fatalf("valid token without scope: %d %s", denied.Code, denied.Body)
	}
	if got := call(g.PairingToken()); got.Code != 204 {
		t.Fatalf("pairing token should keep full access: %d %s", got.Code, got.Body)
	}
}

func TestPairingRejectsInvalidAndUnknownPermissions(t *testing.T) {
	g := New("127.0.0.1:1")
	for _, body := range []string{`{"pairingToken":"invalid"}`, `{"pairingToken":"` + g.pairing + `","permissions":["official:secrets"]}`} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		g.Pair(w, r)
		if w.Code == 200 {
			t.Fatal("invalid pairing accepted")
		}
	}
}

// The plugin runtime posts to /enhance/v1/plugin-runtime/{action}, a path that
// does not start with "plugins". An unmatched path is classified as core
// traffic, which a plugin-scoped pairing does not hold, so every plugin action
// used to fail with PERMISSION_DENIED before the plugin manager ever saw it.
func TestPluginRuntimeRoutesCountAsPluginTraffic(t *testing.T) {
	cases := map[string]string{
		"/enhance/v1/plugins":                             "plugins:read",
		"/enhance/v1/plugins/kimicode-plus.example-hello": "plugins:read",
		"/enhance/v1/plugins/x/enable":                    "plugins:write",
		"/enhance/v1/plugin-runtime/storage.set":          "plugins:write",
		"/enhance/v1/plugin-runtime/events.poll":          "plugins:write",
		// The KCT handover is driven by the plugin page, so it has to be plugin
		// traffic as well. Classified as core traffic, the confirm button answered
		// 403 to the only client that can press it.
		"/enhance/v1/kct/migration":                     "plugins:read",
		"/enhance/v1/kct/migration/confirm":             "plugins:write",
		"/enhance/v1/plugins/monitor/migration":         "plugins:read",
		"/enhance/v1/plugins/monitor/migration/confirm": "plugins:write",
		"/enhance/v1/credentials/reset":                 "credentials:write",
		"/enhance/v1/update/auto-check":                 "update:write",
	}
	for path, expected := range cases {
		method := http.MethodGet
		if strings.HasSuffix(expected, ":write") {
			method = http.MethodPost
		}
		r := httptest.NewRequest(method, path, nil)
		if actual := permission(r); actual != expected {
			t.Errorf("%s %s: got %s, want %s", method, path, actual, expected)
		}
	}
}

func TestNativeActionPermissions(t *testing.T) {
	for _, tc := range []struct{ action, permission string }{
		{"features.get", "core:read"}, {"features.set", "settings:write"},
		{"attach.status", "core:read"}, {"attach.enable", "settings:write"},
		{"keepalive.session.get", "tasks:read"}, {"keepalive.session.set", "tasks:write"},
		{"sessions.history", "tasks:read"}, {"sessions.send", "tasks:write"},
		{"models.list", "models:read"}, {"providers.modelSuggestion", "models:read"},
		{"providers.updateModel", "models:write"}, {"providers.updateModels", "models:write"}, {"providers.saveOfficialForm", "models:write"},
		{"planner.threads", "chat:read"}, {"planner.send", "chat:write"},
		{"update.status", "update:read"}, {"update.apply", "update:write"},
		{"accounts.list", "credentials:read"}, {"accounts.rename", "credentials:write"},
		{"cache.receipts", "usage:read"}, {"contracts.report", "adapter:write"},
	} {
		if actual := nativeActionPermission(tc.action); actual != tc.permission {
			t.Errorf("%s = %q, want %q", tc.action, actual, tc.permission)
		}
	}
}
