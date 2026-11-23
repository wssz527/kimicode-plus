package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"enhancedkimi/internal/security"
	"enhancedkimi/internal/storage"
)

type Option func(*Engine)

func WithNow(fn func() time.Time) Option {
	return func(e *Engine) {
		if fn != nil {
			e.now = fn
		}
	}
}

func WithLocation(loc *time.Location) Option {
	return func(e *Engine) {
		if loc != nil {
			e.loc = loc
		}
	}
}

func WithMaxConcurrent(n int) Option {
	return func(e *Engine) {
		if n > 0 {
			e.maxConcurrent = clampInt(n, 1, MaxAllowedConcurrent)
			e.maxConcurrentOverride = true
		}
	}
}

func WithTickInterval(d time.Duration) Option {
	return func(e *Engine) {
		if d > 0 {
			e.tickInterval = d
		}
	}
}

func WithBusyWaitTimeout(d time.Duration) Option {
	return func(e *Engine) {
		if d > 0 {
			e.busyWaitTimeout = d
		}
	}
}

func WithPollInterval(d time.Duration) Option {
	return func(e *Engine) {
		if d > 0 {
			e.pollInterval = d
		}
	}
}

func WithCatchupGrace(d time.Duration) Option {
	return func(e *Engine) {
		if d >= 0 {
			e.catchupGrace = d
		}
	}
}

func WithPluginEnabled(fn func(pluginID string) bool) Option {
	return func(e *Engine) {
		if fn != nil {
			e.pluginEnabled = fn
		}
	}
}

func WithDefaultCWD(cwd string) Option {
	return func(e *Engine) {
		e.defaultCWD = strings.TrimSpace(cwd)
	}
}

func WithTimeoutScale(d time.Duration) Option {
	return func(e *Engine) {
		if d > 0 {
			e.timeoutScale = d
		}
	}
}

func WithGitChecker(fn func(ctx context.Context, cwd string) GitWorktreeStatus) Option {
	return func(e *Engine) {
		if fn != nil {
			e.gitChecker = fn
		}
	}
}

type Engine struct {
	store *Store
	exec  Executor

	now                   func() time.Time
	loc                   *time.Location
	maxConcurrent         int
	maxConcurrentOverride bool
	tickInterval          time.Duration
	busyWaitTimeout       time.Duration
	pollInterval          time.Duration
	catchupGrace          time.Duration
	timeoutScale          time.Duration
	pluginEnabled         func(string) bool
	gitChecker            func(ctx context.Context, cwd string) GitWorktreeStatus
	defaultCWD            string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	slotMu       sync.Mutex
	runningSlots int
	slotWaiters  []chan struct{}

	mu          sync.Mutex
	activeTasks map[string]activeRun
	closed      bool
	pauseCount  int
}

type activeRun struct {
	pluginID string
	runID    string
	cancel   context.CancelFunc
}

type SaveTaskInput struct {
	ID               string       `json:"id,omitempty"`
	Name             string       `json:"name"`
	Prompt           string       `json:"prompt"`
	CWD              string       `json:"cwd"`
	Mode             string       `json:"mode"`
	SessionID        string       `json:"sessionId,omitempty"`
	Model            string       `json:"model,omitempty"`
	Effort           string       `json:"effort,omitempty"`
	PermissionMode   string       `json:"permissionMode"`
	Schedule         ScheduleRule `json:"schedule"`
	MissedPolicy     string       `json:"missedPolicy"`
	ArchiveOnRead    *bool        `json:"archiveOnRead,omitempty"`
	TimeoutMinutes   *int         `json:"timeoutMinutes,omitempty"`
	MaxTurns         *int         `json:"maxTurns,omitempty"`
	ConfirmDirtyYolo bool         `json:"confirmDirtyYolo,omitempty"`
	Enabled          *bool        `json:"enabled,omitempty"`
}

func NewEngine(store *Store, exec Executor, opts ...Option) (*Engine, error) {
	if store == nil {
		return nil, errors.New("scheduler engine requires a store")
	}
	if exec == nil {
		return nil, errors.New("scheduler engine requires an executor")
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{
		store:           store,
		exec:            exec,
		now:             time.Now,
		loc:             time.Local,
		maxConcurrent:   DefaultMaxConcurrent,
		tickInterval:    5 * time.Second,
		busyWaitTimeout: 10 * time.Minute,
		pollInterval:    1 * time.Second,
		catchupGrace:    90 * time.Second,
		timeoutScale:    time.Minute,
		pluginEnabled:   func(string) bool { return true },
		gitChecker:      CheckGitWorktree,
		ctx:             ctx,
		cancel:          cancel,
		activeTasks:     map[string]activeRun{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	if _, err := store.RecoverInFlight(context.Background(), e.now().UTC()); err != nil {
		cancel()
		return nil, err
	}
	return e, nil
}

func (e *Engine) SetPluginEnabledChecker(fn func(pluginID string) bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if fn != nil {
		e.pluginEnabled = fn
	}
}

func (e *Engine) isPluginEnabled(pluginID string) bool {
	e.mu.Lock()
	fn := e.pluginEnabled
	e.mu.Unlock()
	if fn == nil {
		return true
	}
	return fn(pluginID)
}

func (e *Engine) effectiveMaxConcurrent(ctx context.Context, pluginID string) int {
	prefs := e.store.GetPrefs(ctx, pluginID)
	if prefs.MaxConcurrent >= 1 && prefs.MaxConcurrent <= MaxAllowedConcurrent {
		if e.maxConcurrentOverride && prefs.MaxConcurrent == DefaultMaxConcurrent && e.maxConcurrent != DefaultMaxConcurrent {
			return e.maxConcurrent
		}
		return prefs.MaxConcurrent
	}
	if e.maxConcurrent > 0 {
		return e.maxConcurrent
	}
	return DefaultMaxConcurrent
}

func (e *Engine) acquireSlot(ctx context.Context, limit int) bool {
	if limit < 1 {
		limit = 1
	}
	for {
		e.slotMu.Lock()
		if e.runningSlots < limit {
			e.runningSlots++
			e.slotMu.Unlock()
			return true
		}
		ch := make(chan struct{})
		e.slotWaiters = append(e.slotWaiters, ch)
		e.slotMu.Unlock()

		select {
		case <-ch:
		case <-ctx.Done():
			return false
		}
	}
}

func (e *Engine) releaseSlot() {
	e.slotMu.Lock()
	if e.runningSlots > 0 {
		e.runningSlots--
	}
	waiters := e.slotWaiters
	e.slotWaiters = nil
	e.slotMu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

func (e *Engine) wakeSlotWaiters() {
	e.slotMu.Lock()
	waiters := e.slotWaiters
	e.slotWaiters = nil
	e.slotMu.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

func (e *Engine) Start() {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		ticker := time.NewTicker(e.tickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-e.ctx.Done():
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(e.ctx, 30*time.Second)
				_ = e.Tick(ctx)
				cancel()
			}
		}
	}()
}

func (e *Engine) Close() {
	e.mu.Lock()
	e.closed = true
	cancels := make([]context.CancelFunc, 0, len(e.activeTasks))
	for _, item := range e.activeTasks {
		cancels = append(cancels, item.cancel)
	}
	e.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	e.cancel()
	e.wakeSlotWaiters()
	e.wg.Wait()
}

func (e *Engine) StopPlugin(ctx context.Context, pluginID string) error {
	e.mu.Lock()
	cancels := []context.CancelFunc{}
	for _, item := range e.activeTasks {
		if item.pluginID == pluginID {
			cancels = append(cancels, item.cancel)
		}
	}
	e.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	return nil
}

func (e *Engine) DeletePluginData(ctx context.Context, pluginID string) error {
	_ = e.StopPlugin(ctx, pluginID)
	if err := e.store.DeletePluginData(ctx, pluginID); err != nil {
		return err
	}
	e.publishScheduleUpdate(pluginID, "plugin.cleared", "")
	return nil
}

// Pause temporarily suspends scheduled ticks (for example while switching
// accounts) and returns an idempotent resume callback.
func (e *Engine) Pause() func() {
	e.mu.Lock()
	e.pauseCount++
	e.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			e.mu.Lock()
			if e.pauseCount > 0 {
				e.pauseCount--
			}
			e.mu.Unlock()
		})
	}
}

