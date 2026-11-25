package updater

import (
	"context"
	"crypto/ed25519"
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
	"strings"
	"sync"
	"time"

	"enhancedkimi/plugins"
)

const (
	defaultRepo           = "wssz527/kimicode-plus"
	defaultAPI            = "https://api.github.com"
	defaultReleases       = "https://github.com"
	defaultMirrorBase     = "https://cnb.cool"
	perSourceCheckTimeout = 8 * time.Second

	Idle        = "idle"
	Checking    = "checking"
	UpToDate    = "up_to_date"
	Available   = "available"
	Downloading = "downloading"
	Verifying   = "verifying"
	Restarting  = "restarting"
	Updated     = "updated"
	RolledBack  = "rolled_back"
	Failed      = "error"
)

var (
	ErrSafeMode = errors.New("安全模式下无法更新核心，请退出安全模式后重试")
	ErrBusy     = errors.New("核心更新操作正在进行，请稍候")
	repoPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]*/[a-zA-Z0-9_.-]+$`)
)

type Options struct {
	CurrentVersion           string
	Executable               string
	TargetExecutable         func() string
	DataDir                  string
	Safe                     func() bool
	Dispatch                 func(func() error) error
	RestartState             func() RestartState
	RequestRestart           func()
	launchSupervisor         func(string, string) error
	Launchd                  LaunchdOptions
	HealthyModules           func() []string
	APIBaseURL               string
	ReleaseBaseURL           string
	MirrorReleaseBaseURL     string
	AllowInsecureHTTP        bool
	RequireManifestSignature bool
	ManifestPublicKeys       map[string]ed25519.PublicKey
	AllowTemporaryExe        bool
	GOOS                     string
	GOARCH                   string
	HTTPClient               *http.Client
	Now                      func() time.Time
	OnStatusChange           func(Status)
	AutoCheckInitialDelay    time.Duration
	AutoCheckInterval        time.Duration
}

// LaunchdOptions describes the macOS login item that owns the resident core.
// Enabled decides per update whether launchd, not the supervisor, starts the
// candidate; the remaining fields are injectable for tests.
type LaunchdOptions struct {
	Label string
	// Enabled reports whether targetExe is the managed copy in this data
	// directory, which is the one launchd may start.
	Enabled func(targetExe string) bool
	// Managed reports whether the login item is loaded and keeps that copy alive.
	// Only then is the candidate handed to launchd.
	Managed func() bool
}

type Status struct {
	State             string        `json:"state"`
	CurrentVersion    string        `json:"currentVersion"`
	LatestVersion     string        `json:"latestVersion,omitempty"`
	CheckedAt         string        `json:"checkedAt,omitempty"`
	PublishedAt       string        `json:"publishedAt,omitempty"`
	ReleaseURL        string        `json:"releaseUrl,omitempty"`
	ReleaseNotes      string        `json:"releaseNotes,omitempty"`
	UpdateAvailable   bool          `json:"updateAvailable"`
	CanApply          bool          `json:"canApply"`
	CannotApplyReason string        `json:"cannotApplyReason,omitempty"`
	Note              string        `json:"note,omitempty"`
	Error             string        `json:"error,omitempty"`
	DownloadedBytes   int64         `json:"downloadedBytes"`
	TotalBytes        int64         `json:"totalBytes"`
	LastAttempt       *UpdateResult `json:"lastAttempt,omitempty"`
	AutoCheck         bool          `json:"autoCheck"`
}

type asset struct {
	Name         string `json:"name"`
	URL          string `json:"browser_download_url"`
	Size         int64  `json:"size"`
	signedSHA256 string
}

type release struct {
	Tag         string  `json:"tag_name"`
	PublishedAt string  `json:"published_at"`
	URL         string  `json:"html_url"`
	Notes       string  `json:"body"`
	Assets      []asset `json:"assets"`
}

type releaseManifestEntry struct {
	Version      string `json:"version"`
	Target       string `json:"target"`
	Archive      string `json:"archive"`
	PackageBytes int64  `json:"packageBytes"`
	SHA256       string `json:"sha256"`
	PublishedAt  string `json:"publishedAt"`
	ReleaseNotes string `json:"releaseNotes"`
}

type Manager struct {
	options        Options
	current        version
	repo           string
	api            string
	releaseBase    string
	releaseSources []string
	manifest       bool
	client         http.Client
	configErr      error
	ctx            context.Context
	cancel         context.CancelFunc
	operation      sync.Mutex
	mu             sync.RWMutex
	status         Status
	archive        asset
	checksums      asset

	statusWake       chan struct{}
	publishedState   string
	publishedPercent int
	lastCheck        time.Time

	autoCheckWake chan struct{}
	autoCheckOnce sync.Once
	autoCheckDone chan struct{}
}

func New(options Options) *Manager {
	if options.ManifestPublicKeys == nil {
		options.ManifestPublicKeys = plugins.OfficialPublicKeys()
	}
	options.RequireManifestSignature = options.RequireManifestSignature || os.Getenv("EKD_UPDATE_REQUIRE_SIGNATURE") == "1"
	if options.GOOS == "" {
		options.GOOS = runtime.GOOS
	}
	if options.GOARCH == "" {
		options.GOARCH = runtime.GOARCH
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Safe == nil {
		options.Safe = func() bool { return false }
	}
	if options.launchSupervisor == nil {
		options.launchSupervisor = startSupervisor
	}
	if executable, err := filepath.EvalSymlinks(options.Executable); err == nil && options.Executable != "" {
		options.Executable = executable
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{options: options, ctx: ctx, cancel: cancel, status: Status{State: Idle, CurrentVersion: strings.TrimPrefix(options.CurrentVersion, "v")}, autoCheckWake: make(chan struct{}, 1)}
	m.status.AutoCheck = readAutoCheck(options.DataDir)
	if options.HTTPClient != nil {
		m.client = *options.HTTPClient
	}
	checkRedirect := m.client.CheckRedirect
	m.client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 || !m.validURL(req.URL) {
			return errors.New("更新服务重定向地址无效")
		}
		if checkRedirect != nil {
			return checkRedirect(req, via)
		}
		return nil
	}
	m.repo = os.Getenv("EKD_UPDATE_REPO")
	if m.repo == "" {
		m.repo = defaultRepo
	}
	m.api = options.APIBaseURL
	if m.api == "" {
		m.api = os.Getenv("EKD_UPDATE_API_BASE")
	}
	m.manifest = m.api == "" || options.ReleaseBaseURL != "" || options.MirrorReleaseBaseURL != ""
	if m.api == "" {
		m.api = defaultAPI
	}
	m.api = strings.TrimRight(m.api, "/")
	m.releaseBase = strings.TrimRight(options.ReleaseBaseURL, "/")
	if m.releaseBase == "" {
		m.releaseBase = defaultReleases
	}
	mirrorBase := strings.TrimRight(strings.TrimSpace(options.MirrorReleaseBaseURL), "/")
	if mirrorBase == "" {
		envMirror := strings.TrimSpace(os.Getenv("EKD_UPDATE_MIRROR_BASE"))
		switch {
		case strings.EqualFold(envMirror, "off"), strings.EqualFold(envMirror, "none"), strings.EqualFold(envMirror, "disabled"):
			mirrorBase = ""
		case envMirror != "":
			mirrorBase = strings.TrimRight(envMirror, "/")
		case options.ReleaseBaseURL == "" && m.repo == defaultRepo:
			mirrorBase = defaultMirrorBase
		}
	}
	apiURL, urlErr := url.Parse(m.api)
	releaseURL, releaseErr := url.Parse(m.releaseBase)
	var mirrorURL *url.URL
	var mirrorErr error
	if mirrorBase != "" {
		mirrorURL, mirrorErr = url.Parse(mirrorBase)
	}
	current, validVersion := parseVersion(options.CurrentVersion)
	m.current = current
	switch {
	case !repoPattern.MatchString(m.repo) || strings.HasSuffix(m.repo, "/.") || strings.HasSuffix(m.repo, "/.."):
		m.configErr = errors.New("EKD_UPDATE_REPO 必须使用有效的 owner/repo 格式")
	case m.manifest && mirrorBase != "" && (mirrorErr != nil || !m.validURL(mirrorURL) || mirrorURL.RawQuery != "" || mirrorURL.ForceQuery):
		m.configErr = errors.New("更新镜像地址必须是有效的 HTTPS 地址")
	case m.manifest && (releaseErr != nil || !m.validURL(releaseURL) || releaseURL.RawQuery != "" || releaseURL.ForceQuery):
		m.configErr = errors.New("更新 Release 地址必须是有效的 HTTPS 地址")
	case !m.manifest && (urlErr != nil || !m.validURL(apiURL) || apiURL.RawQuery != "" || apiURL.ForceQuery):
		m.configErr = errors.New("更新 API 地址必须是有效的 HTTPS 地址")
	case !validVersion:
		m.configErr = errors.New("当前核心版本号无效，无法比较发布版本")
	}
	if m.configErr == nil {
		if m.manifest {
			if mirrorBase != "" {
				m.releaseSources = append(m.releaseSources, mirrorBase+"/"+m.repo+"/-/releases")
			}
			m.releaseSources = append(m.releaseSources, m.releaseBase+"/"+m.repo+"/releases")
		}
		m.status.ReleaseURL = m.defaultReleaseURL()
	} else {
		m.status.State, m.status.Error = Failed, m.configErr.Error()
	}
	if options.OnStatusChange != nil {
		m.statusWake = make(chan struct{}, 1)
		go m.runStatusNotifier()
	}
	return m
}

func (m *Manager) defaultReleaseURL() string {
	if m.manifest && len(m.releaseSources) > 0 {
		return m.releaseSources[0]
	}
	return m.releaseBase + "/" + m.repo + "/releases"
}

func (m *Manager) validURL(u *url.URL) bool {
	if u == nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	return u.Scheme == "https" || (m.options.AllowInsecureHTTP && u.Scheme == "http" &&
		(u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback()))
}

func (m *Manager) Status() Status {
	m.mu.RLock()
	status := m.status
	m.mu.RUnlock()
	if result := readResult(m.options.DataDir); result.State != "" {
		status.LastAttempt = &result.UpdateResult
		if status.State == Idle || status.State == Restarting || result.State == Restarting {
			status.State, status.LatestVersion, status.Error = result.State, result.TargetVersion, result.Error
			status.UpdateAvailable, status.CanApply, status.CannotApplyReason = false, false, ""
		}
	}
	if status.UpdateAvailable && m.options.Safe() {
		status.CanApply = false
		status.CannotApplyReason = ErrSafeMode.Error()
	}
	return status
}

func (m *Manager) change(update func(*Status)) {
	m.mu.Lock()
	update(&m.status)
	notify := m.statusAdvanced(m.status)
	m.mu.Unlock()
	if notify {
		m.announce()
	}
}

// commit replaces the whole status but keeps the auto-check preference, which
// can change while a check that snapshotted the status is still in flight.
func (m *Manager) commit(status Status) {
	m.change(func(s *Status) {
		status.AutoCheck = s.AutoCheck
		*s = status
	})
}

func (m *Manager) statusAdvanced(status Status) bool {
	percent := 0
	if status.State == Downloading && status.TotalBytes > 0 {
		percent = int(min(status.DownloadedBytes, status.TotalBytes) * 100 / status.TotalBytes)
	}
	if status.State == m.publishedState && percent < m.publishedPercent+5 {
		return false
	}
	m.publishedState, m.publishedPercent = status.State, percent
	return true
}

func (m *Manager) announce() {
	if m.statusWake == nil {
		return
	}
	select {
	case m.statusWake <- struct{}{}:
	default:
	}
}

// Status changes are delivered from this goroutine so a slow subscriber can
// never stall a check, download, or restart.
func (m *Manager) runStatusNotifier() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.statusWake:
			m.options.OnStatusChange(m.Status())
		}
	}
}

func (m *Manager) Close() {
	m.cancel()
	m.operation.Lock()
	m.operation.Unlock()
}

func (m *Manager) begin() error {
	if !m.operation.TryLock() {
		return ErrBusy
	}
	if err := m.ctx.Err(); err != nil {
		m.operation.Unlock()
		return err
	}
	if m.Status().State == Restarting {
		m.operation.Unlock()
		return ErrBusy
	}
	return nil
}

func (m *Manager) Check(ctx context.Context) (Status, error) {
	if err := m.begin(); err != nil {
		return m.Status(), err
	}
	defer m.operation.Unlock()
	return m.check(ctx)
}

// check performs a foreground check for callers that already own m.operation.
func (m *Manager) check(ctx context.Context) (Status, error) {
	return m.checkInternal(ctx, false)
}

func (m *Manager) checkInternal(ctx context.Context, silent bool) (response Status, err error) {
	if m.configErr != nil {
		return m.Status(), m.configErr
	}
	m.mu.RLock()
	previous := m.status
	m.mu.RUnlock()
	previousArchive, previousChecksums := m.archive, m.checksums
	status := Status{State: Checking, CurrentVersion: strings.TrimPrefix(m.options.CurrentVersion, "v"), ReleaseURL: m.defaultReleaseURL()}
	m.archive, m.checksums = asset{}, asset{}
	m.commit(status)
	checkTimeout := 15 * time.Second
	if m.manifest && len(m.releaseSources) > 1 {
		checkTimeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	published, err := m.latest(ctx)
	status.CheckedAt = m.options.Now().UTC().Format(time.RFC3339)
	defer func() {
		if err != nil && silent && preservedOnSilentFailure(previous.State) {
			m.archive, m.checksums = previousArchive, previousChecksums
			m.commit(previous)
			response = m.Status()
			return
		}
		if err == nil {
			m.mu.Lock()
			m.lastCheck = m.options.Now()
			m.mu.Unlock()
		}
		m.commit(status)
		response = m.Status()
	}()
	if err != nil {
		status.State, status.Error = Failed, err.Error()
		return status, err
	}
	status.State = UpToDate
	if published == nil {
		status.Note = "仓库尚未发布正式 Release 版本"
		return status, nil
	}
	latest, valid := parseVersion(published.Tag)
	if !valid {
		err = errors.New("发布版本号无效，无法检查更新")
		status.State, status.Error = Failed, err.Error()
		return status, err
	}
	status.LatestVersion = strings.TrimPrefix(published.Tag, "v")
	if link, parseErr := url.Parse(published.URL); parseErr == nil && m.validURL(link) && link.Scheme == "https" {
		status.ReleaseURL = link.String()
	}
	status.PublishedAt = published.PublishedAt
	status.ReleaseNotes = strings.ToValidUTF8(published.Notes[:min(len(published.Notes), 64<<10)], "")
	if latest.compare(m.current) <= 0 {
		return status, nil
	}
	status.State, status.UpdateAvailable = Available, true
	m.archive, m.checksums = m.selectAssets(published.Assets, status.LatestVersion)
	switch {
	case m.archive.Name == "":
		status.CannotApplyReason = "该版本缺少本平台安装包，请前往发布页查看"
	case m.checksums.Name == "":
		status.CannotApplyReason = "该发布版本缺少 SHA256SUMS 校验文件，无法自动更新"
	default:
		status.CannotApplyReason = m.executableBlocker()
	}
	status.CanApply = status.CannotApplyReason == ""
	return status, nil
}

func (m *Manager) latest(ctx context.Context) (*release, error) {
	if !m.manifest {
		if m.options.RequireManifestSignature {
			return nil, errors.New("核心更新要求使用签名发布清单")
		}
		return m.latestFromAPI(ctx)
	}
	var (
		sawNotFound bool
		firstErr    error
		lastErr     error
	)
	for _, base := range m.releaseSources {
		attemptCtx := ctx
		cancel := func() {}
		if len(m.releaseSources) > 1 {
			attemptCtx, cancel = context.WithTimeout(ctx, perSourceCheckTimeout)
		}
		published, err := m.latestFromManifest(attemptCtx, base)
		cancel()
		if err == nil && published != nil {
			if _, valid := parseVersion(published.Tag); valid {
				return published, nil
			}
			err = errors.New("发布版本号无效，无法检查更新")
		}
		if err == nil {
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
		return nil, nil
	}
	if sawNotFound && lastErr != nil {
		return nil, lastErr
	}
	return nil, firstErr
}

func (m *Manager) latestFromAPI(ctx context.Context) (*release, error) {
	address := m.api + "/repos/" + m.repo + "/releases/latest"
	res, err := m.get(ctx, address, "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("无法连接更新服务：%w", err)
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusNotFound:
		return nil, nil
	case http.StatusOK:
	case http.StatusForbidden:
		if res.Header.Get("X-RateLimit-Remaining") == "0" {
			return nil, errors.New("GitHub API 匿名请求已达每小时上限（HTTP 403），请稍后重试或前往 GitHub 发布页查看")
		}
		return nil, fmt.Errorf("更新服务返回 HTTP %d，请稍后重试", res.StatusCode)
	default:
		return nil, fmt.Errorf("更新服务返回 HTTP %d，请稍后重试", res.StatusCode)
	}
	body, err := readBounded(res.Body, 2<<20)
	if err != nil {
		return nil, errors.New("无法读取发布信息或发布信息超过大小限制")
	}
	var latest release
	if err = json.Unmarshal(body, &latest); err != nil {
		return nil, errors.New("更新服务返回了无效的发布信息")
	}
	return &latest, nil
}

func (m *Manager) latestFromManifest(ctx context.Context, base string) (*release, error) {
	address := base + "/latest/download/manifest.json"
	res, err := m.get(ctx, address, "application/json")
	if err != nil {
		return nil, fmt.Errorf("无法连接更新服务：%w", err)
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusNotFound:
		return nil, nil
	case http.StatusOK:
	default:
		return nil, fmt.Errorf("更新服务返回 HTTP %d，请稍后重试", res.StatusCode)
	}
	body, err := readBounded(res.Body, 2<<20)
	if err != nil {
		return nil, errors.New("无法读取发布信息或发布信息超过大小限制")
	}
	return m.releaseFromManifestAt(body, base)
}

func (m *Manager) releaseFromManifest(body []byte) (*release, error) {
	return m.releaseFromManifestAt(body, m.defaultReleaseURL())
}

func (m *Manager) releaseFromManifestAt(body []byte, base string) (*release, error) {
	payload, signed, err := verifyReleaseManifest(body, m.options.ManifestPublicKeys, m.options.RequireManifestSignature)
	if err != nil {
		return nil, err
	}
	var entries []releaseManifestEntry
	if err := json.Unmarshal(payload, &entries); err != nil || len(entries) == 0 {
		return nil, errors.New("更新服务返回了无效的发布清单")
	}
	version := strings.TrimPrefix(entries[0].Version, "v")
	if version == "" {
		return nil, errors.New("更新服务返回了无效的发布清单")
	}
	latest := &release{
		Tag:         "v" + version,
		PublishedAt: entries[0].PublishedAt,
		URL:         base + "/tag/" + url.PathEscape("v"+version),
		Notes:       entries[0].ReleaseNotes,
	}
	for _, entry := range entries {
		if strings.TrimPrefix(entry.Version, "v") != version || entry.PackageBytes < 0 {
			return nil, errors.New("发布清单包含不一致的版本或无效的包大小")
		}
		if entry.Archive != "" {
			signedSHA256 := ""
			if signed {
				if !releaseDigestPattern.MatchString(entry.SHA256) {
					return nil, errors.New("签名发布清单缺少安装包 SHA-256 校验值")
				}
				signedSHA256 = entry.SHA256
			}
			latest.Assets = append(latest.Assets, asset{
				Name:         entry.Archive,
				URL:          base + "/download/" + url.PathEscape(latest.Tag) + "/" + url.PathEscape(entry.Archive),
				Size:         entry.PackageBytes,
				signedSHA256: signedSHA256,
			})
		}
	}
	latest.Assets = append(latest.Assets, asset{
		Name: "SHA256SUMS",
		URL:  base + "/download/" + url.PathEscape(latest.Tag) + "/SHA256SUMS",
	})
	return latest, nil
}

func (m *Manager) get(ctx context.Context, address, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil || !m.validURL(req.URL) {
		return nil, errors.New("更新下载地址无效")
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "KimiCodePlus/"+m.options.CurrentVersion)
	return m.client.Do(req)
}

func (m *Manager) selectAssets(assets []asset, latest string) (archive, checksums asset) {
	ext := ".tar.gz"
	if m.options.GOOS == "windows" {
		ext = ".zip"
	}
	suffix := "-" + m.options.GOOS + "-" + m.options.GOARCH + ext
	latest = strings.TrimPrefix(latest, "v")
	plainName, prefixedName := "enhanced-kimi-"+latest+suffix, "enhanced-kimi-v"+latest+suffix
	for _, item := range assets {
		link, err := url.Parse(item.URL)
		if err != nil || !m.validURL(link) {
			continue
		}
		switch item.Name {
		case "SHA256SUMS":
			if checksums.Name == "" {
				checksums = item
			}
		case plainName:
			archive = item
		case prefixedName:
			if archive.Name == "" {
				archive = item
			}
		}
	}
	return archive, checksums
}

func (m *Manager) targetExecutable() string {
	if m.options.TargetExecutable != nil {
		if path := m.options.TargetExecutable(); path != "" {
			return path
		}
	}
	return m.options.Executable
}

func (m *Manager) executableBlocker() string {
	const manual = "，请前往发布页手动下载"
	if m.options.GOOS != "darwin" && m.options.GOOS != "linux" && m.options.GOOS != "windows" {
		return "当前系统不支持自动替换核心" + manual
	}
	file := m.targetExecutable()
	if !filepath.IsAbs(file) || !filepath.IsAbs(m.options.DataDir) || m.options.RestartState == nil || m.options.RequestRestart == nil {
		return "当前运行方式不支持自动替换核心" + manual
	}
	for _, part := range strings.Split(filepath.ToSlash(file), "/") {
		if !m.options.AllowTemporaryExe && strings.HasPrefix(part, "go-build") {
			return "当前核心由 go run 临时构建，无法自动替换" + manual
		}
	}
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o222 == 0 {
		return "核心文件不可写" + manual
	}
	dir := filepath.Dir(file)
	if info, err = os.Stat(dir); err != nil || info.Mode().Perm()&0o222 == 0 {
		return "核心所在目录不可写" + manual
	}
	probe, err := os.CreateTemp(dir, ".enhanced-kimi.update-check.*")
	if err != nil {
		return "核心所在目录不可写" + manual
	}
	closeErr := probe.Close()
	removeErr := os.Remove(probe.Name())
	if closeErr != nil || removeErr != nil {
		return "无法在核心所在目录创建更新文件" + manual
	}
	return ""
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = errors.New("文件超过大小限制")
	}
	return data, err
}
