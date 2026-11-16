package plugins

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"enhancedkimi/internal/redact"
)

const (
	// unpackFileLimit and unpackTotalLimit bound a zip before it is extracted. A
	// plugin is code plus assets, not a data set; anything larger is refused
	// instead of filling the user's disk.
	unpackFileLimit  = 4 << 20
	unpackTotalLimit = 16 << 20
	unpackEntryLimit = 64
	// zipLimit bounds the upload itself.
	zipLimit = 24 << 20
)

// InstallRequest is what the plugin page sends. Root is a folder or a zip on
// disk that the user picked; the core never downloads anything.
//
// Capabilities is the set the user confirmed. It is a pointer so the core can
// tell "the user confirmed exactly these capabilities" from "the user confirmed
// nothing", which would otherwise be indistinguishable from an omitted field and
// would silently grant everything the manifest asks for.
type InstallRequest struct {
	Root         string    `json:"root"`
	Capabilities *[]string `json:"capabilities"`
}

// PackagePreview is what the plugin page shows before asking for consent. It
// validates the package and reports the declared capabilities without installing
// anything, so a user decides from what the plugin actually asked for.
type PackagePreview struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Description  string   `json:"description,omitempty"`
	Author       string   `json:"author,omitempty"`
	License      string   `json:"license,omitempty"`
	HostVersions []string `json:"hostVersions"`
	Capabilities []string `json:"capabilities"`
	Compatible   bool     `json:"compatible"`
	Installed    bool     `json:"installed"`
	Source       string   `json:"source"`
	Upstream     string   `json:"upstream,omitempty"`
}

// Preview validates a package exactly as Install would, but writes nothing.
func (m *Manager) Preview(ctx context.Context, root string) (PackagePreview, error) {
	manifest, data, err := m.inspectPackage(root)
	if err != nil {
		return PackagePreview{}, err
	}
	_ = data
	_, installed := m.Get(manifest.ID)
	return PackagePreview{
		ID:           manifest.ID,
		Name:         manifest.Name,
		Version:      manifest.Version,
		Description:  manifest.Description,
		Author:       manifest.Author,
		License:      manifest.License,
		HostVersions: manifest.HostVersions,
		Capabilities: manifest.CapabilityNames(),
		Compatible:   m.compatible(manifest),
		Installed:    installed,
		Source:       SourceLocal,
		Upstream:     upstreamLabel(manifest),
	}, nil
}

// inspectPackage copies a package into a staging directory and validates it,
// returning the parsed manifest and the raw manifest bytes. The staging directory
// is removed before returning, so a preview leaves no trace.
func (m *Manager) inspectPackage(root string) (Manifest, []byte, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return Manifest{}, nil, apiError(http.StatusBadRequest, "PLUGIN_SOURCE_REQUIRED", "请选择插件文件夹或 zip")
	}
	info, err := os.Stat(root)
	if err != nil {
		return Manifest{}, nil, apiError(http.StatusBadRequest, "PLUGIN_SOURCE_MISSING", "插件来源不存在或无法读取")
	}
	parent := m.dataDir
	if parent == "" {
		parent = os.TempDir()
	}
	staging, err := os.MkdirTemp(parent, ".plugin-inspect-*")
	if err != nil {
		return Manifest{}, nil, apiError(http.StatusInternalServerError, "PLUGIN_STORAGE_UNAVAILABLE", "无法准备检查目录")
	}
	defer os.RemoveAll(staging)
	extracted := filepath.Join(staging, "plugin")
	if info.IsDir() {
		err = copyTree(root, extracted)
	} else {
		err = extractZip(root, extracted)
	}
	if err != nil {
		var pluginErr *Error
		if errors.As(err, &pluginErr) {
			return Manifest{}, nil, pluginErr
		}
		return Manifest{}, nil, apiError(http.StatusBadRequest, "PLUGIN_PACKAGE_INVALID", redact.Text(err.Error()))
	}
	data, err := os.ReadFile(filepath.Join(extracted, "plugin.json"))
	if err != nil {
		return Manifest{}, nil, apiError(http.StatusBadRequest, "PLUGIN_MANIFEST_MISSING", "插件包缺少 plugin.json")
	}
	manifest, err := ParseManifest(data)
	if err != nil {
		return Manifest{}, nil, apiError(http.StatusBadRequest, "PLUGIN_MANIFEST_INVALID", redact.Text(err.Error()))
	}
	if err = manifest.ValidateFiles(extracted); err != nil {
		return Manifest{}, nil, apiError(http.StatusBadRequest, "PLUGIN_FILES_INVALID", redact.Text(err.Error()))
	}
	return manifest, data, nil
}

