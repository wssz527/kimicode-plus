/**
 * A small DOM stand-in for the decoration tests. The plugin runtime touches a
 * narrow slice of the DOM — querySelectorAll, closest, matches, classList,
 * attributes, contains and parent links — so a real browser is not required and
 * a node:test file can drive block removal, streaming and re-registration
 * deterministically.
 *
 * Selectors are limited to what the runtime and the tests actually use: a comma
 * separated list of compound selectors (`tag`, `.class`, `tag.class`,
 * `[attr]`), without combinators. Anything else throws, so a test cannot
 * silently pass against a selector this engine does not understand.
 */

export class FakeElement {
  readonly nodeType = 1;
  tagName: string;
  id = "";
  textContent = "";
  innerHTML = "";
  hidden = false;
  tabIndex = 0;
  ownerDocument: FakeDocument | null = null;
  parentElement: FakeElement | null = null;
  children: FakeElement[] = [];
  isConnected = false;
  style: Record<string, string> & { removeProperty: (name: string) => void };
  private classes = new Set<string>();
  private attributes = new Map<string, string>();
  private listeners = new Map<string, Array<(event: FakeEvent) => void>>();

  constructor(tagName = "div", className = "") {
    this.tagName = tagName.toUpperCase();
    this.className = className;
    const styleMap: Record<string, string> = {};
    this.style = Object.assign(styleMap, {
      removeProperty: (name: string) => {
        const camel = name.replace(/-([a-z])/g, (_, letter: string) => letter.toUpperCase());
        delete styleMap[name];
        delete styleMap[camel];
      },
    }) as Record<string, string> & { removeProperty: (name: string) => void };
  }

  get childNodes(): FakeElement[] {
    return this.children;
  }

  get className(): string {
    return [...this.classes].join(" ");
  }

  set className(value: string) {
    this.classes = new Set(value.split(/\s+/).filter(Boolean));
  }

  get classList() {
    const classes = this.classes;
    return {
      contains: (name: string) => classes.has(name),
      add: (name: string) => {
        classes.add(name);
      },
      remove: (name: string) => {
        classes.delete(name);
      },
      toggle: (name: string, force?: boolean) => {
        const next = force ?? !classes.has(name);
        if (next) classes.add(name);
        else classes.delete(name);
        return next;
      },
      get length() {
        return classes.size;
      },
      item: (index: number) => [...classes][index] ?? null,
      [Symbol.iterator]: () => classes[Symbol.iterator](),
    };
  }

  get dataset(): Record<string, string> {
    const element = this;
    return new Proxy({} as Record<string, string>, {
      get: (_target, key) => element.getAttribute(`data-${kebab(String(key))}`) ?? "",
      set: (_target, key, value) => {
        element.setAttribute(`data-${kebab(String(key))}`, String(value));
        return true;
      },
      deleteProperty: (_target, key) => {
        element.removeAttribute(`data-${kebab(String(key))}`);
        return true;
      },
    });
  }

  get firstElementChild(): FakeElement | null {
    return this.children[0] ?? null;
  }

  get lastElementChild(): FakeElement | null {
    return this.children[this.children.length - 1] ?? null;
  }

  get previousElementSibling(): FakeElement | null {
    const parent = this.parentElement;
    if (!parent) return null;
    const index = parent.children.indexOf(this);
    return index > 0 ? parent.children[index - 1]! : null;
  }

  get nextElementSibling(): FakeElement | null {
    const parent = this.parentElement;
    if (!parent) return null;
    const index = parent.children.indexOf(this);
    return index >= 0 && index < parent.children.length - 1 ? parent.children[index + 1]! : null;
  }

  getAttribute(name: string): string | null {
    return this.attributes.get(name) ?? null;
  }

  setAttribute(name: string, value: string): void {
    this.attributes.set(name, value);
  }

  removeAttribute(name: string): void {
    this.attributes.delete(name);
  }

  hasAttribute(name: string): boolean {
    return this.attributes.has(name);
  }

