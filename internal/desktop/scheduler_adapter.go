package desktop

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"enhancedkimi/internal/scheduler"
)

type SchedulerExecutor struct {
	service        *NativeService
	recordActivity func(ctx context.Context, pluginID, detail string)
}

func (s *NativeService) NewSchedulerExecutor(recordActivity func(ctx context.Context, pluginID, detail string)) *SchedulerExecutor {
	return &SchedulerExecutor{
		service:        s,
		recordActivity: recordActivity,
	}
}

func (e *SchedulerExecutor) KAPConnected(ctx context.Context) (bool, string) {
	if _, err := e.service.kap.Request(ctx, http.MethodGet, "/config", nil); err != nil {
		return false, err.Error()
	}
	return true, ""
}

func (e *SchedulerExecutor) Models(ctx context.Context) ([]scheduler.ModelInfo, error) {
	items, err := e.service.kap.Models(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]scheduler.ModelInfo, 0, len(items))
	for _, item := range items {
		out = append(out, scheduler.ModelInfo{
			ID:             item.ID,
			Name:           item.Name,
			Capabilities:   append([]string(nil), item.Capabilities...),
			SupportEfforts: append([]string(nil), item.SupportEfforts...),
		})
	}
	return out, nil
}

func (e *SchedulerExecutor) Sessions(ctx context.Context) ([]scheduler.SessionInfo, error) {
	items, err := e.service.kap.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]scheduler.SessionInfo, 0, len(items))
	for _, item := range items {
		out = append(out, toSchedulerSession(item))
	}
	return out, nil
}

func (e *SchedulerExecutor) GetSession(ctx context.Context, id string) (scheduler.SessionInfo, error) {
	raw, err := e.service.kap.Request(ctx, http.MethodGet, "/sessions/"+url.PathEscape(id), nil)
	if err != nil {
		return scheduler.SessionInfo{}, err
	}
	item, err := mapNativeSession(raw)
	if err != nil {
		return scheduler.SessionInfo{}, err
	}
	return toSchedulerSession(item), nil
}

func (e *SchedulerExecutor) GetSessionTurns(ctx context.Context, id string, pageSize int) ([]scheduler.TurnInfo, error) {
	if pageSize <= 0 {
		pageSize = 20
	}
	turns, err := e.service.nativeTurns(ctx, url.PathEscape(id), pageSize)
	if err != nil {
		return nil, err
	}
	out := make([]scheduler.TurnInfo, 0, len(turns))
	for _, turn := range turns {
		info := scheduler.TurnInfo{TurnID: turn.TurnID}
		approvals := turnApprovals([]NativeTurn{turn})
		if len(approvals) > 0 {
			info.HasApproval = true
			info.ApprovalTitle = approvals[0].Title
		}
		if turn.Error != nil && turn.Error.Message != "" {
			info.ErrorMessage = turn.Error.Message
		}
		for _, item := range turn.Items {
			if item.Type == "assistant_message" {
				info.HasAssistantMsg = true
				break
			}
		}
		out = append(out, info)
	}
	return out, nil
}

func (e *SchedulerExecutor) CreateSession(ctx context.Context, cwd, title, model, effort, permissionMode string) (scheduler.SessionInfo, error) {
	body := map[string]any{"metadata": map[string]any{"cwd": cwd}}
	if title != "" {
		body["title"] = title
	}
	if model != "" {
		body["agent_config"] = map[string]any{"model": model}
	}
	raw, err := e.service.kap.Request(ctx, http.MethodPost, "/sessions", body)
	if err != nil {
		return scheduler.SessionInfo{}, err
	}
	sess, err := mapNativeSession(raw)
	if err != nil {
		return scheduler.SessionInfo{}, err
	}
	if model != "" || effort != "" || permissionMode != "" {
		agentConfig := map[string]any{}
		if model != "" {
			agentConfig["model"] = model
		}
		if permissionMode != "" {
			agentConfig["permission_mode"] = permissionMode
		}
		if effort != "" {
			agentConfig["thinking"] = effort
		}
		_, _ = e.service.kap.Request(ctx, http.MethodPost, "/sessions/"+url.PathEscape(sess.ID)+"/profile", map[string]any{
			"agent_config": agentConfig,
		})
	}
	return toSchedulerSession(sess), nil
}

func (e *SchedulerExecutor) SendPrompt(ctx context.Context, sessionID, text, model, effort, permissionMode string) error {
	input := map[string]any{
		"id":   sessionID,
		"text": text,
		"mode": "normal",
	}
	if model != "" {
		input["model"] = model
	}
	if effort != "" {
		input["thinking"] = effort
	}
	if permissionMode != "" {
		input["permissionMode"] = permissionMode
	}
	_, err := e.service.sendPrompt(ctx, input)
	return err
}

func (e *SchedulerExecutor) SessionCacheStats(ctx context.Context, sessionIDs []string) (map[string]scheduler.SessionCacheStat, bool, int) {
	settings, err := e.service.automationSettings(ctx)
	keepAliveEnabled := false
	keepAliveMinutes := 25
	if err == nil {
		keepAliveEnabled = settings.KeepAlive.Enabled
		keepAliveMinutes = settings.KeepAlive.Minutes
	}
	out := map[string]scheduler.SessionCacheStat{}
	if len(sessionIDs) == 0 {
		return out, keepAliveEnabled, keepAliveMinutes
	}
	store, err := e.service.cacheStore()
	if err != nil {
		return out, keepAliveEnabled, keepAliveMinutes
	}
	now := time.Now().UTC()
	_, _ = store.Scan(ctx, now, false)
	for _, id := range sessionIDs {
		if id == "" {
			continue
		}
		stat := scheduler.SessionCacheStat{
			KeepAliveEnabled: keepAliveEnabled,
			KeepAliveMinutes: keepAliveMinutes,
		}
		if live, lErr := store.Live(ctx, id); lErr == nil {
			stat.OpenBackground = live.OpenBackground
		}
		if last, seen, sErr := store.LastMainStep(ctx, id); sErr == nil && seen {
			at := last.At
			idle := now.Sub(at).Minutes()
			if idle < 0 {
				idle = 0
			}
			stat.LastRequestAt = &at
			stat.IdleMinutes = &idle
		}
		out[id] = stat
	}
	return out, keepAliveEnabled, keepAliveMinutes
}

func (e *SchedulerExecutor) PublishEvent(scope string, value any) {
	e.service.bus.Publish(scope, value)
}

func (e *SchedulerExecutor) RecordPluginActivity(ctx context.Context, pluginID, detail string) {
	if e.recordActivity != nil {
		e.recordActivity(ctx, pluginID, detail)
	}
}

func toSchedulerSession(s NativeSession) scheduler.SessionInfo {
	return scheduler.SessionInfo{
		ID:                 s.ID,
		Title:              s.Title,
		CWD:                s.CWD,
		UpdatedAt:          s.UpdatedAt,
		Busy:               s.Busy,
		MainTurnActive:     s.MainTurnActive,
		PendingInteraction: s.PendingInteraction,
		LastTurnReason:     s.LastTurnReason,
	}
}
