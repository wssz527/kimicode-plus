package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"

	"enhancedkimi/internal/registry"
)

// Official capability tokens, in the order the official model table lists them.
var capabilityOrder = []string{"image_in", "video_in", "tool_use", "thinking", "always_thinking"}

type ModelEdit struct {
	Capabilities     []string
	Thinking         string
	AdaptiveThinking *bool
	MaxContextSize   int64
	MaxOutputSize    *int64
	OutputChanged    bool
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

var credentialKeyNormalizer = strings.NewReplacer("_", "", "-", "")

func isConfigCredential(key string) bool {
	key = credentialKeyNormalizer.Replace(strings.ToLower(key))
	switch key {
	case "apikey", "apikeyenv", "oauth", "authorization", "proxyauthorization", "cookie", "setcookie",
		"token", "accesstoken", "refreshtoken", "idtoken", "authtoken", "bearertoken", "password",
		"secret", "clientsecret", "privatekey", "credentials", "env", "customheaders", "headers":
		return true
	}
	return false
}

func visibleConfig(raw json.RawMessage) (json.RawMessage, error) {
	if !json.Valid(raw) {
		return nil, errors.New("官方配置响应无效")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var result bytes.Buffer
	if err := writeVisibleConfigValue(&result, decoder, configRoot); err != nil {
		return nil, err
	}
	return json.RawMessage(result.Bytes()), nil
}

type configObjectKind uint8

const (
	configValue configObjectKind = iota
	configRoot
	configSecondaryModel
	configIdentityMap
)

// Read config objects in their original order: secondary_model uses that order as priority.
func writeVisibleConfigValue(out *bytes.Buffer, decoder *json.Decoder, kind configObjectKind) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token != json.Delim('{') && token != json.Delim('[') {
		encoded, err := json.Marshal(token)
		if err != nil {
			return err
		}
		out.Write(encoded)
		return nil
	}
	out.WriteByte(byte(token.(json.Delim)))
	comma := false
	for decoder.More() {
		var key string
		childKind := configValue
		if token == json.Delim('{') {
			field, err := decoder.Token()
			if err != nil {
				return err
			}
			key = field.(string)
			if kind != configIdentityMap && isConfigCredential(key) {
				var skipped json.RawMessage
				if err := decoder.Decode(&skipped); err != nil {
					return err
				}
				continue
			}
			switch {
			case kind == configRoot && key == "secondary_model":
				childKind = configSecondaryModel
			case kind == configRoot && key == "providers", (kind == configRoot || kind == configSecondaryModel) && key == "models":
				childKind = configIdentityMap
			}
		}
		if comma {
			out.WriteByte(',')
		}
		if token == json.Delim('{') {
			encoded, _ := json.Marshal(key)
			out.Write(encoded)
			out.WriteByte(':')
		}
		if err := writeVisibleConfigValue(out, decoder, childKind); err != nil {
			return err
		}
		comma = true
	}
	end, err := decoder.Token()
	if err != nil {
		return err
	}
	out.WriteByte(byte(end.(json.Delim)))
	return nil
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

func mergeModel(base modelConfig, edit ModelEdit) modelConfig {
	next := base
	if edit.Capabilities != nil || edit.Thinking != "" {
		known := make(map[string]bool, len(capabilityOrder))
		for _, capability := range capabilityOrder {
			known[capability] = true
		}
		selected := map[string]bool{}
		input := edit.Capabilities
		if input == nil {
			input = base.Capabilities
		}
		for _, capability := range input {
			if known[capability] && capability != "thinking" && capability != "always_thinking" {
				selected[capability] = true
			}
		}
		switch edit.Thinking {
		case "toggle":
			selected["thinking"] = true
		case "always":
			selected["always_thinking"] = true
		case "":
			for _, capability := range base.Capabilities {
				if capability == "thinking" || capability == "always_thinking" {
					selected[capability] = true
				}
			}
		}
		next.Capabilities = nil
		for _, capability := range capabilityOrder {
			if selected[capability] {
				next.Capabilities = append(next.Capabilities, capability)
			}
		}
		for _, capability := range base.Capabilities {
			if capability != "" && !known[capability] && !selected[capability] {
				selected[capability] = true
				next.Capabilities = append(next.Capabilities, capability)
			}
		}
	}

	switch edit.Thinking {
	case "toggle", "":
		if edit.AdaptiveThinking != nil {
			next.AdaptiveThinking = edit.AdaptiveThinking
		}
	case "always":
		next.AdaptiveThinking = nil
	case "none":
		if edit.AdaptiveThinking != nil && !*edit.AdaptiveThinking {
			next.AdaptiveThinking = edit.AdaptiveThinking
		} else if base.AdaptiveThinking == nil || *base.AdaptiveThinking {
			next.AdaptiveThinking = nil
		}
	}
	if edit.Thinking == "none" {
		next.SupportEfforts = nil
		next.DefaultEffort = ""
	} else if edit.SupportEfforts != nil {
		next.SupportEfforts = edit.SupportEfforts
		if !slices.Contains(edit.SupportEfforts, next.DefaultEffort) {
			next.DefaultEffort = ""
		}
	}
	if edit.MaxContextSize > 0 {
		next.MaxContextSize = edit.MaxContextSize
	}
	if edit.OutputChanged {
		next.MaxOutputSize = edit.MaxOutputSize
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

func (s *NativeService) providerModelsLockChan() chan struct{} {
	s.providerModelsSemOnce.Do(func() {
		s.providerModelsSem = make(chan struct{}, 1)
	})
	return s.providerModelsSem
}

func (s *NativeService) acquireProviderModelsLock(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sem := s.providerModelsLockChan()
	select {
	case sem <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-sem
			return nil, err
		}
		var once sync.Once
		return func() {
			once.Do(func() { <-sem })
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' && json.Valid(trimmed)
}

func decodeAuthoritativeConfig(raw json.RawMessage) (configSnapshot, error) {
	if !isJSONObject(raw) {
		return configSnapshot{}, errors.New("官方模型配置响应不完整：根对象无效，已中止写入")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil || root == nil {
		return configSnapshot{}, errors.New("官方模型配置响应不完整：无法解析根对象，已中止写入")
	}
	rawProviders, hasProviders := root["providers"]
	if !hasProviders || !isJSONObject(rawProviders) {
		return configSnapshot{}, errors.New("官方模型配置响应缺少有效的 providers 对象，已中止写入")
	}
	rawModels, hasModels := root["models"]
	if !hasModels || !isJSONObject(rawModels) {
		return configSnapshot{}, errors.New("官方模型配置响应缺少有效的 models 对象，已中止写入")
	}
	var providerEntries map[string]json.RawMessage
	if err := json.Unmarshal(rawProviders, &providerEntries); err != nil || providerEntries == nil {
		return configSnapshot{}, errors.New("官方模型配置响应的 providers 对象无效，已中止写入")
	}
	for id, entry := range providerEntries {
		if !isJSONObject(entry) {
			return configSnapshot{}, fmt.Errorf("官方模型配置响应的供应商 %s 条目无效，已中止写入", id)
		}
	}
	var modelEntries map[string]json.RawMessage
	if err := json.Unmarshal(rawModels, &modelEntries); err != nil || modelEntries == nil {
		return configSnapshot{}, errors.New("官方模型配置响应的 models 对象无效，已中止写入")
	}
	for id, entry := range modelEntries {
		if !isJSONObject(entry) {
			return configSnapshot{}, fmt.Errorf("官方模型配置响应的模型 %s 条目无效，已中止写入", id)
		}
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

func (s *NativeService) readAuthoritativeConfig(ctx context.Context) (configSnapshot, error) {
	raw, err := s.kap.Request(ctx, http.MethodGet, "/config", nil)
	if err != nil {
		return configSnapshot{}, err
	}
	return decodeAuthoritativeConfig(raw)
}

func (s *NativeService) readAuthoritativeConfigAtOrigin(ctx context.Context, expectedOrigin string) (configSnapshot, error) {
	raw, err := s.kap.RequestPinnedOrigin(ctx, expectedOrigin, http.MethodGet, "/config", nil)
	if err != nil {
		return configSnapshot{}, err
	}
	return decodeAuthoritativeConfig(raw)
}

type publicModelCatalog interface {
	FindPublicModel(ctx context.Context, providerID, baseURL, modelID string) (map[string]registry.Capability, error)
}

func (s *NativeService) UsePublicModelCatalog(catalog publicModelCatalog) {
	s.publicModelCatalog = catalog
}

func (s *NativeService) providerModelSuggestion(ctx context.Context, input map[string]any) (any, error) {
	providerID := stringInput(input, "providerId")
	owner, modelID := splitModelKey(stringInput(input, "model"))
	if providerID == "" || owner != providerID || isManagedProvider(providerID) {
		return nil, errors.New("请选择已保存的自定义供应商模型")
	}
	if s.publicModelCatalog == nil {
		return map[string]any{}, nil
	}
	config, err := s.readConfig(ctx)
	if err != nil {
		return nil, err
	}
	provider, ok := config.Providers[providerID]
	if !ok {
		return nil, errors.New("找不到已保存的供应商")
	}
	if _, _, ok := lookupProviderModel(config.Models, providerID, modelID); !ok {
		return nil, errors.New("找不到已保存的模型")
	}
	capabilities, err := s.publicModelCatalog.FindPublicModel(ctx, providerID, provider.BaseURL, modelID)
	if err != nil {
		return nil, err
	}
	suggestion := map[string]any{}
	for source, field := range map[string]string{"vision": "image_in", "video": "video_in", "tools": "tool_use", "thinking": "thinking", "output": "max_output_size"} {
		if capability, known := capabilities[source]; known {
			suggestion[field] = capability.Value
		}
	}
	if len(suggestion) == 0 {
		return map[string]any{}, nil
	}
	return map[string]any{"suggestion": suggestion}, nil
}

type providerModelUpdate struct {
	Key   string          `json:"key"`
	Model json.RawMessage `json:"model"`
}

func (s *NativeService) updateProviderModel(ctx context.Context, input map[string]any) (any, error) {
	providerID := stringInput(input, "providerId")
	updates, err := s.saveProviderModels(ctx, providerID, []any{input})
	if err != nil {
		return nil, err
	}
	return map[string]any{"model": updates[0].Model, "provider": providerID, "key": updates[0].Key}, nil
}

func (s *NativeService) updateProviderModels(ctx context.Context, input map[string]any) (any, error) {
	providerID := stringInput(input, "providerId")
	items, ok := input["models"].([]any)
	if !ok || len(items) == 0 {
		return nil, errors.New("请至少选择一个模型")
	}
	updates, err := s.saveProviderModels(ctx, providerID, items)
	if err != nil {
		return nil, err
	}
	return map[string]any{"provider": providerID, "models": updates}, nil
}

type wireCapabilitySnapshot struct {
	Capabilities     []string `json:"capabilities"`
	SupportEfforts   []string `json:"support_efforts"`
	AdaptiveThinking *bool    `json:"adaptive_thinking,omitempty"`
	DefaultEffort    string   `json:"default_effort"`
	OffEffort        string   `json:"off_effort"`
}

type officialFormModelOpening struct {
	Raw    wireCapabilitySnapshot
	Merged *wireCapabilitySnapshot
}

func cleanWireStringList(raw any) []string {
	out := []string{}
	switch items := raw.(type) {
	case []string:
		for _, value := range items {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	case []any:
		for _, item := range items {
			if value, ok := item.(string); ok {
				if trimmed := strings.TrimSpace(value); trimmed != "" {
					out = append(out, trimmed)
				}
			}
		}
	}
	return out
}

func uniqueWireEfforts(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !strings.EqualFold(value, "no") && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func extractWireCapabilitySnapshot(itemMap map[string]any) wireCapabilitySnapshot {
	if itemMap == nil {
		return wireCapabilitySnapshot{
			Capabilities:   []string{},
			SupportEfforts: []string{},
		}
	}
	rawEfforts := itemMap["support_efforts"]
	if rawEfforts == nil {
		rawEfforts = itemMap["supportEfforts"]
	}
	var adaptive *bool
	if val, ok := itemMap["adaptive_thinking"]; ok {
		if flag, isBool := val.(bool); isBool {
			adaptive = boolPtr(flag)
		}
	} else if val, ok := itemMap["adaptiveThinking"]; ok {
		if flag, isBool := val.(bool); isBool {
			adaptive = boolPtr(flag)
		}
	}
	rawDefault := itemMap["default_effort"]
	if rawDefault == nil {
		rawDefault = itemMap["defaultEffort"]
	}
	defaultEffort, _ := rawDefault.(string)
	rawOff := itemMap["off_effort"]
	if rawOff == nil {
		rawOff = itemMap["offEffort"]
	}
	offEffort, _ := rawOff.(string)
	return wireCapabilitySnapshot{
		Capabilities:     cleanWireStringList(itemMap["capabilities"]),
		SupportEfforts:   cleanWireStringList(rawEfforts),
		AdaptiveThinking: adaptive,
		DefaultEffort:    strings.TrimSpace(defaultEffort),
		OffEffort:        strings.TrimSpace(offEffort),
	}
}

func parseOfficialFormOpening(raw any) map[string]*officialFormModelOpening {
	root, ok := raw.(map[string]any)
	if !ok || len(root) == 0 {
		return nil
	}
	result := make(map[string]*officialFormModelOpening, len(root))
	for key, entryRaw := range root {
		modelID := strings.TrimSpace(key)
		entryMap, ok := entryRaw.(map[string]any)
		if modelID == "" || !ok || entryMap == nil {
			continue
		}
		rawMap, ok := entryMap["raw"].(map[string]any)
		if !ok || rawMap == nil {
			continue
		}
		entry := &officialFormModelOpening{
			Raw: extractWireCapabilitySnapshot(rawMap),
		}
		if mergedMap, ok := entryMap["merged"].(map[string]any); ok && mergedMap != nil {
			mergedSnap := extractWireCapabilitySnapshot(mergedMap)
			entry.Merged = &mergedSnap
		}
		result[modelID] = entry
	}
	return result
}

func wireAdaptiveBool(value *bool) bool {
	return value != nil && *value
}

func sameWireSnapshotField(field string, left, right wireCapabilitySnapshot) bool {
	switch field {
	case "capabilities":
		return sameStrings(left.Capabilities, right.Capabilities)
	case "support_efforts":
		return sameStrings(left.SupportEfforts, right.SupportEfforts) ||
			sameStrings(uniqueWireEfforts(left.SupportEfforts), uniqueWireEfforts(right.SupportEfforts))
	case "adaptive_thinking":
		return wireAdaptiveBool(left.AdaptiveThinking) == wireAdaptiveBool(right.AdaptiveThinking)
	case "default_effort":
		return left.DefaultEffort == right.DefaultEffort
	case "off_effort":
		return left.OffEffort == right.OffEffort
	default:
		return false
	}
}

func isUnchangedFromOpening(modelOpening *officialFormModelOpening, field string, rawSnap wireCapabilitySnapshot) bool {
	if modelOpening == nil {
		return true
	}
	if sameWireSnapshotField(field, rawSnap, modelOpening.Raw) {
		return true
	}
	if modelOpening.Merged != nil && sameWireSnapshotField(field, rawSnap, *modelOpening.Merged) {
		return true
	}
	return false
}

func hasAnyMapKey(m map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := m[key]; ok {
			return true
		}
	}
	return false
}

func sanitizeProviderPutResponse(raw json.RawMessage) any {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return nil
	}
	value, err := decodeJSONValue(trimmed)
	if err != nil {
		return nil
	}
	if obj, ok := value.(map[string]any); ok {
		delete(obj, "api_key")
		delete(obj, "apiKey")
		if providerObj, ok := obj["provider"].(map[string]any); ok {
			delete(providerObj, "api_key")
			delete(providerObj, "apiKey")
		}
		if providersMap, ok := obj["providers"].(map[string]any); ok {
			for _, entry := range providersMap {
				if providerObj, ok := entry.(map[string]any); ok {
					delete(providerObj, "api_key")
					delete(providerObj, "apiKey")
				}
			}
		}
	}
	return value
}

func (s *NativeService) saveOfficialProviderForm(ctx context.Context, input map[string]any) (any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	providerID := strings.TrimSpace(stringInput(input, "providerId"))
	if providerID == "" {
		return nil, errors.New("缺少供应商")
	}
	if isManagedProvider(providerID) {
		return nil, errors.New("Kimi 订阅的内置供应商是只读的，不能修改模型能力")
	}
	expectedOrigin, err := canonicalLoopbackOrigin(stringInput(input, "expectedOrigin"))
	if err != nil {
		return nil, err
	}
	rawPayload, ok := input["payload"].(map[string]any)
	if !ok || rawPayload == nil {
		return nil, errors.New("缺少供应商表单数据")
	}
	rawModels, ok := rawPayload["models"].([]any)
	if !ok {
		return nil, errors.New("供应商表单缺少模型列表")
	}
	opening := parseOfficialFormOpening(input["opening"])

	payload := maps.Clone(rawPayload)
	models := make([]any, len(rawModels))
	for i, elem := range rawModels {
		if itemMap, ok := elem.(map[string]any); ok && itemMap != nil {
			models[i] = maps.Clone(itemMap)
		} else {
			models[i] = elem
		}
	}
	payload["models"] = models

	unlock, err := s.acquireProviderModelsLock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()

	s.invalidateOfficialConfigCache()
	before, err := s.readAuthoritativeConfigAtOrigin(ctx, expectedOrigin)
	if err != nil {
		return nil, err
	}

	mergedModels := make(map[string]wireCapabilitySnapshot, len(models))
	for _, elem := range models {
		itemMap, ok := elem.(map[string]any)
		if !ok || itemMap == nil {
			continue
		}
		modelID := strings.TrimSpace(stringInput(itemMap, "model"))
		if modelID == "" {
			continue
		}
		rawSnap := extractWireCapabilitySnapshot(itemMap)
		if stored, _, hasStored := lookupProviderModel(before.Models, providerID, modelID); hasStored {
			if !hasAnyMapKey(itemMap, "max_output_size", "maxOutputSize") {
				rawContext, hasSnakeContext := itemMap["max_context_size"]
				if !hasSnakeContext || rawContext == nil {
					rawContext = itemMap["maxContextSize"]
				}
				if nextContext, err := numberValue(rawContext); err == nil && nextContext > 0 &&
					stored.MaxOutputSize != nil && *stored.MaxOutputSize > 0 && *stored.MaxOutputSize <= nextContext {
					itemMap["max_output_size"] = *stored.MaxOutputSize
				}
			}
			modelOpening := opening[modelID]
			if modelOpening == nil && opening != nil {
				modelOpening = opening[providerModelKey(providerID, modelID)]
			}
			if !hasAnyMapKey(itemMap, "capabilities") || isUnchangedFromOpening(modelOpening, "capabilities", rawSnap) {
				if len(stored.Capabilities) > 0 {
					itemMap["capabilities"] = slices.Clone(stored.Capabilities)
				} else {
					delete(itemMap, "capabilities")
				}
			}
			if !hasAnyMapKey(itemMap, "support_efforts", "supportEfforts") || isUnchangedFromOpening(modelOpening, "support_efforts", rawSnap) {
				delete(itemMap, "supportEfforts")
				if len(stored.SupportEfforts) > 0 {
					itemMap["support_efforts"] = slices.Clone(stored.SupportEfforts)
				} else {
					delete(itemMap, "support_efforts")
				}
			}
			if !hasAnyMapKey(itemMap, "adaptive_thinking", "adaptiveThinking") || isUnchangedFromOpening(modelOpening, "adaptive_thinking", rawSnap) {
				delete(itemMap, "adaptiveThinking")
				if stored.AdaptiveThinking != nil {
					itemMap["adaptive_thinking"] = *stored.AdaptiveThinking
				} else {
					delete(itemMap, "adaptive_thinking")
				}
			}
			if !hasAnyMapKey(itemMap, "default_effort", "defaultEffort") || isUnchangedFromOpening(modelOpening, "default_effort", rawSnap) {
				delete(itemMap, "defaultEffort")
				if trimmed := strings.TrimSpace(stored.DefaultEffort); trimmed != "" {
					itemMap["default_effort"] = trimmed
				} else {
					delete(itemMap, "default_effort")
				}
			}
			if !hasAnyMapKey(itemMap, "off_effort", "offEffort") || isUnchangedFromOpening(modelOpening, "off_effort", rawSnap) {
				delete(itemMap, "offEffort")
				if trimmed := strings.TrimSpace(stored.OffEffort); trimmed != "" {
					itemMap["off_effort"] = trimmed
				} else {
					delete(itemMap, "off_effort")
				}
			}
		}
		mergedModels[modelID] = extractWireCapabilitySnapshot(itemMap)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rawResp, err := s.kap.RequestPinnedOrigin(ctx, expectedOrigin, http.MethodPut, "/providers/"+urlPathValue(providerID), payload)
	if err != nil {
		return nil, err
	}
	s.invalidateOfficialConfigCache()

	return map[string]any{
		"data":         sanitizeProviderPutResponse(rawResp),
		"mergedModels": mergedModels,
	}, nil
}

// The official PUT replaces the provider's complete model array. Serialize the
// read/merge/write/read cycle so concurrent enhancement saves retain each other's edits.
func (s *NativeService) saveProviderModels(ctx context.Context, providerID string, items []any) ([]providerModelUpdate, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if providerID == "" {
		return nil, errors.New("缺少供应商")
	}
	if isManagedProvider(providerID) {
		return nil, errors.New("Kimi 订阅的内置供应商是只读的，不能修改模型能力")
	}
	modelIDs := make([]string, 0, len(items))
	edits := make(map[string]ModelEdit, len(items))
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		key := stringInput(item, "model")
		if key == "" {
			return nil, errors.New("缺少模型")
		}
		owner, modelID := splitModelKey(key)
		if owner == "" {
			modelID = key
		} else if owner != providerID {
			return nil, fmt.Errorf("模型键 %s 不属于供应商 %s", key, providerID)
		}
		if _, duplicate := edits[modelID]; duplicate {
			return nil, fmt.Errorf("模型 %s 重复", modelID)
		}
		edit, err := decodeModelEdit(item["edit"])
		if err != nil {
			return nil, fmt.Errorf("模型 %s：%w", modelID, err)
		}
		modelIDs = append(modelIDs, modelID)
		edits[modelID] = edit
	}

	unlock, err := s.acquireProviderModelsLock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	before, err := s.readAuthoritativeConfig(ctx)
	if err != nil {
		return nil, err
	}
	provider, ok := before.Providers[providerID]
	if !ok {
		return nil, fmt.Errorf("供应商 %s 不在官方配置里", providerID)
	}
	chosen := make(map[string]string)
	for key, item := range before.Models {
		owner, modelID := resolveProviderAndModelID(key, item)
		if owner != providerID || modelID == "" {
			continue
		}
		qualified := providerModelKey(providerID, modelID)
		prev, exists := chosen[modelID]
		if !exists || key == qualified || (prev != qualified && key < prev) {
			chosen[modelID] = key
		}
	}
	edited := make(map[string]modelConfig, len(edits))
	for _, modelID := range modelIDs {
		key, ok := chosen[modelID]
		if !ok {
			return nil, fmt.Errorf("模型 %s 不在官方配置里，先保存供应商", modelID)
		}
		edit := edits[modelID]
		next := mergeModel(before.Models[key], edit)
		if (edit.OutputChanged || edit.MaxContextSize > 0) && next.MaxOutputSize != nil && *next.MaxOutputSize > next.MaxContextSize {
			return nil, fmt.Errorf("模型 %s 的最大输出长度不能超过上下文长度", modelID)
		}
		edited[modelID] = next
	}

	orderedIDs := make([]string, 0, len(chosen))
	for modelID := range chosen {
		orderedIDs = append(orderedIDs, modelID)
	}
	sort.Strings(orderedIDs)
	models := make([]any, 0, len(chosen))
	includedModels := make(map[string]bool, len(chosen))
	for _, modelID := range orderedIDs {
		model, changed := edited[modelID]
		if !changed {
			model = before.Models[chosen[modelID]]
		}
		model.Model = modelID
		includedModels[modelID] = true
		models = append(models, modelBody(model))
	}
	body := providerBody(providerID, provider, models, includedModels)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err = s.kap.Request(ctx, http.MethodPut, "/providers/"+urlPathValue(providerID), body); err != nil {
		return nil, err
	}
	s.invalidateOfficialConfigCache()

	after, err := s.readConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("已提交，无法读回核对：%w", err)
	}
	updates := make([]providerModelUpdate, 0, len(edited))
	for _, modelID := range modelIDs {
		actual, _, ok := lookupProviderModel(after.Models, providerID, modelID)
		if !ok {
			return nil, fmt.Errorf("保存后读不到模型 %s", modelID)
		}
		if err = compareModel(edited[modelID], actual); err != nil {
			return nil, fmt.Errorf("模型 %s：%w", modelID, err)
		}
		encoded, err := json.Marshal(actual)
		if err != nil {
			return nil, err
		}
		model, err := visibleConfig(encoded)
		if err != nil {
			return nil, err
		}
		updates = append(updates, providerModelUpdate{Key: providerModelKey(providerID, modelID), Model: model})
	}
	return updates, nil
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

func urlPathValue(value string) string {
	return url.PathEscape(value)
}

func decodeModelEdit(raw any) (ModelEdit, error) {
	object, ok := raw.(map[string]any)
	if !ok {
		return ModelEdit{}, errors.New("缺少模型字段")
	}
	var edit ModelEdit
	if rawThinking, present := object["thinking"]; present {
		value, _ := rawThinking.(string)
		switch value {
		case "none", "toggle", "always":
			edit.Thinking = value
		default:
			return ModelEdit{}, errors.New("思考模式只能是 none、toggle 或 always")
		}
	}
	adaptive, present := object["adaptive_thinking"]
	if !present {
		adaptive = object["adaptiveThinking"]
	}
	if adaptive != nil {
		flag, ok := adaptive.(bool)
		if !ok {
			return ModelEdit{}, errors.New("自适应思考需要布尔值")
		}
		edit.AdaptiveThinking = &flag
	}
	capabilities, err := modelEditStrings(object["capabilities"])
	if err != nil {
		return ModelEdit{}, fmt.Errorf("模型能力：%w", err)
	}
	edit.Capabilities = capabilities
	efforts, err := modelEditStrings(object["support_efforts"])
	if err != nil {
		return ModelEdit{}, fmt.Errorf("思考强度：%w", err)
	}
	if efforts != nil {
		edit.SupportEfforts = make([]string, 0, len(efforts))
		seen := map[string]bool{}
		for _, effort := range efforts {
			effort = strings.TrimSpace(effort)
			if effort != "" && !strings.EqualFold(effort, "no") && !seen[effort] {
				seen[effort] = true
				edit.SupportEfforts = append(edit.SupportEfforts, effort)
			}
		}
	}
	if rawContext, present := object["max_context_size"]; present {
		value, err := numberValue(rawContext)
		if err != nil || value < 1 {
			return ModelEdit{}, errors.New("上下文长度需要正整数")
		}
		edit.MaxContextSize = value
	}
	if rawOutput, present := object["max_output_size"]; present {
		edit.OutputChanged = true
		if rawOutput != nil {
			value, err := numberValue(rawOutput)
			if err != nil || value < 1 {
				return ModelEdit{}, errors.New("最大输出长度需要正整数，或留空")
			}
			edit.MaxOutputSize = &value
		}
	}
	return edit, nil
}

func modelEditStrings(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, errors.New("需要字符串列表")
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		value, ok := item.(string)
		if !ok {
			return nil, errors.New("需要字符串列表")
		}
		out = append(out, value)
	}
	return out, nil
}

func numberValue(raw any) (int64, error) {
	switch value := raw.(type) {
	case float64:
		if value != float64(int64(value)) {
			return 0, errors.New("需要整数")
		}
		return int64(value), nil
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, errors.New("需要整数")
		}
		return parsed, nil
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
	if !sameCapabilitySet(expected.Capabilities, actual.Capabilities) {
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

func sameCapabilitySet(left, right []string) bool {
	set := func(values []string) map[string]bool {
		result := make(map[string]bool, len(values))
		for _, value := range values {
			result[value] = true
		}
		return result
	}
	return maps.Equal(set(left), set(right))
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
