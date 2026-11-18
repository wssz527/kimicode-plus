package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"enhancedkimi/internal/quota"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func TestQuotaBalancesReadsExternalWithoutLeakingKeys(t *testing.T) {
	const deepseekSecret = "sk-secret-deepseek-9999"
	const bigmodelSecret = "bm-secret-8888"

	var listFail atomic.Bool
	var deepseekFail atomic.Bool
	var outboundCalls atomic.Int32

	kapHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/providers":
			if listFail.Load() {
				http.Error(w, "kap unavailable", http.StatusServiceUnavailable)
				return
			}
			writeNativeTestEnvelope(w, map[string]any{
				"items": []any{
					map[string]any{"id": "ds-1", "name": "DeepSeek", "base_url": "https://api.deepseek.com", "type": "openai", "has_api_key": true},
					map[string]any{"id": "bm-1", "name": "BigModel", "base_url": "https://open.bigmodel.cn/api/paas/v4", "type": "openai", "has_api_key": true},
				},
			})
		case "/api/v1/providers/ds-1":
			writeNativeTestEnvelope(w, map[string]any{"api_key": deepseekSecret})
		case "/api/v1/providers/bm-1":
			writeNativeTestEnvelope(w, map[string]any{"api_key": bigmodelSecret})
		default:
			writeNativeTestEnvelope(w, map[string]any{"items": []any{}})
		}
	})

	service, _ := newAutomationTestServiceWithHome(t, kapHandler, t.TempDir())
	service.quota.httpClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			outboundCalls.Add(1)
			switch req.URL.String() {
			case quota.DeepSeekEndpoint:
				if auth := req.Header.Get("Authorization"); auth != "Bearer "+deepseekSecret {
					t.Fatalf("deepseek auth = %q", auth)
				}
				if deepseekFail.Load() {
					return jsonResponse(http.StatusInternalServerError, `{"error":"boom"}`), nil
				}
				return jsonResponse(http.StatusOK, `{"is_available":true,"balance_infos":[{"currency":"CNY","total_balance":"20.50","granted_balance":"5.00","topped_up_balance":"15.50"}]}`), nil
			case "https://open.bigmodel.cn/api/monitor/usage/quota/limit":
				if auth := req.Header.Get("Authorization"); auth != "Bearer "+bigmodelSecret {
					t.Fatalf("bigmodel auth = %q", auth)
				}
				return jsonResponse(http.StatusOK, `{"data":{"planName":"GLM Coding Pro","limits":[{"number":5,"unit":"hour","percentage":35,"nextResetTime":1795000000000}]}}`), nil
			default:
				return jsonResponse(http.StatusNotFound, `{}`), nil
			}
		}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First refresh populates both providers.
	raw, err := service.Action(ctx, "quota.balances", json.RawMessage(`{"refresh":true}`))
	if err != nil {
		t.Fatalf("quota.balances refresh: %v", err)
	}
	state, ok := raw.(quota.State)
	if !ok {
		t.Fatalf("unexpected type %T", raw)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{deepseekSecret, bigmodelSecret} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("quota.balances leaked secret %q: %s", secret, encoded)
		}
	}
	if len(state.Cards) != 2 {
		t.Fatalf("cards = %d, want 2: %#v", len(state.Cards), state.Cards)
	}
	if state.Cards[0].Status != "ok" || len(state.Cards[0].Records) != 1 || *state.Cards[0].Records[0].Remaining != 20.5 {
		t.Fatalf("deepseek card = %#v", state.Cards[0])
	}
	if state.Cards[1].Status != "ok" || len(state.Cards[1].Records) != 1 || *state.Cards[1].Records[0].Used != 35 {
		t.Fatalf("bigmodel card = %#v", state.Cards[1])
	}

	// Reading without refresh must not issue outbound HTTP calls.
	beforeCached := outboundCalls.Load()
	cachedRaw, err := service.Action(ctx, "quota.balances", json.RawMessage(`{"refresh":false}`))
	if err != nil {
		t.Fatalf("quota.balances cached: %v", err)
	}
	if outboundCalls.Load() != beforeCached {
		t.Fatalf("refresh=false triggered %d outbound requests", outboundCalls.Load()-beforeCached)
	}
	cached := cachedRaw.(quota.State)
	if *cached.Cards[0].Records[0].Remaining != 20.5 {
		t.Fatalf("cached card = %#v", cached.Cards[0])
	}

	// Upstream failure after the 1s throttle window must keep last reading (never 0) and mark stale.
	deepseekFail.Store(true)
	time.Sleep(1050 * time.Millisecond)
	staleRaw, err := service.Action(ctx, "quota.balances", json.RawMessage(`{"refresh":true}`))
	if err != nil {
		t.Fatalf("quota.balances on upstream error: %v", err)
	}
	stale := staleRaw.(quota.State)
	if stale.Cards[0].Status != "stale" || !stale.Cards[0].Stale || *stale.Cards[0].Records[0].Remaining != 20.5 {
		t.Fatalf("stale deepseek card = %#v", stale.Cards[0])
	}

	// Provider list failure falls back to lastProviders and sets ReadError.
	listFail.Store(true)
	fallbackRaw, err := service.Action(ctx, "quota.balances", json.RawMessage(`{"refresh":false}`))
	if err != nil {
		t.Fatalf("quota.balances on list error: %v", err)
	}
	fallback := fallbackRaw.(quota.State)
	if fallback.ReadError == "" {
		t.Fatal("expected ReadError when provider list fails")
	}
	if len(fallback.Cards) != 2 || *fallback.Cards[0].Records[0].Remaining != 20.5 {
		t.Fatalf("fallback cards = %#v", fallback.Cards)
	}
}
