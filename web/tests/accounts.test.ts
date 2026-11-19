import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import {
  formatAccountQuotaLine,
  listAccounts,
  preflightAccountSwitch,
  switchAccount,
  type NativeHost,
} from "../src/native.js";

test("formatAccountQuotaLine never fabricates 0% on missing or failed quota", () => {
  assert.equal(formatAccountQuotaLine(null), "5小时：未知 · 每周：未知");
  assert.equal(formatAccountQuotaLine(undefined), "5小时：未知 · 每周：未知");
  assert.equal(formatAccountQuotaLine({ status: "unknown", error: "network_error" }), "5小时：未知 · 每周：未知");
  assert.equal(formatAccountQuotaLine({ status: "auth_rejected" }), "令牌已失效，需要重新登录");
  assert.equal(formatAccountQuotaLine({ status: "ok", limit5hUsed: 24.6, limit7dUsed: 71.2 }), "5小时 25% · 每周 71%");
  assert.equal(formatAccountQuotaLine({ status: "ok", limit5hUsed: 0, limit7dUsed: undefined }), "5小时 0% · 每周：未知");
});

test("account action wrappers send exact action names and payloads", async () => {
  const calls: { action: string; payload: unknown }[] = [];
  const host = {
    request: async (action: string, payload?: unknown) => {
      calls.push({ action, payload });
      return {} as never;
    },
  } as unknown as NativeHost;

  await listAccounts(host, true);
  await preflightAccountSwitch(host, "acc_1", "managed:kimi-code/k2.8");
  await switchAccount(host, "acc_1", true);

  assert.deepEqual(calls, [
    { action: "accounts.list", payload: { refreshQuota: true } },
    { action: "accounts.switchPreflight", payload: { id: "acc_1", activeModel: "managed:kimi-code/k2.8" } },
    { action: "accounts.switch", payload: { id: "acc_1", waitUntilIdle: true } },
  ]);
});

test("N3 account management stays in official account tab and user-menu without leaking into KimiCode+", () => {
  const surface = readFileSync("src/components/SettingsSurface.tsx", "utf8");
  assert.equal(surface.includes("AccountsManagerSection"), false, "KimiCode+ should only render AccountSettingsSection via fallback");
  const renderer = readFileSync("../internal/desktop/assets/renderer.js", "utf8");
  assert.ok(renderer.includes("syncAccountMenu"));
  assert.ok(renderer.includes("refreshOfficialAuth"));
  assert.ok(renderer.includes("ekd:account-switched"));
  assert.ok(renderer.includes("account-switch-modal"));
});
