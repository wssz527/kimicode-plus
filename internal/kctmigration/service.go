// Package kctmigration handles the one-time handover from the standalone
// kimi-cot-translate desktop extension (KCT) to this core's KCT adapter plugin.
//
// Two independent injectors writing the same thinking blocks is the failure this
// package exists to prevent, so the adapter plugin cannot be enabled while the
// standalone loader is still installed. Detection only reads files: it never
// starts, stops or writes anything. Stopping the loader happens only after an
// explicit confirmation, and only for the exact label below.
package kctmigration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/plugins"
)

// Label is the standalone KCT background loader. It is the only launchd job this
// package may ever touch; the core's own agent (com.user.enhanced-kimi.attach)
// is deliberately unreachable from here, and the path guard in disableLoader
// makes that structural rather than a comment.
const Label = "com.user.kimi-cot-translate.runtime"

// SettingsKey is the plugin-private storage key the adapter reads its settings
// from. The migration writes it only when the user has no adapter settings yet.
const SettingsKey = "settings.v1"

// PluginID is the bundled adapter this migration gates.
const PluginID = "kimicode-plus.kct"

const legacyDataDirName = "kimi-cot-translate"

// Only the public settings keys the standalone CLI writes are considered.
// Anything else in cli.json is ignored: the desktop build keeps its API key in
// the renderer's localStorage, which this migration never opens, and a secret
// must not travel through this package at all.
var publicProviders = map[string]bool{"bing": true, "google": true}
var targetLanguages = map[string]bool{"zh": true, "en": true, "ja": true, "ko": true, "es": true, "fr": true, "de": true, "ru": true}

// System is the whole operating-system surface of this package. Every field is
// injectable so tests run against a temporary HOME and a fake launchctl; a nil
// field falls back to the real implementation.
type System struct {
	Home      string
	UID       func() int
	Run       func(ctx context.Context, args ...string) ([]byte, error)
	Stat      func(name string) (os.FileInfo, error)
	ReadFile  func(name string) ([]byte, error)
	Rename    func(oldPath, newPath string) error
	WriteFile func(name string, data []byte, perm os.FileMode) error
	Now       func() time.Time
}

// Store is the adapter's private settings storage. The plugin host implements
// it; this package never reaches into plugin storage directly.
type Store interface {
	PluginStorage(ctx context.Context, id, key string) ([]byte, bool, error)
	SetPluginStorage(ctx context.Context, id, key string, value []byte) error
}

// MarkerPath is where a completed migration is recorded. It lives in the core
// data directory, not in plugin storage, so uninstalling and reinstalling the
// adapter does not silently re-enable a second injector.
func MarkerPath(dataDir string) string {
	return filepath.Join(dataDir, "migrations", "kct-legacy.json")
}

// Report is the whole state a settings page needs. It carries no secret.
type Report struct {
	PluginID string `json:"pluginId"`
	Label    string `json:"label"`
	DataDir  string `json:"dataDir"`
	// Detected is true when the standalone loader is still in place.
	Detected bool `json:"detected"`
	// PlistPresent means the loader would be registered again at next login.
	PlistPresent bool `json:"plistPresent"`
	// DataDirPresent means the extension has been installed at least once; its
	// data stays on disk even after the loader is removed.
	DataDirPresent bool `json:"dataDirPresent"`
	// Migrated is the recorded outcome. A migrated install no longer blocks the
	// adapter even though the old data directory usually still exists.
	Migrated bool `json:"migrated"`
	// NeedsConfirmation is what the page keys its explanation off.
	NeedsConfirmation bool `json:"needsConfirmation"`
	// Settings is the public subset that would be migrated.
	Settings map[string]string `json:"settings"`
	// MigratedAt is set once the handover completed.
	MigratedAt string `json:"migratedAt,omitempty"`
	// Detail explains the last failure.
	Detail string `json:"detail,omitempty"`
}

