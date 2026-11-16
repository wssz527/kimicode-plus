package plugins

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"enhancedkimi/internal/credentials"
	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/redact"
)

const APIPrefix = "/enhance/v1"

const (
	StateDisabled  = "DISABLED"
	StateEnabled   = "ENABLED"
	StateFaulted   = "FAULTED"
	StateBlocked   = "BLOCKED"
	StateIncompat  = "INCOMPATIBLE"
	StateCorrupt   = "CORRUPT"
	StateUninstall = "UNINSTALLED"
)

const (
	SourceBundled = "BUNDLED"
	SourceLocal   = "LOCAL"
)

// rendererTokenLimit bounds the in-memory identity tokens. One token exists per
// enabled plugin per running core; a leak here would be an unbounded growth bug
// rather than a security event.
const rendererTokenLimit = 256

// Options wires the manager to the App it belongs to. Everything that a plugin
// can reach is held on the instance: there is no package-level vault, core or
// data directory, so two Apps in one process cannot see each other's state.
type Options struct {
	DataDir     string
	HostVersion string
	Core        Core
	Vault       *credentials.Vault
	Now         func() time.Time
	// Bundled supplies the embedded plugin set for restore and release. The core
	// injects it so this package never imports the plugin bundle.
	Bundled func() (map[string]map[string][]byte, error)
	// Resolver overrides DNS for outbound plugin requests. Nil means the system
	// resolver; a test supplies one to exercise rebinding deterministically.
	Resolver *net.Resolver
}

// Option mutates Options during construction.
type Option func(*Options)

func WithDataDir(dir string) Option         { return func(o *Options) { o.DataDir = dir } }
func WithHostVersion(v string) Option       { return func(o *Options) { o.HostVersion = v } }
func WithCore(core Core) Option             { return func(o *Options) { o.Core = core } }
func WithVault(v *credentials.Vault) Option { return func(o *Options) { o.Vault = v } }

func WithBundled(load func() (map[string]map[string][]byte, error)) Option {
	return func(o *Options) { o.Bundled = load }
}

// WithResolver replaces the resolver used for outbound plugin requests. Tests
// use it to prove what a name resolves to without touching the network.
func WithResolver(resolver *net.Resolver) Option {
	return func(o *Options) { o.Resolver = resolver }
}

// EnableGate lets another component refuse an enable before the plugin becomes
// live. The KCT migration uses it so the adapter cannot start while the
// standalone injector is still loaded. A gate answers a code the page can act
// on, or an empty code to allow the enable.
type EnableGate interface {
	CheckEnable(ctx context.Context, id string) (code, message string)
}

// Core is the narrow slice of the App the plugin host needs. nativeService
// implements it; declaring it here keeps the desktop package out of this
// package's imports and makes the manager testable with a fake.
type Core interface {
	Action(ctx context.Context, action string, payload json.RawMessage) (any, error)
	Events() EventSource
}

// EventSource is desktop.EventBus reduced to the one method the plugin host
// calls. The adapter in package server bridges the concrete event type.
type EventSource interface {
	Poll(ctx context.Context, after int64, scopes []string, wait time.Duration) (EventPage, error)
}

type Event struct {
	Sequence int64  `json:"sequence"`
	Scope    string `json:"scope"`
	Value    any    `json:"value"`
}

type EventPage struct {
	Events []Event `json:"events"`
	Next   int64   `json:"next"`
	Reset  bool    `json:"reset,omitempty"`
}

