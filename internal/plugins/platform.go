package plugins

import (
	"context"
	"net/http"

	"enhancedkimi/internal/security"
)

func (m *Manager) platformFor(ctx context.Context) string {
	if m.platform == "windows" && security.HasElectronMain(ctx) {
		return ""
	}
	return m.platform
}

func (m *Manager) pluginFor(ctx context.Context, plugin Plugin) Plugin {
	if m.platformFor(ctx) != m.platform {
		plugin.PlatformLimitations = nil
	}
	return plugin
}

const (
	FeatureBrowserView   = "browser.view"
	FeaturePopout        = "window.popout"
	FeatureNativeOverlay = "entry.nativeOverlay"
)

type PlatformLimitation struct {
	Feature    string `json:"feature"`
	Capability string `json:"capability"`
	Message    string `json:"message"`
}

func PlatformLimitations(platform string) []PlatformLimitation {
	if platform != "windows" {
		return nil
	}
	return []PlatformLimitation{
		{FeatureBrowserView, CapBrowserView, "插件独立网页需要从 KimiCode+ 入口重新打开 Kimi Code。"},
		{FeaturePopout, CapUIRightPanel, "独立插件窗口需要从 KimiCode+ 入口重新打开 Kimi Code。右侧面板标签仍可用。"},
		{FeatureNativeOverlay, CapUIEntry, "独立的右侧「+」浮层需要从 KimiCode+ 入口重新打开 Kimi Code。会话信息和右侧面板列表入口仍可用。"},
	}
}

func pluginPlatformLimitations(platform string, declared []string) []PlatformLimitation {
	var result []PlatformLimitation
	for _, limitation := range PlatformLimitations(platform) {
		if contains(declared, limitation.Capability) {
			result = append(result, limitation)
		}
	}
	return result
}

func CheckPlatformFeature(platform, feature string) *Error {
	for _, limitation := range PlatformLimitations(platform) {
		if limitation.Feature == feature {
			return apiError(http.StatusConflict, "PLATFORM_UNSUPPORTED", limitation.Message)
		}
	}
	return nil
}
