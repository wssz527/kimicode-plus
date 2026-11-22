package desktop

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// renewalProbe drives the shipped bootstrap.js against a fake Electron and a
// fake core. Each scenario installs a fresh host, so the assertions describe
// behaviour of the real file rather than a restatement of its source.
const renewalProbe = `
const fs = require('node:fs');
const http = require('node:http');
const Module = require('node:module');
const path = require('node:path');

const source = fs.readFileSync(path.resolve(process.argv[2]), 'utf8');
const runtimePath = path.resolve(process.argv[3]);
const PAIRING_ONE = 'p'.repeat(48);
const PAIRING_TWO = 'q'.repeat(48);
process.versions.electron = '99.0.0';

let nextId = 500;
let currentElectron = null;

function fakeContents(url, id) {
  const page = {
    id: id || nextId++,
    url,
    destroyed: false,
    onceListeners: new Map(),
    listeners: new Map(),
    renderers: [],
    getURL() { return page.url; },
    isDestroyed() { return page.destroyed; },
    on(event, handler) { page.listeners.set(event, handler); return page; },
    once(event, handler) { page.onceListeners.set(event, handler); return page; },
    removeListener(event) {
      page.listeners.delete(event);
      page.onceListeners.delete(event);
      return page;
    },
    emit(event) {
      // Node snapshots the listener list when the event is emitted, so a
      // listener added while dispatching does not also receive this event.
      const once = page.onceListeners.get(event);
      if (once) page.onceListeners.delete(event);
      const persistent = page.listeners.get(event);
      if (once) once({ sender: page });
      if (persistent) persistent({ sender: page });
    },
    destroy() {
      if (page.destroyed) return;
      page.destroyed = true;
      page.emit('destroyed');
    },
    setWindowOpenHandler() {},
    executeJavaScript(expression) {
      if (page.destroyed) return Promise.reject(new Error('page destroyed'));
      if (expression.includes('RENEWAL_RENDERER')) {
        const start = expression.indexOf(')(');
        page.renderers.push(JSON.parse(expression.slice(start + 2, -1)));
        return Promise.resolve({ status: 'installed' });
      }
      if (expression === 'document.documentElement.dataset.colorScheme||"dark"') return Promise.resolve('dark');
      return Promise.resolve(null);
    },
  };
  if (currentElectron) currentElectron.webContents.known.set(page.id, page);
  return page;
}

class FakeWindow {
  constructor() {
    this.webContents = fakeContents('about:blank');
    this.destroyed = false;
    this.windowListeners = new Map();
    this.contentView = { addChildView() {}, removeChildView() {} };
  }
  on(event, handler) { this.windowListeners.set(event, handler); }
  isDestroyed() { return this.destroyed; }
  getBounds() { return { x: 0, y: 0, width: 640, height: 840 }; }
  loadURL(url) { this.webContents.url = url; return Promise.resolve(); }
  show() {}
  focus() {}
  close() { this.destroy(); }
  destroy() {
    if (this.destroyed) return;
    this.destroyed = true;
    this.webContents.destroyed = true;
    const closed = this.windowListeners.get('closed');
    if (closed) closed();
    this.webContents.emit('destroyed');
  }
}

function makeElectron(hostContents) {
  const appListeners = new Map();
  const electron = {
    appListeners,
    windows: [],
    app: {
      getVersion: () => '9.9.9',
      on(event, handler) { appListeners.set(event, handler); return this; },
      once(event, handler) { appListeners.set(event, handler); return this; },
      removeListener(event) { appListeners.delete(event); return this; },
    },
    session: { fromPartition: () => ({}) },
    dialog: { showOpenDialog: async () => ({ canceled: true, filePaths: [] }) },
    shell: { async openExternal() {} },
    BrowserWindow: Object.assign(function BrowserWindow() {
      const win = new FakeWindow();
      electron.windows.push(win);
      return win;
    }, { fromWebContents: () => null }),
    WebContentsView: class WebContentsView {
      constructor() { this.webContents = fakeContents('about:blank'); }
      setBounds() {}
      setVisible() {}
    },
    ipcMain: { prependListener() {}, on() {}, removeListener() {} },
    webContents: {
      known: new Map(),
      fromId(id) { return this.known.get(id); },
      getAllWebContents() { return hostContents ? [hostContents] : []; },
    },
  };
  if (hostContents) electron.webContents.known.set(hostContents.id, hostContents);
  currentElectron = electron;
  return electron;
}

const originalLoad = Module._load;
Module._load = function (request) {
  if (request === 'electron') return currentElectron;
  return originalLoad.apply(this, arguments);
};
globalThis.require = require;

function listen(server) {
  return new Promise(resolve => server.listen(0, '127.0.0.1', () => resolve(server.address().port)));
}

// The core records every pairing and every authorized request, so a test can see
// which token each call used and whether a renewal spent a /pair call. Tokens are
// minted per pairing; a token the scenario has expired answers 401 TOKEN_EXPIRED.
function makeCore(state) {
  return http.createServer((request, response) => {
    let body = '';
    request.on('data', chunk => { body += chunk; });
    request.on('end', () => {
      response.setHeader('Content-Type', 'application/json');
      if (request.url === '/enhance/v1/pair') {
        const pairing = JSON.parse(body || '{}').pairingToken;
        state.pairingTokens.push(pairing);
        const minted = state.mint(pairing);
        response.end(JSON.stringify({ sessionToken: minted.token, expiresAt: minted.expiresAt }));
        return;
      }
      const token = String(request.headers.authorization || '').replace('Bearer ', '');
      state.requestTokens.push(token);
      if (state.expiredTokens.has(token)) {
        const reply = () => {
          response.statusCode = 401;
          response.end(JSON.stringify({ error: { code: 'TOKEN_EXPIRED', message: '增强核心会话凭据已失效或过期，正在重新连接' } }));
        };
        if (state.holdExpired) {
          state.holdExpired(reply);
          return;
        }
        reply();
        return;
      }
      if (state.forbidden) {
        response.statusCode = 403;
        response.end(JSON.stringify({ error: { code: 'PERMISSION_DENIED', message: '当前凭据没有此资源权限' } }));
        return;
      }
      if (request.url === '/enhance/v1/plugin-runtime/ui.popout') {
        response.end(JSON.stringify({ value: { pluginId: 'probe.plugin' } }));
        return;
      }
      response.end(JSON.stringify({ value: { ok: true, token } }));
    });
  });
}

async function install(coreURL, pairingToken, hostContents, electron) {
  const run = new Function('return (' + source + ')')();
  const installed = await run({
    expectedApp: '9.9.9',
    expectedElectron: process.versions.electron,
    coreURL,
    pairingToken,
    runtimePath,
    rendererSource: 'async function RENEWAL_RENDERER(options) { return { status: "installed" }; }',
    cssSource: '',
    moduleSource: '',
  });
  const state = globalThis.__ekdNative;
  return { installed, state, channel: state.channels.get(hostContents.id) };
}

async function bridge(state, contents, action, payload) {
  const channel = state.channels.get(contents.id);
  if (!channel) return { refused: 'NO_CHANNEL' };
  const response = await fetch(state.endpoint + '/ekd/native/v1', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Origin: channel.expectedOrigin, 'x-ekd-bridge': channel.key },
    body: JSON.stringify({ protocol: 1, id: 'probe-' + action, action, payload }),
  });
  const answer = await response.json();
  if (answer.error) return { refused: answer.error.code, message: answer.error.message };
  if (answer.result && answer.result.error) return { refused: answer.result.error.code, message: answer.result.error.message };
  return { ok: true, value: answer.result };
}

function writeRuntime(coreURL, pairingToken) {
  fs.writeFileSync(runtimePath, JSON.stringify({ url: coreURL, pairingToken }), 'utf8');
}

async function settle(times) {
  for (let i = 0; i < (times || 4); i++) await new Promise(resolve => setTimeout(resolve, 5));
}

// Scenario 1: the token is refused with 401 TOKEN_EXPIRED on the same core. The
// host must renew once and retry once, without re-injecting anything.
async function scenarioRenewAfterExpiry() {
  const host = fakeContents('app://renderer/index.html');
  const electron = makeElectron(host);
  let minted = 0;
  const state = {
    pairingTokens: [], requestTokens: [], expiredTokens: new Set(),
    mint: () => (++minted === 1
      ? { token: '{{SECRET_2j9mctzq}}', expiresAt: new Date(Date.now() + 6 * 60 * 60 * 1000).toISOString() }
      : { token: 'planner-token-123456', expiresAt: new Date(Date.now() + 12 * 60 * 60 * 1000).toISOString() }),
  };
  const core = makeCore(state);
  const port = await listen(core);
  const coreURL = 'http://127.0.0.1:' + port;
  writeRuntime(coreURL, PAIRING_ONE);
  const { installed, state: world } = await install(coreURL, PAIRING_ONE, host, electron);
  state.expiredTokens.add('{{SECRET_2j9mctzq}}');
  const result = await bridge(world, host, 'attach.status', {});
  await settle();
  const findings = {
    installed: installed.status,
    ok: result.ok === true,
    pairCalls: state.pairingTokens.length,
    requestTokens: state.requestTokens,
    bindings: host.renderers.length,
    windowsInState: world.windows.size,
  };
  await world.cleanup();
  core.close();
  return findings;
}

// Scenario 2: the first token is already inside the renewal window, so the host
// renews before sending anything at all.
async function scenarioPreemptiveRenewal() {
  const host = fakeContents('app://renderer/index.html');
  const electron = makeElectron(host);
  let minted = 0;
  const state = {
    pairingTokens: [], requestTokens: [], expiredTokens: new Set(),
    mint: () => (++minted === 1
      ? { token: '{{SECRET_7mlq4xtn}}', expiresAt: new Date(Date.now() + 5 * 60 * 1000).toISOString() }
      : { token: '{{SECRET_9pwq2kzd}}', expiresAt: new Date(Date.now() + 12 * 60 * 60 * 1000).toISOString() }),
  };
  const core = makeCore(state);
  const port = await listen(core);
  const coreURL = 'http://127.0.0.1:' + port;
  writeRuntime(coreURL, PAIRING_ONE);
  const { state: world } = await install(coreURL, PAIRING_ONE, host, electron);
  const result = await bridge(world, host, 'attach.status', {});
  await settle();
  const findings = {
    ok: result.ok === true,
    pairCalls: state.pairingTokens.length,
    requestTokens: state.requestTokens,
    firstRequestRenewed: state.requestTokens[0] === '{{SECRET_9pwq2kzd}}',
    bindings: host.renderers.length,
  };
  await world.cleanup();
  core.close();
  return findings;
}

// Scenario 3: a live token that lacks the scope. Renewing cannot fix that, so it
// must not spend a /pair call or a retry.
async function scenarioPermissionDeniedDoesNotRenew() {
  const host = fakeContents('app://renderer/index.html');
  const electron = makeElectron(host);
  const state = {
    pairingTokens: [], requestTokens: [], expiredTokens: new Set(), forbidden: true,
    mint: () => ({ token: '{{SECRET_2j9mctzq}}', expiresAt: new Date(Date.now() + 6 * 60 * 60 * 1000).toISOString() }),
  };
  const core = makeCore(state);
  const port = await listen(core);
  const coreURL = 'http://127.0.0.1:' + port;
  writeRuntime(coreURL, PAIRING_ONE);
  const { state: world } = await install(coreURL, PAIRING_ONE, host, electron);
  const result = await bridge(world, host, 'attach.status', {});
  await settle();
  const findings = {
    refused: result.refused || '',
    pairCalls: state.pairingTokens.length,
    requestTokens: state.requestTokens,
  };
  await world.cleanup();
  core.close();
  return findings;
}

// Shared by the two re-injection scenarios: open a plugin window on a healthy
// token, then make that token expire.
async function pluginWindowThenExpire(mint) {
  const host = fakeContents('app://renderer/index.html');
  const electron = makeElectron(host);
  const state = { pairingTokens: [], requestTokens: [], expiredTokens: new Set(), mint };
  const core = makeCore(state);
  const port = await listen(core);
  const coreURL = 'http://127.0.0.1:' + port;
  writeRuntime(coreURL, PAIRING_ONE);
  const { state: world } = await install(coreURL, PAIRING_ONE, host, electron);
  const opened = await bridge(world, host, 'plugin.window.open', { token: 'multi-token-12345678' });
  const pluginWindow = world.windows.get('plugin:probe.plugin');
  state.expiredTokens.add('{{SECRET_2j9mctzq}}');
  return { host, electron, state, core, coreURL, world, opened, pluginWindow };
}

// Scenario 4: the same core renews. The user's plugin window must survive, and
// the renderer must not be injected a second time.
async function scenarioSameCoreRenewsSilently() {
  let minted = 0;
  const context = await pluginWindowThenExpire(() => (++minted === 1
    ? { token: '{{SECRET_2j9mctzq}}', expiresAt: new Date(Date.now() + 6 * 60 * 60 * 1000).toISOString() }
    : { token: 'planner-token-123456', expiresAt: new Date(Date.now() + 12 * 60 * 60 * 1000).toISOString() }));
  const result = await bridge(context.world, context.host, 'attach.status', {});
  await settle(8);
  const findings = {
    opened: context.opened.ok === true,
    ok: result.ok === true,
    pairCalls: context.state.pairingTokens.length,
    bindings: context.host.renderers.length,
    pluginWindowAlive: Boolean(context.pluginWindow && !context.pluginWindow.isDestroyed()),
    windowsInState: context.world.windows.size,
    requestTokens: context.state.requestTokens,
  };
  await context.world.cleanup();
  context.core.close();
  return findings;
}

// Scenario 5: the runtime record carries a different pairing token, which means
// the core is a new instance. Everything bound to the old session is rebuilt.
async function scenarioRestartedCoreRebinds() {
  let minted = 0;
  const context = await pluginWindowThenExpire(() => (++minted === 1
    ? { token: '{{SECRET_2j9mctzq}}', expiresAt: new Date(Date.now() + 6 * 60 * 60 * 1000).toISOString() }
    : { token: 'planner-token-123456', expiresAt: new Date(Date.now() + 12 * 60 * 60 * 1000).toISOString() }));
  writeRuntime(context.coreURL, PAIRING_TWO);
  const result = await bridge(context.world, context.host, 'attach.status', {});
  await settle(12);
  const findings = {
    opened: context.opened.ok === true,
    ok: result.ok === true,
    pairCalls: context.state.pairingTokens.length,
    pairTokens: context.state.pairingTokens,
    bindings: context.host.renderers.length,
    pluginWindowDestroyed: Boolean(context.pluginWindow && context.pluginWindow.isDestroyed()),
    windowsInState: context.world.windows.size,
    requestTokens: context.state.requestTokens,
  };
  await context.world.cleanup();
  context.core.close();
  return findings;
}

// Scenario 6: Cmd+W on macOS keeps the process alive. Closing the main window
// must not tear down the integration, and the window the Dock reopens must be
// injected and bound automatically.
async function scenarioReopenAfterWindowClosed() {
  const host = fakeContents('app://renderer/index.html');
  const electron = makeElectron(host);
  const state = {
    pairingTokens: [], requestTokens: [], expiredTokens: new Set(),
    mint: () => ({ token: '{{SECRET_2j9mctzq}}', expiresAt: new Date(Date.now() + 12 * 60 * 60 * 1000).toISOString() }),
  };
  const core = makeCore(state);
  const port = await listen(core);
  const coreURL = 'http://127.0.0.1:' + port;
  writeRuntime(coreURL, PAIRING_ONE);
  const { state: world } = await install(coreURL, PAIRING_ONE, host, electron);
  const before = host.renderers.length;
  host.destroy();
  await settle(4);
  const afterClose = {
    cleaned: Boolean(world.cleaned),
    serverListening: Boolean(world.server && world.server.listening),
    globalState: globalThis.__ekdNative === world,
  };
  const reopened = fakeContents('app://renderer/index.html');
  const created = electron.appListeners.get('web-contents-created');
  // Electron calls this listener as (event, webContents).
  if (created) created({}, reopened);
  reopened.emit('did-finish-load');
  await settle(6);
  const channel = world.channels.get(reopened.id);
  const findings = {
    before,
    afterClose,
    adopted: reopened.renderers.length > 0,
    surfaces: reopened.renderers.map(item => item.surface),
    channelBound: Boolean(channel),
    beforeQuitRegistered: typeof electron.appListeners.get('before-quit') === 'function',
    worksAfterReopen: false,
  };
  if (channel) {
    const result = await bridge(world, reopened, 'attach.status', {});
    findings.worksAfterReopen = result.ok === true;
  }
  await world.cleanup();
  core.close();
  findings.listenersRemoved = !electron.appListeners.has('web-contents-created') && !electron.appListeners.has('before-quit');
  return findings;
}

async function scenarioConcurrentRenewal() {
  const host = fakeContents('app://renderer/index.html');
  const electron = makeElectron(host);
  const expiredReplies = [];
  const state = {
    pairingTokens: [], requestTokens: [], expiredTokens: new Set(),
    holdExpired: reply => { expiredReplies.push(reply); },
    mint: (() => {
      let count = 0;
      return () => (++count === 1
        ? { token: '{{SECRET_2j9mctzq}}', expiresAt: new Date(Date.now() + 6 * 60 * 60 * 1000).toISOString() }
        : { token: 'planner-token-123456', expiresAt: new Date(Date.now() + 12 * 60 * 60 * 1000).toISOString() });
    })(),
  };
  const core = makeCore(state);
  const port = await listen(core);
  const coreURL = 'http://127.0.0.1:' + port;
  writeRuntime(coreURL, PAIRING_ONE);
  const { state: world } = await install(coreURL, PAIRING_ONE, host, electron);
  state.expiredTokens.add('{{SECRET_2j9mctzq}}');
  const calls = [bridge(world, host, 'attach.status', {}), bridge(world, host, 'accounts.list', {})];
  for (let i = 0; i < 200 && expiredReplies.length < 2; i += 1) await settle(1);
  if (expiredReplies.length !== 2) throw new Error('two requests did not use the expired token');
  expiredReplies[0]();
  for (let i = 0; i < 200 && (state.pairingTokens.length < 2 || !state.requestTokens.includes('planner-token-123456')); i += 1) await settle(1);
  if (state.pairingTokens.length !== 2) throw new Error('first request did not renew');
  expiredReplies[1]();
  const results = await Promise.all(calls);
  const findings = { ok: results.every(result => result.ok === true), pairCalls: state.pairingTokens.length, requestTokens: state.requestTokens };
  await world.cleanup();
  core.close();
  return findings;
}

async function main() {
  return {
    renewAfterExpiry: await scenarioRenewAfterExpiry(),
    preemptive: await scenarioPreemptiveRenewal(),
    permissionDenied: await scenarioPermissionDeniedDoesNotRenew(),
    sameCore: await scenarioSameCoreRenewsSilently(),
    restartedCore: await scenarioRestartedCoreRebinds(),
    concurrentRenewal: await scenarioConcurrentRenewal(),
    reopen: await scenarioReopenAfterWindowClosed(),
    fatal: '',
  };
}

main().then(findings => {
  process.stdout.write(JSON.stringify(findings, null, 2) + '\n');
}).catch(error => {
  process.stdout.write(JSON.stringify({ fatal: String(error && error.stack || error) }) + '\n');
  process.exitCode = 1;
});
`

