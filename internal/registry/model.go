package registry

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"time"
)

type Source string

const (
	SourceUnknown   Source = "UNKNOWN"
	SourceManual    Source = "manual"
	SourceOfficial  Source = "official"
	SourceRegistry  Source = "registry"
	SourceModelsDev Source = "modelsdev"
)

var sourcePriority = map[Source]int{
	SourceUnknown:   0,
	SourceManual:    5,
	SourceOfficial:  4,
	SourceRegistry:  3,
	SourceModelsDev: 2,
}

const (
	kindNumber = "number"
	kindBool   = "bool"
	kindNull   = "null"
)

var numericFields = map[string]bool{"context": true, "input": true, "output": true, "cacheTTL": true}
var booleanFields = map[string]bool{"vision": true, "video": true, "audio": true, "tools": true, "thinking": true}

var knownFields = []string{"context", "input", "output", "cacheTTL", "vision", "video", "audio", "tools", "thinking"}

type Capability struct {
	Value          any     `json:"value"`
	Source         Source  `json:"source"`
	FetchedAt      *string `json:"fetchedAt"`
	UpdatedAt      *string `json:"updatedAt,omitempty"`
	SourceURL      *string `json:"sourceUrl,omitempty"`
	SourceRevision *string `json:"sourceRevision,omitempty"`

	observedMs int64
}

func (c Capability) IsBool() bool {
	_, ok := c.Value.(bool)
	return ok
}

type modelKey struct {
	ProviderID string
	ModelID    string
}

type modelRecord struct {
	Key              modelKey
	DisplayName      string
	Capabilities     map[string]Capability
	AutoName         string
	ManualName       *string
	AutoCapabilities map[string]Capability
}

func newModelRecord(providerID, modelID string) *modelRecord {
	rec := &modelRecord{Key: modelKey{ProviderID: providerID, ModelID: modelID}, DisplayName: modelID, Capabilities: map[string]Capability{}, AutoCapabilities: map[string]Capability{}}
	for _, field := range knownFields {
		rec.Capabilities[field] = Capability{Source: SourceUnknown}
	}
	return rec
}

func (r *modelRecord) clone() *modelRecord {
	out := &modelRecord{Key: r.Key, DisplayName: r.DisplayName, AutoName: r.AutoName, ManualName: r.ManualName, AutoCapabilities: map[string]Capability{}, Capabilities: make(map[string]Capability, len(r.Capabilities))}
	for field, c := range r.AutoCapabilities {
		out.AutoCapabilities[field] = c
	}
	for field, capability := range r.Capabilities {
		out.Capabilities[field] = capability
	}
	return out
}

type modelInput struct {
	ProviderID   string                `json:"providerId"`
	ID           string                `json:"id"`
	Name         string                `json:"name"`
	DisplayName  string                `json:"displayName"`
	Capabilities map[string]Capability `json:"capabilities"`
}

type Model struct {
	ProviderID       string                `json:"providerId"`
	ID               string                `json:"id"`
	Name             string                `json:"name"`
	DisplayName      string                `json:"displayName"`
	Capabilities     map[string]Capability `json:"capabilities"`
	AutoName         string                `json:"autoName"`
	ManualName       *string               `json:"manualName"`
	AutoCapabilities map[string]Capability `json:"autoCapabilities"`
}

func recordToModel(rec *modelRecord) Model {
	caps := make(map[string]Capability, len(knownFields))
	for _, field := range knownFields {
		capability, ok := rec.Capabilities[field]
		if !ok {
			capability = Capability{Source: SourceUnknown}
		}
		caps[field] = capability
	}
	extra := make([]string, 0)
	for field := range rec.Capabilities {
		if !isKnownField(field) {
			extra = append(extra, field)
		}
	}
	sort.Strings(extra)
	for _, field := range extra {
		caps[field] = rec.Capabilities[field]
	}
	return Model{ProviderID: rec.Key.ProviderID, ID: rec.Key.ModelID, Name: rec.DisplayName, DisplayName: rec.DisplayName, Capabilities: caps, AutoName: rec.AutoName, ManualName: rec.ManualName, AutoCapabilities: rec.AutoCapabilities}
}

func marshalModel(rec *modelRecord) ([]byte, error) {
	return json.Marshal(recordToModel(rec))
}

