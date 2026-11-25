package desktop

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"enhancedkimi/internal/credentials"
	"enhancedkimi/internal/storage"
)

type fakeAccountKAP struct {
	home              string
	mu                sync.Mutex
	tokens            map[string]AccountProfile // access_token -> profile
	sessions          []map[string]any
	models            map[string]any
	hasManaged        bool
	oauthKey          string
	baseURL           string
	oauthHost         string
	legacyHost        string
	failModels        bool
	failUser          int
	wrongUID          string
	rejectToken       string
	failConfigRestore bool
	failAfterPatch    bool
	configRestores    int
	rotateNext        map[string]string // access_token -> rotated access_token
	server            *httptest.Server
}

func newFakeAccountKAP(t *testing.T) *fakeAccountKAP {
	t.Helper()
	home := t.TempDir()
	f := &fakeAccountKAP{
		home:       home,
		tokens:     map[string]AccountProfile{},
		hasManaged: true,
		oauthKey:   defaultManagedOAuthKey,
		baseURL:    defaultManagedBaseURL,
		sessions: []map[string]any{
			{"id": "sess-1", "title": "主会话", "updated_at": time.Now().UTC().Format(time.RFC3339Nano), "busy": false, "main_turn_active": false, "pending_interaction": "none"},
			{"id": "sess-2", "title": "辅助会话", "updated_at": time.Now().UTC().Format(time.RFC3339Nano), "busy": false, "main_turn_active": false, "pending_interaction": "none"},
		},
		models: map[string]any{
			"managed:kimi-code/k2.8": map[string]any{"provider": "managed:kimi-code", "model": "k2.8"},
			"managed:kimi-code/k2.5": map[string]any{"provider": "managed:kimi-code", "model": "k2.5"},
		},
		rotateNext: map[string]string{},
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	// Write official instance discovery metadata so KAPClient.resolve() succeeds
	// even if the background event stream invalidates and re-resolves.
	instDir := filepath.Join(home, "server", "instances")
	if err := os.MkdirAll(instDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hostPort := strings.TrimPrefix(f.server.URL, "http://")
	parts := strings.Split(hostPort, ":")
	port := 0
	if len(parts) == 2 {
		for _, ch := range parts[1] {
			port = port*10 + int(ch-'0')
		}
	}
	instBytes, _ := json.Marshal(map[string]any{"host": "127.0.0.1", "port": port, "heartbeat_at": time.Now().Unix()})
	if err := os.WriteFile(filepath.Join(instDir, "test.json"), instBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "server.token"), []byte("test-server-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fakeAccountKAP) credPath() string {
	return f.credPathFor(f.oauthKey)
}

func (f *fakeAccountKAP) credPathFor(oauthKey string) string {
	return filepath.Join(f.home, "credentials", sanitizeOAuthKey(oauthKey)+".json")
}

func (f *fakeAccountKAP) writeDiskToken(t *testing.T, access, refresh string, expiresAt int64) {
	t.Helper()
	if err := f.writeDiskTokenFor(f.oauthKey, access, refresh, expiresAt); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeAccountKAP) writeDiskTokenFor(oauthKey, access, refresh string, expiresAt int64) error {
	path := f.credPathFor(oauthKey)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(map[string]any{
		"access_token":  access,
		"refresh_token": refresh,
		"expires_at":    expiresAt,
		"expires_in":    3600,
		"token_type":    "Bearer",
		"scope":         "kimi-code",
	})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (f *fakeAccountKAP) readDiskToken(t *testing.T) rawOAuthTokenFields {
	t.Helper()
	return f.readDiskTokenFor(t, f.oauthKey)
}

func (f *fakeAccountKAP) readDiskTokenFor(t *testing.T, oauthKey string) rawOAuthTokenFields {
	t.Helper()
	data, err := os.ReadFile(f.credPathFor(oauthKey))
	if err != nil {
		t.Fatalf("read disk token for %s: %v", oauthKey, err)
	}
	var out rawOAuthTokenFields
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal disk token for %s: %v", oauthKey, err)
	}
	return out
}

func (f *fakeAccountKAP) handle(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, NativeAPI)
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case path == "/config" && r.Method == http.MethodGet:
		if f.failModels {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 500, "msg": "config unavailable"})
			return
		}
		providers := map[string]any{}
		if f.hasManaged {
			oauth := map[string]any{
				"storage": "file",
				"key":     "oauth/" + sanitizeOAuthKey(f.oauthKey),
			}
			if f.oauthHost != "" {
				oauth["oauthHost"] = f.oauthHost
			}
			if f.legacyHost != "" {
				oauth["host"] = f.legacyHost
				oauth["oauth_host"] = f.legacyHost
			}
			providers["managed:kimi-code"] = map[string]any{
				"type":    "kimi",
				"baseUrl": f.baseURL,
				"apiKey":  "",
				"oauth":   oauth,
			}
		}
		writeNativeTestEnvelope(w, map[string]any{
			"providers":     providers,
			"models":        f.models,
			"default_model": "managed:kimi-code/k2.8",
		})
	case path == "/config" && r.Method == http.MethodPost:
		var patch map[string]any
		_ = json.NewDecoder(r.Body).Decode(&patch)
		if providers, ok := patch["providers"].(map[string]any); ok {
			if val, present := providers["managed:kimi-code"]; present {
				if val == nil {
					f.configRestores++
					if f.failConfigRestore {
						w.WriteHeader(http.StatusInternalServerError)
						_ = json.NewEncoder(w).Encode(map[string]any{"code": 500, "msg": "config restore rejected"})
						return
					}
				} else if provider, ok := val.(map[string]any); ok {
					if baseURL, present := provider["baseUrl"].(string); present {
						f.baseURL = baseURL
					}
					if oauth, ok := provider["oauth"].(map[string]any); ok {
						if key, ok := oauth["key"].(string); ok && strings.TrimSpace(key) != "" {
							f.oauthKey = sanitizeOAuthKey(key)
						}
						f.oauthHost, _ = oauth["oauthHost"].(string)
						f.legacyHost, _ = oauth["host"].(string)
						if f.legacyHost == "" {
							f.legacyHost, _ = oauth["oauth_host"].(string)
						}
					}
				}
				f.hasManaged = val != nil
				if val != nil && f.failAfterPatch {
					w.WriteHeader(http.StatusInternalServerError)
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 500, "msg": "config patch applied before error"})
					return
				}
			}
		}
		writeNativeTestEnvelope(w, map[string]any{"ok": true})
	case path == "/sessions" && r.Method == http.MethodGet:
		writeNativeTestEnvelope(w, map[string]any{"items": f.sessions})
	case path == "/oauth/userinfo" && r.Method == http.MethodGet:
		if f.failUser != 0 {
			w.WriteHeader(f.failUser)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": f.failUser, "msg": "unauthorized"})
			return
		}
		if !f.hasManaged {
			writeNativeTestEnvelope(w, map[string]any{"kind": "error", "status": 401, "message": "unauthenticated"})
			return
		}
		raw, err := os.ReadFile(f.credPath())
		if err != nil {
			writeNativeTestEnvelope(w, map[string]any{"kind": "error", "status": 401, "message": "missing token"})
			return
		}
		var tok rawOAuthTokenFields
		if json.Unmarshal(raw, &tok) != nil || tok.AccessToken == "" {
			writeNativeTestEnvelope(w, map[string]any{"kind": "error", "status": 401, "message": "invalid token"})
			return
		}
		if f.rejectToken != "" && tok.AccessToken == f.rejectToken {
			writeNativeTestEnvelope(w, map[string]any{"kind": "error", "status": 401, "message": "token rejected"})
			return
		}
		if rotated, ok := f.rotateNext[tok.AccessToken]; ok {
			delete(f.rotateNext, tok.AccessToken)
			if prof, exists := f.tokens[tok.AccessToken]; exists {
				f.tokens[rotated] = prof
			}
			tok.AccessToken = rotated
			tok.RefreshToken = "refresh-" + rotated
			if encoded, err := json.Marshal(tok); err == nil {
				_ = os.WriteFile(f.credPath(), encoded, 0o600)
			}
		}
		prof, ok := f.tokens[tok.AccessToken]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 401, "msg": "token rejected"})
			return
		}
		uid := prof.UserID
		if f.wrongUID != "" {
			uid = f.wrongUID
		}
		writeNativeTestEnvelope(w, map[string]any{
			"kind": "ok",
			"userInfo": map[string]any{
				"userId":        uid,
				"nickname":      prof.Nickname,
				"avatar":        prof.Avatar,
				"userLevelName": prof.Plan,
				"region":        prof.Region,
			},
		})
	default:
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 404, "msg": "not found"})
	}
}

