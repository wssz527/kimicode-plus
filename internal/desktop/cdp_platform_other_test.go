//go:build !darwin && !windows

package desktop

import (
	"context"
	"errors"
	"os"
	"testing"
)

// TestUnsupportedPlatformRefusesWithoutSignalling covers the compile-only
// targets: every host runtime entry point must fail closed with the support
// boundary instead of signalling a process or reporting a successful attach.
func TestUnsupportedPlatformRefusesWithoutSignalling(t *testing.T) {
	if hostPlatformSupported {
		t.Fatal("non-macOS hosts have no verified runtime path")
	}
	ctx := context.Background()
	if _, err := FindMainPID(ctx, ""); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("host discovery did not refuse: %v", err)
	}
	if _, err := Attach(ctx, os.Getpid()); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("attach did not refuse: %v", err)
	}
	if err := VerifyLiveProcess(ctx, ProcessInfo{PID: os.Getpid()}); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("live process verification did not refuse: %v", err)
	}
	if err := verifyRuntimeImage(ctx, ProcessInfo{PID: os.Getpid(), Executable: "absent"}); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("runtime image check did not refuse: %v", err)
	}
	// Must return without killing the process running the test.
	if err := activateInspector(os.Getpid()); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("inspector activation did not refuse: %v", err)
	}
}

func TestAttachStatusReportsTheSupportBoundary(t *testing.T) {
	service := &NativeService{}
	status := service.attachStatus(context.Background())
	if status["available"] != false {
		t.Fatalf("background attach was offered on an unsupported platform: %+v", status)
	}
	if reason, _ := status["unavailableReason"].(string); reason != ErrUnsupportedPlatform.Error() {
		t.Fatalf("support boundary is not explained: %q", reason)
	}
	host, _ := status["host"].(map[string]any)
	if host["running"] != false {
		t.Fatalf("a host was reported as running: %+v", host)
	}
	if _, err := service.enableAttach(context.Background(), map[string]any{"allowDailyInstance": true}); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("agent install did not refuse: %v", err)
	}
}
