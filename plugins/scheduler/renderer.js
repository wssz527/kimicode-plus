// 定时任务插件 · 预装，默认关闭
//
// 本文件保持单文件 ESM，供宿主通过 blob URL 直接导入；纯函数同时导出供 Node 单元测试使用。
// 全程使用 createElement / createElementNS / textContent 构建 DOM 与 SVG，兼容 Trusted Types。

export const PLUGIN_ID = "kimicode-plus.scheduler";

export const SCHEDULER_ICON = "M12 6v6l4 2m6-2a10 10 0 1 1-20 0 10 10 0 0 1 20 0Z";

export const READONLY_TEMPLATES = [
  {
    id: "daily-diff",
    label: "每日代码变更摘要",
    name: "每日代码变更摘要",
    prompt: "只读分析当前仓库最近 24 小时内的代码与提交变更（如 git log / git diff），总结核心改动、潜在风险与未覆盖测试，不要修改任何文件。",
  },
  {
    id: "todo-fixme",
    label: "TODO 与 FIXME 汇总",
    name: "TODO 与 FIXME 汇总",
    prompt: "只读扫描项目中的 TODO、FIXME 与待办注释，按模块和紧急程度汇总列表并指出位置，不要修改代码。",
  },
  {
    id: "deps-audit",
    label: "依赖更新与安全公告检查",
    name: "依赖更新与安全公告检查",
    prompt: "只读检查项目依赖清单与锁文件（如 package.json、go.mod 等），列出可疑安全风险、过期依赖及升级建议，不要直接修改依赖配置。",
  },
  {
    id: "test-patrol",
    label: "测试失败巡检",
    name: "测试失败巡检",
    prompt: "只读检查项目测试状态与近期失败用例，定位可能原因与相关文件，不要改动源码。",
  },
];

export const PERMISSION_MODES = [
  { value: "manual", label: "始终询问", note: "工具执行与文件修改前需人工批准" },
  { value: "auto", label: "自动判断", note: "常规操作自动放行，高风险操作仍需人工批准" },
  { value: "yolo", label: "始终允许（免审批）", note: "读写文件与终端命令自动执行，仅交互式提问时暂停" },
];

export const RUN_STATUS_LABELS = {
  queued: "排队中",
  running: "运行中",
  completed: "已完成",
  failed: "失败",
  awaiting_approval: "等待批准",
  skipped: "已跳过",
};

export const FAILURE_KIND_LABELS = {
  preflight_failed: "校验失败",
  quota_insufficient: "额度不足",
  concurrency_limit: "并发上限",
  timeout: "运行超时",
  max_turns: "超过最大回合",
  overlap: "重叠跳过",
  missed: "错过跳过",
  busy_timeout: "忙碌超时",
  cancelled: "已取消",
  execution_failed: "执行失败",
};

export const PROMPT_VARIABLES = [
  { token: "{{date}}", label: "当前日期", hint: "YYYY-MM-DD" },
  { token: "{{time}}", label: "当前时间", hint: "HH:MM" },
  { token: "{{last_run_at}}", label: "上次运行时间", hint: "YYYY-MM-DD HH:MM 或暂无" },
  { token: "{{last_run_summary}}", label: "上次运行摘要", hint: "上次结果摘要或暂无" },
];

export const TRIGGER_LABELS = {
  scheduled: "定时",
  catchup: "补跑",
  manual: "手动",
};

const FOLDER_ICON = "M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7Z";
const REFRESH_ICON = "M4 4v5h5M20 20v-5h-5M4 9a9 9 0 0 1 14.85-3.36L20 7M20 15a9 9 0 0 1-14.85 3.36L4 17";
const BACK_ICON = "M19 12H5m6-6-6 6 6 6";

const WEEKDAY_ITEMS = [
  { value: 1, short: "一", label: "周一" },
  { value: 2, short: "二", label: "周二" },
  { value: 3, short: "三", label: "周三" },
  { value: 4, short: "四", label: "周四" },
  { value: 5, short: "五", label: "周五" },
  { value: 6, short: "六", label: "周六" },
  { value: 0, short: "日", label: "周日" },
];

const SCHEDULE_TABS = [
  { value: "daily", label: "每天" },
  { value: "weekly", label: "每周" },
  { value: "times", label: "每日多时点" },
  { value: "interval", label: "固定间隔" },
  { value: "once", label: "单次执行" },
];

const LEGACY_SCHEDULE_LABELS = {
  hourly: "每小时",
  weekdays: "工作日",
  monthly: "每月",
};

const QUICK_TIMES = ["08:30", "09:00", "12:00", "18:00", "22:00"];
const QUICK_MULTI_TIMES = ["08:30", "12:00", "14:00", "18:00", "21:00"];
const INTERVAL_PRESETS = [
  { label: "15 分钟", value: 15, unit: "minutes" },
  { label: "30 分钟", value: 30, unit: "minutes" },
  { label: "1 小时", value: 1, unit: "hours" },
  { label: "2 小时", value: 2, unit: "hours" },
  { label: "6 小时", value: 6, unit: "hours" },
];

export function formatModelOptionLabel(model) {
  if (!model) return "";
  const parts = [model.name || model.id];
  if (model.provider) parts.push(model.provider);
  const caps = Array.isArray(model.capabilities) ? model.capabilities : [];
  const capLabels = [];
  if (caps.includes("image_in")) capLabels.push("图片");
  if (caps.includes("video_in")) capLabels.push("视频");
  if (caps.includes("thinking") || caps.includes("always_thinking") || model.adaptive_thinking) capLabels.push("思考");
  if (capLabels.length > 0) parts.push(capLabels.join("/"));
  return parts.join(" · ");
}

export function effortOptionsForModel(model) {
  if (!model) return [];
  const efforts = Array.isArray(model.support_efforts) ? model.support_efforts.filter(Boolean) : [];
  const caps = Array.isArray(model.capabilities) ? model.capabilities : [];
  const supportsThinking = efforts.length > 0 || caps.includes("thinking") || caps.includes("always_thinking") || model.adaptive_thinking === true;
  if (!supportsThinking) return [];
  const out = [{ value: "", label: "跟随默认" }];
  if (!caps.includes("always_thinking")) {
    out.push({ value: "off", label: "关闭思考 (off)" });
    if (efforts.length === 0) {
      out.push({ value: "on", label: "开启思考 (on)" });
    }
  }
  for (const effort of efforts) {
    out.push({ value: effort, label: effort });
  }
  return out;
}

export function keepAliveStatusText(task) {
  if (!task || task.mode !== "bound_session") return "";
  if (typeof task.sessionIdleMinutes !== "number" || Number.isNaN(task.sessionIdleMinutes)) {
    return "尚无主代理请求记录";
  }
  const mins = Math.max(0, Math.round(task.sessionIdleMinutes));
  const base = `距上次请求 ${mins} 分钟`;
  if (typeof task.keepAliveMinutes === "number" && task.keepAliveMinutes > 0) {
    const windowNote = task.withinKeepAlive
      ? `仍在用户设置的 ${task.keepAliveMinutes} 分钟保活窗口内`
      : `已超过用户设置的 ${task.keepAliveMinutes} 分钟保活窗口`;
    return `${base} · ${windowNote}`;
  }
  return base;
}

export function hasPromptVariables(template) {
  return /\{\{(date|time|last_run_at|last_run_summary)\}\}/.test(String(template || ""));
}

export function previewPromptTemplate(template, context = {}) {
  const source = String(template || "");
  if (!source) return "";
  const now = context.now instanceof Date && !Number.isNaN(context.now.getTime()) ? context.now : new Date();
  const yyyy = now.getFullYear();
  const mm = String(now.getMonth() + 1).padStart(2, "0");
  const dd = String(now.getDate()).padStart(2, "0");
  const hh = String(now.getHours()).padStart(2, "0");
  const min = String(now.getMinutes()).padStart(2, "0");
  let lastRunAtText = "暂无";
  const rawLastAt = typeof context.lastRunAt === "string" ? context.lastRunAt.trim() : "";
  if (rawLastAt) {
    const parsed = new Date(rawLastAt);
    if (!Number.isNaN(parsed.getTime())) {
      lastRunAtText = `${parsed.getFullYear()}-${String(parsed.getMonth() + 1).padStart(2, "0")}-${String(parsed.getDate()).padStart(2, "0")} ${String(parsed.getHours()).padStart(2, "0")}:${String(parsed.getMinutes()).padStart(2, "0")}`;
    } else {
      lastRunAtText = rawLastAt;
    }
  }
  const rawSummary = typeof context.lastRunSummary === "string" ? context.lastRunSummary.trim() : "";
  const values = {
    date: `${yyyy}-${mm}-${dd}`,
    time: `${hh}:${min}`,
    last_run_at: lastRunAtText,
    last_run_summary: rawSummary || "暂无",
  };
  return source.replace(/\{\{(date|time|last_run_at|last_run_summary)\}\}/g, (_, key) => values[key]);
}

function localDateOffset(days = 0) {
  const d = new Date();
  d.setDate(d.getDate() + days);
  const yyyy = d.getFullYear();
  const mm = String(d.getMonth() + 1).padStart(2, "0");
  const dd = String(d.getDate()).padStart(2, "0");
  return `${yyyy}-${mm}-${dd}`;
}

export function normalizeClock(value, fallback = "09:00") {
  const match = /^(\d{1,2}):(\d{1,2})$/.exec(String(value || "").trim());
  if (!match) return fallback;
  const hh = Math.max(0, Math.min(23, Number.parseInt(match[1], 10)));
  const mm = Math.max(0, Math.min(59, Number.parseInt(match[2], 10)));
  return `${String(hh).padStart(2, "0")}:${String(mm).padStart(2, "0")}`;
}

export function parseTimesList(timesOrText) {
  const rawItems = Array.isArray(timesOrText)
    ? timesOrText
    : String(timesOrText || "").split(/[,，\s]+/);
  const seen = new Set();
  const valid = [];
  for (const item of rawItems) {
    const trimmed = String(item || "").trim();
    if (!trimmed) continue;
    const match = /^(\d{1,2}):(\d{1,2})$/.exec(trimmed);
    if (!match) continue;
    const hh = Number.parseInt(match[1], 10);
    const mm = Number.parseInt(match[2], 10);
    if (hh < 0 || hh > 23 || mm < 0 || mm > 59) continue;
    const norm = `${String(hh).padStart(2, "0")}:${String(mm).padStart(2, "0")}`;
    if (!seen.has(norm)) {
      seen.add(norm);
      valid.push(norm);
    }
  }
  valid.sort();
  return valid;
}

export function resolveDefaultCwd(prefillCwd, sessions = [], targetSessionId = "", activeSessionId = "") {
  const explicit = typeof prefillCwd === "string" ? prefillCwd.trim() : "";
  if (explicit) return explicit;
  const list = Array.isArray(sessions) ? sessions : [];
  if (list.length === 0) return "";
  const preferredId = targetSessionId || activeSessionId || "";
  if (preferredId) {
    const matched = list.find(s => s && s.id === preferredId && typeof s.cwd === "string" && s.cwd.trim());
    if (matched) return matched.cwd.trim();
  }
  const firstWithCwd = list.find(s => s && typeof s.cwd === "string" && s.cwd.trim());
  return firstWithCwd ? firstWithCwd.cwd.trim() : "";
}

function formatWeekdaysText(weekdays) {
  const selected = new Set(Array.isArray(weekdays) ? weekdays : []);
  const ordered = WEEKDAY_ITEMS.filter(item => selected.has(item.value));
  return ordered.map(item => item.label);
}

export function formatSchedulePreview(draft = {}) {
  const kind = draft.scheduleKind || "daily";
  const clock = normalizeClock(draft.time, "09:00");
  let core = "";
  switch (kind) {
    case "once": {
      const dateStr = String(draft.date || "").trim() || "未选日期";
      return `执行计划：${dateStr} ${clock} 单次运行，执行后自动停用`;
    }
    case "daily":
      core = `每天 ${clock} 自动运行`;
      break;
    case "weekdays":
      core = `每个工作日（周一至周五）${clock} 自动运行`;
      break;
    case "hourly": {
      const min = Math.max(0, Math.min(59, Number.parseInt(String(draft.minute ?? 0), 10) || 0));
      core = `每小时第 ${String(min).padStart(2, "0")} 分自动运行`;
      break;
    }
    case "weekly": {
      const names = formatWeekdaysText(draft.weekdays);
      if (names.length === 0) {
        core = `每周（请选择至少一天）${clock} 自动运行`;
      } else if (names.length === 7) {
        core = `每天（周一至周日）${clock} 自动运行`;
      } else {
        core = `每${names.join("、")} ${clock} 自动运行`;
      }
      break;
    }
    case "monthly": {
      const days = String(draft.daysText || "")
        .split(/[,，\s]+/)
        .map(item => Number.parseInt(item, 10))
        .filter(n => Number.isFinite(n) && n >= 1 && n <= 31);
      const parts = days.map(d => `${d} 日`);
      if (draft.lastDay) parts.push("最后一天");
      core = `每月 ${parts.length ? parts.join("、") : "指定日期"} ${clock} 自动运行`;
      break;
    }
    case "interval": {
      const val = Math.max(1, Number.parseInt(String(draft.intervalValue || 1), 10) || 1);
      const unit = draft.intervalUnit === "hours" ? "小时" : "分钟";
      let note = "";
      if (draft.intervalUnit !== "hours" && val >= 60 && val % 60 === 0) {
        note = `（每 ${val / 60} 小时）`;
      }
      if (draft.testMode) {
        note += "（测试模式）";
      }
      core = `每隔 ${val} ${unit}自动运行${note}`;
      break;
    }
    case "times": {
      const times = parseTimesList(Array.isArray(draft.times) ? draft.times : draft.timesText);
      const names = formatWeekdaysText(draft.weekdays);
      const prefix = names.length === 0 || names.length === 7 ? "每天" : `每${names.join("、")}`;
      if (times.length === 0) {
        core = `${prefix}多时点（请添加至少一个执行时间）`;
      } else {
        core = `${prefix} ${times.join("、")} 自动运行`;
      }
      break;
    }
    default:
      core = `${kind} 自动运行`;
  }

  const startDate = String(draft.startDate || "").trim();
  const endDate = String(draft.endDate || "").trim();
  let validity = "立即生效，长期持续有效";
  if (startDate && endDate) {
    validity = `${startDate} 至 ${endDate} 生效`;
  } else if (startDate) {
    validity = `${startDate} 起生效，长期有效`;
  } else if (endDate) {
    validity = `立即生效，至 ${endDate} 止`;
  }
  return `执行计划：${core} · ${validity}`;
}

