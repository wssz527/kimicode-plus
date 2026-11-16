async function installNativeBootstrap(options) {
  const req = typeof require === 'function' ? require : process.mainModule.require.bind(process.mainModule);
  const { webContents, BrowserWindow, WebContentsView, session, app, dialog } = req('electron');
  const http = req('node:http');
  const crypto = req('node:crypto');
  if (app.getVersion() !== options.expectedApp || process.versions.electron !== options.expectedElectron) throw new Error('宿主版本在接入时发生变化');
  if (globalThis.__ekdNative) await globalThis.__ekdNative.cleanup();
  const wc = webContents.getAllWebContents().find(item => item.getURL().startsWith('app://renderer/') && !/screenshot|browser-overlay/.test(item.getURL()));
  if (!wc) throw new Error('隔离主窗口未找到');
  const pairResponse = await fetch(new URL('/enhance/v1/pair', options.coreURL), {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ pairingToken: options.pairingToken, permissions: ['core:read','settings:write','models:read','models:write','usage:read','usage:write','tasks:read','tasks:write','chat:read','chat:write','plugins:read','plugins:write','adapter:write','credentials:read','credentials:write','workspace:read','workspace:write','browser:read','browser:write'] }),
  });
  const pair = await pairResponse.json();
  if (!pairResponse.ok) throw new Error(pair?.error?.message || '增强核心配对失败');
  let coreToken = pair.sessionToken;
  const state = { channels: new Map(), windows: new Map(), views: new Map(), bounds: new Map(), browserWindow: null, server: null, cleanup: null };
  globalThis.__ekdNative = state;
  const browserSession = session.fromPartition('persist:ekd-native-browser');
  function json(response, status, value, origin) {
    response.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8', 'Cache-Control': 'no-store', 'Access-Control-Allow-Origin': origin, 'Access-Control-Allow-Headers': 'content-type,x-ekd-bridge', 'Access-Control-Allow-Methods': 'POST, OPTIONS', 'Access-Control-Allow-Private-Network': 'true', 'Vary': 'Origin' });
    response.end(JSON.stringify(value));
  }
  function readBody(request) {
    return new Promise((resolve, reject) => {
      let size = 0; const chunks = [];
      request.on('data', chunk => { size += chunk.length; if (size > 24 * 1024 * 1024) { reject(new Error('RPC 请求过大')); request.destroy(); } else chunks.push(chunk); });
      request.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')));
      request.on('error', reject);
    });
  }
  async function coreFetch(path, init = {}) {
    const headers = new Headers(init.headers || {});
    headers.set('Accept', 'application/json');
    headers.set('Authorization', 'Bearer ' + coreToken);
    if (init.body !== undefined && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json');
    const response = await fetch(new URL(path, options.coreURL), { ...init, headers, signal: AbortSignal.timeout(120000) });
    return response;
  }
  async function nativeAction(action, payload) {
    const response = await coreFetch('/enhance/v1/desktop/actions/' + action, { method: 'POST', body: JSON.stringify(payload || {}) });
    const body = await response.json().catch(() => null);
    if (!response.ok) { const error = new Error(body?.error?.message || `核心返回 HTTP ${response.status}`); error.code = body?.error?.code || 'CORE_ERROR'; throw error; }
    return body?.value;
  }
  function ownedBrowser(target, mode) { return mode === 'exec' ? state.browserWindow?.webContents : state.views.get(target.id)?.webContents; }
  async function openWindow(kind, source = {}) {
    const existing = state.windows.get(kind);
    if (existing && !existing.isDestroyed()) { existing.show(); existing.focus(); return { focused: true }; }
    const theme = await wc.executeJavaScript('document.documentElement.dataset.colorScheme||"dark"');
    const previous = state.bounds.get(kind);
    const win = new BrowserWindow({
      title: 'KimiCode+ 规划',
      width: previous?.width || 640, height: previous?.height || 840,
      x: previous?.x, y: previous?.y, minWidth: 400, minHeight: 500,
      show: false, autoHideMenuBar: true, backgroundColor: theme === 'dark' ? '#121212' : '#ffffff',
      webPreferences: { contextIsolation: true, nodeIntegration: false, sandbox: true },
    });
    state.windows.set(kind, win);
    const contentsId = win.webContents.id;
    win.on('close', () => { if (!win.isDestroyed()) state.bounds.set(kind, win.getBounds()); });
    win.on('closed', () => { state.channels.delete(contentsId); state.windows.delete(kind); });
    win.webContents.setWindowOpenHandler(() => ({ action: 'deny' }));
    await win.loadURL('about:blank');
    await bindRenderer(win.webContents, 'planner', { popout: true, theme, ...source });
    win.show(); win.focus();
    return { opened: true };
  }
  async function bindRenderer(target, surface, extra = {}) {
    const key = crypto.randomBytes(32).toString('base64url');
    state.channels.set(target.id, { key, expectedOrigin: target === wc ? 'app://renderer' : 'null' });
    const config = {
      endpoint: state.endpoint, bridgeKey: key, moduleSource: options.moduleSource, cssSource: options.cssSource,
      surface, ...extra,
    };
    return target.executeJavaScript('(' + options.rendererSource + ')(' + JSON.stringify(config) + ')');
  }
  async function dispatch(target, message) {
    const payload = message.payload || {};
    switch (message.action) {
      case 'core.fetch': {
        if (typeof payload.path !== 'string' || !payload.path.startsWith('/enhance/v1/') || payload.path.includes('..') || payload.path.includes('\\')) throw new Error('核心请求路径不被允许');
        const method = String(payload.method || 'GET').toUpperCase();
        if (!['GET','POST','PUT','PATCH','DELETE'].includes(method)) throw new Error('核心请求方法不被允许');
        const headers = {};
        for (const [key, value] of Object.entries(payload.headers || {})) {
          if (['accept','content-type','idempotency-key','x-attachment-sha256','x-attachment-type'].includes(key.toLowerCase()) && typeof value === 'string') headers[key] = value;
        }
        const body = payload.bodyBase64 ? Buffer.from(payload.bodyBase64, 'base64') : undefined;
        if (body && body.length > 16 * 1024 * 1024) throw new Error('核心请求体过大');
        const response = await coreFetch(payload.path, { method, headers, body });
        const result = Buffer.from(await response.arrayBuffer()).toString('base64');
        return { status: response.status, headers: [['Content-Type', response.headers.get('content-type') || 'application/json']], bodyBase64: result };
      }
      case 'window.openSettings': return wc.executeJavaScript('window.__ekdNativeUI.openSettings()');
      case 'window.popout': return openWindow('planner', { sourceSessionId: payload.sourceSessionId || '', initialSource: payload.initialSource === 'local' ? 'local' : 'web' });
      case 'window.close': BrowserWindow.fromWebContents(target)?.close(); return {};
      case 'window.ensureWidth': {
        const win = BrowserWindow.fromWebContents(target);
        if (!win) return {};
        const size = win.getSize(); const width = Math.max(Number(payload.width) || 0, size[0]);
        if (width > size[0]) win.setSize(Math.min(width, 3000), size[1]);
        return {};
      }
      case 'host.openSession': return wc.executeJavaScript('window.__ekdNativeUI.openOfficial(' + JSON.stringify(payload.id) + ')');
      case 'host.pickSession': return wc.executeJavaScript('window.__ekdNativeUI.pickSession(' + JSON.stringify(payload.exclude || []) + ')');
      case 'host.activateSession': return wc.executeJavaScript('window.__ekdNativeUI.activateSession(' + JSON.stringify(payload.id) + ')');
      case 'host.pickFiles': {
        const result = await dialog.showOpenDialog(BrowserWindow.fromWebContents(target), { title: '添加附件', properties: ['openFile', 'multiSelections'] });
        return result.canceled ? [] : result.filePaths.map(path => ({ path, name: req('node:path').basename(path) }));
      }
      case 'host.openBrowser': {
        const url = new URL(payload.url);
        if (!['http:','https:'].includes(url.protocol) || url.href.length > 2048) throw new Error('只允许有效的 http 或 https 地址');
        if (payload.mode === 'exec') {
          if (!state.browserWindow || state.browserWindow.isDestroyed()) {
            state.browserWindow = new BrowserWindow({ title: 'KimiCode+ 网页', width: 1000, height: 800, webPreferences: { session: browserSession, contextIsolation: true, nodeIntegration: false, sandbox: true } });
            state.browserWindow.on('closed', () => { state.browserWindow = null; });
          }
          const browser = state.browserWindow.webContents;
          if (browser.getURL() !== url.href) await browser.loadURL(url.href);
          state.browserWindow.show();
          return { browserId: browser.id };
        }
        let view = state.views.get(target.id);
        if (!view) {
          view = new WebContentsView({ webPreferences: { session: browserSession, contextIsolation: true, nodeIntegration: false, sandbox: true } });
          BrowserWindow.fromWebContents(target).contentView.addChildView(view);
          view.setVisible(false); state.views.set(target.id, view);
        }
        if (view.webContents.getURL() !== url.href) await view.webContents.loadURL(url.href);
        return { browserId: view.webContents.id };
      }
      case 'host.browserBounds': {
        const view = state.views.get(target.id);
        if (view) {
          if (payload.visible && payload.width > 0 && payload.height > 0) view.setBounds({ x: Math.round(payload.x), y: Math.round(payload.y), width: Math.round(payload.width), height: Math.round(payload.height) });
          view.setVisible(Boolean(payload.visible && payload.width > 0 && payload.height > 0));
        }
        return {};
      }
      case 'host.extractBrowser': {
        const browser = ownedBrowser(target, payload.mode);
        if (!browser || browser.isDestroyed()) throw new Error('请先打开增强程序创建的网页聊天');
        const text = await browser.executeJavaScript('window.getSelection()?.toString() || document.querySelector("main")?.innerText || document.body.innerText');
        return String(text || '').slice(0, 200000);
      }
      default: return nativeAction(message.action, payload);
    }
  }
  const server = http.createServer(async (request, response) => {
    const origin = request.headers.origin || '';
    const trustedPreflight = ['app://renderer','null'].includes(origin) && ['127.0.0.1','::ffff:127.0.0.1','::1'].includes(request.socket.remoteAddress);
    if (request.method === 'OPTIONS') {
      json(response, trustedPreflight ? 204 : 403, {}, trustedPreflight ? origin : 'null');
      return;
    }
    let channel;
    for (const candidate of state.channels.values()) if (candidate.key === request.headers['x-ekd-bridge']) { channel = candidate; break; }
    if (!trustedPreflight || !channel || channel.expectedOrigin !== origin || request.method !== 'POST' || request.url !== '/ekd/native/v1') {
      json(response, 403, { error: { code: 'RPC_DENIED', message: '增强桥来源或凭据无效' } }, origin || 'null');
      return;
    }
    let id = '';
    try {
      const body = JSON.parse(await readBody(request));
      id = typeof body.id === 'string' ? body.id : '';
      if (body.protocol !== 1 || !id || id.length > 100 || typeof body.action !== 'string' || !/^(?:[a-z]+\.)?[a-zA-Z]+$/.test(body.action)) throw new Error('增强桥请求格式无效');
      const result = await dispatch(request.owner, { id, action: body.action, payload: body.payload });
      json(response, 200, { protocol: 1, id, result }, origin);
    } catch (error) {
      json(response, 200, { protocol: 1, id, error: { code: error.code || 'NATIVE_RPC', message: error.message || String(error) } }, origin);
    }
  });
  const originalHandler = server.listeners('request')[0];
  server.removeAllListeners('request');
  server.on('request', (request, response) => {
    let channelEntry;
    const key = request.headers['x-ekd-bridge'];
    for (const [id, candidate] of state.channels) if (candidate.key === key) { channelEntry = { id, candidate }; break; }
    if (request.method === 'POST' && channelEntry) request.owner = webContents.fromId(channelEntry.id);
    if (request.method === 'POST' && !request.owner) {
      json(response, 403, { protocol: 1, id: '', error: { code: 'RPC_OWNER', message: '增强桥窗口已失效' } }, request.headers.origin || 'null');
      return;
    }
    void originalHandler(request, response);
  });
  state.server = server;
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
  state.endpoint = 'http://127.0.0.1:' + server.address().port;
  wc.on('did-finish-load', state.reload = () => {
    if (wc.getURL().startsWith('app://renderer/')) void bindRenderer(wc, 'host').catch(() => {});
  });
  wc.once('destroyed', () => { void state.cleanup(); });
  state.cleanup = async () => {
    if (state.cleaned) return;
    state.cleaned = true;
    coreToken = '';
    if (state.reload) wc.removeListener('did-finish-load', state.reload);
    for (const win of state.windows.values()) if (!win.isDestroyed()) win.destroy();
    state.windows.clear();
    if (state.browserWindow && !state.browserWindow.isDestroyed()) state.browserWindow.destroy();
    for (const view of state.views.values()) if (!view.webContents.isDestroyed()) view.webContents.close();
    state.views.clear();
    state.channels.clear();
    if (server.listening) {
      const closed = new Promise(resolve => server.close(resolve));
      server.closeAllConnections();
      await closed;
    }
    if (!wc.isDestroyed()) await wc.executeJavaScript('window.__ekdNativeUI?.remove()').catch(() => {});
    if (globalThis.__ekdNative === state) delete globalThis.__ekdNative;
  };
  const mounted = await bindRenderer(wc, 'host');
  return { status: mounted.status, transport: 'main-loopback-rpc', protocol: 1, appVersion: app.getVersion(), electron: process.versions.electron };
}
