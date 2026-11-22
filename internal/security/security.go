package security

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"enhancedkimi/internal/httpx"
)

var Permissions = []string{"core:read", "settings:write", "models:read", "models:write", "usage:read", "usage:write", "tasks:read", "tasks:write", "chat:read", "chat:write", "plugins:read", "plugins:write", "adapter:write", "credentials:read", "credentials:write", "workspace:read", "workspace:write", "browser:read", "browser:write", "update:read", "update:write"}

const ElectronMainPermission = "host:electron-main"

type electronMainKey struct{}

func HasElectronMain(ctx context.Context) bool {
	return ctx.Value(electronMainKey{}) == true
}

type grant struct {
	permissions map[string]bool
	expires     time.Time
}
type Guard struct {
	mu         sync.Mutex
	host       string
	pairing    string
	grants     map[string]grant
	window     time.Time
	requests   int
	pairWindow time.Time
	pairs      int
}

func Random() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func New(host string) *Guard {
	return &Guard{host: host, pairing: Random(), grants: make(map[string]grant)}
}
func (g *Guard) PairingToken() string { return g.pairing }
func (g *Guard) Host() string         { return g.host }
func (g *Guard) IssueSessionToken(requested []string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	for token, v := range g.grants {
		if now.After(v.expires) {
			delete(g.grants, token)
		}
	}
	if len(requested) == 0 {
		requested = Permissions
	}
	scopes := make(map[string]bool, len(requested))
	for _, p := range requested {
		scopes[p] = true
	}
	token := Random()
	g.grants[token] = grant{scopes, now.Add(12 * time.Hour)}
	return token
}
func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

type authStatus int

const (
	authOK authStatus = iota
	authExpired
	authDenied
)

// authorize separates "this credential is unknown or expired" from "this
// credential is valid but not scoped for the resource". A host that restarted
// clears its grant table, so every token survives as an unknown token; reporting
// that as PERMISSION_DENIED told the client its permissions were wrong and left
// it unable to recover. The distinct statuses let the bridge renew on the first
// and leave the second alone.
func (g *Guard) authorize(token, permission string) (status authStatus, electronMain bool) {
	if equal(token, g.pairing) {
		return authOK, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	v, ok := g.grants[token]
	if !ok || !time.Now().Before(v.expires) {
		return authExpired, false
	}
	if !v.permissions[permission] {
		return authDenied, false
	}
	return authOK, v.permissions[ElectronMainPermission]
}
func (g *Guard) limited() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if now.Sub(g.window) > time.Second {
		g.window = now
		g.requests = 0
	}
	g.requests++
	return g.requests > 200
}
func permission(r *http.Request) string {
	p := strings.TrimPrefix(r.URL.Path, "/enhance/v1/")
	write := r.Method != "GET" && r.Method != "HEAD"
	category := "core"
	switch {
	case strings.HasPrefix(p, "models"), strings.HasPrefix(p, "registry/"), strings.HasPrefix(p, "model-changes"), strings.HasPrefix(p, "providers"), strings.HasPrefix(p, "accounts"), p == "provider-presets", p == "gateways":
		category = "models"
	case strings.HasPrefix(p, "usage"), strings.HasPrefix(p, "quotas"):
		category = "usage"
	case strings.HasPrefix(p, "tasks"), strings.HasPrefix(p, "sessions"), strings.HasPrefix(p, "attachments"), p == "web-targets":
		category = "tasks"
	case strings.HasPrefix(p, "chat"):
		category = "chat"
	case strings.HasPrefix(p, "plugins"), strings.HasPrefix(p, "plugin-runtime"), strings.HasPrefix(p, "kct/"):
		// Plugin runtime calls carry a plugin identity token in the body and are
		// authorized again in the plugin manager. They still arrive over the core
		// API, so the core guard must classify them as plugin traffic; leaving them
		// unmatched maps them to core:write, which a plugin-scoped pairing does not
		// hold, and every plugin action fails with PERMISSION_DENIED.
		//
		// The KCT handover is classified here too: it is part of the plugin page and
		// gated by the same permission, and without this it would answer 403 to the
		// page that owns the confirm button.
		category = "plugins"
	case strings.HasPrefix(p, "credentials"):
		category = "credentials"
	case strings.HasPrefix(p, "workspace"):
		category = "workspace"
	case strings.HasPrefix(p, "browser"):
		category = "browser"
	case strings.HasPrefix(p, "update"):
		category = "update"
	case p == "settings" && write:
		category = "settings"
	case p == "adapter" && write:
		category = "adapter"
	case p == "desktop/bridge-session":
		return "adapter:write"
	case strings.HasPrefix(p, "desktop/actions/"):
		return nativeActionPermission(strings.TrimPrefix(p, "desktop/actions/"))
	}
	if write {
		return category + ":write"
	}
	return category + ":read"
}
func nativeActionPermission(action string) string {
	switch action {
	case "bootstrap", "events.poll", "config.get", "automation.get", "recovery.get", "diagnostics.get", "contracts.status":
		return "core:read"
	case "accounts.list", "accounts.switchPreflight":
		return "credentials:read"
	case "accounts.saveCurrent", "accounts.beginAdd", "accounts.completeAdd", "accounts.rename", "accounts.delete", "accounts.refreshQuotas", "accounts.switch", "accounts.cancelPending", "accounts.resetVault":
		return "credentials:write"
	case "contracts.report":
		return "adapter:write"
	case "session.view", "session.watch", "session.unwatch":
		return "tasks:read"
	case "managed.usage", "cache.summary", "cache.sessions", "cache.history", "cache.turns", "cache.retention", "cache.receipts", "quota.balances":
		return "usage:read"
	case "config.save", "automation.set", "recovery.set", "attach.reopen":
		return "settings:write"
	case "planner.get", "planner.threads":
		return "chat:read"
	case "update.status":
		return "update:read"
	case "update.check", "update.apply", "update.autoCheck":
		return "update:write"
	default:
		if strings.HasPrefix(action, "planner.") {
			return "chat:write"
		}
		return "tasks:write"
	}
}

