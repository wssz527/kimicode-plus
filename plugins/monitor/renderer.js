// Monitor · 预装适配插件
//
// 结构与样式参考 bfjnbvf/kimi-code-monitor@1d529f0b（MIT，见同目录 LICENSE），
// 数据全部来自宿主 M2 指标接口，不读取会话正文、不携带上游的采集器、浏览器
// 本地存储或经验 TTL。未知值显示为“--/未上报”，从不补零（计划 D4）。
//
// 本文件必须保持单文件：插件 loader 以 blob URL 导入，相对 import 无法解析。
// 纯函数（映射、格式化、设置清洗、刷新控制器）单独导出，供 Node 直接测试。

const STORAGE_KEY = "settings";
const HUB_KEY = "ekdpMonitorSettingsHub";
const RANGES = ["24h", "7d", "30d"];
const RANGE_LABEL = { "24h": "24 小时", "7d": "7 天", "30d": "30 天" };
const RECEIPT_KIND = { keepalive: "保活", compaction: "压缩", recovery: "额度恢复" };
const RECEIPT_STATE = {
  verified: "保活成功",
  completed: "官方已完成",
  acknowledged: "官方已接收",
  invalid: "保活无效",
  failed: "发送失败",
  cancelled: "已取消",
  skipped: "未发送",
  unknown: "结果未知",
};

export const DEFAULT_SETTINGS = { mini: false, pet: true, range: "24h" };

/** Validates a stored settings object. Anything unusable falls back to a default
 * instead of breaking the surface: a hand-edited or older value must not turn
 * the sidebar into an empty box. */
export function normalizeSettings(raw) {
  const source = raw && typeof raw === "object" ? raw : {};
  return {
    mini: source.mini === true,
    pet: source.pet !== false,
    range: RANGES.includes(source.range) ? source.range : DEFAULT_SETTINGS.range,
  };
}

export function mergeSettings(current, patch) {
  const base = normalizeSettings(current);
  const next = patch && typeof patch === "object" ? patch : {};
  return normalizeSettings({
    mini: "mini" in next ? next.mini : base.mini,
    pet: "pet" in next ? next.pet : base.pet,
    range: "range" in next ? next.range : base.range,
  });
}

/** A provider that never reported cache fields must never render as 0%. */
export function hitRateText(totals) {
  if (!totals) return "--";
  if (totals.reported === "NOT_REPORTED") return "未上报";
  if (typeof totals.hitRate !== "number" || !Number.isFinite(totals.hitRate)) return "--";
  return `${(totals.hitRate * 100).toFixed(1)}%`;
}

export function tokenText(value) {
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) return "--";
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(2)}M`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`;
  return String(Math.round(value));
}

export function speedText(speed) {
  if (!speed || speed.state !== "REPORTED") return "--";
  const rate = speed.tokensPerSecond;
  if (typeof rate !== "number" || !Number.isFinite(rate)) return "--";
  return `${rate.toFixed(1)} tok/s`;
}

/** "距上次请求 N 分钟" counts only the main agent's own model request. */
export function idleText(minutes) {
  if (typeof minutes !== "number" || !Number.isFinite(minutes)) return "尚无请求";
  if (minutes < 1) return "不到 1 分钟";
  if (minutes < 60) return `${Math.round(minutes)} 分钟`;
  const hours = minutes / 60;
  return hours < 48 ? `${hours.toFixed(1)} 小时` : `${Math.round(hours / 24)} 天`;
}

function percentOf(ratio) {
  if (typeof ratio !== "number" || !Number.isFinite(ratio)) return null;
  return Math.round(Math.min(Math.max(ratio, 0), 1) * 100);
}

function resetInfo(resetAt, now) {
  if (typeof resetAt !== "string" || !resetAt) return null;
  const at = new Date(resetAt);
  if (Number.isNaN(at.getTime())) return null;
  const minutes = Math.ceil((at.getTime() - now) / 60_000);
  if (minutes <= 0) return { short: "待刷新", long: "已到重置点，等待官方用量刷新" };
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);
  const short = days ? `${days}d${hours % 24}h` : hours ? `${hours}h${minutes % 60}m` : `${minutes}m`;
  return { short, long: `${at.toLocaleString("zh-CN")} 重置` };
}

/**
 * Maps cache.summary.quota onto two allowance windows. `value` is the official
 * /oauth/usage payload as returned by the core, so the window shape is read from
 * `value.quota.usages` rather than guessed. A real 0% stays 0%; a missing ratio
 * is `percent: null` and is never displayed as 0; a failed read keeps the state
 * UNKNOWN/ERROR from the core.
 */
export function quotaView(quota, now = Date.now()) {
  const state = String(quota?.state || "UNKNOWN").toUpperCase();
  const usages = quota?.value?.quota?.usages;
  const windows = [];
  const labels = [["limit5h", "5 小时"], ["limit7d", "本周"]];
  for (const [key, label] of labels) {
    const raw = usages && typeof usages === "object" ? usages[key] : undefined;
    if (!raw || typeof raw !== "object") continue;
    const percent = percentOf(raw.usedRatio);
    windows.push({ key, label, percent, reset: resetInfo(raw.resetAt, now) });
  }
  return {
    state,
    source: typeof quota?.source === "string" ? quota.source : "",
    note: typeof quota?.note === "string" ? quota.note : "",
    windows,
    known: windows.some(window => window.percent !== null),
  };
}

/** The 2x2 metrics. `steps === 0` means there is nothing to aggregate yet, so
 * tokens read as unknown rather than as a real zero. */
export function metricCards(summary) {
  const today = summary?.today;
  const hasSamples = Boolean(today && Number.isFinite(today.steps) && today.steps > 0);
  const input = hasSamples ? today.inputOther + today.inputCacheRead + today.inputCacheCreation : null;
  return [
    { key: "input", label: "输入", value: tokenText(input), known: input !== null },
    { key: "output", label: "输出", value: tokenText(hasSamples ? today.output : null), known: hasSamples },
    {
      key: "cache",
      label: "缓存命中",
      value: hitRateText(today),
      known: hasSamples && today.reported === "REPORTED" && typeof today.hitRate === "number",
      note: today?.reported === "NOT_REPORTED" ? "供应商未上报缓存数据" : hasSamples ? "" : "今日还没有模型请求",
    },
    {
      key: "speed",
      label: "速度",
      value: speedText(summary?.speed),
      known: summary?.speed?.state === "REPORTED",
      note: summary?.speed?.state === "REPORTED" ? `按解码时长 · ${summary.speed.samples ?? 0} 个样本` : "没有可用的解码时长样本",
    },
  ];
}

