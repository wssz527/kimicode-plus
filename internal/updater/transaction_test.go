package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func digestOf(t *testing.T, body string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func newTestTransaction(t *testing.T, dataDir, executable string) *updateTransaction {
	t.Helper()
	job := supervisorJob{Executable: executable, DataDir: dataDir, Ticket: strings.Repeat("a", 32)}
	previous, target := sha256.Sum256([]byte("previous")), sha256.Sum256([]byte("target"))
	tx, err := job.beginTransaction(previous, target)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func TestTransactionIsolationAndDatabaseRestore(t *testing.T) {
	dataDir := t.TempDir()
	executable := filepath.Join(t.TempDir(), "enhanced-kimi")
	writeTestFile(t, executable, "previous", 0o755)
	writeTestFile(t, filepath.Join(dataDir, "enhancement.db"), "old schema", 0o600)
	writeTestFile(t, filepath.Join(dataDir, "cache.db"), "old cache", 0o600)

	tx := newTestTransaction(t, dataDir, executable)
	// Every transaction owns its own directory: a second begin must not reuse the
	// previous backup path.
	if !strings.Contains(tx.dir, "tx-"+strings.Repeat("a", 16)) {
		t.Fatalf("transaction dir = %s", tx.dir)
	}
	if err := backupDatabases(dataDir, tx.DBBackupDir); err != nil {
		t.Fatal(err)
	}
	if err := tx.setPhase(phaseBackedUp); err != nil {
		t.Fatal(err)
	}
	// The candidate migrates the schema and leaves journal files behind.
	writeTestFile(t, filepath.Join(dataDir, "enhancement.db"), "new incompatible schema", 0o600)
	writeTestFile(t, filepath.Join(dataDir, "enhancement.db-wal"), "wal", 0o600)
	writeTestFile(t, filepath.Join(dataDir, "enhancement.db-shm"), "shm", 0o600)

	if err := restoreDatabases(dataDir, tx.DBBackupDir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"enhancement.db", "cache.db"} {
		data, err := os.ReadFile(filepath.Join(dataDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "enhancement.db" && string(data) != "old schema" {
			t.Fatalf("%s was not restored: %q", name, data)
		}
	}
	for _, name := range []string{"enhancement.db-wal", "enhancement.db-shm"} {
		if _, err := os.Stat(filepath.Join(dataDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("candidate journal %s survived the restore", name)
		}
	}
}

func TestActiveTransactionRefusesEscapingPointer(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "updates"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), transactionName)
	writeTestFile(t, outside, `{"ticket":"t","phase":"replaced","executable":"/tmp/x"}`, 0o600)
	pointer := `{"path":"` + outside + `"}`
	writeTestFile(t, filepath.Join(dataDir, "updates", activeTransactionName), pointer, 0o600)
	if tx := readActiveTransaction(dataDir); tx != nil {
		t.Fatalf("pointer outside the updates directory was accepted: %+v", tx)
	}
}

func TestVerifyModuleHealthRequiresEveryExpectedModuleReady(t *testing.T) {
	ready := []healthModule{{ID: "registry", State: "READY"}, {ID: "plugins", State: "READY"}}
	if err := verifyModuleHealth(ready, []string{"registry", "plugins"}); err != nil {
		t.Fatalf("healthy modules rejected: %v", err)
	}
	faulted := []healthModule{{ID: "registry", State: "READY"}, {ID: "plugins", State: "FAULTED", Message: "初始化失败"}}
	if err := verifyModuleHealth(faulted, []string{"registry"}); err == nil || !strings.Contains(err.Error(), "plugins") {
		t.Fatalf("a FAULTED module passed: %v", err)
	}
	missing := []healthModule{{ID: "registry", State: "READY"}}
	if err := verifyModuleHealth(missing, []string{"registry", "plugins"}); err == nil || !strings.Contains(err.Error(), "缺失") {
		t.Fatalf("a missing expected module passed: %v", err)
	}
	disabled := []healthModule{{ID: "registry", State: "READY"}, {ID: "plugins", State: "DISABLED"}}
	if err := verifyModuleHealth(disabled, []string{"registry", "plugins"}); err == nil {
		t.Fatal("a DISABLED expected module passed")
	}
}

func TestPrepareSupervisorSurvivesDeletedDownloadCopy(t *testing.T) {
	if runtimeIsWindows() {
		t.Skip("uses a Unix-style download directory layout")
	}
	root := t.TempDir()
	downloadSrc := filepath.Join(root, "Downloads", "enhanced-kimi")
	build := []byte("\xcf\xfa\xed\xfe core image")
	writeTestFile(t, downloadSrc, string(build), 0o700)
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	managed := managedCorePathForTest(dataDir)
	writeTestFile(t, managed, string(build), 0o700)

	var captured supervisorJob
	options := Options{
		CurrentVersion: "0.2.0", Executable: downloadSrc, DataDir: dataDir, GOOS: "darwin",
		RestartState:   func() RestartState { return RestartState{Port: 48352} },
		RequestRestart: func() {},
		TargetExecutable: func() string {
			return managed
		},
		launchSupervisor: func(_, jobPath string) error {
			data, err := os.ReadFile(jobPath)
			if err != nil {
				return err
			}
			return jsonUnmarshalJob(data, &captured)
		},
	}
	stage := filepath.Join(dataDir, "updates", "downloads", "update-test")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(stage, "binary"), string(build), 0o700)
	// The user followed the README and deleted the downloaded copy and its folder.
	if err := os.RemoveAll(filepath.Dir(downloadSrc)); err != nil {
		t.Fatal(err)
	}
	m := New(options)
	defer m.Close()
	if err := m.prepareSupervisor(stage, "0.3.0", extractedFiles{binary: filepath.Join(stage, "binary")}); err != nil {
		t.Fatalf("update was blocked after the download was deleted: %v", err)
	}
	if captured.Executable != managed {
		t.Fatalf("supervisor target = %s", captured.Executable)
	}
	if captured.WorkingDir != dataDir {
		t.Fatalf("working directory leaked the download folder: %s", captured.WorkingDir)
	}
	if captured.PreviousDigest != digestOf(t, string(build)) {
		t.Fatalf("previous digest = %s", captured.PreviousDigest)
	}
	if _, err := os.Stat(filepath.Join(stage, "supervisor")); err != nil {
		t.Fatalf("supervisor helper was not staged from the managed copy: %v", err)
	}
}

func runtimeIsWindows() bool { return runtime.GOOS == "windows" }

func managedCorePathForTest(dataDir string) string {
	return filepath.Join(dataDir, "resident", "enhanced-kimi")
}

func jsonUnmarshalJob(data []byte, job *supervisorJob) error { return json.Unmarshal(data, job) }