func (e *Engine) Paused() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pauseCount > 0
}

func (e *Engine) Tick(ctx context.Context) error {
	e.mu.Lock()
	closed := e.closed
	paused := e.pauseCount > 0
	e.mu.Unlock()
	if closed || paused || e.ctx.Err() != nil {
		return nil
	}
	now := e.now().UTC()
	due, err := e.store.DueTasks(ctx, now)
	if err != nil {
		return err
	}
	if len(due) == 0 {
		return nil
	}
	for _, task := range due {
		if !e.isPluginEnabled(task.PluginID) {
			continue
		}
		scheduledAt, _ := storage.ParseSortableTime(task.NextRunAt)
		nextTime, hasNext := NextRun(task.Schedule, now, e.loc)
		nextStr := ""
		if hasNext {
			nextStr = storage.SortableTime(nextTime)
		}
		nextEnabled := task.Enabled && (task.Schedule.Kind != "once" || hasNext)

		if e.isTaskActive(task.ID) {
			if err := e.store.UpdateTaskSchedule(ctx, task.ID, nextEnabled, nextStr, now); err == nil {
				e.recordSkipped(ctx, task, TriggerScheduled, task.NextRunAt, FailureOverlap, "上一次运行尚未结束，已跳过本次重叠调度", now)
			}
			continue
		}

		missed := !scheduledAt.IsZero() && now.Sub(scheduledAt) > e.catchupGrace
		if missed && task.MissedPolicy == MissedSkip {
			if err := e.store.UpdateTaskSchedule(ctx, task.ID, nextEnabled, nextStr, now); err == nil {
				label := scheduledAt.In(e.loc).Format("2006-01-02 15:04")
				e.recordSkipped(ctx, task, TriggerScheduled, task.NextRunAt, FailureMissed, fmt.Sprintf("错过预定时间（%s），按任务设置已跳过", label), now)
			}
			continue
		}

		limit := e.effectiveMaxConcurrent(ctx, task.PluginID)
		if e.activeTaskCount() >= limit {
			if err := e.store.UpdateTaskSchedule(ctx, task.ID, nextEnabled, nextStr, now); err == nil {
				e.recordSkipped(ctx, task, TriggerScheduled, task.NextRunAt, FailureConcurrencyLimit,
					fmt.Sprintf("当前并发运行任务已达全局上限（%d），已跳过本次调度", limit), now)
			}
			continue
		}

		if err := e.store.UpdateTaskSchedule(ctx, task.ID, nextEnabled, nextStr, now); err != nil {
			continue
		}
		triggerKind := TriggerScheduled
		if missed {
			triggerKind = TriggerCatchup
		}
		_, _ = e.dispatch(ctx, task, triggerKind, task.NextRunAt)
	}
	return nil
}

func (e *Engine) recordSkipped(ctx context.Context, task Task, triggerKind, scheduledFor, failureKind, message string, now time.Time) {
	stamp := storage.SortableTime(now)
	run := RunRecord{
		ID:             newID("run"),
		TaskID:         task.ID,
		PluginID:       task.PluginID,
		TaskName:       task.Name,
		TriggerKind:    triggerKind,
		Mode:           task.Mode,
		SessionID:      task.SessionID,
		Model:          task.Model,
		Effort:         task.Effort,
		PermissionMode: task.PermissionMode,
		Status:         RunSkipped,
		FailureKind:    failureKind,
		Message:        message,
		Unread:         true,
		ScheduledFor:   scheduledFor,
		StartedAt:      stamp,
		FinishedAt:     stamp,
	}
	_ = e.store.InsertRun(ctx, run)
	_ = e.store.UpdateTaskLastResult(ctx, task.ID, stamp, RunSkipped, message, "", now)
	e.publishScheduleUpdate(task.PluginID, "run.finished", run.ID)
	e.maybePublishNotification(run)
}

func (e *Engine) isTaskActive(taskID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.activeTasks[taskID]
	return ok
}

func (e *Engine) activeTaskCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.activeTasks)
}

func (e *Engine) runningTaskIDs(pluginID string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []string{}
	for taskID, item := range e.activeTasks {
		if item.pluginID == pluginID {
			out = append(out, taskID)
		}
	}
	return out
}

