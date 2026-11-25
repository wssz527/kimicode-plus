package desktop

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOfficialSessionListCreateAndView(t *testing.T) {
	const older = `{"id":"session_old","title":"Old session","cwd":"/workspace","activity":"idle","pending_interaction":"none","archived":false}`
	const newer = `{"id":"session_new","title":"New session","cwd":"/workspace","activity":"turn","pending_interaction":"none","archived":false}`
	const legacy = `{"id":"legacy","metadata":{"cwd":"/legacy"},"activity":{"status":"idle"}}`
	created := false
	server := kapServer(t, func(w http.ResponseWriter, r *http.Request) {
		var data string
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/sessions":
			if got := r.URL.Query().Get("page_size"); got != "100" {
				t.Errorf("unexpected page size %q", got)
			}
			switch r.URL.Query().Get("page_token") {
			case "":
				first := older
				if created {
					first = newer + "," + older
				}
				data = `{"items":[` + first + `],"has_more":true,"next_page_token":"1+2/="}`
			case "1+2/=":
				data = `{"items":[` + legacy + `],"has_more":false}`
			default:
				t.Errorf("unexpected page token %q", r.URL.Query().Get("page_token"))
				http.NotFound(w, r)
				return
			}
		case "POST /api/v1/sessions":
			var body struct {
				Metadata struct{ CWD string } `json:"metadata"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Metadata.CWD != "/workspace" {
				t.Errorf("unexpected create body %+v, %v", body, err)
			}
			created = true
			data = newer
		case "GET /api/v1/sessions/session_old":
			data = older
		case "GET /api/v1/sessions/session_old/agents/main/turns":
			data = `{"turns":[{"turn_id":"t_old","items":[{"type":"user_message","item_id":"msg_old","text":"Past message"}]}],"has_more":false}`
		case "GET /api/v1/sessions/session_old/goal":
			data = "null"
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"code":0,"msg":"success","data":%s}`, data)
	})
	home := t.TempDir()
	writeInstance(t, home, "official.json", portOf(t, server), "test-token")
	service := &NativeService{kap: NewKAPClient(home), cwd: "/workspace", cache: map[string]cachedSession{}}
	ctx := t.Context()
	checkList := func(want ...string) []NativeSession {
		t.Helper()
		value, err := service.Action(ctx, "sessions.list", nil)
		if err != nil {
			t.Fatal(err)
		}
		items := value.(map[string]any)["items"].([]NativeSession)
		if len(items) != len(want) {
			t.Fatalf("list got %+v, want %v", items, want)
		}
		for i, id := range want {
			if items[i].ID != id {
				t.Fatalf("list got %+v, want %v", items, want)
			}
		}
		return items
	}
	items := checkList("session_old", "legacy")
	if items[0].CWD != "/workspace" || items[0].Status != "idle" || items[0].Busy || items[1].CWD != "/legacy" || items[1].Status != "idle" {
		t.Fatalf("unsupported session shape: %+v", items)
	}
	value, err := service.Action(ctx, "session.view", json.RawMessage(`{"id":"session_old"}`))
	if err != nil {
		t.Fatal(err)
	}
	view := value.(SessionView)
	if view.Session.ID != "session_old" || len(view.Messages) != 1 || view.Messages[0].Text != "Past message" {
		t.Fatalf("unexpected history: %+v", view)
	}
	value, err = service.Action(ctx, "session.create", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := value.(NativeSession); got.ID != "session_new" || !got.Busy || !got.MainTurnActive || got.Status != "turn" {
		t.Fatalf("unexpected created session: %+v", got)
	}
	checkList("session_new", "session_old", "legacy")
}

func TestSessionSendDeduplicatesConcurrentAndRepeatedClientMessages(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	server := kapServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.URL.Path != "/api/v1/sessions/s1/prompts" {
			t.Errorf("unexpected prompt %s: %v", r.URL, err)
		}
		if body["plan_mode"] != true || body["thinking"] != "off" || body["permission_mode"] != "manual" {
			t.Errorf("composer options missing from official payload: %+v", body)
		}
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		writeNativeTestEnvelope(w, map[string]any{"turn_id": "t1"})
	})
	home := t.TempDir()
	writeInstance(t, home, "official.json", portOf(t, server), "test-token")
	service := &NativeService{kap: NewKAPClient(home)}
	payload := json.RawMessage(`{"id":"s1","text":"hello","mode":"plan","thinking":"off","permissionMode":"manual","clientMessageId":"same"}`)
	result := make(chan error, 2)
	call := func() {
		_, err := service.Action(t.Context(), "session.send", payload)
		result <- err
	}
	go call()
	<-started
	go call()
	conflict := json.RawMessage(strings.Replace(string(payload), "hello", "different", 1))
	_, conflictErr := service.Action(t.Context(), "session.send", conflict)
	close(release)
	if conflictErr == nil || !strings.Contains(conflictErr.Error(), "不同内容") {
		t.Errorf("reused id with changed payload: %v", conflictErr)
	}
	for range 2 {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	value, err := service.Action(t.Context(), "session.send", payload)
	if err != nil || value.(map[string]any)["sessionId"] != "s1" || calls.Load() != 1 {
		t.Fatalf("replayed value = %v, error = %v, official requests = %d", value, err, calls.Load())
	}
}

