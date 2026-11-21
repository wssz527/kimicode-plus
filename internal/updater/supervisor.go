package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"enhancedkimi/internal/platform"
	"enhancedkimi/internal/security"

	"github.com/gofrs/flock"
)

const (
	jobName      = "job.json"
	readyTimeout = 10 * time.Second
)

var coreTimeout = 30 * time.Second

type supervisorJob struct {
	Executable      string
	DataDir         string
	WorkingDir      string
	Args            []string
	OldPID          int
	Ticket          string
	PreviousVersion string
	TargetVersion   string
	PreviousDigest  string
	TargetDigest    string
	Restart         RestartState
	HasNotices      bool
	HasLicense      bool
	// LaunchdLabel is set when launchd owns the resident core. The supervisor
	// then hands the candidate to launchd instead of starting a detached child,
	// so only one --resident process can exist.
	LaunchdLabel    string
	ExpectedModules []string
}

func (m *Manager) prepareSupervisor(stage, target string, files extractedFiles) error {
	targetExe := m.targetExecutable()
	managedTarget := targetExe != m.options.Executable
	// helper is the supervisor image. Once the user deletes the downloaded copy
	// after enabling the resident login item, the managed copy is the only valid
	// source left; the running process image still belongs to that same file.
	helper := m.options.Executable
	if managedTarget {
		current, err := executableDigest(m.options.Executable)
		switch {
		case err == nil:
			installed, err := executableDigest(targetExe)
			if err != nil {
				return err
			}
			if current != installed {
				return errors.New("正在运行的核心与登录项中的副本不同，请从登录项启动核心后再更新")
			}
		case errors.Is(err, os.ErrNotExist):
			if _, err = executableDigest(targetExe); err != nil {
				return err
			}
			helper = targetExe
		default:
			return err
		}
	}
	previous, err := executableDigest(helper)
	if err != nil {
		return err
	}
	normalizedTarget, err := executableDigest(filepath.Join(stage, "binary"))
	if err != nil {
		return err
	}
	launchdLabel := launchdLabelFor(m.options, targetExe)
	job := supervisorJob{
		Executable: targetExe, DataDir: m.options.DataDir, WorkingDir: m.workingDir(managedTarget || launchdLabel != ""),
		Args: os.Args[1:], OldPID: os.Getpid(), Ticket: security.Random(), PreviousVersion: m.options.CurrentVersion,
		TargetVersion: target, Restart: m.options.RestartState(), HasNotices: files.notices != "", HasLicense: files.license != "",
		PreviousDigest: hex.EncodeToString(previous[:]), TargetDigest: hex.EncodeToString(normalizedTarget[:]),
		LaunchdLabel: launchdLabel,
	}
	if job.Restart.Port < 1 || job.Restart.Port > 65535 {
		return errors.New("无法保存核心当前的监听端口")
	}
	job.ExpectedModules = healthyModules(m.options.HealthyModules)
	jobPath := filepath.Join(stage, jobName)
	if err = writeUpdateJSON(jobPath, job); err != nil {
		return err
	}
	supervisor := filepath.Join(stage, "supervisor")
	if m.options.GOOS == "windows" {
		supervisor += ".exe"
	}
	if err = atomicCopy(helper, supervisor, 0o755); err != nil {
		return err
	}
	if err = m.options.launchSupervisor(supervisor, jobPath); err != nil {
		if result := readResult(job.DataDir); result.State == Restarting && result.Ticket == job.Ticket {
			return errors.Join(err, result.finish(job.DataDir, Failed, err.Error()))
		}
	}
	return err
}

// workingDir keeps the download directory out of the restarted resident core.
// os.Getwd fails outright once the download folder is gone.
func (m *Manager) workingDir(managedTarget bool) string {
	if !managedTarget {
		if cwd, err := os.Getwd(); err == nil {
			return cwd
		}
	}
	return m.options.DataDir
}

func launchdLabelFor(options Options, targetExe string) string {
	if options.GOOS != "darwin" || targetExe == "" || options.Executable == "" {
		return ""
	}
	// The handover is only safe when launchd is the process that starts the
	// candidate. A registered-but-unloaded login item cannot restart it, so the
	// supervisor starts the core itself instead.
	enabled := options.Launchd.Enabled
	if enabled == nil {
		enabled = func(target string) bool { return defaultLaunchdManaged(options.DataDir, target) }
	}
	if !enabled(targetExe) {
		return ""
	}
	if options.Launchd.Managed != nil && !options.Launchd.Managed() {
		return ""
	}
	if options.Launchd.Label == "" {
		return launchdDefaultLabel
	}
	return options.Launchd.Label
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}

