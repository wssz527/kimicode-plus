package plugins

import (
	"net/http"
	"strings"
	"testing"
)

func TestBrowserNavigationControlsKeepCoreOwnership(t *testing.T) {
	manager, token := officialFixture(t, []string{CapBrowserView})
	if code, value := callPlugin(t, manager, token, "browser.open", map[string]any{
		"viewId": "main", "url": "https://example.com/start",
	}); code != http.StatusOK {
		t.Fatalf("browser.open => %d %#v", code, value)
	}
	for _, action := range []string{"browser.back", "browser.forward", "browser.reload", "browser.openExternal"} {
		code, value := callPlugin(t, manager, token, action, map[string]any{"viewId": "main"})
		if code != http.StatusOK || value["value"].(map[string]any)["pluginId"] != "test.official" {
			t.Fatalf("%s => %d %#v", action, code, value)
		}
	}
	if got := manager.Views("test.official")[0].URL; got != "https://example.com/start" {
		t.Fatalf("a navigation control invented a page URL: %q", got)
	}
	for _, action := range []string{"browser.back", "browser.forward", "browser.reload", "browser.openExternal"} {
		if code, _ := callPlugin(t, manager, token, action, map[string]any{"viewId": "missing"}); code != http.StatusNotFound {
			t.Fatalf("%s reached a view not owned by this plugin: %d", action, code)
		}
	}
}

func TestOpenSessionAuthorizesTheSessionBeforeNamingTheHostTarget(t *testing.T) {
	core := &pluginCoreDouble{}
	manager, token := officialManager(t, core, []string{CapSessionsRead})
	code, value := callPlugin(t, manager, token, "sessions.open", map[string]any{"id": "session-1"})
	if code != http.StatusOK || value["value"].(map[string]any)["id"] != "session-1" {
		t.Fatalf("sessions.open => %d %#v", code, value)
	}
	if last := core.last(); last.action != "sessions.view" || last.payload["id"] != "session-1" {
		t.Fatalf("the session was not checked before opening: %#v", last)
	}
	before := len(core.calls)
	if code, _ := callPlugin(t, manager, token, "sessions.open", map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("missing session id => %d", code)
	}
	if len(core.calls) != before {
		t.Fatal("an invalid session-open request reached the core")
	}
	managerWithoutGrant, tokenWithoutGrant := officialFixture(t, []string{CapSessionsHistory})
	if code, value := callPlugin(t, managerWithoutGrant, tokenWithoutGrant, "sessions.open", map[string]any{"id": "session-1"}); code != http.StatusForbidden || !strings.Contains(mustString(value), "PLUGIN_CAPABILITY_DENIED") {
		t.Fatalf("sessions.open without sessions:read => %d %#v", code, value)
	}
}

func TestPluginWindowUsesOnlyTheAuthorizedIdentity(t *testing.T) {
	manager, token := officialFixture(t, []string{CapUIRightPanel})
	code, value := callPlugin(t, manager, token, "ui.popout", map[string]any{"pluginId": "test.other"})
	if code != http.StatusOK || value["value"].(map[string]any)["pluginId"] != "test.official" {
		t.Fatalf("ui.popout trusted a caller-supplied identity: %d %#v", code, value)
	}
	managerWithoutGrant, tokenWithoutGrant := officialFixture(t, []string{CapBrowserView})
	if code, _ := callPlugin(t, managerWithoutGrant, tokenWithoutGrant, "ui.popout", nil); code != http.StatusForbidden {
		t.Fatalf("ui.popout without ui:right-panel => %d", code)
	}
}

func TestBrowserViewsIsolateByWindowInstanceID(t *testing.T) {
	manager, token := officialFixture(t, []string{CapBrowserView})
	if code, _ := callPlugin(t, manager, token, "browser.open", map[string]any{
		"viewId": "web", "instanceId": "", "url": "https://example.com/",
	}); code != http.StatusBadRequest {
		t.Fatalf("missing instanceId should fail with 400, got %d", code)
	}
	if code, _ := callPlugin(t, manager, token, "browser.open", map[string]any{
		"viewId": "web", "instanceId": "host-win", "url": "https://example.com/host",
	}); code != http.StatusOK {
		t.Fatalf("host open failed: %d", code)
	}
	if code, _ := callPlugin(t, manager, token, "browser.open", map[string]any{
		"viewId": "web", "instanceId": "popup-win", "url": "https://example.com/popup",
	}); code != http.StatusOK {
		t.Fatalf("popup open failed: %d", code)
	}
	if views := manager.Views("test.official"); len(views) != 2 {
		t.Fatalf("expected 2 isolated views, got %#v", views)
	}
	if code, _ := callPlugin(t, manager, token, "browser.close", map[string]any{
		"viewId": "web", "instanceId": "popup-win",
	}); code != http.StatusOK {
		t.Fatalf("popup close failed: %d", code)
	}
	views := manager.Views("test.official")
	if len(views) != 1 || views[0].InstanceID != "host-win" {
		t.Fatalf("closing popup view affected host view: %#v", views)
	}
}

func TestSessionDiffRequiresSessionsReadAndValidID(t *testing.T) {
	core := &pluginCoreDouble{}
	manager, token := officialManager(t, core, []string{CapSessionsRead})
	code, value := callPlugin(t, manager, token, "sessions.diff", map[string]any{"id": "session-1", "extra": "drop-me"})
	if code != http.StatusOK {
		t.Fatalf("sessions.diff => %d %#v", code, value)
	}
	if last := core.last(); last.action != "sessions.diff" || last.payload["id"] != "session-1" || last.payload["extra"] != nil {
		t.Fatalf("unexpected forwarded payload: %#v", last)
	}
	if code, _ := callPlugin(t, manager, token, "sessions.diff", map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("missing session id => %d", code)
	}
	managerWithoutGrant, tokenWithoutGrant := officialFixture(t, []string{CapSessionsHistory})
	if code, value := callPlugin(t, managerWithoutGrant, tokenWithoutGrant, "sessions.diff", map[string]any{"id": "session-1"}); code != http.StatusForbidden || !strings.Contains(mustString(value), "PLUGIN_CAPABILITY_DENIED") {
		t.Fatalf("sessions.diff without sessions:read => %d %#v", code, value)
	}
}
