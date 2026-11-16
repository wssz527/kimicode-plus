package cache

import (
	"context"
	"time"
)

// Reported states answer "did the provider report cache fields at all?". A zero
// hit rate and an unreported model look identical in the numbers but mean
// different things to a user, so they are never collapsed (plan D4, M2 section 3).
const (
	Reported    = "REPORTED"
	NotReported = "NOT_REPORTED"
	NoSamples   = "NO_SAMPLES"
)

// Speed is the decoding speed sample aggregate. It is derived from
// llmServerDecodeMs, see Step.DecodeMS for the field evidence.
type Speed struct {
	State           string   `json:"state"`
	TokensPerSecond *float64 `json:"tokensPerSecond,omitempty"`
	Samples         int      `json:"samples"`
	Note            string   `json:"note,omitempty"`
}

// Totals is the token aggregate for one dimension (all models or one model).
type Totals struct {
	Steps              int   `json:"steps"`
	InputOther         int64 `json:"inputOther"`
	InputCacheRead     int64 `json:"inputCacheRead"`
	InputCacheCreation int64 `json:"inputCacheCreation"`
	Output             int64 `json:"output"`
	// HitRate is input_cache_read over all input tokens. Nil means it must not be
	// displayed as a number: either nothing was sampled or the provider does not
	// report cache fields.
	HitRate  *float64 `json:"hitRate"`
	Reported string   `json:"reported"`
}

// ModelBreakdown is Totals plus the model identity.
type ModelBreakdown struct {
	Model string `json:"model"`
	Totals
	Speed Speed  `json:"speed"`
	Note  string `json:"note,omitempty"`
}

// Summary answers cache.summary.
type Summary struct {
	GeneratedAt time.Time        `json:"generatedAt"`
	Hour        Totals           `json:"hour"`
	Today       Totals           `json:"today"`
	Models      []ModelBreakdown `json:"models"`
	Speed       Speed            `json:"speed"`
	Quota       Quota            `json:"quota"`
	// ReportingNote explains a global "not reported" state without implying 0%.
	ReportingNote string `json:"reportingNote,omitempty"`
}

// Quota is the allowance sub-object folded into cache.summary. It stays UNKNOWN
// when the official endpoint gives nothing usable.
type Quota struct {
	State  string `json:"state"`
	Source string `json:"source"`
	Note   string `json:"note,omitempty"`
	Value  any    `json:"value,omitempty"`
}

// SessionRow answers one cache.sessions entry.
type SessionRow struct {
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
	CWD       string `json:"cwd"`
	// Model is the newest model seen in this session, or UNKNOWN.
	Model string `json:"model"`
	// LastRequestAt is the newest main-agent step. It is what "距上次请求 N 分钟"
	// counts from; it deliberately ignores subagent activity.
	LastRequestAt *time.Time `json:"lastRequestAt"`
	IdleMinutes   *float64   `json:"idleMinutes"`
	// Recent is the aggregate over the newest 5 main-agent steps.
	Recent Totals `json:"recent"`
	// ContextTokens estimates how much context the session is carrying: the input
	// side of the newest step.
	ContextTokens  int64  `json:"contextTokens"`
	OpenBackground int    `json:"openBackground"`
	Blocked        bool   `json:"blocked"`
	Reported       string `json:"reported"`
	Note           string `json:"note,omitempty"`
}

// HistoryPoint is one bucket of cache.history.
type HistoryPoint struct {
	Bucket string `json:"bucket"`
	Totals Totals `json:"totals"`
	Speed  Speed  `json:"speed"`
}

// History answers cache.history.
type History struct {
	Range  string         `json:"range"`
	Unit   string         `json:"unit"`
	Points []HistoryPoint `json:"points"`
	Totals Totals         `json:"totals"`
}

// RetentionBucket answers one cache.retention row: how the *first* request after
// an idle gap of a given length performed.
type RetentionBucket struct {
	Label   string `json:"label"`
	MinMs   int64  `json:"minMs"`
	MaxMs   int64  `json:"maxMs"`
	Samples int    `json:"samples"`
	// HitRate of the first step after the gap. Nil when no sample is usable.
	HitRate  *float64 `json:"hitRate"`
	Reported string   `json:"reported"`
}

