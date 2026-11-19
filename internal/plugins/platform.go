package plugins

import "net/http"

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
		{FeatureBrowserView, CapBrowserView, "Windows 暂不支持插件独立网页视图。"},
		{FeaturePopout, CapUIRightPanel, "Windows 暂不支持弹出独立插件窗口。右侧面板标签仍可用。"},
		{FeatureNativeOverlay, CapUIEntry, "Windows 暂不支持同步独立的右侧「+」浮层。会话信息和右侧面板列表入口仍可用。"},
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
