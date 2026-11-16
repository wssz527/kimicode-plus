package platform

import (
	"os"
	"path/filepath"
)

func DefaultDataDir() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "EnhancedKimiDesktop"), nil
}
