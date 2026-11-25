package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProviderClassificationAndFallback(t *testing.T) {
	config := newIdleTestHarness("kimi-code/kimi-for-coding").config
	for _, tc := range []struct {
		model, provider string
		interval        int
	}{
		{"kimi-code/kimi-for-coding", ProviderTypeSubscription, 25},
		{"moonshot/kimi-k2", ProviderTypeOpenPlatform, 4},
		{"custom/model", ProviderTypeOther, 4},
	} {
		policy := applyRetentionPolicy(resolveCachePolicyFromConfig(tc.model, config), nil)
		if policy.ProviderType != tc.provider || policy.IntervalMinutes != tc.interval || policy.Source != "fallback" {
			t.Errorf("model %s: unexpected policy %+v", tc.model, policy)
		}
	}
}

type idleTestHarness struct {
	mu              sync.Mutex
	session         map[string]any
	turns           []NativeTurn
	goal            any
	usage           any
	config          map[string]any
	prompts         []map[string]any
	undos           []map[string]any
	compactions     int
	undoFail        bool
	appendExtraTurn bool
	emitToolCall    bool
	turnsReads      int
	promptTurnsBase int
	// onTurnsRead runs with h.mu held when the configured turn read after the
	// keepalive prompt is served. It stands in for a user action that lands while
	// the pre-undo Git scan runs.
	onTurnsRead      func()
	onTurnsReadIndex int
	mutateWorkspace  func() error
	nextTurnNum      int
	wirePath         string
	probeCacheRead   int
	probeCacheWrite  int
	rejectPrompt     bool
	slowPrompt       bool
	acknowledge      chan struct{}
}

func newIdleTestHarness(model string) *idleTestHarness {
	limit := int64(200000)
	sess := nativeTestSession("s1", 150000, &limit)
	sess["agent_config"] = map[string]any{"model": model}
	delete(sess, "metadata")
	return &idleTestHarness{
		session: sess,
		turns: []NativeTurn{
			{
				TurnID: "turn-user-1",
				Items: []NativeItem{
					{ItemID: "msg-u1", Type: "user_message", Text: "hello"},
					{ItemID: "msg-a1", Type: "assistant_message", Text: "hi"},
				},
			},
		},
		goal: nil,
		usage: map[string]any{
			"kind": "ok",
			"quota": map[string]any{
				"usages": map[string]any{
					"limit5h": map[string]any{"usedRatio": 0.25},
					"weekly":  map[string]any{"usedRatio": 0.30},
				},
			},
		},
		config: map[string]any{
			"default_model": model,
			"providers": map[string]any{
				"kimi-code": map[string]any{"type": "kimi", "oauth": map[string]any{"key": "kimi-code"}},
				"moonshot":  map[string]any{"type": "openai_legacy", "base_url": "https://api.moonshot.cn/v1"},
				"custom":    map[string]any{"type": "openai_legacy", "base_url": "https://example.com/v1"},
			},
			"models": map[string]any{
				"kimi-code/kimi-for-coding": map[string]any{"provider": "kimi-code", "model": "kimi-for-coding"},
				"moonshot/kimi-k2":          map[string]any{"provider": "moonshot", "model": "kimi-k2"},
				"custom/model":              map[string]any{"provider": "custom", "model": "model"},
			},
		},
		nextTurnNum:    1,
		probeCacheRead: 900,
	}
}

func (h *idleTestHarness) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/config":
			writeNativeTestEnvelope(w, h.config)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions":
			writeNativeTestEnvelope(w, map[string]any{"items": []any{h.session}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/s1":
			writeNativeTestEnvelope(w, h.session)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/s1/agents/main/turns":
			h.turnsReads++
			if h.onTurnsRead != nil && len(h.prompts) > 0 && h.turnsReads-h.promptTurnsBase == h.onTurnsReadIndex {
				h.onTurnsRead()
			}
			cp := append([]NativeTurn(nil), h.turns...)
			writeNativeTestEnvelope(w, map[string]any{"turns": cp})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/s1/goal":
			writeNativeTestEnvelope(w, h.goal)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/oauth/usage":
			writeNativeTestEnvelope(w, h.usage)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/s1/prompts":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			h.prompts = append(h.prompts, body)
			if h.rejectPrompt {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			h.promptTurnsBase = h.turnsReads
			turnID := "turn-ka-" + string(rune('0'+h.nextTurnNum))
			userMsgID := "msg-ka-u-" + string(rune('0'+h.nextTurnNum))
			h.nextTurnNum++
			content := body["content"].([]any)
			message := content[0].(map[string]any)["text"].(string)
			items := []NativeItem{{ItemID: userMsgID, Type: "user_message", Text: message}}
			if h.emitToolCall {
				items = append(items, NativeItem{ItemID: "tool-1", Type: "tool_call", Name: "Bash"})
			}
			if !h.slowPrompt {
				items = append(items, NativeItem{ItemID: "msg-ka-a", Type: "assistant_message", Text: "1"})
			}
			h.turns = append(h.turns, NativeTurn{TurnID: turnID, Items: items})
			if h.wirePath != "" && (h.probeCacheRead > 0 || h.probeCacheWrite > 0) {
				appendIdleTestStep(t, h.wirePath, h.session["agent_config"].(map[string]any)["model"].(string), turnID, time.Now().UTC(), h.probeCacheRead, h.probeCacheWrite)
			}
			if h.acknowledge != nil {
				close(h.acknowledge)
			}
			if h.mutateWorkspace != nil {
				if err := h.mutateWorkspace(); err != nil {
					t.Error(err)
				}
			}
			if h.appendExtraTurn {
				h.turns = append(h.turns, NativeTurn{
					TurnID: "turn-user-new",
					Items: []NativeItem{
						{ItemID: "msg-u-new", Type: "user_message", Text: "用户新消息"},
						{ItemID: "msg-a-new", Type: "assistant_message", Text: "好的"},
					},
				})
			}
			writeNativeTestEnvelope(w, map[string]any{
				"prompt_id":       "prompt-" + turnID,
				"user_message_id": userMsgID,
				"status":          "accepted",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/s1:undo":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			h.undos = append(h.undos, body)
			if h.undoFail {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 50001, "msg": "undo rejected"})
				return
			}
			targetID, _ := body["turn_id"].(string)
			kept := []NativeTurn{}
			removed := []string{}
			for _, turn := range h.turns {
				if turn.TurnID == targetID {
					removed = append(removed, turn.TurnID)
					continue
				}
				kept = append(kept, turn)
			}
			h.turns = kept
			writeNativeTestEnvelope(w, map[string]any{"removed_turn_ids": removed})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/s1:compact":
			h.compactions++
			writeNativeTestEnvelope(w, map[string]any{})
		default:
			writeNativeTestEnvelope(w, map[string]any{})
		}
	})
}

