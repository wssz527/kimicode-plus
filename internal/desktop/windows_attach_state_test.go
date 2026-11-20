package desktop

import "testing"

func TestWindowsCurrentAttachStateRejectsPreviousProcess(t *testing.T) {
	proc := WindowsProcessInfo{PID: 42, Started: "current"}
	last := map[string]any{"hostPid": float64(42), "hostStarted": "old", "status": "installed", "transport": "main-loopback-rpc"}
	if got := windowsCurrentAttachState(proc, last, false); got != "unattached" {
		t.Fatalf("PID reuse reported %s", got)
	}
	last["hostStarted"] = proc.Started
	if got := windowsCurrentAttachState(proc, last, false); got != "attached" {
		t.Fatalf("main bridge reported %s", got)
	}
	last["transport"] = "windows-loopback-rpc"
	if got := windowsCurrentAttachState(proc, last, true); got != "compatibility" {
		t.Fatalf("renderer transport reported %s", got)
	}
	last["status"] = "error"
	if got := windowsCurrentAttachState(proc, last, false); got != "failed" {
		t.Fatalf("failed attach reported %s", got)
	}
}