// Install copies a plugin folder or zip into <dataDir>/plugins/<id>, validates
// the copy, and stores the granted capabilities. The capabilities are the set the
// user confirmed after Preview; a request without them is refused rather than
// defaulted, because defaulting would grant whatever the manifest asked for
// without the user ever seeing it.
func (m *Manager) Install(ctx context.Context, request InstallRequest) (Plugin, error) {
	if m.safe() {
		return Plugin{}, apiError(http.StatusConflict, "SAFE_MODE", "安全模式不允许安装插件")
	}
	if request.Capabilities == nil {
		return Plugin{}, apiError(http.StatusBadRequest, "PLUGIN_CONSENT_REQUIRED", "安装前需要先查看并确认插件申请的能力")
	}
	if m.pluginsDir() == "" {
		return Plugin{}, apiError(http.StatusConflict, "PLUGIN_STORAGE_UNAVAILABLE", "没有可用的数据目录，无法安装插件")
	}
	manifest, data, err := m.inspectPackage(request.Root)
	if err != nil {
		return Plugin{}, err
	}
	grants, err := m.resolveGrants(manifest, *request.Capabilities)
	if err != nil {
		return Plugin{}, err
	}
	// The validated copy is re-created here because inspectPackage removed its
	// staging directory; installing from the same source keeps one validation
	// rule instead of two slightly different copies of it.
	source, err := os.Stat(strings.TrimSpace(request.Root))
	if err != nil {
		return Plugin{}, apiError(http.StatusBadRequest, "PLUGIN_SOURCE_MISSING", "插件来源不存在或无法读取")
	}
	staging, err := os.MkdirTemp(m.dataDir, ".plugin-install-*")
	if err != nil {
		return Plugin{}, apiError(http.StatusInternalServerError, "PLUGIN_STORAGE_UNAVAILABLE", "无法准备安装目录")
	}
	defer os.RemoveAll(staging)
	extracted := filepath.Join(staging, "plugin")
	if source.IsDir() {
		err = copyTree(strings.TrimSpace(request.Root), extracted)
	} else {
		err = extractZip(strings.TrimSpace(request.Root), extracted)
	}
	if err != nil {
		var pluginErr *Error
		if errors.As(err, &pluginErr) {
			return Plugin{}, pluginErr
		}
		return Plugin{}, apiError(http.StatusBadRequest, "PLUGIN_PACKAGE_INVALID", redact.Text(err.Error()))
	}
	if err = manifest.ValidateFiles(extracted); err != nil {
		return Plugin{}, apiError(http.StatusBadRequest, "PLUGIN_FILES_INVALID", redact.Text(err.Error()))
	}

	target := filepath.Join(m.pluginsDir(), manifest.DataDirName())
	if err = os.MkdirAll(m.pluginsDir(), 0o700); err != nil {
		return Plugin{}, apiError(http.StatusInternalServerError, "PLUGIN_STORAGE_UNAVAILABLE", "无法创建插件目录")
	}
	// Replacing a running plugin must stop it first: the old files are about to
	// disappear from under its renderer.
	if m.enabled(manifest.ID) {
		_ = m.Disable(ctx, manifest.ID)
	}
	backup := ""
	if _, err = os.Stat(target); err == nil {
		backup = target + ".previous"
		_ = os.RemoveAll(backup)
		if err = os.Rename(target, backup); err != nil {
			return Plugin{}, apiError(http.StatusInternalServerError, "PLUGIN_INSTALL_FAILED", "无法替换现有插件文件")
		}
	}
	if err = os.Rename(extracted, target); err != nil {
		if backup != "" {
			_ = os.Rename(backup, target)
		}
		return Plugin{}, apiError(http.StatusInternalServerError, "PLUGIN_INSTALL_FAILED", "无法写入插件文件，原插件已保留")
	}
	if backup != "" {
		_ = os.RemoveAll(backup)
	}

	now := m.now()
	item := &entry{
		manifest: manifest, grants: grants, declared: manifest.CapabilityNames(),
		source: SourceLocal, dir: target, state: StateDisabled, updated: now, installed: now,
	}
	stored := record{
		id: manifest.ID, name: manifest.Name, version: manifest.Version,
		manifest: data, capabilities: grants.Names(), source: SourceLocal,
		state: StateDisabled, installedAt: now, updatedAt: now,
	}
	if err = saveRecord(ctx, m.db, stored); err != nil {
		return Plugin{}, apiError(http.StatusInternalServerError, "PLUGIN_STATE_UNAVAILABLE", "插件已复制，但状态未能保存")
	}
	m.mu.Lock()
	m.entries[manifest.ID] = item
	if !contains(m.order, manifest.ID) {
		m.order = append(m.order, manifest.ID)
		sortStrings(m.order)
	}
	plugin := m.presentLocked(item)
	m.mu.Unlock()
	_ = recordActivity(ctx, m.db, manifest.ID, ActivityInstall, "已从本地安装 "+manifest.Name+" "+manifest.Version, now)
	return plugin, nil
}

