package plugins

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// BundledPlugins returns the embedded plugin set as manifest id → (relative path
// → bytes). The directory name is only a packaging detail: the manifest id is
// what the install is stored and mounted under, so a bundle directory may be
// shorter than the id without becoming a second plugin.
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
