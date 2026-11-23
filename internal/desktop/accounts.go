package desktop

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"enhancedkimi/internal/credentials"
	"enhancedkimi/internal/platform"
	"enhancedkimi/internal/quota"
	"enhancedkimi/internal/security"
	"enhancedkimi/internal/storage"
)

const (
	accountStatusReady           = "ready"
	accountStatusReloginRequired = "relogin_required"
	defaultManagedProviderID     = "managed:kimi-code"
	defaultManagedOAuthKey       = "kimi-code"
	defaultManagedBaseURL        = "https://api.kimi.com/coding/v1"
)

type SchedulerPauser interface {
	Pause() func()
}

type AccountQuotaSummary struct {
	Status       string   `json:"status"` // "ok" | "unknown" | "auth_rejected"
	Limit5hUsed  *float64 `json:"limit5hUsed,omitempty"`
	Limit7dUsed  *float64 `json:"limit7dUsed,omitempty"`
	Limit5hReset *string  `json:"limit5hReset,omitempty"`
	Limit7dReset *string  `json:"limit7dReset,omitempty"`
	CheckedAt    string   `json:"checkedAt,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type AccountProfile struct {
	UserID   string `json:"userId"`
	Nickname string `json:"nickname"`
	Avatar   string `json:"avatar,omitempty"`
	Plan     string `json:"plan,omitempty"`
	Region   string `json:"region,omitempty"`
	Email    string `json:"email,omitempty"`
}

type AccountSummary struct {
	ID              string               `json:"id"`
	UserID          string               `json:"userId"`
	Label           string               `json:"label"`
	Nickname        string               `json:"nickname"`
	Avatar          string               `json:"avatar,omitempty"`
	Plan            string               `json:"plan,omitempty"`
	Region          string               `json:"region,omitempty"`
	Current         bool                 `json:"current"`
	Status          string               `json:"status"`
	StatusReason    string               `json:"statusReason,omitempty"`
	Quota           *AccountQuotaSummary `json:"quota,omitempty"`
	CredentialSaved bool                 `json:"credentialSaved"`
	UpdatedAt       string               `json:"updatedAt"`
}

type AccountBusySession struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

type AccountPendingSwitch struct {
	TargetID       string   `json:"targetId"`
	TargetLabel    string   `json:"targetLabel"`
	BusySessionIDs []string `json:"busySessionIds"`
	QueuedAt       string   `json:"queuedAt"`
}

type AccountAddFlow struct {
	Active           bool   `json:"active"`
	Mode             string `json:"mode"` // "add" | "relogin"
	PreviousUserID   string `json:"previousUserId,omitempty"`
	ReloginAccountID string `json:"reloginAccountId,omitempty"`
	StartedAt        string `json:"startedAt,omitempty"`
}

type AccountsState struct {
	VaultUnlocked            bool                  `json:"vaultUnlocked"`
	VaultOSKeychain          bool                  `json:"vaultOsKeychain"`
	VaultOSKeychainAvailable bool                  `json:"vaultOsKeychainAvailable"`
	VaultOSKeychainBackend   string                `json:"vaultOsKeychainBackend,omitempty"`
	LegacyPasswordVault      bool                  `json:"legacyPasswordVault"`
	SignedIn                 bool                  `json:"signedIn"`
	CurrentAccountID         string                `json:"currentAccountId,omitempty"`
	CurrentProfile           *AccountProfile       `json:"currentProfile,omitempty"`
	Accounts                 []AccountSummary      `json:"accounts"`
	PendingSwitch            *AccountPendingSwitch `json:"pendingSwitch,omitempty"`
	AddFlow                  *AccountAddFlow       `json:"addFlow,omitempty"`
	CLIRunning               bool                  `json:"cliRunning"`
}

type AccountSwitchPreflight struct {
	Target         AccountSummary       `json:"target"`
	VaultUnlocked  bool                 `json:"vaultUnlocked"`
	CanSwitch      bool                 `json:"canSwitch"`
	CanForceSwitch bool                 `json:"canForceSwitch"`
	RequiresLogin  bool                 `json:"requiresLogin"`
	BusySessions   []AccountBusySession `json:"busySessions"`
	Warnings       []string             `json:"warnings"`
	CLIRunning     bool                 `json:"cliRunning"`
	ActiveModel    string               `json:"activeModel,omitempty"`
	ModelMayDiffer bool                 `json:"modelMayDiffer"`
}

type AccountSwitchResult struct {
	Switched             bool                  `json:"switched"`
	Queued               bool                  `json:"queued,omitempty"`
	Account              *AccountSummary       `json:"account,omitempty"`
	PendingSwitch        *AccountPendingSwitch `json:"pendingSwitch,omitempty"`
	SessionsCount        int                   `json:"sessionsCount,omitempty"`
	HostPID              int                   `json:"hostPid,omitempty"`
	Warnings             []string              `json:"warnings,omitempty"`
	BackupCleanupWarning string                `json:"backupCleanupWarning,omitempty"`
	RefreshAuth          bool                  `json:"refreshAuth"`
}

type storedKimiAccountSecret struct {
	RawCredential json.RawMessage `json:"rawCredential"`
	OAuthKey      string          `json:"oauthKey,omitempty"`
	OAuthHost     string          `json:"oauthHost,omitempty"`
	BaseURL       string          `json:"baseUrl,omitempty"`
}

type rawOAuthTokenFields struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

type accountRow struct {
	ID           string
	UserID       string
	Label        string
	Nickname     string
	Avatar       string
	Plan         string
	Region       string
	OAuthKey     string
	OAuthHost    string
	BaseURL      string
	Status       string
	StatusReason string
	QuotaJSON    string
	ModelsJSON   string
	LastSeenAt   string
	CreatedAt    string
	UpdatedAt    string
}

type pendingSwitchState struct {
	info        AccountPendingSwitch
	seq         uint64
	expectedPID int
	cancel      context.CancelFunc
}

var errAccountSwitchSuperseded = errors.New("账号切换请求已取消或已被新的切换取代")

func (s *NativeService) UseVault(vault *credentials.Vault) {
	s.mu.Lock()
	s.vault = vault
	if vault == nil {
		s.accountVault = nil
	} else {
		s.accountVault = vault.KimiAccountStore()
	}
	s.mu.Unlock()
	if vault != nil {
		vault.UseOnReset(s.resetAccountMetadata)
	}
}

func (s *NativeService) currentVault() *credentials.Vault {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.vault
}

func (s *NativeService) currentAccountVault() *credentials.KimiAccountVault {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accountVault
}

func (s *NativeService) pauseForAccountSwitch() func() {
	s.mu.Lock()
	s.switchPauseCount++
	sched := s.scheduler
	s.mu.Unlock()

	var resumeSched func()
	if pauser, ok := sched.(SchedulerPauser); ok {
		resumeSched = pauser.Pause()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			if resumeSched != nil {
				resumeSched()
			}
			s.mu.Lock()
			if s.switchPauseCount > 0 {
				s.switchPauseCount--
			}
			s.mu.Unlock()
		})
	}
}

func (s *NativeService) isAutomationPausedForSwitch() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.switchPauseCount > 0
}

func accountVaultRef(id string) string {
	return credentials.KimiAccountRefPrefix + id
}

func sanitizeOAuthKey(key string) string {
	trimmed := strings.TrimSpace(key)
	trimmed = strings.TrimPrefix(trimmed, "oauth/")
	if trimmed == "" || trimmed == "." || trimmed == ".." || strings.ContainsAny(trimmed, `/\`) {
		return defaultManagedOAuthKey
	}
	base := filepath.Base(trimmed)
	if base == "" || base == "." || base == ".." || strings.HasPrefix(base, ".") {
		return defaultManagedOAuthKey
	}
	return base
}

func (s *NativeService) kimiCredentialPath(oauthKey string) (string, error) {
	if s.kap == nil || strings.TrimSpace(s.kap.Home()) == "" {
		return "", errors.New("未配置 Kimi Code 数据目录")
	}
	key := sanitizeOAuthKey(oauthKey)
	return filepath.Join(s.kap.Home(), "credentials", key+".json"), nil
}

func (s *NativeService) kimiOAuthLockDir(oauthKey string) string {
	if s.kap == nil || strings.TrimSpace(s.kap.Home()) == "" {
		return ""
	}
	key := sanitizeOAuthKey(oauthKey)
	return filepath.Join(s.kap.Home(), "oauth", key+".lock")
}

func (s *NativeService) waitForOAuthRefreshLock(ctx context.Context, oauthKey string) {
	lockDir := s.kimiOAuthLockDir(oauthKey)
	if lockDir == "" {
		return
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		info, err := os.Stat(lockDir)
		if err != nil {
			return
		}
		if time.Since(info.ModTime()) > 5*time.Second {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func writeFileAtomic0600(target string, data []byte) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d.%s", target, os.Getpid(), hex.EncodeToString(suffix[:]))
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	writeErr := func() error {
		if _, err := f.Write(data); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
		return f.Close()
	}()
	if writeErr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return writeErr
	}
	_ = os.Chmod(tmp, 0o600)
	if err := platform.RetryFileOperation(func() error { return os.Rename(tmp, target) }); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func writeAccountSwitchBackup(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return errors.Join(err, os.Remove(path))
	}
	return nil
}

func (s *NativeService) writeCredentialFile(target string, data []byte) error {
	if s.writeAccountFileHook != nil {
		return s.writeAccountFileHook(target, data)
	}
	return writeFileAtomic0600(target, data)
}

func (s *NativeService) readManagedProviderConfig(ctx context.Context) (oauthKey, oauthHost, baseURL string, exists bool, err error) {
	oauthKey = defaultManagedOAuthKey
	baseURL = defaultManagedBaseURL
	raw, err := s.kap.Request(ctx, http.MethodGet, "/config", nil)
	if err != nil {
		return oauthKey, oauthHost, baseURL, false, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return oauthKey, oauthHost, baseURL, false, err
	}
	if cfg == nil {
		return oauthKey, oauthHost, baseURL, false, errors.New("官方供应商配置为空")
	}
	providers, ok := cfg["providers"].(map[string]any)
	if !ok && cfg["providers"] != nil {
		return oauthKey, oauthHost, baseURL, false, errors.New("官方供应商配置格式无效")
	}
	managed, ok := providers[defaultManagedProviderID].(map[string]any)
	if !ok && providers[defaultManagedProviderID] != nil {
		return oauthKey, oauthHost, baseURL, false, errors.New("官方账号供应商配置格式无效")
	}
	if managed == nil {
		return oauthKey, oauthHost, baseURL, false, nil
	}
	exists = true
	if u, _ := managed["baseUrl"].(string); strings.TrimSpace(u) != "" {
		baseURL = strings.TrimRight(strings.TrimSpace(u), "/")
	} else if u, _ := managed["base_url"].(string); strings.TrimSpace(u) != "" {
		baseURL = strings.TrimRight(strings.TrimSpace(u), "/")
	}
	if oauth, _ := managed["oauth"].(map[string]any); oauth != nil {
		if k, _ := oauth["key"].(string); strings.TrimSpace(k) != "" {
			oauthKey = sanitizeOAuthKey(k)
		}
		if h, _ := oauth["oauthHost"].(string); strings.TrimSpace(h) != "" {
			oauthHost = strings.TrimRight(strings.TrimSpace(h), "/")
		} else if h, _ := oauth["oauth_host"].(string); strings.TrimSpace(h) != "" {
			oauthHost = strings.TrimRight(strings.TrimSpace(h), "/")
		}
	}
	return oauthKey, oauthHost, baseURL, exists, nil
}

func (s *NativeService) readOfficialUserInfo(ctx context.Context) (*AccountProfile, int, string) {
	raw, err := s.kap.Request(ctx, http.MethodGet, "/oauth/userinfo", nil)
	if err != nil {
		var native *NativeError
		if errors.As(err, &native) {
			return nil, native.Status, native.Message
		}
		return nil, 0, err.Error()
	}
	var reply struct {
		Kind     string `json:"kind"`
		Status   int    `json:"status"`
		Message  string `json:"message"`
		UserInfo *struct {
			UserID        string `json:"userId"`
			UserIDSnake   string `json:"user_id"`
			Nickname      string `json:"nickname"`
			Avatar        string `json:"avatar"`
			Region        string `json:"region"`
			UserLevelName string `json:"userLevelName"`
			UserLevelSnk  string `json:"user_level_name"`
			Email         string `json:"email"`
		} `json:"userInfo"`
		UserID        string `json:"userId"`
		UserIDSnake   string `json:"user_id"`
		Nickname      string `json:"nickname"`
		Avatar        string `json:"avatar"`
		Region        string `json:"region"`
		UserLevelName string `json:"userLevelName"`
		UserLevelSnk  string `json:"user_level_name"`
		Email         string `json:"email"`
	}
	if json.Unmarshal(raw, &reply) != nil {
		return nil, 0, "官方用户信息格式无法识别"
	}
	if reply.Kind == "error" {
		return nil, reply.Status, firstNonEmpty(reply.Message, "未登录或令牌无效")
	}
	if reply.UserInfo != nil {
		uid := firstNonEmpty(reply.UserInfo.UserID, reply.UserInfo.UserIDSnake)
		if uid == "" {
			return nil, 0, "官方用户信息缺少账号标识"
		}
		return &AccountProfile{
			UserID:   uid,
			Nickname: firstNonEmpty(reply.UserInfo.Nickname, uid),
			Avatar:   strings.TrimSpace(reply.UserInfo.Avatar),
			Plan:     firstNonEmpty(reply.UserInfo.UserLevelName, reply.UserInfo.UserLevelSnk),
			Region:   strings.TrimSpace(reply.UserInfo.Region),
			Email:    strings.TrimSpace(reply.UserInfo.Email),
		}, http.StatusOK, ""
	}
	uid := firstNonEmpty(reply.UserID, reply.UserIDSnake)
	if uid == "" {
		return nil, 0, "当前未登录 Kimi Code 账号"
	}
	return &AccountProfile{
		UserID:   uid,
		Nickname: firstNonEmpty(reply.Nickname, uid),
		Avatar:   strings.TrimSpace(reply.Avatar),
		Plan:     firstNonEmpty(reply.UserLevelName, reply.UserLevelSnk),
		Region:   strings.TrimSpace(reply.Region),
		Email:    strings.TrimSpace(reply.Email),
	}, http.StatusOK, ""
}

func parseRawTokenFields(raw []byte) (rawOAuthTokenFields, error) {
	var fields rawOAuthTokenFields
	if len(raw) == 0 {
		return fields, errors.New("凭据文件为空")
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fields, errors.New("凭据文件格式无效")
	}
	return fields, nil
}

func tokenIsRevokedOrEmpty(fields rawOAuthTokenFields) bool {
	return strings.TrimSpace(fields.AccessToken) == "" || strings.TrimSpace(fields.RefreshToken) == ""
}

func (s *NativeService) loadAccountRows(ctx context.Context) ([]accountRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,user_id,label,nickname,avatar,plan,region,oauth_key,oauth_host,base_url,status,status_reason,quota_json,models_json,last_seen_at,created_at,updated_at FROM kimi_accounts ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []accountRow{}
	for rows.Next() {
		var row accountRow
		if err = rows.Scan(&row.ID, &row.UserID, &row.Label, &row.Nickname, &row.Avatar, &row.Plan, &row.Region, &row.OAuthKey, &row.OAuthHost, &row.BaseURL, &row.Status, &row.StatusReason, &row.QuotaJSON, &row.ModelsJSON, &row.LastSeenAt, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *NativeService) loadAccountRow(ctx context.Context, id string) (accountRow, error) {
	var row accountRow
	err := s.db.QueryRowContext(ctx, `SELECT id,user_id,label,nickname,avatar,plan,region,oauth_key,oauth_host,base_url,status,status_reason,quota_json,models_json,last_seen_at,created_at,updated_at FROM kimi_accounts WHERE id=?`, id).
		Scan(&row.ID, &row.UserID, &row.Label, &row.Nickname, &row.Avatar, &row.Plan, &row.Region, &row.OAuthKey, &row.OAuthHost, &row.BaseURL, &row.Status, &row.StatusReason, &row.QuotaJSON, &row.ModelsJSON, &row.LastSeenAt, &row.CreatedAt, &row.UpdatedAt)
	return row, err
}

func (s *NativeService) loadAccountRowByUserID(ctx context.Context, userID string) (accountRow, error) {
	var row accountRow
	err := s.db.QueryRowContext(ctx, `SELECT id,user_id,label,nickname,avatar,plan,region,oauth_key,oauth_host,base_url,status,status_reason,quota_json,models_json,last_seen_at,created_at,updated_at FROM kimi_accounts WHERE user_id=?`, userID).
		Scan(&row.ID, &row.UserID, &row.Label, &row.Nickname, &row.Avatar, &row.Plan, &row.Region, &row.OAuthKey, &row.OAuthHost, &row.BaseURL, &row.Status, &row.StatusReason, &row.QuotaJSON, &row.ModelsJSON, &row.LastSeenAt, &row.CreatedAt, &row.UpdatedAt)
	return row, err
}

// accountRecordSnapshot is the exact pre-switch state of one saved account
// record. The credential secret and the row metadata are captured together so a
// failed switch can put the database back the way it was instead of leaving the
// outgoing account's rotated token behind.
type accountRecordSnapshot struct {
	row       accountRow
	secret    []byte
	hadSecret bool
}

func (s *NativeService) snapshotAccountRecord(ctx context.Context, userID string) (*accountRecordSnapshot, error) {
	row, err := s.loadAccountRowByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	vault := s.currentAccountVault()
	if vault == nil {
		return nil, errors.New("加密凭据库不可用")
	}
	snapshot := &accountRecordSnapshot{row: row}
	secret, err := vault.Get(ctx, accountVaultRef(row.ID))
	if errors.Is(err, credentials.ErrNotFound) {
		return snapshot, nil
	}
	if err != nil {
		return nil, err
	}
	snapshot.secret = secret
	snapshot.hadSecret = true
	return snapshot, nil
}

func (s *NativeService) restoreAccountRecord(ctx context.Context, snapshot *accountRecordSnapshot) error {
	if snapshot == nil {
		return nil
	}
	vault := s.currentAccountVault()
	if vault == nil {
		return errors.New("加密凭据库不可用")
	}
	var restoreErrs []error
	if snapshot.hadSecret {
		restoreErrs = append(restoreErrs, vault.Put(ctx, accountVaultRef(snapshot.row.ID), snapshot.secret))
	} else if err := vault.Delete(ctx, accountVaultRef(snapshot.row.ID)); err != nil && !errors.Is(err, credentials.ErrNotFound) {
		restoreErrs = append(restoreErrs, err)
	}
	row := snapshot.row
	_, err := s.db.ExecContext(ctx, `UPDATE kimi_accounts SET user_id=?,label=?,nickname=?,avatar=?,plan=?,region=?,oauth_key=?,oauth_host=?,base_url=?,status=?,status_reason=?,quota_json=?,models_json=?,last_seen_at=?,created_at=?,updated_at=? WHERE id=?`,
		row.UserID, row.Label, row.Nickname, row.Avatar, row.Plan, row.Region, row.OAuthKey, row.OAuthHost, row.BaseURL, row.Status, row.StatusReason, row.QuotaJSON, row.ModelsJSON, row.LastSeenAt, row.CreatedAt, row.UpdatedAt, row.ID)
	restoreErrs = append(restoreErrs, err)
	return errors.Join(restoreErrs...)
}

func (s *NativeService) deleteAccountRecord(ctx context.Context, id string) error {
	var deleteErrs []error
	if vault := s.currentAccountVault(); vault != nil {
		if err := vault.Delete(ctx, accountVaultRef(id)); err != nil && !errors.Is(err, credentials.ErrNotFound) {
			deleteErrs = append(deleteErrs, err)
		}
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM kimi_accounts WHERE id=?`, id)
	deleteErrs = append(deleteErrs, err)
	return errors.Join(deleteErrs...)
}

