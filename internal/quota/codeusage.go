package quota

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CodeUsageEndpoint is the Kimi Code subscription allowance the desktop client
// itself reads. It needs the local sign-in token, not an API key.
const CodeUsageEndpoint = "https://api.kimi.com/coding/v1/usages"

const (
	CodeSourceManual = "manual"
	CodeSourceLocal  = "local"
)

const (
	codeManualRef     = "kimi-code:manual"
	maxCodeWindows    = 8
	maxCodeKeyChars   = 4096
	localCredentialRel = ".kimi-code/credentials/kimi-code.json"
)

const (
	CodeAvailable   = "available"
	CodeMissing     = "missing"
	CodeUnadopted   = "unadopted"
	CodeUnavailable = "unavailable"
)

// CodeUsageWindow is one allowance window. `limit_month_code` is a subset of
// `limit_month_total`, so no window is ever added to another.
type CodeUsageWindow struct {
	Key              string  `json:"key"`
	Label            string  `json:"label"`
	UsedRatio        float64 `json:"usedRatio"`
	UsedPercent      float64 `json:"usedPercent"`
	RemainingPercent float64 `json:"remainingPercent"`
	ResetAt          *string `json:"resetAt"`
}

// CodeUsageWallet is the prepaid booster the same reply carries. A field the
// reply did not carry stays nil: a missing balance is not a zero balance.
type CodeUsageWallet struct {
	BalanceCents        *float64 `json:"balanceCents"`
	TotalCents          *float64 `json:"totalCents"`
	Currency            *string  `json:"currency"`
	MonthlyLimitCents   *float64 `json:"monthlyChargeLimitCents"`
	MonthlyUsedCents    *float64 `json:"monthlyUsedCents"`
	MonthlyLimitEnabled *bool    `json:"monthlyChargeLimitEnabled"`
}

// CodeUsagePayload is one parsed reply.
type CodeUsagePayload struct {
	Windows []CodeUsageWindow `json:"windows"`
	Wallet  *CodeUsageWallet  `json:"wallet"`
	// Notes name parts of a reply that could not be read. They are not errors.
	Notes []string `json:"notes"`
}

// CodeSourceView is one credential source and its status.
type CodeSourceView struct {
	ID            string  `json:"id"`
	Label         string  `json:"label"`
	Availability  string  `json:"availability"`
	Adopted       bool    `json:"adopted"`
	Selected      bool    `json:"selected"`
	AccountMasked *string `json:"accountMasked"`
	Detail        *string `json:"detail"`
}

// CodeUsageState is the panel payload. Pure: no request, no resolver call.
type CodeUsageState struct {
	SchemaVersion int               `json:"schemaVersion"`
	Endpoint      string            `json:"endpoint"`
	Status        string            `json:"status"`
	Stale         bool              `json:"stale"`
	ErrorReason   *string           `json:"errorReason"`
	SelectedSource *string          `json:"selectedSource"`
	ReadingSource  *string          `json:"readingSource"`
	AccountMasked  *string          `json:"accountMasked"`
	Sources        []CodeSourceView `json:"sources"`
	Windows        []CodeUsageWindow `json:"windows"`
	Wallet         *CodeUsageWallet  `json:"wallet"`
	Notes          []string          `json:"notes"`
	ObservedAt     *string           `json:"observedAt"`
	LastAttemptAt  *string           `json:"lastAttemptAt"`
	FreshnessMS    int64             `json:"freshnessMs"`
}

// CodeUsageOptions wires one reader. A resolver returns only a key: the masked
// account shown to a person is a hash of the key that actually answered, so the
// card can never claim an account the reading did not come from.
type CodeUsageOptions struct {
	// KimiHome is the Kimi Code home directory itself (for example ~/.kimi-code
	// or an isolated KIMI_CODE_HOME); the local sign-in is read directly from
	// <KimiHome>/credentials/kimi-code.json.
	KimiHome string
	// HomeDir is the user's home; kept for callers/tests that pass a parent home
	// directory containing .kimi-code/credentials/kimi-code.json.
	HomeDir string
	// ManualKey, when set, is offered as a second source. The host does not set
	// it today; the reader supports it so the panel never has to invent a second
	// code path.
	ManualKey  func() string
	HTTPClient *http.Client
	Now        func() int64
	TimeoutMS  int64
	FreshnessMS int64
	ThrottleMS int64
}

type codeIdentity struct {
	source  string
	account string
}

type codeReading struct {
	codeIdentity
	windows []CodeUsageWindow
	wallet  *CodeUsageWallet
	notes   []string
	at      int64
}

