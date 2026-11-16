package registry

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"enhancedkimi/internal/httpx"
)

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

// Gateways exposes the read-only mock adapters. Balance is deliberately unknown:
// no gateway was contacted and this build has no credential or balance API.
func (s *Service) Gateways() []Gateway {
	return []Gateway{
		{ID: "mock-gateway", Name: "Mock 兼容网关", Kind: "mock", Balance: nil, Currency: nil, CheckedAt: nil, Note: "未查询远端，余额 UNKNOWN", ReadOnly: true},
		{ID: "litellm-optional", Name: "LiteLLM (未配置)", Kind: "litellm", Balance: nil, Currency: nil, CheckedAt: nil, Note: "本轮未运行 LiteLLM，余额与价格均 UNKNOWN", ReadOnly: true},
		{ID: "newapi-optional", Name: "New API (未配置)", Kind: "newapi", Balance: nil, Currency: nil, CheckedAt: nil, Note: "标准推理端点不代表提供余额 API", ReadOnly: true},
	}
}

func (s *Service) Register(mux *http.ServeMux) {
	s.registerCatalog(mux)
	mux.HandleFunc("GET /enhance/v1/registry/snapshot", s.handleSnapshot)
	mux.HandleFunc("POST /enhance/v1/models/metadata", s.handleMetadataEdit)
	mux.HandleFunc("GET /enhance/v1/model-changes/{id}/preview", s.handleRollbackPreview)
	mux.HandleFunc("GET /enhance/v1/providers", s.handleListProviders)
	mux.HandleFunc("POST /enhance/v1/providers", s.handleCreateProvider)
	mux.HandleFunc("PUT /enhance/v1/providers/{id}", s.handleUpdateProvider)
	mux.HandleFunc("GET /enhance/v1/provider-presets", s.handlePresets)
	mux.HandleFunc("GET /enhance/v1/gateways", s.handleGateways)
	mux.HandleFunc("GET /enhance/v1/models", s.handleListModels)
	mux.HandleFunc("POST /enhance/v1/models/discovery", s.handleDiscovery)
	mux.HandleFunc("POST /enhance/v1/models/preview", s.handlePreview)
	mux.HandleFunc("POST /enhance/v1/models/import", s.handleImport)
	mux.HandleFunc("POST /enhance/v1/models/override", s.handleOverride)
	mux.HandleFunc("POST /enhance/v1/models/modelsdev", s.handleModelsDev)
	mux.HandleFunc("GET /enhance/v1/model-changes", s.handleListChanges)
	mux.HandleFunc("GET /enhance/v1/model-changes/{id}", s.handleChangeDetail)
	mux.HandleFunc("POST /enhance/v1/model-changes/{id}/rollback", s.handleRollback)
}

func fail(w http.ResponseWriter, err error) {
	status, code, message := errorResponse(err)
	httpx.Fail(w, status, code, message)
}

func (s *Service) handleListProviders(w http.ResponseWriter, r *http.Request) {
	items, err := s.ListProviders(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

type createProviderRequest struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Protocol  string  `json:"protocol"`
	BaseURL   string  `json:"baseUrl"`
	SecretRef *string `json:"secretRef"`
}

func (s *Service) handleCreateProvider(w http.ResponseWriter, r *http.Request) {
	var req createProviderRequest
	if !httpx.Decode(w, r, &req) {
		return
	}
	provider, err := s.CreateProvider(r.Context(), providerInput(req))
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, provider)
}

type updateProviderRequest struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Protocol  string  `json:"protocol"`
	BaseURL   string  `json:"baseUrl"`
	SecretRef *string `json:"secretRef"`
	Revision  *int64  `json:"revision"`
}

func (s *Service) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	var req updateProviderRequest
	if !httpx.Decode(w, r, &req) {
		return
	}
	if req.Revision == nil {
		fail(w, badRequest("MISSING_REVISION", "更新供应商必须提供当前 revision 以防止覆盖并发修改"))
		return
	}
	result, err := s.UpdateProvider(r.Context(), r.PathValue("id"), providerInput{
		ID: req.ID, Name: req.Name, Protocol: req.Protocol, BaseURL: req.BaseURL, SecretRef: req.SecretRef,
	}, *req.Revision)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}

func (s *Service) handlePresets(w http.ResponseWriter, r *http.Request) {
	presets := s.ProviderPresets()
	httpx.JSON(w, http.StatusOK, map[string]any{"items": presets, "total": len(presets)})
}

func (s *Service) handleGateways(w http.ResponseWriter, r *http.Request) {
	items := s.Gateways()
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (s *Service) handleListModels(w http.ResponseWriter, r *http.Request) {
	offset, limit := httpx.Page(r)
	items, total, err := s.ListModels(r.Context(), offset, limit)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "offset": offset, "limit": limit})
}

