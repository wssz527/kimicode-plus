package desktop

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"enhancedkimi/internal/cache"
	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/storage"
)

type PlannerActions interface {
	PlannerAction(context.Context, string, json.RawMessage) (any, error)
	PlannerThreads(context.Context) (any, error)
	PlannerModels(context.Context) (any, error)
}

// AddonFeatureState are the optional surfaces kept out of the core product.
// Both default to false: an unset or unreadable row never enables an addon.
type AddonFeatureState struct {
	Planner      bool `json:"planner"`
	MultiSession bool `json:"multiSession"`
}

type cachedSession struct {
	session      NativeSession
	turns        []NativeTurn
	goal         json.RawMessage
	goalKnown    bool
	revision     uint64
	lastActivity time.Time
}

type NativeService struct {
	db     *sql.DB
	kap    *KAPClient
	bus    *EventBus
	cwd    string
	safe   func() bool
	ctx    context.Context
	cancel context.CancelFunc

	mu           sync.Mutex
	automationMu sync.Mutex
	featureMu    sync.Mutex
	cacheMu      sync.Mutex
	wg           sync.WaitGroup
	cache        map[string]cachedSession
	// store is the session-record metrics layer. It is created lazily because the
	// official records directory may not exist yet.
	store    *cache.Store
	cacheErr error
	// verifyWindow / verifyPoll override the keepalive closed-loop timings. Tests
	// shorten them; production leaves them zero so the defaults apply.
	verifyWindow time.Duration
	verifyPoll   time.Duration
	planner      PlannerActions
	attach       AttachConfig
}

const addonFeaturesKey = "features"

func (s *NativeService) addonFeatures(ctx context.Context) (AddonFeatureState, error) {
	state := AddonFeatureState{}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM native_state WHERE key=?`, addonFeaturesKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err = json.Unmarshal([]byte(raw), &state); err != nil {
		return AddonFeatureState{}, errors.New("附加功能设置无法读取，请重新保存")
	}
	return state, nil
}

func (s *NativeService) storeAddonFeatures(ctx context.Context, state AddonFeatureState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO native_state(key,data) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET data=excluded.data`, addonFeaturesKey, string(data))
	return err
}

func (s *NativeService) setAddonFeatures(ctx context.Context, payload json.RawMessage) (AddonFeatureState, error) {
	s.featureMu.Lock()
	defer s.featureMu.Unlock()
	state, err := s.addonFeatures(ctx)
	if err != nil {
		return state, err
	}
	previous := state
	if err = json.Unmarshal(payload, &state); err != nil {
		return previous, errors.New("附加功能设置格式无效")
	}
	if err = s.storeAddonFeatures(ctx, state); err != nil {
		return previous, err
	}
	if state != previous {
		s.bus.Publish("features", state)
	}
	return state, nil
}

func NewNativeService(db *sql.DB, kap *KAPClient, cwd string, safe func() bool) (*NativeService, error) {
	if err := storage.Migrate(db, "native_service", []string{
		`CREATE TABLE native_state(key TEXT PRIMARY KEY,data TEXT NOT NULL)`,
		`CREATE TABLE automation_intents(id TEXT PRIMARY KEY,kind TEXT NOT NULL,host_id TEXT NOT NULL,session_id TEXT NOT NULL,model_id TEXT NOT NULL,context_revision TEXT NOT NULL,state TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,result TEXT NOT NULL DEFAULT '')`,
	}); err != nil {
		return nil, err
	}
	// automation_intents timestamps are ordered as text (the cooldown, supersede and
	// restart queries all compare them), so legacy variable-width rows are rewritten.
	if err := storage.BackfillSortableTimes(context.Background(), db, []storage.TimeColumn{
		{Table: "automation_intents", Column: "created_at"},
		{Table: "automation_intents", Column: "updated_at"},
	}); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &NativeService{db: db, kap: kap, bus: NewEventBus(), cwd: cwd, safe: safe, ctx: ctx, cancel: cancel, cache: map[string]cachedSession{}}
	if _, err := db.ExecContext(ctx, `UPDATE automation_intents SET state='unknown',result='核心重启后无法确认原请求结果，不会自动重发',updated_at=? WHERE state IN ('pending','acknowledged')`, storage.SortableTime(time.Now().UTC())); err != nil {
		cancel()
		return nil, err
	}
	go kap.RunEvents(ctx, service.applyFrame)
	service.wg.Add(1)
	go func() {
		defer service.wg.Done()
		service.runAutomation()
	}()
	return service, nil
}

