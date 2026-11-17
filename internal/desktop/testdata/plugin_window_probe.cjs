const fs = require('node:fs');
const http = require('node:http');
const Module = require('node:module');
const path = require('node:path');

const source = fs.readFileSync(path.resolve(process.argv[2]), 'utf8');
process.versions.electron = '99.0.0';
let nextId = 100;
const windows = [];

function fakeContents(url) {
  const page = {
    id: nextId++, url, destroyed: false, listeners: new Map(), commands: [],
    getURL() { return page.url; },
    isDestroyed() { return page.destroyed; },
    on(event, handler) { page.listeners.set(event, handler); return page; },
    once(event, handler) { page.listeners.set(event, handler); return page; },
    removeListener(event) { page.listeners.delete(event); return page; },
    setWindowOpenHandler() {},
    close() { page.destroyed = true; },
    loadURL(next) { page.url = next; return Promise.resolve(); },
    reload() { page.reloaded = (page.reloaded || 0) + 1; },
    executeJavaScript(expression) {
      if (page.destroyed) return Promise.reject(new Error('page destroyed'));
      if (expression.includes('PLUGIN_RENDERER')) {
        const start = expression.indexOf(')(');
        page.renderer = JSON.parse(expression.slice(start + 2, -1));
        return Promise.resolve({ status: 'installed' });
      }
      if (expression === 'document.documentElement.dataset.colorScheme||"dark"') return Promise.resolve('dark');
      if (expression === 'window.__ekdNativeUI?.remove()') {
        page.removeCalled = true;
        return Promise.resolve();
      }
      if (expression.startsWith('window.__ekdPlugins ?')) {
        const match = expression.match(/invokeCommand\((.*)\) : Promise/);
        const command = JSON.parse(match[1]);
        page.commands.push(command);
        return Promise.resolve({ executedBy: page.renderer.surface, command: command.command, args: command.args });
      }
      return Promise.resolve(null);
    },
  };
  fakeElectron.webContents.known.set(page.id, page);
  return page;
}

class FakeWindow {
  constructor() {
    this.webContents = fakeContents('about:blank');
    this.destroyed = false;
    this.listeners = new Map();
    this.contentView = {
      added: [], removed: [],
      addChildView: view => this.contentView.added.push(view),
      removeChildView: view => this.contentView.removed.push(view),
    };
    windows.push(this);
  }
  on(event, handler) { this.listeners.set(event, handler); }
  isDestroyed() { return this.destroyed; }
  getBounds() { return { x: 10, y: 10, width: 640, height: 840 }; }
  loadURL(url) { this.webContents.url = url; return Promise.resolve(); }
  show() { this.shown = true; }
  focus() { this.focused = true; }
  close() {
    let prevented = false;
    this.listeners.get('close')?.({ preventDefault: () => { prevented = true; } });
    if (!prevented) this.destroy();
  }
  destroy() {
    if (this.destroyed) return;
    this.destroyed = true;
    this.webContents.destroyed = true;
    this.listeners.get('closed')?.();
  }
}

const fakeElectron = {
  app: { getVersion: () => '9.9.9' },
  session: { fromPartition: () => ({}) },
  dialog: { showOpenDialog: async () => ({ canceled: true, filePaths: [] }) },
  shell: { openExternal: async () => {} },
  BrowserWindow: Object.assign(FakeWindow, { fromWebContents: contents => windows.find(item => item.webContents === contents) }),
  WebContentsView: class WebContentsView {
    constructor() { this.webContents = fakeContents('about:blank'); }
    setBounds() {}
    setVisible() {}
  },
  webContents: {
    known: new Map(),
    fromId(id) { return fakeElectron.webContents.known.get(id); },
    getAllWebContents() { return [hostContents]; },
  },
};

const owners = {
  'planner-token-123456': 'kimicode-plus.planner',
  'multi-token-12345678': 'kimicode-plus.multi-session',
};
const closedViews = [];
const core = http.createServer((request, response) => {
  let body = '';
  request.on('data', chunk => { body += chunk; });
  request.on('end', () => {
    response.setHeader('Content-Type', 'application/json');
    if (request.url === '/enhance/v1/pair') {
      response.end(JSON.stringify({ sessionToken: 'core-token' }));
      return;
    }
    const action = request.url.replace('/enhance/v1/plugin-runtime/', '');
    const payload = body ? JSON.parse(body) : {};
    const pluginId = owners[payload.token];
    const params = payload.params || {};
    if (action === 'browser.close') closedViews.push({ pluginId, viewId: params.viewId, instanceId: params.instanceId });
    let value = {};
    if (action === 'runtime.info') value = { id: pluginId };
    if (action === 'ui.popout') value = { pluginId };
    if (action === 'commands.list') value = { items: [
      { name: 'multiSession.open', pluginId: 'kimicode-plus.multi-session' },
      { name: 'planner.refresh', pluginId: 'kimicode-plus.planner' },
    ] };
    if (action === 'commands.invoke') value = {
      command: params.command,
      ownerId: params.command === 'multiSession.open' ? 'kimicode-plus.multi-session' : 'kimicode-plus.planner',
      args: params.args,
    };
    if (action.startsWith('browser.')) value = { pluginId, instanceId: params.instanceId, viewId: params.viewId, url: params.url, visible: false };
    response.end(JSON.stringify({ value }));
  });
});

