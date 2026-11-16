package plugins

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"enhancedkimi/internal/storage"
)

// reopen closes the manager and builds a second one over the same database and
// data directory. Restart behaviour is only meaningful across a real reopen: the
// in-memory entry would keep a decision that a fresh process has to reload.
func reopen(t *testing.T, current *Manager, root string, options ...Option) *Manager {
	t.Helper()
	if err := current.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	manager, err := newManagerAt(t, root, options...)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { manager.Close() })
	return manager
}

func newManagerAt(t *testing.T, root string, options ...Option) (*Manager, error) {
	t.Helper()
	db, err := storage.Open(filepath.Join(root, "plugins.db"))
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { db.Close() })
	return New(db, func() bool { return false }, append([]Option{WithDataDir(root)}, options...)...)
}

func TestEnableDecisionSurvivesShutdown(t *testing.T) {
	root := t.TempDir()
	manager, err := newManagerAt(t, root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	source := writePluginDir(t, filepath.Join(t.TempDir(), "source"), testManifest, nil)
	if _, err := manager.Install(ctx, InstallRequest{Root: source, Capabilities: confirmAll(t, source)}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Enable(ctx, "test.sample"); err != nil {
		t.Fatal(err)
	}

	reopened := reopen(t, manager, root)
	plugin, ok := reopened.Get("test.sample")
	if !ok {
		t.Fatal("plugin was not loaded from its record")
	}
	if !plugin.Enabled || plugin.State != StateEnabled {
		t.Fatalf("enable decision was lost across restart: %#v", plugin)
	}
	if !plugin.Desired {
		t.Fatal("desired flag was not restored")
	}
	// Nothing is running yet: a restart must not hand out a token without the
	// renderer asking for one.
	if plugin.Token != "" {
		t.Fatal("a reopened manager issued a token before the renderer asked")
	}
	token, err := reopened.Token("test.sample")
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if token == "" {
		t.Fatal("reopened manager did not issue a token on demand")
	}
}

func TestDisableDecisionSurvivesShutdown(t *testing.T) {
	root := t.TempDir()
	manager, err := newManagerAt(t, root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	source := writePluginDir(t, filepath.Join(t.TempDir(), "source"), testManifest, nil)
	if _, err := manager.Install(ctx, InstallRequest{Root: source, Capabilities: confirmAll(t, source)}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Enable(ctx, "test.sample"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Disable(ctx, "test.sample"); err != nil {
		t.Fatal(err)
	}
	reopened := reopen(t, manager, root)
	plugin, _ := reopened.Get("test.sample")
	if plugin.Enabled || plugin.Desired || plugin.State != StateDisabled {
		t.Fatalf("a disabled plugin came back enabled: %#v", plugin)
	}
}

// TestNarrowedGrantSurvivesReleaseBundled covers the whole chain the tower asked
// for: the user narrows the grant, enables the plugin, the App shuts down, and
// the next start releases the bundled copy again. The narrowed grant and the
// enable decision must both still be there afterwards.
func TestNarrowedGrantSurvivesReleaseBundled(t *testing.T) {
	root := t.TempDir()
	bundled := map[string]map[string][]byte{
		"test.sample": {
			"plugin.json": []byte(testManifest),
			"renderer.js": []byte("export function activate(){}\n"),
			"LICENSE":     []byte("MIT\n"),
		},
	}
	manager, err := newManagerAt(t, root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := manager.ReleaseBundled(ctx, bundled); err != nil {
		t.Fatal(err)
	}
	// The user reinstalls the same plugin and accepts only the sidebar, not storage.
	narrowed := []string{CapUISidebar}
	source := writePluginDir(t, filepath.Join(t.TempDir(), "source"), testManifest, nil)
	if _, err := manager.Install(ctx, InstallRequest{Root: source, Capabilities: &narrowed}); err != nil {
		t.Fatal(err)
	}
	installed, _ := manager.Get("test.sample")
	if len(installed.Capabilities) != 1 || installed.Capabilities[0] != CapUISidebar {
		t.Fatalf("narrowed grant was not stored: %#v", installed.Capabilities)
	}
	if _, err := manager.Enable(ctx, "test.sample"); err != nil {
		t.Fatal(err)
	}

	// A restart releases the same version again, which must adopt rather than
	// re-install, and must not re-widen what the user narrowed.
	reopened := reopen(t, manager, root)
	if err := reopened.ReleaseBundled(ctx, bundled); err != nil {
		t.Fatal(err)
	}
	plugin, ok := reopened.Get("test.sample")
	if !ok {
		t.Fatal("bundled release dropped the plugin")
	}
	if len(plugin.Capabilities) != 1 || plugin.Capabilities[0] != CapUISidebar {
		t.Fatalf("bundled release re-widened the grant: %#v", plugin.Capabilities)
	}
	if !plugin.Enabled || plugin.State != StateEnabled {
		t.Fatalf("bundled release dropped the enable decision: %#v", plugin)
	}
	if len(plugin.Declared) != 2 {
		t.Fatalf("declared capabilities should still come from the manifest: %#v", plugin.Declared)
	}
}

// TestUpgradeKeepsNarrowedGrantAndEnableDecision covers the upgrade path, which
// replaces the files and therefore re-adopts the directory.
func TestUpgradeKeepsNarrowedGrantAndEnableDecision(t *testing.T) {
	root := t.TempDir()
	v1 := map[string]map[string][]byte{
		"test.sample": {
			"plugin.json": []byte(testManifest),
			"renderer.js": []byte("// v1\n"),
			"LICENSE":     []byte("MIT\n"),
		},
	}
	manager, err := newManagerAt(t, root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := manager.ReleaseBundled(ctx, v1); err != nil {
		t.Fatal(err)
	}
	narrowed := []string{CapUISidebar}
	source := writePluginDir(t, filepath.Join(t.TempDir(), "source"), testManifest, nil)
	if _, err := manager.Install(ctx, InstallRequest{Root: source, Capabilities: &narrowed}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Enable(ctx, "test.sample"); err != nil {
		t.Fatal(err)
	}

	v2Manifest := `{
  "id": "test.sample",
  "name": "Sample",
  "version": "1.1.0",
  "apiVersion": 1,
  "hostVersions": ["1.*"],
  "entry": {"renderer": "renderer.js"},
  "capabilities": ["ui:sidebar", "storage", "metrics:read"]
}`
	v2 := map[string]map[string][]byte{
		"test.sample": {
			"plugin.json": []byte(v2Manifest),
			"renderer.js": []byte("// v2\n"),
			"LICENSE":     []byte("MIT\n"),
		},
	}
	if err := manager.ReleaseBundled(ctx, v2); err != nil {
		t.Fatal(err)
	}

	plugin, _ := manager.Get("test.sample")
	if plugin.Version != "1.1.0" {
		t.Fatalf("upgrade did not replace the files: %s", plugin.Version)
	}
	if len(plugin.Capabilities) != 1 || plugin.Capabilities[0] != CapUISidebar {
		t.Fatalf("upgrade re-widened the grant: %#v", plugin.Capabilities)
	}
	if !plugin.Enabled || plugin.State != StateEnabled {
		t.Fatalf("upgrade dropped the enable decision: %#v", plugin)
	}
	data, err := os.ReadFile(filepath.Join(root, "plugins", "test.sample", "renderer.js"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "// v2\n" {
		t.Fatalf("upgrade did not install the new entry: %q", data)
	}

	// And the decision still survives a restart after the upgrade.
	reopened := reopen(t, manager, root)
	after, _ := reopened.Get("test.sample")
	if !after.Enabled || len(after.Capabilities) != 1 {
		t.Fatalf("upgrade result was not persisted: %#v", after)
	}
}

// TestRestoreBundledBringsBackAnUninstalledPlugin covers the explicit restore
// path: an uninstalled bundled plugin stays gone until the user asks for it.
func TestRestoreBundledBringsBackAnUninstalledPlugin(t *testing.T) {
	root := t.TempDir()
	manager, err := newManagerAt(t, root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	bundled := map[string]map[string][]byte{
		"test.sample": {
			"plugin.json": []byte(testManifest),
			"renderer.js": []byte("export function activate(){}\n"),
			"LICENSE":     []byte("MIT\n"),
		},
	}
	if err := manager.ReleaseBundled(ctx, bundled); err != nil {
		t.Fatal(err)
	}
	if err := manager.Uninstall(ctx, "test.sample"); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Get("test.sample"); ok {
		t.Fatal("uninstall left the plugin installed")
	}
	restored, err := manager.RestoreBundled(ctx, "test.sample", bundled["test.sample"])
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.ID != "test.sample" || restored.State != StateDisabled {
		t.Fatalf("restore did not produce a stopped install: %#v", restored)
	}
	if _, err := os.Stat(filepath.Join(root, "plugins", "test.sample", "plugin.json")); err != nil {
		t.Fatalf("restore did not write the files: %v", err)
	}
	// Restating the release must not resurrect it twice or fail.
	if err := manager.ReleaseBundled(ctx, bundled); err != nil {
		t.Fatalf("release after restore: %v", err)
	}
	if _, ok := manager.Get("test.sample"); !ok {
		t.Fatal("release after restore dropped the plugin")
	}
}

func TestForceEnableIsRequiredOutsideHostVersions(t *testing.T) {
	root := t.TempDir()
	manager, err := newManagerAt(t, root, WithHostVersion("2.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	source := writePluginDir(t, filepath.Join(t.TempDir(), "source"), testManifest, nil)
	if _, err := manager.Install(ctx, InstallRequest{Root: source, Capabilities: confirmAll(t, source)}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Enable(ctx, "test.sample"); err == nil {
		t.Fatal("enable succeeded outside hostVersions without confirmation")
	}
	plugin, err := manager.ForceEnable(ctx, "test.sample")
	if err != nil {
		t.Fatalf("force enable: %v", err)
	}
	if !plugin.Enabled || !plugin.Forced {
		t.Fatalf("force enable did not record the override: %#v", plugin)
	}
	reopened := reopen(t, manager, root, WithHostVersion("2.0.0"))
	after, _ := reopened.Get("test.sample")
	if !after.Enabled || !after.Forced {
		t.Fatalf("forced enable was not persisted: %#v", after)
	}
}

// TestConcurrentEnableAndActionIsRaceFree exercises the snapshot rule in
// runAction: an action may run while Enable/Disable rewrite the entry.
func TestConcurrentEnableAndActionIsRaceFree(t *testing.T) {
	root := t.TempDir()
	manager, err := newManagerAt(t, root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	source := writePluginDir(t, filepath.Join(t.TempDir(), "source"), testManifest, nil)
	if _, err := manager.Install(ctx, InstallRequest{Root: source, Capabilities: confirmAll(t, source)}); err != nil {
		t.Fatal(err)
	}
	enabled, err := manager.Enable(ctx, "test.sample")
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			for round := 0; round < 12; round++ {
				if index%2 == 0 {
					_, _ = manager.Enable(ctx, "test.sample")
					_ = manager.Disable(ctx, "test.sample")
					continue
				}
				_, _ = manager.Call(ctx, "test.sample", "runtime.info", enabled.Token, nil)
			}
		}(index)
	}
	wait.Wait()
}

// TestEnableAlreadyEnabledReturnsCurrentState pins the fast path: enabling a
// plugin that is already running returns the entry's current projection instead
// of re-issuing work. That projection has to be built under the manager lock —
// a concurrent Disable rewrites the same fields — so the returned value must
// reflect a coherent state rather than a mix of pre- and post-transition reads.
func TestEnableAlreadyEnabledReturnsCurrentState(t *testing.T) {
	manager, _ := testManager(t, func() bool { return false })
	ctx := context.Background()
	source := writePluginDir(t, filepath.Join(t.TempDir(), "source"), testManifest, nil)
	if _, err := manager.Install(ctx, InstallRequest{Root: source, Capabilities: confirmAll(t, source)}); err != nil {
		t.Fatal(err)
	}
	first, err := manager.Enable(ctx, "test.sample")
	if err != nil {
		t.Fatal(err)
	}
	again, err := manager.Enable(ctx, "test.sample")
	if err != nil {
		t.Fatal(err)
	}
	if again.State != StateEnabled || !again.Enabled || !again.Desired {
		t.Fatalf("re-enable did not report the running state: %#v", again)
	}
	if again.Token != "" {
		t.Fatalf("re-enable returned a token; only the first enable issues one: %#v", again)
	}
	if first.Token == "" {
		t.Fatal("the first enable issued no token")
	}
	if again.ID != first.ID || again.Version != first.Version {
		t.Fatalf("re-enable changed identity: %#v vs %#v", again, first)
	}
}

func TestReportErrorIsRecordedWithoutCore(t *testing.T) {
	manager, _ := testManager(t, func() bool { return false })
	ctx := context.Background()
	source := writePluginDir(t, filepath.Join(t.TempDir(), "source"), testManifest, nil)
	if _, err := manager.Install(ctx, InstallRequest{Root: source, Capabilities: confirmAll(t, source)}); err != nil {
		t.Fatal(err)
	}
	enabled, err := manager.Enable(ctx, "test.sample")
	if err != nil {
		t.Fatal(err)
	}
	value, err := manager.Call(ctx, "test.sample", "report.error", enabled.Token, nil)
	if err != nil {
		t.Fatalf("report.error should not need a core: %v", err)
	}
	if recorded, _ := value.(map[string]any)["recorded"].(bool); !recorded {
		t.Fatalf("report.error did not confirm the record: %#v", value)
	}
}

// A corrupt directory still carries the user's grant and intent forward: the
// repair path must not quietly re-widen what was agreed to.
func TestCorruptPluginKeepsGrantAndIntent(t *testing.T) {
	root := t.TempDir()
	manager, err := newManagerAt(t, root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	source := writePluginDir(t, filepath.Join(t.TempDir(), "source"), testManifest, nil)
	narrowed := []string{CapUISidebar}
	if _, err := manager.Install(ctx, InstallRequest{Root: source, Capabilities: &narrowed}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Enable(ctx, "test.sample"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "plugins", "test.sample", "renderer.js")); err != nil {
		t.Fatal(err)
	}
	if err := manager.adopt(ctx, "test.sample", filepath.Join(root, "plugins", "test.sample"), SourceLocal); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	broken, _ := manager.Get("test.sample")
	if broken.State != StateCorrupt {
		t.Fatalf("missing entry file was not marked corrupt: %#v", broken)
	}
	if len(broken.Capabilities) != 1 || broken.Capabilities[0] != CapUISidebar {
		t.Fatalf("corrupt mark re-widened the grant: %#v", broken.Capabilities)
	}
	if !broken.Desired {
		t.Fatal("corrupt mark dropped the enable intent")
	}
	// And a restart must not turn the corrupt marker into a running plugin.
	reopened := reopen(t, manager, root)
	after, _ := reopened.Get("test.sample")
	if after.State != StateCorrupt || after.Enabled {
		t.Fatalf("a corrupt plugin came back as running: %#v", after)
	}
	if !after.Desired || len(after.Capabilities) != 1 {
		t.Fatalf("corrupt state lost the grant or the intent across restart: %#v", after)
	}
}

// TestUpgradeFromPreviousSchema covers a real upgrade: the previous release
// created `plugin_states` and recorded this module at version 1, so the new
// migration list starts at step 2 and the plugin tables must still appear.
func TestUpgradeFromPreviousSchema(t *testing.T) {
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "plugins.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// The exact schema the previous release shipped, including its version row.
	legacy := []string{
		`CREATE TABLE schema_versions (module TEXT PRIMARY KEY, version INTEGER NOT NULL)`,
		`CREATE TABLE plugin_states (
			id TEXT PRIMARY KEY,
			enabled INTEGER NOT NULL DEFAULT 0,
			state TEXT NOT NULL DEFAULT 'DISABLED',
			faults INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			resources INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		)`,
		`INSERT INTO schema_versions(module,version) VALUES('plugins',1)`,
	}
	for _, statement := range legacy {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("legacy schema: %v", err)
		}
	}
	manager, err := New(db, func() bool { return false }, WithDataDir(root))
	if err != nil {
		t.Fatalf("upgrade from the previous schema failed: %v", err)
	}
	defer manager.Close()
	if got := len(manager.List()); got != 0 {
		t.Fatalf("upgraded database reported %d plugins", got)
	}
	// And the upgraded database is usable, not merely openable.
	source := writePluginDir(t, filepath.Join(t.TempDir(), "source"), testManifest, nil)
	if _, err := manager.Install(context.Background(), InstallRequest{Root: source, Capabilities: confirmAll(t, source)}); err != nil {
		t.Fatalf("install on an upgraded database: %v", err)
	}
}

// A list call must not deadlock: restorable entries need the embedded set and
// the tombstone rows, and both used to be read while the entries lock was held.
func TestListReportsUninstalledBundledPluginWithoutDeadlock(t *testing.T) {
	root := t.TempDir()
	bundled := map[string]map[string][]byte{
		"test.sample": {
			"plugin.json": []byte(testManifest),
			"renderer.js": []byte("export function activate(){}\n"),
			"LICENSE":     []byte("MIT\n"),
		},
	}
	manager, err := newManagerAt(t, root, WithBundled(func() (map[string]map[string][]byte, error) { return bundled, nil }))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := manager.ReleaseBundled(ctx, bundled); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = manager.List()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("List deadlocked")
	}

	live, err := manager.Enable(ctx, "test.sample")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Uninstall(ctx, "test.sample"); err != nil {
		t.Fatal(err)
	}
	// The token is revoked before any file work, so it cannot survive the call.
	if manager.Verify("test.sample", live.Token) {
		t.Fatal("uninstall left a valid token")
	}
	listed := manager.List()
	var restorable *Plugin
	for index := range listed {
		if listed[index].ID == "test.sample" {
			restorable = &listed[index]
		}
	}
	if restorable == nil {
		t.Fatal("an uninstalled bundled plugin disappeared from the list")
	}
	if !restorable.Restorable || restorable.State != StateUninstall || restorable.Enabled {
		t.Fatalf("restorable entry is wrong: %#v", restorable)
	}
	if len(restorable.Capabilities) != 0 {
		t.Fatalf("a tombstone reported capabilities: %#v", restorable.Capabilities)
	}
	// Nothing was released automatically.
	if _, statErr := os.Stat(filepath.Join(root, "plugins", "test.sample")); !os.IsNotExist(statErr) {
		t.Fatal("listing released the uninstalled plugin's files again")
	}
	// And restore is still the explicit path.
	restored, err := manager.RestoreBundled(ctx, "test.sample", bundled["test.sample"])
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if restored.State != StateDisabled || restored.Enabled {
		t.Fatalf("restore did not keep the plugin stopped: %#v", restored)
	}
	for _, plugin := range manager.List() {
		if plugin.ID == "test.sample" && plugin.Restorable {
			t.Fatal("a restored plugin is still listed as restorable")
		}
	}
}
