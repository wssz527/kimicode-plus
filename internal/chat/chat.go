package chat

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/provider"
	"enhancedkimi/internal/security"
	"enhancedkimi/internal/storage"
)

type Source struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	AdapterStatus string `json:"adapterStatus"`
}
type Message struct {
	ID         string              `json:"id"`
	Role       string              `json:"role"`
	Text       string              `json:"text"`
	At         string              `json:"at"`
	Tool       string              `json:"tool,omitempty"`
	Reasoning  string              `json:"reasoning,omitempty"`
	ToolCalls  []provider.ToolCall `json:"toolCalls,omitempty"`
	ToolCallID string              `json:"toolCallId,omitempty"`
	Failed     bool                `json:"failed,omitempty"`
}
type Thread struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	SourceID    string        `json:"sourceId"`
	Model       string        `json:"model"`
	Messages    []Message     `json:"messages"`
	Mode        string        `json:"mode"`
	ProviderID  string        `json:"providerId,omitempty"`
	ProjectID   string        `json:"projectId,omitempty"`
	LastRequest *RequestState `json:"lastRequest,omitempty"`
}
type Service struct {
	db      *sql.DB
	runtime *Runtime
}

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func New(db *sql.DB) (*Service, error) {
	err := storage.Migrate(db, "chat", []string{`CREATE TABLE chat_sources(id TEXT PRIMARY KEY, data TEXT NOT NULL); CREATE TABLE chat_threads(id TEXT PRIMARY KEY,data TEXT NOT NULL); CREATE TABLE chat_intents(thread_id TEXT NOT NULL,intent TEXT NOT NULL,digest TEXT NOT NULL,PRIMARY KEY(thread_id,intent));`})
	if err != nil {
		return nil, err
	}
	s := &Service{db: db}
	if err = s.initRequests(); err != nil {
		return nil, err
	}
	for _, src := range []Source{{"kimi", "Kimi 官网快速聊天", "https://www.kimi.com/", "URL_ONLY"}, {"chatgpt", "ChatGPT", "https://chatgpt.com/", "URL_ONLY"}, {"gemini", "Gemini", "https://gemini.google.com/", "URL_ONLY"}, {"deepseek", "DeepSeek", "https://chat.deepseek.com/", "URL_ONLY"}, {"custom-model", "自定义模型轻量聊天", "", "PROVIDER"}} {
		b, _ := json.Marshal(src)
		if _, err = db.Exec(`INSERT OR IGNORE INTO chat_sources VALUES(?,?)`, src.ID, string(b)); err != nil {
			return nil, err
		}
	}
	return s, nil
}
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /enhance/v1/chat/sources", s.sources)
	mux.HandleFunc("POST /enhance/v1/chat/sources", s.addSource)
	mux.HandleFunc("GET /enhance/v1/chat/threads", s.threads)
	mux.HandleFunc("POST /enhance/v1/chat/threads", s.create)
	mux.HandleFunc("GET /enhance/v1/chat/threads/{id}", s.get)
	mux.HandleFunc("POST /enhance/v1/chat/threads/{id}/messages", s.message)
	mux.HandleFunc("GET /enhance/v1/chat/threads/{id}/requests", s.listRequests)
	mux.HandleFunc("POST /enhance/v1/chat/threads/{id}/requests/{requestId}/resolve", s.resolveRequest)
}
func (s *Service) sources(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT data FROM chat_sources ORDER BY CASE WHEN id='kimi' THEN 0 ELSE 1 END,id`)
	if err != nil {
		httpx.Fail(w, 500, "STORAGE_ERROR", "无法读取聊天来源")
		return
	}
	defer rows.Close()
	items := []Source{}
	for rows.Next() {
		var b string
		var v Source
		if err = rows.Scan(&b); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(b), &v); err != nil {
			break
		}
		if v.ID == "custom-model" {
			v.Name = "自定义模型轻量聊天"
			v.AdapterStatus = "PROVIDER"
		}
		items = append(items, v)
	}
	if err != nil || rows.Err() != nil {
		httpx.Fail(w, 500, "STORAGE_ERROR", "无法读取聊天来源")
		return
	}
	httpx.JSON(w, 200, map[string]any{"items": items, "total": len(items)})
}
func (s *Service) addSource(w http.ResponseWriter, r *http.Request) {
	var v struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if !httpx.Decode(w, r, &v) {
		return
	}
	u, err := url.Parse(v.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !idPattern.MatchString(v.ID) || strings.TrimSpace(v.Name) == "" || len(v.Name) > 120 {
		httpx.Fail(w, 422, "INVALID_SOURCE", "请输入来源名称和不含凭据或查询参数的 HTTPS 地址")
		return
	}
	src := Source{v.ID, v.Name, u.String(), "URL_ONLY"}
	b, _ := json.Marshal(src)
	if _, err = s.db.Exec(`INSERT INTO chat_sources VALUES(?,?)`, v.ID, string(b)); err != nil {
		httpx.Fail(w, 409, "SOURCE_CONFLICT", "来源 ID 已存在")
		return
	}
	httpx.JSON(w, 201, src)
}
func (s *Service) threads(w http.ResponseWriter, r *http.Request) {
	offset, limit := httpx.Page(r)
	var total int
	if err := s.db.QueryRow(`SELECT count(*) FROM chat_threads`).Scan(&total); err != nil {
		httpx.Fail(w, 500, "STORAGE_ERROR", "无法查询聊天历史")
		return
	}
	rows, err := s.db.Query(`SELECT data FROM chat_threads ORDER BY rowid DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		httpx.Fail(w, 500, "STORAGE_ERROR", "无法查询聊天历史")
		return
	}
	defer rows.Close()
	items := []Thread{}
	for rows.Next() {
		var b string
		var t Thread
		if err = rows.Scan(&b); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(b), &t); err != nil {
			break
		}
		items = append(items, t)
	}
	if err != nil || rows.Err() != nil {
		httpx.Fail(w, 500, "STORAGE_ERROR", "无法查询聊天历史")
		return
	}
	httpx.JSON(w, 200, map[string]any{"items": items, "total": total})
}
func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Title      string `json:"title"`
		SourceID   string `json:"sourceId"`
		Model      string `json:"model"`
		ProviderID string `json:"providerId"`
		ProjectID  string `json:"projectId"`
	}
	if !httpx.Decode(w, r, &v) {
		return
	}
	if strings.TrimSpace(v.Title) == "" || len(v.Title) > 200 || len(v.Model) > 200 || len(v.ProjectID) > 200 {
		httpx.Fail(w, 422, "INVALID_THREAD", "请输入有效标题与模型")
		return
	}
	var exists int
	if err := s.db.QueryRow(`SELECT count(*) FROM chat_sources WHERE id=?`, v.SourceID).Scan(&exists); err != nil || exists == 0 {
		httpx.Fail(w, 422, "INVALID_SOURCE", "请先添加聊天来源")
		return
	}
	mode := "MOCK"
	if v.ProviderID != "" {
		if s.runtime == nil || v.SourceID != "custom-model" || strings.TrimSpace(v.Model) == "" {
			httpx.Fail(w, 422, "INVALID_PROVIDER", "请选择轻量聊天的供应商和模型")
			return
		}
		if _, err := s.runtime.client.Config(r.Context(), v.ProviderID); err != nil {
			httpx.Fail(w, 422, "INVALID_PROVIDER", "供应商不存在")
			return
		}
		mode = "PROVIDER"
	}
	t := Thread{ID: security.Random()[:24], Title: v.Title, SourceID: v.SourceID, Model: v.Model, Messages: []Message{}, Mode: mode, ProviderID: v.ProviderID, ProjectID: v.ProjectID}
	b, _ := json.Marshal(t)
	if _, err := s.db.Exec(`INSERT INTO chat_threads VALUES(?,?)`, t.ID, string(b)); err != nil {
		httpx.Fail(w, 500, "STORAGE_ERROR", "无法保存聊天")
		return
	}
	httpx.JSON(w, 201, t)
}
func (s *Service) load(id string) (Thread, error) {
	var raw string
	var t Thread
	err := s.db.QueryRow(`SELECT data FROM chat_threads WHERE id=?`, id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &t)
	}
	return t, err
}
func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	t, err := s.load(r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		httpx.Fail(w, 404, "THREAD_NOT_FOUND", "聊天不存在")
		return
	}
	if err != nil {
		httpx.Fail(w, 500, "STORAGE_ERROR", "无法读取聊天")
		return
	}
	s.refreshProviderUsage(&t)
	httpx.JSON(w, 200, t)
}
func (s *Service) message(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Text string `json:"text"`
	}
	if !httpx.Decode(w, r, &v) {
		return
	}
	if strings.TrimSpace(v.Text) == "" || len(v.Text) > 32000 {
		httpx.Fail(w, 422, "INVALID_MESSAGE", "消息为空或超过 32000 字节")
		return
	}
	id := r.PathValue("id")
	intent := r.Header.Get("Idempotency-Key")
	if intent == "" {
		intent = security.Random()
	}
	if len(intent) > 200 {
		httpx.Fail(w, 422, "INVALID_INTENT", "幂等键过长")
		return
	}
	digest := sha256.Sum256([]byte(v.Text))
	hash := hex.EncodeToString(digest[:])
	t, err := s.load(id)
	if err != nil {
		httpx.Fail(w, 404, "THREAD_NOT_FOUND", "聊天不存在")
		return
	}
	if t.Mode == "PROVIDER" {
		s.sendProvider(w, r, id, intent, hash, v.Text)
		return
	}
	err = storage.Transaction(r.Context(), s.db, func(tx *sql.Tx) error {
		var b string
		if err := tx.QueryRow(`SELECT data FROM chat_threads WHERE id=?`, id).Scan(&b); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(b), &t); err != nil {
			return err
		}
		var previous string
		err := tx.QueryRow(`SELECT digest FROM chat_intents WHERE thread_id=? AND intent=?`, id, intent).Scan(&previous)
		if err == nil {
			if previous != hash {
				return errors.New("intent conflict")
			}
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		at := time.Now().UTC().Format(time.RFC3339Nano)
		t.Messages = append(t.Messages, Message{ID: security.Random()[:16], Role: "user", Text: v.Text, At: at})
		count := min(12, len(t.Messages))
		reply := "模拟回复：已保存在本地独立聊天中。当前精简上下文包含最近 " + strconv.Itoa(count) + " 条消息。可以选择消息预览后交接至 Code；本阶段未调用任何模型。"
		t.Messages = append(t.Messages, Message{ID: security.Random()[:16], Role: "assistant", Text: reply, At: at})
		b2, _ := json.Marshal(t)
		if _, err = tx.Exec(`UPDATE chat_threads SET data=? WHERE id=?`, string(b2), id); err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO chat_intents VALUES(?,?,?)`, id, intent, hash)
		return err
	})
	if err != nil {
		if err.Error() == "intent conflict" {
			httpx.Fail(w, 409, "INTENT_CONFLICT", "相同幂等键对应不同消息")
		} else {
			httpx.Fail(w, 500, "STORAGE_ERROR", "无法保存消息")
		}
		return
	}
	httpx.JSON(w, 200, t)
}
