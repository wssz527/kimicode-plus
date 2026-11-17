package desktop

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"enhancedkimi/internal/cache"
	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/redact"
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
	watchMu      sync.Mutex
	// watchOpMu serializes subscribe and unsubscribe transitions. It is separate
	// from watchMu because one transition includes a network call: holding the
	// reference map only while it is read would let a second caller see a
	// reference for a subscription the official side has not accepted yet, and
	// answer "watching" for a stream that never opened.
	watchOpMu sync.Mutex
	wg        sync.WaitGroup
	cache     map[string]cachedSession
	// watchRefs counts the consumers of each session's official event stream.
	// The KAP subscription is opened for the first consumer and closed when the
	// last one releases, so one pane closing cannot stop another's updates.
	watchRefs map[string]int
	// internalWatches names the sessions an internal module (automation,
	// keepalive, recovery) keeps listening to. It is a set rather than a count
	// because those modules re-assert the same interest on every tick and never
	// pair it with a release, so a count would grow without bound. A session in
	// this set keeps its subscription open across a plugin's last unwatch, which
	// is what stops a plugin from silencing automation for the whole process.
	internalWatches map[string]bool
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
	if strings.HasPrefix(action, "planner.service.") {
		// The v2 plugin path to the local planner. It deliberately skips the old
		// addon feature flag: that flag belongs to the frozen built-in planner UI,
		// while a plugin asks for the engine itself and brings its own surface.
		s.mu.Lock()
		planner := s.planner
		s.mu.Unlock()
		if planner == nil {
			return nil, errors.New("规划模块尚未启用")
		}
		return planner.PlannerAction(ctx, strings.TrimPrefix(action, "planner.service."), payload)
	}
	// Thread and model discovery is read-only and has no plugin-visible meaning
	// behind the frozen built-in planner, so it is answered here before the
	// feature flag is consulted. It must come before the generic `planner.` branch
	// below, which would otherwise demand that flag and never reach these cases.
	if action == "planner.threads" || action == "planner.models" {
		s.mu.Lock()
		planner := s.planner
		s.mu.Unlock()
		if planner == nil {
			return nil, errors.New("规划模块尚未启用")
		}
		if action == "planner.threads" {
			threads, err := planner.PlannerThreads(ctx)
			if err != nil {
				return nil, err
			}
			return map[string]any{"items": threads}, nil
		}
		models, err := planner.PlannerModels(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"items": models}, nil
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
	case "sessions.diff":
		return s.sessionDiff(ctx, stringInput(input, "id"))
	case "sessions.watch":
		id := stringInput(input, "id")
		if err := s.WatchSession(id); err != nil {
			return nil, err
		}
		return map[string]any{"watching": true, "sessionId": id}, nil
	case "sessions.unwatch":
		id := stringInput(input, "id")
		s.UnwatchSession(id)
		return map[string]any{"watching": false, "sessionId": id}, nil
	case "sessions.history":
		return s.sessionHistory(ctx, input)
	case "models.list":
		models, err := s.kap.Models(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"items": models, "total": len(models)}, nil
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
		// The v1 action keeps its shape but now takes a reference, so a reader
		// that paired it with the v1 unwatch cannot drop a subscription another
		// consumer still holds.
		if err = s.WatchSession(id); err != nil {
			return nil, err
		}
		return map[string]any{"watching": true}, nil
	case "session.unwatch":
		id := stringInput(input, "id")
		s.UnwatchSession(id)
		// The cache belongs to the session, not to this reader: another pane, a
		// plugin or automation may still be subscribed, and dropping it here would
		// make applyFrame discard every later frame for want of a cache entry.
		// Only a session nobody watches any more is dropped, and the next read
		// rebuilds it from the official record anyway.
		if !s.sessionHeld(id) {
			s.mu.Lock()
			delete(s.cache, id)
			s.mu.Unlock()
		}
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
	case "diagnostics.get":
		return s.diagnosticsStatus(), nil
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

// sessionHistory reads one page of an agent's turns with the official ordinal
// cursor. Unlike sessionView it is not capped at the newest 50 turns and does not
// fill the live cache: history is read-only paging, and mixing it into the cache
// would make the next event frame replace the newest page with an older one.
func (s *NativeService) sessionHistory(ctx context.Context, input map[string]any) (any, error) {
	id := stringInput(input, "id")
	if id == "" {
		return nil, errors.New("缺少会话 ID")
	}
	agent := stringInput(input, "agentId")
	if agent == "" {
		agent = "main"
	}
	size := intInput(input, "size", 50)
	if size < 1 {
		size = 1
	}
	if size > 100 {
		size = 100
	}
	query := fmt.Sprintf("/sessions/%s/agents/%s/turns?page_size=%d", url.PathEscape(id), url.PathEscape(agent), size)
	before := intInput(input, "before", 0)
	if before > 0 {
		query += fmt.Sprintf("&before_ordinal=%d", before)
	}
	raw, err := s.kap.Request(ctx, http.MethodGet, query, nil)
	if err != nil {
		return nil, err
	}
	page, err := decodeNative[struct {
		Turns   []NativeTurn `json:"turns"`
		HasMore bool         `json:"has_more"`
	}](raw, "会话历史")
	if err != nil {
		return nil, err
	}
	messages := []NativeMessage{}
	for _, turn := range page.Turns {
		messages = append(messages, turnMessages(turn)...)
		if turn.Error != nil {
			messages = append(messages, NativeMessage{ID: turn.TurnID + "-error", Role: "error", Text: turn.Error.Message})
		}
	}
	var nextBefore *int64
	for _, turn := range page.Turns {
		if turn.Ordinal == nil {
			continue
		}
		if nextBefore == nil || *turn.Ordinal < *nextBefore {
			value := *turn.Ordinal
			nextBefore = &value
		}
	}
	return map[string]any{
		"sessionId":  id,
		"agentId":    agent,
		"messages":   messages,
		"approvals":  turnApprovals(page.Turns),
		"hasMore":    page.HasMore,
		"nextBefore": nextBefore,
		"pageSize":   size,
	}, nil
}

func intInput(input map[string]any, key string, fallback int) int {
	switch value := input[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	}
	return fallback
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
	// A read does not subscribe: watching is an explicit, reference-counted
	// decision, so a plugin that only looks at a session never holds the
	// official event stream open. The v1 `sessions.view` capability still works;
	// it just no longer changes the watch set as a side effect.
	return sessionViewFromCache(loaded), nil
}

// WatchSession adds one consumer of a session's official event stream. The KAP
// watch is sent only for the first consumer, and each holder must release with
// exactly one UnwatchSession: another pane, plugin or automation watching the
// same session keeps it alive.
//
// The whole transition is serialized. Two concurrent first watches would
// otherwise both see a zero count, both answer success, and the loser's rollback
// would leave a reference for a stream nobody subscribed to — a caller told it
// is watching while no frame ever arrives.
func (s *NativeService) WatchSession(id string) error {
	if id == "" {
		return errors.New("缺少会话 ID")
	}
	s.watchOpMu.Lock()
	defer s.watchOpMu.Unlock()
	s.watchMu.Lock()
	if s.watchRefs == nil {
		s.watchRefs = map[string]int{}
	}
	first := s.watchRefs[id] == 0 && !s.internalWatches[id]
	s.watchRefs[id] += 1
	s.watchMu.Unlock()
	if !first {
		return nil
	}
	if err := s.kap.Watch(id); err != nil {
		// A failed first watch leaves no reference behind; otherwise a later
		// retry would believe the stream is already open.
		s.watchMu.Lock()
		if s.watchRefs[id] == 1 {
			delete(s.watchRefs, id)
		} else if s.watchRefs[id] > 1 {
			s.watchRefs[id] -= 1
		}
		s.watchMu.Unlock()
		return err
	}
	return nil
}

// UnwatchSession releases one consumer. The official unsubscribe is sent only
// when the last one leaves and no internal module is still listening, so closing
// one pane cannot stop another's updates and a plugin cannot silence automation.
// Releasing an id that was never watched is a no-op, which makes teardown safe
// to run on every path.
func (s *NativeService) UnwatchSession(id string) {
	if id == "" {
		return
	}
	s.watchOpMu.Lock()
	defer s.watchOpMu.Unlock()
	s.releaseRef(id, false)
}

// releaseRef drops one reference and, when nothing holds the session any more,
// sends the official unsubscribe. Callers hold watchOpMu. A release with no
// reference to drop is a no-op: it must neither send a frame nor make an
// unrelated module's subscription look ended.
func (s *NativeService) releaseRef(id string, internal bool) {
	s.watchMu.Lock()
	held := true
	if internal {
		if !s.internalWatches[id] {
			held = false
		}
		delete(s.internalWatches, id)
	} else if refs := s.watchRefs[id]; refs > 1 {
		s.watchRefs[id] = refs - 1
	} else if refs == 1 {
		delete(s.watchRefs, id)
	} else {
		held = false
	}
	empty := s.watchRefs[id] == 0 && !s.internalWatches[id]
	s.watchMu.Unlock()
	if held && empty {
		s.kap.Unwatch(id)
	}
}

// WatchSessionInternal records an internal module's lasting interest in a
// session and subscribes when nothing else holds it. The interest is a set
// member, not a reference: automation re-asserts it every tick and never
// releases it, so counting it would grow until the process exits. The
// subscription therefore survives the last plugin unwatch, and the reconnecting
// event loop re-subscribes it from `kap.watched` like any other.
func (s *NativeService) WatchSessionInternal(id string) error {
	if id == "" {
		return errors.New("缺少会话 ID")
	}
	s.watchOpMu.Lock()
	defer s.watchOpMu.Unlock()
	s.watchMu.Lock()
	if s.internalWatches == nil {
		s.internalWatches = map[string]bool{}
	}
	first := !s.internalWatches[id] && s.watchRefs[id] == 0
	s.internalWatches[id] = true
	s.watchMu.Unlock()
	if !first {
		return nil
	}
	if err := s.kap.Watch(id); err != nil {
		s.watchMu.Lock()
		delete(s.internalWatches, id)
		s.watchMu.Unlock()
		return err
	}
	return nil
}

// WatchedSessions reports the reference count per session. It is the observable
// proof that a release actually dropped the KAP subscription.
func (s *NativeService) WatchedSessions() map[string]int {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	out := make(map[string]int, len(s.watchRefs))
	for id, refs := range s.watchRefs {
		out[id] = refs
	}
	return out
}

// HeldSessions reports every session with an open official subscription,
// counting the internal modules' standing interest as one holder each. It is
// what tells a release whether the stream still has a consumer.
func (s *NativeService) HeldSessions() map[string]int {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	out := make(map[string]int, len(s.watchRefs))
	for id, refs := range s.watchRefs {
		out[id] = refs
	}
	for id := range s.internalWatches {
		out[id] += 1
	}
	return out
}

// sessionHeld reports whether anything still needs a session's event stream.
func (s *NativeService) sessionHeld(id string) bool {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	return s.watchRefs[id] > 0 || s.internalWatches[id]
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
	// The new session's working directory, model and title are chosen by the
	// caller. They only apply when this handoff creates the session: handing off
	// into an existing conversation must not silently move it to another
	// directory or switch its model out from under the user. `model` is validated
	// here and applied through the prompt below, which is where the official
	// client reads it.
	cwd, _, title, err := s.handoffNewSession(ctx, input)
	if err != nil {
		return nil, err
	}
	created := false
	if id == "" {
		session, createErr := s.createSession(ctx, map[string]any{"title": title, "cwd": cwd})
		if createErr != nil {
			return nil, createErr
		}
		id = session.ID
		created = true
	}
	input["id"] = id
	// A model in the prompt body is only meaningful for a session this call just
	// created; an existing target keeps whatever model it already uses. It is
	// applied on the first turn because that is where the official client reads
	// it, and it is dropped for an existing session for the same reason.
	if !created {
		delete(input, "model")
	}
	if _, err = s.sendPrompt(ctx, input); err != nil {
		return nil, err
	}
	return map[string]any{"sessionId": id, "created": created}, nil
}

// handoffNewSession validates what a handoff may choose for a new session. The
// directory must be an existing directory, the model must be in the official
// catalog, and the title is bounded; an existing target ignores all three, so a
// bad value is only an error when it would actually be used.
func (s *NativeService) handoffNewSession(ctx context.Context, input map[string]any) (cwd, model, title string, err error) {
	if stringInput(input, "targetId") != "" {
		return "", "", "", nil
	}
	title = stringInput(input, "title")
	if len([]rune(title)) > 120 {
		return "", "", "", errors.New("交接标题不超过 120 个字符")
	}
	cwd = stringInput(input, "cwd")
	model = stringInput(input, "model")
	if cwd == "" {
		cwd = s.cwd
	} else {
		if !filepath.IsAbs(cwd) {
			return "", "", "", errors.New("交接目录必须是绝对路径")
		}
		info, statErr := os.Stat(cwd)
		if statErr != nil || !info.IsDir() {
			return "", "", "", errors.New("交接目录不存在或不是目录")
		}
		cwd = filepath.Clean(cwd)
	}
	if model != "" {
		catalog, catalogErr := s.kap.Models(ctx)
		if catalogErr != nil {
			return "", "", "", errors.New("无法核对模型列表，请稍后重试")
		}
		known := false
		for _, item := range catalog {
			if item["id"] == model {
				known = true
				break
			}
		}
		if !known {
			return "", "", "", errors.New("交接模型不在可用模型列表中")
		}
	}
	if title == "" {
		title = "规划交接"
	}
	return cwd, model, title, nil
}

type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		b.truncated = true
		return len(p), nil
	}
	remain := b.limit - b.buf.Len()
	if remain <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remain {
		b.buf.Write(p[:remain])
		b.truncated = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func runGitCapped(ctx context.Context, cwd string, limit int, args ...string) (string, bool, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", cwd}, args...)...)
	out := &limitedBuffer{limit: limit}
	var errBuf bytes.Buffer
	cmd.Stdout = out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return out.buf.String(), out.truncated, err
}

func truncateUTF8(text string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text, false
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(text[:end]) {
		end--
	}
	return strings.TrimRight(text[:end], "\r\n") + "\n…（内容已按长度上限截断）", true
}

func (s *NativeService) sessionDiff(ctx context.Context, id string) (any, error) {
	if id == "" {
		return nil, errors.New("缺少会话 ID")
	}
	raw, err := s.kap.Request(ctx, http.MethodGet, "/sessions/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	session, err := mapNativeSession(raw)
	if err != nil {
		return nil, err
	}
	cwd := strings.TrimSpace(session.CWD)
	unavailable := func(reason string) map[string]any {
		return map[string]any{
			"sessionId": id,
			"title":     session.Title,
			"cwd":       cwd,
			"available": false,
			"clean":     false,
			"status":    "",
			"diff":      "",
			"truncated": false,
			"reason":    reason,
		}
	}
	if cwd == "" || !filepath.IsAbs(cwd) {
		return unavailable("该会话没有记录本地工作目录，无法读取 Git Diff。"), nil
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return unavailable("该会话的工作目录不存在，无法读取 Git Diff。"), nil
	}
	gitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	inside, _, insideErr := runGitCapped(gitCtx, cwd, 256, "rev-parse", "--is-inside-work-tree")
	if insideErr != nil || strings.TrimSpace(inside) != "true" {
		return unavailable("该会话工作目录不是 Git 仓库，无法读取工作区 Diff。"), nil
	}
	statusRaw, statusCap, statusErr := runGitCapped(gitCtx, cwd, 16<<10, "status", "--short", "--untracked-files=normal")
	if statusErr != nil {
		return unavailable("无法读取 Git 工作区状态，请确认 Git 仓库可访问。"), nil
	}
	var diffRaw string
	var diffCap bool
	if _, _, headErr := runGitCapped(gitCtx, cwd, 256, "rev-parse", "--verify", "HEAD"); headErr == nil {
		var diffErr error
		diffRaw, diffCap, diffErr = runGitCapped(gitCtx, cwd, 64<<10, "diff", "--no-color", "--no-ext-diff", "HEAD")
		if diffErr != nil {
			return unavailable("读取 Git Diff 失败，请确认工作区可访问。"), nil
		}
	} else {
		staged, stagedCap, _ := runGitCapped(gitCtx, cwd, 32<<10, "diff", "--no-color", "--no-ext-diff", "--cached")
		unstaged, unstagedCap, _ := runGitCapped(gitCtx, cwd, 32<<10, "diff", "--no-color", "--no-ext-diff")
		diffCap = stagedCap || unstagedCap
		parts := []string{}
		if strings.TrimSpace(staged) != "" {
			parts = append(parts, strings.TrimSpace(staged))
		}
		if strings.TrimSpace(unstaged) != "" {
			parts = append(parts, strings.TrimSpace(unstaged))
		}
		diffRaw = strings.Join(parts, "\n")
	}
	status, statusCut := truncateUTF8(strings.TrimSpace(redact.Text(statusRaw)), 4000)
	diff, diffCut := truncateUTF8(strings.TrimSpace(redact.Text(diffRaw)), 20000)
	clean := status == "" && diff == ""
	reason := ""
	if clean {
		reason = "当前工作区没有未提交的 Git 变更。"
	}
	return map[string]any{
		"sessionId": id,
		"title":     session.Title,
		"cwd":       cwd,
		"available": true,
		"clean":     clean,
		"status":    status,
		"diff":      diff,
		"truncated": statusCap || diffCap || statusCut || diffCut,
		"reason":    reason,
	}, nil
}
