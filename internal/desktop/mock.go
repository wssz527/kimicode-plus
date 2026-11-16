package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"enhancedkimi/internal/httpx"
)

const (
	APIPrefix         = "/enhance/v1"
	OwnerOfficialHost = "official-host"
)

type Mode string

const (
	ModeAvailable   Mode = "AVAILABLE"
	ModeChanged     Mode = "CHANGED"
	ModeUnavailable Mode = "UNAVAILABLE"
)

func (m Mode) Valid() bool {
	switch m {
	case ModeAvailable, ModeChanged, ModeUnavailable:
		return true
	default:
		return false
	}
}

type Behavior string

const (
	BehaviorOK       Behavior = "OK"
	BehaviorTimeout  Behavior = "TIMEOUT"
	BehaviorEmpty    Behavior = "EMPTY"
	BehaviorOccupied Behavior = "OCCUPIED"
	BehaviorPanic    Behavior = "PANIC"
)

func (b Behavior) Valid() bool {
	switch b {
	case BehaviorOK, BehaviorTimeout, BehaviorEmpty, BehaviorOccupied, BehaviorPanic:
		return true
	default:
		return false
	}
}

var (
	ErrUnavailable     = errors.New("desktop adapter unavailable")
	ErrChanged         = errors.New("desktop site structure changed")
	ErrOccupied        = errors.New("session occupied by another client")
	ErrSessionUnknown  = errors.New("desktop session not found")
	ErrNotOwner        = errors.New("desktop session is owned by another host")
	ErrTimeout         = errors.New("desktop submit outcome unknown after timeout")
	ErrReceiptConflict = errors.New("desktop receipt conflicts with an earlier intent")
	ErrEmptyReceipt    = errors.New("desktop returned an empty receipt")
	ErrReceiptMismatch = errors.New("desktop receipt does not match the submitted task")
	ErrSafeMode        = errors.New("safe mode blocks desktop dispatch")
)

type Capabilities struct {
	Sessions    bool `json:"sessions"`
	Submit      bool `json:"submit"`
	Reconcile   bool `json:"reconcile"`
	Draft       bool `json:"draft"`
	Attachments bool `json:"attachments"`
}

type Status struct {
	HostID       string       `json:"hostId"`
	Mock         bool         `json:"mock"`
	Mode         Mode         `json:"mode"`
	Site         string       `json:"site"`
	HostVersion  string       `json:"hostVersion"`
	Owner        string       `json:"owner"`
	Capabilities Capabilities `json:"capabilities"`
	Degraded     []string     `json:"degraded"`
	Notice       string       `json:"notice"`
	CheckedAt    time.Time    `json:"checkedAt"`
}

