package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"enhancedkimi/internal/quota"
)

// quotaService is the core side of the balance layer. It reads the official
// providers through KAP, resolves each key for exactly one outbound request, and
// hands the panel a payload that never contains a key. The reading model is
// ported from the user's own Kimi Work+ project.
//
// Polling belongs to the caller: nothing here starts a timer, so a plugin that
// keeps its balance module collapsed simply never asks.
type quotaService struct {
	service    *NativeService
	httpClient *http.Client

	mu      sync.Mutex
	manager *quota.Manager
	initErr error
	// lastProviders is what the previous refresh saw. A provider removed from the
	// client stops being listed on the next read even if the fetch fails.
	lastProviders []quota.Provider
}

func newQuotaService(service *NativeService) *quotaService {
	return &quotaService{service: service}
}

// quotaProviderDetail is the one field this package needs from a provider
// detail. The official endpoint returns the stored key in `api_key`; it is read
// into a local variable, used for one request header, and never stored on the
// service, in a log line or in an activity record.
type quotaProviderDetail struct {
	APIKey string `json:"api_key"`
}

type quotaProviderRow struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	Type      string `json:"type"`
	HasAPIKey *bool  `json:"has_api_key"`
}

// providers lists the client's configured providers. The list call never
// returns a key; only the detail call does, and only when asked for one id.
func (q *quotaService) providers(ctx context.Context) ([]quota.Provider, error) {
	raw, err := q.service.kap.Request(ctx, http.MethodGet, "/providers", nil)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Items []quotaProviderRow `json:"items"`
		Data  struct {
			Items []quotaProviderRow `json:"items"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, errQuotaUnreadable
	}
	rows := envelope.Items
	if len(rows) == 0 {
		rows = envelope.Data.Items
	}
	out := make([]quota.Provider, 0, len(rows))
	for _, row := range rows {
		id := strings.TrimSpace(row.ID)
		if id == "" {
			continue
		}
		hasKey := row.HasAPIKey == nil || *row.HasAPIKey
		out = append(out, quota.Provider{ID: id, Name: firstNonEmpty(row.Name, id),
			BaseURL: row.BaseURL, Type: row.Type, HasAPIKey: hasKey})
	}
	return out, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

type quotaUnreadableError struct{}

func (quotaUnreadableError) Error() string { return "供应商列表响应无法识别" }

var errQuotaUnreadable = quotaUnreadableError{}

// credential reads one provider's key. The value is returned to the caller and
// nowhere else: no cache, no struct field, no error text.
func (q *quotaService) credential(ctx context.Context, providerID string) (string, error) {
	raw, err := q.service.kap.Request(ctx, http.MethodGet, "/providers/"+safeProviderID(providerID), nil)
	if err != nil {
		return "", err
	}
	var detail struct {
		quotaProviderDetail
		Data quotaProviderDetail `json:"data"`
	}
	if json.Unmarshal(raw, &detail) != nil {
		return "", errQuotaUnreadable
	}
	if detail.APIKey != "" {
		return detail.APIKey, nil
	}
	return detail.Data.APIKey, nil
}

// safeProviderID keeps a provider id from becoming a path traversal. Provider
// ids are chosen by the client, but nothing in this path may be trusted to be a
// single segment.
func safeProviderID(id string) string {
	cleaned := strings.TrimSpace(id)
	cleaned = strings.ReplaceAll(cleaned, "/", "")
	cleaned = strings.ReplaceAll(cleaned, "\\", "")
	cleaned = strings.ReplaceAll(cleaned, "..", "")
	if cleaned == "" {
		return "unknown"
	}
	return cleaned
}

// engine builds the manager on first use. A missing home directory is not fatal:
// the Kimi Code reader then reports "not configured" instead of failing the whole
// surface.
func (q *quotaService) engine() (*quota.Manager, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.manager != nil || q.initErr != nil {
		return q.manager, q.initErr
	}
	home := ""
	if q.service.kap != nil {
		home = q.service.kap.Home()
	}
	codeClient := q.httpClient
	if codeClient == nil {
		codeClient = &http.Client{Timeout: 15 * time.Second}
	}
	code := quota.NewCodeUsage(quota.CodeUsageOptions{
		HomeDir:    home,
		HTTPClient: codeClient,
	})
	magpieClient := q.httpClient
	if magpieClient == nil {
		magpieClient = &http.Client{Timeout: 5 * time.Second}
	}
	magpie, err := quota.NewMagpie(quota.MagpieOptions{
		BaseURL:    quota.MagpieDefaultBaseURL,
		HTTPClient: magpieClient,
	})
	if err != nil {
		// An unusable local gateway address must not take the vendor balances
		// down with it; the field stays nil and the payload says why.
		magpie = nil
	}
	client := q.httpClient
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	q.manager = quota.New(quota.Options{
		Credentials: q.credential,
		HTTPClient:  client,
		KimiCode:    code,
		Magpie:      magpie,
	})
	return q.manager, nil
}

// balances answers `quota.balances`. `refresh` runs the network reads; without it
// the payload is built from what was last read, which is what a panel gets when
// it reopens its balance module.
func (q *quotaService) balances(ctx context.Context, refresh bool) (quota.State, error) {
	engine, err := q.engine()
	if err != nil {
		return quota.State{}, err
	}
	providers, listErr := q.providers(ctx)
	q.mu.Lock()
	if listErr != nil {
		// A failed list keeps the previous provider set so a transient KAP hiccup
		// does not erase every balance card.
		providers = q.lastProviders
	} else {
		q.lastProviders = providers
	}
	q.mu.Unlock()
	if listErr != nil && len(providers) == 0 {
		state := engine.State(providers)
		state.ReadError = "供应商列表暂不可用，显示上次读数"
		return state, nil
	}
	var state quota.State
	if !refresh {
		state = engine.State(providers)
	} else {
		state = engine.Refresh(ctx, providers)
	}
	if listErr != nil {
		state.ReadError = "供应商列表暂不可用，显示上次读数"
	}
	return state, nil
}