func (s *NativeService) Close() {
	s.cancel()
	s.wg.Wait()
	s.kap.mu.Lock()
	if s.kap.socket != nil {
		_ = s.kap.socket.Close(1000, "shutdown")
		s.kap.socket = nil
	}
	s.kap.mu.Unlock()
}

func (s *NativeService) UsePlanner(planner PlannerActions) {
	if aware, ok := planner.(interface {
		UseWorkspace(func(context.Context, string) (string, error))
	}); ok {
		aware.UseWorkspace(s.plannerWorkspace)
	}
	s.mu.Lock()
	s.planner = planner
	s.mu.Unlock()
}

func (s *NativeService) Events() *EventBus { return s.bus }

func (s *NativeService) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /enhance/v1/desktop/actions/{action}", func(w http.ResponseWriter, r *http.Request) {
		var payload json.RawMessage
		if !httpx.Decode(w, r, &payload) {
			return
		}
		value, err := s.Action(r.Context(), r.PathValue("action"), payload)
		if err != nil {
			status := 422
			code := "NATIVE_ACTION"
			var native *NativeError
			if errors.As(err, &native) {
				code = native.Code
				status = 409
			}
			httpx.Fail(w, status, code, err.Error())
			return
		}
		httpx.JSON(w, 200, map[string]any{"value": value})
	})
}

func (s *NativeService) Action(ctx context.Context, action string, payload json.RawMessage) (any, error) {
	input := map[string]any{}
	if len(payload) > 0 && string(payload) != "null" {
		if err := json.Unmarshal(payload, &input); err != nil {
			return nil, errors.New("动作参数必须是对象")
		}
	}
	if strings.HasPrefix(action, "planner.") {
		features, err := s.addonFeatures(ctx)
		if err != nil {
			return nil, err
		}
		if !features.Planner {
			return nil, errors.New("规划是附加功能，请先在设置中开启")
		}
		s.mu.Lock()
		planner := s.planner
		s.mu.Unlock()
		if planner == nil {
			return nil, errors.New("规划模块尚未启用")
		}
		return planner.PlannerAction(ctx, action, payload)
	}
	switch action {
	case "bootstrap":
		return s.bootstrap(ctx)
	case "features.get":
		return s.addonFeatures(ctx)
	case "features.set":
		return s.setAddonFeatures(ctx, payload)
	case "events.poll":
		after, _ := input["after"].(float64)
		wait, _ := input["waitMs"].(float64)
		if wait < 0 || wait > 10000 {
			wait = 10000
		}
		scopes := []string{}
		rawScopes, _ := input["scopes"].([]any)
		for _, value := range rawScopes {
			if scope, ok := value.(string); ok && len(scope) <= 200 {
				scopes = append(scopes, scope)
			}
		}
		return s.bus.Poll(ctx, int64(after), scopes, time.Duration(wait)*time.Millisecond), nil
	case "session.view":
		return s.sessionView(ctx, stringInput(input, "id"))
	case "sessions.list":
		// A plugin-visible list that carries only session metadata. bootstrap also
		// returns models, planner threads and recovery settings, which a plugin with
		// sessions:read must not receive.
		sessions, err := s.kap.Sessions(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"items": sessions, "total": len(sessions)}, nil
	case "sessions.view":
		return s.sessionView(ctx, stringInput(input, "id"))
	case "sessions.create":
		return s.createSession(ctx, input)
	case "sessions.send":
		_, err := s.sendPrompt(ctx, input)
		return map[string]any{"sessionId": stringInput(input, "id")}, err
	case "session.watch":
		id := stringInput(input, "id")
		_, err := s.sessionView(ctx, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"watching": true}, nil
	case "session.unwatch":
		id := stringInput(input, "id")
		s.kap.Unwatch(id)
		s.mu.Lock()
		delete(s.cache, id)
		s.mu.Unlock()
		return map[string]any{"watching": false}, nil
	case "session.create":
		return s.createSession(ctx, input)
	case "session.send":
		_, err := s.sendPrompt(ctx, input)
		return map[string]any{"sessionId": stringInput(input, "id")}, err
	case "session.abort":
		id := stringInput(input, "id")
		if id == "" {
			return nil, errors.New("缺少会话 ID")
		}
		s.touchSession(id)
		raw, err := s.kap.Request(ctx, http.MethodPost, "/sessions/"+id+":abort", map[string]any{})
		return rawAsAny(raw), err
	case "session.reply":
		return s.reply(ctx, input)
	case "handoff":
		return s.handoff(ctx, input)
	case "config.get":
		raw, err := s.kap.Request(ctx, http.MethodGet, "/config", nil)
		return rawAsAny(raw), err
	case "config.save":
		patch, ok := input["patch"].(map[string]any)
		if !ok || len(patch) == 0 {
			return nil, errors.New("需要非空配置补丁")
		}
		raw, err := s.kap.Request(ctx, http.MethodPost, "/config", patch)
		return rawAsAny(raw), err
	case "managed.usage":
		raw, err := s.kap.Request(ctx, http.MethodGet, "/oauth/usage", nil)
		return rawAsAny(raw), err
	case "attach.status":
		return s.attachStatus(ctx), nil
	case "attach.enable":
		return s.enableAttach(ctx, input)
	case "attach.disable":
		return s.disableAttach()
	case "cache.summary":
		return s.cacheSummary(ctx, time.Now().UTC())
	case "cache.sessions":
		return s.cacheSessions(ctx, time.Now().UTC(), parseCacheLimit(input))
	case "cache.history":
		return s.cacheHistory(ctx, time.Now().UTC(), stringInput(input, "range"))
	case "cache.retention":
		return s.cacheRetention(ctx, time.Now().UTC())
	case "cache.receipts":
		return s.cacheReceipts(ctx, parseCacheLimit(input))
	case "automation.get":
		return s.automationSettings(ctx)
	case "automation.set":
		return s.saveAutomationSettings(ctx, payload)
	case "recovery.get":
		settings, err := s.automationSettings(ctx)
		return settings.Recovery, err
	case "recovery.set":
		return s.setRecovery(ctx, payload)
	case "recovery.watch":
		return s.watchRecovery(ctx, input)
	case "recovery.unwatch":
		return s.unwatchRecovery(ctx, input)
	case "recovery.check":
		return s.recoveryCheck(ctx)
	default:
		return nil, fmt.Errorf("未知或未经允许的宿主动作：%s", action)
	}
}

