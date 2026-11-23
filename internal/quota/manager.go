package quota

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	maxSources          = 20
	maxResponseBytes    = 64 * 1024
	maxErrorBodyBytes   = 2048
	defaultTimeoutMS    = 15000
	defaultFreshnessMS  = 15 * 60 * 1000
	defaultThrottleMS   = 1000
	externalIntervalMS  = 60 * 1000
	failureBackoffMaxMS = 5 * 60 * 1000
)

// Kinds are the quota shapes this package understands.
var Kinds = []string{"subscription_window", "coding_plan", "api_balance", "gateway_limit"}

// Statuses are the answers a card can carry.
//
//	ok             — the last attempt succeeded and the reading is fresh
//	stale          — a reading is shown that is older than the freshness window
//	                 or from before a failed attempt; the numbers are real
//	error          — the last attempt failed and there is no earlier reading
//	not_read       — a credential is available but no read has happened yet
//	not_configured — no credential resolved, so no request was attempted
//	unsupported    — no documented read-only interface exists for this source
var Statuses = []string{"ok", "stale", "error", "not_read", "not_configured", "unsupported"}

// The documented read-only endpoints. A stored provider entry can name a vendor
// but never a different host for its adapter.
const (
	DeepSeekEndpoint       = "https://api.deepseek.com/user/balance"
	MoonshotCNEndpoint     = "https://api.moonshot.cn/v1/users/me/balance"
	MoonshotGlobalEndpoint = "https://api.moonshot.ai/v1/users/me/balance"
)

// Provider is one configured entry as the host knows it. Only an id, a display
// name, a base URL and whether a key is stored are needed; the key itself is
// resolved separately and never becomes a field here.
type Provider struct {
	ID        string
	Name      string
	BaseURL   string
	Type      string
	HasAPIKey bool
}

// CredentialFunc resolves a provider's key at request time. The host reads it
// from the official client through KAP, so this package never owns a key file.
type CredentialFunc func(ctx context.Context, providerID string) (string, error)

// Options configures one manager. There is no timer and no background work: a
// reading changes only inside Refresh.
type Options struct {
	Credentials CredentialFunc
	HTTPClient  *http.Client
	Now         func() int64
	FreshnessMS int64
	ThrottleMS  int64
	TimeoutMS   int64
	// KimiCode is the local subscription reader; Magpie the local gateway reader.
	// Either may be nil, in which case that part of the state reports why.
	KimiCode *CodeUsage
	Magpie   *Magpie
}

// Reading is the last attempt and the last success for one identity.
type Reading struct {
	LastAttemptAt *int64   `json:"lastAttemptAt"`
	LastAttemptOK bool     `json:"lastAttemptOk"`
	ErrorReason   *string  `json:"errorReason"`
	LastSuccessAt *int64   `json:"lastSuccessAt"`
	Available     *bool    `json:"available"`
	Parts         []Part   `json:"parts"`
	Records       []Record `json:"records"`
}

// RecordView is one flat quota row the panel renders. ObservedAt is when the
// figures were actually read; it is what tells a reused value apart from a fresh
// one.
type RecordView struct {
	SourceID     string   `json:"sourceId"`
	ProviderID   string   `json:"providerId"`
	AccountTag   string   `json:"accountTag"`
	Kind         string   `json:"kind"`
	WindowLabel  *string  `json:"windowLabel"`
	Unit         *string  `json:"unit"`
	Limit        *float64 `json:"limit"`
	Used         *float64 `json:"used"`
	Remaining    *float64 `json:"remaining"`
	PeriodStart  *string  `json:"periodStart"`
	ResetAt      *string  `json:"resetAt"`
	ObservedAt   *string  `json:"observedAt"`
	Stale        bool     `json:"stale"`
	Status       string   `json:"status"`
	ErrorReason  *string  `json:"errorReason"`
}

// SourceCard is one configured provider's presentation.
type SourceCard struct {
	SourceID         string       `json:"sourceId"`
	ProviderID       string       `json:"providerId"`
	ProviderName     string       `json:"providerName"`
	Adapter          string       `json:"adapter"`
	TypeLabel        string       `json:"typeLabel"`
	Kind             string       `json:"kind"`
	Supported        bool         `json:"supported"`
	Status           string       `json:"status"`
	Stale            bool         `json:"stale"`
	StaleReason      *string      `json:"staleReason"`
	AccountTag       string       `json:"accountTag"`
	ObservedAt       *string      `json:"observedAt"`
	LastSuccessfulAt *string      `json:"lastSuccessfulAt"`
	LastAttemptAt    *string      `json:"lastAttemptAt"`
	ErrorReason      *string      `json:"errorReason"`
	Available        *bool        `json:"available"`
	Parts            []Part       `json:"parts"`
	Plan             *string      `json:"plan"`
	OfficialPage     *string      `json:"officialPage"`
	ConsoleURL       *string      `json:"consoleUrl"`
	Hint             *string      `json:"hint"`
	Notes            []string     `json:"notes"`
	Records          []RecordView `json:"records"`
}

