//go:build darwin

package desktop

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestInspectorSignalHelper is the target of TestActivateInspectorSignalsTheHost.
// It is a real process that traps SIGUSR1, so the test observes signal delivery
// instead of trusting that the syscall returned nil.
func TestInspectorSignalHelper(t *testing.T) {
	marker := os.Getenv("EKD_SIGNAL_MARKER")
	if marker == "" {
		t.Skip("helper process for TestActivateInspectorSignalsTheHost")
	}
	received := make(chan os.Signal, 1)
	signal.Notify(received, syscall.SIGUSR1)
	if err := os.WriteFile(marker+".ready", []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
		if err := os.WriteFile(marker, []byte("hit"), 0o600); err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("helper never received SIGUSR1")
	}
}

// TestActivateInspectorSignalsTheHost proves the macOS branch still delivers the
// SIGUSR1 an Electron host needs to open its fuse-gated inspector, and that the
// signal reaches a real trapping process rather than being reported as sent.
func TestActivateInspectorSignalsTheHost(t *testing.T) {
	if !hostPlatformSupported {
		t.Fatal("macOS must be a supported host platform")
	}
	marker := filepath.Join(t.TempDir(), "received")
	helper := exec.Command(os.Args[0], "-test.run=TestInspectorSignalHelper$", "-test.v")
	helper.Env = append(os.Environ(), "EKD_SIGNAL_MARKER="+marker)
	helper.Stdout, helper.Stderr = os.Stderr, os.Stderr
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = helper.Process.Kill()
		_ = helper.Wait()
	})
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(marker + ".ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not arm its SIGUSR1 handler")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := activateInspector(helper.Process.Pid); err != nil {
		t.Fatalf("SIGUSR1 delivery failed: %v", err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		if data, err := os.ReadFile(marker); err == nil && string(data) == "hit" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("SIGUSR1 was not delivered to the target process")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestVerifyRuntimeImageReadsMappedInodes keeps the runtime image check honest:
// the mapped binary of a live process must be accepted and an executable that is
// not mapped must be refused.
func TestVerifyRuntimeImageReadsMappedInodes(t *testing.T) {
	executable, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	running := ProcessInfo{PID: os.Getpid(), Executable: executable}
	if err := verifyRuntimeImage(ctx, running); err != nil {
		t.Fatalf("mapped binary rejected: %v", err)
	}
	other := filepath.Join(t.TempDir(), "unmapped")
	if err = os.WriteFile(other, []byte("not a running image"), 0o600); err != nil {
		t.Fatal(err)
	}
	running.Executable = other
	if err = verifyRuntimeImage(ctx, running); err == nil {
		t.Fatal("an executable that is not the running image was accepted")
	}
}
