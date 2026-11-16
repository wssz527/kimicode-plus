package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"enhancedkimi/internal/desktop"
)

// attachOptions is the argument set launchd runs. It mirrors the flags the
// LaunchAgent plist renders so a manual run and an agent run behave identically.
type attachOptions struct {
	dataDir            string
	kimiHome           string
	profile            string
	allowDailyInstance bool
	coreURL            string
	pairingToken       string
	install            bool
	uninstall          bool
	status             bool
}

// runAttach implements `enhanced-kimi attach`. Without --install/--uninstall it
// performs one attach attempt, which is exactly what the WatchPaths agent does
// per event: no process polling anywhere.
func runAttach(args []string) error {
	options := attachOptions{}
	set := flag.NewFlagSet("attach", flag.ContinueOnError)
	set.StringVar(&options.dataDir, "data-dir", "", "independent data directory")
	set.StringVar(&options.kimiHome, "kimi-home", "", "KIMI_CODE_HOME of the host")
	set.StringVar(&options.profile, "host-profile", "", "isolated Kimi Code profile (empty: the running client)")
	set.BoolVar(&options.allowDailyInstance, "allow-daily-instance", false, "attach to the client you actually use")
	set.StringVar(&options.coreURL, "core-url", "", "core base URL to report to")
	set.StringVar(&options.pairingToken, "pairing-token", "", "core pairing token")
	set.BoolVar(&options.install, "install", false, "install and load the WatchPaths LaunchAgent")
	set.BoolVar(&options.uninstall, "uninstall", false, "unload and remove the LaunchAgent")
	set.BoolVar(&options.status, "status", false, "print the current attach state")
	if err := set.Parse(args); err != nil {
		return err
	}
	if options.status {
		state := desktop.LaunchAgentStatus()
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
		// The installed agent runs this same subcommand with no other input, so it
		// needs the same credentials a manual run gets.
		CoreURL: options.coreURL, PairingToken: options.pairingToken,
	}
	switch {
	case options.uninstall:
		removed, err := desktop.UninstallLaunchAgent()
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
	result, attachErr := desktop.AttachOnce(ctx, desktop.AttachOptions{
		Profile: options.profile, CoreURL: options.coreURL, PairingToken: options.pairingToken,
		AllowDailyInstance: options.allowDailyInstance,
	})
	recordErr := desktop.RecordAttach(options.dataDir, anyOrError(result, attachErr))
	if attachErr != nil {
		return attachErr
	}
	if recordErr != nil {
		fmt.Fprintln(os.Stderr, "接入结果未能写入状态文件：", recordErr)
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

// installLaunchAgent is the install step, indirected so a test can prove
// --install validates before it writes anything without touching the real
// LaunchAgents directory.
var installLaunchAgent = desktop.InstallLaunchAgent

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
	set.BoolVar(&options.allowDailyInstance, "allow-daily-instance", false, "")
	set.StringVar(&options.coreURL, "core-url", "", "")
	set.StringVar(&options.pairingToken, "pairing-token", "", "")
	if err := set.Parse(args[1:]); err != nil {
		return fmt.Errorf("后台接入参数无法解析：%w", err)
	}
	if options.dataDir == "" {
		return errors.New("后台接入缺少 --data-dir")
	}
	if options.coreURL == "" {
		return errors.New("后台接入缺少 --core-url，重启后无法向核心回报")
	}
	if len(options.pairingToken) < desktop.AttachTokenMinLength {
		return errors.New("后台接入缺少 --pairing-token，重启后无法通过核心鉴权")
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
