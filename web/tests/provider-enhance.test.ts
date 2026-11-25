import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import vm from "node:vm";

interface MiniNode {
  tagName: string;
  nodeType: number;
  textContent: string;
  className: string;
  classList: { contains(token: string): boolean };
  disabled: boolean;
  checked: boolean;
  hidden: boolean;
  type: string;
  showModal(): void;
  getClientRects(): unknown[];
  focus(): void;
  removeAttribute(name: string): void;
  closest(selector: string): MiniNode | null;
  fire(type: string, metadata?: Record<string, unknown>): { defaultPrevented: boolean };
  before(...nodes: Array<MiniNode | string>): void;
  value: string;
  dataset: Record<string, string>;
  attributes: Map<string, string>;
  listeners: Map<string, Array<(event: unknown) => void>>;
  children: MiniNode[];
  parentElement: MiniNode | null;
  isConnected: boolean;
  shown: boolean;
  setAttribute(name: string, value: string): void;
  getAttribute(name: string): string | null;
  addEventListener(type: string, handler: (event: unknown) => void): void;
  click(): void;
  append(...nodes: Array<MiniNode | string>): void;
  after(...nodes: Array<MiniNode | string>): void;
  replaceChildren(...nodes: Array<MiniNode | string>): void;
  remove(): void;
  close(): void;
  querySelectorAll(selector: string): MiniNode[];
  querySelector(selector: string): MiniNode | null;
}

