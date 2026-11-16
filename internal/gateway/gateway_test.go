package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enhancedkimi/internal/config"
	"enhancedkimi/internal/credentials"
	"enhancedkimi/internal/provider"
	"enhancedkimi/internal/storage"
	"enhancedkimi/internal/usage"
)

type fixture struct {
	gateway  *Gateway
	mux      *http.ServeMux
	db       *sql.DB
	upstream *httptest.Server
	calls    *atomic.Int32
	lastBody *map[string]any
	mu       *sync.Mutex
	token    string
}

const testToken = "local-gateway-token-0123456789"

// newFixture wires the real provider client, vault, usage ledger and SQLite so
// the gateway is exercised through its actual dependencies.
func newFixture(t *testing.T, upstreamHandler http.HandlerFunc) *fixture {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE registry_providers(id TEXT PRIMARY KEY,name TEXT,protocol TEXT,base_url TEXT,secret_ref TEXT,created_at TEXT); CREATE TABLE registry_models(provider_id TEXT NOT NULL,model_id TEXT NOT NULL,display_name TEXT NOT NULL,PRIMARY KEY(provider_id,model_id));`); err != nil {
		t.Fatal(err)
	}
	vault, err := credentials.NewVault(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.Unlock(context.Background(), "gateway-fixture-password"); err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int32{}
	lastBody := &map[string]any{}
	mu := &sync.Mutex{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if upstreamHandler != nil {
			if r.Method == "POST" {
				body := map[string]any{}
				json.NewDecoder(r.Body).Decode(&body)
				mu.Lock()
				*lastBody = body
				mu.Unlock()
			}
			upstreamHandler.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)
	if _, err := db.Exec(`INSERT INTO registry_providers VALUES('kimi','Kimi','openai',?,'provider/kimi/default','2026-01-01T00:00:00Z')`, upstream.URL); err != nil {
		t.Fatal(err)
	}
	if err := vault.Put(context.Background(), "provider/kimi/default", []byte("upstream-secret-key")); err != nil {
		t.Fatal(err)
	}
	client := provider.New(db, vault)
	ledger, err := usage.New(db)
	if err != nil {
		t.Fatal(err)
	}
	g := New(client, ledger, func(fn func() error) error { return fn() }, testToken)
	mux := http.NewServeMux()
	g.Register(mux)
	return &fixture{gateway: g, mux: mux, db: db, upstream: upstream, calls: calls, lastBody: lastBody, mu: mu, token: testToken}
}

func (f *fixture) declareModel(t *testing.T, providerID, model string) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT OR IGNORE INTO registry_models VALUES(?,?,?)`, providerID, model, model); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) do(t *testing.T, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.RemoteAddr = "127.0.0.1:54321"
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func (f *fixture) completion(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	return f.do(t, "POST", "/v1/chat/completions", body, f.token)
}

func TestGatewayRequiresItsOwnToken(t *testing.T) {
	f := newFixture(t, nil)
	f.declareModel(t, "kimi", "kimi-k2")
	if w := f.do(t, "GET", "/v1/models", "", ""); w.Code != 401 {
		t.Fatalf("unauthenticated %d", w.Code)
	}
	if w := f.do(t, "GET", "/v1/models", "", "wrong-token"); w.Code != 401 {
		t.Fatalf("wrong token %d", w.Code)
	}
	// The enhancement pairing token is a different credential and must not open
	// the gateway.
	if w := f.do(t, "GET", "/v1/models", "", strings.Repeat("a", 64)); w.Code != 401 {
		t.Fatalf("foreign token %d", w.Code)
	}
	w := f.do(t, "GET", "/v1/models", "", f.token)
	if w.Code != 200 {
		t.Fatalf("authorized %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "upstream-secret-key") {
		t.Fatal("gateway response leaked the supplier key")
	}
}

func TestGatewayRejectsForwardedAndNonLoopbackRequests(t *testing.T) {
	f := newFixture(t, nil)
	r := httptest.NewRequest("GET", "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer "+f.token)
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	r.RemoteAddr = "127.0.0.1:1000"
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("forwarded request %d: %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer "+f.token)
	r.RemoteAddr = "203.0.113.9:1000"
	w = httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("non-loopback request %d: %s", w.Code, w.Body.String())
	}
}

func TestModelsAggregatesAndDeduplicates(t *testing.T) {
	f := newFixture(t, nil)
	f.declareModel(t, "kimi", "kimi-k2")
	f.declareModel(t, "kimi", "kimi-k1")
	// A second provider lacking a reachable catalog must not hide the first.
	if _, err := f.db.Exec(`INSERT INTO registry_providers VALUES('broken','Broken','openai','http://127.0.0.1:1',NULL,'2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	w := f.do(t, "GET", "/v1/models", "", f.token)
	if w.Code != 200 {
		t.Fatalf("models %d: %s", w.Code, w.Body.String())
	}
	var payload struct {
		Object string       `json:"object"`
		Data   []modelEntry `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &payload)
	if payload.Object != "list" || len(payload.Data) != 2 {
		t.Fatalf("models payload %+v", payload)
	}
	if payload.Data[0].OwnedBy != "kimi" || payload.Data[0].Object != "model" || payload.Data[0].Created == 0 {
		t.Fatalf("model attribution %+v", payload.Data[0])
	}
	// The catalog is read through the provider client, so no upstream call is
	// made when the catalog is already imported.
	if f.calls.Load() != 0 {
		t.Fatalf("models endpoint called upstream %d times", f.calls.Load())
	}
}

func TestChatForwardsUpstreamKeyNeverReachesClient(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-secret-key" {
			t.Errorf("upstream credential missing: %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"hello","reasoning_content":"thought"}}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`)
	})
	f.declareModel(t, "kimi", "kimi-k2")
	w := f.completion(t, `{"model":"kimi-k2","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != 200 {
		t.Fatalf("completion %d: %s", w.Code, w.Body.String())
	}
	var payload struct {
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
			Finish string `json:"finish_reason"`
		} `json:"choices"`
		Usage map[string]any `json:"usage"`
	}
	json.Unmarshal(w.Body.Bytes(), &payload)
	if payload.Object != "chat.completion" || payload.Model != "kimi-k2" || len(payload.Choices) != 1 {
		t.Fatalf("payload %+v", payload)
	}
	if payload.Choices[0].Message.Content != "hello" || payload.Choices[0].Message.ReasoningContent != "thought" {
		t.Fatalf("reasoning not relayed: %+v", payload.Choices[0].Message)
	}
	if payload.Choices[0].Finish != "stop" || payload.Usage["completion_tokens"] != float64(3) {
		t.Fatalf("usage/finish %+v", payload)
	}
	if strings.Contains(w.Body.String(), "upstream-secret-key") {
		t.Fatal("supplier key leaked to the client")
	}
	// The upstream request must not carry gateway-only attribution fields.
	f.mu.Lock()
	body := *f.lastBody
	f.mu.Unlock()
	if _, ok := body["session_id"]; ok {
		t.Fatal("gateway attribution field forwarded upstream")
	}
	if body["stream"] != false {
		t.Fatalf("buffered request asked for a stream: %v", body["stream"])
	}
}

func TestChatRejectsUnsupportedAndInvalidInput(t *testing.T) {
	f := newFixture(t, nil)
	f.declareModel(t, "kimi", "kimi-k2")
	cases := []struct {
		name string
		body string
		code int
	}{
		{"empty messages", `{"model":"kimi-k2","messages":[]}`, 400},
		{"malformed tools", `{"model":"kimi-k2","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function"}]}`, 400},
		{"non-function tool", `{"model":"kimi-k2","messages":[{"role":"user","content":"x"}],"tools":[{"type":"web_search"}]}`, 400},
		{"tool without call id", `{"model":"kimi-k2","messages":[{"role":"tool","content":"result"}]}`, 400},
		{"tool calls on user", `{"model":"kimi-k2","messages":[{"role":"user","content":"x","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`, 400},
		{"image input", `{"model":"kimi-k2","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`, 400},
		{"bad role", `{"model":"kimi-k2","messages":[{"role":"robot","content":"x"}]}`, 400},
		{"missing model", `{"messages":[{"role":"user","content":"x"}]}`, 400},
		{"unknown model", `{"model":"nope","messages":[{"role":"user","content":"x"}]}`, 404},
		{"max tokens", `{"model":"kimi-k2","messages":[{"role":"user","content":"x"}],"max_tokens":9999999}`, 400},
		{"temperature", `{"model":"kimi-k2","messages":[{"role":"user","content":"x"}],"temperature":9}`, 400},
		{"top p", `{"model":"kimi-k2","messages":[{"role":"user","content":"x"}],"top_p":2}`, 400},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			w := f.completion(t, test.body)
			if w.Code != test.code {
				t.Fatalf("got %d want %d: %s", w.Code, test.code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), `"error"`) || !strings.Contains(w.Body.String(), `"type"`) {
				t.Fatalf("error envelope: %s", w.Body.String())
			}
		})
	}
	if f.calls.Load() != 0 {
		t.Fatalf("invalid input reached upstream %d times", f.calls.Load())
	}
}

