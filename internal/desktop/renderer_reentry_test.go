package desktop

import (
	"strings"
	"testing"
)

func TestRendererCleanupAllowsReentrantInstall(t *testing.T) {
	published := strings.Index(rendererSource, "window.__ekdNativeUI = state")
	if published < 0 {
		t.Fatal("renderer.js no longer publishes its native state")
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
	for _, wanted := range []string{
		"function pluginRefresh() { void syncPlugins(); }",
		"await refreshFeatures();\n  if (state.removed) return { status: 'removed', surface: 'host', domRevision: 1 };",
	} {
		if !strings.Contains(rendererSource, wanted) {
			t.Fatalf("renderer reentrant cleanup guard is missing: %s", wanted)
		}
	}
}
