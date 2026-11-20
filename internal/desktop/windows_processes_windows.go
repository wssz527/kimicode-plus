//go:build windows

package desktop

import (
	"errors"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Probe candidates are only a wake-up signal. CIM still verifies identity and flags before attach.
func windowsHostCandidates(session uint32) (map[uint32]windows.Filetime, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	parents := map[uint32]uint32{}
	for err := windows.Process32First(snapshot, &entry); ; err = windows.Process32Next(snapshot, &entry) {
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			break
		}
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), WindowsKimiExecutableName) {
			continue
		}
		var desktop uint32
		if windows.ProcessIdToSessionId(entry.ProcessID, &desktop) == nil && desktop == session {
			parents[entry.ProcessID] = entry.ParentProcessID
		}
	}
	roots := make(map[uint32]windows.Filetime, len(parents))
	for pid, ppid := range parents {
		if _, child := parents[ppid]; child {
			continue
		}
		handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if err != nil {
			continue
		}
		var started, exited, kernel, user windows.Filetime
		err = windows.GetProcessTimes(handle, &started, &exited, &kernel, &user)
		windows.CloseHandle(handle)
		if err == nil && exited == (windows.Filetime{}) {
			roots[pid] = started
		}
	}
	return roots, nil
}
