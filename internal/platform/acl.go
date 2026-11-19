package platform

import (
	"errors"
	"path/filepath"
	"strings"
)

func validateWindowsSID(sid string) error {
	sid = strings.TrimSpace(sid)
	if !strings.HasPrefix(sid, "S-1-") || len(sid) < 8 {
		return errors.New("invalid Windows user SID")
	}
	for _, r := range sid[4:] {
		if (r < '0' || r > '9') && r != '-' {
			return errors.New("invalid Windows user SID")
		}
	}
	return nil
}

// buildWindowsACLArgs constructs icacls arguments that strip inherited ACEs
// and grant full control solely to the current user SID on the private data dir.
func buildWindowsACLArgs(dir, userSID string) ([]string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("data directory path is required")
	}
	if !filepath.IsAbs(dir) && !(len(dir) >= 3 && dir[1] == ':' && (dir[2] == '\\' || dir[2] == '/')) {
		return nil, errors.New("data directory path must be absolute")
	}
	if err := validateWindowsSID(userSID); err != nil {
		return nil, err
	}
	return []string{
		dir,
		"/inheritance:r",
		"/grant:r",
		"*" + strings.TrimSpace(userSID) + ":(OI)(CI)F",
	}, nil
}
