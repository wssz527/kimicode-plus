package plugins

import (
	"encoding/json"
	"strings"
	"testing"

	bundledpkg "enhancedkimi/plugins"
)

// TestBundledPlannerPluginIsComplete guards the preinstalled planner package.
// The embed directive is easy to forget when a directory is added: without it
// the release step never installs the entry and the right-panel tab silently
// never appears.
func TestBundledPlannerPluginIsComplete(t *testing.T) {
	bundled, err := bundledpkg.BundledPlugins()
	if err != nil {
		t.Fatalf("read bundled set: %v", err)
	}
	files, ok := bundled["kimicode-plus.planner"]
	if !ok {
		names := make([]string, 0, len(bundled))
		for id := range bundled {
			names = append(names, id)
		}
		t.Fatalf("planner plugin missing from the embedded set; have %v", names)
	}
	for _, name := range []string{"plugin.json", "renderer.js", "LICENSE"} {
		if len(files[name]) == 0 {
			t.Fatalf("bundled planner is missing %s", name)
		}
	}
	for name := range files {
		if strings.Contains(name, "..") {
			t.Fatalf("bundled file escapes its directory: %s", name)
		}
	}
	manifest, err := ParseManifest(files["plugin.json"])
	if err != nil {
		t.Fatalf("bundled planner manifest: %v", err)
	}
	if manifest.APIVersion != 2 {
		t.Fatalf("planner apiVersion = %d, want 2", manifest.APIVersion)
	}
	if manifest.Entry.Renderer != "renderer.js" {
		t.Fatalf("entry.renderer = %q", manifest.Entry.Renderer)
	}
	granted, err := ParseCapabilities(manifest.Capabilities)
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	// The panel needs exactly these surfaces. A missing one silently disables a
	// documented part of the plugin: without ui:entry there is no entry point,
	// without browser:view no web source, without planner:run no local source.
	for _, name := range []string{
		CapUIEntry, CapUIRightPanel, CapUILayout, CapBrowserView,
		CapPlannerRun, CapHandoff, CapModelsReadV2, CapSessionsRead, CapCommands, CapStorage,
	} {
		if !granted.Has(name) {
			t.Fatalf("planner is missing %s", name)
		}
	}
	// Anything it does not use must not be requested: the consent table and the
	// privilege gate are only meaningful if the list stays minimal.
	for _, name := range []string{
		CapUIMainArea, CapSessionsApprove, CapSessionsAbort, CapSessionsHistory,
		CapAttachments, CapSessionsSend, CapConfigRead, CapConfigWrite,
		CapMetricsRead, CapUISidebar, CapUISettings, CapUIThinking, CapUIMessage,
	} {
		if granted.Has(name) {
			t.Fatalf("planner asks for unused capability %s", name)
		}
	}
	if len(granted.Hosts()) > 0 {
		t.Fatalf("planner must not declare net hosts, got %v", granted.Hosts())
	}
}

// TestBundledPlannerModuleStaysSingleFile covers the loader contract: a plugin
// module is imported from a blob URL, so any relative import or re-export fails
// at activation time in the host.
func TestBundledPlannerModuleStaysSingleFile(t *testing.T) {
	bundled, err := bundledpkg.BundledPlugins()
	if err != nil {
		t.Fatal(err)
	}
	files := bundled["kimicode-plus.planner"]
	source := strings.ReplaceAll(string(files["renderer.js"]), "\r\n", "\n")
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import ") {
			t.Fatalf("planner has a top-level import: %s", trimmed)
		}
		if strings.HasPrefix(trimmed, "export ") && strings.Contains(trimmed, " from ") {
			t.Fatalf("planner re-exports another module: %s", trimmed)
		}
	}
	if !strings.Contains(source, "export async function activate") {
		t.Fatal("planner module does not export activate")
	}
}

// TestBundledPlannerDeclaresItsSiteAdapters keeps the manifest's promise and the
// code in step. The five sites from A-D7 are the ones the plan names, and the
// adapter sources have to exist for each of them.
func TestBundledPlannerDeclaresItsSiteAdapters(t *testing.T) {
	bundled, err := bundledpkg.BundledPlugins()
	if err != nil {
		t.Fatal(err)
	}
	source := string(bundled["kimicode-plus.planner"]["renderer.js"])
	for _, site := range []string{"www.kimi.com", "chat.deepseek.com", "gemini.google.com", "claude.ai", "chatgpt.com"} {
		if !strings.Contains(source, site) {
			t.Fatalf("planner does not reference %s", site)
		}
	}
	// The web view is created in the fixed isolated partition: the plugin must
	// never claim to handle cookies or logins itself.
	for _, forbidden := range []string{"document.cookie", "persist:", "session.setCookie"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("planner touches %q; the host owns the browser partition", forbidden)
		}
	}
}

// TestPlannerManifestCapabilitiesAreAllPrivileged documents the reason the
// plugin is preinstalled: nine of its ten grants are v2-only, so a locally
// installed copy with the same manifest could never run it.
func TestPlannerManifestCapabilitiesAreAllPrivileged(t *testing.T) {
	bundled, err := bundledpkg.BundledPlugins()
	if err != nil {
		t.Fatal(err)
	}
	var header struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(bundled["kimicode-plus.planner"]["plugin.json"], &header); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	privileged, ordinary := 0, 0
	var ordinaryNames []string
	for _, name := range header.Capabilities {
		if RequiresPrivilege(name) {
			privileged++
		} else {
			ordinary++
			ordinaryNames = append(ordinaryNames, name)
		}
	}
	if privileged < 8 {
		t.Fatalf("planner declares only %d privileged capabilities", privileged)
	}
	// `sessions:read` (Code→网页摘要) and `storage` (站点、草稿、上次站点) are the
	// only v1 grants; a third one means the manifest grew a permission the plugin
	// does not use.
	if ordinary != 2 {
		t.Fatalf("planner declares %d ordinary capabilities, want sessions:read and storage: %v", ordinary, header.Capabilities)
	}
	for _, name := range ordinaryNames {
		if name != CapSessionsRead && name != CapStorage {
			t.Fatalf("planner declares unexpected ordinary capability %s", name)
		}
	}
}
