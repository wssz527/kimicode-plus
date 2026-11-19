package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseMainPIDRequiresIsolatedProfile(t *testing.T) {
	executable := "/Applications/Kimi Code.app/Contents/MacOS/Kimi Code"
	output := []byte(" 101 " + executable + " --user-data-dir=/tmp/isolated --no-first-run\n 102 " + executable + " --type=renderer --user-data-dir=/tmp/isolated\n")
	pid, err := parseMainPID(output, executable, "/tmp/isolated")
	if err != nil || pid != 101 {
		t.Fatalf("unexpected pid %d: %v", pid, err)
	}
	if _, err = parseMainPID(output, executable, "/tmp/other"); err == nil {
		t.Fatal("other profile must not match")
	}
	output = append(output, []byte(" 103 "+executable+" --user-data-dir=/tmp/isolated\n")...)
	if _, err = parseMainPID(output, executable, "/tmp/isolated"); err == nil {
		t.Fatal("multiple main processes must be refused")
	}
}
func TestHostContractIsVersionAgnostic(t *testing.T) {
	for _, version := range []string{"1.3.4", "1.3.5", "1.3.7"} {
		identity := HostIdentity{Type: "browser", AppVersion: version, Electron: SupportedElectron}
		contract, err := identity.Contract()
		if err != nil {
			t.Fatalf("verified host %s rejected: %v", version, err)
		}
		if !contract.Verified {
			t.Fatalf("verified host %s not marked verified", version)
		}
	}
	unknown := HostIdentity{Type: "browser", AppVersion: "1.9.9", Electron: SupportedElectron}
	contract, err := unknown.Contract()
	if err != nil {
		t.Fatalf("unknown version with valid process type was rejected: %v", err)
	}
	if contract.Verified {
		t.Fatal("unknown version must not be marked human-verified")
	}
	if got := HostVersionNote(contract.AppVersion, contract.Electron); got != UnverifiedVersionNote {
		t.Fatalf("HostVersionNote = %q, want %q", got, UnverifiedVersionNote)
	}
	if _, err := (HostIdentity{Type: "renderer", AppVersion: "1.3.7", Electron: SupportedElectron}).Contract(); err == nil {
		t.Fatal("non-browser process must be rejected")
	}
	if _, err := (HostIdentity{Type: "browser", AppVersion: "", Electron: SupportedElectron}).Contract(); err == nil {
		t.Fatal("empty appVersion must be rejected")
	}
}

func TestNativeDOMAllowsEmptyOfficialChat(t *testing.T) {
	capabilities := map[string]bool{"ptShell": true, "ptBody": true, "sideColumn": true, "sideFooter": true, "chat": false, "composer": false, "sideChat": false}
	if missing := missingNativeCapability(capabilities); missing != "" {
		t.Fatalf("empty official chat blocked unrelated surfaces: %s", missing)
	}
	capabilities["sideFooter"] = false
	if missing := missingNativeCapability(capabilities); missing != "sideFooter" {
		t.Fatalf("missing required mount was accepted: %s", missing)
	}
}

func TestEventBusScopesAndUnknownCursor(t *testing.T) {
	bus := NewEventBus()
	initial := bus.Poll(context.Background(), -1, []string{"session:a"}, 0)
	if initial.Next != 0 || len(initial.Events) != 0 {
		t.Fatalf("unexpected initial page: %+v", initial)
	}
	bus.Publish("session:a", "a")
	bus.Publish("session:b", "b")
	page := bus.Poll(context.Background(), initial.Next, []string{"session:a"}, 0)
	if len(page.Events) != 1 || page.Events[0].Value != "a" || page.Next != 2 {
		t.Fatalf("events crossed scopes: %+v", page)
	}
	page = bus.Poll(context.Background(), 99, []string{"session:a"}, 0)
	if len(page.Events) != 0 {
		t.Fatalf("future cursor must not invent events: %+v", page)
	}
}

