package plugins

import (
	"encoding/json"
	"strings"
	"testing"

	bundledpkg "enhancedkimi/plugins"
)

func TestBundledSchedulerPluginIsComplete(t *testing.T) {
	bundled, err := bundledpkg.BundledPlugins()
	if err != nil {
		t.Fatalf("read bundled set: %v", err)
	}
	files, ok := bundled["kimicode-plus.scheduler"]
	if !ok {
		names := make([]string, 0, len(bundled))
		for id := range bundled {
			names = append(names, id)
		}
		t.Fatalf("scheduler plugin missing from the embedded set; have %v", names)
	}
	for _, name := range []string{"plugin.json", "renderer.js", "LICENSE"} {
		if len(files[name]) == 0 {
			t.Fatalf("bundled scheduler is missing %s", name)
		}
	}
	manifest, err := ParseManifest(files["plugin.json"])
	if err != nil {
		t.Fatalf("bundled scheduler manifest: %v", err)
	}
	if manifest.APIVersion != 2 {
		t.Fatalf("scheduler apiVersion = %d, want 2", manifest.APIVersion)
	}
	if manifest.Entry.Renderer != "renderer.js" {
		t.Fatalf("entry.renderer = %q", manifest.Entry.Renderer)
	}
	granted, err := ParseCapabilities(manifest.Capabilities)
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	for _, name := range []string{
		CapUISidebarNav, CapUIMainArea, CapUIEntry, CapUIRightPanel,
		CapSessionsRead, CapModelsReadV2, CapScheduleManage,
	} {
		if !granted.Has(name) {
			t.Fatalf("scheduler is missing %s", name)
		}
	}
	for _, name := range []string{
		CapPlannerRun, CapBrowserView, CapHandoff, CapSessionsApprove,
		CapSessionsAbort, CapSessionsSend, CapConfigWrite, CapStorage,
	} {
		if granted.Has(name) {
			t.Fatalf("scheduler asks for unused capability %s", name)
		}
	}
	if len(granted.Hosts()) > 0 {
		t.Fatalf("scheduler must not declare net hosts, got %v", granted.Hosts())
	}
}

func TestBundledSchedulerModuleStaysSingleFileAndTrustedTypesSafe(t *testing.T) {
	bundled, err := bundledpkg.BundledPlugins()
	if err != nil {
		t.Fatal(err)
	}
	files := bundled["kimicode-plus.scheduler"]
	source := strings.ReplaceAll(string(files["renderer.js"]), "\r\n", "\n")
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import ") {
			t.Fatalf("scheduler has a top-level import: %s", trimmed)
		}
		if strings.HasPrefix(trimmed, "export ") && strings.Contains(trimmed, " from ") {
			t.Fatalf("scheduler re-exports another module: %s", trimmed)
		}
	}
	if !strings.Contains(source, "export async function activate") {
		t.Fatal("scheduler module does not export activate")
	}
	for _, forbidden := range []string{".innerHTML", ".outerHTML", "insertAdjacentHTML"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("scheduler uses unsafe DOM sink %q (violates Trusted Types)", forbidden)
		}
	}
	var header struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(files["plugin.json"], &header); err != nil {
		t.Fatal(err)
	}
	if !contains(header.Capabilities, CapScheduleManage) {
		t.Fatalf("manifest missing %s", CapScheduleManage)
	}
}
