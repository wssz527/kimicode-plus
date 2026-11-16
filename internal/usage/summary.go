package usage

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

type fieldTotal struct {
	Sum      *int64 `json:"sum"`
	Known    int    `json:"known"`
	Total    int    `json:"total"`
	Complete bool   `json:"complete"`
}

type Coverage struct {
	Input      fieldTotal `json:"input"`
	Fresh      fieldTotal `json:"fresh"`
	Output     fieldTotal `json:"output"`
	CacheRead  fieldTotal `json:"cacheRead"`
	CacheWrite fieldTotal `json:"cacheWrite"`
}

type Estimate struct {
	Kind           string   `json:"kind"`
	Amount         *float64 `json:"amount"`
	Currency       *string  `json:"currency"`
	Currencies     []string `json:"currencies"`
	Complete       bool     `json:"complete"`
	PricedEvents   int      `json:"pricedEvents"`
	UnpricedEvents int      `json:"unpricedEvents"`
	UnknownUsage   int      `json:"unknownUsageEvents"`
	PartialKnown   bool     `json:"partialKnown"`
	PriceSources   []string `json:"priceSources"`
	PriceFetchedAt *string  `json:"priceFetchedAt"`
	// CostBasis says whether the amount used the price captured with each event
	// (event_price) or a later saved price as a retrospective estimate
	// (CURRENT_PRICE_FALLBACK / CURRENT_PRICE_ONLY). A fallback amount is a
	// current-price estimate, never a historical charge.
	CostBasis             string `json:"costBasis"`
	LateFallbackEvents    int    `json:"latePriceFallbackEvents"`
	InconsistentUsage     int    `json:"inconsistentUsageEvents"`
	UnknownProtocolEvents int    `json:"unknownProtocolEvents"`
	Unit                  string `json:"unit"`
	Note                  string `json:"note"`
}

type ActualBilling struct {
	Kind         string              `json:"kind"`
	Amount       *float64            `json:"amount"`
	Currency     *string             `json:"currency"`
	Currencies   []string            `json:"currencies"`
	Observations []ActualObservation `json:"observations"`
	// Attribution states whether the amount is provider-level only. Billing
	// rows have no project/session/agent dimension, so those filters can never
	// be applied to them.
	Attribution    string              `json:"attribution"`
	Scope          map[string]any      `json:"scope"`
	ProviderLedger *ActualBillingTotal `json:"providerLedger,omitempty"`
	Note           string              `json:"note"`
}

// ActualBillingTotal is a provider+time-window total. It is deliberately
// separate from any event list so it can never be read as the cost of the
// filtered events.
type ActualBillingTotal struct {
	Amount       *float64            `json:"amount"`
	Currency     *string             `json:"currency"`
	Currencies   []string            `json:"currencies"`
	Observations []ActualObservation `json:"observations"`
}

type ActualObservation struct {
	ProviderID string  `json:"providerId"`
	Amount     float64 `json:"amount"`
	Currency   string  `json:"currency"`
	Source     string  `json:"source"`
	ObservedAt string  `json:"observedAt"`
	Note       string  `json:"note"`
	// Duplicate is set only on the write receipt when an identical observation
	// was already stored; the ledger itself never marks rows.
	Duplicate bool `json:"duplicate,omitempty"`
}

