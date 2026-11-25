package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"enhancedkimi/internal/provider"
	"enhancedkimi/internal/security"
	"enhancedkimi/internal/usage"
)

var ErrPlannerModelNotFound = errors.New("没有供应商声明该规划模型；请先导入目录")

// Stream runs a routed provider turn for a planner thread and records usage.
func (g *Gateway) Stream(ctx context.Context, model string, messages []provider.Message, tools []map[string]any, sessionID string, emit func(provider.StreamEvent) error) error {
	providerID, target, qualified := strings.Cut(model, "/")
	if qualified {
		var exists bool
		err := g.client.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM registry_models WHERE provider_id=? AND model_id=?)`, providerID, target).Scan(&exists)
		if err != nil {
			return err
		}
		if exists {
			model = target
		} else {
			qualified = false
		}
	}
	if !qualified {
		var err error
		providerID, err = g.route(ctx, model)
		if err != nil {
			var routeErr errRoute
			if errors.As(err, &routeErr) && routeErr.code == "MODEL_NOT_FOUND" {
				return ErrPlannerModelNotFound
			}
			return err
		}
	}
	config, account, err := g.client.EffectiveConfig(ctx, providerID)
	if err != nil {
		return err
	}
	options, headers, err := g.plannerModelSettings(ctx, providerID, model)
	if err != nil {
		return err
	}
	config.CustomHeaders = headers
	attribution := usage.EventInput{
		ProviderID: providerID, Model: model, RequestID: security.Random()[:24], AttemptID: "1",
		SessionID: sessionID, AgentID: "planner", Role: "main", RequestKind: "chat",
		Protocol: config.Protocol, Source: "gateway", At: time.Now().UTC().Format(time.RFC3339Nano),
		GatewayAccountID: account.AccountID,
	}
	var usageRaw json.RawMessage
	err = g.dispatch(func() error {
		return g.client.StreamPlanner(ctx, config, model, options, messages, tools, func(event provider.StreamEvent) error {
			if len(event.Usage) > 0 && string(event.Usage) != "null" {
				usageRaw = event.Usage
			}
			return emit(event)
		})
	})
	attribution.ObservationState = "FINAL"
	if err != nil {
		attribution.ObservationState = "PARTIAL"
	}
	g.record(attribution, usageRaw)
	return err
}

func (g *Gateway) plannerModelSettings(ctx context.Context, providerID, model string) (provider.PlannerModelOptions, map[string]string, error) {
	var raw string
	var output sql.NullFloat64
	var thinking sql.NullBool
	err := g.client.DB.QueryRowContext(ctx, `SELECT m.metadata_json, output.value_number, thinking.value_boolean
		FROM registry_models m
		LEFT JOIN registry_capabilities output ON output.provider_id=m.provider_id AND output.model_id=m.model_id AND output.field='output'
		LEFT JOIN registry_capabilities thinking ON thinking.provider_id=m.provider_id AND thinking.model_id=m.model_id AND thinking.field='thinking'
		WHERE m.provider_id=? AND m.model_id=?`, providerID, model).Scan(&raw, &output, &thinking)
	if err != nil {
		return provider.PlannerModelOptions{}, nil, err
	}
	var metadata struct {
		Thinking         string            `json:"thinking"`
		AdaptiveThinking bool              `json:"adaptiveThinking"`
		DefaultEffort    string            `json:"defaultEffort"`
		SupportEfforts   []string          `json:"supportEfforts"`
		OffEffort        string            `json:"offEffort"`
		MaxOutputSize    int64             `json:"maxOutputSize"`
		CustomHeaders    map[string]string `json:"customHeaders"`
	}
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return provider.PlannerModelOptions{}, nil, err
	}
	options := provider.PlannerModelOptions{
		Thinking: metadata.Thinking, AdaptiveThinking: metadata.AdaptiveThinking,
		DefaultEffort: metadata.DefaultEffort, SupportEfforts: metadata.SupportEfforts,
		OffEffort: metadata.OffEffort, MaxOutput: metadata.MaxOutputSize,
	}
	if options.MaxOutput == 0 && output.Valid {
		options.MaxOutput = int64(output.Float64)
	}
	if options.Thinking == "" && thinking.Valid {
		if thinking.Bool {
			options.Thinking = "toggle"
		} else {
			options.Thinking = "none"
		}
	}
	return options, metadata.CustomHeaders, nil
}
