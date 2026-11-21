package updater

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enhancedkimi/internal/platform"

	"github.com/gofrs/flock"
)

func TestCheckCoreHealthRequiresNewRuntimeAndMatchingHealth(t *testing.T) {
	version := "0.2.1"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/enhance/v1/health" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("unexpected health request: %s", r.URL.Path)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "OK", "version": version})
	}))
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "runtime.json")
	writeRecord := func(pid int, target string) {
		t.Helper()
		if err := writeUpdateJSON(path, map[string]any{"pid": pid, "url": server.URL, "version": target, "pairingToken": "test-token"}); err != nil {
			t.Fatal(err)
		}
	}
	validate := func(reason string) {
		t.Helper()
		err := checkCoreHealth(t.Context(), server.Client(), path, server.URL, 87, "0.2.1", nil)
		if (err == nil) != (reason == "") || err != nil && !strings.Contains(err.Error(), reason) {
			t.Fatalf("health check returned %v, expected %q", err, reason)
		}
	}
	validate("尚未读取到新运行记录")
	writeRecord(86, "0.2.1")
	validate("运行记录中的进程")
	writeRecord(87, "0.2.0")
	validate("运行记录版本")
	writeRecord(87, "v0.2.1")
	validate("")
	if err := checkCoreHealth(t.Context(), server.Client(), path, server.URL, 87, "0.2.1+build", nil); err == nil {
		t.Fatal("different build metadata matched the target version")
	}
	wrongHealth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"OK","version":"0.2.0"}`))
	}))
	t.Cleanup(wrongHealth.Close)
	if err := writeUpdateJSON(path, map[string]any{"pid": 87, "url": wrongHealth.URL, "version": "0.2.1", "pairingToken": "test-token"}); err != nil {
		t.Fatal(err)
	}
	if err := checkCoreHealth(t.Context(), wrongHealth.Client(), path, wrongHealth.URL, 87, "0.2.1", nil); err == nil || !strings.Contains(err.Error(), "健康接口未确认") {
		t.Fatalf("health version mismatch: %v", err)
	}
}

func TestAcquireCoreReservesStartupForSupervisedProcess(t *testing.T) {
	original, err := platform.Acquire(filepath.Join(t.TempDir(), "core"))
	if err != nil {
		t.Fatal(err)
	}
	dir := original.Dir
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Dir(startupLockPath(dir)), 0o700); err != nil {
		t.Fatal(err)
	}
	gate := flock.New(startupLockPath(dir))
	t.Cleanup(func() { _ = gate.Close() })
	if locked, err := gate.TryLock(); !locked || err != nil {
		t.Fatalf("could not reserve startup: %v", err)
	}
	pending := updateReceipt{UpdateResult: UpdateResult{State: Restarting, PreviousVersion: "0.2.0", TargetVersion: "0.2.1"}, Ticket: "test-ticket"}
	if err := pending.write(dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv(childTicketEnv, "unrelated-process")
	if _, _, err := AcquireCore(dir); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent startup was not blocked: %v", err)
	}
	t.Setenv(childTicketEnv, pending.Ticket)
	child, supervised, err := AcquireCore(dir)
	if err != nil || !supervised {
		t.Fatalf("supervised child was blocked: %v", err)
	}
	if os.Getenv(childTicketEnv) != "" {
		t.Fatal("startup ticket was passed on to the core's environment")
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gate.Close(); err != nil {
		t.Fatal(err)
	}
	core, supervised, err := AcquireCore(dir)
	if err != nil || supervised {
		t.Fatalf("ordinary startup was blocked after interrupted supervision: %v", err)
	}
	defer core.Close()
	if result := readResult(dir); result.State != Failed || result.Ticket != "" || !strings.Contains(result.Error, "更新监督进程已退出") {
		t.Fatalf("interruption was not diagnosed: %+v", result.UpdateResult)
	}
}

func TestApplyClearsRestartingWhenSupervisorFailsToAcknowledge(t *testing.T) {
	r := darwinRelease(t, "\xcf\xfa\xed\xfe staged update")
	o := testOptions(t, serveRelease(t, r).URL, "darwin")
	o.RequestRestart = func() { t.Error("old core was asked to stop without a supervisor") }
	handoffErr := errors.New("supervisor stopped before acknowledging")
	o.launchSupervisor = func(_, path string) error {
		raw, err := os.ReadFile(path)
		var job supervisorJob
		if err != nil || json.Unmarshal(raw, &job) != nil || job.Ticket == "" {
			t.Fatalf("invalid supervisor job: %v", err)
		}
		receipt := updateReceipt{UpdateResult: UpdateResult{State: Restarting, TargetVersion: job.TargetVersion, PreviousVersion: job.PreviousVersion}, Ticket: job.Ticket}
		if err := receipt.write(job.DataDir); err != nil {
			t.Fatal(err)
		}
		return handoffErr
	}
	m := New(o)
	defer m.Close()
	if status, err := m.Apply(t.Context()); !errors.Is(err, handoffErr) || status.State != Failed || status.LastAttempt == nil || status.LastAttempt.Error != handoffErr.Error() {
		t.Fatalf("failed handoff did not reach diagnostics: %+v, %v", status, err)
	}
	if status, err := m.Check(t.Context()); err != nil || status.State != Available {
		t.Fatalf("failed handoff blocked later checks: %+v, %v", status, err)
	}
	checkFile(t, o.Executable, oldBinary)
	checkEmptyDownloads(t, o.DataDir)
}

func TestHealthyUpdateReportsLicenseCopyFailure(t *testing.T) {
	dir, stage := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Dir(startupLockPath(dir)), 0o700); err != nil {
		t.Fatal(err)
	}
	job := supervisorJob{Executable: filepath.Join(dir, "enhanced-kimi"), DataDir: dir, HasNotices: true, HasLicense: true}
	if err := os.WriteFile(filepath.Join(stage, licenseName), []byte("new license"), 0o644); err != nil {
		t.Fatal(err)
	}
	tx := &updateTransaction{dir: filepath.Join(dir, "updates", "tx-test")}
	if err := os.MkdirAll(tx.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	receipt := updateReceipt{UpdateResult: UpdateResult{TargetVersion: "0.3.0", PreviousVersion: "0.2.0"}}
	if err := receipt.finish(dir, Updated, ""); err != nil {
		t.Fatal(err)
	}
	if err := job.cleanUpTransaction(tx, receipt, Updated, "", job.installReleaseLicenses(stage)); err != nil {
		t.Fatal(err)
	}
	checkFile(t, filepath.Join(dir, licenseName), "new license")
	if result := readResult(dir); result.State != Updated || !strings.Contains(result.Error, "无法更新 "+noticesName) {
		t.Fatalf("license copy problem missing from update result: %+v", result.UpdateResult)
	}
	if _, err := os.Stat(tx.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transaction backup was not cleaned: %v", err)
	}
}

func TestStatusSurfacesFailedSupervisorHandoffWhileOldCoreRuns(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Dir(startupLockPath(dir)), 0o700); err != nil {
		t.Fatal(err)
	}
	m := New(Options{DataDir: dir, CurrentVersion: "0.2.0"})
	defer m.Close()
	m.change(func(s *Status) { s.State = Restarting })
	receipt := updateReceipt{UpdateResult: UpdateResult{TargetVersion: "0.3.0", PreviousVersion: "0.2.0"}}
	reason := "更新失败，旧核心未能退出（context deadline exceeded）"
	if err := receipt.finish(dir, Failed, reason); err != nil {
		t.Fatal(err)
	}
	if status := m.Status(); status.State != Failed || status.Error != reason || status.LastAttempt == nil || status.LastAttempt.Error != reason {
		t.Fatalf("failure missing while old core still runs: %+v", status)
	}
}

// The supervisor must never start a second --resident process while launchd
// keeps one alive. These tests cover the handover bookkeeping only: the process
// launch itself belongs to launchd and is exercised by the desktop package.
func TestLaunchdHandoverRequiresALoadedLoginItem(t *testing.T) {
	target := "/data/resident/enhanced-kimi"
	options := Options{GOOS: "darwin", Executable: "/Applications/core", DataDir: "/data"}
	managed := &Options{GOOS: "darwin", Executable: "/Applications/core", DataDir: "/data",
		Launchd: LaunchdOptions{Enabled: func(p string) bool { return p == target }, Managed: func() bool { return false }}}
	if label := launchdLabelFor(options, target); label != "" {
		t.Fatalf("a managed target without an installed login item requested a handover: %s", label)
	}
	if label := launchdLabelFor(*managed, target); label != "" {
		t.Fatalf("an unloaded login item still requested a handover: %s", label)
	}
	managed.Launchd.Managed = func() bool { return true }
	if label := launchdLabelFor(*managed, target); label != launchdDefaultLabel {
		t.Fatalf("handover label = %q", label)
	}
	if label := launchdLabelFor(*managed, managed.Executable); label != "" {
		t.Fatalf("a non-managed target requested a handover: %s", label)
	}
	// When the core itself is running from the launchd-managed resident copy,
	// options.Executable == targetExe and handover MUST still be selected.
	residentSelf := *managed
	residentSelf.Executable = target
	if label := launchdLabelFor(residentSelf, target); label != launchdDefaultLabel {
		t.Fatalf("resident self-update handover label = %q, want %q", label, launchdDefaultLabel)
	}
}
