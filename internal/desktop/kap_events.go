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
	InteractionID string             `json:"interaction_id"`
	Input         any                `json:"input"`
	InputText     string             `json:"input_text"`
	Display       json.RawMessage    `json:"display"`
}

type NativeTurn struct {
	TurnID string           `json:"turn_id"`
	Items  []NativeItem     `json:"items"`
	Error  *NativeTurnError `json:"error"`
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
	for ctx.Err() == nil {
		endpoint, token, err := k.websocketURL()
		if err == nil {
			var conn *websocket.Conn
			conn, _, err = websocket.Dial(ctx, endpoint, &websocket.DialOptions{Subprotocols: []string{"kimi-code.bearer." + token}, HTTPHeader: http.Header{"User-Agent": []string{"EKD/0.3"}}})
			if err == nil {
				conn.SetReadLimit(8 << 20)
				k.mu.Lock()
				k.socket = conn
				watched := make([]string, 0, len(k.watched))
				for id := range k.watched {
					watched = append(watched, id)
				}
				k.mu.Unlock()
				for _, id := range watched {
					_ = k.sendFrame(map[string]any{"type": "subscribe", "request_id": id, "session_id": id})
				}
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
			}
		}
		k.mu.Lock()
		if k.socket != nil {
			_ = k.socket.Close(websocket.StatusNormalClosure, "reconnect")
			k.socket = nil
		}
		k.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(1500 * time.Millisecond):
		}
	}
}
