package usage

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

type Price struct {
	Input     float64  `json:"input"`
	Output    float64  `json:"output"`
	Read      *float64 `json:"cacheRead"`
	Write     *float64 `json:"cacheWrite"`
	Currency  string   `json:"currency"`
	Source    string   `json:"source"`
	FetchedAt *string  `json:"fetchedAt"`
	Unit      string   `json:"unit"`

	fetchedMs int64
}

type priceWire struct {
	Input     *float64 `json:"input"`
	Output    *float64 `json:"output"`
	Read      *float64 `json:"cacheRead"`
	Write     *float64 `json:"cacheWrite"`
	Currency  string   `json:"currency"`
	Source    string   `json:"source"`
	FetchedAt *string  `json:"fetchedAt"`
	Unit      string   `json:"unit"`
}

// UnmarshalJSON keeps input/output required at the JSON boundary: an omitted or
// null base rate must fail instead of silently decoding to a free zero price.
// Unknown nested fields are rejected, and an explicit 0 stays a valid rate.
func (p *Price) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("price 必须是对象: %w", err)
	}
	for field := range fields {
		if !priceFields[field] {
			return fmt.Errorf("price 包含未知字段 %q", field)
		}
	}
	var wire priceWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return fmt.Errorf("price 字段无效: %w", err)
	}
	if wire.Input == nil || wire.Output == nil {
		return fmt.Errorf("price.input 与 price.output 必须显式出现，null 或缺失表示未知而不是免费")
	}
	if !finite(*wire.Input) || !finite(*wire.Output) {
		return fmt.Errorf("price.input 与 price.output 必须是有限数值")
	}
	if wire.Read != nil && !finite(*wire.Read) {
		return fmt.Errorf("price.cacheRead 必须是有限数值")
	}
	if wire.Write != nil && !finite(*wire.Write) {
		return fmt.Errorf("price.cacheWrite 必须是有限数值")
	}
	*p = Price{
		Input: *wire.Input, Output: *wire.Output, Read: wire.Read, Write: wire.Write,
		Currency: wire.Currency, Source: wire.Source, FetchedAt: wire.FetchedAt, Unit: wire.Unit,
	}
	return nil
}

var priceFields = map[string]bool{
	"input": true, "output": true, "cacheRead": true, "cacheWrite": true,
	"currency": true, "source": true, "fetchedAt": true, "unit": true,
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

const priceUnit = "per_million_tokens"

type EventInput struct {
	ObservationState string          `json:"observationState"`
	Sequence         *int64          `json:"sequence"`
	ProviderID       string          `json:"providerId"`
	Model            string          `json:"model"`
	RequestID        string          `json:"requestId"`
	AttemptID        string          `json:"attemptId"`
	SessionID        string          `json:"sessionId"`
	ProjectID        string          `json:"projectId"`
	AgentID          string          `json:"agentId"`
	Role             string          `json:"role"`
	RequestKind      string          `json:"requestKind"`
	Protocol         string          `json:"protocol"`
	Source           string          `json:"source"`
	Kind             string          `json:"kind"`
	At               string          `json:"at"`
	Raw              json.RawMessage `json:"raw"`
	Counts           *Counts         `json:"counts"`
	Price            *Price          `json:"price"`
	Observation      string          `json:"observationId"`
	WireAgent        string          `json:"wireAgent"`
	RecordPosition   string          `json:"recordPosition"`
	// GatewayAccountID is gateway-local attribution. It is not a wire field:
	// record() folds it into the raw object, and prepare clears it so it never
	// reaches canonical keys or duplicate detection.
	GatewayAccountID string `json:"-"`
}

type Event struct {
	ObservationState string `json:"observationState"`
	Sequence         int64  `json:"sequence"`
	ConflictStatus   string `json:"conflictStatus"`
	ID               int64  `json:"id"`
	CanonicalKey     string `json:"canonicalKey"`
	ProviderID       string `json:"providerId"`
	Model            string `json:"model"`
	RequestID        string `json:"requestId"`
	AttemptID        string `json:"attemptId"`
	SessionID        string `json:"sessionId"`
	ProjectID        string `json:"projectId"`
	AgentID          string `json:"agentId"`
	Role             string `json:"role"`
	RequestKind      string `json:"requestKind"`
	Protocol         string `json:"protocol"`
	Source           string `json:"source"`
	Authoritative    bool   `json:"authoritative"`
	Kind             string `json:"kind"`
	At               string `json:"at"`
	Fresh            *int64 `json:"fresh"`
	Input            *int64 `json:"input"`
	Output           *int64 `json:"output"`
	CacheRead        *int64 `json:"cacheRead"`
	CacheWrite       *int64 `json:"cacheWrite"`
	Price            *Price `json:"price"`
	Raw              any    `json:"raw"`
	ObservedAt       string `json:"observedAt"`
}

type Provenance struct {
	EventID       int64  `json:"eventId"`
	ObservationID string `json:"observationId"`
	Source        string `json:"source"`
	Authority     int    `json:"authority"`
	Role          string `json:"role"`
	At            string `json:"at"`
	RecordedAt    string `json:"recordedAt"`
}

type IngestResult struct {
	Conflicts  int `json:"conflicts"`
	Added      int `json:"added"`
	Duplicates int `json:"duplicates"`
	Updated    int `json:"updated"`
	Snapshots  int `json:"snapshots"`
	Unresolved int `json:"unresolved"`
	Total      int `json:"total"`
}

const (
	sourceOfficial = "official"
	sourceGateway  = "gateway"
	sourceManual   = "manual"
	sourceMock     = "mock"
	sourceMonitor  = "monitor"
)

var authority = map[string]int{
	"UNKNOWN":      0,
	sourceMock:     1,
	sourceMonitor:  1,
	sourceGateway:  2,
	sourceOfficial: 3,
	sourceManual:   4,
}

var knownRoles = map[string]bool{
	"main": true, "worker": true, "reviewer": true, "btw": true, "system": true, "unknown": true, "UNKNOWN": true,
}

var knownRequestKinds = map[string]bool{
	"chat": true, "coding": true, "compaction": true, "review": true, "gateway": true, "unknown": true, "UNKNOWN": true,
}

var knownSources = map[string]bool{
	sourceOfficial: true, sourceGateway: true, sourceManual: true, sourceMock: true, sourceMonitor: true, "broker": true, "UNKNOWN": true,
}

func canonicalSource(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "UNKNOWN"
	}
	if value == "official-event" {
		return sourceOfficial
	}
	return value
}

