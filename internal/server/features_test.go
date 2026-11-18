package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"enhancedkimi/internal/security"
	"enhancedkimi/internal/storage"
)

func TestProviderChatVaultAndLocalFeatures(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "features.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	guard := security.New("127.0.0.1:43125")
	// A real plugin root: the bundled example plugin is released here, so the
	// install/enable paths are exercised against actual files instead of an
	// empty manager that would pass every test by having nothing to enable.
	dataDir := t.TempDir()
	app, err := NewWithNative(db, guard, false, "", NativeOptions{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	call := func(method, path string, body any, key string) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "http://127.0.0.1:43125/enhance/v1"+path, bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+guard.PairingToken())
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		app.Handler.ServeHTTP(w, r)
		return w
	}
	require := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("HTTP %d expected %d: %s", w.Code, status, w.Body.String())
		}
	}
	actionWithToken := func(action string, params map[string]any, token string) *httptest.ResponseRecorder {
		data, _ := json.Marshal(map[string]any{"params": params})
		r := httptest.NewRequest("POST", "http://127.0.0.1:43125/enhance/v1/plugins/kimicode-plus.monitor/action/"+action, bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+guard.PairingToken())
		if token != "" {
			r.Header.Set("X-Plugin-Token", token)
		}
		w := httptest.NewRecorder()
		app.Handler.ServeHTTP(w, r)
		return w
	}
	require(call("POST", "/credentials/unlock", map[string]any{"password": "synthetic-vault-password"}, ""), 200)
	require(call("PUT", "/credentials", map[string]any{"ref": "provider/demo", "value": "synthetic-provider-credential"}, ""), 200)
	var ciphertext []byte
	if err = db.QueryRow(`SELECT value FROM vault_entries WHERE ref='provider/demo'`).Scan(&ciphertext); err != nil || bytes.Contains(ciphertext, []byte("synthetic-provider-credential")) {
		t.Fatal("credential is not encrypted", err)
	}
	for _, protocol := range []string{"openai", "responses", "anthropic", "gemini"} {
		t.Run(protocol, func(t *testing.T) {
			var calls atomic.Int32
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				credential := r.Header.Get("Authorization")
				if protocol == "anthropic" {
					credential = r.Header.Get("x-api-key")
				} else if protocol == "gemini" {
					credential = r.Header.Get("x-goog-api-key")
				}
				if !strings.Contains(credential, "synthetic-provider-credential") {
					t.Error("credential header absent")
				}
				w.Header().Set("Content-Type", "application/json")
				if r.Method == "GET" {
					if protocol == "gemini" {
						fmt.Fprint(w, `{"models":[{"name":"models/synthetic","displayName":"Synthetic"}]}`)
					} else {
						fmt.Fprint(w, `{"data":[{"id":"synthetic","display_name":"Synthetic"}]}`)
					}
					return
				}
				calls.Add(1)
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("invalid request body")
				}
				switch protocol {
				case "openai":
					if !strings.HasSuffix(r.URL.Path, "/chat/completions") || body["messages"] == nil {
						t.Error("chat protocol")
					}
					fmt.Fprint(w, `{"choices":[{"message":{"content":"synthetic reply"}}],"usage":{"prompt_tokens":10,"completion_tokens":4}}`)
				case "responses":
					if r.URL.Path != "/responses" || body["input"] == nil {
						t.Error("responses protocol")
					}
					fmt.Fprint(w, `{"output":[{"content":[{"text":"synthetic reply"}]}],"usage":{"input_tokens":10,"output_tokens":4}}`)
				case "anthropic":
					if r.URL.Path != "/v1/messages" || body["max_tokens"] == nil {
						t.Error("anthropic protocol")
					}
					fmt.Fprint(w, `{"content":[{"text":"synthetic reply"}],"usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`)
				case "gemini":
					if !strings.Contains(r.URL.Path, ":generateContent") || body["contents"] == nil {
						t.Error("gemini protocol")
					}
					fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"synthetic reply"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":4,"thoughtsTokenCount":0}}`)
				}
			}))
			defer remote.Close()
			require(call("POST", "/providers", map[string]any{"id": protocol, "name": protocol, "protocol": protocol, "baseUrl": remote.URL, "secretRef": "provider/demo"}, ""), 201)
			w := call("POST", "/providers/"+protocol+"/discover", map[string]any{}, "")
			require(w, 200)
			if strings.Contains(w.Body.String(), `"mock":true`) || !strings.Contains(w.Body.String(), "synthetic") {
				t.Fatal(w.Body.String())
			}
			w = call("POST", "/chat/threads", map[string]any{"title": "protocol thread", "sourceId": "custom-model", "model": "synthetic", "providerId": protocol, "projectId": "isolated-project"}, "")
			require(w, 201)
			var thread struct {
				ID string `json:"id"`
			}
			json.Unmarshal(w.Body.Bytes(), &thread)
			path := "/chat/threads/" + thread.ID + "/messages"
			for i := 0; i < 2; i++ {
				w = call("POST", path, map[string]any{"text": "synthetic input"}, "same-intent")
				require(w, 200)
				if !strings.Contains(w.Body.String(), "synthetic reply") {
					t.Fatal(w.Body.String())
				}
			}
			if calls.Load() != 1 {
				t.Fatal("request was repeated", calls.Load())
			}
		})
	}
	w := call("GET", "/usage/summary", nil, "")
	require(w, 200)
	var summary struct {
		Requests int  `json:"requests"`
		Output   *int `json:"output"`
	}
	json.Unmarshal(w.Body.Bytes(), &summary)
	if summary.Requests != 4 || summary.Output == nil || *summary.Output != 16 {
		t.Fatal(w.Body.String())
	}
	t.Run("unknown never retries", func(t *testing.T) {
		var calls atomic.Int32
		remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, `{}`) }))
		defer remote.Close()
		require(call("POST", "/providers", map[string]any{"id": "empty", "name": "empty", "protocol": "openai", "baseUrl": remote.URL}, ""), 201)
		w := call("POST", "/chat/threads", map[string]any{"title": "unknown", "sourceId": "custom-model", "model": "synthetic", "providerId": "empty"}, "")
		require(w, 201)
		var thread struct {
			ID string `json:"id"`
		}
		json.Unmarshal(w.Body.Bytes(), &thread)
		path := "/chat/threads/" + thread.ID + "/messages"
		for i := 0; i < 2; i++ {
			w = call("POST", path, map[string]any{"text": "hello"}, "unknown-intent")
			require(w, 200)
			if !strings.Contains(w.Body.String(), "UNKNOWN") {
				t.Fatal(w.Body.String())
			}
		}
		require(call("POST", path, map[string]any{"text": "another"}, "another-intent"), 409)
		if calls.Load() != 1 {
			t.Fatal("unknown repeated")
		}
	})
	t.Run("isolated migration conflicts ignore source store", func(t *testing.T) {
		preview := func(store string) string {
			t.Helper()
			w := call("POST", "/browser/migrations/preview", map[string]any{"browser": "chrome", "target": "test", "mode": "SITES", "sites": []string{"example.com"}, "consent": true, "cookies": []any{map[string]any{"domain": ".example.com", "path": "/", "name": "session", "value": "synthetic-cookie", "storeId": store}}}, "")
			require(w, 200)
			var p struct {
				ID        string   `json:"id"`
				Conflicts []string `json:"conflicts"`
			}
			json.Unmarshal(w.Body.Bytes(), &p)
			if store == "edge-store" && len(p.Conflicts) != 1 {
				t.Fatal("store ID hid target conflict", w.Body.String())
			}
			return p.ID
		}
		id := preview("chrome-store")
		require(call("POST", "/browser/migrations/"+id+"/apply", map[string]string{"conflict": "KEEP"}, ""), 200)
		preview("edge-store")
	})
	t.Run("local policies and portable plugins", func(t *testing.T) {
		require(call("PUT", "/workspace/sessions/local", map[string]any{"title": "Local", "projectId": "isolated", "revision": 0, "lastActivity": time.Now().Add(-30 * time.Minute).UTC()}, ""), 200)
		require(call("PUT", "/workspace/compaction/default", map[string]any{"scope": "global", "target": "*", "enabled": true, "idleMinutes": 25}, ""), 200)
		w := call("GET", "/workspace/compaction", nil, "")
		require(w, 200)
		if !strings.Contains(w.Body.String(), "BLOCKED_NATIVE_UNCONNECTED") {
			t.Fatal(w.Body.String())
		}
		// Plugins are now real local installs. The assertions keep the original
		// intent: an installed plugin can be enabled, and its action endpoint goes
		// through the capability gate rather than answering 404.
		w = call("GET", "/plugins", nil, "")
		require(w, 200)
		if strings.Contains(w.Body.String(), "kimicode-plus.example-hello") {
			t.Fatalf("example-hello should not be preinstalled: %s", w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "kimicode-plus.monitor") {
			t.Fatalf("bundled plugin missing from list: %s", w.Body.String())
		}
		enable := call("POST", "/plugins/kimicode-plus.monitor/enable", map[string]any{}, "")
		require(enable, 200)
		var enabled struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(enable.Body.Bytes(), &enabled); err != nil || enabled.Token == "" {
			t.Fatalf("enable did not return a plugin identity token: %s", enable.Body.String())
		}
		// The identity token is what makes an action attributable. Without it the
		// call must be refused, and with it the granted capability applies.
		w = call("POST", "/plugins/kimicode-plus.monitor/action/runtime.info", map[string]any{}, "")
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "PLUGIN_IDENTITY_REJECTED") {
			t.Fatalf("action without an identity token was not refused: %d %s", w.Code, w.Body.String())
		}
		w = actionWithToken("runtime.info", map[string]any{}, enabled.Token)
		require(w, 200)
		if !strings.Contains(w.Body.String(), "kimicode-plus.monitor") {
			t.Fatal(w.Body.String())
		}
		// The action route exists, so an ungranted capability must be refused with
		// the documented code instead of falling through to NOT_FOUND.
		w = actionWithToken("config.save", map[string]any{"patch": map[string]any{"x": 1}}, enabled.Token)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "PLUGIN_CAPABILITY_DENIED") {
			t.Fatalf("capability gate did not deny config.save: %d %s", w.Code, w.Body.String())
		}
		require(call("POST", "/plugins/kimicode-plus.monitor/disable", map[string]any{}, ""), 200)
	})
	require(call("POST", "/credentials/lock", map[string]any{}, ""), 200)
	app.Close()
	app, err = New(db, guard, false)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	w = call("GET", "/credentials", nil, "")
	require(w, 200)
	if !strings.Contains(w.Body.String(), `"unlocked":false`) {
		t.Fatal("vault not locked on restart")
	}
}
