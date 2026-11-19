import type {
  AdapterStatus,
  DeliveryReceipt,
  HandoffAttachment,
  HandoffDirection,
  HandoffMode,
  MockBehavior,
  OutboxRecord,
  SessionReference,
  TaskPackage,
  WebTarget,
} from "../../adapters/types.js";

export type { AdapterStatus, DeliveryReceipt, HandoffAttachment, HandoffDirection, HandoffMode, MockBehavior, OutboxRecord, SessionReference, TaskPackage };

export interface ApiList<T> {
  items: T[];
  total: number;
}

export interface ApiErrorBody {
  error: { code: string; message: string };
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly plugin?: PluginRecord;

  constructor(status: number, code: string, message: string, plugin?: PluginRecord) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.plugin = plugin;
  }
}

export interface PairResult {
  sessionToken: string;
  permissions: string[];
  expiresAt?: string;
}

export interface AuthSnapshot {
  status: "unpaired" | "pairing" | "paired" | "expired";
  permissions: string[];
  expiresAt: string | null;
  message: string | null;
}

export interface ModuleState {
  id: string;
  state: string;
  message?: string;
}

export interface Health {
  status: string;
  safeMode: boolean;
  adapter: AdapterStatus;
  modules: ModuleState[];
  version: string;
  uptimeMs?: number;
  compaction?: string;
  credentials?: string;
}

export interface Settings {
  revision: number;
  safeMode: boolean;
  compactionIdleMinutes: number;
  defaultChatSource: string;
  modules: Record<string, boolean>;
}

export interface UsagePrice {
  input: number | null;
  output: number | null;
  cacheRead: number | null;
  cacheWrite: number | null;
  currency: string | null;
  source: string;
  fetchedAt: string | null;
  unit?: string;
}

export interface UsageEvent {
  observationState?: "PARTIAL" | "FINAL" | "CORRECTION";
  sequence?: number;
  conflictStatus?: string;
  id?: number;
  canonicalKey?: string;
  providerId: string;
  model: string;
  requestId: string;
  attemptId: string;
  sessionId: string;
  projectId: string;
  agentId: string;
  role: string;
  requestKind?: string;
  protocol: string;
  source: string;
  authoritative?: boolean;
  kind?: string;
  at: string;
  fresh?: number | null;
  input?: number | null;
  output?: number | null;
  cacheRead?: number | null;
  cacheWrite?: number | null;
  price?: UsagePrice | null;
  raw: unknown;
  observedAt?: string;
}

export interface FieldTotal {
  sum: number | null;
  known: number;
  total: number;
  complete: boolean;
}

export interface UsageCoverage {
  input: FieldTotal;
  fresh: FieldTotal;
  output: FieldTotal;
  cacheRead: FieldTotal;
  cacheWrite: FieldTotal;
}

export interface UsageEstimate {
  kind: string;
  amount: number | null;
  currency: string | null;
  currencies: string[];
  complete: boolean;
  pricedEvents: number;
  unpricedEvents: number;
  unknownUsageEvents: number;
  partialKnown: boolean;
  priceSources: string[];
  priceFetchedAt: string | null;
  costBasis?: string;
  latePriceFallbackEvents?: number;
  unknownProtocolEvents?: number;
  unit?: string;
  note?: string;
}

export interface ActualBilling {
  kind: string;
  amount: number | null;
  currency: string | null;
  currencies: string[];
  attribution?: string;
  note?: string;
}

export interface SubscriptionLedger {
  kind: string;
  remaining: string | null;
  resetAt: string | null;
  source: string | null;
  observedAt: string | null;
  note: string;
}

export interface UsageSummary {
  requests: number;
  knownRequests: number;
  partialKnownRequests?: number;
  unknownRequests: number;
  input: number | null;
  output: number | null;
  fresh?: number | null;
  cacheRead: number | null;
  cacheWrite: number | null;
  coverage?: Record<string, number>;
  partialKnown?: UsageCoverage;
  estimate: UsageEstimate;
  actual: ActualBilling;
  subscription: SubscriptionLedger;
  filters?: Record<string, unknown>;
  generatedAt?: string;
  note?: string;
}

export interface QuotaSnapshot {
  id: number;
  ledger: string;
  providerId: string;
  scope: string;
  remaining: string | null;
  limit: string | null;
  unit: string | null;
  resetAt: string | null;
  currency: string | null;
  source: string;
  observedAt: string;
  note: string;
}

export interface LedgerOverview {
  ledger: string;
  label: string;
  remaining: number | null;
  known: boolean;
  note: string;
}

