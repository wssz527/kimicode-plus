package desktop

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Official capability tokens, in the order the official model table lists them.
var capabilityOrder = []string{"image_in", "video_in", "tool_use", "thinking", "always_thinking"}

// ModelEdit carries the model fields the official provider form does not expose.
// MaxOutputSize is a pointer because the official endpoint clears an omitted
// max_output_size: null has to stay distinguishable from "not set here".
type ModelEdit struct {
	Capabilities     []string
	Thinking         string
	AdaptiveThinking bool
	MaxContextSize   int64
	MaxOutputSize    *int64
	SupportEfforts   []string
}

// Optional fields carry omitempty: the official config leaves a field out when
// it has no value, so a readback that includes it would be indistinguishable
// from a stored one.
type modelConfig struct {
	Provider         string   `json:"provider"`
	Model            string   `json:"model"`
	MaxContextSize   int64    `json:"maxContextSize"`
	MaxOutputSize    *int64   `json:"maxOutputSize,omitempty"`
	Capabilities     []string `json:"capabilities,omitempty"`
	DisplayName      string   `json:"displayName,omitempty"`
	AdaptiveThinking bool     `json:"adaptiveThinking,omitempty"`
	SupportEfforts   []string `json:"supportEfforts,omitempty"`
}

type providerConfig struct {
	Type    string `json:"type"`
	BaseURL string `json:"baseUrl"`
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
	keep := map[string]bool{}
	for _, capability := range edit.Capabilities {
		for _, allowed := range capabilityOrder {
			if capability == allowed {
				keep[allowed] = true
			}
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
	next.Capabilities = capabilities
	next.AdaptiveThinking = edit.Thinking == "toggle" && edit.AdaptiveThinking
	next.MaxContextSize = edit.MaxContextSize
	next.MaxOutputSize = edit.MaxOutputSize
	efforts := []string{}
	seen := map[string]bool{}
	for _, effort := range edit.SupportEfforts {
		trimmed := strings.TrimSpace(effort)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		efforts = append(efforts, trimmed)
	}
	next.SupportEfforts = efforts
	return next
}

// modelBody renders one model for the official PUT. An omitted display_name or
// max_output_size is the only way to clear it; an empty capability list is sent
// as an empty array so the stored value becomes empty rather than absent.
func modelBody(model modelConfig) map[string]any {
	item := map[string]any{"model": model.Model, "max_context_size": model.MaxContextSize}
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
	if model.AdaptiveThinking {
		item["adaptive_thinking"] = true
	}
	return item
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
	fromKey, _ := splitModelKey(key)
	if fromKey != providerID {
		return nil, fmt.Errorf("模型键 %s 不属于供应商 %s", key, providerID)
	}
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
	base, ok := before.Models[key]
	if !ok {
		return nil, fmt.Errorf("模型 %s 不在官方配置里，先保存供应商", key)
	}
	provider, ok := before.Providers[providerID]
	if !ok {
		return nil, fmt.Errorf("供应商 %s 不在官方配置里", providerID)
	}
	edited := mergeModel(base, edit)

	keys := make([]string, 0, len(before.Models))
	for candidate := range before.Models {
		if owner, _ := splitModelKey(candidate); owner == providerID {
			keys = append(keys, candidate)
		}
	}
	sort.Strings(keys)
	models := make([]any, 0, len(keys))
	for _, candidate := range keys {
		model := before.Models[candidate]
		if candidate == key {
			model = edited
		}
		if _, modelID := splitModelKey(candidate); modelID != "" {
			model.Model = modelID
		}
		models = append(models, modelBody(model))
	}
	body := map[string]any{"type": provider.Type, "models": models}
	if provider.BaseURL != "" {
		body["base_url"] = provider.BaseURL
	}
	if _, err = s.kap.Request(ctx, http.MethodPut, "/providers/"+urlPathValue(providerID), body); err != nil {
		return nil, err
	}

	after, err := s.readConfig(ctx)
	if err != nil {
		return nil, err
	}
	actual, ok := after.Models[key]
	if !ok {
		return nil, errors.New("保存后读不到这个模型，官方可能没有接受这次修改")
	}
	if err = compareModel(edited, actual); err != nil {
		return nil, err
	}
	return map[string]any{"model": actual, "provider": providerID, "key": key}, nil
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
	edit.AdaptiveThinking, _ = object["adaptive_thinking"].(bool)
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
	if expected.AdaptiveThinking != actual.AdaptiveThinking {
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