func TestSessionViewPreservesMessagesAndPlanApproval(t *testing.T) {
	cache := cachedSession{
		session: NativeSession{ID: "s1", Title: "one"},
		turns: []NativeTurn{{TurnID: "turn1", Items: []NativeItem{
			{ItemID: "u1", Type: "user_message", Text: "hello", CreatedAt: "2026-11-16T00:00:00Z", Attachments: []NativeAttachment{{Kind: "file", Name: "note.txt"}}},
			{ItemID: "a1", Type: "assistant_message", Text: "world"},
			{ItemID: "tool1", Type: "tool_call", Name: "plan", InteractionID: "i1", Stage: "awaiting_approval", Display: []byte(`{"kind":"plan_review","plan":"steps","review":{"state":"pending"},"options":[{"label":"执行"}]}`)},
		}}},
	}
	view := sessionViewFromCache(cache)
	if len(view.Messages) != 3 || view.Messages[0].Role != "user" || view.Messages[1].Text != "world" || view.Messages[2].Role != "tool" {
		t.Fatalf("messages changed: %+v", view.Messages)
	}
	if len(view.Messages[0].Attachments) != 1 || view.Messages[0].Attachments[0].Name != "note.txt" {
		t.Fatalf("message attachments lost: %+v", view.Messages[0])
	}
	if view.Messages[0].At != "2026-11-16T00:00:00Z" || view.Messages[1].At != "" {
		t.Fatalf("message timestamps changed: %+v", view.Messages)
	}
	if len(view.Approvals) != 1 || view.Approvals[0].Detail != "steps" || view.Approvals[0].Options[0].Value != "执行" {
		t.Fatalf("approval changed: %+v", view.Approvals)
	}
}

func TestTurnEndedPreservesStreamedItems(t *testing.T) {
	service := &NativeService{bus: NewEventBus(), cache: map[string]cachedSession{
		"s1": {session: NativeSession{ID: "s1", Busy: true, MainTurnActive: true}, turns: []NativeTurn{{TurnID: "turn1", Items: []NativeItem{{ItemID: "a1", Type: "assistant_message", Text: "complete reply"}}}}},
	}}
	service.applyFrame(NativeFrame{Type: "turn.ended", SessionID: "s1", Turn: NativeTurn{TurnID: "turn1"}})
	view := sessionViewFromCache(service.cache["s1"])
	if len(view.Messages) != 1 || view.Messages[0].Text != "complete reply" {
		t.Fatalf("turn completion erased messages: %+v", view.Messages)
	}
	if view.Session.Busy || view.Session.MainTurnActive {
		t.Fatal("turn completion did not release busy state")
	}
}

func TestAutomationDefaultsAreIndependentAndDisabled(t *testing.T) {
	settings := defaultAutomationSettings()
	if settings.KeepAlive.Enabled || settings.Compaction.Enabled || settings.Recovery.Enabled {
		t.Fatal("automation must be opt-in")
	}
	if settings.KeepAlive.Minutes != 25 || settings.Compaction.IdleMinutes != 25 || settings.Compaction.ContextThreshold != 60 {
		t.Fatalf("unexpected defaults: %+v", settings)
	}
}

func TestEventPollWithoutScopesDoesNotPanic(t *testing.T) {
	service := &NativeService{bus: NewEventBus()}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	value, err := service.Action(ctx, "events.poll", []byte(`{"after":0,"waitMs":0}`))
	if err != nil {
		t.Fatal(err)
	}
	page, ok := value.(EventPage)
	if !ok || len(page.Events) != 0 {
		t.Fatalf("unexpected page: %+v", value)
	}
}

func TestPromptPreservesPermissionAndLocalAttachments(t *testing.T) {
	file := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(file, []byte("synthetic attachment"), 0600); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v1/sessions/s1/prompts" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		body = nil
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		writeNativeTestEnvelope(w, map[string]any{"prompt_id": "p1"})
	}))
	defer server.Close()
	kap := NewKAPClient("")
	kap.base, kap.token = server.URL, "synthetic"
	service := &NativeService{kap: kap, cache: map[string]cachedSession{}}
	input := map[string]any{"id": "s1", "text": "review", "mode": "plan", "permissionMode": "manual", "attachments": []any{file}}
	if _, err := service.sendPrompt(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if body["permission_mode"] != "manual" || body["plan_mode"] != true {
		t.Fatalf("prompt controls changed: %+v", body)
	}
	content := body["content"].([]any)
	if len(content) != 2 || content[1].(map[string]any)["path"] != file {
		t.Fatalf("attachment lost: %+v", content)
	}
	delete(input, "permissionMode")
	if _, err := service.sendPrompt(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if _, exists := body["permission_mode"]; exists {
		t.Fatalf("inherited permission was overridden: %+v", body)
	}
	input["permissionMode"] = "unknown"
	if _, err := service.sendPrompt(context.Background(), input); err == nil {
		t.Fatal("invalid permission accepted")
	}
	delete(input, "permissionMode")
	input["attachments"] = []any{"relative.txt"}
	if _, err := service.sendPrompt(context.Background(), input); err == nil {
		t.Fatal("relative attachment accepted")
	}
	if requests != 2 {
		t.Fatalf("invalid input sent %d requests", requests)
	}
}
