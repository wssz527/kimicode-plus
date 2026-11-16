// Monitor 插件的纯函数与设置持久化测试。
//
// 插件模块必须是单文件（loader 用 blob URL 导入，相对 import 无法解析），所以
// 这里读取真实文件、用 data URL 导入，测的就是随二进制发布的那份代码，而不是
// 一份复制品。Web API（document/HTML 元素）不参与纯函数，可以在 Node 里直接测。
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

interface MonitorModule {
  DEFAULT_SETTINGS: { mini: boolean; pet: boolean; range: string };
  normalizeSettings(raw: unknown): { mini: boolean; pet: boolean; range: string };
  mergeSettings(current: unknown, patch: unknown): { mini: boolean; pet: boolean; range: string };
  hitRateText(totals: unknown): string;
  tokenText(value: unknown): string;
  speedText(speed: unknown): string;
  idleText(minutes: unknown): string;
  quotaView(quota: unknown, now?: number): {
    state: string;
    source: string;
    note: string;
    known: boolean;
    windows: { key: string; label: string; percent: number | null; reset: { short: string; long: string } | null }[];
  };
  metricCards(summary: unknown): { key: string; label: string; value: string; known: boolean; note?: string }[];
  historyModel(history: unknown): {
    unit: string;
    empty: boolean;
    bars: { bucket: string; total: number; empty: boolean; height: number; hitRate: string }[];
    hitRate: string;
    read: string;
    miss: string;
    output: string;
  };
  trendModel(history: unknown): { runs: number[][][]; samples: number; drawable: boolean };
  subagentModel(sessions: unknown): { rows: { id: string; title: string; model: string; open: number; blocked: boolean; idle: string; hitRate: string }[]; openTotal: number; waiting: number };
  receiptModel(receipts: unknown): { key: string; kind: string; state: string; session: string; note: string; at: string }[];
  clockText(date: unknown): string;
  createRefresher(options: Record<string, unknown>): {
    trigger(): void;
    refresh(): void;
    resume(): void;
    start(): void;
    dispose(): void;
    status(): { disposed: boolean; generation: number; scheduled: boolean; deferred: boolean };
  };
}

// Paths resolve from web/ because that is the package the test runner runs in,
// the same convention the other file-reading tests use.
const rendererSource = readFileSync("../plugins/monitor/renderer.js", "utf8");
const settingsSource = readFileSync("../plugins/monitor/settings.js", "utf8");
const monitor = (await import(`data:text/javascript;base64,${Buffer.from(rendererSource).toString("base64")}`)) as MonitorModule;
const {
  DEFAULT_SETTINGS,
  clockText,
  createRefresher,
  historyModel,
  hitRateText,
  idleText,
  mergeSettings,
  metricCards,
  normalizeSettings,
  quotaView,
  receiptModel,
  speedText,
  subagentModel,
  tokenText,
  trendModel,
} = monitor;

test("settings normalize an empty, edited or stale value", () => {
  assert.deepEqual(normalizeSettings(undefined), DEFAULT_SETTINGS);
  assert.deepEqual(normalizeSettings(null), DEFAULT_SETTINGS);
  assert.deepEqual(normalizeSettings({ mini: "yes", pet: 0, range: "2h" }), DEFAULT_SETTINGS);
  assert.deepEqual(normalizeSettings({ mini: true, pet: false, range: "7d" }), { mini: true, pet: false, range: "7d" });
});

test("a settings patch only touches the field it names", () => {
  const base = { mini: true, pet: false, range: "30d" };
  assert.deepEqual(mergeSettings(base, { pet: true }), { mini: true, pet: true, range: "30d" });
  // A malformed patch is ignored instead of clearing the form.
  assert.deepEqual(mergeSettings(base, null), base);
  // An unknown range falls back to the default window, not to an empty selector.
  assert.deepEqual(mergeSettings(base, { range: "90d" }), { mini: true, pet: false, range: "24h" });
});

