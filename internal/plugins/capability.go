package plugins

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
)

// Capability names from the v1 manifest. The spelling is part of the public
// plugin contract, so it is fixed here rather than derived per caller.
const (
	CapMetricsRead   = "metrics:read"
	CapSessionsRead  = "sessions:read"
	CapSessionsSend  = "sessions:send"
	CapConfigRead    = "config:read"
	CapConfigWrite   = "config:write"
	CapUISidebar     = "ui:sidebar"
	CapUISettings    = "ui:settings"
	CapUIThinking    = "ui:thinking"
	CapUIMessage     = "ui:message"
	CapStorage       = "storage"
	capNetPrefix     = "net:"
	capCredentialPre = "credentials:"

	// API v2 capabilities. Every one of them is privileged: it is granted only to
	// an official bundled plugin whose installed content still matches the digest
	// compiled into this binary. They are listed separately so the v1 consent
	// path and the v2 privilege gate cannot drift apart.
	CapUIEntry         = "ui:entry"
	CapUISidebarNav    = "ui:sidebar-nav"
	CapUIMainArea      = "ui:main-area"
	CapUIRightPanel    = "ui:right-panel"
	CapUILayout        = "ui:layout"
	CapSessionsApprove = "sessions:approve"
	CapSessionsAbort   = "sessions:abort"
	CapSessionsHistory = "sessions:history"
	CapAttachments     = "attachments"
	CapBrowserView     = "browser:view"
	CapPlannerRun      = "planner:run"
	CapHandoff         = "handoff"
	CapModelsReadV2    = "models:read"
	CapCommands        = "commands"
	CapScheduleManage  = "schedule:manage"
)

// privilegedCapabilities is the closed set of v2 high-privilege capabilities.
// Membership is what the core checks at grant and action time; a name that is
// not here is an ordinary v1 capability and keeps its old consent path.
var privilegedCapabilities = []string{
	CapUIEntry, CapUISidebarNav, CapUIMainArea, CapUIRightPanel, CapUILayout,
	CapSessionsApprove, CapSessionsAbort, CapSessionsHistory,
	CapAttachments, CapBrowserView, CapPlannerRun, CapHandoff, CapModelsReadV2, CapCommands,
	CapScheduleManage,
}

// RequiresPrivilege reports whether a capability belongs to the v2 set that is
// reserved for official bundled plugins.
func RequiresPrivilege(name string) bool { return contains(privilegedCapabilities, name) }

// uiCapabilities are the mount points a plugin may ask for. Each one is checked
// twice: the renderer will not mount an ungranted slot, and this list is what
// the consent prompt shows.
var uiCapabilities = []string{CapUISidebar, CapUISettings, CapUIThinking, CapUIMessage}

// fixedCapabilities is every capability with no parameter. Anything else must
// carry a parameter behind a documented prefix.
var fixedCapabilities = append([]string{
	CapMetricsRead, CapSessionsRead, CapSessionsSend, CapConfigRead, CapConfigWrite,
	CapUISidebar, CapUISettings, CapUIThinking, CapUIMessage, CapStorage,
}, privilegedCapabilities...)

var (
	netHostPattern      = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
	netHostLocalPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	credentialPattern   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
	// netHostNumericPattern matches anything that only looks like an address: a
	// bare number, a short group count, or 0x/octal notation. Go's resolver would
	// happily turn those into an IP, so a host that matches this and is not the
	// canonical dotted form is refused instead of being read as a name.
	netHostNumericPattern = regexp.MustCompile(`^[0-9a-fx.]+$`)
)

