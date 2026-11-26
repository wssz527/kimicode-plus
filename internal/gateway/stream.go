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
	options, err := g.plannerModelSettings(ctx, providerID, model)
	if err != nil {
		return err
	}
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

// plannerModelSettings reads the planner knobs the registry actually produces:
// the per-model `output` and `thinking` capability rows imported with the
// catalog. Production metadata_json writes only autoName, manualName and
// autoCapabilities, so effort levels, thinking adaptations and custom headers
// have no registry source — those knobs exist only on the desktop model config
// that desktop.StreamKAPModel reads directly.
func (g *Gateway) plannerModelSettings(ctx context.Context, providerID, model string) (provider.PlannerModelOptions, error) {
	var output sql.NullFloat64
	var thinking sql.NullBool
	err := g.client.DB.QueryRowContext(ctx, `SELECT output.value_number, thinking.value_boolean
		FROM registry_models m
		LEFT JOIN registry_capabilities output ON output.provider_id=m.provider_id AND output.model_id=m.model_id AND output.field='output'
		LEFT JOIN registry_capabilities thinking ON thinking.provider_id=m.provider_id AND thinking.model_id=m.model_id AND thinking.field='thinking'
		WHERE m.provider_id=? AND m.model_id=?`, providerID, model).Scan(&output, &thinking)
	if err != nil {
		return provider.PlannerModelOptions{}, err
	}
	var options provider.PlannerModelOptions
	if output.Valid {
		options.MaxOutput = int64(output.Float64)
	}
	if thinking.Valid {
		if thinking.Bool {
			options.Thinking = "toggle"
		} else {
			options.Thinking = "none"
		}
	}
	return options, nil
}