test("the settings module reads and writes the same storage key as the sidebar", () => {
  // Both modules are separately imported blob modules; if the keys diverge the
  // setting saves but never comes back after a restart.
  const renderer = readFileSync("../plugins/monitor/renderer.js", "utf8");
  const keyOf = (source: string) => /const STORAGE_KEY = "([^"]+)"/.exec(source)?.[1];
  assert.equal(keyOf(renderer), "settings");
  assert.equal(keyOf(settingsSource), "settings");
  assert.match(settingsSource, /ctx\.storage\.set\(STORAGE_KEY/);
  // The plugin must not keep private browser storage: ctx.storage is the only
  // channel that survives a host restart and is scoped to this plugin.
  assert.doesNotMatch(settingsSource, /(?:local|session)Storage\./);
  assert.doesNotMatch(renderer, /(?:local|session)Storage\./);
});

test("the plugin declares only the capabilities it uses", () => {
  const manifest = JSON.parse(readFileSync("../plugins/monitor/plugin.json", "utf8"));
  assert.deepEqual([...manifest.capabilities].sort(), ["metrics:read", "storage", "ui:settings", "ui:sidebar"].sort());
  // Monitor reads metrics, never conversation text: asking for sessions:read
  // would grant message content it has no use for.
  assert.ok(!manifest.capabilities.includes("sessions:read"));
  assert.equal(manifest.entry.renderer, "renderer.js");
  assert.equal(manifest.entry.settings, "settings.js");
  assert.ok(manifest.hostVersions.includes("1.*") || manifest.hostVersions.includes("1.3.5"));
});

test("a provider that never reported cache fields is never shown as 0%", () => {
  assert.equal(hitRateText({ hitRate: null, reported: "NOT_REPORTED" }), "未上报");
  assert.equal(hitRateText({ hitRate: 0, reported: "REPORTED" }), "0.0%");
  assert.equal(hitRateText({ hitRate: 0.975, reported: "REPORTED" }), "97.5%");
  assert.equal(hitRateText({ hitRate: null, reported: "NO_SAMPLES" }), "--");
  assert.equal(hitRateText(undefined), "--");
});

test("unknown tokens and speed stay unknown", () => {
  assert.equal(tokenText(999), "999");
  assert.equal(tokenText(1500), "1.5K");
  assert.equal(tokenText(2_500_000), "2.50M");
  assert.equal(tokenText(null), "--");
  assert.equal(tokenText(-1), "--");
  assert.equal(speedText({ state: "REPORTED", tokensPerSecond: 140.44 }), "140.4 tok/s");
  assert.equal(speedText({ state: "NO_SAMPLES" }), "--");
  assert.equal(speedText(null), "--");
});

test("idle text shows elapsed time, never a TTL countdown", () => {
  assert.equal(idleText(45), "45 分钟");
  assert.equal(idleText(0.2), "不到 1 分钟");
  assert.equal(idleText(90), "1.5 小时");
  assert.equal(idleText(24 * 60 * 3), "3 天");
  assert.equal(idleText(null), "尚无请求");
  assert.doesNotMatch(idleText(45), /还剩|剩余|过期/);
});

test("the 2x2 metrics read the summary shape M2 actually returns", () => {
  const summary = {
    today: { steps: 4, inputOther: 1000, inputCacheRead: 3000, inputCacheCreation: 0, output: 500, hitRate: 0.75, reported: "REPORTED" },
    speed: { state: "REPORTED", tokensPerSecond: 61.25, samples: 3 },
  };
  const cards = metricCards(summary);
  assert.deepEqual(cards.map(card => card.key), ["input", "output", "cache", "speed"]);
  assert.equal(cards[0].value, "4.0K");
  assert.equal(cards[1].value, "500");
  assert.equal(cards[2].value, "75.0%");
  assert.equal(cards[3].value, "61.3 tok/s");
  assert.ok(cards.every(card => card.known));
});

