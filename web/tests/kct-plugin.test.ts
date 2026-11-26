import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { createPluginRuntime } from "../src/plugin-runtime/runtime.js";

const source = readFileSync("../plugins/kct/renderer.js", "utf8");
const settingsSource = readFileSync("../plugins/kct/settings.js", "utf8");
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
  value = "";
  placeholder = "";
  autocomplete = "";
  selectedIndex = 0;
  style: Record<string, string> = {};
  isConnected = true;

  get options() {
    return this.children;
  }

  replaceChildren(...nodes: FakeElement[]) {
    for (const child of [...this.children]) child.remove();
    for (const node of nodes) this.appendChild(node);
  }

  constructor(tag: string, className = "", root?: unknown) {
    this.tagName = tag.toUpperCase();
    this.root = root;
    this.classList = new FakeClassList(className);
  }

  root: unknown;

  getRootNode() {
    return this.root ?? document;
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
    if (/^[a-z]+$/.test(selector)) return this.tagName === selector.toUpperCase();
    if (selector === "button.think-head") return this.tagName === "BUTTON" && this.classList.contains("think-head");
    if (selector.startsWith(".")) return this.classList.contains(selector.slice(1));
    if (selector.startsWith("[data-")) return this.attributes.has(selector.slice(1, -1));
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
  private listeners = new Map<string, Array<(event: Event) => void>>();

  addEventListener(type: string, listener: (event: Event) => void) {
    const listeners = this.listeners.get(type) ?? [];
    listeners.push(listener);
    this.listeners.set(type, listeners);
  }
  removeEventListener(type: string, listener: (event: Event) => void) {
    this.listeners.set(type, (this.listeners.get(type) ?? []).filter(item => item !== listener));
  }
  dispatchEvent(event: Event) {
    for (const listener of this.listeners.get(event.type) ?? []) listener(event);
    return true;
  }
  click() {
    this.dispatchEvent(new Event("click"));
  }
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
  translate: (url: string, init: any) => Promise<unknown>;
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
  let decorationOptions: any = null;
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
    net: { fetch: (url: string, init: any) => options.translate(url, init) },
    ui: {
      decorate: (_target: string, fn: any, options: unknown) => { decorator = fn; decorationOptions = options; return { dispose() {} }; },
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
    decorationOptions,
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

const SETTINGS_KEY = "settings.v1";

function wait(ms: number) {
  return new Promise(resolve => setTimeout(resolve, ms));
}

interface CapturedRequest {
  url: string;
  init: any;
  resolve: (response: unknown) => void;
}

function makeBlock(root: FakeElement, text: string) {
  const block = new FakeElement("div", "think");
  const thinkHead = new FakeElement("button", "think-head");
  const thinkBody = new FakeElement("div", "think-body open");
  const thinkInner = new FakeElement("div", "think-body-inner");
  const sourcePre = new FakeElement("pre", "think-text");
  sourcePre.textContent = text;
  thinkInner.appendChild(sourcePre);
  thinkBody.appendChild(thinkInner);
  block.append(thinkHead, thinkBody);
  root.appendChild(block);
  return { block, inner: thinkInner, pre: sourcePre };
}

function captureRequests() {
  const requests: CapturedRequest[] = [];
  return {
    requests,
    transport: (url: string, init: any) => new Promise<unknown>(resolve => requests.push({ url, init, resolve })),
  };
}

function pushSettings(boot: Awaited<ReturnType<typeof bootKct>>, settings: Record<string, unknown>) {
  void boot.ctx.storage.set(SETTINGS_KEY, { targetLanguage: "zh", provider: "bing", autoTranslate: true, ...settings });
  (globalThis as any).window.dispatchEvent(new (globalThis as any).CustomEvent("ekdp-kct:settings", { detail: { source: "test" } }));
}

function customOk(request: CapturedRequest) {
  const payload = JSON.parse(request.init.body);
  const userContent: string = payload.messages[1].content;
  const numbered = /^1\. /.test(userContent);
  const content = userContent
    .split("\n")
    .map(line => {
      const step = /step (\d+)/.exec(line)?.[1];
      if (!step) return line;
      return numbered ? `${line.slice(0, line.indexOf(".") + 1)} 译文${step}` : `译文${step}`;
    })
    .join("\n");
  return { ok: true, status: 200, body: JSON.stringify({ choices: [{ message: { content } }] }) };
}

test("two blocks with the same content share one in-flight request", async () => {
  const { requests, transport } = captureRequests();
  const boot = await bootKct({ translate: transport });
  try {
    const text = "We need to inspect the configuration first.";
    const a = makeBlock(boot.bodyRoot, text);
    const b = makeBlock(boot.bodyRoot, text);
    const handleA = boot.decorator!(a.block, { open: true, streaming: false });
    const handleB = boot.decorator!(b.block, { open: true, streaming: false });
    await wait(80);
    assert.equal(requests.length, 1, `expected one shared request, saw ${requests.length}`);

    // 取消其中一个消费者（折叠再展开）不得取消仍有效的另一个，也不得重发请求。
    handleA.update({ open: false, streaming: false });
    await wait(40);
    handleA.update({ open: true, streaming: false });
    await wait(40);
    assert.equal(requests.length, 1, "cancelling one consumer dropped or duplicated the shared request");

    requests[0].resolve({ ok: true, status: 200, body: JSON.stringify([{ translations: [{ text: "我们需要先检查配置。" }] }]) });
    await wait(80);
    assert.equal(a.inner.querySelector(".ekdp-kct-translated")?.textContent, "我们需要先检查配置。");
    assert.equal(b.inner.querySelector(".ekdp-kct-translated")?.textContent, "我们需要先检查配置。");
    handleA.dispose?.();
    handleB.dispose?.();
  } finally {
    boot.cleanup();
  }
});

test("folding and reopening a block while its request is in flight does not re-request", async () => {
  const { requests, transport } = captureRequests();
  const boot = await bootKct({ translate: transport });
  try {
    const { block, inner } = makeBlock(boot.bodyRoot, "We need to inspect the configuration first.");
    const handle = boot.decorator!(block, { open: true, streaming: false });
    await wait(60);
    assert.equal(requests.length, 1);

    handle.update({ open: false, streaming: false });
    await wait(40);
    handle.update({ open: true, streaming: false });
    await wait(60);
    assert.equal(requests.length, 1, "a folded and reopened block re-requested work still in flight");

    requests[0].resolve({ ok: true, status: 200, body: JSON.stringify([{ translations: [{ text: "我们需要先检查配置。" }] }]) });
    await wait(80);
    assert.equal(inner.querySelector(".ekdp-kct-translated")?.textContent, "我们需要先检查配置。");
    const status = block.querySelector(".ekdp-kct-status");
    assert.equal(status?.dataset.state, "ok");
    assert.equal(status?.textContent, "");
    handle.dispose?.();
  } finally {
    boot.cleanup();
  }
});

test("a queued job for an invalidated consumer is dropped instead of sent to the new service", async () => {
  const { requests, transport } = captureRequests();
  const boot = await bootKct({
    settings: { provider: "google" },
    translate: transport,
  });
  try {
    const paragraphs = [
      "First paragraph of the plan.",
      "Second paragraph of the plan.",
      "Third paragraph of the plan.",
      "Fourth paragraph of the plan.",
    ];
    const { block } = makeBlock(boot.bodyRoot, paragraphs.join("\n\n"));
    const handle = boot.decorator!(block, { open: true, streaming: false });
    await wait(80);
    assert.equal(requests.length, 3, "google should translate single pieces with three in flight");
    assert.ok(requests.every(request => request.url.includes("translate.googleapis.com")));

    // 折叠块使消费者失效，再把服务切到 Bing：排队中的旧任务必须被丢弃，
    // 不能带着旧服务的缓存键发往新服务。
    handle.update({ open: false, streaming: false });
    await wait(40);
    pushSettings(boot, { provider: "bing" });
    await wait(60);

    for (const request of requests) {
      request.resolve({ ok: true, status: 200, body: JSON.stringify([[["第一段。"]]]) });
    }
    await wait(240);
    assert.equal(requests.length, 3, "the invalidated queued job was executed anyway");
    assert.ok(!requests.some(request => request.url.includes("edge.microsoft.com")), "queued work was sent to the new provider");
    handle.dispose?.();
  } finally {
    boot.cleanup();
  }
});

test("a queued request keeps the settings snapshot it was created with", async () => {
  const { requests, transport } = captureRequests();
  const endpoint = "http://127.0.0.1:9999/v1/chat/completions";
  const boot = await bootKct({
    settings: { provider: "custom", customEndpoint: endpoint, useCredential: true },
    translate: transport,
  });
  try {
    const paragraph = (step: number) =>
      `We must verify step ${step} of the runbook with care and patience before moving on. `.repeat(18);
    const { block, inner } = makeBlock(boot.bodyRoot, [1, 2, 3, 4, 5, 6, 7].map(paragraph).join("\n\n"));
    const handle = boot.decorator!(block, { open: true, streaming: false });
    await wait(100);
    assert.equal(requests.length, 3, "custom batches should fill the concurrency window");

    // 凭据开关不在翻译签名内：切换它不得重排仍在进行的翻译，也不得改变已排队
    // 任务的请求形态。
    pushSettings(boot, { provider: "custom", customEndpoint: endpoint, useCredential: false });
    await wait(60);
    assert.equal(requests.length, 3, "a signature-preserving toggle rescheduled the running translation");

    requests[0].resolve(customOk(requests[0]));
    await wait(100);
    assert.equal(requests.length, 4, "the queued job never ran");
    assert.equal(requests[3].url, endpoint);
    assert.equal(requests[3].init.credential, "translator", "the queued job read settings mutated after its creation");
    assert.equal(requests[3].init.credentialHeader, "Authorization");

    for (const request of requests) request.resolve(customOk(request));
    await wait(200);
    const translated = inner.querySelector(".ekdp-kct-translated");
    assert.ok(translated, "the translation never rendered");
    assert.match(translated!.textContent, /译文7/);
    handle.dispose?.();
  } finally {
    boot.cleanup();
  }
});

test("a request retrying after a settings change is stopped instead of being resent", async () => {
  const { requests, transport } = captureRequests();
  const boot = await bootKct({
    settings: { provider: "google" },
    translate: transport,
  });
  try {
    const { block, inner } = makeBlock(boot.bodyRoot, "We need to inspect the configuration first.");
    const handle = boot.decorator!(block, { open: true, streaming: false });
    await wait(60);
    assert.equal(requests.length, 1);
    assert.ok(requests[0].url.includes("translate.googleapis.com"));

    // 服务切换会重新调度；旧请求的 500 退避结束后必须放弃，而不是换服务再试。
    pushSettings(boot, { provider: "bing" });
    await wait(60);
    requests[0].resolve({ ok: false, status: 500, body: "{}" });
    await wait(2200);

    const google = requests.filter(request => request.url.includes("translate.googleapis.com"));
    assert.equal(google.length, 1, "the invalidated request retried after the settings change");
    const bing = requests.filter(request => request.url.includes("edge.microsoft.com"));
    assert.equal(bing.length, 1, "the rescheduled translation never ran");
    bing[0].resolve({ ok: true, status: 200, body: JSON.stringify([{ translations: [{ text: "我们需要先检查配置。" }] }]) });
    await wait(80);
    assert.equal(inner.querySelector(".ekdp-kct-translated")?.textContent, "我们需要先检查配置。");
    handle.dispose?.();
  } finally {
    boot.cleanup();
  }
});

test("a stream that ends with everything already translated clears the pending status", async () => {
  let calls = 0;
  const boot = await bootKct({
    translate: async () => {
      calls += 1;
      return { ok: true, status: 200, body: JSON.stringify([{ translations: [{ text: "我们应该立即验证部署配置。" }] }]) };
    },
  });
  try {
    // 以空段落结尾的原文让流式稳定源一次性覆盖全文。
    const { block } = makeBlock(boot.bodyRoot, "We should verify the deployment configuration right now.\n\n");
    const handle = boot.decorator!(block, { open: true, streaming: true });
    await wait(900);
    assert.equal(calls, 1, "the streaming pass never ran");
    const status = block.querySelector(".ekdp-kct-status");
    assert.equal(status?.textContent, "后续内容待翻译");

    handle.update({ open: true, streaming: false });
    await wait(80);
    assert.equal(status?.textContent, "", "the finished stream left a stale pending status");
    assert.equal(status?.dataset.state, "ok");
    handle.dispose?.();
  } finally {
    boot.cleanup();
  }
});

test("replacing the whole source reschedules the translation once the old request settles", async () => {
  const { requests, transport } = captureRequests();
  const boot = await bootKct({ translate: transport });
  try {
    const { block, inner, pre } = makeBlock(boot.bodyRoot, "First version of the notes.");
    const handle = boot.decorator!(block, { open: true, streaming: false });
    await wait(60);
    assert.equal(requests.length, 1);
    const status = block.querySelector(".ekdp-kct-status");

    // 原文整体替换（宿主未发 update）：旧请求结算后必须按新原文重新调度。
    pre.textContent = "Second completely different notes.";
    await wait(60);
    assert.equal(requests.length, 1, "replacement alone must not re-request before the old work settles");
    assert.equal(status?.textContent, "翻译中…", "the stale request did not leave its status behind");

    requests[0].resolve({ ok: true, status: 200, body: JSON.stringify([{ translations: [{ text: "第一版笔记。" }] }]) });
    await wait(80);
    assert.equal(requests.length, 2, "the replaced source was never rescheduled");
    assert.equal(status?.textContent, "翻译中…");

    requests[1].resolve({ ok: true, status: 200, body: JSON.stringify([{ translations: [{ text: "第二版完全不同的笔记。" }] }]) });
    await wait(80);
    assert.equal(inner.querySelector(".ekdp-kct-translated")?.textContent, "第二版完全不同的笔记。");
    assert.equal(status?.dataset.state, "ok");
    assert.equal(status?.textContent, "");
    handle.dispose?.();
  } finally {
    boot.cleanup();
  }
});

test("enhanced thinking translates only on request and keeps both views renderable", async () => {
  const requests: string[][] = [];
  const boot = await bootKct({
    translate: async (_url: string, init: any) => {
      requests.push(JSON.parse(init.body));
      return { ok: true, status: 200, body: JSON.stringify([{ translations: [{ text: "我们应该先检查配置。" }] }]) };
    },
  });
  try {
    const shadowRoot = new FakeElement("shadow-root");
    const shadowHead = new FakeElement("head");
    Object.assign(shadowRoot, { ownerDocument: document, head: shadowHead });
    const block = new FakeElement("details", "", shadowRoot);
    block.setAttribute("data-ekdp-thinking", "");
    const summary = new FakeElement("summary", "", shadowRoot);
    const body = new FakeElement("div", "", shadowRoot);
    body.setAttribute("data-ekdp-thinking-body", "");
    const original = new FakeElement("div", "wb-markdown", shadowRoot);
    const prose = new FakeElement("p", "", shadowRoot);
    prose.textContent = "Prose first.";
    const code = new FakeElement("pre", "", shadowRoot);
    code.textContent = "const a = 1;";
    original.append(prose, code);
    const rawSource = "Prose first.\n\n```js\nconst a = 1;\n```";
    body.setAttribute("data-ekdp-thinking-source", rawSource);
    body.appendChild(original);
    block.append(summary, body);
    boot.bodyRoot.appendChild(block);

    assert.equal(boot.decorationOptions?.includeEnhanced, true);
    const handle = boot.decorator!(block, { open: true, streaming: false });
    await wait(60);
    assert.equal(requests.length, 0, "opening an enhanced block sent its text without a user request");
    const bar = block.querySelector(".ekdp-kct-bar");
    assert.equal(bar?.hidden, false, "the enhanced toolbar stayed hidden");
    const toggle = block.querySelectorAll(".ekdp-kct-btn").find(item => item.textContent === "翻译");
    assert.ok(toggle, "the manual translation action is missing");
    toggle.click();
    await wait(80);
    assert.equal(requests.length, 1);
    assert.ok(requests[0].includes("Prose first."), "the prose was not sent for translation");
    assert.ok(!requests[0].some(text => text.includes("const a = 1;")), "a rendered pre replaced the Markdown source");

    const translated = body.querySelector(".ekdp-kct-translated");
    assert.equal(translated?.tagName, "DIV");
    assert.match(translated?.textContent ?? "", /我们应该先检查配置。/);
    assert.match(translated?.textContent ?? "", /```js\nconst a = 1;\n```/);
    assert.equal(block.getAttribute("data-ekdp-kct-view"), "translated");
    assert.equal(original.getAttribute("data-ekdp-kct-hidden"), "1");
    assert.equal(translated?.getAttribute("data-ekdp-kct-hidden"), null);
    assert.equal(body.getAttribute("data-ekdp-kct-hidden"), null);
    const css = shadowHead.children.map(item => item.textContent).join("\n");
    assert.match(css, /\[data-ekdp-kct-view="translated"\] \[data-ekdp-kct-hidden\]/);
    assert.match(css, /\[data-ekdp-kct-view="original"\] \.ekdp-kct-translated/);
    assert.doesNotMatch(css, /\[data-ekdp-kct-view\][^{]*\[data-ekdp-thinking-body\]/);

    toggle.click();
    assert.equal(block.getAttribute("data-ekdp-kct-view"), "original");
    assert.equal(original.getAttribute("data-ekdp-kct-hidden"), null);
    assert.equal(requests.length, 1, "switching back to the original issued another request");
    toggle.click();
    assert.equal(block.getAttribute("data-ekdp-kct-view"), "translated");
    assert.equal(original.getAttribute("data-ekdp-kct-hidden"), "1");
    assert.equal(requests.length, 1, "showing the cached translation issued another request");

    handle.dispose?.();
    assert.equal(body.querySelector(".ekdp-kct-translated"), null);
    assert.equal(original.getAttribute("data-ekdp-kct-hidden"), null);
    assert.equal(shadowHead.children.length, 0);
  } finally {
    boot.cleanup();
  }
});

test("streaming single paragraph waits 3000ms before splitting and translates only incremental deltas", async () => {
  const sentTexts: string[][] = [];
  const dict: Record<string, string> = {
    "Sentence one. Sentence two. Sentence three.": "句一。句二。句三。",
    "Sentence four. Sentence five.": "句四。句五。",
    "Sentence six.": "句六。",
  };
  const realDateNow = Date.now;
  let now = 1_000_000;
  Date.now = () => now;
  const boot = await bootKct({
    translate: async (_url: string, init: any) => {
      const texts: string[] = JSON.parse(init.body);
      sentTexts.push(texts);
      return {
        ok: true,
        status: 200,
        body: JSON.stringify(texts.map(text => ({ translations: [{ text: dict[text] ?? `译(${text})` }] }))),
      };
    },
  });
  try {
    const { block, inner, pre } = makeBlock(boot.bodyRoot, "Sentence one. Partial");
    const handle = boot.decorator!(block, { open: true, streaming: true });

    // 700ms debounce fires at +750ms (< 3000ms): must not split paragraph yet.
    now = 1_000_750;
    await wait(760);
    assert.equal(sentTexts.length, 0, "split streaming paragraph before 3000ms");

    // Append sentence two at +1500ms and wait another debounce (+2250ms < 3000ms).
    now = 1_001_500;
    pre.textContent = "Sentence one. Sentence two. Partial";
    handle.update({ open: true, streaming: true });
    now = 1_002_250;
    await wait(760);
    assert.equal(sentTexts.length, 0, "streamSince should still block splitting before 3000ms");

    // Append sentence three and cross 3000ms threshold: first prefix translates once.
    now = 1_002_500;
    pre.textContent = "Sentence one. Sentence two. Sentence three. Partial";
    handle.update({ open: true, streaming: true });
    now = 1_003_200;
    await wait(760);
    assert.equal(sentTexts.length, 1, "expected first translation after 3000ms");
    assert.deepEqual(sentTexts[0], ["Sentence one. Sentence two. Sentence three."]);
    assert.equal(inner.querySelector(".ekdp-kct-translated")?.textContent, "句一。句二。句三。 Partial");

    // Next sentence arrives at +800ms after reset (< 3000ms): must wait again.
    now = 1_004_000;
    pre.textContent = "Sentence one. Sentence two. Sentence three. Sentence four. Partial";
    handle.update({ open: true, streaming: true });
    now = 1_004_750;
    await wait(760);
    assert.equal(sentTexts.length, 1, "streamSince did not reset after prefix translation");

    // Cross another 3000ms: only the new delta is sent, never the already translated prefix.
    now = 1_005_500;
    pre.textContent = "Sentence one. Sentence two. Sentence three. Sentence four. Sentence five. Partial";
    handle.update({ open: true, streaming: true });
    now = 1_006_300;
    await wait(760);
    assert.equal(sentTexts.length, 2, "expected incremental delta translation after second 3000ms window");
    assert.deepEqual(sentTexts[1], ["Sentence four. Sentence five."]);
    assert.ok(
      !sentTexts[1].some(text => text.includes("Sentence one") || text.includes("Sentence two") || text.includes("Sentence three")),
      "already translated prefix was requested again",
    );
    assert.equal(inner.querySelector(".ekdp-kct-translated")?.textContent, "句一。句二。句三。 句四。句五。 Partial");

    // Stream finishes with a final sentence: only the final tail is translated.
    now = 1_006_500;
    pre.textContent = "Sentence one. Sentence two. Sentence three. Sentence four. Sentence five. Sentence six.";
    handle.update({ open: true, streaming: false });
    await wait(80);
    assert.equal(sentTexts.length, 3);
    assert.deepEqual(sentTexts[2], ["Sentence six."]);
    assert.equal(inner.querySelector(".ekdp-kct-translated")?.textContent, "句一。句二。句三。 句四。句五。 句六。");
    assert.equal(block.querySelector(".ekdp-kct-status")?.textContent, "");

    // Appending a target-language-only delta reuses the prefix with zero extra requests.
    pre.textContent = "Sentence one. Sentence two. Sentence three. Sentence four. Sentence five. Sentence six. 仅追加中文尾巴。";
    handle.update({ open: true, streaming: false });
    await wait(80);
    assert.equal(sentTexts.length, 3, "target-language-only delta should not issue a request");
    assert.equal(inner.querySelector(".ekdp-kct-translated")?.textContent, "句一。句二。句三。 句四。句五。 句六。 仅追加中文尾巴。");
    assert.equal(block.getAttribute("data-ekdp-kct-view"), "translated");
    handle.dispose?.();
  } finally {
    Date.now = realDateNow;
    boot.cleanup();
  }
});

test("decorating the same block twice keeps only one toolbar", async () => {
  const boot = await bootKct({
    settings: { autoTranslate: false },
    translate: async () => ({ ok: true, status: 200, body: "[]" }),
  });
  try {
    const { block } = makeBlock(boot.bodyRoot, "Check idempotent bar mounting.");
    const first = boot.decorator!(block, { open: true, streaming: false });
    const second = boot.decorator!(block, { open: true, streaming: false });
    assert.equal(block.querySelectorAll(".ekdp-kct-bar").length, 1, "duplicate toolbar was mounted on the same block");

    first?.dispose?.();
    assert.equal(block.querySelectorAll(".ekdp-kct-bar").length, 1, "disposing stale handle removed active toolbar");

    second?.dispose?.();
    assert.equal(block.querySelectorAll(".ekdp-kct-bar").length, 0, "disposing active handle left toolbar behind");
  } finally {
    boot.cleanup();
  }
});

test("enhanced thinking toolbar stays visible while open and streaming", async () => {
  const requests: string[][] = [];
  const boot = await bootKct({
    translate: async (_url: string, init: any) => {
      requests.push(JSON.parse(init.body));
      return { ok: true, status: 200, body: JSON.stringify([{ translations: [{ text: "思考完成后的译文。" }] }]) };
    },
  });
  try {
    const shadowRoot = new FakeElement("shadow-root");
    const shadowHead = new FakeElement("head");
    Object.assign(shadowRoot, { ownerDocument: document, head: shadowHead });
    const block = new FakeElement("details", "", shadowRoot);
    block.setAttribute("data-ekdp-thinking", "");
    const summary = new FakeElement("summary", "", shadowRoot);
    const body = new FakeElement("div", "", shadowRoot);
    body.setAttribute("data-ekdp-thinking-body", "");
    body.setAttribute("data-ekdp-thinking-source", "Streaming thought in progress.");
    block.append(summary, body);
    boot.bodyRoot.appendChild(block);

    const handle = boot.decorator!(block, { open: true, streaming: true });
    const bar = block.querySelector(".ekdp-kct-bar");
    assert.equal(bar?.hidden, false, "enhanced toolbar was hidden during initial open stream");

    handle.update({ open: true, streaming: true });
    assert.equal(bar?.hidden, false, "enhanced toolbar was hidden on streaming update");
    assert.equal(requests.length, 0, "enhanced streaming block auto-translated unexpectedly");

    const toggle = block.querySelectorAll(".ekdp-kct-btn").find(item => item.textContent === "翻译");
    assert.ok(toggle);
    toggle.click();
    await wait(40);
    assert.equal(requests.length, 0, "enhanced streaming block translated before completion");
    assert.equal(block.querySelector(".ekdp-kct-status")?.textContent, "等待思考完成");

    handle.update({ open: true, streaming: false });
    await wait(80);
    assert.equal(requests.length, 1, "manual request did not complete once streaming ended");
    assert.equal(body.querySelector(".ekdp-kct-translated")?.textContent, "思考完成后的译文。");

    handle.update({ open: false, streaming: false });
    assert.equal(bar?.hidden, true, "toolbar stayed visible after collapsing enhanced block");
    handle.dispose?.();
  } finally {
    boot.cleanup();
  }
});

test("KCT settings unmounting while credentials.list is pending removes ekdp-kct:settings listener immediately", async () => {
  const runtimeGlobals = globalThis as unknown as Record<string, any>;
  const prevDocument = runtimeGlobals.document;
  const prevWindow = runtimeGlobals.window;
  const windowEvents = new EventTarget();
  runtimeGlobals.document = {
    createElement: (tag: string) => new FakeElement(tag),
  };
  runtimeGlobals.window = {
    addEventListener: (...args: any[]) => (windowEvents.addEventListener as any)(...args),
    removeEventListener: (...args: any[]) => (windowEvents.removeEventListener as any)(...args),
    dispatchEvent: (event: any) => windowEvents.dispatchEvent(event),
  };

  try {
    const moduleUrl = `data:text/javascript;base64,${Buffer.from(settingsSource).toString("base64")}`;
    const settingsModule = await import(moduleUrl);

    let storageGetCalls = 0;
    let storageListCalls = 0;
    let resolveCredentials!: (value: unknown) => void;
    let credentialsRequested!: () => void;
    const credentialsSeen = new Promise<void>(resolve => {
      credentialsRequested = resolve;
    });
    const credentialsPending = new Promise<unknown>(resolve => {
      resolveCredentials = resolve;
    });

    const pluginRuntime = createPluginRuntime({
      slots: {},
      request: async path => {
        if (path === "/plugin-runtime/storage.get") {
          storageGetCalls += 1;
          return { value: { found: true, value: JSON.stringify({ targetLanguage: "zh", provider: "bing", autoTranslate: true }) } };
        }
        if (path === "/plugin-runtime/storage.list") {
          storageListCalls += 1;
          return { value: { keys: [] } };
        }
        if (path === "/plugin-runtime/credentials.list") {
          credentialsRequested();
          return { value: await credentialsPending };
        }
        return { value: {} };
      },
    });

    const active = await pluginRuntime.activate(
      { activate: () => {} },
      {
        id: "kimicode-plus.kct",
        version: "1.0.0",
        granted: ["ui:settings", "storage", "credentials:translator"],
        token: "t-kct",
      },
    );

    const container = new FakeElement("div");
    const mountPromise = active.mountSettings(settingsModule, container as unknown as HTMLElement);
    await credentialsSeen;
    assert.equal(storageGetCalls, 1, "initial settings load should have executed once");
    assert.equal(container.children.length, 1, "settings card should be mounted before credentials.list resolves");

    // Deactivating the plugin while credentials.list is pending must remove the window listener immediately.
    pluginRuntime.deactivate("kimicode-plus.kct");
    assert.equal(container.children.length, 0, "settings card was not removed on deactivation");

    windowEvents.dispatchEvent(new Event("ekdp-kct:settings"));
    await wait(20);
    assert.equal(storageGetCalls, 1, "ekdp-kct:settings listener leaked and triggered storage.get after deactivation");

    resolveCredentials({ items: [] });
    await mountPromise;
    assert.equal(storageListCalls, 0, "refreshCache ran after settings panel was already disposed");
    assert.equal(container.children.length, 0);

    windowEvents.dispatchEvent(new Event("ekdp-kct:cache"));
    await wait(20);
    assert.equal(storageListCalls, 0, "ekdp-kct:cache listener leaked after disposal during pending mount");
    pluginRuntime.dispose();

    // Direct renderSettings call whose ctx.onDispose is triggered while credentials.list is pending.
    let directGets = 0;
    let directLists = 0;
    let releaseDirectCreds!: (value: unknown) => void;
    let directCredsRequested!: () => void;
    const directCredsSeen = new Promise<void>(resolve => {
      directCredsRequested = resolve;
    });
    const directCredsPending = new Promise<unknown>(resolve => {
      releaseDirectCreds = resolve;
    });
    const directDisposers: Array<() => void> = [];
    const directContainer = new FakeElement("div");
    const directPromise = settingsModule.renderSettings(
      {
        plugin: { version: "1.0.0" },
        storage: {
          get: async () => {
            directGets += 1;
            return { targetLanguage: "zh", provider: "bing", autoTranslate: true };
          },
          set: async () => {},
          delete: async () => {},
          list: async () => {
            directLists += 1;
            return [];
          },
        },
        core: {
          request: async () => {
            directCredsRequested();
            return directCredsPending;
          },
        },
        onDispose: (fn: () => void) => directDisposers.push(fn),
      },
      directContainer,
    );
    await directCredsSeen;
    assert.equal(directGets, 1);
    for (const fn of directDisposers) fn();
    windowEvents.dispatchEvent(new Event("ekdp-kct:settings"));
    await wait(20);
    assert.equal(directGets, 1, "direct settings handle disposal did not remove ekdp-kct:settings listener");
    releaseDirectCreds({ items: [] });
    await directPromise;
    assert.equal(directLists, 0);
  } finally {
    runtimeGlobals.document = prevDocument;
    runtimeGlobals.window = prevWindow;
  }
});

test("streaming protects unclosed inline code, fenced code, and links from being split into translation", async () => {
  const sentTexts: string[][] = [];
  const dict: Record<string, string> = {
    "Completed sentence.": "已完成句子。",
    "Done.": "完成。",
    "Link prefix sentence.": "链接前缀句。",
    "Url prefix sentence.": "网址前缀句。",
    "Fence prefix sentence.": "围栏前缀句。",
  };
  const realDateNow = Date.now;
  let now = 2_000_000;
  Date.now = () => now;
  const boot = await bootKct({
    translate: async (_url: string, init: any) => {
      const texts: string[] = JSON.parse(init.body);
      sentTexts.push(texts);
      return {
        ok: true,
        status: 200,
        body: JSON.stringify(texts.map(text => ({ translations: [{ text: dict[text] ?? `译(${text})` }] }))),
      };
    },
  });
  try {
    // 1) Unclosed inline code span: never send code fragment for translation.
    const { block, inner, pre } = makeBlock(boot.bodyRoot, "Completed sentence. `private fragment. still typing");
    const handle = boot.decorator!(block, { open: true, streaming: true });
    now = 2_003_200;
    await wait(760);
    assert.equal(sentTexts.length, 1, "expected completed prefix before unclosed inline code to translate");
    assert.deepEqual(sentTexts[0], ["Completed sentence."]);
    assert.ok(
      !sentTexts.flat().some(text => text.includes("private fragment")),
      "unclosed inline code fragment leaked to external translator",
    );
    assert.equal(inner.querySelector(".ekdp-kct-translated")?.textContent, "已完成句子。 `private fragment. still typing");

    // Closing the inline code and finishing the sentence preserves inline code verbatim and translates only Done.
    pre.textContent = "Completed sentence. `private fragment. still typing` Done.";
    handle.update({ open: true, streaming: false });
    await wait(80);
    assert.equal(sentTexts.length, 2);
    assert.deepEqual(sentTexts[1], ["Done."]);
    assert.ok(
      !sentTexts.flat().some(text => text.includes("private fragment")),
      "closed inline code fragment was translated instead of preserved",
    );
    assert.equal(inner.querySelector(".ekdp-kct-translated")?.textContent, "已完成句子。 `private fragment. still typing` 完成。");
    handle.dispose?.();

    // 2) Unclosed Markdown link, bare URL, and unclosed fenced code block must also be excluded.
    const linkBlock = makeBlock(boot.bodyRoot, "Link prefix sentence. [ref. note](https://example.com/a.b");
    const linkHandle = boot.decorator!(linkBlock.block, { open: true, streaming: true });
    now += 3_200;
    await wait(760);
    assert.equal(sentTexts.length, 3);
    assert.deepEqual(sentTexts[2], ["Link prefix sentence."]);
    assert.ok(!sentTexts.flat().some(text => text.includes("ref. note") || text.includes("example.com")));
    assert.equal(
      linkBlock.inner.querySelector(".ekdp-kct-translated")?.textContent,
      "链接前缀句。 [ref. note](https://example.com/a.b",
    );
    linkHandle.dispose?.();

    const urlBlock = makeBlock(boot.bodyRoot, "Url prefix sentence. https://example.com/a.b");
    const urlHandle = boot.decorator!(urlBlock.block, { open: true, streaming: true });
    now += 3_200;
    await wait(760);
    assert.equal(sentTexts.length, 4);
    assert.deepEqual(sentTexts[3], ["Url prefix sentence."]);
    assert.ok(!sentTexts.flat().some(text => text.includes("example.com")));
    assert.equal(
      urlBlock.inner.querySelector(".ekdp-kct-translated")?.textContent,
      "网址前缀句。 https://example.com/a.b",
    );
    urlHandle.dispose?.();

    const fenceBlock = makeBlock(boot.bodyRoot, "Fence prefix sentence.\n```js\nconst secret = 1. still typing");
    const fenceHandle = boot.decorator!(fenceBlock.block, { open: true, streaming: true });
    now += 3_200;
    await wait(760);
    assert.equal(sentTexts.length, 5);
    assert.deepEqual(sentTexts[4], ["Fence prefix sentence."]);
    assert.ok(!sentTexts.flat().some(text => text.includes("secret")));
    assert.equal(
      fenceBlock.inner.querySelector(".ekdp-kct-translated")?.textContent,
      "围栏前缀句。\n```js\nconst secret = 1. still typing",
    );
    fenceHandle.dispose?.();
  } finally {
    Date.now = realDateNow;
    boot.cleanup();
  }
});

test("streaming single paragraph auto-schedules translation after 3000ms pause with no new tokens", async () => {
  const sentTexts: string[][] = [];
  const boot = await bootKct({
    translate: async (_url: string, init: any) => {
      const texts: string[] = JSON.parse(init.body);
      sentTexts.push(texts);
      return {
        ok: true,
        status: 200,
        body: JSON.stringify(texts.map(() => ({ translations: [{ text: "停顿前的句子。" }] }))),
      };
    },
  });
  try {
    const { block, inner } = makeBlock(boot.bodyRoot, "Paused sentence. still thinking");
    const handle = boot.decorator!(block, { open: true, streaming: true });

    // After the initial 700ms debounce, 3000ms has not elapsed yet.
    await wait(850);
    assert.equal(sentTexts.length, 0, "translated single streaming paragraph before 3000ms");

    // With zero further handle.update() calls, the scheduled remaining-wait timer fires once 3000ms elapses.
    await wait(2450);
    assert.equal(sentTexts.length, 1, "remaining-wait timer did not fire after 3000ms pause");
    assert.deepEqual(sentTexts[0], ["Paused sentence."]);
    assert.equal(inner.querySelector(".ekdp-kct-translated")?.textContent, "停顿前的句子。 still thinking");
    handle.dispose?.();
  } finally {
    boot.cleanup();
  }
});

test("streaming failed prefix is not resent while incomplete tail grows until next sentence or manual retry", async () => {
  const sentTexts: string[][] = [];
  let mode: "429" | "400" | "ok" = "429";
  const realDateNow = Date.now;
  let now = 3_000_000;
  Date.now = () => now;
  const boot = await bootKct({
    translate: async (_url: string, init: any) => {
      const texts: string[] = JSON.parse(init.body);
      sentTexts.push(texts);
      if (mode === "429") {
        return { ok: false, status: 429, headers: { "Retry-After": "1" }, body: JSON.stringify({ error: "rate limit" }) };
      }
      if (mode === "400") {
        return { ok: false, status: 400, body: JSON.stringify({ error: "bad request" }) };
      }
      return {
        ok: true,
        status: 200,
        body: JSON.stringify(texts.map(text => ({ translations: [{ text: `译(${text})` }] }))),
      };
    },
  });
  try {
    const { block, inner, pre } = makeBlock(boot.bodyRoot, "First sentence. typing");
    const handle = boot.decorator!(block, { open: true, streaming: true });
    const status = block.querySelector(".ekdp-kct-status")!;

    // Trigger first streaming prefix after 3000ms; 429 retries twice internally then marks prefix failed.
    now = 3_003_200;
    const deadline = realDateNow() + 6_000;
    while (status.dataset.state !== "error" && realDateNow() < deadline) {
      await wait(100);
    }
    assert.equal(status.dataset.state, "error", "expected streaming 429 to enter error state");
    const callsAfter429 = sentTexts.length;
    assert.ok(callsAfter429 >= 1);

    // Move clock past 429 cooldown so only failedSource/failedSignature deduplication guards against resending.
    now += 10_000;

    // Appending characters inside the same incomplete sentence must NOT resend the failed prefix.
    pre.textContent = "First sentence. typing more";
    handle.update({ open: true, streaming: true });
    await wait(780);
    pre.textContent = "First sentence. typing more words";
    handle.update({ open: true, streaming: true });
    await wait(780);
    assert.equal(sentTexts.length, callsAfter429, "failed streaming prefix was resent while incomplete tail grew");

    // Manual retry clears the failed guard immediately (using 400 to test immediate failure without backoff).
    mode = "400";
    status.click();
    await wait(60);
    assert.equal(sentTexts.length, callsAfter429 + 1, "manual retry did not resend failed prefix immediately");
    assert.equal(status.dataset.state, "error");

    // Growing the same incomplete tail after a failed manual retry must still NOT trigger a retry storm.
    pre.textContent = "First sentence. typing more words still";
    handle.update({ open: true, streaming: true });
    await wait(780);
    assert.equal(sentTexts.length, callsAfter429 + 1, "failed manual retry caused a resend storm on tail growth");

    // Forming the next complete sentence changes the stable prefix and automatically recovers.
    mode = "ok";
    pre.textContent = "First sentence. Second sentence. tail";
    handle.update({ open: true, streaming: true });
    await wait(780);
    assert.equal(sentTexts.length, callsAfter429 + 2, "completing a new sentence did not recover from failed prefix");
    assert.deepEqual(sentTexts.at(-1), ["First sentence. Second sentence."]);
    assert.equal(inner.querySelector(".ekdp-kct-translated")?.textContent, "译(First sentence. Second sentence.) tail");
    handle.dispose?.();
  } finally {
    Date.now = realDateNow;
    boot.cleanup();
  }
});

test("streaming protects unclosed Markdown link labels, pending brackets, and quoted link titles", async () => {
  const sentTexts: string[][] = [];
  const realDateNow = Date.now;
  let now = 4_000_000;
  Date.now = () => now;
  const boot = await bootKct({
    translate: async (_url: string, init: any) => {
      const texts: string[] = JSON.parse(init.body);
      sentTexts.push(texts);
      return {
        ok: true,
        status: 200,
        body: JSON.stringify(texts.map(text => ({ translations: [{ text: `译(${text})` }] }))),
      };
    },
  });
  try {
    // 1) Unclosed `[` link label: at 3000ms only translate "Completed. " without splitting at "Private label."
    const labelBlock = makeBlock(boot.bodyRoot, "Completed. [Private label. still typing");
    const labelHandle = boot.decorator!(labelBlock.block, { open: true, streaming: true });
    now += 3_200;
    await wait(780);
    assert.equal(sentTexts.length, 1);
    assert.deepEqual(sentTexts[0], ["Completed."]);
    assert.ok(
      !sentTexts.flat().some(text => text.includes("Private label")),
      "unclosed Markdown link label leaked into translation",
    );
    assert.equal(
      labelBlock.inner.querySelector(".ekdp-kct-translated")?.textContent,
      "译(Completed.) [Private label. still typing",
    );

    // Trailing `]` waits for the next character in case `(` follows immediately.
    labelBlock.pre.textContent = "Completed. [Private label. still typing]";
    labelHandle.update({ open: true, streaming: true });
    now += 3_200;
    await wait(780);
    assert.equal(sentTexts.length, 1, "pending ] bracket was translated before confirming whether ( follows");

    // Closing into a full link + Next. incrementally translates the remainder.
    labelBlock.pre.textContent = "Completed. [Private label. still typing](https://example.com) Next.";
    labelHandle.update({ open: true, streaming: false });
    await wait(80);
    assert.equal(sentTexts.length, 2, "closing the Markdown link did not continue translation");
    assert.ok(
      !sentTexts[1].includes("Completed."),
      "already translated prefix was re-sent instead of continuing incrementally",
    );
    assert.ok(
      sentTexts[1].some(text => text.includes("Next.")),
      "post-link sentence was not translated after link closed",
    );
    const labelRendered = labelBlock.inner.querySelector(".ekdp-kct-translated")?.textContent ?? "";
    assert.ok(labelRendered.startsWith("译(Completed.) "), "previous translated prefix was lost");
    assert.ok(labelRendered.includes("https://example.com)"), "link URL was not preserved");
    assert.ok(labelRendered.endsWith("译(Next.)"), "post-link translation missing");
    labelHandle.dispose?.();
  } finally {
    Date.now = realDateNow;
    boot.cleanup();
  }

  // 2) Unclosed link with spaces and quoted title: at 3000ms only translate "Completed. "
  const titleSentTexts: string[][] = [];
  let titleNow = 4_100_000;
  Date.now = () => titleNow;
  const titleBoot = await bootKct({
    translate: async (_url: string, init: any) => {
      const texts: string[] = JSON.parse(init.body);
      titleSentTexts.push(texts);
      return {
        ok: true,
        status: 200,
        body: JSON.stringify(texts.map(text => ({ translations: [{ text: `译(${text})` }] }))),
      };
    },
  });
  try {
    const titleBlock = makeBlock(titleBoot.bodyRoot, 'Completed. [Doc](https://example.com "Title. still typing');
    const titleHandle = titleBoot.decorator!(titleBlock.block, { open: true, streaming: true });
    titleNow += 3_200;
    await wait(780);
    assert.equal(titleSentTexts.length, 1);
    assert.deepEqual(titleSentTexts[0], ["Completed."]);
    assert.ok(
      !titleSentTexts[0].some(text => text.includes("Doc") || text.includes("Title") || text.includes("example.com")),
      "unclosed Markdown link with quoted title leaked into translation",
    );
    assert.equal(
      titleBlock.inner.querySelector(".ekdp-kct-translated")?.textContent,
      '译(Completed.) [Doc](https://example.com "Title. still typing',
    );
    titleHandle.dispose?.();
  } finally {
    Date.now = realDateNow;
    titleBoot.cleanup();
  }
});

test("failed streaming paragraph schedules 3000ms unlock for longer prefix and stops if that prefix also fails", async () => {
  const sentTexts: string[][] = [];
  let attempt = 0;
  let failSecond = false;
  const boot = await bootKct({
    translate: async (_url: string, init: any) => {
      const texts: string[] = JSON.parse(init.body);
      sentTexts.push(texts);
      attempt += 1;
      if (attempt === 1 || (failSecond && attempt >= 2)) {
        return { ok: false, status: 400, body: JSON.stringify({ error: "transient error" }) };
      }
      return {
        ok: true,
        status: 200,
        body: JSON.stringify(texts.map(text => ({ translations: [{ text: `译(${text})` }] }))),
      };
    },
  });
  try {
    // 1) Initial 700ms paragraph fails while stream pauses; 3000ms timer wakes up for longer prefix.
    const { block, inner } = makeBlock(boot.bodyRoot, "First para.\n\nSecond sentence. still typing");
    const handle = boot.decorator!(block, { open: true, streaming: true });
    const status = block.querySelector(".ekdp-kct-status")!;

    await wait(850);
    assert.equal(sentTexts.length, 1, "first paragraph should have been attempted after 700ms");
    assert.deepEqual(sentTexts[0], ["First para."]);
    assert.equal(status.dataset.state, "error", "first paragraph failure should show error status");

    await wait(2450);
    assert.equal(sentTexts.length, 2, "3000ms wait timer did not wake up for longer unlocked prefix");
    assert.deepEqual(sentTexts[1], ["First para.", "Second sentence."]);
    assert.equal(
      inner.querySelector(".ekdp-kct-translated")?.textContent,
      "译(First para.)\n\n译(Second sentence.) still typing",
    );
    handle.dispose?.();

    // 2) If the unlocked 3000ms prefix also fails, no retry timer is scheduled.
    attempt = 0;
    failSecond = true;
    sentTexts.length = 0;
    const stormBlock = makeBlock(boot.bodyRoot, "Alpha para.\n\nBeta sentence. still typing");
    const stormHandle = boot.decorator!(stormBlock.block, { open: true, streaming: true });

    await wait(850);
    assert.equal(sentTexts.length, 1);
    assert.deepEqual(sentTexts[0], ["Alpha para."]);
    await wait(2450);
    assert.equal(sentTexts.length, 2, "3000ms unlock attempt did not fire");
    assert.deepEqual(sentTexts[1], ["Alpha para.", "Beta sentence."]);
    await wait(250);
    assert.equal(sentTexts.length, 2, "failing the unlocked prefix triggered a retry storm");
    stormHandle.dispose?.();
  } finally {
    boot.cleanup();
  }
});

test("streaming protects multiline unclosed Markdown link labels, unlocks on blank line, and preserves closed multiline links incrementally", async () => {
  const realDateNow = Date.now;
  let now = 5_000_000;
  Date.now = () => now;

  // 1) Multiline unclosed label stays protected at 3000ms, then unlocks once a blank line proves it is not a link.
  const blankSentTexts: string[][] = [];
  const blankBoot = await bootKct({
    translate: async (_url: string, init: any) => {
      const texts: string[] = JSON.parse(init.body);
      blankSentTexts.push(texts);
      return {
        ok: true,
        status: 200,
        body: JSON.stringify(texts.map(text => ({ translations: [{ text: `译(${text})` }] }))),
      };
    },
  });
  try {
    const { block, inner, pre } = makeBlock(blankBoot.bodyRoot, "Completed. [private label.\nstill typing");
    const handle = blankBoot.decorator!(block, { open: true, streaming: true });
    now += 3_200;
    await wait(780);
    assert.equal(blankSentTexts.length, 1);
    assert.deepEqual(blankSentTexts[0], ["Completed."]);
    assert.ok(
      !blankSentTexts.flat().some(text => text.includes("private label") || text.includes("still typing")),
      "multiline unclosed Markdown link label leaked into translation",
    );
    assert.equal(
      inner.querySelector(".ekdp-kct-translated")?.textContent,
      "译(Completed.) [private label.\nstill typing",
    );

    pre.textContent = "Completed. [private label.\n\nNext para.";
    handle.update({ open: true, streaming: true });
    await wait(780);
    assert.equal(blankSentTexts.length, 2, "blank line did not unlock non-link bracket paragraph");
    assert.deepEqual(blankSentTexts[1], ["[private label."]);
    assert.equal(
      inner.querySelector(".ekdp-kct-translated")?.textContent,
      "译(Completed.) 译([private label.)\n\nNext para.",
    );

    handle.update({ open: true, streaming: false });
    await wait(80);
    assert.equal(blankSentTexts.length, 3);
    assert.deepEqual(blankSentTexts[2], ["Next para."]);
    assert.equal(
      inner.querySelector(".ekdp-kct-translated")?.textContent,
      "译(Completed.) 译([private label.)\n\n译(Next para.)",
    );
    handle.dispose?.();
  } finally {
    blankBoot.cleanup();
  }

  // 2) Closing a multiline label into a full link preserves the multiline link and translates only Tail. incrementally.
  const closeSentTexts: string[][] = [];
  now = 5_100_000;
  const closeBoot = await bootKct({
    translate: async (_url: string, init: any) => {
      const texts: string[] = JSON.parse(init.body);
      closeSentTexts.push(texts);
      return {
        ok: true,
        status: 200,
        body: JSON.stringify(texts.map(text => ({ translations: [{ text: `译(${text})` }] }))),
      };
    },
  });
  try {
    const { block, inner, pre } = makeBlock(closeBoot.bodyRoot, "Completed. [private label.\nstill typing");
    const handle = closeBoot.decorator!(block, { open: true, streaming: true });
    now += 3_200;
    await wait(780);
    assert.equal(closeSentTexts.length, 1);
    assert.deepEqual(closeSentTexts[0], ["Completed."]);
    assert.equal(
      inner.querySelector(".ekdp-kct-translated")?.textContent,
      "译(Completed.) [private label.\nstill typing",
    );

    // Even once closed while still streaming, neither the dot nor the newline inside the multiline link splits it.
    pre.textContent = "Completed. [private label.\nstill typing](https://example.test/path) waiting";
    handle.update({ open: true, streaming: true });
    now += 3_200;
    await wait(780);
    assert.equal(closeSentTexts.length, 1, "closed multiline link was split across its internal newline or period");

    pre.textContent = "Completed. [private label.\nstill typing](https://example.test/path) Tail.";
    handle.update({ open: true, streaming: false });
    await wait(80);
    assert.equal(closeSentTexts.length, 2, "closing multiline link did not translate Tail.");
    assert.deepEqual(closeSentTexts[1], ["Tail."]);
    assert.ok(
      !closeSentTexts.flat().some(text => text.includes("private label") || text.includes("still typing") || text.includes("example.test")),
      "multiline Markdown link content leaked into translation",
    );
    assert.equal(
      inner.querySelector(".ekdp-kct-translated")?.textContent,
      "译(Completed.) [private label.\nstill typing](https://example.test/path) 译(Tail.)",
    );
    handle.dispose?.();
  } finally {
    Date.now = realDateNow;
    closeBoot.cleanup();
  }
});

test("streaming protects Markdown links with parentheses inside double- and single-quoted titles", async () => {
  const realDateNow = Date.now;
  let now = 5_200_000;
  Date.now = () => now;
  const cases = [
    {
      name: "double-quoted title",
      unclosed: 'Completed. [Doc](https://example.test/path "Private ) title. still typing',
      streamingClosed: 'Completed. [Doc](https://example.test/path "Private ) title. Done") waiting',
      final: 'Completed. [Doc](https://example.test/path "Private ) title. Done") Tail.',
      expectedFinal: '译(Completed.) [Doc](https://example.test/path "Private ) title. Done") 译(Tail.)',
    },
    {
      name: "single-quoted title",
      unclosed: "Completed. [Doc](https://example.test/path 'Private ) title. still typing",
      streamingClosed: "Completed. [Doc](https://example.test/path 'Private ) title. Done') waiting",
      final: "Completed. [Doc](https://example.test/path 'Private ) title. Done') Tail.",
      expectedFinal: "译(Completed.) [Doc](https://example.test/path 'Private ) title. Done') 译(Tail.)",
    },
  ];
  try {
    for (const tc of cases) {
      const sentTexts: string[][] = [];
      now += 100_000;
      const boot = await bootKct({
        translate: async (_url: string, init: any) => {
          const texts: string[] = JSON.parse(init.body);
          sentTexts.push(texts);
          return {
            ok: true,
            status: 200,
            body: JSON.stringify(texts.map(text => ({ translations: [{ text: `译(${text})` }] }))),
          };
        },
      });
      try {
        const { block, inner, pre } = makeBlock(boot.bodyRoot, tc.unclosed);
        const handle = boot.decorator!(block, { open: true, streaming: true });
        now += 3_200;
        await wait(780);
        assert.equal(sentTexts.length, 1, `${tc.name}: expected only Completed. to translate`);
        assert.deepEqual(sentTexts[0], ["Completed."]);
        assert.ok(
          !sentTexts.flat().some(text => text.includes("Doc") || text.includes("Private") || text.includes("title") || text.includes("example.test")),
          `${tc.name}: unclosed quoted title with ) leaked into translation`,
        );
        assert.equal(
          inner.querySelector(".ekdp-kct-translated")?.textContent,
          `译(Completed.) ${tc.unclosed.slice("Completed. ".length)}`,
        );

        pre.textContent = tc.streamingClosed;
        handle.update({ open: true, streaming: true });
        now += 3_200;
        await wait(780);
        assert.equal(sentTexts.length, 1, `${tc.name}: closed link title was split at internal period during streaming`);

        pre.textContent = tc.final;
        handle.update({ open: true, streaming: false });
        await wait(80);
        assert.equal(sentTexts.length, 2, `${tc.name}: closing link did not incrementally translate Tail.`);
        assert.deepEqual(sentTexts[1], ["Tail."]);
        assert.ok(
          !sentTexts.flat().some(text => text.includes("Doc") || text.includes("Private") || text.includes("title") || text.includes("example.test")),
          `${tc.name}: closed link content leaked into translation`,
        );
        assert.equal(inner.querySelector(".ekdp-kct-translated")?.textContent, tc.expectedFinal);
        handle.dispose?.();
      } finally {
        boot.cleanup();
      }
    }
  } finally {
    Date.now = realDateNow;
  }
});

test("non-streaming translation preserves closed multiline links and links with dotted quoted titles verbatim", async () => {
  const sentTexts: string[][] = [];
  const boot = await bootKct({
    translate: async (_url: string, init: any) => {
      const texts: string[] = JSON.parse(init.body);
      sentTexts.push(texts);
      return {
        ok: true,
        status: 200,
        body: JSON.stringify(texts.map(text => ({ translations: [{ text: `译(${text})` }] }))),
      };
    },
  });
  try {
    const multilineSource = "Completed. [private label.\nsecond line](https://example.test/path) Tail.";
    const multilineBlock = makeBlock(boot.bodyRoot, multilineSource);
    const multilineHandle = boot.decorator!(multilineBlock.block, { open: true, streaming: false });
    await wait(80);
    assert.equal(sentTexts.length, 1);
    assert.deepEqual(sentTexts[0], ["Completed.", "Tail."]);
    assert.equal(
      multilineBlock.inner.querySelector(".ekdp-kct-translated")?.textContent,
      "译(Completed.) [private label.\nsecond line](https://example.test/path) 译(Tail.)",
    );
    multilineHandle.dispose?.();

    const quotedTitleSource = 'Intro. [Doc. Sub](https://example.test/path "Private ) title. Here") Outro.';
    const quotedTitleBlock = makeBlock(boot.bodyRoot, quotedTitleSource);
    const quotedTitleHandle = boot.decorator!(quotedTitleBlock.block, { open: true, streaming: false });
    await wait(80);
    assert.equal(sentTexts.length, 2);
    assert.deepEqual(sentTexts[1], ["Intro.", "Outro."]);
    assert.equal(
      quotedTitleBlock.inner.querySelector(".ekdp-kct-translated")?.textContent,
      '译(Intro.) [Doc. Sub](https://example.test/path "Private ) title. Here") 译(Outro.)',
    );
    quotedTitleHandle.dispose?.();

    const imageAndBalancedSource = 'See ![Chart. v1](<https://example.test/a b> "Note ) here.") and [Wiki](https://en.wikipedia.org/wiki/Foo_(bar)) End.';
    const imageBlock = makeBlock(boot.bodyRoot, imageAndBalancedSource);
    const imageHandle = boot.decorator!(imageBlock.block, { open: true, streaming: false });
    await wait(80);
    assert.equal(sentTexts.length, 3);
    assert.deepEqual(sentTexts[2], ["See", "and", "End."]);
    assert.equal(
      imageBlock.inner.querySelector(".ekdp-kct-translated")?.textContent,
      '译(See) ![Chart. v1](<https://example.test/a b> "Note ) here.") 译(and) [Wiki](https://en.wikipedia.org/wiki/Foo_(bar)) 译(End.)',
    );
    imageHandle.dispose?.();

    const targetLangWithLink = "已完成 [private label.\nsecond line](https://example.test/path) 结束。";
    const zhBlock = makeBlock(boot.bodyRoot, targetLangWithLink);
    const zhHandle = boot.decorator!(zhBlock.block, { open: true, streaming: false });
    await wait(80);
    assert.equal(sentTexts.length, 3, "target-language text with English multiline link should not invoke translator");
    assert.equal(zhBlock.inner.querySelector(".ekdp-kct-translated"), null);
    assert.equal(zhBlock.pre.textContent, targetLangWithLink);
    assert.equal(zhBlock.pre.getAttribute("data-ekdp-kct-hidden"), null);
    zhHandle.dispose?.();
  } finally {
    boot.cleanup();
  }
});

test("streaming protects Markdown links with backticks in destination or title until closed", async () => {
  const realDateNow = Date.now;
  let now = 5_600_000;
  Date.now = () => now;
  const cases = [
    {
      name: "paired backticks in URL with quoted title",
      unclosed: 'Completed. [label](https://example.test/foo`bar`baz "private title. more',
      streamingClosed: 'Completed. [label](https://example.test/foo`bar`baz "private title. more") waiting',
      final: 'Completed. [label](https://example.test/foo`bar`baz "private title. more") Tail.',
      expectedFinal: '译(Completed.) [label](https://example.test/foo`bar`baz "private title. more") 译(Tail.)',
    },
    {
      name: "single backtick in URL with quoted title",
      unclosed: 'Completed. [label](https://example.test/foo`bar "private title. more',
      streamingClosed: 'Completed. [label](https://example.test/foo`bar "private title. more") waiting',
      final: 'Completed. [label](https://example.test/foo`bar "private title. more") Tail.',
      expectedFinal: '译(Completed.) [label](https://example.test/foo`bar "private title. more") 译(Tail.)',
    },
    {
      name: "backticks inside quoted title",
      unclosed: 'Completed. [label](https://example.test/path "title `code` here. more',
      streamingClosed: 'Completed. [label](https://example.test/path "title `code` here. more") waiting',
      final: 'Completed. [label](https://example.test/path "title `code` here. more") Tail.',
      expectedFinal: '译(Completed.) [label](https://example.test/path "title `code` here. more") 译(Tail.)',
    },
  ];
  try {
    for (const tc of cases) {
      const sentTexts: string[][] = [];
      now += 100_000;
      const boot = await bootKct({
        translate: async (_url: string, init: any) => {
          const texts: string[] = JSON.parse(init.body);
          sentTexts.push(texts);
          return {
            ok: true,
            status: 200,
            body: JSON.stringify(texts.map(text => ({ translations: [{ text: `译(${text})` }] }))),
          };
        },
      });
      try {
        const { block, inner, pre } = makeBlock(boot.bodyRoot, tc.unclosed);
        const handle = boot.decorator!(block, { open: true, streaming: true });
        now += 3_200;
        await wait(780);
        assert.equal(sentTexts.length, 1, `${tc.name}: expected only Completed. to translate`);
        assert.deepEqual(sentTexts[0], ["Completed."]);
        assert.ok(
          !sentTexts.flat().some(text => text.includes("label") || text.includes("title") || text.includes("example.test")),
          `${tc.name}: unclosed backtick link leaked into translation`,
        );
        assert.equal(
          inner.querySelector(".ekdp-kct-translated")?.textContent,
          `译(Completed.) ${tc.unclosed.slice("Completed. ".length)}`,
        );

        pre.textContent = tc.streamingClosed;
        handle.update({ open: true, streaming: true });
        now += 3_200;
        await wait(780);
        assert.equal(sentTexts.length, 1, `${tc.name}: closed backtick link was split at internal period during streaming`);

        pre.textContent = tc.final;
        handle.update({ open: true, streaming: false });
        await wait(80);
        assert.equal(sentTexts.length, 2, `${tc.name}: closing backtick link did not incrementally translate Tail.`);
        assert.deepEqual(sentTexts[1], ["Tail."]);
        assert.ok(
          !sentTexts.flat().some(text => text.includes("label") || text.includes("title") || text.includes("example.test")),
          `${tc.name}: closed backtick link content leaked into translation`,
        );
        assert.equal(inner.querySelector(".ekdp-kct-translated")?.textContent, tc.expectedFinal);
        handle.dispose?.();
      } finally {
        boot.cleanup();
      }
    }
  } finally {
    Date.now = realDateNow;
  }
});

test("non-streaming translation preserves links with backticks in URL, title, and code-span labels", async () => {
  const cases = [
    {
      name: "paired backticks in URL with quoted title",
      source: 'Intro. [label](https://example.test/foo`bar`baz "private title. more") Outro.',
      expectedSent: ["Intro.", "Outro."],
      expectedRendered: '译(Intro.) [label](https://example.test/foo`bar`baz "private title. more") 译(Outro.)',
    },
    {
      name: "single backtick in URL followed by inline code",
      source: "Intro. [Doc](https://example.test/a`b) Next `inline` end.",
      expectedSent: ["Intro.", "Next", "end."],
      expectedRendered: "译(Intro.) [Doc](https://example.test/a`b) 译(Next) `inline` 译(end.)",
    },
    {
      name: "single backtick in URL with quoted title",
      source: 'Intro. [label](https://example.test/foo`bar "private title. more") Outro.',
      expectedSent: ["Intro.", "Outro."],
      expectedRendered: '译(Intro.) [label](https://example.test/foo`bar "private title. more") 译(Outro.)',
    },
    {
      name: "backticks inside quoted title",
      source: 'Intro. [label](https://example.test/path "title `code` here. more") Outro.',
      expectedSent: ["Intro.", "Outro."],
      expectedRendered: '译(Intro.) [label](https://example.test/path "title `code` here. more") 译(Outro.)',
    },
    {
      name: "inline code containing closing bracket inside link label",
      source: "Intro. [`a]b` label](https://example.test/path) Outro.",
      expectedSent: ["Intro.", "Outro."],
      expectedRendered: "译(Intro.) [`a]b` label](https://example.test/path) 译(Outro.)",
    },
  ];

  for (const tc of cases) {
    const sentTexts: string[][] = [];
    const boot = await bootKct({
      translate: async (_url: string, init: any) => {
        const texts: string[] = JSON.parse(init.body);
        sentTexts.push(texts);
        return {
          ok: true,
          status: 200,
          body: JSON.stringify(texts.map(text => ({ translations: [{ text: `译(${text})` }] }))),
        };
      },
    });
    try {
      const { block, inner } = makeBlock(boot.bodyRoot, tc.source);
      const handle = boot.decorator!(block, { open: true, streaming: false });
      await wait(80);
      assert.equal(sentTexts.length, 1, `${tc.name}: expected exactly one translate request`);
      assert.deepEqual(sentTexts[0], tc.expectedSent, `${tc.name}: unexpected translated slices`);
      assert.equal(
        inner.querySelector(".ekdp-kct-translated")?.textContent,
        tc.expectedRendered,
        `${tc.name}: unexpected rendered translation`,
      );
      handle.dispose?.();
    } finally {
      boot.cleanup();
    }
  }

  const zhSentTexts: string[][] = [];
  const zhBoot = await bootKct({
    translate: async (_url: string, init: any) => {
      const texts: string[] = JSON.parse(init.body);
      zhSentTexts.push(texts);
      return {
        ok: true,
        status: 200,
        body: JSON.stringify(texts.map(text => ({ translations: [{ text: `译(${text})` }] }))),
      };
    },
  });
  try {
    const zhBacktickLink = '已完成 [label](https://example.test/foo`bar`baz "private title. more") 结束。';
    const zhBlock = makeBlock(zhBoot.bodyRoot, zhBacktickLink);
    const zhHandle = zhBoot.decorator!(zhBlock.block, { open: true, streaming: false });
    await wait(80);
    assert.equal(zhSentTexts.length, 0, "target-language text with backtick-URL link should not invoke translator");
    assert.equal(zhBlock.inner.querySelector(".ekdp-kct-translated"), null);
    assert.equal(zhBlock.pre.textContent, zhBacktickLink);
    zhHandle.dispose?.();
  } finally {
    zhBoot.cleanup();
  }
});

test("streaming protects links and images containing HTML comments, tags, or autolinks with brackets until closed", async () => {
  const realDateNow = Date.now;
  let now = 6_200_000;
  Date.now = () => now;
  const cases = [
    {
      name: "link with HTML comment containing closing bracket and unclosed title",
      unclosed: 'Completed. [foo <!-- ] --> bar](https://example.test/x "Private title. More',
      streamingClosed: 'Completed. [foo <!-- ] --> bar](https://example.test/x "Private title. More") waiting',
      final: 'Completed. [foo <!-- ] --> bar](https://example.test/x "Private title. More") Tail.',
      expectedFinal: '译(Completed.) [foo <!-- ] --> bar](https://example.test/x "Private title. More") 译(Tail.)',
    },
    {
      name: "image with URI autolink containing closing bracket and unclosed title",
      unclosed: 'Completed. ![alt <https://example.test/p?a=1]2> more](https://example.test/img.png "Private title. More',
      streamingClosed: 'Completed. ![alt <https://example.test/p?a=1]2> more](https://example.test/img.png "Private title. More") waiting',
      final: 'Completed. ![alt <https://example.test/p?a=1]2> more](https://example.test/img.png "Private title. More") Tail.',
      expectedFinal: '译(Completed.) ![alt <https://example.test/p?a=1]2> more](https://example.test/img.png "Private title. More") 译(Tail.)',
    },
    {
      name: "link with unclosed HTML comment containing period and bracket",
      unclosed: "Completed. [foo <!-- Private title. More ] still comment",
      streamingClosed: 'Completed. [foo <!-- Private title. More ] still comment --> bar](https://example.test/x "Private title. More") waiting',
      final: 'Completed. [foo <!-- Private title. More ] still comment --> bar](https://example.test/x "Private title. More") Tail.',
      expectedFinal: '译(Completed.) [foo <!-- Private title. More ] still comment --> bar](https://example.test/x "Private title. More") 译(Tail.)',
    },
    {
      name: "link with unclosed HTML open tag quoted attribute containing period and bracket",
      unclosed: 'Completed. [foo <span data-x="Private title. ] More',
      streamingClosed: 'Completed. [foo <span data-x="Private title. ] More">bar</span>](https://example.test/x "Private title. More") waiting',
      final: 'Completed. [foo <span data-x="Private title. ] More">bar</span>](https://example.test/x "Private title. More") Tail.',
      expectedFinal: '译(Completed.) [foo <span data-x="Private title. ] More">bar</span>](https://example.test/x "Private title. More") 译(Tail.)',
    },
  ];
  try {
    for (const tc of cases) {
      const sentTexts: string[][] = [];
      now += 100_000;
      const boot = await bootKct({
        translate: async (_url: string, init: any) => {
          const texts: string[] = JSON.parse(init.body);
          sentTexts.push(texts);
          return {
            ok: true,
            status: 200,
            body: JSON.stringify(texts.map(text => ({ translations: [{ text: `译(${text})` }] }))),
          };
        },
      });
      try {
        const { block, inner, pre } = makeBlock(boot.bodyRoot, tc.unclosed);
        const handle = boot.decorator!(block, { open: true, streaming: true });
        now += 3_200;
        await wait(780);
        assert.equal(sentTexts.length, 1, `${tc.name}: expected only Completed. to translate`);
        assert.deepEqual(sentTexts[0], ["Completed."]);
        assert.ok(
          !sentTexts.flat().some(text => text.includes("Private title") || text.includes("foo") || text.includes("example.test")),
          `${tc.name}: unclosed link with HTML/autolink leaked into translation`,
        );
        assert.equal(
          inner.querySelector(".ekdp-kct-translated")?.textContent,
          `译(Completed.) ${tc.unclosed.slice("Completed. ".length)}`,
        );

        pre.textContent = tc.streamingClosed;
        handle.update({ open: true, streaming: true });
        now += 3_200;
        await wait(780);
        assert.equal(sentTexts.length, 1, `${tc.name}: closed link was split at internal period during streaming`);

        pre.textContent = tc.final;
        handle.update({ open: true, streaming: false });
        await wait(80);
        assert.equal(sentTexts.length, 2, `${tc.name}: closing link did not incrementally translate Tail.`);
        assert.deepEqual(sentTexts[1], ["Tail."]);
        assert.ok(
          !sentTexts.flat().some(text => text.includes("Private title") || text.includes("foo") || text.includes("example.test")),
          `${tc.name}: closed link content leaked into translation`,
        );
        assert.equal(inner.querySelector(".ekdp-kct-translated")?.textContent, tc.expectedFinal);
        handle.dispose?.();
      } finally {
        boot.cleanup();
      }
    }
  } finally {
    Date.now = realDateNow;
  }
});

test("non-streaming translation preserves links/images with HTML constructs or autolinks and translates angle-bracket comparisons", async () => {
  const cases = [
    {
      name: "link with HTML comment containing closing bracket",
      source: 'Completed. [foo <!-- ] --> bar](https://example.test/x "Private title. More") Tail.',
      expectedSent: ["Completed.", "Tail."],
      expectedRendered: '译(Completed.) [foo <!-- ] --> bar](https://example.test/x "Private title. More") 译(Tail.)',
    },
    {
      name: "image alt text with URI autolink containing closing bracket",
      source: 'Intro. ![alt <https://example.test/p?a=1]2> more](https://example.test/img.png "Private title. More") Outro.',
      expectedSent: ["Intro.", "Outro."],
      expectedRendered: '译(Intro.) ![alt <https://example.test/p?a=1]2> more](https://example.test/img.png "Private title. More") 译(Outro.)',
    },
    {
      name: "link label with HTML open tag quoted ] / > / backtick attribute and closing tag",
      source: 'Intro. [foo <span data-x="a]b>c`d" data-y=\'e]f\'>bar</span> baz](https://example.test/x "Private title. More") Outro.',
      expectedSent: ["Intro.", "Outro."],
      expectedRendered: '译(Intro.) [foo <span data-x="a]b>c`d" data-y=\'e]f\'>bar</span> baz](https://example.test/x "Private title. More") 译(Outro.)',
    },
    {
      name: "link label with CDATA section containing closing bracket",
      source: 'Intro. [foo <![CDATA[a]b]]> bar](https://example.test/x "Private title. More") Outro.',
      expectedSent: ["Intro.", "Outro."],
      expectedRendered: '译(Intro.) [foo <![CDATA[a]b]]> bar](https://example.test/x "Private title. More") 译(Outro.)',
    },
    {
      name: "link label with processing instruction containing closing bracket",
      source: 'Intro. [foo <?pi ] ?> bar](https://example.test/x "Private title. More") Outro.',
      expectedSent: ["Intro.", "Outro."],
      expectedRendered: '译(Intro.) [foo <?pi ] ?> bar](https://example.test/x "Private title. More") 译(Outro.)',
    },
    {
      name: "link label with HTML declarations including closing bracket",
      source: 'Intro. [foo <!DOCTYPE html> <!ENTITY x "a]b"> bar](https://example.test/x "Private title. More") Outro.',
      expectedSent: ["Intro.", "Outro."],
      expectedRendered: '译(Intro.) [foo <!DOCTYPE html> <!ENTITY x "a]b"> bar](https://example.test/x "Private title. More") 译(Outro.)',
    },
    {
      name: "link label with CommonMark short HTML comments",
      source: 'Intro. [foo <!--> mid <!---> bar](https://example.test/x "Private title. More") Outro.',
      expectedSent: ["Intro.", "Outro."],
      expectedRendered: '译(Intro.) [foo <!--> mid <!---> bar](https://example.test/x "Private title. More") 译(Outro.)',
    },
    {
      name: "link label with email autolinks",
      source: 'Intro. [Contact <user@example.test> or </alt@example.test>](https://example.test/x "Private title. More") Outro.',
      expectedSent: ["Intro.", "Outro."],
      expectedRendered: '译(Intro.) [Contact <user@example.test> or </alt@example.test>](https://example.test/x "Private title. More") 译(Outro.)',
    },
    {
      name: "prose with spaced angle-bracket comparisons still translates normally",
      source: "When x < 5 and count < 10, check [docs](https://example.test/a).",
      expectedSent: ["When x < 5 and count < 10, check", "."],
      expectedRendered: "译(When x < 5 and count < 10, check) [docs](https://example.test/a)译(.)",
    },
    {
      name: "prose with spaced comparisons including > and unspaced a<b and c<d",
      source: "If a < b and c > d or a<b and c<d, check [docs](https://example.test/a).",
      expectedSent: ["If a < b and c > d or a<b and c<d, check", "."],
      expectedRendered: "译(If a < b and c > d or a<b and c<d, check) [docs](https://example.test/a)译(.)",
    },
    {
      name: "blank line invalidates unclosed HTML comment before subsequent link",
      source: "Intro <!-- unclosed\n\nNext [docs](https://example.test/a) Outro.",
      expectedSent: ["Intro <!-- unclosed", "Next", "Outro."],
      expectedRendered: "译(Intro <!-- unclosed)\n\n译(Next) [docs](https://example.test/a) 译(Outro.)",
    },
  ];

  for (const tc of cases) {
    const sentTexts: string[][] = [];
    const boot = await bootKct({
      translate: async (_url: string, init: any) => {
        const texts: string[] = JSON.parse(init.body);
        sentTexts.push(texts);
        return {
          ok: true,
          status: 200,
          body: JSON.stringify(texts.map(text => ({ translations: [{ text: `译(${text})` }] }))),
        };
      },
    });
    try {
      const { block, inner } = makeBlock(boot.bodyRoot, tc.source);
      const handle = boot.decorator!(block, { open: true, streaming: false });
      await wait(80);
      assert.equal(sentTexts.length, 1, `${tc.name}: expected exactly one translate request`);
      assert.deepEqual(sentTexts[0], tc.expectedSent, `${tc.name}: unexpected translated slices`);
      assert.equal(
        inner.querySelector(".ekdp-kct-translated")?.textContent,
        tc.expectedRendered,
        `${tc.name}: unexpected rendered translation`,
      );
      handle.dispose?.();
    } finally {
      boot.cleanup();
    }
  }
});
