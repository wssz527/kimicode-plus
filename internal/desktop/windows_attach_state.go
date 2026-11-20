package desktop

import "time"

func recordWindowsAttachFailure(dir string, proc WindowsProcessInfo, err error) error {
	return recordWindowsAttachState(dir, proc, "error", err)
}

func recordWindowsAttachState(dir string, proc WindowsProcessInfo, state string, err error) error {
	payload := map[string]any{
		"at": time.Now().UTC().Format(time.RFC3339Nano), "status": state,
		"hostPid": proc.PID, "hostStarted": proc.Started, "platform": "windows",
	}
	if err != nil {
		payload["error"] = err.Error()
	}
	return writeAttachState(dir, payload)
}

func windowsCurrentAttachState(proc WindowsProcessInfo, last map[string]any, hasDebugPort bool) string {
	if last["status"] == "reopening" {
		recorded, _ := last["at"].(string)
		if at, err := time.Parse(time.RFC3339Nano, recorded); err == nil && time.Since(at) < 2*time.Minute {
			return "reopening"
		}
	}
	if last["hostPid"] != float64(proc.PID) || last["hostStarted"] != proc.Started {
		return "unattached"
	}
	switch last["status"] {
	case "installed":
		if last["transport"] == "main-loopback-rpc" {
			return "attached"
		}
		if hasDebugPort {
			return "compatibility"
		}
	case "error":
		return "failed"
	}
	return "unattached"
}