type fakeSchedulerPauser struct {
	paused atomic.Int32
	calls  atomic.Int32
}

func (p *fakeSchedulerPauser) SchedulerAction(context.Context, string, string, json.RawMessage) (any, error) {
	return map[string]any{}, nil
}

func (p *fakeSchedulerPauser) Pause() func() {
	p.paused.Add(1)
	p.calls.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			p.paused.Add(-1)
		})
	}
}

func newAccountTestService(t *testing.T) (*NativeService, *fakeAccountKAP, *credentials.Vault) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	vault, err := credentials.NewVault(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.Unlock(context.Background(), "test-passphrase-1234"); err != nil {
		t.Fatal(err)
	}

	fake := newFakeAccountKAP(t)
	kap := NewKAPClient(fake.home)
	kap.base = fake.server.URL
	kap.token = "test-server-token"
	kap.resolved = true

	service, err := NewNativeService(db, kap, t.TempDir(), func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	service.UseVault(vault)
	service.cliDetector = func(context.Context) bool { return false }
	service.hostPIDProvider = func() int { return 4242 }
	return service, fake, vault
}

func saveTwoTestAccounts(t *testing.T, service *NativeService, fake *fakeAccountKAP) (AccountSummary, AccountSummary) {
	t.Helper()
	ctx := context.Background()
	exp := time.Now().Add(time.Hour).Unix()

	fake.mu.Lock()
	fake.tokens["tok-acc-a-secret-1"] = AccountProfile{UserID: "uid-a", Nickname: "账号A", Plan: "Moderato", Region: "cn"}
	fake.tokens["tok-acc-b-secret-1"] = AccountProfile{UserID: "uid-b", Nickname: "账号B", Plan: "Free", Region: "cn"}
	fake.mu.Unlock()

	fake.writeDiskToken(t, "tok-acc-a-secret-1", "ref-acc-a-secret-1", exp)
	rawA, err := service.Action(ctx, "accounts.saveCurrent", json.RawMessage(`{"label":"主号A"}`))
	if err != nil {
		t.Fatalf("save A: %v", err)
	}
	accA := rawA.(AccountSummary)

	fake.writeDiskToken(t, "tok-acc-b-secret-1", "ref-acc-b-secret-1", exp)
	rawB, err := service.Action(ctx, "accounts.saveCurrent", json.RawMessage(`{"label":"备号B"}`))
	if err != nil {
		t.Fatalf("save B: %v", err)
	}
	accB := rawB.(AccountSummary)

	// Restore A as currently active on disk.
	fake.writeDiskToken(t, "tok-acc-a-secret-1", "ref-acc-a-secret-1", exp)
	return accA, accB
}

func TestAccountSwitchRejectsWhenBusyAndQueuesWaitUntilIdle(t *testing.T) {
	service, fake, _ := newAccountTestService(t)
	_, accB := saveTwoTestAccounts(t, service, fake)
	ctx := context.Background()

	// Mark sess-1 busy.
	fake.mu.Lock()
	fake.sessions[0]["busy"] = true
	fake.sessions[0]["main_turn_active"] = true
	fake.mu.Unlock()

	payload, _ := json.Marshal(map[string]any{"id": accB.ID})
	_, err := service.Action(ctx, "accounts.switch", payload)
	if err == nil {
		t.Fatal("expected busy rejection")
	}
	var nativeErr *NativeError
	if !errors.As(err, &nativeErr) || nativeErr.Code != "ACCOUNT_SWITCH_BUSY" {
		t.Fatalf("expected ACCOUNT_SWITCH_BUSY, got %v", err)
	}
	if !strings.Contains(err.Error(), "主会话") {
		t.Fatalf("expected busy session title in error, got %q", err.Error())
	}
	if got := fake.readDiskToken(t).AccessToken; got != "tok-acc-a-secret-1" {
		t.Fatalf("disk token changed despite busy rejection: %s", got)
	}

	// Test pending interaction rejection as well.
	fake.mu.Lock()
	fake.sessions[0]["busy"] = false
	fake.sessions[0]["main_turn_active"] = false
	fake.sessions[0]["pending_interaction"] = "approval"
	fake.mu.Unlock()
	if _, err := service.Action(ctx, "accounts.switch", payload); err == nil {
		t.Fatal("expected rejection when pending interaction exists")
	}

	// Queue waitUntilIdle and verify automatic switch once idle.
	queuePayload, _ := json.Marshal(map[string]any{"id": accB.ID, "waitUntilIdle": true})
	rawQueued, err := service.Action(ctx, "accounts.switch", queuePayload)
	if err != nil {
		t.Fatalf("queue waitUntilIdle: %v", err)
	}
	queued := rawQueued.(AccountSwitchResult)
	if !queued.Queued || queued.PendingSwitch == nil || queued.PendingSwitch.TargetID != accB.ID {
		t.Fatalf("unexpected queued result: %+v", queued)
	}

	fake.mu.Lock()
	fake.sessions[0]["pending_interaction"] = "none"
	fake.mu.Unlock()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fake.readDiskToken(t).AccessToken == "tok-acc-b-secret-1" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("queued switch did not execute after session became idle; disk token=%s", fake.readDiskToken(t).AccessToken)
}

func TestAccountSwitchRollsBackOnWriteAndVerificationFailures(t *testing.T) {
	ctx := context.Background()

	t.Run("write failure rolls back", func(t *testing.T) {
		service, fake, _ := newAccountTestService(t)
		_, accB := saveTwoTestAccounts(t, service, fake)
		service.writeAccountFileHook = func(string, []byte) error {
			return errors.New("simulated disk full")
		}
		payload, _ := json.Marshal(map[string]any{"id": accB.ID})
		if _, err := service.Action(ctx, "accounts.switch", payload); err == nil {
			t.Fatal("expected write failure")
		}
		if got := fake.readDiskToken(t).AccessToken; got != "tok-acc-a-secret-1" {
			t.Fatalf("disk token not preserved on write failure: %s", got)
		}
	})

	t.Run("userinfo 401 rolls back and marks relogin_required", func(t *testing.T) {
		service, fake, _ := newAccountTestService(t)
		_, accB := saveTwoTestAccounts(t, service, fake)
		// Remove B from valid server tokens so /oauth/userinfo returns 401 after writing B.
		fake.mu.Lock()
		delete(fake.tokens, "tok-acc-b-secret-1")
		fake.mu.Unlock()

		payload, _ := json.Marshal(map[string]any{"id": accB.ID})
		_, err := service.Action(ctx, "accounts.switch", payload)
		if err == nil {
			t.Fatal("expected verification failure")
		}
		if got := fake.readDiskToken(t).AccessToken; got != "tok-acc-a-secret-1" {
			t.Fatalf("disk token not rolled back after 401: %s", got)
		}
		row, err := service.loadAccountRow(ctx, accB.ID)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != accountStatusReloginRequired {
			t.Fatalf("expected B marked relogin_required, got %q", row.Status)
		}
	})

	t.Run("models failure rolls back", func(t *testing.T) {
		service, fake, _ := newAccountTestService(t)
		_, accB := saveTwoTestAccounts(t, service, fake)
		service.writeAccountFileHook = func(target string, data []byte) error {
			if err := writeFileAtomic0600(target, data); err != nil {
				return err
			}
			fake.mu.Lock()
			fake.failModels = true
			fake.mu.Unlock()
			return nil
		}
		payload, _ := json.Marshal(map[string]any{"id": accB.ID})
		if _, err := service.Action(ctx, "accounts.switch", payload); err == nil {
			t.Fatal("expected models failure")
		}
		if got := fake.readDiskToken(t).AccessToken; got != "tok-acc-a-secret-1" {
			t.Fatalf("disk token not rolled back after models failure: %s", got)
		}
	})

	t.Run("session loss rolls back", func(t *testing.T) {
		service, fake, _ := newAccountTestService(t)
		_, accB := saveTwoTestAccounts(t, service, fake)
		service.writeAccountFileHook = func(target string, data []byte) error {
			if err := writeFileAtomic0600(target, data); err != nil {
				return err
			}
			fake.mu.Lock()
			fake.sessions = fake.sessions[:1]
			fake.mu.Unlock()
			return nil
		}
		payload, _ := json.Marshal(map[string]any{"id": accB.ID})
		if _, err := service.Action(ctx, "accounts.switch", payload); err == nil {
			t.Fatal("expected session drop rollback")
		}
		if got := fake.readDiskToken(t).AccessToken; got != "tok-acc-a-secret-1" {
			t.Fatalf("disk token not rolled back after session loss: %s", got)
		}
	})

	t.Run("host PID change rolls back", func(t *testing.T) {
		service, fake, _ := newAccountTestService(t)
		_, accB := saveTwoTestAccounts(t, service, fake)
		pidCalls := 0
		service.hostPIDProvider = func() int {
			pidCalls++
			if pidCalls == 1 {
				return 4242
			}
			return 9999
		}
		payload, _ := json.Marshal(map[string]any{"id": accB.ID})
		if _, err := service.Action(ctx, "accounts.switch", payload); err == nil {
			t.Fatal("expected PID change rollback")
		}
		if got := fake.readDiskToken(t).AccessToken; got != "tok-acc-a-secret-1" {
			t.Fatalf("disk token not rolled back after PID change: %s", got)
		}
	})
}

func TestAccountSwitchRollbackBackup(t *testing.T) {
	for _, tc := range []struct {
		name              string
		failCredential    bool
		failConfig        bool
		failAfterPatch    bool
		missingCredential bool
	}{
		{name: "credential restore fails", failCredential: true},
		{name: "config restore fails", failConfig: true},
		{name: "both restores fail", failCredential: true, failConfig: true},
		{name: "credential was absent", failCredential: true, missingCredential: true},
		{name: "both restores succeed"},
		{name: "patch applied before error", failAfterPatch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, fake, _ := newAccountTestService(t)
			_, target := saveTwoTestAccounts(t, service, fake)
			credPath := fake.credPath()
			backupPath := credPath + ".ekd-bak"
			original, err := os.ReadFile(credPath)
			if err != nil {
				t.Fatal(err)
			}
			if tc.missingCredential {
				if err := os.Remove(credPath); err != nil {
					t.Fatal(err)
				}
			}
			fake.mu.Lock()
			fake.hasManaged = false
			fake.failConfigRestore = tc.failConfig
			fake.failAfterPatch = tc.failAfterPatch
			fake.mu.Unlock()

			service.hostPIDProvider = func() int {
				if tc.failCredential {
					if err := os.Remove(credPath); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(credPath, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(credPath, "block-restore"), nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return 9999
			}
			payload, _ := json.Marshal(map[string]any{"id": target.ID, "expectedPid": 4242})
			_, err = service.Action(context.Background(), "accounts.switch", payload)
			if err == nil {
				t.Fatal("expected switch to fail")
			}
			trigger := "客户端进程号发生变化"
			if tc.failAfterPatch {
				trigger = "config patch applied before error"
			}
			message := err.Error()
			if !strings.Contains(message, trigger) ||
				strings.Contains(message, "恢复凭据文件失败") != tc.failCredential ||
				strings.Contains(message, "恢复官方供应商配置失败") != tc.failConfig {
				t.Fatalf("switch error = %q", message)
			}
			fake.mu.Lock()
			managed, restores := fake.hasManaged, fake.configRestores
			fake.mu.Unlock()
			if managed != tc.failConfig || restores != 1 {
				t.Fatalf("provider present = %t, restore attempts = %d", managed, restores)
			}
			if !tc.failCredential {
				restored, err := os.ReadFile(credPath)
				if err != nil || !bytes.Equal(restored, original) {
					t.Fatalf("original credential was not restored: %v", err)
				}
			}
			backup, backupErr := os.ReadFile(backupPath)
			if !tc.failCredential && !tc.failConfig {
				if !errors.Is(backupErr, os.ErrNotExist) || !strings.Contains(message, "已自动恢复原账号") {
					t.Fatalf("successful rollback error = %q, backup read error = %v", message, backupErr)
				}
				return
			}
			if backupErr != nil || !strings.Contains(message, backupPath) || strings.Contains(message, "已自动恢复原账号") {
				t.Fatalf("incomplete rollback error = %q, backup read error = %v", message, backupErr)
			}
			if tc.missingCredential {
				if !bytes.Contains(backup, []byte("no credential file existed")) {
					t.Fatal("backup does not record the absence of an original credential")
				}
			} else if !bytes.Equal(backup, original) {
				t.Fatal("backup does not contain the original credential")
			}
			if _, err := service.Action(context.Background(), "accounts.switch", payload); err == nil || !strings.Contains(err.Error(), backupPath) {
				t.Fatalf("retry should stop with backup path, got %v", err)
			}
			if afterRetry, err := os.ReadFile(backupPath); err != nil || !bytes.Equal(afterRetry, backup) {
				t.Fatalf("retry altered the retained backup: %v", err)
			}
		})
	}
}

func TestAccountSwitchReportsBackupCleanupFailure(t *testing.T) {
	service, fake, _ := newAccountTestService(t)
	_, target := saveTwoTestAccounts(t, service, fake)
	backupPath := fake.credPath() + ".ekd-bak"
	service.hostPIDProvider = func() int {
		backup, err := os.ReadFile(backupPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(backupPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(backupPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(backupPath, "backup"), backup, 0o600); err != nil {
			t.Fatal(err)
		}
		return 4242
	}
	payload, _ := json.Marshal(map[string]any{"id": target.ID, "expectedPid": 4242})
	value, err := service.Action(context.Background(), "accounts.switch", payload)
	if err != nil {
		t.Fatal(err)
	}
	result := value.(AccountSwitchResult)
	if !result.Switched || !strings.Contains(result.BackupCleanupWarning, backupPath) {
		t.Fatalf("switch must succeed with the backup cleanup warning: %+v", result)
	}
}

func TestAccountSwitchResnapshotsRotatedTokenBeforeLeaving(t *testing.T) {
	service, fake, _ := newAccountTestService(t)
	accA, accB := saveTwoTestAccounts(t, service, fake)
	ctx := context.Background()
	exp := time.Now().Add(2 * time.Hour).Unix()

	// Simulate official OAuth background refresh rotating A's tokens on disk.
	fake.mu.Lock()
	fake.tokens["tok-acc-a-rotated-v2"] = AccountProfile{UserID: "uid-a", Nickname: "账号A", Plan: "Moderato", Region: "cn"}
	fake.mu.Unlock()
	fake.writeDiskToken(t, "tok-acc-a-rotated-v2", "ref-acc-a-rotated-v2", exp)

	// Switch A -> B: must re-snapshot A's v2 token before writing B.
	toB, _ := json.Marshal(map[string]any{"id": accB.ID})
	rawRes, err := service.Action(ctx, "accounts.switch", toB)
	if err != nil {
		t.Fatalf("switch A->B: %v", err)
	}
	res := rawRes.(AccountSwitchResult)
	if !res.Switched || res.Account == nil || res.Account.UserID != "uid-b" || res.HostPID != 4242 {
		t.Fatalf("unexpected switch result: %+v", res)
	}
	if got := fake.readDiskToken(t).AccessToken; got != "tok-acc-b-secret-1" {
		t.Fatalf("expected B token on disk, got %s", got)
	}
	if _, err := os.Lstat(fake.credPath() + ".ekd-bak"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery backup still present after successful switch: %v", err)
	}

	// Switch B -> A: disk must receive A's rotated v2 token (not stale v1!).
	toA, _ := json.Marshal(map[string]any{"id": accA.ID})
	if _, err := service.Action(ctx, "accounts.switch", toA); err != nil {
		t.Fatalf("switch B->A: %v", err)
	}
	gotA := fake.readDiskToken(t)
	if gotA.AccessToken != "tok-acc-a-rotated-v2" || gotA.RefreshToken != "ref-acc-a-rotated-v2" {
		t.Fatalf("expected re-snapshotted v2 token for A, got %+v", gotA)
	}
}

func TestAccountPayloadsNeverLeakTokensAndVaultIsolated(t *testing.T) {
	service, fake, vault := newAccountTestService(t)
	_, accB := saveTwoTestAccounts(t, service, fake)
	ctx := context.Background()

	// Plugin vault HTTP listing and Vault.Get must NOT expose kimi account refs.
	if _, getErr := vault.Get(ctx, accountVaultRef(accB.ID)); !errors.Is(getErr, credentials.ErrNotFound) {
		t.Fatalf("plugin vault.Get unexpectedly returned account secret: %v", getErr)
	}
	mux := http.NewServeMux()
	vault.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/enhance/v1/credentials", nil))
	if strings.Contains(rec.Body.String(), "kimi-code-account") {
		t.Fatalf("GET /enhance/v1/credentials exposed account ref: %s", rec.Body.String())
	}

	// Set up mock codeusage endpoint for non-current account B.
	usageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-acc-b-secret-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"usages": map[string]any{
				"limit_5h": map[string]any{"used_ratio": 0.35},
				"limit_7d": map[string]any{"used_ratio": 0.80},
			},
		})
	}))
	t.Cleanup(usageServer.Close)
	service.codeUsageEndpointOverride = usageServer.URL

	secrets := []string{"tok-acc-a-secret-1", "ref-acc-a-secret-1", "tok-acc-b-secret-1", "ref-acc-b-secret-1"}
	checkNoLeak := func(label string, v any) {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s marshal: %v", label, err)
		}
		text := string(raw)
		for _, s := range secrets {
			if strings.Contains(text, s) {
				t.Fatalf("%s leaked secret %q in payload: %s", label, s, text)
			}
		}
	}

	listVal, err := service.Action(ctx, "accounts.list", json.RawMessage(`{"refreshQuota":true}`))
	if err != nil {
		t.Fatalf("accounts.list: %v", err)
	}
	checkNoLeak("accounts.list", listVal)
	state := listVal.(AccountsState)
	if len(state.Accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(state.Accounts))
	}
	var foundB *AccountSummary
	for i := range state.Accounts {
		if state.Accounts[i].ID == accB.ID {
			foundB = &state.Accounts[i]
		}
	}
	if foundB == nil || foundB.Quota == nil || foundB.Quota.Status != "ok" {
		t.Fatalf("expected B quota ok, got %+v", foundB)
	}
	if foundB.Quota.Limit5hUsed == nil || *foundB.Quota.Limit5hUsed != 35 {
		t.Fatalf("expected B 5h=35, got %+v", foundB.Quota)
	}
	if foundB.Quota.Limit7dUsed == nil || *foundB.Quota.Limit7dUsed != 80 {
		t.Fatalf("expected B 7d=80, got %+v", foundB.Quota)
	}

	prePayload, _ := json.Marshal(map[string]any{"id": accB.ID, "activeModel": "managed:kimi-code/k2.8"})
	preVal, err := service.Action(ctx, "accounts.switchPreflight", prePayload)
	if err != nil {
		t.Fatalf("switchPreflight: %v", err)
	}
	checkNoLeak("accounts.switchPreflight", preVal)

	switchPayload, _ := json.Marshal(map[string]any{"id": accB.ID})
	swVal, err := service.Action(ctx, "accounts.switch", switchPayload)
	if err != nil {
		t.Fatalf("accounts.switch: %v", err)
	}
	checkNoLeak("accounts.switch", swVal)
}

