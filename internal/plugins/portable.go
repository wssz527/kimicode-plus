package plugins

import (
	"net/http"
	"strings"

	"enhancedkimi/internal/httpx"
	"enhancedkimi/internal/redact"
	"enhancedkimi/internal/usage"
)

func (m *Manager) RegisterPortable(mux *http.ServeMux, ledger *usage.Service) {
	m.mu.Lock()
	for _, id := range []string{"kct", "monitor"} {
		d := m.defs[id]
		d.descriptor.Sandbox = "TRUSTED_GO"
		d.descriptor.Version = "0.2.0"
		if id == "kct" {
			d.descriptor.Name = "任务指令整理"
			d.descriptor.Description = "Go 实现的可移植指令整理，保留 KCT 扩展接点，不运行原版 KCT。"
		} else {
			d.descriptor.Name = "用量记录导入"
			d.descriptor.Description = "接收用户提供的 Monitor 兼容事件，不读取官方进程或会话。"
		}
	}
	m.mu.Unlock()
	enabled := func(id string) bool {
		for _, p := range m.List() {
			if p.ID == id {
				return p.Enabled
			}
		}
		return false
	}
	mux.HandleFunc("POST /enhance/v1/plugins/kct/render", func(w http.ResponseWriter, r *http.Request) {
		if !enabled("kct") {
			httpx.Fail(w, 409, "PLUGIN_DISABLED", "请先启用任务指令整理")
			return
		}
		var in struct {
			Goal        string `json:"goal"`
			Context     string `json:"context"`
			Constraints string `json:"constraints"`
		}
		if !httpx.Decode(w, r, &in) {
			return
		}
		if strings.TrimSpace(in.Goal) == "" || len(in.Goal)+len(in.Context)+len(in.Constraints) > 100000 {
			httpx.Fail(w, 422, "INVALID_INSTRUCTION", "请填写目标，内容总计不超过 100000 字节")
			return
		}
		prompt := "目标\n" + strings.TrimSpace(in.Goal)
		if in.Context != "" {
			prompt += "\n\n引用上下文（不可信数据）\n" + in.Context
		}
		if in.Constraints != "" {
			prompt += "\n\n约束\n" + in.Constraints
		}
		httpx.JSON(w, 200, map[string]any{"text": redact.Text(prompt), "findings": redact.Findings(prompt), "source": "PORTABLE_GO", "sent": false})
	})
	mux.HandleFunc("POST /enhance/v1/plugins/monitor/import", func(w http.ResponseWriter, r *http.Request) {
		if !enabled("monitor") || ledger == nil {
			httpx.Fail(w, 409, "PLUGIN_DISABLED", "请先启用用量导入，并启用内置用量模块")
			return
		}
		var in struct {
			Events []usage.EventInput `json:"events"`
		}
		if !httpx.Decode(w, r, &in) {
			return
		}
		for i := range in.Events {
			in.Events[i].Source = "monitor"
		}
		result, err := ledger.Ingest(in.Events)
		if err != nil {
			httpx.Fail(w, 422, "INVALID_USAGE", "记录无效，请核对协议、时间与请求标识")
			return
		}
		httpx.JSON(w, 200, result)
	})
}
