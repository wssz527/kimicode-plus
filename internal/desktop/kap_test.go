package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// writeInstance mirrors what the official client writes on every launch: one
// instance record carrying the port, plus the token file next to it.
func writeInstance(t *testing.T, home string, name string, port int, token string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "server", "instances"), 0o700); err != nil {
		t.Fatal(err)
	}
	record := map[string]any{"port": port, "host": "127.0.0.1", "heartbeat_at": time.Now().Unix()}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, "server", "instances", name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, "server.token"), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
}

func kapServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func portOf(t *testing.T, server *httptest.Server) int {
	t.Helper()
	index := strings.LastIndex(server.URL, ":")
	port, err := strconv.Atoi(server.URL[index+1:])
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func TestKAPRequestRecoversAfterPortChange(t *testing.T) {
	home := t.TempDir()
	first := kapServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeNativeTestEnvelope(w, map[string]any{"from": "first"})
	})
	writeInstance(t, home, "first.json", portOf(t, first), "token-one")
	kap := NewKAPClient(home)
	raw, err := kap.Request(context.Background(), http.MethodGet, "/sessions", nil)
	if err != nil {
		t.Fatalf("first resolve failed: %v", err)
	}
	if !strings.Contains(string(raw), "first") {
		t.Fatalf("unexpected payload: %s", raw)
	}
	// The client quit and relaunched on a new port with a fresh token.
	first.Close()
	second := kapServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token-two" {
			t.Errorf("stale token reused: %q", got)
		}
		writeNativeTestEnvelope(w, map[string]any{"from": "second"})
	})
	os.RemoveAll(filepath.Join(home, "server", "instances"))
	writeInstance(t, home, "second.json", portOf(t, second), "token-two")
	raw, err = kap.Request(context.Background(), http.MethodGet, "/sessions", nil)
	if err != nil {
		t.Fatalf("port change was not recovered: %v", err)
	}
	if !strings.Contains(string(raw), "second") {
		t.Fatalf("stale endpoint answered: %s", raw)
	}
}

func TestKAPRequestRecoversFromUnauthorized(t *testing.T) {
	home := t.TempDir()
	var calls atomic.Int32
	server := kapServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer fresh" {
			t.Errorf("token was not re-read: %q", got)
		}
		writeNativeTestEnvelope(w, map[string]any{"ok": true})
	})
	writeInstance(t, home, "instance.json", portOf(t, server), "stale")
	kap := NewKAPClient(home)
	// Prime the cache from disk, then rotate the token the way a relaunch does.
	if _, _, err := kap.endpoint(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "server.token"), []byte("fresh"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := kap.Request(context.Background(), http.MethodGet, "/sessions", nil); err != nil {
		t.Fatalf("401 was not recovered: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected exactly one retry, saw %d calls", calls.Load())
	}
}

func TestKAPRequestDoesNotRepeatUnsafePOSTAfterBrokenResponse(t *testing.T) {
	home := t.TempDir()
	var calls atomic.Int32
	server := kapServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// Promise a body, then drop the connection mid-response. The request did
		// reach the server, so a prompt must never be sent a second time.
		w.Header().Set("Content-Length", "512")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0`))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	})
	writeInstance(t, home, "instance.json", portOf(t, server), "token")
	kap := NewKAPClient(home)
	if _, err := kap.Request(context.Background(), http.MethodPost, "/sessions/s1/prompts", map[string]any{"text": "hi"}); err == nil {
		t.Fatal("broken response was reported as success")
	}
	if calls.Load() != 1 {
		t.Fatalf("unsafe POST was resent %d times", calls.Load())
	}
}

func TestKAPRequestRetriesIdempotentGETAfterBrokenResponse(t *testing.T) {
	home := t.TempDir()
	var calls atomic.Int32
	server := kapServer(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Length", "512")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"code":0`))
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		writeNativeTestEnvelope(w, map[string]any{"ok": true})
	})
	writeInstance(t, home, "instance.json", portOf(t, server), "token")
	kap := NewKAPClient(home)
	if _, err := kap.Request(context.Background(), http.MethodGet, "/sessions", nil); err != nil {
		t.Fatalf("idempotent retry failed: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected one retry, saw %d calls", calls.Load())
	}
}

func TestKAPReconnectRestoresWatchSubscriptions(t *testing.T) {
	home := t.TempDir()
	var mu sync.Mutex
	subscribes := []string{}
	dials := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != NativeAPI+"/ws" {
			http.NotFound(w, r)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		mu.Lock()
		dials++
		attempt := dials
		mu.Unlock()
		// The first connection drops right after subscribing; the second stays up
		// long enough for the test to read the recovered subscription.
		deadline := time.Now().Add(6 * time.Second)
		if attempt == 1 {
			deadline = time.Now().Add(2 * time.Second)
		}
		for time.Now().Before(deadline) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_, data, readErr := conn.Read(ctx)
			cancel()
			if readErr != nil {
				return
			}
			var frame map[string]any
			if json.Unmarshal(data, &frame) != nil {
				continue
			}
			if frame["type"] == "subscribe" {
				id, _ := frame["session_id"].(string)
				mu.Lock()
				subscribes = append(subscribes, id)
				mu.Unlock()
			}
			if attempt == 1 {
				return
			}
		}
	}))
	defer server.Close()
	writeInstance(t, home, "instance.json", portOf(t, server), "token")
	kap := NewKAPClient(home)
	kap.backoffMin = 10 * time.Millisecond
	kap.backoffMax = 40 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := kap.Watch("session-1"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); kap.RunEvents(ctx, func(NativeFrame) {}) }()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(subscribes)
		mu.Unlock()
		if count >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(subscribes) < 2 {
		t.Fatalf("subscription was not restored after reconnect: %v", subscribes)
	}
	if subscribes[0] != "session-1" || subscribes[1] != "session-1" {
		t.Fatalf("unexpected subscriptions: %v", subscribes)
	}
}

func TestKAPBackoffGrowsWhileEndpointIsUnreachable(t *testing.T) {
	home := t.TempDir()
	// Point the client at a closed port: every dial fails without connecting.
	server := kapServer(t, func(w http.ResponseWriter, r *http.Request) {})
	port := portOf(t, server)
	server.Close()
	writeInstance(t, home, "instance.json", port, "token")
	kap := NewKAPClient(home)
	kap.backoffMin = 5 * time.Millisecond
	kap.backoffMax = 20 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	start := time.Now()
	kap.RunEvents(ctx, func(NativeFrame) {})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("unreachable endpoint blocked shutdown for %s", elapsed)
	}
}
