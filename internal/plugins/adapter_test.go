package plugins

import (
	"net/http"
	"strings"
	"testing"
)

// An adapter answer names the authorized view and carries the exact body the
// plugin asked to run, so the host executes what the core approved rather than
// something looked up separately.
func TestBrowserAdapterCarriesTheAuthorizedViewAndBody(t *testing.T) {
	manager, token := officialFixture(t, []string{CapBrowserView})
	if code, value := callPlugin(t, manager, token, "browser.open", map[string]any{
		"viewId": "v1", "url": "https://example.com/list",
	}); code != http.StatusOK {
		t.Fatalf("open => %d %#v", code, value)
	}
	code, value := callPlugin(t, manager, token, "browser.adapter", map[string]any{
		"viewId":   "v1",
		"pluginId": "test.other",
		"adapter":  map[string]any{"source": "(args) => ({ count: args.count })", "args": map[string]any{"count": 3}},
	})
	if code != http.StatusOK {
		t.Fatalf("browser.adapter => %d %#v", code, value)
	}
	inner := value["value"].(map[string]any)
	if inner["pluginId"] != "test.official" {
		t.Fatalf("adapter answered with a caller-supplied owner: %#v", inner)
	}
	if inner["viewId"] != "v1" {
		t.Fatalf("adapter answered for the wrong view: %#v", inner)
	}
	adapter := inner["adapter"].(map[string]any)
	if !strings.Contains(adapter["source"].(string), "args.count") {
		t.Fatalf("adapter source was not forwarded: %#v", adapter)
	}
}

// The adapter is refused when the view does not exist or does not belong to the
// caller: an adapter is an execution primitive, so it may never cross plugins.
func TestBrowserAdapterCannotReachAnotherPluginsView(t *testing.T) {
	manager, token := officialFixture(t, []string{CapBrowserView})
	code, value := callPlugin(t, manager, token, "browser.adapter", map[string]any{
		"viewId":  "ghost",
		"adapter": map[string]any{"source": "() => 1"},
	})
	if code != http.StatusNotFound || !strings.Contains(mustString(value), "PLUGIN_BROWSER_VIEW_MISSING") {
		t.Fatalf("adapter against a missing view => %d %#v", code, value)
	}
}

// An adapter must be a function expression, bounded in size, with no silent
// fallback to an empty body.
func TestBrowserAdapterValidatesItsShape(t *testing.T) {
	manager, token := officialFixture(t, []string{CapBrowserView})
	if code, _ := callPlugin(t, manager, token, "browser.open", map[string]any{"viewId": "v1", "url": "https://example.com/"}); code != http.StatusOK {
		t.Fatal("open failed")
	}
	cases := []struct {
		name    string
		adapter any
		code    int
	}{
		{"missing", nil, http.StatusBadRequest},
		{"no source", map[string]any{"args": nil}, http.StatusBadRequest},
		{"blank source", map[string]any{"source": "   "}, http.StatusBadRequest},
		{"a statement", map[string]any{"source": "document.body.innerText"}, http.StatusBadRequest},
		{"too long", map[string]any{"source": "() => " + strings.Repeat("1", adapterSourceLimit)}, http.StatusRequestEntityTooLarge},
		{"not an object", "() => 1", http.StatusBadRequest},
	}
	for _, item := range cases {
		code, value := callPlugin(t, manager, token, "browser.adapter", map[string]any{"viewId": "v1", "adapter": item.adapter})
		if code != item.code {
			t.Fatalf("%s => %d %#v", item.name, code, value)
		}
	}
	// Both accepted forms still work: an arrow function and a function expression.
	for _, source := range []string{"() => 1", "async () => 2", "function () { return 3; }", "async function () { return 4; }"} {
		if code, value := callPlugin(t, manager, token, "browser.adapter", map[string]any{
			"viewId": "v1", "adapter": map[string]any{"source": source},
		}); code != http.StatusOK {
			t.Fatalf("source %q => %d %#v", source, code, value)
		}
	}
}

// The adapter action is gated like every other browser action.
func TestBrowserAdapterRequiresTheCapability(t *testing.T) {
	manager, token := officialFixture(t, []string{CapSessionsRead})
	code, value := callPlugin(t, manager, token, "browser.adapter", map[string]any{
		"viewId":  "v1",
		"adapter": map[string]any{"source": "() => 1"},
	})
	if code != http.StatusForbidden || !strings.Contains(mustString(value), "PLUGIN_CAPABILITY_DENIED") {
		t.Fatalf("adapter without browser:view => %d %#v", code, value)
	}
}

// The default extraction path stays untouched: a plugin with no adapter still
// gets the selection/body fallback from browser.extract.
func TestBrowserExtractStillNeedsNoAdapter(t *testing.T) {
	manager, token := officialFixture(t, []string{CapBrowserView})
	if code, _ := callPlugin(t, manager, token, "browser.open", map[string]any{"viewId": "v1", "url": "https://example.com/"}); code != http.StatusOK {
		t.Fatal("open failed")
	}
	code, value := callPlugin(t, manager, token, "browser.extract", map[string]any{"viewId": "v1"})
	if code != http.StatusOK {
		t.Fatalf("extract => %d %#v", code, value)
	}
	if _, present := value["value"].(map[string]any)["adapter"]; present {
		t.Fatalf("extract grew an adapter field: %#v", value)
	}
}
