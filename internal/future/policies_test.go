package future

import (
	"testing"
	"time"
)

func TestCookieConflictDoesNotUseBrowserStoreID(t *testing.T) {
	chromeSource := CookieCandidate{SourceBrowser: "Chrome", SourceStoreID: "0", Target: CookieIdentity{Domain: ".Example.COM", Path: "/", Name: "session", TargetPartition: "test-target"}}
	edgeSource := CookieCandidate{SourceBrowser: "Edge", SourceStoreID: "profile-2-store", Target: CookieIdentity{Domain: "example.com", Path: "/", Name: "session", TargetPartition: "test-target"}}
	chrome, edge := chromeSource.Target, edgeSource.Target
	if chrome.ConflictKey() != edge.ConflictKey() {
		t.Fatal("browser store identity must not hide destination cookie collision")
	}
	edge.TargetPartition = "another-target"
	if chrome.ConflictKey() == edge.ConflictKey() {
		t.Fatal("different target sessions must not collide")
	}
	edge.TargetPartition = chrome.TargetPartition
	edge.PartitionKey = "https://partition.example"
	if chrome.ConflictKey() == edge.ConflictKey() {
		t.Fatal("partitioned cookies must stay distinct")
	}
}
func TestCompactionRequiresNativeAndTowerEvidence(t *testing.T) {
	now := time.Now()
	s := SessionState{LastActivity: now.Add(-26 * time.Minute), Revision: "v1", WorkerRunning: true}
	p := CompactionPolicy{}
	if p.Eligible(now, s) {
		t.Fatal("default must be off")
	}
	p.Enabled = true
	p.NativeVerified = true
	if p.Eligible(now, s) {
		t.Fatal("worker must not pass without native verification")
	}
	p.TowerWorkerVerified = true
	if !p.Eligible(now, s) {
		t.Fatal("verified idle controller should be eligible")
	}
	s.ToolBusy = true
	if p.Eligible(now, s) {
		t.Fatal("tool activity must block")
	}
}

func TestCompactionUnknownActivityNeverBecomesIdle(t *testing.T) {
	p := CompactionPolicy{Enabled: true, NativeVerified: true}
	s := SessionState{Revision: "synthetic-revision"}
	if p.Eligible(time.Now(), s) {
		t.Fatal("unknown activity timestamp must block native scheduling")
	}
}
