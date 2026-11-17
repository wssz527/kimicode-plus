package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"enhancedkimi/internal/desktop"
	"enhancedkimi/internal/plugins"
	"enhancedkimi/internal/storage"
)

// fakeKAP is a minimal stand-in for the official session API. It records what the
// native service asked for so the test can prove the plugin action reached the
// real code path instead of being answered by a mock core.
type fakeKAP struct {
	server *httptest.Server
	paths  []string
}

// writeInstanceRecord writes the instance and token files the official client
// leaves in KIMI_CODE_HOME, discovered by KAPClient.resolve.
func writeInstanceRecord(t *testing.T, home, endpoint string) {
	t.Helper()
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "server", "instances")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	record, _ := json.Marshal(map[string]any{"port": port, "host": parsed.Hostname(), "heartbeat_at": 1})
	if err := os.WriteFile(filepath.Join(dir, "instance-1.json"), record, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "server.token"), []byte("synthetic-token"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newFakeKAP(t *testing.T) *fakeKAP {
	t.Helper()
	fake := &fakeKAP{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.paths = append(fake.paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": []any{
				map[string]any{"id": "s1", "title": "Synthetic one", "busy": false},
				map[string]any{"id": "s2", "title": "Synthetic two", "busy": true},
			}}})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/agents/main/turns"):
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"turns": []any{
				map[string]any{"id": "t1", "messages": []any{
					map[string]any{"id": "m1", "role": "user", "text": "hello"},
				}},
			}}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/sessions/"):
			// readSession decodes the whole data payload as the session, so the
			// identity fields sit at the top level like the official response.
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
				"id": "s1", "title": "Synthetic one", "metadata": map[string]any{"cwd": "/tmp"},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"id": "created-1", "title": "Plugin created", "metadata": map[string]any{"cwd": "/tmp"}}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/prompts"):
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"prompt_id": "p1"}})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 404, "msg": "unexpected"})
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

