package future

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"enhancedkimi/internal/config"
	"enhancedkimi/internal/credentials"
	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/security"
	"enhancedkimi/internal/storage"
)

type Cookie struct {
	Domain       string   `json:"domain"`
	Path         string   `json:"path"`
	Name         string   `json:"name"`
	Value        string   `json:"value"`
	PartitionKey string   `json:"partitionKey"`
	HostOnly     bool     `json:"hostOnly"`
	Secure       bool     `json:"secure"`
	HTTPOnly     bool     `json:"httpOnly"`
	SameSite     string   `json:"sameSite"`
	Expires      *float64 `json:"expires"`
	StoreID      string   `json:"storeId,omitempty"`
}
type Migration struct {
	ID        string   `json:"id"`
	Browser   string   `json:"browser"`
	Target    string   `json:"target"`
	Mode      string   `json:"mode"`
	Sites     []string `json:"sites"`
	Count     int      `json:"count"`
	Conflicts []string `json:"conflicts"`
	Skipped   int      `json:"skipped"`
	State     string   `json:"state"`
	At        string   `json:"at"`
}

func (m Migration) expired() bool {
	at, err := time.Parse(time.RFC3339Nano, m.At)
	return err != nil || !time.Now().Before(at.Add(credentials.BrowserPreviewTTL))
}

type BrowserStore struct {
	db       *sql.DB
	vault    *credentials.BrowserVault
	dispatch func(func() error) error
	mu       sync.Mutex
}

