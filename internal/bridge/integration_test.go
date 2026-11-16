package bridge_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enhancedkimi/internal/bridge"
	"enhancedkimi/internal/desktop"
	"enhancedkimi/internal/plugins"
	"enhancedkimi/internal/storage"
)

func TestModulesShareOneMuxAndStorage(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "integration.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	adapter := desktop.NewMock()
	safe := func() bool { return false }
	service, err := bridge.New(db, adapter, safe)
	if err != nil {
		t.Fatalf("bridge: %v", err)
	}
	manager, err := plugins.New(db, safe, plugins.WithDataDir(t.TempDir()))
	if err != nil {
		t.Fatalf("plugins: %v", err)
	}
	defer manager.Close()

	mux := http.NewServeMux()
	adapter.Register(mux)
	service.Register(mux)
	manager.Register(mux)

	check := func(method, path string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
		return recorder
	}
	for _, path := range []string{"/enhance/v1/adapter", "/enhance/v1/sessions", "/enhance/v1/tasks", "/enhance/v1/plugins", "/enhance/v1/plugins/health"} {
		recorder := check(http.MethodGet, path)
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s => %d %s", path, recorder.Code, recorder.Body.String())
		}
		if !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("GET %s missing json content type", path)
		}
	}

	preview := httptest.NewRecorder()
	body := `{"task":{"version":1,"taskId":"task-int","direction":"CODE_TO_CHAT","webTarget":{"site":"kimi","origin":"https://www.kimi.com","tabId":"mock-kimi-tab","threadId":"mock-kimi-thread"},"sessionId":"mock-session-alpha","projectId":"mock-project-alpha","source":{"site":"kimi","url":"https://www.kimi.com/chat/fixture","messageIds":["m1"]},"goal":"导出进度","messages":[{"id":"m1","text":"进度一半","role":"assistant"}],"attachments":[],"checkpoint":1,"parentCheckpoint":0,"mode":"PLAN"}}`
	mux.ServeHTTP(preview, httptest.NewRequest(http.MethodPost, "/enhance/v1/tasks/preview", strings.NewReader(body)))
	if preview.Code != http.StatusOK {
		t.Fatalf("preview => %d %s", preview.Code, preview.Body.String())
	}
	var result struct {
		ConfirmationHash string   `json:"confirmationHash"`
		Findings         []string `json:"findings"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if result.ConfirmationHash == "" {
		t.Fatalf("missing confirmation hash")
	}

	submitBody := `{"intent":"integration/1","confirmed":true,"confirmationHash":"` + result.ConfirmationHash + `","task":{"version":1,"taskId":"task-int","direction":"CODE_TO_CHAT","webTarget":{"site":"kimi","origin":"https://www.kimi.com","tabId":"mock-kimi-tab","threadId":"mock-kimi-thread"},"sessionId":"mock-session-alpha","projectId":"mock-project-alpha","source":{"site":"kimi","url":"https://www.kimi.com/chat/fixture","messageIds":["m1"]},"goal":"导出进度","messages":[{"id":"m1","text":"进度一半","role":"assistant"}],"attachments":[],"checkpoint":1,"parentCheckpoint":0,"mode":"PLAN"}}`
	submit := httptest.NewRecorder()
	mux.ServeHTTP(submit, httptest.NewRequest(http.MethodPost, "/enhance/v1/tasks", strings.NewReader(submitBody)))
	if submit.Code != http.StatusOK {
		t.Fatalf("submit => %d %s", submit.Code, submit.Body.String())
	}
	var record struct {
		State   string `json:"state"`
		Receipt struct {
			ReceiptID string `json:"receiptId"`
			State     string `json:"state"`
		} `json:"receipt"`
		CheckpointAdvanced bool `json:"checkpointAdvanced"`
	}
	if err := json.Unmarshal(submit.Body.Bytes(), &record); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	if record.State != "DELIVERED" || record.Receipt.ReceiptID == "" || !record.CheckpointAdvanced {
		t.Fatalf("unexpected delivery: %#v", record)
	}

	// The plugin module still mounts into the same mux and shares the same
	// database. The old assertion enabled a simulated builtin; the new contract is
	// a real local install, so the same intent is checked by installing a package
	// and driving its lifecycle through HTTP.
	root := t.TempDir()
	if err = os.WriteFile(filepath.Join(root, "plugin.json"), []byte(`{"id":"bridge.sample","name":"Bridge","version":"1.0.0","apiVersion":1,"hostVersions":["1.*"],"entry":{"renderer":"renderer.js"},"capabilities":["ui:sidebar"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "renderer.js"), []byte("export function activate(){}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "LICENSE"), []byte("MIT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	install := httptest.NewRecorder()
	// The user confirmed the manifest's own capability list after the preview.
	mux.ServeHTTP(install, httptest.NewRequest(http.MethodPost, "/enhance/v1/plugins/install", strings.NewReader(`{"root":"`+root+`","capabilities":["ui:sidebar"]}`)))
	if install.Code != http.StatusCreated {
		t.Fatalf("install => %d %s", install.Code, install.Body.String())
	}
	enable := httptest.NewRecorder()
	mux.ServeHTTP(enable, httptest.NewRequest(http.MethodPost, "/enhance/v1/plugins/bridge.sample/enable", nil))
	if enable.Code != http.StatusOK {
		t.Fatalf("enable => %d %s", enable.Code, enable.Body.String())
	}

	health := httptest.NewRecorder()
	mux.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/enhance/v1/plugins/health", nil))
	var healthBody map[string]any
	if err := json.Unmarshal(health.Body.Bytes(), &healthBody); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if healthBody["enabled"] != float64(1) || healthBody["total"] != float64(1) {
		t.Fatalf("unexpected plugin health: %#v", healthBody)
	}
}
