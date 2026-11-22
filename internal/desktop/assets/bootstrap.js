async function installNativeBootstrap(options) {
  const req = typeof require === 'function' ? require : process.mainModule.require.bind(process.mainModule);
  const { webContents, BrowserWindow, WebContentsView, session, app, dialog, shell, ipcMain } = req('electron');
  const http = req('node:http');
  const crypto = req('node:crypto');
  if (app.getVersion() !== options.expectedApp || process.versions.electron !== options.expectedElectron) throw new Error('宿主版本在接入时发生变化');
  if (options.windowsMainBridge && (typeof BrowserWindow !== 'function' || typeof WebContentsView !== 'function' || typeof ipcMain?.prependListener !== 'function')) {
    throw new Error('Windows 官方宿主缺少插件窗口所需的 Electron 接口');
  }
  if (globalThis.__ekdNative) await globalThis.__ekdNative.cleanup();
  const isMainRendererURL = url => {
    if (typeof url !== 'string' || !url.startsWith('app://renderer/')) return false;
    if (/screenshot|browser-overlay|browser-control/.test(url)) return false;
    const path = url.slice('app://renderer/'.length).split(/[?#]/)[0];
    return path === '' || path === 'index.html' || !path.endsWith('.html');
  };
  // The main window can be closed and reopened without this process exiting
  // (Cmd+W on macOS, then reopening from the Dock). The reference therefore has
  // to be replaceable: the host integration, the loopback server and the core
  // session all outlive one window, and only the per-window state is dropped.
  const findMainContents = () => webContents.getAllWebContents().find(item => !item.isDestroyed() && isMainRendererURL(item.getURL()));
  const initialWC = findMainContents();
  if (!initialWC) throw new Error('隔离主窗口未找到');
  let currentWC = initialWC;
  const mainWindow = () => (currentWC && !currentWC.isDestroyed() ? currentWC : null);
  function requireMainWindow() {
    const live = mainWindow();
    if (!live) throw new Error('增强主窗口尚未打开，请从托盘菜单或 Dock 重新打开 Kimi Code 窗口');
    return live;
  }
  async function pairCore(pairingToken) {
    const permissions = ['core:read','settings:write','models:read','models:write','usage:read','usage:write','tasks:read','tasks:write','chat:read','chat:write','plugins:read','plugins:write','adapter:write','credentials:read','credentials:write','workspace:read','workspace:write','browser:read','browser:write','update:read','update:write'];
    if (process.platform === 'win32') permissions.push('host:electron-main');
    const response = await fetch(new URL('/enhance/v1/pair', options.coreURL), {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ pairingToken, permissions }), signal: AbortSignal.timeout(5000), redirect: 'error',
    });
    const pair = await response.json();
    if (!response.ok || !pair.sessionToken) throw new Error(pair?.error?.message || '增强核心配对失败');
    const expiresAtMs = Date.parse(pair.expiresAt);
    return { sessionToken: pair.sessionToken, expiresAtMs: Number.isFinite(expiresAtMs) ? expiresAtMs : Date.now() + 12 * 60 * 60 * 1000 };
  }
  const initialPair = await pairCore(options.pairingToken);
  let coreToken = initialPair.sessionToken;
  let coreTokenExpiresAtMs = initialPair.expiresAtMs;
  let pairedWith = options.pairingToken;
  let pairing = null;
  const state = { channels: new Map(), windows: new Map(), views: new Map(), pluginViews: new Map(), pluginViewTokens: new Map(), pluginViewOwners: new Map(), pluginViewQueues: new Map(), pluginCommands: new Map(), bounds: new Map(), browserWindow: null, server: null, cleanup: null, overlayEntries: [], activeOverlayMenu: null };
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
  // A session token lasts twelve hours. Renewing it is what keeps an attach
  // alive past that, so the runtime record is the only input: it carries the
  // token of the core that is running now, whether that is the same instance or
  // a replacement after a restart.
  function refreshCoreSession() {
    pairing ??= (async () => {
      if (!options.runtimePath || state.cleaned) return false;
      const runtime = JSON.parse(await req('node:fs/promises').readFile(options.runtimePath, 'utf8'));
      if (runtime.url !== options.coreURL || typeof runtime.pairingToken !== 'string' || runtime.pairingToken.length < 32) return false;
      const refreshed = await pairCore(runtime.pairingToken);
      if (state.cleaned) return false;
      const restarted = pairedWith !== runtime.pairingToken;
      coreToken = refreshed.sessionToken;
      coreTokenExpiresAtMs = refreshed.expiresAtMs;
      if (restarted) {
        // Only a different pairing token means the core is a new instance, and
        // only then is re-injecting the renderer safe. A plain token expiry on
        // the same core must never tear down the windows the user has open.
        pairedWith = runtime.pairingToken;
        setImmediate(rebindRenderers);
      }
      return true;
    })().finally(() => { pairing = null; });
    return pairing;
  }
  async function coreFetch(path, init = {}) {
    const headers = new Headers(init.headers || {});
    headers.set('Accept', 'application/json');
    if (init.body !== undefined && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json');
    const request = token => {
      headers.set('Authorization', 'Bearer ' + token);
      return fetch(new URL(path, options.coreURL), { ...init, headers, signal: AbortSignal.timeout(120000), redirect: 'error' });
    };
    // Renew ahead of the expiry instead of waiting for a failure. This also
    // covers a laptop that slept through the twelve-hour mark: the first request
    // after waking renews before it is sent.
    if (options.runtimePath && !state.cleaned && coreTokenExpiresAtMs > 0 && Date.now() >= coreTokenExpiresAtMs - 15 * 60 * 1000) {
      await refreshCoreSession().catch(error => { console.warn('[EKD] proactive core session renewal failed:', error?.message || String(error)); });
    }
    const originalToken = coreToken;
    let response = await request(originalToken);
    // A 401 TOKEN_EXPIRED is the one renewable failure. A 403 means this
    // credential is live but not scoped for the resource, which renewing cannot
    // fix and would only spend the /pair rate limit on.
    if (response.status !== 401 || !options.runtimePath || state.cleaned) return response;
    const failure = await response.clone().json().catch(() => null);
    if (failure?.error?.code !== 'TOKEN_EXPIRED') return response;
    if (coreToken === originalToken) await refreshCoreSession().catch(error => { console.warn('[EKD] reactive core session renewal failed:', error?.message || String(error)); });
    if (state.cleaned || coreToken === originalToken) return response;
    await response.body?.cancel();
    // Exactly one retry: a renewed token that is still refused is a real
    // authorization failure and must surface to the caller unchanged.
    return request(coreToken);
  }
  async function nativeAction(action, payload) {
    const response = await coreFetch('/enhance/v1/desktop/actions/' + action, { method: 'POST', body: JSON.stringify(payload || {}) });
    const body = await response.json().catch(() => null);
    if (!response.ok) { const error = new Error(body?.error?.message || `核心返回 HTTP ${response.status}`); error.code = body?.error?.code || 'CORE_ERROR'; throw error; }
    return body?.value;
  }
  // A plugin view is owned by one plugin and one view id; both must be present
  // and well-formed before anything touches Electron.
  function pluginViewKey(pluginId, viewId, instanceId) {
    if (typeof pluginId !== 'string' || !/^[a-z0-9][a-z0-9.-]{0,63}$/.test(pluginId)) throw new Error('插件 ID 无效');
    if (typeof viewId !== 'string' || !/^[A-Za-z0-9_-]{1,64}$/.test(viewId)) throw new Error('网页视图 ID 无效');
    if (typeof instanceId !== 'string' || !/^[A-Za-z0-9_-]{1,64}$/.test(instanceId)) throw new Error('网页视图实例 ID 无效');
    return instanceId + '/' + pluginId + '/' + viewId;
  }
  function isBrowserURL(value) {
    try {
      const parsed = new URL(value);
      return (parsed.protocol === 'http:' || parsed.protocol === 'https:') && !parsed.username && !parsed.password && value.length <= 2048;
    } catch { return false; }
  }
  function browserURL(value) {
    const text = String(value || '');
    if (!isBrowserURL(text)) throw new Error('只允许有效的 http 或 https 地址');
    return text;
  }
  // Every plugin browser call is authorized by the core, not by this process.
  // The bridge forwards the plugin's own token and the core verifies, in one
  // step, that the token is live, the plugin is enabled, it holds browser:view
  // and its official digest still matches. The answer names the owner, so a
  // payload can never name one — and a plugin without the capability can never
  // reach Electron at all.
  async function corePluginAction(action, token, params) {
    if (typeof token !== 'string' || token.length < 16 || token.length > 256) throw new Error('插件身份令牌无效');
    const response = await coreFetch('/enhance/v1/plugin-runtime/' + action, { method: 'POST', body: JSON.stringify({ token, params }) });
    const answer = await response.json().catch(() => null);
    if (!response.ok) {
      const error = new Error(answer?.error?.message || `核心返回 HTTP ${response.status}`);
      error.code = answer?.error?.code || 'CORE_ERROR';
      throw error;
    }
    return answer?.value;
  }
  async function coreBrowserAction(action, token, params) {
    const value = await corePluginAction(action, token, params);
    if (!value || typeof value.pluginId !== 'string' || typeof value.viewId !== 'string') throw new Error('核心返回的网页视图归属无效');
    return value;
  }
  // Views are keyed by the owner the core named plus the view id. The token that
  // created each page is recorded next to it, because a plugin being torn down
  // has already lost its token when its release runs: that recorded token is
  // what still proves the page is its own to destroy.
  function dropPluginView(key) {
    const view = state.pluginViews.get(key);
    state.pluginViews.delete(key);
    state.pluginViewTokens.delete(key);
    const ownerId = state.pluginViewOwners.get(key);
    state.pluginViewOwners.delete(key);
    if (!view) return false;
    const contents = webContents.fromId(ownerId);
    const owner = contents && !contents.isDestroyed() ? BrowserWindow.fromWebContents(contents) : null;
    try { owner?.contentView.removeChildView(view); } catch {}
    if (!view.webContents.isDestroyed()) view.webContents.close();
    return true;
  }
  function withPluginViewLock(lockKey, task) {
    const previous = state.pluginViewQueues.get(lockKey) || Promise.resolve();
    const run = previous.catch(() => {}).then(task);
    const settled = run.catch(() => {});
    state.pluginViewQueues.set(lockKey, settled);
    settled.finally(() => {
      if (state.pluginViewQueues.get(lockKey) === settled) state.pluginViewQueues.delete(lockKey);
    });
    return run;
  }
  function destroyPluginViews() {
    for (const key of [...state.pluginViews.keys()]) dropPluginView(key);
  }
  function releaseOwnedPluginViews(contentsId) {
    const pending = [];
    for (const [key, owner] of [...state.pluginViewOwners]) {
      if (owner !== contentsId) continue;
      const token = state.pluginViewTokens.get(key);
      const slash = key.indexOf('/');
      const secondSlash = slash >= 0 ? key.indexOf('/', slash + 1) : -1;
      const instanceId = slash > 0 ? key.slice(0, slash) : '';
      const viewId = secondSlash > slash ? key.slice(secondSlash + 1) : '';
      dropPluginView(key);
      if (token && instanceId && viewId) {
        pending.push(coreBrowserAction('browser.close', token, { viewId, instanceId }).catch(() => {}));
      }
    }
    return Promise.all(pending);
  }
  // Reading a plugin page works on the page it is actually on, not on the
  // address core recorded when the view was opened. A sign-in redirect or an
  // in-page route change moves the plugin's own view, and the plugin owns that
  // page — browser:view already lets it navigate anywhere http(s). The check
  // that still matters is the scheme: a view that ended up on file:, data: or
  // javascript: has left the web and must not be read.
  function currentPluginPage(view) {
    const current = String(view.webContents.getURL() || '');
    if (!isBrowserURL(current)) throw new Error('插件网页视图已离开 http 或 https 页面');
    return current;
  }
  // Resolves the Electron view the core just authorized. A view the core knows
  // about but this process never built is reported missing rather than created
  // here: only browser.view.open may create one.
  function livePluginView(answer, target) {
    const instanceId = state.channels.get(target.id)?.instanceId || '';
    if (answer.instanceId !== instanceId) throw new Error('网页视图实例与宿主窗口不匹配');
    const key = pluginViewKey(answer.pluginId, answer.viewId, instanceId);
    const view = state.pluginViews.get(key);
    if (!view || view.webContents.isDestroyed()) throw new Error('插件网页视图不存在');
    return { key, view };
  }
  function ownedBrowser(target, mode) { return mode === 'exec' ? state.browserWindow?.webContents : state.views.get(target.id)?.webContents; }
  // A site adapter is a function body the plugin wrote, run once against its own
  // page. It is evaluated as an expression so a body that is not a function
  // throws instead of running as a statement, and its answer is serialized to
  // JSON here: the caller receives data, never a live object it could use to
  // reach the page or this process.
  async function runSiteAdapter(contents, adapter) {
    const source = typeof adapter?.source === 'string' ? adapter.source : '';
    if (!source) throw new Error('站点适配器缺少函数体');
    const args = JSON.stringify(adapter?.args ?? null);
    const expression = [
      '(() => {',
      'const run = (' + source + ');',
      'if (typeof run !== "function") throw new Error("站点适配器必须是函数表达式");',
      'return Promise.resolve(run(' + args + '));',
      '})()',
    ].join('\n');
    const value = await contents.executeJavaScript(expression, true);
    const text = JSON.stringify(value === undefined ? null : value);
    if (typeof text !== 'string') throw new Error('站点适配器返回值无法序列化为 JSON');
    if (text.length > 200000) throw new Error('站点适配器返回值超过 200000 字符');
    return JSON.parse(text);
  }
  async function openWindow(kind, source = {}) {
    const existing = state.windows.get(kind);
    if (existing && !existing.isDestroyed()) {
      existing.show(); existing.focus();
      return { focused: true };
    }
    const theme = await requireMainWindow().executeJavaScript('document.documentElement.dataset.colorScheme||"dark"');
    const previous = state.bounds.get(kind);
    const plugin = kind.startsWith('plugin:');
    const win = new BrowserWindow({
      title: 'KimiCode+ 插件',
      width: previous?.width || 640,
      height: previous?.height || 840,
      x: previous?.x, y: previous?.y,
      minWidth: 400, minHeight: 500,
      show: false, autoHideMenuBar: true, backgroundColor: theme === 'dark' ? '#121212' : '#ffffff',
      webPreferences: { contextIsolation: true, nodeIntegration: false, sandbox: true, devTools: false },
    });
    state.windows.set(kind, win);
    const contentsId = win.webContents.id;
    let closing = false;
    win.on('close', event => {
      if (!win.isDestroyed()) state.bounds.set(kind, win.getBounds());
      if (!plugin || closing) return;
      closing = true;
      event.preventDefault();
      void win.webContents.executeJavaScript('window.__ekdNativeUI?.remove()')
        .catch(() => {})
        .then(() => Promise.race([releaseOwnedPluginViews(contentsId), new Promise(resolve => setTimeout(resolve, 250))]))
        .finally(() => { if (!win.isDestroyed()) win.destroy(); });
    });
    win.on('closed', () => {
      state.channels.delete(contentsId);
      for (const [key, owner] of [...state.pluginCommands]) if (owner === contentsId) state.pluginCommands.delete(key);
      if (state.windows.get(kind) === win) state.windows.delete(kind);
      releaseOwnedPluginViews(contentsId);
    });
    win.webContents.setWindowOpenHandler(() => ({ action: 'deny' }));
    // The window only ever hosts the injected renderer on about:blank. Any
    // other navigation would put an external page inside a window that carries
    // a bridge key, so it is refused instead of followed.
    win.webContents.on('will-navigate', (event, url) => {
      if (url !== 'about:blank') event.preventDefault();
    });
    await win.loadURL('about:blank');
    await bindRenderer(win.webContents, 'plugin', { popout: true, theme, pluginId: source.pluginId, sourceSessionId: source.sourceSessionId || '' });
    win.show(); win.focus();
    return { opened: true };
  }
  async function bindRenderer(target, surface, extra = {}) {
    for (const [key, owner] of [...state.pluginCommands]) if (owner === target.id) state.pluginCommands.delete(key);
    releaseOwnedPluginViews(target.id);
    const key = crypto.randomBytes(32).toString('base64url');
    const instanceId = crypto.randomBytes(16).toString('base64url');
    state.channels.set(target.id, { key, instanceId, expectedOrigin: target === currentWC ? 'app://renderer' : 'null' });
    const config = {
      endpoint: state.endpoint, bridgeKey: key, instanceId, moduleSource: options.moduleSource, cssSource: options.cssSource,
      coreURL: options.windowsMainBridge ? options.coreURL : '',
      appVersion: options.reportedApp || app.getVersion(), electron: process.versions.electron,
      surface, ...extra,
    };
    return target.executeJavaScript('(' + options.rendererSource + ')(' + JSON.stringify(config) + ')');
  }
  function rebindRenderers() {
    const target = mainWindow();
    if (state.cleaned || !target) return;
    destroyPluginViews();
    for (const win of state.windows.values()) if (!win.isDestroyed()) win.close();
    void bindRenderer(target, 'host').catch(error => console.warn('KimiCode+ host reconnection failed', error.message));
  }
  async function dispatch(target, message) {
    const payload = message.payload || {};
    if (message.action.startsWith('browser.view.')) payload.instanceId = state.channels.get(target.id)?.instanceId || '';
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
      case 'window.openSettings': return requireMainWindow().executeJavaScript('window.__ekdNativeUI.openSettings(' + JSON.stringify(typeof payload.tab === 'string' ? payload.tab : '') + ')');
      case 'plugin.window.open': {
        const answer = await corePluginAction('ui.popout', payload.token, {});
        if (typeof answer?.pluginId !== 'string' || !/^[a-z0-9][a-z0-9.-]{0,63}$/.test(answer.pluginId)) throw new Error('核心返回的插件窗口归属无效');
        return openWindow('plugin:' + answer.pluginId, { pluginId: answer.pluginId, sourceSessionId: typeof payload.sourceSessionId === 'string' ? payload.sourceSessionId : '' });
      }
      case 'window.close': BrowserWindow.fromWebContents(target)?.close(); return {};
      case 'window.ensureWidth': {
        const win = BrowserWindow.fromWebContents(target);
        if (!win) return {};
        const size = win.getSize(); const width = Math.max(Number(payload.width) || 0, size[0]);
        if (width > size[0]) win.setSize(Math.min(width, 3000), size[1]);
        return {};
      }
      case 'host.openSession': return requireMainWindow().executeJavaScript('window.__ekdNativeUI.openOfficial(' + JSON.stringify(payload.id) + ')');
      case 'host.pickSession': return requireMainWindow().executeJavaScript('window.__ekdNativeUI.pickSession(' + JSON.stringify(payload.exclude || []) + ')');
      case 'host.activateSession': return requireMainWindow().executeJavaScript('window.__ekdNativeUI.activateSession(' + JSON.stringify(payload.id) + ')');
      case 'host.syncOverlayEntries': {
        if (target !== currentWC) return {};
        state.overlayEntries = sanitizeOverlayEntries(payload.entries);
        if (state.activeOverlayMenu) {
          const ov = webContents.getAllWebContents().find(item => !item.isDestroyed() && item.getURL().includes('browser-overlay'));
          if (ov) {
            const activeActions = state.overlayEntries.map(entry => entry.action);
            void ov.executeJavaScript('(() => { const keep = ' + JSON.stringify(activeActions) + '; for (const row of document.querySelectorAll(".overlay-menu-row[data-ekd-entry]")) { if (!keep.includes(row.dataset.ekdEntry)) row.closest(".overlay-menu-row-group")?.remove(); } })()').catch(() => {});
          }
        }
        return { count: state.overlayEntries.length };
      }
      case 'host.pickFiles': {
        const dialogOptions = {
          title: typeof payload?.title === 'string' && payload.title.trim() ? payload.title.trim() : '添加附件',
          properties: payload?.single ? ['openFile'] : ['openFile', 'multiSelections'],
        };
        if (Array.isArray(payload?.filters) && payload.filters.length > 0) {
          dialogOptions.filters = payload.filters;
        }
        const result = await dialog.showOpenDialog(BrowserWindow.fromWebContents(target), dialogOptions);
        return result.canceled ? [] : result.filePaths.map(path => ({ path, name: req('node:path').basename(path) }));
      }
      case 'host.pickDirectory': {
        const dialogOptions = {
          title: typeof payload.title === 'string' && payload.title.trim() ? payload.title.trim() : '选择项目文件夹',
          properties: ['openDirectory', 'createDirectory'],
        };
        if (typeof payload.defaultPath === 'string' && payload.defaultPath.trim()) {
          dialogOptions.defaultPath = payload.defaultPath.trim();
        }
        const result = await dialog.showOpenDialog(BrowserWindow.fromWebContents(target), dialogOptions);
        return { path: result.canceled ? '' : (result.filePaths[0] || '') };
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
      // Plugin-owned views. Each action starts at the core: the plugin's token
      // is authorized for browser:view, the core answers with the owner and the
      // canonical URL, and only then does this process touch Electron.
      case 'browser.view.open': {
        return withPluginViewLock(String(payload.instanceId || '') + '/' + String(payload.viewId || ''), async () => {
          const answer = await coreBrowserAction('browser.open', payload.token, { viewId: payload.viewId, instanceId: payload.instanceId, url: payload.url });
          const key = pluginViewKey(answer.pluginId, answer.viewId, answer.instanceId);
          const url = browserURL(answer.url);
          let view = state.pluginViews.get(key);
          let created = false;
          if (!view) {
            const owner = target.isDestroyed() ? null : BrowserWindow.fromWebContents(target);
            if (!owner || state.channels.get(target.id)?.instanceId !== answer.instanceId) {
              // The core now believes the view exists; without a live owner
              // window here it cannot, so the registry is put back the way it was.
              await coreBrowserAction('browser.close', payload.token, { viewId: answer.viewId, instanceId: answer.instanceId }).catch(() => {});
              throw new Error('插件网页视图缺少宿主窗口');
            }
            view = new WebContentsView({ webPreferences: { session: browserSession, contextIsolation: true, nodeIntegration: false, sandbox: true } });
            owner.contentView.addChildView(view);
            view.setVisible(false);
            view.webContents.setWindowOpenHandler(() => ({ action: 'deny' }));
            view.webContents.on('will-navigate', (event, next) => {
              if (!isBrowserURL(next)) event.preventDefault();
            });
            const pushNavigate = nextUrl => {
              if (!isBrowserURL(nextUrl)) return;
              const ownerContents = webContents.fromId(state.pluginViewOwners.get(key));
              if (!ownerContents || ownerContents.isDestroyed()) return;
              const event = { pluginId: answer.pluginId, instanceId: answer.instanceId, viewId: answer.viewId, url: String(nextUrl) };
              ownerContents.executeJavaScript('window.__ekdPluginBrowserNavigated?.(' + JSON.stringify(event) + ')').catch(() => {});
            };
            view.webContents.on('did-navigate', (_event, nextUrl) => pushNavigate(nextUrl));
            view.webContents.on('did-navigate-in-page', (_event, nextUrl) => pushNavigate(nextUrl));
            state.pluginViews.set(key, view);
            state.pluginViewTokens.set(key, payload.token);
            state.pluginViewOwners.set(key, target.id);
            created = true;
          }
          try {
            if (view.webContents.getURL() !== url) await view.webContents.loadURL(url);
            if (target.isDestroyed() || state.channels.get(target.id)?.instanceId !== answer.instanceId) {
              throw new Error('插件网页视图宿主窗口已关闭');
            }
          } catch (error) {
            // A page that never loaded leaves nothing usable behind: the Electron
            // view goes away and the core record is released, so a retry starts
            // from a clean state instead of reusing a broken view.
            dropPluginView(key);
            await coreBrowserAction('browser.close', payload.token, { viewId: answer.viewId, instanceId: answer.instanceId }).catch(() => {});
            throw error;
          }
          if (!created && state.pluginViewTokens.get(key) !== payload.token) state.pluginViewTokens.set(key, payload.token);
          return { browserId: view.webContents.id, pluginId: answer.pluginId, instanceId: answer.instanceId, viewId: answer.viewId, url: answer.url };
        });
      }
      case 'browser.view.navigate': {
        return withPluginViewLock(String(payload.instanceId || '') + '/' + String(payload.viewId || ''), async () => {
          const answer = await coreBrowserAction('browser.navigate', payload.token, { viewId: payload.viewId, instanceId: payload.instanceId, url: payload.url });
          const { key, view } = livePluginView(answer, target);
          const url = browserURL(answer.url);
          if (view.webContents.getURL() !== url) await view.webContents.loadURL(url);
          state.pluginViewTokens.set(key, payload.token);
          return { pluginId: answer.pluginId, viewId: answer.viewId, url: answer.url };
        });
      }
      case 'browser.view.history': {
        if (!['back','forward','reload'].includes(payload.operation)) throw new Error('网页导航动作无效');
        const answer = await coreBrowserAction('browser.' + payload.operation, payload.token, { viewId: payload.viewId, instanceId: payload.instanceId });
        const { key, view } = livePluginView(answer, target);
        currentPluginPage(view);
        let changed = true;
        if (payload.operation === 'back') {
          changed = view.webContents.canGoBack();
          if (changed) view.webContents.goBack();
        } else if (payload.operation === 'forward') {
          changed = view.webContents.canGoForward();
          if (changed) view.webContents.goForward();
        } else view.webContents.reload();
        state.pluginViewTokens.set(key, payload.token);
        return { pluginId: answer.pluginId, viewId: answer.viewId, changed };
      }
      case 'browser.view.external': {
        const answer = await coreBrowserAction('browser.openExternal', payload.token, { viewId: payload.viewId, instanceId: payload.instanceId });
        const { key, view } = livePluginView(answer, target);
        const url = currentPluginPage(view);
        await shell.openExternal(url);
        state.pluginViewTokens.set(key, payload.token);
        return { pluginId: answer.pluginId, viewId: answer.viewId, url };
      }
      case 'browser.view.bounds': {
        const visible = Boolean(payload.visible && payload.width > 0 && payload.height > 0);
        const params = { viewId: payload.viewId, instanceId: payload.instanceId, visible, x: payload.x, y: payload.y, width: payload.width, height: payload.height };
        const answer = await coreBrowserAction(visible ? 'browser.bounds' : 'browser.visible', payload.token, params);
        const { key, view } = livePluginView(answer, target);
        if (visible) view.setBounds({ x: Math.round(payload.x), y: Math.round(payload.y), width: Math.round(payload.width), height: Math.round(payload.height) });
        view.setVisible(visible);
        state.pluginViewTokens.set(key, payload.token);
        return { pluginId: answer.pluginId, viewId: answer.viewId, visible: Boolean(answer.visible) };
      }
      case 'browser.view.extract': {
        const answer = await coreBrowserAction('browser.extract', payload.token, { viewId: payload.viewId, instanceId: payload.instanceId });
        const { key, view } = livePluginView(answer, target);
        currentPluginPage(view);
        const text = await view.webContents.executeJavaScript('window.getSelection()?.toString() || document.querySelector("main")?.innerText || document.body.innerText');
        state.pluginViewTokens.set(key, payload.token);
        return String(text || '').slice(0, 200000);
      }
      case 'browser.view.adapter': {
        const answer = await coreBrowserAction('browser.adapter', payload.token, { viewId: payload.viewId, instanceId: payload.instanceId, adapter: payload.adapter });
        const { key, view } = livePluginView(answer, target);
        currentPluginPage(view);
        const result = await runSiteAdapter(view.webContents, answer.adapter);
        state.pluginViewTokens.set(key, payload.token);
        return result;
      }
      case 'browser.view.close': {
        if (typeof payload.viewId !== 'string' || !/^[A-Za-z0-9_-]{1,64}$/.test(payload.viewId)) throw new Error('网页视图 ID 无效');
        const recorded = [...state.pluginViewTokens].find(([candidate, token]) => token === payload.token && candidate.startsWith(payload.instanceId + '/') && candidate.endsWith('/' + payload.viewId) && state.pluginViewOwners.get(candidate) === target.id);
        // Interrupt an owned load before joining its queue; a stalled navigation
        // must not prevent teardown, including after the core revokes the token.
        const interrupted = recorded ? dropPluginView(recorded[0]) : false;
        return withPluginViewLock(String(payload.instanceId || '') + '/' + String(payload.viewId || ''), async () => {
          let key;
          try {
            const answer = await coreBrowserAction('browser.close', payload.token, { viewId: payload.viewId, instanceId: payload.instanceId });
            key = pluginViewKey(answer.pluginId, answer.viewId, answer.instanceId);
          } catch (error) {
            if (!recorded) throw error;
            key = recorded[0];
          }
          return { closed: dropPluginView(key) || interrupted };
        });
      }
      case 'plugin.command.register': {
        const identity = await corePluginAction('runtime.info', payload.token, {});
        const answer = await corePluginAction('commands.list', payload.token, {});
        const command = answer?.items?.find(item => item?.name === payload.name && item?.pluginId === identity?.id);
        if (!command || typeof command.pluginId !== 'string') throw new Error('核心没有确认这个命令属于当前插件');
        state.pluginCommands.set(command.pluginId + ':' + command.name, target.id);
        return { registered: true, ownerId: command.pluginId, command: command.name };
      }
      case 'plugin.command.unregister': {
        await corePluginAction('commands.list', payload.token, {});
        let removed = false;
        for (const [key, owner] of [...state.pluginCommands]) {
          if (owner === target.id && key.endsWith(':' + payload.name)) {
            state.pluginCommands.delete(key);
            removed = true;
          }
        }
        return { removed };
      }
      case 'plugin.command.invoke': {
        const answer = await corePluginAction('commands.invoke', payload.token, { command: payload.name, args: payload.args ?? null });
        const key = answer?.ownerId + ':' + answer?.command;
        const ownerId = state.pluginCommands.get(key);
        const owner = webContents.fromId(ownerId);
        if (!owner || owner.isDestroyed()) {
          const error = new Error('命令所属插件没有可执行的宿主窗口');
          error.code = 'PLUGIN_COMMAND_UNAVAILABLE';
          throw error;
        }
        const targetCommand = { command: answer.command, ownerId: answer.ownerId, args: answer.args ?? null };
        const value = await owner.executeJavaScript('window.__ekdPlugins ? window.__ekdPlugins.invokeCommand(' + JSON.stringify(targetCommand) + ') : Promise.reject(new Error("命令所属插件的宿主尚未就绪"))');
        return value === undefined ? null : value;
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
      // Plugin-owned views are addressed over the bridge as namespaced verbs
      // (`browser.view.open`), so the action grammar allows dotted prefixes
      // rather than a single one.
      if (body.protocol !== 1 || !id || id.length > 100 || typeof body.action !== 'string' || body.action.length > 80 || !/^(?:[a-z]+\.){0,4}[a-zA-Z][a-zA-Z0-9]*$/.test(body.action)) throw new Error('增强桥请求格式无效');
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
  const OVERLAY_ICON_PATH = /^[MmLlHhVvCcSsQqTtAaZz0-9eE ,.+-]+$/;
  const PANEL_ADD_ITEM_IDS = new Set(['diff', 'browser', 'term', 'files', 'btw', 'inspector']);
  function sanitizeOverlayEntries(list) {
    if (!Array.isArray(list)) return [];
    const out = [];
    for (const item of list.slice(0, 16)) {
      if (!item || typeof item.action !== 'string' || !item.action || item.action.length > 128) continue;
      if (typeof item.title !== 'string' || !item.title.trim() || item.title.length > 80) continue;
      const icon = typeof item.icon === 'string' && OVERLAY_ICON_PATH.test(item.icon) ? item.icon : '';
      out.push({ action: item.action, title: item.title.trim(), icon });
    }
    return out;
  }
  function isPanelAddMenuPayload(payload) {
    if (!payload || payload.placement !== 'bottom-end' || payload.width !== 220 || !Array.isArray(payload.items) || payload.items.length === 0) return false;
    return payload.items.every(item => item && typeof item.id === 'string' && PANEL_ADD_ITEM_IDS.has(item.id));
  }
  function patchOverlayIcons(ov, menu) {
    if (!ov || ov.isDestroyed() || !menu?.entries?.length) return;
    const expr = `(() => {
      const rows = [...document.querySelectorAll('.browser-overlay-list .overlay-menu-row')];
      const entries = ${JSON.stringify(menu.entries)};
      const start = ${Number(menu.officialCount)};
      for (let i = 0; i < entries.length; i++) {
        const row = rows[start + i];
        const entry = entries[i];
        if (!row || !entry) continue;
        row.dataset.ekdEntry = entry.action;
        const svg = row.querySelector('.overlay-menu-icon svg');
        if (svg && entry.icon) {
          svg.setAttribute('viewBox', '0 0 24 24');
          svg.setAttribute('fill', 'none');
          svg.setAttribute('stroke', 'currentColor');
          svg.setAttribute('stroke-width', '1.6');
          svg.setAttribute('stroke-linecap', 'round');
          svg.setAttribute('stroke-linejoin', 'round');
          svg.innerHTML = '<path d="' + entry.icon + '"></path>';
        }
      }
      return rows.length;
    })()`;
    void ov.executeJavaScript(expr).catch(() => {});
  }
  const onOverlayOpen = (event, payload) => {
    const host = mainWindow();
    if (!host || event.sender !== host || !state.overlayEntries.length || !isPanelAddMenuPayload(payload)) {
      if (host && event.sender === host) state.activeOverlayMenu = null;
      return;
    }
    const officialCount = payload.items.length;
    const entries = state.overlayEntries.slice();
    state.activeOverlayMenu = { menuId: payload.menuId, officialCount, entries };
    for (const entry of entries) {
      payload.items.push({ kind: 'item', id: 'ekd:' + entry.action, label: entry.title, icon: 'message' });
    }
  };
  const onOverlayRendered = (event) => {
    if (!state.activeOverlayMenu || !event?.sender || event.sender.isDestroyed()) return;
    if (!String(event.sender.getURL?.() || '').includes('browser-overlay')) return;
    patchOverlayIcons(event.sender, state.activeOverlayMenu);
  };
  const onOverlayActivate = (event, payload) => {
    if (!payload || typeof payload.itemId !== 'string' || !payload.itemId.startsWith('ekd:')) return;
    const action = payload.itemId.slice(4);
    const known = state.overlayEntries.some(entry => entry.action === action)
      || state.activeOverlayMenu?.entries?.some(entry => entry.action === action);
    state.activeOverlayMenu = null;
    const host = mainWindow();
    if (!known || !host) return;
    void host.executeJavaScript('window.__ekdNativeUI?.invokeEntry?.(' + JSON.stringify(action) + ')').catch(() => {});
  };
  const onOverlayClosed = () => { state.activeOverlayMenu = null; };
  if (ipcMain && typeof ipcMain.prependListener === 'function') {
    ipcMain.prependListener('kimi:browser-overlay-open', onOverlayOpen);
    ipcMain.on('kimi:browser-overlay-rendered', onOverlayRendered);
    ipcMain.prependListener('kimi:browser-overlay-activate', onOverlayActivate);
    ipcMain.on('kimi:browser-overlay-hidden', onOverlayClosed);
    ipcMain.on('kimi:browser-overlay-dismiss', onOverlayClosed);
  }
  state.server = server;
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
  state.endpoint = 'http://127.0.0.1:' + server.address().port;
  // One window's listeners. They are installed per window and removed with that
  // same window, so the reopen path cannot accumulate handlers across windows.
  function unattachMainWebContents(contents) {
    if (!contents) return;
    // removeListener stays valid after destroyed, so the persistent reload
    // handler is dropped here even when teardown follows a closed window.
    try { contents.removeListener('did-finish-load', onMainDidFinishLoad); } catch {}
    try { contents.removeListener('destroyed', onMainDestroyed); } catch {}
  }
  // Closing only the main window must not tear the integration down: the
  // loopback server, the core session and the app listeners outlive it. Only the
  // state that belonged to that window goes away, so a reopened window starts
  // from a clean slate without a second integration or a leaked channel.
  function dropMainWindowState(contentsId) {
    state.channels.delete(contentsId);
    for (const [key, owner] of [...state.pluginCommands]) if (owner === contentsId) state.pluginCommands.delete(key);
    state.overlayEntries = [];
    state.activeOverlayMenu = null;
    void releaseOwnedPluginViews(contentsId);
    const view = state.views.get(contentsId);
    if (view) {
      if (!view.webContents.isDestroyed()) view.webContents.close();
      state.views.delete(contentsId);
    }
  }
  const onMainDidFinishLoad = () => {
    const contents = currentWC;
    if (!contents || contents.isDestroyed()) return;
    if (contents.getURL().startsWith('app://renderer/')) void bindRenderer(contents, 'host').catch(() => {});
  };
  const onMainDestroyed = () => {
    const contents = currentWC;
    if (!contents) return;
    unattachMainWebContents(contents);
    currentWC = null;
    if (!state.cleaned) dropMainWindowState(contents.id);
  };
  function attachMainWebContents(next) {
    if (!next || next.isDestroyed()) throw new Error('增强主窗口尚未打开，请从托盘菜单或 Dock 重新打开 Kimi Code 窗口');
    currentWC = next;
    next.on('did-finish-load', onMainDidFinishLoad);
    next.once('destroyed', onMainDestroyed);
  }
  // macOS keeps the process alive after Cmd+W, and reopening from the Dock
  // creates a brand-new webContents. Nothing else re-injects the host renderer,
  // so the reopened window stayed unenhanced forever; this listener notices the
  // replacement and binds it like the first window.
  // Electron passes the event first, the new webContents second.
  const onWebContentsCreated = (_event, contents) => {
    if (state.cleaned || !contents || typeof contents.once !== 'function') return;
    const adopt = () => {
      if (state.cleaned || mainWindow()) return;
      if (!isMainRendererURL(contents.getURL())) return;
      try {
        attachMainWebContents(contents);
        void bindRenderer(contents, 'host').catch(error => console.warn('KimiCode+ host reconnection failed', error.message));
      } catch {}
    };
    contents.once('did-finish-load', adopt);
  };
  const onBeforeQuit = () => { void state.cleanup(); };
  if (typeof app?.on === 'function') app.on('web-contents-created', onWebContentsCreated);
  if (typeof app?.once === 'function') app.once('before-quit', onBeforeQuit);
  state.cleanup = async () => {
    if (state.cleaned) return;
    state.cleaned = true;
    coreToken = '';
    if (typeof app?.removeListener === 'function') {
      app.removeListener('web-contents-created', onWebContentsCreated);
      app.removeListener('before-quit', onBeforeQuit);
    }
    if (ipcMain && typeof ipcMain.removeListener === 'function') {
      ipcMain.removeListener('kimi:browser-overlay-open', onOverlayOpen);
      ipcMain.removeListener('kimi:browser-overlay-rendered', onOverlayRendered);
      ipcMain.removeListener('kimi:browser-overlay-activate', onOverlayActivate);
      ipcMain.removeListener('kimi:browser-overlay-hidden', onOverlayClosed);
      ipcMain.removeListener('kimi:browser-overlay-dismiss', onOverlayClosed);
    }
    state.overlayEntries = [];
    state.activeOverlayMenu = null;
    const closing = currentWC;
    unattachMainWebContents(closing);
    currentWC = null;
    for (const win of state.windows.values()) if (!win.isDestroyed()) win.destroy();
    state.windows.clear();
    if (state.browserWindow && !state.browserWindow.isDestroyed()) state.browserWindow.destroy();
    for (const view of state.views.values()) if (!view.webContents.isDestroyed()) view.webContents.close();
    state.views.clear();
    destroyPluginViews();
    state.pluginViewQueues.clear();
    state.pluginCommands.clear();
    state.channels.clear();
    if (server.listening) {
      const closed = new Promise(resolve => server.close(resolve));
      server.closeAllConnections();
      await closed;
    }
    if (closing && !closing.isDestroyed()) await closing.executeJavaScript('window.__ekdNativeUI?.remove()').catch(() => {});
    if (globalThis.__ekdNative === state) delete globalThis.__ekdNative;
  };
  attachMainWebContents(initialWC);
  const mounted = await bindRenderer(initialWC, 'host');
  return { status: mounted.status, transport: 'main-loopback-rpc', protocol: 1, appVersion: app.getVersion(), electron: process.versions.electron };
}
