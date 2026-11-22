package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"enhancedkimi/internal/platform"
)

const (
	prefsName                    = "update-prefs.json"
	defaultAutoCheckInitialDelay = 15 * time.Second
	defaultAutoCheckInterval     = 4 * time.Hour
)

type updatePrefs struct {
	AutoCheck *bool `json:"autoCheck"`
}

// A background check that cannot reach the update service must not replace a
// useful or quiet status with a transient error badge.
func preservedOnSilentFailure(state string) bool {
	switch state {
	case Idle, UpToDate, Available, Updated, RolledBack:
		return true
	}
	return false
}

func readAutoCheck(directory string) bool {
	if directory == "" {
		return true
	}
	var prefs updatePrefs
	data, err := os.ReadFile(filepath.Join(directory, prefsName))
	if err != nil || json.Unmarshal(data, &prefs) != nil || prefs.AutoCheck == nil {
		return true
	}
	return *prefs.AutoCheck
}

func writeAutoCheck(directory string, enabled bool) error {
	if directory == "" {
		return nil
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("无法创建更新偏好目录：%w", err)
	}
	data, err := json.Marshal(updatePrefs{AutoCheck: &enabled})
	if err != nil {
		return err
	}
	if err := platform.AtomicPrivateFile(filepath.Join(directory, prefsName), data); err != nil {
		return fmt.Errorf("无法保存更新偏好：%w", err)
	}
	return nil
}

func (m *Manager) SetAutoCheck(enabled bool) (Status, error) {
	previous := m.Status().AutoCheck
	if err := writeAutoCheck(m.options.DataDir, enabled); err != nil {
		return m.Status(), err
	}
	m.change(func(s *Status) { s.AutoCheck = enabled })
	m.announce()
	if enabled && !previous {
		m.wakeAutoCheck()
	}
	return m.Status(), nil
}

func (m *Manager) wakeAutoCheck() {
	select {
	case m.autoCheckWake <- struct{}{}:
	default:
	}
}

func (m *Manager) StartAutoCheck() {
	m.autoCheckOnce.Do(func() {
		m.autoCheckDone = make(chan struct{})
		go m.runAutoCheck()
	})
}

func (m *Manager) runAutoCheck() {
	defer close(m.autoCheckDone)
	timer := time.NewTimer(m.autoCheckInitialDelay())
	defer timer.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.autoCheckWake:
			m.autoCheckNow(true)
		case <-timer.C:
			m.autoCheckNow(false)
		}
		resetAutoCheckTimer(timer, m.autoCheckInterval())
	}
}

func resetAutoCheckTimer(timer *time.Timer, wait time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(wait)
}

func (m *Manager) autoCheckInitialDelay() time.Duration {
	if m.options.AutoCheckInitialDelay > 0 {
		return m.options.AutoCheckInitialDelay
	}
	return defaultAutoCheckInitialDelay
}

func (m *Manager) autoCheckInterval() time.Duration {
	if m.options.AutoCheckInterval > 0 {
		return m.options.AutoCheckInterval
	}
	return defaultAutoCheckInterval
}

func (m *Manager) autoCheckAllowed(force bool) bool {
	if m.configErr != nil || m.options.Safe() {
		return false
	}
	status := m.Status()
	if !status.AutoCheck {
		return false
	}
	switch status.State {
	case Checking, Available, Downloading, Verifying, Restarting:
		return false
	}
	m.mu.RLock()
	last := m.lastCheck
	m.mu.RUnlock()
	return force || last.IsZero() || m.options.Now().Sub(last) >= m.autoCheckInterval()/2
}

func (m *Manager) autoCheckNow(force bool) {
	if !m.autoCheckAllowed(force) {
		return
	}
	_, _ = m.checkSilent(m.ctx)
}

func (m *Manager) checkSilent(ctx context.Context) (Status, error) {
	if err := m.begin(); err != nil {
		return m.Status(), err
	}
	defer m.operation.Unlock()
	m.mu.RLock()
	enabled := m.status.AutoCheck
	m.mu.RUnlock()
	if !enabled {
		return m.Status(), nil
	}
	return m.checkInternal(ctx, true)
}
