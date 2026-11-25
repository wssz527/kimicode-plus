function createProviderEnhance({ request }) {
  const CAPABILITIES = [
    ['image_in', '图片输入', '图片'],
    ['video_in', '视频输入', '视频'],
    ['tool_use', '工具调用', '工具'],
  ];
  const MODES = [['none', '关闭'], ['toggle', '可切换'], ['always', '始终思考']];
  const EFFORTS = ['low', 'medium', 'high', 'xhigh', 'max'];
  const PANEL_ID = 'ekd-px-inspector';
  const state = { panel: null, style: null, table: null };
  let enabled = true;
  let removed = false;
  let observer;
  let timer;
  let generation = 0;
  let snapshot;
  let pending;

  function el(tag, props = {}, children = []) {
    const node = document.createElement(tag);
    for (const [key, value] of Object.entries(props)) {
      if (key === 'class') node.className = value;
      else if (key === 'text') node.textContent = value;
      else if (key.startsWith('on')) node.addEventListener(key.slice(2).toLowerCase(), value);
      else node.setAttribute(key, String(value));
    }
    node.append(...children);
    return node;
  }

  function button(text, onClick, props = {}) {
    return el('button', { type: 'button', class: 'ekd-px-button', text, onClick, ...props });
  }

  function formatSize(size) {
    if (!Number.isSafeInteger(size) || size < 1) return '';
    if (size % 1048576 === 0) return `${size / 1048576}M`;
    if (size % 1024 === 0) return `${size / 1024}K`;
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

  function invalidate() {
    generation += 1;
    snapshot = null;
    pending = null;
    scan();
  }

  function findForm() {
    return document.querySelector('.settings-dialog .pf, .ui-dialog .pf, [role="dialog"] .pf');
  }

  function modelInputs(row) {
    const inputs = [...row.children].filter(child => child.tagName === 'INPUT');
    return inputs.length ? inputs : [...row.querySelectorAll('input.ui-input')];
  }

  function modelInput(row) {
    return modelInputs(row)[0];
  }

  function savedModel(config, providerId, modelId) {
    const key = `${providerId}/${modelId}`;
    const models = config.models || {};
    if (Object.hasOwn(models, key)) return models[key];
    const bare = Object.hasOwn(models, modelId) ? models[modelId] : null;
    return bare?.provider === providerId && (bare.model || modelId) === modelId ? bare : null;
  }

  function resolveProvider(form, config) {
    const providers = config.providers || {};
    const inputs = [...form.querySelectorAll('.pf-field input.ui-input')]
      .filter(input => input.type !== 'password' && !input.closest('.pf-key-wrap'));
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
      .ekd-px-row,.ekd-px-toolbar,.ekd-px-dialog{
        --px-bg:var(--color-bg,light-dark(#fff,#1f1f1f));
        --px-ink:var(--color-text,light-dark(#1a1a1a,#ededed));
        --px-muted:color-mix(in srgb,var(--px-ink) 86%,var(--px-bg));
        --px-line:var(--color-line,color-mix(in srgb,var(--px-ink) 22%,var(--px-bg)));
        --px-fill:color-mix(in srgb,var(--px-ink) 8%,var(--px-bg));
        --px-accent:var(--color-accent,var(--blue,#1783ff));
        color:var(--px-ink);font-family:var(--font-ui,system-ui,sans-serif);
        -webkit-app-region:no-drag;
      }
      [data-ekd-px-mount][hidden],[data-ekd-px-mount] [hidden]{display:none}
      .ekd-px-row{grid-column:1 / -1;display:flex;align-items:center;gap:8px;min-inline-size:0}
      .ekd-px-toolbar{display:flex;align-items:center;gap:8px;flex-wrap:wrap;margin-block:0 8px}
      .ekd-px-toolbar [role="status"]{margin-inline-end:auto;font-size:12px}
      .ekd-px-button{
        min-block-size:30px;padding:4px 9px;border:1px solid var(--px-line);border-radius:6px;
        background:var(--px-bg);color:var(--px-ink);font:inherit;cursor:pointer;
      }
      .ekd-px-button:hover:not(:disabled){background:var(--px-fill)}
      .ekd-px-button:disabled{cursor:default;color:var(--px-muted)}
      .ekd-px-summary{
        border-color:transparent;background:transparent;max-inline-size:100%;min-inline-size:0;
        overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:12px;text-align:start;
      }
      .ekd-px-dialog{
        box-sizing:border-box;position:fixed;inset-block:12px;inset-inline:auto 12px;margin:0;
        inline-size:min(408px,calc(100% - 24px));max-block-size:calc(100% - 24px);max-inline-size:none;
        overflow:hidden;padding:0;border:1px solid var(--px-line);border-radius:8px;
        background:var(--px-bg);font-size:13px;line-height:1.5;
        --px-error:light-dark(#9b2325,#ffb4ab);
      }
      .ekd-px-dialog[open]{display:flex;flex-direction:column}
      .ekd-px-dialog::backdrop{background:rgba(0,0,0,.32)}
      .ekd-px-header,.ekd-px-footer{flex:none;padding:14px 18px;background:var(--px-bg)}
      .ekd-px-header{border-block-end:1px solid var(--px-line)}
      .ekd-px-header h2{font-size:16px;font-weight:600;margin:0}
      .ekd-px-identity,.ekd-px-status,.ekd-px-hint{color:var(--px-muted);overflow-wrap:anywhere;margin:0}
      .ekd-px-identity,.ekd-px-hint{font-size:12px}
      .ekd-px-body{min-block-size:0;overflow:auto;overscroll-behavior:contain;padding:14px 18px;display:flex;flex-direction:column;gap:14px}
      .ekd-px-body > *{flex:none}
      .ekd-px-group{border:0;margin:0;padding:0;min-inline-size:0}
      .ekd-px-group legend{font-weight:600;padding:0;margin-block-end:6px}
      .ekd-px-options,.ekd-px-actions{display:flex;flex-wrap:wrap;align-items:center;gap:6px}
      .ekd-px-check,.ekd-px-mode,.ekd-px-switch{display:inline-flex;align-items:center;gap:5px;padding:4px 6px;overflow-wrap:anywhere}
      .ekd-px-check input,.ekd-px-mode input,.ekd-px-row input{margin:0;accent-color:var(--px-accent)}
      .ekd-px-mode{border:1px solid var(--px-line);border-radius:6px}
      .ekd-px-mode:has(:checked){background:var(--px-fill);color:var(--px-ink);border-color:var(--px-accent);font-weight:600}
      .ekd-px-check:has(:checked){color:var(--px-ink);font-weight:600}
      .ekd-px-switch{margin-top:5px;padding-inline:0;cursor:pointer}
      .ekd-px-switch [role="switch"]{appearance:none;inline-size:30px;block-size:17px;border:1px solid var(--px-muted);border-radius:var(--radius-sm,8px);margin:0;background:var(--px-fill);padding:2px}
      .ekd-px-switch [role="switch"]::before{content:'';display:block;inline-size:11px;block-size:11px;border-radius:50%;background:var(--px-ink)}
      .ekd-px-switch [role="switch"]:checked{background:var(--px-accent);border-color:var(--px-ink)}
      .ekd-px-switch [role="switch"]:checked::before{margin-inline-start:auto;background:#fff}
      .ekd-px-limits{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:8px}
      .ekd-px-limits label{display:grid;gap:4px;font-weight:600}
      .ekd-px-input{
        box-sizing:border-box;inline-size:100%;min-inline-size:0;min-block-size:32px;padding:4px 8px;
        border:1px solid var(--px-line);border-radius:6px;color:var(--px-ink);
        background:var(--px-bg);font:inherit;font-variant-numeric:tabular-nums;
      }
      .ekd-px-input::placeholder{color:var(--px-muted);opacity:1}
      .ekd-px-input[aria-invalid="true"]{border-color:var(--px-error)}
      .ekd-px-footer{border-block-start:1px solid var(--px-line);display:grid;gap:8px}
      .ekd-px-actions{justify-content:flex-end}
      .ekd-px-primary:not(:disabled){background:var(--px-ink);color:var(--px-bg);border-color:var(--px-ink);font-weight:600}
      .ekd-px-primary:hover:not(:disabled){background:var(--px-muted)}
      .ekd-px-status[data-tone="error"]{color:var(--px-error)}
      .ekd-px-dialog :focus-visible,.ekd-px-row :focus-visible,.ekd-px-toolbar :focus-visible{outline:2px solid var(--px-accent);outline-offset:2px}
      @media(forced-colors:active){.ekd-px-switch [role="switch"]{appearance:auto;inline-size:auto;block-size:auto}}
      @media(max-width:360px){.ekd-px-limits{grid-template-columns:1fr}}
    ` });
    document.head.append(state.style);
  }

  function setText(node, value) {
    if (node.textContent !== value) node.textContent = value;
  }

  function updateSelection(current) {
    const selectable = [...current.rows.values()].filter(row => row.model);
    const count = new Set(selectable.filter(row => row.select.checked).map(row => row.modelId)).size;
    for (const row of current.rows.values()) row.selection.hidden = !current.selecting || !row.model;
    setText(current.count, current.selecting ? `已选 ${count} 个模型` : (current.feedback || ''));
    setText(current.toggle, current.selecting ? '退出批量配置' : '批量配置');
    current.all.hidden = !current.selecting;
    current.apply.hidden = !current.selecting;
    current.all.disabled = selectable.length === 0;
    current.apply.disabled = count === 0;
  }

  function decorateToolbar(current) {
    current.count = el('span', { role: 'status' });
    current.toggle = button('批量配置', () => {
      current.feedback = '';
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
          showMessage(current, shown, '所选模型已变化，请重新选择后再应用模板。');
          return;
        }
        openPanel({ current, button: shown, providerId, ids, officialDirty: hasOfficialModelEdits(current, config, providerId) });
      });
    }, { 'aria-haspopup': 'dialog', 'aria-controls': PANEL_ID, 'aria-expanded': 'false' });
    current.toolbar = el('div', { class: 'ekd-px-toolbar', 'data-ekd-px-mount': '1', role: 'group', 'aria-label': '模型批量配置' }, [current.count, current.toggle, current.all, current.apply]);
    current.table.before(current.toolbar);
  }

  async function withFreshConfig(current, trigger, callback) {
    if (state.panel || trigger.getAttribute('aria-busy') === 'true') return;
    const startGeneration = generation;
    trigger.setAttribute('aria-busy', 'true');
    try {
      const config = await readConfig();
      if (startGeneration !== generation || state.table !== current || !trigger.isConnected || state.panel) return;
      callback(config, resolveProvider(current.form, config));
      render(current, config);
    } catch (error) {
      if (state.table === current && trigger.isConnected && !state.panel) {
        showMessage(current, trigger, `模型配置读取失败。${error.message || '请重试。'}`);
      }
    } finally {
      trigger.removeAttribute('aria-busy');
    }
  }

  function render(current, config) {
    const providerId = resolveProvider(current.form, config);
    if (current.providerId !== providerId) {
      current.selecting = false;
      for (const row of current.rows.values()) row.select.checked = false;
    }
    current.providerId = providerId;
    const managed = providerId.startsWith('managed:');
    current.toolbar.hidden = !providerId || managed;
    const rows = [...current.table.querySelectorAll('.pmt-grid')].filter(row => !row.classList.contains('pmt-head'));
    for (const [node, row] of current.rows) {
      if (!rows.includes(node) || modelInput(node) !== row.input) {
        row.mount.remove();
        current.rows.delete(node);
      }
    }
    for (const node of rows) {
      const input = modelInput(node);
      if (!input) continue;
      let row = current.rows.get(node);
      if (!row) {
        const select = el('input', { type: 'checkbox', onChange: () => updateSelection(current) });
        const selection = el('label', { class: 'ekd-px-options' }, [select]);
        const trigger = button('', () => {
          const idAtClick = input.value.trim();
          void withFreshConfig(current, trigger, (latest, providerId) => {
            if (input.value.trim() !== idAtClick) return;
            const model = savedModel(latest, providerId, idAtClick);
            if (!model) {
              showMessage(current, trigger, '请先在官方供应商表单中保存这个模型。');
              return;
            }
            openPanel({ current, button: trigger, providerId, ids: [idAtClick], model, officialDirty: hasOfficialModelEdits(current, latest, providerId) });
          });
        }, { class: 'ekd-px-button ekd-px-summary', 'data-ekd-px-button': '1', 'data-ekd-enhance': '1', 'aria-haspopup': 'dialog', 'aria-controls': PANEL_ID, 'aria-expanded': 'false' });
        const mount = el('div', { class: 'ekd-px-row', 'data-ekd-px-mount': '1' }, [selection, trigger]);
        node.append(mount);
        row = { input, mount, trigger, selection, select };
        current.rows.set(node, row);
      }
      const id = input.value.trim();
      if (row.modelId !== id) row.select.checked = false;
      row.modelId = id;
      row.model = savedModel(config, providerId, id);
      if (!row.model) row.select.checked = false;
      const text = summary(row.model, managed);
      setText(row.trigger, text);
      row.trigger.setAttribute('title', text);
      row.trigger.setAttribute('aria-label', `${id || '新模型'}的能力与限制，${text}`);
      row.select.setAttribute('aria-label', `选择模型 ${id}`);
    }
    updateSelection(current);
  }

  function nativeInput(event) {
    const current = state.table;
    if (!current || !snapshot || event.target.tagName !== 'INPUT') return;
    const row = event.target.closest('.pmt-grid');
    if (current.rows.get(row)?.input === event.target) render(current, snapshot);
  }

  function showMessage(current, trigger, message) {
    const panel = showDialog(current, trigger, '能力与限制', '');
    panel.body.append(el('p', { class: 'ekd-px-hint', text: message }));
    panel.close.focus();
  }

  function showDialog(current, trigger, title, identity) {
    const status = el('p', { class: 'ekd-px-status', role: 'status' });
    const discard = button('放弃修改', () => closePanel(true));
    discard.hidden = true;
    const close = button('关闭', () => closePanel());
    const actions = el('div', { class: 'ekd-px-actions' }, [discard, close]);
    const body = el('div', { class: 'ekd-px-body' });
    const node = el('dialog', {
      id: PANEL_ID, class: 'ekd-px-dialog', 'aria-labelledby': `${PANEL_ID}-title`, 'aria-modal': 'true',
      'data-ekd-px-mount': '1', onCancel: event => { event.preventDefault(); closePanel(); },
    }, [
      el('header', { class: 'ekd-px-header' }, [
        el('h2', { id: `${PANEL_ID}-title`, text: title }),
        ...(identity ? [el('p', { class: 'ekd-px-identity', text: identity })] : []),
      ]),
      body,
      el('footer', { class: 'ekd-px-footer' }, [status, actions]),
    ]);
    const panel = { node, current, trigger, status, discard, close, actions, body, dirty: () => false, saving: false };
    state.panel = panel;
    current.form.append(node);
    trigger.setAttribute('aria-expanded', 'true');
    node.showModal();
    return panel;
  }

  function statusOf(panel, text, error = false) {
    setText(panel.status, text);
    panel.status.dataset.tone = error ? 'error' : '';
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
    panel.node.close();
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
    } else if (event.key === 'Tab') {
      event.stopImmediatePropagation();
      const controls = [...panel.node.querySelectorAll('button,input')].filter(node => !node.disabled && node.getClientRects().length && (node.type !== 'radio' || node.checked));
      const first = controls[0];
      const last = controls.at(-1);
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last?.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first?.focus();
      }
    }
  }

  function optionsGroup(title, choices) {
    return el('fieldset', { class: 'ekd-px-group' }, [
      el('legend', { text: title }), el('div', { class: 'ekd-px-options' }, choices),
    ]);
  }

  function labelledInput(label, kind, props = {}) {
    const input = el('input', props);
    return { input, label: el('label', { class: `ekd-px-${kind}` }, [input, label]) };
  }

  async function loadSuggestion(providerId, id) {
    const key = `${providerId}/${id}`;
    const [official, publicCatalog] = await Promise.allSettled([
      request('models.list'), request('providers.modelSuggestion', { providerId, model: key }),
    ]);
    const items = official.value?.items;
    const exact = Array.isArray(items) ? items.find(item => item.provider === providerId && (item.id === key || (item.id === id && item.model === id))) : null;
    const metadata = publicCatalog.value?.suggestion;
    const fields = modelFields(exact ?? {});
    const known = new Map();
    for (const [capability] of CAPABILITIES) {
      if (fields.capabilities.has(capability)) known.set(capability, true);
      else if (typeof metadata?.[capability] === 'boolean') known.set(capability, metadata[capability]);
    }
    const thinking = fields.thinking !== 'none' ? fields.thinking : metadata?.thinking === true ? 'toggle' : null;
    const output = Number.isSafeInteger(metadata?.max_output_size) && metadata.max_output_size > 0 ? metadata.max_output_size : null;
    const fromOfficial = fields.thinking !== 'none' || CAPABILITIES.some(([capability]) => fields.capabilities.has(capability));
    const source = [fromOfficial && '官方模型目录', metadata && 'models.dev'].filter(Boolean).join('、');
    if (!known.size && !thinking && !output) {
      const error = official.status === 'rejected' || publicCatalog.status === 'rejected';
      return { empty: error ? '部分目录暂时无法读取，也没有可用的建议。可以重试。' : '目录没有此模型的可用建议。' };
    }
    return { known, thinking, output, adaptive: fields.adaptive, efforts: fields.supportEfforts, source };
  }

  function openPanel({ current, button: trigger, providerId, ids, model, officialDirty }) {
    const batch = !model;
    const managed = providerId.startsWith('managed:');
    const initial = modelFields(model);
    const panel = showDialog(current, trigger, batch ? '批量能力模板' : '能力与限制', batch ? `${providerId} · ${ids.length} 个模型` : `${providerId} / ${ids[0]}`);
    const capabilities = CAPABILITIES.map(([key, label]) => [key, labelledInput(label, 'check', { type: 'checkbox' })]);
    const modes = MODES.map(([mode, label]) => [mode, labelledInput(label, 'mode', { type: 'radio', name: 'ekd-px-thinking', value: mode })]);
    const adaptive = labelledInput('自适应思考', 'switch', { type: 'checkbox', role: 'switch' });
    const efforts = [...new Set([...EFFORTS, ...initial.supportEfforts])].map(value => [value, labelledInput(value, 'check', { type: 'checkbox' })]);
    for (const [key, field] of capabilities) field.input.checked = initial.capabilities.has(key);
    for (const [mode, field] of modes) field.input.checked = mode === initial.thinking;
    adaptive.input.checked = initial.adaptive;
    for (const [value, field] of efforts) field.input.checked = initial.supportEfforts.includes(value);
    const output = el('input', { class: 'ekd-px-input', type: 'text', value: formatSize(initial.output), placeholder: '留空使用默认值', autocomplete: 'off', 'aria-describedby': `${PANEL_ID}-limits` });
    const context = el('input', { class: 'ekd-px-input', type: 'text', value: formatSize(initial.context), placeholder: '如 256K', autocomplete: 'off', 'aria-describedby': `${PANEL_ID}-limits` });
    const allFields = [...capabilities, ...modes, ...efforts].map(([, field]) => field.input).concat(adaptive.input, output, context);
    const inputMode = () => modes.find(([, field]) => field.input.checked)?.[0] || 'none';
    const checkedValues = fields => fields.filter(([, field]) => field.input.checked).map(([value]) => value);
    const readFields = () => ({
      caps: checkedValues(capabilities), thinking: inputMode(), adaptive: adaptive.input.checked,
      efforts: checkedValues(efforts), context: context.value.trim(), output: output.value.trim(),
    });
    const custom = [...initial.capabilities].filter(key => !CAPABILITIES.some(([value]) => value === key));
    let baseline = readFields();
    const save = button(batch ? `保存到 ${ids.length} 个模型` : '保存', () => void saveDraft(), { class: 'ekd-px-button ekd-px-primary' });
    const limitHint = el('p', { id: `${PANEL_ID}-limits`, class: 'ekd-px-hint', text: '支持整数、K 和 M。留空输出上限将使用模型默认值。' });
    const limitIssue = el('p', { class: 'ekd-px-status', 'data-tone': 'error', role: 'status' });
    panel.dirty = () => !managed && JSON.stringify(readFields()) !== JSON.stringify(baseline);

    function patch() {
      const draft = readFields();
      const edit = {};
      if (batch || JSON.stringify(baseline.caps) !== JSON.stringify(draft.caps)) edit.capabilities = draft.caps;
      if (batch || baseline.thinking !== draft.thinking) edit.thinking = draft.thinking;
      if (draft.thinking === 'toggle' && (batch || baseline.adaptive !== draft.adaptive)) edit.adaptive_thinking = draft.adaptive;
      if (draft.thinking !== 'none' && (batch || JSON.stringify(baseline.efforts) !== JSON.stringify(draft.efforts))) edit.support_efforts = draft.efforts;
      if (!batch) {
        const storedContext = parseSize(baseline.context).value ?? initial.context;
        const storedOutput = parseSize(baseline.output).value;
        const nextContext = draft.context !== baseline.context ? parseSize(draft.context) : { value: storedContext };
        const nextOutput = draft.output !== baseline.output ? parseSize(draft.output) : { value: storedOutput };
        if (draft.context !== baseline.context && (nextContext.error || nextContext.value === null)) {
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
      for (const field of allFields) field.disabled = managed || panel.saving;
      adaptive.input.disabled ||= inputMode() !== 'toggle';
      for (const [, field] of efforts) field.input.disabled ||= inputMode() === 'none';
      panel.close.disabled = panel.saving;
      panel.discard.disabled = panel.saving;
      const { edit, contextMessage, outputMessage } = patch();
      context.setAttribute('aria-invalid', contextMessage ? 'true' : 'false');
      output.setAttribute('aria-invalid', outputMessage ? 'true' : 'false');
      setText(limitIssue, contextMessage || outputMessage || '');
      save.disabled = managed || panel.saving || !edit || !Object.keys(edit).length;
      if (showDirty && !panel.saving) {
        panel.discard.hidden = true;
        statusOf(panel, panel.dirty() && !batch ? '有未保存的修改。' : '');
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
        invalidate();
        window.dispatchEvent(new CustomEvent('ekd:config-saved'));
        if (state.panel !== panel) return;
        if (batch) {
          current.selecting = false;
          current.feedback = `已保存 ${ids.length} 个模型。再次编辑官方表单前，请返回列表并重新打开。`;
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
        statusOf(panel, '已保存并核对。再次编辑官方表单前，请返回列表并重新打开。');
      } catch (error) {
        if (state.panel === panel) statusOf(panel, `保存未确认。${error.message || '请重试。'}`, true);
      } finally {
        panel.saving = false;
        if (state.panel === panel) {
          sync(false);
          if (save.disabled && document.activeElement === document.body) panel.close.focus();
        }
      }
    }

    if (!managed && officialDirty) panel.body.append(el('p', { class: 'ekd-px-hint', text: '官方表单有未保存的模型改动。可先保存官方改动，或在此保存后返回列表，避免提交旧表单。' }));
    if (managed) panel.body.append(el('p', { class: 'ekd-px-hint', text: '此模型由内置订阅供应商维护。' }));
    if (batch) panel.body.append(el('p', { class: 'ekd-px-hint', text: '同一模板会替换所选模型的输入、工具与思考配置。' }), el('p', { class: 'ekd-px-identity', text: ids.join('、') }));
    let suggesting = false;
    if (!batch && !managed) {
      const note = el('p', { class: 'ekd-px-hint', role: 'status' });
      const suggest = button('填入目录建议', async () => {
        if (suggesting || panel.saving) return;
        suggesting = true;
        const draftAtRequest = JSON.stringify(readFields());
        setText(note, '正在查询匹配的模型…');
        try {
          const found = await loadSuggestion(providerId, ids[0]);
          if (state.panel !== panel) return;
          if (panel.saving || JSON.stringify(readFields()) !== draftAtRequest) {
            setText(note, '草稿已更改。再次点击可填入目录建议。');
            return;
          }
          if (found.empty) {
            setText(note, found.empty);
            return;
          }
          for (const [key, field] of capabilities) if (found.known.has(key)) field.input.checked = found.known.get(key);
          if (found.thinking) {
            for (const [mode, field] of modes) field.input.checked = found.thinking === mode;
            if (found.adaptive) adaptive.input.checked = true;
            if (found.efforts.length) for (const [value, field] of efforts) field.input.checked = found.efforts.includes(value);
          }
          const draftContext = parseSize(context.value).value ?? initial.context;
          const oversized = found.output !== null && found.output > draftContext;
          if (found.output !== null && !oversized) output.value = formatSize(found.output);
          setText(note, `${found.source}建议已填入，确认后保存。${oversized ? '目录输出长度超过当前上下文，已保留原值。' : ''}`);
          sync();
        } catch {
          if (state.panel === panel) setText(note, '目录暂时无法读取，可以重试。');
        } finally {
          suggesting = false;
        }
      });
      panel.body.append(el('div', { class: 'ekd-px-options' }, [suggest, note]));
    }
    panel.body.append(optionsGroup('输入与调用', capabilities.map(([, field]) => field.label)));
    if (custom.length) panel.body.append(el('p', { class: 'ekd-px-hint', text: `已保留的自定义能力 · ${custom.join('、')}` }));
    panel.body.append(optionsGroup('思考模式', modes.map(([, field]) => field.label).concat(adaptive.label)));
    panel.body.append(optionsGroup('支持的思考强度', efforts.map(([, field]) => field.label)));
    if (!batch) {
      panel.body.append(el('div', { class: 'ekd-px-limits' }, [
        el('label', {}, ['上下文长度', context]),
        el('label', {}, ['最大输出长度', output]),
      ]), limitHint, limitIssue);
    }
    panel.body.addEventListener('input', () => sync());
    if (!managed) panel.actions.append(save);
    sync(false);
    panel.close.focus();
  }

  async function refresh() {
    if (!enabled || removed) return;
    const form = findForm();
    const table = form?.querySelector('.pmt');
    if (state.table?.table !== table || state.table?.form !== form || (state.table && !form.querySelector('.pf-back'))) {
      clearDecorations();
      if (!table || !form.querySelector('.pf-back')) return;
      state.table = { form, table, rows: new Map(), providerId: '', selecting: false };
      decorateToolbar(state.table);
      state.table.toolbar.hidden = true;
    }
    const current = state.table;
    if (!current) return;
    const startGeneration = generation;
    try {
      const hasNewRows = [...current.table.querySelectorAll('.pmt-grid')].some(row => !row.classList.contains('pmt-head') && modelInput(row) && !current.rows.has(row));
      const config = snapshot && !hasNewRows ? snapshot : await readConfig();
      if (startGeneration === generation && state.table === current && findForm() === form) render(current, config);
    } catch {
      // The next official change or interaction retries the read.
    }
  }

  function scan() {
    if (!enabled || removed || timer !== undefined) return;
    timer = setTimeout(() => {
      timer = undefined;
      void refresh();
    }, 0);
  }

  function clearDecorations() {
    closePanel(true);
    state.table = null;
    snapshot = null;
    generation += 1;
    pending = null;
    for (const node of document.querySelectorAll('[data-ekd-px-mount]')) node.remove();
  }

  function observe() {
    observer.observe(document.body, { childList: true, subtree: true });
  }

  function setEnabled(next) {
    if (removed || enabled === Boolean(next)) return;
    enabled = Boolean(next);
    if (enabled) {
      if (observer) observe();
      scan();
    } else {
      observer?.disconnect();
      clearTimeout(timer);
      timer = undefined;
      clearDecorations();
    }
  }

  function install() {
    ensureStyle();
    observer = new MutationObserver(scan);
    window.addEventListener('ekd:config-saved', invalidate);
    window.addEventListener('ekd:official-config-changed', invalidate);
    window.addEventListener('keydown', onKeyDown, true);
    document.addEventListener('input', nativeInput);
    if (enabled) observe();
    scan();
  }

  function remove() {
    setEnabled(false);
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