func TestAccountSwitchPausesSchedulerAndDetectsCLIAndUnknownQuota(t *testing.T) {
	service, fake, _ := newAccountTestService(t)
	_, accB := saveTwoTestAccounts(t, service, fake)
	ctx := context.Background()

	pauser := &fakeSchedulerPauser{}
	service.UseScheduler(pauser)
	service.cliDetector = func(context.Context) bool { return true }

	// Upstream quota error must report "unknown", never 0%.
	errServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(errServer.Close)
	service.codeUsageEndpointOverride = errServer.URL

	quotasRaw, err := service.Action(ctx, "accounts.refreshQuotas", nil)
	if err != nil {
		t.Fatalf("refreshQuotas: %v", err)
	}
	quotas := quotasRaw.(map[string]AccountQuotaSummary)
	if q := quotas[accB.ID]; q.Status != "unknown" || q.Limit5hUsed != nil || q.Limit7dUsed != nil {
		t.Fatalf("expected unknown quota without fabricated 0, got %+v", q)
	}

	// Preflight should warn about cache miss and running kimi CLI.
	prePayload, _ := json.Marshal(map[string]any{"id": accB.ID})
	preRaw, err := service.Action(ctx, "accounts.switchPreflight", prePayload)
	if err != nil {
		t.Fatalf("switchPreflight: %v", err)
	}
	pre := preRaw.(AccountSwitchPreflight)
	if !pre.CLIRunning || len(pre.Warnings) < 2 {
		t.Fatalf("expected CLI warning in preflight, got %+v", pre)
	}

	// Verify scheduler + automation paused during credential write and resumed after.
	pausedDuringWrite := false
	autoPausedDuringWrite := false
	service.writeAccountFileHook = func(target string, data []byte) error {
		pausedDuringWrite = pauser.paused.Load() > 0
		autoPausedDuringWrite = service.isAutomationPausedForSwitch()
		return writeFileAtomic0600(target, data)
	}
	swPayload, _ := json.Marshal(map[string]any{"id": accB.ID})
	if _, err := service.Action(ctx, "accounts.switch", swPayload); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if !pausedDuringWrite || !autoPausedDuringWrite {
		t.Fatalf("expected scheduler and automation paused during switch (sched=%v, auto=%v)", pausedDuringWrite, autoPausedDuringWrite)
	}
	if pauser.paused.Load() != 0 || service.isAutomationPausedForSwitch() {
		t.Fatalf("expected scheduler and automation resumed after switch")
	}
}

