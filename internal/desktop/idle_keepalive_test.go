package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProviderCacheTTLAndIntervalDerivation(t *testing.T) {
	cfg := map[string]any{
		"default_model": "kimi-code/kimi-for-coding",
		"providers": map[string]any{
			"kimi-code": map[string]any{
				"type":  "kimi",
				"oauth": map[string]any{"storage": "file", "key": "kimi-code"},
			},
			"kimi-sub-url": map[string]any{
				"type":     "openai_legacy",
				"base_url": "https://api.kimi.com/coding/v1",
			},
			"moonshot-cn": map[string]any{
				"type":     "openai_legacy",
				"base_url": "https://api.moonshot.cn/v1",
			},
			"moonshot-ai": map[string]any{
				"type":     "openai_legacy",
				"base_url": "https://api.moonshot.ai/v1",
			},
			"custom-other": map[string]any{
				"type":     "openai_legacy",
				"base_url": "https://example.com/v1",
			},
		},
		"models": map[string]any{
			"kimi-code/kimi-for-coding": map[string]any{"provider": "kimi-code", "model": "kimi-for-coding"},
			"kimi-sub-url/k2.5":         map[string]any{"provider": "kimi-sub-url", "model": "k2.5"},
			"moonshot-cn/kimi-k2":       map[string]any{"provider": "moonshot-cn", "model": "kimi-k2"},
			"moonshot-ai/kimi-k2":       map[string]any{"provider": "moonshot-ai", "model": "kimi-k2"},
			"custom-other/gpt":          map[string]any{"provider": "custom-other", "model": "gpt"},
		},
	}

	defaults := defaultProviderTTLSettings()
	cases := []struct {
		name         string
		model        string
		ttlCfg       ProviderTTLSettings
		wantType     string
		wantTTL      int
		wantInterval int
	}{
		{"managed subscription", "kimi-code/kimi-for-coding", defaults, ProviderTypeSubscription, 30, 25},
		{"coding api subscription", "kimi-sub-url/k2.5", defaults, ProviderTypeSubscription, 30, 25},
		{"moonshot cn open platform", "moonshot-cn/kimi-k2", defaults, ProviderTypeOpenPlatform, 5, 4},
		{"moonshot ai open platform", "moonshot-ai/kimi-k2", defaults, ProviderTypeOpenPlatform, 5, 4},
		{"other unknown disabled", "custom-other/gpt", defaults, ProviderTypeOther, 0, 0},
		{"other custom configured", "custom-other/gpt", ProviderTTLSettings{SubscriptionMinutes: 30, OpenPlatformMinutes: 5, OtherMinutes: 12}, ProviderTypeOther, 12, 10},
		{"synthetic 2 min ttl", "kimi-code/kimi-for-coding", ProviderTTLSettings{SubscriptionMinutes: 2, OpenPlatformMinutes: 5, OtherMinutes: 0}, ProviderTypeSubscription, 2, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveCachePolicyFromConfig(tc.model, cfg, tc.ttlCfg)
			if got.ProviderType != tc.wantType {
				t.Fatalf("ProviderType = %q, want %q", got.ProviderType, tc.wantType)
			}
			if got.TTLMinutes != tc.wantTTL {
				t.Fatalf("TTLMinutes = %d, want %d", got.TTLMinutes, tc.wantTTL)
			}
			if got.IntervalMinutes != tc.wantInterval {
				t.Fatalf("IntervalMinutes = %d, want %d", got.IntervalMinutes, tc.wantInterval)
			}
		})
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
	mutateFile      string
	nextTurnNum     int
}

