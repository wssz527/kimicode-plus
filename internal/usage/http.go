package usage

import (
	"net/http"
	"strings"
	"time"

	"enhancedkimi/internal/httpx"
)

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /enhance/v1/usage/observations", s.handleObservations)
	mux.HandleFunc("GET /enhance/v1/usage", s.handleList)
	mux.HandleFunc("POST /enhance/v1/usage", s.handleIngest)
	mux.HandleFunc("GET /enhance/v1/usage/summary", s.handleSummary)
	mux.HandleFunc("POST /enhance/v1/usage/demo", s.handleDemo)
	mux.HandleFunc("POST /enhance/v1/usage/demo/clear", s.handleClearDemo)
	mux.HandleFunc("GET /enhance/v1/usage/unlinked", s.handleUnlinked)
	mux.HandleFunc("GET /enhance/v1/usage/provenance", s.handleProvenance)
	mux.HandleFunc("GET /enhance/v1/quotas", s.handleQuotas)
	mux.HandleFunc("POST /enhance/v1/quotas", s.handleRecordQuota)
	mux.HandleFunc("POST /enhance/v1/usage/actual", s.handleActualBilling)
}

func fail(w http.ResponseWriter, err error) {
	status, code, message := errorResponse(err)
	httpx.Fail(w, status, code, message)
}

func parseTimestamp(raw string) (*int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil, badRequest("INVALID_TIME_FILTER", "时间过滤必须使用 ISO8601/RFC3339，例如 2026-11-10T00:00:00Z")
	}
	ms := parsed.UnixMilli()
	return &ms, nil
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	from, err := parseTimestamp(query.Get("from"))
	if err != nil {
		fail(w, err)
		return
	}
	to, err := parseTimestamp(query.Get("to"))
	if err != nil {
		fail(w, err)
		return
	}
	if from != nil && to != nil && *from > *to {
		fail(w, badRequest("INVALID_RANGE", "from 不能晚于 to"))
		return
	}
	offset, limit := httpx.Page(r)
	filter := listFilter{
		ProjectID:   strings.TrimSpace(query.Get("projectId")),
		SessionID:   strings.TrimSpace(query.Get("sessionId")),
		AgentID:     strings.TrimSpace(query.Get("agentId")),
		Model:       strings.TrimSpace(query.Get("model")),
		ProviderID:  strings.TrimSpace(query.Get("providerId")),
		Role:        strings.TrimSpace(query.Get("role")),
		RequestKind: strings.TrimSpace(query.Get("requestKind")),
		FromMs:      from,
		ToMs:        to,
	}
	items, total, err := s.ListEvents(filter, offset, limit)
	if err != nil {
		fail(w, err)
		return
	}
	var ids []int64
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	provenance, err := s.ProvenanceFor(ids)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "offset": offset, "limit": limit,
		"provenance": provenance,
	})
}

type ingestRequest struct {
	Events []EventInput `json:"events"`
}

func (s *Service) handleIngest(w http.ResponseWriter, r *http.Request) {
	var req ingestRequest
	if !httpx.Decode(w, r, &req) {
		return
	}
	result, err := s.Ingest(req.Events)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}

func (s *Service) handleSummary(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	from, err := parseTimestamp(query.Get("from"))
	if err != nil {
		fail(w, err)
		return
	}
	to, err := parseTimestamp(query.Get("to"))
	if err != nil {
		fail(w, err)
		return
	}
	filter := listFilter{
		ProjectID:   strings.TrimSpace(query.Get("projectId")),
		SessionID:   strings.TrimSpace(query.Get("sessionId")),
		AgentID:     strings.TrimSpace(query.Get("agentId")),
		Model:       strings.TrimSpace(query.Get("model")),
		ProviderID:  strings.TrimSpace(query.Get("providerId")),
		Role:        strings.TrimSpace(query.Get("role")),
		RequestKind: strings.TrimSpace(query.Get("requestKind")),
		FromMs:      from,
		ToMs:        to,
	}
	summary, err := s.Summary(filter)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, summary)
}

func (s *Service) handleDemo(w http.ResponseWriter, r *http.Request) {
	result, err := s.Demo()
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}

func (s *Service) handleQuotas(w http.ResponseWriter, r *http.Request) {
	items, err := s.Quotas()
	if err != nil {
		fail(w, err)
		return
	}
	leaderboard := []map[string]any{
		{"ledger": "SUBSCRIPTION", "label": "官方订阅额度", "remaining": nil, "known": false, "note": "未查询官方 usage，未知"},
		{"ledger": "ACTUAL_TOKENS", "label": "实际上报 Token", "remaining": nil, "known": false, "note": "见 /usage/summary 的 coverage"},
		{"ledger": "ACTUAL_BILLING", "label": "实际计费/供应商余额", "remaining": nil, "known": false, "note": "未调用账单接口"},
		{"ledger": "ESTIMATE", "label": "估算费用", "remaining": nil, "known": false, "note": "见 /usage/summary 的 estimate"},
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items), "ledgers": leaderboard})
}

func (s *Service) handleRecordQuota(w http.ResponseWriter, r *http.Request) {
	var req QuotaInput
	if !httpx.Decode(w, r, &req) {
		return
	}
	snapshot, err := s.RecordQuota(req)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, snapshot)
}

func (s *Service) handleActualBilling(w http.ResponseWriter, r *http.Request) {
	var req ActualBillingInput
	if !httpx.Decode(w, r, &req) {
		return
	}
	observation, err := s.RecordActualBilling(req)
	if err != nil {
		fail(w, err)
		return
	}
	status := http.StatusCreated
	if observation.Duplicate {
		status = http.StatusOK
	}
	httpx.JSON(w, status, observation)
}

func (s *Service) handleUnlinked(w http.ResponseWriter, r *http.Request) {
	offset, limit := httpx.Page(r)
	items, total, err := s.ListUnlinked(offset, limit)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "offset": offset, "limit": limit})
}

func (s *Service) handleProvenance(w http.ResponseWriter, r *http.Request) {
	offset, limit := httpx.Page(r)
	items, total, err := s.ListEvents(listFilter{}, offset, limit)
	if err != nil {
		fail(w, err)
		return
	}
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	provenance, err := s.ProvenanceFor(ids)
	if err != nil {
		fail(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": provenance, "total": total})
}
