import type {
  HandoffAttachment,
  HandoffDirection,
  HandoffMode,
  MockBehavior,
  TaskPackage,
} from "../../../adapters/types.js";

export interface PairBody {
  pairingToken: string;
}

export function buildPairBody(pairingToken: string): PairBody {
  return { pairingToken };
}

export const LIST_DEFAULT_LIMIT = 100;

export interface ListResult<T> {
  items: T[];
  total: number;
}

export function extractList<T>(value: unknown): ListResult<T> {
  if (Array.isArray(value)) return { items: value as T[], total: value.length };
  if (value && typeof value === "object") {
    const record = value as { items?: unknown; total?: unknown };
    if (Array.isArray(record.items)) {
      return {
        items: record.items as T[],
        total: typeof record.total === "number" ? record.total : record.items.length,
      };
    }
  }
  return { items: [], total: 0 };
}

export function parseApiError(status: number, bodyText: string): { code: string; message: string } {
  let code = `HTTP_${status}`;
  let message = "请求未完成";
  if (!bodyText) return { code, message };
  try {
    const parsed = JSON.parse(bodyText) as { error?: { code?: unknown; message?: unknown } };
    if (parsed && typeof parsed.error === "object" && parsed.error) {
      if (typeof parsed.error.code === "string" && parsed.error.code) code = parsed.error.code;
      if (typeof parsed.error.message === "string" && parsed.error.message) message = parsed.error.message;
    }
  } catch {
    message = bodyText.slice(0, 200);
  }
  return { code, message };
}

export interface UsageQueryInput {
  projectId?: string;
  sessionId?: string;
  agentId?: string;
  model?: string;
  from?: string;
  to?: string;
}

export function buildUsageQuery(
  filters: UsageQueryInput,
  offset = 0,
  limit = LIST_DEFAULT_LIMIT,
): Record<string, string | number> {
  const query: Record<string, string | number> = { offset, limit };
  for (const key of ["projectId", "sessionId", "agentId", "model", "from", "to"] as const) {
    const value = filters[key];
    if (typeof value === "string" && value.trim()) query[key] = value.trim();
  }
  return query;
}

export interface TaskSubmitBody {
  intent: string;
  task: TaskPackage;
  confirmed: true;
  mockBehavior?: MockBehavior;
}

export function buildTaskSubmitBody(
  intent: string,
  task: TaskPackage,
  mockBehavior?: MockBehavior,
): TaskSubmitBody {
  if (!intent.trim()) throw new Error("意图键不能为空");
  const body: TaskSubmitBody = { intent, task, confirmed: true };
  if (mockBehavior) body.mockBehavior = mockBehavior;
  return body;
}

export interface ModelsImportBody {
  records: unknown[];
  revision: number;
}

export function buildModelsImportBody(records: unknown[], revision: number): ModelsImportBody {
  if (!Number.isInteger(revision) || revision < 0) throw new Error("revision 必须是非负整数");
  return { records, revision };
}

export interface ModelOverrideBody {
  providerId: string;
  id: string;
  field: string;
  value: number | boolean | null;
  revision: number;
}

export function buildOverrideBody(input: ModelOverrideBody): ModelOverrideBody {
  if (!Number.isInteger(input.revision) || input.revision < 0) throw new Error("revision 必须是非负整数");
  if (typeof input.value === "string" || Number.isNaN(input.value)) throw new Error("覆盖值必须是数字、布尔或 null");
  return {
    providerId: input.providerId,
    id: input.id,
    field: input.field,
    value: input.value,
    revision: input.revision,
  };
}

export interface ModelsDevBody {
  catalog: unknown;
  revision: number;
  preview: boolean;
}

export function buildModelsDevBody(catalog: unknown, revision: number, preview: boolean): ModelsDevBody {
  if (!Number.isInteger(revision) || revision < 0) throw new Error("revision 必须是非负整数");
  if (!catalog || typeof catalog !== "object") throw new Error("models.dev 目录必须是 JSON 对象");
  return { catalog, revision, preview };
}

