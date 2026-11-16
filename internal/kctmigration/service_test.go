package kctmigration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSystem builds a temporary HOME with the paths the standalone installer
// would create, and records every launchctl invocation instead of running one.
// The default launchctl answers "service not found", which is the state of a
// Mac where the extension was installed but is not currently loaded.
type fakeSystem struct {
	home    string
	calls   [][]string
	runErr  func(args []string) error
	renames int
	now     time.Time
	sys     System
	// printOutput is what `launchctl print` reports for a loaded job. A real
	// launchctl names the plist it loaded; the tests that exercise the handover
	// must do the same, because the service refuses to boot out a job it cannot
	// attribute to this home.
	printOutput string
}

func newFake(t *testing.T) *fakeSystem {
	t.Helper()
	f := &fakeSystem{home: t.TempDir(), now: time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC)}
	f.runErr = func(args []string) error {
		return errors.New("Could not find service \"com.user.kimi-cot-translate.runtime\" in domain for port")
	}
	f.printOutput = ""
	f.sys = System{
		Home: f.home,
		UID:  func() int { return 501 },
		Run: func(_ context.Context, args ...string) ([]byte, error) {
			f.calls = append(f.calls, args)
			if f.runErr != nil {
				return nil, f.runErr(args)
			}
			if len(args) > 0 && args[0] == "print" {
				return []byte("gui/501/com.user.kimi-cot-translate.runtime = {\n\tactive count = 0\n\tpath = " + f.printOutput + "\n}\n"), nil
			}
			return nil, nil
		},
		Stat:      os.Stat,
		ReadFile:  os.ReadFile,
		Rename:    os.Rename,
		WriteFile: os.WriteFile,
		Now:       func() time.Time { return f.now },
	}
	return f
}

func (f *fakeSystem) plistPath() string {
	return filepath.Join(f.home, "Library", "LaunchAgents", Label+".plist")
}

func (f *fakeSystem) legacyDir() string {
	return filepath.Join(f.home, "Library", "Application Support", legacyDataDirName)
}

// installLegacy creates the two files an installed extension leaves behind.
func (f *fakeSystem) installLegacy(t *testing.T, settings map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.plistPath()), 0o755); err != nil {
		t.Fatalf("mkdir launchagents: %v", err)
	}
	plist := `<?xml version="1.0"?><plist><dict><key>Label</key><string>` + Label + `</string></dict></plist>`
	if err := os.WriteFile(f.plistPath(), []byte(plist), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	if err := os.MkdirAll(f.legacyDir(), 0o755); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}
	if settings != nil {
		raw, err := json.Marshal(settings)
		if err != nil {
			t.Fatalf("marshal settings: %v", err)
		}
		if err := os.WriteFile(filepath.Join(f.legacyDir(), "cli.json"), raw, 0o600); err != nil {
			t.Fatalf("write cli.json: %v", err)
		}
	}
}

func (f *fakeSystem) called(verb string) [][]string {
	out := [][]string{}
	for _, call := range f.calls {
		if len(call) > 0 && call[0] == verb {
			out = append(out, call)
		}
	}
	return out
}

// memoryStore is the adapter's private settings storage.
type memoryStore struct {
	values  map[string][]byte
	writeOK bool
	failSet error
}

func newStore() *memoryStore {
	return &memoryStore{values: map[string][]byte{}, writeOK: true}
}

func (s *memoryStore) PluginStorage(_ context.Context, id, key string) ([]byte, bool, error) {
	value, ok := s.values[id+"\x00"+key]
	return value, ok, nil
}

func (s *memoryStore) SetPluginStorage(_ context.Context, id, key string, value []byte) error {
	if s.failSet != nil {
		return s.failSet
	}
	s.values[id+"\x00"+key] = value
	return nil
}

func (s *memoryStore) settings(t *testing.T) map[string]any {
	t.Helper()
	raw, ok := s.values[PluginID+"\x00"+SettingsKey]
	if !ok {
		t.Fatalf("settings were not written")
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("settings are not JSON: %v", err)
	}
	return out
}

