package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"enhancedkimi/internal/storage"
)

// historyService wires a NativeService to a synthetic official API that answers
// the one endpoint history paging uses and records the queries it received.
func historyService(t *testing.T) (*NativeService, *[]string) {
	t.Helper()
	queries := &[]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only the page request is recorded: the service also probes the host at
		// startup, and an unrelated query would shift the index the assertions use.
		if !strings.Contains(r.URL.Path, "/agents/main/turns") {
			w.WriteHeader(http.StatusNotFound)
			writeNativeTestEnvelope(w, map[string]any{})
			return
		}
		*queries = append(*queries, r.URL.RawQuery)
		writeNativeTestEnvelope(w, map[string]any{
			"turns": []any{
				map[string]any{"turn_id": "t21", "ordinal": 21, "items": []any{
					map[string]any{"item_id": "a21", "type": "assistant_message", "text": "newer"},
				}},
				map[string]any{"turn_id": "t19", "ordinal": 19, "items": []any{
					map[string]any{"item_id": "u19", "type": "user_message", "text": "older"},
				}},
			},
			"has_more": true,
		})
	}))
	t.Cleanup(server.Close)
	db, err := storage.Open(filepath.Join(t.TempDir(), "history.db"))
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
	return service, queries
}

func TestSessionHistoryForwardsCursorAndHasMore(t *testing.T) {
	service, queries := historyService(t)
	value, err := service.Action(context.Background(), "sessions.history", json.RawMessage(`{"id":"s1","before":40,"size":20,"agentId":"main"}`))
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	page := value.(map[string]any)
	if page["hasMore"] != true {
		t.Fatalf("has_more was not forwarded: %#v", page)
	}
	// The smallest ordinal of the page is the cursor for the next older page.
	if next, _ := page["nextBefore"].(*int64); next == nil || *next != 19 {
		t.Fatalf("nextBefore = %#v", page["nextBefore"])
	}
	if !strings.Contains((*queries)[0], "page_size=20") || !strings.Contains((*queries)[0], "before_ordinal=40") {
		t.Fatalf("history query lost its cursor: %s", (*queries)[0])
	}
	messages := page["messages"].([]NativeMessage)
	if len(messages) != 2 || messages[0].Text != "newer" || messages[1].Text != "older" {
		t.Fatalf("history messages changed: %#v", messages)
	}
}

func TestSessionHistoryDefaultsAndBounds(t *testing.T) {
	service, queries := historyService(t)
	if _, err := service.Action(context.Background(), "sessions.history", json.RawMessage(`{"id":"s1","size":5000}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*queries)[0], "page_size=100") {
		t.Fatalf("page size was not capped: %s", (*queries)[0])
	}
	if strings.Contains((*queries)[0], "before_ordinal") {
		t.Fatalf("an unbounded request carried a cursor: %s", (*queries)[0])
	}
	if _, err := service.Action(context.Background(), "sessions.history", json.RawMessage(`{}`)); err == nil {
		t.Fatal("history without a session id was accepted")
	}
}

// The model list is a plain read of the official catalog and must not include
// recovery or planner fields from bootstrap.
func TestModelsListReturnsCatalogOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Models() reads the official /config document and derives the catalog.
		writeNativeTestEnvelope(w, map[string]any{"models": map[string]any{
			"kimi-k2": map[string]any{"display_name": "Kimi K2"},
		}})
	}))
	t.Cleanup(server.Close)
	db, err := storage.Open(filepath.Join(t.TempDir(), "models.db"))
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
	value, err := service.Action(context.Background(), "models.list", nil)
	if err != nil {
		t.Fatal(err)
	}
	page := value.(map[string]any)
	items := page["items"].([]map[string]string)
	if len(items) != 1 || items[0]["id"] != "kimi-k2" {
		t.Fatalf("models.list = %#v", page)
	}
}
