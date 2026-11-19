package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Official capability tokens, in the order the official model table lists them.
var capabilityOrder = []string{"image_in", "video_in", "tool_use", "thinking", "always_thinking"}

// ModelEdit carries the model fields the official provider form does not expose.
// MaxOutputSize and AdaptiveThinking are pointers because the official endpoint
// distinguishes an omitted field from an explicit value, and the inline drawer
// must not overwrite a stored adaptive_thinking flag when saving other edits.
type ModelEdit struct {
	Capabilities     []string
	Thinking         string
	AdaptiveThinking *bool
	MaxContextSize   int64
	MaxOutputSize    *int64
	SupportEfforts   []string
}

var redactedProviderKeys = map[string]bool{
	"api_key":     true,
	"apiKey":      true,
	"api_key_env": true,
	"apiKeyEnv":   true,
	"has_api_key": true,
	"hasApiKey":   true,
	"oauth":       true,
}

func decodeJSONValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneBoolPtr(v *bool) *bool {
	if v == nil {
		return nil
	}
	copyValue := *v
	return &copyValue
}

func cloneInt64Ptr(v *int64) *int64 {
	if v == nil {
		return nil
	}
	copyValue := *v
	return &copyValue
}

func boolPtr(v bool) *bool {
	return &v
}