function createMiniDocument() {
  let activeElement: MiniNode;
  function makeNode(tagName: string, textContent = ""): MiniNode {
    const node: MiniNode = {
      tagName: tagName.toUpperCase(),
      nodeType: tagName === "#text" ? 3 : 1,
      textContent,
      className: "",
      classList: {
        contains(token: string) {
          return node.className.split(/\s+/).includes(token);
        },
      },
      disabled: false,
      checked: false,
      hidden: false,
      type: '',
      getClientRects() {
        for (let current: MiniNode | null = this; current; current = current.parentElement) {
          if (current.hidden) return [];
        }
        return this.isConnected ? [{}] : [];
      },
      focus() {
        activeElement = this;
      },
      removeAttribute(name) {
        this.attributes.delete(name);
      },
      closest(selector) {
        for (let current: MiniNode | null = this; current; current = current.parentElement) {
          if (matchesSingle(current, selector)) return current;
        }
        return null;
      },
      value: "",
      dataset: {},
      attributes: new Map<string, string>(),
      listeners: new Map<string, Array<(event: unknown) => void>>(),
      children: [],
      parentElement: null,
      isConnected: false,
      shown: false,
      setAttribute(name: string, value: string) {
        this.attributes.set(name, String(value));
        if (name === "value") this.value = value;
        if (name === "type") this.type = value;
      },
      getAttribute(name: string) {
        return this.attributes.get(name) ?? null;
      },
      addEventListener(type: string, handler: (event: unknown) => void) {
        const list = this.listeners.get(type) ?? [];
        list.push(handler);
        this.listeners.set(type, list);
      },
      fire(type, metadata = {}) {
        const event = {
          target: this,
          defaultPrevented: false,
          stopped: false,
          preventDefault() { this.defaultPrevented = true; },
          stopImmediatePropagation() { this.stopped = true; },
          ...metadata,
        };
        for (let current: MiniNode | null = this; current && !event.stopped; current = current.parentElement) {
          for (const handler of current.listeners.get(type) ?? []) handler(event);
        }
        return event;
      },
      click() {
        if (this.disabled) return;
        this.focus();
        if (this.type === "checkbox") this.checked = !this.checked;
        if (this.type === "radio") {
          const name = this.getAttribute("name");
          for (const radio of body.querySelectorAll(`input[name="${name}"]`)) radio.checked = radio === this;
        }
        this.fire("click");
        if (this.type === "checkbox" || this.type === "radio") {
          this.fire("input");
          this.fire("change");
        }
      },
      before(...nodes) {
        if (!this.parentElement) return;
        const parent = this.parentElement;
        const index = parent.children.indexOf(this);
        parent.append(...nodes);
        parent.children.splice(index, 0, ...parent.children.splice(-nodes.length));
      },
      append(...nodes: Array<MiniNode | string>) {
        for (const child of nodes) {
          const item = typeof child === "string" ? makeNode("#text", child) : child;
          item.parentElement = this;
          function connect(node: MiniNode, connected: boolean) {
            node.isConnected = connected;
            node.children.forEach(child => connect(child, connected));
          }
          connect(item, this.isConnected);
          this.children.push(item);
        }
      },
      after(...nodes: Array<MiniNode | string>) {
        if (!this.parentElement) return;
        const parent = this.parentElement;
        const idx = parent.children.indexOf(this);
        if (idx === -1) return;
        const inserted: MiniNode[] = [];
        for (const child of nodes) {
          const item = typeof child === "string" ? makeNode("#text", child) : child;
          item.parentElement = parent;
          item.isConnected = parent.isConnected;
          inserted.push(item);
        }
        parent.children.splice(idx + 1, 0, ...inserted);
      },
      replaceChildren(...nodes: Array<MiniNode | string>) {
        for (const old of this.children) {
          old.parentElement = null;
          old.isConnected = false;
        }
        this.children = [];
        this.append(...nodes);
      },
      remove() {
        this.isConnected = false;
        if (!this.parentElement) return;
        this.parentElement.children = this.parentElement.children.filter(item => item !== this);
        this.parentElement = null;
      },
      showModal() {
        this.shown = true;
      },
      close() {
        this.shown = false;
      },
      querySelectorAll(selector: string) {
        const out: MiniNode[] = [];
        const visit = (current: MiniNode) => {
          for (const child of current.children) {
            if (matches(child, selector)) out.push(child);
            visit(child);
          }
        };
        visit(this);
        return out;
      },
      querySelector(selector: string) {
        return this.querySelectorAll(selector)[0] ?? null;
      },
    };
    return node;
  }

  function matchesSingle(node: MiniNode, selector: string): boolean {
    if (node.nodeType !== 1) return false;
    const trimmed = selector.trim();
    if (!trimmed) return false;
    let rest = trimmed;
    const tagMatch = rest.match(/^[a-zA-Z0-9_-]+/);
    if (tagMatch) {
      if (node.tagName !== tagMatch[0].toUpperCase()) return false;
      rest = rest.slice(tagMatch[0].length);
    }
    while (rest.length > 0) {
      if (rest.startsWith(".")) {
        const clsMatch = rest.slice(1).match(/^[a-zA-Z0-9_-]+/);
        if (!clsMatch) return false;
        if (!node.className.split(/\s+/).includes(clsMatch[0])) return false;
        rest = rest.slice(1 + clsMatch[0].length);
      } else if (rest.startsWith("[")) {
        const end = rest.indexOf("]");
        if (end === -1) return false;
        const inner = rest.slice(1, end);
        const eq = inner.indexOf("=");
        if (eq === -1) {
          if (!node.attributes.has(inner)) return false;
        } else {
          const attr = inner.slice(0, eq).trim();
          const rawVal = inner.slice(eq + 1).trim().replace(/^["']|["']$/g, "");
          if (node.getAttribute(attr) !== rawVal) return false;
        }
        rest = rest.slice(end + 1);
      } else {
        return false;
      }
    }
    return true;
  }

  function hasAncestorMatching(node: MiniNode, parts: string[]): boolean {
    if (parts.length === 0) return true;
    const target = parts[parts.length - 1]!;
    let cur = node.parentElement;
    while (cur) {
      if (matchesSingle(cur, target)) {
        return hasAncestorMatching(cur, parts.slice(0, -1));
      }
      cur = cur.parentElement;
    }
    return false;
  }

  function matches(node: MiniNode, selectorGroup: string): boolean {
    for (const selector of selectorGroup.split(",")) {
      const parts = selector.trim().split(/\s+/).filter(Boolean);
      if (parts.length === 0) continue;
      if (matchesSingle(node, parts[parts.length - 1]!) && hasAncestorMatching(node, parts.slice(0, -1))) {
        return true;
      }
    }
    return false;
  }

  const head = makeNode("head");
  const body = makeNode("body");
  head.isConnected = true;
  body.isConnected = true;
  activeElement = body;
  const eventTarget = new EventTarget();

  return {
    head,
    body,
    get activeElement() { return activeElement; },
    addEventListener: eventTarget.addEventListener.bind(eventTarget),
    removeEventListener: eventTarget.removeEventListener.bind(eventTarget),
    createElement: (tag: string) => makeNode(tag),
    createElementNS: (_ns: string, tag: string) => makeNode(tag),
    createTextNode: (text: string) => makeNode("#text", text),
    querySelector: (selector: string) => body.querySelector(selector),
    querySelectorAll: (selector: string) => body.querySelectorAll(selector),
  };
}

type Model = Record<string, unknown>;
type Action = { action: string; payload?: Record<string, unknown> };

function loadFixture(request?: (action: string, payload?: Record<string, unknown>) => Promise<unknown>, providerId = "team") {
  const doc = createMiniDocument();
  const window = new EventTarget();
  const calls: Action[] = [];
  const config = {
    providers: { [providerId]: { type: "openai", base_url: "https://example.com/v1" } },
    models: {
      [`${providerId}/family/alpha`]: { maxContextSize: 262144, capabilities: ["audio_in", "image_in", "tool_use", "thinking"], supportEfforts: ["no", "high", "high"], adaptiveThinking: true },
      [`${providerId}/beta`]: { max_context_size: 65536, max_output_size: 1024 },
    } as Record<string, Model>,
  };
  const context = vm.createContext({
    document: doc, window, Event, CustomEvent: Event, setTimeout, clearTimeout,
    MutationObserver: class { observe() {} disconnect() {} },
  });
  vm.runInContext(readFileSync("../internal/desktop/assets/provider_enhance.js", "utf8"), context);
  const enhance = context.createProviderEnhance({ request: async (action: string, payload?: Record<string, unknown>) => {
    calls.push(JSON.parse(JSON.stringify({ action, payload })) as Action);
    if (action === "config.get") return structuredClone(config);
    return request?.(action, payload) ?? {};
  } }) as { install(): void; remove(): void; scan(): void };

  function node(tag: string, className = "") {
    const element = doc.createElement(tag);
    element.className = className;
    return element;
  }
  const official = node("section", "ui-dialog");
  official.setAttribute("role", "dialog");
  const form = node("div", "pf");
  const field = node("div", "pf-field");
  const name = node("input", "ui-input");
  name.value = providerId;
  field.append(name);
  const table = node("div", "pmt");
  const heading = node("div", "pmt-grid pmt-head");
  heading.append("模型 ID", "上下文", "移除");
  table.append(heading);
  function addRow(modelID: string) {
    const row = node("div", "pmt-grid");
    const id = node("input");
    id.value = modelID;
    const ctxInput = node("input");
    ctxInput.value = "128000";
    row.append(id, ctxInput, node("button"));
    table.append(row);
    return { row, ctxInput };
  }
  const alpha = addRow("family/alpha");
  const beta = addRow("beta");
  form.append(node("button", "pf-back"), field, table);
  official.append(form);
  doc.body.append(official);
  enhance.install();
  return { doc, window, form, table, heading, alpha, beta, config, calls, enhance, addRow };
}

const settle = () => new Promise(resolve => setTimeout(resolve, 5));
const inspector = (form: MiniNode) => form.querySelector("dialog.ekd-px-dialog")!;

function allText(node: MiniNode): string {
  return node.textContent + node.children.map(allText).join("");
}

function findButton(root: MiniNode, label: string): MiniNode {
  const hit = root.querySelectorAll("button").find(button => allText(button).trim() === label);
  assert.ok(hit, `button ${label} not found`);
  return hit;
}

function key(window: EventTarget, key: string, shiftKey = false) {
  const event = new Event("keydown", { cancelable: true });
  Object.assign(event, { key, shiftKey });
  window.dispatchEvent(event);
  return event.defaultPrevented;
}

test("preserves official columns, displays saved capabilities and keeps a tracked modal focus loop", async () => {
  const { doc, window, heading, form, alpha, beta, enhance } = loadFixture();
  await settle();
  assert.equal(heading.children.length, 3);
  assert.equal(alpha.row.children.slice(0, 3).map(node => node.tagName).join(","), "INPUT,INPUT,BUTTON");
  const trigger = findButton(alpha.row, "图片 · 工具 · 思考 · 其他 1 项 · 256K");
  assert.equal(findButton(beta.row, "未配置 · 64K").getAttribute("aria-expanded"), "false");
  trigger.click();
  await settle();
  const panel = inspector(form);
  assert.ok(panel.shown);
  assert.equal(trigger.getAttribute("aria-controls"), panel.getAttribute("id"));
  assert.equal(trigger.getAttribute("aria-expanded"), "true");
  assert.equal(panel.parentElement, form);
  assert.match(allText(panel), /audio_in/);
  const close = findButton(panel, "关闭");
  assert.equal(doc.activeElement, close);
  assert.equal(key(window, "Tab"), true);
  assert.equal(doc.activeElement, findButton(panel, "填入目录建议"));
  assert.equal(key(window, "Tab", true), true);
  assert.equal(doc.activeElement, close);
  const output = panel.querySelectorAll("input.ekd-px-input")[1]!;
  output.value = "32K";
  output.fire("input");
  assert.equal(key(window, "Escape"), true);
  assert.equal(inspector(form), panel);
  assert.match(allText(panel), /有未保存的修改/);
  assert.equal(doc.activeElement, findButton(panel, "放弃修改"));
  findButton(panel, "放弃修改").click();
  assert.equal(inspector(form), null);
  assert.equal(doc.activeElement, trigger);
  assert.equal(trigger.getAttribute("aria-expanded"), "false");
  enhance.remove();
  assert.equal(alpha.row.querySelector("[data-ekd-px-button]"), null);
});

test("validates output against saved context and sends only edited capability fields", async () => {
  const { calls, form, alpha } = loadFixture();
  await settle();
  alpha.row.querySelector("[data-ekd-px-button]")!.click();
  await settle();
  const panel = inspector(form);
  const output = panel.querySelectorAll("input.ekd-px-input")[1]!;
  const adaptive = panel.querySelector('input[role="switch"]')!;
  assert.equal(adaptive.checked, true);
  assert.equal(panel.querySelector('input[name="ekd-px-thinking"][value="toggle"]')?.checked, true);
  assert.equal(panel.querySelectorAll("input[type=radio]").length, 3);
  assert.doesNotMatch(allText(panel), /\bno\b/i);
  assert.equal(panel.querySelectorAll("label").filter(node => allText(node) === "high").length, 1);
  const save = findButton(panel, "保存");
  output.value = "1M";
  output.fire("input");
  assert.equal(output.getAttribute("aria-invalid"), "true");
  assert.equal(save.disabled, true);
  output.value = "";
  output.fire("input");
  panel.querySelector('input[value="always"]')!.click();
  assert.equal(adaptive.disabled, true);
  save.click();
  await settle();
  const write = calls.find(call => call.action === "providers.updateModel");
  assert.deepEqual(write?.payload, { providerId: "team", model: "team/family/alpha", edit: { thinking: "always" } });
  assert.equal(alpha.ctxInput.value, "128000");
  assert.match(allText(panel), /已保存并核对/);
});

test("saves both edited limits atomically and flags unsaved changes in the official row", async () => {
  const { calls, form, alpha } = loadFixture();
  await settle();
  alpha.row.querySelector("[data-ekd-px-button]")!.click();
  await settle();
  const panel = inspector(form);
  assert.match(allText(panel), /官方表单有未保存的模型改动/);
  const [context, output] = panel.querySelectorAll("input.ekd-px-input");
  context!.value = "";
  context!.fire("input");
  assert.equal(context!.getAttribute("aria-invalid"), "true");
  context!.value = "64K";
  output!.value = "128K";
  output!.fire("input");
  assert.equal(output!.getAttribute("aria-invalid"), "true");
  context!.value = "384K";
  context!.fire("input");
  assert.equal(output!.getAttribute("aria-invalid"), "false");
  findButton(panel, "保存").click();
  await settle();
  assert.deepEqual(calls.find(call => call.action === "providers.updateModel")?.payload?.edit, {
    max_context_size: 393216,
    max_output_size: 131072,
  });
  assert.equal(alpha.ctxInput.value, "128000");
});

test("re-reads just-imported rows and renders managed rows read-only", async () => {
  const { form, config, enhance, addRow } = loadFixture();
  await settle();
  const fresh = addRow("just-imported");
  enhance.scan();
  await settle();
  const trigger = findButton(fresh.row, "未保存");
  config.models["team/just-imported"] = { maxContextSize: 131072, capabilities: ["video_in"] };
  trigger.click();
  await settle();
  assert.ok(inspector(form));
  assert.equal(inspector(form).querySelector("input.ekd-px-input")?.value, "128K");
  assert.equal(allText(trigger), "视频 · 128K");

  const managed = loadFixture(undefined, "managed:kimi-code");
  await settle();
  findButton(managed.alpha.row, "只读").click();
  await settle();
  const panel = inspector(managed.form);
  assert.match(allText(panel), /内置订阅供应商/);
  assert.equal(panel.querySelectorAll("input").every(input => input.disabled), true);
  assert.equal(managed.form.querySelector(".ekd-px-toolbar")?.hidden, true);
});

test("uses exact official and public suggestions only as a draft", async () => {
  const { calls, form, beta } = loadFixture(async (action, payload) => {
    switch (action) {
      case "models.list": return { items: [
        { id: "other/beta", provider: "other", model: "beta", capabilities: ["image_in"] },
        { id: "team/beta", provider: "team", model: "beta", capabilities: ["tool_use"] },
      ] };
      case "providers.modelSuggestion":
        assert.deepEqual(JSON.parse(JSON.stringify(payload)), { providerId: "team", model: "team/beta" });
        return { suggestion: { video_in: true, thinking: true, max_output_size: 8192 } };
      default: return {};
    }
  });
  await settle();
  beta.row.querySelector("[data-ekd-px-button]")!.click();
  await settle();
  const panel = inspector(form);
  findButton(panel, "填入目录建议").click();
  await settle();
  assert.equal(calls.some(call => call.action.startsWith("providers.update")), false);
  assert.match(allText(panel), /官方模型目录、models.dev/);
  findButton(panel, "保存").click();
  await settle();
  assert.deepEqual(calls.find(call => call.action === "providers.updateModel")?.payload?.edit, {
    capabilities: ["video_in", "tool_use"], thinking: "toggle", max_output_size: 8192,
  });
});

test("submits selected models once from the batch template and returns focus to the toolbar", async () => {
  const { doc, form, calls, alpha, beta } = loadFixture();
  await settle();
  const toolbar = form.querySelector(".ekd-px-toolbar")!;
  const toggle = findButton(toolbar, "批量配置");
  toggle.click();
  findButton(toolbar, "全选已保存模型").click();
  for (const item of [alpha, beta]) assert.equal(item.row.querySelector("input[type=checkbox]")?.checked, true);
  findButton(toolbar, "编辑能力模板").click();
  await settle();
  const panel = inspector(form);
  panel.querySelector("input[type=checkbox]")!.click();
  findButton(panel, "保存到 2 个模型").click();
  await settle();
  assert.equal(inspector(form), null);
  assert.equal(doc.activeElement, toggle);
  assert.deepEqual(calls.filter(call => call.action.startsWith("providers.update")), [{
    action: "providers.updateModels",
    payload: { providerId: "team", models: ["team/family/alpha", "team/beta"].map(model => ({ model, edit: { capabilities: ["image_in"], thinking: "none" } })) },
  }]);
});

test("theme rules keep state text separate from the host accent and preserve the official grid", async () => {
  const { doc } = loadFixture();
  const css = doc.head.querySelector("style")!.textContent;
  assert.doesNotMatch(css, /pmt-grid|96px/);
  assert.match(css, /--px-bg:var\(--color-bg,/);
  assert.match(css, /--px-ink:var\(--color-text,/);
  assert.match(css, /\.ekd-px-mode:has\(:checked\)\{background:var\(--px-fill\);color:var\(--px-ink\)/);
  assert.match(css, /--px-error:light-dark\(#9b2325,#ffb4ab\)/);
  assert.match(css, /--px-muted:color-mix\(in srgb,var\(--px-ink\) 86%,var\(--px-bg\)\)/);
});