func (e *Engine) SaveTask(ctx context.Context, pluginID string, input SaveTaskInput) (TaskView, error) {
	now := e.now().UTC()
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > 120 {
		return TaskView{}, errors.New("任务名称必须为 1–120 个字符")
	}
	prompt := strings.TrimSpace(input.Prompt)
	if prompt == "" || len(prompt) > 200000 {
		return TaskView{}, errors.New("提示词必须为 1–200000 字节")
	}
	mode := strings.TrimSpace(input.Mode)
	if mode == "" {
		mode = ModeNewSession
	}
	if mode != ModeNewSession && mode != ModeBoundSession {
		return TaskView{}, errors.New("执行方式必须是 new_session 或 bound_session")
	}

	sessionID := strings.TrimSpace(input.SessionID)
	cwd := strings.TrimSpace(input.CWD)
	if mode == ModeBoundSession {
		if sessionID == "" {
			return TaskView{}, errors.New("绑定会话模式需要选择目标会话")
		}
		if connected, _ := e.exec.KAPConnected(ctx); connected {
			sess, err := e.exec.GetSession(ctx, sessionID)
			if err != nil {
				return TaskView{}, fmt.Errorf("无法读取绑定会话：%w", err)
			}
			if cwd == "" {
				cwd = strings.TrimSpace(sess.CWD)
			}
		}
		if cwd == "" {
			cwd = e.defaultCWD
		}
	} else {
		sessionID = ""
		if cwd == "" {
			cwd = e.defaultCWD
		}
		if cwd == "" || !filepath.IsAbs(cwd) {
			return TaskView{}, errors.New("项目目录必须是本机绝对路径")
		}
		info, err := os.Stat(cwd)
		if err != nil || !info.IsDir() {
			return TaskView{}, errors.New("项目目录不存在或不是文件夹")
		}
		cwd = filepath.Clean(cwd)
	}

	permissionMode := strings.TrimSpace(input.PermissionMode)
	if permissionMode == "" {
		permissionMode = "manual"
	}
	if permissionMode != "manual" && permissionMode != "yolo" && permissionMode != "auto" {
		return TaskView{}, errors.New("权限模式只支持 manual、yolo 或 auto")
	}

	allowDirtyYolo := false
	if permissionMode == "yolo" && cwd != "" {
		gitStatus := e.gitChecker(ctx, cwd)
		if gitStatus.IsGitRepo && !gitStatus.Clean {
			if !input.ConfirmDirtyYolo {
				return TaskView{}, fmt.Errorf("项目目录是 Git 仓库且工作区存在 %d 处未提交改动。使用「始终允许（免审批）」可能修改或覆盖未提交文件，请先清理工作区，或在勾选风险确认后再保存", max(1, gitStatus.DirtyCount))
			}
			allowDirtyYolo = true
		} else if input.ConfirmDirtyYolo {
			allowDirtyYolo = true
		}
	}

	missedPolicy := strings.TrimSpace(input.MissedPolicy)
	if missedPolicy == "" {
		missedPolicy = MissedCatchup
	}
	if missedPolicy != MissedCatchup && missedPolicy != MissedSkip {
		return TaskView{}, errors.New("错过运行策略只支持 catchup 或 skip")
	}

	rule, err := ValidateRule(input.Schedule)
	if err != nil {
		return TaskView{}, err
	}

	model := strings.TrimSpace(input.Model)
	effort := strings.TrimSpace(input.Effort)
	if model != "" {
		if models, mErr := e.exec.Models(ctx); mErr == nil && len(models) > 0 {
			var matched *ModelInfo
			for i := range models {
				if models[i].ID == model {
					matched = &models[i]
					break
				}
			}
			if matched == nil {
				return TaskView{}, errors.New("所选模型不在当前可用模型列表中")
			}
			if effort != "" && effort != "off" && effort != "on" && len(matched.SupportEfforts) > 0 {
				allowed := false
				for _, item := range matched.SupportEfforts {
					if item == effort {
						allowed = true
						break
					}
				}
				if !allowed {
					return TaskView{}, fmt.Errorf("模型 %s 不支持推理强度 %s", matched.Name, effort)
				}
			}
		}
	} else {
		effort = ""
	}

	var existing *Task
	id := strings.TrimSpace(input.ID)
	if id != "" {
		found, getErr := e.store.GetTask(ctx, pluginID, id)
		if getErr != nil {
			return TaskView{}, getErr
		}
		existing = &found
	} else {
		id = newID("task")
	}

	archiveOnRead := mode == ModeNewSession
	if input.ArchiveOnRead != nil {
		archiveOnRead = *input.ArchiveOnRead && mode == ModeNewSession
	} else if existing != nil {
		archiveOnRead = existing.ArchiveOnRead && mode == ModeNewSession
	}

	timeoutMinutes := DefaultTimeoutMinutes
	if existing != nil && existing.TimeoutMinutes > 0 {
		timeoutMinutes = existing.TimeoutMinutes
	}
	if input.TimeoutMinutes != nil {
		if *input.TimeoutMinutes < 1 || *input.TimeoutMinutes > MaxTimeoutMinutes {
			return TaskView{}, fmt.Errorf("单次超时必须在 1–%d 分钟之间", MaxTimeoutMinutes)
		}
		timeoutMinutes = *input.TimeoutMinutes
	}

	maxTurns := 0
	if existing != nil {
		maxTurns = existing.MaxTurns
	}
	if input.MaxTurns != nil {
		if *input.MaxTurns < 0 || *input.MaxTurns > 500 {
			return TaskView{}, errors.New("最大回合数必须在 0–500 之间（0 表示不限）")
		}
		maxTurns = *input.MaxTurns
	}

	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	} else if existing != nil {
		enabled = existing.Enabled
	}

	nextRunAt := ""
	if enabled {
		nextTime, hasNext := NextRun(rule, now, e.loc)
		if !hasNext {
			if rule.Kind == "once" {
				return TaskView{}, errors.New("单次调度时间必须晚于当前时间")
			}
			return TaskView{}, errors.New("当前调度规则在有效日期范围内没有可执行时间")
		}
		nextRunAt = storage.SortableTime(nextTime)
	}

	stamp := storage.SortableTime(now)
	task := Task{
		ID:             id,
		PluginID:       pluginID,
		Name:           name,
		Prompt:         prompt,
		CWD:            cwd,
		Mode:           mode,
		SessionID:      sessionID,
		Model:          model,
		Effort:         effort,
		PermissionMode: permissionMode,
		Schedule:       rule,
		MissedPolicy:   missedPolicy,
		ArchiveOnRead:  archiveOnRead,
		TimeoutMinutes: timeoutMinutes,
		MaxTurns:       maxTurns,
		AllowDirtyYolo: allowDirtyYolo,
		Enabled:        enabled,
		NextRunAt:      nextRunAt,
		CreatedAt:      stamp,
		UpdatedAt:      stamp,
	}
	if existing != nil {
		task.LastRunAt = existing.LastRunAt
		task.LastStatus = existing.LastStatus
		task.LastSummary = existing.LastSummary
		task.LastSessionID = existing.LastSessionID
		task.CreatedAt = existing.CreatedAt
	}
	if err := e.store.SaveTask(ctx, task); err != nil {
		return TaskView{}, err
	}
	e.publishScheduleUpdate(pluginID, "task.saved", task.ID)
	return e.buildTaskView(ctx, task), nil
}

func (e *Engine) ToggleTask(ctx context.Context, pluginID, id string, enabled bool) (TaskView, error) {
	task, err := e.store.GetTask(ctx, pluginID, id)
	if err != nil {
		return TaskView{}, err
	}
	now := e.now().UTC()
	nextRunAt := ""
	if enabled {
		nextTime, hasNext := NextRun(task.Schedule, now, e.loc)
		if !hasNext {
			if task.Schedule.Kind == "once" {
				return TaskView{}, errors.New("单次任务时间已过，请编辑新的执行时间后再启用")
			}
			return TaskView{}, errors.New("当前调度规则在有效日期范围内没有可执行时间")
		}
		nextRunAt = storage.SortableTime(nextTime)
	}
	if err := e.store.UpdateTaskSchedule(ctx, task.ID, enabled, nextRunAt, now); err != nil {
		return TaskView{}, err
	}
	task.Enabled = enabled
	task.NextRunAt = nextRunAt
	task.UpdatedAt = storage.SortableTime(now)
	e.publishScheduleUpdate(pluginID, "task.toggled", task.ID)
	return e.buildTaskView(ctx, task), nil
}

