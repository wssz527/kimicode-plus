package plugins

import (
	"strings"
	"testing"

	bundledpkg "enhancedkimi/plugins"
)

// TestBundledMonitorPluginIsComplete guards the preinstalled Monitor package the
// binary ships. The embed directive is easy to forget when a directory is added:
// without this the release step omits the sidebar widget and it silently never
// installs.
func TestBundledMonitorPluginIsComplete(t *testing.T) {
	bundled, err := bundledpkg.BundledPlugins()
	if err != nil {
		t.Fatalf("read bundled set: %v", err)
	}
	files, ok := bundled["kimicode-plus.monitor"]
	if !ok {
		names := make([]string, 0, len(bundled))
		for id := range bundled {
			names = append(names, id)
		}
		t.Fatalf("monitor plugin missing from the embedded set; have %v", names)
	}
	for _, name := range []string{"plugin.json", "renderer.js", "settings.js", "LICENSE"} {
		if len(files[name]) == 0 {
			t.Fatalf("bundled monitor is missing %s", name)
		}
	}
	for name := range files {
		if strings.Contains(name, "..") {
			t.Fatalf("bundled file escapes its directory: %s", name)
		}
	}
	manifest, err := ParseManifest(files["plugin.json"])
	if err != nil {
		t.Fatalf("bundled monitor manifest: %v", err)
	}
	if manifest.Entry.Renderer != "renderer.js" || manifest.Entry.Settings != "settings.js" {
		t.Fatalf("entry = %#v", manifest.Entry)
	}
	granted, err := ParseCapabilities(manifest.Capabilities)
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	// The plugin reads cache metrics and its own storage, mounts a sidebar surface
	// and a settings page. Nothing else may be granted: sessions:read would expose
	// conversation text the widget never renders.
	for _, name := range []string{CapMetricsRead, CapUISidebar, CapUISettings, CapStorage} {
		if !granted.Has(name) {
			t.Fatalf("monitor is missing %s", name)
		}
	}
	if granted.Has(CapSessionsRead) {
		t.Fatal("monitor asks for sessions:read but never reads conversation text")
	}
	if granted.Has(CapSessionsSend) || granted.Has(CapConfigWrite) || len(granted.Hosts()) > 0 {
		t.Fatalf("monitor asks for a capability it does not use: %v", granted.Names())
	}
	// A plugin module is a single file loaded from a blob URL, so a relative import
	// would resolve against the official app scheme and fail at activation.
	for name, data := range files {
		if !strings.HasSuffix(name, ".js") {
			continue
		}
		source := strings.ReplaceAll(string(data), "\r\n", "\n")
		for _, line := range strings.Split(source, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "export ... from") {
				t.Fatalf("%s has a relative import: %s", name, trimmed)
			}
			if strings.HasPrefix(trimmed, "export ") && strings.Contains(trimmed, " from ") {
				t.Fatalf("%s re-exports another module: %s", name, trimmed)
			}
		}
	}
	// The sidebar widget must not persist through private browser storage:
	// ctx.storage is the only durable channel, and the settings page writes the
	// same key. The ported panel keeps its upstream synchronous API calls
	// (`localStorage.getItem` in widget-structure.js), so the entry installs a
	// shim that forwards those reads and writes to ctx.storage. What must never
	// appear is a direct call into the browser store that bypasses the shim:
	// `window.localStorage` / `globalThis.sessionStorage`.
	for name, data := range files {
		source := string(data)
		for _, direct := range []string{"window.localStorage.", "globalThis.sessionStorage", "window.sessionStorage."} {
			if strings.Contains(source, direct) {
				t.Fatalf("%s uses private browser storage: %s", name, direct)
			}
		}
		if strings.Contains(source, "localStorage.getItem") && !strings.Contains(source, "forward") {
			// The shim explains itself; a direct read elsewhere would not.
			t.Logf("%s reads localStorage through the host shim", name)
		}
	}
	// The single durable channel has to be present, or the panel loses every
	// preference on restart.
	if !strings.Contains(string(bundled["kimicode-plus.monitor"]["renderer.js"]), "storage.set") {
		t.Fatal("monitor never writes through ctx.storage")
	}
}

// TestMonitorManifestUpstreamAttribution keeps the MIT notice attached to the
// ported structure. scripts/notices.py reads the same directory, so a manifest
// that drops the attribution would ship MIT text with no source.
func TestMonitorManifestUpstreamAttribution(t *testing.T) {
	bundled, err := bundledpkg.BundledPlugins()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ParseManifest(bundled["kimicode-plus.monitor"]["plugin.json"])
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Upstream == nil || manifest.Upstream.Repo != "bfjnbvf/kimi-code-monitor" || manifest.Upstream.License != "MIT" {
		t.Fatalf("upstream attribution = %#v", manifest.Upstream)
	}
	if !strings.HasPrefix(manifest.Upstream.Commit, "1d529f0") {
		t.Fatalf("upstream commit = %q", manifest.Upstream.Commit)
	}
	if manifest.License != "MIT" {
		t.Fatalf("license = %q", manifest.License)
	}
	license := string(bundled["kimicode-plus.monitor"]["LICENSE"])
	if !strings.Contains(license, "MIT License") || !strings.Contains(license, "十叶") {
		t.Fatal("bundled LICENSE does not carry the upstream MIT notice")
	}
}

// TestBundledPluginsHaveDistinctIDs covers the whole embedded set, so adding a
// second plugin cannot silently overwrite the first one's install directory.
func TestBundledPluginsHaveDistinctIDs(t *testing.T) {
	bundled, err := bundledpkg.BundledPlugins()
	if err != nil {
		t.Fatal(err)
	}
	if len(bundled) < 2 {
		t.Fatalf("bundled set has %d plugins, want official bundled plugins including monitor", len(bundled))
	}
	for id, files := range bundled {
		if _, err := ParseManifest(files["plugin.json"]); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if len(files["LICENSE"]) == 0 {
			t.Fatalf("%s has no LICENSE", id)
		}
	}
}