export interface ProviderUpdateBody {
  name: string;
  protocol: string;
  baseUrl: string;
  secretRef: string | null;
  revision: number;
}

// PUT /providers/{id} 是全量语义：id 不可改（发送会被核心拒绝），
// secretRef 必须原样回传或显式置 null；revision 来自核心当前值。
export function buildProviderUpdateBody(input: {
  name: string;
  protocol: string;
  baseUrl: string;
  secretRef?: string | null;
  revision: number;
}): ProviderUpdateBody {
  if (!Number.isInteger(input.revision) || input.revision < 0) {
    throw new Error("revision 必须是非负整数");
  }
  if (!input.name.trim() || !input.protocol.trim() || !input.baseUrl.trim()) {
    throw new Error("名称、协议与 Base URL 不能为空");
  }
  return {
    name: input.name.trim(),
    protocol: input.protocol.trim(),
    baseUrl: input.baseUrl.trim(),
    secretRef: input.secretRef ?? null,
    revision: input.revision,
  };
}

export const MODEL_PAGE_LIMIT = 100;

export function buildModelPageQuery(page: number, limit = MODEL_PAGE_LIMIT): { offset: number; limit: number } {
  if (!Number.isInteger(page) || page < 0) throw new Error("page 必须是非负整数");
  if (!Number.isInteger(limit) || limit < 1 || limit > 1000) throw new Error("limit 必须在 1..1000");
  return { offset: page * limit, limit };
}

export function chatSendHeaders(idempotencyKey: string): Record<string, string> {
  return { "Idempotency-Key": idempotencyKey };
}

const DIRECTIONS: HandoffDirection[] = ["CHAT_TO_CODE", "CODE_TO_CHAT", "CHAT_TO_CODE_CONTINUE"];
const MODES: HandoffMode[] = ["PLAN", "EXECUTE", "TOWER"];

export interface TaskIssue {
  path: string;
  message: string;
}

