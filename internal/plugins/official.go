package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
)

// OfficialDigests is the build-time allowlist of embedded plugin content. The
// digest is SHA-256 over the directory's relative paths and bytes, so it changes
// when any shipped file changes. Values are the digest an installed copy must
// reproduce for its v2 capabilities to stay effective.
type OfficialDigests func() (map[string]string, error)

// WithOfficial attaches the allowlist to the manager. The core injects the
// bundled plugin set's digests, so this package never imports the bundle.
func WithOfficial(load OfficialDigests) Option {
	return func(o *Options) { o.Official = load }
}

// ContentDigest hashes one plugin directory. Paths are sorted and both the path
// and the byte length are fed in, which keeps `a/b` from colliding with a file
// named `a` followed by `b`. It is the single canonical form used for both the
// bundled allowlist and an installed directory.
func ContentDigest(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	digest := sha256.New()
	for _, name := range names {
		data := files[name]
		fmt.Fprintf(digest, "%s\x00%d\x00", name, len(data))
		digest.Write(data)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// DirDigest hashes an installed plugin directory with the same canonical form as
// ContentDigest. Symbolic links and non-regular files make the directory
// unverifiable, so they fail instead of being skipped.
func DirDigest(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("插件目录未知")
	}
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("插件目录含非普通文件：%s", current)
		}
		relative, err := filepath.Rel(dir, current)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		files[path.Clean(filepath.ToSlash(relative))] = data
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", errors.New("插件目录为空")
	}
	return ContentDigest(files), nil
}

// officialFor reports whether one install is an official bundle whose content
// still matches the compiled allowlist. It reads both the allowlist and the
// directory, so callers must not hold m.mu.
func (m *Manager) officialFor(manifest Manifest, dir, source string) bool {
	if source != SourceBundled {
		return false
	}
	load := m.officialDigests
	if load == nil {
		return false
	}
	allowlist, err := load()
	if err != nil {
		return false
	}
	expected, ok := allowlist[manifest.ID]
	if !ok || expected == "" {
		return false
	}
	actual, err := DirDigest(dir)
	if err != nil {
		return false
	}
	return actual == expected
}

// verifyPrivilege re-checks the allowlist for one privileged call. The check is
// repeated rather than trusted from enable time because the files can change
// while the plugin runs; a mismatch stops nothing the user already enabled, but
// every high-privilege action is refused until the content is restored.
func (m *Manager) verifyPrivilege(id string) error {
	m.mu.Lock()
	item := m.entries[id]
	var manifest Manifest
	var dir, source string
	if item != nil {
		manifest, dir, source = item.manifest, item.dir, item.source
	}
	m.mu.Unlock()
	if item == nil {
		return apiError(http.StatusNotFound, "PLUGIN_NOT_FOUND", "插件未安装")
	}
	if !m.officialFor(manifest, dir, source) {
		return apiError(http.StatusForbidden, "PLUGIN_PRIVILEGE_DENIED", "高权限能力只授予内容摘要匹配的官方预装插件")
	}
	return nil
}
