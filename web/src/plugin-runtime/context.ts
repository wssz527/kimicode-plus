/** The plugin-facing API. Every field here is a capability-gated channel; there
 * is deliberately no `fetch`, no DOM-wide observer and no raw credential access.
 */

export type SlotName = "sidebar" | "settings" | "thinking" | "message";

/** One openable tab type. The same definition is injected into the session-info
 * "available" group, the right-panel empty list and the `+` menu. */
export interface EntryDefinition {
  /** Stable id inside the plugin; registering twice with one id replaces it. */
  id: string;
  /** Label shown in every official row this entry is cloned into. */
  title: string;
  /** Single SVG path (`d`, viewBox 0 0 24 24) drawn in the official icon slot. */
  icon?: string;
  open(): void | Promise<void>;
}

/** One row under the official "new chat" button. Requires ui:sidebar-nav. */
export interface SidebarNavDefinition {
  id: string;
  title: string;
  icon?: string;
  open(): void | Promise<void>;
}

export interface MainAreaDefinition {
  render(element: HTMLElement): Cleanup | void;
  /** "split" places the pane beside the native chat view; "page" replaces the main area while mounted. */
  mode?: "split" | "page";
  /** Initial split width in CSS pixels for the plugin's own side (split mode only). */
  width?: number;
  minWidth?: number;
  maxWidth?: number;
}

export interface MainAreaHandle extends Disposable {
  setWidth(px: number): void;
  width(): number;
}

export interface RightPanelDefinition {
  title: string;
  render(element: HTMLElement): Cleanup | void;
  width?: number;
}

export interface RightPanelHandle extends Disposable {
  open(): void;
  close(): void;
  readonly open_: boolean;
}

export interface SessionHistoryPage {
  sessionId: string;
  agentId: string;
  messages: Array<{ id: string; role: string; text: string; tool?: string; at?: string }>;
  approvals: Array<{ id: string; title: string; detail?: string; options?: Array<{ label: string; value: string }> }>;
  hasMore: boolean;
  nextBefore: number | null;
  pageSize: number;
}

export interface AttachmentPick {
  path: string;
  name: string;
}

export interface BrowserViewBounds {
  x: number;
  y: number;
  width: number;
  height: number;
  visible: boolean;
}

export interface BrowserView {
  readonly id: string;
  readonly url: string;
  navigate(url: string): Promise<void>;
  back(): Promise<boolean>;
  forward(): Promise<boolean>;
  reload(): Promise<void>;
  openExternal(): Promise<void>;
  setBounds(bounds: BrowserViewBounds): Promise<void>;
  show(bounds: BrowserViewBounds): Promise<void>;
  hide(): Promise<void>;
  extract(): Promise<string>;
  /**
   * Runs a site adapter (a function body this plugin writes) against its own
   * page. The answer is bounded JSON; an adapter that throws fails the call
   * instead of returning a partial result.
   */
  adapter<T = unknown>(source: string, args?: unknown): Promise<T>;
  onNavigate?(listener: (url: string) => void): Disposable;
  close(): Promise<void>;
}

export interface PlannerThread {
  id: string;
  title: string;
  model: string;
  status: string;
  revision: number;
  messages: Array<{ id: string; role: string; text: string; at?: string }>;
  workspace?: string;
  approval?: { id: string; title: string; detail?: string };
  error?: string;
}

export interface HandoffPreview {
  targetId: string;
  mode: "normal" | "plan" | "tower";
  text: string;
  bytes: number;
  newSession: boolean;
  /** Present for a new session only: the directory it would be created in. */
  cwd?: string;
  /** Present for a new session only: the model its first turn would run on. */
  model?: string;
  /** Present for a new session only. */
  title?: string;
}

export interface HandoffInput {
  targetId?: string;
  text: string;
  mode?: string;
  cwd?: string;
  model?: string;
  title?: string;
}

export interface HandoffResult {
  sessionId: string;
  /** False when the handoff went into an existing session. */
  created?: boolean;
}

export interface CommandInfo {
  name: string;
  pluginId: string;
  pluginName: string;
  title?: string;
}

/**
 * Host-owned v2 surfaces. The renderer implements these; the runtime only
 * enforces grants, ownership and disposal, so a plugin cannot reach the host
 * DOM beyond the container it is given.
 */
export interface EntryMountHost {
  register(pluginId: string, definition: EntryDefinition): Disposable;
}

export interface SidebarNavHost {
  register(pluginId: string, definition: SidebarNavDefinition): Disposable;
}

export interface MainAreaHost {
  mount(pluginId: string, definition: MainAreaDefinition): MainAreaHandle;
}

