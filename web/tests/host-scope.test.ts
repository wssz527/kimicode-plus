import test from "node:test";
import assert from "node:assert/strict";
import * as native from "../src/native.js";
import { createEventChannel, createNativeHost, type EventChannel, type NativeTransport } from "../src/native.js";
import { hostConnection, installHost, releaseHost } from "../src/host.js";

function transportWith(handler: (action: string, payload?: Record<string, unknown>) => Promise<unknown>) {
  const calls: { action: string; payload?: Record<string, unknown> }[] = [];
  const transport: NativeTransport = {
    request: async <T = unknown>(action: string, payload?: Record<string, unknown>): Promise<T> => {
      calls.push({ action, payload });
      return (await handler(action, payload)) as T;
    },
  };
  return { transport, calls };
}

test("an unmounting panel no longer detaches the panels that are still open", () => {
  const settings = { back: () => {} };
  const planner = { back: () => {} };
  installHost(settings);
  installHost(planner);
  // The settings tab closes first; the planner panel must keep its connection.
  releaseHost(settings);
  assert.equal(hostConnection(), planner);
  // A stale unmount for an older connection is a no-op rather than a wipe.
  releaseHost(settings);
  assert.equal(hostConnection(), planner);
  releaseHost(planner);
  assert.equal(hostConnection(), null);
});

test("the module no longer exposes a mutable host singleton", () => {
  const surface = native as unknown as Record<string, unknown>;
  for (const name of ["getDemoHost", "getNativeHost", "installDemoHost", "installNativeHost", "demoRequest"]) {
    assert.equal(surface[name], undefined, `${name} must be gone`);
  }
  assert.equal(typeof native.useHost, "function");
  assert.equal(typeof native.HostScopeContext, "object");
});

test("surfaces of one scope share a single events.poll loop", async () => {
  let inFlight = 0;
  let peak = 0;
  let polls = 0;
  const { transport } = transportWith(async action => {
    assert.equal(action, "events.poll");
    polls += 1;
    inFlight += 1;
    peak = Math.max(peak, inFlight);
    await new Promise(resolve => setTimeout(resolve, 15));
    inFlight -= 1;
    return { events: [], next: 0 };
  });
  const channel = createEventChannel(transport);
  const first = createNativeHost(transport, { activeSession: () => "", close: () => {} }, channel);
  const second = createNativeHost(transport, { activeSession: () => "", close: () => {} }, channel);
  const third = createNativeHost(transport, { activeSession: () => "", close: () => {} }, channel);
  const offs = [
    first.subscribe("session:a", () => {}),
    second.subscribe("session:b", () => {}),
    third.subscribe("planner:p1", () => {}),
  ];
  await new Promise(resolve => setTimeout(resolve, 80));
  for (const off of offs) off();
  channel.close();
  assert.equal(peak, 1, `overlapping polls: ${polls} requests, peak ${peak}`);
});

test("closing one surface leaves the shared event stream running", async () => {
  const scopes: string[] = [];
  let emitted = false;
  const { transport } = transportWith(async (_action, payload) => {
    scopes.push(...((payload?.scopes as string[] | undefined) ?? []));
    // The server filters by cursor, so a value is delivered exactly once.
    if (emitted) return { events: [], next: 1 };
    emitted = true;
    return { events: [{ sequence: 1, scope: "session:b", value: { ok: true } }], next: 1 };
  });
  const channel = createEventChannel(transport);
  const first = createNativeHost(transport, { activeSession: () => "", close: () => {} }, channel);
  const second = createNativeHost(transport, { activeSession: () => "", close: () => {} }, channel);
  const seen: unknown[] = [];
  first.subscribe("session:a", () => {});
  const unsubscribe = second.subscribe("session:b", value => seen.push(value));
  await new Promise(resolve => setTimeout(resolve, 30));
  // The settings-style surface closes; the one still subscribed must keep working.
  first.close();
  await new Promise(resolve => setTimeout(resolve, 30));
  assert.ok(seen.length > 0, "second surface stopped receiving events");
  unsubscribe();
  channel.close();
  assert.ok(scopes.includes("session:b"));
});

test("a scope stops polling once its last subscription is released", async () => {
  let polls = 0;
  const { transport } = transportWith(async () => {
    polls += 1;
    return { events: [], next: 0 };
  });
  const channel = createEventChannel(transport);
  const host = createNativeHost(transport, { activeSession: () => "", close: () => {} }, channel);
  const off = host.subscribe("session:a", () => {});
  await new Promise(resolve => setTimeout(resolve, 20));
  off();
  await new Promise(resolve => setTimeout(resolve, 20));
  const settled = polls;
  await new Promise(resolve => setTimeout(resolve, 60));
  assert.equal(polls, settled, "subscription release kept a poll loop alive");
});

test("a scope without a shared channel owns and closes its own loop", async () => {
  let polls = 0;
  const { transport } = transportWith(async () => {
    polls += 1;
    return { events: [], next: 0 };
  });
  const host = createNativeHost(transport, { activeSession: () => "", close: () => {} });
  host.subscribe("session:a", () => {});
  await new Promise(resolve => setTimeout(resolve, 20));
  host.close();
  await new Promise(resolve => setTimeout(resolve, 20));
  const settled = polls;
  await new Promise(resolve => setTimeout(resolve, 60));
  assert.equal(polls, settled, "standalone host kept polling after close");
});

test("event channel closes every subscription it owns", async () => {
  const { transport } = transportWith(async () => ({ events: [], next: 0 }));
  const channel: EventChannel = createEventChannel(transport);
  let received = 0;
  const off = channel.subscribe("session:a", () => { received += 1; });
  await new Promise(resolve => setTimeout(resolve, 20));
  channel.close();
  off();
  // A closed channel must not revive the loop when a listener tickles it.
  channel.subscribe("session:b", () => {});
  await new Promise(resolve => setTimeout(resolve, 40));
  assert.equal(received, 0);
});

test("events.poll is the only poll action a surface performs", async () => {
  const { transport, calls } = transportWith(async action => action === "events.poll" ? { events: [], next: 0 } : {});
  const channel = createEventChannel(transport);
  const host = createNativeHost(transport, { activeSession: () => "", close: () => {} }, channel);
  const off = host.subscribe("planner:p1", () => {});
  await new Promise(resolve => setTimeout(resolve, 60));
  off();
  channel.close();
  const actions = new Set(calls.map(call => call.action));
  assert.deepEqual([...actions], ["events.poll"]);
});
