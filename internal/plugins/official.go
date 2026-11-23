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
	"sync"
	"time"
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

// officialDigestSet is the one-time-loaded embedded allowlist. It is held
// behind an atomic pointer rather than directly on the Manager so a test that
// swaps the injected loader can invalidate it without recreating the manager.
type officialDigestSet struct {
	once    sync.Once
	digests map[string]string
}

// officialVerificationKey identifies one installed official plugin state. The
// timestamps make a reinstall or hot update a different key even when the
// version string did not change.
type officialVerificationKey struct {
	id        string
	dir       string
	version   string
	installed time.Time
	updated   time.Time
}

// officialVerification caches the verified verdict together with the cheap
// directory fingerprint it was computed for. A file touched on disk changes the
// fingerprint and forces a full re-verification, which keeps the cache from
// hiding tampering between actions.
type officialVerification struct {
	fingerprint string
	official    bool
}

func officialKeyFor(item entry) officialVerificationKey {
	return officialVerificationKey{
		id:        item.manifest.ID,
		dir:       item.dir,
		version:   item.manifest.Version,
		installed: item.installed,
		updated:   item.updated,
	}
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

// officialForEntry checks the installed copy against either the compiled
// bundled allowlist or a verified Ed25519-signed official plugin manifest.
//
// The verified verdict is cached per installed state, so the repeated
// events.poll / runtime.info reads do not re-walk and re-hash the plugin files
// or re-run Ed25519 verification. A metadata fingerprint is checked on every
// hit, which keeps a modified file from inheriting the previous verdict.
func (m *Manager) officialForEntry(item entry, module *servedModule) bool {
	if item.source != SourceBundled {
		return false
	}
	key := officialKeyFor(item)
	fingerprint := ""
	if module == nil {
		if value, err := directoryFingerprint(item.dir); err == nil {
			fingerprint = value
			m.officialCacheMu.Lock()
			cached, found := m.officialCache[key]
			m.officialCacheMu.Unlock()
			if found && cached.fingerprint == fingerprint {
				return cached.official
			}
		}
	}
	official := false
	if expected := m.officialDigest(item.manifest, item.source); expected != "" && digestMatches(item.dir, expected, module) {
		official = true
	} else if expected := m.signedOfficialDigest(item); expected != "" && digestMatches(item.dir, expected, module) {
		official = true
	}
	if fingerprint != "" {
		m.officialCacheMu.Lock()
		if m.officialCache == nil {
			m.officialCache = map[officialVerificationKey]officialVerification{}
		}
		m.officialCache[key] = officialVerification{fingerprint: fingerprint, official: official}
		m.officialCacheMu.Unlock()
	}
	return official
}

// officialAllowlist loads the embedded digest allowlist once. The pointer is
// swapped by invalidateOfficialDigestCache, which is what keeps a hot update or
// a test-supplied loader from being pinned to the first answer forever.
func (m *Manager) officialAllowlist() map[string]string {
	if m.officialDigests == nil {
		return nil
	}
	set := m.officialSet.Load()
	if set == nil {
		fresh := &officialDigestSet{}
		if !m.officialSet.CompareAndSwap(nil, fresh) {
			set = m.officialSet.Load()
		} else {
			set = fresh
		}
	}
	if set == nil {
		return nil
	}
	set.once.Do(func() {
		allowlist, err := m.officialDigests()
		if err == nil {
			set.digests = allowlist
		}
	})
	return set.digests
}

func (m *Manager) invalidateOfficialDigestCache() {
	m.officialSet.Store(&officialDigestSet{})
}

// invalidateOfficialCache drops the cached verdicts of one plugin. It is called
// whenever its files or install record change; other plugins keep their entries.
func (m *Manager) invalidateOfficialCache(id string) {
	m.officialCacheMu.Lock()
	for key := range m.officialCache {
		if key.id == id {
			delete(m.officialCache, key)
		}
	}
	m.officialCacheMu.Unlock()
}

func (m *Manager) officialDigest(manifest Manifest, source string) string {
	if source != SourceBundled {
		return ""
	}
	allowlist := m.officialAllowlist()
	if allowlist == nil {
		return ""
	}
	return allowlist[manifest.ID]
}

// directoryFingerprint hashes file names, sizes and modification times without
// reading any content. It is far cheaper than DirDigest and is only used to
// decide whether a cached verdict still describes this directory.
func directoryFingerprint(dir string) (string, error) {
	root, err := openPluginRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	type meta struct {
		name string
		size int64
		mod  int64
	}
	var entries []meta
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("插件目录含非普通文件：%s", name)
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		entries = append(entries, meta{name: name, size: info.Size(), mod: info.ModTime().UnixNano()})
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", errors.New("插件目录为空")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	digest := sha256.New()
	for _, entry := range entries {
		fmt.Fprintf(digest, "%s\x00%d\x00%d\x00", entry.name, entry.size, entry.mod)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func (m *Manager) signedOfficialDigest(item entry) string {
	if item.source != SourceBundled || len(item.officialManifest) == 0 || len(m.officialKeys) == 0 {
		return ""
	}
	catalog, err := VerifySignedPluginManifest(item.officialManifest, m.officialKeys)
	if err != nil {
		return ""
	}
	entryMeta, ok := catalog.Find(item.manifest.ID)
	if !ok || entryMeta.Version != item.manifest.Version {
		return ""
	}
	if m.bundled != nil {
		if set, err := m.bundled(); err == nil {
			if files, exists := set[item.manifest.ID]; exists {
				if bundledM, mErr := bundledManifest(item.manifest.ID, files); mErr == nil {
					if newerVersion(bundledM.Version, item.manifest.Version) {
						return ""
					}
				}
			}
		}
	}
	return entryMeta.ContentDigest
}

func digestMatches(dir, expected string, module *servedModule) bool {
	if expected == "" {
		return false
	}
	actual, err := directoryDigest(dir, module)
	return err == nil && actual == expected
}

func (m *Manager) privilegeIntact(item entry) bool {
	return m.privilegeIntactForModule(item, nil)
}

func (m *Manager) privilegeIntactForModule(item entry, module *servedModule) bool {
	switch item.source {
	case SourceBundled:
		return m.officialForEntry(item, module)
	case SourceLocal:
		return digestMatches(item.dir, item.installDigest, module)
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