func rawAsAny(raw json.RawMessage) any {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	return value
}

func stringInput(input map[string]any, key string) string {
	value, _ := input[key].(string)
	return strings.TrimSpace(value)
}

func (s *NativeService) plannerWorkspace(ctx context.Context, id string) (string, error) {
	raw, err := s.kap.Request(ctx, http.MethodGet, "/sessions/"+id, nil)
	if err != nil {
		return "", err
	}
	session, err := mapNativeSession(raw)
	if err != nil {
		return "", err
	}
	if session.CWD == "" {
		return s.cwd, nil
	}
	return session.CWD, nil
}

func (s *NativeService) bootstrap(ctx context.Context) (map[string]any, error) {
	sessions, err := s.kap.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	models, err := s.kap.Models(ctx)
	if err != nil {
		return nil, err
	}
	features, err := s.addonFeatures(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	planner := s.planner
	s.mu.Unlock()
	threads := any([]any{})
	modelList := any(models)
	if planner != nil && features.Planner {
		threads, err = planner.PlannerThreads(ctx)
		if err != nil {
			return nil, err
		}
		modelList, err = planner.PlannerModels(ctx)
		if err != nil {
			return nil, err
		}
	}
	recovery, err := s.automationSettings(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"cwd": s.cwd, "models": modelList, "nativeModels": models, "threads": threads, "sessions": sessions, "recovery": recovery.Recovery, "features": features}, nil
}

func (s *NativeService) nativeTurns(ctx context.Context, id string, count int) ([]NativeTurn, error) {
	raw, err := s.kap.Request(ctx, http.MethodGet, fmt.Sprintf("/sessions/%s/agents/main/turns?page_size=%d", id, count), nil)
	if err != nil {
		return nil, err
	}
	page, err := decodeNative[struct {
		Turns []NativeTurn `json:"turns"`
	}](raw, "会话轮次")
	return page.Turns, err
}

func (s *NativeService) readSession(ctx context.Context, id string) (cachedSession, error) {
	raw, err := s.kap.Request(ctx, http.MethodGet, "/sessions/"+id, nil)
	if err != nil {
		return cachedSession{}, err
	}
	session, err := mapNativeSession(raw)
	if err != nil {
		return cachedSession{}, err
	}
	turns, err := s.nativeTurns(ctx, id, 50)
	if err != nil {
		return cachedSession{}, err
	}
	var goal json.RawMessage
	goalKnown := false
	if raw, goalErr := s.kap.Request(ctx, http.MethodGet, "/sessions/"+id+"/goal", nil); goalErr == nil {
		goalKnown = true
		if string(raw) != "null" {
			goal = raw
		}
	}
	return cachedSession{session: session, turns: turns, goal: goal, goalKnown: goalKnown}, nil
}

func (s *NativeService) sessionView(ctx context.Context, id string) (SessionView, error) {
	if id == "" {
		return SessionView{}, errors.New("缺少会话 ID")
	}
	loaded, err := s.readSession(ctx, id)
	if err != nil {
		return SessionView{}, err
	}
	s.mu.Lock()
	previous := s.cache[id]
	loaded.revision = previous.revision + 1
	loaded.lastActivity = time.Now().UTC()
	s.cache[id] = loaded
	s.mu.Unlock()
	if err = s.kap.Watch(id); err != nil {
		return SessionView{}, err
	}
	return sessionViewFromCache(loaded), nil
}

func sessionViewFromCache(cache cachedSession) SessionView {
	messages := []NativeMessage{}
	for _, turn := range cache.turns {
		messages = append(messages, turnMessages(turn)...)
		if turn.Error != nil {
			messages = append(messages, NativeMessage{ID: turn.TurnID + "-error", Role: "error", Text: turn.Error.Message})
		}
	}
	return SessionView{Session: cache.session, Messages: messages, Approvals: turnApprovals(cache.turns), Goal: cache.goal}
}

func boundedText(value any, limit int) string {
	text := ""
	switch typed := value.(type) {
	case string:
		text = typed
	case nil:
	default:
		data, _ := json.Marshal(value)
		text = string(data)
	}
	if len(text) > limit {
		return text[:limit] + "\n…（内容已截断）"
	}
	return text
}

func turnMessages(turn NativeTurn) []NativeMessage {
	messages := []NativeMessage{}
	for index, item := range turn.Items {
		id := item.ItemID
		if id == "" {
			id = fmt.Sprintf("%s-item-%d", turn.TurnID, index)
		}
		switch item.Type {
		case "user_message", "assistant_message":
			role := "user"
			if item.Type == "assistant_message" {
				role = "assistant"
			}
			messages = append(messages, NativeMessage{ID: id, Role: role, Text: boundedText(item.Text, 20000), At: item.CreatedAt, Attachments: item.Attachments})
		case "tool_call":
			text := boundedText(item.Output, 20000)
			if item.Error != nil {
				text = "错误：" + boundedText(item.Error, 4000)
			}
			if text == "" {
				text = "（" + item.Stage + "）"
			}
			messages = append(messages, NativeMessage{ID: id, Role: "tool", Text: text, Tool: item.Name})
		case "notification":
			messages = append(messages, NativeMessage{ID: id, Role: "system", Text: boundedText(item.Text, 4000)})
		}
	}
	return messages
}

func turnApprovals(turns []NativeTurn) []NativeApproval {
	approvals := []NativeApproval{}
	seen := map[string]bool{}
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.Type != "tool_call" || item.InteractionID == "" || item.Status == "ended" || seen[item.InteractionID] {
				continue
			}
			var display struct {
				Kind   string `json:"kind"`
				Plan   string `json:"plan"`
				Review struct {
					State string `json:"state"`
				} `json:"review"`
				Options []struct {
					Label string `json:"label"`
				} `json:"options"`
			}
			_ = json.Unmarshal(item.Display, &display)
			if item.Stage != "awaiting_approval" && !(display.Kind == "plan_review" && display.Review.State == "pending") {
				continue
			}
			seen[item.InteractionID] = true
			approval := NativeApproval{ID: item.InteractionID, Title: item.Name, Detail: boundedText(item.Input, 4000)}
			if item.InputText != "" {
				approval.Detail = boundedText(item.InputText, 4000)
			}
			if display.Kind == "plan_review" {
				approval.Detail = boundedText(display.Plan, 4000)
				for _, option := range display.Options {
					approval.Options = append(approval.Options, NativeOption{Label: option.Label, Value: option.Label})
				}
			}
			if len(approval.Options) == 0 {
				approval.Options = []NativeOption{{Label: "批准", Value: "approved"}, {Label: "拒绝", Value: "rejected"}}
			}
			approvals = append(approvals, approval)
		}
	}
	return approvals
}