func startIdleKeepAliveService(t *testing.T, h *idleTestHarness, home string, lastActivity time.Time) *NativeService {
	t.Helper()
	if h.session["metadata"] == nil {
		h.session["metadata"] = map[string]any{"cwd": home}
	}
	h.wirePath = filepath.Join(home, "sessions", "wd_test", "session_s1", "agents", "main", "wire.jsonl")
	service, _ := newAutomationTestServiceWithHome(t, h.handler(t), home)
	service.verifyPoll = 15 * time.Millisecond
	service.idleVerifyPoll = 15 * time.Millisecond
	service.verifyWindow = 800 * time.Millisecond
	raw, err := json.Marshal(h.session)
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := mapNativeSession(raw)
	if err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	service.cache["s1"] = cachedSession{
		session:      mapped,
		turns:        append([]NativeTurn(nil), h.turns...),
		goalKnown:    true,
		revision:     1,
		lastActivity: lastActivity,
	}
	service.mu.Unlock()
	return service
}

func appendIdleTestStep(t *testing.T, path, model, turn string, at time.Time, read, creation int) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	usage := fmt.Sprintf(`{"inputOther":1000,"output":1,"inputCacheRead":%d,"inputCacheCreation":%d}`, read, creation)
	if _, err = fmt.Fprintf(file, `{"type":"usage.record","agentId":"main","model":%q,"usage":%s,"usageScope":"turn","time":%d}`+"\n"+
		`{"type":"context.append_loop_event","agentId":"main","event":{"type":"step.end","uuid":%q,"turnId":%q,"step":1,"finishReason":"end_turn","usage":%s},"time":%d}`+"\n",
		model, usage, at.UnixMilli(), "step-"+turn, turn, usage, at.UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

func waitUntilIdleDone(t *testing.T, service *NativeService, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !service.isIdleKeepAliveInFlight(id) {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("idle keepalive for %s stayed in flight past deadline", id)
}

func TestIdleKeepAliveCountLimitUndoAndUserReset(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC().Truncate(time.Millisecond)
	keepAliveFixture{home: home, mainAge: 26 * time.Minute, background: false, lastCacheRead: 900}.write(t, "kimi-code/kimi-for-coding")

	h := newIdleTestHarness("kimi-code/kimi-for-coding")
	service := startIdleKeepAliveService(t, h, home, now.Add(-26*time.Minute))

	settings := defaultAutomationSettings()
	settings.IdleKeepAlive.Enabled = true
	settings.IdleKeepAlive.DefaultCount = 2
	if err := service.storeAutomationSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}

	// Keepalive #1 at +26m (within 30m TTL, >= 25m interval).
	service.runIdleKeepAliveOnce(context.Background(), settings, "s1", now)
	waitUntilIdleDone(t, service, "s1")

	h.mu.Lock()
	if len(h.prompts) != 1 || len(h.undos) != 1 {
		t.Fatalf("after KA #1: prompts=%d undos=%d, want 1/1", len(h.prompts), len(h.undos))
	}
	if _, hasModel := h.prompts[0]["model"]; hasModel {
		t.Fatalf("keepalive must not override model parameter: %+v", h.prompts[0])
	}
	if _, hasThinking := h.prompts[0]["thinking"]; hasThinking {
		t.Fatalf("keepalive must not override thinking parameter: %+v", h.prompts[0])
	}
	if h.undos[0]["turn_id"] != "turn-ka-1" {
		t.Fatalf("undo targeted %v, want turn-ka-1", h.undos[0]["turn_id"])
	}
	if len(h.turns) != 1 || h.turns[0].TurnID != "turn-user-1" {
		t.Fatalf("turns after undo = %+v, want only original turn-user-1", h.turns)
	}
	h.mu.Unlock()

	// Keepalive #2 at +26m after KA #1.
	t2 := now.Add(26 * time.Minute)
	service.runIdleKeepAliveOnce(context.Background(), settings, "s1", t2)
	waitUntilIdleDone(t, service, "s1")

	h.mu.Lock()
	if len(h.prompts) != 2 || len(h.undos) != 2 {
		t.Fatalf("after KA #2: prompts=%d undos=%d, want 2/2", len(h.prompts), len(h.undos))
	}
	if h.undos[1]["turn_id"] != "turn-ka-2" {
		t.Fatalf("second undo targeted %v, want turn-ka-2", h.undos[1]["turn_id"])
	}
	h.mu.Unlock()

	// Attempt #3 after count exhausted: must NOT send.
	t3 := t2.Add(26 * time.Minute)
	service.runIdleKeepAliveOnce(context.Background(), settings, "s1", t3)
	h.mu.Lock()
	if len(h.prompts) != 2 {
		t.Fatalf("prompts after count exhaustion = %d, want 2", len(h.prompts))
	}
	h.mu.Unlock()

	view, err := service.sessionKeepAliveView(context.Background(), "s1", t3)
	if err != nil {
		t.Fatal(err)
	}
	if view.StoppedReason != "count_exhausted" || view.UsedCount != 2 {
		t.Fatalf("exhausted view = %+v, want count_exhausted / used=2", view)
	}

	// User sends a new message -> count and timer reset.
	userAt := t3.Add(2 * time.Minute)
	service.noteUserMessage(context.Background(), "s1", userAt)
	afterReset, err := service.sessionKeepAliveView(context.Background(), "s1", userAt)
	if err != nil {
		t.Fatal(err)
	}
	if afterReset.UsedCount != 0 || afterReset.StoppedReason != "" {
		t.Fatalf("after user reset = %+v, want used=0 and empty stop reason", afterReset)
	}

	// User activity alone cannot warm the prefix. Add the real model step.
	h.mu.Lock()
	h.turns = append(h.turns, NativeTurn{TurnID: "turn-user-2", Items: []NativeItem{{Type: "user_message", Text: "new request"}, {Type: "assistant_message", Text: "hello"}}})
	h.mu.Unlock()
	appendIdleTestStep(t, h.wirePath, "kimi-code/kimi-for-coding", "turn-user-2", userAt.Add(time.Second), 900, 0)
	// 26m after the real request, keepalive fires again.
	t4 := userAt.Add(26 * time.Minute)
	service.runIdleKeepAliveOnce(context.Background(), settings, "s1", t4)
	waitUntilIdleDone(t, service, "s1")
	h.mu.Lock()
	if len(h.prompts) != 3 {
		t.Fatalf("prompts after reset = %d, want 3", len(h.prompts))
	}
	h.mu.Unlock()
}

func TestIdleKeepAliveBlocksCompactionUntilFinished(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC().Truncate(time.Millisecond)
	keepAliveFixture{home: home, mainAge: 26 * time.Minute, background: false}.write(t, "kimi-code/kimi-for-coding")

	h := newIdleTestHarness("kimi-code/kimi-for-coding")
	service := startIdleKeepAliveService(t, h, home, now.Add(-30*time.Minute))

	settings := defaultAutomationSettings()
	settings.IdleKeepAlive.Enabled = true
	settings.IdleKeepAlive.DefaultCount = 1
	settings.Compaction.Enabled = true
	settings.Compaction.IdleMinutes = 25
	settings.Compaction.ContextThreshold = 50
	if err := service.storeAutomationSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}

	if _, err := service.cacheScan(context.Background(), now, false); err != nil {
		t.Fatal(err)
	}
	// While keepalive still has budget (0/1), compaction MUST NOT run.
	service.runCompaction(context.Background(), settings, "s1", now)
	h.mu.Lock()
	if h.compactions != 0 {
		t.Fatalf("compactions during active keepalive = %d, want 0", h.compactions)
	}
	h.mu.Unlock()

	// Fire the single keepalive.
	service.runIdleKeepAliveOnce(context.Background(), settings, "s1", now)
	waitUntilIdleDone(t, service, "s1")

	// Right after keepalive (10m later, still inside 25m refreshed cache window), compaction still holds off.
	service.runCompaction(context.Background(), settings, "s1", now.Add(10*time.Minute))
	h.mu.Lock()
	if h.compactions != 0 {
		t.Fatalf("compactions inside refreshed window = %d, want 0", h.compactions)
	}
	h.mu.Unlock()

	// After the keepalive's refreshed interval finishes (+26m), compaction takes over!
	service.runCompaction(context.Background(), settings, "s1", now.Add(26*time.Minute))
	h.mu.Lock()
	if h.compactions != 1 {
		t.Fatalf("compactions after keepalive ended = %d, want 1", h.compactions)
	}
	h.mu.Unlock()
}

