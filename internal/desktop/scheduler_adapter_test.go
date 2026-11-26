package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"enhancedkimi/internal/quota"
)

func TestKAPArchiveAndAbortEndpoints(t *testing.T) {
	var mu sync.Mutex
	var hits []string
	var batchIDs []string
	v2NotFound := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.Method+" "+r.URL.Path)
		rejectV2 := v2NotFound
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":archive") && r.URL.Path == "/api/v2/sessions:archive":
			if rejectV2 {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":404,"message":"not found"}`))
				return
			}
			var body struct {
				IDs []string `json:"ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			batchIDs = body.IDs
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":0,"data":{"archived":true}}`))
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/sessions/") && strings.HasSuffix(r.URL.Path, ":archive"):
			_, _ = w.Write([]byte(`{"code":0,"data":{"archived":true}}`))
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/sessions/") && strings.HasSuffix(r.URL.Path, ":abort"):
			_, _ = w.Write([]byte(`{"code":0,"data":{"aborted":true}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	kap := NewKAPClient(t.TempDir())
	kap.base = server.URL
	kap.token = "test-token"

	if err := kap.ArchiveSession(context.Background(), "sess_a"); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}
	if err := kap.ArchiveSessions(context.Background(), []string{"sess_1", "sess_2", "sess_1"}); err != nil {
		t.Fatalf("ArchiveSessions v2: %v", err)
	}
	if err := kap.AbortSession(context.Background(), "sess_a"); err != nil {
		t.Fatalf("AbortSession: %v", err)
	}

	mu.Lock()
	if len(batchIDs) != 2 || batchIDs[0] != "sess_1" || batchIDs[1] != "sess_2" {
		t.Fatalf("expected deduped batch ids [sess_1 sess_2], got %v", batchIDs)
	}
	v2NotFound = true
	hits = nil
	mu.Unlock()

	// When /api/v2/sessions:archive is absent, ArchiveSessions falls back to v1 per-session :archive.
	if err := kap.ArchiveSessions(context.Background(), []string{"sess_x", "sess_y"}); err != nil {
		t.Fatalf("ArchiveSessions v1 fallback: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 3 || hits[0] != "POST /api/v2/sessions:archive" || hits[1] != "POST /api/v1/sessions/sess_x:archive" || hits[2] != "POST /api/v1/sessions/sess_y:archive" {
		t.Fatalf("unexpected fallback hits: %v", hits)
	}
}

func TestCodeUsageRemainingPercentSelectsLowestActiveWindow(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	future := now.Add(2 * time.Hour).Format(time.RFC3339)
	past := now.Add(-1 * time.Hour).Format(time.RFC3339)

	state := quota.CodeUsageState{
		Status: quota.CodeAvailable,
		Windows: []quota.CodeUsageWindow{
			{Key: "expired", Label: "已重置窗口", RemainingPercent: 0, ResetAt: &past},
			{Key: "5h", Label: "5 小时限额", RemainingPercent: 18, ResetAt: &future},
			{Key: "7d", Label: "每周限额", RemainingPercent: 62, ResetAt: &future},
		},
	}
	rem, known, label := codeUsageRemainingPercent(state, now)
	if !known || rem != 18 || label != "5 小时限额" {
		t.Fatalf("got rem=%v known=%v label=%q, want 18/true/5 小时限额", rem, known, label)
	}

	// Unavailable state must report unknown (never fabricate 0%).
	if _, known, _ := codeUsageRemainingPercent(quota.CodeUsageState{Status: quota.CodeUnavailable}, now); known {
		t.Fatal("unavailable code usage must report known=false")
	}
}

func TestSchedulerCheckAccountCustomVsManagedProvider(t *testing.T) {
	var mu sync.Mutex
	configBody := `{"providers":{"custom_openai":{"type":"openai","api_key":"sk-test"}}}`
	userInfoStatus := http.StatusOK
	userInfoBody := `{"kind":"error","status":401,"message":"No token for \"kimi-code\"."}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cfg := configBody
		st := userInfoStatus
		ui := userInfoBody
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/oauth/userinfo":
			w.WriteHeader(st)
			_, _ = w.Write([]byte(ui))
		case "/api/v1/config":
			_, _ = w.Write([]byte(cfg))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	kap := NewKAPClient(t.TempDir())
	kap.base = server.URL
	kap.token = "test-token"
	exec := &SchedulerExecutor{service: &NativeService{kap: kap}}

	// Custom provider without managed:kimi-code must not fail preflight on /oauth/userinfo 401.
	if ok, reason := exec.CheckAccount(context.Background()); !ok {
		t.Fatalf("expected custom provider setup to pass CheckAccount, got ok=%v reason=%q", ok, reason)
	}

	// Managed provider configured with 401 token must fail preflight.
	mu.Lock()
	configBody = `{"providers":{"managed:kimi-code":{"type":"kimi"}}}`
	mu.Unlock()
	if ok, reason := exec.CheckAccount(context.Background()); ok || !strings.Contains(reason, "kimi-code") {
		t.Fatalf("expected managed provider with 401 to fail CheckAccount, got ok=%v reason=%q", ok, reason)
	}

	// Logged-in managed account passes.
	mu.Lock()
	userInfoStatus = http.StatusOK
	userInfoBody = `{"userInfo":{"userId":"u_123","nickname":"Demo"}}`
	mu.Unlock()
	if ok, reason := exec.CheckAccount(context.Background()); !ok {
		t.Fatalf("expected logged-in managed account to pass CheckAccount, got ok=%v reason=%q", ok, reason)
	}
}

// TestSchedulerSendPromptReceiptAndTurnUserMessageIDs pins the attribution
// contract between the prompts ACK and the turns listing: the ACK's
// user_message_id equals the item_id of the turn's user_message item.
func TestSchedulerSendPromptReceiptAndTurnUserMessageIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/prompts"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["permission_mode"] != "manual" {
				t.Errorf("expected permission_mode manual, got %v", body["permission_mode"])
			}
			_, _ = w.Write([]byte(`{"prompt_id":"p1","user_message_id":"um_1","status":"accepted"}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/turns"):
			_, _ = w.Write([]byte(`{"turns":[` +
				`{"turn_id":"t1","ordinal":2,"items":[{"item_id":"um_1","type":"user_message","text":"hi"},{"item_id":"a1","type":"assistant_message","text":"ok"}]},` +
				`{"turn_id":"t2","ordinal":3,"items":[{"item_id":"um_2","type":"user_message","text":"go"}]}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	kap := NewKAPClient(t.TempDir())
	kap.base = server.URL
	kap.token = "test-token"
	exec := &SchedulerExecutor{service: &NativeService{kap: kap}}

	receipt, err := exec.SendPrompt(context.Background(), "s1", "hi", "", "", "manual")
	if err != nil {
		t.Fatalf("SendPrompt: %v", err)
	}
	if receipt.UserMessageID != "um_1" {
		t.Fatalf("expected the ACK user_message_id, got %q", receipt.UserMessageID)
	}

	turns, err := exec.GetSessionTurns(context.Background(), "s1", 20)
	if err != nil {
		t.Fatalf("GetSessionTurns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(turns))
	}
	if turns[0].TurnID != "t1" || !turns[0].HasAssistantMsg {
		t.Fatalf("unexpected first turn: %+v", turns[0])
	}
	if len(turns[0].UserMessageIDs) != 1 || turns[0].UserMessageIDs[0] != "um_1" {
		t.Fatalf("expected t1 to carry um_1, got %v", turns[0].UserMessageIDs)
	}
	if len(turns[1].UserMessageIDs) != 1 || turns[1].UserMessageIDs[0] != "um_2" {
		t.Fatalf("expected t2 to carry um_2, got %v", turns[1].UserMessageIDs)
	}
}
