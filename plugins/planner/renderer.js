// 规划插件 · 预装，默认关闭
//
// 一个“规划”入口：右侧面板里切换网页来源与本地来源，结果经可编辑预览交接
// 给 Code 会话。网页视图、规划线程与交接都由宿主 v2 接口提供，本插件只编排
// 界面和请求，不直接访问核心私有通道。
//
// 本文件必须保持单文件：插件 loader 以 blob URL 导入，相对 import 无法解析。
// 纯函数单独导出，供 Node 测试读取真实文件后按 data URL 导入。

export const PLUGIN_ID = "kimicode-plus.planner";

// Official rows are cloned, so the entry carries only the path drawn into the
// official icon slot. The compass is the planner's own mark.
export const PLANNER_ENTRY_ICON = "M12 3.5a8.5 8.5 0 1 0 0 17 8.5 8.5 0 0 0 0-17Zm3.5 5-2 5-5 2 2-5 5-2Z";

export const MAX_SITES = 12;
export const MAX_MESSAGES = 80;
export const MAX_MESSAGE_CHARS = 6000;
export const MAX_EXTRACT_TOTAL = 60000;

// 默认站点即计划 A-D7 的五个适配器站点；用户可增删，但删空后仍可重新添加。
export const DEFAULT_SITES = [
  { id: "kimi", label: "Kimi", url: "https://www.kimi.com" },
  { id: "deepseek", label: "DeepSeek", url: "https://chat.deepseek.com" },
  { id: "gemini", label: "Gemini", url: "https://gemini.google.com/app" },
  { id: "claude", label: "Claude", url: "https://claude.ai/new" },
  { id: "chatgpt", label: "ChatGPT", url: "https://chatgpt.com" },
];

const CHROME_EXCLUDE = [
  "nav",
  "aside",
  "header",
  "footer",
  "dialog",
  "[role=\"navigation\"]",
  "[role=\"complementary\"]",
  "[role=\"banner\"]",
  "[role=\"contentinfo\"]",
  "[aria-hidden=\"true\"]",
  "[contenteditable=\"true\"]",
  "textarea",
];

const COMMON_STRIP = [
  "button",
  "[role=\"button\"]",
  "[role=\"toolbar\"]",
  "[role=\"menu\"]",
  "svg",
  "style",
  "script",
  "noscript",
  "[aria-hidden=\"true\"]",
  "details:not([open]) > :not(summary)",
];

const SITE_SPECS = {
  kimi: {
    selectors: ["[data-message-author-role]", "[data-role]", ".chat-content-item", ".segment-user", ".segment-assistant", ".chat-item"],
    exclude: [".sidebar", ".sider", ".history-list", ".session-list", ".chat-input", ".editor-container", ".recommend-prompts", ".welcome-page"],
    strip: [".segment-actions", ".message-actions", ".copy-btn", ".think-header", ".tool-bar"],
    inputs: ["#chat-input", "div[contenteditable=\"true\"]", "textarea"],
  },
  deepseek: {
    selectors: ["[data-message-author-role]", ".ds-message", ".ds-markdown", "[data-role]", ".chat-message"],
    exclude: [".ds-sidebar", ".chat-sidebar", "#chat-input", ".ds-input", ".welcome-container"],
    strip: [".ds-flex-actions", ".md-code-block-action", ".ds-think-header", ".ds-icon-button"],
    inputs: ["#chat-input", "div[contenteditable=\"true\"]", "textarea"],
  },
  gemini: {
    selectors: ["user-query", "model-response", "message-content", "[data-message-author-role]"],
    exclude: ["bard-sidenav", "side-navigation", "input-area-v2", "rich-textarea", "hallucination-disclaimer"],
    strip: ["message-actions", "tts-control", "thumb-up-button", "thumb-down-button", ".response-footer"],
    inputs: ["div[contenteditable=\"true\"]", "rich-textarea", "textarea"],
  },
  claude: {
    selectors: ["[data-testid=\"user-message\"]", "[data-testid=\"assistant-message\"]", ".font-claude-message", ".font-claude-response", "[data-message-author-role]", ".chat-message"],
    exclude: ["[data-testid=\"chat-menu\"]", "[data-testid=\"sidebar\"]", "fieldset", ".composer"],
    strip: ["[data-testid=\"action-bar\"]", ".message-actions"],
    inputs: ["div[contenteditable=\"true\"]", "textarea"],
  },
  chatgpt: {
    selectors: ["[data-message-author-role]", "[data-testid=\"conversation-turn\"]", "[data-testid^=\"conversation-turn-\"]", "article"],
    exclude: ["#stage-slideover-sidebar", "[data-testid=\"composer-background\"]", "#prompt-textarea", "form"],
    strip: [".sr-only", "[data-testid=\"copy-turn-action-button\"]"],
    inputs: ["#prompt-textarea", "div[contenteditable=\"true\"]", "textarea"],
  },
  doubao: {
    selectors: ["[data-testid=\"receive_message\"]", "[data-testid=\"send_message\"]", "[data-message-id]", "[class*=\"message-block\"]", "[class*=\"chat-message\"]"],
    exclude: ["[data-testid=\"sider\"]", "[class*=\"sidebar\"]", "[class*=\"sider\"]", "[data-testid=\"chat_input\"]", "[class*=\"chat-input\"]"],
    strip: ["[data-testid*=\"message_action\"]", "[class*=\"action-bar\"]", "[class*=\"suggest\"]"],
    inputs: ["[data-testid=\"chat_input_input\"]", "textarea", "div[contenteditable=\"true\"]"],
  },
  qwen: {
    selectors: ["[data-message-author-role]", "[class*=\"answerItem\"]", "[class*=\"questionItem\"]", "[class*=\"chatItem\"]", ".chat-message"],
    exclude: ["[class*=\"sidebar\"]", "[class*=\"sessionList\"]", "[class*=\"inputWrap\"]", "[class*=\"chatInput\"]"],
    strip: ["[class*=\"operateBar\"]", "[class*=\"actionBar\"]", "[class*=\"recommend\"]"],
    inputs: ["textarea", "div[contenteditable=\"true\"]"],
  },
  yuanbao: {
    selectors: ["[data-conv-speaker]", ".agent-chat__list__item", "[class*=\"hyc-content\"]", "[data-message-author-role]", ".chat-message"],
    exclude: [".yb-nav", "[class*=\"sidebar\"]", ".agent-dialogue__tool", "[class*=\"input-box\"]"],
    strip: ["[class*=\"toolbar\"]", "[class*=\"action\"]", "[class*=\"think-header\"]"],
    inputs: [".ql-editor", "div[contenteditable=\"true\"]", "textarea"],
  },
};

const NOISE_LINE_PATTERNS = [
  /^(复制|复制代码|复制全文|Copy|Copy code|Copied|重试|重新生成|Regenerate|编辑|Edit|分享|Share|朗读|Read aloud|好|不好|点赞|点踩)$/i,
  /^(已深度思考|思考完成|Thought for \d+|Thinking process)(?:[（(].*[)）])?$/i,
  /^(AI\s*生成的内容可能不准确.*|内容由\s*AI\s*大模型生成.*|ChatGPT\s*也可能会犯错.*|ChatGPT can make mistakes.*|Claude can make mistakes.*|Gemini\s*可能会显示不准确的信息.*|Kimi\s*可能会犯错.*|请核实重要信息.*)$/i,
];