func savedAccountSecret(t *testing.T, ctx context.Context, vault *credentials.Vault, accountID string) []byte {
	t.Helper()
	raw, err := vault.KimiAccountStore().Get(ctx, accountVaultRef(accountID))
	if err != nil {
		t.Fatalf("read saved secret for %s: %v", accountID, err)
	}
	return raw
}

func accountRowForTest(t *testing.T, ctx context.Context, service *NativeService, userID string) accountRow {
	t.Helper()
	row, err := service.loadAccountRowByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("load account row for %s: %v", userID, err)
	}
	return row
}

// saveAccountUnderOAuthKey stores one account while the official provider points
// at oauthKey, so the saved record and its credential file carry that key.
func saveAccountUnderOAuthKey(t *testing.T, service *NativeService, fake *fakeAccountKAP, oauthKey, accessToken, refreshToken, label string) AccountSummary {
	t.Helper()
	ctx := context.Background()
	fake.mu.Lock()
	fake.oauthKey = oauthKey
	fake.tokens[accessToken] = AccountProfile{UserID: "uid-" + oauthKey, Nickname: label, Plan: "Free", Region: "cn"}
	fake.mu.Unlock()
	if err := fake.writeDiskTokenFor(oauthKey, accessToken, refreshToken, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"label": label})
	raw, err := service.Action(ctx, "accounts.saveCurrent", payload)
	if err != nil {
		t.Fatalf("save account for %s: %v", oauthKey, err)
	}
	return raw.(AccountSummary)
}

