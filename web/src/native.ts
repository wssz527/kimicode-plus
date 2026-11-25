import { createContext, useContext } from "react";
export interface PlannerOptions {
  context: boolean;
  skills: boolean;
  tools: boolean;
  readOnly: boolean;
}
export interface DemoMessage {
  id: string;
  role: string;
  text: string;
  at?: string;
  attachments?: { kind: string; name?: string }[];
  tool?: string;
  reasoning?: string;
  toolCalls?: { id: string; function: { name: string; arguments: string } }[];
}
export interface PlannerThread {
  id: string;
  title: string;
  model: string;
  options: PlannerOptions;
  messages: DemoMessage[];
  status: "idle" | "running" | "quota" | "error";
  revision?: number;
  error?: string;
  approval?: { id: string; title: string; detail: string };
}
export interface NativeSession {
  id: string;
  title: string;
  cwd?: string;
  busy?: boolean;
  main_turn_active?: boolean;
  pending_interaction?: string;
}
export interface NativeToolCall {
  id: string;
  name: string;
  status: string;
  input?: unknown;
  output?: unknown;
  duration?: number;
}
export interface NativeMessage extends Omit<DemoMessage, "toolCalls"> {
  turnId?: string;
  streaming?: boolean;
  toolCalls?: NativeToolCall[];
}
export type NativeApprovalDecision = "approved" | "rejected" | "cancelled";
export interface NativeQuestion {
  id: string;
  question: string;
  header?: string;
  body?: string;
  options: { id: string; label: string }[];
  multi_select?: boolean;
  allow_other?: boolean;
  other_label?: string;
}
export interface NativeApproval {
  id: string;
  title: string;
  detail?: string;
  options?: { label: string; value: string; decision?: NativeApprovalDecision }[];
  questions?: NativeQuestion[];
}
export interface SessionView {
  session: NativeSession;
  messages: NativeMessage[];
  approvals: NativeApproval[];
  goal?: { status?: string; [key: string]: unknown };
}
/** Cache metrics mirrored from internal/cache. Every numeric cache field can be
 * absent by design: a missing rate means "the provider never reported cache
 * fields", which is not the same as a zero hit rate (plan D4 / M2 section 3). */
export interface CacheTotals {
  steps: number;
  inputOther: number;
  inputCacheRead: number;
  inputCacheCreation: number;
  output: number;
  hitRate: number | null;
  reported: string;
}

export interface CacheSpeed {
  state: string;
  tokensPerSecond?: number;
  samples: number;
  note?: string;
}

export interface CacheQuota {
  state: string;
  source: string;
  note?: string;
  value?: unknown;
}

export interface CacheModelRow extends CacheTotals {
  model: string;
  speed: CacheSpeed;
  note?: string;
}

export interface CacheSummary {
  generatedAt: string;
  hour: CacheTotals;
  today: CacheTotals;
  models: CacheModelRow[];
  speed: CacheSpeed;
  quota: CacheQuota;
  reportingNote?: string;
}

export interface CacheSessionRow {
  sessionId: string;
  title: string;
  cwd: string;
  model: string;
  lastRequestAt: string | null;
  idleMinutes: number | null;
  recent: CacheTotals;
  contextTokens: number;
  openBackground: number;
  blocked: boolean;
  reported: string;
  note?: string;
}

export interface CacheHistoryPoint {
  bucket: string;
  totals: CacheTotals;
  speed: CacheSpeed;
}

export interface CacheHistory {
  range: string;
  unit: string;
  points: CacheHistoryPoint[];
  totals: CacheTotals;
}

export interface CacheRetentionBucket {
  label: string;
  minMs: number;
  maxMs: number;
  samples: number;
  hitRate: number | null;
  reported: string;
}

export interface CacheRetention {
  idleBuckets: CacheRetentionBucket[];
  byModel?: { model: string; idleBuckets: CacheRetentionBucket[]; samples?: number; lastSeen?: string }[];
  note: string;
}

export interface CacheReceipt {
  kind: string;
  sessionId: string;
  sessionTitle?: string;
  state: string;
  note: string;
  at: string;
}

