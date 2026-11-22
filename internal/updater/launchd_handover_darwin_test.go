//go:build darwin

package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"enhancedkimi/internal/platform"
)

type fakeLaunchdHandover struct {
	t          *testing.T
	dataDir    string
	executable string
	port       int
	nextPID    int

	mu            sync.Mutex
	activePID     int
	server        *http.Server
	instance      *platform.Instance
	kickstarts    int
	bootouts      int
	acquireErrs   []error
	acquireSuperv []bool
	onStart       func(attempt int, binaryContent string) (version string, modules []map[string]any, mutateDB bool)
}

func (f *fakeLaunchdHandover) kickstart(label string) error {
	f.mu.Lock()
	f.kickstarts++
	attempt := f.kickstarts
	f.nextPID++
	pid := f.nextPID
	f.mu.Unlock()

	go func() {
		instance, supervised, err := AcquireCore(f.dataDir)
		f.mu.Lock()
		f.acquireErrs = append(f.acquireErrs, err)
		f.acquireSuperv = append(f.acquireSuperv, supervised)
		f.mu.Unlock()
		if err != nil {
			return
		}
		binBytes, _ := os.ReadFile(f.executable)
		version, modules, mutateDB := f.onStart(attempt, string(binBytes))
		if mutateDB {
			_ = os.WriteFile(filepath.Join(f.dataDir, "enhancement.db"), []byte("candidate-v99-schema"), 0o600)
			_ = os.WriteFile(filepath.Join(f.dataDir, "enhancement.db-wal"), []byte("candidate-wal"), 0o600)
		}
		ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", f.port))
		if err != nil {
			_ = instance.Close()
			return
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/enhance/v1/health", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer launchd-test-token" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":  "OK",
				"version": version,
				"modules": modules,
			})
		})
		srv := &http.Server{Handler: mux}
		f.mu.Lock()
		f.activePID = pid
		f.server = srv
		f.instance = instance
		f.mu.Unlock()

		runtimePath := filepath.Join(f.dataDir, "runtime.json")
		_ = writeUpdateJSON(runtimePath, map[string]any{
			"pid":          pid,
			"url":          fmt.Sprintf("http://127.0.0.1:%d", f.port),
			"version":      version,
			"pairingToken": "launchd-test-token",
		})
		_ = srv.Serve(ln)
	}()
	return nil
}

func (f *fakeLaunchdHandover) bootstrap(string) error { return nil }

func (f *fakeLaunchdHandover) bootout(string) error {
	f.mu.Lock()
	f.bootouts++
	srv := f.server
	inst := f.instance
	f.server = nil
	f.instance = nil
	f.activePID = 0
	f.mu.Unlock()
	if srv != nil {
		_ = srv.Close()
	}
	if inst != nil {
		_ = inst.Close()
	}
	return nil
}

func (f *fakeLaunchdHandover) kill(string, string) error { return nil }

func (f *fakeLaunchdHandover) servicePID(string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.activePID, nil
}

func (f *fakeLaunchdHandover) cleanup() {
	_ = f.bootout("")
}

func reserveFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func TestLaunchdHandoverHealthyUpdateDoesNotSelfRollback(t *testing.T) {
	prevRunner := launchdRunner
	prevTimeout := coreTimeout
	coreTimeout = 5 * time.Second
	t.Cleanup(func() {
		launchdRunner = prevRunner
		coreTimeout = prevTimeout
	})

	dataDir := filepath.Join(t.TempDir(), "core")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(dataDir, "resident", "enhanced-kimi")
	stage := filepath.Join(dataDir, "updates", "downloads", "stage-ok")
	writeTestFile(t, executable, "old-binary-v0.2.0", 0o755)
	writeTestFile(t, filepath.Join(dataDir, "enhancement.db"), "old-schema", 0o600)
	writeTestFile(t, filepath.Join(stage, "binary"), "new-binary-v0.2.1", 0o755)

	port := reserveFreePort(t)
	fake := &fakeLaunchdHandover{
		t: t, dataDir: dataDir, executable: executable, port: port, nextPID: 9000,
		onStart: func(_ int, bin string) (string, []map[string]any, bool) {
			if bin == "new-binary-v0.2.1" {
				return "0.2.1", []map[string]any{
					{"id": "registry", "state": "READY"},
					{"id": "plugins", "state": "READY"},
				}, false
			}
			return "0.2.0", []map[string]any{
				{"id": "registry", "state": "READY"},
				{"id": "plugins", "state": "READY"},
			}, false
		},
	}
	t.Cleanup(fake.cleanup)
	launchdRunner = fake

	gate, err := openStartupGate(dataDir, executable)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()

	prevSum := sha256.Sum256([]byte("old-binary-v0.2.0"))
	nextSum := sha256.Sum256([]byte("new-binary-v0.2.1"))
	ticket := strings.Repeat("b", 32)
	receipt := updateReceipt{
		UpdateResult: UpdateResult{State: Restarting, PreviousVersion: "0.2.0", TargetVersion: "0.2.1"},
		Ticket:       ticket,
	}
	if err := receipt.write(dataDir); err != nil {
		t.Fatal(err)
	}
	job := supervisorJob{
		Executable:      executable,
		DataDir:         dataDir,
		OldPID:          8000,
		Ticket:          ticket,
		PreviousVersion: "0.2.0",
		TargetVersion:   "0.2.1",
		PreviousDigest:  hex.EncodeToString(prevSum[:]),
		TargetDigest:    hex.EncodeToString(nextSum[:]),
		Restart:         RestartState{Port: port},
		LaunchdLabel:    launchdDefaultLabel,
		ExpectedModules: []string{"registry", "plugins"},
	}

	if err := job.replaceAndRestart(stage, gate, receipt); err != nil {
		t.Fatalf("replaceAndRestart failed: %v", err)
	}
	res := readResult(dataDir)
	if res.State != Updated || res.Error != "" {
		t.Fatalf("healthy launchd handover did not finish Updated: %+v", res.UpdateResult)
	}
	if bin, err := os.ReadFile(executable); err != nil || string(bin) != "new-binary-v0.2.1" {
		t.Fatalf("binary after healthy launchd handover = %q, err=%v", bin, err)
	}
	if active := readActiveTransaction(dataDir); active != nil {
		t.Fatalf("active-tx remained after healthy launchd handover: %+v", active)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.kickstarts != 1 || fake.bootouts != 0 {
		t.Fatalf("kickstarts=%d bootouts=%d, want 1/0", fake.kickstarts, fake.bootouts)
	}
	if len(fake.acquireErrs) != 1 || fake.acquireErrs[0] != nil || !fake.acquireSuperv[0] {
		t.Fatalf("AcquireCore during launchd handover: errs=%v supervised=%v", fake.acquireErrs, fake.acquireSuperv)
	}
}

func TestStopLaunchdCoreWaitsForCapturedProcessExit(t *testing.T) {
	previousRunner, previousAlive := launchdRunner, launchdProcessAlive
	t.Cleanup(func() {
		launchdRunner, launchdProcessAlive = previousRunner, previousAlive
	})
	fake := &fakeLaunchdHandover{activePID: 4321}
	launchdRunner = fake
	released := make(chan struct{})
	observed := make(chan struct{}, 1)
	launchdProcessAlive = func(pid int) bool {
		if pid != 4321 {
			t.Fatalf("unexpected pid: %d", pid)
		}
		observed <- struct{}{}
		select {
		case <-released:
			return false
		default:
			return true
		}
	}
	done := make(chan error, 1)
	go func() { done <- (supervisorJob{LaunchdLabel: launchdDefaultLabel}).stopLaunchdCore() }()
	select {
	case <-observed:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not check the captured process")
	}
	select {
	case err := <-done:
		t.Fatalf("stop returned while the captured process was alive: %v", err)
	default:
	}
	close(released)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not finish after process exit")
	}
}

func TestLaunchdHandoverUnhealthyCandidateRollsBackBinaryAndDB(t *testing.T) {
	testLaunchdHandoverRollback(t, true)
}

func TestLaunchdRollbackWithUnhealthyRestoredCoreDoesNotPreserveActiveTransaction(t *testing.T) {
	testLaunchdHandoverRollback(t, false)
}