  append(...nodes: FakeElement[]): void {
    for (const node of nodes) {
      if (node.parentElement) {
        const index = node.parentElement.children.indexOf(node);
        if (index >= 0) node.parentElement.children.splice(index, 1);
      }
      node.parentElement = this;
      node.ownerDocument = this.ownerDocument;
      this.children.push(node);
      node.setConnected(this.isConnected);
    }
  }

  replaceChildren(...nodes: FakeElement[]): void {
    for (const child of [...this.children]) child.remove();
    this.append(...nodes);
  }

  insertAdjacentElement(position: "afterend" | "beforebegin" | "afterbegin" | "beforeend", element: FakeElement): FakeElement | null {
    const parent = this.parentElement;
    if (!parent) return null;
    const index = parent.children.indexOf(this);
    if (position === "afterend") parent.children.splice(index + 1, 0, element);
    else if (position === "beforebegin") parent.children.splice(index, 0, element);
    else if (position === "afterbegin") this.children.unshift(element);
    else this.children.push(element);
    element.parentElement = position === "afterbegin" || position === "beforeend" ? this : parent;
    element.ownerDocument = this.ownerDocument;
    element.setConnected((element.parentElement ?? this).isConnected);
    return element;
  }

  cloneNode(deep = false): FakeElement {
    const clone = new FakeElement(this.tagName);
    for (const name of this.classes) clone.classList.add(name);
    for (const [name, value] of this.attributes) clone.setAttribute(name, value);
    clone.textContent = this.textContent;
    clone.innerHTML = this.innerHTML;
    clone.ownerDocument = this.ownerDocument;
    if (deep) for (const child of this.children) clone.append(child.cloneNode(true));
    return clone;
  }

  addEventListener(type: string, listener: (event: FakeEvent) => void): void {
    const list = this.listeners.get(type) ?? [];
    list.push(listener);
    this.listeners.set(type, list);
  }

  removeEventListener(type: string, listener: (event: FakeEvent) => void): void {
    const list = this.listeners.get(type);
    if (!list) return;
    const index = list.indexOf(listener);
    if (index >= 0) list.splice(index, 1);
    if (list.length === 0) this.listeners.delete(type);
  }

  listenerCount(type = "click"): number {
    return this.listeners.get(type)?.length ?? 0;
  }

  dispatchEvent(event: FakeEvent): boolean {
    for (const listener of [...(this.listeners.get(event.type) ?? [])]) {
      if (!event.target) event.target = this;
      listener(event);
    }
    return true;
  }

  click(): void {
    this.dispatchEvent({ type: "click", target: this, preventDefault: () => {}, stopPropagation: () => {} });
  }

  setConnected(value: boolean): void {
    this.isConnected = value;
    for (const child of this.children) child.setConnected(value);
  }

  remove(): void {
    const parent = this.parentElement;
    if (!parent) return;
    const index = parent.children.indexOf(this);
    if (index >= 0) parent.children.splice(index, 1);
    this.parentElement = null;
    this.setConnected(false);
  }

  contains(other: FakeElement | null): boolean {
    if (!other) return false;
    let current: FakeElement | null = other;
    while (current) {
      if (current === this) return true;
      current = current.parentElement;
    }
    return false;
  }

  closest(selector: string): FakeElement | null {
    let current: FakeElement | null = this;
    while (current) {
      if (current.matches(selector)) return current;
      current = current.parentElement;
    }
    return null;
  }

  matches(selector: string): boolean {
    return splitSelector(selector).some(part => matchesChain(this, part));
  }

  querySelectorAll(selector: string): FakeElement[] {
    const out: FakeElement[] = [];
    const visit = (node: FakeElement) => {
      for (const child of node.children) {
        if (child.matches(selector)) out.push(child);
        visit(child);
      }
    };
    visit(this);
    return out;
  }

  querySelector(selector: string): FakeElement | null {
    return this.querySelectorAll(selector)[0] ?? null;
  }
}

