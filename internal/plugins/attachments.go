package plugins

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type stagedBatch struct {
	token   string
	files   map[string]os.FileInfo
	expires time.Time
	claimed bool
}

func parseAttachmentPaths(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_ATTACHMENT_PATH", "附件路径必须是字符串数组")
	}
	if len(list) > attachmentMax {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_ATTACHMENT_LIMIT", fmt.Sprintf("一次最多 %d 个附件", attachmentMax))
	}
	paths := make([]string, 0, len(list))
	for _, item := range list {
		path, ok := item.(string)
		if !ok || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return nil, apiError(http.StatusBadRequest, "PLUGIN_ATTACHMENT_PATH", "附件必须使用核心返回的暂存路径")
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func (m *Manager) validateStagedPaths(id, token string, paths []string) ([]string, error) {
	root, err := m.stageRoot(id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	denied := apiError(http.StatusForbidden, "PLUGIN_ATTACHMENT_NOT_STAGED", "附件不在当前插件的有效暂存批次中")
	if m.closed || m.tokens[id] != token {
		return nil, denied
	}
	for _, path := range paths {
		batchPath := filepath.Dir(path)
		batch := m.staged[batchPath]
		if filepath.Dir(batchPath) != root || batch.claimed || batch.token != token || !m.now().Before(batch.expires) || batch.files[path] == nil {
			return nil, denied
		}
		batchInfo, err := os.Lstat(batchPath)
		if err != nil || !batchInfo.IsDir() {
			return nil, denied
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || !os.SameFile(batch.files[path], info) {
			return nil, denied
		}
	}
	for _, path := range paths {
		batchPath := filepath.Dir(path)
		batch := m.staged[batchPath]
		batch.claimed = true
		m.staged[batchPath] = batch
	}
	return paths, nil
}

func (m *Manager) removeStagedBatch(path string, afterSend bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !afterSend && m.staged[path].claimed {
		return nil
	}
	delete(m.staged, path)
	return os.RemoveAll(path)
}

func (m *Manager) releaseSentPaths(paths []string) {
	for _, path := range paths {
		_ = m.removeStagedBatch(filepath.Dir(path), true)
	}
}

const (
	// A dropped file travels inside a JSON body as base64, so the sizes here are
	// chosen to stay under the route's body limit instead of being rejected by
	// the HTTP layer before the action ever sees them.
	attachmentFileLimit  = 8 << 20
	attachmentTotalLimit = 16 << 20
	attachmentStagedTTL  = 30 * time.Minute
)

// stageRoot is <dataDir>/staged/<id>, deliberately outside the plugin's install
// directory: the integrity check hashes that directory's contents, so a
// temporary file inside it would invalidate every privileged call, and the asset
// route would serve it. A manager with no data directory cannot stage at all,
// because a shared temporary directory would expose one plugin's dropped bytes
// to another.
func (m *Manager) stageRoot(id string) (string, error) {
	if m.dataDir == "" {
		return "", apiError(http.StatusServiceUnavailable, "PLUGIN_STORAGE_UNAVAILABLE", "插件数据目录不可用，无法暂存拖入的文件")
	}
	if _, ok := m.entry(id); !ok {
		return "", apiError(http.StatusNotFound, "PLUGIN_NOT_FOUND", "插件未安装")
	}
	data, err := filepath.Abs(m.dataDir)
	if err != nil {
		return "", err
	}
	parent := filepath.Join(data, "staged")
	root := filepath.Join(parent, id)
	for _, dir := range []string{parent, root} {
		info, err := os.Lstat(dir)
		if !os.IsNotExist(err) && (err != nil || !info.IsDir()) {
			return "", apiError(http.StatusServiceUnavailable, "PLUGIN_STORAGE_UNAVAILABLE", "插件暂存目录不可用")
		}
	}
	return root, nil
}

func (m *Manager) entry(id string) (*entry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.entries[id]
	return item, ok
}

// stageAttachments writes the dropped bytes and returns the paths plus the
// names they were given. Each call gets its own subdirectory so two concurrent
// sends cannot overwrite each other's files.
func (m *Manager) stageAttachments(ctx context.Context, id, token string, payload json.RawMessage) (any, error) {
	input, err := decodeObject(payload)
	if err != nil {
		return nil, err
	}
	rawList, _ := input["files"].([]any)
	if len(rawList) == 0 {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_ATTACHMENT_EMPTY", "没有选择附件")
	}
	if len(rawList) > attachmentMax {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_ATTACHMENT_LIMIT", fmt.Sprintf("一次最多 %d 个附件", attachmentMax))
	}
	root, err := m.stageRoot(id)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0o700); err != nil {
		return nil, apiError(http.StatusInternalServerError, "PLUGIN_STORAGE_UNAVAILABLE", "无法准备暂存目录")
	}
	// A plugin that stages and never sends must not grow the directory forever,
	// so every new stage also sweeps the ones nobody came back for.
	m.pruneStagedAttachments(id)
	batch := filepath.Join(root, newStageBatch())
	if err = os.MkdirAll(batch, 0o700); err != nil {
		return nil, apiError(http.StatusInternalServerError, "PLUGIN_STORAGE_UNAVAILABLE", "无法准备暂存目录")
	}
	// A failed staging leaves nothing behind for the plugin to reference.
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(batch)
		}
	}()
	staged := make([]map[string]any, 0, len(rawList))
	registered := make(map[string]os.FileInfo, len(rawList))
	total := int64(0)
	for index, value := range rawList {
		item, ok := value.(map[string]any)
		if !ok {
			return nil, apiError(http.StatusBadRequest, "PLUGIN_ATTACHMENT_INVALID", "附件必须是 {name, data} 对象")
		}
		name := safeAttachmentName(stringValue(item["name"]))
		if name == "" {
			return nil, apiError(http.StatusBadRequest, "PLUGIN_ATTACHMENT_NAME", "附件缺少可用文件名")
		}
		data, decodeErr := base64.StdEncoding.DecodeString(stringValue(item["data"]))
		if decodeErr != nil {
			return nil, apiError(http.StatusBadRequest, "PLUGIN_ATTACHMENT_INVALID", "附件内容不是合法的 base64")
		}
		if len(data) == 0 {
			return nil, apiError(http.StatusBadRequest, "PLUGIN_ATTACHMENT_EMPTY", "附件内容为空")
		}
		if int64(len(data)) > attachmentFileLimit {
			return nil, apiError(http.StatusRequestEntityTooLarge, "PLUGIN_ATTACHMENT_LIMIT", fmt.Sprintf("单个附件不超过 %d 字节", attachmentFileLimit))
		}
		total += int64(len(data))
		if total > attachmentTotalLimit {
			return nil, apiError(http.StatusRequestEntityTooLarge, "PLUGIN_ATTACHMENT_LIMIT", fmt.Sprintf("附件总量不超过 %d 字节", attachmentTotalLimit))
		}
		target := filepath.Join(batch, fmt.Sprintf("%02d-%s", index, name))
		if err = os.WriteFile(target, data, 0o600); err != nil {
			return nil, apiError(http.StatusInternalServerError, "PLUGIN_STORAGE_UNAVAILABLE", "无法写入暂存附件")
		}
		info, err := os.Lstat(target)
		if err != nil || !info.Mode().IsRegular() {
			return nil, apiError(http.StatusInternalServerError, "PLUGIN_STORAGE_UNAVAILABLE", "无法登记暂存附件")
		}
		registered[target] = info
		staged = append(staged, map[string]any{"path": target, "name": name, "size": len(data)})
	}
	m.mu.Lock()
	if !subtleEqual(m.tokens[id], token) || m.closed {
		m.mu.Unlock()
		return nil, apiError(http.StatusUnauthorized, "PLUGIN_IDENTITY_REJECTED", "插件身份令牌无效")
	}
	if m.staged == nil {
		m.staged = make(map[string]stagedBatch)
	}
	m.staged[batch] = stagedBatch{token: token, files: registered, expires: m.now().Add(attachmentStagedTTL)}
	m.mu.Unlock()
	cleanup = false
	_ = recordActivity(ctx, m.db, id, ActivitySession, "插件暂存了拖入的附件", m.now())
	return map[string]any{"files": staged, "batch": batch}, nil
}