export function defaultDraft(prefill = {}) {
  const scheduleKind = prefill.schedule?.kind || "daily";
  const hasExplicitWeekdays = Array.isArray(prefill.schedule?.weekdays);
  const weekdaysAutoDefault = !hasExplicitWeekdays && scheduleKind === "weekly";
  const initialTimes = Array.isArray(prefill.schedule?.times) && prefill.schedule.times.length
    ? parseTimesList(prefill.schedule.times)
    : ["09:00", "18:00"];
  return {
    id: prefill.id || "",
    name: prefill.name || "",
    prompt: prefill.prompt || "",
    cwd: prefill.cwd || "",
    mode: prefill.mode || (prefill.sessionId ? "bound_session" : "new_session"),
    sessionId: prefill.sessionId || "",
    model: prefill.model || "",
    effort: prefill.effort || "",
    permissionMode: prefill.permissionMode || "manual",
    missedPolicy: prefill.missedPolicy || "catchup",
    archiveOnRead: prefill.archiveOnRead !== undefined
      ? Boolean(prefill.archiveOnRead)
      : (prefill.mode || (prefill.sessionId ? "bound_session" : "new_session")) === "new_session",
    timeoutMinutes: typeof prefill.timeoutMinutes === "number" && prefill.timeoutMinutes >= 1
      ? Math.round(prefill.timeoutMinutes)
      : 30,
    maxTurns: typeof prefill.maxTurns === "number" && prefill.maxTurns >= 0
      ? Math.round(prefill.maxTurns)
      : 0,
    confirmDirtyYolo: Boolean(prefill.allowDirtyYolo),
    lastRunAt: prefill.lastRunAt || "",
    lastSummary: prefill.lastSummary || "",
    enabled: prefill.enabled !== undefined ? Boolean(prefill.enabled) : true,
    scheduleKind,
    date: prefill.schedule?.date || localDateOffset(0),
    time: normalizeClock(prefill.schedule?.time, "09:00"),
    minute: typeof prefill.schedule?.minute === "number" ? prefill.schedule.minute : 0,
    weekdays: hasExplicitWeekdays
      ? [...prefill.schedule.weekdays]
      : weekdaysAutoDefault
        ? [1]
        : [],
    weekdaysAutoDefault,
    daysText: Array.isArray(prefill.schedule?.daysOfMonth) && prefill.schedule.daysOfMonth.length
      ? prefill.schedule.daysOfMonth.join(",")
      : "1",
    lastDay: Boolean(prefill.schedule?.lastDay),
    intervalValue: prefill.schedule?.intervalValue || 30,
    intervalUnit: prefill.schedule?.intervalUnit || "minutes",
    testMode: Boolean(prefill.schedule?.testMode),
    timesText: (initialTimes.length ? initialTimes : ["09:00", "18:00"]).join(", "),
    startDate: prefill.schedule?.startDate || "",
    endDate: prefill.schedule?.endDate || "",
  };
}

export function switchScheduleKind(draft, nextKind) {
  draft.scheduleKind = nextKind;
  if (nextKind === "weekly") {
    if (!Array.isArray(draft.weekdays) || draft.weekdays.length === 0) {
      draft.weekdays = [1];
      draft.weekdaysAutoDefault = true;
    }
  } else if (draft.weekdaysAutoDefault) {
    draft.weekdays = [];
    draft.weekdaysAutoDefault = false;
  }
  return draft;
}

export function buildScheduleRule(draft) {
  const rule = { kind: draft.scheduleKind || "daily" };
  if (rule.kind !== "once") {
    if (draft.startDate) rule.startDate = String(draft.startDate).trim();
    if (draft.endDate) rule.endDate = String(draft.endDate).trim();
  }
  switch (rule.kind) {
    case "once":
      rule.date = String(draft.date || "").trim();
      rule.time = normalizeClock(draft.time, "09:00");
      break;
    case "hourly":
      rule.minute = Math.max(0, Math.min(59, Number.parseInt(String(draft.minute ?? 0), 10) || 0));
      break;
    case "daily":
    case "weekdays":
      rule.time = normalizeClock(draft.time, "09:00");
      break;
    case "weekly":
      rule.time = normalizeClock(draft.time, "09:00");
      rule.weekdays = Array.isArray(draft.weekdays) ? [...draft.weekdays] : [];
      break;
    case "monthly": {
      rule.time = normalizeClock(draft.time, "09:00");
      const rawDays = String(draft.daysText || "")
        .split(/[,，\s]+/)
        .map(item => Number.parseInt(item, 10))
        .filter(n => Number.isFinite(n) && n >= 1 && n <= 31);
      rule.daysOfMonth = rawDays;
      rule.lastDay = Boolean(draft.lastDay);
      break;
    }
    case "interval":
      rule.intervalValue = Math.max(1, Number.parseInt(String(draft.intervalValue || 1), 10) || 1);
      rule.intervalUnit = draft.intervalUnit === "hours" ? "hours" : "minutes";
      rule.testMode = Boolean(draft.testMode);
      break;
    case "times": {
      const source = Array.isArray(draft.times) ? draft.times : draft.timesText;
      const parsed = parseTimesList(source);
      if (parsed.length > 0) {
        rule.times = parsed;
      } else {
        rule.times = String(draft.timesText || "")
          .split(/[,，\s]+/)
          .map(item => item.trim())
          .filter(Boolean);
      }
      if (Array.isArray(draft.weekdays) && draft.weekdays.length > 0) {
        rule.weekdays = [...draft.weekdays];
      }
      break;
    }
  }
  return rule;
}