func TestIdleKeepAliveGuardrails(t *testing.T) {
	t.Run("paused during account switch", func(t *testing.T) {
		home := t.TempDir()
		now := time.Now().UTC().Truncate(time.Millisecond)
		keepAliveFixture{home: home, mainAge: 26 * time.Minute}.write(t, "kimi-code/kimi-for-coding")
		h := newIdleTestHarness("kimi-code/kimi-for-coding")
		service := startIdleKeepAliveService(t, h, home, now.Add(-26*time.Minute))
		settings := defaultAutomationSettings()
		settings.IdleKeepAlive.Enabled = true
		_ = service.storeAutomationSettings(context.Background(), settings)

		resume := service.pauseForAccountSwitch()
		service.runIdleKeepAliveOnce(context.Background(), settings, "s1", now)
		h.mu.Lock()
		if len(h.prompts) != 0 {
			t.Fatalf("prompts while account switch paused = %d, want 0", len(h.prompts))
		}
		h.mu.Unlock()
		resume()

		service.runIdleKeepAliveOnce(context.Background(), settings, "s1", now)
		waitUntilIdleDone(t, service, "s1")
		h.mu.Lock()
		if len(h.prompts) != 1 {
			t.Fatalf("prompts after switch resume = %d, want 1", len(h.prompts))
		}
		h.mu.Unlock()
	})

	t.Run("stops on low quota and emits toast alert", func(t *testing.T) {
		home := t.TempDir()
		now := time.Now().UTC().Truncate(time.Millisecond)
		keepAliveFixture{home: home, mainAge: 26 * time.Minute}.write(t, "kimi-code/kimi-for-coding")
		h := newIdleTestHarness("kimi-code/kimi-for-coding")
		h.usage = map[string]any{
			"kind": "ok",
			"quota": map[string]any{
				"usages": map[string]any{
					"limit5h": map[string]any{"usedRatio": 0.95},
				},
			},
		}
		service := startIdleKeepAliveService(t, h, home, now.Add(-26*time.Minute))
		settings := defaultAutomationSettings()
		settings.IdleKeepAlive.Enabled = true
		settings.IdleKeepAlive.MinQuotaPercent = 10
		_ = service.storeAutomationSettings(context.Background(), settings)

		service.runIdleKeepAliveOnce(context.Background(), settings, "s1", now)
		h.mu.Lock()
		if len(h.prompts) != 0 {
			t.Fatalf("prompts on low quota = %d, want 0", len(h.prompts))
		}
		h.mu.Unlock()
		view, _ := service.sessionKeepAliveView(context.Background(), "s1", now)
		if view.StoppedReason != "quota_low" || !strings.Contains(view.StoppedDetail, "剩余额度") {
			t.Fatalf("low quota view = %+v", view)
		}
		batch := service.bus.Poll(context.Background(), 0, []string{"keepalive"}, 10*time.Millisecond)
		foundAlert := false
		for _, event := range batch.Events {
			if payload, ok := event.Value.(map[string]any); ok && payload["type"] == "keepalive.alert" {
				foundAlert = true
			}
		}
		if !foundAlert {
			t.Fatalf("expected keepalive.alert toast event on low quota stop, got %+v", batch.Events)
		}
	})

	t.Run("blocks on active goal, pending approval, archived, and shutdown past TTL", func(t *testing.T) {
		cases := []struct {
			name    string
			mainAge time.Duration
			mutate  func(h *idleTestHarness)
		}{
			{"active goal", 26 * time.Minute, func(h *idleTestHarness) { h.goal = map[string]any{"status": "active"} }},
			{"pending approval", 26 * time.Minute, func(h *idleTestHarness) { h.session["pending_interaction"] = "approval" }},
			{"archived session", 26 * time.Minute, func(h *idleTestHarness) { h.session["archived"] = true }},
			{"shutdown past TTL no catchup", 45 * time.Minute, func(h *idleTestHarness) {}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				home := t.TempDir()
				now := time.Now().UTC().Truncate(time.Millisecond)
				keepAliveFixture{home: home, mainAge: tc.mainAge}.write(t, "kimi-code/kimi-for-coding")
				h := newIdleTestHarness("kimi-code/kimi-for-coding")
				tc.mutate(h)
				service := startIdleKeepAliveService(t, h, home, now.Add(-tc.mainAge))
				settings := defaultAutomationSettings()
				settings.IdleKeepAlive.Enabled = true
				_ = service.storeAutomationSettings(context.Background(), settings)

				service.runIdleKeepAliveOnce(context.Background(), settings, "s1", now)
				h.mu.Lock()
				if len(h.prompts) != 0 {
					t.Fatalf("%s: prompts = %d, want 0", tc.name, len(h.prompts))
				}
				h.mu.Unlock()
			})
		}
	})
}