// releaseStagedAttachments removes one staged batch. It is safe to call twice and
// safe to call for a batch that is already gone, because a send that failed
// still has to clean up.
//
// `stage` answers with the batch's full path, so a caller normally passes that
// back. A bare name is accepted too. Either way only a direct child of this
// plugin's staging root is removed, which is what keeps a caller-supplied value
// from deleting anything else.
func (m *Manager) releaseStagedAttachments(_ context.Context, id string, payload json.RawMessage) (any, error) {
	input, err := decodeObject(payload)
	if err != nil {
		return nil, err
	}
	root, err := m.stageRoot(id)
	if err != nil {
		return nil, err
	}
	batch := strings.TrimSpace(stringValue(input["batch"]))
	if batch == "" {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_ATTACHMENT_BATCH", "缺少暂存批次")
	}
	name := filepath.Base(filepath.Clean(batch))
	if name == "" || name == "." || name == ".." || name == string(filepath.Separator) || !looksLikeStageBatch(name) {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_ATTACHMENT_BATCH", "暂存批次无效")
	}
	// A path must already point into this plugin's own staging root; a bare name
	// is resolved under it. A path that names anything else is refused rather
	// than silently reduced to its base name, so a plugin cannot probe another
	// directory and believe the release succeeded.
	if batch != name && filepath.Dir(filepath.Clean(batch)) != root {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_ATTACHMENT_BATCH", "暂存批次无效")
	}
	if err = m.removeStagedBatch(filepath.Join(root, name), false); err != nil {
		return nil, apiError(http.StatusInternalServerError, "PLUGIN_STORAGE_UNAVAILABLE", "无法清理暂存附件")
	}
	return map[string]any{"released": true}, nil
}

