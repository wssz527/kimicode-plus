package desktop

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"enhancedkimi/internal/cache"
	"enhancedkimi/internal/storage"
)

const (
	ProviderTypeSubscription = "subscription"
	ProviderTypeOpenPlatform = "open_platform"
	ProviderTypeOther        = "other"

	IdleModeDefault = "default"
	IdleModeOff     = "off"
	IdleModeCount   = "count"

	KeepAliveStateDisabled = "disabled"
	KeepAliveStateActive   = "keeping_alive"
	KeepAliveStateStopped  = "stopped"
)

type ResolvedCachePolicy struct {
	Model           string `json:"model"`
	Provider        string `json:"provider"`
	ProviderType    string `json:"providerType"`
	ProviderLabel   string `json:"providerLabel"`
	WindowMinutes   int    `json:"windowMinutes"`
	IntervalMinutes int    `json:"intervalMinutes"`
	Source          string `json:"source"`
}

type sessionKeepAliveRecord struct {
	SessionID           string
	Mode                string
	TargetCount         int
	UsedCount           int
	AnchorAt            string
	LastRequestAt       string
	LastKeepAliveAt     string
	LastKeepAliveTurnID string
	LastPrompt          string
	LastResult          string
	LastNote            string
	StoppedReason       string
	StoppedDetail       string
	RetryAt             string
	SpentTokens         int64
	Misses              int
}

type SessionKeepAliveView struct {
	SessionID        string              `json:"sessionId"`
	State            string              `json:"state"`
	Mode             string              `json:"mode"`
	TargetCount      int                 `json:"targetCount"`
	EffectiveEnabled bool                `json:"effectiveEnabled"`
	EffectiveLimit   int                 `json:"effectiveLimit"`
	UsedCount        int                 `json:"usedCount"`
	RemainingCount   *int                `json:"remainingCount,omitempty"`
	StatusLabel      string              `json:"statusLabel"`
	StatusTone       string              `json:"statusTone"`
	LastResult       string              `json:"lastResult,omitempty"`
	LastResultLabel  string              `json:"lastResultLabel,omitempty"`
	LastNote         string              `json:"lastNote,omitempty"`
	LastKeepAliveAt  string              `json:"lastKeepAliveAt,omitempty"`
	StoppedReason    string              `json:"stoppedReason,omitempty"`
	StoppedDetail    string              `json:"stoppedDetail,omitempty"`
	EstimatedTokens  int64               `json:"estimatedTokens"`
	BudgetTokens     int64               `json:"budgetTokens"`
	SpentTokens      int64               `json:"spentTokens"`
	Policy           ResolvedCachePolicy `json:"policy"`
	InFlight         bool                `json:"inFlight"`
	GlobalEnabled    bool                `json:"globalEnabled"`
	GlobalCount      int                 `json:"globalCount"`
}

type idleInFlightState struct {
	UserMessageID string
	TurnID        string
}

func sessionIsArchivedOrDone(session NativeSession) bool {
	if session.Archived || strings.TrimSpace(session.ArchivedAt) != "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(session.Status)) {
	case "archived", "done", "completed":
		return true
	}
	return false
}

func goalActiveForIdleKeepAlive(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var goal struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &goal) != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(goal.Status), "active")
}

func providerLabelForType(kind string) string {
	switch kind {
	case ProviderTypeSubscription:
		return "订阅"
	case ProviderTypeOpenPlatform:
		return "开放平台"
	default:
		return "其他"
	}
}