func newIdleTestGitRepository(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	runIdleTestGit(t, repo, "init")
	return repo
}

func runIdleTestGit(t *testing.T, cwd string, args ...string) []byte {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", cwd}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return out
}

func writeIdleTestFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGitStatusFingerprint(t *testing.T) {
	ctx := context.Background()
	repo := newIdleTestGitRepository(t)
	nested := filepath.Join(repo, "untracked directory")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	oldPath, newPath := "old name", "new name"
	if os.PathSeparator == '/' {
		oldPath += "\nold"
		newPath += " -> name\tnew"
	}
	writeIdleTestFile(t, filepath.Join(repo, oldPath), "renamed\n")
	writeIdleTestFile(t, filepath.Join(repo, "deleted"), "deleted\n")
	runIdleTestGit(t, repo, "add", oldPath, "deleted")
	runIdleTestGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "init")
	runIdleTestGit(t, repo, "mv", oldPath, newPath)
	if err := os.Remove(filepath.Join(repo, "deleted")); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(nested, "child")
	writeIdleTestFile(t, child, "first\n")
	status := runIdleTestGit(t, repo, "-c", "status.renames=true", "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	for _, entry := range [][]byte{[]byte("R  " + newPath + "\x00" + oldPath + "\x00"), []byte(" D deleted\x00"), []byte("?? untracked directory/\x00")} {
		if !bytes.Contains(status, entry) {
			t.Fatalf("expected %q in status %q", entry, status)
		}
	}
	runIdleTestGit(t, repo, "config", "status.renames", "true")
	before, err := gitStatusFingerprint(ctx, nested)
	if err != nil || before == "" {
		t.Fatalf("fingerprint for rename, deletion, and directory = %q, %v", before, err)
	}
	if repeated, err := gitStatusFingerprint(ctx, repo); err != nil || repeated != before {
		t.Fatalf("repository root and nested working directory fingerprints differ: %q, %q, %v", before, repeated, err)
	}
	writeIdleTestFile(t, child, "other\n")
	if after := runIdleTestGit(t, repo, "status", "--porcelain=v1", "-z", "--untracked-files=normal"); !bytes.Equal(after, status) {
		t.Fatalf("porcelain changed after rewriting an untracked child: %q", after)
	}
	if after, err := gitStatusFingerprint(ctx, repo); err != nil || after == "" || after == before {
		t.Fatalf("directory child rewrite must change the fingerprint: %q, %q, %v", before, after, err)
	}

	t.Run("bounded total content", func(t *testing.T) {
		for i := range gitFingerprintMaxTotalBytes/gitFingerprintMaxFileBytes + 1 {
			path := filepath.Join(nested, "large-"+string(rune('a'+i)))
			writeIdleTestFile(t, path, "")
			if err := os.Truncate(path, gitFingerprintMaxFileBytes); err != nil {
				t.Fatal(err)
			}
		}
		if fingerprint, err := gitStatusFingerprint(ctx, repo); err == nil || fingerprint != "" || !strings.Contains(err.Error(), "超过大小上限") {
			t.Fatalf("fingerprint over cumulative cap = %q, %v", fingerprint, err)
		}
	})

	t.Run("non Git differs from missing working directory", func(t *testing.T) {
		plain := t.TempDir()
		if fingerprint, err := gitStatusFingerprint(ctx, plain); err != nil || fingerprint != "" {
			t.Fatalf("non-Git directory fingerprint = %q, %v", fingerprint, err)
		}
		if fingerprint, err := gitStatusFingerprint(ctx, filepath.Join(plain, "missing")); err == nil || fingerprint != "" {
			t.Fatalf("missing directory fingerprint = %q, %v", fingerprint, err)
		}
		if err := os.Mkdir(filepath.Join(plain, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
		if fingerprint, err := gitStatusFingerprint(ctx, plain); err == nil || fingerprint != "" {
			t.Fatalf("invalid repository fingerprint = %q, %v", fingerprint, err)
		}
	})

	t.Run("unreadable file", func(t *testing.T) {
		repo := newIdleTestGitRepository(t)
		path := filepath.Join(repo, "unreadable")
		writeIdleTestFile(t, path, "unreadable\n")
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(path, 0o600); err != nil {
				t.Error(err)
			}
		})
		if file, err := os.Open(path); err == nil {
			_ = file.Close()
			t.Skip("filesystem or current user can still read the file")
		}
		if fingerprint, err := gitStatusFingerprint(ctx, repo); err == nil || fingerprint != "" || !strings.Contains(err.Error(), "unreadable") {
			t.Fatalf("fingerprint with unreadable path = %q, %v", fingerprint, err)
		}
	})
}

