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
}
func TestNativeActionPermissionsFollowIntent(t *testing.T) {
	for action, expected := range map[string]string{
		"bootstrap": "core:read", "events.poll": "core:read", "session.view": "tasks:read",
		"session.watch": "tasks:read", "session.unwatch": "tasks:read", "config.get": "core:read",
		"config.save": "settings:write", "managed.usage": "usage:read", "monitor.summary": "usage:read",
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
