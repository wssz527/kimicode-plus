package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/gofrs/flock"
)

var ErrInstanceOwned = errors.New("enhancement instance already owns this data directory")

type Instance struct {
	lock *flock.Flock
	Dir  string
}

func Acquire(dir string) (*Instance, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("data directory must be a real private directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("data directory must have private permissions (0700)")
	}
	if runtime.GOOS == "windows" {
		if err = ensurePrivateDirACL(abs); err != nil {
			return nil, fmt.Errorf("data directory ACL enforcement failed: %w", err)
		}
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(abs, "core.lock"))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrInstanceOwned
	}
	return &Instance{lock, abs}, nil
}
func (i *Instance) Close() error { return i.lock.Close() }