// Result is the answer to a confirmed migration.
type Result struct {
	Report Report `json:"report"`
	// StoppedLoader is true only when this call actually unloaded the job.
	StoppedLoader bool `json:"stoppedLoader"`
	// DisabledPlist is true when the plist was moved aside so it cannot come back
	// at the next login.
	DisabledPlist bool `json:"disabledPlist"`
	// MigratedKeys lists the public setting keys copied into the adapter.
	MigratedKeys []string `json:"migratedKeys"`
	// AlreadyMigrated marks an idempotent repeat.
	AlreadyMigrated bool `json:"alreadyMigrated"`
	// Message is user-facing copy for the settings page.
	Message string `json:"message"`
}

// Service is the migration state machine. One instance serves one core.
type Service struct {
	sys     System
	store   Store
	dataDir string

	mu sync.Mutex
	// migrating serialises confirm calls so two settings windows cannot both
	// bootout and rename; the second would otherwise report success for work the
	// first one did.
	migrating bool
	// lastError keeps a failure visible to the page until a successful retry.
	lastError string
}

// New builds the service. Injected System fields replace the real implementation
// one by one, so a test overrides only what it observes.
func New(dataDir string, store Store, injected System) *Service {
	sys := realSystem()
	if injected.Home != "" {
		sys.Home = injected.Home
	}
	if injected.UID != nil {
		sys.UID = injected.UID
	}
	if injected.Run != nil {
		sys.Run = injected.Run
	}
	if injected.Stat != nil {
		sys.Stat = injected.Stat
	}
	if injected.ReadFile != nil {
		sys.ReadFile = injected.ReadFile
	}
	if injected.Rename != nil {
		sys.Rename = injected.Rename
	}
	if injected.WriteFile != nil {
		sys.WriteFile = injected.WriteFile
	}
	if injected.Now != nil {
		sys.Now = injected.Now
	}
	return &Service{sys: sys, store: store, dataDir: dataDir}
}

func (s *Service) paths() (plist string, legacyDir string) {
	return filepath.Join(s.sys.Home, "Library", "LaunchAgents", Label+".plist"),
		filepath.Join(s.sys.Home, "Library", "Application Support", legacyDataDirName)
}

func (s *Service) marker() string { return MarkerPath(s.dataDir) }

// Status reports the current handover state. It never mutates anything.
func (s *Service) Status(_ context.Context) Report {
	plist, legacyDir := s.paths()
	report := Report{PluginID: PluginID, Label: Label, DataDir: legacyDir, Settings: map[string]string{}}
	if info, err := s.sys.Stat(plist); err == nil && !info.IsDir() {
		report.PlistPresent = true
	}
	if info, err := s.sys.Stat(legacyDir); err == nil && info.IsDir() {
		report.DataDirPresent = true
	}
	if settings := s.readPublicSettings(legacyDir); settings != nil {
		report.Settings = settings
	}
	if record, ok := s.readMarker(); ok {
		report.Migrated = true
		report.MigratedAt = record.At
	}
	report.Detected = report.PlistPresent || report.DataDirPresent
	report.NeedsConfirmation = report.Detected && !report.Migrated
	if report.Migrated {
		// Once migrated the leftover data directory is expected; it no longer
		// means a second injector exists.
		report.Detected = false
		report.NeedsConfirmation = false
	}
	s.mu.Lock()
	report.Detail = s.lastError
	s.mu.Unlock()
	return report
}

// CheckEnable is the plugins.EnableGate for the adapter. A standalone install
// that has not been handed over blocks it, with user-facing copy rather than an
// internal code.
func (s *Service) CheckEnable(ctx context.Context, id string) (string, string) {
	if id != PluginID {
		return "", ""
	}
	if !s.Status(ctx).NeedsConfirmation {
		return "", ""
	}
	return "PLUGIN_MIGRATION_REQUIRED",
		"检测到独立版 KCT。请先在插件详情里确认迁移并停用它的后台加载器，避免同一份思考内容被注入两次。"
}

