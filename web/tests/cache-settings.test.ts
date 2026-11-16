import assert from "node:assert/strict";
import { test } from "node:test";
import {
  CACHE_NOT_REPORTED,
  commitNumberField,
  hitRateLabel,
  idleLabel,
  mergePolicy,
  shouldApplyPolicyEvent,
  speedLabel,
  tokenLabel,
} from "../src/native.js";

const policy = {
  keepAlive: { enabled: false, minutes: 25 },
  compaction: { enabled: false, idleMinutes: 25, contextThreshold: 60 },
  recovery: { enabled: false, intervalSeconds: 60, entries: [] },
};

test("a provider that never reported cache fields is never shown as 0%", () => {
  // Both a real miss and an unreported provider produce read=0; only the reported
  // state tells them apart, so the label must branch on it.
  assert.equal(hitRateLabel({ hitRate: null, reported: CACHE_NOT_REPORTED }), "未上报");
  assert.equal(hitRateLabel({ hitRate: 0, reported: "REPORTED" }), "0.0%");
  assert.equal(hitRateLabel({ hitRate: 0.975, reported: "REPORTED" }), "97.5%");
  assert.equal(hitRateLabel({ hitRate: null, reported: "NO_SAMPLES" }), "--");
  assert.equal(hitRateLabel(undefined), "--");
});

test("idle label never fabricates a countdown", () => {
  // Plan D4: only "time since the last request" is shown, never a TTL countdown.
  assert.equal(idleLabel(45), "45 分钟");
  assert.equal(idleLabel(0.4), "不到 1 分钟");
  assert.equal(idleLabel(90), "1.5 小时");
  assert.equal(idleLabel(60 * 24 * 3), "3 天");
  assert.equal(idleLabel(null), "尚无请求记录");
  assert.equal(idleLabel(undefined), "尚无请求记录");
  assert.doesNotMatch(idleLabel(45), /还剩|剩余|过期/);
});

test("token and speed labels keep unknown as a dash", () => {
  assert.equal(tokenLabel(999), "999");
  assert.equal(tokenLabel(1500), "1.5K");
  assert.equal(tokenLabel(2_500_000), "2.50M");
  assert.equal(tokenLabel(null), "--");
  assert.equal(tokenLabel(-1), "--");
  assert.equal(speedLabel({ tokensPerSecond: 140.44 }), "140.4 tok/s");
  assert.equal(speedLabel({ tokensPerSecond: undefined }), "--");
  assert.equal(speedLabel(null), "--");
});

test("a background policy event does not clobber an edit in flight", () => {
  // The plan requires that automation events never overwrite a draft being edited.
  assert.equal(shouldApplyPolicyEvent({ writing: false, editing: false }), true);
  assert.equal(shouldApplyPolicyEvent({ writing: true, editing: false }), false);
  assert.equal(shouldApplyPolicyEvent({ writing: false, editing: true }), false);
});

test("number fields only commit integer values inside the allowed range", () => {
  assert.equal(commitNumberField("25", 5, 60), 25);
  assert.equal(commitNumberField(" 5 ", 5, 60), 5);
  assert.equal(commitNumberField("60", 5, 60), 60);
  assert.equal(commitNumberField("4", 5, 60), null);
  assert.equal(commitNumberField("61", 5, 60), null);
  assert.equal(commitNumberField("25.5", 5, 60), null);
  assert.equal(commitNumberField("", 5, 60), null);
  assert.equal(commitNumberField("abc", 5, 60), null);
});

test("merging a policy patch keeps fields the server owns", () => {
  const entries = [{ id: "r1", title: "合成会话", status: "WAITING" }];
  const base = { ...policy, recovery: { ...policy.recovery, entries } };
  const merged = mergePolicy(base, { keepAlive: { enabled: true, minutes: 30 } });
  assert.equal(merged.keepAlive.enabled, true);
  assert.equal(merged.keepAlive.minutes, 30);
  // Untouched groups survive.
  assert.deepEqual(merged.compaction, policy.compaction);
  assert.deepEqual(merged.recovery.entries, entries);
});

test("merging ignores a malformed payload instead of throwing", () => {
  const merged = mergePolicy(policy, null);
  assert.deepEqual(merged, policy);
  const partial = mergePolicy(policy, { compaction: { contextThreshold: 80 } });
  assert.equal(partial.compaction.contextThreshold, 80);
  assert.equal(partial.compaction.idleMinutes, 25);
});

test("keepalive policy defaults stay off", () => {
  // Plan D8 / M2: all three policies are opt-in.
  assert.equal(policy.keepAlive.enabled, false);
  assert.equal(policy.compaction.enabled, false);
  assert.equal(policy.recovery.enabled, false);
});
