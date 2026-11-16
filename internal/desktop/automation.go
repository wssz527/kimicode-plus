package desktop

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// KeepAliveSettings configures the background-wait keepalive only.
//
// D8: this never applies to an idle session. Minutes counts from the main agent's
// own last model request, not from session or subagent activity.
type KeepAliveSettings struct {
	Enabled bool `json:"enabled"`
	Minutes int  `json:"minutes"`
}

// CompactionSettings configures idle auto-compaction, which replaces keepalive for
// idle sessions. It only ever runs when the session is neither busy nor active.
type CompactionSettings struct {
	Enabled          bool `json:"enabled"`
	IdleMinutes      int  `json:"idleMinutes"`
	ContextThreshold int  `json:"contextThreshold"`
}

type RecoveryEntry struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Status string `json:"status"`
	NextAt string `json:"nextAt,omitempty"`
	Note   string `json:"note,omitempty"`
}

type RecoverySettings struct {
	Enabled         bool            `json:"enabled"`
	IntervalSeconds int             `json:"intervalSeconds"`
	Entries         []RecoveryEntry `json:"entries"`
}

type AutomationSettings struct {
	KeepAlive  KeepAliveSettings  `json:"keepAlive"`
	Compaction CompactionSettings `json:"compaction"`
	Recovery   RecoverySettings   `json:"recovery"`
}

func defaultAutomationSettings() AutomationSettings {
	return AutomationSettings{
		KeepAlive:  KeepAliveSettings{Enabled: false, Minutes: 25},
		Compaction: CompactionSettings{Enabled: false, IdleMinutes: 25, ContextThreshold: 60},
		Recovery:   RecoverySettings{Enabled: false, IntervalSeconds: 60, Entries: []RecoveryEntry{}},
	}
}

func (s *NativeService) automationSettings(ctx context.Context) (AutomationSettings, error) {
	settings := defaultAutomationSettings()
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM native_state WHERE key='automation'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	if err = json.Unmarshal([]byte(raw), &settings); err != nil {
		return settings, errors.New("自动化设置无法读取，请重新保存")
	}
	if settings.Recovery.Entries == nil {
		settings.Recovery.Entries = []RecoveryEntry{}
	}
	return settings, validateAutomationSettings(settings)
}

// keepAliveMinMinutes / keepAliveMaxMinutes bound the main-agent inactivity
// threshold. The plan fixes the range at 5 to 60 minutes; the user's measured
// cache loss is around 30.
const (
	keepAliveMinMinutes = 5
	keepAliveMaxMinutes = 60
)

func validateAutomationSettings(settings AutomationSettings) error {
	if settings.KeepAlive.Minutes < keepAliveMinMinutes || settings.KeepAlive.Minutes > keepAliveMaxMinutes {
		return errors.New("保活阈值须为 5–60 分钟")
	}
	if settings.Compaction.IdleMinutes < 5 || settings.Compaction.IdleMinutes > 1440 {
		return errors.New("空闲阈值须为 5–1440 分钟")
	}
	if settings.Compaction.ContextThreshold < 1 || settings.Compaction.ContextThreshold > 100 {
		return errors.New("上下文阈值须为 1–100%")
	}
	if settings.Recovery.IntervalSeconds < 15 || settings.Recovery.IntervalSeconds > 3600 {
		return errors.New("额度检测间隔须为 15–3600 秒")
	}
	return nil
}

func (s *NativeService) storeAutomationSettings(ctx context.Context, settings AutomationSettings) error {
	if err := validateAutomationSettings(settings); err != nil {
		return err
	}
	if settings.Recovery.Entries == nil {
		settings.Recovery.Entries = []RecoveryEntry{}
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO native_state(key,data) VALUES('automation',?) ON CONFLICT(key) DO UPDATE SET data=excluded.data`, string(data))
	return err
}

func (s *NativeService) publishAutomationSettings(settings AutomationSettings) {
	s.bus.Publish("automation", settings)
	s.bus.Publish("recovery", settings.Recovery)
}

func (s *NativeService) saveAutomationSettings(ctx context.Context, payload json.RawMessage) (AutomationSettings, error) {
	s.automationMu.Lock()
	defer s.automationMu.Unlock()
	settings := defaultAutomationSettings()
	if err := json.Unmarshal(payload, &settings); err != nil {
		return settings, errors.New("自动化设置格式无效")
	}
	if err := s.storeAutomationSettings(ctx, settings); err != nil {
		return settings, err
	}
	s.publishAutomationSettings(settings)
	return settings, nil
}

func (s *NativeService) setRecovery(ctx context.Context, payload json.RawMessage) (RecoverySettings, error) {
	s.automationMu.Lock()
	defer s.automationMu.Unlock()
	settings, err := s.automationSettings(ctx)
	if err != nil {
		return settings.Recovery, err
	}
	var input struct {
		Enabled         *bool `json:"enabled"`
		IntervalSeconds *int  `json:"intervalSeconds"`
	}
	if err = json.Unmarshal(payload, &input); err != nil {
		return settings.Recovery, errors.New("额度恢复设置格式无效")
	}
	if input.Enabled != nil {
		settings.Recovery.Enabled = *input.Enabled
	}
	if input.IntervalSeconds != nil {
		settings.Recovery.IntervalSeconds = *input.IntervalSeconds
	}
	for index := range settings.Recovery.Entries {
		if settings.Recovery.Enabled {
			if settings.Recovery.Entries[index].NextAt == "" {
				settings.Recovery.Entries[index].NextAt = time.Now().UTC().Add(time.Duration(settings.Recovery.IntervalSeconds) * time.Second).Format(time.RFC3339)
			}
		} else {
			settings.Recovery.Entries[index].NextAt = ""
		}
	}
	if err = s.storeAutomationSettings(ctx, settings); err != nil {
		return settings.Recovery, err
	}
	s.publishAutomationSettings(settings)
	return settings.Recovery, nil
}

type automationIntent struct {
	ID              string
	Kind            string
	HostID          string
	SessionID       string
	ModelID         string
	ContextRevision string
	State           string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Result          string
}
