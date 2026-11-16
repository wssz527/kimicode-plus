package provider

import (
	"context"
	"errors"
	"net/http"
	"time"

	"enhancedkimi/internal/httpx"
)

// Register mounts catalog discovery plus provider accounts. Accounts live with
// the provider client because they resolve to the credential used on the wire.
func (c *Client) Register(mux *http.ServeMux) {
	services := &Service{db: c.DB, now: func() time.Time { return time.Now().UTC() }}
	services.RegisterAccounts(mux, c.Secrets)
	c.registerDiscovery(mux)
}

func (c *Client) registerDiscovery(mux *http.ServeMux) {
	mux.HandleFunc("POST /enhance/v1/providers/{id}/discover", func(w http.ResponseWriter, r *http.Request) {
		models, err := c.Models(r.Context(), r.PathValue("id"))
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(r.Context().Err(), context.DeadlineExceeded) {
				httpx.Fail(w, 504, "DISCOVERY_TIMEOUT", "目录读取超时，未导入部分结果。请稍后重试或导入目录文件。")
			} else {
				httpx.Fail(w, 502, "DISCOVERY_FAILED", "目录读取失败。请核对地址、协议和凭据库状态。")
			}
			return
		}
		records := []any{}
		for _, m := range models {
			name := m.Name
			if name == "" {
				name = m.ID
			}
			records = append(records, map[string]any{"providerId": r.PathValue("id"), "id": m.ID, "name": name, "capabilities": map[string]any{}})
		}
		httpx.JSON(w, 200, map[string]any{"records": records, "mock": false, "external": true, "note": "仅目录 GET，未调用模型；未声明的能力保持 UNKNOWN"})
	})
}
