import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
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

const cacheSource = readFileSync("src/components/CacheSettings.tsx", "utf8");

const policy = {
  keepAlive: { enabled: false, minutes: 25 },
  idleKeepAlive: {
    enabled: false,
    defaultCount: 2,
    minQuotaPercent: 10,
  },
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
  // Plan D8 / M2 / N4: all policies are opt-in.
  assert.equal(policy.keepAlive.enabled, false);
  assert.equal(policy.idleKeepAlive.enabled, false);
  assert.equal(policy.idleKeepAlive.defaultCount, 2);
  assert.equal(policy.compaction.enabled, false);
  assert.equal(policy.recovery.enabled, false);
});

test("merging keepalive settings drops old manual TTL values and retains partial updates", () => {
  const merged = mergePolicy(policy, {
    idleKeepAlive: {
      enabled: true,
      defaultCount: 3,
      providerTTL: { subscriptionMinutes: 40, otherMinutes: 12 },
    },
  });
  assert.deepEqual(merged.idleKeepAlive, { enabled: true, defaultCount: 3, minQuotaPercent: 10 });
});

test("keepalive surfaces display the observed interval, budget, and stop events", () => {
  const menu = readFileSync("src/components/SessionKeepAliveMenu.tsx", "utf8");
  assert.match(menu, /view\.policy\.intervalMinutes/);
  assert.match(menu, /view\.policy\.source === "observed" \? "模型实测" : "样本不足，默认值"/);
  assert.match(menu, /view\.estimatedTokens/);
  assert.match(menu, /view\.spentTokens.*view\.budgetTokens/);
  assert.match(menu, /view\.stoppedDetail/);
  assert.doesNotMatch(menu, /untilAt|截止时刻/);
  assert.match(cacheSource, /event\?\.type === "keepalive\.alert"/);
  assert.match(cacheSource, /sent:.*已发送/);
  assert.match(cacheSource, /hit:.*命中/);
  assert.match(cacheSource, /miss:.*未命中/);
  assert.doesNotMatch(cacheSource, /providerTTL|80 分钟|已抑制官方通知/);
});

test("the model retention dropdown resets to 全部模型 when the selected model disappears", () => {
  // A stale value would render an empty <select> whose label no longer matches any
  // observation row, so the component clears it as soon as the model is missing.
  assert.match(cacheSource, /if \(!retentionModel\) return;[\s\S]*?setRetentionModel\(""\)/);
  assert.match(cacheSource, /retention\?\.byModel\?\.some\(item => item\.model === retentionModel\)/);
  // Options carry the sample count so two same-named buckets stay distinguishable.
  assert.match(cacheSource, /typeof item\.samples === "number" && item\.samples > 0/);
  assert.match(cacheSource, /（\$\{item\.samples\} 次）/);
});

test("the sparkline scales against a single peak and marks empty buckets", () => {
  // One peak per render, not one per bar: the previous implementation recomputed it
  // inside the map, and empty buckets drew a full-accessibility accent bar.
  assert.equal(cacheSource.match(/reduce\(/g)?.length, 1);
  assert.equal(cacheSource.includes("Math.max(...history.points.map"), false);
  assert.match(cacheSource, /const peakTokens = historyPoints\.reduce/);
  assert.match(cacheSource, /const empty = total === 0/);
  assert.match(cacheSource, /eks-spark-bar\$\{empty \? " is-empty" : ""\}/);
  assert.match(cacheSource, /height: empty \? "2px"/);
  assert.match(cacheSource, /：无记录/);
});
