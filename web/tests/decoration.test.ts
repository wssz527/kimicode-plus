import test from "node:test";
import assert from "node:assert/strict";
import { DecorationHub, type DecorationMutation, type DecorationObserver } from "../src/plugin-runtime/decoration.js";
import type { BlockInfo, DecorationHandle } from "../src/plugin-runtime/context.js";
import { Scope } from "../src/plugin-runtime/scope.js";
import { FakeDocument, FakeElement, type FakeMutation } from "./helpers/fake-dom.js";

interface Harness {
  doc: FakeDocument;
  hub: DecorationHub;
  observer: FakeObserverBox;
  flush(): void;
  think(text?: string, options?: { open?: boolean; streaming?: boolean; id?: string }): FakeElement;
}

/** Owns the fake MutationObserver so every test can emit an exact batch. */
class FakeObserverBox implements DecorationObserver {
  callback: ((records: DecorationMutation[]) => void) | null = null;
  observed = false;
  disconnected = false;
  targets = new Set<unknown>();
  constructor(callback: (records: DecorationMutation[]) => void) {
    this.callback = callback;
  }
  observe(target: unknown): void {
    this.observed = true;
    this.targets.add(target);
  }
  disconnect(): void {
    this.disconnected = true;
    this.targets.clear();
  }
  emit(records: Partial<FakeMutation>[]): void {
    this.callback?.(
      records.map(record => ({
        type: record.type ?? "childList",
        target: record.target ?? ({} as FakeElement),
        addedNodes: record.addedNodes ?? [],
        removedNodes: record.removedNodes ?? [],
        attributeName: record.attributeName ?? null,
      })) as unknown as DecorationMutation[],
    );
  }
}

function harness(): Harness {
  const doc = new FakeDocument();
  const pending: Array<() => void> = [];
  let observer: FakeObserverBox | null = null;
  const hub = new DecorationHub({
    document: doc as unknown as { body: unknown; querySelectorAll(selector: string): ArrayLike<HTMLElement> },
    createObserver: callback => {
      observer = new FakeObserverBox(callback);
      return observer;
    },
    schedule: flush => pending.push(flush),
  });
  const think = (text = "First paragraph.", options: { open?: boolean; streaming?: boolean; id?: string } = {}) => {
    const block = new FakeElement("div", `think${options.streaming ? " streaming" : ""}`);
    if (options.id) block.id = options.id;
    const head = new FakeElement("button", "think-head");
    const body = new FakeElement("div", `think-body${options.open === false ? "" : " open"}`);
    const inner = new FakeElement("div", "think-body-inner");
    const pre = new FakeElement("pre", "think-text");
    pre.textContent = text;
    pre.childNodes.push(Object.assign(new FakeElement("#TEXT"), { nodeValue: text }) as unknown as FakeElement);
    inner.append(pre);
    body.append(inner);
    block.append(head, body);
    doc.body.append(block);
    return block;
  };
  const flush = () => {
    while (pending.length) {
      const next = pending.shift();
      next?.();
    }
  };
  return {
    doc,
    hub,
    get observer() {
      return observer as unknown as FakeObserverBox;
    },
    flush,
    think,
  };
}

test("every registered plugin receives only blocks of its own target", () => {
  const h = harness();
  const seen: Array<{ plugin: string; key: string; slot: string }> = [];
  h.think("a", { id: "t1" });
  h.doc.body.append(Object.assign(new FakeElement("div", "u-turn"), { id: "m1" }));
  const scope = new Scope();
  h.hub.register("thinking", (_block, info) => {
    seen.push({ plugin: "one", key: info.key, slot: info.slot });
  }, scope);
  h.hub.register("message", (_block, info) => {
    seen.push({ plugin: "two", key: info.key, slot: info.slot });
  }, scope);
  assert.deepEqual(seen, [
    { plugin: "one", key: "t1", slot: "thinking" },
    { plugin: "two", key: "m1", slot: "message" },
  ]);
  h.hub.dispose();
});

test("a newly inserted block is discovered from its parent's mutation", () => {
  const h = harness();
  let runs = 0;
  h.hub.register("thinking", () => {
    runs += 1;
  }, new Scope());
  const inserted = h.think("added", { id: "inserted" });
  h.observer.emit([{ type: "childList", target: h.doc.body, addedNodes: [inserted] }]);
  h.flush();
  assert.equal(runs, 1, "the added block was not discovered from its parent's mutation");
  h.hub.dispose();
});

