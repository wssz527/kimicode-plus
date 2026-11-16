/** The plugin-facing API. Every field here is a capability-gated channel; there
 * is deliberately no `fetch`, no DOM-wide observer and no raw credential access.
 */

export type SlotName = "sidebar" | "settings" | "thinking" | "message";

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
      decorator: (block: HTMLElement, info: BlockInfo) => Cleanup | void,
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
