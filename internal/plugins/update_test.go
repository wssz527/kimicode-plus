package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"enhancedkimi/internal/storage"
)

func makePluginZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write(files[name]); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func makeSignedEntry(t *testing.T, files map[string][]byte, minCore, notes string) (OfficialPluginEntry, []byte) {
	t.Helper()
	manifest, err := ParseManifest(files["plugin.json"])
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	archive := makePluginZip(t, files)
	sum := sha256.Sum256(archive)
	return OfficialPluginEntry{
		ID:             manifest.ID,
		Name:           manifest.Name,
		Version:        manifest.Version,
		APIVersion:     manifest.APIVersion,
		Description:    manifest.Description,
		HostVersions:   manifest.HostVersions,
		MinCoreVersion: minCore,
		Capabilities:   manifest.CapabilityNames(),
		Archive:        manifest.ID + "-" + manifest.Version + ".zip",
		ArchiveBytes:   int64(len(archive)),
		ArchiveSHA256:  hex.EncodeToString(sum[:]),
		ContentDigest:  ContentDigest(files),
		PublishedAt:    "2026-11-23T00:00:00Z",
		ReleaseNotes:   notes,
	}, archive
}

func TestSignAndVerifyPluginCatalogRejectsTampering(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	files := map[string][]byte{
		"plugin.json": []byte(`{"id":"kimicode-plus.kct","name":"KCT","version":"1.0.5","apiVersion":1,"hostVersions":["1.*"],"entry":{"renderer":"renderer.js"},"capabilities":["ui:thinking","storage"]}`),
		"renderer.js": []byte("export function activate() {}\n"),
	}
	entry, _ := makeSignedEntry(t, files, "0.2.8", "Fix thinking style")
	signed, err := SignPluginCatalog(OfficialPluginCatalog{
		GeneratedAt: "2026-11-23T00:00:00Z",
		Plugins:     []OfficialPluginEntry{entry},
	}, DefaultOfficialKeyID, priv)
	if err != nil {
		t.Fatalf("sign catalog: %v", err)
	}

	keys := map[string]ed25519.PublicKey{DefaultOfficialKeyID: pub}
	verified, err := VerifySignedPluginManifest(signed, keys)
	if err != nil {
		t.Fatalf("verify catalog: %v", err)
	}
	if len(verified.Plugins) != 1 || verified.Plugins[0].Version != "1.0.5" {
		t.Fatalf("unexpected catalog: %+v", verified)
	}

	// Tampering any byte inside manifest must fail signature verification.
	tampered := bytes.Replace(signed, []byte("1.0.5"), []byte("1.0.6"), 1)
	if _, err := VerifySignedPluginManifest(tampered, keys); err == nil {
		t.Fatal("expected tampered manifest to fail verification")
	}

	// Unknown key must fail verification.
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := VerifySignedPluginManifest(signed, map[string]ed25519.PublicKey{DefaultOfficialKeyID: otherPub}); err == nil {
		t.Fatal("expected mismatched key to fail verification")
	}
}

