package plugins

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// gatedDefinition lets a test observe the activation as genuinely in-flight and
// decide, per attempt, whether that attempt finishes fine or fails late.
type gatedAttempt struct {
	started chan struct{}
	release chan struct{}
	fail    bool
}

func gatedDef(id string, attempts []*gatedAttempt, activations *int32, resources *int32) *definition {
	var next atomic.Int32
	return &definition{
		descriptor: Descriptor{ID: id, Name: id, Version: "1", Builtin: true, Sandbox: "IN_PROCESS_SIMULATION"},
		activate: func(scope *Scope) error {
			atomic.AddInt32(activations, 1)
			index := int(next.Add(1)) - 1
			if index >= len(attempts) {
				scope.Own("timer", func() error { atomic.AddInt32(resources, -1); return nil })
				return nil
			}
			attempt := attempts[index]
			attempt.started <- struct{}{}
			<-attempt.release
			scope.Own("timer", func() error { atomic.AddInt32(resources, -1); return nil })
			scope.Own("connection", func() error { atomic.AddInt32(resources, -1); return nil })
			atomic.AddInt32(resources, 2)
			if attempt.fail {
				return errors.New("late activation failure")
			}
			return nil
		},
	}
}

// A late failure from a cancelled activation must not overwrite the state of a
// newer, already-successful activation.
func TestLateFailureFromCancelledActivationDoesNotClobberNewerSuccess(t *testing.T) {
	manager, _ := newManager(t, nil)
	first := &gatedAttempt{started: make(chan struct{}, 1), release: make(chan struct{}), fail: true}
	second := &gatedAttempt{started: make(chan struct{}, 1), release: make(chan struct{}), fail: false}
	var activations, resources int32
	install(manager, gatedDef("slow", []*gatedAttempt{first, second}, &activations, &resources))

	firstDone := make(chan error, 1)
	go func() {
		_, err := manager.Enable("slow")
		firstDone <- err
	}()
	<-first.started

	if err := manager.Disable("slow"); err != nil {
		t.Fatalf("disable: %v", err)
	}

	secondDone := make(chan error, 1)
	go func() {
		_, err := manager.Enable("slow")
		secondDone <- err
	}()
	<-second.started
	close(second.release)
	if err := <-secondDone; err != nil {
		t.Fatalf("second enable: %v", err)
	}
	enabled := pluginByID(t, manager, "slow")
	if enabled.State != StateEnabled || enabled.Resources != 2 {
		t.Fatalf("second activation not established: %#v", enabled)
	}
	faultsBefore := enabled.Faults

	// The first (cancelled) attempt now returns its late error.
	close(first.release)
	firstErr := <-firstDone
	var apiErr *APIError
	if !errors.As(firstErr, &apiErr) || apiErr.Code != "PLUGIN_ACTIVATION_CANCELLED" {
		t.Fatalf("stale activation must report cancellation, got %v", firstErr)
	}

	if got := atomic.LoadInt32(&activations); got != 2 {
		t.Fatalf("expected two activation attempts, got %d", got)
	}
	if got := atomic.LoadInt32(&resources); got != 2 {
		t.Fatalf("stale attempt leaked resources: %d", got)
	}
	after := pluginByID(t, manager, "slow")
	if after.State != StateEnabled {
		t.Fatalf("stale failure overwrote newer state: %s", after.State)
	}
	if !after.Enabled || after.Resources != 2 {
		t.Fatalf("newer activation resources disturbed: %#v", after)
	}
	if after.Faults != faultsBefore {
		t.Fatalf("stale failure incremented faults: %d -> %d", faultsBefore, after.Faults)
	}
	if strings.Contains(after.Error, "late activation failure") {
		t.Fatalf("stale error surfaced on the enabled plugin: %q", after.Error)
	}
}

// The same protection must hold when safe mode cancels the first attempt and
// safe mode is then lifted before the stale attempt fails.
func TestLateFailureAfterSafeModeCancellationKeepsNewerActivation(t *testing.T) {
	var safe atomic.Bool
	manager, _ := newManager(t, func() bool { return safe.Load() })
	first := &gatedAttempt{started: make(chan struct{}, 1), release: make(chan struct{}), fail: true}
	second := &gatedAttempt{started: make(chan struct{}, 1), release: make(chan struct{}), fail: false}
	var activations, resources int32
	install(manager, gatedDef("slow", []*gatedAttempt{first, second}, &activations, &resources))

	firstDone := make(chan error, 1)
	go func() {
		_, err := manager.Enable("slow")
		firstDone <- err
	}()
	<-first.started
	safe.Store(true)
	manager.EnforceSafeMode()
	safe.Store(false)

	secondDone := make(chan error, 1)
	go func() {
		_, err := manager.Enable("slow")
		secondDone <- err
	}()
	<-second.started
	close(second.release)
	if err := <-secondDone; err != nil {
		t.Fatalf("second enable after safe mode: %v", err)
	}

	close(first.release)
	var apiErr *APIError
	if err := <-firstDone; !errors.As(err, &apiErr) || apiErr.Code != "PLUGIN_ACTIVATION_CANCELLED" {
		t.Fatalf("stale activation must report cancellation, got %v", err)
	}
	after := pluginByID(t, manager, "slow")
	if after.State != StateEnabled || after.Resources != 2 || after.Faults != 0 {
		t.Fatalf("stale failure clobbered state after safe mode: %#v", after)
	}
}