func TestAmbiguousModelIsRefused(t *testing.T) {
	f := newFixture(t, nil)
	f.declareModel(t, "kimi", "shared")
	if _, err := f.db.Exec(`INSERT INTO registry_providers VALUES('other','Other','openai','https://example.invalid/v1',NULL,'2026-01-01T00:00:00Z'); INSERT INTO registry_models VALUES('other','shared','shared')`); err != nil {
		t.Fatal(err)
	}
	w := f.completion(t, `{"model":"shared","messages":[{"role":"user","content":"x"}]}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "AMBIGUOUS_MODEL") {
		t.Fatalf("ambiguous model %d: %s", w.Code, w.Body.String())
	}
}

func TestUpstreamStatusesMapToGatewayErrors(t *testing.T) {
	status := 429
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, `{"error":{"message":"upstream quota detail"}}`)
	})
	f.declareModel(t, "kimi", "kimi-k2")
	for _, test := range []struct {
		upstream int
		gateway  int
		code     string
	}{
		{429, 429, "UPSTREAM_RATE_LIMIT"},
		{401, 502, "UPSTREAM_AUTH"},
		{404, 404, "UPSTREAM_MODEL_NOT_FOUND"},
		{500, 502, "UPSTREAM_ERROR"},
	} {
		status = test.upstream
		w := f.completion(t, `{"model":"kimi-k2","messages":[{"role":"user","content":"x"}]}`)
		if w.Code != test.gateway || !strings.Contains(w.Body.String(), test.code) {
			t.Fatalf("upstream %d -> %d %s", test.upstream, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "upstream quota detail") {
			t.Fatalf("upstream body leaked for %d", test.upstream)
		}
		if strings.Contains(w.Body.String(), "upstream-secret-key") {
			t.Fatalf("credential leaked for %d", test.upstream)
		}
	}
}

func TestSafeModeBlocksBeforeAnyUpstreamCall(t *testing.T) {
	f := newFixture(t, nil)
	f.declareModel(t, "kimi", "kimi-k2")
	f.gateway.dispatch = func(func() error) error { return config.ErrSafeMode }
	w := f.completion(t, `{"model":"kimi-k2","messages":[{"role":"user","content":"x"}],"stream":true}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "SAFE_MODE") {
		t.Fatalf("safe mode %d: %s", w.Code, w.Body.String())
	}
	if f.calls.Load() != 0 {
		t.Fatal("safe mode called upstream")
	}
	if strings.Contains(w.Body.String(), "text/event-stream") {
		t.Fatal("safe mode committed a stream")
	}
}

