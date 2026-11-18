package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

type NativeTurnError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	StatusCode int    `json:"status_code"`
}

type NativeItem struct {
	ItemID        string             `json:"item_id"`
	CreatedAt     string             `json:"created_at,omitempty"`
	Attachments   []NativeAttachment `json:"attachments,omitempty"`
	Type          string             `json:"type"`
	Text          string             `json:"text"`
	Output        any                `json:"output"`
	Error         any                `json:"error"`
	Name          string             `json:"name"`
	Stage         string             `json:"stage"`
	Status        string             `json:"status"`
	Outcome       string             `json:"outcome"`
	InteractionID string             `json:"interaction_id"`
	Input         any                `json:"input"`
	InputText     string             `json:"input_text"`
	Display       json.RawMessage    `json:"display"`
}

type NativeTurn struct {
	TurnID string `json:"turn_id"`
	// Ordinal is the official page cursor. Older turns carry a smaller ordinal,
	// so a history page asks for the turns below the smallest one it already has.
	Ordinal *int64           `json:"ordinal,omitempty"`
	Items   []NativeItem     `json:"items"`
	Error   *NativeTurnError `json:"error"`
}

type NativeFrame struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id"`
	AgentID   string          `json:"agent_id"`
	TurnID    string          `json:"turn_id"`
	Turn      NativeTurn      `json:"turn"`
	Turns     []NativeTurn    `json:"turns"`
	Item      NativeItem      `json:"item"`
	ItemID    string          `json:"item_id"`
	Set       json.RawMessage `json:"set"`
	Field     string          `json:"field"`
	Delta     string          `json:"delta"`
	TurnIDs   []string        `json:"turn_ids"`
	Goal      json.RawMessage `json:"goal"`
}

func (k *KAPClient) sendFrame(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	k.writeMu.Lock()
	defer k.writeMu.Unlock()
	if k.socket == nil {
		return errors.New("官方事件连接尚未建立")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return k.socket.Write(ctx, websocket.MessageText, data)
}

func (k *KAPClient) Watch(id string) error {
	if id == "" {
		return errors.New("缺少会话 ID")
	}
	// Tests replace the official side with a refusing hook so a failed subscribe
	// can be exercised without a socket: the rollback it triggers is the whole
	// point of serializing the transition.
	k.mu.Lock()
	hook := k.watchHook
	k.mu.Unlock()
	if hook != nil {
		if err := hook(id); err != nil {
			return err
		}
	}
	k.mu.Lock()
	k.watched[id] = true
	connected := k.socket != nil
	k.mu.Unlock()
	if connected {
		return k.sendFrame(map[string]any{"type": "subscribe", "request_id": id, "session_id": id})
	}
	return nil
}

func (k *KAPClient) Unwatch(id string) {
	k.mu.Lock()
	delete(k.watched, id)
	connected := k.socket != nil
	k.mu.Unlock()
	if connected {
		_ = k.sendFrame(map[string]any{"type": "unsubscribe", "request_id": id, "session_id": id})
	}
}

func (k *KAPClient) RunEvents(ctx context.Context, onFrame func(NativeFrame)) {
	backoff := k.backoffMin
	for ctx.Err() == nil {
		connected, err := k.runEventConnection(ctx, onFrame)
		if ctx.Err() != nil {
			return
		}
		// A socket that carried traffic earns a fresh short backoff. A socket
		// that never opened keeps doubling, so a quit official client is found
		// within seconds instead of being polled every 1.5s forever.
		if connected {
			backoff = k.backoffMin
		} else if err != nil && k.home != "" {
			// The recorded port and token belong to the process that wrote them.
			k.invalidate()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < k.backoffMax {
			if backoff *= 2; backoff > k.backoffMax {
				backoff = k.backoffMax
			}
		}
	}
}

// runEventConnection keeps one socket alive and reports whether it ever became
// usable, so the caller can decide how far to back off.
func (k *KAPClient) runEventConnection(ctx context.Context, onFrame func(NativeFrame)) (bool, error) {
	endpoint, token, err := k.websocketURL()
	if err != nil {
		return false, err
	}
	conn, _, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{Subprotocols: []string{"kimi-code.bearer." + token}, HTTPHeader: http.Header{"User-Agent": []string{"EKD/0.3"}}})
	if err != nil {
		return false, err
	}
	conn.SetReadLimit(8 << 20)
	k.mu.Lock()
	k.socket = conn
	watched := make([]string, 0, len(k.watched))
	for id := range k.watched {
		watched = append(watched, id)
	}
	k.mu.Unlock()
	for _, id := range watched {
		if err = k.sendFrame(map[string]any{"type": "subscribe", "request_id": id, "session_id": id}); err != nil {
			break
		}
	}
	subscribed := err == nil
	for ctx.Err() == nil {
		_, data, readErr := conn.Read(ctx)
		if readErr != nil {
			break
		}
		frames := []NativeFrame{}
		if len(data) > 0 && data[0] == '[' {
			if json.Unmarshal(data, &frames) != nil {
				continue
			}
		} else {
			var frame NativeFrame
			if json.Unmarshal(data, &frame) != nil {
				continue
			}
			frames = append(frames, frame)
		}
		for _, frame := range frames {
			if frame.Type == "ping" {
				_ = k.sendFrame(map[string]any{"type": "pong"})
				continue
			}
			onFrame(frame)
		}
	}
	k.mu.Lock()
	if k.socket == conn {
		_ = conn.Close(websocket.StatusNormalClosure, "reconnect")
		k.socket = nil
	}
	k.mu.Unlock()
	if !subscribed {
		return false, errors.New("官方事件连接未能恢复订阅")
	}
	return true, nil
}
