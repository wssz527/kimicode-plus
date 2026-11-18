package registry

import (
	"net/http"

	"enhancedkimi/internal/httpx"
)

// Gateway is one read-only adapter entry shown next to the usage quotas.
type Gateway struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Balance   any    `json:"balance"`
	Currency  any    `json:"currency"`
	CheckedAt any    `json:"checkedAt"`
	Note      string `json:"note"`
	ReadOnly  bool   `json:"readOnly"`
}

// Gateways lists the adapters this build knows about. Balance stays unknown:
// no gateway is contacted here, and balance reads belong to the quota module.
func (s *Service) Gateways() []Gateway {
	return []Gateway{
		{ID: "mock-gateway", Name: "Mock 兼容网关", Kind: "mock", Balance: nil, Currency: nil, CheckedAt: nil, Note: "未查询远端，余额 UNKNOWN", ReadOnly: true},
		{ID: "litellm-optional", Name: "LiteLLM (未配置)", Kind: "litellm", Balance: nil, Currency: nil, CheckedAt: nil, Note: "本轮未运行 LiteLLM，余额与价格均 UNKNOWN", ReadOnly: true},
		{ID: "newapi-optional", Name: "New API (未配置)", Kind: "newapi", Balance: nil, Currency: nil, CheckedAt: nil, Note: "标准推理端点不代表提供余额 API", ReadOnly: true},
	}
}

// RegisterGateways mounts the one read-only route this package still serves.
// The usage and overview pages show these adapters next to the quotas, so the
// route outlived the model page that used to own the rest of the surface.
func (s *Service) RegisterGateways(mux *http.ServeMux) {
	mux.HandleFunc("GET /enhance/v1/gateways", func(w http.ResponseWriter, r *http.Request) {
		items := s.Gateways()
		httpx.JSON(w, 200, map[string]any{"items": items, "total": len(items)})
	})
}