// CodeUsage is the Kimi Code subscription reader.
type CodeUsage struct {
	mu        sync.Mutex
	refreshMu sync.Mutex
	kimiHome  string
	homeDir   string
	manualKey func() string
	client    *http.Client
	now       func() int64
	timeoutMS int64
	freshnessMS int64
	throttleMS  int64

	availability map[string]string
	accounts     map[string]string
	reading      *codeReading
	lastAttempt  *int64
	lastError    string
	lastStatus   string
}

// NewCodeUsage builds the reader.
func NewCodeUsage(options CodeUsageOptions) *CodeUsage {
	reader := &CodeUsage{
		kimiHome:     options.KimiHome,
		homeDir:      options.HomeDir,
		manualKey:    options.ManualKey,
		client:       options.HTTPClient,
		now:          options.Now,
		timeoutMS:    options.TimeoutMS,
		freshnessMS:  options.FreshnessMS,
		throttleMS:   options.ThrottleMS,
		availability: map[string]string{},
		accounts:     map[string]string{},
	}
	if reader.now == nil {
		reader.now = func() int64 { return time.Now().UnixMilli() }
	}
	if reader.freshnessMS <= 0 {
		reader.freshnessMS = defaultFreshnessMS
	}
	if reader.timeoutMS <= 0 {
		reader.timeoutMS = defaultTimeoutMS
	}
	return reader
}

func readSecret(key string) (string, bool) {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" || len(trimmed) > maxCodeKeyChars || isControl(trimmed) {
		return "", false
	}
	for _, character := range trimmed {
		if character <= 0x20 {
			return "", false
		}
	}
	return trimmed, true
}

// localCredential reads this machine's Kimi Code sign-in. The token is used for
// one request and is never returned, logged or stored by this package.
func (c *CodeUsage) localCredential() (string, bool) {
	var credPath string
	switch {
	case strings.TrimSpace(c.kimiHome) != "":
		credPath = filepath.Join(strings.TrimSpace(c.kimiHome), "credentials", "kimi-code.json")
	case strings.TrimSpace(c.homeDir) != "":
		credPath = filepath.Join(strings.TrimSpace(c.homeDir), filepath.FromSlash(localCredentialRel))
	default:
		return "", false
	}
	raw, err := os.ReadFile(credPath)
	if err != nil {
		return "", false
	}
	var document struct {
		AccessToken string `json:"access_token"`
		ExpiresAt   int64  `json:"expires_at"`
	}
	if json.Unmarshal(raw, &document) != nil {
		return "", false
	}
	return readSecret(document.AccessToken)
}

// manualCredential reads the key a person saved for this source.
func (c *CodeUsage) manualCredential() (string, bool) {
	if c.manualKey == nil {
		return "", false
	}
	return readSecret(c.manualKey())
}

// detectLocked re-reads which sources hold a credential. Detection never adopts a
// sign-in silently: the local sign-in is this machine's, and using it is the
// person's decision, which the host records by asking for it.
func (c *CodeUsage) detectLocked() {
	for _, source := range []string{CodeSourceManual, CodeSourceLocal} {
		c.availability[source] = CodeMissing
		c.accounts[source] = ""
	}
	if c.manualKey == nil {
		c.availability[CodeSourceManual] = CodeUnavailable
	} else if secret, ok := c.manualCredential(); ok {
		c.availability[CodeSourceManual] = CodeAvailable
		c.accounts[CodeSourceManual] = AccountTag(secret)
	}
	if secret, ok := c.localCredential(); ok {
		c.accounts[CodeSourceLocal] = AccountTag(secret)
		c.availability[CodeSourceLocal] = CodeAvailable
	} else {
		c.availability[CodeSourceLocal] = CodeMissing
	}
}

// resolveLocked returns the source a refresh should use and its credential.
func (c *CodeUsage) resolveLocked() (string, string) {
	if secret, ok := c.manualCredential(); ok {
		return CodeSourceManual, secret
	}
	if secret, ok := c.localCredential(); ok {
		return CodeSourceLocal, secret
	}
	return "", ""
}