export interface IdleKeepAlivePolicy {
  enabled: boolean;
  defaultCount: number;
  minQuotaPercent: number;
}

export interface ResolvedCachePolicy {
  model: string;
  provider: string;
  providerType: "subscription" | "open_platform" | "other";
  providerLabel: string;
  windowMinutes: number;
  intervalMinutes: number;
  source: "observed" | "fallback";
}

export interface SessionKeepAliveView {
  sessionId: string;
  state: "disabled" | "keeping_alive" | "stopped";
  mode: "default" | "off" | "count";
  targetCount: number;
  effectiveEnabled: boolean;
  effectiveLimit: number;
  usedCount: number;
  remainingCount?: number | null;
  statusLabel: string;
  statusTone: "ok" | "active" | "warn" | "error" | "muted";
  lastResult?: string;
  lastResultLabel?: string;
  lastNote?: string;
  lastKeepAliveAt?: string;
  stoppedReason?: string;
  stoppedDetail?: string;
  estimatedTokens: number;
  budgetTokens: number;
  spentTokens: number;
  policy: ResolvedCachePolicy;
  inFlight: boolean;
  globalEnabled: boolean;
  globalCount: number;
}

export interface CachePolicySettings {
  keepAlive: { enabled: boolean; minutes: number };
  idleKeepAlive: IdleKeepAlivePolicy;
  compaction: { enabled: boolean; idleMinutes: number; contextThreshold: number };
  recovery: { enabled: boolean; intervalSeconds: number; entries: { id: string; title: string; status: string; nextAt?: string; note?: string }[] };
}

export type KeepAliveEvent =
  | { type: "keepalive.session"; sessionId: string; view: SessionKeepAliveView }
  | { type: "keepalive.alert"; sessionId: string; reason: string; title: string; message: string };

/** A provider that never reported cache fields must never render as 0%. */
export const CACHE_NOT_REPORTED = "NOT_REPORTED";

/** Renders a hit rate. `null` means "no number is honest here". */
export function hitRateLabel(totals: Pick<CacheTotals, "hitRate" | "reported"> | null | undefined): string {
  if (!totals) return "--";
  if (totals.reported === CACHE_NOT_REPORTED) return "未上报";
  if (totals.hitRate === null || totals.hitRate === undefined || !Number.isFinite(totals.hitRate)) return "--";
  return `${(totals.hitRate * 100).toFixed(1)}%`;
}

/**
 * "距上次请求 N 分钟". The value comes from the main agent's own last model
 * request, so subagent activity never resets it (plan D8).
 */
export function idleLabel(minutes: number | null | undefined): string {
  if (minutes === null || minutes === undefined || !Number.isFinite(minutes)) return "尚无请求记录";
  if (minutes < 1) return "不到 1 分钟";
  if (minutes < 60) return `${Math.round(minutes)} 分钟`;
  const hours = minutes / 60;
  return hours < 48 ? `${hours.toFixed(1)} 小时` : `${Math.round(hours / 24)} 天`;
}

export function tokenLabel(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value) || value < 0) return "--";
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(2)}M`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`;
  return String(value);
}

export function speedLabel(speed: Pick<CacheSpeed, "tokensPerSecond"> | null | undefined): string {
  if (!speed || speed.tokensPerSecond === undefined || !Number.isFinite(speed.tokensPerSecond)) return "--";
  return `${speed.tokensPerSecond.toFixed(1)} tok/s`;
}

/**
 * Decides whether a background policy event may replace the form. An event that
 * arrives while the user has an unsaved edit in flight must not overwrite the draft
 * (plan M2 section 5: "后台 automation 事件不得覆盖正在编辑的草稿").
 */
export function shouldApplyPolicyEvent(state: { writing: boolean; editing: boolean }): boolean {
  return !state.writing && !state.editing;
}

export interface CachePolicyDraft {
  keepAlive: { enabled: boolean; minutes: number };
  idleKeepAlive: IdleKeepAlivePolicy;
  compaction: { enabled: boolean; idleMinutes: number; contextThreshold: number };
  recovery: { enabled: boolean; intervalSeconds: number; entries: NonNullable<CachePolicySettings["recovery"]["entries"]> };
}

