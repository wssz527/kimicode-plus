package desktop

import (
	"context"
	"strings"
	"time"

	"enhancedkimi/internal/cache"
)

// keepAlivePrompt is the short steer message sent while the main agent waits for
// a background subagent. Steered messages cannot be individually undone, so it is
// kept as brief as possible while explicitly asking for no action.
const keepAlivePrompt = "[保活]无需操作，继续等待"

// keepAliveVerdict describes what the closed loop observed after a keepalive.
type keepAliveVerdict struct {
	// State is one of verified / invalid / unknown / skipped.
	State string
	Note  string
	// CacheRead is the input_cache_read of the step that followed the keepalive.
	CacheRead int64
}

const (
	keepAliveVerified = "verified"
	keepAliveInvalid  = "invalid"
	keepAliveUnknown  = "unknown"
	keepAliveSkipped  = "skipped"
)

// keepAliveGate decides whether a keepalive may be sent right now, and records why
// not when it may not.
//
// Plan D8: keepalive exists for exactly one situation - the main agent is waiting
// for a background subagent and has not issued a model request for long enough that
// the cache would be dropped. Every other situation is excluded, and idle sessions
// are compaction's job, never keepalive's.
func (s *NativeService) keepAliveGate(ctx context.Context, id string, settings AutomationSettings, now time.Time) (keepAliveCandidate, bool) {
	snapshot, ok := s.automationSnapshot(id)
	if !ok {
		return keepAliveCandidate{}, false
	}
	candidate := keepAliveCandidate{
		SessionID:   id,
		TurnActive:  snapshot.session.Busy || snapshot.session.MainTurnActive,
		Pending:     pendingNativeInteraction(snapshot),
		GoalBlocked: goalBlocksIdle(snapshot),
	}
	// An approval, question, or archived/completed session is never kept alive.
	if candidate.Pending || candidate.GoalBlocked || sessionIsArchivedOrDone(snapshot.session) {
		return candidate, false
	}
	store, err := s.cacheStore()
	if err != nil {
		return candidate, false
	}
	if _, err = store.Scan(ctx, now, false); err != nil {
		return candidate, false
	}
	live, err := store.Live(ctx, id)
	if err != nil {
		return candidate, false
	}
	candidate.OpenBackground = live.OpenBackground
	candidate.Blocked = live.Blocked
	// An explicit official "idle" outranks the offline counter: after a client
	// exit or restart a lost background task may never write task.terminated to
	// disk, and that stale count would otherwise keep a finished session looking
	// busy forever. The same rule is applied by the account switch busy check.
	if strings.EqualFold(strings.TrimSpace(snapshot.session.Status), "idle") {
		candidate.OpenBackground = 0
		candidate.Blocked = false
	}
	// D8 defines "running" by the lifecycle of an unreturned background subagent, not
	// by the official busy/main_turn_active flags. Once the main agent has handed work
	// to a background subagent it commonly reports busy=false and main_turn_active=false
	// while that subagent still runs; gating on those flags would skip keepalive in
	// exactly the situation it exists for, and would let idle compaction take over a
	// session that is still working.
	candidate.Running = candidate.OpenBackground > 0 || candidate.TurnActive
	if !candidate.Running {
		return candidate, false
	}
	// No background subagent means nothing is being waited on. A live turn alone is not
	// enough: the main agent is producing requests then, so there is no cache to keep.
	if candidate.OpenBackground <= 0 {
		return candidate, false
	}
	// A blocking wait cannot be kept alive: during task.waitDelivered the main agent
	// produces no model request and steered messages are queued, so a keepalive
	// message would only be delivered after the wait ends. The observed evidence is
	// in work/replan-20261116/keepalive-findings.md section 4.2.
	candidate.BlockedWait = candidate.Blocked

	// The clock is the main agent's own last model request, never session activity.
	// Subagent progress keeps the session busy but must not reset this clock, or the
	// keepalive could never fire.
	last, seen, err := store.LastMainStep(ctx, id)
	if err != nil {
		return candidate, false
	}
	if seen {
		candidate.LastRequest = last.At
		candidate.StepUUID = last.StepUUID
		candidate.Model = last.Model
	} else {
		// Without a recorded main step the session activity time is the only anchor.
		// It is used as a fallback but marked as such in the receipt.
		candidate.LastRequest = snapshot.lastActivity
		candidate.FromActivity = true
	}
	candidate.IdleMinutes = now.Sub(candidate.LastRequest).Minutes()
	threshold := time.Duration(settings.KeepAlive.Minutes) * time.Minute
	if now.Sub(candidate.LastRequest) < threshold {
		return candidate, false
	}
	return candidate, true
}

