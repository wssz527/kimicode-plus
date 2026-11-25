package updater

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"enhancedkimi/internal/platform"
)

type downloadCandidate struct {
	archive   asset
	checksums asset
}

const (
	maxArchiveBytes = 128 << 20
	maxSumsBytes    = 256 << 10
	noticesName     = "THIRD_PARTY_NOTICES.txt"
)

var (
	releaseDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
	checksumPattern      = regexp.MustCompile(`^([a-f0-9]{64}) [ *]([^/\\\s]+)$`)
	ErrUnavailable       = errors.New("当前没有可用的核心更新")
)

func (m *Manager) Apply(ctx context.Context) (Status, error) {
	if err := m.beginApply(); err != nil {
		return m.Status(), err
	}
	defer m.operation.Unlock()
	err := m.apply(ctx)
	return m.Status(), err
}

func (m *Manager) StartApply(ctx context.Context) (Status, error) {
	if err := m.beginApply(); err != nil {
		return m.Status(), err
	}
	status := m.Status()
	go func() {
		defer m.operation.Unlock()
		_ = m.apply(context.WithoutCancel(ctx))
	}()
	return status, nil
}

func (m *Manager) beginApply() error {
	if m.options.Safe() {
		return ErrSafeMode
	}
	if m.configErr != nil {
		return m.configErr
	}
	if err := m.begin(); err != nil {
		return err
	}
	status := m.Status()
	if status.CheckedAt != "" {
		if err := canApply(status); err != nil {
			m.operation.Unlock()
			return err
		}
	}
	m.change(func(s *Status) {
		s.Error, s.DownloadedBytes, s.TotalBytes = "", 0, 0
		s.State = Downloading
		if s.CheckedAt == "" {
			s.State = Checking
		}
	})
	return nil
}

func canApply(status Status) error {
	switch {
	case !status.UpdateAvailable && status.Error != "":
		return errors.New(status.Error)
	case !status.UpdateAvailable && status.Note != "":
		return fmt.Errorf("%w：%s", ErrUnavailable, status.Note)
	case !status.UpdateAvailable:
		return ErrUnavailable
	case !status.CanApply:
		return errors.New(status.CannotApplyReason)
	default:
		return nil
	}
}

func (m *Manager) apply(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			m.change(func(s *Status) { s.State, s.Error = Failed, err.Error() })
		}
	}()
	status := m.Status()
	if status.CheckedAt == "" {
		status, err = m.check(ctx)
		if err != nil {
			return err
		}
		if err = canApply(status); err != nil {
			return err
		}
	}
	if m.options.Safe() {
		return ErrSafeMode
	}
	if reason := m.executableBlocker(); reason != "" {
		m.change(func(s *Status) { s.CanApply, s.CannotApplyReason = false, reason })
		return errors.New(reason)
	}
	m.change(func(s *Status) { s.State, s.TotalBytes = Downloading, max(0, m.archive.Size) })
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	downloads := filepath.Join(m.options.DataDir, "updates", "downloads")
	if err = os.MkdirAll(downloads, 0o700); err != nil {
		return fmt.Errorf("无法创建更新下载目录：%w", err)
	}
	stage, err := os.MkdirTemp(downloads, "update-*")
	if err != nil {
		return fmt.Errorf("无法准备更新下载目录：%w", err)
	}
	handedOff := false
	defer func() {
		if !handedOff {
			_ = os.RemoveAll(stage)
		}
	}()
	archivePath, err := m.downloadWithFallback(ctx, stage, status.LatestVersion)
	if err != nil {
		return err
	}
	files, err := extractArchive(archivePath, stage, m.options.GOOS, m.options.GOARCH)
	if err != nil {
		return fmt.Errorf("无法解压或验证安装包：%w", err)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if m.options.Safe() {
		return ErrSafeMode
	}
	replace := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return m.prepareSupervisor(stage, status.LatestVersion, files)
	}
	if m.options.Dispatch != nil {
		err = m.options.Dispatch(replace)
	} else {
		err = replace()
	}
	if errors.Is(err, ErrSafeMode) {
		return err
	}
	if err != nil {
		return fmt.Errorf("无法替换核心程序：%w", err)
	}
	handedOff = true
	m.change(func(s *Status) { s.State = Restarting })
	m.options.RequestRestart()
	return nil
}

func (m *Manager) downloadCandidates(latestVersion string) []downloadCandidate {
	candidates := []downloadCandidate{{archive: m.archive, checksums: m.checksums}}
	if !m.manifest || m.archive.Name == "" || m.checksums.Name == "" {
		return candidates
	}
	tag := "v" + strings.TrimPrefix(latestVersion, "v")
	for _, base := range m.releaseSources {
		archiveURL := base + "/download/" + url.PathEscape(tag) + "/" + url.PathEscape(m.archive.Name)
		checksumURL := base + "/download/" + url.PathEscape(tag) + "/" + url.PathEscape(m.checksums.Name)
		if archiveURL == m.archive.URL && checksumURL == m.checksums.URL {
			continue
		}
		candidates = append(candidates, downloadCandidate{
			archive:   asset{Name: m.archive.Name, URL: archiveURL, Size: m.archive.Size, signedSHA256: m.archive.signedSHA256},
			checksums: asset{Name: m.checksums.Name, URL: checksumURL},
		})
	}
	return candidates
}

