package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"enhancedkimi/internal/desktop"
	"enhancedkimi/internal/platform"
)

// attachOptions is the argument set launchd runs. It mirrors the flags the
// LaunchAgent plist renders so a manual run and an agent run behave identically.
type attachOptions struct {
	dataDir            string
	kimiHome           string
	kimiExe            string
	profile            string
	debugPort          int
	allowDailyInstance bool
	appVersionOverride string
	coreURL            string
	pairingToken       string
	install            bool
	uninstall          bool
	status             bool
	background         bool
}

// runAttach implements `enhanced-kimi attach`. Without --install/--uninstall it
// performs one attach attempt, which is exactly what the WatchPaths agent does
// per event: no process polling anywhere.
func runAttach(args []string) error {
	options := attachOptions{}
	set := flag.NewFlagSet("attach", flag.ContinueOnError)
	set.StringVar(&options.dataDir, "data-dir", "", "independent data directory")
	set.StringVar(&options.kimiHome, "kimi-home", "", "KIMI_CODE_HOME of the host")
	set.StringVar(&options.kimiExe, "kimi-exe", "", "explicit path to Kimi Code.exe on Windows")
	set.StringVar(&options.profile, "host-profile", "", "isolated Kimi Code profile (empty: the running client)")
	set.IntVar(&options.debugPort, "debug-port", 0, "optional loopback debugging port on Windows")
	set.BoolVar(&options.background, "background", false, "skip an existing attachment to the same running core")
	set.BoolVar(&options.allowDailyInstance, "allow-daily-instance", false, "attach to the client you actually use")
	set.StringVar(&options.appVersionOverride, "app-version-override", "", "override reported host version on isolated profile for contract acceptance")
	set.StringVar(&options.coreURL, "core-url", "", "core base URL to report to")
	set.StringVar(&options.pairingToken, "pairing-token", "", "core pairing token")
	set.BoolVar(&options.install, "install", false, "install login and auto-attach tasks")
	set.BoolVar(&options.uninstall, "uninstall", false, "remove login and auto-attach tasks")
	set.BoolVar(&options.status, "status", false, "print the current attach state")
	if err := set.Parse(args); err != nil {
		return err
	}
	if options.dataDir == "" && (options.status || options.uninstall) {
		dir, err := platform.DefaultDataDir()
		if err != nil {
			return err
		}
		options.dataDir = dir
	}
	if options.status {
		state := desktop.AutoAttachStatus(options.dataDir)
		payload := map[string]any{"agent": state, "last": desktop.ReadAttachState(options.dataDir)}
		return json.NewEncoder(os.Stdout).Encode(payload)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	config := desktop.LaunchAgentConfig{
		DataDir: options.dataDir, KimiHome: options.kimiHome, Profile: options.profile,
		AllowDailyInstance: options.allowDailyInstance, Executable: executable,
	}
	switch {
	case options.uninstall:
		removed, err := desktop.DisableAutoAttach(options.dataDir)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"agent": removed})
	case options.install:
		// Validate before writing: an installed agent whose command cannot run is
		// worse than no agent, because the settings surface reports it as enabled
		// while every wake-up fails. The same argument builder renders the plist,
		// so a passing check here is the contract the installed agent will run.
		if err = validateAgentArguments(desktop.AttachArguments(config)); err != nil {
			return fmt.Errorf("后台接入参数校验失败，未写入接入定义：%w", err)
		}
		if config.KimiHome == "" {
			config.KimiHome = os.Getenv("KIMI_CODE_HOME")
			if config.KimiHome == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				config.KimiHome = filepath.Join(home, ".kimi-code")
			}
		}
		installed, err := installLaunchAgent(config)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"agent": installed})
	}
	if err = validateAgentArguments(desktop.AttachArguments(config)); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := validateAttachCredentials(options.coreURL, options.pairingToken); err != nil {
		return err
	}
	coreURL, pairingToken := resolveAttachCredentials(ctx, options.dataDir, options.coreURL, options.pairingToken)
	if coreURL == "" {
		wait, stop := context.WithTimeout(ctx, 6*time.Second)
		defer stop()
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for coreURL == "" && wait.Err() == nil {
			select {
			case <-wait.Done():
			case <-ticker.C:
				coreURL, pairingToken = resolveAttachCredentials(wait, options.dataDir, "", "")
			}
		}
		if coreURL == "" {
			return errors.New("增强核心尚未就绪，待核心启动后重试接入")
		}
	}
	result, attachErr := desktop.AttachOnce(ctx, desktop.AttachOptions{
		DataDir: options.dataDir, KimiExecutable: options.kimiExe, DebugPort: options.debugPort,
		Profile: options.profile, CoreURL: coreURL, PairingToken: pairingToken,
		AllowDailyInstance: options.allowDailyInstance,
		AppVersionOverride: options.appVersionOverride,
		SkipCurrent:        options.background,
	})
	if result == nil && attachErr == nil {
		return nil
	}
	recordErr := desktop.RecordAttach(options.dataDir, anyOrError(result, attachErr))
	if attachErr != nil {
		return attachErr
	}
	if recordErr != nil {
		fmt.Fprintln(os.Stderr, "接入结果未能写入状态文件：", recordErr)
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func resolveAttachCredentials(ctx context.Context, dataDir, fallbackURL, fallbackToken string) (string, string) {
	if dataDir == "" {
		return fallbackURL, fallbackToken
	}
	data, err := os.ReadFile(filepath.Join(dataDir, "runtime.json"))
	if err != nil {
		return fallbackURL, fallbackToken
	}
	var runtimeInfo struct {
		URL          string `json:"url"`
		PairingToken string `json:"pairingToken"`
	}
	if json.Unmarshal(data, &runtimeInfo) != nil {
		return fallbackURL, fallbackToken
	}
	liveURL := strings.TrimRight(strings.TrimSpace(runtimeInfo.URL), "/")
	liveToken := strings.TrimSpace(runtimeInfo.PairingToken)
	if !isLoopbackHTTPURL(liveURL) || len(liveToken) < desktop.AttachTokenMinLength {
		return fallbackURL, fallbackToken
	}
	if liveURL == strings.TrimRight(strings.TrimSpace(fallbackURL), "/") && liveToken == strings.TrimSpace(fallbackToken) {
		return fallbackURL, fallbackToken
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, liveURL+"/enhance/v1/health", nil)
	if err != nil {
		return fallbackURL, fallbackToken
	}
	req.Header.Set("Authorization", "Bearer "+liveToken)
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return fallbackURL, fallbackToken
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fallbackURL, fallbackToken
	}
	return liveURL, liveToken
}

func isLoopbackHTTPURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Port() == "" ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return false
	}
	host := parsed.Hostname()
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// installLaunchAgent is the install step, indirected so a test can prove
// --install validates before it writes anything without touching the real
// LaunchAgents directory.
var installLaunchAgent = desktop.EnableAutoAttach

// validateAgentArguments parses the agent's argument list with the same flag set
// the subcommand uses. This is the guard against the plist and the CLI drifting
// apart: if a rendered agent could not run, installing it fails instead of
// silently producing an agent that errors on every wake-up.
func validateAgentArguments(args []string) error {
	if len(args) == 0 || args[0] != "attach" {
		return errors.New("后台接入参数必须以 attach 子命令开头")
	}
	options := attachOptions{}
	set := flag.NewFlagSet("attach", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.StringVar(&options.dataDir, "data-dir", "", "")
	set.StringVar(&options.kimiHome, "kimi-home", "", "")
	set.StringVar(&options.profile, "host-profile", "", "")
	set.BoolVar(&options.background, "background", false, "skip an existing attachment to the same running core")
	set.BoolVar(&options.allowDailyInstance, "allow-daily-instance", false, "")
	set.StringVar(&options.coreURL, "core-url", "", "")
	set.StringVar(&options.pairingToken, "pairing-token", "", "")
	if err := set.Parse(args[1:]); err != nil {
		return fmt.Errorf("后台接入参数无法解析：%w", err)
	}
	if options.dataDir == "" {
		return errors.New("后台接入缺少 --data-dir")
	}
	return validateAttachCredentials(options.coreURL, options.pairingToken)
}

func validateAttachCredentials(coreURL, token string) error {
	if coreURL == "" && token == "" {
		return nil
	}
	if !isLoopbackHTTPURL(strings.TrimRight(coreURL, "/")) {
		return errors.New("请提供本机回环的 --core-url，或让接入器从数据目录读取当前核心地址")
	}
	if len(token) < desktop.AttachTokenMinLength {
		return errors.New("提供核心地址时需要有效的 --pairing-token")
	}
	return nil
}

// anyOrError lets RecordAttach receive either branch without a nil-interface trap.
func anyOrError(result *desktop.AttachResult, err error) any {
	if err != nil {
		return err
	}
	return result
}
