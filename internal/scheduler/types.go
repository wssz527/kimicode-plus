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

	FailureNone              = ""
	FailurePreflight         = "preflight_failed"
	FailureQuotaInsufficient = "quota_insufficient"
	FailureConcurrencyLimit  = "concurrency_limit"
	FailureTimeout           = "timeout"
	FailureMaxTurns          = "max_turns"
	FailureOverlap           = "overlap"
	FailureMissed            = "missed"
	FailureBusyTimeout       = "busy_timeout"
	FailureCancelled         = "cancelled"
	FailureExecution         = "execution_failed"

	NotifyRunFailed       = "run_failed"
	NotifyPreflightFailed = "preflight_failed"
	NotifyRunSkipped      = "run_skipped"

	ScheduledSessionPrefix = "[定时] "

	DefaultTimeoutMinutes  = 30
	MaxTimeoutMinutes      = 1440
	DefaultMaxConcurrent   = 2
	MaxAllowedConcurrent   = 10
	DefaultMinQuotaPercent = 10
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
	ArchiveOnRead  bool         `json:"archiveOnRead"`
	TimeoutMinutes int          `json:"timeoutMinutes"`
	MaxTurns       int          `json:"maxTurns"`
	AllowDirtyYolo bool         `json:"allowDirtyYolo,omitempty"`
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
	FailureKind    string `json:"failureKind,omitempty"`
	Message        string `json:"message,omitempty"`
	Unread         bool   `json:"unread"`
	Archived       bool   `json:"archived,omitempty"`
	ArchiveError   string `json:"archiveError,omitempty"`
	ScheduledFor   string `json:"scheduledFor,omitempty"`
	StartedAt      string `json:"startedAt"`
	FinishedAt     string `json:"finishedAt,omitempty"`
}

type Prefs struct {
	AutoMarkRead       bool `json:"autoMarkRead"`
	MaxConcurrent      int  `json:"maxConcurrent"`
	MinQuotaPercent    int  `json:"minQuotaPercent"`
	MinQuotaPercentSet bool `json:"-"`
}

type AutoAttachInfo struct {
	Enabled           bool   `json:"enabled"`
	Kind              string `json:"kind,omitempty"`
	Platform          string `json:"platform,omitempty"`
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
}

type GitWorktreeStatus struct {
	IsGitRepo  bool     `json:"isGitRepo"`
	Clean      bool     `json:"clean"`
	DirtyCount int      `json:"dirtyCount,omitempty"`
	Sample     []string `json:"sample,omitempty"`
	Error      string   `json:"error,omitempty"`
}

type NotificationEvent struct {
	RunID       string `json:"runId"`
	TaskID      string `json:"taskId"`
	TaskName    string `json:"taskName"`
	SessionID   string `json:"sessionId,omitempty"`
	EventKind   string `json:"eventKind"`
	FailureKind string `json:"failureKind,omitempty"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	Tag         string `json:"tag"`
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
	// UserMessageIDs carries the item ids of the turn's user messages. The
	// prompts endpoint echoes the accepted user_message_id, which is how a run
	// proves which turns belong to it.
	UserMessageIDs []string
}

// SendPromptReceipt is the official acknowledgment of an accepted prompt.
type SendPromptReceipt struct {
	UserMessageID string
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
	SendPrompt(ctx context.Context, sessionID, text, model, effort, permissionMode string) (SendPromptReceipt, error)
	ArchiveSession(ctx context.Context, sessionID string) error
	ArchiveSessions(ctx context.Context, sessionIDs []string) error
	AbortSession(ctx context.Context, sessionID string) error
	CheckAccount(ctx context.Context) (bool, string)
	QuotaRemainingPercent(ctx context.Context) (remaining float64, known bool, source string, detail string)
	AutoAttachStatus(ctx context.Context) AutoAttachInfo
	SessionCacheStats(ctx context.Context, sessionIDs []string) (map[string]SessionCacheStat, bool, int)
	PublishEvent(scope string, value any)
	RecordPluginActivity(ctx context.Context, pluginID, detail string)
}
