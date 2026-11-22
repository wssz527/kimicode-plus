package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

const hostAttachLockFile = "host-attach.lock"

// acquireHostAttachLock serializes concurrent attach attempts across processes
// so a restarted core and a background watcher never race over the host's
// temporary inspector port.
func acquireHostAttachLock(ctx context.Context, dataDir string) (*flock.Flock, error) {
	if dataDir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	gate := flock.New(filepath.Join(dataDir, hostAttachLockFile))
	ok, err := gate.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		_ = gate.Close()
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, fmt.Errorf("等待其他 KimiCode+ 接入流程完成超时：%w", err)
		}
		return nil, err
	}
	if !ok {
		_ = gate.Close()
		return nil, errors.New("KimiCode+ 正在接入，请稍后重试")
	}
	return gate, nil
}