type discoveryRequest struct {
	ProviderID string `json:"providerId"`
}

func (s *Service) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	var req discoveryRequest
	if !httpx.Decode(w, r, &req) {
		return
	}
	records, err := s.Discovery(r.Context(), req.ProviderID)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"providerId": strings.TrimSpace(req.ProviderID), "records": records, "total": len(records), "mock": true, "external": false})
}

type recordsRequest struct {
	Records []modelInput `json:"records"`
}

func (s *Service) handlePreview(w http.ResponseWriter, r *http.Request) {
	var req recordsRequest
	if !httpx.Decode(w, r, &req) {
		return
	}
	result, err := s.Preview(r.Context(), req.Records)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}

type importRequest struct {
	Records  []modelInput `json:"records"`
	Revision *int64       `json:"revision"`
}

func (s *Service) handleImport(w http.ResponseWriter, r *http.Request) {
	var req importRequest
	if !httpx.Decode(w, r, &req) {
		return
	}
	if len(req.Records) == 0 {
		fail(w, badRequest("EMPTY_RECORDS", "records 不能为空"))
		return
	}
	if req.Revision == nil {
		fail(w, badRequest("MISSING_REVISION", "必须提供预览基线"))
		return
	}
	result, err := s.Import(r.Context(), req.Records, *req.Revision, "import")
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}

type overrideRequest struct {
	ProviderID string          `json:"providerId"`
	ID         string          `json:"id"`
	Field      string          `json:"field"`
	Value      json.RawMessage `json:"value"`
	Revision   *int64          `json:"revision"`
}

func (s *Service) handleOverride(w http.ResponseWriter, r *http.Request) {
	var req overrideRequest
	if !httpx.Decode(w, r, &req) {
		return
	}
	if req.Revision == nil {
		fail(w, badRequest("MISSING_REVISION", "必须提供编辑基线"))
		return
	}
	value, err := decodeJSONValue(req.Value)
	if err != nil {
		fail(w, err)
		return
	}
	result, err := s.Override(r.Context(), req.ProviderID, req.ID, req.Field, value, *req.Revision)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}

// decodeJSONValue keeps explicit null distinct from an absent field so that a
// manual UNKNOWN override is possible and cannot be confused with "no change".
func decodeJSONValue(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, badRequest("MISSING_VALUE", "value 必须显式提供，null 表示手动 UNKNOWN")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, badRequest("INVALID_VALUE", "value 只能是数字、布尔值或 null")
	}
	switch value.(type) {
	case nil, bool, float64:
		return value, nil
	default:
		return nil, badRequest("INVALID_VALUE", "value 只能是数字、布尔值或 null")
	}
}

type modelsDevRequest struct {
	Catalog  json.RawMessage `json:"catalog"`
	Revision *int64          `json:"revision"`
	Preview  bool            `json:"preview"`
}

func (s *Service) handleModelsDev(w http.ResponseWriter, r *http.Request) {
	var req modelsDevRequest
	if !httpx.Decode(w, r, &req) {
		return
	}
	if len(req.Catalog) == 0 {
		fail(w, badRequest("MISSING_CATALOG", "catalog 不能为空"))
		return
	}
	catalog, err := s.decodeCatalog(req.Catalog)
	if err != nil {
		fail(w, err)
		return
	}
	if !req.Preview && req.Revision == nil {
		fail(w, badRequest("MISSING_REVISION", "必须提供预览基线"))
		return
	}
	var revision int64
	if req.Revision != nil {
		revision = *req.Revision
	}
	result, err := s.ImportModelsDev(r.Context(), catalog, revision, req.Preview)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}

func (s *Service) handleListChanges(w http.ResponseWriter, r *http.Request) {
	offset, limit := httpx.Page(r)
	items, total, err := s.ListChanges(r.Context(), offset, limit)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "offset": offset, "limit": limit})
}

func parseID(raw string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest("INVALID_ID", "变更 ID 必须是正整数")
	}
	return id, nil
}

func (s *Service) handleChangeDetail(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	detail, err := s.ChangeDetail(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, detail)
}

type rollbackRequest struct {
	Revision *int64 `json:"revision"`
}

func (s *Service) handleRollback(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	var req rollbackRequest
	if !httpx.Decode(w, r, &req) {
		return
	}
	if req.Revision == nil {
		fail(w, badRequest("MISSING_REVISION", "必须提供回滚预览基线"))
		return
	}
	result, err := s.Rollback(r.Context(), id, *req.Revision)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}
