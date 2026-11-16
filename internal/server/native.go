package server

import (
	"database/sql"

	"enhancedkimi/internal/desktop"
)

func newNativeIntegration(db *sql.DB, options NativeOptions, safe func() bool) (*desktop.NativeService, error) {
	return desktop.NewNativeService(db, desktop.NewKAPClient(options.Home), options.CWD, safe)
}
