package plugins

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLocalV2PluginGrantsAndIntegrity(t *testing.T) {
	ctx := context.Background()
	manager, root := testManager(t, func() bool { return false })
	core := &pluginCoreDouble{}
	manager.UseCore(core)
	id := "test.local"
	all := append(append([]string{}, fixedCapabilities...), "net:example.com", "credentials:service")
	source := writePluginDir(t, t.TempDir(), manifestFor(id, 2, all), nil)
	ordinary := []string{CapStorage, CapUISidebar}
	if installed, err := manager.Install(ctx, InstallRequest{Root: source, PackageDigest: previewDigest(t, manager, source), Capabilities: &ordinary}); err != nil || !reflect.DeepEqual(installed.Capabilities, ordinary) {
		t.Fatalf("install with ordinary grants only: %#v, %v", installed, err)
	}
	wantGrants, err := ParseCapabilities(all)
	if err != nil {
		t.Fatal(err)
	}
	wantSlots := []string{"sidebar", "settings", "thinking", "message", "entry", "sidebar-nav", "main-area", "right-panel", "layout"}
	request := InstallRequest{Root: source, PackageDigest: previewDigest(t, manager, source), Capabilities: &all, ConfirmPrivileged: true}
	mux := http.NewServeMux()
	manager.Register(mux)
	for _, confirm := range []bool{false, true} {
		request.ConfirmPrivileged = confirm
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, APIPrefix+"/plugins/install", strings.NewReader(string(body))))
		if !confirm {
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "PLUGIN_PRIVILEGED_CONFIRM_REQUIRED") {
				t.Fatalf("install without confirmation: %d %s", response.Code, response.Body)
			}
			continue
		}
		var installed Plugin
		if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &installed) != nil {
			t.Fatalf("confirmed install: %d %s", response.Code, response.Body)
		}
		if installed.Source != SourceLocal || installed.Official || installed.Enabled || installed.APIVersion != 2 ||
			!reflect.DeepEqual(installed.Capabilities, wantGrants.Names()) || !reflect.DeepEqual(installed.Slots, wantSlots) || len(installed.PrivilegedCapabilities) != 15 {
			t.Fatalf("unexpected local grants: %#v", installed)
		}
	}

	manager = reopen(t, manager, root)
	manager.UseCore(core)
	if _, err = manager.Enable(ctx, id); err != nil {
		t.Fatal(err)
	}
	token, err := manager.Token(id)
	if err != nil {
		t.Fatal(err)
	}
	code, value := callPlugin(t, manager, token, "runtime.info", nil)
	if code != http.StatusOK {
		t.Fatalf("runtime identity: %d %#v", code, value)
	}
	identity := value["value"].(map[string]any)
	for _, capability := range all {
		if !containsAny(identity["capabilities"], capability) {
			t.Fatalf("runtime identity missing %s: %#v", capability, identity)
		}
	}
	for _, slot := range wantSlots {
		if !containsAny(identity["slots"], slot) {
			t.Fatalf("runtime identity missing %s: %#v", slot, identity)
		}
	}
	assets := http.NewServeMux()
	manager.Register(assets)
	checkEntry := func(want int) {
		t.Helper()
		response := httptest.NewRecorder()
		assets.ServeHTTP(response, httptest.NewRequest(http.MethodGet, APIPrefix+"/plugins/"+id+"/assets/renderer.js", nil))
		if response.Code != want {
			t.Fatalf("renderer module: %d %s", response.Code, response.Body)
		}
	}
	checkEntry(http.StatusOK)
	for action, params := range map[string]any{
		"ui.popout":        nil,
		"sessions.approve": map[string]any{"id": "s1", "interactionId": "i1", "value": map[string]any{"decision": "approved"}},
		"sessions.abort":   map[string]any{"id": "s1"}, "sessions.history": map[string]any{"id": "s1"},
		"attachments.stage": map[string]any{"files": []map[string]string{{"name": "note.txt", "data": "TUlU"}}},
		"browser.open":      map[string]any{"viewId": "v1", "url": "https://example.com/"},
		"planner.threads":   nil, "handoff.commit": map[string]any{"text": "plan", "mode": "plan"},
		"models.list": nil, "commands.register": map[string]any{"name": "test.open"}, "schedule.list": nil,
	} {
		code, value := callPlugin(t, manager, token, action, params)
		if manager.platform == "windows" && (action == "ui.popout" || action == "browser.open") {
			if code != http.StatusConflict || !strings.Contains(mustString(value), "PLATFORM_UNSUPPORTED") {
				t.Errorf("local action %s: %d %#v", action, code, value)
			}
		} else if code != http.StatusOK {
			t.Errorf("local action %s: %d %#v", action, code, value)
		}
	}

	installedFile := filepath.Join(root, "plugins", id, "renderer.js")
	if err := os.WriteFile(installedFile, []byte("// changed after install"), 0o600); err != nil {
		t.Fatal(err)
	}
	checkEntry(http.StatusForbidden)
	if code, value := callPlugin(t, manager, token, "models.list", nil); code != http.StatusForbidden || !strings.Contains(mustString(value), "PLUGIN_PRIVILEGE_DENIED") {
		t.Fatalf("tampered privileged call: %d %#v", code, value)
	}
	if code, value := callPlugin(t, manager, token, "storage.get", map[string]any{"key": "settings"}); code != http.StatusOK {
		t.Fatalf("ordinary action suspended: %d %#v", code, value)
	}
	if installed := manager.Get2(id); !reflect.DeepEqual(installed.Capabilities, wantGrants.WithoutPrivileged().Names()) || contains(installed.Slots, "main-area") {
		t.Fatalf("tampered install still advertises privileged grants: %#v", installed)
	}
	if _, value := callPlugin(t, manager, token, "runtime.info", nil); containsAny(value["value"].(map[string]any)["capabilities"], CapModelsReadV2) {
		t.Fatalf("tampered identity still advertises privileged grants: %#v", value)
	}
	if _, err := manager.Install(ctx, request); err != nil {
		t.Fatal(err)
	}
	if manager.Verify(id, token) {
		t.Fatal("reinstall retained a previous token")
	}
	enabled, err := manager.Enable(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if code, value := callPlugin(t, manager, enabled.Token, "models.list", nil); code != http.StatusOK {
		t.Fatalf("reinstall did not restore action: %d %#v", code, value)
	}
}