// Optional fields carry omitempty: the official config leaves a field out when
// it has no value, so a readback that includes it would be indistinguishable
// from a stored one.
type modelConfig struct {
	Provider         string            `json:"provider"`
	Model            string            `json:"model"`
	MaxContextSize   int64             `json:"maxContextSize"`
	MaxOutputSize    *int64            `json:"maxOutputSize,omitempty"`
	Capabilities     []string          `json:"capabilities,omitempty"`
	DisplayName      string            `json:"displayName,omitempty"`
	AdaptiveThinking *bool             `json:"adaptiveThinking,omitempty"`
	SupportEfforts   []string          `json:"supportEfforts,omitempty"`
	DefaultEffort    string            `json:"defaultEffort,omitempty"`
	OffEffort        string            `json:"offEffort,omitempty"`
	CustomHeaders    map[string]string `json:"customHeaders,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	Extra            map[string]any    `json:"-"`
}

var knownModelKeys = map[string]bool{
	"provider":          true,
	"model":             true,
	"maxContextSize":    true,
	"max_context_size":  true,
	"maxOutputSize":     true,
	"max_output_size":   true,
	"capabilities":      true,
	"displayName":       true,
	"display_name":      true,
	"adaptiveThinking":  true,
	"adaptive_thinking": true,
	"supportEfforts":    true,
	"support_efforts":   true,
	"defaultEffort":     true,
	"default_effort":    true,
	"offEffort":         true,
	"off_effort":        true,
	"customHeaders":     true,
	"custom_headers":    true,
	"env":               true,
}

func (m *modelConfig) UnmarshalJSON(data []byte) error {
	var raw struct {
		Provider              string            `json:"provider"`
		Model                 string            `json:"model"`
		MaxContextSizeCamel   int64             `json:"maxContextSize"`
		MaxContextSizeSnake   int64             `json:"max_context_size"`
		MaxOutputSizeCamel    *int64            `json:"maxOutputSize"`
		MaxOutputSizeSnake    *int64            `json:"max_output_size"`
		Capabilities          []string          `json:"capabilities"`
		DisplayNameCamel      string            `json:"displayName"`
		DisplayNameSnake      string            `json:"display_name"`
		AdaptiveThinkingCamel *bool             `json:"adaptiveThinking"`
		AdaptiveThinkingSnake *bool             `json:"adaptive_thinking"`
		SupportEffortsCamel   []string          `json:"supportEfforts"`
		SupportEffortsSnake   []string          `json:"support_efforts"`
		DefaultEffortCamel    string            `json:"defaultEffort"`
		DefaultEffortSnake    string            `json:"default_effort"`
		OffEffortCamel        string            `json:"offEffort"`
		OffEffortSnake        string            `json:"off_effort"`
		CustomHeadersCamel    map[string]string `json:"customHeaders"`
		CustomHeadersSnake    map[string]string `json:"custom_headers"`
		Env                   map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	rawFields := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &rawFields); err != nil {
		return err
	}
	*m = modelConfig{
		Provider:     raw.Provider,
		Model:        raw.Model,
		Capabilities: raw.Capabilities,
	}
	if raw.MaxContextSizeCamel != 0 {
		m.MaxContextSize = raw.MaxContextSizeCamel
	} else {
		m.MaxContextSize = raw.MaxContextSizeSnake
	}
	if raw.MaxOutputSizeCamel != nil {
		m.MaxOutputSize = cloneInt64Ptr(raw.MaxOutputSizeCamel)
	} else {
		m.MaxOutputSize = cloneInt64Ptr(raw.MaxOutputSizeSnake)
	}
	if raw.DisplayNameCamel != "" {
		m.DisplayName = raw.DisplayNameCamel
	} else {
		m.DisplayName = raw.DisplayNameSnake
	}
	if raw.AdaptiveThinkingCamel != nil {
		m.AdaptiveThinking = cloneBoolPtr(raw.AdaptiveThinkingCamel)
	} else if raw.AdaptiveThinkingSnake != nil {
		m.AdaptiveThinking = cloneBoolPtr(raw.AdaptiveThinkingSnake)
	}
	if len(raw.SupportEffortsCamel) > 0 {
		m.SupportEfforts = append([]string(nil), raw.SupportEffortsCamel...)
	} else if len(raw.SupportEffortsSnake) > 0 {
		m.SupportEfforts = append([]string(nil), raw.SupportEffortsSnake...)
	}
	if raw.DefaultEffortCamel != "" {
		m.DefaultEffort = raw.DefaultEffortCamel
	} else {
		m.DefaultEffort = raw.DefaultEffortSnake
	}
	if raw.OffEffortCamel != "" {
		m.OffEffort = raw.OffEffortCamel
	} else {
		m.OffEffort = raw.OffEffortSnake
	}
	if len(raw.CustomHeadersSnake) > 0 {
		m.CustomHeaders = cloneStringMap(raw.CustomHeadersSnake)
	} else if len(raw.CustomHeadersCamel) > 0 {
		m.CustomHeaders = cloneStringMap(raw.CustomHeadersCamel)
	}
	if len(raw.Env) > 0 {
		m.Env = cloneStringMap(raw.Env)
	}
	for key, val := range rawFields {
		if knownModelKeys[key] || redactedProviderKeys[key] {
			continue
		}
		decoded, err := decodeJSONValue(val)
		if err != nil {
			continue
		}
		if m.Extra == nil {
			m.Extra = map[string]any{}
		}
		m.Extra[key] = decoded
	}
	return nil
}

func (m modelConfig) MarshalJSON() ([]byte, error) {
	out := map[string]any{}
	for key, val := range m.Extra {
		if knownModelKeys[key] || redactedProviderKeys[key] {
			continue
		}
		out[key] = val
	}
	out["provider"] = m.Provider
	out["model"] = m.Model
	out["maxContextSize"] = m.MaxContextSize
	if m.MaxOutputSize != nil {
		out["maxOutputSize"] = *m.MaxOutputSize
	}
	if len(m.Capabilities) > 0 {
		out["capabilities"] = m.Capabilities
	}
	if m.DisplayName != "" {
		out["displayName"] = m.DisplayName
	}
	if m.AdaptiveThinking != nil {
		out["adaptiveThinking"] = *m.AdaptiveThinking
	}
	if len(m.SupportEfforts) > 0 {
		out["supportEfforts"] = m.SupportEfforts
	}
	if m.DefaultEffort != "" {
		out["defaultEffort"] = m.DefaultEffort
	}
	if m.OffEffort != "" {
		out["offEffort"] = m.OffEffort
	}
	if len(m.CustomHeaders) > 0 {
		out["customHeaders"] = m.CustomHeaders
	}
	if len(m.Env) > 0 {
		out["env"] = m.Env
	}
	return json.Marshal(out)
}

type providerConfig struct {
	Type          string            `json:"type"`
	BaseURL       string            `json:"base_url,omitempty"`
	DefaultModel  string            `json:"default_model,omitempty"`
	CustomHeaders map[string]string `json:"custom_headers,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Extra         map[string]any    `json:"-"`
}

var knownProviderKeys = map[string]bool{
	"type":           true,
	"base_url":       true,
	"baseUrl":        true,
	"default_model":  true,
	"defaultModel":   true,
	"custom_headers": true,
	"customHeaders":  true,
	"env":            true,
}

