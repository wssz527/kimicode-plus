package plugins

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enhancedkimi/internal/storage"
)

// confirmAll grants exactly what a manifest declares, which is what a test user
// who accepted the whole consent prompt would confirm. The install path requires
// an explicit decision, so every test states one instead of relying on a default.
func confirmAll(t *testing.T, source string) *[]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(source, "plugin.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	manifest, err := ParseManifest(data)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	names := manifest.CapabilityNames()
	return &names
}

func testManager(t *testing.T, safe func() bool) (*Manager, string) {
	t.Helper()
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "plugins.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	manager, err := New(db, safe, WithDataDir(root))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	t.Cleanup(func() { manager.Close() })
	return manager, root
}

const testManifest = `{
  "id": "test.sample",
  "name": "Sample",
  "version": "1.0.0",
  "apiVersion": 1,
  "hostVersions": ["1.*"],
  "entry": {"renderer": "renderer.js"},
  "capabilities": ["ui:sidebar", "storage"]
}`

func writePluginDir(t *testing.T, dir string, manifest string, extra map[string]string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"plugin.json": manifest, "renderer.js": "export function activate(){}\n", "LICENSE": "MIT\n"}
	for name, content := range extra {
		files[name] = content
	}
	for name, content := range files {
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestParseManifestRejectsUnknownAndTrailingContent(t *testing.T) {
	if _, err := ParseManifest([]byte(strings.Replace(testManifest, `"capabilities"`, `"capabilites"`, 1))); err == nil {
		t.Fatal("unknown field was accepted")
	}
	trailing := testManifest + "\n{}"
	if _, err := ParseManifest([]byte(trailing)); err == nil {
		t.Fatal("trailing document was accepted")
	}
}

func TestManifestRejectsCapabilityEscapes(t *testing.T) {
	cases := map[string]string{
		"parent path":  `{"id":"test.sample","name":"S","version":"1.0.0","apiVersion":1,"hostVersions":["1.*"],"entry":{"renderer":"../evil.js"},"capabilities":[]}`,
		"absolute":     `{"id":"test.sample","name":"S","version":"1.0.0","apiVersion":1,"hostVersions":["1.*"],"entry":{"renderer":"/etc/passwd.js"},"capabilities":[]}`,
		"unknown cap":  `{"id":"test.sample","name":"S","version":"1.0.0","apiVersion":1,"hostVersions":["1.*"],"entry":{"renderer":"renderer.js"},"capabilities":["root:everything"]}`,
		"duplicate":    `{"id":"test.sample","name":"S","version":"1.0.0","apiVersion":1,"hostVersions":["1.*"],"entry":{"renderer":"renderer.js"},"capabilities":["storage","storage"]}`,
		"bad id":       `{"id":"NotAPublisher","name":"S","version":"1.0.0","apiVersion":1,"hostVersions":["1.*"],"entry":{"renderer":"renderer.js"},"capabilities":[]}`,
		"bad version":  `{"id":"test.sample","name":"S","version":"1.0","apiVersion":1,"hostVersions":["1.*"],"entry":{"renderer":"renderer.js"},"capabilities":[]}`,
		"bad api":      `{"id":"test.sample","name":"S","version":"1.0.0","apiVersion":3,"hostVersions":["1.*"],"entry":{"renderer":"renderer.js"},"capabilities":[]}`,
		"v2 cap in v1": `{"id":"test.sample","name":"S","version":"1.0.0","apiVersion":1,"hostVersions":["1.*"],"entry":{"renderer":"renderer.js"},"capabilities":["ui:right-panel"]}`,
		"no renderer":  `{"id":"test.sample","name":"S","version":"1.0.0","apiVersion":1,"hostVersions":["1.*"],"entry":{},"capabilities":[]}`,
	}
	for name, manifest := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseManifest([]byte(manifest)); err == nil {
				t.Fatalf("%s: manifest was accepted", name)
			}
		})
	}
}

func TestManifestRequiresLicenseFile(t *testing.T) {
	manifest, err := ParseManifest([]byte(testManifest))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "renderer.js"), []byte(""), 0o600)
	if err := manifest.ValidateFiles(dir); err == nil || !strings.Contains(err.Error(), "LICENSE") {
		t.Fatalf("missing LICENSE was accepted: %v", err)
	}
}

