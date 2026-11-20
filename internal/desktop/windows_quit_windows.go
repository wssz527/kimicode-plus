//go:build windows

package desktop

import (
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var restartManager = windows.NewLazySystemDLL("Rstrtmgr.dll")

func quitWindowsApplication(proc WindowsProcessInfo) error {
	started, err := time.Parse(time.RFC3339Nano, proc.Started)
	if err != nil {
		return err
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(proc.PID))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	identity := struct {
		PID     uint32
		Started windows.Filetime
	}{PID: uint32(proc.PID)}
	var exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &identity.Started, &exited, &kernel, &user); err != nil {
		return err
	}
	if !time.Unix(0, identity.Started.Nanoseconds()).Truncate(time.Microsecond).Equal(started.Truncate(time.Microsecond)) || exited != (windows.Filetime{}) {
		return fmt.Errorf("Kimi Code 进程已变化")
	}
	var session uint32
	var key [33]uint16
	result, _, _ := restartManager.NewProc("RmStartSession").Call(uintptr(unsafe.Pointer(&session)), 0, uintptr(unsafe.Pointer(&key[0])))
	if result != 0 {
		return fmt.Errorf("RmStartSession: %w", syscall.Errno(result))
	}
	defer restartManager.NewProc("RmEndSession").Call(uintptr(session))
	result, _, _ = restartManager.NewProc("RmRegisterResources").Call(uintptr(session), 0, 0, 1, uintptr(unsafe.Pointer(&identity)), 0, 0)
	runtime.KeepAlive(&identity)
	if result != 0 {
		return fmt.Errorf("RmRegisterResources: %w", syscall.Errno(result))
	}
	// Register only this process identity and request a non-forced shutdown.
	result, _, _ = restartManager.NewProc("RmShutdown").Call(uintptr(session), 0, 0)
	if result != 0 {
		return fmt.Errorf("RmShutdown: %w", syscall.Errno(result))
	}
	return nil
}