func (e *Engine) DeleteTask(ctx context.Context, pluginID, id string) error {
	e.mu.Lock()
	active, running := e.activeTasks[id]
	e.mu.Unlock()
	if running && active.pluginID == pluginID {
		active.cancel()
	}
	if err := e.store.DeleteTask(ctx, pluginID, id); err != nil {
		return err
	}
	e.publishScheduleUpdate(pluginID, "task.deleted", id)
	return nil
}

func (e *Engine) RunNow(ctx context.Context, pluginID, id string) (RunRecord, error) {
	if e.Paused() {
		return RunRecord{}, errors.New("账号切换中，定时任务已暂停，请稍后重试")
	}
	task, err := e.store.GetTask(ctx, pluginID, id)
	if err != nil {
		return RunRecord{}, err
	}
	if !e.isPluginEnabled(pluginID) {
		return RunRecord{}, errors.New("插件未启用，无法运行任务")
	}
	if e.isTaskActive(task.ID) {
		return RunRecord{}, errors.New("该任务正在运行中，请等待当前运行结束")
	}
	return e.dispatch(ctx, task, TriggerManual, "")
}

func (e *Engine) ListTaskViews(ctx context.Context, pluginID string) ([]TaskView, error) {
	tasks, err := e.store.ListTasks(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	sessionTitles := map[string]string{}
	if connected, _ := e.exec.KAPConnected(ctx); connected {
		if sessions, sErr := e.exec.Sessions(ctx); sErr == nil {
			for _, s := range sessions {
				sessionTitles[s.ID] = s.Title
			}
		}
	}
	boundIDs := []string{}
	for _, t := range tasks {
		if t.Mode == ModeBoundSession && t.SessionID != "" {
			boundIDs = append(boundIDs, t.SessionID)
		}
	}
	stats, keepAliveEnabled, keepAliveMinutes := e.exec.SessionCacheStats(ctx, boundIDs)
	views := make([]TaskView, 0, len(tasks))
	for _, t := range tasks {
		views = append(views, enrichTaskView(t, sessionTitles, stats, keepAliveEnabled, keepAliveMinutes))
	}
	return views, nil
}

func (e *Engine) buildTaskView(ctx context.Context, task Task) TaskView {
	sessionTitles := map[string]string{}
	boundIDs := []string{}
	if task.Mode == ModeBoundSession && task.SessionID != "" {
		boundIDs = append(boundIDs, task.SessionID)
		if sess, err := e.exec.GetSession(ctx, task.SessionID); err == nil {
			sessionTitles[sess.ID] = sess.Title
		}
	}
	stats, keepAliveEnabled, keepAliveMinutes := e.exec.SessionCacheStats(ctx, boundIDs)
	return enrichTaskView(task, sessionTitles, stats, keepAliveEnabled, keepAliveMinutes)
}

func enrichTaskView(task Task, sessionTitles map[string]string, stats map[string]SessionCacheStat, keepAliveEnabled bool, keepAliveMinutes int) TaskView {
	view := TaskView{
		Task:             task,
		ScheduleSummary:  FormatScheduleSummary(task.Schedule),
		KeepAliveMinutes: keepAliveMinutes,
		KeepAliveEnabled: keepAliveEnabled,
	}
	if task.Mode == ModeBoundSession && task.SessionID != "" {
		view.SessionTitle = sessionTitles[task.SessionID]
		if stat, ok := stats[task.SessionID]; ok {
			if stat.KeepAliveMinutes > 0 {
				view.KeepAliveMinutes = stat.KeepAliveMinutes
			}
			view.KeepAliveEnabled = stat.KeepAliveEnabled
			if stat.LastRequestAt != nil {
				iso := storage.SortableTime(*stat.LastRequestAt)
				view.SessionLastRequestAt = &iso
			}
			if stat.IdleMinutes != nil {
				idle := *stat.IdleMinutes
				view.SessionIdleMinutes = &idle
				if view.KeepAliveMinutes > 0 {
					within := idle <= float64(view.KeepAliveMinutes)
					view.WithinKeepAlive = &within
				}
			}
		}
	}
	return view
}

func (e *Engine) dispatch(ctx context.Context, task Task, triggerKind, scheduledFor string) (RunRecord, error) {
	now := e.now().UTC()
	stamp := storage.SortableTime(now)
	run := RunRecord{
		ID:             newID("run"),
		TaskID:         task.ID,
		PluginID:       task.PluginID,
		TaskName:       task.Name,
		TriggerKind:    triggerKind,
		Mode:           task.Mode,
		SessionID:      task.SessionID,
		Model:          task.Model,
		Effort:         task.Effort,
		PermissionMode: task.PermissionMode,
		Status:         RunQueued,
		Message:        "等待执行槽位…",
		Unread:         true,
		ScheduledFor:   scheduledFor,
		StartedAt:      stamp,
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return RunRecord{}, errors.New("调度引擎已关闭")
	}
	if _, exists := e.activeTasks[task.ID]; exists {
		e.mu.Unlock()
		return RunRecord{}, errors.New("该任务正在运行中")
	}
	runCtx, runCancel := context.WithCancel(e.ctx)
	e.activeTasks[task.ID] = activeRun{pluginID: task.PluginID, runID: run.ID, cancel: runCancel}
	e.mu.Unlock()

	if err := e.store.InsertRun(ctx, run); err != nil {
		e.clearActive(task.ID, run.ID)
		runCancel()
		return RunRecord{}, err
	}
	_ = e.store.UpdateTaskLastResult(ctx, task.ID, stamp, RunQueued, run.Message, "", now)
	e.publishScheduleUpdate(task.PluginID, "run.queued", run.ID)

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer runCancel()
		defer e.clearActive(task.ID, run.ID)
		e.executeRun(runCtx, task, run)
	}()
	return run, nil
}

func (e *Engine) clearActive(taskID, runID string) {
	e.mu.Lock()
	if cur, ok := e.activeTasks[taskID]; ok && cur.runID == runID {
		delete(e.activeTasks, taskID)
	}
	e.mu.Unlock()
}

