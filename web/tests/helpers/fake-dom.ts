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
  parentElement: FakeElement | null = null;
  children: FakeElement[] = [];
  isConnected = false;
  private classes = new Set<string>();
  private attributes = new Map<string, string>();

  constructor(tagName = "div", className = "") {
    this.tagName = tagName.toUpperCase();
    this.className = className;
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
    };
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
      node.parentElement = this;
      this.children.push(node);
      node.setConnected(this.isConnected);
    }
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
    return splitSelector(selector).some(part => matchesCompound(this, part));
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

export class FakeDocument {
  body = new FakeElement("body");
  documentElement = new FakeElement("html");

  constructor() {
    this.body.setConnected(true);
  }

  createElement(tag: string): FakeElement {
    return new FakeElement(tag);
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

function matchesCompound(element: FakeElement, part: string): boolean {
  if (/[\s>+~]/.test(part)) throw new Error(`unsupported selector: ${part}`);
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
