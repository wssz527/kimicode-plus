package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPluginSendClaimsOnlyRegisteredCurrentSessionAttachments(t *testing.T) {
	core := &pluginCoreDouble{}
	manager, token := officialManager(t, core, []string{CapSessionsSend, CapAttachments})
	record, err := os.CreateTemp(t.TempDir(), "unregistered-*")
	if err != nil {
		t.Fatal(err)
	}
	record.Close()
	for action, field := range map[string]string{"sessions.send": "attachments", "attachments.send": "paths"} {
		if status, _ := callPlugin(t, manager, token, action, map[string]any{"id": "s1", field: []string{record.Name()}}); status != http.StatusForbidden {
			t.Fatalf("%s accepted an unregistered path: %d", action, status)
		}
	}
	if core.last().action != "" {
		t.Fatal("a rejected attachment reached the core")
	}
	_, value := callPlugin(t, manager, token, "attachments.stage", map[string]any{
		"files": []any{map[string]any{"name": "drop.txt", "data": base64Of("dropped")}},
	})
	files, batch := stagedFiles(t, value)
	path := files[0]["path"].(string)
	undeclared := filepath.Join(batch, "unregistered.txt")
	if err := os.WriteFile(undeclared, []byte("local fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if status, _ := callPlugin(t, manager, token, "attachments.send", map[string]any{"id": "s1", "paths": []string{path, undeclared}}); status != http.StatusForbidden {
		t.Fatalf("mixed batch accepted an unregistered file: %d", status)
	}
	if _, err := manager.validateStagedPaths("test.official", "other-session", []string{path}); err == nil {
		t.Fatal("another plugin session can consume a stage")
	}
	payload := map[string]any{"id": "s1", "attachments": []string{path}}
	if status, body := callPlugin(t, manager, token, "sessions.send", payload); status != http.StatusOK {
		t.Fatalf("staged send = %d %v", status, body)
	}
	if status, _ := callPlugin(t, manager, token, "sessions.send", payload); status != http.StatusForbidden {
		t.Fatalf("consumed attachment reused: %d", status)
	}
	if _, err := os.Stat(batch); !os.IsNotExist(err) {
		t.Fatalf("claimed batch not cleaned up: %v", err)
	}
}

func TestSessionsSendWithoutAttachmentCapability(t *testing.T) {
	core := &pluginCoreDouble{}
	manager, token := officialManager(t, core, []string{CapSessionsSend})
	params := map[string]any{"id": "s1", "text": "hi", "attachments": []string{filepath.Join(t.TempDir(), "dummy.txt")}}
	if status, body := callPlugin(t, manager, token, "sessions.send", params); status != http.StatusForbidden || core.last().action != "" {
		t.Fatalf("sessions.send with attachments = %d %v", status, body)
	}
	delete(params, "attachments")
	if status, body := callPlugin(t, manager, token, "sessions.send", params); status != http.StatusOK || core.last().action != "sessions.send" {
		t.Fatalf("text-only sessions.send = %d %v", status, body)
	}
}

func TestPluginActivityRetentionAndPagination(t *testing.T) {
	manager, _ := officialManager(t, &pluginCoreDouble{}, []string{CapSessionsSend})
	ctx := context.Background()
	now := manager.now().Add(2 * time.Hour)
	manager.now = func() time.Time { return now }
	const id = "test.official"
	if _, err := manager.db.Exec(`DELETE FROM plugin_activity`); err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		kind, detail string
		at           time.Time
	}{
		{ActivitySession, "expired", now.Add(-31 * 24 * time.Hour)},
		{ActivitySession, "retained", now.Add(-24 * time.Hour)},
		{ActivityNetwork, "POST example.test", now.Add(-30 * time.Second)},
		{ActivityNetwork, "POST example.test", now},
		{ActivitySession, "newest", now},
	} {
		if err := recordActivity(ctx, manager.db, id, event.kind, event.detail, event.at); err != nil {
			t.Fatal(err)
		}
	}
	if err := recordActivity(ctx, manager.db, "other", ActivitySession, "other", now); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/?offset=1&limit=1", nil)
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	manager.handleActivity(w, r)
	var body struct {
		Items []Activity
		Total int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK || body.Total != 3 || len(body.Items) != 1 || body.Items[0].Kind != ActivityNetwork {
		t.Fatalf("paged activity = %d %s (%v)", w.Code, w.Body, err)
	}
	items, total, err := loadActivity(ctx, manager.db, id, body.Items[0].ID, 0, 10)
	if err != nil || total != 3 || len(items) != 1 || items[0].Detail != "retained" {
		t.Fatalf("cursor page = %v, total=%d, err=%v", items, total, err)
	}
}

func TestPluginUninstallRejectsMalformedBodyBeforeChangingState(t *testing.T) {
	manager, _ := officialManager(t, &pluginCoreDouble{}, []string{CapSessionsSend})
	for _, body := range []string{`{"deleteData":`, `{"deleteData":true} {}`, `null`, `{"deleteSchedulerData":true}`} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		r.SetPathValue("id", "test.official")
		w := httptest.NewRecorder()
		manager.handleUninstall(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid body %q = %d %s", body, w.Code, w.Body)
		}
		if _, ok := manager.Get("test.official"); !ok {
			t.Fatalf("request %q removed the installed plugin", body)
		}
	}
}

func TestKCTProxyAllowsOnlyConfiguredOrFixedTranslationRoutes(t *testing.T) {
	manager, _ := testManager(t, nil)
	ctx := context.Background()
	if err := storageSet(ctx, manager.db, KCTPluginID, KCTSettingsKey, json.RawMessage(`{"provider":"custom","customEndpoint":"https://translate.example:443/v1/chat/completions?mode=local","useCredential":true}`), manager.now()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		url, method, credential string
		allowed                 bool
	}{
		{"https://translate.example/v1/chat/completions?mode=local", http.MethodPost, "translator", true},
		{"https://translate.example/v1/chat/completions", http.MethodPost, "translator", false},
		{"https://translate.example:444/v1/chat/completions?mode=local", http.MethodPost, "translator", false},
		{"https://translate.example/v1/chat/completions?mode=local#fragment", http.MethodPost, "", false},
		{"https://translate.googleapis.com/translate_a/single?client=gtx", http.MethodPost, "", true},
		{"https://translate.googleapis.com/translate_a/single?client=gtx", http.MethodPost, "translator", false},
		{"https://edge.microsoft.com/translate/translatetext", http.MethodGet, "", false},
		{"http://127.0.0.1:11434/v1/chat/completions", http.MethodPost, "", false},
	} {
		u, err := parseProxyURL(tc.url)
		if err != nil {
			t.Fatal(err)
		}
		err = manager.authorizeTranslationEndpoint(ctx, u, tc.method, tc.credential)
		if (err == nil) != tc.allowed {
			t.Errorf("%s %s allowed=%v, err = %v", tc.method, tc.url, tc.allowed, err)
		}
	}
}