type preparedEvent struct {
	lifecycle   string
	sequence    int64
	input       EventInput
	counts      Counts
	at          time.Time
	atText      string
	canonical   string
	duplicateID string
	authority   int
	role        string
	requestKind string
	source      string
	price       *Price
}

func prepareEvent(index int, in EventInput) (preparedEvent, error) {
	var out preparedEvent
	in.GatewayAccountID = ""
	in.ProviderID = strings.TrimSpace(in.ProviderID)
	in.Model = strings.TrimSpace(in.Model)
	in.RequestID = strings.TrimSpace(in.RequestID)
	in.AttemptID = strings.TrimSpace(in.AttemptID)
	in.SessionID = strings.TrimSpace(in.SessionID)
	in.ProjectID = strings.TrimSpace(in.ProjectID)
	in.AgentID = strings.TrimSpace(in.AgentID)
	in.Kind = strings.ToLower(strings.TrimSpace(in.Kind))
	if in.Kind == "" {
		in.Kind = "request"
	}
	if in.ProviderID == "" || len(in.ProviderID) > 100 {
		return out, unprocessable("INVALID_EVENT", "事件 %d 缺少 providerId", index)
	}
	if in.Model == "" || len(in.Model) > 200 {
		return out, unprocessable("INVALID_EVENT", "事件 %d 缺少 model", index)
	}
	if in.RequestID == "" || len(in.RequestID) > 200 {
		return out, unprocessable("INVALID_EVENT", "事件 %d 缺少 requestId，无法进行 canonical 去重", index)
	}
	if in.Kind != "request" && in.Kind != "snapshot" {
		return out, unprocessable("INVALID_EVENT", "事件 %d 的 kind 只能是 request 或 snapshot", index)
	}
	atText := strings.TrimSpace(in.At)
	if atText == "" {
		return out, unprocessable("INVALID_EVENT", "事件 %d 缺少 at 时间", index)
	}
	at, err := time.Parse(time.RFC3339Nano, atText)
	if err != nil {
		return out, unprocessable("INVALID_EVENT", "事件 %d 的 at 必须是 RFC3339 时间", index)
	}
	out.at = at.UTC()
	out.atText = out.at.Format(time.RFC3339Nano)
	out.input = in
	out.role = normalizeEnum(in.Role, knownRoles, "UNKNOWN")
	out.requestKind = normalizeEnum(in.RequestKind, knownRequestKinds, "UNKNOWN")
	out.source = canonicalSource(in.Source)
	if !knownSources[out.source] && out.source != "UNKNOWN" {
		return out, unprocessable("INVALID_EVENT", "事件 %d 的 source %q 不受支持", index, in.Source)
	}
	out.authority = authority[out.source]
	if out.authority == 0 {
		out.authority = 0
	}
	out.counts = normalizedCounts(in)
	out.lifecycle = strings.ToUpper(strings.TrimSpace(in.ObservationState))
	if out.lifecycle == "" {
		out.lifecycle = "PARTIAL"
		if out.counts.Output != nil {
			out.lifecycle = "FINAL"
		}
	}
	if out.lifecycle != "PARTIAL" && out.lifecycle != "FINAL" && out.lifecycle != "CORRECTION" {
		return out, unprocessable("INVALID_OBSERVATION", "observationState 必须为 PARTIAL/FINAL/CORRECTION")
	}
	out.sequence = out.at.UnixNano()
	if in.Sequence != nil {
		if *in.Sequence < 0 {
			return out, unprocessable("INVALID_SEQUENCE", "sequence 不能为负数")
		}
		out.sequence = *in.Sequence
	}

	out.canonical = canonicalKey(in, out.atText)
	out.duplicateID = observationID(in, out.source, out.atText, out.counts) + "-" + shortHash(fmt.Sprintf("%s/%d/%s", out.lifecycle, out.sequence, in.Observation))
	if in.Price != nil {
		price, err := preparePrice(*in.Price, index)
		if err != nil {
			return out, err
		}
		out.price = price
	}
	return out, nil
}

