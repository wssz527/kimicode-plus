package desktop

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"enhancedkimi/internal/updater"
)

func TestCoreUpdateNativeActions(t *testing.T) {
	service := &NativeService{}
	if _, err := service.Action(t.Context(), "update.status", nil); err == nil || !strings.Contains(err.Error(), "尚未启用") {
		t.Fatalf("missing updater was not handled: %v", err)
	}
	t.Setenv("EKD_UPDATE_REPO", "example/releases")
	github := httptest.NewServer(http.NotFoundHandler())
	defer github.Close()
	safe := false
	manager := updater.New(updater.Options{
		CurrentVersion: "0.2.0", APIBaseURL: github.URL, AllowInsecureHTTP: true,
		Safe: func() bool { return safe },
	})
	defer manager.Close()
	service.UseUpdater(manager)
	value, err := service.Action(t.Context(), "update.status", nil)
	if err != nil || value.(updater.Status).State != updater.Idle {
		t.Fatalf("native status: %+v, %v", value, err)
	}
	value, err = service.Action(t.Context(), "update.check", nil)
	if err != nil || value.(updater.Status).State != updater.UpToDate || manager.Status().CheckedAt == "" {
		t.Fatalf("native check: %+v, %v", value, err)
	}
	if _, err = service.Action(t.Context(), "update.apply", nil); !errors.Is(err, updater.ErrUnavailable) {
		t.Fatalf("native apply without update: %v", err)
	}
	safe = true
	if _, err = service.Action(t.Context(), "update.apply", nil); !errors.Is(err, updater.ErrSafeMode) {
		t.Fatalf("native apply in safe mode: %v", err)
	}
}