func newService(t *testing.T, f *fakeSystem, store Store) *Service {
	t.Helper()
	return New(t.TempDir(), store, f.sys)
}

func TestStatusWithoutInstall(t *testing.T) {
	f := newFake(t)
	service := newService(t, f, newStore())

	report := service.Status(context.Background())
	if report.Detected || report.PlistPresent || report.DataDirPresent || report.NeedsConfirmation {
		t.Fatalf("clean machine reported as installed: %+v", report)
	}
	if code, _ := service.CheckEnable(context.Background(), PluginID); code != "" {
		t.Fatalf("clean machine blocked the adapter with %q", code)
	}
}

func TestStatusDetectsLoaderAndBlocksEnable(t *testing.T) {
	f := newFake(t)
	f.installLegacy(t, map[string]string{"provider": "bing", "targetLanguage": "ja"})
	service := newService(t, f, newStore())

	report := service.Status(context.Background())
	if !report.PlistPresent || !report.DataDirPresent || !report.Detected || !report.NeedsConfirmation {
		t.Fatalf("install not detected: %+v", report)
	}
	if report.Settings["provider"] != "bing" || report.Settings["targetLanguage"] != "ja" {
		t.Fatalf("public settings not read: %+v", report.Settings)
	}
	code, message := service.CheckEnable(context.Background(), PluginID)
	if code != "PLUGIN_MIGRATION_REQUIRED" {
		t.Fatalf("enable was not blocked, code=%q", code)
	}
	if !strings.Contains(message, "独立版") {
		t.Fatalf("message is not user-facing: %q", message)
	}
	if code, _ := service.CheckEnable(context.Background(), "other.plugin"); code != "" {
		t.Fatalf("gate blocked an unrelated plugin with %q", code)
	}
}

// A loaded job must be booted out exactly once, and the plist must stop coming
// back at login. This is the single-injector guarantee.
func TestConfirmStopsLoaderAndMigrates(t *testing.T) {
	f := newFake(t)
	f.installLegacy(t, map[string]string{"provider": "google", "targetLanguage": "en"})
	f.runErr = nil // the job is loaded: print reports this home's plist
	f.printOutput = f.plistPath()
	store := newStore()
	service := newService(t, f, store)

	result, err := service.Confirm(context.Background())
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !result.StoppedLoader || !result.DisabledPlist {
		t.Fatalf("confirm did not stop the loader: %+v", result)
	}
	if got := len(f.called("bootout")); got != 1 {
		t.Fatalf("bootout called %d times, want 1 (%v)", got, f.calls)
	}
	if got := f.called("bootout")[0]; len(got) != 2 || got[1] != "gui/501" {
		t.Fatalf("bootout used the wrong domain: %v", got)
	}
	if _, err := os.Stat(f.plistPath()); !os.IsNotExist(err) {
		t.Fatalf("plist is still in place: %v", err)
	}
	if _, err := os.Stat(f.plistPath() + ".disabled-by-kimicode-plus"); err != nil {
		t.Fatalf("plist was not moved aside: %v", err)
	}
	if _, err := os.Stat(MarkerPath(service.dataDir)); err != nil {
		t.Fatalf("marker missing: %v", err)
	}
	settings := store.settings(t)
	if settings["provider"] != "google" || settings["targetLanguage"] != "en" {
		t.Fatalf("settings not migrated: %v", settings)
	}
	if settings["autoTranslate"] != nil {
		t.Fatalf("migration invented a setting: %v", settings)
	}
	if result.Report.MigratedAt != "2026-11-17T09:30:00Z" {
		t.Fatalf("migratedAt = %q", result.Report.MigratedAt)
	}
	if strings.Contains(result.Message, "设置未能写入") {
		t.Fatalf("success message reports a settings failure: %q", result.Message)
	}
}

