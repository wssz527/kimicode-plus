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
			e.maxConcurrent = n
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

type Engine struct {
	store *Store
	exec  Executor

	now             func() time.Time
	loc             *time.Location
	maxConcurrent   int
	tickInterval    time.Duration
	busyWaitTimeout time.Duration
	pollInterval    time.Duration
	catchupGrace    time.Duration
	pluginEnabled   func(string) bool
	defaultCWD      string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	sem chan struct{}

	mu          sync.Mutex
	activeTasks map[string]activeRun
	closed      bool
}

type activeRun struct {
	pluginID string
	runID    string
	cancel   context.CancelFunc
}

type SaveTaskInput struct {
	ID             string       `json:"id,omitempty"`
	Name           string       `json:"name"`
	Prompt         string       `json:"prompt"`
	CWD            string       `json:"cwd"`
	Mode           string       `json:"mode"`
	SessionID      string       `json:"sessionId,omitempty"`
	Model          string       `json:"model,omitempty"`
	Effort         string       `json:"effort,omitempty"`
	PermissionMode string       `json:"permissionMode"`
	Schedule       ScheduleRule `json:"schedule"`
	MissedPolicy   string       `json:"missedPolicy"`
	Enabled        *bool        `json:"enabled,omitempty"`
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
		maxConcurrent:   2,
		tickInterval:    5 * time.Second,
		busyWaitTimeout: 10 * time.Minute,
		pollInterval:    1 * time.Second,
		catchupGrace:    90 * time.Second,
		pluginEnabled:   func(string) bool { return true },
		ctx:             ctx,
		cancel:          cancel,
		activeTasks:     map[string]activeRun{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	e.sem = make(chan struct{}, e.maxConcurrent)
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

func (e *Engine) Tick(ctx context.Context) error {
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed || e.ctx.Err() != nil {
		return nil
	}
	connected, _ := e.exec.KAPConnected(ctx)
	if !connected {
		return nil
	}
	now := e.now().UTC()
	due, err := e.store.DueTasks(ctx, now)
	if err != nil {
		return err
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
				e.recordSkipped(ctx, task, TriggerScheduled, task.NextRunAt, "上一次运行尚未结束，已跳过本次重叠调度", now)
			}
			continue
		}

		missed := !scheduledAt.IsZero() && now.Sub(scheduledAt) > e.catchupGrace
		if missed && task.MissedPolicy == MissedSkip {
			if err := e.store.UpdateTaskSchedule(ctx, task.ID, nextEnabled, nextStr, now); err == nil {
				label := scheduledAt.In(e.loc).Format("2006-01-02 15:04")
				e.recordSkipped(ctx, task, TriggerScheduled, task.NextRunAt, fmt.Sprintf("错过预定时间（%s），按任务设置已跳过", label), now)
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

func (e *Engine) recordSkipped(ctx context.Context, task Task, triggerKind, scheduledFor, message string, now time.Time) {
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
		Message:        message,
		Unread:         true,
		ScheduledFor:   scheduledFor,
		StartedAt:      stamp,
		FinishedAt:     stamp,
	}
	_ = e.store.InsertRun(ctx, run)
	_ = e.store.UpdateTaskLastResult(ctx, task.ID, stamp, RunSkipped, message, "", now)
	e.publishScheduleUpdate(task.PluginID, "run.finished", run.ID)
}

func (e *Engine) isTaskActive(taskID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.activeTasks[taskID]
	return ok
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
	task, err := e.store.GetTask(ctx, pluginID, id)
	if err != nil {
		return RunRecord{}, err
	}
	if !e.isPluginEnabled(pluginID) {
		return RunRecord{}, errors.New("插件未启用，无法运行任务")
	}
	connected, reason := e.exec.KAPConnected(ctx)
	if !connected {
		if reason == "" {
			reason = "官方客户端未连接（KAP 不可用）"
		}
		return RunRecord{}, errors.New(reason)
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

func (e *Engine) executeRun(ctx context.Context, task Task, run RunRecord) {
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		e.finishRun(task, run, RunSkipped, "任务已取消或插件已停用", "")
		return
	}

	if !e.isPluginEnabled(task.PluginID) {
		e.finishRun(task, run, RunSkipped, "插件已停用，已取消本次运行", "")
		return
	}

	run.Status = RunRunning
	run.Message = "正在执行…"
	_ = e.store.UpdateRun(context.Background(), run)
	_ = e.store.UpdateTaskLastResult(context.Background(), task.ID, run.StartedAt, RunRunning, run.Message, "", e.now().UTC())
	e.publishScheduleUpdate(task.PluginID, "run.started", run.ID)

	targetSessionID := task.SessionID
	if task.Mode == ModeNewSession {
		title := fmt.Sprintf("[定时] %s", task.Name)
		sess, err := e.exec.CreateSession(ctx, task.CWD, title, task.Model, task.Effort, task.PermissionMode)
		if err != nil {
			e.finishRun(task, run, RunFailed, "创建会话失败："+err.Error(), "")
			return
		}
		targetSessionID = sess.ID
		run.SessionID = sess.ID
		run.SessionTitle = sess.Title
		e.exec.RecordPluginActivity(context.Background(), task.PluginID, fmt.Sprintf("定时任务「%s」新建会话 %s", task.Name, sess.ID))
		_ = e.store.UpdateRun(context.Background(), run)
	} else {
		ok, waitErr := e.waitForSessionIdle(ctx, task, &run)
		if !ok {
			if ctx.Err() != nil {
				e.finishRun(task, run, RunSkipped, "任务已取消或插件已停用", "")
				return
			}
			if waitErr != nil {
				e.finishRun(task, run, RunFailed, waitErr.Error(), "")
				return
			}
			e.finishRun(task, run, RunSkipped, fmt.Sprintf("绑定会话持续忙碌超过 %s，已跳过本次运行", formatDuration(e.busyWaitTimeout)), "")
			return
		}
	}

	beforeTurns, _ := e.exec.GetSessionTurns(ctx, targetSessionID, 20)
	prevTurnCount := len(beforeTurns)
	prevLastTurnID := ""
	if prevTurnCount > 0 {
		prevLastTurnID = beforeTurns[prevTurnCount-1].TurnID
	}

	if err := e.exec.SendPrompt(ctx, targetSessionID, task.Prompt, task.Model, task.Effort, task.PermissionMode); err != nil {
		e.finishRun(task, run, RunFailed, "发送提示词失败："+err.Error(), targetSessionID)
		return
	}
	e.exec.RecordPluginActivity(context.Background(), task.PluginID, fmt.Sprintf("定时任务「%s」向会话 %s 发送提示词", task.Name, targetSessionID))

	status, msg := e.observeTurnOutcome(ctx, targetSessionID, prevTurnCount, prevLastTurnID)
	e.finishRun(task, run, status, msg, targetSessionID)
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

func (e *Engine) observeTurnOutcome(ctx context.Context, sessionID string, prevTurnCount int, prevLastTurnID string) (string, string) {
	sawBusy := false
	idlePolls := 0
	for {
		sess, err := e.exec.GetSession(ctx, sessionID)
		if err != nil {
			if ctx.Err() != nil {
				return RunSkipped, "任务已取消或插件已停用"
			}
			return RunFailed, "读取会话执行状态失败：" + err.Error()
		}
		turns, tErr := e.exec.GetSessionTurns(ctx, sessionID, 20)
		if tErr == nil {
			for _, turn := range turns {
				if turn.HasApproval {
					detail := "会话出现待批准操作，无人值守模式不会自动批准，请打开会话处理"
					if turn.ApprovalTitle != "" {
						detail = fmt.Sprintf("会话正在等待批准（%s），无人值守模式不会自动批准，请打开会话处理", turn.ApprovalTitle)
					}
					return RunAwaitingApproval, detail
				}
			}
		}
		if sess.PendingInteraction != "" && sess.PendingInteraction != "none" {
			return RunAwaitingApproval, fmt.Sprintf("会话处于等待交互状态（%s），无人值守模式不会自动批准，请打开会话处理", sess.PendingInteraction)
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
				return RunFailed, "执行失败：" + latestErr
			}
			idlePolls++
			if sawBusy || newTurnSeen || idlePolls >= 2 {
				return RunCompleted, "已完成"
			}
		}
		select {
		case <-ctx.Done():
			return RunSkipped, "任务已取消或插件已停用"
		case <-time.After(e.pollInterval):
		}
	}
}

func (e *Engine) finishRun(task Task, run RunRecord, status, message, sessionID string) {
	now := e.now().UTC()
	finished := storage.SortableTime(now)
	if sessionID != "" {
		run.SessionID = sessionID
	}
	run.Status = status
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
}

func (e *Engine) publishScheduleUpdate(pluginID, kind, targetID string) {
	e.exec.PublishEvent("schedule", map[string]any{
		"pluginId": pluginID,
		"kind":     kind,
		"id":       targetID,
		"at":       storage.SortableTime(e.now().UTC()),
	})
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
			ID   string `json:"id"`
			Read *bool  `json:"read"`
		}
		if err := json.Unmarshal(payload, &input); err != nil || strings.TrimSpace(input.ID) == "" {
			return nil, errors.New("缺少运行记录 ID")
		}
		read := true
		if input.Read != nil {
			read = *input.Read
		}
		if err := e.store.MarkRunRead(ctx, pluginID, strings.TrimSpace(input.ID), read); err != nil {
			return nil, err
		}
		unread, _ := e.store.UnreadCount(ctx, pluginID)
		e.publishScheduleUpdate(pluginID, "runs.read", strings.TrimSpace(input.ID))
		return map[string]any{"unreadCount": unread}, nil

	case "schedule.markAllRead":
		if err := e.store.MarkAllRead(ctx, pluginID); err != nil {
			return nil, err
		}
		e.publishScheduleUpdate(pluginID, "runs.readAll", "")
		return map[string]any{"unreadCount": 0}, nil

	case "schedule.prefs":
		if len(payload) > 0 && string(payload) != "null" {
			var raw map[string]any
			if err := json.Unmarshal(payload, &raw); err != nil {
				return nil, errors.New("偏好设置格式无效")
			}
			if val, has := raw["autoMarkRead"]; has {
				autoRead, ok := val.(bool)
				if !ok {
					return nil, errors.New("autoMarkRead 必须是布尔值")
				}
				if err := e.store.SetPrefs(ctx, pluginID, Prefs{AutoMarkRead: autoRead}); err != nil {
					return nil, err
				}
				e.publishScheduleUpdate(pluginID, "prefs.updated", "")
			}
		}
		return e.store.GetPrefs(ctx, pluginID), nil

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
