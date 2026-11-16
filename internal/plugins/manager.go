package plugins

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/storage"
)

const APIPrefix = "/enhance/v1"

// activationWaitLimit bounds how long a duplicate enable waits for the running
// activation, so a stuck simulated plugin cannot pin an HTTP handler forever.
const activationWaitLimit = 30 * time.Second

const (
	StateDisabled = "DISABLED"
	StateEnabling = "ENABLING"
	StateEnabled  = "ENABLED"
	StateFaulted  = "FAULTED"
	StateBlocked  = "BLOCKED"
)

const (
	ActionEnable  = "ENABLE"
	ActionDisable = "DISABLE"
	ActionFault   = "FAULT"
)

var migrations = []string{
	`CREATE TABLE plugin_states (
		id TEXT PRIMARY KEY,
		enabled INTEGER NOT NULL DEFAULT 0,
		state TEXT NOT NULL DEFAULT 'DISABLED',
		faults INTEGER NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT '',
		resources INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL
	)`,
}

type Descriptor struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Kind         string   `json:"kind"`
	Description  string   `json:"description"`
	HostRange    string   `json:"hostRange"`
	Capabilities []string `json:"capabilities"`
	Builtin      bool     `json:"builtin"`
	Sandbox      string   `json:"sandbox"`
}

type Plugin struct {
	Descriptor
	State      string `json:"state"`
	Enabled    bool   `json:"enabled"`
	Faults     int    `json:"faults"`
	Error      string `json:"error,omitempty"`
	Resources  int    `json:"resources"`
	UpdatedAt  string `json:"updatedAt"`
	SafeMode   bool   `json:"safeMode"`
	Controlled string `json:"controlled"`
}

type Scope struct {
	mu     sync.Mutex
	own    []resource
	closed bool
}

type resource struct {
	name    string
	cleanup func() error
}

func (s *Scope) Own(name string, cleanup func() error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		runCleanup(name, cleanup)
		return
	}
	s.own = append(s.own, resource{name: name, cleanup: cleanup})
	s.mu.Unlock()
}

// Dispose releases every registered resource in reverse order. Each disposer is
// isolated so one panic or error can never skip the remaining cleanup.
func (s *Scope) Dispose() []string {
	s.mu.Lock()
	resources := s.own
	s.own = nil
	s.closed = true
	s.mu.Unlock()
	var failures []string
	for i := len(resources) - 1; i >= 0; i-- {
		if message, ok := runCleanup(resources[i].name, resources[i].cleanup); !ok {
			failures = append(failures, message)
		}
	}
	return failures
}

func runCleanup(name string, cleanup func() error) (message string, ok bool) {
	if cleanup == nil {
		return "", true
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			message = fmt.Sprintf("%s: disposer panic isolated: %v", name, recovered)
			ok = false
		}
	}()
	if err := cleanup(); err != nil {
		return name + ": " + err.Error(), false
	}
	return "", true
}

func (s *Scope) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.own)
}

type Activation func(scope *Scope) error

type definition struct {
	descriptor Descriptor
	activate   Activation
	selfCheck  func() error
}

// activation links one running attempt to the scope it created. A stale
// activation must never publish state for a newer attempt.
type activation struct {
	scope *Scope
	token uint64
	done  chan struct{}
}

type runtimeState struct {
	state     string
	active    *activation
	faults    int
	err       string
	updatedAt time.Time
}

type Manager struct {
	db      *sql.DB
	safe    func() bool
	mu      sync.Mutex
	defs    map[string]*definition
	order   []string
	runtime map[string]*runtimeState
	closed  bool
	token   uint64
}