test("a second plugin still decorates a block the first plugin already saw", () => {
  const h = harness();
  h.think("a", { id: "t1" });
  const first = new Scope();
  const second = new Scope();
  let firstCount = 0;
  let secondCount = 0;
  h.hub.register("thinking", () => {
    firstCount += 1;
  }, first);
  // v1 kept a WeakSet per registration, so this would have stayed at zero only
  // if the set had been global; the failure it caused was the reverse: a block
  // decorated before a plugin re-registered was never decorated again.
  h.hub.register("thinking", () => {
    secondCount += 1;
  }, second);
  assert.equal(firstCount, 1);
  assert.equal(secondCount, 1, "a newly registered plugin did not decorate an existing block");
  // Releasing and registering again must decorate the same element again.
  first.dispose();
  let thirdCount = 0;
  h.hub.register("thinking", () => {
    thirdCount += 1;
  }, new Scope());
  assert.equal(thirdCount, 1, "a re-registered plugin did not re-decorate an existing block");
  h.hub.dispose();
});

test("a streaming block reports its state and the update channel, not a re-run", () => {
  const h = harness();
  const block = h.think("partial", { id: "t1", streaming: true });
  const scope = new Scope();
  const infos: BlockInfo[] = [];
  let runs = 0;
  let updates = 0;
  h.hub.register("thinking", (_block, info) => {
    runs += 1;
    infos.push(info);
    const handle: DecorationHandle = {
      dispose: () => {},
      update: next => {
        updates += 1;
        infos.push(next);
      },
    };
    return handle;
  }, scope);
  assert.equal(runs, 1);
  assert.equal(infos[0]?.streaming, true);
  assert.equal(infos[0]?.open, true);
  assert.equal(infos[0]?.revision, 0);

  // The host appends text and stops streaming: one dirty batch, one update.
  block.classList.remove("streaming");
  const pre = block.querySelector("pre") as FakeElement;
  pre.textContent = "partial and final";
  h.observer.emit([{ type: "childList", target: pre }]);
  h.flush();
  assert.equal(runs, 1, "the decorator was re-run instead of updated");
  assert.equal(updates, 1);
  assert.equal(infos.at(-1)?.streaming, false);
  assert.equal(infos.at(-1)?.revision, 1);
  h.hub.dispose();
});

test("only one sweep runs for a burst of host mutations", () => {
  const h = harness();
  const block = h.think("a", { id: "t1" });
  const scope = new Scope();
  let runs = 0;
  h.hub.register("thinking", () => {
    runs += 1;
  }, scope);
  const pre = block.querySelector("pre") as FakeElement;
  for (let index = 0; index < 20; index += 1) {
    h.observer.emit([{ type: "childList", target: pre }]);
  }
  h.flush();
  // The first sweep decorated the block; the 20 dirty marks coalesced into one
  // later sweep that found it already decorated.
  assert.equal(runs, 1);
  h.hub.dispose();
});

test("removing one block releases only that block's closure", () => {
  const h = harness();
  const keep = h.think("keep", { id: "keep" });
  const gone = h.think("gone", { id: "gone" });
  const scope = new Scope();
  const released: string[] = [];
  h.hub.register("thinking", (block, info) => () => released.push(info.key), scope);
  assert.equal(h.hub.decoratedBlocks, 2);

  // The host removes the second block without touching the first.
  const container = gone.parentElement!;
  const index = container.children.indexOf(gone);
  container.children.splice(index, 1);
  gone.setConnected(false);
  gone.parentElement = null;
  h.observer.emit([{ type: "childList", target: container, removedNodes: [gone] }]);
  h.flush();

  assert.deepEqual(released, ["gone"], "removing a block released the wrong closure");
  assert.ok(keep.isConnected);
  assert.equal(h.hub.decoratedBlocks, 1);
  h.hub.dispose();
});

test("a block replaced by a new element decorates the replacement, not the old node", () => {
  const h = harness();
  const first = h.think("first", { id: "old" });
  const scope = new Scope();
  const keys: string[] = [];
  const released: string[] = [];
  h.hub.register("thinking", (_block, info) => {
    keys.push(info.key);
    return () => released.push(info.key);
  }, scope);

  const container = first.parentElement!;
  const replacement = new FakeElement("div", "think");
  replacement.id = "new";
  const head = new FakeElement("button", "think-head");
  const body = new FakeElement("div", "think-body open");
  body.append(new FakeElement("div", "think-body-inner"));
  replacement.append(head, body);
  container.children.splice(container.children.indexOf(first), 1, replacement);
  first.setConnected(false);
  first.parentElement = null;
  replacement.parentElement = container;
  replacement.setConnected(true);
  h.observer.emit([{ type: "childList", target: container, removedNodes: [first], addedNodes: [replacement] }]);
  h.flush();

  assert.deepEqual(released, ["old"]);
  assert.deepEqual(keys, ["old", "new"]);
  h.hub.dispose();
});

