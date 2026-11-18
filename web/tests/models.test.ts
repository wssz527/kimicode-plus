import assert from "node:assert/strict";
import { test } from "node:test";
import {
  capabilitySummary,
  fetchModelOptions,
  normalizeModelOptions,
  splitModelKey,
} from "../src/lib/models.js";

test("the official catalog keeps the provider-qualified key and the bare id apart", () => {
  const options = normalizeModelOptions({
    items: [
      { provider: "r2-inspect", model: "r2-inspect/alpha", display_name: "R2 Alpha", capabilities: ["image_in", "tool_use"], support_efforts: ["low", "high"] },
      { provider: "other", model: "other/beta", displayName: "Beta", max_context_size: 65536 },
    ],
  });
  const alpha = options.find(option => option.displayName === "R2 Alpha");
  assert.ok(alpha);
  // The provider is not repeated inside the model half.
  assert.equal(alpha.key, "r2-inspect/alpha");
  assert.equal(alpha.provider, "r2-inspect");
  assert.equal(alpha.model, "alpha");
  assert.deepEqual(alpha.supportEfforts, ["low", "high"]);
  const beta = options.find(option => option.displayName === "Beta");
  assert.equal(beta?.key, "other/beta");
  assert.equal(beta?.maxContextSize, 65536);
});

test("a bare model id from an older catalog still becomes a usable option", () => {
  const options = normalizeModelOptions([{ id: "kimi-k2", name: "Kimi K2" }]);
  assert.deepEqual(options, [
    { key: "kimi-k2", provider: "", model: "kimi-k2", displayName: "Kimi K2", maxContextSize: undefined, capabilities: [], supportEfforts: [] },
  ]);
});

test("duplicates collapse onto one key and display names fall back to the key", () => {
  const options = normalizeModelOptions({
    items: [
      { provider: "p", model: "p/m" },
      { provider: "p", model: "p/m", display_name: "Duplicate" },
      { provider: "p", model: "p/n", display_name: "Named" },
    ],
  });
  assert.equal(options.length, 2);
  const unnamed = options.find(option => option.key === "p/m");
  assert.equal(unnamed?.displayName, "p/m");
});

test("splitModelKey refuses shapes that are not provider-qualified", () => {
  assert.deepEqual(splitModelKey("p/m"), { provider: "p", model: "m" });
  assert.deepEqual(splitModelKey("m"), { provider: "", model: "m" });
  assert.deepEqual(splitModelKey("/m"), { provider: "", model: "/m" });
  assert.deepEqual(splitModelKey("p/"), { provider: "", model: "p/" });
});

test("the capability summary lists known capabilities once, in order", () => {
  const [option] = normalizeModelOptions({
    items: [{ provider: "p", model: "p/m", capabilities: ["image_in", "tool_use", "thinking", "image_in", "custom_cap"] }],
  });
  assert.equal(capabilitySummary(option!), "图片 · 工具 · 思考 · custom_cap");
  assert.equal(capabilitySummary({ ...option!, capabilities: [] }), "");
});

test("fetchModelOptions asks the one desktop action", async () => {
  const calls: { action: string; payload?: Record<string, unknown> }[] = [];
  const options = await fetchModelOptions(async (action, payload) => {
    calls.push({ action, payload });
    return { items: [{ provider: "p", model: "p/m", display_name: "M" }] };
  });
  assert.deepEqual(calls, [{ action: "models.list", payload: {} }]);
  assert.equal(options[0]?.key, "p/m");
});