/** A number field commits only on blur, and only when the value is in range. */
export function commitNumberField(text: string, min: number, max: number): number | null {
  const trimmed = text.trim();
  if (!/^-?\d+$/.test(trimmed)) return null;
  const value = Number(trimmed);
  if (!Number.isInteger(value) || value < min || value > max) return null;
  return value;
}

export type ConfigObject = Record<string, unknown>;

export function asConfigObject(value: unknown): ConfigObject | null {
  return value && typeof value === "object" && !Array.isArray(value) ? (value as ConfigObject) : null;
}

/** Reads the first present key, so a camelCase GET and a snake_case write both resolve. */
export function configSection(config: ConfigObject | null, names: string[]): ConfigObject | null {
  if (!config) return null;
  for (const name of names) {
    const found = asConfigObject(config[name]);
    if (found) return found;
  }
  return null;
}

export function configField(source: ConfigObject | null, names: string[]): unknown {
  if (!source) return undefined;
  for (const name of names) {
    if (source[name] !== undefined) return source[name];
  }
  return undefined;
}

/**
 * `GET /config` answers with camelCase for these two fields even though the
 * write used snake_case. Reading only the snake_case key reported "not set"
 * for values that were set, so both spellings are checked.
 */
export function configTimeoutMinutes(config: unknown): string {
  const value = Number(configField(configSection(asConfigObject(config), ["subagent"]), ["timeout_ms", "timeoutMs"]));
  return Number.isFinite(value) && value > 0 ? String(Math.round(value / 60000)) : "";
}

export function configMaxSteps(config: unknown): string {
  const value = configField(configSection(asConfigObject(config), ["loop_control", "loopControl"]), ["max_steps_per_turn", "maxStepsPerTurn"]);
  return typeof value === "number" || typeof value === "string" ? String(value) : "";
}

/**
 * The guard defaults to on: the core only reports the field once it was set to
 * false, so an absent value is not "off".
 */
export function readDangerousGuard(config: unknown): boolean {
  const permission = configSection(asConfigObject(config), ["permission"]);
  return configField(permission, ["dangerous_command_guard", "dangerousCommandGuard"]) !== false;
}

/**
 * `POST /config` merges each top-level section key by key, so a patch that
 * carries only this field rewrites the guard and leaves the stored permission
 * rules in place. That is what makes it safe to save the guard on its own while
 * the rule rows above still hold an uncommitted edit.
 */
export function dangerousGuardPatch(enabled: boolean): Record<string, unknown> {
  return { permission: { dangerous_command_guard: enabled } };
}

export interface SubagentPoolEntry {
  key: string;
  desc: string;
}

export interface SubagentPoolState {
  entries: SubagentPoolEntry[];
  existingDefaultModel: string;
  rest: Record<string, unknown>;
}

/**
 * Reads the shared `secondary_model` / `secondaryModel` section without
 * inventing defaults when the official single-select cleared it to null.
 */
export function readSubagentPoolState(config: unknown): SubagentPoolState {
  const section = configSection(asConfigObject(config), ["secondary_model", "secondaryModel"]);
  if (!section) {
    return { entries: [], existingDefaultModel: "", rest: {} };
  }
  const models = asConfigObject(configField(section, ["models"]));
  const entries: SubagentPoolEntry[] = Object.entries(models ?? {}).map(([key, desc]) => ({
    key,
    desc: typeof desc === "string" ? desc : "",
  }));
  const explicitDefault = configField(section, ["defaultModel", "default_model"]);
  const legacyModel = configField(section, ["model"]);
  const existingDefaultModel =
    typeof explicitDefault === "string"
      ? explicitDefault.trim()
      : typeof legacyModel === "string"
        ? legacyModel.trim()
        : "";
  if (entries.length === 0 && existingDefaultModel) {
    entries.push({ key: existingDefaultModel, desc: "" });
  }
  const rest: Record<string, unknown> = { ...section };
  delete rest.models;
  delete rest.defaultModel;
  delete rest.default_model;
  delete rest.model;
  return { entries, existingDefaultModel, rest };
}

/**
 * Compares the secondary model pool read back from the core against what was
 * submitted. `GET /config` answers with camelCase field names while the write
 * used snake_case, so both spellings resolve; every model keeps its description,
 * and the default model must be the same key.
 */
