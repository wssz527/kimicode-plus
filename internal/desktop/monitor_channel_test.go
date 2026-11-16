package desktop

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestMonitorSummaryActionIsGone is the D5/D10 regression for M4.
//
// The sidebar widget used to be a core surface fed by a private `monitor.summary`
// action. After M4 it is the preinstalled Monitor plugin, which reads
// cache.summary/sessions/history/receipts through its own granted capability.
// Keeping the old action around would leave a private channel in the core, so a
// call must now be refused like any other unknown action.
func TestMonitorSummaryActionIsGone(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/oauth/usage" {
			writeNativeTestEnvelope(w, map[string]any{"kind": "ok", "quota": map[string]any{"usages": map[string]any{
				"limit5h": map[string]any{"usedRatio": 0.25, "resetAt": "2026-11-16T20:00:00Z"},
			}}})
			return
		}
		writeNativeTestEnvelope(w, map[string]any{"items": []any{}})
	})
	service, _ := newAutomationTestServiceWithHome(t, handler, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	value, err := service.Action(ctx, "monitor.summary", nil)
	if err == nil {
		t.Fatalf("monitor.summary still answered: %#v", value)
	}
	if !strings.Contains(err.Error(), "未知或未经允许的宿主动作") {
		t.Fatalf("unexpected refusal: %v", err)
	}
	// The replacement actions must keep working, so removing the old one did not
	// take the data source with it.
	summary, err := service.Action(ctx, "cache.summary", nil)
	if err != nil {
		t.Fatalf("cache.summary broke: %v", err)
	}
	if summary == nil {
		t.Fatal("cache.summary returned nothing")
	}
}
