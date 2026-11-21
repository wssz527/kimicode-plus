//go:build darwin

package desktop

import (
	"errors"
	"fmt"
	"io"
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
			// The login item already runs this copy; only the release documents
			// may still be missing from a first install of an older build.
			return dest, syncReleaseDocuments(source, dest, false)
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
	return dest, syncReleaseDocuments(source, dest, true)
}

// syncReleaseDocuments copies the notices and license next to the managed core.
// The first resident start runs from that directory and the diagnostics page
// reads the files from there. A freshly copied binary brings its own release
// documents; an already running copy only gets the files it is missing.
func syncReleaseDocuments(source, dest string, refresh bool) error {
	sourceDir, targetDir := filepath.Dir(source), filepath.Dir(dest)
	for _, name := range []string{"THIRD_PARTY_NOTICES.txt", "LICENSE"} {
		from := filepath.Join(sourceDir, name)
		if info, err := os.Lstat(from); err != nil || !info.Mode().IsRegular() {
			continue
		}
		to := filepath.Join(targetDir, name)
		if info, err := os.Lstat(to); err == nil {
			if info.Mode().IsRegular() && !refresh {
				continue
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("数据目录中的 %s 不是普通文件", name)
			}
		}
		if err := atomicCopyFile(from, to, 0o644); err != nil {
			return fmt.Errorf("无法复制 %s：%w", name, err)
		}
	}
	return nil
}

func atomicCopyFile(source, destination string, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(destination), ".release-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	input, err := os.Open(source)
	if err == nil {
		_, err = io.Copy(file, input)
		err = errors.Join(err, input.Close())
	}
	if err == nil {
		err = file.Chmod(mode)
	}
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), destination)
}
