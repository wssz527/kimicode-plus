package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	ContractAvailable = "available"
	ContractDisabled  = "disabled"
	ContractUntested  = "untested"

	UnverifiedVersionNote = "该版本未经人工验证，契约检测通过"
)

// ContractProbe describes the check result of a single DOM injection point or
// official local API endpoint.
type ContractProbe struct {
	ID        string `json:"id"`
	Group     string `json:"group"` // "dom" | "api"
	Label     string `json:"label"`
	Status    string `json:"status"` // "available" | "disabled" | "untested"
	Reason    string `json:"reason,omitempty"`
	Detail    string `json:"detail,omitempty"`
	CheckedAt string `json:"checkedAt,omitempty"`
}

// ContractReport aggregates host version verification metadata with per-feature
// DOM and API contract probe statuses for diagnostics.
type ContractReport struct {
	AppVersion  string          `json:"appVersion,omitempty"`
	Electron    string          `json:"electron,omitempty"`
	Verified    bool            `json:"verified"`
	VersionNote string          `json:"versionNote,omitempty"`
	UpdatedAt   string          `json:"updatedAt,omitempty"`
	Probes      []ContractProbe `json:"probes"`
}

// IsVerifiedHost reports whether the app/Electron pair matches a version
// explicitly verified by human acceptance. Unverified versions are still
// allowed whenever security gates and feature probes pass.
func IsVerifiedHost(appVersion, electron string) bool {
	for _, host := range SupportedHosts {
		if host.AppVersion == appVersion && (host.Electron == "" || electron == "" || host.Electron == electron) {
			return true
		}
	}
	return false
}

// HostVersionNote returns the diagnostic banner for an unrecognized host version.
func HostVersionNote(appVersion, electron string) string {
	if strings.TrimSpace(appVersion) == "" {
		return ""
	}
	if IsVerifiedHost(appVersion, electron) {
		return ""
	}
	return UnverifiedVersionNote
}

func defaultDOMProbes() []ContractProbe {
	return []ContractProbe{
		{ID: "dom.session_info_available", Group: "dom", Label: "会话信息「可打开」分组", Status: ContractUntested, Reason: "等待渲染页面上报"},
		{ID: "dom.right_panel", Group: "dom", Label: "右侧面板空白列表与 + 菜单", Status: ContractUntested, Reason: "等待渲染页面上报"},
		{ID: "dom.sidebar_nav", Group: "dom", Label: "左侧栏「新建会话」行", Status: ContractUntested, Reason: "等待渲染页面上报"},
		{ID: "dom.settings_dialog", Group: "dom", Label: "设置弹层与标签栏", Status: ContractUntested, Reason: "等待渲染页面上报"},
		{ID: "dom.settings_account", Group: "dom", Label: "官方「账户」设置页内容区", Status: ContractUntested, Reason: "打开官方设置后自动检测"},
		{ID: "dom.settings_agent", Group: "dom", Label: "官方「智能体与会话」设置页内容区", Status: ContractUntested, Reason: "打开官方设置后自动检测"},
		{ID: "dom.settings_providers", Group: "dom", Label: "官方「模型供应商」设置页与模型行", Status: ContractUntested, Reason: "打开官方设置后自动检测"},
		{ID: "dom.settings_lab", Group: "dom", Label: "官方「实验室」设置页内容区", Status: ContractUntested, Reason: "打开官方设置后自动检测"},
		{ID: "dom.sidebar_footer", Group: "dom", Label: "会话侧栏底部（Monitor 挂载位）", Status: ContractUntested, Reason: "等待渲染页面上报"},
		{ID: "dom.account_menu", Group: "dom", Label: "官方账号菜单", Status: ContractUntested, Reason: "等待渲染页面上报"},
	}
}

func normalizeProbeStatus(status string) string {
	switch status {
	case ContractAvailable, ContractDisabled, ContractUntested:
		return status
	default:
		return ContractUntested
	}
}

