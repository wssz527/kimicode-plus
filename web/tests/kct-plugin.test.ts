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