export function matchesModelPool(config: unknown, expected: { models: Record<string, string>; defaultModel: string }): boolean {
  const section = configSection(asConfigObject(config), ["secondary_model", "secondaryModel"]);
  const written = asConfigObject(configField(section, ["models"])) ?? {};
  const writtenKeys = Object.keys(written);
  const wantedKeys = Object.keys(expected.models);
  if (writtenKeys.length !== wantedKeys.length) return false;
  if (!wantedKeys.every((key, index) => writtenKeys[index] === key && written[key] === expected.models[key])) {
    return false;
  }
  return configField(section, ["defaultModel", "default_model", "model"]) === expected.defaultModel;
}

/**
 * Compares the permission rules read back from the core against the submitted
 * payload, in order and field by field. A length check alone would accept a rule
 * that kept its count but changed its decision, pattern, scope or a preserved
 * extra field.
 */
export function matchesPermissionRules(config: unknown, expected: readonly unknown[]): boolean {
  const permission = configSection(asConfigObject(config), ["permission"]);
  const actual = configField(permission, ["rules"]);
  if (!Array.isArray(actual) || actual.length !== expected.length) return false;
  return expected.every((rule, index) => sameConfigValue(actual[index], rule));
}

function sameConfigValue(left: unknown, right: unknown): boolean {
  if (left === right) return true;
  if (Array.isArray(left) || Array.isArray(right)) {
    if (!Array.isArray(left) || !Array.isArray(right) || left.length !== right.length) return false;
    return left.every((item, index) => sameConfigValue(item, right[index]));
  }
  const leftObject = asConfigObject(left);
  const rightObject = asConfigObject(right);
  if (!leftObject || !rightObject) return false;
  const keys = Object.keys(rightObject);
  return keys.length === Object.keys(leftObject).length
    && keys.every((key) => sameConfigValue(leftObject[key], rightObject[key]));
}

/** Applies an incoming policy patch onto a base, keeping the fields the server owns. */
export function mergePolicy(base: CachePolicyDraft, incoming: unknown): CachePolicyDraft {
  const source = (incoming ?? {}) as Partial<CachePolicyDraft>;
  const incomingIdle = source.idleKeepAlive;
  return {
    keepAlive: { ...base.keepAlive, ...(source.keepAlive ?? {}) },
    idleKeepAlive: {
      enabled: incomingIdle?.enabled ?? base.idleKeepAlive.enabled,
      defaultCount: incomingIdle?.defaultCount ?? base.idleKeepAlive.defaultCount,
      minQuotaPercent: incomingIdle?.minQuotaPercent ?? base.idleKeepAlive.minQuotaPercent,
    },
    compaction: { ...base.compaction, ...(source.compaction ?? {}) },
    recovery: {
      ...base.recovery,
      ...(source.recovery ?? {}),
      entries: source.recovery?.entries ?? base.recovery.entries,
    },
  };
}

