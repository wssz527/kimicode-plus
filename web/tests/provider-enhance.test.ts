import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import vm from "node:vm";

interface MiniNode {
  tagName: string;
  nodeType: number;
  textContent: string;
  className: string;
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

  function matches(node: MiniNode, selector: string): boolean {
    if (node.nodeType !== 1) return false;
    if (selector.startsWith(".")) return node.className.split(/\s+/).includes(selector.slice(1));
    if (selector.startsWith("[") && selector.endsWith("]")) return node.attributes.has(selector.slice(1, -1));
    return node.tagName === selector.toUpperCase();
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
  thinkingMode(model: Record<string, unknown>): "none" | "toggle" | "always";
  readThinkingState(model: Record<string, unknown>): { thinking: "none" | "toggle" | "always"; adaptiveThinking: boolean };
  applyThinkingState(
    baseCapabilities: string[],
    thinking: "none" | "toggle" | "always",
    adaptiveThinking: boolean,
  ): { capabilities: string[]; thinking: "none" | "toggle" | "always"; adaptive_thinking: boolean };
  draftOf(model: Record<string, unknown>): {
    capabilities: Set<string>;
    thinking: "none" | "toggle" | "always";
    adaptiveThinking: boolean;
    maxContextSize: number;
    maxOutputSize: number | null;
    supportEfforts: string[];
  };
  openPanel(context: { providerId: string; key: string; model: Record<string, unknown>; managed: boolean }): void;
  closePanel(): void;
  state: { panel: MiniNode | null };
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

function findButton(dialog: MiniNode, label: string): MiniNode {
  const hit = dialog.querySelectorAll("button").find(button => allText(button) === label);
  assert.ok(hit, `button ${label} not found`);
  return hit;
}

function plain<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

test("bidirectional thinking mapping covers none, toggle, and always modes using provider_enhance.js", () => {
  const { enhance } = loadProviderEnhance();

  // 1. 不支持 (none)
  const noneStored = { capabilities: ["image_in", "tool_use"], adaptiveThinking: false };
  assert.deepEqual(plain(enhance.readThinkingState(noneStored)), { thinking: "none", adaptiveThinking: false });
  assert.deepEqual(plain(enhance.applyThinkingState(["tool_use", "image_in"], "none", true)), {
    capabilities: ["image_in", "tool_use"],
    thinking: "none",
    adaptive_thinking: false,
  });

  // 2. 可开关 (toggle), with adaptive_thinking false and true
  const toggleOffStored = { capabilities: ["image_in", "thinking"], adaptiveThinking: false };
  assert.deepEqual(plain(enhance.readThinkingState(toggleOffStored)), { thinking: "toggle", adaptiveThinking: false });
  assert.deepEqual(plain(enhance.applyThinkingState(["image_in"], "toggle", false)), {
    capabilities: ["image_in", "thinking"],
    thinking: "toggle",
    adaptive_thinking: false,
  });

  const toggleOnStored = { capabilities: ["video_in", "thinking"], adaptive_thinking: true };
  assert.deepEqual(plain(enhance.readThinkingState(toggleOnStored)), { thinking: "toggle", adaptiveThinking: true });
  assert.deepEqual(plain(enhance.applyThinkingState(["video_in"], "toggle", true)), {
    capabilities: ["video_in", "thinking"],
    thinking: "toggle",
    adaptive_thinking: true,
  });

  // Legacy/implicit adaptiveThinking without explicit `thinking` token still resolves to toggle and round-trips cleanly.
  const implicitToggle = { capabilities: ["tool_use"], adaptiveThinking: true };
  const implicitDraft = enhance.draftOf(implicitToggle);
  assert.equal(implicitDraft.thinking, "toggle");
  assert.equal(implicitDraft.adaptiveThinking, true);
  assert.deepEqual(
    plain(enhance.applyThinkingState([...implicitDraft.capabilities], implicitDraft.thinking, implicitDraft.adaptiveThinking)),
    { capabilities: ["tool_use", "thinking"], thinking: "toggle", adaptive_thinking: true },
  );

  // 3. 始终开启 (always) clears `thinking` and forces adaptive_thinking=false
  const alwaysStored = { capabilities: ["tool_use", "always_thinking", "thinking"], adaptiveThinking: true };
  assert.deepEqual(plain(enhance.readThinkingState(alwaysStored)), { thinking: "always", adaptiveThinking: false });
  assert.deepEqual(plain(enhance.applyThinkingState(["tool_use"], "always", true)), {
    capabilities: ["tool_use", "always_thinking"],
    thinking: "always",
    adaptive_thinking: false,
  });
});

test("opening the dialog initializes thinking segment aria-pressed and adaptive disabled state", () => {
  const { enhance } = loadProviderEnhance();

  // none -> 不支持 pressed, 自适应思考 disabled
  enhance.openPanel({
    providerId: "custom",
    key: "custom/m-none",
    model: { displayName: "None", maxContextSize: 32768, capabilities: ["tool_use"] },
    managed: false,
  });
  let dialog = enhance.state.panel!;
  assert.equal(findButton(dialog, "不支持").getAttribute("aria-pressed"), "true");
  assert.equal(findButton(dialog, "可开关").getAttribute("aria-pressed"), "false");
  assert.equal(findButton(dialog, "始终开启").getAttribute("aria-pressed"), "false");
  assert.equal(findButton(dialog, "自适应思考").disabled, true);
  assert.equal(findButton(dialog, "自适应思考").getAttribute("aria-pressed"), "false");

  // toggle + adaptiveThinking -> 可开关 pressed, 自适应思考 enabled and pressed
  enhance.openPanel({
    providerId: "custom",
    key: "custom/m-toggle",
    model: { displayName: "Toggle", maxContextSize: 131072, capabilities: ["thinking"], adaptiveThinking: true },
    managed: false,
  });
  dialog = enhance.state.panel!;
  assert.equal(findButton(dialog, "不支持").getAttribute("aria-pressed"), "false");
  assert.equal(findButton(dialog, "可开关").getAttribute("aria-pressed"), "true");
  assert.equal(findButton(dialog, "始终开启").getAttribute("aria-pressed"), "false");
  assert.equal(findButton(dialog, "自适应思考").disabled, false);
  assert.equal(findButton(dialog, "自适应思考").getAttribute("aria-pressed"), "true");

  // always -> 始终开启 pressed, 自适应思考 disabled
  enhance.openPanel({
    providerId: "custom",
    key: "custom/m-always",
    model: { displayName: "Always", maxContextSize: 131072, capabilities: ["always_thinking"], adaptiveThinking: true },
    managed: false,
  });
  dialog = enhance.state.panel!;
  assert.equal(findButton(dialog, "不支持").getAttribute("aria-pressed"), "false");
  assert.equal(findButton(dialog, "可开关").getAttribute("aria-pressed"), "false");
  assert.equal(findButton(dialog, "始终开启").getAttribute("aria-pressed"), "true");
  assert.equal(findButton(dialog, "自适应思考").disabled, true);
  assert.equal(findButton(dialog, "自适应思考").getAttribute("aria-pressed"), "false");
});

test("dialog interactions update thinking mode and submit mapped capabilities and adaptive_thinking", async () => {
  const requests: Array<{ action: string; payload?: Record<string, unknown> }> = [];
  const { enhance } = loadProviderEnhance(async (action, payload) => {
    requests.push({ action, payload });
    return {};
  });

  enhance.openPanel({
    providerId: "custom",
    key: "custom/m1",
    model: { displayName: "M1", maxContextSize: 65536, capabilities: ["image_in"] },
    managed: false,
  });
  const dialog = enhance.state.panel!;

  findButton(dialog, "可开关").click();
  const adaptive = findButton(dialog, "自适应思考");
  assert.equal(adaptive.disabled, false);
  adaptive.click();
  assert.equal(adaptive.getAttribute("aria-pressed"), "true");

  findButton(dialog, "保存").click();
  await new Promise(resolve => setTimeout(resolve, 0));

  assert.equal(requests.length, 1);
  assert.equal(requests[0]?.action, "providers.updateModel");
  assert.deepEqual(plain(requests[0]?.payload?.edit), {
    capabilities: ["image_in", "thinking"],
    thinking: "toggle",
    adaptive_thinking: true,
    max_context_size: 65536,
    max_output_size: null,
    support_efforts: [],
  });
});