func (p *providerConfig) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type               string            `json:"type"`
		BaseURLSnake       string            `json:"base_url"`
		BaseURLCamel       string            `json:"baseUrl"`
		DefaultModelSnake  string            `json:"default_model"`
		DefaultModelCamel  string            `json:"defaultModel"`
		CustomHeadersSnake map[string]string `json:"custom_headers"`
		CustomHeadersCamel map[string]string `json:"customHeaders"`
		Env                map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	rawFields := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &rawFields); err != nil {
		return err
	}
	*p = providerConfig{Type: raw.Type}
	if strings.TrimSpace(raw.BaseURLSnake) != "" {
		p.BaseURL = strings.TrimSpace(raw.BaseURLSnake)
	} else {
		p.BaseURL = strings.TrimSpace(raw.BaseURLCamel)
	}
	if strings.TrimSpace(raw.DefaultModelSnake) != "" {
		p.DefaultModel = strings.TrimSpace(raw.DefaultModelSnake)
	} else {
		p.DefaultModel = strings.TrimSpace(raw.DefaultModelCamel)
	}
	if len(raw.CustomHeadersSnake) > 0 {
		p.CustomHeaders = cloneStringMap(raw.CustomHeadersSnake)
	} else if len(raw.CustomHeadersCamel) > 0 {
		p.CustomHeaders = cloneStringMap(raw.CustomHeadersCamel)
	}
	if len(raw.Env) > 0 {
		p.Env = cloneStringMap(raw.Env)
	}
	for key, val := range rawFields {
		if knownProviderKeys[key] || redactedProviderKeys[key] {
			continue
		}
		decoded, err := decodeJSONValue(val)
		if err != nil {
			continue
		}
		if p.Extra == nil {
			p.Extra = map[string]any{}
		}
		p.Extra[key] = decoded
	}
	return nil
}

func (p providerConfig) MarshalJSON() ([]byte, error) {
	out := map[string]any{}
	for key, val := range p.Extra {
		if knownProviderKeys[key] || redactedProviderKeys[key] {
			continue
		}
		out[key] = val
	}
	out["type"] = p.Type
	if p.BaseURL != "" {
		out["base_url"] = p.BaseURL
	}
	if p.DefaultModel != "" {
		out["default_model"] = p.DefaultModel
	}
	if len(p.CustomHeaders) > 0 {
		out["custom_headers"] = p.CustomHeaders
	}
	if len(p.Env) > 0 {
		out["env"] = p.Env
	}
	return json.Marshal(out)
}

type configSnapshot struct {
	Providers map[string]providerConfig `json:"providers"`
	Models    map[string]modelConfig    `json:"models"`
}

// providerModelKey is the single definition of a model key. The official catalog,
// the config map and secondary_model all use the same provider-qualified string,
// so every read and write goes through this.
func providerModelKey(providerID, model string) string {
	return providerID + "/" + model
}

func splitModelKey(key string) (string, string) {
	index := strings.Index(key, "/")
	if index <= 0 || index == len(key)-1 {
		return "", ""
	}
	return key[:index], key[index+1:]
}

func isManagedProvider(id string) bool { return strings.HasPrefix(id, "managed:") }

