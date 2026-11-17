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
    features: { planner: false, multiSession: false },
    handles: new Set(),
    entries: new Set(),
    sidebarRefs: 0,
    sidebarCollapsed: null,
    observer: null,
    panel: null,
    plannerHostElement: null,
    plannerHandle: null,
    plannerRelease: null,
    plannerSidebarRelease: null,
    plannerTab: null,
    returnTabId: null,
    wasVisible: false,
    sessionContainer: null,
    sessionHostElement: null,
    sessionHandle: null,
    sessionRelease: null,
    sessionSidebarRelease: null,
    picker: null,
    settingsDialog: null,
    settingsList: null,
    settingsBody: null,
    settingsButton: null,
    settingsHost: null,
    settingsHandle: null,
    settingsActive: false,
    settingsRelease: null,
    removed: false,
    plugins: null,
    pluginReleases: new Map(),
    pluginTokens: new Map(),
    pluginAssets: new Map(),
    pluginSettings: new Map(),
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
      popout: async source => { await transport.request('window.popout', { surface: 'planner', sourceSessionId: activeSession(), initialSource: source || 'web' }); close(); },
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
  function collapseSidebar() {
    state.plannerSidebarRelease ??= acquireSidebarCollapse();
  }
  function restoreSidebar() {
    state.plannerSidebarRelease?.();
    state.plannerSidebarRelease = null;
  }
  function closePlanner(restore = true) {
    unmountHandle(state.plannerHandle);
    state.plannerHandle = null;
    state.plannerHostElement?.remove();
    state.plannerHostElement = null;
    state.plannerRelease?.();
    state.plannerRelease = null;
    if (restore && state.returnTabId && state.panel?.panelTabs.value.some(tab => tab.id === state.returnTabId)) state.panel.activateTab(state.returnTabId);
    state.returnTabId = null;
    state.plannerTab?.setAttribute('aria-selected', 'false');
    state.plannerTab?.parentElement?.classList.remove('on');
    if (!state.wasVisible) state.panel?.hidePanel();
    state.panel = null;
    restoreSidebar();
    void transport.request('host.browserBounds', { visible: false }).catch(() => {});
  }
  async function openPlanner(source = 'web') {
    if (!state.features.planner) throw new Error('规划是附加功能，请先在 KimiCode+ 设置中开启');
    if (state.plannerHandle) {
      window.dispatchEvent(new CustomEvent('ekd:planner-source', { detail: source }));
      state.panel?.showPanel();
      return;
    }
    const currentPanel = panel();
    const body = document.querySelector('.pt-body');
    if (!currentPanel || !body) throw new Error('右侧规划面板契约未就绪');
    state.panel = currentPanel;
    state.wasVisible = currentPanel.panelVisible.value;
    state.returnTabId = currentPanel.activeTabId.value;
    currentPanel.activeTabId.value = null;
    collapseSidebar();
    currentPanel.previewWidth.value = Math.min(currentPanel.previewMax.value, Math.max(currentPanel.previewWidth.value, 605));
    currentPanel.showPanel();
    await new Promise(resolve => setTimeout(resolve, 0));
    state.plannerRelease = contain(body, 'planner');
    const element = document.createElement('div');
    element.className = 'ekd-planner-host';
    body.append(element);
    state.plannerHostElement = element;
    const host = makeHost(() => closePlanner());
    state.plannerHandle = mount(element, 'planner', host, { initialSource: source });
    state.plannerTab?.setAttribute('aria-selected', 'true');
    state.plannerTab?.parentElement?.classList.add('on');
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
  async function openSettings() {
    if (!document.querySelector('.settings-dialog')) {
      const button = document.querySelector('button.side-footer-settings');
      if (!button) throw new Error('官方设置入口未就绪');
      button.click();
    }
    const deadline = Date.now() + 10000;
    while (!state.settingsBody || !state.settingsButton) {
      syncSettingsTab();
      if (state.settingsBody && state.settingsButton) break;
      if (Date.now() >= deadline) throw new Error('官方设置对话框契约未就绪');
      await new Promise(resolve => setTimeout(resolve, 50));
    }
    await activateSettings();
  }
  function syncSettingsTab() {
    const dialog = document.querySelector('.settings-dialog');
    if (!dialog) {
      if (state.settingsDialog) {
        deactivateSettings();
        detachSettings();
      }
      return;
    }
    const list = dialog.querySelector('.settings-tab-list');
    const body = dialog.querySelector('section.settings-region .body');
    if (!list || !body) return;
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
    const nativeTab = list.querySelector('button.tab[role="tab"]');
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
    const archived = [...list.querySelectorAll('button.tab')].find(tab => tab.textContent.includes('已归档'));
    list.insertBefore(button, archived || null);
    replaceIcon(button, 'settings');
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
    closePlanner();
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
    [data-ekd-containment=planner]>:not(.ekd-planner-host),[data-ekd-containment=sessions]>:not(.ekd-session-host),[data-ekd-containment=settings]>:not(.ekd-settings-mount){display:none!important}
    .ekd-planner-host,.ekd-session-host{display:flex;flex:1;min-width:0;min-height:0;width:100%;height:100%;overflow:hidden}
    /* The official settings body is a fixed-height box that does not scroll. The
       mount therefore owns the scroll for the enhanced page only: it is clamped to
       the body's height and scrolls its own content. Without this the tail of the
       panel sits below the dialog and cannot be reached by wheel, keyboard or
       scripted scrolling. flex:1 1 auto covers a flex body, max-height:100% covers
       a block body; both resolve against the body's definite height. */
    .ekd-settings-mount{display:block;width:100%;min-width:0;min-height:0;flex:1 1 auto;max-height:100%;box-sizing:border-box;overflow-y:auto;overscroll-behavior:contain}
    .ekd-native-entry{position:relative}
    .ekd-native-entry[data-error="true"]{color:var(--red,#ff8a8c)}
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
  function replaceIcon(button, kind) {
    const paths = {
      planner: '<circle cx="12" cy="12" r="8.5"/><path d="m15.5 8.5-2 5-5 2 2-5 5-2Z"/>',
      split: '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M12 4v16"/>',
      settings: '<path d="m12 3 2.5 5 5.5 1-4 4 .8 5.5L12 16l-4.8 2.5L8 13 4 9l5.5-1L12 3Z"/>',
    };
    const existing = button.querySelector('svg');
    if (!existing || !paths[kind]) return;
    existing.setAttribute('viewBox', '0 0 24 24');
    existing.setAttribute('fill', 'none');
    existing.setAttribute('stroke', 'currentColor');
    existing.setAttribute('stroke-width', '1.6');
    existing.setAttribute('stroke-linecap', 'round');
    existing.setAttribute('stroke-linejoin', 'round');
    existing.innerHTML = paths[kind];
  }
  function setEntryLabel(button, text) {
    const label = button.querySelector('.sit-name, :scope > span:not(.ui-kbd)');
    if (label) label.textContent = text;
    else {
      const node = [...button.childNodes].find(item => item.nodeType === Node.TEXT_NODE && item.textContent.includes('侧边聊天'));
      if (node) node.textContent = ' ' + text + ' ';
    }
    for (const shortcut of button.querySelectorAll('.ui-kbd')) shortcut.remove();
  }
  function makeEntry(text, action, after, template) {
    const button = template.cloneNode(true);
    button.type = 'button';
    button.classList.add('ekd-native-entry');
    button.dataset.ekdEntry = action;
    replaceIcon(button, action);
    setEntryLabel(button, text);
    button.addEventListener('click', async () => {
      try {
        if (action === 'planner') await openPlanner('web');
        else if (action === 'split') {
          if (state.sessionHandle) window.dispatchEvent(new CustomEvent('ekd:add-session'));
          else { const id = await pickSession([activeSession()]); if (id) await mountSessions([id]); }
        }
      } catch (error) {
        if (error?.message !== '已取消选择') {
          button.title = error?.message || String(error);
          button.dataset.error = 'true';
          setTimeout(() => { delete button.dataset.error; button.title = ''; }, 5000);
        }
      }
    });
    after.insertAdjacentElement('afterend', button);
    state.entries.add(button);
    return button;
  }
  function addonEntryPlan() {
    const plan = [];
    if (state.features.planner) plan.push({ action: 'planner', label: '规划' });
    if (state.features.multiSession) plan.push({ action: 'split', label: '添加同屏会话' });
    return plan;
  }
  function removeEntry(action) {
    for (const element of [...state.entries]) {
      if (element.dataset?.ekdEntry === action) { element.remove(); state.entries.delete(element); }
    }
    for (const element of [...state.entries]) if (!element.isConnected) state.entries.delete(element);
    if (action === 'planner') {
      state.plannerTab?.parentElement?.remove();
      state.plannerTab = null;
    }
  }
  function applyFeatures(next, initial) {
    const previous = state.features;
    state.features = { planner: next?.planner === true, multiSession: next?.multiSession === true };
    if (!state.features.planner) {
      if (state.plannerHandle) closePlanner();
      removeEntry('planner');
    }
    if (!state.features.multiSession) {
      if (state.sessionContainer) closeSessions();
      removeEntry('split');
    }
    if (!initial && (previous.planner !== state.features.planner || previous.multiSession !== state.features.multiSession)) ensure();
  }
  async function refreshFeatures() {
    try {
      applyFeatures(await transport.request('features.get'), true);
    } catch {
      applyFeatures(null, true);
    }
  }
  // Entry injection is per container. Each visible host menu container (the
  // empty-panel `.sit` list and the `+` dropdown `.pl`) is checked on its own, so
  // a container that the host re-rendered gets its entry back without a stale
  // module-level reference deciding whether it "already exists".
  function entryContainers() {
    const containers = [];
    for (const list of document.querySelectorAll('.pt-body .sit, .pt-body .pl')) containers.push(list);
    return containers;
  }
  function sideChatIn(container) {
    return [...container.querySelectorAll('button.sit-link, button.ui-menu-item')].find(button =>
      button.querySelector('.sit-name')?.textContent.trim() === '侧边聊天'
      || [...button.childNodes].some(node => node.nodeType === Node.TEXT_NODE && node.textContent.trim() === '侧边聊天')) || null;
  }
  function syncNativeEntries() {
    const live = new Set();
    const addonEntries = addonEntryPlan();
    for (const container of entryContainers()) {
      const anchorHost = sideChatIn(container);
      if (!anchorHost) continue;
      let anchor = anchorHost;
      for (const { action, label } of addonEntries) {
        // Duplicate guard lives inside this container: exactly one entry with
        // this action may exist here, and a re-rendered anchor gets a new entry.
        const existing = [...container.querySelectorAll(`button.ekd-native-entry[data-ekd-entry="${action}"]`)];
        let entry = existing[0];
        for (const extra of existing.slice(1)) { extra.remove(); live.delete(extra); }
        if (!entry || entry.previousElementSibling !== anchor) {
          entry?.remove();
          entry = makeEntry(label, action, anchor, anchorHost);
        }
        anchor = entry;
        live.add(entry);
      }
    }
    // Entries whose host container went away are dropped from the registry.
    // Only entry buttons are owned by the container loop. Other tracked nodes
    // (the planner tab) are kept so remove() can still clean them up.
    for (const entry of [...state.entries]) {
      if (entry.classList?.contains('ekd-native-entry') && !live.has(entry)) state.entries.delete(entry);
    }
    for (const element of live) state.entries.add(element);
    const tabs = document.querySelector('.ptb-tabs');
    if (tabs && state.features.planner && !state.plannerTab?.isConnected) {
      const wrapper = document.createElement('div'); wrapper.className = 'ptb-tab'; wrapper.dataset.ekdPlannerTab = '';
      const button = document.createElement('button'); button.type = 'button'; button.className = 'ptb-tab-main'; button.textContent = 'KimiCode+ 规划'; button.setAttribute('aria-selected', 'false');
      button.addEventListener('click', () => void openPlanner('web'));
      const attributes = [...tabs.attributes].filter(attribute => attribute.name.startsWith('data-v-'));
      for (const element of [wrapper, button]) for (const attribute of attributes) element.setAttribute(attribute.name, attribute.value);
      wrapper.append(button); tabs.append(wrapper); state.plannerTab = button; state.entries.add(wrapper);
    }
    // The sidebar cache widget used to be mounted here. It is now the preinstalled
    // Monitor plugin, which mounts through the plugin runtime's own sidebar slot,
    // so this file owns no cache surface and no private channel for one.
    syncSettingsTab();
    if (state.sessionContainer && !state.sessionContainer.isConnected) closeSessions();
    if (state.sessionContainer) state.sessionContainer.setAttribute('data-ekd-containment', 'sessions');
    if (state.plannerHandle) {
      const body = document.querySelector('.pt-body');
      if (body && state.plannerHostElement?.parentElement !== body) {
        state.plannerRelease?.();
        state.plannerRelease = contain(body, 'planner');
        body.append(state.plannerHostElement);
      }
    }
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
    state.pluginReleases.clear();
    for (const release of [...state.pluginSettings.values()]) release.dispose();
    state.pluginSettings.clear();
    state.plugins?.dispose();
    state.plugins = null;
    delete window.__ekdPlugins;
    document.removeEventListener('click', nativeTabClick, true);
    document.removeEventListener('click', settingsTabClick, true);
    document.removeEventListener('scroll', settingsScroll, true);
    clearTimeout(state.settingsScrollTimer);
    state.picker?.close();
    deactivateSettings();
    detachSettings();
    closePlanner(false);
    closeSessions();
    for (const handle of [...state.handles]) unmountHandle(handle);
    for (const element of state.entries) element.remove();
    nativeStyle.remove();
    URL.revokeObjectURL(moduleURL);
    delete window.__ekdNativeUI;
  };
  const nativeTabClick = event => {
    if (state.plannerHandle && (event.target.closest('.ptb-tab:not([data-ekd-planner-tab])') || event.target.closest('.sit-link:not([data-ekd-entry]), .pt-body .pl button.ui-menu-item:not([data-ekd-entry])'))) closePlanner(false);
  };
  const settingsTabClick = event => {
    const tab = event.target.closest('.settings-tab-list button.tab[role="tab"]');
    if (tab && tab !== state.settingsButton && state.settingsActive) deactivateSettings();
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
  if (options.surface === 'settings' || options.surface === 'management' || options.popout) {
    document.documentElement.dataset.colorScheme = options.theme || 'dark';
    const title = options.surface === 'settings' ? 'KimiCode+ 设置' : options.surface === 'management' ? 'KimiCode+ 管理' : 'KimiCode+ 规划聊天';
    document.title = title;
    document.body.style.cssText = 'margin:0;height:100vh;overflow:hidden;background:var(--color-bg,#121212);color:var(--color-text,#eee)';
    const element = document.createElement('div');
    element.style.cssText = 'height:100vh;min-height:0';
    document.body.replaceChildren(element);
    const host = makeHost(() => { void transport.request('window.close'); });
    if (options.surface === 'management') mount(element, 'management', host, { initialPath: options.initialPath });
    else mount(element, options.surface === 'settings' ? 'settings' : 'planner', host, { initialSource: options.initialSource || 'web' });
    window.__ekdNativeUI = state;
    return { status: 'installed', surface: options.surface || 'planner' };
  }
  document.addEventListener('click', nativeTabClick, true);
  window.__ekdNativeUI = state;
  await refreshFeatures();
  if (state.removed) return { status: 'removed', surface: 'host', domRevision: 1 };
  state.unsubscribeFeatures = makeHost(() => {}).subscribe('features', value => applyFeatures(value, false));
  let previousSession = activeSession();
  const sessionStore = document.querySelector('#app')?.__vue_app__?.config.globalProperties.$pinia._s.get('kimi.sessions');
  state.unsubscribeSession = sessionStore?.$subscribe(() => {
    const id = activeSession();
    if (id === previousSession) return;
    previousSession = id;
    if (!id && state.sessionHandle) closeSessions();
    window.dispatchEvent(new CustomEvent('ekd:active-session', { detail: id }));
  }, { detached: true, flush: 'sync' });
  ensure();
  state.observer = new MutationObserver(ensure);
  state.observer.observe(document.body, { childList: true, subtree: true });
  window.__ekdNativeUI.openPlanner = openPlanner;
  window.__ekdNativeUI.mountSessions = mountSessions;
  window.__ekdNativeUI.pickSession = pickSession;
  window.__ekdNativeUI.openOfficial = openOfficial;
  window.__ekdNativeUI.activateSession = activateSession;
  window.__ekdNativeUI.openSettings = openSettings;
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
    const enabled = items.filter(plugin => plugin.enabled);
    if (!state.plugins) {
      state.plugins = module.createPluginRuntime({
        slots: pluginSlots,
        request: pluginRequest,
        onError: (id, error) => noteFault(id, error),
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
    list: () => state.pluginList || [],
  };

  state.syncPlugins = syncPlugins;
  state.mountPluginSettings = mountPluginSettings;
  void syncPlugins();
  // Plugin state changes come from outside this renderer (the settings page is a
  // separate window), so the host re-reads the list whenever the surface becomes
  // visible again instead of only once at startup.
  function pluginRefresh() { void syncPlugins(); }
  document.addEventListener('visibilitychange', pluginRefresh);
  window.addEventListener('focus', pluginRefresh);
  // Features carry the enable/disable decisions this App made; the same event
  // also refreshes the plugin list so the page and the DOM agree.
  state.unsubscribePlugins = makeHost(() => {}).subscribe('features', () => { void syncPlugins(); });
  return { status: 'installed', surface: 'host', domRevision: 1 };
}
