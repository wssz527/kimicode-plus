/**
 * Monitor 插件的纯逻辑与清单测试。
 *
 * 面板本体是从上游 bfjnbvf/kimi-code-monitor 移植的 panel-app，产物由 esbuild
 * 打成单文件。这里导入真实产物，测的就是随二进制发布的那份代码：
 * - 面板布局归一化（编辑模式三区拖拽后落盘的形状）；
 * - 数值口径（不把「未上报」写成 0）；
 * - 清单只申请它用到的能力，且不申请会话正文；
 * - 侧栏整理的开关默认关闭、且停用后不会残留。
 *
 * 面板的真实渲染、Rive 动画与拖拽交互由隔离宿主的界面验收覆盖。
 */
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

// 路径解析从 web/ 出发：npm test 从该包运行，与其他读文件的测试同一约定
const source = readFileSync("../plugins/monitor/renderer.js", "utf8");
const settingsSource = readFileSync("../plugins/monitor/settings.js", "utf8");
const manifest = JSON.parse(readFileSync("../plugins/monitor/plugin.json", "utf8"));

const runtime = globalThis as unknown as Record<string, any>;

// 产物在模块求值期就访问 DOM 与 rive，导入前先铺最小桩
runtime.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
runtime.MutationObserver = class { observe() {} disconnect() {} };
const stubElement = (): any => ({
  style: {}, dataset: {}, classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
  children: [] as any[], append(...nodes: any[]) { this.children.push(...nodes); }, appendChild(node: any) { this.children.push(node); return node; }, replaceChildren() {}, prepend() {}, insertBefore() {},
  addEventListener() {}, removeEventListener() {}, setAttribute() {}, getAttribute: () => null,
  removeAttribute() {}, remove() {}, querySelector: () => null, querySelectorAll: () => [],
  closest: () => null, getClientRects: () => [], getBoundingClientRect: () => ({ top: 0, left: 0, width: 0, height: 0, bottom: 0, right: 0 }),
  get offsetHeight() { return 0; }, get offsetWidth() { return 0; }, get isConnected() { return true; },
  get firstElementChild() { return null; }, get nextElementSibling() { return null; }, get parentElement() { return null; },
  get childNodes() { return []; }, attachShadow: () => stubElement(),
});
runtime.document = {
  documentElement: stubElement(),
  head: stubElement(),
  body: stubElement(),
  createElement: () => stubElement(),
  getElementById: () => null,
  querySelector: () => null,
  querySelectorAll: () => [],
  addEventListener() {},
  removeEventListener() {},
};
runtime.window = globalThis;
runtime.requestAnimationFrame = (fn: () => void) => setTimeout(fn, 0);
runtime.cancelAnimationFrame = clearTimeout;
runtime.Event = class { type: string; constructor(type: string) { this.type = type; } };

const moduleUrl = `data:text/javascript;base64,${Buffer.from(source).toString("base64")}`;

// 布局归一化是纯函数，直接从源码树的 vendor 副本导入（metrics.js 无 import、无 DOM）。
// 用 data URL 导入，避免 tsc 要求给 vendor 的 .js 写类型声明。
const metricsUrl = `data:text/javascript;base64,${Buffer.from(
  readFileSync("../plugins/monitor-src/vendor/metrics.js", "utf8"),
).toString("base64")}`;
const metricsModule = await import(metricsUrl);
const defaults = () => metricsModule.defaultWidgetConfig();

test("the plugin declares only the capabilities it uses", () => {
  assert.deepEqual(
    [...manifest.capabilities].sort(),
    ["metrics:read", "storage", "ui:settings", "ui:sidebar"].sort(),
  );
  // Monitor 读指标，从不读会话正文：申请 sessions:read 会拿到消息内容
  assert.ok(!manifest.capabilities.includes("sessions:read"));
  assert.equal(manifest.entry.renderer, "renderer.js");
  assert.equal(manifest.entry.settings, "settings.js");
  assert.ok(manifest.upstream?.repo === "bfjnbvf/kimi-code-monitor");
  assert.equal(manifest.upstream?.license, "MIT");
});

