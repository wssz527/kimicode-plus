/**
 * 打包冒烟测试：证明真实的 renderer.js 满足宿主 loader 的加载条件。
 *
 * 宿主用 blob URL import 入口（web/src/plugin-runtime/loader.ts），模块内不能
 * 有相对 import；上游又在模块求值期就读取 chrome.runtime.getURL 与
 * globalThis.rive。这三件事一旦破坏，插件在客户端里会静默不激活，所以用
 * 最小 DOM 桩在 Node 里把它们钉住。
 *
 * 面板的真实渲染与交互由隔离宿主的界面验收覆盖，这里不做 DOM 级断言：
 * 上游 createWidget 依赖 innerHTML 解析，桩实现无法忠实模拟。
 */
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

// 路径解析从 web/ 出发：npm test 从该包运行，与其他读文件的测试同一约定
const source = readFileSync("../plugins/monitor/renderer.js", "utf8");
const settingsSource = readFileSync("../plugins/monitor/settings.js", "utf8");
const url = `data:text/javascript;base64,${Buffer.from(source).toString("base64")}`;

const runtime = globalThis as unknown as Record<string, any>;

class StubElement {
  tagName: string;
  children: StubElement[] = [];
  style: Record<string, string> = {};
  dataset: Record<string, string> = {};
  attributes: Record<string, string> = {};
  className = "";
  textContent = "";
  classList = { add: () => {}, remove: () => {}, toggle: () => {}, contains: () => false };
  constructor(tag = "div") {
    this.tagName = tag.toUpperCase();
  }
  append(...nodes: StubElement[]) { this.children.push(...nodes); }
  appendChild(node: StubElement) { this.children.push(node); return node; }
  prepend(node: StubElement) { this.children.unshift(node); }
  insertBefore(node: StubElement) { this.children.unshift(node); return node; }
  replaceChildren(...nodes: StubElement[]) { this.children = nodes; }
  replaceWith() {}
  remove() {}
  setAttribute(key: string, value: unknown) { this.attributes[key] = String(value); }
  getAttribute(key: string) { return this.attributes[key] ?? null; }
  removeAttribute(key: string) { delete this.attributes[key]; }
  addEventListener() {}
  removeEventListener() {}
  querySelector() { return null; }
  querySelectorAll() { return []; }
  closest() { return null; }
  getClientRects() { return []; }
  getBoundingClientRect() { return { top: 0, left: 0, width: 0, height: 0, bottom: 0, right: 0 }; }
  get offsetHeight() { return 0; }
  get offsetWidth() { return 0; }
  get isConnected() { return true; }
  get firstElementChild() { return null; }
  get nextElementSibling() { return null; }
  get parentElement() { return null; }
  get childNodes() { return this.children; }
  attachShadow() { return new StubElement(); }
}

function installDOM() {
  runtime.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  runtime.MutationObserver = class { observe() {} disconnect() {} };
  runtime.document = {
    documentElement: new StubElement("html"),
    head: new StubElement("head"),
    body: new StubElement("body"),
    createElement: (tag: string) => new StubElement(tag),
    getElementById: () => null,
    querySelector: () => null,
    querySelectorAll: () => [],
    addEventListener() {},
    removeEventListener() {},
  };
  runtime.window = globalThis;
  runtime.requestAnimationFrame = (fn: () => void) => setTimeout(fn, 0);
  runtime.cancelAnimationFrame = clearTimeout;
  runtime.HTMLElement = StubElement;
  runtime.Element = StubElement;
  runtime.Node = StubElement;
  runtime.Event = class { type: string; constructor(type: string) { this.type = type; } };
  runtime.CustomEvent = class {
    type: string;
    detail: unknown;
    constructor(type: string, options?: { detail?: unknown }) {
      this.type = type;
      this.detail = options?.detail;
    }
  };
}

test("the bundled renderer imports the way the host loader imports it", async () => {
  installDOM();
  const module = await import(url);
  assert.equal(typeof module.activate, "function");
  // 产物必须是自包含模块：宿主从 blob URL 导入，相对 import 无法解析
  assert.doesNotMatch(source, /^\s*import\s[^;]*from\s*["']\./m);
  // settings 入口由 loader 以 renderSettings 导出加载（esbuild 会把
  // `function renderSettings` 收成尾部的 export 列表，所以查导出而不是签名）
  const settingsUrl = `data:text/javascript;base64,${Buffer.from(settingsSource).toString("base64")}`;
  const settingsModule = await import(settingsUrl);
  assert.equal(typeof settingsModule.renderSettings, "function");
});

test("importing the renderer has no side effect and activate installs the host shims", async () => {
  installDOM();
  delete runtime.chrome;
  await import(url);
  // 模块求值只注册入口：宿主替身与 localStorage 覆盖都发生在 activate() 里，
  // 这样停用插件能把页面完全还原，重复激活也不会叠加副作用。
  assert.equal(runtime.chrome as unknown, undefined, "importing the renderer installed the chrome shim");
  // Rive UMD 在 ESM 严格模式下要靠 globalThis 当 root，否则 root["rive"] 赋值抛 TypeError
  assert.equal(typeof runtime.rive?.Rive, "function");
  assert.equal(typeof runtime.rive?.RuntimeLoader, "function");
  // activate 之后替身必须在位，且资产是内联 data URL 而非扩展目录路径。
  const module = await import(url);
  const releases: Array<() => void> = [];
  const ctx = {
    plugin: { id: "kimicode-plus.monitor", version: "1.1.1", surface: "host" },
    storage: {
      list: async () => [],
      get: async () => undefined,
      set: async () => {},
      delete: async () => {},
    },
    core: { request: async () => null },
    events: { on: () => ({ dispose() {} }) },
    // The panel's real DOM assembly needs a browser (innerHTML parsing); this
    // smoke test only proves the entry activates and installs its shims.
    ui: { mount: () => ({ dispose() {} }) },
    log: () => {},
    onDispose: (fn: () => void) => { releases.push(fn); },
    activeSessionId: () => "",
  };
  const active = await module.activate(ctx);
  assert.equal(active?.status, "active");
  const shim = runtime.chrome as { runtime?: { getURL?: (path: string) => string } } | undefined;
  assert.equal(typeof shim?.runtime?.getURL, "function");
  assert.match(String(shim?.runtime?.getURL?.("rive/kimi_avatar_web-PnsTWI-X.riv")), /^data:application\/octet-stream;base64,/);
  assert.match(String(shim?.runtime?.getURL?.("rive/rive.wasm")), /^data:application\/wasm;base64,/);
  // activate starts real timers and observers; tear them down so node --test exits,
  // and prove dispose restores the page (the shim must be gone again).
  for (const release of releases.reverse()) {
    try {
      release();
    } catch {
      // teardown best effort in the stub environment
    }
  }
  assert.equal(runtime.chrome as unknown, undefined, "dispose left the chrome shim installed");
});

test("inlined assets stay inside the host asset limit", () => {
  const limit = 8 << 20;
  assert.ok(Buffer.byteLength(source, "utf8") <= limit, `renderer.js exceeds ${limit} bytes`);
  assert.ok(Buffer.byteLength(settingsSource, "utf8") <= limit, `settings.js exceeds ${limit} bytes`);
  // 体积主要来自内联的 wasm 与 riv；两个都必须真的在里面，而不是被悄悄省略
  for (const marker of ["data:application/wasm;base64,", "data:application/octet-stream;base64,"]) {
    assert.ok(source.includes(marker), `renderer.js is missing ${marker}`);
  }
});
