package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	"enhancedkimi/internal/httpx"
)

var Permissions = []string{"core:read", "settings:write", "models:read", "models:write", "usage:read", "usage:write", "tasks:read", "tasks:write", "chat:read", "chat:write", "plugins:read", "plugins:write", "adapter:write", "credentials:read", "credentials:write", "workspace:read", "workspace:write", "browser:read", "browser:write"}

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
func equal(a, b string) bool          { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func (g *Guard) permitted(token, permission string) bool {
	if equal(token, g.pairing) {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	v, ok := g.grants[token]
	return ok && time.Now().Before(v.expires) && v.permissions[permission]
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
	case strings.HasPrefix(p, "plugins"), strings.HasPrefix(p, "plugin-runtime"):
		// Plugin runtime calls carry a plugin identity token in the body and are
		// authorized again in the plugin manager. They still arrive over the core
		// API, so the core guard must classify them as plugin traffic; leaving them
		// unmatched maps them to core:write, which a plugin-scoped pairing does not
		// hold, and every plugin action fails with PERMISSION_DENIED.
		category = "plugins"
	case strings.HasPrefix(p, "credentials"):
		category = "credentials"
	case strings.HasPrefix(p, "workspace"):
		category = "workspace"
	case strings.HasPrefix(p, "browser"):
		category = "browser"
	case p == "settings" && write:
		category = "settings"
	case p == "adapter" && write:
		category = "adapter"
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
	case "bootstrap", "events.poll", "config.get", "automation.get", "recovery.get", "diagnostics.get":
		return "core:read"
	case "session.view", "session.watch", "session.unwatch":
		return "tasks:read"
	case "managed.usage", "cache.summary", "cache.sessions", "cache.history", "cache.retention", "cache.receipts":
		return "usage:read"
	case "config.save", "automation.set", "recovery.set":
		return "settings:write"
	case "planner.get", "planner.threads":
		return "chat:read"
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
	allowed := make(map[string]bool)
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
			if !g.permitted(strings.TrimPrefix(auth, "Bearer "), permission(r)) {
				httpx.Fail(w, 403, "PERMISSION_DENIED", "凭据无效、已过期或没有此资源权限")
				return
			}
		}
		binaryUpload := r.Method == "POST" && r.URL.Path == "/enhance/v1/attachments" && r.Header.Get("Content-Type") == "application/octet-stream"
		if r.Method != "GET" && r.Method != "HEAD" && !binaryUpload && strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
			httpx.Fail(w, 415, "CONTENT_TYPE", "请使用 application/json")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, httpx.BodyLimit(r))
		next.ServeHTTP(w, r)
	})
}
