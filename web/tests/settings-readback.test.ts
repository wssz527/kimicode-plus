import test from "node:test";
import assert from "node:assert/strict";
import { matchesModelPool, matchesPermissionRules } from "../src/native.js";

const pool = {
  models: { "demo-local": "演示实现与规划", "demo-fast": "快速执行" },
  defaultModel: "demo-local",
};

const rules = [
  { decision: "ask", scope: "user", pattern: "read", reason: "读取项目文件前询问", note: "保留字段" },
  { decision: "deny", scope: "project", pattern: "bash(rm -rf*)", reason: "禁止递归删除" },
];

test("a model pool read back unchanged matches, in either section spelling", () => {
  assert.equal(matchesModelPool({ secondary_model: { models: pool.models, defaultModel: "demo-local" } }, pool), true);
  assert.equal(matchesModelPool({ secondaryModel: { models: pool.models, defaultModel: "demo-local" } }, pool), true);
  // The write uses default_model; the core reports defaultModel.
  assert.equal(matchesModelPool({ secondary_model: { models: pool.models, default_model: "demo-local" } }, pool), true);
});

test("a pool with the same key count but another key does not match", () => {
  const written = { models: { "demo-local": "演示实现与规划", "demo-other": "快速执行" }, defaultModel: "demo-local" };
  assert.equal(matchesModelPool({ secondary_model: written }, pool), false);
});

test("other fields kept in the section do not break the comparison", () => {
  // `defaultEffort` and friends are legitimate neighbours in `secondaryModel`;
  // only the model map and the default model decide the read back.
  const written = { models: pool.models, defaultModel: "demo-local", defaultEffort: "high" };
  assert.equal(matchesModelPool({ secondary_model: written }, pool), true);
});

test("a pool that kept the keys but changed a description does not match", () => {
  const written = { models: { "demo-local": "演示实现与规划", "demo-fast": "旧描述" }, defaultModel: "demo-local" };
  assert.equal(matchesModelPool({ secondary_model: written }, pool), false);
});

test("a pool whose default model differs does not match", () => {
  assert.equal(
    matchesModelPool({ secondary_model: { models: pool.models, defaultModel: "demo-fast" } }, pool),
    false,
  );
  assert.equal(matchesModelPool({ secondary_model: { models: pool.models } }, pool), false);
});

test("permission rules read back unchanged match, field by field and in order", () => {
  assert.equal(matchesPermissionRules({ permission: { rules } }, rules), true);
  assert.equal(matchesPermissionRules({ permission: { rules, dangerousCommandGuard: false } }, rules), true);
  assert.equal(matchesPermissionRules({ permission: { rules: [] } }, []), true);
});

test("rules with the submitted length but a changed field do not match", () => {
  const changedDecision = [{ ...rules[0]!, decision: "allow" }, rules[1]!];
  const changedPattern = [{ ...rules[0]!, pattern: "write" }, rules[1]!];
  assert.equal(matchesPermissionRules({ permission: { rules: changedDecision } }, rules), false);
  assert.equal(matchesPermissionRules({ permission: { rules: changedPattern } }, rules), false);
  // Same length, same set, other order: the rules are evaluated top down, so a
  // reorder is a different configuration.
  assert.equal(matchesPermissionRules({ permission: { rules: [rules[1]!, rules[0]!] } }, rules), false);
});

test("a rule that dropped a preserved field does not match", () => {
  const written = [{ decision: "ask", scope: "user", pattern: "read", reason: "读取项目文件前询问" }, rules[1]!];
  assert.equal(matchesPermissionRules({ permission: { rules: written } }, rules), false);
});

test("a rule list of another length or another shape does not match", () => {
  assert.equal(matchesPermissionRules({ permission: { rules: [rules[0]!] } }, rules), false);
  assert.equal(matchesPermissionRules({ permission: {} }, rules), false);
  assert.equal(matchesPermissionRules({}, rules), false);
  assert.equal(matchesPermissionRules({ permission: { rules: "read" } }, rules), false);
});
