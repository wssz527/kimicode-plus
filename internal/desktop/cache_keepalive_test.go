package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"enhancedkimi/internal/cache"
)

type cacheStepAlias = cache.Step

// keepAliveFixture builds a synthesised session whose main agent last called a model
// at mainAge and whose background subagent is either still running or finished.
type keepAliveFixture struct {
	home       string
	mainAge    time.Duration
	background bool
	blocked    bool
	// lastCacheRead is the input_cache_read of the main agent's recorded step.
	lastCacheRead int
}

func (f keepAliveFixture) write(t *testing.T, model string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	mainAt := now.Add(-f.mainAge)
	dir := filepath.Join(f.home, "sessions", "wd_test", "session_s1", "agents", "main")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	usage := fmt.Sprintf(`{"inputOther":1000,"output":200,"inputCacheRead":%d,"inputCacheCreation":0}`, f.lastCacheRead)
	lines := []string{
		fmt.Sprintf(`{"type":"usage.record","agentId":"main","model":%q,"usage":%s,"usageScope":"turn","time":%d}`, model, usage, mainAt.UnixMilli()),
		fmt.Sprintf(`{"type":"context.append_loop_event","agentId":"main","event":{"type":"step.end","uuid":"step-main","turnId":"0","step":1,"finishReason":"end_turn","usage":%s,"llmServerDecodeMs":2000},"time":%d}`, usage, mainAt.UnixMilli()),
	}
	if f.background {
		lines = append(lines, fmt.Sprintf(`{"type":"task.started","agentId":"main","info":{"taskId":"agent-bg","kind":"agent","detached":true,"status":"running","startedAt":%d},"time":%d}`, mainAt.UnixMilli(), mainAt.UnixMilli()))
	}
	if f.blocked {
		// A blocking window is an outstanding WaitFor tool call. task.waitDelivered is the
		// delivery of a wait that already finished, so it must not be used here.
		lines = append(lines, fmt.Sprintf(`{"type":"context.append_loop_event","agentId":"main","event":{"type":"tool.call","name":"WaitFor","toolCallId":"call-wait"},"time":%d}`, mainAt.Add(time.Second).UnixMilli()))
	}
	if err := os.WriteFile(filepath.Join(dir, "wire.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := fmt.Sprintf(`{"id":"s1","cwd":"/tmp/synthetic","createdAt":%d,"updatedAt":%d,"agents":{"main":{"type":"main"}}}`, now.Add(-time.Hour).UnixMilli(), now.UnixMilli())
	if err := os.WriteFile(filepath.Join(f.home, "sessions", "wd_test", "session_s1", "state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
}

// keepAliveService builds a service pointed at a synthesised KIMI_CODE_HOME with a
// synthetic provider that records every prompt it receives.
func keepAliveService(t *testing.T, home string, session map[string]any, prompts *[]string, promptMu *sync.Mutex) *NativeService {
	t.Helper()
	handler := automationTestSessionHandler(t, session, []NativeTurn{}, nil, nil, func(path string, body map[string]any) {
		if path == "/api/v1/sessions/s1/prompts" {
			if promptMu != nil {
				promptMu.Lock()
			}
			if prompts != nil {
				content, _ := body["content"].([]any)
				text := ""
				for _, part := range content {
					if item, ok := part.(map[string]any); ok {
						if value, ok := item["text"].(string); ok {
							text += value
						}
					}
				}
				*prompts = append(*prompts, text)
			}
			if promptMu != nil {
				promptMu.Unlock()
			}
		}
	})
	// The synthesised home is injected before the service starts: writing it after
	// construction races the background event loop and the automation scheduler.
	service, _ := newAutomationTestServiceWithHome(t, handler, home)
	service.verifyPoll = 20 * time.Millisecond
	service.verifyWindow = 500 * time.Millisecond
	// The session map passed by the caller is the source of truth, so a test can set
	// pending_interaction and have the gate observe it.
	raw, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := mapNativeSession(raw)
	if err != nil {
		t.Fatal(err)
	}
	if mapped.PendingInteraction == "" {
		mapped.PendingInteraction = "none"
	}
	service.cache["s1"] = automationTestCache(mapped, time.Now().UTC())
	return service
}

func runningSession() map[string]any {
	session := nativeTestSession("s1", 100, nil)
	session["busy"] = true
	session["main_turn_active"] = true
	return session
}

// TestKeepAliveTriggersWhileSubagentProducesActivity is the core D8 fix: subagent
// progress keeps lastActivity fresh but must not stop the keepalive, because the
// keepalive clock is the main agent's own last model request.
func TestKeepAliveTriggersWhileSubagentProducesActivity(t *testing.T) {
	home := t.TempDir()
	keepAliveFixture{home: home, mainAge: 40 * time.Minute, background: true}.write(t, "synthetic/model")
	prompts := []string{}
	service := keepAliveService(t, home, runningSession(), &prompts, nil)
	// lastActivity is recent: the old implementation would treat this as active and
	// never fire.
	service.mu.Lock()
	cached := service.cache["s1"]
	cached.lastActivity = time.Now().UTC()
	service.cache["s1"] = cached
	service.mu.Unlock()

	settings := defaultAutomationSettings()
	settings.KeepAlive.Enabled = true
	settings.KeepAlive.Minutes = 25
	service.runKeepAliveOnce(context.Background(), settings, "s1", time.Now().UTC())

	if len(prompts) != 1 {
		t.Fatalf("prompts sent = %d, want 1 (subagent activity must not suppress keepalive)", len(prompts))
	}
	if prompts[0] != keepAlivePrompt {
		t.Fatalf("prompt = %q, want the fixed keepalive text", prompts[0])
	}
}

// TestKeepAliveBlockedByApproval checks a pending approval prevents any send.
func TestKeepAliveBlockedByApproval(t *testing.T) {
	home := t.TempDir()
	keepAliveFixture{home: home, mainAge: 40 * time.Minute, background: true}.write(t, "synthetic/model")
	prompts := []string{}
	session := runningSession()
	session["pending_interaction"] = "approval"
	service := keepAliveService(t, home, session, &prompts, nil)
	settings := defaultAutomationSettings()
	settings.KeepAlive.Enabled = true
	settings.KeepAlive.Minutes = 25
	service.runKeepAliveOnce(context.Background(), settings, "s1", time.Now().UTC())
	if len(prompts) != 0 {
		t.Fatalf("prompts sent = %d, want 0 while an approval is pending", len(prompts))
	}
}

// TestKeepAliveSkipsIdleSession asserts an idle session is never kept alive - that
// is compaction's job (D8).
func TestKeepAliveSkipsIdleSession(t *testing.T) {
	home := t.TempDir()
	keepAliveFixture{home: home, mainAge: 40 * time.Minute, background: false}.write(t, "synthetic/model")
	prompts := []string{}
	session := nativeTestSession("s1", 100, nil)
	session["busy"] = false
	session["main_turn_active"] = false
	service := keepAliveService(t, home, session, &prompts, nil)
	settings := defaultAutomationSettings()
	settings.KeepAlive.Enabled = true
	settings.KeepAlive.Minutes = 25
	service.runKeepAliveOnce(context.Background(), settings, "s1", time.Now().UTC())
	if len(prompts) != 0 {
		t.Fatalf("prompts sent = %d, want 0 for an idle session", len(prompts))
	}
}

// TestKeepAliveSkipsWithoutBackgroundAgent asserts no background subagent means no
// keepalive even when the main agent has been quiet long enough.
func TestKeepAliveSkipsWithoutBackgroundAgent(t *testing.T) {
	home := t.TempDir()
	keepAliveFixture{home: home, mainAge: 40 * time.Minute, background: false}.write(t, "synthetic/model")
	prompts := []string{}
	service := keepAliveService(t, home, runningSession(), &prompts, nil)
	settings := defaultAutomationSettings()
	settings.KeepAlive.Enabled = true
	settings.KeepAlive.Minutes = 25
	service.runKeepAliveOnce(context.Background(), settings, "s1", time.Now().UTC())
	if len(prompts) != 0 {
		t.Fatalf("prompts sent = %d, want 0 with no open background subagent", len(prompts))
	}
}

// TestKeepAliveSkipsBelowThreshold asserts the threshold gates the send.
func TestKeepAliveSkipsBelowThreshold(t *testing.T) {
	home := t.TempDir()
	keepAliveFixture{home: home, mainAge: 5 * time.Minute, background: true}.write(t, "synthetic/model")
	prompts := []string{}
	service := keepAliveService(t, home, runningSession(), &prompts, nil)
	settings := defaultAutomationSettings()
	settings.KeepAlive.Enabled = true
	settings.KeepAlive.Minutes = 25
	service.runKeepAliveOnce(context.Background(), settings, "s1", time.Now().UTC())
	if len(prompts) != 0 {
		t.Fatalf("prompts sent = %d, want 0 below the threshold", len(prompts))
	}
}

// TestKeepAliveRunsWithUnreadGoal pins the split between the two goal tests. A cache
// entry discovered through the KAP session list has goalKnown=false until something
// fetches the goal, and treating that as blocked used to silence keepalive for every
// such session. Recovery must keep the opposite answer, so the two cannot share one
// helper: goalBlocksIdle ignores an unread goal, goalAutomationBlocked does not.
func TestKeepAliveRunsWithUnreadGoal(t *testing.T) {
	home := t.TempDir()
	keepAliveFixture{home: home, mainAge: 40 * time.Minute, background: true}.write(t, "synthetic/model")
	prompts := []string{}
	service := keepAliveService(t, home, runningSession(), &prompts, nil)
	service.mu.Lock()
	cached := service.cache["s1"]
	cached.goalKnown = false
	service.cache["s1"] = cached
	service.mu.Unlock()
	settings := defaultAutomationSettings()
	settings.KeepAlive.Enabled = true
	settings.KeepAlive.Minutes = 25
	service.runKeepAliveOnce(context.Background(), settings, "s1", time.Now().UTC())
	if len(prompts) != 1 {
		t.Fatalf("prompts sent = %d, want 1: an unread goal must not block keepalive", len(prompts))
	}

	snapshot := automationSnapshot{goalKnown: false}
	if goalBlocksIdle(snapshot) {
		t.Fatal("an unread goal must not block keepalive or compaction")
	}
	if !goalAutomationBlocked(snapshot) {
		t.Fatal("an unread goal must block recovery: an unknown budget must not be spent")
	}
}

// TestKeepAliveBlockedWaitIsSkippedNotReportedAsSuccess encodes the measured state B
// result: during a blocking wait a keepalive message is only queued, so the action
// must be recorded as skipped with a reason instead of pretending success.
func TestKeepAliveBlockedWaitIsSkippedNotReportedAsSuccess(t *testing.T) {
	home := t.TempDir()
	keepAliveFixture{home: home, mainAge: 40 * time.Minute, background: true, blocked: true}.write(t, "synthetic/model")
	prompts := []string{}
	service := keepAliveService(t, home, runningSession(), &prompts, nil)
	settings := defaultAutomationSettings()
	settings.KeepAlive.Enabled = true
	settings.KeepAlive.Minutes = 25
	service.runKeepAliveOnce(context.Background(), settings, "s1", time.Now().UTC())

	if len(prompts) != 0 {
		t.Fatalf("prompts sent = %d, want 0 during a blocking wait", len(prompts))
	}
	var state, note string
	err := service.db.QueryRow(`SELECT state,result FROM automation_intents WHERE kind='keepalive' AND session_id='s1' ORDER BY rowid DESC LIMIT 1`).Scan(&state, &note)
	if err != nil {
		t.Fatal(err)
	}
	if state != keepAliveSkipped {
		t.Fatalf("state = %q, want %q", state, keepAliveSkipped)
	}
	if !strings.Contains(note, "阻塞") {
		t.Fatalf("note = %q, want it to explain the blocking wait", note)
	}
}

// TestSupersededKeepAliveDoesNotClaimTheNewerStep reproduces what happened on the
// isolated host. The first keepalive produced no model request at all during its whole
// window; the cooldown expired, a second keepalive fired, and the step it caused landed
// 30ms later. The first intent must not present that step as its own success.
func TestSupersededKeepAliveDoesNotClaimTheNewerStep(t *testing.T) {
	hit := stepWith(1100, 0)
	hit.StepUUID = "step-from-the-newer-send"

	superseded := decideKeepAlive(true, hit, "anchor", "synthetic/model", true, false)
	if !superseded.Done || superseded.Verdict.State != keepAliveInvalid {
		t.Fatalf("superseded verdict = %+v, want invalid: the step belongs to the newer keepalive", superseded)
	}
	if superseded.Verdict.CacheRead != 0 {
		t.Fatalf("superseded verdict reported cache read %d, which is the newer attempt's hit", superseded.Verdict.CacheRead)
	}
	// Expiry must not let it claim anything either.
	expiredAndSuperseded := decideKeepAlive(true, hit, "anchor", "synthetic/model", true, true)
	if expiredAndSuperseded.Verdict.State != keepAliveInvalid {
		t.Fatalf("expired superseded verdict = %+v, want invalid", expiredAndSuperseded)
	}

	// The owning keepalive keeps the normal behaviour.
	owner := decideKeepAlive(true, hit, "anchor", "synthetic/model", false, false)
	if !owner.Done || owner.Verdict.State != keepAliveVerified || owner.Verdict.CacheRead != 1100 {
		t.Fatalf("owning keepalive verdict = %+v, want verified with read 1100", owner)
	}
	// The anchor step means no new request happened yet, so the loop keeps polling.
	anchorStep := stepWith(1100, 0)
	anchorStep.StepUUID = "anchor"
	if pending := decideKeepAlive(true, anchorStep, "anchor", "synthetic/model", false, false); pending.Done {
		t.Fatalf("anchor step concluded the loop: %+v", pending)
	}
	if expired := decideKeepAlive(false, cacheStepAlias{}, "anchor", "synthetic/model", false, true); !expired.Done || expired.Verdict.State != keepAliveInvalid {
		t.Fatalf("expired verdict = %+v, want invalid", expired)
	}
	if pending := decideKeepAlive(false, cacheStepAlias{}, "anchor", "synthetic/model", false, false); pending.Done {
		t.Fatalf("loop concluded before the window expired: %+v", pending)
	}
}

// TestSupersedeQueryScopesToSession checks the database half of the guard.
func TestSupersedeQueryScopesToSession(t *testing.T) {
	service, _ := newAutomationTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	now := time.Now().UTC()
	for _, spec := range []struct {
		id      string
		session string
		at      time.Time
	}{
		{"older", "s1", now.Add(-5 * time.Minute)},
		{"newer", "s1", now},
		{"elsewhere", "s2", now},
	} {
		if _, err := service.db.Exec(`INSERT INTO automation_intents(id,kind,host_id,session_id,model_id,context_revision,state,created_at,updated_at,result) VALUES(?,?,?,?,?,?,?,?,?,'')`,
			spec.id, "keepalive", "native", spec.session, "", "", "acknowledged", spec.at.Format(time.RFC3339Nano), spec.at.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	if superseded, err := service.keepAliveSuperseded("older", "s1"); err != nil || !superseded {
		t.Fatalf("older keepalive superseded = %v, %v; want true", superseded, err)
	}
	if superseded, err := service.keepAliveSuperseded("newer", "s1"); err != nil || superseded {
		t.Fatalf("newest keepalive superseded = %v, %v; want false", superseded, err)
	}
	// A keepalive in another session must not supersede this one.
	if superseded, err := service.keepAliveSuperseded("older", "s2"); err != nil || superseded {
		t.Fatalf("cross-session supersede = %v, %v; want false", superseded, err)
	}
}

// TestJudgeKeepAliveVerdicts covers the three outcomes of the closed loop.
func TestJudgeKeepAliveVerdicts(t *testing.T) {
	hit := judgeKeepAlive(stepWith(120, 0), "synthetic/model")
	if hit.State != keepAliveVerified || hit.CacheRead != 120 {
		t.Fatalf("hit verdict = %+v", hit)
	}
	unreported := judgeKeepAlive(stepWith(0, 0), "synthetic/model")
	if unreported.State != keepAliveUnknown {
		t.Fatalf("double-zero verdict = %+v, want unknown rather than a fake success", unreported)
	}
	miss := judgeKeepAlive(stepWith(0, 500), "synthetic/model")
	if miss.State != keepAliveInvalid {
		t.Fatalf("miss verdict = %+v, want invalid", miss)
	}
}

func TestKeepAlivePromptIsFixedAndAsksForNothing(t *testing.T) {
	if strings.Contains(keepAlivePrompt, "继续") && !strings.Contains(keepAlivePrompt, "继续等待") {
		t.Fatal("keepalive prompt must not be a bare instruction to continue")
	}
	for _, required := range []string{"缓存保活", "不需要任何操作"} {
		if !strings.Contains(keepAlivePrompt, required) {
			t.Fatalf("keepalive prompt is missing %q", required)
		}
	}
}

func TestKeepAliveThresholdRange(t *testing.T) {
	settings := defaultAutomationSettings()
	for _, minutes := range []int{4, 61, 120} {
		settings.KeepAlive.Minutes = minutes
		if err := validateAutomationSettings(settings); err == nil {
			t.Fatalf("minutes=%d should be rejected", minutes)
		}
	}
	for _, minutes := range []int{5, 25, 60} {
		settings.KeepAlive.Minutes = minutes
		if err := validateAutomationSettings(settings); err != nil {
			t.Fatalf("minutes=%d should be accepted: %v", minutes, err)
		}
	}
}

func stepWith(read, creation int64) cacheStepAlias {
	return cacheStepAlias{InputCacheRead: read, InputCacheCreation: creation}
}

// waitForKeepAliveState polls the intent row instead of waiting on the service's
// WaitGroup: that WaitGroup also tracks the resident scheduler goroutine, so waiting
// on it would block until shutdown.
func waitForKeepAliveState(t *testing.T, service *NativeService, want string) (string, string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var state, note string
		err := service.db.QueryRow(`SELECT state,result FROM automation_intents WHERE kind='keepalive' ORDER BY rowid DESC LIMIT 1`).Scan(&state, &note)
		if err == nil && state == want {
			return state, note
		}
		time.Sleep(10 * time.Millisecond)
	}
	var state, note string
	_ = service.db.QueryRow(`SELECT state,result FROM automation_intents WHERE kind='keepalive' ORDER BY rowid DESC LIMIT 1`).Scan(&state, &note)
	return state, note
}