func New(db *sql.DB, safe func() bool) (*Manager, error) {
	if db == nil {
		return nil, errors.New("plugins requires a database")
	}
	if safe == nil {
		safe = func() bool { return false }
	}
	if err := storage.Migrate(db, "plugins", migrations); err != nil {
		return nil, fmt.Errorf("plugins migrations: %w", err)
	}
	manager := &Manager{
		db:      db,
		safe:    safe,
		defs:    make(map[string]*definition),
		runtime: make(map[string]*runtimeState),
	}
	manager.registerBuiltins()
	if err := manager.load(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) registerBuiltins() {
	m.add(definition{
		descriptor: Descriptor{
			ID: "kct", Name: "KCT 指令转接（模拟）", Version: "1.0.0", Kind: "BRIDGE_HELPER",
			Description: "内置模拟插件：在没有真实通道时展示指令转接的生命周期与资源记账。",
			HostRange:   "1.x", Capabilities: []string{"ROUTE_INSTRUCTIONS"}, Builtin: true,
			Sandbox: "IN_PROCESS_SIMULATION",
		},
		activate: func(scope *Scope) error {
			return simulateActivation(scope)
		},
	})
	m.add(definition{
		descriptor: Descriptor{
			ID: "monitor", Name: "运行监视（模拟）", Version: "1.0.0", Kind: "OBSERVER",
			Description: "内置模拟插件：只记录内存中的模拟计数，不读取官方进程、配置或会话。",
			HostRange:   "1.x", Capabilities: []string{"HEALTH_SUMMARY"}, Builtin: true,
			Sandbox: "IN_PROCESS_SIMULATION",
		},
		activate: func(scope *Scope) error {
			return simulateActivation(scope)
		},
	})
	m.add(definition{
		descriptor: Descriptor{
			ID: "fault-demo", Name: "故障示例（测试）", Version: "1.0.0", Kind: "FAULT_INJECTOR",
			Description: "唯一会抛出错误的测试插件；用于验证禁用隔离与状态持久化，不影响其他插件。",
			HostRange:   "1.x", Capabilities: []string{"INJECT_FAULT"}, Builtin: true,
			Sandbox: "IN_PROCESS_SIMULATION",
		},
		activate: func(scope *Scope) error {
			return simulateActivation(scope)
		},
		selfCheck: func() error {
			return errors.New("模拟故障：插件自检按设计失败")
		},
	})
}

func simulateActivation(scope *Scope) error {
	if scope == nil {
		return errors.New("missing activation scope")
	}
	scope.Own("connection", func() error { return nil })
	scope.Own("timer", func() error { return nil })
	scope.Own("listener", func() error { return nil })
	return nil
}

func (m *Manager) add(def definition) {
	if _, exists := m.defs[def.descriptor.ID]; exists {
		return
	}
	m.defs[def.descriptor.ID] = &def
	m.order = append(m.order, def.descriptor.ID)
}

func (m *Manager) load() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows, err := m.db.Query(`SELECT id,enabled,state,faults,error,updated_at FROM plugin_states`)
	if err != nil {
		return fmt.Errorf("load plugin states: %w", err)
	}
	defer rows.Close()
	type persisted struct {
		id      string
		enabled bool
		state   string
		faults  int
		errText string
	}
	var saved []persisted
	for rows.Next() {
		var row persisted
		var enabled int
		if err := rows.Scan(&row.id, &enabled, &row.state, &row.faults, &row.errText, new(string)); err != nil {
			return fmt.Errorf("scan plugin state: %w", err)
		}
		row.enabled = enabled == 1
		saved = append(saved, row)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate plugin states: %w", err)
	}
	for _, row := range saved {
		if _, ok := m.defs[row.id]; !ok {
			continue
		}
		state := &runtimeState{state: StateDisabled, faults: row.faults, err: row.errText, updatedAt: time.Now().UTC()}
		if row.enabled || row.state == StateEnabled || row.state == StateEnabling {
			state.state = StateBlocked
			state.err = "上次运行结束前插件仍处于启用状态；模拟插件不会在读取持久状态时自动启动，请显式启用。"
		} else if row.state == StateFaulted {
			state.state = StateFaulted
		}
		m.runtime[row.id] = state
	}
	return nil
}