// RefreshResult reports one provider refresh. HTTPStatus is the upstream code
// when there was one; it is never the credential.
type RefreshResult struct {
	SourceID    string  `json:"sourceId"`
	Status      string  `json:"status"`
	ErrorReason *string `json:"errorReason"`
	HTTPStatus  *int    `json:"httpStatus,omitempty"`
	Skipped     *string `json:"skipped,omitempty"`
}

// CacheState reports whether readings are being kept between refreshes.
type CacheState struct {
	LastReadAt  *int64  `json:"lastReadAt"`
	LastWriteAt *int64  `json:"lastWriteAt"`
	Error       *string `json:"error"`
}

// State is the whole payload the panel receives. It is pure: no request, no
// write, no timer.
type State struct {
	SchemaVersion int          `json:"schemaVersion"`
	Cards         []SourceCard `json:"cards"`
	Records       []RecordView `json:"records"`
	RefreshedAt   *string      `json:"refreshedAt"`
	FreshnessMS   int64        `json:"freshnessMs"`
	Combining     string       `json:"combining"`
	KimiCode      any          `json:"kimiCode,omitempty"`
	Magpie        any          `json:"magpie,omitempty"`
	NextRefreshMS int64        `json:"nextRefreshMs"`
	// ReadError explains why the provider list could not be read. The cards below
	// are then the previous ones, and the panel must not present them as current.
	ReadError string `json:"readError,omitempty"`
}

// Manager owns the last reading per identity and nothing else. The identity is
// (provider id, account tag): a failure may only fall back to the reading of the
// same account, because showing another account's balance as this one's would be
// a wrong number rather than a stale one.
type Manager struct {
	mu          sync.Mutex
	refreshMu   sync.Mutex
	credentials CredentialFunc
	client      *http.Client
	now         func() int64
	freshnessMS int64
	throttleMS  int64
	timeoutMS   int64
	kimiCode    *CodeUsage
	magpie      *Magpie

	readings   map[string]*Reading
	identities map[string]string
	plans      map[string]string
	// credentialState records what the last attempt found: "present", "missing",
	// or empty before any attempt. It is what separates "no key configured" from
	// "a read has not happened yet"; the host's own configured flag is only used
	// until the first attempt answers.
	credentialState map[string]string
	refreshedAt     *int64
	backoff         int
}

// New builds a manager. A missing HTTP client leaves every remote adapter in the
// error state rather than silently skipping them.
func New(options Options) *Manager {
	manager := &Manager{
		credentials: options.Credentials,
		client:      options.HTTPClient,
		now:         options.Now,
		freshnessMS: options.FreshnessMS,
		throttleMS:  options.ThrottleMS,
		timeoutMS:   options.TimeoutMS,
		kimiCode:    options.KimiCode,
		magpie:      options.Magpie,
		readings:       map[string]*Reading{},
		identities:     map[string]string{},
		plans:          map[string]string{},
		credentialState: map[string]string{},
	}
	if manager.now == nil {
		manager.now = func() int64 { return time.Now().UnixMilli() }
	}
	if manager.freshnessMS <= 0 {
		manager.freshnessMS = defaultFreshnessMS
	}
	if manager.throttleMS == 0 {
		manager.throttleMS = defaultThrottleMS
	}
	if manager.timeoutMS <= 0 {
		manager.timeoutMS = defaultTimeoutMS
	}
	return manager
}

// identityKey is the pair a reading belongs to.
func identityKey(providerID, accountTag string) string {
	return providerID + "|" + accountTag
}

type httpTarget struct {
	url     string
	headers map[string]string
}

func targetOf(adapter Adapter, secret string) *httpTarget {
	headers := map[string]string{
		"Accept":        "application/json",
		"Authorization": "Bearer " + secret,
	}
	return &httpTarget{url: adapter.Endpoint, headers: headers}
}

