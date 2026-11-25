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
const monitorSource = (path: string) => readFileSync(`../plugins/monitor-src/${path}`, "utf8");
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
  const body = /PANEL_WIDGET_CONFIG = \{([\s\S]*?)\n\};/.exec(monitorSource("src/main.js"))?.[1] ?? "";
  assert.ok(body, "layout literal is in the renderer entry");
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
  const settingsEntry = monitorSource("src/settings.js");
  const save = /async function saveConfig\(mutate\)[\s\S]*?\n  \}/.exec(settingsEntry)?.[0] ?? "";
  assert.ok(save, "saveConfig is in the settings entry");
  const writeAt = save.indexOf("await ctx.storage.set");
  const commitAt = save.indexOf("config = next");
  assert.ok(writeAt >= 0 && commitAt > writeAt, "settings commit only after a successful write");
  assert.match(save, /仍在使用上次保存的设置/);
  // 设置页自带作用域样式，注入的 <style> 随面板一起移除
  assert.match(settingsEntry, /createElement\("style"\)/);
  assert.match(settingsEntry, /style\.remove\(\)/);
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
  assert.match(monitorSource("vendor/content/widget-structure.js"), /openExternalUrl\(target\.dataset\.url\)/);
  assert.match(source, /ksb-external-row/);
  assert.match(source, /data-url/);
  assert.match(source, /consoleUrl/);
});

test("historical cache.turns snapshots are marked as replays so idle turns do not enter thinking", () => {
  const stepHelper = /function stepEvent\([\s\S]*?\n\}/.exec(monitorSource("src/datasource.js"))?.[0] ?? "";
  assert.ok(stepHelper, "stepEvent helper is present in the data source");
  assert.match(stepHelper, /kcmReplay:\s*true/);
  assert.match(stepHelper, /kcmSkipDaily:\s*true/);
});

