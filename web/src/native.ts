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
export interface SessionView {
  session: NativeSession;
  messages: DemoMessage[];
  approvals: { id: string; title: string; detail?: string; options?: { label: string; value: string }[] }[];
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

export interface CachePolicySettings {
  keepAlive: { enabled: boolean; minutes: number };
  compaction: { enabled: boolean; idleMinutes: number; contextThreshold: number };
  recovery: { enabled: boolean; intervalSeconds: number; entries: { id: string; title: string; status: string; nextAt?: string; note?: string }[] };
}

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

/** Applies an incoming policy patch onto a base, keeping the fields the server owns. */
export function mergePolicy(base: CachePolicyDraft, incoming: unknown): CachePolicyDraft {
  const source = (incoming ?? {}) as Partial<CachePolicyDraft>;
  return {
    keepAlive: { ...base.keepAlive, ...(source.keepAlive ?? {}) },
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
  popout?: (source?: "local" | "web") => Promise<void>;
  pickSession?: (exclude: string[]) => Promise<string>;
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
  popout: (source?: "local" | "web") => Promise<void>;
  openSettings: () => Promise<void>;
  pickSession: (exclude: string[]) => Promise<string>;
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
  let polling = false;
  let disposed = false;
  let wake: (() => void) | null = null;

  function notifyError(cause: unknown) {
    const message = cause instanceof Error ? cause.message : String(cause);
    for (const listener of listeners.get("native:error") ?? []) listener(message);
  }

  function nextScopes(): string[] {
    return [...listeners.keys()].filter(scope => scope !== "native:error" && (listeners.get(scope)?.size ?? 0) > 0);
  }

  async function poll() {
    if (polling || disposed) return;
    polling = true;
    try {
      while (!disposed) {
        const scopes = nextScopes();
        if (!scopes.length) break;
        const started = Date.now();
        try {
          const page = await transport.request<EventPage>("events.poll", { after: cursor, scopes, waitMs: 8000 });
          if (disposed) return;
          cursor = page.next;
          if (page.reset && page.events.length) cursor = page.events[page.events.length - 1]!.sequence;
          for (const event of page.events) {
            for (const listener of [...(listeners.get(event.scope) ?? [])]) listener(event.value);
          }
          // The server long-polls, so an immediate empty answer means the wait
          // was cut short. Yielding keeps a broken hop from spinning the loop.
          if (!page.events.length && Date.now() - started < 50) await sleep(50);
        } catch (cause) {
          if (disposed) return;
          notifyError(cause);
          await new Promise<void>(resolve => { wake = resolve; setTimeout(resolve, retryMs); });
          wake = null;
        }
      }
    } finally {
      polling = false;
    }
  }

  return {
    subscribe(scope, listener) {
      let group = listeners.get(scope);
      if (!group) {
        group = new Set();
        listeners.set(scope, group);
      }
      group.add(listener);
      if (scope !== "native:error") void poll();
      return () => {
        const current = listeners.get(scope);
        current?.delete(listener);
        if (current?.size === 0) listeners.delete(scope);
      };
    },
    close() {
      if (disposed) return;
      disposed = true;
      listeners.clear();
      wake?.();
    },
  };
}

export function createNativeHost(transport: NativeTransport, hooks: NativeHooks, scope?: EventChannel): NativeHost {
  const channel = scope ?? createEventChannel(transport);
  let disposed = false;

  const host: NativeHost = {
    onActiveSession: hooks.onActiveSession,
    onAddSession: hooks.onAddSession,
    request: transport.request,
    subscribe: (scopeName, listener) => channel.subscribe(scopeName, listener as Listener),
    activeSession: hooks.activeSession,
    openSession: async id => { await transport.request("host.openSession", { id }); },
    openBrowser: async (url, mode = "plan") => { await transport.request("host.openBrowser", { url, mode }); },
    browserBounds: async bounds => { await transport.request("host.browserBounds", bounds); },
    extractBrowser: async (mode = "plan") => transport.request<string>("host.extractBrowser", { mode }),
    ensureWidth: async width => { await transport.request("window.ensureWidth", { width }); },
    popout: hooks.popout ?? (async source => { await transport.request("window.popout", { surface: "planner", initialSource: source ?? "web" }); }),
    openSettings: async () => { await transport.request("window.openSettings"); },
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

export function createCoreTransport(host: NativeHost) {
  return async (path: string, init: RequestInit): Promise<Response> => {
    let body = "";
    if (typeof init.body === "string") body = bytesToBase64(new TextEncoder().encode(init.body));
    else if (init.body instanceof ArrayBuffer) body = bytesToBase64(new Uint8Array(init.body));
    else if (ArrayBuffer.isView(init.body)) body = bytesToBase64(new Uint8Array(init.body.buffer, init.body.byteOffset, init.body.byteLength));
    else if (init.body) throw new Error("当前宿主桥只支持文本或二进制请求体");
    const headers: Record<string, string> = {};
    new Headers(init.headers).forEach((value, key) => { headers[key] = value; });
    const result = await host.request<CoreFetchResult>("core.fetch", { path, method: init.method ?? "GET", headers, bodyBase64: body });
    const bytes = result.bodyBase64 ? Uint8Array.from(atob(result.bodyBase64), char => char.charCodeAt(0)) : new Uint8Array();
    return new Response(bytes, { status: result.status, headers: result.headers });
  };
}
