package desktop

import (
	"context"
	"testing"
	"time"
)

func TestAcquireHostAttachLockSerializesAndTimesOut(t *testing.T) {
	dir := t.TempDir()
	first, err := acquireHostAttachLock(context.Background(), dir)
	if err != nil {
		t.Fatalf("first lock failed: %v", err)
	}
	if first == nil {
		t.Fatal("expected non-nil lock for non-empty dataDir")
	}

	shortCtx, cancelShort := context.WithTimeout(context.Background(), 220*time.Millisecond)
	defer cancelShort()
	if second, err := acquireHostAttachLock(shortCtx, dir); err == nil {
		_ = second.Close()
		_ = first.Close()
		t.Fatal("second lock succeeded while first lock was held")
	}

	waitDone := make(chan error, 1)
	go func() {
		waitCtx, cancelWait := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelWait()
		second, err := acquireHostAttachLock(waitCtx, dir)
		if err != nil {
			waitDone <- err
			return
		}
		waitDone <- second.Close()
	}()

	time.Sleep(150 * time.Millisecond)
	if err := first.Close(); err != nil {
		t.Fatalf("releasing first lock failed: %v", err)
	}
	if err := <-waitDone; err != nil {
		t.Fatalf("waiting lock did not acquire after release: %v", err)
	}
}

func TestAcquireHostAttachLockEmptyDirIsNoop(t *testing.T) {
	lock, err := acquireHostAttachLock(context.Background(), "")
	if err != nil || lock != nil {
		t.Fatalf("empty dataDir: got (%v, %v), want (nil, nil)", lock, err)
	}
}