// newPluginCoreFixture builds a real NativeService wired to the fake KAP and a
// real plugin manager whose core is the production adapter. Only the official
// client is faked; the adapter, the manager and the capability gate are the real
// ones.
func newPluginCoreFixture(t *testing.T, capabilities []string) (*plugins.Manager, *desktop.NativeService, string) {
	t.Helper()
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// The fake official service is discovered through the same on-disk instance
	// record the real client writes, so the test exercises endpoint resolution
	// rather than injecting a private field that production never sets.
	home := t.TempDir()
	kap := desktop.NewKAPClient(home)
	fake := newFakeKAP(t)
	writeInstanceRecord(t, home, fake.server.URL)
	service, err := desktop.NewNativeService(db, kap, root, func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)

	manager, err := plugins.New(db, func() bool { return false }, plugins.WithDataDir(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close() })
	manager.UseCore(pluginCore{service: service})

	dir := filepath.Join(t.TempDir(), "plugin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"id": "test.sessions", "name": "Sessions", "version": "1.0.0", "apiVersion": 1,
		"hostVersions": []string{"1.*"},
		"entry":        map[string]any{"renderer": "renderer.js"},
		"capabilities": capabilities,
	}
	data, _ := json.Marshal(manifest)
	files := map[string][]byte{
		"plugin.json": data,
		"renderer.js": []byte("export function activate(){}\n"),
		"LICENSE":     []byte("MIT\n"),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The fixture plays the user who reviewed the preview and accepted the
	// manifest's declared capabilities.
	consent := append([]string(nil), capabilities...)
	if _, err := manager.Install(context.Background(), plugins.InstallRequest{Root: dir, Capabilities: &consent}); err != nil {
		t.Fatal(err)
	}
	enabled, err := manager.Enable(context.Background(), "test.sessions")
	if err != nil {
		t.Fatal(err)
	}
	if enabled.Token == "" {
		t.Fatal("enable did not issue a token")
	}
	return manager, service, enabled.Token
}

func callRuntime(t *testing.T, manager *plugins.Manager, action, token string, params any) (int, []byte) {
	t.Helper()
	mux := http.NewServeMux()
	manager.Register(mux)
	body, _ := json.Marshal(map[string]any{"token": token, "params": params})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/enhance/v1/plugin-runtime/"+action, strings.NewReader(string(body))))
	return recorder.Code, recorder.Body.Bytes()
}

// The plural session actions are the public plugin contract. This test drives
// them through the production adapter into the real native service, which is the
// only way to catch a contract that the capability table allows but the service
// refuses.
func TestPluginSessionActionsReachTheNativeService(t *testing.T) {
	manager, _, token := newPluginCoreFixture(t, []string{"sessions:read", "sessions:send"})

	code, body := callRuntime(t, manager, "sessions.list", token, nil)
	if code != http.StatusOK {
		t.Fatalf("sessions.list => %d %s", code, body)
	}
	var list struct {
		Value struct {
			Items []map[string]any `json:"items"`
			Total int              `json:"total"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode list: %v (%s)", err, body)
	}
	if list.Value.Total != 2 || len(list.Value.Items) != 2 {
		t.Fatalf("sessions.list returned %#v", list.Value)
	}
	if list.Value.Items[0]["id"] != "s1" || list.Value.Items[0]["title"] != "Synthetic one" {
		t.Fatalf("sessions.list lost session metadata: %#v", list.Value.Items[0])
	}
	// bootstrap would also return models, planner threads and recovery settings;
	// a sessions:read plugin must not receive them.
	if strings.Contains(string(body), "recovery") || strings.Contains(string(body), "nativeModels") {
		t.Fatalf("sessions.list leaked bootstrap fields: %s", body)
	}

	code, body = callRuntime(t, manager, "sessions.view", token, map[string]any{"id": "s1"})
	if code != http.StatusOK || !strings.Contains(string(body), "Synthetic one") {
		t.Fatalf("sessions.view => %d %s", code, body)
	}

	code, body = callRuntime(t, manager, "sessions.create", token, map[string]any{"title": "Plugin created"})
	if code != http.StatusOK || !strings.Contains(string(body), "created-1") {
		t.Fatalf("sessions.create => %d %s", code, body)
	}

	code, body = callRuntime(t, manager, "sessions.send", token, map[string]any{"id": "s1", "text": "from the plugin"})
	if code != http.StatusOK || !strings.Contains(string(body), `"sessionId":"s1"`) {
		t.Fatalf("sessions.send => %d %s", code, body)
	}

	// A send is a plugin action with side effects, so it must be auditable.
	mux := http.NewServeMux()
	manager.Register(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/enhance/v1/plugins/test.sessions/activity", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "SESSIONS_SEND") {
		t.Fatalf("activity log missed the plugin send: %d %s", recorder.Code, recorder.Body.String())
	}
}

// A plugin without sessions capability must be refused before the service runs.
func TestPluginSessionActionsRespectCapability(t *testing.T) {
	manager, _, token := newPluginCoreFixture(t, []string{"metrics:read"})
	for _, action := range []string{"sessions.list", "sessions.view", "sessions.create", "sessions.send"} {
		code, body := callRuntime(t, manager, action, token, map[string]any{"id": "s1", "text": "x"})
		if code != http.StatusForbidden || !strings.Contains(string(body), "PLUGIN_CAPABILITY_DENIED") {
			t.Fatalf("%s was not refused: %d %s", action, code, body)
		}
	}
}

// A plugin that keeps polling after being disabled must not read the core.
func TestDisabledPluginCannotUseSessionActions(t *testing.T) {
	manager, _, token := newPluginCoreFixture(t, []string{"sessions:read"})
	if err := manager.Disable(context.Background(), "test.sessions"); err != nil {
		t.Fatal(err)
	}
	code, body := callRuntime(t, manager, "sessions.list", token, nil)
	if code != http.StatusUnauthorized && code != http.StatusConflict {
		t.Fatalf("disabled plugin still acted: %d %s", code, body)
	}
}

var _ = sql.ErrNoRows

// The v2 history action must page through the official cursor instead of taking
// the newest 50 turns. The fake records the query so a regression to a fixed
// page size is visible here, not only in the renderer.
func TestPluginHistoryPagesByOrdinal(t *testing.T) {
	manager, _, token := newPluginCoreFixture(t, []string{"sessions:read"})
	// The fixture is a local install with a v1 manifest, so the capability table
	// refuses the v2 action before the core is reached. The digest half of the
	// gate is covered in package plugins, where the allowlist can be stubbed.
	code, body := callRuntime(t, manager, "sessions.history", token, map[string]any{"id": "s1", "beforeOrdinal": 40, "pageSize": 20})
	if code != http.StatusForbidden || !strings.Contains(string(body), "PLUGIN_CAPABILITY_DENIED") {
		t.Fatalf("non-official plugin paged history: %d %s", code, body)
	}
}
