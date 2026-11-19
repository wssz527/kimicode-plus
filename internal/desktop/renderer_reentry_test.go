package desktop

import (
	"strings"
	"testing"
)

func TestRendererCleanupAllowsReentrantInstall(t *testing.T) {
	// The published surface is an explicit object, not the internal state: the
	// state carries the bridge transport and the plugin runtime, so handing it to
	// the page would let any script act as the host.
	published := strings.Index(rendererSource, "window.__ekdNativeUI = publishNativeUI()")
	if published < 0 {
		t.Fatal("renderer.js no longer publishes its native surface")
	}
	if strings.Contains(rendererSource, "window.__ekdNativeUI = state") {
		t.Fatal("renderer.js publishes its internal state to the page")
	}
	// The published object must not carry the transport, the module or the
	// runtime: those are the host's own authority, not a page-facing API.
	start := strings.Index(rendererSource, "function publishNativeUI()")
	if start < 0 {
		t.Fatal("renderer.js has no explicit native surface")
	}
	end := strings.Index(rendererSource[start:], "window.__ekdNativeUI = ui;")
	if end < 0 {
		t.Fatal("renderer.js never assigns the native surface it builds")
	}
	surface := rendererSource[start : start+end]
	for _, forbidden := range []string{"request", "module", "channel", "transport"} {
		if strings.Contains(surface, forbidden) {
			t.Fatalf("native surface still exposes %s", forbidden)
		}
	}
	for _, field := range []string{
		"plugins: null,",
		"pluginReleases: new Map(),",
		"pluginTokens: new Map(),",
		"pluginAssets: new Map(),",
		"pluginSettings: new Map(),",
	} {
		initialized := strings.Index(rendererSource, field)
		if initialized < 0 || initialized > published {
			t.Fatalf("renderer cleanup can run before %s is initialized", field)
		}
	}
	source := strings.ReplaceAll(rendererSource, "\r\n", "\n")
	for _, wanted := range []string{
		"function pluginRefresh() { void syncPlugins(); }",
		"await refreshFeatures();\n  if (state.removed) return { status: 'removed', surface: 'host', domRevision: 1 };",
	} {
		if !strings.Contains(source, wanted) {
			t.Fatalf("renderer reentrant cleanup guard is missing: %s", wanted)
		}
	}
}