export function validateTaskPackage(value: unknown): TaskIssue[] {
  const issues: TaskIssue[] = [];
  if (!value || typeof value !== "object") return [{ path: "", message: "任务包必须是对象" }];
  const task = value as Partial<TaskPackage>;
  if (task.version !== 1) issues.push({ path: "version", message: "任务包版本必须为 1" });
  for (const key of ["taskId", "sessionId", "projectId", "goal"] as const) {
    const raw = task[key];
    if (typeof raw !== "string" || !raw.trim()) issues.push({ path: key, message: `${key} 不能为空` });
  }
  if (!task.direction || !DIRECTIONS.includes(task.direction)) {
    issues.push({ path: "direction", message: "方向必须是三种交接之一" });
  }
  if (!task.mode || !MODES.includes(task.mode)) {
    issues.push({ path: "mode", message: "模式必须是 PLAN、EXECUTE 或 TOWER" });
  }
  if (!task.source || typeof task.source !== "object") {
    issues.push({ path: "source", message: "缺少来源" });
  } else {
    if (typeof task.source.url !== "string" || !/^https:\/\//.test(task.source.url)) {
      issues.push({ path: "source.url", message: "来源必须是 HTTPS 网址" });
    }
    if (!Array.isArray(task.source.messageIds) || task.source.messageIds.length === 0) {
      issues.push({ path: "source.messageIds", message: "至少选择一条来源消息" });
    }
  }
  const messages = Array.isArray(task.messages) ? task.messages : [];
  if (messages.length === 0) {
    issues.push({ path: "messages", message: "至少包含一条消息" });
  } else {
    const roles = new Set(["user", "assistant", "system"]);
    const ids = new Set<string>();
    for (const message of messages) {
      if (!message.id || !message.text.trim()) {
        issues.push({ path: "messages", message: "每条消息都需要 ID 与文本" });
        break;
      }
      if (ids.has(message.id)) {
        issues.push({ path: "messages", message: `消息 ID 重复：${message.id}` });
        break;
      }
      ids.add(message.id);
      if (!roles.has(String(message.role).toLowerCase())) {
        issues.push({ path: "messages", message: `消息角色必须是 user、assistant 或 system：${message.id}` });
        break;
      }
    }
    const selected = new Set(Array.isArray(task.source?.messageIds) ? task.source.messageIds : []);
    if (
      (task.direction === "CHAT_TO_CODE" || task.direction === "CHAT_TO_CODE_CONTINUE") &&
      messages.some((message) => message.id && !selected.has(message.id))
    ) {
      issues.push({ path: "messages", message: "所选消息必须全部包含在 source.messageIds 中" });
    }
  }
  if (!Number.isInteger(task.checkpoint) || (task.checkpoint ?? -1) < 1) {
    issues.push({ path: "checkpoint", message: "checkpoint 必须是从 1 开始的整数" });
  }
  if (!Number.isInteger(task.parentCheckpoint) || (task.parentCheckpoint ?? -1) < 0) {
    issues.push({ path: "parentCheckpoint", message: "parentCheckpoint 必须是非负整数" });
  }
  if ((task.parentCheckpoint ?? 0) > 0) {
    if (!/^[a-f0-9]{64}$/.test(task.parentHash ?? "")) {
      issues.push({ path: "parentHash", message: "parentHash 必须是 64 位小写十六进制" });
    }
    if ((task.checkpoint ?? 0) <= (task.parentCheckpoint ?? 0)) {
      issues.push({ path: "checkpoint", message: "checkpoint 必须大于 parentCheckpoint" });
    }
  }
  if ((task.parentCheckpoint ?? 0) === 0 && task.parentHash) {
    issues.push({ path: "parentHash", message: "没有父检查点时不应提供 parentHash" });
  }
  if (task.direction === "CHAT_TO_CODE_CONTINUE") {
    if (!task.linkedTaskId) issues.push({ path: "linkedTaskId", message: "续接必须关联原任务" });
    if ((task.parentCheckpoint ?? 0) < 1) issues.push({ path: "parentCheckpoint", message: "续接必须指定父检查点" });
    if (!/^[a-f0-9]{64}$/.test(task.parentHash ?? "")) {
      issues.push({ path: "parentHash", message: "续接必须提供 64 位小写十六进制的 parentHash" });
    }
  }
  const attachments = Array.isArray(task.attachments) ? task.attachments : [];
  if (attachments.length > 32) issues.push({ path: "attachments", message: "附件数量不能超过 32 个" });
  for (const [index, attachment] of attachments.entries()) {
    const check = validateAttachment(attachment);
    if (check) issues.push({ path: `attachments[${index}]`, message: check });
  }
  return issues;
}

export function validateAttachment(attachment: HandoffAttachment): string | null {
  if (!attachment || typeof attachment !== "object") return "附件必须是对象";
  if (!attachment.name || typeof attachment.name !== "string") return "附件缺少文件名";
  if (!Number.isInteger(attachment.size) || attachment.size < 0) return "附件大小无效";
  if (attachment.size > 1_048_576) return "单个附件不能超过 1 MiB";
  if (!/^[a-f0-9]{64}$/i.test(attachment.sha256 ?? "")) return "附件缺少 SHA-256";
  if (attachment.status !== "verified" && !(attachment.status==="STORED" && attachment.blobRef)) return "附件未通过本地字节校验";
  if (attachment.dataBase64 && !isBase64(attachment.dataBase64)) return "附件 base64 无效";
  return null;
}

export function isBase64(value: string): boolean {
  if (!value) return false;
  try {
    atob(value);
    return true;
  } catch {
    return false;
  }
}

export function isHttpsUrl(value: string): boolean {
  try {
    const url = new URL(value);
    return (
      url.protocol === "https:" &&
      url.hostname.length > 0 &&
      url.username === "" &&
      url.password === "" &&
      url.port === ""
    );
  } catch {
    return false;
  }
}