func normalizeEnum(raw string, known map[string]bool, fallback string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return fallback
	}
	if !known[value] {
		return "UNKNOWN"
	}
	if value == "unknown" || value == "UNKNOWN" {
		return "UNKNOWN"
	}
	return value
}

func normalizedCounts(in EventInput) Counts {
	var counts Counts
	if in.Raw != nil && len(in.Raw) > 0 {
		var raw map[string]any
		if err := json.Unmarshal(in.Raw, &raw); err == nil {
			counts = Normalize(in.Protocol, raw)
		}
	}
	if in.Counts != nil {
		counts = Counts{
			Fresh:  chooseCount(counts.Fresh, in.Counts.Fresh),
			Input:  chooseCount(counts.Input, in.Counts.Input),
			Output: chooseCount(counts.Output, in.Counts.Output),
			Read:   chooseCount(counts.Read, in.Counts.Read),
			Write:  chooseCount(counts.Write, in.Counts.Write),
		}
	}
	return counts
}

func chooseCount(fallback, explicit *int64) *int64 {
	if explicit == nil {
		return fallback
	}
	if *explicit < 0 {
		return fallback
	}
	return explicit
}

func canonicalKey(in EventInput, atText string) string {
	if in.AttemptID != "" && in.SessionID != "" && in.AgentID != "" {
		return strings.Join([]string{"attempt", in.ProviderID, in.RequestID, in.AttemptID, in.SessionID, in.AgentID}, "\x1f")
	}
	return strings.Join([]string{"unlinked", in.ProviderID, in.RequestID, atText, "UNKNOWN"}, "\x1f")
}

func observationID(in EventInput, source, atText string, counts Counts) string {
	payload, _ := json.Marshal([]any{source, in.RequestID, in.AttemptID, in.SessionID, in.AgentID, atText, counts.Fresh, counts.Input, counts.Output, counts.Read, counts.Write})
	key := strings.Join([]string{strings.ToLower(in.ProviderID), in.Model, string(payload)}, "\x1f")
	return "obs-" + shortHash(key)
}

func preparePrice(price Price, index int) (*Price, error) {
	if price.Currency == "" {
		return nil, unprocessable("INVALID_PRICE", "事件 %d 的价格缺少 currency", index)
	}
	if price.Source == "" || price.FetchedAt == nil {
		return nil, unprocessable("INVALID_PRICE", "事件 %d 的价格缺少 source 或 fetchedAt", index)
	}
	if price.Unit == "" {
		price.Unit = priceUnit
	}
	if price.Unit != priceUnit {
		return nil, unprocessable("INVALID_PRICE", "事件 %d 的价格单位必须是 %s", index, priceUnit)
	}
	if !finite(price.Input) || !finite(price.Output) ||
		(price.Read != nil && (!finite(*price.Read) || *price.Read < 0)) ||
		(price.Write != nil && (!finite(*price.Write) || *price.Write < 0)) {
		return nil, unprocessable("INVALID_PRICE", "事件 %d 的价格必须是有限非负数", index)
	}
	if price.Input < 0 || price.Output < 0 {
		return nil, unprocessable("INVALID_PRICE", "事件 %d 的价格不能为负数", index)
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(*price.FetchedAt))
	if err != nil {
		return nil, unprocessable("INVALID_PRICE", "事件 %d 的价格 fetchedAt 必须是 RFC3339 时间", index)
	}
	text := parsed.UTC().Format(time.RFC3339Nano)
	price.FetchedAt = &text
	price.fetchedMs = parsed.UnixMilli()
	return &price, nil
}

func (e preparedEvent) isSnapshot() bool { return e.input.Kind == "snapshot" }
