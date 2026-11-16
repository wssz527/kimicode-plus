package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"enhancedkimi/internal/storage"
)

func newManager(t *testing.T, safe func() bool) (*Manager, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugins.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	manager, err := New(db, safe)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	t.Cleanup(func() { manager.Close() })
	return manager, path
}

func pluginByID(t *testing.T, manager *Manager, id string) Plugin {
	t.Helper()
	for _, plugin := range manager.List() {
		if plugin.ID == id {
			return plugin
		}
	}
	t.Fatalf("plugin %s missing", id)
	return Plugin{}
}

func TestOnlyBuiltinSimulatedPluginsExist(t *testing.T) {
	manager, _ := newManager(t, nil)
	plugins := manager.List()
	if len(plugins) != 3 {
		t.Fatalf("expected three builtins, got %d", len(plugins))
	}
	ids := map[string]bool{}
	for _, plugin := range plugins {
		if !plugin.Builtin {
			t.Fatalf("non builtin plugin exposed: %#v", plugin)
		}
		if plugin.Sandbox != "IN_PROCESS_SIMULATION" || plugin.Controlled != "SIMULATED_ONLY" {
			t.Fatalf("plugin must be marked simulated: %#v", plugin)
		}
		if plugin.Resources != 0 || plugin.Enabled {
			t.Fatalf("plugins must not be running by default: %#v", plugin)
		}
		ids[plugin.ID] = true
	}
	for _, want := range []string{"fault-demo", "kct", "monitor"} {
		if !ids[want] {
			t.Fatalf("missing builtin %s", want)
		}
	}
	_, err := manager.Enable("../evil")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "PLUGIN_NOT_FOUND" {
		t.Fatalf("unknown plugin must be refused, got %v", err)
	}
}

func TestEnableIsIdempotentAndReleasesResources(t *testing.T) {
	manager, _ := newManager(t, nil)
	plugin, err := manager.Enable("kct")
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if plugin.State != StateEnabled || plugin.Resources != 3 {
		t.Fatalf("unexpected plugin state: %#v", plugin)
	}
	again, err := manager.Enable("kct")
	if err != nil {
		t.Fatalf("repeat enable: %v", err)
	}
	if again.Resources != 3 {
		t.Fatalf("repeat enable duplicated resources: %d", again.Resources)
	}
	if err := manager.Disable("kct"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := manager.Disable("kct"); err != nil {
		t.Fatalf("repeat disable: %v", err)
	}
	after := pluginByID(t, manager, "kct")
	if after.Enabled || after.Resources != 0 {
		t.Fatalf("resources not released: %#v", after)
	}
}

func TestConcurrentEnableStartsOnce(t *testing.T) {
	manager, _ := newManager(t, nil)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := manager.Enable("monitor"); err != nil {
				t.Errorf("enable: %v", err)
			}
		}()
	}
	wg.Wait()
	plugin := pluginByID(t, manager, "monitor")
	if plugin.Resources != 3 {
		t.Fatalf("concurrent enable duplicated resources: %d", plugin.Resources)
	}
	health := manager.Health(context.Background())
	if health["resources"] != 3 {
		t.Fatalf("health resources wrong: %#v", health)
	}
}

func TestSafeModeBlocksActivationButKeepsState(t *testing.T) {
	safe := func() bool { return true }
	manager, _ := newManager(t, safe)
	_, err := manager.Enable("kct")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "SAFE_MODE" {
		t.Fatalf("expected SAFE_MODE, got %v", err)
	}
	plugin := pluginByID(t, manager, "kct")
	if plugin.State != StateBlocked || plugin.Resources != 0 {
		t.Fatalf("safe mode plugin state wrong: %#v", plugin)
	}
	health := manager.Health(context.Background())
	if health["safeMode"] != true {
		t.Fatalf("health must report safe mode")
	}
}

