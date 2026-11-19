package desktop

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"enhancedkimi/internal/cache"
	"enhancedkimi/internal/storage"
)

// idleKeepAlivePrompt asks the model for a single-character reply with no tool use
// or extended reasoning. The turn is undone via POST /sessions/{id}:undo once it
// settles, leaving the conversation history identical while refreshing cache TTL.
const idleKeepAlivePrompt = "[保活]仅回复1"

const (
	ProviderTypeSubscription = "subscription"
	ProviderTypeOpenPlatform = "open_platform"
	ProviderTypeOther        = "other"

	IdleModeDefault = "default"
	IdleModeOff     = "off"
	IdleModeCount   = "count"
	IdleModeUntil   = "until"

	KeepAliveStateActive          = "active"
	KeepAliveStateWaitingSubagent = "waiting_subagent"
	KeepAliveStateIdleKeepAlive   = "idle_keepalive"
	KeepAliveStateIdleCompaction  = "idle_compaction"
	KeepAliveStateStopped         = "stopped"
)

type ResolvedCachePolicy struct {
	Model           string `json:"model"`
	Provider        string `json:"provider"`
	ProviderType    string `json:"providerType"`
	ProviderLabel   string `json:"providerLabel"`
	BaseURL         string `json:"baseUrl,omitempty"`
	TTLMinutes      int    `json:"ttlMinutes"`
	IntervalMinutes int    `json:"intervalMinutes"`
	MarginMinutes   int    `json:"marginMinutes"`
}

type sessionKeepAliveRecord struct {
	SessionID           string
	Mode                string
	TargetCount         int
	UntilAt             string
	UsedCount           int
	AnchorAt            string
	LastKeepAliveAt     string
	LastKeepAliveTurnID string
	LastResult          string
	LastNote            string
	StoppedReason       string
	StoppedDetail       string
	UpdatedAt           string
}

type SessionKeepAliveView struct {
	SessionID        string              `json:"sessionId"`
	State            string              `json:"state"`
	Mode             string              `json:"mode"`
	TargetCount      int                 `json:"targetCount"`
	UntilAt          string              `json:"untilAt,omitempty"`
	EffectiveEnabled bool                `json:"effectiveEnabled"`
	EffectiveLimit   int                 `json:"effectiveLimit"`
	UsedCount        int                 `json:"usedCount"`
	RemainingCount   *int                `json:"remainingCount,omitempty"`
	StatusLabel      string              `json:"statusLabel"`
	StatusTone       string              `json:"statusTone"`
	LastResult       string              `json:"lastResult,omitempty"`
	LastResultLabel  string              `json:"lastResultLabel,omitempty"`
	LastNote         string              `json:"lastNote,omitempty"`
	LastKeepAliveAt  string              `json:"lastKeepAliveAt,omitempty"`
	StoppedReason    string              `json:"stoppedReason,omitempty"`
	StoppedDetail    string              `json:"stoppedDetail,omitempty"`
	EstimatedCost    string              `json:"estimatedCost"`
	Policy           ResolvedCachePolicy `json:"policy"`
	InFlight         bool                `json:"inFlight"`
	GlobalEnabled    bool                `json:"globalEnabled"`
	GlobalCount      int                 `json:"globalCount"`
}

type idleInFlightState struct {
	SentAt        time.Time
	PromptID      string
	UserMessageID string
	TurnID        string
}

func defaultSessionKeepAliveRecord(sessionID string) sessionKeepAliveRecord {
	return sessionKeepAliveRecord{
		SessionID:   sessionID,
		Mode:        IdleModeDefault,
		TargetCount: 2,
	}
}

func sessionIsArchivedOrDone(session NativeSession) bool {
	if session.Archived || strings.TrimSpace(session.ArchivedAt) != "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(session.Status)) {
	case "archived", "done", "completed":
		return true
	}
	return false
}

func goalActiveForIdleKeepAlive(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var goal struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &goal) != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(goal.Status), "active")
}

// cacheIntervalForTTL derives the keepalive interval and safety margin from a
// cache TTL in minutes. 30 min -> margin 5 (interval 25); 5 min -> margin 1 (interval 4).
func cacheIntervalForTTL(ttlMinutes int) (interval int, margin int) {
	if ttlMinutes < providerTTLMinMinutes {
		return 0, 0
	}
	margin = (ttlMinutes + 2) / 6
	if margin < 1 {
		margin = 1
	}
	if margin >= ttlMinutes {
		margin = ttlMinutes - 1
	}
	return ttlMinutes - margin, margin
}

func providerLabelForType(kind string) string {
	switch kind {
	case ProviderTypeSubscription:
		return "订阅"
	case ProviderTypeOpenPlatform:
		return "开放平台"
	default:
		return "其他"
	}
}