func TestIdleKeepAliveUndoSafetyChecks(t *testing.T) {
	t.Run("skips undo when newer turn arrived", func(t *testing.T) {
		home := t.TempDir()
		now := time.Now().UTC().Truncate(time.Millisecond)
		keepAliveFixture{home: home, mainAge: 26 * time.Minute}.write(t, "kimi-code/kimi-for-coding")
		h := newIdleTestHarness("kimi-code/kimi-for-coding")
		h.appendExtraTurn = true
		service := startIdleKeepAliveService(t, h, home, now.Add(-26*time.Minute))
		settings := defaultAutomationSettings()
		settings.IdleKeepAlive.Enabled = true
		_ = service.storeAutomationSettings(context.Background(), settings)

		service.runIdleKeepAliveOnce(context.Background(), settings, "s1", now)
		waitUntilIdleDone(t, service, "s1")

		h.mu.Lock()
		if len(h.undos) != 0 {
			t.Fatalf("undos = %d, want 0 when a newer turn appeared", len(h.undos))
		}
		h.mu.Unlock()
	})

	t.Run("skips undo when keepalive triggered tool call", func(t *testing.T) {
		home := t.TempDir()
		now := time.Now().UTC().Truncate(time.Millisecond)
		keepAliveFixture{home: home, mainAge: 26 * time.Minute}.write(t, "kimi-code/kimi-for-coding")
		h := newIdleTestHarness("kimi-code/kimi-for-coding")
		h.emitToolCall = true
		service := startIdleKeepAliveService(t, h, home, now.Add(-26*time.Minute))
		settings := defaultAutomationSettings()
		settings.IdleKeepAlive.Enabled = true
		_ = service.storeAutomationSettings(context.Background(), settings)

		service.runIdleKeepAliveOnce(context.Background(), settings, "s1", now)
		waitUntilIdleDone(t, service, "s1")

		h.mu.Lock()
		if len(h.undos) != 0 {
			t.Fatalf("undos = %d, want 0 when tool_call occurred", len(h.undos))
		}
		h.mu.Unlock()
		view, _ := service.sessionKeepAliveView(context.Background(), "s1", now)
		if view.StoppedReason != "undo_failed" {
			t.Fatalf("StoppedReason = %q, want undo_failed", view.StoppedReason)
		}
	})

	for _, tc := range []struct {
		name       string
		largeFirst bool
		largeAfter bool
		wantUndo   bool
		wantNote   string
	}{
		{name: "git status and file content unchanged", wantUndo: true, wantNote: "已撤回保活回合"},
		{name: "git status unchanged but file changed", wantNote: "工作区状态无法确认或已变化"},
		{name: "git fingerprint unknown before prompt", largeFirst: true, wantNote: "暂时无法确认工作区状态"},
		{name: "git fingerprint unknown before undo", largeAfter: true, wantNote: "工作区状态无法确认或已变化"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newIdleTestGitRepository(t)
			file := filepath.Join(repo, "a.txt")
			writeIdleTestFile(t, file, "initial\n")
			runIdleTestGit(t, repo, "add", "a.txt")
			runIdleTestGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "init")
			writeIdleTestFile(t, file, "modified\n")
			if tc.largeFirst {
				if err := os.Truncate(file, gitFingerprintMaxFileBytes+1); err != nil {
					t.Fatal(err)
				}
			}
			porcelainBefore := runIdleTestGit(t, repo, "status", "--porcelain=v1", "-z")

			home := t.TempDir()
			now := time.Now().UTC().Truncate(time.Millisecond)
			keepAliveFixture{home: home, mainAge: 26 * time.Minute}.write(t, "kimi-code/kimi-for-coding")
			h := newIdleTestHarness("kimi-code/kimi-for-coding")
			h.session["metadata"] = map[string]any{"cwd": repo}
			switch {
			case tc.largeAfter:
				h.mutateWorkspace = func() error { return os.Truncate(file, gitFingerprintMaxFileBytes+1) }
			case !tc.largeFirst && !tc.wantUndo:
				h.mutateWorkspace = func() error { return os.WriteFile(file, []byte("changed!\n"), 0o600) }
			}
			service := startIdleKeepAliveService(t, h, home, now.Add(-26*time.Minute))
			settings := defaultAutomationSettings()
			settings.IdleKeepAlive.Enabled = true
			if err := service.storeAutomationSettings(context.Background(), settings); err != nil {
				t.Fatal(err)
			}

			service.runIdleKeepAliveOnce(context.Background(), settings, "s1", now)
			waitUntilIdleDone(t, service, "s1")

			if after := runIdleTestGit(t, repo, "status", "--porcelain=v1", "-z"); len(after) == 0 || !bytes.Equal(after, porcelainBefore) {
				t.Fatalf("expected identical dirty porcelain before and after, got %q and %q", porcelainBefore, after)
			}
			h.mu.Lock()
			prompts, undos := len(h.prompts), len(h.undos)
			h.mu.Unlock()
			wantPrompts, wantUndos := 1, 0
			if tc.largeFirst {
				wantPrompts = 0
			}
			if tc.wantUndo {
				wantUndos = 1
			}
			if prompts != wantPrompts || undos != wantUndos {
				t.Fatalf("prompts = %d, undos = %d, want %d and %d", prompts, undos, wantPrompts, wantUndos)
			}
			view, err := service.sessionKeepAliveView(context.Background(), "s1", now)
			if err != nil || view.StoppedReason != "" || !strings.Contains(view.LastNote, tc.wantNote) {
				t.Fatalf("keepalive view = %+v, err = %v", view, err)
			}
		})
	}

	t.Run("stops and alerts without retry when undo fails", func(t *testing.T) {
		home := t.TempDir()
		now := time.Now().UTC().Truncate(time.Millisecond)
		keepAliveFixture{home: home, mainAge: 26 * time.Minute}.write(t, "kimi-code/kimi-for-coding")
		h := newIdleTestHarness("kimi-code/kimi-for-coding")
		h.undoFail = true
		service := startIdleKeepAliveService(t, h, home, now.Add(-26*time.Minute))
		settings := defaultAutomationSettings()
		settings.IdleKeepAlive.Enabled = true
		settings.IdleKeepAlive.DefaultCount = 3
		_ = service.storeAutomationSettings(context.Background(), settings)

		service.runIdleKeepAliveOnce(context.Background(), settings, "s1", now)
		waitUntilIdleDone(t, service, "s1")

		view, _ := service.sessionKeepAliveView(context.Background(), "s1", now)
		if view.StoppedReason != "undo_failed" || !strings.Contains(view.StoppedDetail, "撤回") {
			t.Fatalf("undo_failed view = %+v", view)
		}
		// Must not retry on next tick.
		service.runIdleKeepAliveOnce(context.Background(), settings, "s1", now.Add(26*time.Minute))
		h.mu.Lock()
		if len(h.prompts) != 1 {
			t.Fatalf("prompts after undo_failed = %d, want 1 (no retry)", len(h.prompts))
		}
		h.mu.Unlock()
	})
}