func (m *Manager) persistLocked(id string, state *runtimeState) error {
	enabled := 0
	if state.state == StateEnabled {
		enabled = 1
	}
	resources := 0
	if state.active != nil && state.active.scope != nil {
		resources = state.active.scope.Count()
	}
	updated := state.updatedAt.UTC().Format(time.RFC3339Nano)
	_, err := m.db.Exec(`INSERT INTO plugin_states(id,enabled,state,faults,error,resources,updated_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET enabled=excluded.enabled,state=excluded.state,faults=excluded.faults,error=excluded.error,resources=excluded.resources,updated_at=excluded.updated_at`,
		id, enabled, state.state, state.faults, state.err, resources, updated)
	if err != nil {
		return fmt.Errorf("persist plugin state: %w", err)
	}
	return nil
}

func (m *Manager) state(id string) *runtimeState {
	state, ok := m.runtime[id]
	if !ok {
		state = &runtimeState{state: StateDisabled, updatedAt: time.Now().UTC()}
		m.runtime[id] = state
	}
	return state
}

// takeScopeLocked detaches the current activation and returns its scope. The
// caller disposes it outside the lock. Detaching alone invalidates the matching
// in-flight activation, because a stale attempt only publishes when its token is
// still the current one.
func (m *Manager) takeScopeLocked(state *runtimeState) *Scope {
	if state.active == nil {
		return nil
	}
	scope := state.active.scope
	state.active = nil
	return scope
}