func (g *Guard) Pair(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PairingToken string   `json:"pairingToken"`
		Permissions  []string `json:"permissions,omitempty"`
	}
	if !httpx.Decode(w, r, &body) {
		return
	}
	if !equal(body.PairingToken, g.pairing) {
		httpx.Fail(w, 401, "PAIRING_DENIED", "配对凭据无效，请从本程序运行目录获取新的凭据")
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if now.Sub(g.pairWindow) > time.Minute {
		g.pairWindow = now
		g.pairs = 0
	}
	g.pairs++
	if g.pairs > 20 {
		httpx.Fail(w, 429, "RATE_LIMIT", "配对过于频繁，请稍后再试")
		return
	}
	requested := body.Permissions
	if requested == nil {
		requested = Permissions
	}
	allowed := map[string]bool{ElectronMainPermission: true}
	for _, p := range Permissions {
		allowed[p] = true
	}
	scopes := make(map[string]bool)
	for _, p := range requested {
		if !allowed[p] {
			httpx.Fail(w, 422, "INVALID_PERMISSION", "未知资源权限")
			return
		}
		scopes[p] = true
	}
	for token, v := range g.grants {
		if now.After(v.expires) {
			delete(g.grants, token)
		}
	}
	if len(g.grants) >= 64 {
		httpx.Fail(w, 429, "SESSION_LIMIT", "授权会话过多，请重启本增强服务后重新配对")
		return
	}
	token := Random()
	g.grants[token] = grant{scopes, now.Add(12 * time.Hour)}
	httpx.JSON(w, 200, map[string]any{"sessionToken": token, "permissions": requested, "expiresAt": now.Add(12 * time.Hour)})
}
func (g *Guard) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if r.Host != g.host {
			httpx.Fail(w, 403, "HOST_DENIED", "本地服务 Host 不匹配")
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" && origin != "http://"+g.host {
			httpx.Fail(w, 403, "ORIGIN_DENIED", "来源未获授权")
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			httpx.Fail(w, 403, "ORIGIN_DENIED", "跨站请求被拒绝")
			return
		}
		if g.limited() {
			httpx.Fail(w, 429, "RATE_LIMIT", "请求过于频繁，请稍后再试")
			return
		}
		if r.Method == "OPTIONS" {
			httpx.Fail(w, 403, "CORS_DENIED", "不允许跨域访问")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/enhance/v1/") && r.URL.Path != "/enhance/v1/pair" {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				httpx.Fail(w, 401, "AUTH_REQUIRED", "请配对后访问增强服务")
				return
			}
			status, electronMain := g.authorize(strings.TrimPrefix(auth, "Bearer "), permission(r))
			switch status {
			case authExpired:
				httpx.Fail(w, 401, "TOKEN_EXPIRED", "增强核心会话凭据已失效或过期，正在重新连接")
				return
			case authDenied:
				httpx.Fail(w, 403, "PERMISSION_DENIED", "当前凭据没有此资源权限")
				return
			}
			if electronMain {
				r = r.WithContext(context.WithValue(r.Context(), electronMainKey{}, true))
			}
		}
		binaryUpload := r.Method == "POST" && r.URL.Path == "/enhance/v1/attachments" && r.Header.Get("Content-Type") == "application/octet-stream"
		emptyDelete := r.Method == "DELETE" && r.ContentLength == 0
		if r.Method != "GET" && r.Method != "HEAD" && !emptyDelete && !binaryUpload && strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
			httpx.Fail(w, 415, "CONTENT_TYPE", "请使用 application/json")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, httpx.BodyLimit(r))
		next.ServeHTTP(w, r)
	})
}
