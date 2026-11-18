package quota_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"enhancedkimi/internal/quota"
)

// codeUsageReply is Kimi Work+'s fixture for the Kimi Code subscription endpoint.
const codeUsageReply = `{
  "usages": {
    "limit_5h": {"used_ratio": 0.42, "reset_time": "2026-11-16T19:01:15.24Z"},
    "limit_7d": {"used_ratio": "0.2", "reset_time": "2026-11-23T05:01:15.24Z"},
    "limit_month_total": {"used_ratio": 0.31},
    "limit_month_code": {"used_ratio": 0.05}
  },
  "boosterWallet": {
    "balance": {"type": "BOOSTER", "amount": 500000000, "amountLeft": 350000000},
    "monthlyChargeLimitEnabled": true,
    "monthlyChargeLimit": {"priceInCents": 5000, "currency": "usd"},
    "monthlyUsed": {"priceInCents": 1200, "currency": "usd"}
  }
}`

func TestKimiCodeReplyIsReadAsDocumentedWindowsAndAWallet(t *testing.T) {
	payload, code := quota.ParseCodeUsageReply(decode(t, codeUsageReply))
	if code != "" {
		t.Fatalf("code %q", code)
	}
	if len(payload.Windows) != 4 {
		t.Fatalf("windows %+v", payload.Windows)
	}
	byKey := map[string]quota.CodeUsageWindow{}
	for _, window := range payload.Windows {
		byKey[window.Key] = window
	}
	if byKey["limit_5h"].UsedRatio != 0.42 || byKey["limit_5h"].UsedPercent != 42 ||
		byKey["limit_5h"].RemainingPercent != 58 {
		t.Fatalf("limit_5h %+v", byKey["limit_5h"])
	}
	if byKey["limit_7d"].UsedRatio != 0.2 || byKey["limit_7d"].RemainingPercent != 80 {
		t.Fatalf("limit_7d %+v", byKey["limit_7d"])
	}
	if byKey["limit_5h"].ResetAt == nil || *byKey["limit_5h"].ResetAt != "2026-11-16T19:01:15.240Z" {
		t.Fatalf("reset %v", byKey["limit_5h"].ResetAt)
	}
	if _, present := byKey["limit_month_code"]; !present {
		t.Fatal("the month code window was dropped")
	}
	wallet := payload.Wallet
	if wallet == nil {
		t.Fatal("no wallet")
	}
	// The wallet's figures are in millionths of a cent: 500000000 → 500 cents.
	if wallet.TotalCents == nil || *wallet.TotalCents != 500 {
		t.Fatalf("total %v", wallet.TotalCents)
	}
	if wallet.BalanceCents == nil || *wallet.BalanceCents != 350 {
		t.Fatalf("balance %v", wallet.BalanceCents)
	}
	if wallet.Currency == nil || *wallet.Currency != "USD" {
		t.Fatalf("currency %v", wallet.Currency)
	}
	if wallet.MonthlyUsedCents == nil || *wallet.MonthlyUsedCents != 1200 {
		t.Fatalf("monthly used %v", wallet.MonthlyUsedCents)
	}
	if wallet.MonthlyLimitEnabled == nil || !*wallet.MonthlyLimitEnabled {
		t.Fatalf("monthly limit flag %v", wallet.MonthlyLimitEnabled)
	}
}

func TestKimiCodeReplyAcceptsTheCamelCaseAliases(t *testing.T) {
	payload, code := quota.ParseCodeUsageReply(decode(t,
		`{"usages":{"limit5h":{"usedRatio":0.5},"limit7d":{"usedRatio":0.1,"resetAt":"2026-11-23T05:00:00Z"}}}`))
	if code != "" || len(payload.Windows) != 2 {
		t.Fatalf("payload %+v code %q", payload, code)
	}
	if payload.Windows[0].UsedPercent != 50 {
		t.Fatalf("window %+v", payload.Windows[0])
	}
}

