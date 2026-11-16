package bridge

import (
	"context"
	"enhancedkimi/internal/desktop"
	"sync/atomic"
	"testing"
)

// Source-derived regressions, NOT executed against the full product by reviewer.
type reviewSafetyFlipAdapter struct {
	*desktop.Mock
	enabled *atomic.Bool
}

func (a *reviewSafetyFlipAdapter) Sessions(ctx context.Context) ([]desktop.Session, error) {
	sessions, err := a.Mock.Sessions(ctx)
	a.enabled.Store(true) // deterministically interleave safe-mode activation after initial check
	return sessions, err
}
func TestReviewSafeModeWinsBeforeDispatch(t *testing.T) {
	var safe atomic.Bool
	adapter := &reviewSafetyFlipAdapter{desktop.NewMock(), &safe}
	service := newServiceWithSafe(t, adapter, safe.Load)
	_, err := service.Submit(context.Background(), SubmitRequest{Intent: "review-safe", Task: baseTask(), Confirmed: true})
	submits, _ := adapter.Counters()
	if err == nil || submits != 0 {
		t.Fatalf("safe-mode enabled before actual submission: err=%v submits=%d", err, submits)
	}
}
func TestReviewProjectBindingRequired(t *testing.T) {
	adapter := desktop.NewMock()
	service := newServiceWithSafe(t, adapter, func() bool { return false })
	task := baseTask(func(p *TaskPackage) { p.ProjectID = "different-project" })
	_, err := service.Submit(context.Background(), SubmitRequest{Intent: "review-project", Task: task, Confirmed: true})
	submits, _ := adapter.Counters()
	if err == nil || submits != 0 {
		t.Fatalf("mismatched project was dispatched: err=%v submits=%d", err, submits)
	}
}