// keepAliveCandidate is the pre-send snapshot. Sending re-checks it, because the
// official session can change between the decision and the request.
type keepAliveCandidate struct {
	SessionID string
	// Running follows D8: an unreturned background subagent, or a live main turn.
	Running bool
	// TurnActive is the official busy/main_turn_active pair, kept separately so a
	// receipt can explain which signal justified the decision.
	TurnActive     bool
	Pending        bool
	GoalBlocked    bool
	OpenBackground int
	Blocked        bool
	BlockedWait    bool
	LastRequest    time.Time
	IdleMinutes    float64
	StepUUID       string
	Model          string
	FromActivity   bool
}

// runKeepAliveOnce applies the D8 policy for one session.
func (s *NativeService) runKeepAliveOnce(ctx context.Context, settings AutomationSettings, id string, now time.Time) {
	candidate, ok := s.keepAliveGate(ctx, id, settings, now)
	if !ok {
		return
	}
	revision := ""
	if snapshot, found := s.automationSnapshot(id); found {
		revision = contextRevision(snapshot)
	}
	allowed, err := s.automationAllowed(ctx, "keepalive", id, revision, now)
	if err != nil || !allowed {
		return
	}
	intent, _, err := s.beginAutomation(ctx, "keepalive", id, revision, now)
	if err != nil {
		return
	}
	if candidate.BlockedWait {
		note := "主代理正在阻塞等待中：保活消息只会排队到等待结束，此时缓存已过期，因此不发送。"
		_ = s.updateAutomation(ctx, intent.ID, keepAliveSkipped, note, now)
		s.publishAutomationReceipt("keepalive", id, keepAliveSkipped, note)
		return
	}

	// Re-check right before sending: a new activity or approval invalidates the
	// candidate, and a stale decision must not produce a message.
	latest, found := s.automationSnapshot(id)
	if !found || pendingNativeInteraction(latest) || goalBlocksIdle(latest) {
		_ = s.updateAutomation(ctx, intent.ID, "cancelled", "发送前出现新活动或审批，候选已失效", now)
		return
	}
	// The background subagent may have returned between the decision and the send.
	// Re-read the live signals instead of trusting the official busy flags.
	store, storeErr := s.cacheStore()
	if storeErr != nil {
		_ = s.updateAutomation(ctx, intent.ID, "cancelled", "发送前无法复核后台子代理状态", now)
		return
	}
	if _, storeErr = store.Scan(ctx, now, false); storeErr != nil {
		_ = s.updateAutomation(ctx, intent.ID, "cancelled", "发送前无法复核后台子代理状态", now)
		return
	}
	live, liveErr := store.Live(ctx, id)
	if liveErr != nil || live.OpenBackground <= 0 {
		_ = s.updateAutomation(ctx, intent.ID, "cancelled", "发送前后台子代理已返回，候选已失效", now)
		return
	}
	if strings.EqualFold(strings.TrimSpace(latest.session.Status), "idle") {
		_ = s.updateAutomation(ctx, intent.ID, "cancelled", "发送前官方已报告会话空闲，候选已失效", now)
		return
	}
	if err = s.WatchSessionInternal(id); err != nil {
		_ = s.updateAutomation(ctx, intent.ID, "failed", "无法订阅保活回执："+err.Error(), now)
		return
	}
	sentAt := time.Now().UTC()
	if _, err = s.sendPrompt(ctx, map[string]any{"id": id, "text": keepAlivePrompt, "mode": "inherit"}); err != nil {
		note := "保活消息未被官方接收：" + err.Error()
		_ = s.updateAutomation(ctx, intent.ID, "failed", note, now)
		s.publishAutomationReceipt("keepalive", id, "failed", note)
		return
	}
	// The send itself is only an acknowledgement. The result is decided by the next
	// main-agent step, which is verified asynchronously so the tick is not blocked.
	note := "保活消息已被官方接收，正在核对主代理下一步的 input_cache_read"
	_ = s.updateAutomation(ctx, intent.ID, "acknowledged", note, now)
	s.publishAutomationReceipt("keepalive", id, "acknowledged", note)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.verifyKeepAlive(intent.ID, id, candidate.StepUUID, candidate.Model, sentAt)
	}()
}

// Default closed-loop timings. They are fields on NativeService so tests can run
// the loop without waiting minutes; production uses these values.
const (
	defaultKeepAliveVerifyWindow = 5 * time.Minute
	defaultKeepAlivePollInterval = 15 * time.Second
)

func (s *NativeService) keepAliveWindow() time.Duration {
	if s.verifyWindow > 0 {
		return s.verifyWindow
	}
	return defaultKeepAliveVerifyWindow
}

func (s *NativeService) keepAlivePoll() time.Duration {
	if s.verifyPoll > 0 {
		return s.verifyPoll
	}
	return defaultKeepAlivePollInterval
}

