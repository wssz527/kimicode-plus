package desktop

import (
	"encoding/json"
	"fmt"
	"net/http"
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