// mergeModel applies an edit onto a model read from the official config. Fields
// the dialog owns are always written, because the official endpoint clears any
// optional field that a PUT omits rather than preserving it.
func mergeModel(base modelConfig, edit ModelEdit) modelConfig {
	next := base
	next.CustomHeaders = cloneStringMap(base.CustomHeaders)
	next.Env = cloneStringMap(base.Env)
	next.Extra = cloneAnyMap(base.Extra)

	knownCap := make(map[string]bool, len(capabilityOrder))
	for _, allowed := range capabilityOrder {
		knownCap[allowed] = true
	}
	keep := map[string]bool{}
	for _, capability := range edit.Capabilities {
		if knownCap[capability] {
			keep[capability] = true
		}
	}
	switch edit.Thinking {
	case "always":
		keep["always_thinking"] = true
		delete(keep, "thinking")
	case "toggle":
		keep["thinking"] = true
		delete(keep, "always_thinking")
	default:
		delete(keep, "thinking")
		delete(keep, "always_thinking")
	}
	capabilities := []string{}
	for _, capability := range capabilityOrder {
		if keep[capability] {
			capabilities = append(capabilities, capability)
		}
	}
	seenExtraCap := map[string]bool{}
	for _, capability := range base.Capabilities {
		if knownCap[capability] || capability == "" || seenExtraCap[capability] {
			continue
		}
		seenExtraCap[capability] = true
		capabilities = append(capabilities, capability)
	}
	next.Capabilities = capabilities

	switch edit.Thinking {
	case "toggle":
		if edit.AdaptiveThinking != nil {
			next.AdaptiveThinking = cloneBoolPtr(edit.AdaptiveThinking)
		} else {
			next.AdaptiveThinking = cloneBoolPtr(base.AdaptiveThinking)
		}
	case "always":
		next.AdaptiveThinking = nil
	default:
		if edit.AdaptiveThinking != nil && !*edit.AdaptiveThinking {
			next.AdaptiveThinking = cloneBoolPtr(edit.AdaptiveThinking)
		} else if base.AdaptiveThinking != nil && !*base.AdaptiveThinking {
			next.AdaptiveThinking = cloneBoolPtr(base.AdaptiveThinking)
		} else {
			next.AdaptiveThinking = nil
		}
	}

	next.MaxContextSize = edit.MaxContextSize
	next.MaxOutputSize = cloneInt64Ptr(edit.MaxOutputSize)

	if edit.Thinking == "none" || (edit.Thinking != "toggle" && edit.Thinking != "always") {
		next.SupportEfforts = nil
	} else {
		efforts := []string{}
		seen := map[string]bool{}
		for _, effort := range edit.SupportEfforts {
			trimmed := strings.TrimSpace(effort)
			if trimmed == "" || strings.EqualFold(trimmed, "no") || seen[trimmed] {
				continue
			}
			seen[trimmed] = true
			efforts = append(efforts, trimmed)
		}
		next.SupportEfforts = efforts
	}
	return next
}

// modelBody renders one model for the official PUT. An omitted display_name,
// max_output_size, or adaptive_thinking clears that field on the official server;
// preserved non-dialog fields and unknown fields are carried through intact.
func modelBody(model modelConfig) map[string]any {
	item := map[string]any{}
	for key, val := range model.Extra {
		if knownModelKeys[key] || redactedProviderKeys[key] {
			continue
		}
		item[key] = val
	}
	item["model"] = model.Model
	item["max_context_size"] = model.MaxContextSize
	if model.DisplayName != "" {
		item["display_name"] = model.DisplayName
	}
	if len(model.Capabilities) > 0 {
		item["capabilities"] = model.Capabilities
	}
	if model.MaxOutputSize != nil {
		item["max_output_size"] = *model.MaxOutputSize
	}
	if len(model.SupportEfforts) > 0 {
		item["support_efforts"] = model.SupportEfforts
	}
	if model.AdaptiveThinking != nil {
		item["adaptive_thinking"] = *model.AdaptiveThinking
	}
	if model.DefaultEffort != "" {
		item["default_effort"] = model.DefaultEffort
	}
	if model.OffEffort != "" {
		item["off_effort"] = model.OffEffort
	}
	if len(model.CustomHeaders) > 0 {
		item["custom_headers"] = cloneStringMap(model.CustomHeaders)
	}
	if len(model.Env) > 0 {
		item["env"] = cloneStringMap(model.Env)
	}
	return item
}

func providerBody(providerID string, provider providerConfig, models []any, includedModels map[string]bool) map[string]any {
	body := map[string]any{}
	for key, val := range provider.Extra {
		if knownProviderKeys[key] || redactedProviderKeys[key] {
			continue
		}
		body[key] = val
	}
	body["type"] = provider.Type
	body["models"] = models
	if provider.BaseURL != "" {
		body["base_url"] = provider.BaseURL
	}
	if provider.DefaultModel != "" {
		defaultModelID := strings.TrimSpace(provider.DefaultModel)
		if owner, modelID := splitModelKey(defaultModelID); owner == providerID && modelID != "" {
			defaultModelID = modelID
		}
		if defaultModelID != "" && includedModels[defaultModelID] {
			body["default_model"] = defaultModelID
		}
	}
	if len(provider.CustomHeaders) > 0 {
		body["custom_headers"] = cloneStringMap(provider.CustomHeaders)
	}
	if len(provider.Env) > 0 {
		body["env"] = cloneStringMap(provider.Env)
	}
	return body
}