test("the panel asks for the activity scope, not the session transcript scope", async () => {
  // 事件只用于状态与用量计数；会话正文走 sessions:read，Monitor 没有也不该有。
  assert.match(source, /["']activity["']/);
  assert.doesNotMatch(source, /["']session:[^"']*["']/);
});

test("the shipped default layout keeps the pet and both quota bars in the mini zone", () => {
  // 布局是 JS 对象字面量（键无引号），不要当 JSON 解析：用 normalizeWidgetConfig
  // 的默认值验证形状，它由 metrics.js 的 defaultWidgetConfig 给出，两者必须一致。
  const layout = JSON.parse(JSON.stringify(defaults()));
  assert.deepEqual(layout.orderMini, ["pet", "quota5h", "quotaWeek"]);
  assert.equal(layout.modules.pet.show, "mini");
  assert.equal(layout.modules.external.show, "hidden");
  // 上游网页端默认开启侧栏改造；桌面装配层把它改成默认关闭，这一条在
  // main.js 的 PANEL_WIDGET_CONFIG 里覆盖，见下一个用例。
  assert.equal(layout.modules.pet.sidebarTidy, true);
});

test("the desktop default turns the sidebar tidy off", () => {
  // 装配层显式写出默认布局：只有侧栏整理与上游不同（桌面默认不整理）
  const body = /PANEL_WIDGET_CONFIG = \{([\s\S]*?)\n\};/.exec(source)?.[1] ?? "";
  assert.ok(body, "layout literal is in the bundle");
  assert.match(body, /sidebarTidy: false/);
  assert.match(body, /orderMini: \["pet", "quota5h", "quotaWeek"\]/);
});

test("a stored layout from the widget wins over the default one", async () => {
  const { normalizeWidgetConfig } = await import(metricsUrl);
  const normalized = normalizeWidgetConfig({
    version: 3,
    modules: {
      pet: { show: "full", span: 2, sidebarTidy: true },
      external: { show: "hidden", span: 2, hiddenAccounts: ["p1"] },
      agents: { show: "mini", span: 1 },
    },
    orderFull: ["pet", "external"],
    orderMini: [],
    orderHidden: ["agents"],
  });
  assert.equal(normalized.modules.pet.show, "full");
  assert.equal(normalized.modules.pet.sidebarTidy, true);
  assert.equal(normalized.modules.external.show, "hidden");
  assert.deepEqual(normalized.modules.external.hiddenAccounts, ["p1"]);
  // 子代理列表需要多行高度，只允许整宽
  assert.equal(normalized.modules.agents.span, 2);
});

test("a malformed layout falls back instead of rendering an empty panel", async () => {
  const { normalizeWidgetConfig } = await import(metricsUrl);
  for (const broken of [null, undefined, 42, "nope", { modules: "x" }]) {
    const normalized = normalizeWidgetConfig(broken);
    assert.ok(normalized.orderMini.length > 0, `orderMini survives ${JSON.stringify(broken)}`);
    assert.ok(normalized.orderFull.length > 0, `orderFull survives ${JSON.stringify(broken)}`);
    assert.equal(normalized.modules.pet.show, "mini");
  }
});

test("the settings entry reads and writes the same storage keys as the panel", () => {
  for (const key of ["kimi-statusbar.config", "kimi-statusbar.mini"]) {
    assert.ok(settingsSource.includes(key), `settings page uses ${key}`);
    assert.ok(source.includes(key), `panel uses ${key}`);
  }
  // 设置页与面板都不碰浏览器私有存储：所有读写都经 ctx/storage 通道
  for (const file of [settingsSource, source]) {
    assert.doesNotMatch(file, /(?:window\.)?sessionStorage/);
  }
});

test("the settings page writes before it commits and explains a failure", () => {
  const save = /async function saveConfig\(mutate\)[\s\S]*?\n  \}/.exec(settingsSource)?.[0] ?? "";
  assert.ok(save, "saveConfig is in the shipped settings file");
  const writeAt = save.indexOf("await ctx.storage.set");
  const commitAt = save.indexOf("config = next");
  assert.ok(writeAt >= 0 && commitAt > writeAt, "settings commit only after a successful write");
  assert.match(save, /仍在使用上次保存的设置/);
  // 设置页自带作用域样式，注入的 <style> 随面板一起移除
  assert.match(settingsSource, /createElement\("style"\)/);
  assert.match(settingsSource, /style\.remove\(\)/);
});

test("sidebar tidy is a persisted switch and is removed when the plugin stops", () => {
  // 开关本身：切类名 + 写同一个 storage 键
  assert.match(source, /documentElement\.classList\.toggle\("ksb-sidebar-tidy"/);
  // 停用时必须把类摘掉，否则用户卸插件后侧栏还是改造过的样子
  assert.match(source, /classList\.remove\("ksb-sidebar-tidy"\)/);
  // 安全闸沿用上游：头部出现 logo 与折叠按钮之外的可见内容时不改造
  assert.match(source, /ch-brand/);
  assert.match(source, /ch-collapse/);
});

test("the panel never logs or forwards a provider key", () => {
  // 余额链路的硬要求：密钥只出现在 Go 端的请求头里。
  // 渲染进程拿到的只有 accountTag（密钥散列尾号）。
  // 「Authorization」本身是 i18n 里的英文译文（"Authorization failed"），
  // 与密钥无关；要钉的是构造请求头的代码与密钥字段名。
  for (const marker of ["api_key", "apiKey", "Bearer ", "x-api-key"]) {
    assert.ok(!source.includes(marker), `bundle must not contain ${marker}`);
  }
  assert.doesNotMatch(source, /headers\s*[:=]\s*\{[^}]*[Aa]uthorization/);
  assert.match(source, /accountTag/);
});

test("deactivate clears timers, listeners, globals and rive instance", async () => {
  // 真实激活并停用插件：验证 1s 定时器、ResizeObserver、MutationObserver、Rive 实例全部回收
  const intervals = new Set<number>();
  const origSetInterval = runtime.setInterval;
  const origClearInterval = runtime.clearInterval;
  let nextId = 9000;
  runtime.setInterval = (fn: () => void, ms?: number) => {
    const id = nextId++;
    intervals.add(id);
    return id as unknown as ReturnType<typeof setInterval>;
  };
  runtime.clearInterval = (id: number) => {
    intervals.delete(id);
  };
  let riveCleanups = 0;
  let observerDisconnects = 0;
  let mutationDisconnects = 0;
  runtime.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() { observerDisconnects += 1; }
  };
  runtime.MutationObserver = class {
    observe() {}
    disconnect() { mutationDisconnects += 1; }
  };
  const origRive = runtime.rive;
  const origGetById = runtime.document.getElementById;
  const origCreateElement = runtime.document.createElement;
  try {
    const mod = await import(`${moduleUrl}#cleanup`);
    const regionStub = stubElement();
    const widgetStub = {
      ...stubElement(),
      querySelector: () => regionStub,
    };
    const petCanvasStub = stubElement();
    runtime.document.getElementById = (id: string) => {
      if (id === "ksb-widget") return widgetStub;
      if (id === "ksb-pet-canvas") return petCanvasStub;
      return null;
    };
    runtime.rive = {
      RuntimeLoader: { setWasmUrl() {}, setWasmFallbackUrl() {} },
      Fit: { Contain: "contain" },
      Alignment: { Center: "center" },
      Rive: class {
        cleanup() { riveCleanups += 1; }
        stop() {}
        play() {}
        startRendering() {}
        stopRendering() {}
      },
    };
    const disposers: Array<() => void> = [];
    const mountedEl = stubElement();
    runtime.document.createElement = () => {
      const node = stubElement();
      node.querySelector = () => regionStub;
      node.remove = () => {
        const index = mountedEl.children.indexOf(node);
        if (index >= 0) mountedEl.children.splice(index, 1);
      };
      return node;
    };
    const store = new Map<string, unknown>();
    const ctx = {
      log() {},
      storage: {
        list: async () => [...store.keys()],
        get: async (k: string) => store.get(k),
        set: async (k: string, v: unknown) => { store.set(k, v); },
        delete: async (k: string) => { store.delete(k); },
      },
      core: { request: async () => null },
      events: { on: () => ({ dispose() {} }) },
      ui: {
        mount(_slot: string, fn: (el: any) => () => void) {
          const unmount = fn(mountedEl);
          return { dispose: unmount };
        },
      },
      onDispose(fn: () => void) { disposers.push(fn); },
      activeSessionId: () => "",
    };
    await mod.activate(ctx);
    assert.ok(runtime.__kcm, "bridge installed");
    assert.ok(intervals.size > 0, "tickers running while active");

    for (const fn of disposers.reverse()) fn();
    assert.equal(intervals.size, 0, "all intervals cleared on dispose");
    assert.equal(runtime.__kcm, undefined, "bridge global removed on dispose");
    assert.equal(runtime.chrome, undefined, "chrome shim removed on dispose");
    assert.equal(riveCleanups, 1, "Rive instance cleaned up on dispose");
    assert.ok(observerDisconnects >= 1, "ResizeObserver disconnected on dispose");
    assert.ok(mutationDisconnects >= 1, "MutationObserver disconnected on dispose");
    assert.equal(mountedEl.children.length, 0, "mounted node removed on dispose");
  } finally {
    runtime.setInterval = origSetInterval;
    runtime.clearInterval = origClearInterval;
    runtime.rive = origRive;
    runtime.document.getElementById = origGetById;
    runtime.document.createElement = origCreateElement;
  }
});

test("external provider rows carry consoleUrl for click navigation", () => {
  assert.match(source, /openExternalUrl/);
  assert.match(source, /ksb-external-row/);
  assert.match(source, /data-url/);
  assert.match(source, /consoleUrl/);
});

test("historical cache.turns snapshots are marked as replays so idle turns do not enter thinking", () => {
  const stepHelper = /function stepEvent\([\s\S]*?\n\}/.exec(source)?.[0] ?? "";
  assert.ok(stepHelper, "stepEvent helper is present in bundle");
  assert.match(stepHelper, /kcmReplay:\s*true/);
});

test("datasource skips cache.sessions seeding and schedules cooldown follow-up after turn.ended", async () => {
  const dsSource = readFileSync("../plugins/monitor-src/src/datasource.js", "utf8");
  const dsUrl = `data:text/javascript;base64,${Buffer.from(dsSource).toString("base64")}`;
  const { startDatasource } = await import(dsUrl);

  const requestedActions: string[] = [];
  const pushed: any[] = [];
  const scheduledDelays: number[] = [];
  let activityHandler = null as ((payload: any) => void) | null;
  const origSetTimeout = globalThis.setTimeout;
  const origClearTimeout = globalThis.clearTimeout;
  const callbacks = new Map<number, () => void>();
  let nextTimerId = 100;

  globalThis.setTimeout = ((fn: () => void, ms?: number) => {
    const id = nextTimerId++;
    scheduledDelays.push(Number(ms || 0));
    callbacks.set(id, fn);
    return id as unknown as ReturnType<typeof setTimeout>;
  }) as typeof setTimeout;
  globalThis.clearTimeout = ((id?: ReturnType<typeof setTimeout>) => {
    if (id != null) callbacks.delete(Number(id));
  }) as typeof clearTimeout;

  try {
    const ctx = {
      log() {},
      core: {
        request: async (action: string) => {
          requestedActions.push(action);
          if (action === "cache.turns") {
            return [{ stepId: "s1", agent: "main", model: "r3-local", inputOther: 300, inputCacheRead: 900, inputCacheCreation: 0, output: 45, decodeMs: 500 }];
          }
          return null;
        },
      },
      events: {
        on: (scope: string, fn: (payload: any) => void) => {
          if (scope === "activity") activityHandler = fn;
          return { dispose() {} };
        },
      },
    };
    const ds = startDatasource(ctx, (msg: any) => pushed.push(msg), () => "sess-1", () => false);
    const unsub = ds.watchActivity();
    await ds.start();

    assert.ok(!requestedActions.includes("cache.sessions"), "must not seed recent-5 cache.sessions alongside cache.turns");
    assert.ok(!pushed.some((m) => m.type === "usageDaily" && m.sessions), "must not push sessions map that causes double counting");

    scheduledDelays.length = 0;
    activityHandler?.({ type: "turn.ended", session_id: "sess-1" });
    assert.ok(scheduledDelays.includes(600), "schedules 600ms follow-up refresh");
    assert.ok(scheduledDelays.some((ms) => ms > 5000), "schedules >5s follow-up past cache.Store 5s scanInterval");

    unsub?.dispose?.();
    ds.dispose();
  } finally {
    globalThis.setTimeout = origSetTimeout;
    globalThis.clearTimeout = origClearTimeout;
  }
});
