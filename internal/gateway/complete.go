package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"enhancedkimi/internal/provider"
	"enhancedkimi/internal/usage"
)

// bufferedCompletion forwards one non-streaming request and echoes the upstream
// reply shape: text, reasoning and tool calls.
func (g *Gateway) bufferedCompletion(w http.ResponseWriter, r *http.Request, config provider.Config, model string, body map[string]any, messages []provider.Message, attribution usage.EventInput, accountID string) {
	attribution = withAccount(attribution, accountID)
	var reply provider.Reply
	err := g.dispatch(func() error {
		var err error
		reply, err = g.client.Complete(r.Context(), config, model, body, messages)
		return err
	})
	if err != nil {
		attribution.ObservationState = "PARTIAL"
		g.record(attribution, reply.Usage)
		upstreamFailure(w, g, err)
		return
	}
	g.record(attribution, reply.Usage)
	message := map[string]any{"role": "assistant", "content": reply.Text}
	if reply.Reasoning != "" {
		message["reasoning_content"] = reply.Reasoning
	}
	finish := reply.FinishReason
	if len(reply.ToolCalls) > 0 {
		message["tool_calls"] = reply.ToolCalls
		if finish == "" || finish == "stop" {
			finish = "tool_calls"
		}
	}
	if finish == "" {
		finish = "stop"
	}
	writeJSON(w, 200, map[string]any{
		"id":      "chatcmpl-" + shortID(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
		"usage":   usageOrNull(reply.Usage),
	})
}

func usageOrNull(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return value
}

// streamCompletion relays an upstream SSE stream.
//
// The whole generation runs inside the dispatch gate, not just its admission:
// a settings write that needs the exclusive permit must wait for every stream
// that was already admitted, instead of assuming an idle server.
//
// The response status is decided before committing: the stream is opened and
// the first delta is read while the header is still unset. An unsupported
// protocol, a buffered upstream, an upstream 401/429 or an immediate failure
// therefore returns a real error status instead of a 200 whose only content is
// an error.
func (g *Gateway) streamCompletion(w http.ResponseWriter, r *http.Request, config provider.Config, model string, body map[string]any, attribution usage.EventInput, accountID string) {
	attribution = withAccount(attribution, accountID)
	flusher, ok := w.(http.Flusher)
	if !ok {
		gatewayFail(w, 500, "STREAM_UNAVAILABLE", "当前连接不支持流式响应")
		return
	}
	streamID := "chatcmpl-" + shortID()
	created := time.Now().Unix()
	var usageRaw json.RawMessage
	var committed bool
	commit := func() {
		if committed {
			return
		}
		committed = true
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(200)
		flusher.Flush()
	}
	emit := func(event provider.StreamEvent) error {
		if len(event.Usage) > 0 && string(event.Usage) != "null" {
			usageRaw = event.Usage
		}
		// A pure usage chunk carries no choices. Forwarding it verbatim keeps
		// the protocol honest: no invented empty-delta choice, and the usage is
		// reported once, here, rather than appended again at the end.
		if len(event.Usage) > 0 && event.Text == "" && event.Reasoning == "" && event.Finish == "" && len(event.ToolCalls) == 0 && event.Role == "" {
			commit()
			return writeEvent(w, map[string]any{
				"id": streamID, "object": "chat.completion.chunk", "created": created, "model": model,
				"choices": []any{}, "usage": usageOrNull(event.Usage),
			})
		}
		commit()
		delta := map[string]any{}
		if event.Role != "" {
			delta["role"] = event.Role
		}
		if event.Reasoning != "" {
			delta["reasoning_content"] = event.Reasoning
		}
		if event.Text != "" {
			delta["content"] = event.Text
		}
		if len(event.ToolCalls) > 0 {
			delta["tool_calls"] = event.ToolCalls
		}
		var finish any
		if event.Finish != "" {
			finish = event.Finish
		}
		return writeEvent(w, map[string]any{
			"id": streamID, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		})
	}
	var streamErr error
	dispatchErr := g.dispatch(func() error {
		streamErr = g.client.OpenStream(r.Context(), config, model, body, emit, nil)
		return nil
	})
	if dispatchErr != nil {
		// Only safe mode can refuse here, and it does so before any byte.
		attribution.ObservationState = "PARTIAL"
		g.record(attribution, usageRaw)
		upstreamFailure(w, g, dispatchErr)
		return
	}
	if streamErr != nil && !committed {
		attribution.ObservationState = "PARTIAL"
		g.record(attribution, usageRaw)
		upstreamFailure(w, g, streamErr)
		return
	}
	if streamErr != nil {
		attribution.ObservationState = "PARTIAL"
		g.record(attribution, usageRaw)
		// The response is already committed; an in-band error keeps the client
		// from treating a truncated answer as complete.
		writeEvent(w, map[string]any{
			"error": map[string]any{
				"message": "上游流在完成前中断；本网关未重试，结果未知",
				"type":    "api_error",
				"code":    streamErrorCode(streamErr),
			},
		})
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}
	// OpenStream only succeeds with a finish_reason and [DONE], and the finish
	// chunk was already relayed with its own delta. Neither an extra empty
	// choice nor a second usage chunk is appended: the protocol terminator is
	// all that remains.
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
	attribution.ObservationState = "FINAL"
	g.record(attribution, usageRaw)
}

func streamErrorCode(err error) string {
	var api *provider.APIError
	if errors.As(err, &api) {
		return api.Code
	}
	if errors.Is(err, context.Canceled) {
		return "CLIENT_CANCELLED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "UPSTREAM_TIMEOUT"
	}
	return "UPSTREAM_STREAM_INTERRUPTED"
}

func writeEvent(w http.ResponseWriter, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

// withAccount carries the chosen account into the observation without losing
// the usage payload. The raw object keeps the protocol counts and adds the
// attribution field, so the ledger parses tokens and the account together.
func withAccount(attribution usage.EventInput, accountID string) usage.EventInput {
	if accountID == "" {
		return attribution
	}
	attribution.GatewayAccountID = accountID
	return attribution
}

// record writes one gateway observation into the existing usage ledger. A
// missing usage payload stays UNKNOWN; it is never turned into zeros.
func (g *Gateway) record(attribution usage.EventInput, raw json.RawMessage) {
	if g.usage == nil {
		return
	}
	merged, err := mergeUsageRaw(raw, attribution.GatewayAccountID)
	if err == nil {
		attribution.Raw = merged
	} else {
		attribution.Raw = raw
	}
	attribution.GatewayAccountID = ""
	if attribution.At == "" {
		attribution.At = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, _ = g.usage.Ingest([]usage.EventInput{attribution})
}

// mergeUsageRaw keeps the protocol usage object and adds the account id.
func mergeUsageRaw(raw json.RawMessage, accountID string) (json.RawMessage, error) {
	if accountID == "" {
		return raw, nil
	}
	merged := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &merged); err != nil {
			return nil, err
		}
	}
	merged["gatewayAccountId"] = accountID
	return json.Marshal(merged)
}

// PreviewConfig renders the configuration a client would need, without writing
// any file. The gateway token is shown, the supplier key never is.
func (g *Gateway) PreviewConfig(baseURL string) map[string]any {
	return map[string]any{
		"provider": map[string]any{
			"type":     "kimi",
			"base_url": strings.TrimRight(baseURL, "/") + "/v1",
			"api_key":  g.token,
		},
		"models":           "*",
		"writesUserConfig": false,
		"notes": []string{
			"此预览不会写入任何配置文件；接入由调用方自行完成。",
			"网关令牌只用于本机回环，供应商密钥留在凭据库。",
			"模型由客户端静态 [models] 配置声明，客户端不依赖 GET /v1/models。",
			"支持工具调用回环与 reasoning_content 透传；多账号自动调度仍留待后续阶段。",
		},
	}
}
