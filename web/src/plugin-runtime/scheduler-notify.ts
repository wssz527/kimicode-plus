export interface SchedulerNotificationPayload {
  runId: string;
  taskId: string;
  taskName: string;
  sessionId?: string;
  eventKind: string;
  failureKind?: string;
  title: string;
  body: string;
  tag?: string;
}

export const ALLOWED_SCHEDULER_NOTIFY_EVENTS = new Set([
  "run_failed",
  "preflight_failed",
  "run_skipped",
]);

const NOTIFY_ENABLED_STORAGE_KEY = "kimi-web.notify-enabled";
const NOTIFY_SOUND_STORAGE_KEY = "kimi-web.notify-sound";

interface OfficialNotificationsStore {
  notifyEnabled?: boolean | { value?: boolean };
  notifySound?: boolean | { value?: boolean };
}

function readPiniaFlag(doc: Document | null | undefined, key: "notifyEnabled" | "notifySound"): boolean | null {
  try {
    const app = (doc?.querySelector?.("#app") as (Element & {
      __vue_app__?: {
        config?: {
          globalProperties?: {
            $pinia?: { _s?: Map<string, OfficialNotificationsStore> };
          };
        };
      };
    }) | null);
    const store = app?.__vue_app__?.config?.globalProperties?.$pinia?._s?.get("kimi.notifications");
    if (!store) return null;
    const raw = store[key];
    if (typeof raw === "boolean") return raw;
    if (raw && typeof raw === "object" && typeof raw.value === "boolean") return raw.value;
    return null;
  } catch {
    return null;
  }
}

function readStorageFlag(storage: Pick<Storage, "getItem"> | null | undefined, key: string, fallback: boolean): boolean {
  try {
    const raw = storage?.getItem?.(key);
    if (raw === null || raw === undefined) return fallback;
    return raw === "1" || raw === "true";
  } catch {
    return fallback;
  }
}

/**
 * Mirrors official 1.3.7 `kimi.notifications`: Pinia store first, then
 * `localStorage["kimi-web.notify-enabled"]` (defaults to true when unset).
 */
export function isOfficialNotificationEnabled(
  doc?: Document | null,
  storage?: Pick<Storage, "getItem"> | null,
): boolean {
  const fromStore = readPiniaFlag(doc, "notifyEnabled");
  if (fromStore !== null) return fromStore;
  return readStorageFlag(storage, NOTIFY_ENABLED_STORAGE_KEY, true);
}

export function isOfficialNotificationSoundEnabled(
  doc?: Document | null,
  storage?: Pick<Storage, "getItem"> | null,
): boolean {
  const fromStore = readPiniaFlag(doc, "notifySound");
  if (fromStore !== null) return fromStore;
  return readStorageFlag(storage, NOTIFY_SOUND_STORAGE_KEY, true);
}

export function shouldDispatchSchedulerNotification(
  payload: Partial<SchedulerNotificationPayload> | null | undefined,
  options: {
    officialEnabled: boolean;
    seenTags?: Set<string>;
  },
): { allowed: boolean; reason?: string; tag: string } {
  if (!payload || typeof payload !== "object") {
    return { allowed: false, reason: "empty_payload", tag: "" };
  }
  const eventKind = typeof payload.eventKind === "string" ? payload.eventKind : "";
  if (!ALLOWED_SCHEDULER_NOTIFY_EVENTS.has(eventKind)) {
    return { allowed: false, reason: "covered_by_official_or_unknown", tag: "" };
  }
  if (!options.officialEnabled) {
    return { allowed: false, reason: "official_notifications_disabled", tag: "" };
  }
  const tag = payload.tag || (payload.runId ? `ekd-sched-${payload.runId}` : "");
  if (tag && options.seenTags?.has(tag)) {
    return { allowed: false, reason: "duplicate", tag };
  }
  return { allowed: true, tag };
}

export interface NotificationConstructorLike {
  permission: NotificationPermission;
  new (title: string, options?: NotificationOptions): {
    onclick: ((this: unknown, ev: unknown) => unknown) | null;
    close?: () => void;
  };
}

export interface SchedulerNotifyEnv {
  document?: Document | null;
  storage?: Pick<Storage, "getItem"> | null;
  NotificationCtor?: NotificationConstructorLike | null;
  seenTags?: Set<string>;
  showWindow?: () => void;
  focusWindow?: () => void;
  openSession?: (sessionId: string) => void | Promise<void>;
  openRuns?: (target: { taskId: string; runId: string }) => void;
}

export function dispatchSchedulerNotification(
  payload: SchedulerNotificationPayload | null | undefined,
  env: SchedulerNotifyEnv = {},
): { shown: boolean; reason?: string; tag?: string } {
  const doc = env.document ?? (typeof document !== "undefined" ? document : null);
  const storage = env.storage ?? (typeof globalThis !== "undefined" && "localStorage" in globalThis ? globalThis.localStorage : null);
  const officialEnabled = isOfficialNotificationEnabled(doc, storage);
  const check = shouldDispatchSchedulerNotification(payload, { officialEnabled, seenTags: env.seenTags });
  if (!check.allowed || !payload) {
    return { shown: false, reason: check.reason };
  }
  const Ctor = env.NotificationCtor ?? (typeof Notification !== "undefined" ? (Notification as unknown as NotificationConstructorLike) : null);
  if (!Ctor) {
    return { shown: false, reason: "notification_api_unavailable" };
  }
  if (Ctor.permission !== "granted") {
    return { shown: false, reason: `permission_${Ctor.permission}` };
  }
  const soundEnabled = isOfficialNotificationSoundEnabled(doc, storage);
  try {
    const instance = new Ctor(payload.title || "KimiCode+ 定时任务", {
      body: payload.body || "",
      tag: check.tag || undefined,
      silent: !soundEnabled,
    });
    if (check.tag && env.seenTags) {
      env.seenTags.add(check.tag);
      if (env.seenTags.size > 200) {
        const first = env.seenTags.values().next().value;
        if (first) env.seenTags.delete(first);
      }
    }
    instance.onclick = () => {
      try {
        env.showWindow?.();
        env.focusWindow?.();
      } catch {
        // ignore focus errors
      }
      if (payload.sessionId && env.openSession) {
        void env.openSession(payload.sessionId);
      } else if (env.openRuns) {
        env.openRuns({ taskId: payload.taskId || "", runId: payload.runId || "" });
      }
      try {
        instance.close?.();
      } catch {
        // ignore close errors
      }
    };
    return { shown: true, tag: check.tag };
  } catch {
    return { shown: false, reason: "notification_construct_failed" };
  }
}
