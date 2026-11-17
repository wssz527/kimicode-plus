import type { BlockInfo, Cleanup, DecorationHandle, Disposable } from "./context.js";

/**
 * Decorators run against host DOM that the plugin does not own. A single shared
 * observer serves every plugin: a plugin that installed its own document-wide
 * observer would multiply work with every installed plugin, and the contract
 * forbids it.
 *
 * The decorator targets are host selectors, pinned here instead of in each
 * plugin so a host DOM change is a one-line fix rather than a change in every
 * adapter plugin. A target also declares how its open/streaming state is read,
 * because that state lives in host class names rather than in an attribute the
 * core can observe directly.
 */
export interface TargetSpec {
  selector: string;
  state(block: HTMLElement): { open: boolean; streaming: boolean };
  role?(block: HTMLElement): string | undefined;
}

export const DECORATION_TARGETS = {
  thinking: {
    selector: ".think",
    state(block: HTMLElement) {
      const body = block.querySelector(".think-body");
      const open = Boolean(body && body.classList.contains("open"));
      const streaming =
        block.classList.contains("streaming") || block.getAttribute("data-streaming") === "true";
      return { open, streaming };
    },
  } satisfies TargetSpec,
  message: {
    selector: ".u-turn, .a-msg",
    state() {
      return { open: true, streaming: false };
    },
    role(block: HTMLElement) {
      if (block.classList.contains("a-msg")) return "assistant";
      if (block.classList.contains("u-turn")) return "user";
      return undefined;
    },
  } satisfies TargetSpec,
} as const;

export type DecorationTarget = keyof typeof DECORATION_TARGETS;

/** The slice of the DOM the hub uses, so tests can drive it without a browser. */
export interface DecorationDocument {
  body: unknown;
  querySelectorAll(selector: string): ArrayLike<HTMLElement>;
}

export interface DecorationObserver {
  observe(target: unknown, options: MutationObserverInit): void;
  disconnect(): void;
}

export interface DecorationHubOptions {
  document?: DecorationDocument | null;
  createObserver?: (callback: (records: DecorationMutation[]) => void) => DecorationObserver | null;
  /** Coalescing strategy; the default is one microtask per batch. */
  schedule?: (flush: () => void) => void;
}

/** The subset of MutationRecord the hub reads. */
export interface DecorationMutation {
  type: string;
  target: Node;
  addedNodes?: ArrayLike<Node>;
  removedNodes?: ArrayLike<Node>;
  attributeName?: string | null;
}

interface Registration {
  target: DecorationTarget;
  decorator: (block: HTMLElement, info: BlockInfo) => Cleanup | DecorationHandle | void;
  onError?: (error: unknown) => void;
  blocks: Map<HTMLElement, BlockState>;
}

/**
 * One decorated block. The closure belongs to this element alone: when the block
 * is removed or replaced, only this entry is released. v1 attached the cleanup to
 * the plugin scope and remembered decorated elements in a WeakSet, so a removed
 * block leaked its listeners until the plugin was disabled and a re-registered
 * plugin could never decorate an element it had already seen.
 */
interface BlockState {
  info: BlockInfo;
  revision: number;
  release: () => void;
  onUpdate?: (info: BlockInfo) => void;
}

export class DecorationHub implements Disposable {
  private registrations = new Set<Registration>();
  private observer: DecorationObserver | null = null;
  private doc: DecorationDocument | null = null;
  private scheduled = false;
  private readonly options: DecorationHubOptions;
  private readonly schedule: (flush: () => void) => void;

  constructor(options: DecorationHubOptions = {}) {
    this.options = options;
    this.schedule =
      options.schedule ??
      (flush => {
        if (typeof queueMicrotask === "function") queueMicrotask(flush);
        else setTimeout(flush, 0);
      });
  }

  /** Compatibility check: without these nodes the host DOM is not the one the
   * adapters were written against, so nothing is decorated and the caller can
   * report an incompatible host instead of decorating the wrong elements. */
  static hostSupported(doc: Pick<Document, "querySelector">): boolean {
    return Boolean(doc.querySelector(".chat, .chat-main, main"));
  }

  register(
    target: DecorationTarget,
    decorator: Registration["decorator"],
    scope: { add(cleanup: Cleanup | void): void },
    onError?: (error: unknown) => void,
  ): Disposable {
    const registration: Registration = { target, decorator, onError, blocks: new Map() };
    this.registrations.add(registration);
    this.ensureObserving();
    this.sweep();
    let released = false;
    const release = () => {
      if (released) return;
      released = true;
      this.registrations.delete(registration);
      for (const block of [...registration.blocks.keys()]) this.releaseBlock(registration, block);
      if (this.registrations.size === 0) this.stop();
    };
    scope.add(release);
    return { dispose: release };
  }

