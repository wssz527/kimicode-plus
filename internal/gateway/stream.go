package gateway

import (
	"context"
	"encoding/json"
	"time"

	"enhancedkimi/internal/provider"
	"enhancedkimi/internal/security"
	"enhancedkimi/internal/usage"
)

// Stream runs the same routed provider stream as the HTTP gateway for an
// internal planning thread. It returns only after the upstream turn is final.
func (g *Gateway) Stream(ctx context.Context, model string, messages []provider.Message, tools []map[string]any, sessionID string, emit func(provider.StreamEvent) error) error {
	providerID, err := g.route(ctx, model)
	if err != nil {
		return err
	}
	config, account, err := g.client.EffectiveConfig(ctx, providerID)
	if err != nil {
		return err
	}
	body := map[string]any{"model": model, "messages": messages, "stream": true, "stream_options": map[string]any{"include_usage": true}}
	if len(tools) > 0 {
		body["tools"] = tools
		body["tool_choice"] = "auto"
	}
	attribution := usage.EventInput{
		ProviderID: providerID, Model: model, RequestID: security.Random()[:24], AttemptID: "1",
		SessionID: sessionID, AgentID: "planner", Role: "main", RequestKind: "chat",
		Protocol: config.Protocol, Source: "gateway", At: time.Now().UTC().Format(time.RFC3339Nano),
		GatewayAccountID: account.AccountID,
	}
	var usageRaw json.RawMessage
	err = g.dispatch(func() error {
		return g.client.OpenStream(ctx, config, model, body, func(event provider.StreamEvent) error {
			if len(event.Usage) > 0 && string(event.Usage) != "null" {
				usageRaw = event.Usage
			}
			return emit(event)
		}, nil)
	})
	attribution.ObservationState = "FINAL"
	if err != nil {
		attribution.ObservationState = "PARTIAL"
	}
	g.record(attribution, usageRaw)
	return err
}
