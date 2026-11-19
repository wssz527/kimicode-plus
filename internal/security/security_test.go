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
		"config.save":     "settings:write", "managed.usage": "usage:read", "cache.summary": "usage:read",
		"automation.get": "core:read", "automation.set": "settings:write",
		"recovery.get": "core:read", "recovery.set": "settings:write", "recovery.watch": "tasks:write",
		"recovery.check": "tasks:write", "planner.get": "chat:read", "planner.send": "chat:write",
		"session.send": "tasks:write", "session.create": "tasks:write", "session.abort": "tasks:write",
		"session.reply": "tasks:write", "handoff": "tasks:write",
	} {
		r := httptest.NewRequest(http.MethodPost, "/enhance/v1/desktop/actions/"+action, nil)
		if actual := permission(r); actual != expected {
			t.Errorf("%s: got %s, want %s", action, actual, expected)
		}
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
		"/enhance/v1/kct/migration":         "plugins:read",
		"/enhance/v1/kct/migration/confirm": "plugins:write",
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
