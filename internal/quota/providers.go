package quota

import "strings"

// Adapter is one documented read-only balance endpoint. Coverage is the union of
// the user's own Kimi Work+ project (DeepSeek, Moonshot CN, Moonshot global) and
// bfjnbvf/kimi-code-monitor's src/providers.js (the same three plus BigModel,
// MiniMax and StepFun).
type Adapter struct {
	// ID is stable: it travels into the panel and into the reading identity.
	ID string
	// Name is the vendor name shown next to the balance.
	Name string
	// TypeLabel is the short kind a person reads ("API余额", "Token Plan").
	TypeLabel string
	// Hosts are matched against a configured provider's base URL. Only the host
	// decides the adapter: a provider name is chosen by the user and is not an
	// identity.
	Hosts []string
	// Endpoint is fixed per adapter, so a stored provider entry cannot repoint a
	// vendor adapter at another host.
	Endpoint string
	Kind     string
	Unit     *string
	// Parse reads the vendor reply. The second result is an error code, empty on
	// success.
	Parse func(any) (Parsed, string)
	// OfficialPage documents the endpoint; ConsoleURL is where a person tops up.
	OfficialPage string
	ConsoleURL   string
	Hint         string
}

func unitOf(value string) *string { return &value }

func str(value string) *string { return &value }

// Adapters is the closed set this package may request. Adding a vendor means
// adding a documented endpoint here, which is what keeps a hand-edited config
// file from turning a provider entry into an arbitrary request.
var Adapters = []Adapter{
	{
		ID: "deepseek", Name: "DeepSeek", TypeLabel: "API余额",
		Hosts:        []string{"api.deepseek.com"},
		Endpoint:     DeepSeekEndpoint,
		Kind:         "api_balance",
		Unit:         unitOf("CNY"),
		Parse:        ParseDeepSeekBalance,
		OfficialPage: "https://api-docs.deepseek.com/api/get-user-balance",
		ConsoleURL:   "https://platform.deepseek.com/top_up",
		Hint:         "platform.deepseek.com → API keys",
	},
	{
		ID: "moonshot-cn", Name: "Kimi API", TypeLabel: "API余额",
		Hosts:        []string{"api.moonshot.cn"},
		Endpoint:     MoonshotCNEndpoint,
		Kind:         "api_balance",
		Unit:         unitOf("CNY"),
		Parse:        ParseMoonshotBalance,
		OfficialPage: "https://platform.moonshot.cn/docs/api/balance",
		ConsoleURL:   "https://platform.moonshot.cn/console/account",
		Hint:         "platform.moonshot.cn → API Key 管理",
	},
	{
		ID: "moonshot-global", Name: "Kimi API（国际）", TypeLabel: "API余额",
		Hosts:        []string{"api.moonshot.ai"},
		Endpoint:     MoonshotGlobalEndpoint,
		Kind:         "api_balance",
		Unit:         unitOf("USD"),
		Parse:        ParseMoonshotBalance,
		OfficialPage: "https://platform.kimi.ai/docs/api/balance",
		ConsoleURL:   "https://platform.kimi.ai/console/account",
		Hint:         "platform.kimi.ai → API keys",
	},
	{
		ID: "bigmodel", Name: "智谱 BigModel", TypeLabel: "Token Plan",
		Hosts:        []string{"open.bigmodel.cn", "bigmodel.cn"},
		Endpoint:     "https://open.bigmodel.cn/api/monitor/usage/quota/limit",
		Kind:         "coding_plan",
		Parse:        ParseBigModelQuota,
		OfficialPage: "https://docs.bigmodel.cn/cn/guide/platform/rate-limit",
		ConsoleURL:   "https://bigmodel.cn/usercenter/apikeys",
		Hint:         "bigmodel.cn → API keys",
	},
	{
		ID: "minimax", Name: "MiniMax", TypeLabel: "Token Plan",
		Hosts:        []string{"minimaxi.com", "minimax.io"},
		Endpoint:     "https://www.minimaxi.com/v1/token_plan/remains",
		Kind:         "coding_plan",
		Parse:        ParseMiniMaxPlan,
		OfficialPage: "https://platform.minimaxi.com/document/guides/pricing",
		ConsoleURL:   "https://platform.minimaxi.com/subscribe/coding-plan",
		Hint:         "platform.minimaxi.com → 订阅付费 → API Key",
	},
	{
		ID: "stepfun", Name: "阶跃星辰 StepFun", TypeLabel: "API余额",
		Hosts:        []string{"api.stepfun.com"},
		Endpoint:     "https://api.stepfun.com/v1/accounts",
		Kind:         "api_balance",
		Unit:         unitOf("CNY"),
		Parse:        ParseStepFunBalance,
		OfficialPage: "https://platform.stepfun.com/docs/api-reference/accounts/get",
		ConsoleURL:   "https://platform.stepfun.com/account-overview/billing",
		Hint:         "platform.stepfun.com → API Keys",
	},
}

// AdapterByID returns the adapter with that id.
func AdapterByID(id string) (Adapter, bool) {
	for _, adapter := range Adapters {
		if adapter.ID == id {
			return adapter, true
		}
	}
	return Adapter{}, false
}

// AdapterForBaseURL matches a configured provider's base URL by host. A provider
// the client manages itself (the Kimi subscription) is not matched here: its
// allowance is already the panel's top row, and asking a managed account for an
// API balance key would be a request the client never promised to answer.
func AdapterForBaseURL(baseURL string) (Adapter, string, bool) {
	host := HostOf(baseURL)
	if host == "" {
		return Adapter{}, "", false
	}
	for _, adapter := range Adapters {
		for _, candidate := range adapter.Hosts {
			if host == candidate || strings.HasSuffix(host, "."+candidate) {
				return adapter, host, true
			}
		}
	}
	return Adapter{}, host, false
}

// ProviderClass is what the panel does with one configured provider.
type ProviderClass struct {
	Kind     string `json:"kind"` // supported | managed | unsupported
	Host     string `json:"host"`
	Adapter  string `json:"adapterId,omitempty"`
	TypeLabel string `json:"typeLabel,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

const (
	ProviderSupported   = "supported"
	ProviderManaged     = "managed"
	ProviderUnsupported = "unsupported"
)

// ClassifyProvider decides a configured provider's treatment. A managed account
// is the client's own Kimi subscription; its usage is the top row of the panel and
// is not repeated in the external list.
func ClassifyProvider(providerType, baseURL string) ProviderClass {
	host := HostOf(baseURL)
	if strings.EqualFold(strings.TrimSpace(providerType), "kimi") || host == "api.kimi.com" {
		return ProviderClass{Kind: ProviderManaged, Host: host, Reason: "客户端托管账号（额度见面板顶部）"}
	}
	if adapter, _, ok := AdapterForBaseURL(baseURL); ok {
		return ProviderClass{Kind: ProviderSupported, Host: host, Adapter: adapter.ID, TypeLabel: adapter.TypeLabel}
	}
	return ProviderClass{Kind: ProviderUnsupported, Host: host, Reason: "暂无该供应商的余额查询适配"}
}