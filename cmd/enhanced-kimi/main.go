package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"enhancedkimi/internal/desktop"
	"enhancedkimi/internal/gateway"
	"enhancedkimi/internal/platform"
	"enhancedkimi/internal/redact"
	"enhancedkimi/internal/security"
	"enhancedkimi/internal/server"
	"enhancedkimi/internal/storage"
	"enhancedkimi/internal/updater"
)

type redactedWriter struct{ out io.Writer }

func (w redactedWriter) Write(p []byte) (int, error) {
	_, err := io.WriteString(w.out, redact.Text(string(p)))
	return len(p), err
}
func main() {
	log.SetOutput(redactedWriter{os.Stderr})
	log.SetFlags(log.LstdFlags | log.LUTC)
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

const restartLeaseProbeTimeout = 3 * time.Second

// restartStateForCore decides how the restarted core reattaches. An installed
// attach record is replayed exactly as recorded. A Windows launcher lease is
// only trusted when the host it names is actually running: an expired lease must
// not widen consent to the daily instance.
func restartStateForCore(dataDir string, port int, hostProfile string, allowDaily bool) updater.RestartState {
	state := updater.RestartState{Port: port}
	last := desktop.ReadAttachState(dataDir)
	if last["status"] == "installed" {
		state.Reattach = true
		state.ReattachDaily = last["daily"] == true
	}
	if desktop.HostPlatformName() != "windows" {
		return state
	}
	lease := desktop.ReadWindowsLauncherState(dataDir)
	if lease == nil {
		return state
	}
	if dataDir != "" && desktop.AutoAttachStatus(dataDir).Enabled {
		state.Reattach = true
		state.ReattachDaily = state.ReattachDaily || last["daily"] == true
		return state
	}
	if !liveWindowsLauncherLease(dataDir, lease) {
		return state
	}
	// A live, matching lease means the launcher still owns the host. Daily
	// consent is only widened when this run was explicitly allowed to touch the
	// client the user is actually using.
	state.Reattach = true
	state.ReattachDaily = last["daily"] == true || (hostProfile == "" && allowDaily)
	return state
}

// liveWindowsLauncherLease confirms the lease still describes the running host:
// same PID, same loopback debugging port and not a debug instance.
func liveWindowsLauncherLease(dataDir string, lease *desktop.WindowsLauncherState) bool {
	ctx, cancel := context.WithTimeout(context.Background(), restartLeaseProbeTimeout)
	defer cancel()
	discovered, err := desktop.DiscoverHostWithDataDir(ctx, "", dataDir)
	if err != nil || discovered == nil {
		return false
	}
	running, _ := discovered["running"].(bool)
	pid, _ := discovered["pid"].(int)
	if !running || pid != lease.PID {
		return false
	}
	port, _ := discovered["debugPort"].(int)
	if port != lease.DebugPort {
		return false
	}
	debug, _ := discovered["debugInstance"].(bool)
	return !debug
}

// newHTTPServer keeps the process-wide budgets in one place. WriteTimeout is
// deliberately unset: a single server-wide value would cap generation, so the
// Deadlines handler owns the per-path write budget instead.
func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
}

func run() error {
	return runCore("", nil)
}

