package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"enhancedkimi/internal/config"
	"enhancedkimi/internal/credentials"
	"enhancedkimi/internal/provider"
)

const managedKimiCodeBaseURL = "https://api.kimi.com/coding/v1"

// StreamKAPModel resolves a provider-qualified model from the official Kimi Code
// catalog (`/config` + `/providers/{id}`) and streams or completes one planner
// turn. The resolved credential lives only in an ephemeral in-memory store for
// the duration of the outbound request and is wiped before returning.
func (s *NativeService) StreamKAPModel(
	ctx context.Context,
	modelKey string,
	messages []provider.Message,
	tools []map[string]any,
	emit func(provider.StreamEvent) error,
) (bool, error) {
	if s == nil || s.kap == nil {
		return false, nil
	}
	if s.safe != nil && s.safe() {
		return true, config.ErrSafeMode
	}
	snapshot, err := s.readConfig(ctx)
	if err != nil {
		if strings.Contains(modelKey, "/") {
			return true, err
		}
		return false, nil
	}

	var providerID, upstreamModel string
	if owner, target := splitModelKey(modelKey); owner != "" {
		item, _, ok := lookupProviderModel(snapshot.Models, owner, target)
		if !ok {
			return false, nil
		}
		providerID = owner
		upstreamModel = modelPart(firstNonEmpty(item.Model, target))
	} else if item, ok := snapshot.Models[modelKey]; ok {
		owner, target := resolveProviderAndModelID(modelKey, item)
		if owner == "" || target == "" {
			return false, nil
		}
		providerID = owner
		upstreamModel = modelPart(target)
	} else {
		return false, nil
	}

	provCfg, hasProv := snapshot.Providers[providerID]
	if !hasProv && !isManagedProvider(providerID) {
		return true, fmt.Errorf("供应商 %s 不在官方配置里", providerID)
	}

	protocol, err := mapKAPProtocol(provCfg.Type)
	if err != nil {
		return true, err
	}
	baseURL := strings.TrimSpace(provCfg.BaseURL)
	if isManagedProvider(providerID) && baseURL == "" {
		baseURL = managedKimiCodeBaseURL
	}
	if baseURL == "" {
		return true, fmt.Errorf("供应商 %s 未配置接口地址", providerID)
	}

	secret, err := s.plannerCredential(ctx, providerID)
	if err != nil {
		return true, err
	}

	mem := credentials.NewMemory()
	const secretRef = "kap-planner-ephemeral"
	cfg := provider.Config{ID: providerID, Protocol: protocol, BaseURL: baseURL}
	if secret != "" {
		if putErr := mem.Put(ctx, secretRef, []byte(secret)); putErr != nil {
			return true, putErr
		}
		defer func() { _ = mem.Delete(context.Background(), secretRef) }()
		ref := secretRef
		cfg = cfg.WithSecret(&ref)
	}

	client := provider.New(nil, mem)
	if protocol == "openai" {
		body := map[string]any{
			"model":          upstreamModel,
			"messages":       messages,
			"stream":         true,
			"stream_options": map[string]any{"include_usage": true},
		}
		if len(tools) > 0 {
			body["tools"] = tools
			body["tool_choice"] = "auto"
		}
		return true, client.OpenStream(ctx, cfg, upstreamModel, body, emit, nil)
	}

	if len(tools) > 0 {
		return true, fmt.Errorf("协议 %s 暂不支持带工具流式规划，请改选 OpenAI 兼容供应商或使用网页归纳", protocol)
	}
	reply, err := client.Complete(ctx, cfg, upstreamModel, nil, messages)
	if err != nil {
		return true, err
	}
	return true, emit(provider.StreamEvent{
		Model:     upstreamModel,
		Role:      "assistant",
		Text:      reply.Text,
		Reasoning: reply.Reasoning,
		Usage:     reply.Usage,
		Finish:    firstNonEmpty(reply.FinishReason, "stop"),
	})
}

func mapKAPProtocol(rawType string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(rawType)) {
	case "", "openai", "openai_legacy", "kimi":
		return "openai", nil
	case "openai_responses", "responses":
		return "responses", nil
	case "anthropic":
		return "anthropic", nil
	case "gemini", "google_genai":
		return "gemini", nil
	default:
		return "", fmt.Errorf("暂不支持供应商类型 %s", rawType)
	}
}

func (s *NativeService) plannerCredential(ctx context.Context, providerID string) (string, error) {
	if isManagedProvider(providerID) {
		home := strings.TrimSpace(s.kap.Home())
		if home != "" {
			raw, err := os.ReadFile(filepath.Join(home, "credentials", "kimi-code.json"))
			if err == nil && len(raw) <= 65536 {
				var doc struct {
					AccessToken string `json:"access_token"`
				}
				if json.Unmarshal(raw, &doc) == nil && strings.TrimSpace(doc.AccessToken) != "" {
					return strings.TrimSpace(doc.AccessToken), nil
				}
			}
		}
		return "", errors.New("未读取到 Kimi Code 登录凭据，请先在客户端登录或选择自定义供应商模型")
	}
	if s.quota == nil {
		return "", errors.New("供应商凭据服务未就绪")
	}
	secret, err := s.quota.credential(ctx, providerID)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(secret), nil
}
