package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"enhancedkimi/internal/storage"

	"github.com/coder/websocket"
)

// watchFrames is one connection's view of the official event stream: the frames
// the client sent, in order, behind a lock because the reader goroutine appends
// while the test reads.
type watchFrames struct {
	mu     sync.Mutex
	frames []string
	ready  chan struct{}
}

func (w *watchFrames) add(kind string) {
	w.mu.Lock()
	w.frames = append(w.frames, kind)
	w.mu.Unlock()
}

func (w *watchFrames) snapshot() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string{}, w.frames...)
}

// waitFor polls until the recorded frames contain the wanted kind, or the
// budget runs out. A watch is asynchronous: Watch() only enqueues, the frame is
// written by the connection goroutine, so a bare sleep would be a flaky test.
func (w *watchFrames) waitFor(t *testing.T, wanted string, budget time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		for _, kind := range w.snapshot() {
			if kind == wanted {
				return true
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// watchService wires a NativeService to a synthetic official API that accepts an
// event stream and records the subscribe/unsubscribe frames it receives. This is
// the only way to prove the reference count reaches the wire: the local map can
// be correct while nothing is sent.
func watchService(t *testing.T) (*NativeService, *watchFrames) {
	t.Helper()
	frames := &watchFrames{ready: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != NativeAPI+"/ws" {
			if r.URL.Path == "/api/v1/sessions/s1" {
				writeNativeTestEnvelope(w, map[string]any{"id": "s1", "title": "Synthetic"})
				return
			}
			if r.URL.Path == "/api/v1/sessions/s1/goal" {
				writeNativeTestEnvelope(w, nil)
				return
			}
			if r.URL.Path == "/api/v1/sessions/s1/agents/main/turns" {
				writeNativeTestEnvelope(w, map[string]any{"turns": []any{}})
				return
			}
			http.NotFound(w, r)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		closeOnce(frames.ready)
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			_, data, readErr := conn.Read(ctx)
			cancel()
			if readErr != nil {
				return
			}
			var frame struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(data, &frame) != nil {
				continue
			}
			if frame.Type == "subscribe" || frame.Type == "unsubscribe" {
				frames.add(frame.Type)
			}
		}
	}))
	t.Cleanup(server.Close)
	db, err := storage.Open(filepath.Join(t.TempDir(), "watch.db"))
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
	select {
	case <-frames.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("the event stream never connected")
	}
	return service, frames
}

func closeOnce(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

// Two consumers on one session produce one official subscribe, and the stream
// stays open until the last of them releases.
func TestWatchIsRefcountedPerSession(t *testing.T) {
	service, frames := watchService(t)
	ctx := context.Background()
	for index := 0; index < 2; index++ {
		value, err := service.Action(ctx, "sessions.watch", json.RawMessage(`{"id":"s1"}`))
		if err != nil {
			t.Fatalf("watch %d: %v", index, err)
		}
		if value.(map[string]any)["watching"] != true {
			t.Fatalf("watch %d did not report watching: %#v", index, value)
		}
	}
	if refs := service.WatchedSessions()["s1"]; refs != 2 {
		t.Fatalf("refcount = %d, want 2", refs)
	}
	if !frames.waitFor(t, "subscribe", 3*time.Second) {
		t.Fatal("the official stream was never subscribed")
	}
	// Give a duplicate subscribe a chance to appear before asserting there is none.
	time.Sleep(150 * time.Millisecond)
	if got := countFrames(frames.snapshot(), "subscribe"); got != 1 {
		t.Fatalf("official subscribes = %d, want 1", got)
	}

	if _, err := service.Action(ctx, "sessions.unwatch", json.RawMessage(`{"id":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if got := countFrames(frames.snapshot(), "unsubscribe"); got != 0 {
		t.Fatalf("first release unsubscribed while another holder remained: %d", got)
	}
	if _, err := service.Action(ctx, "sessions.unwatch", json.RawMessage(`{"id":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	if !frames.waitFor(t, "unsubscribe", 3*time.Second) {
		t.Fatal("the last release did not unsubscribe")
	}
	if refs := service.WatchedSessions()["s1"]; refs != 0 {
		t.Fatalf("refcount survived the last release: %d", refs)
	}
}

// Reading a session must not open the official stream: a reader that never
// watches should cost nothing.
func TestReadingASessionDoesNotSubscribe(t *testing.T) {
	service, frames := watchService(t)
	if _, err := service.Action(context.Background(), "sessions.view", json.RawMessage(`{"id":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := frames.snapshot(); len(got) != 0 {
		t.Fatalf("a plain read subscribed: %v", got)
	}
	if len(service.WatchedSessions()) != 0 {
		t.Fatalf("a plain read left a reference: %#v", service.WatchedSessions())
	}
}

// The v1 action pair must share the same count. A v1 reader releasing its own
// watch cannot drop a subscription a second holder still needs.
func TestLegacyWatchPairSharesTheSameReferenceCount(t *testing.T) {
	service, frames := watchService(t)
	ctx := context.Background()
	if _, err := service.Action(ctx, "session.watch", json.RawMessage(`{"id":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Action(ctx, "sessions.watch", json.RawMessage(`{"id":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	if refs := service.WatchedSessions()["s1"]; refs != 2 {
		t.Fatalf("the two APIs did not share the count: %d", refs)
	}
	if !frames.waitFor(t, "subscribe", 3*time.Second) {
		t.Fatal("no official subscribe")
	}
	if _, err := service.Action(ctx, "session.unwatch", json.RawMessage(`{"id":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if got := countFrames(frames.snapshot(), "unsubscribe"); got != 0 {
		t.Fatalf("the v1 release dropped a shared subscription: %d", got)
	}
	if refs := service.WatchedSessions()["s1"]; refs != 1 {
		t.Fatalf("refcount after the v1 release = %d, want 1", refs)
	}
}

// Releasing twice is a no-op rather than an extra unsubscribe, so a teardown
// path that also runs after an explicit unwatch cannot silence the stream.
func TestUnwatchWithoutAReferenceIsANoOp(t *testing.T) {
	service, frames := watchService(t)
	ctx := context.Background()
	if _, err := service.Action(ctx, "sessions.unwatch", json.RawMessage(`{"id":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if got := frames.snapshot(); len(got) != 0 {
		t.Fatalf("a stray release reached the wire: %v", got)
	}
	if refs := service.WatchedSessions()["s1"]; refs != 0 {
		t.Fatalf("a stray release created a reference: %d", refs)
	}
}

// A watch with no session id is refused instead of silently watching "".
func TestWatchRequiresASessionID(t *testing.T) {
	service, _ := watchService(t)
	if _, err := service.Action(context.Background(), "sessions.watch", json.RawMessage(`{}`)); err == nil {
		t.Fatal("watch without a session id was accepted")
	}
	if _, err := service.Action(context.Background(), "sessions.unwatch", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("releasing nothing must stay safe: %v", err)
	}
}

func countFrames(frames []string, kind string) int {
	count := 0
	for _, frame := range frames {
		if frame == kind {
			count++
		}
	}
	return count
}

// An internal module's interest is a standing set member, not a reference: the
// automation ticks re-assert it every round and never release it, so counting it
// would grow until the process exits. While it stands, a plugin's last unwatch
// must not reach the wire — otherwise a plugin pane closing would silence
// automation for the whole process.
func TestInternalInterestSurvivesTheLastPluginRelease(t *testing.T) {
	service, frames := watchService(t)
	ctx := context.Background()
	if err := service.WatchSessionInternal("s1"); err != nil {
		t.Fatalf("internal watch: %v", err)
	}
	if !frames.waitFor(t, "subscribe", 3*time.Second) {
		t.Fatal("the internal interest never subscribed")
	}
	// Re-asserting the same interest is idempotent: automation calls this on
	// every tick, and a second subscribe or a growing count would both be wrong.
	for index := 0; index < 3; index++ {
		if err := service.WatchSessionInternal("s1"); err != nil {
			t.Fatalf("re-assert %d: %v", index, err)
		}
	}
	if refs := service.WatchedSessions()["s1"]; refs != 0 {
		t.Fatalf("an internal interest became a counted reference: %d", refs)
	}
	if held := service.HeldSessions()["s1"]; held != 1 {
		t.Fatalf("held = %d, want the one internal holder", held)
	}

	if _, err := service.Action(ctx, "sessions.watch", json.RawMessage(`{"id":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if got := countFrames(frames.snapshot(), "subscribe"); got != 1 {
		t.Fatalf("a plugin watch re-subscribed under an internal holder: %d", got)
	}
	if _, err := service.Action(ctx, "sessions.unwatch", json.RawMessage(`{"id":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := countFrames(frames.snapshot(), "unsubscribe"); got != 0 {
		t.Fatalf("the last plugin release unsubscribed an internally held session: %d", got)
	}
	if !service.sessionHeld("s1") {
		t.Fatal("the session stopped being held while automation still needs it")
	}
}

// The internal interest is what keeps a subscription alive across a reconnect:
// the client re-subscribes everything in its watched set, so the set membership
// is the observable proof that a dropped socket comes back with the session.
func TestInternalInterestSurvivesReconnect(t *testing.T) {
	service, frames := watchService(t)
	if err := service.WatchSessionInternal("s1"); err != nil {
		t.Fatalf("internal watch: %v", err)
	}
	if !frames.waitFor(t, "subscribe", 3*time.Second) {
		t.Fatal("the internal interest never subscribed")
	}
	// Dropping the socket is what a quit-and-relaunch official client looks
	// like to this process; the loop reconnects and replays the watched set.
	service.kap.mu.Lock()
	socket := service.kap.socket
	service.kap.socket = nil
	service.kap.mu.Unlock()
	if socket != nil {
		_ = socket.Close(websocket.StatusGoingAway, "test reconnect")
	}
	if !frames.waitFor(t, "unsubscribe", 100*time.Millisecond) {
		// No unsubscribe is expected; the call only yields the reader.
	}
	if !waitForFrameCount(frames, "subscribe", 2, 10*time.Second) {
		t.Fatalf("the reconnect did not re-subscribe the internally held session: %v", frames.snapshot())
	}
}

func waitForFrameCount(frames *watchFrames, kind string, want int, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if countFrames(frames.snapshot(), kind) >= want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// Concurrent first watches must not leave a reference behind when the one that
// actually opened the stream fails. The old code let the loser answer success
// and then kept a count whose subscription was rolled back, so the caller was
// told it was watching and never received a frame.
func TestConcurrentFirstWatchLeavesNoFalseSubscription(t *testing.T) {
	service, _ := watchService(t)
	// The first subscribe is refused. Before the transition was serialized, the
	// second caller saw the first caller's reference, answered success, and the
	// refusal then rolled the count back to zero — so one caller was told it was
	// watching a stream that never opened, and its later release closed nothing.
	var mu sync.Mutex
	refusals := 0
	service.kap.mu.Lock()
	service.kap.watchHook = func(string) error {
		mu.Lock()
		defer mu.Unlock()
		refusals++
		return errors.New("official refused the subscribe")
	}
	service.kap.mu.Unlock()

	const callers = 8
	results := make([]error, callers)
	var wg sync.WaitGroup
	for index := 0; index < callers; index++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			results[slot] = service.WatchSession("s1")
		}(index)
	}
	wg.Wait()
	mu.Lock()
	attempts := refusals
	mu.Unlock()
	// Serializing means every caller runs its own transition and hears the
	// refusal. The old code answered success to the callers that arrived while
	// the first one was still on the wire, which is the false subscription here.
	if attempts != callers {
		t.Fatalf("the subscribe was attempted %d times, want one attempt per caller (%d)", attempts, callers)
	}
	for index, err := range results {
		if err == nil {
			t.Fatalf("caller %d was told it was watching after the subscribe failed", index)
		}
	}
	// Nothing holds the session: a refusal that leaves a reference behind is the
	// false subscription this test exists for.
	if refs := service.WatchedSessions()["s1"]; refs != 0 {
		t.Fatalf("a refused first watch left refcount %d", refs)
	}
	if service.sessionHeld("s1") {
		t.Fatal("a refused first watch left the session held")
	}

	// A retry after the official side recovers must be able to open it: the
	// failure cannot have poisoned the state.
	service.kap.mu.Lock()
	service.kap.watchHook = nil
	service.kap.mu.Unlock()
	if err := service.WatchSession("s1"); err != nil {
		t.Fatalf("retry after the refusal: %v", err)
	}
	if refs := service.WatchedSessions()["s1"]; refs != 1 {
		t.Fatalf("retry refcount = %d, want 1", refs)
	}
	service.UnwatchSession("s1")
	if refs, held := service.WatchedSessions()["s1"], service.sessionHeld("s1"); refs != 0 || held {
		t.Fatalf("releasing everything left refs=%d held=%v", refs, held)
	}
}

// A release that never held anything must not send a frame: teardown runs on
// every path, including ones that already released explicitly.
func TestReleasingAnInternallyHeldSessionSendsNothingWhenItWasNeverWatched(t *testing.T) {
	service, frames := watchService(t)
	if err := service.WatchSessionInternal("s1"); err != nil {
		t.Fatalf("internal watch: %v", err)
	}
	if !frames.waitFor(t, "subscribe", 3*time.Second) {
		t.Fatal("the internal interest never subscribed")
	}
	service.UnwatchSession("s1")
	time.Sleep(200 * time.Millisecond)
	if got := countFrames(frames.snapshot(), "unsubscribe"); got != 0 {
		t.Fatalf("a plugin release with no reference unsubscribed an internal holder: %d", got)
	}
}

// A v1 release must not throw away a cache another holder still needs: with the
// entry gone, applyFrame has nowhere to write and every later frame for that
// session is silently dropped, so the pane that stayed subscribed stops
// updating. The entry may only go when nothing watches the session any more.
func TestReleaseKeepsTheCacheAnotherHolderStillUses(t *testing.T) {
	service, _ := watchService(t)
	ctx := context.Background()
	if _, err := service.Action(ctx, "sessions.watch", json.RawMessage(`{"id":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Action(ctx, "sessions.watch", json.RawMessage(`{"id":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Action(ctx, "session.view", json.RawMessage(`{"id":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	_, before := service.cache["s1"]
	service.mu.Unlock()
	if !before {
		t.Fatal("the read did not populate the cache")
	}

	if _, err := service.Action(ctx, "session.unwatch", json.RawMessage(`{"id":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	_, kept := service.cache["s1"]
	service.mu.Unlock()
	if !kept {
		t.Fatal("a release dropped the cache while another holder remained")
	}

	// A frame that arrives now must still be applied, which is only observable
	// through the revision and the published view.
	service.mu.Lock()
	startRevision := service.cache["s1"].revision
	service.mu.Unlock()
	service.applyFrame(NativeFrame{Type: "goal", SessionID: "s1", Goal: json.RawMessage(`{"objective":"kept"}`)})
	service.mu.Lock()
	after := service.cache["s1"]
	service.mu.Unlock()
	if after.revision <= startRevision {
		t.Fatalf("the frame was dropped: revision stayed at %d", after.revision)
	}
	if !after.goalKnown || !strings.Contains(string(after.goal), "kept") {
		t.Fatalf("the frame was not applied: %#v", after.goal)
	}

	// The last release is what may drop it, and the next read rebuilds it.
	if _, err := service.Action(ctx, "session.unwatch", json.RawMessage(`{"id":"s1"}`)); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	_, gone := service.cache["s1"]
	service.mu.Unlock()
	if gone {
		t.Fatal("the last release left the cache behind")
	}
}
