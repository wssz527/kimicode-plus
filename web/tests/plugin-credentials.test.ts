import assert from "node:assert/strict";
import { test } from "node:test";
import { readFileSync } from "node:fs";
import {
  credentialState,
  declaredCredentialNames,
  deleteCredential,
  grantedCredentialNames,
} from "../src/lib/pluginCredentials.js";

const source = (path: string) => readFileSync(path, "utf8");

test("only credential names the plugin declared are offered", () => {
  // The form is rendered from the manifest, never from free text: a name the
  // plugin did not declare could not be stored under its grant anyway.
  assert.deepEqual(
    declaredCredentialNames({ declaredCapabilities: ["credentials:alpha", "net:api.example.com", "credentials:beta", "credentials:alpha"] }),
    ["alpha", "beta"],
  );
  assert.deepEqual(declaredCredentialNames({}), []);
  assert.deepEqual(declaredCredentialNames({ declaredCapabilities: ["credentials:"] }), []);
  // An older record without declaredCapabilities still shows what it can use.
  assert.deepEqual(declaredCredentialNames({ capabilities: ["credentials:legacy"] }), ["legacy"]);
});

test("only granted credential names count as usable", () => {
  assert.deepEqual(
    [...grantedCredentialNames({ declaredCapabilities: ["credentials:a", "credentials:b"], capabilities: ["credentials:b", "net:example.com"] })],
    ["b"],
  );
  assert.equal(grantedCredentialNames({}).size, 0);
});

test("a credential whose vault is locked is described as unusable, not as missing", () => {
  assert.equal(credentialState(true, true).tone, "ok");
  const locked = credentialState(true, false);
  assert.equal(locked.tone, "warn");
  assert.equal(locked.label, "已设置，凭据库锁定");
  const missing = credentialState(false, false);
  assert.equal(missing.tone, "muted");
  assert.equal(missing.label, "未设置");
});

test("the credential panel never renders or echoes a plaintext value", () => {
  const panel = source("src/components/PluginCredentials.tsx");
  // The value only travels from the controlled input to features.saveSecret and
  // is cleared right after; no toast, list row or readback may show it.
  assert.ok(panel.includes("features.saveSecret(name, value)"), "the panel no longer saves through the vault API");
  assert.ok(panel.includes('setValue("")'), "the plaintext input is not cleared after save");
  assert.ok(panel.includes("vault.data?.refs"), "the panel no longer reads the stored reference list");
  // Reading a secret back is not part of the surface at all.
  assert.equal(panel.includes("getSecret"), false);
});

test("the panel requires an unlocked vault and offers the unlock entry", () => {
  const panel = source("src/components/PluginCredentials.tsx");
  assert.ok(panel.includes("解锁 / 初始化凭据库"), "the panel has no unlock entry, so a locked vault would be a dead end");
  // Writing and deleting both require the unlock, matching the core's 423 answer.
  assert.ok(panel.includes("disabled={!value || !unlocked}"));
  assert.ok(panel.includes("disabled={action.busy || !unlocked}"));
});

test("the plugin detail page mounts the credential panel", () => {
  const settings = source("src/components/PluginSettings.tsx");
  assert.ok(settings.includes('from "./PluginCredentials"'), "the detail page does not import the panel");
  assert.ok(settings.includes("<PluginCredentials plugin={current} />"), "the detail page does not render the panel");
});

test("deleting a credential refreshes state even when the core answers HTTP 204 (undefined)", async () => {
  let refreshed = 0;
  const run = async (task: () => Promise<boolean>): Promise<boolean | null> => {
    try {
      return await task();
    } catch {
      return null;
    }
  };

  const deleted = await deleteCredential(run, async () => undefined, () => {
    refreshed += 1;
  });
  assert.equal(deleted, true);
  assert.equal(refreshed, 1);

  const failed = await deleteCredential(
    run,
    async () => {
      throw new Error("请先解锁凭据库");
    },
    () => {
      refreshed += 1;
    },
  );
  assert.equal(failed, false);
  assert.equal(refreshed, 1);

  const panel = source("src/components/PluginCredentials.tsx");
  assert.ok(panel.includes("deleteCredential(action.run,"), "remove() must use deleteCredential so 204/undefined still refreshes");
});

test("an OS-managed vault is described as automatic, not as a password to remember", () => {
  const panel = source("src/components/PluginCredentials.tsx");
  assert.ok(panel.includes("vault.data?.osKeychain"), "the panel ignores the OS keychain flag");
  assert.ok(panel.includes("vault.data?.osKeychainAvailable"), "the panel ignores whether a key store exists");
  assert.ok(panel.includes("vault.data?.legacyPasswordVault"), "the panel ignores the legacy password vault flag");
  assert.ok(panel.includes("凭据由系统凭据库自动托管"), "no reassuring note for the auto-managed vault");
  assert.ok(panel.includes("macOS 钥匙串 / Windows DPAPI"), "the note does not name the OS stores");
  // Telling the user a restart will ask again would be false once a key store holds the key.
  assert.ok(
    panel.includes("输入一次原口令即可解锁，随后会转入系统凭据库自动托管"),
    "the migrating unlock hint is missing",
  );
  assert.ok(panel.includes("重启后需要重新输入") && panel.includes("旧版密码保护的凭据库"), "the restart warning survives only for a manual vault");
});

test("a forgotten legacy vault password can be reset from the credential panel", () => {
  const panel = source("src/components/PluginCredentials.tsx");
  assert.ok(panel.includes("features.resetVault()"), "the reset does not go through the vault reset API");
  assert.ok(panel.includes("忘记旧密码？重置凭据库"), "the reset entry is missing");
  assert.ok(panel.includes("确认重置凭据库"), "the second confirmation step is missing");
  assert.ok(panel.includes("vault.reload()"), "a successful reset must reload the vault state");
  assert.ok(panel.includes("已保存的内容需要重新填写"), "the copy does not say the stored values are lost");
  assert.ok(panel.includes("Kimi 账号令牌"), "the copy omits account token deletion");
  // The reset is only offered where a password vault actually exists.
  assert.ok(panel.includes("legacyPasswordVault || osKeychainAvailable"), "the reset is offered without a legacy vault");
});
