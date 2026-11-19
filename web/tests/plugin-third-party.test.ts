import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { api, connectHostTransport, type PluginLegal, type PluginPreview, type PluginRecord } from "../src/api.js";
import { createPluginRuntime } from "../src/plugin-runtime/runtime.js";

const pickDirectoryGrants = [
  "schedule:manage", "sessions:send", "handoff", "attachments", "planner:run",
  "ui:main-area", "ui:right-panel", "ui:sidebar", "ui:settings",
];
const openSettingsGrants = [
  "ui:main-area", "ui:sidebar-nav", "ui:right-panel", "ui:sidebar", "ui:settings", "ui:entry", "schedule:manage",
];

test("each eligible grant independently enables its native UI helper", async () => {
  const picked: string[] = [];
  const opened: string[] = [];
  const runtime = createPluginRuntime({
    slots: {},
    request: async () => ({ value: {} }),
    pickDirectory: async options => {
      picked.push(options?.title ?? "");
      return "/tmp/workspace";
    },
    openSettings: async tab => { opened.push(tab ?? ""); },
  });
  try {
    const all = new Set([...pickDirectoryGrants, ...openSettingsGrants, "sessions:create", "storage"]);
    for (const [index, capability] of [...all].entries()) {
      await runtime.activate({
        activate: async ({ ui }) => {
          if (pickDirectoryGrants.includes(capability)) {
            assert.equal(await ui.pickDirectory!({ title: capability }), "/tmp/workspace");
          } else {
            await assert.rejects(ui.pickDirectory!({ title: capability }), { code: "PLUGIN_CAPABILITY_DENIED" });
          }
          if (openSettingsGrants.includes(capability)) {
            await ui.openSettings!(capability);
          } else {
            await assert.rejects(ui.openSettings!(capability), { code: "PLUGIN_CAPABILITY_DENIED" });
          }
        },
      }, { id: `local.plugin-${index}`, version: "1.0.0", apiVersion: 2, granted: [capability], token: `token-${index}` });
    }
    assert.deepEqual(picked.sort(), [...pickDirectoryGrants].sort());
    assert.deepEqual(opened.sort(), [...openSettingsGrants].sort());
  } finally {
    runtime.dispose();
  }
});

test("plugin preview, explicit consent and legal disclosures use the core wire fields", async () => {
  const preview: PluginPreview = {
    packageDigest: "a".repeat(64), id: "local.package", name: "Local package", version: "1.0.0", apiVersion: 2,
    hostVersions: ["1.*"], capabilities: ["storage", "ui:layout"], privilegedCapabilities: ["ui:layout"],
    compatible: true, installed: false, source: "LOCAL", upstreamRepo: "owner/repo",
    upstreamUrl: "https://github.com/owner/repo", upstreamCommit: "abcdef123456", upstreamLicense: "MIT", hasNotice: true,
  };
  const installed: PluginRecord = { ...preview, source: "LOCAL", state: "DISABLED", enabled: false };
  const legal: PluginLegal = {
    id: preview.id, license: "MIT", licenseText: "License text", licenseTruncated: true,
    noticeFile: "NOTICE.md", noticeText: "Attribution", noticeTruncated: false,
    upstreamRepo: "owner/repo", upstreamUrl: "https://github.com/owner/repo", upstreamCommit: "abcdef123456", upstreamLicense: "MIT",
  };
  const calls: Array<{ path: string; method?: string; body?: unknown }> = [];
  connectHostTransport(async (path, init) => {
    calls.push({ path, method: init.method, body: typeof init.body === "string" ? JSON.parse(init.body) : undefined });
    if (path.endsWith("/preview")) return Response.json(preview);
    if (path.endsWith("/install")) return Response.json(installed, { status: 201 });
    return Response.json(legal);
  });
  try {
    assert.deepEqual(await api.plugins.preview("/tmp/package.zip"), preview);
    assert.deepEqual(await api.installPlugin("/tmp/package.zip", preview.packageDigest, ["storage", "ui:layout"], true), installed);
    await api.plugins.install("/tmp/package.zip", preview.packageDigest, ["storage"]);
    assert.deepEqual(await api.pluginLegal("local/package"), legal);
    assert.deepEqual(calls, [
      { path: "/enhance/v1/plugins/preview", method: "POST", body: { root: "/tmp/package.zip" } },
      { path: "/enhance/v1/plugins/install", method: "POST", body: { root: "/tmp/package.zip", packageDigest: preview.packageDigest, capabilities: ["storage", "ui:layout"], confirmPrivileged: true } },
      { path: "/enhance/v1/plugins/install", method: "POST", body: { root: "/tmp/package.zip", packageDigest: preview.packageDigest, capabilities: ["storage"] } },
      { path: "/enhance/v1/plugins/local%2Fpackage/legal", method: "GET", body: undefined },
    ]);
  } finally {
    connectHostTransport(null);
  }
});