// renewalPairingTwo is the runtime pairing token of the "restarted" core in the
// probe below; the host refuses pairing tokens shorter than 32 characters.
const renewalPairingTwo = "qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq"

type renewalFindings struct {
	RenewAfterExpiry struct {
		Installed     string   `json:"installed"`
		OK            bool     `json:"ok"`
		PairCalls     int      `json:"pairCalls"`
		RequestTokens []string `json:"requestTokens"`
		Bindings      int      `json:"bindings"`
		WindowsAlive  int      `json:"windowsInState"`
	} `json:"renewAfterExpiry"`
	Preemptive struct {
		OK                  bool     `json:"ok"`
		PairCalls           int      `json:"pairCalls"`
		RequestTokens       []string `json:"requestTokens"`
		FirstRequestRenewed bool     `json:"firstRequestRenewed"`
		Bindings            int      `json:"bindings"`
	} `json:"preemptive"`
	PermissionDenied struct {
		Refused       string   `json:"refused"`
		PairCalls     int      `json:"pairCalls"`
		RequestTokens []string `json:"requestTokens"`
	} `json:"permissionDenied"`
	SameCore struct {
		Opened           bool     `json:"opened"`
		OK               bool     `json:"ok"`
		PairCalls        int      `json:"pairCalls"`
		Bindings         int      `json:"bindings"`
		PluginWindowAliv bool     `json:"pluginWindowAlive"`
		WindowsInState   int      `json:"windowsInState"`
		RequestTokens    []string `json:"requestTokens"`
	} `json:"sameCore"`
	RestartedCore struct {
		Opened              bool     `json:"opened"`
		OK                  bool     `json:"ok"`
		PairCalls           int      `json:"pairCalls"`
		PairTokens          []string `json:"pairTokens"`
		Bindings            int      `json:"bindings"`
		PluginWindowDestroy bool     `json:"pluginWindowDestroyed"`
		WindowsInState      int      `json:"windowsInState"`
		RequestTokens       []string `json:"requestTokens"`
	} `json:"restartedCore"`
	ConcurrentRenewal struct {
		OK            bool     `json:"ok"`
		PairCalls     int      `json:"pairCalls"`
		RequestTokens []string `json:"requestTokens"`
	} `json:"concurrentRenewal"`
	Reopen struct {
		Before  int `json:"before"`
		AfterCl struct {
			Cleaned         bool `json:"cleaned"`
			ServerListening bool `json:"serverListening"`
			GlobalState     bool `json:"globalState"`
		} `json:"afterClose"`
		Adopted            bool     `json:"adopted"`
		Surfaces           []string `json:"surfaces"`
		ChannelBound       bool     `json:"channelBound"`
		BeforeQuitRegister bool     `json:"beforeQuitRegistered"`
		WorksAfterReopen   bool     `json:"worksAfterReopen"`
		ListenersRemoved   bool     `json:"listenersRemoved"`
	} `json:"reopen"`
	Fatal string `json:"fatal"`
}