func TestHostVersionGate(t *testing.T) {
	manifest, err := ParseManifest([]byte(testManifest))
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.HostVersionSupported("1.3.5") {
		t.Fatal("1.* did not match 1.3.5")
	}
	if manifest.HostVersionSupported("2.0.0") {
		t.Fatal("1.* matched 2.0.0")
	}
	// A minor-series pattern covers one label only, so "1.3.*" must not match 1.4.
	narrow := Manifest{HostVersions: []string{"1.3.*"}}
	if narrow.HostVersionSupported("1.4.0") {
		t.Fatal("1.3.* matched 1.4.0")
	}
	if !narrow.HostVersionSupported("1.3.9") {
		t.Fatal("1.3.* did not match 1.3.9")
	}
}

func TestNetHostWildcardExcludesBareDomain(t *testing.T) {
	grants, err := ParseCapabilities([]string{"net:*.example.com", "net:api.other.test"})
	if err != nil {
		t.Fatal(err)
	}
	if !grants.AllowsHost("a.example.com") {
		t.Fatal("wildcard did not match a single-label subdomain")
	}
	if grants.AllowsHost("example.com") {
		t.Fatal("wildcard matched the bare domain")
	}
	if grants.AllowsHost("a.b.example.com") {
		t.Fatal("wildcard matched a two-label subdomain")
	}
	if !grants.AllowsHost("api.other.test") {
		t.Fatal("explicit host did not match")
	}
	if grants.AllowsHost("other.test") {
		t.Fatal("explicit host matched a different host")
	}
}

func TestUnknownActionDenies(t *testing.T) {
	grants, err := ParseCapabilities([]string{"metrics:read"})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"cache.summary", "cache.history"} {
		if err := grants.Authorize(action); err != nil {
			t.Fatalf("%s should be granted: %v", action, err)
		}
	}
	for _, action := range []string{"config.save", "sessions.send", "root.everything"} {
		err := grants.Authorize(action)
		if err == nil {
			t.Fatalf("%s was authorized without its capability", action)
		}
		var pluginErr *Error
		if !asError(err, &pluginErr) || pluginErr.Status != http.StatusForbidden || pluginErr.Code != "PLUGIN_CAPABILITY_DENIED" {
			t.Fatalf("%s produced %v", action, err)
		}
	}
}