func (s *NativeService) readConfig(ctx context.Context) (configSnapshot, error) {
	raw, err := s.kap.Request(ctx, http.MethodGet, "/config", nil)
	if err != nil {
		return configSnapshot{}, err
	}
	snapshot, err := decodeNative[configSnapshot](raw, "模型配置")
	if err != nil {
		return configSnapshot{}, err
	}
	if snapshot.Models == nil {
		snapshot.Models = map[string]modelConfig{}
	}
	if snapshot.Providers == nil {
		snapshot.Providers = map[string]providerConfig{}
	}
	return snapshot, nil
}

// updateProviderModel writes one model of one provider. The body carries every
// model of that provider because the official endpoint replaces the array as a
// whole; models of other providers are not included and stay untouched.
// api_key is never part of the body, and the official endpoint keeps the stored
// key when the field is absent.
func (s *NativeService) updateProviderModel(ctx context.Context, input map[string]any) (any, error) {
	providerID := strings.TrimSpace(stringInput(input, "providerId"))
	key := strings.TrimSpace(stringInput(input, "model"))
	if providerID == "" || key == "" {
		return nil, errors.New("缺少供应商或模型")
	}
	if isManagedProvider(providerID) || isManagedProvider(strings.SplitN(key, "/", 2)[0]) {
		return nil, errors.New("Kimi 订阅的内置供应商是只读的，不能修改模型能力")
	}
	fromKey, targetModelID := splitModelKey(key)
	if fromKey == "" {
		targetModelID = key
	} else if fromKey != providerID {
		return nil, fmt.Errorf("模型键 %s 不属于供应商 %s", key, providerID)
	}
	canonicalKey := providerModelKey(providerID, targetModelID)
	edit, err := decodeModelEdit(input["edit"])
	if err != nil {
		return nil, err
	}
	if edit.MaxContextSize < 1 {
		return nil, errors.New("上下文长度需要正整数")
	}
	if edit.MaxOutputSize != nil && *edit.MaxOutputSize < 1 {
		return nil, errors.New("最大输出长度需要正整数，或留空")
	}

	before, err := s.readConfig(ctx)
	if err != nil {
		return nil, err
	}
	base, matchedKey, ok := lookupProviderModel(before.Models, providerID, targetModelID)
	if !ok {
		return nil, fmt.Errorf("模型 %s 不在官方配置里，先保存供应商", canonicalKey)
	}
	provider, ok := before.Providers[providerID]
	if !ok {
		return nil, fmt.Errorf("供应商 %s 不在官方配置里", providerID)
	}
	edited := mergeModel(base, edit)

	chosenByModelID := make(map[string]string, len(before.Models))
	for candidate, item := range before.Models {
		owner, modelID := resolveProviderAndModelID(candidate, item)
		if owner != providerID || modelID == "" {
			continue
		}
		qualifiedCandidate := providerModelKey(providerID, modelID)
		prev, exists := chosenByModelID[modelID]
		if !exists || candidate == qualifiedCandidate || (prev != qualifiedCandidate && candidate < prev) {
			chosenByModelID[modelID] = candidate
		}
	}
	keys := make([]string, 0, len(chosenByModelID))
	for _, candidate := range chosenByModelID {
		keys = append(keys, candidate)
	}
	sort.Strings(keys)
	models := make([]any, 0, len(keys))
	includedModels := make(map[string]bool, len(keys))
	for _, candidate := range keys {
		model := before.Models[candidate]
		_, modelID := resolveProviderAndModelID(candidate, model)
		if candidate == matchedKey || modelID == targetModelID {
			model = edited
		}
		if modelID != "" {
			model.Model = modelID
			includedModels[modelID] = true
		}
		models = append(models, modelBody(model))
	}
	body := providerBody(providerID, provider, models, includedModels)
	if _, err = s.kap.Request(ctx, http.MethodPut, "/providers/"+urlPathValue(providerID), body); err != nil {
		return nil, err
	}

	after, err := s.readConfig(ctx)
	if err != nil {
		return nil, err
	}
	actual, _, ok := lookupProviderModel(after.Models, providerID, targetModelID)
	if !ok {
		return nil, errors.New("保存后读不到这个模型，官方可能没有接受这次修改")
	}
	if err = compareModel(edited, actual); err != nil {
		return nil, err
	}
	return map[string]any{"model": actual, "provider": providerID, "key": canonicalKey}, nil
}

func resolveProviderAndModelID(mapKey string, item modelConfig) (string, string) {
	if owner, modelID := splitModelKey(mapKey); owner != "" && modelID != "" {
		return owner, modelID
	}
	owner := strings.TrimSpace(item.Provider)
	modelID := strings.TrimSpace(item.Model)
	if modelID == "" {
		modelID = strings.TrimSpace(mapKey)
	}
	return owner, modelID
}

