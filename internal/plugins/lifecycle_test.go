package plugins

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// blocking definition: activation parks until release is closed, so a test can
// act while the plugin is genuinely ENABLING.
func blockingDefinition(id string, started chan<- struct{}, release <-chan struct{}, activated *int32) *definition {
	return &definition{
		descriptor: Descriptor{ID: id, Name: id, Version: "1", Builtin: true, Sandbox: "IN_PROCESS_SIMULATION"},
		activate: func(scope *Scope) error {
			atomic.AddInt32(activated, 1)
			started <- struct{}{}
			<-release
			scope.Own("timer", func() error { return nil })
			scope.Own("connection", func() error { return nil })
			return nil
		},
	}
}

func install(m *Manager, def *definition) {
	m.defs[def.descriptor.ID] = def
	m.order = append(m.order, def.descriptor.ID)
}

// A concurrent enable must not build a second resource set while the first is
// still activating; it waits for the same attempt.
func TestConcurrentEnableDuringActivationJoinsSingleAttempt(t *testing.T) {
	manager, _ := newManager(t, nil)
	var activated int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	install(manager, blockingDefinition("slow", started, release, &activated))

	var wg sync.WaitGroup
	results := make([]error, 6)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, results[0] = manager.Enable("slow")
	}()
	<-started
	for i := 1; i < 6; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, results[index] = manager.Enable("slow")
		}(i)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&activated); got != 1 {
		t.Fatalf("activation ran %d times, want 1", got)
	}
	for i, err := range results {
		if err != nil {
			t.Fatalf("caller %d errored: %v", i, err)
		}
	}
	plugin := pluginByID(t, manager, "slow")
	if plugin.Resources != 2 {
		t.Fatalf("resource set duplicated: %d", plugin.Resources)
	}
}

