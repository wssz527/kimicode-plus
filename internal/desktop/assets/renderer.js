async function installNativeRenderer(options) {
  const freeze = Object.freeze;
  const installCtrl = (window.__ekdNativeInstall ??= { seq: 0, chain: Promise.resolve() });
  const mySeq = ++installCtrl.seq;
  const isStale = () => installCtrl.seq !== mySeq;
  const prevUI = window.__ekdNativeUI;
  let activeRemove = null;
  let publishedUI = null;
  let publishedPlugins = null;
  let publishedBrowserNav = null;
  const prevRemove = prevUI && typeof prevUI.remove === 'function'
    ? Promise.resolve().then(() => prevUI.remove()).catch(() => {})
    : Promise.resolve();
  const teardownDone = installCtrl.chain.then(() => prevRemove);
  installCtrl.chain = teardownDone.catch(() => {});
  const pendingUI = freeze({
    remove: async () => {
      if (installCtrl.seq === mySeq) installCtrl.seq += 1;
      await teardownDone;
      if (typeof activeRemove === 'function') await activeRemove();
    },
  });
  window.__ekdNativeUI = pendingUI;
  await teardownDone;
  if (isStale()) return { status: 'removed', surface: options.surface || 'host', domRevision: 1 };
  // Third-party plugins share this page and can replace globals. Pin the
  // primitives the host depends on before any enhancement or plugin module
  // runs: a plugin that patches window.fetch could otherwise read the
  // X-EKD-Bridge credential off every host request.
  const nativeFetch = window.fetch.bind(window);
  const nativeStringify = JSON.stringify;
  const nativeParse = JSON.parse;
  const windowsLoopback = options.transport === 'windows-cdp-loopback';
  // A bridge 401/403 means the host-side channel was rebound or its key rotated,
  // not that the official Kimi account lost its credentials. Keep the two apart
  // in the code and in the message the user sees.
  const bridgeCredentialCodes = new Set(['TOKEN_EXPIRED', 'BRIDGE_SESSION_EXPIRED', 'RPC_DENIED', 'RPC_OWNER', 'PAIRING_EXPIRED']);
  const bridgeCredentialMessage = '增强核心本地通信凭据已刷新或正在重新连接；你的官方 Kimi 账号登录未受影响，请稍后重试。';
  // Only these mean the request never reached the action: the bridge itself
  // refused it. Anything else with a semantic code is a real answer from the
  // core (for example PLUGIN_PRIVILEGE_DENIED) and must keep its own message
  // and must never be retried, because the action may already have run.
  const bridgeRejectedBeforeDispatch = code => !code || code === 'RPC_DENIED' || code === 'RPC_OWNER' || code.startsWith('HTTP_');
  function bridgeCredentialFailure(status, code) {
    return bridgeCredentialCodes.has(code) || ((status === 401 || status === 403) && bridgeRejectedBeforeDispatch(code));
  }
  async function bridgeRequest(action, payload) {
    const id = typeof globalThis.crypto?.randomUUID === 'function'
      ? globalThis.crypto.randomUUID()
      : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}-${Math.random().toString(36).slice(2)}`;
    let response;
    let body;
    for (let attempt = 0; attempt < 2; attempt += 1) {
      try {
        response = await nativeFetch(options.endpoint + '/ekd/native/v1', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'X-EKD-Bridge': options.bridgeKey },
          body: nativeStringify({ protocol: 1, id, action, payload }),
        });
        body = await response.json().catch(() => null);
      } catch (error) {
        // A restarted core refuses the connection for a moment; that is the same
        // retryable window as a rotated bridge key, not a terminal failure.
        if (attempt === 1) {
          const wrapped = new Error(bridgeCredentialMessage);
          wrapped.code = 'BRIDGE_SESSION_EXPIRED';
          wrapped.status = 0;
          wrapped.retryable = true;
          wrapped.cause = error;
          throw wrapped;
        }
        await new Promise(resolve => setTimeout(resolve, 250));
        continue;
      }
      const code = body?.error?.code || (!response.ok ? `HTTP_${response.status}` : '');
      const refused = (!response.ok || body?.error) && bridgeRejectedBeforeDispatch(code);
      if (!refused || attempt === 1) break;
      // A rotated bridge key or a rebinding window is visible for a few hundred
      // milliseconds. One retry rides that out instead of surfacing an error
      // that reads like the official account credentials were lost.
      await new Promise(resolve => setTimeout(resolve, 250));
    }
    if (!response.ok || body?.error) {
      const code = body?.error?.code || `HTTP_${response.status}`;
      const error = new Error(body?.error?.message || `增强桥返回 HTTP ${response.status}`);
      error.code = code;
      error.status = response.status;
      if (bridgeCredentialFailure(response.status, code)) {
        error.message = bridgeCredentialMessage;
        error.code = 'BRIDGE_SESSION_EXPIRED';
        error.retryable = true;
      }
      throw error;
    }
    if (body?.protocol !== 1 || body?.id !== id) throw new Error('增强桥响应标识不匹配');
    return body?.result;
  }
  const transport = {
    async request(action, payload = {}) {
      if (windowsLoopback) {
        if (action === 'window.openSettings') {
          window.__ekdNativeUI?.openSettings?.(typeof payload?.tab === 'string' ? payload.tab : '');
          return {};
        }
        if (action === 'host.openSession') {
          window.__ekdNativeUI?.openOfficial?.(payload?.id);
          return {};
        }
        if (action === 'host.activateSession') {
          window.__ekdNativeUI?.activateSession?.(payload?.id);
          return {};
        }
        if (action === 'host.pickSession') {
          return window.__ekdNativeUI?.pickSession?.(payload?.exclude || []);
        }
        if (action === 'host.pickDirectory' && typeof window.kimiDesktop?.showOpenDialog === 'function') {
          const res = await window.kimiDesktop.showOpenDialog({
            title: typeof payload?.title === 'string' && payload.title.trim() ? payload.title.trim() : '选择项目文件夹',
            properties: ['openDirectory', 'createDirectory'],
            defaultPath: typeof payload?.defaultPath === 'string' && payload.defaultPath.trim() ? payload.defaultPath.trim() : undefined,
          });
          return { path: res?.canceled ? '' : (res?.filePaths?.[0] || '') };
        }
        if (action === 'host.pickFiles' && typeof window.kimiDesktop?.showOpenDialog === 'function') {
          const dialogOptions = {
            title: typeof payload?.title === 'string' && payload.title.trim() ? payload.title.trim() : '添加附件',
            properties: payload?.single ? ['openFile'] : ['openFile', 'multiSelections'],
          };
          if (Array.isArray(payload?.filters) && payload.filters.length > 0) {
            dialogOptions.filters = payload.filters;
          }
          const res = await window.kimiDesktop.showOpenDialog(dialogOptions);
          return res?.canceled ? [] : (res?.filePaths || []).map(p => ({ path: p, name: String(p).split(/[\\/]/).pop() || p }));
        }
        if (action === 'host.openBrowser') {
          // Compatibility mode has no Electron main bridge, so a popover link or
          // a release page must open in the OS browser instead of failing.
          const url = typeof payload?.url === 'string' ? payload.url : '';
          if (!/^https?:\/\//i.test(url)) throw new Error('只允许有效的 http 或 https 地址');
          const opened = typeof window.open === 'function' ? window.open(url, '_blank', 'noopener,noreferrer') : null;
          return { browserId: opened ? 'external' : '' };
        }
      }
      return bridgeRequest(action, payload);
    },
  };
  const moduleURL = URL.createObjectURL(new Blob([options.moduleSource], { type: 'text/javascript' }));
  let module;
  try {
    module = await import(moduleURL);
  } catch (error) {
    URL.revokeObjectURL(moduleURL);
    throw error;
  }
  if (isStale()) {
    URL.revokeObjectURL(moduleURL);
    return { status: 'removed', surface: options.surface || 'host', domRevision: 1 };
  }
  // One poll loop for the whole renderer. Every surface shares it, so three open
  // panels no longer run three overlapping events.poll requests.
  const channel = module.createEventChannel(transport);
  const state = {
    module,
    request: transport.request,
    channel,
    features: { multiSession: false },
    handles: new Set(),
    entryMount: null,
    sidebarRefs: 0,
    sidebarCollapsed: null,
    observer: null,
    ensureFrame: null,
    ensureCancel: null,
    sessionContainer: null,
    sessionHostElement: null,
    sessionHandle: null,
    sessionRelease: null,
    sessionSidebarRelease: null,
    picker: null,
    providerEnhance: null,
    settingsDialog: null,
    settingsList: null,
    settingsBody: null,
    settingsTabs: new Map(),
    settingsHost: null,
    settingsHandle: null,
    settingsActiveTab: null,
    settingsRelease: null,
    settingsObserver: null,
    updateStatus: null,
    updatePill: null,
    updatePopover: null,
    updateOutside: null,
    updateKeydown: null,
    updatePillDigest: '',
    updatePainted: null,
    unsubscribeUpdate: null,
    officialSections: new Map(),
    officialAuthRefresh: null,
    officialPluginsTabActive: false,
    officialPluginsTabPending: false,
    officialPluginsPanel: null,
    officialPluginsSeg: null,
    officialPluginsTabBtn: null,
    officialPluginsSegClick: null,
    accountsCache: null,
    accountsFetchedAt: 0,
    accountsFetchPromise: null,
    accountSwitchModalEl: null,
    accountSwitchModalHandle: null,
    keepAliveCache: new Map(),
    keepAliveFetchPromises: new Map(),
    keepAliveMenuEl: null,
    seenSchedulerNotifyTags: new Set(),
    unsubscribeSchedule: null,
    keepAliveMenuHandle: null,
    keepAliveMenuSessionId: '',
    keepAliveMenuOutside: null,
    keepAliveSuppress: new Map(),
    restoreNotifyCompletion: null,
    unsubscribeConfig: null,
    unsubscribeAccounts: null,
    unsubscribeKeepAlive: null,
    unsubscribeAutomation: null,
    removed: false,
    pluginEntries: new Map(),
    pluginSidebarNav: new Map(),
    pluginPanels: new Map(),
    pluginMainAreas: new Map(),
    activePluginPanel: null,
    panelContract: null,
    plugins: null,
    popoutRoot: null,
    pluginReleases: new Map(),
    pluginRevisions: new Map(),
    pluginTokens: new Map(),
    pluginAssets: new Map(),
    pluginRetiredTokens: new Map(),
    pluginSettings: new Map(),
    watchedSession: '',
    browserNavListeners: new Map(),
    contractState: module.createDOMContractState(),
    contracts: null,
    lastContractDigest: '',
    contractReportTimer: null,
  };
  // The official client is not a stable DOM contract. Every probe is allowed to
  // fail after an official upgrade: the failure disables the one enhancement
  // that needed it, with one clear contract note, and must never escape into
  // the official renderer or its chat input.
  const warnedContracts = new Set();
  function contractDegraded(area, error) {
    const detail = error?.message || String(error);
    const key = area + ': ' + detail;
    if (warnedContracts.has(key)) return;
    warnedContracts.add(key);
    console.warn(`[KimiCode+] ${area}契约探测失败，已停用对应增强入口；官方聊天区与输入不受影响。${detail}`);
  }
  function probe(area, run, fallback) {
    try {
      return run();
    } catch (error) {
      contractDegraded(area, error);
      return fallback;
    }
  }
  function piniaStore(name) {
    return probe('官方 Vue-Pinia 状态仓库', () => document.querySelector('#app')?.__vue_app__?.config?.globalProperties?.$pinia?._s?.get?.(name) || null, null);
  }
  const activeSession = () => {
    if (options.sourceSessionId) return options.sourceSessionId;
    return probe('官方会话状态', () => piniaStore('kimi.sessions')?.activeSessionId || '', '');
  };
  function makeHost(close) {
    return module.createNativeHost(transport, {
      activeSession,
      onActiveSession: listener => listen('ekd:active-session', listener),
      onAddSession: listener => listen('ekd:add-session', listener),
      close,
      pickSession: exclude => options.popout ? transport.request('host.pickSession', { exclude }) : pickSession(exclude),
    }, channel);
  }
  function mount(element, surface, host, extra = {}) {
    const handle = module.mountSurface(element, surface, {
      host,
      stylesheetURL: '',
      stylesheetText: options.cssSource,
      ...extra,
    });
    state.handles.add(handle);
    return handle;
  }
  function unmountHandle(handle) {
    if (!handle) return;
    handle.unmount();
    state.handles.delete(handle);
  }
  function contain(container, kind) {
    container.setAttribute('data-ekd-containment', kind);
    return () => container.removeAttribute('data-ekd-containment');
  }
  function listen(name, listener) {
    const handler = event => listener(event.detail);
    window.addEventListener(name, handler);
    return () => window.removeEventListener(name, handler);
  }
  function panel() {
    return probe('官方右侧面板', () => {
      const shell = document.querySelector('.pt-shell');
      let found;
      const seen = new Set();
      function walk(node) {
        if (!node || seen.has(node) || found) return;
        seen.add(node);
        const component = node.component;
        if (component) {
          const candidate = component.props?.panel;
          if (candidate?.showPanel && candidate?.activateTab && candidate?.previewWidth && candidate?.panelTabs && (component.subTree?.el === shell || component.subTree?.el?.contains?.(shell))) found = candidate;
          walk(component.subTree);
        }
        if (Array.isArray(node.children)) node.children.forEach(walk);
      }
      walk(document.querySelector('#app')?._vnode);
      return found;
    }, undefined);
  }
  // Reference-counted sidebar collapse. The state before the first acquire is
  // remembered so the last release restores exactly what the user had, and a
  // sidebar that was already collapsed is never expanded by us.
  function acquireSidebarCollapse() {
    if (!state.sidebarRefs) {
      const collapse = document.querySelector('button.sidebar-toggle-btn[aria-label="收起侧边栏"]');
      state.sidebarCollapsed = collapse ? (collapse.click(), true) : null;
    }
    state.sidebarRefs += 1;
    let released = false;
    return () => {
      if (released) return;
      released = true;
      state.sidebarRefs = Math.max(0, state.sidebarRefs - 1);
      if (state.sidebarRefs > 0) return;
      const shouldExpand = state.sidebarCollapsed === true;
      state.sidebarCollapsed = null;
      if (shouldExpand) document.querySelector('button.sidebar-toggle-btn[aria-label="展开侧边栏"]')?.click();
    };
  }
  function closeSessions() {
    unmountHandle(state.sessionHandle);
    state.sessionHandle = null;
    state.sessionHostElement?.remove();
    state.sessionHostElement = null;
    state.sessionRelease?.();
    state.sessionRelease = null;
    state.sessionContainer = null;
    state.sessionSidebarRelease?.();
    state.sessionSidebarRelease = null;
  }
  async function mountSessions(extraIds) {
    if (!state.features.multiSession) throw new Error('会话并排已关闭，请先在「实验室」设置中开启');
    // `.chat-main` has exactly one enhancer layout at a time. The plugin split
    // wraps every direct child of the column, so an open plugin main area must
    // be unwrapped before the session host is appended: otherwise the session
    // host ends up inside `.ekdp-main-split` and the session CSS hides the whole
    // column.
    for (const handle of [...state.pluginMainAreas.values()]) handle.dispose();
    closeSessions();
    const chat = document.querySelector('.chat');
    const composer = document.querySelector('.composer');
    const container = composer?.closest('.chat-main') || chat?.closest('.chat-main');
    if (!container || !composer) throw new Error('主聊天区契约未就绪');
    state.sessionSidebarRelease ??= acquireSidebarCollapse();
    state.sessionContainer = container;
    state.sessionRelease = contain(container, 'sessions');
    const element = document.createElement('div');
    element.className = 'ekd-session-host';
    container.append(element);
    state.sessionHostElement = element;
    const host = makeHost(closeSessions);
    state.sessionHandle = mount(element, 'sessions', host, { initialExtraIds: extraIds, onNativeRestore: closeSessions });
  }
  function clearOfficialPluginsTab(resetActive = true) {
    if (state.officialPluginsSeg && state.officialPluginsSegClick) {
      state.officialPluginsSeg.removeEventListener('click', state.officialPluginsSegClick, true);
    }
    if (state.officialPluginsSeg) {
      state.officialPluginsSeg.removeAttribute('data-ekd-plugins-active');
    }
    if (state.officialPluginsPanel) {
      state.officialPluginsPanel.classList.remove('ekd-plugins-active');
      state.officialPluginsPanel.removeAttribute('data-ekd-plugins-segmented');
    }
    state.officialPluginsTabBtn?.remove();
    state.officialPluginsPanel = null;
    state.officialPluginsSeg = null;
    state.officialPluginsTabBtn = null;
    state.officialPluginsSegClick = null;
    if (resetActive) state.officialPluginsTabActive = false;
  }
  function applyOfficialPluginsTabState(panel, seg, btn) {
    const active = state.officialPluginsTabActive === true;
    if (panel) {
      if (panel.dataset.ekdPluginsSegmented !== 'true') panel.dataset.ekdPluginsSegmented = 'true';
      if (panel.classList.contains('ekd-plugins-active') !== active) {
        panel.classList.toggle('ekd-plugins-active', active);
      }
    }
    if (seg) {
      const hasAttr = seg.getAttribute('data-ekd-plugins-active') === 'true';
      if (active && !hasAttr) seg.setAttribute('data-ekd-plugins-active', 'true');
      else if (!active && hasAttr) seg.removeAttribute('data-ekd-plugins-active');
    }
    if (btn) {
      if (btn.classList.contains('is-on') !== active) btn.classList.toggle('is-on', active);
      if (btn.getAttribute('aria-selected') !== String(active)) btn.setAttribute('aria-selected', String(active));
    }
  }
  function unmountOfficialSection(key) {
    const entry = state.officialSections.get(key);
    if (!entry) return;
    state.officialSections.delete(key);
    unmountHandle(entry.handle);
    entry.hostEl.remove();
  }
  function unmountAllOfficialSections() {
    clearOfficialPluginsTab(true);
    for (const key of [...state.officialSections.keys()]) unmountOfficialSection(key);
    for (const stale of document.querySelectorAll('[data-ekd-official-section], [data-ekd-plugins-tab]')) stale.remove();
  }
  function placeOfficialSection(panel, key, surface, placement) {
    let entry = state.officialSections.get(key);
    for (const extra of document.querySelectorAll(`[data-ekd-official-section="${key}"]`)) {
      if (!entry || extra !== entry.hostEl) extra.remove();
    }
    if (!entry) {
      const hostEl = document.createElement('div');
      hostEl.className = 'ekd-official-section';
      hostEl.dataset.ekdOfficialSection = key;
      const handle = mount(hostEl, surface, makeHost(() => {}));
      entry = { key, surface, hostEl, handle };
      state.officialSections.set(key, entry);
    }
    const { hostEl } = entry;
    if (placement.kind === 'prepend') {
      if (hostEl.parentElement !== panel || panel.firstElementChild !== hostEl) {
        panel.insertBefore(hostEl, panel.firstChild);
      }
      return entry;
    }
    if (placement.kind === 'after' && placement.anchor && placement.anchor.parentElement === panel) {
      if (hostEl.parentElement !== panel || placement.anchor.nextElementSibling !== hostEl) {
        placement.anchor.after(hostEl);
      }
      return entry;
    }
    if (hostEl.parentElement !== panel || panel.lastElementChild !== hostEl) {
      panel.append(hostEl);
    }
    return entry;
  }
  function syncOfficialSettingsSections(evaluation) {
    if (!state.settingsDialog || state.settingsActiveTab || evaluation.byId['dom.settings_dialog']?.status === 'disabled') {
      unmountAllOfficialSections();
      return;
    }
    const desired = new Set();
    if (evaluation.byId['dom.settings_account']?.status !== 'disabled') {
      const accountPanel = module.findOfficialSettingsPanel(document, 'account');
      if (accountPanel) {
        desired.add('account');
        placeOfficialSection(accountPanel, 'account', 'settings-account', { kind: 'prepend' });
      }
    }
    if (evaluation.byId['dom.settings_agent']?.status !== 'disabled') {
      const agentPanel = module.findOfficialSettingsPanel(document, 'agent');
      if (agentPanel) {
        const agentDefaults = module.findOfficialSettingsSection(agentPanel, document, 'settings.agentDefaults');
        const secondarySection = module.findOfficialSettingsSection(agentPanel, document, 'settings.secondaryModelSection');
        desired.add('agent-permissions');
        const permEntry = placeOfficialSection(
          agentPanel,
          'agent-permissions',
          'settings-agent-permissions',
          agentDefaults ? { kind: 'after', anchor: agentDefaults } : { kind: 'append' },
        );
        desired.add('agent-subagent');
        placeOfficialSection(
          agentPanel,
          'agent-subagent',
          'settings-agent-subagent',
          secondarySection
            ? { kind: 'after', anchor: secondarySection }
            : permEntry?.hostEl?.parentElement === agentPanel
              ? { kind: 'after', anchor: permEntry.hostEl }
              : { kind: 'append' },
        );
      }
    }
    if (evaluation.byId['dom.settings_lab']?.status !== 'disabled') {
      const labPanel = module.findOfficialSettingsPanel(document, 'lab');
      if (labPanel) {
        desired.add('lab');
        placeOfficialSection(labPanel, 'lab', 'settings-lab', { kind: 'append' });
      }
    }
    if (evaluation.byId['dom.settings_plugins']?.status !== 'disabled') {
      const pluginsPanel = module.findOfficialSettingsPanel(document, 'plugins');
      if (pluginsPanel) {
        const officialDetailOpen = Boolean(pluginsPanel.querySelector('.plugin-details'));
        const seg = officialDetailOpen
          ? null
          : pluginsPanel.querySelector('.pp-toolbar [role="tablist"], .pp-toolbar .ui-seg');
        const officialBtn = seg
          ? seg.querySelector('button[role="tab"]:not([data-ekd-plugins-tab]), .ui-seg__item:not([data-ekd-plugins-tab])')
          : null;
        if (seg && officialBtn && state.officialPluginsTabPending) {
          state.officialPluginsTabActive = true;
          state.officialPluginsTabPending = false;
        }
        if (officialDetailOpen) {
          clearOfficialPluginsTab(true);
        } else if (seg && officialBtn) {
          if (state.officialPluginsSeg !== seg || !state.officialPluginsTabBtn?.isConnected) {
            clearOfficialPluginsTab(false);
            const btn = officialBtn.cloneNode(false);
            btn.classList.remove('is-on');
            btn.removeAttribute('disabled');
            btn.dataset.ekdPluginsTab = 'true';
            btn.setAttribute('role', 'tab');
            btn.setAttribute('aria-selected', String(state.officialPluginsTabActive));
            btn.textContent = 'KimiCode+';
            btn.addEventListener('click', event => {
              event.preventDefault();
              event.stopPropagation();
              state.officialPluginsTabActive = true;
              applyOfficialPluginsTabState(pluginsPanel, seg, btn);
              ensure();
            });
            seg.append(btn);
            const onSegClick = event => {
              const clicked = event.target?.closest?.('button[role="tab"], .ui-seg__item');
              if (!clicked || clicked.hasAttribute('data-ekd-plugins-tab')) return;
              if (state.officialPluginsTabActive) {
                state.officialPluginsTabActive = false;
                applyOfficialPluginsTabState(pluginsPanel, seg, btn);
                setTimeout(() => ensure(), 20);
              }
            };
            seg.addEventListener('click', onSegClick, true);
            state.officialPluginsPanel = pluginsPanel;
            state.officialPluginsSeg = seg;
            state.officialPluginsTabBtn = btn;
            state.officialPluginsSegClick = onSegClick;
          }
          applyOfficialPluginsTabState(pluginsPanel, seg, state.officialPluginsTabBtn);
          if (state.officialPluginsTabActive) {
            desired.add('plugins');
            placeOfficialSection(pluginsPanel, 'plugins', 'settings-plugins', { kind: 'append' });
          }
        } else {
          clearOfficialPluginsTab(true);
          desired.add('plugins');
          placeOfficialSection(pluginsPanel, 'plugins', 'settings-plugins', { kind: 'append' });
        }
      } else {
        clearOfficialPluginsTab(true);
      }
    } else {
      clearOfficialPluginsTab(true);
    }
    for (const key of [...state.officialSections.keys()]) {
      if (!desired.has(key)) unmountOfficialSection(key);
    }
    for (const stale of document.querySelectorAll('[data-ekd-official-section]')) {
      const key = stale.dataset.ekdOfficialSection;
      const active = key ? state.officialSections.get(key) : null;
      if (!active || active.hostEl !== stale) stale.remove();
    }
  }
  function closeAccountSwitchModal() {
    unmountHandle(state.accountSwitchModalHandle);
    state.accountSwitchModalHandle = null;
    state.accountSwitchModalEl?.remove();
    state.accountSwitchModalEl = null;
  }
  function openAccountSwitchModal(targetId) {
    if (!targetId || state.removed) return;
    closeAccountSwitchModal();
    const el = document.createElement('div');
    el.dataset.ekdAccountModal = 'true';
    document.body.append(el);
    state.accountSwitchModalEl = el;
    state.accountSwitchModalHandle = mount(el, 'account-switch-modal', makeHost(closeAccountSwitchModal), {
      accountTargetId: targetId,
      onCloseModal: closeAccountSwitchModal,
    });
  }
  async function refreshAccountsCache(force = false) {
    if (state.removed) return state.accountsCache;
    if (!force && state.accountsCache && Date.now() - state.accountsFetchedAt < 15000) {
      return state.accountsCache;
    }
    if (state.accountsFetchPromise) return state.accountsFetchPromise;
    state.accountsFetchPromise = transport.request('accounts.list', {})
      .then(result => {
        if (!state.removed && result && Array.isArray(result.accounts)) {
          state.accountsCache = result;
          state.accountsFetchedAt = Date.now();
          if (state.contracts) syncAccountMenu(state.contracts);
        }
        return state.accountsCache;
      })
      .catch(() => state.accountsCache)
      .finally(() => {
        state.accountsFetchPromise = null;
      });
    return state.accountsFetchPromise;
  }
  async function refreshOfficialAuth() {
    if (state.officialAuthRefresh) return state.officialAuthRefresh;
    state.officialAuthRefresh = doRefreshOfficialAuth().finally(() => {
      state.officialAuthRefresh = null;
    });
    return state.officialAuthRefresh;
  }
  async function doRefreshOfficialAuth() {
    try {
      const pinia = document.querySelector('#app')?.__vue_app__?.config.globalProperties.$pinia?._s;
      const authStore = pinia?.get('kimi.auth');
      if (authStore) {
        if (typeof authStore.resetAuth === 'function') {
          authStore.resetAuth();
          await new Promise(resolve => setTimeout(resolve, 45));
        }
        if (typeof authStore.checkAuth === 'function') {
          await authStore.checkAuth();
        }
        if (typeof authStore.probeMembership === 'function') {
          await authStore.probeMembership();
        }
      }
      const configStore = pinia?.get('kimi.config');
      if (typeof configStore?.loadConfig === 'function') {
        await configStore.loadConfig();
      }
    } catch {
      // ignore official store refresh errors if host structure changes
    }
    void refreshAccountsCache(true);
  }
  function syncAccountMenu(evaluation) {
    if (evaluation.byId['dom.account_menu']?.status === 'disabled') {
      for (const node of document.querySelectorAll('[data-ekd-account-menu]')) node.remove();
      return;
    }
    const menu = document.querySelector('.user-menu');
    if (!menu) return;
    if (!state.accountsCache || Date.now() - state.accountsFetchedAt > 15000) {
      void refreshAccountsCache(false);
    }
    const accounts = Array.isArray(state.accountsCache?.accounts) ? state.accountsCache.accounts : [];
    const digest = nativeStringify(accounts.map(a => [a.id, a.label, a.current, a.status, a.avatar || '', a.plan || '']));
    const existing = menu.querySelector('[data-ekd-account-menu]');
    if (accounts.length === 0) {
      existing?.remove();
      delete menu.dataset.ekdAccountDigest;
      return;
    }
    if (existing && menu.dataset.ekdAccountDigest === digest) return;
    existing?.remove();
    menu.dataset.ekdAccountDigest = digest;

    const group = document.createElement('div');
    group.dataset.ekdAccountMenu = 'true';

    const sep = document.createElement('div');
    sep.className = 'ui-menu-sep ekd-account-menu-sep';
    sep.setAttribute('role', 'separator');
    group.append(sep);

    const title = document.createElement('div');
    title.className = 'ekd-account-menu-title';
    title.textContent = '切换账号';
    group.append(title);

    for (const acc of accounts) {
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'ui-menu-item ekd-account-menu-item';
      btn.dataset.ekdAccountQuickId = acc.id;

      if (acc.avatar) {
        const img = document.createElement('img');
        img.className = 'ekd-account-menu-avatar';
        img.src = acc.avatar;
        img.alt = '';
        img.referrerPolicy = 'no-referrer';
        img.onerror = () => {
          const fallback = document.createElement('span');
          fallback.className = 'ekd-account-menu-avatar is-fallback';
          fallback.textContent = ((acc.label || 'K').trim()[0] || 'K').toUpperCase();
          img.replaceWith(fallback);
        };
        btn.append(img);
      } else {
        const fallback = document.createElement('span');
        fallback.className = 'ekd-account-menu-avatar is-fallback';
        fallback.textContent = ((acc.label || 'K').trim()[0] || 'K').toUpperCase();
        btn.append(fallback);
      }

      const name = document.createElement('span');
      name.className = 'ekd-account-menu-name';
      name.textContent = acc.label || acc.nickname || acc.userId;
      btn.append(name);

      if (acc.current) {
        const check = document.createElement('span');
        check.className = 'ekd-account-menu-check';
        check.textContent = '✓';
        btn.append(check);
      } else if (acc.status === 'relogin_required') {
        const meta = document.createElement('span');
        meta.className = 'ekd-account-menu-meta';
        meta.textContent = '需重登';
        btn.append(meta);
      } else if (acc.plan) {
        const meta = document.createElement('span');
        meta.className = 'ekd-account-menu-meta';
        meta.textContent = acc.plan;
        btn.append(meta);
      }

      btn.addEventListener('click', event => {
        event.preventDefault();
        event.stopPropagation();
        document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
        if (!acc.current) {
          openAccountSwitchModal(acc.id);
        }
      });
      group.append(btn);
    }

    const separators = Array.from(menu.querySelectorAll(':scope > .ui-menu-sep'));
    const lastSep = separators.length > 0 ? separators[separators.length - 1] : null;
    if (lastSep && lastSep.parentElement === menu) {
      menu.insertBefore(group, lastSep);
    } else {
      menu.append(group);
    }
  }
  function closeKeepAliveMenu() {
    if (state.keepAliveMenuOutside) {
      document.removeEventListener('pointerdown', state.keepAliveMenuOutside, true);
      window.removeEventListener('resize', closeKeepAliveMenu);
      state.keepAliveMenuOutside = null;
    }
    unmountHandle(state.keepAliveMenuHandle);
    state.keepAliveMenuHandle = null;
    state.keepAliveMenuResize?.disconnect();
    state.keepAliveMenuResize = null;
    state.keepAliveMenuEl?.remove();
    state.keepAliveMenuEl = null;
    state.keepAliveMenuSessionId = '';
  }
  function positionKeepAliveMenu(menuEl, anchorEl) {
    const rect = anchorEl.getBoundingClientRect();
    const menuWidth = 296;
    const measured = menuEl.getBoundingClientRect().height || menuEl.offsetHeight || 0;
    const estHeight = measured > 0 ? measured : 320;
    let left = rect.right - menuWidth;
    if (left < 12) left = Math.min(rect.left, Math.max(12, window.innerWidth - menuWidth - 12));
    if (left + menuWidth > window.innerWidth - 12) left = Math.max(12, window.innerWidth - menuWidth - 12);
    let top = rect.bottom + 6;
    if (top + estHeight > window.innerHeight - 12 && rect.top - estHeight - 6 > 12) {
      top = rect.top - estHeight - 6;
    }
    if (top + estHeight > window.innerHeight - 12) top = Math.max(12, window.innerHeight - estHeight - 12);
    menuEl.style.left = `${Math.round(left)}px`;
    menuEl.style.top = `${Math.round(Math.max(12, top))}px`;
  }
  function watchKeepAliveMenuSize(menuEl, anchorEl) {
    // Expanding a sub-item can push the footer button past the window edge, so
    // the popover re-measures itself instead of trusting the first estimate.
    if (typeof ResizeObserver !== 'function') return null;
    const observer = new ResizeObserver(() => {
      if (!menuEl.isConnected) return;
      positionKeepAliveMenu(menuEl, anchorEl);
    });
    observer.observe(menuEl);
    return observer;
  }
  function openKeepAliveMenu(anchorEl, sessionId) {
    if (!sessionId || state.removed) return;
    if (state.keepAliveMenuEl && state.keepAliveMenuSessionId === sessionId) {
      closeKeepAliveMenu();
      return;
    }
    closeKeepAliveMenu();
    const hostEl = document.createElement('div');
    hostEl.className = 'ekd-keepalive-popover';
    hostEl.dataset.ekdKeepalivePopover = 'true';
    positionKeepAliveMenu(hostEl, anchorEl);
    document.body.append(hostEl);
    state.keepAliveMenuEl = hostEl;
    state.keepAliveMenuSessionId = sessionId;
    state.keepAliveMenuResize = watchKeepAliveMenuSize(hostEl, anchorEl);
    state.keepAliveMenuHandle = mount(hostEl, 'session-keepalive-menu', makeHost(closeKeepAliveMenu), {
      sessionId,
      onCloseModal: closeKeepAliveMenu,
      onKeepAliveUpdated: view => {
        if (view && typeof view === 'object' && view.sessionId) {
          state.keepAliveCache.set(view.sessionId, { view, fetchedAt: Date.now() });
          syncSessionInfoKeepAlive();
        }
      },
    });
    state.keepAliveMenuOutside = event => {
      const path = typeof event.composedPath === 'function' ? event.composedPath() : [];
      if (path.includes(hostEl) || path.includes(anchorEl)) return;
      closeKeepAliveMenu();
    };
    document.addEventListener('pointerdown', state.keepAliveMenuOutside, true);
    window.addEventListener('resize', closeKeepAliveMenu);
  }
  async function fetchSessionKeepAlive(sessionId, force = false) {
    if (!sessionId || state.removed) return null;
    const cached = state.keepAliveCache.get(sessionId);
    if (!force && cached && Date.now() - cached.fetchedAt < 10000) return cached.view;
    if (state.keepAliveFetchPromises.has(sessionId)) return state.keepAliveFetchPromises.get(sessionId);
    const promise = transport.request('keepalive.session.get', { id: sessionId })
      .then(view => {
        if (!state.removed && view && typeof view === 'object') {
          state.keepAliveCache.set(sessionId, { view, fetchedAt: Date.now() });
          syncSessionInfoKeepAlive();
        }
        return view;
      })
      .catch(() => cached?.view ?? null)
      .finally(() => {
        state.keepAliveFetchPromises.delete(sessionId);
      });
    state.keepAliveFetchPromises.set(sessionId, promise);
    return promise;
  }
  function pushKeepAliveToast(event) {
    if (!event || state.removed) return;
    const severity = event.severity === 'error' ? 'error' : event.severity === 'info' ? 'info' : 'warning';
    const title = String(event.title || '空闲缓存保活已停止');
    const message = String(event.message || '');
    try {
      const warningsStore = document.querySelector('#app')?.__vue_app__?.config.globalProperties.$pinia?._s?.get('kimi.warnings');
      if (typeof warningsStore?.pushWarning === 'function') {
        warningsStore.pushWarning({ severity, title, message });
        return;
      }
    } catch {
      // fall through to lightweight native-styled toast fallback
    }
    let stack = document.querySelector('[data-ekd-toast-stack]');
    if (!stack) {
      stack = document.createElement('div');
      stack.className = 'ekd-toast-stack';
      stack.dataset.ekdToastStack = 'true';
      document.body.append(stack);
    }
    const toast = document.createElement('div');
    toast.className = `ekd-toast is-${severity}`;
    toast.setAttribute('role', 'status');
    const body = document.createElement('div');
    body.className = 'ekd-toast-body';
    const heading = document.createElement('strong');
    heading.textContent = title;
    body.append(heading);
    if (message) {
      const text = document.createElement('p');
      text.textContent = message;
      body.append(text);
    }
    const dismiss = document.createElement('button');
    dismiss.type = 'button';
    dismiss.className = 'ekd-toast-close';
    dismiss.setAttribute('aria-label', '关闭提示');
    dismiss.textContent = '×';
    dismiss.onclick = () => toast.remove();
    toast.append(body, dismiss);
    stack.append(toast);
    setTimeout(() => toast.remove(), 9000);
  }
  function installKeepAliveNotifySuppress() {
    if (state.restoreNotifyCompletion) return;
    try {
      const store = document.querySelector('#app')?.__vue_app__?.config.globalProperties.$pinia?._s?.get('kimi.notifications');
      if (!store || typeof store.maybeNotifyCompletion !== 'function') return;
      const original = store.maybeNotifyCompletion;
      store.maybeNotifyCompletion = function patchedNotifyCompletion(sessionId, payload) {
        const entry = state.keepAliveSuppress.get(sessionId);
        if (entry) {
          const now = Date.now();
          if (now <= entry.expiresAt) {
            if (!entry.promptId || !payload?.promptId || entry.promptId === payload.promptId) {
              return;
            }
          } else {
            state.keepAliveSuppress.delete(sessionId);
          }
        }
        return original.apply(this, arguments);
      };
      state.restoreNotifyCompletion = () => {
        if (store.maybeNotifyCompletion !== original) {
          store.maybeNotifyCompletion = original;
        }
      };
    } catch {
      // ignore if notifications store is unavailable
    }
  }
  function syncSessionInfoKeepAlive() {
    if (options.surface !== 'host' || options.popout || state.removed) return;
    const anchor = module.findSessionInfoKeepAliveAnchor(document);
    if (!anchor) return;
    const sessionId = activeSession();
    if (!sessionId) {
      anchor.aside.querySelector('[data-ekd-keepalive-row]')?.remove();
      return;
    }
    const cached = state.keepAliveCache.get(sessionId);
    if (!cached || Date.now() - cached.fetchedAt > 15000) {
      void fetchSessionKeepAlive(sessionId, false);
    }
    const view = cached?.view;
    const statusText = view?.statusLabel || '跟随默认';
    const tone = view?.statusTone || 'muted';
    const digest = `${sessionId}|${statusText}|${tone}`;

    let container = anchor.folders;
    let wrapper = null;
    if (!container) {
      wrapper = anchor.aside.querySelector('[data-ekd-keepalive-wrap]');
      if (!wrapper) {
        wrapper = document.createElement('div');
        wrapper.className = 'ekd-si-keepalive-wrap';
        wrapper.dataset.ekdKeepaliveWrap = 'true';
        if (anchor.before && anchor.before.parentElement === anchor.aside) {
          anchor.aside.insertBefore(wrapper, anchor.before);
        } else {
          anchor.aside.prepend(wrapper);
        }
      }
      container = wrapper;
    } else {
      anchor.aside.querySelector('[data-ekd-keepalive-wrap]')?.remove();
    }

    let button = container.querySelector('[data-ekd-keepalive-row]');
    if (button && button.dataset.ekdKeepaliveDigest === digest) return;
    if (!button) {
      button = document.createElement('button');
      button.type = 'button';
      button.className = 'sif-line sif-action ekd-keepalive-line';
      button.dataset.ekdKeepaliveRow = 'true';
      button.setAttribute('aria-haspopup', 'menu');
      button.innerHTML =
        '<svg class="kw-icon ekd-keepalive-icon" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="9"></circle><path d="M12 7v5l3 2"></path></svg>' +
        '<span class="ekd-keepalive-label">缓存保活</span>' +
        '<span class="ekd-keepalive-status"></span>';
      button.addEventListener('click', event => {
        event.preventDefault();
        event.stopPropagation();
        const currentId = activeSession();
        if (currentId) openKeepAliveMenu(button, currentId);
      });
      container.append(button);
    }
    button.dataset.ekdKeepaliveDigest = digest;
    button.title = view?.stoppedDetail || view?.lastNote || `缓存保活：${statusText}`;
    const statusEl = button.querySelector('.ekd-keepalive-status');
    if (statusEl) {
      statusEl.textContent = statusText;
      statusEl.className = `ekd-keepalive-status is-${tone}`;
    }
  }
  function attachSettingsObserver(dialog) {
    state.settingsObserver?.disconnect();
    state.settingsObserver = null;
    if (!dialog || state.removed) return;
    state.settingsObserver = new MutationObserver(records => {
      const relevant = records.some(record => {
        const target = record.target;
        if (!(target instanceof Element)) return true;
        return !target.closest('[data-ekd-official-section], .ekd-settings-mount, [data-ekd-settings-tab], [data-ekd-plugins-tab]');
      });
      if (relevant) ensure();
    });
    state.settingsObserver.observe(dialog, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeFilter: ['style', 'aria-selected', 'class'],
    });
  }
  function deactivateSettings() {
    state.settingsActiveTab = null;
    unmountHandle(state.settingsHandle);
    state.settingsHandle = null;
    state.settingsHost?.remove();
    state.settingsHost = null;
    state.settingsRelease?.();
    state.settingsRelease = null;
    for (const button of state.settingsTabs.values()) {
      button.classList.remove('on');
      button.setAttribute('aria-selected', 'false');
    }
  }
  function detachSettings() {
    state.officialPluginsTabPending = false;
    state.settingsObserver?.disconnect();
    state.settingsObserver = null;
    unmountAllOfficialSections();
    for (const button of state.settingsTabs.values()) button.remove();
    state.settingsTabs.clear();
    state.settingsDialog = null;
    state.settingsList = null;
    state.settingsBody = null;
  }
  function markSettingsActive() {
    for (const tab of state.settingsList?.querySelectorAll('button.tab[role="tab"]') || []) {
      const selected = typeof state.settingsActiveTab === 'string' && tab === state.settingsTabs.get(state.settingsActiveTab);
      // `ensure()` is driven by DOM mutations, so only write when the state
      // differs; rewriting the same class would schedule another sync.
      if (tab.classList.contains('on') === selected && tab.getAttribute('aria-selected') === String(selected)) continue;
      tab.classList.toggle('on', selected);
      tab.setAttribute('aria-selected', String(selected));
    }
  }
  async function activateSettings(kind = 'kimicode-plus') {
    if (!state.settingsBody) return;
    if (state.settingsActiveTab === kind && state.settingsHandle) return;
    unmountAllOfficialSections();
    unmountHandle(state.settingsHandle);
    state.settingsHandle = null;
    state.settingsHost?.remove();
    state.settingsHost = null;
    state.settingsRelease?.();
    state.settingsRelease = null;
    state.settingsActiveTab = kind;
    markSettingsActive();
    state.settingsRelease = contain(state.settingsBody, 'settings');
    const element = document.createElement('div');
    element.className = 'ekd-settings-mount';
    state.settingsBody.append(element);
    state.settingsHost = element;
    const host = makeHost(() => deactivateSettings());
    state.settingsHandle = mount(element, kind === 'cache' ? 'settings-cache' : 'settings', host);
  }
  async function openSettings(tab) {
    state.officialPluginsTabPending = tab === 'plugins-ekd';
    if (!module.findSettingsDialogParts(document)) {
      const button = document.querySelector('button.side-footer-settings');
      if (!button) throw new Error('官方设置入口未就绪');
      button.click();
    }
    const officialTabs = new Set(['general', 'account', 'agent', 'providers', 'plugins', 'lab', 'archived']);
    const targetOfficialTab = tab === 'plugins-ekd' ? 'plugins' : tab;
    const wantOfficial = typeof targetOfficialTab === 'string' && officialTabs.has(targetOfficialTab);
    const findOfficialTab = key => module.findOfficialSettingsTab(document, key);
    const ready = () => Boolean(state.settingsBody) && state.settingsTabs.size > 0;
    const deadline = Date.now() + 10000;
    while (!ready() || (wantOfficial && !findOfficialTab(targetOfficialTab))) {
      syncSettingsTab();
      if (ready() && (!wantOfficial || findOfficialTab(targetOfficialTab))) break;
      if (Date.now() >= deadline) throw new Error('官方设置对话框契约未就绪');
      await new Promise(resolve => setTimeout(resolve, 50));
    }
    if (wantOfficial) {
      if (state.settingsActiveTab) deactivateSettings();
      const targetTab = findOfficialTab(targetOfficialTab);
      if (!targetTab) throw new Error(`官方「${targetOfficialTab}」标签未找到`);
      targetTab.click();
      targetTab.classList.add('on');
      targetTab.setAttribute('aria-selected', 'true');
      setTimeout(() => ensure(), 40);
      return;
    }
    await activateSettings(tab === 'cache' ? 'cache' : 'kimicode-plus');
  }
  function syncSettingsTab() {
    const parts = module.findSettingsDialogParts(document);
    if (!parts) {
      if (state.settingsDialog) {
        deactivateSettings();
        detachSettings();
      }
      return;
    }
    const { dialog, tabList: list, body } = parts;
    if (!list || !body || parts.officialTabs.length === 0) {
      if (state.settingsDialog) {
        deactivateSettings();
        detachSettings();
      }
      return;
    }
    if (state.settingsDialog !== dialog) {
      deactivateSettings();
      detachSettings();
      state.settingsDialog = dialog;
      attachSettingsObserver(dialog);
    }
    if (state.settingsList !== list) {
      for (const button of state.settingsTabs.values()) button.remove();
      state.settingsTabs.clear();
      state.settingsList = list;
    }
    if (state.settingsBody !== body) {
      state.settingsRelease?.();
      state.settingsBody = body;
      if (state.settingsActiveTab && state.settingsHost) {
        state.settingsRelease = contain(body, 'settings');
        body.append(state.settingsHost);
      }
    }
    if (state.settingsActiveTab) {
      body.setAttribute('data-ekd-containment', 'settings');
      if (state.settingsHost && state.settingsHost.parentElement !== body) body.append(state.settingsHost);
    }
    const injected = [...state.settingsTabs.values()];
    if (injected.length === 2 && injected.every(button => button.isConnected)) {
      if (state.settingsActiveTab) markSettingsActive();
      return;
    }
    const nativeTab = parts.officialTabs[0];
    if (!nativeTab) return;
    for (const button of injected) button.remove();
    for (const stale of list.querySelectorAll('[data-ekd-settings-tab]')) stale.remove();
    state.settingsTabs.clear();
    const archived = module.findOfficialSettingsTab(document, 'archived');
    const makeTab = (kind, label, paintIcon) => {
      const button = nativeTab.cloneNode(true);
      button.classList.remove('on');
      button.setAttribute('aria-selected', 'false');
      button.dataset.ekdSettingsTab = '';
      button.setAttribute('data-ekd-settings-tab', kind);
      const span = button.querySelector('span');
      if (span) span.textContent = label;
      else button.textContent = label;
      button.addEventListener('click', event => {
        event.preventDefault();
        event.stopPropagation();
        void activateSettings(kind);
      });
      paintIcon(button);
      state.settingsTabs.set(kind, button);
      list.insertBefore(button, archived || null);
      return button;
    };
    makeTab('cache', '缓存与用量', replaceCacheIcon);
    makeTab('kimicode-plus', 'KimiCode+', replaceIcon);
    if (state.settingsActiveTab) markSettingsActive();
  }
  const UPDATE_DISMISS_KEY = 'ekd:update-dismissed';
  const UPDATE_VISIBLE_STATES = new Set(['available', 'downloading', 'verifying', 'restarting', 'rolled_back']);
  const UPDATE_ICON = '<svg class="upd-pill-icon" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 4v11"></path><path d="m7 10 5 5 5-5"></path><path d="M5 20h14"></path></svg>';
  function dismissedVersions() {
    try {
      const raw = window.localStorage.getItem(UPDATE_DISMISS_KEY);
      const parsed = raw ? JSON.parse(raw) : [];
      return new Set(Array.isArray(parsed) ? parsed.filter(item => typeof item === 'string' && item) : []);
    } catch {
      return new Set();
    }
  }
  function readUpdateDismissed() {
    // The most recently ignored version drives the pill; the stored set keeps
    // older dismissals so a check that has not refreshed yet cannot resurface them.
    let latest = '';
    try {
      latest = window.sessionStorage.getItem(UPDATE_DISMISS_KEY) || '';
    } catch {}
    return latest;
  }
  function writeUpdateDismissed(version) {
    try {
      const versions = dismissedVersions();
      if (version) versions.add(version);
      else versions.clear();
      window.localStorage.setItem(UPDATE_DISMISS_KEY, JSON.stringify([...versions]));
      if (version) window.sessionStorage.setItem(UPDATE_DISMISS_KEY, version);
      else window.sessionStorage.removeItem(UPDATE_DISMISS_KEY);
    } catch {
      // Storage can be unavailable; dismissal is best effort.
    }
  }
  function updateDismissed(status) {
    const version = status?.latestVersion;
    if (!version) return false;
    if (readUpdateDismissed() === version) return true;
    return dismissedVersions().has(version);
  }
  function updatePercent(status) {
    const total = Number(status?.totalBytes) || 0;
    if (!total) return 0;
    const done = Math.max(0, Number(status?.downloadedBytes) || 0);
    return Math.max(0, Math.min(100, Math.round((done / total) * 100)));
  }
  function updatePillLabel(status) {
    switch (status?.state) {
      case 'available': return '有新版本';
      case 'downloading': return `更新中 ${updatePercent(status)}%`;
      case 'verifying': return '正在更新…';
      case 'restarting': return '正在重启…';
      case 'rolled_back': return '更新已回滚';
      default: return '有新版本';
    }
  }
  function updatePillVisible(status) {
    if (!status || !UPDATE_VISIBLE_STATES.has(status.state)) return false;
    if (status.state !== 'available') return true;
    return !updateDismissed(status);
  }
  function closeUpdatePopover() {
    if (state.updateOutside) {
      document.removeEventListener('pointerdown', state.updateOutside, true);
      state.updateOutside = null;
    }
    if (state.updateKeydown) {
      document.removeEventListener('keydown', state.updateKeydown, true);
      state.updateKeydown = null;
    }
    window.removeEventListener('resize', positionUpdatePopover);
    state.updatePopover?.remove();
    state.updatePopover = null;
    state.updatePainted = null;
  }
  function positionUpdatePopover() {
    const popover = state.updatePopover;
    const pill = state.updatePill;
    if (!popover || !pill) return;
    const rect = pill.getBoundingClientRect();
    const width = Math.min(348, Math.max(272, window.innerWidth - 24));
    popover.style.width = `${width}px`;
    const height = popover.offsetHeight || 300;
    let left = Math.min(rect.right - width, window.innerWidth - width - 12);
    if (left < 12) left = 12;
    let top = rect.bottom + 8;
    if (top + height > window.innerHeight - 12) top = Math.max(12, rect.top - height - 8);
    popover.style.left = `${Math.round(left)}px`;
    popover.style.top = `${Math.round(Math.max(12, top))}px`;
  }
  function appendUpdateAction(node, text, className, onClick) {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = className;
    button.textContent = text;
    button.addEventListener('click', event => {
      event.preventDefault();
      event.stopPropagation();
      onClick();
    });
    node.append(button);
    return button;
  }
  function appendReleaseNotes(container, raw) {
    const lines = String(raw || '').split(/\r?\n/);
    let list = null;
    for (const rawLine of lines) {
      const line = rawLine.trim();
      if (!line) {
        list = null;
        continue;
      }
      const bullet = /^[-*•]\s+(.+)$/.exec(line);
      if (bullet) {
        if (!list) {
          list = document.createElement('ul');
          list.className = 'ekd-upd-list';
          container.append(list);
        }
        const li = document.createElement('li');
        li.textContent = bullet[1];
        list.append(li);
        continue;
      }
      list = null;
      const heading = /^#{1,4}\s+(.+)$/.exec(line);
      if (heading) {
        const h = document.createElement('h5');
        h.className = 'ekd-upd-h';
        h.textContent = heading[1];
        container.append(h);
        continue;
      }
      const p = document.createElement('p');
      p.className = 'ekd-upd-p';
      p.textContent = line;
      container.append(p);
    }
  }
    function openExternal(url) {
    const safe = /^https?:\/\//i.test(String(url || '')) ? String(url) : '';
    if (!safe) return;
    void transport.request('host.openBrowser', { url: safe, mode: 'exec' })
      .catch(() => {
        // No Electron main bridge (compatibility mode) or the browser window
        // refused; handing the URL to the OS browser is the honest fallback.
        if (typeof window.open === 'function') window.open(safe, '_blank', 'noopener,noreferrer');
      });
  }
  function paintUpdatePopover() {
    const popover = state.updatePopover;
    const status = state.updateStatus;
    if (!popover) return;
    // The popover is rebuilt only when its shape changes. A download progress
    // tick updates the number, the bar width and the message in place, so the
    // scroll position of the release notes is preserved.
    const previous = state.updatePainted;
    const shape = status
      ? [status.state, status.error || '', status.note || '', status.cannotApplyReason || '', status.releaseNotes || '', status.releaseUrl || '', status.latestVersion || '', status.canApply ? '1' : '0'].join('|')
      : '';
    if (previous && previous.popover === popover && previous.shape === shape) {
      const percent = updatePercent(status);
      const fill = previous.fill;
      if (fill) fill.style.width = `${percent}%`;
      if (previous.progress) previous.progress.textContent = `正在下载更新… ${percent}%`;
      // The heading carries the same percentage, so patching only the body would
      // leave a stale number above a moving bar.
      if (previous.title) {
        previous.title.textContent = status.state === 'available'
          ? `发现新版本 v${status.latestVersion || ''}`
          : `KimiCode+ ${updatePillLabel(status)}`;
      }
      return;
    }
    const painted = { popover, shape, fill: null, progress: null, title: null };
    state.updatePainted = painted;
    popover.replaceChildren();
    if (!status) return;
    const head = document.createElement('div');
    head.className = 'ekd-upd-head';
    const title = document.createElement('strong');
    title.className = 'ekd-upd-title';
    title.textContent = status.state === 'available'
      ? `发现新版本 v${status.latestVersion || ''}`
      : `KimiCode+ ${updatePillLabel(status)}`;
    painted.title = title;
    const meta = document.createElement('span');
    meta.className = 'ekd-upd-meta';
    meta.textContent = status.currentVersion ? `KimiCode+ · 当前 v${status.currentVersion}` : 'KimiCode+';
    const close = document.createElement('button');
    close.type = 'button';
    close.className = 'ekd-upd-close';
    close.setAttribute('aria-label', '关闭更新提示');
    close.textContent = '×';
    close.addEventListener('click', event => {
      event.preventDefault();
      event.stopPropagation();
      closeUpdatePopover();
    });
    const heading = document.createElement('div');
    heading.className = 'ekd-upd-heading';
    heading.append(title, meta);
    head.append(heading, close);

    const body = document.createElement('div');
    body.className = 'ekd-upd-body';
    const message = status.error || status.note || status.cannotApplyReason || '';
    if (status.state === 'downloading') {
      const percent = updatePercent(status);
      const bar = document.createElement('div');
      bar.className = 'ekd-upd-bar';
      const fill = document.createElement('span');
      fill.style.width = `${percent}%`;
      bar.append(fill);
      body.append(bar);
      const progress = document.createElement('p');
      progress.className = 'ekd-upd-message';
      progress.textContent = `正在下载更新… ${percent}%`;
      body.append(progress);
      painted.fill = fill;
      painted.progress = progress;
    } else if (message) {
      const note = document.createElement('p');
      note.className = 'ekd-upd-message';
      note.textContent = message;
      body.append(note);
    }
    if (status.releaseNotes) {
      const notes = document.createElement('div');
      notes.className = 'ekd-upd-notes';
      const notesTitle = document.createElement('h4');
      notesTitle.className = 'ekd-upd-notes-title';
      notesTitle.textContent = '更新内容';
      const notesBody = document.createElement('div');
      notesBody.className = 'ekd-upd-notes-body';
      appendReleaseNotes(notesBody, status.releaseNotes);
      notes.append(notesTitle, notesBody);
      body.append(notes);
    }

    const foot = document.createElement('div');
    foot.className = 'ekd-upd-foot';
    const links = document.createElement('div');
    links.className = 'ekd-upd-links';
    const releaseUrl = typeof status.releaseUrl === 'string' ? status.releaseUrl : '';
    if (/^https:\/\//i.test(releaseUrl)) {
      appendUpdateAction(links, '查看发布页', 'ekd-upd-link', () => {
        openExternal(releaseUrl);
      });
    }
    appendUpdateAction(links, '更新设置', 'ekd-upd-link', () => {
      closeUpdatePopover();
      void openSettings('kimicode-plus').catch(() => {});
    });
    const actions = document.createElement('div');
    actions.className = 'ekd-upd-actions';
    if (status.state === 'available' && status.latestVersion) {
      appendUpdateAction(actions, '忽略此版本', 'ekd-upd-secondary', () => {
        writeUpdateDismissed(status.latestVersion);
        closeUpdatePopover();
        syncUpdatePill();
      });
    }
    if (status.state === 'available' && status.canApply) {
      const applyButton = appendUpdateAction(actions, '下载并更新', 'ekd-upd-primary', () => {
        if (applyButton.disabled) return;
        applyButton.disabled = true;
        body.querySelector('.ekd-upd-message.is-error')?.remove();
        void transport.request('update.apply')
          .then(next => { if (next && typeof next === 'object') applyUpdateStatus(next); })
          .catch(error => {
            applyButton.disabled = false;
            const text = document.createElement('p');
            text.className = 'ekd-upd-message is-error';
            text.textContent = error?.message || '更新启动失败';
            body.append(text);
          });
      });
    }
    if (links.childElementCount > 0) foot.append(links);
    if (actions.childElementCount > 0) foot.append(actions);
    popover.append(head, body);
    if (foot.childElementCount > 0) popover.append(foot);
  }
  function openUpdatePopover() {
    if (state.updatePopover || !state.updatePill || state.removed) return;
    const popover = document.createElement('div');
    popover.className = 'ekd-upd-pop';
    popover.dataset.ekdUpdatePopover = 'true';
    popover.setAttribute('role', 'dialog');
    popover.setAttribute('aria-label', 'KimiCode+ 更新');
    document.body.append(popover);
    state.updatePopover = popover;
    paintUpdatePopover();
    positionUpdatePopover();
    state.updateOutside = event => {
      const path = typeof event.composedPath === 'function' ? event.composedPath() : [];
      if (path.includes(popover) || path.includes(state.updatePill)) return;
      closeUpdatePopover();
    };
    state.updateKeydown = event => {
      if (event.key === 'Escape') closeUpdatePopover();
    };
    document.addEventListener('pointerdown', state.updateOutside, true);
    document.addEventListener('keydown', state.updateKeydown, true);
    window.addEventListener('resize', positionUpdatePopover);
  }
  function syncUpdatePill() {
    const status = state.updateStatus;
    if (state.removed || !status) {
      if (!state.updatePill && !state.updatePopover) return;
      closeUpdatePopover();
      state.updatePill?.remove();
      state.updatePill = null;
      state.updatePillDigest = '';
      return;
    }
    const tail = updatePillVisible(status) ? module.findChatHeaderTail(document) : null;
    if (!tail) {
      if (!state.updatePill && !state.updatePopover) return;
      closeUpdatePopover();
      state.updatePill?.remove();
      state.updatePill = null;
      state.updatePillDigest = '';
      return;
    }
    // `ensure()` runs on every DOM mutation, and rewriting the pill's children
    // here would post another mutation. Only touch the DOM when the rendered
    // state actually changed.
    const digest = [
      status.state,
      updatePillLabel(status),
      status.latestVersion || '',
      status.canApply ? '1' : '0',
      status.error || status.note || status.cannotApplyReason || '',
    ].join('|');
    if (state.updatePill?.parentElement === tail && digest === state.updatePillDigest) return;
    state.updatePillDigest = digest;
    if (!state.updatePill) {
      const pill = document.createElement('button');
      pill.type = 'button';
      pill.className = 'upd-pill';
      pill.dataset.ekdUpdatePill = 'true';
      pill.addEventListener('click', event => {
        event.preventDefault();
        event.stopPropagation();
        if (state.updatePopover) closeUpdatePopover();
        else openUpdatePopover();
      });
      state.updatePill = pill;
    }
    const pill = state.updatePill;
    if (pill.parentElement !== tail) tail.append(pill);
    const label = updatePillLabel(status);
    pill.dataset.state = status.state;
    pill.setAttribute('aria-label', `KimiCode+ ${label}`);
    pill.title = status.state === 'available'
      ? `KimiCode+ v${status.latestVersion || ''} 可用，点击查看更新`
      : label;
    pill.replaceChildren();
    const icon = document.createElement('span');
    icon.className = 'upd-pill-icon-wrap';
    icon.innerHTML = UPDATE_ICON;
    const text = document.createElement('span');
    text.className = 'upd-pill-text';
    text.textContent = label;
    pill.append(icon, text);
    if (state.updatePopover) {
      paintUpdatePopover();
      positionUpdatePopover();
    }
  }
  function applyUpdateStatus(status) {
    if (state.removed) return;
    if (!status || typeof status !== 'object' || typeof status.state !== 'string') {
      // A read that answers with an envelope instead of a status must not wipe a
      // pill the last real status produced.
      if (status === null) {
        state.updateStatus = null;
        syncUpdatePill();
      }
      return;
    }
    state.updateStatus = status;
    syncUpdatePill();
  }
  function disposeUpdateSurface() {
    state.unsubscribeUpdate?.();
    state.unsubscribeUpdate = null;
    closeUpdatePopover();
    state.updatePill?.remove();
    state.updatePill = null;
    state.updatePillDigest = '';
    state.updateStatus = null;
  }
  async function pickSession(exclude = []) {
    if (state.picker) { state.picker.close(); state.picker.remove(); }
    return new Promise((resolve, reject) => {
      let settled = false;
      let items = null;
      let loadError = '';
      let retryTimer;
      const dialog = document.createElement('dialog');
      dialog.className = 'ekd-session-picker';
      dialog.innerHTML = '<div class="ekd-picker-head"><div><strong>添加同屏会话</strong><p>选择一个已有会话，与当前会话并排在主聊天区。</p></div><button type="button" class="ekd-picker-close" aria-label="关闭"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true"><path d="m6 6 12 12M18 6 6 18"/></svg></button></div><label class="ekd-picker-search"><span>搜索</span><input type="search" placeholder="搜索会话标题" /></label><div class="ekd-picker-list"></div><p class="ekd-picker-foot">最多 3 个同屏。关闭窗格不会结束会话。</p>';
      const list = dialog.querySelector('.ekd-picker-list');
      const input = dialog.querySelector('input');
      function finish(id) {
        if (settled) return;
        settled = true;
        clearTimeout(retryTimer);
        if (state.picker === dialog) state.picker = null;
        if (id) resolve(id);
        else reject(new Error('已取消选择'));
        dialog.close();
        dialog.remove();
      }
      async function loadSessions(retries = 3) {
        try {
          const result = await transport.request('sessions.list');
          if (settled) return;
          const listed = Array.isArray(result?.items) ? result.items : [];
          const available = listed.filter(session => session && typeof session.id === 'string' && !exclude.includes(session.id));
          if (!available.length && retries > 0) {
            retryTimer = setTimeout(() => void loadSessions(retries - 1), 500);
            return;
          }
          items = available;
        } catch (error) {
          if (settled) return;
          loadError = error?.message || '无法读取会话。';
        }
        render();
      }
      function render() {
        const query = input.value.trim().toLowerCase();
        const sessions = items?.filter(session => !query || (session.title || session.id).toLowerCase().includes(query)) || [];
        list.replaceChildren();
        if (!sessions.length) {
          const empty = document.createElement('p');
          empty.className = 'ekd-picker-empty';
          empty.setAttribute('role', 'status');
          empty.textContent = loadError || (items === null ? '正在读取会话…' : '没有可添加的已有会话。');
          list.append(empty);
          if (loadError || items?.length === 0) {
            const retry = document.createElement('button');
            retry.type = 'button';
            retry.textContent = '刷新会话';
            retry.addEventListener('click', () => {
              items = null;
              loadError = '';
              render();
              void loadSessions();
            });
            list.append(retry);
          }
          return;
        }
        for (const session of sessions) {
          const button = document.createElement('button');
          button.type = 'button';
          button.dataset.sessionId = session.id;
          const title = document.createElement('strong'); title.textContent = session.title || session.id.slice(0, 8);
          const detail = document.createElement('small'); detail.textContent = session.cwd || session.id;
          button.append(title, detail);
          button.addEventListener('click', () => finish(session.id));
          list.append(button);
        }
      }
      input.addEventListener('input', render);
      dialog.querySelector('.ekd-picker-close').addEventListener('click', () => finish());
      dialog.addEventListener('close', () => finish(), { once: true });
      state.picker = dialog;
      document.body.append(dialog);
      render();
      dialog.showModal();
      input.focus();
      void loadSessions();
    });
  }
  function openOfficial(id) {
    closeSessions();
    return activateSession(id);
  }
  function activateSession(id) {
    const element = probe('官方会话行', () => [...document.querySelectorAll('[data-session-id]')].find(item => item.dataset.sessionId === id)?.querySelector('.row'), undefined);
    if (element) element.click();
    else {
      const store = piniaStore('kimi.sessions');
      if (typeof store?.setActiveSessionId !== 'function') throw new Error('官方会话选择契约未就绪');
      try {
        store.setActiveSessionId(id);
      } catch (error) {
        contractDegraded('官方会话选择', error);
        throw new Error('官方会话选择契约未就绪');
      }
    }
    if (activeSession() !== id) throw new Error('官方宿主没有接受会话切换');
    return { opened: true };
  }
  const nativeStyle = document.createElement('style');
  nativeStyle.textContent = `
    /* light-dark() resolves from the element's color-scheme property, not from
       the host's data attribute. A standalone window is about:blank, so nothing
       else declares it there and every light-dark() fallback painted white in
       dark mode. */
    html[data-color-scheme="dark"]{color-scheme:dark}
    html[data-color-scheme="light"]{color-scheme:light}
    [data-ekd-containment=sessions]>:not(.ekd-session-host),[data-ekd-containment=settings]>:not(.ekd-settings-mount){display:none!important}
    .ekd-session-host{display:flex;flex:1;min-width:0;min-height:0;width:100%;height:100%;overflow:hidden;padding-top:44px;box-sizing:border-box;-webkit-app-region:no-drag}
    header.chat-header~.chat-layout .ekd-session-host,header.chat-header~.ekd-session-host{padding-top:0}
    .pt-body[data-ekdp-panel]>:not(.ekdp-panel-host){display:none!important}
    .pt-body[data-ekdp-panel]>.ekdp-panel-host[hidden]{display:none!important}
    .ekdp-panel-host{display:flex;flex:1;flex-direction:column;min-width:0;min-height:0;width:100%;height:100%;overflow:hidden}
    .ekdp-main-split{display:flex;flex:1 1 auto;min-width:0;min-height:0;width:100%;overflow:hidden}
    .ekdp-main-split[data-ekdp-mode="page"]>.ekdp-main-native,.ekdp-main-split[data-ekdp-mode="page"]>.ekdp-main-splitter{display:none!important}
    .ekdp-main-split[data-ekdp-mode="page"]>.ekdp-main-pane{flex:1 1 auto;width:100%;padding-top:44px;box-sizing:border-box;-webkit-app-region:no-drag}
    .ekdp-main-split[data-ekdp-mode="split"]>.ekdp-main-pane{padding-top:44px;box-sizing:border-box;-webkit-app-region:no-drag}
    header.chat-header~.chat-layout .ekdp-main-split>.ekdp-main-pane,header.chat-header~.ekdp-main-split>.ekdp-main-pane{padding-top:0}
    .ekdp-main-native{display:flex;flex:1 1 auto;flex-direction:column;min-width:0;min-height:0}
    .ekdp-main-pane{display:flex;flex:0 0 auto;flex-direction:column;min-width:0;min-height:0;background:var(--color-bg,light-dark(#fff,#121212))}
    .ekdp-main-splitter{flex:0 0 5px;cursor:col-resize;background:var(--color-line,#8884);opacity:.55;touch-action:none}
    .ekdp-main-splitter:hover,.ekdp-main-splitter:focus-visible,.ekdp-main-splitter.dragging{opacity:1;outline:none;background:var(--blue,#5b8def)}
    /* The official settings body is a fixed-height box that does not scroll. The
       mount therefore owns the scroll for the enhanced page only: it is clamped to
       the body's height and scrolls its own content. Without this the tail of the
       panel sits below the dialog and cannot be reached by wheel, keyboard or
       scripted scrolling. flex:1 1 auto covers a flex body, max-height:100% covers
       a block body; both resolve against the body's definite height. */
    .ekd-settings-mount{display:block;width:100%;min-width:0;min-height:0;flex:1 1 auto;max-height:100%;box-sizing:border-box;overflow-y:auto;overscroll-behavior:contain;scrollbar-width:thin;scrollbar-color:color-mix(in srgb,var(--color-text-faint,#888) 38%,transparent) transparent}
    .ekd-settings-mount::-webkit-scrollbar{width:6px;height:6px}
    .ekd-settings-mount::-webkit-scrollbar-track{background:transparent}
    .ekd-settings-mount::-webkit-scrollbar-thumb{background:color-mix(in srgb,var(--color-text-faint,#888) 34%,transparent);border-radius:9999px}
    .ekd-settings-mount::-webkit-scrollbar-thumb:hover{background:color-mix(in srgb,var(--color-text-faint,#888) 52%,transparent)}
    .ekd-settings-mount::-webkit-scrollbar-corner{background:transparent}
    .ekd-official-section{display:block;width:100%;min-width:0;box-sizing:border-box;padding:var(--space-4,16px) 0}
    .panel>.ekd-official-section:first-child,.plugins-panel>.ekd-official-section[data-ekd-official-section="plugins"]{padding-top:0}
    .plugins-panel[data-ekd-plugins-segmented="true"]:not(.ekd-plugins-active)>[data-ekd-official-section="plugins"]{display:none!important}
    .plugins-panel.ekd-plugins-active>:not(.pp-panel-title):not(.pp-toolbar):not([data-ekd-official-section="plugins"]){display:none!important}
    .plugins-panel.ekd-plugins-active .pp-tools{visibility:hidden;pointer-events:none}
    .ui-seg[data-ekd-plugins-active="true"] .ui-seg__item:not([data-ekd-plugins-tab]){background:transparent!important;box-shadow:none!important;color:var(--color-text-muted,var(--color-text-faint,inherit))!important}
    .ui-seg[data-ekd-plugins-active="true"] .ui-seg__indicator{opacity:0!important;pointer-events:none}
    .ekd-native-entry{position:relative}
    .ekd-session-picker{width:min(520px,calc(100vw - 40px));max-height:min(680px,calc(100vh - 60px));padding:0;border:1px solid var(--color-line,#8884);border-radius:8px;background:var(--color-surface-raised,var(--color-surface,var(--color-bg,light-dark(#fff,#121212))));color:var(--color-text,light-dark(#1a1c20,#eee));font:13px/1.5 var(--font-ui,system-ui);box-shadow:0 16px 50px rgba(0,0,0,.24)}
    .ekd-session-picker::backdrop{background:rgba(0,0,0,.36)}
    .ekd-picker-head{display:flex;gap:16px;padding:18px 18px 12px;border-bottom:1px solid var(--color-line,#8883)}
    .ekd-picker-head strong{font-size:15px}.ekd-picker-head p{margin:4px 0 0;color:var(--color-text-secondary,#aaa);font-size:12px}
    .ekd-picker-close{margin-left:auto;width:26px;height:26px;border:0;border-radius:6px;background:transparent;color:inherit;font-size:20px;cursor:pointer}
    .ekd-picker-close:hover{background:var(--color-hover,#8882)}
    .ekd-picker-search{display:flex;align-items:center;gap:10px;margin:12px 18px;padding:0 10px;border:1px solid var(--color-line,#8884);border-radius:6px;color:var(--color-text-secondary,#aaa);font-size:12px}
    .ekd-picker-search input{flex:1;min-width:0;height:32px;border:0;outline:0;background:transparent;color:inherit;font:inherit}
    .ekd-picker-list{max-height:380px;overflow:auto;padding:0 10px}
    .ekd-picker-list button{display:block;width:100%;padding:10px;border:0;border-radius:6px;background:transparent;color:inherit;text-align:left;cursor:pointer}
    .ekd-picker-list button:hover,.ekd-picker-list button:focus-visible{background:var(--color-hover,#8882)}
    .ekd-picker-list strong,.ekd-picker-list small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
    .ekd-picker-list small{margin-top:2px;color:var(--color-text-secondary,#aaa);font-size:11px}
    .ekd-picker-foot,.ekd-picker-empty{margin:10px 18px 16px;color:var(--color-text-secondary,#aaa);font-size:11px}
    .ekd-account-menu-sep{height:1px;margin:4px 6px;background:var(--color-line,rgba(128,128,128,.22))}
    .ekd-account-menu-title{padding:4px 10px 3px;color:var(--color-text-secondary,#8e8e93);font-size:11px;font-weight:500;letter-spacing:.02em;user-select:none}
    .ekd-account-menu-item{display:flex;align-items:center;gap:8px;width:100%;height:30px;margin:0;padding:0 10px;border:0;border-radius:6px;background:transparent;color:var(--color-text,inherit);font:13px/1.4 var(--font-ui,system-ui);text-align:left;cursor:pointer;appearance:none;-webkit-appearance:none;box-sizing:border-box}
    .ekd-account-menu-item:hover,.ekd-account-menu-item:focus-visible{background:var(--color-hover,rgba(128,128,128,.16));outline:none}
    .ekd-account-menu-avatar{flex:none;width:18px;height:18px;border-radius:50%;object-fit:cover;border:1px solid var(--color-line,#8883)}
    .ekd-account-menu-avatar.is-fallback{display:inline-flex;align-items:center;justify-content:center;font-size:10px;font-weight:600;background:color-mix(in srgb,var(--color-accent,#5b8def) 18%,transparent);color:var(--color-text,inherit)}
    .ekd-account-menu-name{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;text-align:left}
    .ekd-account-menu-meta{flex:none;font-size:11px;color:var(--color-text-secondary,#8e8e93)}
    .ekd-account-menu-check{flex:none;color:var(--success,#27865a);font-size:12px;font-weight:600}
    .ekd-si-keepalive-wrap{position:relative;padding:8px 0 10px;border-bottom:1px solid var(--color-line,rgba(128,128,128,.22))}
    .ekd-keepalive-line{display:flex;align-items:center;gap:8px;width:100%;min-height:32px;margin:0;padding:4px 16px;border:0;border-radius:6px;background:transparent;color:var(--color-text-muted,var(--color-text-secondary,#8e8e93));font:12px/1.4 var(--font-ui,system-ui);text-align:left;cursor:pointer;appearance:none;-webkit-appearance:none;box-sizing:border-box;transition:background 120ms ease,color 120ms ease}
    .ekd-keepalive-line:hover,.ekd-keepalive-line:focus-visible{background:var(--color-hover,rgba(128,128,128,.14));color:var(--color-text,inherit);outline:none}
    .ekd-keepalive-icon{flex:none;color:var(--color-text-faint,currentColor)}
    .ekd-keepalive-label{flex:none;white-space:nowrap}
    .ekd-keepalive-status{flex:1;min-width:0;text-align:right;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:11.5px;color:var(--color-text-faint,#8e8e93)}
    .ekd-keepalive-status.is-ok,.ekd-keepalive-status.is-active{color:var(--color-success,var(--success,#27865a))}
    .ekd-keepalive-status.is-warn{color:var(--color-warning,var(--warning,#b4761f))}
    .ekd-keepalive-status.is-error{color:var(--color-danger,var(--danger,#c64b52))}
    .ekd-keepalive-popover{position:fixed;z-index:2147483000}
    .ch-tail .upd-pill{display:inline-flex;align-items:center;gap:4px;height:24px;padding:0 9px;border:0;border-radius:9999px;font:500 11.5px/1 var(--font-ui,system-ui);white-space:nowrap;cursor:pointer;background:var(--color-warning,#d9822b);color:var(--color-text-on-accent,#fff);transition:filter 120ms ease;-webkit-app-region:no-drag}
    .ch-tail .upd-pill:hover{filter:brightness(1.08)}
    .ch-tail .upd-pill:focus-visible{outline:none;box-shadow:var(--p-focus-ring,0 0 0 2px color-mix(in srgb,var(--color-accent,#5b8def) 55%,transparent))}
    .ch-tail .upd-pill[data-state="downloading"],.ch-tail .upd-pill[data-state="verifying"],.ch-tail .upd-pill[data-state="restarting"]{background:var(--color-accent,#5b8def);color:var(--color-text-on-accent,#fff)}
    .ch-tail .upd-pill[data-state="rolled_back"]{background:var(--color-danger,#c64b52);color:var(--color-text-on-accent,#fff)}
    .ch-tail .upd-pill .upd-pill-icon-wrap{display:inline-flex;flex:none;align-items:center;justify-content:center}
    .ch-tail .upd-pill .upd-pill-icon{flex:none;color:currentColor}
    .ch-tail .upd-pill .upd-pill-text{font-variant-numeric:tabular-nums}
    .ekd-upd-pop{position:fixed;z-index:2147483000;box-sizing:border-box;padding:16px;border:1px solid var(--color-line,rgba(128,128,128,.24));border-radius:14px;background:var(--color-surface-raised,var(--color-surface,var(--color-bg,light-dark(#fff,#222))));color:var(--color-text,light-dark(#1a1c20,#eee));font:12.5px/1.5 var(--font-ui,system-ui);box-shadow:0 16px 44px light-dark(rgba(15,23,42,.14),rgba(0,0,0,.42))}
    .ekd-upd-head{display:flex;align-items:flex-start;gap:10px}
    .ekd-upd-heading{display:flex;flex:1;min-width:0;flex-direction:column;gap:2px}
    .ekd-upd-title{font-size:13.5px;font-weight:600;color:var(--color-text,light-dark(#1a1c20,#eee))}
    .ekd-upd-meta{font-size:11.5px;color:var(--color-text-faint,light-dark(#6b7280,#8e8e93))}
    .ekd-upd-close{flex:none;width:24px;height:24px;padding:0;border:0;border-radius:6px;background:transparent;color:var(--color-text-muted,var(--color-text-secondary,light-dark(#5c6370,#aaa)));font:16px/1 var(--font-ui,system-ui);cursor:pointer}
    .ekd-upd-close:hover{background:var(--color-hover,rgba(128,128,128,.16));color:var(--color-text,light-dark(#1a1c20,#eee))}
    .ekd-upd-body{display:flex;flex-direction:column;gap:10px;margin-top:10px}
    .ekd-upd-message{margin:0;font-size:12px;color:var(--color-text-muted,var(--color-text-secondary,light-dark(#5c6370,#aaa)));word-break:break-word}
    .ekd-upd-message.is-error{color:var(--color-danger,#c64b52)}
    .ekd-upd-bar{height:4px;border-radius:9999px;background:color-mix(in srgb,var(--color-text-faint,#888) 22%,transparent);overflow:hidden}
    .ekd-upd-bar>span{display:block;height:100%;border-radius:9999px;background:var(--color-accent,#5b8def);transition:width 200ms ease}
    .ekd-upd-notes{margin:0;padding-top:10px;border-top:1px solid var(--color-line,rgba(128,128,128,.24))}
    .ekd-upd-notes-title{margin:0 0 6px;font-size:11.5px;font-weight:500;color:var(--color-text-faint,light-dark(#6b7280,#8e8e93))}
    .ekd-upd-notes-body{margin:0;max-height:min(280px,38vh);overflow-y:auto;font:12px/1.6 var(--font-ui,system-ui);color:var(--color-text,light-dark(#1a1c20,#eee));scrollbar-width:thin;scrollbar-color:color-mix(in srgb,var(--color-text-faint,#888) 38%,transparent) transparent}
    .ekd-upd-notes-body::-webkit-scrollbar{width:6px}
    .ekd-upd-notes-body::-webkit-scrollbar-track{background:transparent}
    .ekd-upd-notes-body::-webkit-scrollbar-thumb{background:color-mix(in srgb,var(--color-text-faint,#888) 34%,transparent);border-radius:9999px}
    .ekd-upd-list{margin:0;padding-left:16px;display:flex;flex-direction:column;gap:4px}
    .ekd-upd-list li{margin:0}
    .ekd-upd-h{margin:8px 0 4px;font-size:12px;font-weight:600;color:var(--color-text,light-dark(#1a1c20,#eee))}
    .ekd-upd-h:first-child{margin-top:0}
    .ekd-upd-p{margin:0 0 6px}
    .ekd-upd-p:last-child{margin-bottom:0}
    .ekd-upd-foot{display:flex;align-items:center;justify-content:space-between;gap:10px;flex-wrap:wrap;margin-top:14px;padding-top:12px;border-top:1px solid var(--color-line,rgba(128,128,128,.2))}
    .ekd-upd-links{display:flex;align-items:center;gap:10px;flex-wrap:wrap}
    .ekd-upd-actions{display:flex;align-items:center;justify-content:flex-end;gap:8px;margin-left:auto}
    .ekd-upd-secondary{height:30px;padding:0 10px;border:1px solid var(--color-line,rgba(128,128,128,.28));border-radius:8px;background:transparent;color:var(--color-text,light-dark(#1a1c20,#eee));font:500 12px/1 var(--font-ui,system-ui);cursor:pointer;white-space:nowrap}
    .ekd-upd-secondary:hover{background:var(--color-hover,rgba(128,128,128,.12))}
    .ekd-upd-primary{height:30px;padding:0 12px;border:0;border-radius:8px;background:var(--color-accent,#2563eb);color:var(--color-text-on-accent,#fff);font:500 12px/1 var(--font-ui,system-ui);cursor:pointer;white-space:nowrap}
    .ekd-upd-primary:hover{filter:brightness(1.08)}
    .ekd-upd-primary:disabled{opacity:.6;cursor:default}
    .ekd-upd-link{padding:0;border:0;background:transparent;color:var(--color-text-muted,var(--color-text-secondary,light-dark(#5c6370,#aaa)));font:11.5px/1.4 var(--font-ui,system-ui);cursor:pointer}
    .ekd-upd-link:hover{color:var(--color-text,light-dark(#1a1c20,#eee));text-decoration:underline}
    .ekd-toast-stack{position:fixed;right:18px;bottom:18px;z-index:2147483001;display:flex;flex-direction:column;gap:8px;max-width:360px}
    .ekd-toast{display:flex;align-items:flex-start;gap:10px;padding:12px 14px;border-radius:12px;border:1px solid var(--color-line,rgba(128,128,128,.28));background:var(--color-surface-raised,var(--color-surface,light-dark(#fff,#222)));color:var(--color-text,light-dark(#1a1c20,#eee));box-shadow:0 8px 24px light-dark(rgba(15,23,42,.12),rgba(0,0,0,.28));font:12.5px/1.45 var(--font-ui,system-ui)}
    .ekd-toast.is-warning{border-color:color-mix(in srgb,var(--color-warning,#b4761f) 45%,var(--color-line,#8884))}
    .ekd-toast.is-error{border-color:color-mix(in srgb,var(--color-danger,#c64b52) 45%,var(--color-line,#8884))}
    .ekd-toast-body{flex:1;min-width:0}
    .ekd-toast-body strong{display:block;font-weight:600;margin-bottom:2px}
    .ekd-toast-body p{margin:0;color:var(--color-text-muted,var(--color-text-secondary,light-dark(#5c6370,#aaa)))}
    .ekd-toast-close{flex:none;border:0;background:transparent;color:var(--color-text-muted,var(--color-text-secondary,light-dark(#5c6370,#aaa)));font-size:16px;line-height:1;cursor:pointer;padding:0 2px}
    @media(prefers-reduced-motion:reduce){.ekd-session-picker{scroll-behavior:auto}}
  `;
  document.head.append(nativeStyle);
  // Entry rows are cloned from official rows by the host module. The module owns
  // only reconciliation; this renderer keeps the single MutationObserver and asks
  // for a sync from `ensure()`.
  state.entryMount = module.createEntryMountManager({
    document,
    openPanel: () => panelContract()?.showPanel?.(),
    onError: (pluginId, error) => {
      if (pluginId) { noteFault(pluginId, error); return; }
      if (error?.message !== '已取消选择') console.warn('native entry failed', error?.message || error);
    },
  });
  const SETTINGS_ICON = 'm12 3 2.5 5 5.5 1-4 4 .8 5.5L12 16l-4.8 2.5L8 13 4 9l5.5-1L12 3Z';
  const CACHE_ICON = 'M4 19V5 M4 19h16 M8 16v-4 M12 16V8 M16 16v-6';
  function paintIcon(button, path) {
    const existing = button.querySelector('svg');
    if (!existing) return;
    existing.setAttribute('viewBox', '0 0 24 24');
    existing.setAttribute('fill', 'none');
    existing.setAttribute('stroke', 'currentColor');
    existing.setAttribute('stroke-width', '1.6');
    existing.setAttribute('stroke-linecap', 'round');
    existing.setAttribute('stroke-linejoin', 'round');
    existing.innerHTML = '<path d="' + path + '"></path>';
  }
  function replaceIcon(button) {
    paintIcon(button, SETTINGS_ICON);
  }
  function replaceCacheIcon(button) {
    paintIcon(button, CACHE_ICON);
  }
  // The built-in same-screen entry is the only entry this file still owns. Plugin
  // entries arrive as definitions from their own code, so disabling a plugin
  // removes both the plan and the row without a hardcoded planner surface here.
  const ENTRY_ICONS = {
    split: 'M4 6a2 2 0 0 1 2-2h12a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2Z M12 4v16',
  };
  function builtinEntryPlans() {
    if (!state.features.multiSession) return [];
    return [{
      action: 'split',
      title: '添加同屏会话',
      icon: ENTRY_ICONS.split,
      async open() {
        if (state.sessionHandle) window.dispatchEvent(new CustomEvent('ekd:add-session'));
        else {
          const id = await pickSession([activeSession()]);
          if (id) await mountSessions([id]);
        }
      },
    }];
  }
  function pluginEntryPlans() {
    return [...state.pluginEntries].map(([key, entry]) => ({
      action: 'plugin:' + key,
      title: entry.definition.title,
      icon: entry.definition.icon,
      pluginId: entry.pluginId,
      open: () => entry.definition.open(),
    }));
  }
  function sidebarNavPlans() {
    return [...state.pluginSidebarNav].map(([key, entry]) => ({
      action: 'sidebar:' + key,
      title: entry.definition.title,
      icon: entry.definition.icon,
      pluginId: entry.pluginId,
      open: () => entry.definition.open(),
    }));
  }
  let lastOverlayPayload = '';
  function allEntryPlans() {
    return [...builtinEntryPlans(), ...pluginEntryPlans()];
  }
  function syncOverlayEntries(entries) {
    if (options.popout || windowsLoopback) return;
    const next = entries.map(entry => ({ action: entry.action, title: entry.title, icon: entry.icon || '' }));
    const encoded = nativeStringify(next);
    if (encoded === lastOverlayPayload) return;
    lastOverlayPayload = encoded;
    void transport.request('host.syncOverlayEntries', { entries: next }).catch(() => {});
  }
  function invokeEntryPlan(action) {
    const plan = allEntryPlans().find(item => item.action === action);
    if (!plan) return false;
    void Promise.resolve()
      .then(() => plan.open())
      .catch(error => {
        if (plan.pluginId) { noteFault(plan.pluginId, error); return; }
        if (error?.message !== '已取消选择') console.warn('native entry failed', error?.message || error);
      });
    return true;
  }
  function scheduleContractReport(probes, immediate = false) {
    if (options.surface !== 'host' || state.removed) return;
    const digest = nativeStringify(probes.map(p => [p.id, p.status, p.reason || '', p.detail || '']));
    if (digest === state.lastContractDigest) return;
    state.lastContractDigest = digest;
    const send = () => {
      state.contractReportTimer = null;
      if (state.removed) return;
      void transport.request('contracts.report', {
        appVersion: options.appVersion || '',
        electron: options.electron || '',
        probes,
      }).catch(() => {});
    };
    if (state.contractReportTimer) {
      clearTimeout(state.contractReportTimer);
      state.contractReportTimer = null;
    }
    if (immediate) send();
    else state.contractReportTimer = setTimeout(send, 120);
  }
  function syncEntryMount(gates) {
    const entries = allEntryPlans();
    state.entryMount?.sync(entries, sidebarNavPlans(), gates);
    syncOverlayEntries(gates?.rightPanel === false ? [] : entries);
  }
  function removeEntry(action) {
    state.entryMount?.remove(action);
  }
  function applyFeatures(next, initial) {
    const previous = state.features;
    state.features = { multiSession: next?.multiSession === true };
    if (!state.features.multiSession) {
      if (state.sessionContainer) closeSessions();
      removeEntry('split');
    }
    if (!initial && previous.multiSession !== state.features.multiSession) ensure();
  }
  async function refreshFeatures() {
    try {
      applyFeatures(await transport.request('features.get'), true);
    } catch {
      applyFeatures(null, true);
    }
  }
  // The official host re-renders its lists on its own schedule. The entry mount
  // reconciles every container on each sync, so a re-rendered session-info group
  // gets its plugin row back without a stale module-level reference deciding
  // whether it "already exists".
  function syncNativeEntries() {
    try {
      syncNativeEntriesInner();
    } catch (error) {
      contractDegraded('官方界面同步', error);
    }
  }
  function syncNativeEntriesInner() {
    const firstEvaluation = state.contracts === null;
    const evaluation = module.evaluateDOMContracts(document, state.contractState);
    state.contractState = evaluation.state;
    state.contracts = evaluation;
    const gates = {
      sessionInfo: evaluation.byId['dom.session_info_available']?.status !== 'disabled',
      rightPanel: evaluation.byId['dom.right_panel']?.status !== 'disabled',
      sidebarNav: evaluation.byId['dom.sidebar_nav']?.status !== 'disabled',
    };
    syncEntryMount(gates);
    // The sidebar cache widget used to be mounted here. It is now the preinstalled
    // Monitor plugin, which mounts through the plugin runtime's own sidebar slot,
    // so this file owns no cache surface and no private channel for one.
    if (evaluation.byId['dom.sidebar_footer']?.status === 'disabled') {
      for (const anchor of document.querySelectorAll('[data-ekdp-sidebar-anchor]')) anchor.remove();
    }
    if (evaluation.byId['dom.settings_dialog']?.status === 'disabled') {
      deactivateSettings();
      detachSettings();
    } else {
      syncSettingsTab();
      syncOfficialSettingsSections(evaluation);
    }
    if (evaluation.byId['dom.settings_providers']?.status === 'disabled') {
      state.providerEnhance?.setEnabled?.(false);
      state.providerEnhance?.clearDecorations?.();
    } else {
      state.providerEnhance?.setEnabled?.(true);
    }
    syncAccountMenu(evaluation);
    syncSessionInfoKeepAlive();
    syncUpdatePill();
    if (!gates.rightPanel) {
      if (state.activePluginPanel) deactivatePluginPanel(state.activePluginPanel, false);
      for (const entry of state.pluginPanels.values()) {
        if (entry.wrapper.isConnected) entry.wrapper.remove();
      }
    } else {
      if (state.activePluginPanel && panelContract()?.activeTabId?.value) {
        deactivatePluginPanel(state.activePluginPanel, false);
      }
      for (const [id, entry] of state.pluginPanels) {
        syncPluginPanelTab(entry);
        if (state.activePluginPanel === id) {
          const body = panelBody();
          if (body) {
            body.dataset.ekdpPanel = id;
            if (entry.host.parentElement !== body) body.append(entry.host);
          }
        }
      }
    }
    if (state.sessionContainer && !state.sessionContainer.isConnected) closeSessions();
    if (state.sessionContainer) state.sessionContainer.setAttribute('data-ekd-containment', 'sessions');
    scheduleContractReport(evaluation.probes, firstEvaluation || Boolean(module.findSettingsDialogParts(document)));
  }
  const legacyZombieTimers = [];
  function reclaimLegacyZombie() {
    if (state.removed || isStale() || !publishedUI) return;
    const current = window.__ekdNativeUI;
    if (!current || current === publishedUI || current === pendingUI) return;
    window.__ekdNativeUI = publishedUI;
    if (typeof current.remove === 'function') {
      void Promise.resolve()
        .then(() => current.remove())
        .catch(() => {})
        .finally(() => {
          if (!state.removed && !isStale()) {
            if (window.__ekdNativeUI !== publishedUI) window.__ekdNativeUI = publishedUI;
            if (publishedPlugins && window.__ekdPlugins !== publishedPlugins) window.__ekdPlugins = publishedPlugins;
            if (publishedBrowserNav && window.__ekdPluginBrowserNavigated !== publishedBrowserNav) window.__ekdPluginBrowserNavigated = publishedBrowserNav;
            queueMicrotask(() => { if (!state.removed && !isStale()) ensure(); });
          }
        });
    }
  }
  function ensure() {
    if (state.removed || isStale()) {
      state.observer?.disconnect();
      state.settingsObserver?.disconnect();
      return;
    }
    // Every observed DOM change used to run a full contract evaluation in the
    // same microtask, so a typing session could queue dozens of passes. One
    // animation frame consolidates them into a single sync.
    if (state.ensureFrame !== null) return;
    const run = () => {
      state.ensureFrame = null;
      if (state.removed || isStale()) return;
      runEnsure();
    };
    state.ensureFrame = typeof requestAnimationFrame === 'function'
      ? requestAnimationFrame(run)
      : setTimeout(run, 16);
    state.ensureCancel = typeof cancelAnimationFrame === 'function' ? cancelAnimationFrame : clearTimeout;
  }
  function runEnsure() {
    if (state.removed || isStale()) {
      state.observer?.disconnect();
      state.settingsObserver?.disconnect();
      return;
    }
    reclaimLegacyZombie();
    state.observer?.disconnect();
    state.settingsObserver?.disconnect();
    try {
      syncNativeEntries();
    } finally {
      if (!state.removed && !isStale()) {
        state.observer?.observe(document.body, { childList: true, subtree: true });
        if (state.settingsDialog?.isConnected && state.settingsObserver) {
          state.settingsObserver.observe(state.settingsDialog, {
            childList: true,
            subtree: true,
            attributes: true,
            attributeFilter: ['style', 'aria-selected', 'class'],
          });
        }
      }
    }
  }
  let removePromise = null;
  state.remove = () => (removePromise ??= (async () => {
    if (state.removed) return;
    state.removed = true;
    if (state.ensureFrame !== null) {
      state.ensureCancel?.(state.ensureFrame);
      state.ensureFrame = null;
    }
    while (legacyZombieTimers.length > 0) clearTimeout(legacyZombieTimers.pop());
    state.observer?.disconnect();
    state.settingsObserver?.disconnect();
    state.settingsObserver = null;
    state.unsubscribeSession?.();
    state.unsubscribeConfig?.();
    state.unsubscribeAccounts?.();
    state.unsubscribeKeepAlive?.();
    state.unsubscribeAutomation?.();
    state.unsubscribeSchedule?.();
    state.unsubscribeFeatures?.();
    state.unsubscribePlugins?.();
    disposeUpdateSurface();
    state.channel?.close();
    state.restoreNotifyCompletion?.();
    state.restoreNotifyCompletion = null;
    window.removeEventListener('ekd:config-saved', onConfigSaved);
    window.removeEventListener('ekd:account-switched', onAccountSwitched);
    closeAccountSwitchModal();
    closeKeepAliveMenu();
    for (const node of document.querySelectorAll('[data-ekd-account-menu],[data-ekd-keepalive-row],[data-ekd-keepalive-wrap],[data-ekd-toast-stack],[data-ekd-update-pill],[data-ekd-update-popover],[data-ekd-plugins-tab],[data-ekd-settings-tab],[data-ekd-official-section],[data-ekd-account-modal],[data-ekd-keepalive-popover]')) node.remove();
    document.removeEventListener('visibilitychange', pluginRefresh);
    window.removeEventListener('focus', pluginRefresh);
    for (const release of [...state.pluginReleases.values()]) release();
    for (const entry of [...state.pluginPanels.values()]) entry.dispose?.();
    state.pluginPanels.clear();
    for (const handle of [...state.pluginMainAreas.values()]) handle.dispose();
    state.pluginMainAreas.clear();
    state.pluginEntries.clear();
    state.pluginReleases.clear();
    state.pluginRevisions.clear();
    for (const release of [...state.pluginSettings.values()]) release.dispose();
    state.pluginSettings.clear();
    state.browserNavListeners.clear();
    if (window.__ekdPluginBrowserNavigated === publishedBrowserNav) delete window.__ekdPluginBrowserNavigated;
    state.plugins?.dispose();
    state.plugins = null;
    if (window.__ekdPlugins === publishedPlugins) delete window.__ekdPlugins;
    document.removeEventListener('click', nativeTabClick, true);
    document.removeEventListener('click', sidebarResetClick, true);
    document.removeEventListener('click', settingsTabClick, true);
    document.removeEventListener('scroll', settingsScroll, true);
    clearTimeout(state.settingsScrollTimer);
    clearTimeout(state.contractReportTimer);
    state.contractReportTimer = null;
    state.picker?.close();
    deactivateSettings();
    detachSettings();
    closeSessions();
    for (const handle of [...state.handles]) unmountHandle(handle);
    state.entryMount?.dispose();
    state.entryMount = null;
    state.pluginSidebarNav.clear();
    lastOverlayPayload = '';
    syncOverlayEntries([]);
    state.providerEnhance?.remove();
    state.providerEnhance = null;
    nativeStyle.remove();
    URL.revokeObjectURL(moduleURL);
    if (window.__ekdNativeUI === publishedUI || window.__ekdNativeUI === pendingUI) delete window.__ekdNativeUI;
    await Promise.race([unwatchActiveSession(), new Promise(resolve => setTimeout(resolve, 250))]);
  })());
  activeRemove = state.remove;
  if (isStale()) {
    await state.remove();
    return { status: 'removed', surface: options.surface || 'host', domRevision: 1 };
  }
  const nativeTabClick = event => {
    const clickedNative = event.target.closest('.ptb-tab:not([data-ekdp-plugin-panel])')
      || event.target.closest('.sit-link:not([data-ekd-entry]), .pt-body .pl button.ui-menu-item:not([data-ekd-entry])');
    if (!clickedNative) return;
    if (clickedNative.classList?.contains('ptb-tab') && event.target.closest('.ptb-x')) return;
    if (state.activePluginPanel) deactivatePluginPanel(state.activePluginPanel, false);
  };
  const sidebarResetClick = event => {
    const newChat = event.target.closest('button.btn-new-chat:not([data-ekd-sidebar-nav])');
    const sessionRow = event.target.closest('[data-session-id]');
    const clickedSession = sessionRow && !event.target.closest('[data-session-id] button:not(.row)');
    if (!newChat && !clickedSession) return;
    for (const handle of [...state.pluginMainAreas.values()]) {
      if (handle.mode === 'page') handle.dispose();
    }
  };
  const settingsTabClick = event => {
    const clicked = event.target.closest('.settings-tab-list button.tab[role="tab"]');
    const tab = clicked && !clicked.hasAttribute('data-ekd-settings-tab') ? clicked : null;
    if (tab) {
      // An official tab always wins: the custom mount owns the body only while
      // one of its own tabs is selected.
      if (state.settingsActiveTab) deactivateSettings();
      tab.classList.add('on');
      tab.setAttribute('aria-selected', 'true');
      setTimeout(() => ensure(), 40);
    }
  };
  const settingsScroll = event => {
    if (!state.settingsActiveTab || event.target !== state.settingsBody) return;
    const body = state.settingsBody;
    body.classList.add('scrolling');
    clearTimeout(state.settingsScrollTimer);
    state.settingsScrollTimer = setTimeout(() => body.classList.remove('scrolling'), 900);
  };
  document.addEventListener('click', settingsTabClick, true);
  document.addEventListener('scroll', settingsScroll, true);
  function applyStandaloneTheme(theme) {
    const scheme = theme === 'light' ? 'light' : 'dark';
    document.documentElement.dataset.colorScheme = scheme;
    // A standalone window has no host stylesheet declaring color-scheme, so the
    // light-dark() fallbacks resolve from this inline declaration.
    document.documentElement.style.colorScheme = scheme;
  }
  if (options.surface === 'plugin') {
    applyStandaloneTheme(options.theme);
    document.title = 'KimiCode+ 增强插件';
    document.body.style.cssText = 'margin:0;height:100vh;overflow:hidden;background:var(--color-surface,var(--color-bg,light-dark(#fff,#121212)));color:var(--color-text,light-dark(#1a1c20,#eee))';
    state.popoutRoot = document.createElement('div');
    state.popoutRoot.style.cssText = 'height:100vh;min-height:0;overflow:hidden';
    document.body.replaceChildren(state.popoutRoot);
    window.__ekdNativeUI = publishNativeUI();
  } else if (options.surface === 'settings') {
    applyStandaloneTheme(options.theme);
    document.title = 'KimiCode+ 设置';
    document.body.style.cssText = 'margin:0;height:100vh;overflow:hidden;background:var(--color-surface,var(--color-bg,light-dark(#fff,#121212)));color:var(--color-text,light-dark(#1a1c20,#eee))';
    const element = document.createElement('div');
    element.style.cssText = 'height:100vh;min-height:0';
    document.body.replaceChildren(element);
    const host = makeHost(() => { void transport.request('window.close'); });
    mount(element, options.surface, host, {});
    window.__ekdNativeUI = publishNativeUI();
    return { status: 'installed', surface: options.surface };
  }
  document.addEventListener('click', nativeTabClick, true);
  document.addEventListener('click', sidebarResetClick, true);
  publishNativeUI();
  await refreshFeatures();
  if (state.removed) return { status: 'removed', surface: 'host', domRevision: 1 };
  if (isStale()) {
    await state.remove();
    return { status: 'removed', surface: 'host', domRevision: 1 };
  }
  legacyZombieTimers.push(setTimeout(reclaimLegacyZombie, 300), setTimeout(reclaimLegacyZombie, 1000));
  state.unsubscribeFeatures = makeHost(() => {}).subscribe('features', value => applyFeatures(value, false));
  if (options.surface === 'host') {
    // The update indicator lives in the chat header, so only the official window
    // subscribes. The first read seeds the pill without waiting for a status
    // change event; a failed read just leaves the pill hidden.
    state.unsubscribeUpdate = makeHost(() => {}).subscribe('update', applyUpdateStatus);
    void transport.request('update.status').then(applyUpdateStatus).catch(() => {});
  }
  let watchChain = Promise.resolve();
  function syncActiveSessionWatch(targetId) {
    watchChain = watchChain.then(async () => {
      try {
        const next = state.removed ? '' : (targetId || '');
        const prev = state.watchedSession || '';
        if (next === prev) return;
        if (prev) {
          state.watchedSession = '';
          await transport.request('session.unwatch', { id: prev }).catch(() => {});
        }
        if (next && !state.removed) {
          const result = await transport.request('session.watch', { id: next });
          if (state.removed) {
            await transport.request('session.unwatch', { id: next }).catch(() => {});
          } else if (result?.watching === false) {
            // The read succeeded but the official stream did not open. No
            // reference is held, so leave the watched id empty and let the next
            // session change or plugin sync retry instead of pretending to watch.
            state.watchedSession = '';
          } else {
            state.watchedSession = next;
          }
        }
      } catch (error) {
        // KAP may not have finished opening yet, or the bridge is reconnecting;
        // the next session change or plugin sync retries. A rejected link in
        // the chain would skip every later session transition.
        contractDegraded('官方会话实时订阅', error);
      }
    });
    return watchChain;
  }
  function unwatchActiveSession() {
    return syncActiveSessionWatch('');
  }
  let previousSession = activeSession();
  if (previousSession) void syncActiveSessionWatch(previousSession);
  const sessionStore = piniaStore('kimi.sessions');
  state.unsubscribeSession = probe('官方会话状态订阅', () => sessionStore?.$subscribe?.(() => {
    try {
      const id = activeSession();
      if (id === previousSession) return;
      previousSession = id;
      if (!id && state.sessionHandle) closeSessions();
      for (const handle of [...state.pluginMainAreas.values()]) {
        if (handle.mode === 'page') handle.dispose();
      }
      void syncActiveSessionWatch(id);
      closeKeepAliveMenu();
      if (id) void fetchSessionKeepAlive(id, true);
      else syncSessionInfoKeepAlive();
      window.dispatchEvent(new CustomEvent('ekd:active-session', { detail: id }));
    } catch (error) {
      contractDegraded('官方会话状态订阅', error);
    }
  }, { detached: true, flush: 'sync' }), null);
  const configStore = piniaStore('kimi.config');
  let lastConfigSnapshot = nativeStringify(probe('官方配置', () => configStore?.config ?? null, null));
  state.unsubscribeConfig = probe('官方配置订阅', () => configStore?.$subscribe?.(() => {
    try {
      const nextSnapshot = nativeStringify(probe('官方配置', () => configStore?.config ?? null, null));
      if (nextSnapshot === lastConfigSnapshot) return;
      lastConfigSnapshot = nextSnapshot;
      window.dispatchEvent(new CustomEvent('ekd:official-config-changed'));
    } catch (error) {
      contractDegraded('官方配置订阅', error);
    }
  }, { detached: true }), null);
  const onConfigSaved = () => {
    const store = piniaStore('kimi.config');
    if (typeof store?.loadConfig === 'function') {
      void store.loadConfig().then(() => {
        lastConfigSnapshot = nativeStringify(probe('官方配置', () => store?.config ?? null, null));
      }).catch(error => contractDegraded('官方配置刷新', error));
    }
  };
  window.addEventListener('ekd:config-saved', onConfigSaved);
  const onAccountSwitched = () => {
    void refreshOfficialAuth();
  };
  window.addEventListener('ekd:account-switched', onAccountSwitched);
  const accountEventHost = makeHost(() => {});
  state.unsubscribeAccounts = accountEventHost.subscribe('accounts', event => {
    if (event?.type === 'account.switched' && event?.refreshAuth) {
      // The section also dispatches this event for the sidebar; both paths merge
      // into one official-store refresh instead of racing each other.
      window.dispatchEvent(new CustomEvent('ekd:account-switched', { detail: event }));
      void refreshOfficialAuth();
    } else {
      void refreshAccountsCache(true);
    }
  });
  installKeepAliveNotifySuppress();
  state.unsubscribeKeepAlive = accountEventHost.subscribe('keepalive', event => {
    if (!event || typeof event !== 'object') return;
    if (event.type === 'keepalive.suppress' && event.sessionId) {
      const expiresAt = Date.parse(event.expiresAt || '') || (Date.now() + 5 * 60 * 1000);
      state.keepAliveSuppress.set(event.sessionId, { promptId: event.promptId || '', expiresAt });
      installKeepAliveNotifySuppress();
      return;
    }
    if (event.type === 'keepalive.alert') {
      pushKeepAliveToast(event);
      if (event.sessionId) void fetchSessionKeepAlive(event.sessionId, true);
      return;
    }
    if (event.type === 'keepalive.session' && event.sessionId && event.view) {
      state.keepAliveCache.set(event.sessionId, { view: event.view, fetchedAt: Date.now() });
      syncSessionInfoKeepAlive();
    }
  });
  state.unsubscribeAutomation = accountEventHost.subscribe('automation', () => {
    const currentId = activeSession();
    if (currentId) void fetchSessionKeepAlive(currentId, true);
  });
  state.unsubscribeSchedule = accountEventHost.subscribe('schedule', event => {
    if (!event || typeof event !== 'object' || event.kind !== 'run.notify' || !event.notification) return;
    if (typeof module.dispatchSchedulerNotification !== 'function') return;
    module.dispatchSchedulerNotification(event.notification, {
      document,
      storage: window.localStorage,
      seenTags: state.seenSchedulerNotifyTags,
      showWindow: () => { window.kimiDesktop?.showWindow?.(); },
      focusWindow: () => { window.focus(); },
      openSession: id => openOfficial(id),
      openRuns: detail => {
        window.dispatchEvent(new CustomEvent('ekd:scheduler-open-runs', { detail }));
      },
    });
  });
  void refreshAccountsCache(true);
  ensure();
  state.observer = new MutationObserver(records => {
    // Only structural changes can move a contract target. Attribute churn (class
    // toggles such as `scrolling`, inline style during scrolling) is covered by
    // the settings observer where it actually matters, so it is not watched on
    // the whole document.
    if (Array.isArray(records) && records.length > 0 && records.every(record => record.type === 'attributes')) {
      const meaningful = records.some(record => (record.attributeName || '') !== 'class' && (record.attributeName || '') !== 'style');
      if (!meaningful) return;
    }
    ensure();
  });
  state.observer.observe(document.body, { childList: true, subtree: true });
  // Plugin host wiring. The runtime implementation lives in the host bundle
  // (exported from host-entry); this file only supplies mount points and drives
  // the lifecycle.
  //
  // All plugin traffic goes through `core.fetch`, which is the only channel the
  // bridge allows, so the plugin runtime sees the same authenticated core the
  // settings surface does. The identity token travels in the body: the bridge
  // forwards a fixed header allowlist and would drop a custom header.
  //
  // Reads are GET and actions are POST. Sending a POST to a GET-only route is not
  // a harmless mismatch: the router answers 405, so every list and asset fetch
  // failed and no plugin ever activated.
  // A plugin surface goes above the official footer, not below it: the footer is
  // the settings/help entry and must stay the last row of the column. The host
  // owns this anchor, so a plugin cannot reorder the official sidebar.
  const sidebarAnchor = () => {
    const footer = document.querySelector('.side-footer');
    const column = footer?.parentElement;
    if (!footer || !column) return null;
    let anchor = column.querySelector('[data-ekdp-sidebar-anchor]');
    if (!anchor) {
      anchor = document.createElement('div');
      anchor.dataset.ekdpSidebarAnchor = '';
      anchor.className = 'ekdp-sidebar-anchor';
      column.insertBefore(anchor, footer);
    } else if (anchor.nextElementSibling !== footer) {
      // The official UI may re-render the column; keep the anchor adjacent.
      column.insertBefore(anchor, footer);
    }
    return anchor;
  };
  const pluginSlots = {
    sidebar: sidebarAnchor,
    settings: () => state.settingsBody,
    thinking: () => document.querySelector('.chat-main') ?? null,
    message: () => document.querySelector('.chat-main') ?? null,
  };

  // --- API v2 host surfaces -------------------------------------------------
  //
  // Every surface here belongs to exactly one plugin and is released by that
  // plugin's Scope. Nothing is shared between plugins, so stopping one plugin
  // cannot leave another plugin's pane or tab behind.

  function localScope() {
    let items = [];
    let disposed = false;
    return {
      add(cleanup) {
        if (cleanup === undefined || cleanup === null) return;
        const run = typeof cleanup === "function" ? cleanup : () => cleanup.dispose();
        if (disposed) {
          try { run(); } catch { /* a failing teardown must not strand the rest */ }
          return;
        }
        items.push(run);
      },
      dispose() {
        if (disposed) return;
        disposed = true;
        for (const run of items.reverse()) {
          try { run(); } catch { /* one failing teardown must not skip the rest */ }
        }
        items = [];
      },
    };
  }

  function pluginTabContainers() {
    return [...document.querySelectorAll(".ptb-tabs")];
  }

  function tabAttributes(container) {
    return [...(container?.attributes ?? [])].filter(attribute => attribute.name.startsWith("data-v-"));
  }

  const pluginEntryHost = {
    register(pluginId, definition) {
      if (!definition || typeof definition.id !== "string" || !definition.id || typeof definition.open !== "function") {
        throw new Error("插件入口必须提供 id 和 open()");
      }
      if (typeof definition.title !== "string" || !definition.title.trim()) throw new Error("插件入口必须有 title");
      const key = pluginId + ":" + definition.id;
      state.pluginEntries.set(key, { pluginId, definition });
      syncNativeEntries();
      if (options.surface === 'plugin' && pluginId === options.pluginId) queueMicrotask(() => runPluginEntry(pluginId, definition));
      // Dropping the registry alone would leave the button in the container: the
      // next sync only reconciles the entries still planned, so the removed one
      // has to take its own element with it.
      return { dispose: () => {
        state.pluginEntries.delete(key);
        removeEntry('plugin:' + key);
        syncNativeEntries();
      } };
    },
  };
  const pluginSidebarNavHost = {
    register(pluginId, definition) {
      if (!definition || typeof definition.id !== "string" || !definition.id || typeof definition.open !== "function") {
        throw new Error("侧栏入口必须提供 id 和 open()");
      }
      if (typeof definition.title !== "string" || !definition.title.trim()) throw new Error("侧栏入口必须有 title");
      const key = pluginId + ":" + definition.id;
      state.pluginSidebarNav.set(key, { pluginId, definition });
      syncNativeEntries();
      return { dispose: () => {
        state.pluginSidebarNav.delete(key);
        removeEntry('sidebar:' + key);
        syncNativeEntries();
      } };
    },
  };

  function runPluginEntry(pluginId, definition) {
    Promise.resolve()
      .then(() => definition.open())
      .catch(error => {
        noteFault(pluginId, Object.assign(error instanceof Error ? error : new Error(String(error)), { code: "PLUGIN_ENTRY_FAILED" }));
      });
  }

  // A main-area mount wraps the native chat column so the plugin's pane sits
  // beside it and the split width shrinks the native column. Unwrapping puts the
  // exact DOM the official shell produced back where it was.
  const pluginMainAreaHost = {
    mount(pluginId, definition) {
      for (const handle of [...state.pluginMainAreas.values()]) handle.dispose();
      // The wrapper takes the whole native chat column. A live session host is
      // a direct child of that column, would be wrapped too, and is then hidden
      // by the session CSS - the main area goes blank. Restore the official
      // children first, then wrap them.
      if (state.sessionHandle || state.sessionHostElement || state.sessionRelease) closeSessions();
      const container = document.querySelector(".composer")?.closest(".chat-main") || document.querySelector(".chat")?.closest(".chat-main");
      if (!container) throw new Error("主聊天区契约未就绪");
      const mount = module.mountMainAreaSurface({ document, container, pluginId, definition, window });
      const release = () => {
        if (state.pluginMainAreas.get(pluginId) === handle) state.pluginMainAreas.delete(pluginId);
        mount.dispose();
      };
      const handle = {
        mode: definition?.mode === 'page' ? 'page' : 'split',
        setWidth: mount.setWidth,
        width: mount.width,
        dispose: release,
      };
      state.pluginMainAreas.set(pluginId, handle);
      return handle;
    },
  };

  function panelBody() {
    return document.querySelector(".pt-body");
  }

  function panelContract() {
    const shell = document.querySelector('.pt-shell');
    const root = document.querySelector('#app')?._vnode;
    const cached = state.panelContract;
    if (cached?.shell === shell && cached?.root === root && cached?.value) return cached.value;
    const value = shell && panel();
    state.panelContract = { shell, root, value };
    return value;
  }

  function syncPluginPanelTab(entry) {
    const tabs = document.querySelector(".ptb-tabs");
    if (!tabs) return;
    if (!entry.opened) {
      if (entry.wrapper.isConnected) entry.wrapper.remove();
      return;
    }
    const attributes = tabAttributes(tabs);
    for (const element of [entry.wrapper, entry.tab, entry.titleSpan, entry.close]) {
      for (const attribute of attributes) {
        if (element.getAttribute(attribute.name) !== attribute.value) {
          element.setAttribute(attribute.name, attribute.value);
        }
      }
    }
    const dragFill = tabs.querySelector(".ptb-drag-fill");
    const needsMove = entry.wrapper.parentElement !== tabs
      || (dragFill && (dragFill.compareDocumentPosition(entry.wrapper) & Node.DOCUMENT_POSITION_FOLLOWING));
    if (needsMove) tabs.insertBefore(entry.wrapper, dragFill || null);
  }

  function activatePluginPanel(pluginId) {
    const entry = state.pluginPanels.get(pluginId);
    const body = panelBody();
    const contract = panelContract();
    if (!entry || !body) return;
    if (state.activePluginPanel && state.activePluginPanel !== pluginId) {
      deactivatePluginPanel(state.activePluginPanel, false);
    }
    entry.opened = true;
    if (contract?.activeTabId?.value) entry.returnTabId = contract.activeTabId.value;
    if (contract?.activeTabId) contract.activeTabId.value = null;
    contract?.showPanel?.();
    syncPluginPanelTab(entry);
    if (entry.host.parentElement !== body) body.append(entry.host);
    body.dataset.ekdpPanel = pluginId;
    state.activePluginPanel = pluginId;
    for (const [id, other] of state.pluginPanels) {
      const active = id === pluginId;
      other.host.hidden = !active;
      other.tab.setAttribute("aria-selected", String(active));
      other.wrapper.classList.toggle("on", active);
    }
  }

  function deactivatePluginPanel(pluginId, hidePanel = true) {
    const entry = state.pluginPanels.get(pluginId);
    if (!entry) return;
    entry.tab.setAttribute("aria-selected", "false");
    entry.wrapper.classList.remove("on");
    entry.host.hidden = true;
    if (hidePanel) {
      entry.opened = false;
      entry.wrapper.remove();
    }
    const wasActive = state.activePluginPanel === pluginId;
    if (wasActive) {
      state.activePluginPanel = null;
      const body = panelBody();
      if (body) delete body.dataset.ekdpPanel;
    }
    if (hidePanel && wasActive) {
      const returnTabId = entry.returnTabId;
      entry.returnTabId = null;
      const nextPlugin = [...state.pluginPanels.entries()].find(([id, other]) => id !== pluginId && other.opened)?.[0];
      if (nextPlugin) {
        activatePluginPanel(nextPlugin);
        return;
      }
      const contract = panelContract();
      const tabs = contract?.panelTabs?.value;
      if (Array.isArray(tabs) && tabs.length) {
        const targetId = returnTabId && tabs.some(tab => tab.id === returnTabId) ? returnTabId : tabs[0].id;
        contract.activateTab?.(targetId);
        return;
      }
      contract?.hidePanel?.();
    } else if (hidePanel) {
      entry.returnTabId = null;
    }
  }

  const pluginRightPanelHost = {
    mount(pluginId, definition) {
      if (options.surface === 'plugin') {
        if (pluginId !== options.pluginId || !state.popoutRoot) throw new Error('插件窗口只能挂载所属插件');
        const host = state.popoutRoot;
        const mountScope = localScope();
        document.title = definition?.title || 'KimiCode+ 插件';
        try {
          mountScope.add(definition.render(host));
        } catch (error) {
          mountScope.dispose();
          host.replaceChildren();
          throw error;
        }
        let released = false;
        return {
          open: () => { if (!released) host.hidden = false; },
          close: () => { void transport.request('window.close'); },
          get open_() { return !released; },
          dispose: () => {
            if (released) return;
            released = true;
            mountScope.dispose();
            host.replaceChildren();
          },
        };
      }
      if (state.pluginPanels.has(pluginId)) state.pluginPanels.get(pluginId)?.dispose?.();
      const wrapper = document.createElement("div");
      wrapper.className = "ptb-tab";
      wrapper.dataset.ekdpPluginPanel = pluginId;
      const tab = document.createElement("button");
      tab.type = "button";
      tab.className = "ptb-tab-main";
      tab.setAttribute("role", "tab");
      tab.setAttribute("aria-selected", "false");
      tab.tabIndex = 0;
      const titleSpan = document.createElement("span");
      titleSpan.className = "panel-tab-title";
      titleSpan.textContent = definition?.title || "插件面板";
      tab.append(titleSpan);
      const close = document.createElement("button");
      close.type = "button";
      close.className = "ptb-x";
      close.setAttribute("aria-label", "关闭面板");
      close.tabIndex = 0;
      close.innerHTML = '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d="M17.9542 4.77253C18.3056 4.42106 18.8761 4.42106 19.2276 4.77253C19.579 5.12401 19.579 5.69452 19.2276 6.04597L13.2735 12.0001L19.2276 17.9542C19.5791 18.3056 19.5791 18.8761 19.2276 19.2276C18.8761 19.5791 18.3056 19.5791 17.9542 19.2276L12.0001 13.2735L6.04595 19.2276C5.69451 19.5791 5.12399 19.579 4.77252 19.2276C4.42104 18.8761 4.42104 18.3056 4.77252 17.9542L10.7266 12.0001L4.77252 6.04597C4.42104 5.6945 4.42104 5.124 4.77252 4.77253C5.12399 4.42107 5.69448 4.42106 6.04595 4.77253L12.0001 10.7266L17.9542 4.77253Z" fill="currentColor"></path></svg>';
      wrapper.append(tab, close);
      const host = document.createElement("div");
      host.className = "ekdp-panel-host";
      host.dataset.ekdpPlugin = pluginId;
      host.hidden = true;
      panelBody()?.append(host);
      const mountScope = localScope();
      try {
        mountScope.add(definition.render(host));
      } catch (error) {
        wrapper.remove();
        host.remove();
        throw error;
      }
      const entry = { wrapper, tab, titleSpan, close, host, opened: false, returnTabId: null };
      state.pluginPanels.set(pluginId, entry);
      tab.addEventListener("click", () => activatePluginPanel(pluginId));
      close.addEventListener("click", event => {
        event.preventDefault();
        event.stopPropagation();
        deactivatePluginPanel(pluginId);
      });
      let released = false;
      const dispose = () => {
        if (released) return;
        released = true;
        deactivatePluginPanel(pluginId);
        if (state.pluginPanels.get(pluginId) === entry) state.pluginPanels.delete(pluginId);
        mountScope.dispose();
        wrapper.remove();
        host.remove();
      };
      entry.dispose = dispose;
      return {
        open: () => activatePluginPanel(pluginId),
        close: () => deactivatePluginPanel(pluginId),
        get open_() { return state.activePluginPanel === pluginId; },
        dispose,
      };
    },
  };

  const pluginLayoutHost = { acquireSidebarCollapse: () => ({ dispose: acquireSidebarCollapse() }) };

  // The published native surface is a fixed, read-only set of host entry points.
  // `state` itself stays private: it carries the bridge transport and the plugin
  // runtime, and a page script that reached either of them could act with the
  // host's own core token or drive another plugin's scope.
  function publishNativeUI() {
    const ui = {
      mainBridgeCore: options.coreURL || '',
      remove: () => {
        if (installCtrl.seq === mySeq) installCtrl.seq += 1;
        return state.remove();
      },
      mountSessions: extraIds => mountSessions(extraIds),
      pickSession: exclude => pickSession(exclude),
      openOfficial: id => openOfficial(id),
      activateSession: id => activateSession(id),
      openSettings: tab => openSettings(tab),
      invokeEntry: action => invokeEntryPlan(action),
      contracts: () => (state.contracts?.probes || []).map(item => ({ ...item })),
      get plugins() {
        const runtime = state.plugins;
        if (!runtime) return null;
        return { stats: () => runtime.stats(), eventLoops: () => runtime.eventLoops() };
      },
      get sidebarRefs() { return state.sidebarRefs; },
      get sidebarCollapsed() { return state.sidebarCollapsed; },
    };
    // A plugin can reach `window.__ekdNativeUI` too. Freezing the surface keeps
    // the published entry points from being replaced or extended in place; the
    // private closure stays out of reach either way.
    if (freeze) freeze(ui);
    publishedUI = ui;
    window.__ekdNativeUI = ui;
    return ui;
  }
  // A plugin's own token is the only identity the browser bridge carries; the
  // host process asks the core whose it is. A closed plugin has already lost its
  // token, so close sends the token the runtime still holds in scope.
  //
  // Deleting a plugin's token must not leave its pages unclosable: the last
  // token it held is kept aside, and only `browser.view.close` may fall back to
  // it. Every other action keeps requiring a live token, so a revoked identity
  // can never open, navigate or run a command again.
  const pluginViewTokens = new Map();
  let closingView = false;
  function pluginToken(id) {
    const live = state.pluginTokens.get(id) || '';
    if (live) return live;
    return closingView ? (state.pluginRetiredTokens?.get(id) || '') : '';
  }
  function retirePluginToken(id) {
    const current = state.pluginTokens.get(id);
    if (current) (state.pluginRetiredTokens ??= new Map()).set(id, current);
  }
  const pluginWindowHost = {
    popout: async pluginId => { await transport.request('plugin.window.open', { token: pluginToken(pluginId), sourceSessionId: activeSession() }); },
    close: async () => { await transport.request('window.close'); },
  };
  const pluginCommandHost = {
    register: (pluginId, name) => transport.request('plugin.command.register', { token: pluginToken(pluginId), name }),
    unregister: (pluginId, name) => transport.request('plugin.command.unregister', { token: pluginToken(pluginId), name }),
    invoke: (pluginId, name, args) => transport.request('plugin.command.invoke', { token: pluginToken(pluginId), name, args }),
  };
  publishedBrowserNav = payload => {
    if (!payload || typeof payload.pluginId !== 'string' || typeof payload.viewId !== 'string' || typeof payload.url !== 'string') return;
    const listeners = state.browserNavListeners.get(payload.pluginId + ':' + payload.viewId);
    if (!listeners) return;
    for (const listener of [...listeners]) {
      try { listener(payload.url); } catch {}
    }
  };
  window.__ekdPluginBrowserNavigated = publishedBrowserNav;
  const pluginBrowserHost = {
    open: async (pluginId, viewId, url) => {
      const result = await transport.request("browser.view.open", { token: pluginToken(pluginId), viewId, url });
      // Remembered per view so a close still names the token that opened it,
      // even after a hot update replaced the plugin's live token.
      const token = state.pluginTokens.get(pluginId);
      if (token) pluginViewTokens.set(pluginId + '/' + viewId, token);
      return result;
    },
    navigate: (pluginId, viewId, url) => transport.request("browser.view.navigate", { token: pluginToken(pluginId), viewId, url }),
    history: async (pluginId, viewId, operation) => Boolean((await transport.request("browser.view.history", { token: pluginToken(pluginId), viewId, operation }))?.changed),
    external: (pluginId, viewId) => transport.request("browser.view.external", { token: pluginToken(pluginId), viewId }).then(() => {}),
    bounds: (pluginId, viewId, bounds) => transport.request("browser.view.bounds", { token: pluginToken(pluginId), viewId, ...bounds }),
    extract: (pluginId, viewId) => transport.request("browser.view.extract", { token: pluginToken(pluginId), viewId }),
    adapter: (pluginId, viewId, adapter) => transport.request("browser.view.adapter", { token: pluginToken(pluginId), viewId, adapter }),
    onNavigate: (pluginId, viewId, listener) => {
      const key = pluginId + ':' + viewId;
      let set = state.browserNavListeners.get(key);
      if (!set) {
        set = new Set();
        state.browserNavListeners.set(key, set);
      }
      set.add(listener);
      return () => {
        set.delete(listener);
        if (set.size === 0) state.browserNavListeners.delete(key);
      };
    },
    close: (pluginId, viewId) => {
      const viewKey = pluginId + '/' + viewId;
      const active = state.pluginTokens.get(pluginId);
      const recorded = pluginViewTokens.get(viewKey);
      // A view opened before a hot update is still that plugin's to destroy, so
      // close is the one action allowed to name the token it was created with.
      const promote = Boolean(recorded && recorded !== active);
      if (promote) state.pluginTokens.set(pluginId, recorded);
      closingView = true;
      let request;
      try {
        request = transport.request("browser.view.close", { token: pluginToken(pluginId), viewId });
      } finally {
        closingView = false;
        if (promote) {
          if (active) state.pluginTokens.set(pluginId, active);
          else state.pluginTokens.delete(pluginId);
        }
        pluginViewTokens.delete(viewKey);
      }
      return request;
    },
  };

  // `core.fetch` carries the request body as base64 (`bodyBase64`) and ignores a
  // string `body`; this mirrors `createCoreTransport` in native.ts, which is the
  // one place that encoding is defined.
  const coreFetch = async (method, path, body) => {
    let encoded = '';
    if (body !== undefined) {
      const bytes = new TextEncoder().encode(nativeStringify(body ?? {}));
      let binary = '';
      for (let index = 0; index < bytes.length; index += 0x8000) {
        binary += String.fromCharCode(...bytes.subarray(index, index + 0x8000));
      }
      encoded = btoa(binary);
    }
    const result = await transport.request('core.fetch', {
      path: '/enhance/v1' + path,
      method,
      headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
      bodyBase64: encoded,
    });
    const text = result?.bodyBase64
      ? new TextDecoder().decode(Uint8Array.from(atob(result.bodyBase64), character => character.charCodeAt(0)))
      : '';
    const parsed = text ? nativeParse(text) : null;
    if (result?.status >= 400) {
      const code = parsed?.error?.code || 'CORE_ERROR';
      const error = new Error(parsed?.error?.message || `核心返回 HTTP ${result.status}`);
      error.code = code;
      error.status = result.status;
      // A core answer is not a bridge refusal: only an expired local session or
      // a bare HTTP status reads as "the local channel rotated". Everything else,
      // including PERMISSION_DENIED and PLUGIN_PRIVILEGE_DENIED, keeps its own
      // meaning and is never retried.
      if (bridgeCredentialCodes.has(code) || ((result.status === 401 || result.status === 403) && bridgeRejectedBeforeDispatch(code))) {
        error.message = bridgeCredentialMessage;
        error.code = 'BRIDGE_SESSION_EXPIRED';
        error.retryable = true;
      }
      throw error;
    }
    return parsed;
  };
  const coreGet = path => coreFetch('GET', path);
  const corePost = (path, body) => coreFetch('POST', path, body ?? {});
  // The plugin runtime sends `(path, body)` and expects the parsed body back.
  const pluginRequest = (path, body) => corePost(path, body);

  // A plugin that keeps throwing is stopped instead of being left half-alive in
  // the page. Two paths reach that stop and they are deliberately different:
  //
  // - activate() throwing is immediate: the plugin never came up, so there is no
  //   half-working surface worth keeping and no reason to wait for a second
  //   failure.
  // - an error inside a mount, decorator or event listener is counted per plugin
  //   per window, so one bad render does not stop an otherwise healthy plugin.
  //
  // Both are reported as fatal to the core, which records FAULTED with the
  // reason and revokes the token. Calling the plain /disable route instead would
  // clear the reason, and the plugin page would show a stopped plugin with no
  // explanation for why.
  const faultWindowMs = 60000;
  const faultLimit = 5;
  const faults = new Map();

  // A poll the core refuses because the plugin is no longer authorized is not a
  // plugin bug: the renderer is simply still holding a token the core revoked.
  // Counting those would turn every normal disable into a reported failure and,
  // worse, would let a permanent 401 accumulate into a code-fault verdict.
  const passiveRefusals = new Set(['PLUGIN_IDENTITY_REJECTED', 'PLUGIN_DISABLED', 'MANAGER_CLOSED', 'SAFE_MODE', 'PLUGIN_NOT_FOUND']);
  function isPassiveRefusal(error) {
    return passiveRefusals.has(error?.code) || error?.status === 401;
  }

  function reportFault(id, error, fatal) {
    const message = error?.message || String(error);
    void corePost('/plugin-runtime/report.error', {
      token: state.pluginTokens.get(id) || '',
      params: { message, code: error?.code || 'RENDERER_ERROR', fatal: Boolean(fatal) },
    }).then(async result => {
      if (!fatal || result?.stopped === false) return;
      await syncPlugins();
    }).catch(() => {});
    return message;
  }

  function noteFault(id, error) {
    if (isPassiveRefusal(error)) return;
    const now = Date.now();
    const entry = faults.get(id) || { count: 0, since: now };
    if (now - entry.since > faultWindowMs) {
      entry.count = 0;
      entry.since = now;
    }
    entry.count += 1;
    if (entry.count < faultLimit) {
      faults.set(id, entry);
      reportFault(id, error, false);
      return;
    }
    faults.delete(id);
    reportFault(id, error, true);
  }

  // An activation failure is fatal on the first occurrence.
  function noteActivationFault(id, error) {
    if (isPassiveRefusal(error)) return;
    faults.delete(id);
    reportFault(id, error, true);
  }

  let syncPromise = null;
  let syncPending = false;

  async function loadPluginAsset(id, entryPath, requiredExport = 'activate', revision = '') {
    // The revision is part of the key: a hot update changes version/updatedAt,
    // so the old bundle can never be served for the new one.
    const key = id + '/' + entryPath + '/' + requiredExport + (revision ? '/' + revision : '');
    if (state.pluginAssets.has(key)) return state.pluginAssets.get(key);
    const asset = await coreGet('/plugins/' + encodeURIComponent(id) + '/assets/' + entryPath.split('/').map(encodeURIComponent).join('/'));
    const loaded = await module.loadPluginModule(asset?.source || '', requiredExport);
    state.pluginAssets.set(key, loaded);
    return loaded;
  }

  async function syncPlugins() {
    if (state.removed || isStale()) return;
    // Concurrent triggers (a settings action plus a feature change) must not run
    // two reconciliations against the same maps.
    if (syncPromise) {
      syncPending = true;
      return syncPromise;
    }
    syncPromise = (async () => {
      do {
        syncPending = false;
        await reconcilePlugins();
      } while (syncPending && !state.removed && !isStale());
    })();
    try {
      await syncPromise;
    } finally {
      syncPromise = null;
    }
  }

  async function reconcilePlugins() {
    if (state.removed || isStale()) return;
    const current = activeSession();
    if (current && state.watchedSession !== current) void syncActiveSessionWatch(current);
    let list;
    try {
      list = await coreGet('/plugins');
    } catch (error) {
      console.warn('plugin list unavailable', error?.message || error);
      return;
    }
    if (state.removed || isStale()) return;
    const items = list?.items || [];
    state.pluginList = items;
    const enabled = items.filter(plugin => plugin.enabled && (options.surface !== 'plugin' || plugin.id === options.pluginId));
    if (!state.plugins) {
      state.plugins = module.createPluginRuntime({
        slots: pluginSlots,
        request: pluginRequest,
        surface: options.surface === 'plugin' ? 'popout' : 'host',
        instanceId: options.instanceId,
        onError: (id, error) => noteFault(id, error),
        entryHost: pluginEntryHost,
        sidebarNavHost: pluginSidebarNavHost,
        mainAreaHost: pluginMainAreaHost,
        rightPanelHost: pluginRightPanelHost,
        layoutHost: options.surface === 'plugin' ? { acquireSidebarCollapse: () => ({ dispose: () => {} }) } : pluginLayoutHost,
        browserHost: pluginBrowserHost,
        windowHost: pluginWindowHost,
        commandHost: windowsLoopback ? undefined : pluginCommandHost,
        platformLimitations: list?.platformLimitations || [],
        pickFiles: async () => (await transport.request('host.pickFiles', {})) || [],
        pickDirectory: async opts => ((await transport.request('host.pickDirectory', opts || {}))?.path) || null,
        openSettings: async tab => { await openSettings(tab || ''); },
        openSession: async id => { await transport.request('host.openSession', { id }); },
        activeSession,
      });
    }
    // A plugin the runtime reports as stopped or upgraded to a new revision must
    // leave the page first: dropping the module, the token, the asset cache and
    // the DOM together ensures hot updates activate the new bundle cleanly.
    const pluginRevision = plugin => `${plugin?.version || ''}|${plugin?.updatedAt || ''}|${Array.isArray(plugin?.capabilities) ? plugin.capabilities.join(',') : ''}`;
    const wanted = new Map(enabled.map(plugin => [plugin.id, plugin]));
    for (const id of [...state.pluginReleases.keys()]) {
      const next = wanted.get(id);
      if (next && state.pluginRevisions.get(id) === pluginRevision(next)) continue;
      state.pluginSettings.get(id)?.dispose();
      state.pluginSettings.delete(id);
      state.pluginReleases.get(id)?.();
      state.pluginReleases.delete(id);
      state.pluginRevisions.delete(id);
      // A disabled or upgraded plugin keeps no live token, but the views it
      // opened were authorized with the one it just lost: close still has to be
      // able to name it, or Electron leaves the pages behind.
      const retiredToken = state.pluginTokens.get(id);
      state.pluginTokens.delete(id);
      if (retiredToken) (state.pluginRetiredTokens ??= new Map()).set(id, retiredToken);
      for (const key of [...state.pluginAssets.keys()]) {
        if (key.startsWith(id + '/')) state.pluginAssets.delete(key);
      }
    }
    for (const plugin of enabled) {
      if (state.removed || isStale() || !state.plugins) return;
      if (state.pluginReleases.has(plugin.id)) continue;
      try {
        const token = plugin.token || (await corePost('/plugins/' + encodeURIComponent(plugin.id) + '/token'))?.token;
        if (state.removed || isStale() || !state.plugins) return;
        if (!token) continue;
        state.pluginTokens.set(plugin.id, token);
        state.pluginRetiredTokens?.delete(plugin.id);
        const entry = await loadPluginAsset(plugin.id, module.pluginEntryPath(plugin.entry?.renderer || 'renderer.js'), 'activate', pluginRevision(plugin));
        if (state.removed || isStale() || !state.plugins) return;
        const runtime = state.plugins;
        await runtime.activate(entry, {
          id: plugin.id,
          version: plugin.version,
          apiVersion: plugin.apiVersion,
          granted: plugin.capabilities || [],
          token,
        });
        if (state.removed || isStale() || state.plugins !== runtime) {
          runtime.deactivate(plugin.id);
          return;
        }
        state.pluginReleases.set(plugin.id, () => runtime.deactivate(plugin.id));
        state.pluginRevisions.set(plugin.id, pluginRevision(plugin));
      } catch (error) {
        if (state.removed || isStale()) return;
        // One plugin failing to activate must not stop the others. The plugin
        // that failed is stopped by the core with the reason recorded.
        noteActivationFault(plugin.id, error);
        const retiredToken = state.pluginTokens.get(plugin.id);
        state.pluginTokens.delete(plugin.id);
        if (retiredToken) (state.pluginRetiredTokens ??= new Map()).set(plugin.id, retiredToken);
        state.pluginRevisions.delete(plugin.id);
        // A faulted plugin's cached bundle must not be reused on the retry: the
        // core may hand out a repaired entry under the same path.
        for (const key of [...state.pluginAssets.keys()]) {
          if (key.startsWith(plugin.id + '/')) state.pluginAssets.delete(key);
        }
      }
    }
  }

  // The settings surface owns the settings module lifecycle: it asks for the
  // module and the container, and closing the page releases both. The runtime
  // never mounts a plugin's settings page on its own, because the host's own
  // settings body belongs to the official client.
  //
  // This copy exists because the activation block below keeps its own inline
  // `pluginRevision` (the desktop contract probe slices that block and runs it
  // standalone). Both spell the same identity: version, updatedAt, capabilities.
  const pluginRevisionOf = plugin => `${plugin?.version || ''}|${plugin?.updatedAt || ''}|${Array.isArray(plugin?.capabilities) ? plugin.capabilities.join(',') : ''}`;
  async function mountPluginSettings(id, element) {
    if (state.removed) throw new Error('插件宿主已卸载');
    const plugin = (state.pluginList || []).find(item => item.id === id);
    const active = state.plugins?.get(id);
    if (!active) throw new Error('插件未运行，无法打开设置页');
    if (!plugin?.entry?.settings) throw new Error('这个插件没有设置页');
    const settingsModule = await loadPluginAsset(id, module.pluginEntryPath(plugin.entry.settings), 'renderSettings', pluginRevisionOf(plugin));
    const release = await active.mountSettings(settingsModule, element);
    state.pluginSettings.set(id, release);
    return () => {
      release.dispose();
      if (state.pluginSettings.get(id) === release) state.pluginSettings.delete(id);
    };
  }
  publishedPlugins = {
    sync: syncPlugins,
    mountSettings: mountPluginSettings,
    invokeCommand: target => state.plugins?.invokeLocal(target),
    list: () => state.pluginList || [],
  };
  window.__ekdPlugins = publishedPlugins;

  state.syncPlugins = syncPlugins;
  state.mountPluginSettings = mountPluginSettings;
  // The provider page enhancement lives in its own file; this is the only hook
  // renderer.js keeps into it, so a parallel change to the entry injection never
  // has to touch it. It belongs to the official window only: the settings
  // surface has no provider page.
  state.providerEnhance = options.surface === 'host' ? createProviderEnhance({ request: transport.request }) : null;
  state.providerEnhance?.install();
  // Features and plugin lifecycle events drive syncPlugins() immediately, so
  // enable/disable takes effect on the next frame without waiting for window focus.
  const pluginEventHost = makeHost(() => {});
  const unsubFeatures = pluginEventHost.subscribe('features', () => { void syncPlugins(); });
  const unsubPlugins = pluginEventHost.subscribe('plugins', () => { void syncPlugins(); });
  state.unsubscribePlugins = () => { unsubFeatures(); unsubPlugins(); };
  void syncPlugins();
  // Plugin state changes come from outside this renderer (the settings page is a
  // separate window), so the host re-reads the list whenever the surface becomes
  // visible again instead of only once at startup.
  function pluginRefresh() { void syncPlugins(); }
  document.addEventListener('visibilitychange', pluginRefresh);
  window.addEventListener('focus', pluginRefresh);
  return { status: 'installed', surface: 'host', domRevision: 1 };
}
