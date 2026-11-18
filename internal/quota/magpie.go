package quota

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MagpieDefaultBaseURL is the local gateway's documented address.
const MagpieDefaultBaseURL = "http://127.0.0.1:3425"

// MagpieQuotasPath is the documented read-only path.
const MagpieQuotasPath = "/v1/magpie/quotas"

const (
	maxMagpieEntries = 200
	maxMagpieWindows = 16
)

// MagpieWindow is one allowance window as Magpie reports it. Exactly one of the
// absolute or the percentage reading is meaningful for any given entry; which one
// is stated in `Basis` so a renderer cannot treat a percentage as money.
type MagpieWindow struct {
	Name      string   `json:"name"`
	Basis     string   `json:"basis"`
	Used      *float64 `json:"used"`
	Remaining *float64 `json:"remaining"`
	Amount    *float64 `json:"amount"`
	Limit     *float64 `json:"limit"`
	Unit      *string  `json:"unit"`
	ResetAt   *string  `json:"resetsAt"`
	Display   *string  `json:"display"`
	Note      *string  `json:"note"`
}

// MagpieWindowBasis values.
const (
	MagpieBasisAmount  = "amount"
	MagpieBasisPercent = "percent"
	MagpieBasisText    = "text"
)

// MagpieEntry is one account or provider balance.
type MagpieEntry struct {
	Provider        string         `json:"provider"`
	Name            string         `json:"name"`
	Kind            string         `json:"kind"`
	Plan            *string        `json:"plan"`
	User            *string        `json:"user"`
	Alias           *string        `json:"alias"`
	Windows         []MagpieWindow `json:"windows"`
	Balance         *string        `json:"balance"`
	BalanceCurrency *string        `json:"balanceCurrency"`
	BalanceAmount   *float64       `json:"balanceAmount"`
	AsOf            *string        `json:"asOf"`
	ReadAt          *string        `json:"readAt"`
	LastServedAt    *string        `json:"lastServedAt"`
}

// MagpieSnapshot is one successful read.
type MagpieSnapshot struct {
	BaseURL   string        `json:"baseUrl"`
	Entries   []MagpieEntry `json:"entries"`
	FetchedAt *string       `json:"fetchedAt"`
	Notes     []string      `json:"notes"`
}

// MagpieState is the panel payload. Pure: no request.
type MagpieState struct {
	SchemaVersion int             `json:"schemaVersion"`
	BaseURL       string          `json:"baseUrl"`
	Status        string          `json:"status"`
	Stale         bool            `json:"stale"`
	ErrorReason   *string         `json:"errorReason"`
	KeyConfigured bool            `json:"keyConfigured"`
	Snapshot      *MagpieSnapshot `json:"snapshot"`
	LastAttemptAt *string         `json:"lastAttemptAt"`
	Notes         []string        `json:"notes"`
}

// MagpieOptions wires one reader. `BaseURL` is the caller's own gateway address;
// it is validated here, not trusted.
type MagpieOptions struct {
	BaseURL     string
	APIKey      func() string
	HTTPClient  *http.Client
	Now         func() int64
	TimeoutMS   int64
	FreshnessMS int64
}

// Magpie reads the local gateway's own quota list.
type Magpie struct {
	mu        sync.Mutex
	refreshMu sync.Mutex
	baseURL   string
	apiKey    func() string
	client    *http.Client
	now       func() int64
	timeoutMS int64
	freshnessMS int64

	snapshot    *MagpieSnapshot
	snapshotMS  int64
	lastAt      *int64
	lastError   string
}

// NewMagpie builds the reader. An unusable base URL is an error the caller can
// report rather than a silently dead module.
func NewMagpie(options MagpieOptions) (*Magpie, error) {
	base := strings.TrimSpace(options.BaseURL)
	if base == "" {
		base = MagpieDefaultBaseURL
	}
	check := CheckEndpoint(base)
	if !check.OK {
		return nil, sourceError(check.Code, "本机 Magpie 地址不可用。")
	}
	reader := &Magpie{baseURL: check.Href, apiKey: options.APIKey, client: options.HTTPClient,
		now: options.Now, timeoutMS: options.TimeoutMS, freshnessMS: options.FreshnessMS}
	if reader.now == nil {
		reader.now = func() int64 { return time.Now().UnixMilli() }
	}
	if reader.timeoutMS <= 0 {
		reader.timeoutMS = 5000
	}
	if reader.freshnessMS <= 0 {
		reader.freshnessMS = defaultFreshnessMS
	}
	return reader, nil
}

// Endpoint is the URL the reader requests.
func (m *Magpie) Endpoint() string { return m.baseURL + MagpieQuotasPath }

