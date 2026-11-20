package desktop

import (
	"strings"
	"testing"
)

// The v2 entry host hands the runtime a Disposable per registration. Dropping the
// registry row alone used to leave the button in the container, because the next
// sync only reconciles entries that are still planned. The dispose path is pinned
// as source because the renderer has no DOM in these tests.
func TestPluginEntryDisposeRemovesItsButton(t *testing.T) {
	index := strings.Index(rendererSource, "const pluginEntryHost")
	if index < 0 {
		t.Fatal("renderer.js lost the v2 entry host")
	}
	end := strings.Index(rendererSource[index:], "function runPluginEntry")
	if end < 0 {
		t.Fatal("the entry host is not followed by runPluginEntry")
	}
	host := rendererSource[index : index+end]
	for _, wanted := range []string{
		"state.pluginEntries.delete(key);",
		"removeEntry('plugin:' + key);",
		"syncNativeEntries();",
	} {
		if !strings.Contains(host, wanted) {
			t.Fatalf("disposing a plugin entry no longer removes its button: %s", wanted)
		}
	}
}

// A v2 surface belongs to one plugin and is released by that plugin's scope, so
// every host registration has to reach the scope that owns it. The renderer owns
// the mount and its teardown; the runtime owns registering those teardowns. Both
// halves are pinned, because either one alone leaks the surface.
func TestV2HostsRegisterInThePluginScope(t *testing.T) {
	start := strings.Index(rendererSource, "const pluginEntryHost")
	end := strings.Index(rendererSource, "const pluginBrowserHost")
	if start < 0 || end < 0 || start > end {
		t.Fatal("renderer.js lost the v2 host block")
	}
	block := rendererSource[start:end]
	for _, wanted := range []string{
		"return { dispose: () => {",
		"removeEntry('plugin:' + key);",
		"dispose: release,",
		"entry.dispose = dispose;",
		"const pluginLayoutHost = { acquireSidebarCollapse: () => ({ dispose: acquireSidebarCollapse() }) };",
	} {
		if !strings.Contains(block, wanted) {
			t.Fatalf("a v2 host lost the teardown it hands to the runtime scope: %s", wanted)
		}
	}
}

func TestPluginMainAreaTracksPageModeForSessionNavigation(t *testing.T) {
	for _, wanted := range []string{
		"mode: definition?.mode === 'page' ? 'page' : 'split'",
		"if (handle.mode === 'page') handle.dispose();",
	} {
		if !strings.Contains(rendererSource, wanted) {
			t.Fatalf("renderer.js lost plugin page cleanup on session navigation: %s", wanted)
		}
	}
}

func TestNativeTabClickDeactivatesPluginRightPanelWithoutHidingPanel(t *testing.T) {
	for _, wanted := range []string{
		".ptb-tab:not([data-ekdp-plugin-panel])",
		"if (state.activePluginPanel) deactivatePluginPanel(state.activePluginPanel, false);",
		"if (contract?.activeTabId) contract.activeTabId.value = null;",
		"contract?.hidePanel?.();",
	} {
		if !strings.Contains(rendererSource, wanted) {
			t.Fatalf("renderer.js lost plugin panel / native tab switch guard: %s", wanted)
		}
	}
}

func TestPluginRightPanelTabIsDynamicAndPreservesOfficialWidth(t *testing.T) {
	mountStart := strings.Index(rendererSource, "const pluginRightPanelHost")
	if mountStart < 0 {
		t.Fatal("renderer.js lost pluginRightPanelHost")
	}
	mountBlock := rendererSource[mountStart : mountStart+strings.Index(rendererSource[mountStart:], "const pluginLayoutHost")]
	if strings.Contains(mountBlock, "tabs.append(wrapper)") {
		t.Fatal("mount() must not pin plugin tab into .ptb-tabs before open()")
	}
	if !strings.Contains(mountBlock, "panelBody()?.append(host);") || strings.Contains(mountBlock, "右侧面板契约未就绪") {
		t.Fatal("mount() must let plugins activate before the official right panel is available")
	}
	for _, wanted := range []string{
		`close.className = "ptb-x";`,
		`tabs.insertBefore(entry.wrapper, dragFill || null);`,
		"entry.opened = false;",
		"entry.wrapper.remove();",
		// The plugin's own icon reaches the host as a definition field. A hardcoded
		// planner branch here would keep showing a planner mark after the plugin is
		// disabled, which is exactly what this round removes.
		"icon: entry.definition.icon,",
		"icon: ENTRY_ICONS.split,",
	} {
		if !strings.Contains(rendererSource, wanted) {
			t.Fatalf("renderer.js missing right-panel dynamic tab or icon behavior: %s", wanted)
		}
	}
	if strings.Contains(rendererSource[strings.Index(rendererSource, "function activatePluginPanel"):mountStart], "previewWidth.value = Math.min") {
		t.Fatal("activatePluginPanel must not clobber user-dragged official panel width")
	}
}