func decodeModel(raw []byte) (*modelRecord, error) {
	var model Model
	if err := json.Unmarshal(raw, &model); err != nil {
		return nil, err
	}
	rec := newModelRecord(model.ProviderID, model.ID)
	rec.DisplayName = model.Name
	if rec.DisplayName == "" {
		rec.DisplayName = model.DisplayName
	}
	if rec.DisplayName == "" {
		rec.DisplayName = model.ID
	}
	rec.AutoName = model.AutoName
	rec.ManualName = model.ManualName
	rec.AutoCapabilities = model.AutoCapabilities
	for field, capability := range model.Capabilities {
		if err := capability.normalize(field); err != nil {
			return nil, err
		}
		rec.Capabilities[field] = capability
	}
	rec.restoreMetadata()
	return rec, nil
}

func isKnownField(field string) bool {
	return numericFields[field] || booleanFields[field]
}

func (c *Capability) normalizeProvenance() error {
	c.Source = Source(strings.TrimSpace(string(c.Source)))
	if c.Source == "" {
		c.Source = SourceUnknown
	}
	if _, ok := sourcePriority[c.Source]; !ok {
		return badRequest("INVALID_SOURCE", "来源 %q 不受支持，允许 manual/official/registry/modelsdev/UNKNOWN", c.Source)
	}
	if c.FetchedAt == nil && c.UpdatedAt != nil {
		c.FetchedAt = c.UpdatedAt
	}
	c.UpdatedAt = nil
	if c.FetchedAt == nil {
		return badRequest("MISSING_PROVENANCE", "字段必须包含 fetchedAt 才能记录来源")
	}
	text := strings.TrimSpace(*c.FetchedAt)
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return badRequest("INVALID_FETCHED_AT", "fetchedAt 必须是 ISO8601/RFC3339 时间: %q", text)
	}
	normalized := parsed.UTC().Format(time.RFC3339Nano)
	c.FetchedAt = &normalized
	c.observedMs = parsed.UnixMilli()
	if c.SourceURL != nil {
		if err := validateReferenceURL(*c.SourceURL); err != nil {
			return err
		}
	}
	if c.SourceRevision != nil && strings.TrimSpace(*c.SourceRevision) == "" {
		c.SourceRevision = nil
	}
	return nil
}

// normalize validates the field, writes the canonical source and UTC
// timestamp back into the receiver, and leaves an UNKNOWN/null observation
// untouched. The pointer receiver is deliberate: a value receiver would
// normalize only a copy and let callers persist a padded source that then
// misses the priority table.
func (c *Capability) normalize(field string) error {
	if !isKnownField(field) {
		return badRequest("UNKNOWN_FIELD", "未知模型能力字段 %q，允许 %s", field, strings.Join(knownFields, ", "))
	}
	trimmedSource := Source(strings.TrimSpace(string(c.Source)))
	if trimmedSource == "" {
		trimmedSource = SourceUnknown
	}
	if trimmedSource == SourceUnknown && c.Value == nil {
		*c = Capability{Source: SourceUnknown}
		return nil
	}
	if c.Source == "" {
		return badRequest("MISSING_PROVENANCE", "字段 %s 缺少来源", field)
	}
	if err := c.normalizeProvenance(); err != nil {
		return err
	}
	if c.Source == SourceUnknown {
		return badRequest("INVALID_SOURCE", "字段 %s 声明 UNKNOWN 来源时不能同时提供值", field)
	}
	if c.Value == nil {
		return nil
	}
	if numericFields[field] {
		number, ok := c.Value.(float64)
		if !ok || number != math.Trunc(number) || number <= 0 || math.IsInf(number, 0) || math.IsNaN(number) {
			return unprocessable("INVALID_CAPABILITY", "字段 %s 必须是正整数或 null，收到 %v", field, c.Value)
		}
		return nil
	}
	if _, ok := c.Value.(bool); !ok {
		return unprocessable("INVALID_CAPABILITY", "字段 %s 必须是布尔值或 null，收到 %v", field, c.Value)
	}
	return nil
}

func validateIdentity(providerID, modelID string) error {
	if !providerIDPattern.MatchString(providerID) {
		return badRequest("INVALID_PROVIDER_ID", "providerId %q 只能包含小写字母、数字、点、下划线和短横线", providerID)
	}
	if modelID == "" || strings.TrimSpace(modelID) != modelID || len(modelID) > 200 {
		return badRequest("INVALID_MODEL_ID", "模型 ID 不能为空、不能包含首尾空白且最长 200 字符")
	}
	for _, r := range modelID {
		if r < 0x20 || r == 0x7f {
			return badRequest("INVALID_MODEL_ID", "模型 ID 不能包含控制字符")
		}
	}
	return nil
}

func validateReferenceURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return badRequest("INVALID_SOURCE_URL", "来源地址必须是完整的 http/https URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return badRequest("INVALID_SOURCE_URL", "来源地址只能使用 http 或 https")
	}
	if parsed.User != nil {
		return badRequest("CREDENTIALS_IN_URL", "来源地址不能包含用户名或密码")
	}
	if err := rejectSensitiveQuery(parsed); err != nil {
		return err
	}
	return nil
}

var sensitiveQueryKeys = map[string]bool{
	"api_key": true, "apikey": true, "key": true, "token": true, "access_token": true,
	"secret": true, "client_secret": true, "password": true, "passwd": true, "sig": true,
	"signature": true, "credential": true, "credentials": true, "auth": true, "authorization": true,
}

var sensitiveValuePrefixes = []string{"sk-", "sk_", "ghp_", "gho_", "xoxb-", "xoxp-", "AKIA", "AIza"}

func rejectSensitiveQuery(parsed *url.URL) error {
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return badRequest("INVALID_URL_QUERY", "URL 查询参数格式无效")
	}
	for key, values := range query {
		if sensitiveQueryKeys[strings.ToLower(key)] {
			return badRequest("CREDENTIALS_IN_URL", "URL 不能包含凭据参数 %q，密钥应放在 secretRef/凭据库", key)
		}
		for _, value := range values {
			for _, prefix := range sensitiveValuePrefixes {
				if strings.HasPrefix(value, prefix) {
					return badRequest("CREDENTIALS_IN_URL", "URL 查询参数 %q 看起来包含凭据", key)
				}
			}
		}
	}
	return nil
}

func capabilityInstant(c Capability) int64 {
	if c.observedMs != 0 || c.FetchedAt == nil {
		return c.observedMs
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(*c.FetchedAt))
	if err != nil {
		return 0
	}
	return parsed.UnixMilli()
}

func mergeCapability(current Capability, next Capability) Capability {
	if next.Source == SourceUnknown && next.Value == nil {
		return current
	}
	currentPriority := sourcePriority[current.Source]
	nextPriority := sourcePriority[next.Source]
	if nextPriority > currentPriority {
		return next
	}
	if nextPriority == currentPriority && capabilityInstant(next) > capabilityInstant(current) {
		return next
	}
	return current
}

func applyRecords(current map[modelKey]*modelRecord, records []*modelRecord) map[modelKey]*modelRecord {
	next := make(map[modelKey]*modelRecord, len(current)+len(records))
	for key, rec := range current {
		next[key] = rec.clone()
	}
	for _, record := range records {
		rec, ok := next[record.Key]
		if !ok {
			rec = newModelRecord(record.Key.ProviderID, record.Key.ModelID)
			next[record.Key] = rec
		}
		if record.AutoName != "" {
			rec.AutoName = record.AutoName
		}
		if record.ManualName != nil {
			rec.ManualName = record.ManualName
		}
		if rec.AutoName != "" {
			rec.DisplayName = rec.AutoName
		}
		if rec.ManualName != nil {
			rec.DisplayName = *rec.ManualName
		}
		for field, capability := range record.Capabilities {
			if capability.Source == SourceUnknown && capability.Value == nil {
				continue
			}
			if capability.Source != SourceManual {
				rec.AutoCapabilities[field] = mergeCapability(rec.AutoCapabilities[field], capability)
			}
			rec.Capabilities[field] = mergeCapability(rec.Capabilities[field], capability)
		}
	}
	return next
}

func capabilityEqual(a, b Capability) bool {
	if a.Source != b.Source {
		return false
	}
	if (a.FetchedAt == nil) != (b.FetchedAt == nil) || (a.FetchedAt != nil && *a.FetchedAt != *b.FetchedAt) {
		return false
	}
	if (a.SourceURL == nil) != (b.SourceURL == nil) || (a.SourceURL != nil && *a.SourceURL != *b.SourceURL) {
		return false
	}
	if (a.SourceRevision == nil) != (b.SourceRevision == nil) || (a.SourceRevision != nil && *a.SourceRevision != *b.SourceRevision) {
		return false
	}
	switch left := a.Value.(type) {
	case nil:
		return b.Value == nil
	case float64:
		right, ok := b.Value.(float64)
		return ok && left == right
	case bool:
		right, ok := b.Value.(bool)
		return ok && left == right
	}
	return false
}

func recordEqual(a, b *modelRecord) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Key != b.Key || a.DisplayName != b.DisplayName || a.AutoName != b.AutoName || !reflect.DeepEqual(a.ManualName, b.ManualName) || !capabilityMapsEqual(a.AutoCapabilities, b.AutoCapabilities) || len(a.Capabilities) != len(b.Capabilities) {
		return false
	}
	for field, capability := range a.Capabilities {
		other, ok := b.Capabilities[field]
		if !ok || !capabilityEqual(capability, other) {
			return false
		}
	}
	return true
}