// Retention answers cache.retention. It is the only honest basis for choosing a
// keepalive threshold: it observes how long cache actually survives, and it does
// not turn into a countdown (plan D4).
type Retention struct {
	IdleBuckets []RetentionBucket `json:"idleBuckets"`
	Note        string            `json:"note"`
}

// retentionBounds are the idle gap buckets from the plan.
var retentionBounds = []struct {
	label string
	min   int64
	max   int64
}{
	{"0–2 分钟", 0, 2 * 60 * 1000},
	{"2–5 分钟", 2 * 60 * 1000, 5 * 60 * 1000},
	{"5–10 分钟", 5 * 60 * 1000, 10 * 60 * 1000},
	{"10–30 分钟", 10 * 60 * 1000, 30 * 60 * 1000},
	{"30 分钟以上", 30 * 60 * 1000, 0},
}

type rowTotals struct {
	steps, read, creation, other, output int64
}

func (r rowTotals) totals() Totals {
	totals := Totals{
		Steps:              int(r.steps),
		InputOther:         r.other,
		InputCacheRead:     r.read,
		InputCacheCreation: r.creation,
		Output:             r.output,
	}
	totals.Reported, totals.HitRate = reportedOf(r)
	return totals
}

// reportedOf decides the reporting state and hit rate for one aggregate.
//
// A model that has never produced a non-zero cache field is reported as
// NOT_REPORTED: on this machine inputCacheCreation is always 0, and several
// providers report both fields as 0 forever, so a 0% hit rate would be a
// fabricated number. Real low hit rates (magpie deepseek: 3.1%) do produce a
// non-zero read count and therefore do get a number.
func reportedOf(r rowTotals) (string, *float64) {
	if r.steps == 0 {
		return NoSamples, nil
	}
	if r.read == 0 && r.creation == 0 {
		return NotReported, nil
	}
	denominator := r.other + r.read + r.creation
	if denominator <= 0 {
		return NoSamples, nil
	}
	rate := float64(r.read) / float64(denominator)
	return Reported, &rate
}

type speedAggregate struct {
	output   int64
	decodeMS int64
	samples  int
}

func (a speedAggregate) speed() Speed {
	if a.samples == 0 || a.decodeMS <= 0 {
		return Speed{State: NoSamples, Note: "没有可用的解码时长样本"}
	}
	rate := float64(a.output) / (float64(a.decodeMS) / 1000)
	return Speed{State: Reported, TokensPerSecond: &rate, Samples: a.samples}
}

// Add folds one step into the aggregate when the step is a usable speed sample.
// Short steps are excluded: with output <= 20 or decode <= 300ms the quotient is
// noise (the real maximum was 47337 tok/s).
func (a *speedAggregate) add(step Step) {
	if step.Output <= minSpeedOutput || step.DecodeMS <= minSpeedDecodeMS {
		return
	}
	a.output += step.Output
	a.decodeMS += step.DecodeMS
	a.samples++
}

func (s *Store) sinceTotals(ctx context.Context, from time.Time) (rowTotals, speedAggregate, error) {
	return s.totalsWhere(ctx, `time >= ?`, from.UTC().Format(time.RFC3339Nano))
}