func TestFaultIsolatedToOffender(t *testing.T) {
	manager, _ := newManager(t, nil)
	if _, err := manager.Enable("kct"); err != nil {
		t.Fatalf("enable kct: %v", err)
	}
	if _, err := manager.Enable("fault-demo"); err != nil {
		t.Fatalf("enable fault-demo: %v", err)
	}
	_, err := manager.Fault("fault-demo")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "PLUGIN_FAULTED" {
		t.Fatalf("expected PLUGIN_FAULTED, got %v", err)
	}
	faulted := pluginByID(t, manager, "fault-demo")
	if faulted.State != StateFaulted || faulted.Resources != 0 || faulted.Faults != 1 {
		t.Fatalf("faulted plugin state wrong: %#v", faulted)
	}
	healthy := pluginByID(t, manager, "kct")
	if !healthy.Enabled || healthy.Resources != 3 {
		t.Fatalf("healthy plugin disturbed: %#v", healthy)
	}
	health := manager.Health(context.Background())
	if health["faulted"] != 1 || health["enabled"] != 1 {
		t.Fatalf("unexpected health: %#v", health)
	}
}

func TestFaultRequiresEnabledPlugin(t *testing.T) {
	manager, _ := newManager(t, nil)
	_, err := manager.Fault("kct")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "PLUGIN_NOT_ENABLED" {
		t.Fatalf("expected PLUGIN_NOT_ENABLED, got %v", err)
	}
}

func TestPanickingActivationCleansPartialResources(t *testing.T) {
	manager, _ := newManager(t, nil)
	manager.defs["panic-plugin"] = &definition{
		descriptor: Descriptor{ID: "panic-plugin", Name: "panic", Version: "1", Builtin: true, Sandbox: "IN_PROCESS_SIMULATION"},
		activate: func(scope *Scope) error {
			scope.Own("partial", func() error { return nil })
			panic("activation exploded")
		},
	}
	manager.order = append(manager.order, "panic-plugin")
	_, err := manager.Enable("panic-plugin")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "PLUGIN_ACTIVATION_FAILED" {
		t.Fatalf("expected activation failure, got %v", err)
	}
	plugin := pluginByID(t, manager, "panic-plugin")
	if plugin.State != StateFaulted || plugin.Resources != 0 {
		t.Fatalf("panic left resources: %#v", plugin)
	}
	if _, err := manager.Enable("kct"); err != nil {
		t.Fatalf("manager unusable after panic: %v", err)
	}
}

