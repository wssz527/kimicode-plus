package quota_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"enhancedkimi/internal/quota"
)

type call struct {
	Method  string
	URL     string
	Headers http.Header
}

type recorder struct {
	mu    sync.Mutex
	calls []call
	// reply answers one request. A nil reply is a transport failure.
	reply func(call) (int, string)
}

// bodyReader keeps the response readable without a second HTTP layer in tests.
func (r *recorder) client() *http.Client {
	return &http.Client{Transport: transportFunc(func(request *http.Request) (*http.Response, error) {
		headers := request.Header.Clone()
		r.mu.Lock()
		r.calls = append(r.calls, call{Method: request.Method, URL: request.URL.String(), Headers: headers})
		r.mu.Unlock()
		if r.reply == nil {
			return nil, errors.New("dial refused")
		}
		status, payload := r.reply(call{Method: request.Method, URL: request.URL.String(), Headers: headers})
		if status == 0 {
			return nil, errors.New("dial refused")
		}
		return &http.Response{StatusCode: status, Body: nopCloser{strings.NewReader(payload)},
			Header: http.Header{}, Request: request}, nil
	})}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type nopCloser struct{ *strings.Reader }

func (nopCloser) Close() error { return nil }

func provider(id, base string, hasKey bool) quota.Provider {
	return quota.Provider{ID: id, Name: id, BaseURL: base, HasAPIKey: hasKey}
}

func manager(t *testing.T, options quota.Options) *quota.Manager {
	t.Helper()
	if options.Now == nil {
		options.Now = func() int64 { return 1794196800000 }
	}
	if options.FreshnessMS == 0 {
		options.FreshnessMS = 15 * 60 * 1000
	}
	if options.ThrottleMS == 0 {
		options.ThrottleMS = -1
	}
	return quota.New(options)
}

func findCard(t *testing.T, state quota.State, providerID string) quota.SourceCard {
	t.Helper()
	for _, card := range state.Cards {
		if card.ProviderID == providerID {
			return card
		}
	}
	t.Fatalf("card %s missing from %+v", providerID, state.Cards)
	return quota.SourceCard{}
}

func TestRefreshAsksTheDocumentedEndpointWithTheResolvedKey(t *testing.T) {
	recorder := &recorder{reply: func(c call) (int, string) { return 200, deepSeekBalance }}
	accounts := manager(t, quota.Options{
		HTTPClient: recorder.client(),
		Credentials: func(ctx context.Context, id string) (string, error) {
			return "sk-test-secret-value", nil
		},
	})
	state := accounts.Refresh(context.Background(), []quota.Provider{provider("deepseek", "https://api.deepseek.com", true)})
	if len(recorder.calls) != 1 {
		t.Fatalf("calls %d", len(recorder.calls))
	}
	if recorder.calls[0].URL != quota.DeepSeekEndpoint || recorder.calls[0].Method != "GET" {
		t.Fatalf("call %+v", recorder.calls[0])
	}
	if recorder.calls[0].Headers.Get("Authorization") != "Bearer sk-test-secret-value" {
		t.Fatalf("authorization not sent")
	}
	card := findCard(t, state, "deepseek")
	if card.Status != "ok" || card.ObservedAt == nil || card.AccountTag == "" {
		t.Fatalf("card %+v", card)
	}
	if len(card.Records) != 2 {
		t.Fatalf("records %+v", card.Records)
	}
	if card.Records[0].Status != "ok" || card.Records[0].ObservedAt == nil {
		t.Fatalf("record status %+v", card.Records[0])
	}
}

func TestAFailedRefreshKeepsTheLastGoodReadingAndMarksItStale(t *testing.T) {
	status := 200
	recorder := &recorder{reply: func(c call) (int, string) {
		if status == 200 {
			return 200, deepSeekBalance
		}
		return status, `{"error":{"message":"internal"}}`
	}}
	accounts := manager(t, quota.Options{HTTPClient: recorder.client(),
		Credentials: func(ctx context.Context, id string) (string, error) { return "sk-live", nil }})
	providers := []quota.Provider{provider("deepseek", "https://api.deepseek.com", true)}
	first := accounts.Refresh(context.Background(), providers)
	before := findCard(t, first, "deepseek")
	if before.Status != "ok" {
		t.Fatalf("first read %+v", before)
	}
	status = 500
	second := accounts.Refresh(context.Background(), providers)
	after := findCard(t, second, "deepseek")
	if after.Status != "stale" || !after.Stale {
		t.Fatalf("failed read hid its state: %+v", after)
	}
	if after.ErrorReason == nil || *after.ErrorReason != "upstream_error" {
		t.Fatalf("reason %v", after.ErrorReason)
	}
	// The numbers are the previous ones, and they say when they were read.
	if len(after.Records) != 2 || after.Records[0].Remaining == nil || *after.Records[0].Remaining != 110 {
		t.Fatalf("records lost: %+v", after.Records)
	}
	if after.ObservedAt == nil || after.ObservedAt == after.LastAttemptAt {
		t.Fatalf("observedAt %v attemptAt %v", after.ObservedAt, after.LastAttemptAt)
	}
	for _, record := range after.Records {
		if record.Remaining == nil || *record.Remaining == 0 && record.Unit != nil && *record.Unit == "CNY" {
			t.Fatalf("a failure was written as zero: %+v", record)
		}
	}
}

func TestFirstFailureWithNoEarlierReadingIsAnErrorWithNoNumber(t *testing.T) {
	recorder := &recorder{reply: func(c call) (int, string) { return 403, "forbidden" }}
	accounts := manager(t, quota.Options{HTTPClient: recorder.client(),
		Credentials: func(ctx context.Context, id string) (string, error) { return "sk-refused", nil }})
	state := accounts.Refresh(context.Background(), []quota.Provider{provider("deepseek", "https://api.deepseek.com", true)})
	card := findCard(t, state, "deepseek")
	if card.Status != "error" || len(card.Records) != 0 {
		t.Fatalf("card %+v", card)
	}
	if card.ErrorReason == nil || *card.ErrorReason != "auth_rejected" {
		t.Fatalf("reason %v", card.ErrorReason)
	}
	if card.ObservedAt != nil {
		t.Fatalf("a failed read claimed an observation time: %v", card.ObservedAt)
	}
}

func TestNoCredentialMeansNoRequestAndANotConfiguredCard(t *testing.T) {
	recorder := &recorder{reply: func(c call) (int, string) { return 200, deepSeekBalance }}
	accounts := manager(t, quota.Options{HTTPClient: recorder.client(),
		Credentials: func(ctx context.Context, id string) (string, error) { return "", nil }})
	state := accounts.Refresh(context.Background(), []quota.Provider{provider("deepseek", "https://api.deepseek.com", true)})
	if len(recorder.calls) != 0 {
		t.Fatalf("a request was sent without a credential")
	}
	card := findCard(t, state, "deepseek")
	if card.Status != "not_configured" {
		t.Fatalf("card %+v", card)
	}
}

func TestAnotherAccountsNumbersAreNotShownAsThisAccountsStale(t *testing.T) {
	key := "sk-account-a"
	status := 200
	recorder := &recorder{reply: func(c call) (int, string) {
		if status != 200 {
			return status, `{}`
		}
		return 200, deepSeekBalance
	}}
	accounts := manager(t, quota.Options{HTTPClient: recorder.client(),
		Credentials: func(ctx context.Context, id string) (string, error) { return key, nil }})
	providers := []quota.Provider{provider("deepseek", "https://api.deepseek.com", true)}
	first := accounts.Refresh(context.Background(), providers)
	if findCard(t, first, "deepseek").Status != "ok" {
		t.Fatal("setup read failed")
	}
	// Switching the stored key is switching the account: the previous account's
	// balance must not come back as this one's stale reading.
	key = "sk-account-b"
	status = 500
	second := accounts.Refresh(context.Background(), providers)
	card := findCard(t, second, "deepseek")
	if card.Status != "error" || len(card.Records) != 0 {
		t.Fatalf("another account's figures leaked: %+v", card)
	}
	// And the tag follows the key that answered, not the key that was configured
	// first.
	if card.AccountTag == quota.AccountTag("sk-account-a") || card.AccountTag != quota.AccountTag("sk-account-b") {
		t.Fatalf("account tag %q", card.AccountTag)
	}
}

func TestTheCredentialNeverAppearsInAnyAnswer(t *testing.T) {
	const secret = "sk-very-secret-token-9f8e7d6c"
	recorder := &recorder{reply: func(c call) (int, string) { return 500, `{"echo":"` + secret + `"}` }}
	accounts := manager(t, quota.Options{HTTPClient: recorder.client(),
		Credentials: func(ctx context.Context, id string) (string, error) { return secret, nil }})
	state := accounts.Refresh(context.Background(), []quota.Provider{provider("deepseek", "https://api.deepseek.com", true)})
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("encode state: %v", err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("the credential reached the panel payload")
	}
	// The tail of the key is a partial secret too: the upstream widget printed the
	// last four characters, and that is still key material.
	if strings.Contains(string(encoded), secret[len(secret)-4:]) {
		t.Fatalf("a key tail reached the panel payload")
	}
	// The error body is echoed by this fixture; it must not survive either.
	if strings.Contains(string(encoded), "echo") {
		t.Fatalf("an upstream error body reached the panel payload")
	}
}

func TestUnsupportedAndManagedProvidersAreNotRequested(t *testing.T) {
	recorder := &recorder{reply: func(c call) (int, string) { return 200, deepSeekBalance }}
	accounts := manager(t, quota.Options{HTTPClient: recorder.client(),
		Credentials: func(ctx context.Context, id string) (string, error) { return "sk-key", nil }})
	state := accounts.Refresh(context.Background(), []quota.Provider{
		provider("relay", "http://127.0.0.1:8081/v1", true),
		provider("kimi", "https://api.kimi.com/coding/v1", true),
	})
	if len(recorder.calls) != 0 {
		t.Fatalf("calls %d", len(recorder.calls))
	}
	if len(state.Cards) != 1 {
		t.Fatalf("a managed account was listed as an external balance: %+v", state.Cards)
	}
	if state.Cards[0].Status != "unsupported" {
		t.Fatalf("card %+v", state.Cards[0])
	}
}

func TestAnOldReadingIsStaleWithoutAnyFailure(t *testing.T) {
	now := int64(1794196800000)
	recorder := &recorder{reply: func(c call) (int, string) { return 200, deepSeekBalance }}
	accounts := manager(t, quota.Options{HTTPClient: recorder.client(), Now: func() int64 { return now },
		FreshnessMS: 60_000,
		Credentials: func(ctx context.Context, id string) (string, error) { return "sk-live", nil }})
	providers := []quota.Provider{provider("deepseek", "https://api.deepseek.com", true)}
	accounts.Refresh(context.Background(), providers)
	state := accounts.State(providers)
	card := findCard(t, state, "deepseek")
	if card.Status != "ok" {
		t.Fatalf("card %+v", card)
	}
	now += 120_000
	state = accounts.State(providers)
	card = findCard(t, state, "deepseek")
	if card.Status != "stale" || !card.Stale || card.StaleReason == nil || *card.StaleReason != "age" {
		t.Fatalf("aged reading %+v", card)
	}
}

func TestStateMakesNoRequest(t *testing.T) {
	recorder := &recorder{reply: func(c call) (int, string) { return 200, deepSeekBalance }}
	accounts := manager(t, quota.Options{HTTPClient: recorder.client(),
		Credentials: func(ctx context.Context, id string) (string, error) { return "sk-live", nil }})
	providers := []quota.Provider{provider("deepseek", "https://api.deepseek.com", true)}
	state := accounts.State(providers)
	card := findCard(t, state, "deepseek")
	if card.Status != "not_read" {
		t.Fatalf("unread card %+v", card)
	}
	if len(recorder.calls) != 0 {
		t.Fatalf("State made %d requests", len(recorder.calls))
	}
}

func TestReadingsAreNotSummedAcrossProviders(t *testing.T) {
	recorder := &recorder{reply: func(c call) (int, string) {
		if strings.Contains(c.URL, "deepseek") {
			return 200, deepSeekBalance
		}
		return 200, moonshotBalance
	}}
	accounts := manager(t, quota.Options{HTTPClient: recorder.client(),
		Credentials: func(ctx context.Context, id string) (string, error) { return "sk-key", nil }})
	state := accounts.Refresh(context.Background(), []quota.Provider{
		provider("deepseek", "https://api.deepseek.com", true),
		provider("moonshot", "https://api.moonshot.cn/v1", true),
	})
	if len(state.Records) != 3 {
		t.Fatalf("records %+v", state.Records)
	}
	if state.Combining != "never-summed" {
		t.Fatalf("combining %q", state.Combining)
	}
	for _, record := range state.Records {
		if record.Kind == "" {
			t.Fatalf("a record lost its kind: %+v", record)
		}
	}
}