package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// BundledPlugins returns the embedded plugin set as manifest id → (relative path
// → bytes). The directory name is only a packaging detail: the manifest id is
// what the install is stored and mounted under, so a bundle directory may be
// shorter than the id without becoming a second plugin.
// OfficialDigests returns the content digest of every embedded plugin. The map
// is computed from the same bytes BundledPlugins serves, so the allowlist in the
// binary and the directory released to disk have one definition. A plugin whose
// installed files no longer hash to this value keeps only its v1 capabilities.
func OfficialDigests() (map[string]string, error) {
	set, err := BundledPlugins()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(set))
	for id, files := range set {
		out[id] = digestFiles(files)
	}
	return out, nil
}

func digestFiles(files map[string][]byte) string {
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

func BundledPlugins() (map[string]map[string][]byte, error) {
	root, err := fs.Sub(bundled, ".")
	if err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(root, ".")
	if err != nil {
		return nil, err
	}
	out := map[string]map[string][]byte{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dir := entry.Name()
		files := map[string][]byte{}
		err = fs.WalkDir(root, dir, func(current string, item fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if item.IsDir() {
				return nil
			}
			data, readErr := fs.ReadFile(root, current)
			if readErr != nil {
				return readErr
			}
			files[path.Clean(strings.TrimPrefix(current, dir+"/"))] = data
			return nil
		})
		if err != nil {
			return nil, err
		}
		raw, ok := files["plugin.json"]
		if !ok {
			return nil, fmt.Errorf("bundled plugin %s has no plugin.json", dir)
		}
		var header struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			return nil, fmt.Errorf("bundled plugin %s has an unreadable manifest: %w", dir, err)
		}
		if _, duplicate := out[header.ID]; duplicate {
			return nil, fmt.Errorf("bundled plugin id %s appears twice", header.ID)
		}
		out[header.ID] = files
	}
	return out, nil
}