func TestAnUnreadableKimiCodeReplyIsAFailureAndNeverZero(t *testing.T) {
	for _, body := range []string{
		`{}`,
		`{"usages":{}}`,
		`{"usages":{"limit_5h":{}}}`,
		`{"usages":{"limit_5h":{"used_ratio":"not a number"}}}`,
		`{"usages":{"limit_5h":{"used_ratio":1.4}}}`,
		`[]`,
		`null`,
		`"text"`,
	} {
		payload, code := quota.ParseCodeUsageReply(decode(t, body))
		if code == "" {
			t.Fatalf("%s: an unreadable reply became %+v", body, payload)
		}
		if len(payload.Windows) != 0 {
			t.Fatalf("%s: a failed parse carried windows", body)
		}
	}
}

func writeSignIn(t *testing.T, home, token string) {
	t.Helper()
	dir := filepath.Join(home, ".kimi-code", "credentials")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"access_token": token, "refresh_token": token + "-r", "expires_at": 1794973223})
	if err := os.WriteFile(filepath.Join(dir, "kimi-code.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTheLocalSignInIsReadFromThisMachineAndNeverEchoed(t *testing.T) {
	const token = "{{SECRET_gv3wnqxe}}"
	home := t.TempDir()
	writeSignIn(t, home, token)
	recorder := &recorder{reply: func(call) (int, string) { return 200, codeUsageReply }}
	reader := quota.NewCodeUsage(quota.CodeUsageOptions{HomeDir: home, HTTPClient: recorder.client(),
		Now: func() int64 { return 1794196800000 }})
	state := reader.Refresh(context.Background())
	if state.Status != "ok" {
		t.Fatalf("state %+v", state)
	}
	if len(recorder.calls) != 1 || recorder.calls[0].URL != quota.CodeUsageEndpoint {
		t.Fatalf("calls %+v", recorder.calls)
	}
	if recorder.calls[0].Headers.Get("Authorization") != "Bearer "+token {
		t.Fatalf("the local sign-in was not used as a bearer")
	}
	if len(state.Windows) != 4 || state.Wallet == nil {
		t.Fatalf("windows %+v", state.Windows)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), token) {
		t.Fatal("the local token reached the panel payload")
	}
	if state.AccountMasked == nil || !strings.HasPrefix(*state.AccountMasked, "acct-") {
		t.Fatalf("account masked %v", state.AccountMasked)
	}
}

func TestAnExpiredLocalSignInIsAFailureWithItsOwnReason(t *testing.T) {
	home := t.TempDir()
	writeSignIn(t, home, "stale-token")
	recorder := &recorder{reply: func(call) (int, string) { return 401, `{"error":"expired"}` }}
	reader := quota.NewCodeUsage(quota.CodeUsageOptions{HomeDir: home, HTTPClient: recorder.client(),
		Now: func() int64 { return 1794196800000 }})
	state := reader.Refresh(context.Background())
	if state.Status != "error" {
		t.Fatalf("state %+v", state)
	}
	if state.ErrorReason == nil || *state.ErrorReason != "local_signin_expired" {
		t.Fatalf("reason %v", state.ErrorReason)
	}
	if len(state.Windows) != 0 || state.Wallet != nil {
		t.Fatalf("a refusal carried figures: %+v", state)
	}
	if state.ObservedAt != nil {
		t.Fatalf("a failed read claimed an observation time")
	}
}

func TestAFailedKimiCodeReadKeepsTheSameAccountsWindows(t *testing.T) {
	home := t.TempDir()
	writeSignIn(t, home, "live-token")
	refuse := false
	recorder := &recorder{reply: func(call) (int, string) {
		if refuse {
			return 500, `{"error":"down"}`
		}
		return 200, codeUsageReply
	}}
	clock := int64(1794196800000)
	reader := quota.NewCodeUsage(quota.CodeUsageOptions{HomeDir: home, HTTPClient: recorder.client(),
		Now: func() int64 { return clock }})
	reader.Refresh(context.Background())
	clock += 30_000
	refuse = true
	state := reader.Refresh(context.Background())
	if state.Status != "stale" || !state.Stale {
		t.Fatalf("state %+v", state)
	}
	if len(state.Windows) != 4 {
		t.Fatalf("windows lost: %+v", state.Windows)
	}
	if state.ObservedAt == nil || state.LastAttemptAt == nil || *state.ObservedAt == *state.LastAttemptAt {
		t.Fatalf("observed %v attempt %v", state.ObservedAt, state.LastAttemptAt)
	}
}

