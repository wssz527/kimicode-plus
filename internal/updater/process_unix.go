//go:build !windows

package updater

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func watchParent(pid int) (func(context.Context) error, func(), error) {
	if os.Getppid() != pid {
		return nil, nil, errors.New("无法确认待退出的旧核心进程")
	}
	return func(ctx context.Context) error {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for os.Getppid() == pid {
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}, func() {}, nil
}

func cleanupSupervisor(stage string) {
	_ = os.RemoveAll(stage)
}