func (e *Engine) preflightCheck(ctx context.Context, task Task) (bool, string) {
	connected, connReason := e.exec.KAPConnected(ctx)
	if !connected {
		detail := strings.TrimSpace(connReason)
		if detail == "" {
			detail = "本地服务未连接"
		}
		return false, fmt.Sprintf("运行前校验失败：官方客户端尚未接入（%s）。下一步：请启动 Kimi Code 并在「设置 → KimiCode+ → 状态与诊断」中重新接入或开启自动接入。", detail)
	}

	validAccount, accReason := e.exec.CheckAccount(ctx)
	if !validAccount {
		detail := strings.TrimSpace(accReason)
		if detail == "" {
			detail = "账号未登录或凭据已失效"
		}
		return false, fmt.Sprintf("运行前校验失败：当前账号不可用（%s）。下一步：请打开「设置 → 账户」重新登录或切换至有效账号。", detail)
	}

	dirToCheck := strings.TrimSpace(task.CWD)
	if task.Mode == ModeBoundSession {
		sess, err := e.exec.GetSession(ctx, task.SessionID)
		if err != nil {
			return false, fmt.Sprintf("运行前校验失败：绑定的会话（%s）不存在或无法读取（%s）。下一步：请编辑任务重新绑定会话，或改为「新开会话」模式。", task.SessionID, err.Error())
		}
		if dirToCheck == "" {
			dirToCheck = strings.TrimSpace(sess.CWD)
		}
	}
	if task.Mode == ModeNewSession || dirToCheck != "" {
		if dirToCheck == "" || !filepath.IsAbs(dirToCheck) {
			return false, "运行前校验失败：项目目录无效。下一步：请编辑该任务并选择有效的本机绝对目录。"
		}
		info, err := os.Stat(dirToCheck)
		if err != nil || !info.IsDir() {
			return false, fmt.Sprintf("运行前校验失败：项目目录「%s」不存在或不是文件夹。下一步：请恢复该目录，或编辑任务重新选择工作目录。", dirToCheck)
		}
	}

	if model := strings.TrimSpace(task.Model); model != "" {
		models, err := e.exec.Models(ctx)
		if err != nil {
			return false, fmt.Sprintf("运行前校验失败：无法读取官方模型列表（%s）。下一步：请确认官方客户端服务正常后重试。", err.Error())
		}
		found := false
		for _, item := range models {
			if item.ID == model {
				found = true
				break
			}
		}
		if !found {
			return false, fmt.Sprintf("运行前校验失败：任务指定的模型「%s」已不在官方可用模型列表中。下一步：请编辑任务重新选择可用模型，或在「设置 → 模型供应商」中恢复该模型。", model)
		}
	}

	if task.PermissionMode == "yolo" && !task.AllowDirtyYolo && dirToCheck != "" {
		gitStatus := e.gitChecker(ctx, dirToCheck)
		if gitStatus.IsGitRepo && !gitStatus.Clean {
			return false, fmt.Sprintf("运行前校验失败：任务使用「始终允许（免审批）」，但 Git 工作区存在 %d 处未提交改动。下一步：请先提交或暂存工作区改动，或在编辑任务中勾选风险确认后保存。", max(1, gitStatus.DirtyCount))
		}
	}

	return true, ""
}

func (e *Engine) executeRun(ctx context.Context, task Task, run RunRecord) {
	limit := e.effectiveMaxConcurrent(context.Background(), task.PluginID)
	if !e.acquireSlot(ctx, limit) {
		e.finishRun(task, run, RunSkipped, FailureCancelled, "任务已取消或插件已停用", "")
		return
	}
	defer e.releaseSlot()

	if !e.isPluginEnabled(task.PluginID) {
		e.finishRun(task, run, RunSkipped, FailureCancelled, "插件已停用，已取消本次运行", "")
		return
	}
	if e.Paused() {
		e.finishRun(task, run, RunSkipped, FailureCancelled, "账号切换中，定时任务已暂停", "")
		return
	}

	run.Status = RunRunning
	run.Message = "正在执行运行前校验…"
	_ = e.store.UpdateRun(context.Background(), run)
	_ = e.store.UpdateTaskLastResult(context.Background(), task.ID, run.StartedAt, RunRunning, run.Message, "", e.now().UTC())
	e.publishScheduleUpdate(task.PluginID, "run.started", run.ID)

	if ok, preflightMsg := e.preflightCheck(ctx, task); !ok {
		e.finishRun(task, run, RunFailed, FailurePreflight, preflightMsg, "")
		return
	}

	prefs := e.store.GetPrefs(context.Background(), task.PluginID)
	if prefs.MinQuotaPercent > 0 {
		if rem, known, _, detail := e.exec.QuotaRemainingPercent(ctx); known && rem < float64(prefs.MinQuotaPercent) {
			msg := fmt.Sprintf("跳过（额度不足）：当前剩余额度约 %.0f%%，低于阈值 %d%%", rem, prefs.MinQuotaPercent)
			if strings.TrimSpace(detail) != "" {
				msg += "（" + strings.TrimSpace(detail) + "）"
			}
			e.finishRun(task, run, RunSkipped, FailureQuotaInsufficient, msg, "")
			return
		}
	}

	timeoutMin := task.TimeoutMinutes
	if timeoutMin <= 0 {
		timeoutMin = DefaultTimeoutMinutes
	}
	execCtx, execCancel := context.WithTimeout(ctx, time.Duration(timeoutMin)*e.timeoutScale)
	defer execCancel()

	run.Message = "正在执行…"
	_ = e.store.UpdateRun(context.Background(), run)

	targetSessionID := task.SessionID
	if task.Mode == ModeNewSession {
		title := FormatScheduledSessionTitle(task.Name)
		sess, err := e.exec.CreateSession(execCtx, task.CWD, title, task.Model, task.Effort, task.PermissionMode)
		if err != nil {
			if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
				e.finishRun(task, run, RunFailed, FailureTimeout, fmt.Sprintf("运行超时（超过单次超时限制 %d 分钟）", timeoutMin), "")
				return
			}
			e.finishRun(task, run, RunFailed, FailureExecution, "创建会话失败："+err.Error(), "")
			return
		}
		targetSessionID = sess.ID
		run.SessionID = sess.ID
		run.SessionTitle = sess.Title
		e.exec.RecordPluginActivity(context.Background(), task.PluginID, fmt.Sprintf("定时任务「%s」新建会话 %s", task.Name, sess.ID))
		_ = e.store.UpdateRun(context.Background(), run)
	} else {
		ok, waitErr := e.waitForSessionIdle(execCtx, task, &run)
		if !ok {
			if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
				e.finishRun(task, run, RunFailed, FailureTimeout, fmt.Sprintf("运行超时（超过单次超时限制 %d 分钟）", timeoutMin), targetSessionID)
				return
			}
			if ctx.Err() != nil {
				e.finishRun(task, run, RunSkipped, FailureCancelled, "任务已取消或插件已停用", "")
				return
			}
			if waitErr != nil {
				e.finishRun(task, run, RunFailed, FailureExecution, waitErr.Error(), "")
				return
			}
			e.finishRun(task, run, RunSkipped, FailureBusyTimeout, fmt.Sprintf("绑定会话持续忙碌超过 %s，已跳过本次运行", formatDuration(e.busyWaitTimeout)), "")
			return
		}
	}

	beforeTurns, _ := e.exec.GetSessionTurns(execCtx, targetSessionID, 20)
	prevTurnCount := len(beforeTurns)
	prevLastTurnID := ""
	if prevTurnCount > 0 {
		prevLastTurnID = beforeTurns[prevTurnCount-1].TurnID
	}

	expandedPrompt := ExpandPrompt(task.Prompt, e.now(), e.loc, task.LastRunAt, task.LastSummary)
	if e.Paused() {
		e.finishRun(task, run, RunSkipped, FailureCancelled, "账号切换中，定时任务已暂停", targetSessionID)
		return
	}
	if err := e.exec.SendPrompt(execCtx, targetSessionID, expandedPrompt, task.Model, task.Effort, task.PermissionMode); err != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			_ = e.exec.AbortSession(context.Background(), targetSessionID)
			e.finishRun(task, run, RunFailed, FailureTimeout, fmt.Sprintf("运行超时（超过单次超时限制 %d 分钟），已尝试中止当前会话", timeoutMin), targetSessionID)
			return
		}
		e.finishRun(task, run, RunFailed, FailureExecution, "发送提示词失败："+err.Error(), targetSessionID)
		return
	}
	e.exec.RecordPluginActivity(context.Background(), task.PluginID, fmt.Sprintf("定时任务「%s」向会话 %s 发送提示词", task.Name, targetSessionID))

	status, failureKind, msg := e.observeTurnOutcome(execCtx, ctx, targetSessionID, prevTurnCount, prevLastTurnID, timeoutMin, task.MaxTurns)
	e.finishRun(task, run, status, failureKind, msg, targetSessionID)
}

