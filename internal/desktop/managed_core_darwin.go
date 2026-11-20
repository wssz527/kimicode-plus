//go:build darwin

package desktop

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func installManagedCore(source, dataDir string) (string, error) {
	if !filepath.IsAbs(source) || !filepath.IsAbs(dataDir) {
		return "", errors.New("后台核心要求可执行文件和数据目录的绝对路径")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", err
	}
	root, err := os.Lstat(dataDir)
	if err != nil || !root.IsDir() || root.Mode().Perm()&0o077 != 0 {
		return "", errors.New("增强数据目录必须是当前用户的私有目录")
	}
	dest := managedCoreExecutable(dataDir)
	if err := os.Mkdir(filepath.Dir(dest), 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	dir, err := os.Lstat(filepath.Dir(dest))
	if err != nil || !dir.IsDir() || dir.Mode().Perm()&0o077 != 0 {
		return "", errors.New("后台核心目录必须是当前用户的私有目录")
	}
	srcInfo, err := os.Stat(source)
	if err != nil || !srcInfo.Mode().IsRegular() {
		return "", errors.New("后台核心可执行文件无效")
	}
	if info, err := os.Lstat(dest); err == nil {
		if !info.Mode().IsRegular() {
			return "", errors.New("数据目录中的后台核心不可执行")
		}
		if os.SameFile(srcInfo, info) || launches.loaded(managedCoreLabel) {
			if info.Mode().Perm()&0o100 == 0 {
				return "", errors.New("数据目录中的后台核心不可执行")
			}
			return dest, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	file, err := os.CreateTemp(filepath.Dir(dest), ".core-*")
	if err != nil {
		return "", err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if err := file.Close(); err != nil {
		return "", err
	}
	// Preserve the downloaded file's attributes and signature when copying it out of Downloads.
	if out, err := exec.Command("/bin/cp", "-p", source, tmp).CombinedOutput(); err != nil {
		return "", fmt.Errorf("无法复制后台核心：%w (%s)", err, strings.TrimSpace(string(out)))
	}
	if err := os.Chmod(tmp, 0o700); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return "", err
	}
	return dest, nil
}