/** A histogram of input tokens per bucket, plus the range's honest aggregates.
 * Heights are relative to the busiest bucket; an empty bucket has no bar at all
 * instead of a zero-height rectangle that would read as "0 tokens". */
export function historyModel(history) {
  const points = Array.isArray(history?.points) ? history.points : [];
  const totalsOf = point => {
    const totals = point?.totals || {};
    return (totals.inputOther || 0) + (totals.inputCacheRead || 0) + (totals.inputCacheCreation || 0);
  };
  const peak = points.reduce((max, point) => Math.max(max, totalsOf(point)), 0);
  const bars = points.map(point => {
    const total = totalsOf(point);
    const empty = !point?.totals || point.totals.reported === "NO_SAMPLES";
    return {
      bucket: String(point?.bucket || ""),
      total,
      empty,
      height: empty || peak <= 0 ? 0 : Math.max(2, Math.round((total / peak) * 100)),
      hitRate: hitRateText(point?.totals),
    };
  });
  return {
    range: String(history?.range || ""),
    unit: history?.unit === "day" ? "day" : "hour",
    bars,
    peak,
    empty: bars.length === 0 || bars.every(bar => bar.empty),
    hitRate: hitRateText(history?.totals),
    read: tokenText(history?.totals?.inputCacheRead),
    miss: tokenText(history?.totals?.inputOther),
    output: tokenText(history?.totals?.output),
  };
}

/** Contiguous runs of reported hit rates over the whole window, so a gap stays a
 * gap: joining across an unreported bucket would invent data. */
export function trendModel(history) {
  const points = Array.isArray(history?.points) ? history.points : [];
  const total = points.length;
  const runs = [];
  let run = [];
  points.forEach((point, index) => {
    const rate = point?.totals?.hitRate;
    const reported = point?.totals?.reported === "REPORTED" && typeof rate === "number" && Number.isFinite(rate);
    if (!reported) {
      if (run.length) runs.push(run);
      run = [];
      return;
    }
    const x = total <= 1 ? 0 : (index / (total - 1)) * 100;
    const y = 26 - Math.min(Math.max(rate, 0), 1) * 24;
    run.push([Number(x.toFixed(2)), Number(y.toFixed(2))]);
  });
  if (run.length) runs.push(run);
  const samples = runs.reduce((count, points) => count + points.length, 0);
  return { runs: runs.filter(points => points.length > 0), samples, drawable: samples >= 2 };
}

/**
 * The real background-subagent signal this build has: cache.sessions carries an
 * `openBackground` count and a `blocked` flag per session, and no per-agent
 * identity. The list therefore shows waiting sessions with their counts; it
 * never invents agent names, models or token numbers for individual subagents.
 */
export function subagentModel(sessions) {
  const rows = (Array.isArray(sessions) ? sessions : [])
    .filter(row => row && (Number(row.openBackground) > 0 || row.blocked === true))
    .map(row => ({
      id: String(row.sessionId || ""),
      title: String(row.title || "").trim() || "未命名会话",
      model: !row.model || row.model === "UNKNOWN" ? "模型未关联" : String(row.model),
      open: Math.max(0, Number(row.openBackground) || 0),
      blocked: row.blocked === true,
      idle: idleText(row.idleMinutes),
      hitRate: hitRateText(row.recent),
    }));
  return {
    rows,
    openTotal: rows.reduce((sum, row) => sum + row.open, 0),
    waiting: rows.length,
  };
}

export function receiptModel(receipts) {
  return (Array.isArray(receipts) ? receipts : []).slice(0, 3).map((receipt, index) => ({
    key: `${receipt?.kind || "task"}-${receipt?.sessionId || index}-${receipt?.at || index}`,
    kind: RECEIPT_KIND[receipt?.kind] || String(receipt?.kind || "任务"),
    state: RECEIPT_STATE[receipt?.state] || String(receipt?.state || "未知状态"),
    session: String(receipt?.sessionTitle || "").trim() || String(receipt?.sessionId || "").slice(0, 10),
    note: String(receipt?.note || ""),
    at: typeof receipt?.at === "string" ? receipt.at : "",
  }));
}

export function clockText(date) {
  if (!(date instanceof Date) || Number.isNaN(date.getTime())) return "--:--";
  return `${String(date.getHours()).padStart(2, "0")}:${String(date.getMinutes()).padStart(2, "0")}`;
}

/**
 * One coalescing refresh loop. It owns the generation counter, so a slow request
 * can never overwrite a newer one, and it clears its timer on dispose instead of
 * leaving a callback that would touch a detached DOM.
 *
 * Timers are injectable so a Node test can drive it without a browser.
 */
export function createRefresher(options) {
  const load = options.load;
  const apply = options.apply;
  const fail = options.fail ?? (() => {});
  const minGap = options.minGap ?? 10_000;
  const fallback = options.fallback ?? 60_000;
  const now = options.now ?? (() => Date.now());
  const setTimer = options.setTimer ?? ((fn, ms) => setTimeout(fn, ms));
  const clearTimer = options.clearTimer ?? (handle => clearTimeout(handle));

  let disposed = false;
  let generation = 0;
  let lastStart = -Infinity;
  let timer = null;
  let inFlight = false;

  function clear() {
    if (timer !== null) {
      clearTimer(timer);
      timer = null;
    }
  }

  function schedule(ms) {
    clear();
    if (disposed) return;
    timer = setTimer(() => {
      timer = null;
      void run();
    }, Math.max(0, ms));
  }

  async function run(force = false) {
    if (disposed) return;
    // A plain trigger (an event, a timer) never stacks another request on top of
    // one that is still running: the running one already covers that change. A
    // manual refresh is different — the user asked for a read after this request
    // started — so it opens a new generation and the older answer is dropped by
    // the generation check below instead of overwriting the newer one.
    if (inFlight && !force) return;
    inFlight = true;
    const mine = ++generation;
    lastStart = now();
    let result;
    let error = null;
    try {
      result = await load();
    } catch (cause) {
      error = cause;
    } finally {
      if (mine === generation) inFlight = false;
    }
    if (disposed || mine !== generation) return;
    if (error) fail(error);
    else apply(result);
    schedule(fallback);
  }

  return {
    trigger() {
      if (disposed) return;
      const gap = now() - lastStart;
      if (gap < minGap) {
        schedule(gap);
        return;
      }
      void run();
    },
    /** A user-requested read. It queues behind nothing and supersedes the answer
     * of a request that started earlier. */
    refresh() {
      if (disposed) return;
      void run(true);
    },
    start() {
      if (disposed) return;
      void run(true);
    },
    dispose() {
      disposed = true;
      clear();
    },
    status() {
      return { disposed, generation, scheduled: timer !== null };
    },
  };
}