// parseNetHost validates one `net:` host entry. A canonical IP literal is
// accepted and normalised so what the user approved is exactly what the dialer
// connects to; a non-canonical numeric form is refused, because "0x7f000001",
// "2130706433" and "127.1" all resolve to loopback while looking like names.
func parseNetHost(host string) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return "", errors.New("net: 后必须写明主机名")
	}
	// Literals first: an IPv6 address contains colons that the name grammar
	// rejects, so the character check below must not run before this.
	if literal, ok := canonicalIP(host); ok {
		return literal, nil
	}
	if strings.ContainsAny(host, "/:@ \t\n?#") {
		return "", fmt.Errorf("net:%s 只能包含主机名，不含协议、端口或路径", host)
	}
	if strings.HasPrefix(host, "*.") {
		// A wildcard only makes sense over a name: there is no subdomain of an
		// address, and accepting one would let `*.127.0.0.1` read as a scope.
		base := strings.TrimPrefix(host, "*.")
		if _, isLiteral := canonicalIP(base); isLiteral {
			return "", fmt.Errorf("net:%s 不能在 IP 地址上使用通配符", host)
		}
		if netHostNumericPattern.MatchString(base) {
			return "", fmt.Errorf("net:%s 不是规范域名", host)
		}
		if !netHostPattern.MatchString(host) {
			return "", fmt.Errorf("net:%s 不是有效主机名", host)
		}
		return host, nil
	}
	if netHostNumericPattern.MatchString(host) {
		return "", fmt.Errorf("net:%s 不是规范 IP 字面量；数字、缩写或十六进制形式不接受，请写完整的点分地址", host)
	}
	if !netHostPattern.MatchString(host) && !netHostLocalPattern.MatchString(host) {
		return "", fmt.Errorf("net:%s 不是有效主机名", host)
	}
	if !strings.Contains(host, ".") && host != "localhost" {
		return "", fmt.Errorf("net:%s 是单标签主机名；只接受 localhost 或完整域名", host)
	}
	return host, nil
}

// canonicalIP reports whether host is a dotted IP literal and returns its
// canonical form. net.ParseIP accepts exactly the standard notations, so a value
// it rejects is not a literal at all.
func canonicalIP(host string) (string, bool) {
	ip := net.ParseIP(host)
	if ip == nil {
		return "", false
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		return ipv4.String(), true
	}
	// IPv6 literals arrive without brackets from url.Hostname; keep the canonical
	// compressed form so grants and dials compare the same string.
	return ip.String(), true
}

// Grants is a parsed capability list. The zero value grants nothing, so a
// missing or unreadable grant always denies rather than allows.
type Grants struct {
	fixed       map[string]bool
	hosts       []string
	credentials []string
}

// ParseCapabilities validates a manifest capability list. Duplicates are
// rejected because a repeated entry is a declaration mistake, and an unknown
// name is rejected because silently ignoring it would under-grant without the
// plugin author noticing.
func ParseCapabilities(names []string) (Grants, error) {
	grants := Grants{fixed: make(map[string]bool)}
	if len(names) > 64 {
		return grants, errors.New("capabilities 最多 64 项")
	}
	seen := make(map[string]bool, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			return grants, errors.New("capabilities 含空项")
		}
		if seen[name] {
			return grants, fmt.Errorf("capability %q 重复", name)
		}
		seen[name] = true
		switch {
		case contains(fixedCapabilities, name):
			grants.fixed[name] = true
		case strings.HasPrefix(name, capNetPrefix):
			host, err := parseNetHost(strings.TrimPrefix(name, capNetPrefix))
			if err != nil {
				return grants, err
			}
			grants.hosts = append(grants.hosts, host)
		case strings.HasPrefix(name, capCredentialPre):
			credential := strings.TrimPrefix(name, capCredentialPre)
			if !credentialPattern.MatchString(credential) {
				return grants, fmt.Errorf("capability %q 凭据名无效", name)
			}
			grants.credentials = append(grants.credentials, credential)
		default:
			return grants, fmt.Errorf("未知 capability %q", name)
		}
	}
	sort.Strings(grants.hosts)
	sort.Strings(grants.credentials)
	return grants, nil
}

// Has reports a parameterless capability.
func (g Grants) Has(name string) bool { return g.fixed[name] }

// AllowsHost reports whether a URL host is covered. A "*." entry matches any
// single-label-deep subdomain, never the bare domain, so consent for
// "*.example.com" does not silently include "example.com".
func (g Grants) AllowsHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for _, allowed := range g.hosts {
		if allowed == host {
			return true
		}
		if strings.HasPrefix(allowed, "*.") {
			suffix := strings.TrimPrefix(allowed, "*.")
			if strings.HasSuffix(host, "."+suffix) && strings.Count(host, ".") == strings.Count(suffix, ".")+1 {
				return true
			}
		}
	}
	return false
}

