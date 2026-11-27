import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import vm from "node:vm";

interface MiniNode {
  tagName: string;
  nodeType: number;
  textContent: string;
  className: string;
  classList: { contains(token: string): boolean };
  disabled: boolean;
  checked: boolean;
  hidden: boolean;
  type: string;
  showModal(): void;
  getClientRects(): unknown[];
  focus(): void;
  removeAttribute(name: string): void;
  closest(selector: string): MiniNode | null;
  fire(type: string, metadata?: Record<string, unknown>): { defaultPrevented: boolean };
  before(...nodes: Array<MiniNode | string>): void;
  value: string;
  dataset: Record<string, string>;
  attributes: Map<string, string>;
  listeners: Map<string, Array<(event: unknown) => void>>;
  children: MiniNode[];
  parentElement: MiniNode | null;
  isConnected: boolean;
  shown: boolean;
  setAttribute(name: string, value: string): void;
  getAttribute(name: string): string | null;
  addEventListener(type: string, handler: (event: unknown) => void): void;
  dispatchEvent(event: Event): boolean;
  click(): void;
  append(...nodes: Array<MiniNode | string>): void;
  after(...nodes: Array<MiniNode | string>): void;
  replaceChildren(...nodes: Array<MiniNode | string>): void;
  remove(): void;
  close(): void;
  querySelectorAll(selector: string): MiniNode[];
  querySelector(selector: string): MiniNode | null;
}