// readManagedProviderObject reads the raw managed:kimi-code provider object so a
// switch can change only the OAuth key and restore the complete object on
// rollback without dropping fields this service does not model.
func (s *NativeService) readManagedProviderObject(ctx context.Context) (map[string]any, bool, error) {
	raw, err := s.kap.Request(ctx, http.MethodGet, "/config", nil)
	if err != nil {
		return nil, false, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, false, err
	}
	providers, ok := cfg["providers"].(map[string]any)
	if !ok {
		return nil, false, nil
	}
	managed, ok := providers[defaultManagedProviderID].(map[string]any)
	if !ok || managed == nil {
		return nil, false, nil
	}
	return managed, true, nil
}

func (s *NativeService) accountRowToSummary(ctx context.Context, row accountRow, currentUserID string) AccountSummary {
	var quotaSummary *AccountQuotaSummary
	if strings.TrimSpace(row.QuotaJSON) != "" {
		var parsed AccountQuotaSummary
		if json.Unmarshal([]byte(row.QuotaJSON), &parsed) == nil && parsed.Status != "" {
			quotaSummary = &parsed
		}
	}
	saved := false
	if vault := s.currentAccountVault(); vault != nil {
		if ok, err := vault.Exists(ctx, accountVaultRef(row.ID)); err == nil {
			saved = ok
		}
	}
	status := row.Status
	if status == "" {
		status = accountStatusReady
	}
	return AccountSummary{
		ID:              row.ID,
		UserID:          row.UserID,
		Label:           firstNonEmpty(row.Label, row.Nickname, row.UserID),
		Nickname:        firstNonEmpty(row.Nickname, row.UserID),
		Avatar:          row.Avatar,
		Plan:            row.Plan,
		Region:          row.Region,
		Current:         currentUserID != "" && row.UserID == currentUserID,
		Status:          status,
		StatusReason:    row.StatusReason,
		Quota:           quotaSummary,
		CredentialSaved: saved,
		UpdatedAt:       row.UpdatedAt,
	}
}

