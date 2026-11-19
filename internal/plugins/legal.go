package plugins

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"enhancedkimi/internal/httpx"
)

const legalFileLimit = 128 << 10

var noticeNames = []string{"NOTICE", "NOTICE.md"}

type PluginLegal struct {
	ID               string `json:"id"`
	License          string `json:"license"`
	LicenseText      string `json:"licenseText"`
	LicenseTruncated bool   `json:"licenseTruncated"`
	NoticeFile       string `json:"noticeFile"`
	NoticeText       string `json:"noticeText"`
	NoticeTruncated  bool   `json:"noticeTruncated"`
	UpstreamRepo     string `json:"upstreamRepo"`
	UpstreamURL      string `json:"upstreamUrl"`
	UpstreamCommit   string `json:"upstreamCommit"`
	UpstreamLicense  string `json:"upstreamLicense"`
}

func (m *Manager) handleLegal(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	m.mu.Lock()
	item, ok := m.entries[id]
	var dir string
	var manifest Manifest
	if ok {
		dir, manifest = item.dir, item.manifest
	}
	m.mu.Unlock()
	if !ok {
		httpx.Fail(w, http.StatusNotFound, "PLUGIN_NOT_FOUND", "插件未安装")
		return
	}
	upstream := manifest.upstreamMetadata()
	legal := PluginLegal{
		ID: id, License: manifest.License,
		UpstreamRepo: upstream.UpstreamRepo, UpstreamURL: upstream.UpstreamURL,
		UpstreamCommit: upstream.UpstreamCommit, UpstreamLicense: upstream.UpstreamLicense,
	}
	if root, err := openPluginRoot(dir); err == nil {
		defer root.Close()
		legal.LicenseText, legal.LicenseTruncated, _ = readLegalFile(root, "LICENSE")
		for _, name := range noticeNames {
			text, truncated, err := readLegalFile(root, name)
			if err == nil {
				legal.NoticeFile, legal.NoticeText, legal.NoticeTruncated = name, text, truncated
				break
			}
		}
	}
	httpx.JSON(w, http.StatusOK, legal)
}

func hasNotice(dir string) bool {
	root, err := openPluginRoot(dir)
	if err != nil {
		return false
	}
	defer root.Close()
	for _, name := range noticeNames {
		if info, err := root.Lstat(name); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

func readLegalFile(root *os.Root, name string) (string, bool, error) {
	file, _, err := openPluginFile(root, name)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, legalFileLimit+1))
	if err != nil {
		return "", false, err
	}
	truncated := len(data) > legalFileLimit
	return strings.ToValidUTF8(string(data[:min(len(data), legalFileLimit)]), ""), truncated, nil
}

func openPluginRoot(dir string) (*os.Root, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fs.ErrInvalid
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(info, actual) {
		root.Close()
		return nil, fs.ErrInvalid
	}
	return root, nil
}

func openPluginFile(root *os.Root, name string) (*os.File, os.FileInfo, error) {
	if name == "." || strings.Contains(name, "\\") {
		return nil, nil, fs.ErrInvalid
	}
	local, err := filepath.Localize(name)
	if err != nil {
		return nil, nil, err
	}
	for parent := filepath.Dir(local); parent != "."; parent = filepath.Dir(parent) {
		info, err := root.Lstat(parent)
		if err != nil || !info.IsDir() {
			return nil, nil, fs.ErrInvalid
		}
	}
	info, err := root.Lstat(local)
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, fs.ErrInvalid
	}
	file, err := root.Open(local)
	if err != nil {
		return nil, nil, err
	}
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) {
		file.Close()
		return nil, nil, fs.ErrInvalid
	}
	return file, actual, nil
}