export interface QuotaResponse extends ApiList<QuotaSnapshot> {
  ledgers: LedgerOverview[];
}

export interface Gateway {
  id: string;
  name: string;
  kind: string;
  balance: number | null;
  currency: string | null;
  checkedAt: string | null;
  note: string;
  readOnly: boolean;
}

export interface PluginUpstream {
  upstreamRepo?: string;
  upstreamUrl?: string;
  upstreamCommit?: string;
  upstreamLicense?: string;
}

export interface PluginPlatformLimitation {
  feature: "browser.view" | "window.popout" | "entry.nativeOverlay";
  capability: string;
  message: string;
}

export interface PluginRecord extends PluginUpstream {
  platformLimitations?: PluginPlatformLimitation[];
  id: string;
  name: string;
  version?: string;
  description?: string;
  author?: string;
  license?: string;
  source?: "BUNDLED" | "LOCAL";
  state: string;
  enabled?: boolean;
  error?: string;
  capabilities?: string[];
  privilegedCapabilities?: string[];
  declaredCapabilities?: string[];
  slots?: string[];
  hostVersions?: string[];
  compatible?: boolean;
  hasSettings?: boolean;
  apiVersion?: number;
  official?: boolean;
  hasNotice?: boolean;
  upstream?: string;
  installedAt?: string;
  updatedAt?: string;
  /** Only present in the enable response: the renderer identity token. */
  token?: string;
  /**
   * A bundled plugin the user uninstalled. It is listed only so the page can
   * offer to restore the embedded copy; it has no files, no capabilities and is
   * never running.
   */
  restorable?: boolean;
}

/** A validated package, before the user grants anything. Mirrors plugins.PackagePreview. */
export interface PluginPreview extends PluginUpstream {
  platformLimitations?: PluginPlatformLimitation[];
  packageDigest: string;
  id: string;
  name: string;
  version: string;
  description?: string;
  author?: string;
  license?: string;
  hostVersions: string[];
  capabilities: string[];
  privilegedCapabilities?: string[];
  apiVersion: number;
  hasNotice?: boolean;
  compatible: boolean;
  installed: boolean;
  source: string;
  upstream?: string;
}

export interface PluginLegal extends Required<PluginUpstream> {
  id: string;
  license: string;
  licenseText: string;
  licenseTruncated: boolean;
  noticeFile: string;
  noticeText: string;
  noticeTruncated: boolean;
}

export interface PluginActivity {
  id: number;
  pluginId: string;
  kind: string;
  detail: string;
  at: string;
}

export interface PluginList {
  items: PluginRecord[];
  platformLimitations?: PluginPlatformLimitation[] | null;
  total: number;
  safeMode: boolean;
  hostVersion?: string;
  apiVersion?: number;
}

/** Mirrors kctmigration.Report: the handover state, with no secret in it. */
export interface KctMigrationReport {
  pluginId: string;
  label: string;
  dataDir: string;
  detected: boolean;
  plistPresent: boolean;
  dataDirPresent: boolean;
  migrated: boolean;
  needsConfirmation: boolean;
  settings: Record<string, string>;
  migratedAt?: string;
  detail?: string;
}

/** Mirrors kctmigration.Result. */
export interface KctMigrationResult {
  report: KctMigrationReport;
  stoppedLoader: boolean;
  disabledPlist: boolean;
  migratedKeys: string[];
  alreadyMigrated: boolean;
  message: string;
}

export interface ChatSource {
  id: string;
  name: string;
  url: string;
  adapterStatus?: string;
}

export interface ChatMessage {
  id: string;
  role: string;
  text: string;
  at: string;
}

export interface ChatThread {
  id: string;
  title: string;
  sourceId: string;
  model?: string;
  messages?: ChatMessage[];
  mode?: string;
}

export interface TaskPreviewResult {
  task: TaskPackage;
  redacted: TaskPackage;
  findings: string[] | null;
  requiresConfirmation: boolean;
  confirmationHash: string;
  previewId: string;
  warning?: string;
}

export interface UsageFilters {
  projectId?: string;
  sessionId?: string;
  agentId?: string;
  model?: string;
  from?: string;
  to?: string;
}

const API_BASE = "/enhance/v1";
const REQUEST_TIMEOUT_MS = 20_000;
const DEFAULT_LIMIT = 100;

let sessionToken: string | null = null;
let sessionExpiresAt: string | null = null;
let sessionPermissions: string[] = [];
let authStatus: AuthSnapshot["status"] = "unpaired";
let authMessage: string | null = null;
const authListeners = new Set<() => void>();