func (e *Engine) waitForSessionIdle(ctx context.Context, task Task, run *RunRecord) (bool, error) {
	deadline := e.now().Add(e.busyWaitTimeout)
	waitedNotice := false
	for {
		sess, err := e.exec.GetSession(ctx, task.SessionID)
		if err != nil {
			return false, fmt.Errorf("无法读取绑定会话状态：%w", err)
		}
		if sess.Title != "" {
			run.SessionTitle = sess.Title
		}
		busy := sess.Busy || sess.MainTurnActive || (sess.PendingInteraction != "" && sess.PendingInteraction != "none")
		if !busy {
			return true, nil
		}
		if !waitedNotice {
			waitedNotice = true
			run.Message = "绑定会话正在忙碌或等待交互，正在等待其空闲…"
			_ = e.store.UpdateRun(context.Background(), *run)
			e.publishScheduleUpdate(task.PluginID, "run.waiting", run.ID)
		}
		if e.now().After(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(e.pollInterval):
		}
	}
}

func countNewTurns(turns []TurnInfo, prevLastTurnID string, prevTurnCount int) int {
	return len(newTurnsSince(turns, prevLastTurnID, prevTurnCount))
}

// newTurnsSince returns only the turns produced after prevLastTurnID. A bound
// session carries its whole history, and an approval left in that history must
// not immediately fail every new run.
func newTurnsSince(turns []TurnInfo, prevLastTurnID string, prevTurnCount int) []TurnInfo {
	if len(turns) == 0 {
		return nil
	}
	if prevLastTurnID != "" {
		for i := len(turns) - 1; i >= 0; i-- {
			if turns[i].TurnID == prevLastTurnID {
				return turns[i+1:]
			}
		}
	}
	if len(turns) > prevTurnCount && prevTurnCount >= 0 {
		return turns[prevTurnCount:]
	}
	return nil
}

func (e *Engine) observeTurnOutcome(execCtx, parentCtx context.Context, sessionID string, prevTurnCount int, prevLastTurnID string, timeoutMin, maxTurns int) (string, string, string) {
	sawBusy := false
	idlePolls := 0
	for {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			_ = e.exec.AbortSession(context.Background(), sessionID)
			return RunFailed, FailureTimeout, fmt.Sprintf("运行超时（超过单次超时限制 %d 分钟），已尝试中止当前会话", timeoutMin)
		}
		if parentCtx.Err() != nil {
			e.abortRemoteSession(sessionID)
			return RunSkipped, FailureCancelled, "任务已取消或插件已停用"
		}

		sess, err := e.exec.GetSession(execCtx, sessionID)
		if err != nil {
			if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
				_ = e.exec.AbortSession(context.Background(), sessionID)
				return RunFailed, FailureTimeout, fmt.Sprintf("运行超时（超过单次超时限制 %d 分钟），已尝试中止当前会话", timeoutMin)
			}
			if parentCtx.Err() != nil {
				e.abortRemoteSession(sessionID)
				return RunSkipped, FailureCancelled, "任务已取消或插件已停用"
			}
			return RunFailed, FailureExecution, "读取会话执行状态失败：" + err.Error()
		}
		pageSize := 20
		if maxTurns > 0 && prevTurnCount+maxTurns+5 > pageSize {
			pageSize = prevTurnCount + maxTurns + 5
		}
		turns, tErr := e.exec.GetSessionTurns(execCtx, sessionID, pageSize)
		if tErr == nil {
			if maxTurns > 0 {
				added := countNewTurns(turns, prevLastTurnID, prevTurnCount)
				if added > maxTurns {
					_ = e.exec.AbortSession(context.Background(), sessionID)
					return RunFailed, FailureMaxTurns, fmt.Sprintf("已超过单次最大回合数限制（%d 回合），已尝试中止本次运行", maxTurns)
				}
			}
			for _, turn := range newTurnsSince(turns, prevLastTurnID, prevTurnCount) {
				if turn.HasApproval {
					detail := "会话出现待批准操作，无人值守模式不会自动批准，请打开会话处理"
					if turn.ApprovalTitle != "" {
						detail = fmt.Sprintf("会话正在等待批准（%s），无人值守模式不会自动批准，请打开会话处理", turn.ApprovalTitle)
					}
					return RunAwaitingApproval, FailureNone, detail
				}
			}
		}
		if sess.PendingInteraction != "" && sess.PendingInteraction != "none" {
			return RunAwaitingApproval, FailureNone, fmt.Sprintf("会话处于等待交互状态（%s），无人值守模式不会自动批准，请打开会话处理", sess.PendingInteraction)
		}
		if sess.Busy || sess.MainTurnActive {
			sawBusy = true
			idlePolls = 0
		} else {
			newTurnSeen := false
			var latestErr string
			if tErr == nil && len(turns) > 0 {
				last := turns[len(turns)-1]
				if len(turns) > prevTurnCount || (last.TurnID != "" && last.TurnID != prevLastTurnID) || (prevTurnCount == 0 && last.HasAssistantMsg) {
					newTurnSeen = true
				}
				if last.ErrorMessage != "" && (newTurnSeen || sawBusy) {
					latestErr = last.ErrorMessage
				}
			}
			if latestErr != "" {
				return RunFailed, FailureExecution, "执行失败：" + latestErr
			}
			idlePolls++
			if sawBusy || newTurnSeen || idlePolls >= 2 {
				return RunCompleted, FailureNone, "已完成"
			}
		}
		select {
		case <-execCtx.Done():
			if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
				_ = e.exec.AbortSession(context.Background(), sessionID)
				return RunFailed, FailureTimeout, fmt.Sprintf("运行超时（超过单次超时限制 %d 分钟），已尝试中止当前会话", timeoutMin)
			}
			// StopPlugin, DeleteTask and safe mode cancel the run context. The
			// prompt is already sent, so the remote session must be told to stop
			// with an independent context: this one is already cancelled.
			e.abortRemoteSession(sessionID)
			return RunSkipped, FailureCancelled, "任务已取消或插件已停用"
		case <-time.After(e.pollInterval):
		}
	}
}