func (s *NativeService) reportContracts(raw json.RawMessage) (ContractReport, error) {
	var input struct {
		AppVersion string          `json:"appVersion"`
		Electron   string          `json:"electron"`
		Probes     []ContractProbe `json:"probes"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return ContractReport{}, errors.New("契约上报格式无效")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	byID := map[string]ContractProbe{}
	for _, probe := range input.Probes {
		id := strings.TrimSpace(probe.ID)
		if id == "" {
			continue
		}
		probe.ID = id
		if probe.Group == "" {
			probe.Group = "dom"
		}
		probe.Status = normalizeProbeStatus(probe.Status)
		if probe.CheckedAt == "" {
			probe.CheckedAt = now
		}
		byID[id] = probe
	}

	s.mu.Lock()
	if strings.TrimSpace(input.AppVersion) != "" {
		s.contractAppVersion = strings.TrimSpace(input.AppVersion)
	}
	if strings.TrimSpace(input.Electron) != "" {
		s.contractElectron = strings.TrimSpace(input.Electron)
	}
	if s.domProbes == nil {
		s.domProbes = map[string]ContractProbe{}
	}
	for id, probe := range byID {
		s.domProbes[id] = probe
	}
	s.contractUpdatedAt = now
	s.mu.Unlock()

	return s.ContractStatus(context.Background()), nil
}

// ContractStatus returns the combined DOM + API contract detection status.
func (s *NativeService) ContractStatus(ctx context.Context) ContractReport {
	s.mu.Lock()
	appVersion := s.contractAppVersion
	electron := s.contractElectron
	updatedAt := s.contractUpdatedAt
	recorded := make(map[string]ContractProbe, len(s.domProbes))
	for k, v := range s.domProbes {
		recorded[k] = v
	}
	s.mu.Unlock()

	if appVersion == "" {
		if last := ReadAttachState(s.attachConfig().DataDir); last != nil {
			if v, ok := last["appVersion"].(string); ok {
				appVersion = strings.TrimSpace(v)
			}
			if e, ok := last["electron"].(string); ok {
				electron = strings.TrimSpace(e)
			}
		}
	}

	probes := defaultDOMProbes()
	seen := map[string]bool{}
	for i, base := range probes {
		seen[base.ID] = true
		if live, ok := recorded[base.ID]; ok {
			if live.Label == "" {
				live.Label = base.Label
			}
			live.Group = "dom"
			probes[i] = live
		}
	}
	extra := []ContractProbe{}
	for id, live := range recorded {
		if !seen[id] {
			extra = append(extra, live)
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i].ID < extra[j].ID })
	probes = append(probes, extra...)

	apiProbes := s.probeOfficialAPI(ctx)
	probes = append(probes, apiProbes...)

	if updatedAt == "" {
		updatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	verified := IsVerifiedHost(appVersion, electron)
	return ContractReport{
		AppVersion:  appVersion,
		Electron:    electron,
		Verified:    verified,
		VersionNote: HostVersionNote(appVersion, electron),
		UpdatedAt:   updatedAt,
		Probes:      probes,
	}
}

func (s *NativeService) probeOfficialAPI(ctx context.Context) []ContractProbe {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if s.kap == nil {
		return []ContractProbe{
			{ID: "api.config", Group: "api", Label: "官方接口 /config", Status: ContractUntested, Reason: "尚未连接官方本地服务", CheckedAt: now},
			{ID: "api.providers", Group: "api", Label: "官方接口 /providers", Status: ContractUntested, Reason: "尚未连接官方本地服务", CheckedAt: now},
			{ID: "api.models", Group: "api", Label: "官方接口 /models", Status: ContractUntested, Reason: "尚未连接官方本地服务", CheckedAt: now},
			{ID: "api.oauth_usage", Group: "api", Label: "官方接口 /oauth/usage", Status: ContractUntested, Reason: "尚未连接官方本地服务", CheckedAt: now},
			{ID: "api.sessions", Group: "api", Label: "官方接口 /sessions", Status: ContractUntested, Reason: "尚未连接官方本地服务", CheckedAt: now},
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	return []ContractProbe{
		probeAPIConfig(probeCtx, s.kap, now),
		probeAPIProviders(probeCtx, s.kap, now),
		probeAPIModels(probeCtx, s.kap, now),
		probeAPIOAuthUsage(probeCtx, s.kap, now),
		probeAPISessions(probeCtx, s.kap, now),
	}
}

func probeAPIConfig(ctx context.Context, kap *KAPClient, now string) ContractProbe {
	probe := ContractProbe{ID: "api.config", Group: "api", Label: "官方接口 /config", CheckedAt: now}
	raw, err := kap.Request(ctx, http.MethodGet, "/config", nil)
	if err != nil {
		return disableAPIProbe(probe, err)
	}
	if err := validateConfigPayload(raw); err != nil {
		probe.Status = ContractDisabled
		probe.Reason = err.Error()
		return probe
	}
	probe.Status = ContractAvailable
	return probe
}

func probeAPIProviders(ctx context.Context, kap *KAPClient, now string) ContractProbe {
	probe := ContractProbe{ID: "api.providers", Group: "api", Label: "官方接口 /providers", CheckedAt: now}
	raw, err := kap.Request(ctx, http.MethodGet, "/providers", nil)
	if err != nil {
		return disableAPIProbe(probe, err)
	}
	if err := validateProvidersPayload(raw); err != nil {
		probe.Status = ContractDisabled
		probe.Reason = err.Error()
		return probe
	}
	probe.Status = ContractAvailable
	return probe
}

func probeAPIModels(ctx context.Context, kap *KAPClient, now string) ContractProbe {
	probe := ContractProbe{ID: "api.models", Group: "api", Label: "官方接口 /models", CheckedAt: now}
	raw, err := kap.Request(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return disableAPIProbe(probe, err)
	}
	if err := validateModelsPayload(raw); err != nil {
		probe.Status = ContractDisabled
		probe.Reason = err.Error()
		return probe
	}
	probe.Status = ContractAvailable
	return probe
}

func probeAPIOAuthUsage(ctx context.Context, kap *KAPClient, now string) ContractProbe {
	probe := ContractProbe{ID: "api.oauth_usage", Group: "api", Label: "官方接口 /oauth/usage", CheckedAt: now}
	raw, err := kap.Request(ctx, http.MethodGet, "/oauth/usage", nil)
	if err != nil {
		var nativeErr *NativeError
		if errors.As(err, &nativeErr) && (nativeErr.Status == http.StatusUnauthorized || nativeErr.Status == http.StatusForbidden) {
			probe.Status = ContractUntested
			probe.Reason = "当前未登录官方账号，暂未检测用量字段"
			return probe
		}
		return disableAPIProbe(probe, err)
	}
	if err := validateOAuthUsagePayload(raw); err != nil {
		probe.Status = ContractDisabled
		probe.Reason = err.Error()
		return probe
	}
	probe.Status = ContractAvailable
	return probe
}

func probeAPISessions(ctx context.Context, kap *KAPClient, now string) ContractProbe {
	probe := ContractProbe{ID: "api.sessions", Group: "api", Label: "官方接口 /sessions", CheckedAt: now}
	raw, err := kap.Request(ctx, http.MethodGet, "/sessions", nil)
	if err != nil {
		return disableAPIProbe(probe, err)
	}
	if err := validateSessionsPayload(raw); err != nil {
		probe.Status = ContractDisabled
		probe.Reason = err.Error()
		return probe
	}
	probe.Status = ContractAvailable
	return probe
}

func disableAPIProbe(probe ContractProbe, err error) ContractProbe {
	probe.Status = ContractDisabled
	var nativeErr *NativeError
	if errors.As(err, &nativeErr) {
		if nativeErr.Status == http.StatusNotFound {
			probe.Reason = "官方接口返回 HTTP 404（路径可能已变更）"
			return probe
		}
		if nativeErr.Status > 0 {
			probe.Reason = fmt.Sprintf("官方接口返回 HTTP %d", nativeErr.Status)
			return probe
		}
	}
	probe.Reason = "无法访问官方本地服务：" + err.Error()
	return probe
}

func validateConfigPayload(raw json.RawMessage) error {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return errors.New("/config 响应不是 JSON 对象")
	}
	for _, key := range []string{"providers", "models", "default_model", "defaultModel"} {
		if _, exists := obj[key]; exists {
			return nil
		}
	}
	return errors.New("/config 响应缺少已知配置字段")
}

func validateProvidersPayload(raw json.RawMessage) error {
	items, err := extractItemsArray(raw)
	if err != nil {
		return errors.New("/providers 响应缺少 items 数组")
	}
	for _, entry := range items {
		obj, ok := entry.(map[string]any)
		if !ok {
			return errors.New("/providers 条目不是对象")
		}
		if strings.TrimSpace(stringFromAny(obj["id"])) == "" {
			return errors.New("/providers 条目缺少 id 字段")
		}
	}
	return nil
}

func validateModelsPayload(raw json.RawMessage) error {
	items, err := extractItemsArray(raw)
	if err != nil {
		return errors.New("/models 响应缺少 items 数组")
	}
	for _, entry := range items {
		obj, ok := entry.(map[string]any)
		if !ok {
			return errors.New("/models 条目不是对象")
		}
		if strings.TrimSpace(stringFromAny(obj["model"])) == "" && strings.TrimSpace(stringFromAny(obj["id"])) == "" {
			return errors.New("/models 条目缺少 model 字段")
		}
	}
	return nil
}

func validateOAuthUsagePayload(raw json.RawMessage) error {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return errors.New("/oauth/usage 响应不是 JSON 对象")
	}
	for _, key := range []string{"kind", "quota", "usages", "limits", "usage", "status", "windows", "user", "membership"} {
		if _, exists := obj[key]; exists {
			return nil
		}
	}
	return errors.New("/oauth/usage 响应缺少已知用量字段")
}

func validateSessionsPayload(raw json.RawMessage) error {
	items, err := extractItemsArray(raw)
	if err != nil {
		return errors.New("/sessions 响应缺少会话列表")
	}
	for _, entry := range items {
		obj, ok := entry.(map[string]any)
		if !ok {
			return errors.New("/sessions 条目不是对象")
		}
		if strings.TrimSpace(stringFromAny(obj["id"])) == "" && strings.TrimSpace(stringFromAny(obj["session_id"])) == "" {
			return errors.New("/sessions 条目缺少 id 字段")
		}
	}
	return nil
}

func extractItemsArray(raw json.RawMessage) ([]any, error) {
	var direct []any
	if err := json.Unmarshal(raw, &direct); err == nil && direct != nil {
		return direct, nil
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope == nil {
		return nil, errors.New("响应格式无效")
	}
	for _, key := range []string{"items", "sessions", "providers", "models"} {
		if rawItems, ok := envelope[key].([]any); ok {
			return rawItems, nil
		}
	}
	return nil, errors.New("缺少列表字段")
}

func stringFromAny(v any) string {
	s, _ := v.(string)
	return s
}