// fetch reads one endpoint. The reply body is bounded, and an error body is
// drained without being kept: a refusal can echo the request, and nothing from it
// may reach a log or the panel.
func (m *Manager) fetch(ctx context.Context, target *httpTarget) (any, string, *int) {
	if m.client == nil {
		return nil, "network_error", nil
	}
	client := m.client
	if client.Timeout == 0 {
		copied := *client
		copied.Timeout = time.Duration(m.timeoutMS) * time.Millisecond
		client = &copied
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.url, nil)
	if err != nil {
		return nil, "network_error", nil
	}
	for name, value := range target.headers {
		request.Header.Set(name, value)
	}
	reply, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "timeout", nil
		}
		return nil, "network_error", nil
	}
	defer func() { _ = reply.Body.Close() }()
	if reply.StatusCode < 200 || reply.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(reply.Body, maxErrorBodyBytes))
		status := reply.StatusCode
		switch {
		case status == 401 || status == 403:
			return nil, "auth_rejected", &status
		case status == 429:
			return nil, "rate_limited", &status
		default:
			return nil, "upstream_error", &status
		}
	}
	raw, err := io.ReadAll(io.LimitReader(reply.Body, maxResponseBytes+1))
	if err != nil {
		return nil, "network_error", nil
	}
	if len(raw) > maxResponseBytes {
		return nil, "response_too_large", nil
	}
	var document any
	if json.Unmarshal(raw, &document) != nil {
		return nil, "invalid_response", nil
	}
	return document, "", nil
}

// emptyReading is the shape a failure stores: the attempt is recorded, the
// figures are not invented.
func emptyReading(attemptedAt int64, code string) *Reading {
	return &Reading{LastAttemptAt: &attemptedAt, ErrorReason: optString(code), Parts: []Part{}, Records: []Record{}}
}

func carryForward(reading, previous *Reading) *Reading {
	reading.Parts = []Part{}
	reading.Records = []Record{}
	if previous == nil {
		return reading
	}
	reading.LastSuccessAt = previous.LastSuccessAt
	reading.Available = previous.Available
	reading.Parts = previous.Parts
	reading.Records = previous.Records
	if reading.Parts == nil {
		reading.Parts = []Part{}
	}
	if reading.Records == nil {
		reading.Records = []Record{}
	}
	return reading
}

func (m *Manager) refreshOne(ctx context.Context, provider Provider) RefreshResult {
	class := ClassifyProvider(provider.Type, provider.BaseURL)
	sourceID := "provider:" + provider.ID

	switch class.Kind {
	case ProviderManaged:
		return RefreshResult{SourceID: sourceID, Status: "unsupported", ErrorReason: optString("managed_account"), Skipped: optString("managed")}
	case ProviderUnsupported:
		return RefreshResult{SourceID: sourceID, Status: "unsupported", ErrorReason: optString("unsupported_source"), Skipped: optString("unsupported")}
	}
	adapter, _ := AdapterByID(class.Adapter)

	attemptedAt := m.now()
	previousTag := m.previousIdentity(provider.ID)
	previous := m.reading(provider.ID, previousTag)
	if !provider.HasAPIKey {
		m.putCredentialState(provider.ID, "missing")
		reading := carryForward(emptyReading(attemptedAt, "credential_missing"), previous)
		m.putReading(provider.ID, previousTag, reading)
		return RefreshResult{SourceID: sourceID, Status: "not_configured", ErrorReason: optString("credential_missing")}
	}
	secret := ""
	if m.credentials != nil {
		// Resolving a credential may hit the keychain or the official client.
		// It runs with mu released so State() stays responsive.
		value, err := m.credentials(ctx, provider.ID)
		if err != nil {
			// The resolver failing is not the same as the user having no key; the
			// reading keeps its previous identity so a transient read failure does
			// not look like a different account.
			reading := carryForward(emptyReading(attemptedAt, "credential_unavailable"), previous)
			m.putReading(provider.ID, previousTag, reading)
			return RefreshResult{SourceID: sourceID, Status: "stale", ErrorReason: optString("credential_unavailable")}
		}
		secret = strings.TrimSpace(value)
	}
	if secret == "" || len(secret) > 4096 || isControl(secret) {
		m.putCredentialState(provider.ID, "missing")
		reading := carryForward(emptyReading(attemptedAt, "credential_missing"), previous)
		m.putReading(provider.ID, previousTag, reading)
		return RefreshResult{SourceID: sourceID, Status: "not_configured", ErrorReason: optString("credential_missing")}
	}
	tag := AccountTag(secret)
	// A different account is a different source of truth: the new reading starts
	// empty, and the old account's figures are not shown as this one's.
	if previousTag != tag {
		m.setIdentity(provider.ID, tag)
		previous = m.reading(provider.ID, tag)
	}
	if m.throttleMS > 0 && previous != nil && previous.LastAttemptAt != nil &&
		attemptedAt-*previous.LastAttemptAt < m.throttleMS {
		return RefreshResult{SourceID: sourceID, Status: "throttled", Skipped: optString("throttled")}
	}
	m.putCredentialState(provider.ID, "present")
	target := targetOf(adapter, secret)
	document, code, status := m.fetch(ctx, target)
	if code != "" {
		return m.failSource(provider, tag, attemptedAt, previous, code, status)
	}
	parsed, code := adapter.Parse(document)
	if code != "" {
		return m.failSource(provider, tag, attemptedAt, previous, code, nil)
	}
	records := make([]Record, 0, len(parsed.Records))
	for _, record := range parsed.Records {
		kind := record.Kind
		if kind == "" {
			kind = adapter.Kind
		}
		unit := record.Unit
		if unit == nil {
			unit = adapter.Unit
		}
		records = append(records, Record{Kind: kind, Unit: unit, WindowLabel: record.WindowLabel,
			Limit: record.Limit, Used: record.Used, Remaining: record.Remaining,
			PeriodStart: record.PeriodStart, ResetAt: record.ResetAt})
	}
	successAt := attemptedAt
	plan := PlanNameOf(document)
	m.mu.Lock()
	m.readings[identityKey(provider.ID, tag)] = &Reading{LastAttemptAt: &attemptedAt, LastAttemptOK: true,
		LastSuccessAt: &successAt, Available: parsed.Available, Parts: nonNilParts(parsed.Parts), Records: records}
	if plan != "" {
		m.plans[provider.ID] = plan
	}
	m.backoff = 0
	m.mu.Unlock()
	return RefreshResult{SourceID: sourceID, Status: "ok"}
}

