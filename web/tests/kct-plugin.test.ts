import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

const source = readFileSync("../plugins/kct/renderer.js", "utf8");
const manifest = JSON.parse(readFileSync("../plugins/kct/plugin.json", "utf8"));

class FakeClassList {
  private set: Set<string>;
  constructor(initial = "") {
    this.set = new Set(initial.split(/\s+/).filter(Boolean));
  }
  add(...names: string[]) {
    for (const name of names) if (name) this.set.add(name);
  }
  remove(...names: string[]) {
    for (const name of names) this.set.delete(name);
  }
  contains(name: string) {
    return this.set.has(name);
  }
  toString() {
    return [...this.set].join(" ");
  }
}

class FakeElement {
  tagName: string;
  attributes = new Map<string, string>();
  dataset: Record<string, string> = {};
  classList: FakeClassList;
  children: FakeElement[] = [];
  parentElement: FakeElement | null = null;
  textContent = "";
  hidden = false;
  disabled = false;
  type = "";
  title = "";
  style: Record<string, string> = {};
  isConnected = true;

  constructor(tag: string, className = "") {
    this.tagName = tag.toUpperCase();
    this.classList = new FakeClassList(className);
  }

  get className() {
    return this.classList.toString();
  }
  set className(value: string) {
    this.classList = new FakeClassList(value);
  }

  setAttribute(name: string, value: string) {
    if (name === "class") {
      this.className = value;
      return;
    }
    this.attributes.set(name, String(value));
  }
  getAttribute(name: string) {
    if (name === "class") return this.className;
    return this.attributes.get(name) ?? null;
  }
  removeAttribute(name: string) {
    if (name === "class") {
      this.className = "";
      return;
    }
    this.attributes.delete(name);
  }
  appendChild(child: FakeElement) {
    child.parentElement = this;
    child.isConnected = this.isConnected;
    this.children.push(child);
    return child;
  }
  append(...nodes: FakeElement[]) {
    for (const node of nodes) this.appendChild(node);
  }
  after(node: FakeElement) {
    if (!this.parentElement) return;
    const idx = this.parentElement.children.indexOf(this);
    node.parentElement = this.parentElement;
    this.parentElement.children.splice(idx + 1, 0, node);
  }
  remove() {
    this.isConnected = false;
    if (!this.parentElement) return;
    this.parentElement.children = this.parentElement.children.filter(item => item !== this);
    this.parentElement = null;
  }
  contains(node: FakeElement | null): boolean {
    if (!node) return false;
    if (node === this) return true;
    return this.children.some(child => child.contains(node));
  }
  cloneNode(deep = false): FakeElement {
    const copy = new FakeElement(this.tagName.toLowerCase(), this.className);
    for (const [key, value] of this.attributes) copy.attributes.set(key, value);
    if (deep) {
      copy.textContent = this.textContent;
      for (const child of this.children) copy.appendChild(child.cloneNode(true));
    }
    return copy;
  }
  matches(selector: string): boolean {
    if (selector === "pre") return this.tagName === "PRE";
    if (selector === "button.think-head") return this.tagName === "BUTTON" && this.classList.contains("think-head");
    if (selector.startsWith(".")) return this.classList.contains(selector.slice(1));
    return false;
  }
  closest(selector: string): FakeElement | null {
    let cur: FakeElement | null = this;
    while (cur) {
      if (cur.matches(selector)) return cur;
      cur = cur.parentElement;
    }
    return null;
  }
  querySelectorAll(selector: string): FakeElement[] {
    const selectors = selector.split(",").map(item => item.trim()).filter(Boolean);
    const matches: FakeElement[] = [];
    const visit = (node: FakeElement) => {
      for (const child of node.children) {
        if (selectors.some(sel => child.matches(sel))) matches.push(child);
        visit(child);
      }
    };
    visit(this);
    return matches;
  }
  querySelector(selector: string): FakeElement | null {
    return this.querySelectorAll(selector)[0] ?? null;
  }
  addEventListener() {}
  removeEventListener() {}
}