// ParseCodeUsageReply reads the documented reply. A window whose ratio is outside
// the documented 0..1 range is reported rather than clamped into a number the
// vendor never sent.
func ParseCodeUsageReply(document any) (CodeUsagePayload, string) {
	root, ok := document.(map[string]any)
	if !ok {
		return CodeUsagePayload{}, "invalid_response"
	}
	usages, ok := root["usages"].(map[string]any)
	if !ok {
		if data, present := root["data"].(map[string]any); present {
			usages, ok = data["usages"].(map[string]any)
		}
	}
	if !ok {
		usages = root
	}
	payload := CodeUsagePayload{Windows: []CodeUsageWindow{}, Notes: []string{}}
	for _, entry := range []struct {
		key   string
		alias string
		label string
	}{
		{"limit_5h", "limit5h", "5 小时窗口"},
		{"limit_7d", "limit7d", "每周窗口"},
		{"limit_month_total", "monthTotal", "月度总额"},
		{"limit_month_code", "monthCode", "月度（Kimi Code 部分）"},
	} {
		raw, present := usages[entry.key]
		if !present || raw == nil {
			raw, present = usages[entry.alias]
		}
		if !present || raw == nil {
			continue
		}
		window, code := parseCodeWindow(entry.key, entry.label, raw)
		if code != "" {
			payload.Notes = append(payload.Notes, code+":"+entry.key)
			continue
		}
		payload.Windows = append(payload.Windows, window)
		if len(payload.Windows) >= maxCodeWindows {
			break
		}
	}
	payload.Wallet = parseCodeWallet(root)
	if len(payload.Windows) == 0 && payload.Wallet == nil {
		return CodeUsagePayload{}, "invalid_response"
	}
	return payload, ""
}

func parseCodeWindow(key, label string, raw any) (CodeUsageWindow, string) {
	object, ok := raw.(map[string]any)
	if !ok {
		return CodeUsageWindow{}, "window_unreadable"
	}
	ratio := ToAmount(object["used_ratio"])
	if ratio == nil {
		ratio = ToAmount(object["usedRatio"])
	}
	if ratio == nil {
		ratio = ToAmount(object["ratio"])
	}
	if ratio == nil {
		return CodeUsageWindow{}, "window_unreadable"
	}
	if *ratio < 0 || *ratio > 1 {
		return CodeUsageWindow{}, "window_ratio_out_of_range"
	}
	window := CodeUsageWindow{Key: key, Label: label, UsedRatio: *ratio,
		UsedPercent: round6(*ratio * 100)}
	window.RemainingPercent = round6(100 - window.UsedPercent)
	if window.RemainingPercent < 0 {
		window.RemainingPercent = 0
	}
	for _, name := range []string{"reset_time", "resetTime", "resetAt"} {
		if stamp := ToTimestamp(object[name]); stamp != nil {
			window.ResetAt = optISO(stamp)
			break
		}
	}
	return window, ""
}

// microToCents is the official reader's own conversion: the wallet carries
// millionths of a cent, and a positive fraction below one cent bills as one cent.
func microToCents(value float64) float64 {
	cents := value / 1e6
	if cents > 0 && cents < 1 {
		return 1
	}
	return round6(math.Round(cents))
}

func parseCodeWallet(root map[string]any) *CodeUsageWallet {
	var wallet map[string]any
	for _, name := range []string{"boosterWallet", "booster_wallet", "wallet"} {
		if object, ok := root[name].(map[string]any); ok {
			wallet = object
			break
		}
	}
	if wallet == nil {
		return nil
	}
	balance, ok := wallet["balance"].(map[string]any)
	if !ok {
		return nil
	}
	if kind, _ := balance["type"].(string); kind != "BOOSTER" {
		return nil
	}
	total := ToAmount(balance["amount"])
	if total == nil || *total <= 0 {
		return nil
	}
	result := &CodeUsageWallet{}
	totalCents := microToCents(*total)
	result.TotalCents = &totalCents
	if left := ToAmount(balance["amountLeft"]); left != nil {
		balanceCents := microToCents(*left)
		result.BalanceCents = &balanceCents
	}
	if limit := parseCodeMoney(wallet["monthlyChargeLimit"]); limit != nil {
		result.MonthlyLimitCents = &limit.cents
		if limit.currency != "" {
			result.Currency = optString(limit.currency)
		}
	}
	if used := parseCodeMoney(wallet["monthlyUsed"]); used != nil {
		result.MonthlyUsedCents = &used.cents
		if result.Currency == nil && used.currency != "" {
			result.Currency = optString(used.currency)
		}
	}
	if enabled, ok := wallet["monthlyChargeLimitEnabled"].(bool); ok {
		result.MonthlyLimitEnabled = &enabled
	}
	return result
}

type codeMoney struct {
	cents    float64
	currency string
}

func parseCodeMoney(value any) *codeMoney {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	cents := ToAmount(object["priceInCents"])
	if cents == nil {
		return nil
	}
	currency, _ := object["currency"].(string)
	return &codeMoney{cents: *cents, currency: strings.ToUpper(strings.TrimSpace(currency))}
}

