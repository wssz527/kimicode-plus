package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// This file implements the API v2 surface whose behaviour lives in the core: the
// browser-view registry, the plugin command registry, attachment validation and
// the handoff preview. Every entry point is reached only through runAction,
// which applies the capability and install integrity checks first.

const (
	commandPattern  = `^[a-zA-Z][a-zA-Z0-9._-]{0,63}$`
	maxViewsPerPlug = 4
	attachmentMax   = 16
	commandArgLimit = 64 << 10
)

var (
	viewIDExpr      = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	commandNameExpr = regexp.MustCompile(commandPattern)
)

// browserView is one web view owned by one plugin. The core owns the record and
// its authorization; the Electron side owns the actual WebContentsView and is
// told what the core accepted.
type browserView struct {
	PluginID   string `json:"pluginId"`
	InstanceID string `json:"instanceId"`
	ID         string `json:"viewId"`
	URL        string `json:"url"`
	Visible    bool   `json:"visible"`
	Created    string `json:"createdAt"`
}

// owned is the shape every browser answer uses. The host applies an Electron
// operation only for the plugin named here, and that name comes from the
// identity token this call was authorized with — never from the request body.
// A plugin therefore cannot address another plugin's page by naming it.
func owned(view browserView) map[string]any {
	return map[string]any{
		"pluginId":   view.PluginID,
		"instanceId": view.InstanceID,
		"viewId":     view.ID,
		"url":        view.URL,
		"visible":    view.Visible,
		"createdAt":  view.Created,
	}
}

// validateBrowserURL accepts only http(s), bounds the length, and refuses embedded
// credentials, so a plugin cannot smuggle file:, data: or javascript: into the
// isolated browser session.
func validateBrowserURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return "", apiError(http.StatusBadRequest, "PLUGIN_BROWSER_URL", "网页地址必须为 1–2048 个字符")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", apiError(http.StatusBadRequest, "PLUGIN_BROWSER_URL", "只允许有效的 http 或 https 地址")
	}
	if parsed.User != nil {
		return "", apiError(http.StatusBadRequest, "PLUGIN_BROWSER_URL", "网页地址不能包含账号或密码")
	}
	return parsed.String(), nil
}

