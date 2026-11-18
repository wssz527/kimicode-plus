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
  { value: "manual", label: "每次询问（manual · 推荐）", note: "只读工具自动放行，写入与命令需人工批准。" },
  { value: "yolo", label: "按需询问（yolo）", note: "常规编辑与命令自动执行，高风险操作与提问需批准。" },
  { value: "auto", label: "完全自动（auto）", note: "除 Plan 审核外自动执行；无人值守如触发审批仍会暂停等待处理。" },
];

export const RUN_STATUS_LABELS = {
  queued: "排队中",
  running: "运行中",
  completed: "已完成",
  failed: "失败",
  awaiting_approval: "等待批准",
  skipped: "已跳过",
};

export const TRIGGER_LABELS = {
  scheduled: "定时",
  catchup: "补跑",
  manual: "手动",
};

const WEEKDAY_LABELS = [
  { value: 1, label: "周一" },
  { value: 2, label: "周二" },
  { value: 3, label: "周三" },
  { value: 4, label: "周四" },
  { value: 5, label: "周五" },
  { value: 6, label: "周六" },
  { value: 0, label: "周日" },
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

export function defaultDraft(prefill = {}) {
  const today = new Date();
  const yyyy = today.getFullYear();
  const mm = String(today.getMonth() + 1).padStart(2, "0");
  const dd = String(today.getDate()).padStart(2, "0");
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
    enabled: prefill.enabled !== undefined ? Boolean(prefill.enabled) : true,
    scheduleKind: prefill.schedule?.kind || "daily",
    date: prefill.schedule?.date || `${yyyy}-${mm}-${dd}`,
    time: prefill.schedule?.time || "09:00",
    minute: typeof prefill.schedule?.minute === "number" ? prefill.schedule.minute : 0,
    weekdays: Array.isArray(prefill.schedule?.weekdays) ? [...prefill.schedule.weekdays] : [1],
    daysText: Array.isArray(prefill.schedule?.daysOfMonth) && prefill.schedule.daysOfMonth.length
      ? prefill.schedule.daysOfMonth.join(",")
      : "1",
    lastDay: Boolean(prefill.schedule?.lastDay),
    intervalValue: prefill.schedule?.intervalValue || 30,
    intervalUnit: prefill.schedule?.intervalUnit || "minutes",
    testMode: Boolean(prefill.schedule?.testMode),
    timesText: Array.isArray(prefill.schedule?.times) && prefill.schedule.times.length
      ? prefill.schedule.times.join(", ")
      : "09:00, 18:00",
    startDate: prefill.schedule?.startDate || "",
    endDate: prefill.schedule?.endDate || "",
  };
}