func sortStrings(list []string) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j] < list[j-1]; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

// resolveGrants narrows the manifest declaration to what the user accepted. A
// capability the user did not confirm is not silently granted, and a capability
// that is not declared at all cannot be granted.
func (m *Manager) resolveGrants(manifest Manifest, accepted []string) (Grants, error) {
	declared, err := ParseCapabilities(manifest.Capabilities)
	if err != nil {
		return Grants{}, apiError(http.StatusBadRequest, "PLUGIN_MANIFEST_INVALID", redact.Text(err.Error()))
	}
	// An explicitly empty list is a real decision: the user installed the plugin
	// but granted nothing, so it can mount only what needs no capability.
	granted, err := ParseCapabilities(accepted)
	if err != nil {
		return Grants{}, apiError(http.StatusBadRequest, "PLUGIN_CAPABILITY_INVALID", redact.Text(err.Error()))
	}
	for _, name := range granted.Names() {
		if !contains(declared.Names(), name) {
			return Grants{}, apiError(http.StatusBadRequest, "PLUGIN_CAPABILITY_UNDECLARED", "插件清单没有声明 "+name+"，不能授予")
		}
	}
	return granted, nil
}

// copyTree copies a plugin folder. Symbolic links are refused: a link would let
// the package point outside its own directory after installation.
func copyTree(source, target string) error {
	var total int64
	return filepath.WalkDir(source, func(current string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		if relative == "." {
			return os.MkdirAll(target, 0o700)
		}
		if strings.HasPrefix(entry.Name(), ".") && entry.IsDir() {
			return filepath.SkipDir
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("插件包不能包含符号链接：%s", relative)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("插件包包含非普通文件：%s", relative)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > unpackFileLimit {
			return fmt.Errorf("文件 %s 超过 %d 字节", relative, unpackFileLimit)
		}
		total += info.Size()
		if total > unpackTotalLimit {
			return fmt.Errorf("插件包超过 %d 字节", unpackTotalLimit)
		}
		return copyFile(current, destination)
	})
}

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, io.LimitReader(in, unpackFileLimit)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// extractZip unpacks a plugin zip with the usual archive defences: no absolute
// paths, no parent traversal, no symlink entries, bounded sizes.
func extractZip(source, target string) error {
	reader, err := zip.OpenReader(source)
	if err != nil {
		return errors.New("无法读取 zip：文件损坏或不是 zip 格式")
	}
	defer reader.Close()
	if len(reader.File) > unpackEntryLimit {
		return fmt.Errorf("zip 含 %d 个条目，上限 %d", len(reader.File), unpackEntryLimit)
	}
	if err = os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	var total int64
	for _, file := range reader.File {
		name := strings.ReplaceAll(file.Name, "\\", "/")
		cleaned := path.Clean(name)
		if cleaned == "." || strings.HasPrefix(cleaned, "..") || path.IsAbs(cleaned) || strings.Contains(cleaned, "..") {
			return fmt.Errorf("zip 条目路径越界：%s", file.Name)
		}
		if file.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("zip 不能包含符号链接：%s", file.Name)
		}
		destination := filepath.Join(target, filepath.FromSlash(cleaned))
		if file.FileInfo().IsDir() {
			if err = os.MkdirAll(destination, 0o700); err != nil {
				return err
			}
			continue
		}
		if file.UncompressedSize64 > unpackFileLimit {
			return fmt.Errorf("文件 %s 超过 %d 字节", cleaned, unpackFileLimit)
		}
		total += int64(file.UncompressedSize64)
		if total > unpackTotalLimit {
			return fmt.Errorf("zip 解压后超过 %d 字节", unpackTotalLimit)
		}
		if err = os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		if err = writeZipEntry(file, destination); err != nil {
			return err
		}
	}
	return nil
}

