package server

import (
	"database/sql"

	"enhancedkimi/internal/desktop"
)

// NativeOptions now carries the LaunchAgent settings in NativeOptions itself.
type AttachSurface struct{ Ready bool }

func newNativeIntegration(db *sql.DB, options NativeOptions, safe func() bool) (*desktop.NativeService, error) {
	service, err := desktop.NewNativeService(db, desktop.NewKAPClient(options.Home), options.CWD, safe)
	if err != nil {
		return nil, err
	}
	service.UseAttach(desktop.AttachConfig{
		DataDir: options.DataDir, KimiHome: options.Home, Profile: options.Profile,
		CoreURL: options.CoreURL, PairingToken: options.PairingToken, Executable: options.Executable,
	})
	return service, nil
}
