package desktop

import (
	"context"
	"sync"
	"time"
)

type Event struct {
	Sequence int64  `json:"sequence"`
	Scope    string `json:"scope"`
	Value    any    `json:"value"`
}

type EventPage struct {
	Events []Event `json:"events"`
	Next   int64   `json:"next"`
	Reset  bool    `json:"reset,omitempty"`
}

type EventBus struct {
	mu     sync.Mutex
	next   int64
	events []Event
	notify chan struct{}
}

func NewEventBus() *EventBus {
	return &EventBus{notify: make(chan struct{})}
}

// Publish appends one event and returns its sequence. Existing callers use it as
// a statement, so the return value is optional; an activity event that must name
// its own sequence uses it.
func (b *EventBus) Publish(scope string, value any) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	sequence := b.next
	b.events = append(b.events, Event{Sequence: sequence, Scope: scope, Value: value})
	if len(b.events) > 2000 {
		copy(b.events, b.events[len(b.events)-2000:])
		b.events = b.events[:2000]
	}
	close(b.notify)
	b.notify = make(chan struct{})
	return sequence
}

func (b *EventBus) Poll(ctx context.Context, after int64, scopes []string, wait time.Duration) EventPage {
	for {
		b.mu.Lock()
		oldest := b.next - int64(len(b.events)) + 1
		if after < 0 {
			after = b.next
		}
		page := EventPage{Events: []Event{}, Next: after, Reset: after > 0 && after < oldest-1}
		allowed := map[string]bool{}
		for _, scope := range scopes {
			allowed[scope] = true
		}
		for _, event := range b.events {
			if event.Sequence <= after {
				continue
			}
			if allowed[event.Scope] {
				page.Events = append(page.Events, event)
			}
			page.Next = event.Sequence
		}
		notify := b.notify
		b.mu.Unlock()
		if len(page.Events) > 0 || page.Reset || wait <= 0 {
			return page
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return page
		case <-notify:
			timer.Stop()
		case <-timer.C:
			return page
		}
	}
}