// A second confirmation must not bootout or rename anything again, and it must
// report the idempotent outcome rather than pretending to have done work.
func TestConfirmIsIdempotent(t *testing.T) {
	f := newFake(t)
	f.installLegacy(t, map[string]string{"provider": "bing", "targetLanguage": "zh"})
	f.runErr = nil
	f.printOutput = f.plistPath()
	service := newService(t, f, newStore())

	if _, err := service.Confirm(context.Background()); err != nil {
		t.Fatalf("first confirm: %v", err)
	}
	before := len(f.calls)
	second, err := service.Confirm(context.Background())
	if err != nil {
		t.Fatalf("second confirm: %v", err)
	}
	if !second.AlreadyMigrated {
		t.Fatalf("repeat was not reported as already migrated: %+v", second)
	}
	if len(f.calls) != before {
		t.Fatalf("repeat ran launchctl again: %v", f.calls[before:])
	}
	if code, _ := service.CheckEnable(context.Background(), PluginID); code != "" {
		t.Fatalf("migrated install still blocks the adapter with %q", code)
	}
}

// A failing bootout must leave the loader exactly as it was: no rename, no
// marker, and an error the page can show.
func TestConfirmKeepsLoaderWhenBootoutFails(t *testing.T) {
	f := newFake(t)
	f.installLegacy(t, map[string]string{"provider": "bing", "targetLanguage": "zh"})
	f.runErr = func(args []string) error {
		if args[0] == "print" {
			return nil
		}
		return errors.New("Operation not permitted")
	}
	f.printOutput = f.plistPath()
	service := newService(t, f, newStore())

	result, err := service.Confirm(context.Background())
	if err == nil {
		t.Fatalf("confirm reported success after a failed bootout")
	}
	if result.StoppedLoader || result.DisabledPlist {
		t.Fatalf("failed confirm reported work: %+v", result)
	}
	if _, err := os.Stat(f.plistPath()); err != nil {
		t.Fatalf("plist was moved after a failed bootout: %v", err)
	}
	if _, err := os.Stat(MarkerPath(service.dataDir)); !os.IsNotExist(err) {
		t.Fatalf("marker was written after a failed bootout: %v", err)
	}
	if code, _ := service.CheckEnable(context.Background(), PluginID); code != "PLUGIN_MIGRATION_REQUIRED" {
		t.Fatalf("adapter was not blocked after a failed migration, code=%q", code)
	}
	if report := service.Status(context.Background()); report.Detail == "" {
		t.Fatalf("failure is not visible to the page")
	}
}

// When the plist cannot be moved aside, the job that was stopped must be
// restarted, otherwise the user is left with neither injector.
func TestConfirmRollsBackWhenRenameFails(t *testing.T) {
	f := newFake(t)
	f.installLegacy(t, map[string]string{"provider": "bing", "targetLanguage": "zh"})
	f.runErr = nil
	f.printOutput = f.plistPath()
	f.sys.Rename = func(string, string) error { return errors.New("read-only volume") }
	service := newService(t, f, newStore())

	if _, err := service.Confirm(context.Background()); err == nil {
		t.Fatalf("confirm reported success after a failed rename")
	}
	bootstraps := f.called("bootstrap")
	if len(bootstraps) != 1 {
		t.Fatalf("loader was not restarted: %v", f.calls)
	}
	if bootstraps[0][2] != f.plistPath() {
		t.Fatalf("bootstrap used the wrong plist: %v", bootstraps[0])
	}
	if _, err := os.Stat(MarkerPath(service.dataDir)); !os.IsNotExist(err) {
		t.Fatalf("marker was written after a failed rename: %v", err)
	}
}

