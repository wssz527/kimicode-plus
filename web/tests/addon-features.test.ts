import test from "node:test";
import assert from "node:assert/strict";
import { addonFeatureKeys, normalizeAddonFeatures, readAddonFeatures, writeAddonFeatures } from "../src/native.js";

test("addon features default to off for missing or untrusted values", () => {
  assert.equal(addonFeatureKeys.includes("planner"), true);
  assert.equal(addonFeatureKeys.includes("multiSession"), true);
  for (const value of [null, undefined, {}, "planner", 1, [], { planner: "true", multiSession: 1 }, { planner: null }]) {
    assert.deepEqual(normalizeAddonFeatures(value), { planner: false, multiSession: false }, `accepted ${JSON.stringify(value)}`);
  }
});

test("addon features only follow explicit booleans", () => {
  assert.deepEqual(normalizeAddonFeatures({ planner: true }), { planner: true, multiSession: false });
  assert.deepEqual(normalizeAddonFeatures({ multiSession: true }), { planner: false, multiSession: true });
  assert.deepEqual(normalizeAddonFeatures({ planner: true, multiSession: true, extra: true }), { planner: true, multiSession: true });
});

test("feature requests use the host action names and echo the saved state", async () => {
  const calls: { action: string; payload?: Record<string, unknown> }[] = [];
  const host = {
    request: async <T = unknown>(action: string, payload?: Record<string, unknown>): Promise<T> => {
      calls.push({ action, payload });
      return (action === "features.get" ? { planner: false, multiSession: true } : { ...(payload ?? {}), planner: true }) as T;
    },
  };
  assert.deepEqual(await readAddonFeatures(host), { planner: false, multiSession: true });
  assert.equal(calls[0]!.action, "features.get");
  const saved = await writeAddonFeatures(host, { planner: true, multiSession: false });
  assert.equal(calls[1]!.action, "features.set");
  assert.deepEqual(calls[1]!.payload, { planner: true, multiSession: false });
  assert.deepEqual(saved, { planner: true, multiSession: false });
});

test("writing features drops values that are not explicit booleans", async () => {
  let sent: unknown = null;
  const host = {
    request: async <T = unknown>(action: string, payload?: Record<string, unknown>): Promise<T> => {
      sent = payload;
      return payload as T;
    },
  };
  await writeAddonFeatures(host, { planner: "yes", multiSession: 1 } as unknown as { planner: boolean; multiSession: boolean });
  assert.deepEqual(sent, { planner: false, multiSession: false });
});
