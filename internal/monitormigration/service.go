// Package monitormigration handles the handover from the standalone
// kimi-code-monitor desktop edition to this core's Monitor plugin.
//
// The standalone edition patches Kimi Code's desktop-dist bundle in place: it
// creates <dist>/kcm, backs up <dist>/index.html to <dist>/index.html.bak-kcm and
// injects a /kcm/loader.js script into <dist>/index.html. That loader mounts a
// second Monitor panel in the same sidebar, which is the collision this package
// prevents. Detection only reads files; restoring index.html and deleting the
// payload happens only after an explicit confirmation.
package monitormigration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/plugins"
)

// PluginID is the bundled Monitor plugin this migration gates.
const PluginID = "kimicode-plus.monitor"

const (
	distDirName = "desktop-dist"
	payloadName = "kcm"
	indexName   = "index.html"
	backupName  = indexName + ".bak-kcm"
	loaderPath  = "/kcm/loader.js"
)

// System is the whole operating-system surface of this package. Every field is
// injectable so tests run against a temporary home and a fake application
// directory; a nil or empty field falls back to the real implementation.
type System struct {
	Home      string
	Platform  string
	Env       func(string) string
	AppDirs   []string
	Stat      func(name string) (os.FileInfo, error)
	ReadFile  func(name string) ([]byte, error)
	WriteFile func(name string, data []byte, perm os.FileMode) error
	Rename    func(oldPath, newPath string) error
	Remove    func(name string) error
	RemoveAll func(path string) error
	Now       func() time.Time
}

// MarkerPath is where a completed migration is recorded. It lives in the core
// data directory, not in the application bundle, so uninstalling and
// reinstalling the plugin does not silently allow a second injector.
func MarkerPath(dataDir string) string {
	return filepath.Join(dataDir, "migrations", "monitor-legacy.json")
}

// Report is the whole state a settings page needs. It carries no secret.
type Report struct {
	PluginID   string `json:"pluginId"`
	DistDir    string `json:"distDir"`
	PayloadDir string `json:"payloadDir"`
	BackupPath string `json:"backupPath"`
	// Detected is true when any part of the standalone patch is still in place.
	Detected bool `json:"detected"`
	// PayloadPresent is true while <dist>/kcm exists.
	PayloadPresent bool `json:"payloadPresent"`
	// IndexPatched is true while index.html still loads /kcm/loader.js.
	IndexPatched bool `json:"indexPatched"`
	// BackupPresent is true while index.html.bak-kcm exists.
	BackupPresent bool `json:"backupPresent"`
	// Migrated is the recorded outcome, and only while no patch is detected.
	Migrated bool `json:"migrated"`
	// NeedsConfirmation is what blocks the plugin and what the page keys its
	// explanation off. A reinstalled standalone patch brings it back.
	NeedsConfirmation bool `json:"needsConfirmation"`
	// MigratedAt is set once the handover completed.
	MigratedAt string `json:"migratedAt,omitempty"`
	// Detail explains the last failure.
	Detail string `json:"detail,omitempty"`
}

// Result is the answer to a confirmed migration.
type Result struct {
	Report Report `json:"report"`
	// RestoredIndex is true when this call wrote index.html back.
	RestoredIndex bool `json:"restoredIndex"`
	// RemovedPayload is true when this call deleted <dist>/kcm.
	RemovedPayload bool `json:"removedPayload"`
	// AlreadyMigrated marks an idempotent repeat.
	AlreadyMigrated bool `json:"alreadyMigrated"`
	// Message is user-facing copy for the settings page.
	Message string `json:"message"`
}

// Service is the migration state machine. One instance serves one core.
type Service struct {
	sys     System
	dataDir string

	mu sync.Mutex
	// migrating serialises confirm calls so two settings windows cannot both
	// restore index.html and delete the payload.
	migrating bool
	// lastError keeps a failure visible to the page until a successful retry.
	lastError string
}