func providerBaseURL(cfg map[string]any) string {
	for _, key := range []string{"base_url", "baseUrl", "url"} {
		if value, ok := cfg[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func classifyProvider(providerKey string, providerCfg map[string]any) string {
	key := strings.ToLower(strings.TrimSpace(providerKey))
	baseURL := providerBaseURL(providerCfg)

	if providerCfg != nil {
		if oauth, ok := providerCfg["oauth"]; ok && oauth != nil {
			switch typed := oauth.(type) {
			case string:
				if strings.TrimSpace(typed) != "" {
					return ProviderTypeSubscription
				}
			case map[string]any:
				if len(typed) > 0 {
					return ProviderTypeSubscription
				}
			case bool:
				if typed {
					return ProviderTypeSubscription
				}
			}
		}
		if typ, ok := providerCfg["type"].(string); ok {
			switch strings.ToLower(strings.TrimSpace(typ)) {
			case "kimi", "kimi-code", "managed":
				return ProviderTypeSubscription
			}
		}
	}

	if baseURL != "" {
		if parsed, err := url.Parse(baseURL); err == nil {
			host := strings.ToLower(parsed.Hostname())
			path := strings.ToLower(strings.TrimRight(parsed.Path, "/"))
			if host == "api.kimi.com" && (path == "/coding" || strings.HasPrefix(path, "/coding/")) {
				return ProviderTypeSubscription
			}
			if host == "api.moonshot.cn" || host == "api.moonshot.ai" {
				return ProviderTypeOpenPlatform
			}
		}
	}

	switch key {
	case "kimi-code", "managed:kimi-code", "kimi-for-coding", "managed":
		return ProviderTypeSubscription
	}
	if strings.Contains(key, "api.kimi.com/coding") {
		return ProviderTypeSubscription
	}
	if strings.Contains(key, "api.moonshot.cn") || strings.Contains(key, "api.moonshot.ai") {
		return ProviderTypeOpenPlatform
	}
	return ProviderTypeOther
}

func resolveCachePolicyFromConfig(modelID string, cfg map[string]any) ResolvedCachePolicy {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" && cfg != nil {
		for _, key := range []string{"default_model", "defaultModel"} {
			if value, ok := cfg[key].(string); ok && strings.TrimSpace(value) != "" {
				modelID = strings.TrimSpace(value)
				break
			}
		}
	}

	providerKey, _ := splitModelKey(modelID)
	if cfg != nil {
		if models, ok := cfg["models"].(map[string]any); ok && modelID != "" {
			if entry, ok := models[modelID].(map[string]any); ok {
				if p, _ := entry["provider"].(string); strings.TrimSpace(p) != "" {
					providerKey = strings.TrimSpace(p)
				}
			} else {
				for key, raw := range models {
					entry, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					entryModel, _ := entry["model"].(string)
					if key == modelID || strings.EqualFold(entryModel, modelID) || strings.HasSuffix(key, "/"+modelID) {
						if p, _ := entry["provider"].(string); strings.TrimSpace(p) != "" {
							providerKey = strings.TrimSpace(p)
						} else if p, _ := splitModelKey(key); p != "" {
							providerKey = p
						}
						break
					}
				}
			}
		}
	}

	var providerCfg map[string]any
	if cfg != nil && providerKey != "" {
		if providers, ok := cfg["providers"].(map[string]any); ok {
			if matched, ok := providers[providerKey].(map[string]any); ok {
				providerCfg = matched
			}
		}
	}

	kind := classifyProvider(providerKey, providerCfg)
	return ResolvedCachePolicy{Model: modelID, Provider: providerKey, ProviderType: kind, ProviderLabel: providerLabelForType(kind)}
}

func (s *NativeService) cachedOfficialConfig(ctx context.Context, now time.Time) map[string]any {
	s.idleMu.Lock()
	if s.idleConfigCache != nil && now.Sub(s.idleConfigAt) < 10*time.Second {
		cached := s.idleConfigCache
		s.idleMu.Unlock()
		return cached
	}
	s.idleMu.Unlock()

	raw, err := s.kap.Request(ctx, http.MethodGet, "/config", nil)
	if err != nil {
		s.idleMu.Lock()
		cached := s.idleConfigCache
		s.idleMu.Unlock()
		return cached
	}
	var parsed map[string]any
	if json.Unmarshal(raw, &parsed) != nil {
		return nil
	}
	s.idleMu.Lock()
	s.idleConfigCache = parsed
	s.idleConfigAt = now
	s.idleMu.Unlock()
	return parsed
}

func (s *NativeService) invalidateOfficialConfigCache() {
	s.idleMu.Lock()
	s.idleConfigCache = nil
	s.idleConfigAt = time.Time{}
	s.idleMu.Unlock()
}

func managedQuotaRemainingPercent(usage managedUsage, now time.Time) (float64, bool) {
	if usage.Kind != "ok" || usage.Quota == nil || len(usage.Quota.Usages) == 0 {
		return 0, false
	}
	known := false
	minRemaining := 100.0
	for _, window := range usage.Quota.Usages {
		if window.UsedRatio == nil {
			continue
		}
		known = true
		if window.ResetAt != "" {
			if reset, err := time.Parse(time.RFC3339, window.ResetAt); err == nil && !now.Before(reset) {
				continue
			}
		}
		rem := (1.0 - *window.UsedRatio) * 100.0
		if rem < 0 {
			rem = 0
		}
		if rem > 100 {
			rem = 100
		}
		if rem < minRemaining {
			minRemaining = rem
		}
	}
	return minRemaining, known
}

func (s *NativeService) checkIdleQuotaGuard(ctx context.Context, settings AutomationSettings, policy ResolvedCachePolicy, now time.Time) (blocked bool, remaining float64, detail string) {
	if policy.ProviderType != ProviderTypeSubscription || settings.IdleKeepAlive.MinQuotaPercent <= 0 {
		return false, 0, ""
	}
	raw, err := s.kap.Request(ctx, http.MethodGet, "/oauth/usage", nil)
	if err != nil {
		return false, 0, ""
	}
	usage, err := parseManagedUsage(raw)
	if err != nil {
		return false, 0, ""
	}
	rem, known := managedQuotaRemainingPercent(usage, now)
	if !known {
		return false, 0, ""
	}
	if rem < float64(settings.IdleKeepAlive.MinQuotaPercent) {
		return true, rem, fmt.Sprintf("剩余额度约 %.0f%%，低于阈值 %d%%", rem, settings.IdleKeepAlive.MinQuotaPercent)
	}
	return false, rem, ""
}

const (
	gitFingerprintMaxFileBytes  = 8 << 20
	gitFingerprintMaxTotalBytes = 64 << 20
	gitFingerprintMaxStatus     = 4 << 20
	gitFingerprintMaxEntries    = 16_384
)

// An empty fingerprint with no error means Git confirmed there is no repository.
func gitStatusFingerprint(ctx context.Context, cwd string) (string, error) {
	if strings.TrimSpace(cwd) == "" {
		return "", errors.New("会话未记录工作目录")
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	stderr := &limitedBuffer{limit: 4096}
	top := &limitedBuffer{limit: 64 << 10}
	cmd := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.Stderr = stderr
	cmd.Stdout = top
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if ctx.Err() == nil && errors.As(err, &exitErr) && exitErr.ExitCode() == 128 &&
			strings.HasPrefix(stderr.buf.String(), "fatal: not a git repository (or any of the parent directories") {
			return "", confirmNonGitDirectory(cwd)
		}
		return "", fmt.Errorf("无法定位 Git 工作区：%w", err)
	}
	rootPath, ok := strings.CutSuffix(top.buf.String(), "\n")
	if top.truncated || !ok || !filepath.IsAbs(rootPath) {
		return "", errors.New("Git 工作区路径无效")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return "", err
	}
	defer root.Close()

	status, truncated, err := runGitCapped(ctx, rootPath, gitFingerprintMaxStatus, "--no-optional-locks", "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return "", fmt.Errorf("无法读取 Git 状态：%w", err)
	}
	if truncated {
		return "", errors.New("Git 状态超过大小上限")
	}
	digest := sha256.New()
	_, _ = io.WriteString(digest, rootPath+"\x00"+status+"\x00")
	head, headErr := gitHeadRevision(ctx, rootPath)
	if headErr != nil {
		return "", headErr
	}
	_, _ = io.WriteString(digest, head+"\x00")
	index, indexErr := gitIndexDigest(ctx, rootPath, head)
	if indexErr != nil {
		return "", indexErr
	}
	_, _ = io.WriteString(digest, index+"\x00")
	scan := gitFingerprintScan{ctx: ctx, root: root, entries: gitFingerprintMaxEntries, bytes: gitFingerprintMaxTotalBytes}
	for status != "" {
		var entry string
		entry, status, ok = strings.Cut(status, "\x00")
		if !ok || len(entry) < 4 || entry[2] != ' ' {
			return "", errors.New("Git 状态条目无效")
		}
		xy := entry[:2]
		if err := scan.addPath(digest, entry[3:], strings.Contains(xy, "D")); err != nil {
			return "", err
		}
		if strings.ContainsAny(xy, "RC") {
			var source string
			source, status, ok = strings.Cut(status, "\x00")
			if !ok {
				return "", errors.New("Git 状态缺少重命名或复制的源路径")
			}
			if err := scan.addPath(digest, source, strings.Contains(xy, "R")); err != nil {
				return "", err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func confirmNonGitDirectory(cwd string) error {
	path, err := filepath.Abs(cwd)
	if err != nil {
		return err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	for {
		if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
			return errors.New("发现 .git，但 Git 无法读取工作区")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

// gitHeadRevision reads the current commit so a commit or reset during the
// keepalive turn is noticed even when the working tree ends up identical. An
// unborn branch has no HEAD and is represented by an empty revision rather than
// an error, because `git add` before the first commit is a normal state.
func gitHeadRevision(ctx context.Context, rootPath string) (string, error) {
	out, truncated, err := runGitCapped(ctx, rootPath, gitFingerprintMaxStatus, "--no-optional-locks", "rev-parse", "--verify", "-q", "HEAD")
	if err != nil {
		var exitErr *exec.ExitError
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && strings.TrimSpace(out) == "" {
			return "", nil
		}
		return "", fmt.Errorf("无法读取 Git HEAD：%w", err)
	}
	if truncated {
		return "", errors.New("Git HEAD 超过大小上限")
	}
	revision := strings.TrimSpace(out)
	if revision == "" {
		return "", errors.New("Git HEAD 输出为空")
	}
	return revision, nil
}

// gitIndexDigest summarizes the staged content. `status --porcelain` only says a
// path is staged; an MM file whose working tree returns to the original bytes
// keeps the same status and the same working tree hash while its index blob
// changed. Hashing the full blob ids closes that gap. A repository without HEAD
// cannot diff against it, so the staged entries themselves are hashed instead.
func gitIndexDigest(ctx context.Context, rootPath, head string) (string, error) {
	digest := sha256.New()
	if head == "" {
		out, truncated, err := runGitCapped(ctx, rootPath, gitFingerprintMaxStatus, "--no-optional-locks", "ls-files", "--stage", "-z")
		if err != nil {
			return "", fmt.Errorf("无法读取 Git 暂存区：%w", err)
		}
		if truncated {
			return "", errors.New("Git 暂存区超过大小上限")
		}
		_, _ = io.WriteString(digest, "unborn\x00"+out)
		return fmt.Sprintf("%x", digest.Sum(nil)), nil
	}
	out, truncated, err := runGitCapped(ctx, rootPath, gitFingerprintMaxStatus, "--no-optional-locks", "diff", "--cached", "--raw", "-z", "--no-abbrev", "HEAD")
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("无法读取 Git 暂存区：%w", err)
	}
	if truncated {
		return "", errors.New("Git 暂存区超过大小上限")
	}
	_, _ = io.WriteString(digest, "head\x00"+out)
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

type gitFingerprintScan struct {
	ctx     context.Context
	root    *os.Root
	entries int
	bytes   int64
}

func (s *gitFingerprintScan) addPath(digest hash.Hash, path string, allowMissing bool) error {
	path = filepath.FromSlash(strings.TrimSuffix(path, "/"))
	if path == "." || !filepath.IsLocal(path) {
		return fmt.Errorf("Git 状态路径无效：%q", path)
	}
	if err := s.addEntry(digest, path, allowMissing); err != nil {
		return fmt.Errorf("无法读取工作区条目 %q：%w", path, err)
	}
	return nil
}

func (s *gitFingerprintScan) addEntry(dst hash.Hash, path string, allowMissing bool) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.entries == 0 {
		return errors.New("工作区条目超过数量上限")
	}
	s.entries--
	info, err := s.root.Lstat(path)
	if err != nil {
		if allowMissing && (errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)) {
			_, _ = io.WriteString(dst, "-")
			return nil
		}
		return err
	}

	digest := sha256.New()
	var kind string
	switch mode := info.Mode(); {
	case mode.IsRegular():
		kind = "F"
		if err := s.hashFile(digest, path, info); err != nil {
			return err
		}
	case mode.IsDir():
		kind = "D"
		entries, err := s.readDir(path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			_, _ = io.WriteString(digest, entry.Name()+"\x00")
			if err := s.addEntry(digest, filepath.Join(path, entry.Name()), false); err != nil {
				return fmt.Errorf("%q：%w", entry.Name(), err)
			}
		}
	case mode&os.ModeSymlink != 0:
		kind = "L"
		target, err := s.root.Readlink(path)
		if err != nil {
			return err
		}
		if int64(len(target)) > s.bytes {
			return errors.New("工作区文件超过大小上限")
		}
		s.bytes -= int64(len(target))
		_, _ = io.WriteString(digest, target)
	default:
		return errors.New("工作区条目类型不支持")
	}
	_, _ = io.WriteString(dst, kind)
	_, _ = dst.Write(digest.Sum(nil))
	return nil
}

func (s *gitFingerprintScan) readDir(path string) ([]fs.DirEntry, error) {
	dir, err := s.root.Open(path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(s.entries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > s.entries {
		return nil, errors.New("工作区条目超过数量上限")
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

func (s *gitFingerprintScan) hashFile(digest hash.Hash, path string, info os.FileInfo) error {
	limit := min(gitFingerprintMaxFileBytes, s.bytes)
	if info.Size() > limit {
		return errors.New("工作区文件超过大小上限")
	}
	file, err := s.root.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	current, err := file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, current) || !current.Mode().IsRegular() {
		return errors.New("工作区文件在读取前发生变化")
	}
	read, err := io.Copy(digest, io.LimitReader(file, limit+1))
	if err != nil {
		return err
	}
	if read > limit {
		return errors.New("工作区文件超过大小上限")
	}
	current, err = s.root.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(info, current) || info.Size() != read || current.Size() != read || !info.ModTime().Equal(current.ModTime()) {
		return errors.New("工作区文件在读取期间发生变化")
	}
	s.bytes -= read
	return nil
}

func (s *NativeService) loadSessionKeepAlive(ctx context.Context, id string) (sessionKeepAliveRecord, error) {
	record := sessionKeepAliveRecord{SessionID: id, Mode: IdleModeDefault, TargetCount: 2}
	err := s.db.QueryRowContext(ctx, `SELECT mode,target_count,used_count,anchor_at,last_request_at,last_keepalive_at,last_keepalive_turn_id,last_prompt,last_result,last_note,stopped_reason,stopped_detail,retry_at,spent_tokens,misses FROM session_keepalive_state WHERE session_id=?`, id).
		Scan(&record.Mode, &record.TargetCount, &record.UsedCount, &record.AnchorAt, &record.LastRequestAt, &record.LastKeepAliveAt, &record.LastKeepAliveTurnID, &record.LastPrompt, &record.LastResult, &record.LastNote, &record.StoppedReason, &record.StoppedDetail, &record.RetryAt, &record.SpentTokens, &record.Misses)
	if errors.Is(err, sql.ErrNoRows) {
		return record, nil
	}
	if record.TargetCount < idleKeepAliveMinCount || record.TargetCount > idleKeepAliveMaxCount {
		record.TargetCount = 2
	}
	switch record.Mode {
	case "until":
		record.Mode = IdleModeOff
		record.StoppedReason = "legacy_until_removed"
		record.StoppedDetail = "旧版截止时刻保活已停用，请重新选择保活次数"
	case IdleModeDefault, IdleModeOff, IdleModeCount:
	default:
		record.Mode = IdleModeDefault
	}
	return record, err
}

func (s *NativeService) saveSessionKeepAlive(ctx context.Context, record sessionKeepAliveRecord, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO session_keepalive_state(session_id,mode,target_count,used_count,anchor_at,last_request_at,last_keepalive_at,last_keepalive_turn_id,last_prompt,last_result,last_note,stopped_reason,stopped_detail,retry_at,spent_tokens,misses,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(session_id) DO UPDATE SET mode=excluded.mode,target_count=excluded.target_count,used_count=excluded.used_count,anchor_at=excluded.anchor_at,last_request_at=excluded.last_request_at,last_keepalive_at=excluded.last_keepalive_at,last_keepalive_turn_id=excluded.last_keepalive_turn_id,last_prompt=excluded.last_prompt,last_result=excluded.last_result,last_note=excluded.last_note,stopped_reason=excluded.stopped_reason,stopped_detail=excluded.stopped_detail,retry_at=excluded.retry_at,spent_tokens=excluded.spent_tokens,misses=excluded.misses,updated_at=excluded.updated_at`,
		record.SessionID, record.Mode, record.TargetCount, record.UsedCount, record.AnchorAt, record.LastRequestAt, record.LastKeepAliveAt, record.LastKeepAliveTurnID, record.LastPrompt, record.LastResult, record.LastNote, record.StoppedReason, record.StoppedDetail, record.RetryAt, record.SpentTokens, record.Misses, storage.SortableTime(now))
	return err
}

func (s *NativeService) idleInFlightInfo(id string) (idleInFlightState, bool) {
	s.idleMu.Lock()
	defer s.idleMu.Unlock()
	info, ok := s.idleInFlight[id]
	return info, ok
}

func (s *NativeService) isIdleKeepAliveInFlight(id string) bool {
	_, active := s.idleInFlightInfo(id)
	return active
}

func (s *NativeService) setIdleInFlight(id string, info idleInFlightState) {
	s.idleMu.Lock()
	defer s.idleMu.Unlock()
	if s.idleInFlight == nil {
		s.idleInFlight = map[string]idleInFlightState{}
	}
	s.idleInFlight[id] = info
}

func (s *NativeService) clearIdleInFlight(id string) {
	s.idleMu.Lock()
	defer s.idleMu.Unlock()
	delete(s.idleInFlight, id)
}

func (s *NativeService) idleKeepAlivePoll() time.Duration {
	if s.idleVerifyPoll > 0 {
		return s.idleVerifyPoll
	}
	if s.verifyPoll > 0 {
		return s.verifyPoll
	}
	return 1500 * time.Millisecond
}

func parseSortableOrRFC3339(value string) time.Time {
	at, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	return at
}

func (record *sessionKeepAliveRecord) resetCycle(at time.Time) {
	record.AnchorAt = storage.SortableTime(at)
	record.UsedCount, record.SpentTokens, record.Misses = 0, 0, 0
	record.LastRequestAt, record.RetryAt = "", ""
	if record.StoppedReason != "legacy_until_removed" {
		record.StoppedReason, record.StoppedDetail = "", ""
	}
}

func (s *NativeService) noteUserMessage(ctx context.Context, id string, at time.Time) {
	if id == "" || s.db == nil {
		return
	}
	s.idleStateMu.Lock()
	record, err := s.loadSessionKeepAlive(ctx, id)
	changed := err == nil && at.After(parseSortableOrRFC3339(record.AnchorAt))
	if changed {
		record.resetCycle(at)
		err = s.saveSessionKeepAlive(ctx, record, at)
	}
	s.idleStateMu.Unlock()
	if changed && err == nil {
		s.publishSessionKeepAliveUpdate(ctx, id, at)
	}
}

func (s *NativeService) noteUserMessageFromFrame(frame NativeFrame) {
	if frame.SessionID == "" || (frame.Type != "turn.started" && frame.Type != "item.started") {
		return
	}
	info, _ := s.idleInFlightInfo(frame.SessionID)
	if info.TurnID != "" && (frame.TurnID == info.TurnID || frame.Turn.TurnID == info.TurnID) {
		return
	}
	items := frame.Turn.Items
	if frame.Type == "item.started" {
		items = []NativeItem{frame.Item}
	}
	for _, item := range items {
		if item.Type != "user_message" || isKeepAlivePrompt(item.Text) || (info.UserMessageID != "" && item.ItemID == info.UserMessageID) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		s.noteUserMessage(ctx, frame.SessionID, time.Now().UTC())
		cancel()
		return
	}
}

func (s *NativeService) initializedCacheStore() *cache.Store {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	return s.store
}

func (s *NativeService) retentionForPolicy(ctx context.Context, store *cache.Store, now time.Time) cache.Retention {
	if store == nil {
		return cache.Retention{}
	}
	s.idleMu.Lock()
	if !s.idleRetentionAt.IsZero() && now.Sub(s.idleRetentionAt) < 5*time.Minute {
		result := s.idleRetention
		s.idleMu.Unlock()
		return result
	}
	s.idleMu.Unlock()
	result, err := store.Retention(ctx, now)
	if err != nil {
		return cache.Retention{}
	}
	s.idleMu.Lock()
	s.idleRetention, s.idleRetentionAt = result, now
	s.idleMu.Unlock()
	return result
}

func (s *NativeService) resolveSessionPolicy(ctx context.Context, snapshot automationSnapshot, lastStep cache.Step, store *cache.Store, now time.Time) ResolvedCachePolicy {
	model := strings.TrimSpace(snapshot.session.Model)
	if model == "" && lastStep.Model != cache.ModelUnknown {
		model = lastStep.Model
	}
	policy := resolveCachePolicyFromConfig(model, s.cachedOfficialConfig(ctx, now))
	retention := s.retentionForPolicy(ctx, store, now)
	for _, item := range retention.ByModel {
		if item.Model == policy.Model {
			return applyRetentionPolicy(policy, item.IdleBuckets)
		}
	}
	// A completed step can report an unqualified model name while the daemon
	// session uses its provider-qualified config key.
	_, shortModel := splitModelKey(policy.Model)
	if shortModel != "" && shortModel == lastStep.Model {
		for _, item := range retention.ByModel {
			if item.Model == lastStep.Model {
				return applyRetentionPolicy(policy, item.IdleBuckets)
			}
		}
	}
	return applyRetentionPolicy(policy, nil)
}

func effectiveIdleConfig(settings AutomationSettings, record sessionKeepAliveRecord) (bool, int) {
	if !settings.IdleKeepAlive.Enabled || record.Mode == IdleModeOff {
		return false, 0
	}
	if record.Mode == IdleModeCount {
		return true, record.TargetCount
	}
	return true, settings.IdleKeepAlive.DefaultCount
}

func idleKeepAliveBlocked(snapshot automationSnapshot, live cache.LiveSignals) bool {
	return compactionBlocked(snapshot, live) || goalBlocksIdle(snapshot) || goalActiveForIdleKeepAlive(snapshot.goal)
}

type keepAliveDecision struct {
	warm   bool
	due    bool
	reason string
	detail string
}

func decideIdleKeepAlive(record sessionKeepAliveRecord, policy ResolvedCachePolicy, now time.Time, limit int, estimate int64) keepAliveDecision {
	if record.StoppedReason == "undo_failed" || record.StoppedReason == "unconfirmed" {
		return keepAliveDecision{reason: record.StoppedReason, detail: record.StoppedDetail}
	}
	if record.Misses >= 2 {
		return keepAliveDecision{reason: "consecutive_misses", detail: "连续两次保活未命中缓存，等待下一次真实请求"}
	}
	lastRequest := parseSortableOrRFC3339(record.LastRequestAt)
	elapsed := now.Sub(lastRequest)
	if policy.IntervalMinutes <= 0 {
		return keepAliveDecision{reason: "low_retention", detail: "短间隔的实测缓存读取比例过低"}
	}
	warm := !lastRequest.IsZero() && elapsed >= 0 && elapsed < time.Duration(policy.WindowMinutes)*time.Minute
	if record.UsedCount >= limit {
		return keepAliveDecision{warm: warm && elapsed < time.Duration(policy.IntervalMinutes)*time.Minute && record.LastResult == keepAliveHit, reason: "count_exhausted", detail: "本轮保活次数已用完"}
	}
	if !warm {
		if record.UsedCount > 0 {
			return keepAliveDecision{reason: "expired", detail: "当前缓存可能已冷却，等待下一次真实请求"}
		}
		return keepAliveDecision{detail: "等待下一次真实请求激活缓存"}
	}
	if estimate <= 0 || estimate > idleKeepAliveTokenBudget-record.SpentTokens {
		return keepAliveDecision{reason: "token_budget", detail: "本次 Token 估算不可用或超出本轮剩余预算"}
	}
	if now.Before(parseSortableOrRFC3339(record.RetryAt)) {
		return keepAliveDecision{warm: true, detail: record.LastNote}
	}
	return keepAliveDecision{warm: true, due: elapsed >= time.Duration(policy.IntervalMinutes)*time.Minute}
}

func (s *NativeService) observeRealStep(ctx context.Context, record *sessionKeepAliveRecord, step cache.Step) {
	if step.At.IsZero() || !step.At.After(parseSortableOrRFC3339(record.AnchorAt)) || !step.At.After(parseSortableOrRFC3339(record.LastRequestAt)) {
		return
	}
	if step.TurnID != "" && step.TurnID == record.LastKeepAliveTurnID {
		return
	}
	if (record.LastPrompt != "" || record.UsedCount > 0) && !step.At.Before(parseSortableOrRFC3339(record.LastKeepAliveAt)) {
		// After a lost ACK or a restart, a timestamp alone cannot distinguish
		// our probe from the user's next request.
		turns, err := s.nativeTurns(ctx, record.SessionID, 20)
		if err != nil || step.TurnID == "" {
			return
		}
		matched := false
		for _, turn := range turns {
			if turn.TurnID == step.TurnID {
				for _, item := range turn.Items {
					matched = matched || (item.Type == "user_message" && !isKeepAlivePrompt(item.Text))
				}
				break
			}
		}
		if !matched {
			return
		}
	}
	legacyStop := record.StoppedReason == "legacy_until_removed"
	if legacyStop {
		return
	}
	record.resetCycle(step.At)
	record.LastPrompt = ""
	record.LastRequestAt = storage.SortableTime(step.At)
}

func (s *NativeService) publishSessionKeepAliveUpdate(ctx context.Context, id string, now time.Time) {
	if view, err := s.sessionKeepAliveView(ctx, id, now); err == nil {
		s.bus.Publish("keepalive", map[string]any{"type": "keepalive.session", "sessionId": id, "view": view})
	}
}

func (s *NativeService) publishKeepAliveAlert(id, reason, detail string) {
	message := detail
	if name := s.cachedTitle(id); name != "" {
		message = fmt.Sprintf("会话「%s」：%s", name, detail)
	}
	s.bus.Publish("keepalive", map[string]any{
		"type": "keepalive.alert", "sessionId": id, "reason": reason,
		"title": "缓存保活已停止", "severity": "warning", "message": message,
	})
}

func (s *NativeService) isIdleKeepAliveProtecting(ctx context.Context, settings AutomationSettings, id string, now time.Time) bool {
	if s.isIdleKeepAliveInFlight(id) {
		return true
	}
	if !settings.IdleKeepAlive.Enabled {
		return false
	}
	snapshot, ok := s.automationSnapshot(id)
	if !ok || sessionIsArchivedOrDone(snapshot.session) || idleKeepAliveBlocked(snapshot, s.automationLive(ctx, snapshot)) {
		return false
	}
	record, err := s.loadSessionKeepAlive(ctx, id)
	enabled, limit := effectiveIdleConfig(settings, record)
	if err != nil || !enabled {
		return false
	}
	store := s.initializedCacheStore()
	if store == nil {
		return false
	}
	step, _, err := store.LastMainStep(ctx, id)
	if err != nil {
		return false
	}
	s.observeRealStep(ctx, &record, step)
	policy := s.resolveSessionPolicy(ctx, snapshot, step, store, now)
	decision := decideIdleKeepAlive(record, policy, now, limit, keepAliveTokenEstimate(snapshot.session, step))
	return record.StoppedReason != "quota_low" && decision.warm
}

const idleKeepAliveRetryDelay = 30 * time.Second

type idleKeepAliveJob struct {
	intentID      string
	sessionID     string
	preLastTurnID string
	message       string
	userMessageID string
	cwd           string
	preGit        string
	priorStep     cache.Step
	before        sessionKeepAliveRecord
	estimate      int64
	actualTokens  int64
	sentAt        time.Time
	sendNote      string
}

func (s *NativeService) runIdleKeepAliveOnce(ctx context.Context, settings AutomationSettings, id string, now time.Time) {
	if !settings.IdleKeepAlive.Enabled || s.isAutomationPausedForSwitch() || s.isIdleKeepAliveInFlight(id) {
		return
	}
	snapshot, ok := s.automationSnapshot(id)
	if !ok || sessionIsArchivedOrDone(snapshot.session) || idleKeepAliveBlocked(snapshot, s.automationLive(ctx, snapshot)) {
		return
	}
	store, err := s.cacheStore()
	if err != nil {
		return
	}
	if _, err = store.Scan(ctx, now, false); err != nil {
		return
	}
	step, _, err := store.LastMainStep(ctx, id)
	if err != nil {
		return
	}
	s.idleStateMu.Lock()
	record, err := s.loadSessionKeepAlive(ctx, id)
	enabled, limit := effectiveIdleConfig(settings, record)
	s.idleStateMu.Unlock()
	if err != nil || !enabled {
		return
	}
	original := record
	s.observeRealStep(ctx, &record, step)
	policy := s.resolveSessionPolicy(ctx, snapshot, step, store, now)
	decision := decideIdleKeepAlive(record, policy, now, limit, keepAliveTokenEstimate(snapshot.session, step))
	if record.StoppedReason == "quota_low" {
		return
	}
	record.StoppedReason, record.StoppedDetail = decision.reason, decision.detail
	if record != original && !s.saveKeepAliveChange(ctx, original, record, now) {
		return
	}
	if !decision.due {
		return
	}

	loaded, err := s.readSession(ctx, id)
	if err != nil {
		s.deferIdleKeepAlive(ctx, record, "暂时无法读取会话，稍后重试", now)
		return
	}
	fresh := snapshotFromCache(loaded)
	if sessionIsArchivedOrDone(fresh.session) || idleKeepAliveBlocked(fresh, s.automationLive(ctx, fresh)) || len(loaded.turns) == 0 || s.isAutomationPausedForSwitch() {
		return
	}
	if !fresh.goalKnown {
		s.deferIdleKeepAlive(ctx, record, "暂时无法确认目标状态，稍后重试", now)
		return
	}
	if snapshot.session.UpdatedAt != fresh.session.UpdatedAt || snapshot.session.Model != fresh.session.Model {
		return
	}
	estimate := keepAliveTokenEstimate(fresh.session, step)
	if estimate <= 0 || estimate > idleKeepAliveTokenBudget-record.SpentTokens {
		s.stopKeepAlive(ctx, record, "token_budget", "本次 Token 估算不可用或超出本轮剩余预算", now)
		return
	}
	if low, _, detail := s.checkIdleQuotaGuard(ctx, settings, policy, now); low {
		s.stopKeepAlive(ctx, record, "quota_low", detail, now)
		return
	}
	preGit, err := gitStatusFingerprint(ctx, fresh.session.CWD)
	if err != nil {
		s.deferIdleKeepAlive(ctx, record, "暂时无法确认工作区状态，稍后重试", now)
		return
	}
	latest, ok := s.automationSnapshot(id)
	if !ok || latest.revision != snapshot.revision || idleKeepAliveBlocked(latest, s.automationLive(ctx, latest)) {
		return
	}
	intent, _, err := s.beginAutomation(ctx, "idle_keepalive", id, contextRevision(fresh), now)
	if err != nil {
		return
	}
	message := idleKeepAlivePrompt + " #" + intent.ID
	before := record
	record.LastPrompt, record.LastResult, record.LastNote = message, keepAliveSent, "请求已提交，正在核对缓存结果"
	record.LastKeepAliveAt, record.RetryAt = storage.SortableTime(now), ""
	record.UsedCount++
	record.SpentTokens += estimate
	if !s.saveKeepAliveChange(ctx, before, record, now) {
		_ = s.updateAutomation(ctx, intent.ID, "cancelled", "候选已发生变化", now)
		return
	}
	s.setIdleInFlight(id, idleInFlightState{})
	job := idleKeepAliveJob{intentID: intent.ID, sessionID: id, preLastTurnID: loaded.turns[len(loaded.turns)-1].TurnID,
		message: message, cwd: fresh.session.CWD, preGit: preGit, priorStep: step, before: before, estimate: estimate, sentAt: now}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.sendIdleKeepAlive(job)
	}()
}

func (s *NativeService) saveKeepAliveChange(ctx context.Context, before, after sessionKeepAliveRecord, now time.Time) bool {
	s.idleStateMu.Lock()
	current, err := s.loadSessionKeepAlive(ctx, before.SessionID)
	if err == nil && current == before {
		err = s.saveSessionKeepAlive(ctx, after, now)
	} else {
		s.idleStateMu.Unlock()
		return false
	}
	s.idleStateMu.Unlock()
	if err != nil {
		return false
	}
	if after.StoppedReason != before.StoppedReason && after.StoppedReason != "" {
		s.publishKeepAliveAlert(after.SessionID, after.StoppedReason, after.StoppedDetail)
	}
	if after.StoppedReason != before.StoppedReason || after.AnchorAt != before.AnchorAt {
		s.publishSessionKeepAliveUpdate(ctx, after.SessionID, now)
	}
	return true
}

func (s *NativeService) deferIdleKeepAlive(ctx context.Context, record sessionKeepAliveRecord, note string, now time.Time) {
	after := record
	after.RetryAt, after.LastNote = storage.SortableTime(now.Add(idleKeepAliveRetryDelay)), note
	s.saveKeepAliveChange(ctx, record, after, now)
}

func (s *NativeService) stopKeepAlive(ctx context.Context, record sessionKeepAliveRecord, reason, detail string, now time.Time) {
	after := record
	after.StoppedReason, after.StoppedDetail = reason, detail
	s.saveKeepAliveChange(ctx, record, after, now)
}

func (s *NativeService) sendIdleKeepAlive(job idleKeepAliveJob) {
	defer func() {
		s.clearIdleInFlight(job.sessionID)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.publishSessionKeepAliveUpdate(ctx, job.sessionID, time.Now().UTC())
	}()
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	settings, err := s.automationSettings(ctx)
	record, recordErr := s.loadSessionKeepAlive(ctx, job.sessionID)
	snapshot, found := s.automationSnapshot(job.sessionID)
	enabled, limit := effectiveIdleConfig(settings, record)
	if err != nil || recordErr != nil || !enabled || record.UsedCount > limit || record.AnchorAt != job.before.AnchorAt || !found || idleKeepAliveBlocked(snapshot, s.automationLive(ctx, snapshot)) || s.isSafe() || s.isAutomationPausedForSwitch() {
		cancel()
		s.finishIdleKeepAlive(job, "", "cancelled", "发送前会话或设置发生变化", "", false, true)
		return
	}
	loaded, err := s.readSession(ctx, job.sessionID)
	latest := snapshotFromCache(loaded)
	if err != nil || !latest.goalKnown || sessionIsArchivedOrDone(latest.session) || idleKeepAliveBlocked(latest, s.automationLive(ctx, latest)) ||
		latest.session.CWD != job.cwd || latest.session.Model != snapshot.session.Model || latest.session.UpdatedAt != snapshot.session.UpdatedAt ||
		len(loaded.turns) == 0 || loaded.turns[len(loaded.turns)-1].TurnID != job.preLastTurnID || s.isAutomationPausedForSwitch() {
		cancel()
		s.finishIdleKeepAlive(job, "", "cancelled", "发送前无法确认会话仍处于原空闲回合，稍后重试", "", false, true)
		return
	}
	body := map[string]any{"content": []any{map[string]string{"type": "text", "text": job.message}}}
	raw, err := s.kap.Request(ctx, http.MethodPost, "/sessions/"+url.PathEscape(job.sessionID)+"/prompts", body)
	cancel()
	if err != nil {
		var transport *kapTransportError
		var native *NativeError
		switch {
		case errors.As(err, &native) && native.Status >= 400 && native.Status < 500:
			s.finishIdleKeepAlive(job, "", keepAliveUnknown, "官方暂未接受请求，稍后重试", "", false, true)
			return
		case errors.As(err, &transport) && !transport.sent:
			s.finishIdleKeepAlive(job, "", keepAliveUnknown, "网络连接暂时不可用，稍后重试", "", false, true)
			return
		default:
			job.sendNote = "发送结果暂不可确认"
		}
	} else {
		var response struct {
			UserMessageID string `json:"user_message_id"`
		}
		_ = json.Unmarshal(raw, &response)
		job.userMessageID = response.UserMessageID
	}
	s.setIdleInFlight(job.sessionID, idleInFlightState{UserMessageID: job.userMessageID})
	ackCtx, ackCancel := context.WithTimeout(s.ctx, 3*time.Second)
	ackNote := "保活请求已发送，等待核对缓存用量"
	if job.sendNote != "" {
		ackNote = job.sendNote + "，正在查找对应回合"
	}
	_ = s.updateAutomation(ackCtx, job.intentID, keepAliveSent, ackNote, time.Now().UTC())
	s.publishAutomationReceipt("idle_keepalive", job.sessionID, keepAliveSent, ackNote)
	s.publishSessionKeepAliveUpdate(ackCtx, job.sessionID, job.sentAt)
	ackCancel()
	s.completeIdleKeepAlive(job)
}

func isIdleKeepAliveTurn(turn NativeTurn, job idleKeepAliveJob) bool {
	if turn.TurnID == "" || turn.TurnID == job.preLastTurnID {
		return false
	}
	count := 0
	for _, item := range turn.Items {
		if item.Type != "user_message" {
			continue
		}
		if strings.TrimSpace(item.Text) != job.message || (job.userMessageID != "" && item.ItemID != job.userMessageID) {
			return false
		}
		count++
	}
	return count == 1
}

func findIdleKeepAliveTurn(turns []NativeTurn, job idleKeepAliveJob) (NativeTurn, bool) {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].TurnID == job.preLastTurnID {
			break
		}
		if isIdleKeepAliveTurn(turns[i], job) {
			return turns[i], i == len(turns)-1 && i > 0 && turns[i-1].TurnID == job.preLastTurnID
		}
	}
	return NativeTurn{}, false
}

func turnHasToolCall(turn NativeTurn) bool {
	for _, item := range turn.Items {
		if item.Type == "tool_call" {
			return true
		}
	}
	return false
}

func turnHasSettledOutput(turn NativeTurn) bool {
	if turn.Error != nil {
		return true
	}
	for _, item := range turn.Items {
		if item.Type == "assistant_message" {
			return true
		}
	}
	return false
}

func (s *NativeService) completeIdleKeepAlive(job idleKeepAliveJob) {
	ctx, cancel := context.WithTimeout(s.ctx, s.keepAliveWindow())
	defer cancel()
	poll := time.NewTicker(s.idleKeepAlivePoll())
	defer poll.Stop()
	var turn NativeTurn
	for {
		select {
		case <-ctx.Done():
			s.finishIdleKeepAlive(job, turn.TurnID, keepAliveUnknown, "保活回合未在核对窗口内完成，等待下一次真实请求", "unconfirmed", false, false)
			return
		case <-poll.C:
		}
		loaded, err := s.readSession(ctx, job.sessionID)
		if err != nil {
			continue
		}
		candidate, isLast := findIdleKeepAliveTurn(loaded.turns, job)
		if candidate.TurnID == "" {
			continue
		}
		turn = candidate
		s.setIdleInFlight(job.sessionID, idleInFlightState{UserMessageID: job.userMessageID, TurnID: turn.TurnID})
		if !isLast {
			s.finishIdleKeepAlive(job, turn.TurnID, keepAliveUnknown, "保活后已有新回合，保留当前对话", "", false, false)
			return
		}
		snapshot := snapshotFromCache(loaded)
		if !idleKeepAliveBlocked(snapshot, s.automationLive(ctx, snapshot)) && turnHasSettledOutput(turn) {
			break
		}
	}
	if turnHasToolCall(turn) {
		s.finishIdleKeepAlive(job, turn.TurnID, keepAliveUnknown, "保活回合触发工具，已保留回合供检查", "undo_failed", false, false)
		return
	}

	state, note := keepAliveUnknown, "保活回合尚无可核实的缓存用量"
	if store := s.initializedCacheStore(); store != nil {
		for attempt := 0; attempt < 3 && ctx.Err() == nil; attempt++ {
			_, _ = store.Scan(ctx, time.Now().UTC(), true)
			step, found, err := store.FirstMainStepForTurn(ctx, job.sessionID, turn.TurnID)
			if err == nil && found && step.StepUUID != job.priorStep.StepUUID {
				reportsCache := job.priorStep.Model == step.Model && (job.priorStep.InputCacheRead > 0 || job.priorStep.InputCacheCreation > 0)
				state, note = judgeKeepAlive(step, reportsCache)
				job.actualTokens = step.InputOther + step.InputCacheRead + step.InputCacheCreation + step.Output
				break
			}
			if attempt < 2 {
				select {
				case <-ctx.Done():
				case <-poll.C:
				}
			}
		}
	}
	postGit, err := gitStatusFingerprint(ctx, job.cwd)
	if err != nil || postGit != job.preGit {
		s.finishIdleKeepAlive(job, turn.TurnID, state, note+"。工作区状态无法确认或已变化，本次未撤回", "", true, false)
		return
	}
	// Recheck the whole latest turn after hashing Git. The official undo endpoint
	// targets a turn ID, and the verified tail must still contain only our prompt.
	latest, err := s.readSession(ctx, job.sessionID)
	if err != nil {
		s.finishIdleKeepAlive(job, turn.TurnID, state, note+"。暂时无法复核会话，本次未撤回", "", true, false)
		return
	}
	tail, isLast := findIdleKeepAliveTurn(latest.turns, job)
	latestSnapshot := snapshotFromCache(latest)
	if !isLast || tail.TurnID != turn.TurnID || !latest.goalKnown || latest.session.CWD != job.cwd || sessionIsArchivedOrDone(latest.session) || idleKeepAliveBlocked(latestSnapshot, s.automationLive(ctx, latestSnapshot)) {
		s.finishIdleKeepAlive(job, turn.TurnID, state, note+"。会话已变化，本次未撤回", "", true, false)
		return
	}
	if turnHasToolCall(tail) || !turnHasSettledOutput(tail) {
		s.finishIdleKeepAlive(job, turn.TurnID, state, note+"。回合状态已变化，本次未撤回", "undo_failed", false, false)
		return
	}
	rawSession, readErr := s.kap.Request(ctx, http.MethodGet, "/sessions/"+url.PathEscape(job.sessionID), nil)
	current, mapErr := mapNativeSession(rawSession)
	latestSnapshot.session = current
	if readErr != nil || mapErr != nil || current.CWD != job.cwd || current.UpdatedAt != latest.session.UpdatedAt || sessionIsArchivedOrDone(current) || idleKeepAliveBlocked(latestSnapshot, s.automationLive(ctx, latestSnapshot)) {
		s.finishIdleKeepAlive(job, turn.TurnID, state, note+"。会话已恢复活动，本次未撤回", "", true, false)
		return
	}
	removed, err := s.kap.UndoSession(ctx, job.sessionID, turn.TurnID, "")
	if err != nil || len(removed) != 1 || removed[0] != turn.TurnID {
		s.finishIdleKeepAlive(job, turn.TurnID, state, note+"。未能确认官方仅撤回了保活回合", "undo_failed", false, false)
		return
	}

	s.mu.Lock()
	if cached, ok := s.cache[job.sessionID]; ok {
		cached.turns = slices.DeleteFunc(cached.turns, func(item NativeTurn) bool { return item.TurnID == turn.TurnID })
		s.cache[job.sessionID] = cached
	}
	s.mu.Unlock()
	s.finishIdleKeepAlive(job, turn.TurnID, state, note+"。已撤回保活回合", "", false, false)
}

func (s *NativeService) finishIdleKeepAlive(job idleKeepAliveJob, turnID, state, note, stopReason string, retry, release bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := time.Now().UTC()
	var alert string
	s.idleStateMu.Lock()
	record, err := s.loadSessionKeepAlive(ctx, job.sessionID)
	if err == nil && record.LastPrompt == job.message {
		if turnID != "" {
			record.LastKeepAliveTurnID = turnID
		}
		if record.AnchorAt == job.before.AnchorAt {
			record.LastResult, record.LastNote = state, note
			switch state {
			case keepAliveHit:
				record.Misses = 0
				record.LastRequestAt = storage.SortableTime(job.sentAt)
			case keepAliveMiss:
				record.Misses++
				record.LastRequestAt = storage.SortableTime(job.sentAt)
				if record.Misses >= 2 && stopReason == "" {
					stopReason = "consecutive_misses"
					note += "。连续两次保活未命中缓存，等待下一次真实请求"
					record.LastNote = note
				}
			}
			if release {
				record.UsedCount = job.before.UsedCount
				record.SpentTokens = job.before.SpentTokens
				record.LastPrompt, record.LastKeepAliveAt = job.before.LastPrompt, job.before.LastKeepAliveAt
			} else {
				record.SpentTokens = job.before.SpentTokens + max(job.estimate, job.actualTokens)
			}
			if retry || release || state == keepAliveUnknown {
				record.RetryAt = storage.SortableTime(maxTime(now, job.sentAt).Add(idleKeepAliveRetryDelay))
			}
			if stopReason != "" && record.StoppedReason != stopReason {
				record.StoppedReason, record.StoppedDetail = stopReason, note
				alert = stopReason
			}
		}
		err = s.saveSessionKeepAlive(ctx, record, now)
	}
	s.idleStateMu.Unlock()
	if err == nil && alert != "" {
		s.publishKeepAliveAlert(job.sessionID, alert, note)
	}
	_ = s.updateAutomation(ctx, job.intentID, state, note, now)
	s.publishAutomationReceipt("idle_keepalive", job.sessionID, state, note)
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (s *NativeService) sessionKeepAliveView(ctx context.Context, sessionID string, now time.Time) (SessionKeepAliveView, error) {
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return SessionKeepAliveView{}, errors.New("缺少会话 ID")
	}
	settings, err := s.automationSettings(ctx)
	if err != nil {
		return SessionKeepAliveView{}, err
	}
	record, err := s.loadSessionKeepAlive(ctx, id)
	if err != nil {
		return SessionKeepAliveView{}, err
	}
	snapshot, ok := s.automationSnapshot(id)
	if !ok {
		if raw, getErr := s.kap.Request(ctx, http.MethodGet, "/sessions/"+url.PathEscape(id), nil); getErr == nil {
			snapshot.session, _ = mapNativeSession(raw)
		}
	}
	store := s.initializedCacheStore()
	if store == nil {
		store = cache.OpenMetrics(s.db)
	}
	var step cache.Step
	if store != nil {
		step, _, _ = store.LastMainStep(ctx, id)
	}
	enabled, limit := effectiveIdleConfig(settings, record)
	inFlight := s.isIdleKeepAliveInFlight(id)
	policy := s.resolveSessionPolicy(ctx, snapshot, step, store, now)
	estimate := keepAliveTokenEstimate(snapshot.session, step)
	state, label, tone := KeepAliveStateDisabled, "未启用", "muted"
	var remaining *int
	reason, detail := "", ""
	if enabled {
		if !inFlight {
			s.observeRealStep(ctx, &record, step)
		}
		count := max(0, limit-record.UsedCount)
		remaining = &count
		state, label, tone = KeepAliveStateActive, "保活中", "ok"
		decision := decideIdleKeepAlive(record, policy, now, limit, estimate)
		reason, detail = decision.reason, decision.detail
		if record.StoppedReason == "quota_low" {
			reason, detail = record.StoppedReason, record.StoppedDetail
		}
		if sessionIsArchivedOrDone(snapshot.session) {
			reason, detail = "archived", "会话已归档或完成"
		}
		if inFlight {
			reason, detail = "", "正在核对本次保活请求"
		}
	} else if record.StoppedReason == "legacy_until_removed" {
		reason, detail = record.StoppedReason, record.StoppedDetail
	}
	if reason != "" {
		state, label, tone = KeepAliveStateStopped, "已停止", "warn"
	}
	return SessionKeepAliveView{
		SessionID: id, State: state, Mode: record.Mode, TargetCount: record.TargetCount,
		EffectiveEnabled: enabled, EffectiveLimit: limit, UsedCount: record.UsedCount, RemainingCount: remaining,
		StatusLabel: label, StatusTone: tone, LastResult: record.LastResult, LastResultLabel: formatKeepAliveResultLabel(record.LastResult),
		LastNote: record.LastNote, LastKeepAliveAt: record.LastKeepAliveAt, StoppedReason: reason, StoppedDetail: detail,
		EstimatedTokens: estimate, BudgetTokens: idleKeepAliveTokenBudget, SpentTokens: record.SpentTokens, Policy: policy, InFlight: inFlight,
		GlobalEnabled: settings.IdleKeepAlive.Enabled, GlobalCount: settings.IdleKeepAlive.DefaultCount,
	}, nil
}

func formatKeepAliveResultLabel(state string) string {
	switch state {
	case keepAliveSent, "acknowledged":
		return "已发送"
	case keepAliveHit, "verified":
		return "命中"
	case keepAliveMiss, "invalid":
		return "未命中"
	case "":
		return "尚未发送"
	default:
		return "未确认"
	}
}

func (s *NativeService) setSessionKeepAlive(ctx context.Context, input map[string]any) (SessionKeepAliveView, error) {
	id, mode := stringInput(input, "id"), stringInput(input, "mode")
	if id == "" {
		return SessionKeepAliveView{}, errors.New("缺少会话 ID")
	}
	if mode != IdleModeDefault && mode != IdleModeOff && mode != IdleModeCount {
		return SessionKeepAliveView{}, errors.New("保活模式无效")
	}
	now := time.Now().UTC()
	s.idleStateMu.Lock()
	record, err := s.loadSessionKeepAlive(ctx, id)
	if err == nil {
		if mode == IdleModeCount {
			record.TargetCount = intInput(input, "targetCount", record.TargetCount)
			if record.TargetCount < idleKeepAliveMinCount || record.TargetCount > idleKeepAliveMaxCount {
				s.idleStateMu.Unlock()
				return SessionKeepAliveView{}, errors.New("单会话保活次数须为 1–20 次")
			}
		}
		record.Mode = mode
		if record.StoppedReason == "legacy_until_removed" {
			record.StoppedReason, record.StoppedDetail = "", ""
		}
		err = s.saveSessionKeepAlive(ctx, record, now)
	}
	s.idleStateMu.Unlock()
	if err != nil {
		return SessionKeepAliveView{}, err
	}
	view, err := s.sessionKeepAliveView(ctx, id, now)
	if err == nil {
		s.bus.Publish("keepalive", map[string]any{"type": "keepalive.session", "sessionId": id, "view": view})
	}
	return view, err
}
