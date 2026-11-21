package updater

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"enhancedkimi/internal/platform"

	"github.com/gofrs/flock"
)

const (
	SupervisorCommand = "__ekd_apply_update"
	childTicketEnv    = "EKD_UPDATE_CHILD_TICKET"
	restartName       = "update-restart.json"
)

type RestartState struct {
	Port          int  `json:"port"`
	Reattach      bool `json:"reattach"`
	ReattachDaily bool `json:"reattachDaily"`
}

func ReadRestartState(dir string) RestartState {
	var state RestartState
	data, err := os.ReadFile(filepath.Join(dir, restartName))
	if err != nil || json.Unmarshal(data, &state) != nil || state.Port < 1 || state.Port > 65535 {
		return RestartState{}
	}
	return state
}

func ClearRestartState(dir string) {
	_ = os.Remove(filepath.Join(dir, restartName))
}

type UpdateResult struct {
	State           string `json:"state"`
	TargetVersion   string `json:"targetVersion"`
	PreviousVersion string `json:"previousVersion"`
	CompletedAt     string `json:"completedAt,omitempty"`
	Error           string `json:"error,omitempty"`
}

type updateReceipt struct {
	UpdateResult
	Ticket string `json:"ticket,omitempty"`
}

func readResult(dir string) updateReceipt {
	if dir == "" {
		return updateReceipt{}
	}
	var result updateReceipt
	data, err := os.ReadFile(filepath.Join(dir, "updates", "result.json"))
	if err != nil || json.Unmarshal(data, &result) != nil {
		return updateReceipt{}
	}
	return result
}

func (r updateReceipt) write(dir string) error {
	return writeUpdateJSON(filepath.Join(dir, "updates", "result.json"), r)
}

func (r updateReceipt) finish(dir, state, reason string) error {
	r.State, r.Error, r.Ticket = state, reason, ""
	r.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	return r.write(dir)
}

func writeUpdateJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return platform.AtomicPrivateFile(path, data)
}

func startupLockPath(dir string) string {
	return filepath.Join(dir, "updates", "restart.lock")
}

func supervisorLockPath(dir string) string {
	return filepath.Join(dir, "updates", "supervisor.lock")
}

// supervisorLockHeld reports whether an active update supervisor currently owns
// supervisor.lock. Unlike restart.lock (which the supervisor drops during a
// launchd handover so the candidate can acquire the core), supervisor.lock stays
// held until the supervisor exits or crashes.
func supervisorLockHeld(dir string) (bool, error) {
	path := supervisorLockPath(dir)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	lock := flock.New(path)
	defer lock.Close()
	unlocked, err := lock.TryRLock()
	if err != nil {
		return false, err
	}
	return !unlocked, nil
}

func hasRecoverableActiveTransaction(dataDir string) bool {
	tx := readActiveTransaction(dataDir)
	if tx == nil {
		return false
	}
	switch tx.Phase {
	case phaseReplaced, phaseVerifying, phaseRollingBack, phaseFailed, phaseCommitted:
		return true
	default:
		return false
	}
}

func AcquireCore(dir string) (*platform.Instance, bool, error) {
	ticket := os.Getenv(childTicketEnv)
	_ = os.Unsetenv(childTicketEnv)
	lockPath := startupLockPath(dir)
	if _, err := os.Stat(lockPath); errors.Is(err, os.ErrNotExist) {
		instance, err := platform.Acquire(dir)
		return instance, false, err
	} else if err != nil {
		return nil, false, err
	}
	gate := flock.New(lockPath)
	defer gate.Close()
	locked, err := gate.TryRLock()
	if err != nil {
		return nil, false, err
	}
	result := readResult(dir)
	supervised := !locked && result.State == Restarting && ticket != "" && ticket == result.Ticket
	if !locked && !supervised {
		return nil, false, ErrBusy
	}
	instance, err := platform.Acquire(dir)
	if err != nil {
		return nil, false, err
	}
	if locked {
		supervisorAlive, err := supervisorLockHeld(instance.Dir)
		if err != nil {
			_ = instance.Close()
			return nil, false, err
		}
		if supervisorAlive {
			// A live supervisor released restart.lock intentionally to hand off
			// startup to launchd; do not mistake the handover for a supervisor crash.
			if result.State == Restarting {
				return instance, true, nil
			}
			return instance, false, nil
		}
		if result.State == Restarting || hasRecoverableActiveTransaction(instance.Dir) {
			recovery, err := recoverInterruptedTransaction(instance.Dir)
			if err != nil {
				_ = instance.Close()
				return nil, false, err
			}
			finalState := recovery.state
			if finalState == "" {
				finalState = Failed
			}
			if err = result.finish(instance.Dir, finalState, recovery.detail); err != nil {
				_ = instance.Close()
				return nil, false, fmt.Errorf("无法记录中断的更新：%w", err)
			}
			if recovery.runningImageReplaced {
				// This process is the candidate that was just rolled back. It cannot
				// keep serving a database restored under it; stepping down lets the
				// restored binary start instead of migrating the schema again.
				_ = instance.Close()
				return nil, false, errors.New(recovery.detail)
			}
		}
	}
	return instance, supervised, nil
}

