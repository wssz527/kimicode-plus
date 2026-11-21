package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"time"
)

const windowsAttachMaxAttempts = 5

var windowsAttachRetryDelays = [...]time.Duration{
	6 * time.Second,
	12 * time.Second,
	24 * time.Second,
	48 * time.Second,
	60 * time.Second,
}

// readCoreRuntimePID reports the pid recorded by the running enhancement core.
// It deliberately does not validate the whole runtime record: identity checks
// only need the pid, and a partially written file must not crash discovery.
func readCoreRuntimePID(dir string) (int, error) {
	if dir == "" {
		return 0, os.ErrNotExist
	}
	raw, err := os.ReadFile(filepath.Join(dir, "runtime.json"))
	if err != nil {
		return 0, err
	}
	var record struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(raw, &record); err != nil || record.PID <= 0 {
		return 0, os.ErrInvalid
	}
	return record.PID, nil
}

func currentCorePID(dir string) int {
	pid, err := readCoreRuntimePID(dir)
	if err != nil || pid <= 0 {
		return os.Getpid()
	}
	return pid
}

func recordWindowsAttachFailure(dir string, proc WindowsProcessInfo, err error) error {
	return recordWindowsAttachState(dir, proc, "error", err)
}

func recordWindowsAttachState(dir string, proc WindowsProcessInfo, state string, err error) error {
	payload := map[string]any{
		"at": time.Now().UTC().Format(time.RFC3339Nano), "status": state,
		"hostPid": proc.PID, "hostStarted": proc.Started, "platform": "windows",
		"corePid": currentCorePID(dir),
	}
	if err != nil {
		payload["error"] = err.Error()
	}
	return writeAttachState(dir, payload)
}

// recordWindowsAttachInstalled keeps the standard attach payload and adds the
// core identity so a later core restart cannot be mistaken for this attach.
func recordWindowsAttachInstalled(dir string, proc WindowsProcessInfo, result *AttachResult) error {
	if result == nil {
		return recordWindowsAttachState(dir, proc, "installed", nil)
	}
	if err := RecordAttach(dir, result); err != nil {
		return err
	}
	last := ReadAttachState(dir)
	if last == nil {
		last = map[string]any{}
	}
	// Stamp the identity the caller already verified, but never overwrite an
	// attach result that reported a different host process.
	if recorded, ok := attachStatePID(last["hostPid"]); !ok || recorded == proc.PID {
		last["hostPid"] = proc.PID
		last["hostStarted"] = proc.Started
	}
	last["corePid"] = currentCorePID(dir)
	return writeAttachState(dir, last)
}

func attachStatePID(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, typed > 0
	case int64:
		return int(typed), typed > 0
	case float64:
		if typed > 0 && typed <= math.MaxInt32 && typed == math.Trunc(typed) {
			return int(typed), true
		}
	case json.Number:
		if parsed, err := typed.Int64(); err == nil && parsed > 0 && parsed <= math.MaxInt32 {
			return int(parsed), true
		}
	}
	return 0, false
}

// windowsCurrentAttachState keeps the three-argument form used by callers that
// do not know the running core identity.
func windowsCurrentAttachState(proc WindowsProcessInfo, last map[string]any, hasDebugPort bool) string {
	return windowsCurrentAttachStateForCore(proc, last, hasDebugPort, 0)
}