type QuotaSnapshot struct {
	ID         int64   `json:"id"`
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

type Summary struct {
	Requests             int                `json:"requests"`
	KnownRequests        int                `json:"knownRequests"`
	PartialKnownRequests int                `json:"partialKnownRequests"`
	UnknownRequests      int                `json:"unknownRequests"`
	Input                *int64             `json:"input"`
	Output               *int64             `json:"output"`
	Fresh                *int64             `json:"fresh"`
	CacheRead            *int64             `json:"cacheRead"`
	CacheWrite           *int64             `json:"cacheWrite"`
	PartialKnown         Coverage           `json:"partialKnown"`
	Coverage             map[string]float64 `json:"coverage"`
	Estimate             Estimate           `json:"estimate"`
	Actual               ActualBilling      `json:"actual"`
	Subscription         SubscriptionLedger `json:"subscription"`
	Filters              map[string]any     `json:"filters"`
	GeneratedAt          string             `json:"generatedAt"`
	Note                 string             `json:"note"`
}

type SubscriptionLedger struct {
	Kind       string  `json:"kind"`
	Remaining  *string `json:"remaining"`
	ResetAt    *string `json:"resetAt"`
	Source     *string `json:"source"`
	ObservedAt *string `json:"observedAt"`
	Note       string  `json:"note"`
}

type summaryEvent struct {
	providerID string
	model      string
	protocol   string
	atMs       int64
	fresh      *int64
	input      *int64
	output     *int64
	read       *int64
	write      *int64
	price      *Price
}

func (s *Service) Summary(filter listFilter) (Summary, error) {
	where, args := buildFilter(filter)
	rows, err := s.db.Query(`SELECT provider_id,model,protocol,at_ms,fresh,input,output,cache_read,cache_write,price_json FROM usage_events`+where, args...)
	if err != nil {
		return Summary{}, err
	}
	events := []summaryEvent{}
	for rows.Next() {
		var item summaryEvent
		var fresh, input, output, read, write sql.NullInt64
		var priceJSON sql.NullString
		if err := rows.Scan(&item.providerID, &item.model, &item.protocol, &item.atMs, &fresh, &input, &output, &read, &write, &priceJSON); err != nil {
			rows.Close()
			return Summary{}, err
		}
		item.fresh = nullInt(fresh)
		item.input = nullInt(input)
		item.output = nullInt(output)
		item.read = nullInt(read)
		item.write = nullInt(write)
		item.price = decodePrice(priceJSON)
		events = append(events, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Summary{}, err
	}
	rows.Close()

	summary := Summary{Filters: map[string]any{}, GeneratedAt: s.nowText(), Note: "估算与官方账单、订阅额度、网关余额完全分离；缺失计数显式 null"}
	if filter.ProjectID != "" {
		summary.Filters["projectId"] = filter.ProjectID
	}
	if filter.SessionID != "" {
		summary.Filters["sessionId"] = filter.SessionID
	}
	if filter.AgentID != "" {
		summary.Filters["agentId"] = filter.AgentID
	}
	if filter.Model != "" {
		summary.Filters["model"] = filter.Model
	}
	if filter.FromMs != nil {
		summary.Filters["from"] = time.UnixMilli(*filter.FromMs).UTC().Format(time.RFC3339Nano)
	}
	if filter.ToMs != nil {
		summary.Filters["to"] = time.UnixMilli(*filter.ToMs).UTC().Format(time.RFC3339Nano)
	}
	summary.Requests = len(events)
	summary.PartialKnown = Coverage{
		Input:      fieldTotal{Total: len(events)},
		Fresh:      fieldTotal{Total: len(events)},
		Output:     fieldTotal{Total: len(events)},
		CacheRead:  fieldTotal{Total: len(events)},
		CacheWrite: fieldTotal{Total: len(events)},
	}
	var partialCount int
	for _, event := range events {
		shape, knownProtocol := shapeFor(event.protocol)
		known := 0
		required := 4 // input, fresh, output, read are reported by every supported protocol
		if knownProtocol && shape.cacheWrite {
			required = 5
		}
		accumulate(&summary.PartialKnown.Input, event.input, &known)
		accumulate(&summary.PartialKnown.Fresh, event.fresh, &known)
		accumulate(&summary.PartialKnown.Output, event.output, &known)
		accumulate(&summary.PartialKnown.CacheRead, event.read, &known)
		accumulate(&summary.PartialKnown.CacheWrite, event.write, &known)
		if !knownProtocol {
			required = 5
		}
		switch {
		case known == 0:
			summary.UnknownRequests++
		case known >= required:
			summary.KnownRequests++
		default:
			summary.PartialKnownRequests++
			partialCount++
		}
	}
	finalize(&summary.PartialKnown.Input)
	finalize(&summary.PartialKnown.Fresh)
	finalize(&summary.PartialKnown.Output)
	finalize(&summary.PartialKnown.CacheRead)
	finalize(&summary.PartialKnown.CacheWrite)
	// A top-level total is only reported when every row has that field; otherwise
	// it stays null and the partial known sum remains available below.
	summary.Input = completeSum(summary.PartialKnown.Input)
	summary.Fresh = completeSum(summary.PartialKnown.Fresh)
	summary.Output = completeSum(summary.PartialKnown.Output)
	summary.CacheRead = completeSum(summary.PartialKnown.CacheRead)
	summary.CacheWrite = completeSum(summary.PartialKnown.CacheWrite)
	summary.Coverage = map[string]float64{
		"input":      ratio(summary.PartialKnown.Input),
		"fresh":      ratio(summary.PartialKnown.Fresh),
		"output":     ratio(summary.PartialKnown.Output),
		"cacheRead":  ratio(summary.PartialKnown.CacheRead),
		"cacheWrite": ratio(summary.PartialKnown.CacheWrite),
	}
	prices, err := s.priceTable(events)
	if err != nil {
		return Summary{}, err
	}
	summary.Estimate = estimate(events, prices, partialCount)
	actual, err := s.actualBilling(filter, events)
	if err != nil {
		return Summary{}, err
	}
	summary.Actual = actual
	subscription, err := s.subscriptionLedger(filter.ProviderID)
	if err != nil {
		return Summary{}, err
	}
	summary.Subscription = subscription
	return summary, nil
}

func accumulate(field *fieldTotal, value *int64, known *int) {
	if value == nil {
		return
	}
	if field.Sum == nil {
		field.Sum = new(int64)
	}
	*field.Sum += *value
	field.Known++
	*known++
}

func finalize(field *fieldTotal) {
	field.Complete = field.Total > 0 && field.Known == field.Total
	if field.Known == 0 {
		field.Sum = nil
	}
}

func completeSum(field fieldTotal) *int64 {
	if !field.Complete {
		return nil
	}
	return field.Sum
}

func ratio(field fieldTotal) float64 {
	if field.Total == 0 {
		return 0
	}
	return float64(field.Known) / float64(field.Total)
}

// priceTable reads saved prices once, before any long-lived row iterator is
// opened, because the pool has a single connection.
func (s *Service) priceTable(events []summaryEvent) (map[modelKey]Price, error) {
	needed := map[modelKey]bool{}
	for _, event := range events {
		if event.price == nil {
			needed[modelKey{providerID: event.providerID, model: event.model}] = true
		}
	}
	out := map[modelKey]Price{}
	if len(needed) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(`SELECT provider_id,model,currency,input_per_million,output_per_million,read_per_million,write_per_million,source,fetched_at FROM usage_prices`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key modelKey
		var price Price
		var read, write sql.NullFloat64
		var fetchedAt string
		if err := rows.Scan(&key.providerID, &key.model, &price.Currency, &price.Input, &price.Output, &read, &write, &price.Source, &fetchedAt); err != nil {
			return nil, err
		}
		if read.Valid {
			value := read.Float64
			price.Read = &value
		}
		if write.Valid {
			value := write.Float64
			price.Write = &value
		}
		price.FetchedAt = &fetchedAt
		price.Unit = priceUnit
		if needed[key] {
			out[key] = price
		}
	}
	return out, rows.Err()
}

type modelKey struct {
	providerID string
	model      string
}

func estimate(events []summaryEvent, savedPrices map[modelKey]Price, partialCount int) Estimate {
	out := Estimate{Kind: "UNKNOWN", Unit: priceUnit, Currencies: []string{}, PriceSources: []string{}, Complete: true}
	if len(events) == 0 {
		out.Note = "没有可估算事件"
		return out
	}
	amounts := map[string]float64{}
	prices := map[string]Price{}
	tally := costTally{}
	fallback := 0
	for _, event := range events {
		price := event.price
		usedFallback := false
		if price == nil {
			current, ok := savedPrices[modelKey{providerID: event.providerID, model: event.model}]
			if !ok {
				tally.missingPrice++
				continue
			}
			price = &current
			usedFallback = true
		}
		cost, result := computeCost(event, price)
		if result != costOK {
			tallyFor(result, &tally)
			continue
		}
		if usedFallback {
			fallback++
		}
		amounts[price.Currency] += cost
		prices[price.Currency] = *price
		out.PricedEvents++
		if !contains(out.PriceSources, price.Source) {
			out.PriceSources = append(out.PriceSources, price.Source)
		}
	}
	unpriced := tally.missingPrice
	unusable := tally.missingUsage + tally.inconsistent + tally.unknownProto
	out.UnpricedEvents = unpriced
	out.UnknownUsage = unusable
	out.PartialKnown = unpriced > 0 || unusable > 0 || partialCount > 0 || fallback > 0
	out.CostBasis = "event_price"
	if fallback > 0 {
		out.CostBasis = "CURRENT_PRICE_FALLBACK"
		if fallback == out.PricedEvents {
			out.CostBasis = "CURRENT_PRICE_ONLY"
		}
	}
	out.LateFallbackEvents = fallback
	out.InconsistentUsage = tally.inconsistent
	out.UnknownProtocolEvents = tally.unknownProto
	out.Currencies = keys(amounts)
	sort.Strings(out.Currencies)
	if out.PricedEvents > 0 {
		out.Kind = "ESTIMATE"
	}
	if len(amounts) == 1 {
		for currency, amount := range amounts {
			out.Currency = &currency
			out.Amount = &amount
			price := prices[currency]
			out.PriceFetchedAt = price.FetchedAt
		}
	} else if len(amounts) > 1 {
		out.Note = "多币种估算，汇总金额不可直接相加"
	}
	out.Complete = out.PricedEvents > 0 && unpriced == 0 && unusable == 0 && fallback == 0 && !out.PartialKnown
	notes := make([]string, 0, 4)
	if unpriced > 0 {
		notes = append(notes, fmt.Sprintf("%d 条事件没有任何价格", unpriced))
	}
	if tally.missingUsage > 0 {
		notes = append(notes, fmt.Sprintf("%d 条事件缺少计费所需计数", tally.missingUsage))
	}
	if tally.inconsistent > 0 {
		notes = append(notes, fmt.Sprintf("%d 条事件的缓存计数与输入不一致，拒绝双计", tally.inconsistent))
	}
	if tally.unknownProto > 0 {
		notes = append(notes, fmt.Sprintf("%d 条事件协议无法确定计费口径", tally.unknownProto))
	}
	if fallback > 0 {
		notes = append(notes, fmt.Sprintf("%d 条无价格事件使用了当前保存价格，属于事后按现价估算，不是当时的历史价格", fallback))
	}
	if len(notes) > 0 {
		out.Note = strings.Join(notes, "；")
	}
	if out.PricedEvents == 0 {
		out.Note = "没有可完整计费的事件，估算未知"
	}
	return out
}

// pricingShape is what a protocol actually bills. nil means the protocol does
// not report the field at all (for example cache writes under OpenAI), which is
// different from a reported null: only a shape that requires the field fails
// when it is missing.
type pricingShape struct {
	freshInput bool
	totalInput bool
	cacheRead  bool
	cacheWrite bool
}

func shapeFor(protocol string) (pricingShape, bool) {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "openai", "chat", "chat_completions", "openai-chat", "responses", "openai-responses":
		return pricingShape{freshInput: true, cacheRead: true}, true
	case "anthropic", "anthropic-messages":
		return pricingShape{freshInput: true, cacheRead: true, cacheWrite: true}, true
	case "gemini", "google", "google-gemini":
		return pricingShape{freshInput: true, cacheRead: true}, true
	default:
		return pricingShape{}, false
	}
}

func priceKnown(price *Price) bool {
	if price == nil || price.Currency == "" {
		return false
	}
	if !finite(price.Input) || !finite(price.Output) {
		return false
	}
	if price.Read != nil && !finite(*price.Read) {
		return false
	}
	if price.Write != nil && !finite(*price.Write) {
		return false
	}
	return true
}

const (
	costOK      = "OK"
	costMissing = "MISSING_USAGE"
	costInvalid = "INCONSISTENT_USAGE"
	costUnknown = "UNKNOWN_PROTOCOL"
	costNoPrice = "MISSING_PRICE"
)

// computeCost returns a cost only when the protocol's billed fields are all
// known and internally consistent. It never substitutes total input for fresh
// input when the cache split is unknown, and it never turns null into zero.
func computeCost(event summaryEvent, price *Price) (float64, string) {
	shape, ok := shapeFor(event.protocol)
	if !ok {
		return 0, costUnknown
	}
	if !priceKnown(price) {
		return 0, costNoPrice
	}
	if event.input == nil || event.fresh == nil || event.output == nil {
		return 0, costMissing
	}
	// A protocol that reports cache reads needs the split known, but a reported
	// zero needs no cache rate: zero cached tokens cost nothing.
	if shape.cacheRead && event.read == nil {
		return 0, costMissing
	}
	// Cache writes are N/A for OpenAI/Gemini. A non-zero write there contradicts
	// the protocol's own accounting, so it is treated as inconsistent instead of
	// dereferencing a price field that does not exist for that shape.
	if !shape.cacheWrite && event.write != nil && *event.write > 0 {
		return 0, costInvalid
	}
	if shape.cacheWrite && event.write == nil {
		return 0, costMissing
	}
	read := int64(0)
	if event.read != nil {
		read = *event.read
	}
	write := int64(0)
	if event.write != nil {
		write = *event.write
	}
	if read > *event.input || write > *event.input || *event.fresh+read > *event.input {
		return 0, costInvalid
	}
	if read > 0 && price.Read == nil {
		return 0, costNoPrice
	}
	if write > 0 && price.Write == nil {
		return 0, costNoPrice
	}
	total := float64(*event.fresh) * price.Input / 1e6
	total += float64(*event.output) * price.Output / 1e6
	if read > 0 {
		total += float64(read) * *price.Read / 1e6
	}
	if write > 0 {
		total += float64(write) * *price.Write / 1e6
	}
	if !finite(total) || total < 0 {
		return 0, costInvalid
	}
	return total, costOK
}

// estimateSkippedCounting tallies why events were not priced. The caller treats
// any non-zero counter as incomplete coverage.
type costTally struct {
	priced       int
	missingUsage int
	inconsistent int
	unknownProto int
	missingPrice int
}

func tallyFor(result string, tally *costTally) {
	switch result {
	case costOK:
		tally.priced++
	case costMissing:
		tally.missingUsage++
	case costInvalid:
		tally.inconsistent++
	case costUnknown:
		tally.unknownProto++
	case costNoPrice:
		tally.missingPrice++
	}
}

func (s *Service) actualBilling(filter listFilter, events []summaryEvent) (ActualBilling, error) {
	out := ActualBilling{
		Kind:         "UNKNOWN",
		Currencies:   []string{},
		Observations: []ActualObservation{},
		Attribution:  "PROVIDER_LEVEL",
		Scope:        map[string]any{},
		Note:         "账单行只有 provider 与时间维度；项目/会话/Agent 不能归属，因此不会把供应商总额当作该范围的实际费用",
	}
	providers := map[string]bool{}
	for _, event := range events {
		providers[event.providerID] = true
	}
	for _, provider := range nonEmpty(filter.ProviderID) {
		providers[provider] = true
	}
	if len(providers) == 0 {
		out.Note = "没有可归属的供应商，账单保持 UNKNOWN"
		return out, nil
	}
	// Only an explicit from/to narrows billing. Deriving a window from event
	// timestamps would silently drop legitimate bills for the same provider.
	query := `SELECT provider_id,amount_micros,currency,source,observed_at,observed_ms,note FROM usage_actual_billing WHERE 1=1`
	args := []any{}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(providers)), ",")
	for _, provider := range sortedKeys(providers) {
		args = append(args, provider)
	}
	query += ` AND provider_id IN (` + placeholders + `)`
	if filter.FromMs != nil {
		query += ` AND observed_ms>=?`
		args = append(args, *filter.FromMs)
	}
	if filter.ToMs != nil {
		query += ` AND observed_ms<=?`
		args = append(args, *filter.ToMs)
	}
	query += ` ORDER BY observed_at DESC,id DESC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	totals := map[string]float64{}
	for rows.Next() {
		var providerID, currency, source, observedAt, note string
		var micros, observedMs int64
		if err := rows.Scan(&providerID, &micros, &currency, &source, &observedAt, &observedMs, &note); err != nil {
			return out, err
		}
		out.Observations = append(out.Observations, ActualObservation{ProviderID: providerID, Amount: float64(micros) / 1e6, Currency: currency, Source: source, ObservedAt: observedAt, Note: note})
		totals[currency] += float64(micros) / 1e6
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.Currencies = keys(totals)
	sort.Strings(out.Currencies)
	if filter.FromMs != nil {
		out.Scope["from"] = time.UnixMilli(*filter.FromMs).UTC().Format(time.RFC3339Nano)
	}
	if filter.ToMs != nil {
		out.Scope["to"] = time.UnixMilli(*filter.ToMs).UTC().Format(time.RFC3339Nano)
	}
	out.Scope["providers"] = sortedKeys(providers)
	ledger := ActualBillingTotal{Currencies: out.Currencies, Observations: out.Observations}
	if len(totals) == 1 {
		for currency, amount := range totals {
			ledger.Currency = &currency
			ledger.Amount = &amount
		}
	}
	out.ProviderLedger = &ledger
	attributable := filter.ProjectID == "" && filter.SessionID == "" && filter.AgentID == ""
	switch {
	case len(totals) > 1:
		out.Note = "多币种账单，供应商账本不可直接相加；账单只按 provider 与时间范围归属"
	case len(totals) == 0:
		out.Note = "该 provider 与时间范围内没有账单观察，实际费用 UNKNOWN"
	case attributable:
		out.Kind = "PROVIDER_LEDGER"
		out.Amount = ledger.Amount
		out.Currency = ledger.Currency
		out.Note = "这是该供应商在时间范围内的账单总额（供应商级账本），不是按 Token 估算"
	default:
		out.Note = "筛选包含项目/会话/Agent；账单缺少这些维度，因此不给该范围的实际费用，providerLedger 是供应商级总额"
	}
	return out, nil
}

func nonEmpty(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return []string{value}
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	out := value.String
	return &out
}

func keys[V any](values map[string]V) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	return out
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (s *Service) nowText() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (s *Service) subscriptionLedger(providerID string) (SubscriptionLedger, error) {
	out := SubscriptionLedger{Kind: "UNKNOWN", Note: "官方订阅剩余额度与重置窗口未知；不换算为 API 费用"}
	query := `SELECT remaining,reset_at,source,observed_at FROM usage_quota_snapshots WHERE ledger='SUBSCRIPTION'`
	args := []any{}
	if providerID != "" {
		query += ` AND provider_id=?`
		args = append(args, providerID)
	}
	query += ` ORDER BY observed_at DESC,id DESC LIMIT 1`
	var remaining, resetAt, source, observedAt sql.NullString
	err := s.db.QueryRow(query, args...).Scan(&remaining, &resetAt, &source, &observedAt)
	if err == sql.ErrNoRows {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Kind = "SUBSCRIPTION"
	if remaining.Valid {
		out.Remaining = &remaining.String
	}
	if resetAt.Valid {
		out.ResetAt = &resetAt.String
	}
	if source.Valid {
		out.Source = &source.String
	}
	if observedAt.Valid {
		out.ObservedAt = &observedAt.String
	}
	out.Note = "订阅额度独立账本，缺失值保持 UNKNOWN"
	return out, nil
}

func (s *Service) Quotas() ([]QuotaSnapshot, error) {
	rows, err := s.db.Query(`SELECT id,ledger,provider_id,scope,remaining,limit_value,unit,reset_at,currency,source,observed_at,note
		FROM usage_quota_snapshots ORDER BY ledger,provider_id,scope`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []QuotaSnapshot{}
	for rows.Next() {
		var item QuotaSnapshot
		var remaining, limitValue, unit, resetAt, currency sql.NullString
		if err := rows.Scan(&item.ID, &item.Ledger, &item.ProviderID, &item.Scope, &remaining, &limitValue, &unit, &resetAt, &currency, &item.Source, &item.ObservedAt, &item.Note); err != nil {
			return nil, err
		}
		item.Remaining = nullableString(remaining)
		item.Limit = nullableString(limitValue)
		item.Unit = nullableString(unit)
		item.ResetAt = nullableString(resetAt)
		item.Currency = nullableString(currency)
		out = append(out, item)
	}
	return out, rows.Err()
}
