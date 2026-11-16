package usage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

type QuotaInput struct {
	Ledger     string  `json:"ledger"`
	ProviderID string  `json:"providerId"`
	Scope      string  `json:"scope"`
	Remaining  *string `json:"remaining"`
	Limit      *string `json:"limit"`
	Unit       *string `json:"unit"`
	ResetAt    *string `json:"resetAt"`
	Currency   *string `json:"currency"`
	Source     string  `json:"source"`
	ObservedAt string  `json:"observedAt"`
	Note       string  `json:"note"`
}

var quotaLedgers = map[string]bool{"SUBSCRIPTION": true, "GATEWAY": true, "PROVIDER": true}

var quotaSources = map[string]bool{sourceOfficial: true, sourceGateway: true, sourceManual: true, sourceMock: true}

func (s *Service) RecordQuota(in QuotaInput) (QuotaSnapshot, error) {
	ledger := strings.ToUpper(strings.TrimSpace(in.Ledger))
	if ledger == "" {
		ledger = "SUBSCRIPTION"
	}
	if !quotaLedgers[ledger] {
		return QuotaSnapshot{}, badRequest("INVALID_LEDGER", "ledger 只能是 SUBSCRIPTION/GATEWAY/PROVIDER")
	}
	providerID := strings.TrimSpace(in.ProviderID)
	if providerID == "" {
		return QuotaSnapshot{}, badRequest("INVALID_PROVIDER", "providerId 不能为空")
	}
	source := canonicalSource(in.Source)
	if !quotaSources[source] {
		return QuotaSnapshot{}, badRequest("INVALID_SOURCE", "quota source 只能是 official/gateway/manual/mock")
	}
	observedAt := strings.TrimSpace(in.ObservedAt)
	if observedAt == "" {
		observedAt = s.nowText()
	}
	parsed, err := parseTime(observedAt)
	if err != nil {
		return QuotaSnapshot{}, badRequest("INVALID_OBSERVED_AT", "observedAt 必须是 RFC3339 时间")
	}
	observedAt = parsed
	scope := strings.TrimSpace(in.Scope)
	if scope == "" {
		scope = "default"
	}
	snapshot := QuotaSnapshot{Ledger: ledger, ProviderID: providerID, Scope: scope, Remaining: in.Remaining, Limit: in.Limit, Unit: in.Unit, ResetAt: in.ResetAt, Currency: in.Currency, Source: source, ObservedAt: observedAt, Note: strings.TrimSpace(in.Note)}
	res, err := s.db.Exec(`INSERT INTO usage_quota_snapshots(ledger,provider_id,scope,remaining,limit_value,unit,reset_at,currency,source,observed_at,note)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, ledger, providerID, scope, deref(in.Remaining), deref(in.Limit), deref(in.Unit), deref(in.ResetAt), deref(in.Currency), source, observedAt, snapshot.Note)
	if err != nil {
		return QuotaSnapshot{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return QuotaSnapshot{}, err
	}
	snapshot.ID = id
	return snapshot, nil
}

type ActualBillingInput struct {
	ObservationID string  `json:"observationId"`
	ProviderID    string  `json:"providerId"`
	Amount        float64 `json:"amount"`
	Currency      string  `json:"currency"`
	Source        string  `json:"source"`
	ObservedAt    string  `json:"observedAt"`
	Note          string  `json:"note"`
}

// maxAmount is a deliberate, exactly representable ceiling. float64 cannot
// distinguish 9_223_372_036_854.775 from one micro above it near 2^63, so the
// documented limit is 1e12 rather than the int64 edge: an amount is accepted
// only when its rounded micro value is below maxAmount*1e6, which every platform
// computes identically.
const maxAmount = 1e12

var actualBillingFields = map[string]bool{
	"observationId": true, "providerId": true, "amount": true, "currency": true,
	"source": true, "observedAt": true, "note": true,
}

func amountMicros(amount float64) (int64, bool) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
		return 0, false
	}
	rounded := math.Round(amount * 1e6)
	if math.IsNaN(rounded) || math.IsInf(rounded, 0) {
		return 0, false
	}
	if rounded >= maxAmount*1e6 {
		return 0, false
	}
	return int64(rounded), true
}

// UnmarshalJSON keeps amount required and finite at the JSON boundary. A missing
// or null amount means "unknown", never a zero charge, while explicit 0 stays a
// valid free amount.
func (in *ActualBillingInput) UnmarshalJSON(raw []byte) error {
	type wire struct {
		ObservationID string   `json:"observationId"`
		ProviderID    string   `json:"providerId"`
		Amount        *float64 `json:"amount"`
		Currency      string   `json:"currency"`
		Source        string   `json:"source"`
		ObservedAt    string   `json:"observedAt"`
		Note          string   `json:"note"`
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("实际计费必须是对象: %w", err)
	}
	for field := range fields {
		if !actualBillingFields[field] {
			return fmt.Errorf("实际计费包含未知字段 %q", field)
		}
	}
	var decoded wire
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return fmt.Errorf("实际计费字段无效: %w", err)
	}
	if decoded.Amount == nil {
		return fmt.Errorf("amount 必须显式出现，null 或缺失表示未知而不是零")
	}
	if math.IsNaN(*decoded.Amount) || math.IsInf(*decoded.Amount, 0) {
		return fmt.Errorf("amount 必须是有限数值")
	}
	*in = ActualBillingInput{
		ObservationID: decoded.ObservationID, ProviderID: decoded.ProviderID,
		Amount: *decoded.Amount, Currency: decoded.Currency, Source: decoded.Source,
		ObservedAt: decoded.ObservedAt, Note: decoded.Note,
	}
	return nil
}

func (s *Service) RecordActualBilling(in ActualBillingInput) (ActualObservation, error) {
	observationID := strings.TrimSpace(in.ObservationID)
	if observationID == "" {
		return ActualObservation{}, badRequest("MISSING_OBSERVATION", "observationId 不能为空，用于账单观察去重")
	}
	providerID := strings.TrimSpace(in.ProviderID)
	if providerID == "" {
		return ActualObservation{}, badRequest("INVALID_PROVIDER", "providerId 不能为空")
	}
	currency := strings.ToUpper(strings.TrimSpace(in.Currency))
	if currency == "" || len(currency) > 8 {
		return ActualObservation{}, badRequest("INVALID_CURRENCY", "currency 必须是 ISO 货币代码")
	}
	micros, ok := amountMicros(in.Amount)
	if !ok {
		return ActualObservation{}, badRequest("INVALID_AMOUNT", "amount 必须是有限非负数，且小于 %.0f（按百万分之一单位存储）", maxAmount)
	}
	source := canonicalSource(in.Source)
	if !quotaSources[source] {
		return ActualObservation{}, badRequest("INVALID_SOURCE", "实际计费 source 只能是 official/gateway/manual/mock")
	}
	note := strings.TrimSpace(in.Note)
	explicitAt := strings.TrimSpace(in.ObservedAt)
	if explicitAt != "" {
		parsed, err := parseTime(explicitAt)
		if err != nil {
			return ActualObservation{}, badRequest("INVALID_OBSERVED_AT", "observedAt 必须是 RFC3339 时间")
		}
		explicitAt = parsed
	}

	// Without an explicit timestamp an identical retry must reuse the stored
	// observation time; otherwise every retry would look like a new bill.
	if explicitAt == "" {
		stored, found, err := s.findActualBilling(observationID)
		if err != nil {
			return ActualObservation{}, err
		}
		if found {
			if !sameActualObservation(stored, micros, currency, providerID, source, stored.ObservedAt, note) {
				return ActualObservation{}, conflict("OBSERVATION_CONFLICT", "observationId %q 已存在且内容不同，拒绝覆盖既有账单；请使用新的 observationId", observationID)
			}
			observation := stored.ActualObservation
			observation.Amount = float64(stored.micros) / 1e6
			observation.Duplicate = true
			return observation, nil
		}
	}
	observedAt := explicitAt
	if observedAt == "" {
		observedAt = s.nowText()
	}
	observedMoment, err := time.Parse(time.RFC3339Nano, observedAt)
	if err != nil {
		return ActualObservation{}, badRequest("INVALID_OBSERVED_AT", "observedAt 必须是 RFC3339 时间")
	}
	res, err := s.db.Exec(`INSERT INTO usage_actual_billing(observation_id,provider_id,amount_micros,currency,source,observed_at,observed_ms,note)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(observation_id) DO NOTHING`,
		observationID, providerID, micros, currency, source, observedAt, observedMoment.UnixMilli(), note)
	if err != nil {
		return ActualObservation{}, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return ActualObservation{}, err
	}
	if affected == 0 {
		// Lost a concurrent race. When the caller supplied no timestamp the
		// stored row is authoritative for time, so a locally generated value must
		// not turn an identical retry into a conflict.
		stored, found, err := s.findActualBilling(observationID)
		if err != nil {
			return ActualObservation{}, err
		}
		if !found {
			return ActualObservation{}, conflict("OBSERVATION_CONFLICT", "observationId %q 已存在但无法回读", observationID)
		}
		compareAt := observedAt
		if explicitAt == "" {
			compareAt = stored.ObservedAt
		}
		if !sameActualObservation(stored, micros, currency, providerID, source, compareAt, note) {
			return ActualObservation{}, conflict("OBSERVATION_CONFLICT", "observationId %q 已存在且内容不同，拒绝覆盖既有账单；请使用新的 observationId", observationID)
		}
		observation := stored.ActualObservation
		observation.Amount = float64(stored.micros) / 1e6
		observation.Duplicate = true
		return observation, nil
	}
	return ActualObservation{ProviderID: providerID, Amount: float64(micros) / 1e6, Currency: currency, Source: source, ObservedAt: observedAt, Note: note}, nil
}

type storedActualBilling struct {
	ActualObservation
	micros int64
}

func (s *Service) findActualBilling(observationID string) (storedActualBilling, bool, error) {
	var observation storedActualBilling
	err := s.db.QueryRow(`SELECT provider_id,amount_micros,currency,source,observed_at,note FROM usage_actual_billing WHERE observation_id=?`, observationID).
		Scan(&observation.ProviderID, &observation.micros, &observation.Currency, &observation.Source, &observation.ObservedAt, &observation.Note)
	if err == sql.ErrNoRows {
		return storedActualBilling{}, false, nil
	}
	if err != nil {
		return storedActualBilling{}, false, err
	}
	observation.Amount = float64(observation.micros) / 1e6
	return observation, true, nil
}

func sameActualObservation(stored storedActualBilling, micros int64, currency, providerID, source, observedAt, note string) bool {
	return stored.ProviderID == providerID &&
		stored.Currency == currency &&
		stored.Source == source &&
		stored.ObservedAt == observedAt &&
		stored.Note == note &&
		stored.micros == micros
}

type UnlinkedObservation struct {
	ID             int64   `json:"id"`
	ObservationID  string  `json:"observationId"`
	Source         string  `json:"source"`
	ProviderID     string  `json:"providerId"`
	Model          string  `json:"model"`
	WireAgent      *string `json:"wireAgent"`
	RecordPosition string  `json:"recordPosition"`
	Protocol       string  `json:"protocol"`
	At             string  `json:"at"`
	Counts         any     `json:"counts"`
	Reason         string  `json:"reason"`
	RecordedAt     string  `json:"recordedAt"`
}

func (s *Service) ListUnlinked(offset, limit int) ([]UnlinkedObservation, int, error) {
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM usage_unlinked`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(`SELECT id,observation_id,source,provider_id,model,wire_agent,record_position,protocol,at,counts_json,reason,recorded_at
		FROM usage_unlinked ORDER BY at_ms DESC,id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []UnlinkedObservation{}
	for rows.Next() {
		var item UnlinkedObservation
		var wireAgent, countsJSON sql.NullString
		if err := rows.Scan(&item.ID, &item.ObservationID, &item.Source, &item.ProviderID, &item.Model, &wireAgent, &item.RecordPosition, &item.Protocol, &item.At, &countsJSON, &item.Reason, &item.RecordedAt); err != nil {
			return nil, 0, err
		}
		if wireAgent.Valid {
			item.WireAgent = &wireAgent.String
		}
		if countsJSON.Valid {
			var counts any
			if err := json.Unmarshal([]byte(countsJSON.String), &counts); err == nil {
				item.Counts = counts
			}
		}
		out = append(out, item)
	}
	return out, total, rows.Err()
}