test("an empty window reports no samples instead of zeroes", () => {
  const cards = metricCards({
    today: { steps: 0, inputOther: 0, inputCacheRead: 0, inputCacheCreation: 0, output: 0, hitRate: null, reported: "NO_SAMPLES" },
    speed: { state: "NO_SAMPLES", samples: 0 },
  });
  assert.deepEqual(cards.map(card => card.value), ["--", "--", "--", "--"]);
  assert.ok(cards.every(card => card.known === false));
});

test("an unreported provider gets a reason, not a fake hit rate", () => {
  const cards = metricCards({
    today: { steps: 2, inputOther: 800, inputCacheRead: 0, inputCacheCreation: 0, output: 90, hitRate: null, reported: "NOT_REPORTED" },
    speed: { state: "NO_SAMPLES" },
  });
  assert.equal(cards[2].value, "未上报");
  assert.equal(cards[2].note, "供应商未上报缓存数据");
  // Tokens themselves are real and still shown.
  assert.equal(cards[0].value, "800");
});

test("the sidebar renders one module grid and never duplicates the subagent list", () => {
  // A regression guard for the structure: two grids made the columns drift, and
  // an open detail panel used to render the same waiting sessions twice.
  const gridUsers = [...rendererSource.matchAll(/node\("div", "ksb-region"\)/g)];
  assert.equal(gridUsers.length, 1, "bodyBlocks builds exactly one module grid");
  const detail = /function detailBlock\(\)[\s\S]*?return block;/.exec(rendererSource)?.[0] ?? "";
  assert.doesNotMatch(detail, /agentsModules|agentsBlock/, "detail must not repeat the subagent list");
  assert.match(detail, /historySection\(\)/);
  assert.match(detail, /receiptSection\(\)/);
});

test("quota windows are read from value.quota.usages and keep a real 0%", () => {
  const quota = {
    state: "AVAILABLE",
    source: "官方 /oauth/usage",
    value: { kind: "ok", quota: { usages: {
      limit5h: { usedRatio: 0, resetAt: "2026-11-16T20:00:00Z" },
      limit7d: { usedRatio: 0.42, resetAt: "2026-11-20T00:00:00Z" },
    } } },
  };
  const view = quotaView(quota, Date.parse("2026-11-16T10:00:00Z"));
  assert.equal(view.state, "AVAILABLE");
  assert.equal(view.known, true);
  assert.equal(view.windows.length, 2);
  // A real 0 stays 0 and does not degrade into "unknown".
  assert.equal(view.windows[0].percent, 0);
  assert.equal(view.windows[0].reset?.short, "10h0m");
  assert.equal(view.windows[1].percent, 42);
});

test("a missing ratio is unknown even when another window is known", () => {
  const view = quotaView({ state: "AVAILABLE", value: { quota: { usages: {
    limit5h: { resetAt: "2026-11-16T20:00:00Z" },
    limit7d: { usedRatio: 1 },
  } } } }, Date.parse("2026-11-16T10:00:00Z"));
  assert.equal(view.windows[0].percent, null);
  assert.equal(view.windows[1].percent, 100);
});

test("a failed quota read keeps the core state and shows no window", () => {
  const unknown = quotaView({ state: "UNKNOWN", source: "官方 /oauth/usage", note: "额度暂不可用，稍后重试" });
  assert.equal(unknown.state, "UNKNOWN");
  assert.deepEqual(unknown.windows, []);
  assert.equal(unknown.note, "额度暂不可用，稍后重试");
  const exhausted = quotaView({ state: "EXHAUSTED", value: { quota: { usages: { limit5h: { usedRatio: 1 } } } } });
  assert.equal(exhausted.state, "EXHAUSTED");
  assert.equal(exhausted.windows[0].percent, 100);
});