func TestGitStatusFingerprintCoversIndex(t *testing.T) {
	ctx := context.Background()

	t.Run("MM index blob change with identical working tree", func(t *testing.T) {
		repo := newIdleTestGitRepository(t)
		path := filepath.Join(repo, "mm.txt")
		writeIdleTestFile(t, path, "base\n")
		runIdleTestGit(t, repo, "add", "mm.txt")
		runIdleTestGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "init")

		writeIdleTestFile(t, path, "staged-v1\n")
		runIdleTestGit(t, repo, "add", "mm.txt")
		writeIdleTestFile(t, path, "worktree\n")
		if status := runIdleTestGit(t, repo, "status", "--porcelain=v1", "-z"); !bytes.HasPrefix(status, []byte("MM mm.txt")) {
			t.Fatalf("fixture is not MM: %q", status)
		}
		before, err := gitStatusFingerprint(ctx, repo)
		if err != nil || before == "" {
			t.Fatalf("fingerprint = %q, %v", before, err)
		}

		// Change the index blob only, then put the working tree bytes back
		// byte-for-byte: status and working tree hash stay the same.
		writeIdleTestFile(t, path, "staged-v2-longer\n")
		runIdleTestGit(t, repo, "add", "mm.txt")
		writeIdleTestFile(t, path, "worktree\n")
		if status := runIdleTestGit(t, repo, "status", "--porcelain=v1", "-z"); !bytes.HasPrefix(status, []byte("MM mm.txt")) {
			t.Fatalf("status after index change = %q", status)
		}
		after, err := gitStatusFingerprint(ctx, repo)
		if err != nil || after == "" {
			t.Fatalf("fingerprint after index change = %q, %v", after, err)
		}
		if after == before {
			t.Fatal("index-only change with identical working tree was not detected")
		}
	})

	t.Run("unborn branch staged content", func(t *testing.T) {
		repo := newIdleTestGitRepository(t)
		path := filepath.Join(repo, "first.txt")
		writeIdleTestFile(t, path, "one\n")
		runIdleTestGit(t, repo, "add", "first.txt")
		before, err := gitStatusFingerprint(ctx, repo)
		if err != nil || before == "" {
			t.Fatalf("unborn fingerprint = %q, %v", before, err)
		}
		if repeated, err := gitStatusFingerprint(ctx, repo); err != nil || repeated != before {
			t.Fatalf("unborn fingerprint is unstable: %q, %q, %v", before, repeated, err)
		}
		writeIdleTestFile(t, path, "two\n")
		runIdleTestGit(t, repo, "add", "first.txt")
		after, err := gitStatusFingerprint(ctx, repo)
		if err != nil || after == "" {
			t.Fatalf("unborn fingerprint after restage = %q, %v", after, err)
		}
		if after == before {
			t.Fatal("restaged content on an unborn branch was not detected")
		}
	})

	t.Run("HEAD move with identical working tree", func(t *testing.T) {
		repo := newIdleTestGitRepository(t)
		path := filepath.Join(repo, "a.txt")
		writeIdleTestFile(t, path, "a\n")
		runIdleTestGit(t, repo, "add", "a.txt")
		runIdleTestGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "one")
		before, err := gitStatusFingerprint(ctx, repo)
		if err != nil || before == "" {
			t.Fatalf("fingerprint = %q, %v", before, err)
		}
		runIdleTestGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-m", "two")
		after, err := gitStatusFingerprint(ctx, repo)
		if err != nil || after == "" {
			t.Fatalf("fingerprint after commit = %q, %v", after, err)
		}
		if after == before {
			t.Fatal("a new commit was not reflected in the fingerprint")
		}
	})

	t.Run("unchanged repository stays stable", func(t *testing.T) {
		repo := newIdleTestGitRepository(t)
		writeIdleTestFile(t, filepath.Join(repo, "stable.txt"), "stable\n")
		runIdleTestGit(t, repo, "add", "stable.txt")
		runIdleTestGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "init")
		before, err := gitStatusFingerprint(ctx, repo)
		if err != nil || before == "" {
			t.Fatalf("fingerprint = %q, %v", before, err)
		}
		if after, err := gitStatusFingerprint(ctx, repo); err != nil || after != before {
			t.Fatalf("clean repository fingerprint changed: %q, %q, %v", before, after, err)
		}
	})
}