export interface RecoveryState {
  enabled: boolean;
  intervalSeconds: number;
  entries: { id: string; kind: string; title: string; status: string; nextAt?: string; note?: string }[];
}
/** Optional addons frozen out of the core product; both default to false. */
export interface AddonFeatures {
  planner: boolean;
  multiSession: boolean;
}
export const addonFeatureKeys = ["planner", "multiSession"] as const;
export type AddonFeatureKey = typeof addonFeatureKeys[number];
export function normalizeAddonFeatures(value: unknown): AddonFeatures {
  const source = (value ?? {}) as Partial<Record<AddonFeatureKey, unknown>>;
  return { planner: source.planner === true, multiSession: source.multiSession === true };
}
export async function readAddonFeatures(host: Pick<NativeHost, "request">): Promise<AddonFeatures> {
  return normalizeAddonFeatures(await host.request<AddonFeatures>("features.get"));
}
export async function writeAddonFeatures(host: Pick<NativeHost, "request">, next: AddonFeatures): Promise<AddonFeatures> {
  const target = normalizeAddonFeatures(next);
  return normalizeAddonFeatures(await host.request<AddonFeatures>("features.set", { planner: target.planner, multiSession: target.multiSession }));
}
export interface DemoBootstrap {
  cwd: string;
  models: { id: string; name: string }[];
  nativeModels: { id: string; name: string }[];
  threads: PlannerThread[];
  sessions: NativeSession[];
  recovery: RecoveryState;
  features?: AddonFeatures;
}
export interface NativeHooks {
  onActiveSession?: (listener: (id: string) => void) => () => void;
  onAddSession?: (listener: () => void) => () => void;
  activeSession: () => string;
  close: () => void;
  pickSession?: (exclude: string[]) => Promise<string>;
  localFilePath?: (file: File) => string;
}
export interface NativeTransport {
  request: <T = unknown>(action: string, payload?: Record<string, unknown>) => Promise<T>;
}
export interface NativeHost {
  onActiveSession?: NativeHooks["onActiveSession"];
  onAddSession?: NativeHooks["onAddSession"];
  request: <T = unknown>(action: string, payload?: Record<string, unknown>) => Promise<T>;
  subscribe: <T = unknown>(scope: string, listener: (value: T) => void) => () => void;
  activeSession: () => string;
  openSession: (id: string) => Promise<void>;
  openBrowser: (url: string, mode?: "plan" | "exec") => Promise<void>;
  browserBounds: (bounds: { visible: boolean; x?: number; y?: number; width?: number; height?: number }) => Promise<void>;
  extractBrowser: (mode?: "plan" | "exec") => Promise<string>;
  ensureWidth: (width: number) => Promise<void>;
  openSettings: (tab?: string) => Promise<void>;
  pickSession: (exclude: string[]) => Promise<string>;
  localFilePath?: NativeHooks["localFilePath"];
  close: () => void;
}
export function followNativeSession(ids: string[], id: string): string[] {
  if (!id || ids[0] === id) return ids;
  const previous = ids[0];
  return [id, ...ids.slice(1).map(item => item === id ? previous : item).filter((item): item is string => Boolean(item))];
}

export function preferPlannerThread(current: PlannerThread | null, next: PlannerThread): boolean {
  return !current || current.id !== next.id || (next.revision ?? 0) >= (current.revision ?? 0);
}

export type DemoHost = NativeHost;

/**
 * Per-mount host scope. A module-level singleton was installed and then cleared
 * by whichever panel unmounted first, so opening and closing the settings tab
 * detached every other panel. Each mount now owns its own scope.
 */
export interface HostScopeValue {
  host: NativeHost | null;
}

export const detachedHostScope: HostScopeValue = { host: null };

export const HostScopeContext = createContext<HostScopeValue>(detachedHostScope);

/** The host of the surface currently rendering; null outside a native mount. */
export function useHost(): NativeHost | null {
  return useContext(HostScopeContext).host;
}

export function requireHost(host: NativeHost | null): NativeHost {
  if (!host) throw new Error("请从 Kimi Code 的增强入口打开此功能。");
  return host;
}

interface EventPage {
  events: { sequence: number; scope: string; value: unknown }[];
  next: number;
  reset?: boolean;
}

type Listener = (value: unknown) => void;

function sleep(ms: number): Promise<void> {
  return new Promise(resolve => setTimeout(resolve, ms));
}

export interface EventChannel {
  subscribe: (scope: string, listener: Listener) => () => void;
  close: () => void;
}

/**
 * One poll loop per host scope. Every surface of that scope shares it, so the
 * renderer never runs several overlapping `events.poll` requests and a scope
 * that unmounts stops its loop.
 */