func (s *NativeService) applyFrame(frame NativeFrame) {
	if frame.SessionID == "" || frame.AgentID != "" && frame.AgentID != "main" {
		return
	}
	s.mu.Lock()
	cache, ok := s.cache[frame.SessionID]
	if ok {
		cache.revision++
		cache.lastActivity = time.Now().UTC()
		switch frame.Type {
		case "turns":
			cache.turns = frame.Turns
		case "turn.started", "turn.ended":
			found := false
			for index := range cache.turns {
				if cache.turns[index].TurnID == frame.Turn.TurnID {
					updated := frame.Turn
					if len(updated.Items) == 0 {
						updated.Items = cache.turns[index].Items
					}
					if updated.Error == nil {
						updated.Error = cache.turns[index].Error
					}
					cache.turns[index] = updated
					found = true
					break
				}
			}
			if !found {
				cache.turns = append(cache.turns, frame.Turn)
			}
			cache.session.Busy = frame.Type == "turn.started"
			cache.session.MainTurnActive = cache.session.Busy
			if frame.Type == "turn.ended" {
				cache.session.PendingInteraction = "none"
			}
		case "item.started", "item.ended", "item.updated", "item.delta", "item.removed":
			for turnIndex := range cache.turns {
				if cache.turns[turnIndex].TurnID != frame.TurnID {
					continue
				}
				items := cache.turns[turnIndex].Items
				index := -1
				for itemIndex := range items {
					if items[itemIndex].ItemID == frame.Item.ItemID || frame.ItemID != "" && items[itemIndex].ItemID == frame.ItemID {
						index = itemIndex
						break
					}
				}
				switch frame.Type {
				case "item.started", "item.ended":
					if index < 0 {
						cache.turns[turnIndex].Items = append(items, frame.Item)
					} else {
						cache.turns[turnIndex].Items[index] = frame.Item
					}
				case "item.removed":
					if index >= 0 {
						cache.turns[turnIndex].Items = append(items[:index], items[index+1:]...)
					}
				case "item.updated":
					if index >= 0 {
						raw, _ := json.Marshal(items[index])
						var merged map[string]any
						_ = json.Unmarshal(raw, &merged)
						var changes map[string]any
						_ = json.Unmarshal(frame.Set, &changes)
						for key, value := range changes {
							merged[key] = value
						}
						raw, _ = json.Marshal(merged)
						_ = json.Unmarshal(raw, &cache.turns[turnIndex].Items[index])
					}
				case "item.delta":
					if index >= 0 && frame.Field == "text" {
						cache.turns[turnIndex].Items[index].Text += frame.Delta
					}
				}
			}
		case "turns.removed":
			removed := map[string]bool{}
			for _, id := range frame.TurnIDs {
				removed[id] = true
			}
			kept := cache.turns[:0]
			for _, turn := range cache.turns {
				if !removed[turn.TurnID] {
					kept = append(kept, turn)
				}
			}
			cache.turns = kept
		case "goal":
			cache.goal = frame.Goal
			cache.goalKnown = true
		}
		s.cache[frame.SessionID] = cache
	}
	s.mu.Unlock()
	if frame.Type == "compaction.completed" || frame.Type == "compaction.cancelled" || frame.Type == "compaction.blocked" {
		s.finishCompaction(frame)
	}
	if ok {
		s.bus.Publish("session:"+frame.SessionID, sessionViewFromCache(cache))
	}
}

