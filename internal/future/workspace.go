package future

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"enhancedkimi/internal/config"
	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/storage"
)

type Session struct {
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	ProjectID       string    `json:"projectId"`
	ModelID         string    `json:"modelId"`
	Worktree        string    `json:"worktree"`
	HostID          string    `json:"hostId"`
	LastActivity    time.Time `json:"lastActivity"`
	MainBusy        bool      `json:"mainBusy"`
	ToolBusy        bool      `json:"toolBusy"`
	ApprovalPending bool      `json:"approvalPending"`
	Recovering      bool      `json:"recovering"`
	WorkerRunning   bool      `json:"workerRunning"`
	Revision        int       `json:"revision"`
}
type Policy struct {
	ID          string `json:"id"`
	Scope       string `json:"scope"`
	Target      string `json:"target"`
	Enabled     bool   `json:"enabled"`
	IdleMinutes int    `json:"idleMinutes"`
}
type Layout struct {
	Columns   int             `json:"columns"`
	Panes     []PaneBinding   `json:"panes"`
	Reviewers []ReviewerRoute `json:"reviewers"`
}
type Evaluation struct {
	Session Session `json:"session"`
	Policy  Policy  `json:"policy"`
	Due     bool    `json:"due"`
	State   string  `json:"state"`
	Reason  string  `json:"reason"`
}
type Workspace struct {
	db       *sql.DB
	safe     func() bool
	dispatch func(func() error) error
}