/* ---------- 设置共享通道 ---------- */
// 设置模块与挂载模块是两个 blob 模块，无法相互 import；但两者拿到的是同一个
// ctx 对象。把订阅表挂在 ctx 上一个命名空间属性里，既能让设置改动即时生效，
// 又不引入任何全局对象或全局监听，插件停用后随 ctx 一起被回收。
export function settingsHub(ctx) {
  if (!ctx[HUB_KEY]) {
    const listeners = new Set();
    ctx[HUB_KEY] = {
      listeners,
      publish(next) {
        for (const listener of [...listeners]) {
          try {
            listener(next);
          } catch {
            /* 一个监听器失败不影响另一个 */
          }
        }
      },
    };
  }
  return ctx[HUB_KEY];
}

/* ---------- DOM ---------- */

const STYLE = String.raw`
:host {
  display: block;
  min-width: 0;
  color: inherit;
  font: inherit;
  background: transparent;
}
.ekd-mon {
  --mon-text: var(--ink, var(--color-text, currentColor));
  --mon-muted: var(--ink-muted, var(--color-text-secondary, color-mix(in srgb, currentColor 80%, transparent)));
  --mon-faint: color-mix(in srgb, currentColor 74%, transparent);
  --mon-line: var(--line, var(--color-line, color-mix(in srgb, currentColor 14%, transparent)));
  --mon-fill: color-mix(in srgb, currentColor 7%, transparent);
  --mon-fill-hover: color-mix(in srgb, currentColor 12%, transparent);
  --mon-accent: var(--accent, var(--blue, #1783ff));
  --mon-green: var(--success, #16a34a);
  --mon-warn: var(--warn, #d97706);
  --mon-danger: var(--danger, #dc2626);
  --mon-mono: var(--font-mono, ui-monospace, "SF Mono", Menlo, monospace);
  container-type: inline-size;
  display: block;
  min-width: 0;
  padding: 2px 0 4px;
  background: transparent;
  color: var(--mon-muted);
  font-family: var(--font-ui, var(--font-sans, system-ui, sans-serif));
  font-size: 12px;
  line-height: 1.45;
  text-align: left;
}
.ekd-mon *,
.ekd-mon *::before,
.ekd-mon *::after { box-sizing: border-box; }
.ekd-mon p { margin: 0; }
.ekd-mon button { font: inherit; color: inherit; }

.ksb-header { display: flex; align-items: center; gap: 6px; min-width: 0; }
.ksb-status-dot { flex: none; width: 6px; height: 6px; border-radius: 50%; background: var(--mon-faint); }
.ksb-dot-off { background: var(--mon-danger); }
.ksb-dot-warn { background: var(--mon-warn); }
.ksb-dot-live { background: var(--mon-green); }
.ksb-title {
  flex: 0 1 auto; min-width: 0; overflow: hidden;
  color: var(--mon-faint); font-size: 10px; font-weight: 600;
  letter-spacing: 0.06em; text-transform: uppercase; white-space: nowrap;
}
.ksb-agent-status { flex: 0 1 auto; color: var(--mon-muted); font-size: 10px; white-space: nowrap; }
.ksb-header-stat {
  display: inline-flex; align-items: center; gap: 3px; margin-left: auto;
  padding: 1px 6px; border: 1px solid transparent; border-radius: 100px;
  background: var(--mon-fill); color: var(--mon-muted); font-size: 10px; cursor: pointer;
  transition: background-color 150ms ease;
}
.ksb-header-stat:hover { background: var(--mon-fill-hover); }
.ksb-header-stat:focus-visible { outline: 2px solid var(--mon-accent); outline-offset: 2px; }
.ksb-header-stat-value { font-family: var(--mon-mono); font-variant-numeric: tabular-nums; }
.ksb-icon-btn {
  display: inline-flex; align-items: center; justify-content: center; flex: none;
  width: 20px; height: 20px; padding: 0; border: 0; border-radius: 6px;
  background: transparent; color: var(--mon-faint); cursor: pointer;
}
.ksb-icon-btn:hover { background: var(--mon-fill); color: var(--mon-text); }
.ksb-icon-btn:focus-visible { outline: 2px solid var(--mon-accent); outline-offset: 1px; }
.ksb-chevron { display: inline-flex; transition: transform 160ms ease; }
.ksb-icon-btn[aria-expanded="true"] .ksb-chevron { transform: rotate(180deg); }
.ksb-detail-toggle { width: auto; height: 22px; padding: 2px 6px; gap: 5px; color: var(--mon-muted); font-size: 10px; }
.ksb-refresh-icon { display: inline-flex; }
.ekd-mon.is-busy .ksb-refresh-icon { animation: ksb-spin 900ms linear infinite; }

.ksb-banner {
  margin-top: 6px; padding: 5px 7px; border: 1px dashed var(--mon-danger); border-radius: 8px;
  color: var(--mon-danger); font-size: 11px; line-height: 1.45;
}
.ksb-banner strong { display: block; font-weight: 600; }
.ksb-banner small { display: block; margin-top: 1px; color: var(--mon-muted); font-size: 10px; overflow-wrap: anywhere; }
.ksb-banner-action {
  margin-top: 3px; padding: 0 7px; border: 1px solid currentColor; border-radius: 100px;
  background: transparent; color: inherit; font-size: 10px; cursor: pointer;
}
.ksb-banner-action:focus-visible { outline: 2px solid var(--mon-accent); outline-offset: 2px; }

.ksb-region { display: grid; grid-template-columns: 1fr 1fr; gap: 7px 12px; margin-top: 8px; }
.ksb-module { min-width: 0; }
.ksb-span-2 { grid-column: 1 / -1; }
.ksb-section-head { display: flex; align-items: baseline; gap: 6px; min-width: 0; }
.ksb-quota-label { flex: none; color: var(--mon-faint); font-size: 10px; }
.ksb-tag {
  margin-left: auto; padding: 0 6px; border-radius: 100px; background: var(--mon-fill);
  color: var(--mon-muted); font-size: 9px; white-space: nowrap;
}
.ksb-tag.is-live { color: var(--mon-accent); }
.ksb-tag.is-plain { background: transparent; border: 1px solid var(--mon-line); }

.ksb-quota-group { display: flex; flex-direction: column; gap: 4px; min-width: 0; container: ksb-quota / inline-size; }
.ksb-quota-head { display: flex; align-items: baseline; gap: 4px; }
.ksb-reset {
  flex: none; margin-left: auto; color: var(--mon-faint); font-family: var(--mon-mono);
  font-size: 10px; font-variant-numeric: tabular-nums; white-space: nowrap;
}
.ksb-quota-pct {
  flex: none; min-width: 24px; color: var(--mon-text); font-family: var(--mon-mono);
  font-size: 11px; font-weight: 500; font-variant-numeric: tabular-nums; text-align: right;
}
.ksb-quota-pct.mid { color: var(--mon-warn); }
.ksb-quota-pct.high { color: var(--mon-danger); }
.ksb-quota-pct.is-unknown { color: var(--mon-faint); }
.ksb-progress { height: 3px; border-radius: 100px; background: var(--mon-fill); overflow: hidden; }
.ksb-progress-fill { height: 100%; border-radius: 100px; background: var(--mon-green); transform-origin: left center; transition: transform 240ms ease-out, background-color 200ms ease; }
.ksb-progress-fill.mid { background: var(--mon-warn); }
.ksb-progress-fill.high { background: var(--mon-danger); }
.ksb-progress.is-unknown .ksb-progress-fill { background: transparent; }

.ksb-stat-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 6px 12px; }
.ksb-stat { display: flex; flex-direction: column; gap: 1px; min-width: 0; }
.ksb-stat-label { color: var(--mon-faint); font-size: 10px; white-space: nowrap; }
.ksb-stat-value {
  color: var(--mon-text); font-family: var(--mon-mono); font-size: 14px; font-weight: 500;
  font-variant-numeric: tabular-nums; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.ksb-stat-value.is-unknown { color: var(--mon-faint); font-size: 13px; }
.ksb-value-note { color: var(--mon-faint); font-size: 9px; line-height: 1.35; }

.ksb-trend { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.ksb-spark { display: block; width: 100%; height: 30px; overflow: visible; }
.ksb-spark-run { fill: none; stroke: var(--mon-accent); stroke-width: 1.5; stroke-linecap: round; stroke-linejoin: round; vector-effect: non-scaling-stroke; }
.ksb-spark-dot { fill: var(--mon-accent); }
.ksb-spark-base { stroke: var(--mon-line); stroke-width: 1; stroke-dasharray: 2 3; vector-effect: non-scaling-stroke; }
.ksb-footnote { color: var(--mon-faint); font-size: 10px; line-height: 1.45; }

.ksb-agents { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.ksb-agent-row {
  display: grid; grid-template-columns: minmax(0, 1fr) auto auto; align-items: baseline;
  gap: 6px; padding: 2px 0; border-top: 1px solid var(--mon-line);
}
.ksb-agent-row:first-of-type { border-top: 0; }
.ksb-agent-name { min-width: 0; overflow: hidden; color: var(--mon-text); font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }
.ksb-agent-count { color: var(--mon-accent); font-family: var(--mon-mono); font-size: 10px; font-variant-numeric: tabular-nums; }
.ksb-agent-idle { color: var(--mon-faint); font-family: var(--mon-mono); font-size: 9px; font-variant-numeric: tabular-nums; white-space: nowrap; }
.ksb-agent-blocked { color: var(--mon-warn); }
.ksb-agents-empty { color: var(--mon-faint); font-size: 10px; }

.ksb-history { display: flex; flex-direction: column; gap: 5px; min-width: 0; }
.ksb-range { display: inline-flex; gap: 2px; padding: 1px; border-radius: 100px; background: var(--mon-fill); }
.ksb-range button {
  padding: 1px 7px; border: 0; border-radius: 100px; background: transparent;
  color: var(--mon-muted); font-size: 10px; cursor: pointer;
}
.ksb-range button[aria-pressed="true"] { background: var(--mon-fill-hover); color: var(--mon-text); font-weight: 600; }
.ksb-range button:focus-visible { outline: 2px solid var(--mon-accent); outline-offset: 1px; }
.ksb-bars { display: flex; align-items: flex-end; gap: 1px; height: 34px; }
.ksb-bar { flex: 1 1 0; min-width: 1px; height: 100%; display: flex; align-items: flex-end; }
.ksb-bar-fill { width: 100%; border-radius: 1px 1px 0 0; background: color-mix(in srgb, var(--mon-accent) 62%, transparent); }
.ksb-bar:hover .ksb-bar-fill { background: var(--mon-accent); }
.ksb-bars-empty { color: var(--mon-faint); font-size: 10px; line-height: 34px; }
.ksb-totals { display: flex; flex-wrap: wrap; gap: 2px 10px; color: var(--mon-faint); font-size: 10px; }
.ksb-totals strong { color: var(--mon-text); font-family: var(--mon-mono); font-weight: 500; font-variant-numeric: tabular-nums; }

.ksb-receipts { display: flex; flex-direction: column; gap: 3px; }
.ksb-receipt { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 1px 6px; font-size: 10px; }
.ksb-receipt-state { color: var(--mon-muted); }
.ksb-receipt-state.is-ok { color: var(--mon-green); }
.ksb-receipt-state.is-bad { color: var(--mon-danger); }
.ksb-receipt-meta { min-width: 0; overflow: hidden; color: var(--mon-faint); text-overflow: ellipsis; white-space: nowrap; }
.ksb-receipt-note { grid-column: 1 / -1; color: var(--mon-muted); font-size: 10px; overflow-wrap: anywhere; }

.ksb-diag { display: flex; flex-direction: column; gap: 4px; margin-top: 8px; padding-top: 7px; border-top: 1px solid var(--mon-line); }
.ksb-diag-list { display: flex; flex-direction: column; gap: 2px; margin: 0; }
.ksb-diag-row { display: grid; grid-template-columns: 52px minmax(0, 1fr); gap: 6px; font-size: 10px; line-height: 1.45; }
.ksb-diag-row dt { color: var(--mon-faint); }
.ksb-diag-row dd { margin: 0; color: var(--mon-muted); overflow-wrap: anywhere; }

.ksb-pet { display: flex; align-items: center; gap: 6px; min-width: 0; }
.ksb-pet-svg { flex: none; width: 26px; height: 26px; overflow: visible; }
.ksb-pet-body { fill: color-mix(in srgb, var(--mon-accent) 16%, transparent); stroke: var(--mon-accent); stroke-width: 1.4; }
.ksb-pet-eye { fill: var(--mon-text); transform-origin: center; }
.ksb-pet-mouth { stroke: var(--mon-text); stroke-width: 1.2; fill: none; stroke-linecap: round; }
.ksb-pet-mood { min-width: 0; color: var(--mon-faint); font-size: 10px; line-height: 1.35; }
.ksb-pet.is-warn .ksb-pet-body { stroke: var(--mon-warn); fill: color-mix(in srgb, var(--mon-warn) 16%, transparent); }
.ksb-pet.is-warn .ksb-pet-mood { color: var(--mon-warn); }
@media (prefers-reduced-motion: no-preference) {
  .ksb-pet-svg { animation: ksb-bob 4.8s ease-in-out infinite; }
  .ksb-pet-eye { animation: ksb-blink 5.6s steps(1, end) infinite; }
}
.ksb-mini-stats { display: flex; align-items: baseline; gap: 8px; min-width: 0; overflow: hidden; }
.ksb-mini-stat { display: inline-flex; align-items: baseline; gap: 3px; min-width: 0; white-space: nowrap; }
.ksb-mini-stat .ksb-stat-label { font-size: 9px; }
.ksb-mini-stat .ksb-stat-value { font-size: 11px; }
.ekd-mon.is-mini .ksb-region,
.ekd-mon.is-mini .ksb-diag,
.ekd-mon.is-mini .ksb-banner,
.ekd-mon.is-mini .ksb-pet { display: none; }
.ekd-mon:not(.is-mini) .ksb-mini-stats { display: none; }

@container (max-width: 190px) {
  .ekd-mon .ksb-stat-value { font-size: 13px; }
  .ksb-reset { display: none; }
  .ksb-agent-row { grid-template-columns: minmax(0, 1fr) auto; }
  .ksb-agent-idle { display: none; }
}
@container ksb-quota (max-width: 74px) {
  .ksb-quota-head { flex-wrap: wrap; }
  .ksb-reset { display: none; }
}
@media (prefers-reduced-motion: reduce) {
  .ekd-mon .ksb-chevron,
  .ekd-mon .ksb-progress-fill,
  .ekd-mon .ksb-header-stat { transition: none; }
  .ekd-mon.is-busy .ksb-refresh-icon { animation: none; }
}
@keyframes ksb-spin { to { transform: rotate(360deg); } }
@keyframes ksb-bob { 0%, 100% { transform: translateY(0); } 50% { transform: translateY(-1.5px); } }
@keyframes ksb-blink { 0%, 92%, 100% { transform: scaleY(1); } 94%, 98% { transform: scaleY(0.1); } }
`;