func TestThrowingCleanupDoesNotBlockOthers(t *testing.T) {
	manager, _ := newManager(t, nil)
	manager.defs["bad-cleanup"] = &definition{
		descriptor: Descriptor{ID: "bad-cleanup", Name: "bad", Version: "1", Builtin: true, Sandbox: "IN_PROCESS_SIMULATION"},
		activate: func(scope *Scope) error {
			scope.Own("timer", func() error { return nil })
			scope.Own("boom", func() error { return errors.New("dispose failed") })
			return nil
		},
	}
	manager.order = append(manager.order, "bad-cleanup")
	if _, err := manager.Enable("bad-cleanup"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := manager.Disable("bad-cleanup"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	plugin := pluginByID(t, manager, "bad-cleanup")
	if plugin.Resources != 0 || plugin.Enabled {
		t.Fatalf("cleanup incomplete: %#v", plugin)
	}
	if !strings.Contains(plugin.Error, "dispose failed") {
		t.Fatalf("cleanup failure not recorded: %#v", plugin)
	}
}

func TestStatePersistsButDoesNotAutoStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugins.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	manager, err := New(db, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, err := manager.Enable("monitor"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	// Simulate a crash: the enabled flag survives, but the process resources do not.
	if _, err := db.Exec(`UPDATE plugin_states SET enabled=1,state='ENABLED' WHERE id='monitor'`); err != nil {
		t.Fatalf("simulate crash: %v", err)
	}
	// Crash: no Close, so only the persisted enabled flag remains.
	db.Close()

	reopen, err := storage.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopen.Close()
	restored, err := New(reopen, nil)
	if err != nil {
		t.Fatalf("new after reopen: %v", err)
	}
	defer restored.Close()
	plugin := pluginByID(t, restored, "monitor")
	if plugin.Enabled || plugin.Resources != 0 {
		t.Fatalf("plugin auto-started after restart: %#v", plugin)
	}
	if plugin.State != StateBlocked || !strings.Contains(plugin.Error, "不会") {
		t.Fatalf("persisted enabled state not surfaced: %#v", plugin)
	}
	if health := restored.Health(context.Background()); health["processes"] != 0 {
		t.Fatalf("unexpected plugin processes: %#v", health)
	}
}

func TestFaultStatePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugins.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	manager, err := New(db, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, err := manager.Enable("fault-demo"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, err := manager.Fault("fault-demo"); err == nil {
		t.Fatalf("expected fault")
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	db.Close()

	reopen, err := storage.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopen.Close()
	restored, err := New(reopen, nil)
	if err != nil {
		t.Fatalf("new after reopen: %v", err)
	}
	defer restored.Close()
	plugin := pluginByID(t, restored, "fault-demo")
	if plugin.State != StateFaulted || plugin.Faults != 1 {
		t.Fatalf("fault state lost: %#v", plugin)
	}
}

func TestCloseReleasesResourcesAndRejectsRestart(t *testing.T) {
	manager, _ := newManager(t, nil)
	if _, err := manager.Enable("kct"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := manager.Enable("kct"); err == nil {
		t.Fatalf("enable after close must fail")
	}
}

func TestHTTPActionsAndFullPrefix(t *testing.T) {
	manager, _ := newManager(t, nil)
	mux := http.NewServeMux()
	manager.Register(mux)

	list := httptest.NewRecorder()
	mux.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/enhance/v1/plugins", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status %d", list.Code)
	}
	var listing struct {
		Items  []Plugin `json:"items"`
		Total  int      `json:"total"`
		Notice string   `json:"notice"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if listing.Total != 3 || len(listing.Items) != 3 {
		t.Fatalf("unexpected listing: %#v", listing)
	}
	if !strings.Contains(listing.Notice, "不是操作系统沙箱") {
		t.Fatalf("notice must state this is not an OS sandbox: %q", listing.Notice)
	}

	enable := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/enhance/v1/plugins/kct", strings.NewReader(`{"action":"ENABLE"}`))
	mux.ServeHTTP(enable, request)
	if enable.Code != http.StatusOK {
		t.Fatalf("enable status %d: %s", enable.Code, enable.Body.String())
	}
	var enabled Plugin
	if err := json.Unmarshal(enable.Body.Bytes(), &enabled); err != nil {
		t.Fatalf("decode enable: %v", err)
	}
	if !enabled.Enabled || enabled.Resources != 3 {
		t.Fatalf("unexpected enabled plugin: %#v", enabled)
	}

	bad := httptest.NewRecorder()
	mux.ServeHTTP(bad, httptest.NewRequest(http.MethodPost, "/enhance/v1/plugins/kct", strings.NewReader(`{"action":"INSTALL"}`)))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid action status %d", bad.Code)
	}

	missing := httptest.NewRecorder()
	mux.ServeHTTP(missing, httptest.NewRequest(http.MethodPost, "/enhance/v1/plugins/nope", strings.NewReader(`{"action":"ENABLE"}`)))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing plugin status %d", missing.Code)
	}
}

func TestSafeModeViaManagerAfterEnable(t *testing.T) {
	var safeMu sync.Mutex
	safeValue := false
	safe := func() bool {
		safeMu.Lock()
		defer safeMu.Unlock()
		return safeValue
	}
	manager, _ := newManager(t, safe)
	if _, err := manager.Enable("kct"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	safeMu.Lock()
	safeValue = true
	safeMu.Unlock()
	if _, err := manager.Enable("monitor"); err == nil {
		t.Fatalf("safe mode must reject enable")
	}
	plugin := pluginByID(t, manager, "kct")
	if plugin.Enabled || plugin.Resources != 0 || plugin.State != StateBlocked {
		t.Fatalf("safe mode must stop running plugins: %#v", plugin)
	}
}
