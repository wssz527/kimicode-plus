package monitormigration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"enhancedkimi/internal/plugins"
)

// originalIndex is an official desktop-dist page, with the loader tag absent.
const originalIndex = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <script src="/boot.js"></script>
    <title>Kimi Code</title>
    <script type="module" crossorigin src="/assets/main-BVDCsvvI.js"></script>
  </head>
  <body>
    <div id="app"></div>
  </body>
</html>
`

// fakeSystem builds a temporary application bundle and data directory. Every
// filesystem field is real except the ones a test overrides, so the service
// exercises the same code paths as a user's Mac.
type fakeSystem struct {
	root     string
	dist     string
	now      time.Time
	writeErr error
	sys      System
}

func newFake(t *testing.T) *fakeSystem {
	t.Helper()
	root := t.TempDir()
	f := &fakeSystem{
		root: root,
		dist: filepath.Join(root, "Kimi Code.app", "Contents", "Resources", distDirName),
		now:  time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC),
	}
	if err := os.MkdirAll(f.dist, 0o755); err != nil {
		t.Fatalf("mkdir desktop-dist: %v", err)
	}
	f.sys = System{
		Home:     root,
		Platform: "darwin",
		Env:      func(string) string { return "" },
		AppDirs:  []string{f.dist},
		Stat:     os.Stat,
		ReadFile: os.ReadFile,
		WriteFile: func(name string, data []byte, perm os.FileMode) error {
			if f.writeErr != nil {
				return f.writeErr
			}
			return os.WriteFile(name, data, perm)
		},
		Rename:    os.Rename,
		Remove:    os.Remove,
		RemoveAll: os.RemoveAll,
		Now:       func() time.Time { return f.now },
	}
	return f
}

func (f *fakeSystem) service(t *testing.T) *Service {
	t.Helper()
	return New(t.TempDir(), f.sys)
}

func (f *fakeSystem) indexPath() string  { return filepath.Join(f.dist, indexName) }
func (f *fakeSystem) backupPath() string { return filepath.Join(f.dist, backupName) }
func (f *fakeSystem) payloadDir() string { return filepath.Join(f.dist, payloadName) }

func (f *fakeSystem) patch(t *testing.T, withBackup, withPayload bool) string {
	t.Helper()
	return writePatch(t, f.dist, withBackup, withPayload)
}

func (f *fakeSystem) read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func (f *fakeSystem) exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// injectLoader mirrors the standalone installer: the tag goes right before
// </head>, with a newline added only when the marker is not already at line
// start.
func injectLoader(t *testing.T, html, hash string) string {
	t.Helper()
	marker := "</head>"
	at := strings.Index(html, marker)
	if at < 0 {
		t.Fatalf("fixture has no %s", marker)
	}
	tag := fmt.Sprintf(`      <script src="%s?v=%s"></script>`, loaderPath, hash)
	if at > 0 && html[at-1] != '\n' {
		tag = "\n" + tag
	} else {
		tag += "\n"
	}
	return html[:at] + tag + html[at:]
}

func writePatch(t *testing.T, dist string, withBackup, withPayload bool) string {
	t.Helper()
	patched := injectLoader(t, originalIndex, "1750000000")
	if err := os.WriteFile(filepath.Join(dist, indexName), []byte(patched), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if withBackup {
		if err := os.WriteFile(filepath.Join(dist, backupName), []byte(originalIndex), 0o644); err != nil {
			t.Fatalf("write backup: %v", err)
		}
	}
	if withPayload {
		payload := filepath.Join(dist, payloadName)
		if err := os.MkdirAll(payload, 0o755); err != nil {
			t.Fatalf("mkdir kcm: %v", err)
		}
		if err := os.WriteFile(filepath.Join(payload, "loader.js"), []byte("/* monitor loader */"), 0o644); err != nil {
			t.Fatalf("write loader.js: %v", err)
		}
	}
	return patched
}

func TestStatusCleanInstall(t *testing.T) {
	f := newFake(t)
	service := f.service(t)
	ctx := context.Background()

	report := service.Status(ctx)
	if report.PluginID != PluginID {
		t.Fatalf("pluginId = %q", report.PluginID)
	}
	if report.DistDir != f.dist || report.PayloadDir != f.payloadDir() || report.BackupPath != f.backupPath() {
		t.Fatalf("paths not reported: %+v", report)
	}
	if report.PayloadPresent || report.IndexPatched || report.BackupPresent || report.Detected || report.NeedsConfirmation || report.Migrated {
		t.Fatalf("clean install reported as patched: %+v", report)
	}
	if code, _ := service.CheckEnable(ctx, PluginID); code != "" {
		t.Fatalf("clean install blocked the plugin with %q", code)
	}
	if code, _ := service.CheckEnable(ctx, "other.plugin"); code != "" {
		t.Fatalf("gate blocked an unrelated plugin with %q", code)
	}

	result, err := service.Confirm(ctx)
	if err != nil {
		t.Fatalf("confirm on a clean install: %v", err)
	}
	if result.RestoredIndex || result.RemovedPayload || result.Report.Migrated {
		t.Fatalf("clean confirm reported work: %+v", result)
	}
	if !strings.Contains(result.Message, "没有检测到") {
		t.Fatalf("message does not explain the clean state: %q", result.Message)
	}
	if f.exists(MarkerPath(service.dataDir)) {
		t.Fatalf("a clean confirm wrote a migration marker")
	}
}

func TestStatusDetectsPatchedInstallWithBackup(t *testing.T) {
	f := newFake(t)
	f.patch(t, true, true)
	service := f.service(t)

	report := service.Status(context.Background())
	if !report.PayloadPresent || !report.IndexPatched || !report.BackupPresent {
		t.Fatalf("patch parts not all reported: %+v", report)
	}
	if !report.Detected || !report.NeedsConfirmation || report.Migrated {
		t.Fatalf("patched install state is wrong: %+v", report)
	}
	code, message := service.CheckEnable(context.Background(), PluginID)
	if code != "PLUGIN_MIGRATION_REQUIRED" {
		t.Fatalf("enable was not blocked, code=%q", code)
	}
	if !strings.Contains(message, "独立版") || !strings.Contains(message, "侧栏") {
		t.Fatalf("message is not user-facing: %q", message)
	}
}

func TestStatusDetectsPatchedInstallWithoutBackup(t *testing.T) {
	f := newFake(t)
	f.patch(t, false, true)
	service := f.service(t)

	report := service.Status(context.Background())
	if !report.PayloadPresent || !report.IndexPatched {
		t.Fatalf("patch parts not reported: %+v", report)
	}
	if report.BackupPresent {
		t.Fatalf("backup reported although none exists: %+v", report)
	}
	if !report.Detected || !report.NeedsConfirmation {
		t.Fatalf("patched install without backup was not detected: %+v", report)
	}
}

// Any single part of the standalone patch means a second panel can appear, so
// each part is enough to block the plugin on its own.
func TestDetectionSeesAnyPatchPart(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, f *fakeSystem)
		want  func(Report) bool
	}{
		{
			name: "payload only",
			setup: func(t *testing.T, f *fakeSystem) {
				if err := os.MkdirAll(f.payloadDir(), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.payloadDir(), "loader.js"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.indexPath(), []byte(originalIndex), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: func(r Report) bool { return r.PayloadPresent && !r.IndexPatched && !r.BackupPresent && r.Detected },
		},
		{
			name: "backup only",
			setup: func(t *testing.T, f *fakeSystem) {
				if err := os.WriteFile(f.indexPath(), []byte(originalIndex), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(f.backupPath(), []byte(originalIndex), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: func(r Report) bool { return r.BackupPresent && !r.PayloadPresent && !r.IndexPatched && r.Detected },
		},
		{
			name: "loader only",
			setup: func(t *testing.T, f *fakeSystem) {
				if err := os.WriteFile(f.indexPath(), []byte(injectLoader(t, originalIndex, "abc")), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: func(r Report) bool { return r.IndexPatched && !r.PayloadPresent && !r.BackupPresent && r.Detected },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			tc.setup(t, f)
			report := f.service(t).Status(context.Background())
			if !tc.want(report) {
				t.Fatalf("detection mismatch: %+v", report)
			}
			if !report.NeedsConfirmation {
				t.Fatalf("single marker did not block the plugin: %+v", report)
			}
		})
	}
}

func TestConfirmRestoresExactOriginalIndex(t *testing.T) {
	f := newFake(t)
	f.patch(t, true, true)
	service := f.service(t)
	ctx := context.Background()

	result, err := service.Confirm(ctx)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !result.RestoredIndex || !result.RemovedPayload {
		t.Fatalf("confirm did not report both steps: %+v", result)
	}
	if result.AlreadyMigrated {
		t.Fatalf("first confirm reported an idempotent repeat: %+v", result)
	}
	if got := f.read(t, f.indexPath()); got != originalIndex {
		t.Fatalf("index.html was not restored byte for byte:\n%q\nwant:\n%q", got, originalIndex)
	}
	if f.exists(f.backupPath()) {
		t.Fatalf("backup was not removed")
	}
	if f.exists(f.payloadDir()) {
		t.Fatalf("kcm payload was not removed")
	}
	if !result.Report.Migrated || result.Report.NeedsConfirmation || result.Report.Detected {
		t.Fatalf("report after confirm is wrong: %+v", result.Report)
	}
	if result.Report.MigratedAt != "2026-11-22T10:00:00Z" {
		t.Fatalf("migratedAt = %q", result.Report.MigratedAt)
	}
	if code, _ := service.CheckEnable(ctx, PluginID); code != "" {
		t.Fatalf("plugin stayed blocked after confirm, code=%q", code)
	}
	for _, want := range []string{"Cmd+R", "Ctrl+R", "index.html"} {
		if !strings.Contains(result.Message, want) {
			t.Fatalf("message lacks %q: %q", want, result.Message)
		}
	}

	raw, err := os.ReadFile(MarkerPath(service.dataDir))
	if err != nil {
		t.Fatalf("marker missing: %v", err)
	}
	var marker markerRecord
	if err := json.Unmarshal(raw, &marker); err != nil {
		t.Fatalf("marker is not JSON: %v", err)
	}
	if !marker.Migrated || marker.PluginID != PluginID || marker.DistDir != f.dist {
		t.Fatalf("marker content is wrong: %+v", marker)
	}
}

func TestConfirmWithoutBackupStripsLoader(t *testing.T) {
	f := newFake(t)
	f.patch(t, false, true)
	service := f.service(t)

	result, err := service.Confirm(context.Background())
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !result.RestoredIndex {
		t.Fatalf("index.html was not reported as restored: %+v", result)
	}
	if got := f.read(t, f.indexPath()); got != originalIndex {
		t.Fatalf("injected line was not stripped cleanly:\n%q\nwant:\n%q", got, originalIndex)
	}
	if f.exists(f.payloadDir()) {
		t.Fatalf("kcm payload was not removed")
	}
	if !f.exists(MarkerPath(service.dataDir)) {
		t.Fatalf("marker missing")
	}
}

// Older installers could leave a loader tag inside the backup itself; restoring
// it must still produce the page without the injection.
func TestConfirmStripsLoaderFromBackup(t *testing.T) {
	f := newFake(t)
	patched := f.patch(t, false, true)
	if err := os.WriteFile(f.backupPath(), []byte(patched), 0o644); err != nil {
		t.Fatal(err)
	}
	service := f.service(t)

	if _, err := service.Confirm(context.Background()); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if got := f.read(t, f.indexPath()); got != originalIndex {
		t.Fatalf("backup was restored without stripping the loader:\n%q", got)
	}
}

func TestConfirmIsIdempotent(t *testing.T) {
	f := newFake(t)
	f.patch(t, true, true)
	service := f.service(t)
	ctx := context.Background()

	if _, err := service.Confirm(ctx); err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	second, err := service.Confirm(ctx)
	if err != nil {
		t.Fatalf("second confirm: %v", err)
	}
	if !second.AlreadyMigrated || second.RestoredIndex || second.RemovedPayload {
		t.Fatalf("repeat was not reported as already migrated: %+v", second)
	}
	if !second.Report.Migrated {
		t.Fatalf("repeat lost the migrated state: %+v", second.Report)
	}
	if got := f.read(t, f.indexPath()); got != originalIndex {
		t.Fatalf("repeat touched index.html: %q", got)
	}
	if code, _ := service.CheckEnable(ctx, PluginID); code != "" {
		t.Fatalf("repeat left the plugin blocked, code=%q", code)
	}
}

// Restoring index.html is the step that matters: if it fails, nothing may be
// deleted and the backup has to stay for the user to retry.
func TestConfirmRestoreFailurePreservesBackup(t *testing.T) {
	f := newFake(t)
	patched := f.patch(t, true, true)
	f.writeErr = &os.PathError{Op: "open", Path: f.indexPath() + ".kimicode-plus.next", Err: os.ErrPermission}
	service := f.service(t)
	ctx := context.Background()

	result, err := service.Confirm(ctx)
	if err == nil {
		t.Fatalf("confirm reported success after a failed restore")
	}
	var apiErr *plugins.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "MONITOR_MIGRATION_RESTORE_FAILED" {
		t.Fatalf("failure is not the actionable restore error: %v", err)
	}
	for _, want := range []string{"权限", "install.sh --uninstall"} {
		if !strings.Contains(apiErr.Message, want) {
			t.Fatalf("message lacks %q: %q", want, apiErr.Message)
		}
	}
	if result.RestoredIndex || result.RemovedPayload {
		t.Fatalf("failed confirm reported work: %+v", result)
	}
	if !f.exists(f.backupPath()) {
		t.Fatalf("backup was lost after a failed restore")
	}
	if !f.exists(f.payloadDir()) {
		t.Fatalf("payload was deleted after a failed restore")
	}
	if got := f.read(t, f.indexPath()); got != patched {
		t.Fatalf("index.html changed after a failed restore: %q", got)
	}
	if f.exists(MarkerPath(service.dataDir)) {
		t.Fatalf("marker was written after a failed restore")
	}
	report := service.Status(ctx)
	if !report.NeedsConfirmation || report.Detail == "" {
		t.Fatalf("failure is not visible to the page: %+v", report)
	}
	if code, _ := service.CheckEnable(ctx, PluginID); code != "PLUGIN_MIGRATION_REQUIRED" {
		t.Fatalf("plugin was not blocked after a failed migration, code=%q", code)
	}
}

// The delete-and-restore path must refuse a directory that is not a
// desktop-dist; an injected candidate cannot aim it at another folder.
func TestConfirmRefusesDirectoryOutsideDesktopDist(t *testing.T) {
	f := newFake(t)
	other := filepath.Join(f.root, "not-a-desktop-dist")
	if err := os.MkdirAll(filepath.Join(other, payloadName), 0o755); err != nil {
		t.Fatal(err)
	}
	patched := writePatch(t, other, false, true)
	f.sys.AppDirs = []string{other}
	service := f.service(t)
	ctx := context.Background()

	report := service.Status(ctx)
	if report.DistDir != other || !report.Detected {
		t.Fatalf("test patch was not detected: %+v", report)
	}
	_, err := service.Confirm(ctx)
	var apiErr *plugins.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "MONITOR_MIGRATION_PATH" {
		t.Fatalf("path guard did not fire: %v", err)
	}
	if got := f.read(t, filepath.Join(other, indexName)); got != patched {
		t.Fatalf("guarded confirm touched index.html")
	}
	if !f.exists(filepath.Join(other, payloadName)) {
		t.Fatalf("guarded confirm deleted kcm")
	}
	if f.exists(MarkerPath(service.dataDir)) {
		t.Fatalf("guarded confirm wrote a marker")
	}
}

// A completed handover keeps the plugin usable, but reinstalling the standalone
// patch has to block it again: that is what prevents a second injection.
func TestMarkerSurvivesReinstallAndBlocksAgain(t *testing.T) {
	f := newFake(t)
	f.patch(t, true, true)
	service := f.service(t)
	ctx := context.Background()

	if _, err := service.Confirm(ctx); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if code, _ := service.CheckEnable(ctx, PluginID); code != "" {
		t.Fatalf("migrated install stayed blocked, code=%q", code)
	}

	f.patch(t, true, true)
	report := service.Status(ctx)
	if !report.Detected || !report.NeedsConfirmation || report.Migrated {
		t.Fatalf("reinstalled patch did not require confirmation: %+v", report)
	}
	if code, _ := service.CheckEnable(ctx, PluginID); code != "PLUGIN_MIGRATION_REQUIRED" {
		t.Fatalf("reinstalled patch did not block the plugin, code=%q", code)
	}
}

// A marker for another plugin must not count: the migrations directory is
// shared with the other standalone handovers.
func TestForeignMarkerIsIgnored(t *testing.T) {
	f := newFake(t)
	service := f.service(t)
	path := MarkerPath(service.dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"at":"2026-01-01T00:00:00Z","migrated":true,"pluginId":"kimicode-plus.kct"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if report := service.Status(context.Background()); report.Migrated {
		t.Fatalf("a foreign marker counted as this migration: %+v", report)
	}
}

func TestCandidatesMatchStandaloneInstaller(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	home := "/Users/tester"
	cases := []struct {
		name     string
		platform string
		home     string
		env      map[string]string
		override []string
		want     []string
	}{
		{
			name:     "darwin app directory from env",
			platform: "darwin",
			home:     home,
			env:      map[string]string{"KIMI_CODE_APP_DIR": "/opt/Kimi Code.app"},
			want: []string{
				"/opt/Kimi Code.app/Contents/Resources/desktop-dist",
				"/Applications/Kimi Code.app/Contents/Resources/desktop-dist",
				"/Users/tester/Applications/Kimi Code.app/Contents/Resources/desktop-dist",
			},
		},
		{
			name:     "darwin dist directory from env is accepted as is",
			platform: "darwin",
			home:     home,
			env:      map[string]string{"KIMI_CODE_APP_DIR": "/opt/Kimi Code.app/Contents/Resources/desktop-dist"},
			want: []string{
				"/opt/Kimi Code.app/Contents/Resources/desktop-dist",
				"/Applications/Kimi Code.app/Contents/Resources/desktop-dist",
				"/Users/tester/Applications/Kimi Code.app/Contents/Resources/desktop-dist",
			},
		},
		{
			name:     "windows candidates",
			platform: "windows",
			home:     `C:\Users\tester`,
			env:      map[string]string{"LOCALAPPDATA": `C:\Users\tester\AppData\Local`},
			want: []string{
				`C:\Users\tester\AppData\Local\Programs\Kimi Code\resources\desktop-dist`,
				`C:\Program Files\Kimi Code\resources\desktop-dist`,
				`D:\kimi_code\Kimi Code\resources\desktop-dist`,
			},
		},
		{
			name:     "windows dist directory from env is accepted as is",
			platform: "windows",
			home:     `C:\Users\tester`,
			env: map[string]string{
				"KIMI_CODE_APP_DIR": `D:\kimi_code\Kimi Code\resources\desktop-dist`,
				"LOCALAPPDATA":      `C:\Users\tester\AppData\Local`,
			},
			want: []string{
				`D:\kimi_code\Kimi Code\resources\desktop-dist`,
				`C:\Users\tester\AppData\Local\Programs\Kimi Code\resources\desktop-dist`,
				`C:\Program Files\Kimi Code\resources\desktop-dist`,
			},
		},
		{
			name:     "explicit override wins",
			platform: "darwin",
			home:     home,
			env:      map[string]string{"KIMI_CODE_APP_DIR": "/opt/Kimi Code.app"},
			override: []string{"/tmp/only"},
			want:     []string{"/tmp/only"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := distCandidates(tc.platform, tc.home, env(tc.env), tc.override)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("candidates = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestRegisterServesStatusAndConfirm(t *testing.T) {
	f := newFake(t)
	f.patch(t, true, true)
	service := f.service(t)
	mux := http.NewServeMux()
	service.Register(mux)

	get := httptest.NewRequest(http.MethodGet, "/enhance/v1/plugins/monitor/migration", nil)
	getRecorder := httptest.NewRecorder()
	mux.ServeHTTP(getRecorder, get)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d: %s", getRecorder.Code, getRecorder.Body)
	}
	var report Report
	if err := json.Unmarshal(getRecorder.Body.Bytes(), &report); err != nil {
		t.Fatalf("GET body is not a report: %v", err)
	}
	if !report.Detected || report.DistDir != f.dist {
		t.Fatalf("GET report is wrong: %+v", report)
	}

	post := httptest.NewRequest(http.MethodPost, "/enhance/v1/plugins/monitor/migration/confirm", nil)
	postRecorder := httptest.NewRecorder()
	mux.ServeHTTP(postRecorder, post)
	if postRecorder.Code != http.StatusOK {
		t.Fatalf("POST confirm status = %d: %s", postRecorder.Code, postRecorder.Body)
	}
	var result Result
	if err := json.Unmarshal(postRecorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("POST body is not a result: %v", err)
	}
	if !result.RestoredIndex || !result.RemovedPayload {
		t.Fatalf("POST confirm did not finish the handover: %+v", result)
	}
	if got := f.read(t, f.indexPath()); got != originalIndex {
		t.Fatalf("POST confirm did not restore index.html")
	}
}