type interruptedRecovery struct {
	state                string
	detail               string
	runningImageReplaced bool
}

// recoverInterruptedTransaction undoes what a supervisor that died mid-update
// left behind. It never touches an older backup: it reads only the transaction
// the crashed run pointed at, and it restores files only when the recorded
// digests still match.
func recoverInterruptedTransaction(dataDir string) (interruptedRecovery, error) {
	tx := readActiveTransaction(dataDir)
	if tx == nil {
		return interruptedRecovery{state: Failed, detail: "更新监督进程已退出，无法确认更新结果"}, nil
	}
	switch tx.Phase {
	case phaseCommitted:
		_ = clearActiveTransaction(dataDir)
		return interruptedRecovery{state: Updated}, nil
	case phaseReplaced, phaseVerifying, phaseRollingBack, phaseFailed:
	default:
		_ = clearActiveTransaction(dataDir)
		return interruptedRecovery{state: Failed, detail: fmt.Sprintf("更新监督进程在 %s 阶段退出，未替换任何文件，备份保留在 %s", tx.Phase, tx.dir)}, nil
	}
	// A new update must never reuse this run's backup, so it stays in its own
	// directory even when recovery succeeds.
	// The running image is measured before the binary is restored: afterwards the
	// file on disk is the previous release and the answer would always be false.
	runningCandidate := runningImageIs(tx.TargetDigest)
	var problems []string
	backupCorrupt := false
	if err := restoreDatabases(dataDir, tx.DBBackupDir); err != nil {
		problems = append(problems, fmt.Sprintf("数据库还原失败：%v", err))
		if errors.Is(err, os.ErrNotExist) {
			backupCorrupt = true
		}
	}
	if backupMatches(tx.BackupPath, tx.PreviousDigest) {
		if err := updateFile(tx.BackupPath, tx.Executable, 0o755); err != nil {
			problems = append(problems, fmt.Sprintf("核心文件还原失败：%v", err))
		}
	} else {
		problems = append(problems, "旧核心备份缺失或已损坏")
		backupCorrupt = true
	}
	if len(problems) > 0 {
		if backupCorrupt {
			_ = clearActiveTransaction(dataDir)
		}
		return interruptedRecovery{state: Failed, detail: fmt.Sprintf("更新在 %s 阶段中断（正在运行的核心为%s版本）；%s；备份保留在 %s", tx.Phase, runningLabel(runningCandidate), strings.Join(problems, "；"), tx.dir)}, nil
	}
	_ = tx.setPhase(phaseRolledBack)
	_ = clearActiveTransaction(dataDir)
	detail := fmt.Sprintf("更新在 %s 阶段被中断，已还原数据库与核心文件", tx.Phase)
	if runningCandidate {
		detail += "；当前进程是已回滚的新版核心，即将退出以便旧版启动"
	}
	return interruptedRecovery{state: Failed, detail: detail, runningImageReplaced: runningCandidate}, nil
}

// runningImageIs reports whether this process is the candidate binary. On a
// successful rollback it must step down instead of keeping the new image alive
// against a restored database.
func runningImageIs(targetDigest string) bool {
	if targetDigest == "" {
		return false
	}
	executable, err := os.Executable()
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	sum, err := executableDigest(executable)
	if err != nil {
		return false
	}
	raw := fmt.Sprintf("%x", sum)
	return raw == targetDigest
}

func runningLabel(candidate bool) string {
	if candidate {
		return "新版"
	}
	return "旧版"
}
