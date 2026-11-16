import type { BlockInfo, Cleanup, Disposable } from "./context.js";
import { Scope } from "./scope.js";

/**
 * Decorators run against host DOM that the plugin does not own. A single shared
 * observer serves every plugin: a plugin that installed its own document-wide
 * observer would multiply work with every installed plugin, and the contract
 * forbids it.
 *
 * The decorator targets are host selectors, pinned here instead of in each
 * plugin so a host DOM change is a one-line fix rather than a change in every
 * adapter plugin.
 */
export const DECORATION_TARGETS = {
  thinking: ".think",
  message: ".u-turn, .a-msg",
} as const;

export type DecorationTarget = keyof typeof DECORATION_TARGETS;

interface Registration {
  target: DecorationTarget;
  decorator: (block: HTMLElement, info: BlockInfo) => Cleanup | void;
  scope: Scope;
  decorated: WeakSet<HTMLElement>;
}

export class DecorationHub implements Disposable {
  private registrations = new Set<Registration>();
  private observer: MutationObserver | null = null;
  private root: Document | null = null;
  private scheduled = false;

  /** Compatibility check: without these nodes the host DOM is not the one the
   * adapters were written against, so nothing is decorated and the caller can
   * report an incompatible host instead of decorating the wrong elements. */
  static hostSupported(doc: Document): boolean {
    return Boolean(doc.querySelector(".chat, .chat-main, main"));
  }

  register(
    target: DecorationTarget,
    decorator: Registration["decorator"],
    scope: Scope,
  ): Disposable {
    const registration: Registration = { target, decorator, scope, decorated: new WeakSet() };
    this.registrations.add(registration);
    this.ensureObserving();
    this.sweep();
    let released = false;
    const release = () => {
      if (released) return;
      released = true;
      this.registrations.delete(registration);
      if (this.registrations.size === 0) this.stop();
    };
    scope.add(release);
    return { dispose: release };
  }

  /** Dispatches one batch. Exposed so a host that already re-renders on its own
   * schedule can trigger decoration without waiting for the microtask. */
  sweep(): void {
    if (this.registrations.size === 0) return;
    const doc = this.root;
    if (!doc) return;
    for (const registration of this.registrations) {
      let blocks: HTMLElement[] = [];
      try {
        blocks = Array.from(doc.querySelectorAll<HTMLElement>(DECORATION_TARGETS[registration.target]));
      } catch {
        continue;
      }
      for (const block of blocks) {
        if (registration.decorated.has(block)) continue;
        registration.decorated.add(block);
        const info: BlockInfo = {
          key: blockKey(block),
          role: block.classList.contains("a-msg") ? "assistant" : block.classList.contains("u-turn") ? "user" : undefined,
          slot: registration.target,
        };
        try {
          const cleanup = registration.decorator(block, info);
          if (cleanup) registration.scope.add(cleanup);
        } catch {
          // One plugin's decorator must not stop the rest of the batch, and the
          // failure is not reportable through a plugin channel that may itself be
          // the thing that broke.
        }
      }
    }
  }

  private ensureObserving(): void {
    if (this.observer || typeof document === "undefined") return;
    this.root = document;
    const Observer = globalThis.MutationObserver;
    if (typeof Observer !== "function") return;
    this.observer = new Observer(() => {
      if (this.scheduled) return;
      this.scheduled = true;
      // Decoration is coalesced to one sweep per frame: a host render can emit
      // hundreds of mutations and every one of them would otherwise walk the tree.
      queueMicrotask(() => {
        this.scheduled = false;
        this.sweep();
      });
    });
    this.observer.observe(document.body, { childList: true, subtree: true });
  }

  private stop(): void {
    this.observer?.disconnect();
    this.observer = null;
    this.root = null;
  }

  dispose(): void {
    this.stop();
    for (const registration of this.registrations) {
      registration.scope.dispose();
    }
    this.registrations.clear();
  }

  get count(): number {
    return this.registrations.size;
  }
}

function blockKey(block: HTMLElement): string {
  const explicit = block.getAttribute("data-message-key") ?? block.getAttribute("data-block-key");
  if (explicit) return explicit;
  const id = block.id;
  if (id) return id;
  const index = Array.from(block.parentElement?.children ?? []).indexOf(block);
  return `${block.className || block.tagName}:${index}`;
}
