import { createBridge } from "./bridge.js";
import {
  PluginError,
  type Disposable,
  type PluginContext,
  type PluginIdentity,
  type PluginModule,
  type PluginTheme,
  type RuntimeRequest,
  type SettingsModule,
  type SlotElement,
  type SlotName,
} from "./context.js";
import { DecorationHub, type DecorationTarget } from "./decoration.js";
import { Scope } from "./scope.js";

/**
 * One runtime serves every installed plugin in this renderer. Slot mount points
 * are registered once by the host, and a plugin only receives the ones its grant
 * covers; an ungranted slot is refused by the runtime as well as by the core, so
 * a manifest/consent mismatch cannot end in a silent permission escalation.
 */
export interface SlotHost {
  mount(slot: SlotName, element: HTMLElement): Disposable;
}

export interface RuntimeOptions {
  /** Slot element providers, keyed by slot. A missing slot is simply unavailable. */
  slots: Partial<Record<SlotName, () => SlotElement | null>>;
  /** Transport for core requests. The runtime never calls fetch directly. */
  request: RuntimeRequest;
  theme?: () => PluginTheme;
  /** Called for every failure the runtime isolates from the rest of the page. */
  onError?: (id: string, error: unknown) => void;
}

export interface ActivePlugin {
  id: string;
  theme(): PluginTheme;
  /**
   * Mounts the plugin's settings module into a container the caller owns. The
   * returned handle disposes the module and empties the container, so a detail
   * page that closes cannot leave either a live scope or stale nodes behind.
   */
  mountSettings(module: SettingsModule, element: HTMLElement): Promise<Disposable>;
  dispose(): void;
}

/** Maps a granted capability to the slot it unlocks. */
const SLOT_CAPABILITY: Record<SlotName, string> = {
  sidebar: "ui:sidebar",
  settings: "ui:settings",
  thinking: "ui:thinking",
  message: "ui:message",
};

/** Poll cadence for a plugin with no subscribers, so a loop ends instead of idling. */
const EVENT_IDLE_MS = 5000;