type Session struct {
	HostID    string    `json:"hostId"`
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Owner     string    `json:"owner"`
	ProjectID string    `json:"projectId"`
	Site      string    `json:"site"`
	Kind      string    `json:"kind"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Submission struct {
	HostID         string
	ProjectID      string
	WebTarget      *WebTarget
	Attachments    []Blob
	ReadAttachment func(context.Context, string) ([]byte, error)
	Intent         string
	TaskID         string
	SessionID      string
	Digest         string
	Direction      string
	Checkpoint     int
	Payload        []byte
	Behavior       Behavior
}

type Receipt struct {
	Attachments []Blob    `json:"attachments,omitempty"`
	ReceiptID   string    `json:"receiptId"`
	TaskID      string    `json:"taskId"`
	SessionID   string    `json:"sessionId"`
	Digest      string    `json:"digest"`
	Checkpoint  int       `json:"checkpoint"`
	State       string    `json:"state"`
	AcceptedAt  time.Time `json:"acceptedAt"`
}

type ReconcileRequest struct {
	Intent     string
	TaskID     string
	SessionID  string
	Digest     string
	ReceiptID  string
	Checkpoint int
}

type Adapter interface {
	Status(ctx context.Context) Status
	Sessions(ctx context.Context) ([]Session, error)
	Submit(ctx context.Context, submission Submission) (Receipt, error)
	Reconcile(ctx context.Context, request ReconcileRequest) (Receipt, bool, error)
}

type Mock struct {
	mu          sync.Mutex
	mode        Mode
	sessions    []Session
	receipts    map[string]Receipt
	byIntent    map[string]Receipt
	intentHash  map[string]string
	submits     int
	reconciles  int
	lastPayload []byte
}

func NewMock() *Mock {
	return &Mock{
		mode: ModeAvailable,
		sessions: []Session{
			{HostID: "mock-desktop-host", ID: "mock-session-alpha", Title: "合成会话 Alpha", Owner: OwnerOfficialHost, ProjectID: "mock-project-alpha", Site: "kimi", Kind: "SYNTHETIC_REFERENCE", UpdatedAt: time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC)},
			{HostID: "mock-desktop-host", ID: "mock-session-beta", Title: "合成会话 Beta", Owner: OwnerOfficialHost, ProjectID: "mock-project-beta", Site: "kimi", Kind: "SYNTHETIC_REFERENCE", UpdatedAt: time.Date(2026, 10, 2, 15, 10, 0, 0, time.UTC)},
			{HostID: "mock-desktop-host", ID: "mock-session-gamma", Title: "合成会话 Gamma", Owner: OwnerOfficialHost, ProjectID: "mock-project-gamma", Site: "kimi", Kind: "SYNTHETIC_REFERENCE", UpdatedAt: time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)},
		},
		receipts:   make(map[string]Receipt),
		byIntent:   make(map[string]Receipt),
		intentHash: make(map[string]string),
	}
}

func (m *Mock) Mode() Mode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mode
}

func (m *Mock) SetMode(mode Mode) error {
	if !mode.Valid() {
		return fmt.Errorf("invalid adapter mode %q", mode)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mode = mode
	return nil
}

func (m *Mock) Status(context.Context) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := Status{HostID: "mock-desktop-host",
		Mock:        true,
		Mode:        m.mode,
		Site:        "kimi",
		HostVersion: "mock-1.0",
		Owner:       OwnerOfficialHost,
		Notice:      "模拟适配器：不启动官方进程，不读取真实配置、Cookie 或会话。",
		CheckedAt:   time.Now().UTC(),
	}
	switch m.mode {
	case ModeAvailable:
		status.Capabilities = Capabilities{Sessions: true, Submit: true, Reconcile: true, Draft: true, Attachments: true}
		status.Degraded = []string{}
	case ModeChanged:
		status.Capabilities = Capabilities{Sessions: true, Submit: false, Reconcile: true, Draft: false, Attachments: false}
		status.Degraded = []string{"SUBMIT", "DRAFT", "ATTACHMENTS"}
	case ModeUnavailable:
		status.Capabilities = Capabilities{}
		status.Degraded = []string{"SESSIONS", "SUBMIT", "RECONCILE", "DRAFT", "ATTACHMENTS"}
	}
	return status
}

func (m *Mock) Sessions(context.Context) ([]Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mode == ModeUnavailable {
		return nil, ErrUnavailable
	}
	out := append([]Session(nil), m.sessions...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func (m *Mock) Submit(ctx context.Context, submission Submission) (Receipt, error) {
	if submission.Intent == "" || submission.TaskID == "" || submission.SessionID == "" || submission.Digest == "" {
		return Receipt{}, fmt.Errorf("mock submit requires intent, task, session and digest")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.submits++
	m.lastPayload = append([]byte(nil), submission.Payload...)
	if m.mode == ModeUnavailable {
		return Receipt{}, ErrUnavailable
	}
	if m.mode == ModeChanged {
		return Receipt{}, ErrChanged
	}
	found := false
	for _, session := range m.sessions {
		if session.ID != submission.SessionID {
			continue
		}
		found = true
		if submission.HostID != "" && (session.HostID != submission.HostID || session.ProjectID != submission.ProjectID) {
			return Receipt{}, ErrNotOwner
		}
		if session.Owner != OwnerOfficialHost {
			return Receipt{}, ErrNotOwner
		}
	}
	if !found {
		return Receipt{}, ErrSessionUnknown
	}
	_, err := receiveBlobs(ctx, submission)
	if err != nil {
		return Receipt{}, err
	}
	key := submission.Intent + "\x00" + submission.Digest
	if existing, ok := m.byIntent[key]; ok {
		return existing, nil
	}
	if prior, ok := m.intentHash[submission.Intent]; ok && prior != submission.Digest {
		return Receipt{}, ErrReceiptConflict
	}
	if submission.Behavior == "" {
		submission.Behavior = BehaviorOK
	}
	switch submission.Behavior {
	case BehaviorOK:
		receipt := m.makeReceipt(submission)
		m.remember(submission, receipt)
		return receipt, nil
	case BehaviorTimeout:
		receipt := m.makeReceipt(submission)
		m.remember(submission, receipt)
		return Receipt{}, ErrTimeout
	case BehaviorEmpty:
		return Receipt{}, ErrEmptyReceipt
	case BehaviorOccupied:
		return Receipt{}, ErrOccupied
	case BehaviorPanic:
		panic("mock adapter panic: submit failure injection")
	default:
		return Receipt{}, fmt.Errorf("invalid mock behavior %q", submission.Behavior)
	}
}

func (m *Mock) Reconcile(_ context.Context, request ReconcileRequest) (Receipt, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mode == ModeUnavailable {
		return Receipt{}, false, ErrUnavailable
	}
	var receipt Receipt
	var ok bool
	m.reconciles++
	if request.ReceiptID != "" {
		receipt, ok = m.receipts[request.ReceiptID]
	} else {
		receipt, ok = m.byIntent[request.Intent+"\x00"+request.Digest]
	}
	if !ok {
		return Receipt{}, false, nil
	}
	if receipt.TaskID != request.TaskID || receipt.SessionID != request.SessionID || receipt.Digest != request.Digest {
		return Receipt{}, false, nil
	}
	if request.Checkpoint != 0 && receipt.Checkpoint != request.Checkpoint {
		return Receipt{}, false, nil
	}
	return receipt, true, nil
}

func (m *Mock) Counters() (submits, reconciles int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.submits, m.reconciles
}

func (m *Mock) LastPayload() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.lastPayload...)
}

func (m *Mock) ResetCounters() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.submits, m.reconciles = 0, 0
	m.lastPayload = nil
}

func (m *Mock) makeReceipt(submission Submission) Receipt {
	delivered := append([]Blob(nil), submission.Attachments...)
	for i := range delivered {
		delivered[i].Status = "UPLOADED"
	}
	sum := sha256.Sum256([]byte(submission.Intent + "\x00" + submission.Digest + "\x00" + submission.TaskID + "\x00" + submission.SessionID))
	return Receipt{
		Attachments: delivered,
		ReceiptID:   "mock-receipt-" + hex.EncodeToString(sum[:16]),
		TaskID:      submission.TaskID,
		SessionID:   submission.SessionID,
		Digest:      submission.Digest,
		Checkpoint:  submission.Checkpoint,
		State:       "DELIVERED",
		AcceptedAt:  time.Now().UTC(),
	}
}

func (m *Mock) remember(submission Submission, receipt Receipt) {
	m.receipts[receipt.ReceiptID] = receipt
	m.byIntent[submission.Intent+"\x00"+submission.Digest] = receipt
	m.intentHash[submission.Intent] = submission.Digest
}

func (m *Mock) Register(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	mux.HandleFunc("GET "+APIPrefix+"/adapter", m.handleStatus)
	mux.HandleFunc("PUT "+APIPrefix+"/adapter", m.handleSetMode)
	mux.HandleFunc("GET "+APIPrefix+"/sessions", m.handleSessions)
}

func (m *Mock) handleStatus(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, m.Status(context.Background()))
}

func (m *Mock) handleSetMode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode Mode `json:"mode"`
	}
	if !httpx.Decode(w, r, &body) {
		return
	}
	if !body.Mode.Valid() {
		httpx.Fail(w, http.StatusBadRequest, "INVALID_MODE", "模式只能是 AVAILABLE、CHANGED 或 UNAVAILABLE")
		return
	}
	if err := m.SetMode(body.Mode); err != nil {
		httpx.Fail(w, http.StatusBadRequest, "INVALID_MODE", "模式无效")
		return
	}
	httpx.JSON(w, http.StatusOK, m.Status(context.Background()))
}

func (m *Mock) handleSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := m.Sessions(r.Context())
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			httpx.Fail(w, http.StatusServiceUnavailable, "ADAPTER_UNAVAILABLE", "模拟适配器当前不可用；本地任务历史仍可查看")
			return
		}
		httpx.Fail(w, http.StatusBadGateway, "ADAPTER_ERROR", "读取模拟会话失败")
		return
	}
	offset, limit := httpx.Page(r)
	total := len(sessions)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	items := sessions[offset:end]
	httpx.JSON(w, http.StatusOK, map[string]any{
		"items":     items,
		"total":     total,
		"mock":      true,
		"ownership": OwnerOfficialHost,
		"notice":    "这些是独立合成的引用，只读且不获取官方会话所有权。",
	})
}