func TestSessionSendHandlesDefiniteAndUnknownResults(t *testing.T) {
	var calls atomic.Int32
	server := kapServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			http.Error(w, "definitely refused", http.StatusBadRequest)
		case 2:
			http.Error(w, "unknown outcome", http.StatusInternalServerError)
		default:
			writeNativeTestEnvelope(w, nil)
		}
	})
	home := t.TempDir()
	writeInstance(t, home, "official.json", portOf(t, server), "test-token")
	service := &NativeService{kap: NewKAPClient(home)}
	payload := json.RawMessage(`{"id":"s1","text":"retry example","clientMessageId":"receipt"}`)
	for _, status := range []string{"400", "500", "500"} {
		_, err := service.Action(t.Context(), "session.send", payload)
		if err == nil || !strings.Contains(err.Error(), status) {
			t.Fatalf("expected HTTP %s, got %v", status, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("official requests = %d, want 2", calls.Load())
	}
}

func TestSessionRepliesSendExplicitCustomApprovalAndTypedAnswers(t *testing.T) {
	bodies := make(chan map[string]any, 2)
	server := kapServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		bodies <- body
		writeNativeTestEnvelope(w, nil)
	})
	home := t.TempDir()
	writeInstance(t, home, "official.json", portOf(t, server), "test-token")
	service := &NativeService{kap: NewKAPClient(home)}
	for _, payload := range []string{
		`{"id":"s1","interactionId":"a1","value":{"decision":"rejected","selectedLabel":"改变计划","feedback":"先列出影响"}}`,
		`{"id":"s1","interactionId":"q1","value":{"answers":{"q":{"kind":"multi_with_other","option_ids":["one"],"other_text":"two"}}}}`,
	} {
		if _, err := service.Action(t.Context(), "session.reply", json.RawMessage(payload)); err != nil {
			t.Fatal(err)
		}
	}
	approval, question := <-bodies, <-bodies
	if approval["kind"] != "approval" || approval["decision"] != "rejected" || approval["selected_label"] != "改变计划" || approval["feedback"] != "先列出影响" {
		t.Fatalf("invalid approval payload: %+v", approval)
	}
	answer := question["answers"].(map[string]any)["q"].(map[string]any)
	if question["kind"] != "question" || answer["kind"] != "multi_with_other" || answer["other_text"] != "two" {
		t.Fatalf("invalid question payload: %+v", question)
	}
	if _, err := service.Action(t.Context(), "session.reply", json.RawMessage(`{"id":"s1","interactionId":"a1","value":{"selectedLabel":"未知"}}`)); err == nil {
		t.Fatal("an unknown option became an approval")
	}
}
