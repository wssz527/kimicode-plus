import type { Disposable } from "./context.js";

/** Collects everything a plugin creates so one call releases all of it. */
export class Scope implements Disposable {
  private items: Array<() => void> = [];
  private disposed = false;

  add(cleanup: (() => void) | void | Disposable): void {
    if (cleanup === undefined || cleanup === null) return;
    const run: () => void =
      typeof cleanup === "function" ? cleanup : () => (cleanup as Disposable).dispose();
    if (this.disposed) {
      // A plugin that registers work after disposal must not leak it. It runs
      // immediately instead of being dropped, which is the only outcome that does
      // not leave a dangling listener behind.
      safeRun(run);
      return;
    }
    this.items.push(run);
  }

  get size(): number {
    return this.items.length;
  }

  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    const items = this.items;
    this.items = [];
    for (const run of items.reverse()) safeRun(run);
  }
}

/** One failing cleanup must not skip the rest of the scope. */
function safeRun(run: () => void): void {
  try {
    run();
  } catch (error) {
    // The plugin's own teardown error is deliberately swallowed: it cannot be
    // reported through the plugin's channels during teardown, and rethrowing
    // would strand the remaining resources.
    void error;
  }
}
