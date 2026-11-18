package cache

import (
	"context"
	"time"

	"enhancedkimi/internal/storage"
)

// TurnStep is one completed model step, ordered oldest first. The panel draws its
// per-turn line from these, so the row is the usage record itself rather than a
// re-aggregation of a bucket: a line that is derived from hourly totals would
// smear one turn's spike across the whole hour.
type TurnStep struct {
	StepID             string   `json:"stepId"`
	Agent              string   `json:"agent"`
	Model              string   `json:"model"`
	At                 string   `json:"at"`
	TurnID             string   `json:"turnId"`
	InputOther         int64    `json:"inputOther"`
	InputCacheRead     int64    `json:"inputCacheRead"`
	InputCacheCreation int64    `json:"inputCacheCreation"`
	Output             int64    `json:"output"`
	FirstTokenMS       int64    `json:"firstTokenMs"`
	DecodeMS           int64    `json:"decodeMs"`
	// HitRate is input_cache_read over all input tokens, or nil when the provider
	// reported nothing to divide. A nil is never rendered as 0%.
	HitRate *float64 `json:"hitRate"`
	// Reported says whether the step carried cache fields at all.
	Reported string `json:"reported"`
	// Speed is the decoded tokens per second for this step, or nil when the step
	// was too short for the figure to mean anything.
	Speed *float64 `json:"speed"`
}

// TurnSteps answers `cache.turns`: the newest completed steps of one session.
// `before` is an exclusive upper bound on step time, so a caller can page
// backwards; an empty value starts from the newest step.
func (s *Store) TurnSteps(ctx context.Context, sessionID string, limit int, before time.Time) ([]TurnStep, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if sessionID == "" {
		return []TurnStep{}, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query := `SELECT step_uuid,agent,model,time,turn_id,input_other,input_cache_read,input_cache_creation,output,first_token_ms,decode_ms FROM cache_steps WHERE session_id=?`
	args := []any{sessionID}
	if !before.IsZero() {
		query += ` AND time<?`
		args = append(args, storage.SortableTime(before))
	}
	query += ` ORDER BY time DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	steps := make([]TurnStep, 0, limit)
	for rows.Next() {
		var step TurnStep
		var at string
		if err = rows.Scan(&step.StepID, &step.Agent, &step.Model, &at, &step.TurnID,
			&step.InputOther, &step.InputCacheRead, &step.InputCacheCreation, &step.Output,
			&step.FirstTokenMS, &step.DecodeMS); err != nil {
			return nil, err
		}
		step.At = at
		step.Reported = Reported
		if step.InputCacheRead == 0 && step.InputCacheCreation == 0 {
			step.Reported = NotReported
		}
		input := step.InputOther + step.InputCacheRead + step.InputCacheCreation
		if input > 0 && step.Reported == Reported {
			rate := round4(float64(step.InputCacheRead) / float64(input))
			step.HitRate = &rate
		}
		if step.Output >= minSpeedOutput && step.DecodeMS >= minSpeedDecodeMS {
			speed := round4(float64(step.Output) / (float64(step.DecodeMS) / 1000))
			step.Speed = &speed
		}
		steps = append(steps, step)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	// The query reads newest first so the LIMIT keeps the newest rows; the panel
	// draws left to right, so the answer is reversed once here instead of in the
	// renderer.
	for left, right := 0, len(steps)-1; left < right; left, right = left+1, right-1 {
		steps[left], steps[right] = steps[right], steps[left]
	}
	return steps, nil
}

func round4(value float64) float64 {
	return float64(int64(value*10000+0.5)) / 10000
}