type fieldChange struct {
	Field  string     `json:"field"`
	Before Capability `json:"before"`
	After  Capability `json:"after"`
}

type modelChange struct {
	ProviderID string        `json:"providerId"`
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	State      string        `json:"state"`
	Before     *Model        `json:"before,omitempty"`
	After      *Model        `json:"after"`
	Fields     []fieldChange `json:"fields"`
}

type changeSummary struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
}

type modelDelta struct {
	key    modelKey
	state  string
	before *modelRecord
	after  *modelRecord
	fields []fieldChange
}

func computeDeltas(before, after map[modelKey]*modelRecord, keys []modelKey) ([]modelDelta, changeSummary) {
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].ProviderID != keys[j].ProviderID {
			return keys[i].ProviderID < keys[j].ProviderID
		}
		return keys[i].ModelID < keys[j].ModelID
	})
	seen := map[modelKey]bool{}
	var deltas []modelDelta
	var summary changeSummary
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		prev := before[key]
		next := after[key]
		if next == nil {
			continue
		}
		if prev != nil && recordEqual(prev, next) {
			summary.Unchanged++
			continue
		}
		state := "created"
		if prev != nil {
			state = "updated"
			summary.Updated++
		} else {
			summary.Created++
		}
		var fields []fieldChange
		for _, field := range knownFields {
			oldCapability := Capability{Source: SourceUnknown}
			if prev != nil {
				if existing, ok := prev.Capabilities[field]; ok {
					oldCapability = existing
				}
			}
			newCapability := next.Capabilities[field]
			if !capabilityEqual(oldCapability, newCapability) {
				fields = append(fields, fieldChange{Field: field, Before: oldCapability, After: newCapability})
			}
		}
		deltas = append(deltas, modelDelta{key: key, state: state, before: prev, after: next, fields: fields})
	}
	return deltas, summary
}

func modelKeys(records []*modelRecord) []modelKey {
	keys := make([]modelKey, 0, len(records))
	seen := map[modelKey]bool{}
	for _, record := range records {
		if seen[record.Key] {
			continue
		}
		seen[record.Key] = true
		keys = append(keys, record.Key)
	}
	return keys
}

func prepareRecords(inputs []modelInput) ([]*modelRecord, error) {
	if len(inputs) == 0 {
		return nil, badRequest("EMPTY_RECORDS", "records 不能为空")
	}
	if len(inputs) > 5000 {
		return nil, badRequest("TOO_MANY_RECORDS", "单次最多 5000 条模型记录")
	}
	records := make([]*modelRecord, 0, len(inputs))
	for _, input := range inputs {
		providerID := strings.TrimSpace(input.ProviderID)
		modelID := strings.TrimSpace(input.ID)
		if err := validateIdentity(providerID, modelID); err != nil {
			return nil, err
		}
		record := newModelRecord(providerID, modelID)
		record.AutoName = strings.TrimSpace(input.Name)
		record.DisplayName = record.AutoName
		if strings.TrimSpace(input.DisplayName) != "" {
			name := strings.TrimSpace(input.DisplayName)
			record.ManualName = &name
			record.DisplayName = name
		}

		for field, capability := range input.Capabilities {
			if err := capability.normalize(field); err != nil {
				return nil, err
			}
			record.Capabilities[field] = capability
		}
		records = append(records, record)
	}
	return records, nil
}

func summaryText(kind string, summary changeSummary, extra string) string {
	text := fmt.Sprintf("%s: 新增 %d，更新 %d", kind, summary.Created, summary.Updated)
	if summary.Unchanged > 0 {
		text += fmt.Sprintf("，未变化 %d", summary.Unchanged)
	}
	if extra != "" {
		text += "，" + extra
	}
	return text
}

func capabilityMapsEqual(a, b map[string]Capability) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		other, ok := b[k]
		if !ok || !capabilityEqual(v, other) {
			return false
		}
	}
	return true
}
func (r *modelRecord) restoreMetadata() {
	if len(r.AutoCapabilities) == 0 {
		r.AutoCapabilities = map[string]Capability{}
		for k, c := range r.Capabilities {
			if c.Source != SourceManual && c.Source != SourceUnknown {
				r.AutoCapabilities[k] = c
			}
		}
	}
	if r.AutoName == "" && r.ManualName == nil {
		r.AutoName = r.DisplayName
	}
}
