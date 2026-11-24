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

/** Boots the real renderer against a permissive DOM stub so a test can drive
 * one decorated block through the scheduler. */
async function bootKct(options: {
  translate: (text: string) => Promise<unknown>;
  settings?: Record<string, unknown>;
}) {
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
  // A real event target so a test can drive the settings/cache events the panel
  // dispatches through window.
  const windowEvents = new EventTarget();
  runtime.window = {
    setTimeout,
    clearTimeout,
    addEventListener: (...args: any[]) => (windowEvents.addEventListener as any)(...args),
    removeEventListener: (...args: any[]) => (windowEvents.removeEventListener as any)(...args),
    dispatchEvent: (event: any) => windowEvents.dispatchEvent(event),
    innerWidth: 1200,
    innerHeight: 800,
    navigator: { language: "zh-CN" },
  };
  runtime.addEventListener = () => {};
  runtime.removeEventListener = () => {};
  let decorator: any = null;
  const disposers: Array<() => void> = [];
  const storage = new Map<string, unknown>([
    ["settings.v1", { targetLanguage: "zh", provider: "bing", autoTranslate: true, ...options.settings }],
  ]);
  const stored = new Map<string, number>();
  const deleted: string[] = [];
  const ctx = {
    storage: {
      get: async (key: string) => storage.get(key),
      set: async (key: string, value: unknown) => { storage.set(key, value); stored.set(key, (stored.get(key) ?? 0) + 1); },
      delete: async (key: string) => { storage.delete(key); deleted.push(key); },
    },
    net: { fetch: () => options.translate("") },
    ui: {
      decorate: (_target: string, fn: any) => { decorator = fn; return { dispose() {} }; },
      openSettings: async () => {},
    },
    onDispose: (fn: () => void) => disposers.push(fn),
    log: () => {},
  };
  const moduleUrl = `data:text/javascript;base64,${Buffer.from(source).toString("base64")}`;
  const kct = await import(moduleUrl);
  await kct.activate(ctx);
  return {
    decorator,
    ctx,
    head,
    bodyRoot,
    stored,
    deleted,
    cleanup() {
      for (const dispose of disposers) dispose();
      runtime.document = prevDocument;
      runtime.window = prevWindow;
      runtime.addEventListener = prevAddEventListener;
      runtime.removeEventListener = prevRemoveEventListener;
    },
  };
}

test("a failed translation is not retried until the source grows or the user asks", async () => {
  let calls = 0;
  // A 4xx that is not 429: it fails immediately, with no retry backoff and no
  // rate-limit cooldown, so the only thing under test is the failed-source guard.
  const boot = await bootKct({
    translate: async () => {
      calls += 1;
      return { ok: false, status: 400, body: JSON.stringify({ error: "bad request" }) };
    },
  });
  try {
    const block = new FakeElement("div", "think");
    const thinkHead = new FakeElement("button", "think-head");
    const thinkBody = new FakeElement("div", "think-body open");
    const thinkInner = new FakeElement("div", "think-body-inner");
    const sourcePre = new FakeElement("pre", "think-text");
    sourcePre.textContent = "We need to inspect the configuration first.";
    thinkInner.appendChild(sourcePre);
    thinkBody.appendChild(thinkInner);
    block.append(thinkHead, thinkBody);
    boot.bodyRoot.appendChild(block);
    const handle = boot.decorator!(block, { open: true, streaming: false });
    await new Promise(resolve => setTimeout(resolve, 60));
    const afterFirst = calls;
    assert.ok(afterFirst >= 1, "the first attempt never ran");
    // Repeated sweeps with the same failing source must not fire another request.
    for (let index = 0; index < 4; index += 1) {
      handle.update({ open: true, streaming: false });
      await new Promise(resolve => setTimeout(resolve, 40));
    }
    assert.equal(calls, afterFirst, "a failed source produced a retry storm");
    // Growing the source is a new input, so one more attempt is allowed.
    sourcePre.textContent = "We need to inspect the configuration first. Then check the cache.";
    handle.update({ open: true, streaming: false });
    await new Promise(resolve => setTimeout(resolve, 60));
    assert.ok(calls > afterFirst, "a grown source was never retried");
    handle.dispose?.();
  } finally {
    boot.cleanup();
  }
});