func (m *Manager) List() []Plugin {
	m.EnforceSafeMode()
	m.mu.Lock()
	defer m.mu.Unlock()
	safe := m.safe()
	out := make([]Plugin, 0, len(m.order))
	for _, id := range m.order {
		def := m.defs[id]
		state := m.state(id)
		plugin := Plugin{
			Descriptor: def.descriptor,
			State:      state.state,
			Enabled:    state.state == StateEnabled,
			Faults:     state.faults,
			Error:      state.err,
			Resources:  0,
			UpdatedAt:  state.updatedAt.UTC().Format(time.RFC3339Nano),
			SafeMode:   safe,
			Controlled: "SIMULATED_ONLY",
		}
		if def.descriptor.Sandbox == "TRUSTED_GO" {
			plugin.Controlled = "PORTABLE_GO_ONLY"
		}
		if state.active != nil && state.active.scope != nil {
			plugin.Resources = state.active.scope.Count()
		}
		if safe && (plugin.State == StateDisabled || plugin.State == StateBlocked) {
			plugin.State = StateBlocked
			if plugin.Error == "" {
				plugin.Error = "安全模式已阻止插件启动。"
			}
		}
		out = append(out, plugin)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// EnforceSafeMode is reversible: it stops plugins and releases their resources
// while safe mode is on, but leaves the manager usable once safe mode is off.
func (m *Manager) EnforceSafeMode() {
	if !m.safe() {
		return
	}
	m.mu.Lock()
	var active []*Scope
	for id := range m.defs {
		state := m.state(id)
		if scope := m.takeScopeLocked(state); scope != nil {
			active = append(active, scope)
		}
		if state.state == StateEnabled || state.state == StateEnabling {
			state.state = StateBlocked
			state.err = "安全模式已阻止插件运行；资源已释放。"
			state.updatedAt = time.Now().UTC()
			_ = m.persistLocked(id, state)
		}
	}
	m.mu.Unlock()
	for _, scope := range active {
		scope.Dispose()
	}
}

func (m *Manager) Enable(id string) (Plugin, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Plugin{}, apiError(409, "MANAGER_CLOSED", "插件管理器已关闭")
	}
	def, ok := m.defs[id]
	if !ok {
		m.mu.Unlock()
		return Plugin{}, apiError(404, "PLUGIN_NOT_FOUND", "内置插件不存在；不支持安装任意插件")
	}
	state := m.state(id)
	if m.safe() {
		state.state = StateBlocked
		state.err = "安全模式已阻止插件启动。"
		state.updatedAt = time.Now().UTC()
		err := m.persistLocked(id, state)
		m.mu.Unlock()
		if err != nil {
			return Plugin{}, apiError(500, "PLUGIN_STATE_UNAVAILABLE", "无法保存插件状态")
		}
		return m.snapshot(id), apiError(409, "SAFE_MODE", "安全模式已阻止插件启动")
	}
	if state.state == StateEnabled && state.active != nil {
		m.mu.Unlock()
		return m.snapshot(id), nil
	}
	if state.state == StateEnabling && state.active != nil {
		// A concurrent enable joins the running activation instead of creating a
		// competing scope. It never restarts after a cancellation.
		token := state.active.token
		done := state.active.done
		m.mu.Unlock()
		select {
		case <-done:
		case <-time.After(activationWaitLimit):
			return m.snapshot(id), apiError(409, "PLUGIN_ENABLING", "插件启动超时，状态保持 ENABLING")
		}
		m.mu.Lock()
		state = m.state(id)
		enabled := state.state == StateEnabled && state.active != nil && state.active.token == token
		finalState := state.state
		m.mu.Unlock()
		if enabled {
			return m.snapshot(id), nil
		}
		if finalState == StateFaulted {
			return m.snapshot(id), apiError(502, "PLUGIN_ACTIVATION_FAILED", "插件启动失败，已回滚其资源；其他插件不受影响")
		}
		return m.snapshot(id), apiError(409, "PLUGIN_ACTIVATION_CANCELLED", "插件启动已被取消，资源已释放")
	}

	m.token++
	token := m.token
	scope := &Scope{}
	state.state = StateEnabling
	state.err = ""
	state.updatedAt = time.Now().UTC()
	state.active = &activation{scope: scope, token: token, done: make(chan struct{})}
	done := state.active.done
	m.mu.Unlock()
	defer close(done)

	err := runActivation(def.activate, scope)
	if err != nil {
		failures := scope.Dispose()
		m.mu.Lock()
		state = m.state(id)
		if m.closed || state.active == nil || state.active.token != token {
			// Disable, safe mode or close cancelled this attempt while it was
			// running and a newer attempt may already own the state. Its late
			// failure must not fault or clear that newer activation.
			m.mu.Unlock()
			return Plugin{}, apiError(409, "PLUGIN_ACTIVATION_CANCELLED", "插件启动已被取消，资源已释放")
		}
		state.active = nil
		state.state = StateFaulted
		state.faults++
		state.err = sanitize(err.Error())
		state.updatedAt = time.Now().UTC()
		if len(failures) > 0 {
			state.err = sanitize(state.err + "；清理失败：" + strings.Join(failures, "；"))
		}
		persistErr := m.persistLocked(id, state)
		m.mu.Unlock()
		if persistErr != nil {
			return Plugin{}, apiError(500, "PLUGIN_STATE_UNAVAILABLE", "插件启动失败且状态未能保存")
		}
		return m.snapshot(id), apiError(502, "PLUGIN_ACTIVATION_FAILED", "插件启动失败，已回滚其资源；其他插件不受影响")
	}

	m.mu.Lock()
	state = m.state(id)
	if m.closed || state.active == nil || state.active.token != token {
		// Disable, close or safe mode cancelled this attempt while it was running.
		m.mu.Unlock()
		scope.Dispose()
		return m.snapshot(id), apiError(409, "PLUGIN_ACTIVATION_CANCELLED", "插件启动已被取消，资源已释放")
	}
	state.state = StateEnabled
	state.err = ""
	state.updatedAt = time.Now().UTC()
	persistErr := m.persistLocked(id, state)
	m.mu.Unlock()
	if persistErr != nil {
		m.Disable(id)
		return Plugin{}, apiError(500, "PLUGIN_STATE_UNAVAILABLE", "无法保存插件状态，已回滚启动")
	}
	return m.snapshot(id), nil
}

func runActivation(activate Activation, scope *Scope) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("plugin panic isolated: %v", recovered)
		}
	}()
	return activate(scope)
}

