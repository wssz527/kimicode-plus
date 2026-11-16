//go:build !darwin

package desktop

import "context"

// hostPlatformSupported gates the surfaces that would otherwise take over a
// host process. Windows and Linux only have to compile: no host runtime path has
// been verified there, and neither the inode check nor the SIGUSR1 inspector
// trigger exists in the same form.
const hostPlatformSupported = false

func verifyRuntimeImage(context.Context, ProcessInfo) error {
	return ErrUnsupportedPlatform
}

func activateInspector(int) error {
	return ErrUnsupportedPlatform
}