// windowsCurrentAttachStateForCore rejects records left behind by an earlier
// core. In compatibility mode the bridge session lives in core memory, so the
// same host process is not attached after the core restarts.
func windowsCurrentAttachStateForCore(proc WindowsProcessInfo, last map[string]any, hasDebugPort bool, corePID int) string {
	staleCore := false
	if corePID > 0 {
		if recorded, ok := last["corePid"]; ok {
			if pid, valid := attachStatePID(recorded); !valid || pid != corePID {
				staleCore = true
			}
		}
	}
	if last["status"] == "reopening" && !staleCore {
		recorded, _ := last["at"].(string)
		if at, err := time.Parse(time.RFC3339Nano, recorded); err == nil && time.Since(at) < 2*time.Minute {
			return "reopening"
		}
	}
	if last["hostPid"] != float64(proc.PID) || last["hostStarted"] != proc.Started {
		return "unattached"
	}
	if staleCore {
		switch last["status"] {
		case "installed", "error":
			return "unattached"
		}
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

// windowsAttachStateRecordedBeforeCore reports whether a legacy state file
// without corePid was written before the running core started.
func windowsAttachStateRecordedBeforeCore(dataDir string) bool {
	if dataDir == "" {
		return false
	}
	runtimeInfo, err := os.Stat(filepath.Join(dataDir, "runtime.json"))
	if err != nil {
		return false
	}
	attachInfo, err := os.Stat(filepath.Join(dataDir, "attach-state.json"))
	if err != nil {
		return false
	}
	return runtimeInfo.ModTime().After(attachInfo.ModTime())
}

// windowsCurrentAttachStateForDir additionally rejects outcomes that predate the
// running core even when they carry no corePid, which covers state files written
// by older code paths.
func windowsCurrentAttachStateForDir(dataDir string, proc WindowsProcessInfo, last map[string]any, hasDebugPort bool, corePID int) string {
	state := windowsCurrentAttachStateForCore(proc, last, hasDebugPort, corePID)
	switch state {
	case "attached", "compatibility", "failed":
	default:
		return state
	}
	if corePID <= 0 {
		return state
	}
	if _, recorded := last["corePid"]; recorded {
		return state
	}
	if windowsAttachStateRecordedBeforeCore(dataDir) {
		return "unattached"
	}
	return state
}

// windowsAttachTarget identifies one host process lifetime. A restarted process
// gets a new started value and therefore a fresh retry budget.
type windowsAttachTarget struct {
	PID     int
	Started string
}

type windowsAttachRetry struct {
	Attempts        int
	NextRetryAt     time.Time
	Attached        bool
	AttachedCorePID int
}

// windowsAttachRetries tracks bounded exponential backoff per candidate and
// resets the budget whenever the running core changes.
type windowsAttachRetries struct {
	corePID            int
	entries            map[windowsAttachTarget]windowsAttachRetry
	discoveryNotBefore time.Time
}

func newWindowsAttachRetries(corePID int) *windowsAttachRetries {
	return &windowsAttachRetries{corePID: corePID, entries: map[windowsAttachTarget]windowsAttachRetry{}}
}

func (r *windowsAttachRetries) observeCore(corePID int) {
	if r.corePID == corePID {
		return
	}
	r.corePID = corePID
	for target, entry := range r.entries {
		entry.Attempts = 0
		entry.NextRetryAt = time.Time{}
		if entry.AttachedCorePID != corePID {
			entry.Attached = false
			entry.AttachedCorePID = 0
		}
		r.entries[target] = entry
	}
}

func (r *windowsAttachRetries) due(target windowsAttachTarget, now time.Time) bool {
	entry, ok := r.entries[target]
	if !ok {
		return true
	}
	if entry.Attached && entry.AttachedCorePID == r.corePID {
		return false
	}
	if entry.Attempts > windowsAttachMaxAttempts {
		return false
	}
	return entry.NextRetryAt.IsZero() || !now.Before(entry.NextRetryAt)
}

func (r *windowsAttachRetries) success(target windowsAttachTarget) {
	r.entries[target] = windowsAttachRetry{Attached: true, AttachedCorePID: r.corePID}
}

// failure records one failed attach. Attempts counts failures: the first one is
// followed by a 6s retry and the fifth by 60s, after which the candidate is left
// alone until the process or core changes.
func (r *windowsAttachRetries) failure(target windowsAttachTarget, now time.Time) {
	entry := r.entries[target]
	entry.Attempts++
	entry.Attached = false
	entry.AttachedCorePID = 0
	if entry.Attempts <= windowsAttachMaxAttempts {
		entry.NextRetryAt = now.Add(windowsAttachRetryDelays[entry.Attempts-1])
	} else {
		entry.NextRetryAt = time.Time{}
	}
	r.entries[target] = entry
}

// block retires a candidate that must never be auto-attached while this process
// lifetime lasts, such as an externally launched debug instance.
func (r *windowsAttachRetries) block(target windowsAttachTarget) {
	r.entries[target] = windowsAttachRetry{Attempts: windowsAttachMaxAttempts + 1}
}

// deferDiscovery skips host discovery until the given time, so an ambiguous
// process set does not respawn the PowerShell snapshot on every tick.
func (r *windowsAttachRetries) deferDiscovery(until time.Time) {
	if until.After(r.discoveryNotBefore) {
		r.discoveryNotBefore = until
	}
}

func (r *windowsAttachRetries) discoveryAllowed(now time.Time) bool {
	return !now.Before(r.discoveryNotBefore)
}

// dueAny reports whether any observed candidate still has retry budget. It is
// what keeps the watcher revisiting a failed process once its backoff elapses.
func (r *windowsAttachRetries) dueAny(targets map[windowsAttachTarget]bool, now time.Time) bool {
	for target := range targets {
		if r.due(target, now) {
			return true
		}
	}
	return false
}

func (r *windowsAttachRetries) prune(targets map[windowsAttachTarget]bool) {
	for target := range r.entries {
		if !targets[target] {
			delete(r.entries, target)
		}
	}
}

type windowsBackgroundAttachDeps struct {
	now             func() time.Time
	serviceAlive    func() bool
	discoverHost    func(ctx context.Context) (WindowsProcessInfo, error)
	targetForPID    func(pid int) (windowsAttachTarget, bool)
	readLease       func() *WindowsLauncherState
	readAttachState func() map[string]any
	attach          func(ctx context.Context, opts AttachOptions) (*AttachResult, error)
	recordInstalled func(proc WindowsProcessInfo, result *AttachResult) error
	recordFailure   func(proc WindowsProcessInfo, err error) error
}

func runWindowsBackgroundAttachStep(ctx context.Context, cfg AttachConfig, retries *windowsAttachRetries, corePID int, deps windowsBackgroundAttachDeps) {
	now := deps.now
	if now == nil {
		now = time.Now
	}
	proc, err := deps.discoverHost(ctx)
	if err != nil {
		// Discovery is the expensive step; when it cannot pick exactly one host
		// there is nothing to attach to, so back off instead of retrying each tick.
		retries.deferDiscovery(now().Add(windowsAttachRetryDelays[0]))
		return
	}
	target, found := deps.targetForPID(proc.PID)
	if !found {
		return
	}
	if !retries.due(target, now()) {
		return
	}
	_, hasDebugPort := extractDebugPortArg(SplitWindowsCommandLine(proc.CommandLine))
	lease := deps.readLease()
	ownedByLauncher := windowsLauncherOwnsProcess(lease, proc)
	if hasDebugPort && !ownedByLauncher {
		// Never touch a debugging instance that our launcher did not start.
		retries.block(target)
		return
	}
	if !hasDebugPort && cfg.Profile == "" && IsWindowsDebugInstance(proc, lease) {
		retries.block(target)
		return
	}
	state := windowsCurrentAttachStateForDir(cfg.DataDir, proc, deps.readAttachState(), hasDebugPort, corePID)
	if state == "reopening" {
		return
	}
	if state == "attached" || state == "compatibility" {
		retries.success(target)
		return
	}
	result, err := deps.attach(ctx, AttachOptions{
		DataDir: cfg.DataDir, CoreURL: cfg.CoreURL, PairingToken: cfg.PairingToken, Profile: cfg.Profile,
		AllowDailyInstance: cfg.Profile == "",
	})
	if err == nil && result != nil {
		_ = deps.recordInstalled(proc, result)
		retries.success(target)
		return
	}
	if err == nil {
		err = errors.New("接入未返回结果")
	}
	if deps.serviceAlive == nil || deps.serviceAlive() {
		_ = deps.recordFailure(proc, err)
		retries.failure(target, now())
	}
}