func (m *Manager) downloadWithFallback(ctx context.Context, stage, latestVersion string) (string, error) {
	candidates := m.downloadCandidates(latestVersion)
	var lastErr error
	for idx, candidate := range candidates {
		m.change(func(s *Status) {
			s.State = Downloading
			s.DownloadedBytes = 0
			s.TotalBytes = max(0, candidate.archive.Size)
		})
		checksum, err := m.downloadChecksum(ctx, candidate)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				break
			}
			continue
		}
		attemptCtx := ctx
		attemptCancel := func() {}
		if len(candidates) > 1 && idx < len(candidates)-1 {
			attemptCtx, attemptCancel = context.WithTimeout(ctx, 2*time.Minute)
		}
		attemptPath := filepath.Join(stage, fmt.Sprintf("release-%d.archive", idx))
		err = m.downloadArchive(attemptCtx, candidate, attemptPath, checksum)
		attemptCancel()
		if err == nil {
			return attemptPath, nil
		}
		_ = os.Remove(attemptPath)
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return "", lastErr
}

func (m *Manager) downloadChecksum(ctx context.Context, candidate downloadCandidate) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := m.get(ctx, candidate.checksums.URL, "application/octet-stream")
	if err != nil {
		return "", fmt.Errorf("无法下载 SHA256SUMS：%w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载 SHA256SUMS 失败，HTTP %d", res.StatusCode)
	}
	data, err := readBounded(res.Body, maxSumsBytes)
	if err != nil {
		return "", fmt.Errorf("无法读取 SHA256SUMS：%w", err)
	}
	var digest string
	lines := bufio.NewScanner(bytes.NewReader(data))
	lines.Buffer(make([]byte, 1024), maxSumsBytes+1)
	for lines.Scan() {
		entry := checksumPattern.FindStringSubmatch(lines.Text())
		if entry != nil && entry[2] == candidate.archive.Name {
			if digest != "" {
				return "", errors.New("SHA256SUMS 中存在重复的安装包校验值")
			}
			digest = entry[1]
		}
	}
	if err = lines.Err(); err != nil {
		return "", err
	}
	if digest == "" {
		return "", errors.New("SHA256SUMS 中缺少当前安装包的有效 SHA-256 校验值")
	}
	if candidate.archive.signedSHA256 != "" && digest != candidate.archive.signedSHA256 {
		return "", errors.New("SHA256SUMS 与签名发布清单的安装包校验值不一致")
	}
	return digest, nil
}

func (m *Manager) downloadArchive(ctx context.Context, candidate downloadCandidate, destination, checksum string) error {
	if candidate.archive.Size > maxArchiveBytes {
		return errors.New("安装包超过 128 MiB 下载上限")
	}
	res, err := m.get(ctx, candidate.archive.URL, "application/octet-stream")
	if err != nil {
		return fmt.Errorf("无法下载安装包：%w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("下载安装包失败，HTTP %d", res.StatusCode)
	}
	if res.ContentLength > maxArchiveBytes {
		return errors.New("安装包超过 128 MiB 下载上限")
	}
	if res.ContentLength > 0 {
		m.change(func(s *Status) { s.TotalBytes = res.ContentLength })
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	digest := sha256.New()
	writer := io.MultiWriter(file, digest, progressWriter(func(count int) {
		m.change(func(s *Status) { s.DownloadedBytes += int64(count) })
	}))
	count, copyErr := io.Copy(writer, io.LimitReader(res.Body, maxArchiveBytes+1))
	if err = errors.Join(copyErr, file.Close()); err != nil {
		return fmt.Errorf("无法保存安装包：%w", err)
	}
	if count > maxArchiveBytes {
		return errors.New("安装包超过 128 MiB 下载上限")
	}
	m.change(func(s *Status) { s.State, s.TotalBytes = Verifying, count })
	if actual := hex.EncodeToString(digest.Sum(nil)); actual != checksum {
		return errors.New("安装包 SHA-256 校验失败，已丢弃下载文件")
	}
	return nil
}

type progressWriter func(int)

func (progress progressWriter) Write(buffer []byte) (int, error) {
	progress(len(buffer))
	return len(buffer), nil
}

func writeCopy(file *os.File, source string, mode os.FileMode) error {
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
	return errors.Join(err, file.Close())
}

func atomicCopy(source, destination string, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(destination), ".enhanced-kimi.copy.*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = writeCopy(file, source, mode); err != nil {
		return err
	}
	// Windows refuses a rename while another process holds the target open, so a
	// rollback retries instead of failing on a transient sharing violation.
	return platform.RetryFileOperation(func() error { return os.Rename(file.Name(), destination) })
}
