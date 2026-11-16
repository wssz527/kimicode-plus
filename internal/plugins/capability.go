package plugins

import (
	"errors"
	"fmt"
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
)

// uiCapabilities are the mount points a plugin may ask for. Each one is checked
// twice: the renderer will not mount an ungranted slot, and this list is what
// the consent prompt shows.
var uiCapabilities = []string{CapUISidebar, CapUISettings, CapUIThinking, CapUIMessage}

// fixedCapabilities is every capability with no parameter. Anything else must
// carry a parameter behind a documented prefix.
var fixedCapabilities = []string{
	CapMetricsRead, CapSessionsRead, CapSessionsSend, CapConfigRead, CapConfigWrite,
	CapUISidebar, CapUISettings, CapUIThinking, CapUIMessage, CapStorage,
}

var (
	netHostPattern      = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
	netHostLocalPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	credentialPattern   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
)

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

func parseNetHost(host string) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return "", errors.New("net: 后必须写明主机名")
	}
	if strings.ContainsAny(host, "/:@ \t\n?#") {
		return "", fmt.Errorf("net:%s 只能包含主机名，不含协议、端口或路径", host)
	}
	if !netHostPattern.MatchString(host) && !netHostLocalPattern.MatchString(host) {
		return "", fmt.Errorf("net:%s 不是有效主机名", host)
	}
	return host, nil
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
	"cache.summary":   CapMetricsRead,
	"cache.sessions":  CapMetricsRead,
	"cache.history":   CapMetricsRead,
	"cache.retention": CapMetricsRead,
	"cache.receipts":  CapMetricsRead,
	"sessions.list":   CapSessionsRead,
	"sessions.view":   CapSessionsRead,
	"sessions.send":   CapSessionsSend,
	"sessions.create": CapSessionsSend,
	"config.get":      CapConfigRead,
	"config.save":     CapConfigWrite,
	"storage.get":     CapStorage,
	"storage.set":     CapStorage,
	"storage.delete":  CapStorage,
	"storage.list":    CapStorage,
}

// eventScopeCapability maps an event scope to the capability that reveals it.
// Scopes that are not listed are never delivered to a plugin, so a new internal
// scope does not silently become plugin-visible.
var eventScopeCapability = map[string]string{
	"cache":      CapMetricsRead,
	"automation": CapMetricsRead,
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

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