let authSnapshot: AuthSnapshot = { status: "unpaired", permissions: [], expiresAt: null, message: null };

function publishAuth(): void {
  authSnapshot = {
    status: authStatus,
    permissions: [...sessionPermissions],
    expiresAt: sessionExpiresAt,
    message: authMessage,
  };
  for (const listener of authListeners) listener();
}

export function subscribeAuth(listener: () => void): () => void {
  authListeners.add(listener);
  return () => authListeners.delete(listener);
}

export function authSnapshotValue(): AuthSnapshot {
  return authSnapshot;
}

export function hasPermission(permission: string): boolean {
  return sessionPermissions.includes(permission);
}


export function clearSession(message: string | null = null): void {
  coreTransport = null;
  sessionToken = null;
  sessionPermissions = [];
  sessionExpiresAt = null;
  authStatus = "unpaired";
  authMessage = message;
  publishAuth();
}

export function consumePairingFragment(): string | null {
  if (typeof window === "undefined") return null;
  const hash = window.location.hash;
  if (!hash.startsWith("#pair=")) return null;
  const raw = hash.slice("#pair=".length);
  const clean = window.location.pathname + window.location.search;
  window.history.replaceState(null, "", clean);
  try {
    const value = decodeURIComponent(raw).trim();
    return value.length > 0 ? value : null;
  } catch {
    return null;
  }
}

export async function pair(pairingToken: string): Promise<PairResult> {
  const token = pairingToken.trim();
  if (!token) throw new ApiError(400, "PAIRING_EMPTY", "请输入配对凭据");
  authStatus = "pairing";
  authMessage = null;
  publishAuth();
  try {
    const result = await request<PairResult>("/pair", {
      method: "POST",
      body: { pairingToken: token },
      anonymous: true,
    });
    if (!result.sessionToken) throw new ApiError(500, "PAIRING_INVALID", "核心返回了空会话凭据");
    sessionToken = result.sessionToken;
    sessionPermissions = Array.isArray(result.permissions) ? result.permissions : [];
    sessionExpiresAt = result.expiresAt ?? null;
    authStatus = "paired";
    authMessage = null;
    publishAuth();
    return result;
  } catch (error) {
    sessionToken = null;
    sessionPermissions = [];
    sessionExpiresAt = null;
    authStatus = "unpaired";
    authMessage = error instanceof Error ? error.message : "配对失败";
    publishAuth();
    throw error;
  }
}

interface RequestOptions {
  method?: "GET" | "POST" | "PUT" | "DELETE";
  body?: unknown;
  query?: Record<string, string | number | undefined | null>;
  headers?: Record<string, string>;
  anonymous?: boolean;
  signal?: AbortSignal;
  binaryBody?: ArrayBuffer;
}

function buildQuery(query: Record<string, string | number | undefined | null> | undefined): string {
  if (!query) return "";
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === "") continue;
    params.set(key, String(value));
  }
  const text = params.toString();
  return text ? `?${text}` : "";
}

function friendlyNetworkMessage(error: unknown): string {
  if (error instanceof DOMException && error.name === "AbortError") {
    return "请求超时。请确认本地核心仍在运行，然后重试。";
  }
  return "无法连接本地核心服务。请确认增强程序已启动，并保持同一地址访问。";
}

async function parseError(response: Response): Promise<ApiError> {
  let code = `HTTP_${response.status}`;
  let message = response.statusText || "请求失败";
  let plugin: PluginRecord | undefined;
  try {
    const data = (await response.json()) as Partial<ApiErrorBody> & { plugin?: PluginRecord };
    if (data?.error?.code) code = data.error.code;
    if (data?.error?.message) message = data.error.message;
    if (data?.plugin && typeof data.plugin === "object") plugin = data.plugin;
    if (data?.error === null || data?.error === undefined) {
      const anyBody = data as { code?: unknown; message?: unknown };
      if (typeof anyBody.code === "string") code = anyBody.code;
      if (typeof anyBody.message === "string") message = anyBody.message;
    }
  } catch {
    // 非 JSON 错误体保留状态码与通用说明
  }
  return new ApiError(response.status, code, message, plugin);
}

