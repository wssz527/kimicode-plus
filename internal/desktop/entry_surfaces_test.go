package desktop

import (
	"strings"
	"testing"
)

// A disabled planner must leave no entry and no panel behind. The renderer used
// to carry a planner branch of its own (icon, host, tab), which drew a planner
// row even when the plugin was off. This pins the removal so it cannot creep
// back: the only planner surface is the plugin's own ctx.entry registration.
func TestRendererHasNoHardcodedPlannerEntryOrPanel(t *testing.T) {
	for _, forbidden := range []string{
		"kind.endsWith(':planner')",
		"state.features.planner",
		"ekd-planner-host",
		"ekdPlannerTab",
		"openPlanner(",
		"planner: '<circle",
	} {
		if strings.Contains(rendererSource, forbidden) {
			t.Fatalf("renderer.js still draws a planner surface on its own: %s", forbidden)
		}
	}
	for _, wanted := range []string{
		"function pluginEntryPlans()",
		"action: 'plugin:' + key,",
		"title: entry.definition.title,",
		"icon: entry.definition.icon,",
		"open: () => entry.definition.open(),",
		"function sidebarNavPlans()",
		"action: 'sidebar:' + key,",
		"function builtinEntryPlans()",
	} {
		if !strings.Contains(rendererSource, wanted) {
			t.Fatalf("renderer.js lost a plugin-driven entry surface: %s", wanted)
		}
	}
}

func TestRendererSubscribesPluginsAndSyncsOverlay(t *testing.T) {
	for _, wanted := range []string{
		"pluginEventHost.subscribe('plugins',",
		"host.syncOverlayEntries",
		"invokeEntry: action => invokeEntryPlan(action)",
	} {
		if !strings.Contains(rendererSource, wanted) {
			t.Fatalf("renderer.js missing %s", wanted)
		}
	}
	for _, wanted := range []string{
		"case 'host.syncOverlayEntries':",
		"kimi:browser-overlay-open",
		"kimi:browser-overlay-rendered",
		"kimi:browser-overlay-activate",
	} {
		if !strings.Contains(bootstrapSource, wanted) {
			t.Fatalf("bootstrap.js missing %s", wanted)
		}
	}
}
