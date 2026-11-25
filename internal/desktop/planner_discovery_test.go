package desktop

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

// Thread and model discovery is answered before the `planner.` feature flag is
// consulted. It used to sit in the action switch, which the generic prefix branch
// already owned, so a v2 plugin could never reach it while the frozen built-in
// planner stayed closed.
func TestPlannerDiscoveryBypassesTheBuiltinFeatureFlag(t *testing.T) {
	service, _ := newAddonFeatureService(t)
	stub := &recoveryPlannerStub{thread: plannerRecoveryThread{ID: "p1", Title: "合成规划", Model: "synthetic"}}
	service.UsePlanner(stub)
	value, err := service.Action(context.Background(), "planner.threads", nil)
	if err != nil {
		t.Fatalf("planner.threads with the built-in planner closed: %v", err)
	}
	page, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("planner.threads = %#v", value)
	}
	threads, ok := page["items"].([]plannerRecoveryThread)
	if !ok || len(threads) != 1 || threads[0].ID != "p1" {
		t.Fatalf("planner.threads items = %#v", page["items"])
	}
	if _, err = service.Action(context.Background(), "planner.models", nil); err != nil {
		t.Fatalf("planner.models with the built-in planner closed: %v", err)
	}
}

// Every other planner action keeps the flag: only read-only discovery is exempt.
func TestPlannerMutationsStillRequireTheFeatureFlag(t *testing.T) {
	service, _ := newAddonFeatureService(t)
	stub := &recoveryPlannerStub{thread: plannerRecoveryThread{ID: "p1"}}
	service.UsePlanner(stub)
	if _, err := service.Action(context.Background(), "planner.retry", []byte(`{"id":"p1","revision":1}`)); err == nil || !strings.Contains(err.Error(), "附加功能") {
		t.Fatalf("closed planner still dispatched a mutation: %v", err)
	}
	if _, err := service.Action(context.Background(), "planner.service.planner.retry", []byte(`{"id":"p1","revision":1}`)); err != nil {
		t.Fatalf("plugin planner path must skip the built-in flag: %v", err)
	}
}

// The bridge accepts one action per request and forwards it verbatim, so its
// grammar has to cover every name the plugin surface can send — including the
// two-segment `browser.view.open` and `planner.service.*` verbs.
func TestBootstrapAcceptsNamespacedActionNames(t *testing.T) {
	for _, wanted := range []string{
		"browser.view.open",
		"browser.view.bounds",
		"planner.service.planner.send",
		"core.fetch",
		"window.openSettings",
		"host.pickFiles",
	} {
		if !bootstrapActionAllowed(wanted) {
			t.Fatalf("bridge grammar rejects the action it must forward: %s", wanted)
		}
	}
	for _, rejected := range []string{
		"",
		"Browser.view.open",
		"browser..open",
		"a.b.c.d.e.f",
		".open",
		"browser.view.",
		"browser/view",
		"browser.view.open;rm -rf /",
	} {
		if bootstrapActionAllowed(rejected) {
			t.Fatalf("bridge grammar accepted a malformed action: %q", rejected)
		}
	}
}

// bootstrapActionAllowed applies the same grammar the host uses. The segment
// bound is read out of the source literal, so the test cannot keep passing after
// the host rule changes.
func bootstrapActionAllowed(action string) bool {
	const marker = "action.length > 80 || !/^(?:[a-z]+\\.){0,"
	index := strings.Index(bootstrapSource, marker)
	if index < 0 {
		return false
	}
	rest := bootstrapSource[index+len(marker):]
	end := strings.Index(rest, "}[a-zA-Z][a-zA-Z0-9]*$/.test(body.action)")
	if end < 0 {
		return false
	}
	segments := rest[:end]
	pattern := regexp.MustCompile(`^(?:[a-z]+\.){0,` + segments + `}[a-zA-Z][a-zA-Z0-9]*$`)
	if len(action) == 0 || len(action) > 80 {
		return false
	}
	return pattern.MatchString(action)
}

func TestPlannerEventPageSignalsLostAndRestartedCursors(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()
	for i := range 2003 {
		bus.Publish("unrelated", i)
	}
	for _, after := range []int64{0, 1, 4000} {
		page := bus.Poll(context.Background(), after, []string{"planner:p1"}, 0)
		if !page.Reset || page.Next != 2003 || len(page.Events) != 0 {
			t.Fatalf("cursor %d did not reset: %+v", after, page)
		}
	}
	if page := bus.Poll(context.Background(), 2002, []string{"planner:p1"}, 0); page.Reset || page.Next != 2003 {
		t.Fatalf("valid cursor reset: %+v", page)
	}
}