func (s *NativeService) detectRunningKimiCLI(ctx context.Context) bool {
	if s.cliDetector != nil {
		return s.cliDetector(ctx)
	}
	if runtime.GOOS == "windows" {
		return false
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(cmdCtx, "ps", "-eo", "pid=,comm=,args=").Output()
	if err != nil {
		return false
	}
	selfPID := fmt.Sprintf("%d", os.Getpid())
	for _, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			continue
		}
		if fields[0] == selfPID {
			continue
		}
		comm := filepath.Base(fields[1])
		args := strings.Join(fields[2:], " ")
		if comm == "kimi" || (len(fields) >= 3 && (strings.HasSuffix(fields[2], "/kimi") || fields[2] == "kimi")) {
			if strings.Contains(args, "Kimi Code.app") || strings.Contains(args, "enhanced-kimi") {
				continue
			}
			return true
		}
	}
	return false
}

func (s *NativeService) accountsList(ctx context.Context, input map[string]any) (AccountsState, error) {
	vault := s.currentAccountVault()
	unlocked := vault != nil && vault.Unlocked()

	profile, _, _ := s.readOfficialUserInfo(ctx)
	currentUserID := ""
	if profile != nil {
		currentUserID = profile.UserID
	}

	if refreshQuota, _ := input["refreshQuota"].(bool); refreshQuota && unlocked {
		_, _ = s.accountsRefreshQuotas(ctx, currentUserID)
	}

	rows, err := s.loadAccountRows(ctx)
	if err != nil {
		return AccountsState{}, err
	}
	accounts := make([]AccountSummary, 0, len(rows))
	currentAccountID := ""
	for _, row := range rows {
		summary := s.accountRowToSummary(ctx, row, currentUserID)
		if summary.Current {
			currentAccountID = summary.ID
		}
		accounts = append(accounts, summary)
	}

	s.accountMu.Lock()
	var pending *AccountPendingSwitch
	if s.pendingSwitch != nil {
		copied := s.pendingSwitch.info
		pending = &copied
	}
	var addFlow *AccountAddFlow
	if s.addFlow != nil && s.addFlow.Active {
		copied := *s.addFlow
		addFlow = &copied
	}
	s.accountMu.Unlock()

	rawVault := s.currentVault()
	var vaultStatus credentials.VaultStatus
	if rawVault != nil {
		vaultStatus = rawVault.Status(ctx)
	}

	return AccountsState{
		VaultUnlocked:            unlocked,
		VaultOSKeychain:          vaultStatus.OSKeychain,
		VaultOSKeychainAvailable: vaultStatus.OSKeychainAvailable,
		VaultOSKeychainBackend:   vaultStatus.OSKeychainBackend,
		LegacyPasswordVault:      vaultStatus.LegacyPasswordVault,
		SignedIn:                 profile != nil,
		CurrentAccountID:         currentAccountID,
		CurrentProfile:           profile,
		Accounts:                 accounts,
		PendingSwitch:            pending,
		AddFlow:                  addFlow,
		CLIRunning:               s.detectRunningKimiCLI(ctx),
	}, nil
}