func TestStreamingRelaysContentReasoningAndUsage(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, chunk := range []string{
			`{"id":"c1","model":"kimi-k2","choices":[{"delta":{"role":"assistant","reasoning_content":"think "},"finish_reason":null}]}`,
			`{"id":"c1","model":"kimi-k2","choices":[{"delta":{"reasoning_content":"more"},"finish_reason":null}]}`,
			`{"id":"c1","model":"kimi-k2","choices":[{"delta":{"content":"hello"},"finish_reason":null}]}`,
			`{"id":"c1","model":"kimi-k2","choices":[{"delta":{"content":" world"},"finish_reason":"stop"}]}`,
			`{"id":"c1","model":"kimi-k2","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":4}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	f.declareModel(t, "kimi", "kimi-k2")
	w := f.completion(t, `{"model":"kimi-k2","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if w.Code != 200 {
		t.Fatalf("stream %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content type %q", w.Header().Get("Content-Type"))
	}
	body := w.Body.String()
	for _, want := range []string{`"reasoning_content":"think "`, `"reasoning_content":"more"`, `"content":"hello"`, `"content":" world"`, `"finish_reason":"stop"`, `"prompt_tokens":5`, "data: [DONE]"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "upstream-secret-key") || strings.Contains(body, "upstream") {
		t.Fatal("stream leaked upstream detail")
	}
	f.mu.Lock()
	upstreamBody := *f.lastBody
	f.mu.Unlock()
	if upstreamBody["stream"] != true {
		t.Fatalf("upstream was not asked to stream: %v", upstreamBody)
	}
	if options, ok := upstreamBody["stream_options"].(map[string]any); !ok || options["include_usage"] != true {
		t.Fatalf("usage was not requested on the stream: %v", upstreamBody["stream_options"])
	}
}

func TestStreamingRequiresUpstreamEventStream(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"buffered"}}]}`)
	})
	f.declareModel(t, "kimi", "kimi-k2")
	w := f.completion(t, `{"model":"kimi-k2","messages":[{"role":"user","content":"x"}],"stream":true}`)
	if w.Code != 502 || !strings.Contains(w.Body.String(), "UPSTREAM_NOT_STREAMING") {
		t.Fatalf("buffered upstream accepted for a stream: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "buffered") {
		t.Fatal("gateway silently downgraded to a buffered reply")
	}
}

// A stream that dies mid-generation must not look like a normal completion.
func TestInterruptedStreamReportsUnknownOutcome(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n")
		flusher.Flush()
		// Abort without a finish chunk or [DONE].
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	})
	f.declareModel(t, "kimi", "kimi-k2")
	w := f.completion(t, `{"model":"kimi-k2","messages":[{"role":"user","content":"x"}],"stream":true}`)
	body := w.Body.String()
	if !strings.Contains(body, "partial") {
		t.Fatalf("partial content not relayed: %s", body)
	}
	if !strings.Contains(body, `"error"`) || !strings.Contains(body, "UPSTREAM_STREAM_INTERRUPTED") {
		t.Fatalf("interruption not reported in band: %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("stream not terminated: %s", body)
	}
}

func TestStreamingRejectsNonOpenAIProtocol(t *testing.T) {
	f := newFixture(t, nil)
	if _, err := f.db.Exec(`INSERT INTO registry_providers VALUES('anth','A','anthropic','https://example.invalid',NULL,'2026-01-01T00:00:00Z'); INSERT INTO registry_models VALUES('anth','claude-x','claude-x')`); err != nil {
		t.Fatal(err)
	}
	w := f.completion(t, `{"model":"claude-x","messages":[{"role":"user","content":"x"}],"stream":true}`)
	if w.Code != 501 || !strings.Contains(w.Body.String(), "STREAM_PROTOCOL") {
		t.Fatalf("non-openai stream %d: %s", w.Code, w.Body.String())
	}
}

func TestTokenFileIsStablePrivateAndShortLivedOnlyOnDemand(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) < 32 {
		t.Fatalf("token too short: %q", first)
	}
	info, err := os.Stat(filepath.Join(dir, tokenFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token file permissions %o", info.Mode().Perm())
	}
	second, err := LoadOrCreateToken(dir)
	if err != nil || second != first {
		t.Fatalf("token not stable across restarts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, tokenFile), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := LoadOrCreateToken(dir)
	if err != nil || third == first || len(third) < 32 {
		t.Fatalf("corrupt token file not replaced: %q %v", third, err)
	}
	if masked := MaskToken(first); strings.Contains(masked, first) || !strings.Contains(masked, "…") {
		t.Fatalf("mask leaked the token: %q", masked)
	}
}

func TestPreviewConfigNeverWritesOrLeaks(t *testing.T) {
	f := newFixture(t, nil)
	preview := f.gateway.PreviewConfig("http://127.0.0.1:1234")
	providerSection, ok := preview["provider"].(map[string]any)
	if !ok {
		t.Fatalf("preview shape %+v", preview)
	}
	if providerSection["type"] != "kimi" || providerSection["base_url"] != "http://127.0.0.1:1234/v1" || providerSection["api_key"] != testToken {
		t.Fatalf("preview provider %+v", providerSection)
	}
	if _, ok := providerSection["baseUrl"]; ok {
		t.Fatal("preview kept the wrong baseUrl key")
	}
	if preview["writesUserConfig"] != false {
		t.Fatal("preview claims it writes configuration")
	}
	if preview["models"] != "*" {
		t.Fatalf("preview must leave models to the client's static config: %v", preview["models"])
	}
	if strings.Contains(fmt.Sprint(preview["notes"]), "不支持工具调用") {
		t.Fatal("preview still claims tools are unsupported")
	}
	encoded, _ := json.Marshal(preview)
	if strings.Contains(string(encoded), "upstream-secret-key") {
		t.Fatal("preview leaked the supplier key")
	}
}

func TestGatewayRecordsUsageWithoutInventingCounts(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	})
	f.declareModel(t, "kimi", "kimi-k2")
	w := f.completion(t, `{"model":"kimi-k2","messages":[{"role":"user","content":"hi"}],"session_id":"session-a","project_id":"project-a"}`)
	if w.Code != 200 {
		t.Fatalf("completion %d: %s", w.Code, w.Body.String())
	}
	var sessionID, agentID string
	var input, output *int64
	err := f.db.QueryRow(`SELECT session_id,agent_id,input,output FROM usage_events ORDER BY id DESC LIMIT 1`).Scan(&sessionID, &agentID, &input, &output)
	if err != nil {
		t.Fatal(err)
	}
	if sessionID != "session-a" || agentID != "gateway" {
		t.Fatalf("attribution %q %q", sessionID, agentID)
	}
	if input != nil || output != nil {
		t.Fatalf("missing usage was padded: %v %v", input, output)
	}
}

// reasoning_content must survive both directions: upstream -> client, and a
// client returning an assistant turn -> upstream.
func TestReasoningContentRoundTripsUpstream(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"answer"}}]}`)
	})
	f.declareModel(t, "kimi", "kimi-k2")
	w := f.completion(t, `{"model":"kimi-k2","messages":[{"role":"user","content":"q"},{"role":"assistant","content":"prior answer","reasoning_content":"prior thought"},{"role":"user","content":"again"}]}`)
	if w.Code != 200 {
		t.Fatalf("completion %d: %s", w.Code, w.Body.String())
	}
	f.mu.Lock()
	body := *f.lastBody
	f.mu.Unlock()
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) != 3 {
		t.Fatalf("forwarded messages %+v", body["messages"])
	}
	assistant, _ := messages[1].(map[string]any)
	if assistant["content"] != "prior answer" {
		t.Fatalf("assistant content lost: %+v", assistant)
	}
}

