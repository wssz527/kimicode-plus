function createProviderEnhance({ request, officialOrigin, excludeOrigins, readOfficialConfig, retryDelayMs } = {}) {
  const CAPABILITIES = [
    ['image_in', '图片输入', '图片'],
    ['video_in', '视频输入', '视频'],
    ['tool_use', '工具调用', '工具'],
  ];
  const MODES = [
    ['none', '关闭', '不声明思考能力，也不发送任何思考强度'],
    ['toggle', '可切换', '声明 thinking 能力，由思考强度决定本次请求是否思考'],
    ['always', '始终思考', '声明 always_thinking，每次请求都开启思考'],
  ];
  const EFFORTS = ['low', 'medium', 'high', 'xhigh', 'max'];
  const CONTEXT_PRESETS = [
    { label: '128K', value: 131072 },
    { label: '256K', value: 262144 },
    { label: '512K', value: 524288 },
    { label: '1M', value: 1048576 },
  ];
  const OUTPUT_PRESETS = [
    { label: '8K', value: 8192 },
    { label: '16K', value: 16384 },
    { label: '32K', value: 32768 },
    { label: '64K', value: 65536 },
  ];
  const SLIDERS_ICON = [
    'M3.7 5.1h6.4a1 1 0 0 1 0 2H3.7a1 1 0 0 1 0-2Z',
    'M15.5 4.2a3.7 3.7 0 1 0 0 7.4 3.7 3.7 0 0 0 0-7.4Zm0 2a1.7 1.7 0 1 1 0 3.4 1.7 1.7 0 0 1 0-3.4Z',
    'M13.9 15.1h6.4a1 1 0 0 1 0 2h-6.4a1 1 0 0 1 0-2Z',
    'M8.5 12.4a3.7 3.7 0 1 0 0 7.4 3.7 3.7 0 0 0 0-7.4Zm0 2a1.7 1.7 0 1 1 0 3.4 1.7 1.7 0 0 1 0-3.4Z',
  ];
  const CHEVRON_DOWN_ICON = ['M6.7 9.3a1 1 0 0 1 1.4 0L12 13.2l3.9-3.9a1 1 0 1 1 1.4 1.4l-4.6 4.6a1 1 0 0 1-1.4 0l-4.6-4.6a1 1 0 0 1 0-1.4Z'];
  const PANEL_ID = 'ekd-px-inspector';
  const HTTP_STATUS_TEXT = {
    200: 'OK',
    400: 'Bad Request',
    401: 'Unauthorized',
    403: 'Forbidden',
    404: 'Not Found',
    409: 'Conflict',
    422: 'Unprocessable Entity',
    500: 'Internal Server Error',
    502: 'Bad Gateway',
    503: 'Service Unavailable',
    504: 'Gateway Timeout',
  };
  const CATALOG_IMPORT_TTL_MS = 60000;
  const CONFIG_RETRY_BASE_MS = Number.isFinite(retryDelayMs) && retryDelayMs >= 0 ? retryDelayMs : 800;
  const MAX_CONFIG_RETRIES = 3;
  const state = { panel: null, style: null, table: null };
  const openingModels = new Map();
  const recentCatalogImports = new Map();
  let enabled = true;
  let removed = false;
  let observer;
  let timer;
  let retryTimer;
  let retryCount = 0;
  let generation = 0;
  let snapshot;
  let pending;
  let pendingFormOpen = null;
  let originalFetch = null;
  let wrappedFetch = null;
  let installExcludeOrigins = null;
  let installReadOfficialConfig = null;

  function el(tag, props = {}, children = []) {
    const node = document.createElement(tag);
    for (const [key, value] of Object.entries(props)) {
      if (value === undefined || value === null) continue;
      if (key === 'class') node.className = value;
      else if (key === 'text') node.textContent = value;
      else if (key.startsWith('on')) node.addEventListener(key.slice(2).toLowerCase(), value);
      else node.setAttribute(key, String(value));
    }
    node.append(...children);
    return node;
  }

  function icon(paths, size = 12) {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('width', String(size));
    svg.setAttribute('height', String(size));
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('fill', 'currentColor');
    svg.setAttribute('class', 'kw-icon');
    svg.setAttribute('aria-hidden', 'true');
    for (const d of paths) {
      const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
      path.setAttribute('d', d);
      svg.append(path);
    }
    return svg;
  }

  function prependChild(parent, child) {
    const first = parent.children[0];
    if (first && typeof first.before === 'function') first.before(child);
    else parent.append(child);
  }

  function button(text, onClick, props = {}) {
    return el('button', { type: 'button', class: 'ekd-px-btn', text, onClick, ...props });
  }

  function formatSize(size) {
    if (!Number.isSafeInteger(size) || size < 1) return '';
    if (size % 1000 !== 0) {
      if (size % 1048576 === 0) return `${size / 1048576}M`;
      if (size % 1024 === 0) return `${size / 1024}K`;
    }
    return String(size);
  }

  function parseSize(raw) {
    const text = raw.trim().replace(/[,\s_]/g, '');
    if (!text) return { value: null, error: '' };
    const match = /^(\d+(?:\.\d+)?)([KM])B?$/i.exec(text);
    const unit = match?.[2].toUpperCase();
    const value = match ? Number(match[1]) * (unit === 'K' ? 1024 : 1048576) : (/^\d+$/.test(text) ? Number(text) : 0);
    if (!Number.isSafeInteger(value) || value < 1) return { value: null, error: '请输入正整数或等效的 K、M 值。' };
    return { value, error: '' };
  }

  function uniqueEfforts(raw) {
    const entries = Array.isArray(raw) ? raw.filter(value => typeof value === 'string') : [];
    return [...new Set(entries.map(value => value.trim()).filter(value => value && value.toLowerCase() !== 'no'))];
  }

  function modelFields(model = {}) {
    const capabilities = new Set(Array.isArray(model.capabilities) ? model.capabilities.filter(value => typeof value === 'string') : []);
    const supportEfforts = uniqueEfforts(model.supportEfforts ?? model.support_efforts ?? []);
    const adaptive = model.adaptiveThinking ?? model.adaptive_thinking ?? false;
    const thinking = capabilities.has('always_thinking') ? 'always'
      : capabilities.has('thinking') || adaptive || supportEfforts.length ? 'toggle' : 'none';
    capabilities.delete('always_thinking');
    capabilities.delete('thinking');
    return {
      capabilities, supportEfforts, thinking, adaptive,
      context: model.maxContextSize ?? model.max_context_size ?? 0,
      output: model.maxOutputSize ?? model.max_output_size ?? null,
    };
  }

  function summary(model, managed) {
    if (managed) return '只读';
    if (!model) return '未保存';
    const fields = modelFields(model);
    const labels = CAPABILITIES.filter(([key]) => fields.capabilities.has(key)).map(([, , label]) => label);
    if (fields.thinking !== 'none') labels.push(fields.thinking === 'always' ? '始终思考' : '思考');
    const custom = [...fields.capabilities].filter(key => !CAPABILITIES.some(([known]) => known === key));
    if (custom.length) labels.push(`其他 ${custom.length} 项`);
    if (!labels.length) labels.push('未配置');
    if (fields.context > 0) labels.push(formatSize(fields.context));
    return labels.join(' · ');
  }

  function readConfig() {
    if (pending) return pending;
    const readGeneration = generation;
    pending = request('config.get').then(config => {
      if (readGeneration === generation) snapshot = config;
      return config;
    }).finally(() => {
      if (readGeneration === generation) pending = null;
    });
    return pending;
  }

  function clearRetry() {
    clearTimeout(retryTimer);
    retryTimer = undefined;
    retryCount = 0;
  }

  function invalidate() {
    clearRetry();
    generation += 1;
    snapshot = null;
    pending = null;
    scan();
  }

  function isHiddenNode(node, stopAt = null) {
    let current = node;
    while (current && current !== stopAt) {
      if (current.hidden) return true;
      if (typeof current.getAttribute === 'function' && current.getAttribute('hidden') !== null) return true;
      if (current.style?.display === 'none') return true;
      const styleAttr = typeof current.getAttribute === 'function' ? current.getAttribute('style') : '';
      if (styleAttr && /(?:^|;)\s*display\s*:\s*none\b/i.test(styleAttr)) return true;
      if (typeof globalThis.getComputedStyle === 'function') {
        try {
          if (globalThis.getComputedStyle(current)?.display === 'none') return true;
        } catch {
          // Ignore synthetic DOM errors.
        }
      }
      current = current.parentElement;
    }
    return false;
  }

  function isEditForm(form) {
    if (!form || isHiddenNode(form, document.body)) return false;
    if (form.closest?.('.ap')) return false;
    return Boolean(form.querySelector('.pf-back, .pf-nav, .pf-head-sticky'));
  }

  function findForm() {
    const candidates = [...document.querySelectorAll('.settings-dialog .pf, .ui-dialog .pf, [role="dialog"] .pf')]
      .filter(form => !isHiddenNode(form, document.body));
    return candidates.find(isEditForm) || candidates[0] || null;
  }

  function modelInputs(row) {
    const inputs = [...row.children].filter(child => child.tagName === 'INPUT');
    return inputs.length ? inputs : [...row.querySelectorAll('input.ui-input')];
  }

  function modelInput(row) {
    return modelInputs(row)[0];
  }

  function resolveModelEntryIdentity(key, item) {
    if (!item || typeof item !== 'object') return { owner: '', modelId: '' };
    const explicitProvider = typeof item.provider === 'string' ? item.provider.trim() : '';
    const explicitModel = typeof item.model === 'string' ? item.model.trim() : '';
    const slash = key.indexOf('/');
    const hasSlash = slash > 0 && slash < key.length - 1;
    const keyOwner = hasSlash ? key.slice(0, slash) : '';
    const keyModel = hasSlash ? key.slice(slash + 1) : '';
    if (explicitProvider) {
      const modelId = explicitModel || (keyOwner === explicitProvider && keyModel ? keyModel : key.trim());
      return { owner: explicitProvider, modelId };
    }
    if (hasSlash) return { owner: keyOwner, modelId: keyModel };
    return { owner: '', modelId: explicitModel || key.trim() };
  }

  function savedModel(config, providerId, modelId) {
    if (!providerId || !modelId) return null;
    const models = config?.models || {};
    const key = `${providerId}/${modelId}`;
    if (Object.hasOwn(models, key)) {
      const identity = resolveModelEntryIdentity(key, models[key]);
      if (identity.owner === providerId && identity.modelId === modelId) return models[key];
    }
    if (Object.hasOwn(models, modelId)) {
      const identity = resolveModelEntryIdentity(modelId, models[modelId]);
      if (identity.owner === providerId && identity.modelId === modelId) return models[modelId];
    }
    let matchedKey = '';
    for (const [entryKey, item] of Object.entries(models)) {
      const identity = resolveModelEntryIdentity(entryKey, item);
      if (identity.owner === providerId && identity.modelId === modelId) {
        if (!matchedKey || entryKey < matchedKey) matchedKey = entryKey;
      }
    }
    return matchedKey ? models[matchedKey] : null;
  }

  function formTextInputs(form) {
    return [...form.querySelectorAll('.pf-field input.ui-input')]
      .filter(input => input.type !== 'password' && !input.closest('.pf-key-wrap'));
  }

  function resolveProvider(form, config) {
    const providers = config.providers || {};
    const inputs = formTextInputs(form);
    const name = inputs[0]?.value.trim();
    const url = inputs.find(input => /^https?:\/\//i.test(input.value.trim()))?.value.trim();
    if (name && Object.hasOwn(providers, name)) {
      const stored = providers[name].base_url ?? providers[name].baseUrl;
      return !url || url === stored ? name : '';
    }
    if (url) {
      const matching = Object.entries(providers).filter(([, provider]) => (provider.base_url ?? provider.baseUrl) === url);
      return matching.length === 1 ? matching[0][0] : '';
    }
    const rows = [...form.querySelectorAll('.pmt-grid')];
    const ids = new Set(rows.map(row => modelInput(row)?.value.trim()).filter(Boolean));
    const matching = Object.keys(providers).filter(providerId => [...ids].some(id => savedModel(config, providerId, id)));
    return matching.length === 1 ? matching[0] : '';
  }

  function hasModelsRecord(config) {
    return Boolean(config && typeof config === 'object' && config.models && typeof config.models === 'object' && !Array.isArray(config.models));
  }

  function readOfficialConfigSnapshot() {
    const reader = typeof installReadOfficialConfig === 'function'
      ? installReadOfficialConfig
      : typeof readOfficialConfig === 'function'
        ? readOfficialConfig
        : null;
    if (!reader) return null;
    try {
      const config = reader();
      return hasModelsRecord(config) ? config : null;
    } catch {
      return null;
    }
  }

  function cloneOpeningConfig(config) {
    if (!hasModelsRecord(config)) return null;
    const providers = {};
    if (config.providers && typeof config.providers === 'object' && !Array.isArray(config.providers)) {
      for (const [id, provider] of Object.entries(config.providers)) {
        if (!provider || typeof provider !== 'object') continue;
        providers[id] = {
          base_url: provider.base_url ?? provider.baseUrl,
          baseUrl: provider.baseUrl ?? provider.base_url,
        };
      }
    }
    const models = {};
    for (const [key, model] of Object.entries(config.models)) {
      if (!model || typeof model !== 'object') continue;
      const copy = { ...model };
      if (Array.isArray(model.capabilities)) copy.capabilities = [...model.capabilities];
      if (Array.isArray(model.support_efforts)) copy.support_efforts = [...model.support_efforts];
      if (Array.isArray(model.supportEfforts)) copy.supportEfforts = [...model.supportEfforts];
      models[key] = copy;
    }
    return { providers, models };
  }

  function cleanStringList(raw) {
    if (!Array.isArray(raw)) return [];
    return raw.filter(value => typeof value === 'string').map(value => value.trim()).filter(Boolean);
  }

  function wireCapabilitySnapshot(model = {}) {
    const rawCapabilities = Object.hasOwn(model, 'capabilities') ? model.capabilities : undefined;
    const rawEfforts = Object.hasOwn(model, 'support_efforts')
      ? model.support_efforts
      : Object.hasOwn(model, 'supportEfforts')
        ? model.supportEfforts
        : undefined;
    const rawAdaptive = Object.hasOwn(model, 'adaptive_thinking')
      ? model.adaptive_thinking
      : Object.hasOwn(model, 'adaptiveThinking')
        ? model.adaptiveThinking
        : undefined;
    const rawDefault = Object.hasOwn(model, 'default_effort')
      ? model.default_effort
      : Object.hasOwn(model, 'defaultEffort')
        ? model.defaultEffort
        : undefined;
    const rawOff = Object.hasOwn(model, 'off_effort')
      ? model.off_effort
      : Object.hasOwn(model, 'offEffort')
        ? model.offEffort
        : undefined;
    return {
      capabilities: cleanStringList(rawCapabilities),
      support_efforts: cleanStringList(rawEfforts),
      adaptive_thinking: typeof rawAdaptive === 'boolean' ? rawAdaptive : undefined,
      default_effort: typeof rawDefault === 'string' ? rawDefault.trim() : '',
      off_effort: typeof rawOff === 'string' ? rawOff.trim() : '',
    };
  }

  function providerModels(config, providerId) {
    const result = new Map();
    const models = config?.models || {};
    const chosenKeys = new Map();
    for (const [key, item] of Object.entries(models)) {
      const { owner, modelId } = resolveModelEntryIdentity(key, item);
      if (owner !== providerId || !modelId) continue;
      const qualified = `${providerId}/${modelId}`;
      const prevKey = chosenKeys.get(modelId);
      if (!prevKey || key === qualified || (prevKey !== qualified && key < prevKey)) {
        chosenKeys.set(modelId, key);
        result.set(modelId, item);
      }
    }
    return result;
  }

  function captureModelsFromConfig(config, providerId) {
    if (!providerId || providerId.startsWith('managed:')) return;
    if (!hasModelsRecord(config)) {
      openingModels.delete(providerId);
      return;
    }
    const models = new Map();
    for (const [modelId, model] of providerModels(config, providerId)) {
      models.set(modelId, { raw: wireCapabilitySnapshot(model), merged: null });
    }
    openingModels.set(providerId, models);
  }

  function captureOpeningModels(current, providerId) {
    if (!providerId || providerId.startsWith('managed:')) return;
    if (current.openingCaptured.has(providerId)) return;
    current.openingCaptured.add(providerId);
    if (!current.openingConfig) {
      openingModels.delete(providerId);
      return;
    }
    captureModelsFromConfig(current.openingConfig, providerId);
  }

  function capturePendingFormOpen() {
    const form = findForm();
    const table = form?.querySelector('.pmt');
    if (!form || !table || !isEditForm(form)) {
      pendingFormOpen = null;
      return;
    }
    if (state.table?.form === form && state.table?.table === table) return;
    if (pendingFormOpen?.form === form && pendingFormOpen?.table === table) return;
    const officialAtOpen = readOfficialConfigSnapshot();
    const snapshotAtOpen = hasModelsRecord(snapshot) ? snapshot : null;
    const openingConfig = cloneOpeningConfig(officialAtOpen || snapshotAtOpen);
    const openingOrigin = resolveOfficialBase()?.origin || '';
    pendingFormOpen = { form, table, openingConfig, openingOrigin };
    if (openingConfig) {
      const openProviderId = resolveProvider(form, openingConfig);
      if (openProviderId && !openProviderId.startsWith('managed:')) {
        captureModelsFromConfig(openingConfig, openProviderId);
      }
    }
  }

  function ensureOpeningCapturedForProvider(providerId) {
    if (!providerId || providerId.startsWith('managed:')) return;
    if (state.table) {
      if (!state.table.openingCaptured.has(providerId)) {
        captureOpeningModels(state.table, providerId);
      }
      return;
    }
    if (!pendingFormOpen) capturePendingFormOpen();
    if (pendingFormOpen) {
      if (pendingFormOpen.openingConfig && !openingModels.has(providerId)) {
        captureModelsFromConfig(pendingFormOpen.openingConfig, providerId);
      } else if (!pendingFormOpen.openingConfig) {
        openingModels.delete(providerId);
      }
    }
  }

  function serializeOpeningModels(providerId) {
    const providerOpening = openingModels.get(providerId);
    if (!providerOpening || providerOpening.size === 0) return {};
    const out = {};
    for (const [modelId, entry] of providerOpening.entries()) {
      if (!entry || typeof entry !== 'object') continue;
      out[modelId] = {
        raw: entry.raw,
        merged: entry.merged || null,
      };
    }
    return out;
  }

  function buildNextOpening(payload, mergedModels) {
    const nextOpening = new Map();
    const mergedMap = mergedModels && typeof mergedModels === 'object' && !Array.isArray(mergedModels)
      ? mergedModels
      : null;
    for (const item of payload.models) {
      if (!item || typeof item !== 'object') continue;
      const modelId = String(item.model || '').trim();
      if (!modelId) continue;
      const rawSnap = wireCapabilitySnapshot(item);
      const mergedEntry = mergedMap && Object.hasOwn(mergedMap, modelId) ? mergedMap[modelId] : null;
      const mergedSnap = mergedEntry && typeof mergedEntry === 'object'
        ? wireCapabilitySnapshot(mergedEntry)
        : null;
      nextOpening.set(modelId, { raw: rawSnap, merged: mergedSnap });
    }
    return nextOpening;
  }

  function isLoopbackHost(hostname) {
    const host = String(hostname || '').toLowerCase();
    return host === '127.0.0.1' || host === 'localhost' || host === '[::1]' || host === '::1';
  }

  function readLocation() {
    try {
      return globalThis.location || (typeof window !== 'undefined' ? window.location : null);
    } catch {
      return null;
    }
  }

  function readQueryOrigin() {
    const loc = readLocation();
    try {
      let search = typeof loc?.search === 'string' ? loc.search : '';
      if (!search && typeof loc?.href === 'string') {
        search = new URL(loc.href).search;
      }
      if (!search) return '';
      if (typeof URLSearchParams === 'function') {
        return new URLSearchParams(search).get('kimi_origin') || '';
      }
      const qs = search.startsWith('?') ? search : `?${search}`;
      return new URL(`http://localhost/${qs}`).searchParams.get('kimi_origin') || '';
    } catch {
      return '';
    }
  }

  function readSessionStorageOrigin() {
    try {
      const storage = globalThis.sessionStorage || (typeof window !== 'undefined' ? window.sessionStorage : null);
      return storage?.getItem?.('kimi-desktop-server-origin') || '';
    } catch {
      return '';
    }
  }

  function parseOfficialBase(raw, originOnly = false) {
    const text = typeof raw === 'string'
      ? raw.trim()
      : (typeof URL === 'function' && raw instanceof URL ? raw.href : '');
    if (!text) return null;
    let parsed;
    try {
      parsed = new URL(text);
    } catch {
      return null;
    }
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return null;
    if (!isLoopbackHost(parsed.hostname)) return null;
    if (originOnly) return { origin: parsed.origin, basePath: '' };
    const basePath = parsed.pathname.replace(/\/v1\/?$/, '').replace(/\/$/, '');
    return { origin: parsed.origin, basePath };
  }

  function resolveOfficialBase() {
    if (officialOrigin !== undefined && officialOrigin !== null) {
      const raw = typeof officialOrigin === 'function' ? officialOrigin() : officialOrigin;
      return parseOfficialBase(raw);
    }
    const queryOrigin = readQueryOrigin();
    if (queryOrigin && queryOrigin.trim()) {
      return parseOfficialBase(queryOrigin);
    }
    const storedOrigin = readSessionStorageOrigin();
    if (storedOrigin && storedOrigin.trim()) {
      return parseOfficialBase(storedOrigin);
    }
    const loc = readLocation();
    const fromOrigin = parseOfficialBase(loc?.origin, true);
    if (fromOrigin) return fromOrigin;
    return parseOfficialBase(loc?.href, true);
  }

  function resolveExcludedOrigins() {
    const excluded = new Set();
    for (const source of [excludeOrigins, installExcludeOrigins]) {
      const rawList = typeof source === 'function' ? source() : source;
      if (!rawList) continue;
      for (const item of rawList) {
        const text = typeof item === 'string'
          ? item.trim()
          : (typeof URL === 'function' && item instanceof URL ? item.href : '');
        if (!text) continue;
        try {
          const parsed = new URL(text);
          if (parsed.origin && parsed.origin !== 'null') excluded.add(parsed.origin);
        } catch {
          // Ignore invalid exclude entries.
        }
      }
    }
    return excluded;
  }

  function parseOfficialApiRequest(input, init) {
    const method = String(init?.method ?? (input && typeof input === 'object' ? input.method : '') ?? 'GET').toUpperCase();
    const official = resolveOfficialBase();
    if (!official || official.basePath) return null;
    const rawUrl = typeof input === 'string'
      ? input
      : (typeof URL === 'function' && input instanceof URL)
        ? input.href
        : (typeof input?.url === 'string' ? input.url : '');
    if (!rawUrl) return null;
    let parsed;
    try {
      const loc = readLocation();
      const base = typeof loc?.href === 'string' && loc.href ? loc.href : undefined;
      try {
        parsed = base ? new URL(rawUrl, base) : new URL(rawUrl);
      } catch {
        parsed = new URL(rawUrl);
      }
    } catch {
      return null;
    }
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return null;
    if (parsed.origin !== official.origin) return null;
    if (resolveExcludedOrigins().has(parsed.origin)) return null;
    return { method, origin: parsed.origin, pathname: parsed.pathname };
  }

  function matchCatalogImportPost(input, init) {
    const parsed = parseOfficialApiRequest(input, init);
    if (!parsed || parsed.method !== 'POST' || parsed.pathname !== '/api/v1/providers:import_catalog') return null;
    return { origin: parsed.origin };
  }

  function recordCatalogImport(origin, providerId) {
    if (!origin || !providerId || providerId.startsWith('managed:')) return;
    recentCatalogImports.set(`${origin}\n${providerId}`, Date.now());
  }

  function hasRecentCatalogImport(origin, providerId) {
    const key = `${origin}\n${providerId}`;
    const timestamp = recentCatalogImports.get(key);
    if (timestamp === undefined) return false;
    if (Date.now() - timestamp > CATALOG_IMPORT_TTL_MS) {
      recentCatalogImports.delete(key);
      return false;
    }
    return true;
  }

  function matchProviderPut(input, init) {
    if (typeof init?.body !== 'string') return null;
    const parsed = parseOfficialApiRequest(input, init);
    if (!parsed || parsed.method !== 'PUT') return null;
    const match = /^\/api\/v1\/providers\/([^/?#]+)$/.exec(parsed.pathname);
    if (!match) return null;
    let providerId;
    try {
      providerId = decodeURIComponent(match[1]);
    } catch {
      return null;
    }
    if (!providerId || providerId.startsWith('managed:')) return null;
    return { providerId, origin: parsed.origin };
  }

  function abortReason(signal) {
    if (signal?.reason !== undefined) return signal.reason;
    if (typeof DOMException === 'function') return new DOMException('The operation was aborted.', 'AbortError');
    return Object.assign(new Error('The operation was aborted.'), { name: 'AbortError' });
  }

  function raceWithSignal(promise, signal) {
    if (!signal) return promise;
    if (signal.aborted) {
      Promise.resolve(promise).catch(() => {});
      return Promise.reject(abortReason(signal));
    }
    let onAbort;
    const abortPromise = new Promise((_, reject) => {
      onAbort = () => reject(abortReason(signal));
      signal.addEventListener('abort', onAbort, { once: true });
    });
    const guarded = Promise.resolve(promise);
    guarded.catch(() => {});
    return Promise.race([guarded, abortPromise]).finally(() => {
      signal.removeEventListener('abort', onAbort);
    });
  }

  function createDaemonJsonResponse(status, bodyObj) {
    const statusText = HTTP_STATUS_TEXT[status] || (status >= 200 && status < 300 ? 'OK' : 'Error');
    const bodyStr = JSON.stringify(bodyObj);
    if (typeof Response === 'function') {
      return new Response(bodyStr, {
        status,
        statusText,
        headers: { 'Content-Type': 'application/json; charset=utf-8' },
      });
    }
    return {
      ok: status >= 200 && status < 300,
      status,
      statusText,
      headers: {
        get: key => (String(key).toLowerCase() === 'content-type' ? 'application/json; charset=utf-8' : null),
        [Symbol.iterator]: function* () {
          yield ['content-type', 'application/json; charset=utf-8'];
        },
      },
      clone() {
        return this;
      },
      text: async () => bodyStr,
      json: async () => JSON.parse(bodyStr),
    };
  }

  function createSaveErrorResponse(err) {
    const status = Number.isInteger(err?.status) && err.status >= 400 && err.status < 600 ? err.status : 503;
    const kapCode = Number.isInteger(err?.kapCode) ? err.kapCode : 50300;
    const msg = String(err?.message || '无法读取或合并最新增强模型配置，已中止保存，请重试。');
    return createDaemonJsonResponse(status, { code: kapCode, msg, data: null });
  }

  async function forwardMergedProviderPut(providerId, expectedOrigin, payload, signal, { preserveCatalogImport = false } = {}) {
    if (signal?.aborted) throw abortReason(signal);
    const activeFormOrigin = state.table?.form?.isConnected
      ? state.table.openingOrigin
      : (pendingFormOpen?.form?.isConnected ? pendingFormOpen.openingOrigin : '');
    if (!preserveCatalogImport && activeFormOrigin && activeFormOrigin !== expectedOrigin) {
      return createDaemonJsonResponse(409, {
        code: 40900,
        msg: '当前窗口连接的官方服务实例已变化，请关闭表单后重新打开再保存。',
        data: null,
      });
    }
    if (preserveCatalogImport) {
      openingModels.delete(providerId);
    } else {
      ensureOpeningCapturedForProvider(providerId);
    }
    const requestPayload = {
      providerId,
      expectedOrigin,
      payload,
      opening: preserveCatalogImport ? {} : serializeOpeningModels(providerId),
    };
    if (preserveCatalogImport) requestPayload.preserveCatalogImport = true;
    let result;
    try {
      result = await raceWithSignal(
        request('providers.saveOfficialForm', requestPayload, { signal }),
        signal,
      );
    } catch (err) {
      if (signal?.aborted || err?.name === 'AbortError') {
        throw signal?.aborted ? abortReason(signal) : err;
      }
      return createSaveErrorResponse(err);
    }
    if (signal?.aborted) throw abortReason(signal);
    if (!result || typeof result !== 'object' || Array.isArray(result)) {
      return createSaveErrorResponse(null);
    }
    if (preserveCatalogImport) {
      recentCatalogImports.delete(`${expectedOrigin}\n${providerId}`);
    }
    if (!removed && enabled) {
      const nextProviderId = typeof payload.new_id === 'string' && payload.new_id.trim()
        ? payload.new_id.trim()
        : providerId;
      if (nextProviderId !== providerId) openingModels.delete(providerId);
      openingModels.set(nextProviderId, buildNextOpening(payload, result.mergedModels));
      if (state.table?.openingCaptured) {
        state.table.openingCaptured.delete(providerId);
        state.table.openingCaptured.add(nextProviderId);
      }
      invalidate();
    }
    return createDaemonJsonResponse(200, {
      code: 0,
      msg: '',
      data: result?.data ?? null,
    });
  }

  function handleFetch(baseFetch, thisArg, input, init) {
    const catalogImport = matchCatalogImportPost(input, init);
    if (catalogImport) {
      let requestedId = '';
      if (typeof init?.body === 'string') {
        try {
          const parsedBody = JSON.parse(init.body);
          const candidate = parsedBody?.id || parsedBody?.catalog_id;
          if (typeof candidate === 'string') requestedId = candidate.trim();
        } catch {
          // Ignore malformed request bodies and let official server handle them.
        }
      }
      return Promise.resolve(baseFetch.call(thisArg, input, init)).then(async res => {
        if (res?.ok && !removed && enabled) {
          let importedId = requestedId;
          if (typeof res.clone === 'function') {
            try {
              const body = await res.clone().json();
              const resolved = body?.data?.provider?.id ?? body?.provider?.id;
              if (typeof resolved === 'string' && resolved.trim()) importedId = resolved.trim();
            } catch {
              // Keep requestedId fallback when response body cannot be parsed.
            }
          }
          if (importedId) {
            recordCatalogImport(catalogImport.origin, importedId);
            openingModels.delete(importedId);
            invalidate();
          }
        }
        return res;
      });
    }
    const matched = matchProviderPut(input, init);
    if (!matched) return baseFetch.call(thisArg, input, init);
    let payload;
    try {
      payload = JSON.parse(init.body);
    } catch {
      return baseFetch.call(thisArg, input, init);
    }
    if (!payload || typeof payload !== 'object' || !Array.isArray(payload.models)) {
      return baseFetch.call(thisArg, input, init);
    }
    const signal = init?.signal !== undefined
      ? init.signal
      : (input && typeof input === 'object' ? input.signal : null);
    const preserveCatalogImport = !isEditForm(findForm()) && hasRecentCatalogImport(matched.origin, matched.providerId);
    return forwardMergedProviderPut(matched.providerId, matched.origin, payload, signal, { preserveCatalogImport });
  }

  function attachFetch() {
    if (wrappedFetch || typeof globalThis.fetch !== 'function') return;
    const baseFetch = globalThis.fetch;
    originalFetch = baseFetch;
    wrappedFetch = function ekdProviderEnhanceFetch(input, init) {
      if (!enabled || removed || wrappedFetch !== ekdProviderEnhanceFetch) {
        return baseFetch.call(this, input, init);
      }
      return handleFetch(baseFetch, this, input, init);
    };
    globalThis.fetch = wrappedFetch;
  }

  function detachFetch() {
    if (!wrappedFetch) return;
    if (globalThis.fetch === wrappedFetch) globalThis.fetch = originalFetch;
    wrappedFetch = null;
    originalFetch = null;
  }

  function syncOfficialContextInput(current, modelId, maxContextSize) {
    if (!current || !Number.isSafeInteger(maxContextSize) || maxContextSize < 1) return;
    const nextValue = String(maxContextSize);
    for (const [node, row] of current.rows) {
      if (row.input.value.trim() !== modelId) continue;
      const ctxInput = modelInputs(node)[1];
      if (!ctxInput || ctxInput.value === nextValue) continue;
      ctxInput.value = nextValue;
      if (typeof ctxInput.dispatchEvent === 'function') {
        ctxInput.dispatchEvent(new Event('input', { bubbles: true }));
        ctxInput.dispatchEvent(new Event('change', { bubbles: true }));
      }
    }
  }

  function hasOfficialModelEdits(current, config, providerId) {
    return [...current.table.querySelectorAll('.pmt-grid')].some(row => {
      const [id, context, name] = modelInputs(row);
      if (!id) return false;
      const model = savedModel(config, providerId, id.value.trim());
      if (!model) return Boolean(id.value.trim() || context?.value.trim() || name?.value.trim());
      const fields = modelFields(model);
      return (context && Number(context.value.trim()) !== fields.context)
        || (name && name.value.trim() !== (model.displayName ?? model.display_name ?? ''));
    });
  }

  function ensureStyle() {
    if (state.style?.isConnected) return;
    state.style = el('style', { 'data-ekd-provider-enhance-style': '1', text: `
      :is(.settings-dialog,.ui-dialog,[role="dialog"]) .pf .pmt[data-ekd-px-table="1"] :is(.pmt-grid.pmt-head,.pmt-grid[data-ekd-px-row="1"]){
        grid-template-columns:minmax(0,1.35fr) minmax(0,.68fr) minmax(0,1fr) 26px 112px;
        row-gap:8px;
      }
      :is(.settings-dialog,.ui-dialog,[role="dialog"]) .pf .pmt[data-ekd-px-table="1"][data-ekd-px-selecting="1"] :is(.pmt-grid.pmt-head,.pmt-grid[data-ekd-px-row="1"]){
        grid-template-columns:22px minmax(0,1.35fr) minmax(0,.68fr) minmax(0,1fr) 26px 112px;
      }
      @media(max-width:640px){
        :is(.settings-dialog,.ui-dialog,[role="dialog"]) .pf .pmt[data-ekd-px-table="1"] :is(.pmt-grid.pmt-head,.pmt-grid[data-ekd-px-row="1"]){
          grid-template-columns:minmax(0,1fr) auto;
        }
        .ekd-px-head-pad,.ekd-px-head-sel{display:none!important}
      }
      [data-ekd-px-mount][hidden],[data-ekd-px-mount] [hidden]{display:none!important}
      .ekd-px-head-pad{display:inline-block;width:112px;height:1px;pointer-events:none}
      .ekd-px-head-sel{display:inline-block;width:22px;height:1px;pointer-events:none}
      .ekd-px-toolbar{
        display:flex;align-items:center;justify-content:flex-end;gap:6px;flex-wrap:wrap;
        margin:0 0 8px;
        color:var(--color-text,inherit);
        font-family:var(--font-ui,system-ui,-apple-system,sans-serif);
        -webkit-app-region:no-drag;
      }
      .ekd-px-toolbar .ekd-px-status{margin-right:auto;font-size:12px}
      .ekd-px-sel{
        display:inline-flex;align-items:center;justify-content:center;
        width:22px;height:28px;cursor:pointer;flex:none;
      }
      .ekd-px-sel[data-empty="1"]{visibility:hidden;pointer-events:none}
      .ekd-px-sel-input{
        appearance:none;-webkit-appearance:none;
        width:16px;height:16px;margin:0;padding:0;
        border:1px solid color-mix(in srgb,currentColor 26%,transparent);
        border-radius:5px;
        background:color-mix(in srgb,currentColor 3%,transparent);
        cursor:pointer;position:relative;flex:none;
        transition:border-color .12s ease,background .12s ease;
      }
      .ekd-px-sel-input:hover:not(:disabled){
        border-color:color-mix(in srgb,var(--color-accent,#1783ff) 65%,transparent);
      }
      .ekd-px-sel-input:checked{
        border-color:var(--color-accent,#1783ff);
        background:var(--color-accent,#1783ff);
      }
      .ekd-px-sel-input:checked::after{
        content:'';position:absolute;left:4px;top:1px;
        width:5px;height:9px;
        border:solid var(--color-text-on-accent,#fff);
        border-width:0 1.8px 1.8px 0;
        transform:rotate(42deg);
      }
      .ekd-px-sel-input:focus-visible{outline:2px solid var(--color-accent,#1783ff);outline-offset:2px}
      .ekd-px-btn{
        display:inline-flex;align-items:center;justify-content:center;gap:4px;flex:none;
        height:28px;padding:0 9px;
        border:1px solid color-mix(in srgb,currentColor 18%,transparent);
        border-radius:7px;
        background:color-mix(in srgb,currentColor 4%,transparent);
        color:var(--color-text,inherit);
        font-family:var(--font-ui,system-ui,-apple-system,sans-serif);
        font-size:12px;font-weight:500;line-height:1;
        white-space:nowrap;cursor:pointer;
        -webkit-app-region:no-drag;
        transition:border-color .14s ease,background .14s ease,color .14s ease;
      }
      .ekd-px-btn:hover:not(:disabled){
        border-color:color-mix(in srgb,var(--color-accent,#1783ff) 62%,transparent);
        background:var(--color-accent-soft,color-mix(in srgb,var(--color-accent,#1783ff) 13%,transparent));
        color:light-dark(var(--color-accent,#1783ff),#6eb2ff);
      }
      .ekd-px-btn[aria-expanded="true"]{
        border-color:var(--color-accent,#1783ff);
        background:var(--color-accent-soft,color-mix(in srgb,var(--color-accent,#1783ff) 16%,transparent));
        color:light-dark(var(--color-accent,#1783ff),#6eb2ff);
      }
      .pmt-grid[data-ekd-px-row="1"] > .ekd-px-btn{height:32px;width:100%}
      .ekd-px-btn:disabled{opacity:.48;cursor:not-allowed}
      .ekd-px-btn:focus-visible{outline:2px solid var(--color-accent,#1783ff);outline-offset:2px}
      .ekd-px-btn-compact{height:25px;padding:0 8px;font-size:11.5px}
      .ekd-px-caret{display:inline-flex;transition:transform .16s ease}
      .ekd-px-btn[aria-expanded="true"] .ekd-px-caret{transform:rotate(180deg)}

      .ekd-px-drawer{
        grid-column:1 / -1;
        display:flex;flex-direction:column;gap:12px;
        margin:2px 0 6px;padding:13px 14px;
        border:1px solid color-mix(in srgb,currentColor 15%,transparent);
        border-radius:10px;
        background:var(--color-surface,color-mix(in srgb,currentColor 4%,var(--color-bg,transparent)));
        color:var(--color-text,inherit);
        font:13px/1.5 var(--font-ui,system-ui,-apple-system,sans-serif);
        cursor:default;
        -webkit-app-region:no-drag;
      }
      .ekd-px-drawer[data-ekd-px-batch="1"]{margin:0 0 10px}
      .ekd-px-body{display:flex;flex-direction:column;gap:12px;min-width:0}
      .ekd-px-drawer-head{
        display:flex;align-items:center;justify-content:space-between;gap:8px;flex-wrap:wrap;
        padding-bottom:9px;
        border-bottom:1px solid color-mix(in srgb,currentColor 10%,transparent);
      }
      .ekd-px-drawer-title{
        display:flex;align-items:center;gap:8px;flex-wrap:wrap;min-width:0;
      }
      .ekd-px-drawer-name{font-size:12.5px;font-weight:600;color:var(--color-text,inherit)}
      .ekd-px-identity{
        font-size:11.5px;color:var(--color-text-muted,color-mix(in srgb,currentColor 60%,transparent));
        overflow-wrap:anywhere;margin:0;
      }
      .ekd-px-suggest-bar{display:flex;align-items:center;gap:8px;flex-wrap:wrap}
      .ekd-px-top-grid{
        display:grid;grid-template-columns:minmax(0,auto) minmax(0,1fr);
        gap:12px 18px;align-items:start;
      }
      @media(max-width:640px){
        .ekd-px-top-grid{grid-template-columns:1fr}
      }
      .ekd-px-section{display:flex;flex-direction:column;gap:6px;min-width:0}
      .ekd-px-title{
        font-size:11.5px;font-weight:600;letter-spacing:.01em;
        color:var(--color-text-muted,color-mix(in srgb,currentColor 64%,transparent));
      }
      .ekd-px-pills{display:flex;flex-wrap:wrap;gap:6px;align-items:center}
      .ekd-px-pill{
        display:inline-flex;align-items:center;gap:6px;
        height:27px;padding:0 10px;
        border:1px solid color-mix(in srgb,currentColor 18%,transparent);
        border-radius:7px;
        background:color-mix(in srgb,currentColor 3%,transparent);
        color:color-mix(in srgb,currentColor 84%,transparent);
        font:inherit;font-size:12px;font-weight:500;line-height:1;
        cursor:pointer;transition:border-color .12s ease,background .12s ease,color .12s ease;
      }
      .ekd-px-pill:hover:not(:disabled){
        border-color:color-mix(in srgb,currentColor 34%,transparent);
        background:color-mix(in srgb,currentColor 8%,transparent);
        color:inherit;
      }
      .ekd-px-pill[aria-pressed="true"]{
        border-color:var(--color-accent,#1783ff);
        background:var(--color-accent-soft,color-mix(in srgb,var(--color-accent,#1783ff) 16%,transparent));
        color:light-dark(var(--color-accent,#1783ff),#6eb2ff);
        font-weight:600;
      }
      .ekd-px-pill[data-kind="mode"][data-mode="none"][aria-pressed="true"]{
        border-color:color-mix(in srgb,currentColor 38%,transparent);
        background:color-mix(in srgb,currentColor 11%,transparent);
        color:inherit;
      }
      .ekd-px-pill:disabled{opacity:.45;cursor:not-allowed}
      .ekd-px-pill:focus-visible{outline:2px solid var(--color-accent,#1783ff);outline-offset:2px}
      .ekd-px-dot{
        width:6px;height:6px;border-radius:50%;
        background:color-mix(in srgb,currentColor 32%,transparent);
      }
      .ekd-px-pill[aria-pressed="true"] .ekd-px-dot{background:currentColor}
      .ekd-px-divider{
        width:1px;height:16px;margin:0 2px;
        background:color-mix(in srgb,currentColor 16%,transparent);
      }

      .ekd-px-limits{
        display:grid;grid-template-columns:repeat(2,minmax(0,1fr));
        gap:12px;align-items:start;
      }
      @media(max-width:560px){
        .ekd-px-limits{grid-template-columns:1fr}
      }
      .ekd-px-input-wrap{display:flex;align-items:center;gap:6px;min-width:0}
      .ekd-px-input{
        box-sizing:border-box;width:100%;min-width:0;height:30px;padding:0 10px;
        border:1px solid color-mix(in srgb,currentColor 20%,transparent);
        border-radius:7px;
        background:var(--color-bg,transparent);
        color:var(--color-text,inherit);font:inherit;font-size:12.5px;
        font-variant-numeric:tabular-nums;
        outline:none;transition:border-color .12s ease,box-shadow .12s ease;
      }
      .ekd-px-input::placeholder{color:var(--color-text-faint,color-mix(in srgb,currentColor 42%,transparent))}
      .ekd-px-input:focus{
        border-color:var(--color-accent,#1783ff);
        box-shadow:0 0 0 2px color-mix(in srgb,var(--color-accent,#1783ff) 20%,transparent);
      }
      .ekd-px-input[aria-invalid="true"]{border-color:var(--color-danger,#c0392b)}
      .ekd-px-input:disabled{opacity:.5;cursor:not-allowed}

      .ekd-px-presets{display:flex;flex-wrap:wrap;gap:5px;align-items:center}
      .ekd-px-preset{
        height:22px;padding:0 8px;
        border:1px solid color-mix(in srgb,currentColor 16%,transparent);
        border-radius:999px;
        background:transparent;
        color:var(--color-text-muted,color-mix(in srgb,currentColor 68%,transparent));
        font:inherit;font-size:11px;font-weight:500;
        font-variant-numeric:tabular-nums;
        cursor:pointer;transition:border-color .12s ease,background .12s ease,color .12s ease;
      }
      .ekd-px-preset:hover:not(:disabled){
        border-color:color-mix(in srgb,currentColor 34%,transparent);
        color:inherit;
      }
      .ekd-px-preset[aria-pressed="true"]{
        border-color:var(--color-accent,#1783ff);
        background:var(--color-accent-soft,color-mix(in srgb,var(--color-accent,#1783ff) 16%,transparent));
        color:light-dark(var(--color-accent,#1783ff),#6eb2ff);
        font-weight:600;
      }
      .ekd-px-preset:disabled{opacity:.45;cursor:not-allowed}
      .ekd-px-preset:focus-visible{outline:2px solid var(--color-accent,#1783ff);outline-offset:1px}

      .ekd-px-meta{
        font-size:11.5px;line-height:1.4;margin:0;
        color:var(--color-text-muted,color-mix(in srgb,currentColor 56%,transparent));
      }
      .ekd-px-hint{
        color:var(--color-text-muted,color-mix(in srgb,currentColor 60%,transparent));
        font-size:12px;margin:0;overflow-wrap:anywhere;
      }
      .ekd-px-issue{color:var(--color-danger,#c0392b);font-size:11.5px;line-height:1.4;margin:0}

      .ekd-px-foot{
        display:flex;flex-wrap:wrap;gap:8px;align-items:center;
        padding-top:9px;
        border-top:1px solid color-mix(in srgb,currentColor 12%,transparent);
      }
      .ekd-px-status{
        margin-right:auto;font-size:12px;margin-top:0;margin-bottom:0;
        color:var(--color-text-muted,color-mix(in srgb,currentColor 60%,transparent));
        overflow-wrap:anywhere;
      }
      .ekd-px-status[data-tone="ok"]{color:var(--color-success,#0e7a38)}
      .ekd-px-status[data-tone="error"]{color:var(--color-danger,#c0392b)}
      .ekd-px-actions{display:flex;flex-wrap:wrap;align-items:center;gap:6px}
      .ekd-px-primary{
        display:inline-flex;align-items:center;justify-content:center;gap:4px;
        height:28px;padding:0 13px;border:0;border-radius:7px;
        background:var(--color-accent,#1783ff);
        color:var(--color-text-on-accent,#fff);
        font-family:var(--font-ui,system-ui,-apple-system,sans-serif);
        font-size:12.5px;font-weight:500;white-space:nowrap;cursor:pointer;
      }
      .ekd-px-primary:hover:not(:disabled){background:var(--color-accent-hover,#167ff7)}
      .ekd-px-primary:disabled{opacity:.48;cursor:not-allowed}
      .ekd-px-primary:focus-visible{outline:2px solid var(--color-accent,#1783ff);outline-offset:2px}
      .ekd-px-quiet{
        display:inline-flex;align-items:center;justify-content:center;gap:4px;
        height:28px;padding:0 11px;
        border:1px solid color-mix(in srgb,currentColor 18%,transparent);
        border-radius:7px;background:transparent;color:var(--color-text,inherit);
        font-family:var(--font-ui,system-ui,-apple-system,sans-serif);
        font-size:12.5px;white-space:nowrap;cursor:pointer;
      }
      .ekd-px-quiet:hover:not(:disabled){background:var(--color-hover,color-mix(in srgb,currentColor 6%,transparent))}
      .ekd-px-quiet:disabled{opacity:.48;cursor:not-allowed}
      .ekd-px-quiet:focus-visible{outline:2px solid var(--color-accent,#1783ff);outline-offset:2px}
      .ekd-px-readonly{
        padding:8px 10px;border-radius:7px;
        border:1px solid color-mix(in srgb,currentColor 16%,transparent);
        background:color-mix(in srgb,currentColor 4%,transparent);
        color:var(--color-text-muted,color-mix(in srgb,currentColor 68%,transparent));
        font-size:12px;
      }
      @media(prefers-reduced-motion:reduce){
        .ekd-px-btn,.ekd-px-caret,.ekd-px-pill,.ekd-px-input,.ekd-px-preset,.ekd-px-sel-input{transition:none}
      }
    ` });
    document.head.append(state.style);
  }

  function setText(node, value) {
    if (node.textContent !== value) node.textContent = value;
  }

  function setHidden(node, next) {
    const hidden = Boolean(next);
    if (node && node.hidden !== hidden) node.hidden = hidden;
  }

  function buildCatalogBackfillEdit(model, suggestion) {
    if (!model || !suggestion || typeof suggestion !== 'object') return {};
    const fields = modelFields(model);
    const edit = {};

    const currentCaps = CAPABILITIES.filter(([key]) => fields.capabilities.has(key)).map(([key]) => key);
    const nextCapSet = new Set(currentCaps);
    for (const [key] of CAPABILITIES) {
      if (typeof suggestion[key] === 'boolean') {
        if (suggestion[key]) nextCapSet.add(key);
        else nextCapSet.delete(key);
      }
    }
    const nextCaps = CAPABILITIES.filter(([key]) => nextCapSet.has(key)).map(([key]) => key);
    if (JSON.stringify(nextCaps) !== JSON.stringify(currentCaps)) {
      edit.capabilities = nextCaps;
    }

    if (typeof suggestion.thinking === 'boolean') {
      const targetThinking = suggestion.thinking
        ? (fields.thinking === 'always' ? 'always' : 'toggle')
        : 'none';
      if (targetThinking !== fields.thinking) {
        edit.thinking = targetThinking;
      }
    }

    let targetContext = fields.context;
    const suggestedContext = Number(suggestion.max_context_size);
    if (Number.isSafeInteger(suggestedContext) && suggestedContext > 0) {
      targetContext = suggestedContext;
      if (targetContext !== fields.context) {
        edit.max_context_size = targetContext;
      }
    }

    const suggestedOutput = Number(suggestion.max_output_size);
    if (Number.isSafeInteger(suggestedOutput) && suggestedOutput > 0 && (targetContext <= 0 || suggestedOutput <= targetContext)) {
      if (suggestedOutput !== fields.output) {
        edit.max_output_size = suggestedOutput;
      }
    } else if (fields.output !== null && targetContext > 0 && fields.output > targetContext) {
      edit.max_output_size = null;
    }

    return edit;
  }

  async function runCatalogBackfill(current) {
    if (state.panel) {
      closePanel();
      if (state.panel) return;
    }
    if (current.backfilling || current.backfill?.getAttribute('aria-busy') === 'true') return;
    const startGeneration = generation;
    current.backfilling = true;
    current.backfill.setAttribute('aria-busy', 'true');
    current.feedback = '正在查询 models.dev 目录并补全能力与限制…';
    current.feedbackTone = '';
    updateSelection(current);
    try {
      const config = await readConfig();
      if (startGeneration !== generation || state.table !== current || !current.backfill.isConnected || state.panel) return;
      const providerId = resolveProvider(current.form, config);
      if (!providerId || providerId.startsWith('managed:')) {
        current.feedback = '当前供应商不支持按目录补全。';
        current.feedbackTone = 'error';
        render(current, config);
        return;
      }
      const rows = [...current.rows.values()];
      const unsavedIds = new Set();
      const targetIds = [];
      const seen = new Set();
      for (const row of rows) {
        const id = row.input.value.trim();
        if (!id) continue;
        const model = savedModel(config, providerId, id);
        if (!model) {
          unsavedIds.add(id);
          continue;
        }
        if (current.selecting && !row.select.checked) continue;
        if (!seen.has(id)) {
          seen.add(id);
          targetIds.push(id);
        }
      }
      if (!targetIds.length) {
        current.feedback = current.selecting
          ? '请先选择至少一个已保存模型，再按目录补全。'
          : (unsavedIds.size > 0
            ? '请先点击官方表单「保存」，再按目录补全能力与限制。'
            : '当前没有可补全的已保存模型。');
        current.feedbackTone = '';
        render(current, config);
        return;
      }

      const result = await request('providers.modelSuggestion', {
        providerId,
        models: targetIds.map(id => `${providerId}/${id}`),
      });
      if (startGeneration !== generation || state.table !== current || !current.backfill.isConnected || state.panel) return;

      const suggestions = result?.suggestions && typeof result.suggestions === 'object' ? result.suggestions : {};
      const updates = [];
      let matched = 0;
      let unchanged = 0;
      let unmatched = 0;
      for (const id of targetIds) {
        const suggestion = suggestions[id] ?? suggestions[`${providerId}/${id}`];
        if (!suggestion || typeof suggestion !== 'object' || !Object.keys(suggestion).length) {
          unmatched += 1;
          continue;
        }
        matched += 1;
        const edit = buildCatalogBackfillEdit(savedModel(config, providerId, id), suggestion);
        if (Object.keys(edit).length > 0) {
          updates.push({ id, model: `${providerId}/${id}`, edit });
        } else {
          unchanged += 1;
        }
      }

      const unsavedNote = !current.selecting && unsavedIds.size > 0
        ? `；另有 ${unsavedIds.size} 个未保存模型请先点击官方保存`
        : '';

      if (!matched) {
        current.feedback = `未在 models.dev 目录中匹配到当前供应商的模型${unsavedNote}。`;
        current.feedbackTone = '';
        render(current, config);
        return;
      }

      if (!updates.length) {
        const extra = unmatched > 0 ? `，${unmatched} 个未命中目录` : '';
        current.feedback = `已核对 ${matched} 个模型，能力与限制均已与目录一致${extra}${unsavedNote}。`;
        current.feedbackTone = 'ok';
        render(current, config);
        return;
      }

      await request('providers.updateModels', {
        providerId,
        models: updates.map(({ model, edit }) => ({ model, edit })),
      });
      if (state.table !== current) return;

      for (const { id, edit } of updates) {
        if (edit.max_context_size !== undefined) {
          syncOfficialContextInput(current, id, edit.max_context_size);
        }
      }
      if (current.selecting) {
        current.selecting = false;
        for (const row of current.rows.values()) row.select.checked = false;
      }
      const details = [];
      if (unchanged > 0) details.push(`${unchanged} 个已最新`);
      if (unmatched > 0) details.push(`${unmatched} 个未命中目录`);
      const detailText = details.length ? `（${details.join('，')}）` : '';
      current.feedback = `已按目录补全 ${updates.length} 个模型${detailText}${unsavedNote}。`;
      current.feedbackTone = 'ok';
      invalidate();
      window.dispatchEvent(new CustomEvent('ekd:config-saved'));
    } catch (error) {
      if (state.table === current) {
        current.feedback = `按目录补全失败。${error?.message || '请重试。'}`;
        current.feedbackTone = 'error';
      }
    } finally {
      current.backfilling = false;
      current.backfill?.removeAttribute('aria-busy');
      if (state.table === current) updateSelection(current);
    }
  }

  function updateSelection(current) {
    const selectable = [...current.rows.values()].filter(row => row.model);
    const count = new Set(selectable.filter(row => row.select.checked).map(row => row.modelId)).size;
    if (current.selecting) current.table.setAttribute('data-ekd-px-selecting', '1');
    else current.table.removeAttribute('data-ekd-px-selecting');
    setHidden(current.headSel, !current.selecting);
    for (const row of current.rows.values()) {
      setHidden(row.selection, !current.selecting);
      if (current.selecting) {
        if (row.model) {
          row.selection.removeAttribute('data-empty');
          row.select.disabled = false;
        } else {
          row.selection.setAttribute('data-empty', '1');
          row.select.disabled = true;
        }
      }
    }
    setText(current.count, current.backfilling
      ? (current.feedback || '正在按目录补全…')
      : (current.selecting ? `已选 ${count} 个模型` : (current.feedback || '')));
    current.count.dataset.tone = current.backfilling || current.selecting ? '' : (current.feedbackTone || '');
    setText(current.toggle, current.selecting ? '退出批量配置' : '批量配置');
    setHidden(current.all, !current.selecting);
    setHidden(current.apply, !current.selecting);
    current.backfill.disabled = current.backfilling || selectable.length === 0 || (current.selecting && count === 0);
    current.toggle.disabled = current.backfilling;
    current.all.disabled = current.backfilling || selectable.length === 0;
    current.apply.disabled = current.backfilling || count === 0;
  }

  function decorateToolbar(current) {
    current.count = el('span', { class: 'ekd-px-status', role: 'status' });
    current.backfill = button('按目录补全', () => {
      void runCatalogBackfill(current);
    }, { title: '按 models.dev 目录补全已保存模型的能力与长度限制' });
    current.toggle = button('批量配置', () => {
      if (state.panel?.batch) {
        closePanel();
        if (state.panel) return;
      }
      current.feedback = '';
      current.feedbackTone = '';
      current.selecting = !current.selecting;
      if (!current.selecting) for (const row of current.rows.values()) row.select.checked = false;
      updateSelection(current);
    });
    current.all = button('全选已保存模型', () => {
      for (const row of current.rows.values()) row.select.checked = Boolean(row.model);
      updateSelection(current);
    });
    current.apply = button('编辑能力模板', async () => {
      const selected = [...current.rows.values()].filter(row => row.select.checked).map(row => row.modelId);
      const shown = current.apply;
      await withFreshConfig(current, shown, (config, providerId) => {
        const ids = [...new Set(selected)];
        if (!ids.length || providerId !== current.providerId || providerId.startsWith('managed:') || !ids.every(id => savedModel(config, providerId, id))) {
          showMessage(current, shown, '所选模型已变化，请重新选择后再应用模板。', null);
          return;
        }
        openPanel({ current, button: shown, rowNode: null, providerId, ids, officialDirty: hasOfficialModelEdits(current, config, providerId) });
      });
    }, { class: 'ekd-px-primary', 'aria-controls': PANEL_ID, 'aria-expanded': 'false' });
    current.toolbar = el('div', { class: 'ekd-px-toolbar', 'data-ekd-px-mount': '1', role: 'group', 'aria-label': '模型能力与批量配置' }, [
      current.count, current.backfill, current.toggle, current.all, current.apply,
    ]);
    current.table.before(current.toolbar);
  }

  async function withFreshConfig(current, trigger, callback) {
    if (state.panel) {
      const sameTrigger = state.panel.trigger === trigger;
      closePanel();
      if (sameTrigger || state.panel) return;
    }
    if (trigger.getAttribute('aria-busy') === 'true') return;
    const startGeneration = generation;
    trigger.setAttribute('aria-busy', 'true');
    try {
      const config = await readConfig();
      if (startGeneration !== generation || state.table !== current || !trigger.isConnected || state.panel) return;
      callback(config, resolveProvider(current.form, config));
      render(current, config);
    } catch (error) {
      if (state.table === current && trigger.isConnected && !state.panel) {
        const rowNode = trigger.closest('.pmt-grid');
        showMessage(current, trigger, `模型配置读取失败。${error.message || '请重试。'}`, rowNode);
      }
    } finally {
      trigger.removeAttribute('aria-busy');
    }
  }

  function render(current, config) {
    const providerId = resolveProvider(current.form, config);
    if (current.providerId !== providerId) {
      current.selecting = false;
      current.feedback = '';
      current.feedbackTone = '';
      for (const row of current.rows.values()) row.select.checked = false;
    }
    current.providerId = providerId;
    captureOpeningModels(current, providerId);
    const managed = providerId.startsWith('managed:');
    setHidden(current.toolbar, !providerId || managed);

    const allGrids = [...current.table.querySelectorAll('.pmt-grid')];
    const head = allGrids.find(row => row.classList.contains('pmt-head'));
    const rows = allGrids.filter(row => !row.classList.contains('pmt-head'));

    current.table.setAttribute('data-ekd-px-table', '1');
    if (head) {
      if (!current.headSel || current.headSel.parentElement !== head) {
        current.headSel?.remove();
        current.headSel = el('span', { class: 'ekd-px-head-sel', 'data-ekd-px-mount': '1', 'aria-hidden': 'true' });
        prependChild(head, current.headSel);
      }
      if (!current.headPad || current.headPad.parentElement !== head) {
        current.headPad?.remove();
        current.headPad = el('span', { class: 'ekd-px-head-pad', 'data-ekd-px-mount': '1', 'aria-hidden': 'true' });
        head.append(current.headPad);
      }
    }

    for (const [node, row] of current.rows) {
      if (!rows.includes(node) || modelInput(node) !== row.input) {
        if (state.panel?.rowNode === node) closePanel(true);
        row.selection.remove();
        row.trigger.remove();
        node.removeAttribute('data-ekd-px-row');
        current.rows.delete(node);
      }
    }

    for (const node of rows) {
      const input = modelInput(node);
      if (!input) continue;
      node.setAttribute('data-ekd-px-row', '1');
      let row = current.rows.get(node);
      if (!row) {
        const select = el('input', {
          type: 'checkbox',
          class: 'ekd-px-sel-input',
          onInput: event => event?.stopPropagation?.(),
          onChange: event => {
            event?.stopPropagation?.();
            updateSelection(current);
          },
        });
        const selection = el('label', { class: 'ekd-px-sel', 'data-ekd-px-mount': '1' }, [select]);
        const labelSpan = el('span', { class: 'ekd-px-btn-label', text: '能力与限制' });
        const trigger = el('button', {
          type: 'button',
          class: 'ekd-px-btn',
          'data-ekd-px-mount': '1',
          'data-ekd-px-button': '1',
          'data-ekd-enhance': '1',
          'aria-controls': PANEL_ID,
          'aria-expanded': 'false',
          onClick: event => {
            event?.preventDefault?.();
            event?.stopPropagation?.();
            const idAtClick = input.value.trim();
            void withFreshConfig(current, trigger, (latest, resolvedProviderId) => {
              if (input.value.trim() !== idAtClick) return;
              const model = savedModel(latest, resolvedProviderId, idAtClick);
              if (!model) {
                showMessage(current, trigger, '请先在官方供应商表单中保存这个模型。', node);
                return;
              }
              openPanel({
                current,
                button: trigger,
                rowNode: node,
                providerId: resolvedProviderId,
                ids: [idAtClick],
                model,
                officialDirty: hasOfficialModelEdits(current, latest, resolvedProviderId),
              });
            });
          },
        }, [
          icon(SLIDERS_ICON, 12),
          labelSpan,
          el('span', { class: 'ekd-px-caret', 'aria-hidden': 'true' }, [icon(CHEVRON_DOWN_ICON, 12)]),
        ]);
        prependChild(node, selection);
        const removeBtn = node.querySelector('button[aria-label="移除模型"], button[aria-label="Remove model"]')
          || [...node.children].filter(child => child.tagName === 'BUTTON' && child.getAttribute('data-ekd-px-mount') === null).at(-1);
        if (removeBtn && typeof removeBtn.after === 'function') removeBtn.after(trigger);
        else node.append(trigger);
        row = { input, selection, select, trigger, labelSpan };
        current.rows.set(node, row);
      }
      const id = input.value.trim();
      if (row.modelId !== id) row.select.checked = false;
      row.modelId = id;
      row.model = savedModel(config, providerId, id);
      if (!row.model) row.select.checked = false;
      const text = summary(row.model, managed);
      setText(row.labelSpan, managed ? '只读' : '能力与限制');
      row.trigger.setAttribute('data-ekd-px-summary', text);
      row.trigger.setAttribute('title', managed ? '内置订阅模型只读，点击查看能力与限制' : `${text} — 点击展开设置模型能力、思考强度与上下文限制`);
      row.trigger.setAttribute('aria-label', `${id || '新模型'}的能力与限制，${text}`);
      row.select.setAttribute('aria-label', `选择模型 ${id}`);
    }
    updateSelection(current);
  }

  function nativeInput(event) {
    const current = state.table;
    if (!current || event.target.tagName !== 'INPUT' || !snapshot) return;
    const row = event.target.closest('.pmt-grid');
    if (current.rows.get(row)?.input === event.target) render(current, snapshot);
  }

  function showDrawer(current, trigger, { rowNode, batch = false, title, identity = '', extraHead = null }) {
    const status = el('span', { class: 'ekd-px-status', role: 'status' });
    const discard = el('button', { type: 'button', class: 'ekd-px-quiet', text: '放弃修改', onClick: () => closePanel(true) });
    discard.hidden = true;
    const close = el('button', { type: 'button', class: 'ekd-px-quiet', text: '收起', onClick: () => closePanel() });
    const actions = el('div', { class: 'ekd-px-actions' }, [discard, close]);
    const body = el('div', { class: 'ekd-px-body' });
    const headChildren = [
      el('div', { class: 'ekd-px-drawer-title' }, [
        el('span', { class: 'ekd-px-drawer-name', text: title }),
        ...(identity ? [el('span', { class: 'ekd-px-identity', text: identity })] : []),
      ]),
    ];
    if (extraHead) headChildren.push(extraHead);
    const node = el('div', {
      id: PANEL_ID,
      class: 'ekd-px-drawer',
      role: 'region',
      'aria-label': title,
      'data-ekd-px-mount': '1',
      'data-ekd-provider-enhance': '1',
      ...(batch ? { 'data-ekd-px-batch': '1' } : {}),
      draggable: 'false',
      onInput: event => event?.stopPropagation?.(),
      onChange: event => event?.stopPropagation?.(),
      onDragstart: event => {
        event?.preventDefault?.();
        event?.stopPropagation?.();
      },
    }, [
      el('div', { class: 'ekd-px-drawer-head' }, headChildren),
      body,
      el('div', { class: 'ekd-px-foot' }, [status, actions]),
    ]);
    const panel = { node, current, trigger, rowNode, batch, status, discard, close, actions, body, dirty: () => false, saving: false };
    state.panel = panel;
    if (rowNode) rowNode.append(node);
    else current.toolbar.after(node);
    trigger.setAttribute('aria-expanded', 'true');
    return panel;
  }

  function showMessage(current, trigger, message, rowNode = null) {
    const panel = showDrawer(current, trigger, { rowNode, batch: !rowNode, title: '能力与限制' });
    panel.body.append(el('div', { class: 'ekd-px-readonly', text: message }));
    statusOf(panel, '保存供应商后即可直接在此行展开编辑。');
    panel.close.focus();
  }

  function statusOf(panel, text, tone = '') {
    setText(panel.status, text);
    panel.status.dataset.tone = tone === true ? 'error' : (tone || '');
  }

  function closePanel(force = false) {
    const panel = state.panel;
    if (!panel || (panel.saving && !force)) return;
    if (!force && panel.dirty()) {
      statusOf(panel, '有未保存的修改。可继续编辑，或放弃修改后关闭。');
      panel.discard.hidden = false;
      panel.discard.focus();
      return;
    }
    state.panel = null;
    panel.trigger.setAttribute('aria-expanded', 'false');
    panel.node.remove();
    if (panel.trigger.isConnected) panel.trigger.focus();
  }

  function onKeyDown(event) {
    const panel = state.panel;
    if (!panel) return;
    if (event.key === 'Escape') {
      event.preventDefault();
      event.stopImmediatePropagation();
      closePanel();
    }
  }

  async function loadSuggestion(providerId, id) {
    const key = `${providerId}/${id}`;
    const [official, publicCatalog] = await Promise.allSettled([
      request('models.list'), request('providers.modelSuggestion', { providerId, model: key }),
    ]);
    const items = official.value?.items;
    const exact = Array.isArray(items)
      ? items.find(item => item.provider === providerId && (item.id === key || item.model === id))
      : null;
    const metadata = publicCatalog.value?.suggestion;
    const fields = modelFields(exact ?? {});
    const known = new Map();
    for (const [capability] of CAPABILITIES) {
      if (typeof metadata?.[capability] === 'boolean') known.set(capability, metadata[capability]);
      else if (fields.capabilities.has(capability)) known.set(capability, true);
    }
    const thinking = typeof metadata?.thinking === 'boolean'
      ? (metadata.thinking ? (fields.thinking === 'always' ? 'always' : 'toggle') : 'none')
      : (fields.thinking !== 'none' ? fields.thinking : null);
    const context = Number.isSafeInteger(metadata?.max_context_size) && metadata.max_context_size > 0 ? metadata.max_context_size : null;
    const output = Number.isSafeInteger(metadata?.max_output_size) && metadata.max_output_size > 0 ? metadata.max_output_size : null;
    const fromOfficial = fields.thinking !== 'none' || CAPABILITIES.some(([capability]) => fields.capabilities.has(capability));
    const source = [fromOfficial && '官方模型目录', metadata && 'models.dev'].filter(Boolean).join('、');
    if (!known.size && !thinking && !context && !output) {
      const error = official.status === 'rejected' || publicCatalog.status === 'rejected';
      return { empty: error ? '部分目录暂时无法读取，也没有可用的建议。可以重试。' : '目录没有此模型的可用建议。' };
    }
    return { known, thinking, context, output, adaptive: fields.adaptive, efforts: fields.supportEfforts, source };
  }

  function openPanel({ current, button: trigger, rowNode, providerId, ids, model, officialDirty }) {
    const batch = !model;
    const managed = providerId.startsWith('managed:');
    const initial = modelFields(model);

    const draft = {
      caps: new Set(CAPABILITIES.filter(([key]) => initial.capabilities.has(key)).map(([key]) => key)),
      thinking: initial.thinking,
      adaptive: initial.adaptive,
      efforts: new Set(initial.supportEfforts),
    };

    const custom = [...initial.capabilities].filter(key => !CAPABILITIES.some(([value]) => value === key));
    const effortOrder = [...new Set([...EFFORTS, ...initial.supportEfforts])];

    let suggestBar = null;
    let suggesting = false;
    let suggestBtn = null;
    let suggestNote = null;
    if (!batch && !managed) {
      suggestNote = el('span', { class: 'ekd-px-meta', role: 'status' });
      suggestBtn = el('button', {
        type: 'button',
        class: 'ekd-px-btn ekd-px-btn-compact',
        text: '填入目录建议',
        onClick: async () => {
          if (suggesting || panel.saving) return;
          suggesting = true;
          const draftAtRequest = JSON.stringify(readFields());
          setText(suggestNote, '正在查询匹配的模型…');
          try {
            const found = await loadSuggestion(providerId, ids[0]);
            if (state.panel !== panel) return;
            if (panel.saving || JSON.stringify(readFields()) !== draftAtRequest) {
              setText(suggestNote, '草稿已更改。再次点击可填入目录建议。');
              return;
            }
            if (found.empty) {
              setText(suggestNote, found.empty);
              return;
            }
            for (const [key] of CAPABILITIES) {
              if (found.known.has(key)) {
                if (found.known.get(key)) draft.caps.add(key);
                else draft.caps.delete(key);
              }
            }
            if (found.thinking) {
              draft.thinking = found.thinking;
              if (found.adaptive) draft.adaptive = true;
              if (found.efforts.length) {
                draft.efforts = new Set(found.efforts);
              }
            }
            if (found.context !== null) context.value = formatSize(found.context);
            const draftContext = parseSize(context.value).value ?? initial.context;
            const oversized = found.output !== null && found.output > draftContext;
            if (found.output !== null && !oversized) output.value = formatSize(found.output);
            setText(suggestNote, `${found.source}建议已填入，确认后保存。${oversized ? '目录输出长度超过当前上下文，已保留原值。' : ''}`);
            sync();
          } catch {
            if (state.panel === panel) setText(suggestNote, '目录暂时无法读取，可以重试。');
          } finally {
            suggesting = false;
          }
        },
      });
      suggestBar = el('div', { class: 'ekd-px-suggest-bar' }, [suggestNote, suggestBtn]);
    }

    const panel = showDrawer(current, trigger, {
      rowNode,
      batch,
      title: batch ? '批量能力模板' : '能力与限制',
      identity: batch ? `${providerId} · ${ids.length} 个模型` : `${providerId} / ${ids[0]}`,
      extraHead: suggestBar,
    });

    const capButtons = new Map();
    const capabilityPills = el('div', { class: 'ekd-px-pills', role: 'group', 'aria-label': '输入与调用能力' });
    for (const [key, label] of CAPABILITIES) {
      const btn = el('button', {
        type: 'button',
        class: 'ekd-px-pill',
        'data-kind': 'capability',
        'data-capability': key,
        'aria-pressed': draft.caps.has(key) ? 'true' : 'false',
        onClick: () => {
          if (draft.caps.has(key)) draft.caps.delete(key);
          else draft.caps.add(key);
          sync();
        },
      }, [
        el('span', { class: 'ekd-px-dot', 'aria-hidden': 'true' }),
        label,
      ]);
      capButtons.set(key, btn);
      capabilityPills.append(btn);
    }

    const modeButtons = new Map();
    const modePills = el('div', { class: 'ekd-px-pills', role: 'group', 'aria-label': '思考模式' });
    for (const [mode, label, title] of MODES) {
      const btn = el('button', {
        type: 'button',
        class: 'ekd-px-pill',
        'data-kind': 'mode',
        'data-mode': mode,
        'aria-pressed': draft.thinking === mode ? 'true' : 'false',
        title,
        text: label,
        onClick: () => {
          draft.thinking = mode;
          sync();
        },
      });
      modeButtons.set(mode, btn);
      modePills.append(btn);
    }

    const adaptiveBtn = el('button', {
      type: 'button',
      class: 'ekd-px-pill',
      'data-kind': 'adaptive',
      'aria-pressed': draft.adaptive ? 'true' : 'false',
      title: '仅在「可切换」思考模式下可配置自适应思考',
      onClick: () => {
        draft.adaptive = !draft.adaptive;
        sync();
      },
    }, [
      el('span', { class: 'ekd-px-dot', 'aria-hidden': 'true' }),
      '自适应思考',
    ]);
    modePills.append(el('span', { class: 'ekd-px-divider', 'aria-hidden': 'true' }), adaptiveBtn);

    const effortButtons = new Map();
    const effortPills = el('div', { class: 'ekd-px-pills', role: 'group', 'aria-label': '支持的思考强度' });
    for (const value of effortOrder) {
      const btn = el('button', {
        type: 'button',
        class: 'ekd-px-pill',
        'data-kind': 'effort',
        'data-effort': value,
        'aria-pressed': draft.efforts.has(value) ? 'true' : 'false',
        text: value,
        onClick: () => {
          if (draft.efforts.has(value)) draft.efforts.delete(value);
          else draft.efforts.add(value);
          sync();
        },
      });
      effortButtons.set(value, btn);
      effortPills.append(btn);
    }

    const context = el('input', {
      class: 'ekd-px-input',
      type: 'text',
      value: formatSize(initial.context),
      placeholder: '如 128K、256K、1M',
      autocomplete: 'off',
      spellcheck: 'false',
      'aria-label': '上下文长度',
      onInput: () => sync(),
    });
    const contextPresetsRow = el('div', { class: 'ekd-px-presets', role: 'group', 'aria-label': '上下文快捷档位' });
    const contextPresetBtns = [];
    for (const preset of CONTEXT_PRESETS) {
      const btn = el('button', {
        type: 'button',
        class: 'ekd-px-preset',
        'aria-pressed': 'false',
        text: preset.label,
        onClick: () => {
          context.value = preset.label;
          sync();
        },
      });
      contextPresetBtns.push({ value: preset.value, btn });
      contextPresetsRow.append(btn);
    }
    const contextMeta = el('p', { class: 'ekd-px-meta' });
    const contextIssue = el('p', { class: 'ekd-px-issue', role: 'status' });

    const output = el('input', {
      class: 'ekd-px-input',
      type: 'text',
      value: formatSize(initial.output),
      placeholder: '如 16K、32K，留空使用默认值',
      autocomplete: 'off',
      spellcheck: 'false',
      'aria-label': '最大输出长度',
      onInput: () => sync(),
    });
    const outputPresetsRow = el('div', { class: 'ekd-px-presets', role: 'group', 'aria-label': '最大输出快捷档位' });
    const outputPresetBtns = [];
    for (const preset of OUTPUT_PRESETS) {
      const btn = el('button', {
        type: 'button',
        class: 'ekd-px-preset',
        'aria-pressed': 'false',
        text: preset.label,
        onClick: () => {
          output.value = preset.label;
          sync();
        },
      });
      outputPresetBtns.push({ value: preset.value, btn });
      outputPresetsRow.append(btn);
    }
    const clearOutputBtn = el('button', {
      type: 'button',
      class: 'ekd-px-preset',
      'aria-pressed': 'false',
      text: '不限制',
      onClick: () => {
        output.value = '';
        sync();
      },
    });
    outputPresetsRow.append(clearOutputBtn);
    const outputMeta = el('p', { class: 'ekd-px-meta' });
    const outputIssue = el('p', { class: 'ekd-px-issue', role: 'status' });

    const readFields = () => ({
      caps: CAPABILITIES.filter(([key]) => draft.caps.has(key)).map(([key]) => key),
      thinking: draft.thinking,
      adaptive: draft.adaptive,
      efforts: effortOrder.filter(value => draft.efforts.has(value)),
      context: context.value.trim(),
      output: output.value.trim(),
    });
    let baseline = readFields();
    const save = el('button', {
      type: 'button',
      class: 'ekd-px-primary',
      text: batch ? `保存到 ${ids.length} 个模型` : '保存',
      onClick: () => void saveDraft(),
    });
    panel.dirty = () => !managed && JSON.stringify(readFields()) !== JSON.stringify(baseline);

    function patch() {
      const next = readFields();
      const edit = {};
      if (batch || JSON.stringify(baseline.caps) !== JSON.stringify(next.caps)) edit.capabilities = next.caps;
      if (batch || baseline.thinking !== next.thinking) edit.thinking = next.thinking;
      if (next.thinking === 'toggle' && (batch || baseline.adaptive !== next.adaptive)) edit.adaptive_thinking = next.adaptive;
      if (next.thinking !== 'none' && (batch || JSON.stringify(baseline.efforts) !== JSON.stringify(next.efforts))) edit.support_efforts = next.efforts;
      if (!batch) {
        const storedContext = parseSize(baseline.context).value ?? initial.context;
        const storedOutput = parseSize(baseline.output).value;
        const nextContext = next.context !== baseline.context ? parseSize(next.context) : { value: storedContext, error: '' };
        const nextOutput = next.output !== baseline.output ? parseSize(next.output) : { value: storedOutput, error: '' };
        if (next.context !== baseline.context && (nextContext.error || nextContext.value === null)) {
          return { contextMessage: nextContext.error || '上下文长度需要正整数。' };
        }
        if (nextOutput.error) return { outputMessage: nextOutput.error };
        if (nextContext.value !== storedContext || nextOutput.value !== storedOutput) {
          if (nextOutput.value !== null && nextOutput.value > nextContext.value) {
            return { outputMessage: '最大输出长度不能超过上下文长度。' };
          }
          if (nextContext.value !== storedContext) edit.max_context_size = nextContext.value;
          if (nextOutput.value !== storedOutput) edit.max_output_size = nextOutput.value;
        }
      }
      return { edit };
    }

    function sync(showDirty = true) {
      const locked = managed || panel.saving;
      for (const [key, btn] of capButtons) {
        btn.setAttribute('aria-pressed', draft.caps.has(key) ? 'true' : 'false');
        btn.disabled = locked;
      }
      for (const [mode, btn] of modeButtons) {
        btn.setAttribute('aria-pressed', draft.thinking === mode ? 'true' : 'false');
        btn.disabled = locked;
      }
      adaptiveBtn.setAttribute('aria-pressed', draft.thinking === 'toggle' && draft.adaptive ? 'true' : 'false');
      adaptiveBtn.disabled = locked || draft.thinking !== 'toggle';
      for (const [value, btn] of effortButtons) {
        btn.setAttribute('aria-pressed', draft.efforts.has(value) ? 'true' : 'false');
        btn.disabled = locked || draft.thinking === 'none';
      }
      context.disabled = locked;
      output.disabled = locked;
      for (const item of contextPresetBtns) item.btn.disabled = locked;
      for (const item of outputPresetBtns) item.btn.disabled = locked;
      clearOutputBtn.disabled = locked;
      if (suggestBtn) suggestBtn.disabled = locked;
      panel.close.disabled = panel.saving;
      panel.discard.disabled = panel.saving;

      const { edit, contextMessage, outputMessage } = patch();
      context.setAttribute('aria-invalid', contextMessage ? 'true' : 'false');
      output.setAttribute('aria-invalid', outputMessage ? 'true' : 'false');
      setText(contextIssue, contextMessage || '');
      setText(outputIssue, outputMessage || '');

      const parsedContext = parseSize(context.value);
      for (const item of contextPresetBtns) {
        item.btn.setAttribute('aria-pressed', !parsedContext.error && parsedContext.value === item.value ? 'true' : 'false');
      }
      setText(contextMeta, contextMessage || parsedContext.error || parsedContext.value === null
        ? ''
        : `${parsedContext.value.toLocaleString('zh-CN')} tokens · 支持整数、K、M`);

      const parsedOutput = parseSize(output.value);
      for (const item of outputPresetBtns) {
        item.btn.setAttribute('aria-pressed', !parsedOutput.error && parsedOutput.value === item.value ? 'true' : 'false');
      }
      clearOutputBtn.setAttribute('aria-pressed', !parsedOutput.error && parsedOutput.value === null ? 'true' : 'false');
      setText(outputMeta, outputMessage || parsedOutput.error
        ? ''
        : (parsedOutput.value === null
          ? '留空使用模型默认输出上限'
          : `${parsedOutput.value.toLocaleString('zh-CN')} tokens · 留空或点「不限制」恢复默认`));

      save.disabled = locked || !edit || !Object.keys(edit).length;
      if (showDirty && !panel.saving) {
        panel.discard.hidden = true;
        if (managed) {
          statusOf(panel, '内置订阅模型只读');
        } else if (panel.dirty() && !batch) {
          statusOf(panel, '有未保存的修改。');
        } else {
          statusOf(panel, batch ? '确认模板后保存到所选模型。' : '保存后立即生效，并与官方表单保持同步。');
        }
      }
    }

    async function saveDraft() {
      const { edit } = patch();
      if (managed || panel.saving || !edit || !Object.keys(edit).length) return;
      panel.saving = true;
      sync(false);
      statusOf(panel, '正在保存…');
      try {
        if (batch) {
          await request('providers.updateModels', { providerId, models: ids.map(id => ({ model: `${providerId}/${id}`, edit })) });
        } else {
          await request('providers.updateModel', { providerId, model: `${providerId}/${ids[0]}`, edit });
        }
        if (!batch && edit.max_context_size !== undefined) {
          syncOfficialContextInput(current, ids[0], edit.max_context_size);
        }
        invalidate();
        window.dispatchEvent(new CustomEvent('ekd:config-saved'));
        if (state.panel !== panel) return;
        if (batch) {
          current.selecting = false;
          current.feedback = `已保存 ${ids.length} 个模型。后续点击官方保存时会自动保留这些配置。`;
          current.feedbackTone = 'ok';
          for (const row of current.rows.values()) row.select.checked = false;
          updateSelection(current);
          const templateTrigger = panel.trigger;
          panel.trigger = current.toggle;
          templateTrigger.setAttribute('aria-expanded', 'false');
          closePanel(true);
          return;
        }
        baseline = readFields();
        panel.discard.hidden = true;
        statusOf(panel, '已保存并核对。上下文与增强配置会自动与官方表单保持同步。', 'ok');
      } catch (error) {
        if (state.panel === panel) statusOf(panel, `保存未确认。${error.message || '请重试。'}`, 'error');
      } finally {
        panel.saving = false;
        if (state.panel === panel) {
          sync(false);
          if (save.disabled && document.activeElement === document.body) panel.close.focus();
        }
      }
    }

    if (managed) {
      panel.body.append(el('div', { class: 'ekd-px-readonly', text: '内置订阅供应商由官方托管，模型能力与限制为只读。' }));
    }
    if (!managed && officialDirty) {
      panel.body.append(el('p', { class: 'ekd-px-hint', text: '官方表单有未保存的模型改动。在此保存后会同步上下文，并在官方保存后自动保留增强配置。' }));
    }
    if (batch) {
      panel.body.append(
        el('p', { class: 'ekd-px-hint', text: '同一模板会替换所选模型的输入、工具与思考配置，保留各自上下文与最大输出。' }),
        el('p', { class: 'ekd-px-identity', text: ids.join('、') }),
      );
    }

    const capSectionChildren = [
      el('span', { class: 'ekd-px-title', text: '输入与调用能力' }),
      capabilityPills,
    ];
    if (custom.length) {
      capSectionChildren.push(el('p', { class: 'ekd-px-meta', text: `已保留的自定义能力 · ${custom.join('、')}` }));
    }

    panel.body.append(
      el('div', { class: 'ekd-px-top-grid' }, [
        el('div', { class: 'ekd-px-section' }, capSectionChildren),
        el('div', { class: 'ekd-px-section' }, [
          el('span', { class: 'ekd-px-title', text: '思考模式' }),
          modePills,
          el('span', { class: 'ekd-px-title', text: '支持的思考强度' }),
          effortPills,
        ]),
      ]),
    );

    if (!batch) {
      panel.body.append(
        el('div', { class: 'ekd-px-limits' }, [
          el('div', { class: 'ekd-px-section' }, [
            el('span', { class: 'ekd-px-title', text: '上下文长度' }),
            el('div', { class: 'ekd-px-input-wrap' }, [context]),
            contextPresetsRow,
            contextIssue,
            contextMeta,
          ]),
          el('div', { class: 'ekd-px-section' }, [
            el('span', { class: 'ekd-px-title', text: '最大输出长度' }),
            el('div', { class: 'ekd-px-input-wrap' }, [output]),
            outputPresetsRow,
            outputIssue,
            outputMeta,
          ]),
        ]),
      );
    }

    if (!managed) panel.actions.append(save);
    sync(true);
    panel.close.focus();
  }

  async function refresh() {
    if (!enabled || removed) return;
    const form = findForm();
    const table = form?.querySelector('.pmt');
    if (state.table?.table !== table || state.table?.form !== form || (state.table && !isEditForm(form))) {
      const matchedPending = pendingFormOpen && pendingFormOpen.form === form && pendingFormOpen.table === table
        ? pendingFormOpen
        : null;
      const snapshotAtOpen = hasModelsRecord(snapshot) ? snapshot : null;
      clearDecorations();
      if (!table || !isEditForm(form)) return;
      const openingConfig = matchedPending
        ? matchedPending.openingConfig
        : cloneOpeningConfig(readOfficialConfigSnapshot() || snapshotAtOpen);
      const openingOrigin = matchedPending
        ? matchedPending.openingOrigin
        : (resolveOfficialBase()?.origin || '');
      state.table = {
        form,
        table,
        rows: new Map(),
        providerId: '',
        selecting: false,
        backfilling: false,
        feedback: '',
        feedbackTone: '',
        headSel: null,
        headPad: null,
        openingCaptured: new Set(),
        openingConfig,
        openingOrigin,
      };
      decorateToolbar(state.table);
      setHidden(state.table.toolbar, true);
      if (openingConfig) {
        const openProviderId = resolveProvider(form, openingConfig);
        if (openProviderId) captureOpeningModels(state.table, openProviderId);
      }
    }
    const current = state.table;
    if (!current) return;
    const startGeneration = generation;
    try {
      const hasNewRows = [...current.table.querySelectorAll('.pmt-grid')].some(row => !row.classList.contains('pmt-head') && modelInput(row) && !current.rows.has(row));
      const config = snapshot && !hasNewRows ? snapshot : await readConfig();
      clearRetry();
      if (startGeneration === generation && state.table === current && findForm() === form) render(current, config);
    } catch {
      if (enabled && !removed && startGeneration === generation && state.table === current && findForm() === form && retryCount < MAX_CONFIG_RETRIES && retryTimer === undefined) {
        retryCount += 1;
        retryTimer = setTimeout(() => {
          retryTimer = undefined;
          void refresh();
        }, CONFIG_RETRY_BASE_MS * retryCount);
        retryTimer?.unref?.();
      }
    }
  }

  function scan() {
    if (!enabled || removed) return;
    capturePendingFormOpen();
    if (timer !== undefined) return;
    timer = setTimeout(() => {
      timer = undefined;
      void refresh();
    }, 0);
  }

  function isOwnNode(node) {
    return Boolean(node && node.nodeType === 1 && typeof node.matches === 'function'
      && node.matches('[data-ekd-px-mount], [data-ekd-provider-enhance-style]'));
  }

  function onMutations(records) {
    if (Array.isArray(records) && records.length > 0) {
      const relevant = records.some(record => {
        const target = record?.target;
        if (!target || typeof target.closest !== 'function') return true;
        if (target.closest('[data-ekd-px-mount], [data-ekd-provider-enhance-style]')) return false;
        if (record.type === 'childList') {
          const changed = [...(record.addedNodes || []), ...(record.removedNodes || [])];
          if (changed.length > 0 && changed.every(isOwnNode)) return false;
        }
        if (record.type === 'attributes') {
          return Boolean(target.closest('.settings-dialog, .ui-dialog, [role="dialog"]'));
        }
        return true;
      });
      if (!relevant) return;
    }
    scan();
  }

  function clearDecorations() {
    clearRetry();
    closePanel(true);
    if (state.table?.table) {
      state.table.table.removeAttribute('data-ekd-px-table');
      state.table.table.removeAttribute('data-ekd-px-selecting');
      for (const row of state.table.rows.keys()) row.removeAttribute('data-ekd-px-row');
    }
    state.table = null;
    pendingFormOpen = null;
    snapshot = null;
    generation += 1;
    pending = null;
    openingModels.clear();
    for (const node of document.querySelectorAll('[data-ekd-px-mount]')) node.remove();
  }

  function observe() {
    observer.observe(document.body, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeFilter: ['style', 'hidden'],
    });
  }

  function setEnabled(next) {
    if (removed || enabled === Boolean(next)) return;
    enabled = Boolean(next);
    if (enabled) {
      if (observer) {
        observe();
        attachFetch();
      }
      scan();
    } else {
      observer?.disconnect();
      clearTimeout(timer);
      timer = undefined;
      clearRetry();
      detachFetch();
      openingModels.clear();
      recentCatalogImports.clear();
      clearDecorations();
    }
  }

  function install(installOpts) {
    if (installOpts?.excludeOrigins !== undefined) {
      installExcludeOrigins = installOpts.excludeOrigins;
    }
    if (installOpts?.readOfficialConfig !== undefined) {
      installReadOfficialConfig = installOpts.readOfficialConfig;
    }
    ensureStyle();
    observer = new MutationObserver(onMutations);
    window.addEventListener('ekd:config-saved', invalidate);
    window.addEventListener('ekd:official-config-changed', invalidate);
    window.addEventListener('keydown', onKeyDown, true);
    document.addEventListener('input', nativeInput);
    if (enabled) {
      observe();
      attachFetch();
    }
    scan();
  }

  function remove() {
    setEnabled(false);
    detachFetch();
    openingModels.clear();
    recentCatalogImports.clear();
    pendingFormOpen = null;
    installExcludeOrigins = null;
    installReadOfficialConfig = null;
    removed = true;
    observer = null;
    window.removeEventListener('ekd:config-saved', invalidate);
    window.removeEventListener('ekd:official-config-changed', invalidate);
    window.removeEventListener('keydown', onKeyDown, true);
    document.removeEventListener('input', nativeInput);
    state.style?.remove();
  }

  return { install, remove, clearDecorations, setEnabled, scan };
}
