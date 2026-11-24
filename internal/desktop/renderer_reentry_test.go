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
		"pluginRevisions: new Map(),",
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
async function run(responses, action = 'update.apply') {
  let calls = 0;
  const window = { fetch: async (_url, init) => {
    const request = JSON.parse(init.body);
    const item = responses[Math.min(calls++, responses.length - 1)];
    if (item.disconnected) throw new TypeError('network disconnected');
    const body = item.status === 200
      ? { protocol: 1, id: request.id, result: { ok: true } }
      : item.code ? { error: { code: item.code, message: 'refused' } } : null;
    return { status: item.status, ok: item.status === 200, json: async () => body };
  } };
  const install = new Function('window', prefix)(window);
  const request = await install({ endpoint: 'http://bridge', bridgeKey: 'bridge-key', transport: 'windows-cdp-loopback' });
  try {
    const result = await request(action, {});
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
    lostWriteResponse: await run([{ disconnected: true }, { status: 200 }]),
    lostReadResponse: await run([{ disconnected: true }, { status: 200 }], 'update.status'),
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
	for _, name := range []string{"expired", "oversized", "httpFailure", "lostWriteResponse"} {
		if result := results[name]; result.Calls != 1 || result.OK || result.Error == "" {
			t.Fatalf("%s failure was retried: %+v", name, result)
		}
	}
	if result := results["lostWriteResponse"]; result.Error != "BRIDGE_OUTCOME_UNKNOWN" {
		t.Fatalf("a lost write response must leave its outcome unknown: %+v", result)
	}
	for _, name := range []string{"refused", "lostReadResponse"} {
		if result := results[name]; result.Calls != 2 || !result.OK {
			t.Fatalf("%s safe request was not retried once: %+v", name, result)
		}
	}
}

func TestRendererConcurrentInstallAndLegacyZombieRecovery(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the renderer reentry probe needs it")
	}
	source, err := filepath.Abs("assets/renderer.js")
	if err != nil {
		t.Fatal(err)
	}
	script := `
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { pathToFileURL } = require('node:url');

const source = fs.readFileSync(process.argv[1], 'utf8');
const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'ekd-renderer-reentry-'));

function makeNode(tag) {
  const node = {
    tagName: String(tag || 'div').toUpperCase(),
    dataset: {},
    style: { cssText: '', setProperty() {}, removeProperty() {} },
    children: [],
    parentElement: null,
    attributes: new Map(),
    className: '',
    textContent: '',
    innerHTML: '',
    hidden: false,
    classList: {
      set: new Set(),
      add(...items) { items.forEach(i => this.set.add(i)); },
      remove(...items) { items.forEach(i => this.set.delete(i)); },
      contains(item) { return this.set.has(item); },
      toggle(item, force) {
        if (force === undefined) return this.set.has(item) ? (this.set.delete(item), false) : (this.set.add(item), true);
        if (force) this.set.add(item); else this.set.delete(item);
        return Boolean(force);
      },
    },
    get isConnected() {
      let cur = this;
      while (cur) {
        if (cur === document.body || cur === document.head || cur === document.documentElement) return true;
        cur = cur.parentElement;
      }
      return false;
    },
    get nextElementSibling() {
      if (!this.parentElement) return null;
      const idx = this.parentElement.children.indexOf(this);
      return idx >= 0 ? (this.parentElement.children[idx + 1] || null) : null;
    },
    setAttribute(name, value) { this.attributes.set(name, String(value)); },
    getAttribute(name) { return this.attributes.has(name) ? this.attributes.get(name) : null; },
    hasAttribute(name) { return this.attributes.has(name); },
    removeAttribute(name) { this.attributes.delete(name); },
    addEventListener() {},
    removeEventListener() {},
    append(...items) {
      for (const item of items) {
        if (!item || typeof item === 'string') continue;
        if (item.parentElement) item.remove();
        item.parentElement = this;
        this.children.push(item);
      }
    },
    insertBefore(item, ref) {
      if (!item) return item;
      if (item.parentElement) item.remove();
      item.parentElement = this;
      const idx = ref ? this.children.indexOf(ref) : -1;
      if (idx >= 0) this.children.splice(idx, 0, item);
      else this.children.push(item);
      return item;
    },
    replaceChildren(...items) {
      for (const child of [...this.children]) child.parentElement = null;
      this.children = [];
      this.append(...items);
    },
    remove() {
      if (!this.parentElement) return;
      const idx = this.parentElement.children.indexOf(this);
      if (idx >= 0) this.parentElement.children.splice(idx, 1);
      this.parentElement = null;
    },
    querySelector() { return null; },
    querySelectorAll() { return []; },
    closest() { return null; },
  };
  return node;
}

const fakeAppNode = makeNode('div');
fakeAppNode.__vue_app__ = {
  config: {
    globalProperties: {
      $pinia: {
        _s: new Map([
          ['kimi.sessions', { activeSessionId: 'sess-1', $subscribe: () => () => {} }],
        ]),
      },
    },
  },
};

const document = {
  documentElement: makeNode('html'),
  head: makeNode('head'),
  body: makeNode('body'),
  hidden: false,
  createElement: tag => makeNode(tag),
  createTextNode: text => String(text),
  addEventListener() {},
  removeEventListener() {},
  querySelector(selector) { return selector === '#app' ? fakeAppNode : null; },
  querySelectorAll() { return []; },
};
const windowObj = {
  addEventListener() {},
  removeEventListener() {},
  dispatchEvent() {},
  location: { href: 'app://renderer/index.html' },
  localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
};

let activeObservers = 0;
let totalObserversCreated = 0;
const observerCallbacks = [];
class FakeMutationObserver {
  constructor(cb) {
    this.cb = cb;
    this.connected = false;
    totalObserversCreated += 1;
    observerCallbacks.push(this);
  }
  observe() {
    if (!this.connected) {
      this.connected = true;
      activeObservers += 1;
    }
  }
  disconnect() {
    if (this.connected) {
      this.connected = false;
      activeObservers -= 1;
    }
  }
}

let activeChannels = 0;
let closedChannels = 0;
let unwatchCalls = 0;
let holdUnwatch = false;
let ensureSyncCount = 0;
let releaseFirstImport = null;

globalThis.__EKD_TEST_GATES__ = {
  waitFirstImport() {
    return new Promise(resolve => { releaseFirstImport = resolve; });
  },
  onChannelOpen() { activeChannels += 1; },
  onChannelClose() { activeChannels -= 1; closedChannels += 1; },
  onEnsure() { ensureSyncCount += 1; },
};

let blobCounter = 0;
const fakeURL = {
  revoked: [],
  createObjectURL(blob) {
    const file = path.join(tmpDir, 'mod-' + (++blobCounter) + '.mjs');
    fs.writeFileSync(file, blob.__text, 'utf8');
    return pathToFileURL(file).href;
  },
  revokeObjectURL(url) { fakeURL.revoked.push(url); },
};
class FakeBlob {
  constructor(parts) { this.__text = parts.join(''); }
}

function buildModuleSource(waitOnImport) {
  const lines = [
    waitOnImport ? 'await globalThis.__EKD_TEST_GATES__.waitFirstImport();' : '',
    'export function createEventChannel() {',
    '  let closed = false;',
    '  globalThis.__EKD_TEST_GATES__.onChannelOpen();',
    '  return {',
    '    subscribe() { return () => {}; },',
    '    close() {',
    '      if (closed) return;',
    '      closed = true;',
    '      globalThis.__EKD_TEST_GATES__.onChannelClose();',
    '    },',
    '  };',
    '}',
    'export function createDOMContractState() { return {}; }',
    'export function evaluateDOMContracts() {',
    '  globalThis.__EKD_TEST_GATES__.onEnsure();',
    '  return { probes: [], byId: {}, gates: {} };',
    '}',
    'export function createEntryMountManager() { return { sync() {}, remove() {}, dispose() {} }; }',
    'export function createNativeHost(_transport, _hooks, channel) {',
    '  return { subscribe: (scope, listener) => channel.subscribe(scope, listener), close() {} };',
    '}',
    'export function findSettingsDialogParts() { return null; }',
    'export function findOfficialSettingsPanel() { return null; }',
    'export function findOfficialSettingsSection() { return null; }',
    'export function findOfficialSettingsTab() { return null; }',
    'export function findSessionInfoKeepAliveAnchor() { return null; }',
    'export function findChatHeaderTail() { return null; }',
    'export function createPluginRuntime() {',
    '  return { activate: async () => {}, deactivate() {}, dispose() {}, get() { return null; }, stats() { return {}; }, eventLoops() { return 0; }, invokeLocal() {} };',
    '}',
    'export function loadPluginModule() { return {}; }',
    'export function pluginEntryPath(p) { return p; }',
    'export function mountSurface() { return { unmount() {} }; }',
    'export function mountMainAreaSurface() { return { setWidth() {}, width: 0, dispose() {} }; }',
  ];
  return lines.join('\n');
}

windowObj.fetch = async (_url, init) => {
  const req = JSON.parse(init.body);
  if (req.action === 'session.unwatch' && holdUnwatch) {
    unwatchCalls += 1;
    await new Promise(() => {});
  }
  if (req.action === 'session.watch') {
    return { ok: true, status: 200, json: async () => ({ protocol: 1, id: req.id, result: { watching: true } }) };
  }
  if (req.action === 'features.list') {
    return { ok: true, status: 200, json: async () => ({ protocol: 1, id: req.id, result: { multiSession: false } }) };
  }
  if (req.action === 'plugins.list') {
    return { ok: true, status: 200, json: async () => ({ protocol: 1, id: req.id, result: { items: [] } }) };
  }
  return { ok: true, status: 200, json: async () => ({ protocol: 1, id: req.id, result: {} }) };
};

const createProviderEnhance = () => ({ install() {}, remove() {}, clearDecorations() {} });
const install = new Function(
  'window', 'document', 'MutationObserver', 'URL', 'Blob', 'CustomEvent', 'createProviderEnhance',
  source + '\nreturn installNativeRenderer;'
)(
  windowObj,
  document,
  FakeMutationObserver,
  fakeURL,
  FakeBlob,
  class CustomEvent { constructor(type, init) { this.type = type; this.detail = init?.detail; } },
  createProviderEnhance,
);

(async () => {
  const out = {};

  // 1. Concurrent installNativeRenderer while first call is awaiting import(moduleURL).
  const firstPromise = install({
    endpoint: 'http://bridge-1',
    bridgeKey: 'key-1',
    surface: 'host',
    coreURL: 'http://127.0.0.1:1001',
    moduleSource: buildModuleSource(true),
    cssSource: '',
  });
  while (!releaseFirstImport) await new Promise(resolve => setTimeout(resolve, 5));

  const secondPromise = install({
    endpoint: 'http://bridge-2',
    bridgeKey: 'key-2',
    surface: 'host',
    coreURL: 'http://127.0.0.1:1002',
    moduleSource: buildModuleSource(false),
    cssSource: '',
  });
  releaseFirstImport();

  const [firstRes, secondRes] = await Promise.all([firstPromise, secondPromise]);
  out.concurrent = {
    firstStatus: firstRes.status,
    secondStatus: secondRes.status,
    activeObservers,
    activeChannels,
    mainBridgeCore: windowObj.__ekdNativeUI?.mainBridgeCore || '',
    revokedCount: fakeURL.revoked.length,
  };

  // 2. Simulate legacy v0.2.6 zombie waking up late, hijacking __ekdNativeUI, and wiping globals in remove().
  const savedUI = windowObj.__ekdNativeUI;
  const savedPlugins = windowObj.__ekdPlugins;
  const savedBrowserNav = windowObj.__ekdPluginBrowserNavigated;
  let zombieRemoved = 0;
  windowObj.__ekdNativeUI = {
    mainBridgeCore: 'http://127.0.0.1:1001',
    async remove() {
      zombieRemoved += 1;
      delete windowObj.__ekdPlugins;
      delete windowObj.__ekdPluginBrowserNavigated;
      await new Promise(resolve => setTimeout(resolve, 20));
      delete windowObj.__ekdNativeUI;
    },
  };
  const ensureBefore = ensureSyncCount;
  // Trigger the active observer callback so ensure() runs reclaimLegacyZombie().
  const liveObserver = observerCallbacks.find(o => o.connected);
  liveObserver.cb();
  await new Promise(resolve => setTimeout(resolve, 60));
  out.zombie = {
    zombieRemoved,
    uiRestored: windowObj.__ekdNativeUI === savedUI,
    pluginsRestored: windowObj.__ekdPlugins === savedPlugins,
    browserNavRestored: windowObj.__ekdPluginBrowserNavigated === savedBrowserNav,
    ensuredAgain: ensureSyncCount > ensureBefore,
    activeObservers,
    activeChannels,
  };

  // 3. Idempotent remove() closes channel and does not hang when session.unwatch stalls.
  holdUnwatch = true;
  const activeUI = windowObj.__ekdNativeUI;
  const startedAt = Date.now();
  await Promise.all([activeUI.remove(), activeUI.remove()]);
  const elapsedMs = Date.now() - startedAt;
  out.teardown = {
    elapsedMs,
    unwatchCalls,
    activeObservers,
    activeChannels,
    closedChannels,
    uiCleared: windowObj.__ekdNativeUI === undefined,
    pluginsCleared: windowObj.__ekdPlugins === undefined,
    browserNavCleared: windowObj.__ekdPluginBrowserNavigated === undefined,
  };

  fs.rmSync(tmpDir, { recursive: true, force: true });
  process.stdout.write(JSON.stringify(out));
})().catch(error => {
  fs.rmSync(tmpDir, { recursive: true, force: true });
  console.error(error);
  process.exitCode = 1;
});
`
	output, err := exec.Command(node, "-e", script, source).CombinedOutput()
	if err != nil {
		t.Fatalf("renderer concurrency probe failed: %v\n%s", err, output)
	}
	var got struct {
		Concurrent struct {
			FirstStatus    string `json:"firstStatus"`
			SecondStatus   string `json:"secondStatus"`
			ActiveObservers int   `json:"activeObservers"`
			ActiveChannels int    `json:"activeChannels"`
			MainBridgeCore string `json:"mainBridgeCore"`
			RevokedCount   int    `json:"revokedCount"`
		} `json:"concurrent"`
		Zombie struct {
			ZombieRemoved      int  `json:"zombieRemoved"`
			UIRestored         bool `json:"uiRestored"`
			PluginsRestored    bool `json:"pluginsRestored"`
			BrowserNavRestored bool `json:"browserNavRestored"`
			EnsuredAgain       bool `json:"ensuredAgain"`
			ActiveObservers    int  `json:"activeObservers"`
			ActiveChannels     int  `json:"activeChannels"`
		} `json:"zombie"`
		Teardown struct {
			ElapsedMs         int  `json:"elapsedMs"`
			UnwatchCalls      int  `json:"unwatchCalls"`
			ActiveObservers   int  `json:"activeObservers"`
			ActiveChannels    int  `json:"activeChannels"`
			ClosedChannels    int  `json:"closedChannels"`
			UICleared         bool `json:"uiCleared"`
			PluginsCleared    bool `json:"pluginsCleared"`
			BrowserNavCleared bool `json:"browserNavCleared"`
		} `json:"teardown"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("unreadable probe JSON: %v\n%s", err, output)
	}
	if got.Concurrent.FirstStatus != "removed" || got.Concurrent.SecondStatus != "installed" {
		t.Fatalf("concurrent install did not supersede stale call: %+v", got.Concurrent)
	}
	if got.Concurrent.ActiveObservers != 1 || got.Concurrent.ActiveChannels != 1 || got.Concurrent.MainBridgeCore != "http://127.0.0.1:1002" {
		t.Fatalf("concurrent install leaked observers/channels or wrong UI: %+v", got.Concurrent)
	}
	if got.Concurrent.RevokedCount < 1 {
		t.Fatalf("stale moduleURL was not revoked: %+v", got.Concurrent)
	}
	if got.Zombie.ZombieRemoved != 1 || !got.Zombie.UIRestored || !got.Zombie.PluginsRestored || !got.Zombie.BrowserNavRestored || !got.Zombie.EnsuredAgain {
		t.Fatalf("legacy zombie recovery failed: %+v", got.Zombie)
	}
	if got.Zombie.ActiveObservers != 1 || got.Zombie.ActiveChannels != 1 {
		t.Fatalf("legacy zombie recovery altered active observer/channel counts: %+v", got.Zombie)
	}
	if got.Teardown.UnwatchCalls != 1 || got.Teardown.ElapsedMs < 200 || got.Teardown.ElapsedMs > 1200 || got.Teardown.ActiveObservers != 0 || got.Teardown.ActiveChannels != 0 || got.Teardown.ClosedChannels != 1 {
		t.Fatalf("renderer teardown leaked resources or stalled: %+v", got.Teardown)
	}
	if !got.Teardown.UICleared || !got.Teardown.PluginsCleared || !got.Teardown.BrowserNavCleared {
		t.Fatalf("renderer teardown did not clear globals cleanly: %+v", got.Teardown)
	}
}

func TestReconcilePluginsHotReloadsOnVersionChange(t *testing.T) {
	source := strings.ReplaceAll(rendererSource, "\r\n", "\n")
	for _, want := range []string{
		"pluginRevisions: new Map()",
		"state.pluginRevisions.clear()",
		"state.pluginRevisions.get(id) === pluginRevision(next)",
		"state.pluginRevisions.set(plugin.id, pluginRevision(plugin))",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("renderer.js missing plugin hot-reload guard: %s", want)
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; skipping JS hot-reload simulation")
	}
	rendererAbs, err := filepath.Abs("assets/renderer.js")
	if err != nil {
		t.Fatal(err)
	}
	script := `
const fs = require('node:fs');
const source = fs.readFileSync(process.argv[1], 'utf8');
const start = source.indexOf('    // A plugin the runtime reports as stopped or upgraded');
const end = source.indexOf('\n  }\n\n  // The settings surface owns the settings module lifecycle', start);
if (start < 0 || end < 0) throw new Error('reconcilePlugins block not found');
const snippet = source.slice(start, end);
(async () => {
  const events = [];
  const state = {
    removed: false,
    pluginReleases: new Map(),
    pluginRevisions: new Map(),
    pluginTokens: new Map(),
    pluginAssets: new Map(),
    pluginSettings: new Map(),
    plugins: {
      activate: async (entry, meta) => { events.push('activate:' + meta.id + '@' + meta.version + ':' + entry.ver + ':' + meta.token); },
      deactivate: id => { events.push('deactivate:' + id); },
    },
  };
  let currentList = [];
  let tokenSeq = 0;
  let assetLoads = 0;
  const isStale = () => false;
  const corePost = async () => ({ token: 'tok-' + (++tokenSeq) });
  const module = { pluginEntryPath: p => p };
  const loadPluginAsset = async (id, entryPath) => {
    const key = id + '/' + entryPath + '/activate';
    if (state.pluginAssets.has(key)) return state.pluginAssets.get(key);
    assetLoads += 1;
    const loaded = { ver: 'bundle-' + assetLoads };
    state.pluginAssets.set(key, loaded);
    return loaded;
  };
  const noteActivationFault = (_id, err) => { throw err; };
  const reconcile = new Function('state', 'enabled', 'isStale', 'corePost', 'loadPluginAsset', 'module', 'noteActivationFault', 'return (async () => {\n' + snippet + '\n})();');

  const v1 = [{ id: 'kimicode-plus.kct', version: '1.0.4', updatedAt: 't1', apiVersion: 1, capabilities: ['ui:thinking'], entry: { renderer: 'renderer.js' } }];
  await reconcile(state, v1, isStale, corePost, loadPluginAsset, module, noteActivationFault);
  // Same revision must not reload.
  await reconcile(state, v1, isStale, corePost, loadPluginAsset, module, noteActivationFault);
  // Version bump must deactivate v1, clear asset cache, fetch fresh token & bundle, and activate v2.
  const v2 = [{ id: 'kimicode-plus.kct', version: '1.0.5', updatedAt: 't2', apiVersion: 1, capabilities: ['ui:thinking'], entry: { renderer: 'renderer.js' } }];
  await reconcile(state, v2, isStale, corePost, loadPluginAsset, module, noteActivationFault);
  process.stdout.write(JSON.stringify({ events, assetLoads, activeToken: state.pluginTokens.get('kimicode-plus.kct') }));
})().catch(err => { console.error(err); process.exitCode = 1; });
`
	output, err := exec.Command(node, "-e", script, rendererAbs).CombinedOutput()
	if err != nil {
		t.Fatalf("hot-reload probe failed: %v\n%s", err, output)
	}
	var res struct {
		Events      []string `json:"events"`
		AssetLoads  int      `json:"assetLoads"`
		ActiveToken string   `json:"activeToken"`
	}
	if err := json.Unmarshal(output, &res); err != nil {
		t.Fatalf("decode hot-reload probe output: %v\n%s", err, output)
	}
	wantEvents := []string{
		"activate:kimicode-plus.kct@1.0.4:bundle-1:tok-1",
		"deactivate:kimicode-plus.kct",
		"activate:kimicode-plus.kct@1.0.5:bundle-2:tok-2",
	}
	if strings.Join(res.Events, "|") != strings.Join(wantEvents, "|") || res.AssetLoads != 2 || res.ActiveToken != "tok-2" {
		t.Fatalf("unexpected hot-reload lifecycle: %+v", res)
	}
}
