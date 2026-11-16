//go:build !windows

package cache

import (
	"os"
	"syscall"
)

// identityOf reports the inode and device of a file. Rotation and replacement
// change them, which is how an incremental reader knows to restart from offset 0
// even when the replacement happens to be larger than the old file.
func identityOf(info os.FileInfo) (inode, device uint64) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0
	}
	return stat.Ino, uint64(stat.Dev)
}
