//go:build !windows

package platform

func ensurePrivateDirACL(string) error {
	return nil
}