export interface RightPanelHost {
  mount(pluginId: string, definition: RightPanelDefinition): RightPanelHandle;
}

export interface LayoutHost {
  /** Returns a release handle; calling it drops one reference only. */
  acquireSidebarCollapse(pluginId: string): Disposable;
}

export interface BrowserHost {
  open(pluginId: string, viewId: string, url: string): Promise<void>;
  navigate(pluginId: string, viewId: string, url: string): Promise<void>;
  history(pluginId: string, viewId: string, action: "back" | "forward" | "reload"): Promise<boolean>;
  external(pluginId: string, viewId: string): Promise<void>;
  bounds(pluginId: string, viewId: string, bounds: BrowserViewBounds): Promise<void>;
  extract(pluginId: string, viewId: string): Promise<string>;
  /** Executes an authorized site adapter in the plugin's own view. */
  adapter(pluginId: string, viewId: string, adapter: { source: string; args: unknown }): Promise<unknown>;
  onNavigate?(pluginId: string, viewId: string, listener: (url: string) => void): () => void;
  close(pluginId: string, viewId: string): Promise<void>;
}

export interface Disposable {
  dispose(): void;
}

/**
 * A plugin may return either a Disposable or a plain cleanup function. Both are
 * registered in the plugin scope, so a plugin that writes `return () => node.remove()`
 * is not forced to allocate a wrapper object.
 */
export type Cleanup = Disposable | (() => void);

/**
 * The slice of an element a slot mount point needs. Narrowing it here keeps the
 * runtime testable without a browser DOM and documents that a mount point cannot
 * read or mutate the host beyond appending its own container.
 */
export interface SlotElement {
  ownerDocument: { createElement(tag: string): HTMLElement };
  append(child: HTMLElement): void;
}

export interface PluginIdentity {
  id: string;
  version: string;
  /** The plugin manifest API version, so a plugin can branch on the contract. */
  dataVersion: number;
  surface?: "host" | "popout";
}

export interface PluginTheme {
  scheme: "light" | "dark";
  tokens: Record<string, string>;
}

export interface BlockInfo {
  /** Stable key for the decorated block; a plugin must not use it as text. */
  key: string;
  role?: string;
  slot: Exclude<SlotName, "sidebar" | "settings">;
  /** Whether the host currently shows the block's body (a collapsed thinking
   * block is not read and not translated). */
  open: boolean;
  /** Whether the host is still appending to this block. A streaming block is
   * translated on a stable prefix and settled on the final content. */
  streaming: boolean;
  /** Monotonic content revision: incremented by host mutations in the block,
   * never by the decorator's own nodes (mark those `data-ekdp-owned`). */
  revision: number;
}

/**
 * A decorator may return an optional update channel. The hub calls `update` for
 * the same element instead of re-running the decorator on every host change, so
 * a block that streams does not rebuild its UI; `dispose` still releases just
 * this block. A decorator that returns nothing or a plain cleanup keeps working.
 */
export interface DecorationHandle extends Disposable {
  update?(info: BlockInfo): void;
}

export interface ProxyResponse {
  status: number;
  headers: Record<string, string>;
  body: string;
  url: string;
  redirectTo?: string;
}

export interface ProxyRequestInit {
  method?: string;
  headers?: Record<string, string>;
  body?: string;
  /**
   * A credential name from `credentials:<name>`. The core injects the plaintext
   * into the outbound request; the plugin never sees the value.
   */
  credential?: string;
  credentialHeader?: string;
  credentialPrefix?: string;
}

export interface PluginContext {
  plugin: PluginIdentity;
  core: { request<T = unknown>(action: string, params?: unknown): Promise<T> };
  events: { on(name: string, listener: (payload: unknown) => void): Disposable };
  ui: {
    mount(slot: SlotName, render: (element: HTMLElement) => Cleanup | void): Disposable;
    decorate(
      target: Exclude<SlotName, "sidebar" | "settings">,
      decorator: (block: HTMLElement, info: BlockInfo) => Cleanup | DecorationHandle | void,
    ): Disposable;
    theme(): PluginTheme;
  };
  net: { fetch(url: string, init?: ProxyRequestInit): Promise<ProxyResponse> };
  storage: {
    get<T = unknown>(key: string): Promise<T | undefined>;
    set(key: string, value: unknown): Promise<void>;
    delete(key: string): Promise<void>;
    list(prefix?: string): Promise<string[]>;
  };
  log(level: "info" | "warn" | "error", message: string): void;
  /** Register a teardown that runs when the plugin is disabled or the host closes. */
  onDispose(cleanup: () => void): void;

