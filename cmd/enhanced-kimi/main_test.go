package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"enhancedkimi/internal/server"
)

// A server-wide WriteTimeout would cap every generation at one value. The chat
// path needs its own, larger budget, so the global field must stay unset.
func TestServerHasNoGlobalWriteTimeout(t *testing.T) {
	srv := newHTTPServer(http.NotFoundHandler())
	if srv.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout %s would truncate a long completion", srv.WriteTimeout)
	}
	if srv.ReadHeaderTimeout == 0 || srv.ReadTimeout == 0 {
		t.Fatal("request read budgets must stay bounded")
	}
	if srv.MaxHeaderBytes != 16<<10 {
		t.Fatalf("header cap %d", srv.MaxHeaderBytes)
	}
}

// The chat write budget has to outlive both the old 60-second global cap and
// the shared provider chat budget, otherwise the socket closes mid-generation.
func TestChatWriteBudgetOutlivesLegacyCap(t *testing.T) {
	if server.ChatWriteBudget <= 60*time.Second {
		t.Fatalf("chat write budget %s does not exceed the old 60s cap", server.ChatWriteBudget)
	}
	if server.DefaultWriteBudget > server.ChatWriteBudget {
		t.Fatalf("default budget %s exceeds the chat budget %s", server.DefaultWriteBudget, server.ChatWriteBudget)
	}
}

// A slow chat handler must keep writing past the old 60-second cap on a real
// connection. The delay is scaled to prove the mechanism: the handler extends
// its deadline per write and the response survives a client read timeout that
// is shorter than the legacy cap but longer than the extension unit.
func TestChatResponseSurvivesServerWriteDeadline(t *testing.T) {
	const extension = 150 * time.Millisecond
	handler := server.Deadlines{Chat: extension, Default: 60 * time.Second}.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer cannot flush")
			return
		}
		for i := 0; i < 4; i++ {
			time.Sleep(extension / 2)
			if err := server.ExtendWriteDeadline(w, extension); err != nil {
				t.Errorf("extend write deadline: %v", err)
				return
			}
			w.Write([]byte("x"))
			flusher.Flush()
		}
	}))
	srv := newHTTPServer(handler)
	remote := httptest.NewServer(srv.Handler)
	defer remote.Close()
	client := &http.Client{Timeout: 4 * extension}
	res, err := client.Get(remote.URL + "/enhance/v1/chat/threads/t1/messages")
	if err != nil {
		t.Fatalf("chat response was cut at the first deadline: %v", err)
	}
	defer res.Body.Close()
	buf := make([]byte, 8)
	n, _ := res.Body.Read(buf)
	if n == 0 {
		t.Fatal("no bytes streamed back")
	}
}