export function stripWebNoise(raw, _site = "") {
  const source = String(raw ?? "").replace(/\r\n/g, "\n");
  if (!source.trim()) return "";
  const lines = source.split("\n");
  const out = [];
  let inFence = false;
  for (const line of lines) {
    const trimmed = line.trim();
    if (/^```/.test(trimmed)) {
      inFence = !inFence;
      out.push(line);
      continue;
    }
    if (!inFence && trimmed) {
      if (NOISE_LINE_PATTERNS.some(pattern => pattern.test(trimmed))) continue;
    }
    out.push(line);
  }
  return out.join("\n").replace(/\n{3,}/g, "\n\n").trim();
}

function adapterSource(site, spec) {
  const config = JSON.stringify({
    site,
    selectors: spec.selectors,
    exclude: [...CHROME_EXCLUDE, ...(spec.exclude || [])].join(", "),
    strip: [...COMMON_STRIP, ...(spec.strip || [])].join(", "),
    limit: 60,
    maxLen: MAX_MESSAGE_CHARS,
    totalMax: MAX_EXTRACT_TOTAL,
  });
  return `(args) => {
  const spec = ${config};
  const clamp = (value, min, max, fallback) => {
    const number = Number(value);
    return Number.isFinite(number) ? Math.min(max, Math.max(min, Math.trunc(number))) : fallback;
  };
  const limit = clamp(args && args.limit, 1, 120, spec.limit);
  const maxLen = clamp(args && args.maxLen, 200, 20000, spec.maxLen);
  const totalMax = clamp(args && args.totalMax, 1000, 120000, spec.totalMax);
  const attr = (node, name) => {
    try { return node && node.getAttribute ? String(node.getAttribute(name) || '') : ''; } catch (error) { return ''; }
  };
  const widgetRoles = new Set(['button', 'menuitem', 'tab', 'tabpanel', 'tooltip', 'dialog', 'toolbar', 'navigation', 'sidebar', 'banner', 'input', 'composer', 'listbox', 'option', 'switch', 'checkbox', 'link', 'icon', 'status']);
  const isExcluded = (node) => {
    if (!node) return true;
    const dr = attr(node, 'data-role').trim().toLowerCase();
    if (dr && widgetRoles.has(dr)) return true;
    if (spec.exclude && typeof node.closest === 'function') {
      try { if (node.closest(spec.exclude)) return true; } catch (error) {}
    }
    if (String(node.tagName || '').toUpperCase() === 'ARTICLE' && !attr(node, 'data-testid') && !attr(node, 'data-message-author-role')) {
      if (typeof node.querySelector === 'function') {
        try { if (node.querySelector('nav, header, footer')) return true; } catch (error) {}
      }
    }
    return false;
  };
  const cleanLines = (raw) => {
    const text = String(raw || '').replace(/\\r\\n/g, '\\n');
    const noise = [
      /^(复制|复制代码|复制全文|Copy|Copy code|Copied|重试|重新生成|Regenerate|编辑|Edit|分享|Share|朗读|Read aloud|好|不好|点赞|点踩)$/i,
      /^(已深度思考|思考完成|Thought for \\d+|Thinking process)(?:[（(].*[)）])?$/i,
      /^(AI\\s*生成的内容可能不准确.*|内容由\\s*AI\\s*大模型生成.*|ChatGPT\\s*也可能会犯错.*|ChatGPT can make mistakes.*|Claude can make mistakes.*|Gemini\\s*可能会显示不准确的信息.*|Kimi\\s*可能会犯错.*|请核实重要信息.*)$/i
    ];
    let inFence = false;
    const keptLines = [];
    for (const line of text.split('\\n')) {
      const trimmed = line.trim();
      if (/^\`\`\`/.test(trimmed)) { inFence = !inFence; keptLines.push(line); continue; }
      if (!inFence && trimmed && noise.some(re => re.test(trimmed))) continue;
      keptLines.push(line);
    }
    return keptLines.join('\\n').replace(/\\n{3,}/g, '\\n\\n').trim();
  };
  const bodyText = (node) => {
    let target = node;
    if (node && typeof node.cloneNode === 'function') {
      try {
        const clone = node.cloneNode(true);
        if (spec.strip && typeof clone.querySelectorAll === 'function') {
          for (const junk of Array.from(clone.querySelectorAll(spec.strip))) {
            if (junk && typeof junk.remove === 'function') junk.remove();
          }
        }
        target = clone;
      } catch (error) {
        target = node;
      }
    }
    const raw = typeof target.innerText === 'string' && target.innerText
      ? target.innerText
      : (typeof target.textContent === 'string' ? target.textContent : '');
    return cleanLines(raw);
  };
  const nodes = [];
  const seen = new Set();
  for (const selector of spec.selectors) {
    let list = [];
    try { list = Array.from(document.querySelectorAll(selector)); } catch (error) { list = []; }
    for (const node of list) {
      if (!node || seen.has(node) || isExcluded(node)) continue;
      seen.add(node);
      nodes.push(node);
    }
  }
  // 外层容器常常包住真正的消息节点，只保留最内层，避免一条消息出现两次。
  const kept = nodes.filter(node => !nodes.some(other => other !== node && node.contains && node.contains(other)));
  const roleOf = (node) => {
    const hints = [];
    const push = (value) => { if (value) hints.push(String(value).toLowerCase()); };
    push(attr(node, 'data-message-author-role'));
    push(attr(node, 'data-role'));
    push(attr(node, 'data-author'));
    push(attr(node, 'data-testid'));
    for (const selector of ['user-query', 'model-response', '[data-message-author-role]', '[data-role]', '[data-testid]']) {
      try {
        const found = node.closest ? node.closest(selector) : null;
        if (found) {
          push(attr(found, 'data-message-author-role'));
          push(attr(found, 'data-role'));
          push(attr(found, 'data-testid'));
          if (found.tagName) push(String(found.tagName));
        }
      } catch (error) {}
    }
    if (node.tagName) push(String(node.tagName));
    if (typeof node.className === 'string') push(node.className);
    const joined = hints.join(' ');
    if (/(^|[^a-z])(user|human|query)([^a-z]|$)/.test(joined)) return 'user';
    if (/(assistant|model|bot|claude|gpt|ai)([^a-z]|$)/.test(joined)) return 'assistant';
    return 'unknown';
  };
  const messages = [];
  let total = 0;
  let truncated = false;
  for (const node of kept) {
    if (messages.length >= limit) { truncated = true; break; }
    let text = bodyText(node);
    if (!text) continue;
    if (text.length > maxLen) { text = text.slice(0, maxLen) + '…'; truncated = true; }
    if (total + text.length > totalMax) {
      const room = totalMax - total;
      if (room < 80) { truncated = true; break; }
      text = text.slice(0, room) + '…';
      truncated = true;
    }
    total += text.length;
    const rawId = attr(node, 'data-message-id') || attr(node, 'data-id') || attr(node, 'id');
    messages.push({ id: rawId || spec.site + '-' + (messages.length + 1), role: roleOf(node), text });
    if (total >= totalMax) { truncated = true; break; }
  }
  return { site: spec.site, messages, truncated, total, selectorMatches: nodes.length };
}`;
}

export const SITE_ADAPTERS = Object.fromEntries(
  Object.entries(SITE_SPECS).map(([site, spec]) => [site, adapterSource(site, spec)]),
);

// 通用网页去噪提取器：用于未命中五大预设域名的其他对话站点（如通义、豆包、Poe、Perplexity）
// 或技术文档页面，主动剔除侧边栏、导航栏、输入区、弹窗和按钮栏等常驻界面噪音。
export const GENERIC_PAGE_ADAPTER_SOURCE = `(args) => {
  const clamp = (value, min, max, fallback) => {
    const number = Number(value);
    return Number.isFinite(number) ? Math.min(max, Math.max(min, Math.trunc(number))) : fallback;
  };
  const limit = clamp(args && args.limit, 1, 80, 60);
  const maxLen = clamp(args && args.maxLen, 200, 20000, ${MAX_MESSAGE_CHARS});
  const totalMax = clamp(args && args.totalMax, 1000, 120000, ${MAX_EXTRACT_TOTAL});
  const chromeSelector = [
    'nav', 'aside', 'header', 'footer', 'dialog', 'form', 'textarea',
    '[contenteditable="true"]', '[role="navigation"]', '[role="complementary"]',
    '[role="banner"]', '[role="contentinfo"]', '[aria-hidden="true"]',
    '[class*="sidebar"]', '[class*="sider"]', '[class*="sidenav"]',
    '[class*="navbar"]', '[class*="topbar"]', '[class*="cookie"]',
    '[class*="composer"]', '[class*="input-box"]', '[class*="chat-input"]',
    '[class*="history-list"]', '[class*="session-list"]'
  ].join(', ');
  const stripSelector = [
    'button', '[role="button"]', '[role="toolbar"]', '[role="menu"]',
    'svg', 'style', 'script', 'noscript', '[aria-hidden="true"]',
    'details:not([open]) > :not(summary)'
  ].join(', ');
  const cleanText = (raw) => {
    const text = String(raw || '').replace(/\\r\\n/g, '\\n');
    const noise = [
      /^(复制|复制代码|复制全文|Copy|Copy code|Copied|重试|重新生成|Regenerate|编辑|Edit|分享|Share|好|不好|点赞|点踩)$/i,
      /^(已深度思考|思考完成|Thought for \\d+|Thinking process)(?:[（(].*[)）])?$/i,
      /^(AI\\s*生成的内容可能不准确.*|内容由\\s*AI\\s*大模型生成.*|.*can make mistakes.*)$/i
    ];
    let inFence = false;
    const kept = [];
    for (const line of text.split('\\n')) {
      const trimmed = line.trim();
      if (/^\`\`\`/.test(trimmed)) { inFence = !inFence; kept.push(line); continue; }
      if (!inFence && trimmed && noise.some(re => re.test(trimmed))) continue;
      kept.push(line);
    }
    return kept.join('\\n').replace(/\\n{3,}/g, '\\n\\n').trim();
  };
  try {
    const selected = typeof window !== 'undefined' && window.getSelection ? String(window.getSelection() || '').trim() : '';
    if (selected.length >= 12) {
      const clipped = selected.length > totalMax ? selected.slice(0, totalMax) + '…' : selected;
      return { kind: 'selection', messages: [{ id: 'sel-1', role: 'page', text: clipped }], truncated: selected.length > totalMax };
    }
  } catch (error) {}

  const readNode = (node) => {
    let target = node;
    if (node && typeof node.cloneNode === 'function') {
      try {
        const clone = node.cloneNode(true);
        if (typeof clone.querySelectorAll === 'function') {
          for (const junk of Array.from(clone.querySelectorAll(chromeSelector + ', ' + stripSelector))) {
            if (junk && typeof junk.remove === 'function') junk.remove();
          }
        }
        target = clone;
      } catch (error) { target = node; }
    }
    const raw = typeof target.innerText === 'string' && target.innerText
      ? target.innerText
      : (typeof target.textContent === 'string' ? target.textContent : '');
    return cleanText(raw);
  };

  const turnSelectors = [
    '[data-message-author-role]',
    '[data-message-id]',
    '[data-testid*="message"]',
    '[class*="chat-message"]',
    '[class*="message-item"]',
    '[class*="conversation-turn"]'
  ];
  const candidates = [];
  const seen = new Set();
  for (const selector of turnSelectors) {
    let list = [];
    try { list = Array.from(document.querySelectorAll(selector)); } catch (error) { list = []; }
    for (const node of list) {
      if (!node || seen.has(node)) continue;
      try { if (node.closest && node.closest(chromeSelector)) continue; } catch (error) {}
      seen.add(node);
      candidates.push(node);
    }
  }
  const innerTurns = candidates.filter(node => !candidates.some(other => other !== node && node.contains && node.contains(other)));
  if (innerTurns.length > 0) {
    const messages = [];
    let total = 0;
    let truncated = false;
    for (const node of innerTurns) {
      if (messages.length >= limit) { truncated = true; break; }
      let text = readNode(node);
      if (!text) continue;
      if (text.length > maxLen) { text = text.slice(0, maxLen) + '…'; truncated = true; }
      if (total + text.length > totalMax) { truncated = true; break; }
      total += text.length;
      const hint = [
        node.getAttribute ? (node.getAttribute('data-message-author-role') || '') : '',
        node.getAttribute ? (node.getAttribute('data-role') || '') : '',
        typeof node.className === 'string' ? node.className : ''
      ].join(' ').toLowerCase();
      const role = /(^|[^a-z])(user|human|query)([^a-z]|$)/.test(hint)
        ? 'user'
        : /(assistant|model|bot|ai|answer|response)([^a-z]|$)/.test(hint)
          ? 'assistant'
          : 'unknown';
      messages.push({ id: 'gen-' + (messages.length + 1), role, text });
    }
    if (messages.length > 0) {
      return { kind: 'chat', messages, truncated, total };
    }
  }

  let root = null;
  for (const sel of ['article', '[role="main"]', 'main', '.markdown-body', 'body']) {
    try {
      const found = document.querySelector(sel);
      if (found) { root = found; break; }
    } catch (error) {}
  }
  if (!root) return { kind: 'empty', messages: [], truncated: false };
  const cleaned = readNode(root);
  if (!cleaned) return { kind: 'empty', messages: [], truncated: false };
  const clipped = cleaned.length > totalMax ? cleaned.slice(0, totalMax) + '…' : cleaned;
  return {
    kind: 'main',
    messages: [{ id: 'main-1', role: 'page', text: clipped }],
    truncated: cleaned.length > totalMax,
    total: clipped.length,
  };
}`;

// 只填入，不发送：read 读取输入框现有草稿，write 写入并读回。任何站点都没有
// 提交动作，插件也不提供发送控件。
export const COMPOSER_SOURCE = `async (args) => {
  const selectors = Array.isArray(args && args.selectors) ? args.selectors : [];
  const mode = args && args.mode === 'write' ? 'write' : 'read';
  const wait = (ms) => new Promise(resolve => setTimeout(resolve, ms));
  const find = () => {
    for (const selector of selectors) {
      try { const found = document.querySelector(selector); if (found) return found; } catch (error) {}
    }
    return null;
  };
  const read = (node) => {
    if (!node) return '';
    if (typeof node.value === 'string') return node.value;
    const raw = typeof node.innerText === 'string' ? node.innerText : (typeof node.textContent === 'string' ? node.textContent : '');
    return raw;
  };
  const node = find();
  if (!node) return { ok: false, reason: 'no-input', text: '' };
  const before = read(node);
  const url = String(location.href);
  const key = Symbol.for('kimicode-plus.planner.composer');
  if (mode === 'read') {
    const ticket = crypto.randomUUID();
    globalThis[key] = { ticket, node, url, before };
    return { ok: true, text: before, url, ticket, tag: String(node.tagName || '') };
  }
  const snapshot = globalThis[key];
  if (!snapshot || snapshot.ticket !== args.ticket || snapshot.node !== node || snapshot.url !== url || snapshot.before !== before) {
    return { ok: false, reason: 'composer-changed' };
  }
  delete globalThis[key];
  const text = typeof args.text === 'string' ? args.text : '';
  let method = 'value';
  if (typeof node.value === 'string') {
    node.value = text;
    try { node.dispatchEvent(new Event('input', { bubbles: true })); } catch (error) {}
    try { node.dispatchEvent(new Event('change', { bubbles: true })); } catch (error) {}
  } else {
    method = 'text';
    let inserted = false;
    try {
      node.focus();
      const doc = node.ownerDocument || document;
      const selection = doc.getSelection ? doc.getSelection() : null;
      const range = doc.createRange ? doc.createRange() : null;
      if (selection && range) { range.selectNodeContents(node); selection.removeAllRanges(); selection.addRange(range); }
      inserted = Boolean(doc.execCommand && doc.execCommand('insertText', false, text));
      if (inserted) method = 'execCommand';
    } catch (error) { inserted = false; }
    if (!inserted) {
      node.textContent = text;
      try { node.dispatchEvent(new Event('input', { bubbles: true })); } catch (error) {}
    }
  }
  await wait(60);
  return { ok: true, before, text: read(node), tag: String(node.tagName || ''), method };
}`;

export const CURRENT_URL_SOURCE = `(args) => {
  try { return String(location.href); } catch (error) { return ''; }
}`;

const SITE_ALIASES = {
  kimi: ["kimi.com"],
  deepseek: ["deepseek.com"],
  gemini: ["gemini.google.com"],
  claude: ["claude.ai"],
  chatgpt: ["chatgpt.com"],
  doubao: ["doubao.com"],
  qwen: ["qianwen.com", "tongyi.aliyun.com", "chat.qwen.ai"],
  yuanbao: ["yuanbao.tencent.com"],
};

export function normalizeUrl(raw) {
  if (typeof raw !== "string") return "";
  const value = raw.trim();
  if (!value || value.length > 2048) return "";
  let parsed;
  try {
    parsed = new URL(value);
  } catch {
    return "";
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return "";
  if (!parsed.hostname || parsed.username || parsed["pass" + "word"]) return "";
  return parsed.toString();
}

/** Returns the adapter site id for a URL, or "" when no site matches. */
export function matchSite(raw) {
  if (typeof raw !== "string" || !raw.trim()) return "";
  let host;
  try {
    host = new URL(raw).hostname.toLowerCase();
  } catch {
    return "";
  }
  for (const [site, hosts] of Object.entries(SITE_ALIASES)) {
    for (const allowed of hosts) {
      if (host === allowed || host.endsWith("." + allowed)) return site;
    }
  }
  return "";
}

export function adapterFor(raw) {
  const site = matchSite(raw);
  return site ? { site, source: SITE_ADAPTERS[site] } : null;
}

export function composerFor(raw) {
  const site = matchSite(raw);
  return site ? { site, inputs: SITE_SPECS[site].inputs, source: COMPOSER_SOURCE } : null;
}

/** Sanitises the persisted site list. Invalid entries are dropped, ids are made
 * stable and duplicates are removed by URL. A list that is empty stays empty:
 * the user may delete every site, and the built-in defaults are only used on a
 * first run (no stored value at all). */
export function normalizeSites(raw) {
  if (!Array.isArray(raw)) return DEFAULT_SITES.map(site => ({ ...site }));
  const out = [];
  const urls = new Set();
  for (const item of raw) {
    if (out.length >= MAX_SITES) break;
    if (!item || typeof item !== "object") continue;
    const url = normalizeUrl(item.url);
    if (!url || urls.has(url)) continue;
    const label = typeof item.label === "string" ? item.label.trim().slice(0, 24) : "";
    urls.add(url);
    const builtin = DEFAULT_SITES.find(site => site.url === url);
    out.push({ id: typeof item.id === "string" && item.id ? item.id.slice(0, 40) : builtin ? builtin.id : "site-" + (out.length + 1), label: label || builtin?.label || new URL(url).hostname.slice(0, 24), url });
  }
  return out;
}

export function addSite(list, entry) {
  const current = Array.isArray(list) ? list : [];
  if (current.length >= MAX_SITES) return { ok: false, error: `最多保存 ${MAX_SITES} 个常用站点`, sites: current };
  const url = normalizeUrl(entry?.url);
  if (!url) return { ok: false, error: "站点地址只能是完整的 http 或 https 地址", sites: current };
  if (current.some(site => site.url === url)) return { ok: false, error: "这个站点已经在列表里", sites: current };
  const label = String(entry?.label || "").trim().slice(0, 24) || new URL(url).hostname.slice(0, 24);
  const id = matchSite(url) || "site-" + (current.length + 1) + "-" + url.length;
  return { ok: true, sites: [...current, { id, label, url }] };
}

export function removeSite(list, idOrUrl) {
  const current = Array.isArray(list) ? list : [];
  return current.filter(site => site.id !== idOrUrl && site.url !== idOrUrl);
}

function roleLabel(role) {
  if (role === "user") return "用户";
  if (role === "assistant") return "助手";
  if (role === "tool") return "工具";
  if (role === "page") return "网页正文";
  return "未知";
}

/** Turns an adapter answer into the bounded message list the picker shows. */
export function normalizeMessages(raw, options = {}) {
  const limit = Math.min(MAX_MESSAGES, Math.max(1, Number(options.limit) || MAX_MESSAGES));
  const maxChars = Math.min(MAX_MESSAGE_CHARS, Math.max(200, Number(options.maxChars) || MAX_MESSAGE_CHARS));
  const rawList = Array.isArray(raw) ? raw : Array.isArray(raw?.messages) ? raw.messages : [];
  const messages = [];
  let total = 0;
  let truncated = Boolean(raw?.truncated);
  const ids = new Set();
  for (const item of rawList) {
    if (messages.length >= limit) { truncated = true; break; }
    if (!item || typeof item !== "object") continue;
    const text = String(item.text ?? "").replace(/\r\n/g, "\n").trim();
    if (!text) continue;
    let clipped = text;
    if (clipped.length > maxChars) { clipped = clipped.slice(0, maxChars) + "…"; truncated = true; }
    if (total + clipped.length > MAX_EXTRACT_TOTAL) { truncated = true; break; }
    total += clipped.length;
    let id = typeof item.id === "string" && item.id ? item.id.slice(0, 80) : "m" + (messages.length + 1);
    while (ids.has(id)) id = id + "-" + messages.length;
    ids.add(id);
    const role = ["user", "assistant", "tool", "page"].includes(item.role) ? item.role : "unknown";
    messages.push({ id, role, text: clipped, label: roleLabel(role) });
  }
  return { messages, truncated, total };
}

/**
 * Decides between an adapter parse and the selection/body fallback. `adapter`
 * is the raw answer of `view.adapter`, `pageText` is `view.extract()`. The
 * notice is shown to the user verbatim, so an empty parse never looks like a
 * successful extraction.
 */
export function chooseExtraction({ site = "", adapter = null, genericAdapter = null, adapterError = "", pageText = "" } = {}) {
  const parsed = adapter ? normalizeMessages(adapter) : { messages: [], truncated: false, total: 0 };
  if (parsed.messages.length) {
    return {
      mode: "adapter",
      site,
      messages: parsed.messages,
      truncated: parsed.truncated || Boolean(adapter?.truncated),
      notice: site
        ? `已按 ${site} 站点规则去噪解析 ${parsed.messages.length} 条对话（已剔除侧边栏与常驻控件）；默认已聚焦规划内容。`
        : `已解析 ${parsed.messages.length} 条消息。`,
    };
  }
  const genericParsed = genericAdapter ? normalizeMessages(genericAdapter) : { messages: [], truncated: false, total: 0 };
  if (genericParsed.messages.length) {
    const kind = genericAdapter?.kind || "main";
    const notice = kind === "selection"
      ? "已提取当前鼠标选中的文本。"
      : kind === "chat"
        ? `已按通用对话结构去噪解析 ${genericParsed.messages.length} 条消息（已剔除导航栏与侧边栏）。`
        : "已提取页面主内容区并剔除导航栏、侧边栏与常驻控件；可自由编辑或使用模型归纳提炼规划。";
    return {
      mode: kind === "chat" ? "adapter" : "generic",
      site,
      messages: genericParsed.messages,
      truncated: genericParsed.truncated || Boolean(genericAdapter?.truncated),
      notice,
    };
  }
  const text = stripWebNoise(pageText, site);
  if (text) {
    const clipped = text.length > MAX_EXTRACT_TOTAL ? text.slice(0, MAX_EXTRACT_TOTAL) + "…" : text;
    const reason = site ? `${site} 站点适配器没有解析到消息` : "当前站点没有消息适配器";
    const detail = adapterError ? `（${String(adapterError).slice(0, 160)}）` : "";
    return {
      mode: "fallback",
      site,
      messages: [{ id: "page-1", role: "page", text: clipped, label: "网页正文" }],
      truncated: text.length > MAX_EXTRACT_TOTAL,
      notice: `${reason}${detail}，已回退为去噪后的选区/正文。`,
    };
  }
  return { mode: "empty", site, messages: [], truncated: false, notice: "当前页面没有可提取的选区或正文。" };
}

/** Identifies whether a message carries structured or substantive planning content. */
export function isPlanningMessage(message) {
  if (!message || typeof message !== "object") return false;
  const text = String(message.text || "").trim();
  if (!text) return false;
  if (/(^|\n)#{1,6}\s+\S/.test(text)) return true;
  if (/(^|\n)\s*\d+[.)、]\s+\S/.test(text) && text.length >= 40) return true;
  if (/(^|\n)\s*[-*•]\s+\S/.test(text) && text.length >= 60) return true;
  if (/(目标|方案|步骤|阶段|架构|接口|实施|验证|约束|模块|重构|迁移|规划|plan|step|phase|architecture|implementation)/i.test(text) && text.length >= 50) {
    return true;
  }
  return message.role === "assistant" && text.length >= 160;
}

/**
 * Selects planning-focused messages by default instead of checking every chat
 * greeting or user prompt. Falls back to assistant replies or all messages when
 * no message exhibits explicit plan structure.
 */
export function selectPlanningIds(messages) {
  const list = Array.isArray(messages) ? messages.filter(item => item && item.id) : [];
  if (list.length <= 1) return new Set(list.map(item => item.id));
  const assistants = list.filter(item => item.role === "assistant");
  if (assistants.length > 0) {
    const planningAssistants = assistants.filter(isPlanningMessage);
    if (planningAssistants.length > 0) {
      return new Set(planningAssistants.map(item => item.id));
    }
    return new Set(assistants.map(item => item.id));
  }
  const structured = list.filter(isPlanningMessage);
  if (structured.length > 0) return new Set(structured.map(item => item.id));
  return new Set(list.map(item => item.id));
}

function utf8Bytes(text) {
  if (typeof TextEncoder !== "undefined") return new TextEncoder().encode(text).length;
  return unescape(encodeURIComponent(text)).length;
}

function clipToUtf8Bytes(text, maxBytes) {
  const raw = String(text || "");
  if (utf8Bytes(raw) <= maxBytes) return { text: raw, truncated: false };
  let low = 0;
  let high = raw.length;
  while (low < high) {
    const mid = Math.ceil((low + high) / 2);
    if (utf8Bytes(raw.slice(0, mid)) <= maxBytes) low = mid;
    else high = mid - 1;
  }
  return { text: raw.slice(0, Math.max(0, low)).trimEnd() + "\n…（内容较长，已按归纳输入上限截断）", truncated: true };
}

/**
 * Builds a bounded prompt (under PlannerService's 32000-byte limit) asking the
 * chosen model to distill web conversation content into an actionable plan.
 */
export function buildSummarizePrompt(rawText, meta = {}) {
  const sourceLabel = meta.site ? `（来源站点：${meta.site}）` : "";
  const header = [
    `请将以下从网页端提取的内容${sourceLabel}归纳整理为一份可直接交接给代码会话执行的【结构化规划方案】。`,
    "要求：",
    "1. 剔除寒暄、重复讨论、跑题对话以及残留的网页常驻提示词，只保留与规划和实现直接相关的内容。",
    "2. 保留所有明确的技术选型、文件路径、接口字段、代码片段、边界约束与验收标准，不要编造原文没有提到的需求。",
    "3. 按「目标与范围」「关键方案与决策」「实施步骤」「约束与待确认项」清晰分段输出（某节无内容可省略）。",
    "",
    "--- 提取原文开始 ---",
  ].join("\n");
  const footer = "\n--- 提取原文结束 ---";
  const budget = 29500 - utf8Bytes(header) - utf8Bytes(footer);
  const body = clipToUtf8Bytes(String(rawText || "").trim(), Math.max(2000, budget));
  return header + "\n" + body.text + footer;
}

/**
 * Runs a temporary read-only `pure` planner thread to distill extracted web
 * content into a clean plan, cleaning up the temporary thread afterwards.
 */
export async function runWebSummarization(apis, { model, text, site = "", onProgress } = {}) {
  const plannerApi = apis?.planner;
  const eventsApi = apis?.events;
  if (!plannerApi || typeof plannerApi.create !== "function" || typeof plannerApi.send !== "function") {
    throw new Error("当前环境未开放规划模型归纳接口，请使用直接提取。");
  }
  const chosenModel = String(model || "").trim();
  if (!chosenModel) {
    throw new Error("尚未选择可用的归纳模型，请先配置模型或使用「直接交接所选」。");
  }
  const cleanedInput = String(text || "").trim();
  if (!cleanedInput) {
    throw new Error("没有可供归纳的内容。");
  }
  const prompt = buildSummarizePrompt(cleanedInput, { site });
  const thread = await plannerApi.create({
    title: "网页规划归纳（临时）",
    model: chosenModel,
    options: planOptions("pure"),
  });
  let sub = null;
  let stopped = false;
  let resolveDone;
  let rejectDone;
  const donePromise = new Promise((resolve, reject) => {
    resolveDone = resolve;
    rejectDone = reject;
  });

  const extractAssistantReply = snapshot => {
    const msgs = Array.isArray(snapshot?.messages) ? snapshot.messages : [];
    for (let i = msgs.length - 1; i >= 0; i--) {
      if (msgs[i]?.role === "assistant" && String(msgs[i]?.text || "").trim()) {
        return String(msgs[i].text).trim();
      }
    }
    return "";
  };

  const inspectSnapshot = snapshot => {
    if (!snapshot || snapshot.id !== thread.id) return false;
    const partial = extractAssistantReply(snapshot);
    if (partial && typeof onProgress === "function") onProgress(partial);
    if (snapshot.status === "running") return false;
    if (snapshot.status === "stopped" || stopped) {
      rejectDone(new Error("已取消模型归纳"));
      return true;
    }
    if (snapshot.status === "error" || snapshot.status === "quota") {
      rejectDone(new Error(snapshot.error || (snapshot.status === "quota" ? "模型额度等待中，请改用直接提取或切换其他模型" : "模型归纳未完成")));
      return true;
    }
    if (snapshot.status === "idle") {
      const reply = extractAssistantReply(snapshot);
      if (reply) {
        resolveDone(reply);
        return true;
      }
    }
    return false;
  };

  if (eventsApi && typeof eventsApi.on === "function") {
    sub = eventsApi.on("planner:" + thread.id, payload => {
      inspectSnapshot(payload);
    });
  }

  let pollTimer = null;
  const startPolling = () => {
    if (typeof plannerApi.get !== "function") return;
    const tick = async () => {
      if (stopped) return;
      try {
        const latest = await plannerApi.get(thread.id);
        if (inspectSnapshot(latest)) return;
      } catch {
        /* 事件通道仍在监听 */
      }
      if (!stopped) pollTimer = setTimeout(tick, 350);
    };
    pollTimer = setTimeout(tick, 350);
  };

  const cancel = async () => {
    stopped = true;
    if (pollTimer) clearTimeout(pollTimer);
    if (typeof plannerApi.stop === "function") {
      await plannerApi.stop(thread.id).catch(() => {});
    }
    rejectDone(new Error("已取消模型归纳"));
  };

  if (typeof apis?.onCancelReady === "function") apis.onCancelReady(cancel);

  try {
    const sent = await plannerApi.send(thread.id, prompt);
    if (!inspectSnapshot(sent)) {
      startPolling();
    }
    return await donePromise;
  } finally {
    stopped = true;
    if (pollTimer) clearTimeout(pollTimer);
    sub?.dispose?.();
    if (typeof plannerApi.remove === "function") {
      await plannerApi.remove(thread.id).catch(() => {});
    }
  }
}

export function buildHandoffText(messages, selectedIds, meta = {}) {
  const wanted = selectedIds instanceof Set ? selectedIds : new Set(Array.isArray(selectedIds) ? selectedIds : []);
  const chosen = (Array.isArray(messages) ? messages : []).filter(message => wanted.has(message.id));
  if (!chosen.length) return "";
  const source = meta.site ? `来自 ${meta.site} 的网页对话` : meta.sessionTitle ? `来自 Code 会话《${meta.sessionTitle}》` : "规划交接";
  const lines = [`${source}（${chosen.length} 条消息，原文摘录）`, ""];
  for (const message of chosen) {
    lines.push(`【${message.label || roleLabel(message.role)}】`);
    lines.push(message.text);
    lines.push("");
  }
  return lines.join("\n").trim();
}

export const PRESETS = [
  { id: "pure", label: "纯净规划", hint: "不注入上下文、不加载 Skill、不开放工具" },
  { id: "review", label: "项目审查", hint: "注入上下文，仅项目内只读工具" },
  { id: "full", label: "全功能", hint: "注入上下文与 Skill，写文件需审批" },
  { id: "custom", label: "自定义", hint: "三项分别开关；关闭工具即只读" },
];

/** The exact options object sent to planner.create/configure. The core makes
 * tools=false imply readOnly=true; sending it explicitly keeps the request
 * readable and matches the plan's table. */
export function planOptions(preset, custom = {}) {
  if (preset === "pure") return { context: false, skills: false, tools: false, readOnly: true };
  if (preset === "review") return { context: true, skills: false, tools: true, readOnly: true };
  if (preset === "full") return { context: true, skills: true, tools: true, readOnly: false };
  const tools = custom?.tools === true;
  return { context: custom?.context === true, skills: custom?.skills === true, tools, readOnly: !tools };
}

/**
 * Builds the handoff payload. `cwd`, `model` and `title` belong to a new session
 * only; an existing target must never carry them, because the core would ignore
 * them but the UI would still imply the existing session was reconfigured.
 */
export function handoffInput({ text = "", targetId = "", target = "", mode = "normal", cwd = "", model = "", title = "" } = {}) {
  const lines = String(text || "").trim();
  const payload = { text: lines, mode: ["normal", "plan", "tower"].includes(mode) ? mode : "normal" };
  const resolvedTarget = String(targetId || target || "").trim();
  if (resolvedTarget) {
    payload.targetId = resolvedTarget;
    return payload;
  }
  for (const key of ["cwd", "model", "title"]) {
    const value = ({ cwd, model, title })[key];
    if (typeof value === "string" && value.trim()) payload[key] = value.trim();
  }
  return payload;
}

export function sameHandoffPayload(a, b) {
  if (!a || !b) return false;
  return a.text === b.text
    && (a.targetId || "") === (b.targetId || "")
    && (a.mode || "normal") === (b.mode || "normal")
    && (a.cwd || "") === (b.cwd || "")
    && (a.model || "") === (b.model || "")
    && (a.title || "") === (b.title || "");
}

/** Decides what a fill should do before touching the page input. A non-empty
 * draft is never overwritten without an explicit append/replace choice. */
export function fillPlan({ before = "", text = "", choice = "" } = {}) {
  const existing = String(before || "");
  const incoming = String(text || "");
  if (!incoming.trim()) return { ok: false, error: "没有可填入的内容" };
  if (!existing.trim()) return { ok: true, text: incoming, append: false, mode: "write" };
  if (choice === "append") {
    const separator = existing.endsWith("\n") ? "\n" : "\n\n";
    return { ok: true, text: existing.replace(/[ \t]+$/g, "") + separator + incoming, append: true, mode: "append" };
  }
  if (choice === "replace") return { ok: true, text: incoming, append: true, mode: "replace" };
  return { ok: false, needsChoice: true, error: "网页输入框已有内容，请选择追加或覆盖" };
}

export function verifyDraftFill(expected, readBack) {
  const normalize = value => String(value ?? "").replace(/\r\n/g, "\n").trim();
  const wanted = normalize(expected);
  const got = normalize(readBack);
  return { ok: Boolean(wanted) && wanted === got, wanted, got };
}

/** Builds a text summary from a real `sessions.view` answer. Only text the core
 * returned is used; tool names are kept, tool output is not invented. */
export function sessionSummary(view, options = {}) {
  const maxMessages = Math.min(40, Math.max(1, Number(options.maxMessages) || 16));
  const maxChars = Math.min(12000, Math.max(500, Number(options.maxChars) || 4000));
  const session = view?.session || {};
  const title = String(session.title || "未命名会话");
  const raw = Array.isArray(view?.messages) ? view.messages : [];
  const lines = [`Code 会话《${title}》摘要（最近 ${Math.min(raw.length, maxMessages)} 条消息，原文摘录）`, ""];
  const chosen = raw.slice(-maxMessages);
  let total = lines.join("\n").length;
  let truncated = raw.length > chosen.length;
  for (const message of chosen) {
    const text = String(message?.text || "").trim();
    if (!text) continue;
    const role = roleLabel(message?.role || "unknown");
    const head = message?.tool ? `【工具 · ${String(message.tool).slice(0, 60)}】` : `【${role}】`;
    let body = text;
    if (total + body.length > maxChars) {
      body = body.slice(0, Math.max(0, maxChars - total)) + "…";
      truncated = true;
    }
    lines.push(head, body, "");
    total += head.length + body.length;
    if (total >= maxChars) { truncated = true; break; }
  }
  return { title, text: lines.join("\n").trim(), truncated };
}

/**
 * Extracts explicit diff blocks from a session's messages when the host does
 * not expose a git working-tree diff or the working tree is clean. Never
 * fabricates changes that were not present in the session record.
 */
export function extractMessageDiffs(view, maxChars = 8000) {
  const raw = Array.isArray(view?.messages) ? view.messages : [];
  const blocks = [];
  const fencePattern = /```diff\r?\n([\s\S]*?)```/g;
  for (const message of raw) {
    const text = String(message?.text || "");
    if (!text.trim()) continue;
    let foundFence = false;
    fencePattern.lastIndex = 0;
    let match;
    while ((match = fencePattern.exec(text))) {
      const body = String(match[1] || "").trim();
      if (body) {
        blocks.push(body);
        foundFence = true;
      }
    }
    if (!foundFence && /(?:^|\n)diff --git /.test(text)) {
      const start = text.indexOf("diff --git ");
      const snippet = text.slice(start).trim();
      if (snippet) blocks.push(snippet);
    }
  }
  if (!blocks.length) return { text: "", truncated: false };
  const combined = blocks.join("\n\n");
  if (combined.length <= maxChars) return { text: combined, truncated: false };
  return { text: combined.slice(0, maxChars) + "\n…（Diff 已按长度上限截断）", truncated: true };
}

/**
 * Shapes a session's workspace git diff (from `sessions.diff`) or message diff
 * snippets into text safe for filling into a web composer without auto-sending.
 */
export function sessionDiff(diffAnswer, view, options = {}) {
  const maxChars = Math.min(16000, Math.max(500, Number(options.maxChars) || 8000));
  const title = String(diffAnswer?.title || view?.session?.title || "未命名会话");
  const status = String(diffAnswer?.status || "").trim();
  const diff = String(diffAnswer?.diff || "").trim();
  if (diffAnswer?.available && !diffAnswer?.clean && (diff || status)) {
    const location = String(diffAnswer.cwd || "").trim() || "工作区";
    const lines = [`Code 会话《${title}》工作区 Diff（${location}）`, ""];
    if (status) {
      lines.push("变更文件：", status, "");
    }
    let truncated = Boolean(diffAnswer.truncated);
    if (diff) {
      const used = lines.join("\n").length + 16;
      const room = Math.max(200, maxChars - used);
      let body = diff;
      if (body.length > room) {
        body = body.slice(0, room) + "\n…（Diff 已按长度上限截断）";
        truncated = true;
      }
      lines.push("```diff", body, "```");
    }
    return { ok: true, title, text: lines.join("\n").trim(), source: "git", truncated };
  }
  const fromMessages = extractMessageDiffs(view, maxChars - 120);
  if (fromMessages.text) {
    return {
      ok: true,
      title,
      text: `Code 会话《${title}》消息中的 Diff 片段\n\n\`\`\`diff\n${fromMessages.text}\n\`\`\``,
      source: "messages",
      truncated: fromMessages.truncated,
    };
  }
  return {
    ok: false,
    title,
    text: "",
    truncated: false,
    error: String(diffAnswer?.reason || "").trim() || "该会话工作区没有未提交的 Git 变更，也没有可提取的 Diff 片段，请改选“会话摘要”。",
  };
}

/**
 * Decides whether a captured host interaction should close the "更多" menu so
 * the underlying WebContentsView is not kept hidden behind an abandoned menu.
 */
export function shouldCloseMenuOnInteraction(event, menu, moreBtn) {
  if (!menu || menu.hidden) return false;
  if (event?.type === "keydown") return event.key === "Escape";
  if (event?.type !== "pointerdown" && event?.type !== "focusin") return false;
  const path = typeof event.composedPath === "function" ? event.composedPath() : [];
  if (path.length) return !path.includes(menu) && !(moreBtn && path.includes(moreBtn));
  const target = event.target;
  if (!target) return false;
  if (typeof menu.contains === "function" && menu.contains(target)) return false;
  if (moreBtn && typeof moreBtn.contains === "function" && moreBtn.contains(target)) return false;
  return true;
}

export function draftWith(drafts, source, text) {
  const base = drafts && typeof drafts === "object" ? drafts : { web: "", local: "" };
  return { web: String(base.web || ""), local: String(base.local || ""), [source]: String(text ?? "").slice(0, 24000) };
}

export function normalizeModelItems(result) {
  return (Array.isArray(result) ? result : [])
    .map(item => ({
      id: String(item?.id || ""),
      name: String(item?.name || item?.id || ""),
    }))
    .filter(item => item.id);
}

/**
 * Local planner models come from the official catalog (`models.list`), the same
 * source the subagent pool and handoff read. `planner.models` is kept as a
 * fallback for a core that predates that change; on this core both answer the
 * same provider-qualified keys.
 */
export async function loadPlannerModels({ planner, models } = {}) {
  if (models && typeof models.list === "function") {
    return normalizeModelItems(await models.list());
  }
  if (planner && typeof planner.models === "function") {
    return normalizeModelItems(await planner.models());
  }
  return [];
}

export function pickHandoffModel(localModel, handoffModels = []) {
  const target = String(localModel || "").trim();
  if (!target) return "";
  return Array.isArray(handoffModels) && handoffModels.some(model => model?.id === target) ? target : "";
}

/**
 * A planner thread belongs to whoever created it. `pluginOwner` is set by the
 * core when a plugin creates the thread; an empty owner means the built-in UI
 * created it, which is not this plugin's to change.
 */
export function threadActions(thread, pluginId) {
  const owned = Boolean(thread) && Boolean(pluginId) && thread.pluginOwner === pluginId;
  return {
    owned,
    canOpen: Boolean(thread),
    canDelete: owned,
    canRename: owned,
  };
}

/**
 * Resolves how a committed session can be opened. A2's `multiSession.open`
 * puts the session on the same screen; without it `sessions.open` is the
 * host's own navigation, which every plugin with `sessions:read` gets.
 */
export function openTarget(commands, sessionId) {
  const list = Array.isArray(commands) ? commands : [];
  const match = list.find(item => item?.name === "multiSession.open");
  if (match && sessionId) return { mode: "same-screen", action: "multiSession.open", args: { sessionId } };
  return { mode: "same-window", action: "sessions.open", args: { sessionId } };
}

/**
 * What the preview's "open session" button shows. A host without `sessions`
 * disables it and says why; it never claims the session was opened.
 */
export function openButtonState(target, apis) {
  const sameScreen = target?.mode === "same-screen";
  const usable = sameScreen ? Boolean(apis?.commands) : Boolean(apis?.sessions);
  return {
    mode: target?.mode || "same-window",
    label: sameScreen ? "在同屏打开" : "打开会话",
    disabled: !usable,
    title: sameScreen
      ? "经 multiSession.open 在同屏会话中打开"
      : usable
        ? "在 Code 里打开这个会话"
        : "当前宿主没有开放打开会话的接口（sessions:read）",
  };
}

/**
 * Runs one navigation action on the plugin's own browser view. `back` reports
 * whether the host actually had somewhere to go; `reload` and `external` have
 * no such answer, so they only report success.
 */
export async function runPageAction(view, kind) {
  if (!view) throw new Error("网页视图还没有打开");
  if (kind === "back") return { moved: Boolean(await view.back()) };
  if (kind === "reload") {
    await view.reload();
    return { moved: true };
  }
  if (kind === "external") {
    await view.openExternal();
    return { moved: true };
  }
  throw new Error("未知的页面动作：" + kind);
}

/**
 * Moves the panel into its own window. The host opens a window that mounts the
 * same plugin; closing this tab's panel only drops this mount, so the instance
 * (and its storage) keeps running in the popout. A popout that asks to pop out
 * again is refused instead of opening a third window.
 */
export async function popoutIntoWindow(apis) {
  if (!apis?.window) throw new Error("当前宿主没有开放弹出窗口的接口（ui:right-panel）");
  if (apis.surface === "popout") throw new Error("当前已经在独立窗口里");
  await apis.window.popout();
  if (apis.panel?.close) apis.panel.close();
  return { surface: "popout" };
}

/** Closes the popout the panel is mounted in. A host surface has no window. */
export async function closePopoutWindow(apis) {
  if (!apis?.window) throw new Error("当前宿主没有开放窗口接口（ui:right-panel）");
  if (apis.surface !== "popout") throw new Error("当前不在独立窗口里");
  await apis.window.close();
  return { surface: "host" };
}

/**
 * Opens a committed session through whichever path the host actually offers:
 * A2's same-screen command when it is published, the host's own session
 * navigation otherwise. A host with neither refuses instead of pretending.
 */
export async function openSessionTarget(apis, target) {
  if (!target?.args?.sessionId) throw new Error("没有可打开的会话");
  if (target.mode === "same-screen") {
    if (!apis?.commands) throw new Error("当前宿主没有开放命令接口");
    await apis.commands.invoke(target.action, target.args);
    return { via: "command" };
  }
  if (!apis?.sessions) throw new Error("当前宿主没有开放打开会话的接口（sessions:read）");
  await apis.sessions.open(target.args.sessionId);
  return { via: "sessions" };
}

/**
 * Renames a planner thread through the core, then reads the list back so the
 * history shows what was really stored. A caller without a rename-capable
 * planner refuses before touching anything.
 */
export async function renamePlannerThread(apis, threadId, title) {
  const value = String(title ?? "").trim();
  // The core measures the limit in bytes, so a 100-character Chinese title is
  // already 300 bytes; refuse here with the same rule instead of a round trip.
  const bytes = new TextEncoder().encode(value).length;
  if (bytes < 1) throw new Error("标题不能为空");
  if (bytes > 200) throw new Error("标题最多 200 字节（中文约 66 个字）");
  if (!apis?.planner?.rename) throw new Error("当前宿主没有开放重命名接口（planner:run）");
  const thread = await apis.planner.rename(threadId, value);
  const threads = typeof apis.planner.threads === "function" ? await apis.planner.threads() : [];
  return { thread, threads: Array.isArray(threads) ? threads : [] };
}

/**
 * What the plugin actually sends for a local turn. Exported so tests can
 * assert the create/configure payload without a browser. */
export async function sendPlanMessage(api, input) {
  const options = planOptions(input.preset, input.custom);
  let thread = input.thread || null;
  if (!thread) {
    thread = await api.create({ model: input.model, options });
  } else {
    thread = await api.configure(thread.id, { model: input.model, options });
  }
  return api.send(thread.id, input.text);
}

export async function approvePlan(api, threadId, approvalId, allow) {
  return api.approve(threadId, approvalId, Boolean(allow));
}

export async function stopPlan(api, threadId) {
  return api.stop(threadId);
}

/**
 * The view is an OS-level layer: it must be hidden whenever the panel's own
 * HTML is on top of it (menu, sheet, handoff preview) or the panel is closed.
 * `apply()` reads fresh state each time, so a stale resize cannot show it again
 * after a menu opened.
 */
export function createBrowserSync(options) {
  let visible = false;
  let disposed = false;
  const view = options.view;
  const getRect = options.getRect;
  const isCovered = options.isCovered || (() => false);
  async function apply() {
    if (disposed) return { visible: false };
    const rect = getRect();
    const covered = Boolean(isCovered());
    const usable = rect && rect.width > 2 && rect.height > 2 && !covered;
    if (usable) {
      await view.show({ x: rect.x, y: rect.y, width: rect.width, height: rect.height });
      visible = true;
    } else if (visible) {
      await view.hide();
      visible = false;
    }
    return { visible };
  }
  return {
    apply,
    get visible() { return visible; },
    async dispose() {
      if (disposed) return;
      disposed = true;
      if (visible) {
        visible = false;
        await view.hide().catch(() => {});
      }
    },
  };
}

export function createViewLauncher({ open, defaultUrl, onOpen, onError }) {
  let view = null;
  let pending = null;
  let autoFailed = false;

  async function ensure(targetUrl = "") {
    if (view) return view;
    if (pending) {
      await pending;
      if (view) return view;
      if (!targetUrl) return null;
    }
    const url = normalizeUrl(targetUrl) || normalizeUrl(defaultUrl?.());
    if (!url) return null;
    const attempt = (async () => {
      try {
        const opened = await open(url);
        if (!opened) return null;
        view = opened;
        autoFailed = false;
        onOpen?.(opened, url);
        return opened;
      } catch (error) {
        autoFailed = true;
        onError?.(error);
        return null;
      }
    })();
    pending = attempt;
    try {
      return await attempt;
    } finally {
      if (pending === attempt) pending = null;
    }
  }

  async function openTo(rawUrl) {
    const url = normalizeUrl(rawUrl);
    if (!url) throw new Error("地址必须是完整的 http 或 https 链接");
    autoFailed = false;
    const alreadyOpen = Boolean(view);
    if (pending) await pending;
    if (!view) return ensure(url);
    if (alreadyOpen || normalizeUrl(view.url) !== url) {
      await view.navigate(url);
    }
    return view;
  }

  return {
    get view() { return view; },
    get pending() { return Boolean(pending); },
    get autoFailed() { return autoFailed; },
    ensure,
    openTo,
    resetAutoFailure() { autoFailed = false; },
    clear() { view = null; autoFailed = false; },
  };
}

export function errorText(error) {
  if (error instanceof Error && error.message) return error.message;
  if (typeof error === "string" && error) return error;
  return "操作没有完成，请稍后重试。";
}

// ---------------------------------------------------------------------------
// 界面
// ---------------------------------------------------------------------------

const STYLE = `
:host{display:flex;flex-direction:column;min-height:0;height:100%;color:var(--color-text,#1f2328);font-family:var(--font-ui,-apple-system,BlinkMacSystemFont,"PingFang SC","Microsoft YaHei UI",sans-serif);font-size:var(--ui-b2,14px);line-height:1.55}
*,*::before,*::after{box-sizing:border-box}
.ekd-p{display:flex;flex-direction:column;flex:1 1 auto;min-height:0;min-width:0;background:var(--color-bg,#fff);color:var(--color-text,inherit)}
.ekd-p button,.ekd-p input,.ekd-p select,.ekd-p textarea{font:inherit;color:inherit}
.ekd-p button{cursor:pointer;background:none;border:0;border-radius:6px;padding:4px 8px;min-height:26px}
.ekd-p button:hover:not(:disabled){background:var(--color-hover,color-mix(in srgb,var(--color-text,#1f2328) 7%,transparent))}
.ekd-p button:focus-visible,.ekd-p input:focus-visible,.ekd-p select:focus-visible,.ekd-p textarea:focus-visible{outline:2px solid var(--blue,#1783ff);outline-offset:1px}
.ekd-p button:disabled{opacity:.45;cursor:not-allowed}
.ekd-p [hidden]{display:none!important}
.ekd-bar{display:flex;align-items:center;gap:6px;padding:6px 10px;border-bottom:1px solid var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 16%,transparent));flex:0 0 auto;min-width:0}
.ekd-src{display:flex;gap:2px;padding:2px;border-radius:8px;background:var(--panel,color-mix(in srgb,var(--color-text,#1f2328) 5%,var(--color-bg,#fff)));flex:0 0 auto}
.ekd-src button{min-height:24px;padding:2px 10px;border-radius:6px;color:var(--color-text-secondary,color-mix(in srgb,var(--color-text,#1f2328) 62%,var(--color-bg,#fff)))}
.ekd-src button[aria-pressed=true]{background:var(--color-bg,#fff);color:var(--color-text,#1f2328);box-shadow:0 0 0 1px var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 16%,transparent))}
.ekd-tools{display:flex;align-items:center;gap:2px;margin-left:auto;min-width:0;flex-wrap:nowrap;overflow:hidden}
.ekd-tools button{white-space:nowrap;flex:0 0 auto}
.ekd-icon{width:26px;padding:2px;display:inline-flex;align-items:center;justify-content:center}
.ekd-icon svg{width:15px;height:15px;stroke:currentColor;stroke-width:1.7;fill:none;stroke-linecap:round;stroke-linejoin:round}
.ekd-more{position:relative}
.ekd-menu{position:absolute;right:0;top:calc(100% + 4px);z-index:20;min-width:236px;max-width:min(320px,90vw);max-height:calc(100vh - 110px);overflow:auto;overscroll-behavior:contain;padding:6px;border-radius:8px;background:var(--color-bg,#fff);border:1px solid var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 18%,transparent));box-shadow:0 10px 24px rgba(0,0,0,.18);display:flex;flex-direction:column;gap:4px}
.ekd-menu label{display:flex;flex-direction:column;gap:4px;font-size:12px;color:var(--color-text-secondary,color-mix(in srgb,var(--color-text,#1f2328) 62%,var(--color-bg,#fff)));padding:4px 6px}
.ekd-menu input{min-height:28px;border:1px solid var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 20%,transparent));border-radius:6px;padding:4px 8px;background:var(--color-bg,#fff);min-width:0}
.ekd-menu .ekd-row{display:flex;gap:4px}
.ekd-menu .ekd-row input{flex:1 1 auto}
.ekd-sep{height:1px;background:var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 16%,transparent));margin:2px 0}
.ekd-note{padding:4px 6px;font-size:12px;color:var(--color-text-secondary,color-mix(in srgb,var(--color-text,#1f2328) 62%,var(--color-bg,#fff)))}
.ekd-sites{display:flex;gap:2px;padding:0;min-width:0;overflow-x:auto;scrollbar-width:none}
.ekd-sites::-webkit-scrollbar{display:none}
.ekd-sites button,.ekd-site{min-height:24px;padding:2px 8px;border:1px solid var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 18%,transparent));border-radius:999px;font-size:12px;flex:0 0 auto;white-space:nowrap}
.ekd-sites button[aria-pressed=true]{border-color:var(--blue,#1783ff);color:var(--blue,#1783ff);background:var(--soft,color-mix(in srgb,var(--blue,#1783ff) 12%,var(--color-bg,#fff)))}
.ekd-menu .ekd-sites{flex-wrap:wrap;overflow:visible;padding:2px 6px}
.ekd-draftbar{display:flex;align-items:center;gap:6px;font-size:12px;padding:5px 10px;border-top:1px solid var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 16%,transparent));background:var(--panel,color-mix(in srgb,var(--color-text,#1f2328) 4%,var(--color-bg,#fff)));color:var(--color-text-secondary,color-mix(in srgb,var(--color-text,#1f2328) 62%,var(--color-bg,#fff)));flex:0 0 auto}
.ekd-draftbar .ekd-grow{flex:1 1 auto;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.ekd-body{position:relative;flex:1 1 auto;min-height:0;min-width:0;display:flex;flex-direction:column;overflow:hidden}
.ekd-view{position:absolute;inset:0;min-width:0;min-height:0}
.ekd-fallback{position:absolute;inset:0;display:flex;flex-direction:column;align-items:center;justify-content:center;gap:10px;padding:24px;text-align:center;color:var(--color-text-secondary,color-mix(in srgb,var(--color-text,#1f2328) 62%,var(--color-bg,#fff)));background:var(--color-bg,#fff);z-index:2}
.ekd-fallback p{margin:0;max-width:42ch}
.ekd-scroll{flex:1 1 auto;min-height:0;overflow-y:auto;overscroll-behavior:contain;padding:12px 14px 20px;display:flex;flex-direction:column;gap:10px}
.ekd-empty{margin:auto;text-align:center;color:var(--color-text-secondary,color-mix(in srgb,var(--color-text,#1f2328) 62%,var(--color-bg,#fff)));display:flex;flex-direction:column;gap:6px;align-items:center;padding:24px}
.ekd-msg{display:flex;flex-direction:column;gap:4px;border-radius:8px;padding:8px 10px;background:var(--panel,color-mix(in srgb,var(--color-text,#1f2328) 4%,var(--color-bg,#fff)));border:1px solid transparent;min-width:0}
.ekd-msg.is-mine{align-self:flex-end;width:fit-content;max-width:86%;background:var(--color-hover,color-mix(in srgb,var(--color-text,#1f2328) 6%,var(--color-bg,#fff)))}
.ekd-msg:not(.is-mine){background:transparent}
.ekd-workspace{font-size:12px;color:var(--color-text-secondary,inherit);overflow-wrap:anywhere}
.ekd-msg.is-pick{border-color:var(--blue,#1783ff)}
.ekd-msg-head{display:flex;align-items:center;gap:8px;font-size:12px;color:var(--color-text-secondary,color-mix(in srgb,var(--color-text,#1f2328) 62%,var(--color-bg,#fff)));min-width:0}
.ekd-msg-head .ekd-who{font-weight:600;color:var(--color-text,#1f2328)}
.ekd-msg-head time{margin-left:auto;flex:0 0 auto;opacity:0;transition:opacity .12s}
.ekd-msg:hover .ekd-msg-head time,.ekd-msg:focus-within .ekd-msg-head time{opacity:1}
.ekd-msg-text{white-space:pre-wrap;overflow-wrap:anywhere;min-width:0}
.ekd-msg-actions{display:flex;gap:4px;opacity:0;transition:opacity .12s}
.ekd-msg:hover .ekd-msg-actions,.ekd-msg:focus-within .ekd-msg-actions{opacity:1}
.ekd-msg-actions button{font-size:12px;min-height:22px;padding:0 6px;color:var(--color-text-secondary,color-mix(in srgb,var(--color-text,#1f2328) 62%,var(--color-bg,#fff)))}
.ekd-tool{font-size:12px;color:var(--color-text-secondary,color-mix(in srgb,var(--color-text,#1f2328) 62%,var(--color-bg,#fff)));background:color-mix(in srgb,var(--panel,color-mix(in srgb,var(--color-text,#1f2328) 7%,var(--color-bg,#fff))) 80%,transparent);border-radius:6px;padding:4px 8px;white-space:pre-wrap;overflow-wrap:anywhere}
.ekd-tool summary{cursor:pointer}
.ekd-code{position:relative;display:block;margin:6px 0 0;padding:28px 10px 8px;border-radius:6px;background:var(--panel2,color-mix(in srgb,var(--color-text,#1f2328) 7%,var(--color-bg,#fff)));font-family:var(--mono,ui-monospace,"SF Mono",Menlo,Consolas,monospace);font-size:12px;white-space:pre-wrap;overflow-wrap:anywhere}
.ekd-code button{position:absolute;right:4px;top:4px;font-size:11px;min-height:20px;padding:0 6px}
.ekd-composer{flex:0 0 auto;border-top:1px solid var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 16%,transparent));padding:8px 10px;display:flex;flex-direction:column;gap:6px;background:var(--color-bg,#fff)}
.ekd-composer textarea{width:100%;min-height:64px;max-height:34vh;resize:vertical;border:1px solid var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 20%,transparent));border-radius:8px;padding:8px 10px;background:var(--color-bg,#fff);line-height:1.5}
.ekd-foot{display:flex;align-items:center;gap:6px;flex-wrap:wrap;min-width:0}
.ekd-foot select{min-height:26px;max-width:46%;border:1px solid var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 20%,transparent));border-radius:6px;padding:2px 6px;background:var(--color-bg,#fff);min-width:0}
.ekd-foot .ekd-spacer{flex:1 1 auto}
.ekd-chip{font-size:11px;padding:1px 7px;border-radius:999px;background:var(--color-hover,rgba(0,0,0,.05));color:var(--color-text-secondary,color-mix(in srgb,var(--color-text,#1f2328) 62%,var(--color-bg,#fff)));white-space:nowrap;min-width:0;overflow:hidden;text-overflow:ellipsis}
.ekd-outline{border:1px solid var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 20%,transparent))!important;padding:3px 9px!important}
.ekd-primary{background:var(--blue,#1783ff)!important;color:#fff!important;padding:4px 10px!important}
.ekd-primary:hover:not(:disabled){filter:brightness(1.06)}
.ekd-danger{color:var(--red,var(--danger,#d2352c))!important}
.ekd-subbar{display:flex;align-items:center;gap:6px;flex-wrap:wrap;padding:6px 10px;border-bottom:1px solid var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 14%,transparent));background:var(--panel,color-mix(in srgb,var(--color-text,#1f2328) 3%,var(--color-bg,#fff)));flex:0 0 auto;min-width:0}
.ekd-subbar select{min-height:26px;max-width:170px;border:1px solid var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 20%,transparent));border-radius:6px;padding:2px 6px;background:var(--color-bg,#fff);font-size:12px;min-width:0}
.ekd-filter-btn{min-height:22px!important;padding:1px 8px!important;font-size:12px!important;border:1px solid var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 18%,transparent))!important;border-radius:999px!important}
.ekd-variant-bar{display:flex;align-items:center;gap:6px;flex-wrap:wrap;padding:6px 8px;border-radius:8px;border:1px solid var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 16%,transparent));background:var(--panel,color-mix(in srgb,var(--color-text,#1f2328) 4%,var(--color-bg,#fff)));font-size:12px}
.ekd-variant-bar select{min-height:24px;max-width:160px;border:1px solid var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 20%,transparent));border-radius:6px;padding:1px 6px;background:var(--color-bg,#fff);font-size:12px}
.ekd-status{font-size:12px;color:var(--color-text-secondary,color-mix(in srgb,var(--color-text,#1f2328) 62%,var(--color-bg,#fff)));min-width:0;overflow-wrap:anywhere}
.ekd-status.is-error{color:var(--red,var(--danger,#d2352c))}
.ekd-banner{display:flex;gap:6px;align-items:flex-start;font-size:12px;padding:6px 10px;border-bottom:1px solid var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 16%,transparent));background:color-mix(in srgb,var(--warn,#e3b654) 16%,transparent)}
.ekd-preview{display:flex;flex-direction:column;gap:8px;min-height:0;flex:1 1 auto;overflow-y:auto;padding:12px 14px 18px}
.ekd-preview textarea{width:100%;min-height:150px;flex:1 1 auto;resize:vertical;border:1px solid var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 20%,transparent));border-radius:8px;padding:8px 10px;background:var(--color-bg,#fff);line-height:1.5}
.ekd-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:6px}
.ekd-field{display:flex;flex-direction:column;gap:3px;font-size:12px;color:var(--color-text-secondary,color-mix(in srgb,var(--color-text,#1f2328) 62%,var(--color-bg,#fff)));min-width:0}
.ekd-field input,.ekd-field select{min-height:28px;border:1px solid var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 20%,transparent));border-radius:6px;padding:3px 7px;background:var(--color-bg,#fff);min-width:0}
.ekd-list{display:flex;flex-direction:column;gap:4px;min-width:0}
.ekd-item{display:flex;align-items:center;gap:6px;padding:6px 8px;border-radius:6px;border:1px solid var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 16%,transparent));min-width:0;text-align:left;width:100%;background:none}
.ekd-item .ekd-grow{flex:1 1 auto;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.ekd-item small{color:var(--color-text-secondary,color-mix(in srgb,var(--color-text,#1f2328) 62%,var(--color-bg,#fff)));white-space:nowrap}
.ekd-check{display:flex;gap:6px;align-items:flex-start;padding:6px 8px;border-radius:6px;border:1px solid var(--color-line,color-mix(in srgb,var(--color-text,#1f2328) 16%,transparent));min-width:0;font-size:13px;cursor:pointer}
.ekd-check input{margin-top:3px;flex:0 0 auto}
.ekd-check span{overflow-wrap:anywhere;min-width:0}
.ekd-check.is-role{display:block;height:100%;padding:8px}
.ekd-sr{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0);white-space:nowrap}
@media (prefers-reduced-motion:reduce){.ekd-p *{transition:none!important;animation:none!important}}
@media (max-width:420px){
  .ekd-tools .ekd-label{display:none}
  .ekd-sites{padding:4px}
  .ekd-scroll{padding:10px}
}
`;

// ---------------------------------------------------------------------------

function node(tag, className = "", text = "") {
  const element = document.createElement(tag);
  if (className) element.className = className;
  if (text) element.textContent = text;
  return element;
}

function icon(name) {
  const paths = {
    back: "M10 5l-5 5 5 5M5 10h9",
    reload: "M4 8a6 6 0 1 1 2 4.4M4 4v4h4",
    more: "M4 4h8M4 8h8M4 12h8",
    extract: "M7 3h4l3 3v7a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1M8 3v3h3",
    close: "M4 4l8 8M12 4l-8 8",
    send: "M3 8h9M8 4l4 4-4 4",
    stop: "M5 5h6v6H5z",
    check: "M3 8l3 3 5-6",
    plus: "M8 4v8M4 8h8",
    copy: "M5 5h6v7H5zM3 3h6v2",
  };
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 16 16");
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("focusable", "false");
  const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
  path.setAttribute("d", paths[name] || paths.more);
  svg.append(path);
  return svg;
}

function button(label, className = "", title = "") {
  const element = node("button", className);
  element.type = "button";
  element.textContent = label;
  if (title) element.title = title;
  return element;
}

function iconButton(name, label) {
  const element = button("", "ekd-icon");
  element.setAttribute("aria-label", label);
  element.title = label;
  element.append(icon(name));
  return element;
}

function field(label, control) {
  const wrapper = node("label", "ekd-field");
  wrapper.append(node("span", "", label), control);
  return wrapper;
}

/** Only a reader already at the bottom should be carried along by new output. */
function nearBottom(element) {
  if (!element) return true;
  return element.scrollHeight - element.scrollTop - element.clientHeight < 48;
}

function clockText(value) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return date.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" });
}

/** Splits assistant text into plain runs and fenced code blocks, so a fence gets
 * its own “交接此段” button instead of a markdown renderer dependency. */
export function splitCodeBlocks(text) {
  const source = String(text || "");
  const out = [];
  const pattern = /```([\w-]*)\n([\s\S]*?)```/g;
  let index = 0;
  let match;
  while ((match = pattern.exec(source))) {
    if (match.index > index) out.push({ type: "text", text: source.slice(index, match.index) });
    out.push({ type: "code", lang: match[1] || "", code: match[2].replace(/\n$/, "") });
    index = pattern.lastIndex;
  }
  if (index < source.length) out.push({ type: "text", text: source.slice(index) });
  return out.length ? out : [{ type: "text", text: source }];
}


const STORAGE_KEYS = {
  sites: "planner.sites",
  lastWeb: "planner.lastWebUrl",
  source: "planner.source",
  drafts: "planner.drafts",
};

const GAPS = {
  directory: "插件接口没有目录选择器，新会话工作目录需要手工输入已存在的路径。",
};

export async function activate(ctx) {
  if (typeof document === "undefined") return;
  const pluginId = ctx.plugin?.id || PLUGIN_ID;
  const { storage, planner, handoff, browser, commands, sessions, models: modelsApi } = ctx;
  const windowHost = ctx.window;
  const surface = ctx.plugin?.surface === "popout" ? "popout" : "host";

  const state = {
    source: "web",
    sites: DEFAULT_SITES.map(site => ({ ...site })),
    lastWebUrl: DEFAULT_SITES[0].url,
    drafts: { web: "", local: "" },
    view: null,
    viewUrl: "",
    extraction: null,
    selected: new Set(),
    pickOpen: false,
    previewSeq: 0,
    previewDraftSeq: 0,
    preview: null,
    committed: null,
    status: "",
    statusError: false,
    busy: "",
    fillMode: "summary",
    pendingFill: null,
    fillGeneration: 0,
    fillInFlight: false,
    summarize: {
      busy: false,
      model: "",
      cancel: null,
      seq: 0,
    },
    local: {
      thread: null,
      threads: [],
      models: [],
      model: "",
      preset: "review",
      custom: { context: true, skills: false, tools: false },
      busy: false,
      sending: false,
      draftRevision: 0,
      selectionGeneration: 0,
      confirmingDelete: "",
      renamingId: "",
      renameDraft: "",
      renameBusy: false,
    },
    handoffModels: [],
    sessions: null,
  };

  const el = {};
  let panelHandle = null;
  let mountHost = null;
  let shadow = null;
  let root = null;
  let sync = null;
  let syncFrame = null;
  let sidebarRelease = null;
  let panelObserver = null;
  let disposed = false;
  let threadSubscription = null;
  let viewNavSubscription = null;
  let previewDraftTimer = null;
  let draftsDirty = false;
  const viewLauncher = createViewLauncher({
    defaultUrl: webUrl,
    open: async targetUrl => {
      const view = await browser.open("planner-web", targetUrl);
      if (disposed) {
        await view.close().catch(() => {});
        return null;
      }
      return view;
    },
    onOpen: (view, targetUrl) => {
      state.view = view;
      state.viewUrl = view.url || targetUrl;
      viewNavSubscription?.dispose?.();
      viewNavSubscription = typeof view.onNavigate === "function"
        ? view.onNavigate(nextUrl => { syncViewUrl(nextUrl); })
        : null;
      if (sync) void sync.dispose().catch(() => {});
      sync = createBrowserSync({ view, getRect: () => rectOf(el.viewport), isCovered: obscuring });
      if (el.fallback) el.fallback.hidden = true;
      if (el.webStatus) el.webStatus.textContent = "";
      scheduleSync();
    },
    onError: error => {
      showFallback("内嵌网页无法打开：" + errorText(error) + "。可以先在系统浏览器里打开并登录，再用“粘贴内容导入”。");
      renderToolbar();
    },
  });

  // --- status --------------------------------------------------------------
  function setStatus(text, isError = false) {
    state.status = String(text || "");
    state.statusError = Boolean(isError) && Boolean(text);
    if (!el.banner) return;
    el.banner.hidden = !state.status;
    el.banner.classList.toggle("is-error", state.statusError);
    el.banner.textContent = state.status;
  }

  function setMenuOpen(open) {
    if (!el.menu) return;
    const next = Boolean(open);
    el.menu.hidden = !next;
    if (el.moreBtn) el.moreBtn.setAttribute("aria-expanded", String(next));
  }

  // --- persistence ---------------------------------------------------------
  async function persist(key, value) {
    try {
      await storage.set(key, value);
    } catch (error) {
      ctx.log("warn", "规划插件未能保存 " + key + "：" + errorText(error));
    }
  }
  function queueDrafts() {
    draftsDirty = true;
    window.clearTimeout(previewDraftTimer);
    previewDraftTimer = window.setTimeout(() => {
      previewDraftTimer = null;
      draftsDirty = false;
      void persist(STORAGE_KEYS.drafts, state.drafts);
    }, 400);
  }
  function flushDrafts() {
    window.clearTimeout(previewDraftTimer);
    previewDraftTimer = null;
    if (!draftsDirty) return;
    draftsDirty = false;
    void persist(STORAGE_KEYS.drafts, state.drafts);
  }
  function setDraft(source, text) {
    state.drafts = draftWith(state.drafts, source, text);
  }

  async function restore() {
    const keys = [STORAGE_KEYS.sites, STORAGE_KEYS.lastWeb, STORAGE_KEYS.source, STORAGE_KEYS.drafts];
    const settled = await Promise.allSettled(keys.map(key => storage.get(key)));
    const value = index => (settled[index].status === "fulfilled" ? settled[index].value : undefined);
    const sites = value(0);
    if (Array.isArray(sites)) state.sites = normalizeSites(sites);
    const lastWeb = normalizeUrl(value(1));
    if (lastWeb) state.lastWebUrl = lastWeb;
    const source = value(2);
    if (source === "web" || source === "local") state.source = source;
    const drafts = value(3);
    if (drafts && typeof drafts === "object") {
      state.drafts = { web: String(drafts.web || ""), local: String(drafts.local || "") };
    }
  }

  // --- browser view --------------------------------------------------------
  function webUrl() {
    return normalizeUrl(state.viewUrl) || normalizeUrl(state.lastWebUrl) || state.sites[0]?.url || DEFAULT_SITES[0].url;
  }

  function obscuring() {
    if (!root || !root.isConnected) return true;
    // A closed panel leaves the mount in the DOM but hidden; the OS-level view
    // must not stay on screen above whatever the host shows instead.
    if (!panelVisible()) return true;
    if (state.source !== "web") return true;
    if (state.preview) return true;
    if (state.pickOpen) return true;
    if (el.menu && !el.menu.hidden) return true;
    if (el.fillChoice && !el.fillChoice.hidden) return true;
    if (el.sessionPicker && !el.sessionPicker.hidden) return true;
    if (document.querySelector(".settings-dialog, dialog[open], .ekd-session-picker[open]")) return true;
    return false;
  }

  function rectOf(element) {
    if (!element) return null;
    const rect = element.getBoundingClientRect();
    if (!rect || rect.width <= 2 || rect.height <= 2) return null;
    return { x: rect.left, y: rect.top, width: rect.width, height: rect.height };
  }

  function scheduleSync() {
    if (syncFrame !== null) return;
    syncFrame = window.requestAnimationFrame(() => {
      syncFrame = null;
      if (!sync) return;
      sync.apply().catch(error => ctx.log("warn", "网页视图同步失败：" + errorText(error)));
    });
  }

  function syncViewUrl(rawUrl) {
    const url = normalizeUrl(rawUrl);
    if (!url || url === state.viewUrl) return false;
    state.viewUrl = url;
    state.lastWebUrl = url;
    if (state.pendingFill || state.fillInFlight || (el.sessionPicker && !el.sessionPicker.hidden)) resetFillUi();
    void persist(STORAGE_KEYS.lastWeb, url);
    renderToolbar();
    return true;
  }

  async function ensureView(initialUrl = "") {
    if (state.view) return state.view;
    if (!browser) {
      showFallback("当前宿主没有提供网页视图能力（browser:view），无法在内嵌页面里登录。");
      return null;
    }
    return viewLauncher.ensure(initialUrl);
  }

  function showFallback(text) {
    if (!el.fallback) return;
    el.fallback.hidden = false;
    el.fallbackText.textContent = text;
  }

  async function navigate(raw) {
    const url = normalizeUrl(raw);
    if (!url) {
      setStatus("地址必须是完整的 http 或 https 链接", true);
      return;
    }
    if (!browser) {
      showFallback("当前宿主没有提供网页视图能力（browser:view），无法在内嵌页面里登录。");
      return;
    }
    state.busy = "navigate";
    renderToolbar();
    try {
      const view = await viewLauncher.openTo(url);
      if (!view) return;
      if (el.fallback) el.fallback.hidden = true;
      state.viewUrl = url;
      state.lastWebUrl = url;
      await persist(STORAGE_KEYS.lastWeb, url);
      renderToolbar();
      scheduleSync();
      setStatus("已打开 " + new URL(url).hostname);
    } catch (error) {
      setStatus("打开失败：" + errorText(error), true);
    } finally {
      state.busy = "";
      renderToolbar();
    }
  }

  async function pageAction(kind, label) {
    if (!state.view) {
      setStatus("网页视图还没有打开", true);
      return;
    }
    try {
      const result = await runPageAction(state.view, kind);
      await refreshCurrentUrl();
      if (result.moved === false) setStatus("没有可返回的历史页面");
      else setStatus(label + "完成");
    } catch (error) {
      setStatus(label + "失败：" + errorText(error), true);
    }
  }

  async function openExternal() {
    if (!state.view) {
      setStatus("先打开网页再在系统浏览器里打开。", true);
      return;
    }
    try {
      await runPageAction(state.view, "external");
      setStatus("已在系统浏览器打开当前页面");
    } catch (error) {
      setStatus("打开系统浏览器失败：" + errorText(error), true);
    }
  }

  async function popOut() {
    setMenuOpen(false);
    try {
      await popoutIntoWindow({ window: windowHost, surface, panel: panelHandle });
      setStatus("已在独立窗口打开规划面板");
    } catch (error) {
      setStatus("弹出窗口失败：" + errorText(error), true);
    }
  }

  async function closePopout() {
    setMenuOpen(false);
    try {
      await closePopoutWindow({ window: windowHost, surface });
    } catch (error) {
      setStatus("关闭窗口失败：" + errorText(error), true);
    }
  }

  async function refreshCurrentUrl() {
    if (!state.view) return;
    try {
      const current = await state.view.adapter(CURRENT_URL_SOURCE, {});
      const url = normalizeUrl(current);
      if (url && url !== state.viewUrl) {
        state.viewUrl = url;
        state.lastWebUrl = url;
        await persist(STORAGE_KEYS.lastWeb, url);
        renderToolbar();
      }
    } catch {
      /* 页面跳转中的地址读取失败不影响其余操作 */
    }
  }

  async function extract(options = {}) {
    const wantSummarize = Boolean(options && options.summarize);
    if (!state.view) {
      setStatus("先打开网页再提取。", true);
      return;
    }
    if (state.summarize.busy) {
      setStatus("正在进行模型归纳，可先取消归纳或等待完成。", true);
      return;
    }
    state.busy = "extract";
    renderToolbar();
    setStatus("正在按站点规则去噪读取当前页面…");
    await refreshCurrentUrl();
    const adapter = adapterFor(state.viewUrl);
    let answer = null;
    let genericAdapter = null;
    let adapterError = "";
    if (adapter) {
      try {
        answer = await state.view.adapter(adapter.source, { limit: 60 });
      } catch (error) {
        adapterError = errorText(error);
      }
    }
    if (!answer || !Array.isArray(answer.messages) || answer.messages.length === 0) {
      try {
        genericAdapter = await state.view.adapter(GENERIC_PAGE_ADAPTER_SOURCE, { limit: 60 });
      } catch (error) {
        if (!adapterError) adapterError = errorText(error);
      }
    }
    let pageText = "";
    let pageError = "";
    const hasStructured =
      (Array.isArray(answer?.messages) && answer.messages.length > 0) ||
      (Array.isArray(genericAdapter?.messages) && genericAdapter.messages.length > 0);
    if (!hasStructured) {
      try {
        pageText = await state.view.extract();
      } catch (error) {
        pageError = errorText(error);
      }
    }
    const result = chooseExtraction({
      site: adapter?.site || "",
      adapter: answer,
      genericAdapter,
      adapterError,
      pageText,
    });
    state.busy = "";
    renderToolbar();
    if (!result.messages.length) {
      setStatus(result.notice + (pageError ? " 读取正文失败：" + pageError : ""), true);
      return;
    }
    state.extraction = result;
    state.selected = selectPlanningIds(result.messages);
    setMenuOpen(false);
    await loadModels().catch(() => []);
    if (!state.summarize.model && state.local.model) {
      state.summarize.model = state.local.model;
    }
    if (wantSummarize) {
      const chosen = selectedMessages();
      const rawText = buildHandoffText(chosen.length ? chosen : result.messages, new Set((chosen.length ? chosen : result.messages).map(item => item.id)), { site: result.site || "" });
      if (!state.summarize.model) {
        state.pickOpen = true;
        renderPicker();
        applyChrome();
        setStatus("当前尚未配置可用归纳模型，已打开去噪提取结果；可直接勾选后点「直接交接所选」。", true);
        return;
      }
      await summarizeToPreview(rawText, { site: result.site || "", fallbackToPicker: true });
      return;
    }
    state.pickOpen = true;
    renderPicker();
    applyChrome();
    setStatus(result.notice, result.mode === "fallback");
  }

  async function cancelSummarize() {
    if (!state.summarize.busy) return;
    state.summarize.seq++;
    const cancel = state.summarize.cancel;
    state.summarize.cancel = null;
    state.summarize.busy = false;
    renderToolbar();
    renderPicker();
    renderPreview();
    if (typeof cancel === "function") {
      await cancel().catch(() => {});
    }
    setStatus("已取消模型归纳；仍可使用「直接提取 / 直接交接所选」。");
  }

  async function summarizeToPreview(rawText, meta = {}) {
    const cleaned = String(rawText || "").trim();
    if (!cleaned) {
      setStatus("请先选择或填写要归纳的内容。", true);
      return;
    }
    await loadModels().catch(() => []);
    const model = state.summarize.model || state.local.model || state.local.models[0]?.id || "";
    if (!model) {
      setStatus("没有可用的归纳模型，请先在模型供应商里配置模型，或直接使用「直接交接所选」。", true);
      if (meta.fallbackToPicker && state.extraction) {
        state.pickOpen = true;
        renderPicker();
        applyChrome();
      }
      return;
    }
    state.summarize.model = model;
    const seq = ++state.summarize.seq;
    state.summarize.busy = true;
    state.summarize.cancel = null;
    renderToolbar();
    renderPicker();
    renderPreview();
    setStatus("正在用模型归纳提炼规划内容（可随时点「取消归纳」）…");
    try {
      const distilled = await runWebSummarization(
        {
          planner,
          events: ctx.events,
          onCancelReady: fn => {
            if (state.summarize.seq === seq) state.summarize.cancel = fn;
          },
        },
        {
          model,
          text: cleaned,
          site: meta.site || "",
          onProgress: partial => {
            if (state.summarize.seq !== seq) return;
            setStatus("正在用模型归纳规划内容…（已生成 " + partial.length + " 字符）");
          },
        },
      );
      if (disposed || state.summarize.seq !== seq) return;
      state.summarize.busy = false;
      state.summarize.cancel = null;
      const prefix = meta.site ? `来自 ${meta.site} 的网页规划（经模型归纳）\n\n` : "网页规划（经模型归纳）\n\n";
      const summaryFull = prefix + distilled;
      openPreview(summaryFull, "web", {
        rawText: cleaned,
        summarizedText: summaryFull,
        variant: "summary",
        site: meta.site || "",
        preserveFields: Boolean(meta.preserveFields),
      });
      setStatus("已完成模型归纳，可在下方编辑或随时切回提取原文。");
    } catch (error) {
      if (disposed || state.summarize.seq !== seq) return;
      state.summarize.busy = false;
      state.summarize.cancel = null;
      const msg = errorText(error);
      if (meta.fallbackToPicker && state.extraction) {
        state.pickOpen = true;
        renderPicker();
        applyChrome();
      }
      setStatus("模型归纳未完成：" + msg + "。你仍可直接交接提取原文。", true);
    } finally {
      if (!disposed) {
        renderToolbar();
        renderPicker();
        renderPreview();
      }
    }
  }

  function selectedMessages() {
    return (state.extraction?.messages || []).filter(message => state.selected.has(message.id));
  }

  // --- code to web ---------------------------------------------------------
  async function loadSessions(force = false) {
    if (state.sessions && !force) return state.sessions;
    const page = await ctx.core.request("sessions.list", {});
    state.sessions = Array.isArray(page?.items) ? page.items : [];
    return state.sessions;
  }

  function resetFillUi() {
    state.fillGeneration++;
    state.pendingFill = null;
    if (el.fillChoice) {
      el.fillChoice.hidden = true;
      el.fillChoice.replaceChildren();
    }
    if (el.sessionPicker) {
      el.sessionPicker.hidden = true;
      el.sessionPickerList.replaceChildren();
    }
    scheduleSync();
  }

  async function pickSessionForFill(mode = state.fillMode) {
    if (state.fillInFlight) return;
    resetFillUi();
    const generation = state.fillGeneration;
    state.fillMode = mode === "diff" ? "diff" : "summary";
    setMenuOpen(false);
    setStatus("正在读取 Code 会话列表…");
    try {
      const sessions = await loadSessions(true);
      if (generation !== state.fillGeneration || state.source !== "web" || !panelVisible()) return;
      if (!sessions.length) {
        setStatus("还没有可用的 Code 会话。", true);
        return;
      }
      renderSessionPicker(sessions);
      el.sessionPicker.hidden = false;
      scheduleSync();
      setStatus("");
    } catch (error) {
      setStatus("读取会话失败：" + errorText(error), true);
    }
  }

  function renderSessionPicker(sessions) {
    el.sessionPickerList.replaceChildren();
    const modeNote = state.fillMode === "diff"
      ? "选择要填入网页输入框的 Code 会话 Diff（只读工作区变更与消息片段，经核心脱敏后填入）："
      : "选择要填入网页输入框的 Code 会话（只读摘要，不会改动会话）：";
    const modeRow = node("div", "ekd-row");
    const summaryTab = button("会话摘要", state.fillMode === "summary" ? "ekd-primary" : "");
    summaryTab.setAttribute("aria-pressed", String(state.fillMode === "summary"));
    summaryTab.addEventListener("click", () => {
      state.fillMode = "summary";
      renderSessionPicker(sessions);
    });
    const diffTab = button("工作区 Diff", state.fillMode === "diff" ? "ekd-primary" : "");
    diffTab.setAttribute("aria-pressed", String(state.fillMode === "diff"));
    diffTab.addEventListener("click", () => {
      state.fillMode = "diff";
      renderSessionPicker(sessions);
    });
    modeRow.append(summaryTab, diffTab);
    el.sessionPicker.replaceChildren(modeRow, node("p", "ekd-note", modeNote), el.sessionPickerList);
    for (const session of sessions) {
      const item = button("", "ekd-item");
      item.dataset.sessionId = String(session.id);
      const grow = node("span", "ekd-grow", String(session.title || session.id));
      item.append(grow, node("small", "", String(session.cwd || "").split("/").pop() || ""));
      item.addEventListener("click", () => void prepareFill(session, state.fillMode));
      el.sessionPickerList.append(item);
    }
    const cancel = button("取消");
    cancel.addEventListener("click", () => {
      resetFillUi();
      setStatus("");
    });
    el.sessionPicker.append(cancel);
  }

  async function prepareFill(session, mode = state.fillMode) {
    if (state.fillInFlight) return;
    const kind = mode === "diff" ? "diff" : "summary";
    el.sessionPicker.hidden = true;
    state.fillInFlight = true;
    setStatus(kind === "diff" ? "正在读取会话 Diff 与网页输入框…" : "正在读取会话内容与网页输入框…");
    const generation = state.fillGeneration;
    const view = state.view;
    try {
      if (!view) {
        setStatus("先打开网页再填入内容", true);
        return;
      }
      const composer = composerFor(state.viewUrl);
      if (!composer) {
        setStatus("当前网站没有输入框适配器，无法安全填入。可以复制后手动粘贴。", true);
        return;
      }
      const reading = await view.adapter(COMPOSER_SOURCE, { mode: "read", selectors: composer.inputs });
      if (generation !== state.fillGeneration) return;
      if (!reading?.ok || !reading.ticket || normalizeUrl(reading.url) !== normalizeUrl(state.viewUrl)) {
        setStatus("未能确认当前网页的输入框。请打开对话页，再选择要填入的会话。", true);
        return;
      }
      let fill = null;
      if (kind === "diff") {
        let diffAnswer = null;
        try {
          diffAnswer = await ctx.core.request("sessions.diff", { id: session.id });
        } catch {
          diffAnswer = null;
        }
        if (diffAnswer?.available && !diffAnswer?.clean && (diffAnswer.diff || diffAnswer.status)) {
          fill = sessionDiff(diffAnswer, { session });
        } else {
          const view = await ctx.core.request("sessions.view", { id: session.id });
          fill = sessionDiff(diffAnswer, { ...view, session: view?.session || session });
        }
        if (!fill.ok) {
          setStatus(fill.error, true);
          return;
        }
      } else {
        const view = await ctx.core.request("sessions.view", { id: session.id });
        fill = sessionSummary({ ...view, session: view?.session || session });
      }
      if (generation !== state.fillGeneration || state.source !== "web" || !panelVisible()) return;
      state.pendingFill = { mode: kind, summary: fill, before: String(reading.text || ""), inputs: composer.inputs, ticket: reading.ticket, view };
      if (!state.pendingFill.before.trim()) {
        await writeFill("write", true);
        return;
      }
      renderFillChoice();
    } catch (error) {
      if (generation === state.fillGeneration) setStatus("填入失败：" + errorText(error), true);
    } finally {
      state.fillInFlight = false;
      scheduleSync();
    }
  }

  function renderFillChoice() {
    const pending = state.pendingFill;
    if (!pending || !el.fillChoice) return;
    el.fillChoice.replaceChildren();
    el.fillChoice.hidden = false;
    el.fillChoice.append(node("p", "ekd-note", "网页输入框已有 " + pending.before.length + " 个字符。选择如何处理："));
    const row = node("div", "ekd-row");
    const append = button(pending.mode === "diff" ? "追加 Diff" : "追加摘要", "ekd-primary");
    append.addEventListener("click", () => void writeFill("append"));
    const replace = button("覆盖输入框");
    replace.addEventListener("click", () => void writeFill("replace"));
    const cancel = button("取消");
    cancel.addEventListener("click", () => {
      resetFillUi();
      setStatus("未改动网页输入框。");
    });
    row.append(append, replace, cancel);
    el.fillChoice.append(row);
    el.fillChoice.append(node("p", "ekd-note", "填入后只读回校验，不会发送。"));
    scheduleSync();
  }

  async function writeFill(choice, automatic = false) {
    const pending = state.pendingFill;
    if (!pending || (state.fillInFlight && !automatic) || pending.view !== state.view || state.source !== "web" || !panelVisible()) return;
    const plan = fillPlan({ before: pending.before, text: pending.summary.text, choice });
    if (!plan.ok) {
      if (plan.needsChoice) renderFillChoice();
      else setStatus(plan.error, true);
      return;
    }
    state.fillInFlight = true;
    try {
      const written = await pending.view.adapter(COMPOSER_SOURCE, { mode: "write", selectors: pending.inputs, text: plan.text, ticket: pending.ticket });
      if (state.pendingFill !== pending) return;
      if (!written?.ok) {
        resetFillUi();
        setStatus(written?.reason === "composer-changed" || written?.reason === "no-input"
          ? "网页或草稿已变化，未写入。请重新发起回填。"
          : "未收到网页写入确认，请先检查输入框。", true);
        return;
      }
      const check = verifyDraftFill(plan.text, written.text);
      if (!check.ok) {
        resetFillUi();
        setStatus("写入后读回的内容与预期不一致（预期 " + check.wanted.length + " 字符，实际 " + check.got.length + " 字符）。没有发送任何内容，请手动核对。", true);
        return;
      }
      resetFillUi();
      const label = pending.mode === "diff" ? "Diff " : "摘要";
      setStatus("已填入 " + pending.summary.text.length + " 个字符并读回校验通过；本插件不会自动发送。" + (pending.summary.truncated ? label + "已按上限截断。" : ""));
    } catch (error) {
      if (state.pendingFill === pending) {
        resetFillUi();
        setStatus("未能确认网页写入结果，请先检查输入框。" + errorText(error), true);
      }
    } finally {
      state.fillInFlight = false;
    }
  }

  async function pasteImport() {
    setMenuOpen(false);
    if (state.drafts.web.trim()) {
      openPreview(state.drafts.web, "web");
      return;
    }
    setStatus("请在交接预览里粘贴网页内容；也可以先从网页勾选消息再进入。");
    openPreview("", "web");
  }

  // --- local planner -------------------------------------------------------
  async function loadModels() {
    if (state.local.models.length) return state.local.models;
    const list = await loadPlannerModels({ planner, models: modelsApi });
    state.local.models = list;
    if (!state.local.model) state.local.model = list[0]?.id || "";
    return list;
  }

  async function loadHandoffModels() {
    if (state.handoffModels.length) return state.handoffModels;
    if (!modelsApi || typeof modelsApi.list !== "function") return [];
    try {
      const list = normalizeModelItems(await modelsApi.list());
      state.handoffModels = list;
      return list;
    } catch {
      return [];
    }
  }

  function subscribeThread(threadId) {
    // One live subscription at a time: switching threads releases the previous
    // one instead of accumulating listeners. The teardown is registered once,
    // with the plugin, not per switch.
    if (threadSubscription) {
      threadSubscription.dispose();
      threadSubscription = null;
    }
    if (!threadId) return;
    threadSubscription = ctx.events.on("planner:" + threadId, payload => {
      if (!payload || typeof payload !== "object" || payload.id !== threadId) return;
      const active = state.local.thread?.id;
      if (active && active !== threadId) return;
      state.local.thread = payload;
      state.local.busy = payload.status === "running";
      renderThread();
    });
  }

  async function sendLocal() {
    if (state.local.sending || state.local.busy) return;
    const text = String(el.draft.value || "").trim();
    if (!text) {
      setStatus("请输入规划内容。", true);
      return;
    }
    const status = state.local.thread?.status;
    if (status === "running" || status === "quota") {
      setStatus("请先停止当前生成或解除额度等待。", true);
      return;
    }
    const options = planOptions(state.local.preset, state.local.custom);
    const draftRevision = state.local.draftRevision;
    state.local.selectionGeneration++;
    let thread = state.local.thread;
    state.local.sending = true;
    renderThread();
    try {
      await loadModels();
      if (!state.local.model) {
        setStatus("没有可用模型。请先在模型与凭据里配置，再发送这份草稿。", true);
        return;
      }
      if (!thread) {
        thread = await planner.create({ model: state.local.model, options });
        if (disposed) return;
        state.local.thread = thread;
        subscribeThread(thread.id);
      } else {
        state.local.thread = await planner.configure(thread.id, { model: state.local.model, options });
      }
      if (disposed) return;
      const sent = await planner.send(thread.id, text);
      if (!disposed && state.local.thread?.id === thread.id && (state.local.thread.revision ?? 0) <= (sent.revision ?? 0)) {
        state.local.thread = sent;
      }
      if (disposed) return;
      if (state.local.draftRevision === draftRevision) {
        setDraft("local", "");
        el.draft.value = "";
        draftsDirty = true;
        flushDrafts();
      }
      setStatus("");
    } catch (error) {
      setStatus(errorText(error), true);
    } finally {
      state.local.sending = false;
      if (!disposed) renderThread();
    }
  }

  async function stopLocal() {
    const thread = state.local.thread;
    if (!thread) return;
    state.local.busy = true;
    renderThread();
    try {
      state.local.thread = await planner.stop(thread.id);
    } catch (error) {
      setStatus("停止失败：" + errorText(error), true);
    } finally {
      state.local.busy = false;
      renderThread();
    }
  }

  async function retryLocal() {
    const thread = state.local.thread;
    if (!thread) return;
    state.local.busy = true;
    renderThread();
    try {
      state.local.thread = await planner.retry(thread.id, thread.revision);
    } catch (error) {
      setStatus("继续失败：" + errorText(error), true);
    } finally {
      state.local.busy = false;
      renderThread();
    }
  }

  async function approveLocal(approvalId, allow) {
    const thread = state.local.thread;
    if (!thread) return;
    try {
      state.local.thread = await planner.approve(thread.id, approvalId, allow);
    } catch (error) {
      setStatus("审批失败：" + errorText(error), true);
    }
    renderThread();
  }

  async function loadThreads() {
    try {
      state.local.threads = await planner.threads();
    } catch (error) {
      setStatus("读取规划历史失败：" + errorText(error), true);
      return;
    }
    renderHistory();
  }

  async function openThread(id) {
    if (state.local.sending || state.local.busy) return;
    const generation = ++state.local.selectionGeneration;
    try {
      const thread = await planner.get(id);
      if (state.local.sending || disposed || generation !== state.local.selectionGeneration) return;
      state.local.thread = thread;
      state.local.busy = thread.status === "running";
      state.local.confirmingDelete = "";
      setMenuOpen(false);
      subscribeThread(thread.id);
      setSource("local");
      renderThread();
      setStatus("已打开规划线程《" + (thread.title || "未命名规划") + "》");
    } catch (error) {
      setStatus("打开规划线程失败：" + errorText(error), true);
    }
  }

  async function deleteThread(id) {
    try {
      await planner.remove(id);
      if (state.local.thread?.id === id) {
        state.local.thread = null;
        if (threadSubscription) {
          threadSubscription.dispose();
          threadSubscription = null;
        }
      }
      state.local.confirmingDelete = "";
      await loadThreads();
      renderThread();
      setStatus("规划线程已删除");
    } catch (error) {
      setStatus("删除失败：" + errorText(error), true);
    }
  }

  async function renameThread(id, title) {
    const value = String(title ?? "").trim();
    if (!value) {
      setStatus("标题不能为空", true);
      return;
    }
    state.local.renameBusy = true;
    renderHistory();
    try {
      const stored = await renamePlannerThread({ planner }, id, value);
      if (state.local.thread?.id === id) state.local.thread = stored.thread;
      state.local.threads = stored.threads;
      state.local.renamingId = "";
      state.local.renameDraft = "";
      renderThread();
      setStatus("标题已改为《" + (stored.thread?.title || value) + "》");
    } catch (error) {
      setStatus("重命名失败：" + errorText(error), true);
    } finally {
      state.local.renameBusy = false;
      renderHistory();
    }
  }

  // --- handoff -------------------------------------------------------------
  function openPreview(text, from, extra = {}) {
    const preserved = extra.preserveFields && state.preview ? state.preview : null;
    state.pickOpen = false;
    state.committed = null;
    state.previewSeq = (Number(state.previewSeq) || 0) + 1;
    state.previewDraftSeq = (Number(state.previewDraftSeq) || 0) + 1;
    const draftId = state.previewDraftSeq;
    const initialText = String(text || "");
    state.preview = {
      draftId,
      seq: state.previewSeq,
      text: initialText,
      rawText: typeof extra.rawText === "string" ? extra.rawText : (from === "web" ? initialText : ""),
      summarizedText: typeof extra.summarizedText === "string" ? extra.summarizedText : "",
      variant: extra.variant === "summary" ? "summary" : "raw",
      site: String(extra.site || state.extraction?.site || ""),
      from,
      target: preserved ? preserved.target : "",
      mode: preserved ? preserved.mode : "normal",
      cwd: preserved ? preserved.cwd : "",
      model: preserved ? preserved.model : pickHandoffModel(state.local.model, state.handoffModels),
      title: preserved ? preserved.title : "",
      confirmed: null,
      confirmedPayload: null,
    };
    renderPreview();
    applyChrome();
    void loadModels().then(() => renderPreview()).catch(() => {});
    void populatePreviewTargets(draftId);
    void populatePreviewModels(draftId);
  }

  async function populatePreviewModels(draftId = state.preview?.draftId) {
    if (!el.previewModel) return;
    const list = await loadHandoffModels();
    if (!state.preview || (draftId && state.preview.draftId !== draftId)) return;
    if (!state.preview.model && !state.preview.confirmed && !state.preview.busy && !state.preview.committing) {
      state.preview.model = pickHandoffModel(state.local.model, list);
    }
    renderPreviewModels(state.preview.model, list);
    syncPreviewControls(state.preview);
  }

  async function populatePreviewTargets(draftId = state.preview?.draftId) {
    if (!el.previewTarget) return;
    try {
      const sessions = await loadSessions(true);
      if (!state.preview || (draftId && state.preview.draftId !== draftId)) return;
      const fragment = document.createDocumentFragment();
      const fresh = document.createElement("option");
      fresh.value = "";
      fresh.textContent = "新会话";
      fragment.append(fresh);
      for (const session of sessions) {
        const option = document.createElement("option");
        option.value = String(session.id);
        option.textContent = String(session.title || session.id);
        fragment.append(option);
      }
      el.previewTarget.replaceChildren(fragment);
      el.previewTarget.value = state.preview?.target || "";
    } catch (error) {
      setStatus("读取会话列表失败：" + errorText(error), true);
    }
  }

  function previewValues() {
    if (!state.preview) return null;
    return {
      text: String(el.previewText?.value ?? state.preview.text),
      target: String(el.previewTarget?.value ?? state.preview.target),
      mode: String(el.previewMode?.value ?? state.preview.mode),
      cwd: String(el.previewCwd?.value ?? state.preview.cwd),
      model: String(el.previewModel?.value ?? state.preview.model),
      title: String(el.previewTitle?.value ?? state.preview.title),
    };
  }

  async function previewHandoff() {
    const current = previewValues();
    if (!current) return;
    const payload = handoffInput(current);
    const nextSeq = Math.max(Number(state.previewSeq) || 0, Number(state.preview?.seq) || 0) + 1;
    state.previewSeq = nextSeq;
    if (!payload.text) {
      state.committed = null;
      state.preview = { ...state.preview, ...current, seq: nextSeq, confirmed: null, confirmedPayload: null, busy: false, error: "交接内容不能为空" };
      renderPreview();
      return;
    }
    state.committed = null;
    state.preview = { ...state.preview, ...current, seq: nextSeq, error: "", confirmed: null, confirmedPayload: null, busy: true };
    renderPreview();
    try {
      const confirmed = await handoff.preview(payload);
      if (!state.preview || state.preview.seq !== nextSeq) return;
      const latest = previewValues();
      if (!latest || !sameHandoffPayload(handoffInput(latest), payload)) {
        state.preview = { ...state.preview, ...latest, confirmed: null, confirmedPayload: null, busy: false };
        renderPreview();
        return;
      }
      state.preview = { ...state.preview, confirmed, confirmedPayload: payload, busy: false };
    } catch (error) {
      if (!state.preview || state.preview.seq !== nextSeq) return;
      state.preview = { ...state.preview, confirmed: null, confirmedPayload: null, busy: false, error: errorText(error) };
    }
    renderPreview();
  }

  async function commitHandoff() {
    const preview = state.preview;
    if (!preview || preview.committing || preview.busy || state.committed) return;
    const current = previewValues();
    if (!current) return;
    const payload = handoffInput(current);
    if (!preview.confirmed || !preview.confirmedPayload || !sameHandoffPayload(preview.confirmedPayload, payload)) {
      state.preview = {
        ...preview,
        ...current,
        confirmed: null,
        confirmedPayload: null,
        error: preview.confirmed ? "交接内容或目标已变更，请重新预览确认。" : "请先预览确认内容与目标。",
      };
      renderPreview();
      return;
    }
    const commitDraftId = preview.draftId || 0;
    state.preview = { ...preview, ...current, committing: true, error: "" };
    renderPreview();
    try {
      const result = await handoff.commit(payload);
      const sessionId = String(result?.sessionId || "");
      setStatus("已交接，会话 " + sessionId);
      if (!state.preview || (commitDraftId && state.preview.draftId !== commitDraftId)) return;
      state.committed = { sessionId, created: result?.created !== false };
      state.preview = { ...state.preview, committing: false, confirmed: null, confirmedPayload: null };
      void refreshOpenButton();
    } catch (error) {
      if (!state.preview || (commitDraftId && state.preview.draftId !== commitDraftId)) return;
      state.preview = { ...state.preview, committing: false, error: errorText(error) };
    }
    renderPreview();
  }

  async function refreshOpenButton() {
    if (!el.openSessionBtn) return;
    let list = [];
    if (commands) {
      try {
        list = await commands.list();
      } catch {
        list = [];
      }
    }
    const target = openTarget(list, state.committed?.sessionId || "");
    const view = openButtonState(target, { commands, sessions });
    el.openSessionBtn.dataset.mode = view.mode;
    el.openSessionBtn.disabled = view.disabled;
    el.openSessionBtn.textContent = view.label;
    el.openSessionBtn.title = view.title;
    scheduleSync();
  }

  async function openCommitted() {
    if (!state.committed) return;
    let list = [];
    if (commands) {
      try {
        list = await commands.list();
      } catch {
        list = [];
      }
    }
    const target = openTarget(list, state.committed.sessionId);
    try {
      const result = await openSessionTarget({ commands, sessions }, target);
      setStatus(result.via === "command"
        ? "已请求在同屏会话中打开 " + target.args.sessionId
        : "已在 Code 中打开会话 " + target.args.sessionId);
    } catch (error) {
      setStatus("打开会话失败：" + errorText(error), true);
    }
  }

  // --- rendering -----------------------------------------------------------
  function applyChrome() {
    for (const [name, target] of Object.entries(el.sourceButtons || {})) {
      target.setAttribute("aria-pressed", String(state.source === name));
    }
    if (el.webPane) el.webPane.hidden = state.source !== "web" || state.pickOpen || Boolean(state.preview);
    if (el.localPane) el.localPane.hidden = state.source !== "local" || state.pickOpen || Boolean(state.preview);
    if (el.pickPane) el.pickPane.hidden = !state.pickOpen;
    if (el.previewPane) el.previewPane.hidden = !state.preview;
    if (el.webTools) el.webTools.hidden = state.source !== "web";
    if (el.localTools) el.localTools.hidden = state.source !== "local";
    if (el.composer) el.composer.hidden = state.source !== "local" || state.pickOpen || Boolean(state.preview);
    if (el.webDraft) {
      const has = Boolean(state.drafts.web.trim());
      el.webDraft.hidden = !has || state.source !== "web" || state.pickOpen || Boolean(state.preview);
      if (has) el.webDraftText.textContent = "网页草稿 · " + state.drafts.web.length + " 字符";
    }
    scheduleSync();
  }

  function renderToolbar() {
    if (!el.siteChips) return;
    el.siteChips.replaceChildren();
    for (const site of state.sites) {
      const chip = button(site.label);
      chip.className = "ekd-site";
      const current = normalizeUrl(site.url) === normalizeUrl(state.viewUrl);
      chip.setAttribute("aria-pressed", String(current));
      chip.title = site.url;
      chip.addEventListener("click", () => void navigate(site.url));
      el.siteChips.append(chip);
    }
    if (!state.sites.length) el.siteChips.append(node("span", "ekd-note", "没有常用站点"));
    if (el.address) el.address.value = state.viewUrl || state.lastWebUrl || "";
    if (el.backBtn) el.backBtn.disabled = !state.view;
    if (el.reloadBtn) el.reloadBtn.disabled = !state.view;
    if (el.extractBtn) {
      el.extractBtn.disabled = !state.view || state.busy === "extract" || state.summarize.busy;
      el.extractBtn.textContent = state.busy === "extract" ? "读取中…" : "直接提取";
    }
    if (el.summarizeExtractBtn) {
      el.summarizeExtractBtn.hidden = state.summarize.busy;
      el.summarizeExtractBtn.disabled = !state.view || state.busy === "extract" || state.summarize.busy;
      el.summarizeExtractBtn.textContent = "模型归纳";
    }
    if (el.cancelSummarizeBtn) {
      el.cancelSummarizeBtn.hidden = !state.summarize.busy;
    }
    if (el.webStatus) {
      el.webStatus.textContent = state.view ? "" : "网页视图尚未打开";
      el.webStatus.hidden = Boolean(state.view);
    }
    renderSiteEditors();
  }

  function renderSiteEditors() {
    if (!el.siteEditorList) return;
    el.siteEditorList.replaceChildren();
    for (const site of state.sites) {
      const row = node("div", "ekd-item");
      const name = node("span", "ekd-grow", site.label);
      name.title = site.url;
      const remove = button("移除", "ekd-danger");
      remove.addEventListener("click", () => {
        state.sites = removeSite(state.sites, site.id);
        void persist(STORAGE_KEYS.sites, state.sites);
        renderToolbar();
        renderSiteEditors();
      });
      row.append(name, remove);
      el.siteEditorList.append(row);
    }
    if (!state.sites.length) el.siteEditorList.append(node("p", "ekd-note", "站点列表为空；默认站点只在首次运行时写入。"));
  }

  function renderThread() {
    if (!el.threadList) return;
    const stickToBottom = el.threadScroll && nearBottom(el.threadScroll);
    const thread = state.local.thread;
    el.threadList.replaceChildren();
    if (!thread) {
      const empty = node("div", "ekd-empty");
      empty.append(
        node("strong", "", "开始一次本地规划"),
        node("span", "", "不依赖已有会话；交接到 Code 时才创建会话。"),
      );
      el.threadList.append(empty);
    } else {
      for (const message of thread.messages || []) el.threadList.append(messageView(message));
      if (thread.approval) el.threadList.append(approvalCard(thread.approval));
      if (thread.error) {
        const wrap = node("div", "ekd-msg");
        wrap.append(node("div", "ekd-status is-error", thread.error));
        if (thread.status === "quota") {
          const retry = button("解除等待后继续");
          retry.disabled = state.local.busy;
          retry.addEventListener("click", () => void retryLocal());
          wrap.append(retry);
        }
        el.threadList.append(wrap);
      }
    }
    const status = thread?.status || "";
    if (el.runStatus) {
      el.runStatus.textContent = status === "running" ? "生成中…" : status === "quota" ? "等待额度恢复" : status === "error" ? "上次未完成" : status === "stopped" ? "已停止" : "";
      el.runStatus.hidden = !el.runStatus.textContent;
    }
    if (el.stopBtn) el.stopBtn.hidden = status !== "running";
    if (el.sendBtn) {
      el.sendBtn.disabled = state.local.sending || state.local.busy || status === "running";
      el.sendBtn.textContent = state.local.sending ? "发送中…" : "发送";
    }
    if (el.threadTitle) el.threadTitle.textContent = thread?.title || "新规划";
    if (el.newThread) el.newThread.disabled = state.local.sending || state.local.busy || status === "running";
    if (el.workspace) {
      const workspace = thread?.workspace || "";
      el.workspace.textContent = workspace || (surface === "popout" ? "首轮使用弹出时的项目目录" : "首轮使用当前项目目录");
      el.workspace.title = workspace;
    }
    if (el.modelSelect) {
      el.modelSelect.replaceChildren();
      for (const model of state.local.models) {
        const option = document.createElement("option");
        option.value = model.id;
        option.textContent = model.name;
        el.modelSelect.append(option);
      }
      if (!state.local.models.length) {
        const option = document.createElement("option");
        option.value = "";
        option.textContent = "没有可用模型";
        el.modelSelect.append(option);
      }
      el.modelSelect.value = state.local.model;
      el.modelSelect.disabled = state.local.sending || state.local.busy || !state.local.models.length;
    }
    if (el.presetSelect) el.presetSelect.value = state.local.preset;
    if (el.customRow) el.customRow.hidden = state.local.preset !== "custom";
    if (el.customContext) el.customContext.checked = state.local.custom.context;
    if (el.customSkills) el.customSkills.checked = state.local.custom.skills;
    if (el.customTools) el.customTools.checked = state.local.custom.tools;
    if (stickToBottom && state.local.thread) {
      el.threadScroll.scrollTop = el.threadScroll.scrollHeight;
    }
  }

  function messageView(message) {
    const mine = message.role === "user";
    const wrapper = node("article", "ekd-msg" + (mine ? " is-mine" : ""));
    wrapper.dataset.messageId = message.id;
    const head = node("div", "ekd-msg-head");
    head.append(node("span", "ekd-who", mine ? "你" : message.role === "assistant" ? "规划" : message.role === "tool" ? "工具" : String(message.role || "")));
    if (message.at) {
      const time = document.createElement("time");
      time.dateTime = message.at;
      time.textContent = clockText(message.at);
      head.append(time);
    }
    wrapper.append(head);
    if (message.tool) {
      const details = document.createElement("details");
      details.className = "ekd-tool";
      const summary = document.createElement("summary");
      summary.textContent = "工具 · " + message.tool + (message.text ? "（" + message.text.length + " 字符）" : "");
      details.append(summary, node("div", "", message.text || ""));
      wrapper.append(details);
    } else {
      for (const part of splitCodeBlocks(message.text || "")) {
        if (part.type === "code") {
          const pre = document.createElement("pre");
          pre.className = "ekd-code";
          pre.textContent = part.code;
          const send = button("交接此段");
          send.addEventListener("click", () => openPreview(buildHandoffText([{ ...message, text: part.code, label: "代码段" }], new Set([message.id]), { sessionTitle: state.local.thread?.title }), "local"));
          pre.append(send);
          wrapper.append(pre);
        } else if (part.text.trim()) {
          wrapper.append(node("div", "ekd-msg-text", part.text));
        }
      }
    }
    const actions = node("div", "ekd-msg-actions");
    const copy = button("复制");
    copy.addEventListener("click", async () => {
      try {
        if (!navigator.clipboard) throw new Error("当前宿主没有剪贴板接口");
        await navigator.clipboard.writeText(message.text || "");
        copy.textContent = "已复制";
      } catch {
        copy.textContent = "复制失败";
      }
      window.setTimeout(() => {
        copy.textContent = "复制";
      }, 1600);
    });
    const pick = button("选择交接");
    pick.addEventListener("click", () => openPreview(buildHandoffText([message], new Set([message.id]), { sessionTitle: state.local.thread?.title }), "local"));
    actions.append(copy, pick);
    wrapper.append(actions);
    return wrapper;
  }

  function approvalCard(approval) {
    const card = node("div", "ekd-msg");
    card.append(node("strong", "", "需要审批：" + (approval.title || "工具写入")));
    if (state.local.thread?.workspace) card.append(node("div", "ekd-workspace", "工作目录 · " + state.local.thread.workspace));
    if (approval.detail) card.append(node("div", "ekd-msg-text", approval.detail));
    const row = node("div", "ekd-row");
    const allow = button("允许", "ekd-primary");
    allow.addEventListener("click", () => void approveLocal(approval.id, true));
    const deny = button("拒绝", "ekd-danger");
    deny.addEventListener("click", () => void approveLocal(approval.id, false));
    row.append(allow, deny);
    card.append(row);
    return card;
  }

  function renderHistory() {
    if (!el.historyList) return;
    el.historyList.replaceChildren();
    if (!state.local.threads.length) {
      el.historyList.append(node("p", "ekd-note", "还没有本插件创建的规划线程。"));
      return;
    }
    for (const thread of state.local.threads.slice(0, 12)) {
      const row = node("div", "ekd-item");
      const actions = threadActions(thread, pluginId);
      if (state.local.renamingId === thread.id) {
        const input = document.createElement("input");
        input.type = "text";
        input.className = "ekd-grow";
        input.value = state.local.renameDraft;
        input.setAttribute("aria-label", "规划标题");
        input.maxLength = 200;
        input.title = "最多 200 字节，中文约 66 个字";
        input.addEventListener("input", () => {
          state.local.renameDraft = input.value;
        });
        input.addEventListener("keydown", event => {
          if (event.key === "Enter") {
            event.preventDefault();
            void renameThread(thread.id, input.value);
          } else if (event.key === "Escape") {
            event.preventDefault();
            cancelRename();
          }
        });
        const save = button("保存", "ekd-primary");
        save.disabled = state.local.renameBusy;
        save.addEventListener("click", () => void renameThread(thread.id, input.value));
        const cancel = button("取消");
        cancel.addEventListener("click", () => cancelRename());
        row.append(input, save, cancel);
        el.historyList.append(row);
        queueMicrotask(() => input.focus());
        continue;
      }
      const open = button("", "ekd-grow");
      open.textContent = String(thread.title || "未命名规划");
      open.title = thread.title || "";
      open.addEventListener("click", () => void openThread(thread.id));
      row.append(open, node("small", "", String(thread.status || "")));
      if (state.local.confirmingDelete === thread.id) {
        const yes = button("确认删除", "ekd-danger");
        yes.addEventListener("click", () => void deleteThread(thread.id));
        const no = button("取消");
        no.addEventListener("click", () => {
          state.local.confirmingDelete = "";
          renderHistory();
        });
        row.append(yes, no);
      } else {
        const rename = button("重命名");
        rename.disabled = !actions.canRename || state.local.renameBusy;
        rename.title = actions.canRename ? "修改这个规划线程的标题" : "只能重命名本插件创建的规划线程";
        rename.addEventListener("click", () => startRename(thread));
        const remove = button("删除");
        remove.disabled = !actions.canDelete;
        remove.title = actions.canDelete ? "删除这个规划线程" : "只能删除本插件创建的规划线程";
        remove.addEventListener("click", () => {
          state.local.confirmingDelete = thread.id;
          renderHistory();
        });
        row.append(rename, remove);
      }
      el.historyList.append(row);
    }
  }

  function startRename(thread) {
    state.local.renamingId = thread.id;
    state.local.renameDraft = String(thread.title || "");
    state.local.confirmingDelete = "";
    renderHistory();
  }

  function cancelRename() {
    state.local.renamingId = "";
    state.local.renameDraft = "";
    renderHistory();
  }

  function renderSummarizeSelect(selectNode) {
    if (!selectNode) return;
    selectNode.replaceChildren();
    const list = state.local.models;
    if (!list.length) {
      const opt = document.createElement("option");
      opt.value = "";
      opt.textContent = "无可用归纳模型";
      selectNode.append(opt);
      selectNode.disabled = true;
      return;
    }
    for (const model of list) {
      const opt = document.createElement("option");
      opt.value = model.id;
      opt.textContent = model.name;
      selectNode.append(opt);
    }
    const wanted = state.summarize.model || state.local.model || list[0]?.id || "";
    state.summarize.model = wanted;
    selectNode.value = wanted;
    selectNode.disabled = state.summarize.busy;
  }

  function renderPicker() {
    const extraction = state.extraction;
    if (!el.pickList) return;
    el.pickList.replaceChildren();
    el.pickNotice.textContent = extraction?.notice || "";
    renderSummarizeSelect(el.pickSummarizeModel);
    if (!extraction) {
      updatePickCount();
      return;
    }
    for (const message of extraction.messages) {
      const label = document.createElement("label");
      label.className = "ekd-check";
      const box = document.createElement("input");
      box.type = "checkbox";
      box.checked = state.selected.has(message.id);
      box.addEventListener("change", () => {
        if (box.checked) state.selected.add(message.id);
        else state.selected.delete(message.id);
        updatePickCount();
      });
      const body = node("span", "");
      const roleTag = isPlanningMessage(message) && message.role === "assistant"
        ? (message.label || "助手") + " · 规划内容"
        : (message.label || "");
      body.append(node("strong", "", roleTag), document.createTextNode("\n"), node("span", "", message.text.slice(0, 300)));
      label.append(box, body);
      el.pickList.append(label);
    }
    if (extraction.truncated) el.pickList.append(node("p", "ekd-note", "内容过长，已按上限截断。"));
    updatePickCount();
  }

  function updatePickCount() {
    if (!el.pickCount) return;
    const total = state.extraction?.messages.length || 0;
    const empty = state.selected.size === 0;
    el.pickCount.textContent = "已选 " + state.selected.size + " / " + total;
    if (el.pickGo) el.pickGo.disabled = empty || state.summarize.busy;
    if (el.pickSummarizeBtn) {
      el.pickSummarizeBtn.hidden = state.summarize.busy;
      el.pickSummarizeBtn.disabled = empty || !state.local.models.length;
    }
    if (el.pickCancelSummarizeBtn) {
      el.pickCancelSummarizeBtn.hidden = !state.summarize.busy;
    }
  }

  function renderPreviewModels(selectedModel, list = state.handoffModels) {
    if (!el.previewModel) return;
    el.previewModel.replaceChildren();
    const fallback = document.createElement("option");
    fallback.value = "";
    fallback.textContent = "默认（跟随官方客户端）";
    el.previewModel.append(fallback);
    for (const model of list) {
      const option = document.createElement("option");
      option.value = model.id;
      option.textContent = model.name;
      el.previewModel.append(option);
    }
    if (selectedModel && !list.some(model => model.id === selectedModel)) {
      const option = document.createElement("option");
      option.value = selectedModel;
      option.textContent = selectedModel;
      el.previewModel.append(option);
    }
    el.previewModel.value = selectedModel || "";
  }

  function renderPreviewStructure(preview) {
    if (!preview || !el.previewPane) return;
    if (el.previewText) el.previewText.value = preview.text;
    if (el.previewMode) el.previewMode.value = preview.mode;
    if (el.previewTarget) el.previewTarget.value = preview.target;
    if (el.previewCwd) el.previewCwd.value = preview.cwd;
    if (el.previewTitle) el.previewTitle.value = preview.title;
    renderPreviewModels(preview.model, state.handoffModels);
    if (el.previewVariantBar) {
      const isWeb = preview.from === "web";
      el.previewVariantBar.hidden = !isWeb;
      if (isWeb) {
        renderSummarizeSelect(el.previewSummarizeModel);
        const hasBoth = Boolean(preview.summarizedText && preview.rawText);
        if (el.previewVariantLabel) {
          el.previewVariantLabel.textContent = hasBoth
            ? (preview.variant === "summary" ? "当前：模型归纳版" : "当前：直接提取原文")
            : "当前：直接提取 / 粘贴原文";
        }
        if (el.previewToggleVariantBtn) {
          el.previewToggleVariantBtn.hidden = !hasBoth;
          el.previewToggleVariantBtn.disabled = state.summarize.busy || Boolean(preview.committing);
          el.previewToggleVariantBtn.textContent = preview.variant === "summary" ? "切回提取原文" : "切回模型归纳版";
        }
        if (el.previewRunSummarizeBtn) {
          el.previewRunSummarizeBtn.hidden = state.summarize.busy;
          el.previewRunSummarizeBtn.disabled = Boolean(preview.committing) || !state.local.models.length;
          el.previewRunSummarizeBtn.textContent = hasBoth ? "重新归纳" : "用模型归纳规划";
        }
        if (el.previewCancelSummarizeBtn) {
          el.previewCancelSummarizeBtn.hidden = !state.summarize.busy;
        }
      }
    }
  }

  function renderPreview() {
    const preview = state.preview;
    if (!preview || !el.previewPane) return;
    renderPreviewStructure(preview);
    syncPreviewControls(preview);
    scheduleSync();
  }

  /**
   * Re-renders only what a keystroke can change. Rewriting the textarea's value
   * on every input would move the caret to the end mid-sentence, so the value is
   * written by renderPreviewStructure and left alone while the user types.
   */
  function syncPreviewControls(preview) {
    if (!preview || !el.previewPane) return;
    // The textarea owns its own value while the user types; only the structural
    // render writes it.
    const existing = Boolean(preview.target);
    const locked = Boolean(preview.committing);
    const summarizing = Boolean(state.summarize.busy);
    if (el.previewNewFields) el.previewNewFields.hidden = existing;
    if (el.previewExistingNote) el.previewExistingNote.hidden = !existing;
    if (el.previewText) el.previewText.disabled = locked || summarizing;
    if (el.previewTarget) el.previewTarget.disabled = locked;
    if (el.previewMode) el.previewMode.disabled = locked;
    if (el.previewCwd) el.previewCwd.disabled = locked;
    if (el.previewTitle) el.previewTitle.disabled = locked;
    if (el.previewModel) el.previewModel.disabled = existing || locked;
    const currentValues = previewValues() || preview;
    const validConfirmed = preview.confirmed && preview.confirmedPayload && sameHandoffPayload(preview.confirmedPayload, handoffInput(currentValues))
      ? preview.confirmed
      : null;
    if (el.previewConfirm) {
      el.previewConfirm.hidden = Boolean(state.committed);
      el.previewConfirm.disabled = Boolean(preview.busy || preview.committing || summarizing || state.committed);
      el.previewConfirm.textContent = preview.busy ? "预览中…" : "预览";
    }
    if (el.previewCommit) {
      el.previewCommit.hidden = !validConfirmed;
      el.previewCommit.disabled = Boolean(preview.committing || summarizing) || !validConfirmed;
      el.previewCommit.textContent = preview.committing ? "交接中…" : validConfirmed?.newSession ? "创建会话并交接" : "交接到已有会话";
    }
    if (el.previewSummary) {
      el.previewSummary.hidden = !validConfirmed;
      el.previewSummary.textContent = validConfirmed
        ? "将写入：" + (validConfirmed.newSession ? "新会话" : "会话 " + validConfirmed.targetId) + " · 模式 " + validConfirmed.mode + " · " + validConfirmed.bytes + " 字节"
        : "";
    }
    if (el.previewError) {
      el.previewError.hidden = !preview.error;
      el.previewError.textContent = preview.error || "";
    }
    if (el.previewResult) {
      el.previewResult.hidden = !state.committed;
      if (state.committed) {
        el.previewResultText.textContent = "已交接，会话 ID：" + state.committed.sessionId + (state.committed.created ? "（新建）" : "（已有会话）");
      }
    }
  }

  // --- skeleton ------------------------------------------------------------
  function siteChips() {
    const wrap = node("div", "ekd-sites");
    wrap.setAttribute("role", "group");
    wrap.setAttribute("aria-label", "站点快切");
    return wrap;
  }

  function buildBar() {
    const bar = node("div", "ekd-bar");
    bar.setAttribute("role", "toolbar");
    bar.setAttribute("aria-label", "规划工具条");

    const sourceGroup = node("div", "ekd-src");
    sourceGroup.setAttribute("role", "group");
    sourceGroup.setAttribute("aria-label", "规划来源");
    el.sourceButtons = {};
    for (const [id, label] of [["web", "网页"], ["local", "本地"]]) {
      const item = button(label);
      item.setAttribute("aria-pressed", String(state.source === id));
      item.addEventListener("click", () => setSource(id));
      el.sourceButtons[id] = item;
      sourceGroup.append(item);
    }
    bar.append(sourceGroup);

    const webTools = node("div", "ekd-tools");
    el.webTools = webTools;
    el.backBtn = iconButton("back", "后退");
    el.backBtn.addEventListener("click", () => void pageAction("back", "后退"));
    el.reloadBtn = iconButton("reload", "刷新");
    el.reloadBtn.addEventListener("click", () => void pageAction("reload", "刷新"));
    el.siteChips = siteChips();
    el.extractBtn = button("直接提取", "ekd-outline", "按站点去噪提取页面对话，勾选后直接交接（不经模型归纳）");
    el.extractBtn.addEventListener("click", () => void extract({ summarize: false }));
    el.summarizeExtractBtn = button("模型归纳", "ekd-primary", "提取页面对话并由模型归纳提炼规划方案（可选项）");
    el.summarizeExtractBtn.addEventListener("click", () => void extract({ summarize: true }));
    el.cancelSummarizeBtn = button("取消归纳", "ekd-danger", "取消正在进行的模型归纳");
    el.cancelSummarizeBtn.hidden = true;
    el.cancelSummarizeBtn.addEventListener("click", () => void cancelSummarize());
    webTools.append(el.backBtn, el.reloadBtn, el.siteChips, el.extractBtn, el.summarizeExtractBtn, el.cancelSummarizeBtn);
    bar.append(webTools);

    const localTools = node("div", "ekd-tools");
    el.localTools = localTools;
    el.threadTitle = node("span", "ekd-chip", "新规划");
    const newThread = button("新建规划");
    el.newThread = newThread;
    newThread.addEventListener("click", () => {
      if (state.local.sending || state.local.busy || state.local.thread?.status === "running") return;
      state.local.selectionGeneration++;
      subscribeThread(null);
      state.local.thread = null;
      state.local.busy = false;
      renderThread();
      setStatus("");
      el.draft.focus();
    });
    localTools.append(el.threadTitle, newThread);
    bar.append(localTools);

    const more = node("div", "ekd-more");
    el.moreBtn = iconButton("more", "更多");
    el.moreBtn.setAttribute("aria-haspopup", "true");
    el.moreBtn.setAttribute("aria-expanded", "false");
    el.menu = node("div", "ekd-menu");
    el.menu.hidden = true;
    el.menu.setAttribute("role", "menu");
    el.moreBtn.addEventListener("click", event => {
      event.stopPropagation();
      const open = el.menu.hidden;
      setMenuOpen(open);
      if (open) {
        renderHistory();
        void loadThreads();
        renderSiteEditors();
        el.address.value = state.viewUrl || state.lastWebUrl || "";
        el.address.focus();
        void refreshCurrentUrl();
      }
      scheduleSync();
    });
    el.menu.addEventListener("click", event => event.stopPropagation());

    const addressLabel = document.createElement("label");
    addressLabel.append(node("span", "", "地址（仅 http/https）"));
    el.address = document.createElement("input");
    el.address.type = "url";
    el.address.placeholder = "https://";
    el.address.setAttribute("inputmode", "url");
    el.address.addEventListener("keydown", event => {
      if (event.key !== "Enter") return;
      event.preventDefault();
      setMenuOpen(false);
      void navigate(el.address.value);
    });
    const go = button("打开");
    go.addEventListener("click", () => {
      setMenuOpen(false);
      void navigate(el.address.value);
    });
    const addressRow = node("div", "ekd-row");
    addressRow.append(el.address, go);
    addressLabel.append(addressRow);
    el.menu.append(addressLabel, node("div", "ekd-sep"), node("p", "ekd-note", "常用站点"));
    el.siteEditorList = node("div", "ekd-list");
    el.menu.append(el.siteEditorList);
    const addRow = node("div", "ekd-row");
    el.newSiteLabel = document.createElement("input");
    el.newSiteLabel.placeholder = "名称";
    el.newSiteLabel.setAttribute("aria-label", "站点名称");
    el.newSiteUrl = document.createElement("input");
    el.newSiteUrl.placeholder = "https://";
    el.newSiteUrl.setAttribute("aria-label", "站点地址");
    el.newSiteUrl.setAttribute("inputmode", "url");
    const add = iconButton("plus", "添加站点");
    add.addEventListener("click", () => {
      const result = addSite(state.sites, { label: el.newSiteLabel.value, url: el.newSiteUrl.value });
      if (!result.ok) {
        setStatus(result.error, true);
        return;
      }
      state.sites = result.sites;
      el.newSiteLabel.value = "";
      el.newSiteUrl.value = "";
      void persist(STORAGE_KEYS.sites, state.sites);
      renderToolbar();
      renderSiteEditors();
      setStatus("站点已添加");
    });
    addRow.append(el.newSiteLabel, el.newSiteUrl, add);
    el.menu.append(addRow);
    el.menu.append(node("div", "ekd-sep"));
    const paste = button("粘贴内容导入");
    paste.addEventListener("click", () => void pasteImport());
    const fillSummary = button("把会话摘要填入网页");
    fillSummary.addEventListener("click", () => void pickSessionForFill("summary"));
    const fillDiff = button("把会话 Diff 填入网页");
    fillDiff.addEventListener("click", () => void pickSessionForFill("diff"));
    el.menu.append(paste, fillSummary, fillDiff);
    el.menu.append(node("p", "ekd-note", "只填入输入框并读回校验，不发送、不读取网页回复。"));
    el.menu.append(node("div", "ekd-sep"), node("p", "ekd-note", "窗口"));
    const external = button("在系统浏览器打开");
    external.addEventListener("click", () => {
      setMenuOpen(false);
      void openExternal();
    });
    el.menu.append(external);
    if (surface === "popout") {
      const closeWindow = button("关闭窗口");
      closeWindow.addEventListener("click", () => void closePopout());
      el.menu.append(closeWindow);
    } else if (windowHost) {
      const popout = button("弹出独立窗口");
      popout.title = "在独立窗口里继续这个规划面板；原右侧面板会关闭，插件不会被停用";
      popout.addEventListener("click", () => void popOut());
      el.menu.append(popout);
    }
    el.menu.append(node("div", "ekd-sep"), node("p", "ekd-note", "规划线程"));
    el.historyList = node("div", "ekd-list");
    el.menu.append(el.historyList);
    el.menu.append(node("p", "ekd-note", GAPS.directory));
    more.append(el.moreBtn, el.menu);
    bar.append(more);
    return bar;
  }

  function buildWebPane() {
    const pane = node("div", "ekd-view");
    el.webPane = pane;
    pane.style.display = "flex";
    pane.style.flexDirection = "column";
    el.viewport = node("div", "ekd-view");
    el.viewport.setAttribute("aria-label", "网页内容");
    el.fallback = node("div", "ekd-fallback");
    el.fallbackText = node("p", "");
    const hint = node("p", "", "登录风险：Google 账号可能拒绝在内嵌页面登录；claude.ai 与 chatgpt.com 可能出现人机验证。人机验证只能在可见网页里由你本人完成，本插件不做任何绕过。");
    el.fallback.append(node("strong", "", "网页视图不可用"), el.fallbackText, hint);
    el.fallback.hidden = true;
    el.webStatus = node("div", "ekd-status");
    el.webStatus.style.position = "absolute";
    el.webStatus.style.left = "12px";
    el.webStatus.style.bottom = "10px";
    el.fillChoice = node("div", "ekd-menu");
    el.fillChoice.hidden = true;
    el.fillChoice.dataset.ekdPicker = "fill-choice";
    el.fillChoice.setAttribute("aria-label", "选择填入方式");
    el.fillChoice.style.position = "static";
    el.fillChoice.style.margin = "6px";
    el.sessionPicker = node("div", "ekd-menu");
    el.sessionPicker.hidden = true;
    el.sessionPicker.dataset.ekdPicker = "session";
    el.sessionPicker.setAttribute("aria-label", "选择代码会话");
    el.sessionPicker.style.position = "static";
    el.sessionPicker.style.margin = "6px";
    el.sessionPickerList = node("div", "ekd-list");
    el.webDraft = node("div", "ekd-draftbar");
    el.webDraft.hidden = true;
    el.webDraftText = node("span", "ekd-grow", "");
    const resume = button("继续编辑");
    resume.addEventListener("click", () => openPreview(state.drafts.web, "web"));
    const clear = button("清空草稿", "ekd-danger");
    clear.addEventListener("click", () => {
      setDraft("web", "");
      void persist(STORAGE_KEYS.drafts, state.drafts);
      renderPreview();
    });
    el.webDraft.append(el.webDraftText, resume, clear);
    pane.append(el.viewport, el.webStatus, el.fallback, el.webDraft, el.fillChoice, el.sessionPicker);
    return pane;
  }

  function buildLocalPane() {
    const pane = node("div", "ekd-view");
    el.localPane = pane;
    pane.style.display = "flex";
    pane.style.flexDirection = "column";
    el.threadScroll = node("div", "ekd-scroll");
    el.threadScroll.setAttribute("aria-live", "polite");
    el.threadList = node("div", "ekd-list");
    el.threadScroll.append(el.threadList);
    pane.append(el.threadScroll);
    return pane;
  }

  function buildComposer() {
    const composer = node("div", "ekd-composer");
    el.composer = composer;
    el.draft = document.createElement("textarea");
    el.draft.placeholder = "描述要规划的问题；Ctrl/Cmd+Enter 发送";
    el.draft.setAttribute("aria-label", "规划内容");
    el.draft.value = state.drafts.local;
    el.draft.addEventListener("input", () => {
      state.local.draftRevision++;
      setDraft("local", el.draft.value);
      queueDrafts();
    });
    el.draft.addEventListener("keydown", event => {
      if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
        event.preventDefault();
        void sendLocal();
      }
    });
    el.workspace = node("div", "ekd-workspace");
    composer.append(el.workspace, el.draft);
    const foot = node("div", "ekd-foot");
    el.runStatus = node("span", "ekd-status");
    el.runStatus.hidden = true;
    el.modelSelect = document.createElement("select");
    el.modelSelect.setAttribute("aria-label", "规划模型");
    el.modelSelect.addEventListener("change", () => {
      state.local.model = el.modelSelect.value;
    });
    el.presetSelect = document.createElement("select");
    el.presetSelect.setAttribute("aria-label", "规划预设");
    for (const preset of PRESETS) {
      const option = document.createElement("option");
      option.value = preset.id;
      option.textContent = preset.label;
      option.title = preset.hint;
      el.presetSelect.append(option);
    }
    el.presetSelect.addEventListener("change", () => {
      state.local.preset = el.presetSelect.value;
      renderThread();
    });
    el.stopBtn = iconButton("stop", "停止生成");
    el.stopBtn.hidden = true;
    el.stopBtn.addEventListener("click", () => void stopLocal());
    el.sendBtn = button("发送", "ekd-primary");
    el.sendBtn.addEventListener("click", () => void sendLocal());
    foot.append(el.modelSelect, el.presetSelect, el.runStatus, node("span", "ekd-spacer"), el.stopBtn, el.sendBtn);
    composer.append(foot);

    el.customRow = node("div", "ekd-foot");
    el.customRow.hidden = true;
    const toggle = (label, key) => {
      const wrap = document.createElement("label");
      wrap.className = "ekd-chip";
      wrap.style.display = "inline-flex";
      wrap.style.alignItems = "center";
      wrap.style.gap = "4px";
      const box = document.createElement("input");
      box.type = "checkbox";
      box.addEventListener("change", () => {
        state.local.custom[key] = box.checked;
      });
      wrap.append(box, document.createTextNode(label));
      return { wrap, box };
    };
    const context = toggle("注入上下文", "context");
    const skills = toggle("加载 Skill", "skills");
    const tools = toggle("开放工具", "tools");
    el.customContext = context.box;
    el.customSkills = skills.box;
    el.customTools = tools.box;
    el.customRow.append(context.wrap, skills.wrap, tools.wrap, node("span", "ekd-note", "关闭工具时核心按只读执行"));
    composer.append(el.customRow);
    return composer;
  }

  function buildPickPane() {
    const pane = node("div", "ekd-view");
    el.pickPane = pane;
    pane.style.display = "flex";
    pane.style.flexDirection = "column";
    const head = node("div", "ekd-bar");
    head.append(node("strong", "", "提取结果"));
    el.pickCount = node("span", "ekd-chip", "已选 0 / 0");
    const spacer = node("span", "ekd-spacer");
    spacer.style.flex = "1 1 auto";
    const back = button("返回");
    back.addEventListener("click", () => {
      state.pickOpen = false;
      applyChrome();
    });
    head.append(el.pickCount, spacer, back);

    const filterBar = node("div", "ekd-subbar");
    const onlyPlan = button("仅选规划内容", "ekd-filter-btn", "只勾选助手回复中的规划正文，跳过提问与寒暄");
    onlyPlan.addEventListener("click", () => {
      if (!state.extraction) return;
      state.selected = selectPlanningIds(state.extraction.messages);
      renderPicker();
    });
    const onlyLatest = button("仅选最新回复", "ekd-filter-btn", "只勾选最后一条助手回复");
    onlyLatest.addEventListener("click", () => {
      const list = state.extraction?.messages || [];
      const lastAssistant = [...list].reverse().find(item => item.role === "assistant") || list[list.length - 1];
      state.selected = lastAssistant ? new Set([lastAssistant.id]) : new Set();
      renderPicker();
    });
    const selectAll = button("全选", "ekd-filter-btn");
    selectAll.addEventListener("click", () => {
      state.selected = new Set((state.extraction?.messages || []).map(item => item.id));
      renderPicker();
    });
    const clearAll = button("清空", "ekd-filter-btn");
    clearAll.addEventListener("click", () => {
      state.selected = new Set();
      renderPicker();
    });
    filterBar.append(onlyPlan, onlyLatest, selectAll, clearAll);

    const actionBar = node("div", "ekd-subbar");
    el.pickSummarizeModel = document.createElement("select");
    el.pickSummarizeModel.setAttribute("aria-label", "归纳模型");
    el.pickSummarizeModel.title = "选择用于归纳提炼规划内容的模型（可选功能）";
    el.pickSummarizeModel.addEventListener("change", () => {
      state.summarize.model = el.pickSummarizeModel.value;
    });
    const actionSpacer = node("span", "ekd-spacer");
    actionSpacer.style.flex = "1 1 auto";
    el.pickGo = button("直接交接所选", "ekd-outline", "不经过模型归纳，直接将所选去噪内容带入交接预览");
    el.pickGo.addEventListener("click", () => {
      const messages = selectedMessages();
      if (!messages.length) return;
      const rawText = buildHandoffText(messages, state.selected, { site: state.extraction?.site || "" });
      openPreview(rawText, "web", { rawText, variant: "raw", site: state.extraction?.site || "" });
    });
    el.pickSummarizeBtn = button("模型归纳后交接", "ekd-primary", "用所选模型把勾选内容提炼为结构化规划后再进入交接预览");
    el.pickSummarizeBtn.addEventListener("click", () => {
      const messages = selectedMessages();
      if (!messages.length) return;
      const rawText = buildHandoffText(messages, state.selected, { site: state.extraction?.site || "" });
      void summarizeToPreview(rawText, { site: state.extraction?.site || "", fallbackToPicker: false });
    });
    el.pickCancelSummarizeBtn = button("取消归纳", "ekd-danger");
    el.pickCancelSummarizeBtn.hidden = true;
    el.pickCancelSummarizeBtn.addEventListener("click", () => void cancelSummarize());
    actionBar.append(el.pickSummarizeModel, actionSpacer, el.pickGo, el.pickSummarizeBtn, el.pickCancelSummarizeBtn);

    const scroll = node("div", "ekd-scroll");
    el.pickNotice = node("p", "ekd-note");
    el.pickList = node("div", "ekd-list");
    scroll.append(el.pickNotice, el.pickList);
    pane.append(head, filterBar, actionBar, scroll);
    return pane;
  }

  function buildPreviewPane() {
    const pane = node("div", "ekd-preview");
    el.previewPane = pane;
    const head = node("div", "ekd-bar");
    head.append(node("strong", "", "交接预览"));
    const spacer = node("span", "ekd-spacer");
    spacer.style.flex = "1 1 auto";
    const back = button("返回");
    back.addEventListener("click", () => {
      state.previewSeq = (Number(state.previewSeq) || 0) + 1;
      state.preview = null;
      state.committed = null;
      applyChrome();
    });
    head.append(spacer, back);

    function invalidatePreviewConfirmation() {
      if (!state.preview) return;
      state.committed = null;
      state.previewSeq = (Number(state.previewSeq) || 0) + 1;
      state.preview.seq = state.previewSeq;
      state.preview.confirmed = null;
      state.preview.confirmedPayload = null;
      state.preview.busy = false;
      state.preview.error = "";
      syncPreviewControls(state.preview);
    }

    function bindPreviewField(control, events, apply) {
      const handler = () => {
        if (!state.preview) return;
        apply();
        invalidatePreviewConfirmation();
      };
      for (const name of events) control.addEventListener(name, handler);
    }

    el.previewVariantBar = node("div", "ekd-variant-bar");
    el.previewVariantBar.hidden = true;
    el.previewVariantLabel = node("span", "ekd-status", "当前：直接提取 / 粘贴原文");
    const variantSpacer = node("span", "ekd-spacer");
    variantSpacer.style.flex = "1 1 auto";
    el.previewToggleVariantBtn = button("切回提取原文", "ekd-filter-btn");
    el.previewToggleVariantBtn.hidden = true;
    el.previewToggleVariantBtn.addEventListener("click", () => {
      if (!state.preview || state.preview.from !== "web") return;
      if (!state.preview.rawText || !state.preview.summarizedText) return;
      const nextVariant = state.preview.variant === "summary" ? "raw" : "summary";
      state.preview.variant = nextVariant;
      state.preview.text = nextVariant === "summary" ? state.preview.summarizedText : state.preview.rawText;
      setDraft("web", state.preview.text);
      queueDrafts();
      invalidatePreviewConfirmation();
      renderPreviewStructure(state.preview);
      applyChrome();
    });
    el.previewSummarizeModel = document.createElement("select");
    el.previewSummarizeModel.setAttribute("aria-label", "归纳模型");
    el.previewSummarizeModel.title = "选择用于归纳提炼规划内容的模型";
    el.previewSummarizeModel.addEventListener("change", () => {
      state.summarize.model = el.previewSummarizeModel.value;
    });
    el.previewRunSummarizeBtn = button("用模型归纳规划", "ekd-outline");
    el.previewRunSummarizeBtn.addEventListener("click", () => {
      if (!state.preview || state.preview.from !== "web") return;
      const sourceText = (state.preview.rawText || el.previewText?.value || state.preview.text || "").trim();
      if (!sourceText) {
        setStatus("请先输入或提取要归纳的内容。", true);
        return;
      }
      state.preview.rawText = sourceText;
      void summarizeToPreview(sourceText, {
        site: state.preview.site || state.extraction?.site || "",
        fallbackToPicker: false,
        preserveFields: true,
      });
    });
    el.previewCancelSummarizeBtn = button("取消归纳", "ekd-danger");
    el.previewCancelSummarizeBtn.hidden = true;
    el.previewCancelSummarizeBtn.addEventListener("click", () => void cancelSummarize());
    el.previewVariantBar.append(
      el.previewVariantLabel,
      variantSpacer,
      el.previewToggleVariantBtn,
      el.previewSummarizeModel,
      el.previewRunSummarizeBtn,
      el.previewCancelSummarizeBtn,
    );

    el.previewText = document.createElement("textarea");
    el.previewText.setAttribute("aria-label", "交接内容");
    bindPreviewField(el.previewText, ["input", "change"], () => {
      state.preview.text = el.previewText.value;
      if (state.preview.from === "web") {
        if (state.preview.variant === "summary") {
          state.preview.summarizedText = el.previewText.value;
        } else {
          state.preview.rawText = el.previewText.value;
        }
        setDraft("web", el.previewText.value);
        queueDrafts();
      }
      applyChrome();
    });

    el.previewTarget = document.createElement("select");
    el.previewTarget.setAttribute("aria-label", "交接目标");
    const fresh = document.createElement("option");
    fresh.value = "";
    fresh.textContent = "新会话";
    el.previewTarget.append(fresh);
    bindPreviewField(el.previewTarget, ["change"], () => {
      state.preview.target = el.previewTarget.value;
    });

    el.previewMode = document.createElement("select");
    el.previewMode.setAttribute("aria-label", "交接模式");
    for (const [value, label] of [["normal", "普通"], ["plan", "Plan"], ["tower", "Tower"]]) {
      const option = document.createElement("option");
      option.value = value;
      option.textContent = label;
      el.previewMode.append(option);
    }
    bindPreviewField(el.previewMode, ["change"], () => {
      state.preview.mode = el.previewMode.value;
    });

    el.previewCwd = document.createElement("input");
    el.previewCwd.placeholder = "工作目录（留空使用核心默认）";
    el.previewCwd.setAttribute("aria-label", "新会话工作目录");
    bindPreviewField(el.previewCwd, ["input", "change"], () => {
      state.preview.cwd = el.previewCwd.value;
    });
    el.previewTitle = document.createElement("input");
    el.previewTitle.placeholder = "会话标题";
    el.previewTitle.setAttribute("aria-label", "新会话标题");
    bindPreviewField(el.previewTitle, ["input", "change"], () => {
      state.preview.title = el.previewTitle.value;
    });
    el.previewModel = document.createElement("select");
    el.previewModel.setAttribute("aria-label", "新会话模型");
    bindPreviewField(el.previewModel, ["change"], () => {
      state.preview.model = el.previewModel.value;
    });
    el.previewNewFields = node("div", "ekd-grid");
    el.previewNewFields.append(field("工作目录", el.previewCwd), field("会话标题", el.previewTitle), field("模型", el.previewModel));
    el.previewExistingNote = node("p", "ekd-note", "已有会话保持自己的模型、目录和标题；这三项不会随交接提交。");
    el.previewExistingNote.hidden = true;

    el.previewSummary = node("p", "ekd-note");
    el.previewSummary.hidden = true;
    el.previewError = node("p", "ekd-status is-error");
    el.previewError.hidden = true;

    const actions = node("div", "ekd-row");
    el.previewConfirm = button("预览", "ekd-primary");
    el.previewConfirm.addEventListener("click", () => void previewHandoff());
    el.previewCommit = button("交接", "ekd-primary");
    el.previewCommit.hidden = true;
    el.previewCommit.addEventListener("click", () => void commitHandoff());
    actions.append(el.previewConfirm, el.previewCommit);

    el.previewResult = node("div", "ekd-msg");
    el.previewResult.hidden = true;
    el.previewResultText = node("div", "ekd-msg-text");
    el.openSessionBtn = button("打开会话");
    el.openSessionBtn.addEventListener("click", () => void openCommitted());
    el.previewResult.append(el.previewResultText, el.openSessionBtn);

    const targetField = node("div", "ekd-grid");
    targetField.append(field("目标", el.previewTarget), field("模式", el.previewMode));
    pane.append(head, el.previewVariantBar, el.previewText, targetField, el.previewNewFields, el.previewExistingNote, actions, el.previewSummary, el.previewError, el.previewResult);
    return pane;
  }

  function renderRoot() {
    root = node("div", "ekd-p");
    root.append(buildBar());
    el.banner = node("div", "ekd-banner");
    el.banner.hidden = true;
    el.banner.setAttribute("role", "status");
    root.append(el.banner);
    const body = node("div", "ekd-body");
    body.append(buildWebPane(), buildLocalPane(), buildPickPane(), buildPreviewPane());
    root.append(body, buildComposer());
    renderToolbar();
    renderThread();
    renderPicker();
    renderPreview();
    applyChrome();
  }

  // --- lifecycle -----------------------------------------------------------
  function setSource(source) {
    if (source !== "web" && source !== "local") return;
    if (state.source === source) {
      applyChrome();
      return;
    }
    flushDrafts();
    resetFillUi();
    state.source = source;
    setMenuOpen(false);
    void persist(STORAGE_KEYS.source, source);
    if (source === "web") {
      viewLauncher.resetAutoFailure();
      void ensureView().then(() => renderToolbar());
    } else {
      if (el.draft) el.draft.value = state.drafts.local;
      void loadModels().then(() => renderThread()).catch(error => setStatus("读取模型失败：" + errorText(error), true));
    }
    applyChrome();
  }

  function panelVisible() {
    if (!mountHost || !mountHost.isConnected) return false;
    // The right-panel host toggles `hidden` on the container it handed us when
    // another tab becomes active or the tab is closed.
    if (mountHost.hidden) return false;
    return Boolean(rectOf(mountHost));
  }

  function panelStateChanged() {
    const visible = panelVisible();
    if (visible && !sidebarRelease && ctx.layout) sidebarRelease = ctx.layout.acquireSidebarCollapse();
    if (!visible && sidebarRelease) {
      sidebarRelease.dispose();
      sidebarRelease = null;
    }
    if (!visible) {
      flushDrafts();
      if (state.pendingFill || state.fillInFlight || (el.sessionPicker && !el.sessionPicker.hidden)) resetFillUi();
    }
    if (visible) {
      if (!state.view && !viewLauncher.pending && !viewLauncher.autoFailed && state.source === "web") {
        void ensureView().then(() => renderToolbar());
      } else if (state.view && state.source === "web" && typeof state.view.onNavigate !== "function") {
        void refreshCurrentUrl();
      }
      if (!state.local.models.length) {
        void loadModels().then(() => renderThread()).catch(error => setStatus("读取模型失败：" + errorText(error), true));
      }
      renderToolbar();
    }
    scheduleSync();
  }

  function observePanel() {
    if (!mountHost) return;
    if (typeof ResizeObserver !== "undefined") {
      panelObserver = new ResizeObserver(() => panelStateChanged());
      panelObserver.observe(mountHost);
      if (mountHost.parentElement) panelObserver.observe(mountHost.parentElement);
    }
    window.addEventListener("resize", scheduleSync);
    document.addEventListener("visibilitychange", scheduleSync);
    // The host's settings sheet and pickers are Vue nodes the plugin must not
    // observe globally (PLUGINS.md: one host-owned decoration observer). They
    // open and close on user interaction, so re-check the layer on interaction
    // and on focus changes instead of watching the whole document.
    document.addEventListener("pointerdown", onHostInteraction, true);
    document.addEventListener("focusin", onHostInteraction, true);
    document.addEventListener("keydown", onHostInteraction, true);
  }

  function onHostInteraction(event) {
    if (!mountHost) return;
    if (shouldCloseMenuOnInteraction(event, el.menu, el.moreBtn)) {
      if (event?.type === "keydown" && event.key === "Escape") {
        event.stopPropagation?.();
        el.moreBtn?.focus?.();
      }
      setMenuOpen(false);
    }
    scheduleSync();
  }

  function releasePanelObservers() {
    panelObserver?.disconnect();
    panelObserver = null;
    window.removeEventListener("resize", scheduleSync);
    document.removeEventListener("visibilitychange", scheduleSync);
    document.removeEventListener("pointerdown", onHostInteraction, true);
    document.removeEventListener("focusin", onHostInteraction, true);
    document.removeEventListener("keydown", onHostInteraction, true);
  }

  // --- wiring --------------------------------------------------------------
  await restore();

  if (ctx.entry) {
    ctx.entry.add({
      id: "planner",
      title: "规划",
      icon: PLANNER_ENTRY_ICON,
      open: () => {
        if (!panelHandle) {
          setStatus("右侧面板尚未就绪，请稍后再试。", true);
          return;
        }
        panelHandle.open();
        panelStateChanged();
      },
    });
  }

  if (!ctx.rightPanel) {
    ctx.log("warn", "规划插件没有获得右侧面板能力（ui:right-panel），面板不会出现。");
    return { status: "degraded" };
  }

  panelHandle = ctx.rightPanel.mount({
    title: "规划",
    render: element => {
      mountHost = element;
      const host = document.createElement("div");
      host.style.cssText = "display:flex;flex-direction:column;flex:1 1 auto;min-height:0;min-width:0;height:100%";
      shadow = host.attachShadow({ mode: "open" });
      const style = document.createElement("style");
      style.textContent = STYLE;
      shadow.append(style);
      element.append(host);
      renderRoot();
      shadow.append(root);
      observePanel();
      panelStateChanged();
      return () => {
        releasePanelObservers();
        root?.remove();
        host.remove();
        root = null;
        shadow = null;
        mountHost = null;
      };
    },
  });

  ctx.onDispose(() => {
    disposed = true;
    state.summarize.seq++;
    const pendingSummarizeCancel = state.summarize.cancel;
    state.summarize.cancel = null;
    state.summarize.busy = false;
    if (typeof pendingSummarizeCancel === "function") {
      void pendingSummarizeCancel().catch(() => {});
    }
    flushDrafts();
    if (syncFrame !== null) window.cancelAnimationFrame(syncFrame);
    window.clearTimeout(previewDraftTimer);
    releasePanelObservers();
    sidebarRelease?.dispose();
    sidebarRelease = null;
    threadSubscription?.dispose();
    threadSubscription = null;
    viewNavSubscription?.dispose?.();
    viewNavSubscription = null;
    if (sync) void sync.dispose().catch(() => {});
    if (state.view) void state.view.close().catch(() => {});
    viewLauncher.clear();
    state.view = null;
    root?.remove();
    if (mountHost) mountHost.remove();
    root = null;
    shadow = null;
    mountHost = null;
  });

  return {
    status: "active",
    panel() {
      return { open: () => panelHandle?.open(), close: () => panelHandle?.close(), open_: Boolean(panelHandle?.open_) };
    },
  };
}