func TestLegacyInstallWithoutDigestRequiresReinstallForPrivilege(t *testing.T) {
	ctx := context.Background()
	manager, root := testManager(t, func() bool { return false })
	id := "test.legacy"
	capabilities := []string{CapModelsReadV2}
	source := writePluginDir(t, t.TempDir(), manifestFor(id, 2, capabilities), nil)
	if _, err := manager.Install(ctx, InstallRequest{Root: source, PackageDigest: previewDigest(t, manager, source), Capabilities: &capabilities, ConfirmPrivileged: true}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.db.Exec(`ALTER TABLE plugin_installs DROP COLUMN official_manifest`); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.db.Exec(`ALTER TABLE plugin_installs DROP COLUMN install_digest`); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.db.Exec(`UPDATE schema_versions SET version=5 WHERE module='plugins'`); err != nil {
		t.Fatal(err)
	}
	manager = reopen(t, manager, root)
	enabled, err := manager.Enable(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if code, value := callPlugin(t, manager, enabled.Token, "models.list", nil); code != http.StatusForbidden || !strings.Contains(mustString(value), "PLUGIN_PRIVILEGE_DENIED") {
		t.Fatalf("legacy install automatically gained privilege: %d %#v", code, value)
	}
}

func TestNormalizeUpstreamURL(t *testing.T) {
	for input, want := range map[string]string{
		"publisher/my_repo.git":                  "https://github.com/publisher/my_repo.git",
		"HTTP://Example.com/repository?q=1#main": "http://example.com/repository?q=1#main",
		"https://host.test/repo%20name":          "https://host.test/repo%20name",
		"https://user@host.test/repo":            "", "https://:443/": "", "https:///repo": "",
		"https:host.test/repo": "", "//host.test/repo": "", "javascript:alert(1)": "",
		"file:///etc/passwd": "", "owner/../repo": "", "../repo": "", "plain": "",
	} {
		t.Run(input, func(t *testing.T) {
			if got := normalizeUpstreamURL(input); got != want {
				t.Errorf("normalizeUpstreamURL(%q) = %q, want %q", input, got, want)
			}
		})
	}
}

func TestPluginLegalMetadataAndBoundedFiles(t *testing.T) {
	ctx := context.Background()
	manager, root := testManager(t, func() bool { return false })
	manifest := strings.TrimSuffix(manifestFor("test.legal", 2, []string{CapUIEntry}), "}") +
		`,"license":"MIT","upstream":{"repo":"publisher/repo","commit":"a1b2c3d4abcd","license":"Apache-2.0"}}`
	source := writePluginDir(t, t.TempDir(), manifest, map[string]string{
		"LICENSE": strings.Repeat("L", legalFileLimit-1) + "文末", "NOTICE": strings.Repeat("N", legalFileLimit+1), "NOTICE.md": "Markdown notice",
	})
	preview, err := manager.Preview(ctx, source)
	if err != nil || preview.APIVersion != 2 || !preview.HasNotice || !reflect.DeepEqual(preview.PrivilegedCapabilities, []string{CapUIEntry}) || preview.UpstreamURL != "https://github.com/publisher/repo" {
		t.Fatalf("preview metadata: %#v, %v", preview, err)
	}
	capabilities := []string{}
	installed, err := manager.Install(ctx, InstallRequest{Root: source, PackageDigest: previewDigest(t, manager, source), Capabilities: &capabilities})
	if err != nil || !installed.HasNotice || installed.UpstreamURL != preview.UpstreamURL || !reflect.DeepEqual(installed.PrivilegedCapabilities, preview.PrivilegedCapabilities) || installed.Enabled {
		t.Fatalf("installed metadata: %#v, %v", installed, err)
	}
	mux := http.NewServeMux()
	manager.Register(mux)
	readLegal := func(id string, status int) PluginLegal {
		t.Helper()
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, APIPrefix+"/plugins/"+id+"/legal", nil))
		if response.Code != status {
			t.Fatalf("legal for %s: %d %s", id, response.Code, response.Body)
		}
		var legal PluginLegal
		if err := json.Unmarshal(response.Body.Bytes(), &legal); err != nil {
			t.Fatal(err)
		}
		return legal
	}
	legal := readLegal(installed.ID, http.StatusOK)
	if legal.ID != installed.ID || legal.License != "MIT" || legal.UpstreamRepo != installed.UpstreamRepo || legal.UpstreamURL != installed.UpstreamURL ||
		legal.UpstreamLicense != installed.UpstreamLicense || legal.UpstreamCommit != installed.UpstreamCommit ||
		legal.NoticeFile != "NOTICE" || !legal.NoticeTruncated || len(legal.NoticeText) != legalFileLimit ||
		!legal.LicenseTruncated || len(legal.LicenseText) != legalFileLimit-1 {
		t.Fatalf("legal metadata or truncation failed: license %d bytes, notice %d bytes, file %s, upstream %#v", len(legal.LicenseText), len(legal.NoticeText), legal.NoticeFile, legal.UpstreamURL)
	}
	dir := filepath.Join(root, "plugins", installed.ID)
	for _, name := range []string{"LICENSE", "NOTICE"} {
		target := filepath.Join(dir, name)
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(source, name), target); err != nil {
			t.Fatal(err)
		}
	}
	legal = readLegal(installed.ID, http.StatusOK)
	if legal.LicenseText != "" || legal.NoticeFile != "NOTICE.md" || legal.NoticeText != "Markdown notice" || legal.NoticeTruncated {
		t.Fatalf("legal API did not ignore symlinks or fall back to NOTICE.md: %s %q", legal.NoticeFile, legal.NoticeText)
	}
	if err := os.Remove(filepath.Join(dir, "NOTICE.md")); err != nil {
		t.Fatal(err)
	}
	if manager.Get2(installed.ID).HasNotice {
		t.Fatal("notice symlink counted as a notice file")
	}
	readLegal("test.missing", http.StatusNotFound)
}

