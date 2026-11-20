package main

import (
	"context"
	"errors"
	"time"

	"enhancedkimi/internal/platform"
	"enhancedkimi/internal/updater"
)

func acquireCore(ctx context.Context, dir string, resident bool) (*platform.Instance, bool, error) {
	if !resident {
		return updater.AcquireCore(dir)
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		instance, supervised, err := updater.AcquireCore(dir)
		if !errors.Is(err, platform.ErrInstanceOwned) && !errors.Is(err, updater.ErrBusy) {
			return instance, supervised, err
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
	return nil, false, nil
}
