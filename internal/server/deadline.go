package server

import (
	"net/http"
	"strings"
	"time"
)

// DefaultWriteBudget bounds ordinary JSON responses. Chat completion and the
// local gateway get a generation budget instead: a single reply can take
// longer than a catalog request, and SSE keeps writing for the whole stream.
const (
	DefaultWriteBudget = 60 * time.Second
	ChatWriteBudget    = 15 * time.Minute
)

// Deadlines applies a per-request write deadline. The http.Server itself must
// not carry WriteTimeout, because one global value cannot both cut a stuck
// JSON reply quickly and allow a long streamed generation.
type Deadlines struct {
	Default time.Duration
	Chat    time.Duration
}

func (d Deadlines) budget(path string) time.Duration {
	chat := d.Chat
	if chat <= 0 {
		chat = ChatWriteBudget
	}
	standard := d.Default
	if standard <= 0 {
		standard = DefaultWriteBudget
	}
	switch {
	case strings.HasSuffix(path, "/chat/completions"):
		return chat
	case strings.HasPrefix(path, "/enhance/v1/chat/threads/") && strings.HasSuffix(path, "/messages"):
		return chat
	}
	return standard
}

// Handler sets the deadline before the handler runs. Streaming handlers extend
// it per flush through ExtendWriteDeadline.
func (d Deadlines) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		if err := controller.SetWriteDeadline(time.Now().Add(d.budget(r.URL.Path))); err != nil {
			// The recorder used by tests and wrappers may not support
			// deadlines; the handler still runs with the server default.
			_ = err
		}
		next.ServeHTTP(w, r)
	})
}

// ExtendWriteDeadline pushes the deadline forward while a stream is active.
// A handler that cannot set deadlines keeps the server default.
func ExtendWriteDeadline(w http.ResponseWriter, budget time.Duration) error {
	if budget <= 0 {
		budget = ChatWriteBudget
	}
	return http.NewResponseController(w).SetWriteDeadline(time.Now().Add(budget))
}
