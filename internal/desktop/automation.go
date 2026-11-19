package desktop

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// ProviderTTLSettings lets advanced users override the assumed cache TTL (in minutes)
// per provider class. Interval is derived by subtracting the safety margin.
type ProviderTTLSettings struct {
	SubscriptionMinutes int `json:"subscriptionMinutes"`
	OpenPlatformMinutes int `json:"openPlatformMinutes"`
	OtherMinutes        int `json:"otherMinutes"`
}

// IdleKeepAliveSettings configures optional idle-session cache keepalive (N4).
// Individual sessions can override the global switch and count.
type IdleKeepAliveSettings struct {
	Enabled         bool                `json:"enabled"`
	DefaultCount    int                 `json:"defaultCount"`
	MinQuotaPercent int                 `json:"minQuotaPercent"`
	ProviderTTL     ProviderTTLSettings `json:"providerTTL"`
}

// KeepAliveSettings configures the background-wait keepalive only.
//
// Waiting for a background subagent has no repeat limit and steers a minimal
// message into the running turn. Minutes counts from the main agent's own last
// model request, not from session or subagent activity.
type KeepAliveSettings struct {
	Enabled bool `json:"enabled"`
	Minutes int  `json:"minutes"`
}

// CompactionSettings configures idle auto-compaction. When idle keepalive is active
// for a session, compaction waits until keepalive finishes before taking over.
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
	KeepAlive     KeepAliveSettings     `json:"keepAlive"`
	IdleKeepAlive IdleKeepAliveSettings `json:"idleKeepAlive"`
	Compaction    CompactionSettings    `json:"compaction"`
	Recovery      RecoverySettings      `json:"recovery"`
}

func defaultProviderTTLSettings() ProviderTTLSettings {
	return ProviderTTLSettings{
		SubscriptionMinutes: 30,
		OpenPlatformMinutes: 5,
		OtherMinutes:        0,
	}
}

func defaultIdleKeepAliveSettings() IdleKeepAliveSettings {
	return IdleKeepAliveSettings{
		Enabled:         false,
		DefaultCount:    2,
		MinQuotaPercent: 10,
		ProviderTTL:     defaultProviderTTLSettings(),
	}
}

func defaultAutomationSettings() AutomationSettings {
	return AutomationSettings{
		KeepAlive:     KeepAliveSettings{Enabled: false, Minutes: 25},
		IdleKeepAlive: defaultIdleKeepAliveSettings(),
		Compaction:    CompactionSettings{Enabled: false, IdleMinutes: 25, ContextThreshold: 60},
		Recovery:      RecoverySettings{Enabled: false, IntervalSeconds: 60, Entries: []RecoveryEntry{}},
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

const (
	keepAliveMinMinutes = 5
	keepAliveMaxMinutes = 60

	idleKeepAliveMinCount = 1
	idleKeepAliveMaxCount = 20
	providerTTLMinMinutes = 2
	providerTTLMaxMinutes = 240
)

func validateAutomationSettings(settings AutomationSettings) error {
	if settings.KeepAlive.Minutes < keepAliveMinMinutes || settings.KeepAlive.Minutes > keepAliveMaxMinutes {
		return errors.New("保活阈值须为 5–60 分钟")
	}
	if settings.IdleKeepAlive.DefaultCount < idleKeepAliveMinCount || settings.IdleKeepAlive.DefaultCount > idleKeepAliveMaxCount {
		return errors.New("默认保活次数须为 1–20 次")
	}
	if settings.IdleKeepAlive.MinQuotaPercent < 0 || settings.IdleKeepAlive.MinQuotaPercent > 100 {
		return errors.New("额度停止阈值须为 0–100%")
	}
	ttl := settings.IdleKeepAlive.ProviderTTL
	if ttl.SubscriptionMinutes < providerTTLMinMinutes || ttl.SubscriptionMinutes > providerTTLMaxMinutes {
		return errors.New("订阅缓存时长须为 2–240 分钟")
	}
	if ttl.OpenPlatformMinutes < providerTTLMinMinutes || ttl.OpenPlatformMinutes > providerTTLMaxMinutes {
		return errors.New("开放平台缓存时长须为 2–240 分钟")
	}
	if ttl.OtherMinutes != 0 && (ttl.OtherMinutes < providerTTLMinMinutes || ttl.OtherMinutes > providerTTLMaxMinutes) {
		return errors.New("其他供应商缓存时长须为 0（不自动保活）或 2–240 分钟")
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
	settings, err := s.automationSettings(ctx)
	if err != nil {
		settings = defaultAutomationSettings()
	}
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(payload, &rawMap); err != nil {
		return settings, errors.New("自动化设置格式无效")
	}
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