// The session a host holds lasts twelve hours. Renewing it, renewing it early,
// refusing to renew a real permission failure, re-attaching a reopened window,
// and leaving the user's windows alone unless the core itself restarted are all
// behaviours of the shipped bootstrap.js, so they are exercised by running it
// rather than by reading it.
func TestBootstrapRenewsSessionsAndRebindsWindows(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the executable bootstrap probe needs it")
	}
	dir := t.TempDir()
	probe := filepath.Join(dir, "bootstrap_renewal_probe.cjs")
	if err := os.WriteFile(probe, []byte(renewalProbe), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := filepath.Abs("assets/bootstrap.js")
	if err != nil {
		t.Fatal(err)
	}
	runtime := filepath.Join(dir, "runtime.json")
	output, err := exec.Command(node, probe, source, runtime).Output()
	if err != nil {
		t.Fatalf("the renewal probe failed: %v\n%s", err, output)
	}
	var run renewalFindings
	if err := json.Unmarshal(output, &run); err != nil {
		t.Fatalf("the renewal probe answered unreadable JSON: %v\n%s", err, output)
	}
	if run.Fatal != "" {
		t.Fatalf("the probe could not install the host: %s", run.Fatal)
	}

	if run.RenewAfterExpiry.Installed != "installed" || !run.RenewAfterExpiry.OK {
		t.Fatalf("a 401 TOKEN_EXPIRED was not recovered: %#v", run.RenewAfterExpiry)
	}
	if run.RenewAfterExpiry.PairCalls != 2 {
		t.Fatalf("expected exactly one renewal pairing, got %d: %#v", run.RenewAfterExpiry.PairCalls, run.RenewAfterExpiry)
	}
	if len(run.RenewAfterExpiry.RequestTokens) != 2 || run.RenewAfterExpiry.RequestTokens[0] == run.RenewAfterExpiry.RequestTokens[1] {
		t.Fatalf("the retry did not use a renewed token: %q", run.RenewAfterExpiry.RequestTokens)
	}
	if run.RenewAfterExpiry.Bindings != 1 {
		t.Fatalf("a same-core renewal must not rebind renderers: %#v", run.RenewAfterExpiry)
	}

	if !run.Preemptive.OK || !run.Preemptive.FirstRequestRenewed || run.Preemptive.PairCalls != 2 {
		t.Fatalf("the token was not renewed before it expired: %#v", run.Preemptive)
	}
	if run.Preemptive.Bindings != 1 {
		t.Fatalf("a pre-emptive renewal must not rebind renderers: %#v", run.Preemptive)
	}

	if run.PermissionDenied.Refused != "PERMISSION_DENIED" || run.PermissionDenied.PairCalls != 1 || len(run.PermissionDenied.RequestTokens) != 1 {
		t.Fatalf("a permission failure spent a renewal: %#v", run.PermissionDenied)
	}

	if !run.SameCore.Opened || !run.SameCore.OK || run.SameCore.PairCalls != 2 {
		t.Fatalf("the same-core renewal did not recover the call: %#v", run.SameCore)
	}
	if !run.SameCore.PluginWindowAliv || run.SameCore.Bindings != 1 || run.SameCore.WindowsInState != 1 {
		t.Fatalf("a same-core renewal tore down the user's windows: %#v", run.SameCore)
	}

	if !run.RestartedCore.Opened || !run.RestartedCore.OK || run.RestartedCore.PairCalls != 2 {
		t.Fatalf("a restarted core did not recover the call: %#v", run.RestartedCore)
	}
	if run.RestartedCore.Bindings != 2 {
		t.Fatalf("a restarted core must rebind the renderer exactly once: %#v", run.RestartedCore)
	}
	if len(run.RestartedCore.PairTokens) != 2 || run.RestartedCore.PairTokens[1] != renewalPairingTwo {
		t.Fatalf("the renewal did not read the new runtime pairing token: %q", run.RestartedCore.PairTokens)
	}
	if !run.RestartedCore.PluginWindowDestroy || run.RestartedCore.WindowsInState != 0 {
		t.Fatalf("a restarted core must drop windows bound to the old session: %#v", run.RestartedCore)
	}

	if run.ConcurrentRenewal.PairCalls != 2 || !run.ConcurrentRenewal.OK || len(run.ConcurrentRenewal.RequestTokens) != 4 {
		t.Fatalf("a staggered 401 triggered another renewal instead of reusing the new token: %#v", run.ConcurrentRenewal)
	}
	if run.Reopen.Before != 1 {
		t.Fatalf("the initial bind did not happen once: %#v", run.Reopen)
	}
	if run.Reopen.AfterCl.Cleaned || !run.Reopen.AfterCl.ServerListening || !run.Reopen.AfterCl.GlobalState {
		t.Fatalf("closing the main window tore the integration down: %#v", run.Reopen.AfterCl)
	}
	if !run.Reopen.Adopted || !run.Reopen.ChannelBound || !run.Reopen.WorksAfterReopen {
		t.Fatalf("the reopened window was not re-injected: %#v", run.Reopen)
	}
	if len(run.Reopen.Surfaces) != 1 || run.Reopen.Surfaces[0] != "host" {
		t.Fatalf("the reopened window was bound to the wrong surface: %q", run.Reopen.Surfaces)
	}
	if !run.Reopen.BeforeQuitRegister || !run.Reopen.ListenersRemoved {
		t.Fatalf("app listeners were not installed and removed cleanly: %#v", run.Reopen)
	}
}