func TestNoLocalSignInMeansNoRequest(t *testing.T) {
	recorder := &recorder{reply: func(call) (int, string) { return 200, codeUsageReply }}
	reader := quota.NewCodeUsage(quota.CodeUsageOptions{HomeDir: t.TempDir(), HTTPClient: recorder.client()})
	state := reader.Refresh(context.Background())
	if len(recorder.calls) != 0 {
		t.Fatalf("a request was sent without a sign-in")
	}
	if state.Status != "not_configured" {
		t.Fatalf("state %+v", state)
	}
}

func TestKimiCodeStateMakesNoRequest(t *testing.T) {
	home := t.TempDir()
	writeSignIn(t, home, "token")
	recorder := &recorder{reply: func(call) (int, string) { return 200, codeUsageReply }}
	reader := quota.NewCodeUsage(quota.CodeUsageOptions{HomeDir: home, HTTPClient: recorder.client()})
	state := reader.State()
	if len(recorder.calls) != 0 {
		t.Fatalf("State made a request")
	}
	if state.Status != "not_read" {
		t.Fatalf("state %+v", state)
	}
	for _, source := range state.Sources {
		if source.ID == quota.CodeSourceLocal && source.Availability != quota.CodeAvailable {
			t.Fatalf("local source %+v", source)
		}
	}
}

func TestKimiHomeReadsCredentialsDirectlyWithoutDoubleDotKimiCode(t *testing.T) {
	const token = "{{SECRET_7kqz9mna}}"
	kimiHome := t.TempDir()
	credDir := filepath.Join(kimiHome, "credentials")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"access_token": token, "expires_at": 1794973223})
	if err := os.WriteFile(filepath.Join(credDir, "kimi-code.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}

	recorder := &recorder{reply: func(call) (int, string) { return 200, codeUsageReply }}
	reader := quota.NewCodeUsage(quota.CodeUsageOptions{
		KimiHome:   kimiHome,
		HTTPClient: recorder.client(),
		Now:        func() int64 { return 1794196800000 },
	})
	state := reader.Refresh(context.Background())
	if state.Status != "ok" {
		t.Fatalf("expected ok from KimiHome, got %+v", state)
	}
	if len(recorder.calls) != 1 || recorder.calls[0].Headers.Get("Authorization") != "Bearer "+token {
		t.Fatalf("expected Bearer token from KimiHome, calls=%+v", recorder.calls)
	}
}

func TestEmptyHomeNeverFallsBackToUserHomeDir(t *testing.T) {
	realHome := t.TempDir()
	writeSignIn(t, realHome, "real-host-token-must-not-be-read")
	t.Setenv("HOME", realHome)

	recorder := &recorder{reply: func(call) (int, string) { return 200, codeUsageReply }}
	reader := quota.NewCodeUsage(quota.CodeUsageOptions{HTTPClient: recorder.client()})
	state := reader.Refresh(context.Background())
	if len(recorder.calls) != 0 {
		t.Fatalf("empty home must not fall back to os.UserHomeDir, got calls=%+v", recorder.calls)
	}
	if state.Status != "not_configured" {
		t.Fatalf("expected not_configured, got %+v", state)
	}
	for _, source := range state.Sources {
		if source.ID == quota.CodeSourceLocal && source.Availability != quota.CodeMissing {
			t.Fatalf("expected local source missing, got %+v", source)
		}
	}
}