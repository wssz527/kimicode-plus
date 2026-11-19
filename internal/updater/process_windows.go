package updater

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true, CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
}

func watchParent(pid int) (func(context.Context) error, func(), error) {
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return nil, nil, err
	}
	return func(ctx context.Context) error {
		for ctx.Err() == nil {
			result, err := windows.WaitForSingleObject(process, 100)
			if err != nil || result == windows.WAIT_OBJECT_0 {
				return err
			}
			if result != uint32(windows.WAIT_TIMEOUT) {
				return errors.New("无法等待旧核心进程退出")
			}
		}
		return ctx.Err()
	}, func() { _ = windows.CloseHandle(process) }, nil
}

func retryFileOperation(action func() error) error {
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(250 * time.Millisecond) {
		err := action()
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED) || time.Now().After(deadline) {
			return err
		}
	}
}

func cleanupSupervisor(stage string) {
	// Windows keeps the running supervisor image open until the process exits.
	if err := os.RemoveAll(stage); err == nil {
		return
	}
	systemDir, err := windows.GetSystemDirectory()
	if err != nil {
		return
	}
	script := `$ProgressPreference = 'SilentlyContinue'
$process = Get-Process -Id ` + strconv.Itoa(os.Getpid()) + ` -ErrorAction SilentlyContinue
if ($null -ne $process) { try { [void]$process.WaitForExit(15000) } catch {} }
for ($attempt = 0; $attempt -lt 10; $attempt++) {
    try { Remove-Item -LiteralPath '` + psQuote.Replace(stage) + `' -Recurse -Force -ErrorAction Stop; break }
    catch { Start-Sleep -Milliseconds 250 }
}`
	encoded := utf16.Encode([]rune(script))
	data := make([]byte, len(encoded)*2)
	for i, unit := range encoded {
		binary.LittleEndian.PutUint16(data[i*2:], unit)
	}
	powershell := filepath.Join(systemDir, `WindowsPowerShell\v1.0\powershell.exe`)
	cmd := exec.Command(powershell, "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(data))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err == nil {
		_ = cmd.Process.Release()
	}
}

var psQuote = strings.NewReplacer("'", "''", "‘", "‘‘", "’", "’’", "‚", "‚‚", "‛", "‛‛")
