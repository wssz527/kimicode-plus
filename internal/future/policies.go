package future

import (
	"context"
	"strings"
	"time"
)

type CompactionPolicy struct {
	Idle                time.Duration
	Enabled             bool
	NativeVerified      bool
	TowerWorkerVerified bool
}
type SessionState struct {
	LastActivity                                                   time.Time
	MainBusy, ToolBusy, ApprovalPending, Recovering, WorkerRunning bool
	Revision                                                       string
}

func (p CompactionPolicy) Eligible(now time.Time, s SessionState) bool {
	idle := p.Idle
	if idle == 0 {
		idle = 25 * time.Minute
	}
	return p.Enabled && p.NativeVerified && (!s.WorkerRunning || p.TowerWorkerVerified) && !s.MainBusy && !s.ToolBusy && !s.ApprovalPending && !s.Recovering && s.Revision != "" && !s.LastActivity.IsZero() && now.Sub(s.LastActivity) >= idle
}

type NativeCompactor interface {
	Request(context.Context, string, string) (string, error)
}
type BrowserPermissions struct {
	Read      bool `json:"read"`
	FillDraft bool `json:"fillDraft"`
	Send      bool `json:"send"`
	Upload    bool `json:"upload"`
}
type MigrationScope struct {
	Browser   string
	Mode      string
	Sites     []string
	ConsentID string
}
type BrowserSessionManager interface {
	Plan(context.Context, MigrationScope) (MigrationPlan, error)
}
type MigrationPlan struct {
	ID        string
	Conflicts []string
	Skipped   []string
	Status    string
}
type CookieIdentity struct {
	Domain, Path, Name, PartitionKey string
	TargetPartition                  string
	HostOnly                         bool
}

type CookieCandidate struct {
	SourceBrowser string
	SourceStoreID string
	Target        CookieIdentity
}

func (c CookieIdentity) ConflictKey() string {
	return c.TargetPartition + "\x00" + strings.ToLower(strings.TrimPrefix(c.Domain, ".")) + "\x00" + c.Path + "\x00" + c.Name + "\x00" + c.PartitionKey
}

type PaneBinding struct{ HostID, SessionID, ProjectID, Worktree string }
type ReviewerRoute struct {
	ModelID     string
	Role        string
	Enforcement string
}
