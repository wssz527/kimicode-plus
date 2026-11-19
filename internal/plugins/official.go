package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
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
	return directoryDigest(dir, nil)
}

type servedModule struct {
	name string
	data []byte
}

func directoryDigest(dir string, module *servedModule) (string, error) {
	root, err := openPluginRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	var names []string
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			if !entry.Type().IsRegular() {
				return fmt.Errorf("插件目录含非普通文件：%s", name)
			}
			names = append(names, name)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", errors.New("插件目录为空")
	}
	sort.Strings(names)
	digest := sha256.New()
	for _, name := range names {
		// Verify the source returned to the renderer, even if the file changed again after it was read.
		if module != nil && name == module.name {
			fmt.Fprintf(digest, "%s\x00%d\x00", name, len(module.data))
			digest.Write(module.data)
			continue
		}
		file, info, err := openPluginFile(root, name)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(digest, "%s\x00%d\x00", name, info.Size())
		_, copyErr := io.CopyN(digest, file, info.Size())
		var extra [1]byte
		_, endErr := file.Read(extra[:])
		file.Close()
		if copyErr != nil || endErr != io.EOF {
			return "", fmt.Errorf("校验时插件文件已变更：%s", name)
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// officialFor checks the installed copy against the compiled allowlist.
func (m *Manager) officialFor(manifest Manifest, dir, source string) bool {
	return digestMatches(dir, m.officialDigest(manifest, source), nil)
}

func (m *Manager) officialDigest(manifest Manifest, source string) string {
	if source != SourceBundled || m.officialDigests == nil {
		return ""
	}
	allowlist, err := m.officialDigests()
	if err != nil {
		return ""
	}
	return allowlist[manifest.ID]
}

func digestMatches(dir, expected string, module *servedModule) bool {
	if expected == "" {
		return false
	}
	actual, err := directoryDigest(dir, module)
	return err == nil && actual == expected
}

func (m *Manager) privilegeIntact(item entry) bool {
	switch item.source {
	case SourceBundled:
		return m.officialFor(item.manifest, item.dir, item.source)
	case SourceLocal:
		return digestMatches(item.dir, item.installDigest, nil)
	}
	return false
}

func (m *Manager) verifyPrivilege(item entry) error {
	if !m.privilegeIntact(item) {
		return privilegeDenied()
	}
	return nil
}

func privilegeDenied() error {
	return apiError(http.StatusForbidden, "PLUGIN_PRIVILEGE_DENIED", "插件文件与已验证的安装摘要不一致，请重新安装插件")
}
