import test from "node:test";
import assert from "node:assert/strict";
import { createCoreTransport, createNativeHost, followNativeSession, preferPlannerThread } from "../src/native.js";

test("native sidebar changes follow the primary pane and preserve extra panes", () => {
  assert.deepEqual(followNativeSession(["a", "b", "c"], "d"), ["d", "b", "c"]);
  assert.deepEqual(followNativeSession(["a", "b", "c"], "b"), ["b", "a", "c"]);
  assert.deepEqual(followNativeSession(["a", "b", "c"], "c"), ["c", "b", "a"]);
  const ids = ["a", "b"];
  assert.equal(followNativeSession(ids, "a"), ids);
  assert.equal(followNativeSession(ids, ""), ids);
  assert.deepEqual(followNativeSession([], "a"), ["a"]);
});

test("native host routes events only to their session and stops polling on close", async () => {
  let polls = 0;
  let closed = false;
  const host = createNativeHost({
    request: async <T = unknown>(action: string): Promise<T> => {
      assert.equal(action, "events.poll");
      polls += 1;
      if (polls === 1) return { events: [], next: 0 } as T;
      return { events: [{ sequence: 1, scope: "session:b", value: { ok: true } }], next: 1 } as T;
    },
  }, { activeSession: () => "b", close: () => { closed = true; } });
  let aCalls = 0;
  let bValue: unknown = null;
  host.subscribe("session:a", () => { aCalls += 1; });
  host.subscribe("session:b", value => { bValue = value; host.close(); });
  // An empty page keeps the loop gentle, so wait for the delivery instead of a
  // fixed delay that depends on the idle guard.
  const deadline = Date.now() + 2000;
  while (!closed && Date.now() < deadline) await new Promise(resolve => setTimeout(resolve, 10));
  assert.equal(aCalls, 0);
  assert.deepEqual(bValue, { ok: true });
  assert.equal(closed, true);
  const settled = polls;
  await new Promise(resolve => setTimeout(resolve, 100));
  assert.equal(polls, settled);
});

test("core transport preserves unicode JSON and returns a real Response", async () => {
  let seen: { action: string; payload: { method?: string; bodyBase64: string; headers: Record<string, string> } } | null = null;
  const host = createNativeHost({
    request: async <T = unknown>(action: string, payload?: Record<string, unknown>): Promise<T> => {
      seen = { action, payload: payload as { method?: string; bodyBase64: string; headers: Record<string, string> } };
      return { status: 200, headers: [["Content-Type", "application/json"]], bodyBase64: Buffer.from(JSON.stringify({ ok: true })).toString("base64") } as T;
    },
  }, { activeSession: () => "", close: () => {} });
  const transport = createCoreTransport(host);
  const response = await transport("/enhance/v1/example", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ text: "规划🙂" }) });
  assert.deepEqual(await response.json(), { ok: true });
  assert.equal(seen!.action, "core.fetch");
  assert.equal(seen!.payload.method, "POST");
  assert.deepEqual(JSON.parse(Buffer.from(seen!.payload.bodyBase64, "base64").toString("utf8")), { text: "规划🙂" });
  assert.equal("authorization" in seen!.payload.headers, false);
});

test("core transport constructs valid Response for HTTP 204 empty bodies", async () => {
  const host = createNativeHost({
    request: async <T = unknown>(): Promise<T> =>
      ({ status: 204, headers: [["Content-Type", "application/json"]], bodyBase64: "" }) as T,
  }, { activeSession: () => "", close: () => {} });
  const transport = createCoreTransport(host);
  const response = await transport("/enhance/v1/credentials/translator", { method: "DELETE" });
  assert.equal(response.status, 204);
  assert.equal(await response.text(), "");
});

test("planner state refill accepts current revision and rejects stale running snapshots", () => {
  const base = { id: "p", title: "规划", model: "m", options: { context: false, skills: false, tools: false, readOnly: true }, messages: [], status: "idle" as const, revision: 3 };
  assert.equal(preferPlannerThread(null, base), true);
  assert.equal(preferPlannerThread(base, { ...base, status: "running", revision: 2 }), false);
  assert.equal(preferPlannerThread(base, { ...base, revision: 4 }), true);
  assert.equal(preferPlannerThread(base, { ...base, id: "other", revision: 0 }), true);
});