func providerBaseURL(cfg map[string]any) string {
	for _, key := range []string{"base_url", "baseUrl", "url"} {
		if value, ok := cfg[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func classifyProvider(providerKey string, providerCfg map[string]any) (string, string) {
	key := strings.ToLower(strings.TrimSpace(providerKey))
	baseURL := providerBaseURL(providerCfg)

	if providerCfg != nil {
		if oauth, ok := providerCfg["oauth"]; ok && oauth != nil {
			switch typed := oauth.(type) {
			case string:
				if strings.TrimSpace(typed) != "" {
					return ProviderTypeSubscription, baseURL
				}
			case map[string]any:
				if len(typed) > 0 {
					return ProviderTypeSubscription, baseURL
				}
			case bool:
				if typed {
					return ProviderTypeSubscription, baseURL
				}
			}
		}
		if typ, ok := providerCfg["type"].(string); ok {
			switch strings.ToLower(strings.TrimSpace(typ)) {
			case "kimi", "kimi-code", "managed":
				return ProviderTypeSubscription, baseURL
			}
		}
	}

	if baseURL != "" {
		if parsed, err := url.Parse(baseURL); err == nil {
			host := strings.ToLower(parsed.Hostname())
			path := strings.ToLower(strings.TrimRight(parsed.Path, "/"))
			if host == "api.kimi.com" && (path == "/coding" || strings.HasPrefix(path, "/coding/")) {
				return ProviderTypeSubscription, baseURL
			}
			if host == "api.moonshot.cn" || host == "api.moonshot.ai" {
				return ProviderTypeOpenPlatform, baseURL
			}
		}
	}

	switch key {
	case "kimi-code", "managed:kimi-code", "kimi-for-coding", "managed":
		return ProviderTypeSubscription, baseURL
	}
	if strings.Contains(key, "api.kimi.com/coding") {
		return ProviderTypeSubscription, baseURL
	}
	if strings.Contains(key, "api.moonshot.cn") || strings.Contains(key, "api.moonshot.ai") {
		return ProviderTypeOpenPlatform, baseURL
	}
	return ProviderTypeOther, baseURL
}

func resolveCachePolicyFromConfig(modelID string, cfg map[string]any, ttlCfg ProviderTTLSettings) ResolvedCachePolicy {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" && cfg != nil {
		for _, key := range []string{"default_model", "defaultModel"} {
			if value, ok := cfg[key].(string); ok && strings.TrimSpace(value) != "" {
				modelID = strings.TrimSpace(value)
				break
			}
		}
	}

	providerKey, _ := splitModelKey(modelID)
	if cfg != nil {
		if models, ok := cfg["models"].(map[string]any); ok && modelID != "" {
			if entry, ok := models[modelID].(map[string]any); ok {
				if p, _ := entry["provider"].(string); strings.TrimSpace(p) != "" {
					providerKey = strings.TrimSpace(p)
				}
			} else {
				for key, raw := range models {
					entry, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					entryModel, _ := entry["model"].(string)
					if key == modelID || strings.EqualFold(entryModel, modelID) || strings.HasSuffix(key, "/"+modelID) {
						if p, _ := entry["provider"].(string); strings.TrimSpace(p) != "" {
							providerKey = strings.TrimSpace(p)
						} else if p, _ := splitModelKey(key); p != "" {
							providerKey = p
						}
						break
					}
				}
			}
		}
	}

	var providerCfg map[string]any
	if cfg != nil && providerKey != "" {
		if providers, ok := cfg["providers"].(map[string]any); ok {
			if matched, ok := providers[providerKey].(map[string]any); ok {
				providerCfg = matched
			}
		}
	}

	kind, baseURL := classifyProvider(providerKey, providerCfg)
	ttlMinutes := 0
	switch kind {
	case ProviderTypeSubscription:
		ttlMinutes = ttlCfg.SubscriptionMinutes
	case ProviderTypeOpenPlatform:
		ttlMinutes = ttlCfg.OpenPlatformMinutes
	default:
		ttlMinutes = ttlCfg.OtherMinutes
	}
	interval, margin := cacheIntervalForTTL(ttlMinutes)
	return ResolvedCachePolicy{
		Model:           modelID,
		Provider:        providerKey,
		ProviderType:    kind,
		ProviderLabel:   providerLabelForType(kind),
		BaseURL:         baseURL,
		TTLMinutes:      ttlMinutes,
		IntervalMinutes: interval,
		MarginMinutes:   margin,
	}
}

func (s *NativeService) cachedOfficialConfig(ctx context.Context, now time.Time) map[string]any {
	s.idleMu.Lock()
	if s.idleConfigCache != nil && now.Sub(s.idleConfigAt) < 10*time.Second {
		cached := s.idleConfigCache
		s.idleMu.Unlock()
		return cached
	}
	s.idleMu.Unlock()

	raw, err := s.kap.Request(ctx, http.MethodGet, "/config", nil)
	if err != nil {
		s.idleMu.Lock()
		cached := s.idleConfigCache
		s.idleMu.Unlock()
		return cached
	}
	var parsed map[string]any
	if json.Unmarshal(raw, &parsed) != nil {
		return nil
	}
	s.idleMu.Lock()
	s.idleConfigCache = parsed
	s.idleConfigAt = now
	s.idleMu.Unlock()
	return parsed
}

func (s *NativeService) invalidateOfficialConfigCache() {
	s.idleMu.Lock()
	s.idleConfigCache = nil
	s.idleConfigAt = time.Time{}
	s.idleMu.Unlock()
}

func (s *NativeService) loadSessionKeepAlive(ctx context.Context, sessionID string) (sessionKeepAliveRecord, error) {
	record := defaultSessionKeepAliveRecord(sessionID)
	if sessionID == "" || s == nil || s.db == nil {
		return record, nil
	}
	err := s.db.QueryRowContext(ctx, `SELECT mode,target_count,until_at,used_count,anchor_at,last_keepalive_at,last_keepalive_turn_id,last_result,last_note,stopped_reason,stopped_detail,updated_at FROM session_keepalive_state WHERE session_id=?`, sessionID).
		Scan(&record.Mode, &record.TargetCount, &record.UntilAt, &record.UsedCount, &record.AnchorAt, &record.LastKeepAliveAt, &record.LastKeepAliveTurnID, &record.LastResult, &record.LastNote, &record.StoppedReason, &record.StoppedDetail, &record.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return record, nil
	}
	if err != nil {
		return record, err
	}
	if record.Mode == "" {
		record.Mode = IdleModeDefault
	}
	if record.TargetCount < idleKeepAliveMinCount {
		record.TargetCount = 2
	}
	return record, nil
}

func (s *NativeService) saveSessionKeepAlive(ctx context.Context, record sessionKeepAliveRecord, now time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	if record.SessionID == "" {
		return errors.New("缺少会话 ID")
	}
	if record.Mode == "" {
		record.Mode = IdleModeDefault
	}
	if record.TargetCount < idleKeepAliveMinCount {
		record.TargetCount = 2
	}
	record.UpdatedAt = storage.SortableTime(now)
	_, err := s.db.ExecContext(ctx, `INSERT INTO session_keepalive_state(session_id,mode,target_count,until_at,used_count,anchor_at,last_keepalive_at,last_keepalive_turn_id,last_result,last_note,stopped_reason,stopped_detail,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(session_id) DO UPDATE SET mode=excluded.mode,target_count=excluded.target_count,until_at=excluded.until_at,used_count=excluded.used_count,anchor_at=excluded.anchor_at,last_keepalive_at=excluded.last_keepalive_at,last_keepalive_turn_id=excluded.last_keepalive_turn_id,last_result=excluded.last_result,last_note=excluded.last_note,stopped_reason=excluded.stopped_reason,stopped_detail=excluded.stopped_detail,updated_at=excluded.updated_at`,
		record.SessionID, record.Mode, record.TargetCount, record.UntilAt, record.UsedCount, record.AnchorAt, record.LastKeepAliveAt, record.LastKeepAliveTurnID, record.LastResult, record.LastNote, record.StoppedReason, record.StoppedDetail, record.UpdatedAt)
	return err
}

func (s *NativeService) hasAnySessionKeepAliveOverride(ctx context.Context) bool {
	if s == nil || s.db == nil {
		return false
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_keepalive_state WHERE mode IN ('count','until')`).Scan(&count)
	return err == nil && count > 0
}

func (s *NativeService) isIdleKeepAliveInFlight(sessionID string) bool {
	s.idleMu.Lock()
	defer s.idleMu.Unlock()
	_, ok := s.idleInFlight[sessionID]
	return ok
}

func (s *NativeService) idleInFlightInfo(sessionID string) (idleInFlightState, bool) {
	s.idleMu.Lock()
	defer s.idleMu.Unlock()
	info, ok := s.idleInFlight[sessionID]
	return info, ok
}

func (s *NativeService) setIdleInFlight(sessionID string, info idleInFlightState) {
	s.idleMu.Lock()
	if s.idleInFlight == nil {
		s.idleInFlight = map[string]idleInFlightState{}
	}
	s.idleInFlight[sessionID] = info
	s.idleMu.Unlock()
}

func (s *NativeService) clearIdleInFlight(sessionID string) {
	s.idleMu.Lock()
	delete(s.idleInFlight, sessionID)
	s.idleMu.Unlock()
}

func (s *NativeService) idleKeepAlivePoll() time.Duration {
	if s.idleVerifyPoll > 0 {
		return s.idleVerifyPoll
	}
	if s.verifyPoll > 0 && s.verifyPoll < 2*time.Second {
		return s.verifyPoll
	}
	return 1500 * time.Millisecond
}

func parseSortableOrRFC3339(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed
	}
	return time.Time{}
}

// noteUserMessage resets the idle keepalive cycle whenever the user sends a real
// message in this session (N4: 用户发出消息后，计时和次数都重置).
func (s *NativeService) noteUserMessage(ctx context.Context, sessionID string, at time.Time) {
	if sessionID == "" || s == nil || s.db == nil {
		return
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	record, err := s.loadSessionKeepAlive(ctx, sessionID)
	if err != nil {
		return
	}
	anchor := parseSortableOrRFC3339(record.AnchorAt)
	if !anchor.IsZero() && !at.After(anchor) && record.UsedCount == 0 && record.StoppedReason == "" {
		return
	}
	record.UsedCount = 0
	record.AnchorAt = storage.SortableTime(at)
	record.LastKeepAliveAt = ""
	record.LastKeepAliveTurnID = ""
	record.StoppedReason = ""
	record.StoppedDetail = ""
	_ = s.saveSessionKeepAlive(ctx, record, at)
	s.idleMu.Lock()
	delete(s.idleAlerted, sessionID)
	s.idleMu.Unlock()
	s.publishSessionKeepAliveUpdate(ctx, sessionID, at)
}

func (s *NativeService) noteUserMessageFromFrame(frame NativeFrame) {
	if frame.SessionID == "" {
		return
	}
	if frame.Type != "turn.started" && frame.Type != "item.started" {
		return
	}
	if frame.Type == "item.started" && frame.Item.Type != "user_message" {
		return
	}
	if frame.Type == "item.started" && strings.TrimSpace(frame.Item.Text) == idleKeepAlivePrompt {
		return
	}
	if frame.Type == "turn.started" {
		for _, item := range frame.Turn.Items {
			if item.Type == "user_message" && strings.TrimSpace(item.Text) == idleKeepAlivePrompt {
				return
			}
		}
	}
	if info, active := s.idleInFlightInfo(frame.SessionID); active {
		if frame.Type == "item.started" && info.UserMessageID != "" && frame.Item.ItemID == info.UserMessageID {
			return
		}
		if frame.TurnID != "" && info.TurnID != "" && frame.TurnID == info.TurnID {
			return
		}
		if frame.Turn.TurnID != "" && info.TurnID != "" && frame.Turn.TurnID == info.TurnID {
			return
		}
		// While an idle keepalive send is freshly in flight and we haven't bound TurnID yet,
		// avoid mistaking the keepalive's own turn.started for a user message.
		if time.Since(info.SentAt) < 15*time.Second && frame.Type == "turn.started" && len(frame.Turn.Items) == 0 {
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s.noteUserMessage(ctx, frame.SessionID, time.Now().UTC())
}

// syncUserActivityReset inspects cache_live.last_user_prompt and cache_steps to detect
// user messages sent directly in the official UI (bypassing our bridge).
func (s *NativeService) syncUserActivityReset(ctx context.Context, record *sessionKeepAliveRecord, live cache.LiveSignals, lastStep cache.Step, lastStepSeen bool, now time.Time) bool {
	if s.isIdleKeepAliveInFlight(record.SessionID) {
		return false
	}
	anchor := parseSortableOrRFC3339(record.AnchorAt)
	lastKA := parseSortableOrRFC3339(record.LastKeepAliveAt)

	isKeepAliveStamp := func(ts time.Time) bool {
		if lastKA.IsZero() {
			return false
		}
		diff := ts.Sub(lastKA)
		return diff >= -5*time.Second && diff <= 45*time.Second
	}

	var resetAt time.Time
	if !live.LastUserPrompt.IsZero() && live.LastUserPrompt.After(anchor) && !isKeepAliveStamp(live.LastUserPrompt) {
		resetAt = live.LastUserPrompt
	}
	if lastStepSeen && !lastStep.At.IsZero() && lastStep.At.After(anchor) && lastStep.At.After(lastKA) {
		isKnownKATurn := record.LastKeepAliveTurnID != "" && lastStep.TurnID == record.LastKeepAliveTurnID
		if !isKnownKATurn && !isKeepAliveStamp(lastStep.At) && lastStep.At.After(resetAt) {
			resetAt = lastStep.At
		}
	}
	if resetAt.IsZero() {
		return false
	}
	changed := record.UsedCount != 0 || record.StoppedReason != "" || record.LastKeepAliveAt != "" || !resetAt.Equal(anchor)
	record.UsedCount = 0
	record.AnchorAt = storage.SortableTime(resetAt)
	record.LastKeepAliveAt = ""
	record.LastKeepAliveTurnID = ""
	record.StoppedReason = ""
	record.StoppedDetail = ""
	if changed {
		_ = s.saveSessionKeepAlive(ctx, *record, now)
		s.idleMu.Lock()
		delete(s.idleAlerted, record.SessionID)
		s.idleMu.Unlock()
	}
	return changed
}

func managedQuotaRemainingPercent(usage managedUsage, now time.Time) (float64, bool) {
	if usage.Kind != "ok" || usage.Quota == nil || len(usage.Quota.Usages) == 0 {
		return 0, false
	}
	known := false
	minRemaining := 100.0
	for _, window := range usage.Quota.Usages {
		if window.UsedRatio == nil {
			continue
		}
		known = true
		if window.ResetAt != "" {
			if reset, err := time.Parse(time.RFC3339, window.ResetAt); err == nil && !now.Before(reset) {
				continue
			}
		}
		rem := (1.0 - *window.UsedRatio) * 100.0
		if rem < 0 {
			rem = 0
		}
		if rem > 100 {
			rem = 100
		}
		if rem < minRemaining {
			minRemaining = rem
		}
	}
	return minRemaining, known
}

func (s *NativeService) checkIdleQuotaGuard(ctx context.Context, settings AutomationSettings, policy ResolvedCachePolicy, now time.Time) (blocked bool, remaining float64, detail string) {
	if policy.ProviderType != ProviderTypeSubscription || settings.IdleKeepAlive.MinQuotaPercent <= 0 {
		return false, 0, ""
	}
	raw, err := s.kap.Request(ctx, http.MethodGet, "/oauth/usage", nil)
	if err != nil {
		return false, 0, ""
	}
	usage, err := parseManagedUsage(raw)
	if err != nil {
		return false, 0, ""
	}
	rem, known := managedQuotaRemainingPercent(usage, now)
	if !known {
		return false, 0, ""
	}
	if rem < float64(settings.IdleKeepAlive.MinQuotaPercent) {
		return true, rem, fmt.Sprintf("剩余额度约 %.0f%%，低于阈值 %d%%", rem, settings.IdleKeepAlive.MinQuotaPercent)
	}
	return false, rem, ""
}

func (s *NativeService) publishKeepAliveAlert(sessionID, reasonKey, severity, title, message string) {
	s.idleMu.Lock()
	if s.idleAlerted == nil {
		s.idleAlerted = map[string]string{}
	}
	if s.idleAlerted[sessionID] == reasonKey {
		s.idleMu.Unlock()
		return
	}
	s.idleAlerted[sessionID] = reasonKey
	s.idleMu.Unlock()

	s.bus.Publish("keepalive", map[string]any{
		"type":      "keepalive.alert",
		"sessionId": sessionID,
		"reason":    reasonKey,
		"severity":  severity,
		"title":     title,
		"message":   message,
	})
}

func (s *NativeService) publishKeepAliveSuppress(sessionID, promptID string, expiresAt time.Time) {
	s.bus.Publish("keepalive", map[string]any{
		"type":      "keepalive.suppress",
		"sessionId": sessionID,
		"promptId":  promptID,
		"expiresAt": expiresAt.UTC().Format(time.RFC3339Nano),
	})
}

func (s *NativeService) publishSessionKeepAliveUpdate(ctx context.Context, sessionID string, now time.Time) {
	view, err := s.sessionKeepAliveView(ctx, sessionID, now)
	if err != nil {
		return
	}
	s.bus.Publish("keepalive", map[string]any{
		"type":      "keepalive.session",
		"sessionId": sessionID,
		"view":      view,
	})
}

func effectiveIdleConfig(settings AutomationSettings, record sessionKeepAliveRecord, now time.Time) (enabled bool, limit int, until time.Time, exhausted bool, exhaustedReason string) {
	switch record.Mode {
	case IdleModeOff:
		return false, 0, time.Time{}, false, ""
	case IdleModeCount:
		limit = record.TargetCount
		if limit < idleKeepAliveMinCount {
			limit = 1
		}
		if record.UsedCount >= limit {
			return true, limit, time.Time{}, true, "count_exhausted"
		}
		return true, limit, time.Time{}, false, ""
	case IdleModeUntil:
		until = parseSortableOrRFC3339(record.UntilAt)
		if until.IsZero() || !now.Before(until) {
			return true, 0, until, true, "until_reached"
		}
		return true, 0, until, false, ""
	default: // IdleModeDefault
		if !settings.IdleKeepAlive.Enabled {
			return false, settings.IdleKeepAlive.DefaultCount, time.Time{}, false, ""
		}
		limit = settings.IdleKeepAlive.DefaultCount
		if record.UsedCount >= limit {
			return true, limit, time.Time{}, true, "count_exhausted"
		}
		return true, limit, time.Time{}, false, ""
	}
}

func (s *NativeService) resolveSessionPolicy(ctx context.Context, sessionID string, snapshot automationSnapshot, lastStep cache.Step, lastStepSeen bool, settings AutomationSettings, now time.Time) ResolvedCachePolicy {
	modelID := strings.TrimSpace(snapshot.session.Model)
	if modelID == "" && lastStepSeen {
		modelID = strings.TrimSpace(lastStep.Model)
	}
	cfg := s.cachedOfficialConfig(ctx, now)
	return resolveCachePolicyFromConfig(modelID, cfg, settings.IdleKeepAlive.ProviderTTL)
}

// isIdleKeepAliveProtecting reports whether idle keepalive currently owns this session
// so idle compaction must hold off (N4: 保活期间绝不压缩；保活结束后仍然空闲再进入压缩).
func (s *NativeService) isIdleKeepAliveProtecting(ctx context.Context, settings AutomationSettings, sessionID string, now time.Time) bool {
	if s.isIdleKeepAliveInFlight(sessionID) {
		return true
	}
	snapshot, ok := s.automationSnapshot(sessionID)
	if !ok || sessionIsArchivedOrDone(snapshot.session) || pendingNativeInteraction(snapshot) || goalBlocksIdle(snapshot) || goalActiveForIdleKeepAlive(snapshot.goal) {
		return false
	}
	record, err := s.loadSessionKeepAlive(ctx, sessionID)
	if err != nil {
		return false
	}
	var live cache.LiveSignals
	var lastStep cache.Step
	var lastStepSeen bool
	if store, storeErr := s.cacheStore(); storeErr == nil {
		_, _ = store.Scan(ctx, now, false)
		live, _ = store.Live(ctx, sessionID)
		lastStep, lastStepSeen, _ = store.LastMainStep(ctx, sessionID)
	}
	s.syncUserActivityReset(ctx, &record, live, lastStep, lastStepSeen, now)

	enabled, _, _, exhausted, _ := effectiveIdleConfig(settings, record, now)
	if !enabled {
		return false
	}
	if record.StoppedReason != "" && record.StoppedReason != "count_exhausted" && record.StoppedReason != "until_reached" {
		return false
	}
	policy := s.resolveSessionPolicy(ctx, sessionID, snapshot, lastStep, lastStepSeen, settings, now)
	if policy.TTLMinutes < providerTTLMinMinutes || policy.IntervalMinutes <= 0 {
		return false
	}
	// If cache already expired past TTL with no active keepalive, do not block compaction.
	lastReq := latestKeepAliveClock(record, lastStep, lastStepSeen, snapshot)
	if !lastReq.IsZero() && now.Sub(lastReq) >= time.Duration(policy.TTLMinutes)*time.Minute {
		return false
	}
	if !exhausted {
		return true
	}
	// After the final keepalive fires, hold off compaction until that refreshed cache
	// interval has elapsed so compaction does not immediately wipe the cache we just kept alive.
	lastKA := parseSortableOrRFC3339(record.LastKeepAliveAt)
	if !lastKA.IsZero() && now.Sub(lastKA) < time.Duration(policy.IntervalMinutes)*time.Minute {
		return true
	}
	return false
}

func latestKeepAliveClock(record sessionKeepAliveRecord, lastStep cache.Step, lastStepSeen bool, snapshot automationSnapshot) time.Time {
	var best time.Time
	consider := func(ts time.Time) {
		if !ts.IsZero() && ts.After(best) {
			best = ts
		}
	}
	if lastStepSeen {
		consider(lastStep.At)
	}
	consider(parseSortableOrRFC3339(record.LastKeepAliveAt))
	consider(parseSortableOrRFC3339(record.AnchorAt))
	if best.IsZero() {
		consider(snapshot.lastActivity)
	}
	return best
}

func (s *NativeService) stopIdleKeepAlive(ctx context.Context, record *sessionKeepAliveRecord, reason, detail string, alert bool, now time.Time) {
	prevReason := record.StoppedReason
	record.StoppedReason = reason
	record.StoppedDetail = detail
	if reason == "undo_failed" {
		record.LastResult = "undo_failed"
		record.LastNote = detail
	}
	_ = s.saveSessionKeepAlive(ctx, *record, now)
	if prevReason != reason {
		s.publishSessionKeepAliveUpdate(ctx, record.SessionID, now)
	}
	if alert {
		title := "空闲缓存保活已停止"
		severity := "warning"
		if reason == "undo_failed" || reason == "error" {
			severity = "error"
		}
		name := s.cachedTitle(record.SessionID)
		msg := detail
		if name != "" {
			msg = fmt.Sprintf("会话「%s」：%s", name, detail)
		}
		s.publishKeepAliveAlert(record.SessionID, reason, severity, title, msg)
	}
}

// runIdleKeepAliveOnce evaluates the N4 idle-session keepalive state machine for one session.
func (s *NativeService) runIdleKeepAliveOnce(ctx context.Context, settings AutomationSettings, id string, now time.Time) {
	if s.isAutomationPausedForSwitch() || s.isIdleKeepAliveInFlight(id) {
		return
	}
	snapshot, ok := s.automationSnapshot(id)
	if !ok {
		return
	}
	record, err := s.loadSessionKeepAlive(ctx, id)
	if err != nil {
		return
	}

	var live cache.LiveSignals
	var lastStep cache.Step
	var lastStepSeen bool
	if store, storeErr := s.cacheStore(); storeErr == nil {
		_, _ = store.Scan(ctx, now, false)
		live, _ = store.Live(ctx, id)
		lastStep, lastStepSeen, _ = store.LastMainStep(ctx, id)
	}
	s.syncUserActivityReset(ctx, &record, live, lastStep, lastStepSeen, now)

	// Guardrail: archived or done session stops immediately.
	if sessionIsArchivedOrDone(snapshot.session) {
		if record.StoppedReason != "archived" && (record.Mode == IdleModeCount || record.Mode == IdleModeUntil || settings.IdleKeepAlive.Enabled) {
			s.stopIdleKeepAlive(ctx, &record, "archived", "会话已归档或完成", false, now)
		}
		return
	}

	// Guardrail: active turn, waiting for subagent, blocked wait, pending interaction, or active/blocked goal.
	if snapshot.session.Busy || snapshot.session.MainTurnActive || live.OpenBackground > 0 || live.Blocked || pendingNativeInteraction(snapshot) || goalBlocksIdle(snapshot) || goalActiveForIdleKeepAlive(snapshot.goal) {
		return
	}

	enabled, _, _, exhausted, exhaustedReason := effectiveIdleConfig(settings, record, now)
	if !enabled {
		return
	}
	if exhausted {
		if record.StoppedReason != exhaustedReason {
			detail := "保活次数已用完"
			if exhaustedReason == "until_reached" {
				detail = "已到达保活截止时间"
			}
			s.stopIdleKeepAlive(ctx, &record, exhaustedReason, detail, false, now)
		}
		return
	}
	// Hard stop reasons stay stopped until user sends a new message or changes override.
	switch record.StoppedReason {
	case "undo_failed", "undo_skipped", "quota_low", "error":
		return
	}

	policy := s.resolveSessionPolicy(ctx, id, snapshot, lastStep, lastStepSeen, settings, now)
	if policy.TTLMinutes < providerTTLMinMinutes || policy.IntervalMinutes <= 0 {
		if record.StoppedReason != "provider_unknown" {
			s.stopIdleKeepAlive(ctx, &record, "provider_unknown", "当前供应商缓存时长未知，可在高级设置中配置", false, now)
		}
		return
	}
	if record.StoppedReason == "provider_unknown" {
		record.StoppedReason = ""
		record.StoppedDetail = ""
		_ = s.saveSessionKeepAlive(ctx, record, now)
	}

	lastReq := latestKeepAliveClock(record, lastStep, lastStepSeen, snapshot)
	if lastReq.IsZero() {
		return
	}
	elapsed := now.Sub(lastReq)
	interval := time.Duration(policy.IntervalMinutes) * time.Minute
	ttl := time.Duration(policy.TTLMinutes) * time.Minute
	if elapsed < interval {
		return
	}
	// Guardrail: Kimi Code shutdown / long absence past cache TTL must not catch up.
	if elapsed >= ttl {
		if record.StoppedReason != "expired" {
			s.stopIdleKeepAlive(ctx, &record, "expired", "距上次请求已超过缓存时长，不补发保活", false, now)
		}
		return
	}

	// Guardrail: quota threshold check (default 10%).
	if low, _, detail := s.checkIdleQuotaGuard(ctx, settings, policy, now); low {
		s.stopIdleKeepAlive(ctx, &record, "quota_low", detail, true, now)
		return
	}

	// Refresh fresh session, turns, and goal from official daemon before committing to send.
	loaded, err := s.readSession(ctx, id)
	if err != nil {
		return
	}
	if sessionIsArchivedOrDone(loaded.session) {
		s.stopIdleKeepAlive(ctx, &record, "archived", "会话已归档或完成", false, now)
		return
	}
	if loaded.session.Busy || loaded.session.MainTurnActive || pendingNativeInteraction(snapshotFromCache(loaded)) || goalBlocksIdle(snapshotFromCache(loaded)) || goalActiveForIdleKeepAlive(loaded.goal) {
		return
	}
	if len(loaded.turns) == 0 {
		return
	}
	if s.isAutomationPausedForSwitch() {
		return
	}
	if store, storeErr := s.cacheStore(); storeErr == nil {
		if liveNow, liveErr := store.Live(ctx, id); liveErr == nil && (liveNow.OpenBackground > 0 || liveNow.Blocked) {
			return
		}
	}

	revision := contextRevision(snapshotFromCache(loaded))
	intent, _, err := s.beginAutomation(ctx, "idle_keepalive", id, revision, now)
	if err != nil {
		return
	}

	preLastTurnID := loaded.turns[len(loaded.turns)-1].TurnID
	preGit, preGitErr := gitStatusFingerprint(ctx, loaded.session.CWD)
	sentAt := time.Now().UTC()
	if !now.IsZero() && now.Before(sentAt.Add(-time.Minute)) {
		// Preserve synthetic test clock when caller injects a simulated timestamp.
		sentAt = now
	}

	s.setIdleInFlight(id, idleInFlightState{SentAt: sentAt})
	s.publishKeepAliveSuppress(id, "", time.Now().UTC().Add(s.keepAliveWindow()))
	_ = s.WatchSessionInternal(id)

	// Send with zero parameter overrides so cache prefix stays byte-identical.
	body := map[string]any{
		"content": []any{map[string]string{"type": "text", "text": idleKeepAlivePrompt}},
	}
	rawResp, sendErr := s.kap.Request(ctx, http.MethodPost, "/sessions/"+url.PathEscape(id)+"/prompts", body)
	if sendErr != nil {
		s.clearIdleInFlight(id)
		note := "空闲保活消息发送失败：" + sendErr.Error()
		_ = s.updateAutomation(ctx, intent.ID, "failed", note, now)
		s.publishAutomationReceipt("idle_keepalive", id, "failed", note)
		reason := "error"
		var nativeErr *NativeError
		if errors.As(sendErr, &nativeErr) && nativeErr.Status == http.StatusTooManyRequests {
			reason = "quota_low"
		}
		s.stopIdleKeepAlive(ctx, &record, reason, note, true, now)
		return
	}

	var promptResp struct {
		PromptID      string `json:"prompt_id"`
		UserMessageID string `json:"user_message_id"`
	}
	_ = json.Unmarshal(rawResp, &promptResp)
	s.setIdleInFlight(id, idleInFlightState{
		SentAt:        sentAt,
		PromptID:      promptResp.PromptID,
		UserMessageID: promptResp.UserMessageID,
	})
	if promptResp.PromptID != "" {
		s.publishKeepAliveSuppress(id, promptResp.PromptID, time.Now().UTC().Add(s.keepAliveWindow()))
	}

	record.UsedCount++
	record.LastKeepAliveAt = storage.SortableTime(sentAt)
	record.LastResult = "acknowledged"
	record.LastNote = "空闲保活已发送，等待回合结束并自动撤回"
	record.StoppedReason = ""
	record.StoppedDetail = ""
	_ = s.saveSessionKeepAlive(ctx, record, now)

	ackNote := "空闲保活已发送，等待回合结束并自动撤回"
	_ = s.updateAutomation(ctx, intent.ID, "acknowledged", ackNote, now)
	s.publishAutomationReceipt("idle_keepalive", id, "acknowledged", ackNote)
	s.publishSessionKeepAliveUpdate(ctx, id, now)

	anchorStepUUID := ""
	if lastStepSeen {
		anchorStepUUID = lastStep.StepUUID
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.completeIdleKeepAlive(idleKeepAliveJob{
			intentID:       intent.ID,
			sessionID:      id,
			preLastTurnID:  preLastTurnID,
			userMessageID:  promptResp.UserMessageID,
			promptID:       promptResp.PromptID,
			cwd:            loaded.session.CWD,
			preGit:         preGit,
			preGitErr:      preGitErr,
			anchorStepUUID: anchorStepUUID,
			model:          policy.Model,
			sentAt:         sentAt,
		})
	}()
}

type idleKeepAliveJob struct {
	intentID       string
	sessionID      string
	preLastTurnID  string
	userMessageID  string
	promptID       string
	cwd            string
	preGit         string
	preGitErr      error
	anchorStepUUID string
	model          string
	sentAt         time.Time
}

func findIdleKeepAliveTurn(turns []NativeTurn, preLastTurnID, userMessageID string) (turn NativeTurn, index int, found bool) {
	start := 0
	if preLastTurnID != "" {
		for i, candidate := range turns {
			if candidate.TurnID == preLastTurnID {
				start = i + 1
				break
			}
		}
	}
	for i := start; i < len(turns); i++ {
		candidate := turns[i]
		for _, item := range candidate.Items {
			if userMessageID != "" && item.ItemID == userMessageID {
				return candidate, i, true
			}
			if item.Type == "user_message" && strings.TrimSpace(item.Text) == idleKeepAlivePrompt {
				return candidate, i, true
			}
		}
	}
	return NativeTurn{}, -1, false
}

func turnHasToolCall(turn NativeTurn) bool {
	for _, item := range turn.Items {
		if item.Type == "tool_call" {
			return true
		}
	}
	return false
}

func turnHasSettledOutput(turn NativeTurn) bool {
	if turn.Error != nil {
		return true
	}
	for _, item := range turn.Items {
		if item.Type == "assistant_message" {
			return true
		}
	}
	return false
}

const (
	gitFingerprintMaxFileBytes  = 8 << 20
	gitFingerprintMaxTotalBytes = 64 << 20
	gitFingerprintMaxStatus     = 4 << 20
	gitFingerprintMaxEntries    = 16_384
)

// An empty fingerprint with no error means Git confirmed there is no repository.
func gitStatusFingerprint(ctx context.Context, cwd string) (string, error) {
	if strings.TrimSpace(cwd) == "" {
		return "", errors.New("会话未记录工作目录")
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	stderr := &limitedBuffer{limit: 4096}
	top := &limitedBuffer{limit: 64 << 10}
	cmd := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.Stderr = stderr
	cmd.Stdout = top
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if ctx.Err() == nil && errors.As(err, &exitErr) && exitErr.ExitCode() == 128 &&
			strings.HasPrefix(stderr.buf.String(), "fatal: not a git repository (or any of the parent directories") {
			return "", confirmNonGitDirectory(cwd)
		}
		return "", fmt.Errorf("无法定位 Git 工作区：%w", err)
	}
	rootPath, ok := strings.CutSuffix(top.buf.String(), "\n")
	if top.truncated || !ok || !filepath.IsAbs(rootPath) {
		return "", errors.New("Git 工作区路径无效")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return "", err
	}
	defer root.Close()

	status, truncated, err := runGitCapped(ctx, rootPath, gitFingerprintMaxStatus, "--no-optional-locks", "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return "", fmt.Errorf("无法读取 Git 状态：%w", err)
	}
	if truncated {
		return "", errors.New("Git 状态超过大小上限")
	}
	digest := sha256.New()
	_, _ = io.WriteString(digest, rootPath+"\x00"+status+"\x00")
	scan := gitFingerprintScan{ctx: ctx, root: root, entries: gitFingerprintMaxEntries, bytes: gitFingerprintMaxTotalBytes}
	for status != "" {
		var entry string
		entry, status, ok = strings.Cut(status, "\x00")
		if !ok || len(entry) < 4 || entry[2] != ' ' {
			return "", errors.New("Git 状态条目无效")
		}
		xy := entry[:2]
		if err := scan.addPath(digest, entry[3:], strings.Contains(xy, "D")); err != nil {
			return "", err
		}
		if strings.ContainsAny(xy, "RC") {
			var source string
			source, status, ok = strings.Cut(status, "\x00")
			if !ok {
				return "", errors.New("Git 状态缺少重命名或复制的源路径")
			}
			if err := scan.addPath(digest, source, strings.Contains(xy, "R")); err != nil {
				return "", err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func confirmNonGitDirectory(cwd string) error {
	path, err := filepath.Abs(cwd)
	if err != nil {
		return err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	for {
		if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
			return errors.New("发现 .git，但 Git 无法读取工作区")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

type gitFingerprintScan struct {
	ctx     context.Context
	root    *os.Root
	entries int
	bytes   int64
}

func (s *gitFingerprintScan) addPath(digest hash.Hash, path string, allowMissing bool) error {
	path = filepath.FromSlash(strings.TrimSuffix(path, "/"))
	if path == "." || !filepath.IsLocal(path) {
		return fmt.Errorf("Git 状态路径无效：%q", path)
	}
	if err := s.addEntry(digest, path, allowMissing); err != nil {
		return fmt.Errorf("无法读取工作区条目 %q：%w", path, err)
	}
	return nil
}

func (s *gitFingerprintScan) addEntry(dst hash.Hash, path string, allowMissing bool) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.entries == 0 {
		return errors.New("工作区条目超过数量上限")
	}
	s.entries--
	info, err := s.root.Lstat(path)
	if err != nil {
		if allowMissing && (errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)) {
			_, _ = io.WriteString(dst, "-")
			return nil
		}
		return err
	}

	digest := sha256.New()
	var kind string
	switch mode := info.Mode(); {
	case mode.IsRegular():
		kind = "F"
		if err := s.hashFile(digest, path, info); err != nil {
			return err
		}
	case mode.IsDir():
		kind = "D"
		entries, err := s.readDir(path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			_, _ = io.WriteString(digest, entry.Name()+"\x00")
			if err := s.addEntry(digest, filepath.Join(path, entry.Name()), false); err != nil {
				return fmt.Errorf("%q：%w", entry.Name(), err)
			}
		}
	case mode&os.ModeSymlink != 0:
		kind = "L"
		target, err := s.root.Readlink(path)
		if err != nil {
			return err
		}
		if int64(len(target)) > s.bytes {
			return errors.New("工作区文件超过大小上限")
		}
		s.bytes -= int64(len(target))
		_, _ = io.WriteString(digest, target)
	default:
		return errors.New("工作区条目类型不支持")
	}
	_, _ = io.WriteString(dst, kind)
	_, _ = dst.Write(digest.Sum(nil))
	return nil
}

func (s *gitFingerprintScan) readDir(path string) ([]fs.DirEntry, error) {
	dir, err := s.root.Open(path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(s.entries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > s.entries {
		return nil, errors.New("工作区条目超过数量上限")
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

func (s *gitFingerprintScan) hashFile(digest hash.Hash, path string, info os.FileInfo) error {
	limit := min(gitFingerprintMaxFileBytes, s.bytes)
	if info.Size() > limit {
		return errors.New("工作区文件超过大小上限")
	}
	file, err := s.root.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	current, err := file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, current) || !current.Mode().IsRegular() {
		return errors.New("工作区文件在读取前发生变化")
	}
	read, err := io.Copy(digest, io.LimitReader(file, limit+1))
	if err != nil {
		return err
	}
	if read > limit {
		return errors.New("工作区文件超过大小上限")
	}
	current, err = s.root.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(info, current) || info.Size() != read || current.Size() != read || !info.ModTime().Equal(current.ModTime()) {
		return errors.New("工作区文件在读取期间发生变化")
	}
	s.bytes -= read
	return nil
}

func (s *NativeService) completeIdleKeepAlive(job idleKeepAliveJob) {
	defer s.clearIdleInFlight(job.sessionID)

	deadline := time.Now().Add(s.keepAliveWindow())
	poll := s.idleKeepAlivePoll()
	ctx := s.ctx
	var keepAliveTurn NativeTurn
	var keepAliveFound bool

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}

		tickCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		rawSession, sessionErr := s.kap.Request(tickCtx, http.MethodGet, "/sessions/"+url.PathEscape(job.sessionID), nil)
		turns, turnsErr := s.nativeTurns(tickCtx, job.sessionID, 20)
		cancel()

		if sessionErr == nil && turnsErr == nil {
			session, mapErr := mapNativeSession(rawSession)
			if mapErr == nil {
				turn, idx, found := findIdleKeepAliveTurn(turns, job.preLastTurnID, job.userMessageID)
				if found {
					keepAliveTurn = turn
					keepAliveFound = true
					if info, ok := s.idleInFlightInfo(job.sessionID); ok && info.TurnID != turn.TurnID {
						info.TurnID = turn.TurnID
						s.setIdleInFlight(job.sessionID, info)
					}
					// If another turn has already started after our keepalive turn, never undo.
					if idx < len(turns)-1 {
						s.finishIdleKeepAliveWithoutUndo(job, "completed", "保活期间出现新回合，已跳过撤回，保留当前对话", false, "")
						return
					}
					if !session.Busy && !session.MainTurnActive && (turnHasSettledOutput(turn) || time.Now().After(deadline)) {
						break
					}
				}
			}
		}

		if time.Now().After(deadline) {
			if !keepAliveFound {
				s.finishIdleKeepAliveWithoutUndo(job, keepAliveInvalid, "保活后在超时窗口内未观测到对应回合，保活无效", false, "")
				return
			}
			break
		}
	}

	// Read usage step from local session records (closed-loop verdict, no extra model request).
	verdict := keepAliveVerdict{
		State: keepAliveUnknown,
		Note:  "空闲保活回合已完成，暂未读取到缓存字段",
	}
	if store, err := s.cacheStore(); err == nil {
		for attempt := 0; attempt < 3; attempt++ {
			scanCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, _ = store.Scan(scanCtx, time.Now().UTC(), true)
			step, found, stepErr := store.StepAfter(scanCtx, job.sessionID, job.sentAt.Add(-time.Second))
			cancel()
			if stepErr == nil && found && step.StepUUID != job.anchorStepUUID {
				verdict = judgeKeepAlive(step, job.model)
				break
			}
			if attempt < 2 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(minDuration(poll, 300*time.Millisecond)):
				}
			}
		}
	}

	// Final safety check immediately before :undo.
	checkCtx, checkCancel := context.WithTimeout(ctx, 15*time.Second)
	defer checkCancel()
	latestTurns, err := s.nativeTurns(checkCtx, job.sessionID, 20)
	if err != nil {
		s.failIdleKeepAliveUndo(job, "撤回前无法确认最新回合："+err.Error())
		return
	}
	if len(latestTurns) == 0 || latestTurns[len(latestTurns)-1].TurnID != keepAliveTurn.TurnID {
		s.finishIdleKeepAliveWithoutUndo(job, verdict.State, verdict.Note+"；保活期间出现新回合，已跳过撤回", false, "")
		return
	}
	finalTurn := latestTurns[len(latestTurns)-1]
	if turnHasToolCall(finalTurn) {
		s.finishIdleKeepAliveWithoutUndo(job, "failed", "保活回合意外触发工具调用，未自动撤回", true, "undo_skipped")
		return
	}
	if job.preGitErr != nil {
		s.finishIdleKeepAliveWithoutUndo(job, "failed", "无法确认保活前的工作区状态，未自动撤回："+job.preGitErr.Error(), true, "undo_skipped")
		return
	}
	if job.preGit != "" {
		postGit, err := gitStatusFingerprint(checkCtx, job.cwd)
		if err == nil && postGit == "" {
			err = errors.New("工作目录已不是 Git 仓库")
		}
		if err != nil {
			s.finishIdleKeepAliveWithoutUndo(job, "failed", "无法确认撤回前的工作区状态，未自动撤回："+err.Error(), true, "undo_skipped")
			return
		}
		if postGit != job.preGit {
			s.finishIdleKeepAliveWithoutUndo(job, "failed", "保活期间工作区文件发生变化，未自动撤回", true, "undo_skipped")
			return
		}
	}

	if _, err := s.kap.UndoSession(checkCtx, job.sessionID, keepAliveTurn.TurnID, ""); err != nil {
		s.failIdleKeepAliveUndo(job, "保活回合撤回失败："+err.Error())
		return
	}

	// Prune the undone turn from local cache as well so listeners see the clean history promptly.
	s.mu.Lock()
	if cached, ok := s.cache[job.sessionID]; ok {
		kept := cached.turns[:0]
		for _, turn := range cached.turns {
			if turn.TurnID != keepAliveTurn.TurnID {
				kept = append(kept, turn)
			}
		}
		cached.turns = kept
		s.cache[job.sessionID] = cached
	}
	s.mu.Unlock()

	now := time.Now().UTC()
	note := verdict.Note + "；已自动撤回保活回合"
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	if record, loadErr := s.loadSessionKeepAlive(finishCtx, job.sessionID); loadErr == nil {
		record.LastKeepAliveTurnID = keepAliveTurn.TurnID
		record.LastResult = verdict.State
		record.LastNote = note
		_ = s.saveSessionKeepAlive(finishCtx, record, now)
	}
	_ = s.updateAutomation(finishCtx, job.intentID, verdict.State, note, now)
	s.publishAutomationReceipt("idle_keepalive", job.sessionID, verdict.State, note)
	s.publishSessionKeepAliveUpdate(finishCtx, job.sessionID, now)
}

func (s *NativeService) finishIdleKeepAliveWithoutUndo(job idleKeepAliveJob, state, note string, stop bool, stopReason string) {
	now := time.Now().UTC()
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	if record, err := s.loadSessionKeepAlive(finishCtx, job.sessionID); err == nil {
		record.LastResult = state
		record.LastNote = note
		if stop {
			s.stopIdleKeepAlive(finishCtx, &record, stopReason, note, true, now)
		} else {
			_ = s.saveSessionKeepAlive(finishCtx, record, now)
		}
	}
	_ = s.updateAutomation(finishCtx, job.intentID, state, note, now)
	s.publishAutomationReceipt("idle_keepalive", job.sessionID, state, note)
	s.publishSessionKeepAliveUpdate(finishCtx, job.sessionID, now)
}

func (s *NativeService) failIdleKeepAliveUndo(job idleKeepAliveJob, detail string) {
	now := time.Now().UTC()
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer finishCancel()
	if record, err := s.loadSessionKeepAlive(finishCtx, job.sessionID); err == nil {
		s.stopIdleKeepAlive(finishCtx, &record, "undo_failed", detail, true, now)
	}
	_ = s.updateAutomation(finishCtx, job.intentID, "failed", detail, now)
	s.publishAutomationReceipt("idle_keepalive", job.sessionID, "failed", detail)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func (s *NativeService) sessionKeepAliveView(ctx context.Context, sessionID string, now time.Time) (SessionKeepAliveView, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionKeepAliveView{}, errors.New("缺少会话 ID")
	}
	settings, err := s.automationSettings(ctx)
	if err != nil {
		settings = defaultAutomationSettings()
	}
	record, err := s.loadSessionKeepAlive(ctx, sessionID)
	if err != nil {
		return SessionKeepAliveView{}, err
	}
	snapshot, ok := s.automationSnapshot(sessionID)
	if !ok {
		if loaded, readErr := s.readSession(ctx, sessionID); readErr == nil {
			snapshot = snapshotFromCache(loaded)
			ok = true
		}
	}
	var live cache.LiveSignals
	var lastStep cache.Step
	var lastStepSeen bool
	if store, storeErr := s.cacheStore(); storeErr == nil {
		_, _ = store.Scan(ctx, now, false)
		live, _ = store.Live(ctx, sessionID)
		lastStep, lastStepSeen, _ = store.LastMainStep(ctx, sessionID)
	}
	s.syncUserActivityReset(ctx, &record, live, lastStep, lastStepSeen, now)
	policy := s.resolveSessionPolicy(ctx, sessionID, snapshot, lastStep, lastStepSeen, settings, now)

	enabled, limit, until, exhausted, _ := effectiveIdleConfig(settings, record, now)
	inFlight := s.isIdleKeepAliveInFlight(sessionID)

	state := KeepAliveStateStopped
	switch {
	case live.OpenBackground > 0:
		state = KeepAliveStateWaitingSubagent
	case ok && (snapshot.session.Busy || snapshot.session.MainTurnActive):
		state = KeepAliveStateActive
	case enabled && !exhausted && policy.IntervalMinutes > 0 && record.StoppedReason == "" && !(ok && sessionIsArchivedOrDone(snapshot.session)):
		state = KeepAliveStateIdleKeepAlive
	case settings.Compaction.Enabled && !(ok && sessionIsArchivedOrDone(snapshot.session)):
		state = KeepAliveStateIdleCompaction
	default:
		state = KeepAliveStateStopped
	}

	var remainingPtr *int
	if enabled && (record.Mode == IdleModeDefault || record.Mode == IdleModeCount) {
		rem := limit - record.UsedCount
		if rem < 0 {
			rem = 0
		}
		remainingPtr = &rem
	}

	statusLabel, statusTone := formatKeepAliveStatus(record, settings, policy, state, enabled, remainingPtr, until, exhausted, inFlight)
	estimatedCost := formatKeepAliveEstimatedCost(snapshot, policy)

	return SessionKeepAliveView{
		SessionID:        sessionID,
		State:            state,
		Mode:             record.Mode,
		TargetCount:      record.TargetCount,
		UntilAt:          record.UntilAt,
		EffectiveEnabled: enabled,
		EffectiveLimit:   limit,
		UsedCount:        record.UsedCount,
		RemainingCount:   remainingPtr,
		StatusLabel:      statusLabel,
		StatusTone:       statusTone,
		LastResult:       record.LastResult,
		LastResultLabel:  formatKeepAliveResultLabel(record.LastResult),
		LastNote:         record.LastNote,
		LastKeepAliveAt:  record.LastKeepAliveAt,
		StoppedReason:    record.StoppedReason,
		StoppedDetail:    record.StoppedDetail,
		EstimatedCost:    estimatedCost,
		Policy:           policy,
		InFlight:         inFlight,
		GlobalEnabled:    settings.IdleKeepAlive.Enabled,
		GlobalCount:      settings.IdleKeepAlive.DefaultCount,
	}, nil
}

func formatKeepAliveStatus(
	record sessionKeepAliveRecord,
	settings AutomationSettings,
	policy ResolvedCachePolicy,
	state string,
	enabled bool,
	remaining *int,
	until time.Time,
	exhausted bool,
	inFlight bool,
) (string, string) {
	if inFlight {
		return "保活中…", "active"
	}
	if state == KeepAliveStateWaitingSubagent {
		if settings.KeepAlive.Enabled {
			return "等待子代理 · 自动保活", "active"
		}
		return "等待子代理", "muted"
	}
	if record.Mode == IdleModeOff {
		return "已关闭", "muted"
	}
	if !enabled {
		return "跟随默认 · 已关闭", "muted"
	}
	switch record.StoppedReason {
	case "quota_low":
		return "已停止：额度不足", "warn"
	case "undo_failed":
		return "已停止：撤回失败", "error"
	case "undo_skipped":
		return "已停止：未自动撤回", "warn"
	case "archived":
		return "已停止：会话已归档", "muted"
	case "provider_unknown":
		return "未保活：缓存时长未知", "muted"
	case "expired":
		return "已停止：缓存已过期", "muted"
	case "error":
		return "已停止：发送失败", "error"
	}
	if policy.IntervalMinutes <= 0 {
		return "未保活：缓存时长未知", "muted"
	}
	if exhausted {
		if record.Mode == IdleModeUntil {
			return "已结束：已到截止时刻", "muted"
		}
		return "已结束：次数已用完", "muted"
	}
	if record.Mode == IdleModeUntil && !until.IsZero() {
		return "保活至 " + until.Local().Format("15:04"), "ok"
	}
	rem := 0
	if remaining != nil {
		rem = *remaining
	}
	if record.Mode == IdleModeCount {
		return fmt.Sprintf("保活 %d 次 · 还剩 %d 次", record.TargetCount, rem), "ok"
	}
	return fmt.Sprintf("跟随默认 · 还剩 %d 次", rem), "ok"
}

func formatKeepAliveResultLabel(result string) string {
	switch result {
	case keepAliveVerified:
		return "后续请求命中"
	case keepAliveInvalid:
		return "保活无效"
	case keepAliveUnknown:
		return "结果未知"
	case "acknowledged":
		return "等待回合完成"
	case "completed":
		return "已完成"
	case "undo_failed":
		return "撤回失败"
	case "failed":
		return "发送失败"
	default:
		return "尚未触发"
	}
}

func formatKeepAliveEstimatedCost(snapshot automationSnapshot, policy ResolvedCachePolicy) string {
	ctxTokens := int64(0)
	if snapshot.session.Usage != nil && snapshot.session.Usage.ContextTokens > 0 {
		ctxTokens = snapshot.session.Usage.ContextTokens
	}
	if policy.ProviderType == ProviderTypeSubscription {
		if ctxTokens > 0 {
			return fmt.Sprintf("每次约 1 次极短对话（按缓存读取计费，当前上下文约 %s tokens）", formatCompactTokens(ctxTokens))
		}
		return "每次约 1 次极短对话（按缓存读取计费，输出约 1–3 tokens）"
	}
	if policy.ProviderType == ProviderTypeOpenPlatform {
		if ctxTokens > 0 {
			return fmt.Sprintf("每次按开放平台缓存命中计费（当前上下文约 %s tokens，输出极少）", formatCompactTokens(ctxTokens))
		}
		return "每次按开放平台缓存命中计费（输出约 1–3 tokens）"
	}
	return "按所选供应商的缓存读取价格计费（输出约 1–3 tokens）"
}

func formatCompactTokens(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

func (s *NativeService) setSessionKeepAlive(ctx context.Context, input map[string]any) (SessionKeepAliveView, error) {
	id := stringInput(input, "id")
	if id == "" {
		return SessionKeepAliveView{}, errors.New("缺少会话 ID")
	}
	mode := stringInput(input, "mode")
	if mode != IdleModeDefault && mode != IdleModeOff && mode != IdleModeCount && mode != IdleModeUntil {
		return SessionKeepAliveView{}, errors.New("保活模式无效")
	}

	now := time.Now().UTC()
	record, err := s.loadSessionKeepAlive(ctx, id)
	if err != nil {
		return SessionKeepAliveView{}, err
	}
	record.Mode = mode
	if mode == IdleModeCount {
		count := intInput(input, "targetCount", record.TargetCount)
		if count < idleKeepAliveMinCount || count > idleKeepAliveMaxCount {
			return SessionKeepAliveView{}, errors.New("单会话保活次数须为 1–20 次")
		}
		record.TargetCount = count
		record.UntilAt = ""
	} else if mode == IdleModeUntil {
		rawUntil := stringInput(input, "untilAt")
		parsed := parseSortableOrRFC3339(rawUntil)
		if parsed.IsZero() {
			return SessionKeepAliveView{}, errors.New("请提供有效的截止时间")
		}
		if !parsed.After(now) {
			return SessionKeepAliveView{}, errors.New("截止时间必须晚于当前时间")
		}
		record.UntilAt = parsed.UTC().Format(time.RFC3339)
	} else {
		record.UntilAt = ""
	}
	// Changing override explicitly clears prior stop state and starts fresh for the new choice.
	record.StoppedReason = ""
	record.StoppedDetail = ""
	if mode != IdleModeOff {
		record.UsedCount = 0
		if record.AnchorAt == "" {
			record.AnchorAt = storage.SortableTime(now)
		}
	}
	if err := s.saveSessionKeepAlive(ctx, record, now); err != nil {
		return SessionKeepAliveView{}, err
	}
	s.idleMu.Lock()
	delete(s.idleAlerted, id)
	s.idleMu.Unlock()

	view, err := s.sessionKeepAliveView(ctx, id, now)
	if err != nil {
		return SessionKeepAliveView{}, err
	}
	s.bus.Publish("keepalive", map[string]any{
		"type":      "keepalive.session",
		"sessionId": id,
		"view":      view,
	})
	return view, nil
}