// Refresh reads the gateway. A failure keeps the last snapshot and reports why.
func (m *Magpie) Refresh(ctx context.Context) MagpieState {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()

	m.mu.Lock()
	base, key := m.baseURL, ""
	if m.apiKey != nil {
		key, _ = readSecret(m.apiKey())
	}
	attemptedAt := m.now()
	m.lastAt = &attemptedAt
	m.mu.Unlock()

	if m.client == nil {
		return m.fail("network_error")
	}
	client := m.client
	if client.Timeout == 0 {
		copied := *client
		copied.Timeout = time.Duration(m.timeoutMS) * time.Millisecond
		client = &copied
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+MagpieQuotasPath, nil)
	if err != nil {
		return m.fail("network_error")
	}
	request.Header.Set("Accept", "application/json")
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("x-api-key", key)
	}
	reply, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return m.fail("timeout")
		}
		// The most likely failure here is that Magpie is not running.
		return m.fail("magpie_unreachable")
	}
	defer func() { _ = reply.Body.Close() }()
	if reply.StatusCode < 200 || reply.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(reply.Body, maxErrorBodyBytes))
		switch reply.StatusCode {
		case 401, 403:
			return m.fail("magpie_key_rejected")
		case 404:
			return m.fail("magpie_endpoint_unavailable")
		case 429:
			return m.fail("rate_limited")
		default:
			return m.fail("magpie_error")
		}
	}
	raw, err := io.ReadAll(io.LimitReader(reply.Body, maxResponseBytes+1))
	if err != nil {
		return m.fail("network_error")
	}
	if len(raw) > maxResponseBytes {
		return m.fail("response_too_large")
	}
	var document any
	if json.Unmarshal(raw, &document) != nil {
		return m.fail("invalid_response")
	}
	snapshot, code := ParseMagpieQuotas(document)
	if code != "" {
		return m.fail(code)
	}
	stamp := m.now()
	snapshot.BaseURL = base + MagpieQuotasPath
	snapshot.FetchedAt = optISO(&stamp)
	m.mu.Lock()
	m.snapshot = snapshot
	m.snapshotMS = stamp
	m.lastError = ""
	m.mu.Unlock()
	return m.State()
}

func (m *Magpie) fail(code string) MagpieState {
	m.mu.Lock()
	m.lastError = code
	m.mu.Unlock()
	return m.State()
}

// State builds the payload without making a request.
func (m *Magpie) State() MagpieState {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := MagpieState{SchemaVersion: 1, BaseURL: m.baseURL, Status: "not_read",
		KeyConfigured: m.apiKey != nil, Notes: []string{}, LastAttemptAt: optISO(m.lastAt)}
	if m.lastError != "" {
		state.ErrorReason = optString(m.lastError)
		state.Status = "error"
	}
	if m.snapshot != nil {
		state.Snapshot = m.snapshot
		state.Status = "ok"
		if m.now()-m.snapshotMS > m.freshnessMS {
			state.Status = "stale"
			state.Stale = true
		}
		if m.lastError != "" {
			state.Status = "stale"
			state.Stale = true
		}
	}
	return state
}

// ParseMagpieQuotas reads the documented reply. Both the bare array and the
// `{data:[…]}` envelope the HTTP form uses are accepted, because the CLI prints
// the array and the gateway wraps it.
func ParseMagpieQuotas(document any) (*MagpieSnapshot, string) {
	rows, ok := document.([]any)
	if !ok {
		object, isObject := document.(map[string]any)
		if !isObject {
			return nil, "invalid_response"
		}
		rows, ok = object["data"].([]any)
		if !ok {
			return nil, "invalid_response"
		}
	}
	snapshot := &MagpieSnapshot{Entries: []MagpieEntry{}, Notes: []string{}}
	for _, row := range rows {
		if len(snapshot.Entries) >= maxMagpieEntries {
			break
		}
		object, ok := row.(map[string]any)
		if !ok {
			continue
		}
		entry := parseMagpieEntry(object, snapshot)
		if entry == nil {
			continue
		}
		snapshot.Entries = append(snapshot.Entries, *entry)
	}
	if len(snapshot.Entries) == 0 && len(rows) > 0 {
		// An empty list is a real answer ("nothing configured"), which is
		// different from a reply this package could not read. Only the latter is
		// an error, and it never becomes "0".
		return nil, "invalid_response"
	}
	return snapshot, ""
}