func newIdleTestHarness(model string) *idleTestHarness {
	limit := int64(200000)
	sess := nativeTestSession("s1", 150000, &limit)
	sess["agent_config"] = map[string]any{"model": model}
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
		nextTurnNum: 1,
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
			turnID := "turn-ka-" + string(rune('0'+h.nextTurnNum))
			userMsgID := "msg-ka-u-" + string(rune('0'+h.nextTurnNum))
			h.nextTurnNum++
			items := []NativeItem{
				{ItemID: userMsgID, Type: "user_message", Text: idleKeepAlivePrompt},
			}
			if h.emitToolCall {
				items = append(items, NativeItem{ItemID: "tool-1", Type: "tool_call", Name: "Bash"})
			}
			items = append(items, NativeItem{ItemID: "msg-ka-a", Type: "assistant_message", Text: "1"})
			h.turns = append(h.turns, NativeTurn{TurnID: turnID, Items: items})
			if h.mutateFile != "" {
				_ = os.WriteFile(h.mutateFile, []byte("changed\n"), 0o600)
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

	// 26m after user message, keepalive fires again.
	t4 := userAt.Add(26 * time.Minute)
	service.runIdleKeepAliveOnce(context.Background(), settings, "s1", t4)
	waitUntilIdleDone(t, service, "s1")
	h.mu.Lock()
	if len(h.prompts) != 3 {
		t.Fatalf("prompts after reset = %d, want 3", len(h.prompts))
	}
	h.mu.Unlock()
}

func TestIdleKeepAliveUntilOverrideStopsAtDeadline(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC().Truncate(time.Millisecond)
	keepAliveFixture{home: home, mainAge: 4*time.Minute + 20*time.Second, background: false}.write(t, "moonshot/kimi-k2")

	h := newIdleTestHarness("moonshot/kimi-k2")
	service := startIdleKeepAliveService(t, h, home, now.Add(-4*time.Minute-20*time.Second))

	// Global is OFF, per-session override is "until" +10m.
	settings := defaultAutomationSettings()
	settings.IdleKeepAlive.Enabled = false
	if err := service.storeAutomationSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	until := now.Add(6 * time.Minute)
	if _, err := service.setSessionKeepAlive(context.Background(), map[string]any{
		"id":      "s1",
		"mode":    IdleModeUntil,
		"untilAt": until.Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	// Reset AnchorAt to simulate the prior message 4m20s ago (within 5m open-platform TTL, >= 4m interval).
	rec, _ := service.loadSessionKeepAlive(context.Background(), "s1")
	rec.AnchorAt = now.Add(-4*time.Minute - 20*time.Second).Format(time.RFC3339Nano)
	_ = service.saveSessionKeepAlive(context.Background(), rec, now)

	// Before deadline -> fires.
	service.runIdleKeepAliveOnce(context.Background(), settings, "s1", now)
	waitUntilIdleDone(t, service, "s1")
	h.mu.Lock()
	if len(h.prompts) != 1 {
		t.Fatalf("prompts before until = %d, want 1", len(h.prompts))
	}
	h.mu.Unlock()

	// After deadline -> stops without sending.
	afterUntil := until.Add(1 * time.Minute)
	service.runIdleKeepAliveOnce(context.Background(), settings, "s1", afterUntil)
	h.mu.Lock()
	if len(h.prompts) != 1 {
		t.Fatalf("prompts after until = %d, want 1", len(h.prompts))
	}
	h.mu.Unlock()
	view, _ := service.sessionKeepAliveView(context.Background(), "s1", afterUntil)
	if view.StoppedReason != "until_reached" {
		t.Fatalf("StoppedReason = %q, want until_reached", view.StoppedReason)
	}
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
		if view.StoppedReason != "quota_low" || !strings.Contains(view.StatusLabel, "额度不足") {
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
		if view.StoppedReason != "undo_skipped" {
			t.Fatalf("StoppedReason = %q, want undo_skipped", view.StoppedReason)
		}
	})

	t.Run("skips undo when git workspace files changed", func(t *testing.T) {
		repo := t.TempDir()
		if err := exec.Command("git", "-C", repo, "init").Run(); err != nil {
			t.Skip("git unavailable")
		}
		file := filepath.Join(repo, "a.txt")
		_ = os.WriteFile(file, []byte("initial\n"), 0o600)
		_ = exec.Command("git", "-C", repo, "add", "a.txt").Run()
		_ = exec.Command("git", "-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-m", "init").Run()

		home := t.TempDir()
		now := time.Now().UTC().Truncate(time.Millisecond)
		keepAliveFixture{home: home, mainAge: 26 * time.Minute}.write(t, "kimi-code/kimi-for-coding")
		h := newIdleTestHarness("kimi-code/kimi-for-coding")
		h.session["metadata"] = map[string]any{"cwd": repo}
		h.mutateFile = file
		service := startIdleKeepAliveService(t, h, home, now.Add(-26*time.Minute))
		settings := defaultAutomationSettings()
		settings.IdleKeepAlive.Enabled = true
		_ = service.storeAutomationSettings(context.Background(), settings)

		service.runIdleKeepAliveOnce(context.Background(), settings, "s1", now)
		waitUntilIdleDone(t, service, "s1")

		h.mu.Lock()
		if len(h.undos) != 0 {
			t.Fatalf("undos = %d, want 0 when workspace files changed", len(h.undos))
		}
		h.mu.Unlock()
	})

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
		if view.StoppedReason != "undo_failed" || !strings.Contains(view.StatusLabel, "撤回失败") {
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
