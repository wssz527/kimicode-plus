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
	adapterToken := guard.IssueSessionToken([]string{"adapter:write"})
	res = request(http.MethodPost, "/enhance/v1/desktop/bridge-session", adapterToken, "{}")
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
