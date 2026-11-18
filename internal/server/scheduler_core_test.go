package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"enhancedkimi/internal/desktop"
	"enhancedkimi/internal/plugins"
	"enhancedkimi/internal/scheduler"
	"enhancedkimi/internal/storage"
)

func TestSchedulerPluginGateAndUninstallCleanup(t *testing.T) {
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	home := t.TempDir()
	kap := desktop.NewKAPClient(home)
	fake := newFakeKAP(t)
	writeInstanceRecord(t, home, fake.server.URL)

	service, err := desktop.NewNativeService(db, kap, root, func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	var manager *plugins.Manager
	var engine *scheduler.Engine

	filesOfficial := map[string][]byte{
		"plugin.json": []byte(`{
  "id": "kimicode-plus.scheduler",
  "name": "定时任务",
  "version": "1.0.0",
  "apiVersion": 2,
  "hostVersions": ["1.*"],
  "entry": {"renderer": "renderer.js"},
  "capabilities": ["schedule:manage", "sessions:read", "models:read"]
}`),
		"renderer.js": []byte("export function activate(){}\n"),
		"LICENSE":     []byte("MIT\n"),
	}
	bundles := map[string]map[string][]byte{"kimicode-plus.scheduler": filesOfficial}

	manager, err = plugins.New(
		db,
		func() bool { return false },
		plugins.WithDataDir(root),
		plugins.WithBundled(func() (map[string]map[string][]byte, error) { return bundles, nil }),
		plugins.WithOfficial(func() (map[string]string, error) {
			return map[string]string{"kimicode-plus.scheduler": plugins.ContentDigest(filesOfficial)}, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	manager.UseCore(pluginCore{service: service})

	store, err := scheduler.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	engine, err = scheduler.NewEngine(
		store,
		service.NewSchedulerExecutor(func(ctx context.Context, pluginID, detail string) {
			manager.RecordActivity(ctx, pluginID, plugins.ActivitySession, detail)
		}),
		scheduler.WithDefaultCWD(root),
		scheduler.WithPluginEnabled(func(id string) bool {
			p, ok := manager.Get(id)
			return ok && p.Enabled
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	service.UseScheduler(engine)
	manager.UseStopPluginWork(func(ctx context.Context, id string) error {
		return engine.StopPlugin(ctx, id)
	})
	manager.UseOnUninstall(func(ctx context.Context, id string, deleteData bool) error {
		if deleteData {
			return engine.DeletePluginData(ctx, id)
		}
		return engine.StopPlugin(ctx, id)
	})

	if err := manager.ReleaseBundled(context.Background(), bundles); err != nil {
		t.Fatal(err)
	}
	// Bundled scheduler must start disabled.
	if item, ok := manager.Get("kimicode-plus.scheduler"); !ok || item.Enabled {
		t.Fatalf("bundled scheduler should start disabled, got %+v", item)
	}
	enabled, err := manager.Enable(context.Background(), "kimicode-plus.scheduler")
	if err != nil {
		t.Fatal(err)
	}

	code, body := callRuntime(t, manager, "schedule.save", enabled.Token, map[string]any{
		"name":           "每日检查",
		"prompt":         "请汇总今日变更",
		"cwd":            root,
		"mode":           "new_session",
		"permissionMode": "manual",
		"schedule":       map[string]any{"kind": "daily", "time": "09:00"},
		"missedPolicy":   "catchup",
	})
	if code != http.StatusOK || !strings.Contains(string(body), "每日检查") {
		t.Fatalf("schedule.save => %d %s", code, body)
	}

	// Uninstall with deleteData=true removes scheduled tasks and runs.
	mux := http.NewServeMux()
	manager.Register(mux)
	req := httptest.NewRequest(http.MethodPost, "/enhance/v1/plugins/kimicode-plus.scheduler/uninstall", strings.NewReader(`{"deleteData":true}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("uninstall => %d %s", rec.Code, rec.Body.String())
	}
	tasks, err := store.ListTasks(context.Background(), "kimicode-plus.scheduler")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected tasks cleared on uninstall with deleteData=true, got %+v", tasks)
	}

	// Non-privileged plugin without schedule:manage is refused.
	otherManager, _, plainToken := newPluginCoreFixture(t, []string{"sessions:read"})
	code, body = callRuntime(t, otherManager, "schedule.list", plainToken, nil)
	if code != http.StatusForbidden || !strings.Contains(string(body), "PLUGIN_CAPABILITY_DENIED") {
		t.Fatalf("expected PLUGIN_CAPABILITY_DENIED, got %d %s", code, body)
	}
	_ = json.RawMessage(nil)
}