func (m *Manager) previousIdentity(providerID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.identities[providerID]
}

func (m *Manager) reading(providerID, tag string) *Reading {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.readings[identityKey(providerID, tag)]
}

func (m *Manager) putReading(providerID, tag string, reading *Reading) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readings[identityKey(providerID, tag)] = reading
}

func (m *Manager) setIdentity(providerID, tag string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.identities[providerID] = tag
}

func (m *Manager) putCredentialState(providerID, state string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.credentialState[providerID] = state
}

func nonNilParts(parts []Part) []Part {
	if parts == nil {
		return []Part{}
	}
	return parts
}

func (m *Manager) failSource(provider Provider, tag string, attemptedAt int64, previous *Reading, code string, status *int) RefreshResult {
	reading := carryForward(emptyReading(attemptedAt, code), previous)
	m.putReading(provider.ID, tag, reading)
	// A failed read keeps what was last read. It is never written as 0 and never
	// as "no limit".
	result := RefreshResult{SourceID: "provider:" + provider.ID, ErrorReason: optString(code), HTTPStatus: status}
	if len(reading.Records) > 0 {
		result.Status = "stale"
	} else {
		result.Status = "error"
	}
	return result
}

// Refresh is the only method that makes a request. A second call while a refresh
// is running waits for that one and then runs its own selection, rather than
// being answered with another call's results.
//
// mu is never held across a credential resolution or an upstream HTTP request:
// those can take seconds, and State() must stay available for the settings page
// and the automation tick while a refresh is in flight.
func (m *Manager) Refresh(ctx context.Context, providers []Provider) State {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	if len(providers) > maxSources {
		providers = providers[:maxSources]
	}
	for _, provider := range providers {
		_ = m.refreshOne(ctx, provider)
	}
	stamp := m.now()
	m.mu.Lock()
	m.refreshedAt = &stamp
	m.mu.Unlock()

	var kimi any
	if m.kimiCode != nil {
		kimi = m.kimiCode.Refresh(ctx)
	}
	var magpie any
	if m.magpie != nil {
		magpie = m.magpie.Refresh(ctx)
	}
	state := m.State(providers)
	if kimi != nil {
		state.KimiCode = kimi
	}
	if magpie != nil {
		state.Magpie = magpie
	}
	return state
}