func (s *NativeService) resetAccountMetadata(ctx context.Context) error {
	s.accountsCancelPending()
	s.accountMu.Lock()
	s.addFlow = nil
	s.accountMu.Unlock()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM kimi_accounts`); err != nil {
		return err
	}
	s.bus.Publish("accounts", map[string]any{"type": "vault.reset"})
	return nil
}

func (s *NativeService) accountsResetVault(ctx context.Context, input map[string]any) (AccountsState, error) {
	confirm, _ := input["confirm"].(bool)
	if !confirm {
		return AccountsState{}, errors.New("重置凭据库需要明确确认")
	}
	rawVault := s.currentVault()
	if rawVault == nil {
		return AccountsState{}, errors.New("加密凭据库不可用")
	}
	if err := rawVault.Reset(ctx); err != nil {
		return AccountsState{}, err
	}
	return s.accountsList(ctx, nil)
}

func (s *NativeService) saveCurrentAccountLocked(ctx context.Context, labelOverride string) (AccountSummary, error) {
	vault := s.currentAccountVault()
	if vault == nil {
		return AccountSummary{}, errors.New("加密凭据库不可用")
	}
	if !vault.Unlocked() {
		return AccountSummary{}, errors.New("请先解锁加密凭据库后再保存账号")
	}
	oauthKey, oauthHost, baseURL, _, _ := s.readManagedProviderConfig(ctx)
	profile, status, msg := s.readOfficialUserInfo(ctx)
	if profile == nil {
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return AccountSummary{}, errors.New("当前账号登录已失效，请先在官方界面重新登录")
		}
		return AccountSummary{}, errors.New(firstNonEmpty(msg, "当前未登录 Kimi Code 订阅账号，请先登录"))
	}
	credPath, err := s.kimiCredentialPath(oauthKey)
	if err != nil {
		return AccountSummary{}, err
	}
	s.waitForOAuthRefreshLock(ctx, oauthKey)
	rawCred, err := os.ReadFile(credPath)
	if err != nil {
		return AccountSummary{}, errors.New("未读取到当前 Kimi Code 登录凭据文件，请确认已登录")
	}
	fields, err := parseRawTokenFields(rawCred)
	if err != nil {
		return AccountSummary{}, err
	}
	if tokenIsRevokedOrEmpty(fields) {
		return AccountSummary{}, errors.New("当前账号的登录令牌为空或已撤销，请先重新登录")
	}
	return s.upsertAccountWithSecret(ctx, profile, rawCred, oauthKey, oauthHost, baseURL, labelOverride)
}

func (s *NativeService) upsertAccountWithSecret(ctx context.Context, profile *AccountProfile, rawCred []byte, oauthKey, oauthHost, baseURL, labelOverride string) (AccountSummary, error) {
	vault := s.currentAccountVault()
	if vault == nil || !vault.Unlocked() {
		return AccountSummary{}, errors.New("请先解锁加密凭据库")
	}
	now := storage.SortableTime(time.Now().UTC())
	existing, lookupErr := s.loadAccountRowByUserID(ctx, profile.UserID)
	id := existing.ID
	label := existing.Label
	createdAt := existing.CreatedAt
	if errors.Is(lookupErr, sql.ErrNoRows) {
		id = "acc_" + security.Random()[:16]
		createdAt = now
	} else if lookupErr != nil {
		return AccountSummary{}, lookupErr
	}
	if trimmed := strings.TrimSpace(labelOverride); trimmed != "" {
		label = trimmed
	}
	if label == "" {
		label = firstNonEmpty(profile.Nickname, profile.UserID)
	}
	secretPayload, err := json.Marshal(storedKimiAccountSecret{
		RawCredential: json.RawMessage(rawCred),
		OAuthKey:      sanitizeOAuthKey(oauthKey),
		OAuthHost:     strings.TrimSpace(oauthHost),
		BaseURL:       strings.TrimRight(strings.TrimSpace(baseURL), "/"),
	})
	if err != nil {
		return AccountSummary{}, err
	}
	if err := vault.Put(ctx, accountVaultRef(id), secretPayload); err != nil {
		return AccountSummary{}, errors.New("加密保存账号凭据失败")
	}
	modelsJSON := existing.ModelsJSON
	if managedModels := s.snapshotManagedModels(ctx); len(managedModels) > 0 {
		if encoded, encErr := json.Marshal(managedModels); encErr == nil {
			modelsJSON = string(encoded)
		}
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO kimi_accounts(id,user_id,label,nickname,avatar,plan,region,oauth_key,oauth_host,base_url,status,status_reason,quota_json,models_json,last_seen_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET
			label=excluded.label,
			nickname=excluded.nickname,
			avatar=excluded.avatar,
			plan=excluded.plan,
			region=excluded.region,
			oauth_key=excluded.oauth_key,
			oauth_host=excluded.oauth_host,
			base_url=excluded.base_url,
			status=?,
			status_reason='',
			models_json=CASE WHEN excluded.models_json != '' THEN excluded.models_json ELSE kimi_accounts.models_json END,
			last_seen_at=excluded.last_seen_at,
			updated_at=excluded.updated_at`,
		id, profile.UserID, label, profile.Nickname, profile.Avatar, profile.Plan, profile.Region,
		sanitizeOAuthKey(oauthKey), strings.TrimSpace(oauthHost), strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		accountStatusReady, "", existing.QuotaJSON, modelsJSON, now, createdAt, now,
		accountStatusReady,
	)
	if err != nil {
		return AccountSummary{}, err
	}
	row, err := s.loadAccountRow(ctx, id)
	if err != nil {
		return AccountSummary{}, err
	}
	summary := s.accountRowToSummary(ctx, row, profile.UserID)
	s.bus.Publish("accounts", map[string]any{"type": "account.saved", "accountId": summary.ID})
	return summary, nil
}

func (s *NativeService) accountsSaveCurrent(ctx context.Context, input map[string]any) (AccountSummary, error) {
	s.switchMu.Lock()
	defer s.switchMu.Unlock()
	return s.saveCurrentAccountLocked(ctx, stringInput(input, "label"))
}

func (s *NativeService) accountsBeginAdd(ctx context.Context, input map[string]any) (map[string]any, error) {
	s.switchMu.Lock()
	defer s.switchMu.Unlock()
	vault := s.currentAccountVault()
	if vault == nil || !vault.Unlocked() {
		return nil, errors.New("请先解锁加密凭据库")
	}
	reloginID := stringInput(input, "reloginAccountId")
	mode := "add"
	if reloginID != "" {
		mode = "relogin"
	}
	var savedCurrent *AccountSummary
	previousUserID := ""
	if profile, _, _ := s.readOfficialUserInfo(ctx); profile != nil {
		previousUserID = profile.UserID
		if saved, err := s.saveCurrentAccountLocked(ctx, ""); err == nil {
			savedCurrent = &saved
		}
	}
	flow := &AccountAddFlow{
		Active:           true,
		Mode:             mode,
		PreviousUserID:   previousUserID,
		ReloginAccountID: reloginID,
		StartedAt:        storage.SortableTime(time.Now().UTC()),
	}
	s.accountMu.Lock()
	s.addFlow = flow
	s.accountMu.Unlock()
	s.bus.Publish("accounts", map[string]any{"type": "account.add_started", "mode": mode})
	return map[string]any{
		"addFlow":      flow,
		"savedCurrent": savedCurrent,
	}, nil
}

func (s *NativeService) accountsCompleteAdd(ctx context.Context, input map[string]any) (map[string]any, error) {
	s.switchMu.Lock()
	defer s.switchMu.Unlock()
	if cancel, _ := input["cancel"].(bool); cancel {
		s.accountMu.Lock()
		s.addFlow = nil
		s.accountMu.Unlock()
		return map[string]any{"completed": false, "cancelled": true}, nil
	}
	profile, _, _ := s.readOfficialUserInfo(ctx)
	if profile == nil {
		return map[string]any{"completed": false, "reason": "尚未检测到新的已登录账号"}, nil
	}
	s.accountMu.Lock()
	flow := s.addFlow
	s.accountMu.Unlock()
	if flow != nil && flow.Active && flow.Mode == "add" && flow.PreviousUserID != "" && profile.UserID == flow.PreviousUserID {
		return map[string]any{"completed": false, "reason": "当前仍是原账号，请先在下方退出并登录另一个账号"}, nil
	}
	saved, err := s.saveCurrentAccountLocked(ctx, stringInput(input, "label"))
	if err != nil {
		return nil, err
	}
	s.accountMu.Lock()
	s.addFlow = nil
	s.accountMu.Unlock()
	return map[string]any{"completed": true, "account": saved}, nil
}

func (s *NativeService) accountsRename(ctx context.Context, input map[string]any) (AccountSummary, error) {
	id := stringInput(input, "id")
	label := strings.TrimSpace(stringInput(input, "label"))
	if id == "" || label == "" {
		return AccountSummary{}, errors.New("账号 ID 和新名称不能为空")
	}
	if len(label) > 80 {
		return AccountSummary{}, errors.New("账号名称不能超过 80 个字符")
	}
	now := storage.SortableTime(time.Now().UTC())
	res, err := s.db.ExecContext(ctx, `UPDATE kimi_accounts SET label=?, updated_at=? WHERE id=?`, label, now, id)
	if err != nil {
		return AccountSummary{}, err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return AccountSummary{}, errors.New("未找到该账号")
	}
	row, err := s.loadAccountRow(ctx, id)
	if err != nil {
		return AccountSummary{}, err
	}
	profile, _, _ := s.readOfficialUserInfo(ctx)
	uid := ""
	if profile != nil {
		uid = profile.UserID
	}
	summary := s.accountRowToSummary(ctx, row, uid)
	s.bus.Publish("accounts", map[string]any{"type": "account.renamed", "accountId": id})
	return summary, nil
}