function node(tag, className, text) {
  const element = document.createElement(tag);
  if (className) element.className = className;
  if (text !== undefined) element.textContent = text;
  return element;
}

function icon(paths) {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 16 16");
  svg.setAttribute("width", "11");
  svg.setAttribute("height", "11");
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("focusable", "false");
  const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
  path.setAttribute("d", paths);
  path.setAttribute("fill", "none");
  path.setAttribute("stroke", "currentColor");
  path.setAttribute("stroke-width", "1.6");
  path.setAttribute("stroke-linecap", "round");
  path.setAttribute("stroke-linejoin", "round");
  svg.append(path);
  return svg;
}

function petSvg() {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 32 32");
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("focusable", "false");
  svg.classList.add("ksb-pet-svg");
  const body = document.createElementNS("http://www.w3.org/2000/svg", "path");
  body.setAttribute("class", "ksb-pet-body");
  // 自绘的圆润几何体：两耳上扬的卵形，不是官方 Kimi 商标素材。
  body.setAttribute("d", "M16 3.4c3.1 0 5.1 1.5 6.2 3.3 1.1-.4 2.4-.2 2.9 1 .4 1-.2 2-1.1 2.5 2 1.7 3.1 4.3 3.1 7.2 0 5.6-4.7 9.9-11.1 9.9S4.9 23 4.9 17.4c0-2.9 1.1-5.5 3.1-7.2-.9-.5-1.5-1.5-1.1-2.5.5-1.2 1.8-1.4 2.9-1C11 4.9 12.9 3.4 16 3.4Z");
  const left = document.createElementNS("http://www.w3.org/2000/svg", "circle");
  left.setAttribute("class", "ksb-pet-eye");
  left.setAttribute("cx", "12.4");
  left.setAttribute("cy", "16.2");
  left.setAttribute("r", "1.5");
  const right = document.createElementNS("http://www.w3.org/2000/svg", "circle");
  right.setAttribute("class", "ksb-pet-eye");
  right.setAttribute("cx", "19.6");
  right.setAttribute("cy", "16.2");
  right.setAttribute("r", "1.5");
  const mouth = document.createElementNS("http://www.w3.org/2000/svg", "path");
  mouth.setAttribute("class", "ksb-pet-mouth");
  mouth.setAttribute("d", "M14.3 20.2c1 .9 2.4.9 3.4 0");
  svg.append(body, left, right, mouth);
  return svg;
}

