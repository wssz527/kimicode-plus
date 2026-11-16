package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWriteBudgetSeparatesChatFromJSON(t *testing.T) {
	d := Deadlines{}
	if got := d.budget("/enhance/v1/chat/threads/t1/messages"); got != ChatWriteBudget {
		t.Fatalf("chat thread message budget %s", got)
	}
	if got := d.budget("/v1/chat/completions"); got != ChatWriteBudget {
		t.Fatalf("gateway completion budget %s", got)
	}
	if got := d.budget("/enhance/v1/providers/kimi/discover"); got != DefaultWriteBudget {
		t.Fatalf("discovery budget %s", got)
	}
	if got := d.budget("/enhance/v1/models"); got != DefaultWriteBudget {
		t.Fatalf("models budget %s", got)
	}
	if ChatWriteBudget <= DefaultWriteBudget {
		t.Fatal("chat budget must exceed the default JSON budget")
	}
}

// The server must not carry a global WriteTimeout that would cut a stream, and
// the wrapper must stay transparent when the ResponseWriter cannot set
// deadlines (httptest recorders, wrapped writers).
func TestDeadlineHandlerKeepsResponsesIntact(t *testing.T) {
	called := false
	h := Deadlines{}.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/enhance/v1/chat/threads/t/messages", nil))
	if !called || w.Code != 200 || w.Body.String() != `{"ok":true}` {
		t.Fatalf("handler altered response: %d %s", w.Code, w.Body.String())
	}
	if err := ExtendWriteDeadline(w, time.Second); err == nil {
		t.Log("recorder accepted a deadline; streaming extension is best effort")
	}
}
