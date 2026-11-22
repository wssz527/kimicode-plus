package updater

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInterruptedTransactionRestoresDatabasesAndBinary(t *testing.T) {
	dataDir := t.TempDir()
	executable := filepath.Join(t.TempDir(), "enhanced-kimi")
	writeTestFile(t, executable, "previous", 0o755)
	writeTestFile(t, filepath.Join(dataDir, "enhancement.db"), "old schema", 0o600)
	writeTestFile(t, filepath.Join(dataDir, "updates", "result.json"), `{"state":"restarting"}`, 0o600)

	tx := newTestTransaction(t, dataDir, executable)
	if err := updateFile(executable, tx.BackupPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := backupDatabases(dataDir, tx.DBBackupDir); err != nil {
		t.Fatal(err)
	}
	// The supervisor replaced both files and then died before verifying.
	writeTestFile(t, executable, "candidate", 0o755)
	writeTestFile(t, filepath.Join(dataDir, "enhancement.db"), "candidate schema", 0o600)
	writeTestFile(t, filepath.Join(dataDir, "enhancement.db-wal"), "wal", 0o600)
	if err := tx.setPhase(phaseReplaced); err != nil {
		t.Fatal(err)
	}
	recovery, err := recoverInterruptedTransaction(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.runningImageReplaced {
		t.Fatal("the recovered process is not the candidate image")
	}
	if data, err := os.ReadFile(executable); err != nil || string(data) != "previous" {
		t.Fatalf("binary was not restored: %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(dataDir, "enhancement.db")); err != nil || string(data) != "old schema" {
		t.Fatalf("database was not restored: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "enhancement.db-wal")); !os.IsNotExist(err) {
		t.Fatal("candidate journal survived the recovery")
	}
	if !strings.Contains(recovery.detail, phaseReplaced) || strings.Contains(recovery.detail, "rolled_back 阶段") {
		t.Fatalf("recovery reported the rewritten phase instead of the interrupted phase: %q", recovery.detail)
	}
	if active := readActiveTransaction(dataDir); active != nil {
		t.Fatalf("recovered transaction still blocks new updates: %+v", active)
	}
	// The backup stays in its own directory: a later update must not reuse it.
	if _, err := os.Stat(tx.dir); err != nil {
		t.Fatalf("independent backup directory was removed: %v", err)
	}
}

func TestInterruptedTransactionBeforeReplacementKeepsFiles(t *testing.T) {
	dataDir := t.TempDir()
	executable := filepath.Join(t.TempDir(), "enhanced-kimi")
	writeTestFile(t, executable, "previous", 0o755)
	writeTestFile(t, filepath.Join(dataDir, "enhancement.db"), "schema", 0o600)
	tx := newTestTransaction(t, dataDir, executable)
	if err := tx.setPhase(phasePrepared); err != nil {
		t.Fatal(err)
	}
	recovery, err := recoverInterruptedTransaction(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recovery.detail, "未替换任何文件") {
		t.Fatalf("detail = %q", recovery.detail)
	}
	if data, err := os.ReadFile(filepath.Join(dataDir, "enhancement.db")); err != nil || string(data) != "schema" {
		t.Fatalf("database changed before any replacement: %q, %v", data, err)
	}
}

func TestBackupDigestMustMatchBeforeRestoring(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binary.bak")
	writeTestFile(t, path, "previous", 0o755)
	if !backupMatches(path, digestOf(t, "previous")) {
		t.Fatal("matching backup was rejected")
	}
	if backupMatches(path, digestOf(t, "candidate")) {
		t.Fatal("tampered backup was accepted")
	}
	if backupMatches(filepath.Join(t.TempDir(), "absent"), digestOf(t, "previous")) {
		t.Fatal("missing backup was accepted")
	}
}

// The candidate must be recognised before its file is replaced, otherwise the
// check would compare the restored binary with the target digest and always
// report the older release.
func TestInterruptedRecoveryDetectsTheRunningCandidate(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Skip("no executable path in this environment")
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	sum, err := executableDigest(executable)
	if err != nil {
		t.Fatal(err)
	}
	if !runningImageIs(fmt.Sprintf("%x", sum)) {
		t.Fatal("the running binary was not recognised by its digest")
	}
	if runningImageIs(strings.Repeat("0", 64)) {
		t.Fatal("an unrelated digest matched the running binary")
	}
}

func TestInterruptedCommittedTransactionReportsUpdated(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "core")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "enhanced-kimi")
	writeTestFile(t, executable, "candidate", 0o755)
	writeTestFile(t, filepath.Join(dataDir, "enhancement.db"), "candidate schema", 0o600)
	writeTestFile(t, filepath.Join(dataDir, "updates", "result.json"), `{"state":"restarting","targetVersion":"0.2.1","previousVersion":"0.2.0"}`, 0o600)
	writeTestFile(t, filepath.Join(dataDir, "updates", "restart.lock"), "", 0o600)

	tx := newTestTransaction(t, dataDir, executable)
	if err := tx.setPhase(phaseCommitted); err != nil {
		t.Fatal(err)
	}
	instance, supervised, err := AcquireCore(dataDir)
	if err != nil || supervised {
		t.Fatalf("AcquireCore = (%v, %v, %v)", instance, supervised, err)
	}
	defer instance.Close()
	result := readResult(dataDir)
	if result.State != Updated || result.Error != "" {
		t.Fatalf("committed transaction was not finalized as Updated: %+v", result.UpdateResult)
	}
	if active := readActiveTransaction(dataDir); active != nil {
		t.Fatalf("committed active transaction was not cleared: %+v", active)
	}
	if data, err := os.ReadFile(executable); err != nil || string(data) != "candidate" {
		t.Fatalf("committed candidate binary was altered: %q, %v", data, err)
	}
}

func TestInterruptedTransactionWithMissingDatabaseBackupDoesNotLoop(t *testing.T) {
	dataDir := t.TempDir()
	executable := filepath.Join(t.TempDir(), "enhanced-kimi")
	writeTestFile(t, executable, "previous", 0o755)
	writeTestFile(t, filepath.Join(dataDir, "enhancement.db"), "candidate schema", 0o600)
	tx := newTestTransaction(t, dataDir, executable)
	if err := updateFile(executable, tx.BackupPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(tx.DBBackupDir); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, executable, "candidate", 0o755)
	if err := tx.setPhase(phaseReplaced); err != nil {
		t.Fatal(err)
	}
	recovery, err := recoverInterruptedTransaction(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.state != Failed || !strings.Contains(recovery.detail, "数据库备份目录不存在") {
		t.Fatalf("missing backup was not reported: %+v", recovery)
	}
	if active := readActiveTransaction(dataDir); active != nil || hasRecoverableActiveTransaction(dataDir) {
		t.Fatalf("missing backup left a permanent recovery loop: %+v", active)
	}
}

func TestPreservedFailedTransactionAutoRecoversOnNextAcquire(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "core")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "enhanced-kimi")
	writeTestFile(t, executable, "previous", 0o755)
	writeTestFile(t, filepath.Join(dataDir, "enhancement.db"), "old schema", 0o600)
	writeTestFile(t, filepath.Join(dataDir, "updates", "restart.lock"), "", 0o600)

	tx := newTestTransaction(t, dataDir, executable)
	if err := updateFile(executable, tx.BackupPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := backupDatabases(dataDir, tx.DBBackupDir); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, executable, "candidate", 0o755)
	writeTestFile(t, filepath.Join(dataDir, "enhancement.db"), "candidate schema", 0o600)

	receipt := updateReceipt{UpdateResult: UpdateResult{State: Restarting, TargetVersion: "0.2.1", PreviousVersion: "0.2.0"}, Ticket: tx.Ticket}
	job := supervisorJob{DataDir: dataDir, Executable: executable}
	if err := job.failPreservingRecovery(tx, receipt, fmt.Errorf("无法重新锁定核心更新")); err != nil {
		t.Fatal(err)
	}
	if active := readActiveTransaction(dataDir); active == nil || active.Phase != phaseFailed {
		t.Fatalf("active-tx was not preserved after post-replacement failure: %+v", active)
	}
	if res := readResult(dataDir); res.State != Failed || !strings.Contains(res.Error, tx.dir) {
		t.Fatalf("failure receipt missing backup dir: %+v", res.UpdateResult)
	}

	instance, supervised, err := AcquireCore(dataDir)
	if err != nil || supervised {
		t.Fatalf("AcquireCore = (%v, %v, %v)", instance, supervised, err)
	}
	defer instance.Close()
	if data, err := os.ReadFile(executable); err != nil || string(data) != "previous" {
		t.Fatalf("binary was not auto-recovered on next start: %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(dataDir, "enhancement.db")); err != nil || string(data) != "old schema" {
		t.Fatalf("database was not auto-recovered on next start: %q, %v", data, err)
	}
	if active := readActiveTransaction(dataDir); active != nil {
		t.Fatalf("active-tx was not cleared after auto-recovery: %+v", active)
	}
}

func TestFilesRestoredFailureNeverRestoresDatabaseAgain(t *testing.T) {
	dataDir := t.TempDir()
	executable := filepath.Join(t.TempDir(), "enhanced-kimi")
	writeTestFile(t, executable, "previous", 0o755)
	writeTestFile(t, filepath.Join(dataDir, "enhancement.db"), "new user data", 0o600)
	tx := newTestTransaction(t, dataDir, executable)
	if err := tx.setPhase(phaseFilesRestored); err != nil {
		t.Fatal(err)
	}
	if hasRecoverableActiveTransaction(dataDir) {
		t.Fatal("a completed file rollback remained recoverable")
	}
	recovery, err := recoverInterruptedTransaction(dataDir)
	if err != nil || recovery.state != Failed {
		t.Fatalf("files-restored recovery = %+v, %v", recovery, err)
	}
	if data, err := os.ReadFile(filepath.Join(dataDir, "enhancement.db")); err != nil || string(data) != "new user data" {
		t.Fatalf("new user data was overwritten: %q, %v", data, err)
	}
}
