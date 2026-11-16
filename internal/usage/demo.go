package usage

import (
	"encoding/json"
	"time"
)

// demoEvents are deterministic, fully explicit synthetic records. They contain
// no credentials, no network identifiers and are re-imported idempotently.
func demoEvents() []EventInput {
	base := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	stamp := func(minutes int) string {
		return base.Add(time.Duration(minutes) * time.Minute).Format(time.RFC3339Nano)
	}
	raw := encodeRaw
	claudePrice := &Price{Input: 5, Output: 25, Read: numberPtr(0.5), Write: numberPtr(6.25), Currency: "USD", Source: "fixture", FetchedAt: stringPtr("2026-11-10T00:00:00Z"), Unit: priceUnit}
	gptPrice := &Price{Input: 2, Output: 8, Read: numberPtr(0.25), Currency: "USD", Source: "fixture", FetchedAt: stringPtr("2026-11-10T00:00:00Z"), Unit: priceUnit}
	return []EventInput{
		{
			ProviderID: "anthropic", Model: "claude-opus-5-5", RequestID: "demo-req-1", AttemptID: "attempt-1",
			SessionID: "session-demo", ProjectID: "project-demo", AgentID: "main", Role: "main", RequestKind: "coding",
			Protocol: "anthropic", Source: "official", Kind: "request", At: stamp(0), Observation: "demo-obs-1",
			Raw:   raw(map[string]any{"input_tokens": 100, "output_tokens": 20, "cache_read_input_tokens": 50, "cache_creation_input_tokens": 10}),
			Price: claudePrice,
		},
		{
			ProviderID: "anthropic", Model: "claude-opus-5-5", RequestID: "demo-req-2", AttemptID: "attempt-1",
			SessionID: "session-demo", ProjectID: "project-demo", AgentID: "worker-1", Role: "worker", RequestKind: "coding",
			Protocol: "anthropic", Source: "official", Kind: "request", At: stamp(2), Observation: "demo-obs-2",
			Raw:   raw(map[string]any{"input_tokens": 40, "output_tokens": 8, "cache_read_input_tokens": 12}),
			Price: claudePrice,
		},
		{
			// A genuine retry: same logical request, different attempt.
			ProviderID: "openai", Model: "gpt-5.2-pro", RequestID: "demo-req-3", AttemptID: "attempt-1",
			SessionID: "session-demo", ProjectID: "project-demo", AgentID: "main", Role: "main", RequestKind: "chat",
			Protocol: "openai", Source: "gateway", Kind: "request", At: stamp(5), Observation: "demo-obs-3a",
			Raw:   raw(map[string]any{"prompt_tokens": 1000, "completion_tokens": 0, "prompt_tokens_details": map[string]any{"cached_tokens": 0}}),
			Price: gptPrice,
		},
		{
			ProviderID: "openai", Model: "gpt-5.2-pro", RequestID: "demo-req-3", AttemptID: "attempt-2",
			SessionID: "session-demo", ProjectID: "project-demo", AgentID: "main", Role: "main", RequestKind: "chat",
			Protocol: "openai", Source: "official", Kind: "request", At: stamp(6), Observation: "demo-obs-3b",
			Raw:   raw(map[string]any{"prompt_tokens": 1000, "completion_tokens": 120, "prompt_tokens_details": map[string]any{"cached_tokens": 400}}),
			Price: gptPrice,
		},
		{
			// Compaction request, attributed separately.
			ProviderID: "openai", Model: "gpt-5.1", RequestID: "demo-req-4", AttemptID: "attempt-1",
			SessionID: "session-demo", ProjectID: "project-demo", AgentID: "main", Role: "main", RequestKind: "compaction",
			Protocol: "responses", Source: "official", Kind: "request", At: stamp(8), Observation: "demo-obs-4",
			Raw:   raw(map[string]any{"input_tokens": 500, "output_tokens": 60, "input_tokens_details": map[string]any{"cached_tokens": 100}}),
			Price: gptPrice,
		},
		{
			// Unknown usage: the provider did not report counts. Must stay null.
			ProviderID: "google", Model: "gemini-3-pro-image", RequestID: "demo-req-5", AttemptID: "attempt-1",
			SessionID: "session-demo", ProjectID: "project-demo", AgentID: "reviewer-1", Role: "reviewer", RequestKind: "review",
			Protocol: "gemini", Source: "monitor", Kind: "request", At: stamp(9), Observation: "demo-obs-5",
			Raw: raw(map[string]any{}),
		},
		{
			// Context-state snapshot: never billable.
			ProviderID: "anthropic", Model: "claude-opus-5-5", RequestID: "demo-snapshot-1", AttemptID: "state-1",
			SessionID: "session-demo", ProjectID: "project-demo", AgentID: "main", Role: "main",
			Protocol: "anthropic", Source: "official", Kind: "snapshot", At: stamp(10), Observation: "demo-snapshot-obs",
			Counts: &Counts{Input: countPtr(900), Fresh: countPtr(800), Output: nil, Read: countPtr(100)},
		},
		{
			// Wire record without attempt identity: unresolved, not summed into totals.
			ProviderID: "openai", Model: "gpt-5.1", RequestID: "demo-unlinked-1", AttemptID: "",
			SessionID: "", ProjectID: "project-demo", AgentID: "", Role: "UNKNOWN",
			Protocol: "openai", Source: "official", Kind: "request", At: stamp(11), Observation: "demo-obs-6",
			Raw: raw(map[string]any{"prompt_tokens": 250, "completion_tokens": 30}),
		},
	}
}