func writeZipEntry(file *zip.File, target string) error {
	source, err := file.Open()
	if err != nil {
		return err
	}
	defer source.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, io.LimitReader(source, unpackFileLimit)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ReleaseBundled installs or upgrades the plugins embedded in the binary. Three
// rules make it safe to run on every start:
//
//   - a tombstone wins: a bundled plugin the user uninstalled is never released;
//   - a newer embedded version replaces an older bundled install, keeping the
//     granted capabilities and the private storage, and restoring the old files
//     if the replacement fails;
//   - an existing install at the same or a newer version is left alone, so a
//     user's edits are not overwritten on every launch.
func (m *Manager) ReleaseBundled(ctx context.Context, bundled map[string]map[string][]byte) error {
	if m.pluginsDir() == "" {
		return nil
	}
	tombstones, err := m.tombstones(ctx)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(m.pluginsDir(), 0o700); err != nil {
		return err
	}
	for id, files := range bundled {
		if tombstones[id] {
			continue
		}
		manifest, err := bundledManifest(id, files)
		if err != nil {
			return err
		}
		target := filepath.Join(m.pluginsDir(), id)
		targetExists := false
		if _, statErr := os.Stat(target); statErr == nil {
			targetExists = true
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("inspect bundled plugin %s: %w", id, statErr)
		}
		if targetExists {
			existing, versionErr := readInstalledVersion(target)
			if versionErr != nil {
				// A directory the core cannot read is the user's to inspect, not
				// ours to overwrite: it is marked corrupt and left in place.
				if err = m.adopt(ctx, id, target, SourceBundled); err != nil {
					return err
				}
				continue
			}
			if !newerVersion(manifest.Version, existing) {
				if err = m.adopt(ctx, id, target, SourceBundled); err != nil {
					return err
				}
				continue
			}
			if err = m.upgradeBundled(ctx, id, target, manifest, files); err != nil {
				// The upgrade already restored the previous files; the old version
				// keeps running and the failure is reported, not propagated as a
				// reason to abandon every other plugin.
				_ = m.markUpgradeFailure(ctx, id, err)
			}
			continue
		}
		if err = writeBundle(target, files); err != nil {
			_ = os.RemoveAll(target)
			return fmt.Errorf("release bundled plugin %s: %w", id, err)
		}
		if err = m.adopt(ctx, id, target, SourceBundled); err != nil {
			return err
		}
	}
	return nil
}

func bundledManifest(id string, files map[string][]byte) (Manifest, error) {
	data, ok := files["plugin.json"]
	if !ok {
		return Manifest{}, fmt.Errorf("bundled plugin %s has no plugin.json", id)
	}
	manifest, err := ParseManifest(data)
	if err != nil {
		return Manifest{}, fmt.Errorf("bundled plugin %s: %w", id, err)
	}
	return manifest, nil
}

// readInstalledVersion reports the version a directory currently holds. A missing
// manifest is reported as an error so the caller falls through to a fresh write.
func readInstalledVersion(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return "", err
	}
	var header struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil || header.Version == "" {
		return "", errors.New("installed manifest is unreadable")
	}
	return header.Version, nil
}

