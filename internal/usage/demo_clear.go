package usage

import (
	"database/sql"
	"enhancedkimi/internal/httpx"
	"net/http"
)

func (s *Service) ClearDemo() (map[string]int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	result := map[string]int64{"events": 0, "unlinked": 0, "quotas": 0, "preserved": 0}
	for i, input := range demoEvents() {
		event, err := prepareEvent(i, input)
		if err != nil {
			return nil, err
		}
		if event.isSnapshot() {
			continue
		}
		if input.AttemptID == "" {
			res, err := tx.Exec(`DELETE FROM usage_unlinked WHERE observation_id=? AND provider_id=? AND model=? AND source=? AND at=?`, event.duplicateID, input.ProviderID, input.Model, event.source, event.atText)
			if err != nil {
				return nil, err
			}
			n, _ := res.RowsAffected()
			result["unlinked"] += n
			continue
		}
		var id int64
		err = tx.QueryRow(`SELECT id FROM usage_events WHERE canonical_key=?`, event.canonical).Scan(&id)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		var foreign int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM usage_provenance WHERE event_id=? AND observation_id<>?`, id, event.duplicateID).Scan(&foreign); err != nil {
			return nil, err
		}
		if foreign > 0 {
			result["preserved"]++
			continue
		}
		for _, table := range []string{"usage_observations", "usage_provenance"} {
			if _, err = tx.Exec(`DELETE FROM `+table+` WHERE event_id=?`, id); err != nil {
				return nil, err
			}
		}
		if _, err = tx.Exec(`DELETE FROM usage_events WHERE id=?`, id); err != nil {
			return nil, err
		}
		result["events"]++
	}
	for _, q := range demoQuotas() {
		res, err := tx.Exec(`DELETE FROM usage_quota_snapshots WHERE ledger=? AND provider_id=? AND scope=? AND source=? AND observed_at=? AND note=? AND remaining IS NULL AND limit_value IS NULL AND unit IS NULL AND reset_at IS NULL AND currency IS NULL`, q.Ledger, q.ProviderID, q.Scope, q.Source, q.ObservedAt, q.Note)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		result["quotas"] += n
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) handleClearDemo(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !httpx.Decode(w, r, &body) {
		return
	}
	result, err := s.ClearDemo()
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}
