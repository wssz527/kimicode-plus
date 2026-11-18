package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"enhancedkimi/internal/storage"
)

func newAddonFeatureService(t *testing.T) (*NativeService, func(t *testing.T) *NativeService) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeNativeTestEnvelope(w, map[string]any{"items": []any{}})
	}))
	kap := NewKAPClient("")
	kap.base, kap.token = server.URL, "synthetic"
	path := filepath.Join(t.TempDir(), "features.db")
	db, err := storage.Open(path)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	service, err := NewNativeService(db, kap, t.TempDir(), func() bool { return false })
	if err != nil {
		server.Close()
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		service.Close()
		db.Close()
		server.Close()
	})
	reopen := func(t *testing.T) *NativeService {
		t.Helper()
		nextDB, err := storage.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		restarted, err := NewNativeService(nextDB, kap, t.TempDir(), func() bool { return false })
		if err != nil {
			nextDB.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() {
			restarted.Close()
			nextDB.Close()
		})
		return restarted
	}
	return service, reopen
}

func addonFeaturesOf(t *testing.T, service *NativeService) AddonFeatureState {
	t.Helper()
	value, err := service.Action(context.Background(), "features.get", nil)
	if err != nil {
		t.Fatal(err)
	}
	state, ok := value.(AddonFeatureState)
	if !ok {
		t.Fatalf("features.get returned %T", value)
	}
	return state
}

