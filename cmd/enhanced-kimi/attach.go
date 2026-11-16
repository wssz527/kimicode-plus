package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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
	}
	switch {
	case options.uninstall:
		state, err := desktop.UninstallLaunchAgent()
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"agent": state})
	case options.install:
		state, err := desktop.InstallLaunchAgent(config)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"agent": state})
	}
	if options.coreURL == "" || len(options.pairingToken) < 32 {
		return errors.New("后台接入需要 --core-url 和 --pairing-token")
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

// anyOrError lets RecordAttach receive either branch without a nil-interface trap.
func anyOrError(result *desktop.AttachResult, err error) any {
	if err != nil {
		return err
	}
	return result
}
