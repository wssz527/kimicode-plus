package updater

import (
	"context"
	"crypto/sha256"
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
	coreTimeout  = 30 * time.Second
)

type supervisorJob struct {
	Executable      string
	DataDir         string
	WorkingDir      string
	Args            []string
	OldPID          int
	Ticket          string
	PreviousVersion string
	TargetVersion   string
	Restart         RestartState
	HasNotices      bool
	HasLicense      bool
}

func (m *Manager) prepareSupervisor(stage, target string, files extractedFiles) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	targetExe := m.targetExecutable()
	if targetExe != m.options.Executable {
		current, err := executableDigest(m.options.Executable)
		if err != nil {
			return err
		}
		installed, err := executableDigest(targetExe)
		if err != nil {
			return err
		}
		if current != installed {
			return errors.New("正在运行的核心与登录项中的副本不同，请从登录项启动核心后再更新")
		}
	}
	job := supervisorJob{
		Executable: targetExe, DataDir: m.options.DataDir, WorkingDir: cwd,
		Args: os.Args[1:], OldPID: os.Getpid(), Ticket: security.Random(), PreviousVersion: m.options.CurrentVersion,
		TargetVersion: target, Restart: m.options.RestartState(), HasNotices: files.notices != "", HasLicense: files.license != "",
	}
	if job.Restart.Port < 1 || job.Restart.Port > 65535 {
		return errors.New("无法保存核心当前的监听端口")
	}
	jobPath := filepath.Join(stage, jobName)
	if err = writeUpdateJSON(jobPath, job); err != nil {
		return err
	}
	helper := filepath.Join(stage, "supervisor")
	if m.options.GOOS == "windows" {
		helper += ".exe"
	}
	if err = atomicCopy(m.options.Executable, helper, 0o755); err != nil {
		return err
	}
	if err = m.options.launchSupervisor(helper, jobPath); err != nil {
		if result := readResult(job.DataDir); result.State == Restarting && result.Ticket == job.Ticket {
			return errors.Join(err, result.finish(job.DataDir, Failed, err.Error()))
		}
	}
	return err
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

func (job supervisorJob) supervise(stage string) error {
	defer cleanupSupervisor(stage)
	waitForParent, closeParent, err := watchParent(job.OldPID)
	if err != nil {
		return err
	}
	defer closeParent()
	for _, path := range []string{startupLockPath(job.DataDir), job.Executable + ".update.lock"} {
		lock := flock.New(path)
		defer lock.Close()
		locked, err := lock.TryLock()
		if err != nil {
			return fmt.Errorf("无法锁定核心更新：%w", err)
		}
		if !locked {
			return ErrBusy
		}
	}
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
	return job.replaceAndRestart(stage, receipt)
}

func (job supervisorJob) replaceAndRestart(stage string, receipt updateReceipt) error {
	backup := job.Executable + ".bak"
	if err := updateFile(job.Executable, backup, 0o755); err != nil {
		return job.rollback(receipt, "", fmt.Errorf("无法备份旧核心：%w", err))
	}
	if err := updateFile(filepath.Join(stage, "binary"), job.Executable, 0o755); err != nil {
		return job.rollback(receipt, backup, fmt.Errorf("无法安装新版本核心：%w", err))
	}
	core, err := job.startCore(receipt.Ticket)
	if err == nil {
		err = job.waitForHealth(core, job.TargetVersion)
	}
	if err == nil {
		err = receipt.finish(job.DataDir, Updated, "")
		if err == nil {
			warnings := job.installReleaseLicenses(stage)
			return job.cleanUpBackup(receipt, Updated, "", backup, warnings)
		}
		err = fmt.Errorf("无法记录更新结果：%w", err)
	}
	if core != nil {
		if stopErr := core.stop(); stopErr != nil {
			return receipt.finish(job.DataDir, Failed, fmt.Sprintf("更新失败（%v），无法结束新核心：%v", err, stopErr))
		}
	}
	return job.rollback(receipt, backup, err)
}

func (job supervisorJob) rollback(receipt updateReceipt, backup string, cause error) error {
	if backup != "" {
		if err := updateFile(backup, job.Executable, 0o755); err != nil {
			return receipt.finish(job.DataDir, Failed, fmt.Sprintf("更新失败（%v），无法还原旧核心文件：%v", cause, err))
		}
	}
	core, err := job.startCore(receipt.Ticket)
	if err == nil {
		err = job.waitForHealth(core, job.PreviousVersion)
		if err != nil {
			err = errors.Join(err, core.stop())
		}
	}
	if err != nil {
		return receipt.finish(job.DataDir, Failed, fmt.Sprintf("更新失败（%v），旧核心未恢复运行：%v", cause, err))
	}
	reason := fmt.Sprintf("更新失败，已回滚：%v", cause)
	if err := receipt.finish(job.DataDir, RolledBack, reason); err != nil {
		return err
	}
	return job.cleanUpBackup(receipt, RolledBack, reason, backup, nil)
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

func (job supervisorJob) cleanUpBackup(receipt updateReceipt, state, reason, backup string, warnings []string) error {
	if backup != "" {
		if err := removeUpdateFile(backup); err != nil {
			warnings = append(warnings, fmt.Sprintf("无法删除旧核心备份：%v", err))
		}
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
	child := &runningCore{process: cmd.Process, done: make(chan struct{})}
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
}

func (core *runningCore) stop() error {
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
	return waitForCoreHealth(ctx, core, filepath.Join(job.DataDir, "runtime.json"), job.Restart.Port, version)
}

func waitForCoreHealth(ctx context.Context, core *runningCore, runtimePath string, port int, version string) error {
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport, Timeout: time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := checkCoreHealth(ctx, client, runtimePath, base, core.process.Pid, version)
		select {
		case <-core.done:
			if core.err != nil {
				return fmt.Errorf("核心在通过健康检查前退出：%w", core.err)
			}
			return errors.New("核心在通过健康检查前退出")
		default:
			if err == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待核心通过健康检查超时：%w", err)
		case <-core.done:
		case <-ticker.C:
		}
	}
}

func checkCoreHealth(ctx context.Context, client *http.Client, runtimePath, base string, pid int, version string) error {
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
	var health struct{ Status, Version string }
	if json.Unmarshal(body, &health) != nil || health.Status != "OK" || strings.TrimPrefix(health.Version, "v") != version {
		return fmt.Errorf("核心健康接口未确认目标版本 %s 正常运行", version)
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