function formatDateTime(iso) {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

function el(doc, tag, props, children) {
  const node = doc.createElement(tag);
  for (const [key, value] of Object.entries(props || {})) {
    if (value === undefined || value === null) continue;
    if (key === "class") node.className = value;
    else if (key === "text") node.textContent = String(value);
    else if (key === "value") node.value = String(value);
    else if (key === "checked") node.checked = Boolean(value);
    else if (key === "disabled") node.disabled = Boolean(value);
    else if (key.startsWith("on") && typeof value === "function") node.addEventListener(key.slice(2).toLowerCase(), value);
    else node.setAttribute(key, String(value));
  }
  for (const child of children || []) {
    if (child === null || child === undefined || child === false) continue;
    node.append(typeof child === "string" ? doc.createTextNode(child) : child);
  }
  return node;
}

function makeSvg(doc, pathD, size = 14) {
  const svg = doc.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("width", String(size));
  svg.setAttribute("height", String(size));
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("fill", "none");
  svg.setAttribute("stroke", "currentColor");
  svg.setAttribute("stroke-width", "1.7");
  svg.setAttribute("stroke-linecap", "round");
  svg.setAttribute("stroke-linejoin", "round");
  svg.setAttribute("aria-hidden", "true");
  const path = doc.createElementNS("http://www.w3.org/2000/svg", "path");
  path.setAttribute("d", pathD);
  svg.append(path);
  return svg;
}

const SHARED_CSS = `
:host{
  display:flex;
  flex-direction:column;
  flex:1 1 auto;
  min-width:0;
  min-height:0;
  height:100%;
  --sched-bg:var(--color-bg, #ffffff);
  --sched-text:var(--color-text, #1f2328);
  --sched-muted:var(--color-text-muted, var(--color-text-secondary, color-mix(in srgb, var(--sched-text) 64%, transparent)));
  --sched-line:var(--color-line, color-mix(in srgb, var(--sched-text) 16%, transparent));
  --sched-fill:var(--color-fill-1, var(--panel, color-mix(in srgb, var(--sched-text) 6%, transparent)));
  --sched-hover:var(--color-hover, color-mix(in srgb, var(--sched-text) 8%, transparent));
  --sched-accent:var(--color-accent, var(--blue, #1783ff));
  --sched-accent-soft:var(--color-accent-soft, var(--soft, color-mix(in srgb, var(--sched-accent) 14%, transparent)));
  --sched-danger:var(--color-danger, var(--red, var(--danger, #dc2626)));
  --sched-ok:var(--color-done, var(--success, #16a34a));
  --sched-warn:var(--warn, #d97706);
  color:var(--sched-text);
  font:13.5px/1.5 var(--font-ui, system-ui, -apple-system, sans-serif);
}
*{box-sizing:border-box}
.sched-shell{
  display:flex;
  flex-direction:column;
  flex:1 1 auto;
  min-height:0;
  background:var(--sched-bg);
  -webkit-app-region:no-drag;
  position:relative;
  z-index:1;
}
button,input,select,textarea,.sched-tab{
  -webkit-app-region:no-drag;
  position:relative;
  z-index:1;
}
.sched-head{
  display:flex;
  align-items:center;
  justify-content:space-between;
  flex-wrap:wrap;
  gap:10px 16px;
  padding:14px 20px 12px;
  border-bottom:1px solid var(--sched-line);
  background:var(--sched-bg);
  -webkit-app-region:no-drag;
  position:relative;
  z-index:1;
}
.sched-head-main{
  display:flex;
  flex-direction:column;
  gap:2px;
  min-width:0;
  flex:1 1 220px;
}
.sched-head-title-row{
  display:flex;
  align-items:center;
  gap:8px;
  flex-wrap:wrap;
  min-width:0;
}
.sched-title{margin:0;font-size:16px;font-weight:600;letter-spacing:-0.01em}
.sched-subtitle{margin:0;font-size:12px;color:var(--sched-muted);line-height:1.45}
.sched-head-actions{
  margin-left:auto;
  display:flex;
  align-items:center;
  gap:8px;
  flex-wrap:wrap;
  flex-shrink:0;
  -webkit-app-region:no-drag;
  position:relative;
  z-index:1;
}
.sched-shell.compact .sched-head{padding:12px 14px 10px;gap:8px 10px}
.sched-shell.compact .sched-title{font-size:14.5px}
.sched-shell.compact .sched-tabs{padding:8px 14px 0}
.sched-shell.compact .sched-banner{margin:10px 14px 0}
.sched-shell.compact .sched-body{padding:12px 14px 18px}
.sched-tabs{
  display:flex;
  align-items:center;
  gap:6px;
  padding:8px 20px 0;
  border-bottom:1px solid var(--sched-line);
}
.sched-tab{
  position:relative;
  z-index:1;
  -webkit-app-region:no-drag;
  display:inline-flex;
  align-items:center;
  gap:6px;
  padding:7px 12px 9px;
  border:0;
  border-bottom:2px solid transparent;
  background:transparent;
  color:var(--sched-muted);
  font:inherit;
  font-size:13px;
  cursor:pointer;
}
.sched-tab:hover{color:var(--sched-text)}
.sched-tab[aria-selected="true"]{
  color:var(--sched-text);
  font-weight:600;
  border-bottom-color:var(--sched-accent);
}
.sched-badge{
  display:inline-flex;
  align-items:center;
  justify-content:center;
  min-width:18px;
  height:18px;
  padding:0 5px;
  border-radius:999px;
  background:var(--sched-fill);
  color:var(--sched-muted);
  font-size:11px;
  font-variant-numeric:tabular-nums;
}
.sched-badge.unread{
  background:var(--sched-accent);
  color:var(--color-text-on-accent, #fff);
}
.sched-banner{
  margin:10px 20px 0;
  padding:9px 12px;
  border-radius:8px;
  border:1px solid var(--sched-line);
  background:var(--sched-fill);
  font-size:12.5px;
  line-height:1.5;
  color:var(--sched-muted);
}
.sched-banner.warn{
  border-color:color-mix(in srgb, var(--sched-warn) 42%, transparent);
  background:color-mix(in srgb, var(--sched-warn) 12%, transparent);
  color:var(--sched-text);
}
.sched-banner.error{
  border-color:color-mix(in srgb, var(--sched-danger) 42%, transparent);
  background:color-mix(in srgb, var(--sched-danger) 12%, transparent);
  color:var(--sched-danger);
}
.sched-body{
  flex:1 1 auto;
  min-height:0;
  overflow-y:auto;
  padding:14px 20px 20px;
  display:flex;
  flex-direction:column;
  gap:10px;
}
.sched-toolbar{
  display:flex;
  align-items:center;
  justify-content:space-between;
  gap:8px;
  flex-wrap:wrap;
  padding-bottom:6px;
}
.sched-card{
  display:flex;
  flex-direction:column;
  gap:8px;
  padding:12px 14px;
  border:1px solid var(--sched-line);
  border-radius:10px;
  background:var(--sched-fill);
}
.sched-card.unread{
  border-color:var(--sched-accent);
}
.sched-card-top{display:flex;align-items:center;gap:8px;min-width:0;flex-wrap:wrap}
.sched-card-title{font-weight:600;font-size:13.5px;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.sched-pill{
  display:inline-flex;
  align-items:center;
  gap:4px;
  height:20px;
  padding:0 7px;
  border-radius:999px;
  font-size:11px;
  background:var(--sched-fill);
  color:var(--sched-muted);
  flex:none;
}
.sched-pill.ok{background:color-mix(in srgb, var(--sched-ok) 15%, transparent);color:var(--sched-ok)}
.sched-pill.warn{background:color-mix(in srgb, var(--sched-warn) 16%, transparent);color:var(--sched-warn)}
.sched-pill.danger{background:color-mix(in srgb, var(--sched-danger) 16%, transparent);color:var(--sched-danger)}
.sched-pill.accent{background:var(--sched-accent-soft);color:var(--sched-accent)}
.sched-meta{
  display:flex;
  flex-direction:column;
  gap:3px;
  font-size:12px;
  color:var(--sched-muted);
}
.sched-meta strong{color:var(--sched-text);font-weight:500}
.sched-actions{display:flex;align-items:center;gap:6px;flex-wrap:wrap;padding-top:2px}
.sched-btn{
  display:inline-flex;
  align-items:center;
  justify-content:center;
  gap:5px;
  height:30px;
  padding:0 11px;
  border:1px solid var(--sched-line);
  border-radius:7px;
  background:transparent;
  color:var(--sched-text);
  font:inherit;
  font-size:12.5px;
  font-weight:500;
  white-space:nowrap;
  flex-shrink:0;
  cursor:pointer;
}
.sched-shell.compact .sched-btn{height:27px;padding:0 9px;font-size:12px}
.sched-btn:hover:not([disabled]){background:var(--sched-hover)}
.sched-btn[disabled]{opacity:.48;cursor:not-allowed}
.sched-btn.primary{
  border-color:transparent;
  background:var(--sched-accent);
  color:var(--color-text-on-accent, #fff);
  font-weight:600;
}
.sched-btn.primary:hover:not([disabled]){filter:brightness(1.06)}
.sched-btn.danger{color:var(--sched-danger)}
.sched-btn.quiet{
  border-color:transparent;
  background:var(--sched-fill);
  color:var(--sched-muted);
}
.sched-btn.quiet:hover:not([disabled]){
  background:var(--sched-hover);
  color:var(--sched-text);
}
.sched-select,.sched-input{
  height:30px;
  padding:0 9px;
  border:1px solid var(--sched-line);
  border-radius:7px;
  background:var(--sched-bg);
  color:var(--sched-text);
  font:inherit;
  font-size:12.5px;
}
.sched-empty{
  padding:24px 16px;
  border:1px dashed var(--sched-line);
  border-radius:10px;
  text-align:center;
  display:flex;
  flex-direction:column;
  align-items:center;
  gap:10px;
  color:var(--sched-muted);
}
.sched-empty p{margin:0;font-size:12.5px;max-width:360px;line-height:1.55}
.sched-tpl-row{display:flex;flex-wrap:wrap;gap:6px;justify-content:center}
.sched-unread-dot{
  width:7px;height:7px;border-radius:50%;
  background:var(--sched-accent);
  flex:none;
}
.sched-check{
  display:inline-flex;align-items:center;gap:6px;
  font-size:12px;color:var(--sched-muted);cursor:pointer;
}
.sched-switch{
  margin-left:auto;
  display:inline-flex;align-items:center;gap:5px;
  padding:3px 9px;border-radius:999px;
  border:1px solid var(--sched-line);
  background:transparent;color:inherit;font:inherit;font-size:11.5px;cursor:pointer;
}
.sched-switch[aria-pressed="true"]{
  border-color:var(--sched-accent);
  background:var(--sched-accent-soft);
}
.sched-banner-row{
  display:flex;
  align-items:center;
  justify-content:space-between;
  gap:10px;
  flex-wrap:wrap;
}
.sched-info-strip{
  margin:10px 20px 0;
  padding:8px 12px;
  border-radius:8px;
  border:1px solid var(--sched-line);
  background:var(--sched-fill);
  display:flex;
  align-items:center;
  justify-content:space-between;
  gap:10px;
  flex-wrap:wrap;
  font-size:12px;
  line-height:1.5;
  color:var(--sched-muted);
}
.sched-shell.compact .sched-info-strip{margin:10px 14px 0}
.sched-link-btn{
  border:0;
  padding:0;
  background:transparent;
  color:var(--sched-accent);
  font:inherit;
  font-size:12px;
  font-weight:500;
  cursor:pointer;
  white-space:nowrap;
}
.sched-link-btn:hover{text-decoration:underline}
.sched-guardrails-bar{
  display:flex;
  align-items:center;
  justify-content:space-between;
  gap:10px 14px;
  flex-wrap:wrap;
  padding:9px 12px;
  border:1px solid var(--sched-line);
  border-radius:10px;
  background:var(--sched-fill);
  font-size:12px;
  color:var(--sched-muted);
}
.sched-guardrails-group{
  display:flex;
  align-items:center;
  gap:8px 14px;
  flex-wrap:wrap;
}
.sched-guardrail-item{
  display:inline-flex;
  align-items:center;
  gap:6px;
}
.sched-guardrail-item input{
  width:64px;
  height:27px;
  padding:0 7px;
  font-variant-numeric:tabular-nums;
}
.sched-card.highlighted{
  border-color:var(--sched-accent);
  box-shadow:0 0 0 2px var(--sched-accent-soft);
}
.sched-meta-warn{
  color:var(--sched-warn);
  font-weight:500;
}
`;

const DIALOG_CSS = `
.ekd-sched-dialog{
  --sched-bg:var(--color-bg, #ffffff);
  --sched-text:var(--color-text, #1f2328);
  --sched-muted:var(--color-text-muted, var(--color-text-secondary, color-mix(in srgb, var(--sched-text) 64%, transparent)));
  --sched-line:var(--color-line, color-mix(in srgb, var(--sched-text) 16%, transparent));
  --sched-fill:var(--color-fill-1, var(--panel, color-mix(in srgb, var(--sched-text) 6%, transparent)));
  --sched-hover:var(--color-hover, color-mix(in srgb, var(--sched-text) 8%, transparent));
  --sched-accent:var(--color-accent, var(--blue, #1783ff));
  --sched-accent-soft:var(--color-accent-soft, var(--soft, color-mix(in srgb, var(--sched-accent) 14%, transparent)));
  --sched-danger:var(--color-danger, var(--red, var(--danger, #dc2626)));
  --sched-ok:var(--color-done, var(--success, #16a34a));
  --sched-warn:var(--warn, #d97706);
  position:fixed;
  z-index:9999;
  -webkit-app-region:no-drag;
  width:min(660px,calc(100vw - 36px));
  max-height:min(860px,calc(100vh - 48px));
  padding:0;
  border:1px solid var(--sched-line);
  border-radius:12px;
  background:var(--sched-bg);
  color:var(--sched-text);
  font:13.5px/1.5 var(--font-ui,system-ui,-apple-system,sans-serif);
  box-shadow:0 24px 64px rgba(0,0,0,.32);
}
.ekd-sched-dialog::backdrop{background:rgba(0,0,0,.44)}
.ekd-sched-dialog button,.ekd-sched-dialog input,.ekd-sched-dialog select,.ekd-sched-dialog textarea{
  -webkit-app-region:no-drag;
  position:relative;
  z-index:1;
}
.ekd-sd-head{
  display:flex;align-items:flex-start;gap:12px;
  padding:16px 20px 12px;
  border-bottom:1px solid var(--sched-line);
}
.ekd-sd-head h2{margin:0;font-size:16px;font-weight:600}
.ekd-sd-head p{margin:3px 0 0;font-size:12px;color:var(--sched-muted)}
.ekd-sd-close{
  margin-left:auto;flex:none;width:28px;height:28px;border:0;border-radius:6px;
  background:transparent;color:inherit;cursor:pointer;display:inline-flex;align-items:center;justify-content:center;
}
.ekd-sd-close:hover{background:var(--sched-hover)}
.ekd-sd-body{
  display:flex;flex-direction:column;gap:14px;
  padding:16px 20px;
  overflow-y:auto;
  max-height:min(68vh,620px);
}
.ekd-sd-field{display:flex;flex-direction:column;gap:5px}
.ekd-sd-label{display:flex;align-items:baseline;justify-content:space-between;gap:8px;font-size:12.5px}
.ekd-sd-label strong{font-weight:600}
.ekd-sd-hint{font-size:12px;color:var(--sched-muted);margin:0;line-height:1.45}
.ekd-sd-input,.ekd-sd-select,.ekd-sd-textarea{
  width:100%;padding:7px 10px;
  border:1px solid var(--sched-line);
  border-radius:7px;
  background:var(--sched-bg);
  color:var(--sched-text);
  font:inherit;
}
.ekd-sd-textarea{min-height:84px;resize:vertical;line-height:1.5}
.ekd-sd-row{display:flex;flex-wrap:wrap;gap:8px;align-items:center}
.ekd-sd-grid2{display:grid;grid-template-columns:1fr 1fr;gap:10px}
.ekd-sd-path-row{display:flex;align-items:center;gap:8px}
.ekd-sd-path-row .ekd-sd-input{flex:1 1 auto;min-width:0}
.ekd-sd-pick-btn{
  display:inline-flex;align-items:center;gap:6px;
  height:34px;padding:0 12px;
  border:1px solid var(--sched-line);border-radius:7px;
  background:var(--sched-fill);color:var(--sched-text);
  font:inherit;font-size:12.5px;font-weight:500;
  cursor:pointer;white-space:nowrap;flex:none;
}
.ekd-sd-pick-btn:hover:not([disabled]){background:var(--sched-hover)}
.ekd-sd-pick-btn[disabled]{opacity:.55;cursor:not-allowed}
.ekd-sd-chips{display:flex;flex-wrap:wrap;gap:6px}
.ekd-sd-chip{
  height:26px;padding:0 11px;
  border:1px solid var(--sched-line);
  border-radius:999px;background:var(--sched-bg);color:inherit;
  font:inherit;font-size:12px;cursor:pointer;
  font-variant-numeric:tabular-nums;
}
.ekd-sd-chip:hover{background:var(--sched-hover)}
.ekd-sd-chip[aria-pressed="true"]{
  border-color:var(--sched-accent);
  background:var(--sched-accent-soft);
  color:var(--sched-accent);
  font-weight:600;
}
.ekd-sd-seg{
  display:inline-flex;gap:2px;padding:2px;border-radius:999px;
  background:var(--sched-fill);
  width:fit-content;
}
.ekd-sd-seg button{
  height:26px;padding:0 12px;border:0;border-radius:999px;
  background:transparent;color:inherit;font:inherit;font-size:12.5px;cursor:pointer;
}
.ekd-sd-seg button[aria-pressed="true"]{
  background:var(--sched-bg);font-weight:600;box-shadow:0 0 0 1px var(--sched-line);
}
.ekd-sd-sched-card{
  display:flex;flex-direction:column;gap:12px;
  padding:13px 14px;
  border:1px solid var(--sched-line);
  border-radius:10px;
  background:color-mix(in srgb, var(--sched-fill) 70%, transparent);
}
.ekd-sd-preview{
  display:flex;align-items:flex-start;gap:8px;
  padding:9px 11px;
  border-radius:8px;
  border:1px solid color-mix(in srgb, var(--sched-accent) 30%, var(--sched-line));
  background:var(--sched-accent-soft);
  color:var(--sched-text);
  font-size:12.5px;font-weight:500;line-height:1.45;
  font-variant-numeric:tabular-nums;
}
.ekd-sd-preview svg{flex:none;margin-top:2px;color:var(--sched-accent)}
.ekd-sd-freq-seg{
  display:flex;flex-wrap:wrap;gap:4px;padding:3px;
  border-radius:9px;background:var(--sched-fill);border:1px solid var(--sched-line);
}
.ekd-sd-freq-btn{
  flex:1 1 auto;min-width:72px;height:29px;padding:0 10px;
  border:0;border-radius:6px;background:transparent;
  color:var(--sched-muted);font:inherit;font-size:12.5px;cursor:pointer;white-space:nowrap;
}
.ekd-sd-freq-btn:hover{color:var(--sched-text)}
.ekd-sd-freq-btn[aria-pressed="true"]{
  background:var(--sched-bg);color:var(--sched-text);font-weight:600;
  box-shadow:0 1px 2px rgba(0,0,0,.08),0 0 0 1px var(--sched-line);
}
.ekd-sd-subblock{display:flex;flex-direction:column;gap:7px}
.ekd-sd-sublabel{font-size:12px;font-weight:600;color:var(--sched-text)}
.ekd-sd-time-row{display:flex;align-items:center;flex-wrap:wrap;gap:8px}
.ekd-sd-time-picker{
  display:inline-flex;align-items:center;gap:2px;padding:2px 6px;
  border:1px solid var(--sched-line);border-radius:8px;background:var(--sched-bg);
}
.ekd-sd-time-seg{
  width:58px;height:28px;padding:0 4px;border:0;border-radius:5px;
  background:transparent;color:var(--sched-text);
  font:inherit;font-size:13px;font-weight:500;
  font-variant-numeric:tabular-nums;text-align:center;cursor:pointer;
}
.ekd-sd-time-colon{font-weight:600;color:var(--sched-muted);font-variant-numeric:tabular-nums;user-select:none}
.ekd-sd-day-row{display:flex;align-items:center;flex-wrap:wrap;gap:8px}
.ekd-sd-days{display:inline-flex;align-items:center;gap:5px;flex-wrap:wrap}
.ekd-sd-day-btn{
  width:31px;height:31px;padding:0;
  border:1px solid var(--sched-line);border-radius:999px;
  background:var(--sched-bg);color:var(--sched-text);
  font:inherit;font-size:12px;font-weight:500;cursor:pointer;
  display:inline-flex;align-items:center;justify-content:center;
}
.ekd-sd-day-btn:hover{background:var(--sched-hover)}
.ekd-sd-day-btn[aria-pressed="true"]{
  border-color:var(--sched-accent);background:var(--sched-accent);
  color:var(--color-text-on-accent,#fff);font-weight:600;
}
.ekd-sd-times-list{display:flex;flex-wrap:wrap;gap:6px}
.ekd-sd-time-tag{
  display:inline-flex;align-items:center;gap:5px;height:27px;padding:0 6px 0 10px;
  border:1px solid color-mix(in srgb, var(--sched-accent) 32%, var(--sched-line));
  border-radius:999px;background:var(--sched-bg);
  font-size:12.5px;font-weight:500;font-variant-numeric:tabular-nums;
}
.ekd-sd-time-remove{
  width:18px;height:18px;padding:0;border:0;border-radius:999px;
  background:transparent;color:var(--sched-muted);cursor:pointer;
  display:inline-flex;align-items:center;justify-content:center;font-size:13px;line-height:1;
}
.ekd-sd-time-remove:hover{background:var(--sched-hover);color:var(--sched-danger)}
.ekd-sd-validity{
  display:flex;flex-direction:column;gap:8px;
  padding-top:10px;border-top:1px solid var(--sched-line);
}
.ekd-sd-date-wrap{display:flex;align-items:center;gap:6px}
.ekd-sd-date-wrap .ekd-sd-input{flex:1 1 auto;min-width:0}
.ekd-sd-mini-btn{
  height:26px;padding:0 9px;
  border:1px solid var(--sched-line);border-radius:6px;
  background:var(--sched-bg);color:var(--sched-muted);
  font:inherit;font-size:11.5px;cursor:pointer;white-space:nowrap;flex:none;
}
.ekd-sd-mini-btn:hover{background:var(--sched-hover);color:var(--sched-text)}
.ekd-sd-error{
  padding:8px 10px;border-radius:7px;
  background:color-mix(in srgb, var(--sched-danger) 12%, transparent);color:var(--sched-danger);
  font-size:12.5px;
}
.ekd-sd-var-bar{
  display:flex;
  align-items:center;
  flex-wrap:wrap;
  gap:6px;
}
.ekd-sd-var-preview{
  padding:8px 10px;
  border:1px solid var(--sched-line);
  border-radius:7px;
  background:var(--sched-fill);
  font-size:12px;
  line-height:1.5;
  color:var(--sched-text);
  white-space:pre-wrap;
  word-break:break-word;
  max-height:110px;
  overflow-y:auto;
}
.ekd-sd-var-preview.empty{
  color:var(--sched-muted);
}
.ekd-sd-warn{
  display:flex;
  flex-direction:column;
  gap:6px;
  padding:9px 11px;
  border:1px solid color-mix(in srgb, var(--sched-warn) 45%, transparent);
  border-radius:8px;
  background:color-mix(in srgb, var(--sched-warn) 12%, transparent);
  color:var(--sched-text);
  font-size:12px;
  line-height:1.5;
}
.ekd-sd-warn-sample{
  margin:0;
  padding:6px 8px;
  border-radius:6px;
  background:color-mix(in srgb, var(--sched-bg) 70%, transparent);
  font-family:ui-monospace,SFMono-Regular,Menlo,monospace;
  font-size:11px;
  white-space:pre-wrap;
  color:var(--sched-muted);
}
.ekd-sd-ok-hint{
  font-size:12px;
  color:var(--sched-ok);
  margin:0;
}
.ekd-sd-check{
  display:inline-flex;
  align-items:flex-start;
  gap:7px;
  font-size:12.5px;
  line-height:1.45;
  color:var(--sched-text);
  cursor:pointer;
}
.ekd-sd-check input{margin-top:2px;flex:none}
.ekd-sd-foot{
  display:flex;align-items:center;gap:8px;flex-wrap:wrap;
  padding:12px 20px 16px;
  border-top:1px solid var(--sched-line);
}
.ekd-sd-foot-note{margin-right:auto;font-size:11.5px;color:var(--sched-muted)}
.ekd-sd-btn{
  height:30px;padding:0 14px;
  border:1px solid var(--sched-line);
  border-radius:6px;background:transparent;color:inherit;
  font:inherit;font-size:12.5px;cursor:pointer;white-space:nowrap;
}
.ekd-sd-btn:hover:not([disabled]){background:var(--sched-hover)}
.ekd-sd-btn[disabled]{opacity:.5;cursor:not-allowed}
.ekd-sd-btn.primary{
  border-color:transparent;
  background:var(--sched-accent);
  color:var(--color-text-on-accent,#fff);
  font-weight:600;
}
`;

export async function activate(ctx) {
  let disposed = false;
  let mainHandle = null;
  let panelHandle = null;
  let dialogEl = null;
  let dialogStyleEl = null;

  const state = {
    loading: true,
    error: "",
    activeTab: "tasks",
    tasks: [],
    runs: [],
    runFilterTaskId: "",
    kapConnected: true,
    kapReason: "",
    runningTaskIds: [],
    unreadCount: 0,
    prefs: { autoMarkRead: false, maxConcurrent: 2, minQuotaPercent: 10 },
    autoAttach: null,
    highlightRunId: "",
    prefsNotice: "",
    models: [],
    sessions: [],
    rightPanelSessionId: "",
  };

  const listeners = new Set();
  function notify() {
    for (const fn of [...listeners]) {
      try { fn(); } catch {}
    }
  }

  async function refreshAll() {
    if (disposed) return;
    state.error = "";
    try {
      const [taskRes, runRes, modelList, sessRes] = await Promise.all([
        ctx.core.request("schedule.list", {}),
        ctx.core.request("schedule.runs", { taskId: state.runFilterTaskId, limit: 80 }),
        ctx.models?.list ? ctx.models.list().catch(() => []) : Promise.resolve([]),
        ctx.core.request("sessions.list", {}).catch(() => ({ items: [] })),
      ]);
      if (disposed) return;
      state.tasks = Array.isArray(taskRes?.items) ? taskRes.items : [];
      state.kapConnected = Boolean(taskRes?.kapConnected);
      state.kapReason = taskRes?.kapReason || "";
      state.runningTaskIds = Array.isArray(taskRes?.runningTaskIds) ? taskRes.runningTaskIds : [];
      state.unreadCount = Number(taskRes?.unreadCount || 0);
      const rawPrefs = taskRes?.prefs || {};
      state.prefs = {
        autoMarkRead: Boolean(rawPrefs.autoMarkRead),
        maxConcurrent: typeof rawPrefs.maxConcurrent === "number" && rawPrefs.maxConcurrent >= 1 ? rawPrefs.maxConcurrent : 2,
        minQuotaPercent: typeof rawPrefs.minQuotaPercent === "number" && rawPrefs.minQuotaPercent >= 0 ? rawPrefs.minQuotaPercent : 10,
      };
      state.autoAttach = taskRes?.autoAttach && typeof taskRes.autoAttach === "object" ? taskRes.autoAttach : null;
      state.runs = Array.isArray(runRes?.items) ? runRes.items : [];
      state.models = Array.isArray(modelList) ? modelList : [];
      state.sessions = Array.isArray(sessRes?.items) ? sessRes.items : [];
      state.loading = false;
    } catch (error) {
      if (disposed) return;
      state.loading = false;
      state.error = error instanceof Error ? error.message : String(error);
    }
    notify();
  }

  const unsubSchedule = ctx.events.on("schedule", () => {
    void refreshAll();
  });

  function closeMainArea() {
    if (!mainHandle) return;
    const handle = mainHandle;
    mainHandle = null;
    handle.dispose();
  }

  const onActiveSessionChange = () => {
    closeMainArea();
  };
  const onOpenRunsEvent = event => {
    const detail = event && typeof event === "object" ? event.detail : null;
    if (detail && typeof detail === "object") {
      state.runFilterTaskId = typeof detail.taskId === "string" ? detail.taskId : "";
      state.highlightRunId = typeof detail.runId === "string" ? detail.runId : "";
    }
    openMainArea("runs");
  };
  const onAttachChanged = () => {
    void refreshAll();
  };
  if (typeof window !== "undefined" && typeof window.addEventListener === "function") {
    window.addEventListener("ekd:active-session", onActiveSessionChange);
    window.addEventListener("ekd:scheduler-open-runs", onOpenRunsEvent);
    window.addEventListener("ekd:attach-changed", onAttachChanged);
  }

  function closeDialog() {
    if (dialogEl) {
      try {
        if (typeof dialogEl.close === "function" && dialogEl.open) dialogEl.close();
      } catch {}
      dialogEl.remove();
      dialogEl = null;
    }
  }

  function ensureDialogStyle() {
    if (dialogStyleEl && dialogStyleEl.isConnected) return;
    dialogStyleEl = document.createElement("style");
    dialogStyleEl.dataset.ekdSchedDialog = "1";
    dialogStyleEl.textContent = DIALOG_CSS;
    document.head.append(dialogStyleEl);
  }

  function openTaskDialog(prefill = {}) {
    if (disposed) return;
    closeDialog();
    ensureDialogStyle();
    const doc = document;
    const draft = defaultDraft(prefill);
    const activeSessionId = ctx.sessions?.activeId?.() || ctx.activeSessionId?.() || state.rightPanelSessionId || "";
    if (!draft.cwd) {
      draft.cwd = resolveDefaultCwd(prefill.cwd, state.sessions, draft.sessionId, activeSessionId);
    }
    let busy = false;
    let pickingDir = false;
    let dirPickerUnavailable = false;
    let dirPickerHint = "";
    let pendingTime = "12:00";
    let errorMsg = "";
    let gitChecking = false;
    let gitStatus = null;
    let gitCheckSeq = 0;
    let gitCheckTimer = null;

    function effectiveYoloCwd() {
      if (draft.cwd && draft.cwd.trim()) return draft.cwd.trim();
      if (draft.mode === "bound_session" && draft.sessionId) {
        const matched = state.sessions.find(s => s.id === draft.sessionId);
        if (matched?.cwd) return String(matched.cwd).trim();
      }
      return "";
    }

    async function runGitCheck(targetCwd) {
      const seq = ++gitCheckSeq;
      if (!targetCwd) {
        gitChecking = false;
        gitStatus = null;
        renderDialog();
        return;
      }
      gitChecking = true;
      renderDialog();
      try {
        const res = await ctx.core.request("schedule.checkGit", { cwd: targetCwd });
        if (disposed || seq !== gitCheckSeq || dialogEl !== dialog) return;
        gitStatus = res && typeof res === "object" ? res : null;
      } catch (err) {
        if (disposed || seq !== gitCheckSeq || dialogEl !== dialog) return;
        gitStatus = { isGitRepo: false, clean: true, error: err instanceof Error ? err.message : String(err) };
      } finally {
        if (!disposed && seq === gitCheckSeq && dialogEl === dialog) {
          gitChecking = false;
          renderDialog();
        }
      }
    }

    function scheduleGitCheck(immediate = false) {
      if (gitCheckTimer) {
        clearTimeout(gitCheckTimer);
        gitCheckTimer = null;
      }
      if (draft.permissionMode !== "yolo") {
        gitChecking = false;
        gitStatus = null;
        return;
      }
      const targetCwd = effectiveYoloCwd();
      if (!targetCwd) {
        gitChecking = false;
        gitStatus = null;
        return;
      }
      if (immediate) {
        void runGitCheck(targetCwd);
      } else {
        gitCheckTimer = setTimeout(() => {
          gitCheckTimer = null;
          void runGitCheck(effectiveYoloCwd());
        }, 320);
      }
    }

    const dialog = doc.createElement("dialog");
    dialog.className = "ekd-sched-dialog";
    dialog.setAttribute("aria-label", draft.id ? "编辑定时任务" : "新建定时任务");

    function createTimePicker(value, onChange) {
      const normalized = normalizeClock(value, "09:00");
      const [initH, initM] = normalized.split(":");
      const hourOptions = [];
      for (let h = 0; h < 24; h += 1) {
        const str = String(h).padStart(2, "0");
        hourOptions.push(el(doc, "option", { value: str, text: str }));
      }
      const minuteOptions = [];
      for (let m = 0; m < 60; m += 1) {
        const str = String(m).padStart(2, "0");
        minuteOptions.push(el(doc, "option", { value: str, text: str }));
      }
      const hourSel = el(doc, "select", {
        class: "ekd-sd-time-seg",
        "aria-label": "小时",
      }, hourOptions);
      hourSel.value = initH;
      const minSel = el(doc, "select", {
        class: "ekd-sd-time-seg",
        "aria-label": "分钟",
      }, minuteOptions);
      minSel.value = initM;
      const emit = () => onChange(`${hourSel.value}:${minSel.value}`);
      hourSel.addEventListener("change", emit);
      minSel.addEventListener("change", emit);
      return el(doc, "div", { class: "ekd-sd-time-picker" }, [
        hourSel,
        el(doc, "span", { class: "ekd-sd-time-colon", text: ":" }),
        minSel,
      ]);
    }

    function createWeekdayPicker(options = {}) {
      const allowEmpty = Boolean(options.allowEmpty);
      const selected = new Set(Array.isArray(draft.weekdays) ? draft.weekdays : []);
      const dayButtons = el(doc, "div", { class: "ekd-sd-days", role: "group", "aria-label": "选择星期" }, WEEKDAY_ITEMS.map(wd => {
        const active = selected.has(wd.value);
        return el(doc, "button", {
          type: "button",
          class: "ekd-sd-day-btn",
          text: wd.short,
          title: wd.label,
          "aria-label": wd.label,
          "aria-pressed": active ? "true" : "false",
          onClick: () => {
            draft.weekdaysAutoDefault = false;
            if (active) draft.weekdays = draft.weekdays.filter(v => v !== wd.value);
            else draft.weekdays = [...draft.weekdays, wd.value];
            renderDialog();
          },
        });
      }));

      const quickBtns = [
        el(doc, "button", {
          type: "button",
          class: "ekd-sd-mini-btn",
          text: "工作日",
          onClick: () => {
            draft.weekdaysAutoDefault = false;
            draft.weekdays = [1, 2, 3, 4, 5];
            renderDialog();
          },
        }),
        el(doc, "button", {
          type: "button",
          class: "ekd-sd-mini-btn",
          text: "周末",
          onClick: () => {
            draft.weekdaysAutoDefault = false;
            draft.weekdays = [0, 6];
            renderDialog();
          },
        }),
        el(doc, "button", {
          type: "button",
          class: "ekd-sd-mini-btn",
          text: allowEmpty ? "不限（每天）" : "全选",
          onClick: () => {
            draft.weekdaysAutoDefault = false;
            draft.weekdays = allowEmpty ? [] : [1, 2, 3, 4, 5, 6, 0];
            renderDialog();
          },
        }),
      ];

      return el(doc, "div", { class: "ekd-sd-day-row" }, [
        dayButtons,
        el(doc, "div", { class: "ekd-sd-chips" }, quickBtns),
      ]);
    }

    function renderDialog() {
      const selectedModel = state.models.find(m => m.id === draft.model) || null;
      const efforts = effortOptionsForModel(selectedModel);
      if (draft.effort && !efforts.some(e => e.value === draft.effort)) {
        draft.effort = "";
      }

      const bodyChildren = [];

      if (!draft.id) {
        const tplChips = el(doc, "div", { class: "ekd-sd-chips" }, READONLY_TEMPLATES.map(tpl =>
          el(doc, "button", {
            type: "button",
            class: "ekd-sd-chip",
            text: tpl.label,
            onClick: () => {
              draft.name = tpl.name;
              draft.prompt = tpl.prompt;
              draft.permissionMode = "manual";
              renderDialog();
            },
          }),
        ));
        bodyChildren.push(el(doc, "div", { class: "ekd-sd-field" }, [
          el(doc, "div", { class: "ekd-sd-label" }, [el(doc, "strong", { text: "只读分析模板" })]),
          tplChips,
        ]));
      }

      bodyChildren.push(el(doc, "div", { class: "ekd-sd-field" }, [
        el(doc, "label", { class: "ekd-sd-label" }, [el(doc, "strong", { text: "任务名称" })]),
        el(doc, "input", {
          type: "text",
          class: "ekd-sd-input",
          placeholder: "例如：每日代码变更摘要",
          value: draft.name,
          onInput: e => { draft.name = e.target.value; },
        }),
      ]));

      const promptPreviewEl = el(doc, "div", {
        class: `ekd-sd-var-preview${draft.prompt.trim() ? "" : " empty"}`,
        "data-ekd-prompt-preview": "true",
        text: draft.prompt.trim()
          ? previewPromptTemplate(draft.prompt, { lastRunAt: draft.lastRunAt, lastRunSummary: draft.lastSummary })
          : "支持插入下方变量；输入提示词后在此实时预览实际发送内容。",
      });
      const updatePromptPreview = () => {
        if (draft.prompt.trim()) {
          promptPreviewEl.className = "ekd-sd-var-preview";
          promptPreviewEl.textContent = previewPromptTemplate(draft.prompt, {
            lastRunAt: draft.lastRunAt,
            lastRunSummary: draft.lastSummary,
          });
        } else {
          promptPreviewEl.className = "ekd-sd-var-preview empty";
          promptPreviewEl.textContent = "支持插入下方变量；输入提示词后在此实时预览实际发送内容。";
        }
      };

      const promptTextarea = el(doc, "textarea", {
        class: "ekd-sd-textarea",
        placeholder: "任务到点后将自动发送此提示词…",
        value: draft.prompt,
        onInput: e => {
          draft.prompt = e.target.value;
          updatePromptPreview();
        },
      });

      const varBar = el(doc, "div", { class: "ekd-sd-var-bar", role: "group", "aria-label": "插入提示词变量" }, [
        el(doc, "span", { class: "ekd-sd-hint", text: "插入变量：" }),
        ...PROMPT_VARIABLES.map(variable => el(doc, "button", {
          type: "button",
          class: "ekd-sd-chip",
          text: `${variable.token} ${variable.label}`,
          title: `${variable.label}（${variable.hint}）`,
          "data-ekd-var-token": variable.token,
          onClick: () => {
            const current = String(draft.prompt || "");
            const hasSelection = typeof promptTextarea.selectionStart === "number" && typeof promptTextarea.selectionEnd === "number";
            if (hasSelection) {
              const start = promptTextarea.selectionStart;
              const end = promptTextarea.selectionEnd;
              const next = current.slice(0, start) + variable.token + current.slice(end);
              draft.prompt = next;
              promptTextarea.value = next;
              const caret = start + variable.token.length;
              if (typeof promptTextarea.setSelectionRange === "function") {
                promptTextarea.setSelectionRange(caret, caret);
              }
              if (typeof promptTextarea.focus === "function") {
                promptTextarea.focus();
              }
            } else {
              const next = current ? `${current}${current.endsWith(" ") || current.endsWith("\n") ? "" : " "}${variable.token}` : variable.token;
              draft.prompt = next;
              promptTextarea.value = next;
            }
            updatePromptPreview();
          },
        })),
      ]);

      bodyChildren.push(el(doc, "div", { class: "ekd-sd-field" }, [
        el(doc, "label", { class: "ekd-sd-label" }, [
          el(doc, "strong", { text: "提示词" }),
          el(doc, "span", { class: "ekd-sd-hint", text: "运行前自动展开变量" }),
        ]),
        promptTextarea,
        varBar,
        el(doc, "span", { class: "ekd-sd-hint", text: "提示词展开预览" }),
        promptPreviewEl,
      ]));

      const modeSeg = el(doc, "div", { class: "ekd-sd-seg", role: "group", "aria-label": "执行方式" }, [
        el(doc, "button", {
          type: "button",
          text: "新开会话",
          "aria-pressed": draft.mode === "new_session" ? "true" : "false",
          onClick: () => {
            draft.mode = "new_session";
            if (!draft.cwd) {
              draft.cwd = resolveDefaultCwd("", state.sessions, draft.sessionId, activeSessionId);
            }
            renderDialog();
            scheduleGitCheck(true);
          },
        }),
        el(doc, "button", {
          type: "button",
          text: "绑定已有会话",
          "aria-pressed": draft.mode === "bound_session" ? "true" : "false",
          onClick: () => {
            draft.mode = "bound_session";
            renderDialog();
            scheduleGitCheck(true);
          },
        }),
      ]);

      bodyChildren.push(el(doc, "div", { class: "ekd-sd-field" }, [
        el(doc, "div", { class: "ekd-sd-label" }, [el(doc, "strong", { text: "执行方式" })]),
        modeSeg,
      ]));

      if (draft.mode === "new_session") {
        const canPickDirectory = typeof ctx.ui?.pickDirectory === "function" && !dirPickerUnavailable;
        const cwdInput = el(doc, "input", {
          type: "text",
          class: "ekd-sd-input",
          placeholder: "/path/to/project",
          value: draft.cwd,
          onInput: e => {
            const nextCwd = e.target.value;
            if (nextCwd.trim() !== draft.cwd.trim()) {
              draft.confirmDirtyYolo = false;
            }
            draft.cwd = nextCwd;
            scheduleGitCheck(false);
          },
        });
        const pickBtn = canPickDirectory
          ? el(doc, "button", {
              type: "button",
              class: "ekd-sd-pick-btn",
              disabled: pickingDir || busy,
              onClick: async () => {
                if (pickingDir || busy) return;
                pickingDir = true;
                dirPickerHint = "";
                renderDialog();
                try {
                  const picked = await ctx.ui.pickDirectory({
                    title: "选择定时任务工作目录",
                    defaultPath: draft.cwd || undefined,
                  });
                  if (typeof picked === "string" && picked.trim()) {
                    if (picked.trim() !== draft.cwd.trim()) {
                      draft.confirmDirtyYolo = false;
                    }
                    draft.cwd = picked.trim();
                  }
                } catch (err) {
                  const code = err && typeof err === "object" ? err.code : "";
                  if (code === "PLUGIN_DIRECTORY_PICKER_UNAVAILABLE") {
                    dirPickerUnavailable = true;
                    dirPickerHint = "当前环境不支持访达目录选择器，请直接输入或粘贴项目路径。";
                  } else {
                    dirPickerHint = err instanceof Error ? err.message : String(err);
                  }
                } finally {
                  pickingDir = false;
                  renderDialog();
                  scheduleGitCheck(true);
                }
              },
            }, [
              makeSvg(doc, FOLDER_ICON, 14),
              pickingDir ? "选择中…" : "选择文件夹…",
            ])
          : null;

        bodyChildren.push(el(doc, "div", { class: "ekd-sd-field" }, [
          el(doc, "label", { class: "ekd-sd-label" }, [el(doc, "strong", { text: "项目目录" })]),
          el(doc, "div", { class: "ekd-sd-path-row" }, [cwdInput, pickBtn]),
          dirPickerHint
            ? el(doc, "p", { class: "ekd-sd-hint", text: dirPickerHint })
            : !canPickDirectory
              ? el(doc, "p", { class: "ekd-sd-hint", text: "请输入或粘贴本机项目文件夹的绝对路径。" })
              : null,
          el(doc, "label", { class: "ekd-sd-check" }, [
            el(doc, "input", {
              type: "checkbox",
              checked: draft.archiveOnRead,
              onChange: e => { draft.archiveOnRead = e.target.checked; },
            }),
            "运行记录标为已读后，自动归档对应会话到官方「已归档的会话」",
          ]),
          el(doc, "p", {
            class: "ekd-sd-hint",
            text: "新开会话标题将自动加上「[定时] 」前缀；不需要自动归档时可取消勾选。",
          }),
        ]));
      } else {
        const sessSelect = el(doc, "select", {
          class: "ekd-sd-select",
          onChange: e => {
            draft.sessionId = e.target.value;
            const found = state.sessions.find(s => s.id === draft.sessionId);
            if (found?.cwd) {
              if (found.cwd.trim() !== draft.cwd.trim()) {
                draft.confirmDirtyYolo = false;
              }
              draft.cwd = found.cwd;
            }
            scheduleGitCheck(true);
          },
        }, [
          el(doc, "option", { value: "", text: state.sessions.length ? "选择目标会话" : "当前没有可选会话" }),
          ...state.sessions.map(s => el(doc, "option", {
            value: s.id,
            text: `${s.title || s.id}${s.cwd ? ` (${s.cwd})` : ""}`,
          })),
        ]);
        sessSelect.value = draft.sessionId;
        bodyChildren.push(el(doc, "div", { class: "ekd-sd-field" }, [
          el(doc, "label", { class: "ekd-sd-label" }, [el(doc, "strong", { text: "绑定会话" })]),
          sessSelect,
          el(doc, "p", { class: "ekd-sd-hint", text: "若到点时该会话正忙，调度器最多等待 10 分钟；超时将记为跳过。" }),
        ]));
      }

      const modelSelect = el(doc, "select", {
        class: "ekd-sd-select",
        onChange: e => {
          draft.model = e.target.value;
          draft.effort = "";
          renderDialog();
        },
      }, [
        el(doc, "option", { value: "", text: draft.mode === "bound_session" ? "跟随会话默认模型" : "使用客户端默认模型" }),
        ...state.models.map(m => el(doc, "option", { value: m.id, text: formatModelOptionLabel(m) })),
      ]);
      modelSelect.value = draft.model;

      const effortSelect = el(doc, "select", {
        class: "ekd-sd-select",
        disabled: efforts.length === 0,
        onChange: e => { draft.effort = e.target.value; },
      }, efforts.length
        ? efforts.map(opt => el(doc, "option", { value: opt.value, text: opt.label }))
        : [el(doc, "option", { value: "", text: "当前模型无推理强度选项" })]);
      effortSelect.value = draft.effort;

      bodyChildren.push(el(doc, "div", { class: "ekd-sd-grid2" }, [
        el(doc, "div", { class: "ekd-sd-field" }, [
          el(doc, "label", { class: "ekd-sd-label" }, [el(doc, "strong", { text: "模型" })]),
          modelSelect,
        ]),
        el(doc, "div", { class: "ekd-sd-field" }, [
          el(doc, "label", { class: "ekd-sd-label" }, [el(doc, "strong", { text: "推理强度" })]),
          effortSelect,
        ]),
      ]));

      const permSelect = el(doc, "select", {
        class: "ekd-sd-select",
        onChange: e => {
          draft.permissionMode = e.target.value;
          if (draft.permissionMode !== "yolo") {
            draft.confirmDirtyYolo = false;
          }
          renderDialog();
          scheduleGitCheck(true);
        },
      }, PERMISSION_MODES.map(m => el(doc, "option", { value: m.value, text: m.label })));
      permSelect.value = draft.permissionMode;
      const permNote = PERMISSION_MODES.find(m => m.value === draft.permissionMode)?.note || "";

      const yoloNodes = [];
      if (draft.permissionMode === "yolo") {
        if (gitChecking) {
          yoloNodes.push(el(doc, "p", { class: "ekd-sd-hint", text: "正在检查项目目录的 Git 工作区状态…" }));
        } else if (gitStatus?.isGitRepo && !gitStatus.clean) {
          const sampleText = Array.isArray(gitStatus.sample) && gitStatus.sample.length > 0
            ? gitStatus.sample.join("\n")
            : "";
          yoloNodes.push(el(doc, "div", { class: "ekd-sd-warn", "data-ekd-yolo-dirty": "true", role: "alert" }, [
            el(doc, "div", {
              text: `项目目录是 Git 仓库，且当前存在 ${Math.max(1, Number(gitStatus.dirtyCount || 0))} 处未提交改动。使用「始终允许（免审批）」可能直接修改或覆盖这些文件，建议先清理工作区。`,
            }),
            sampleText ? el(doc, "pre", { class: "ekd-sd-warn-sample", text: sampleText }) : null,
            el(doc, "label", { class: "ekd-sd-check" }, [
              el(doc, "input", {
                type: "checkbox",
                checked: draft.confirmDirtyYolo,
                "data-ekd-confirm-dirty-yolo": "true",
                onChange: e => {
                  draft.confirmDirtyYolo = e.target.checked;
                  errorMsg = "";
                  renderDialog();
                },
              }),
              "我已了解风险，确认允许在未清理的 Git 工作区中使用免审批模式",
            ]),
          ]));
        } else if (gitStatus?.isGitRepo && gitStatus.clean) {
          yoloNodes.push(el(doc, "p", { class: "ekd-sd-ok-hint", text: "Git 工作区干净，可以使用免审批模式。" }));
        } else if (gitStatus?.error) {
          yoloNodes.push(el(doc, "p", { class: "ekd-sd-hint", text: `Git 状态检查提示：${gitStatus.error}` }));
        }
      }

      bodyChildren.push(el(doc, "div", { class: "ekd-sd-field" }, [
        el(doc, "label", { class: "ekd-sd-label" }, [el(doc, "strong", { text: "权限模式" })]),
        permSelect,
        el(doc, "p", { class: "ekd-sd-hint", text: permNote }),
        ...yoloNodes,
      ]));

      bodyChildren.push(el(doc, "div", { class: "ekd-sd-grid2" }, [
        el(doc, "div", { class: "ekd-sd-field" }, [
          el(doc, "label", { class: "ekd-sd-label" }, [el(doc, "strong", { text: "单次超时（分钟）" })]),
          el(doc, "input", {
            type: "number",
            min: "1",
            max: "1440",
            class: "ekd-sd-input",
            style: "font-variant-numeric:tabular-nums",
            "aria-label": "单次超时（分钟）",
            value: String(draft.timeoutMinutes),
            onInput: e => {
              const parsed = Number.parseInt(e.target.value, 10);
              if (Number.isFinite(parsed) && parsed >= 1) draft.timeoutMinutes = Math.min(1440, parsed);
            },
          }),
          el(doc, "p", { class: "ekd-sd-hint", text: "默认 30 分钟；超时将自动中止任务并记为失败。" }),
        ]),
        el(doc, "div", { class: "ekd-sd-field" }, [
          el(doc, "label", { class: "ekd-sd-label" }, [el(doc, "strong", { text: "最大回合数" })]),
          el(doc, "input", {
            type: "number",
            min: "0",
            max: "500",
            class: "ekd-sd-input",
            style: "font-variant-numeric:tabular-nums",
            "aria-label": "最大回合数",
            value: String(draft.maxTurns),
            onInput: e => {
              const parsed = Number.parseInt(e.target.value, 10);
              if (Number.isFinite(parsed) && parsed >= 0) draft.maxTurns = Math.min(500, parsed);
            },
          }),
          el(doc, "p", { class: "ekd-sd-hint", text: "0 表示不限；达到上限后自动中止并记为失败。" }),
        ]),
      ]));

      const previewTextEl = el(doc, "span", { text: formatSchedulePreview(draft) });
      const previewBar = el(doc, "div", { class: "ekd-sd-preview", role: "status", "aria-live": "polite" }, [
        makeSvg(doc, SCHEDULER_ICON, 15),
        previewTextEl,
      ]);
      const updatePreview = () => {
        previewTextEl.textContent = formatSchedulePreview(draft);
      };

      const tabItems = [...SCHEDULE_TABS];
      if (LEGACY_SCHEDULE_LABELS[draft.scheduleKind]) {
        tabItems.push({ value: draft.scheduleKind, label: LEGACY_SCHEDULE_LABELS[draft.scheduleKind] });
      }
      const freqTabs = el(doc, "div", { class: "ekd-sd-freq-seg", role: "group", "aria-label": "调度频率" }, tabItems.map(item =>
        el(doc, "button", {
          type: "button",
          class: "ekd-sd-freq-btn",
          text: item.label,
          "aria-pressed": draft.scheduleKind === item.value ? "true" : "false",
          onClick: () => {
            switchScheduleKind(draft, item.value);
            renderDialog();
          },
        }),
      ));

      const schedDetails = [];
      if (draft.scheduleKind === "daily" || draft.scheduleKind === "weekdays") {
        const quickChips = QUICK_TIMES.map(t => el(doc, "button", {
          type: "button",
          class: "ekd-sd-chip",
          text: t,
          "aria-pressed": normalizeClock(draft.time) === t ? "true" : "false",
          onClick: () => {
            draft.time = t;
            renderDialog();
          },
        }));
        schedDetails.push(el(doc, "div", { class: "ekd-sd-subblock" }, [
          el(doc, "span", { class: "ekd-sd-sublabel", text: "执行时间" }),
          el(doc, "div", { class: "ekd-sd-time-row" }, [
            createTimePicker(draft.time, next => {
              draft.time = next;
              renderDialog();
            }),
            el(doc, "div", { class: "ekd-sd-chips" }, quickChips),
          ]),
        ]));
      } else if (draft.scheduleKind === "weekly") {
        const quickChips = QUICK_TIMES.map(t => el(doc, "button", {
          type: "button",
          class: "ekd-sd-chip",
          text: t,
          "aria-pressed": normalizeClock(draft.time) === t ? "true" : "false",
          onClick: () => {
            draft.time = t;
            renderDialog();
          },
        }));
        schedDetails.push(
          el(doc, "div", { class: "ekd-sd-subblock" }, [
            el(doc, "span", { class: "ekd-sd-sublabel", text: "重复星期" }),
            createWeekdayPicker({ allowEmpty: false }),
          ]),
          el(doc, "div", { class: "ekd-sd-subblock" }, [
            el(doc, "span", { class: "ekd-sd-sublabel", text: "执行时间" }),
            el(doc, "div", { class: "ekd-sd-time-row" }, [
              createTimePicker(draft.time, next => {
                draft.time = next;
                renderDialog();
              }),
              el(doc, "div", { class: "ekd-sd-chips" }, quickChips),
            ]),
          ]),
        );
      } else if (draft.scheduleKind === "times") {
        const currentTimes = parseTimesList(draft.timesText);
        const timeTagNodes = currentTimes.map(t => el(doc, "span", { class: "ekd-sd-time-tag" }, [
          t,
          el(doc, "button", {
            type: "button",
            class: "ekd-sd-time-remove",
            "aria-label": `移除 ${t}`,
            text: "×",
            onClick: () => {
              const next = currentTimes.filter(item => item !== t);
              draft.timesText = next.join(", ");
              renderDialog();
            },
          }),
        ]));
        const multiQuickChips = QUICK_MULTI_TIMES.map(t => {
          const exists = currentTimes.includes(t);
          return el(doc, "button", {
            type: "button",
            class: "ekd-sd-chip",
            text: t,
            "aria-pressed": exists ? "true" : "false",
            onClick: () => {
              const next = exists
                ? currentTimes.filter(item => item !== t)
                : parseTimesList([...currentTimes, t]);
              draft.timesText = next.join(", ");
              renderDialog();
            },
          });
        });
        schedDetails.push(
          el(doc, "div", { class: "ekd-sd-subblock" }, [
            el(doc, "span", { class: "ekd-sd-sublabel", text: "已添加时点" }),
            currentTimes.length
              ? el(doc, "div", { class: "ekd-sd-times-list" }, timeTagNodes)
              : el(doc, "p", { class: "ekd-sd-hint", text: "暂未添加执行时点，请在下方选择并添加。" }),
          ]),
          el(doc, "div", { class: "ekd-sd-subblock" }, [
            el(doc, "span", { class: "ekd-sd-sublabel", text: "添加时点" }),
            el(doc, "div", { class: "ekd-sd-time-row" }, [
              createTimePicker(pendingTime, next => { pendingTime = next; }),
              el(doc, "button", {
                type: "button",
                class: "ekd-sd-btn",
                text: "+ 添加时点",
                onClick: () => {
                  const next = parseTimesList([...currentTimes, pendingTime]);
                  draft.timesText = next.join(", ");
                  renderDialog();
                },
              }),
              el(doc, "div", { class: "ekd-sd-chips" }, multiQuickChips),
            ]),
          ]),
          el(doc, "div", { class: "ekd-sd-subblock" }, [
            el(doc, "span", { class: "ekd-sd-sublabel", text: "星期筛选（可选）" }),
            createWeekdayPicker({ allowEmpty: true }),
            el(doc, "p", {
              class: "ekd-sd-hint",
              text: draft.weekdays.length === 0
                ? "不限星期（每天按上述时点运行）"
                : "仅在所选星期按上述时点运行",
            }),
          ]),
        );
      } else if (draft.scheduleKind === "interval") {
        const unitSel = el(doc, "select", {
          class: "ekd-sd-select",
          style: "width:96px",
          onChange: e => {
            const nextUnit = e.target.value === "hours" ? "hours" : "minutes";
            const curVal = Math.max(1, Number.parseInt(String(draft.intervalValue || 1), 10) || 1);
            if (draft.intervalUnit !== nextUnit) {
              if (nextUnit === "hours") {
                draft.intervalValue = curVal >= 60 && curVal % 60 === 0 ? curVal / 60 : Math.max(1, Math.round(curVal / 60));
              } else {
                draft.intervalValue = curVal * 60;
              }
              draft.intervalUnit = nextUnit;
            }
            renderDialog();
          },
        }, [
          el(doc, "option", { value: "minutes", text: "分钟" }),
          el(doc, "option", { value: "hours", text: "小时" }),
        ]);
        unitSel.value = draft.intervalUnit;

        const presetChips = INTERVAL_PRESETS.map(preset => {
          const active = Number(draft.intervalValue) === preset.value && draft.intervalUnit === preset.unit;
          return el(doc, "button", {
            type: "button",
            class: "ekd-sd-chip",
            text: preset.label,
            "aria-pressed": active ? "true" : "false",
            onClick: () => {
              draft.intervalValue = preset.value;
              draft.intervalUnit = preset.unit;
              renderDialog();
            },
          });
        });

        schedDetails.push(
          el(doc, "div", { class: "ekd-sd-subblock" }, [
            el(doc, "span", { class: "ekd-sd-sublabel", text: "间隔周期" }),
            el(doc, "div", { class: "ekd-sd-time-row" }, [
              el(doc, "span", { text: "每隔" }),
              el(doc, "input", {
                type: "number",
                min: "1",
                class: "ekd-sd-input",
                style: "width:90px;font-variant-numeric:tabular-nums",
                value: String(draft.intervalValue),
                onInput: e => {
                  draft.intervalValue = Math.max(1, Number.parseInt(e.target.value, 10) || 1);
                  updatePreview();
                },
              }),
              unitSel,
              el(doc, "div", { class: "ekd-sd-chips" }, presetChips),
            ]),
          ]),
          el(doc, "label", { class: "sched-check" }, [
            el(doc, "input", {
              type: "checkbox",
              checked: draft.testMode,
              onChange: e => { draft.testMode = e.target.checked; updatePreview(); },
            }),
            "测试模式（允许低至每 1 分钟运行一次，建议仅用于短期验证）",
          ]),
        );
      } else if (draft.scheduleKind === "once") {
        const todayStr = localDateOffset(0);
        const tomorrowStr = localDateOffset(1);
        const quickChips = QUICK_TIMES.map(t => el(doc, "button", {
          type: "button",
          class: "ekd-sd-chip",
          text: t,
          "aria-pressed": normalizeClock(draft.time) === t ? "true" : "false",
          onClick: () => {
            draft.time = t;
            renderDialog();
          },
        }));
        schedDetails.push(
          el(doc, "div", { class: "ekd-sd-subblock" }, [
            el(doc, "span", { class: "ekd-sd-sublabel", text: "执行日期" }),
            el(doc, "div", { class: "ekd-sd-time-row" }, [
              el(doc, "input", {
                type: "date",
                class: "ekd-sd-input",
                style: "width:168px",
                value: draft.date,
                onInput: e => { draft.date = e.target.value; updatePreview(); },
              }),
              el(doc, "div", { class: "ekd-sd-chips" }, [
                el(doc, "button", {
                  type: "button",
                  class: "ekd-sd-chip",
                  text: "今天",
                  "aria-pressed": draft.date === todayStr ? "true" : "false",
                  onClick: () => { draft.date = todayStr; renderDialog(); },
                }),
                el(doc, "button", {
                  type: "button",
                  class: "ekd-sd-chip",
                  text: "明天",
                  "aria-pressed": draft.date === tomorrowStr ? "true" : "false",
                  onClick: () => { draft.date = tomorrowStr; renderDialog(); },
                }),
              ]),
            ]),
          ]),
          el(doc, "div", { class: "ekd-sd-subblock" }, [
            el(doc, "span", { class: "ekd-sd-sublabel", text: "执行时间" }),
            el(doc, "div", { class: "ekd-sd-time-row" }, [
              createTimePicker(draft.time, next => {
                draft.time = next;
                renderDialog();
              }),
              el(doc, "div", { class: "ekd-sd-chips" }, quickChips),
            ]),
          ]),
        );
      } else if (draft.scheduleKind === "hourly") {
        schedDetails.push(el(doc, "div", { class: "ekd-sd-row" }, [
          el(doc, "span", { text: "每小时第" }),
          el(doc, "input", {
            type: "number",
            min: "0",
            max: "59",
            class: "ekd-sd-input",
            style: "width:84px;font-variant-numeric:tabular-nums",
            value: String(draft.minute),
            onInput: e => { draft.minute = Number.parseInt(e.target.value, 10) || 0; updatePreview(); },
          }),
          el(doc, "span", { text: "分执行" }),
        ]));
      } else if (draft.scheduleKind === "monthly") {
        schedDetails.push(
          el(doc, "div", { class: "ekd-sd-grid2" }, [
            el(doc, "input", {
              type: "text",
              class: "ekd-sd-input",
              placeholder: "每月几号，如 1, 15, 31",
              value: draft.daysText,
              onInput: e => { draft.daysText = e.target.value; updatePreview(); },
            }),
            createTimePicker(draft.time, next => {
              draft.time = next;
              renderDialog();
            }),
          ]),
          el(doc, "label", { class: "sched-check" }, [
            el(doc, "input", {
              type: "checkbox",
              checked: draft.lastDay,
              onChange: e => { draft.lastDay = e.target.checked; updatePreview(); },
            }),
            "包含每月最后一天（自动适配 28/29/30/31 日）",
          ]),
        );
      }

      if (draft.scheduleKind !== "once") {
        schedDetails.push(el(doc, "div", { class: "ekd-sd-validity" }, [
          el(doc, "div", { class: "ekd-sd-subblock" }, [
            el(doc, "span", { class: "ekd-sd-sublabel", text: "有效期范围（可选）" }),
            el(doc, "p", { class: "ekd-sd-hint", text: "留空表示保存后立即开始、按上述周期长期生效；仅在需要限定起止日期时填写" }),
          ]),
          el(doc, "div", { class: "ekd-sd-grid2" }, [
            el(doc, "div", { class: "ekd-sd-field" }, [
              el(doc, "span", { class: "ekd-sd-hint", text: "开始日期" }),
              el(doc, "div", { class: "ekd-sd-date-wrap" }, [
                el(doc, "input", {
                  type: "date",
                  class: "ekd-sd-input",
                  value: draft.startDate,
                  onInput: e => { draft.startDate = e.target.value; updatePreview(); },
                }),
                el(doc, "button", {
                  type: "button",
                  class: "ekd-sd-mini-btn",
                  text: "清空",
                  disabled: !draft.startDate,
                  onClick: () => { draft.startDate = ""; renderDialog(); },
                }),
              ]),
            ]),
            el(doc, "div", { class: "ekd-sd-field" }, [
              el(doc, "span", { class: "ekd-sd-hint", text: "结束日期" }),
              el(doc, "div", { class: "ekd-sd-date-wrap" }, [
                el(doc, "input", {
                  type: "date",
                  class: "ekd-sd-input",
                  value: draft.endDate,
                  onInput: e => { draft.endDate = e.target.value; updatePreview(); },
                }),
                el(doc, "button", {
                  type: "button",
                  class: "ekd-sd-mini-btn",
                  text: "清空",
                  disabled: !draft.endDate,
                  onClick: () => { draft.endDate = ""; renderDialog(); },
                }),
              ]),
            ]),
          ]),
        ]));
      }

      bodyChildren.push(el(doc, "div", { class: "ekd-sd-field" }, [
        el(doc, "div", { class: "ekd-sd-label" }, [el(doc, "strong", { text: "调度频率（按本机时区计算）" })]),
        el(doc, "div", { class: "ekd-sd-sched-card" }, [
          previewBar,
          freqTabs,
          ...schedDetails,
        ]),
      ]));

      const missedSeg = el(doc, "div", { class: "ekd-sd-seg", role: "group", "aria-label": "错过的运行" }, [
        el(doc, "button", {
          type: "button",
          text: "合并补跑一次（默认）",
          "aria-pressed": draft.missedPolicy === "catchup" ? "true" : "false",
          onClick: () => { draft.missedPolicy = "catchup"; renderDialog(); },
        }),
        el(doc, "button", {
          type: "button",
          text: "跳过",
          "aria-pressed": draft.missedPolicy === "skip" ? "true" : "false",
          onClick: () => { draft.missedPolicy = "skip"; renderDialog(); },
        }),
      ]);

      bodyChildren.push(el(doc, "div", { class: "ekd-sd-field" }, [
        el(doc, "div", { class: "ekd-sd-label" }, [el(doc, "strong", { text: "客户端离线时错过的运行" })]),
        missedSeg,
      ]));

      if (errorMsg) {
        bodyChildren.push(el(doc, "div", { class: "ekd-sd-error", role: "alert", text: errorMsg }));
      }

      async function submit(andRunNow) {
        if (busy) return;
        if (draft.permissionMode === "yolo" && gitStatus?.isGitRepo && !gitStatus.clean && !draft.confirmDirtyYolo) {
          errorMsg = "项目目录存在未提交的 Git 改动，请先清理工作区或勾选上方风险确认后再保存。";
          renderDialog();
          return;
        }
        busy = true;
        errorMsg = "";
        renderDialog();
        try {
          const timeoutMinutes = Math.max(1, Math.min(1440, Number.parseInt(String(draft.timeoutMinutes || 30), 10) || 30));
          const maxTurns = Math.max(0, Math.min(500, Number.parseInt(String(draft.maxTurns ?? 0), 10) || 0));
          const payload = {
            id: draft.id || undefined,
            name: draft.name,
            prompt: draft.prompt,
            cwd: draft.cwd,
            mode: draft.mode,
            sessionId: draft.mode === "bound_session" ? draft.sessionId : "",
            model: draft.model,
            effort: draft.effort,
            permissionMode: draft.permissionMode,
            missedPolicy: draft.missedPolicy,
            archiveOnRead: draft.mode === "new_session" ? Boolean(draft.archiveOnRead) : false,
            timeoutMinutes,
            maxTurns,
            confirmDirtyYolo: Boolean(draft.confirmDirtyYolo),
            enabled: draft.enabled,
            schedule: buildScheduleRule(draft),
          };
          const saved = await ctx.core.request("schedule.save", payload);
          if (andRunNow && saved?.id) {
            await ctx.core.request("schedule.runNow", { id: saved.id });
            state.activeTab = "runs";
          }
          closeDialog();
          await refreshAll();
        } catch (error) {
          errorMsg = error instanceof Error ? error.message : String(error);
          busy = false;
          renderDialog();
        }
      }

      const head = el(doc, "div", { class: "ekd-sd-head" }, [
        el(doc, "div", {}, [
          el(doc, "h2", { text: draft.id ? "编辑定时任务" : "新建定时任务" }),
          el(doc, "p", { text: "任务由 KimiCode+ 核心持久调度，仅在官方客户端 KAP 连通时执行。" }),
        ]),
        el(doc, "button", {
          type: "button",
          class: "ekd-sd-close",
          "aria-label": "关闭对话框",
          onClick: closeDialog,
        }, [makeSvg(doc, "M18 6 6 18M6 6l12 12", 16)]),
      ]);

      const foot = el(doc, "div", { class: "ekd-sd-foot" }, [
        el(doc, "span", { class: "ekd-sd-foot-note", text: "建议先用低频配置「立即试运行」一次，确认输出与权限符合预期。" }),
        el(doc, "button", { type: "button", class: "ekd-sd-btn", text: "取消", disabled: busy, onClick: closeDialog }),
        el(doc, "button", {
          type: "button",
          class: "ekd-sd-btn",
          text: busy ? "执行中…" : "立即试运行",
          disabled: busy,
          onClick: () => void submit(true),
        }),
        el(doc, "button", {
          type: "button",
          class: "ekd-sd-btn primary",
          text: busy ? "保存中…" : "保存",
          disabled: busy,
          onClick: () => void submit(false),
        }),
      ]);

      dialog.replaceChildren(head, el(doc, "div", { class: "ekd-sd-body" }, bodyChildren), foot);
    }

    renderDialog();
    dialog.addEventListener("close", () => {
      if (gitCheckTimer) {
        clearTimeout(gitCheckTimer);
        gitCheckTimer = null;
      }
      if (dialogEl === dialog) {
        dialogEl = null;
        dialog.remove();
      }
    });
    doc.body.append(dialog);
    dialogEl = dialog;
    if (typeof dialog.showModal === "function") {
      dialog.showModal();
    } else {
      dialog.setAttribute("open", "true");
    }
    if (draft.permissionMode === "yolo") {
      scheduleGitCheck(true);
    }
  }

  function renderTaskCard(doc, task, options = {}) {
    const running = state.runningTaskIds.includes(task.id);
    const statusPill = running
      ? el(doc, "span", { class: "sched-pill accent", text: "运行中" })
      : task.enabled
        ? el(doc, "span", { class: "sched-pill ok", text: "已启用" })
        : el(doc, "span", { class: "sched-pill", text: "已暂停" });
    const modePill = el(doc, "span", {
      class: "sched-pill",
      text: task.mode === "bound_session" ? "绑定会话" : "新开会话",
    });
    const yoloPill = task.permissionMode === "yolo"
      ? el(doc, "span", {
          class: "sched-pill warn",
          text: task.allowDirtyYolo ? "免审批·允许脏工作区" : "免审批",
        })
      : null;

    const toggleBtn = el(doc, "button", {
      type: "button",
      class: "sched-switch",
      "aria-pressed": task.enabled ? "true" : "false",
      text: task.enabled ? "启用中" : "已暂停",
      onClick: async () => {
        try {
          await ctx.core.request("schedule.toggle", { id: task.id, enabled: !task.enabled });
          await refreshAll();
        } catch (error) {
          state.error = error instanceof Error ? error.message : String(error);
          notify();
        }
      },
    });

    const metaRows = [
      el(doc, "div", {}, [el(doc, "strong", { text: "调度：" }), task.scheduleSummary || "—"]),
      el(doc, "div", {}, [
        el(doc, "strong", { text: "下次运行：" }),
        task.enabled ? (task.nextRunAt ? formatDateTime(task.nextRunAt) : "无后续计划") : "已暂停",
      ]),
    ];
    if (task.mode === "bound_session") {
      metaRows.push(el(doc, "div", {}, [
        el(doc, "strong", { text: "绑定会话：" }),
        task.sessionTitle || task.sessionId,
      ]));
      metaRows.push(el(doc, "div", {}, [
        el(doc, "strong", { text: "缓存保活参考：" }),
        keepAliveStatusText(task),
      ]));
    } else if (task.cwd) {
      metaRows.push(el(doc, "div", {}, [el(doc, "strong", { text: "目录：" }), task.cwd]));
    }
    const timeoutLabel = `超时 ${task.timeoutMinutes > 0 ? task.timeoutMinutes : 30} 分钟`;
    const turnsLabel = task.maxTurns > 0 ? `最多 ${task.maxTurns} 回合` : "回合不限";
    const archiveLabel = task.mode === "new_session"
      ? (task.archiveOnRead ? " · 已读自动归档" : " · 已读不归档")
      : "";
    metaRows.push(el(doc, "div", {}, [
      el(doc, "strong", { text: "护栏与归档：" }),
      `${timeoutLabel} · ${turnsLabel}${archiveLabel}`,
    ]));
    if (task.lastStatus) {
      const label = RUN_STATUS_LABELS[task.lastStatus] || task.lastStatus;
      metaRows.push(el(doc, "div", {}, [
        el(doc, "strong", { text: "上次结果：" }),
        `${label}${task.lastRunAt ? `（${formatDateTime(task.lastRunAt)}）` : ""}${task.lastSummary ? ` · ${task.lastSummary}` : ""}`,
      ]));
    }

    const actions = [
      el(doc, "button", {
        type: "button",
        class: "sched-btn",
        disabled: running || !state.kapConnected,
        text: "立即运行一次",
        onClick: async () => {
          try {
            await ctx.core.request("schedule.runNow", { id: task.id });
            if (!options.compact) state.activeTab = "runs";
            await refreshAll();
          } catch (error) {
            state.error = error instanceof Error ? error.message : String(error);
            notify();
          }
        },
      }),
      el(doc, "button", {
        type: "button",
        class: "sched-btn",
        text: "编辑",
        onClick: () => openTaskDialog(task),
      }),
      el(doc, "button", {
        type: "button",
        class: "sched-btn",
        text: task.enabled ? "暂停" : "恢复",
        onClick: async () => {
          try {
            await ctx.core.request("schedule.toggle", { id: task.id, enabled: !task.enabled });
            await refreshAll();
          } catch (error) {
            state.error = error instanceof Error ? error.message : String(error);
            notify();
          }
        },
      }),
      el(doc, "button", {
        type: "button",
        class: "sched-btn danger",
        text: "删除",
        onClick: async () => {
          try {
            await ctx.core.request("schedule.delete", { id: task.id });
            await refreshAll();
          } catch (error) {
            state.error = error instanceof Error ? error.message : String(error);
            notify();
          }
        },
      }),
    ];

    return el(doc, "div", { class: "sched-card" }, [
      el(doc, "div", { class: "sched-card-top" }, [
        el(doc, "span", { class: "sched-card-title", text: task.name }),
        modePill,
        yoloPill,
        statusPill,
        toggleBtn,
      ]),
      el(doc, "div", { class: "sched-meta" }, metaRows),
      el(doc, "div", { class: "sched-actions" }, actions),
    ]);
  }

  function renderRunCard(doc, run) {
    const statusLabel = RUN_STATUS_LABELS[run.status] || run.status;
    let pillClass = "sched-pill";
    if (run.status === "completed") pillClass += " ok";
    else if (run.status === "awaiting_approval") pillClass += " warn";
    else if (run.status === "failed") pillClass += " danger";
    else if (run.status === "skipped" && (run.failureKind === "quota_insufficient" || run.failureKind === "concurrency_limit" || run.failureKind === "busy_timeout")) pillClass += " warn";
    else if (run.status === "running" || run.status === "queued") pillClass += " accent";

    const failureLabel = run.failureKind ? (FAILURE_KIND_LABELS[run.failureKind] || run.failureKind) : "";
    const failurePill = failureLabel
      ? el(doc, "span", {
          class: `sched-pill ${run.status === "failed" ? "danger" : "warn"}`,
          text: failureLabel,
        })
      : null;
    const archivedPill = run.archived
      ? el(doc, "span", { class: "sched-pill ok", text: "已归档" })
      : null;

    const meta = [
      el(doc, "div", {}, [
        el(doc, "strong", { text: "开始时间：" }),
        formatDateTime(run.startedAt),
        run.finishedAt ? ` → ${formatDateTime(run.finishedAt)}` : "",
      ]),
    ];
    if (run.sessionTitle) {
      meta.push(el(doc, "div", {}, [el(doc, "strong", { text: "会话：" }), run.sessionTitle]));
    }
    if (run.message) {
      meta.push(el(doc, "div", {}, [el(doc, "strong", { text: "说明：" }), run.message]));
    }
    if (run.archiveError) {
      meta.push(el(doc, "div", { class: "sched-meta-warn", text: run.archiveError }));
    }
    if (run.status === "awaiting_approval") {
      meta.push(el(doc, "div", { class: "sched-meta-warn", text: "等待人工批准：请点击「打开会话」前往处理。" }));
    }

    const actions = [];
    if (run.sessionId) {
      actions.push(el(doc, "button", {
        type: "button",
        class: "sched-btn primary",
        text: "打开会话",
        onClick: async () => {
          try {
            if (run.unread) {
              await ctx.core.request("schedule.markRead", { id: run.id, read: true, archive: false });
            }
            closeMainArea();
            await ctx.sessions?.open(run.sessionId);
            await refreshAll();
          } catch (error) {
            state.error = error instanceof Error ? error.message : String(error);
            notify();
          }
        },
      }));
    }
    if (run.unread) {
      actions.push(el(doc, "button", {
        type: "button",
        class: "sched-btn",
        text: "标为已读",
        onClick: async () => {
          try {
            const res = await ctx.core.request("schedule.markRead", { id: run.id, read: true });
            await refreshAll();
            if (res?.archiveError) {
              state.error = res.archiveError;
              notify();
            }
          } catch (error) {
            state.error = error instanceof Error ? error.message : String(error);
            notify();
          }
        },
      }));
    }
    if (run.mode === "new_session" && run.sessionId && !run.archived) {
      actions.push(el(doc, "button", {
        type: "button",
        class: "sched-btn",
        text: "归档会话",
        onClick: async () => {
          try {
            await ctx.core.request("schedule.archiveRun", { id: run.id });
            await refreshAll();
          } catch (error) {
            state.error = error instanceof Error ? error.message : String(error);
            notify();
          }
        },
      }));
    }

    const highlighted = Boolean(state.highlightRunId && state.highlightRunId === run.id);
    return el(doc, "div", {
      class: `sched-card${run.unread ? " unread" : ""}${highlighted ? " highlighted" : ""}`,
      "data-ekd-run-id": run.id,
    }, [
      el(doc, "div", { class: "sched-card-top" }, [
        run.unread ? el(doc, "span", { class: "sched-unread-dot", title: "未读" }) : null,
        el(doc, "span", { class: "sched-card-title", text: run.taskName }),
        el(doc, "span", { class: "sched-pill", text: TRIGGER_LABELS[run.triggerKind] || run.triggerKind }),
        el(doc, "span", { class: pillClass, text: statusLabel }),
        failurePill,
        archivedPill,
      ]),
      el(doc, "div", { class: "sched-meta" }, meta),
      actions.length ? el(doc, "div", { class: "sched-actions" }, actions) : null,
    ]);
  }

  function renderMainArea(element) {
    const doc = element.ownerDocument;
    const host = doc.createElement("div");
    host.style.cssText = "display:flex;flex-direction:column;flex:1 1 auto;min-width:0;min-height:0;height:100%";
    const shadow = host.attachShadow({ mode: "open" });
    const style = doc.createElement("style");
    style.textContent = SHARED_CSS;
    const shell = doc.createElement("div");
    shell.className = "sched-shell";
    shadow.append(style, shell);
    element.append(host);

    const draw = () => {
      const head = el(doc, "div", { class: "sched-head" }, [
        el(doc, "div", { class: "sched-head-main" }, [
          el(doc, "div", { class: "sched-head-title-row" }, [
            el(doc, "h2", { class: "sched-title", text: "定时任务" }),
            el(doc, "span", {
              class: `sched-pill ${state.kapConnected ? "ok" : "warn"}`,
              text: state.kapConnected ? "KAP 已连接" : "KAP 未连接",
            }),
          ]),
          el(doc, "p", {
            class: "sched-subtitle",
            text: "按本机时区自动向新会话或绑定会话投递任务，支持补跑与执行记录追踪。",
          }),
        ]),
        el(doc, "div", { class: "sched-head-actions" }, [
          el(doc, "button", { type: "button", class: "sched-btn", onClick: () => void refreshAll() }, [
            makeSvg(doc, REFRESH_ICON, 13),
            "刷新",
          ]),
          el(doc, "button", { type: "button", class: "sched-btn primary", text: "+ 新建任务", onClick: () => openTaskDialog() }),
          el(doc, "button", {
            type: "button",
            class: "sched-btn quiet",
            "aria-label": "返回聊天",
            onClick: closeMainArea,
          }, [
            makeSvg(doc, BACK_ICON, 13),
            "返回聊天",
          ]),
        ]),
      ]);

      const tabs = el(doc, "div", { class: "sched-tabs", role: "tablist" }, [
        el(doc, "button", {
          type: "button",
          class: "sched-tab",
          role: "tab",
          "aria-selected": state.activeTab === "tasks" ? "true" : "false",
          onClick: () => { state.activeTab = "tasks"; draw(); },
        }, [
          "任务",
          el(doc, "span", { class: "sched-badge", text: String(state.tasks.length) }),
        ]),
        el(doc, "button", {
          type: "button",
          class: "sched-tab",
          role: "tab",
          "aria-selected": state.activeTab === "runs" ? "true" : "false",
          onClick: () => { state.activeTab = "runs"; draw(); },
        }, [
          "运行记录",
          state.unreadCount > 0 ? el(doc, "span", { class: "sched-badge unread", text: String(state.unreadCount) }) : null,
        ]),
      ]);

      const banners = [];
      if (!state.kapConnected) {
        banners.push(el(doc, "div", {
          class: "sched-banner warn",
          text: "官方客户端当前未连接。定时任务会在恢复连接后，按每个任务的「错过的运行」设置自动补跑一次或跳过。",
        }));
      }
      if (state.autoAttach && state.autoAttach.available !== false && !state.autoAttach.enabled) {
        banners.push(el(doc, "div", { class: "sched-banner warn", "data-ekd-autoattach-banner": "true" }, [
          el(doc, "div", { class: "sched-banner-row" }, [
            el(doc, "span", {
              text: "尚未开启「随 Kimi Code 自动接入」。Kimi Code 重启后，定时任务需要等您手动接入才会继续运行。",
            }),
            typeof ctx.ui?.openSettings === "function"
              ? el(doc, "button", {
                  type: "button",
                  class: "sched-btn",
                  text: "前往设置开启",
                  onClick: async () => {
                    try {
                      await ctx.ui.openSettings("");
                    } catch (error) {
                      state.error = error instanceof Error ? error.message : String(error);
                      notify();
                    }
                  },
                })
              : null,
          ]),
        ]));
      }
      banners.push(el(doc, "div", { class: "sched-info-strip", "data-ekd-sleep-note": "true" }, [
        el(doc, "span", {
          text: "运行期间是否防止休眠，取决于官方设置里的「防止休眠」；系统通知遵循官方「通用 → 系统通知」开关（仅补发运行失败、校验失败与被跳过）。",
        }),
        typeof ctx.ui?.openSettings === "function"
          ? el(doc, "button", {
              type: "button",
              class: "sched-link-btn",
              text: "打开官方设置 →",
              onClick: async () => {
                try {
                  await ctx.ui.openSettings("general");
                } catch (error) {
                  state.error = error instanceof Error ? error.message : String(error);
                  notify();
                }
              },
            })
          : null,
      ]));
      if (state.error) {
        banners.push(el(doc, "div", { class: "sched-banner error", role: "alert", text: state.error }));
      }

      const bodyChildren = [];
      if (state.loading) {
        bodyChildren.push(el(doc, "div", { class: "sched-banner", text: "正在加载定时任务与运行记录…" }));
      } else if (state.activeTab === "tasks") {
        let maxConcurrentDraft = state.prefs.maxConcurrent;
        let minQuotaDraft = state.prefs.minQuotaPercent;
        const guardrailsBar = el(doc, "div", { class: "sched-guardrails-bar", "data-ekd-global-guardrails": "true" }, [
          el(doc, "div", { class: "sched-guardrails-group" }, [
            el(doc, "strong", { style: "color:var(--sched-text);font-weight:600", text: "全局护栏" }),
            el(doc, "label", { class: "sched-guardrail-item" }, [
              "并发上限",
              el(doc, "input", {
                type: "number",
                min: "1",
                max: "10",
                class: "sched-input",
                "aria-label": "全局并发上限",
                value: String(maxConcurrentDraft),
                onInput: e => {
                  const parsed = Number.parseInt(e.target.value, 10);
                  if (Number.isFinite(parsed)) maxConcurrentDraft = parsed;
                },
              }),
            ]),
            el(doc, "label", { class: "sched-guardrail-item" }, [
              "额度低于",
              el(doc, "input", {
                type: "number",
                min: "0",
                max: "95",
                class: "sched-input",
                "aria-label": "额度跳过阈值（%）",
                value: String(minQuotaDraft),
                onInput: e => {
                  const parsed = Number.parseInt(e.target.value, 10);
                  if (Number.isFinite(parsed)) minQuotaDraft = parsed;
                },
              }),
              "% 时跳过（0 为关闭）",
            ]),
          ]),
          el(doc, "div", { class: "sched-guardrails-group" }, [
            state.prefsNotice ? el(doc, "span", { style: "color:var(--sched-ok)", text: state.prefsNotice }) : null,
            el(doc, "button", {
              type: "button",
              class: "sched-btn",
              text: "保存护栏",
              onClick: async () => {
                try {
                  state.prefsNotice = "";
                  await ctx.core.request("schedule.prefs", {
                    maxConcurrent: Math.max(1, Math.min(10, Number(maxConcurrentDraft) || 2)),
                    minQuotaPercent: Math.max(0, Math.min(95, Number(minQuotaDraft) || 0)),
                  });
                  state.prefsNotice = "已保存";
                  await refreshAll();
                } catch (error) {
                  state.error = error instanceof Error ? error.message : String(error);
                  notify();
                }
              },
            }),
          ]),
        ]);
        bodyChildren.push(guardrailsBar);

        if (state.tasks.length === 0) {
          bodyChildren.push(el(doc, "div", { class: "sched-empty" }, [
            el(doc, "strong", { text: "还没有定时任务" }),
            el(doc, "p", { text: "创建一个每天或按周期自动执行的任务，也可以先从只读分析模板开始，并先「立即试运行」一次。" }),
            el(doc, "div", { class: "sched-tpl-row" }, READONLY_TEMPLATES.map(tpl =>
              el(doc, "button", {
                type: "button",
                class: "sched-btn",
                text: tpl.label,
                onClick: () => openTaskDialog({ name: tpl.name, prompt: tpl.prompt }),
              }),
            )),
            el(doc, "button", { type: "button", class: "sched-btn primary", text: "+ 新建定时任务", onClick: () => openTaskDialog() }),
          ]));
        } else {
          for (const task of state.tasks) {
            bodyChildren.push(renderTaskCard(doc, task));
          }
        }
      } else {
        const filterSel = el(doc, "select", {
          class: "sched-select",
          "aria-label": "按任务筛选",
          onChange: e => { state.runFilterTaskId = e.target.value; void refreshAll(); },
        }, [
          el(doc, "option", { value: "", text: "全部任务" }),
          ...state.tasks.map(t => el(doc, "option", { value: t.id, text: t.name })),
        ]);
        filterSel.value = state.runFilterTaskId;

        const toolbar = el(doc, "div", { class: "sched-toolbar" }, [
          filterSel,
          el(doc, "label", { class: "sched-check" }, [
            el(doc, "input", {
              type: "checkbox",
              checked: Boolean(state.prefs?.autoMarkRead),
              onChange: async e => {
                try {
                  await ctx.core.request("schedule.prefs", { autoMarkRead: e.target.checked });
                  await refreshAll();
                } catch (error) {
                  state.error = error instanceof Error ? error.message : String(error);
                  notify();
                }
              },
            }),
            "完成后自动标为已读",
          ]),
          el(doc, "button", {
            type: "button",
            class: "sched-btn",
            disabled: state.unreadCount === 0,
            text: "全部标为已读",
            onClick: async () => {
              try {
                const res = await ctx.core.request("schedule.markAllRead", {});
                await refreshAll();
                if (res?.archiveError) {
                  state.error = res.archiveError;
                  notify();
                }
              } catch (error) {
                state.error = error instanceof Error ? error.message : String(error);
                notify();
              }
            },
          }),
        ]);
        bodyChildren.push(toolbar);
        if (state.runs.length === 0) {
          bodyChildren.push(el(doc, "div", { class: "sched-empty" }, [
            el(doc, "strong", { text: "暂无运行记录" }),
            el(doc, "p", { text: "任务到点执行或点击「立即运行一次」后，运行结果会按时间倒序出现在这里。" }),
          ]));
        } else {
          for (const run of state.runs) {
            bodyChildren.push(renderRunCard(doc, run));
          }
        }
      }

      shell.replaceChildren(head, tabs, ...banners, el(doc, "div", { class: "sched-body" }, bodyChildren));
    };

    listeners.add(draw);
    draw();
    void refreshAll();
    return () => {
      listeners.delete(draw);
      host.remove();
    };
  }

  function openMainArea(initialTab) {
    if (disposed) return;
    if (initialTab) state.activeTab = initialTab;
    if (!ctx.mainArea) return;
    if (mainHandle && !state.mainHostEl?.isConnected) {
      mainHandle = null;
    }
    if (!mainHandle) {
      mainHandle = ctx.mainArea.mount({
        mode: "page",
        render: element => {
          const cleanup = renderMainArea(element);
          state.mainHostEl = element;
          return () => {
            if (state.mainHostEl === element) {
              state.mainHostEl = null;
              mainHandle = null;
            }
            cleanup();
          };
        },
      });
    } else {
      notify();
    }
    void refreshAll();
  }

  function renderSessionRightPanel(element) {
    const doc = element.ownerDocument;
    const host = doc.createElement("div");
    host.style.cssText = "display:flex;flex-direction:column;flex:1 1 auto;min-width:0;min-height:0;height:100%";
    const shadow = host.attachShadow({ mode: "open" });
    const style = doc.createElement("style");
    style.textContent = SHARED_CSS;
    const shell = doc.createElement("div");
    shell.className = "sched-shell compact";
    shadow.append(style, shell);
    element.append(host);

    const draw = () => {
      const activeId = ctx.sessions?.activeId?.() || state.rightPanelSessionId || "";
      const activeSession = state.sessions.find(s => s.id === activeId);
      const boundTasks = activeId ? state.tasks.filter(t => t.mode === "bound_session" && t.sessionId === activeId) : [];

      const head = el(doc, "div", { class: "sched-head" }, [
        el(doc, "div", { class: "sched-head-main" }, [
          el(doc, "h2", { class: "sched-title", text: "本会话定时任务" }),
          el(doc, "p", { class: "sched-subtitle", text: "查看并管理绑定当前会话的定时任务" }),
        ]),
        el(doc, "div", { class: "sched-head-actions" }, [
          el(doc, "button", {
            type: "button",
            class: "sched-btn",
            text: "全部任务",
            onClick: () => openMainArea("tasks"),
          }),
          el(doc, "button", {
            type: "button",
            class: "sched-btn primary",
            disabled: !activeId,
            text: "+ 为本会话创建定时任务",
            onClick: () => openTaskDialog({
              mode: "bound_session",
              sessionId: activeId,
              cwd: activeSession?.cwd || "",
            }),
          }),
        ]),
      ]);

      const bodyChildren = [];
      if (!activeId) {
        bodyChildren.push(el(doc, "div", { class: "sched-empty" }, [
          el(doc, "strong", { text: "当前没有活动会话" }),
          el(doc, "p", { text: "打开任意会话后再从会话信息进入，即可查看并创建绑定该会话的定时任务。" }),
        ]));
      } else {
        bodyChildren.push(el(doc, "div", {
          class: "sched-banner",
          text: `当前会话：${activeSession?.title || activeId}${activeSession?.cwd ? `（${activeSession.cwd}）` : ""}`,
        }));
        if (state.loading) {
          bodyChildren.push(el(doc, "div", { class: "sched-banner", text: "加载中…" }));
        } else if (boundTasks.length === 0) {
          bodyChildren.push(el(doc, "div", { class: "sched-empty" }, [
            el(doc, "strong", { text: "本会话还没有绑定定时任务" }),
            el(doc, "p", { text: "创建后，任务会按调度自动向本会话投递提示词；若会话正忙，最多等待 10 分钟。" }),
            el(doc, "button", {
              type: "button",
              class: "sched-btn primary",
              text: "为本会话创建定时任务",
              onClick: () => openTaskDialog({
                mode: "bound_session",
                sessionId: activeId,
                cwd: activeSession?.cwd || "",
              }),
            }),
          ]));
        } else {
          for (const task of boundTasks) {
            bodyChildren.push(renderTaskCard(doc, task, { compact: true }));
          }
        }
      }

      shell.replaceChildren(head, el(doc, "div", { class: "sched-body" }, bodyChildren));
    };

    listeners.add(draw);
    draw();
    void refreshAll();
    return () => {
      listeners.delete(draw);
      host.remove();
    };
  }

  if (ctx.sidebarNav) {
    ctx.sidebarNav.add({
      id: "scheduler",
      title: "定时任务",
      icon: SCHEDULER_ICON,
      open: () => openMainArea(),
    });
  }

  if (ctx.rightPanel) {
    panelHandle = ctx.rightPanel.mount({
      title: "定时任务",
      render: renderSessionRightPanel,
    });
  }

  if (ctx.entry) {
    ctx.entry.add({
      id: "scheduler-session",
      title: "定时任务",
      icon: SCHEDULER_ICON,
      open: () => {
        state.rightPanelSessionId = ctx.sessions?.activeId?.() || "";
        if (panelHandle) {
          panelHandle.open();
          notify();
          void refreshAll();
        } else {
          openMainArea("tasks");
        }
      },
    });
  }

  ctx.onDispose(() => {
    disposed = true;
    if (typeof window !== "undefined" && typeof window.removeEventListener === "function") {
      window.removeEventListener("ekd:active-session", onActiveSessionChange);
      window.removeEventListener("ekd:scheduler-open-runs", onOpenRunsEvent);
      window.removeEventListener("ekd:attach-changed", onAttachChanged);
    }
    unsubSchedule.dispose();
    closeDialog();
    dialogStyleEl?.remove();
    dialogStyleEl = null;
    closeMainArea();
    panelHandle?.dispose();
    panelHandle = null;
    listeners.clear();
  });

  return { status: "active" };
}
