package desktop

import (
	"strings"
	"testing"
)

// The management window and its two pages are gone. Models are configured on the
// official provider form and credentials belong to the plugin that asks for
// them, so no privileged window may still be reachable from the bridge.
func TestBootstrapDroppedTheManagementWindow(t *testing.T) {
	for _, removed := range []string{
		"window.openManagement",
		"managementPaths",
		"managementPath(",
		"management initialPath",
	} {
		if strings.Contains(bootstrapSource, removed) {
			t.Fatalf("bootstrap.js still carries the removed management window: %s", removed)
		}
	}
	// The plugin window keeps working, and it is now the only popout surface.
	for _, wanted := range []string{
		"case 'plugin.window.open': {",
		"return openWindow('plugin:' + answer.pluginId,",
		"await bindRenderer(win.webContents, 'plugin',",
		"webPreferences: { contextIsolation: true, nodeIntegration: false, sandbox: true, devTools: false },",
		"win.webContents.setWindowOpenHandler(() => ({ action: 'deny' }));",
	} {
		if !strings.Contains(bootstrapSource, wanted) {
			t.Fatalf("bootstrap.js lost a plugin window guard: %s", wanted)
		}
	}
}

// The renderer mounted a management surface with its own title. With the window
// gone, only the settings surface may render into a popout body.
func TestRendererMountsOnlyTheSettingsSurface(t *testing.T) {
	if strings.Contains(rendererSource, "'management'") {
		t.Fatal("renderer.js still handles the removed management surface")
	}
	for _, wanted := range []string{
		"} else if (options.surface === 'settings') {",
		"document.title = 'KimiCode+ 设置';",
	} {
		if !strings.Contains(rendererSource, wanted) {
			t.Fatalf("renderer.js lost the settings surface mount: %s", wanted)
		}
	}
}

// The provider page enhancement runs in the official window only; a settings
// popout has no provider page to decorate.
func TestProviderEnhanceInstallsOnTheOfficialSurfaceOnly(t *testing.T) {
	wanted := "state.providerEnhance = options.surface === 'host' ? createProviderEnhance({ request: transport.request }) : null;"
	if !strings.Contains(rendererSource, wanted) {
		t.Fatalf("renderer.js lost the provider enhancement hook: %s", wanted)
	}
	if !strings.HasPrefix(rendererBundle, "function(options) {\n") || !strings.Contains(rendererBundle, "return installNativeRenderer(options);") {
		t.Fatalf("rendererBundle must wrap providerEnhanceSource and rendererSource in one function expression")
	}
}