func (m *Manager) Disable(id string) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return apiError(409, "MANAGER_CLOSED", "插件管理器已关闭")
	}
	if _, ok := m.defs[id]; !ok {
		m.mu.Unlock()
		return apiError(404, "PLUGIN_NOT_FOUND", "内置插件不存在")
	}
	state := m.state(id)
	scope := m.takeScopeLocked(state)
	state.state = StateDisabled
	state.updatedAt = time.Now().UTC()
	persistErr := m.persistLocked(id, state)
	m.mu.Unlock()

	var failures []string
	if scope != nil {
		failures = scope.Dispose()
	}
	if len(failures) > 0 && persistErr == nil {
		m.mu.Lock()
		state = m.state(id)
		if state.state == StateDisabled {
			state.err = sanitize("资源清理部分失败：" + strings.Join(failures, "；"))
			persistErr = m.persistLocked(id, state)
		}
		m.mu.Unlock()
	}
	if persistErr != nil {
		return apiError(500, "PLUGIN_STATE_UNAVAILABLE", "无法保存插件状态")
	}
	return nil
}

func (m *Manager) Fault(id string) (Plugin, error) {
	m.mu.Lock()
	def, ok := m.defs[id]
	if !ok {
		m.mu.Unlock()
		return Plugin{}, apiError(404, "PLUGIN_NOT_FOUND", "内置插件不存在")
	}
	if m.safe() {
		m.mu.Unlock()
		return m.snapshot(id), apiError(409, "SAFE_MODE", "安全模式已阻止插件运行")
	}
	state := m.state(id)
	if state.state != StateEnabled || state.active == nil {
		m.mu.Unlock()
		return m.snapshot(id), apiError(409, "PLUGIN_NOT_ENABLED", "只有已启用的插件才能触发故障")
	}
	token := state.active.token
	selfCheck := def.selfCheck
	m.mu.Unlock()

	var err error
	if selfCheck != nil {
		err = runSelfCheck(selfCheck)
	}
	if err == nil {
		return m.snapshot(id), nil
	}

	m.mu.Lock()
	state = m.state(id)
	if state.active == nil || state.active.token != token {
		// The attempt was cancelled while the self-check was running.
		m.mu.Unlock()
		return m.snapshot(id), apiError(409, "PLUGIN_ACTIVATION_CANCELLED", "插件已在自检期间被停用")
	}
	scope := m.takeScopeLocked(state)
	state.state = StateFaulted
	state.faults++
	state.err = sanitize(err.Error())
	state.updatedAt = time.Now().UTC()
	persistErr := m.persistLocked(id, state)
	m.mu.Unlock()

	var failures []string
	if scope != nil {
		failures = scope.Dispose()
	}
	if len(failures) > 0 {
		m.mu.Lock()
		state = m.state(id)
		if state.state == StateFaulted {
			state.err = sanitize(state.err + "；清理失败：" + strings.Join(failures, "；"))
			persistErr = m.persistLocked(id, state)
		}
		m.mu.Unlock()
	}
	if persistErr != nil {
		return Plugin{}, apiError(500, "PLUGIN_STATE_UNAVAILABLE", "插件故障状态未能保存")
	}
	return m.snapshot(id), apiError(502, "PLUGIN_FAULTED", "插件自检失败，已隔离并释放其资源；其他插件不受影响")
}

func runSelfCheck(check func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("plugin panic isolated: %v", recovered)
		}
	}()
	return check()
}

func (m *Manager) snapshot(id string) Plugin {
	for _, plugin := range m.List() {
		if plugin.ID == id {
			return plugin
		}
	}
	return Plugin{}
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	type pending struct {
		id    string
		scope *Scope
	}
	var active []pending
	for id := range m.defs {
		state := m.state(id)
		if scope := m.takeScopeLocked(state); scope != nil {
			active = append(active, pending{id: id, scope: scope})
		}
		if state.state == StateEnabled || state.state == StateEnabling {
			state.state = StateDisabled
		}
		state.updatedAt = time.Now().UTC()
	}
	m.mu.Unlock()

	var cleanupErrors []string
	for _, item := range active {
		if failures := item.scope.Dispose(); len(failures) > 0 {
			cleanupErrors = append(cleanupErrors, failures...)
		}
	}

	m.mu.Lock()
	for id := range m.defs {
		if err := m.persistLocked(id, m.state(id)); err != nil {
			cleanupErrors = append(cleanupErrors, err.Error())
		}
	}
	m.mu.Unlock()
	if len(cleanupErrors) > 0 {
		return errors.New("plugin cleanup incomplete: " + strings.Join(cleanupErrors, "; "))
	}
	return nil
}