// AllowsCredential reports whether a credential name was declared.
func (g Grants) AllowsCredential(name string) bool {
	for _, allowed := range g.credentials {
		if allowed == name {
			return true
		}
	}
	return false
}

// Hosts and Credentials return copies, so a caller cannot widen a stored grant
// by mutating what it received.
func (g Grants) Hosts() []string { return append([]string(nil), g.hosts...) }

func (g Grants) Credentials() []string { return append([]string(nil), g.credentials...) }

// Names rebuilds the canonical capability list for display and storage.
func (g Grants) Names() []string {
	names := make([]string, 0, len(g.fixed)+len(g.hosts)+len(g.credentials))
	for name := range g.fixed {
		names = append(names, name)
	}
	for _, host := range g.hosts {
		names = append(names, capNetPrefix+host)
	}
	for _, credential := range g.credentials {
		names = append(names, capCredentialPre+credential)
	}
	sort.Strings(names)
	return names
}

// AllowsSlot reports whether a UI mount point was granted.
func (g Grants) AllowsSlot(slot string) bool {
	return g.fixed["ui:"+slot]
}

// actionCapability is the single source of truth for plugin API authorization.
// An action that is not in this table cannot be called at all: the default is
// denial, so adding an action without its capability fails closed.
var actionCapability = map[string]string{
	"cache.summary":    CapMetricsRead,
	"cache.sessions":   CapMetricsRead,
	"cache.history":    CapMetricsRead,
	"cache.turns":      CapMetricsRead,
	"quota.balances":   CapMetricsRead,
	"cache.retention":  CapMetricsRead,
	"cache.receipts":   CapMetricsRead,
	"sessions.list":    CapSessionsRead,
	"sessions.view":    CapSessionsRead,
	"sessions.diff":    CapSessionsRead,
	"sessions.open":    CapSessionsRead,
	"sessions.watch":   CapSessionsRead,
	"sessions.unwatch": CapSessionsRead,
	"sessions.send":    CapSessionsSend,
	"sessions.create":  CapSessionsSend,
	"config.get":       CapConfigRead,
	"config.save":      CapConfigWrite,
	"storage.get":      CapStorage,
	"storage.set":      CapStorage,
	"storage.delete":   CapStorage,
	"storage.list":     CapStorage,
	"ui.popout":        CapUIRightPanel,

	// v2 actions. The privilege gate is applied separately in runAction, so a
	// tampered stored grant cannot turn one of these on by itself.
	"sessions.approve":     CapSessionsApprove,
	"sessions.abort":       CapSessionsAbort,
	"sessions.history":     CapSessionsHistory,
	"attachments.stage":    CapAttachments,
	"attachments.release":  CapAttachments,
	"attachments.send":     CapAttachments,
	"browser.open":         CapBrowserView,
	"browser.navigate":     CapBrowserView,
	"browser.back":         CapBrowserView,
	"browser.forward":      CapBrowserView,
	"browser.reload":       CapBrowserView,
	"browser.openExternal": CapBrowserView,
	"browser.bounds":       CapBrowserView,
	"browser.visible":      CapBrowserView,
	"browser.extract":      CapBrowserView,
	"browser.close":        CapBrowserView,
	"browser.adapter":      CapBrowserView,
	"planner.threads":      CapPlannerRun,
	"planner.models":       CapPlannerRun,
	"planner.create":       CapPlannerRun,
	"planner.get":          CapPlannerRun,
	"planner.configure":    CapPlannerRun,
	"planner.rename":       CapPlannerRun,
	"planner.send":         CapPlannerRun,
	"planner.retry":        CapPlannerRun,
	"planner.stop":         CapPlannerRun,
	"planner.approve":      CapPlannerRun,
	"planner.delete":       CapPlannerRun,
	"handoff.preview":      CapHandoff,
	"handoff.commit":       CapHandoff,
	"models.list":          CapModelsReadV2,
	"commands.register":    CapCommands,
	"commands.invoke":      CapCommands,
	"commands.unregister":  CapCommands,
	"commands.list":        CapCommands,
	"schedule.status":      CapScheduleManage,
	"schedule.list":        CapScheduleManage,
	"schedule.save":        CapScheduleManage,
	"schedule.delete":      CapScheduleManage,
	"schedule.toggle":      CapScheduleManage,
	"schedule.runNow":      CapScheduleManage,
	"schedule.runs":        CapScheduleManage,
	"schedule.markRead":    CapScheduleManage,
	"schedule.markAllRead": CapScheduleManage,
	"schedule.prefs":       CapScheduleManage,
	"schedule.checkGit":    CapScheduleManage,
	"schedule.archiveRun":  CapScheduleManage,
}

