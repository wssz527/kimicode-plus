package server

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"enhancedkimi/internal/chat"
	"enhancedkimi/internal/desktop"
	"enhancedkimi/internal/gateway"
	"enhancedkimi/internal/provider"
)

// NativeOptions now carries the LaunchAgent settings in NativeOptions itself.
type AttachSurface struct{ Ready bool }

func newNativeIntegration(db *sql.DB, options NativeOptions, safe func() bool) (*desktop.NativeService, error) {
	service, err := desktop.NewNativeService(db, desktop.NewKAPClient(options.Home), options.CWD, safe)
	if err != nil {
		return nil, err
	}
	service.UseAttach(desktop.AttachConfig{
		DataDir: options.DataDir, KimiHome: options.Home, Profile: options.Profile,
		CoreURL: options.CoreURL, PairingToken: options.PairingToken, CoreVersion: Version, Executable: options.Executable,
	})
	return service, nil
}

type nativePlannerGenerator struct {
	gateway  *gateway.Gateway
	native   *desktop.NativeService
	dispatch func(func() error) error
}

func newPlannerGenerator(gw *gateway.Gateway, native *desktop.NativeService, dispatch func(func() error) error) chat.PlannerGenerator {
	if gw == nil && native == nil {
		return nil
	}
	return &nativePlannerGenerator{gateway: gw, native: native, dispatch: dispatch}
}

func (g *nativePlannerGenerator) Stream(
	ctx context.Context,
	model string,
	messages []provider.Message,
	tools []map[string]any,
	sessionID string,
	emit func(provider.StreamEvent) error,
) error {
	preferKAP := strings.Contains(model, "/")
	if preferKAP && g.native != nil {
		handled, err := g.streamKAP(ctx, model, messages, tools, emit)
		if handled {
			return err
		}
	}
	if g.gateway != nil {
		err := g.gateway.Stream(ctx, model, messages, tools, sessionID, emit)
		if err == nil || preferKAP || g.native == nil {
			return err
		}
		if handled, kapErr := g.streamKAP(ctx, model, messages, tools, emit); handled {
			return kapErr
		}
		return err
	}
	if g.native != nil {
		if handled, err := g.streamKAP(ctx, model, messages, tools, emit); handled {
			return err
		}
	}
	return errors.New("未找到可用的规划模型路由")
}

func (g *nativePlannerGenerator) streamKAP(
	ctx context.Context,
	model string,
	messages []provider.Message,
	tools []map[string]any,
	emit func(provider.StreamEvent) error,
) (bool, error) {
	if g.dispatch == nil {
		return g.native.StreamKAPModel(ctx, model, messages, tools, emit)
	}
	handled := false
	err := g.dispatch(func() error {
		var innerErr error
		handled, innerErr = g.native.StreamKAPModel(ctx, model, messages, tools, emit)
		return innerErr
	})
	if !handled && err != nil {
		// Safe mode or dispatch limit rejected before StreamKAPModel ran.
		return true, err
	}
	return handled, err
}
