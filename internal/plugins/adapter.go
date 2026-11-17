package plugins

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// A site adapter is the escape hatch a site-specific plugin needs: it names a
// function body, the parameters for it, and the host runs it against the page
// the plugin already owns. The core does not interpret the body — it bounds it,
// requires the page to be the one the plugin opened, and hands it to the
// Electron side, which executes it in an isolated context with no bridge.
//
// This is deliberately not "the plugin runs JavaScript in the page": the plugin
// never gets an execution primitive it can point at another plugin's view or at
// a page it did not open, and the answer is a bounded JSON value rather than an
// object the caller can use to escape.

const (
	adapterSourceLimit = 32 << 10
	adapterResultLimit = 200000
)

// normalizeAdapter validates the adapter a plugin asked to run and returns the
// exact shape the Electron side executes. An adapter with no source is refused
// rather than defaulted, because a silently empty adapter would return the page
// body and look like a working extraction.
func normalizeAdapter(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_ADAPTER_REQUIRED", "站点适配器缺少函数体")
	}
	var input struct {
		Source string          `json:"source"`
		Args   json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_ADAPTER_INVALID", "站点适配器必须是 {source, args} 对象")
	}
	source := strings.TrimSpace(input.Source)
	if source == "" {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_ADAPTER_REQUIRED", "站点适配器缺少函数体")
	}
	if len(source) > adapterSourceLimit {
		return nil, apiError(http.StatusRequestEntityTooLarge, "PLUGIN_ADAPTER_LIMIT", fmt.Sprintf("适配器源码不超过 %d 字节", adapterSourceLimit))
	}
	// Only a function expression or arrow function is a body this host can call
	// with arguments; a bare statement would execute on load and return nothing.
	if !looksLikeFunction(source) {
		return nil, apiError(http.StatusBadRequest, "PLUGIN_ADAPTER_INVALID", "站点适配器必须是函数表达式")
	}
	args := input.Args
	if len(args) == 0 {
		args = json.RawMessage("null")
	}
	if len(args) > adapterSourceLimit {
		return nil, apiError(http.StatusRequestEntityTooLarge, "PLUGIN_ADAPTER_LIMIT", "适配器参数过大")
	}
	return map[string]any{"source": source, "args": args}, nil
}

// looksLikeFunction accepts the two forms the host can wrap in a call. It is a
// shape check, not a parser: the Electron side still wraps the source in its own
// expression, so a body that is not a function fails there as a thrown error
// rather than running as a statement.
func looksLikeFunction(source string) bool {
	if strings.HasPrefix(source, "(") || strings.HasPrefix(source, "async (") {
		return true
	}
	if strings.HasPrefix(source, "function") || strings.HasPrefix(source, "async function") {
		return true
	}
	return strings.HasPrefix(source, "async ")
}