const hostWindow = new FakeWindow();
const hostContents = hostWindow.webContents;
hostContents.url = 'app://renderer/index.html';
const originalLoad = Module._load;
Module._load = function (request) {
  if (request === 'electron') return fakeElectron;
  return originalLoad.apply(this, arguments);
};
globalThis.require = require;

async function main() {
  await new Promise(resolve => core.listen(0, '127.0.0.1', resolve));
  const install = new Function('return (' + source + ')')();
  const installed = await install({
    expectedApp: '9.9.9', expectedElectron: process.versions.electron,
    coreURL: `http://127.0.0.1:${core.address().port}`, pairingToken: 'pair-token',
    rendererSource: 'async function PLUGIN_RENDERER(options) {}', cssSource: '', moduleSource: '',
  });
  const state = globalThis.__ekdNative;
  async function bridge(contents, action, payload) {
    const channel = state.channels.get(contents.id);
    const response = await fetch(state.endpoint + '/ekd/native/v1', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Origin: channel.expectedOrigin, 'x-ekd-bridge': channel.key },
      body: JSON.stringify({ protocol: 1, id: 'probe-' + action, action, payload }),
    });
    const body = await response.json();
    if (body.error) return { refused: body.error.code, message: body.error.message };
    if (body.result?.error) return { refused: body.result.error.code, message: body.result.error.message };
    return { ok: true, value: body.result };
  }
  const findings = { installed };
  findings.opened = await bridge(hostContents, 'plugin.window.open', {
    token: 'planner-token-123456', pluginId: 'kimicode-plus.multi-session',
  });
  const win = state.windows.get('plugin:kimicode-plus.planner');
  findings.window = win ? {
    surface: win.webContents.renderer.surface,
    pluginId: win.webContents.renderer.pluginId,
    legacyPlannerWindow: state.windows.has('planner'),
  } : null;
  if (!win) return findings;
  const popup = win.webContents;
  findings.foreignRegistration = await bridge(popup, 'plugin.command.register', {
    token: 'planner-token-123456', name: 'multiSession.open',
  });
  findings.mainRegistration = await bridge(hostContents, 'plugin.command.register', {
    token: 'multi-token-12345678', name: 'multiSession.open',
  });
  findings.popupRegistration = await bridge(popup, 'plugin.command.register', {
    token: 'planner-token-123456', name: 'planner.refresh',
  });
  findings.crossWindow = await bridge(popup, 'plugin.command.invoke', {
    token: 'planner-token-123456', name: 'multiSession.open', args: { sessionId: 'session-1' },
  });
  findings.reverseWindow = await bridge(hostContents, 'plugin.command.invoke', {
    token: 'multi-token-12345678', name: 'planner.refresh', args: null,
  });
  findings.view = await bridge(popup, 'browser.view.open', {
    token: 'planner-token-123456', viewId: 'web', url: 'https://example.com/',
  });
  const ownedView = state.pluginViews.get(findings.view.value.instanceId + '/kimicode-plus.planner/web');
  findings.mainView = await bridge(hostContents, 'browser.view.open', {
    token: 'planner-token-123456', viewId: 'web', url: 'https://example.com/main',
  });
  const mainView = state.pluginViews.get(findings.mainView.value.instanceId + '/kimicode-plus.planner/web');
  findings.distinctInstanceIds = Boolean(findings.view.value?.instanceId && findings.mainView.value?.instanceId && findings.view.value.instanceId !== findings.mainView.value.instanceId);
  findings.beforeCloseViews = state.pluginViews.size;
  win.close();
  for (let i = 0; i < 100 && !win.destroyed; i++) {
    await new Promise(resolve => setTimeout(resolve, 10));
  }
  findings.closed = {
    destroyed: win.destroyed,
    rendererRemoved: popup.removeCalled === true,
    views: state.pluginViews.size,
    ownedViewDestroyed: ownedView?.webContents.isDestroyed() === true,
    mainViewAlive: mainView?.webContents.isDestroyed() === false,
    detachedViews: win.contentView.removed.length,
    popupCommand: state.pluginCommands.has('kimicode-plus.planner:planner.refresh'),
    closedCoreViews: [...closedViews],
  };
  findings.closedCommand = await bridge(hostContents, 'plugin.command.invoke', {
    token: 'multi-token-12345678', name: 'planner.refresh', args: null,
  });
  findings.mainStillWorks = await bridge(hostContents, 'plugin.command.invoke', {
    token: 'multi-token-12345678', name: 'multiSession.open', args: { sessionId: 'session-2' },
  });
  findings.mainViewReload = await bridge(hostContents, 'browser.view.history', {
    token: 'planner-token-123456', viewId: 'web', operation: 'reload',
  });
  await state.cleanup();
  return findings;
}

main().then(findings => {
  core.close();
  process.stdout.write(JSON.stringify(findings, null, 2) + '\n');
}).catch(error => {
  core.close();
  process.stderr.write(String(error?.stack || error) + '\n');
  process.exit(1);
});