func (s *Store) totalsWhere(ctx context.Context, clause string, args ...any) (rowTotals, speedAggregate, error) {
	totals := rowTotals{}
	speed := speedAggregate{}
	query := `SELECT COUNT(*),COALESCE(SUM(input_cache_read),0),COALESCE(SUM(input_cache_creation),0),COALESCE(SUM(input_other),0),COALESCE(SUM(output),0) FROM cache_steps`
	if clause != "" {
		query += ` WHERE ` + clause
	}
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&totals.steps, &totals.read, &totals.creation, &totals.other, &totals.output); err != nil {
		return totals, speed, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT output,decode_ms FROM cache_steps WHERE `+speedClause(clause), args...)
	if err != nil {
		return totals, speed, err
	}
	defer rows.Close()
	for rows.Next() {
		var output, decode int64
		if err = rows.Scan(&output, &decode); err != nil {
			return totals, speed, err
		}
		speed.add(Step{Output: output, DecodeMS: decode})
	}
	return totals, speed, rows.Err()
}

// speedClause narrows a clause to the rows that can produce a usable sample,
// keeping the aggregate in SQL instead of pulling every step into Go.
func speedClause(clause string) string {
	filter := "output > " + itoa(minSpeedOutput) + " AND decode_ms > " + itoa(minSpeedDecodeMS)
	if clause == "" {
		return filter
	}
	return clause + " AND " + filter
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// Summary aggregates the last hour, today, and per-model rows.
func (s *Store) Summary(ctx context.Context, now time.Time) (Summary, error) {
	if err := s.ready(); err != nil {
		return Summary{}, err
	}
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	hour, hourSpeed, err := s.sinceTotals(ctx, now.Add(-time.Hour))
	if err != nil {
		return Summary{}, err
	}
	today, todaySpeed, err := s.sinceTotals(ctx, dayStart)
	if err != nil {
		return Summary{}, err
	}
	models, err := s.modelBreakdown(ctx, dayStart)
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{
		GeneratedAt: now.UTC(),
		Hour:        hour.totals(),
		Today:       today.totals(),
		Models:      models,
		Speed:       todaySpeed.speed(),
	}
	if hourSpeed.samples > 0 && hour.steps > 0 {
		summary.Speed = hourSpeed.speed()
	}
	if summary.Today.Reported == NotReported && summary.Hour.Reported == NotReported {
		summary.ReportingNote = "今日所有模型都未上报缓存命中数据，因此不显示命中率，也不按 0% 处理。"
	}
	return summary, nil
}

func (s *Store) modelBreakdown(ctx context.Context, from time.Time) ([]ModelBreakdown, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT model,COUNT(*),COALESCE(SUM(input_cache_read),0),COALESCE(SUM(input_cache_creation),0),COALESCE(SUM(input_other),0),COALESCE(SUM(output),0) FROM cache_steps WHERE time>=? GROUP BY model ORDER BY COUNT(*) DESC`, from.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	models := []ModelBreakdown{}
	for rows.Next() {
		row := ModelBreakdown{}
		var totals rowTotals
		if err = rows.Scan(&row.Model, &totals.steps, &totals.read, &totals.creation, &totals.other, &totals.output); err != nil {
			return nil, err
		}
		row.Totals = totals.totals()
		if row.Totals.Reported == NotReported {
			row.Note = "供应商未上报缓存数据"
		}
		models = append(models, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for index := range models {
		speed, speedErr := s.modelSpeed(ctx, models[index].Model, from)
		if speedErr != nil {
			return nil, speedErr
		}
		models[index].Speed = speed
	}
	return models, nil
}

func (s *Store) modelSpeed(ctx context.Context, model string, from time.Time) (Speed, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT output,decode_ms FROM cache_steps WHERE model=? AND time>=? AND output>? AND decode_ms>?`, model, from.UTC().Format(time.RFC3339Nano), minSpeedOutput, minSpeedDecodeMS)
	if err != nil {
		return Speed{State: NoSamples}, err
	}
	defer rows.Close()
	speed := speedAggregate{}
	for rows.Next() {
		var output, decode int64
		if err = rows.Scan(&output, &decode); err != nil {
			return Speed{State: NoSamples}, err
		}
		speed.add(Step{Output: output, DecodeMS: decode})
	}
	return speed.speed(), rows.Err()
}

// Sessions lists recently active sessions newest first.
func (s *Store) Sessions(ctx context.Context, now time.Time, limit int) ([]SessionRow, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	// Order by the newest recorded main-agent step, not by state.json's updated_at:
	// a session with no steps has nothing to show and must not outrank an active one
	// just because its metadata was touched more recently.
	rows, err := s.db.QueryContext(ctx, `SELECT s.session_id,s.title,s.cwd FROM cache_sessions s LEFT JOIN (SELECT session_id, MAX(time) AS last_step FROM cache_steps WHERE agent='main' GROUP BY session_id) c ON c.session_id=s.session_id ORDER BY (c.last_step IS NULL), c.last_step DESC, s.updated_at DESC,s.scanned_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []SessionRow{}
	for rows.Next() {
		row := SessionRow{Reported: NoSamples}
		if err = rows.Scan(&row.SessionID, &row.Title, &row.CWD); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for index := range result {
		if err = s.fillSession(ctx, &result[index], now); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *Store) fillSession(ctx context.Context, row *SessionRow, now time.Time) error {
	latest, ok, err := s.LastMainStep(ctx, row.SessionID)
	if err != nil {
		return err
	}
	if ok {
		at := latest.At
		row.LastRequestAt = &at
		idle := now.Sub(at).Minutes()
		row.IdleMinutes = &idle
		row.Model = latest.Model
		row.ContextTokens = latest.InputOther + latest.InputCacheRead + latest.InputCacheCreation
	}
	recent, speed, err := s.recentTotals(ctx, row.SessionID, 5)
	if err != nil {
		return err
	}
	row.Recent = recent.totals()
	row.Reported = row.Recent.Reported
	if row.Reported == NotReported {
		row.Note = "供应商未上报缓存数据"
	}
	_ = speed
	live, err := s.Live(ctx, row.SessionID)
	if err != nil {
		return err
	}
	row.OpenBackground = live.OpenBackground
	row.Blocked = live.Blocked
	return nil
}

// recentTotals aggregates the newest N main-agent steps only, so "最近 5 步命中率"
// means the recent shape of the conversation rather than its whole history.
func (s *Store) recentTotals(ctx context.Context, sessionID string, count int) (rowTotals, speedAggregate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT input_cache_read,input_cache_creation,input_other,output,decode_ms FROM cache_steps WHERE session_id=? AND agent='main' ORDER BY time DESC,rowid DESC LIMIT ?`, sessionID, count)
	if err != nil {
		return rowTotals{}, speedAggregate{}, err
	}
	defer rows.Close()
	totals := rowTotals{}
	speed := speedAggregate{}
	for rows.Next() {
		var read, creation, other, output, decode int64
		if err = rows.Scan(&read, &creation, &other, &output, &decode); err != nil {
			return totals, speed, err
		}
		totals.steps++
		totals.read += read
		totals.creation += creation
		totals.other += other
		totals.output += output
		speed.add(Step{Output: output, DecodeMS: decode})
	}
	return totals, speed, rows.Err()
}

// History aggregates by hour or day over one of the three accepted ranges.
func (s *Store) History(ctx context.Context, now time.Time, span string) (History, error) {
	if err := s.ready(); err != nil {
		return History{}, err
	}
	unit := "hour"
	window := 24 * time.Hour
	switch span {
	case "", "24h":
		span, unit, window = "24h", "hour", 24*time.Hour
	case "7d":
		unit, window = "day", 7*24*time.Hour
	case "30d":
		unit, window = "day", 30*24*time.Hour
	default:
		return History{}, errInvalidRange
	}
	from := now.Add(-window)
	step := time.Hour
	format := "2006-01-02T15:00"
	if unit == "day" {
		step = 24 * time.Hour
		format = "2006-01-02"
	}
	buckets := map[string]*HistoryPoint{}
	order := []string{}
	cursor := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, from.Location())
	if unit == "hour" {
		cursor = time.Date(from.Year(), from.Month(), from.Day(), from.Hour(), 0, 0, 0, from.Location())
	}
	for cursor.Before(now) || cursor.Equal(now) {
		label := cursor.Format(format)
		if _, exists := buckets[label]; !exists {
			buckets[label] = &HistoryPoint{Bucket: label}
			order = append(order, label)
		}
		cursor = cursor.Add(step)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT time,input_cache_read,input_cache_creation,input_other,output,decode_ms FROM cache_steps WHERE time>=? ORDER BY time ASC`, from.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return History{}, err
	}
	defer rows.Close()
	type acc struct {
		totals rowTotals
		speed  speedAggregate
	}
	byBucket := map[string]*acc{}
	for rows.Next() {
		var at string
		var read, creation, other, output, decode int64
		if err = rows.Scan(&at, &read, &creation, &other, &output, &decode); err != nil {
			return History{}, err
		}
		parsed := parseTime(at)
		if parsed.IsZero() {
			continue
		}
		label := parsed.In(now.Location()).Format(format)
		if _, exists := buckets[label]; !exists {
			continue
		}
		entry, exists := byBucket[label]
		if !exists {
			entry = &acc{}
			byBucket[label] = entry
		}
		entry.totals.steps++
		entry.totals.read += read
		entry.totals.creation += creation
		entry.totals.other += other
		entry.totals.output += output
		entry.speed.add(Step{Output: output, DecodeMS: decode})
	}
	if err = rows.Err(); err != nil {
		return History{}, err
	}
	points := make([]HistoryPoint, 0, len(order))
	overall := rowTotals{}
	overallSpeed := speedAggregate{}
	for _, label := range order {
		point := *buckets[label]
		if entry, exists := byBucket[label]; exists {
			point.Totals = entry.totals.totals()
			point.Speed = entry.speed.speed()
			overall.steps += entry.totals.steps
			overall.read += entry.totals.read
			overall.creation += entry.totals.creation
			overall.other += entry.totals.other
			overall.output += entry.totals.output
			overallSpeed.output += entry.speed.output
			overallSpeed.decodeMS += entry.speed.decodeMS
			overallSpeed.samples += entry.speed.samples
		} else {
			point.Totals = Totals{Reported: NoSamples}
			point.Speed = Speed{State: NoSamples}
		}
		points = append(points, point)
	}
	return History{Range: span, Unit: unit, Points: points, Totals: overall.totals()}, nil
}

// Retention measures the hit rate of the first request after an idle gap. This is
// the only honest way to help a user pick a keepalive threshold: it never turns
// into a countdown, because the real TTL is not published (plan D4).
func (s *Store) Retention(ctx context.Context, now time.Time) (Retention, error) {
	if err := s.ready(); err != nil {
		return Retention{}, err
	}
	type bucketAgg struct {
		read, creation, other, steps int64
	}
	aggregates := make([]bucketAgg, len(retentionBounds))
	rows, err := s.db.QueryContext(ctx, `SELECT session_id,time,input_cache_read,input_cache_creation,input_other FROM cache_steps WHERE agent='main' ORDER BY session_id,time ASC`)
	if err != nil {
		return Retention{}, err
	}
	defer rows.Close()
	previous := map[string]time.Time{}
	for rows.Next() {
		var sessionID, at string
		var read, creation, other int64
		if err = rows.Scan(&sessionID, &at, &read, &creation, &other); err != nil {
			return Retention{}, err
		}
		parsed := parseTime(at)
		if parsed.IsZero() {
			continue
		}
		last, seen := previous[sessionID]
		previous[sessionID] = parsed
		if !seen {
			continue
		}
		gap := parsed.Sub(last).Milliseconds()
		if gap < 0 {
			continue
		}
		for index, bound := range retentionBounds {
			if gap < bound.min {
				continue
			}
			if bound.max > 0 && gap >= bound.max {
				continue
			}
			aggregates[index].steps++
			aggregates[index].read += read
			aggregates[index].creation += creation
			aggregates[index].other += other
			break
		}
	}
	if err = rows.Err(); err != nil {
		return Retention{}, err
	}
	buckets := make([]RetentionBucket, 0, len(retentionBounds))
	for index, bound := range retentionBounds {
		totals := rowTotals{steps: aggregates[index].steps, read: aggregates[index].read, creation: aggregates[index].creation, other: aggregates[index].other}
		state, rate := reportedOf(totals)
		buckets = append(buckets, RetentionBucket{
			Label: bound.label, MinMs: bound.min, MaxMs: bound.max,
			Samples: int(totals.steps), HitRate: rate, Reported: state,
		})
	}
	return Retention{
		IdleBuckets: buckets,
		Note:        "按“距上一次请求的空闲时长”统计每个区间里第一次请求的命中率。真实的缓存保留时长没有公开值，这里只呈现观测结果，不换算成倒计时。",
	}, nil
}

var errInvalidRange = &rangeError{}

type rangeError struct{}

func (e *rangeError) Error() string { return "历史区间只能是 24h、7d 或 30d" }

// AllSessions lists every persisted session, newest first. The UI uses Sessions with
// a page limit; verification needs the full set so an older session can be checked.
func (s *Store) AllSessions(ctx context.Context, now time.Time) ([]SessionRow, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT s.session_id,s.title,s.cwd FROM cache_sessions s LEFT JOIN (SELECT session_id, MAX(time) AS last_step FROM cache_steps WHERE agent='main' GROUP BY session_id) c ON c.session_id=s.session_id ORDER BY (c.last_step IS NULL), c.last_step DESC, s.updated_at DESC,s.scanned_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []SessionRow{}
	for rows.Next() {
		row := SessionRow{Reported: NoSamples}
		if err = rows.Scan(&row.SessionID, &row.Title, &row.CWD); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for index := range result {
		if err = s.fillSession(ctx, &result[index], now); err != nil {
			return nil, err
		}
	}
	return result, nil
}