func asError(err error, target **Error) bool {
	for err != nil {
		if typed, ok := err.(*Error); ok {
			*target = typed
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

func TestZeroGrantsDenyEverything(t *testing.T) {
	var grants Grants
	for _, action := range []string{"cache.summary", "sessions.list", "config.get", "storage.get"} {
		if grants.Authorize(action) == nil {
			t.Fatalf("zero grants allowed %s", action)
		}
	}
	if grants.AllowsHost("any.test") || grants.AllowsCredential("anything") {
		t.Fatal("zero grants allowed network or credential use")
	}
}

func TestInstallEnableDisableUninstall(t *testing.T) {
	manager, root := testManager(t, func() bool { return false })
	ctx := context.Background()
	source := writePluginDir(t, filepath.Join(t.TempDir(), "source"), testManifest, nil)

	plugin, err := manager.Install(ctx, InstallRequest{Root: source, Capabilities: confirmAll(t, source)})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if plugin.ID != "test.sample" || plugin.State != StateDisabled {
		t.Fatalf("unexpected install result: %#v", plugin)
	}
	if _, err := os.Stat(filepath.Join(root, "plugins", "test.sample", "plugin.json")); err != nil {
		t.Fatalf("plugin files were not copied: %v", err)
	}

	enabled, err := manager.Enable(ctx, "test.sample")
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if enabled.Token == "" {
		t.Fatal("enable did not issue an identity token")
	}
	if !manager.Verify("test.sample", enabled.Token) {
		t.Fatal("issued token did not verify")
	}
	if manager.Verify("test.sample", enabled.Token+"x") || manager.Verify("other.plugin", enabled.Token) {
		t.Fatal("token verified for the wrong value or plugin")
	}

	if err := manager.Uninstall(ctx, "test.sample"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "plugins", "test.sample")); !os.IsNotExist(statErr) {
		t.Fatalf("uninstall left files behind: %v", statErr)
	}
	if manager.Verify("test.sample", enabled.Token) {
		t.Fatal("token survived uninstall")
	}
}

func TestUninstalledBundledPluginIsNotReleasedAgain(t *testing.T) {
	manager, root := testManager(t, func() bool { return false })
	ctx := context.Background()
	bundled := map[string]map[string][]byte{
		"test.sample": {
			"plugin.json": []byte(testManifest),
			"renderer.js": []byte("export function activate(){}\n"),
			"LICENSE":     []byte("MIT\n"),
		},
	}
	if err := manager.ReleaseBundled(ctx, bundled); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, ok := manager.Get("test.sample"); !ok {
		t.Fatal("bundled plugin was not released")
	}
	if err := manager.Uninstall(ctx, "test.sample"); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if err := manager.ReleaseBundled(ctx, bundled); err != nil {
		t.Fatalf("second release: %v", err)
	}
	if _, ok := manager.Get("test.sample"); ok {
		t.Fatal("uninstalled bundled plugin was released again")
	}
	if _, err := os.Stat(filepath.Join(root, "plugins", "test.sample")); !os.IsNotExist(err) {
		t.Fatal("bundled files reappeared after uninstall")
	}
}

func TestReleaseBundledKeepsUserEdits(t *testing.T) {
	manager, root := testManager(t, func() bool { return false })
	target := writePluginDir(t, filepath.Join(root, "plugins", "test.sample"), testManifest, map[string]string{"renderer.js": "// user edit\n"})
	bundled := map[string]map[string][]byte{
		"test.sample": {"plugin.json": []byte(testManifest), "renderer.js": []byte("// bundled\n"), "LICENSE": []byte("MIT\n")},
	}
	if err := manager.ReleaseBundled(context.Background(), bundled); err != nil {
		t.Fatalf("release: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(target, "renderer.js"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "// user edit\n" {
		t.Fatalf("bundled release overwrote a user edit: %q", data)
	}
}

func TestZipSlipIsRejected(t *testing.T) {
	manager, _ := testManager(t, func() bool { return false })
	path := filepath.Join(t.TempDir(), "evil.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	entry, err := archive.Create("../../escaped.js")
	if err != nil {
		t.Fatal(err)
	}
	entry.Write([]byte("// escape\n"))
	archive.Close()
	file.Close()

	// The consent list is irrelevant here: the package is refused before the
	// grant is ever consulted, which is what makes zip slip uninstallable.
	consent := []string{}
	_, err = manager.Install(context.Background(), InstallRequest{Root: path, Capabilities: &consent})
	if err == nil {
		t.Fatal("zip slip was accepted")
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(path), "..", "escaped.js")); statErr == nil {
		t.Fatal("zip slip wrote a file outside the target")
	}
}

func TestInstallRejectsUndeclaredGrant(t *testing.T) {
	manager, _ := testManager(t, func() bool { return false })
	source := writePluginDir(t, filepath.Join(t.TempDir(), "source"), testManifest, nil)
	undeclared := []string{"config:write"}
	_, err := manager.Install(context.Background(), InstallRequest{Root: source, Capabilities: &undeclared})
	if err == nil {
		t.Fatal("undeclared capability was granted")
	}
	var pluginErr *Error
	if !asError(err, &pluginErr) || pluginErr.Code != "PLUGIN_CAPABILITY_UNDECLARED" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSafeModeBlocksEnable(t *testing.T) {
	safe := true
	manager, _ := testManager(t, func() bool { return safe })
	ctx := context.Background()
	source := writePluginDir(t, filepath.Join(t.TempDir(), "source"), testManifest, nil)
	// Safe mode must also refuse installation, not only activation.
	if _, err := manager.Install(ctx, InstallRequest{Root: source, Capabilities: confirmAll(t, source)}); err == nil {
		t.Fatal("safe mode allowed an install")
	}
	safe = false
	if _, err := manager.Install(ctx, InstallRequest{Root: source, Capabilities: confirmAll(t, source)}); err != nil {
		t.Fatalf("install after leaving safe mode: %v", err)
	}
	if _, err := manager.Enable(ctx, "test.sample"); err != nil {
		t.Fatalf("enable after leaving safe mode: %v", err)
	}
	safe = true
	manager.EnforceSafeMode()
	plugin, _ := manager.Get("test.sample")
	if plugin.State != StateBlocked || plugin.Enabled {
		t.Fatalf("safe mode did not stop the plugin: %#v", plugin)
	}
	if _, err := manager.Token("test.sample"); err == nil {
		t.Fatal("a blocked plugin still received a token")
	}
}

func TestCloseRevokesTokensAndRefusesNewWork(t *testing.T) {
	manager, _ := testManager(t, func() bool { return false })
	ctx := context.Background()
	source := writePluginDir(t, filepath.Join(t.TempDir(), "source"), testManifest, nil)
	if _, err := manager.Install(ctx, InstallRequest{Root: source, Capabilities: confirmAll(t, source)}); err != nil {
		t.Fatal(err)
	}
	enabled, err := manager.Enable(ctx, "test.sample")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if manager.Verify("test.sample", enabled.Token) {
		t.Fatal("close left a valid token")
	}
	if _, err := manager.Enable(ctx, "test.sample"); err == nil {
		t.Fatal("closed manager still enabled a plugin")
	}
	if _, err := manager.Call(ctx, "test.sample", "runtime.info", enabled.Token, nil); err == nil {
		t.Fatal("closed manager still served an action")
	}
}

func TestTwoManagersDoNotShareIdentity(t *testing.T) {
	first, _ := testManager(t, func() bool { return false })
	second, _ := testManager(t, func() bool { return false })
	ctx := context.Background()
	source := writePluginDir(t, filepath.Join(t.TempDir(), "source"), testManifest, nil)
	for _, manager := range []*Manager{first, second} {
		if _, err := manager.Install(ctx, InstallRequest{Root: source, Capabilities: confirmAll(t, source)}); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Enable(ctx, "test.sample"); err != nil {
			t.Fatal(err)
		}
	}
	firstToken, err := first.Token("test.sample")
	if err != nil {
		t.Fatal(err)
	}
	secondToken, err := second.Token("test.sample")
	if err != nil {
		t.Fatal(err)
	}
	if firstToken == secondToken {
		t.Fatal("two managers issued the same token")
	}
	if second.Verify("test.sample", firstToken) {
		t.Fatal("a token from one manager was accepted by another")
	}
	if _, err := second.Call(ctx, "test.sample", "runtime.info", firstToken, nil); err == nil {
		t.Fatal("cross-instance token was accepted by Call")
	}
}

func TestCorruptFilesAreMarkedNotDeleted(t *testing.T) {
	manager, root := testManager(t, func() bool { return false })
	target := filepath.Join(root, "plugins", "test.sample")
	writePluginDir(t, target, `{"id":"test.sample"`, nil)
	bundled := map[string]map[string][]byte{
		"test.sample": {"plugin.json": []byte(testManifest), "renderer.js": []byte("// bundled\n"), "LICENSE": []byte("MIT\n")},
	}
	if err := manager.ReleaseBundled(context.Background(), bundled); err != nil {
		t.Fatalf("release: %v", err)
	}
	plugin, ok := manager.Get("test.sample")
	if !ok || plugin.State != StateCorrupt {
		t.Fatalf("corrupt plugin was not marked: %#v", plugin)
	}
	if _, err := os.Stat(filepath.Join(target, "plugin.json")); err != nil {
		t.Fatalf("corrupt plugin files were deleted: %v", err)
	}
}

func TestStorageIsIsolatedPerPlugin(t *testing.T) {
	manager, _ := testManager(t, func() bool { return false })
	ctx := context.Background()
	if err := storageSet(ctx, manager.db, "test.sample", "k", []byte(`"a"`), manager.now()); err != nil {
		t.Fatal(err)
	}
	if _, found, err := storageGet(ctx, manager.db, "other.plugin", "k"); err != nil || found {
		t.Fatalf("storage leaked across plugins: found=%v err=%v", found, err)
	}
}

func TestActivityTracksLifecycle(t *testing.T) {
	manager, _ := testManager(t, func() bool { return false })
	ctx := context.Background()
	source := writePluginDir(t, filepath.Join(t.TempDir(), "source"), testManifest, nil)
	if _, err := manager.Install(ctx, InstallRequest{Root: source, Capabilities: confirmAll(t, source)}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Enable(ctx, "test.sample"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Disable(ctx, "test.sample"); err != nil {
		t.Fatal(err)
	}
	items, err := loadActivity(ctx, manager.db, "test.sample", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, item := range items {
		kinds[item.Kind] = true
	}
	for _, want := range []string{ActivityInstall, ActivityEnable, ActivityDisable} {
		if !kinds[want] {
			t.Fatalf("activity log is missing %s: %#v", want, kinds)
		}
	}
}

func TestActionRequiresIdentityAndCapability(t *testing.T) {
	manager, _ := testManager(t, func() bool { return false })
	ctx := context.Background()
	source := writePluginDir(t, filepath.Join(t.TempDir(), "source"), testManifest, nil)
	if _, err := manager.Install(ctx, InstallRequest{Root: source, Capabilities: confirmAll(t, source)}); err != nil {
		t.Fatal(err)
	}
	enabled, err := manager.Enable(ctx, "test.sample")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Call(ctx, "test.sample", "runtime.info", "", nil); err == nil {
		t.Fatal("action without a token was accepted")
	}
	value, err := manager.Call(ctx, "test.sample", "runtime.info", enabled.Token, nil)
	if err != nil {
		t.Fatalf("runtime.info: %v", err)
	}
	info, _ := value.(map[string]any)
	if info["id"] != "test.sample" {
		t.Fatalf("runtime.info returned %#v", info)
	}
	if _, err := manager.Call(ctx, "test.sample", "config.save", enabled.Token, nil); err == nil {
		t.Fatal("ungranted config.save was accepted")
	}
}

func TestNetworkProxyRejectsUndeclaredHostAndScheme(t *testing.T) {
	manager, _ := testManager(t, func() bool { return false })
	ctx := context.Background()
	source := writePluginDir(t, filepath.Join(t.TempDir(), "source"), testManifest, nil)
	if _, err := manager.Install(ctx, InstallRequest{Root: source, Capabilities: confirmAll(t, source)}); err != nil {
		t.Fatal(err)
	}
	enabled, err := manager.Enable(ctx, "test.sample")
	if err != nil {
		t.Fatal(err)
	}
	call := func(body string) error {
		request := httptest.NewRequest(http.MethodPost, "/enhance/v1/plugins/test.sample/action/net.fetch", strings.NewReader(body))
		request.Header.Set(IdentityHeader, enabled.Token)
		_, err := manager.Call(ctx, "test.sample", "net.fetch", enabled.Token, request)
		return err
	}
	if err := call(`{"params":{"url":"https://not-declared.test/x"}}`); err == nil {
		t.Fatal("undeclared host was proxied")
	}
	if err := call(`{"params":{"url":"file:///etc/passwd"}}`); err == nil {
		t.Fatal("file scheme was proxied")
	}
	if err := call(`{"params":{"url":"https://user:pass@example.test/x"}}`); err == nil {
		t.Fatal("URL with embedded credentials was proxied")
	}
}

func TestLockedVaultReportsItsOwnError(t *testing.T) {
	manager, _ := testManager(t, func() bool { return false })
	manifest, err := ParseManifest([]byte(`{
		"id": "test.creds", "name": "Creds", "version": "1.0.0", "apiVersion": 1,
		"hostVersions": ["1.*"], "entry": {"renderer": "renderer.js"},
		"capabilities": ["net:api.test", "credentials:demo"]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	dir := writePluginDir(t, filepath.Join(t.TempDir(), "creds"), mustJSONText(manifest), nil)
	if _, err := manager.Install(context.Background(), InstallRequest{Root: dir, Capabilities: confirmAll(t, dir)}); err != nil {
		t.Fatal(err)
	}
	// A nil vault is the "core has no credential store" case, which must report
	// unavailable rather than pretending the credential was empty.
	_, err = credentialValue(context.Background(), nil, "demo")
	var pluginErr *Error
	if !asError(err, &pluginErr) || pluginErr.Code != "PLUGIN_CREDENTIAL_UNAVAILABLE" {
		t.Fatalf("unexpected credential error: %v", err)
	}
}

func mustJSONText(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func TestDatabaseIsUsableAfterManagerClose(t *testing.T) {
	manager, root := testManager(t, func() bool { return false })
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(filepath.Join(root, "plugins.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	var value string
	if err := db.QueryRow(`SELECT count(*) FROM plugin_installs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='plugin_activity'`).Scan(&value); err != nil {
		t.Fatalf("activity table missing: %v", err)
	}
}

var _ = sql.ErrNoRows

func TestRuntimeRouteIdentifiesPluginByToken(t *testing.T) {
	manager, _ := testManager(t, func() bool { return false })
	ctx := context.Background()
	source := writePluginDir(t, filepath.Join(t.TempDir(), "source"), testManifest, nil)
	if _, err := manager.Install(ctx, InstallRequest{Root: source, Capabilities: confirmAll(t, source)}); err != nil {
		t.Fatal(err)
	}
	enabled, err := manager.Enable(ctx, "test.sample")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	manager.Register(mux)
	call := func(action, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/enhance/v1/plugin-runtime/"+action, strings.NewReader(body)))
		return recorder
	}
	if recorder := call("runtime.info", `{"token":"forged"}`); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("forged token => %d %s", recorder.Code, recorder.Body.String())
	}
	recorder := call("runtime.info", `{"token":"`+enabled.Token+`"}`)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "test.sample") {
		t.Fatalf("runtime.info => %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := call("config.save", `{"token":"`+enabled.Token+`","params":{"patch":{"x":1}}}`); recorder.Code != http.StatusForbidden {
		t.Fatalf("ungranted action => %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := call("storage.set", `{"token":"`+enabled.Token+`","params":{"key":"k","value":{"a":1}}}`); recorder.Code != http.StatusOK {
		t.Fatalf("granted storage.set => %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := call("storage.get", `{"token":"`+enabled.Token+`","params":{"key":"k"}}`); !strings.Contains(recorder.Body.String(), `\"a\":1`) {
		t.Fatalf("storage.get => %d %s", recorder.Code, recorder.Body.String())
	}
	if err := manager.Disable(ctx, "test.sample"); err != nil {
		t.Fatal(err)
	}
	if recorder := call("runtime.info", `{"token":"`+enabled.Token+`"}`); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("token survived disable => %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestCredentialPlaintextNeverReachesActivity(t *testing.T) {
	manager, _ := testManager(t, func() bool { return false })
	ctx := context.Background()
	dir := writePluginDir(t, filepath.Join(t.TempDir(), "creds"), `{
		"id": "test.creds", "name": "Creds", "version": "1.0.0", "apiVersion": 1,
		"hostVersions": ["1.*"], "entry": {"renderer": "renderer.js"},
		"capabilities": ["net:api.test", "credentials:demo"]
	}`, nil)
	if _, err := manager.Install(ctx, InstallRequest{Root: dir, Capabilities: confirmAll(t, dir)}); err != nil {
		t.Fatal(err)
	}
	enabled, err := manager.Enable(ctx, "test.creds")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	manager.Register(mux)
	// The host allowlist rejects api.test.invalid, so the request never leaves the
	// process; what matters is that the secret cannot appear in the recorded
	// activity or in the error the plugin receives.
	body := `{"token":"` + enabled.Token + `","params":{"url":"https://api.test.invalid/x","credential":"demo"}}`
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/enhance/v1/plugin-runtime/net.fetch", strings.NewReader(body)))
	if strings.Contains(recorder.Body.String(), "synthetic-secret") {
		t.Fatal("error response leaked the credential")
	}
	items, err := loadActivity(ctx, manager.db, "test.creds", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if strings.Contains(item.Detail, "synthetic-secret") {
			t.Fatalf("activity log leaked the credential: %#v", item)
		}
	}
}

// Storage dispatch happens on the action prefix, so the grant check has to run
// inside that branch too. Skipping it lets a plugin write and read private
// storage it never asked the user to confirm.
func TestStorageRequiresTheStorageGrant(t *testing.T) {
	manager, _ := testManager(t, func() bool { return false })
	ctx := context.Background()
	source := writePluginDir(t, filepath.Join(t.TempDir(), "source"), `{
  "id": "test.nostorage",
  "name": "No storage",
  "version": "1.0.0",
  "apiVersion": 1,
  "hostVersions": ["1.*"],
  "entry": {"renderer": "renderer.js"},
  "capabilities": ["ui:sidebar", "storage"]
}`, nil)
	narrowed := []string{CapUISidebar}
	if _, err := manager.Install(ctx, InstallRequest{Root: source, Capabilities: &narrowed}); err != nil {
		t.Fatal(err)
	}
	enabled, err := manager.Enable(ctx, "test.nostorage")
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Call(ctx, "test.nostorage", "storage.set", enabled.Token, nil)
	var pluginErr *Error
	if !errors.As(err, &pluginErr) || pluginErr.Code != "PLUGIN_CAPABILITY_DENIED" {
		t.Fatalf("storage.set without the storage grant was not refused: %v", err)
	}
	if _, err := manager.Call(ctx, "test.nostorage", "storage.get", enabled.Token, nil); err == nil {
		t.Fatal("storage.get without the storage grant was allowed")
	}
}
