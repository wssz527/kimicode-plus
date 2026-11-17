package plugins

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enhancedkimi/internal/storage"
)

// stagedRootOf answers the directory a plugin's dropped files land in, which is
// what every teardown path has to remove.
func stagedRootOf(manager *Manager, id string) string {
	return filepath.Join(manager.DataDir(), "staged", id)
}

// stageOneFile stages a single drop and answers the batch it created.
func stageOneFile(t *testing.T, manager *Manager, token string) string {
	t.Helper()
	code, value := callPlugin(t, manager, token, "attachments.stage", map[string]any{
		"files": []any{map[string]any{"name": "note.txt", "data": base64Of("staged")}},
	})
	if code != http.StatusOK {
		t.Fatalf("attachments.stage => %d %#v", code, value)
	}
	_, batch := stagedFiles(t, value)
	return batch
}

// Disabling a plugin ends its running instance, so the files it dropped and
// never sent have to go with it. The TTL sweep only runs when the plugin stages
// again — a disabled plugin never does — so leaving the root behind means the
// bytes stay until the plugin is removed.
func TestDisableRemovesStagedFiles(t *testing.T) {
	manager, token := officialManager(t, &pluginCoreDouble{}, []string{CapAttachments})
	batch := stageOneFile(t, manager, token)
	root := stagedRootOf(manager, "test.official")
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("the staging root was never created: %v", err)
	}
	if err := manager.Disable(context.Background(), "test.official"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(batch); !os.IsNotExist(err) {
		t.Fatalf("disable kept the staged batch: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("disable kept the plugin's staging root: %v", err)
	}
}

// Uninstalling removes the install record and the files, so a staged batch left
// behind would have no owner left to release it.
func TestUninstallRemovesStagedFiles(t *testing.T) {
	manager, token := officialManager(t, &pluginCoreDouble{}, []string{CapAttachments})
	stageOneFile(t, manager, token)
	other := filepath.Join(manager.DataDir(), "staged", "another.plugin")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	root := stagedRootOf(manager, "test.official")
	if err := manager.Uninstall(context.Background(), "test.official"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("uninstall kept the plugin's staging root: %v", err)
	}
	// Only the uninstalled plugin's own root goes: the shared parent and a
	// sibling plugin's stages are other plugins' data.
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("uninstall removed a sibling plugin's stages: %v", err)
	}
	if _, err := os.Stat(filepath.Join(manager.DataDir(), "staged")); err != nil {
		t.Fatalf("uninstall removed the shared staging parent: %v", err)
	}
}

// Safe mode stops every plugin, including one that only ever staged files and
// therefore holds no view, command or subscription that would otherwise put it
// in the teardown set.
func TestSafeModeRemovesStagedFiles(t *testing.T) {
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "plugins.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	manager, err := New(db, func() bool { return true }, WithDataDir(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Close() })
	manager.UseCore(&pluginCoreDouble{})
	files := map[string][]byte{
		"plugin.json": []byte(manifestFor("test.official", 2, []string{CapAttachments})),
		"renderer.js": []byte("export function activate(){}\n"),
		"LICENSE":     []byte("MIT\n"),
	}
	digest := ContentDigest(files)
	manager.officialDigests = func() (map[string]string, error) { return map[string]string{"test.official": digest}, nil }
	if err := manager.ReleaseBundled(context.Background(), map[string]map[string][]byte{"test.official": files}); err != nil {
		t.Fatal(err)
	}
	// Safe mode refuses to start the plugin, so the staged files are produced
	// through the same staging path a running plugin uses.
	staged := filepath.Join(stagedRootOf(manager, "test.official"), "00112233445566778899aabb")
	if err := os.MkdirAll(staged, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "01-note.txt"), []byte("staged"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.EnforceSafeMode()
	if _, err := os.Stat(stagedRootOf(manager, "test.official")); !os.IsNotExist(err) {
		t.Fatalf("safe mode kept the staged root: %v", err)
	}
}

// The removal is what cleanup does, not what it is allowed to do to a path it
// was handed: only a bare plugin id resolves under the staging parent.
func TestPurgeStagedRefusesAPath(t *testing.T) {
	root := t.TempDir()
	manager, err := newManagerAt(t, root)
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(root, "staged", "keep.me")
	if err := os.MkdirAll(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", ".", "..", "../keep.me", "keep.me/../../staged", "a/b"} {
		if err := manager.purgeStaged(id); err == nil {
			t.Fatalf("purge accepted %q as a plugin id", id)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("a refused id removed another directory: %v", err)
	}
}

// A removal that fails must be recorded, not reported as a clean teardown: the
// bytes are still on disk and someone has to be able to see why.
func TestAFailedPurgeIsRecorded(t *testing.T) {
	manager, token := officialManager(t, &pluginCoreDouble{}, []string{CapAttachments})
	batch := stageOneFile(t, manager, token)
	root := stagedRootOf(manager, "test.official")
	// A file where the batch should be removed from makes RemoveAll fail on a
	// directory that is not empty, which is the shape of a real permission
	// problem without depending on the test running as root.
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	err := manager.purgeStaged("test.official")
	if err == nil {
		// Running as root would still succeed; the recording path is what this
		// test covers, so skip instead of asserting a permission model the
		// environment does not have.
		t.Skip("the environment can remove a read-only directory")
	}
	manager.purgeStagedAndRecord(context.Background(), "test.official", ActivityDisable, "插件已停用，但暂存附件未能清理：", manager.now())
	var message string
	if err := manager.db.QueryRow(`SELECT detail FROM plugin_activity WHERE plugin_id=? AND kind=? ORDER BY id DESC LIMIT 1`, "test.official", ActivityDisable).Scan(&message); err != nil {
		t.Fatalf("the failed removal was not recorded: %v", err)
	}
	if !strings.Contains(message, "暂存附件未能清理") || !strings.Contains(message, err.Error()) {
		t.Fatalf("the record lost the real error: %q", message)
	}
	if _, statErr := os.Stat(batch); statErr != nil {
		t.Fatalf("the batch was gone, so the failure was not real: %v", statErr)
	}
}