export async function activate(ctx) {
  if (typeof document === "undefined") return;
  const hub = settingsHub(ctx);
  const state = {
    settings: DEFAULT_SETTINGS,
    summary: null,
    history: null,
    sessions: [],
    receipts: [],
    errors: [],
    loadedAt: null,
    busy: false,
    detail: false,
  };
  let section = null;
  let shadow = null;
  let disposed = false;

  try {
    state.settings = normalizeSettings(await ctx.storage.get(STORAGE_KEY));
  } catch {
    state.settings = normalizeSettings(null);
  }

  async function persist(next) {
    state.settings = mergeSettings(state.settings, next);
    try {
      await ctx.storage.set(STORAGE_KEY, state.settings);
    } catch (error) {
      ctx.log("warn", `Monitor 设置未能保存：${error?.message || error}`);
    }
    hub.publish(state.settings);
  }

  const refreshNow = () => {
    reportBusy(true);
    refresher.refresh();
  };

  async function load() {
    const tasks = [
      ["summary", ctx.core.request("cache.summary")],
      ["sessions", ctx.core.request("cache.sessions", { limit: 20 })],
      ["receipts", ctx.core.request("cache.receipts", { limit: 3 })],
      ["history", ctx.core.request("cache.history", { range: state.settings.range })],
    ];
    const settled = await Promise.allSettled(tasks.map(([, promise]) => promise));
    const result = { errors: [] };
    settled.forEach((outcome, index) => {
      const name = tasks[index][0];
      if (outcome.status === "fulfilled") result[name] = outcome.value;
      else result.errors.push({ action: name, message: outcome.reason?.message || String(outcome.reason) });
    });
    return result;
  }

  function apply(result) {
    if (result.summary) state.summary = result.summary;
    if (Array.isArray(result.sessions)) state.sessions = result.sessions;
    if (Array.isArray(result.receipts)) state.receipts = result.receipts;
    if (result.history) state.history = result.history;
    state.errors = result.errors;
    state.loadedAt = new Date();
    state.busy = false;
    render();
  }

  function fail(error) {
    state.errors = [{ action: "刷新", message: error?.message || String(error) }];
    state.busy = false;
    render();
  }

  const refresher = createRefresher({
    load,
    apply,
    fail,
    minGap: 10_000,
    fallback: 60_000,
    // 侧栏不可见时不发请求；读可见性不算监听器。
    setTimer: (fn, ms) => window.setTimeout(() => {
      if (typeof document !== "undefined" && document.visibilityState === "hidden") return;
      fn();
    }, ms),
    clearTimer: handle => window.clearTimeout(handle),
  });
  ctx.onDispose(() => refresher.dispose());

  const unsubscribeCache = ctx.events.on("cache", () => refresher.trigger());
  ctx.onDispose(() => unsubscribeCache.dispose());
  const unsubscribeAutomation = ctx.events.on("automation", () => refresher.trigger());
  ctx.onDispose(() => unsubscribeAutomation.dispose());

  function onSettings(next) {
    const previousRange = state.settings.range;
    state.settings = normalizeSettings(next);
    render();
    if (state.settings.range !== previousRange) refresher.trigger();
  }
  hub.listeners.add(onSettings);
  ctx.onDispose(() => hub.listeners.delete(onSettings));

  function render() {
    if (!section || !shadow) return;
    const focusKey = shadow.activeElement?.getAttribute?.("data-focus") || "";
    section.classList.toggle("is-mini", state.settings.mini);
    section.classList.toggle("is-busy", state.busy);
    section.setAttribute("aria-busy", state.busy ? "true" : "false");
    section.replaceChildren(headerBlock(), ...bodyBlocks());
    if (focusKey) shadow.querySelector(`[data-focus="${focusKey}"]`)?.focus({ preventScroll: true });
  }

  function headerBlock() {
    const header = node("header", "ksb-header");
    const view = quotaView(state.summary?.quota);
    const agents = subagentModel(state.sessions);
    const failed = state.errors.length > 0;
    const statusWord = failed
      ? "读取失败"
      : !state.summary
        ? "读取中"
        : view.state === "EXHAUSTED"
          ? "等待重置"
          : agents.openTotal > 0
            ? `等待 ${agents.openTotal}`
            : view.known
              ? "已同步"
              : "未知";
    const dot = node("span", "ksb-status-dot");
    dot.classList.add(failed ? "ksb-dot-off" : view.state === "EXHAUSTED" ? "ksb-dot-warn" : "ksb-dot-live");
    dot.setAttribute("aria-hidden", "true");
    const title = node("span", "ksb-title", "Monitor");
    const status = node("span", "ksb-agent-status", statusWord);

    const refresh = node("button", "ksb-header-stat");
    refresh.type = "button";
    refresh.dataset.focus = "refresh";
    refresh.title = `上次刷新 ${state.loadedAt ? clockText(state.loadedAt) : "尚未完成"}`;
    refresh.setAttribute("aria-label", "刷新缓存指标");
    const spinner = node("span", "ksb-refresh-icon");
    spinner.append(icon("M13.4 8a5.4 5.4 0 1 1-1.6-3.85M13.3 1.7v3.1h-3.1"));
    refresh.append(spinner, node("span", "ksb-header-stat-value", state.loadedAt ? clockText(state.loadedAt) : "--:--"));
    refresh.addEventListener("click", refreshNow);

    const toggle = node("button", "ksb-icon-btn");
    toggle.type = "button";
    toggle.dataset.focus = "toggle";
    toggle.setAttribute("aria-expanded", String(!state.settings.mini));
    toggle.setAttribute("aria-label", state.settings.mini ? "展开 Monitor" : "收起 Monitor");
    toggle.title = state.settings.mini ? "展开 Monitor" : "收起为迷你模式";
    const chevron = node("span", "ksb-chevron");
    chevron.append(icon("m4 6 4 4 4-4"));
    toggle.append(chevron);
    toggle.addEventListener("click", () => void persist({ mini: !state.settings.mini }));

    header.append(dot, title, status, refresh, toggle);
    return header;
  }

  function bodyBlocks() {
    if (state.settings.mini) return [miniBlock()];
    const blocks = [];
    if (state.errors.length) blocks.push(errorBanner());
    // One grid for every module, so the two columns share a rhythm instead of each
    // section sizing its own. The cards are ordinary sections inside it, not a
    // card per row.
    const modules = [...quotaModules(), ...metricModules(), ...trendModules(), ...agentsModules(), ...petModules()];
    if (modules.length) {
      const region = node("div", "ksb-region");
      region.append(...modules);
      blocks.push(region);
    }
    if (state.detail) blocks.push(detailBlock());
    blocks.push(detailToggle());
    return blocks;
  }

  function miniBlock() {
    const row = node("div", "ksb-mini-stats");
    const cards = metricCards(state.summary);
    for (const card of [cards[2], cards[3]]) {
      const item = node("span", "ksb-mini-stat");
      item.append(node("span", "ksb-stat-label", card.label));
      const value = node("span", "ksb-stat-value", card.value);
      if (!card.known) value.classList.add("is-unknown");
      item.append(value);
      row.append(item);
    }
    const view = quotaView(state.summary?.quota);
    const five = view.windows.find(window => window.key === "limit5h");
    const quotaItem = node("span", "ksb-mini-stat");
    quotaItem.append(node("span", "ksb-stat-label", "5 小时"));
    const quotaValue = node("span", "ksb-stat-value", five && five.percent !== null ? `${five.percent}%` : "--");
    if (!five || five.percent === null) quotaValue.classList.add("is-unknown");
    quotaItem.append(quotaValue);
    row.append(quotaItem);
    return row;
  }

  function errorBanner() {
    const banner = node("div", "ksb-banner");
    banner.setAttribute("role", "alert");
    banner.append(node("strong", "", "缓存指标暂时读不到"));
    const first = state.errors[0];
    banner.append(node("small", "", `${first.action}：${first.message}`));
    const retry = node("button", "ksb-banner-action", "重试");
    retry.type = "button";
    retry.dataset.focus = "retry";
    retry.addEventListener("click", refreshNow);
    banner.append(retry);
    return banner;
  }

  function quotaModules() {
    const view = quotaView(state.summary?.quota);
    const modules = [];
    if (!view.windows.length) {
      const module = node("div", "ksb-module ksb-span-2");
      const note = node("p", "ksb-footnote");
      note.setAttribute("role", "status");
      note.textContent = view.note || (view.state === "UNKNOWN" ? "额度窗口未知，官方未返回可用的额度数据。" : "没有可显示时段的额度窗口。");
      module.append(note);
      return [module];
    }
    for (const window of view.windows) {
      const module = node("div", "ksb-module");
      const group = node("div", "ksb-quota-group");
      const head = node("div", "ksb-quota-head");
      head.append(node("span", "ksb-quota-label", window.label));
      if (window.reset) head.append(node("span", "ksb-reset", window.reset.short));
      const level = window.percent !== null && window.percent >= 90 ? "high" : window.percent !== null && window.percent >= 60 ? "mid" : "low";
      const percent = node("span", `ksb-quota-pct ${level}`, window.percent === null ? "--" : `${window.percent}%`);
      if (window.percent === null) percent.classList.add("is-unknown");
      head.append(percent);
      const detail = [window.percent === null ? "已用比例未知" : `已用 ${window.percent}%`, window.reset?.long].filter(Boolean).join("，");
      const track = node("div", `ksb-progress${window.percent === null ? " is-unknown" : ""}`);
      track.setAttribute("role", "img");
      track.setAttribute("aria-label", `${window.label} ${detail}`);
      const fill = node("div", `ksb-progress-fill ${level}`);
      fill.style.transform = `scaleX(${window.percent === null ? 0 : window.percent / 100})`;
      track.append(fill);
      group.append(head, track);
      module.append(group);
      modules.push(module);
    }
    if (view.state === "EXHAUSTED" || view.state === "UNKNOWN") {
      const module = node("div", "ksb-module ksb-span-2");
      const note = node("p", "ksb-footnote", view.state === "EXHAUSTED" ? "额度已用尽，重置后自动刷新。" : view.note || "额度状态未知。");
      note.setAttribute("role", "status");
      module.append(note);
      modules.push(module);
    }
    return modules;
  }

  function metricModules() {
    const module = node("div", "ksb-module ksb-span-2");
    const grid = node("div", "ksb-stat-grid");
    for (const card of metricCards(state.summary)) {
      const stat = node("div", "ksb-stat");
      stat.append(node("span", "ksb-stat-label", card.label));
      const value = node("span", `ksb-stat-value${card.known ? "" : " is-unknown"}`, card.value);
      if (!card.known) value.setAttribute("aria-label", `${card.label}：未知`);
      stat.append(value);
      if (card.note) stat.append(node("span", "ksb-value-note", card.note));
      grid.append(stat);
    }
    module.append(grid);
    return [module];
  }

  function trendModules() {
    const model = trendModel(state.history);
    const module = node("div", "ksb-module ksb-span-2 ksb-trend");
    const head = node("div", "ksb-section-head");
    head.append(node("span", "ksb-quota-label", "命中率趋势"), node("span", "ksb-tag is-plain", "近 24 小时"));
    module.append(head);
    if (!model.drawable) {
      module.append(node("p", "ksb-footnote", model.samples === 0 ? "还没有可绘制命中率的样本，供应商未上报时不会画线。" : "命中率样本不足两个，暂不连线以免误导。"));
      return [module];
    }
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("class", "ksb-spark");
    svg.setAttribute("viewBox", "0 0 100 30");
    svg.setAttribute("preserveAspectRatio", "none");
    svg.setAttribute("role", "img");
    svg.setAttribute("aria-label", "近 24 小时命中率趋势");
    const base = document.createElementNS("http://www.w3.org/2000/svg", "line");
    base.setAttribute("class", "ksb-spark-base");
    base.setAttribute("x1", "0");
    base.setAttribute("x2", "100");
    base.setAttribute("y1", "26");
    base.setAttribute("y2", "26");
    svg.append(base);
    for (const run of model.runs) {
      if (run.length > 1) {
        const line = document.createElementNS("http://www.w3.org/2000/svg", "polyline");
        line.setAttribute("class", "ksb-spark-run");
        line.setAttribute("points", run.map(point => `${point[0]},${point[1]}`).join(" "));
        svg.append(line);
      }
      const last = run[run.length - 1];
      const dot = document.createElementNS("http://www.w3.org/2000/svg", "circle");
      dot.setAttribute("class", "ksb-spark-dot");
      dot.setAttribute("cx", String(last[0]));
      dot.setAttribute("cy", String(last[1]));
      dot.setAttribute("r", "1.3");
      svg.append(dot);
    }
    module.append(svg);
    module.append(node("p", "ksb-footnote", "每个点是一次真实上报；断开的区间表示该小时供应商未上报。"));
    return [module];
  }

  function agentsModules() {
    const model = subagentModel(state.sessions);
    const module = node("div", "ksb-module ksb-span-2 ksb-agents");
    if (!model.rows.length) {
      // Nothing is waiting: stay silent in the compact view and say so in detail.
      if (!state.detail) return [];
      const head = node("div", "ksb-section-head");
      head.append(node("span", "ksb-quota-label", "后台子代理"), node("span", "ksb-tag is-plain", "无"));
      module.append(head, node("p", "ksb-agents-empty", "没有等待中的后台子代理。"));
      return [module];
    }
    const head = node("div", "ksb-section-head");
    head.append(node("span", "ksb-quota-label", "后台子代理"));
    head.append(node("span", "ksb-tag is-live", `${model.waiting} 个会话 · ${model.openTotal} 个`));
    module.append(head);
    for (const row of model.rows) {
      const line = node("div", "ksb-agent-row");
      line.setAttribute("role", "listitem");
      const name = node("span", "ksb-agent-name", row.title);
      name.title = `${row.title} · ${row.model}`;
      const count = node("span", "ksb-agent-count", row.blocked ? "阻塞等待" : `${row.open} 个`);
      if (row.blocked) count.classList.add("ksb-agent-blocked");
      const idle = node("span", "ksb-agent-idle", row.idle);
      line.append(name, count, idle);
      module.append(line);
    }
    module.append(node("p", "ksb-footnote", "会话级计数来自本机缓存记录；插件拿不到逐个代理的名称，因此不展示虚构的代理行。"));
    return [module];
  }

  function petModules() {
    if (!state.settings.pet) return [];
    const view = quotaView(state.summary?.quota);
    const agents = subagentModel(state.sessions);
    const card = metricCards(state.summary)[2];
    const mood = view.state === "EXHAUSTED"
      ? { text: "额度用尽，等重置", warn: true }
      : agents.openTotal > 0
        ? { text: `后台还有 ${agents.openTotal} 个代理在跑`, warn: false }
        : state.errors.length
          ? { text: "指标读取失败", warn: true }
          : card.known
            ? { text: `今日命中 ${card.value}`, warn: false }
            : { text: "等第一个缓存样本", warn: false };
    const module = node("div", "ksb-module ksb-span-2 ksb-pet");
    if (mood.warn) module.classList.add("is-warn");
    module.append(petSvg(), node("span", "ksb-pet-mood", mood.text));
    return [module];
  }

  function detailToggle() {
    const bar = node("div", "ksb-diag");
    const toggle = node("button", "ksb-icon-btn ksb-detail-toggle");
    toggle.type = "button";
    toggle.dataset.focus = "detail";
    toggle.setAttribute("aria-expanded", String(state.detail));
    // 只有展开时才指向真实的子树，收起时不留下悬空的 aria-controls。
    if (state.detail) toggle.setAttribute("aria-controls", "ekd-mon-detail");
    const chevron = node("span", "ksb-chevron");
    chevron.append(icon("m4 6 4 4 4-4"));
    toggle.append(chevron, node("span", "", state.detail ? "收起历史与详情" : "历史、回执与详情"));
    toggle.addEventListener("click", () => {
      state.detail = !state.detail;
      render();
    });
    bar.append(toggle);
    return bar;
  }

  function detailBlock() {
    // The subagent list is not repeated here: bodyBlocks already renders it above,
    // and an open detail panel must not show the same waiting sessions twice.
    const block = node("div", "ksb-diag");
    block.id = "ekd-mon-detail";
    block.append(historySection(), receiptSection(), diagnosticsSection());
    return block;
  }

  function historySection() {
    const wrap = node("div", "ksb-history");
    const head = node("div", "ksb-section-head");
    head.append(node("span", "ksb-quota-label", "历史用量"));
    const rangeSwitch = node("div", "ksb-range");
    rangeSwitch.setAttribute("role", "group");
    rangeSwitch.setAttribute("aria-label", "统计窗口");
    for (const item of RANGES) {
      const button = node("button", "", item);
      button.type = "button";
      button.dataset.focus = `range-${item}`;
      button.setAttribute("aria-pressed", String(state.settings.range === item));
      button.title = `按${RANGE_LABEL[item]}统计`;
      button.addEventListener("click", () => void persist({ range: item }));
      rangeSwitch.append(button);
    }
    head.append(rangeSwitch);
    wrap.append(head);
    const current = state.history && state.history.range === state.settings.range ? state.history : null;
    const model = historyModel(current);
    if (model.empty) {
      wrap.append(node("p", "ksb-footnote", current ? "这个区间还没有用量记录。" : "正在读取历史用量。"));
      return wrap;
    }
    const bars = node("div", "ksb-bars");
    bars.setAttribute("role", "img");
    bars.setAttribute("aria-label", `${RANGE_LABEL[state.settings.range]}输入 token 分布，区间命中率 ${model.hitRate}`);
    for (const bar of model.bars) {
      const cell = node("span", "ksb-bar");
      cell.title = `${bar.bucket}：${bar.empty ? "无样本" : `${tokenText(bar.total)} 输入 · 命中 ${bar.hitRate}`}`;
      if (!bar.empty && bar.height > 0) {
        const fill = node("span", "ksb-bar-fill");
        fill.style.height = `${bar.height}%`;
        cell.append(fill);
      }
      bars.append(cell);
    }
    wrap.append(bars);
    const totals = node("div", "ksb-totals");
    for (const [label, value] of [["命中率", model.hitRate], ["读取", model.read], ["未命中", model.miss], ["输出", model.output]]) {
      const item = node("span", "");
      item.append(document.createTextNode(`${label} `));
      item.append(node("strong", "", value));
      totals.append(item);
    }
    wrap.append(totals);
    wrap.append(node("p", "ksb-footnote", "柱高是各区间的输入 token 相对值；无样本的区间不画柱，不按 0 处理。"));
    return wrap;
  }

  function receiptSection() {
    const wrap = node("div", "ksb-receipts");
    const head = node("div", "ksb-section-head");
    head.append(node("span", "ksb-quota-label", "最近执行记录"), node("span", "ksb-tag is-plain", state.receipts.length ? `${state.receipts.length} 条` : "暂无"));
    wrap.append(head);
    const receipts = receiptModel(state.receipts);
    if (!receipts.length) {
      wrap.append(node("p", "ksb-footnote", "还没有保活、压缩或恢复记录。"));
      return wrap;
    }
    for (const receipt of receipts) {
      const row = node("div", "ksb-receipt");
      const stateText = node("span", `ksb-receipt-state${receipt.state.includes("成功") || receipt.state.includes("已完成") ? " is-ok" : receipt.state.includes("无效") || receipt.state.includes("失败") ? " is-bad" : ""}`, receipt.state);
      row.append(stateText, node("span", "ksb-receipt-meta", `${receipt.kind} · ${receipt.session}`));
      if (receipt.note) row.append(node("span", "ksb-receipt-note", receipt.note));
      wrap.append(row);
    }
    wrap.append(node("p", "ksb-footnote", "“官方已接收”不等于“官方已完成”；回执不代表缓存命中率。"));
    return wrap;
  }

  function diagnosticsSection() {
    const dl = node("dl", "ksb-diag-list");
    const add = (term, detail) => {
      const row = node("div", "ksb-diag-row");
      row.append(node("dt", "", term), node("dd", "", detail));
      dl.append(row);
    };
    const view = quotaView(state.summary?.quota);
    add("额度", `${view.state}${view.source ? ` · ${view.source}` : ""}`);
    if (view.note) add("说明", view.note);
    const cards = metricCards(state.summary);
    add("样本", state.summary ? `今日 ${state.summary.today?.steps ?? 0} 步 · 近 1 小时 ${state.summary.hour?.steps ?? 0} 步` : "尚未读取");
    add("缓存", state.summary?.reportingNote || (cards[2].known ? "命中率来自供应商上报" : "供应商未上报缓存命中数据"));
    if (state.errors.length) add("错误", state.errors.map(item => `${item.action}: ${item.message}`).join("；"));
    add("数据", "本机缓存指标，与“缓存与额度”页同源；不读会话正文。");
    add("许可", "结构参考 bfjnbvf/kimi-code-monitor · MIT");
    return dl;
  }

  function reportBusy(value) {
    state.busy = value;
    render();
  }

  const mount = ctx.ui.mount("sidebar", element => {
    const host = document.createElement("div");
    host.className = "ekdp-mon";
    shadow = host.attachShadow({ mode: "open" });
    const style = document.createElement("style");
    style.textContent = STYLE;
    section = node("section", "ekd-mon");
    section.setAttribute("aria-label", "Monitor 缓存与额度");
    section.setAttribute("role", "region");
    shadow.append(style, section);
    element.append(host);
    reportBusy(true);
    refresher.start();
    return () => {
      disposed = true;
      refresher.dispose();
      hub.listeners.delete(onSettings);
      unsubscribeCache.dispose();
      unsubscribeAutomation.dispose();
      host.remove();
      section = null;
      shadow = null;
    };
  });
  ctx.onDispose(() => mount.dispose());
  return { status: "active" };
}
