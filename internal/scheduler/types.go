package scheduler

import (
	"context"
	"time"
)

const (
	ModeNewSession   = "new_session"
	ModeBoundSession = "bound_session"

	MissedCatchup = "catchup"
	MissedSkip    = "skip"

	RunQueued           = "queued"
	RunRunning          = "running"
	RunCompleted        = "completed"
	RunFailed           = "failed"
	RunAwaitingApproval = "awaiting_approval"
	RunSkipped          = "skipped"

	TriggerScheduled = "scheduled"
	TriggerCatchup   = "catchup"
	TriggerManual    = "manual"
)

type ScheduleRule struct {
	Kind          string   `json:"kind"`
	Date          string   `json:"date,omitempty"`
	Time          string   `json:"time,omitempty"`
	Minute        int      `json:"minute,omitempty"`
	Weekdays      []int    `json:"weekdays,omitempty"`
	DaysOfMonth   []int    `json:"daysOfMonth,omitempty"`
	LastDay       bool     `json:"lastDay,omitempty"`
	IntervalValue int      `json:"intervalValue,omitempty"`
	IntervalUnit  string   `json:"intervalUnit,omitempty"`
	TestMode      bool     `json:"testMode,omitempty"`
	Times         []string `json:"times,omitempty"`
	StartDate     string   `json:"startDate,omitempty"`
	EndDate       string   `json:"endDate,omitempty"`
}

type Task struct {
	ID             string       `json:"id"`
	PluginID       string       `json:"pluginId"`
	Name           string       `json:"name"`
	Prompt         string       `json:"prompt"`
	CWD            string       `json:"cwd"`
	Mode           string       `json:"mode"`
	SessionID      string       `json:"sessionId,omitempty"`
	Model          string       `json:"model,omitempty"`
	Effort         string       `json:"effort,omitempty"`
	PermissionMode string       `json:"permissionMode"`
	Schedule       ScheduleRule `json:"schedule"`
	MissedPolicy   string       `json:"missedPolicy"`
	Enabled        bool         `json:"enabled"`
	NextRunAt      string       `json:"nextRunAt,omitempty"`
	LastRunAt      string       `json:"lastRunAt,omitempty"`
	LastStatus     string       `json:"lastStatus,omitempty"`
	LastSummary    string       `json:"lastSummary,omitempty"`
	LastSessionID  string       `json:"lastSessionId,omitempty"`
	CreatedAt      string       `json:"createdAt"`
	UpdatedAt      string       `json:"updatedAt"`
}

type TaskView struct {
	Task
	ScheduleSummary      string   `json:"scheduleSummary"`
	SessionTitle         string   `json:"sessionTitle,omitempty"`
	SessionIdleMinutes   *float64 `json:"sessionIdleMinutes,omitempty"`
	SessionLastRequestAt *string  `json:"sessionLastRequestAt,omitempty"`
	KeepAliveMinutes     int      `json:"keepAliveMinutes,omitempty"`
	KeepAliveEnabled     bool     `json:"keepAliveEnabled,omitempty"`
	WithinKeepAlive      *bool    `json:"withinKeepAlive,omitempty"`
}

type RunRecord struct {
	ID             string `json:"id"`
	TaskID         string `json:"taskId"`
	PluginID       string `json:"pluginId"`
	TaskName       string `json:"taskName"`
	TriggerKind    string `json:"triggerKind"`
	Mode           string `json:"mode"`
	SessionID      string `json:"sessionId,omitempty"`
	SessionTitle   string `json:"sessionTitle,omitempty"`
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
	PermissionMode string `json:"permissionMode,omitempty"`
	Status         string `json:"status"`
	Message        string `json:"message,omitempty"`
	Unread         bool   `json:"unread"`
	ScheduledFor   string `json:"scheduledFor,omitempty"`
	StartedAt      string `json:"startedAt"`
	FinishedAt     string `json:"finishedAt,omitempty"`
}

type Prefs struct {
	AutoMarkRead bool `json:"autoMarkRead"`
}

type SessionInfo struct {
	ID                 string
	Title              string
	CWD                string
	UpdatedAt          string
	Busy               bool
	MainTurnActive     bool
	PendingInteraction string
	LastTurnReason     string
}

type ModelInfo struct {
	ID             string
	Name           string
	Capabilities   []string
	SupportEfforts []string
}

type TurnInfo struct {
	TurnID          string
	HasApproval     bool
	ApprovalTitle   string
	ErrorMessage    string
	HasAssistantMsg bool
}

type SessionCacheStat struct {
	LastRequestAt    *time.Time
	IdleMinutes      *float64
	OpenBackground   int
	KeepAliveEnabled bool
	KeepAliveMinutes int
}

type Executor interface {
	KAPConnected(ctx context.Context) (bool, string)
	Models(ctx context.Context) ([]ModelInfo, error)
	Sessions(ctx context.Context) ([]SessionInfo, error)
	GetSession(ctx context.Context, id string) (SessionInfo, error)
	GetSessionTurns(ctx context.Context, id string, pageSize int) ([]TurnInfo, error)
	CreateSession(ctx context.Context, cwd, title, model, effort, permissionMode string) (SessionInfo, error)
	SendPrompt(ctx context.Context, sessionID, text, model, effort, permissionMode string) error
	SessionCacheStats(ctx context.Context, sessionIDs []string) (map[string]SessionCacheStat, bool, int)
	PublishEvent(scope string, value any)
	RecordPluginActivity(ctx context.Context, pluginID, detail string)
}
