import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import {
  formatAccountQuotaLine,
  listAccounts,
  preflightAccountSwitch,
  resetAccountVault,
  switchAccount,
  type AccountsState,
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
  await switchAccount(host, "acc_1", false, true);

  assert.deepEqual(calls, [
    { action: "accounts.list", payload: { refreshQuota: true } },
    { action: "accounts.switchPreflight", payload: { id: "acc_1", activeModel: "managed:kimi-code/k2.8" } },
    { action: "accounts.switch", payload: { id: "acc_1", waitUntilIdle: true } },
    { action: "accounts.switch", payload: { id: "acc_1", force: true } },
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

test("resetAccountVault asks the core for an explicit confirmed reset", async () => {
  const calls: { action: string; payload: unknown }[] = [];
  const host = {
    request: async (action: string, payload?: unknown) => {
      calls.push({ action, payload });
      return {} as never;
    },
  } as unknown as NativeHost;

  await resetAccountVault(host);

  // The core refuses a reset without confirm:true, so the wrapper must send it.
  assert.deepEqual(calls, [{ action: "accounts.resetVault", payload: { confirm: true } }]);
});

test("AccountsState carries the OS key store fields the vault reports", () => {
  const state: AccountsState = {
    vaultUnlocked: true,
    vaultOsKeychain: true,
    vaultOsKeychainAvailable: true,
    vaultOsKeychainBackend: "MACOS_KEYCHAIN",
    legacyPasswordVault: false,
    signedIn: true,
    accounts: [],
    cliRunning: false,
  };
  assert.equal(state.vaultOsKeychain, true);
  assert.equal(state.vaultOsKeychainBackend, "MACOS_KEYCHAIN");
  assert.equal(state.legacyPasswordVault, false);

  // A core that predates OS key management answers without these fields; the
  // panel must not treat that as "no key store" or crash on the missing keys.
  const legacy: AccountsState = { vaultUnlocked: false, signedIn: false, accounts: [], cliRunning: false };
  assert.equal(legacy.vaultOsKeychain, undefined);
  assert.equal(legacy.vaultOsKeychainAvailable, undefined);
  assert.equal(legacy.legacyPasswordVault, undefined);
});

test("the accounts section hides the password prompt once the OS vault auto-unlocks", () => {
  const section = readFileSync("src/components/AccountsSection.tsx", "utf8");
  // Unlocked: only a calm status line, never the password form.
  assert.ok(section.includes("state.vaultUnlocked && state.vaultOsKeychain"), "no OS-managed unlocked branch");
  assert.ok(section.includes("由 macOS 钥匙串自动解锁"), "macOS keychain hint missing");
  assert.ok(section.includes("由 Windows DPAPI 自动解锁"), "Windows DPAPI hint missing");
  assert.ok(section.includes("系统凭据库已自动解锁"), "generic key store hint missing");
  // The unlock form stays keyed on a locked vault alone.
  assert.ok(section.includes("{state && !state.vaultUnlocked ? ("), "the unlock form must only render while locked");
});

test("a forgotten legacy vault password can be reset in two steps and says what is lost", () => {
  const section = readFileSync("src/components/AccountsSection.tsx", "utf8");
  assert.ok(section.includes("检测到旧版密码保护的凭据库"), "the legacy vault is not explained");
  assert.ok(section.includes("解锁并转为自动托管"), "the migrating unlock action is missing");
  assert.ok(section.includes("忘记密码，重置凭据库"), "the reset entry is missing");
  assert.ok(section.includes("确认重置凭据库"), "the second confirmation step is missing");
  assert.ok(section.includes("resetAccountVault(host)"), "the reset does not go through the host action");
  assert.ok(section.includes("已重置并切换为系统自动托管"), "a successful reset is not confirmed");
  // The copy must be honest about the destructive part and the untouched login.
  assert.ok(section.includes("不会让你退出当前官方"), "the copy does not say the official login survives");
  assert.ok(section.includes("state.legacyPasswordVault || state.vaultOsKeychainAvailable"), "both legacy markers should offer the reset");
});

test("busy sessions warn without deadlocking and offer both wait and force switch", () => {
  const section = readFileSync("src/components/AccountsSection.tsx", "utf8");
  assert.equal(section.includes("已拒绝立即切换"), false, "copy should warn instead of hard-refusing");
  assert.ok(section.includes("以下会话仍在运行中"), "busy session warning heading missing");
  assert.ok(section.includes("仍然立即切换"), "force switch button missing in confirm dialog");
  assert.ok(section.includes("等完成后自动切换"), "wait-until-idle option should remain available");
  assert.ok(section.includes('data-ekd-account-force-switch="true"'), "force switch hook missing");
  assert.ok(section.includes('data-ekd-account-force-pending="true"'), "pending banner immediate switch hook missing");
});