func TestAccountSwitchKeepsOAuthKeysIsolated(t *testing.T) {
	ctx := context.Background()
	staleToken := "tok-stale-target"

	for _, tc := range []struct {
		name       string
		rollback   bool
		wantErrKey string
	}{
		{name: "switches to a different OAuth key"},
		{name: "rolls back a different OAuth key switch", rollback: true, wantErrKey: "客户端进程号发生变化"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, fake, vault := newAccountTestService(t)
			accA := saveAccountUnderOAuthKey(t, service, fake, "account-a", "tok-a-v1", "ref-a-v1", "主号A")
			accB := saveAccountUnderOAuthKey(t, service, fake, "account-b", "tok-b-v1", "ref-b-v1", "备号B")
			if _, err := service.db.Exec(`UPDATE kimi_accounts SET base_url=?,oauth_host=? WHERE id=?`, "https://old.example/v1", "https://old-oauth.example", accA.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := service.db.Exec(`UPDATE kimi_accounts SET base_url=?,oauth_host='' WHERE id=?`, "https://target.example/v1", accB.ID); err != nil {
				t.Fatal(err)
			}
			var targetSecret storedKimiAccountSecret
			if err := json.Unmarshal(savedAccountSecret(t, ctx, vault, accB.ID), &targetSecret); err != nil {
				t.Fatal(err)
			}
			targetSecret.BaseURL = "https://target.example/v1"
			targetSecret.OAuthHost = ""
			targetSecretJSON, err := json.Marshal(targetSecret)
			if err != nil {
				t.Fatal(err)
			}
			if err := vault.KimiAccountStore().Put(ctx, accountVaultRef(accB.ID), targetSecretJSON); err != nil {
				t.Fatal(err)
			}
			fake.mu.Lock()
			fake.baseURL = "https://old.example/v1"
			fake.oauthHost = "https://old-oauth.example"
			fake.legacyHost = "https://old-oauth.example"
			fake.mu.Unlock()

			// The target path already holds a resolvable token for another account.
			fake.mu.Lock()
			fake.tokens[staleToken] = AccountProfile{UserID: "uid-stale", Nickname: "旧账号"}
			fake.mu.Unlock()
			if err := fake.writeDiskTokenFor("account-b", staleToken, "ref-stale", time.Now().Add(time.Hour).Unix()); err != nil {
				t.Fatal(err)
			}
			// Restore A as the account the official client currently uses, with a token
			// rotated after A was saved: a re-snapshot may update A's record, and a
			// rollback must put the stored record back exactly.
			fake.mu.Lock()
			fake.oauthKey = "account-a"
			fake.tokens["tok-a-v2"] = AccountProfile{UserID: "uid-account-a", Nickname: "主号A", Plan: "Free", Region: "cn"}
			fake.mu.Unlock()
			if err := fake.writeDiskTokenFor("account-a", "tok-a-v2", "ref-a-v2", time.Now().Add(time.Hour).Unix()); err != nil {
				t.Fatal(err)
			}
			targetBytes, err := os.ReadFile(fake.credPathFor("account-b"))
			if err != nil {
				t.Fatal(err)
			}
			currentBytes, err := os.ReadFile(fake.credPathFor("account-a"))
			if err != nil {
				t.Fatal(err)
			}
			secretBefore := savedAccountSecret(t, ctx, vault, accA.ID)
			if !bytes.Contains(secretBefore, []byte("tok-a-v1")) {
				t.Fatalf("saved account A record does not start from the pre-rotation token: %s", secretBefore)
			}
			rowBefore := accountRowForTest(t, ctx, service, accA.UserID)

			if tc.rollback {
				service.hostPIDProvider = func() int { return 9999 }
			}
			payload, _ := json.Marshal(map[string]any{"id": accB.ID, "expectedPid": 4242})
			_, switchErr := service.Action(ctx, "accounts.switch", payload)
			if tc.rollback {
				if switchErr == nil || !strings.Contains(switchErr.Error(), tc.wantErrKey) || !strings.Contains(switchErr.Error(), "已自动恢复原账号") {
					t.Fatalf("switch error = %v", switchErr)
				}
				if after := savedAccountSecret(t, ctx, vault, accA.ID); !bytes.Equal(after, secretBefore) {
					t.Fatalf("outgoing account record was not restored:\nbefore=%s\nafter=%s", secretBefore, after)
				}
				if after := accountRowForTest(t, ctx, service, accA.UserID); after != rowBefore {
					t.Fatalf("outgoing account row was not restored:\nbefore=%+v\nafter=%+v", rowBefore, after)
				}
				if after, err := os.ReadFile(fake.credPathFor("account-b")); err != nil || !bytes.Equal(after, targetBytes) {
					t.Fatalf("target credential file was not restored: %v", err)
				}
				if after, err := os.ReadFile(fake.credPathFor("account-a")); err != nil || !bytes.Equal(after, currentBytes) {
					t.Fatalf("current credential file changed during rollback: %v", err)
				}
				if key, host, baseURL, exists, cfgErr := service.readManagedProviderConfig(ctx); cfgErr != nil || !exists || key != "account-a" || host != "https://old-oauth.example" || baseURL != "https://old.example/v1" {
					t.Fatalf("provider config after rollback = key %q host %q base %q (exists=%t, err=%v)", key, host, baseURL, exists, cfgErr)
				}
				if _, err := os.Lstat(fake.credPathFor("account-b") + ".ekd-bak"); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("recovery backup still present after complete rollback: %v", err)
				}
				return
			}
			if switchErr != nil {
				t.Fatalf("switch: %v", switchErr)
			}
			// The stale token that lived at account-b.json must never reach A's record,
			// and A's rotated disk token must reach it only through A's own record.
			if after := savedAccountSecret(t, ctx, vault, accA.ID); bytes.Contains(after, []byte(staleToken)) {
				t.Fatalf("outgoing account record received the target path's stale token")
			}
			if after := savedAccountSecret(t, ctx, vault, accA.ID); !bytes.Contains(after, []byte("tok-a-v2")) {
				t.Fatalf("outgoing account record was not re-snapshotted from its own credential file: %s", after)
			}
			if after, err := os.ReadFile(fake.credPathFor("account-a")); err != nil || !bytes.Equal(after, currentBytes) {
				t.Fatalf("current credential file changed: %v", err)
			}
			if got := fake.readDiskTokenFor(t, "account-b").AccessToken; got != "tok-b-v1" {
				t.Fatalf("target file token = %q, want tok-b-v1", got)
			}
			if key, host, baseURL, exists, cfgErr := service.readManagedProviderConfig(ctx); cfgErr != nil || !exists || key != "account-b" || host != "" || baseURL != "https://target.example/v1" {
				t.Fatalf("provider config after switch = key %q host %q base %q (exists=%t, err=%v)", key, host, baseURL, exists, cfgErr)
			}
			if fake.legacyHost != "" {
				t.Fatalf("legacy OAuth host survived the switch: %q", fake.legacyHost)
			}
			if _, err := os.Lstat(fake.credPathFor("account-b") + ".ekd-bak"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("recovery backup still present after successful switch: %v", err)
			}
		})
	}
}

