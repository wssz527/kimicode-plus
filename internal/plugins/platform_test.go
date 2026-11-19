package plugins

import (
	"context"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWindowsPlatformLimitationsPreserveOtherPluginCapabilities(t *testing.T) {
	manager, _ := testManager(t, func() bool { return false })
	manager.platform = "windows"
	ctx := context.Background()
	grants := []string{CapUIEntry, CapUIRightPanel, CapBrowserView, CapCommands}
	root := writePluginDir(t, filepath.Join(t.TempDir(), "source"), manifestFor("test.windows", 2, grants), nil)
	preview, err := manager.Preview(ctx, root)
	if err != nil || !reflect.DeepEqual(preview.PlatformLimitations, PlatformLimitations("windows")) {
		t.Fatalf("preview: %+v (%v)", preview, err)
	}
	if _, err = manager.Install(ctx, InstallRequest{Root: root, PackageDigest: preview.PackageDigest, Capabilities: &grants, ConfirmPrivileged: true}); err != nil {
		t.Fatal(err)
	}
	plugin, err := manager.Enable(ctx, preview.ID)
	if err != nil || !reflect.DeepEqual(plugin.PlatformLimitations, preview.PlatformLimitations) {
		t.Fatalf("enabled plugin: %+v (%v)", plugin, err)
	}
	for _, action := range []string{"browser.open", "browser.close", "ui.popout"} {
		code, value := callPlugin(t, manager, plugin.Token, action, nil)
		failure, _ := value["error"].(map[string]any)
		if code != http.StatusConflict || failure["code"] != "PLATFORM_UNSUPPORTED" || failure["message"] == "" {
			t.Fatalf("%s => %d %#v", action, code, value)
		}
	}
	for _, action := range []string{"register", "list", "invoke", "unregister"} {
		code, value := callPlugin(t, manager, plugin.Token, "commands."+action, map[string]any{"name": "windows.echo", "command": "windows.echo"})
		if code != http.StatusOK {
			t.Fatalf("commands.%s => %d %#v", action, code, value)
		}
	}
	root = writePluginDir(t, filepath.Join(t.TempDir(), "storage"), manifestFor("test.storage", 2, []string{CapStorage}), nil)
	preview, err = manager.Preview(ctx, root)
	if err != nil || len(preview.PlatformLimitations) != 0 {
		t.Fatalf("unrelated capabilities: %+v (%v)", preview, err)
	}
}