export type CoreTransport = (path: string, init: RequestInit) => Promise<Response>;
let coreTransport: CoreTransport | null = null;
export function connectHostTransport(transport: CoreTransport | null, permissions: string[] = []) {
  sessionToken = null;
  coreTransport = transport;
  sessionExpiresAt = null;
  authMessage = null;
  sessionPermissions = permissions;
  authStatus = transport ? "paired" : "unpaired";
  publishAuth();
}
export async function coreRequest<T>(path: string, options: RequestOptions = {}): Promise<T> {
  return request<T>(path, options);
}
async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = "GET", body, query, headers: extraHeaders, anonymous = false, signal, binaryBody } = options;
  if (!anonymous && !sessionToken && !coreTransport) {
    throw new ApiError(401, "AUTH_REQUIRED", "尚未连接本地核心，请先完成配对");
  }
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
  const abortFromCaller = () => controller.abort();
  signal?.addEventListener("abort", abortFromCaller, { once: true });
  try {
    const headers: Record<string, string> = { Accept: "application/json", ...extraHeaders };
    if (body !== undefined || (method !== "GET" && !binaryBody)) headers["Content-Type"] = "application/json";
    if(binaryBody) headers["Content-Type"]="application/octet-stream";
    if (!anonymous && sessionToken) headers.Authorization = `Bearer ${sessionToken}`;
    let response: Response;
    try {
      response = await (coreTransport ?? fetch)(`${API_BASE}${path}${buildQuery(query)}`, {
        method,
        headers,
        body: binaryBody ?? (body === undefined ? undefined : JSON.stringify(body)),
        signal: controller.signal,
        credentials: "omit",
        cache: "no-store",
        referrerPolicy: "no-referrer",
      });
    } catch (error) {
      throw new ApiError(0, "NETWORK", friendlyNetworkMessage(error));
    }
    if (!response.ok) {
      const apiError = await parseError(response);
      if (apiError.status === 401 && !anonymous) {
        clearSession("会话已过期或未获授权，请重新配对。凭证只保存在当前页面内存中。");
      }
      throw apiError;
    }
    if (response.status === 204) return undefined as T;
    const text = await response.text();
    if (!text) return undefined as T;
    try {
      return JSON.parse(text) as T;
    } catch {
      throw new ApiError(502, "INVALID_RESPONSE", "核心返回了无法解析的响应。请检查版本是否匹配。");
    }
  } finally {
    clearTimeout(timeout);
    signal?.removeEventListener("abort", abortFromCaller);
  }
}

