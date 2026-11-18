// Adds the model fields the official provider form does not expose:
// capabilities, thinking mode, adaptive thinking, context/output size and
// reasoning efforts.
//
// Loaded by renderer.js as an independent unit (`provider_enhance.js`) and
// instantiated with the native transport. It owns no globals, installs one
// observer, and every write goes through the core action
// `providers.updateModel`, so an api_key never reaches this script.
function createProviderEnhance(options) {
  const request = options.request;
  const state = { observer: null, removed: false, panel: null, style: null, props: null };

  const CAPABILITIES = [
    { key: 'image_in', label: '图片输入' },
    { key: 'video_in', label: '视频输入' },
    { key: 'tool_use', label: '工具调用' },
  ];
  const THINKING = [
    { value: 'none', label: '不支持' },
    { value: 'toggle', label: '可开关' },
    { value: 'always', label: '始终开启' },
  ];
  const PRESET_EFFORTS = ['low', 'medium', 'high'];

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

  // ------------------------------------------------------------------- config

  function modelPart(key) {
    const index = String(key).indexOf('/');
    return index === -1 ? String(key) : String(key).slice(index + 1);
  }

  function providerPart(key) {
    const index = String(key).indexOf('/');
    return index === -1 ? '' : String(key).slice(0, index);
  }

  function thinkingMode(model) {
    const capabilities = Array.isArray(model?.capabilities) ? model.capabilities : [];
    if (capabilities.includes('always_thinking')) return 'always';
    if (capabilities.includes('thinking') || model?.adaptiveThinking === true || model?.adaptive_thinking === true) return 'toggle';
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

  function applyThinkingState(baseCapabilities, thinking, adaptiveThinking) {
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

  /** Turns one stored model into the editable draft the dialog starts from. */
  function draftOf(model) {
    const capabilities = Array.isArray(model?.capabilities) ? model.capabilities : [];
    const thinkingState = readThinkingState(model);
    return {
      capabilities: new Set(CAPABILITIES.map(item => item.key).filter(key => capabilities.includes(key))),
      thinking: thinkingState.thinking,
      adaptiveThinking: thinkingState.adaptiveThinking,
      maxContextSize: typeof model?.maxContextSize === 'number'
        ? model.maxContextSize
        : (typeof model?.max_context_size === 'number' ? model.max_context_size : 0),
      maxOutputSize: typeof model?.maxOutputSize === 'number'
        ? model.maxOutputSize
        : (typeof model?.max_output_size === 'number' ? model.max_output_size : null),
      supportEfforts: Array.isArray(model?.supportEfforts)
        ? [...model.supportEfforts]
        : (Array.isArray(model?.support_efforts) ? [...model.support_efforts] : []),
    };
  }

  async function readConfig() {
    const config = await request('config.get');
    return config && typeof config === 'object' ? config : {};
  }

  /**
   * Resolves which saved provider the open form edits. The official form does not
   * carry the provider id, so the base URL and wire type are matched first, then
   * the owner of the model ids already present in the rows.
   */
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
        return String(entry.baseUrl || '') === baseUrl && (!type || String(entry.type || '').toLowerCase() === type);
      });
      if (byBase.length === 1) return byBase[0];
    }
    const rows = [...form.querySelectorAll('.pmt .pmt-grid')].filter(row => !row.classList.contains('pmt-head'));
    const owners = new Set();
    for (const row of rows) {
      const input = row.querySelector('input');
      const modelId = input ? input.value.trim() : '';
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
      .ekd-px-btn{margin-left:2px}
      .ekd-px-dialog{width:min(560px,calc(100vw - 40px));max-height:min(760px,calc(100vh - 60px));padding:0;border:1px solid var(--color-line,rgba(128,128,128,.28));border-radius:12px;background:var(--color-bg,#fff);color:var(--color-text,#111);font:14px/1.55 var(--font-ui,system-ui);box-shadow:0 24px 64px rgba(0,0,0,.28)}
      .ekd-px-dialog::backdrop{background:rgba(0,0,0,.4)}
      .ekd-px-head{display:flex;gap:12px;align-items:flex-start;padding:18px 20px 12px;border-bottom:1px solid var(--color-line,rgba(128,128,128,.22))}
      .ekd-px-head h2{margin:0;font-size:16px;font-weight:600}
      .ekd-px-head p{margin:4px 0 0;color:var(--color-text-muted,#6b6b6b);font-size:12px}
      .ekd-px-close{margin-left:auto;flex:none;display:inline-flex;align-items:center;justify-content:center;width:26px;height:26px;border:0;border-radius:6px;background:transparent;color:inherit;cursor:pointer}
      .ekd-px-close:hover{background:var(--color-hover,rgba(128,128,128,.14))}
      .ekd-px-close:focus-visible{outline:2px solid var(--color-accent,#3b82f6);outline-offset:2px}
      .ekd-px-body{display:flex;flex-direction:column;gap:15px;padding:16px 20px;overflow-y:auto;max-height:min(56vh,520px)}
      .ekd-px-field{display:flex;flex-direction:column;gap:6px}
      .ekd-px-label{display:flex;align-items:baseline;gap:8px;font-size:12px;color:var(--color-text-muted,#6b6b6b)}
      .ekd-px-label strong{color:inherit;font-size:13px;font-weight:600}
      .ekd-px-row{display:flex;flex-wrap:wrap;gap:8px;align-items:center}
      .ekd-px-switch{display:inline-flex;gap:8px;align-items:center;padding:6px 12px;border:1px solid var(--color-line,rgba(128,128,128,.3));border-radius:999px;background:transparent;color:inherit;font:inherit;font-size:13px;cursor:pointer}
      .ekd-px-switch:hover{background:var(--color-hover,rgba(128,128,128,.12))}
      .ekd-px-switch[aria-pressed=true]{border-color:var(--color-accent,#3b82f6);background:var(--color-accent-soft,rgba(59,130,246,.14))}
      .ekd-px-switch:focus-visible{outline:2px solid var(--color-accent,#3b82f6);outline-offset:2px}
      .ekd-px-dot{width:7px;height:7px;border-radius:50%;background:var(--color-line-strong,rgba(128,128,128,.55))}
      .ekd-px-switch[aria-pressed=true] .ekd-px-dot{background:var(--color-accent,#3b82f6)}
      .ekd-px-switch[disabled]{opacity:.45;cursor:not-allowed}
      .ekd-px-seg{display:inline-flex;gap:2px;padding:2px;border-radius:999px;background:var(--color-fill-1,rgba(128,128,128,.14))}
      .ekd-px-seg button{min-width:76px;height:26px;padding:0 12px;border:0;border-radius:999px;background:transparent;color:inherit;font:inherit;font-size:13px;cursor:pointer}
      .ekd-px-seg button[aria-pressed=true]{background:var(--color-selected,rgba(128,128,128,.26));font-weight:600}
      .ekd-px-seg button:focus-visible{outline:2px solid var(--color-accent,#3b82f6);outline-offset:2px}
      .ekd-px-seg button[disabled]{opacity:.45;cursor:not-allowed}
      .ekd-px-number input{width:190px}
      .ekd-px-hint{color:var(--color-text-faint,#8a8a8a);font-size:12px}
      .ekd-px-issue{color:var(--color-danger,#d33);font-size:12px}
      .ekd-px-chips{display:flex;flex-wrap:wrap;gap:6px}
      .ekd-px-chip{height:26px;padding:0 12px;border:1px solid var(--color-line,rgba(128,128,128,.3));border-radius:999px;background:transparent;color:inherit;font:inherit;font-size:13px;cursor:pointer}
      .ekd-px-chip:hover{background:var(--color-hover,rgba(128,128,128,.12))}
      .ekd-px-chip[aria-pressed=true]{border-color:var(--color-accent,#3b82f6);background:var(--color-accent-soft,rgba(59,130,246,.14))}
      .ekd-px-chip:focus-visible{outline:2px solid var(--color-accent,#3b82f6);outline-offset:2px}
      .ekd-px-foot{display:flex;gap:8px;align-items:center;padding:12px 20px 16px;border-top:1px solid var(--color-line,rgba(128,128,128,.22))}
      .ekd-px-status{margin-right:auto;font-size:12px;color:var(--color-text-muted,#6b6b6b)}
      .ekd-px-status[data-tone=ok]{color:var(--color-done,#15803d)}
      .ekd-px-status[data-tone=error]{color:var(--color-danger,#d33)}
      .ekd-px-primary{height:30px;padding:0 16px;border:0;border-radius:6px;background:var(--color-accent,#3b82f6);color:var(--color-text-on-accent,#fff);font:inherit;font-size:13px;cursor:pointer}
      .ekd-px-primary[disabled]{opacity:.5;cursor:not-allowed}
      .ekd-px-primary:focus-visible{outline:2px solid var(--color-accent,#3b82f6);outline-offset:2px}
      .ekd-px-quiet{height:30px;padding:0 14px;border:1px solid var(--color-line,rgba(128,128,128,.3));border-radius:6px;background:transparent;color:inherit;font:inherit;font-size:13px;cursor:pointer}
      .ekd-px-quiet:hover{background:var(--color-hover,rgba(128,128,128,.12))}
      .ekd-px-note{padding:10px 12px;border-radius:8px;background:var(--color-fill-1,rgba(128,128,128,.1));color:var(--color-text-muted,#6b6b6b);font-size:12px;line-height:1.6}
      .ekd-px-readonly{padding:10px 12px;border:1px solid var(--color-line,rgba(128,128,128,.3));border-radius:8px;color:var(--color-text-muted,#6b6b6b);font-size:13px}
      @media(prefers-reduced-motion:reduce){.ekd-px-dialog{scroll-behavior:auto}}
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

  /**
   * Adds one "能力与限制" button to every model row of a saved provider's edit
   * form. The add form gets a single hint line instead; it is never modified
   * beyond that, so its own state stays untouched.
   */
  async function decorate() {
    if (state.removed) return;
    const form = document.querySelector('.settings-dialog .pf');
    if (!form) return;
    const table = form.querySelector('.pmt');
    if (!table) return;
    const rows = [...table.querySelectorAll('.pmt-grid')].filter(row => !row.classList.contains('pmt-head'));
    const editMode = Boolean(form.querySelector('.pf-back'));
    if (!editMode) {
      if (!form.querySelector('[data-ekd-px-hint]')) {
        const foot = form.querySelector('.pf-foot');
        const hint = el('p', { class: 'ekd-px-hint', 'data-ekd-px-hint': '1', text: '保存后可在模型行设置能力与限制。' });
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
    for (const row of rows) {
      if (row.querySelector('[data-ekd-px-button]')) continue;
      const remove = row.querySelector('button[aria-label="移除模型"]');
      if (!remove) continue;
      const button = el('button', {
        type: 'button',
        class: 'ui-icon-button ui-icon-button--sm ekd-px-btn',
        'aria-label': '能力与限制',
        title: '能力与限制',
        'data-ekd-px-button': '1',
      }, [icon(SLIDERS, 14)]);
      if (managed) button.dataset.state = 'active';
      button.addEventListener('click', event => {
        event.preventDefault();
        event.stopPropagation();
        void openForRow(row, providerId, managed);
      });
      remove.after(button);
    }
  }

  // ------------------------------------------------------------------- dialog

  function closePanel() {
    const panel = state.panel;
    if (!panel) return;
    state.panel = null;
    panel.close();
    panel.remove();
  }

  async function openForRow(row, providerId, managed) {
    if (state.removed) return;
    const input = row.querySelector('input');
    const modelId = input ? input.value.trim() : '';
    if (!modelId) {
      note('先填写模型 ID 并保存供应商，再设置能力与限制。');
      return;
    }
    let config;
    try {
      config = await readConfig();
    } catch (error) {
      note('读不到模型配置：' + messageOf(error));
      return;
    }
    const models = config.models || {};
    const key = providerId + '/' + modelId;
    const model = models[key];
    if (!model) {
      note('这个模型还没有保存。先保存供应商，再设置能力与限制。');
      return;
    }
    openPanel({ providerId, key, model, managed });
  }

  function messageOf(error) {
    return error && typeof error.message === 'string' && error.message ? error.message : '未知错误';
  }

  function openPanel(context) {
    closePanel();
    ensureStyle();
    const draft = draftOf(context.model);
    const managed = context.managed;
    const status = el('span', { class: 'ekd-px-status', role: 'status' });
    const save = el('button', { type: 'button', class: 'ekd-px-primary', text: '保存' });
    save.disabled = managed;

    const name = typeof context.model.displayName === 'string' && context.model.displayName
      ? context.model.displayName
      : modelPart(context.key);

    const capabilityRow = el('div', { class: 'ekd-px-row' });
    for (const item of CAPABILITIES) {
      const pressed = draft.capabilities.has(item.key);
      const button = el('button', { type: 'button', class: 'ekd-px-switch', 'aria-pressed': pressed ? 'true' : 'false' }, [
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
      capabilityRow.append(button);
    }

    const thinkingRow = el('div', { class: 'ekd-px-seg', role: 'group', 'aria-label': '思考模式' });
    const thinkingButtons = new Map();
    const adaptive = el('button', { type: 'button', class: 'ekd-px-switch', 'aria-pressed': draft.adaptiveThinking ? 'true' : 'false' }, [
      el('span', { class: 'ekd-px-dot', 'aria-hidden': 'true' }),
      '自适应思考',
    ]);
    function syncThinking() {
      for (const [value, button] of thinkingButtons) {
        button.setAttribute('aria-pressed', draft.thinking === value ? 'true' : 'false');
      }
      const toggleable = draft.thinking === 'toggle';
      adaptive.disabled = managed || !toggleable;
      if (toggleable) adaptive.setAttribute('aria-pressed', draft.adaptiveThinking ? 'true' : 'false');
      else {
        draft.adaptiveThinking = false;
        adaptive.setAttribute('aria-pressed', 'false');
      }
    }
    for (const mode of THINKING) {
      const button = el('button', { type: 'button', 'aria-pressed': 'false', text: mode.label });
      button.disabled = managed;
      button.addEventListener('click', () => {
        draft.thinking = mode.value;
        syncThinking();
      });
      thinkingButtons.set(mode.value, button);
      thinkingRow.append(button);
    }
    adaptive.addEventListener('click', () => {
      draft.adaptiveThinking = adaptive.getAttribute('aria-pressed') !== 'true';
      syncThinking();
    });
    syncThinking();

    function sizeField(label, initial, settings) {
      const input = el('input', {
        class: 'ui-input ui-input--sm',
        type: 'text',
        inputmode: 'numeric',
        autocomplete: 'off',
        spellcheck: 'false',
        value: initial === null ? '' : String(initial),
        placeholder: settings.placeholder,
        'aria-label': label,
      });
      input.disabled = managed;
      const hint = el('span', { class: 'ekd-px-hint' });
      const issue = el('span', { class: 'ekd-px-issue' });
      function sync() {
        const parsed = parseSize(input.value, settings.allowEmpty);
        issue.textContent = parsed.error || '';
        hint.textContent = parsed.error
          ? ''
          : parsed.value === null
            ? '留空表示不声明这个字段。'
            : parsed.value.toLocaleString('zh-CN') + ' tokens · 也可以写 128K 或 1M。';
        input.setAttribute('aria-invalid', parsed.error ? 'true' : 'false');
        return parsed;
      }
      input.addEventListener('input', sync);
      sync();
      const field = el('div', { class: 'ekd-px-field' }, [
        el('div', { class: 'ekd-px-label' }, [el('strong', { text: label })]),
        el('div', { class: 'ekd-px-number' }, [input]),
        hint,
        issue,
      ]);
      return { field, read: () => parseSize(input.value, settings.allowEmpty) };
    }

    const contextSize = sizeField('上下文长度', draft.maxContextSize || null, { placeholder: '例如 200K', allowEmpty: false });
    const outputSize = sizeField('最大输出长度', draft.maxOutputSize, { placeholder: '例如 32K，可留空', allowEmpty: true });

    const effortChips = el('div', { class: 'ekd-px-chips', role: 'group', 'aria-label': '推理强度' });
    const customInput = el('input', {
      class: 'ui-input ui-input--sm',
      type: 'text',
      placeholder: '补充自定义值',
      'aria-label': '补充自定义推理强度',
      autocomplete: 'off',
      spellcheck: 'false',
    });
    const customAdd = el('button', { type: 'button', class: 'ekd-px-quiet', text: '添加' });
    const known = [...PRESET_EFFORTS, ...draft.supportEfforts].filter((value, index, list) => list.indexOf(value) === index);
    function renderEfforts() {
      effortChips.replaceChildren();
      for (const value of known) {
        const on = draft.supportEfforts.includes(value);
        const chip = el('button', { type: 'button', class: 'ekd-px-chip', 'aria-pressed': on ? 'true' : 'false', text: value });
        chip.disabled = managed;
        chip.addEventListener('click', () => {
          draft.supportEfforts = on
            ? draft.supportEfforts.filter(item => item !== value)
            : [...draft.supportEfforts, value];
          renderEfforts();
        });
        effortChips.append(chip);
      }
    }
    renderEfforts();
    function addEffort() {
      const value = customInput.value.trim();
      if (!value) return;
      if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$/.test(value)) {
        status.textContent = '自定义值只用字母、数字、点、下划线和连字符，最长 32 个字符。';
        status.dataset.tone = 'error';
        return;
      }
      if (!known.includes(value)) known.push(value);
      if (!draft.supportEfforts.includes(value)) draft.supportEfforts.push(value);
      customInput.value = '';
      status.textContent = '';
      delete status.dataset.tone;
      renderEfforts();
    }
    customAdd.addEventListener('click', addEffort);
    customInput.addEventListener('keydown', event => {
      if (event.key === 'Enter') {
        event.preventDefault();
        addEffort();
      }
    });
    customInput.disabled = managed;
    customAdd.disabled = managed;

    const body = el('div', { class: 'ekd-px-body' }, [
      el('div', { class: 'ekd-px-field' }, [
        el('div', { class: 'ekd-px-label' }, [el('strong', { text: '模型' }), name]),
        el('span', { class: 'ekd-px-hint', text: context.key }),
      ]),
      managed
        ? el('div', { class: 'ekd-px-readonly', text: '这是 Kimi 订阅的内置供应商，官方只读。能力与限制由订阅和官方模型目录决定，不能在这里修改。' })
        : null,
      el('div', { class: 'ekd-px-field' }, [
        el('div', { class: 'ekd-px-label' }, [el('strong', { text: '输入与工具' })]),
        capabilityRow,
      ]),
      el('div', { class: 'ekd-px-field' }, [
        el('div', { class: 'ekd-px-label' }, [el('strong', { text: '思考模式' })]),
        thinkingRow,
        el('div', { class: 'ekd-px-row' }, [adaptive]),
        el('span', { class: 'ekd-px-hint', text: '“可开关”允许在会话里切换思考；“始终开启”不允许关闭。自适应思考只在“可开关”时生效。' }),
      ]),
      contextSize.field,
      outputSize.field,
      el('div', { class: 'ekd-px-field' }, [
        el('div', { class: 'ekd-px-label' }, [el('strong', { text: '推理强度' })]),
        effortChips,
        el('div', { class: 'ekd-px-row' }, [customInput, customAdd]),
        el('span', { class: 'ekd-px-hint', text: '选中即写进这个模型的可选推理强度；全部不选表示不声明。' }),
      ]),
      el('div', { class: 'ekd-px-note', text: '保存后当前窗口立即生效，新会话使用新的能力与推理强度；已经开始的会话保持原来的模型设置。' }),
    ]);

    save.addEventListener('click', async () => {
      const contextParsed = contextSize.read();
      const outputParsed = outputSize.read();
      if (contextParsed.error) {
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
        await request('providers.updateModel', {
          providerId: context.providerId,
          model: context.key,
          edit: {
            capabilities: mapped.capabilities,
            thinking: mapped.thinking,
            adaptive_thinking: mapped.adaptive_thinking,
            max_context_size: contextParsed.value,
            max_output_size: outputParsed.value,
            support_efforts: draft.supportEfforts,
          },
        });
        status.textContent = '已保存并读回核对。当前窗口立即生效，新会话使用新设置。';
        status.dataset.tone = 'ok';
      } catch (error) {
        status.textContent = '保存失败：' + messageOf(error);
        status.dataset.tone = 'error';
      } finally {
        save.disabled = managed;
      }
    });

    const close = el('button', { type: 'button', class: 'ekd-px-close', 'aria-label': '关闭' }, [icon(['M6 6l12 12M18 6 6 18'], 16)]);
    close.addEventListener('click', closePanel);
    const dialog = el('dialog', { class: 'ekd-px-dialog', 'aria-label': '能力与限制', 'data-ekd-provider-enhance': '1' });
    dialog.addEventListener('cancel', event => {
      event.preventDefault();
      closePanel();
    });
    dialog.append(
      el('div', { class: 'ekd-px-head' }, [
        el('div', {}, [
          el('h2', { text: '能力与限制' }),
          el('p', { text: managed ? '内置供应商，只读' : '这些字段官方表单没有提供，保存后写回 Kimi Code 配置。' }),
        ]),
        close,
      ]),
      body,
      el('div', { class: 'ekd-px-foot' }, [
        status,
        el('button', { type: 'button', class: 'ekd-px-quiet', text: '关闭', onClick: closePanel }),
        save,
      ]),
    );
    document.body.append(dialog);
    state.panel = dialog;
    dialog.showModal();
  }

  /**
   * Accepts plain digits or a K/M suffix. A cleared output size stays null so the
   * core can omit the field; a cleared context size is an error.
   */
  function parseSize(raw, allowEmpty) {
    const text = String(raw || '').trim().replace(/[,\s_]/g, '');
    if (text === '') return allowEmpty ? { value: null, error: '' } : { value: null, error: '不能为空' };
    const match = text.match(/^(\d+(?:\.\d+)?)([kKmM])?$/);
    if (!match) return { value: null, error: '只接受数字，或以 K、M 结尾' };
    const base = Number(match[1]);
    const unit = (match[2] || '').toUpperCase();
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

  function remove() {
    if (state.removed) return;
    state.removed = true;
    closePanel();
    state.observer?.disconnect();
    state.observer = null;
    for (const node of document.querySelectorAll('[data-ekd-px-button],[data-ekd-px-hint],[data-ekd-px-note]')) node.remove();
    state.style?.remove();
    state.style = null;
  }

  return {
    install,
    remove,
    scan,
    parseSize,
    thinkingMode,
    readThinkingState,
    applyThinkingState,
    draftOf,
    resolveProvider,
    openPanel,
    closePanel,
    state,
  };
}