func runCore(updateAPIBaseURL string, updateHTTPClient *http.Client) error {
	if len(os.Args) == 3 && os.Args[1] == updater.SupervisorCommand {
		return updater.RunSupervisor(os.Args[2])
	}
	if len(os.Args) > 1 && os.Args[1] == "attach" {
		return runAttach(os.Args[2:])
	}
	if len(os.Args) > 1 && os.Args[1] == "launch" {
		return runLaunch(os.Args[2:])
	}
	dir := flag.String("data-dir", "", "independent data directory (default: OS user config/EnhancedKimiDesktop)")
	port := flag.Int("port", 0, "loopback port (0 selects an available port)")
	safe := flag.Bool("safe-mode", false, "persist safe mode and disable task/plugin effects")
	hostProfile := flag.String("host-profile", "", "isolated Kimi Code profile to attach")
	kimiHome := flag.String("kimi-home", "", "KIMI_CODE_HOME of the host")
	allowDaily := flag.Bool("allow-daily-instance", false, "attach to the client you actually use (requires explicit consent)")
	workspace := flag.String("workspace", "", "default planning workspace (defaults to the official session or process directory)")
	version := flag.Bool("version", false, "print version")
	resident := flag.Bool("resident", false, "wait for any existing core and run for the signed-in user")
	flag.Parse()
	if *version {
		fmt.Println(server.Version)
		return nil
	}
	if *port < 0 || *port > 65535 {
		return fmt.Errorf("port out of range")
	}
	if *hostProfile != "" && *kimiHome == "" {
		return fmt.Errorf("--host-profile requires --kimi-home")
	}
	effectiveKimiHome := *kimiHome
	if effectiveKimiHome == "" && *hostProfile == "" && *allowDaily {
		if envHome := os.Getenv("KIMI_CODE_HOME"); envHome != "" {
			effectiveKimiHome = envHome
		} else if userHome, homeErr := os.UserHomeDir(); homeErr == nil && userHome != "" {
			effectiveKimiHome = filepath.Join(userHome, ".kimi-code")
		}
	}
	if *dir == "" {
		defaultDir, err := platform.DefaultDataDir()
		if err != nil {
			return err
		}
		*dir = defaultDir
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	instance, supervised, err := acquireCore(ctx, *dir, *resident)
	if err != nil {
		return err
	}
	if instance == nil {
		return nil
	}
	defer instance.Close()
	runtimePath := filepath.Join(instance.Dir, "runtime.json")
	resume := updater.ReadRestartState(instance.Dir)
	listenPort := *port
	if listenPort == 0 {
		listenPort = resume.Port
	}
	runtimeWritten := false
	defer func() {
		if runtimeWritten {
			_ = os.Remove(runtimePath)
		}
	}()
	db, err := storage.Open(filepath.Join(instance.Dir, "enhancement.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", listenPort))
	if err != nil && !supervised && *port == 0 && resume.Port != 0 {
		listener, err = net.Listen("tcp4", "127.0.0.1:0")
	}
	if err != nil {
		return err
	}
	defer listener.Close()
	guard := security.New(listener.Addr().String())
	gatewayToken, err := gateway.LoadOrCreateToken(instance.Dir)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = instance.Dir
	}
	if *workspace != "" {
		if !filepath.IsAbs(*workspace) {
			return fmt.Errorf("--workspace must be an absolute path")
		}
		info, statErr := os.Stat(*workspace)
		if statErr != nil || !info.IsDir() {
			return fmt.Errorf("--workspace must be an existing directory")
		}
		cwd = filepath.Clean(*workspace)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	attachParent, stopAttach := context.WithCancel(ctx)
	defer stopAttach()
	restartRequested := make(chan struct{}, 1)
	app, err := server.NewWithNative(db, guard, *safe, gatewayToken, server.NativeOptions{
		Home: effectiveKimiHome, CWD: cwd, DataDir: instance.Dir, Executable: executable,
		Profile: *hostProfile, CoreURL: "http://" + listener.Addr().String(), PairingToken: guard.PairingToken(),
		UpdateAPIBaseURL: updateAPIBaseURL, UpdateHTTPClient: updateHTTPClient,
		EnableOSKeyStore:      os.Getenv("EKD_DISABLE_OS_KEYCHAIN") != "1",
		EnableAutoUpdateCheck: os.Getenv("EKD_DISABLE_AUTO_UPDATE_CHECK") != "1",
		RestartState: func() updater.RestartState {
			return restartStateForCore(instance.Dir, listener.Addr().(*net.TCPAddr).Port, *hostProfile, *allowDaily)
		},
		RequestRestart: func() {
			select {
			case restartRequested <- struct{}{}:
			default:
			}
			stopAttach()
		},
	})
	if err != nil {
		return err
	}
	defer app.Close()
	base := "http://" + listener.Addr().String()
	runtimeData := map[string]any{"url": base, "pairingToken": guard.PairingToken(), "pairingUrl": base + "/#pair=" + guard.PairingToken(), "gatewayUrl": base + "/v1", "pid": os.Getpid(), "version": server.Version}
	b, _ := json.MarshalIndent(runtimeData, "", "  ")
	if err = platform.AtomicPrivateFile(runtimePath, b); err != nil {
		return err
	}
	runtimeWritten = true
	if resume.Port != 0 {
		updater.ClearRestartState(instance.Dir)
	}
	httpServer := newHTTPServer(server.Deadlines{}.Handler(app.Handler))
	done := make(chan error, 1)
	go func() {
		e := httpServer.Serve(listener)
		if e == http.ErrServerClosed {
			e = nil
		}
		done <- e
	}()
	var hostResult *desktop.AttachResult
	hostError := ""
	if resume.Reattach || (!*resident && (*hostProfile != "" || *allowDaily)) {
		attachCtx, attachCancel := context.WithTimeout(attachParent, 90*time.Second)
		options := desktop.AttachOptions{DataDir: instance.Dir, Profile: *hostProfile, CoreURL: base, PairingToken: guard.PairingToken(), CoreVersion: server.Version, AllowDailyInstance: *allowDaily || resume.ReattachDaily, SkipCurrent: resume.Reattach}
		if resume.Reattach {
			hostResult, err = desktop.AttachOnce(attachCtx, options)
		} else {
			hostResult, err = desktop.AttachNative(attachCtx, options)
		}
		attachCancel()
		if err != nil {
			hostError = err.Error()
			log.Printf("native enhancement not attached: %s", hostError)
			// A stale installed record would otherwise keep claiming the interface
			// is attached after a restart that failed to reattach.
			if !desktop.IsAttachStateRecorded(err) && !desktop.IsAttachPersistFailed(err) {
				_ = desktop.RecordAttach(instance.Dir, err)
			}
		} else if hostResult != nil && !desktop.IsAttachStateRecordedResult(hostResult) {
			_ = desktop.RecordAttach(instance.Dir, hostResult)
		}
	}
	startup := map[string]any{"status": "READY", "url": base, "runtimeFile": runtimePath, "pairingToken": guard.PairingToken(), "gatewayUrl": base + "/v1", "version": server.Version}
	if hostResult != nil {
		startup["host"] = hostResult
	}
	if hostError != "" {
		startup["hostError"] = hostError
	}
	if !*resident {
		json.NewEncoder(os.Stdout).Encode(startup)
	}
	restarting := false
	select {
	case err = <-done:
		return err
	case <-restartRequested:
		restarting = true
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	// Stop event long-polling before shutting the server down: a pending Poll
	// otherwise holds its request for the full client timeout (8-10s), which is
	// exactly the delay that used to make every exit and updater restart wait.
	if app != nil {
		app.StopEventPolling()
	}
	if err = httpServer.Shutdown(shutdown); err != nil {
		_ = httpServer.Close()
		if !restarting {
			return err
		}
		log.Printf("forced HTTP shutdown before core restart: %v", err)
	}
	return <-done
}