func (s *NativeService) createSession(ctx context.Context, input map[string]any) (NativeSession, error) {
	cwd := stringInput(input, "cwd")
	if cwd == "" {
		cwd = s.cwd
	}
	body := map[string]any{"metadata": map[string]any{"cwd": cwd}}
	if title := stringInput(input, "title"); title != "" {
		body["title"] = title
	}
	raw, err := s.kap.Request(ctx, http.MethodPost, "/sessions", body)
	if err != nil {
		return NativeSession{}, err
	}
	return mapNativeSession(raw)
}

func (s *NativeService) sendPrompt(ctx context.Context, input map[string]any) (any, error) {
	id, text := stringInput(input, "id"), stringInput(input, "text")
	attachments, _ := input["attachments"].([]any)
	if id == "" || text == "" && len(attachments) == 0 {
		return nil, errors.New("会话 ID 和消息内容不能为空")
	}
	if len(attachments) > 16 {
		return nil, errors.New("一次最多添加 16 个附件")
	}
	content := []any{}
	if text != "" {
		content = append(content, map[string]string{"type": "text", "text": text})
	}
	for _, value := range attachments {
		path, ok := value.(string)
		if !ok || !filepath.IsAbs(path) {
			return nil, errors.New("附件必须是本机文件的绝对路径")
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("附件文件已不存在或无法读取")
		}
		kind := "file"
		switch strings.ToLower(filepath.Ext(path)) {
		case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
			kind = "image"
		case ".mp4", ".webm", ".mov":
			kind = "video"
		}
		part := map[string]any{"type": kind, "name": info.Name()}
		if kind == "file" {
			part["path"] = path
		} else {
			part["source"] = map[string]string{"kind": "path", "path": path}
		}
		content = append(content, part)
	}
	mode := stringInput(input, "mode")
	if mode == "" {
		mode = "normal"
	}
	if mode != "normal" && mode != "plan" && mode != "tower" && mode != "inherit" {
		return nil, errors.New("发送模式无效")
	}
	body := map[string]any{"content": content}
	if permission := stringInput(input, "permissionMode"); permission != "" {
		if permission != "manual" && permission != "auto" && permission != "yolo" {
			return nil, errors.New("权限模式无效")
		}
		body["permission_mode"] = permission
	}
	if mode != "inherit" {
		body["plan_mode"] = mode == "plan"
		body["tower_mode"] = mode == "tower"
	}
	if model := stringInput(input, "model"); model != "" {
		body["model"] = model
	}
	s.touchSession(id)
	raw, err := s.kap.Request(ctx, http.MethodPost, "/sessions/"+id+"/prompts", body)
	return rawAsAny(raw), err
}

