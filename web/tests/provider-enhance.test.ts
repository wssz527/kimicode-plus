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
  showModal(): void;
  close(): void;
  querySelectorAll(selector: string): MiniNode[];
  querySelector(selector: string): MiniNode | null;
}

function createMiniDocument() {
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
        if (name === "value") this.value = String(value);
      },
      getAttribute(name: string) {
        return this.attributes.get(name) ?? null;
      },
      addEventListener(type: string, handler: (event: unknown) => void) {
        const list = this.listeners.get(type) ?? [];
        list.push(handler);
        this.listeners.set(type, list);
      },
      click() {
        if (this.disabled) return;
        for (const handler of this.listeners.get("click") ?? []) {
          handler({ preventDefault() {}, stopPropagation() {}, target: this });
        }
      },
      append(...nodes: Array<MiniNode | string>) {
        for (const child of nodes) {
          const item = typeof child === "string" ? makeNode("#text", child) : child;
          item.parentElement = this;
          item.isConnected = this.isConnected;
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

  return {
    head,
    body,
    createElement: (tag: string) => makeNode(tag),
    createElementNS: (_ns: string, tag: string) => makeNode(tag),
    createTextNode: (text: string) => makeNode("#text", text),
    querySelector: (selector: string) => body.querySelector(selector),
    querySelectorAll: (selector: string) => body.querySelectorAll(selector),
  };
}

interface ProviderEnhanceHandle {
  formatSize(num: unknown): string;
  parseSize(raw: unknown, allowEmpty?: boolean): { value: number | null; error: string };
  thinkingMode(model: Record<string, unknown>): "none" | "toggle" | "always";
  readThinkingState(model: Record<string, unknown>): { thinking: "none" | "toggle" | "always"; adaptiveThinking: boolean };
  applyThinkingState(
    baseCapabilities: string[],
    thinking: "none" | "toggle" | "always",
    adaptiveThinking?: boolean,
  ): { capabilities: string[]; thinking: "none" | "toggle" | "always"; adaptive_thinking: boolean };
  selectThinkingOption(draft: Record<string, unknown>, option: string): void;
  draftOf(model: Record<string, unknown>): {
    capabilities: Set<string>;
    thinking: "none" | "toggle" | "always";
    adaptiveThinking: boolean;
    maxContextSize: number;
    maxOutputSize: number | null;
    supportEfforts: string[];
  };
  openPanel(context: {
    providerId: string;
    key: string;
    model: Record<string, unknown>;
    managed: boolean;
    row?: MiniNode;
    button?: MiniNode;
  }): void;
  closePanel(): void;
  scan(): void;
  CONTEXT_PRESETS: Array<{ label: string; value: number }>;
  THINKING_OPTIONS: Array<{ value: string; label: string; title: string }>;
  state: { panel: MiniNode | null; panelRow: MiniNode | null; panelButton: MiniNode | null; style: MiniNode | null };
}

function loadProviderEnhance(request: (action: string, payload?: Record<string, unknown>) => Promise<unknown> = async () => ({})) {
  const doc = createMiniDocument();
  const source = readFileSync("../internal/desktop/assets/provider_enhance.js", "utf8");
  const context = vm.createContext({ document: doc });
  vm.runInContext(source, context);
  const factory = context.createProviderEnhance as (options: {
    request: (action: string, payload?: Record<string, unknown>) => Promise<unknown>;
  }) => ProviderEnhanceHandle;
  return { doc, enhance: factory({ request }) };
}

function allText(node: MiniNode): string {
  if (node.nodeType === 3) return node.textContent;
  return (node.textContent || "") + node.children.map(allText).join("");
}

function findButton(root: MiniNode, label: string): MiniNode {
  const hit = root.querySelectorAll("button").find(button => allText(button).trim() === label);
  assert.ok(hit, `button ${label} not found`);
  return hit;
}

function plain<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

test("bidirectional thinking mapping preserves stored adaptive_thinking and supports effort-driven toggle", () => {
  const { enhance } = loadProviderEnhance();

  const noneStored = { capabilities: ["image_in", "tool_use"], adaptiveThinking: false };
  assert.deepEqual(plain(enhance.readThinkingState(noneStored)), { thinking: "none", adaptiveThinking: false });
  assert.deepEqual(plain(enhance.applyThinkingState(["tool_use", "image_in"], "none", true)), {
    capabilities: ["image_in", "tool_use"],
    thinking: "none",
    adaptive_thinking: false,
  });

  const effortOnlyStored = { capabilities: ["tool_use"], supportEfforts: ["high", "low", "no"] };
  const effortDraft = enhance.draftOf(effortOnlyStored);
  assert.equal(effortDraft.thinking, "toggle");
  assert.equal(effortDraft.adaptiveThinking, false);
  assert.deepEqual(plain(effortDraft.supportEfforts), ["low", "high"]);

  const adaptiveStored = { capabilities: ["video_in", "thinking"], adaptive_thinking: true };
  const adaptiveDraft = enhance.draftOf(adaptiveStored);
  assert.equal(adaptiveDraft.thinking, "toggle");
  assert.equal(adaptiveDraft.adaptiveThinking, true);
  assert.deepEqual(
    plain(enhance.applyThinkingState([...adaptiveDraft.capabilities], adaptiveDraft.thinking, adaptiveDraft.adaptiveThinking)),
    { capabilities: ["video_in", "thinking"], thinking: "toggle", adaptive_thinking: true },
  );

  // Toggling efforts on an adaptive model preserves adaptive_thinking; clicking `no` disables thinking
  enhance.selectThinkingOption(adaptiveDraft, "high");
  assert.equal(adaptiveDraft.thinking, "toggle");
  assert.equal(adaptiveDraft.adaptiveThinking, true);
  assert.deepEqual(plain(adaptiveDraft.supportEfforts), ["high"]);

  enhance.selectThinkingOption(adaptiveDraft, "no");
  assert.equal(adaptiveDraft.thinking, "none");
  assert.equal(adaptiveDraft.adaptiveThinking, false);
  assert.deepEqual(plain(adaptiveDraft.supportEfforts), []);

  // Re-enabling an effort before saving restores preserved adaptive_thinking
  enhance.selectThinkingOption(adaptiveDraft, "medium");
  assert.equal(adaptiveDraft.thinking, "toggle");
  assert.equal(adaptiveDraft.adaptiveThinking, true);
  assert.deepEqual(plain(adaptiveDraft.supportEfforts), ["medium"]);

  const alwaysStored = { capabilities: ["tool_use", "always_thinking", "thinking"], adaptiveThinking: true };
  assert.deepEqual(plain(enhance.readThinkingState(alwaysStored)), { thinking: "always", adaptiveThinking: false });
  assert.deepEqual(plain(enhance.applyThinkingState(["tool_use"], "always", true)), {
    capabilities: ["tool_use", "always_thinking"],
    thinking: "always",
    adaptive_thinking: false,
  });
});

test("inline panel exposes direct thinking options (no/low/medium/high/xhigh/max), omits adaptive thinking, and uses 256K-1M presets", () => {
  const { enhance } = loadProviderEnhance();

  assert.deepEqual(
    plain(enhance.THINKING_OPTIONS.map(item => [item.value, item.label])),
    [
      ["no", "no"],
      ["low", "low"],
      ["medium", "medium"],
      ["high", "high"],
      ["xhigh", "xhigh"],
      ["max", "max"],
    ],
  );
  assert.deepEqual(
    plain(enhance.CONTEXT_PRESETS.map(item => [item.label, item.value])),
    [
      ["256K", 262144],
      ["350K", 358400],
      ["500K", 512000],
      ["600K", 614400],
      ["1M", 1048576],
    ],
  );
  assert.deepEqual(plain(enhance.parseSize("1K", false)), { value: 1024, error: "" });
  assert.deepEqual(plain(enhance.parseSize("1KB", false)), { value: 1024, error: "" });
  assert.deepEqual(plain(enhance.parseSize("256K", false)), { value: 262144, error: "" });
  assert.deepEqual(plain(enhance.parseSize("1M", false)), { value: 1048576, error: "" });
  assert.deepEqual(plain(enhance.parseSize("1MB", false)), { value: 1048576, error: "" });

  // No thinking -> `no` pressed, all efforts unpressed, no 自适应思考 anywhere
  enhance.openPanel({
    providerId: "custom",
    key: "custom/m-none",
    model: { displayName: "NoThink", maxContextSize: 262144, capabilities: ["tool_use"] },
    managed: false,
  });
  let drawer = enhance.state.panel!;
  assert.equal(drawer.tagName, "DIV");
  assert.ok(!allText(drawer).includes("自适应思考"), "adaptive thinking must not appear");
  const contextPresetsGroup = drawer.querySelectorAll(".ekd-px-presets")[0]!;
  assert.deepEqual(
    contextPresetsGroup.querySelectorAll("button").map(b => allText(b).trim()),
    ["256K", "350K", "500K", "600K", "1M"],
  );
  assert.equal(findButton(drawer, "no").getAttribute("aria-pressed"), "true");
  assert.equal(findButton(drawer, "medium").getAttribute("aria-pressed"), "false");
  assert.equal(findButton(drawer, "256K").getAttribute("aria-pressed"), "true");
  assert.equal(drawer.querySelectorAll(".ekd-px-limits").length, 1, "context and output limits share one horizontal grid");

  // Configured efforts -> `no` unpressed, selected efforts pressed
  enhance.openPanel({
    providerId: "custom",
    key: "custom/m-efforts",
    model: {
      displayName: "ThinkModel",
      maxContextSize: 1048576,
      capabilities: ["thinking"],
      supportEfforts: ["medium", "xhigh", "max"],
    },
    managed: false,
  });
  drawer = enhance.state.panel!;
  assert.equal(findButton(drawer, "no").getAttribute("aria-pressed"), "false");
  assert.equal(findButton(drawer, "medium").getAttribute("aria-pressed"), "true");
  assert.equal(findButton(drawer, "xhigh").getAttribute("aria-pressed"), "true");
  assert.equal(findButton(drawer, "max").getAttribute("aria-pressed"), "true");
  assert.equal(findButton(drawer, "1M").getAttribute("aria-pressed"), "true");
});

test("inline panel clicks submit direct thinking efforts, preserve adaptive_thinking, and clear thinking on no", async () => {
  const requests: Array<{ action: string; payload?: Record<string, unknown> }> = [];
  const { doc, enhance } = loadProviderEnhance(async (action, payload) => {
    requests.push({ action, payload });
    return {};
  });

  const row = doc.createElement("div");
  row.className = "pmt-grid";
  const idInput = doc.createElement("input");
  idInput.value = "m1";
  const ctxInput = doc.createElement("input");
  ctxInput.value = "128000";
  row.append(idInput, ctxInput);
  doc.body.append(row);

  enhance.openPanel({
    providerId: "custom",
    key: "custom/m1",
    model: { displayName: "M1", maxContextSize: 128000, capabilities: ["image_in"] },
    managed: false,
    row,
  });
  const drawer = enhance.state.panel!;
  assert.equal(drawer.parentElement, row, "drawer renders inline inside the model row");

  findButton(drawer, "medium").click();
  findButton(drawer, "xhigh").click();
  findButton(drawer, "500K").click();

  const inputs = drawer.querySelectorAll("input.ekd-px-input");
  assert.equal(inputs.length, 2);
  assert.equal(inputs[0]?.value, "500K");
  inputs[1]!.value = "32K";

  findButton(drawer, "保存").click();
  await new Promise(resolve => setTimeout(resolve, 0));

  assert.equal(requests.length, 1);
  assert.equal(requests[0]?.action, "providers.updateModel");
  assert.deepEqual(plain(requests[0]?.payload?.edit), {
    capabilities: ["image_in", "thinking"],
    thinking: "toggle",
    max_context_size: 512000,
    max_output_size: 32768,
    support_efforts: ["medium", "xhigh"],
  });
  assert.equal(ctxInput.value, "512000", "saving inline panel syncs official row context input");

  // Clicking `no` clears all efforts and disables thinking without writing literal "no"
  findButton(drawer, "no").click();
  findButton(drawer, "保存").click();
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.deepEqual(plain(requests[1]?.payload?.edit), {
    capabilities: ["image_in"],
    thinking: "none",
    max_context_size: 512000,
    max_output_size: 32768,
    support_efforts: [],
  });

  // Stored adaptive_thinking: true (e.g. Claude) survives edits until `no` is explicitly selected
  enhance.openPanel({
    providerId: "anthropic",
    key: "anthropic/claude-opus-4-6",
    model: {
      displayName: "Claude Opus 4.6",
      maxContextSize: 200000,
      capabilities: ["tool_use", "thinking"],
      adaptiveThinking: true,
      supportEfforts: ["low", "medium", "high"],
    },
    managed: false,
    row,
  });
  const claudeDrawer = enhance.state.panel!;
  findButton(claudeDrawer, "max").click();
  findButton(claudeDrawer, "保存").click();
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.deepEqual(plain(requests[2]?.payload?.edit), {
    capabilities: ["tool_use", "thinking"],
    thinking: "toggle",
    adaptive_thinking: true,
    max_context_size: 200000,
    max_output_size: null,
    support_efforts: ["low", "medium", "high", "max"],
  });

  findButton(claudeDrawer, "no").click();
  findButton(claudeDrawer, "保存").click();
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.deepEqual(plain(requests[3]?.payload?.edit), {
    capabilities: ["tool_use"],
    thinking: "none",
    max_context_size: 200000,
    max_output_size: null,
    support_efforts: [],
  });
});

test("row enhance button toggles inline collapsible drawer inside the model row", async () => {
  const { doc, enhance } = loadProviderEnhance(async (action) => {
    if (action === "config.get") {
      return {
        providers: {
          "custom-a": { type: "openai_legacy", baseUrl: "https://api.example.com/v1" },
        },
        models: {
          "custom-a/demo-model": { displayName: "Demo Model", maxContextSize: 262144, capabilities: ["tool_use"] },
        },
      };
    }
    return {};
  });

  const dialog = doc.createElement("div");
  dialog.className = "settings-dialog";
  const form = doc.createElement("div");
  form.className = "pf";
  const back = doc.createElement("button");
  back.className = "pf-back";

  const fieldType = doc.createElement("div");
  fieldType.className = "pf-field";
  const typeText = doc.createElement("span");
  typeText.className = "ui-select__value-text";
  typeText.textContent = "openai_legacy";
  fieldType.append(typeText);

  const fieldName = doc.createElement("div");
  fieldName.className = "pf-field";
  const nameInput = doc.createElement("input");
  nameInput.className = "ui-input";
  nameInput.value = "custom-a";
  fieldName.append(nameInput);

  const fieldUrl = doc.createElement("div");
  fieldUrl.className = "pf-field";
  const urlInput = doc.createElement("input");
  urlInput.className = "ui-input";
  urlInput.value = "https://api.example.com/v1";
  fieldUrl.append(urlInput);

  const table = doc.createElement("div");
  table.className = "pmt";
  const row = doc.createElement("div");
  row.className = "pmt-grid";
  const modelInput = doc.createElement("input");
  modelInput.value = "demo-model";
  const removeBtn = doc.createElement("button");
  removeBtn.setAttribute("aria-label", "移除模型");
  row.append(modelInput, removeBtn);
  table.append(row);

  form.append(back, fieldType, fieldName, fieldUrl, table);
  dialog.append(form);
  doc.body.append(dialog);

  enhance.scan();
  await new Promise(resolve => setTimeout(resolve, 10));

  const btn = row.querySelector("[data-ekd-px-button]");
  assert.ok(btn, "expected injected enhance button");
  assert.equal(btn.getAttribute("aria-label"), "能力与限制");
  assert.equal(btn.getAttribute("aria-expanded"), "false");
  assert.equal(btn.getAttribute("title"), "展开设置模型能力、思考强度与上下文限制");
  assert.equal(btn.getAttribute("data-ekd-enhance"), "1");
  assert.ok(allText(btn).includes("能力与限制"), "button should contain readable label text");

  btn.click();
  await new Promise(resolve => setTimeout(resolve, 10));
  assert.ok(enhance.state.panel?.shown, "clicking the labeled button should expand the inline panel");
  assert.equal(enhance.state.panel?.parentElement, row);
  assert.equal(btn.getAttribute("aria-expanded"), "true");

  btn.click();
  await new Promise(resolve => setTimeout(resolve, 10));
  assert.equal(enhance.state.panel, null, "clicking again collapses the inline panel");
  assert.equal(btn.getAttribute("aria-expanded"), "false");
});

test("decorates provider form inside 1.3.2 ui-dialog / role=dialog without .settings-dialog", async () => {
  const { doc, enhance } = loadProviderEnhance(async (action) => {
    if (action === "config.get") {
      return {
        providers: {
          magpie: { type: "openai_legacy", base_url: "http://127.0.0.1:3425/v1" },
        },
        models: {
          "magpie/glm-5.3": { displayName: "GLM-5.3", maxContextSize: 614400, capabilities: ["tool_use"] },
        },
      };
    }
    return {};
  });

  const dialog = doc.createElement("div");
  dialog.className = "ui-dialog ui-dialog--xl ui-dialog--flush ui-dialog--fixed-height ui-dialog--grouped";
  dialog.setAttribute("role", "dialog");
  const form = doc.createElement("div");
  form.className = "pf";
  const back = doc.createElement("button");
  back.className = "pf-back";

  const fieldName = doc.createElement("div");
  fieldName.className = "pf-field";
  const nameInput = doc.createElement("input");
  nameInput.className = "ui-input";
  nameInput.value = "magpie";
  fieldName.append(nameInput);

  const fieldType = doc.createElement("div");
  fieldType.className = "pf-field";
  const typeText = doc.createElement("span");
  typeText.className = "ui-select__value-text";
  typeText.textContent = "OpenAI";
  fieldType.append(typeText);

  const fieldKey = doc.createElement("div");
  fieldKey.className = "pf-field";
  const keyInput = doc.createElement("input");
  keyInput.className = "ui-input";
  keyInput.value = "sk-redacted";
  fieldKey.append(keyInput);

  const fieldUrl = doc.createElement("div");
  fieldUrl.className = "pf-field";
  const urlInput = doc.createElement("input");
  urlInput.className = "ui-input";
  urlInput.value = "http://127.0.0.1:3425/v1";
  fieldUrl.append(urlInput);

  const table = doc.createElement("div");
  table.className = "pmt";
  const head = doc.createElement("div");
  head.className = "pmt-grid pmt-head";
  const row = doc.createElement("div");
  row.className = "pmt-grid";
  const modelInput = doc.createElement("input");
  modelInput.value = "glm-5.3";
  const removeBtn = doc.createElement("button");
  removeBtn.setAttribute("aria-label", "移除模型");
  row.append(modelInput, removeBtn);
  table.append(head, row);

  form.append(back, fieldName, fieldType, fieldKey, fieldUrl, table);
  dialog.append(form);
  doc.body.append(dialog);

  enhance.scan();
  await new Promise(resolve => setTimeout(resolve, 10));

  assert.equal(table.dataset.ekdPxTable, "1");
  assert.ok(head.querySelector("[data-ekd-px-head-pad]"), "expected header column pad");
  const btn = row.querySelector("[data-ekd-px-button]");
  assert.ok(btn, "expected injected enhance button inside .ui-dialog");
  assert.ok(enhance.state.style?.textContent.includes(".ui-dialog .pf .pmt[data-ekd-px-table=\"1\"]"));
  assert.ok(enhance.state.style?.textContent.includes("[role=\"dialog\"] .pf .pmt[data-ekd-px-table=\"1\"]"));

  btn.click();
  await new Promise(resolve => setTimeout(resolve, 10));
  assert.ok(enhance.state.panel?.shown, "clicking enhance button inside .ui-dialog should open inline drawer");
});