  /** Dispatches one batch. Exposed so a host that already re-renders on its own
   * schedule can trigger decoration without waiting for the microtask. */
  sweep(): void {
    if (this.registrations.size === 0) return;
    const doc = this.doc;
    if (!doc) return;
    for (const registration of this.registrations) {
      const spec = DECORATION_TARGETS[registration.target];
      let blocks: HTMLElement[] = [];
      try {
        blocks = Array.from(doc.querySelectorAll(spec.selector));
      } catch (error) {
        registration.onError?.(error);
        continue;
      }
      const live = new Set(blocks);
      // A block that left the document (or stopped matching) takes only its own
      // closure with it. Waiting for the plugin to be disabled is what leaked.
      for (const block of [...registration.blocks.keys()]) {
        if (!live.has(block) || !block.isConnected) this.releaseBlock(registration, block);
      }
      for (const block of blocks) {
        const existing = registration.blocks.get(block);
        if (!existing) {
          this.decorate(registration, block, spec);
          continue;
        }
        const state = spec.state(block);
        const info: BlockInfo = {
          ...existing.info,
          open: state.open,
          streaming: state.streaming,
          revision: existing.revision,
        };
        if (!existing.onUpdate) {
          existing.info = info;
          continue;
        }
        existing.info = info;
        try {
          existing.onUpdate(info);
        } catch (error) {
          registration.onError?.(error);
        }
      }
    }
  }

  private decorate(registration: Registration, block: HTMLElement, spec: TargetSpec): void {
    const state = spec.state(block);
    const info: BlockInfo = {
      key: blockKey(block),
      role: spec.role?.(block),
      slot: registration.target,
      open: state.open,
      streaming: state.streaming,
      revision: 0,
    };
    let result: Cleanup | DecorationHandle | void;
    try {
      result = registration.decorator(block, info);
    } catch (error) {
      // One plugin's decorator must not stop the rest of the batch, and the
      // failure is not reportable through a plugin channel that may itself be
      // the thing that broke.
      registration.onError?.(error);
      return;
    }
    const onUpdate =
      result && typeof result === "object" && typeof (result as DecorationHandle).update === "function"
        ? (result as DecorationHandle).update?.bind(result)
        : undefined;
    const release = cleanupRunner(result);
    registration.blocks.set(block, { info, revision: 0, release, onUpdate });
  }

  private releaseBlock(registration: Registration, block: HTMLElement): void {
    const state = registration.blocks.get(block);
    if (!state) return;
    registration.blocks.delete(block);
    try {
      state.release();
    } catch (error) {
      registration.onError?.(error);
    }
  }

  private ensureObserving(): void {
    if (this.observer || this.registrations.size === 0) return;
    const doc =
      this.options.document ??
      (typeof document === "undefined" ? null : (document as unknown as DecorationDocument));
    if (!doc) return;
    this.doc = doc;
    const create =
      this.options.createObserver ??
      ((callback: (records: DecorationMutation[]) => void) => {
        const Observer = globalThis.MutationObserver;
        if (typeof Observer !== "function") return null;
        return new Observer(records => callback(records as unknown as DecorationMutation[]));
      });
    const observer = create(records => this.handleMutations(records));
    if (!observer) return;
    this.observer = observer;
    observer.observe(doc.body, {
      childList: true,
      subtree: true,
      characterData: true,
      // `.think-body.open` and `.think.streaming` are the two state bits the
      // thinking target needs; observing every attribute would wake the hub for
      // unrelated host churn (style, aria, data-* written by other panels).
      attributes: true,
      attributeFilter: ["class", "data-streaming"],
    });
  }