func TestIdleKeepAliveRechecksActivityAfterGitScan(t *testing.T) {
	// The pre-undo Git scan can take seconds; a user turn that arrives while it
	// runs must cancel the undo. Both cases inject the change on the turn read
	// that follows the scan.
	for _, tc := range []struct {
		name       string
		inject     func(h *idleTestHarness)
		wantNote   string
		wantReason string
	}{
		{
			name: "new user turn during the scan",
			inject: func(h *idleTestHarness) {
				h.turns = append(h.turns, NativeTurn{
					TurnID: "turn-user-late",
					Items: []NativeItem{
						{ItemID: "msg-late-u", Type: "user_message", Text: "用户插话"},
						{ItemID: "msg-late-a", Type: "assistant_message", Text: "收到"},
					},
				})
			},
			wantNote: "会话已变化，本次未撤回",
		},
		{
			name: "session becomes busy during the scan",
			inject: func(h *idleTestHarness) {
				h.session["busy"] = true
				h.session["main_turn_active"] = true
			},
			wantNote: "会话已恢复活动，本次未撤回",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newIdleTestGitRepository(t)
			writeIdleTestFile(t, filepath.Join(repo, "a.txt"), "initial\n")
			runIdleTestGit(t, repo, "add", "a.txt")
			runIdleTestGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "init")

			home := t.TempDir()
			now := time.Now().UTC().Truncate(time.Millisecond)
			keepAliveFixture{home: home, mainAge: 26 * time.Minute}.write(t, "kimi-code/kimi-for-coding")
			h := newIdleTestHarness("kimi-code/kimi-for-coding")
			h.session["metadata"] = map[string]any{"cwd": repo}
			h.onTurnsReadIndex = 2
			h.onTurnsRead = func() { tc.inject(h) }
			service := startIdleKeepAliveService(t, h, home, now.Add(-26*time.Minute))
			settings := defaultAutomationSettings()
			settings.IdleKeepAlive.Enabled = true
			if err := service.storeAutomationSettings(context.Background(), settings); err != nil {
				t.Fatal(err)
			}

			service.runIdleKeepAliveOnce(context.Background(), settings, "s1", now)
			waitUntilIdleDone(t, service, "s1")

			h.mu.Lock()
			prompts, undos, reads := len(h.prompts), len(h.undos), h.turnsReads
			h.mu.Unlock()
			if prompts != 1 {
				t.Fatalf("prompts = %d, want 1", prompts)
			}
			if reads < 3 {
				t.Fatalf("turn reads = %d, the post-scan recheck did not run", reads)
			}
			if undos != 0 {
				t.Fatalf("undos = %d, want 0 when activity resumed during the Git scan", undos)
			}
			view, err := service.sessionKeepAliveView(context.Background(), "s1", now)
			if err != nil {
				t.Fatal(err)
			}
			if view.StoppedReason != "" || !strings.Contains(view.LastNote, tc.wantNote) {
				t.Fatalf("keepalive view = %+v, want note containing %q", view, tc.wantNote)
			}
			if after := runIdleTestGit(t, repo, "status", "--porcelain=v1", "-z"); len(after) != 0 {
				t.Fatalf("test repository was modified: %q", after)
			}
		})
	}
}

func TestKeepAliveGlobalSwitchAndColdActivation(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	now := time.Now().UTC()
	model := "kimi-code/kimi-for-coding"
	keepAliveFixture{home: home, mainAge: 2 * time.Hour}.write(t, model)
	h := newIdleTestHarness(model)
	s := startIdleKeepAliveService(t, h, home, now.Add(-2*time.Hour))
	view, err := s.setSessionKeepAlive(ctx, map[string]any{"id": "s1", "mode": IdleModeCount, "targetCount": float64(3)})
	if err != nil || view.EffectiveEnabled || view.State != KeepAliveStateDisabled {
		t.Fatalf("count override under global off: %+v, %v", view, err)
	}
	if err = s.automationTick(ctx, now); err != nil || s.initializedCacheStore() != nil {
		t.Fatalf("a disabled override initialized a cache scan, err=%v", err)
	}
	settings := defaultAutomationSettings()
	settings.IdleKeepAlive.Enabled = true
	if err = s.storeAutomationSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	s.runIdleKeepAliveOnce(ctx, settings, "s1", now)
	view, err = s.sessionKeepAliveView(ctx, "s1", now)
	if err != nil || view.StoppedReason != "" || view.UsedCount != 0 || !strings.Contains(view.StoppedDetail, "真实请求") {
		t.Fatalf("cold historical session = %+v, %v", view, err)
	}
	appendIdleTestStep(t, h.wirePath, model, "turn-user-2", now.Add(time.Minute), 900, 0)
	h.mu.Lock()
	h.turns = append(h.turns, NativeTurn{TurnID: "turn-user-2", Items: []NativeItem{{Type: "user_message", Text: "next"}}})
	h.mu.Unlock()
	s.runIdleKeepAliveOnce(ctx, settings, "s1", now.Add(27*time.Minute))
	waitUntilIdleDone(t, s, "s1")
	view, err = s.sessionKeepAliveView(ctx, "s1", now.Add(27*time.Minute))
	if err != nil || view.LastResult != keepAliveHit || view.UsedCount != 1 {
		t.Fatalf("keepalive after real request = %+v, %v", view, err)
	}
}