export function createEventChannel(transport: NativeTransport, retryMs = 1500): EventChannel {
  const listeners = new Map<string, Set<Listener>>();
  let cursor = -1;
  let primed = false;
  let polling = false;
  let scheduled = false;
  let activeGen = 0;
  let inFlightScopes: ReadonlySet<string> | null = null;
  let disposed = false;
  let wake: (() => void) | null = null;
  let wakeTimer: ReturnType<typeof setTimeout> | null = null;
  // A broken hop must not turn into permanent 1.5s error spam: back off up to a
  // 10s ceiling and reset as soon as a page arrives again.
  let failures = 0;
  const retryCeilingMs = 10_000;

  function notifyError(cause: unknown) {
    const message = cause instanceof Error ? cause.message : String(cause);
    for (const listener of listeners.get("native:error") ?? []) listener(message);
  }

  function nextScopes(): string[] {
    return [...listeners.keys()].filter(scope => scope !== "native:error" && (listeners.get(scope)?.size ?? 0) > 0);
  }

  async function poll(gen: number) {
    polling = true;
    try {
      while (!disposed && gen === activeGen) {
        const scopes = nextScopes();
        if (!scopes.length) break;
        inFlightScopes = new Set(scopes);
        const initial = !primed && cursor < 0;
        const started = Date.now();
        try {
          const page = await transport.request<EventPage>("events.poll", {
            after: cursor,
            scopes,
            waitMs: initial ? 0 : 8000,
          });
          if (disposed || gen !== activeGen) return;
          failures = 0;
          primed = true;
          cursor = page.next;
          if (page.reset && page.events.length) cursor = page.events[page.events.length - 1]!.sequence;
          for (const event of page.events) {
            for (const listener of [...(listeners.get(event.scope) ?? [])]) listener(event.value);
          }
          // The server long-polls, so an immediate empty answer means the wait
          // was cut short. Yielding keeps a broken hop from spinning the loop.
          if (!initial && !page.events.length && Date.now() - started < 50) await sleep(50);
        } catch (cause) {
          if (disposed || gen !== activeGen) return;
          notifyError(cause);
          const delay = Math.min(retryCeilingMs, retryMs * 2 ** Math.min(failures, 4));
          failures += 1;
          await new Promise<void>(resolve => {
            const release = () => {
              if (wakeTimer !== null) {
                clearTimeout(wakeTimer);
                wakeTimer = null;
              }
              if (wake === release) wake = null;
              resolve();
            };
            wake = release;
            wakeTimer = setTimeout(release, delay);
          });
        }
      }
    } finally {
      if (gen === activeGen) {
        polling = false;
        inFlightScopes = null;
      }
    }
  }

  function ensurePolling() {
    if (scheduled || disposed) return;
    scheduled = true;
    queueMicrotask(() => {
      scheduled = false;
      if (disposed) return;
      const scopes = nextScopes();
      if (!scopes.length) return;
      if (!polling) {
        void poll(++activeGen);
        return;
      }
      if (inFlightScopes && scopes.some(scope => !inFlightScopes!.has(scope))) {
        wake?.();
        wake = null;
        void poll(++activeGen);
      }
    });
  }

  return {
    subscribe(scope, listener) {
      let group = listeners.get(scope);
      if (!group) {
        group = new Set();
        listeners.set(scope, group);
      }
      group.add(listener);
      if (scope !== "native:error") ensurePolling();
      return () => {
        const current = listeners.get(scope);
        current?.delete(listener);
        if (current?.size === 0) listeners.delete(scope);
      };
    },
    close() {
      if (disposed) return;
      disposed = true;
      activeGen += 1;
      inFlightScopes = null;
      listeners.clear();
      if (wakeTimer !== null) {
        clearTimeout(wakeTimer);
        wakeTimer = null;
      }
      wake?.();
      wake = null;
    },
  };
}

export function createNativeHost(transport: NativeTransport, hooks: NativeHooks, scope?: EventChannel): NativeHost {
  const channel = scope ?? createEventChannel(transport);
  let disposed = false;

  const host: NativeHost = {
    onActiveSession: hooks.onActiveSession,
    onAddSession: hooks.onAddSession,
    localFilePath: hooks.localFilePath,
    request: transport.request,
    subscribe: (scopeName, listener) => channel.subscribe(scopeName, listener as Listener),
    activeSession: hooks.activeSession,
    openSession: async id => { await transport.request("host.openSession", { id }); },
    openBrowser: async (url, mode = "plan") => { await transport.request("host.openBrowser", { url, mode }); },
    browserBounds: async bounds => { await transport.request("host.browserBounds", bounds); },
    extractBrowser: async (mode = "plan") => transport.request<string>("host.extractBrowser", { mode }),
    ensureWidth: async width => { await transport.request("window.ensureWidth", { width }); },
    openSettings: async (tab?: string) => { await transport.request("window.openSettings", tab ? { tab } : {}); },
    pickSession: hooks.pickSession ?? (async exclude => transport.request<string>("host.pickSession", { exclude })),
    close: () => {
      if (disposed) return;
      disposed = true;
      // A shared channel belongs to the scope: one surface closing must not cut
      // the event stream the remaining surfaces still use.
      if (!scope) channel.close();
      hooks.close();
    },
  };
  return host;
}