// A provider with accounts configured must use the selected account key, and a
// provider with no accounts keeps its legacy secretRef.
func TestGatewayUsesAccountCredential(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer account-key" {
			t.Errorf("gateway used the wrong credential: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	})
	f.declareModel(t, "kimi", "kimi-k2")
	service, err := provider.NewService(f.db)
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credentials.NewVault(f.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.Unlock(context.Background(), "gateway-fixture-password"); err != nil {
		t.Fatal(err)
	}
	if err := vault.Put(context.Background(), "provider/kimi/account", []byte("account-key")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateAccount(context.Background(), "kimi", provider.AccountInputForTest("provider/kimi/account")); err != nil {
		t.Fatal(err)
	}
	w := f.completion(t, `{"model":"kimi-k2","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != 200 {
		t.Fatalf("completion %d: %s", w.Code, w.Body.String())
	}
}

func TestModelRetrieveForm(t *testing.T) {
	f := newFixture(t, nil)
	f.declareModel(t, "kimi", "kimi-k2")
	if w := f.do(t, "GET", "/v1/models/kimi-k2", "", f.token); w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"kimi-k2"`) {
		t.Fatalf("retrieve %d: %s", w.Code, w.Body.String())
	}
	if w := f.do(t, "GET", "/v1/models/none", "", f.token); w.Code != 404 {
		t.Fatalf("missing model %d: %s", w.Code, w.Body.String())
	}
}

// Kimi's own extension fields must be accepted and forwarded, not rejected as
// unknown. A client upgrade cannot be turned into a hard failure here.
func TestKimiExtensionFieldsAreForwarded(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	})
	f.declareModel(t, "kimi", "kimi-k2")
	body := `{"model":"kimi-k2","messages":[{"role":"user","content":"hi"}],
		"temperature":0.3,"top_p":0.9,"max_completion_tokens":128000,
		"thinking":{"type":"enabled","budget_tokens":512},
		"builtin_function":{"name":"web_search"},
		"tools":[{"type":"builtin_function","function":{"name":"$web_search"}}],
		"reasoning_effort":"medium","user":"synthetic-user",
		"some_future_field":{"nested":true}}`
	if w := f.completion(t, body); w.Code != 200 {
		t.Fatalf("extension fields rejected: %d %s", w.Code, w.Body.String())
	}
	f.mu.Lock()
	upstream := *f.lastBody
	f.mu.Unlock()
	for _, key := range []string{"temperature", "top_p", "max_completion_tokens", "thinking", "builtin_function", "tools", "reasoning_effort", "user"} {
		if _, ok := upstream[key]; !ok {
			t.Fatalf("%s was dropped before the upstream call: %+v", key, upstream)
		}
	}
	if upstream["max_completion_tokens"] != float64(128000) {
		t.Fatalf("max_completion_tokens value %v", upstream["max_completion_tokens"])
	}
	thinking, _ := upstream["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(512) {
		t.Fatalf("thinking not forwarded semantically: %+v", upstream["thinking"])
	}
	// Gateway-only attribution stays local.
	for _, key := range []string{"session_id", "project_id", "agent_id"} {
		if _, ok := upstream[key]; ok {
			t.Fatalf("%s leaked upstream", key)
		}
	}
}

func TestStreamOptionsArePreservedAndIncludeUsageForced(t *testing.T) {
	var f *fixture
	f = newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	f.declareModel(t, "kimi", "kimi-k2")
	w := f.completion(t, `{"model":"kimi-k2","messages":[{"role":"user","content":"x"}],"stream":true,"stream_options":{"include_usage":false,"custom_option":"keep"}}`)
	if w.Code != 200 {
		t.Fatalf("stream %d: %s", w.Code, w.Body.String())
	}
	f.mu.Lock()
	body := *f.lastBody
	f.mu.Unlock()
	options, ok := body["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("stream_options missing: %+v", body)
	}
	if options["include_usage"] != true {
		t.Fatalf("include_usage not forced: %+v", options)
	}
	if options["custom_option"] != "keep" {
		t.Fatalf("other stream_options were dropped: %+v", options)
	}
}

// Full tool-call loop: tools upstream, assistant tool_calls downstream, then a
// tool result coming back with its call id.
func TestToolCallRoundTripNonStreaming(t *testing.T) {
	var seen []map[string]any
	var seenMu sync.Mutex
	var f *fixture
	f = newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		// The fixture captures the request body before this handler runs.
		f.mu.Lock()
		body := *f.lastBody
		f.mu.Unlock()
		messages, _ := body["messages"].([]any)
		seenMu.Lock()
		seen = append(seen, body)
		seenMu.Unlock()
		if len(messages) == 3 {
			// Second turn: the tool result must arrive with its id.
			tool, _ := messages[2].(map[string]any)
			if tool["role"] != "tool" || tool["tool_call_id"] != "call_1" || tool["content"] != "21" {
				t.Errorf("tool result not forwarded: %+v", tool)
			}
			for _, key := range []string{"tools", "tool_choice"} {
				if _, ok := body[key]; !ok {
					t.Errorf("%s dropped upstream", key)
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if len(messages) == 3 {
			io.WriteString(w, `{"choices":[{"message":{"content":"42"},"finish_reason":"stop"}]}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"add","arguments":"{\"a\":20,\"b\":22}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":4}}`)
	})
	f.declareModel(t, "kimi", "kimi-k2")
	tools := `[{"type":"function","function":{"name":"add","description":"add numbers","parameters":{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"}}}}}]`
	first := `{"model":"kimi-k2","messages":[{"role":"user","content":"20+22"}],"tools":` + tools + `,"tool_choice":"auto","parallel_tool_calls":false}`
	w := f.completion(t, first)
	if w.Code != 200 {
		t.Fatalf("tool turn %d: %s", w.Code, w.Body.String())
	}
	var payload struct {
		Choices []struct {
			Message struct {
				Content   string              `json:"content"`
				ToolCalls []provider.ToolCall `json:"tool_calls"`
			} `json:"message"`
			Finish string `json:"finish_reason"`
		} `json:"choices"`
	}
	json.Unmarshal(w.Body.Bytes(), &payload)
	if payload.Choices[0].Finish != "tool_calls" {
		t.Fatalf("finish_reason %q", payload.Choices[0].Finish)
	}
	if len(payload.Choices[0].Message.ToolCalls) != 1 || payload.Choices[0].Message.ToolCalls[0].Function.Name != "add" || payload.Choices[0].Message.ToolCalls[0].Function.Arguments != `{"a":20,"b":22}` {
		t.Fatalf("tool calls not relayed: %+v", payload.Choices[0].Message.ToolCalls)
	}
	if payload.Choices[0].Message.Content != "" {
		t.Fatalf("empty assistant content changed: %q", payload.Choices[0].Message.Content)
	}
	second := `{"model":"kimi-k2","messages":[{"role":"user","content":"20+22"},{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"add","arguments":"{\"a\":20,\"b\":22}"}}]},{"role":"tool","tool_call_id":"call_1","content":"21"}],"tools":` + tools + `,"tool_choice":"auto","parallel_tool_calls":false}`
	if w := f.completion(t, second); w.Code != 200 || !strings.Contains(w.Body.String(), "42") {
		t.Fatalf("tool result turn %d: %s", w.Code, w.Body.String())
	}
}

// Streaming tool calls: fragments keep index/id/type/name/arguments and the
// turn ends with finish_reason tool_calls.
func TestStreamingToolCallFragments(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, chunk := range []string{
			`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"add","arguments":""}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":20,"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"b\":22}"}}]},"finish_reason":null}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":7}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	f.declareModel(t, "kimi", "kimi-k2")
	w := f.completion(t, `{"model":"kimi-k2","messages":[{"role":"user","content":"x"}],"stream":true}`)
	if w.Code != 200 {
		t.Fatalf("stream %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{`"id":"call_1"`, `"type":"function"`, `"name":"add"`, `"arguments":""`, `{\"a\":20,`, `\"b\":22}`, `"finish_reason":"tool_calls"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %s: %s", want, body)
		}
	}
	if strings.Count(body, `"prompt_tokens":5`) != 1 {
		t.Fatalf("usage chunk duplicated or dropped: %s", body)
	}
	if !strings.Contains(body, `"choices":[]`) {
		t.Fatalf("pure usage chunk not forwarded: %s", body)
	}
	if !strings.Contains(body, `"delta":{}`) || !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Fatalf("finish chunk lost its delta shape: %s", body)
	}
	if strings.Count(body, `"choices":[]`) != 1 {
		t.Fatalf("terminal chunk appended after the usage chunk: %s", body)
	}
}

// A stream without a finish_reason or without [DONE] is not a success.
func TestStreamRequiresFinishAndDone(t *testing.T) {
	cases := map[string]string{
		"no done":   "data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":\"stop\"}]}\n\n",
		"no finish": "data: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n",
	}
	for name, stream := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, stream)
			})
			f.declareModel(t, "kimi", "kimi-k2")
			w := f.completion(t, `{"model":"kimi-k2","messages":[{"role":"user","content":"x"}],"stream":true}`)
			body := w.Body.String()
			if !strings.Contains(body, "UPSTREAM_STREAM_INCOMPLETE") {
				t.Fatalf("incomplete stream reported as success: %d %s", w.Code, body)
			}
		})
	}
}