// Confirm stops the standalone loader and copies the public settings. It reports
// success only after both steps happened, or after an idempotent repeat.
func (s *Service) Confirm(ctx context.Context) (Result, error) {
	s.mu.Lock()
	if s.migrating {
		s.mu.Unlock()
		return Result{}, plugins.APIError(http.StatusConflict, "KCT_MIGRATION_BUSY", "迁移正在进行，请稍候")
	}
	s.migrating = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.migrating = false
		s.mu.Unlock()
	}()

	report := s.Status(ctx)
	if report.Migrated {
		return Result{Report: report, AlreadyMigrated: true, Message: "独立版 KCT 已经停用，不需要重复迁移。"}, nil
	}
	if !report.Detected {
		return Result{Report: report, Message: "没有检测到独立版 KCT，不需要迁移。"}, nil
	}

	plist, legacyDir := s.paths()
	stopped, disabled, err := s.disableLoader(ctx, plist)
	if err != nil {
		s.setError(err.Error())
		return Result{Report: s.Status(ctx), StoppedLoader: stopped}, err
	}

	migratedKeys := []string{}
	var settingsErr error
	if settings, ok := s.pendingSettings(ctx, report.Settings); ok {
		settingsErr = s.writeSettings(ctx, settings)
		if settingsErr == nil {
			migratedKeys = sortedKeys(settings)
		}
	}
	// A settings write failure does not put the loader back: the user confirmed
	// the handover and the single-injector guarantee is what matters. The page
	// reports that settings could not be copied so they can be set again.
	record := markerRecord{At: s.sys.Now().UTC().Format(time.RFC3339), Migrated: true, Label: Label, DataDir: legacyDir}
	if err := s.writeMarker(record); err != nil {
		// Without the marker a later enable check would block forever on a loader
		// that is already gone. Report instead of pretending.
		s.setError("已停用独立版，但无法记录迁移结果：" + err.Error())
		return Result{Report: s.Status(ctx), StoppedLoader: stopped, DisabledPlist: disabled, MigratedKeys: migratedKeys}, err
	}
	if settingsErr != nil {
		s.setError("独立版已停用，但设置未迁移：" + settingsErr.Error())
	} else {
		s.setError("")
	}
	final := s.Status(ctx)
	final.MigratedAt = record.At
	message := "已停用独立版 KCT 的后台加载器。"
	if disabled {
		message += "它的启动项已移到旁边，下次登录不会重新加载。"
	}
	if len(migratedKeys) > 0 {
		message += "已迁移：" + strings.Join(migratedKeys, "、") + "。"
	}
	if settingsErr != nil {
		message += " 设置未能写入，请在新插件里重新选择。"
	}
	return Result{
		Report:        final,
		StoppedLoader: stopped,
		DisabledPlist: disabled,
		MigratedKeys:  migratedKeys,
		Message:       message,
	}, settingsErr
}

// disableLoader unloads the exact KCT job and moves its plist aside. The steps
// are ordered so a failure leaves the loader at least as able to run as before.
func (s *Service) disableLoader(ctx context.Context, plist string) (stopped, disabled bool, err error) {
	if filepath.Base(plist) != Label+".plist" || filepath.Base(filepath.Dir(plist)) != "LaunchAgents" {
		// A guard that cannot fire in practice; it exists so a future caller
		// cannot point this function at the core's own agent.
		return false, false, errors.New("拒绝操作非 KCT 的启动项")
	}
	if _, statErr := s.sys.Stat(plist); statErr != nil {
		if os.IsNotExist(statErr) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("无法读取独立版启动项：%w", statErr)
	}
	loaded, ours, err := s.loaded(ctx, plist)
	if err != nil {
		return false, false, err
	}
	if loaded && !ours {
		// Another home installed the extension under the same label. This core has
		// no claim on that job: renaming this plist still stops *this* install from
		// reloading, but booting the other one out would break an install the user
		// is still using.
		if renameErr := s.sys.Rename(plist, plist+".disabled-by-kimicode-plus"); renameErr != nil {
			return false, false, fmt.Errorf("移动独立版启动项失败：%w", renameErr)
		}
		return false, true, nil
	}
	if loaded {
		if _, runErr := s.launchctl(ctx, "bootout", s.domain()+"/"+Label); runErr != nil && !isMissingJob(runErr) {
			return false, false, fmt.Errorf("停用独立版后台加载器失败：%w", runErr)
		} else if runErr == nil {
			stopped = true
		}
	}
	if renameErr := s.sys.Rename(plist, plist+".disabled-by-kimicode-plus"); renameErr != nil {
		// The job is unloaded but the file would reload it at next login. Put the
		// world back: restart the job when we stopped it, and report failure.
		if stopped {
			_, _ = s.launchctl(ctx, "bootstrap", s.domain(), plist)
		}
		return false, false, fmt.Errorf("移动独立版启动项失败：%w", renameErr)
	}
	return stopped, true, nil
}

