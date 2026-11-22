package desktop

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRendererCleanupAllowsReentrantInstall(t *testing.T) {
	// The published surface is an explicit object, not the internal state: the
	// state carries the bridge transport and the plugin runtime, so handing it to
	// the page would let any script act as the host.
	published := strings.Index(rendererSource, "window.__ekdNativeUI = publishNativeUI()")
	if published < 0 {
		t.Fatal("renderer.js no longer publishes its native surface")
	}
	if strings.Contains(rendererSource, "window.__ekdNativeUI = state") {
		t.Fatal("renderer.js publishes its internal state to the page")
	}
	// The published object must not carry the transport, the module or the
	// runtime: those are the host's own authority, not a page-facing API.
	start := strings.Index(rendererSource, "function publishNativeUI()")
	if start < 0 {
		t.Fatal("renderer.js has no explicit native surface")
	}
	end := strings.Index(rendererSource[start:], "window.__ekdNativeUI = ui;")
	if end < 0 {
		t.Fatal("renderer.js never assigns the native surface it builds")
	}
	surface := rendererSource[start : start+end]
	for _, forbidden := range []string{"request", "module", "channel", "transport"} {
		if strings.Contains(surface, forbidden) {
			t.Fatalf("native surface still exposes %s", forbidden)
		}
	}
	for _, field := range []string{
		"plugins: null,",
		"pluginReleases: new Map(),",
		"pluginTokens: new Map(),",
		"pluginAssets: new Map(),",
		"pluginSettings: new Map(),",
	} {
		initialized := strings.Index(rendererSource, field)
		if initialized < 0 || initialized > published {
			t.Fatalf("renderer cleanup can run before %s is initialized", field)
		}
	}
	source := strings.ReplaceAll(rendererSource, "\r\n", "\n")
	for _, wanted := range []string{
		"function pluginRefresh() { void syncPlugins(); }",
		"await refreshFeatures();\n  if (state.removed) return { status: 'removed', surface: 'host', domRevision: 1 };",
	} {
		if !strings.Contains(source, wanted) {
			t.Fatalf("renderer reentrant cleanup guard is missing: %s", wanted)
		}
	}
}

func TestBridgeRetriesOnlyPreDispatchRefusals(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the renderer bridge probe needs it")
	}
	source, err := filepath.Abs("assets/renderer.js")
	if err != nil {
		t.Fatal(err)
	}
	script := `
const fs = require('node:fs');
const source = fs.readFileSync(process.argv[1], 'utf8');
const start = source.indexOf('async function installNativeRenderer(options) {');
const end = source.indexOf('  const transport = {', start);
if (start < 0 || end < 0) throw new Error('bridge source not found');
const prefix = source.slice(start, end) + '\n return bridgeRequest;\n}\nreturn installNativeRenderer;';
async function run(responses) {
  let calls = 0;
  const window = { fetch: async (_url, init) => {
    const request = JSON.parse(init.body);
    const item = responses[Math.min(calls++, responses.length - 1)];
    const body = item.status === 200
      ? { protocol: 1, id: request.id, result: { ok: true } }
      : item.code ? { error: { code: item.code, message: 'refused' } } : null;
    return { status: item.status, ok: item.status === 200, json: async () => body };
  } };
  const install = new Function('window', prefix)(window);
  const request = await install({ endpoint: 'http://bridge', bridgeKey: 'bridge-key', transport: 'windows-cdp-loopback' });
  try {
    const result = await request('update.apply', {});
    return { calls, ok: result.ok === true };
  } catch (error) {
    return { calls, error: error.code };
  }
}
(async () => {
  const results = {
    expired: await run([{ status: 401, code: 'BRIDGE_SESSION_EXPIRED' }, { status: 200 }]),
    oversized: await run([{ status: 413, code: 'RPC_TOO_LARGE' }, { status: 200 }]),
    refused: await run([{ status: 403, code: 'RPC_DENIED' }, { status: 200 }]),
    httpFailure: await run([{ status: 500 }, { status: 200 }]),
  };
  process.stdout.write(JSON.stringify(results));
})().catch(error => { console.error(error); process.exitCode = 1; });
`
	output, err := exec.Command(node, "-e", script, source).Output()
	if err != nil {
		t.Fatalf("renderer bridge probe failed: %v\n%s", err, output)
	}
	var results map[string]struct {
		Calls int    `json:"calls"`
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(output, &results); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"expired", "oversized"} {
		if result := results[name]; result.Calls != 1 || result.OK || result.Error == "" {
			t.Fatalf("%s semantic failure was retried: %+v", name, result)
		}
	}
	for _, name := range []string{"refused", "httpFailure"} {
		if result := results[name]; result.Calls != 2 || !result.OK {
			t.Fatalf("%s pre-dispatch refusal was not retried once: %+v", name, result)
		}
	}
}