  /**
   * Mutations are translated into block-level dirty marks, and one coalesced
   * sweep follows. Two rules keep the shared observer honest:
   *
   * - mutations inside a node the decorator added (`data-ekdp-owned`) are the
   *   plugin's own rendering, not host content. Counting them would let a plugin
   *   that re-renders on update feed itself.
   * - a removed block releases its closure immediately instead of waiting for
   *   the plugin to stop.
   */
  private handleMutations(records: DecorationMutation[]): void {
    if (this.registrations.size === 0) return;
    let dirty = false;
    for (const record of records) {
      const target = record.target;
      const element = elementOf(target);
      if (!element) continue;
      if (isOwned(element)) continue;
      if (record.type === "childList") {
        // A decorator renders into a host container it does not own — appending
        // its translation node to the block body is a childList change whose
        // target is that host node. Counting it as host content would make the
        // plugin invalidate its own result on every render, so a record made up
        // entirely of the plugin's own nodes is not host churn.
        if (onlyOwnedNodes(record)) continue;
        if (!dirty) {
          const addedNodes = record.addedNodes;
          for (let index = 0; addedNodes && index < addedNodes.length; index += 1) {
            const node = addedNodes[index];
            const added = elementOf(node);
            if (!added) continue;
            for (const registration of this.registrations) {
              const selector = DECORATION_TARGETS[registration.target].selector;
              if (closestMatch(added, selector) || added.querySelector?.(selector)) {
                dirty = true;
                break;
              }
            }
            if (dirty) break;
          }
        }
        const removed = record.removedNodes;
        if (removed) {
          for (let index = 0; index < removed.length; index += 1) {
            const node = removed[index];
            if (node && this.releaseInside(node)) dirty = true;
          }
        }
      }
      for (const registration of this.registrations) {
        const spec = DECORATION_TARGETS[registration.target];
        const block = closestMatch(element, spec.selector);
        if (!block) continue;
        const state = registration.blocks.get(block);
        if (state) {
          state.revision += 1;
          state.info = { ...state.info, revision: state.revision };
        }
        dirty = true;
      }
    }
    if (dirty) this.requestSweep();
  }

  /** Releases every decorated block inside a removed subtree. */
  private releaseInside(node: Node): boolean {
    let changed = false;
    const root = elementOf(node);
    for (const registration of this.registrations) {
      for (const block of [...registration.blocks.keys()]) {
        if (block === root || (root && root.contains(block)) || !block.isConnected) {
          this.releaseBlock(registration, block);
          changed = true;
        }
      }
    }
    return changed;
  }

  private requestSweep(): void {
    if (this.scheduled) return;
    this.scheduled = true;
    this.schedule(() => {
      this.scheduled = false;
      this.sweep();
    });
  }

  private stop(): void {
    this.observer?.disconnect();
    this.observer = null;
    this.doc = null;
  }

  dispose(): void {
    for (const registration of [...this.registrations]) {
      for (const block of [...registration.blocks.keys()]) this.releaseBlock(registration, block);
    }
    this.registrations.clear();
    this.stop();
  }

  get count(): number {
    return this.registrations.size;
  }

  /** Diagnostics: how many blocks are decorated right now, per plugin set. */
  get decoratedBlocks(): number {
    let total = 0;
    for (const registration of this.registrations) total += registration.blocks.size;
    return total;
  }
}

function cleanupRunner(result: Cleanup | DecorationHandle | void): () => void {
  if (typeof result === "function") return result;
  if (result && typeof result.dispose === "function") return () => result.dispose();
  return () => {};
}

function elementOf(node: Node | null | undefined): HTMLElement | null {
  if (!node) return null;
  if (node.nodeType === 1) return node as HTMLElement;
  return (node.parentElement as HTMLElement | null) ?? null;
}

function isOwned(element: HTMLElement): boolean {
  let current: Element | null = element;
  while (current) {
    if (current.hasAttribute?.("data-ekdp-owned")) return true;
    current = current.parentElement;
  }
  return false;
}

/**
 * True when every added and removed node in a childList record belongs to the
 * decorator. The record's target is the host node the plugin rendered into, so
 * the target alone cannot tell host churn from the plugin's own output.
 */
function onlyOwnedNodes(record: DecorationMutation): boolean {
  let seen = 0;
  for (const list of [record.addedNodes, record.removedNodes]) {
    if (!list) continue;
    for (let index = 0; index < list.length; index += 1) {
      const node = elementOf(list[index]);
      if (!node) continue;
      seen += 1;
      if (!isOwned(node)) return false;
    }
  }
  return seen > 0;
}

function closestMatch(element: HTMLElement, selector: string): HTMLElement | null {
  return (element.closest?.(selector) as HTMLElement | null) ?? (element.matches?.(selector) ? element : null);
}

function blockKey(block: HTMLElement): string {
  const explicit = block.getAttribute("data-message-key") ?? block.getAttribute("data-block-key");
  if (explicit) return explicit;
  const id = block.id;
  if (id) return id;
  const index = Array.from(block.parentElement?.children ?? []).indexOf(block);
  return `${block.className || block.tagName}:${index}`;
}