interface CoreFetchResult {
  status: number;
  headers: [string, string][];
  bodyBase64: string;
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  for (let index = 0; index < bytes.length; index += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(index, index + 0x8000));
  }
  return btoa(binary);
}

export interface AccountQuotaSummary {
  status: "ok" | "unknown" | "auth_rejected";
  limit5hUsed?: number;
  limit7dUsed?: number;
  limit5hReset?: string;
  limit7dReset?: string;
  checkedAt?: string;
  error?: string;
}

export interface AccountProfile {
  userId: string;
  nickname: string;
  avatar?: string;
  plan?: string;
  region?: string;
  email?: string;
}

export interface AccountSummary {
  id: string;
  userId: string;
  label: string;
  nickname: string;
  avatar?: string;
  plan?: string;
  region?: string;
  current: boolean;
  status: "ready" | "relogin_required" | string;
  statusReason?: string;
  quota?: AccountQuotaSummary | null;
  credentialSaved: boolean;
  updatedAt: string;
}

export interface AccountBusySession {
  id: string;
  title: string;
  reason: string;
}

export interface AccountPendingSwitch {
  targetId: string;
  targetLabel: string;
  busySessionIds: string[];
  queuedAt: string;
}

export interface AccountAddFlow {
  active: boolean;
  mode: "add" | "relogin";
  previousUserId?: string;
  reloginAccountId?: string;
  startedAt?: string;
}

export interface AccountsState {
  vaultUnlocked: boolean;
  vaultOsKeychain?: boolean;
  vaultOsKeychainAvailable?: boolean;
  vaultOsKeychainBackend?: string;
  legacyPasswordVault?: boolean;
  signedIn: boolean;
  currentAccountId?: string;
  currentProfile?: AccountProfile | null;
  accounts: AccountSummary[];
  pendingSwitch?: AccountPendingSwitch | null;
  addFlow?: AccountAddFlow | null;
  cliRunning: boolean;
}

export interface AccountSwitchPreflight {
  target: AccountSummary;
  vaultUnlocked: boolean;
  canSwitch: boolean;
  canForceSwitch?: boolean;
  requiresLogin: boolean;
  busySessions: AccountBusySession[];
  warnings: string[];
  cliRunning: boolean;
  activeModel?: string;
  modelMayDiffer: boolean;
}

export interface AccountSwitchResult {
  switched: boolean;
  queued?: boolean;
  account?: AccountSummary | null;
  pendingSwitch?: AccountPendingSwitch | null;
  sessionsCount?: number;
  hostPid?: number;
  warnings?: string[];
  backupCleanupWarning?: string;
  refreshAuth: boolean;
}

export function formatAccountQuotaLine(quota?: AccountQuotaSummary | null): string {
  if (!quota || quota.status === "unknown") {
    return "5小时：未知 · 每周：未知";
  }
  if (quota.status === "auth_rejected") {
    return "令牌已失效，需要重新登录";
  }
  const parts: string[] = [];
  if (typeof quota.limit5hUsed === "number" && Number.isFinite(quota.limit5hUsed)) {
    parts.push(`5小时 ${Math.round(quota.limit5hUsed)}%`);
  } else {
    parts.push("5小时：未知");
  }
  if (typeof quota.limit7dUsed === "number" && Number.isFinite(quota.limit7dUsed)) {
    parts.push(`每周 ${Math.round(quota.limit7dUsed)}%`);
  } else {
    parts.push("每周：未知");
  }
  return parts.join(" · ");
}

export function listAccounts(host: NativeHost, refreshQuota = false): Promise<AccountsState> {
  return host.request<AccountsState>("accounts.list", refreshQuota ? { refreshQuota: true } : {});
}

