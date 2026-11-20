package desktop

import (
	"encoding/json"
	"errors"
)

func checkReopenSessions(raw json.RawMessage) error {
	unknown := errors.New("无法确认所有会话都已停止，Kimi Code 保持打开")
	var page struct {
		Items []struct {
			ID                 string  `json:"id"`
			Busy               *bool   `json:"busy"`
			MainTurnActive     *bool   `json:"main_turn_active"`
			PendingInteraction *string `json:"pending_interaction"`
		} `json:"items"`
		HasMore *bool `json:"has_more"`
	}
	if err := json.Unmarshal(raw, &page); err != nil || page.Items == nil || page.HasMore == nil || *page.HasMore {
		return unknown
	}
	for _, item := range page.Items {
		if item.ID == "" || item.Busy == nil || item.MainTurnActive == nil || item.PendingInteraction == nil {
			return unknown
		}
		if *item.Busy || *item.MainTurnActive || *item.PendingInteraction != "none" {
			return &NativeError{Code: "HOST_SESSIONS_BUSY", Message: "有会话正在运行或等待操作。请等会话结束后再重新打开 Kimi Code。"}
		}
	}
	return nil
}