func TestPluginAssetsContainmentAndSize(t *testing.T) {
	ctx := context.Background()
	manager, root := testManager(t, func() bool { return false })
	manifest := strings.Replace(testManifest, `"renderer.js"`, `"renderer.js","settings":"settings.js"`, 1)
	source := writePluginDir(t, t.TempDir(), manifest, map[string]string{
		"settings.js": "export function renderSettings(){}", "assets/nested/text.txt": "asset text",
		"assets/logo.bin": "\xff\x00\xfe", "private.txt": "private",
	})
	installed, err := manager.Install(ctx, InstallRequest{Root: source, PackageDigest: previewDigest(t, manager, source), Capabilities: confirmAll(t, source)})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	manager.Register(mux)
	get := func(name string, want int) map[string]string {
		t.Helper()
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, APIPrefix+"/plugins/"+installed.ID+"/assets/"+name, nil))
		if response.Code != want {
			t.Fatalf("asset %s: got status %d, want %d", name, response.Code, want)
		}
		asset := map[string]string{}
		if want == http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &asset); err != nil {
				t.Fatal(err)
			}
		}
		return asset
	}
	get("assets/nested/text.txt", http.StatusConflict)
	if _, err := manager.Enable(ctx, installed.ID); err != nil {
		t.Fatal(err)
	}
	get("renderer.js", http.StatusOK)
	get("settings.js", http.StatusOK)
	if asset := get("assets/nested/text.txt", http.StatusOK); asset["source"] != "asset text" || asset["path"] != "assets/nested/text.txt" {
		t.Fatalf("text asset: %#v", asset)
	}
	binary := get("assets/logo.bin", http.StatusOK)
	if bytes, err := base64.StdEncoding.DecodeString(binary["source"]); err != nil || string(bytes) != "\xff\x00\xfe" || binary["encoding"] != "base64" {
		t.Fatalf("binary asset was changed: %#v, %v", binary, err)
	}
	dir := filepath.Join(root, "plugins", installed.ID)
	for name, target := range map[string]string{
		"assets/link.txt": filepath.Join(dir, "assets", "nested", "text.txt"),
		"assets/linkdir":  filepath.Join(dir, "assets", "nested"),
		"assets/outside":  source,
	} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"LICENSE", "private.txt", "assets/%2e%2e/renderer.js", "%2Frenderer.js", "assets/nested/%2e%2e/logo.bin", "assets%2F%2e%2e%5Crenderer.js", "assets/link.txt", "assets/linkdir/text.txt", "assets/outside/LICENSE"} {
		get(path, http.StatusNotFound)
	}
	large, err := os.Create(filepath.Join(dir, "assets", "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := large.Truncate(assetLimit + 1); err != nil {
		large.Close()
		t.Fatal(err)
	}
	if err := large.Close(); err != nil {
		t.Fatal(err)
	}
	get("assets/large.bin", http.StatusRequestEntityTooLarge)
}