func TestOfficialPluginOnlineUpdateDualSourceAndV2Persistence(t *testing.T) {
	ctx := context.Background()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	keys := map[string]ed25519.PublicKey{DefaultOfficialKeyID: pub}

	// Bundled v1.0.0 privileged v2 plugin.
	v1Files := map[string][]byte{
		"plugin.json": []byte(`{"id":"kimicode-plus.scheduler","name":"定时任务","version":"1.0.0","apiVersion":2,"hostVersions":["1.*"],"entry":{"renderer":"renderer.js"},"capabilities":["ui:sidebar-nav","schedule:manage","storage"]}`),
		"renderer.js": []byte("export function activate() { return 'v1'; }\n"),
		"LICENSE":     []byte("MIT\n"),
	}
	// Online v1.0.1 update with the same capabilities.
	v2Files := map[string][]byte{
		"plugin.json": []byte(`{"id":"kimicode-plus.scheduler","name":"定时任务","version":"1.0.1","apiVersion":2,"hostVersions":["1.*"],"entry":{"renderer":"renderer.js"},"capabilities":["ui:sidebar-nav","schedule:manage","storage"]}`),
		"renderer.js": []byte("export function activate() { return 'v2-fixed'; }\n"),
		"LICENSE":     []byte("MIT\n"),
	}
	entryV2, archiveV2 := makeSignedEntry(t, v2Files, "0.2.8", "修复定时任务视图")
	signedManifest, err := SignPluginCatalog(OfficialPluginCatalog{
		GeneratedAt: "2026-11-23T00:00:00Z",
		Plugins:     []OfficialPluginEntry{entryV2},
	}, DefaultOfficialKeyID, priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Mirror (CNB) fails with 503; GitHub fallback succeeds.
	var mirrorHits atomic.Int32
	mirrorSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrorHits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer mirrorSrv.Close()

	githubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/owner/repo/releases/download/plugins-latest/plugins-manifest.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(signedManifest)
		case "/owner/repo/releases/download/plugins-latest/" + entryV2.Archive:
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(archiveV2)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer githubSrv.Close()

	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "plugins.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	bundledSet := map[string]map[string][]byte{"kimicode-plus.scheduler": v1Files}
	bundledDigests := map[string]string{"kimicode-plus.scheduler": ContentDigest(v1Files)}
	newMgr := func() *Manager {
		mgr, err := New(
			db,
			func() bool { return false },
			WithDataDir(root),
			WithHostVersion("1.3.7"),
			WithBundled(func() (map[string]map[string][]byte, error) { return bundledSet, nil }),
			WithOfficial(func() (map[string]string, error) { return bundledDigests, nil }),
			WithUpdater(UpdateOptions{
				CoreVersion:          "0.2.8",
				Repo:                 "owner/repo",
				MirrorReleaseBaseURL: mirrorSrv.URL,
				ReleaseBaseURL:       githubSrv.URL,
				AllowInsecureHTTP:    true,
				OfficialKeys:         keys,
			}),
		)
		if err != nil {
			t.Fatalf("new manager: %v", err)
		}
		return mgr
	}

	mgr := newMgr()
	var changeNotifications atomic.Int32
	mgr.UseOnChange(func() { changeNotifications.Add(1) })
	if err := mgr.ReleaseBundled(ctx, bundledSet); err != nil {
		t.Fatalf("release bundled: %v", err)
	}
	if err := mgr.SetPluginStorage(ctx, "kimicode-plus.scheduler", "user.pref", []byte(`{"keep":true}`)); err != nil {
		t.Fatalf("seed storage: %v", err)
	}
	enabledV1, err := mgr.Enable(ctx, "kimicode-plus.scheduler")
	if err != nil {
		t.Fatalf("enable v1: %v", err)
	}
	if !enabledV1.Official {
		t.Fatal("v1 should be marked official")
	}
	oldToken := enabledV1.Token

	// Check updates via HTTP route.
	mux := http.NewServeMux()
	mgr.Register(mux)
	checkReq := httptest.NewRequest(http.MethodPost, "/enhance/v1/plugins/updates/check", nil)
	checkRec := httptest.NewRecorder()
	mux.ServeHTTP(checkRec, checkReq)
	if checkRec.Code != http.StatusOK {
		t.Fatalf("check updates failed: %d %s", checkRec.Code, checkRec.Body.String())
	}
	if mirrorHits.Load() == 0 {
		t.Fatal("expected mirror to be tried before GitHub fallback")
	}
	var status UpdateStatus
	if err := json.Unmarshal(checkRec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode check status: %v", err)
	}
	if status.AvailableCount != 1 {
		t.Fatalf("expected 1 available update, got %+v", status)
	}
	item := status.Items["kimicode-plus.scheduler"]
	if !item.UpdateAvailable || !item.CanApply || item.NeedsConsent || item.LatestVersion != "1.0.1" {
		t.Fatalf("unexpected update item: %+v", item)
	}

	// Apply online update via HTTP route.
	applyReq := httptest.NewRequest(http.MethodPost, "/enhance/v1/plugins/kimicode-plus.scheduler/update", strings.NewReader(`{"expectedVersion":"1.0.1"}`))
	applyRec := httptest.NewRecorder()
	mux.ServeHTTP(applyRec, applyReq)
	if applyRec.Code != http.StatusOK {
		t.Fatalf("apply update failed: %d %s", applyRec.Code, applyRec.Body.String())
	}
	var updated Plugin
	if err := json.Unmarshal(applyRec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode updated plugin: %v", err)
	}
	if updated.Version != "1.0.1" || !updated.Enabled || !updated.Official {
		t.Fatalf("expected enabled official v1.0.1 after hot update, got %+v", updated)
	}
	if updated.Token == "" || updated.Token == oldToken {
		t.Fatalf("expected rotated renderer token after update")
	}

	// Privileged asset serving must succeed with the signed official update.
	assetReq := httptest.NewRequest(http.MethodGet, "/enhance/v1/plugins/kimicode-plus.scheduler/assets/renderer.js", nil)
	assetRec := httptest.NewRecorder()
	mux.ServeHTTP(assetRec, assetReq)
	if assetRec.Code != http.StatusOK || !strings.Contains(assetRec.Body.String(), "v2-fixed") {
		t.Fatalf("privileged asset fetch failed after online update: %d %s", assetRec.Code, assetRec.Body.String())
	}

	// Storage must survive the online update.
	storedVal, ok, err := mgr.PluginStorage(ctx, "kimicode-plus.scheduler", "user.pref")
	if err != nil || !ok || string(storedVal) != `{"keep":true}` {
		t.Fatalf("storage lost after update: ok=%v val=%q err=%v", ok, string(storedVal), err)
	}

	// Simulate core restart against the same DB with the OLDER v1.0.0 bundled binary:
	// ReleaseBundled must keep v1.0.1 AND keep Official=true and privileged v2 access!
	if err := mgr.Close(); err != nil {
		t.Fatalf("close mgr: %v", err)
	}
	mgr2 := newMgr()
	defer mgr2.Close()
	if err := mgr2.ReleaseBundled(ctx, bundledSet); err != nil {
		t.Fatalf("release bundled after restart: %v", err)
	}
	afterRestart, ok := mgr2.Get("kimicode-plus.scheduler")
	if !ok {
		t.Fatal("plugin missing after restart")
	}
	if afterRestart.Version != "1.0.1" || !afterRestart.Official || !afterRestart.Enabled {
		t.Fatalf("expected persisted official v1.0.1 after restart, got %+v", afterRestart)
	}
	if !contains(afterRestart.Capabilities, CapScheduleManage) {
		t.Fatalf("expected v2 privileged capability to stay intact after restart: %+v", afterRestart.Capabilities)
	}

	// Tampering a file on disk after signed update must revoke official & v2 privileges.
	if err := os.WriteFile(filepath.Join(root, "plugins", "kimicode-plus.scheduler", "renderer.js"), []byte("export function activate(){/*tampered*/}\n"), 0o600); err != nil {
		t.Fatalf("tamper file: %v", err)
	}
	tamperedView, _ := mgr2.Get("kimicode-plus.scheduler")
	if tamperedView.Official || contains(tamperedView.Capabilities, CapScheduleManage) {
		t.Fatalf("tampered online-updated plugin must lose official status and v2 privilege: %+v", tamperedView)
	}
}

func TestOfficialPluginUpdateConsentAndMinCoreGates(t *testing.T) {
	ctx := context.Background()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	keys := map[string]ed25519.PublicKey{DefaultOfficialKeyID: pub}

	v1Files := map[string][]byte{
		"plugin.json": []byte(`{"id":"kimicode-plus.kct","name":"KCT","version":"1.0.4","apiVersion":1,"hostVersions":["1.*"],"entry":{"renderer":"renderer.js"},"capabilities":["ui:thinking","storage"]}`),
		"renderer.js": []byte("export function activate() {}\n"),
		"LICENSE":     []byte("MIT\n"),
	}
	// v1.1.0 requests an additional capability (`net:edge.microsoft.com`) and requires core 0.2.8.
	v2Files := map[string][]byte{
		"plugin.json": []byte(`{"id":"kimicode-plus.kct","name":"KCT","version":"1.1.0","apiVersion":1,"hostVersions":["1.*"],"entry":{"renderer":"renderer.js"},"capabilities":["ui:thinking","storage","net:edge.microsoft.com"]}`),
		"renderer.js": []byte("export function activate() { return 2; }\n"),
		"LICENSE":     []byte("MIT\n"),
	}
	entryV2, archiveV2 := makeSignedEntry(t, v2Files, "0.2.8", "新增在线翻译能力")
	signedManifest, err := SignPluginCatalog(OfficialPluginCatalog{
		GeneratedAt: "2026-11-23T00:00:00Z",
		Plugins:     []OfficialPluginEntry{entryV2},
	}, DefaultOfficialKeyID, priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/owner/repo/releases/download/plugins-latest/plugins-manifest.json":
			_, _ = w.Write(signedManifest)
		case "/owner/repo/releases/download/plugins-latest/" + entryV2.Archive:
			_, _ = w.Write(archiveV2)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "plugins.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Core 0.2.7 is below minCoreVersion 0.2.8 -> blocked!
	oldCoreMgr, err := New(
		db,
		func() bool { return false },
		WithDataDir(root),
		WithBundled(func() (map[string]map[string][]byte, error) {
			return map[string]map[string][]byte{"kimicode-plus.kct": v1Files}, nil
		}),
		WithOfficial(func() (map[string]string, error) {
			return map[string]string{"kimicode-plus.kct": ContentDigest(v1Files)}, nil
		}),
		WithUpdater(UpdateOptions{
			CoreVersion:       "0.2.7",
			Repo:              "owner/repo",
			ReleaseBaseURL:    srv.URL,
			AllowInsecureHTTP: true,
			OfficialKeys:      keys,
		}),
	)
	if err != nil {
		t.Fatalf("oldCoreMgr: %v", err)
	}
	if err := oldCoreMgr.ReleaseBundled(ctx, map[string]map[string][]byte{"kimicode-plus.kct": v1Files}); err != nil {
		t.Fatalf("release: %v", err)
	}
	st, err := oldCoreMgr.CheckUpdates(ctx)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if st.Items["kimicode-plus.kct"].CanApply || !strings.Contains(st.Items["kimicode-plus.kct"].CannotApplyReason, "0.2.8") {
		t.Fatalf("expected minCoreVersion block, got %+v", st.Items["kimicode-plus.kct"])
	}
	oldCoreMgr.Close()

	// Core 0.2.8 meets minCoreVersion, but must require explicit consent for added capability.
	mgr, err := New(
		db,
		func() bool { return false },
		WithDataDir(root),
		WithBundled(func() (map[string]map[string][]byte, error) {
			return map[string]map[string][]byte{"kimicode-plus.kct": v1Files}, nil
		}),
		WithOfficial(func() (map[string]string, error) {
			return map[string]string{"kimicode-plus.kct": ContentDigest(v1Files)}, nil
		}),
		WithUpdater(UpdateOptions{
			CoreVersion:       "0.2.8",
			Repo:              "owner/repo",
			ReleaseBaseURL:    srv.URL,
			AllowInsecureHTTP: true,
			OfficialKeys:      keys,
		}),
	)
	if err != nil {
		t.Fatalf("mgr: %v", err)
	}
	defer mgr.Close()

	st, err = mgr.CheckUpdates(ctx)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	item := st.Items["kimicode-plus.kct"]
	if !item.NeedsConsent || !contains(item.AddedCapabilities, "net:edge.microsoft.com") {
		t.Fatalf("expected consent required for net:edge.microsoft.com, got %+v", item)
	}
	if _, err := mgr.ApplyUpdate(ctx, "kimicode-plus.kct", UpdateRequest{ExpectedVersion: "1.1.0"}); err == nil || !strings.Contains(err.Error(), "PLUGIN_UPDATE_CONSENT_REQUIRED") {
		t.Fatalf("expected consent error without confirmation, got %v", err)
	}
	updated, err := mgr.ApplyUpdate(ctx, "kimicode-plus.kct", UpdateRequest{ExpectedVersion: "1.1.0", ConfirmCapabilities: true})
	if err != nil {
		t.Fatalf("confirmed apply failed: %v", err)
	}
	if updated.Version != "1.1.0" || !contains(updated.Capabilities, "net:edge.microsoft.com") {
		t.Fatalf("expected granted updated capability, got %+v", updated)
	}
}

func TestEvaluateUpdateBlocksHostIncompatibleRelease(t *testing.T) {
	manager, err := newManagerAt(t, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	item := entry{
		manifest: Manifest{ID: "kimicode-plus.kct", Name: "KCT", Version: "1.0.0", HostVersions: []string{"1.*"}},
		source:   SourceBundled,
	}
	remote := OfficialPluginEntry{ID: "kimicode-plus.kct", Name: "KCT", Version: "1.0.1", HostVersions: []string{"2.*"}}
	blocked := manager.evaluateUpdate(item, remote, "1.3.7", false)
	if !blocked.UpdateAvailable {
		t.Fatalf("expected the newer version to be reported: %+v", blocked)
	}
	if blocked.HostCompatible || blocked.CanApply {
		t.Fatalf("host-incompatible update must not be applicable: %+v", blocked)
	}
	if !strings.Contains(blocked.CannotApplyReason, "1.3.7") || !strings.Contains(blocked.CannotApplyReason, "宿主版本") {
		t.Fatalf("unexpected block reason: %q", blocked.CannotApplyReason)
	}
	allowed := manager.evaluateUpdate(item, remote, "2.0.0", false)
	if !allowed.HostCompatible || !allowed.CanApply {
		t.Fatalf("compatible host should be applicable: %+v", allowed)
	}
}

type countingEnableGate struct {
	mu          sync.Mutex
	calls       int
	refuseAfter int
}

func (g *countingEnableGate) CheckEnable(context.Context, string) (string, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	if g.calls > g.refuseAfter {
		return "TEST_GATE_BLOCKED", "测试门禁拒绝再次启用"
	}
	return "", ""
}

func TestOfficialUpdateExposesReenableFailure(t *testing.T) {
	ctx := context.Background()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	keys := map[string]ed25519.PublicKey{DefaultOfficialKeyID: pub}
	v1Files := map[string][]byte{
		"plugin.json": []byte(`{"id":"kimicode-plus.kct","name":"KCT","version":"1.0.0","apiVersion":1,"hostVersions":["1.*"],"entry":{"renderer":"renderer.js"},"capabilities":["ui:thinking","storage"]}`),
		"renderer.js": []byte("export function activate() { return 'v1'; }\n"),
		"LICENSE":     []byte("MIT\n"),
	}
	v2Files := map[string][]byte{
		"plugin.json": []byte(`{"id":"kimicode-plus.kct","name":"KCT","version":"1.0.1","apiVersion":1,"hostVersions":["1.*"],"entry":{"renderer":"renderer.js"},"capabilities":["ui:thinking","storage"]}`),
		"renderer.js": []byte("export function activate() { return 'v2'; }\n"),
		"LICENSE":     []byte("MIT\n"),
	}
	entryV2, archiveV2 := makeSignedEntry(t, v2Files, "", "")
	signedManifest, err := SignPluginCatalog(OfficialPluginCatalog{
		GeneratedAt: "2026-11-23T00:00:00Z",
		Plugins:     []OfficialPluginEntry{entryV2},
	}, DefaultOfficialKeyID, priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/owner/repo/releases/download/plugins-latest/plugins-manifest.json":
			_, _ = w.Write(signedManifest)
		case "/owner/repo/releases/download/plugins-latest/" + entryV2.Archive:
			_, _ = w.Write(archiveV2)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "plugins.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bundledSet := map[string]map[string][]byte{"kimicode-plus.kct": v1Files}
	manager, err := New(
		db,
		func() bool { return false },
		WithDataDir(root),
		WithHostVersion("1.3.7"),
		WithBundled(func() (map[string]map[string][]byte, error) { return bundledSet, nil }),
		WithOfficial(func() (map[string]string, error) {
			return map[string]string{"kimicode-plus.kct": ContentDigest(v1Files)}, nil
		}),
		WithUpdater(UpdateOptions{
			CoreVersion:       "0.2.8",
			Repo:              "owner/repo",
			ReleaseBaseURL:    srv.URL,
			AllowInsecureHTTP: true,
			OfficialKeys:      keys,
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	gate := &countingEnableGate{refuseAfter: 1}
	manager.UseEnableGate(gate)
	if err := manager.ReleaseBundled(ctx, bundledSet); err != nil {
		t.Fatalf("release bundled: %v", err)
	}
	if _, err := manager.Enable(ctx, "kimicode-plus.kct"); err != nil {
		t.Fatalf("enable v1: %v", err)
	}
	if _, err := manager.CheckUpdates(ctx); err != nil {
		t.Fatalf("check updates: %v", err)
	}
	updated, err := manager.ApplyUpdate(ctx, "kimicode-plus.kct", UpdateRequest{ExpectedVersion: "1.0.1"})
	if err != nil {
		t.Fatalf("apply update should succeed even when re-enable fails: %v", err)
	}
	if updated.Version != "1.0.1" {
		t.Fatalf("new files were not installed: %+v", updated)
	}
	if updated.Enabled {
		t.Fatalf("a blocked re-enable must not report the plugin as running: %+v", updated)
	}
	if !strings.Contains(updated.Error, "重新启用失败") || !strings.Contains(updated.Error, "测试门禁拒绝再次启用") {
		t.Fatalf("re-enable failure was hidden from the result: %q", updated.Error)
	}
	if updated.State != StateBlocked {
		t.Fatalf("expected the blocked state to survive, got %s", updated.State)
	}
}

func TestHandleAssetsRevisionBindingAndMetadata(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := storage.Open(filepath.Join(root, "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	v1Files := map[string][]byte{
		"plugin.json": []byte(`{
			"id": "kimicode-plus.kct",
			"name": "KCT",
			"version": "1.0.0",
			"apiVersion": 2,
			"hostVersions": ["1.*"],
			"capabilities": ["ui:settings", "ui:thinking", "credentials:api..token"],
			"entry": {"renderer": "renderer.js", "settings": "settings.js"}
		}`),
		"renderer.js":      []byte("export function activate(){/*v1*/}\n"),
		"settings.js":      []byte("export function renderSettings(){/*v1*/}\n"),
		"LICENSE":          []byte("MIT\n"),
		"assets/icon.bin":  {0xff, 0xfe, 0x00, 0x01},
	}
	bundledSet := map[string]map[string][]byte{"kimicode-plus.kct": v1Files}
	mgr, err := New(
		db,
		func() bool { return false },
		WithDataDir(root),
		WithHostVersion("1.3.7"),
		WithBundled(func() (map[string]map[string][]byte, error) { return bundledSet, nil }),
		WithOfficial(func() (map[string]string, error) {
			return map[string]string{"kimicode-plus.kct": ContentDigest(v1Files)}, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	if err := mgr.ReleaseBundled(ctx, bundledSet); err != nil {
		t.Fatalf("release bundled: %v", err)
	}
	enabled, err := mgr.Enable(ctx, "kimicode-plus.kct")
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	mux := http.NewServeMux()
	mgr.Register(mux)

	capsCSV := strings.Join(enabled.Capabilities, ",")
	matchingQuery := url.Values{
		"version":      {enabled.Version},
		"updatedAt":    {enabled.UpdatedAt},
		"capabilities": {capsCSV},
	}.Encode()

	// 1. Bare request (without revision query) returns 200 with authoritative metadata.
	bareRec := httptest.NewRecorder()
	mux.ServeHTTP(bareRec, httptest.NewRequest(http.MethodGet, APIPrefix+"/plugins/kimicode-plus.kct/assets/settings.js", nil))
	if bareRec.Code != http.StatusOK {
		t.Fatalf("bare asset fetch = %d %s", bareRec.Code, bareRec.Body.String())
	}
	var bareBody struct {
		Path         string   `json:"path"`
		Source       string   `json:"source"`
		Version      string   `json:"version"`
		UpdatedAt    string   `json:"updatedAt"`
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(bareRec.Body.Bytes(), &bareBody); err != nil {
		t.Fatalf("decode bare asset body: %v", err)
	}
	if bareBody.Version != enabled.Version || bareBody.UpdatedAt != enabled.UpdatedAt || strings.Join(bareBody.Capabilities, ",") != capsCSV {
		t.Fatalf("unexpected bare asset metadata: got=%+v want version=%q updatedAt=%q caps=%q", bareBody, enabled.Version, enabled.UpdatedAt, capsCSV)
	}

	// 2. Exact matching revision query returns 200 for both UTF-8 module and base64 binary asset.
	matchRec := httptest.NewRecorder()
	mux.ServeHTTP(matchRec, httptest.NewRequest(http.MethodGet, APIPrefix+"/plugins/kimicode-plus.kct/assets/settings.js?"+matchingQuery, nil))
	if matchRec.Code != http.StatusOK || !strings.Contains(matchRec.Body.String(), "/*v1*/") {
		t.Fatalf("matching asset fetch = %d %s", matchRec.Code, matchRec.Body.String())
	}
	binRec := httptest.NewRecorder()
	mux.ServeHTTP(binRec, httptest.NewRequest(http.MethodGet, APIPrefix+"/plugins/kimicode-plus.kct/assets/assets/icon.bin?"+matchingQuery, nil))
	if binRec.Code != http.StatusOK {
		t.Fatalf("matching binary asset fetch = %d %s", binRec.Code, binRec.Body.String())
	}
	var binBody struct {
		Encoding     string   `json:"encoding"`
		Version      string   `json:"version"`
		UpdatedAt    string   `json:"updatedAt"`
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(binRec.Body.Bytes(), &binBody); err != nil {
		t.Fatalf("decode binary asset body: %v", err)
	}
	if binBody.Encoding != "base64" || binBody.Version != enabled.Version || binBody.UpdatedAt != enabled.UpdatedAt || strings.Join(binBody.Capabilities, ",") != capsCSV {
		t.Fatalf("unexpected binary asset metadata: %+v", binBody)
	}

	// 3. Any mismatched version, updatedAt, or capabilities (including empty capabilities=) returns 409 PLUGIN_REVISION_MISMATCH.
	mismatches := []struct {
		name  string
		query url.Values
	}{
		{
			name:  "version mismatch",
			query: url.Values{"version": {"1.0.1"}, "updatedAt": {enabled.UpdatedAt}, "capabilities": {capsCSV}},
		},
		{
			name:  "updatedAt mismatch",
			query: url.Values{"version": {enabled.Version}, "updatedAt": {"stale-timestamp"}, "capabilities": {capsCSV}},
		},
		{
			name:  "capabilities mismatch",
			query: url.Values{"version": {enabled.Version}, "updatedAt": {enabled.UpdatedAt}, "capabilities": {"ui:settings"}},
		},
		{
			name:  "empty capabilities query mismatch",
			query: url.Values{"version": {enabled.Version}, "updatedAt": {enabled.UpdatedAt}, "capabilities": {""}},
		},
	}
	for _, tc := range mismatches {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, APIPrefix+"/plugins/kimicode-plus.kct/assets/settings.js?"+tc.query.Encode(), nil))
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "PLUGIN_REVISION_MISMATCH") {
			t.Fatalf("%s: got %d %s, want 409 PLUGIN_REVISION_MISMATCH", tc.name, rec.Code, rec.Body.String())
		}
	}
}