export function createPluginRuntime(options: RuntimeOptions) {
  const hub = new DecorationHub();
  const active = new Map<string, ActivePlugin>();
  const theme = options.theme ?? defaultTheme;
  // One loop for the whole renderer, created once and shared by every plugin.
  const pump = createEventPump((id, error, code, message) => reportFailure(id, error, code, message));

  function grantedSlots(granted: string[]): SlotName[] {
    return (Object.keys(SLOT_CAPABILITY) as SlotName[]).filter(slot => granted.includes(SLOT_CAPABILITY[slot]));
  }

  function reportFailure(id: string, error: unknown, code: string, message: string): void {
    // The host reports this code to the core, which records it as the reason a
    // plugin was stopped. A raw error often carries no code of its own, so the
    // failure is tagged here instead of letting it arrive as the generic
    // RENDERER_ERROR with no hint about which phase broke.
    const tagged =
      error instanceof Error
        ? Object.assign(error, { code: (error as { code?: string }).code || code })
        : Object.assign(new Error(message), { code, cause: error });
    options.onError?.(id, tagged);
    if (typeof console !== "undefined") console.warn(`[plugin:${id}] ${code}`, message, error);
  }


  function createContext(id: string, version: string, granted: string[], token: string, scope: Scope): PluginContext {
    const bridge = createBridge({ token, granted, request: options.request });
    const identity: PluginIdentity = { id, version, dataVersion: 1 };
    const slots = grantedSlots(granted);

    return {
      plugin: identity,
      core: { request: (action, params) => bridge.call(action, params) },
      events: {
        on(name, listener) {
          if (!granted.includes("metrics:read") && !granted.includes("sessions:read")) {
            throw new PluginError(403, "PLUGIN_CAPABILITY_DENIED", "插件没有订阅事件的能力");
          }
          return pump.subscribe(id, bridge, name, listener, scope);
        },
      },
      ui: {
        mount(slot, render) {
          if (!slots.includes(slot)) {
            throw new PluginError(403, "PLUGIN_CAPABILITY_DENIED", `插件没有挂载 ${slot} 的能力`);
          }
          const provider = options.slots[slot];
          const element = provider?.();
          if (!element) {
            return { dispose: () => {} };
          }
          const container = element.ownerDocument.createElement("div");
          container.className = `ekdp-slot ekdp-slot-${slot}`;
          container.dataset.ekdpPlugin = id;
          element.append(container);
          const childScope = new Scope();
          // The returned handle releases this one mount, not the whole plugin: a
          // plugin that mounts twice must be able to drop one surface and keep
          // the other, and the container has to leave the DOM either way.
          const release = () => {
            childScope.dispose();
            container.remove();
          };
          scope.add(release);
          try {
            childScope.add(render(container));
          } catch (error) {
            release();
            reportFailure(id, error, "PLUGIN_MOUNT_FAILED", `挂载 ${slot} 失败`);
            throw error;
          }
          return { dispose: release };
        },
        decorate(target, decorator) {
          const slot = target as DecorationTarget;
          if (!granted.includes(SLOT_CAPABILITY[slot])) {
            throw new PluginError(403, "PLUGIN_CAPABILITY_DENIED", `插件没有装饰 ${slot} 的能力`);
          }
          return hub.register(
            slot,
            (block, info) => {
              try {
                return decorator(block, info);
              } catch (error) {
                reportFailure(id, error, "PLUGIN_DECORATE_FAILED", `装饰 ${slot} 失败`);
                throw error;
              }
            },
            scope,
            error => reportFailure(id, error, "PLUGIN_DECORATE_FAILED", `装饰 ${slot} 失败`),
          );
        },
        theme,
      },
      net: { fetch: (url, init) => bridge.fetch(url, init) },
      storage: {
        async get<T>(key: string) {
          const result = await bridge.call<{ value: unknown; found: boolean }>("storage.get", { key });
          if (!result.found) return undefined;
          // The core stores the exact JSON text it was given, so the value comes
          // back as that text. A plugin that stored `3` must read `3`, not `"3"`.
          return typeof result.value === "string" ? (JSON.parse(result.value) as T) : (result.value as T);
        },
        async set(key, value) {
          await bridge.call("storage.set", { key, value });
        },
        async delete(key) {
          await bridge.call("storage.delete", { key });
        },
        async list(prefix = "") {
          const result = await bridge.call<{ keys: string[] }>("storage.list", { prefix });
          return result.keys;
        },
      },
      log(level, message) {
        if (level === "error") console.error(`[plugin:${id}]`, message);
        else if (level === "warn") console.warn(`[plugin:${id}]`, message);
        else console.info(`[plugin:${id}]`, message);
        void bridge.call("log", { level, message }).catch(() => {});
      },
      onDispose(cleanup) {
        scope.add(cleanup);
      },
    };
  }

  async function activate(
    module: PluginModule,
    install: { id: string; version: string; granted: string[]; token: string },
  ): Promise<ActivePlugin> {
    const existing = active.get(install.id);
    if (existing) return existing;
    const scope = new Scope();
    const context = createContext(install.id, install.version, install.granted, install.token, scope);
    let result: Awaited<ReturnType<PluginModule["activate"]>>;
    try {
      result = await module.activate(context);
    } catch (error) {
      // A plugin that mounts a surface and then throws must not leave that
      // surface behind: nothing recorded this scope as active, so without this
      // the nodes, listeners and timers it created would outlive every retry.
      scope.dispose();
      // A failure raised inside a mount or decorator was already reported there
      // with its own phase code; reporting it again here would turn one broken
      // render into two faults. Only a throw with no phase tag is the plugin's
      // own activation failure, and that one is what the host treats as fatal on
      // the first occurrence.
      const existing = (error as { code?: string } | null)?.code;
      const failure = Object.assign(
        error instanceof Error ? error : new Error(String(error)),
        { code: existing || "PLUGIN_ACTIVATION_FAILED" },
      );
      if (!existing) reportFailure(install.id, failure, failure.code, "插件激活失败");
      throw failure;
    }
    if (result && typeof result === "object" && "dispose" in result && typeof result.dispose === "function") {
      scope.add(() => result.dispose());
    }
    const granted = [...install.granted];
    const plugin: ActivePlugin = {
      id: install.id,
      theme,
      async mountSettings(settingsModule, element) {
        if (!granted.includes("ui:settings")) {
          throw new PluginError(403, "PLUGIN_CAPABILITY_DENIED", "插件没有提供设置页的能力");
        }
        if (typeof settingsModule.renderSettings !== "function") {
          throw new Error("插件设置模块没有导出 renderSettings(ctx, element)");
        }
        const childScope = new Scope();
        scope.add(() => childScope.dispose());
        const release = () => {
          childScope.dispose();
          element.replaceChildren();
        };
        try {
          childScope.add(await settingsModule.renderSettings(context, element));
        } catch (error) {
          release();
          reportFailure(install.id, error, "PLUGIN_SETTINGS_FAILED", "设置页挂载失败");
          throw error;
        }
        return { dispose: release };
      },
      dispose: () => scope.dispose(),
    };
    active.set(install.id, plugin);
    return plugin;
  }

  function deactivate(id: string): void {
    active.get(id)?.dispose();
    active.delete(id);
    // Dropping this plugin's subscriptions leaves the shared loop running for
    // every other plugin.
    pump.release(id);
  }

  return {
    activate,
    deactivate,
    get: (id: string) => active.get(id) ?? null,
    dispose() {
      for (const id of [...active.keys()]) deactivate(id);
      pump.stop();
      hub.dispose();
    },
    /** Diagnostics: the shared event loop must never be more than one. */
    eventLoops: () => pump.loopCount,
    /** Diagnostics for the plugin page: how many decorators and plugins are live. */
    stats() {
      return { plugins: active.size, decorators: hub.count };
    },
    hostSupported: () => DecorationHub.hostSupported(document),
  };
}