// State builds the payload from what is stored. It resolves no credential and
// makes no request.
func (m *Manager) State(providers []Provider) State {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(providers) > maxSources {
		providers = providers[:maxSources]
	}
	state := State{SchemaVersion: 1, Cards: []SourceCard{}, Records: []RecordView{},
		FreshnessMS: m.freshnessMS, RefreshedAt: optISO(m.refreshedAt),
		Combining: "never-summed", NextRefreshMS: externalIntervalMS}
	for _, provider := range providers {
		class := ClassifyProvider(provider.Type, provider.BaseURL)
		if class.Kind == ProviderManaged {
			continue
		}
		sourceID := "provider:" + provider.ID
		tag := m.identities[provider.ID]
		reading := m.readings[identityKey(provider.ID, tag)]
		adapter, known := AdapterByID(class.Adapter)
		card := SourceCard{SourceID: sourceID, ProviderID: provider.ID,
			ProviderName: provider.Name, Adapter: class.Adapter, TypeLabel: class.TypeLabel,
			Kind: adapter.Kind, Supported: class.Kind == ProviderSupported,
			AccountTag: tag, Parts: []Part{}, Records: []RecordView{}, Notes: []string{}}
		if class.Kind == ProviderUnsupported {
			card.Status = "unsupported"
			card.Notes = append(card.Notes, class.Reason)
		} else if known {
			card.OfficialPage = optString(adapter.OfficialPage)
			card.ConsoleURL = optString(adapter.ConsoleURL)
			card.Hint = optString(adapter.Hint)
			hasCredential := provider.HasAPIKey
			switch m.credentialState[provider.ID] {
			case "present":
				hasCredential = true
			case "missing":
				hasCredential = false
			}
			switch status, stale, reason := m.statusOf(card.Supported, reading, hasCredential); status {
			case "ok", "stale":
				card.Status = status
				card.Stale = stale
				card.StaleReason = reason
			default:
				card.Status = status
			}
		}
		if reading != nil {
			card.LastAttemptAt = optISO(reading.LastAttemptAt)
			card.LastSuccessfulAt = optISO(reading.LastSuccessAt)
			card.ErrorReason = reading.ErrorReason
			card.Available = reading.Available
			card.Parts = nonNilParts(reading.Parts)
			// The observed time is the last success, not the last attempt: a reused
			// figure must not look like it was read just now.
			card.ObservedAt = optISO(reading.LastSuccessAt)
			for _, record := range reading.Records {
				view := RecordView{SourceID: sourceID, ProviderID: provider.ID, AccountTag: tag,
					Kind: record.Kind, WindowLabel: record.WindowLabel, Unit: record.Unit,
					Limit: record.Limit, Used: record.Used, Remaining: record.Remaining,
					PeriodStart: optISO(record.PeriodStart), ResetAt: optISO(record.ResetAt),
					ObservedAt: card.ObservedAt, Stale: card.Stale, Status: card.Status,
					ErrorReason: card.ErrorReason}
				card.Records = append(card.Records, view)
				state.Records = append(state.Records, view)
			}
		}
		if plan, ok := m.plans[provider.ID]; ok {
			card.Plan = optString(plan)
		}
		if !card.Supported && class.Kind != ProviderUnsupported && !known {
			card.Status = "unsupported"
		}
		state.Cards = append(state.Cards, card)
	}
	if m.kimiCode != nil {
		state.KimiCode = m.kimiCode.State()
	}
	if m.magpie != nil {
		state.Magpie = m.magpie.State()
	}
	return state
}

// statusOf mirrors the card states. It is a method so freshness keeps one
// definition instead of a renderer re-deriving it.
func (m *Manager) statusOf(supported bool, reading *Reading, hasCredential bool) (string, bool, *string) {
	if !supported {
		return "unsupported", false, nil
	}
	if reading == nil {
		if hasCredential {
			return "not_read", false, nil
		}
		return "not_configured", false, nil
	}
	if reading.LastAttemptOK {
		age := int64(^uint64(0) >> 1)
		if reading.LastSuccessAt != nil {
			age = m.now() - *reading.LastSuccessAt
		}
		if age > m.freshnessMS {
			return "stale", true, optString("age")
		}
		return "ok", false, nil
	}
	if reading.ErrorReason != nil && *reading.ErrorReason == "credential_missing" {
		if hasCredential {
			return "not_read", false, nil
		}
		return "not_configured", false, nil
	}
	if len(reading.Records) > 0 {
		return "stale", true, optString("refresh_failed")
	}
	return "error", false, nil
}