// Disable while activating must cancel the attempt: a late activation cannot
// publish ENABLED or leave resources behind.
func TestDisableDuringActivationCancelsAndNeverRevives(t *testing.T) {
	manager, _ := newManager(t, nil)
	var activated int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	install(manager, blockingDefinition("slow", started, release, &activated))

	done := make(chan error, 1)
	go func() {
		_, err := manager.Enable("slow")
		done <- err
	}()
	<-started
	if err := manager.Disable("slow"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	close(release)
	err := <-done
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "PLUGIN_ACTIVATION_CANCELLED" {
		t.Fatalf("expected cancellation, got %v", err)
	}
	plugin := pluginByID(t, manager, "slow")
	if plugin.Enabled || plugin.Resources != 0 || plugin.State == StateEnabled {
		t.Fatalf("cancelled activation revived: %#v", plugin)
	}
}

// Safe mode reaching the manager mid-activation must stop it as well.
func TestSafeModeDuringActivationCancels(t *testing.T) {
	var safe atomic.Bool
	manager, _ := newManager(t, func() bool { return safe.Load() })
	var activated int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	install(manager, blockingDefinition("slow", started, release, &activated))

	done := make(chan error, 1)
	go func() {
		_, err := manager.Enable("slow")
		done <- err
	}()
	<-started
	safe.Store(true)
	manager.EnforceSafeMode()
	close(release)
	if err := <-done; err == nil {
		t.Fatalf("activation should be cancelled by safe mode")
	}
	plugin := pluginByID(t, manager, "slow")
	if plugin.Enabled || plugin.Resources != 0 {
		t.Fatalf("safe mode let activation publish: %#v", plugin)
	}

	safe.Store(false)
	if _, err := manager.Enable("slow"); err != nil {
		t.Fatalf("safe mode must be reversible: %v", err)
	}
	enabled := pluginByID(t, manager, "slow")
	if !enabled.Enabled || enabled.Resources != 2 {
		t.Fatalf("recovery after safe mode failed: %#v", enabled)
	}
}

// Close during activation is terminal: nothing may start afterwards.
func TestCloseDuringActivationStaysClosed(t *testing.T) {
	manager, _ := newManager(t, nil)
	var activated int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	install(manager, blockingDefinition("slow", started, release, &activated))

	done := make(chan error, 1)
	go func() {
		_, err := manager.Enable("slow")
		done <- err
	}()
	<-started
	closed := make(chan error, 1)
	go func() { closed <- manager.Close() }()
	time.Sleep(10 * time.Millisecond)
	close(release)
	if err := <-done; err == nil {
		t.Fatalf("activation must be cancelled by close")
	}
	if err := <-closed; err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := manager.Enable("slow"); err == nil {
		t.Fatalf("enable after close must fail")
	}
}

// One panicking disposer must not skip the remaining cleanup.
func TestPanickingDisposerDoesNotStopRemainingCleanup(t *testing.T) {
	manager, _ := newManager(t, nil)
	var released int32
	install(manager, &definition{
		descriptor: Descriptor{ID: "panicky", Name: "panicky", Version: "1", Builtin: true, Sandbox: "IN_PROCESS_SIMULATION"},
		activate: func(scope *Scope) error {
			scope.Own("first", func() error { atomic.AddInt32(&released, 1); return nil })
			scope.Own("boom", func() error { panic("disposer exploded") })
			scope.Own("last", func() error { atomic.AddInt32(&released, 1); return nil })
			return nil
		},
	})
	if _, err := manager.Enable("panicky"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if err := manager.Disable("panicky"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if got := atomic.LoadInt32(&released); got != 2 {
		t.Fatalf("remaining disposers skipped: %d of 2 ran", got)
	}
	plugin := pluginByID(t, manager, "panicky")
	if plugin.Resources != 0 {
		t.Fatalf("resources left: %#v", plugin)
	}
	if !containsText(plugin.Error, "disposer panic isolated") {
		t.Fatalf("panic not recorded: %q", plugin.Error)
	}
}

// Scope itself must isolate errors and panics in order, and keep the count at
// zero afterwards.
func TestScopeDisposeIsolatesEveryDisposer(t *testing.T) {
	var order []string
	scope := &Scope{}
	scope.Own("a", func() error { order = append(order, "a"); return nil })
	scope.Own("b", func() error { order = append(order, "b"); panic("b failed") })
	scope.Own("c", func() error { order = append(order, "c"); return errors.New("c failed") })
	scope.Own("d", func() error { order = append(order, "d"); return nil })

	failures := scope.Dispose()
	if len(order) != 4 {
		t.Fatalf("disposers skipped: %v", order)
	}
	if order[0] != "d" || order[3] != "a" {
		t.Fatalf("not reverse order: %v", order)
	}
	if len(failures) != 2 {
		t.Fatalf("expected two failures, got %v", failures)
	}
	if scope.Count() != 0 {
		t.Fatalf("scope not cleared")
	}
	scope.Own("late", func() error { return nil })
	if scope.Count() != 0 {
		t.Fatalf("own after dispose must not accumulate")
	}
}

// Registration after a cancelled activation must still be cleaned immediately,
// not leaked into a dead scope.
func TestOwnAfterDisposeRunsCleanupImmediately(t *testing.T) {
	var cleaned int32
	scope := &Scope{}
	scope.Dispose()
	scope.Own("late", func() error { atomic.AddInt32(&cleaned, 1); return nil })
	if got := atomic.LoadInt32(&cleaned); got != 1 {
		t.Fatalf("late resource not released: %d", got)
	}
	if scope.Count() != 0 {
		t.Fatalf("closed scope accumulated resources")
	}
}

// EnforceSafeMode must be reversible and Close must be terminal.
func TestEnforceSafeModeReversibleCloseTerminal(t *testing.T) {
	var safe atomic.Bool
	manager, _ := newManager(t, func() bool { return safe.Load() })
	if _, err := manager.Enable("kct"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	safe.Store(true)
	manager.EnforceSafeMode()
	stopped := pluginByID(t, manager, "kct")
	if stopped.Enabled || stopped.Resources != 0 || stopped.State != StateBlocked {
		t.Fatalf("safe mode did not stop plugin: %#v", stopped)
	}

	safe.Store(false)
	manager.EnforceSafeMode()
	recovered, err := manager.Enable("kct")
	if err != nil {
		t.Fatalf("safe mode must be reversible: %v", err)
	}
	if !recovered.Enabled || recovered.Resources != 3 {
		t.Fatalf("recovery failed: %#v", recovered)
	}

	if err := manager.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	after, err := manager.Enable("kct")
	if err == nil {
		t.Fatalf("close must be terminal, got %#v", after)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "MANAGER_CLOSED" {
		t.Fatalf("expected MANAGER_CLOSED, got %v", err)
	}
}

// Health must report zero processes and one resource set per enabled plugin.
func TestHealthCountsRemainExact(t *testing.T) {
	manager, _ := newManager(t, nil)
	if _, err := manager.Enable("kct"); err != nil {
		t.Fatalf("enable kct: %v", err)
	}
	if _, err := manager.Enable("monitor"); err != nil {
		t.Fatalf("enable monitor: %v", err)
	}
	health := manager.Health(context.Background())
	if health["enabled"] != 2 || health["resources"] != 6 || health["processes"] != 0 {
		t.Fatalf("unexpected health: %#v", health)
	}
}

func containsText(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