// looksLikeStageBatch accepts only the shape this core generates: lowercase hex.
// A plugin therefore cannot name an arbitrary directory under its staging root
// and have it removed as a "batch".
func looksLikeStageBatch(name string) bool {
	if len(name) < 8 || len(name) > 64 {
		return false
	}
	for _, character := range name {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// purgeStaged removes everything a plugin has staged. The TTL sweep only runs
// when the plugin stages again, so a plugin that is stopped or removed would
// otherwise keep its dropped bytes under <dataDir>/staged/<id> forever — and an
// uninstalled plugin would leave them with no owner left to clean them up.
//
// It is a separate path from releaseStagedAttachments, which handles one batch a
// caller names. This one takes the whole plugin root and reports the real error,
// so a caller can record a removal that failed instead of claiming success.
func (m *Manager) purgeStaged(id string) error {
	if m.dataDir == "" {
		return nil
	}
	// The id decides the directory, so it must be a bare name before it is
	// joined to a path. Anything else is refused: a value with a separator could
	// otherwise name a sibling plugin's root.
	if id == "" || id != filepath.Base(id) || id == "." || id == ".." {
		return fmt.Errorf("暂存目录无效：%q", id)
	}
	root, err := filepath.Abs(filepath.Join(m.dataDir, "staged", id))
	if err != nil {
		return err
	}
	parent, err := os.Lstat(filepath.Dir(root))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !parent.IsDir() {
		return fmt.Errorf("插件暂存目录不可用：%s", filepath.Dir(root))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for batch := range m.staged {
		if filepath.Dir(batch) == root {
			delete(m.staged, batch)
		}
	}
	return os.RemoveAll(root)
}

// purgeStagedAndRecord removes a stopped plugin's staged files and records a
// removal that actually failed. Teardown paths call it because they cannot
// return an error themselves without reporting the teardown as unsuccessful,
// and silently keeping dropped bytes on disk is the outcome this avoids.
func (m *Manager) purgeStagedAndRecord(ctx context.Context, id, kind, prefix string, now time.Time) {
	if err := m.purgeStaged(id); err != nil {
		_ = recordActivity(ctx, m.db, id, kind, prefix+err.Error(), now)
	}
}

// pruneStagedAttachments drops stages older than the TTL. A plugin that stages
// files and never sends them must not grow the plugin directory forever; the
// sweep runs on the same actions that create stages.
func (m *Manager) pruneStagedAttachments(id string) {
	root, err := m.stageRoot(id)
	if err != nil {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-attachmentStagedTTL)
	for _, item := range entries {
		if !item.IsDir() {
			continue
		}
		info, infoErr := item.Info()
		if infoErr != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = m.removeStagedBatch(filepath.Join(root, item.Name()), false)
	}
}

// safeAttachmentName keeps the extension and a readable stem, and removes every
// character that could change the path it is joined to.
func safeAttachmentName(raw string) string {
	name := filepath.Base(strings.TrimSpace(raw))
	name = strings.Map(func(character rune) rune {
		switch {
		case character == '/' || character == '\\' || character == 0:
			return -1
		case character < 0x20 || character == 0x7f:
			return -1
		}
		return character
	}, name)
	name = strings.Trim(name, ". ")
	if name == "" || name == "." || name == ".." {
		return ""
	}
	if len(name) > 120 {
		extension := filepath.Ext(name)
		if len(extension) > 16 {
			extension = ""
		}
		stem := name[:120-len(extension)]
		name = stem + extension
	}
	return name
}

func newStageBatch() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}