func (m *Manager) Register(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("GET "+APIPrefix+"/plugins", m.handleList)
	mux.HandleFunc("GET "+APIPrefix+"/plugins/health", m.handleHealth)
	mux.HandleFunc("POST "+APIPrefix+"/plugins/{id}", m.handleAction)
}

func (m *Manager) handleList(w http.ResponseWriter, _ *http.Request) {
	plugins := m.List()
	httpx.JSON(w, http.StatusOK, map[string]any{
		"items":    plugins,
		"total":    len(plugins),
		"safeMode": m.safe(),
		"notice":   "全部为内置可信模拟插件，在进程内运行；不执行任意 JavaScript，不启动 Node，也没有真实插件进程。这是 Mock，不是操作系统沙箱。",
	})
}

func (m *Manager) handleHealth(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, m.Health(r.Context()))
}

func (m *Manager) handleAction(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	var body struct {
		Action string `json:"action"`
	}
	if !httpx.Decode(w, r, &body) {
		return
	}
	action := strings.ToUpper(strings.TrimSpace(body.Action))
	switch action {
	case ActionEnable:
		plugin, err := m.Enable(id)
		if err != nil {
			fail(w, err, plugin)
			return
		}
		httpx.JSON(w, http.StatusOK, plugin)
	case ActionDisable:
		if err := m.Disable(id); err != nil {
			fail(w, err, Plugin{})
			return
		}
		httpx.JSON(w, http.StatusOK, m.snapshot(id))
	case ActionFault:
		plugin, err := m.Fault(id)
		if err != nil {
			fail(w, err, plugin)
			return
		}
		httpx.JSON(w, http.StatusOK, plugin)
	case "":
		httpx.Fail(w, http.StatusBadRequest, "ACTION_REQUIRED", "action 必须是 ENABLE、DISABLE 或 FAULT")
	default:
		httpx.Fail(w, http.StatusBadRequest, "ACTION_INVALID", "action 必须是 ENABLE、DISABLE 或 FAULT")
	}
}

type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

func apiError(status int, code, message string) *APIError {
	return &APIError{Status: status, Code: code, Message: message}
}

func fail(w http.ResponseWriter, err error, plugin Plugin) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if plugin.ID != "" {
			httpx.JSON(w, apiErr.Status, map[string]any{"error": httpx.Error{Code: apiErr.Code, Message: apiErr.Message}, "plugin": plugin})
			return
		}
		httpx.Fail(w, apiErr.Status, apiErr.Code, apiErr.Message)
		return
	}
	httpx.Fail(w, http.StatusInternalServerError, "INTERNAL_ERROR", "内部错误")
}

func sanitize(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > 300 {
		return text[:300] + "…"
	}
	return text
}

func (m *Manager) Health(ctx context.Context) map[string]any {
	m.EnforceSafeMode()
	plugins := m.List()
	enabled, faulted, resources := 0, 0, 0
	var errs []string
	for _, plugin := range plugins {
		if plugin.Enabled {
			enabled++
		}
		if plugin.State == StateFaulted {
			faulted++
			if plugin.Error != "" {
				errs = append(errs, plugin.ID+": "+plugin.Error)
			}
		}
		resources += plugin.Resources
	}
	return map[string]any{
		"status":    "OK",
		"safeMode":  m.safe(),
		"total":     len(plugins),
		"enabled":   enabled,
		"faulted":   faulted,
		"resources": resources,
		"errors":    errs,
		"processes": 0,
		"mode":      "SIMULATED_IN_PROCESS",
	}
}
