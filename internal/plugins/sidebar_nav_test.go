package plugins

import (
	"context"
	"testing"

	bundledpkg "enhancedkimi/plugins"
)

// ui:sidebar-nav is a v2 privileged capability: a locally installed plugin must
// never be able to put a row under the official "new chat" button, because that
// row sits in the official navigation and carries the client's own authority.
func TestSidebarNavIsPrivilegedAndRefusedToLocalPlugins(t *testing.T) {
	if !contains(privilegedCapabilities, CapUISidebarNav) {
		t.Fatal("ui:sidebar-nav is not in the privileged set")
	}
	if !RequiresPrivilege(CapUISidebarNav) {
		t.Fatal("RequiresPrivilege does not report ui:sidebar-nav")
	}
	grants, err := ParseCapabilities([]string{CapUISidebarNav})
	if err != nil {
		t.Fatalf("ui:sidebar-nav is not a known capability: %v", err)
	}
	if !grants.Has(CapUISidebarNav) {
		t.Fatal("parsed grants do not keep ui:sidebar-nav")
	}
	if dropped := grants.WithoutPrivileged(); dropped.Has(CapUISidebarNav) {
		t.Fatal("a non-official plugin kept ui:sidebar-nav through WithoutPrivileged")
	}
	manager, _ := testManager(t, func() bool { return false })
	source := writePluginDir(t, t.TempDir(), manifestFor("test.local", 2, []string{CapUISidebarNav}), nil)
	declared := []string{CapUISidebarNav}
	if _, err := manager.Install(context.Background(), InstallRequest{Root: source, Capabilities: &declared}); err == nil {
		t.Fatal("a local plugin was granted ui:sidebar-nav")
	} else {
		var pluginErr *Error
		if !asError(err, &pluginErr) || pluginErr.Code != "PLUGIN_CAPABILITY_PRIVILEGED" {
			t.Fatalf("unexpected refusal: %v", err)
		}
	}
}

// The official preinstalled planner is the only plugin that ships a session-info
// entry today. It must not also claim the sidebar row: that row belongs to the
// scheduled-task plugin, and granting it here would put a planner row in the
// official navigation with no way for the user to remove it.
func TestSidebarNavIsNotGrantedToThePlanner(t *testing.T) {
	bundled, err := bundledpkg.BundledPlugins()
	if err != nil {
		t.Fatalf("read bundled set: %v", err)
	}
	manifest, err := ParseManifest(bundled["kimicode-plus.planner"]["plugin.json"])
	if err != nil {
		t.Fatalf("planner manifest: %v", err)
	}
	grants, err := ParseCapabilities(manifest.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if grants.Has(CapUISidebarNav) {
		t.Fatal("planner requests ui:sidebar-nav")
	}
	if !grants.Has(CapUIEntry) {
		t.Fatal("planner lost ui:entry")
	}
}

func TestManagerOnChangeFiresOnEnableAndDisable(t *testing.T) {
	manager, _ := testManager(t, func() bool { return false })
	changes := 0
	manager.UseOnChange(func() { changes++ })
	source := writePluginDir(t, t.TempDir(), manifestFor("test.notify", 1, []string{"storage"}), nil)
	declared := []string{"storage"}
	if _, err := manager.Install(context.Background(), InstallRequest{Root: source, Capabilities: &declared}); err != nil {
		t.Fatalf("install failed: %v", err)
	}
	if changes != 1 {
		t.Fatalf("expected 1 change after install, got %d", changes)
	}
	if _, err := manager.Enable(context.Background(), "test.notify"); err != nil {
		t.Fatalf("enable failed: %v", err)
	}
	if changes != 2 {
		t.Fatalf("expected 2 changes after enable, got %d", changes)
	}
	if err := manager.Disable(context.Background(), "test.notify"); err != nil {
		t.Fatalf("disable failed: %v", err)
	}
	if changes != 3 {
		t.Fatalf("expected 3 changes after disable, got %d", changes)
	}
}