// eventScopeCapability maps an event scope to the capability that reveals it.
// Scopes that are not listed are never delivered to a plugin, so a new internal
// scope does not silently become plugin-visible.
var eventScopeCapability = map[string]string{
	"cache":      CapMetricsRead,
	"automation": CapMetricsRead,
	"schedule":   CapScheduleManage,
	// activity carries counters only (turn and tool lifecycle), never message
	// text or tool input/output. It is deliberately separate from
	// "session:<id>": a panel that renders numbers must not be handed the
	// transcript stream it would need sessions:read to receive.
	"activity": CapMetricsRead,
}

// EventScopeAllowed reports whether a plugin may subscribe to one event scope.
// Session scopes carry a session id ("session:<id>") and need sessions:read.
func EventScopeAllowed(grants Grants, scope string) bool {
	if capability, ok := eventScopeCapability[scope]; ok {
		return grants.Has(capability)
	}
	if strings.HasPrefix(scope, "session:") {
		return grants.Has(CapSessionsRead)
	}
	// Planner threads publish progress under `planner:<threadId>`. A plugin that
	// may run the planner needs that stream, and nothing else can subscribe to it.
	if strings.HasPrefix(scope, "planner:") {
		return grants.Has(CapPlannerRun)
	}
	return false
}

// Authorize resolves the capability an action requires and reports it when the
// grant does not cover it. The returned error is always PLUGIN_CAPABILITY_DENIED
// so the renderer and the activity log agree on the reason.
func (g Grants) Authorize(action string) error {
	if strings.HasPrefix(action, "net.") || action == "log" || action == "report.error" || action == "runtime.info" {
		// net.fetch needs a host and is checked with the URL; the rest carry no
		// host data and are always available to an enabled plugin.
		return nil
	}
	if strings.HasPrefix(action, "credentials.") {
		// The status list and the save action are useless without a declared
		// credential name, so they are granted by the presence of one. The name
		// itself is checked per call, which is what keeps one plugin's credential
		// out of another plugin's reach.
		if len(g.credentials) == 0 {
			return capabilityDenied(action, capCredentialPre+"<name>")
		}
		return nil
	}
	capability, ok := actionCapability[action]
	if !ok {
		return capabilityDenied(action, "UNKNOWN_ACTION")
	}
	if !g.Has(capability) {
		return capabilityDenied(action, capability)
	}
	return nil
}

// AuthorizeNet checks the host allowlist for a proxied request.
func (g Grants) AuthorizeNet(action, host string) error {
	if !g.AllowsHost(host) {
		return capabilityDenied(action, capNetPrefix+host)
	}
	return nil
}

// AuthorizeCredential checks that a plugin declared the credential it wants
// injected. The plaintext never leaves this check's caller.
func (g Grants) AuthorizeCredential(action, name string) error {
	if !g.AllowsCredential(name) {
		return capabilityDenied(action, capCredentialPre+name)
	}
	return nil
}

func capabilityDenied(action, capability string) error {
	return apiError(403, "PLUGIN_CAPABILITY_DENIED", fmt.Sprintf("插件没有 %s 能力，已拒绝 %s", capability, action))
}

// WithoutPrivileged drops every v2 capability. The stored grant is the user's
// decision, but a plugin that is not an intact official bundle must never see
// those names in its runtime identity.
func (g Grants) WithoutPrivileged() Grants {
	out := Grants{fixed: make(map[string]bool), hosts: append([]string(nil), g.hosts...), credentials: append([]string(nil), g.credentials...)}
	for name := range g.fixed {
		if !RequiresPrivilege(name) {
			out.fixed[name] = true
		}
	}
	return out
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