export type PluginRuntime = ReturnType<typeof createPluginRuntime>;

/**
 * The one event loop for this renderer. Every plugin subscription registers a
 * scope here and the loop asks each plugin's own bridge for its scopes in a
 * single long poll per plugin token. There is never a poll per subscription and
 * never a poll per plugin beyond the one request each plugin's token requires:
 * the core authorizes a poll by token, so one request cannot carry two plugins'
 * grants, but the loop that drives them is shared and sequential.
 */
interface PumpSubscriber {
  id: string;
  scope: string;
  listener: (payload: unknown) => void;
}

function createEventPump(reportFailure: (id: string, error: unknown, code: string, message: string) => void) {
  // One entry per plugin: the bridge is bound to that plugin's token and grants.
  const groups = new Map<string, { bridge: ReturnType<typeof createBridge>; subscriptions: Set<PumpSubscriber>; cursor: number; failures: number }>();
  let running = false;
  let stopped = false;
  // A pending wait must end the moment the runtime is disposed, otherwise the
  // loop outlives its owner by up to one poll interval and the process keeps a
  // timer alive with nothing to do.
  let wake: (() => void) | null = null;

  function wait(ms: number): Promise<void> {
    return new Promise(resolve => {
      const timer = setTimeout(() => {
        wake = null;
        resolve();
      }, ms);
      // The wait belongs to a subscription, not to the host process: an idle
      // event loop must never be the reason a page or a test cannot exit.
      (timer as unknown as { unref?: () => void }).unref?.();
      wake = () => {
        clearTimeout(timer);
        wake = null;
        resolve();
      };
    });
  }

  function scopesFor(group: { subscriptions: Set<PumpSubscriber> }): string[] {
    const wanted = new Set<string>();
    for (const subscription of group.subscriptions) wanted.add(subscription.scope);
    return [...wanted];
  }

  async function loop() {
    if (running || stopped) return;
    running = true;
    try {
      while (!stopped && groups.size > 0) {
        let worked = false;
        for (const [id, group] of groups) {
          if (stopped) break;
          const wanted = scopesFor(group);
          if (!wanted.length) continue;
          worked = true;
          try {
            const started = Date.now();
            const page = await group.bridge.call<{
              events: Array<{ sequence: number; scope: string; value: unknown }>;
              next: number;
            }>("events.poll", { after: group.cursor, scopes: wanted, waitMs: 5000 });
            if (stopped) break;
            group.cursor = page.next;
            group.failures = 0;
            for (const event of page.events) {
              for (const subscription of [...group.subscriptions]) {
                if (subscription.scope !== event.scope) continue;
                try {
                  subscription.listener(event.value);
                } catch (error) {
                  // A throwing listener stops only its own subscription; the other
                  // plugins and the remaining subscriptions keep running.
                  reportFailure(id, error, "PLUGIN_EVENT_FAILED", `处理 ${event.scope} 事件失败`);
                  group.subscriptions.delete(subscription);
                }
              }
            }
            // The core long-polls, so a fast answer means the wait was cut short
            // (or the transport is a stub). Yielding between rounds keeps a fast
            // hop from starving timers and the page: an event stream has no
            // natural backpressure, so the loop supplies it.
            if (Date.now() - started < 50) await wait(page.events.length ? 20 : 50);
          } catch (error) {
            if (stopped) break;
            group.failures += 1;
            // A disabled plugin answers every poll with the same refusal, so the
            // loop reports once and then waits instead of spinning against the core.
            if (group.failures === 1) reportFailure(id, error, "PLUGIN_EVENT_POLL_FAILED", "事件轮询失败");
            await wait(group.failures > 3 ? 15000 : 1000);
          }
        }
        if (!worked) await wait(EVENT_IDLE_MS);
      }
    } finally {
      running = false;
    }
  }

  return {
    subscribe(id: string, bridge: ReturnType<typeof createBridge>, scope: string, listener: (payload: unknown) => void, owner: Scope): Disposable {
      let group = groups.get(id);
      if (!group) {
        group = { bridge, subscriptions: new Set(), cursor: 0, failures: 0 };
        groups.set(id, group);
      }
      const subscription: PumpSubscriber = { id, scope, listener };
      group.subscriptions.add(subscription);
      void loop();
      let released = false;
      const release = () => {
        if (released) return;
        released = true;
        // Removing a subscription never touches another plugin's group, so
        // deactivating one plugin cannot stop the loop for the others.
        group!.subscriptions.delete(subscription);
        if (group!.subscriptions.size === 0) groups.delete(id);
        if (groups.size === 0) wake?.();
      };
      owner.add(release);
      return { dispose: release };
    },
    /** Drops every subscription of one plugin; other plugins keep polling. */
    release(id: string): void {
      groups.delete(id);
      if (groups.size === 0) wake?.();
    },
    stop(): void {
      // The loop leaves on its next boundary; `running` then drops to zero in the
      // `finally`, so a disposed runtime reports no live loop.
      stopped = true;
      groups.clear();
      running = false;
      wake?.();
    },
    get loopCount(): number {
      return running ? 1 : 0;
    },
  };
}

export function defaultTheme(): PluginTheme {
  const doc = typeof document === "undefined" ? null : document.documentElement;
  const configured = doc?.dataset.colorScheme;
  const scheme = configured === "light" || (configured !== "dark" && globalThis.matchMedia?.("(prefers-color-scheme: light)").matches) ? "light" : "dark";
  const style = doc ? getComputedStyle(doc) : null;
  const tokens: Record<string, string> = {};
  for (const name of ["--color-bg", "--color-text", "--color-text-secondary", "--color-line", "--color-hover", "--blue", "--font-ui"]) {
    const value = style?.getPropertyValue(name).trim();
    if (value) tokens[name] = value;
  }
  return { scheme, tokens };
}
