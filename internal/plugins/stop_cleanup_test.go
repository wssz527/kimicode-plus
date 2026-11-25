package plugins

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// stopRecorder is the background-work hook the core attaches at runtime, reduced
// to what a test can observe: which plugin ids were stopped, and how many times.
// The real hook is planner.StopPluginThreads, so "one call per stopped plugin" is
// the property that decides whether a plugin keeps streaming or waiting for
// approval after it was stopped.
type stopRecorder struct {
	mu    sync.Mutex
	ids   []string
	errs  map[string]error
	calls int
}

func newStopRecorder() *stopRecorder {
	return &stopRecorder{errs: map[string]error{}}
}

func (r *stopRecorder) stop(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls += 1
	r.ids = append(r.ids, id)
	return r.errs[id]
}

func (r *stopRecorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

func (r *stopRecorder) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// officialBundle is one plugin's files, built the way ReleaseBundled expects
// them, so several plugins can be made official in the same manager.
func officialBundle(id string, capabilities []string) map[string][]byte {
	return map[string][]byte{
		"plugin.json": []byte(manifestFor(id, 2, capabilities)),
		"renderer.js": []byte("export function activate(){}\n"),
		"LICENSE":     []byte("MIT\n"),
	}
}

// publishOfficial registers every given bundle as official in one manager, so
// more than one plugin can hold privileged capabilities at the same time. The
// digest map is computed from the same bytes ReleaseBundled installs.
func publishOfficial(t *testing.T, manager *Manager, bundles map[string]map[string][]byte) {
	t.Helper()
	digests := map[string]string{}
	for id, files := range bundles {
		digests[id] = ContentDigest(files)
	}
	manager.officialDigests = func() (map[string]string, error) { return digests, nil }
	if err := manager.ReleaseBundled(context.Background(), bundles); err != nil {
		t.Fatal(err)
	}
}

// enableOfficial enables one already-published plugin and answers its token.
func enableOfficial(t *testing.T, manager *Manager, id string) string {
	t.Helper()
	enabled, err := manager.Enable(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if enabled.Token == "" {
		t.Fatal("enable issued no token")
	}
	return enabled.Token
}

// A fatal report is a stop, so it has to release everything the running
// instance owned — not only the views and commands. A plugin that faults keeps
// its identity token revoked, so nothing is left to release those resources
// later: a subscription would stay open against the official client, and a
// planner thread would keep streaming under a plugin that is no longer running.
func TestFatalReportReleasesEveryResourceOfTheStoppedPlugin(t *testing.T) {
	core := &pluginCoreDouble{}
	manager, token := officialManager(t, core, []string{CapSessionsRead, CapCommands, CapBrowserView})
	recorder := newStopRecorder()
	manager.UseStopPluginWork(recorder.stop)
	ctx := context.Background()

	// Everything the fault has to clean up: an official subscription, a command
	// another plugin may still call, and a view.
	if code, value := callPlugin(t, manager, token, "sessions.watch", map[string]any{"id": "s1"}); code != http.StatusOK {
		t.Fatalf("watch => %d %#v", code, value)
	}
	if code, value := callPlugin(t, manager, token, "commands.register", map[string]any{"name": "probe.echo", "title": "回显"}); code != http.StatusOK {
		t.Fatalf("commands.register => %d %#v", code, value)
	}
	if code, value := callPlugin(t, manager, token, "browser.open", map[string]any{"viewId": "fault-view", "url": "https://example.com/"}); code != http.StatusOK {
		t.Fatalf("browser.open => %d %#v", code, value)
	}
	if views := manager.Views("test.official"); len(views) != 1 {
		t.Fatalf("view was not registered: %#v", views)
	}

	value, err := manager.Call(ctx, "test.official", "report.error", token, reportRequest(t, map[string]any{
		"message": "activate blew up", "code": "PLUGIN_ACTIVATION_FAILED", "fatal": true,
	}))
	if err != nil {
		t.Fatalf("fatal report: %v", err)
	}
	if stopped, _ := value.(map[string]any)["stopped"].(bool); !stopped {
		t.Fatalf("fatal report did not stop the plugin: %#v", value)
	}

	if _, unwatch := watchedSessions(core); len(unwatch) != 1 || unwatch[0] != "s1" {
		t.Fatalf("the faulted plugin left its official subscription open: %v", unwatch)
	}
	manager.mu.Lock()
	held := len(manager.sessionWatches["test.official"])
	_, commandLeft := manager.commands["probe.echo"]
	manager.mu.Unlock()
	if held != 0 {
		t.Fatalf("the plugin layer still counts %d watches for a faulted plugin", held)
	}
	if commandLeft {
		t.Fatal("a faulted plugin left a callable command behind")
	}
	if views := manager.Views("test.official"); len(views) != 0 {
		t.Fatalf("a faulted plugin left %d views: %#v", len(views), views)
	}
	if got := recorder.seen(); len(got) != 1 || got[0] != "test.official" {
		t.Fatalf("background work was stopped for %v, want the faulted plugin once", got)
	}

	// The fault stays what it was: cleanup neither changes the state nor replaces
	// the reason the user reads.
	faulted := manager.Get2("test.official")
	if faulted.State != StateFaulted || faulted.Enabled {
		t.Fatalf("fatal report left the plugin %s enabled=%v", faulted.State, faulted.Enabled)
	}
	if !strings.Contains(faulted.Error, "PLUGIN_ACTIVATION_FAILED") {
		t.Fatalf("the fault reason was replaced: %q", faulted.Error)
	}
}

// A cleanup that fails during a fault is recorded as activity and nothing more.
// The failure must not change the outcome of the stop, and recording it must not
// hold a lock the cleanup path needs.
func TestFailedFaultCleanupIsRecordedWithoutChangingTheFault(t *testing.T) {
	core := &failingCore{failOn: "sessions.unwatch"}
	manager, token := officialManager(t, core, []string{CapSessionsRead, CapCommands})
	recorder := newStopRecorder()
	recorder.errs["test.official"] = &Error{Status: http.StatusInternalServerError, Code: "PLANNER_STOP_FAILED", Message: "synthetic stop failure"}
	manager.UseStopPluginWork(recorder.stop)
	ctx := context.Background()

	if code, value := callPlugin(t, manager, token, "sessions.watch", map[string]any{"id": "s1"}); code != http.StatusOK {
		t.Fatalf("watch => %d %#v", code, value)
	}
	if _, err := manager.Call(ctx, "test.official", "report.error", token, reportRequest(t, map[string]any{
		"message": "renderer crashed", "code": "PLUGIN_CRASH", "fatal": true,
	})); err != nil {
		t.Fatalf("fatal report: %v", err)
	}

	faulted := manager.Get2("test.official")
	if faulted.State != StateFaulted || !strings.Contains(faulted.Error, "PLUGIN_CRASH") {
		t.Fatalf("cleanup failures changed the fault: %#v", faulted)
	}
	items, _, err := loadActivity(ctx, manager.db, "test.official", 0, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var releaseRecorded, workRecorded bool
	for _, item := range items {
		if item.Kind != ActivityError {
			continue
		}
		if strings.Contains(item.Detail, "会话订阅未能全部释放") {
			releaseRecorded = true
		}
		if strings.Contains(item.Detail, "后台任务未能全部结束") {
			workRecorded = true
		}
	}
	if !releaseRecorded {
		t.Fatalf("a refused unsubscribe was not recorded: %#v", items)
	}
	if !workRecorded {
		t.Fatalf("a failed background stop was not recorded: %#v", items)
	}
	// The fault row survives too, so the reason is not lost among the cleanup rows.
	var reason string
	if err := manager.db.QueryRow(`SELECT detail FROM plugin_activity WHERE plugin_id=? AND kind=? ORDER BY id DESC LIMIT 1`, "test.official", ActivityError).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reason, "PLUGIN_CRASH") {
		t.Fatalf("the recorded reason is not the fault: %q", reason)
	}
	for _, item := range items {
		if item.Kind == ActivityError && strings.Contains(item.Detail, "PLUGIN_CRASH") {
			return
		}
	}
	t.Fatalf("the fault itself was never recorded: %#v", items)
}

// Safe mode stops every running plugin, so their background work ends with
// them: a planner thread that keeps streaming or waits for approval would
// otherwise outlive the plugin the user just blocked.
func TestSafeModeStopsTheBackgroundWorkOfThePluginsItStops(t *testing.T) {
	safe := false
	manager, _ := testManager(t, func() bool { return safe })
	core := &pluginCoreDouble{}
	manager.UseCore(core)
	recorder := newStopRecorder()
	manager.UseStopPluginWork(recorder.stop)
	ctx := context.Background()

	// Three plugins, three different situations: one running and holding work,
	// one already disabled with nothing left to stop, one installed but never
	// enabled.
	bundles := map[string]map[string][]byte{
		"test.running": officialBundle("test.running", []string{CapSessionsRead, CapCommands}),
		"test.stopped": officialBundle("test.stopped", []string{CapSessionsRead}),
		"test.idle":    officialBundle("test.idle", []string{CapSessionsRead}),
	}
	publishOfficial(t, manager, bundles)
	runningToken := enableOfficial(t, manager, "test.running")
	enableOfficial(t, manager, "test.stopped")
	if err := manager.Disable(ctx, "test.stopped"); err != nil {
		t.Fatal(err)
	}
	recorder.mu.Lock()
	recorder.ids = nil
	recorder.calls = 0
	recorder.mu.Unlock()

	// The running plugin holds resources, so it is in more than one cleanup set.
	if code, value := callPlugin(t, manager, runningToken, "sessions.watch", map[string]any{"id": "s1"}); code != http.StatusOK {
		t.Fatalf("watch => %d %#v", code, value)
	}
	if code, value := callPlugin(t, manager, runningToken, "commands.register", map[string]any{"name": "probe.echo"}); code != http.StatusOK {
		t.Fatalf("commands.register => %d %#v", code, value)
	}

	safe = true
	manager.EnforceSafeMode()

	if got := recorder.seen(); len(got) != 1 || got[0] != "test.running" {
		t.Fatalf("safe mode stopped background work for %v, want only the running plugin", got)
	}
	blocked := manager.Get2("test.running")
	if blocked.State != StateBlocked || blocked.Enabled {
		t.Fatalf("safe mode did not block the running plugin: %#v", blocked)
	}
	if remaining := manager.Get2("test.idle"); remaining.State != StateDisabled || remaining.Enabled {
		t.Fatalf("safe mode changed a plugin it did not stop: %#v", remaining)
	}
}

// The order matters when more than one plugin qualifies: every stopped plugin
// gets its own stop, and one failure does not skip the others.
func TestSafeModeStopsEveryStoppedPluginEvenWhenOneFails(t *testing.T) {
	safe := false
	manager, _ := testManager(t, func() bool { return safe })
	recorder := newStopRecorder()
	recorder.errs["test.first"] = &Error{Status: http.StatusInternalServerError, Code: "PLANNER_STOP_FAILED", Message: "synthetic stop failure"}
	manager.UseStopPluginWork(recorder.stop)
	ctx := context.Background()

	publishOfficial(t, manager, map[string]map[string][]byte{
		"test.first":  officialBundle("test.first", []string{CapSessionsRead}),
		"test.second": officialBundle("test.second", []string{CapSessionsRead}),
	})
	enableOfficial(t, manager, "test.first")
	enableOfficial(t, manager, "test.second")
	recorder.mu.Lock()
	recorder.ids = nil
	recorder.calls = 0
	recorder.mu.Unlock()

	safe = true
	manager.EnforceSafeMode()

	seen := map[string]int{}
	for _, id := range recorder.seen() {
		seen[id] += 1
	}
	if seen["test.first"] != 1 || seen["test.second"] != 1 {
		t.Fatalf("safe mode called stop %v, want once per stopped plugin", seen)
	}
	items, _, err := loadActivity(ctx, manager.db, "test.first", 0, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var recorded bool
	for _, item := range items {
		if item.Kind == ActivitySafeMode && strings.Contains(item.Detail, "后台任务未能全部结束") {
			recorded = true
		}
	}
	if !recorded {
		t.Fatalf("a failed background stop was not recorded for the blocked plugin: %#v", items)
	}
	// The other plugin is still blocked and its work still stopped, despite the
	// failure on the first one.
	for _, id := range []string{"test.first", "test.second"} {
		if blocked := manager.Get2(id); blocked.State != StateBlocked || blocked.Enabled {
			t.Fatalf("%s was not blocked by safe mode: %#v", id, blocked)
		}
	}
}

// EnforceSafeMode runs with no request context, so a hook that reports an error
// must not be able to deadlock the transition: the stop is called outside every
// manager lock, and the activity row is written after it returns.
func TestSafeModeStopRunsOutsideTheManagerLock(t *testing.T) {
	safe := false
	manager, _ := testManager(t, func() bool { return safe })
	publishOfficial(t, manager, map[string]map[string][]byte{
		"test.reentrant": officialBundle("test.reentrant", []string{CapSessionsRead}),
	})
	enableOfficial(t, manager, "test.reentrant")

	var sawLockFree bool
	done := make(chan struct{})
	manager.UseStopPluginWork(func(_ context.Context, id string) error {
		defer close(done)
		// The hook reads state through the public path, which takes the manager
		// lock; a stop called while that lock is held would deadlock here instead
		// of answering.
		sawLockFree = manager.Get2(id).State != ""
		return nil
	})
	safe = true
	manager.EnforceSafeMode()
	select {
	case <-done:
	default:
		t.Fatal("the stop hook never ran")
	}
	if !sawLockFree {
		t.Fatal("the stop hook could not read plugin state, so it ran under the manager lock")
	}
}
