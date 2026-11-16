package usage

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

// Ingest keeps one canonical request attempt and records every observation decision.
func (s *Service) Ingest(events []EventInput) (IngestResult, error) {
	if len(events) == 0 {
		return IngestResult{}, badRequest("EMPTY_EVENTS", "events 不能为空")
	}
	if len(events) > 5000 {
		return IngestResult{}, badRequest("TOO_MANY_EVENTS", "单次最多 5000 条事件")
	}
	prepared := make([]preparedEvent, 0, len(events))
	for index, input := range events {
		event, err := prepareEvent(index, input)
		if err != nil {
			return IngestResult{}, err
		}
		prepared = append(prepared, event)
	}
	result := IngestResult{Total: len(prepared)}
	now := s.nowText()
	tx, err := s.db.Begin()
	if err != nil {
		return IngestResult{}, err
	}
	defer tx.Rollback()
	for _, event := range prepared {
		if event.isSnapshot() {
			result.Snapshots++
			continue
		}
		var existingID int64
		err := tx.QueryRow(`SELECT id FROM usage_events WHERE canonical_key=?`, event.canonical).Scan(&existingID)
		switch {
		case err == sql.ErrNoRows:
			if event.input.AttemptID == "" || event.input.SessionID == "" || event.input.AgentID == "" {
				if err := insertUnlinked(tx, event, now); err != nil {
					return IngestResult{}, err
				}
				result.Unresolved++
				continue
			}
			id, err := insertEvent(tx, event, now)
			if err != nil {
				return IngestResult{}, err
			}
			if err := insertProvenance(tx, id, event, now); err != nil {
				return IngestResult{}, err
			}
			if event.price != nil {
				if err := upsertPrice(tx, event, now); err != nil {
					return IngestResult{}, err
				}
			}
			if err := setObservationState(tx, id, event); err != nil {
				return IngestResult{}, err
			}
			if err := recordObservation(tx, id, event, "ADDED", now); err != nil {
				return IngestResult{}, err
			}
			result.Added++
		case err != nil:
			return IngestResult{}, err
		default:
			update, decision, err := observationDecision(tx, existingID, event)
			if err != nil {
				return IngestResult{}, err
			}
			if update {
				if err := updateEventCounts(tx, existingID, event); err != nil {
					return IngestResult{}, err
				}
				if err := setObservationState(tx, existingID, event); err != nil {
					return IngestResult{}, err
				}
				result.Updated++
			} else {
				result.Duplicates++
			}
			if strings.Contains(decision, "CONFLICT") || decision == "CORRECTION_REQUIRED" {
				result.Conflicts++
				if _, err := tx.Exec(`UPDATE usage_events SET conflict_status=? WHERE id=?`, decision, existingID); err != nil {
					return IngestResult{}, err
				}
			}
			if err := recordObservation(tx, existingID, event, decision, now); err != nil {
				return IngestResult{}, err
			}

			if err := insertProvenance(tx, existingID, event, now); err != nil && !isUniqueViolation(err) {
				return IngestResult{}, err
			}
			if event.price != nil {
				if err := upsertPrice(tx, event, now); err != nil {
					return IngestResult{}, err
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return IngestResult{}, err
	}
	return result, nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE")
}

func insertEvent(tx *sql.Tx, event preparedEvent, now string) (int64, error) {
	res, err := tx.Exec(`INSERT INTO usage_events(canonical_key,duplicate_key,provider_id,model,request_id,attempt_id,session_id,project_id,agent_id,role,request_kind,protocol,source,authority,kind,at,at_ms,fresh,input,output,cache_read,cache_write,raw_json,price_json,observed_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		event.canonical, event.duplicateID, event.input.ProviderID, event.input.Model, event.input.RequestID, event.input.AttemptID,
		event.input.SessionID, event.input.ProjectID, event.input.AgentID, event.role, event.requestKind, event.input.Protocol,
		event.source, event.authority, "request", event.atText, event.at.UnixMilli(),
		event.counts.Fresh, event.counts.Input, event.counts.Output, event.counts.Read, event.counts.Write,
		nullableJSON(event.input.Raw), encodePrice(event.price), now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func updateEventCounts(tx *sql.Tx, id int64, event preparedEvent) error {
	_, err := tx.Exec(`UPDATE usage_events SET fresh=?,input=?,output=?,cache_read=?,cache_write=?,source=?,authority=?,role=?,request_kind=?,protocol=?,raw_json=?,price_json=?,attempt_id=?,session_id=?,agent_id=?,provider_id=?,model=?,at=?,at_ms=?
		WHERE id=?`,
		event.counts.Fresh, event.counts.Input, event.counts.Output, event.counts.Read, event.counts.Write,
		event.source, event.authority, event.role, event.requestKind, event.input.Protocol,
		nullableJSON(event.input.Raw), encodePrice(event.price), event.input.AttemptID, event.input.SessionID,
		event.input.AgentID, event.input.ProviderID, event.input.Model, event.atText, event.at.UnixMilli(), id)
	return err
}

func insertProvenance(tx *sql.Tx, eventID int64, event preparedEvent, now string) error {
	_, err := tx.Exec(`INSERT INTO usage_provenance(event_id,observation_id,source,authority,role,at,raw_json,recorded_at)
		VALUES(?,?,?,?,?,?,?,?)`, eventID, event.duplicateID, event.source, event.authority, event.role, event.atText, nullableJSON(event.input.Raw), now)
	return err
}

func insertUnlinked(tx *sql.Tx, event preparedEvent, now string) error {
	countsJSON, err := json.Marshal(event.counts)
	if err != nil {
		return err
	}
	reason := "missing attempt/session/agent identity"
	_, err = tx.Exec(`INSERT INTO usage_unlinked(observation_id,source,provider_id,model,wire_agent,record_position,protocol,at,at_ms,counts_json,raw_json,reason,recorded_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(observation_id) DO NOTHING`,
		event.duplicateID, event.source, event.input.ProviderID, event.input.Model, event.input.AgentID,
		event.input.RequestID+"#"+event.atText, event.input.Protocol, event.atText, event.at.UnixMilli(),
		string(countsJSON), nullableJSON(event.input.Raw), reason, now)
	return err
}

func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

func encodePrice(price *Price) any {
	if price == nil {
		return nil
	}
	payload, err := json.Marshal(price)
	if err != nil {
		return nil
	}
	return string(payload)
}

func decodePrice(raw sql.NullString) *Price {
	if !raw.Valid || raw.String == "" {
		return nil
	}
	var price Price
	if err := json.Unmarshal([]byte(raw.String), &price); err != nil {
		return nil
	}
	return &price
}

func decodeRaw(raw sql.NullString) any {
	if !raw.Valid || raw.String == "" {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(raw.String), &value); err != nil {
		return raw.String
	}
	return value
}

func upsertPrice(tx *sql.Tx, event preparedEvent, now string) error {
	price := event.price
	if _, err := tx.Exec(`INSERT INTO usage_prices(provider_id,model,currency,input_per_million,output_per_million,read_per_million,write_per_million,source,fetched_at,fetched_ms,observed_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(provider_id,model) DO UPDATE SET
			currency=excluded.currency,input_per_million=excluded.input_per_million,output_per_million=excluded.output_per_million,
			read_per_million=excluded.read_per_million,write_per_million=excluded.write_per_million,source=excluded.source,
			fetched_at=excluded.fetched_at,fetched_ms=excluded.fetched_ms,observed_at=excluded.observed_at
		WHERE excluded.fetched_ms > usage_prices.fetched_ms`,
		event.input.ProviderID, event.input.Model, price.Currency, price.Input, price.Output, price.Read, price.Write,
		price.Source, deref(price.FetchedAt), price.fetchedMs, now); err != nil {
		return err
	}
	return nil
}

func deref(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

type listFilter struct {
	ProjectID   string
	SessionID   string
	AgentID     string
	Model       string
	ProviderID  string
	Role        string
	RequestKind string
	FromMs      *int64
	ToMs        *int64
	Offset      int
	Limit       int
}

func buildFilter(filter listFilter) (string, []any) {
	clauses := []string{}
	args := []any{}
	add := func(sqlText string, value any) {
		clauses = append(clauses, sqlText)
		args = append(args, value)
	}
	if filter.ProjectID != "" {
		add("project_id=?", filter.ProjectID)
	}
	if filter.SessionID != "" {
		add("session_id=?", filter.SessionID)
	}
	if filter.AgentID != "" {
		add("agent_id=?", filter.AgentID)
	}
	if filter.Model != "" {
		add("model=?", filter.Model)
	}
	if filter.ProviderID != "" {
		add("provider_id=?", filter.ProviderID)
	}
	if filter.Role != "" {
		add("role=?", filter.Role)
	}
	if filter.RequestKind != "" {
		add("request_kind=?", filter.RequestKind)
	}
	if filter.FromMs != nil {
		add("at_ms>=?", *filter.FromMs)
	}
	if filter.ToMs != nil {
		add("at_ms<=?", *filter.ToMs)
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	return where, args
}

func (s *Service) ListEvents(filter listFilter, offset, limit int) ([]Event, int, error) {
	filter.Offset = offset
	filter.Limit = limit
	where, args := buildFilter(filter)
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM usage_events`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	queryArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.db.Query(`SELECT id,canonical_key,provider_id,model,request_id,attempt_id,session_id,project_id,agent_id,role,request_kind,protocol,source,authority,kind,at,
		fresh,input,output,cache_read,cache_write,raw_json,price_json,observed_at,observation_state,sequence,conflict_status
		FROM usage_events`+where+` ORDER BY at_ms DESC, id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []Event{}
	for rows.Next() {
		var event Event
		var fresh, input, output, read, write sql.NullInt64
		var rawJSON, priceJSON sql.NullString
		var authorityValue int
		if err := rows.Scan(&event.ID, &event.CanonicalKey, &event.ProviderID, &event.Model, &event.RequestID, &event.AttemptID,
			&event.SessionID, &event.ProjectID, &event.AgentID, &event.Role, &event.RequestKind, &event.Protocol,
			&event.Source, &authorityValue, &event.Kind, &event.At,
			&fresh, &input, &output, &read, &write, &rawJSON, &priceJSON, &event.ObservedAt, &event.ObservationState, &event.Sequence, &event.ConflictStatus); err != nil {
			return nil, 0, err
		}
		event.Fresh = nullInt(fresh)
		event.Input = nullInt(input)
		event.Output = nullInt(output)
		event.CacheRead = nullInt(read)
		event.CacheWrite = nullInt(write)
		event.Raw = decodeRaw(rawJSON)
		event.Price = decodePrice(priceJSON)
		event.Authoritative = authorityValue >= authority[sourceOfficial]
		items = append(items, event)
	}
	return items, total, rows.Err()
}

func nullInt(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	out := value.Int64
	return &out
}

func (s *Service) ProvenanceFor(eventIDs []int64) (map[int64][]Provenance, error) {
	out := map[int64][]Provenance{}
	if len(eventIDs) == 0 {
		return out, nil
	}
	ids := make([]int64, len(eventIDs))
	copy(ids, eventIDs)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.Query(`SELECT event_id,observation_id,source,authority,role,at,recorded_at FROM usage_provenance WHERE event_id IN (`+placeholders+`) ORDER BY event_id,recorded_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item Provenance
		if err := rows.Scan(&item.EventID, &item.ObservationID, &item.Source, &item.Authority, &item.Role, &item.At, &item.RecordedAt); err != nil {
			return nil, err
		}
		out[item.EventID] = append(out[item.EventID], item)
	}
	return out, rows.Err()
}