func NewBrowserStore(db *sql.DB, vault *credentials.BrowserVault, dispatch func(func() error) error) (*BrowserStore, error) {
	err := storage.Migrate(db, "browser_store", []string{
		`CREATE TABLE browser_migrations(id TEXT PRIMARY KEY,data TEXT NOT NULL); CREATE TABLE browser_permissions(origin TEXT PRIMARY KEY,data TEXT NOT NULL);`,
		`INSERT INTO vault_browser_entries(ref,value,expires_at)
		 SELECT e.ref,e.value,CASE WHEN e.ref='migration/'||m.id THEN COALESCE(unixepoch(json_extract(m.data,'$.at')),unixepoch())+86400 ELSE 0 END
		 FROM vault_entries e JOIN browser_migrations m ON e.ref='migration/'||m.id OR e.ref='browser/'||json_extract(m.data,'$.target') GROUP BY e.ref;
		 DELETE FROM vault_entries WHERE ref IN (SELECT ref FROM vault_browser_entries);`,
	})
	if err != nil {
		return nil, err
	}
	if err = vault.CollectExpired(context.Background()); err != nil {
		return nil, err
	}
	return &BrowserStore{db: db, vault: vault, dispatch: dispatch}, nil
}
func cookieKey(c Cookie, target string) string {
	return (CookieIdentity{Domain: c.Domain, Path: c.Path, Name: c.Name, PartitionKey: c.PartitionKey, TargetPartition: target, HostOnly: c.HostOnly}).ConflictKey()
}
func (b *BrowserStore) cookies(r *http.Request, target string) ([]Cookie, error) {
	raw, err := b.vault.Get(r.Context(), "browser/"+target)
	if errors.Is(err, credentials.ErrNotFound) {
		return []Cookie{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	var values []Cookie
	err = json.Unmarshal(raw, &values)
	return values, err
}
func browserVaultFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, credentials.ErrLocked):
		httpx.Fail(w, 423, "VAULT_LOCKED", "请先解锁增强凭据库")
	case errors.Is(err, credentials.ErrTooLarge):
		httpx.Fail(w, 413, "BROWSER_STORE_FULL", "隔离数据超过 8 MiB，请减少导入内容或使用其他隔离目标")
	case errors.Is(err, credentials.ErrNotFound):
		httpx.Fail(w, 410, "MIGRATION_EXPIRED", "预览已过期或已清理，请重新导入并预览")
	default:
		httpx.Fail(w, 500, "BROWSER_STORAGE", "无法读取或保存隔离数据，请稍后重试")
	}
}
func (b *BrowserStore) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /enhance/v1/browser/migrations", func(w http.ResponseWriter, r *http.Request) {
		if err := b.vault.CollectExpired(r.Context()); err != nil {
			browserVaultFailure(w, err)
			return
		}
		rows, err := b.db.QueryContext(r.Context(), `SELECT data FROM browser_migrations ORDER BY rowid DESC LIMIT 100`)
		if err != nil {
			httpx.Fail(w, 500, "BROWSER_STORAGE", "无法读取迁移记录")
			return
		}
		defer rows.Close()
		items := []Migration{}
		for rows.Next() {
			var raw string
			var v Migration
			if err = rows.Scan(&raw); err != nil {
				break
			}
			if err = json.Unmarshal([]byte(raw), &v); err != nil {
				break
			}
			if v.State == "PREVIEW" && v.expired() {
				v.State = "EXPIRED"
			}
			items = append(items, v)
		}
		if err != nil || rows.Err() != nil {
			httpx.Fail(w, 500, "BROWSER_STORAGE", "无法读取迁移记录")
			return
		}
		httpx.JSON(w, 200, map[string]any{"items": items, "nativeConnected": false, "destination": "ENCRYPTED_ISOLATED_STORE"})
	})
	mux.HandleFunc("POST /enhance/v1/browser/migrations/preview", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Browser string   `json:"browser"`
			Target  string   `json:"target"`
			Mode    string   `json:"mode"`
			Sites   []string `json:"sites"`
			Cookies []Cookie `json:"cookies"`
			Consent bool     `json:"consent"`
		}
		if !httpx.Decode(w, r, &in) {
			return
		}
		if !in.Consent || !localID.MatchString(in.Target) || (in.Browser != "chrome" && in.Browser != "edge" && in.Browser != "export") || (in.Mode != "SITES" && in.Mode != "ALL") || len(in.Cookies) > 1000 {
			httpx.Fail(w, 422, "INVALID_MIGRATION", "需要明确同意，选择浏览器、隔离目标和迁移范围")
			return
		}
		if in.Mode == "SITES" && len(in.Sites) == 0 {
			httpx.Fail(w, 422, "INVALID_SITES", "请指定网站域名")
			return
		}
		for i, site := range in.Sites {
			site = strings.ToLower(strings.TrimSpace(site))
			if strings.ContainsAny(site, "/:@?# ") || !strings.Contains(site, ".") {
				httpx.Fail(w, 422, "INVALID_SITES", "网站请填写域名")
				return
			}
			in.Sites[i] = site
		}
		if err := b.vault.CollectExpired(r.Context()); err != nil {
			browserVaultFailure(w, err)
			return
		}
		existing, err := b.cookies(r, in.Target)
		if err != nil {
			browserVaultFailure(w, err)
			return
		}
		keys := map[string]bool{}
		for _, c := range existing {
			keys[cookieKey(c, in.Target)] = true
		}
		p := Migration{ID: security.Random()[:24], Browser: in.Browser, Target: in.Target, Mode: in.Mode, Sites: in.Sites, Conflicts: []string{}, State: "PREVIEW", At: time.Now().UTC().Format(time.RFC3339Nano)}
		selected := []Cookie{}
		seen := map[string]bool{}
		for _, c := range in.Cookies {
			domain := strings.ToLower(strings.TrimPrefix(c.Domain, "."))
			matched := in.Mode == "ALL"
			for _, site := range in.Sites {
				if domain == site || strings.HasSuffix(domain, "."+site) {
					matched = true
				}
			}
			if !matched {
				p.Skipped++
				continue
			}
			if domain == "" || strings.ContainsAny(domain, "/:@?# \r\n") || c.Name == "" || len(c.Name) > 256 || !strings.HasPrefix(c.Path, "/") || strings.ContainsAny(c.Name+c.Path, "\r\n\x00") {
				httpx.Fail(w, 422, "INVALID_COOKIE", "导出记录包含无效域名、名称或路径")
				return
			}
			if c.Expires != nil && *c.Expires > 0 && *c.Expires < float64(time.Now().Unix()) {
				p.Skipped++
				continue
			}
			key := cookieKey(c, in.Target)
			if seen[key] {
				httpx.Fail(w, 422, "DUPLICATE_COOKIE", "导出文件含重复目标 Cookie，请先合并")
				return
			}
			seen[key] = true
			if keys[key] {
				p.Conflicts = append(p.Conflicts, c.Domain+c.Path+" · "+c.Name)
			}
			c.StoreID = ""
			selected = append(selected, c)
		}
		p.Count = len(selected)
		raw, _ := json.Marshal(selected)
		defer clear(raw)
		if err = b.vault.Put(r.Context(), "migration/"+p.ID, raw); err != nil {
			browserVaultFailure(w, err)
			return
		}
		data, _ := json.Marshal(p)
		if _, err = b.db.ExecContext(r.Context(), `INSERT INTO browser_migrations VALUES(?,?)`, p.ID, string(data)); err != nil {
			b.vault.Delete(r.Context(), "migration/"+p.ID)
			httpx.Fail(w, 500, "BROWSER_STORAGE", "无法保存预览")
			return
		}
		httpx.JSON(w, 200, p)
	})
	mux.HandleFunc("POST /enhance/v1/browser/migrations/{id}/apply", func(w http.ResponseWriter, r *http.Request) {
		if err := b.dispatch(func() error {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.apply(w, r)
			return nil
		}); err != nil {
			if errors.Is(err, config.ErrSafeMode) {
				httpx.Fail(w, 409, "SAFE_MODE", "安全模式禁止迁移写入")
			} else {
				httpx.Fail(w, 500, "BROWSER_STORAGE", "迁移许可不可用，请稍后重试")
			}
		}
	})
	mux.HandleFunc("GET /enhance/v1/browser/permissions", func(w http.ResponseWriter, r *http.Request) {
		rows, err := b.db.Query(`SELECT origin,data FROM browser_permissions ORDER BY origin`)
		if err != nil {
			httpx.Fail(w, 500, "BROWSER_STORAGE", "读取权限失败")
			return
		}
		defer rows.Close()
		items := []any{}
		for rows.Next() {
			var origin, raw string
			var p BrowserPermissions
			if rows.Scan(&origin, &raw) != nil || json.Unmarshal([]byte(raw), &p) != nil {
				httpx.Fail(w, 500, "BROWSER_STORAGE", "读取权限失败")
				return
			}
			items = append(items, map[string]any{"origin": origin, "permissions": p})
		}
		httpx.JSON(w, 200, map[string]any{"items": items})
	})
	mux.HandleFunc("PUT /enhance/v1/browser/permissions", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Origin      string             `json:"origin"`
			Permissions BrowserPermissions `json:"permissions"`
		}
		if !httpx.Decode(w, r, &in) {
			return
		}
		u, err := url.Parse(in.Origin)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			httpx.Fail(w, 422, "INVALID_ORIGIN", "请填写 HTTPS 来源，例如 https://www.kimi.com")
			return
		}
		data, _ := json.Marshal(in.Permissions)
		if _, err = b.db.Exec(`INSERT INTO browser_permissions VALUES(?,?) ON CONFLICT(origin) DO UPDATE SET data=excluded.data`, u.String(), string(data)); err != nil {
			httpx.Fail(w, 500, "BROWSER_STORAGE", "保存权限失败")
			return
		}
		httpx.JSON(w, 200, in)
	})
}

