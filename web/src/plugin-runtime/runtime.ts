import { createBridge } from "./bridge.js";
import {
  PluginError,
  type AttachmentPick,
  type BrowserHost,
  type BrowserView,
  type CommandInfo,
  type Disposable,
  type EntryMountHost,
  type HandoffPreview,
  type HandoffResult,
  type LayoutHost,
  type MainAreaHost,
  type PlannerThread,
  type RightPanelHost,
  type SessionHistoryPage,
  type SidebarNavHost,
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
  surface?: "host" | "popout";
  instanceId?: string;
  /** Called for every failure the runtime isolates from the rest of the page. */
  onError?: (id: string, error: unknown) => void;
  /**
   * API v2 host surfaces. Each one is optional: a renderer that does not provide
   * a surface simply refuses the matching call, which keeps the runtime usable in
   * tests and in a plain browser preview.
   */
  entryHost?: EntryMountHost;
  sidebarNavHost?: SidebarNavHost;
  mainAreaHost?: MainAreaHost;
  rightPanelHost?: RightPanelHost;
  layoutHost?: LayoutHost;
  browserHost?: BrowserHost;
  /** File picker for `attachments.pick`. Absent means the core dialog is used. */
  pickFiles?: () => Promise<Array<{ path: string; name: string }>>;
  /** Native directory picker for workspace/project selection. */
  pickDirectory?: (options?: { title?: string; defaultPath?: string }) => Promise<string | null>;
  openSession?: (id: string) => Promise<void>;
  activeSession?: () => string;
  windowHost?: { popout(pluginId: string): Promise<void>; close(): Promise<void> };
  commandHost?: {
    register(pluginId: string, name: string): Promise<unknown>;
    unregister(pluginId: string, name: string): Promise<unknown>;
    invoke(pluginId: string, name: string, args: unknown): Promise<unknown>;
  };
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
  // Command bodies live here, keyed by owner and name. Go owns the registry and
  // the authorization; this map is the actual code, and it only ever holds
  // functions the owning plugin registered in its own scope.
  const handlers = new Map<string, (args: unknown) => unknown>();
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


  function requireGrant(granted: string[], capability: string, action: string): void {
    if (!granted.includes(capability)) {
      throw new PluginError(403, "PLUGIN_CAPABILITY_DENIED", `插件没有 ${capability} 能力，已拒绝 ${action}`);
    }
  }

  /**
   * Mirrors `EventScopeAllowed` in the core. Session scopes need sessions:read,
   * planner threads need planner:run, and the two internal scopes need
   * metrics:read. An unlisted scope belongs to nobody, so it is never granted
   * here even if a future core starts publishing it.
   */
  function eventScopeGranted(granted: string[], scope: string): boolean {
    // `activity` is the counters-only stream (turn and tool lifecycle) published
    // by the core: it never carries message text, so it sits with metrics:read
    // rather than sessions:read.
    if (scope === "cache" || scope === "automation" || scope === "activity") {
      return granted.includes("metrics:read");
    }
    if (scope === "schedule") return granted.includes("schedule:manage");
    if (scope.startsWith("session:")) return granted.includes("sessions:read");
    if (scope.startsWith("planner:")) return granted.includes("planner:run");
    return false;
  }

  function createContext(id: string, version: string, apiVersion: number, granted: string[], token: string, scope: Scope): PluginContext {
    const bridge = createBridge({ token, granted, request: options.request });
    // The plugin sees the API version its own manifest declares, not the newest
    // one this host supports: a v1 plugin must not be told it is v2.
    const identity: PluginIdentity = { id, version, dataVersion: apiVersion, surface: options.surface ?? "host" };
    const slots = grantedSlots(granted);

    const sessionsV2 = granted.includes("sessions:approve") || granted.includes("sessions:abort") || granted.includes("sessions:history") || granted.includes("sessions:read")
      ? {
        approve(input: { id: string; interactionId: string; value: unknown }) {
          requireGrant(granted, "sessions:approve", "sessions.approve");
          return bridge.call("sessions.approve", input);
        },
        abort(sessionId: string) {
          requireGrant(granted, "sessions:abort", "sessions.abort");
          return bridge.call("sessions.abort", { id: sessionId });
        },
        async open(sessionId: string): Promise<void> {
          requireGrant(granted, "sessions:read", "sessions.open");
          const answer = await bridge.call<{ id: string }>("sessions.open", { id: sessionId });
          if (!options.openSession) throw new PluginError(409, "PLUGIN_SESSION_UNAVAILABLE", "当前宿主不能打开官方会话");
          await options.openSession(answer.id);
        },
        activeId(): string {
          requireGrant(granted, "sessions:read", "sessions.activeId");
          return options.activeSession?.() || "";
        },
        async history(input: { id: string; beforeOrdinal?: number; pageSize?: number; agentId?: string }): Promise<SessionHistoryPage> {
          requireGrant(granted, "sessions:history", "sessions.history");
          const page = await bridge.call<SessionHistoryPage>("sessions.history", {
            id: input.id,
            beforeOrdinal: input.beforeOrdinal ?? 0,
            pageSize: input.pageSize ?? 50,
            agentId: input.agentId ?? "main",
          });
          return { ...page, messages: page?.messages ?? [], approvals: page?.approvals ?? [] };
        },
        /**
         * Holds one reference to the session's event stream. The core subscribes
         * to the official stream for the first holder only, so two panes on one
         * session cost one subscription and closing one does not silence the
         * other. Every watch needs exactly one unwatch.
         */
        watch(sessionId: string): Promise<{ watching: boolean; sessionId: string }> {
          requireGrant(granted, "sessions:read", "sessions.watch");
          return bridge.call("sessions.watch", { id: sessionId });
        },
        unwatch(sessionId: string): Promise<{ watching: boolean; sessionId: string }> {
          requireGrant(granted, "sessions:read", "sessions.unwatch");
          return bridge.call("sessions.unwatch", { id: sessionId });
        },
      }
      : undefined;

    const browserV2 = granted.includes("browser:view") ? createBrowserApi(id, granted, scope) : undefined;
    const instanceId = options.instanceId ?? globalThis.crypto?.randomUUID?.() ?? `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
    const plannerV2 = granted.includes("planner:run") ? createPlannerApi(bridge, options.activeSession) : undefined;
    const handoffV2 = granted.includes("handoff")
      ? {
        async preview(input: { targetId?: string; text: string; mode?: string }): Promise<HandoffPreview> {
          return bridge.call<HandoffPreview>("handoff.preview", input);
        },
        async commit(input: { targetId?: string; text: string; mode?: string }): Promise<HandoffResult> {
          return bridge.call<HandoffResult>("handoff.commit", input);
        },
      }
      : undefined;
    const commandsV2 = granted.includes("commands") ? createCommandsApi(id, bridge) : undefined;
    const attachmentsV2 = granted.includes("attachments")
      ? {
        async pick() {
          if (!options.pickFiles) {
            throw new PluginError(409, "PLUGIN_ATTACHMENT_UNAVAILABLE", "当前宿主没有文件选择器");
          }
          return options.pickFiles();
        },
        /**
         * Stages files the user dropped. A renderer only holds the File object,
         * so the bytes go to the core, which writes them under its own staging
         * root and returns the paths `send` accepts. The batch is removed with
         * the send; a caller that stages and never sends leaves it to the TTL
         * sweep.
         */
        async stage(files: ArrayLike<File>): Promise<{ files: AttachmentPick[]; batch: string }> {
          const list = Array.from(files ?? []);
          if (!list.length) throw new PluginError(400, "PLUGIN_ATTACHMENT_EMPTY", "没有选择附件");
          const payload = [];
          for (const file of list) {
            if (!(file instanceof Blob)) throw new PluginError(400, "PLUGIN_ATTACHMENT_INVALID", "只能暂存拖入的文件");
            payload.push({ name: file.name, data: await bytesToBase64(new Uint8Array(await file.arrayBuffer())) });
          }
          return bridge.call<{ files: AttachmentPick[]; batch: string }>("attachments.stage", { files: payload });
        },
        release(batch: string): Promise<{ released: boolean }> {
          return bridge.call("attachments.release", { batch });
        },
        /**
         * The whole drop interaction a plugin's own surface needs: refuse the
         * browser default so the web view does not open the file, read the
         * dropped File objects and stage them. Called from a `drop` listener; a
         * drop with no files is refused locally instead of staging nothing.
         */
        async stageFromDrop(event: DragEvent): Promise<{ files: AttachmentPick[]; batch: string }> {
          event.preventDefault();
          if (!event.dataTransfer?.files?.length) {
            throw new PluginError(400, "PLUGIN_ATTACHMENT_EMPTY", "没有可暂存的拖入文件");
          }
          return attachmentsV2!.stage(event.dataTransfer.files);
        },
        async send(input: { id: string; text: string; paths: string[]; mode?: "normal" | "plan" | "tower" | "inherit"; batch?: string }) {
          return bridge.call("attachments.send", input);
        },
      }
      : undefined;
    const modelsV2 = granted.includes("models:read")
      ? {
        async list() {
          const page = await bridge.call<{ items: Array<{ id: string; name: string }> }>("models.list");
          return page?.items ?? [];
        },
      }
      : undefined;

    function createBrowserApi(pluginId: string, grants: string[], owner: Scope) {
      return {
        async open(viewId: string, url: string): Promise<BrowserView> {
          requireGrant(grants, "browser:view", "browser.open");
          const host = options.browserHost;
          if (!host) throw new PluginError(409, "PLUGIN_BROWSER_UNAVAILABLE", "当前宿主没有网页视图");
          if (!/^[A-Za-z0-9_-]{1,64}$/.test(viewId)) {
            throw new PluginError(400, "PLUGIN_BROWSER_VIEW", "viewId 只允许字母、数字、下划线和连字符");
          }
          const created = await bridge.call<{ viewId: string; url: string }>("browser.open", { viewId, url, instanceId });
          try {
            await host.open(pluginId, created.viewId, created.url);
          } catch (error) {
            await bridge.call("browser.close", { viewId: created.viewId, instanceId }).catch(() => {});
            throw error;
          }
          let closed = false;
          let currentUrl = created.url;
          const navListeners = new Set<(url: string) => void>();
          const unlistenNav = host.onNavigate?.(pluginId, created.viewId, nextUrl => {
            if (closed || typeof nextUrl !== "string" || !nextUrl) return;
            currentUrl = nextUrl;
            for (const listener of [...navListeners]) {
              try {
                listener(nextUrl);
              } catch (error) {
                reportFailure(pluginId, error, "PLUGIN_BROWSER_NAVIGATE", "浏览器导航监听错误");
              }
            }
          });
          const release = () => {
            if (closed) return;
            closed = true;
            navListeners.clear();
            unlistenNav?.();
            void host.close(pluginId, created.viewId).catch(() => {});
          };
          owner.add(release);
          return {
            id: created.viewId,
            get url() { return currentUrl; },
            async navigate(next: string) {
              const result = await bridge.call<{ url: string }>("browser.navigate", { viewId: created.viewId, url: next, instanceId });
              await host.navigate(pluginId, created.viewId, result.url);
              currentUrl = result.url;
            },
            async back() {
              await bridge.call("browser.back", { viewId: created.viewId, instanceId });
              return host.history(pluginId, created.viewId, "back");
            },
            async forward() {
              await bridge.call("browser.forward", { viewId: created.viewId, instanceId });
              return host.history(pluginId, created.viewId, "forward");
            },
            async reload() {
              await bridge.call("browser.reload", { viewId: created.viewId, instanceId });
              await host.history(pluginId, created.viewId, "reload");
            },
            async openExternal() {
              await bridge.call("browser.openExternal", { viewId: created.viewId, instanceId });
              await host.external(pluginId, created.viewId);
            },
            async setBounds(bounds) {
              const visible = bounds.visible && bounds.width > 0 && bounds.height > 0;
              await bridge.call("browser.bounds", { viewId: created.viewId, ...bounds, instanceId, visible });
              await host.bounds(pluginId, created.viewId, { ...bounds, visible });
            },
            async show(bounds) {
              const visible = bounds.width > 0 && bounds.height > 0;
              await bridge.call("browser.visible", { viewId: created.viewId, ...bounds, instanceId, visible });
              await host.bounds(pluginId, created.viewId, { ...bounds, visible });
            },
            async hide() {
              await bridge.call("browser.visible", { viewId: created.viewId, visible: false, instanceId });
              await host.bounds(pluginId, created.viewId, { x: 0, y: 0, width: 0, height: 0, visible: false });
            },
            async extract() {
              await bridge.call("browser.extract", { viewId: created.viewId, instanceId });
              return host.extract(pluginId, created.viewId);
            },
            /**
             * Runs a site adapter against this view. The plugin supplies the
             * function body and its arguments; the core authorizes the view and
             * the host executes it in the page, returning a bounded JSON value.
             */
            async adapter<T = unknown>(source: string, args?: unknown): Promise<T> {
              requireGrant(grants, "browser:view", "browser.adapter");
              if (typeof source !== "string" || !source.trim()) {
                throw new PluginError(400, "PLUGIN_ADAPTER_REQUIRED", "站点适配器缺少函数体");
              }
              const answer = await bridge.call<{ adapter: { source: string; args: unknown } }>("browser.adapter", {
                viewId: created.viewId,
                instanceId,
                adapter: { source, args: args ?? null },
              });
              return host.adapter(pluginId, created.viewId, answer.adapter) as Promise<T>;
            },
            onNavigate(listener: (url: string) => void) {
              if (closed) return { dispose() {} };
              navListeners.add(listener);
              const dispose = () => { navListeners.delete(listener); };
              owner.add(dispose);
              return { dispose };
            },
            async close() {
              await bridge.call("browser.close", { viewId: created.viewId, instanceId });
              release();
            },
          };
        },
      };
    }

    function createPlannerApi(call: typeof bridge, activeSession?: () => string) {
      const map = <T>(thread: T) => thread;
      return {
        async threads() {
          const page = await call.call<{ items: PlannerThread[] }>("planner.threads");
          return page?.items ?? [];
        },
        async models() {
          const page = await call.call<{ items: Array<{ id: string; name: string; providerId?: string }> }>("planner.models");
          return page?.items ?? [];
        },
        async create(input: { title?: string; model: string; options?: Record<string, boolean> }) {
          return map(await call.call<PlannerThread>("planner.create", input));
        },
        async get(threadId: string) {
          return call.call<PlannerThread>("planner.get", { id: threadId });
        },
        async configure(threadId: string, input: { model: string; options?: Record<string, boolean> }) {
          return call.call<PlannerThread>("planner.configure", { id: threadId, ...input });
        },
        async rename(threadId: string, title: string) {
          return call.call<PlannerThread>("planner.rename", { id: threadId, title });
        },
        async send(threadId: string, text: string, sourceSessionId?: string) {
          const resolved = sourceSessionId ?? (activeSession?.() || undefined);
          return call.call<PlannerThread>("planner.send", { id: threadId, text, sourceSessionId: resolved });
        },
        async retry(threadId: string, revision: number) {
          return call.call<PlannerThread>("planner.retry", { id: threadId, revision });
        },
        async stop(threadId: string) {
          return call.call<PlannerThread>("planner.stop", { id: threadId });
        },
        async approve(threadId: string, approvalId: string, allow: boolean) {
          return call.call<PlannerThread>("planner.approve", { id: threadId, approvalId, allow });
        },
        async remove(threadId: string) {
          return call.call("planner.delete", { id: threadId });
        },
      };
    }

    function createCommandsApi(pluginId: string, call: typeof bridge) {
      return {
        async register(name: string, input?: { title?: string; run?: (args: unknown) => unknown }): Promise<CommandInfo> {
          if (typeof input?.run === "function" && !handlers.has(`${pluginId}:${name}`)) {
            // The body lives in the plugin's own renderer scope and dies with it:
            // the registry below only ever maps a name to code this plugin wrote.
            handlers.set(`${pluginId}:${name}`, input.run);
          }
          const registered = await call.call<CommandInfo>("commands.register", { name, title: input?.title });
          await options.commandHost?.register(pluginId, name);
          return registered;
        },
        async unregister(name: string): Promise<boolean> {
          const result = await call.call<{ removed: boolean }>("commands.unregister", { name });
          handlers.delete(`${pluginId}:${name}`);
          await options.commandHost?.unregister(pluginId, name);
          return Boolean(result?.removed);
        },
        async list(): Promise<CommandInfo[]> {
          const page = await call.call<{ items: CommandInfo[] }>("commands.list");
          return page?.items ?? [];
        },
        async invoke(name: string, args?: unknown) {
          if (options.commandHost) return options.commandHost.invoke(pluginId, name, args ?? null);
          // The core decides who owns the command, whether that owner is running
          // and whether this caller may reach it. Only then is a body looked up,
          // and only in the owner's own scope: a local handler missing there is a
          // truthful failure, never a fabricated success.
          const target = await call.call<{ command: string; ownerId: string; owner: string; title?: string; args?: unknown }>(
            "commands.invoke",
            { command: name, args: args ?? null },
          );
          return invokeLocal(target);
        },
      };
    }

    return {
      plugin: identity,
      core: { request: (action, params) => bridge.call(action, params) },
      // The host's own idea of the focused session. It is an id, not a stream and
      // not a transcript: a sidebar panel needs to know which session's counters
      // to read, and reading those still goes through metrics:read.
      activeSessionId: () => options.activeSession?.() || "",
      events: {
        on(name, listener) {
          // The same scope-to-capability table the core applies decides this
          // locally, so a planner-only plugin can subscribe to its own thread
          // stream without holding sessions:read, and a plugin with neither still
          // fails here rather than on the wire.
          if (!eventScopeGranted(granted, name)) {
            throw new PluginError(403, "PLUGIN_CAPABILITY_DENIED", `插件没有订阅 ${name} 事件的能力`);
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
        async pickDirectory(pickerOptions?: { title?: string; defaultPath?: string }): Promise<string | null> {
          if (!granted.some(cap => cap === "schedule:manage" || cap === "sessions:create" || cap === "handoff" || cap === "attachments" || cap === "planner:run")) {
            throw new PluginError(403, "PLUGIN_CAPABILITY_DENIED", "插件没有打开目录选择器的能力");
          }
          if (!options.pickDirectory) {
            throw new PluginError(409, "PLUGIN_DIRECTORY_PICKER_UNAVAILABLE", "当前宿主没有目录选择器");
          }
          const chosen = await options.pickDirectory(pickerOptions);
          return typeof chosen === "string" && chosen.trim() ? chosen.trim() : null;
        },
      },
      net: { fetch: (url, init) => bridge.fetch(url, init) },
      sessions: sessionsV2,
      browser: browserV2,
      planner: plannerV2,
      handoff: handoffV2,
      commands: commandsV2,
      attachments: attachmentsV2,
      models: modelsV2,
      entry: granted.includes("ui:entry") && options.entryHost
        ? {
          add(definition) {
            const handle = options.entryHost!.register(id, definition);
            scope.add(handle);
            return handle;
          },
        }
        : undefined,
      sidebarNav: granted.includes("ui:sidebar-nav") && options.sidebarNavHost
        ? {
          add(definition) {
            const handle = options.sidebarNavHost!.register(id, definition);
            scope.add(handle);
            return handle;
          },
        }
        : undefined,
      mainArea: granted.includes("ui:main-area") && options.mainAreaHost
        ? {
          mount(definition) {
            const handle = options.mainAreaHost!.mount(id, definition);
            scope.add(() => handle.dispose());
            return handle;
          },
        }
        : undefined,
      rightPanel: granted.includes("ui:right-panel") && options.rightPanelHost
        ? {
          mount(definition) {
            const handle = options.rightPanelHost!.mount(id, definition);
            scope.add(() => handle.dispose());
            return handle;
          },
        }
        : undefined,
      window: granted.includes("ui:right-panel") && options.windowHost
        ? {
          async popout() {
            await bridge.call("ui.popout");
            await options.windowHost!.popout(id);
          },
          close: () => options.windowHost!.close(),
        }
        : undefined,
      layout: granted.includes("ui:layout") && options.layoutHost
        ? {
          acquireSidebarCollapse() {
            const handle = options.layoutHost!.acquireSidebarCollapse(id);
            scope.add(handle);
            return handle;
          },
        }
        : undefined,
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

  async function invokeLocal(target: { command: string; ownerId: string; args?: unknown }): Promise<unknown> {
    if (!target?.ownerId) throw new PluginError(502, "PLUGIN_COMMAND_PROTOCOL", "核心没有返回命令归属");
    const handler = handlers.get(`${target.ownerId}:${target.command}`);
    if (!handler) {
      throw new PluginError(409, "PLUGIN_COMMAND_UNAVAILABLE", `命令 ${target.command} 在宿主端没有可执行实现`);
    }
    return await handler(target.args ?? null);
  }

  async function activate(
    module: PluginModule,
    install: { id: string; version: string; apiVersion?: number; granted: string[]; token: string },
  ): Promise<ActivePlugin> {
    const existing = active.get(install.id);
    if (existing) return existing;
    const scope = new Scope();
    const context = createContext(install.id, install.version, install.apiVersion ?? 2, install.granted, install.token, scope);
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
    } else if (typeof result === "function") {
      // The module contract allows returning a plain cleanup function, so the
      // teardown a v2 plugin hands back must run on disable like any other.
      scope.add(result);
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
    // Command bodies belong to the plugin that wrote them: dropping the owner's
    // scope without dropping these would leave a callable stub behind.
    for (const key of [...handlers.keys()]) {
      if (key.startsWith(id + ":")) handlers.delete(key);
    }
    // Dropping this plugin's subscriptions leaves the shared loop running for
    // every other plugin.
    pump.release(id);
  }

  return {
    activate,
    deactivate,
    get: (id: string) => active.get(id) ?? null,
    invokeLocal,
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

function bytesToBase64(bytes: Uint8Array): string {
  // Chunked so a large file cannot blow the argument limit of String.fromCharCode.
  let binary = "";
  for (let index = 0; index < bytes.length; index += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(index, index + 0x8000));
  }
  return btoa(binary);
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
