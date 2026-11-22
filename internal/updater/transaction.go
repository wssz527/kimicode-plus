package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	transactionName       = "transaction.json"
	activeTransactionName = "active-tx.json"

	phasePrepared      = "prepared"
	phaseBackedUp      = "backed_up"
	phaseReplaced      = "replaced"
	phaseVerifying     = "verifying"
	phaseRollingBack   = "rolling_back"
	phaseFilesRestored = "files_restored"
	phaseCommitted     = "committed"
	phaseRolledBack    = "rolled_back"
	phaseFailed        = "failed"
)

// updateTransaction records what one update is doing and where its independent
// backup lives. The supervisor writes it in stages so a crash never leaves an
// ambiguous mix of replaced binary and unknown database state.
type updateTransaction struct {
	Ticket          string   `json:"ticket"`
	Phase           string   `json:"phase"`
	Executable      string   `json:"executable"`
	PreviousVersion string   `json:"previousVersion"`
	TargetVersion   string   `json:"targetVersion"`
	PreviousDigest  string   `json:"previousDigest"`
	TargetDigest    string   `json:"targetDigest"`
	BackupPath      string   `json:"backupPath"`
	DBBackupDir     string   `json:"dbBackupDir"`
	ExpectedModules []string `json:"expectedModules,omitempty"`
	SupervisorPID   int      `json:"supervisorPid,omitempty"`
	UpdatedAt       string   `json:"updatedAt,omitempty"`

	dir string
}

type activeTransaction struct {
	Path   string `json:"path"`
	Ticket string `json:"ticket,omitempty"`
}

func transactionDir(dataDir, ticket string) string {
	if len(ticket) > 16 {
		ticket = ticket[:16]
	}
	return filepath.Join(dataDir, "updates", "tx-"+ticket)
}

func (tx *updateTransaction) path() string {
	return filepath.Join(tx.dir, transactionName)
}

func (tx *updateTransaction) save() error {
	tx.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return writeUpdateJSON(tx.path(), tx)
}

func (tx *updateTransaction) setPhase(phase string) error {
	tx.Phase = phase
	return tx.save()
}

func (job supervisorJob) beginTransaction(previous, target [sha256.Size]byte) (*updateTransaction, error) {
	dir := transactionDir(job.DataDir, job.Ticket)
	// A ticket is fresh for every update, but never trust that blind: an old
	// transaction directory must not become this update's backup path.
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("无法清理旧的更新事务目录：%w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "db-backup"), 0o700); err != nil {
		return nil, fmt.Errorf("无法创建更新事务目录：%w", err)
	}
	tx := &updateTransaction{
		Ticket:          job.Ticket,
		Phase:           phasePrepared,
		Executable:      job.Executable,
		PreviousVersion: job.PreviousVersion,
		TargetVersion:   job.TargetVersion,
		PreviousDigest:  hex.EncodeToString(previous[:]),
		TargetDigest:    hex.EncodeToString(target[:]),
		BackupPath:      filepath.Join(dir, "binary.bak"),
		DBBackupDir:     filepath.Join(dir, "db-backup"),
		ExpectedModules: append([]string(nil), job.ExpectedModules...),
		SupervisorPID:   os.Getpid(),
		dir:             dir,
	}
	if err := tx.save(); err != nil {
		return nil, fmt.Errorf("无法记录更新事务：%w", err)
	}
	if err := writeActiveTransaction(job.DataDir, tx); err != nil {
		return nil, err
	}
	return tx, nil
}

func writeActiveTransaction(dataDir string, tx *updateTransaction) error {
	point := activeTransaction{
		Path:   tx.path(),
		Ticket: tx.Ticket,
	}
	if err := writeUpdateJSON(filepath.Join(dataDir, "updates", activeTransactionName), point); err != nil {
		return fmt.Errorf("无法记录进行中的更新事务：%w", err)
	}
	return nil
}

func clearActiveTransaction(dataDir string) error {
	return removeUpdateFile(filepath.Join(dataDir, "updates", activeTransactionName))
}

// readActiveTransaction refuses a pointer that escapes <dataDir>/updates
// (including through symlinks) so a damaged or hostile pointer file can never
// direct a rollback elsewhere.
func readActiveTransaction(dataDir string) *updateTransaction {
	var point activeTransaction
	data, err := os.ReadFile(filepath.Join(dataDir, "updates", activeTransactionName))
	if err != nil || json.Unmarshal(data, &point) != nil || point.Path == "" {
		return nil
	}
	updates := filepath.Join(filepath.Clean(dataDir), "updates")
	if resolved, err := filepath.EvalSymlinks(updates); err == nil {
		updates = resolved
	}
	path := filepath.Clean(point.Path)
	if !filepath.IsAbs(path) {
		path = filepath.Join(updates, path)
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil
	}
	rel, err := filepath.Rel(updates, resolvedPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil
	}
	var tx updateTransaction
	data, err = os.ReadFile(resolvedPath)
	if err != nil || json.Unmarshal(data, &tx) != nil || tx.Ticket == "" || !filepath.IsAbs(tx.Executable) {
		return nil
	}
	tx.dir = filepath.Dir(resolvedPath)
	return &tx
}

func backupDatabases(dataDir, backupDir string) error {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !databaseFile(name) || !entry.Type().IsRegular() {
			continue
		}
		if err := atomicCopy(filepath.Join(dataDir, name), filepath.Join(backupDir, name), 0o600); err != nil {
			return fmt.Errorf("无法备份数据库 %s：%w", name, err)
		}
	}
	return nil
}

func databaseFile(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".db") || strings.HasSuffix(lower, ".db-wal") || strings.HasSuffix(lower, ".db-shm")
}

// restoreDatabases removes journal files the failed candidate may have left and
// restores every backed-up database atomically, so the previous core opens the
// exact schema it closed.
func restoreDatabases(dataDir, backupDir string) error {
	entries, err := os.ReadDir(backupDir)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("数据库备份目录不存在：%w", err)
	}
	if err != nil {
		return err
	}
	// An empty snapshot means the installation had no database file at all;
	// only the journal files the candidate may have left need clearing.
	if err := removeDatabaseJournals(dataDir); err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !databaseFile(name) {
			continue
		}
		if err := removeUpdateFile(filepath.Join(dataDir, name)); err != nil {
			return fmt.Errorf("无法替换数据库 %s：%w", name, err)
		}
		if err := atomicCopy(filepath.Join(backupDir, name), filepath.Join(dataDir, name), 0o600); err != nil {
			return fmt.Errorf("无法还原数据库 %s：%w", name, err)
		}
	}
	return nil
}

func removeDatabaseJournals(dataDir string) error {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		lower := strings.ToLower(entry.Name())
		if !strings.HasSuffix(lower, ".db-wal") && !strings.HasSuffix(lower, ".db-shm") {
			continue
		}
		if err := removeUpdateFile(filepath.Join(dataDir, entry.Name())); err != nil {
			return fmt.Errorf("无法清除候选核心的数据库日志 %s：%w", entry.Name(), err)
		}
	}
	return nil
}

func backupMatches(path, expectedDigest string) bool {
	sum, err := executableDigest(path)
	if err != nil {
		return false
	}
	return hex.EncodeToString(sum[:]) == expectedDigest
}
