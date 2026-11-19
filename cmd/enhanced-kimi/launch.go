package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"enhancedkimi/internal/desktop"
	"enhancedkimi/internal/platform"
)

// runLaunch implements `enhanced-kimi launch`, the fixed-path Windows launcher
// entry point that starts or reuses the enhancement core, launches Kimi Code
// with a loopback-only debugging port, verifies the host process and port owner,
// and injects the shared renderer enhancement over Chromium CDP.
func runLaunch(args []string) error {
	set := flag.NewFlagSet("launch", flag.ContinueOnError)
	dataDir := set.String("data-dir", "", "independent data directory (default: OS user config/EnhancedKimiDesktop)")
	kimiHome := set.String("kimi-home", "", "KIMI_CODE_HOME of the host")
	kimiExe := set.String("kimi-exe", "", "explicit path to Kimi Code.exe on Windows")
	profile := set.String("host-profile", "", "isolated Kimi Code profile (empty: daily instance)")
	debugPort := set.Int("debug-port", 0, "optional fixed loopback debugging port (0 selects a free port)")
	allowDaily := set.Bool("allow-daily-instance", false, "attach to the client you actually use")
	appVersionOverride := set.String("app-version-override", "", "override reported host version on isolated profile")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *debugPort < 0 || *debugPort > 65535 {
		return fmt.Errorf("debug-port out of range")
	}
	if *dataDir == "" {
		dir, err := platform.DefaultDataDir()
		if err != nil {
			return err
		}
		*dataDir = dir
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result, err := desktop.RunWindowsLauncherPlatform(ctx, desktop.WindowsLaunchOptions{
		DataDir:            *dataDir,
		KimiHome:           *kimiHome,
		KimiExecutable:     *kimiExe,
		Executable:         executable,
		Profile:            *profile,
		DebugPort:          *debugPort,
		AllowDailyInstance: *allowDaily,
		AppVersionOverride: *appVersionOverride,
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}