// A failure with no newer activation must still be recorded as FAULTED, so the
// ownership check cannot silently swallow genuine faults.
func TestActivationFailureStillFaultsWhenItOwnsTheAttempt(t *testing.T) {
	manager, _ := newManager(t, nil)
	var activated int32
	install(manager, &definition{
		descriptor: Descriptor{ID: "bad", Name: "bad", Version: "1", Builtin: true, Sandbox: "IN_PROCESS_SIMULATION"},
		activate: func(scope *Scope) error {
			atomic.AddInt32(&activated, 1)
			scope.Own("timer", func() error { return nil })
			return errors.New("activation failed on purpose")
		},
	})
	_, err := manager.Enable("bad")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "PLUGIN_ACTIVATION_FAILED" {
		t.Fatalf("expected PLUGIN_ACTIVATION_FAILED, got %v", err)
	}
	plugin := pluginByID(t, manager, "bad")
	if plugin.State != StateFaulted || plugin.Faults != 1 || plugin.Resources != 0 {
		t.Fatalf("own failure not recorded: %#v", plugin)
	}
	if !strings.Contains(plugin.Error, "activation failed on purpose") {
		t.Fatalf("error lost: %q", plugin.Error)
	}
	if atomic.LoadInt32(&activated) != 1 {
		t.Fatalf("unexpected activation count")
	}
}

// A late failure after Close must not resurrect or fault a closed manager.
func TestLateFailureAfterCloseIsCancelled(t *testing.T) {
	manager, _ := newManager(t, nil)
	first := &gatedAttempt{started: make(chan struct{}, 1), release: make(chan struct{}), fail: true}
	var activations, resources int32
	install(manager, gatedDef("slow", []*gatedAttempt{first}, &activations, &resources))

	done := make(chan error, 1)
	go func() {
		_, err := manager.Enable("slow")
		done <- err
	}()
	<-first.started
	closed := make(chan error, 1)
	go func() { closed <- manager.Close() }()
	time.Sleep(10 * time.Millisecond)
	close(first.release)
	if err := <-done; err == nil {
		t.Fatalf("activation must be cancelled by close")
	}
	if err := <-closed; err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := atomic.LoadInt32(&resources); got != 0 {
		t.Fatalf("resources leaked after close: %d", got)
	}
	if after := manager.snapshot("slow"); after.Enabled || after.State == StateEnabled {
		t.Fatalf("closed manager let the attempt publish: %#v", after)
	}
}

// Concurrent Enable calls must not surface the stale failure to any waiter.
func TestConcurrentWaitersSeeCancellationNotStaleFault(t *testing.T) {
	manager, _ := newManager(t, nil)
	first := &gatedAttempt{started: make(chan struct{}, 1), release: make(chan struct{}), fail: true}
	second := &gatedAttempt{started: make(chan struct{}, 1), release: make(chan struct{}), fail: false}
	var activations, resources int32
	install(manager, gatedDef("slow", []*gatedAttempt{first, second}, &activations, &resources))

	var firstDone sync.WaitGroup
	firstDone.Add(1)
	go func() {
		defer firstDone.Done()
		_, _ = manager.Enable("slow")
	}()
	<-first.started
	if err := manager.Disable("slow"); err != nil {
		t.Fatalf("disable: %v", err)
	}

	results := make([]error, 4)
	var waiters sync.WaitGroup
	for i := range results {
		waiters.Add(1)
		go func(index int) {
			defer waiters.Done()
			_, results[index] = manager.Enable("slow")
		}(i)
	}
	<-second.started
	close(second.release)
	waiters.Wait()
	close(first.release)
	firstDone.Wait()

	for i, err := range results {
		if err != nil {
			t.Fatalf("waiter %d saw stale failure: %v", i, err)
		}
	}
	after := pluginByID(t, manager, "slow")
	if after.State != StateEnabled || after.Faults != 0 || after.Resources != 2 {
		t.Fatalf("stale failure clobbered waiter result: %#v", after)
	}
}
