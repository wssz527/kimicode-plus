package quota_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enhancedkimi/internal/quota"
)

func magpieFixture(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "magpie-quotas.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestMagpieQuotasAreReadFromTheDocumentedReply(t *testing.T) {
	snapshot, code := quota.ParseMagpieQuotas(decode(t, magpieFixture(t)))
	if code != "" {
		t.Fatalf("code %q", code)
	}
	if len(snapshot.Entries) != 4 {
		t.Fatalf("entries %d", len(snapshot.Entries))
	}
	first := snapshot.Entries[0]
	if first.Provider != "kimi-for-coding" || len(first.Windows) != 2 {
		t.Fatalf("entry %+v", first)
	}
	if first.Windows[0].Basis != quota.MagpieBasisPercent || first.Windows[0].Used == nil || *first.Windows[0].Used != 20 {
		t.Fatalf("window %+v", first.Windows[0])
	}
	if first.Windows[0].ResetAt == nil {
		t.Fatal("a reset time was dropped")
	}
	// A balance entry carries its amount as a formatted string: the currency is
	// read from the symbol, and the text is kept verbatim.
	var balance *quota.MagpieEntry
	for index := range snapshot.Entries {
		if snapshot.Entries[index].Kind == "balance" {
			balance = &snapshot.Entries[index]
			break
		}
	}
	if balance == nil || balance.Balance == nil || *balance.Balance != "$19.18" {
		t.Fatalf("balance entry %+v", balance)
	}
	if balance.BalanceAmount == nil || *balance.BalanceAmount != 19.18 ||
		balance.BalanceCurrency == nil || *balance.BalanceCurrency != "USD" {
		t.Fatalf("parsed balance %+v", balance)
	}
}

func TestAWindowWithOnlyADisplayStringIsKeptAsTextAndNotAsZero(t *testing.T) {
	body := `[{"provider":"relay","name":"Relay","kind":"subscription","windows":[
		{"name":"Total","display":"unknown"}]}]`
	snapshot, code := quota.ParseMagpieQuotas(decode(t, body))
	if code != "" {
		t.Fatalf("code %q", code)
	}
	window := snapshot.Entries[0].Windows[0]
	if window.Basis != quota.MagpieBasisText {
		t.Fatalf("window %+v", window)
	}
	if window.Amount != nil || window.Limit != nil || window.Used != nil || window.Remaining != nil {
		t.Fatalf("a text-only window invented a number: %+v", window)
	}
}

func TestAnUnreadableMagpieReplyIsFailureAndAnEmptyListIsNot(t *testing.T) {
	for _, body := range []string{`null`, `"text"`, `{"data":{}}`, `[{"provider":""}]`} {
		if _, code := quota.ParseMagpieQuotas(decode(t, body)); code == "" {
			t.Fatalf("%s: an unreadable reply became a snapshot", body)
		}
	}
	snapshot, code := quota.ParseMagpieQuotas(decode(t, `[]`))
	if code != "" || snapshot == nil || len(snapshot.Entries) != 0 {
		t.Fatalf("an empty list %+v code %q", snapshot, code)
	}
}

func TestTheHTTPEnvelopeIsAcceptedBecauseTheGatewayWrapsTheArray(t *testing.T) {
	body := `{"data":[{"provider":"relay","kind":"balance","balance":"¥12.50"}]}`
	snapshot, code := quota.ParseMagpieQuotas(decode(t, body))
	if code != "" || len(snapshot.Entries) != 1 {
		t.Fatalf("snapshot %+v code %q", snapshot, code)
	}
	if snapshot.Entries[0].BalanceCurrency == nil || *snapshot.Entries[0].BalanceCurrency != "CNY" {
		t.Fatalf("entry %+v", snapshot.Entries[0])
	}
}

func TestABalanceStringThisPackageDoesNotParseKeepsTheTextAndSaysSo(t *testing.T) {
	body := `[{"provider":"relay","kind":"balance","balance":"1.2k credits"}]`
	snapshot, code := quota.ParseMagpieQuotas(decode(t, body))
	if code != "" {
		t.Fatalf("code %q", code)
	}
	entry := snapshot.Entries[0]
	if entry.BalanceAmount != nil {
		t.Fatalf("an unparsed amount became a number: %+v", entry)
	}
	if entry.Balance == nil || *entry.Balance != "1.2k credits" {
		t.Fatalf("the text was dropped: %+v", entry)
	}
	if len(snapshot.Notes) == 0 {
		t.Fatal("an unparsed balance was not reported")
	}
}

