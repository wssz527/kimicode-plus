package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"enhancedkimi/internal/security"
	"enhancedkimi/internal/storage"
	"enhancedkimi/internal/updater"
)

func TestUpdateRoutesAndNativeActionsShareManager(t *testing.T) {
	t.Setenv("EKD_UPDATE_REPO", "invalid/repo/owner")
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	guard := security.New("127.0.0.1:8321")
	app, err := NewWithNative(db, guard, true, "", NativeOptions{Home: t.TempDir(), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://"+guard.Host()+path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		app.Handler.ServeHTTP(w, r)
		return w
	}
	path := "/enhance/v1/update/"
	if res := request(http.MethodGet, path+"status", "", ""); res.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status: %d %s", res.Code, res.Body)
	}
	if res := request(http.MethodPost, path+"apply", guard.IssueSessionToken([]string{"models:read"}), ""); res.Code != http.StatusForbidden {
		t.Fatalf("scoped token allowed update: %d %s", res.Code, res.Body)
	}
	token := guard.PairingToken()
	res := request(http.MethodGet, path+"status", token, "")
	var status updater.Status
	if err := json.Unmarshal(res.Body.Bytes(), &status); err != nil || res.Code != http.StatusOK || status.CurrentVersion != Version || status.State != updater.Failed {
		t.Fatalf("status: %d %s, %v", res.Code, res.Body, err)
	}
	if res := request(http.MethodPost, path+"check", token, ""); res.Code != http.StatusBadGateway || !strings.Contains(res.Body.String(), "owner/repo") {
		t.Fatalf("check: %d %s", res.Code, res.Body)
	}
	if res := request(http.MethodPost, path+"apply", token, ""); res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "安全模式") {
		t.Fatalf("apply: %d %s", res.Code, res.Body)
	}
	res = request(http.MethodPost, "/enhance/v1/desktop/actions/update.status", token, "{}")
	var action struct {
		Value updater.Status `json:"value"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &action); err != nil || res.Code != http.StatusOK || action.Value != status {
		t.Fatalf("native status: %d %s, %v", res.Code, res.Body, err)
	}
	bridgeBootstrapToken := guard.IssueSessionToken([]string{security.WindowsBridgePermission})
	res = request(http.MethodPost, "/enhance/v1/desktop/bridge-session", bridgeBootstrapToken, "{}")
	var bridge struct{ BridgeKey string }
	if err := json.Unmarshal(res.Body.Bytes(), &bridge); err != nil || res.Code != http.StatusOK || bridge.BridgeKey == "" {
		t.Fatalf("could not create Windows bridge session: HTTP %d, %v", res.Code, err)
	}
	bridgeRequest := httptest.NewRequest(http.MethodPost, "http://"+guard.Host()+"/ekd/native/v1", strings.NewReader(`{"protocol":1,"id":"update-test","action":"update.status","payload":{}}`))
	bridgeRequest.Header.Set("Content-Type", "application/json")
	bridgeRequest.Header.Set("Origin", "app://renderer")
	bridgeRequest.Header.Set("X-EKD-Bridge", bridge.BridgeKey)
	res = httptest.NewRecorder()
	app.Handler.ServeHTTP(res, bridgeRequest)
	var bridgeAction struct{ Result updater.Status }
	if err := json.Unmarshal(res.Body.Bytes(), &bridgeAction); err != nil || res.Code != http.StatusOK || bridgeAction.Result != status {
		t.Fatalf("Windows bridge status: %d %s, %v", res.Code, res.Body, err)
	}
}

// The update surface is reached two ways — the routes and the desktop actions —
// and both must be usable by a session token that holds only the update scopes.
// Before the split, every update path fell through to core:write, which no
// pairing grants, so the update check and apply were unreachable for every real
// client even though /pair had just handed it a token.
func TestUpdateRoutesAndActionsAcceptPairedSessionTokens(t *testing.T) {
	t.Setenv("EKD_UPDATE_REPO", "invalid/repo/owner")
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	guard := security.New("127.0.0.1:8322")
	app, err := NewWithNative(db, guard, true, "", NativeOptions{Home: t.TempDir(), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://"+guard.Host()+path, bytes.NewBufferString(body))
		r.Header.Set("Host", guard.Host())
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://"+guard.Host())
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		app.Handler.ServeHTTP(w, r)
		return w
	}
	pair := func(permissions ...string) string {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"pairingToken": guard.PairingToken(), "permissions": permissions})
		res := request(http.MethodPost, "/enhance/v1/pair", "", string(body))
		if res.Code != http.StatusOK {
			t.Fatalf("pair %v: %d %s", permissions, res.Code, res.Body)
		}
		var paired struct {
			SessionToken string `json:"sessionToken"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &paired); err != nil || paired.SessionToken == "" {
			t.Fatalf("pair %v answered no session token: %v %s", permissions, err, res.Body)
		}
		return paired.SessionToken
	}
	reader := pair("update:read")
	applier := pair("update:read", "update:write")
	restricted := pair("tasks:write", "models:read")

	var want updater.Status
	res := request(http.MethodGet, "/enhance/v1/update/status", reader, "")
	if err := json.Unmarshal(res.Body.Bytes(), &want); err != nil || res.Code != http.StatusOK {
		t.Fatalf("update:read status: %d %s, %v", res.Code, res.Body, err)
	}
	if res := request(http.MethodGet, "/enhance/v1/update/status", applier, ""); res.Code != http.StatusOK {
		t.Fatalf("update:write session cannot read status: %d %s", res.Code, res.Body)
	}
	res = request(http.MethodPost, "/enhance/v1/desktop/actions/update.status", reader, "{}")
	var readAction struct {
		Value updater.Status `json:"value"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &readAction); err != nil || res.Code != http.StatusOK || readAction.Value != want {
		t.Fatalf("update:read action status: %d %s, %v", res.Code, res.Body, err)
	}

	if res := request(http.MethodPost, "/enhance/v1/update/check", applier, ""); res.Code == http.StatusForbidden {
		t.Fatalf("update:write session was refused the check: %d %s", res.Code, res.Body)
	} else if res.Code != http.StatusBadGateway || !strings.Contains(res.Body.String(), "owner/repo") {
		t.Fatalf("update:write check: %d %s", res.Code, res.Body)
	}
	if res := request(http.MethodPost, "/enhance/v1/update/apply", applier, ""); res.Code == http.StatusForbidden {
		t.Fatalf("update:write session was refused apply: %d %s", res.Code, res.Body)
	} else if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "安全模式") {
		t.Fatalf("update:write apply: %d %s", res.Code, res.Body)
	}
	for _, action := range []string{"update.check", "update.apply"} {
		res := request(http.MethodPost, "/enhance/v1/desktop/actions/"+action, applier, "{}")
		if res.Code == http.StatusForbidden {
			t.Fatalf("update:write session was refused %s: %d %s", action, res.Code, res.Body)
		}
		if res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("desktop %s: %d %s", action, res.Code, res.Body)
		}
	}

	for _, test := range []struct{ mint, path, body string }{
		{mint: "/enhance/v1/update/apply", path: "/enhance/v1/update/apply"},
		{mint: "/enhance/v1/update/check", path: "/enhance/v1/update/check"},
		{mint: "/enhance/v1/desktop/actions/update.apply", path: "/enhance/v1/desktop/actions/update.apply", body: "{}"},
		{mint: "/enhance/v1/desktop/actions/update.check", path: "/enhance/v1/desktop/actions/update.check", body: "{}"},
	} {
		for label, token := range map[string]string{"tasks:write": restricted, "update:read": reader} {
			res := request(http.MethodPost, test.path, token, test.body)
			if res.Code != http.StatusForbidden || !strings.Contains(res.Body.String(), "PERMISSION_DENIED") {
				t.Errorf("%s with %s: %d %s", test.mint, label, res.Code, res.Body)
			}
		}
		if res := request(http.MethodGet, "/enhance/v1/update/status", restricted, ""); res.Code != http.StatusForbidden || !strings.Contains(res.Body.String(), "PERMISSION_DENIED") {
			t.Errorf("status with tasks:write: %d %s", res.Code, res.Body)
		}
		if res := request(http.MethodPost, "/enhance/v1/desktop/actions/update.status", restricted, "{}"); res.Code != http.StatusForbidden {
			t.Errorf("update.status action with tasks:write: %d %s", res.Code, res.Body)
		}
	}
	if res := request(http.MethodGet, "/enhance/v1/update/status", "stale-session-token", ""); res.Code != http.StatusUnauthorized || !strings.Contains(res.Body.String(), "TOKEN_EXPIRED") {
		t.Fatalf("unknown token must ask for a renew: %d %s", res.Code, res.Body)
	}
}

func TestUpdateCheckRouteHandlesUnpublishedRepository(t *testing.T) {
	t.Setenv("EKD_UPDATE_REPO", "example/releases")
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/example/releases/releases/latest" {
			t.Errorf("unexpected latest release request: %s", r.URL.Path)
		}
		http.NotFound(w, r)
	}))
	defer github.Close()
	manager := updater.New(updater.Options{CurrentVersion: Version, APIBaseURL: github.URL, AllowInsecureHTTP: true})
	defer manager.Close()
	mux := http.NewServeMux()
	registerUpdateRoutes(mux, manager)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/enhance/v1/update/check", nil))
	var status updater.Status
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil || w.Code != http.StatusOK || status.State != updater.UpToDate || status.Error != "" || !strings.Contains(status.Note, "尚未发布") {
		t.Fatalf("404 release: %d %s, %v", w.Code, w.Body, err)
	}
}