test("history bars scale to the busiest bucket and skip empty ones", () => {
  const history = {
    range: "24h",
    unit: "hour",
    points: [
      { bucket: "2026-11-16T09:00", totals: { inputOther: 10, inputCacheRead: 90, inputCacheCreation: 0, reported: "REPORTED", hitRate: 0.9 } },
      { bucket: "2026-11-16T10:00", totals: { inputOther: 0, inputCacheRead: 0, inputCacheCreation: 0, reported: "NO_SAMPLES", hitRate: null } },
      { bucket: "2026-11-16T11:00", totals: { inputOther: 25, inputCacheRead: 25, inputCacheCreation: 0, reported: "REPORTED", hitRate: 0.5 } },
    ],
    totals: { inputOther: 60, inputCacheRead: 140, inputCacheCreation: 0, output: 300, reported: "REPORTED", hitRate: 0.7 },
  };
  const model = historyModel(history);
  assert.equal(model.unit, "hour");
  assert.equal(model.empty, false);
  assert.deepEqual(model.bars.map(bar => bar.empty), [false, true, false]);
  assert.equal(model.bars[0].height, 100);
  assert.equal(model.bars[1].height, 0);
  assert.equal(model.bars[2].height, 50);
  assert.equal(model.bars[2].total, 50);
  assert.equal(model.hitRate, "70.0%");
  assert.equal(model.output, "300");
});

test("an empty history is empty rather than a row of zero bars", () => {
  assert.equal(historyModel(null).empty, true);
  assert.equal(historyModel({ range: "7d", points: [] }).empty, true);
  const allEmpty = historyModel({ range: "30d", points: [{ bucket: "2026-11-01", totals: { reported: "NO_SAMPLES" } }] });
  assert.equal(allEmpty.empty, true);
});

test("the trend breaks at an unreported bucket instead of drawing a line", () => {
  const history = {
    range: "24h",
    points: [
      { bucket: "a", totals: { reported: "REPORTED", hitRate: 0.2 } },
      { bucket: "b", totals: { reported: "REPORTED", hitRate: 0.8 } },
      { bucket: "c", totals: { reported: "NOT_REPORTED", hitRate: null } },
      { bucket: "d", totals: { reported: "REPORTED", hitRate: 0.4 } },
    ],
  };
  const model = trendModel(history);
  assert.equal(model.runs.length, 2);
  assert.equal(model.samples, 3);
  assert.equal(model.drawable, true);
  // A single point cannot form a line, so it is not drawable on its own.
  assert.equal(trendModel({ points: [{ totals: { reported: "REPORTED", hitRate: 0.5 } }] }).drawable, false);
});

test("subagent rows only include sessions with real open background work", () => {
  const model = subagentModel([
    { sessionId: "s1", title: "调研", openBackground: 2, blocked: false, idleMinutes: 12, model: "kimi-k2", recent: { reported: "REPORTED", hitRate: 0.5 } },
    { sessionId: "s2", title: "空闲", openBackground: 0, blocked: false, idleMinutes: 3 },
    { sessionId: "s3", title: "", openBackground: 0, blocked: true, idleMinutes: null, model: "UNKNOWN" },
    { sessionId: "s4", openBackground: 1, blocked: false, idleMinutes: 1 },
  ]);
  assert.deepEqual(model.rows.map(row => row.id), ["s1", "s3", "s4"]);
  assert.equal(model.openTotal, 3);
  assert.equal(model.waiting, 3);
  assert.equal(model.rows[1].title, "未命名会话");
  assert.equal(model.rows[1].model, "模型未关联");
  assert.equal(model.rows[1].idle, "尚无请求");
  assert.equal(model.rows[0].hitRate, "50.0%");
});

test("no subagent data means an empty list, never an invented agent", () => {
  const model = subagentModel(undefined);
  assert.deepEqual(model.rows, []);
  assert.equal(model.openTotal, 0);
  // A busy session flag is not the same as an agent row: only openBackground and
  // blocked produce entries.
  assert.deepEqual(subagentModel([{ sessionId: "s", busy: true }]).rows, []);
});