function asList<T>(value: unknown): ApiList<T> {
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

function pageQuery(offset = 0, limit = DEFAULT_LIMIT): Record<string, number> {
  return { offset, limit };
}


function installPlugin(root: string, packageDigest: string, capabilities: string[], confirmPrivileged?: boolean) {
  return request<PluginRecord>("/plugins/install", { method: "POST", body: { root, packageDigest, capabilities, confirmPrivileged } });
}

export const api = {
  pair,
  installPlugin,
  pluginLegal: (id: string) => request<PluginLegal>(`/plugins/${encodeURIComponent(id)}/legal`),
  auth: {
    snapshot: authSnapshotValue,
    subscribe: subscribeAuth,
    clear: clearSession,
    hasPermission,
  },
  health: () => request<Health>("/health"),
  settings: {
    get: () => request<Settings>("/settings"),
    put: (settings: Settings) => request<Settings>("/settings", { method: "PUT", body: settings }),
  },
  gateways: async () => asList<Gateway>(await request<unknown>("/gateways")),
  usage: {
    list: async (filters: UsageFilters = {}, offset = 0, limit = DEFAULT_LIMIT) =>
      asList<UsageEvent>(
        await request<unknown>("/usage", {
          query: { ...filters, ...pageQuery(offset, limit) },
        }),
      ),
    add: (events: UsageEvent[]) =>
      request<{ added: number; duplicates: number; updated: number; unresolved: number }>("/usage", {
        method: "POST",
        body: { events },
      }),
    summary: (filters: UsageFilters = {}) =>
      request<UsageSummary>("/usage/summary", { query: { ...filters } }),
    quotas: async () => {
      const value = await request<unknown>("/quotas");
      const base = asList<QuotaSnapshot>(value);
      const ledgers =
        value && typeof value === "object" && Array.isArray((value as { ledgers?: unknown }).ledgers)
          ? ((value as { ledgers: LedgerOverview[] }).ledgers)
          : [];
      return { ...base, ledgers };
    },
    clearDemo: () => request<{events:number;unlinked:number;quotas:number;preserved:number}>("/usage/demo/clear", {method:"POST",body:{}}),
    demo: () =>
      request<{ added: number; duplicates: number; updated: number; unresolved: number }>("/usage/demo", {
        method: "POST",
        body: {},
      }),
  },
  adapter: {
    get: () => request<AdapterStatus>("/adapter"),
    setMode: (mode: "AVAILABLE" | "UNAVAILABLE" | "CHANGED") =>
      request<AdapterStatus>("/adapter", { method: "PUT", body: { mode } }),
  },
  sessions: async () => asList<SessionReference>(await request<unknown>("/sessions")),
  webTargets: async () => asList<WebTarget>(await request<unknown>("/web-targets")),
  attachments: { upload: (attachment:HandoffAttachment) => {
    const data=Uint8Array.from(atob(attachment.dataBase64 ?? ""),c=>c.charCodeAt(0));
    return request<HandoffAttachment>("/attachments",{method:"POST",binaryBody:data.buffer,headers:{"X-Attachment-SHA256":attachment.sha256,"X-Attachment-Type":attachment.mime}});
  }, collect:()=>request<{removed:number}>("/attachments/collect",{method:"POST",body:{}}) },
  tasks: {
    list: async (offset = 0, limit = DEFAULT_LIMIT) =>
      asList<OutboxRecord>(await request<unknown>("/tasks", { query: pageQuery(offset, limit) })),
    preview: (task: TaskPackage) => request<TaskPreviewResult>("/tasks/preview", { method: "POST", body: { task } }),
    submit: (
      intent: string,
      task: TaskPackage,
      options: { confirmationHash?: string; mockBehavior?: MockBehavior; acknowledgeUnverifiedAttachments?: boolean } = {},
    ) =>
      request<OutboxRecord>("/tasks", {
        method: "POST",
        body: {
          intent,
          task,
          confirmed: true,
          ...(options.confirmationHash ? { confirmationHash: options.confirmationHash } : {}),
          ...(options.mockBehavior ? { mockBehavior: options.mockBehavior } : {}),
          ...(options.acknowledgeUnverifiedAttachments ? { acknowledgeUnverifiedAttachments: true } : {}),
        },
      }),
    reconcile: (id: string) =>
      request<OutboxRecord>(`/tasks/${encodeURIComponent(id)}/reconcile`, { method: "POST", body: {} }),
  },
  plugins: {
    list: () => request<PluginList>("/plugins"),
    get: (id: string) => request<PluginRecord>(`/plugins/${encodeURIComponent(id)}`),
    enable: (id: string) => request<PluginRecord>(`/plugins/${encodeURIComponent(id)}/enable`, { method: "POST", body: {} }),
    disable: (id: string) => request<PluginRecord>(`/plugins/${encodeURIComponent(id)}/disable`, { method: "POST", body: {} }),
    uninstall: (id: string, options?: { deleteData?: boolean }) =>
      request<{ id: string; uninstalled: boolean }>(`/plugins/${encodeURIComponent(id)}/uninstall`, {
        method: "POST",
        body: options?.deleteData ? { deleteData: true } : {},
      }),
    preview: (root: string) => request<PluginPreview>("/plugins/preview", { method: "POST", body: { root } }),
    install: installPlugin,
    restore: (id: string) => request<PluginRecord>(`/plugins/${encodeURIComponent(id)}/restore`, { method: "POST", body: {} }),
    forceEnable: (id: string) =>
      request<PluginRecord>(`/plugins/${encodeURIComponent(id)}/force-enable`, { method: "POST", body: {} }),
    activity: async (id: string, before = 0) =>
      asList<PluginActivity>(await request<unknown>(`/plugins/${encodeURIComponent(id)}/activity`, { query: { before, limit: 50 } })),
    /** The standalone KCT handover. It is a core route because the adapter plugin
     * is disabled at the moment the page needs it. */
    kctMigration: () => request<KctMigrationReport>("/kct/migration"),
    confirmKctMigration: () => request<KctMigrationResult>("/kct/migration/confirm", { method: "POST", body: {} }),
  },
  chat: {
    sources: async () => asList<ChatSource>(await request<unknown>("/chat/sources")),
    addSource: (body: { id: string; name: string; url: string }) =>
      request<ChatSource>("/chat/sources", { method: "POST", body }),
    threads: async (offset = 0, limit = DEFAULT_LIMIT) =>
      asList<ChatThread>(await request<unknown>("/chat/threads", { query: pageQuery(offset, limit) })),
    createThread: (body: { title: string; sourceId: string; model?: string }) =>
      request<ChatThread>("/chat/threads", { method: "POST", body }),
    thread: (id: string) => request<ChatThread>(`/chat/threads/${encodeURIComponent(id)}`),
    send: (id: string, text: string, intent = randomKey()) =>
      request<ChatThread>(`/chat/threads/${encodeURIComponent(id)}/messages`, {
        method: "POST",
        body: { text },
        headers: { "Idempotency-Key": intent },
      }),
  },
};

function randomKey(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") return crypto.randomUUID();
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`;
}