func TestKeepAliveReadDoesNotWriteAndLegacyModeStops(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	home := t.TempDir()
	model := "kimi-code/kimi-for-coding"
	keepAliveFixture{home: home, mainAge: 26 * time.Minute}.write(t, model)
	s := startIdleKeepAliveService(t, newIdleTestHarness(model), home, now.Add(-26*time.Minute))
	if _, err := s.db.Exec(`INSERT INTO session_keepalive_state(session_id,mode,until_at) VALUES('s1','until','2030-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	view, err := s.sessionKeepAliveView(ctx, "s1", now)
	if err != nil || view.State != KeepAliveStateStopped || view.Mode != IdleModeOff || view.StoppedReason != "legacy_until_removed" {
		t.Fatalf("legacy view = %+v, %v", view, err)
	}
	var mode string
	if err := s.db.QueryRow(`SELECT mode FROM session_keepalive_state WHERE session_id='s1'`).Scan(&mode); err != nil || mode != "until" {
		t.Fatalf("get mutated legacy data: mode=%q err=%v", mode, err)
	}
	var tables int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='cache_steps'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("get initialized cache tables: count=%d err=%v", tables, err)
	}
}

func TestKeepAliveBudgetAndMissFuseAcrossProviders(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		model    string
		interval time.Duration
	}{
		{"kimi-code/kimi-for-coding", 25 * time.Minute},
		{"moonshot/kimi-k2", 4 * time.Minute},
		{"custom/model", 4 * time.Minute},
	} {
		t.Run(tc.model, func(t *testing.T) {
			home := t.TempDir()
			now := time.Now().UTC()
			age := tc.interval + 15*time.Second
			keepAliveFixture{home: home, mainAge: age, lastCacheRead: 900}.write(t, tc.model)
			h := newIdleTestHarness(tc.model)
			h.probeCacheRead, h.probeCacheWrite = 0, 1000
			s := startIdleKeepAliveService(t, h, home, now.Add(-age))
			settings := defaultAutomationSettings()
			settings.IdleKeepAlive.Enabled = true
			settings.IdleKeepAlive.DefaultCount = 4
			if err := s.storeAutomationSettings(ctx, settings); err != nil {
				t.Fatal(err)
			}
			for i := range 3 {
				tick := now.Add(time.Duration(i) * age)
				s.runIdleKeepAliveOnce(ctx, settings, "s1", tick)
				waitUntilIdleDone(t, s, "s1")
			}
			view, err := s.sessionKeepAliveView(ctx, "s1", now.Add(2*age))
			if err != nil || view.StoppedReason != "consecutive_misses" || view.LastResult != keepAliveMiss || view.UsedCount != 2 || view.SpentTokens <= 0 {
				t.Fatalf("miss fuse = %+v, %v", view, err)
			}
			if got := decideIdleKeepAlive(sessionKeepAliveRecord{LastRequestAt: now.Add(-age).Format(time.RFC3339Nano), SpentTokens: idleKeepAliveTokenBudget - 100}, view.Policy, now, 4, 101); got.reason != "token_budget" || got.due {
				t.Fatalf("estimate exceeds remaining budget: %+v", got)
			}
		})
	}
}

func TestKeepAliveSlowTurnAndTransientRejection(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	now := time.Now().UTC()
	model := "kimi-code/kimi-for-coding"
	keepAliveFixture{home: home, mainAge: 26 * time.Minute}.write(t, model)
	h := newIdleTestHarness(model)
	h.rejectPrompt = true
	s := startIdleKeepAliveService(t, h, home, now.Add(-26*time.Minute))
	settings := defaultAutomationSettings()
	settings.IdleKeepAlive.Enabled = true
	if err := s.storeAutomationSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	s.runIdleKeepAliveOnce(ctx, settings, "s1", now)
	waitUntilIdleDone(t, s, "s1")
	s.runIdleKeepAliveOnce(ctx, settings, "s1", now.Add(time.Second))
	h.mu.Lock()
	if len(h.prompts) != 1 {
		t.Fatalf("retry fired during backoff: %d prompts", len(h.prompts))
	}
	h.rejectPrompt, h.slowPrompt, h.acknowledge = false, true, make(chan struct{})
	ack := h.acknowledge
	h.mu.Unlock()
	s.runIdleKeepAliveOnce(ctx, settings, "s1", now.Add(time.Minute))
	select {
	case <-ack:
	case <-time.After(3 * time.Second):
		t.Fatal("retry never reached official API")
	}
	time.Sleep(50 * time.Millisecond)
	s.runIdleKeepAliveOnce(ctx, settings, "s1", now.Add(2*time.Minute))
	h.mu.Lock()
	if len(h.prompts) != 2 || len(h.undos) != 0 {
		t.Fatalf("pending reply produced duplicate send or premature undo: prompts=%d undos=%d", len(h.prompts), len(h.undos))
	}
	tail := &h.turns[len(h.turns)-1]
	tail.Items = append(tail.Items, NativeItem{Type: "assistant_message", Text: "1"})
	h.mu.Unlock()
	waitUntilIdleDone(t, s, "s1")
	view, err := s.sessionKeepAliveView(ctx, "s1", now.Add(time.Minute))
	if err != nil || view.LastResult != keepAliveHit || view.UsedCount != 1 || view.StoppedReason != "" {
		t.Fatalf("retry following transient rejection = %+v, %v", view, err)
	}
}

func TestKeepAliveTurnMustOnlyContainMatchingUserMessage(t *testing.T) {
	job := idleKeepAliveJob{preLastTurnID: "previous", message: idleKeepAlivePrompt + " #nonce", userMessageID: "expected"}
	for _, items := range [][]NativeItem{
		{{Type: "user_message", ItemID: "unexpected", Text: job.message}},
		{{Type: "assistant_message", ItemID: "expected", Text: job.message}},
		{{Type: "user_message", ItemID: "expected", Text: job.message}, {Type: "user_message", Text: "real question"}},
	} {
		if isIdleKeepAliveTurn(NativeTurn{TurnID: "candidate", Items: items}, job) {
			t.Errorf("accepted misattributed turn with items %+v", items)
		}
	}
}