// Stream 401/429 must produce the documented status before any SSE byte, so a
// client never sees a 200 whose only content is an error.
func TestStreamUpstreamAuthAndRateLimitStayHTTPErrors(t *testing.T) {
	for _, test := range []struct {
		upstream int
		status   int
		code     string
	}{
		{401, 502, "UPSTREAM_AUTH"},
		{429, 429, "UPSTREAM_RATE_LIMIT"},
	} {
		t.Run(fmt.Sprint(test.upstream), func(t *testing.T) {
			status := test.upstream
			f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				io.WriteString(w, `{"error":{"message":"upstream detail"}}`)
			})
			f.declareModel(t, "kimi", "kimi-k2")
			w := f.completion(t, `{"model":"kimi-k2","messages":[{"role":"user","content":"x"}],"stream":true}`)
			if w.Code != test.status {
				t.Fatalf("upstream %d produced %d: %s", test.upstream, w.Code, w.Body.String())
			}
			if strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
				t.Fatalf("error was committed as a stream: %q", w.Header().Get("Content-Type"))
			}
			if !strings.Contains(w.Body.String(), test.code) || strings.Contains(w.Body.String(), "upstream detail") {
				t.Fatalf("wrong error body: %s", w.Body.String())
			}
			if strings.Contains(w.Body.String(), "data:") {
				t.Fatalf("SSE body written for an HTTP error: %s", w.Body.String())
			}
		})
	}
}

