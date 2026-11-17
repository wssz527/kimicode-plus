package desktop

import (
	"strings"
	"testing"
)

// Every plugin view action starts at the core, and the core decides the owner.
// A payload field would be whatever the plugin's own script sent, so naming
// another plugin's id is the impersonation this path exists to refuse — and a
// plugin without browser:view must never reach Electron at all.
func TestPluginViewActionsAreAuthorizedByTheCore(t *testing.T) {
	for _, forbidden := range []string{
		"payload.pluginId",
		"pluginViewKey(payload.pluginId",
		"pluginId: payload.pluginId",
		// A token check alone is not authorization: this was the gap that let a
		// plugin without browser:view create a view.
		"plugin-runtime/runtime.info",
	} {
		if strings.Contains(bootstrapSource, forbidden) {
			t.Fatalf("the host still trusts something other than the core's browser action: %s", forbidden)
		}
	}
	for _, wanted := range []string{
		"async function coreBrowserAction(action, token, params) {",
		"coreFetch('/enhance/v1/plugin-runtime/' + action",
		"if (!value || typeof value.pluginId !== 'string' || typeof value.viewId !== 'string')",
		"coreBrowserAction('browser.open', payload.token",
		"coreBrowserAction('browser.navigate', payload.token",
		"coreBrowserAction(visible ? 'browser.bounds' : 'browser.visible', payload.token",
		"coreBrowserAction('browser.extract', payload.token",
		"coreBrowserAction('browser.adapter', payload.token",
		"coreBrowserAction('browser.close', payload.token",
	} {
		if !strings.Contains(bootstrapSource, wanted) {
			t.Fatalf("the host no longer authorizes a view action through the core: %s", wanted)
		}
	}
}

// Creating a view is the one operation allowed to build Electron state, and a
// failure there must not leave the core believing the view exists.
func TestPluginViewOpenRollsBackAFailedCreation(t *testing.T) {
	start := strings.Index(bootstrapSource, "case 'browser.view.open': {")
	if start < 0 {
		t.Fatal("bootstrap.js lost browser.view.open")
	}
	end := strings.Index(bootstrapSource[start:], "case 'browser.view.navigate': {")
	if end < 0 {
		t.Fatal("browser.view.open is not followed by browser.view.navigate")
	}
	block := bootstrapSource[start : start+end]
	for _, wanted := range []string{
		// The registry is released when the window or the load is missing.
		"await coreBrowserAction('browser.close', payload.token, { viewId: answer.viewId }).catch(() => {});",
		"dropPluginView(key);",
	} {
		if !strings.Contains(block, wanted) {
			t.Fatalf("a failed open leaves a half-registered view: %s", wanted)
		}
	}
	// Only open may create an Electron view; navigate/bounds/extract/adapter
	// resolve one.
	for _, action := range []string{"case 'browser.view.navigate': {", "case 'browser.view.bounds': {", "case 'browser.view.extract': {", "case 'browser.view.adapter': {"} {
		index := strings.Index(bootstrapSource, action)
		if index < 0 {
			continue
		}
		next := strings.Index(bootstrapSource[index+len(action):], "case 'browser.view.")
		block := bootstrapSource[index:]
		if next >= 0 {
			block = bootstrapSource[index : index+len(action)+next]
		}
		if strings.Contains(block, "new WebContentsView(") {
			t.Fatalf("%s creates a view; only open may", action)
		}
	}
}

// The renderer is the only caller of the view actions, and it must send the
// plugin's own token rather than a plugin id it looked up itself.
func TestRendererSendsPluginTokensForViews(t *testing.T) {
	start := strings.Index(rendererSource, "const pluginBrowserHost")
	if start < 0 {
		t.Fatal("renderer.js lost the plugin browser host")
	}
	end := strings.Index(rendererSource[start:], "// `core.fetch` carries the request body")
	if end < 0 {
		t.Fatal("the plugin browser host is not followed by the core transport")
	}
	host := rendererSource[start : start+end]
	if strings.Contains(host, "pluginId:") || strings.Contains(host, "{ pluginId") {
		t.Fatal("the plugin browser host still sends a plugin id")
	}
	for _, action := range []string{"browser.view.open", "browser.view.navigate", "browser.view.bounds", "browser.view.extract", "browser.view.adapter", "browser.view.close"} {
		if !strings.Contains(host, action) {
			t.Fatalf("the plugin browser host no longer reaches %s", action)
		}
	}
	if strings.Count(host, "token: pluginToken(pluginId)") != 6 {
		t.Fatal("every plugin view action must carry the caller's own token")
	}
}

// Stop releases every page a plugin created, including the one whose token was
// revoked first: the creator token recorded next to each view is what makes that
// possible without trusting the payload. The same path must not let a revoked
// token reach another plugin's page.
func TestClosingAPluginViewUsesTheRecordedCreatorToken(t *testing.T) {
	for _, wanted := range []string{
		"state.pluginViewTokens.set(key, payload.token);",
		"state.pluginViewTokens.delete(key);",
		"const recorded = [...state.pluginViewTokens].find(([candidate, token]) => token === payload.token && candidate.endsWith('/' + payload.viewId));",
	} {
		if !strings.Contains(bootstrapSource, wanted) {
			t.Fatalf("a stopped plugin can no longer destroy its own views: %s", wanted)
		}
	}
	// The fallback is reachable only from close; nothing else may skip the core.
	closeStart := strings.Index(bootstrapSource, "case 'browser.view.close': {")
	if closeStart < 0 {
		t.Fatal("bootstrap.js lost browser.view.close")
	}
	closeEnd := strings.Index(bootstrapSource[closeStart:], "case 'host.extractBrowser': {")
	if closeEnd < 0 {
		t.Fatal("browser.view.close is not followed by host.extractBrowser")
	}
	closeBlock := bootstrapSource[closeStart : closeStart+closeEnd]
	if !strings.Contains(closeBlock, "coreBrowserAction('browser.close', payload.token") {
		t.Fatal("live close no longer asks the core first")
	}
	if strings.Contains(bootstrapSource, "destroyPluginViews(payload") {
		t.Fatal("view teardown reads its scope from a caller-supplied value")
	}
}
