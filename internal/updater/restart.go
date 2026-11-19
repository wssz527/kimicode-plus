package updater

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	return retryFileOperation(func() error { return platform.AtomicPrivateFile(path, data) })
}

func startupLockPath(dir string) string {
	return filepath.Join(dir, "updates", "restart.lock")
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
	if locked && result.State == Restarting {
		if err := result.finish(instance.Dir, Failed, "更新监督进程已退出，无法确认更新结果"); err != nil {
			_ = instance.Close()
			return nil, false, fmt.Errorf("无法记录中断的更新：%w", err)
		}
	}
	return instance, supervised, nil
}