// loaded reports whether the exact job is loaded, and whether the loaded job is
// the one this home's plist declares. launchctl addresses a job by label inside
// the user's gui domain, so a second installation — another home, a test harness
// with an isolated HOME — is invisible to the label alone. Booting that job out
// would stop an extension this core never migrated, so the path has to match.
func (s *Service) loaded(ctx context.Context, plist string) (loaded, ours bool, err error) {
	output, err := s.launchctl(ctx, "print", s.domain()+"/"+Label)
	if err != nil {
		if isMissingJob(err) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("无法确认独立版后台加载器状态：%w", err)
	}
	path := jobPath(output)
	if path == "" {
		// The job exists but launchctl did not say which plist it came from. Acting
		// on it could stop an installation this core never migrated, so refuse
		// rather than guess.
		return true, false, errors.New("无法确认已加载的后台加载器属于哪个安装")
	}
	return true, samePath(path, plist), nil
}

// jobPath reads the `path = /…/x.plist` line launchctl prints for a job.
func jobPath(output []byte) string {
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "path = ") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, "path = "))
	}
	return ""
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	left, errLeft := filepath.EvalSymlinks(a)
	right, errRight := filepath.EvalSymlinks(b)
	if errLeft != nil || errRight != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return left == right
}

func (s *Service) domain() string {
	uid := 0
	if s.sys.UID != nil {
		uid = s.sys.UID()
	}
	return "gui/" + strconv.Itoa(uid)
}

func (s *Service) launchctl(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "bootout" {
		// `launchctl bootout gui/<uid>` ends the whole GUI session; only the KCT
		// job inside that domain may ever be addressed from here.
		target := s.domain() + "/" + Label
		if len(args) != 2 || args[1] != target || !strings.Contains(args[1], "/"+Label) {
			return nil, fmt.Errorf("%w：需要 %s，拒绝 %s", errUnsafeBootout, target, strings.Join(args, " "))
		}
	}
	if s.sys.Run == nil {
		return nil, errors.New("launchctl 不可用")
	}
	return s.sys.Run(ctx, args...)
}

// errUnsafeBootout is the refusal a bootout gets when it does not name the
// exact KCT job. It is a sentinel so a test can prove the refusal happened
// before launchctl was invoked.
var errUnsafeBootout = errors.New("拒绝执行未指定 KCT 服务目标的 bootout")

func isMissingJob(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "could not find service") || strings.Contains(text, "not found") || strings.Contains(text, "no such process")
}

// pendingSettings decides what to copy. An adapter that already has settings
// keeps them: the standalone values are a starting point, not an override.
func (s *Service) pendingSettings(ctx context.Context, public map[string]string) (map[string]string, bool) {
	settings := map[string]string{}
	if lang := public["targetLanguage"]; targetLanguages[lang] {
		settings["targetLanguage"] = lang
	}
	if provider := public["provider"]; publicProviders[provider] {
		settings["provider"] = provider
	}
	if len(settings) == 0 {
		return nil, false
	}
	if s.store != nil {
		if _, exists, err := s.store.PluginStorage(ctx, PluginID, SettingsKey); err == nil && exists {
			return nil, false
		}
	}
	return settings, true
}

