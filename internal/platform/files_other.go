//go:build !windows

package platform

func RetryFileOperation(action func() error) error { return action() }