// keepAliveDecision is one poll's result: the verdict to record, if the loop is done.
type keepAliveDecision struct {
	Done    bool
	Verdict keepAliveVerdict
}

// decideKeepAlive decides one poll of the closed loop.
//
// Order matters. A superseded intent is settled first: once a newer keepalive has been
// sent, the main agent's next step is downstream of that send, so an older intent must
// not read it. Getting this wrong turned a keepalive that produced no request at all
// into "verified" on the isolated host, because the retry fired exactly one cooldown
// later and its step arrived 30ms after. A superseded intent is settled as invalid
// rather than left unjudged, so no intent sits in "acknowledged" forever.
func decideKeepAlive(found bool, step cache.Step, anchor, model string, superseded, expired bool) keepAliveDecision {
	if superseded {
		return keepAliveDecision{Done: true, Verdict: keepAliveVerdict{
			State: keepAliveInvalid,
			Note:  "本次保活窗口内没有产生自己的模型请求，后续保活已接手核对，本次保活无效",
		}}
	}
	if found && step.StepUUID != anchor {
		return keepAliveDecision{Done: true, Verdict: judgeKeepAlive(step, model)}
	}
	if expired {
		return keepAliveDecision{Done: true, Verdict: keepAliveVerdict{
			State: keepAliveInvalid,
			Note:  "保活后 5 分钟内主代理没有产生新的模型请求，保活无效",
		}}
	}
	return keepAliveDecision{}
}

// verifyKeepAlive closes the loop: it waits for the main agent's next step and reads
// its input_cache_read.
//
// A hit means the cache was still alive and the keepalive worked. A miss means the
// cache was already gone, so the keepalive was ineffective. No new step at all
// within the window also counts as ineffective. When the model never reports cache
// fields the outcome is genuinely unknown and is recorded as such rather than as a
// success.
func (s *NativeService) verifyKeepAlive(intentID, sessionID, anchor string, model string, sentAt time.Time) {
	deadline := time.Now().Add(s.keepAliveWindow())
	ctx := s.ctx
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.keepAlivePoll()):
		}
		tickCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		store, err := s.cacheStore()
		if err != nil {
			cancel()
			return
		}
		if _, err = store.Scan(tickCtx, time.Now().UTC(), false); err != nil {
			cancel()
			continue
		}
		step, found, err := store.StepAfter(tickCtx, sessionID, sentAt)
		cancel()
		if err != nil {
			return
		}
		superseded, err := s.keepAliveSuperseded(intentID, sessionID)
		if err != nil {
			return
		}
		decision := decideKeepAlive(found, step, anchor, model, superseded, time.Now().After(deadline))
		if !decision.Done {
			continue
		}
		finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.finishKeepAlive(finishCtx, intentID, sessionID, decision.Verdict)
		finishCancel()
		return
	}
}

// keepAliveSuperseded reports whether a newer keepalive intent exists for this session.
// Anything the main agent produces after that send belongs to the newer attempt.
func (s *NativeService) keepAliveSuperseded(intentID, sessionID string) (bool, error) {
	var newer int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM automation_intents WHERE kind='keepalive' AND session_id=? AND id<>? AND created_at>(SELECT created_at FROM automation_intents WHERE id=? AND session_id=?)`,
		sessionID, intentID, intentID, sessionID).Scan(&newer)
	return newer > 0, err
}

// judgeKeepAlive turns one following step into a verdict.
func judgeKeepAlive(step cache.Step, model string) keepAliveVerdict {
	if step.InputCacheRead > 0 {
		return keepAliveVerdict{
			State:     keepAliveVerified,
			CacheRead: step.InputCacheRead,
			Note:      "已确认保活后的主代理请求读取了缓存",
		}
	}
	// read == 0 and creation == 0 means this model does not report cache fields at
	// all, so nothing can be concluded about the cache.
	if step.InputCacheRead == 0 && step.InputCacheCreation == 0 {
		return keepAliveVerdict{
			State: keepAliveUnknown,
			Note:  "主代理下一步 input_cache_read 为 0，且该模型从未上报缓存字段，无法判断保活是否有效",
		}
	}
	return keepAliveVerdict{
		State: keepAliveInvalid,
		Note:  "主代理下一步 input_cache_read 为 0，缓存未续上，保活无效；不会自动重试",
	}
}

func (s *NativeService) finishKeepAlive(ctx context.Context, intentID, sessionID string, verdict keepAliveVerdict) error {
	if err := s.updateAutomation(ctx, intentID, verdict.State, verdict.Note, time.Now().UTC()); err != nil {
		return err
	}
	s.publishAutomationReceipt("keepalive", sessionID, verdict.State, verdict.Note)
	return nil
}