// newerVersion reports whether candidate is strictly newer than current. The
// comparison is numeric per component, so 1.0.10 is newer than 1.0.9.
func newerVersion(candidate, current string) bool {
	left := strings.Split(candidate, ".")
	right := strings.Split(current, ".")
	for index := 0; index < len(left) || index < len(right); index++ {
		var a, b int
		if index < len(left) {
			a, _ = strconv.Atoi(left[index])
		}
		if index < len(right) {
			b, _ = strconv.Atoi(right[index])
		}
		if a != b {
			return a > b
		}
	}
	return false
}

// upgradeBundled swaps in a new bundled version while preserving the grant and
// the storage. Files are staged next to the target and swapped by rename, so a
// failure leaves the previous version intact.
func (m *Manager) upgradeBundled(ctx context.Context, id, target string, manifest Manifest, files map[string][]byte) error {
	staging := target + ".upgrade"
	previous := target + ".previous"
	_ = os.RemoveAll(staging)
	_ = os.RemoveAll(previous)
	if err := writeBundle(staging, files); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	if err := manifest.ValidateFiles(staging); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	m.mu.Lock()
	live := m.entries[id]
	wasLive := live != nil && live.state == StateEnabled
	// The enable decision is what has to survive the swap. A running plugin is
	// always a desired one, but a desired plugin that was blocked by a transient
	// failure must come back too, so both are read before anything is stopped.
	wanted := live != nil && live.enabled
	m.mu.Unlock()
	if wasLive {
		if err := m.Disable(ctx, id); err != nil {
			_ = os.RemoveAll(staging)
			return err
		}
	}
	if err := os.Rename(target, previous); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	if err := os.Rename(staging, target); err != nil {
		_ = os.Rename(previous, target)
		_ = os.RemoveAll(staging)
		return err
	}
	_ = os.RemoveAll(previous)
	if err := m.adopt(ctx, id, target, SourceBundled); err != nil {
		return err
	}
	_ = recordActivity(ctx, m.db, id, ActivityUpgrade, "预装插件已升级到 "+manifest.Version, m.now())
	if wanted {
		// The upgrade replaced the files, not the user's decision. Grants were
		// carried over by adopt, so the plugin goes back to the state it had.
		if _, err := m.enable(ctx, id, true); err != nil {
			_ = m.markUpgradeFailure(ctx, id, err)
		}
	}
	return nil
}

func (m *Manager) markUpgradeFailure(ctx context.Context, id string, err error) error {
	message := "预装插件升级失败，保持原版本：" + redact.Text(err.Error())
	m.mu.Lock()
	if item := m.entries[id]; item != nil {
		item.errText = message
		item.updated = m.now()
	}
	m.mu.Unlock()
	return recordActivity(ctx, m.db, id, ActivityError, message, m.now())
}