export function saveCurrentAccount(host: NativeHost, label?: string): Promise<AccountSummary> {
  return host.request<AccountSummary>("accounts.saveCurrent", label ? { label } : {});
}

export function beginAddAccount(host: NativeHost, reloginAccountId?: string): Promise<{ addFlow: AccountAddFlow; savedCurrent?: AccountSummary | null }> {
  return host.request("accounts.beginAdd", reloginAccountId ? { reloginAccountId } : {});
}

export function completeAddAccount(host: NativeHost, options?: { cancel?: boolean; label?: string }): Promise<{ completed: boolean; cancelled?: boolean; reason?: string; account?: AccountSummary }> {
  return host.request("accounts.completeAdd", options ?? {});
}

export function renameAccount(host: NativeHost, id: string, label: string): Promise<AccountSummary> {
  return host.request<AccountSummary>("accounts.rename", { id, label });
}

export function deleteAccount(host: NativeHost, id: string): Promise<{ deleted: boolean; id: string }> {
  return host.request("accounts.delete", { id });
}

export function refreshAccountQuotas(host: NativeHost): Promise<Record<string, AccountQuotaSummary>> {
  return host.request("accounts.refreshQuotas", {});
}

export function preflightAccountSwitch(host: NativeHost, id: string, activeModel?: string): Promise<AccountSwitchPreflight> {
  return host.request<AccountSwitchPreflight>("accounts.switchPreflight", activeModel ? { id, activeModel } : { id });
}

export function switchAccount(
  host: NativeHost,
  id: string,
  waitUntilIdle = false,
  force = false,
): Promise<AccountSwitchResult> {
  const payload: { id: string; waitUntilIdle?: boolean; force?: boolean } = { id };
  if (force) payload.force = true;
  else if (waitUntilIdle) payload.waitUntilIdle = true;
  return host.request<AccountSwitchResult>("accounts.switch", payload);
}

export function cancelPendingAccountSwitch(host: NativeHost): Promise<{ cancelled: boolean }> {
  return host.request("accounts.cancelPending", {});
}

/** Clears the old encrypted tokens and immediately provisions a fresh
 * system-managed vault, so a forgotten legacy password stops being a dead end. */
export function resetAccountVault(host: NativeHost): Promise<AccountsState> {
  return host.request<AccountsState>("accounts.resetVault", { confirm: true });
}

export function createCoreTransport(host: NativeHost) {
  return async (path: string, init: RequestInit): Promise<Response> => {
    let body = "";
    if (typeof init.body === "string") body = bytesToBase64(new TextEncoder().encode(init.body));
    else if (init.body instanceof ArrayBuffer) body = bytesToBase64(new Uint8Array(init.body));
    else if (ArrayBuffer.isView(init.body)) body = bytesToBase64(new Uint8Array(init.body.buffer, init.body.byteOffset, init.body.byteLength));
    else if (init.body) throw new Error("当前宿主桥只支持文本或二进制请求体");
    const headers: Record<string, string> = {};
    new Headers(init.headers).forEach((value, key) => { headers[key] = value; });
    const signal = init.signal;
    let onAbort: (() => void) | null = null;
    let result: CoreFetchResult;
    try {
      result = await new Promise<CoreFetchResult>((resolve, reject) => {
        if (signal?.aborted) {
          reject(new DOMException("请求已取消", "AbortError"));
          return;
        }
        void host
          .request<CoreFetchResult>("core.fetch", { path, method: init.method ?? "GET", headers, bodyBase64: body })
          .then(resolve, reject);
        if (!signal) return;
        onAbort = () => reject(new DOMException("请求已取消", "AbortError"));
        signal.addEventListener("abort", onAbort, { once: true });
      });
    } finally {
      if (onAbort && signal) signal.removeEventListener("abort", onAbort);
    }
    const bytes = result.bodyBase64 ? Uint8Array.from(atob(result.bodyBase64), char => char.charCodeAt(0)) : new Uint8Array();
    const nullBody = result.status === 204 || result.status === 205 || result.status === 304;
    return new Response(nullBody || bytes.length === 0 ? null : bytes, { status: result.status, headers: result.headers });
  };
}