// healthyModules reads the running core's module view at update time. Deferring
// the read keeps the manager from capturing an empty slice during server
// construction, before modules are registered.
func healthyModules(read func() []string) []string {
	if read == nil {
		return nil
	}
	return cloneStrings(read())
}

func executableDigest(path string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	file, err := os.Open(path)
	if err != nil {
		return sum, err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return sum, err
	}
	copy(sum[:], digest.Sum(nil))
	return sum, nil
}

func startSupervisor(executable, jobPath string) error {
	cmd := exec.Command(executable, SupervisorCommand, jobPath)
	detachProcess(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	defer stdout.Close()
	if err = cmd.Start(); err != nil {
		return err
	}
	reply := make(chan error, 1)
	go func() {
		var message string
		if err := json.NewDecoder(stdout).Decode(&message); err != nil {
			reply <- fmt.Errorf("更新监督进程未就绪：%w", err)
		} else if message != "" {
			reply <- errors.New(message)
		} else {
			reply <- nil
		}
	}()
	timer := time.NewTimer(readyTimeout)
	defer timer.Stop()
	select {
	case err = <-reply:
	case <-timer.C:
		err = errors.New("等待更新监督进程就绪超时")
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func RunSupervisor(jobPath string) error {
	job, stage, err := readSupervisorJob(jobPath)
	if err == nil {
		err = job.supervise(stage)
	}
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(err.Error())
	}
	return err
}

func readSupervisorJob(path string) (supervisorJob, string, error) {
	var job supervisorJob
	data, err := os.ReadFile(path)
	if err != nil {
		return job, "", err
	}
	if err = json.Unmarshal(data, &job); err != nil {
		return job, "", err
	}
	stage := filepath.Dir(path)
	self, err := os.Executable()
	if err != nil {
		return job, "", err
	}
	stageInfo, stageErr := os.Stat(stage)
	selfDir, selfErr := os.Stat(filepath.Dir(self))
	if stageErr != nil || selfErr != nil || !os.SameFile(stageInfo, selfDir) ||
		filepath.Dir(stage) != filepath.Join(job.DataDir, "updates", "downloads") ||
		!filepath.IsAbs(job.Executable) || job.OldPID < 1 || job.Ticket == "" || job.Restart.Port < 1 || job.Restart.Port > 65535 {
		return job, "", errors.New("更新监督进程参数无效")
	}
	return job, stage, nil
}

// startupGate owns the locks that coordinate a supervised core update. The
// launchd handover drops only the startup lock so launchd's replacement can pass
// AcquireCore while supervisor.lock stays held until this supervisor exits.
type startupGate struct {
	supervisor *flock.Flock
	startup    *flock.Flock
	update     *flock.Flock
}

func openStartupGate(dataDir, executable string) (*startupGate, error) {
	if err := os.MkdirAll(filepath.Dir(startupLockPath(dataDir)), 0o700); err != nil {
		return nil, fmt.Errorf("无法准备核心更新锁目录：%w", err)
	}
	gate := &startupGate{
		supervisor: flock.New(supervisorLockPath(dataDir)),
		startup:    flock.New(startupLockPath(dataDir)),
		update:     flock.New(executable + ".update.lock"),
	}
	for _, lock := range []*flock.Flock{gate.supervisor, gate.startup, gate.update} {
		locked, err := lock.TryLock()
		if err != nil {
			_ = gate.Close()
			return nil, fmt.Errorf("无法锁定核心更新：%w", err)
		}
		if !locked {
			_ = gate.Close()
			return nil, ErrBusy
		}
	}
	return gate, nil
}

// releaseStartup drops only the startup lock so a candidate can pass AcquireCore.
func (gate *startupGate) releaseStartup() error {
	if gate.startup == nil {
		return nil
	}
	err := gate.startup.Unlock()
	gate.startup = nil
	return err
}

func (gate *startupGate) Close() error {
	var errs []error
	for _, lock := range []*flock.Flock{gate.startup, gate.update, gate.supervisor} {
		if lock != nil {
			errs = append(errs, lock.Close())
		}
	}
	gate.supervisor, gate.startup, gate.update = nil, nil, nil
	return errors.Join(errs...)
}

func (job supervisorJob) supervise(stage string) error {
	defer cleanupSupervisor(stage)
	waitForParent, closeParent, err := watchParent(job.OldPID)
	if err != nil {
		return err
	}
	defer closeParent()
	gate, err := openStartupGate(job.DataDir, job.Executable)
	if err != nil {
		return err
	}
	defer gate.Close()
	receipt := updateReceipt{
		UpdateResult: UpdateResult{State: Restarting, TargetVersion: job.TargetVersion, PreviousVersion: job.PreviousVersion},
		Ticket:       job.Ticket,
	}
	if err := receipt.write(job.DataDir); err != nil {
		return fmt.Errorf("无法记录待执行的更新：%w", err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(""); err != nil {
		return errors.Join(err, receipt.finish(job.DataDir, Failed, "更新监督进程无法通知旧核心退出"))
	}
	_ = os.Stdout.Close()
	ctx, cancel := context.WithTimeout(context.Background(), coreTimeout)
	err = waitForParent(ctx)
	cancel()
	if err != nil {
		return receipt.finish(job.DataDir, Failed, fmt.Sprintf("更新失败，旧核心未能退出（%v）", err))
	}
	return job.replaceAndRestart(stage, gate, receipt)
}

func (job supervisorJob) replaceAndRestart(stage string, gate *startupGate, receipt updateReceipt) error {
	previous, err := decodeDigest(job.PreviousDigest)
	if err != nil {
		return receipt.finish(job.DataDir, Failed, fmt.Sprintf("更新失败（%v），旧核心摘要无效", err))
	}
	target, err := decodeDigest(job.TargetDigest)
	if err != nil {
		return receipt.finish(job.DataDir, Failed, fmt.Sprintf("更新失败（%v），新核心摘要无效", err))
	}
	tx, err := job.beginTransaction(previous, target)
	if err != nil {
		return receipt.finish(job.DataDir, Failed, err.Error())
	}
	if err = updateFile(job.Executable, tx.BackupPath, 0o755); err != nil {
		return job.failTransaction(tx, receipt, fmt.Errorf("无法备份旧核心：%w", err))
	}
	if err = tx.setPhase(phaseBackedUp); err != nil {
		return job.failTransaction(tx, receipt, err)
	}
	// The old core has exited, so the SQLite files are closed and their write
	// locks released. Snapshot them before the candidate can migrate a schema.
	// This holds for the launchd handover as well: launchd has not yet been asked
	// for the new process at this point.
	if err = backupDatabases(job.DataDir, tx.DBBackupDir); err != nil {
		return job.failTransaction(tx, receipt, fmt.Errorf("无法备份数据库：%w", err))
	}
	if err = updateFile(filepath.Join(stage, "binary"), job.Executable, 0o755); err != nil {
		return job.rollback(tx, gate, receipt, nil, fmt.Errorf("无法安装新版本核心：%w", err))
	}
	if err = tx.setPhase(phaseReplaced); err != nil {
		return job.rollback(tx, gate, receipt, nil, err)
	}
	var core *runningCore
	if job.LaunchdLabel != "" {
		core, err = job.handOffToLaunchd(gate, tx, phaseVerifying)
	} else {
		core, err = job.startCore(receipt.Ticket)
	}
	if err == nil {
		if job.LaunchdLabel != "" {
			err = job.waitForLaunchdHealth(job.TargetVersion)
		} else {
			err = job.waitForHealth(core, job.TargetVersion)
		}
	}
	if err == nil {
		if err = tx.setPhase(phaseCommitted); err != nil {
			err = fmt.Errorf("无法记录更新结果：%w", err)
		} else if err = receipt.finish(job.DataDir, Updated, ""); err != nil {
			err = fmt.Errorf("无法记录更新结果：%w", err)
		} else {
			warnings := job.installReleaseLicenses(stage)
			return job.cleanUpTransaction(tx, receipt, Updated, "", warnings)
		}
	}
	if core != nil {
		if stopErr := core.stop(); stopErr != nil {
			return job.failPreservingRecovery(tx, receipt, fmt.Errorf("更新失败（%v），无法结束新核心：%v", err, stopErr))
		}
	}
	return job.rollback(tx, gate, receipt, core, err)
}

func decodeDigest(value string) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != sha256.Size {
		return sum, errors.New("摘要格式无效")
	}
	copy(sum[:], raw)
	return sum, nil
}

func (job supervisorJob) failTransaction(tx *updateTransaction, receipt updateReceipt, cause error) error {
	_ = tx.setPhase(phaseFailed)
	_ = clearActiveTransaction(job.DataDir)
	return receipt.finish(job.DataDir, Failed, cause.Error())
}

// failPreservingRecovery marks a post-replacement failure while keeping
// active-tx.json in place so the next core start can automatically recover once
// the blocking process exits.
func (job supervisorJob) failPreservingRecovery(tx *updateTransaction, receipt updateReceipt, cause error) error {
	_ = tx.setPhase(phaseFailed)
	reason := fmt.Sprintf("%v；备份保留在 %s", cause, tx.dir)
	return receipt.finish(job.DataDir, Failed, reason)
}

func (job supervisorJob) rollback(tx *updateTransaction, gate *startupGate, receipt updateReceipt, core *runningCore, cause error) error {
	if err := job.reacquireStartup(gate, core); err != nil {
		return job.failPreservingRecovery(tx, receipt, fmt.Errorf("更新失败（%v）；%w", cause, err))
	}
	if err := tx.setPhase(phaseRollingBack); err != nil {
		return job.failPreservingRecovery(tx, receipt, fmt.Errorf("更新失败（%v）；%w", cause, err))
	}
	var problems []string
	backupOK := backupMatches(tx.BackupPath, tx.PreviousDigest)
	if err := restoreDatabases(job.DataDir, tx.DBBackupDir); err != nil {
		problems = append(problems, fmt.Sprintf("无法还原数据库：%v", err))
	}
	if backupOK {
		if err := updateFile(tx.BackupPath, job.Executable, 0o755); err != nil {
			problems = append(problems, fmt.Sprintf("无法还原旧核心文件：%v", err))
		}
	} else {
		problems = append(problems, "旧核心备份缺失或已损坏")
	}
	if len(problems) == 0 {
		var old *runningCore
		var err error
		if job.LaunchdLabel != "" {
			old, err = job.handOffToLaunchd(gate, tx, phaseRollingBack)
		} else {
			old, err = job.startCore(receipt.Ticket)
		}
		if err == nil {
			if job.LaunchdLabel != "" {
				err = job.waitForLaunchdHealth(job.PreviousVersion)
			} else {
				err = job.waitForHealth(old, job.PreviousVersion)
			}
			if err != nil {
				err = errors.Join(err, old.stop())
			}
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("旧核心未恢复运行：%v", err))
		}
	}
	if len(problems) == 0 {
		reason := fmt.Sprintf("更新失败，已回滚：%v", cause)
		if err := receipt.finish(job.DataDir, RolledBack, reason); err != nil {
			return err
		}
		_ = tx.setPhase(phaseRolledBack)
		return job.cleanUpTransaction(tx, receipt, RolledBack, reason, nil)
	}
	_ = tx.setPhase(phaseFailed)
	reason := fmt.Sprintf("更新失败（%v）；%s；备份保留在 %s", cause, strings.Join(problems, "；"), tx.dir)
	if !backupOK {
		_ = clearActiveTransaction(job.DataDir)
	}
	return receipt.finish(job.DataDir, Failed, reason)
}

// reacquireStartup blocks launchd from starting the restored binary while the
// files are being put back.
func (job supervisorJob) reacquireStartup(gate *startupGate, core *runningCore) error {
	if job.LaunchdLabel == "" {
		return nil
	}
	if core != nil {
		_ = core.stop()
	}
	if err := job.stopLaunchdCore(); err != nil {
		return fmt.Errorf("无法停止不健康的新核心：%w", err)
	}
	if gate.startup != nil {
		return nil
	}
	lock := flock.New(startupLockPath(job.DataDir))
	deadline := time.Now().Add(coreTimeout)
	for {
		locked, err := lock.TryLock()
		if err != nil {
			_ = lock.Close()
			return fmt.Errorf("无法重新锁定核心更新：%w", err)
		}
		if locked {
			gate.startup = lock
			return nil
		}
		if time.Now().After(deadline) {
			_ = lock.Close()
			return errors.New("等待其他核心进程退出的时间过长，已保留更新备份")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (job supervisorJob) launchdKickstart() error {
	return defaultKickstart(job.LaunchdLabel)
}

func (job supervisorJob) launchdKill(signal string) error {
	return defaultLaunchdKill(job.LaunchdLabel, signal)
}

func (job supervisorJob) launchdBootout() error {
	return defaultLaunchdBootout(job.LaunchdLabel)
}

func (job supervisorJob) launchdServicePID() (int, error) {
	return defaultLaunchdServicePID(job.LaunchdLabel)
}

// handOffToLaunchd clears the stale runtime record, persists the restart marker
// and drops the startup lock so the job launchd starts can acquire the core.
// KeepAlive stays enabled: the job, not the supervisor, owns the process.
func (job supervisorJob) handOffToLaunchd(gate *startupGate, tx *updateTransaction, phase string) (*runningCore, error) {
	if err := job.prepareRestartMarker(); err != nil {
		return nil, err
	}
	if err := tx.setPhase(phase); err != nil {
		return nil, err
	}
	if err := gate.releaseStartup(); err != nil {
		return nil, err
	}
	if err := job.launchdKickstart(); err != nil {
		return nil, fmt.Errorf("无法启动更新后的核心：%w", err)
	}
	return nil, nil
}

func (job supervisorJob) installReleaseLicenses(stage string) []string {
	var warnings []string
	dir := filepath.Dir(job.Executable)
	for _, file := range []struct {
		name    string
		present bool
	}{{licenseName, job.HasLicense}, {noticesName, job.HasNotices}} {
		if file.present {
			if err := updateFile(filepath.Join(stage, file.name), filepath.Join(dir, file.name), 0o644); err != nil {
				warnings = append(warnings, fmt.Sprintf("无法更新 %s：%v", file.name, err))
			}
		}
	}
	return warnings
}

func (job supervisorJob) cleanUpTransaction(tx *updateTransaction, receipt updateReceipt, state, reason string, warnings []string) error {
	if err := clearActiveTransaction(job.DataDir); err != nil {
		warnings = append(warnings, fmt.Sprintf("无法清理更新事务标记：%v", err))
	}
	if err := os.RemoveAll(tx.dir); err != nil {
		warnings = append(warnings, fmt.Sprintf("无法删除更新备份：%v", err))
	}
	if len(warnings) == 0 {
		return nil
	}
	if reason != "" {
		warnings = append([]string{reason}, warnings...)
	}
	return receipt.finish(job.DataDir, state, strings.Join(warnings, "；"))
}

func (job supervisorJob) startCore(ticket string) (*runningCore, error) {
	path := filepath.Join(job.DataDir, "runtime.json")
	if err := removeUpdateFile(path); err != nil {
		return nil, fmt.Errorf("无法清除上次核心的运行记录：%w", err)
	}
	// Both new and older cores can read the same restart marker.
	if err := writeUpdateJSON(filepath.Join(job.DataDir, restartName), job.Restart); err != nil {
		return nil, fmt.Errorf("无法保存核心重启状态：%w", err)
	}
	cmd := exec.Command(job.Executable, job.Args...)
	cmd.Dir = job.WorkingDir
	cmd.Env = append(os.Environ(), childTicketEnv+"="+ticket)
	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("无法启动核心进程：%w", err)
	}
	child := &runningCore{process: cmd.Process, done: make(chan struct{}, 1), pid: cmd.Process.Pid}
	go func() {
		child.err = cmd.Wait()
		close(child.done)
	}()
	return child, nil
}

type runningCore struct {
	process *os.Process
	done    chan struct{}
	err     error
	pid     int
}

func (core *runningCore) stop() error {
	if core == nil {
		return nil
	}
	if core.done == nil {
		return stopDetachedProcess(core.pid)
	}
	select {
	case <-core.done:
		return nil
	default:
	}
	if err := core.process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-core.done:
			return nil
		case <-timer.C:
			return err
		}
	}
	<-core.done
	return nil
}

func (job supervisorJob) waitForHealth(core *runningCore, version string) error {
	ctx, cancel := context.WithTimeout(context.Background(), coreTimeout)
	defer cancel()
	return waitForCoreHealth(ctx, core, filepath.Join(job.DataDir, "runtime.json"), job.Restart.Port, version, job.ExpectedModules)
}

func waitForCoreHealth(ctx context.Context, core *runningCore, runtimePath string, port int, version string, expected []string) error {
	client := healthClient()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := checkCoreHealth(ctx, client, runtimePath, base, core.pid, version, expected)
		if err == nil {
			return nil
		}
		if core.done != nil {
			select {
			case <-core.done:
				if core.err != nil {
					return fmt.Errorf("核心在通过健康检查前退出：%w", core.err)
				}
				return errors.New("核心在通过健康检查前退出")
			default:
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待核心通过健康检查超时：%w", err)
		case <-ticker.C:
		}
	}
}

// waitForLaunchdHealth watches the process launchd started. The supervisor does
// not own that child, so progress is read from runtime.json instead of a wait
// channel.
func (job supervisorJob) waitForLaunchdHealth(version string) error {
	ctx, cancel := context.WithTimeout(context.Background(), coreTimeout)
	defer cancel()
	client := healthClient()
	base := fmt.Sprintf("http://127.0.0.1:%d", job.Restart.Port)
	runtimePath := filepath.Join(job.DataDir, "runtime.json")
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		pid, err := job.observeLaunchdCore(version)
		if err == nil {
			if err = checkCoreHealth(ctx, client, runtimePath, base, pid, version, job.ExpectedModules); err == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待核心通过健康检查超时：%w", err)
		case <-ticker.C:
		}
	}
}

func (job supervisorJob) observeLaunchdCore(version string) (int, error) {
	data, err := os.ReadFile(filepath.Join(job.DataDir, "runtime.json"))
	if err != nil {
		return 0, errors.New("尚未读取到新运行记录 runtime.json")
	}
	var record struct {
		PID     int
		Version string
	}
	if json.Unmarshal(data, &record) != nil || record.PID <= 0 {
		return 0, errors.New("新运行记录 runtime.json 格式无效")
	}
	if record.PID == job.OldPID {
		return 0, errors.New("运行记录仍指向已退出的旧核心")
	}
	if strings.TrimPrefix(record.Version, "v") != strings.TrimPrefix(version, "v") {
		return 0, fmt.Errorf("运行记录版本与目标版本 %s 不匹配", strings.TrimPrefix(version, "v"))
	}
	if servicePID, err := job.launchdServicePID(); err == nil && servicePID > 0 && servicePID != record.PID {
		return 0, fmt.Errorf("运行记录中的进程 %d 不属于登录项服务 %d", record.PID, servicePID)
	}
	return record.PID, nil
}

func healthClient() *http.Client {
	transport := &http.Transport{DisableKeepAlives: true}
	return &http.Client{
		Transport: transport, Timeout: time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// checkCoreHealth requires the runtime record, the reported port and version,
// and every module the previous core had running. A 200 with a module in
// FAULTED is exactly the state a database rollback must catch.
func checkCoreHealth(ctx context.Context, client *http.Client, runtimePath, base string, pid int, version string, expected []string) error {
	data, err := os.ReadFile(runtimePath)
	if err != nil {
		return errors.New("尚未读取到新运行记录 runtime.json")
	}
	var record struct {
		PID          int
		URL          string
		Version      string
		PairingToken string
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return errors.New("新运行记录 runtime.json 格式无效")
	}
	if record.PID != pid || record.URL != base || record.PairingToken == "" {
		return errors.New("新运行记录中的进程、端口或配对凭据不匹配")
	}
	version = strings.TrimPrefix(version, "v")
	if strings.TrimPrefix(record.Version, "v") != version {
		return fmt.Errorf("运行记录版本与目标版本 %s 不匹配", version)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/enhance/v1/health", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+record.PairingToken)
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("无法连接核心健康接口：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("核心健康接口返回 HTTP %d", response.StatusCode)
	}
	body, err := readBounded(response.Body, 64<<10)
	if err != nil {
		return fmt.Errorf("无法读取核心健康接口：%w", err)
	}
	var health struct {
		Status  string
		Version string
		Modules []healthModule
	}
	if json.Unmarshal(body, &health) != nil || health.Status != "OK" || strings.TrimPrefix(health.Version, "v") != version {
		return fmt.Errorf("核心健康接口未确认目标版本 %s 正常运行", version)
	}
	return verifyModuleHealth(health.Modules, expected)
}

type healthModule struct {
	ID      string
	State   string
	Message string
}

func verifyModuleHealth(modules []healthModule, expected []string) error {
	states := make(map[string]string, len(modules))
	for _, module := range modules {
		if module.State == "FAULTED" {
			message := module.Message
			if message == "" {
				message = "初始化失败"
			}
			return fmt.Errorf("核心模块 %s 故障：%s", module.ID, message)
		}
		states[module.ID] = module.State
	}
	for _, id := range expected {
		state, ok := states[id]
		if !ok {
			return fmt.Errorf("核心模块 %s 缺失，无法确认更新后功能完整", id)
		}
		if state != "READY" {
			return fmt.Errorf("核心模块 %s 状态为 %s", id, state)
		}
	}
	return nil
}

func updateFile(source, destination string, mode os.FileMode) error {
	return platform.RetryFileOperation(func() error { return atomicCopy(source, destination, mode) })
}

func removeUpdateFile(path string) error {
	return platform.RetryFileOperation(func() error {
		if err := os.Remove(path); !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}