// RestoreBundled clears the tombstone of a plugin the user uninstalled and
// releases the embedded copy again. It is the explicit path behind "恢复预装插件".
func (m *Manager) RestoreBundled(ctx context.Context, id string, files map[string][]byte) (Plugin, error) {
	if m.pluginsDir() == "" {
		return Plugin{}, apiError(http.StatusConflict, "PLUGIN_STORAGE_UNAVAILABLE", "没有可用的数据目录")
	}
	manifest, err := bundledManifest(id, files)
	if err != nil {
		return Plugin{}, apiError(http.StatusNotFound, "PLUGIN_NOT_BUNDLED", "没有这个预装插件")
	}
	if _, live := m.Get(manifest.ID); live {
		return Plugin{}, apiError(http.StatusConflict, "PLUGIN_ALREADY_INSTALLED", "插件已经安装")
	}
	target := filepath.Join(m.pluginsDir(), id)
	if err := os.RemoveAll(target); err != nil {
		return Plugin{}, apiError(http.StatusInternalServerError, "PLUGIN_RESTORE_FAILED", "无法清理旧文件")
	}
	if err := writeBundle(target, files); err != nil {
		_ = os.RemoveAll(target)
		return Plugin{}, apiError(http.StatusInternalServerError, "PLUGIN_RESTORE_FAILED", "无法写入预装插件文件")
	}
	if err := manifest.ValidateFiles(target); err != nil {
		_ = os.RemoveAll(target)
		return Plugin{}, apiError(http.StatusConflict, "PLUGIN_FILES_INVALID", redact.Text(err.Error()))
	}
	// The tombstone is cleared only after the files are valid again, so a failed
	// restore leaves the uninstalled decision exactly as the user left it.
	if err := deleteRecord(ctx, m.db, id); err != nil {
		_ = os.RemoveAll(target)
		return Plugin{}, apiError(http.StatusInternalServerError, "PLUGIN_RESTORE_FAILED", "无法清除卸载记录")
	}
	if err := m.adopt(ctx, id, target, SourceBundled); err != nil {
		return Plugin{}, err
	}
	_ = recordActivity(ctx, m.db, id, ActivityRestore, "已恢复预装插件", m.now())
	return m.Get2(id), nil
}

// adopt records a plugin directory the core found on disk. A file that fails
// validation is kept and marked corrupt rather than deleted, because deleting a
// user's files to tidy a scan is not the core's decision to make.
func (m *Manager) adopt(ctx context.Context, id, dir, source string) error {
	data, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return m.markCorrupt(ctx, id, dir, source, "插件目录缺少 plugin.json")
	}
	manifest, err := ParseManifest(data)
	if err != nil {
		return m.markCorrupt(ctx, id, dir, source, err.Error())
	}
	if manifest.ID != id {
		return m.markCorrupt(ctx, id, dir, source, "目录名与清单 id 不一致")
	}
	if err = manifest.ValidateFiles(dir); err != nil {
		return m.markCorrupt(ctx, id, dir, source, err.Error())
	}
	// The persisted install is the authority on what the user granted and asked
	// for. Rebuilding either from the manifest would re-widen a grant the user
	// narrowed and drop an enable decision every time a bundled copy is released.
	persisted, persistedErr := m.persistedInstall(ctx, id)
	if persistedErr != nil {
		return persistedErr
	}
	grants, err := ParseCapabilities(manifest.Capabilities)
	if err != nil {
		return m.markCorrupt(ctx, id, dir, source, err.Error())
	}
	desired := false
	if persisted != nil {
		if stored, storedErr := ParseCapabilities(persisted.capabilities); storedErr == nil {
			grants = stored
		}
		desired = persisted.desired
	}

	m.mu.Lock()
	existing := m.entries[id]
	if existing != nil && existing.state == StateEnabled {
		// A rescan must not disturb a running plugin.
		m.mu.Unlock()
		return nil
	}
	now := m.now()
	item := &entry{
		manifest: manifest, grants: grants, declared: manifest.CapabilityNames(),
		source: source, dir: dir, state: StateDisabled, enabled: desired,
		forced: desired && !m.compatible(manifest),
		updated: now, installed: now,
	}
	if existing != nil {
		item.installed = existing.installed
		if existing.state != StateDisabled {
			item.state = existing.state
			item.errText = existing.errText
		}
	}
	// A plugin the user enabled goes back to running after a rescan, because that
	// is what the persisted record says and nothing about the rescan changed it.
	if desired {
		item.state = StateEnabled
		item.errText = ""
	}
	m.entries[id] = item
	if !contains(m.order, id) {
		m.order = append(m.order, id)
		sortStrings(m.order)
	}
	m.mu.Unlock()

	stored := record{
		id: manifest.ID, name: manifest.Name, version: manifest.Version,
		manifest: data, capabilities: grants.Names(), source: source,
		desired: item.state == StateEnabled, state: item.state, err: item.errText,
		installedAt: item.installed, updatedAt: now,
	}
	return saveRecord(ctx, m.db, stored)
}

