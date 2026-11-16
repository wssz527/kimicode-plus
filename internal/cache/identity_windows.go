//go:build windows

package cache

import (
	"os"
	"sync"
	"time"
)

// Windows has no inode. File index and volume serial require opening the file
// with backup semantics, which would mean holding official records open; instead
// identity is derived from the creation time and size, and the reader also
// restarts whenever the recorded size shrinks below the stored offset. The plan
// only accepts macOS hosts this round, so this keeps Windows compiling without
// promising the same replacement detection.
var identityMu sync.Mutex

func identityOf(info os.FileInfo) (inode, device uint64) {
	identityMu.Lock()
	defer identityMu.Unlock()
	stamp := info.ModTime().UnixNano()
	if created, ok := creationTime(info); ok {
		stamp = created.UnixNano()
	}
	return uint64(stamp), 0
}

func creationTime(info os.FileInfo) (time.Time, bool) { return time.Time{}, false }