// An installed-but-unloaded extension still has a plist, so it would load at the
// next login. The plist alone is enough to require the handover.
func TestPlistAloneRequiresMigration(t *testing.T) {
	f := newFake(t)
	if err := os.MkdirAll(filepath.Dir(f.plistPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.plistPath(), []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := newService(t, f, newStore())

	result, err := service.Confirm(context.Background())
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if result.StoppedLoader {
		t.Fatalf("an unloaded job was reported as stopped: %+v", result)
	}
	if !result.DisabledPlist {
		t.Fatalf("the plist was not moved aside: %+v", result)
	}
	if got := len(f.called("bootout")); got != 0 {
		t.Fatalf("bootout ran for an unloaded job: %v", f.called("bootout"))
	}
}

// Settings the adapter already holds are the user's, so the migration must not
// overwrite them; the handover still has to happen.
func TestConfirmKeepsExistingAdapterSettings(t *testing.T) {
	f := newFake(t)
	f.installLegacy(t, map[string]string{"provider": "bing", "targetLanguage": "zh"})
	f.runErr = nil
	f.printOutput = f.plistPath()
	store := newStore()
	existing := []byte(`{"targetLanguage":"ko","provider":"google","autoTranslate":false}`)
	store.values[PluginID+"\x00"+SettingsKey] = existing
	service := newService(t, f, store)

	result, err := service.Confirm(context.Background())
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if len(result.MigratedKeys) != 0 {
		t.Fatalf("migrated keys despite existing settings: %v", result.MigratedKeys)
	}
	if string(store.values[PluginID+"\x00"+SettingsKey]) != string(existing) {
		t.Fatalf("existing adapter settings were overwritten")
	}
	if !result.Report.Migrated {
		t.Fatalf("handover was not recorded")
	}
}

// cli.json is written by another program: unknown keys must be ignored and an
// out-of-range value must not reach the adapter.
func TestPublicSettingsOnlyCarriesKnownValues(t *testing.T) {
	f := newFake(t)
	f.installLegacy(t, map[string]string{
		"provider":       "custom",
		"targetLanguage": "klingon",
		"customKey":      "not-a-secret-this-test-only",
		"enabled":        "true",
	})
	store := newStore()
	service := newService(t, f, store)

	report := service.Status(context.Background())
	if _, ok := report.Settings["customKey"]; ok {
		t.Fatalf("a secret key reached the report: %v", report.Settings)
	}
	result, err := service.Confirm(context.Background())
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if len(result.MigratedKeys) != 0 {
		t.Fatalf("unsupported values were migrated: %v", result.MigratedKeys)
	}
	if _, ok := store.values[PluginID+"\x00"+SettingsKey]; ok {
		t.Fatalf("settings were written from unsupported values")
	}
}

func TestMalformedCLIJSONIsIgnored(t *testing.T) {
	f := newFake(t)
	f.installLegacy(t, nil)
	if err := os.WriteFile(filepath.Join(f.legacyDir(), "cli.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := newService(t, f, newStore())

	report := service.Status(context.Background())
	if len(report.Settings) != 0 {
		t.Fatalf("malformed cli.json produced settings: %v", report.Settings)
	}
	if _, err := service.Confirm(context.Background()); err != nil {
		t.Fatalf("a malformed cli.json blocked the handover: %v", err)
	}
}

// The marker records that the handover happened, so a leftover data directory no
// longer blocks the adapter after the user uninstalls and reinstalls it.
func TestMarkerSurvivesReinstallAndStopsBlocking(t *testing.T) {
	f := newFake(t)
	f.installLegacy(t, map[string]string{"provider": "bing", "targetLanguage": "zh"})
	f.runErr = nil
	f.printOutput = f.plistPath()
	service := newService(t, f, newStore())
	if _, err := service.Confirm(context.Background()); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	// Reinstalling the standalone extension restores its plist; the marker is what
	// keeps the adapter from being blocked a second time.
	if err := os.WriteFile(f.plistPath(), []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	report := service.Status(context.Background())
	if !report.Migrated {
		t.Fatalf("marker was not read: %+v", report)
	}
	if report.Detected || report.NeedsConfirmation {
		t.Fatalf("migrated install still reported as needing confirmation: %+v", report)
	}
	if code, _ := service.CheckEnable(context.Background(), PluginID); code != "" {
		t.Fatalf("marker did not unblock the adapter, code=%q", code)
	}
}

// A marker for another label must not count: the file lives in a shared data
// directory and a future migration could write a different record there.
func TestForeignMarkerIsIgnored(t *testing.T) {
	f := newFake(t)
	f.installLegacy(t, map[string]string{"provider": "bing", "targetLanguage": "zh"})
	service := newService(t, f, newStore())
	path := MarkerPath(service.dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"at":"2026-01-01T00:00:00Z","migrated":true,"label":"com.example.other"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if report := service.Status(context.Background()); report.Migrated {
		t.Fatalf("a foreign marker counted as this migration: %+v", report)
	}
}

// The confirm route answers a failure with both the error and the report, so the
// page can keep its state and offer a retry.
func TestConfirmErrorIsAnAPIError(t *testing.T) {
	f := newFake(t)
	f.runErr = func(args []string) error {
		if args[0] == "print" {
			return nil
		}
		return errors.New("Operation not permitted")
	}
	f.installLegacy(t, map[string]string{"provider": "bing", "targetLanguage": "zh"})
	service := newService(t, f, newStore())

	_, err := service.Confirm(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if statusFor(err) != 500 {
		t.Fatalf("status = %d, want 500", statusFor(err))
	}
	if !strings.Contains(messageFor(err), "保持原状") {
		t.Fatalf("message does not tell the user the loader is untouched: %q", messageFor(err))
	}
}

// The label is unique per gui session, so a second installation — a test
// harness with an isolated HOME, another account's copy — can be the job that
// `launchctl print` answers for. Booting it out would stop an extension this
// core never migrated, so only this home's own plist may be unloaded.
func TestForeignLoadedJobIsNotBootedOut(t *testing.T) {
	f := newFake(t)
	f.installLegacy(t, map[string]string{"provider": "bing", "targetLanguage": "zh"})
	f.runErr = nil
	// A different HOME's plist, which is what the user's real installation looks
	// like from inside an isolated test run.
	f.printOutput = filepath.Join("/Users", "someone-else", "Library", "LaunchAgents", Label+".plist")
	store := newStore()
	service := newService(t, f, store)

	result, err := service.Confirm(context.Background())
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if got := len(f.called("bootout")); got != 0 {
		t.Fatalf("booted out a job from another home: %v", f.called("bootout"))
	}
	if result.StoppedLoader {
		t.Fatalf("reported stopping a job it did not own: %+v", result)
	}
	if !result.DisabledPlist {
		t.Fatalf("this home's plist was not moved aside: %+v", result)
	}
	if _, err := os.Stat(f.plistPath() + ".disabled-by-kimicode-plus"); err != nil {
		t.Fatalf("plist was not moved: %v", err)
	}
	// The handover is still complete for this home, so the adapter is unblocked.
	if code, _ := service.CheckEnable(context.Background(), PluginID); code != "" {
		t.Fatalf("adapter stayed blocked, code=%q", code)
	}
}

// launchctl answering without a path means the loader cannot be attributed to a
// home. Guessing either way is worse than reporting that the state is unknown.
func TestUnattributableLoadedJobFails(t *testing.T) {
	f := newFake(t)
	f.installLegacy(t, map[string]string{"provider": "bing", "targetLanguage": "zh"})
	f.runErr = nil
	f.printOutput = ""
	service := newService(t, f, newStore())

	if _, err := service.Confirm(context.Background()); err == nil {
		t.Fatal("confirm succeeded without knowing which plist was loaded")
	}
	if len(f.called("bootout")) != 0 {
		t.Fatalf("booted out an unattributable job: %v", f.called("bootout"))
	}
	if _, err := os.Stat(f.plistPath()); err != nil {
		t.Fatalf("plist was moved: %v", err)
	}
}

// The path launchctl prints can be a symlink or carry a redundant segment; the
// comparison has to be about the file, not the string.
func TestLoadedJobPathComparisonFollowsSymlinks(t *testing.T) {
	f := newFake(t)
	f.installLegacy(t, map[string]string{"provider": "bing", "targetLanguage": "zh"})
	link := filepath.Join(f.home, "linked-plist")
	if err := os.Symlink(f.plistPath(), link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	f.runErr = nil
	f.printOutput = link
	service := newService(t, f, newStore())

	result, err := service.Confirm(context.Background())
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !result.StoppedLoader || len(f.called("bootout")) != 1 {
		t.Fatalf("a symlinked path was not recognised as this home's job: %+v %v", result, f.calls)
	}
}