test("plugin settings exposes confirmation, native pickers and lazy plain-text disclosures", () => {
  const source = readFileSync("src/components/PluginSettings.tsx", "utf8");
  for (const name of ["ui:layout", "sessions:approve", "sessions:abort", "sessions:history", "attachments", "browser:view", "planner:run", "handoff", "commands"]) {
    const key = name.includes(":") ? `"${name}"` : name;
    assert.ok(source.includes(`${key}: { title:`), `missing label for ${name}`);
  }
  assert.match(source, /host\.request[^\n]+"host\.pickDirectory"/);
  assert.match(source, /host\.request[^\n]+"host\.pickFiles"/);
  assert.match(source, /disabled=\{needsPrivilegeConfirmation && !confirmPrivileged\}/);
  assert.match(source, /api\.installPlugin\(installPath\.trim\(\), preview\.packageDigest, granted, needsPrivilegeConfirmation \? true : undefined\)/);
  assert.match(source, /api\.pluginLegal\(plugin\.id\), \{ enabled: open !== null \}/);
  assert.match(source, /<pre[^>]+>\{text\}<\/pre>/);
  assert.match(source, /target="_blank" rel="noopener noreferrer"/);
});

test("a package changed after preview rejects installation and clears the confirmation step", async () => {
  const oldDigest = "a".repeat(64);
  const newDigest = "b".repeat(64);
  let packageDigest = oldDigest;
  const message = "插件包在预览后发生了变化，请重新预览";
  connectHostTransport(async (path, init) => {
    if (path.endsWith("/preview")) return Response.json({ packageDigest });
    const request = JSON.parse(String(init.body));
    return request.packageDigest === packageDigest
      ? Response.json({ state: "DISABLED" }, { status: 201 })
      : Response.json({ error: { code: "PLUGIN_PACKAGE_CHANGED", message } }, { status: 409 });
  });
  try {
    const preview = await api.plugins.preview("/tmp/package.zip");
    packageDigest = newDigest;
    await assert.rejects(api.installPlugin("/tmp/package.zip", preview.packageDigest, ["storage"]), {
      status: 409, code: "PLUGIN_PACKAGE_CHANGED", message,
    });
    const fresh = await api.plugins.preview("/tmp/package.zip");
    assert.equal((await api.installPlugin("/tmp/package.zip", fresh.packageDigest, [])).state, "DISABLED");
    const settings = readFileSync("src/components/PluginSettings.tsx", "utf8");
    assert.match(settings, /error\.code === "PLUGIN_PACKAGE_CHANGED"\) \{\s*setPreview\(null\);\s*setGranted\(\[\]\);\s*setConfirmPrivileged\(false\);\s*setReconfirmGrants\(true\);/);
    assert.match(settings, /setGranted\(reconfirmGrants \? \[\] : result\.capabilities\)/);
  } finally {
    connectHostTransport(null);
  }
});
