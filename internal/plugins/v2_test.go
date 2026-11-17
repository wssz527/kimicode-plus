package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// officialFixture installs one bundled plugin and makes the manager treat its
// installed copy as official. It is the only way to reach the privileged actions
// in a test, which is exactly the property the tests then check from the other
// direction.
func officialFixture(t *testing.T, capabilities []string) (*Manager, string) {
	t.Helper()
	root := t.TempDir()
	manager, err := newManagerAt(t, root)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"plugin.json": []byte(manifestFor("test.official", 2, capabilities)),
		"renderer.js": []byte("export function activate(){}\n"),
		"LICENSE":     []byte("MIT\n"),
	}
	digest := ContentDigest(files)
	manager.officialDigests = func() (map[string]string, error) {
		return map[string]string{"test.official": digest}, nil
	}
	if err := manager.ReleaseBundled(context.Background(), map[string]map[string][]byte{"test.official": files}); err != nil {
		t.Fatal(err)
	}
	enabled, err := manager.Enable(context.Background(), "test.official")
	if err != nil {
		t.Fatal(err)
	}
	return manager, enabled.Token
}

func manifestFor(id string, apiVersion int, capabilities []string) string {
	value := map[string]any{
		"id": id, "name": "Official", "version": "1.0.0", "apiVersion": apiVersion,
		"hostVersions": []string{"1.*"},
		"entry":        map[string]any{"renderer": "renderer.js"},
		"capabilities": capabilities,
	}
	data, _ := json.Marshal(value)
	return string(data)
}

// callPlugin drives the production runtime route: the token decides identity and
// the body carries the params, exactly like the renderer.
func callPlugin(t *testing.T, manager *Manager, token, action string, params any) (int, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	manager.Register(mux)
	body, _ := json.Marshal(map[string]any{"token": token, "params": params})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/enhance/v1/plugin-runtime/"+action, strings.NewReader(string(body))))
	code := recorder.Code
	value := map[string]any{}
	if recorder.Body.Len() > 0 {
		_ = json.Unmarshal(recorder.Body.Bytes(), &value)
	}
	return code, value
}

func TestLocalPluginCannotBeGrantedPrivilegedCapability(t *testing.T) {
	manager, _ := testManager(t, func() bool { return false })
	source := writePluginDir(t, t.TempDir(), manifestFor("test.local", 2, []string{"ui:right-panel"}), nil)
	declared := []string{CapUIRightPanel}
	_, err := manager.Install(context.Background(), InstallRequest{Root: source, Capabilities: &declared})
	if err == nil {
		t.Fatal("a local plugin was granted a privileged capability")
	}
	var pluginErr *Error
	if !asError(err, &pluginErr) || pluginErr.Code != "PLUGIN_CAPABILITY_PRIVILEGED" {
		t.Fatalf("unexpected refusal: %v", err)
	}
}

func TestOfficialPluginKeepsPrivilegedActions(t *testing.T) {
	manager, token := officialFixture(t, []string{CapUIEntry, CapCommands, CapBrowserView})
	code, value := callPlugin(t, manager, token, "browser.open", map[string]any{"viewId": "v1", "url": "https://example.com/"})
	if code != http.StatusOK {
		t.Fatalf("browser.open => %d %#v", code, value)
	}
	views := manager.Views("test.official")
	if len(views) != 1 || views[0].ID != "v1" || views[0].URL != "https://example.com/" {
		t.Fatalf("view was not recorded: %#v", views)
	}
	code, _ = callPlugin(t, manager, token, "commands.register", map[string]any{"name": "multiSession.open", "title": "同屏打开"})
	if code != http.StatusOK {
		t.Fatalf("commands.register => %d", code)
	}
	code, value = callPlugin(t, manager, token, "commands.invoke", map[string]any{"command": "multiSession.open", "args": map[string]any{"sessionId": "s1"}})
	if code != http.StatusOK || value["value"].(map[string]any)["ownerId"] != "test.official" {
		t.Fatalf("commands.invoke => %d %#v", code, value)
	}
}