// persistedInstall reads the record for one plugin. A missing record is not an
// error: it is what a first release of a bundled plugin looks like.
func (m *Manager) persistedInstall(ctx context.Context, id string) (*record, error) {
	records, err := loadRecords(ctx, m.db)
	if err != nil {
		return nil, apiError(http.StatusInternalServerError, "PLUGIN_STATE_UNAVAILABLE", "无法读取插件记录")
	}
	for index := range records {
		if records[index].id == id && !records[index].tombstone {
			return &records[index], nil
		}
	}
	return nil, nil
}

func (m *Manager) markCorrupt(ctx context.Context, id, dir, source, reason string) error {
	message := redact.Text(reason)
	// A corrupt directory is a file problem, not a permission decision. The
	// stored grant and the enable intent are carried over so fixing the files
	// restores what the user had agreed to, instead of silently widening it to
	// the manifest declaration on the next successful adopt.
	persisted, persistedErr := m.persistedInstall(ctx, id)
	desired := false
	capabilities := []string{}
	var grants Grants
	if persistedErr == nil && persisted != nil {
		desired = persisted.desired
		capabilities = persisted.capabilities
		if parsed, parseErr := ParseCapabilities(persisted.capabilities); parseErr == nil {
			grants = parsed
		}
	}
	if grants.fixed == nil {
		grants = Grants{fixed: make(map[string]bool)}
	}
	m.mu.Lock()
	now := m.now()
	item := m.entries[id]
	if item == nil {
		item = &entry{manifest: Manifest{ID: id, Name: id}, dir: dir, installed: now}
		m.entries[id] = item
		if !contains(m.order, id) {
			m.order = append(m.order, id)
			sortStrings(m.order)
		}
	}
	item.grants = grants
	item.state = StateCorrupt
	item.enabled = desired
	item.errText = message
	item.source = source
	item.updated = now
	delete(m.tokens, id)
	m.mu.Unlock()
	_ = saveRecord(ctx, m.db, record{
		id: id, name: id, version: "0.0.0", manifest: mustJSON(map[string]any{"id": id}), capabilities: capabilities,
		source: source, desired: desired, state: StateCorrupt, err: message, installedAt: now, updatedAt: now,
	})
	_ = recordActivity(ctx, m.db, id, ActivityCorrupt, "插件异常已停用："+message, now)
	return nil
}

func writeBundle(target string, files map[string][]byte) error {
	for name, data := range files {
		cleaned := path.Clean(strings.ReplaceAll(name, "\\", "/"))
		if strings.HasPrefix(cleaned, "..") || path.IsAbs(cleaned) {
			return fmt.Errorf("bundle entry escapes plugin directory: %s", name)
		}
		destination := filepath.Join(target, filepath.FromSlash(cleaned))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(destination, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) tombstones(ctx context.Context) (map[string]bool, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT id FROM plugin_installs WHERE tombstone=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