  // --- API v2, privileged -------------------------------------------------
  /** Right-panel "openable" list, empty right-panel list and `+` menu entry. Requires ui:entry. */
  entry?: { add(definition: EntryDefinition): Disposable };
  /** A row under the official sidebar "new chat" button. Requires ui:sidebar-nav. */
  sidebarNav?: { add(definition: SidebarNavDefinition): Disposable };
  /** A container beside the native chat view, with a resizable split. */
  mainArea?: { mount(definition: MainAreaDefinition): MainAreaHandle };
  /** A dynamic right-panel tab with a close button. */
  rightPanel?: { mount(definition: RightPanelDefinition): RightPanelHandle };
  window?: { popout(): Promise<void>; close(): Promise<void> };
  layout?: { acquireSidebarCollapse(): Disposable };
  sessions?: {
    approve(input: { id: string; interactionId: string; value: unknown }): Promise<unknown>;
    abort(id: string): Promise<unknown>;
    open(id: string): Promise<void>;
    activeId?(): string;
    history(input: { id: string; beforeOrdinal?: number; pageSize?: number; agentId?: string }): Promise<SessionHistoryPage>;
    /**
     * Holds one reference to a session's event stream; every call needs exactly
     * one `unwatch`. The core opens the official subscription for the first
     * holder and closes it when the last one releases. Requires sessions:read.
     */
    watch(id: string): Promise<{ watching: boolean; sessionId: string }>;
    unwatch(id: string): Promise<{ watching: boolean; sessionId: string }>;
  };
  attachments?: {
    pick(): Promise<AttachmentPick[]>;
    /** Stages files the user dropped; the returned batch is released with `send`. */
    stage(files: ArrayLike<File>): Promise<{ files: AttachmentPick[]; batch: string }>;
    /** The full drop handler: refuses the browser default, then stages. */
    stageFromDrop(event: DragEvent): Promise<{ files: AttachmentPick[]; batch: string }>;
    release(batch: string): Promise<{ released: boolean }>;
    send(input: { id: string; text: string; paths: string[]; mode?: "normal" | "plan" | "tower" | "inherit"; batch?: string }): Promise<unknown>;
  };
  browser?: {
    open(viewId: string, url: string): Promise<BrowserView>;
  };
  planner?: {
    threads(): Promise<PlannerThread[]>;
    models(): Promise<Array<{ id: string; name: string; providerId?: string }>>;
    create(input: { title?: string; model: string; options?: Record<string, boolean> }): Promise<PlannerThread>;
    get(id: string): Promise<PlannerThread>;
    configure(id: string, input: { model: string; options?: Record<string, boolean> }): Promise<PlannerThread>;
    rename(id: string, title: string): Promise<PlannerThread>;
    send(id: string, text: string, sourceSessionId?: string): Promise<PlannerThread>;
    retry(id: string, revision: number): Promise<PlannerThread>;
    stop(id: string): Promise<PlannerThread>;
    approve(id: string, approvalId: string, allow: boolean): Promise<PlannerThread>;
    remove(id: string): Promise<unknown>;
  };
  handoff?: {
    /**
     * `cwd`, `model` and `title` choose the new session's directory, model and
     * title. They apply only when no `targetId` is given; an existing session
     * keeps its own and the core ignores them.
     */
    preview(input: HandoffInput): Promise<HandoffPreview>;
    commit(input: HandoffInput): Promise<HandoffResult>;
  };
  models?: {
    list(): Promise<Array<{
      id: string;
      name: string;
      provider?: string;
      model?: string;
      capabilities?: string[];
      support_efforts?: string[];
      adaptive_thinking?: boolean;
      max_context_size?: number;
    }>>;
  };
  commands?: {
    /**
     * Registers a command. `run` is the body this plugin's own renderer executes
     * when any plugin invokes it through `ctx.commands.invoke`; the core decides
     * ownership and authorization, and the body dies with the plugin.
     */
    register(name: string, input?: { title?: string; run?: (args: unknown) => unknown }): Promise<CommandInfo>;
    unregister(name: string): Promise<boolean>;
    list(): Promise<CommandInfo[]>;
    invoke(name: string, args?: unknown): Promise<unknown>;
  };
}

export interface PluginModule {
  activate(context: PluginContext): void | Cleanup | { status?: string } | Promise<void | Cleanup | { status?: string }>;
}

export interface SettingsModule {
  renderSettings(context: PluginContext, element: HTMLElement): void | Cleanup | Promise<void | Cleanup>;
}

export class PluginError extends Error {
  readonly code: string;
  readonly status: number;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "PluginError";
    this.status = status;
    this.code = code;
  }
}

export type RuntimeRequest = (path: string, body: unknown) => Promise<unknown>;
