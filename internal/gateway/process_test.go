package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"enhancedkimi/internal/provider"
	"enhancedkimi/internal/security"
	"enhancedkimi/internal/storage"
)

// TestGatewayThroughRealProcess exercises the shipped binary end to end: a real
// listener, a real runtime file, a real SQLite database and a synthetic
// upstream. The gateway is only accepted here if the built process serves it.
// seedProvider writes the provider and catalog rows the gateway routes on. It
// runs against the same SQLite file the process opened, before the first
// completion is sent.
func seedProvider(t *testing.T, path, id, baseURL, model string) {
	t.Helper()
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open seeded db: %v", err)
	}
	defer db.Close()
	if _, err = db.Exec(`INSERT INTO registry_providers(id,name,protocol,base_url,secret_ref,created_at) VALUES(?,?,?,?,?,?)`,
		id, "Synthetic", "openai", baseURL, "provider/synthetic/account", "2026-11-18T00:00:00Z"); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if _, err = db.Exec(`INSERT INTO registry_models(provider_id,model_id,display_name) VALUES(?,?,?)`, id, model, "Synthetic"); err != nil {
		t.Fatalf("seed model: %v", err)
	}
}

func TestGatewayThroughRealProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "ekd")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, "./cmd/enhanced-kimi")
	build.Dir = repo
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Synthetic upstream: emits reasoning then content, then usage.
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/models"):
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"data":[{"id":"synthetic-model","display_name":"Synthetic"}]}`)
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			if r.Header.Get("Authorization") != "Bearer synthetic-upstream-key" {
				t.Errorf("upstream credential %q", r.Header.Get("Authorization"))
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			// Kimi extension fields and tools must survive the gateway. Only
			// the tool turn is expected to carry them.
			if len(body["messages"].([]any)) == 1 && body["messages"].([]any)[0].(map[string]any)["content"] == "use the tool" {
				for _, key := range []string{"thinking", "max_completion_tokens", "tools", "tool_choice"} {
					if _, ok := body[key]; !ok {
						t.Errorf("%s dropped before upstream: %+v", key, body)
					}
				}
			}
			if body["stream"] == true {
				w.Header().Set("Content-Type", "text/event-stream")
				flusher := w.(http.Flusher)
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"plan \"},\"finish_reason\":null}]}\n\n")
				flusher.Flush()
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello \"},\"finish_reason\":null}]}\n\n")
				flusher.Flush()
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"world\"},\"finish_reason\":\"stop\"}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":5}}\n\n")
				fmt.Fprint(w, "data: [DONE]\n\n")
				flusher.Flush()
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if _, ok := body["tools"]; ok {
				io.WriteString(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"add","arguments":"{\"a\":1}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":6,"completion_tokens":3}}`)
				return
			}
			io.WriteString(w, `{"choices":[{"message":{"content":"buffered hello"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	cmd := exec.Command(bin, "-data-dir", dataDir, "-port", "0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	ready := make(chan map[string]any, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			var record map[string]any
			if json.Unmarshal(scanner.Bytes(), &record) == nil && record["status"] == "READY" {
				ready <- record
				return
			}
		}
		ready <- nil
	}()
	var record map[string]any
	select {
	case record = <-ready:
	case <-time.After(30 * time.Second):
		t.Fatal("process did not become ready")
	}
	if record == nil {
		t.Fatal("no READY record")
	}
	base := record["url"].(string)
	token, err := LoadOrCreateToken(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	pairing := record["pairingToken"].(string)
	client := &http.Client{Timeout: 30 * time.Second}
	call := func(method, path, body, bearer string) (*http.Response, string) {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, base+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		return res, string(data)
	}

	// The provider registry page is gone, so a provider and its catalog are no
	// longer configured over HTTP. The credential still goes through the real
	// API, and the rows the gateway reads are written the way the registry
	// migration leaves them.
	if res, body := call("POST", "/enhance/v1/credentials/unlock", `{"password":"synthetic-gateway-password"}`, pairing); res.StatusCode != 200 {
		t.Fatalf("unlock %d: %s", res.StatusCode, body)
	}
	if res, body := call("PUT", "/enhance/v1/credentials", `{"ref":"provider/synthetic/account","value":"synthetic-upstream-key"}`, pairing); res.StatusCode != 200 {
		t.Fatalf("credential %d: %s", res.StatusCode, body)
	}
	seedProvider(t, filepath.Join(dataDir, "enhancement.db"), "synthetic", upstream.URL, "synthetic-model")

	// Gateway unauthenticated and authenticated.
	if res, _ := call("GET", "/v1/models", "", ""); res.StatusCode != 401 {
		t.Fatalf("gateway without token %d", res.StatusCode)
	}
	res, body := call("GET", "/v1/models", "", token)
	if res.StatusCode != 200 || !strings.Contains(body, "synthetic-model") {
		t.Fatalf("models %d: %s", res.StatusCode, body)
	}
	if strings.Contains(body, "synthetic-upstream-key") {
		t.Fatal("models leaked the upstream key")
	}
	if res, body := call("GET", "/enhance/v1/gateway", "", pairing); res.StatusCode != 200 || !strings.Contains(body, `"toolCalls":true`) {
		t.Fatalf("gateway capabilities %d: %s", res.StatusCode, body)
	}

	// Buffered completion.
	res, body = call("POST", "/v1/chat/completions", `{"model":"synthetic-model","messages":[{"role":"user","content":"hi"}]}`, token)
	if res.StatusCode != 200 || !strings.Contains(body, "buffered hello") {
		t.Fatalf("buffered %d: %s", res.StatusCode, body)
	}

	// Streaming completion: reasoning, content, usage, terminator.
	res, body = call("POST", "/v1/chat/completions", `{"model":"synthetic-model","messages":[{"role":"user","content":"hi"}],"stream":true}`, token)
	if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream %d %q: %s", res.StatusCode, res.Header.Get("Content-Type"), body)
	}
	for _, want := range []string{`"reasoning_content":"plan "`, `"content":"hello "`, `"finish_reason":"stop"`, `"prompt_tokens":11`, "data: [DONE]"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %s: %s", want, body)
		}
	}

	// Tool-call round trip through the real process: assistant tool_calls come
	// back, and a tool result returns with its call id.
	res, body = call("POST", "/v1/chat/completions", `{"model":"synthetic-model","messages":[{"role":"user","content":"use the tool"}],"tools":[{"type":"function","function":{"name":"add","parameters":{"type":"object"}}}],"tool_choice":"auto","thinking":{"type":"enabled"},"max_completion_tokens":900}`, token)
	if res.StatusCode != 200 {
		t.Fatalf("tool turn %d: %s", res.StatusCode, body)
	}
	var toolReply struct {
		Choices []struct {
			Message struct {
				ToolCalls []provider.ToolCall `json:"tool_calls"`
			} `json:"message"`
			Finish string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(body), &toolReply); err != nil {
		t.Fatal(err)
	}
	if len(toolReply.Choices) != 1 || len(toolReply.Choices[0].Message.ToolCalls) != 1 || toolReply.Choices[0].Finish != "tool_calls" {
		t.Fatalf("tool calls not relayed by the real process: %s", body)
	}

	// The gateway token is stable across a restart.
	if runtime.GOOS == "windows" {
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
	} else if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	stopped = true
	again, err := LoadOrCreateToken(dataDir)
	if err != nil || again != token {
		t.Fatalf("gateway token changed across restart: %v", err)
	}
	db, err := storage.Open(filepath.Join(dataDir, "enhancement.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var events int
	if err := db.QueryRow(`SELECT count(*) FROM usage_events WHERE request_kind='gateway'`).Scan(&events); err != nil || events == 0 {
		t.Fatalf("gateway usage not recorded: %d %v", events, err)
	}
	var stored []byte
	if err := db.QueryRow(`SELECT value FROM vault_entries WHERE ref='provider/synthetic/account'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "synthetic-upstream-key") {
		t.Fatal("credential stored in plaintext")
	}
	if upstreamCalls.Load() == 0 {
		t.Fatal("synthetic upstream was never called")
	}
	info, err := os.Stat(filepath.Join(dataDir, tokenFile))
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode: %v %v", info, err)
	}
}

// The guard used by the enhancement API must not accidentally authorize /v1.
func TestGuardTokenIsNotAGatewayToken(t *testing.T) {
	guard := security.New("127.0.0.1:1")
	token := guard.PairingToken()
	if token == "" {
		t.Fatal("guard has no pairing token")
	}
	g := New(nil, nil, func(fn func() error) error { return fn() }, "local-gateway-token-0123456789")
	r := httptest.NewRequest("GET", "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.RemoteAddr = "127.0.0.1:1"
	if g.authorized(r) {
		t.Fatal("pairing token opened the gateway")
	}
	_ = context.Background
}