test("plugin-added nodes marked owned do not feed the decorator", () => {
  const h = harness();
  const block = h.think("a", { id: "t1" });
  const scope = new Scope();
  let updates = 0;
  h.hub.register("thinking", () => ({
    dispose: () => {},
    update: () => {
      updates += 1;
    },
  }), scope);
  const toolbar = new FakeElement("div", "ekdp-thought-tools");
  toolbar.setAttribute("data-ekdp-owned", "");
  block.append(toolbar);
  const label = new FakeElement("span");
  toolbar.append(label);
  h.observer.emit([{ type: "childList", target: toolbar }]);
  h.observer.emit([{ type: "childList", target: label }]);
  h.flush();
  assert.equal(updates, 0, "the plugin's own nodes woke the decorator");
  h.hub.dispose();
});

test("a decorator's own node appended into a host block does not wake it", () => {
  const h = harness();
  const block = h.think("a", { id: "t1" });
  const scope = new Scope();
  let updates = 0;
  h.hub.register("thinking", () => {
    return {
      dispose: () => {},
      update: () => {
        updates += 1;
      },
    };
  }, scope);
  h.flush();
  // The decorator renders into the host block body, so the childList record's
  // target is a host node while the added node is the decorator's own. Counting
  // that as host churn made the plugin invalidate the result it had just drawn.
  const body = block.children[1]!;
  const translated = new FakeElement("pre", "adapter-translated");
  translated.setAttribute("data-ekdp-owned", "");
  body.append(translated);
  h.observer.emit([{ type: "childList", target: body, addedNodes: [translated] }]);
  h.flush();
  assert.equal(updates, 0, "the decorator's own render was reported as host churn");
  h.hub.dispose();
});

test("host content added next to a decorator's node still wakes it", () => {
  const h = harness();
  const block = h.think("a", { id: "t1" });
  const scope = new Scope();
  let updates = 0;
  h.hub.register("thinking", () => ({
    dispose: () => {},
    update: () => {
      updates += 1;
    },
  }), scope);
  h.flush();
  const body = block.children[1]!;
  const own = new FakeElement("pre", "adapter-translated");
  own.setAttribute("data-ekdp-owned", "");
  body.append(own);
  // A batch that also carries a host node must not be discarded because one of
  // its records was the decorator's own.
  const host = new FakeElement("span", "host-appended");
  host.textContent = "host text";
  body.append(host);
  h.observer.emit([{ type: "childList", target: body, addedNodes: [host] }]);
  h.flush();
  assert.equal(updates, 1, "host content was ignored because a sibling record was owned");
  h.hub.dispose();
});

test("a decorator that throws is isolated and does not decorate the next block", () => {
  const h = harness();
  h.think("a", { id: "broken" });
  h.think("b", { id: "healthy" });
  const scope = new Scope();
  const failures: unknown[] = [];
  const decorated: string[] = [];
  h.hub.register("thinking", (_block, info) => {
    if (info.key === "broken") throw new Error("decorator exploded");
    decorated.push(info.key);
  }, scope, error => failures.push(error));
  assert.equal(failures.length, 1);
  assert.deepEqual(decorated, ["healthy"]);
  // The hub stays usable: a later sweep with a new block works.
  h.think("c", { id: "later" });
  h.hub.sweep();
  assert.deepEqual(decorated, ["healthy", "later"]);
  h.hub.dispose();
});

test("disabling a plugin restores every block it decorated", () => {
  const h = harness();
  h.think("a", { id: "t1" });
  h.think("b", { id: "t2" });
  const scope = new Scope();
  const released: string[] = [];
  h.hub.register("thinking", (_block, info) => {
    return () => released.push(info.key);
  }, scope);
  assert.equal(h.hub.decoratedBlocks, 2);
  scope.dispose();
  assert.deepEqual(released.sort(), ["t1", "t2"]);
  assert.equal(h.hub.decoratedBlocks, 0);
  assert.equal(h.hub.count, 0);
  assert.equal(h.observer.disconnected, true, "the shared observer outlived its last plugin");
  h.hub.dispose();
});

