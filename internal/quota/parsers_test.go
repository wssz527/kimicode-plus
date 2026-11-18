package quota_test

import (
	"encoding/json"
	"testing"

	"enhancedkimi/internal/quota"
)

// The DeepSeek, Moonshot and Kimi Code fixtures are the ones Kimi Work+ uses, so
// a change in either product shows up as a difference in the same numbers.
const deepSeekBalance = `{"is_available":true,"balance_infos":[
	{"currency":"USD","total_balance":"0.00","granted_balance":"0.00","topped_up_balance":"0.00"},
	{"currency":"CNY","total_balance":"110.00","granted_balance":"10.00","topped_up_balance":"100.00"}]}`

const moonshotBalance = `{"code":0,"data":{"available_balance":49.58894,"voucher_balance":46.58893,"cash_balance":3.00001},"scode":"0x0","status":true}`

const bigModelQuota = `{"code":200,"msg":"success","data":{"planName":"GLM Coding Pro","limits":[
	{"type":"TOKENS_LIMIT","number":5,"unit":"hour","percentage":62.5,"nextResetTime":1794196800000},
	{"type":"TIME_LIMIT","number":1,"unit":"month","usage":3500000,"total":5600000,"nextResetTime":1794973223000}]}}`

const miniMaxPlan = `{"base_resp":{"status_code":0},"model_remains":[
	{"model_name":"general","current_interval_remaining_percent":66,"end_time":1794196800000,
	 "current_weekly_remaining_percent":12.5,"weekly_end_time":1794973223000}]}`

const stepFunBalance = `{"balance":88.42,"total_voucher_balance":8.42,"total_cash_balance":80}`

func decode(t *testing.T, body string) any {
	t.Helper()
	var document any
	if err := json.Unmarshal([]byte(body), &document); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return document
}

func TestDeepSeekReplyBecomesOneRecordPerCurrency(t *testing.T) {
	parsed, code := quota.ParseDeepSeekBalance(decode(t, deepSeekBalance))
	if code != "" {
		t.Fatalf("code %q", code)
	}
	if parsed.Available == nil || !*parsed.Available {
		t.Fatalf("available %v", parsed.Available)
	}
	// The currency with money left comes first, so a card does not reorder itself
	// between two reads.
	if len(parsed.Records) != 2 || *parsed.Records[0].Unit != "CNY" || *parsed.Records[1].Unit != "USD" {
		t.Fatalf("records %+v", parsed.Records)
	}
	if *parsed.Records[0].Remaining != 110 || *parsed.Records[1].Remaining != 0 {
		t.Fatalf("remaining %v %v", *parsed.Records[0].Remaining, *parsed.Records[1].Remaining)
	}
	// A zero balance is a real 0 and stays 0.
	if len(parsed.Parts) != 2 || parsed.Parts[0].Label != "赠金" || parsed.Parts[0].Amount != 10 ||
		parsed.Parts[1].Label != "充值" || parsed.Parts[1].Amount != 100 {
		t.Fatalf("parts %+v", parsed.Parts)
	}
}

func TestDeepSeekWithoutAReadableAmountIsAFailureAndNeverZero(t *testing.T) {
	for _, body := range []string{
		`{"is_available":true,"balance_infos":[]}`,
		`{"is_available":true}`,
		`{"balance_infos":[{"currency":"CNY","total_balance":"n/a"}]}`,
		`null`,
	} {
		parsed, code := quota.ParseDeepSeekBalance(decode(t, body))
		if code == "" {
			t.Fatalf("%s: an unreadable reply became a balance: %+v", body, parsed)
		}
		if len(parsed.Records) != 0 {
			t.Fatalf("%s: a failed parse carried records", body)
		}
	}
}

func TestMoonshotReplyReadsTheAvailableBalance(t *testing.T) {
	parsed, code := quota.ParseMoonshotBalance(decode(t, moonshotBalance))
	if code != "" || *parsed.Records[0].Remaining != 49.58894 {
		t.Fatalf("parsed %+v code %q", parsed, code)
	}
	if len(parsed.Parts) != 2 || parsed.Parts[0].Label != "代金券" || parsed.Parts[1].Label != "现金" {
		t.Fatalf("parts %+v", parsed.Parts)
	}
	// `code` other than 0 is the platform's own refusal, and its words are not
	// read.
	for _, body := range []string{`{"code":401,"status":false}`, `{"code":0,"data":{}}`} {
		if _, code := quota.ParseMoonshotBalance(decode(t, body)); code == "" {
			t.Fatalf("%s: a refusal became a balance", body)
		}
	}
}

