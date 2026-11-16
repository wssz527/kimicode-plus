package desktop

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type managedUsageWindow struct {
	UsedRatio *float64 `json:"usedRatio"`
	ResetAt   string   `json:"resetAt"`
}

type managedUsage struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Status  int    `json:"status"`
	Quota   *struct {
		Usages map[string]managedUsageWindow `json:"usages"`
	} `json:"quota"`
}

type plannerRecoveryThread struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Model    string `json:"model"`
	Status   string `json:"status"`
	Revision uint64 `json:"revision"`
}

func parseManagedUsage(raw json.RawMessage) (managedUsage, error) {
	var usage managedUsage
	if err := json.Unmarshal(raw, &usage); err != nil {
		return usage, errors.New("官方额度响应格式无效")
	}
	return usage, nil
}

func managedQuotaAvailability(usage managedUsage, now time.Time) (bool, time.Time, bool) {
	if usage.Kind != "ok" || usage.Quota == nil {
		return false, time.Time{}, false
	}
	known := false
	available := true
	var next time.Time
	for _, window := range usage.Quota.Usages {
		if window.UsedRatio == nil {
			continue
		}
		known = true
		if *window.UsedRatio < 1 {
			continue
		}
		reset, err := time.Parse(time.RFC3339, window.ResetAt)
		if err == nil && !now.Before(reset) {
			continue
		}
		available = false
		if err == nil && (next.IsZero() || reset.Before(next)) {
			next = reset
		}
	}
	return available && known, next, known
}

func (s *NativeService) currentPlanner() PlannerActions {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.planner
}