test("one plugin's disposal leaves the other plugin's blocks decorated", () => {
  const h = harness();
  h.think("a", { id: "t1" });
  const one = new Scope();
  const two = new Scope();
  const released: string[] = [];
  h.hub.register("thinking", (block, info) => () => released.push(`one:${info.key}`), one);
  h.hub.register("thinking", (block, info) => () => released.push(`two:${info.key}`), two);
  one.dispose();
  assert.deepEqual(released, ["one:t1"]);
  assert.equal(h.hub.count, 1);
  assert.equal(h.hub.decoratedBlocks, 1);
  assert.equal(h.observer.disconnected, false);
  h.hub.dispose();
  assert.deepEqual(released.sort(), ["one:t1", "two:t1"]);
});

test("a collapsed thinking block reports closed and no decoration is lost", () => {
  const h = harness();
  h.think("a", { id: "t1", open: false });
  const scope = new Scope();
  let info: BlockInfo | null = null;
  h.hub.register("thinking", (_block, next) => {
    info = next;
  }, scope);
  assert.equal(info!.open, false);
  h.hub.dispose();
});

test("an unrelated DOM change does not re-dispatch an unchanged block", () => {
  const h = harness();
  const block = h.think("stable text", { id: "stable" });
  const scope = new Scope();
  let updates = 0;
  h.hub.register("thinking", () => ({
    dispose: () => {},
    update: () => {
      updates += 1;
    },
  }), scope);
  // A sibling message renders and re-renders: the sweep visits the thinking
  // block again, but nothing about it changed.
  const sibling = h.think("other block", { id: "sibling" });
  h.observer.emit([{ type: "childList", target: sibling }]);
  h.flush();
  assert.equal(updates, 0, "an unchanged block was re-triggered by unrelated mutations");

  // A real text change still dispatches exactly one update. The text lives in a
  // child text node, which is how a browser renders `pre.textContent = ...`.
  const pre = block.querySelector("pre") as FakeElement;
  pre.textContent = "stable text and more";
  pre.childNodes[0] = Object.assign(new FakeElement("#TEXT"), { nodeValue: "stable text and more" }) as unknown as FakeElement;
  h.observer.emit([{ type: "childList", target: pre }]);
  h.flush();
  assert.equal(updates, 1, "a real text change was suppressed");

  // Opening the block is a state change and dispatches too.
  const body = block.querySelector(".think-body") as FakeElement;
  body.classList.remove("open");
  h.observer.emit([{ type: "attributes", target: body, attributeName: "class" }]);
  h.flush();
  assert.equal(updates, 2, "an open-state change was suppressed");
  h.hub.dispose();
});


test("enhanced thinking opt-in follows open shadow roots and releases removed blocks", () => {
  const h = harness();
  const native = h.think("native", { id: "native" });
  const regular: string[] = [];
  h.hub.register("thinking", (_block, info) => { regular.push(info.key); }, new Scope());
  const enhanced: BlockInfo[] = [];
  const disposed: string[] = [];
  const scope = new Scope();
  h.hub.register("thinking", (_block, info) => {
    enhanced.push(info);
    return { update: next => enhanced.push(next), dispose: () => disposed.push(info.key) };
  }, scope, undefined, true);

  const host = new FakeElement("div");
  const root = new FakeElement("div");
  const block = new FakeElement("details");
  const body = new FakeElement("div");
  block.id = "enhanced";
  block.setAttribute("data-ekdp-thinking", "");
  block.setAttribute("data-streaming", "");
  body.setAttribute("data-ekdp-thinking-source", "First line");
  block.append(body);
  root.append(block);
  root.setConnected(true);
  Object.assign(host, { shadowRoot: root });
  host.setAttribute("data-ekdp-thinking-root", "");
  h.doc.body.append(host);
  h.observer.emit([{ target: h.doc.body, addedNodes: [host] }]);
  h.flush();
  assert.deepEqual(regular, [native.id]);
  assert.ok(h.observer.targets.has(root));
  assert.deepEqual(enhanced.map(info => [info.key, info.open, info.streaming]), [[native.id, true, false], [block.id, false, true]]);

  block.setAttribute("open", "");
  block.removeAttribute("data-streaming");
  body.setAttribute("data-ekdp-thinking-source", "Final **line**");
  h.observer.emit([{ type: "attributes", target: block, attributeName: "open" }, { type: "attributes", target: body, attributeName: "data-ekdp-thinking-source" }]);
  h.flush();
  assert.deepEqual([enhanced.at(-1)?.open, enhanced.at(-1)?.streaming], [true, false]);

  host.remove();
  root.setConnected(false);
  h.observer.emit([{ target: h.doc.body, removedNodes: [host] }]);
  h.flush();
  assert.deepEqual(disposed, [block.id]);
  assert.equal(h.observer.targets.has(root), false);
  h.hub.dispose();
});