// browserAction handles create, navigate, bounds, visibility, extraction and
// destruction of the caller's own views. A view id is scoped to its owner:
// another plugin's id is reported as missing, not denied, so the answer cannot be
// used to enumerate other plugins.
func (m *Manager) browserAction(ctx context.Context, id, action string, r *http.Request, params json.RawMessage) (any, error) {
	var input struct {
		ViewID     string  `json:"viewId"`
		InstanceID string  `json:"instanceId"`
		URL        string  `json:"url"`
		Visible    bool    `json:"visible"`
		X          float64 `json:"x"`
		Y          float64 `json:"y"`
		Width      float64 `json:"width"`
		Height     float64 `json:"height"`
		Mode       string  `json:"mode"`
		// Adapter is the site adapter a plugin asks the host to run against its
		// own view. The plugin supplies the source and the arguments; the core
		// only bounds and checks them, and the Electron side executes them in the
		// page with no access to the bridge.
		Adapter json.RawMessage `json:"adapter"`
	}
	if err := decodeInto(ctx, r, params, &input); err != nil {
		return nil, err
	}
	viewID := strings.TrimSpace(input.ViewID)
	instanceID := strings.TrimSpace(input.InstanceID)
	if !viewIDExpr.MatchString(instanceID) {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_BROWSER_VIEW", "网页视图实例 ID 无效")
	}
	registryID := instanceID + "/" + viewID
	if action != "browser.open" && viewID == "" {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_BROWSER_VIEW", "缺少 viewId")
	}
	if viewID != "" && !viewIDExpr.MatchString(viewID) {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_BROWSER_VIEW", "viewId 只允许字母、数字、下划线和连字符")
	}
	switch action {
	case "browser.open":
		target, err := validateBrowserURL(input.URL)
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		if m.views == nil {
			m.views = map[string]map[string]*browserView{}
		}
		views := m.views[id]
		if views == nil {
			views = map[string]*browserView{}
			m.views[id] = views
		}
		if _, exists := views[registryID]; !exists && len(views) >= maxViewsPerPlug {
			m.mu.Unlock()
			return nil, apiError(http.StatusConflict, "PLUGIN_BROWSER_LIMIT", fmt.Sprintf("每个插件最多 %d 个网页视图", maxViewsPerPlug))
		}
		view := &browserView{PluginID: id, InstanceID: instanceID, ID: viewID, URL: target, Created: m.now().UTC().Format(time.RFC3339)}
		views[registryID] = view
		snapshot := *view
		m.mu.Unlock()
		return owned(snapshot), nil
	}
	m.mu.Lock()
	view := m.views[id][registryID]
	if view == nil {
		m.mu.Unlock()
		return nil, apiError(http.StatusNotFound, "PLUGIN_BROWSER_VIEW_MISSING", "没有这个网页视图")
	}
	switch action {
	case "browser.navigate":
		target, err := validateBrowserURL(input.URL)
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		view.URL = target
		snapshot := *view
		m.mu.Unlock()
		return owned(snapshot), nil
	case "browser.back", "browser.forward", "browser.reload", "browser.openExternal":
		snapshot := *view
		m.mu.Unlock()
		return owned(snapshot), nil
	case "browser.bounds", "browser.visible":
		visible := input.Visible && input.Width > 0 && input.Height > 0
		view.Visible = visible
		snapshot := *view
		m.mu.Unlock()
		return owned(snapshot), nil
	case "browser.close":
		snapshot := *view
		delete(m.views[id], registryID)
		if len(m.views[id]) == 0 {
			delete(m.views, id)
		}
		m.mu.Unlock()
		answer := owned(snapshot)
		answer["closed"] = true
		return answer, nil
	case "browser.extract", "browser.adapter":
		// Both read the live page, which only the Electron side can do. The answer
		// names the exact view the core authorized, so the renderer cannot be
		// talked into reading a different one. An adapter also carries the source
		// it is asked to run, so the caller's own code travels with the answer
		// instead of being looked up anywhere.
		snapshot := *view
		m.mu.Unlock()
		answer := owned(snapshot)
		if action == "browser.adapter" {
			adapter, adapterErr := normalizeAdapter(input.Adapter)
			if adapterErr != nil {
				return nil, adapterErr
			}
			answer["adapter"] = adapter
		}
		return answer, nil
	}
	m.mu.Unlock()
	return nil, apiError(http.StatusBadRequest, "PLUGIN_ACTION_UNKNOWN", "未知的网页视图动作")
}

// releaseViews drops every view a plugin owns. The Electron side is torn down by
// the renderer when the plugin's scope is released; this registry is the core's
// half of the same transition, so a stopped plugin cannot be addressed again.
func (m *Manager) releaseViews(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.views, id)
}

// sessionWatch runs one watch or unwatch for a plugin, forwarding to the core
// only on the transition. The plugin's own count is what decides, so a plugin
// with two panes on one session sends one subscribe and one unsubscribe, and
// another plugin watching the same session is unaffected.
//
// The whole adjust-then-forward sequence holds sessionWatchMu. Two panes that
// open on the same session at the same time would otherwise both read zero,
// both claim the transition, and the one whose subscribe was refused would roll
// its count back while the other kept a stream it never opened.
func (m *Manager) sessionWatch(ctx context.Context, id, sessionID string, watch bool) (map[string]any, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(sessionID) > 200 || strings.ContainsAny(sessionID, "\x00\r\n") {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_SESSION_REQUIRED", "缺少会话 ID")
	}
	m.sessionWatchMu.Lock()
	defer m.sessionWatchMu.Unlock()
	forward := m.adjustSessionWatch(id, sessionID, watch)
	if !forward {
		return map[string]any{"sessionId": sessionID, "watching": watch, "forwarded": false}, nil
	}
	// Read under the same lock the adjustment used: Disable and Close may swap
	// the core between the two steps.
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if core == nil {
		m.adjustSessionWatch(id, sessionID, !watch)
		return nil, apiError(http.StatusServiceUnavailable, "PLUGIN_CORE_UNAVAILABLE", "核心服务尚未就绪，无法执行该动作")
	}
	action := "sessions.unwatch"
	if watch {
		action = "sessions.watch"
	}
	payload, _ := json.Marshal(map[string]any{"id": sessionID})
	value, err := core.Action(ctx, action, payload)
	if err != nil {
		// A forward that failed must not leave the local count claiming the
		// transition happened. A refused subscribe would then skip every later
		// subscribe, and a refused unsubscribe would drop the plugin's last
		// handle while the official stream stayed open with nothing left to
		// close it. Reversing the same adjustment restores the count exactly.
		m.adjustSessionWatch(id, sessionID, !watch)
		return nil, coreFailure(action, err)
	}
	if result, ok := value.(map[string]any); ok {
		result["forwarded"] = true
		return result, nil
	}
	return map[string]any{"sessionId": sessionID, "watching": watch, "forwarded": true}, nil
}