func lookupProviderModel(models map[string]modelConfig, providerID, targetModelID string) (modelConfig, string, bool) {
	qualified := providerModelKey(providerID, targetModelID)
	if model, ok := models[qualified]; ok {
		return model, qualified, true
	}
	if model, ok := models[targetModelID]; ok {
		owner, modelID := resolveProviderAndModelID(targetModelID, model)
		if owner == providerID && modelID == targetModelID {
			return model, targetModelID, true
		}
	}
	for key, model := range models {
		owner, modelID := resolveProviderAndModelID(key, model)
		if owner == providerID && modelID == targetModelID {
			return model, key, true
		}
	}
	return modelConfig{}, "", false
}

// urlPathValue escapes a provider id for the official path segment. Ids allow
// letters, digits, dashes, underscores and spaces, so only space needs escaping;
// managed ids contain a colon but never reach this function.
func urlPathValue(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "%", "%25"), " ", "%20")
}

func decodeModelEdit(raw any) (ModelEdit, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return ModelEdit{}, errors.New("缺少模型字段")
	}
	edit := ModelEdit{Thinking: "none"}
	if value, ok := object["thinking"].(string); ok {
		switch value {
		case "none", "toggle", "always":
			edit.Thinking = value
		default:
			return ModelEdit{}, errors.New("思考模式只能是 none、toggle 或 always")
		}
	}
	if rawAdaptive, present := object["adaptive_thinking"]; present && rawAdaptive != nil {
		if value, ok := rawAdaptive.(bool); ok {
			edit.AdaptiveThinking = boolPtr(value)
		}
	} else if rawAdaptive, present := object["adaptiveThinking"]; present && rawAdaptive != nil {
		if value, ok := rawAdaptive.(bool); ok {
			edit.AdaptiveThinking = boolPtr(value)
		}
	}
	for _, capability := range stringSlice(object["capabilities"]) {
		edit.Capabilities = append(edit.Capabilities, capability)
	}
	edit.SupportEfforts = stringSlice(object["support_efforts"])
	size, err := numberValue(object["max_context_size"])
	if err != nil {
		return ModelEdit{}, fmt.Errorf("上下文长度：%w", err)
	}
	edit.MaxContextSize = size
	if rawOutput, present := object["max_output_size"]; present && rawOutput != nil {
		value, err := numberValue(rawOutput)
		if err != nil {
			return ModelEdit{}, fmt.Errorf("最大输出长度：%w", err)
		}
		edit.MaxOutputSize = &value
	}
	return edit, nil
}

func stringSlice(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok {
			out = append(out, value)
		}
	}
	return out
}

func numberValue(raw any) (int64, error) {
	switch value := raw.(type) {
	case float64:
		if value != float64(int64(value)) {
			return 0, errors.New("需要整数")
		}
		return int64(value), nil
	case int64:
		return value, nil
	case int:
		return int64(value), nil
	case nil:
		return 0, errors.New("不能为空")
	default:
		return 0, errors.New("需要整数")
	}
}

// compareModel reports every field that the official readback did not store. A
// mismatch is an error, not a warning: the dialog must not claim "saved" for a
// write the server silently dropped.
func compareModel(expected, actual modelConfig) error {
	mismatched := []string{}
	if !sameStrings(expected.Capabilities, actual.Capabilities) {
		mismatched = append(mismatched, "capabilities")
	}
	if !sameOptionalBool(expected.AdaptiveThinking, actual.AdaptiveThinking) {
		mismatched = append(mismatched, "adaptive_thinking")
	}
	if expected.MaxContextSize != actual.MaxContextSize {
		mismatched = append(mismatched, "max_context_size")
	}
	if !sameOptionalInt(expected.MaxOutputSize, actual.MaxOutputSize) {
		mismatched = append(mismatched, "max_output_size")
	}
	if !sameStrings(expected.SupportEfforts, actual.SupportEfforts) {
		mismatched = append(mismatched, "support_efforts")
	}
	if len(mismatched) > 0 {
		return fmt.Errorf("官方读回的值和提交的不一致：%s", strings.Join(mismatched, "、"))
	}
	return nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameOptionalInt(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameOptionalBool(left, right *bool) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