export function buildScheduleRule(draft) {
  const rule = { kind: draft.scheduleKind || "daily" };
  if (draft.startDate) rule.startDate = String(draft.startDate).trim();
  if (draft.endDate) rule.endDate = String(draft.endDate).trim();
  switch (rule.kind) {
    case "once":
      rule.date = String(draft.date || "").trim();
      rule.time = String(draft.time || "09:00").trim();
      break;
    case "hourly":
      rule.minute = Math.max(0, Math.min(59, Number.parseInt(String(draft.minute ?? 0), 10) || 0));
      break;
    case "daily":
    case "weekdays":
      rule.time = String(draft.time || "09:00").trim();
      break;
    case "weekly":
      rule.time = String(draft.time || "09:00").trim();
      rule.weekdays = Array.isArray(draft.weekdays) ? [...draft.weekdays] : [];
      break;
    case "monthly": {
      rule.time = String(draft.time || "09:00").trim();
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
    case "times":
      rule.times = String(draft.timesText || "")
        .split(/[,，\s]+/)
        .map(item => item.trim())
        .filter(Boolean);
      if (Array.isArray(draft.weekdays) && draft.weekdays.length > 0) {
        rule.weekdays = [...draft.weekdays];
      }
      break;
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
}
.sched-head{
  display:flex;
  align-items:center;
  gap:8px;
  padding:12px 14px 10px;
  border-bottom:1px solid var(--sched-line);
}
.sched-title{margin:0;font-size:15px;font-weight:600;letter-spacing:-0.01em}
.sched-head-actions{margin-left:auto;display:flex;align-items:center;gap:6px;flex-wrap:wrap}
.sched-tabs{
  display:flex;
  align-items:center;
  gap:4px;
  padding:8px 14px 0;
  border-bottom:1px solid var(--sched-line);
}
.sched-tab{
  position:relative;
  display:inline-flex;
  align-items:center;
  gap:6px;
  padding:6px 10px 8px;
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
  margin:10px 14px 0;
  padding:8px 11px;
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
  padding:12px 14px 18px;
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
  padding:11px 12px;
  border:1px solid var(--sched-line);
  border-radius:10px;
  background:var(--sched-fill);
}
.sched-card.unread{
  border-color:var(--sched-accent);
}
.sched-card-top{display:flex;align-items:center;gap:8px;min-width:0}
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
  height:27px;
  padding:0 10px;
  border:1px solid var(--sched-line);
  border-radius:6px;
  background:transparent;
  color:var(--sched-text);
  font:inherit;
  font-size:12px;
  cursor:pointer;
}
.sched-btn:hover:not([disabled]){background:var(--sched-hover)}
.sched-btn[disabled]{opacity:.48;cursor:not-allowed}
.sched-btn.primary{
  border-color:transparent;
  background:var(--sched-accent);
  color:var(--color-text-on-accent, #fff);
}
.sched-btn.danger{color:var(--sched-danger)}
.sched-btn.quiet{border-color:transparent}
.sched-select,.sched-input{
  height:28px;
  padding:0 8px;
  border:1px solid var(--sched-line);
  border-radius:6px;
  background:var(--sched-bg);
  color:var(--sched-text);
  font:inherit;
  font-size:12.5px;
}
.sched-empty{
  padding:22px 14px;
  border:1px dashed var(--sched-line);
  border-radius:10px;
  text-align:center;
  display:flex;
  flex-direction:column;
  align-items:center;
  gap:10px;
  color:var(--sched-muted);
}
.sched-empty p{margin:0;font-size:12.5px;max-width:340px;line-height:1.55}
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
  width:min(620px,calc(100vw - 36px));
  max-height:min(820px,calc(100vh - 48px));
  padding:0;
  border:1px solid var(--sched-line);
  border-radius:12px;
  background:var(--sched-bg);
  color:var(--sched-text);
  font:13.5px/1.5 var(--font-ui,system-ui,-apple-system,sans-serif);
  box-shadow:0 24px 64px rgba(0,0,0,.32);
}
.ekd-sched-dialog::backdrop{background:rgba(0,0,0,.42)}
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
  max-height:min(64vh,580px);
}
.ekd-sd-field{display:flex;flex-direction:column;gap:5px}
.ekd-sd-label{display:flex;align-items:baseline;gap:8px;font-size:12.5px}
.ekd-sd-label strong{font-weight:600}
.ekd-sd-hint{font-size:12px;color:var(--sched-muted);margin:0}
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
.ekd-sd-chips{display:flex;flex-wrap:wrap;gap:6px}
.ekd-sd-chip{
  height:26px;padding:0 11px;
  border:1px solid var(--sched-line);
  border-radius:999px;background:transparent;color:inherit;
  font:inherit;font-size:12px;cursor:pointer;
}
.ekd-sd-chip:hover{background:var(--sched-hover)}
.ekd-sd-chip[aria-pressed="true"]{
  border-color:var(--sched-accent);
  background:var(--sched-accent-soft);
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
.ekd-sd-error{
  padding:8px 10px;border-radius:7px;
  background:color-mix(in srgb, var(--sched-danger) 12%, transparent);color:var(--sched-danger);
  font-size:12.5px;
}
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
  font:inherit;font-size:12.5px;cursor:pointer;
}
.ekd-sd-btn:hover:not([disabled]){background:var(--sched-hover)}
.ekd-sd-btn[disabled]{opacity:.5;cursor:not-allowed}
.ekd-sd-btn.primary{
  border-color:transparent;
  background:var(--sched-accent);
  color:var(--color-text-on-accent,#fff);
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
    prefs: { autoMarkRead: false },
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
      state.prefs = taskRes?.prefs || { autoMarkRead: false };
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
    if (!draft.cwd && state.sessions.length > 0) {
      const matched = state.sessions.find(s => s.id === draft.sessionId) || state.sessions[0];
      if (matched?.cwd) draft.cwd = matched.cwd;
    }
    let busy = false;
    let errorMsg = "";

    const dialog = doc.createElement("dialog");
    dialog.className = "ekd-sched-dialog";
    dialog.setAttribute("aria-label", draft.id ? "编辑定时任务" : "新建定时任务");

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

      bodyChildren.push(el(doc, "div", { class: "ekd-sd-field" }, [
        el(doc, "label", { class: "ekd-sd-label" }, [el(doc, "strong", { text: "提示词" })]),
        el(doc, "textarea", {
          class: "ekd-sd-textarea",
          placeholder: "任务到点后将自动发送此提示词…",
          value: draft.prompt,
          onInput: e => { draft.prompt = e.target.value; },
        }),
      ]));

      const modeSeg = el(doc, "div", { class: "ekd-sd-seg", role: "group", "aria-label": "执行方式" }, [
        el(doc, "button", {
          type: "button",
          text: "新开会话",
          "aria-pressed": draft.mode === "new_session" ? "true" : "false",
          onClick: () => { draft.mode = "new_session"; renderDialog(); },
        }),
        el(doc, "button", {
          type: "button",
          text: "绑定已有会话",
          "aria-pressed": draft.mode === "bound_session" ? "true" : "false",
          onClick: () => { draft.mode = "bound_session"; renderDialog(); },
        }),
      ]);

      bodyChildren.push(el(doc, "div", { class: "ekd-sd-field" }, [
        el(doc, "div", { class: "ekd-sd-label" }, [el(doc, "strong", { text: "执行方式" })]),
        modeSeg,
      ]));

      if (draft.mode === "new_session") {
        bodyChildren.push(el(doc, "div", { class: "ekd-sd-field" }, [
          el(doc, "label", { class: "ekd-sd-label" }, [el(doc, "strong", { text: "项目目录" })]),
          el(doc, "input", {
            type: "text",
            class: "ekd-sd-input",
            placeholder: "/path/to/project",
            value: draft.cwd,
            onInput: e => { draft.cwd = e.target.value; },
          }),
        ]));
      } else {
        const sessSelect = el(doc, "select", {
          class: "ekd-sd-select",
          onChange: e => {
            draft.sessionId = e.target.value;
            const found = state.sessions.find(s => s.id === draft.sessionId);
            if (found?.cwd && !draft.cwd) draft.cwd = found.cwd;
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
        onChange: e => { draft.permissionMode = e.target.value; renderDialog(); },
      }, PERMISSION_MODES.map(m => el(doc, "option", { value: m.value, text: m.label })));
      permSelect.value = draft.permissionMode;
      const permNote = PERMISSION_MODES.find(m => m.value === draft.permissionMode)?.note || "";

      bodyChildren.push(el(doc, "div", { class: "ekd-sd-field" }, [
        el(doc, "label", { class: "ekd-sd-label" }, [el(doc, "strong", { text: "权限模式" })]),
        permSelect,
        el(doc, "p", { class: "ekd-sd-hint", text: `${permNote} 无人值守出现任何审批请求时绝不自动批准，会暂停并记为「等待批准」。` }),
      ]));

      const kindSelect = el(doc, "select", {
        class: "ekd-sd-select",
        onChange: e => { draft.scheduleKind = e.target.value; renderDialog(); },
      }, [
        el(doc, "option", { value: "daily", text: "每天一次（默认）" }),
        el(doc, "option", { value: "weekdays", text: "每个工作日（周一至周五）" }),
        el(doc, "option", { value: "hourly", text: "每小时一次" }),
        el(doc, "option", { value: "weekly", text: "每周（自选星期）" }),
        el(doc, "option", { value: "monthly", text: "每月（指定日期 / 月末）" }),
        el(doc, "option", { value: "once", text: "单次（指定日期时间）" }),
        el(doc, "option", { value: "interval", text: "自定义间隔（分钟 / 小时）" }),
        el(doc, "option", { value: "times", text: "自定义一天多时点" }),
      ]);
      kindSelect.value = draft.scheduleKind;

      const schedDetails = [];
      if (draft.scheduleKind === "once") {
        schedDetails.push(el(doc, "div", { class: "ekd-sd-grid2" }, [
          el(doc, "input", { type: "date", class: "ekd-sd-input", value: draft.date, onInput: e => { draft.date = e.target.value; } }),
          el(doc, "input", { type: "time", class: "ekd-sd-input", value: draft.time, onInput: e => { draft.time = e.target.value; } }),
        ]));
      } else if (draft.scheduleKind === "hourly") {
        schedDetails.push(el(doc, "div", { class: "ekd-sd-row" }, [
          el(doc, "span", { text: "每小时第" }),
          el(doc, "input", {
            type: "number", min: "0", max: "59", class: "ekd-sd-input",
            style: "width:84px",
            value: String(draft.minute),
            onInput: e => { draft.minute = Number.parseInt(e.target.value, 10) || 0; },
          }),
          el(doc, "span", { text: "分执行" }),
        ]));
      } else if (draft.scheduleKind === "daily" || draft.scheduleKind === "weekdays") {
        schedDetails.push(el(doc, "div", { class: "ekd-sd-row" }, [
          el(doc, "span", { text: "执行时间" }),
          el(doc, "input", { type: "time", class: "ekd-sd-input", style: "width:140px", value: draft.time, onInput: e => { draft.time = e.target.value; } }),
        ]));
      } else if (draft.scheduleKind === "weekly") {
        const dayChips = el(doc, "div", { class: "ekd-sd-chips" }, WEEKDAY_LABELS.map(wd => {
          const active = draft.weekdays.includes(wd.value);
          return el(doc, "button", {
            type: "button",
            class: "ekd-sd-chip",
            text: wd.label,
            "aria-pressed": active ? "true" : "false",
            onClick: () => {
              if (active) draft.weekdays = draft.weekdays.filter(v => v !== wd.value);
              else draft.weekdays = [...draft.weekdays, wd.value];
              renderDialog();
            },
          });
        }));
        schedDetails.push(dayChips, el(doc, "div", { class: "ekd-sd-row" }, [
          el(doc, "span", { text: "执行时间" }),
          el(doc, "input", { type: "time", class: "ekd-sd-input", style: "width:140px", value: draft.time, onInput: e => { draft.time = e.target.value; } }),
        ]));
      } else if (draft.scheduleKind === "monthly") {
        schedDetails.push(el(doc, "div", { class: "ekd-sd-grid2" }, [
          el(doc, "input", {
            type: "text", class: "ekd-sd-input", placeholder: "每月几号，如 1, 15, 31",
            value: draft.daysText,
            onInput: e => { draft.daysText = e.target.value; },
          }),
          el(doc, "input", { type: "time", class: "ekd-sd-input", value: draft.time, onInput: e => { draft.time = e.target.value; } }),
        ]));
        schedDetails.push(el(doc, "label", { class: "sched-check" }, [
          el(doc, "input", { type: "checkbox", checked: draft.lastDay, onChange: e => { draft.lastDay = e.target.checked; } }),
          "包含每月最后一天（自动适配 28/29/30/31 日）",
        ]));
      } else if (draft.scheduleKind === "interval") {
        const unitSel = el(doc, "select", {
          class: "ekd-sd-select",
          style: "width:110px",
          onChange: e => { draft.intervalUnit = e.target.value; renderDialog(); },
        }, [
          el(doc, "option", { value: "minutes", text: "分钟" }),
          el(doc, "option", { value: "hours", text: "小时" }),
        ]);
        unitSel.value = draft.intervalUnit;
        schedDetails.push(el(doc, "div", { class: "ekd-sd-row" }, [
          el(doc, "span", { text: "每隔" }),
          el(doc, "input", {
            type: "number", min: "1", class: "ekd-sd-input", style: "width:96px",
            value: String(draft.intervalValue),
            onInput: e => { draft.intervalValue = Number.parseInt(e.target.value, 10) || 1; },
          }),
          unitSel,
        ]));
        schedDetails.push(el(doc, "label", { class: "sched-check" }, [
          el(doc, "input", { type: "checkbox", checked: draft.testMode, onChange: e => { draft.testMode = e.target.checked; } }),
          "测试模式（允许低至每 1 分钟运行一次，建议仅用于短期验证）",
        ]));
      } else if (draft.scheduleKind === "times") {
        const dayChips = el(doc, "div", { class: "ekd-sd-chips" }, WEEKDAY_LABELS.map(wd => {
          const active = draft.weekdays.includes(wd.value);
          return el(doc, "button", {
            type: "button",
            class: "ekd-sd-chip",
            text: wd.label,
            "aria-pressed": active ? "true" : "false",
            onClick: () => {
              if (active) draft.weekdays = draft.weekdays.filter(v => v !== wd.value);
              else draft.weekdays = [...draft.weekdays, wd.value];
              renderDialog();
            },
          });
        }));
        schedDetails.push(
          el(doc, "input", {
            type: "text", class: "ekd-sd-input",
            placeholder: "多个时点用逗号分隔，如 09:00, 14:30, 18:00",
            value: draft.timesText,
            onInput: e => { draft.timesText = e.target.value; },
          }),
          el(doc, "p", { class: "ekd-sd-hint", text: "可选限定星期几（不选表示每天）：" }),
          dayChips,
        );
      }

      schedDetails.push(el(doc, "div", { class: "ekd-sd-grid2" }, [
        el(doc, "div", { class: "ekd-sd-field" }, [
          el(doc, "span", { class: "ekd-sd-hint", text: "生效开始日期（可选）" }),
          el(doc, "input", { type: "date", class: "ekd-sd-input", value: draft.startDate, onInput: e => { draft.startDate = e.target.value; } }),
        ]),
        el(doc, "div", { class: "ekd-sd-field" }, [
          el(doc, "span", { class: "ekd-sd-hint", text: "生效结束日期（可选）" }),
          el(doc, "input", { type: "date", class: "ekd-sd-input", value: draft.endDate, onInput: e => { draft.endDate = e.target.value; } }),
        ]),
      ]));

      bodyChildren.push(el(doc, "div", { class: "ekd-sd-field" }, [
        el(doc, "label", { class: "ekd-sd-label" }, [el(doc, "strong", { text: "调度频率（按本机时区计算）" })]),
        kindSelect,
        ...schedDetails,
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
        busy = true;
        errorMsg = "";
        renderDialog();
        try {
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
    else if (run.status === "running" || run.status === "queued") pillClass += " accent";

    const meta = [
      el(doc, "div", {}, [
        el(doc, "strong", { text: "开始时间：" }),
        formatDateTime(run.startedAt),
        run.finishedAt ? ` → ${formatDateTime(run.finishedAt)}` : "",
      ]),
    ];
    if (run.message) {
      meta.push(el(doc, "div", {}, [el(doc, "strong", { text: "说明：" }), run.message]));
    }
    if (run.status === "awaiting_approval") {
      meta.push(el(doc, "div", { style: "color:#b45309;font-weight:500", text: "等待人工批准：无人值守不会自动批准，请点击「打开会话」前往处理。" }));
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
              await ctx.core.request("schedule.markRead", { id: run.id, read: true });
            }
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
            await ctx.core.request("schedule.markRead", { id: run.id, read: true });
            await refreshAll();
          } catch (error) {
            state.error = error instanceof Error ? error.message : String(error);
            notify();
          }
        },
      }));
    }

    return el(doc, "div", { class: `sched-card${run.unread ? " unread" : ""}` }, [
      el(doc, "div", { class: "sched-card-top" }, [
        run.unread ? el(doc, "span", { class: "sched-unread-dot", title: "未读" }) : null,
        el(doc, "span", { class: "sched-card-title", text: run.taskName }),
        el(doc, "span", { class: "sched-pill", text: TRIGGER_LABELS[run.triggerKind] || run.triggerKind }),
        el(doc, "span", { class: pillClass, text: statusLabel }),
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
        el(doc, "h2", { class: "sched-title", text: "定时任务" }),
        el(doc, "span", {
          class: `sched-pill ${state.kapConnected ? "ok" : "warn"}`,
          text: state.kapConnected ? "KAP 已连接" : "KAP 未连接",
        }),
        el(doc, "div", { class: "sched-head-actions" }, [
          el(doc, "button", { type: "button", class: "sched-btn", text: "刷新", onClick: () => void refreshAll() }),
          el(doc, "button", { type: "button", class: "sched-btn primary", text: "+ 新建任务", onClick: () => openTaskDialog() }),
          el(doc, "button", {
            type: "button",
            class: "sched-btn quiet",
            "aria-label": "关闭定时任务视图",
            text: "关闭",
            onClick: () => {
              mainHandle?.dispose();
              mainHandle = null;
            },
          }),
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
      if (state.error) {
        banners.push(el(doc, "div", { class: "sched-banner error", role: "alert", text: state.error }));
      }

      const bodyChildren = [];
      if (state.loading) {
        bodyChildren.push(el(doc, "div", { class: "sched-banner", text: "正在加载定时任务与运行记录…" }));
      } else if (state.activeTab === "tasks") {
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
                await ctx.core.request("schedule.markAllRead", {});
                await refreshAll();
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
        width: 460,
        minWidth: 360,
        maxWidth: 820,
        render: element => {
          const cleanup = renderMainArea(element);
          state.mainHostEl = element;
          return () => {
            if (state.mainHostEl === element) state.mainHostEl = null;
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
    shell.className = "sched-shell";
    shadow.append(style, shell);
    element.append(host);

    const draw = () => {
      const activeId = ctx.sessions?.activeId?.() || state.rightPanelSessionId || "";
      const activeSession = state.sessions.find(s => s.id === activeId);
      const boundTasks = activeId ? state.tasks.filter(t => t.mode === "bound_session" && t.sessionId === activeId) : [];

      const head = el(doc, "div", { class: "sched-head" }, [
        el(doc, "h2", { class: "sched-title", text: "本会话定时任务" }),
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
    unsubSchedule.dispose();
    closeDialog();
    dialogStyleEl?.remove();
    dialogStyleEl = null;
    mainHandle?.dispose();
    mainHandle = null;
    panelHandle?.dispose();
    panelHandle = null;
    listeners.clear();
  });

  return { status: "active" };
}