// adjustSessionWatch applies one handle change and reports whether it is the
// plugin-wide transition that has to reach the core. It is also how a failed
// forward is rolled back, which is why it takes the direction rather than
// assuming a watch.
func (m *Manager) adjustSessionWatch(id, sessionID string, watch bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessionWatches == nil {
		m.sessionWatches = map[string]map[string]int{}
	}
	held := m.sessionWatches[id]
	if held == nil {
		held = map[string]int{}
		m.sessionWatches[id] = held
	}
	forward := false
	switch {
	case watch:
		forward = held[sessionID] == 0
		held[sessionID] += 1
	case held[sessionID] > 1:
		held[sessionID] -= 1
	case held[sessionID] == 1:
		delete(held, sessionID)
		forward = true
	}
	if len(held) == 0 {
		delete(m.sessionWatches, id)
	}
	return forward
}

// releaseSessionWatches drops everything a plugin was watching, forwarding one
// unsubscribe per session it actually held.
func (m *Manager) releaseSessionWatches(ctx context.Context, id string) error {
	// Taking the transition lock keeps a watch that is already forwarding from
	// re-creating the count after the release read it, which would leave a
	// subscription nothing owns any more.
	m.sessionWatchMu.Lock()
	defer m.sessionWatchMu.Unlock()
	m.mu.Lock()
	held := m.sessionWatches[id]
	delete(m.sessionWatches, id)
	core := m.core
	m.mu.Unlock()
	if len(held) == 0 || core == nil {
		return nil
	}
	var failures []string
	for sessionID := range held {
		payload, _ := json.Marshal(map[string]any{"id": sessionID})
		if _, err := core.Action(ctx, "sessions.unwatch", payload); err != nil {
			failures = append(failures, sessionID)
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("会话订阅未能全部释放：%s", strings.Join(failures, ", "))
	}
	return nil
}

// Views reports the views one plugin still owns, which is the observable proof
// that a stopped plugin released everything.
func (m *Manager) Views(id string) []browserView {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []browserView{}
	for _, view := range m.views[id] {
		out = append(out, *view)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID == out[j].ID {
			return out[i].InstanceID < out[j].InstanceID
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// command is one registered plugin command.
type command struct {
	Name    string `json:"name"`
	OwnerID string `json:"pluginId"`
	Owner   string `json:"pluginName"`
	Title   string `json:"title,omitempty"`
}

// registerCommand records a command owned by one plugin. Re-registering the same
// name by the same owner replaces it; a different owner cannot take a name that
// is already in use, which keeps one plugin from hijacking another's entry point.
func (m *Manager) registerCommand(id, name, title string) (command, error) {
	name = strings.TrimSpace(name)
	if !commandNameExpr.MatchString(name) {
		return command{}, apiError(http.StatusBadRequest, "PLUGIN_COMMAND_NAME", "命令名只允许字母、数字、点、下划线和连字符，并以字母开头")
	}
	title = strings.TrimSpace(title)
	if len(title) > 80 {
		return command{}, apiError(http.StatusBadRequest, "PLUGIN_COMMAND_NAME", "命令标题不超过 80 个字符")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.commands == nil {
		m.commands = map[string]command{}
	}
	if existing, ok := m.commands[name]; ok && existing.OwnerID != id {
		return command{}, apiError(http.StatusConflict, "PLUGIN_COMMAND_TAKEN", "命令 "+name+" 已被另一个插件注册")
	}
	owner := ""
	if item := m.entries[id]; item != nil {
		owner = item.manifest.Name
	}
	entry := command{Name: name, OwnerID: id, Owner: owner, Title: title}
	m.commands[name] = entry
	return entry, nil
}

func (m *Manager) unregisterCommand(id, name string) bool {
	name = strings.TrimSpace(name)
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.commands[name]
	if !ok || existing.OwnerID != id {
		return false
	}
	delete(m.commands, name)
	return true
}

// listCommands reports every visible command. The list is what one plugin uses to
// discover a capability another plugin published.
func (m *Manager) listCommands() []command {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]command, 0, len(m.commands))
	for _, entry := range m.commands {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// releaseCommands drops every command one plugin owned, so a disabled plugin
// leaves no callable stub behind.
func (m *Manager) releaseCommands(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, entry := range m.commands {
		if entry.OwnerID == id {
			delete(m.commands, name)
		}
	}
}

// authorizeCommand resolves the owner of a command the caller wants to invoke.
// Execution happens in the renderer that hosts the owning plugin, but the owner,
// its running state and its grant are decided here, so a cross-plugin call obeys
// the same rules as any other privileged action. An unknown command is a hard
// error: the runtime must never fabricate a successful call.
func (m *Manager) authorizeCommand(id, name string) (any, error) {
	name = strings.TrimSpace(name)
	if !commandNameExpr.MatchString(name) {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_COMMAND_NAME", "命令名无效")
	}
	m.mu.Lock()
	registered, ok := m.commands[name]
	var owner *entry
	var ownerState string
	if ok {
		owner = m.entries[registered.OwnerID]
		if owner != nil {
			ownerState = owner.state
		}
	}
	m.mu.Unlock()
	if !ok {
		return nil, apiError(http.StatusNotFound, "PLUGIN_COMMAND_UNKNOWN", "没有插件注册命令 "+name)
	}
	if owner == nil || ownerState != StateEnabled {
		return nil, apiError(http.StatusConflict, "PLUGIN_COMMAND_UNAVAILABLE", "命令所属插件未运行，无法调用 "+name)
	}
	if !owner.grants.Has(CapCommands) {
		return nil, apiError(http.StatusConflict, "PLUGIN_COMMAND_UNAVAILABLE", "命令所属插件没有 commands 能力")
	}
	return map[string]any{"command": registered.Name, "ownerId": registered.OwnerID, "owner": registered.Owner, "title": registered.Title}, nil
}

// commandsAction is the plugin-facing command registry surface.
// coreActionFor maps a privileged plugin action to the native action that
// implements it. A name absent here is forwarded unchanged only after the
// capability table matched it exactly.
var coreActionFor = map[string]string{
	"sessions.approve":  "session.reply",
	"sessions.abort":    "session.abort",
	"sessions.history":  "sessions.history",
	"models.list":       "models.list",
	"handoff.commit":    "handoff",
	"planner.threads":   "planner.threads",
	"planner.models":    "planner.models",
	"planner.create":    "planner.service.planner.create",
	"planner.get":       "planner.service.planner.get",
	"planner.configure": "planner.service.planner.configure",
	"planner.rename":    "planner.service.planner.rename",
	"planner.send":      "planner.service.planner.send",
	"planner.retry":     "planner.service.planner.retry",
	"planner.stop":      "planner.service.planner.stop",
	"planner.approve":   "planner.service.planner.approve",
	"planner.delete":    "planner.service.planner.delete",
}

// forwardParams rewrites plugin parameters into the native action's shape. Each
// entry is explicit so a plugin cannot smuggle extra fields into the native
// service through a generic passthrough.
func forwardParams(action string, payload map[string]any) map[string]any {
	switch action {
	case "sessions.approve":
		return map[string]any{
			"id":            payload["id"],
			"interactionId": payload["interactionId"],
			"value":         payload["value"],
		}
	case "sessions.abort", "sessions.diff":
		return map[string]any{"id": payload["id"]}
	case "sessions.history":
		return map[string]any{
			"id":      payload["id"],
			"before":  payload["beforeOrdinal"],
			"size":    payload["pageSize"],
			"agentId": payload["agentId"],
		}
	default:
		return payload
	}
}

// validateSessionAction rejects a malformed call here instead of forwarding it to
// the native service. The native layer answers a missing id as a plain error,
// which the plugin surface would report as an opaque PLUGIN_ACTION_FAILED; a
// 400 names the field the plugin got wrong.
func validateSessionAction(action string, payload map[string]any) error {
	if stringValue(payload["id"]) == "" {
		return apiError(http.StatusBadRequest, "PLUGIN_SESSION_REQUIRED", "缺少会话 ID")
	}
	switch action {
	case "sessions.approve":
		if stringValue(payload["interactionId"]) == "" {
			return apiError(http.StatusBadRequest, "PLUGIN_APPROVAL_REQUIRED", "缺少审批 ID")
		}
	case "sessions.history":
		if size, ok := payload["pageSize"].(float64); ok && (size < 1 || size > 100 || size != math.Trunc(size)) {
			return apiError(http.StatusBadRequest, "PLUGIN_HISTORY_PAGE", "pageSize 必须是 1–100 的整数")
		}
		if before, ok := payload["beforeOrdinal"].(float64); ok && (before < 0 || before != math.Trunc(before)) {
			return apiError(http.StatusBadRequest, "PLUGIN_HISTORY_PAGE", "beforeOrdinal 必须是非负整数")
		}
	}
	return nil
}

func (m *Manager) openSession(ctx context.Context, core Core, payload map[string]any) (any, error) {
	if err := validateSessionAction("sessions.open", payload); err != nil {
		return nil, err
	}
	id := stringValue(payload["id"])
	body, _ := json.Marshal(map[string]any{"id": id})
	if _, err := core.Action(ctx, "sessions.view", body); err != nil {
		return nil, coreFailure("sessions.open", err)
	}
	return map[string]any{"id": id}, nil
}

func (m *Manager) commandsAction(ctx context.Context, id, action string, r *http.Request, params json.RawMessage) (any, error) {
	var input struct {
		Name    string          `json:"name"`
		Title   string          `json:"title"`
		Command string          `json:"command"`
		Args    json.RawMessage `json:"args"`
	}
	if err := decodeInto(ctx, r, params, &input); err != nil {
		return nil, err
	}
	switch action {
	case "commands.register":
		return m.registerCommand(id, input.Name, input.Title)
	case "commands.unregister":
		return map[string]any{"removed": m.unregisterCommand(id, input.Name)}, nil
	case "commands.list":
		return map[string]any{"items": m.listCommands()}, nil
	case "commands.invoke":
		if len(input.Args) > commandArgLimit {
			return nil, apiError(http.StatusRequestEntityTooLarge, "PLUGIN_COMMAND_ARGS", "命令参数超过 64 KiB")
		}
		if len(input.Args) > 0 && !json.Valid(input.Args) {
			return nil, apiError(http.StatusBadRequest, "PLUGIN_COMMAND_ARGS", "命令参数不是合法 JSON")
		}
		result, err := m.authorizeCommand(id, input.Command)
		if err != nil {
			return nil, err
		}
		_ = recordActivity(ctx, m.db, id, ActivityCommand, "插件调用命令："+input.Command, m.now())
		// The renderer that hosts the owner is the only place the command body
		// can run, so the authorized target travels with the call. The args are
		// forwarded verbatim and unparsed: the core has no idea what a command
		// expects and must not quietly reshape it.
		if len(input.Args) > 0 {
			result.(map[string]any)["args"] = json.RawMessage(input.Args)
		} else {
			result.(map[string]any)["args"] = nil
		}
		return result, nil
	}
	return nil, apiError(http.StatusBadRequest, "PLUGIN_ACTION_UNKNOWN", "未知的命令动作")
}

// attachment is the normalized form of one file the user picked. Only absolute
// paths to regular files are accepted, the same rule sessions.send applies.
type attachment struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Size int64  `json:"size"`
}

func normalizeAttachments(values []string) ([]attachment, error) {
	if len(values) == 0 {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_ATTACHMENT_EMPTY", "没有选择附件")
	}
	if len(values) > attachmentMax {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_ATTACHMENT_LIMIT", fmt.Sprintf("一次最多 %d 个附件", attachmentMax))
	}
	out := make([]attachment, 0, len(values))
	for _, raw := range values {
		target := strings.TrimSpace(raw)
		if target == "" || !filepath.IsAbs(target) {
			return nil, apiError(http.StatusBadRequest, "PLUGIN_ATTACHMENT_PATH", "附件必须是本机文件的绝对路径")
		}
		info, err := os.Stat(target)
		if err != nil || !info.Mode().IsRegular() {
			return nil, apiError(http.StatusBadRequest, "PLUGIN_ATTACHMENT_MISSING", "附件文件已不存在或无法读取")
		}
		kind := "file"
		switch strings.ToLower(filepath.Ext(target)) {
		case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
			kind = "image"
		case ".mp4", ".webm", ".mov":
			kind = "video"
		}
		out = append(out, attachment{Path: target, Name: info.Name(), Kind: kind, Size: info.Size()})
	}
	return out, nil
}

// sendAttachments validates the picked files and forwards them through the
// existing sessions.send path, so a plugin gets exactly the same attachment
// handling as the native composer.
func (m *Manager) sendAttachments(ctx context.Context, id string, payload json.RawMessage) (any, error) {
	input, err := decodeObject(payload)
	if err != nil {
		return nil, err
	}
	sessionID := stringValue(input["id"])
	text := stringValue(input["text"])
	if sessionID == "" {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_ATTACHMENT_SESSION", "缺少会话 ID")
	}
	rawList, _ := input["paths"].([]any)
	paths := make([]string, 0, len(rawList))
	for _, value := range rawList {
		if path, ok := value.(string); ok {
			paths = append(paths, path)
		}
	}
	attachments, err := normalizeAttachments(paths)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if core == nil {
		return nil, apiError(http.StatusServiceUnavailable, "PLUGIN_CORE_UNAVAILABLE", "核心服务尚未就绪，无法发送附件")
	}
	files := make([]string, 0, len(attachments))
	for _, item := range attachments {
		files = append(files, item.Path)
	}
	mode := stringValue(input["mode"])
	body, _ := json.Marshal(map[string]any{"id": sessionID, "text": text, "attachments": files, "mode": mode})
	value, err := core.Action(ctx, "sessions.send", body)
	// A staged batch belongs to this one send. It is removed whether the send
	// succeeded or not: a failed send must not leave the dropped bytes on disk
	// for the plugin to reuse later.
	m.releaseBatch(id, stringValue(input["batch"]))
	if err != nil {
		return nil, coreFailure("attachments.send", err)
	}
	_ = recordActivity(ctx, m.db, id, ActivitySession, "插件发送附件消息", m.now())
	return value, nil
}

// releaseBatch removes one staged batch. Only a name the core itself generated is
// accepted: a caller that sends a path instead of a batch name has it reduced to
// its base name and is still confined to this plugin's staging root, so a plugin
// can never delete anything outside its own directory.
func (m *Manager) releaseBatch(id, batch string) {
	batch = filepath.Base(strings.TrimSpace(batch))
	if batch == "" || batch == "." || batch == ".." || batch == string(filepath.Separator) {
		return
	}
	root, err := m.stageRoot(id)
	if err != nil {
		return
	}
	_ = os.RemoveAll(filepath.Join(root, batch))
}

// handoffPreview builds the preview a handoff shows before anything is sent. It
// reports the normalized target and the exact text, so the plugin can render a
// form without duplicating the core's validation. The directory, model and title
// are echoed for a new session only: an existing target keeps its own.
func handoffPreview(targetID, mode, text, cwd, model, title string) (any, error) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > 200000 {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_HANDOFF_TEXT", "交接内容必须为 1–200000 字节")
	}
	targetID = strings.TrimSpace(targetID)
	mode = strings.TrimSpace(mode)
	switch mode {
	case "":
		mode = "inherit"
		if targetID == "" {
			mode = "plan"
		}
	case "inherit":
		if targetID == "" {
			return nil, apiError(http.StatusBadRequest, "PLUGIN_HANDOFF_MODE", "仅已有会话可以保留原模式")
		}
	case "normal", "plan", "tower":
	default:
		return nil, apiError(http.StatusBadRequest, "PLUGIN_HANDOFF_MODE", "交接模式必须是 inherit、normal、plan 或 tower")
	}
	preview := map[string]any{
		"targetId":   targetID,
		"mode":       mode,
		"text":       text,
		"bytes":      len(text),
		"newSession": targetID == "",
	}
	if targetID == "" {
		// The core makes the final call on every one of these (the directory must
		// exist, the model must be in the catalog); the preview only shows what
		// would be requested.
		if cwd = strings.TrimSpace(cwd); cwd != "" {
			preview["cwd"] = cwd
		}
		if model = strings.TrimSpace(model); model != "" {
			preview["model"] = model
		}
		if title = strings.TrimSpace(title); title != "" {
			preview["title"] = title
		}
	}
	return preview, nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func decodeObject(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, nil
	}
	value := map[string]any{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_PARAMS_INVALID", "动作参数必须是 JSON 对象")
	}
	return value, nil
}