// Plugin is one row of the plugin page. It carries only what the UI renders;
// capabilities are reported as granted names so an ungranted permission is not
// displayed as if it were in force.
type Plugin struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Description  string   `json:"description,omitempty"`
	Author       string   `json:"author,omitempty"`
	License      string   `json:"license,omitempty"`
	Source       string   `json:"source"`
	State        string   `json:"state"`
	Enabled      bool     `json:"enabled"`
	Error        string   `json:"error,omitempty"`
	Capabilities []string `json:"capabilities"`
	Declared     []string `json:"declaredCapabilities"`
	Slots        []string `json:"slots"`
	HostVersions []string `json:"hostVersions"`
	Compatible   bool     `json:"compatible"`
	HasSettings  bool     `json:"hasSettings"`
	// Entry is the manifest entry, which the renderer needs to load the module.
	Entry       Entry  `json:"entry"`
	Upstream    string `json:"upstream,omitempty"`
	InstalledAt string `json:"installedAt,omitempty"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
	// Desired is the user's enabled intent, which survives a restart. Enabled is
	// true only while this process has granted the plugin a live token; a plugin
	// can be desired and not yet live before a renderer adopts it.
	Desired bool `json:"desired"`
	// Forced records that the user enabled a plugin whose host version was not
	// declared in hostVersions.
	Forced bool `json:"forced,omitempty"`
	// Restorable marks a bundled plugin the user uninstalled. It is not
	// installed, holds no files and is not running; it appears in the list only
	// so the page can offer to restore the embedded copy. Nothing is released
	// automatically.
	Restorable bool `json:"restorable,omitempty"`
	// Token is only filled in the enable response, for the renderer that is about
	// to load this plugin. The list never carries it.
	Token string `json:"token,omitempty"`
}

// entry is a live install: the parsed manifest, the granted capabilities and
// where its files are. The manifest is re-read from disk on every start, so an
// edited file is caught instead of being trusted from the database.
type entry struct {
	manifest  Manifest
	grants    Grants
	declared  []string
	source    string
	dir       string
	state     string
	errText   string
	enabled   bool
	forced    bool
	updated   time.Time
	installed time.Time
}

type Manager struct {
	db      *sql.DB
	safe    func() bool
	dataDir string
	host    string
	core    Core
	vault   *credentials.Vault
	bundled func() (map[string]map[string][]byte, error)
	now     func() time.Time
	gate    EnableGate

	mu      sync.Mutex
	entries map[string]*entry
	order   []string
	tokens  map[string]string
	lookup  *net.Resolver
	closed  bool
}

// New builds the plugin host. A missing data directory is not fatal: the manager
// then serves an empty list and refuses installs, which keeps a headless test or
// a degraded start usable instead of failing the whole core.
func New(db *sql.DB, safe func() bool, options ...Option) (*Manager, error) {
	if db == nil {
		return nil, errors.New("plugins requires a database")
	}
	if safe == nil {
		safe = func() bool { return false }
	}
	opts := Options{}
	for _, option := range options {
		option(&opts)
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if err := migrate(db); err != nil {
		return nil, err
	}
	manager := &Manager{
		db:      db,
		safe:    safe,
		dataDir: opts.DataDir,
		host:    opts.HostVersion,
		core:    opts.Core,
		vault:   opts.Vault,
		bundled: opts.Bundled,
		now:     opts.Now,
		entries: make(map[string]*entry),
		tokens:  make(map[string]string),
		lookup:  opts.Resolver,
	}
	if err := manager.load(); err != nil {
		return nil, err
	}
	return manager, nil
}

// UseCore and UseVault attach the App instances after construction, because the
// core builds plugins before the native service and the vault exist. Passing
// them here keeps a single instance per App instead of a package global.
func (m *Manager) UseCore(core Core) {
	m.mu.Lock()
	m.core = core
	m.mu.Unlock()
}

func (m *Manager) UseVault(vault *credentials.Vault) {
	m.mu.Lock()
	m.vault = vault
	m.mu.Unlock()
}

// UseEnableGate attaches the gate after construction for the same reason as
// UseCore: the migration service needs the data directory and the store, which
// the server knows only once the manager exists.
func (m *Manager) UseEnableGate(gate EnableGate) {
	m.mu.Lock()
	m.gate = gate
	m.mu.Unlock()
}

func (m *Manager) SetHostVersion(version string) {
	m.mu.Lock()
	m.host = version
	m.mu.Unlock()
}

// DataDir reports the plugin root this manager owns. The renderer needs it to
// build asset URLs, and two managers in one process must report different roots.
func (m *Manager) DataDir() string { return m.dataDir }

func (m *Manager) bundledFiles() (map[string]map[string][]byte, error) {
	m.mu.Lock()
	load := m.bundled
	m.mu.Unlock()
	if load == nil {
		return nil, nil
	}
	return load()
}

func (m *Manager) pluginsDir() string {
	if m.dataDir == "" {
		return ""
	}
	return filepath.Join(m.dataDir, "plugins")
}

func (m *Manager) load() error {
	ctx := context.Background()
	records, err := loadRecords(ctx, m.db)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, item := range records {
		if item.tombstone {
			continue
		}
		var manifest Manifest
		if err := json.Unmarshal(item.manifest, &manifest); err != nil {
			manifest = Manifest{ID: item.id, Name: item.name, Version: item.version}
		}
		grants, grantErr := ParseCapabilities(item.capabilities)
		if grantErr != nil {
			grants = Grants{fixed: make(map[string]bool)}
		}
		// The persisted record is the user's intent, not a claim that the plugin is
		// running. `desired` is that intent and survives shutdown, while `state`
		// records why the plugin is not running (blocked, incompatible, corrupt).
		// Restoring from `state` alone would lose the enable decision, because a
		// clean shutdown writes DISABLED for every plugin it stopped. Only the
		// running/stopped pair follows `desired`: a recorded reason to stay down
		// still wins over a desire to run.
		state := item.state
		if state == "" {
			state = StateDisabled
		}
		if state == StateEnabled || state == StateDisabled {
			if item.desired {
				state = StateEnabled
			} else {
				state = StateDisabled
			}
		}
		m.entries[item.id] = &entry{
			manifest: manifest, grants: grants, declared: manifest.CapabilityNames(),
			source: item.source, dir: filepath.Join(m.pluginsDir(), manifest.DataDirName()),
			state: state, errText: item.err, enabled: item.desired,
			// Force-enabling an incompatible plugin is not a separate column: it is
			// exactly a desired plugin the host version does not cover, so the flag
			// is re-derived instead of drifting from what was stored.
			forced:    item.desired && !m.compatible(manifest),
			installed: item.installedAt, updated: item.updatedAt,
		}
		m.order = append(m.order, item.id)
	}
	sort.Strings(m.order)
	return nil
}

func (m *Manager) snapshot() []Plugin {
	m.mu.Lock()
	out := make([]Plugin, 0, len(m.order))
	installed := make(map[string]bool, len(m.order))
	for _, id := range m.order {
		item := m.entries[id]
		if item == nil {
			continue
		}
		installed[id] = true
		out = append(out, m.presentLocked(item))
	}
	m.mu.Unlock()
	// Restorable entries need the embedded set and the tombstone rows, and both
	// involve work that takes the same lock (or does DB I/O). They are assembled
	// after the entries snapshot so a list call cannot deadlock on itself.
	out = append(out, m.restorable(installed)...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// restorable reports bundled plugins the user uninstalled. A tombstone is the
// only record left, so the entry carries no capabilities and no entry point:
// restoring re-releases the embedded copy and the consent step happens again.
func (m *Manager) restorable(installed map[string]bool) []Plugin {
	bundled, err := m.bundledFiles()
	if err != nil || len(bundled) == 0 {
		return nil
	}
	records, err := loadRecords(context.Background(), m.db)
	if err != nil {
		return nil
	}
	var out []Plugin
	for _, item := range records {
		if !item.tombstone || item.state != StateUninstall {
			continue
		}
		if _, ok := bundled[item.id]; !ok || installed[item.id] {
			continue
		}
		out = append(out, Plugin{
			ID:           item.id,
			Name:         item.name,
			Version:      item.version,
			Source:       SourceBundled,
			State:        StateUninstall,
			Enabled:      false,
			Capabilities: []string{},
			Declared:     []string{},
			Slots:        []string{},
			HostVersions: []string{},
			Restorable:   true,
			UpdatedAt:    m.now().UTC().Format(time.RFC3339),
		})
	}
	return out
}

// presentLocked builds the API projection of one entry. The caller must hold
// m.mu: every field it reads (state, enabled, forced, errText, updated) is
// written by Enable/Disable/Fail under that same lock, so building the projection
// after unlocking would race with a concurrent transition.
func (m *Manager) presentLocked(item *entry) Plugin {
	plugin := Plugin{
		ID:           item.manifest.ID,
		Name:         item.manifest.Name,
		Version:      item.manifest.Version,
		Description:  item.manifest.Description,
		Author:       item.manifest.Author,
		License:      item.manifest.License,
		Source:       item.source,
		State:        item.state,
		Enabled:      item.state == StateEnabled,
		Desired:      item.enabled,
		Forced:       item.forced,
		Error:        item.errText,
		Capabilities: item.grants.Names(),
		Declared:     append([]string(nil), item.declared...),
		Slots:        grantedSlots(item.grants),
		HostVersions: append([]string(nil), item.manifest.HostVersions...),
		Compatible:   m.compatible(item.manifest),
		HasSettings:  item.manifest.Entry.Settings != "",
		Entry:        item.manifest.Entry,
		Upstream:     upstreamLabel(item.manifest),
		UpdatedAt:    item.updated.String(),
	}
	if !item.installed.IsZero() {
		plugin.InstalledAt = item.installed.String()
	}
	return plugin
}

func upstreamLabel(manifest Manifest) string {
	if manifest.Upstream == nil {
		return ""
	}
	parts := []string{}
	if manifest.Upstream.Repo != "" {
		parts = append(parts, manifest.Upstream.Repo)
	}
	if manifest.Upstream.Commit != "" {
		parts = append(parts, manifest.Upstream.Commit)
	}
	if manifest.Upstream.License != "" {
		parts = append(parts, manifest.Upstream.License)
	}
	return strings.Join(parts, " · ")
}

func grantedSlots(grants Grants) []string {
	slots := []string{}
	for _, slot := range []string{"sidebar", "settings", "thinking", "message"} {
		if grants.AllowsSlot(slot) {
			slots = append(slots, slot)
		}
	}
	return slots
}

// compatible reports the version gate. An unknown host version (a build without
// one, or a test) does not block a plugin: the gate protects users from
// unverified hosts, and refusing everything would make the feature unusable in a
// developer build. A known version that is not declared is a real mismatch.
func (m *Manager) compatible(manifest Manifest) bool {
	host := strings.TrimSpace(m.host)
	if host == "" {
		return true
	}
	return manifest.HostVersionSupported(host)
}

// List returns every install in a stable order.
func (m *Manager) List() []Plugin { return m.snapshot() }

// Get returns one install.
func (m *Manager) Get(id string) (Plugin, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.entries[id]
	if !ok {
		return Plugin{}, false
	}
	return m.presentLocked(item), true
}

// GrantsFor returns the granted capabilities of an enabled plugin. The zero
// Grants denies everything, so a missing or disabled plugin cannot act.
func (m *Manager) GrantsFor(id string) (Grants, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.entries[id]
	if !ok || item.state != StateEnabled {
		return Grants{}, false
	}
	return item.grants, true
}

// TokenOwner resolves the plugin a token belongs to. The dedicated action route
// uses it so a plugin cannot even name another plugin: its identity comes from
// the token, not from the URL.
func (m *Manager) TokenOwner(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, current := range m.tokens {
		if subtleEqual(current, token) {
			return id, true
		}
	}
	return "", false
}

// Token issues the renderer identity token for an enabled plugin. The token is
// generated per enable, kept in memory only, and never written to the database or
// an activity row; it dies with the manager.
func (m *Manager) Token(id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.entries[id]
	if !ok {
		return "", apiError(http.StatusNotFound, "PLUGIN_NOT_FOUND", "插件未安装")
	}
	if item.state != StateEnabled {
		return "", apiError(http.StatusConflict, "PLUGIN_DISABLED", "插件未启用")
	}
	if token, ok := m.tokens[id]; ok {
		return token, nil
	}
	if len(m.tokens) >= rendererTokenLimit {
		return "", apiError(http.StatusTooManyRequests, "PLUGIN_TOKEN_LIMIT", "同时启用的插件过多")
	}
	token, err := newToken()
	if err != nil {
		return "", apiError(http.StatusInternalServerError, "PLUGIN_TOKEN", "无法生成插件身份令牌")
	}
	m.tokens[id] = token
	return token, nil
}

// Verify reports whether a renderer-supplied token belongs to the plugin it
// claims. A token from another plugin, another App or an earlier enable fails,
// which is what makes the identity per-instance rather than global.
func (m *Manager) Verify(id, token string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.tokens[id]
	if !ok || token == "" || current == "" {
		return false
	}
	return subtleEqual(current, token)
}

func subtleEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func newToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func (m *Manager) enabled(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.entries[id]
	return ok && item.state == StateEnabled
}

// Enable turns a plugin on after re-checking the version gate, the safe mode and
// the files on disk. Every refusal leaves a reason the page can show.
// Enable turns a plugin on. A host version outside hostVersions is refused unless
// force is set, which the page only sends after the user confirms the warning.
func (m *Manager) Enable(ctx context.Context, id string) (Plugin, error) {
	return m.enable(ctx, id, false)
}

// ForceEnable is the explicit override for a plugin the current host version did
// not declare. It is deliberately a separate call so the default path can never
// be reached by accident.
func (m *Manager) ForceEnable(ctx context.Context, id string) (Plugin, error) {
	return m.enable(ctx, id, true)
}

func (m *Manager) enable(ctx context.Context, id string, force bool) (Plugin, error) {
	m.mu.Lock()
	item, ok := m.entries[id]
	if !ok {
		m.mu.Unlock()
		return Plugin{}, apiError(http.StatusNotFound, "PLUGIN_NOT_FOUND", "插件未安装")
	}
	if m.closed {
		m.mu.Unlock()
		return Plugin{}, apiError(http.StatusConflict, "MANAGER_CLOSED", "插件管理器已关闭")
	}
	if item.state == StateEnabled {
		// Build the projection while the lock is still held: presentLocked reads
		// fields that a concurrent Disable writes under the same lock.
		plugin := m.presentLocked(item)
		m.mu.Unlock()
		return plugin, nil
	}
	safe := m.safe()
	manifest := item.manifest
	dir := item.dir
	m.mu.Unlock()

	now := m.now()
	if safe {
		m.fail(ctx, id, StateBlocked, "安全模式已阻止插件启动。", ActivitySafeMode, now)
		return m.Get2(id), apiError(http.StatusConflict, "SAFE_MODE", "安全模式已阻止插件启动")
	}
	if !m.compatible(manifest) && !force {
		message := fmt.Sprintf("未在此宿主版本（%s）验证，已拒绝启用。确认风险后可强制启用。", m.host)
		m.fail(ctx, id, StateIncompat, message, ActivityVersion, now)
		return m.Get2(id), apiError(http.StatusConflict, "PLUGIN_INCOMPATIBLE", message)
	}
	if err := manifest.ValidateFiles(dir); err != nil {
		message := redact.Text(err.Error())
		m.fail(ctx, id, StateCorrupt, message, ActivityCorrupt, now)
		return m.Get2(id), apiError(http.StatusConflict, "PLUGIN_FILES_INVALID", message)
	}

	m.mu.Lock()
	gate := m.gate
	m.mu.Unlock()
	if gate != nil {
		if code, message := gate.CheckEnable(ctx, id); code != "" {
			m.fail(ctx, id, StateBlocked, message, ActivitySafeMode, now)
			return m.Get2(id), apiError(http.StatusConflict, code, message)
		}
	}

	m.mu.Lock()
	item = m.entries[id]
	if item == nil || m.closed {
		m.mu.Unlock()
		return Plugin{}, apiError(http.StatusConflict, "MANAGER_CLOSED", "插件管理器已关闭")
	}
	item.state = StateEnabled
	item.errText = ""
	item.enabled = true
	item.forced = force && !m.compatible(manifest)
	item.updated = now
	token, tokenErr := m.issueTokenLocked(id)
	plugin := m.presentLocked(item)
	m.mu.Unlock()

	if tokenErr != nil {
		m.fail(ctx, id, StateFaulted, "无法生成插件身份令牌。", ActivityError, now)
		return m.Get2(id), apiError(http.StatusInternalServerError, "PLUGIN_TOKEN", "无法生成插件身份令牌")
	}
	if err := setState(ctx, m.db, id, StateEnabled, true, "", now); err != nil {
		m.mu.Lock()
		item.state = StateDisabled
		item.enabled = false
		delete(m.tokens, id)
		m.mu.Unlock()
		return m.Get2(id), apiError(http.StatusInternalServerError, "PLUGIN_STATE_UNAVAILABLE", "无法保存插件状态")
	}
	_ = recordActivity(ctx, m.db, id, ActivityEnable, "插件已启用", now)
	plugin.Token = token
	return plugin, nil
}

func (m *Manager) issueTokenLocked(id string) (string, error) {
	if token, ok := m.tokens[id]; ok {
		return token, nil
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	m.tokens[id] = token
	return token, nil
}

// Get2 is Get without the comma-ok, for the paths that just refused an action
// and want to report the resulting state.
func (m *Manager) Get2(id string) Plugin {
	plugin, _ := m.Get(id)
	return plugin
}

func (m *Manager) fail(ctx context.Context, id, state, message, kind string, now time.Time) {
	m.mu.Lock()
	item := m.entries[id]
	if item != nil {
		item.state = state
		item.enabled = false
		item.errText = message
		item.updated = now
	}
	delete(m.tokens, id)
	m.mu.Unlock()
	_ = setState(ctx, m.db, id, state, false, message, now)
	_ = recordActivity(ctx, m.db, id, kind, message, now)
}

// Disable stops a plugin and revokes its renderer token in the same step, so a
// renderer that keeps polling cannot act after the user turned it off.
func (m *Manager) Disable(ctx context.Context, id string) error {
	m.mu.Lock()
	item, ok := m.entries[id]
	if !ok {
		m.mu.Unlock()
		// Reaching here while the page is open means the plugin was uninstalled
		// between the click and this call. The plain not-found code would tell the
		// user they never had it, so the message names the real situation while
		// keeping the status a client already handles.
		return apiError(http.StatusNotFound, "PLUGIN_NOT_FOUND", "插件已不存在，可能已被卸载")
	}
	item.state = StateDisabled
	item.enabled = false
	item.forced = false
	item.errText = ""
	item.updated = m.now()
	delete(m.tokens, id)
	m.mu.Unlock()
	if err := setState(ctx, m.db, id, StateDisabled, false, "", m.now()); err != nil {
		return apiError(http.StatusInternalServerError, "PLUGIN_STATE_UNAVAILABLE", "无法保存插件状态")
	}
	_ = recordActivity(ctx, m.db, id, ActivityDisable, "插件已停用", m.now())
	return nil
}

// Uninstall removes the files, the private storage and the token, and leaves a
// tombstone so the bundled copy is not released again. The tombstone is what
// makes "uninstall" a decision rather than a temporary deletion.
func (m *Manager) Uninstall(ctx context.Context, id string) error {
	m.mu.Lock()
	item, ok := m.entries[id]
	if !ok {
		m.mu.Unlock()
		return apiError(http.StatusNotFound, "PLUGIN_NOT_FOUND", "插件未安装")
	}
	dir := item.dir
	now := m.now()
	name, version, source := item.manifest.Name, item.manifest.Version, item.source
	// Stop first, remove second. A file or row failure below leaves the plugin
	// installed but stopped, never running with a token that outlives the
	// uninstall decision the user just made.
	item.state = StateDisabled
	item.enabled = false
	item.forced = false
	item.errText = ""
	item.updated = now
	delete(m.tokens, id)
	m.mu.Unlock()
	if err := setState(ctx, m.db, id, StateDisabled, false, "", now); err != nil {
		return apiError(http.StatusInternalServerError, "PLUGIN_STATE_UNAVAILABLE", "无法保存插件状态，插件仍保持停用")
	}
	_ = recordActivity(ctx, m.db, id, ActivityDisable, "卸载前已停用插件并撤销令牌", now)

	if err := os.RemoveAll(dir); err != nil {
		return apiError(http.StatusInternalServerError, "PLUGIN_UNINSTALL_FAILED", "无法删除插件文件，插件仍保持停用")
	}
	_ = storageClear(ctx, m.db, id)
	if err := deleteRecord(ctx, m.db, id); err != nil {
		return apiError(http.StatusInternalServerError, "PLUGIN_UNINSTALL_FAILED", "插件文件已删除，但记录未能清理")
	}
	_ = saveRecord(ctx, m.db, record{
		id: id, name: name, version: version,
		manifest: mustJSON(map[string]any{"id": id, "name": name, "version": version}), capabilities: []string{}, source: source,
		state: StateUninstall, tombstone: true, installedAt: now, updatedAt: now,
	})
	m.mu.Lock()
	delete(m.entries, id)
	delete(m.tokens, id)
	m.order = removeString(m.order, id)
	m.mu.Unlock()
	_ = recordActivity(ctx, m.db, id, ActivityUninstall, "插件已卸载；卸载后不会自动恢复预装副本", now)
	return nil
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		return []byte("{}")
	}
	return data
}

func removeString(list []string, value string) []string {
	out := make([]string, 0, len(list))
	for _, item := range list {
		if item != value {
			out = append(out, item)
		}
	}
	return out
}

// EnforceSafeMode stops every plugin when safe mode is on. It is reversible: the
// installs stay, only the running state and tokens are dropped.
func (m *Manager) EnforceSafeMode() {
	if !m.safe() {
		return
	}
	ctx := context.Background()
	m.mu.Lock()
	changed := []string{}
	for id, item := range m.entries {
		if item.state == StateEnabled {
			item.state = StateBlocked
			item.errText = "安全模式已阻止插件运行。"
			item.enabled = false
			item.updated = m.now()
			changed = append(changed, id)
		}
		delete(m.tokens, id)
	}
	m.mu.Unlock()
	for _, id := range changed {
		_ = setState(ctx, m.db, id, StateBlocked, false, "安全模式已阻止插件运行。", m.now())
		_ = recordActivity(ctx, m.db, id, ActivitySafeMode, "安全模式已阻止插件运行。", m.now())
	}
}

// Close drops every token and stops the host. A call after Close is refused so a
// shut-down App cannot be driven through a stale reference.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	// Shutdown releases live resources but preserves the user's enable intent, so
	// the next renderer can adopt the plugin without the user re-enabling it.
	//
	// Everything the write-back needs is copied here: the entries map can lose
	// keys to a concurrent Uninstall and every entry field is mutable under this
	// lock, so iterating m.entries after the unlock reads memory another
	// goroutine may be rewriting or deleting.
	stopped := make([]record, 0, len(m.entries))
	for id, item := range m.entries {
		if item.state == StateEnabled {
			item.state = StateDisabled
		}
		stopped = append(stopped, record{
			id:      id,
			state:   item.state,
			desired: item.enabled,
			err:     item.errText,
		})
	}
	m.tokens = make(map[string]string)
	m.mu.Unlock()
	ctx := context.Background()
	var failures []string
	for _, item := range stopped {
		// desired stays as recorded: it is what the user asked for, not what this
		// process managed to keep running.
		if err := setState(ctx, m.db, item.id, item.state, item.desired, item.err, m.now()); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return errors.New("plugin shutdown incomplete: " + strings.Join(failures, "; "))
	}
	return nil
}

func (m *Manager) closedError() error {
	return apiError(http.StatusConflict, "MANAGER_CLOSED", "插件管理器已关闭")
}

// Health is the summary the core health endpoint shows.
func (m *Manager) Health(ctx context.Context) map[string]any {
	plugins := m.snapshot()
	enabled, faulted := 0, 0
	errs := []string{}
	for _, plugin := range plugins {
		if plugin.Enabled {
			enabled++
		}
		switch plugin.State {
		case StateFaulted, StateCorrupt, StateIncompat, StateBlocked:
			faulted++
			if plugin.Error != "" {
				errs = append(errs, plugin.ID+": "+plugin.Error)
			}
		}
	}
	return map[string]any{
		"status":      "OK",
		"safeMode":    m.safe(),
		"total":       len(plugins),
		"enabled":     enabled,
		"faulted":     faulted,
		"hostVersion": m.host,
		"mode":        "LOCAL_TRUSTED_RENDERER",
		"errors":      errs,
	}
}

func (m *Manager) Register(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("GET "+APIPrefix+"/plugins", m.handleList)
	mux.HandleFunc("GET "+APIPrefix+"/plugins/health", m.handleHealth)
	mux.HandleFunc("POST "+APIPrefix+"/plugins/preview", m.handlePreview)
	mux.HandleFunc("POST "+APIPrefix+"/plugins/install", m.handleInstall)
	// Bundled plugins can be restored after the user uninstalled them, and an
	// unverified host version can be overridden with an explicit confirmation.
	mux.HandleFunc("POST "+APIPrefix+"/plugins/{id}/restore", m.handleRestore)
	mux.HandleFunc("POST "+APIPrefix+"/plugins/{id}/force-enable", m.handleForceEnable)
	// The plugin runtime calls this route with its token in the body. The host
	// bridge only forwards a fixed header allowlist, so a token header would not
	// reach the core; identifying the plugin by its token is also the stronger
	// design, because the URL cannot name a plugin at all.
	mux.HandleFunc("POST "+APIPrefix+"/plugin-runtime/{action}", m.handleRuntime)
	mux.HandleFunc("GET "+APIPrefix+"/plugins/{id}", m.handleGet)
	mux.HandleFunc("POST "+APIPrefix+"/plugins/{id}/enable", m.handleEnable)
	mux.HandleFunc("POST "+APIPrefix+"/plugins/{id}/disable", m.handleDisable)
	mux.HandleFunc("POST "+APIPrefix+"/plugins/{id}/uninstall", m.handleUninstall)
	mux.HandleFunc("POST "+APIPrefix+"/plugins/{id}/action/{action}", m.handleAction)
	mux.HandleFunc("GET "+APIPrefix+"/plugins/{id}/activity", m.handleActivity)
	// The runtime asks for its token when it adopts a plugin enabled before this
	// renderer existed, and loads the module from the plugin's own directory.
	mux.HandleFunc("POST "+APIPrefix+"/plugins/{id}/token", m.handleToken)
	mux.HandleFunc("GET "+APIPrefix+"/plugins/{id}/assets/{file...}", m.handleAssets)
}

func (m *Manager) handleList(w http.ResponseWriter, _ *http.Request) {
	plugins := m.snapshot()
	httpx.JSON(w, http.StatusOK, map[string]any{
		"items":       plugins,
		"total":       len(plugins),
		"safeMode":    m.safe(),
		"hostVersion": m.host,
		"apiVersion":  APIVersion,
	})
}

func (m *Manager) handleHealth(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, m.Health(r.Context()))
}

// handleRuntime is the endpoint the renderer plugin runtime uses. The body
// carries the identity token and the params; the token decides which plugin is
// acting, so neither the URL nor the params can impersonate another plugin.
func (m *Manager) handleRuntime(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimSpace(r.PathValue("action"))
	if action == "" {
		httpx.Fail(w, http.StatusBadRequest, "PLUGIN_ACTION_REQUIRED", "缺少插件动作名")
		return
	}
	var body struct {
		Token  string          `json:"token"`
		Params json.RawMessage `json:"params,omitempty"`
	}
	if !httpx.Decode(w, r, &body) {
		return
	}
	id, ok := m.TokenOwner(body.Token)
	if !ok {
		httpx.Fail(w, http.StatusUnauthorized, "PLUGIN_IDENTITY_REJECTED", "插件身份令牌无效")
		return
	}
	params := body.Params
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	value, err := m.runAction(r.Context(), id, action, params, nil)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"value": value})
}

func (m *Manager) handlePreview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Root string `json:"root"`
	}
	if !httpx.Decode(w, r, &body) {
		return
	}
	preview, err := m.Preview(r.Context(), body.Root)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, preview)
}

func (m *Manager) handleRestore(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	bundled, err := m.bundledFiles()
	if err != nil {
		httpx.Fail(w, http.StatusInternalServerError, "PLUGIN_RESTORE_FAILED", "无法读取预装插件")
		return
	}
	files, ok := bundled[id]
	if !ok {
		httpx.Fail(w, http.StatusNotFound, "PLUGIN_NOT_BUNDLED", "没有这个预装插件")
		return
	}
	plugin, err := m.RestoreBundled(r.Context(), id, files)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, plugin)
}

func (m *Manager) handleForceEnable(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var body struct {
		Confirm bool `json:"confirm"`
	}
	if !httpx.Decode(w, r, &body) {
		return
	}
	if !body.Confirm {
		httpx.Fail(w, http.StatusBadRequest, "PLUGIN_FORCE_CONFIRM_REQUIRED", "强制启用前需要明确确认风险")
		return
	}
	plugin, err := m.ForceEnable(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, plugin)
}

func (m *Manager) handleInstall(w http.ResponseWriter, r *http.Request) {
	var request InstallRequest
	if !httpx.Decode(w, r, &request) {
		return
	}
	plugin, err := m.Install(r.Context(), request)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, plugin)
}

func (m *Manager) handleGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	plugin, ok := m.Get(id)
	if !ok {
		httpx.Fail(w, http.StatusNotFound, "PLUGIN_NOT_FOUND", "插件未安装")
		return
	}
	httpx.JSON(w, http.StatusOK, plugin)
}

func (m *Manager) handleEnable(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	plugin, err := m.Enable(r.Context(), id)
	if err != nil {
		plugin = m.Get2(id)
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, plugin)
}

func (m *Manager) handleDisable(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := m.Disable(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, m.Get2(id))
}

func (m *Manager) handleUninstall(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := m.Uninstall(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"id": id, "uninstalled": true})
}

func (m *Manager) handleActivity(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if _, ok := m.Get(id); !ok {
		httpx.Fail(w, http.StatusNotFound, "PLUGIN_NOT_FOUND", "插件未安装")
		return
	}
	offset, limit := httpx.Page(r)
	if limit > 200 {
		limit = 200
	}
	before := int64(0)
	if _, err := fmt.Sscanf(r.URL.Query().Get("before"), "%d", &before); err != nil {
		before = 0
	}
	items, err := loadActivity(r.Context(), m.db, id, before, offset+limit)
	if err != nil {
		httpx.Fail(w, http.StatusInternalServerError, "PLUGIN_ACTIVITY_UNAVAILABLE", "无法读取插件活动记录")
		return
	}
	if offset < len(items) {
		items = items[offset:]
	} else {
		items = []Activity{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items), "before": before})
}

// handleToken re-issues the identity token to this renderer. It is the same token
// the enable response returned, so a plugin that was already enabled when the
// window opened can still adopt it. Safe mode and a disabled plugin refuse.
func (m *Manager) handleToken(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	token, err := m.Token(id)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"id": id, "token": token})
}

// handleAssets returns a plugin's own module source as JSON. The renderer runs
// under the official `app://renderer` origin, so it cannot fetch a plugin file by
// URL: the source travels through the same authenticated core channel as every
// other plugin call and is imported from a blob URL there.
func (m *Manager) handleAssets(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	file := strings.TrimSpace(r.PathValue("file"))
	m.mu.Lock()
	item, ok := m.entries[id]
	enabled := ok && item.state == StateEnabled
	var dir string
	var manifest Manifest
	if ok {
		dir = item.dir
		manifest = item.manifest
	}
	m.mu.Unlock()
	if !ok {
		httpx.Fail(w, http.StatusNotFound, "PLUGIN_NOT_FOUND", "插件未安装")
		return
	}
	if !enabled {
		httpx.Fail(w, http.StatusConflict, "PLUGIN_DISABLED", "插件未启用")
		return
	}
	if file == "" {
		httpx.Fail(w, http.StatusNotFound, "PLUGIN_ASSET_MISSING", "没有指定文件")
		return
	}
	cleaned := path.Clean(file)
	// Only the files the manifest names can be served. An enabled plugin must not
	// be able to read arbitrary files it happens to ship next to its entry point,
	// and nothing outside the plugin directory is reachable at all.
	allowed := manifest.Entry.Renderer == cleaned || (manifest.Entry.Settings != "" && manifest.Entry.Settings == cleaned)
	if !allowed || strings.HasPrefix(cleaned, "..") || path.IsAbs(cleaned) {
		httpx.Fail(w, http.StatusNotFound, "PLUGIN_ASSET_MISSING", "文件不存在")
		return
	}
	target := filepath.Join(dir, filepath.FromSlash(cleaned))
	if !strings.HasPrefix(target, dir+string(os.PathSeparator)) {
		httpx.Fail(w, http.StatusNotFound, "PLUGIN_ASSET_MISSING", "文件不存在")
		return
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() {
		httpx.Fail(w, http.StatusNotFound, "PLUGIN_ASSET_MISSING", "文件不存在")
		return
	}
	if info.Size() > assetLimit {
		httpx.Fail(w, http.StatusRequestEntityTooLarge, "PLUGIN_ASSET_LIMIT", "插件文件过大")
		return
	}
	data, err := os.ReadFile(target)
	if err != nil {
		httpx.Fail(w, http.StatusInternalServerError, "PLUGIN_ASSET_UNAVAILABLE", "无法读取插件文件")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"path": cleaned, "source": string(data)})
}

const assetLimit = 8 << 20