func parseMagpieEntry(object map[string]any, snapshot *MagpieSnapshot) *MagpieEntry {
	provider := strings.TrimSpace(stringOf(object["provider"]))
	if provider == "" || !identifierPattern.MatchString(provider) {
		return nil
	}
	entry := &MagpieEntry{Provider: provider, Name: boundText(stringOf(object["name"]), 80),
		Kind: boundText(stringOf(object["kind"]), 32), Windows: []MagpieWindow{}}
	if entry.Kind != "subscription" && entry.Kind != "balance" {
		snapshot.Notes = append(snapshot.Notes, "unknown_kind:"+provider)
	}
	entry.Plan = optString(boundText(stringOf(object["plan"]), 64))
	entry.User = optString(boundText(stringOf(object["user"]), 120))
	entry.Alias = optString(boundText(stringOf(object["alias"]), 80))
	entry.AsOf = optISO(ToTimestamp(object["asOf"]))
	entry.ReadAt = optISO(ToTimestamp(object["readAt"]))
	entry.LastServedAt = optISO(ToTimestamp(object["lastServedAt"]))
	if windows, ok := object["windows"].([]any); ok {
		for _, raw := range windows {
			if len(entry.Windows) >= maxMagpieWindows {
				break
			}
			window, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if parsed, code := parseMagpieWindow(window); code == "" {
				entry.Windows = append(entry.Windows, parsed)
			} else {
				snapshot.Notes = append(snapshot.Notes, code+":"+provider)
			}
		}
	}
	if raw, ok := object["balance"].(string); ok {
		text := boundText(raw, 40)
		entry.Balance = optString(text)
		if currency, amount := parseMagpieBalance(text); amount != nil {
			entry.BalanceCurrency = optString(currency)
			entry.BalanceAmount = amount
		} else if text != "" {
			snapshot.Notes = append(snapshot.Notes, "balance_unparsed:"+provider)
		}
	}
	if len(entry.Windows) == 0 && entry.Balance == nil {
		return nil
	}
	return entry
}

func parseMagpieWindow(window map[string]any) (MagpieWindow, string) {
	result := MagpieWindow{Name: boundText(stringOf(window["name"]), 60)}
	if result.Name == "" {
		return MagpieWindow{}, "window_unreadable"
	}
	result.ResetAt = optISO(ToTimestamp(window["resetsAt"]))
	result.Display = optString(boundText(stringOf(window["display"]), 80))
	for _, name := range []string{"holdsWhy", "note"} {
		if text := boundText(stringOf(window[name]), 40); text != "" {
			result.Note = optString(text)
			break
		}
	}
	amount := ToAmount(window["amount"])
	limit := ToAmount(window["limit"])
	used := ToAmount(window["used"])
	remaining := ToAmount(window["remaining"])
	switch {
	case amount != nil || limit != nil:
		// Absolute figures win: a percentage that Magpie derived from them is a
		// second rounding of the same fact.
		result.Basis = MagpieBasisAmount
		result.Amount = amount
		result.Limit = limit
		if unit := boundText(stringOf(window["unit"]), 16); unit != "" {
			result.Unit = optString(unit)
		}
	case used != nil || remaining != nil:
		result.Basis = MagpieBasisPercent
		result.Used = used
		result.Remaining = remaining
	case result.Display != nil:
		// Nothing but a rendered string: the numbers are unknown. Keeping the
		// text and no figure is the only honest rendering.
		result.Basis = MagpieBasisText
	default:
		return MagpieWindow{}, "window_unreadable"
	}
	if result.ResetAt == nil {
		if runsOut := ToTimestamp(window["runsOutAt"]); runsOut != nil {
			result.ResetAt = optISO(runsOut)
		}
	}
	return result, ""
}

// parseMagpieBalance reads Magpie's formatted balance. The currency is the symbol
// the string carries; an unrecognised symbol leaves the amount unset rather than
// guessing which currency it is.
func parseMagpieBalance(text string) (string, *float64) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", nil
	}
	currency := ""
	rest := trimmed
	for symbol, code := range map[string]string{"$": "USD", "¥": "CNY", "€": "EUR", "£": "GBP"} {
		if strings.HasPrefix(trimmed, symbol) {
			currency = code
			rest = strings.TrimSpace(strings.TrimPrefix(trimmed, symbol))
			break
		}
	}
	if currency == "" {
		for code := range customUnits {
			if len(code) != 3 || code == "USD" || code == "CNY" || code == "EUR" || code == "GBP" {
				continue
			}
			if strings.HasSuffix(trimmed, code) {
				currency = code
				rest = strings.TrimSpace(strings.TrimSuffix(trimmed, code))
				break
			}
		}
	}
	if currency == "" {
		return "", nil
	}
	cleaned := strings.ReplaceAll(rest, ",", "")
	parsed, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return "", nil
	}
	amount := round6(parsed)
	if amount != 0 && (amount > maxAmount || amount < -maxAmount) {
		return "", nil
	}
	return currency, &amount
}

func boundText(value string, limit int) string {
	trimmed := strings.TrimSpace(value)
	if isControl(trimmed) {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return trimmed
}