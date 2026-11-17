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
