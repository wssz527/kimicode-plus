import test from "node:test";
import assert from "node:assert/strict";
import {
  commitNumberField,
  configMaxSteps,
  configSection,
  configTimeoutMinutes,
  readAddonFeatures,
} from "../src/native.js";

test("a number field accepts zero for the step limit and rejects negatives", () => {
  // 0 is the documented "no limit" value for loop_control.maxStepsPerTurn.
  assert.equal(commitNumberField("0", 0, 10000), 0);
  assert.equal(commitNumberField(" 0 ", 0, 10000), 0);
  assert.equal(commitNumberField("256", 0, 10000), 256);
  for (const text of ["-1", "1.5", "abc", "", "  ", "1e3", "10001"]) {
    assert.equal(commitNumberField(text, 0, 10000), null, text);
  }
  // The subagent timeout keeps its own floor: a zero-minute timeout is meaningless.
  assert.equal(commitNumberField("0", 1, 10080), null);
  assert.equal(commitNumberField("120", 1, 10080), 120);
});

test("addon reads still go through features.get only", async () => {
  const calls: string[] = [];
  const host = {
    request: async <T = unknown>(action: string): Promise<T> => {
      calls.push(action);
      return { planner: true } as T;
    },
  };
  assert.deepEqual(await readAddonFeatures(host), { planner: true, multiSession: false });
  assert.deepEqual(calls, ["features.get"]);
});

test("config readback understands the camelCase GET for loop control and subagent", () => {
  // KAP answers camelCase even when config.save was given snake_case.
  const camel = { loop_control: { maxStepsPerTurn: 256 }, subagent: { timeoutMs: 7_200_000 } };
  assert.equal(configMaxSteps(camel), "256");
  assert.equal(configTimeoutMinutes(camel), "120");
  // The snake_case spelling must keep working for a core that echoes the write.
  const snake = { loop_control: { max_steps_per_turn: 0 }, subagent: { timeout_ms: 1_800_000 } };
  assert.equal(configMaxSteps(snake), "0");
  assert.equal(configTimeoutMinutes(snake), "30");
  assert.equal(configTimeoutMinutes({}), "");
  assert.equal(configMaxSteps(null), "");
});

test("config sections accept a null-cleared pool without inventing an entry", () => {
  assert.equal(configSection({ secondary_model: null }, ["secondary_model", "secondaryModel"]), null);
  assert.equal(configSection({ secondaryModel: {} }, ["secondary_model", "secondaryModel"]) !== null, true);
  assert.deepEqual(configSection({}, ["secondary_model"]), null);
});