func TestARefreshAsksTheDocumentedPathOnTheConfiguredGateway(t *testing.T) {
	recorder := &recorder{reply: func(call) (int, string) { return 200, magpieFixture(t) }}
	reader, err := quota.NewMagpie(quota.MagpieOptions{BaseURL: "http://127.0.0.1:3430",
		HTTPClient: recorder.client()})
	if err != nil {
		t.Fatal(err)
	}
	state := reader.Refresh(context.Background())
	if len(recorder.calls) != 1 || recorder.calls[0].URL != "http://127.0.0.1:3430/v1/magpie/quotas" {
		t.Fatalf("calls %+v", recorder.calls)
	}
	// A local Magpie answers without a key, so none is invented.
	if recorder.calls[0].Headers.Get("Authorization") != "" {
		t.Fatal("an unconfigured key was sent")
	}
	if state.Status != "ok" || state.Snapshot == nil || state.Snapshot.FetchedAt == nil {
		t.Fatalf("state %+v", state)
	}
}

func TestASharedGatewayKeyIsSentBothWaysMagpieAccepts(t *testing.T) {
	recorder := &recorder{reply: func(call) (int, string) { return 200, magpieFixture(t) }}
	reader, err := quota.NewMagpie(quota.MagpieOptions{BaseURL: quota.MagpieDefaultBaseURL,
		APIKey: func() string { return "gateway-key" }, HTTPClient: recorder.client()})
	if err != nil {
		t.Fatal(err)
	}
	reader.Refresh(context.Background())
	if recorder.calls[0].Headers.Get("Authorization") != "Bearer gateway-key" {
		t.Fatalf("authorization %q", recorder.calls[0].Headers.Get("Authorization"))
	}
	if recorder.calls[0].Headers.Get("x-api-key") != "gateway-key" {
		t.Fatalf("x-api-key %q", recorder.calls[0].Headers.Get("x-api-key"))
	}
}

func TestAMagpieThatIsNotRunningIsReportedAsUnreachable(t *testing.T) {
	reader, err := quota.NewMagpie(quota.MagpieOptions{BaseURL: quota.MagpieDefaultBaseURL,
		HTTPClient: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			return nil, os.ErrDeadlineExceeded
		})}})
	if err != nil {
		t.Fatal(err)
	}
	state := reader.Refresh(context.Background())
	if state.Status != "error" || state.ErrorReason == nil || *state.ErrorReason != "magpie_unreachable" {
		t.Fatalf("state %+v", state)
	}
}

func TestAFailedReadAfterAGoodOneKeepsTheNumbersAndMarksThemStale(t *testing.T) {
	fail := false
	recorder := &recorder{reply: func(call) (int, string) {
		if fail {
			return 503, `{"error":"busy"}`
		}
		return 200, magpieFixture(t)
	}}
	reader, err := quota.NewMagpie(quota.MagpieOptions{BaseURL: quota.MagpieDefaultBaseURL,
		HTTPClient: recorder.client()})
	if err != nil {
		t.Fatal(err)
	}
	reader.Refresh(context.Background())
	fail = true
	state := reader.Refresh(context.Background())
	if state.Status != "stale" || !state.Stale || len(state.Snapshot.Entries) != 4 {
		t.Fatalf("state %+v", state)
	}
}

func TestAnAddressWithACredentialInItIsRefusedBeforeAnyRequest(t *testing.T) {
	if _, err := quota.NewMagpie(quota.MagpieOptions{BaseURL: "http://user:pass@127.0.0.1:3425"}); err == nil {
		t.Fatal("a URL with userinfo was accepted")
	}
	if _, err := quota.NewMagpie(quota.MagpieOptions{BaseURL: "http://10.0.0.5:3425"}); err == nil {
		t.Fatal("a private address was accepted")
	}
	if _, err := quota.NewMagpie(quota.MagpieOptions{BaseURL: "https://example.test/?key=1"}); err == nil {
		t.Fatal("a URL with a query was accepted")
	}
}

func TestTheStateMakesNoRequest(t *testing.T) {
	recorder := &recorder{reply: func(call) (int, string) { return 200, magpieFixture(t) }}
	reader, err := quota.NewMagpie(quota.MagpieOptions{BaseURL: quota.MagpieDefaultBaseURL,
		HTTPClient: recorder.client()})
	if err != nil {
		t.Fatal(err)
	}
	state := reader.State()
	if state.Status != "not_read" || state.Snapshot != nil {
		t.Fatalf("state %+v", state)
	}
	if len(recorder.calls) != 0 {
		t.Fatal("State made a request")
	}
}

func TestMagpieSnapshotNeverCarriesTheGatewayKey(t *testing.T) {
	const key = "magpie-secret-gateway-key"
	recorder := &recorder{reply: func(call) (int, string) { return 200, magpieFixture(t) }}
	reader, err := quota.NewMagpie(quota.MagpieOptions{BaseURL: quota.MagpieDefaultBaseURL,
		APIKey: func() string { return key }, HTTPClient: recorder.client()})
	if err != nil {
		t.Fatal(err)
	}
	state := reader.Refresh(context.Background())
	encoded, marshalErr := json.Marshal(state)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(encoded), key) {
		t.Fatal("the gateway key reached the panel payload")
	}
}