test("receipts are labelled and capped at three", () => {
  const model = receiptModel([
    { kind: "keepalive", state: "verified", sessionId: "abcdefgh12345", note: "主代理下一步命中缓存", at: "2026-11-16T10:00:00Z" },
    { kind: "compaction", state: "acknowledged", sessionId: "s2", sessionTitle: "长会话", note: "", at: "2026-11-16T09:00:00Z" },
    { kind: "recovery", state: "failed", sessionId: "s3", note: "429", at: "2026-11-16T08:00:00Z" },
    { kind: "keepalive", state: "unknown", sessionId: "s4", note: "", at: "2026-11-16T07:00:00Z" },
  ]);
  assert.equal(model.length, 3);
  assert.equal(model[0].kind, "保活");
  assert.equal(model[0].state, "保活成功");
  assert.equal(model[0].session, "abcdefgh12");
  assert.equal(model[1].session, "长会话");
  assert.equal(model[2].state, "发送失败");
  assert.deepEqual(receiptModel(null), []);
});

test("clock text is local HH:MM and unknown time is a dash", () => {
  const at = new Date(2026, 10, 16, 9, 5);
  assert.equal(clockText(at), "09:05");
  assert.equal(clockText(null), "--:--");
  assert.equal(clockText(new Date("nope")), "--:--");
});

/* ---------- 刷新控制器 ---------- */

interface Timer { fn: () => void; at: number }