func (s *NativeService) reply(ctx context.Context, input map[string]any) (any, error) {
	id, interaction := stringInput(input, "id"), stringInput(input, "interactionId")
	if id == "" || interaction == "" {
		return nil, errors.New("缺少会话或交互 ID")
	}
	value := input["value"]
	var body map[string]any
	if decision, ok := value.(string); ok && (decision == "approved" || decision == "rejected" || decision == "cancelled") {
		body = map[string]any{"kind": "approval", "decision": decision}
	} else if object, ok := value.(map[string]any); ok {
		if decision, ok := object["decision"].(string); ok {
			body = map[string]any{"kind": "approval", "decision": decision}
			if label, ok := object["selectedLabel"].(string); ok {
				body["selected_label"] = label
			}
			if feedback, ok := object["feedback"].(string); ok {
				body["feedback"] = feedback
			}
		} else if _, ok := object["answers"]; ok {
			body = map[string]any{"kind": "question", "answers": object["answers"]}
		} else if dismissed, _ := object["dismissed"].(bool); dismissed {
			body = map[string]any{"kind": "question", "dismissed": true}
		} else if label, ok := object["selectedLabel"].(string); ok {
			body = map[string]any{"kind": "approval", "decision": "approved", "selected_label": label}
		}
	}
	if body == nil {
		return nil, errors.New("审批或提问回复格式无效")
	}
	s.touchSession(id)
	raw, err := s.kap.Request(ctx, http.MethodPost, "/sessions/"+id+"/interactions/"+interaction, body)
	return rawAsAny(raw), err
}

func (s *NativeService) handoff(ctx context.Context, input map[string]any) (map[string]any, error) {
	id := stringInput(input, "targetId")
	if id == "" {
		created, err := s.createSession(ctx, map[string]any{"title": "规划交接", "cwd": s.cwd})
		if err != nil {
			return nil, err
		}
		id = created.ID
	}
	input["id"] = id
	if _, err := s.sendPrompt(ctx, input); err != nil {
		return nil, err
	}
	return map[string]any{"sessionId": id}, nil
}