function createMiniDocument() {
  let activeElement: MiniNode;
  const docCaptureListeners = new Map<string, Set<(event: unknown) => void>>();
  const docBubbleListeners = new Map<string, Set<(event: unknown) => void>>();
  function makeNode(tagName: string, textContent = ""): MiniNode {
    const node: MiniNode = {
      tagName: tagName.toUpperCase(),
      nodeType: tagName === "#text" ? 3 : 1,
      textContent,
      className: "",
      classList: {
        contains(token: string) {
          return node.className.split(/\s+/).includes(token);
        },
      },
      disabled: false,
      checked: false,
      hidden: false,
      type: '',
      getClientRects() {
        for (let current: MiniNode | null = this; current; current = current.parentElement) {
          if (current.hidden) return [];
        }
        return this.isConnected ? [{}] : [];
      },
      focus() {
        activeElement = this;
      },
      removeAttribute(name) {
        this.attributes.delete(name);
      },
      closest(selector) {
        for (let current: MiniNode | null = this; current; current = current.parentElement) {
          if (matches(current, selector)) return current;
        }
        return null;
      },
      value: "",
      dataset: {},
      attributes: new Map<string, string>(),
      listeners: new Map<string, Array<(event: unknown) => void>>(),
      children: [],
      parentElement: null,
      isConnected: false,
      shown: false,
      setAttribute(name: string, value: string) {
        this.attributes.set(name, String(value));
        if (name === "value") this.value = value;
        if (name === "type") this.type = value;
      },
      getAttribute(name: string) {
        return this.attributes.get(name) ?? null;
      },
      addEventListener(type: string, handler: (event: unknown) => void) {
        const list = this.listeners.get(type) ?? [];
        list.push(handler);
        this.listeners.set(type, list);
      },
      dispatchEvent(event: Event) {
        Object.defineProperty(event, "target", { value: this, configurable: true });
        for (const handler of docCaptureListeners.get(event.type) ?? []) handler(event);
        for (let current: MiniNode | null = this; current; current = event.bubbles ? current.parentElement : null) {
          for (const handler of current.listeners.get(event.type) ?? []) handler(event);
        }
        if (event.bubbles) {
          for (const handler of docBubbleListeners.get(event.type) ?? []) handler(event);
        }
        return !event.defaultPrevented;
      },
      fire(type, metadata = {}) {
        const event = {
          type,
          target: this,
          defaultPrevented: false,
          stopped: false,
          preventDefault() { this.defaultPrevented = true; },
          stopPropagation() { this.stopped = true; },
          stopImmediatePropagation() { this.stopped = true; },
          ...metadata,
        };
        for (const handler of docCaptureListeners.get(type) ?? []) {
          if (event.stopped) break;
          handler(event);
        }
        for (let current: MiniNode | null = this; current && !event.stopped; current = current.parentElement) {
          for (const handler of current.listeners.get(type) ?? []) handler(event);
        }
        if (!event.stopped) {
          for (const handler of docBubbleListeners.get(type) ?? []) {
            if (event.stopped) break;
            handler(event);
          }
        }
        return event;
      },
      click() {
        if (this.disabled) return;
        this.focus();
        if (this.type === "checkbox") this.checked = !this.checked;
        if (this.type === "radio") {
          const name = this.getAttribute("name");
          for (const radio of body.querySelectorAll(`input[name="${name}"]`)) radio.checked = radio === this;
        }
        this.fire("click");
        if (this.type === "checkbox" || this.type === "radio") {
          this.fire("input");
          this.fire("change");
        }
      },
      before(...nodes) {
        if (!this.parentElement) return;
        const parent = this.parentElement;
        const index = parent.children.indexOf(this);
        parent.append(...nodes);
        parent.children.splice(index, 0, ...parent.children.splice(-nodes.length));
      },
      append(...nodes: Array<MiniNode | string>) {
        for (const child of nodes) {
          const item = typeof child === "string" ? makeNode("#text", child) : child;
          item.parentElement = this;
          function connect(node: MiniNode, connected: boolean) {
            node.isConnected = connected;
            node.children.forEach(child => connect(child, connected));
          }
          connect(item, this.isConnected);
          this.children.push(item);
        }
      },
      after(...nodes: Array<MiniNode | string>) {
        if (!this.parentElement) return;
        const parent = this.parentElement;
        const idx = parent.children.indexOf(this);
        if (idx === -1) return;
        const inserted: MiniNode[] = [];
        function connect(node: MiniNode, connected: boolean) {
          node.isConnected = connected;
          node.children.forEach(child => connect(child, connected));
        }
        for (const child of nodes) {
          const item = typeof child === "string" ? makeNode("#text", child) : child;
          item.parentElement = parent;
          connect(item, parent.isConnected);
          inserted.push(item);
        }
        parent.children.splice(idx + 1, 0, ...inserted);
      },
      replaceChildren(...nodes: Array<MiniNode | string>) {
        for (const old of this.children) {
          old.parentElement = null;
          old.isConnected = false;
        }
        this.children = [];
        this.append(...nodes);
      },
      remove() {
        this.isConnected = false;
        if (!this.parentElement) return;
        this.parentElement.children = this.parentElement.children.filter(item => item !== this);
        this.parentElement = null;
      },
      showModal() {
        this.shown = true;
      },
      close() {
        this.shown = false;
      },
      querySelectorAll(selector: string) {
        const out: MiniNode[] = [];
        const visit = (current: MiniNode) => {
          for (const child of current.children) {
            if (matches(child, selector)) out.push(child);
            visit(child);
          }
        };
        visit(this);
        return out;
      },
      querySelector(selector: string) {
        return this.querySelectorAll(selector)[0] ?? null;
      },
    };
    return node;
  }

  function matchesSingle(node: MiniNode, selector: string): boolean {
    if (node.nodeType !== 1) return false;
    const trimmed = selector.trim();
    if (!trimmed) return false;
    let rest = trimmed;
    const tagMatch = rest.match(/^[a-zA-Z0-9_-]+/);
    if (tagMatch) {
      if (node.tagName !== tagMatch[0].toUpperCase()) return false;
      rest = rest.slice(tagMatch[0].length);
    }
    while (rest.length > 0) {
      if (rest.startsWith(".")) {
        const clsMatch = rest.slice(1).match(/^[a-zA-Z0-9_-]+/);
        if (!clsMatch) return false;
        if (!node.className.split(/\s+/).includes(clsMatch[0])) return false;
        rest = rest.slice(1 + clsMatch[0].length);
      } else if (rest.startsWith("#")) {
        const idMatch = rest.slice(1).match(/^[a-zA-Z0-9_-]+/);
        if (!idMatch) return false;
        if (node.getAttribute("id") !== idMatch[0]) return false;
        rest = rest.slice(1 + idMatch[0].length);
      } else if (rest.startsWith("[")) {
        const end = rest.indexOf("]");
        if (end === -1) return false;
        const inner = rest.slice(1, end);
        const eq = inner.indexOf("=");
        if (eq === -1) {
          if (!node.attributes.has(inner)) return false;
        } else {
          const attr = inner.slice(0, eq).trim();
          const rawVal = inner.slice(eq + 1).trim().replace(/^["']|["']$/g, "");
          if (node.getAttribute(attr) !== rawVal) return false;
        }
        rest = rest.slice(end + 1);
      } else {
        return false;
      }
    }
    return true;
  }

  function hasAncestorMatching(node: MiniNode, parts: string[]): boolean {
    if (parts.length === 0) return true;
    const target = parts[parts.length - 1]!;
    let cur = node.parentElement;
    while (cur) {
      if (matchesSingle(cur, target)) {
        return hasAncestorMatching(cur, parts.slice(0, -1));
      }
      cur = cur.parentElement;
    }
    return false;
  }

  function matches(node: MiniNode, selectorGroup: string): boolean {
    for (const selector of selectorGroup.split(",")) {
      const parts = selector.trim().split(/\s+/).filter(Boolean);
      if (parts.length === 0) continue;
      if (matchesSingle(node, parts[parts.length - 1]!) && hasAncestorMatching(node, parts.slice(0, -1))) {
        return true;
      }
    }
    return false;
  }

  const head = makeNode("head");
  const body = makeNode("body");
  head.isConnected = true;
  body.isConnected = true;
  activeElement = body;
  const isCapture = (options?: unknown) =>
    options === true || (typeof options === "object" && options !== null && Boolean((options as { capture?: boolean }).capture));

  return {
    head,
    body,
    get activeElement() { return activeElement; },
    addEventListener(type: string, handler: (event: unknown) => void, options?: unknown) {
      const map = isCapture(options) ? docCaptureListeners : docBubbleListeners;
      const set = map.get(type) ?? new Set();
      set.add(handler);
      map.set(type, set);
    },
    removeEventListener(type: string, handler: (event: unknown) => void, options?: unknown) {
      const map = isCapture(options) ? docCaptureListeners : docBubbleListeners;
      map.get(type)?.delete(handler);
    },
    createElement: (tag: string) => makeNode(tag),
    createElementNS: (_ns: string, tag: string) => makeNode(tag),
    createTextNode: (text: string) => makeNode("#text", text),
    querySelector: (selector: string) => body.querySelector(selector),
    querySelectorAll: (selector: string) => body.querySelectorAll(selector),
  };
}

type Model = Record<string, unknown>;
type Action = { action: string; payload?: Record<string, unknown> };

interface FixtureEnv {
  location?: Record<string, unknown> | null;
  sessionStorage?: { getItem(key: string): string | null } | (() => never);
  officialOrigin?: string | (() => string | null);
  excludeOrigins?: string[];
  provideResponse?: boolean;
  readOfficialConfig?: (() => unknown) | null;
  installReadOfficialConfig?: (() => unknown) | null;
  officialConfig?: Record<string, unknown> | null;
}

interface SaveOfficialFormCall {
  providerId: string;
  expectedOrigin: string;
  payload: { new_id?: string; models: Array<Record<string, unknown>>; [key: string]: unknown };
  opening: Record<string, { raw: Record<string, unknown>; merged?: Record<string, unknown> | null }>;
  mergedPayload?: { new_id?: string; models: Array<Record<string, unknown>>; [key: string]: unknown };
}

function cleanWireList(raw: unknown): string[] {
  if (!Array.isArray(raw)) return [];
  return raw.filter((v): v is string => typeof v === "string").map(v => v.trim()).filter(Boolean);
}

function uniqueWireEfforts(raw: unknown): string[] {
  return [...new Set(cleanWireList(raw).filter(v => v.toLowerCase() !== "no"))];
}

function sameWireList(left: string[], right: string[]): boolean {
  return left.length === right.length && left.every((value, index) => value === right[index]);
}

function wireSnapshotOf(model: Record<string, unknown> = {}) {
  const rawCaps = Object.hasOwn(model, "capabilities") ? model.capabilities : undefined;
  const rawEfforts = Object.hasOwn(model, "support_efforts")
    ? model.support_efforts
    : Object.hasOwn(model, "supportEfforts")
      ? model.supportEfforts
      : undefined;
  const rawAdaptive = Object.hasOwn(model, "adaptive_thinking")
    ? model.adaptive_thinking
    : Object.hasOwn(model, "adaptiveThinking")
      ? model.adaptiveThinking
      : undefined;
  const rawDefault = Object.hasOwn(model, "default_effort")
    ? model.default_effort
    : Object.hasOwn(model, "defaultEffort")
      ? model.defaultEffort
      : undefined;
  const rawOff = Object.hasOwn(model, "off_effort")
    ? model.off_effort
    : Object.hasOwn(model, "offEffort")
      ? model.offEffort
      : undefined;
  return {
    capabilities: cleanWireList(rawCaps),
    support_efforts: cleanWireList(rawEfforts),
    adaptive_thinking: typeof rawAdaptive === "boolean" ? rawAdaptive : undefined,
    default_effort: typeof rawDefault === "string" ? rawDefault.trim() : "",
    off_effort: typeof rawOff === "string" ? rawOff.trim() : "",
  };
}

function sameWireSnapshotField(field: string, left: unknown, right: unknown): boolean {
  if (field === "capabilities") return sameWireList(cleanWireList(left), cleanWireList(right));
  if (field === "support_efforts") {
    return sameWireList(cleanWireList(left), cleanWireList(right))
      || sameWireList(uniqueWireEfforts(left), uniqueWireEfforts(right));
  }
  if (field === "adaptive_thinking") return Boolean(left) === Boolean(right);
  return String(left ?? "") === String(right ?? "");
}

function hasWireKey(item: Record<string, unknown>, snake: string, camel?: string): boolean {
  return Object.hasOwn(item, snake) || Boolean(camel && Object.hasOwn(item, camel));
}

function isFieldUnchangedFromOpening(
  opening: { raw?: Record<string, unknown>; merged?: Record<string, unknown> | null } | undefined,
  field: string,
  itemValue: unknown,
): boolean {
  if (!opening?.raw) return true;
  const candidates = opening.merged ? [opening.raw, opening.merged] : [opening.raw];
  return candidates.some(candidate => sameWireSnapshotField(field, itemValue, candidate[field]));
}

function simulateBackendSaveOfficialForm(
  activeConfig: { providers: Record<string, Record<string, unknown>>; models: Record<string, Model> },
  providerId: string,
  payload: { new_id?: string; models: Array<Record<string, unknown>>; [key: string]: unknown },
  opening: Record<string, { raw?: Record<string, unknown>; merged?: Record<string, unknown> | null }> = {},
) {
  const merged = structuredClone(payload);
  const mergedModels: Record<string, ReturnType<typeof wireSnapshotOf>> = {};
  for (const item of merged.models) {
    if (!item || typeof item !== "object") continue;
    const modelId = String(item.model ?? "").trim();
    if (!modelId) continue;
    const qualified = `${providerId}/${modelId}`;
    const stored = Object.hasOwn(activeConfig.models, qualified)
      ? activeConfig.models[qualified]
      : (Object.hasOwn(activeConfig.models, modelId) ? activeConfig.models[modelId] : null);
    if (stored) {
      const storedWire = wireSnapshotOf(stored);
      const storedOutput = (stored.maxOutputSize ?? stored.max_output_size) as number | undefined;
      const nextContext = Number(item.max_context_size ?? item.maxContextSize);
      if (!hasWireKey(item, "max_output_size", "maxOutputSize")) {
        if (typeof storedOutput === "number" && Number.isSafeInteger(storedOutput) && storedOutput > 0
          && Number.isSafeInteger(nextContext) && storedOutput <= nextContext) {
          item.max_output_size = storedOutput;
        }
      }
      const rawSnap = wireSnapshotOf(item);
      const openEntry = Object.hasOwn(opening, modelId) ? opening[modelId] : undefined;
      if (!hasWireKey(item, "capabilities") || isFieldUnchangedFromOpening(openEntry, "capabilities", rawSnap.capabilities)) {
        if (storedWire.capabilities.length) item.capabilities = [...storedWire.capabilities];
        else delete item.capabilities;
      }
      if (!hasWireKey(item, "support_efforts", "supportEfforts") || isFieldUnchangedFromOpening(openEntry, "support_efforts", rawSnap.support_efforts)) {
        delete item.supportEfforts;
        if (storedWire.support_efforts.length) item.support_efforts = [...storedWire.support_efforts];
        else delete item.support_efforts;
      }
      if (!hasWireKey(item, "adaptive_thinking", "adaptiveThinking") || isFieldUnchangedFromOpening(openEntry, "adaptive_thinking", rawSnap.adaptive_thinking)) {
        delete item.adaptiveThinking;
        if (typeof storedWire.adaptive_thinking === "boolean") item.adaptive_thinking = storedWire.adaptive_thinking;
        else delete item.adaptive_thinking;
      }
      if (!hasWireKey(item, "default_effort", "defaultEffort") || isFieldUnchangedFromOpening(openEntry, "default_effort", rawSnap.default_effort)) {
        delete item.defaultEffort;
        if (storedWire.default_effort) item.default_effort = storedWire.default_effort;
        else delete item.default_effort;
      }
      if (!hasWireKey(item, "off_effort", "offEffort") || isFieldUnchangedFromOpening(openEntry, "off_effort", rawSnap.off_effort)) {
        delete item.offEffort;
        if (storedWire.off_effort) item.off_effort = storedWire.off_effort;
        else delete item.off_effort;
      }
    }
    mergedModels[modelId] = wireSnapshotOf(item);
  }
  applyOfficialPutToConfig(activeConfig, providerId, merged);
  return {
    merged,
    result: {
      data: { status: "ok", providerId: typeof merged.new_id === "string" && merged.new_id.trim() ? merged.new_id.trim() : providerId },
      mergedModels,
    },
  };
}

function loadFixture(
  request?: (action: string, payload?: Record<string, unknown>) => Promise<unknown>,
  providerId = "team",
  env: FixtureEnv = {},
) {
  const doc = createMiniDocument();
  const window = new EventTarget();
  const calls: Action[] = [];
  const fetchCalls: Array<{ url: string; init?: Record<string, unknown> }> = [];
  const saveCalls: SaveOfficialFormCall[] = [];
  const config = {
    providers: { [providerId]: { type: "openai", base_url: "https://example.com/v1" } } as Record<string, Record<string, unknown>>,
    models: {
      [`${providerId}/family/alpha`]: { maxContextSize: 262144, capabilities: ["audio_in", "image_in", "tool_use", "thinking"], supportEfforts: ["no", "high", "high"], adaptiveThinking: true },
      [`${providerId}/beta`]: { max_context_size: 65536, max_output_size: 1024 },
    } as Record<string, Model>,
  };
  let officialConfig: Record<string, unknown> | null = env.officialConfig === undefined
    ? structuredClone(config)
    : env.officialConfig;
  let fetchResponder: ((url: string, init?: Record<string, unknown>) => Promise<{ ok: boolean; status: number; json?: () => Promise<unknown> }>) | null = null;
  const baseFetch = async (input: unknown, init?: Record<string, unknown>) => {
    const url = typeof input === "string"
      ? input
      : input instanceof URL
        ? input.href
        : String((input as { url?: string } | null)?.url ?? "");
    fetchCalls.push({ url, init: init ? { ...init } : undefined });
    if (fetchResponder) return fetchResponder(url, init);
    return { ok: true, status: 200, json: async () => ({ status: "ok" }) };
  };
  const location = env.location === undefined
    ? { href: "http://127.0.0.1:54321/settings/providers", origin: "http://127.0.0.1:54321", search: "" }
    : env.location;
  const sandbox: Record<string, unknown> = {
    document: doc,
    window,
    URL,
    URLSearchParams,
    DOMException,
    AbortController,
    AbortSignal,
    fetch: baseFetch,
    Event,
    CustomEvent: Event,
    setTimeout,
    clearTimeout,
    MutationObserver: class { observe() {} disconnect() {} },
  };
  if (location !== null) sandbox.location = location;
  if (env.provideResponse) sandbox.Response = Response;
  if (typeof env.sessionStorage === "function") {
    const thrower = env.sessionStorage;
    Object.defineProperty(sandbox, "sessionStorage", {
      configurable: true,
      get() {
        return thrower();
      },
    });
  } else if (env.sessionStorage !== undefined) {
    sandbox.sessionStorage = env.sessionStorage;
  }
  const context = vm.createContext(sandbox);
  vm.runInContext(readFileSync("../internal/desktop/assets/provider_enhance.js", "utf8"), context);
  let failConfigGetError: Error | null = null;
  let configGetOverride: (() => unknown) | null = null;
  let failSaveOfficialFormError: Error | null = null;
  let saveOfficialFormOverride: ((payload: Record<string, unknown>, reqOptions?: { signal?: AbortSignal | null }) => Promise<unknown> | unknown) | null = null;
  let backendLock: Promise<void> = Promise.resolve();
  const resolveExpectedBoundOrigin = (): string => {
    const parseOrigin = (raw: unknown): string => {
      if (typeof raw !== "string" || !raw.trim()) return "";
      try {
        const parsed = new URL(raw.trim());
        if (parsed.protocol !== "http:" && parsed.protocol !== "https:") return "";
        return parsed.origin;
      } catch {
        return "";
      }
    };
    if (env.officialOrigin !== undefined) {
      const raw = typeof env.officialOrigin === "function" ? env.officialOrigin() : env.officialOrigin;
      return parseOrigin(raw);
    }
    const loc = location as { search?: string; href?: string; origin?: string } | null;
    if (loc?.search) {
      const q = new URLSearchParams(loc.search).get("kimi_origin");
      if (q) return parseOrigin(q);
    }
    if (typeof env.sessionStorage === "object" && env.sessionStorage !== null) {
      const stored = env.sessionStorage.getItem("kimi-desktop-server-origin");
      if (stored) return parseOrigin(stored);
    }
    if (loc?.origin && loc.origin !== "null") return parseOrigin(loc.origin);
    if (loc?.href) return parseOrigin(loc.href);
    return "";
  };
  const withBackendLock = async <T>(fn: () => Promise<T> | T, signal?: AbortSignal | null): Promise<T> => {
    const abortErr = () => signal?.reason ?? new DOMException("The operation was aborted.", "AbortError");
    if (signal?.aborted) throw abortErr();
    const prev = backendLock;
    let release!: () => void;
    backendLock = new Promise<void>(resolve => { release = resolve; });
    let abortedWhileQueued = false;
    let onAbort: (() => void) | undefined;
    try {
      if (signal) {
        await new Promise<void>((resolve, reject) => {
          onAbort = () => {
            abortedWhileQueued = true;
            reject(abortErr());
          };
          signal.addEventListener("abort", onAbort, { once: true });
          prev.then(() => {
            if (onAbort) signal.removeEventListener("abort", onAbort);
            resolve();
          });
        });
      } else {
        await prev;
      }
      if (signal?.aborted) throw abortErr();
      return await fn();
    } finally {
      if (onAbort) signal?.removeEventListener("abort", onAbort);
      if (abortedWhileQueued) {
        prev.finally(() => release());
      } else {
        release();
      }
    }
  };
  const enhanceOptions: Record<string, unknown> = {
    request: async (action: string, payload?: Record<string, unknown>, reqOptions?: { signal?: AbortSignal | null }) => {
      const signal = reqOptions?.signal ?? null;
      if (signal?.aborted) {
        throw signal.reason ?? new DOMException("The operation was aborted.", "AbortError");
      }
      calls.push(JSON.parse(JSON.stringify({ action, payload })) as Action);
      if (action === "config.get") {
        if (failConfigGetError) throw failConfigGetError;
        if (configGetOverride) return configGetOverride();
        return structuredClone(config);
      }
      if (action === "providers.saveOfficialForm" && payload) {
        assert.equal(typeof payload.expectedOrigin, "string", "providers.saveOfficialForm must always include expectedOrigin");
        assert.ok(payload.expectedOrigin, "expectedOrigin must be non-empty");
        const boundOrigin = resolveExpectedBoundOrigin();
        if (boundOrigin) {
          assert.equal(payload.expectedOrigin, boundOrigin, "expectedOrigin must match the bound official origin");
        }
        return withBackendLock(async () => {
          if (signal?.aborted) {
            throw signal.reason ?? new DOMException("The operation was aborted.", "AbortError");
          }
          if (failSaveOfficialFormError) throw failSaveOfficialFormError;
          if (saveOfficialFormOverride) return await saveOfficialFormOverride(payload, reqOptions);
          const recorded: SaveOfficialFormCall = {
            providerId: String(payload.providerId),
            expectedOrigin: String(payload.expectedOrigin),
            payload: structuredClone(payload.payload) as SaveOfficialFormCall["payload"],
            opening: structuredClone(payload.opening ?? {}) as SaveOfficialFormCall["opening"],
          };
          const { merged, result } = simulateBackendSaveOfficialForm(
            config,
            recorded.providerId,
            recorded.payload,
            recorded.opening,
          );
          recorded.mergedPayload = merged;
          saveCalls.push(recorded);
          return result;
        }, signal);
      }
      if (request && (action === "providers.updateModel" || action === "providers.updateModels")) {
        return withBackendLock(() => request(action, payload), signal);
      }
      return request?.(action, payload) ?? {};
    },
  };
  if (env.officialOrigin !== undefined) enhanceOptions.officialOrigin = env.officialOrigin;
  if (env.excludeOrigins !== undefined) enhanceOptions.excludeOrigins = env.excludeOrigins;
  if (env.readOfficialConfig !== null) {
    enhanceOptions.readOfficialConfig = env.readOfficialConfig ?? (() => officialConfig);
  }
  const enhance = context.createProviderEnhance(enhanceOptions) as {
    install(opts?: Record<string, unknown>): void;
    remove(): void;
    scan(): void;
    setEnabled(next: boolean): void;
  };

  function node(tag: string, className = "") {
    const element = doc.createElement(tag);
    element.className = className;
    return element;
  }
  const official = node("section", "ui-dialog");
  official.setAttribute("role", "dialog");
  const form = node("div", "pf");
  const field = node("div", "pf-field");
  const name = node("input", "ui-input");
  name.value = providerId;
  field.append(name);
  const table = node("div", "pmt");
  const heading = node("div", "pmt-grid pmt-head");
  heading.append("模型 ID", "上下文", "移除");
  table.append(heading);
  function addRow(modelID: string) {
    const row = node("div", "pmt-grid");
    const id = node("input");
    id.value = modelID;
    const ctxInput = node("input");
    ctxInput.value = "128000";
    row.append(id, ctxInput, node("button"));
    table.append(row);
    return { row, ctxInput };
  }
  const alpha = addRow("family/alpha");
  const beta = addRow("beta");
  const backBtn = node("button", "pf-back");
  backBtn.textContent = "返回";
  const saveBtn = node("button", "ui-btn ui-btn-primary");
  saveBtn.textContent = "保存";
  form.append(backBtn, field, table, saveBtn);
  official.append(form);
  doc.body.append(official);
  enhance.install(env.installReadOfficialConfig ? { readOfficialConfig: env.installReadOfficialConfig } : undefined);
  const setFetchResponder = (fn: typeof fetchResponder) => { fetchResponder = fn; };
  const setConfigGetError = (err: Error | null) => { failConfigGetError = err; };
  const setConfigGetOverride = (fn: (() => unknown) | null) => { configGetOverride = fn; };
  const setSaveOfficialFormError = (err: Error | null) => { failSaveOfficialFormError = err; };
  const setSaveOfficialFormOverride = (fn: typeof saveOfficialFormOverride) => { saveOfficialFormOverride = fn; };
  const setOfficialConfig = (next: Record<string, unknown> | null) => { officialConfig = next; };
  return {
    doc,
    window,
    context,
    baseFetch,
    fetchCalls,
    saveCalls,
    setFetchResponder,
    setConfigGetError,
    setConfigGetOverride,
    setSaveOfficialFormError,
    setSaveOfficialFormOverride,
    setOfficialConfig,
    request: enhanceOptions.request as (action: string, payload?: Record<string, unknown>) => Promise<unknown>,
    form,
    table,
    heading,
    alpha,
    beta,
    backBtn,
    saveBtn,
    nameInput: name,
    config,
    calls,
    enhance,
    addRow,
  };
}

const settle = () => new Promise(resolve => setTimeout(resolve, 5));
const inspector = (form: MiniNode) => form.querySelector(".ekd-px-drawer")!;

function allText(node: MiniNode): string {
  return node.textContent + node.children.map(allText).join("");
}

function findButton(root: MiniNode, label: string): MiniNode {
  const hit = root.querySelectorAll("button").find(button => allText(button).trim() === label);
  assert.ok(hit, `button ${label} not found`);
  return hit;
}

function key(window: EventTarget, key: string, shiftKey = false) {
  const event = new Event("keydown", { cancelable: true });
  Object.assign(event, { key, shiftKey });
  window.dispatchEvent(event);
  return event.defaultPrevented;
}

test("mounts inline row button, expands drawer in-place, toggles cleanly and guards unsaved edits on Escape", async () => {
  const { doc, window, table, heading, form, alpha, beta, enhance } = loadFixture();
  await settle();
  assert.equal(table.getAttribute("data-ekd-px-table"), "1");
  assert.ok(heading.querySelector(".ekd-px-head-pad"));
  assert.equal(heading.querySelector(".ekd-px-head-sel")?.hidden, true);
  assert.equal(alpha.row.getAttribute("data-ekd-px-row"), "1");
  const trigger = alpha.row.querySelector("[data-ekd-px-button]")!;
  assert.equal(allText(trigger).trim(), "能力与限制");
  assert.equal(trigger.getAttribute("data-ekd-px-summary"), "图片 · 工具 · 思考 · 其他 1 项 · 256K");
  const betaTrigger = beta.row.querySelector("[data-ekd-px-button]")!;
  assert.equal(betaTrigger.getAttribute("data-ekd-px-summary"), "未配置 · 64K");
  assert.equal(betaTrigger.getAttribute("aria-expanded"), "false");

  trigger.click();
  await settle();
  const panel = inspector(form);
  assert.ok(panel);
  assert.equal(panel.tagName, "DIV");
  assert.equal(trigger.getAttribute("aria-controls"), panel.getAttribute("id"));
  assert.equal(trigger.getAttribute("aria-expanded"), "true");
  assert.equal(panel.parentElement, alpha.row);
  assert.match(allText(panel), /audio_in/);
  const close = findButton(panel, "收起");
  assert.equal(doc.activeElement, close);

  // Clicking the same trigger again collapses a clean drawer.
  trigger.click();
  assert.equal(inspector(form), null);
  assert.equal(trigger.getAttribute("aria-expanded"), "false");

  // Re-open, edit output, and confirm Escape guards dirty changes.
  trigger.click();
  await settle();
  const reopened = inspector(form);
  const output = reopened.querySelectorAll("input.ekd-px-input")[1]!;
  output.value = "32K";
  output.fire("input");
  assert.equal(key(window, "Escape"), true);
  assert.equal(inspector(form), reopened);
  assert.match(allText(reopened), /有未保存的修改/);
  assert.equal(doc.activeElement, findButton(reopened, "放弃修改"));
  findButton(reopened, "放弃修改").click();
  assert.equal(inspector(form), null);
  assert.equal(doc.activeElement, trigger);
  assert.equal(trigger.getAttribute("aria-expanded"), "false");
  enhance.remove();
  assert.equal(alpha.row.querySelector("[data-ekd-px-button]"), null);
  assert.equal(table.getAttribute("data-ekd-px-table"), null);
});

test("validates output against saved context, supports presets and sends only edited capability fields", async () => {
  const { calls, form, alpha } = loadFixture();
  await settle();
  alpha.row.querySelector("[data-ekd-px-button]")!.click();
  await settle();
  const panel = inspector(form);
  const output = panel.querySelectorAll("input.ekd-px-input")[1]!;
  const adaptive = panel.querySelector('[data-kind="adaptive"]')!;
  assert.equal(adaptive.getAttribute("aria-pressed"), "true");
  assert.equal(panel.querySelector('[data-kind="mode"][data-mode="toggle"]')?.getAttribute("aria-pressed"), "true");
  assert.equal(panel.querySelectorAll('[data-kind="mode"]').length, 3);
  assert.doesNotMatch(allText(panel), /\bno\b/i);
  assert.equal(panel.querySelectorAll('[data-kind="effort"][data-effort="high"]').length, 1);
  const save = findButton(panel, "保存");
  output.value = "1M";
  output.fire("input");
  assert.equal(output.getAttribute("aria-invalid"), "true");
  assert.equal(save.disabled, true);
  findButton(panel, "不限制").click();
  assert.equal(output.value, "");
  assert.equal(output.getAttribute("aria-invalid"), "false");
  panel.querySelector('[data-kind="mode"][data-mode="always"]')!.click();
  assert.equal(adaptive.disabled, true);
  save.click();
  await settle();
  const write = calls.find(call => call.action === "providers.updateModel");
  assert.deepEqual(write?.payload, { providerId: "team", model: "team/family/alpha", edit: { thinking: "always" } });
  assert.equal(alpha.ctxInput.value, "128000");
  assert.match(allText(panel), /已保存并核对/);
});

test("saves both edited limits atomically and flags unsaved changes in the official row", async () => {
  const { calls, form, alpha } = loadFixture();
  await settle();
  const seenEvents: string[] = [];
  alpha.row.addEventListener("input", () => seenEvents.push("input"));
  alpha.row.addEventListener("change", () => seenEvents.push("change"));
  alpha.row.querySelector("[data-ekd-px-button]")!.click();
  await settle();
  const panel = inspector(form);
  assert.match(allText(panel), /官方表单有未保存的模型改动/);
  const [context, output] = panel.querySelectorAll("input.ekd-px-input");
  context!.value = "";
  context!.fire("input");
  assert.equal(context!.getAttribute("aria-invalid"), "true");
  context!.value = "64K";
  output!.value = "128K";
  output!.fire("input");
  assert.equal(output!.getAttribute("aria-invalid"), "true");
  context!.value = "384K";
  context!.fire("input");
  assert.equal(output!.getAttribute("aria-invalid"), "false");
  findButton(panel, "保存").click();
  await settle();
  assert.deepEqual(calls.find(call => call.action === "providers.updateModel")?.payload?.edit, {
    max_context_size: 393216,
    max_output_size: 131072,
  });
  assert.equal(alpha.ctxInput.value, "393216");
  assert.deepEqual(seenEvents, ["input", "change"]);
});

test("re-reads just-imported rows and renders managed rows read-only", async () => {
  const { form, config, enhance, addRow } = loadFixture();
  await settle();
  const fresh = addRow("just-imported");
  enhance.scan();
  await settle();
  const trigger = fresh.row.querySelector("[data-ekd-px-button]")!;
  assert.equal(trigger.getAttribute("data-ekd-px-summary"), "未保存");
  config.models["team/just-imported"] = { maxContextSize: 131072, capabilities: ["video_in"] };
  trigger.click();
  await settle();
  assert.ok(inspector(form));
  assert.equal(inspector(form).querySelector("input.ekd-px-input")?.value, "128K");
  assert.equal(trigger.getAttribute("data-ekd-px-summary"), "视频 · 128K");

  const managed = loadFixture(undefined, "managed:kimi-code");
  await settle();
  findButton(managed.alpha.row, "只读").click();
  await settle();
  const panel = inspector(managed.form);
  assert.match(allText(panel), /内置订阅供应商/);
  assert.equal(panel.querySelectorAll("input").every(input => input.disabled), true);
  assert.equal(panel.querySelectorAll(".ekd-px-pill").every(pill => pill.disabled), true);
  assert.equal(panel.querySelectorAll(".ekd-px-preset").every(preset => preset.disabled), true);
  assert.equal(managed.form.querySelector(".ekd-px-toolbar")?.hidden, true);
});

test("uses exact official and public suggestions only as a draft", async () => {
  const { calls, form, beta } = loadFixture(async (action, payload) => {
    switch (action) {
      case "models.list": return { items: [
        { id: "other/beta", provider: "other", model: "beta", capabilities: ["image_in"] },
        { id: "team/beta", provider: "team", model: "beta", capabilities: ["tool_use"] },
      ] };
      case "providers.modelSuggestion":
        assert.deepEqual(JSON.parse(JSON.stringify(payload)), { providerId: "team", model: "team/beta" });
        return { suggestion: { video_in: true, thinking: true, max_output_size: 8192 } };
      default: return {};
    }
  });
  await settle();
  beta.row.querySelector("[data-ekd-px-button]")!.click();
  await settle();
  const panel = inspector(form);
  findButton(panel, "填入目录建议").click();
  await settle();
  assert.equal(calls.some(call => call.action.startsWith("providers.update")), false);
  assert.match(allText(panel), /官方模型目录、models.dev/);
  findButton(panel, "保存").click();
  await settle();
  assert.deepEqual(calls.find(call => call.action === "providers.updateModel")?.payload?.edit, {
    capabilities: ["video_in", "tool_use"], thinking: "toggle", max_output_size: 8192,
  });
});

test("submits selected models once from the inline batch template and returns focus to the toolbar", async () => {
  const { doc, table, form, calls, alpha, beta } = loadFixture();
  await settle();
  const toolbar = form.querySelector(".ekd-px-toolbar")!;
  const toggle = findButton(toolbar, "批量配置");
  toggle.click();
  assert.equal(table.getAttribute("data-ekd-px-selecting"), "1");
  findButton(toolbar, "全选已保存模型").click();
  for (const item of [alpha, beta]) assert.equal(item.row.querySelector("input[type=checkbox]")?.checked, true);
  findButton(toolbar, "编辑能力模板").click();
  await settle();
  const panel = inspector(form);
  assert.equal(panel.getAttribute("data-ekd-px-batch"), "1");
  assert.equal(panel.parentElement, form);
  panel.querySelector('[data-kind="capability"][data-capability="image_in"]')!.click();
  findButton(panel, "保存到 2 个模型").click();
  await settle();
  assert.equal(inspector(form), null);
  assert.equal(table.getAttribute("data-ekd-px-selecting"), null);
  assert.equal(doc.activeElement, toggle);
  assert.deepEqual(calls.filter(call => call.action.startsWith("providers.update")), [{
    action: "providers.updateModels",
    payload: { providerId: "team", models: ["team/family/alpha", "team/beta"].map(model => ({ model, edit: { capabilities: ["image_in"], thinking: "none" } })) },
  }]);
});

test("theme rules restore inline drawer styling with host CSS tokens and no side modal dialog", async () => {
  const { doc } = loadFixture();
  const css = doc.head.querySelector("style")!.textContent;
  assert.doesNotMatch(css, /\.ekd-px-dialog/);
  assert.match(css, /\.pmt\[data-ekd-px-table="1"\]/);
  assert.match(css, /\.ekd-px-drawer\{/);
  assert.match(css, /\.ekd-px-pill\[aria-pressed="true"\]/);
  assert.match(css, /var\(--color-accent,/);
  assert.match(css, /var\(--color-surface,/);
  assert.match(css, /var\(--color-bg,/);
  assert.match(css, /var\(--color-text,/);
  assert.match(css, /var\(--color-success,/);
  assert.match(css, /var\(--color-danger,/);
});

function applyTestModelEdit(base: Model, edit: Record<string, unknown>): Model {
  const next: Model = structuredClone(base);
  const existingCaps = Array.isArray(next.capabilities) ? (next.capabilities as string[]) : [];
  if (Array.isArray(edit.capabilities) || typeof edit.thinking === "string") {
    const known = new Set(["image_in", "video_in", "tool_use", "thinking", "always_thinking"]);
    const custom = existingCaps.filter(cap => !known.has(cap));
    const nonThinking = Array.isArray(edit.capabilities)
      ? (edit.capabilities as string[]).filter(cap => cap !== "thinking" && cap !== "always_thinking")
      : existingCaps.filter(cap => known.has(cap) && cap !== "thinking" && cap !== "always_thinking");
    const thinking = typeof edit.thinking === "string"
      ? edit.thinking
      : existingCaps.includes("always_thinking")
        ? "always"
        : existingCaps.includes("thinking")
          ? "toggle"
          : "none";
    const merged = [...nonThinking];
    if (thinking === "toggle") merged.push("thinking");
    if (thinking === "always") merged.push("always_thinking");
    merged.push(...custom);
    next.capabilities = merged;
  }
  if (edit.thinking === "always" || edit.thinking === "none") {
    delete next.adaptiveThinking;
    delete next.adaptive_thinking;
  } else if (typeof edit.adaptive_thinking === "boolean") {
    next.adaptiveThinking = edit.adaptive_thinking;
    delete next.adaptive_thinking;
  }
  if (edit.thinking === "none") {
    delete next.supportEfforts;
    delete next.support_efforts;
  } else if (Array.isArray(edit.support_efforts)) {
    next.supportEfforts = [...(edit.support_efforts as string[])];
    delete next.support_efforts;
  }
  if (typeof edit.max_context_size === "number") {
    next.maxContextSize = edit.max_context_size;
    delete next.max_context_size;
  }
  if (Object.hasOwn(edit, "max_output_size")) {
    delete next.max_output_size;
    if (edit.max_output_size === null) delete next.maxOutputSize;
    else next.maxOutputSize = edit.max_output_size;
  }
  return next;
}

function applyOfficialPutToConfig(
  activeConfig: { providers: Record<string, Record<string, unknown>>; models: Record<string, Model> },
  providerId: string,
  body: { new_id?: string; models: Array<Record<string, unknown>> },
) {
  const targetId = typeof body.new_id === "string" && body.new_id.trim() ? body.new_id.trim() : providerId;
  if (targetId !== providerId) {
    activeConfig.providers[targetId] = activeConfig.providers[providerId] ?? { type: "openai", base_url: "https://example.com/v1" };
    delete activeConfig.providers[providerId];
  }
  for (const key of Object.keys(activeConfig.models)) {
    if (key.startsWith(`${providerId}/`) || key.startsWith(`${targetId}/`)) delete activeConfig.models[key];
  }
  for (const item of body.models) {
    const modelId = String(item.model);
    const next: Model = { maxContextSize: item.max_context_size };
    if (typeof item.display_name === "string" && item.display_name) next.displayName = item.display_name;
    if (typeof item.max_output_size === "number") next.maxOutputSize = item.max_output_size;
    if (Array.isArray(item.capabilities) && item.capabilities.length) next.capabilities = [...item.capabilities];
    if (Array.isArray(item.support_efforts) && item.support_efforts.length) next.supportEfforts = [...item.support_efforts];
    if (typeof item.adaptive_thinking === "boolean") next.adaptiveThinking = item.adaptive_thinking;
    if (typeof item.default_effort === "string" && item.default_effort) next.defaultEffort = item.default_effort;
    if (typeof item.off_effort === "string" && item.off_effort) next.offEffort = item.off_effort;
    activeConfig.models[`${targetId}/${modelId}`] = next;
  }
}

test("merges max_output_size and inspector capabilities atomically via providers.saveOfficialForm with zero secondary writes", async () => {
  let activeConfig: { providers: Record<string, Record<string, unknown>>; models: Record<string, Model> };
  const fixture = loadFixture(async (action, payload) => {
    if (action === "providers.updateModel" && payload) {
      const key = String(payload.model);
      activeConfig.models[key] = applyTestModelEdit(activeConfig.models[key]!, payload.edit as Record<string, unknown>);
      return { key, model: structuredClone(activeConfig.models[key]) };
    }
    return {};
  });
  activeConfig = fixture.config;
  const { context, form, alpha, calls, fetchCalls, saveCalls } = fixture;

  await settle();
  alpha.row.querySelector("[data-ekd-px-button]")!.click();
  await settle();
  const panel = inspector(form);
  panel.querySelector('[data-kind="capability"][data-capability="video_in"]')!.click(); // video_in ON
  panel.querySelector('[data-kind="capability"][data-capability="tool_use"]')!.click(); // tool_use OFF
  panel.querySelector('[data-kind="mode"][data-mode="always"]')!.click();
  const [contextInput, outputInput] = panel.querySelectorAll("input.ekd-px-input");
  contextInput!.value = "384K";
  contextInput!.fire("input");
  outputInput!.value = "128K";
  outputInput!.fire("input");
  findButton(panel, "保存").click();
  await settle();

  assert.equal(alpha.ctxInput.value, "393216");
  findButton(panel, "收起").click();

  // Official ProviderForm submits stale opening capabilities and omits max_output_size.
  const officialBody = {
    type: "openai",
    base_url: "https://example.com/v1",
    models: [
      {
        model: "family/alpha",
        max_context_size: Number(alpha.ctxInput.value),
        display_name: "Alpha Official",
        capabilities: ["audio_in", "image_in", "tool_use", "thinking"],
        support_efforts: ["no", "high", "high"],
        adaptive_thinking: true,
      },
      {
        model: "beta",
        max_context_size: 65536,
        display_name: "Beta Official",
      },
      {
        model: "gamma",
        max_context_size: 32768,
        display_name: "Brand New",
      },
    ],
  };

  const response = await context.fetch("http://127.0.0.1:54321/api/v1/providers/team", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(officialBody),
  });
  assert.equal(response.ok, true);
  assert.equal(response.status, 200);
  const envelope = await response.json() as { code: number; msg: string; data: { status: string; providerId: string } };
  assert.equal(envelope.code, 0);
  assert.equal(envelope.msg, "");
  assert.equal(envelope.data.providerId, "team");
  await settle();

  // Zero secondary providers.updateModels write-backs and zero direct baseFetch calls.
  assert.equal(calls.filter(call => call.action === "providers.updateModels").length, 0);
  assert.equal(fetchCalls.length, 0, "intercepted official PUT must route through providers.saveOfficialForm instead of baseFetch");
  assert.equal(saveCalls.length, 1);

  const sent = saveCalls[0]!.mergedPayload!;
  const sentAlpha = sent.models.find(item => item.model === "family/alpha")!;
  const sentBeta = sent.models.find(item => item.model === "beta")!;
  const sentGamma = sent.models.find(item => item.model === "gamma")!;

  assert.equal(sentAlpha.display_name, "Alpha Official");
  assert.equal(sentAlpha.max_context_size, 393216);
  assert.equal(sentAlpha.max_output_size, 131072);
  assert.deepEqual(sentAlpha.capabilities, ["image_in", "video_in", "always_thinking", "audio_in"]);
  assert.equal(Object.hasOwn(sentAlpha, "adaptive_thinking"), false, "cleared adaptive_thinking must be deleted from stale copy");
  assert.equal(sentBeta.display_name, "Beta Official");
  assert.equal(sentBeta.max_output_size, 1024);
  assert.equal(Object.hasOwn(sentGamma, "max_output_size"), false, "brand-new model must not be mutated");
  assert.equal(activeConfig.models["team/family/alpha"]?.maxOutputSize, 131072);
  assert.equal(activeConfig.models["team/beta"]?.maxOutputSize, 1024);
});

test("preserves external config edits and clearings with or without a subsequent official PUT", async () => {
  const fixture = loadFixture();
  const { window, context, alpha, saveBtn, backBtn, config, calls, fetchCalls, saveCalls } = fixture;
  await settle();

  // Clicking official save button followed by validation failure / cancel issues no PUT and mutates nothing.
  saveBtn.click();
  backBtn.click();

  // External entry (CLI / config.toml) clears beta.maxOutputSize, replaces alpha capabilities, clears adaptive/efforts, and sets default/off effort.
  config.models["team/beta"] = { maxContextSize: 65536 };
  config.models["team/family/alpha"] = {
    maxContextSize: 131072,
    capabilities: ["video_in"],
    defaultEffort: "medium",
    offEffort: "none",
  };
  window.dispatchEvent(new Event("ekd:official-config-changed"));
  await settle();

  assert.equal(calls.filter(call => call.action === "providers.updateModels").length, 0);
  assert.equal(config.models["team/beta"]?.maxOutputSize, undefined);
  assert.equal(alpha.row.querySelector("[data-ekd-px-button]")!.getAttribute("data-ekd-px-summary"), "视频 · 128K");

  // Later official PUT still carries the stale form-open copy; authoritative external changes and clearings must win.
  await context.fetch("http://127.0.0.1:54321/api/v1/providers/team", {
    method: "PUT",
    body: JSON.stringify({
      type: "openai",
      models: [
        {
          model: "family/alpha",
          max_context_size: 131072,
          display_name: "Alpha Kept",
          capabilities: ["audio_in", "image_in", "tool_use", "thinking"],
          support_efforts: ["no", "high", "high"],
          adaptive_thinking: true,
        },
        {
          model: "beta",
          max_context_size: 65536,
          display_name: "Beta Kept",
        },
      ],
    }),
  });
  assert.equal(fetchCalls.length, 0);
  const sent1 = saveCalls[0]!.mergedPayload!;
  const alpha1 = sent1.models.find(item => item.model === "family/alpha")!;
  const beta1 = sent1.models.find(item => item.model === "beta")!;
  assert.deepEqual(alpha1.capabilities, ["video_in"]);
  assert.equal(Object.hasOwn(alpha1, "adaptive_thinking"), false);
  assert.equal(Object.hasOwn(alpha1, "support_efforts"), false);
  assert.equal(alpha1.default_effort, "medium");
  assert.equal(alpha1.off_effort, "none");
  assert.equal(Object.hasOwn(beta1, "max_output_size"), false, "external clearing of maxOutputSize must never be resurrected");

  // External entry sets a new beta.maxOutputSize before another official PUT -> new external value is preserved.
  config.models["team/beta"] = { maxContextSize: 65536, maxOutputSize: 4096 };
  await context.fetch("http://127.0.0.1:54321/api/v1/providers/team", {
    method: "PUT",
    body: JSON.stringify({
      type: "openai",
      models: [{ model: "beta", max_context_size: 65536 }],
    }),
  });
  const sent2 = saveCalls[1]!.mergedPayload!;
  assert.equal(sent2.models[0]!.max_output_size, 4096);
});

test("omits oversized max_output_size when context shrinks and respects future official capability edits", async () => {
  const fixture = loadFixture(undefined, "team", {
    location: { href: "http://localhost:54321/settings/providers", origin: "http://localhost:54321", search: "" },
  });
  const { context, fetchCalls, saveCalls } = fixture;
  await settle();

  await context.fetch("http://localhost:54321/api/v1/providers/team", {
    method: "PUT",
    body: JSON.stringify({
      type: "openai",
      models: [
        {
          model: "beta",
          max_context_size: 512,
        },
        {
          model: "family/alpha",
          max_context_size: 262144,
          // Differs from opening snapshot -> official form explicitly changed capabilities.
          capabilities: ["image_in", "video_in"],
          support_efforts: ["low", "high"],
          adaptive_thinking: false,
        },
      ],
    }),
  });

  assert.equal(fetchCalls.length, 0);
  const sent = saveCalls[0]!.mergedPayload!;
  const beta = sent.models.find(item => item.model === "beta")!;
  const alpha = sent.models.find(item => item.model === "family/alpha")!;
  assert.equal(Object.hasOwn(beta, "max_output_size"), false, "must omit max_output_size exceeding new max_context_size");
  assert.deepEqual(alpha.capabilities, ["image_in", "video_in"], "must respect explicit official capability changes");
  assert.deepEqual(alpha.support_efforts, ["low", "high"]);
  assert.equal(alpha.adaptive_thinking, false);
});

test("serializes concurrent official PUT and inspector providers.updateModel clearing max_output_size without resurrecting cleared value", async () => {
  let releaseUpdateModel!: () => void;
  const updateEntered = new Promise<void>(resolve => { releaseUpdateModel = resolve; });
  let allowUpdateCommit!: () => void;
  const updateCommitGate = new Promise<void>(resolve => { allowUpdateCommit = resolve; });

  let activeConfig: { providers: Record<string, Record<string, unknown>>; models: Record<string, Model> };
  const fixture = loadFixture(async (action, payload) => {
    if (action === "providers.updateModel" && payload) {
      releaseUpdateModel();
      await updateCommitGate;
      const key = String(payload.model);
      activeConfig.models[key] = applyTestModelEdit(activeConfig.models[key]!, payload.edit as Record<string, unknown>);
      return { key, model: structuredClone(activeConfig.models[key]) };
    }
    return {};
  });
  activeConfig = fixture.config;
  const { context, request, fetchCalls, saveCalls } = fixture;
  await settle();
  assert.equal(activeConfig.models["team/beta"]?.max_output_size, 1024);

  // Start clearing beta.max_output_size in inspector while official PUT fires concurrently.
  const clearPromise = request("providers.updateModel", {
    providerId: "team",
    model: "team/beta",
    edit: { max_output_size: null },
  });
  await updateEntered;

  const putPromise = context.fetch("http://127.0.0.1:54321/api/v1/providers/team", {
    method: "PUT",
    body: JSON.stringify({
      type: "openai",
      models: [{ model: "beta", max_context_size: 65536, display_name: "Beta Concurrent" }],
    }),
  });

  // Allow the in-flight clear to finish under the shared backend mutex before saveOfficialForm runs.
  allowUpdateCommit();
  const [, putResponse] = await Promise.all([clearPromise, putPromise]);
  assert.equal(putResponse.ok, true);
  assert.equal(fetchCalls.length, 0);
  assert.equal(saveCalls.length, 1);
  assert.equal(Object.hasOwn(saveCalls[0]!.mergedPayload!.models[0]!, "max_output_size"), false, "cleared 1024 must never be resurrected by concurrent official PUT");
  assert.equal(activeConfig.models["team/beta"]?.maxOutputSize, undefined);
  assert.equal(activeConfig.models["team/beta"]?.max_output_size, undefined);
});

test("captures official Pinia snapshot synchronously at form open, distinguishes $J omitted fields from explicit edits, and never fabricates opening from async config.get", async () => {
  // 1. Pinia cache at form open has empty capabilities for beta while disk/backend already has ["video_in"].
  const piniaConfig: Record<string, unknown> = {
    providers: { team: { type: "openai", base_url: "https://example.com/v1" } },
    models: {
      "team/beta": { maxContextSize: 65536, capabilities: [] },
    },
  };
  const fixture = loadFixture(undefined, "team", {
    readOfficialConfig: () => piniaConfig,
  });
  fixture.config.models["team/beta"] = {
    maxContextSize: 65536,
    maxOutputSize: 1024,
    capabilities: ["video_in"],
    supportEfforts: ["high"],
    adaptiveThinking: true,
    defaultEffort: "high",
    offEffort: "low",
  };
  await settle();

  // Official $J omits empty capabilities/support_efforts/adaptive_thinking/default_effort/off_effort keys.
  const omittedRes = await fixture.context.fetch("http://127.0.0.1:54321/api/v1/providers/team", {
    method: "PUT",
    body: JSON.stringify({
      type: "openai",
      models: [{ model: "beta", max_context_size: 65536, display_name: "Beta Omitted" }],
    }),
  });
  assert.equal(omittedRes.ok, true);
  assert.deepEqual(fixture.saveCalls[0]!.opening.beta?.raw.capabilities, [], "opening snapshot must reflect Pinia at form open, not async config.get");
  const mergedOmitted = fixture.saveCalls[0]!.mergedPayload!.models[0]!;
  assert.deepEqual(mergedOmitted.capabilities, ["video_in"], "$J omitted capabilities must preserve stored video_in");
  assert.deepEqual(mergedOmitted.support_efforts, ["high"]);
  assert.equal(mergedOmitted.adaptive_thinking, true);
  assert.equal(mergedOmitted.default_effort, "high");
  assert.equal(mergedOmitted.off_effort, "low");
  assert.equal(mergedOmitted.max_output_size, 1024);

  // Explicit capabilities differing from opening snapshot are respected.
  const explicitRes = await fixture.context.fetch("http://127.0.0.1:54321/api/v1/providers/team", {
    method: "PUT",
    body: JSON.stringify({
      type: "openai",
      models: [{ model: "beta", max_context_size: 65536, capabilities: ["image_in", "video_in"] }],
    }),
  });
  assert.equal(explicitRes.ok, true);
  assert.deepEqual(fixture.saveCalls[1]!.mergedPayload!.models[0]!.capabilities, ["image_in", "video_in"]);
  fixture.enhance.remove();

  // 2. When readOfficialConfig is unavailable and snapshot === null on first form mount,
  //    async config.get MUST NOT be fabricated into opening; omitted capabilities still preserve stored values.
  const noPiniaFixture = loadFixture(undefined, "team", {
    readOfficialConfig: null,
  });
  noPiniaFixture.config.models["team/beta"] = {
    maxContextSize: 65536,
    capabilities: ["video_in"],
  };
  await settle();
  await noPiniaFixture.context.fetch("http://127.0.0.1:54321/api/v1/providers/team", {
    method: "PUT",
    body: JSON.stringify({
      type: "openai",
      models: [{ model: "beta", max_context_size: 65536 }],
    }),
  });
  assert.deepEqual(noPiniaFixture.saveCalls[0]!.opening, {}, "opening must remain empty when neither readOfficialConfig nor sync snapshot existed at form open");
  assert.deepEqual(noPiniaFixture.saveCalls[0]!.mergedPayload!.models[0]!.capabilities, ["video_in"]);
  noPiniaFixture.enhance.remove();

  // 3. install({ readOfficialConfig }) hook also captures Pinia synchronously and resets cleanly on remove().
  let readCalls = 0;
  const installHookFixture = loadFixture(undefined, "team", {
    readOfficialConfig: null,
    installReadOfficialConfig: () => {
      readCalls += 1;
      return {
        providers: { team: { type: "openai", base_url: "https://example.com/v1" } },
        models: { "team/beta": { maxContextSize: 65536, capabilities: ["image_in"] } },
      };
    },
  });
  await settle();
  assert.ok(readCalls >= 1);
  await installHookFixture.context.fetch("http://127.0.0.1:54321/api/v1/providers/team", {
    method: "PUT",
    body: JSON.stringify({
      type: "openai",
      models: [{ model: "beta", max_context_size: 65536, capabilities: ["image_in", "video_in"] }],
    }),
  });
  assert.deepEqual(installHookFixture.saveCalls[0]!.opening.beta?.raw.capabilities, ["image_in"]);
  assert.deepEqual(installHookFixture.saveCalls[0]!.mergedPayload!.models[0]!.capabilities, ["image_in", "video_in"]);
  installHookFixture.enhance.remove();
});

test("supports consecutive official PUTs, provider rename new_id, strict route/origin passthrough, fail-closed error envelope on saveOfficialForm error, and clean fetch restoration", async () => {
  const fixture = loadFixture();
  const {
    context,
    baseFetch,
    fetchCalls,
    saveCalls,
    setSaveOfficialFormError,
    setSaveOfficialFormOverride,
    config,
    calls,
    enhance,
  } = fixture;
  await settle();

  // 1. Official PUT #1 renames provider team -> team-v2 and adds gamma.
  const renameRes = await context.fetch("http://127.0.0.1:54321/api/v1/providers/team", {
    method: "PUT",
    body: JSON.stringify({
      type: "openai",
      new_id: "team-v2",
      models: [
        {
          model: "family/alpha",
          max_context_size: 262144,
          capabilities: ["audio_in", "image_in", "tool_use", "thinking"],
          support_efforts: ["no", "high", "high"],
          adaptive_thinking: true,
        },
        { model: "beta", max_context_size: 65536 },
        { model: "gamma", max_context_size: 32768 },
      ],
    }),
  });
  assert.equal(renameRes.ok, true);
  assert.equal(fetchCalls.length, 0);
  await settle();
  assert.equal(config.models["team-v2/beta"]?.maxOutputSize, 1024);

  // 2. Subsequent inspector/external edit to gamma and alpha on team-v2, followed by consecutive official PUT #2 without closing form.
  config.models["team-v2/gamma"] = {
    maxContextSize: 32768,
    maxOutputSize: 2048,
    capabilities: ["image_in", "thinking"],
  };
  config.models["team-v2/family/alpha"] = {
    maxContextSize: 262144,
    maxOutputSize: 16384,
    capabilities: ["audio_in", "image_in", "always_thinking"],
  };
  await context.fetch("http://127.0.0.1:54321/api/v1/providers/team-v2", {
    method: "PUT",
    body: JSON.stringify({
      type: "openai",
      models: [
        {
          model: "family/alpha",
          max_context_size: 262144,
          display_name: "Alpha Second Save",
          capabilities: ["audio_in", "image_in", "tool_use", "thinking"],
          support_efforts: ["no", "high", "high"],
          adaptive_thinking: true,
        },
        { model: "beta", max_context_size: 65536, display_name: "Beta Second Save" },
        { model: "gamma", max_context_size: 32768, display_name: "Gamma Second Save" },
      ],
    }),
  });
  const sentSecond = saveCalls[1]!.mergedPayload!;
  const gamma2 = sentSecond.models.find(item => item.model === "gamma")!;
  const alpha2 = sentSecond.models.find(item => item.model === "family/alpha")!;
  assert.equal(gamma2.max_output_size, 2048);
  assert.deepEqual(gamma2.capabilities, ["image_in", "thinking"]);
  assert.equal(alpha2.max_output_size, 16384);
  assert.deepEqual(alpha2.capabilities, ["audio_in", "image_in", "always_thinking"]);
  assert.equal(Object.hasOwn(alpha2, "adaptive_thinking"), false);

  // 3. External clearing of beta.maxOutputSize while providers.saveOfficialForm fails;
  //    MUST abort (never call baseFetch) and return a Daemon error envelope.
  await settle();
  config.models["team-v2/beta"] = { maxContextSize: 65536 };
  const fetchCountBeforeFail = fetchCalls.length;
  setSaveOfficialFormError(new Error("bridge busy"));
  const abortRes = await context.fetch("http://127.0.0.1:54321/api/v1/providers/team-v2", {
    method: "PUT",
    body: JSON.stringify({
      type: "openai",
      models: [{ model: "beta", max_context_size: 65536 }],
    }),
  });
  assert.equal(fetchCalls.length, fetchCountBeforeFail, "must not call baseFetch when providers.saveOfficialForm throws");
  assert.equal(abortRes.ok, false);
  assert.equal(abortRes.status, 503);
  assert.equal(abortRes.statusText, "Service Unavailable");
  assert.match(String(abortRes.headers.get("Content-Type")), /application\/json/i);
  const envelopeFromText = JSON.parse(await abortRes.clone().text()) as { code: number; msg: string; data: unknown };
  const envelopeFromJson = await abortRes.json() as { code: number; msg: string; data: unknown };
  assert.equal(envelopeFromText.code, 50300);
  assert.equal(envelopeFromText.data, null);
  assert.equal(envelopeFromText.msg, "bridge busy");
  assert.equal(envelopeFromJson.code, envelopeFromText.code);
  assert.equal(envelopeFromJson.msg, envelopeFromText.msg);
  assert.equal(envelopeFromJson.data, envelopeFromText.data);
  assert.equal(config.models["team-v2/beta"]?.maxOutputSize, undefined, "failed save must not resurrect cleared maxOutputSize");

  // Error carrying custom 4xx HTTP status and kapCode forwards both in the Daemon error envelope.
  setSaveOfficialFormError(Object.assign(new Error("provider conflict"), { status: 409, kapCode: 40901 }));
  const conflictRes = await context.fetch("http://127.0.0.1:54321/api/v1/providers/team-v2", {
    method: "PUT",
    body: JSON.stringify({
      type: "openai",
      models: [{ model: "beta", max_context_size: 65536 }],
    }),
  });
  assert.equal(fetchCalls.length, fetchCountBeforeFail);
  assert.equal(conflictRes.status, 409);
  const conflictBody = await conflictRes.json() as { code: number; msg: string; data: unknown };
  assert.equal(conflictBody.code, 40901);
  assert.equal(conflictBody.msg, "provider conflict");
  assert.equal(conflictBody.data, null);
  setSaveOfficialFormError(null);

  // Non-object saveOfficialForm response must also fail closed with 503 without calling baseFetch.
  setSaveOfficialFormOverride(() => null);
  const badResultRes = await context.fetch("http://127.0.0.1:54321/api/v1/providers/team-v2", {
    method: "PUT",
    body: JSON.stringify({
      type: "openai",
      models: [{ model: "beta", max_context_size: 65536 }],
    }),
  });
  assert.equal(fetchCalls.length, fetchCountBeforeFail, "must not call baseFetch when saveOfficialForm returns non-object");
  assert.equal(badResultRes.status, 503);
  assert.match((await badResultRes.json()).msg, /无法读取或合并最新增强模型配置/);
  setSaveOfficialFormOverride(null);

  // Once backend recovers, retrying the save succeeds atomically without resurrecting the cleared max_output_size.
  const saveCountBeforeRetry = saveCalls.length;
  const retryRes = await context.fetch("http://127.0.0.1:54321/api/v1/providers/team-v2", {
    method: "PUT",
    body: JSON.stringify({
      type: "openai",
      models: [{ model: "beta", max_context_size: 65536 }],
    }),
  });
  assert.equal(retryRes.ok, true);
  assert.equal(fetchCalls.length, fetchCountBeforeFail);
  assert.equal(saveCalls.length, saveCountBeforeRetry + 1);
  const sentRetry = saveCalls[saveCountBeforeRetry]!.mergedPayload!;
  assert.equal(Object.hasOwn(sentRetry.models[0]!, "max_output_size"), false, "retrying after recovery must not restore cleared max_output_size");
  assert.equal(config.models["team-v2/beta"]?.maxOutputSize, undefined);

  // 4. Non-matching routes, origins, proxy paths, and methods pass straight through without calling saveOfficialForm or config.get.
  await settle();
  config.models["team-v2/beta"] = { maxContextSize: 65536, maxOutputSize: 4096 };
  const rpcCallsBefore = calls.filter(call => call.action === "config.get" || call.action === "providers.saveOfficialForm").length;
  const rawPassBody = JSON.stringify({ models: [{ model: "beta", max_context_size: 65536 }] });
  const passthroughUrls: Array<[string, string]> = [
    ["PUT", "http://127.0.0.1:54321/providers/team-v2"],
    ["PUT", "http://127.0.0.1:59999/api/v1/providers/team-v2"],
    ["PUT", "http://localhost:54321/api/v1/providers/team-v2"],
    ["PUT", "http://127.0.0.1:54321/api/v1/hosts/h1/proxy/api/v1/providers/team-v2"],
    ["PUT", "https://api.example.com/api/v1/providers/team-v2"],
    ["PUT", "http://127.0.0.1:54321/api/v1/providers/team-v2/models"],
    ["PUT", "http://127.0.0.1:54321/api/v1/providers/managed%3Akimi-code"],
    ["POST", "http://127.0.0.1:54321/api/v1/providers/team-v2"],
  ];
  const callsBeforePassthrough = fetchCalls.length;
  for (const [method, url] of passthroughUrls) {
    await context.fetch(url, { method, body: rawPassBody });
  }
  assert.equal(
    calls.filter(call => call.action === "config.get" || call.action === "providers.saveOfficialForm").length,
    rpcCallsBefore,
    "non-matching requests must not invoke config.get or providers.saveOfficialForm",
  );
  assert.equal(fetchCalls.length, callsBeforePassthrough + passthroughUrls.length);
  for (let i = 0; i < passthroughUrls.length; i += 1) {
    assert.equal(fetchCalls[callsBeforePassthrough + i]!.init?.body, rawPassBody, `passthrough request ${passthroughUrls[i]![1]} must not be rewritten`);
  }

  // 5. setEnabled(false) and remove() cleanly restore original fetch.
  assert.notEqual(context.fetch, baseFetch);
  enhance.setEnabled(false);
  assert.equal(context.fetch, baseFetch);
  enhance.setEnabled(true);
  assert.notEqual(context.fetch, baseFetch);
  enhance.remove();
  assert.equal(context.fetch, baseFetch);
});

test("binds official origin via ?kimi_origin=, sessionStorage, location.href fallback, honors excludeOrigins, and uses global Response when available", async () => {
  // 1. ?kimi_origin= with trailing /v1/ takes precedence over location and strips /v1/.
  const queryFixture = loadFixture(undefined, "team", {
    location: {
      href: "http://127.0.0.1:5173/settings?kimi_origin=http%3A%2F%2F127.0.0.1%3A58627%2Fv1%2F",
      origin: "http://127.0.0.1:5173",
      search: "?kimi_origin=http%3A%2F%2F127.0.0.1%3A58627%2Fv1%2F",
    },
    provideResponse: true,
  });
  await settle();
  const rawBody = JSON.stringify({ type: "openai", models: [{ model: "beta", max_context_size: 65536 }] });
  // Matches kimi_origin port 58627 -> routes to saveOfficialForm and returns real global Response 200 envelope.
  const okRes = await queryFixture.context.fetch("http://127.0.0.1:58627/api/v1/providers/team", { method: "PUT", body: rawBody });
  assert.equal(okRes instanceof Response, true);
  assert.equal(okRes.status, 200);
  assert.equal((await okRes.json()).code, 0);
  assert.equal(queryFixture.saveCalls.length, 1);
  assert.equal(queryFixture.saveCalls[0]!.mergedPayload!.models[0]!.max_output_size, 1024);
  assert.equal(queryFixture.fetchCalls.length, 0);
  // Page's own port 5173 must NOT be intercepted when kimi_origin points to 58627.
  await queryFixture.context.fetch("http://127.0.0.1:5173/api/v1/providers/team", { method: "PUT", body: rawBody });
  assert.equal(queryFixture.fetchCalls.length, 1);
  assert.equal(queryFixture.fetchCalls[0]!.init?.body, rawBody);

  // Verify real global Response is returned on saveOfficialForm failure when available.
  queryFixture.setSaveOfficialFormError(new Error("down"));
  const real503 = await queryFixture.context.fetch("http://127.0.0.1:58627/api/v1/providers/team", { method: "PUT", body: rawBody });
  assert.equal(real503 instanceof Response, true);
  assert.equal(real503.status, 503);
  assert.equal((await real503.json()).code, 50300);
  queryFixture.enhance.remove();

  // 2. sessionStorage['kimi-desktop-server-origin'] works even on file:// renderer URL.
  const storageFixture = loadFixture(undefined, "team", {
    location: { href: "file:///Applications/Kimi%20Code.app/index.html", origin: "null", search: "" },
    sessionStorage: {
      getItem: key => (key === "kimi-desktop-server-origin" ? "http://127.0.0.1:58628" : null),
    },
  });
  await settle();
  await storageFixture.context.fetch("http://127.0.0.1:58628/api/v1/providers/team", { method: "PUT", body: rawBody });
  assert.equal(storageFixture.saveCalls.length, 1);
  assert.equal(storageFixture.saveCalls[0]!.mergedPayload!.models[0]!.max_output_size, 1024);
  assert.equal(storageFixture.fetchCalls.length, 0);
  await storageFixture.context.fetch("http://127.0.0.1:54321/api/v1/providers/team", { method: "PUT", body: rawBody });
  assert.equal(storageFixture.fetchCalls.length, 1);
  assert.equal(storageFixture.fetchCalls[0]!.init?.body, rawBody);
  storageFixture.enhance.remove();

  // 3. excludeOrigins prevents intercepting the bridge's own loopback port even if it matches.
  const excludedFixture = loadFixture(undefined, "team", {
    location: { href: "http://127.0.0.1:58628/settings", origin: "http://127.0.0.1:58628", search: "" },
    excludeOrigins: ["http://127.0.0.1:58628/ekd/native/v1"],
  });
  await settle();
  await excludedFixture.context.fetch("http://127.0.0.1:58628/api/v1/providers/team", { method: "PUT", body: rawBody });
  assert.equal(excludedFixture.saveCalls.length, 0);
  assert.equal(excludedFixture.fetchCalls[0]!.init?.body, rawBody);
  excludedFixture.enhance.remove();

  // 4. location.href fallback works when location.origin is missing, and opaque file:// + throwing sessionStorage passes through safely.
  const hrefOnlyFixture = loadFixture(undefined, "team", {
    location: { href: "http://127.0.0.1:58629/app/settings" },
    sessionStorage: () => { throw new Error("SecurityError"); },
  });
  await settle();
  await hrefOnlyFixture.context.fetch("http://127.0.0.1:58629/api/v1/providers/team", { method: "PUT", body: rawBody });
  assert.equal(hrefOnlyFixture.saveCalls.length, 1);
  assert.equal(hrefOnlyFixture.saveCalls[0]!.mergedPayload!.models[0]!.max_output_size, 1024);
  hrefOnlyFixture.enhance.remove();

  const opaqueFixture = loadFixture(undefined, "team", {
    location: { href: "file:///Applications/Kimi%20Code.app/index.html", origin: "null", search: "" },
    sessionStorage: () => { throw new Error("SecurityError"); },
  });
  await settle();
  await opaqueFixture.context.fetch("http://127.0.0.1:54321/api/v1/providers/team", { method: "PUT", body: rawBody });
  assert.equal(opaqueFixture.saveCalls.length, 0);
  assert.equal(opaqueFixture.fetchCalls[0]!.init?.body, rawBody);
  opaqueFixture.enhance.remove();
});

test("rejects origin drift between form open and submit with 409 and passes through reverse-proxied subpath origins", async () => {
  let currentOrigin = "http://127.0.0.1:54321";
  const fixture = loadFixture(undefined, "team", {
    officialOrigin: () => currentOrigin,
  });
  await settle();
  assert.equal(fixture.config.models["team/beta"]?.max_output_size, 1024);

  // Daemon origin drifts after the form was already opened -> MUST fail closed with 409 and 0 backend writes.
  currentOrigin = "http://127.0.0.1:59998";
  const driftRes = await fixture.context.fetch("http://127.0.0.1:59998/api/v1/providers/team", {
    method: "PUT",
    body: JSON.stringify({
      type: "openai",
      models: [{ model: "beta", max_context_size: 65536, display_name: "Drifted Save" }],
    }),
  });
  assert.equal(driftRes.ok, false);
  assert.equal(driftRes.status, 409);
  const driftBody = await driftRes.json() as { code: number; msg: string; data: unknown };
  assert.equal(driftBody.code, 40900);
  assert.equal(driftBody.msg, "当前窗口连接的官方服务实例已变化，请关闭表单后重新打开再保存。");
  assert.equal(driftBody.data, null);
  assert.equal(fixture.saveCalls.length, 0, "origin drift must produce 0 backend writes");
  assert.equal(fixture.fetchCalls.length, 0, "origin drift must not fall back to baseFetch");
  assert.equal(
    fixture.calls.filter(call => call.action === "providers.saveOfficialForm").length,
    0,
    "origin drift must abort before calling providers.saveOfficialForm",
  );
  fixture.enhance.remove();

  // Reverse-proxied subpath officialOrigin passes through to baseFetch without interception.
  const subpathFixture = loadFixture(undefined, "team", {
    officialOrigin: "http://127.0.0.1:54321/proxy",
  });
  await settle();
  const rawBody = JSON.stringify({ type: "openai", models: [{ model: "beta", max_context_size: 65536 }] });
  await subpathFixture.context.fetch("http://127.0.0.1:54321/proxy/api/v1/providers/team", {
    method: "PUT",
    body: rawBody,
  });
  assert.equal(subpathFixture.saveCalls.length, 0);
  assert.equal(subpathFixture.fetchCalls.length, 1);
  assert.equal(subpathFixture.fetchCalls[0]!.init?.body, rawBody);
  subpathFixture.enhance.remove();
});

test("honors Fetch AbortSignal end-to-end without calling backend on pre-aborted or queued aborts", async () => {
  const fixture = loadFixture();
  const { context, calls, fetchCalls, saveCalls, config, setSaveOfficialFormOverride, enhance } = fixture;
  await settle();

  const body1 = JSON.stringify({
    type: "openai",
    models: [{ model: "beta", max_context_size: 65536, display_name: "Save 1" }],
  });
  const body2 = JSON.stringify({
    type: "openai",
    models: [{ model: "beta", max_context_size: 131072, display_name: "Save 2 Aborted" }],
  });

  // (a) Pre-aborted signal rejects with AbortError and makes 0 backend/fetch calls.
  const rpcCountBefore = calls.filter(call => call.action === "providers.saveOfficialForm").length;
  await assert.rejects(
    () => context.fetch("http://127.0.0.1:54321/api/v1/providers/team", {
      method: "PUT",
      body: body1,
      signal: AbortSignal.abort(),
    }),
    (err: unknown) => (err as { name?: string })?.name === "AbortError",
  );
  assert.equal(
    calls.filter(call => call.action === "providers.saveOfficialForm").length,
    rpcCountBefore,
    "pre-aborted fetch must make 0 calls to providers.saveOfficialForm",
  );
  assert.equal(fetchCalls.length, 0);

  // (b) Save #1 holds backend mutex while Save #2 is queued with AbortController;
  //     aborting Save #2 while queued rejects Save #2 immediately and Save #2 never executes on backend.
  let notifySave1Entered!: () => void;
  const save1Entered = new Promise<void>(resolve => { notifySave1Entered = resolve; });
  let releaseSave1!: () => void;
  const save1Gate = new Promise<void>(resolve => { releaseSave1 = resolve; });
  const executedDisplayNames: string[] = [];

  setSaveOfficialFormOverride(async payload => {
    const body = payload.payload as { models: Array<Record<string, unknown>> };
    const name = String(body.models[0]?.display_name ?? "");
    executedDisplayNames.push(name);
    if (name === "Save 1") {
      notifySave1Entered();
      await save1Gate;
    }
    const recorded: SaveOfficialFormCall = {
      providerId: String(payload.providerId),
      expectedOrigin: String(payload.expectedOrigin),
      payload: structuredClone(payload.payload) as SaveOfficialFormCall["payload"],
      opening: structuredClone(payload.opening ?? {}) as SaveOfficialFormCall["opening"],
    };
    const { merged, result } = simulateBackendSaveOfficialForm(
      config,
      recorded.providerId,
      recorded.payload,
      recorded.opening,
    );
    recorded.mergedPayload = merged;
    saveCalls.push(recorded);
    return result;
  });

  const save1Promise = context.fetch("http://127.0.0.1:54321/api/v1/providers/team", {
    method: "PUT",
    body: body1,
  });
  await save1Entered;

  const controller2 = new AbortController();
  const save2Promise = context.fetch("http://127.0.0.1:54321/api/v1/providers/team", {
    method: "PUT",
    body: body2,
    signal: controller2.signal,
  });

  // Abort Save #2 while Save #1 still holds the backend mutex.
  controller2.abort();
  await assert.rejects(
    () => save2Promise,
    (err: unknown) => (err as { name?: string })?.name === "AbortError",
  );

  // Release Save #1 and verify Save #2 never executes on the backend even after Save #1 completes.
  releaseSave1();
  const save1Res = await save1Promise;
  assert.equal(save1Res.ok, true);
  assert.equal(save1Res.status, 200);
  await settle();

  assert.deepEqual(executedDisplayNames, ["Save 1"], "aborted queued Save #2 must never execute on backend");
  assert.equal(saveCalls.length, 1);
  assert.equal(config.models["team/beta"]?.maxContextSize, 65536);
  assert.equal(config.models["team/beta"]?.maxOutputSize, 1024);

  // (c) Request-like input carrying .signal + custom request ignoring signal still rejects immediately on abort.
  setSaveOfficialFormOverride(() => new Promise(() => {}));
  const controller3 = new AbortController();
  const pendingReqPromise = context.fetch(
    { url: "http://127.0.0.1:54321/api/v1/providers/team", method: "PUT", signal: controller3.signal },
    { body: body2 },
  );
  controller3.abort();
  await assert.rejects(
    () => pendingReqPromise,
    (err: unknown) => (err as { name?: string })?.name === "AbortError",
  );
  setSaveOfficialFormOverride(null);
  enhance.remove();
});

test("1.5.0 edit ProviderForm mounts without .pf-back and ignores hidden or AddProviderPage forms", async () => {
  const { doc, form, backBtn, alpha, enhance, calls } = loadFixture(undefined, "team", {
    officialOrigin: "http://127.0.0.1:54321",
  });
  await settle();
  // Remove 1.4.x .pf-back and replace with 1.5.0 .pf-brand.pf-head-sticky > .pf-nav
  backBtn.remove();
  const brand = doc.createElement("div");
  brand.className = "pf-brand pf-head-sticky";
  const nav = doc.createElement("button");
  nav.className = "pf-nav";
  brand.append(nav);
  form.children.unshift(brand);
  brand.parentElement = form;
  brand.isConnected = true;
  nav.isConnected = true;

  // Insert a hidden AddProviderPage (.ap) BEFORE the edit form with two .pf forms (v-show hidden + visible)
  const ap = doc.createElement("div");
  ap.className = "ap";
  const hiddenPf = doc.createElement("div");
  hiddenPf.className = "pf";
  hiddenPf.setAttribute("style", "display: none;");
  const hiddenTable = doc.createElement("div");
  hiddenTable.className = "pmt";
  hiddenPf.append(hiddenTable);
  const manualPf = doc.createElement("div");
  manualPf.className = "pf";
  const manualTable = doc.createElement("div");
  manualTable.className = "pmt";
  manualPf.append(manualTable);
  ap.append(hiddenPf, manualPf);
  form.before(ap);

  enhance.scan();
  await settle();
  assert.ok(form.querySelector(".ekd-px-toolbar"), "1.5.0 edit form must mount toolbar without .pf-back");
  assert.ok(alpha.row.querySelector("[data-ekd-px-button]"), "1.5.0 edit form must mount inline capability button");
  assert.equal(hiddenPf.querySelector(".ekd-px-toolbar"), null, "hidden v-show form must not be decorated");
  assert.equal(manualPf.querySelector(".ekd-px-toolbar"), null, "AddProviderPage manual form must not be decorated");

  // When only AddProviderPage is present (edit form removed), no edit decorations mount
  form.remove();
  enhance.scan();
  await settle();
  assert.equal(ap.querySelector("[data-ekd-px-mount]"), null, "AddProviderPage must never mount edit decorations");
  assert.ok(calls.some(call => call.action === "config.get"));
  enhance.remove();
});

test("toolbar catalog backfill repairs fetchModels defaults and single-model suggestion fills context", async () => {
  const { form, alpha, beta, addRow, calls, config, enhance } = loadFixture(async (action, payload) => {
    if (action === "providers.modelSuggestion") {
      if (Array.isArray(payload?.models)) {
        return {
          suggestions: {
            "family/alpha": {
              image_in: true,
              video_in: false,
              tool_use: true,
              thinking: true,
              max_context_size: 200000,
              max_output_size: 16384,
            },
            beta: {
              image_in: true,
              video_in: false,
              tool_use: true,
              thinking: false,
              max_context_size: 128000,
              max_output_size: 8192,
            },
          },
        };
      }
      return {
        suggestion: {
          image_in: true,
          thinking: true,
          max_context_size: 131072,
          max_output_size: 32768,
        },
      };
    }
    if (action === "models.list") return { items: [] };
    if (action === "providers.updateModels" && Array.isArray(payload?.models)) {
      for (const entry of payload.models as Array<{ model: string; edit: Record<string, unknown> }>) {
        if (config.models[entry.model]) {
          config.models[entry.model] = applyTestModelEdit(config.models[entry.model]!, entry.edit);
        }
      }
      return { provider: payload.providerId, models: payload.models };
    }
    return {};
  });
  // Simulate fetchModels hardcoded 256000 + ["tool_use", "thinking"] on beta
  config.models["team/beta"] = { maxContextSize: 256000, capabilities: ["tool_use", "thinking"], adaptiveThinking: true };
  addRow("unsaved-new");
  await settle();

  const toolbar = form.querySelector(".ekd-px-toolbar")!;
  const backfill = findButton(toolbar, "按目录补全");
  backfill.click();
  await settle();

  const suggestCall = calls.find(call => call.action === "providers.modelSuggestion");
  assert.deepEqual(suggestCall?.payload, {
    providerId: "team",
    models: ["team/family/alpha", "team/beta"],
  });
  const updateCall = calls.find(call => call.action === "providers.updateModels");
  assert.deepEqual(updateCall?.payload, {
    providerId: "team",
    models: [
      {
        model: "team/family/alpha",
        edit: { max_context_size: 200000, max_output_size: 16384 },
      },
      {
        model: "team/beta",
        edit: {
          capabilities: ["image_in", "tool_use"],
          thinking: "none",
          max_context_size: 128000,
          max_output_size: 8192,
        },
      },
    ],
  });
  assert.equal(alpha.ctxInput.value, "200000");
  assert.equal(beta.ctxInput.value, "128000");
  assert.match(allText(toolbar), /已按目录补全 2 个模型.*1 个未保存模型/);

  // Single-model suggestion also fills max_context_size, keeps decimal 128000 exact, and honors models.dev thinking: false
  beta.row.querySelector("[data-ekd-px-button]")!.click();
  await settle();
  const panel = inspector(form);
  const [ctxInput, outInput] = panel.querySelectorAll("input.ekd-px-input");
  assert.equal(ctxInput!.value, "128000", "decimal round context 128000 must stay 128000 instead of 125K");
  findButton(panel, "填入目录建议").click();
  await settle();
  assert.equal(ctxInput!.value, "128K");
  assert.equal(outInput!.value, "32K");
  enhance.remove();

  // Single-model suggestion must honor models.dev thinking: false even when models.list echoes stale saved thinking.
  const clearThinking = loadFixture(async action => {
    if (action === "models.list") {
      return { items: [{ id: "team/beta", provider: "team", model: "beta", capabilities: ["tool_use", "thinking"] }] };
    }
    if (action === "providers.modelSuggestion") {
      return { suggestion: { image_in: true, tool_use: true, thinking: false, max_context_size: 128000, max_output_size: 16384 } };
    }
    return {};
  });
  clearThinking.config.models["team/beta"] = { maxContextSize: 256000, capabilities: ["tool_use", "thinking"] };
  await settle();
  clearThinking.beta.row.querySelector("[data-ekd-px-button]")!.click();
  await settle();
  const clearPanel = inspector(clearThinking.form);
  assert.equal(clearPanel.querySelectorAll("input.ekd-px-input")[0]!.value, "256000");
  assert.equal(clearPanel.querySelector('[data-kind="mode"][data-mode="toggle"]')?.getAttribute("aria-pressed"), "true");
  findButton(clearPanel, "填入目录建议").click();
  await settle();
  assert.equal(clearPanel.querySelector('[data-kind="mode"][data-mode="none"]')?.getAttribute("aria-pressed"), "true");
  assert.equal(clearPanel.querySelectorAll("input.ekd-px-input")[0]!.value, "128000");
  assert.equal(clearPanel.querySelectorAll("input.ekd-px-input")[1]!.value, "16K");
  clearThinking.enhance.remove();
});

test("intercepts post-import_catalog follow-up PUT with preserveCatalogImport and empty opening", async () => {
  const { context, form, calls, setFetchResponder, setSaveOfficialFormOverride, enhance } = loadFixture(undefined, "team", {
    officialOrigin: "http://127.0.0.1:54321",
    provideResponse: true,
  });
  await settle();
  // Leave edit form to simulate AddProviderPage catalog import flow
  form.remove();
  enhance.scan();
  await settle();

  setFetchResponder(async (input, init) => {
    const url = typeof input === "string" ? input : String((input as { url?: string })?.url ?? "");
    if (init?.method === "POST" && url.endsWith("/api/v1/providers:import_catalog")) {
      return new Response(JSON.stringify({ code: 0, msg: "", data: { provider: { id: "deepseek" }, models_imported: 2 } }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    return new Response("{}", { status: 200 });
  });

  let capturedSavePayload: Record<string, unknown> | null = null;
  setSaveOfficialFormOverride(async payload => {
    capturedSavePayload = structuredClone(payload);
    return { data: { provider: { id: "deepseek" } }, mergedModels: {} };
  });

  const importRes = await context.fetch("http://127.0.0.1:54321/api/v1/providers:import_catalog", {
    method: "POST",
    body: JSON.stringify({ catalog_id: "deepseek", api_key: "sk-test" }),
  });
  assert.equal(importRes.ok, true);
  assert.equal((await importRes.json()).data.provider.id, "deepseek");

  // AddProviderPage immediately fires an overwriting PUT without base_url/max_output_size/support_efforts
  const putRes = await context.fetch("http://127.0.0.1:54321/api/v1/providers/deepseek", {
    method: "PUT",
    body: JSON.stringify({
      type: "openai",
      models: [{ model: "deepseek-reasoner", max_context_size: 131072, capabilities: ["tool_use", "thinking"], adaptive_thinking: true }],
    }),
  });
  assert.equal(putRes.ok, true);
  assert.ok(capturedSavePayload, "follow-up PUT must be routed through providers.saveOfficialForm");
  assert.equal(capturedSavePayload!["providerId"], "deepseek");
  assert.equal(capturedSavePayload!["preserveCatalogImport"], true);
  assert.deepEqual(capturedSavePayload!["opening"], {});
  assert.ok(calls.some(call => call.action === "providers.saveOfficialForm"));
  enhance.remove();
});
