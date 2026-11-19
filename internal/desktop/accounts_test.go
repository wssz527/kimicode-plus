package desktop

import (
	"bytes"
	"context"
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
	failModels        bool
	failUser          int
	wrongUID          string
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
	return filepath.Join(f.home, "credentials", "kimi-code.json")
}

func (f *fakeAccountKAP) writeDiskToken(t *testing.T, access, refresh string, expiresAt int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.credPath()), 0o700); err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	if err := os.WriteFile(f.credPath(), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeAccountKAP) readDiskToken(t *testing.T) rawOAuthTokenFields {
	t.Helper()
	data, err := os.ReadFile(f.credPath())
	if err != nil {
		t.Fatalf("read disk token: %v", err)
	}
	var out rawOAuthTokenFields
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal disk token: %v", err)
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
			providers["managed:kimi-code"] = map[string]any{
				"type":    "kimi",
				"baseUrl": "https://api.kimi.com/coding/v1",
				"apiKey":  "",
				"oauth": map[string]any{
					"storage": "file",
					"key":     "oauth/kimi-code",
				},
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