func (s *NativeService) accountsDelete(ctx context.Context, input map[string]any) (map[string]any, error) {
	s.switchMu.Lock()
	defer s.switchMu.Unlock()
	id := stringInput(input, "id")
	if id == "" {
		return nil, errors.New("缺少账号 ID")
	}
	vault := s.currentAccountVault()
	if vault == nil || !vault.Unlocked() {
		return nil, errors.New("请先解锁加密凭据库后再删除账号")
	}
	if _, err := s.loadAccountRow(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("未找到该账号")
		}
		return nil, err
	}
	if err := vault.Delete(ctx, accountVaultRef(id)); err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM kimi_accounts WHERE id=?`, id); err != nil {
		return nil, err
	}
	s.cancelPendingSwitchIfTarget(id)
	s.bus.Publish("accounts", map[string]any{"type": "account.deleted", "accountId": id})
	return map[string]any{"deleted": true, "id": id}, nil
}

func (s *NativeService) markAccountStatus(ctx context.Context, id, status, reason string) {
	now := storage.SortableTime(time.Now().UTC())
	_, _ = s.db.ExecContext(ctx, `UPDATE kimi_accounts SET status=?, status_reason=?, updated_at=? WHERE id=?`, status, reason, now, id)
}

func (s *NativeService) saveAccountQuota(ctx context.Context, id string, summary AccountQuotaSummary) {
	encoded, err := json.Marshal(summary)
	if err != nil {
		return
	}
	now := storage.SortableTime(time.Now().UTC())
	_, _ = s.db.ExecContext(ctx, `UPDATE kimi_accounts SET quota_json=?, updated_at=? WHERE id=?`, string(encoded), now, id)
}

func (s *NativeService) accountsRefreshQuotas(ctx context.Context, currentUserID string) (map[string]AccountQuotaSummary, error) {
	vault := s.currentAccountVault()
	if vault == nil || !vault.Unlocked() {
		return nil, errors.New("请先解锁加密凭据库")
	}
	if currentUserID == "" {
		if profile, _, _ := s.readOfficialUserInfo(ctx); profile != nil {
			currentUserID = profile.UserID
		}
	}
	rows, err := s.loadAccountRows(ctx)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 12 * time.Second}
	if s.quota != nil && s.quota.httpClient != nil {
		client = s.quota.httpClient
	}
	now := time.Now().UTC()
	nowStr := storage.SortableTime(now)
	results := map[string]AccountQuotaSummary{}
	for _, row := range rows {
		if currentUserID != "" && row.UserID == currentUserID {
			continue
		}
		rawSecret, getErr := vault.Get(ctx, accountVaultRef(row.ID))
		if getErr != nil {
			summary := AccountQuotaSummary{Status: "unknown", CheckedAt: nowStr, Error: "凭据缺失"}
			s.saveAccountQuota(ctx, row.ID, summary)
			results[row.ID] = summary
			continue
		}
		var secret storedKimiAccountSecret
		if json.Unmarshal(rawSecret, &secret) != nil {
			summary := AccountQuotaSummary{Status: "unknown", CheckedAt: nowStr, Error: "凭据格式无法识别"}
			s.saveAccountQuota(ctx, row.ID, summary)
			results[row.ID] = summary
			continue
		}
		fields, parseErr := parseRawTokenFields(secret.RawCredential)
		if parseErr != nil || tokenIsRevokedOrEmpty(fields) {
			s.markAccountStatus(ctx, row.ID, accountStatusReloginRequired, "令牌已失效，需要重新登录")
			summary := AccountQuotaSummary{Status: "auth_rejected", CheckedAt: nowStr, Error: "需要重新登录"}
			s.saveAccountQuota(ctx, row.ID, summary)
			results[row.ID] = summary
			continue
		}
		if fields.ExpiresAt > 0 && now.Unix() >= fields.ExpiresAt-60 {
			// Access token aged out while sitting in the vault, while refresh_token is still present.
			// Keep status ready so switching lets official Ee.ensureFresh() rotate it cleanly; report quota as unknown (never 0%).
			summary := AccountQuotaSummary{Status: "unknown", CheckedAt: nowStr, Error: "令牌已过期，切过去后自动刷新"}
			s.saveAccountQuota(ctx, row.ID, summary)
			results[row.ID] = summary
			continue
		}
		endpoint := strings.TrimSpace(s.codeUsageEndpointOverride)
		if endpoint == "" {
			base := firstNonEmpty(secret.BaseURL, row.BaseURL, defaultManagedBaseURL)
			endpoint = strings.TrimRight(base, "/") + "/usages"
		}
		payload, code, _ := quota.FetchCodeUsageWithToken(ctx, client, endpoint, fields.AccessToken)
		if code == "auth_rejected" {
			s.markAccountStatus(ctx, row.ID, accountStatusReloginRequired, "上游拒绝了该账号令牌，需要重新登录")
			summary := AccountQuotaSummary{Status: "auth_rejected", CheckedAt: nowStr, Error: "需要重新登录"}
			s.saveAccountQuota(ctx, row.ID, summary)
			results[row.ID] = summary
			continue
		}
		if code != "" {
			summary := AccountQuotaSummary{Status: "unknown", CheckedAt: nowStr, Error: "读取失败（" + code + "）"}
			s.saveAccountQuota(ctx, row.ID, summary)
			results[row.ID] = summary
			continue
		}
		summary := AccountQuotaSummary{Status: "ok", CheckedAt: nowStr}
		for _, w := range payload.Windows {
			v := w.UsedPercent
			switch w.Key {
			case "limit_5h":
				summary.Limit5hUsed = &v
				summary.Limit5hReset = w.ResetAt
			case "limit_7d":
				summary.Limit7dUsed = &v
				summary.Limit7dReset = w.ResetAt
			}
		}
		if summary.Limit5hUsed == nil && summary.Limit7dUsed == nil {
			summary.Status = "unknown"
			summary.Error = "未返回 5 小时或每周窗口"
		} else if row.Status == accountStatusReloginRequired {
			s.markAccountStatus(ctx, row.ID, accountStatusReady, "")
		}
		s.saveAccountQuota(ctx, row.ID, summary)
		results[row.ID] = summary
	}
	return results, nil
}

func (s *NativeService) findBusySessions(ctx context.Context) ([]AccountBusySession, []NativeSession, error) {
	sessions, err := s.kap.Sessions(ctx)
	if err != nil {
		return nil, nil, err
	}
	busy := []AccountBusySession{}
	for _, session := range sessions {
		reasons := []string{}
		status := strings.ToLower(strings.TrimSpace(session.Status))
		if session.MainTurnActive {
			reasons = append(reasons, "正在运行回合")
		} else if status == "background" {
			reasons = append(reasons, "有后台子代理正在运行")
		} else if session.Busy {
			reasons = append(reasons, "正在运行回合")
		}
		if session.PendingInteraction != "" && session.PendingInteraction != "none" {
			reasons = append(reasons, "有待批准或待回答操作")
		}
		if snap, ok := s.automationSnapshot(session.ID); ok && snap.approvals > 0 {
			reasons = append(reasons, "有待批准操作")
		}
		// Trust an explicit live "idle" activity from the official server over the
		// offline wire.jsonl counter: after a client exit or restart, a lost
		// background task may not have a task.terminated record on disk yet.
		if status != "idle" && status != "background" && s.openBackground(ctx, session.ID) > 0 {
			reasons = append(reasons, "有后台子代理尚未返回")
		}
		if len(reasons) > 0 {
			busy = append(busy, AccountBusySession{
				ID:     session.ID,
				Title:  firstNonEmpty(session.Title, session.ID),
				Reason: strings.Join(uniqueStrings(reasons), "、"),
			})
		}
	}
	return busy, sessions, nil
}

func uniqueStrings(items []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	return out
}

func (s *NativeService) readActiveManagedModel(ctx context.Context, requestedModel string) string {
	if trimmed := strings.TrimSpace(requestedModel); trimmed != "" {
		return trimmed
	}
	raw, err := s.kap.Request(ctx, http.MethodGet, "/config", nil)
	if err != nil {
		return ""
	}
	var cfg map[string]any
	if json.Unmarshal(raw, &cfg) != nil {
		return ""
	}
	if model, _ := cfg["defaultModel"].(string); strings.TrimSpace(model) != "" {
		return strings.TrimSpace(model)
	}
	if model, _ := cfg["default_model"].(string); strings.TrimSpace(model) != "" {
		return strings.TrimSpace(model)
	}
	return ""
}

func isManagedKimiModel(modelID string) bool {
	trimmed := strings.TrimSpace(modelID)
	return strings.HasPrefix(trimmed, "kimi-code/") || strings.HasPrefix(trimmed, defaultManagedProviderID+"/")
}

func normalizeManagedModelKey(modelID string) string {
	trimmed := strings.TrimSpace(modelID)
	trimmed = strings.TrimPrefix(trimmed, defaultManagedProviderID+"/")
	trimmed = strings.TrimPrefix(trimmed, "kimi-code/")
	return trimmed
}

func (s *NativeService) snapshotManagedModels(ctx context.Context) []string {
	if s.kap == nil {
		return nil
	}
	models, err := s.kap.Models(ctx)
	if err != nil || len(models) == 0 {
		return nil
	}
	out := make([]string, 0, len(models))
	seen := map[string]bool{}
	for _, m := range models {
		if m.Provider == defaultManagedProviderID || m.Provider == "kimi-code" || isManagedKimiModel(m.ID) {
			key := normalizeManagedModelKey(m.ID)
			if key != "" && !seen[key] {
				seen[key] = true
				out = append(out, key)
			}
		}
	}
	return out
}

func parseStoredModelList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	if json.Unmarshal([]byte(raw), &out) != nil {
		return nil
	}
	return out
}

func modelListContains(models []string, activeModel string) bool {
	want := normalizeManagedModelKey(activeModel)
	if want == "" {
		return true
	}
	for _, m := range models {
		if strings.EqualFold(normalizeManagedModelKey(m), want) {
			return true
		}
	}
	return false
}

func (s *NativeService) accountsSwitchPreflight(ctx context.Context, input map[string]any) (AccountSwitchPreflight, error) {
	targetID := stringInput(input, "id")
	if targetID == "" {
		return AccountSwitchPreflight{}, errors.New("缺少目标账号 ID")
	}
	row, err := s.loadAccountRow(ctx, targetID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AccountSwitchPreflight{}, errors.New("未找到目标账号")
		}
		return AccountSwitchPreflight{}, err
	}
	vault := s.currentAccountVault()
	unlocked := vault != nil && vault.Unlocked()

	profile, _, _ := s.readOfficialUserInfo(ctx)
	currentUserID := ""
	if profile != nil {
		currentUserID = profile.UserID
	}

	requiresLogin := row.Status == accountStatusReloginRequired
	if unlocked && !requiresLogin {
		rawSecret, getErr := vault.Get(ctx, accountVaultRef(row.ID))
		if getErr != nil {
			requiresLogin = true
		} else {
			var secret storedKimiAccountSecret
			if json.Unmarshal(rawSecret, &secret) != nil {
				requiresLogin = true
			} else if fields, parseErr := parseRawTokenFields(secret.RawCredential); parseErr != nil || tokenIsRevokedOrEmpty(fields) {
				requiresLogin = true
				s.markAccountStatus(ctx, row.ID, accountStatusReloginRequired, "令牌已失效，需要重新登录")
				row.Status = accountStatusReloginRequired
				row.StatusReason = "令牌已失效，需要重新登录"
			}
		}
	}

	busySessions, _, err := s.findBusySessions(ctx)
	if err != nil {
		return AccountSwitchPreflight{}, err
	}

	warnings := []string{
		"切换后各会话的第一次请求不会命中提示词缓存（缓存按账号计算）。",
	}
	cliRunning := s.detectRunningKimiCLI(ctx)
	if cliRunning {
		warnings = append(warnings, "检测到本机有正在运行的 kimi 命令行进程，切换后它也会跟着切到新账号。")
	}

	activeModel := s.readActiveManagedModel(ctx, stringInput(input, "activeModel"))
	modelMayDiffer := false
	if activeModel != "" && isManagedKimiModel(activeModel) {
		knownModels := parseStoredModelList(row.ModelsJSON)
		if len(knownModels) > 0 {
			if !modelListContains(knownModels, activeModel) {
				modelMayDiffer = true
				warnings = append(warnings, fmt.Sprintf("当前会话使用的模型（%s）不在目标账号已记录的可用模型列表中，切换前请确认。", activeModel))
			}
		} else {
			currentPlan := ""
			if profile != nil {
				currentPlan = strings.TrimSpace(profile.Plan)
			}
			targetPlan := strings.TrimSpace(row.Plan)
			if targetPlan != "" && currentPlan != "" && !strings.EqualFold(targetPlan, currentPlan) {
				modelMayDiffer = true
				warnings = append(warnings, fmt.Sprintf("当前会话使用的模型（%s）可能不在目标账号套餐（%s）的可用范围内，切换前请确认。", activeModel, targetPlan))
			} else if strings.Contains(strings.ToLower(targetPlan), "free") || strings.Contains(targetPlan, "免费") {
				modelMayDiffer = true
				warnings = append(warnings, fmt.Sprintf("目标账号当前套餐为「%s」，当前模型（%s）可能不可用，切换前请确认。", targetPlan, activeModel))
			}
		}
	}

	summary := s.accountRowToSummary(ctx, row, currentUserID)
	canForceSwitch := unlocked && !requiresLogin && !summary.Current
	canSwitch := canForceSwitch && len(busySessions) == 0
	return AccountSwitchPreflight{
		Target:         summary,
		VaultUnlocked:  unlocked,
		CanSwitch:      canSwitch,
		CanForceSwitch: canForceSwitch,
		RequiresLogin:  requiresLogin,
		BusySessions:   busySessions,
		Warnings:       warnings,
		CLIRunning:     cliRunning,
		ActiveModel:    activeModel,
		ModelMayDiffer: modelMayDiffer,
	}, nil
}

func (s *NativeService) currentHostPID() int {
	if s.hostPIDProvider != nil {
		return s.hostPIDProvider()
	}
	if !hostPlatformSupported {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	status := s.attachStatus(ctx)
	host, _ := status["host"].(map[string]any)
	switch v := host["pid"].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func (s *NativeService) accountsSwitch(ctx context.Context, input map[string]any) (AccountSwitchResult, error) {
	targetID := stringInput(input, "id")
	if targetID == "" {
		return AccountSwitchResult{}, errors.New("缺少目标账号 ID")
	}
	waitUntilIdle, _ := input["waitUntilIdle"].(bool)
	force, _ := input["force"].(bool)
	expectedPID := intInput(input, "expectedPid", 0)
	if expectedPID <= 0 {
		expectedPID = s.currentHostPID()
	}
	return s.executeAccountSwitch(ctx, targetID, waitUntilIdle, force, expectedPID, 0)
}

func (s *NativeService) executeAccountSwitch(ctx context.Context, targetID string, waitUntilIdle, force bool, expectedPID int, queuedSeq uint64) (AccountSwitchResult, error) {
	s.switchMu.Lock()
	defer s.switchMu.Unlock()

	queuedCancel, err := s.beginQueuedSwitch(ctx, queuedSeq, targetID)
	if err != nil {
		return AccountSwitchResult{}, err
	}
	if queuedCancel != nil {
		defer queuedCancel()
	}

	vault := s.currentAccountVault()
	if vault == nil {
		return AccountSwitchResult{}, errors.New("加密凭据库不可用")
	}
	if !vault.Unlocked() {
		return AccountSwitchResult{}, errors.New("请先解锁加密凭据库后再切换账号")
	}

	targetRow, err := s.loadAccountRow(ctx, targetID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AccountSwitchResult{}, errors.New("未找到目标账号")
		}
		return AccountSwitchResult{}, err
	}
	if targetRow.Status == accountStatusReloginRequired {
		return AccountSwitchResult{}, &NativeError{Code: "ACCOUNT_RELOGIN_REQUIRED", Message: "目标账号的令牌已失效，请先重新登录"}
	}
	rawTargetSecret, err := vault.Get(ctx, accountVaultRef(targetRow.ID))
	if err != nil {
		s.markAccountStatus(ctx, targetRow.ID, accountStatusReloginRequired, "未找到该账号的加密凭据，需要重新登录")
		return AccountSwitchResult{}, &NativeError{Code: "ACCOUNT_RELOGIN_REQUIRED", Message: "未找到目标账号凭据，请先重新登录"}
	}
	var targetSecret storedKimiAccountSecret
	if err := json.Unmarshal(rawTargetSecret, &targetSecret); err != nil {
		return AccountSwitchResult{}, errors.New("目标账号凭据格式损坏")
	}
	targetFields, err := parseRawTokenFields(targetSecret.RawCredential)
	if err != nil || tokenIsRevokedOrEmpty(targetFields) {
		s.markAccountStatus(ctx, targetRow.ID, accountStatusReloginRequired, "目标账号令牌已失效，需要重新登录")
		return AccountSwitchResult{}, &NativeError{Code: "ACCOUNT_RELOGIN_REQUIRED", Message: "目标账号令牌已失效，请先重新登录"}
	}

	// Step 1: Busy check across all sessions (running turn, background subagents, pending approvals).
	busySessions, sessionsBefore, err := s.findBusySessions(ctx)
	if err != nil {
		return AccountSwitchResult{}, fmt.Errorf("切换前检查会话状态失败：%w", err)
	}
	forcedWhileBusy := false
	if len(busySessions) > 0 {
		if force {
			forcedWhileBusy = true
		} else {
			names := make([]string, 0, len(busySessions))
			ids := make([]string, 0, len(busySessions))
			for _, item := range busySessions {
				names = append(names, fmt.Sprintf("「%s」（%s）", item.Title, item.Reason))
				ids = append(ids, item.ID)
			}
			if waitUntilIdle {
				queued := s.queuePendingAccountSwitch(targetRow, ids, expectedPID)
				return AccountSwitchResult{
					Switched:      false,
					Queued:        true,
					PendingSwitch: &queued,
					RefreshAuth:   false,
				}, nil
			}
			return AccountSwitchResult{}, &NativeError{
				Code:    "ACCOUNT_SWITCH_BUSY",
				Message: "以下会话仍在运行，默认已暂停切换（可选择等待空闲或强制立即切换）：" + strings.Join(names, "、"),
			}
		}
	}

	// Clear any previous pending switch now that we are switching. A queued
	// switch already cleared its own slot in beginQueuedSwitch, and canceling
	// its watch context here would abort the switch that is now running.
	if queuedSeq == 0 {
		s.cancelPendingSwitchLocked()
	}

	// Step 2: Pause keepalive (N4) and scheduler (N5) during the switch.
	resumeAutomation := s.pauseForAccountSwitch()
	defer resumeAutomation()

	// Step 3: Re-snapshot outgoing current account's latest kimi-code.json (tokens may have rotated).
	currentOAuthKey, currentOAuthHost, currentBaseURL, providerExists, configErr := s.readManagedProviderConfig(ctx)
	targetOAuthKey := firstNonEmpty(targetSecret.OAuthKey, targetRow.OAuthKey, currentOAuthKey, defaultManagedOAuthKey)
	targetCredPath, err := s.kimiCredentialPath(targetOAuthKey)
	if err != nil {
		return AccountSwitchResult{}, err
	}
	backupPath := targetCredPath + ".ekd-bak"
	backupExistsErr := fmt.Errorf("切换已停止，请先处理留存的恢复备份：%s", backupPath)
	if _, err := os.Lstat(backupPath); err == nil {
		return AccountSwitchResult{}, backupExistsErr
	} else if !errors.Is(err, os.ErrNotExist) {
		return AccountSwitchResult{}, fmt.Errorf("无法检查恢复备份 %s：%w", backupPath, err)
	}
	if configErr != nil {
		return AccountSwitchResult{}, fmt.Errorf("切换前无法读取官方供应商配置：%w", configErr)
	}
	currentProfile, _, _ := s.readOfficialUserInfo(ctx)
	s.waitForOAuthRefreshLock(ctx, targetOAuthKey)

	// Each OAuth key owns a separate credential file. The outgoing account must be
	// re-snapshotted from its own file; the target path can already hold a stale
	// token for a different account and must never be written into currentProfile.
	keysDiffer := sanitizeOAuthKey(currentOAuthKey) != sanitizeOAuthKey(targetOAuthKey)
	resnapshotCurrent := providerExists && strings.TrimSpace(currentOAuthKey) != ""
	var currentCredPath string
	if resnapshotCurrent {
		currentCredPath, err = s.kimiCredentialPath(currentOAuthKey)
		if err != nil {
			return AccountSwitchResult{}, err
		}
		if keysDiffer {
			s.waitForOAuthRefreshLock(ctx, currentOAuthKey)
		}
	}

	targetBackupBytes, readErr := os.ReadFile(targetCredPath)
	hadTargetCredential := readErr == nil
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return AccountSwitchResult{}, fmt.Errorf("无法读取目标凭据文件：%w", readErr)
	}

	// Step 4: Reserve the recovery backup before changing credentials or configuration.
	backupData := targetBackupBytes
	if !hadTargetCredential {
		backupData = []byte("EKD: no credential file existed before the switch\n")
	}
	if err := writeAccountSwitchBackup(backupPath, backupData); err != nil {
		if errors.Is(err, os.ErrExist) {
			return AccountSwitchResult{}, backupExistsErr
		}
		return AccountSwitchResult{}, fmt.Errorf("写入恢复备份 %s 失败：%w", backupPath, err)
	}
	removeBackup := func() error {
		if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("无法删除恢复备份 %s：%w", backupPath, err)
		}
		return nil
	}
	var currentRecordSnapshot *accountRecordSnapshot
	autoSavedCurrentID := ""
	targetCredentialWritten := false
	configPatchAttempted := false
	var previousProvider map[string]any
	rollback := func(cause error) (AccountSwitchResult, error) {
		var credentialErr, recordErr, configErr error
		if targetCredentialWritten {
			if hadTargetCredential {
				credentialErr = writeFileAtomic0600(targetCredPath, targetBackupBytes)
			} else if err := os.Remove(targetCredPath); !errors.Is(err, os.ErrNotExist) {
				credentialErr = err
			}
			if credentialErr != nil {
				credentialErr = fmt.Errorf("恢复凭据文件失败：%w", credentialErr)
			}
		}
		rbCtx, rbCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer rbCancel()
		if err := s.restoreAccountRecord(rbCtx, currentRecordSnapshot); err != nil {
			recordErr = fmt.Errorf("恢复当前账号凭据库记录失败：%w", err)
		}
		if autoSavedCurrentID != "" {
			if err := s.deleteAccountRecord(rbCtx, autoSavedCurrentID); err != nil {
				recordErr = errors.Join(recordErr, fmt.Errorf("清理切换前临时保存的当前账号失败：%w", err))
			}
		}
		if configPatchAttempted {
			var restoreProvider any
			if previousProvider != nil {
				restoreProvider = previousProvider
			}
			_, configErr = s.kap.Request(rbCtx, http.MethodPost, "/config", map[string]any{
				"providers": map[string]any{defaultManagedProviderID: restoreProvider},
			})
			if configErr != nil {
				configErr = fmt.Errorf("恢复官方供应商配置失败：%w", configErr)
			}
		}
		if err := errors.Join(credentialErr, recordErr, configErr); err != nil {
			return AccountSwitchResult{}, fmt.Errorf("切换失败，且未能完全恢复原账号，备份保留在 %s：%w", backupPath, errors.Join(cause, err))
		}
		return AccountSwitchResult{}, errors.Join(fmt.Errorf("%w，已自动恢复原账号", cause), removeBackup())
	}

	if resnapshotCurrent && currentProfile != nil {
		currentBytes, currentReadErr := os.ReadFile(currentCredPath)
		if currentReadErr != nil && !errors.Is(currentReadErr, os.ErrNotExist) {
			return AccountSwitchResult{}, errors.Join(fmt.Errorf("无法读取当前账号凭据文件：%w", currentReadErr), removeBackup())
		}
		if currentReadErr == nil {
			if fields, parseErr := parseRawTokenFields(currentBytes); parseErr == nil && !tokenIsRevokedOrEmpty(fields) {
				existingRow, findErr := s.loadAccountRowByUserID(ctx, currentProfile.UserID)
				if findErr != nil && !errors.Is(findErr, sql.ErrNoRows) {
					return AccountSwitchResult{}, errors.Join(fmt.Errorf("切换前查询当前账号记录失败：%w", findErr), removeBackup())
				}
				if findErr == nil {
					snapshot, snapErr := s.snapshotAccountRecord(ctx, currentProfile.UserID)
					if snapErr != nil {
						return AccountSwitchResult{}, errors.Join(fmt.Errorf("切换前无法保存当前账号凭据快照：%w", snapErr), removeBackup())
					}
					currentRecordSnapshot = snapshot
					if _, upsertErr := s.upsertAccountWithSecret(ctx, currentProfile, currentBytes, currentOAuthKey, currentOAuthHost, currentBaseURL, existingRow.Label); upsertErr != nil {
						return rollback(fmt.Errorf("切换前更新当前账号凭据快照失败：%w", upsertErr))
					}
				} else {
					// The user signed in to a new account but never pressed save.
					// Overwriting the target credential path would silently lose
					// this account's OAuth token, so persist it first; rollback
					// removes the record when the switch fails.
					label := firstNonEmpty(strings.TrimSpace(currentProfile.Nickname), strings.TrimSpace(currentProfile.Email), currentProfile.UserID)
					summary, upsertErr := s.upsertAccountWithSecret(ctx, currentProfile, currentBytes, currentOAuthKey, currentOAuthHost, currentBaseURL, label)
					if upsertErr != nil {
						return rollback(fmt.Errorf("切换前保存当前未保存账号失败：%w", upsertErr))
					}
					autoSavedCurrentID = summary.ID
				}
			}
		}
	}

	targetCredentialWritten = true
	if err := s.writeCredentialFile(targetCredPath, targetSecret.RawCredential); err != nil {
		return rollback(fmt.Errorf("写入目标账号凭据失败：%w", err))
	}

	// If managed:kimi-code provider was absent (signed-out state), provision it so /auth and /oauth/userinfo recognize the credential.
	// When it exists but points at another OAuth key, move that pointer to the
	// target file; otherwise the client would keep reading the outgoing account.
	switch {
	case !providerExists:
		oauthObj := map[string]any{
			"storage": "file",
			"key":     "oauth/" + sanitizeOAuthKey(targetOAuthKey),
		}
		if h := firstNonEmpty(targetSecret.OAuthHost, targetRow.OAuthHost); h != "" {
			oauthObj["oauthHost"] = h
		}
		patch := map[string]any{
			"providers": map[string]any{
				defaultManagedProviderID: map[string]any{
					"type":    "kimi",
					"baseUrl": firstNonEmpty(targetSecret.BaseURL, targetRow.BaseURL, defaultManagedBaseURL),
					"apiKey":  "",
					"oauth":   oauthObj,
				},
			},
		}
		// An error response can arrive after the configuration change was applied.
		configPatchAttempted = true
		if _, err := s.kap.Request(ctx, http.MethodPost, "/config", patch); err != nil {
			return rollback(fmt.Errorf("补回官方账号供应商配置失败：%w", err))
		}
	case keysDiffer:
		currentProvider, ok, err := s.readManagedProviderObject(ctx)
		if err != nil {
			return rollback(fmt.Errorf("读取官方供应商配置失败：%w", err))
		}
		if !ok {
			return rollback(errors.New("切换期间官方供应商配置发生变化"))
		}
		previousProvider = currentProvider
		updatedProvider := make(map[string]any, len(currentProvider))
		for key, value := range currentProvider {
			updatedProvider[key] = value
		}
		oauthObj := map[string]any{}
		if existing, ok := currentProvider["oauth"].(map[string]any); ok {
			for key, value := range existing {
				oauthObj[key] = value
			}
		}
		updatedProvider["baseUrl"] = firstNonEmpty(targetSecret.BaseURL, targetRow.BaseURL, defaultManagedBaseURL)
		delete(updatedProvider, "base_url")
		oauthObj["key"] = "oauth/" + sanitizeOAuthKey(targetOAuthKey)
		if _, ok := oauthObj["storage"]; !ok {
			oauthObj["storage"] = "file"
		}
		delete(oauthObj, "host")
		delete(oauthObj, "oauth_host")
		if h := firstNonEmpty(targetSecret.OAuthHost, targetRow.OAuthHost); h != "" {
			oauthObj["oauthHost"] = h
		} else {
			delete(oauthObj, "oauthHost")
		}
		updatedProvider["oauth"] = oauthObj
		// An error response can arrive after the configuration change was applied.
		configPatchAttempted = true
		if _, err := s.kap.Request(ctx, http.MethodPost, "/config", map[string]any{
			"providers": map[string]any{defaultManagedProviderID: updatedProvider},
		}); err != nil {
			return rollback(fmt.Errorf("同步官方账号凭据路径失败：%w", err))
		}
	}

	// Step 5: Verify /oauth/userinfo matches target, models readable, host PID unchanged, sessions intact.
	verifiedProfile, verifyStatus, verifyMsg := s.readOfficialUserInfo(ctx)
	if verifiedProfile == nil {
		if verifyStatus == http.StatusUnauthorized || verifyStatus == http.StatusForbidden {
			result, err := rollback(&NativeError{Code: "ACCOUNT_RELOGIN_REQUIRED", Message: "目标账号令牌已失效，请重新登录目标账号"})
			s.markAccountStatus(ctx, targetRow.ID, accountStatusReloginRequired, "目标账号令牌已被官方拒绝，需要重新登录")
			return result, err
		}
		return rollback(fmt.Errorf("切换后身份校验失败（%s）", firstNonEmpty(verifyMsg, "无法读取用户信息")))
	}
	if verifiedProfile.UserID != targetRow.UserID {
		return rollback(fmt.Errorf("切换后身份不匹配（返回 %s，期望 %s）", verifiedProfile.UserID, targetRow.UserID))
	}
	if _, err := s.kap.Models(ctx); err != nil {
		return rollback(fmt.Errorf("切换后读取模型列表失败：%w", err))
	}
	sessionsAfter, err := s.kap.Sessions(ctx)
	if err != nil {
		return rollback(fmt.Errorf("切换后读取会话列表失败：%w", err))
	}
	if !sessionsPreserved(sessionsBefore, sessionsAfter) {
		return rollback(errors.New("切换后会话列表数量或条目发生异常变化"))
	}
	afterPID := expectedPID
	if expectedPID > 0 {
		if measured := s.currentHostPID(); measured > 0 {
			afterPID = measured
			if measured != expectedPID {
				return rollback(fmt.Errorf("客户端进程号发生变化（%d -> %d）", expectedPID, measured))
			}
		}
	}

	warnings := []string{"切换后各会话的第一次请求不会命中提示词缓存（缓存按账号计算）。"}
	if forcedWhileBusy {
		warnings = append(warnings, fmt.Sprintf("本次在 %d 个会话运行期间强制切换了账号，这些会话的后续请求将改用新账号。", len(busySessions)))
	}
	// Capture refreshed disk token if official Ee.ensureFresh() rotated it during /oauth/userinfo.
	if refreshedBytes, readErr := os.ReadFile(targetCredPath); readErr == nil {
		if fields, parseErr := parseRawTokenFields(refreshedBytes); parseErr == nil && !tokenIsRevokedOrEmpty(fields) {
			if _, upsertErr := s.upsertAccountWithSecret(ctx, verifiedProfile, refreshedBytes, targetOAuthKey, firstNonEmpty(targetSecret.OAuthHost, targetRow.OAuthHost), firstNonEmpty(targetSecret.BaseURL, targetRow.BaseURL, defaultManagedBaseURL), targetRow.Label); upsertErr != nil {
				warnings = append(warnings, fmt.Sprintf("切换已生效，但同步目标账号最新令牌到凭据库失败：%v", upsertErr))
			}
		}
	}

	updatedRow, err := s.loadAccountRow(ctx, targetRow.ID)
	if err != nil {
		updatedRow = targetRow
	}
	summary := s.accountRowToSummary(ctx, updatedRow, verifiedProfile.UserID)
	var backupCleanupWarning string
	if err := removeBackup(); err != nil {
		backupCleanupWarning = err.Error()
		warnings = append(warnings, backupCleanupWarning)
	}
	if s.detectRunningKimiCLI(ctx) {
		warnings = append(warnings, "检测到本机有正在运行的 kimi 命令行进程，已同步切到新账号。")
	}
	s.bus.Publish("accounts", map[string]any{
		"type":                 "account.switched",
		"accountId":            summary.ID,
		"userId":               summary.UserID,
		"refreshAuth":          true,
		"backupCleanupWarning": backupCleanupWarning,
	})
	return AccountSwitchResult{
		Switched:             true,
		Account:              &summary,
		SessionsCount:        len(sessionsAfter),
		HostPID:              afterPID,
		Warnings:             warnings,
		BackupCleanupWarning: backupCleanupWarning,
		RefreshAuth:          true,
	}, nil
}

func sessionsPreserved(before, after []NativeSession) bool {
	if len(after) < len(before) {
		return false
	}
	present := make(map[string]bool, len(after))
	for _, item := range after {
		present[item.ID] = true
	}
	for _, item := range before {
		if !present[item.ID] {
			return false
		}
	}
	return true
}

func (s *NativeService) queuePendingAccountSwitch(target accountRow, busyIDs []string, expectedPID int) AccountPendingSwitch {
	s.cancelPendingSwitchLocked()
	watchCtx, cancel := context.WithCancel(s.ctx)
	info := AccountPendingSwitch{
		TargetID:       target.ID,
		TargetLabel:    firstNonEmpty(target.Label, target.Nickname, target.UserID),
		BusySessionIDs: busyIDs,
		QueuedAt:       storage.SortableTime(time.Now().UTC()),
	}
	s.accountMu.Lock()
	s.switchSeq++
	seq := s.switchSeq
	s.pendingSwitch = &pendingSwitchState{info: info, seq: seq, expectedPID: expectedPID, cancel: cancel}
	s.accountMu.Unlock()
	s.bus.Publish("accounts", map[string]any{"type": "account.switch_queued", "pendingSwitch": info})

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(1500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				checkCtx, checkCancel := context.WithTimeout(watchCtx, 15*time.Second)
				busy, _, err := s.findBusySessions(checkCtx)
				if err != nil {
					checkCancel()
					continue
				}
				if len(busy) > 0 {
					ids := make([]string, 0, len(busy))
					for _, b := range busy {
						ids = append(ids, b.ID)
					}
					s.accountMu.Lock()
					if s.pendingSwitch != nil && s.pendingSwitch.info.TargetID == target.ID {
						s.pendingSwitch.info.BusySessionIDs = ids
					}
					s.accountMu.Unlock()
					checkCancel()
					continue
				}
				checkCancel()
				execCtx, execCancel := context.WithTimeout(watchCtx, 20*time.Second)
				result, switchErr := s.executeAccountSwitch(execCtx, target.ID, false, false, expectedPID, seq)
				execCancel()
				if switchErr != nil {
					if errors.Is(switchErr, errAccountSwitchSuperseded) {
						return
					}
					var native *NativeError
					if errors.As(switchErr, &native) && native.Code == "ACCOUNT_SWITCH_BUSY" {
						continue
					}
					s.cancelPendingSwitchIfTarget(target.ID)
					s.bus.Publish("accounts", map[string]any{
						"type":      "account.switch_failed",
						"accountId": target.ID,
						"error":     switchErr.Error(),
					})
					return
				}
				if result.Switched {
					return
				}
			}
		}
	}()
	return info
}

func (s *NativeService) cancelPendingSwitchLocked() {
	s.accountMu.Lock()
	defer s.accountMu.Unlock()
	if s.pendingSwitch != nil {
		s.pendingSwitch.cancel()
		s.pendingSwitch = nil
	}
}

// beginQueuedSwitch re-validates a queued switch after switchMu is held: the
// request may have been cancelled or replaced by a different target while the
// watcher was waiting, and an old goroutine must never switch back to its own
// account. It clears the pending slot and returns that request's cancel func so
// the caller can release the watch context when the switch is over.
func (s *NativeService) beginQueuedSwitch(ctx context.Context, seq uint64, targetID string) (context.CancelFunc, error) {
	if seq == 0 {
		return nil, nil
	}
	s.accountMu.Lock()
	defer s.accountMu.Unlock()
	if s.pendingSwitch == nil || s.pendingSwitch.seq != seq || s.pendingSwitch.info.TargetID != targetID || ctx.Err() != nil {
		if s.pendingSwitch != nil && s.pendingSwitch.seq == seq {
			s.pendingSwitch.cancel()
			s.pendingSwitch = nil
		}
		return nil, errAccountSwitchSuperseded
	}
	cancel := s.pendingSwitch.cancel
	s.pendingSwitch = nil
	return cancel, nil
}

func (s *NativeService) cancelPendingSwitchIfTarget(targetID string) {
	s.accountMu.Lock()
	defer s.accountMu.Unlock()
	if s.pendingSwitch != nil && (targetID == "" || s.pendingSwitch.info.TargetID == targetID) {
		s.pendingSwitch.cancel()
		s.pendingSwitch = nil
	}
}

func (s *NativeService) accountsCancelPending() map[string]any {
	s.cancelPendingSwitchLocked()
	s.bus.Publish("accounts", map[string]any{"type": "account.switch_cancelled"})
	return map[string]any{"cancelled": true}
}
