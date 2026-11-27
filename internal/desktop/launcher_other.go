//go:build !darwin

package desktop

var handoffManagedCore = func(LaunchAgentConfig) error { return nil }

func darwinLauncherInstalledPath(string) string { return "" }