test("host localStorage shim scopes only monitor keys and passes official keys through to native storage", async () => {
  const hostMap = new Map<string, string>([
    ["kimi-locale", "en"],
    ["kimi-web.input-history", "initial"],
  ]);
  const nativeStorage = {
    getItem: (k: string) => (hostMap.has(k) ? hostMap.get(k)! : null),
    setItem: (k: string, v: string) => { hostMap.set(k, String(v)); },
    removeItem: (k: string) => { hostMap.delete(k); },
    key: (i: number) => ([...hostMap.keys()][i] ?? null),
    get length() { return hostMap.size; },
    clear: () => { hostMap.clear(); },
  };
  const prevDesc = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  Object.defineProperty(globalThis, "localStorage", {
    value: nativeStorage,
    configurable: true,
    writable: true,
  });

  const origRive = runtime.rive;
  const origGetById = runtime.document.getElementById;
  const origCreateElement = runtime.document.createElement;
  const regionStub = stubElement();
  const widgetStub = { ...stubElement(), querySelector: () => regionStub };
  runtime.document.getElementById = (id: string) => (id === "ksb-widget" ? widgetStub : null);
  runtime.document.createElement = () => {
    const node = stubElement();
    node.querySelector = () => regionStub;
    return node;
  };
  runtime.rive = {
    RuntimeLoader: { setWasmUrl() {}, setWasmFallbackUrl() {} },
    Fit: { Contain: "contain" },
    Alignment: { Center: "center" },
    Rive: class { cleanup() {} stop() {} play() {} startRendering() {} stopRendering() {} },
  };

  const pluginStore = new Map<string, unknown>([
    ["kimi-statusbar.mini", "0"],
    ["kimi-web.input-history", "legacy-polluted-entry"],
    ["kimi-active-workspace", "wm_legacy"],
  ]);
  const writtenKeys: string[] = [];
  const deletedKeys: string[] = [];
  const disposers: Array<() => void> = [];
  const mod = await import(moduleUrl);
  try {
    await mod.activate({
      log() {},
      storage: {
        list: async () => [...pluginStore.keys()],
        get: async (k: string) => pluginStore.get(k),
        set: async (k: string, v: unknown) => { writtenKeys.push(k); pluginStore.set(k, v); },
        delete: async (k: string) => { deletedKeys.push(k); pluginStore.delete(k); },
      },
      core: { request: async () => null },
      events: { on: () => ({ dispose() {} }) },
      ui: {
        mount(_slot: string, fn: (el: any) => () => void) {
          const el = stubElement();
          const unmount = fn(el);
          return { dispose: unmount };
        },
      },
      onDispose(fn: () => void) { disposers.push(fn); },
      activeSessionId: () => "sess-live",
    });

    assert.equal(runtime.localStorage.getItem("kimi-locale"), "en");
    const bigPayload = "x".repeat(40_000);
    runtime.localStorage.setItem("kimi-web.input-history", bigPayload);
    runtime.localStorage.setItem("kimi-statusbar.mini", "1");
    runtime.__kcm?.push({
      v: 1,
      type: "event",
      sessionId: "sess-live",
      event: {
        type: "turn.step.completed",
        session_id: "sess-live",
        agent_id: "main",
        seq: 1,
        kcmReplay: true,
        kcmSkipDaily: true,
        payload: {
          stepId: "step-skip-daily",
          model: "m1",
          usage: { inputOther: 500, inputCacheRead: 1000, inputCacheCreation: 0, output: 80 },
        },
      },
    });
    await new Promise((r) => setTimeout(r, 60));

    assert.equal(hostMap.get("kimi-web.input-history"), bigPayload, "official keys write straight to native storage");
    assert.ok(!writtenKeys.includes("kimi-web.input-history"), "official keys never write into ctx.storage");
    assert.ok(writtenKeys.includes("kimi-statusbar.mini"), "monitor keys persist through ctx.storage");
    assert.ok(!writtenKeys.includes("kcm.daily.v2"), "kcmSkipDaily replay must not accumulate into kcm.daily.v2");
    assert.ok(deletedKeys.includes("kimi-web.input-history"), "legacy polluted host keys are pruned from ctx.storage");
    assert.ok(deletedKeys.includes("kimi-active-workspace"), "legacy polluted workspace key is pruned from ctx.storage");
  } finally {
    for (const fn of disposers.reverse()) fn();
    runtime.rive = origRive;
    runtime.document.getElementById = origGetById;
    runtime.document.createElement = origCreateElement;
    if (prevDesc) Object.defineProperty(globalThis, "localStorage", prevDesc);
    else delete runtime.localStorage;
  }
});

