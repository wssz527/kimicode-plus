package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
)

// gateCore holds the first subscribe open until the test lets it finish, so a
// second pane can reach the manager while the first transition is still in
// flight. It counts every attempt, not just the ones that were allowed to
// succeed, because the interesting case is two panes sharing one transition.
type gateCore struct {
	mu       sync.Mutex
	attempts int
	fail     bool
	entered  chan struct{}
	release  chan struct{}
	unwatch  []string
}

func newGateCore() *gateCore {
	return &gateCore{entered: make(chan struct{}), release: make(chan struct{})}
}

func (c *gateCore) Action(_ context.Context, action string, payload json.RawMessage) (any, error) {
	values := map[string]any{}
	_ = json.Unmarshal(payload, &values)
	sessionID, _ := values["id"].(string)
	c.mu.Lock()
	c.attempts += 1
	first := c.attempts == 1
	fail := c.fail
	if action == "sessions.unwatch" {
		c.unwatch = append(c.unwatch, sessionID)
	}
	c.mu.Unlock()
	if action == "sessions.watch" && first {
		close(c.entered)
		<-c.release
	}
	if action == "sessions.watch" && fail {
		return nil, &Error{Status: http.StatusServiceUnavailable, Code: "PLUGIN_CORE_UNAVAILABLE", Message: "synthetic refusal"}
	}
	return map[string]any{"sessionId": sessionID}, nil
}

func (c *gateCore) Events() EventSource { return nil }

// Two panes that open on the same session at the same time share one official
// subscription. When that subscribe is refused, neither pane may be told it is
// watching: the one that waited must see the rolled-back count and try its own
// forward, and it must fail too because nothing is open.
func TestConcurrentFirstWatchLeavesNoFalseSubscription(t *testing.T) {
	core := newGateCore()
	core.fail = true
	manager, token := officialManager(t, core, []string{CapSessionsRead})

	results := make([]int, 2)
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		results[0], _ = callPlugin(t, manager, token, "sessions.watch", map[string]any{"id": "s1"})
	}()
	<-core.entered
	wait.Add(1)
	go func() {
		defer wait.Done()
		results[1], _ = callPlugin(t, manager, token, "sessions.watch", map[string]any{"id": "s1"})
	}()
	// The second pane has to be inside the manager before the first forward
	// answers; that window is exactly what the serialized transition closes.
	time.Sleep(100 * time.Millisecond)
	close(core.release)
	wait.Wait()

	core.mu.Lock()
	attempts := core.attempts
	core.mu.Unlock()
	if attempts != 2 {
		t.Fatalf("refused subscribes reaching the core = %d, want one per pane", attempts)
	}
	for index, code := range results {
		if code == http.StatusOK {
			t.Fatalf("pane %d was told it watches a stream the core refused", index)
		}
	}
	// Nothing survived the refusals, so a later subscribe is a real transition
	// and not a no-op on top of a phantom count.
	core.mu.Lock()
	core.fail = false
	core.mu.Unlock()
	code, value := callPlugin(t, manager, token, "sessions.watch", map[string]any{"id": "s1"})
	if code != http.StatusOK {
		t.Fatalf("watch after the refusals => %d %#v", code, value)
	}
	if inner, ok := value["value"].(map[string]any); !ok || inner["forwarded"] != true {
		t.Fatalf("the retry skipped the subscribe: %#v", value)
	}
}

// A refused unsubscribe must not silently drop the plugin's last handle: the
// official stream is still open and nothing else is left to close it. The retry
// therefore has to forward again.
func TestFailedUnwatchKeepsThePluginHoldingTheStream(t *testing.T) {
	core := &failingCore{}
	manager, token := officialManager(t, core, []string{CapSessionsRead})
	if code, value := callPlugin(t, manager, token, "sessions.watch", map[string]any{"id": "s1"}); code != http.StatusOK {
		t.Fatalf("watch => %d %#v", code, value)
	}
	core.failOn = "sessions.unwatch"
	if code, value := callPlugin(t, manager, token, "sessions.unwatch", map[string]any{"id": "s1"}); code == http.StatusOK {
		t.Fatalf("a refused release was reported as success: %#v", value)
	}
	core.failOn = ""
	code, value := callPlugin(t, manager, token, "sessions.unwatch", map[string]any{"id": "s1"})
	if code != http.StatusOK {
		t.Fatalf("retry after a refused release => %d %#v", code, value)
	}
	inner := value["value"].(map[string]any)
	if inner["forwarded"] != true {
		t.Fatalf("the failed release dropped the plugin's only handle: %#v", inner)
	}
	if _, unwatch := watchedSessions(&core.pluginCoreDouble); len(unwatch) != 1 {
		t.Fatalf("the stream was released %d times, want one real unsubscribe", len(unwatch))
	}
	// The list is now empty, so another release has nothing to forward.
	code, value = callPlugin(t, manager, token, "sessions.unwatch", map[string]any{"id": "s1"})
	if code != http.StatusOK {
		t.Fatalf("idempotent release => %d %#v", code, value)
	}
	if inner := value["value"].(map[string]any); inner["forwarded"] != false {
		t.Fatalf("a second release forwarded again: %#v", inner)
	}
}
