package usage

import (
	"database/sql"
	"encoding/json"
	"enhancedkimi/internal/httpx"
	"net/http"
	"reflect"
)

func countsCover(next, previous Counts) bool {
	for i, v := range []*int64{previous.Fresh, previous.Input, previous.Output, previous.Read, previous.Write} {
		if v != nil && ([]*int64{next.Fresh, next.Input, next.Output, next.Read, next.Write})[i] == nil {
			return false
		}
	}
	return true
}

func observationDecision(tx *sql.Tx, id int64, event preparedEvent) (bool, string, error) {
	var state, source, model, project string
	var seq int64
	var priority int
	var f, i, o, r, w sql.NullInt64
	err := tx.QueryRow(`SELECT observation_state,sequence,source,authority,model,project_id,fresh,input,output,cache_read,cache_write FROM usage_events WHERE id=?`, id).Scan(&state, &seq, &source, &priority, &model, &project, &f, &i, &o, &r, &w)
	if err != nil {
		return false, "", err
	}
	old := Counts{nullInt(f), nullInt(i), nullInt(o), nullInt(r), nullInt(w)}
	var latest sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(sequence) FROM usage_observations WHERE event_id=? AND source=?`, id, event.source).Scan(&latest); err != nil {
		return false, "", err
	}
	if latest.Valid && event.sequence < latest.Int64 {
		return false, "STALE", nil
	}
	equal := reflect.DeepEqual(old, event.counts)
	if model != event.input.Model || project != event.input.ProjectID {
		return false, "IDENTITY_CONFLICT", nil
	}
	if source == event.source {
		if event.sequence < seq {
			return false, "STALE", nil
		}
		if event.sequence == seq {
			if equal && state == event.lifecycle {
				return false, "DUPLICATE", nil
			}
			return false, "SEQUENCE_CONFLICT", nil
		}
		if (state == "FINAL" || state == "CORRECTION") && event.lifecycle == "PARTIAL" {
			return false, "TERMINAL_PROTECTED", nil
		}
		if (state == "FINAL" || state == "CORRECTION") && event.lifecycle != "CORRECTION" && !equal {
			return false, "CORRECTION_REQUIRED", nil
		}
		if !countsCover(event.counts, old) {
			return false, "COVERAGE_CONFLICT", nil
		}
		if equal && state == event.lifecycle {
			return false, "DUPLICATE", nil
		}
		return true, "UPDATED", nil
	}
	if event.authority <= priority {
		if equal {
			return false, "DUPLICATE_SOURCE", nil
		}
		return false, "SOURCE_CONFLICT", nil
	}
	if !countsCover(event.counts, old) || (event.lifecycle == "PARTIAL" && (state == "FINAL" || state == "CORRECTION")) {
		return false, "SOURCE_PARTIAL_CONFLICT", nil
	}
	if equal {
		return true, "AUTHORITY_PROMOTED", nil
	}
	return true, "AUTHORITY_SELECTED_CONFLICT", nil
}

func recordObservation(tx *sql.Tx, id int64, event preparedEvent, decision, now string) error {
	counts, err := json.Marshal(event.counts)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT OR IGNORE INTO usage_observations(event_id,observation_id,source,lifecycle,sequence,counts_json,decision,observed_at) VALUES(?,?,?,?,?,?,?,?)`, id, event.duplicateID, event.source, event.lifecycle, event.sequence, string(counts), decision, now)
	return err
}

func setObservationState(tx *sql.Tx, id int64, event preparedEvent) error {
	_, err := tx.Exec(`UPDATE usage_events SET observation_state=?,sequence=? WHERE id=?`, event.lifecycle, event.sequence, id)
	return err
}

func (s *Service) handleObservations(w http.ResponseWriter, r *http.Request) {
	offset, limit := httpx.Page(r)
	rows, err := s.db.Query(`SELECT event_id,observation_id,source,lifecycle,sequence,counts_json,decision,observed_at FROM usage_observations ORDER BY observed_at DESC,event_id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		fail(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, seq int64
		var observation, source, state, counts, decision, at string
		if err := rows.Scan(&id, &observation, &source, &state, &seq, &counts, &decision, &at); err != nil {
			fail(w, err)
			return
		}
		items = append(items, map[string]any{"eventId": id, "observationId": observation, "source": source, "observationState": state, "sequence": seq, "counts": json.RawMessage(counts), "decision": decision, "observedAt": at})
	}
	if err := rows.Err(); err != nil {
		fail(w, err)
		return
	}
	rows.Close()
	var total int
	if err := s.db.QueryRow(`SELECT count(*) FROM usage_observations`).Scan(&total); err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"items": items, "total": total})
}