test("replayed cache.turns do not accumulate into kcm.daily.v2 and 30d history is authoritative", async () => {
  const dsSource = readFileSync("../plugins/monitor-src/src/datasource.js", "utf8");
  const dsUrl = `data:text/javascript;base64,${Buffer.from(dsSource).toString("base64")}`;
  const { startDatasource } = await import(dsUrl);

  const now = new Date();
  const pad = (v: number) => String(v).padStart(2, "0");
  const localToday = `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
  const utcHourBucket = `${now.getUTCFullYear()}-${pad(now.getUTCMonth() + 1)}-${pad(now.getUTCDate())}T${pad(now.getUTCHours())}:00`;

  const pushed: any[] = [];
  const ctx = {
    log() {},
    core: {
      request: async (action: string, params?: any) => {
        if (action === "cache.history" && params?.range === "24h") {
          return {
            range: "24h",
            unit: "hour",
            points: [{ bucket: utcHourBucket, totals: { inputOther: 120, inputCacheRead: 280, inputCacheCreation: 0, output: 60 } }],
          };
        }
        if (action === "cache.history" && params?.range === "30d") {
          return {
            range: "30d",
            unit: "day",
            points: [{ bucket: "2026-11-20", totals: { inputOther: 100, inputCacheRead: 200, inputCacheCreation: 0, output: 50 } }],
          };
        }
        if (action === "cache.turns") {
          return [{ stepId: "s-hist", agent: "main", model: "m1", inputOther: 500, inputCacheRead: 1000, inputCacheCreation: 0, output: 80, decodeMs: 400 }];
        }
        return null;
      },
    },
    events: { on: () => ({ dispose() {} }) },
  };
  const ds = startDatasource(ctx, (msg: any) => pushed.push(msg), () => "sess-replay", () => false);
  try {
    await ds.start();
    const dailyMsg = pushed.find((m) => m.type === "usageDaily");
    assert.equal(dailyMsg?.authoritative, true, "30d cache.history marks daily buckets authoritative");
    assert.equal(dailyMsg?.daily?.["2026-11-20"]?.input, 300);
    assert.equal(dailyMsg?.daily?.[localToday]?.input, 400, "local-today bucket is derived from 24h UTC hours");
    assert.equal(dailyMsg?.daily?.[localToday]?.output, 60);
    const stepMsg = pushed.find((m) => m.type === "event" && m.event?.type === "turn.step.completed");
    assert.equal(stepMsg?.event?.kcmReplay, true);
    assert.equal(stepMsg?.event?.kcmSkipDaily, true);
  } finally {
    ds.dispose();
  }
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

test("quota.balances flows through datasource -> bridge -> render.js into DOM (5h/week, wallet balance, external rows & mini polling)", async () => {
  type MiniNode = {
    tag: string;
    attrs: Record<string, string>;
    children: MiniNode[];
    parent: MiniNode | null;
    style: Record<string, string>;
    dataset: Record<string, string>;
    hidden: boolean;
    id: string;
    className: string;
    classList: {
      add(...cls: string[]): void;
      remove(...cls: string[]): void;
      toggle(cls: string, force?: boolean): boolean;
      contains(cls: string): boolean;
    };
    innerHTML: string;
    textContent: string;
    append(...nodes: any[]): void;
    appendChild(node: any): any;
    replaceChildren(...nodes: any[]): void;
    prepend(...nodes: any[]): void;
    insertBefore(node: any, ref?: any): any;
    remove(): void;
    addEventListener(): void;
    removeEventListener(): void;
    setAttribute(k: string, v: string): void;
    getAttribute(k: string): string | null;
    removeAttribute(k: string): void;
    querySelector(sel: string): MiniNode | null;
    querySelectorAll(sel: string): MiniNode[];
    closest(sel: string): MiniNode | null;
    getClientRects(): any[];
    getBoundingClientRect(): any;
    offsetHeight: number;
    offsetWidth: number;
    isConnected: boolean;
    firstElementChild: MiniNode | null;
    nextElementSibling: MiniNode | null;
    parentElement: MiniNode | null;
    childNodes: MiniNode[];
    attachShadow(): MiniNode;
  };

  const makeNode = (tag = "div", attrs: Record<string, string> = {}): MiniNode => {
    let htmlText = "";
    let rawText = "";
    const node: MiniNode = {
      tag: tag.toLowerCase(),
      attrs: { ...attrs },
      children: [],
      parent: null,
      style: {},
      dataset: {},
      hidden: false,
      get id() { return node.attrs.id || ""; },
      set id(v: string) { node.attrs.id = v; },
      get className() { return node.attrs.class || ""; },
      set className(v: string) { node.attrs.class = String(v); },
      classList: {
        add(...cls: string[]) {
          const set = new Set((node.attrs.class || "").split(/\s+/).filter(Boolean));
          for (const c of cls) if (c) set.add(c);
          node.attrs.class = [...set].join(" ");
        },
        remove(...cls: string[]) {
          const rm = new Set(cls);
          node.attrs.class = (node.attrs.class || "").split(/\s+/).filter((c) => c && !rm.has(c)).join(" ");
        },
        toggle(cls: string, force?: boolean) {
          const has = node.classList.contains(cls);
          const next = force === undefined ? !has : Boolean(force);
          if (next) node.classList.add(cls);
          else node.classList.remove(cls);
          return next;
        },
        contains(cls: string) {
          return (node.attrs.class || "").split(/\s+/).includes(cls);
        },
      },
      get innerHTML() { return htmlText; },
      set innerHTML(markup: string) {
        htmlText = String(markup ?? "");
        node.children = parseHtmlFragment(htmlText, node);
      },
      get textContent() {
        if (node.children.length === 0) return rawText;
        return rawText + node.children.map((c) => c.textContent).join("");
      },
      set textContent(v: string) {
        rawText = String(v ?? "");
        node.children = [];
        htmlText = rawText;
      },
      append(...nodes: any[]) {
        for (const child of nodes) {
          if (child && typeof child === "object" && "tag" in child) {
            child.parent = node;
            node.children.push(child);
          }
        }
      },
      appendChild(child: any) { node.append(child); return child; },
      replaceChildren(...nodes: any[]) { node.children = []; node.append(...nodes); },
      prepend(...nodes: any[]) {
        for (const child of [...nodes].reverse()) {
          if (child && typeof child === "object" && "tag" in child) {
            child.parent = node;
            node.children.unshift(child);
          }
        }
      },
      insertBefore(child: any) { node.append(child); return child; },
      remove() {
        if (!node.parent) return;
        const idx = node.parent.children.indexOf(node);
        if (idx >= 0) node.parent.children.splice(idx, 1);
        node.parent = null;
      },
      addEventListener() {},
      removeEventListener() {},
      setAttribute(k: string, v: string) {
        node.attrs[k] = String(v);
        if (k.startsWith("data-")) node.dataset[k.slice(5)] = String(v);
      },
      getAttribute(k: string) { return k in node.attrs ? node.attrs[k] : null; },
      removeAttribute(k: string) { delete node.attrs[k]; },
      querySelector(sel: string) { return node.querySelectorAll(sel)[0] ?? null; },
      querySelectorAll(sel: string) {
        const parts = sel.split(",").map((s) => s.trim()).filter(Boolean);
        const matchesSingle = (el: MiniNode, single: string) => {
          const idMatch = single.match(/#([A-Za-z0-9_-]+)/);
          if (idMatch && el.id !== idMatch[1]) return false;
          for (const cls of single.matchAll(/\.([A-Za-z0-9_-]+)/g)) {
            if (!el.classList.contains(cls[1])) return false;
          }
          for (const attr of single.matchAll(/\[([^\]=]+)(?:="([^"]*)")?\]/g)) {
            if (!(attr[1] in el.attrs)) return false;
            if (attr[2] !== undefined && el.attrs[attr[1]] !== attr[2]) return false;
          }
          return true;
        };
        const out: MiniNode[] = [];
        const walk = (cur: MiniNode) => {
          for (const child of cur.children) {
            if (parts.some((p) => matchesSingle(child, p))) out.push(child);
            walk(child);
          }
        };
        walk(node);
        return out;
      },
      closest(sel: string) {
        let cur: MiniNode | null = node;
        while (cur) {
          if (sel.startsWith(".") && cur.classList.contains(sel.slice(1))) return cur;
          if (sel.startsWith("#") && cur.id === sel.slice(1)) return cur;
          cur = cur.parent;
        }
        return null;
      },
      getClientRects: () => [],
      getBoundingClientRect: () => ({ top: 0, left: 0, width: 0, height: 0, bottom: 0, right: 0 }),
      offsetHeight: 0,
      offsetWidth: 0,
      isConnected: true,
      get firstElementChild() { return node.children[0] ?? null; },
      get nextElementSibling() { return null; },
      get parentElement() { return node.parent; },
      get childNodes() { return node.children; },
      attachShadow: () => makeNode("shadow"),
    };
    for (const [k, v] of Object.entries(attrs)) {
      if (k.startsWith("data-")) node.dataset[k.slice(5)] = v;
    }
    return node;
  };

  const parseHtmlFragment = (markup: string, parent: MiniNode): MiniNode[] => {
    const roots: MiniNode[] = [];
    const stack: MiniNode[] = [parent];
    const tokenRe = /<(\/?)([a-zA-Z0-9-]+)([^>]*)>|([^<]+)/g;
    for (const m of markup.matchAll(tokenRe)) {
      if (m[4] !== undefined) {
        const text = m[4].trim();
        if (text && stack.length > 1) stack[stack.length - 1].textContent += m[4];
        continue;
      }
      const closing = m[1] === "/";
      const tag = m[2].toLowerCase();
      const attrRaw = m[3] || "";
      if (closing) {
        while (stack.length > 1) {
          const top = stack.pop()!;
          if (top.tag === tag) break;
        }
        continue;
      }
      const attrs: Record<string, string> = {};
      for (const am of attrRaw.matchAll(/([a-zA-Z0-9_:-]+)(?:="([^"]*)")?/g)) {
        if (am[1] === "/") continue;
        attrs[am[1]] = am[2] ?? "";
      }
      const el = makeNode(tag, attrs);
      const cur = stack[stack.length - 1];
      el.parent = cur;
      if (cur === parent) roots.push(el);
      else cur.children.push(el);
      if (!attrRaw.trim().endsWith("/") && tag !== "path" && tag !== "br" && tag !== "img") {
        stack.push(el);
      }
    }
    return roots;
  };

  const origGetById = runtime.document.getElementById;
  const origQuerySelector = runtime.document.querySelector;
  const origQuerySelectorAll = runtime.document.querySelectorAll;
  const origCreateElement = runtime.document.createElement;
  const origRive = runtime.rive;
  const origHTMLCanvasElement = runtime.HTMLCanvasElement;
  const origSetInterval = runtime.setInterval;
  const origClearInterval = runtime.clearInterval;
  const intervals = new Set<number>();
  let nextInterval = 1;

  const docRoot = makeNode("div");
  runtime.document.createElement = (tag: string) => makeNode(tag);
  runtime.document.getElementById = (id: string) => docRoot.querySelector(`#${id}`);
  runtime.document.querySelector = (sel: string) => docRoot.querySelector(sel);
  runtime.document.querySelectorAll = (sel: string) => docRoot.querySelectorAll(sel);
  runtime.setInterval = () => nextInterval++;
  runtime.clearInterval = (id: number) => { intervals.delete(id); };
  runtime.HTMLCanvasElement = class {};
  runtime.rive = {
    RuntimeLoader: { setWasmUrl() {}, setWasmFallbackUrl() {} },
    Fit: { Contain: "contain" },
    Alignment: { Center: "center" },
    Rive: class {
      cleanup() {}
      stop() {}
      play() {}
      startRendering() {}
      stopRendering() {}
    },
  };

  const reset5h = new Date(Date.now() + 2 * 3600_000 + 30 * 60_000).toISOString();
  const resetWeek = new Date(Date.now() + 3 * 24 * 3600_000 + 5 * 3600_000).toISOString();
  const observedAt = "2026-11-18T08:00:00Z";
  const quotaPayload = {
    schemaVersion: 1,
    cards: [
      {
        providerId: "ds-1",
        providerName: "DeepSeek",
        accountTag: "a1b2",
        status: "ok",
        observedAt,
        consoleUrl: "https://platform.deepseek.com/top_up",
        records: [{ kind: "api_balance", unit: "CNY", remaining: 20.5 }],
      },
      {
        providerId: "bm-1",
        providerName: "智谱 BigModel",
        accountTag: "c3d4",
        status: "ok",
        observedAt,
        consoleUrl: "https://bigmodel.cn/usercenter/apikeys",
        records: [{ kind: "coding_plan", windowLabel: "5h", used: 35, limit: 100 }],
      },
    ],
    kimiCode: {
      status: "ok",
      observedAt,
      windows: [
        { key: "limit_5h", usedRatio: 0.44, resetAt: reset5h },
        { key: "limit_7d", usedRatio: 0.25, resetAt: resetWeek },
      ],
      wallet: { balanceCents: 1975, totalCents: 2500, currency: "CNY" },
    },
  };

  try {
    const mod = await import(`${moduleUrl}#e2e-quota`);

    // 1) Full mode: pet.stat=balance + external=full -> renders 5h/week, ¥19.75, DeepSeek & BigModel rows with data-url
    const mountFull = makeNode("div");
    docRoot.replaceChildren(mountFull);
    const fullStore = new Map<string, unknown>([
      ["kimi-statusbar.config", {
        ...defaults(),
        modules: {
          ...defaults().modules,
          pet: { ...defaults().modules.pet, stat: "balance" },
          external: { ...defaults().modules.external, show: "full" },
        },
      }],
    ]);
    const fullDisposers: Array<() => void> = [];
    const fullActions: string[] = [];
    try {
      await mod.activate({
        log() {},
        storage: {
          list: async () => [...fullStore.keys()],
          get: async (k: string) => fullStore.get(k),
          set: async (k: string, v: unknown) => { fullStore.set(k, v); },
          delete: async (k: string) => { fullStore.delete(k); },
        },
        core: {
          request: async (action: string) => {
            fullActions.push(action);
            if (action === "quota.balances") return quotaPayload;
            return null;
          },
        },
        events: { on: () => ({ dispose() {} }) },
        ui: {
          mount(_slot: string, fn: (el: any) => () => void) {
            const unmount = fn(mountFull);
            return { dispose: unmount };
          },
        },
        onDispose(fn: () => void) { fullDisposers.push(fn); },
        activeSessionId: () => "",
      });
      await new Promise((r) => setTimeout(r, 30));

      assert.ok(fullActions.includes("quota.balances"), "full mode polls quota.balances");
      assert.equal(docRoot.querySelector("#ksb-5h-pct")?.textContent, "44%");
      assert.equal(docRoot.querySelector("#ksb-week-pct")?.textContent, "25%");
      assert.match(docRoot.querySelector("#ksb-5h-reset")?.textContent || "", /\d+h/);
      assert.match(docRoot.querySelector("#ksb-week-reset")?.textContent || "", /\d+d/);
      assert.equal(docRoot.querySelector("#ksb-pet-total-text")?.textContent, "¥19.75");

      const extHtml = docRoot.querySelector("#ksb-external-list")?.innerHTML || "";
      assert.match(extHtml, /DeepSeek/);
      assert.match(extHtml, /¥20\.50/);
      assert.match(extHtml, /data-url="https:\/\/platform\.deepseek\.com\/top_up"/);
      assert.match(extHtml, /智谱 BigModel/);
      assert.match(extHtml, /35\.0%/);
      assert.match(extHtml, /data-url="https:\/\/bigmodel\.cn\/usercenter\/apikeys"/);
    } finally {
      for (const fn of fullDisposers.reverse()) fn();
    }

    // 2) Mini mode with external.show="full" and pet/header stat="daily": must NOT poll quota.balances
    const mountMini = makeNode("div");
    docRoot.replaceChildren(mountMini);
    const miniStore = new Map<string, unknown>([
      ["kimi-statusbar.mini", "1"],
      ["kimi-statusbar.config", {
        ...defaults(),
        modules: {
          ...defaults().modules,
          header: { ...defaults().modules.header, stat: "daily" },
          pet: { ...defaults().modules.pet, stat: "daily" },
          external: { ...defaults().modules.external, show: "full" },
        },
      }],
    ]);
    const miniDisposers: Array<() => void> = [];
    const miniActions: string[] = [];
    try {
      await mod.activate({
        log() {},
        storage: {
          list: async () => [...miniStore.keys()],
          get: async (k: string) => miniStore.get(k),
          set: async (k: string, v: unknown) => { miniStore.set(k, v); },
          delete: async (k: string) => { miniStore.delete(k); },
        },
        core: {
          request: async (action: string) => {
            miniActions.push(action);
            return null;
          },
        },
        events: { on: () => ({ dispose() {} }) },
        ui: {
          mount(_slot: string, fn: (el: any) => () => void) {
            const unmount = fn(mountMini);
            return { dispose: unmount };
          },
        },
        onDispose(fn: () => void) { miniDisposers.push(fn); },
        activeSessionId: () => "",
      });
      await new Promise((r) => setTimeout(r, 30));
      assert.ok(!miniActions.includes("quota.balances"), "mini mode with external hidden from mini must not poll quota.balances");
    } finally {
      for (const fn of miniDisposers.reverse()) fn();
    }
  } finally {
    runtime.document.getElementById = origGetById;
    runtime.document.querySelector = origQuerySelector;
    runtime.document.querySelectorAll = origQuerySelectorAll;
    runtime.document.createElement = origCreateElement;
    runtime.rive = origRive;
    runtime.HTMLCanvasElement = origHTMLCanvasElement;
    runtime.setInterval = origSetInterval;
    runtime.clearInterval = origClearInterval;
  }
});

