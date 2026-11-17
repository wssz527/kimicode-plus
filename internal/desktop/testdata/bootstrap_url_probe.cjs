// Executable probe for the host's plugin-page reads.
//
// The real bootstrap.js is installed here against a fake Electron module and a
// fake core, and the two view actions are driven over the bridge the plugin
// runtime actually uses. The point is to make the page disagree with the address
// the core recorded — which is what a sign-in redirect or an in-page route
// change does — and see whether reading the plugin's own page still works.
//
// Usage: node bootstrap_url_probe.cjs <path to bootstrap.js>
const fs = require('node:fs');
const http = require('node:http');
const Module = require('module');
const path = require('node:path');

const source = fs.readFileSync(path.resolve(process.argv[2]), 'utf8');

// bootstrap.js refuses to install into a host whose Electron version does not
// match what the injector reported, and plain Node reports none.
process.versions.electron = '99.0.0';

let nextId = 100;
function fakeContents(url) {
  const page = {
    id: nextId++,
    url,
    destroyed: false,
    listeners: new Map(),
    getURL() { return page.url; },
    isDestroyed() { return page.destroyed; },
    on(event, handler) { page.listeners.set(event, handler); return page; },
    once(event, handler) { page.listeners.set(event, handler); return page; },
    removeListener(event) { page.listeners.delete(event); return page; },
    setWindowOpenHandler() {},
    close() { page.destroyed = true; },
    loadURL(next) { page.url = next; return Promise.resolve(); },
    executeJavaScript(expression) {
      if (!page.destroyed) {
        // The page an adapter or a text read sees is the page it is on now.
        return Promise.resolve(expression.startsWith('window.getSelection')
          ? `text of ${page.url}`
          : { pageUrl: page.url });
      }
      return Promise.reject(new Error('page destroyed'));
    },
  };
  fakeElectron.webContents.known.set(page.id, page);
  return page;
}

const ownerWindow = { contentView: { addChildView() {}, removeChildView() {} } };

const fakeElectron = {
  app: { getVersion: () => '9.9.9' },
  session: { fromPartition: () => ({}) },
  dialog: { showOpenDialog: async () => ({ canceled: true, filePaths: [] }) },
  BrowserWindow: Object.assign(function BrowserWindow() {}, {
    fromWebContents: () => ownerWindow,
  }),
  WebContentsView: class WebContentsView {
    constructor() {
      this.webContents = fakeContents('about:blank');
    }
    setBounds() {}
    setVisible() {}
  },
  webContents: {
    known: new Map(),
    fromId(id) { return fakeElectron.webContents.known.get(id); },
    getAllWebContents() { return [hostContents]; },
  },
};

// The core the host talks to. Only the two routes bootstrap uses are answered.
const core = http.createServer((request, response) => {
  let body = '';
  request.on('data', chunk => { body += chunk; });
  request.on('end', () => {
    response.setHeader('Content-Type', 'application/json');
    if (request.url === '/enhance/v1/pair') {
      response.end(JSON.stringify({ sessionToken: 'pair-token-synthetic-0001' }));
      return;
    }
    const action = request.url.replace('/enhance/v1/plugin-runtime/', '');
    const params = body ? JSON.parse(body).params || {} : {};
    const value = { pluginId: 'test.probe', viewId: params.viewId, url: recorded.url, action };
    // browser.adapter answers with the adapter the core accepted, which is what
    // the host runs; extract and the rest carry the ownership fields alone.
    if (params.adapter) value.adapter = params.adapter;
    response.end(JSON.stringify({ value }));
  });
});

const hostContents = fakeContents('app://renderer/index.html');
const recorded = { url: '' };

const originalLoad = Module._load;
Module._load = function (request) {
  if (request === 'electron') return fakeElectron;
  return originalLoad.apply(this, arguments);
};
globalThis.require = require;

async function main() {
  await new Promise(resolve => core.listen(0, '127.0.0.1', resolve));
  const coreURL = `http://127.0.0.1:${core.address().port}`;
  const fixture = http.createServer((request, response) => {
    response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    response.end('<!doctype html><html><body><main>Alpha</main></body></html>');
  });
  await new Promise(resolve => fixture.listen(0, '127.0.0.1', resolve));
  recorded.url = `http://127.0.0.1:${fixture.address().port}/list`;

  const install = new Function('return (' + source + ')')();
  const installed = await install({
    expectedApp: '9.9.9',
    expectedElectron: process.versions.electron,
    coreURL,
    pairingToken: 'pairing-token-synthetic-0002',
    rendererSource: 'async function () { return { status: "mounted" }; }',
    cssSource: '',
    moduleSource: '',
  });
  const state = globalThis.__ekdNative;
  const channel = state.channels.get(hostContents.id);
  const token = 'plugin-token-synthetic-0003';

  async function bridge(action, payload) {
    const response = await fetch(state.endpoint + '/ekd/native/v1', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Origin: 'app://renderer', 'x-ekd-bridge': channel.key },
      body: JSON.stringify({ protocol: 1, id: 'probe-' + action, action, payload }),
    });
    const body = await response.json();
    if (body.error) return { refused: body.error.code, message: body.error.message };
    if (body.result?.error) return { refused: body.result.error.code, message: body.result.error.message };
    return { ok: true, value: body.result };
  }

  const findings = { installed, recordedURL: recorded.url };
  const opened = await bridge('browser.view.open', { token, viewId: 'probe-view', url: recorded.url });
  findings.opened = opened;
  const view = state.pluginViews.get('test.probe/probe-view');
  if (!view) {
    findings.fatal = 'the view was never registered';
    return findings;
  }

  async function read(label, pageURL) {
    // The page moves on its own: a redirect or a route change sets what
    // getURL() answers without touching the address core recorded.
    view.webContents.url = pageURL;
    const extracted = await bridge('browser.view.extract', { token, viewId: 'probe-view' });
    const adapted = await bridge('browser.view.adapter', {
      token,
      viewId: 'probe-view',
      adapter: { source: '() => ({ pageUrl: location.href })', args: null },
    });
    findings[label] = { pageURL, extract: extracted, adapter: adapted };
  }

  await read('redirected', `http://127.0.0.1:${fixture.address().port}/after-sign-in`);
  await read('pushState', `http://127.0.0.1:${fixture.address().port}/list/app/42`);
  await read('fileScheme', 'file:///etc/passwd');
  await read('dataScheme', 'data:text/html,<h1>hi</h1>');

  await state.cleanup();
  fixture.close();
  return findings;
}

main()
  .then(findings => {
    core.close();
    process.stdout.write(JSON.stringify(findings, null, 2) + '\n');
    process.exit(findings.fatal ? 1 : 0);
  })
  .catch(error => {
    process.stderr.write(String(error && error.stack || error) + '\n');
    process.exit(1);
  });
