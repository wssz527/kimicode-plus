const dateTimeFormatter = new Intl.DateTimeFormat("zh-CN", {
  year: "numeric",
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
});

const dateFormatter = new Intl.DateTimeFormat("zh-CN", {
  year: "numeric",
  month: "2-digit",
  day: "2-digit",
});

export function toDate(value: unknown): Date | null {
  if (value === null || value === undefined || value === "") return null;
  if (value instanceof Date) return Number.isNaN(value.getTime()) ? null : value;
  if (typeof value === "number") {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? null : date;
  }
  if (typeof value !== "string") return null;
  const text = value.trim();
  if (!text) return null;
  if (/^\d{10,13}$/.test(text)) {
    const numeric = Number(text);
    const date = new Date(text.length === 10 ? numeric * 1000 : numeric);
    return Number.isNaN(date.getTime()) ? null : date;
  }
  const date = new Date(text);
  return Number.isNaN(date.getTime()) ? null : date;
}

export function fmtDateTime(value: unknown): string {
  const date = toDate(value);
  return date ? dateTimeFormatter.format(date).replace(/\//g, "-") : "UNKNOWN";
}

export function fmtDate(value: unknown): string {
  const date = toDate(value);
  return date ? dateFormatter.format(date).replace(/\//g, "-") : "UNKNOWN";
}

export function fmtTime(value: unknown): string {
  const date = toDate(value);
  if (!date) return "UNKNOWN";
  return date.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
}

export function fmtDuration(ms: number | null | undefined): string {
  if (typeof ms !== "number" || !Number.isFinite(ms) || ms < 0) return "UNKNOWN";
  const totalSeconds = Math.floor(ms / 1000);
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  if (hours > 0) return `${hours} 小时 ${minutes} 分`;
  if (minutes > 0) return `${minutes} 分 ${seconds} 秒`;
  return `${seconds} 秒`;
}

export function fmtNumber(value: number | null | undefined, maximumFractionDigits = 0): string {
  if (typeof value !== "number" || !Number.isFinite(value)) return "UNKNOWN";
  return new Intl.NumberFormat("zh-CN", { maximumFractionDigits }).format(value);
}

export function fmtCompact(value: number | null | undefined): string {
  if (typeof value !== "number" || !Number.isFinite(value)) return "UNKNOWN";
  return new Intl.NumberFormat("zh-CN", { notation: "compact", maximumFractionDigits: 1 }).format(value);
}

export function fmtPercent(value: number | null | undefined): string {
  if (typeof value !== "number" || !Number.isFinite(value)) return "UNKNOWN";
  return new Intl.NumberFormat("zh-CN", { style: "percent", maximumFractionDigits: 1 }).format(value);
}

export function fmtBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "UNKNOWN";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(2)} MB`;
}

export function fmtMoney(value: number | null | undefined, currency?: string | null, fractionDigits = 2): string {
  if (typeof value !== "number" || !Number.isFinite(value)) return "UNKNOWN";
  const code = currency && /^[A-Z]{3}$/.test(currency) ? currency : null;
  if (!code) return `${fmtNumber(value, fractionDigits)}（币种 UNKNOWN）`;
  try {
    return new Intl.NumberFormat("zh-CN", {
      style: "currency",
      currency: code,
      maximumFractionDigits: fractionDigits,
      minimumFractionDigits: 0,
    }).format(value);
  } catch {
    return `${fmtNumber(value, fractionDigits)} ${code}`;
  }
}

export function truncate(text: string, max: number): string {
  if (text.length <= max) return text;
  return `${text.slice(0, max - 1)}…`;
}

const STATE_LABELS: Record<string, string> = {
  OK: "正常",
  READY: "就绪",
  DELIVERED: "已送达",
  COMPLETE: "已完成",
  AVAILABLE: "可用",
  ENABLED: "已启用",
  ENABLE: "启用",
  SUCCESS: "成功",
  DISABLED: "已停用",
  BLOCKED: "需显式启用",
  ENABLING: "启用中",
  UNAVAILABLE: "不可用",
  CHANGED: "已变化",
  UNVERIFIED: "未验证",
  UNKNOWN: "未知",
  TIMEOUT: "超时未确认",
  OCCUPIED: "占用未确认",
  EMPTY: "空回执",
  PANIC: "模拟崩溃",
  FAILED: "失败",
  FAULTED: "故障",
  REJECTED: "已拒绝",
  FAULT: "故障",
  ERROR: "错误",
  INFLIGHT: "提交中",
  PENDING: "待提交",
  PAIRING: "配对中",
  STREAMING: "生成中",
  PARTIAL: "部分数据",
  MOCK: "Mock",
  ACTIVE: "活动中",
  DISABLE: "停用",
};

export function stateLabel(state: string | null | undefined): string {
  if (!state) return "UNKNOWN";
  return STATE_LABELS[state.toUpperCase()] ?? state;
}

export type Tone = "neutral" | "success" | "warn" | "danger" | "info";

export function stateTone(state: string | null | undefined): Tone {
  switch ((state ?? "").toUpperCase()) {
    case "OK":
    case "READY":
    case "DELIVERED":
    case "COMPLETE":
    case "AVAILABLE":
    case "ENABLED":
    case "SUCCESS":
      return "success";
    case "UNKNOWN":
    case "TIMEOUT":
    case "OCCUPIED":
    case "EMPTY":
    case "CHANGED":
    case "UNVERIFIED":
    case "PARTIAL":
    case "PENDING":
      return "warn";
    case "FAILED":
    case "FAULTED":
    case "PANIC":
    case "ERROR":
    case "UNAVAILABLE":
      return "danger";
    case "INFLIGHT":
    case "STREAMING":
    case "PAIRING":
      return "info";
    default:
      return "neutral";
  }
}

export function capLabel(key: string): string {
  const labels: Record<string, string> = {
    context: "上下文",
    output: "最大输出",
    vision: "视觉输入",
    tools: "工具调用",
    thinking: "思考",
    audio: "音频",
    reasoning: "推理",
    cacheRead: "缓存读取",
  };
  return labels[key] ?? key;
}

export function directionLabel(direction: string): string {
  switch (direction) {
    case "CHAT_TO_CODE":
      return "聊天 → Code";
    case "CODE_TO_CHAT":
      return "Code → 聊天";
    case "CHAT_TO_CODE_CONTINUE":
      return "同会话续接";
    default:
      return direction;
  }
}

export function modeLabel(mode: string): string {
  switch (mode) {
    case "PLAN":
      return "计划";
    case "EXECUTE":
      return "执行";
    case "TOWER":
      return "Tower";
    default:
      return mode;
  }
}

export function messageRoleLabel(role: string): string {
  switch (role.toLowerCase()) {
    case "user":
      return "用户";
    case "assistant":
      return "助手";
    case "system":
      return "系统";
    case "tool":
      return "工具";
    default:
      return role;
  }
}