func TestBigModelPlanReportsOneWindowPerLimitAndNeverSumsThem(t *testing.T) {
	parsed, code := quota.ParseBigModelQuota(decode(t, bigModelQuota))
	if code != "" {
		t.Fatalf("code %q", code)
	}
	if len(parsed.Records) != 2 {
		t.Fatalf("records %+v", parsed.Records)
	}
	five := parsed.Records[0]
	// The 5-hour limit is a percentage window: used 62.5 out of 100, and the unit
	// says percent so a renderer cannot read it as money.
	if five.WindowLabel == nil || *five.WindowLabel != "5h" || *five.Used != 62.5 || *five.Limit != 100 {
		t.Fatalf("5h window %+v", five)
	}
	if five.Unit == nil || *five.Unit != "percent" {
		t.Fatalf("5h unit %v", five.Unit)
	}
	// A TIME_LIMIT is the MCP monthly allowance even though it carries number=1
	// unit=month; the general duration branch would label it "5h".
	mcp := parsed.Records[1]
	if mcp.WindowLabel == nil || *mcp.WindowLabel != "MCP" {
		t.Fatalf("MCP window %+v", mcp)
	}
	if mcp.Used == nil || *mcp.Used != 62.5 {
		t.Fatalf("MCP used %v", mcp.Used)
	}
	if quota.PlanNameOf(decode(t, bigModelQuota)) != "GLM Coding Pro" {
		t.Fatalf("plan name %q", quota.PlanNameOf(decode(t, bigModelQuota)))
	}
}

func TestBigModelWithoutAWindowIsAFailure(t *testing.T) {
	for _, body := range []string{`{"data":{}}`, `{"data":{"limits":[]}}`, `null`, `[]`} {
		if _, code := quota.ParseBigModelQuota(decode(t, body)); code == "" {
			t.Fatalf("%s: an unreadable reply became a quota", body)
		}
	}
}

func TestMiniMaxPlanReadsRemainingPercentAsUsed(t *testing.T) {
	parsed, code := quota.ParseMiniMaxPlan(decode(t, miniMaxPlan))
	if code != "" {
		t.Fatalf("code %q", code)
	}
	if len(parsed.Records) != 2 {
		t.Fatalf("records %+v", parsed.Records)
	}
	if *parsed.Records[0].WindowLabel != "5h" || *parsed.Records[0].Used != 34 {
		t.Fatalf("5h %+v", parsed.Records[0])
	}
	if *parsed.Records[1].WindowLabel != "1w" || *parsed.Records[1].Used != 87.5 {
		t.Fatalf("1w %+v", parsed.Records[1])
	}
}

func TestMiniMaxRefusalInsideHTTP200IsAFailure(t *testing.T) {
	body := `{"base_resp":{"status_code":1004,"status_msg":"invalid api key"},"model_remains":[]}`
	if _, code := quota.ParseMiniMaxPlan(decode(t, body)); code != "auth_rejected" {
		t.Fatalf("code %q", code)
	}
}

func TestStepFunBalanceReadsThePrepaidTotal(t *testing.T) {
	parsed, code := quota.ParseStepFunBalance(decode(t, stepFunBalance))
	if code != "" {
		t.Fatalf("code %q", code)
	}
	if len(parsed.Records) != 1 || *parsed.Records[0].Remaining != 88.42 {
		t.Fatalf("records %+v", parsed.Records)
	}
	if parsed.Records[0].Unit == nil || *parsed.Records[0].Unit != "CNY" {
		t.Fatalf("unit %v", parsed.Records[0].Unit)
	}
	for _, body := range []string{`{}`, `{"balance":"n/a"}`, `null`} {
		if _, code := quota.ParseStepFunBalance(decode(t, body)); code == "" {
			t.Fatalf("%s: a missing balance became a number", body)
		}
	}
}

func TestAdaptersMatchByHostAndCoverBothProducts(t *testing.T) {
	cases := []struct {
		base string
		want string
	}{
		{"https://api.deepseek.com", "deepseek"},
		{"https://api.deepseek.com/v1", "deepseek"},
		{"https://api.moonshot.cn/v1", "moonshot-cn"},
		{"https://api.moonshot.ai/v1", "moonshot-global"},
		{"https://open.bigmodel.cn/api/paas/v4", "bigmodel"},
		{"https://api.minimaxi.com/v1", "minimax"},
		{"https://api.stepfun.com/step_plan/v1", "stepfun"},
	}
	for _, item := range cases {
		adapter, host, ok := quota.AdapterForBaseURL(item.base)
		if !ok || adapter.ID != item.want {
			t.Fatalf("%s: adapter %q host %q ok %v, want %q", item.base, adapter.ID, host, ok, item.want)
		}
	}
	if _, _, ok := quota.AdapterForBaseURL("http://127.0.0.1:8081/v1"); ok {
		t.Fatal("a local relay was matched to a vendor adapter")
	}
}

func TestClassifyProviderKeepsManagedAndUnknownApart(t *testing.T) {
	managed := quota.ClassifyProvider("kimi", "https://api.kimi.com/coding/v1")
	if managed.Kind != quota.ProviderManaged {
		t.Fatalf("managed %+v", managed)
	}
	// A user-chosen provider name with spaces still matches by host only.
	supported := quota.ClassifyProvider("", "https://api.stepfun.com/step_plan/v1")
	if supported.Kind != quota.ProviderSupported || supported.Adapter != "stepfun" {
		t.Fatalf("supported %+v", supported)
	}
	unknown := quota.ClassifyProvider("relay", "http://127.0.0.1:8081/v1")
	if unknown.Kind != quota.ProviderUnsupported {
		t.Fatalf("unknown %+v", unknown)
	}
}