test("cache writes are incremental and pruned entries are deleted", async () => {
  const boot = await bootKct({
    translate: async () => ({ ok: true, status: 200, body: JSON.stringify([{ translations: [{ text: "需要检查配置。" }] }]) }),
  });
  try {
    const block = new FakeElement("div", "think");
    const thinkHead = new FakeElement("button", "think-head");
    const thinkBody = new FakeElement("div", "think-body open");
    const thinkInner = new FakeElement("div", "think-body-inner");
    const sourcePre = new FakeElement("pre", "think-text");
    sourcePre.textContent = "We need to inspect the configuration first.";
    thinkInner.appendChild(sourcePre);
    thinkBody.appendChild(thinkInner);
    block.append(thinkHead, thinkBody);
    boot.bodyRoot.appendChild(block);
    const handle = boot.decorator!(block, { open: true, streaming: false });
    await new Promise(resolve => setTimeout(resolve, 1200));
    const indexWrites = boot.stored.get("cache.index.v1") ?? 0;
    assert.ok(indexWrites >= 1, "the cache index was never written");
    const entryWrites = [...boot.stored.entries()].filter(([key]) => key.startsWith("cache.e.v1:")).length;
    assert.equal(entryWrites, 1, `expected one cache entry write, saw ${entryWrites}`);
    handle.dispose?.();
  } finally {
    boot.cleanup();
  }
});

test("clearing the cache stops an in-flight translation from repopulating it", async () => {
  let release: (() => void) | null = null;
  const releaseBox = { fire: () => (release as (() => void) | null)?.() };
  let calls = 0;
  const boot = await bootKct({
    translate: async () => {
      calls += 1;
      await new Promise<void>(resolve => { release = () => resolve(); });
      return { ok: true, status: 200, body: JSON.stringify([{ translations: [{ text: "需要检查配置。" }] }]) };
    },
  });
  try {
    const block = new FakeElement("div", "think");
    const thinkHead = new FakeElement("button", "think-head");
    const thinkBody = new FakeElement("div", "think-body open");
    const thinkInner = new FakeElement("div", "think-body-inner");
    const sourcePre = new FakeElement("pre", "think-text");
    sourcePre.textContent = "We need to inspect the configuration first.";
    thinkInner.appendChild(sourcePre);
    thinkBody.appendChild(thinkInner);
    block.append(thinkHead, thinkBody);
    boot.bodyRoot.appendChild(block);

    const handle = boot.decorator!(block, { open: true, streaming: false });
    // Wait for the debounce to fire so the request is genuinely in flight.
    await new Promise(resolve => setTimeout(resolve, 400));
    assert.equal(calls, 1, "the translation request should be running");

    // The user clears the cache while that request is still pending.
    boot.ctx.storage.delete("cache.index.v1");
    (globalThis as any).window.dispatchEvent(new (globalThis as any).CustomEvent("ekdp-kct:cache", { detail: { source: "inline" } }));

    releaseBox.fire();
    await new Promise(resolve => setTimeout(resolve, 1400));

    const entryWrites = [...boot.stored.entries()].filter(([key]) => key.startsWith("cache.e.v1:")).length;
    assert.equal(entryWrites, 0, `a cleared cache was repopulated by the in-flight translation (${entryWrites} writes)`);
    // The translation itself still reached the panel.
    assert.equal(thinkInner.querySelector(".ekdp-kct-translated")?.textContent, "需要检查配置。");
    handle.dispose?.();
  } finally {
    boot.cleanup();
  }
});