func (c *CodeUsage) request(ctx context.Context, key string) (any, string, *int) {
	if c.client == nil {
		return nil, "network_error", nil
	}
	client := c.client
	if client.Timeout == 0 {
		copied := *client
		copied.Timeout = time.Duration(c.timeoutMS) * time.Millisecond
		client = &copied
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, CodeUsageEndpoint, nil)
	if err != nil {
		return nil, "network_error", nil
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+key)
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

// Refresh is the only method that makes a request.
func (c *CodeUsage) Refresh(ctx context.Context) CodeUsageState {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	c.mu.Lock()
	c.detectLocked()
	source, secret := c.resolveLocked()
	attemptedAt := c.now()
	c.lastAttempt = &attemptedAt
	if secret == "" {
		c.lastStatus = "not_configured"
		c.lastError = "credential_missing"
		c.mu.Unlock()
		return c.State()
	}
	tag := AccountTag(secret)
	previous := c.reading
	if previous != nil && (previous.source != source || previous.account != tag) {
		// A different sign-in is a different account: its figures are not reused.
		previous = nil
	}
	c.mu.Unlock()

	document, code, _ := c.request(ctx, secret)
	if code != "" {
		c.mu.Lock()
		c.lastStatus = "error"
		c.lastError = code
		if source == CodeSourceLocal && code == "auth_rejected" {
			c.lastError = "local_signin_expired"
		}
		if previous != nil {
			c.lastStatus = "stale"
		}
		c.mu.Unlock()
		return c.State()
	}
	parsed, code := ParseCodeUsageReply(document)
	if code != "" {
		c.mu.Lock()
		c.lastStatus = "error"
		c.lastError = code
		if previous != nil {
			c.lastStatus = "stale"
		}
		c.mu.Unlock()
		return c.State()
	}
	c.mu.Lock()
	c.reading = &codeReading{codeIdentity: codeIdentity{source: source, account: tag},
		windows: parsed.Windows, wallet: parsed.Wallet, notes: parsed.Notes, at: attemptedAt}
	c.lastStatus = "ok"
	c.lastError = ""
	c.mu.Unlock()
	return c.State()
}

// State builds the payload without making a request.
func (c *CodeUsage) State() CodeUsageState {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Detection reads this machine's sign-in file and never makes a request, so a
	// pure payload can still report which source would answer.
	c.detectLocked()
	state := CodeUsageState{SchemaVersion: 1, Endpoint: CodeUsageEndpoint,
		Status: "not_read", Windows: []CodeUsageWindow{}, Notes: []string{},
		Sources: []CodeSourceView{}, FreshnessMS: c.freshnessMS,
		LastAttemptAt: optISO(c.lastAttempt)}
	for _, entry := range []struct{ id, label, detail string }{
		{CodeSourceManual, "手动密钥", "在插件里保存一枚 Kimi Code 访问令牌"},
		{CodeSourceLocal, "本机 Kimi Code 登录", "读取本机官方客户端的登录凭据，只用于这一次请求"},
	} {
		view := CodeSourceView{ID: entry.id, Label: entry.label,
			Availability: c.availability[entry.id], AccountMasked: optString(c.accounts[entry.id]),
			Detail: optString(entry.detail)}
		view.Adopted = view.Availability == CodeAvailable
		state.Sources = append(state.Sources, view)
	}
	if c.reading != nil {
		state.ReadingSource = optString(c.reading.source)
		state.AccountMasked = optString(c.reading.account)
		state.Windows = c.reading.windows
		state.Wallet = c.reading.wallet
		state.Notes = c.reading.notes
		if state.Notes == nil {
			state.Notes = []string{}
		}
		state.ObservedAt = optISO(&c.reading.at)
		age := c.now() - c.reading.at
		state.Stale = age > c.freshnessMS
		state.Status = "ok"
		if state.Stale {
			state.Status = "stale"
		}
	}
	if c.lastError != "" {
		state.ErrorReason = optString(c.lastError)
		switch {
		case c.reading != nil:
			// Either the last attempt failed or an older reading is being shown;
			// both are a stale figure with a stated reason.
			state.Status = "stale"
			state.Stale = true
		default:
			state.Status = "error"
		}
	}
	if source, secret := c.resolveLocked(); secret == "" {
		state.Status = "not_configured"
		state.ErrorReason = optString("credential_missing")
		_ = source
	}
	return state
}

// CredentialPresent reports whether any Kimi Code source can answer. The host
// uses it to decide whether the module is worth a request at all.
func (c *CodeUsage) CredentialPresent() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, secret := c.resolveLocked()
	return secret != ""
}