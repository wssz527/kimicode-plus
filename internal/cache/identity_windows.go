//go:build windows

package cache

import (
	"os"
	"syscall"
)

func identityOf(file *os.File, _ os.FileInfo) (uint64, uint64, error) {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &info); err != nil {
		return 0, 0, err
	}
	index := uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
	return index, uint64(info.VolumeSerialNumber), nil
}