func encodeRaw(value map[string]any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func (s *Service) Demo() (map[string]any, error) {
	events := demoEvents()
	result, err := s.Ingest(events)
	if err != nil {
		return nil, err
	}
	total, err := s.countEvents()
	if err != nil {
		return nil, err
	}
	quotaAdded := 0
	for _, quota := range demoQuotas() {
		inserted, err := s.insertDemoQuota(quota)
		if err != nil {
			return nil, err
		}
		if inserted {
			quotaAdded++
		}
	}
	return map[string]any{
		"added":               result.Added,
		"duplicates":          result.Duplicates,
		"updated":             result.Updated,
		"snapshots":           result.Snapshots,
		"unresolved":          result.Unresolved,
		"total":               result.Total,
		"eventsAfter":         total,
		"quotaSnapshotsAdded": quotaAdded,
		"demo":                true,
		"note":                "确定性合成事件与 Mock 额度快照；重复调用只产生 duplicates，不新增、不累加倍率",
	}, nil
}

// demoQuotas are the fixed Mock ledger snapshots written by the demo. Their
// identity is the full tuple below, so a repeat can detect them without adding
// uniqueness rules to arbitrary user snapshots.
func demoQuotas() []QuotaInput {
	return []QuotaInput{
		{Ledger: "SUBSCRIPTION", ProviderID: "kimi", Scope: "plan", Remaining: nil, Limit: nil, ResetAt: nil, Source: "mock", Note: "Mock 订阅额度快照，剩余量 UNKNOWN", ObservedAt: "2026-11-10T09:00:00Z"},
		{Ledger: "GATEWAY", ProviderID: "mock-gateway", Scope: "balance", Remaining: nil, Currency: nil, Source: "mock", Note: "网关未查询，余额 UNKNOWN", ObservedAt: "2026-11-10T09:00:00Z"},
	}
}

// insertDemoQuota inserts only when the fixed Mock snapshot is absent. The
// existence check and the insert are a single statement, so concurrent demo
// calls cannot both observe "absent" and each insert a row.
func (s *Service) insertDemoQuota(quota QuotaInput) (bool, error) {
	res, err := s.db.Exec(`INSERT INTO usage_quota_snapshots(ledger,provider_id,scope,remaining,limit_value,unit,reset_at,currency,source,observed_at,note)
		SELECT ?,?,?,?,?,?,?,?,?,?,?
		WHERE NOT EXISTS (
			SELECT 1 FROM usage_quota_snapshots
			WHERE ledger=? AND provider_id=? AND scope=? AND source=? AND observed_at=? AND note=?
		)`,
		quota.Ledger, quota.ProviderID, quota.Scope, deref(quota.Remaining), deref(quota.Limit), deref(quota.Unit), deref(quota.ResetAt), deref(quota.Currency), quota.Source, quota.ObservedAt, quota.Note,
		quota.Ledger, quota.ProviderID, quota.Scope, quota.Source, quota.ObservedAt, quota.Note)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

func (s *Service) countEvents() (int, error) {
	var total int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM usage_events`).Scan(&total)
	return total, err
}