func plannerRecoveryThreads(ctx context.Context, planner PlannerActions) ([]plannerRecoveryThread, error) {
	if planner == nil {
		return []plannerRecoveryThread{}, nil
	}
	value, err := planner.PlannerThreads(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	threads := []plannerRecoveryThread{}
	if err = json.Unmarshal(raw, &threads); err != nil {
		return nil, errors.New("规划线程响应格式无效")
	}
	return threads, nil
}

func recoveryEntryID(kind, id string) string  { return kind + ":" + id }
func recoveryIntentID(kind, id string) string { return "recovery:" + kind + ":" + id }

func recoveryEntry(entries []RecoveryEntry, kind, id string) *RecoveryEntry {
	for index := range entries {
		if entries[index].ID == recoveryEntryID(kind, id) {
			return &entries[index]
		}
	}
	return nil
}

func saveRecoveryIntent(ctx context.Context, s *NativeService, kind, id, model, revision, state, note string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO automation_intents(id,kind,host_id,session_id,model_id,context_revision,state,created_at,updated_at,result) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET model_id=excluded.model_id,context_revision=excluded.context_revision,state=excluded.state,updated_at=excluded.updated_at,result=excluded.result`,
		recoveryIntentID(kind, id), "recovery", kind, id, model, revision, state, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), note)
	return err
}

func upsertRecovery(ctx context.Context, s *NativeService, settings *AutomationSettings, kind, id, model, revision, title, status, note string, now time.Time) (bool, error) {
	entryID := recoveryEntryID(kind, id)
	entry := recoveryEntry(settings.Recovery.Entries, kind, id)
	changed := false
	nextAt := now.Add(time.Duration(settings.Recovery.IntervalSeconds) * time.Second).UTC().Format(time.RFC3339)
	var intentRevision, intentState string
	err := s.db.QueryRowContext(ctx, `SELECT context_revision,state FROM automation_intents WHERE id=?`, recoveryIntentID(kind, id)).Scan(&intentRevision, &intentState)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if entry == nil {
		if status == "WAITING" && intentRevision == revision && (intentState == "completed" || intentState == "unknown" || intentState == "cancelled") {
			return false, nil
		}
		settings.Recovery.Entries = append(settings.Recovery.Entries, RecoveryEntry{ID: entryID, Kind: kind, Title: title, Status: status, NextAt: nextAt, Note: note})
		changed = true
	} else if intentRevision != revision {
		entry.Status = status
		entry.Note = note
		entry.NextAt = nextAt
		changed = true
	}
	if changed {
		if err := saveRecoveryIntent(ctx, s, kind, id, model, revision, "waiting", note, now); err != nil {
			return false, err
		}
	}
	return changed, nil
}

func removeRecovery(entries []RecoveryEntry, kind, id string) ([]RecoveryEntry, bool) {
	for index := range entries {
		if entries[index].ID == recoveryEntryID(kind, id) {
			return append(entries[:index], entries[index+1:]...), true
		}
	}
	return entries, false
}

func (s *NativeService) watchRecovery(ctx context.Context, input map[string]any) (RecoverySettings, error) {
	s.automationMu.Lock()
	defer s.automationMu.Unlock()
	settings, err := s.automationSettings(ctx)
	if err != nil {
		return settings.Recovery, err
	}
	id, kind := stringInput(input, "id"), stringInput(input, "kind")
	if kind == "" {
		kind = "session"
	}
	if id == "" || kind != "session" && kind != "planner" {
		return settings.Recovery, errors.New("请选择有效的原生会话或规划线程")
	}
	now := time.Now().UTC()
	title, revision, model, status, note := id, "", "", "WATCHING", "已监听；只有明确的额度错误才会进入恢复等待"
	if kind == "planner" {
		planner := s.currentPlanner()
		if planner == nil {
			return settings.Recovery, errors.New("规划模块尚未启用")
		}
		value, actionErr := planner.PlannerAction(ctx, "planner.get", []byte(fmt.Sprintf(`{"id":%q}`, id)))
		if actionErr != nil {
			return settings.Recovery, actionErr
		}
		raw, _ := json.Marshal(value)
		var thread plannerRecoveryThread
		if json.Unmarshal(raw, &thread) != nil {
			return settings.Recovery, errors.New("规划线程响应格式无效")
		}
		title, model, revision = thread.Title, thread.Model, strconv.FormatUint(thread.Revision, 10)
		if thread.Status == "quota" {
			status, note = "WAITING", "规划线程因限流或额度不足暂停"
		}
	} else {
		view, viewErr := s.sessionView(ctx, id)
		if viewErr != nil {
			return settings.Recovery, viewErr
		}
		title = view.Session.Title
		snapshot, _ := s.automationSnapshot(id)
		revision = contextRevision(snapshot)
		if snapshot.quota {
			status, note = "WAITING", "官方会话最后一轮返回 429"
		}
	}
	changed, err := upsertRecovery(ctx, s, &settings, kind, id, model, revision, title, status, note, now)
	if err != nil {
		return settings.Recovery, err
	}
	if !changed {
		entry := recoveryEntry(settings.Recovery.Entries, kind, id)
		nextAt := now.Add(time.Duration(settings.Recovery.IntervalSeconds) * time.Second).Format(time.RFC3339)
		if entry == nil {
			settings.Recovery.Entries = append(settings.Recovery.Entries, RecoveryEntry{ID: recoveryEntryID(kind, id), Kind: kind, Title: title, Status: status, NextAt: nextAt, Note: note})
		} else {
			entry.Title, entry.Status, entry.Note = title, status, note
			entry.NextAt = nextAt
		}
		if err = saveRecoveryIntent(ctx, s, kind, id, model, revision, "waiting", note, now); err != nil {
			return settings.Recovery, err
		}
	}
	if err = s.storeAutomationSettings(ctx, settings); err != nil {
		return settings.Recovery, err
	}
	s.publishAutomationSettings(settings)
	return settings.Recovery, nil
}

func (s *NativeService) unwatchRecovery(ctx context.Context, input map[string]any) (RecoverySettings, error) {
	s.automationMu.Lock()
	defer s.automationMu.Unlock()
	settings, err := s.automationSettings(ctx)
	if err != nil {
		return settings.Recovery, err
	}
	id, kind := stringInput(input, "id"), stringInput(input, "kind")
	if kind == "" {
		kind = "session"
	}
	if id == "" || kind != "session" && kind != "planner" {
		return settings.Recovery, errors.New("请选择有效的原生会话或规划线程")
	}
	var revision string
	if err = s.db.QueryRowContext(ctx, `SELECT context_revision FROM automation_intents WHERE id=?`, recoveryIntentID(kind, id)).Scan(&revision); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return settings.Recovery, err
	}
	var removed bool
	settings.Recovery.Entries, removed = removeRecovery(settings.Recovery.Entries, kind, id)
	if !removed {
		return settings.Recovery, nil
	}
	now := time.Now().UTC()
	if err = saveRecoveryIntent(ctx, s, kind, id, "", revision, "cancelled", "用户已停止监听当前额度状态", now); err != nil {
		return settings.Recovery, err
	}
	if err = s.storeAutomationSettings(ctx, settings); err != nil {
		return settings.Recovery, err
	}
	s.publishAutomationSettings(settings)
	return settings.Recovery, nil
}

func (s *NativeService) recoveryCheck(ctx context.Context) (RecoverySettings, error) {
	s.automationMu.Lock()
	defer s.automationMu.Unlock()
	settings, err := s.automationSettings(ctx)
	if err != nil {
		return settings.Recovery, err
	}
	if s.safe != nil && s.safe() {
		return settings.Recovery, errors.New("安全模式下不执行额度恢复")
	}
	err = s.recoveryTick(ctx, settings, time.Now().UTC())
	updated, readErr := s.automationSettings(ctx)
	if readErr != nil {
		return settings.Recovery, readErr
	}
	return updated.Recovery, err
}

func recoveryDue(entry RecoveryEntry, now time.Time) bool {
	if entry.NextAt == "" {
		return true
	}
	next, err := time.Parse(time.RFC3339, entry.NextAt)
	return err != nil || !now.Before(next)
}

func setRecoveryState(entry *RecoveryEntry, status, note, nextAt string) bool {
	if entry.Status == status && entry.Note == note && entry.NextAt == nextAt {
		return false
	}
	entry.Status, entry.Note, entry.NextAt = status, note, nextAt
	return true
}

func (s *NativeService) recoveryTick(ctx context.Context, settings AutomationSettings, now time.Time) error {
	if !settings.Recovery.Enabled && len(settings.Recovery.Entries) == 0 {
		return nil
	}
	changed := false
	planner := s.currentPlanner()
	threads, err := plannerRecoveryThreads(ctx, planner)
	if err != nil {
		return err
	}
	threadByID := map[string]plannerRecoveryThread{}
	for _, thread := range threads {
		threadByID[thread.ID] = thread
		if thread.Status != "quota" {
			continue
		}
		updated, updateErr := upsertRecovery(ctx, s, &settings, "planner", thread.ID, thread.Model, strconv.FormatUint(thread.Revision, 10), thread.Title, "WAITING", "规划线程因限流或额度不足暂停", now)
		if updateErr != nil {
			return updateErr
		}
		changed = changed || updated
	}
	for _, id := range s.automationSessionIDs() {
		snapshot, _ := s.automationSnapshot(id)
		if !snapshot.quota {
			continue
		}
		updated, updateErr := upsertRecovery(ctx, s, &settings, "session", id, "", contextRevision(snapshot), snapshot.session.Title, "WAITING", "官方会话最后一轮返回 429", now)
		if updateErr != nil {
			return updateErr
		}
		changed = changed || updated
	}

	entries := append([]RecoveryEntry(nil), settings.Recovery.Entries...)
	var usage managedUsage
	var usageErr error
	usageLoaded := false
	loadUsage := func() {
		if usageLoaded {
			return
		}
		usageLoaded = true
		var raw json.RawMessage
		raw, usageErr = s.kap.Request(ctx, http.MethodGet, "/oauth/usage", nil)
		if usageErr == nil {
			usage, usageErr = parseManagedUsage(raw)
		}
	}
	for _, pending := range entries {
		if !recoveryDue(pending, now) {
			continue
		}
		kind, id := pending.Kind, strings.TrimPrefix(pending.ID, pending.Kind+":")
		if kind == "planner" {
			thread, exists := threadByID[id]
			if !exists || thread.Status != "quota" {
				var removed bool
				settings.Recovery.Entries, removed = removeRecovery(settings.Recovery.Entries, kind, id)
				changed = changed || removed
				if removed {
					_ = saveRecoveryIntent(ctx, s, kind, id, "", "", "cancelled", "线程已离开额度等待，不自动重发", now)
				}
				continue
			}
			revision := strconv.FormatUint(thread.Revision, 10)
			var intentRevision, intentState string
			_ = s.db.QueryRowContext(ctx, `SELECT context_revision,state FROM automation_intents WHERE id=?`, recoveryIntentID(kind, id)).Scan(&intentRevision, &intentState)
			if intentRevision != revision {
				updated, updateErr := upsertRecovery(ctx, s, &settings, kind, id, thread.Model, revision, thread.Title, "WAITING", "规划线程产生了新的额度等待", now)
				if updateErr != nil {
					return updateErr
				}
				changed = changed || updated
				continue
			}
			entry := recoveryEntry(settings.Recovery.Entries, kind, id)
			if entry == nil {
				continue
			}
			if intentState == "completed" {
				settings.Recovery.Entries, _ = removeRecovery(settings.Recovery.Entries, kind, id)
				changed = true
				continue
			}
			if intentState == "unknown" {
				changed = setRecoveryState(entry, "UNKNOWN", "核心重启后无法确认旧重试结果，请核对后重新监听", "") || changed
				continue
			}
			if !settings.Recovery.Enabled {
				changed = setRecoveryState(entry, entry.Status, entry.Note, "") || changed
				continue
			}
			entry.NextAt = now.Add(time.Duration(settings.Recovery.IntervalSeconds) * time.Second).Format(time.RFC3339)
			changed = true
			_ = saveRecoveryIntent(ctx, s, kind, id, thread.Model, revision, "pending", "受控重试已提交", now)
			_, retryErr := planner.PlannerAction(ctx, "planner.retry", []byte(fmt.Sprintf(`{"id":%q,"revision":%d}`, id, thread.Revision)))
			if retryErr != nil {
				entry.Status, entry.Note = "ERROR", retryErr.Error()
				_ = saveRecoveryIntent(ctx, s, kind, id, thread.Model, revision, "failed", retryErr.Error(), now)
				continue
			}
			settings.Recovery.Entries, _ = removeRecovery(settings.Recovery.Entries, kind, id)
			_ = saveRecoveryIntent(ctx, s, kind, id, thread.Model, revision, "completed", "规划器已接受原任务重试，未追加重复用户消息", now)
			continue
		}

		snapshot, exists := s.automationSnapshot(id)
		if !exists {
			loaded, readErr := s.readSession(ctx, id)
			if readErr != nil {
				entry := recoveryEntry(settings.Recovery.Entries, kind, id)
				if entry != nil {
					entry.Status, entry.Note = "ERROR", readErr.Error()
					entry.NextAt = now.Add(time.Duration(settings.Recovery.IntervalSeconds) * time.Second).Format(time.RFC3339)
					changed = true
				}
				continue
			}
			loaded.revision = 1
			loaded.lastActivity = now
			s.mu.Lock()
			s.cache[id] = loaded
			s.mu.Unlock()
			_ = s.kap.Watch(id)
			snapshot = snapshotFromCache(loaded)
		} else {
			snapshot, exists, err = s.refreshAutomationSnapshot(ctx, id, now)
			if err != nil || !exists {
				continue
			}
		}
		entry := recoveryEntry(settings.Recovery.Entries, kind, id)
		if entry == nil {
			continue
		}
		if !snapshot.quota {
			if entry.Status != "WATCHING" {
				settings.Recovery.Entries, _ = removeRecovery(settings.Recovery.Entries, kind, id)
				changed = true
				_ = saveRecoveryIntent(ctx, s, kind, id, "", contextRevision(snapshot), "cancelled", "会话已由新活动接管或不再处于额度错误，不自动续跑", now)
			}
			continue
		}
		revision := contextRevision(snapshot)
		var intentRevision, intentState string
		_ = s.db.QueryRowContext(ctx, `SELECT context_revision,state FROM automation_intents WHERE id=?`, recoveryIntentID(kind, id)).Scan(&intentRevision, &intentState)
		if intentRevision != revision {
			updated, updateErr := upsertRecovery(ctx, s, &settings, kind, id, "", revision, snapshot.session.Title, "WAITING", "官方会话产生了新的额度等待", now)
			if updateErr != nil {
				return updateErr
			}
			changed = changed || updated
			continue
		}
		if intentState == "completed" {
			settings.Recovery.Entries, _ = removeRecovery(settings.Recovery.Entries, kind, id)
			changed = true
			continue
		}
		if intentState == "unknown" {
			changed = setRecoveryState(entry, "UNKNOWN", "核心重启后无法确认旧续跑结果，请核对后重新监听", "") || changed
			continue
		}
		if goalAutomationBlocked(snapshot) {
			note := "Goal 已停止或达到硬预算，未自动续跑"
			if !snapshot.goalKnown {
				note = "Goal 状态不可用，无法确认硬预算，未自动续跑"
			}
			if setRecoveryState(entry, "BLOCKED", note, "") {
				changed = true
				_ = saveRecoveryIntent(ctx, s, kind, id, "", revision, "blocked", entry.Note, now)
			}
			continue
		}
		if snapshot.session.Busy || snapshot.session.MainTurnActive || pendingNativeInteraction(snapshot) {
			changed = setRecoveryState(entry, "PAUSED", "会话运行中或等待审批，不自动续跑", entry.NextAt) || changed
			continue
		}
		if !settings.Recovery.Enabled {
			changed = setRecoveryState(entry, entry.Status, entry.Note, "") || changed
			continue
		}
		loadUsage()
		if usageErr != nil {
			entry.Status, entry.Note = "ERROR", usageErr.Error()
			entry.NextAt = now.Add(time.Duration(settings.Recovery.IntervalSeconds) * time.Second).Format(time.RFC3339)
			changed = true
			continue
		}
		available, reset, known := managedQuotaAvailability(usage, now)
		if !available {
			entry.Status = "WAITING"
			entry.Note = "官方额度尚未明确恢复"
			if !known && usage.Kind == "error" {
				entry.Note = "官方额度读取失败：" + usage.Message
			}
			entry.NextAt = now.Add(time.Duration(settings.Recovery.IntervalSeconds) * time.Second).Format(time.RFC3339)
			if !reset.IsZero() {
				entry.NextAt = reset.Format(time.RFC3339)
			}
			changed = true
			continue
		}
		_ = saveRecoveryIntent(ctx, s, kind, id, "", revision, "pending", "额度已明确可用，提交续跑", now)
		_, sendErr := s.sendPrompt(ctx, map[string]any{"id": id, "text": "额度已恢复，请继续上一个未完成的任务。先核对已有结果和工具状态，不要重复已完成的操作。", "mode": "inherit"})
		if sendErr != nil {
			entry.Status, entry.Note = "ERROR", sendErr.Error()
			entry.NextAt = now.Add(time.Duration(settings.Recovery.IntervalSeconds) * time.Second).Format(time.RFC3339)
			changed = true
			_ = saveRecoveryIntent(ctx, s, kind, id, "", revision, "failed", sendErr.Error(), now)
			continue
		}
		settings.Recovery.Entries, _ = removeRecovery(settings.Recovery.Entries, kind, id)
		changed = true
		_ = saveRecoveryIntent(ctx, s, kind, id, "", revision, "completed", "续跑消息已被官方接收；任务结果仍以会话事件为准", now)
	}
	if changed {
		if err = s.storeAutomationSettings(ctx, settings); err != nil {
			return err
		}
		s.publishAutomationSettings(settings)
	}
	return nil
}

func managedUsageMonitor(raw json.RawMessage) map[string]any {
	usage, err := parseManagedUsage(raw)
	if err != nil {
		return map[string]any{"state": "UNKNOWN", "source": "官方 /oauth/usage", "note": "响应缺少结构化额度数据"}
	}
	if usage.Kind == "error" {
		note := usage.Message
		if usage.Status != 0 {
			note = fmt.Sprintf("%s（HTTP %d）", note, usage.Status)
		}
		return map[string]any{"state": "ERROR", "source": "官方 /oauth/usage", "note": note}
	}
	if usage.Quota == nil {
		return map[string]any{"state": "UNKNOWN", "source": "官方 /oauth/usage", "note": "响应缺少结构化额度数据"}
	}
	if usage.Kind == "ok" {
		return map[string]any{"state": "AVAILABLE", "source": "官方 /oauth/usage", "value": rawAsAny(raw)}
	}
	return map[string]any{"state": "UNKNOWN", "source": "官方 /oauth/usage", "note": "额度响应种类未知"}
}
