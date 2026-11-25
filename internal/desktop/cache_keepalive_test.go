package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"enhancedkimi/internal/cache"
)

// keepAliveFixture builds a synthesised session whose main agent last called a model
// at mainAge and whose background subagent is either still running or finished.
type keepAliveFixture struct {
	home       string
	mainAge    time.Duration
	background bool
	blocked    bool
	// lastCacheRead is the input_cache_read of the main agent's recorded step.
	lastCacheRead int
}

func (f keepAliveFixture) write(t *testing.T, model string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	mainAt := now.Add(-f.mainAge)
	dir := filepath.Join(f.home, "sessions", "wd_test", "session_s1", "agents", "main")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	usage := fmt.Sprintf(`{"inputOther":1000,"output":200,"inputCacheRead":%d,"inputCacheCreation":0}`, f.lastCacheRead)
	lines := []string{
		fmt.Sprintf(`{"type":"usage.record","agentId":"main","model":%q,"usage":%s,"usageScope":"turn","time":%d}`, model, usage, mainAt.UnixMilli()),
		fmt.Sprintf(`{"type":"context.append_loop_event","agentId":"main","event":{"type":"step.end","uuid":"step-main","turnId":"turn-user-1","step":1,"finishReason":"end_turn","usage":%s,"llmServerDecodeMs":2000},"time":%d}`, usage, mainAt.UnixMilli()),
	}
	if f.background {
		lines = append(lines, fmt.Sprintf(`{"type":"task.started","agentId":"main","info":{"taskId":"agent-bg","kind":"agent","detached":true,"status":"running","startedAt":%d},"time":%d}`, mainAt.UnixMilli(), mainAt.UnixMilli()))
	}
	if f.blocked {
		// A blocking window is an outstanding WaitFor tool call. task.waitDelivered is the
		// delivery of a wait that already finished, so it must not be used here.
		lines = append(lines, fmt.Sprintf(`{"type":"context.append_loop_event","agentId":"main","event":{"type":"tool.call","name":"WaitFor","toolCallId":"call-wait"},"time":%d}`, mainAt.Add(time.Second).UnixMilli()))
	}
	if err := os.WriteFile(filepath.Join(dir, "wire.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := fmt.Sprintf(`{"id":"s1","cwd":"/tmp/synthetic","createdAt":%d,"updatedAt":%d,"agents":{"main":{"type":"main"}}}`, now.Add(-time.Hour).UnixMilli(), now.UnixMilli())
	if err := os.WriteFile(filepath.Join(f.home, "sessions", "wd_test", "session_s1", "state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestKeepAliveRetentionPolicy(t *testing.T) {
	policy := ResolvedCachePolicy{ProviderType: ProviderTypeSubscription}
	high, low := 0.90, 0.10
	bucket := func(minutes, samples int, rate *float64) cache.RetentionBucket {
		return cache.RetentionBucket{MinMs: int64(minutes) * time.Minute.Milliseconds(), Samples: samples, HitRate: rate}
	}
	for _, tc := range []struct {
		name     string
		buckets  []cache.RetentionBucket
		interval int
		source   string
	}{
		{"no data", nil, 25, "fallback"},
		{"insufficient samples", []cache.RetentionBucket{bucket(10, 4, &high)}, 25, "fallback"},
		{"hot lower bound", []cache.RetentionBucket{bucket(0, 10, &high), bucket(10, 5, &high), bucket(30, 5, &low)}, 8, "observed"},
		{"cold boundary", []cache.RetentionBucket{bucket(0, 5, &high), bucket(5, 5, &low), bucket(10, 10, &high)}, 4, "observed"},
		{"cold immediately", []cache.RetentionBucket{bucket(0, 5, &low)}, 0, "observed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := applyRetentionPolicy(policy, tc.buckets)
			if got.IntervalMinutes != tc.interval || got.Source != tc.source {
				t.Fatalf("policy = %+v, want interval %d, source %s", got, tc.interval, tc.source)
			}
		})
	}
}

func TestJudgeKeepAliveVerdicts(t *testing.T) {
	for _, tc := range []struct {
		step  cache.Step
		known bool
		want  string
	}{
		{cache.Step{InputCacheRead: 12}, false, keepAliveHit},
		{cache.Step{InputCacheCreation: 12}, false, keepAliveMiss},
		{cache.Step{InputOther: 12}, true, keepAliveMiss},
		{cache.Step{InputOther: 12}, false, keepAliveUnknown},
	} {
		if state, _ := judgeKeepAlive(tc.step, tc.known); state != tc.want {
			t.Errorf("step %+v with known=%v: got %q, want %q", tc.step, tc.known, state, tc.want)
		}
	}
}
