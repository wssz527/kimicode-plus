//go:build windows

package platform

import (
	"fmt"
	"os/exec"
	"os/user"
	"path/filepath"
	"os"
)

func ensurePrivateDirACL(dir string) error {
	current, err := user.Current()
	if err != nil {
		return fmt.Errorf("resolve current Windows user: %w", err)
	}
	args, err := buildWindowsACLArgs(dir, current.Uid)
	if err != nil {
		return err
	}
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	icacls := filepath.Join(systemRoot, "System32", "icacls.exe")
	if output, err := exec.Command(icacls, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("apply private ACL to %s: %w (%s)", dir, err, string(output))
	}
	return nil
}
