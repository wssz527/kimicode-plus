package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"enhancedkimi/internal/storage"
)

// handoffService wires a NativeService to a synthetic official API that records
// the session creation and prompt bodies, so a test can prove what a handoff
// actually asked the official client to do.
type handoffCalls struct {
	mu       sync.Mutex
	created  []map[string]any
	prompts  []map[string]any
	modelsIn string
}

func handoffService(t *testing.T) (*NativeService, *handoffCalls) {
	t.Helper()
	calls := &handoffCalls{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
			body := map[string]any{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			calls.mu.Lock()
			calls.created = append(calls.created, body)
			calls.mu.Unlock()
			writeNativeTestEnvelope(w, map[string]any{"id": "created-1", "title": "Synthetic", "metadata": map[string]any{"cwd": "/tmp"}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/prompts"):
			body := map[string]any{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			calls.mu.Lock()
			calls.prompts = append(calls.prompts, body)
			calls.mu.Unlock()
			writeNativeTestEnvelope(w, map[string]any{})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/config":
			writeNativeTestEnvelope(w, map[string]any{"models": map[string]any{
				"kimi-k2": map[string]any{"display_name": "Kimi K2"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	db, err := storage.Open(filepath.Join(t.TempDir(), "handoff.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	kap := NewKAPClient("")
	kap.base, kap.token = server.URL, "synthetic"
	service, err := NewNativeService(db, kap, t.TempDir(), func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	return service, calls
}

// A handoff that creates a session applies the caller's directory, model and
// title at creation, so the first turn already runs on them.
func TestHandoffCreatesASessionWithTheChosenDirectoryAndModel(t *testing.T) {
	service, calls := handoffService(t)
	dir := t.TempDir()
	payload, err := json.Marshal(map[string]string{"text": "carry on", "cwd": dir, "model": "kimi-k2", "title": "继续实现"})
	if err != nil {
		t.Fatal(err)
	}
	value, err := service.Action(context.Background(), "handoff", payload)
	if err != nil {
		t.Fatalf("handoff: %v", err)
	}
	result := value.(map[string]any)
	if result["sessionId"] != "created-1" || result["created"] != true {
		t.Fatalf("handoff did not report the new session: %#v", result)
	}
	created := calls.created[0]
	metadata, _ := created["metadata"].(map[string]any)
	if metadata["cwd"] != dir {
		t.Fatalf("created session ignored the directory: %#v", created)
	}
	if created["title"] != "继续实现" {
		t.Fatalf("created session ignored the title: %#v", created)
	}
	// The official client reads the model from the prompt, so that is where the
	// chosen model has to be for the first turn to run on it.
	prompt := calls.prompts[0]
	if prompt["model"] != "kimi-k2" || prompt["plan_mode"] != true {
		t.Fatalf("the first turn did not run on the chosen model and mode: %#v", prompt)
	}
}

// Handing off into an existing session keeps its model: the request must not
// carry one, because the official client would switch the conversation.
func TestHandoffToAnExistingSessionNeverCarriesAModel(t *testing.T) {
	service, calls := handoffService(t)
	value, err := service.Action(context.Background(), "handoff", json.RawMessage(`{"targetId":"s1","text":"carry on","cwd":"/tmp","model":"kimi-k2","title":"改标题"}`))
	if err != nil {
		t.Fatalf("handoff: %v", err)
	}
	if created := value.(map[string]any)["created"]; created != false {
		t.Fatalf("an existing target reported a creation: %#v", value)
	}
	if len(calls.created) != 0 {
		t.Fatalf("an existing target created a session: %#v", calls.created)
	}
	prompt := calls.prompts[0]
	for _, field := range []string{"model", "plan_mode", "tower_mode"} {
		if _, present := prompt[field]; present {
			t.Fatalf("existing session sent a %s override: %#v", field, prompt)
		}
	}
	if _, err := service.Action(context.Background(), "handoff", json.RawMessage(`{"targetId":"s1","text":"carry on","mode":"plan"}`)); err != nil || len(calls.prompts) != 2 || calls.prompts[1]["plan_mode"] != true {
		t.Fatalf("explicit Plan mode was not delivered: %v, %#v", err, calls.prompts)
	}
}

// The directory and model are validated only when they would be used: an
// existing target accepts values a new session would refuse.
func TestHandoffValidatesChoicesOnlyForANewSession(t *testing.T) {
	service, _ := handoffService(t)
	cases := []string{
		`{"text":"x","cwd":"relative/dir"}`,
		`{"text":"x","cwd":"/definitely/missing/directory"}`,
		`{"text":"x","model":"not-a-model"}`,
		`{"text":"x","title":"` + strings.Repeat("长", 121) + `"}`,
	}
	for _, payload := range cases {
		if _, err := service.Action(context.Background(), "handoff", json.RawMessage(payload)); err == nil {
			t.Fatalf("a new-session handoff accepted %s", payload)
		}
	}
	// The same invalid values are ignored for an existing target.
	for _, payload := range cases {
		existing := strings.Replace(payload, `{"text":"x"`, `{"targetId":"s1","text":"x"`, 1)
		if _, err := service.Action(context.Background(), "handoff", json.RawMessage(existing)); err != nil {
			t.Fatalf("an existing-target handoff rejected %s: %v", existing, err)
		}
	}
}

// A handoff with no directory keeps the core's own working directory, so a
// plugin need not know it.
func TestHandoffDefaultsToTheCoreDirectory(t *testing.T) {
	service, calls := handoffService(t)
	if _, err := service.Action(context.Background(), "handoff", json.RawMessage(`{"text":"x"}`)); err != nil {
		t.Fatal(err)
	}
	metadata, _ := calls.created[0]["metadata"].(map[string]any)
	if metadata["cwd"] != service.cwd {
		t.Fatalf("handoff cwd = %v, want the core's %v", metadata["cwd"], service.cwd)
	}
	if calls.created[0]["title"] != "规划交接" {
		t.Fatalf("default title changed: %#v", calls.created[0])
	}
	if _, present := calls.created[0]["model"]; present {
		t.Fatalf("a handoff with no model asked for one: %#v", calls.created[0])
	}
}

// The directory a handoff is given is cleaned before it is checked, so a path
// written with `..` cannot be stored as a different directory than the one that
// was validated.
func TestHandoffCleansTheDirectory(t *testing.T) {
	service, calls := handoffService(t)
	dir := t.TempDir()
	handoffFrom := func(suffix string) error {
		payload, err := json.Marshal(map[string]string{"text": "x", "cwd": dir + suffix})
		if err != nil {
			t.Fatal(err)
		}
		_, err = service.Action(t.Context(), "handoff", payload)
		return err
	}
	if err := handoffFrom("/a"); err == nil {
		t.Fatal("a missing directory was accepted")
	}
	if runtime.GOOS != "windows" {
		if err := handoffFrom("/a/.."); err == nil {
			t.Fatal("a directory that only resolves by removing a missing segment was accepted")
		}
	}
	if err := handoffFrom("/."); err != nil {
		t.Fatalf("a cleaned directory was rejected: %v", err)
	}
	metadata, _ := calls.created[0]["metadata"].(map[string]any)
	if metadata["cwd"] != dir {
		t.Fatalf("the stored directory was not cleaned: %v, want %v", metadata["cwd"], dir)
	}
}
