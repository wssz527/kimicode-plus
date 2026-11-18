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

test("the host shims and the Rive runtime exist before any panel code runs", async () => {
  installDOM();
  await import(url);
  // 上游在模块求值期调用 chrome.runtime.getURL，并把结果存成常量：
  // 替身必须已经在位，资产必须是内联 data URL 而不是扩展目录路径。
  assert.equal(typeof runtime.chrome?.runtime?.getURL, "function");
  assert.match(runtime.chrome.runtime.getURL("rive/kimi_avatar_web-PnsTWI-X.riv"), /^data:application\/octet-stream;base64,/);
  assert.match(runtime.chrome.runtime.getURL("rive/rive.wasm"), /^data:application\/wasm;base64,/);
  // Rive UMD 在 ESM 严格模式下要靠 globalThis 当 root，否则 root["rive"] 赋值抛 TypeError
  assert.equal(typeof runtime.rive?.Rive, "function");
  assert.equal(typeof runtime.rive?.RuntimeLoader, "function");
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