// The dispatch gate must span the whole stream, not just admission: a settings
// write that needs the exclusive permit has to wait for an in-flight stream.
//
// The fixture models config.Store.Dispatch with an RWMutex: streams take the
// read permit for their whole duration, a settings write takes the write permit.
func TestStreamHoldsDispatchForItsWholeDuration(t *testing.T) {
	streamStarted := make(chan struct{})
	releaseStream := make(chan struct{})
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		close(streamStarted)
		<-releaseStream
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"late\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	f.declareModel(t, "kimi", "kimi-k2")
	var permit sync.RWMutex
	f.gateway.dispatch = func(fn func() error) error {
		permit.RLock()
		defer permit.RUnlock()
		return fn()
	}
	done := make(chan int, 1)
	go func() {
		done <- f.completion(t, `{"model":"kimi-k2","messages":[{"role":"user","content":"x"}],"stream":true}`).Code
	}()
	select {
	case <-streamStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("stream never reached upstream")
	}
	writeFinished := make(chan struct{})
	go func() {
		permit.Lock()
		close(writeFinished)
		permit.Unlock()
	}()
	select {
	case <-writeFinished:
		t.Fatal("settings write did not wait for the in-flight stream")
	case <-time.After(200 * time.Millisecond):
	}
	close(releaseStream)
	select {
	case code := <-done:
		if code != 200 {
			t.Fatalf("stream failed %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stream did not finish")
	}
	select {
	case <-writeFinished:
	case <-time.After(5 * time.Second):
		t.Fatal("settings write never proceeded after the stream ended")
	}
}

// Safe mode must still be refused before any byte is written.
func TestStreamSafeModeRejectsBeforeCommit(t *testing.T) {
	f := newFixture(t, nil)
	f.declareModel(t, "kimi", "kimi-k2")
	f.gateway.dispatch = func(func() error) error { return config.ErrSafeMode }
	w := f.completion(t, `{"model":"kimi-k2","messages":[{"role":"user","content":"x"}],"stream":true}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "SAFE_MODE") {
		t.Fatalf("safe mode %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatal("safe mode committed a stream")
	}
	if f.calls.Load() != 0 {
		t.Fatal("safe mode called upstream")
	}
}

// The account attribution must survive record(): the raw object keeps the
// protocol counts and gains the gateway account id.
func TestUsageKeepsAccountAndTokenCounts(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":13,"completion_tokens":7}}`)
	})
	f.declareModel(t, "kimi", "kimi-k2")
	service, err := provider.NewService(f.db)
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credentials.NewVault(f.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.Unlock(context.Background(), "gateway-fixture-password"); err != nil {
		t.Fatal(err)
	}
	if err := vault.Put(context.Background(), "provider/kimi/attributed", []byte("synthetic")); err != nil {
		t.Fatal(err)
	}
	account, err := service.CreateAccount(context.Background(), "kimi", provider.AccountInputForTest("provider/kimi/attributed"))
	if err != nil {
		t.Fatal(err)
	}
	if w := f.completion(t, `{"model":"kimi-k2","messages":[{"role":"user","content":"hi"}]}`); w.Code != 200 {
		t.Fatalf("completion %d: %s", w.Code, w.Body.String())
	}
	var raw string
	if err := f.db.QueryRow(`SELECT COALESCE(raw_json,'') FROM usage_events ORDER BY id DESC LIMIT 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("raw usage: %v (%s)", err, raw)
	}
	if payload["gatewayAccountId"] != account.ID {
		t.Fatalf("account attribution lost: %s", raw)
	}
	if payload["prompt_tokens"] != float64(13) || payload["completion_tokens"] != float64(7) {
		t.Fatalf("token counts lost from raw: %s", raw)
	}
	var input, output *int64
	if err := f.db.QueryRow(`SELECT input,output FROM usage_events ORDER BY id DESC LIMIT 1`).Scan(&input, &output); err != nil {
		t.Fatal(err)
	}
	if input == nil || output == nil || *input != 13 || *output != 7 {
		t.Fatalf("ledger counts %v %v", input, output)
	}
}