func (b *BrowserStore) apply(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Conflict string `json:"conflict"`
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	if in.Conflict != "KEEP" && in.Conflict != "REPLACE" {
		httpx.Fail(w, 422, "INVALID_CONFLICT", "请选择保留现有或替换")
		return
	}
	if err := b.vault.CollectExpired(r.Context()); err != nil {
		browserVaultFailure(w, err)
		return
	}
	var raw string
	var p Migration
	if err := b.db.QueryRowContext(r.Context(), `SELECT data FROM browser_migrations WHERE id=?`, r.PathValue("id")).Scan(&raw); err != nil || json.Unmarshal([]byte(raw), &p) != nil {
		httpx.Fail(w, 404, "MIGRATION_NOT_FOUND", "迁移预览不存在")
		return
	}
	if p.State == "STORED_ISOLATED" {
		httpx.JSON(w, 200, p)
		return
	}
	if p.expired() {
		httpx.Fail(w, 410, "MIGRATION_EXPIRED", "预览已过期，请重新导入并预览")
		return
	}
	data, err := b.vault.Get(r.Context(), "migration/"+p.ID)
	if err != nil {
		browserVaultFailure(w, err)
		return
	}
	defer clear(data)
	var incoming []Cookie
	if json.Unmarshal(data, &incoming) != nil {
		httpx.Fail(w, 422, "INVALID_MIGRATION", "加密预览无效")
		return
	}
	existing, err := b.cookies(r, p.Target)
	if err != nil {
		browserVaultFailure(w, err)
		return
	}
	index := map[string]int{}
	for i, c := range existing {
		index[cookieKey(c, p.Target)] = i
	}
	for _, c := range incoming {
		key := cookieKey(c, p.Target)
		if i, ok := index[key]; ok {
			if in.Conflict == "REPLACE" {
				existing[i] = c
			}
		} else {
			index[key] = len(existing)
			existing = append(existing, c)
		}
	}
	encoded, _ := json.Marshal(existing)
	defer clear(encoded)
	if err = b.vault.Put(r.Context(), "browser/"+p.Target, encoded); err != nil {
		browserVaultFailure(w, err)
		return
	}
	p.State = "STORED_ISOLATED"
	updated, _ := json.Marshal(p)
	if _, err = b.db.ExecContext(r.Context(), `UPDATE browser_migrations SET data=? WHERE id=?`, string(updated), p.ID); err != nil {
		httpx.Fail(w, 500, "BROWSER_STORAGE", "数据已保存，记录更新失败，请核对")
		return
	}
	_ = b.vault.Delete(r.Context(), "migration/"+p.ID)
	httpx.JSON(w, 200, p)
}
