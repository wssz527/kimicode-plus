package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// APIVersion is the newest plugin API the host implements. Version 1 manifests
// keep their exact meaning; version 2 adds the privileged capabilities listed in
// capability.go. A manifest that asks for anything else is refused instead of
// guessed at.
const APIVersion = 2

// ManifestLimit bounds plugin.json. A manifest is a declaration, not data.
const ManifestLimit = 64 << 10

var (
	idPattern           = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}\.[a-z0-9][a-z0-9-]{0,31}$`)
	versionPattern      = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	hostPattern         = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,2}$|^[0-9]+(\.[0-9]+){0,1}\.\*$`)
	upstreamRepoPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)
	entryPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
)

// Entry names the ES modules the host loads. Both are relative to the plugin
// root and must stay inside it.
type Entry struct {
	Renderer string `json:"renderer"`
	Settings string `json:"settings,omitempty"`
}

// Upstream records where a ported plugin came from, so the plugin page and the
// license collector can attribute it.
type Upstream struct {
	Repo    string `json:"repo,omitempty"`
	Commit  string `json:"commit,omitempty"`
	License string `json:"license,omitempty"`
}

type PluginUpstream struct {
	UpstreamRepo    string `json:"upstreamRepo,omitempty"`
	UpstreamURL     string `json:"upstreamUrl,omitempty"`
	UpstreamCommit  string `json:"upstreamCommit,omitempty"`
	UpstreamLicense string `json:"upstreamLicense,omitempty"`
}

func (m Manifest) upstreamMetadata() PluginUpstream {
	if m.Upstream == nil {
		return PluginUpstream{}
	}
	return PluginUpstream{
		UpstreamRepo: m.Upstream.Repo, UpstreamURL: normalizeUpstreamURL(m.Upstream.Repo),
		UpstreamCommit: m.Upstream.Commit, UpstreamLicense: m.Upstream.License,
	}
}

func normalizeUpstreamURL(repo string) string {
	if upstreamRepoPattern.MatchString(repo) {
		owner, name, _ := strings.Cut(repo, "/")
		if owner != "." && owner != ".." && name != "." && name != ".." {
			return "https://github.com/" + repo
		}
		return ""
	}
	u, err := url.Parse(repo)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Hostname() == "" {
		return ""
	}
	u.Host = strings.ToLower(u.Host)
	return u.String()
}

// Manifest is plugin.json. Unknown fields are refused by the parser: a typo in
// a capability name must fail loudly rather than silently drop a permission.
type Manifest struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Version      string    `json:"version"`
	APIVersion   int       `json:"apiVersion"`
	Description  string    `json:"description,omitempty"`
	Author       string    `json:"author,omitempty"`
	License      string    `json:"license,omitempty"`
	HostVersions []string  `json:"hostVersions"`
	Entry        Entry     `json:"entry"`
	Capabilities []string  `json:"capabilities"`
	Upstream     *Upstream `json:"upstream,omitempty"`
}

