//go:build !darwin

package updater

import "errors"

var errLaunchdUnsupported = errors.New("当前系统不使用 launchd")

// defaultLaunchdManaged never reports a managed login item on platforms without
// launchd, so updates use the detached start path.
func defaultLaunchdManaged(string, string) bool { return false }

const launchdDefaultLabel = "com.user.enhanced-kimi.core-managed"

func defaultKickstart(string) error                { return errLaunchdUnsupported }
func defaultLaunchdKill(string, string) error      { return errLaunchdUnsupported }
func defaultLaunchdBootout(string) error           { return errLaunchdUnsupported }
func defaultLaunchdServicePID(string) (int, error) { return 0, nil }

func (job supervisorJob) stopLaunchdCore() error { return nil }

func (job supervisorJob) prepareRestartMarker() error { return nil }

func stopDetachedProcess(int) error { return nil }