func (s *Service) writeSettings(ctx context.Context, settings map[string]string) error {
	if s.store == nil {
		return errors.New("插件存储不可用")
	}
	payload := map[string]any{"migratedFrom": "kimi-cot-translate"}
	// Only the keys actually read from the standalone CLI are written. A guessed
	// target language would silently translate into the wrong language.
	for key, value := range settings {
		payload[key] = value
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.store.SetPluginStorage(ctx, PluginID, SettingsKey, raw)
}

// readPublicSettings reads only the two documented keys. A malformed or missing
// file is not an error: there is simply nothing to migrate.
func (s *Service) readPublicSettings(dir string) map[string]string {
	raw, err := s.sys.ReadFile(filepath.Join(dir, "cli.json"))
	if err != nil {
		return nil
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	out := map[string]string{}
	for _, key := range []string{"provider", "targetLanguage"} {
		var value string
		if rawValue, ok := parsed[key]; ok && json.Unmarshal(rawValue, &value) == nil && value != "" {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type markerRecord struct {
	At       string `json:"at"`
	Migrated bool   `json:"migrated"`
	Label    string `json:"label"`
	DataDir  string `json:"dataDir"`
}

func (s *Service) readMarker() (markerRecord, bool) {
	raw, err := s.sys.ReadFile(s.marker())
	if err != nil {
		return markerRecord{}, false
	}
	var record markerRecord
	if err := json.Unmarshal(raw, &record); err != nil || !record.Migrated || record.Label != Label {
		return markerRecord{}, false
	}
	return record, true
}

// writeMarker writes through a temporary file so a crash cannot leave a
// half-written marker that later reads as "migrated".
func (s *Service) writeMarker(record markerRecord) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	target := s.marker()
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	temp := target + ".next"
	if err := s.sys.WriteFile(temp, raw, 0o600); err != nil {
		return err
	}
	return s.sys.Rename(temp, target)
}

func (s *Service) setError(message string) {
	s.mu.Lock()
	s.lastError = message
	s.mu.Unlock()
}

// Register mounts the migration routes. They are core features rather than plugin
// routes because the adapter plugin is disabled at the time they are used.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET "+plugins.APIPrefix+"/kct/migration", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, s.Status(r.Context()))
	})
	mux.HandleFunc("POST "+plugins.APIPrefix+"/kct/migration/confirm", func(w http.ResponseWriter, r *http.Request) {
		result, err := s.Confirm(r.Context())
		if err != nil {
			// The report still travels: it is what the page uses to decide whether
			// to offer a retry.
			httpx.JSON(w, statusFor(err), map[string]any{"report": s.Status(r.Context()), "result": result, "error": messageFor(err)})
			return
		}
		httpx.JSON(w, http.StatusOK, result)
	})
}

func statusFor(err error) int {
	var apiErr *plugins.Error
	if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status <= 599 {
		return apiErr.Status
	}
	return http.StatusInternalServerError
}

func messageFor(err error) string {
	var apiErr *plugins.Error
	if errors.As(err, &apiErr) {
		return apiErr.Message
	}
	return "迁移未完成，独立版保持原状。"
}

func sortedKeys(settings map[string]string) []string {
	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func realSystem() System {
	return System{
		Home: realHome(),
		UID:  os.Getuid,
		Run: func(ctx context.Context, args ...string) ([]byte, error) {
			command := exec.CommandContext(ctx, "/bin/launchctl", args...)
			output, err := command.CombinedOutput()
			if err != nil {
				return output, fmt.Errorf("launchctl %s：%w：%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
			}
			return output, nil
		},
		Stat:      os.Stat,
		ReadFile:  os.ReadFile,
		Rename:    os.Rename,
		WriteFile: os.WriteFile,
		Now:       time.Now,
	}
}

func realHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