// New builds the service. Injected System fields replace the real implementation
// one by one, so a test overrides only what it observes.
func New(dataDir string, injected System) *Service {
	sys := realSystem()
	if injected.Home != "" {
		sys.Home = injected.Home
	}
	if injected.Platform != "" {
		sys.Platform = injected.Platform
	}
	if injected.Env != nil {
		sys.Env = injected.Env
	}
	if len(injected.AppDirs) > 0 {
		sys.AppDirs = append([]string(nil), injected.AppDirs...)
	}
	if injected.Stat != nil {
		sys.Stat = injected.Stat
	}
	if injected.ReadFile != nil {
		sys.ReadFile = injected.ReadFile
	}
	if injected.WriteFile != nil {
		sys.WriteFile = injected.WriteFile
	}
	if injected.Rename != nil {
		sys.Rename = injected.Rename
	}
	if injected.Remove != nil {
		sys.Remove = injected.Remove
	}
	if injected.RemoveAll != nil {
		sys.RemoveAll = injected.RemoveAll
	}
	if injected.Now != nil {
		sys.Now = injected.Now
	}
	return &Service{sys: sys, dataDir: dataDir}
}

func (s *Service) marker() string { return MarkerPath(s.dataDir) }

// candidates lists the desktop-dist directories the standalone installer patches.
// An explicit AppDirs override wins over discovery.
func (s *Service) candidates() []string {
	return distCandidates(s.sys.Platform, s.sys.Home, s.sys.Env, s.sys.AppDirs)
}