func TestAddonFeaturesDefaultOff(t *testing.T) {
	service, _ := newAddonFeatureService(t)
	if state := addonFeaturesOf(t, service); state.Planner || state.MultiSession {
		t.Fatalf("fresh database enabled addons: %+v", state)
	}
	var rows int
	if err := service.db.QueryRow(`SELECT COUNT(*) FROM native_state WHERE key='features'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatal("reading defaults must not write a features row")
	}
}

func TestAddonFeaturesPersistAcrossRestart(t *testing.T) {
	service, reopen := newAddonFeatureService(t)
	value, err := service.Action(context.Background(), "features.set", json.RawMessage(`{"planner":true,"multiSession":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if state := value.(AddonFeatureState); !state.Planner || state.MultiSession {
		t.Fatalf("features.set stored %+v", state)
	}
	restarted := reopen(t)
	if state := addonFeaturesOf(t, restarted); !state.Planner || state.MultiSession {
		t.Fatalf("restart lost persisted features: %+v", state)
	}
	// A later write must be able to drop the feature again, which is what the
	// settings toggle and the renderer's unmount path both rely on.
	if _, err = restarted.Action(context.Background(), "features.set", json.RawMessage(`{"planner":false,"multiSession":false}`)); err != nil {
		t.Fatal(err)
	}
	if state := addonFeaturesOf(t, reopen(t)); state.Planner || state.MultiSession {
		t.Fatalf("disabling did not persist: %+v", state)
	}
}

func TestAddonFeaturesRejectInvalidPayload(t *testing.T) {
	service, _ := newAddonFeatureService(t)
	if _, err := service.Action(context.Background(), "features.set", json.RawMessage(`[]`)); err == nil {
		t.Fatal("array payload accepted as feature state")
	}
	if state := addonFeaturesOf(t, service); state.Planner || state.MultiSession {
		t.Fatal("rejected payload changed stored state")
	}
}

func TestAddonFeaturesUnreadableRowNeverEnablesAddons(t *testing.T) {
	service, _ := newAddonFeatureService(t)
	if _, err := service.db.Exec(`INSERT INTO native_state(key,data) VALUES('features','{')`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Action(context.Background(), "features.get", nil); err == nil {
		t.Fatal("corrupt feature row was silently accepted")
	}
	if state, err := service.addonFeatures(context.Background()); err == nil || state.Planner || state.MultiSession {
		t.Fatalf("corrupt row must fail closed: %+v %v", state, err)
	}
}

func TestAddonFeaturesGatePlannerActions(t *testing.T) {
	service, _ := newAddonFeatureService(t)
	planner := &recoveryPlannerStub{}
	service.UsePlanner(planner)
	_, err := service.Action(context.Background(), "planner.retry", json.RawMessage(`{"id":"p1"}`))
	if err == nil || !strings.Contains(err.Error(), "附加功能") {
		t.Fatalf("closed planner still dispatched: %v", err)
	}
	if planner.retries.Load() != 0 {
		t.Fatal("closed planner reached the planner backend")
	}
	if _, err = service.Action(context.Background(), "features.set", json.RawMessage(`{"planner":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Action(context.Background(), "planner.retry", json.RawMessage(`{"id":"p1"}`)); err != nil {
		t.Fatal(err)
	}
	if planner.retries.Load() != 1 {
		t.Fatalf("enabled planner retries = %d", planner.retries.Load())
	}
}

func TestAddonFeaturesBootstrapHidesPlannerThreads(t *testing.T) {
	service, _ := newAddonFeatureService(t)
	service.UsePlanner(&recoveryPlannerStub{thread: plannerRecoveryThread{ID: "p1", Title: "合成规划"}})
	closed, err := service.bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if threads, ok := closed["threads"].([]any); !ok || len(threads) != 0 {
		t.Fatalf("closed planner still published threads: %#v", closed["threads"])
	}
	if state, ok := closed["features"].(AddonFeatureState); !ok || state.Planner {
		t.Fatalf("bootstrap features = %#v", closed["features"])
	}
	if _, err = service.Action(context.Background(), "features.set", json.RawMessage(`{"planner":true}`)); err != nil {
		t.Fatal(err)
	}
	opened, err := service.bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if threads, ok := opened["threads"].([]plannerRecoveryThread); !ok || len(threads) != 1 {
		t.Fatalf("enabled planner did not publish threads: %#v", opened["threads"])
	}
}

func TestAddonFeaturesPublishOnlyOnChange(t *testing.T) {
	service, _ := newAddonFeatureService(t)
	poll := func() []Event {
		return service.bus.Poll(context.Background(), 0, []string{"features"}, 0).Events
	}
	if _, err := service.Action(context.Background(), "features.set", json.RawMessage(`{"planner":false}`)); err != nil {
		t.Fatal(err)
	}
	if events := poll(); len(events) != 0 {
		t.Fatalf("unchanged state published %d events", len(events))
	}
	if _, err := service.Action(context.Background(), "features.set", json.RawMessage(`{"planner":true}`)); err != nil {
		t.Fatal(err)
	}
	events := poll()
	if len(events) != 1 {
		t.Fatalf("feature change published %d events", len(events))
	}
	if state, ok := events[0].Value.(AddonFeatureState); !ok || !state.Planner {
		t.Fatalf("feature event value = %#v", events[0].Value)
	}
}

func TestRendererFreezesAddonEntries(t *testing.T) {
	for _, removed := range []string{"state.features.planner", "openPlanner", "ekd-planner-host", "ekdPlannerTab"} {
		if strings.Contains(rendererSource, removed) {
			t.Fatalf("renderer.js still exposes the replaced planner: %s", removed)
		}
	}
	for _, wanted := range []string{
		"if (state.features.multiSession) plan.push({ action: 'split', label: '添加同屏会话' })",
		"if (state.sessionContainer) closeSessions();",
		"state.unsubscribeFeatures = makeHost(() => {}).subscribe('features'",
	} {
		if !strings.Contains(rendererSource, wanted) {
			t.Fatalf("renderer.js lost addon freeze guard: %s", wanted)
		}
	}
	// The sidebar footer must not carry a cloned KimiCode+ settings entry; the
	// only settings entry is the tab inside the official settings dialog.
	if strings.Contains(rendererSource, "dataset.ekdEntry = 'settings'") ||
		strings.Contains(rendererSource, "setEntryLabel(button, 'KimiCode+ 设置')") {
		t.Fatal("renderer.js still clones a sidebar KimiCode+ settings entry")
	}
	if !strings.Contains(rendererSource, "button.dataset.ekdSettingsTab = ''") {
		t.Fatal("renderer.js no longer injects the official settings tab")
	}
}

// TestSettingsMountOwnsItsScroll guards the fix for the enhanced settings page being
// clipped by the official dialog. The official body has a definite height and does not
// scroll, so the mount has to be clamped to it and scroll its own content; without all
// three declarations the tail of the page sits outside the dialog where neither the
// wheel nor scripted scrolling can reach it.
func TestSettingsMountOwnsItsScroll(t *testing.T) {
	start := strings.Index(rendererSource, ".ekd-settings-mount{")
	if start < 0 {
		t.Fatal("renderer.js lost the settings mount style")
	}
	end := strings.Index(rendererSource[start:], "}")
	if end < 0 {
		t.Fatal("settings mount style is not terminated")
	}
	rule := rendererSource[start : start+end]
	for _, wanted := range []string{"overflow-y:auto", "max-height:100%", "flex:1 1 auto"} {
		if !strings.Contains(rule, wanted) {
			t.Fatalf("settings mount style lost %q: %s", wanted, rule)
		}
	}
	if strings.Contains(rule, "flex:none") {
		t.Fatalf("settings mount is unconstrained again: %s", rule)
	}
}
