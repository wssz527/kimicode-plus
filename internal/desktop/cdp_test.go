package desktop

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// An occupied inspector port is refused before any signal is sent, and the
// message has to name the likely owners: the previous wording ("拒绝附着未知进程")
// left the user with no way to tell a stale debug session from their own editor.
func TestAttachRefusesOccupiedInspectorPortWithActionableMessage(t *testing.T) {
	if !hostPlatformSupported {
		t.Skip("only supported hosts open the inspector port")
	}
	if !inspectorOpen(context.Background()) {
		listener, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(InspectorPort))
		if err != nil {
			t.Skipf("cannot bind the inspector port in this environment: %v", err)
		}
		defer listener.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Attach(ctx, 1)
	if err == nil {
		t.Fatal("attach accepted a port it does not own")
	}
	for _, want := range []string{"9229", "VS Code", "重启 Kimi Code"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("occupied-port error does not mention %q: %v", want, err)
		}
	}
}

// With nothing listening, the cleanup is a no-op rather than an error: a port
// that never opened is already in the state the caller wants.
func TestInspectorCleanupIsQuietWhenNoPortIsOpen(t *testing.T) {
	ctx := context.Background()
	if inspectorOpen(ctx) {
		t.Skip("an inspector is already listening on this machine")
	}
	if err := closeInspectorPort(ctx, 1); err != nil {
		t.Fatalf("cleanup of a closed port reported a failure: %v", err)
	}
	if err := ensureWindowsInspectorClosed(InspectorPort, 1, nil); err != nil {
		t.Fatalf("windows cleanup of a closed port reported a failure: %v", err)
	}
}

// A port owned by another process is never closed: the cleanup verifies
// ownership first, and reporting the unresolved state is the correct outcome
// rather than acting on a process this attach never signalled.
func TestEnsureWindowsInspectorClosedLeavesForeignPortsAlone(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	verify := func(context.Context) error { return errForeignInspectorOwner }
	err = ensureWindowsInspectorClosed(port, 4242, verify)
	if err == nil {
		t.Fatal("cleanup claimed success on a port it could not verify")
	}
	if !strings.Contains(err.Error(), inspectorCloseGuidance) || !strings.Contains(err.Error(), "正常退出") {
		t.Fatalf("cleanup failure does not guide the user: %v", err)
	}
	if conn, dialErr := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 250*time.Millisecond); dialErr != nil {
		t.Fatalf("cleanup closed a port it did not own: %v", dialErr)
	} else {
		conn.Close()
	}
}

type staticError string

func (e staticError) Error() string { return string(e) }

const errForeignInspectorOwner = staticError("调试端口的监听者不是预期宿主进程")

// The cleanup is also correct when the process already exited: nothing listens,
// nothing is touched, and no guidance error is produced.
func TestEnsureWindowsInspectorClosedAcceptsAlreadyClosedPort(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	// A closed port needs neither verification nor a target.
	if err := ensureWindowsInspectorClosed(port, 4242, func(context.Context) error { t.Fatal("verification ran for a closed port"); return nil }); err != nil {
		t.Fatalf("closed port was reported as an outstanding failure: %v", err)
	}
}

// V8's inspector.close() drops the WebSocket from the server side immediately,
// making conn.Close return StatusNoStatusRcvd on the normal path. Close must
// succeed once the temporary inspector port is actually gone.
func TestCDPCloseSucceedsWhenPeerDropsWebSocketAndPortIsGone(t *testing.T) {
	if inspectorOpen(context.Background()) {
		t.Skip("an inspector is already listening on port 9229")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_, _, _ = conn.Read(r.Context())
		_ = conn.Close(websocket.StatusGoingAway, "")
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.Dial(t.Context(), wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	cdp := &CDP{pid: 4242, conn: conn, pending: map[int]chan cdpResponse{}, closed: make(chan struct{})}
	go cdp.read()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := cdp.Close(ctx); err != nil {
		t.Fatalf("CDP.Close failed even though inspector port was closed: %v", err)
	}
}