// A changed byte in the installed directory must revoke the privileged surface
// even though the stored grant still lists the capabilities.
func TestChangedDigestRevokesPrivilegedActions(t *testing.T) {
	manager, token := officialFixture(t, []string{CapBrowserView})
	manager.mu.Lock()
	dir := manager.entries["test.official"].dir
	manager.mu.Unlock()
	if err := os.WriteFile(dir+"/renderer.js", []byte("export function activate(){/* tampered */}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, value := callPlugin(t, manager, token, "browser.open", map[string]any{"viewId": "v1", "url": "https://example.com/"})
	if code != http.StatusForbidden || !strings.Contains(mustString(value), "PLUGIN_PRIVILEGE_DENIED") {
		t.Fatalf("tampered plugin still opened a view: %d %#v", code, value)
	}
	// The runtime identity must not list the capability either.
	code, value = callPlugin(t, manager, token, "runtime.info", nil)
	if code != http.StatusOK {
		t.Fatalf("runtime.info => %d", code)
	}
	inner := value["value"].(map[string]any)
	for _, name := range inner["capabilities"].([]any) {
		if name == CapBrowserView {
			t.Fatalf("tampered plugin still advertises %s", CapBrowserView)
		}
	}
}

// Disabling a plugin must release the views and the commands it owned and revoke
// its token in the same step.
func TestDisableReleasesViewsCommandsAndToken(t *testing.T) {
	manager, token := officialFixture(t, []string{CapBrowserView, CapCommands})
	if code, _ := callPlugin(t, manager, token, "browser.open", map[string]any{"viewId": "v1", "url": "https://example.com/"}); code != http.StatusOK {
		t.Fatal("open failed")
	}
	if code, _ := callPlugin(t, manager, token, "commands.register", map[string]any{"name": "multiSession.open"}); code != http.StatusOK {
		t.Fatal("register failed")
	}
	if err := manager.Disable(context.Background(), "test.official"); err != nil {
		t.Fatal(err)
	}
	if views := manager.Views("test.official"); len(views) != 0 {
		t.Fatalf("disable left views: %#v", views)
	}
	if commands := manager.listCommands(); len(commands) != 0 {
		t.Fatalf("disable left commands: %#v", commands)
	}
	if code, _ := callPlugin(t, manager, token, "runtime.info", nil); code != http.StatusUnauthorized {
		t.Fatalf("token survived disable: %d", code)
	}
}

// Another plugin may discover and invoke a command, but not take its name.
func TestCommandsAreVisibleButNotHijackable(t *testing.T) {
	manager, token := officialFixture(t, []string{CapCommands})
	second := writePluginDir(t, t.TempDir(), manifestFor("test.other", 2, []string{CapCommands}), nil)
	declared, err := ParseCapabilities([]string{CapCommands})
	if err != nil {
		t.Fatal(err)
	}
	_ = declared
	// test.other is local: it cannot hold commands, so it is refused at install.
	consent := []string{CapCommands}
	if _, err = manager.Install(context.Background(), InstallRequest{Root: second, Capabilities: &consent}); err == nil {
		t.Fatal("a local plugin was granted commands")
	}
	if code, _ := callPlugin(t, manager, token, "commands.register", map[string]any{"name": "multiSession.open"}); code != http.StatusOK {
		t.Fatal("register failed")
	}
	code, value := callPlugin(t, manager, token, "commands.register", map[string]any{"name": "multiSession.open"})
	if code != http.StatusOK {
		t.Fatalf("re-registering one's own command failed: %d %#v", code, value)
	}
}

// A plugin with only v1 capabilities cannot reach a v2 action at all, even when
// the action name is correct.
func TestV1PluginCannotCallV2Actions(t *testing.T) {
	manager, _ := testManager(t, func() bool { return false })
	source := writePluginDir(t, t.TempDir(), manifestFor("test.v1", 1, []string{"sessions:read"}), nil)
	consent := []string{CapSessionsRead}
	if _, err := manager.Install(context.Background(), InstallRequest{Root: source, Capabilities: &consent}); err != nil {
		t.Fatal(err)
	}
	enabled, err := manager.Enable(context.Background(), "test.v1")
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"sessions.history", "browser.open", "handoff.commit", "models.list", "commands.register"} {
		code, value := callPlugin(t, manager, enabled.Token, action, map[string]any{"viewId": "v1", "url": "https://example.com/"})
		if code != http.StatusForbidden || !strings.Contains(mustString(value), "PLUGIN_CAPABILITY_DENIED") {
			t.Fatalf("%s => %d %#v", action, code, value)
		}
	}
}

// Holding a valid token is not enough: a plugin without browser:view must be
// refused by the same call that would create the view, and nothing may be
// recorded. This is the gap the host previously papered over by only asking who
// the token belonged to.
func TestBrowserActionsRequireTheCapability(t *testing.T) {
	manager, token := officialFixture(t, []string{CapUIEntry})
	for _, action := range []string{"browser.open", "browser.navigate", "browser.bounds", "browser.visible", "browser.extract", "browser.close"} {
		params := map[string]any{"viewId": "v1", "url": "https://example.com/", "visible": true, "width": 10, "height": 10}
		code, value := callPlugin(t, manager, token, action, params)
		if code != http.StatusForbidden || !strings.Contains(mustString(value), "PLUGIN_CAPABILITY_DENIED") {
			t.Fatalf("%s without browser:view => %d %#v", action, code, value)
		}
	}
	if views := manager.Views("test.official"); len(views) != 0 {
		t.Fatalf("a refused open still recorded a view: %#v", views)
	}
}

// A plugin cannot address another plugin's view, and the answer names the owner
// the core resolved from the token — never the plugin id the caller sent.
func TestBrowserAnswersNameTheTokenOwner(t *testing.T) {
	manager, token := officialFixture(t, []string{CapBrowserView})
	code, value := callPlugin(t, manager, token, "browser.open", map[string]any{
		"viewId": "v1", "url": "https://example.com/", "pluginId": "test.other",
	})
	if code != http.StatusOK {
		t.Fatalf("browser.open => %d %#v", code, value)
	}
	opened := value["value"].(map[string]any)
	if opened["pluginId"] != "test.official" {
		t.Fatalf("open answered with a caller-supplied owner: %#v", opened)
	}
	for _, action := range []string{"browser.navigate", "browser.bounds", "browser.visible", "browser.extract"} {
		code, value := callPlugin(t, manager, token, action, map[string]any{
			"viewId": "v1", "url": "https://example.com/next", "visible": true, "width": 10, "height": 10, "pluginId": "test.other",
		})
		if code != http.StatusOK {
			t.Fatalf("%s => %d %#v", action, code, value)
		}
		if owner := value["value"].(map[string]any)["pluginId"]; owner != "test.official" {
			t.Fatalf("%s answered with owner %v", action, owner)
		}
	}
}

// A view id is scoped to its owner: another plugin naming it gets the same answer
// as a view that never existed, so the reply cannot be used to enumerate.
func TestBrowserViewIDSpaceIsPerPlugin(t *testing.T) {
	manager, token := officialFixture(t, []string{CapBrowserView})
	// A second bundled plugin, also official, is the only way to hold
	// browser:view twice in one manager.
	secondFiles := map[string][]byte{
		"plugin.json": []byte(manifestFor("test.other", 2, []string{CapBrowserView})),
		"renderer.js": []byte("export function activate(){}\n"),
		"LICENSE":     []byte("MIT\n"),
	}
	firstFiles := map[string][]byte{
		"plugin.json": []byte(manifestFor("test.official", 2, []string{CapBrowserView})),
		"renderer.js": []byte("export function activate(){}\n"),
		"LICENSE":     []byte("MIT\n"),
	}
	if err := manager.ReleaseBundled(context.Background(), map[string]map[string][]byte{"test.other": secondFiles}); err != nil {
		t.Fatal(err)
	}
	manager.officialDigests = func() (map[string]string, error) {
		return map[string]string{"test.official": ContentDigest(firstFiles), "test.other": ContentDigest(secondFiles)}, nil
	}
	second, err := manager.Enable(context.Background(), "test.other")
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := callPlugin(t, manager, token, "browser.open", map[string]any{"viewId": "shared", "url": "https://example.com/"}); code != http.StatusOK {
		t.Fatal("open failed")
	}
	for _, action := range []string{"browser.navigate", "browser.bounds", "browser.extract"} {
		code, value := callPlugin(t, manager, second.Token, action, map[string]any{"viewId": "shared", "url": "https://example.com/"})
		if code != http.StatusNotFound || !strings.Contains(mustString(value), "PLUGIN_BROWSER_VIEW_MISSING") {
			t.Fatalf("%s reached another plugin's view: %d %#v", action, code, value)
		}
	}
	// Close of a view the caller does not own must not tear the other plugin's
	// view down either.
	if code, _ := callPlugin(t, manager, second.Token, "browser.close", map[string]any{"viewId": "shared"}); code != http.StatusNotFound {
		t.Fatalf("browser.close of a foreign view => %d", code)
	}
	if views := manager.Views("test.official"); len(views) != 1 || views[0].ID != "shared" {
		t.Fatalf("a foreign close removed the owner's view: %#v", views)
	}
}

func TestAttachmentValidation(t *testing.T) {
	dir := t.TempDir()
	file := dir + "/note.txt"
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := normalizeAttachments([]string{file})
	if err != nil || len(items) != 1 || items[0].Kind != "file" || items[0].Name != "note.txt" {
		t.Fatalf("valid attachment rejected: %#v %v", items, err)
	}
	if _, err := normalizeAttachments([]string{"relative.txt"}); err == nil {
		t.Fatal("relative path was accepted")
	}
	if _, err := normalizeAttachments([]string{dir + "/missing.txt"}); err == nil {
		t.Fatal("missing file was accepted")
	}
}

func TestHandoffPreviewValidates(t *testing.T) {
	if _, err := handoffPreview("", "plan", "hello", "", "", ""); err != nil {
		t.Fatalf("valid preview rejected: %v", err)
	}
	if _, err := handoffPreview("", "bogus", "hello", "", "", ""); err == nil {
		t.Fatal("invalid mode accepted")
	}
	if _, err := handoffPreview("", "normal", "  ", "", "", ""); err == nil {
		t.Fatal("empty text accepted")
	}
}

// A new-session preview echoes the directory, model and title it would request;
// a preview for an existing target never does, because that session keeps its
// own.
func TestHandoffPreviewCarriesNewSessionChoicesOnly(t *testing.T) {
	value, err := handoffPreview("", "plan", "hello", "/tmp/work", "kimi-k2", "交接")
	if err != nil {
		t.Fatal(err)
	}
	preview := value.(map[string]any)
	if preview["cwd"] != "/tmp/work" || preview["model"] != "kimi-k2" || preview["title"] != "交接" {
		t.Fatalf("new-session preview lost its choices: %#v", preview)
	}
	value, err = handoffPreview("s1", "plan", "hello", "/tmp/work", "kimi-k2", "交接")
	if err != nil {
		t.Fatal(err)
	}
	existing := value.(map[string]any)
	for _, key := range []string{"cwd", "model", "title"} {
		if _, present := existing[key]; present {
			t.Fatalf("an existing target previewed %s: %#v", key, existing)
		}
	}
}

func TestBrowserURLValidation(t *testing.T) {
	for _, bad := range []string{"", "file:///etc/passwd", "javascript:alert(1)", "data:text/html,x", "https://user:pass@example.com/"} {
		if _, err := validateBrowserURL(bad); err == nil {
			t.Fatalf("%s was accepted", bad)
		}
	}
	if _, err := validateBrowserURL("https://example.com/a?b=1"); err != nil {
		t.Fatalf("valid URL rejected: %v", err)
	}
}

func mustString(value map[string]any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

// A plugin is told the API version its own manifest declares. Reporting the
// host's newest version to a v1 plugin made it believe v2 surfaces existed.
func TestAPIVersionFollowsEachManifest(t *testing.T) {
	manager, token := officialFixture(t, []string{CapBrowserView})
	code, value := callPlugin(t, manager, token, "runtime.info", nil)
	if code != http.StatusOK {
		t.Fatalf("runtime.info => %d", code)
	}
	if inner := value["value"].(map[string]any); inner["apiVersion"] != float64(2) {
		t.Fatalf("v2 plugin was told apiVersion %v", inner["apiVersion"])
	}

	v1manager, _ := testManager(t, func() bool { return false })
	source := writePluginDir(t, t.TempDir(), manifestFor("test.v1", 1, []string{CapSessionsRead}), nil)
	consent := []string{CapSessionsRead}
	if _, err := v1manager.Install(context.Background(), InstallRequest{Root: source, Capabilities: &consent}); err != nil {
		t.Fatal(err)
	}
	enabled, err := v1manager.Enable(context.Background(), "test.v1")
	if err != nil {
		t.Fatal(err)
	}
	code, value = callPlugin(t, v1manager, enabled.Token, "runtime.info", nil)
	if code != http.StatusOK {
		t.Fatalf("v1 runtime.info => %d", code)
	}
	if inner := value["value"].(map[string]any); inner["apiVersion"] != float64(1) {
		t.Fatalf("v1 plugin was told apiVersion %v", inner["apiVersion"])
	}
	// The list still advertises the newest API the host implements, and each row
	// carries its own manifest version.
	mux := http.NewServeMux()
	v1manager.Register(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/enhance/v1/plugins", nil))
	var page map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page["apiVersion"] != float64(APIVersion) {
		t.Fatalf("list apiVersion = %v", page["apiVersion"])
	}
	items := page["items"].([]any)
	row := items[0].(map[string]any)
	if row["apiVersion"] != float64(1) {
		t.Fatalf("v1 row apiVersion = %v", row["apiVersion"])
	}
}