test("KCT translated node preserves official think-text class, Vue scope attr, and muted fallback CSS", async () => {
  assert.equal(manifest.id, "kimicode-plus.kct");
  assert.equal(manifest.version, "1.0.5");

  const head = new FakeElement("head");
  const bodyRoot = new FakeElement("body");
  const runtime = globalThis as unknown as Record<string, any>;
  const prevDocument = runtime.document;
  const prevWindow = runtime.window;
  const prevAddEventListener = runtime.addEventListener;
  const prevRemoveEventListener = runtime.removeEventListener;

  runtime.document = {
    head,
    body: bodyRoot,
    createElement: (tag: string) => new FakeElement(tag),
    addEventListener() {},
    removeEventListener() {},
  };
  runtime.window = {
    setTimeout,
    clearTimeout,
    addEventListener() {},
    removeEventListener() {},
    dispatchEvent() {},
    innerWidth: 1200,
    innerHeight: 800,
    navigator: { language: "zh-CN" },
  };
  runtime.addEventListener = () => {};
  runtime.removeEventListener = () => {};

  try {
    const moduleUrl = `data:text/javascript;base64,${Buffer.from(source).toString("base64")}`;
    const kct = await import(moduleUrl);

    let decorator: any = null;
    const disposers: Array<() => void> = [];
    const storage = new Map<string, unknown>([
      ["settings.v1", { targetLanguage: "zh", provider: "bing", autoTranslate: true }],
    ]);

    const ctx = {
      storage: {
        get: async (key: string) => storage.get(key),
        set: async (key: string, value: unknown) => { storage.set(key, value); },
        delete: async (key: string) => { storage.delete(key); },
      },
      net: {
        fetch: async () => ({
          ok: true,
          status: 200,
          body: JSON.stringify([{ translations: [{ text: "我们需要先检查配置。" }] }]),
        }),
      },
      ui: {
        decorate: (_target: string, fn: any) => {
          decorator = fn;
          return { dispose() {} };
        },
        openSettings: async () => {},
      },
      onDispose: (fn: () => void) => disposers.push(fn),
      log: () => {},
    };

    await kct.activate(ctx);
    assert.ok(decorator, "KCT should register a thinking decorator");

    const styleEl = head.children.find(child => child.tagName === "STYLE");
    assert.ok(styleEl, "KCT should inject its stylesheet");
    assert.match(styleEl.textContent, /:where\(\.ekdp-kct-translated\)\{[^}]*color:var\(--color-text-muted/);

    const block = new FakeElement("div", "think");
    const thinkHead = new FakeElement("button", "think-head");
    const thinkBody = new FakeElement("div", "think-body open");
    const thinkInner = new FakeElement("div", "think-body-inner");
    const sourcePre = new FakeElement("pre", "think-text");
    sourcePre.setAttribute("data-v-6e4307c6", "");
    sourcePre.setAttribute("id", "source-think");
    sourcePre.setAttribute("data-ekdp-kct-hidden", "1");
    sourcePre.textContent = "We need to inspect the configuration first.";
    thinkInner.appendChild(sourcePre);
    thinkBody.appendChild(thinkInner);
    block.append(thinkHead, thinkBody);
    bodyRoot.appendChild(block);

    const handle = decorator!(block, { open: true, streaming: false });
    await new Promise(resolve => setTimeout(resolve, 40));

    const translated = thinkInner.querySelector(".ekdp-kct-translated");
    assert.ok(translated, "translated node should be rendered");
    assert.equal(translated.tagName, "PRE");
    assert.equal(translated.textContent, "我们需要先检查配置。");
    assert.equal(translated.classList.contains("think-text"), true, "must preserve official think-text class");
    assert.equal(translated.classList.contains("ekdp-kct-translated"), true);
    assert.equal(translated.getAttribute("data-v-6e4307c6"), "", "must preserve Vue scoped style attribute");
    assert.equal(translated.getAttribute("id"), null, "must not duplicate source id");
    assert.equal(translated.getAttribute("data-ekdp-kct-hidden"), null, "must not copy hidden marker");
    assert.equal(translated.getAttribute("data-ekdp-owned"), "1");
    assert.equal(sourcePre.getAttribute("data-ekdp-kct-hidden"), "1");
    assert.equal(block.getAttribute("data-ekdp-kct-view"), "translated");

    handle?.dispose?.();
    for (const dispose of disposers) dispose();
    assert.equal(thinkInner.querySelector(".ekdp-kct-translated"), null);
    assert.equal(sourcePre.getAttribute("data-ekdp-kct-hidden"), null);
    assert.equal(styleEl.isConnected, false);
  } finally {
    runtime.document = prevDocument;
    runtime.window = prevWindow;
    runtime.addEventListener = prevAddEventListener;
    runtime.removeEventListener = prevRemoveEventListener;
  }
});
