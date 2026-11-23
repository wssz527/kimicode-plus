package plugins

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/redact"
)

const (
	defaultUpdateRepo        = "wssz527/kimicode-plus"
	defaultGitHubReleaseBase = "https://github.com"
	defaultCNBMirrorBase     = "https://cnb.cool"
	pluginCheckPerSourceWait = 8 * time.Second
	pluginDownloadWait       = 60 * time.Second

	UpdateStateIdle     = "idle"
	UpdateStateChecking = "checking"
	UpdateStateReady    = "ready"
	UpdateStateError    = "error"
)

var updateRepoPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]*/[a-zA-Z0-9_.-]+$`)

// UpdateOptions configures the official standalone plugin update channel.
// Sources follow the same "CNB mirror first, GitHub fallback" ordering as the
// core updater, using a dedicated non-latest release tag (`plugins-latest`).
type UpdateOptions struct {
	CoreVersion          string
	Repo                 string
	ReleaseBaseURL       string
	MirrorReleaseBaseURL string
	ReleaseTag           string
	AllowInsecureHTTP    bool
	HTTPClient           *http.Client
	OfficialKeys         map[string]ed25519.PublicKey
	AutoCheckDelay       time.Duration
	AutoCheckInterval    time.Duration
}

// WithUpdater attaches online official plugin update settings to the manager.
func WithUpdater(cfg UpdateOptions) Option {
	return func(o *Options) { o.Updater = cfg }
}

// WithOfficialKeys sets the trusted Ed25519 public keys used to verify signed
// official plugin catalogs and persisted official attestations.
func WithOfficialKeys(keys map[string]ed25519.PublicKey) Option {
	return func(o *Options) { o.OfficialKeys = keys }
}

// UpdateItem describes the update availability of one installed official plugin.
type UpdateItem struct {
	PluginID          string   `json:"pluginId"`
	Name              string   `json:"name,omitempty"`
	CurrentVersion    string   `json:"currentVersion"`
	LatestVersion     string   `json:"latestVersion"`
	UpdateAvailable   bool     `json:"updateAvailable"`
	CanApply          bool     `json:"canApply"`
	CannotApplyReason string   `json:"cannotApplyReason,omitempty"`
	NeedsConsent      bool     `json:"needsConsent"`
	AddedCapabilities []string `json:"addedCapabilities,omitempty"`
	AddedPrivileged   []string `json:"addedPrivileged,omitempty"`
	ArchiveBytes      int64    `json:"archiveBytes,omitempty"`
	PublishedAt       string   `json:"publishedAt,omitempty"`
	ReleaseNotes      string   `json:"releaseNotes,omitempty"`
	MinCoreVersion    string   `json:"minCoreVersion,omitempty"`
	HostCompatible    bool     `json:"hostCompatible"`
}

// UpdateStatus is the snapshot returned by GET /plugins/updates and POST /plugins/updates/check.
type UpdateStatus struct {
	State          string                `json:"state"`
	CheckedAt      string                `json:"checkedAt,omitempty"`
	Source         string                `json:"source,omitempty"`
	Error          string                `json:"error,omitempty"`
	AvailableCount int                   `json:"availableCount"`
	Items          map[string]UpdateItem `json:"items"`
}

// UpdateRequest carries optional confirmation flags when applying a plugin update.
type UpdateRequest struct {
	ExpectedVersion     string `json:"expectedVersion,omitempty"`
	ConfirmCapabilities bool   `json:"confirmCapabilities,omitempty"`
	ConfirmPrivileged   bool   `json:"confirmPrivileged,omitempty"`
}

type pluginUpdater struct {
	coreVersion       string
	releaseTag        string
	sources           []string
	allowInsecureHTTP bool
	client            http.Client
	configErr         error
	autoCheckDelay    time.Duration
	autoCheckInterval time.Duration

	opMu          sync.Mutex
	mu            sync.RWMutex
	state         string
	checkedAt     time.Time
	activeSource  string
	lastErr       string
	catalog       *OfficialPluginCatalog
	signedRaw     []byte
	autoCheckOnce sync.Once
	stopCtx       context.Context
	stopCancel    context.CancelFunc
}

func newPluginUpdater(cfg UpdateOptions) *pluginUpdater {
	ctx, cancel := context.WithCancel(context.Background())
	u := &pluginUpdater{
		coreVersion:       strings.TrimPrefix(strings.TrimSpace(cfg.CoreVersion), "v"),
		releaseTag:        strings.TrimSpace(cfg.ReleaseTag),
		allowInsecureHTTP: cfg.AllowInsecureHTTP,
		state:             UpdateStateIdle,
		autoCheckDelay:    cfg.AutoCheckDelay,
		autoCheckInterval: cfg.AutoCheckInterval,
		stopCtx:           ctx,
		stopCancel:        cancel,
	}
	if u.releaseTag == "" {
		u.releaseTag = DefaultPluginReleaseTag
	}
	if cfg.HTTPClient != nil {
		u.client = *cfg.HTTPClient
	}
	prevRedirect := u.client.CheckRedirect
	u.client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || !u.validURL(req.URL) {
			return errors.New("插件更新服务重定向地址无效")
		}
		if prevRedirect != nil {
			return prevRedirect(req, via)
		}
		return nil
	}

	repo := strings.TrimSpace(cfg.Repo)
	if repo == "" {
		repo = strings.TrimSpace(os.Getenv("EKD_UPDATE_REPO"))
	}
	if repo == "" {
		repo = defaultUpdateRepo
	}
	releaseBase := strings.TrimRight(strings.TrimSpace(cfg.ReleaseBaseURL), "/")
	if releaseBase == "" {
		releaseBase = defaultGitHubReleaseBase
	}
	mirrorBase := strings.TrimRight(strings.TrimSpace(cfg.MirrorReleaseBaseURL), "/")
	if mirrorBase == "" {
		envMirror := strings.TrimSpace(os.Getenv("EKD_UPDATE_MIRROR_BASE"))
		switch {
		case strings.EqualFold(envMirror, "off"), strings.EqualFold(envMirror, "none"), strings.EqualFold(envMirror, "disabled"):
			mirrorBase = ""
		case envMirror != "":
			mirrorBase = strings.TrimRight(envMirror, "/")
		case cfg.ReleaseBaseURL == "" && repo == defaultUpdateRepo:
			mirrorBase = defaultCNBMirrorBase
		}
	}

	releaseURL, releaseErr := url.Parse(releaseBase)
	var mirrorURL *url.URL
	var mirrorErr error
	if mirrorBase != "" {
		mirrorURL, mirrorErr = url.Parse(mirrorBase)
	}
	switch {
	case !updateRepoPattern.MatchString(repo) || strings.HasSuffix(repo, "/.") || strings.HasSuffix(repo, "/.."):
		u.configErr = errors.New("EKD_UPDATE_REPO 必须使用有效的 owner/repo 格式")
	case mirrorBase != "" && (mirrorErr != nil || !u.validURL(mirrorURL) || mirrorURL.RawQuery != "" || mirrorURL.ForceQuery):
		u.configErr = errors.New("插件更新镜像地址必须是有效的 HTTPS 地址")
	case releaseErr != nil || !u.validURL(releaseURL) || releaseURL.RawQuery != "" || releaseURL.ForceQuery:
		u.configErr = errors.New("插件更新 Release 地址必须是有效的 HTTPS 地址")
	}
	if u.configErr == nil {
		if mirrorBase != "" {
			u.sources = append(u.sources, mirrorBase+"/"+repo+"/-/releases")
		}
		u.sources = append(u.sources, releaseBase+"/"+repo+"/releases")
	}
	return u
}

func (u *pluginUpdater) close() {
	if u == nil {
		return
	}
	u.stopCancel()
}

func (u *pluginUpdater) validURL(parsed *url.URL) bool {
	if parsed == nil || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || parsed.Opaque != "" {
		return false
	}
	return parsed.Scheme == "https" || (u.allowInsecureHTTP && parsed.Scheme == "http" &&
		(parsed.Hostname() == "localhost" || net.ParseIP(parsed.Hostname()).IsLoopback()))
}

func (u *pluginUpdater) assetURL(base, filename string) string {
	return base + "/download/" + url.PathEscape(u.releaseTag) + "/" + url.PathEscape(filename)
}

func (u *pluginUpdater) get(ctx context.Context, address, accept string) (*http.Response, error) {
	parsed, err := url.Parse(address)
	if err != nil || !u.validURL(parsed) {
		return nil, errors.New("插件更新地址无效")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	ua := "KimiCodePlus"
	if u.coreVersion != "" {
		ua += "/" + u.coreVersion
	}
	req.Header.Set("User-Agent", ua)
	return u.client.Do(req)
}

func (u *pluginUpdater) orderedDownloadSources() []string {
	u.mu.RLock()
	active := u.activeSource
	u.mu.RUnlock()
	out := make([]string, 0, len(u.sources))
	if active != "" {
		out = append(out, active)
	}
	for _, base := range u.sources {
		if base == active {
			continue
		}
		out = append(out, base)
	}
	return out
}

// StartAutoUpdateCheck periodically runs silent plugin update checks while the
// manager stays open. Failures never overwrite an already valid check snapshot.
func (m *Manager) StartAutoUpdateCheck() {
	if m.updater == nil || len(m.officialKeys) == 0 {
		return
	}
	m.updater.autoCheckOnce.Do(func() {
		delay := m.updater.autoCheckDelay
		if delay <= 0 {
			delay = 25 * time.Second
		}
		interval := m.updater.autoCheckInterval
		if interval <= 0 {
			interval = 4 * time.Hour
		}
		go func() {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			for {
				select {
				case <-m.updater.stopCtx.Done():
					return
				case <-timer.C:
					_, _ = m.checkUpdatesInternal(m.updater.stopCtx, true)
					timer.Reset(interval)
				}
			}
		}()
	})
}

// UpdateStatus returns the cached plugin update check evaluated against the
// currently installed plugin versions, grants, host version and safe mode.
func (m *Manager) UpdateStatus() UpdateStatus {
	if m.updater == nil {
		return UpdateStatus{State: UpdateStateIdle, Items: map[string]UpdateItem{}}
	}
	m.updater.mu.RLock()
	state := m.updater.state
	checkedAt := m.updater.checkedAt
	source := m.updater.activeSource
	lastErr := m.updater.lastErr
	var catalog *OfficialPluginCatalog
	if m.updater.catalog != nil {
		cp := *m.updater.catalog
		cp.Plugins = append([]OfficialPluginEntry(nil), m.updater.catalog.Plugins...)
		catalog = &cp
	}
	m.updater.mu.RUnlock()

	out := UpdateStatus{
		State:  state,
		Source: source,
		Error:  lastErr,
		Items:  map[string]UpdateItem{},
	}
	if !checkedAt.IsZero() {
		out.CheckedAt = checkedAt.UTC().Format(time.RFC3339)
	}
	if catalog == nil {
		return out
	}

	m.mu.Lock()
	host := m.host
	installed := make([]entry, 0, len(m.order))
	for _, id := range m.order {
		if item := m.entries[id]; item != nil {
			installed = append(installed, *item)
		}
	}
	m.mu.Unlock()
	safe := m.safe()

	for _, item := range installed {
		if item.source != SourceBundled {
			continue
		}
		remote, ok := catalog.Find(item.manifest.ID)
		if !ok {
			continue
		}
		evaluated := m.evaluateUpdate(item, remote, host, safe)
		out.Items[item.manifest.ID] = evaluated
		if evaluated.UpdateAvailable {
			out.AvailableCount++
		}
	}
	return out
}

func (m *Manager) evaluateUpdate(item entry, remote OfficialPluginEntry, host string, safe bool) UpdateItem {
	available := newerVersion(remote.Version, item.manifest.Version)
	hostOK := host == "" || hostVersionListSupports(remote.HostVersions, host)
	added, addedPriv := diffCapabilities(item.grants.Names(), remote.Capabilities)
	notes := strings.ToValidUTF8(remote.ReleaseNotes[:min(len(remote.ReleaseNotes), 8<<10)], "")
	res := UpdateItem{
		PluginID:          item.manifest.ID,
		Name:              remote.Name,
		CurrentVersion:    item.manifest.Version,
		LatestVersion:     remote.Version,
		UpdateAvailable:   available,
		NeedsConsent:      len(added) > 0,
		AddedCapabilities: added,
		AddedPrivileged:   addedPriv,
		ArchiveBytes:      remote.ArchiveBytes,
		PublishedAt:       remote.PublishedAt,
		ReleaseNotes:      notes,
		MinCoreVersion:    remote.MinCoreVersion,
		HostCompatible:    hostOK,
	}
	if !available {
		return res
	}
	switch {
	case safe:
		res.CannotApplyReason = "安全模式下无法更新插件，请先关闭安全模式"
	case m.pluginsDir() == "":
		res.CannotApplyReason = "没有可用的数据目录，无法更新插件"
	case remote.APIVersion > APIVersion:
		res.CannotApplyReason = "该插件需要更新版本的插件接口，请先更新 KimiCode+ 核心"
	case remote.MinCoreVersion != "" && m.updater != nil && m.updater.coreVersion != "":
		if cmp, ok := compareCoreSemver(m.updater.coreVersion, remote.MinCoreVersion); ok && cmp < 0 {
			res.CannotApplyReason = fmt.Sprintf("该插件版本需要 KimiCode+ v%s 或更高版本，请先更新核心", strings.TrimPrefix(remote.MinCoreVersion, "v"))
		}
	}
	res.CanApply = res.CannotApplyReason == ""
	return res
}

func hostVersionListSupports(patterns []string, host string) bool {
	return Manifest{HostVersions: patterns}.HostVersionSupported(host)
}

func diffCapabilities(currentGranted, nextDeclared []string) (added []string, addedPrivileged []string) {
	have := make(map[string]bool, len(currentGranted))
	for _, capName := range currentGranted {
		have[capName] = true
	}
	for _, raw := range nextDeclared {
		parsed, err := ParseCapabilities([]string{raw})
		if err != nil {
			continue
		}
		for _, norm := range parsed.Names() {
			if have[norm] {
				continue
			}
			have[norm] = true
			added = append(added, norm)
			if RequiresPrivilege(norm) {
				addedPrivileged = append(addedPrivileged, norm)
			}
		}
	}
	sort.Strings(added)
	sort.Strings(addedPrivileged)
	return added, addedPrivileged
}

// CheckUpdates fetches and verifies the signed official plugin catalog using
// the configured release sources (CNB mirror first, GitHub fallback).
func (m *Manager) CheckUpdates(ctx context.Context) (UpdateStatus, error) {
	return m.checkUpdatesInternal(ctx, false)
}

func (m *Manager) checkUpdatesInternal(ctx context.Context, silent bool) (UpdateStatus, error) {
	if m.updater == nil {
		return m.UpdateStatus(), apiError(http.StatusServiceUnavailable, "PLUGIN_UPDATE_UNCONFIGURED", "当前构建未启用插件在线更新")
	}
	if m.updater.configErr != nil {
		return m.UpdateStatus(), apiError(http.StatusInternalServerError, "PLUGIN_UPDATE_CONFIG_INVALID", m.updater.configErr.Error())
	}
	if len(m.officialKeys) == 0 {
		return m.UpdateStatus(), apiError(http.StatusServiceUnavailable, "PLUGIN_UPDATE_KEY_MISSING", "未配置官方插件签名公钥")
	}
	if !m.updater.opMu.TryLock() {
		return m.UpdateStatus(), apiError(http.StatusConflict, "PLUGIN_UPDATE_BUSY", "正在检查或应用插件更新，请稍候")
	}
	defer m.updater.opMu.Unlock()

	m.updater.mu.RLock()
	prevState := m.updater.state
	prevCheckedAt := m.updater.checkedAt
	prevSource := m.updater.activeSource
	prevCatalog := m.updater.catalog
	prevRaw := m.updater.signedRaw
	m.updater.mu.RUnlock()

	if !silent {
		m.updater.mu.Lock()
		m.updater.state = UpdateStateChecking
		m.updater.lastErr = ""
		m.updater.mu.Unlock()
	}

	overallTimeout := 15 * time.Second
	if len(m.updater.sources) > 1 {
		overallTimeout = 20 * time.Second
	}
	checkCtx, cancel := context.WithTimeout(ctx, overallTimeout)
	defer cancel()
	stop := context.AfterFunc(m.updater.stopCtx, cancel)
	defer stop()

	catalog, raw, source, notPublished, fetchErr := m.fetchCatalogWithFallback(checkCtx)
	now := m.now()

	if fetchErr != nil {
		if silent && (prevState == UpdateStateReady || prevState == UpdateStateIdle) {
			m.updater.mu.Lock()
			m.updater.state = prevState
			m.updater.checkedAt = prevCheckedAt
			m.updater.activeSource = prevSource
			m.updater.catalog = prevCatalog
			m.updater.signedRaw = prevRaw
			m.updater.mu.Unlock()
			return m.UpdateStatus(), fetchErr
		}
		m.updater.mu.Lock()
		m.updater.state = UpdateStateError
		m.updater.checkedAt = now
		m.updater.lastErr = fetchErr.Error()
		m.updater.mu.Unlock()
		return m.UpdateStatus(), apiError(http.StatusBadGateway, "PLUGIN_UPDATE_CHECK_FAILED", fetchErr.Error())
	}

	if notPublished {
		empty := OfficialPluginCatalog{}
		m.updater.mu.Lock()
		m.updater.state = UpdateStateReady
		m.updater.checkedAt = now
		m.updater.activeSource = source
		m.updater.lastErr = ""
		m.updater.catalog = &empty
		m.updater.signedRaw = nil
		m.updater.mu.Unlock()
		return m.UpdateStatus(), nil
	}

	m.updater.mu.Lock()
	m.updater.state = UpdateStateReady
	m.updater.checkedAt = now
	m.updater.activeSource = source
	m.updater.lastErr = ""
	m.updater.catalog = &catalog
	m.updater.signedRaw = raw
	m.updater.mu.Unlock()
	return m.UpdateStatus(), nil
}

func (m *Manager) fetchCatalogWithFallback(ctx context.Context) (OfficialPluginCatalog, []byte, string, bool, error) {
	var (
		sawNotFound bool
		firstErr    error
		lastErr     error
	)
	for _, base := range m.updater.sources {
		attemptCtx := ctx
		cancel := func() {}
		if len(m.updater.sources) > 1 {
			attemptCtx, cancel = context.WithTimeout(ctx, pluginCheckPerSourceWait)
		}
		catalog, raw, notFound, err := m.fetchCatalogFrom(attemptCtx, base)
		cancel()
		if err == nil && !notFound {
			return catalog, raw, base, false, nil
		}
		if err == nil && notFound {
			sawNotFound = true
			continue
		}
		if firstErr == nil {
			firstErr = err
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	if sawNotFound && firstErr == nil {
		source := ""
		if len(m.updater.sources) > 0 {
			source = m.updater.sources[0]
		}
		return OfficialPluginCatalog{}, nil, source, true, nil
	}
	if sawNotFound && lastErr != nil {
		return OfficialPluginCatalog{}, nil, "", false, lastErr
	}
	return OfficialPluginCatalog{}, nil, "", false, firstErr
}

func (m *Manager) fetchCatalogFrom(ctx context.Context, base string) (OfficialPluginCatalog, []byte, bool, error) {
	address := m.updater.assetURL(base, PluginManifestAssetName)
	res, err := m.updater.get(ctx, address, "application/json")
	if err != nil {
		return OfficialPluginCatalog{}, nil, false, fmt.Errorf("无法连接插件更新服务：%w", err)
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusNotFound:
		return OfficialPluginCatalog{}, nil, true, nil
	case http.StatusOK:
	default:
		return OfficialPluginCatalog{}, nil, false, fmt.Errorf("插件更新服务返回 HTTP %d，请稍后重试", res.StatusCode)
	}
	body, err := readBoundedBytes(res.Body, maxPluginManifestBytes)
	if err != nil {
		return OfficialPluginCatalog{}, nil, false, errors.New("无法读取插件更新清单或文件过大")
	}
	catalog, err := VerifySignedPluginManifest(body, m.officialKeys)
	if err != nil {
		return OfficialPluginCatalog{}, nil, false, err
	}
	return catalog, body, false, nil
}

// ApplyUpdate downloads, verifies, and atomically installs a signed official
// plugin update. Running plugins are disabled, swapped, and re-enabled so the
// renderer hot-reloads the new module without restarting Kimi Code.
func (m *Manager) ApplyUpdate(ctx context.Context, id string, req UpdateRequest) (Plugin, error) {
	if m.safe() {
		return Plugin{}, apiError(http.StatusConflict, "SAFE_MODE", "安全模式下无法更新插件")
	}
	if m.updater == nil {
		return Plugin{}, apiError(http.StatusServiceUnavailable, "PLUGIN_UPDATE_UNCONFIGURED", "当前构建未启用插件在线更新")
	}
	if m.updater.configErr != nil {
		return Plugin{}, apiError(http.StatusInternalServerError, "PLUGIN_UPDATE_CONFIG_INVALID", m.updater.configErr.Error())
	}
	if len(m.officialKeys) == 0 {
		return Plugin{}, apiError(http.StatusServiceUnavailable, "PLUGIN_UPDATE_KEY_MISSING", "未配置官方插件签名公钥")
	}
	if m.pluginsDir() == "" {
		return Plugin{}, apiError(http.StatusConflict, "PLUGIN_STORAGE_UNAVAILABLE", "没有可用的数据目录，无法更新插件")
	}

	m.mu.Lock()
	live, ok := m.entries[id]
	var snapshot entry
	host := m.host
	if ok && live != nil {
		snapshot = *live
	}
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return Plugin{}, m.closedError()
	}
	if !ok {
		return Plugin{}, apiError(http.StatusNotFound, "PLUGIN_NOT_FOUND", "插件未安装")
	}
	if snapshot.source != SourceBundled {
		return Plugin{}, apiError(http.StatusConflict, "PLUGIN_LOCAL_UPDATE_UNSUPPORTED", "本地安装的插件不支持官方在线更新，请重新从本地安装或恢复预装版本")
	}

	m.updater.mu.RLock()
	hasCatalog := m.updater.catalog != nil && len(m.updater.signedRaw) > 0
	m.updater.mu.RUnlock()
	if !hasCatalog {
		if _, err := m.CheckUpdates(ctx); err != nil {
			return Plugin{}, err
		}
	}

	if !m.updater.opMu.TryLock() {
		return Plugin{}, apiError(http.StatusConflict, "PLUGIN_UPDATE_BUSY", "正在检查或应用插件更新，请稍候")
	}
	defer m.updater.opMu.Unlock()

	m.updater.mu.RLock()
	catalog := m.updater.catalog
	signedRaw := append([]byte(nil), m.updater.signedRaw...)
	m.updater.mu.RUnlock()
	if catalog == nil || len(signedRaw) == 0 {
		return Plugin{}, apiError(http.StatusNotFound, "PLUGIN_UPDATE_UNAVAILABLE", "当前没有可用的官方插件更新")
	}
	remote, found := catalog.Find(id)
	if !found {
		return Plugin{}, apiError(http.StatusNotFound, "PLUGIN_UPDATE_UNAVAILABLE", "官方更新通道中未找到该插件")
	}
	if req.ExpectedVersion != "" && strings.TrimSpace(req.ExpectedVersion) != remote.Version {
		return Plugin{}, apiError(http.StatusConflict, "PLUGIN_UPDATE_VERSION_CHANGED", "可用插件版本已变化，请重新检查更新")
	}
	eval := m.evaluateUpdate(snapshot, remote, host, m.safe())
	if !eval.UpdateAvailable {
		return Plugin{}, apiError(http.StatusConflict, "PLUGIN_UP_TO_DATE", "插件已是最新版本")
	}
	if !eval.CanApply {
		return Plugin{}, apiError(http.StatusConflict, "PLUGIN_UPDATE_BLOCKED", eval.CannotApplyReason)
	}
	if err := checkUpdateConsent(eval.AddedCapabilities, eval.AddedPrivileged, req); err != nil {
		return Plugin{}, err
	}

	if err := os.MkdirAll(m.pluginsDir(), 0o700); err != nil {
		return Plugin{}, apiError(http.StatusInternalServerError, "PLUGIN_STORAGE_UNAVAILABLE", "无法创建插件目录")
	}
	stageDir, err := os.MkdirTemp(m.pluginsDir(), ".update-stage-*")
	if err != nil {
		return Plugin{}, apiError(http.StatusInternalServerError, "PLUGIN_UPDATE_FAILED", "无法创建插件更新临时目录")
	}
	defer os.RemoveAll(stageDir)

	zipPath := filepath.Join(stageDir, "package.zip")
	if err := m.downloadPluginArchiveWithFallback(ctx, remote, zipPath); err != nil {
		return Plugin{}, err
	}

	pluginStage := filepath.Join(stageDir, "plugin")
	if err := os.MkdirAll(pluginStage, 0o700); err != nil {
		return Plugin{}, apiError(http.StatusInternalServerError, "PLUGIN_UPDATE_FAILED", "无法准备插件解压目录")
	}
	if err := extractZip(zipPath, pluginStage); err != nil {
		return Plugin{}, apiError(http.StatusBadGateway, "PLUGIN_PACKAGE_INVALID", "插件更新包解压校验失败："+redact.Text(err.Error()))
	}
	manifestBytes, err := os.ReadFile(filepath.Join(pluginStage, "plugin.json"))
	if err != nil {
		return Plugin{}, apiError(http.StatusBadGateway, "PLUGIN_MANIFEST_INVALID", "插件更新包缺少 plugin.json")
	}
	manifest, err := ParseManifest(manifestBytes)
	if err != nil {
		return Plugin{}, apiError(http.StatusBadGateway, "PLUGIN_MANIFEST_INVALID", "插件更新包清单无效："+redact.Text(err.Error()))
	}
	if manifest.ID != id || manifest.Version != remote.Version || manifest.APIVersion != remote.APIVersion {
		return Plugin{}, apiError(http.StatusBadGateway, "PLUGIN_PACKAGE_MISMATCH", "插件更新包元数据与签名清单不一致")
	}
	if err := manifest.ValidateFiles(pluginStage); err != nil {
		return Plugin{}, apiError(http.StatusBadGateway, "PLUGIN_FILES_INVALID", "插件更新包文件校验失败："+redact.Text(err.Error()))
	}
	digest, err := DirDigest(pluginStage)
	if err != nil || digest != remote.ContentDigest {
		return Plugin{}, apiError(http.StatusBadGateway, "PLUGIN_DIGEST_MISMATCH", "插件更新包内容摘要与官方签名不一致，已拒绝安装")
	}

	// Re-verify capability diff against the actual unpacked manifest.
	actualAdded, actualAddedPriv := diffCapabilities(snapshot.grants.Names(), manifest.Capabilities)
	if err := checkUpdateConsent(actualAdded, actualAddedPriv, req); err != nil {
		return Plugin{}, err
	}

	pkg := stagedPackage{
		dir:      stageDir,
		manifest: manifest,
		data:     manifestBytes,
		digest:   digest,
	}
	updated, err := m.commitOfficialUpdate(ctx, pkg, signedRaw, req.ConfirmCapabilities)
	if err != nil {
		return Plugin{}, err
	}
	return updated, nil
}

func checkUpdateConsent(added, addedPriv []string, req UpdateRequest) error {
	if len(added) > 0 && !req.ConfirmCapabilities {
		return apiError(http.StatusConflict, "PLUGIN_UPDATE_CONSENT_REQUIRED", "新版本申请了额外权限，需要确认后再更新")
	}
	if len(addedPriv) > 0 && !req.ConfirmPrivileged {
		return apiError(http.StatusConflict, "PLUGIN_PRIVILEGED_CONFIRM_REQUIRED", "新版本申请了高权限能力，需要明确确认风险后再更新")
	}
	return nil
}

func (m *Manager) downloadPluginArchiveWithFallback(ctx context.Context, remote OfficialPluginEntry, destPath string) error {
	sources := m.updater.orderedDownloadSources()
	var lastErr error
	for _, base := range sources {
		attemptCtx, cancel := context.WithTimeout(ctx, pluginDownloadWait)
		stop := context.AfterFunc(m.updater.stopCtx, cancel)
		err := m.downloadPluginArchiveFrom(attemptCtx, base, remote, destPath)
		stop()
		cancel()
		if err == nil {
			return nil
		}
		_ = os.Remove(destPath)
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		return apiError(http.StatusBadGateway, "PLUGIN_DOWNLOAD_FAILED", "没有可用的插件下载源")
	}
	return apiError(http.StatusBadGateway, "PLUGIN_DOWNLOAD_FAILED", lastErr.Error())
}

func (m *Manager) downloadPluginArchiveFrom(ctx context.Context, base string, remote OfficialPluginEntry, destPath string) error {
	address := m.updater.assetURL(base, remote.Archive)
	res, err := m.updater.get(ctx, address, "application/octet-stream")
	if err != nil {
		return fmt.Errorf("无法下载插件包：%w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("下载插件包失败，HTTP %d", res.StatusCode)
	}
	if res.ContentLength > zipLimit {
		return errors.New("插件包超过大小限制")
	}
	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(out, hash), io.LimitReader(res.Body, zipLimit+1))
	closeErr := out.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return fmt.Errorf("保存插件包失败：%w", err)
	}
	if written > zipLimit {
		return errors.New("插件包超过大小限制")
	}
	if written != remote.ArchiveBytes {
		return errors.New("插件包大小与签名清单不一致")
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != remote.ArchiveSHA256 {
		return errors.New("插件包 SHA-256 校验失败，已丢弃")
	}
	return nil
}

func (m *Manager) commitOfficialUpdate(ctx context.Context, pkg stagedPackage, signedManifest []byte, grantAdded bool) (Plugin, error) {
	m.installMu.Lock()
	defer m.installMu.Unlock()

	manifest := pkg.manifest
	id := manifest.ID
	target := filepath.Join(m.pluginsDir(), manifest.DataDirName())

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Plugin{}, m.closedError()
	}
	live := m.entries[id]
	if live == nil {
		m.mu.Unlock()
		return Plugin{}, apiError(http.StatusNotFound, "PLUGIN_NOT_FOUND", "插件未安装")
	}
	if live.source != SourceBundled {
		m.mu.Unlock()
		return Plugin{}, apiError(http.StatusConflict, "PLUGIN_LOCAL_UPDATE_UNSUPPORTED", "本地安装的插件不支持官方在线更新")
	}
	prevEntry := *live
	wasLive := live.state == StateEnabled
	wanted := live.enabled
	wasForced := live.forced
	m.mu.Unlock()

	if !newerVersion(manifest.Version, prevEntry.manifest.Version) {
		return Plugin{}, apiError(http.StatusConflict, "PLUGIN_UP_TO_DATE", "插件已是最新版本")
	}

	grants, err := computeUpdatedGrants(prevEntry.grants, manifest, grantAdded)
	if err != nil {
		return Plugin{}, err
	}

	if wasLive {
		if err := m.Disable(ctx, id); err != nil {
			return Plugin{}, err
		}
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Plugin{}, m.closedError()
	}
	if cur := m.entries[id]; cur != nil && cur.state == StateEnabled {
		m.mu.Unlock()
		return Plugin{}, apiError(http.StatusConflict, "PLUGIN_UPDATE_FAILED", "插件正在运行，无法替换文件")
	}

	backup := ""
	if _, err := os.Lstat(target); err == nil {
		backup = target + ".previous"
		if err := os.RemoveAll(backup); err != nil {
			m.mu.Unlock()
			return Plugin{}, apiError(http.StatusInternalServerError, "PLUGIN_UPDATE_FAILED", "无法清理旧插件备份")
		}
		if err := renamePluginDir(target, backup); err != nil {
			m.mu.Unlock()
			return Plugin{}, apiError(http.StatusInternalServerError, "PLUGIN_UPDATE_FAILED", "无法备份现有插件目录")
		}
	} else if !os.IsNotExist(err) {
		m.mu.Unlock()
		return Plugin{}, apiError(http.StatusInternalServerError, "PLUGIN_UPDATE_FAILED", "无法检查现有插件目录")
	}

	if err := renamePluginDir(pkg.pluginDir(), target); err != nil {
		restoreLocalInstall(target, backup)
		m.mu.Unlock()
		if wanted {
			_, _ = m.enable(ctx, id, wasForced)
		}
		return Plugin{}, apiError(http.StatusInternalServerError, "PLUGIN_UPDATE_FAILED", "无法写入新版本插件文件")
	}

	digest, err := DirDigest(target)
	if err != nil || digest != pkg.digest {
		restoreLocalInstall(target, backup)
		m.mu.Unlock()
		if wanted {
			_, _ = m.enable(ctx, id, wasForced)
		}
		return Plugin{}, apiError(http.StatusInternalServerError, "PLUGIN_UPDATE_FAILED", "安装后的插件摘要校验失败，已回滚原版本")
	}

	now := m.now()
	nextItem := &entry{
		manifest:         manifest,
		grants:           grants,
		declared:         manifest.CapabilityNames(),
		source:           SourceBundled,
		dir:              target,
		installDigest:    digest,
		officialManifest: append([]byte(nil), signedManifest...),
		state:            StateDisabled,
		enabled:          wanted,
		forced:           wanted && !m.compatible(manifest),
		installed:        prevEntry.installed,
		updated:          now,
	}
	if nextItem.installed.IsZero() {
		nextItem.installed = now
	}
	stored := record{
		id:               manifest.ID,
		name:             manifest.Name,
		version:          manifest.Version,
		manifest:         pkg.data,
		capabilities:     grants.Names(),
		source:           SourceBundled,
		installDigest:    digest,
		officialManifest: nextItem.officialManifest,
		desired:          wanted,
		state:            StateDisabled,
		installedAt:      nextItem.installed,
		updatedAt:        now,
	}
	if err := saveRecord(ctx, m.db, stored); err != nil {
		restoreLocalInstall(target, backup)
		m.mu.Unlock()
		if wanted {
			_, _ = m.enable(ctx, id, wasForced)
		}
		return Plugin{}, apiError(http.StatusInternalServerError, "PLUGIN_STATE_UNAVAILABLE", "无法保存插件更新状态，已回滚原版本")
	}

	m.entries[id] = nextItem
	host := m.host
	m.mu.Unlock()

	if backup != "" {
		_ = os.RemoveAll(backup)
	}
	_ = recordActivity(ctx, m.db, id, ActivityUpgrade, fmt.Sprintf("已在线更新到 %s（原版本 %s）", manifest.Version, prevEntry.manifest.Version), now)

	if wanted {
		reEnabled, enableErr := m.enable(ctx, id, wasForced)
		if enableErr == nil {
			return reEnabled, nil
		}
		_ = recordActivity(ctx, m.db, id, ActivityError, "更新后重新启用失败："+redact.Text(enableErr.Error()), m.now())
	}
	m.notifyChange()
	return m.present(*nextItem, host), nil
}

func computeUpdatedGrants(existing Grants, manifest Manifest, grantAdded bool) (Grants, error) {
	declared, err := ParseCapabilities(manifest.Capabilities)
	if err != nil {
		return Grants{}, apiError(http.StatusBadGateway, "PLUGIN_MANIFEST_INVALID", redact.Text(err.Error()))
	}
	if grantAdded {
		return declared, nil
	}
	// Preserve only previously granted capabilities that are still declared.
	declaredSet := make(map[string]bool, len(declared.Names()))
	for _, name := range declared.Names() {
		declaredSet[name] = true
	}
	kept := make([]string, 0, len(existing.Names()))
	for _, name := range existing.Names() {
		if declaredSet[name] {
			kept = append(kept, name)
		}
	}
	return ParseCapabilities(kept)
}

func renamePluginDir(src, dst string) error {
	var err error
	attempts := 1
	if runtime.GOOS == "windows" {
		attempts = 4
	}
	for i := 0; i < attempts; i++ {
		err = os.Rename(src, dst)
		if err == nil {
			return nil
		}
		if i+1 < attempts {
			time.Sleep(time.Duration(i+1) * 50 * time.Millisecond)
		}
	}
	return err
}

func readBoundedBytes(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("response exceeds size limit")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("response is not valid UTF-8")
	}
	return data, nil
}

func (m *Manager) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, m.UpdateStatus())
}

func (m *Manager) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	status, err := m.CheckUpdates(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, status)
}

func (m *Manager) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var req UpdateRequest
	if r.Body != nil {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		if err != nil {
			httpx.Fail(w, http.StatusBadRequest, "INVALID_INPUT", "无法读取请求内容")
			return
		}
		if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 {
			dec := json.NewDecoder(bytes.NewReader(trimmed))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&req); err != nil {
				httpx.Fail(w, http.StatusBadRequest, "INVALID_INPUT", "插件更新请求参数格式无效")
				return
			}
		}
	}
	plugin, err := m.ApplyUpdate(r.Context(), id, req)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, plugin)
}