func distCandidates(platform, home string, env func(string) string, overrides []string) []string {
	if len(overrides) > 0 {
		return uniquePaths(overrides...)
	}
	var out []string
	if appDir := strings.TrimSpace(env("KIMI_CODE_APP_DIR")); appDir != "" {
		trimmed := strings.TrimRight(appDir, `/\`)
		if trimmed == distDirName || strings.HasSuffix(trimmed, "/"+distDirName) || strings.HasSuffix(trimmed, `\`+distDirName) {
			out = append(out, appDir)
		} else {
			// Upstream accepts either the application directory or the bundle
			// directory itself; both name the same desktop-dist.
			if platform == "windows" {
				out = append(out, joinPath(platform, appDir, "resources", distDirName))
			} else {
				out = append(out, joinPath(platform, appDir, "Contents", "Resources", distDirName))
			}
		}
	}
	if platform == "windows" {
		if local := strings.TrimSpace(env("LOCALAPPDATA")); local != "" {
			out = append(out, joinPath(platform, local, "Programs", "Kimi Code", "resources", distDirName))
		}
		out = append(out,
			`C:\Program Files\Kimi Code\resources\`+distDirName,
			`D:\kimi_code\Kimi Code\resources\`+distDirName,
		)
	} else {
		out = append(out, joinPath(platform, "/Applications", "Kimi Code.app", "Contents", "Resources", distDirName))
		if home != "" {
			out = append(out, joinPath(platform, home, "Applications", "Kimi Code.app", "Contents", "Resources", distDirName))
		}
	}
	return uniquePaths(out...)
}

// joinPath keeps candidate generation independent of the host that runs the
// test: Windows paths use backslashes even when the test runs on macOS.
func joinPath(platform string, parts ...string) string {
	if platform == "windows" {
		return strings.Join(parts, `\`)
	}
	return path.Join(parts...)
}

func uniquePaths(paths ...string) []string {
	out := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, filepath.Clean(path))
	}
	return out
}

// resolveDist is the first candidate that actually exists as a directory. A
// missing application bundle reports no directory rather than inventing one.
func (s *Service) resolveDist() string {
	for _, candidate := range s.candidates() {
		if info, err := s.sys.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}

// Status reports the current handover state. It never mutates anything.
func (s *Service) Status(_ context.Context) Report {
	report := Report{PluginID: PluginID, DistDir: s.resolveDist()}
	if report.DistDir != "" {
		report.PayloadDir = filepath.Join(report.DistDir, payloadName)
		report.BackupPath = filepath.Join(report.DistDir, backupName)
		if info, err := s.sys.Stat(report.PayloadDir); err == nil && info.IsDir() {
			report.PayloadPresent = true
		}
		if info, err := s.sys.Stat(report.BackupPath); err == nil && !info.IsDir() {
			report.BackupPresent = true
		}
		if raw, err := s.sys.ReadFile(filepath.Join(report.DistDir, indexName)); err == nil && bytes.Contains(raw, []byte(loaderPath)) {
			report.IndexPatched = true
		}
	}
	report.Detected = report.PayloadPresent || report.IndexPatched || report.BackupPresent
	if record, ok := s.readMarker(); ok && !report.Detected {
		report.Migrated = true
		report.MigratedAt = record.At
	}
	// Any patch marker blocks again, including one reinstalled after a completed
	// handover: two panels must never be mounted at once.
	report.NeedsConfirmation = report.Detected
	s.mu.Lock()
	report.Detail = s.lastError
	s.mu.Unlock()
	return report
}

// CheckEnable is the plugins.EnableGate for the Monitor plugin. A standalone
// desktop patch that has not been removed blocks it, with user-facing copy
// rather than an internal code.
func (s *Service) CheckEnable(ctx context.Context, id string) (string, string) {
	if id != PluginID {
		return "", ""
	}
	if !s.Status(ctx).NeedsConfirmation {
		return "", ""
	}
	return "PLUGIN_MIGRATION_REQUIRED",
		"检测到独立版 Kimi Code Monitor 桌面补丁，请先在插件详情中确认卸载独立版补丁后再启用，避免侧栏重复显示两个面板。"
}

// Confirm restores index.html, removes the injected payload and records the
// handover. It reports success only after those steps happened, or after an
// idempotent repeat.
func (s *Service) Confirm(ctx context.Context) (Result, error) {
	s.mu.Lock()
	if s.migrating {
		s.mu.Unlock()
		return Result{}, plugins.APIError(http.StatusConflict, "MONITOR_MIGRATION_BUSY", "迁移正在进行，请稍候")
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
		return Result{Report: report, AlreadyMigrated: true, Message: "独立版 Kimi Code Monitor 桌面补丁已经移除，不需要重复迁移。"}, nil
	}
	if !report.Detected {
		return Result{Report: report, Message: "没有检测到独立版 Kimi Code Monitor 桌面补丁，不需要迁移。"}, nil
	}
	if filepath.Base(report.DistDir) != distDirName || filepath.Base(report.PayloadDir) != payloadName {
		// A guard that cannot fire in practice; it exists so a future caller
		// cannot point this delete-and-restore at an arbitrary directory.
		message := "桌面补丁不在预期的 desktop-dist 目录内，已拒绝操作。"
		s.setError(message)
		return Result{Report: s.Status(ctx)}, plugins.APIError(http.StatusConflict, "MONITOR_MIGRATION_PATH", message)
	}

	restored, err := s.restoreIndex(report.DistDir)
	if err != nil {
		message := "无法还原 Kimi Code 的 index.html：" + err.Error() + "。请检查应用目录的写入权限，或在独立版目录运行 install.sh --uninstall 后再重试。"
		s.setError(message)
		return Result{Report: s.Status(ctx)}, plugins.APIError(http.StatusConflict, "MONITOR_MIGRATION_RESTORE_FAILED", message)
	}

	removed := false
	if report.PayloadPresent {
		if err := s.sys.RemoveAll(report.PayloadDir); err != nil {
			message := "index.html 已还原，但无法删除 kcm 目录：" + err.Error() + "。请检查应用目录的写入权限后重试。"
			s.setError(message)
			return Result{Report: s.Status(ctx), RestoredIndex: restored}, plugins.APIError(http.StatusConflict, "MONITOR_MIGRATION_PAYLOAD_FAILED", message)
		}
		removed = true
	}

	record := markerRecord{At: s.sys.Now().UTC().Format(time.RFC3339), Migrated: true, PluginID: PluginID, DistDir: report.DistDir}
	if err := s.writeMarker(record); err != nil {
		// The files are gone but the handover is not recorded; report the failure
		// instead of pretending it completed.
		message := "补丁已移除，但无法记录迁移结果：" + err.Error()
		s.setError(message)
		return Result{Report: s.Status(ctx), RestoredIndex: restored, RemovedPayload: removed}, plugins.APIError(http.StatusInternalServerError, "MONITOR_MIGRATION_MARKER_FAILED", message)
	}
	s.setError("")
	final := s.Status(ctx)
	final.MigratedAt = record.At
	message := "已移除独立版 Kimi Code Monitor 的桌面补丁。"
	if restored {
		message += "index.html 已还原为补丁前的版本。"
	}
	message += "如果当前窗口里已经加载过独立版 Monitor，请按 Cmd+R（Windows 为 Ctrl+R）重新载入 Kimi Code。"
	return Result{Report: final, RestoredIndex: restored, RemovedPayload: removed, Message: message}, nil
}

// restoreIndex puts index.html back before anything is deleted, so a failure
// leaves the application at least as usable as it was.
func (s *Service) restoreIndex(distDir string) (bool, error) {
	indexPath := filepath.Join(distDir, indexName)
	backupPath := filepath.Join(distDir, backupName)
	current, readErr := s.sys.ReadFile(indexPath)
	if readErr == nil && !bytes.Contains(current, []byte(loaderPath)) {
		if err := s.sys.Remove(backupPath); err != nil && !os.IsNotExist(err) {
			return false, fmt.Errorf("无法删除残留备份 %s：%w", backupPath, err)
		}
		return false, nil
	}
	if readErr != nil && !os.IsNotExist(readErr) {
		return false, fmt.Errorf("无法读取 %s：%w", indexPath, readErr)
	}

	backup, backupErr := s.sys.ReadFile(backupPath)
	if backupErr != nil && !os.IsNotExist(backupErr) {
		return false, fmt.Errorf("无法读取备份 %s：%w", backupPath, backupErr)
	}
	if os.IsNotExist(readErr) && os.IsNotExist(backupErr) {
		return false, nil
	}
	restored := stripLoaderScript(current)
	if backupErr == nil {
		fromBackup := stripLoaderScript(backup)
		if os.IsNotExist(readErr) || bytes.Equal(restored, fromBackup) {
			restored = fromBackup
		}
	}
	if writeErr := s.writeIndex(indexPath, restored); writeErr != nil {
		return false, writeErr
	}
	if backupErr == nil {
		if removeErr := s.sys.Remove(backupPath); removeErr != nil {
			return true, fmt.Errorf("无法删除备份 %s：%w", backupPath, removeErr)
		}
	}
	return true, nil
}

// writeIndex replaces index.html through a temporary file so a crash cannot
// leave the application with a half-written page.
func (s *Service) writeIndex(path string, data []byte) error {
	perm := os.FileMode(0o644)
	if info, err := s.sys.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}
	temp := path + ".kimicode-plus.next"
	if err := s.sys.WriteFile(temp, data, perm); err != nil {
		return err
	}
	if err := s.sys.Rename(temp, path); err != nil {
		_ = s.sys.Remove(temp)
		return err
	}
	return nil
}

var loaderTagRE = regexp.MustCompile(`(?i)<script\b[^>]*\bsrc=["']/kcm/loader\.js(?:\?[^"']*)?["'][^>]*></script>`)

// stripLoaderScript removes the injected loader tag. The standalone installer
// puts the tag on its own line; when </head> was indented it leaves a
// whitespace-only line behind, which is folded back so the page is restored
// byte for byte.
func stripLoaderScript(index []byte) []byte {
	lines := strings.Split(string(index), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if !loaderTagRE.MatchString(line) {
			out = append(out, line)
			continue
		}
		cleaned := loaderTagRE.ReplaceAllString(line, "")
		if strings.TrimSpace(cleaned) == "" {
			continue
		}
		if len(out) > 0 && (strings.Contains(strings.ToLower(cleaned), "</head>") || strings.TrimSpace(out[len(out)-1]) == "") {
			out[len(out)-1] += strings.TrimLeft(cleaned, " \t")
			continue
		}
		out = append(out, cleaned)
	}
	return []byte(strings.Join(out, "\n"))
}

type markerRecord struct {
	At       string `json:"at"`
	Migrated bool   `json:"migrated"`
	PluginID string `json:"pluginId"`
	DistDir  string `json:"distDir"`
}

func (s *Service) readMarker() (markerRecord, bool) {
	raw, err := s.sys.ReadFile(s.marker())
	if err != nil {
		return markerRecord{}, false
	}
	var record markerRecord
	if err := json.Unmarshal(raw, &record); err != nil || !record.Migrated || record.PluginID != PluginID {
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

// Register mounts the migration routes. They are core features rather than
// plugin routes because the Monitor plugin is blocked at the time they are used.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET "+plugins.APIPrefix+"/plugins/monitor/migration", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, s.Status(r.Context()))
	})
	mux.HandleFunc("POST "+plugins.APIPrefix+"/plugins/monitor/migration/confirm", func(w http.ResponseWriter, r *http.Request) {
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
	return "迁移未完成，独立版补丁保持原状。"
}

func realSystem() System {
	return System{
		Home:      realHome(),
		Platform:  runtime.GOOS,
		Env:       os.Getenv,
		Stat:      os.Stat,
		ReadFile:  os.ReadFile,
		WriteFile: os.WriteFile,
		Rename:    os.Rename,
		Remove:    os.Remove,
		RemoveAll: os.RemoveAll,
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
