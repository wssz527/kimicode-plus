// Adds inline expandable model capability & limit controls to the official
// provider form: capabilities, direct-click thinking efforts (no / low / medium /
// high / xhigh / max), and single-row context/output limits with K/M support.
//
// Loaded by renderer.js as an independent unit (`provider_enhance.js`) and
// instantiated with the native transport. It owns no globals, installs one
// observer, and every write goes through `providers.updateModel`, so an api_key
// never reaches this script.
function createProviderEnhance(options) {
  const request = options.request;
  const state = {
    observer: null,
    removed: false,
    panel: null,
    panelRow: null,
    panelButton: null,
    style: null,
    props: null,
  };

  const CAPABILITIES = [
    { key: 'image_in', label: '图片输入' },
    { key: 'video_in', label: '视频输入' },
    { key: 'tool_use', label: '工具调用' },
  ];

  const THINKING_OPTIONS = [
    { value: 'no', label: 'no', title: '关闭思考' },
    { value: 'low', label: 'low', title: '低推理强度 (low)' },
    { value: 'medium', label: 'medium', title: '中等推理强度 (medium)' },
    { value: 'high', label: 'high', title: '高推理强度 (high)' },
    { value: 'xhigh', label: 'xhigh', title: '超高推理强度 (xhigh)' },
    { value: 'max', label: 'max', title: '最大推理强度 (max)' },
  ];

  const EFFORT_ORDER = ['low', 'medium', 'high', 'xhigh', 'max'];

  const CONTEXT_PRESETS = [
    { label: '256K', value: 262144 },
    { label: '350K', value: 358400 },
    { label: '500K', value: 512000 },
    { label: '600K', value: 614400 },
    { label: '1M', value: 1048576 },
  ];

  // ---------------------------------------------------------------- rendering

  function el(tag, props, children) {
    const node = document.createElement(tag);
    for (const [key, value] of Object.entries(props || {})) {
      if (value === undefined || value === null) continue;
      if (key === 'class') node.className = value;
      else if (key === 'text') node.textContent = String(value);
      else if (key === 'dataset') Object.assign(node.dataset, value);
      else if (key.startsWith('on') && typeof value === 'function') node.addEventListener(key.slice(2).toLowerCase(), value);
      else node.setAttribute(key, String(value));
    }
    for (const child of children || []) {
      if (child === null || child === undefined) continue;
      node.append(typeof child === 'string' ? document.createTextNode(child) : child);
    }
    return node;
  }

  function icon(paths, size) {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('width', String(size || 14));
    svg.setAttribute('height', String(size || 14));
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

  const SLIDERS = [
    'M3.7 5.1h6.4a1 1 0 0 1 0 2H3.7a1 1 0 0 1 0-2Z',
    'M15.5 4.2a3.7 3.7 0 1 0 0 7.4 3.7 3.7 0 0 0 0-7.4Zm0 2a1.7 1.7 0 1 1 0 3.4 1.7 1.7 0 0 1 0-3.4Z',
    'M13.9 15.1h6.4a1 1 0 0 1 0 2h-6.4a1 1 0 0 1 0-2Z',
    'M8.5 12.4a3.7 3.7 0 1 0 0 7.4 3.7 3.7 0 0 0 0-7.4Zm0 2a1.7 1.7 0 1 1 0 3.4 1.7 1.7 0 0 1 0-3.4Z',
  ];

  const CHEVRON_DOWN = ['M6.7 9.3a1 1 0 0 1 1.4 0L12 13.2l3.9-3.9a1 1 0 1 1 1.4 1.4l-4.6 4.6a1 1 0 0 1-1.4 0l-4.6-4.6a1 1 0 0 1 0-1.4Z'];

  // ------------------------------------------------------------------- config

  function modelPart(key) {
    const index = String(key).indexOf('/');
    return index === -1 ? String(key) : String(key).slice(index + 1);
  }

  function providerPart(key) {
    const index = String(key).indexOf('/');
    return index === -1 ? '' : String(key).slice(0, index);
  }

  function normalizeEfforts(list) {
    const raw = Array.isArray(list) ? list : [];
    const seen = new Set();
    const valid = [];
    for (const item of raw) {
      const trimmed = typeof item === 'string' ? item.trim() : '';
      if (!trimmed || trimmed.toLowerCase() === 'no' || seen.has(trimmed)) continue;
      seen.add(trimmed);
      valid.push(trimmed);
    }
    const preset = EFFORT_ORDER.filter(effort => seen.has(effort));
    const custom = valid.filter(effort => !EFFORT_ORDER.includes(effort));
    return [...preset, ...custom];
  }

  function thinkingMode(model) {
    const capabilities = Array.isArray(model?.capabilities) ? model.capabilities : [];
    if (capabilities.includes('always_thinking')) return 'always';
    const efforts = normalizeEfforts(
      Array.isArray(model?.supportEfforts)
        ? model.supportEfforts
        : (Array.isArray(model?.support_efforts) ? model.support_efforts : []),
    );
    if (
      capabilities.includes('thinking') ||
      model?.adaptiveThinking === true ||
      model?.adaptive_thinking === true ||
      efforts.length > 0
    ) {
      return 'toggle';
    }
    return 'none';
  }

  function readThinkingState(model) {
    const thinking = thinkingMode(model);
    const adaptive = model?.adaptiveThinking === true || model?.adaptive_thinking === true;
    return {
      thinking,
      adaptiveThinking: thinking === 'toggle' && adaptive,
    };
  }

  function applyThinkingState(baseCapabilities, thinking, adaptiveThinking = false) {
    const input = new Set(Array.isArray(baseCapabilities) ? baseCapabilities : []);
    const capabilities = CAPABILITIES.map(item => item.key).filter(key => input.has(key));
    const mode = thinking === 'always' || thinking === 'toggle' ? thinking : 'none';
    if (mode === 'toggle') capabilities.push('thinking');
    else if (mode === 'always') capabilities.push('always_thinking');
    return {
      capabilities,
      thinking: mode,
      adaptive_thinking: mode === 'toggle' && Boolean(adaptiveThinking),
    };
  }

  function selectThinkingOption(draft, option) {
    if (option === 'no') {
      draft.thinking = 'none';
      draft.adaptiveThinking = false;
      draft.supportEfforts = [];
      return;
    }
    const current = new Set(normalizeEfforts(draft.supportEfforts));
    if (current.has(option)) current.delete(option);
    else current.add(option);
    draft.supportEfforts = normalizeEfforts([...current]);
    if (draft.supportEfforts.length === 0) {
      draft.thinking = 'none';
      draft.adaptiveThinking = false;
    } else {
      if (draft.thinking === 'none') draft.thinking = 'toggle';
      const preserved = typeof draft.preservedAdaptiveThinking === 'boolean'
        ? draft.preservedAdaptiveThinking
        : draft.adaptiveThinking === true;
      draft.adaptiveThinking = draft.thinking === 'toggle' && preserved;
    }
  }

  /** Turns one stored model into the editable draft the inline panel starts from. */
  function draftOf(model) {
    const capabilities = Array.isArray(model?.capabilities) ? model.capabilities : [];
    const thinkingState = readThinkingState(model);
    const rawEfforts = Array.isArray(model?.supportEfforts)
      ? model.supportEfforts
      : (Array.isArray(model?.support_efforts) ? model.support_efforts : []);
    const preservedAdaptiveThinking = typeof model?.adaptiveThinking === 'boolean'
      ? model.adaptiveThinking
      : (typeof model?.adaptive_thinking === 'boolean' ? model.adaptive_thinking : undefined);
    return {
      capabilities: new Set(CAPABILITIES.map(item => item.key).filter(key => capabilities.includes(key))),
      thinking: thinkingState.thinking,
      adaptiveThinking: thinkingState.adaptiveThinking,
      preservedAdaptiveThinking,
      maxContextSize: typeof model?.maxContextSize === 'number'
        ? model.maxContextSize
        : (typeof model?.max_context_size === 'number' ? model.max_context_size : 0),
      maxOutputSize: typeof model?.maxOutputSize === 'number'
        ? model.maxOutputSize
        : (typeof model?.max_output_size === 'number' ? model.max_output_size : null),
      supportEfforts: normalizeEfforts(rawEfforts),
    };
  }

  async function readConfig() {
    const config = await request('config.get');
    return config && typeof config === 'object' ? config : {};
  }

  function rowInputs(row) {
    return [...row.children].filter(child => child && child.tagName === 'INPUT');
  }

  function resolveProvider(form, config) {
    const providers = config.providers || {};
    const models = config.models || {};
    const type = form.querySelector('.pf-field .ui-select__value-text')?.textContent.trim().toLowerCase() || '';
    const inputs = [...form.querySelectorAll('.pf-field input.ui-input')];
    const baseUrl = inputs.length > 1 ? inputs[1].value.trim() : '';
    const ids = Object.keys(providers);
    if (baseUrl) {
      const byBase = ids.filter(id => {
        const entry = providers[id] || {};
        const storedBase = String(entry.base_url || entry.baseUrl || '').trim();
        return storedBase === baseUrl && (!type || String(entry.type || '').toLowerCase() === type);
      });
      if (byBase.length === 1) return byBase[0];
    }
    const rows = [...form.querySelectorAll('.pmt .pmt-grid')].filter(row => !row.classList.contains('pmt-head'));
    const owners = new Set();
    for (const row of rows) {
      const firstInput = rowInputs(row)[0] || row.querySelector('input');
      const modelId = firstInput ? firstInput.value.trim() : '';
      if (!modelId) continue;
      for (const key of Object.keys(models)) {
        if (modelPart(key) === modelId) owners.add(providerPart(key));
      }
    }
    owners.delete('');
    return owners.size === 1 ? [...owners][0] : '';
  }

  // ---------------------------------------------------------------------- DOM

  function styleText() {
    return `
      .settings-dialog .pf .pmt[data-ekd-px-table="1"] .pmt-grid.pmt-head,
      .settings-dialog .pf .pmt[data-ekd-px-table="1"] .pmt-grid[data-ekd-px-row="1"]{
        grid-template-columns:minmax(0,1.35fr) minmax(0,.68fr) minmax(0,1fr) auto auto;
        row-gap:8px;
      }
      .ekd-px-head-pad{display:inline-block;width:96px;height:1px;pointer-events:none}
      .ekd-px-btn{
        display:inline-flex;align-items:center;justify-content:center;gap:4px;flex:none;
        height:28px;padding:0 9px;
        border:1px solid color-mix(in srgb,currentColor 18%,transparent);
        border-radius:7px;
        background:color-mix(in srgb,currentColor 5%,transparent);
        color:inherit;font:inherit;font-size:12px;font-weight:500;line-height:1;
        white-space:nowrap;cursor:pointer;
        transition:border-color .14s ease,background .14s ease,color .14s ease;
      }
      .ekd-px-btn:hover{
        border-color:color-mix(in srgb,var(--color-accent,#3b82f6) 62%,transparent);
        background:color-mix(in srgb,var(--color-accent,#3b82f6) 13%,transparent);
        color:var(--color-accent,#3b82f6);
      }
      .ekd-px-btn[aria-expanded="true"]{
        border-color:var(--color-accent,#3b82f6);
        background:color-mix(in srgb,var(--color-accent,#3b82f6) 16%,transparent);
        color:var(--color-accent,#3b82f6);
      }
      .ekd-px-btn:focus-visible{outline:2px solid var(--color-accent,#3b82f6);outline-offset:2px}
      .ekd-px-caret{display:inline-flex;transition:transform .16s ease}
      .ekd-px-btn[aria-expanded="true"] .ekd-px-caret{transform:rotate(180deg)}

      .ekd-px-drawer{
        grid-column:1 / -1;
        display:flex;flex-direction:column;gap:12px;
        margin:2px 0 6px;padding:13px 14px;
        border:1px solid color-mix(in srgb,currentColor 15%,transparent);
        border-radius:10px;
        background:color-mix(in srgb,currentColor 4%,var(--color-bg,transparent));
        color:inherit;font:13px/1.5 var(--font-ui,system-ui,-apple-system,sans-serif);
        cursor:default;
      }
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
        color:color-mix(in srgb,currentColor 64%,transparent);
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
      .ekd-px-pill:hover:not([disabled]){
        border-color:color-mix(in srgb,currentColor 34%,transparent);
        background:color-mix(in srgb,currentColor 8%,transparent);
        color:inherit;
      }
      .ekd-px-pill[aria-pressed="true"]{
        border-color:var(--color-accent,#3b82f6);
        background:color-mix(in srgb,var(--color-accent,#3b82f6) 17%,transparent);
        color:var(--color-accent,#60a5fa);
        font-weight:600;
      }
      .ekd-px-pill[data-kind="no"][aria-pressed="true"]{
        border-color:color-mix(in srgb,currentColor 42%,transparent);
        background:color-mix(in srgb,currentColor 14%,transparent);
        color:inherit;
      }
      .ekd-px-pill[disabled]{opacity:.45;cursor:not-allowed}
      .ekd-px-pill:focus-visible{outline:2px solid var(--color-accent,#3b82f6);outline-offset:2px}
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
        width:100%;min-width:0;height:30px;padding:0 10px;
        border:1px solid color-mix(in srgb,currentColor 20%,transparent);
        border-radius:7px;
        background:color-mix(in srgb,var(--color-bg,#18181b) 82%,currentColor 4%);
        color:inherit;font:inherit;font-size:12.5px;
        font-variant-numeric:tabular-nums;
        outline:none;transition:border-color .12s ease,box-shadow .12s ease;
      }
      .ekd-px-input::placeholder{color:color-mix(in srgb,currentColor 40%,transparent)}
      .ekd-px-input:focus{
        border-color:var(--color-accent,#3b82f6);
        box-shadow:0 0 0 2px color-mix(in srgb,var(--color-accent,#3b82f6) 20%,transparent);
      }
      .ekd-px-input[aria-invalid="true"]{border-color:var(--color-danger,#ef4444)}
      .ekd-px-input[disabled]{opacity:.5;cursor:not-allowed}

      .ekd-px-presets{display:flex;flex-wrap:wrap;gap:5px;align-items:center}
      .ekd-px-preset{
        height:22px;padding:0 8px;
        border:1px solid color-mix(in srgb,currentColor 16%,transparent);
        border-radius:999px;
        background:transparent;
        color:color-mix(in srgb,currentColor 68%,transparent);
        font:inherit;font-size:11px;font-weight:500;
        font-variant-numeric:tabular-nums;
        cursor:pointer;transition:border-color .12s ease,background .12s ease,color .12s ease;
      }
      .ekd-px-preset:hover:not([disabled]){
        border-color:color-mix(in srgb,currentColor 34%,transparent);
        color:inherit;
      }
      .ekd-px-preset[aria-pressed="true"]{
        border-color:var(--color-accent,#3b82f6);
        background:color-mix(in srgb,var(--color-accent,#3b82f6) 16%,transparent);
        color:var(--color-accent,#60a5fa);
        font-weight:600;
      }
      .ekd-px-preset[disabled]{opacity:.45;cursor:not-allowed}
      .ekd-px-preset:focus-visible{outline:2px solid var(--color-accent,#3b82f6);outline-offset:1px}

      .ekd-px-meta{font-size:11.5px;color:color-mix(in srgb,currentColor 54%,transparent);line-height:1.4}
      .ekd-px-hint{color:color-mix(in srgb,currentColor 56%,transparent);font-size:12px;margin:4px 0 0}
      .ekd-px-hint[data-tone="error"],.ekd-px-issue{color:var(--color-danger,#ef4444);font-size:11.5px;line-height:1.4}

      .ekd-px-foot{
        display:flex;flex-wrap:wrap;gap:8px;align-items:center;
        padding-top:9px;
        border-top:1px solid color-mix(in srgb,currentColor 12%,transparent);
      }
      .ekd-px-status{margin-right:auto;font-size:12px;color:color-mix(in srgb,currentColor 60%,transparent)}
      .ekd-px-status[data-tone="ok"]{color:var(--color-done,#22c55e)}
      .ekd-px-status[data-tone="error"]{color:var(--color-danger,#ef4444)}
      .ekd-px-primary{
        height:28px;padding:0 14px;border:0;border-radius:7px;
        background:var(--color-accent,#3b82f6);color:var(--color-text-on-accent,#fff);
        font:inherit;font-size:12.5px;font-weight:500;cursor:pointer;
      }
      .ekd-px-primary:hover:not([disabled]){filter:brightness(1.06)}
      .ekd-px-primary[disabled]{opacity:.5;cursor:not-allowed}
      .ekd-px-primary:focus-visible{outline:2px solid var(--color-accent,#3b82f6);outline-offset:2px}
      .ekd-px-quiet{
        height:28px;padding:0 11px;
        border:1px solid color-mix(in srgb,currentColor 18%,transparent);
        border-radius:7px;background:transparent;color:inherit;
        font:inherit;font-size:12.5px;cursor:pointer;
      }
      .ekd-px-quiet:hover{background:color-mix(in srgb,currentColor 8%,transparent)}
      .ekd-px-readonly{
        padding:8px 10px;border-radius:7px;
        border:1px solid color-mix(in srgb,currentColor 16%,transparent);
        background:color-mix(in srgb,currentColor 4%,transparent);
        color:color-mix(in srgb,currentColor 68%,transparent);font-size:12px;
      }
      @media(prefers-reduced-motion:reduce){
        .ekd-px-btn,.ekd-px-caret,.ekd-px-pill,.ekd-px-input,.ekd-px-preset{transition:none}
      }
    `;
  }

  function ensureStyle() {
    if (state.style && state.style.isConnected) return;
    const style = document.createElement('style');
    style.dataset.ekdProviderEnhance = '1';
    style.textContent = styleText();
    document.head.append(style);
    state.style = style;
  }

  function note(text, tone) {
    const form = document.querySelector('.settings-dialog .pf');
    if (!form) return;
    let line = form.querySelector('[data-ekd-px-note]');
    if (!line) {
      line = el('p', { class: 'ekd-px-hint', role: 'status', 'data-ekd-px-note': '1' });
      form.prepend(line);
    }
    line.textContent = text;
    if (tone) line.dataset.tone = tone;
    else delete line.dataset.tone;
  }

  // -------------------------------------------------------------- row buttons

  async function decorate() {
    if (state.removed) return;
    const form = document.querySelector('.settings-dialog .pf');
    if (!form) return;
    const table = form.querySelector('.pmt');
    if (!table) return;
    const allGrids = [...table.querySelectorAll('.pmt-grid')];
    const head = allGrids.find(row => row.classList.contains('pmt-head'));
    const rows = allGrids.filter(row => !row.classList.contains('pmt-head'));
    const editMode = Boolean(form.querySelector('.pf-back'));
    if (!editMode) {
      if (!form.querySelector('[data-ekd-px-hint]')) {
        const foot = form.querySelector('.pf-foot');
        const hint = el('p', { class: 'ekd-px-hint', 'data-ekd-px-hint': '1', text: '保存供应商后，可在每一行展开设置模型能力、思考强度与上下文。' });
        if (foot) foot.before(hint);
        else form.append(hint);
      }
      return;
    }
    let config;
    try {
      config = await readConfig();
    } catch {
      return;
    }
    const providerId = resolveProvider(form, config);
    if (!providerId) return;
    const managed = providerId.startsWith('managed:');
    state.props = { providerId, managed };
    table.dataset.ekdPxTable = '1';
    if (head && !head.querySelector('[data-ekd-px-head-pad]')) {
      head.append(el('span', { class: 'ekd-px-head-pad', 'data-ekd-px-head-pad': '1', 'aria-hidden': 'true' }));
    }
    for (const row of rows) {
      row.dataset.ekdPxRow = '1';
      if (row.querySelector('[data-ekd-px-button]')) continue;
      const remove = row.querySelector('button[aria-label="移除模型"], button[aria-label="Remove model"]')
        || [...row.querySelectorAll('button')].filter(b => !b.hasAttribute('data-ekd-px-button')).at(-1);
      if (!remove) continue;
      const button = el('button', {
        type: 'button',
        class: 'ekd-px-btn',
        'aria-label': '能力与限制',
        'aria-expanded': 'false',
        title: '展开设置模型能力、思考强度与上下文限制',
        'data-ekd-px-button': '1',
        'data-ekd-enhance': '1',
      }, [
        icon(SLIDERS, 12),
        el('span', { text: '能力与限制' }),
        el('span', { class: 'ekd-px-caret', 'aria-hidden': 'true' }, [icon(CHEVRON_DOWN, 12)]),
      ]);
      if (managed) button.dataset.state = 'active';
      button.addEventListener('click', event => {
        event.preventDefault();
        event.stopPropagation();
        void toggleForRow(row, button, providerId, managed);
      });
      remove.after(button);
    }
  }

  // ----------------------------------------------------------- inline drawer

  function closePanel() {
    if (state.panelButton) {
      state.panelButton.setAttribute('aria-expanded', 'false');
      state.panelButton = null;
    }
    state.panelRow = null;
    const panel = state.panel;
    if (!panel) return;
    state.panel = null;
    if (typeof panel.close === 'function') {
      try { panel.close(); } catch {}
    }
    panel.remove();
  }

  function openUnsavedDrawer(row, button, message) {
    closePanel();
    ensureStyle();
    const drawer = el('div', {
      class: 'ekd-px-drawer',
      role: 'region',
      'aria-label': '能力与限制提示',
      'data-ekd-provider-enhance': '1',
      draggable: 'false',
      onDragstart: event => {
        event.preventDefault();
        event.stopPropagation();
      },
    }, [
      el('div', { class: 'ekd-px-readonly', text: message }),
      el('div', { class: 'ekd-px-foot' }, [
        el('span', { class: 'ekd-px-status', text: '保存供应商后即可直接在此行展开编辑。' }),
        el('button', { type: 'button', class: 'ekd-px-quiet', text: '收起', onClick: closePanel }),
      ]),
    ]);
    row.append(drawer);
    button.setAttribute('aria-expanded', 'true');
    state.panel = drawer;
    state.panelRow = row;
    state.panelButton = button;
  }

  async function toggleForRow(row, button, providerId, managed) {
    if (state.removed) return;
    if (state.panel && state.panelRow === row) {
      closePanel();
      return;
    }
    const inputs = rowInputs(row);
    const firstInput = inputs[0] || row.querySelector('input');
    const modelId = firstInput ? firstInput.value.trim() : '';
    if (!modelId) {
      openUnsavedDrawer(row, button, '请先填写模型 ID 并点击底部「保存」，再展开配置能力与思考强度。');
      return;
    }
    let config;
    try {
      config = await readConfig();
    } catch (error) {
      note('读不到模型配置：' + messageOf(error), 'error');
      return;
    }
    const models = config.models || {};
    const key = providerId + '/' + modelId;
    const model = models[key] || Object.entries(models).find(([k, item]) => {
      const p = providerPart(k) || (item && typeof item.provider === 'string' ? item.provider.trim() : '');
      const m = providerPart(k) ? modelPart(k) : (item && typeof item.model === 'string' && item.model.trim() ? item.model.trim() : k);
      return p === providerId && m === modelId;
    })?.[1];
    if (!model) {
      openUnsavedDrawer(row, button, '该模型尚未写入配置。请先点击右下角「保存」保存供应商，再展开设置能力与思考强度。');
      return;
    }
    openPanel({ providerId, key, model, managed, row, button });
  }

  function messageOf(error) {
    return error && typeof error.message === 'string' && error.message ? error.message : '未知错误';
  }

  function syncOfficialRowContextInput(row, nextSize) {
    if (!row || !Number.isFinite(nextSize) || nextSize <= 0) return;
    const inputs = rowInputs(row);
    const ctxInput = inputs[1];
    if (!ctxInput) return;
    const nextText = String(nextSize);
    if (ctxInput.value === nextText) return;
    try {
      const descriptor = typeof HTMLInputElement !== 'undefined'
        ? Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')
        : null;
      if (descriptor && typeof descriptor.set === 'function') {
        descriptor.set.call(ctxInput, nextText);
      } else {
        ctxInput.value = nextText;
      }
      if (typeof Event === 'function') {
        ctxInput.dispatchEvent(new Event('input', { bubbles: true }));
        ctxInput.dispatchEvent(new Event('change', { bubbles: true }));
      }
    } catch {
      ctxInput.value = nextText;
    }
  }

  function openPanel(context) {
    closePanel();
    ensureStyle();
    const draft = draftOf(context.model);
    const managed = Boolean(context.managed);
    const status = el('span', {
      class: 'ekd-px-status',
      role: 'status',
      text: managed ? '内置订阅模型只读' : '保存后立即生效，新会话与模型切换即刻可用。',
    });
    const save = el('button', { type: 'button', class: 'ekd-px-primary', text: '保存' });
    save.disabled = managed;

    // 1. 输入与调用能力
    const capabilityPills = el('div', { class: 'ekd-px-pills', role: 'group', 'aria-label': '输入与调用能力' });
    for (const item of CAPABILITIES) {
      const pressed = draft.capabilities.has(item.key);
      const button = el('button', {
        type: 'button',
        class: 'ekd-px-pill',
        'aria-pressed': pressed ? 'true' : 'false',
      }, [
        el('span', { class: 'ekd-px-dot', 'aria-hidden': 'true' }),
        item.label,
      ]);
      button.disabled = managed;
      button.addEventListener('click', () => {
        const on = button.getAttribute('aria-pressed') === 'true';
        button.setAttribute('aria-pressed', on ? 'false' : 'true');
        if (on) draft.capabilities.delete(item.key);
        else draft.capabilities.add(item.key);
      });
      capabilityPills.append(button);
    }

    // 2. 思考强度（直接点选：no / low / medium / high / xhigh / max）
    const thinkingPills = el('div', { class: 'ekd-px-pills', role: 'group', 'aria-label': '思考强度' });
    const thinkingHint = el('span', { class: 'ekd-px-meta' });
    const optionButtons = new Map();

    function syncThinkingUI() {
      const isNo = draft.thinking === 'none' && draft.supportEfforts.length === 0;
      for (const [value, button] of optionButtons) {
        if (value === 'no') {
          button.setAttribute('aria-pressed', isNo ? 'true' : 'false');
        } else {
          button.setAttribute('aria-pressed', draft.supportEfforts.includes(value) ? 'true' : 'false');
        }
      }
      if (isNo) {
        thinkingHint.textContent = '已选 no（不开启思考）';
      } else if (draft.supportEfforts.length === 0) {
        thinkingHint.textContent = '当前开启默认思考；可点选 low–max 指定可用强度，或点 no 关闭思考';
      } else {
        thinkingHint.textContent = '已启用思考强度：' + draft.supportEfforts.join(' / ') + '（可多选，点 no 关闭思考）';
      }
    }

    for (const option of THINKING_OPTIONS) {
      const button = el('button', {
        type: 'button',
        class: 'ekd-px-pill',
        'data-kind': option.value === 'no' ? 'no' : 'effort',
        'data-effort': option.value,
        'aria-pressed': 'false',
        title: option.title,
        text: option.label,
      });
      button.disabled = managed;
      button.addEventListener('click', () => {
        selectThinkingOption(draft, option.value);
        syncThinkingUI();
      });
      optionButtons.set(option.value, button);
      thinkingPills.append(button);
      if (option.value === 'no') {
        thinkingPills.append(el('span', { class: 'ekd-px-divider', 'aria-hidden': 'true' }));
      }
    }

    // Preserve visibility & removal for any pre-existing custom effort tags in config.toml
    const customEfforts = draft.supportEfforts.filter(item => !EFFORT_ORDER.includes(item));
    for (const customValue of customEfforts) {
      const chip = el('button', {
        type: 'button',
        class: 'ekd-px-pill',
        'data-kind': 'effort',
        'data-effort': customValue,
        'aria-pressed': 'true',
        title: '点击移除自定义思考强度 ' + customValue,
        text: customValue,
      });
      chip.disabled = managed;
      chip.addEventListener('click', () => {
        selectThinkingOption(draft, customValue);
        syncThinkingUI();
      });
      optionButtons.set(customValue, chip);
      thinkingPills.append(chip);
    }
    syncThinkingUI();

    // 3. 同行双列：上下文长度（含 256K–1M 快捷预设）+ 最大输出长度
    function createContextField(initial) {
      const input = el('input', {
        class: 'ekd-px-input',
        type: 'text',
        autocomplete: 'off',
        spellcheck: 'false',
        value: formatSize(initial),
        placeholder: '如 256K、1M 或 128000',
        'aria-label': '上下文长度',
      });
      input.disabled = managed;
      const presetsRow = el('div', { class: 'ekd-px-presets', role: 'group', 'aria-label': '上下文快捷档位' });
      const presetButtons = [];
      const meta = el('span', { class: 'ekd-px-meta' });
      const issue = el('span', { class: 'ekd-px-issue' });

      function sync() {
        const parsed = parseSize(input.value, false);
        issue.textContent = parsed.error || '';
        meta.textContent = parsed.error || parsed.value === null
          ? ''
          : parsed.value.toLocaleString('zh-CN') + ' tokens · 支持 1K=1024、1M=1024K 或直接填整数';
        input.setAttribute('aria-invalid', parsed.error ? 'true' : 'false');
        for (const item of presetButtons) {
          item.button.setAttribute('aria-pressed', parsed.value === item.value ? 'true' : 'false');
        }
        return parsed;
      }

      for (const preset of CONTEXT_PRESETS) {
        const btn = el('button', {
          type: 'button',
          class: 'ekd-px-preset',
          'aria-pressed': 'false',
          text: preset.label,
        });
        btn.disabled = managed;
        btn.addEventListener('click', () => {
          input.value = preset.label;
          sync();
        });
        presetButtons.push({ value: preset.value, button: btn });
        presetsRow.append(btn);
      }

      input.addEventListener('input', sync);
      sync();

      return {
        node: el('div', { class: 'ekd-px-section' }, [
          el('span', { class: 'ekd-px-title', text: '上下文长度' }),
          el('div', { class: 'ekd-px-input-wrap' }, [input]),
          presetsRow,
          issue,
          meta,
        ]),
        read: sync,
      };
    }

    function createOutputField(initial) {
      const input = el('input', {
        class: 'ekd-px-input',
        type: 'text',
        autocomplete: 'off',
        spellcheck: 'false',
        value: initial === null ? '' : formatSize(initial),
        placeholder: '如 32K、64K，留空不限制',
        'aria-label': '最大输出长度',
      });
      input.disabled = managed;
      const presetsRow = el('div', { class: 'ekd-px-presets', role: 'group', 'aria-label': '最大输出快捷档位' });
      const outputPresets = [
        { label: '32K', value: 32768 },
        { label: '64K', value: 65536 },
        { label: '128K', value: 131072 },
      ];
      const presetButtons = [];
      const clearBtn = el('button', {
        type: 'button',
        class: 'ekd-px-preset',
        'aria-pressed': 'false',
        text: '不限制',
      });
      clearBtn.disabled = managed;
      const meta = el('span', { class: 'ekd-px-meta' });
      const issue = el('span', { class: 'ekd-px-issue' });

      function sync() {
        const parsed = parseSize(input.value, true);
        issue.textContent = parsed.error || '';
        meta.textContent = parsed.error
          ? ''
          : parsed.value === null
            ? '留空表示不声明上限（使用模型默认输出限制）'
            : parsed.value.toLocaleString('zh-CN') + ' tokens · 清空或点「不限制」恢复默认';
        input.setAttribute('aria-invalid', parsed.error ? 'true' : 'false');
        clearBtn.setAttribute('aria-pressed', !parsed.error && parsed.value === null ? 'true' : 'false');
        for (const item of presetButtons) {
          item.button.setAttribute('aria-pressed', parsed.value === item.value ? 'true' : 'false');
        }
        return parsed;
      }

      for (const preset of outputPresets) {
        const btn = el('button', {
          type: 'button',
          class: 'ekd-px-preset',
          'aria-pressed': 'false',
          text: preset.label,
        });
        btn.disabled = managed;
        btn.addEventListener('click', () => {
          input.value = preset.label;
          sync();
        });
        presetButtons.push({ value: preset.value, button: btn });
        presetsRow.append(btn);
      }
      clearBtn.addEventListener('click', () => {
        input.value = '';
        sync();
      });
      presetsRow.append(clearBtn);

      input.addEventListener('input', sync);
      sync();

      return {
        node: el('div', { class: 'ekd-px-section' }, [
          el('span', { class: 'ekd-px-title', text: '最大输出长度' }),
          el('div', { class: 'ekd-px-input-wrap' }, [input]),
          presetsRow,
          issue,
          meta,
        ]),
        read: sync,
      };
    }

    const contextField = createContextField(draft.maxContextSize);
    const outputField = createOutputField(draft.maxOutputSize);

    save.addEventListener('click', async () => {
      if (managed) return;
      const contextParsed = contextField.read();
      const outputParsed = outputField.read();
      if (contextParsed.error || contextParsed.value === null) {
        status.textContent = '上下文长度：' + contextParsed.error;
        status.dataset.tone = 'error';
        return;
      }
      if (outputParsed.error) {
        status.textContent = '最大输出长度：' + outputParsed.error;
        status.dataset.tone = 'error';
        return;
      }
      save.disabled = true;
      status.textContent = '正在保存…';
      delete status.dataset.tone;
      try {
        const mapped = applyThinkingState([...draft.capabilities], draft.thinking, draft.adaptiveThinking);
        const edit = {
          capabilities: mapped.capabilities,
          thinking: mapped.thinking,
          max_context_size: contextParsed.value,
          max_output_size: outputParsed.value,
          support_efforts: mapped.thinking === 'none' ? [] : normalizeEfforts(draft.supportEfforts),
        };
        if (mapped.thinking === 'toggle') {
          if (typeof draft.preservedAdaptiveThinking === 'boolean') {
            edit.adaptive_thinking = draft.preservedAdaptiveThinking;
          } else if (mapped.adaptive_thinking) {
            edit.adaptive_thinking = true;
          }
        } else if (mapped.thinking === 'none' && draft.preservedAdaptiveThinking === false) {
          edit.adaptive_thinking = false;
        }
        await request('providers.updateModel', {
          providerId: context.providerId,
          model: context.key,
          edit,
        });
        syncOfficialRowContextInput(context.row, contextParsed.value);
        status.textContent = '已保存并读回核对，当前窗口立即生效。';
        status.dataset.tone = 'ok';
      } catch (error) {
        status.textContent = '保存失败：' + messageOf(error);
        status.dataset.tone = 'error';
      } finally {
        save.disabled = managed;
      }
    });

    const drawer = el('div', {
      class: 'ekd-px-drawer',
      role: 'region',
      'aria-label': '能力与限制',
      'data-ekd-provider-enhance': '1',
      draggable: 'false',
      onDragstart: event => {
        event.preventDefault();
        event.stopPropagation();
      },
    });

    if (managed) {
      drawer.append(el('div', { class: 'ekd-px-readonly', text: '内置订阅供应商由官方托管，模型能力与限制为只读。' }));
    }

    drawer.append(
      el('div', { class: 'ekd-px-top-grid' }, [
        el('div', { class: 'ekd-px-section' }, [
          el('span', { class: 'ekd-px-title', text: '输入与调用能力' }),
          capabilityPills,
        ]),
        el('div', { class: 'ekd-px-section' }, [
          el('span', { class: 'ekd-px-title', text: '思考强度' }),
          thinkingPills,
          thinkingHint,
        ]),
      ]),
      el('div', { class: 'ekd-px-limits' }, [
        contextField.node,
        outputField.node,
      ]),
      el('div', { class: 'ekd-px-foot' }, [
        status,
        el('button', { type: 'button', class: 'ekd-px-quiet', text: '收起', onClick: closePanel }),
        save,
      ]),
    );

    if (context.row) {
      context.row.append(drawer);
      if (context.button) {
        context.button.setAttribute('aria-expanded', 'true');
        state.panelButton = context.button;
      }
      state.panelRow = context.row;
    } else {
      document.body.append(drawer);
    }
    drawer.shown = true;
    state.panel = drawer;
  }

  /**
   * Formats stored token integers into concise K/M notation when exact (262144 -> 256K,
   * 1048576 -> 1M), keeping arbitrary numbers (128000) intact.
   */
  function formatSize(num) {
    if (typeof num !== 'number' || !Number.isFinite(num) || num <= 0) return '';
    if (num % (1024 * 1024) === 0) return String(num / (1024 * 1024)) + 'M';
    if (num % 1024 === 0) return String(num / 1024) + 'K';
    return String(num);
  }

  /**
   * Accepts plain digits or K/KB/M/MB suffixes (1K = 1024, 1M = 1024 * 1024).
   * A cleared output size stays null so the core omits the field; a cleared
   * context size is an error.
   */
  function parseSize(raw, allowEmpty) {
    const text = String(raw || '').trim().replace(/[,\s_]/g, '');
    if (text === '') return allowEmpty ? { value: null, error: '' } : { value: null, error: '不能为空' };
    const match = text.match(/^(\d+(?:\.\d+)?)([kKmM])(?:[bB])?$/);
    const plain = !match ? text.match(/^(\d+(?:\.\d+)?)$/) : null;
    if (!match && !plain) return { value: null, error: '只接受数字，或以 K、M 结尾（如 256K、1M）' };
    const base = Number((match || plain)[1]);
    const unit = match ? match[2].toUpperCase() : '';
    const value = Math.round(unit === 'K' ? base * 1024 : unit === 'M' ? base * 1024 * 1024 : base);
    if (!Number.isFinite(value) || value < 1) return { value: null, error: '需要正整数' };
    if (value > 10000000) return { value: null, error: '超出上限（10M）' };
    return { value, error: '' };
  }

  // ---------------------------------------------------------------- lifecycle

  let scanning = false;
  function scan() {
    if (state.removed || scanning) return;
    scanning = true;
    ensureStyle();
    void decorate().catch(() => {}).finally(() => { scanning = false; });
  }

  function install() {
    ensureStyle();
    state.observer = new MutationObserver(() => scan());
    state.observer.observe(document.body, { childList: true, subtree: true });
    scan();
  }

  function clearDecorations() {
    closePanel();
    for (const node of document.querySelectorAll('[data-ekd-px-button],[data-ekd-px-head-pad],[data-ekd-px-hint],[data-ekd-px-note],[data-ekd-provider-enhance]')) {
      node.remove();
    }
    for (const table of document.querySelectorAll('[data-ekd-px-table]')) {
      delete table.dataset.ekdPxTable;
    }
    for (const row of document.querySelectorAll('[data-ekd-px-row]')) {
      delete row.dataset.ekdPxRow;
    }
  }

  function remove() {
    if (state.removed) return;
    state.removed = true;
    state.observer?.disconnect();
    state.observer = null;
    clearDecorations();
    state.style?.remove();
    state.style = null;
  }

  return {
    install,
    remove,
    clearDecorations,
    scan,
    formatSize,
    parseSize,
    thinkingMode,
    readThinkingState,
    applyThinkingState,
    selectThinkingOption,
    draftOf,
    resolveProvider,
    openPanel,
    closePanel,
    CONTEXT_PRESETS,
    THINKING_OPTIONS,
    state,
  };
}