// abortRemoteSession stops a session after the run was cancelled. It uses a
// short independent context because the run context is already done.
func (e *Engine) abortRemoteSession(sessionID string) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = e.exec.AbortSession(ctx, sessionID)
}

func (e *Engine) finishRun(task Task, run RunRecord, status, failureKind, message, sessionID string) {
	now := e.now().UTC()
	finished := storage.SortableTime(now)
	if sessionID != "" {
		run.SessionID = sessionID
	}
	run.Status = status
	run.FailureKind = failureKind
	run.Message = message
	run.FinishedAt = finished
	prefs := e.store.GetPrefs(context.Background(), task.PluginID)
	if status == RunCompleted && prefs.AutoMarkRead {
		run.Unread = false
	} else {
		run.Unread = true
	}
	_ = e.store.UpdateRun(context.Background(), run)
	_ = e.store.UpdateTaskLastResult(context.Background(), task.ID, finished, status, message, run.SessionID, now)
	e.publishScheduleUpdate(task.PluginID, "run.finished", run.ID)
	e.maybePublishNotification(run)
}

func (e *Engine) maybePublishNotification(run RunRecord) {
	notif, ok := BuildNotification(run)
	if !ok {
		return
	}
	e.exec.PublishEvent("schedule", map[string]any{
		"pluginId":     run.PluginID,
		"kind":         "run.notify",
		"id":           run.ID,
		"at":           storage.SortableTime(e.now().UTC()),
		"notification": notif,
	})
}

func (e *Engine) publishScheduleUpdate(pluginID, kind, targetID string) {
	e.exec.PublishEvent("schedule", map[string]any{
		"pluginId": pluginID,
		"kind":     kind,
		"id":       targetID,
		"at":       storage.SortableTime(e.now().UTC()),
	})
}

func (e *Engine) shouldArchiveRun(ctx context.Context, pluginID string, run RunRecord) bool {
	if run.Mode != ModeNewSession || strings.TrimSpace(run.SessionID) == "" || run.Archived {
		return false
	}
	task, err := e.store.GetTask(ctx, pluginID, run.TaskID)
	if err != nil {
		return false
	}
	return task.ArchiveOnRead
}

func (e *Engine) archiveRunSession(ctx context.Context, pluginID string, run RunRecord) (bool, string) {
	if strings.TrimSpace(run.SessionID) == "" || run.Mode != ModeNewSession {
		return false, ""
	}
	if run.Archived {
		return true, ""
	}
	if err := e.exec.ArchiveSession(ctx, run.SessionID); err != nil {
		msg := "已标为已读，但归档官方会话失败：" + err.Error()
		_ = e.store.MarkRunArchived(ctx, pluginID, run.ID, false, msg)
		return false, msg
	}
	_ = e.store.MarkRunArchived(ctx, pluginID, run.ID, true, "")
	e.exec.RecordPluginActivity(context.Background(), pluginID, fmt.Sprintf("定时任务「%s」会话 %s 已归档", run.TaskName, run.SessionID))
	return true, ""
}

func (e *Engine) SchedulerAction(ctx context.Context, pluginID, action string, payload json.RawMessage) (any, error) {
	return e.Action(ctx, pluginID, action, payload)
}