func TestDirectVaultResetClearsAccountRecordsAndPendingFlows(t *testing.T) {
	service, fake, vault := newAccountTestService(t)
	saveTwoTestAccounts(t, service, fake)
	service.accountMu.Lock()
	service.addFlow = &AccountAddFlow{Active: true}
	service.pendingSwitch = &pendingSwitchState{cancel: func() {}}
	service.accountMu.Unlock()
	if err := vault.Reset(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := service.accountsList(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Accounts) != 0 || state.PendingSwitch != nil || state.AddFlow != nil {
		t.Fatalf("direct vault reset left stale account state: %+v", state)
	}
	var count int
	if err := service.db.QueryRow(`SELECT COUNT(*) FROM kimi_accounts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("account metadata count=%d err=%v", count, err)
	}
}

func TestAccountSwitchRollsBackDatabaseOnSameOAuthKey(t *testing.T) {
	ctx := context.Background()
	service, fake, vault := newAccountTestService(t)
	accA, accB := saveTwoTestAccounts(t, service, fake)

	// The official client rotated A's token while the user was still signed in.
	if err := fake.writeDiskTokenFor(defaultManagedOAuthKey, "tok-a-v2", "ref-a-v2", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.tokens["tok-a-v2"] = AccountProfile{UserID: "uid-a", Nickname: "账号A", Plan: "Moderato", Region: "cn"}
	fake.mu.Unlock()
	diskBefore, err := os.ReadFile(fake.credPath())
	if err != nil {
		t.Fatal(err)
	}
	storedABefore := savedAccountSecret(t, ctx, vault, accA.ID)
	storedBBefore := savedAccountSecret(t, ctx, vault, accB.ID)
	rowABefore := accountRowForTest(t, ctx, service, accA.UserID)
	rowBBefore := accountRowForTest(t, ctx, service, accB.UserID)

	service.hostPIDProvider = func() int { return 9999 }
	payload, _ := json.Marshal(map[string]any{"id": accB.ID, "expectedPid": 4242})
	if _, err := service.Action(ctx, "accounts.switch", payload); err == nil || !strings.Contains(err.Error(), "已自动恢复原账号") {
		t.Fatalf("switch error = %v", err)
	}

	if after := savedAccountSecret(t, ctx, vault, accA.ID); !bytes.Equal(after, storedABefore) {
		t.Fatalf("account A record was not restored after rollback:\nbefore=%s\nafter=%s", storedABefore, after)
	}
	if after := savedAccountSecret(t, ctx, vault, accB.ID); !bytes.Equal(after, storedBBefore) {
		t.Fatalf("account B record changed during rollback")
	}
	if after := accountRowForTest(t, ctx, service, accA.UserID); after != rowABefore {
		t.Fatalf("account A row was not restored:\nbefore=%+v\nafter=%+v", rowABefore, after)
	}
	if after := accountRowForTest(t, ctx, service, accB.UserID); after != rowBBefore {
		t.Fatalf("account B row was not restored:\nbefore=%+v\nafter=%+v", rowBBefore, after)
	}
	if after, err := os.ReadFile(fake.credPath()); err != nil || !bytes.Equal(after, diskBefore) {
		t.Fatalf("credential file was not restored: %v", err)
	}
	if _, err := os.Lstat(fake.credPath() + ".ekd-bak"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery backup still present after complete rollback: %v", err)
	}
}

func TestAccountSwitchRollsBackOnResnapshotFailure(t *testing.T) {
	ctx := context.Background()
	service, fake, vault := newAccountTestService(t)
	accA, accB := saveTwoTestAccounts(t, service, fake)

	if err := fake.writeDiskTokenFor(defaultManagedOAuthKey, "tok-a-v2", "ref-a-v2", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.tokens["tok-a-v2"] = AccountProfile{UserID: "uid-a", Nickname: "账号A", Plan: "Moderato", Region: "cn"}
	fake.mu.Unlock()
	diskBefore, err := os.ReadFile(fake.credPath())
	if err != nil {
		t.Fatal(err)
	}
	storedABefore := savedAccountSecret(t, ctx, vault, accA.ID)
	rowABefore := accountRowForTest(t, ctx, service, accA.UserID)

	// Fail only INSERT (used by upsertAccountWithSecret) while allowing UPDATE (used by restoreAccountRecord).
	if _, err := service.db.ExecContext(ctx, `CREATE TRIGGER fail_kimi_accounts_insert BEFORE INSERT ON kimi_accounts BEGIN SELECT RAISE(ABORT, 'injected upsert failure'); END;`); err != nil {
		t.Fatal(err)
	}

	payload, _ := json.Marshal(map[string]any{"id": accB.ID, "expectedPid": 4242})
	_, switchErr := service.Action(ctx, "accounts.switch", payload)
	if switchErr == nil || !strings.Contains(switchErr.Error(), "切换前更新当前账号凭据快照失败") || !strings.Contains(switchErr.Error(), "已自动恢复原账号") {
		t.Fatalf("switch error = %v", switchErr)
	}
	if after := savedAccountSecret(t, ctx, vault, accA.ID); !bytes.Equal(after, storedABefore) {
		t.Fatalf("account A vault secret was not restored after resnapshot failure:\nbefore=%s\nafter=%s", storedABefore, after)
	}
	if after := accountRowForTest(t, ctx, service, accA.UserID); after != rowABefore {
		t.Fatalf("account A row was not restored after resnapshot failure:\nbefore=%+v\nafter=%+v", rowABefore, after)
	}
	if after, err := os.ReadFile(fake.credPath()); err != nil || !bytes.Equal(after, diskBefore) {
		t.Fatalf("credential file changed after pre-write rollback: %v", err)
	}
	if _, err := os.Lstat(fake.credPath() + ".ekd-bak"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery backup still present after resnapshot rollback: %v", err)
	}
}

func TestAccountSwitchIgnoresStaleOpenBackgroundWhenKAPReportsIdle(t *testing.T) {
	ctx := context.Background()
	service, fake, _ := newAccountTestService(t)
	_, accB := saveTwoTestAccounts(t, service, fake)

	store, err := service.cacheStore()
	if err != nil {
		t.Fatalf("cacheStore: %v", err)
	}
	if _, err := service.db.ExecContext(ctx, `INSERT INTO cache_live(session_id,open_background,blocked,last_user_prompt,updated_at) VALUES('sess-1',2,0,'',?) ON CONFLICT(session_id) DO UPDATE SET open_background=2`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed stale cache_live: %v", err)
	}
	_ = store

	// Modern KAP explicitly reports activity="idle": stale open_background on disk must not block switching.
	fake.mu.Lock()
	fake.sessions[0]["activity"] = "idle"
	fake.sessions[0]["busy"] = false
	fake.sessions[0]["main_turn_active"] = false
	fake.sessions[0]["pending_interaction"] = "none"
	fake.mu.Unlock()

	prePayload, _ := json.Marshal(map[string]any{"id": accB.ID})
	preRaw, err := service.Action(ctx, "accounts.switchPreflight", prePayload)
	if err != nil {
		t.Fatalf("switchPreflight(idle): %v", err)
	}
	pre := preRaw.(AccountSwitchPreflight)
	if len(pre.BusySessions) != 0 || !pre.CanSwitch || !pre.CanForceSwitch {
		t.Fatalf("expected idle session to ignore stale open_background, got %+v", pre)
	}

	// When KAP explicitly reports activity="background", preflight should report background subagent running.
	fake.mu.Lock()
	fake.sessions[0]["activity"] = "background"
	fake.mu.Unlock()
	preRaw, err = service.Action(ctx, "accounts.switchPreflight", prePayload)
	if err != nil {
		t.Fatalf("switchPreflight(background): %v", err)
	}
	pre = preRaw.(AccountSwitchPreflight)
	if len(pre.BusySessions) != 1 || pre.BusySessions[0].Reason != "有后台子代理正在运行" || pre.CanSwitch || !pre.CanForceSwitch {
		t.Fatalf("unexpected preflight for background activity: %+v", pre)
	}

	// Switch succeeds cleanly once KAP reports idle again without needing force.
	fake.mu.Lock()
	fake.sessions[0]["activity"] = "idle"
	fake.mu.Unlock()
	swPayload, _ := json.Marshal(map[string]any{"id": accB.ID})
	swRaw, err := service.Action(ctx, "accounts.switch", swPayload)
	if err != nil {
		t.Fatalf("accounts.switch when idle with stale cache_live: %v", err)
	}
	sw := swRaw.(AccountSwitchResult)
	if !sw.Switched || fake.readDiskToken(t).AccessToken != "tok-acc-b-secret-1" {
		t.Fatalf("expected clean switch to B, got %+v", sw)
	}
}

func TestAccountSwitchSavesUnsavedCurrentAccountBeforeOverwriting(t *testing.T) {
	ctx := context.Background()
	service, fake, _ := newAccountTestService(t)
	accA, accB := saveTwoTestAccounts(t, service, fake)

	fake.mu.Lock()
	fake.tokens["{{SECRET_n3wunsaved}}"] = AccountProfile{UserID: "uid-new", Nickname: "新登录账号", Email: "new@example.com", Plan: "Moderato"}
	fake.mu.Unlock()
	if err := fake.writeDiskTokenFor(fake.oauthKey, "{{SECRET_n3wunsaved}}", "ref-new-unsaved", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	payload, _ := json.Marshal(map[string]any{"id": accB.ID})
	raw, err := service.Action(ctx, "accounts.switch", payload)
	if err != nil {
		t.Fatalf("accounts.switch: %v", err)
	}
	if sw := raw.(AccountSwitchResult); !sw.Switched {
		t.Fatalf("expected switch to succeed: %+v", sw)
	}

	saved, err := service.loadAccountRowByUserID(ctx, "uid-new")
	if err != nil {
		t.Fatalf("unsaved current account was not persisted: %v", err)
	}
	if saved.Label != "新登录账号" {
		t.Fatalf("expected nickname fallback label, got %q", saved.Label)
	}
	vault := service.currentAccountVault()
	secret, err := vault.Get(ctx, accountVaultRef(saved.ID))
	if err != nil {
		t.Fatalf("read saved secret: %v", err)
	}
	var stored storedKimiAccountSecret
	if err := json.Unmarshal(secret, &stored); err != nil {
		t.Fatal(err)
	}
	fields, err := parseRawTokenFields(stored.RawCredential)
	if err != nil || fields.AccessToken != "{{SECRET_n3wunsaved}}" {
		t.Fatalf("unsaved account token was not preserved: %+v %v", fields, err)
	}
	if saved.ID == accA.ID || saved.ID == accB.ID {
		t.Fatalf("unexpected id reuse: %s", saved.ID)
	}
}

func TestAccountSwitchRollbackRemovesAutoSavedCurrentAccount(t *testing.T) {
	ctx := context.Background()
	service, fake, _ := newAccountTestService(t)
	_, accB := saveTwoTestAccounts(t, service, fake)

	fake.mu.Lock()
	fake.tokens["{{SECRET_n3wunsaved}}"] = AccountProfile{UserID: "uid-new", Nickname: "新登录账号", Plan: "Moderato"}
	fake.mu.Unlock()
	if err := fake.writeDiskTokenFor(fake.oauthKey, "{{SECRET_n3wunsaved}}", "ref-new-unsaved", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	service.writeAccountFileHook = func(string, []byte) error { return errors.New("simulated disk full") }

	payload, _ := json.Marshal(map[string]any{"id": accB.ID})
	if _, err := service.Action(ctx, "accounts.switch", payload); err == nil {
		t.Fatal("expected write failure")
	}
	if _, err := service.loadAccountRowByUserID(ctx, "uid-new"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("auto-saved account survived rollback: %v", err)
	}
}

func TestQueuedAccountSwitchCancelledBeforeRunDoesNotSwitch(t *testing.T) {
	ctx := context.Background()
	service, fake, _ := newAccountTestService(t)
	_, accB := saveTwoTestAccounts(t, service, fake)

	fake.mu.Lock()
	fake.sessions[0]["activity"] = "turn"
	fake.sessions[0]["busy"] = true
	fake.sessions[0]["main_turn_active"] = true
	fake.mu.Unlock()

	queuePayload, _ := json.Marshal(map[string]any{"id": accB.ID, "waitUntilIdle": true})
	rawQueued, err := service.Action(ctx, "accounts.switch", queuePayload)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if queued := rawQueued.(AccountSwitchResult); !queued.Queued {
		t.Fatalf("expected queued result: %+v", queued)
	}
	if _, err := service.Action(ctx, "accounts.cancelPending", nil); err != nil {
		t.Fatalf("cancelPending: %v", err)
	}
	fake.mu.Lock()
	fake.sessions[0]["activity"] = "idle"
	fake.sessions[0]["busy"] = false
	fake.sessions[0]["main_turn_active"] = false
	fake.mu.Unlock()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := fake.readDiskToken(t).AccessToken; got != "tok-acc-a-secret-1" {
			t.Fatalf("cancelled queued switch still switched accounts: %s", got)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestAccountSwitchForceWhileBusySucceedsAndCancelsPending(t *testing.T) {
	ctx := context.Background()
	service, fake, _ := newAccountTestService(t)
	_, accB := saveTwoTestAccounts(t, service, fake)

	fake.mu.Lock()
	fake.sessions[0]["activity"] = "turn"
	fake.sessions[0]["busy"] = true
	fake.sessions[0]["main_turn_active"] = true
	fake.mu.Unlock()

	prePayload, _ := json.Marshal(map[string]any{"id": accB.ID})
	preRaw, err := service.Action(ctx, "accounts.switchPreflight", prePayload)
	if err != nil {
		t.Fatalf("switchPreflight: %v", err)
	}
	pre := preRaw.(AccountSwitchPreflight)
	if pre.CanSwitch || !pre.CanForceSwitch || len(pre.BusySessions) != 1 {
		t.Fatalf("expected CanSwitch=false CanForceSwitch=true BusySessions=1, got %+v", pre)
	}

	// Default switch without force still pauses with ACCOUNT_SWITCH_BUSY.
	defaultPayload, _ := json.Marshal(map[string]any{"id": accB.ID})
	_, err = service.Action(ctx, "accounts.switch", defaultPayload)
	var nativeErr *NativeError
	if !errors.As(err, &nativeErr) || nativeErr.Code != "ACCOUNT_SWITCH_BUSY" {
		t.Fatalf("expected ACCOUNT_SWITCH_BUSY without force, got %v", err)
	}

	// Queue a pending switch, then force-switch immediately and ensure the queue is cleared.
	queuePayload, _ := json.Marshal(map[string]any{"id": accB.ID, "waitUntilIdle": true})
	if _, err := service.Action(ctx, "accounts.switch", queuePayload); err != nil {
		t.Fatalf("queue waitUntilIdle: %v", err)
	}
	forcePayload, _ := json.Marshal(map[string]any{"id": accB.ID, "force": true})
	swRaw, err := service.Action(ctx, "accounts.switch", forcePayload)
	if err != nil {
		t.Fatalf("force switch while busy: %v", err)
	}
	sw := swRaw.(AccountSwitchResult)
	if !sw.Switched || sw.Queued {
		t.Fatalf("expected immediate switched result, got %+v", sw)
	}
	if got := fake.readDiskToken(t).AccessToken; got != "tok-acc-b-secret-1" {
		t.Fatalf("expected disk token switched to B, got %s", got)
	}
	foundForceWarning := false
	for _, w := range sw.Warnings {
		if strings.Contains(w, "强制切换") {
			foundForceWarning = true
			break
		}
	}
	if !foundForceWarning {
		t.Fatalf("expected forced switch warning in %+v", sw.Warnings)
	}
	stateRaw, err := service.Action(ctx, "accounts.list", nil)
	if err != nil {
		t.Fatalf("accounts.list: %v", err)
	}
	if state := stateRaw.(AccountsState); state.PendingSwitch != nil {
		t.Fatalf("expected pendingSwitch cleared after force switch, got %+v", state.PendingSwitch)
	}
}

func TestKeepAliveIgnoresStaleOpenBackgroundWhenKAPReportsIdle(t *testing.T) {
	ctx := context.Background()
	service, fake, _ := newAccountTestService(t)
	if _, err := service.cacheStore(); err != nil {
		t.Fatalf("cacheStore: %v", err)
	}

	if _, err := service.db.ExecContext(ctx, `INSERT INTO cache_live(session_id,open_background,blocked,last_user_prompt,updated_at) VALUES('sess-1',2,1,'',?) ON CONFLICT(session_id) DO UPDATE SET open_background=2,blocked=1`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed stale cache_live: %v", err)
	}

	fake.mu.Lock()
	fake.sessions[0]["activity"] = "idle"
	fake.sessions[0]["busy"] = false
	fake.sessions[0]["main_turn_active"] = false
	fake.sessions[0]["pending_interaction"] = "none"
	fake.mu.Unlock()

	if err := service.syncAutomationSessions(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("sync automation sessions: %v", err)
	}
	snapshot, ok := service.automationSnapshot("sess-1")
	if !ok {
		t.Fatal("missing native session snapshot")
	}
	live := service.automationLive(ctx, snapshot)
	if idleKeepAliveBlocked(snapshot, live) {
		t.Fatalf("stale cache_live blocked an officially idle session: %+v", live)
	}
}

// A queued switch that sees a session become busy again must keep waiting in the
// same queue entry instead of silently dropping the request. The window is
// reproduced directly: the queue is set up, then the switch runs while busy.
func TestQueuedAccountSwitchRetriesAfterBecomingBusyAgain(t *testing.T) {
	service, fake, _ := newAccountTestService(t)
	_, accB := saveTwoTestAccounts(t, service, fake)

	fake.mu.Lock()
	fake.sessions[0]["activity"] = "turn"
	fake.sessions[0]["busy"] = true
	fake.sessions[0]["main_turn_active"] = true
	fake.mu.Unlock()

	watchCtx, cancelWatch := context.WithCancel(service.ctx)
	defer cancelWatch()
	service.accountMu.Lock()
	service.switchSeq = 41
	seq := service.switchSeq
	service.pendingSwitch = &pendingSwitchState{
		info:   AccountPendingSwitch{TargetID: accB.ID},
		seq:    seq,
		cancel: cancelWatch,
	}
	service.accountMu.Unlock()

	// A busy attempt must not consume the queue entry or cancel its watch context.
	attemptCtx, attemptCancel := context.WithTimeout(watchCtx, 5*time.Second)
	_, err := service.executeAccountSwitch(attemptCtx, accB.ID, false, false, 0, seq)
	attemptCancel()
	var native *NativeError
	if !errors.As(err, &native) || native.Code != "ACCOUNT_SWITCH_BUSY" {
		t.Fatalf("expected ACCOUNT_SWITCH_BUSY, got %v", err)
	}
	if watchCtx.Err() != nil {
		t.Fatal("a busy retry cancelled the queue watch context")
	}
	service.accountMu.Lock()
	pendingIntact := service.pendingSwitch != nil && service.pendingSwitch.seq == seq
	service.accountMu.Unlock()
	if !pendingIntact {
		t.Fatal("a busy retry consumed the pending switch")
	}

	// Once idle, the same queue entry can still perform the switch.
	fake.mu.Lock()
	fake.sessions[0]["activity"] = "idle"
	fake.sessions[0]["busy"] = false
	fake.sessions[0]["main_turn_active"] = false
	fake.mu.Unlock()

	retryCtx, retryCancel := context.WithTimeout(watchCtx, 10*time.Second)
	result, err := service.executeAccountSwitch(retryCtx, accB.ID, false, false, 0, seq)
	retryCancel()
	if err != nil {
		t.Fatalf("retry after idle: %v", err)
	}
	if !result.Switched {
		t.Fatalf("retry did not switch: %+v", result)
	}
	if got := fake.readDiskToken(t).AccessToken; got != "tok-acc-b-secret-1" {
		t.Fatalf("disk token after retry = %s", got)
	}
	service.accountMu.Lock()
	cleared := service.pendingSwitch == nil
	service.accountMu.Unlock()
	if !cleared {
		t.Fatal("successful switch did not clear the pending slot")
	}
}

func TestPendingSwitchMatchingIsScopedToSeq(t *testing.T) {
	service, _, _ := newAccountTestService(t)
	_, cancelFirst := context.WithCancel(service.ctx)
	defer cancelFirst()
	_, cancelSecond := context.WithCancel(service.ctx)
	defer cancelSecond()

	service.accountMu.Lock()
	service.switchSeq = 7
	service.pendingSwitch = &pendingSwitchState{
		info:   AccountPendingSwitch{TargetID: "acc_target"},
		seq:    7,
		cancel: cancelSecond,
	}
	service.accountMu.Unlock()

	// A stale watcher for the same target must not touch the newer request.
	service.cancelPendingSwitchIfSeq(6)
	service.accountMu.Lock()
	stillPending := service.pendingSwitch != nil && service.pendingSwitch.seq == 7
	service.accountMu.Unlock()
	if !stillPending {
		t.Fatal("a stale sequence cancelled a newer request for the same target")
	}

	// A stale read-only validation must be rejected without consuming the slot.
	if err := service.validateQueuedSwitch(context.Background(), 6, "acc_target"); !errors.Is(err, errAccountSwitchSuperseded) {
		t.Fatalf("stale validation error = %v", err)
	}
	service.accountMu.Lock()
	stillPending = service.pendingSwitch != nil && service.pendingSwitch.seq == 7
	service.accountMu.Unlock()
	if !stillPending {
		t.Fatal("a rejected validation consumed the pending slot")
	}

	// A cancelled watch context must not be able to start the switch.
	cancelFirst()
	supersededCtx, supersededCancel := context.WithCancel(service.ctx)
	supersededCancel()
	if err := service.validateQueuedSwitch(supersededCtx, 7, "acc_target"); !errors.Is(err, errAccountSwitchSuperseded) {
		t.Fatalf("cancelled watch error = %v", err)
	}

	// Owning sequence consumes exactly its own entry.
	if _, err := service.consumeQueuedSwitch(context.Background(), 7, "acc_target"); err != nil {
		t.Fatalf("consume own sequence: %v", err)
	}
	service.accountMu.Lock()
	cleared := service.pendingSwitch == nil
	service.accountMu.Unlock()
	if !cleared {
		t.Fatal("owning sequence did not clear the pending slot")
	}
}

// Rollback must not delete the metadata row when the ciphertext could not be
// removed, or the secret becomes an orphan nothing can reach.
func TestDeleteAccountRecordKeepsRowWhenVaultDeleteFails(t *testing.T) {
	ctx := context.Background()
	service, fake, vault := newAccountTestService(t)
	accA, _ := saveTwoTestAccounts(t, service, fake)

	vault.Lock()
	err := service.deleteAccountRecord(ctx, accA.ID)
	if err == nil {
		t.Fatal("delete succeeded while the vault was locked")
	}
	if _, rowErr := service.loadAccountRow(ctx, accA.ID); rowErr != nil {
		t.Fatalf("metadata row was deleted without removing the secret: %v", rowErr)
	}

	if unlockErr := vault.Unlock(ctx, "test-passphrase-1234"); unlockErr != nil {
		t.Fatal(unlockErr)
	}
	if err := service.deleteAccountRecord(ctx, accA.ID); err != nil {
		t.Fatalf("delete after unlock: %v", err)
	}
	if _, rowErr := service.loadAccountRow(ctx, accA.ID); !errors.Is(rowErr, sql.ErrNoRows) {
		t.Fatalf("metadata row survived a successful delete: %v", rowErr)
	}
	if exists, existsErr := vault.KimiAccountStore().Exists(ctx, accountVaultRef(accA.ID)); existsErr != nil || exists {
		t.Fatalf("ciphertext survived delete: exists=%v err=%v", exists, existsErr)
	}
}