function fakeClock() {
  let current = 0;
  const timers = new Map<number, Timer>();
  let nextId = 1;
  return {
    now: () => current,
    advance(ms: number) {
      current += ms;
      for (const [id, timer] of [...timers]) {
        if (timer.at > current) continue;
        timers.delete(id);
        timer.fn();
      }
    },
    setTimer(fn: () => void, ms: number) {
      const id = nextId++;
      timers.set(id, { fn, at: current + Math.max(0, ms) });
      return id;
    },
    clearTimer(id: number) {
      timers.delete(id);
    },
    pending: () => timers.size,
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

test("a burst of cache events collapses into one refresh", async () => {
  const clock = fakeClock();
  let loads = 0;
  const applied: { n: number }[] = [];
  const refresher = createRefresher({
    load: async () => {
      loads += 1;
      return { n: loads };
    },
    apply: (value: unknown) => applied.push(value as { n: number }),
    minGap: 10_000,
    fallback: 60_000,
    now: clock.now,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
  });
  refresher.start();
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(loads, 1);
  // Ten cache events in the same second schedule one deferred refresh, not ten.
  for (let index = 0; index < 10; index += 1) refresher.trigger();
  assert.equal(loads, 1);
  assert.equal(clock.pending(), 1);
  clock.advance(10_000);
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(loads, 2);
  refresher.dispose();
});

test("a slow response cannot overwrite a newer one", async () => {
  const clock = fakeClock();
  const first = deferred<{ value: string }>();
  const second = deferred<{ value: string }>();
  const queue = [first, second];
  const applied: { value: string }[] = [];
  const refresher = createRefresher({
    load: () => queue.shift()!.promise,
    apply: (value: unknown) => applied.push(value as { value: string }),
    minGap: 0,
    fallback: 60_000,
    now: clock.now,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
  });
  refresher.start();
  refresher.refresh();
  second.resolve({ value: "new" });
  await Promise.resolve();
  await Promise.resolve();
  first.resolve({ value: "old" });
  await Promise.resolve();
  await Promise.resolve();
  assert.deepEqual(applied, [{ value: "new" }]);
  refresher.dispose();
});

test("dispose clears the pending timer and ignores late results", async () => {
  const clock = fakeClock();
  const pending = deferred<{ value: string }>();
  const applied: { value: string }[] = [];
  const refresher = createRefresher({
    load: () => pending.promise,
    apply: (value: unknown) => applied.push(value as { value: string }),
    minGap: 10_000,
    fallback: 60_000,
    now: clock.now,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
  });
  refresher.start();
  refresher.trigger();
  assert.equal(clock.pending(), 1);
  refresher.dispose();
  assert.equal(clock.pending(), 0);
  pending.resolve({ value: "late" });
  await Promise.resolve();
  await Promise.resolve();
  assert.deepEqual(applied, []);
  assert.equal(refresher.status().disposed, true);
});

test("a failed load reports the error and still schedules the fallback", async () => {
  const clock = fakeClock();
  const errors: string[] = [];
  let loads = 0;
  const refresher = createRefresher({
    load: async () => {
      loads += 1;
      throw new Error("核心暂不可用");
    },
    apply: () => {},
    fail: (error: unknown) => errors.push((error as Error).message),
    minGap: 10_000,
    fallback: 60_000,
    now: clock.now,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
  });
  refresher.start();
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
  assert.deepEqual(errors, ["核心暂不可用"]);
  assert.equal(clock.pending(), 1);
  clock.advance(60_000);
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(loads, 2);
  refresher.dispose();
});

test("a trigger inside the window waits only the remainder", async () => {
  const clock = fakeClock();
  let loads = 0;
  const refresher = createRefresher({
    load: async () => {
      loads += 1;
      return { n: loads };
    },
    apply: () => {},
    minGap: 10_000,
    fallback: 60_000,
    now: clock.now,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
  });
  refresher.start();
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(loads, 1);
  clock.advance(3_000);
  refresher.trigger();
  // The coalescing window is measured from the last start, so this read is due at
  // 10s, not 13s: re-scheduling the whole gap would drift further on every event.
  assert.equal(clock.pending(), 1);
  clock.advance(6_999);
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(loads, 1, "not due before the window closes");
  clock.advance(1);
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(loads, 2);
  refresher.dispose();
});

test("a timer that expires while hidden parks until the surface is visible", async () => {
  const clock = fakeClock();
  let visible = true;
  let loads = 0;
  const refresher = createRefresher({
    load: async () => {
      loads += 1;
      return { n: loads };
    },
    apply: () => {},
    isVisible: () => visible,
    minGap: 10_000,
    fallback: 60_000,
    now: clock.now,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
  });
  refresher.start();
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(loads, 1);
  visible = false;
  clock.advance(60_000);
  await Promise.resolve();
  await Promise.resolve();
  // A hidden sidebar does not read the core: the due work is parked instead of run
  // and no repeating timer keeps firing in the background.
  assert.equal(loads, 1);
  assert.equal(refresher.status().deferred, true);
  assert.equal(clock.pending(), 0);
  visible = true;
  refresher.resume();
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(loads, 2);
  assert.equal(refresher.status().deferred, false);
  refresher.dispose();
});

test("regaining visibility without due work does not read the core", async () => {
  const clock = fakeClock();
  let visible = true;
  let loads = 0;
  const refresher = createRefresher({
    load: async () => {
      loads += 1;
      return { n: loads };
    },
    apply: () => {},
    isVisible: () => visible,
    minGap: 10_000,
    fallback: 60_000,
    now: clock.now,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
  });
  refresher.start();
  await Promise.resolve();
  await Promise.resolve();
  visible = false;
  clock.advance(1_000);
  visible = true;
  refresher.resume();
  await Promise.resolve();
  await Promise.resolve();
  // Focus alone is not a reason to poll: the fallback window has not elapsed.
  assert.equal(loads, 1);
  assert.equal(refresher.status().deferred, false);
  refresher.dispose();
});

test("resume on a hidden surface stays parked", async () => {
  const clock = fakeClock();
  let loads = 0;
  const refresher = createRefresher({
    load: async () => {
      loads += 1;
      return { n: loads };
    },
    apply: () => {},
    isVisible: () => false,
    minGap: 10_000,
    fallback: 60_000,
    now: clock.now,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
  });
  refresher.start();
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(loads, 1);
  refresher.resume();
  await Promise.resolve();
  await Promise.resolve();
  // A resume from a still hidden surface is not a reason to read the core.
  assert.equal(loads, 1);
  refresher.dispose();
});

test("dispose drops a parked read", async () => {
  const clock = fakeClock();
  let visible = false;
  let loads = 0;
  const refresher = createRefresher({
    load: async () => {
      loads += 1;
      return { n: loads };
    },
    apply: () => {},
    isVisible: () => visible,
    minGap: 10_000,
    fallback: 60_000,
    now: clock.now,
    setTimer: clock.setTimer,
    clearTimer: clock.clearTimer,
  });
  refresher.start();
  await Promise.resolve();
  await Promise.resolve();
  clock.advance(60_000);
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(refresher.status().deferred, true);
  assert.equal(loads, 1);
  refresher.dispose();
  assert.equal(refresher.status().deferred, false);
  visible = true;
  refresher.resume();
  await Promise.resolve();
  await Promise.resolve();
  assert.equal(loads, 1, "a disposed refresher never reads again");
  assert.equal(clock.pending(), 0);
});

/* ---------- 侧栏与设置页的分工 ---------- */

test("the trend window follows the sidebar range instead of a fixed label", () => {
  const trend = /function trendModules\(\)[\s\S]*?\n  \}/.exec(rendererSource)?.[0] ?? "";
  assert.ok(trend, "trendModules is found in the shipped file");
  assert.match(trend, /RANGE_LABEL\[state\.range\]/);
  assert.match(trend, /state\.history\.range === state\.range/);
  // The tag used to hard-code "近 24 小时" while the bars could show another window.
  assert.doesNotMatch(rendererSource, /"近 24 小时"/);
  // A point is one aggregated bucket, not a single request.
  assert.match(trend, /聚合命中率/);
  assert.match(trend, /state\.range === "24h" \? "小时" : "天"/);
  assert.doesNotMatch(trend, /一次真实上报/);
});

test("a sidebar range switch is temporary and never rewrites the default", () => {
  const showRange = /function showRange\(range\)[\s\S]*?\n  \}/.exec(rendererSource)?.[0] ?? "";
  assert.ok(showRange, "showRange is found in the shipped file");
  assert.match(showRange, /state\.range = range/);
  assert.match(showRange, /refresher\.trigger\(\)/);
  assert.doesNotMatch(showRange, /storage\.set|persist\(/);
  // The history request and both range selectors read the sidebar's own value.
  assert.match(rendererSource, /cache\.history", \{ range: state\.range \}/);
  assert.match(rendererSource, /setAttribute\("aria-pressed", String\(state\.range === item\)\)/);
});

test("a settings write that fails leaves the published value untouched", () => {
  const persist = /async function persist\(next\)[\s\S]*?\n  \}/.exec(rendererSource)?.[0] ?? "";
  assert.ok(persist, "persist is found in the shipped file");
  // The commit of state.settings comes after the awaited write, so a rejected write
  // cannot publish a value that would be gone after a restart.
  const writeAt = persist.indexOf("await ctx.storage.set");
  const commitAt = persist.indexOf("state.settings = candidate");
  const publishAt = persist.indexOf("hub.publish(candidate)");
  assert.ok(writeAt >= 0 && commitAt > writeAt, "settings commit after the successful write");
  assert.ok(publishAt > commitAt, "the hub only hears about a saved value");
  assert.match(persist, /settingsError/);
});

test("the settings page also writes before it commits", () => {
  const save = /async function save\(patch, message\)[\s\S]*?\n  \}/.exec(settingsSource)?.[0] ?? "";
  assert.ok(save, "save is found in the shipped settings file");
  const writeAt = save.indexOf("await ctx.storage.set");
  const commitAt = save.indexOf("settings = candidate");
  assert.ok(writeAt >= 0 && commitAt > writeAt, "settings commit after the successful write");
  assert.match(save, /仍在使用上次保存的设置/);
});

test("the settings page brings its own scoped styles", () => {
  // The panel is injected into the host page, which has no rules for these class
  // names; without its own stylesheet it rendered as unstyled text.
  assert.match(settingsSource, /createElement\("style"\)/);
  assert.match(settingsSource, /\.ekdp-mon-/);
  const cleanup = /return \(\) => \{[\s\S]*?\};/.exec(settingsSource)?.[0] ?? "";
  assert.match(cleanup, /style\.remove\(\)/, "the injected style is removed with the panel");
});