func TestReleaseBundledPreservesLocalSourceAtSameVersion(t *testing.T) {
	ctx := context.Background()
	manager, root := testManager(t, func() bool { return false })
	id := "test.override"
	source := writePluginDir(t, t.TempDir(), manifestFor(id, 2, []string{CapModelsReadV2}), nil)
	capabilities := []string{CapModelsReadV2}
	if _, err := manager.Install(ctx, InstallRequest{Root: source, PackageDigest: previewDigest(t, manager, source), Capabilities: &capabilities, ConfirmPrivileged: true}); err != nil {
		t.Fatal(err)
	}
	if err := manager.ReleaseBundled(ctx, map[string]map[string][]byte{
		id: {
			"plugin.json": []byte(manifestFor(id, 2, []string{CapModelsReadV2})),
			"renderer.js": []byte("export function activate(){ /* bundled */ }"),
			"LICENSE":     []byte("MIT"),
		},
	}); err != nil {
		t.Fatal(err)
	}
	got := manager.Get2(id)
	if got.Source != SourceLocal || got.Version != "1.0.0" {
		t.Fatalf("same-version ReleaseBundled overwrote local source: %#v", got)
	}
	data, err := os.ReadFile(filepath.Join(root, "plugins", id, "renderer.js"))
	if err != nil || strings.Contains(string(data), "bundled") {
		t.Fatalf("local plugin renderer was overwritten: %q, %v", string(data), err)
	}
}
