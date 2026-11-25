package desktop

import (
	"strings"
	"time"

	"enhancedkimi/internal/cache"
)

const (
	idleKeepAlivePrompt = "[保活]仅回复1"
	keepAliveSent       = "sent"
	keepAliveHit        = "hit"
	keepAliveMiss       = "miss"
	keepAliveUnknown    = "unknown"
)

func isKeepAlivePrompt(text string) bool {
	text = strings.TrimSpace(text)
	return text == idleKeepAlivePrompt || strings.HasPrefix(text, idleKeepAlivePrompt+" #") || text == "[保活]无需操作，继续等待"
}

func (s *NativeService) keepAliveWindow() time.Duration {
	if s.verifyWindow > 0 {
		return s.verifyWindow
	}
	return 5 * time.Minute
}

func judgeKeepAlive(step cache.Step, reportsCache bool) (state, note string) {
	if step.InputCacheRead > 0 {
		return keepAliveHit, "保活请求读取了缓存"
	}
	if step.InputCacheCreation > 0 || (reportsCache && step.InputOther > 0) {
		return keepAliveMiss, "保活请求未读取缓存"
	}
	return keepAliveUnknown, "保活请求未提供可核实的缓存用量"
}

func applyRetentionPolicy(policy ResolvedCachePolicy, buckets []cache.RetentionBucket) ResolvedCachePolicy {
	policy.Source = "fallback"
	policy.WindowMinutes = 5
	if policy.ProviderType == ProviderTypeSubscription {
		policy.WindowMinutes = 30
	}

	// Use the lower edge of a well-sampled bucket. A later hot bucket cannot
	// override a reliably cold shorter interval.
	badBoundary := 0
	observed := 0
	for _, bucket := range buckets {
		if bucket.Samples < 5 || bucket.HitRate == nil {
			continue
		}
		minutes := int(time.Duration(bucket.MinMs) * time.Millisecond / time.Minute)
		if *bucket.HitRate < 0.70 {
			if minutes == 0 {
				policy.Source = "observed"
				policy.WindowMinutes = 0
				return policy
			}
			badBoundary = minutes
			break
		}
		observed = minutes
	}
	if observed > 0 {
		policy.Source = "observed"
		policy.WindowMinutes = observed
	} else if badBoundary > 0 && badBoundary <= policy.WindowMinutes {
		policy.Source = "observed"
		policy.WindowMinutes = badBoundary
	}
	margin := max(1, (policy.WindowMinutes+2)/6)
	policy.IntervalMinutes = policy.WindowMinutes - margin
	return policy
}

const idleKeepAliveTokenBudget int64 = 400_000

// Count cache reads at the full input-token weight so a miss fits the same
// allowance. Reserve a small amount for the probe and its reply.
func keepAliveTokenEstimate(session NativeSession, step cache.Step) int64 {
	input := step.InputOther + step.InputCacheRead + step.InputCacheCreation
	if session.Usage != nil {
		input = max(input, session.Usage.ContextTokens)
	}
	if input <= 0 {
		return 0
	}
	return input + 256
}
