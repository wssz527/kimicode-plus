package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"enhancedkimi/internal/credentials"
	"enhancedkimi/internal/provider"
	"enhancedkimi/internal/storage"
	"enhancedkimi/internal/usage"
)

type runtimeFixture struct {
	service *Service
	mux     *http.ServeMux
	calls   *atomic.Int32
	vault   *credentials.Vault
	release chan struct{}
}

// newRuntimeFixture wires a real SQLite file, the chat module, the encrypted
// vault and a synthetic upstream, so the request lifecycle is exercised end to
// end instead of through a stubbed runtime.
func newRuntimeFixture(t *testing.T, respond func(w http.ResponseWriter)) *runtimeFixture {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = db.Exec(`CREATE TABLE registry_providers(id TEXT PRIMARY KEY,protocol TEXT,base_url TEXT,secret_ref TEXT,created_at TEXT); INSERT INTO registry_providers VALUES('synthetic','openai','http://127.0.0.1',NULL,'2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	vault, err := credentials.NewVault(db)
	if err != nil {
		t.Fatal(err)
	}
	if err = vault.Unlock(context.Background(), "synthetic-vault-password"); err != nil {
		t.Fatal(err)
	}
	ledger, err := usage.New(db)
	if err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		respond(w)
	}))
	t.Cleanup(upstream.Close)
	if _, err = db.Exec(`UPDATE registry_providers SET base_url=? WHERE id='synthetic'`, upstream.URL); err != nil {
		t.Fatal(err)
	}
	client := provider.New(db, vault)
	service, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	service.UseProvider(client, ledger, func(fn func() error) error { return fn() })
	mux := http.NewServeMux()
	service.Register(mux)
	return &runtimeFixture{service: service, mux: mux, calls: calls, vault: vault}
}

func (f *runtimeFixture) call(t *testing.T, method, path, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/enhance/v1"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}

func (f *runtimeFixture) thread(t *testing.T) string {
	t.Helper()
	w := f.call(t, "POST", "/chat/threads", `{"title":"t","sourceId":"custom-model","model":"synthetic","providerId":"synthetic"}`, "")
	if w.Code != 201 {
		t.Fatalf("create thread %d: %s", w.Code, w.Body.String())
	}
	var thread Thread
	json.Unmarshal(w.Body.Bytes(), &thread)
	return thread.ID
}

func readThread(t *testing.T, w *httptest.ResponseRecorder) Thread {
	t.Helper()
	var thread Thread
	if err := json.Unmarshal(w.Body.Bytes(), &thread); err != nil {
		t.Fatalf("decode thread: %v (%s)", err, w.Body.String())
	}
	return thread
}

const unknownReply = `{}`

func TestUnknownRequestBlocksUntilExplicitlyResolved(t *testing.T) {
	f := newRuntimeFixture(t, func(w http.ResponseWriter) { w.Write([]byte(unknownReply)) })
	id := f.thread(t)
	path := "/chat/threads/" + id + "/messages"
	if w := f.call(t, "POST", path, `{"text":"first"}`, "intent-a"); w.Code != 200 {
		t.Fatalf("first send %d: %s", w.Code, w.Body.String())
	}
	if f.calls.Load() != 1 {
		t.Fatalf("upstream calls %d", f.calls.Load())
	}
	thread := readThread(t, f.call(t, "GET", "/chat/threads/"+id, "", ""))
	if thread.LastRequest == nil || thread.LastRequest.State != "UNKNOWN" {
		t.Fatalf("state not recorded as UNKNOWN: %+v", thread.LastRequest)
	}
	if w := f.call(t, "POST", path, `{"text":"second"}`, "intent-b"); w.Code != 409 {
		t.Fatalf("unresolved UNKNOWN allowed a new send: %d %s", w.Code, w.Body.String())
	}
	if w := f.call(t, "POST", "/chat/threads/"+id+"/requests/"+thread.LastRequest.ID+"/resolve", `{"resolution":"NOT_FOUND_UPSTREAM"}`, ""); w.Code != 200 {
		t.Fatalf("resolve %d: %s", w.Code, w.Body.String())
	}
	resolved := readThread(t, f.call(t, "GET", "/chat/threads/"+id, "", ""))
	if w := f.call(t, "POST", path, `{"text":"second"}`, "intent-b"); w.Code != 200 {
		t.Fatalf("resolved request still blocked: %d %s", w.Code, w.Body.String())
	}
	if f.calls.Load() != 2 {
		t.Fatalf("unexpected upstream calls %d", f.calls.Load())
	}
	var stored string
	if err := f.service.db.QueryRow(`SELECT state FROM chat_requests WHERE thread_id=? AND intent='intent-a'`, id).Scan(&stored); err != nil || stored != "UNKNOWN" {
		t.Fatalf("audit state rewritten: %q %v", stored, err)
	}
	if resolved.LastRequest.Resolution != "NOT_FOUND_UPSTREAM" || resolved.LastRequest.ResolvedAt == "" {
		t.Fatalf("resolution not visible on the thread: %+v", resolved.LastRequest)
	}
}

func TestResolveIsIdempotentAndRefusesUnsafeStates(t *testing.T) {
	f := newRuntimeFixture(t, func(w http.ResponseWriter) { w.Write([]byte(unknownReply)) })
	id := f.thread(t)
	w := f.call(t, "POST", "/chat/threads/"+id+"/messages", `{"text":"first"}`, "intent-a")
	if w.Code != 200 {
		t.Fatalf("send %d: %s", w.Code, w.Body.String())
	}
	thread := readThread(t, w)
	requestID := thread.LastRequest.ID
	base := "/chat/threads/" + id + "/requests/" + requestID + "/resolve"
	for _, body := range []string{`{"resolution":"GUESSED"}`, `{"resolution":""}`, `{"resolution":"NOT_FOUND_UPSTREAM","note":"` + strings.Repeat("x", 501) + `"}`} {
		if got := f.call(t, "POST", base, body, ""); got.Code != 422 {
			t.Fatalf("invalid resolution accepted (%d): %s", got.Code, got.Body.String())
		}
	}
	if got := f.call(t, "POST", "/chat/threads/"+id+"/requests/missing/resolve", `{"resolution":"NOT_FOUND_UPSTREAM"}`, ""); got.Code != 404 {
		t.Fatalf("unknown request %d", got.Code)
	}
	if got := f.call(t, "POST", "/chat/threads/missing/requests/"+requestID+"/resolve", `{"resolution":"NOT_FOUND_UPSTREAM"}`, ""); got.Code != 404 {
		t.Fatalf("unknown thread %d", got.Code)
	}
	first := f.call(t, "POST", base, `{"resolution":"NOT_FOUND_UPSTREAM","note":"额度面板无记录"}`, "")
	if first.Code != 200 {
		t.Fatalf("resolve %d: %s", first.Code, first.Body.String())
	}
	replay := f.call(t, "POST", base, `{"resolution":"NOT_FOUND_UPSTREAM","note":"额度面板无记录"}`, "")
	if replay.Code != 200 {
		t.Fatalf("idempotent replay %d: %s", replay.Code, replay.Body.String())
	}
	if f.calls.Load() != 1 {
		t.Fatalf("resolution triggered an upstream call: %d", f.calls.Load())
	}
	if got := f.call(t, "POST", base, `{"resolution":"SEEN_UPSTREAM"}`, ""); got.Code != 409 || !strings.Contains(got.Body.String(), "RESOLUTION_CONFLICT") {
		t.Fatalf("conflicting resolution %d: %s", got.Code, got.Body.String())
	}
	list := f.call(t, "GET", "/chat/threads/"+id+"/requests", "", "")
	if list.Code != 200 || !strings.Contains(list.Body.String(), `"resolutionNote":"额度面板无记录"`) || !strings.Contains(list.Body.String(), `"state":"UNKNOWN"`) {
		t.Fatalf("audit history lost the resolution: %d %s", list.Code, list.Body.String())
	}
}

func TestCompletedRequestCannotBeResolved(t *testing.T) {
	f := newRuntimeFixture(t, func(w http.ResponseWriter) {
		w.Write([]byte(`{"choices":[{"message":{"content":"done"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	})
	id := f.thread(t)
	w := f.call(t, "POST", "/chat/threads/"+id+"/messages", `{"text":"hello"}`, "intent-ok")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "COMPLETED") {
		t.Fatalf("send %d: %s", w.Code, w.Body.String())
	}
	thread := readThread(t, w)
	got := f.call(t, "POST", "/chat/threads/"+id+"/requests/"+thread.LastRequest.ID+"/resolve", `{"resolution":"SEEN_UPSTREAM"}`, "")
	if got.Code != 409 || !strings.Contains(got.Body.String(), "REQUEST_TERMINAL") {
		t.Fatalf("terminal request resolved %d: %s", got.Code, got.Body.String())
	}
	if f.calls.Load() != 1 {
		t.Fatalf("upstream calls %d", f.calls.Load())
	}
}

// INFLIGHT is not resolvable: while a provider request is still running a user
// assertion must not open the thread for a second concurrent send.
func TestInflightRequestCannotBeResolved(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	f := newRuntimeFixture(t, func(w http.ResponseWriter) {
		started <- struct{}{}
		<-release
		w.Write([]byte(`{"choices":[{"message":{"content":"late"}}]}`))
	})
	id := f.thread(t)
	path := "/chat/threads/" + id + "/messages"
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- f.call(t, "POST", path, `{"text":"slow"}`, "intent-slow")
	}()
	select {
	case <-started:
	case resp := <-done:
		t.Fatalf("send did not reach upstream: %d %s", resp.Code, resp.Body.String())
	}
	var requestID string
	if err := f.service.db.QueryRow(`SELECT id FROM chat_requests WHERE thread_id=? AND intent='intent-slow'`, id).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	got := f.call(t, "POST", "/chat/threads/"+id+"/requests/"+requestID+"/resolve", `{"resolution":"SEEN_UPSTREAM"}`, "")
	if got.Code != 409 || !strings.Contains(got.Body.String(), "REQUEST_IN_FLIGHT") {
		t.Fatalf("inflight request resolved %d: %s", got.Code, got.Body.String())
	}
	close(release)
	if w := <-done; w.Code != 200 {
		t.Fatalf("in-flight send failed: %d %s", w.Code, w.Body.String())
	}
}

// A request left INFLIGHT by a crash becomes UNKNOWN on restart: it must stay
// blocking, stay unconfirmed, and become resolvable.
func TestRestartTurnsInflightIntoResolvableUnknown(t *testing.T) {
	f := newRuntimeFixture(t, func(w http.ResponseWriter) { w.Write([]byte(unknownReply)) })
	id := f.thread(t)
	if w := f.call(t, "POST", "/chat/threads/"+id+"/messages", `{"text":"first"}`, "intent-a"); w.Code != 200 {
		t.Fatalf("send %d: %s", w.Code, w.Body.String())
	}
	if _, err := f.service.db.Exec(`UPDATE chat_requests SET state='INFLIGHT' WHERE thread_id=?`, id); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(f.service.db)
	if err != nil {
		t.Fatal(err)
	}
	restarted.UseProvider(f.service.runtime.client, f.service.runtime.usage, f.service.runtime.dispatch)
	mux := http.NewServeMux()
	restarted.Register(mux)
	r := httptest.NewRequest("GET", "/enhance/v1/chat/threads/"+id, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	thread := readThread(t, w)
	if thread.LastRequest == nil || thread.LastRequest.State != "UNKNOWN" || thread.LastRequest.Resolution != "" {
		t.Fatalf("restart did not demote INFLIGHT to a fresh UNKNOWN: %+v", thread.LastRequest)
	}
	blocked := httptest.NewRequest("POST", "/enhance/v1/chat/threads/"+id+"/messages", strings.NewReader(`{"text":"second"}`))
	blocked.Header.Set("Content-Type", "application/json")
	bw := httptest.NewRecorder()
	mux.ServeHTTP(bw, blocked)
	if bw.Code != 409 {
		t.Fatalf("restarted UNKNOWN did not block: %d %s", bw.Code, bw.Body.String())
	}
}

// A configuration failure between "request accepted" and "request dispatched"
// must not persist an INFLIGHT row: that row would block every later message on
// the thread until a restart demoted it, for a request the provider never saw.
// The accounts table is broken on purpose to fail before any intent is stored.
func TestUnresolvableProviderLeavesNoPendingRequest(t *testing.T) {
	f := newRuntimeFixture(t, func(w http.ResponseWriter) { w.Write([]byte(unknownReply)) })
	id := f.thread(t)
	if _, err := f.service.db.Exec(`CREATE TABLE provider_accounts(broken TEXT)`); err != nil {
		t.Fatal(err)
	}
	path := "/chat/threads/" + id + "/messages"
	w := f.call(t, "POST", path, `{"text":"first"}`, "intent-a")
	if w.Code != 422 || !strings.Contains(w.Body.String(), "INVALID_PROVIDER") {
		t.Fatalf("unreadable provider config %d: %s", w.Code, w.Body.String())
	}
	var pending int
	if err := f.service.db.QueryRow(`SELECT count(*) FROM chat_requests WHERE thread_id=?`, id).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("%d request rows written for a send that never reached the provider", pending)
	}
	if f.calls.Load() != 0 {
		t.Fatalf("upstream calls %d", f.calls.Load())
	}
	thread := readThread(t, f.call(t, "GET", "/chat/threads/"+id, "", ""))
	if len(thread.Messages) != 0 || thread.LastRequest != nil {
		t.Fatalf("thread recorded a message that was never sent: %+v", thread)
	}
}

func TestLockedAccountLeavesNoPendingRequest(t *testing.T) {
	f := newRuntimeFixture(t, func(w http.ResponseWriter) { w.Write([]byte(unknownReply)) })
	accounts, err := provider.NewService(f.service.db)
	if err != nil {
		t.Fatal(err)
	}
	const ref = "provider/synthetic/account"
	if err := f.vault.Put(context.Background(), ref, []byte("synthetic-account-key")); err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.CreateAccount(context.Background(), "synthetic", provider.AccountInputForTest(ref)); err != nil {
		t.Fatal(err)
	}
	f.vault.Lock()
	id := f.thread(t)
	w := f.call(t, "POST", "/chat/threads/"+id+"/messages", `{"text":"must not dispatch"}`, "intent-locked")
	if w.Code != 423 {
		t.Fatalf("locked account send %d: %s", w.Code, w.Body.String())
	}
	var requests int
	if err := f.service.db.QueryRow(`SELECT count(*) FROM chat_requests WHERE thread_id=?`, id).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if requests != 0 || f.calls.Load() != 0 {
		t.Fatalf("locked account wrote %d requests and made %d upstream calls", requests, f.calls.Load())
	}
	thread := readThread(t, f.call(t, "GET", "/chat/threads/"+id, "", ""))
	if len(thread.Messages) != 0 || thread.LastRequest != nil {
		t.Fatalf("locked account changed thread history: %+v", thread)
	}
}

// Two concurrent assertions of the same resolution are a retry, not a conflict:
// they must both succeed and leave exactly one audit record.
func TestConcurrentResolveOfSameOutcomeIsIdempotent(t *testing.T) {
	f := newRuntimeFixture(t, func(w http.ResponseWriter) { w.Write([]byte(unknownReply)) })
	id := f.thread(t)
	w := f.call(t, "POST", "/chat/threads/"+id+"/messages", `{"text":"first"}`, "intent-a")
	if w.Code != 200 {
		t.Fatalf("send %d: %s", w.Code, w.Body.String())
	}
	requestID := readThread(t, w).LastRequest.ID
	base := "/chat/threads/" + id + "/requests/" + requestID + "/resolve"
	const workers = 4
	codes := make([]int, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = f.call(t, "POST", base, `{"resolution":"NOT_FOUND_UPSTREAM"}`, "").Code
		}(i)
	}
	wg.Wait()
	for i, code := range codes {
		if code != 200 {
			t.Fatalf("concurrent resolve %d returned %d", i, code)
		}
	}
	var resolved int
	if err := f.service.db.QueryRow(`SELECT count(*) FROM chat_requests WHERE thread_id=? AND id=? AND resolution='NOT_FOUND_UPSTREAM' AND resolved_at<>''`, id, requestID).Scan(&resolved); err != nil {
		t.Fatal(err)
	}
	if resolved != 1 {
		t.Fatalf("resolution rows %d", resolved)
	}
}
