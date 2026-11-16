package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"enhancedkimi/internal/desktop"
	"enhancedkimi/internal/plugins"
)

// pluginCore adapts this App's NativeService to the narrow interface the plugin
// host needs. Two things differ from the native service and are bridged here: the
// event page type and error translation. Keeping both in the adapter means the
// plugin package never imports desktop and desktop never imports plugins.
//
// The native service implements the plural session actions directly
// (`sessions.list/view/create/send`), so this adapter does not rename them:
// translating in two places would let the public contract and the accepted
// action drift apart again.
type pluginCore struct {
	service *desktop.NativeService
}

func (c pluginCore) Action(ctx context.Context, action string, payload json.RawMessage) (any, error) {
	value, err := c.service.Action(ctx, action, payload)
	if err == nil {
		return value, nil
	}
	var native *desktop.NativeError
	if errors.As(err, &native) {
		status := native.Status
		if status < 400 || status > 599 {
			status = http.StatusConflict
		}
		return nil, &plugins.Error{Status: status, Code: native.Code, Message: native.Message}
	}
	return nil, err
}

func (c pluginCore) Events() plugins.EventSource { return pluginEvents{bus: c.service.Events()} }

type pluginEvents struct {
	bus *desktop.EventBus
}

func (e pluginEvents) Poll(ctx context.Context, after int64, scopes []string, wait time.Duration) (plugins.EventPage, error) {
	page := e.bus.Poll(ctx, after, scopes, wait)
	out := plugins.EventPage{Events: make([]plugins.Event, 0, len(page.Events)), Next: page.Next, Reset: page.Reset}
	for _, event := range page.Events {
		out.Events = append(out.Events, plugins.Event{Sequence: event.Sequence, Scope: event.Scope, Value: event.Value})
	}
	return out, nil
}