func testLaunchdHandoverRollback(t *testing.T, restoredHealthy bool) {
	prevRunner := launchdRunner
	prevTimeout := coreTimeout
	coreTimeout = 1500 * time.Millisecond
	t.Cleanup(func() {
		launchdRunner = prevRunner
		coreTimeout = prevTimeout
	})

	dataDir := filepath.Join(t.TempDir(), "core")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(dataDir, "resident", "enhanced-kimi")
	stage := filepath.Join(dataDir, "updates", "downloads", "stage-bad")
	writeTestFile(t, executable, "old-binary-v0.2.0", 0o755)
	writeTestFile(t, filepath.Join(dataDir, "enhancement.db"), "old-schema", 0o600)
	writeTestFile(t, filepath.Join(stage, "binary"), "broken-binary-v0.2.1", 0o755)

	port := reserveFreePort(t)
	fake := &fakeLaunchdHandover{
		t: t, dataDir: dataDir, executable: executable, port: port, nextPID: 9500,
		onStart: func(_ int, bin string) (string, []map[string]any, bool) {
			if bin == "broken-binary-v0.2.1" {
				return "0.2.1", []map[string]any{
					{"id": "registry", "state": "READY"},
					{"id": "plugins", "state": "FAULTED", "message": "schema migration failed"},
				}, true
			}
			oldModules := []map[string]any{
				{"id": "registry", "state": "READY"},
				{"id": "plugins", "state": "READY"},
			}
			if !restoredHealthy {
				oldModules[1]["state"] = "FAULTED"
				oldModules[1]["message"] = "old core health check failed"
			}
			return "0.2.0", oldModules, false
		},
	}
	t.Cleanup(fake.cleanup)
	launchdRunner = fake

	gate, err := openStartupGate(dataDir, executable)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()

	prevSum := sha256.Sum256([]byte("old-binary-v0.2.0"))
	nextSum := sha256.Sum256([]byte("broken-binary-v0.2.1"))
	ticket := strings.Repeat("c", 32)
	receipt := updateReceipt{
		UpdateResult: UpdateResult{State: Restarting, PreviousVersion: "0.2.0", TargetVersion: "0.2.1"},
		Ticket:       ticket,
	}
	if err := receipt.write(dataDir); err != nil {
		t.Fatal(err)
	}
	job := supervisorJob{
		Executable:      executable,
		DataDir:         dataDir,
		OldPID:          8500,
		Ticket:          ticket,
		PreviousVersion: "0.2.0",
		TargetVersion:   "0.2.1",
		PreviousDigest:  hex.EncodeToString(prevSum[:]),
		TargetDigest:    hex.EncodeToString(nextSum[:]),
		Restart:         RestartState{Port: port},
		LaunchdLabel:    launchdDefaultLabel,
		ExpectedModules: []string{"registry", "plugins"},
	}

	if err := job.replaceAndRestart(stage, gate, receipt); err != nil {
		t.Fatalf("replaceAndRestart failed: %v", err)
	}
	res := readResult(dataDir)
	wantState, wantBootouts := RolledBack, 1
	if !restoredHealthy {
		wantState, wantBootouts = Failed, 2
	}
	if res.State != wantState {
		t.Fatalf("launchd rollback state = %+v, want %s (restoredHealthy=%t)", res.UpdateResult, wantState, restoredHealthy)
	}
	if bin, err := os.ReadFile(executable); err != nil || string(bin) != "old-binary-v0.2.0" {
		t.Fatalf("binary was not restored after launchd rollback: %q, %v", bin, err)
	}
	if db, err := os.ReadFile(filepath.Join(dataDir, "enhancement.db")); err != nil || string(db) != "old-schema" {
		t.Fatalf("database was not restored after launchd rollback: %q, %v", db, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "enhancement.db-wal")); !os.IsNotExist(err) {
		t.Fatalf("candidate wal survived launchd rollback: %v", err)
	}
	if active := readActiveTransaction(dataDir); active != nil {
		t.Fatalf("active-tx remained after launchd rollback: %+v", active)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.kickstarts != 2 || fake.bootouts != wantBootouts {
		t.Fatalf("kickstarts=%d bootouts=%d, want 2/%d", fake.kickstarts, fake.bootouts, wantBootouts)
	}
}