func (e *Engine) Action(ctx context.Context, pluginID, action string, payload json.RawMessage) (any, error) {
	switch action {
	case "schedule.status":
		connected, reason := e.exec.KAPConnected(ctx)
		unread, err := e.store.UnreadCount(ctx, pluginID)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"kapConnected":   connected,
			"kapReason":      reason,
			"runningTaskIds": e.runningTaskIDs(pluginID),
			"unreadCount":    unread,
			"prefs":          e.store.GetPrefs(ctx, pluginID),
			"autoAttach":     e.exec.AutoAttachStatus(ctx),
		}, nil

	case "schedule.list":
		items, err := e.ListTaskViews(ctx, pluginID)
		if err != nil {
			return nil, err
		}
		connected, reason := e.exec.KAPConnected(ctx)
		unread, _ := e.store.UnreadCount(ctx, pluginID)
		return map[string]any{
			"items":          items,
			"total":          len(items),
			"kapConnected":   connected,
			"kapReason":      reason,
			"runningTaskIds": e.runningTaskIDs(pluginID),
			"unreadCount":    unread,
			"prefs":          e.store.GetPrefs(ctx, pluginID),
			"autoAttach":     e.exec.AutoAttachStatus(ctx),
		}, nil

	case "schedule.save":
		var input SaveTaskInput
		if err := json.Unmarshal(payload, &input); err != nil {
			return nil, errors.New("任务参数格式无效")
		}
		return e.SaveTask(ctx, pluginID, input)

	case "schedule.delete":
		var input struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil || strings.TrimSpace(input.ID) == "" {
			return nil, errors.New("缺少任务 ID")
		}
		if err := e.DeleteTask(ctx, pluginID, strings.TrimSpace(input.ID)); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "id": strings.TrimSpace(input.ID)}, nil

	case "schedule.toggle":
		var input struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		}
		if err := json.Unmarshal(payload, &input); err != nil || strings.TrimSpace(input.ID) == "" {
			return nil, errors.New("缺少任务 ID")
		}
		return e.ToggleTask(ctx, pluginID, strings.TrimSpace(input.ID), input.Enabled)

	case "schedule.runNow":
		var input struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil || strings.TrimSpace(input.ID) == "" {
			return nil, errors.New("缺少任务 ID")
		}
		return e.RunNow(ctx, pluginID, strings.TrimSpace(input.ID))

	case "schedule.runs":
		var input struct {
			TaskID string `json:"taskId"`
			Limit  int    `json:"limit"`
		}
		if len(payload) > 0 && string(payload) != "null" {
			if err := json.Unmarshal(payload, &input); err != nil {
				return nil, errors.New("运行记录查询参数格式无效")
			}
		}
		items, err := e.store.ListRuns(ctx, pluginID, strings.TrimSpace(input.TaskID), input.Limit)
		if err != nil {
			return nil, err
		}
		unread, _ := e.store.UnreadCount(ctx, pluginID)
		return map[string]any{
			"items":       items,
			"total":       len(items),
			"unreadCount": unread,
			"prefs":       e.store.GetPrefs(ctx, pluginID),
		}, nil

	case "schedule.markRead":
		var input struct {
			ID      string `json:"id"`
			Read    *bool  `json:"read"`
			Archive *bool  `json:"archive"`
		}
		if err := json.Unmarshal(payload, &input); err != nil || strings.TrimSpace(input.ID) == "" {
			return nil, errors.New("缺少运行记录 ID")
		}
		runID := strings.TrimSpace(input.ID)
		read := true
		if input.Read != nil {
			read = *input.Read
		}
		allowArchive := true
		if input.Archive != nil {
			allowArchive = *input.Archive
		}
		run, err := e.store.GetRun(ctx, pluginID, runID)
		if err != nil {
			return nil, err
		}
		if err := e.store.MarkRunRead(ctx, pluginID, runID, read); err != nil {
			return nil, err
		}
		archived := run.Archived
		archiveErr := ""
		if read && allowArchive && e.shouldArchiveRun(ctx, pluginID, run) {
			archived, archiveErr = e.archiveRunSession(ctx, pluginID, run)
		}
		unread, _ := e.store.UnreadCount(ctx, pluginID)
		e.publishScheduleUpdate(pluginID, "runs.read", runID)
		return map[string]any{
			"unreadCount":  unread,
			"archived":     archived,
			"archiveError": archiveErr,
		}, nil

	case "schedule.archiveRun":
		var input struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(payload, &input); err != nil || strings.TrimSpace(input.ID) == "" {
			return nil, errors.New("缺少运行记录 ID")
		}
		runID := strings.TrimSpace(input.ID)
		run, err := e.store.GetRun(ctx, pluginID, runID)
		if err != nil {
			return nil, err
		}
		if run.Mode != ModeNewSession || strings.TrimSpace(run.SessionID) == "" {
			return nil, errors.New("只有新开会话的运行记录支持归档对应会话")
		}
		archived, archiveErr := e.archiveRunSession(ctx, pluginID, run)
		e.publishScheduleUpdate(pluginID, "runs.archived", runID)
		if !archived && archiveErr != "" {
			return nil, errors.New(archiveErr)
		}
		return map[string]any{"archived": archived, "id": runID, "sessionId": run.SessionID}, nil

	case "schedule.markAllRead":
		unreadRuns, err := e.store.ListUnreadRuns(ctx, pluginID)
		if err != nil {
			return nil, err
		}
		if err := e.store.MarkAllRead(ctx, pluginID); err != nil {
			return nil, err
		}
		tasks, _ := e.store.ListTasks(ctx, pluginID)
		archiveEnabledByTask := map[string]bool{}
		for _, t := range tasks {
			archiveEnabledByTask[t.ID] = t.ArchiveOnRead
		}
		eligibleRuns := []RunRecord{}
		sessionIDs := []string{}
		seenSession := map[string]bool{}
		for _, run := range unreadRuns {
			if run.Mode != ModeNewSession || strings.TrimSpace(run.SessionID) == "" || run.Archived {
				continue
			}
			if !archiveEnabledByTask[run.TaskID] {
				continue
			}
			eligibleRuns = append(eligibleRuns, run)
			if !seenSession[run.SessionID] {
				seenSession[run.SessionID] = true
				sessionIDs = append(sessionIDs, run.SessionID)
			}
		}
		archivedCount := 0
		archiveErr := ""
		if len(sessionIDs) > 0 {
			var err error
			if len(sessionIDs) == 1 {
				err = e.exec.ArchiveSession(ctx, sessionIDs[0])
			} else {
				err = e.exec.ArchiveSessions(ctx, sessionIDs)
			}
			if err != nil {
				archiveErr = "已全部标为已读，但批量归档官方会话失败：" + err.Error()
				for _, run := range eligibleRuns {
					_ = e.store.MarkRunArchived(ctx, pluginID, run.ID, false, archiveErr)
				}
			} else {
				archivedCount = len(sessionIDs)
				for _, run := range eligibleRuns {
					_ = e.store.MarkRunArchived(ctx, pluginID, run.ID, true, "")
				}
				e.exec.RecordPluginActivity(context.Background(), pluginID, fmt.Sprintf("批量归档 %d 个定时任务会话", archivedCount))
			}
		}
		e.publishScheduleUpdate(pluginID, "runs.readAll", "")
		return map[string]any{
			"unreadCount":   0,
			"archivedCount": archivedCount,
			"archiveError":  archiveErr,
		}, nil

	case "schedule.prefs":
		if len(payload) > 0 && string(payload) != "null" {
			var raw map[string]any
			if err := json.Unmarshal(payload, &raw); err != nil {
				return nil, errors.New("偏好设置格式无效")
			}
			cur := e.store.GetPrefs(ctx, pluginID)
			changed := false
			if val, has := raw["autoMarkRead"]; has {
				autoRead, ok := val.(bool)
				if !ok {
					return nil, errors.New("autoMarkRead 必须是布尔值")
				}
				cur.AutoMarkRead = autoRead
				changed = true
			}
			if val, has := raw["maxConcurrent"]; has {
				num, ok := val.(float64)
				if !ok || int(num) < 1 || int(num) > MaxAllowedConcurrent {
					return nil, fmt.Errorf("全局并发上限必须是 1–%d 的整数", MaxAllowedConcurrent)
				}
				cur.MaxConcurrent = int(num)
				changed = true
			}
			if val, has := raw["minQuotaPercent"]; has {
				num, ok := val.(float64)
				if !ok || int(num) < 0 || int(num) > 95 {
					return nil, errors.New("额度跳过阈值必须是 0–95 之间的百分比（0 表示关闭）")
				}
				cur.MinQuotaPercent = int(num)
				cur.MinQuotaPercentSet = true
				changed = true
			}
			if changed {
				if err := e.store.SetPrefs(ctx, pluginID, cur); err != nil {
					return nil, err
				}
				e.wakeSlotWaiters()
				e.publishScheduleUpdate(pluginID, "prefs.updated", "")
			}
		}
		return e.store.GetPrefs(ctx, pluginID), nil

	case "schedule.checkGit":
		var input struct {
			CWD string `json:"cwd"`
		}
		if len(payload) > 0 && string(payload) != "null" {
			if err := json.Unmarshal(payload, &input); err != nil {
				return nil, errors.New("目录参数格式无效")
			}
		}
		return e.gitChecker(ctx, strings.TrimSpace(input.CWD)), nil

	default:
		return nil, fmt.Errorf("未知的定时任务动作：%s", action)
	}
}

func newID(prefix string) string {
	token := security.Random()
	if len(token) > 16 {
		token = token[:16]
	}
	return prefix + "_" + token
}

func formatDuration(d time.Duration) string {
	if d%time.Minute == 0 && d >= time.Minute {
		return fmt.Sprintf("%d 分钟", int(d/time.Minute))
	}
	return d.Round(time.Second).String()
}
