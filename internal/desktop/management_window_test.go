package desktop

import (
	"strings"
	"testing"
)

// The management window is a second BrowserWindow with its own bridge key. The
// route list is the only thing standing between a surface request and an
// arbitrary path inside a privileged window, so it is checked both as a literal
// list and as the guard that rejects everything else.
func TestBootstrapRestrictsManagementRoutes(t *testing.T) {
	for _, wanted := range []string{
		"const managementPaths = ['/models', '/credentials'];",
		"if (typeof value !== 'string' || !managementPaths.includes(value)) throw new Error('管理窗口路径不被允许');",
		"case 'window.openManagement': return openWindow('management', { path: managementPath(payload.path) });",
	} {
		if !strings.Contains(bootstrapSource, wanted) {
			t.Fatalf("bootstrap.js lost the management route guard: %s", wanted)
		}
	}
	// Plugins are managed inside the settings dialog, so the window must not
	// expose a plugin route at all.
	if strings.Contains(bootstrapSource, "'/plugins'") {
		t.Fatal("bootstrap.js still exposes a plugin route to the management window")
	}
	// window.popout and window.openSettings keep their original behaviour.
	for _, wanted := range []string{
		"case 'window.openSettings': return wc.executeJavaScript('window.__ekdNativeUI.openSettings()');",
		"case 'window.popout': return openWindow('planner',",
	} {
		if !strings.Contains(bootstrapSource, wanted) {
			t.Fatalf("bootstrap.js changed an existing window action: %s", wanted)
		}
	}
}

// The management window renders the injected surface on about:blank and holds a
// bridge key, so it must not be able to navigate anywhere else, open a window or
// expose devtools. Each guard is pinned by the literal that implements it.
func TestBootstrapKeepsManagementWindowContained(t *testing.T) {
	for _, wanted := range []string{
		"webPreferences: { contextIsolation: true, nodeIntegration: false, sandbox: true, devTools: false },",
		"win.webContents.setWindowOpenHandler(() => ({ action: 'deny' }));",
		"win.webContents.on('will-navigate', (event, url) => {",
		"if (url !== 'about:blank') event.preventDefault();",
		"await win.loadURL('about:blank');",
	} {
		if !strings.Contains(bootstrapSource, wanted) {
			t.Fatalf("bootstrap.js lost a management window containment guard: %s", wanted)
		}
	}
	// The navigation listener must sit before the renderer is bound, so a page
	// cannot start loading while the guard is still missing.
	guard := strings.Index(bootstrapSource, "win.webContents.on('will-navigate', (event, url) => {")
	bind := strings.Index(bootstrapSource, "if (management) await bindRenderer(win.webContents, 'management'")
	if guard < 0 || bind < 0 || guard > bind {
		t.Fatal("the will-navigate guard must be installed before the management renderer is bound")
	}
}

// A management window is wider than the planner so a model table fits, and it
// keeps its own title. Both windows take a fresh bridge key per WebContents.
func TestBootstrapSizesManagementWindowSeparately(t *testing.T) {
	for _, wanted := range []string{
		"title: management ? 'KimiCode+ 管理' : 'KimiCode+ 规划',",
		"width: previous?.width || (management ? 1080 : 640),",
		"height: previous?.height || (management ? 760 : 840),",
		"minWidth: management ? 720 : 400, minHeight: management ? 520 : 500,",
		"const key = crypto.randomBytes(32).toString('base64url');",
	} {
		if !strings.Contains(bootstrapSource, wanted) {
			t.Fatalf("bootstrap.js lost window sizing or key isolation: %s", wanted)
		}
	}
}

// Re-opening on a different route must replace the bound renderer, otherwise the
// old page keeps a key that was issued for the old route.
func TestBootstrapRebindsManagementOnRouteChange(t *testing.T) {
	wanted := "if (kind === 'management' && source.path) {"
	if !strings.Contains(bootstrapSource, wanted) {
		t.Fatalf("bootstrap.js no longer rebinds a management window on route change")
	}
	start := strings.Index(bootstrapSource, wanted)
	end := strings.Index(bootstrapSource[start:], "\n    }")
	if end < 0 {
		t.Fatal("management rebind block is not terminated")
	}
	block := bootstrapSource[start : start+end]
	if !strings.Contains(block, "await bindRenderer(existing.webContents, 'management',") {
		t.Fatalf("management route change does not rebind the renderer: %s", block)
	}
}

// The management surface renders its own window chrome, so the renderer must
// pass the start path through instead of falling back to the planner surface.
func TestRendererMountsManagementSurface(t *testing.T) {
	for _, wanted := range []string{
		"options.surface === 'settings' || options.surface === 'management' || options.popout",
		"if (options.surface === 'management') mount(element, 'management', host, { initialPath: options.initialPath });",
		"const title = options.surface === 'settings' ? 'KimiCode+ 设置' : options.surface === 'management' ? 'KimiCode+ 管理' : 'KimiCode+ 规划聊天';",
	} {
		if !strings.Contains(rendererSource, wanted) {
			t.Fatalf("renderer.js lost the management mount: %s", wanted)
		}
	}
}
