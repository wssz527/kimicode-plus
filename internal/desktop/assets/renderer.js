async function installNativeRenderer(options) {
  if (window.__ekdNativeUI) await window.__ekdNativeUI.remove();
  const transport = {
    async request(action, payload = {}) {
      const id = typeof globalThis.crypto?.randomUUID === 'function'
        ? globalThis.crypto.randomUUID()
        : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}-${Math.random().toString(36).slice(2)}`;
      const response = await fetch(options.endpoint + '/ekd/native/v1', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-EKD-Bridge': options.bridgeKey },
        body: JSON.stringify({ protocol: 1, id, action, payload }),
      });
      const body = await response.json().catch(() => null);
      if (!response.ok || body?.error) {
        const error = new Error(body?.error?.message || `增强桥返回 HTTP ${response.status}`);
        error.code = body?.error?.code || 'NATIVE_RPC';
        throw error;
      }
      if (body?.protocol !== 1 || body?.id !== id) throw new Error('增强桥响应标识不匹配');
      return body?.result;
    },
  };
  const moduleURL = URL.createObjectURL(new Blob([options.moduleSource], { type: 'text/javascript' }));
  const module = await import(moduleURL);
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
    settingsButton: null,
    settingsHost: null,
    settingsHandle: null,
    settingsActive: false,
    settingsRelease: null,
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
    pluginTokens: new Map(),
    pluginAssets: new Map(),
    pluginSettings: new Map(),
    watchedSession: '',
    browserNavListeners: new Map(),
    contractState: module.createDOMContractState(),
    contracts: null,
    lastContractDigest: '',
    contractReportTimer: null,
  };
  const activeSession = () => {
    if (options.sourceSessionId) return options.sourceSessionId;
    return document.querySelector('#app')?.__vue_app__?.config.globalProperties.$pinia._s.get('kimi.sessions')?.activeSessionId || '';
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
    if (!state.features.multiSession) throw new Error('多屏会话是附加功能，请先在 KimiCode+ 设置中开启');
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
  function deactivateSettings() {
    state.settingsActive = false;
    unmountHandle(state.settingsHandle);
    state.settingsHandle = null;
    state.settingsHost?.remove();
    state.settingsHost = null;
    state.settingsRelease?.();
    state.settingsRelease = null;
    state.settingsButton?.classList.remove('on');
    state.settingsButton?.setAttribute('aria-selected', 'false');
  }
  function detachSettings() {
    state.settingsButton?.remove();
    state.settingsDialog = null;
    state.settingsList = null;
    state.settingsBody = null;
    state.settingsButton = null;
  }
  function markSettingsActive() {
    for (const tab of state.settingsList?.querySelectorAll('button.tab[role="tab"]') || []) {
      const selected = tab === state.settingsButton;
      tab.classList.toggle('on', selected);
      tab.setAttribute('aria-selected', String(selected));
    }
  }
  function activateSettings() {
    if (!state.settingsBody || state.settingsActive) return;
    state.settingsActive = true;
    markSettingsActive();
    state.settingsRelease = contain(state.settingsBody, 'settings');
    const element = document.createElement('div');
    element.className = 'ekd-settings-mount';
    state.settingsBody.append(element);
    state.settingsHost = element;
    const host = makeHost(() => deactivateSettings());
    state.settingsHandle = mount(element, 'settings', host);
  }
  async function openSettings(tab) {
    if (!document.querySelector('.settings-dialog')) {
      const button = document.querySelector('button.side-footer-settings');
      if (!button) throw new Error('官方设置入口未就绪');
      button.click();
    }
    const findOfficialTab = key => module.findOfficialSettingsTab(document, key);
    const deadline = Date.now() + 10000;
    while (!state.settingsBody || !state.settingsButton || (tab === 'providers' && !findOfficialTab('providers'))) {
      syncSettingsTab();
      if (state.settingsBody && state.settingsButton && (tab !== 'providers' || findOfficialTab('providers'))) break;
      if (Date.now() >= deadline) throw new Error('官方设置对话框契约未就绪');
      await new Promise(resolve => setTimeout(resolve, 50));
    }
    if (tab === 'providers') {
      if (state.settingsActive) deactivateSettings();
      const providerTab = findOfficialTab('providers');
      if (!providerTab) throw new Error('官方模型供应商标签未找到');
      providerTab.click();
      providerTab.classList.add('on');
      providerTab.setAttribute('aria-selected', 'true');
      return;
    }
    await activateSettings();
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
    }
    if (state.settingsList !== list) {
      state.settingsButton?.remove();
      state.settingsButton = null;
      state.settingsList = list;
    }
    if (state.settingsBody !== body) {
      state.settingsRelease?.();
      state.settingsBody = body;
      if (state.settingsActive && state.settingsHost) {
        state.settingsRelease = contain(body, 'settings');
        body.append(state.settingsHost);
      }
    }
    if (state.settingsActive) {
      body.setAttribute('data-ekd-containment', 'settings');
      if (state.settingsHost && state.settingsHost.parentElement !== body) body.append(state.settingsHost);
    }
    if (state.settingsButton?.isConnected) {
      if (state.settingsActive) markSettingsActive();
      return;
    }
    const nativeTab = parts.officialTabs[0];
    if (!nativeTab) return;
    const button = nativeTab.cloneNode(true);
    button.classList.remove('on');
    button.setAttribute('aria-selected', 'false');
    button.dataset.ekdSettingsTab = '';
    const label = button.querySelector('span');
    if (label) label.textContent = 'KimiCode+';
    else button.textContent = 'KimiCode+';
    button.addEventListener('click', event => {
      event.preventDefault();
      event.stopPropagation();
      void activateSettings();
    });
    const archived = module.findOfficialSettingsTab(document, 'archived');
    list.insertBefore(button, archived || null);
    replaceIcon(button);
    state.settingsButton = button;
    if (state.settingsActive) markSettingsActive();
  }
  async function pickSession(exclude = []) {
    if (state.picker) { state.picker.close(); state.picker.remove(); }
    const boot = await transport.request('bootstrap', { sourceSessionId: activeSession() });
    return new Promise((resolve, reject) => {
      let settled = false;
      const dialog = document.createElement('dialog');
      dialog.className = 'ekd-session-picker';
      dialog.innerHTML = '<div class="ekd-picker-head"><div><strong>添加同屏会话</strong><p>选择一个已有会话，与当前会话并排在主聊天区。</p></div><button type="button" class="ekd-picker-close" aria-label="关闭"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true"><path d="m6 6 12 12M18 6 6 18"/></svg></button></div><label class="ekd-picker-search"><span>搜索</span><input type="search" placeholder="搜索会话标题" /></label><div class="ekd-picker-list"></div><p class="ekd-picker-foot">最多 3 个同屏。关闭窗格不会结束会话。</p>';
      const list = dialog.querySelector('.ekd-picker-list');
      const input = dialog.querySelector('input');
      function finish(id) {
        if (settled) return;
        settled = true;
        if (state.picker === dialog) state.picker = null;
        if (id) resolve(id);
        else reject(new Error('已取消选择'));
        dialog.close();
        dialog.remove();
      }
      function render() {
        const query = input.value.trim().toLowerCase();
        const sessions = boot.sessions.filter(session => !exclude.includes(session.id) && (!query || (session.title || session.id).toLowerCase().includes(query)));
        list.replaceChildren();
        if (!sessions.length) {
          const empty = document.createElement('p'); empty.className = 'ekd-picker-empty'; empty.textContent = '没有可添加的已有会话。'; list.append(empty); return;
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
    });
  }
  function openOfficial(id) {
    closeSessions();
    return activateSession(id);
  }
  function activateSession(id) {
    const element = [...document.querySelectorAll('[data-session-id]')].find(item => item.dataset.sessionId === id)?.querySelector('.row');
    if (element) element.click();
    else {
      const store = document.querySelector('#app')?.__vue_app__?.config.globalProperties.$pinia._s.get('kimi.sessions');
      if (typeof store?.setActiveSessionId !== 'function') throw new Error('官方会话选择契约未就绪');
      store.setActiveSessionId(id);
    }
    if (activeSession() !== id) throw new Error('官方宿主没有接受会话切换');
    return { opened: true };
  }
  const nativeStyle = document.createElement('style');
  nativeStyle.textContent = `
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
    .ekdp-main-pane{display:flex;flex:0 0 auto;flex-direction:column;min-width:0;min-height:0;background:var(--color-bg,#121212)}
    .ekdp-main-splitter{flex:0 0 5px;cursor:col-resize;background:var(--color-line,#8884);opacity:.55;touch-action:none}
    .ekdp-main-splitter:hover,.ekdp-main-splitter:focus-visible,.ekdp-main-splitter.dragging{opacity:1;outline:none;background:var(--blue,#5b8def)}
    /* The official settings body is a fixed-height box that does not scroll. The
       mount therefore owns the scroll for the enhanced page only: it is clamped to
       the body's height and scrolls its own content. Without this the tail of the
       panel sits below the dialog and cannot be reached by wheel, keyboard or
       scripted scrolling. flex:1 1 auto covers a flex body, max-height:100% covers
       a block body; both resolve against the body's definite height. */
    .ekd-settings-mount{display:block;width:100%;min-width:0;min-height:0;flex:1 1 auto;max-height:100%;box-sizing:border-box;overflow-y:auto;overscroll-behavior:contain}
    .ekd-native-entry{position:relative}
    .ekd-session-picker{width:min(520px,calc(100vw - 40px));max-height:min(680px,calc(100vh - 60px));padding:0;border:1px solid var(--color-line,#8884);border-radius:8px;background:var(--color-bg,#121212);color:var(--color-text,#eee);font:13px/1.5 var(--font-ui,system-ui);box-shadow:0 16px 50px rgba(0,0,0,.24)}
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
  function replaceIcon(button) {
    const existing = button.querySelector('svg');
    if (!existing) return;
    existing.setAttribute('viewBox', '0 0 24 24');
    existing.setAttribute('fill', 'none');
    existing.setAttribute('stroke', 'currentColor');
    existing.setAttribute('stroke-width', '1.6');
    existing.setAttribute('stroke-linecap', 'round');
    existing.setAttribute('stroke-linejoin', 'round');
    existing.innerHTML = '<path d="' + SETTINGS_ICON + '"></path>';
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
    if (options.popout) return;
    const next = entries.map(entry => ({ action: entry.action, title: entry.title, icon: entry.icon || '' }));
    const encoded = JSON.stringify(next);
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
    const digest = JSON.stringify(probes.map(p => [p.id, p.status, p.reason || '', p.detail || '']));
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
    }
    if (evaluation.byId['dom.settings_providers']?.status === 'disabled') {
      state.providerEnhance?.clearDecorations?.();
    }
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
    scheduleContractReport(evaluation.probes, firstEvaluation || Boolean(document.querySelector('.settings-dialog')));
  }
  function ensure() {
    if (state.removed) return;
    state.observer?.disconnect();
    try {
      syncNativeEntries();
    } finally {
      if (!state.removed) state.observer?.observe(document.body, { childList: true, subtree: true });
    }
  }
  state.remove = async () => {
    if (state.removed) return;
    state.removed = true;
    state.observer?.disconnect();
    state.unsubscribeSession?.();
    state.unsubscribeFeatures?.();
    state.unsubscribePlugins?.();
    document.removeEventListener('visibilitychange', pluginRefresh);
    window.removeEventListener('focus', pluginRefresh);
    for (const release of [...state.pluginReleases.values()]) release();
    for (const entry of [...state.pluginPanels.values()]) entry.dispose?.();
    state.pluginPanels.clear();
    for (const handle of [...state.pluginMainAreas.values()]) handle.dispose();
    state.pluginMainAreas.clear();
    state.pluginEntries.clear();
    state.pluginReleases.clear();
    for (const release of [...state.pluginSettings.values()]) release.dispose();
    state.pluginSettings.clear();
    state.browserNavListeners.clear();
    delete window.__ekdPluginBrowserNavigated;
    await unwatchActiveSession();
    state.plugins?.dispose();
    state.plugins = null;
    delete window.__ekdPlugins;
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
    if (!options.popout) void transport.request('host.syncOverlayEntries', { entries: [] }).catch(() => {});
    state.providerEnhance?.remove();
    state.providerEnhance = null;
    nativeStyle.remove();
    URL.revokeObjectURL(moduleURL);
    delete window.__ekdNativeUI;
  };
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
    const tab = event.target.closest('.settings-tab-list button.tab[role="tab"]');
    if (tab && tab !== state.settingsButton) {
      if (state.settingsActive) {
        deactivateSettings();
        tab.classList.add('on');
        tab.setAttribute('aria-selected', 'true');
      }
      setTimeout(() => ensure(), 40);
    }
  };
  const settingsScroll = event => {
    if (!state.settingsActive || event.target !== state.settingsBody) return;
    const body = state.settingsBody;
    body.classList.add('scrolling');
    clearTimeout(state.settingsScrollTimer);
    state.settingsScrollTimer = setTimeout(() => body.classList.remove('scrolling'), 900);
  };
  document.addEventListener('click', settingsTabClick, true);
  document.addEventListener('scroll', settingsScroll, true);
  if (options.surface === 'plugin') {
    document.documentElement.dataset.colorScheme = options.theme || 'dark';
    document.title = 'KimiCode+ 插件';
    document.body.style.cssText = 'margin:0;height:100vh;overflow:hidden;background:var(--color-bg,#121212);color:var(--color-text,#eee)';
    state.popoutRoot = document.createElement('div');
    state.popoutRoot.style.cssText = 'height:100vh;min-height:0;overflow:hidden';
    document.body.replaceChildren(state.popoutRoot);
    window.__ekdNativeUI = publishNativeUI();
  } else if (options.surface === 'settings') {
    document.documentElement.dataset.colorScheme = options.theme || 'dark';
    document.title = 'KimiCode+ 设置';
    document.body.style.cssText = 'margin:0;height:100vh;overflow:hidden;background:var(--color-bg,#121212);color:var(--color-text,#eee)';
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
  state.unsubscribeFeatures = makeHost(() => {}).subscribe('features', value => applyFeatures(value, false));
  let watchChain = Promise.resolve();
  function syncActiveSessionWatch(targetId) {
    watchChain = watchChain.then(async () => {
      const next = state.removed ? '' : (targetId || '');
      const prev = state.watchedSession || '';
      if (next === prev) return;
      if (prev) {
        state.watchedSession = '';
        await transport.request('session.unwatch', { id: prev }).catch(() => {});
      }
      if (next && !state.removed) {
        try {
          await transport.request('session.watch', { id: next });
          if (state.removed) {
            await transport.request('session.unwatch', { id: next }).catch(() => {});
          } else {
            state.watchedSession = next;
          }
        } catch {
          // KAP may not have finished opening yet; next session change or plugin sync retries.
        }
      }
    });
    return watchChain;
  }
  function unwatchActiveSession() {
    return syncActiveSessionWatch('');
  }
  let previousSession = activeSession();
  if (previousSession) void syncActiveSessionWatch(previousSession);
  const sessionStore = document.querySelector('#app')?.__vue_app__?.config.globalProperties.$pinia._s.get('kimi.sessions');
  state.unsubscribeSession = sessionStore?.$subscribe(() => {
    const id = activeSession();
    if (id === previousSession) return;
    previousSession = id;
    if (!id && state.sessionHandle) closeSessions();
    for (const handle of [...state.pluginMainAreas.values()]) {
      if (handle.mode === 'page') handle.dispose();
    }
    void syncActiveSessionWatch(id);
    window.dispatchEvent(new CustomEvent('ekd:active-session', { detail: id }));
  }, { detached: true, flush: 'sync' });
  ensure();
  state.observer = new MutationObserver(ensure);
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
  function mountMainArea(pluginId, definition) {
    const container = document.querySelector(".composer")?.closest(".chat-main") || document.querySelector(".chat")?.closest(".chat-main");
    if (!container) throw new Error("主聊天区契约未就绪");
    const handle = module.mountMainAreaSurface({
      document,
      container,
      pluginId,
      definition,
      window,
    });
    const release = handle.dispose;
    return {
      mode: definition?.mode === "page" ? "page" : "split",
      setWidth: handle.setWidth,
      width: handle.width,
      dispose: release,
    };
  }

  const pluginMainAreaHost = {
    mount(pluginId, definition) {
      // A plugin has one main area, and page mode owns the full chat column, so
      // replace any matching or page-mode mount rather than stacking wrappers.
      for (const [otherId, otherHandle] of [...state.pluginMainAreas]) {
        if (otherId === pluginId || otherHandle.mode === "page" || definition?.mode === "page") {
          otherHandle.dispose();
        }
      }
      const handle = mountMainArea(pluginId, definition);
      state.pluginMainAreas.set(pluginId, handle);
      const release = handle.dispose.bind(handle);
      handle.dispose = () => {
        release();
        if (state.pluginMainAreas.get(pluginId) === handle) state.pluginMainAreas.delete(pluginId);
      };
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
      const body = panelBody();
      if (!body || !document.querySelector(".ptb-tabs")) throw new Error("右侧面板契约未就绪");
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
      body.append(host);
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
      remove: () => state.remove(),
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
    window.__ekdNativeUI = ui;
    return ui;
  }
  // A plugin's own token is the only identity the browser bridge carries; the
  // host process asks the core whose it is. A closed plugin has already lost its
  // token, so close sends the token the runtime still holds in scope.
  function pluginToken(id) {
    return state.pluginTokens.get(id) || '';
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
  window.__ekdPluginBrowserNavigated = payload => {
    if (!payload || typeof payload.pluginId !== 'string' || typeof payload.viewId !== 'string' || typeof payload.url !== 'string') return;
    const listeners = state.browserNavListeners.get(payload.pluginId + ':' + payload.viewId);
    if (!listeners) return;
    for (const listener of [...listeners]) {
      try { listener(payload.url); } catch {}
    }
  };
  const pluginBrowserHost = {
    open: (pluginId, viewId, url) => transport.request("browser.view.open", { token: pluginToken(pluginId), viewId, url }),
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
    close: (pluginId, viewId) => transport.request("browser.view.close", { token: pluginToken(pluginId), viewId }),
  };

  // `core.fetch` carries the request body as base64 (`bodyBase64`) and ignores a
  // string `body`; this mirrors `createCoreTransport` in native.ts, which is the
  // one place that encoding is defined.
  const coreFetch = async (method, path, body) => {
    let encoded = '';
    if (body !== undefined) {
      const bytes = new TextEncoder().encode(JSON.stringify(body ?? {}));
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
    const parsed = text ? JSON.parse(text) : null;
    if (result?.status >= 400) {
      const error = new Error(parsed?.error?.message || `核心返回 HTTP ${result.status}`);
      error.code = parsed?.error?.code || 'CORE_ERROR';
      error.status = result.status;
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

  async function loadPluginAsset(id, entryPath, requiredExport = 'activate') {
    const key = id + '/' + entryPath + '/' + requiredExport;
    if (state.pluginAssets.has(key)) return state.pluginAssets.get(key);
    const asset = await coreGet('/plugins/' + encodeURIComponent(id) + '/assets/' + entryPath.split('/').map(encodeURIComponent).join('/'));
    const loaded = await module.loadPluginModule(asset?.source || '', requiredExport);
    state.pluginAssets.set(key, loaded);
    return loaded;
  }

  async function syncPlugins() {
    if (state.removed) return;
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
      } while (syncPending);
    })();
    try {
      await syncPromise;
    } finally {
      syncPromise = null;
    }
  }

  async function reconcilePlugins() {
    const current = activeSession();
    if (current && state.watchedSession !== current) void syncActiveSessionWatch(current);
    let list;
    try {
      list = await coreGet('/plugins');
    } catch (error) {
      console.warn('plugin list unavailable', error?.message || error);
      return;
    }
    if (state.removed) return;
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
        commandHost: pluginCommandHost,
        pickFiles: async () => (await transport.request('host.pickFiles', {})) || [],
        pickDirectory: async opts => ((await transport.request('host.pickDirectory', opts || {}))?.path) || null,
        openSession: async id => { await transport.request('host.openSession', { id }); },
        activeSession,
      });
    }
    // A plugin the runtime reports as stopped must leave the page: dropping the
    // module, the token and the DOM together is what makes "disabled" observable.
    const wanted = new Set(enabled.map(plugin => plugin.id));
    for (const id of [...state.pluginReleases.keys()]) {
      if (wanted.has(id)) continue;
      state.pluginSettings.get(id)?.dispose();
      state.pluginSettings.delete(id);
      state.pluginReleases.get(id)?.();
      state.pluginReleases.delete(id);
      state.pluginTokens.delete(id);
      for (const key of [...state.pluginAssets.keys()]) {
        if (key.startsWith(id + '/')) state.pluginAssets.delete(key);
      }
    }
    for (const plugin of enabled) {
      if (state.pluginReleases.has(plugin.id)) continue;
      try {
        const token = plugin.token || (await corePost('/plugins/' + encodeURIComponent(plugin.id) + '/token'))?.token;
        if (!token) continue;
        state.pluginTokens.set(plugin.id, token);
        const entry = await loadPluginAsset(plugin.id, module.pluginEntryPath(plugin.entry?.renderer || 'renderer.js'));
        await state.plugins.activate(entry, {
          id: plugin.id,
          version: plugin.version,
          apiVersion: plugin.apiVersion,
          granted: plugin.capabilities || [],
          token,
        });
        state.pluginReleases.set(plugin.id, () => state.plugins.deactivate(plugin.id));
      } catch (error) {
        // One plugin failing to activate must not stop the others. The plugin
        // that failed is stopped by the core with the reason recorded.
        noteActivationFault(plugin.id, error);
        state.pluginTokens.delete(plugin.id);
      }
    }
  }

  // The settings surface owns the settings module lifecycle: it asks for the
  // module and the container, and closing the page releases both. The runtime
  // never mounts a plugin's settings page on its own, because the host's own
  // settings body belongs to the official client.
  async function mountPluginSettings(id, element) {
    if (state.removed) throw new Error('插件宿主已卸载');
    const plugin = (state.pluginList || []).find(item => item.id === id);
    const active = state.plugins?.get(id);
    if (!active) throw new Error('插件未运行，无法打开设置页');
    if (!plugin?.entry?.settings) throw new Error('这个插件没有设置页');
    const settingsModule = await loadPluginAsset(id, module.pluginEntryPath(plugin.entry.settings), 'renderSettings');
    const release = await active.mountSettings(settingsModule, element);
    state.pluginSettings.set(id, release);
    return () => {
      release.dispose();
      if (state.pluginSettings.get(id) === release) state.pluginSettings.delete(id);
    };
  }
  window.__ekdPlugins = {
    sync: syncPlugins,
    mountSettings: mountPluginSettings,
    invokeCommand: target => state.plugins?.invokeLocal(target),
    list: () => state.pluginList || [],
  };

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