// ParseManifest decodes a manifest strictly. Trailing content after the object
// is refused, because a second document cannot be declared and would be read
// differently by another tool.
func ParseManifest(data []byte) (Manifest, error) {
	manifest := Manifest{}
	if len(data) == 0 {
		return manifest, errors.New("plugin.json 为空")
	}
	if len(data) > ManifestLimit {
		return manifest, fmt.Errorf("plugin.json 超过 %d 字节", ManifestLimit)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("plugin.json 无法解析：%w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("plugin.json 在对象后还有多余内容")
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Validate checks every field the host depends on. It never touches the
// filesystem; ValidateFiles does that.
func (m Manifest) Validate() error {
	if !idPattern.MatchString(m.ID) {
		return errors.New("id 必须是“发布者.名称”的小写形式，例如 kimicode-plus.monitor")
	}
	if strings.TrimSpace(m.Name) == "" || len(m.Name) > 80 {
		return errors.New("name 必须为 1–80 个字符")
	}
	if !versionPattern.MatchString(m.Version) {
		return errors.New("version 必须是 major.minor.patch")
	}
	if m.APIVersion != 1 && m.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion 必须为 1 或 %d，收到 %d", APIVersion, m.APIVersion)
	}
	if len(m.HostVersions) == 0 || len(m.HostVersions) > 32 {
		return errors.New("hostVersions 必须列出 1–32 个宿主版本")
	}
	for _, version := range m.HostVersions {
		if !hostPattern.MatchString(version) {
			return fmt.Errorf("hostVersions 含无效版本 %q", version)
		}
	}
	if err := validateEntry(m.Entry.Renderer, true); err != nil {
		return fmt.Errorf("entry.renderer：%w", err)
	}
	if m.Entry.Settings != "" {
		if err := validateEntry(m.Entry.Settings, false); err != nil {
			return fmt.Errorf("entry.settings：%w", err)
		}
	}
	if _, err := m.CapabilityGrants(); err != nil {
		return err
	}
	if len(m.Description) > 500 {
		return errors.New("description 不超过 500 个字符")
	}
	if m.Upstream != nil {
		if m.Upstream.Repo != "" && (strings.ContainsAny(m.Upstream.Repo, " \t\n") || len(m.Upstream.Repo) > 200) {
			return errors.New("upstream.repo 无效")
		}
		if m.Upstream.Commit != "" && len(m.Upstream.Commit) > 64 {
			return errors.New("upstream.commit 无效")
		}
	}
	return nil
}

func validateEntry(value string, required bool) error {
	if value == "" {
		if required {
			return errors.New("缺少入口文件")
		}
		return nil
	}
	if !entryPattern.MatchString(value) {
		return fmt.Errorf("路径 %q 含不允许的字符", value)
	}
	cleaned := path.Clean(value)
	if cleaned != value || strings.HasPrefix(cleaned, "..") || path.IsAbs(cleaned) {
		return fmt.Errorf("路径 %q 必须位于插件目录内", value)
	}
	if !strings.HasSuffix(strings.ToLower(cleaned), ".js") {
		return errors.New("入口必须是 ES module（.js）")
	}
	return nil
}

// ValidateFiles checks the files the manifest promises actually exist inside
// dir, and that the required LICENSE is present.
func (m Manifest) ValidateFiles(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, "LICENSE")); err != nil {
		return errors.New("缺少 LICENSE 文件")
	}
	for _, name := range []string{m.Entry.Renderer, m.Entry.Settings} {
		if name == "" {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			return fmt.Errorf("缺少入口文件 %s", name)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("入口 %s 不是普通文件", name)
		}
		if info.Size() > 4<<20 {
			return fmt.Errorf("入口 %s 超过 4MB", name)
		}
	}
	return nil
}

// HostVersionSupported reports whether a concrete host version satisfies any
// hostVersions entry. A trailing ".*" is a dot-boundary prefix, so "1.*" covers
// every 1.x.y and "1.3.*" covers 1.3.y but not 1.4.0.
func (m Manifest) HostVersionSupported(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	for _, allowed := range m.HostVersions {
		allowed = strings.TrimSpace(allowed)
		if allowed == host {
			return true
		}
		if !strings.HasSuffix(allowed, ".*") {
			continue
		}
		prefix := strings.TrimSuffix(allowed, ".*")
		if strings.HasPrefix(host, prefix+".") {
			return true
		}
	}
	return false
}

// CapabilityGrants parses the declared capabilities and enforces the API version
// split: a v2 capability in a v1 manifest is refused, because declaring it there
// would ask for a privilege the v1 contract never had. The reverse is allowed: a
// v2 manifest may still declare every v1 capability.
func (m Manifest) CapabilityGrants() (Grants, error) {
	grants, err := ParseCapabilities(m.Capabilities)
	if err != nil {
		return grants, err
	}
	if m.APIVersion < 2 {
		for _, name := range m.Capabilities {
			if RequiresPrivilege(strings.TrimSpace(name)) {
				return grants, fmt.Errorf("capability %q 需要 apiVersion 2，当前清单是 %d", strings.TrimSpace(name), m.APIVersion)
			}
		}
	}
	return grants, nil
}

// CapabilityNames returns the manifest capabilities in a stable order so the
// consent prompt and the stored grant do not churn between reads.
func (m Manifest) CapabilityNames() []string {
	grants, _ := m.CapabilityGrants()
	return grants.Names()
}

// DataDirName is the on-disk directory name under <dataDir>/plugins. The id is
// already restricted to [a-z0-9.-], so it cannot escape the parent.
func (m Manifest) DataDirName() string { return m.ID }