test("an account switch drops quota and usage responses that were already in flight", async () => {
  const dsSource = readFileSync("../plugins/monitor-src/src/datasource.js", "utf8");
  const dsUrl = `data:text/javascript;base64,${Buffer.from(dsSource).toString("base64")}`;
  const { startDatasource } = await import(dsUrl);

  const pushed: any[] = [];
  let account = "first";
  let releaseFirst: (() => void) | null = null;
  const firstGate = new Promise<void>((resolve) => { releaseFirst = () => resolve(); });
  let requestCount = 0;

  const summaryFor = (usedRatio: number) => ({
    quota: {
      state: "AVAILABLE",
      value: {
        quota: {
          usages: {
            "5h": { usedRatio, resetAt: account === "first" ? "2026-11-24T10:00:00Z" : "2026-11-24T22:00:00Z" },
            week: { usedRatio, resetAt: "" },
          },
        },
      },
    },
  });

  const ctx = {
    log() {},
    core: {
      request: async (action: string) => {
        requestCount += 1;
        if (action === "cache.summary") {
          // The first account's page is held open until the switch has happened.
          const isFirst = account === "first";
          if (isFirst) await firstGate;
          return summaryFor(isFirst ? 0.9 : 0.1);
        }
        return null;
      },
    },
    events: { on: () => ({ dispose() {} }) },
  };

  const ds = startDatasource(ctx, (msg: any) => pushed.push(msg), () => "sess-1", () => false);
  try {
    const pending = ds.start();
    // The switch runs while the first page is still pending; accountGeneration
    // moves on, so that page must not reach the panel.
    account = "second";
    const afterSwitch = ds.resetForAccountSwitch();
    (releaseFirst as (() => void) | null)?.();
    await pending;
    await afterSwitch;

    const quotas = pushed.filter((m) => m.type === "quota");
    assert.equal(quotas.length, 1, `expected only the new account's quota, got ${JSON.stringify(quotas)}`);
    assert.equal(quotas[0]?.quota?.limit5h?.usedRatio, 0.1, "the previous account's quota leaked into the panel");
    assert.equal(quotas[0]?.quota?.limit5h?.resetAt, "2026-11-24T22:00:00Z", "the quota shown must belong to the new account");
    assert.ok(requestCount >= 2, "the switch must trigger a fresh read instead of reusing the old page");
  } finally {
    ds.dispose();
  }
});
