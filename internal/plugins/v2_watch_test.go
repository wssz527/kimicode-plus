package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// watchedSessions returns the sessions a core double was asked to subscribe to,
// in call order, and the ones it was asked to release.
func watchedSessions(core *pluginCoreDouble) (watch []string, unwatch []string) {
	for _, item := range core.calls {
		if id, ok := item.payload["id"].(string); ok {
			switch item.action {
			case "sessions.watch":
				watch = append(watch, id)
			case "sessions.unwatch":
				unwatch = append(unwatch, id)
			}
		}
	}
	return watch, unwatch
}

// A plugin holding one session through two handles sends one subscribe: the
// second handle is its own count, not a second official subscription.
func TestSessionWatchForwardsOnlyOnThePluginTransition(t *testing.T) {
	core := &pluginCoreDouble{}
	manager, token := officialManager(t, core, []string{CapSessionsRead})
	for index := 0; index < 2; index++ {
		if code, value := callPlugin(t, manager, token, "sessions.watch", map[string]any{"id": "s1"}); code != http.StatusOK {
			t.Fatalf("watch %d => %d %#v", index, code, value)
		}
	}
	watch, unwatch := watchedSessions(core)
	if len(watch) != 1 || watch[0] != "s1" {
		t.Fatalf("official subscribes = %v, want one for s1", watch)
	}
	if len(unwatch) != 0 {
		t.Fatalf("a watch released something: %v", unwatch)
	}
	// The answer a second handle gets must not claim it opened a new stream.
	code, value := callPlugin(t, manager, token, "sessions.watch", map[string]any{"id": "s1"})
	if code != http.StatusOK {
		t.Fatalf("third watch => %d %#v", code, value)
	}
	inner := value["value"].(map[string]any)
	if inner["forwarded"] != false {
		t.Fatalf("the third handle claimed a forward: %#v", inner)
	}
	for index := 0; index < 3; index++ {
		if code, value := callPlugin(t, manager, token, "sessions.unwatch", map[string]any{"id": "s1"}); code != http.StatusOK {
			t.Fatalf("unwatch %d => %d %#v", index, code, value)
		}
	}
	watch, unwatch = watchedSessions(core)
	if len(watch) != 1 || len(unwatch) != 1 || unwatch[0] != "s1" {
		t.Fatalf("release was not collapsed into one unsubscribe: watch=%v unwatch=%v", watch, unwatch)
	}
}

// Disabling a plugin releases every subscription it still holds, so a stopped
// plugin cannot keep the official stream open. Views and commands are already
// released with the token; this is the third resource in that set.
func TestDisableReleasesSessionSubscriptions(t *testing.T) {
	core := &pluginCoreDouble{}
	manager, token := officialManager(t, core, []string{CapSessionsRead})
	for _, id := range []string{"s1", "s2"} {
		if code, value := callPlugin(t, manager, token, "sessions.watch", map[string]any{"id": id}); code != http.StatusOK {
			t.Fatalf("watch %s => %d %#v", id, code, value)
		}
	}
	if _, unwatch := watchedSessions(core); len(unwatch) != 0 {
		t.Fatalf("a plain watch released something: %v", unwatch)
	}
	if err := manager.Disable(context.Background(), "test.official"); err != nil {
		t.Fatal(err)
	}
	_, unwatch := watchedSessions(core)
	if len(unwatch) != 2 {
		t.Fatalf("disable released %v, want both sessions", unwatch)
	}
	seen := map[string]bool{}
	for _, id := range unwatch {
		seen[id] = true
	}
	if !seen["s1"] || !seen["s2"] {
		t.Fatalf("disable released the wrong sessions: %v", unwatch)
	}
	// The token is gone, so the same body cannot re-subscribe after the release.
	if code, _ := callPlugin(t, manager, token, "sessions.watch", map[string]any{"id": "s1"}); code == http.StatusOK {
		t.Fatal("a disabled plugin still held a working token")
	}
}

// A failed forward must not leave the local count claiming the stream is open,
// or every later watch would skip the subscribe forever.
func TestFailedWatchDoesNotStick(t *testing.T) {
	core := &failingCore{failOn: "sessions.watch"}
	manager, token := officialManager(t, core, []string{CapSessionsRead})
	if code, _ := callPlugin(t, manager, token, "sessions.watch", map[string]any{"id": "s1"}); code == http.StatusOK {
		t.Fatal("a failed official subscribe was reported as success")
	}
	core.failOn = ""
	code, value := callPlugin(t, manager, token, "sessions.watch", map[string]any{"id": "s1"})
	if code != http.StatusOK {
		t.Fatalf("retry after a failed subscribe => %d %#v", code, value)
	}
	inner := value["value"].(map[string]any)
	if inner["forwarded"] != true {
		t.Fatalf("the retry believed the stream was already open: %#v", inner)
	}
}

// The capability gate covers the watch actions like any other session action.
func TestSessionWatchRequiresSessionsRead(t *testing.T) {
	core := &pluginCoreDouble{}
	manager, token := officialManager(t, core, []string{CapSessionsHistory})
	for _, action := range []string{"sessions.watch", "sessions.unwatch"} {
		if code, value := callPlugin(t, manager, token, action, map[string]any{"id": "s1"}); code != http.StatusForbidden {
			t.Fatalf("%s without sessions:read => %d %#v", action, code, value)
		}
	}
	if watch, _ := watchedSessions(core); len(watch) != 0 {
		t.Fatalf("a refused watch reached the core: %v", watch)
	}
}

// A watch needs a real session id; an empty or malformed one is refused before
// anything reaches the core.
func TestSessionWatchValidatesTheID(t *testing.T) {
	core := &pluginCoreDouble{}
	manager, token := officialManager(t, core, []string{CapSessionsRead})
	for _, params := range []map[string]any{{}, {"id": ""}, {"id": "bad\nid"}, {"id": string(make([]byte, 201))}} {
		if code, value := callPlugin(t, manager, token, "sessions.watch", params); code != http.StatusBadRequest {
			t.Fatalf("watch %#v => %d %#v", params, code, value)
		}
	}
	if watch, _ := watchedSessions(core); len(watch) != 0 {
		t.Fatalf("a malformed id reached the core: %v", watch)
	}
}

// failingCore refuses one action and answers everything else normally, so a
// test can make a single transition fail without a second fake.
type failingCore struct {
	pluginCoreDouble
	failOn string
}

func (c *failingCore) Action(ctx context.Context, action string, payload json.RawMessage) (any, error) {
	if action == c.failOn {
		return nil, &Error{Status: http.StatusServiceUnavailable, Code: "PLUGIN_CORE_UNAVAILABLE", Message: "synthetic failure"}
	}
	return c.pluginCoreDouble.Action(ctx, action, payload)
}