var localID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$`)

func NewWorkspace(db *sql.DB, safe func() bool, dispatch func(func() error) error) (*Workspace, error) {
	err := storage.Migrate(db, "workspace", []string{`CREATE TABLE workspace_sessions(id TEXT PRIMARY KEY,revision INTEGER NOT NULL,data TEXT NOT NULL); CREATE TABLE compaction_policies(id TEXT PRIMARY KEY,data TEXT NOT NULL); CREATE TABLE workspace_layout(id INTEGER PRIMARY KEY CHECK(id=1),data TEXT NOT NULL);`})
	if err != nil {
		return nil, err
	}
	return &Workspace{db: db, safe: safe, dispatch: dispatch}, nil
}
func (w *Workspace) Sessions() ([]Session, error) {
	rows, err := w.db.Query(`SELECT data FROM workspace_sessions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Session{}
	for rows.Next() {
		var raw string
		var item Session
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (w *Workspace) policies() ([]Policy, error) {
	rows, err := w.db.Query(`SELECT data FROM compaction_policies ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Policy{}
	for rows.Next() {
		var raw string
		var item Policy
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (w *Workspace) evaluate(sessions []Session, policies []Policy) []Evaluation {
	out := []Evaluation{}
	priority := map[string]int{"global": 0, "model": 1, "project": 2, "session": 3}
	for _, s := range sessions {
		p := Policy{ID: "default", Scope: "global", Target: "*", IdleMinutes: 25}
		best := -1
		for _, candidate := range policies {
			matches := candidate.Scope == "global" || candidate.Scope == "model" && candidate.Target == s.ModelID || candidate.Scope == "project" && candidate.Target == s.ProjectID || candidate.Scope == "session" && candidate.Target == s.ID
			if matches && priority[candidate.Scope] > best {
				best = priority[candidate.Scope]
				p = candidate
			}
		}
		state := SessionState{LastActivity: s.LastActivity, MainBusy: s.MainBusy, ToolBusy: s.ToolBusy, ApprovalPending: s.ApprovalPending, Recovering: s.Recovering, WorkerRunning: s.WorkerRunning}
		state.Revision = "local-reference"
		due := !s.LastActivity.IsZero() && !s.LastActivity.After(time.Now()) && (CompactionPolicy{Idle: time.Duration(p.IdleMinutes) * time.Minute, Enabled: p.Enabled, NativeVerified: true}).Eligible(time.Now(), state)
		reason := "原生压缩接口未连接，策略仅评估"
		if !p.Enabled {
			reason = "策略已关闭"
		} else if s.WorkerRunning {
			reason = "Worker 正在运行，保留 Tower 会话"
		} else if !due {
			reason = "未达空闲时间或会话仍忙"
		}
		status := "WAITING"
		if due {
			status = "BLOCKED_NATIVE_UNCONNECTED"
		}
		if w.safe() {
			status = "SAFE_MODE"
			reason = "安全模式禁止执行"
		}
		out = append(out, Evaluation{s, p, due, status, reason})
	}
	return out
}
func (w *Workspace) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /enhance/v1/workspace/sessions", func(out http.ResponseWriter, r *http.Request) {
		items, err := w.Sessions()
		if err != nil {
			httpx.Fail(out, 500, "WORKSPACE_STORAGE", "无法读取会话引用")
			return
		}
		httpx.JSON(out, 200, map[string]any{"items": items, "nativeConnected": false})
	})
	mux.HandleFunc("PUT /enhance/v1/workspace/sessions/{id}", func(out http.ResponseWriter, r *http.Request) {
		var v Session
		if !httpx.Decode(out, r, &v) {
			return
		}
		v.ID = r.PathValue("id")
		if !localID.MatchString(v.ID) || v.Title == "" || len(v.Title) > 200 || len(v.ProjectID) > 200 || len(v.ModelID) > 200 || len(v.Worktree) > 2000 || v.Revision < 0 {
			httpx.Fail(out, 422, "INVALID_SESSION", "请填写有效的会话引用与版本")
			return
		}
		err := storage.Transaction(r.Context(), w.db, func(tx *sql.Tx) error {
			var revision int
			e := tx.QueryRow(`SELECT revision FROM workspace_sessions WHERE id=?`, v.ID).Scan(&revision)
			if e != nil && e != sql.ErrNoRows {
				return e
			}
			if revision != v.Revision {
				return errors.New("revision conflict")
			}
			v.Revision++
			data, e := json.Marshal(v)
			if e != nil {
				return e
			}
			_, e = tx.Exec(`INSERT INTO workspace_sessions VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,data=excluded.data`, v.ID, v.Revision, string(data))
			return e
		})
		if err != nil {
			httpx.Fail(out, 409, "SESSION_CONFLICT", "引用已变化，请刷新后保存")
			return
		}
		httpx.JSON(out, 200, v)
	})
	mux.HandleFunc("GET /enhance/v1/workspace/compaction", func(out http.ResponseWriter, r *http.Request) {
		sessions, err := w.Sessions()
		if err != nil {
			httpx.Fail(out, 500, "WORKSPACE_STORAGE", "读取失败")
			return
		}
		policies, err := w.policies()
		if err != nil {
			httpx.Fail(out, 500, "WORKSPACE_STORAGE", "读取失败")
			return
		}
		httpx.JSON(out, 200, map[string]any{"policies": policies, "evaluations": w.evaluate(sessions, policies), "nativeConnected": false, "defaultIdleMinutes": 25})
	})
	mux.HandleFunc("PUT /enhance/v1/workspace/compaction/{id}", func(out http.ResponseWriter, r *http.Request) {
		var p Policy
		if !httpx.Decode(out, r, &p) {
			return
		}
		p.ID = r.PathValue("id")
		if !localID.MatchString(p.ID) || p.IdleMinutes < 1 || p.IdleMinutes > 1440 || p.Target == "" || !(p.Scope == "global" || p.Scope == "model" || p.Scope == "project" || p.Scope == "session") {
			httpx.Fail(out, 422, "INVALID_POLICY", "空闲时间须为 1–1440 分钟，选择有效作用域")
			return
		}
		data, _ := json.Marshal(p)
		if _, err := w.db.Exec(`INSERT INTO compaction_policies VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, p.ID, string(data)); err != nil {
			httpx.Fail(out, 500, "WORKSPACE_STORAGE", "保存失败")
			return
		}
		httpx.JSON(out, 200, p)
	})
	mux.HandleFunc("GET /enhance/v1/workspace/layout", func(out http.ResponseWriter, r *http.Request) {
		v := Layout{Columns: 2, Panes: []PaneBinding{}, Reviewers: []ReviewerRoute{}}
		var raw string
		err := w.db.QueryRow(`SELECT data FROM workspace_layout WHERE id=1`).Scan(&raw)
		if err == nil {
			err = json.Unmarshal([]byte(raw), &v)
		}
		if err != nil && err != sql.ErrNoRows {
			httpx.Fail(out, 500, "WORKSPACE_STORAGE", "读取布局失败")
			return
		}
		httpx.JSON(out, 200, v)
	})
	mux.HandleFunc("PUT /enhance/v1/workspace/layout", func(out http.ResponseWriter, r *http.Request) {
		var v Layout
		if !httpx.Decode(out, r, &v) {
			return
		}
		if v.Columns < 1 || v.Columns > 4 || len(v.Panes) > 12 || len(v.Reviewers) > 8 {
			httpx.Fail(out, 422, "INVALID_LAYOUT", "支持 1–4 列、最多 12 个引用和 8 条路由")
			return
		}
		for i := range v.Reviewers {
			if v.Reviewers[i].ModelID == "" || v.Reviewers[i].Role == "" {
				httpx.Fail(out, 422, "INVALID_ROUTE", "审查路由需要角色和模型")
				return
			}
			v.Reviewers[i].Enforcement = "LOCAL_PREFERENCE_ONLY"
		}
		data, _ := json.Marshal(v)
		if _, err := w.db.Exec(`INSERT INTO workspace_layout VALUES(1,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, string(data)); err != nil {
			httpx.Fail(out, 500, "WORKSPACE_STORAGE", "保存失败")
			return
		}
		httpx.JSON(out, 200, v)
	})
	mux.HandleFunc("POST /enhance/v1/workspace/worktrees", func(out http.ResponseWriter, r *http.Request) {
		var v struct {
			Repository string `json:"repository"`
			Name       string `json:"name"`
			Ref        string `json:"ref"`
		}
		if !httpx.Decode(out, r, &v) {
			return
		}
		if !filepath.IsAbs(v.Repository) || !localID.MatchString(v.Name) || v.Ref == "" || strings.HasPrefix(v.Ref, "-") || strings.ContainsAny(v.Ref, "\r\n\x00") {
			httpx.Fail(out, 422, "INVALID_WORKTREE", "请输入仓库绝对路径、名称和 Git 引用")
			return
		}
		target := filepath.Join(v.Repository, ".ekd-worktrees", v.Name)
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "git", "-C", v.Repository, "worktree", "add", "--detach", target, v.Ref)
		if err := w.dispatch(command.Run); err != nil {
			if errors.Is(err, config.ErrSafeMode) {
				httpx.Fail(out, 409, "SAFE_MODE", "安全模式禁止创建 Worktree")
			} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				httpx.Fail(out, 504, "WORKTREE_TIMEOUT", "创建 Worktree 超时，请核对仓库状态后再操作")
			} else {
				httpx.Fail(out, 422, "WORKTREE_FAILED", "Git 未创建 Worktree，请核对仓库、引用及目标目录")
			}
			return
		}
		httpx.JSON(out, 201, map[string]string{"path": target, "state": "CREATED", "ownership": "ENHANCEMENT_MANAGED"})
	})
}
