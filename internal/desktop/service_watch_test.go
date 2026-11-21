package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"enhancedkimi/internal/storage"
)

// watchHistoryService wires a NativeService to a synthetic official API that serves a
// session with one recorded turn but refuses no reads. Watch outcomes are driven
// by the KAP watch hook, which is the only way to make the official stream fail
// without tearing down the socket the read needs.
func watchHistoryService(t *testing.T) *NativeService {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/sessions/s1":
			writeNativeTestEnvelope(w, map[string]any{"id": "s1", "title": "Synthetic"})
		case "/api/v1/sessions/s1/goal":
			writeNativeTestEnvelope(w, nil)
		case "/api/v1/sessions/s1/agents/main/turns":
			writeNativeTestEnvelope(w, map[string]any{"turns": []any{map[string]any{
				"turn_id": "t1",
				"items": []any{
					map[string]any{"item_id": "m1", "type": "user_message", "text": "保留这条历史"},
					map[string]any{"item_id": "m2", "type": "assistant_message", "text": "这条也必须保留"},
				},
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	db, err := storage.Open(filepath.Join(t.TempDir(), "watch-history.db"))
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
	return service
}

func refuseWatch(service *NativeService, count *int) {
	service.kap.mu.Lock()
	service.kap.watchHook = func(string) error {
		(*count)++
		return errors.New("官方事件流瞬时断开")
	}
	service.kap.mu.Unlock()
}

func releaseWatchHook(service *NativeService) {
	service.kap.mu.Lock()
	service.kap.watchHook = nil
	service.kap.mu.Unlock()
}

// A failed subscribe must not discard the history that was already read. Before
// this split the whole action failed, the pane showed an error and the user lost
// the messages they had a moment earlier.
func TestSessionWatchKeepsHistoryWhenSubscribeFails(t *testing.T) {
	service := watchHistoryService(t)
	refusals := 0
	refuseWatch(service, &refusals)

	value, err := service.Action(context.Background(), "session.watch", json.RawMessage(`{"id":"s1"}`))
	if err != nil {
		t.Fatalf("a refused subscribe failed the whole action: %v", err)
	}
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("session.watch result = %#v", value)
	}
	if result["watching"] != false {
		t.Fatalf("watching = %#v, want false after a refused subscribe", result["watching"])
	}
	watchError, _ := result["watchError"].(string)
	if watchError == "" {
		t.Fatal("a refused subscribe did not report watchError")
	}
	view, ok := result["view"].(SessionView)
	if !ok {
		t.Fatalf("session.watch lost the view: %#v", result["view"])
	}
	if view.Session.ID != "s1" {
		t.Fatalf("view session = %q", view.Session.ID)
	}
	if len(view.Messages) != 2 {
		t.Fatalf("view carried %d messages, want the 2 already read", len(view.Messages))
	}
	if view.Messages[0].Text != "保留这条历史" || view.Messages[1].Text != "这条也必须保留" {
		t.Fatalf("view messages = %#v", view.Messages)
	}
	if refusals != 1 {
		t.Fatalf("the subscribe was attempted %d times, want 1", refusals)
	}
	// WatchSession rolls its own count back on a failed first subscribe, so the
	// caller is told it holds nothing and must not unwatch.
	if refs := service.WatchedSessions()["s1"]; refs != 0 {
		t.Fatalf("a refused subscribe left refcount %d", refs)
	}
	if service.sessionHeld("s1") {
		t.Fatal("a refused subscribe left the session held")
	}
}

// A refused first subscribe must leave no reference behind, and an unwatch the
// caller sends anyway must not turn into a second release that silences a later
// successful watch.
func TestSessionWatchRefusalHoldsNoReference(t *testing.T) {
	service := watchHistoryService(t)
	refusals := 0
	refuseWatch(service, &refusals)
	ctx := context.Background()
	if _, err := service.Action(ctx, "session.watch", json.RawMessage(`{"id":"s1"}`)); err != nil {
		t.Fatalf("refused subscribe: %v", err)
	}
	if _, err := service.Action(ctx, "session.unwatch", json.RawMessage(`{"id":"s1"}`)); err != nil {
		t.Fatalf("a release after a refusal must stay safe: %v", err)
	}
	if refs := service.WatchedSessions()["s1"]; refs != 0 {
		t.Fatalf("refcount after the stray release = %d", refs)
	}

	// The retry after the official side recovers has to open the stream: the
	// refusal and the stray release cannot have poisoned the count.
	releaseWatchHook(service)
	value, err := service.Action(ctx, "session.watch", json.RawMessage(`{"id":"s1"}`))
	if err != nil {
		t.Fatalf("retry after the refusal: %v", err)
	}
	if value.(map[string]any)["watching"] != true {
		t.Fatalf("retry did not report watching: %#v", value)
	}
	if refs := service.WatchedSessions()["s1"]; refs != 1 {
		t.Fatalf("retry refcount = %d, want 1", refs)
	}
	service.UnwatchSession("s1")
	if refs, held := service.WatchedSessions()["s1"], service.sessionHeld("s1"); refs != 0 || held {
		t.Fatalf("releasing everything left refs=%d held=%v", refs, held)
	}
}

// A second holder keeps the stream open, so a pane that failed to subscribe and
// retries must not double count: one success is exactly one reference.
func TestSessionWatchSuccessHoldsExactlyOneReference(t *testing.T) {
	service := watchHistoryService(t)
	ctx := context.Background()
	value, err := service.Action(ctx, "session.watch", json.RawMessage(`{"id":"s1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if value.(map[string]any)["watching"] != true {
		t.Fatalf("watch did not report watching: %#v", value)
	}
	if _, present := value.(map[string]any)["watchError"]; present {
		t.Fatalf("a successful watch reported watchError: %#v", value)
	}
	if refs := service.WatchedSessions()["s1"]; refs != 1 {
		t.Fatalf("refcount = %d, want 1", refs)
	}
	if _, err := service.Action(ctx, "session.unwatch", json.RawMessage(`{"id":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	if refs := service.WatchedSessions()["s1"]; refs != 0 {
		t.Fatalf("refcount survived the release: %d", refs)
	}
}

// A read failure is still a failure: the view does not exist, so there is
// nothing to hand back and the pane must see the error.
func TestSessionWatchStillFailsWhenTheViewCannotBeRead(t *testing.T) {
	service := watchHistoryService(t)
	if _, err := service.Action(context.Background(), "session.watch", json.RawMessage(`{"id":"missing"}`)); err == nil {
		t.Fatal("a missing session was reported as watched")
	}
	if len(service.WatchedSessions()) != 0 {
		t.Fatalf("a failed read left a reference: %#v", service.WatchedSessions())
	}
}

// The failed-subscribe result has to survive the JSON round trip the renderer
// bridge performs; a field the encoder drops would silently lose the retry hint.
func TestSessionWatchFailureResultIsSerializable(t *testing.T) {
	service := watchHistoryService(t)
	refusals := 0
	refuseWatch(service, &refusals)
	value, err := service.Action(context.Background(), "session.watch", json.RawMessage(`{"id":"s1"}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("the result cannot be encoded: %v", err)
	}
	var decoded struct {
		Watching   bool        `json:"watching"`
		WatchError string      `json:"watchError"`
		View       SessionView `json:"view"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("the result cannot be decoded: %v", err)
	}
	if decoded.Watching {
		t.Fatal("a refused subscribe was encoded as watching")
	}
	if decoded.WatchError == "" {
		t.Fatal("the encoded result lost watchError")
	}
	if decoded.View.Session.ID != "s1" || len(decoded.View.Messages) != 2 {
		t.Fatalf("the encoded result lost the history: %#v", decoded.View)
	}
}