export interface FakeEvent {
  type: string;
  target?: FakeElement;
  preventDefault: () => void;
  stopPropagation: () => void;
}

export class FakeDocument {
  body = new FakeElement("body");
  documentElement = new FakeElement("html");

  constructor() {
    this.body.ownerDocument = this;
    this.body.setConnected(true);
  }

  createElement(tag: string): FakeElement {
    const element = new FakeElement(tag);
    element.ownerDocument = this;
    return element;
  }

  createElementNS(_namespace: string, tag: string): FakeElement {
    return this.createElement(tag);
  }

  querySelector(selector: string): FakeElement | null {
    return this.body.querySelector(selector) ?? (this.body.matches(selector) ? this.body : null);
  }

  querySelectorAll(selector: string): FakeElement[] {
    return this.body.querySelectorAll(selector);
  }
}

export interface FakeMutation {
  type: string;
  target: FakeElement;
  addedNodes: FakeElement[];
  removedNodes: FakeElement[];
  attributeName: string | null;
}

/** A MutationObserver double: tests decide exactly which batch the hub sees. */
export class FakeObserver {
  static live: FakeObserver[] = [];
  records: MutationRecord[] = [];
  callback: (records: MutationRecord[]) => void;
  observed: Array<{ target: unknown; options: MutationObserverInit }> = [];
  disconnected = false;

  constructor(callback: (records: MutationRecord[]) => void) {
    this.callback = callback;
    FakeObserver.live.push(this);
  }

  observe(target: unknown, options: MutationObserverInit): void {
    this.observed.push({ target, options });
  }

  disconnect(): void {
    this.disconnected = true;
    FakeObserver.live = FakeObserver.live.filter(item => item !== this);
  }

  emit(records: Partial<FakeMutation>[]): void {
    this.callback(
      records.map(
        record =>
          ({
            type: record.type ?? "childList",
            target: record.target ?? ({} as FakeElement),
            addedNodes: record.addedNodes ?? [],
            removedNodes: record.removedNodes ?? [],
            attributeName: record.attributeName ?? null,
          }) as unknown as MutationRecord,
      ),
    );
  }
}

function splitSelector(selector: string): string[] {
  const parts = selector.split(",").map(part => part.trim()).filter(Boolean);
  if (parts.length === 0) throw new Error(`empty selector: ${selector}`);
  return parts;
}

function kebab(name: string): string {
  return name.replace(/[A-Z]/g, letter => "-" + letter.toLowerCase());
}

function matchesChain(element: FakeElement, part: string): boolean {
  const compounds = part.split(/\s+/).filter(Boolean);
  if (compounds.length === 0) throw new Error(`unsupported selector: ${part}`);
  const last = compounds[compounds.length - 1]!;
  if (!matchesCompound(element, last)) return false;
  let current: FakeElement | null = element;
  for (let index = compounds.length - 2; index >= 0; index -= 1) {
    const ancestorSelector = compounds[index]!;
    current = current.parentElement;
    while (current && !matchesCompound(current, ancestorSelector)) current = current.parentElement;
    if (!current) return false;
  }
  return true;
}

function matchesCompound(element: FakeElement, part: string): boolean {
  if (/[>+~]/.test(part)) throw new Error(`unsupported selector: ${part}`);
  const tokens = part.match(/^[a-zA-Z][a-zA-Z0-9-]*|\.[A-Za-z0-9_-]+|\[[A-Za-z0-9_-]+\]|#[A-Za-z0-9_-]+/g) ?? [];
  if (!tokens.length) throw new Error(`unsupported selector: ${part}`);
  for (const token of tokens) {
    if (token.startsWith(".")) {
      if (!element.classList.contains(token.slice(1))) return false;
    } else if (token.startsWith("#")) {
      if (element.id !== token.slice(1)) return false;
    } else if (token.startsWith("[")) {
      if (!element.hasAttribute(token.slice(1, -1))) return false;
    } else if (element.tagName !== token.toUpperCase()) {
      return false;
    }
  }
  return true;
}
