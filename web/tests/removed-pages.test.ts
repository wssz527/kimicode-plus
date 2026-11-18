import assert from "node:assert/strict";
import { test } from "node:test";
import { existsSync, readFileSync } from "node:fs";

const source = (path: string) => readFileSync(path, "utf8");

test("the standalone model and credential pages are gone with their entry points", () => {
  for (const path of [
    "src/pages/ModelsPage.tsx",
    "src/pages/CredentialsPage.tsx",
    "src/components/ManagementWindow.tsx",
    "src/components/ProviderDiscovery.tsx",
    "src/components/PublicCatalogImport.tsx",
  ]) {
    assert.equal(existsSync(path), false, `${path} should be deleted`);
  }
  for (const [path, needle] of [
    ["src/App.tsx", '"/models"'],
    ["src/App.tsx", '"/credentials"'],
    ["src/routes.ts", '"/models"'],
    ["src/routes.ts", '"/credentials"'],
    ["src/native.ts", "openManagement"],
    ["src/native.ts", "managementPaths"],
    ["src/host-entry.tsx", "ManagementWindow"],
    ["src/components/SettingsSurface.tsx", "独立窗口"],
  ] as const) {
    assert.equal(source(path).includes(needle), false, `${path} still mentions ${needle}`);
  }
});

test("the host bridge no longer opens or whitelists a management window", () => {
  const bootstrap = source("../internal/desktop/assets/bootstrap.js");
  assert.equal(bootstrap.includes("window.openManagement"), false, "bootstrap still exposes the window action");
  assert.equal(bootstrap.includes("managementPaths"), false, "bootstrap still whitelists management routes");
  assert.equal(bootstrap.includes("'management'"), false, "bootstrap still binds a management surface");
  const renderer = source("../internal/desktop/assets/renderer.js");
  assert.equal(renderer.includes("'management'"), false, "renderer still mounts the management surface");
});

test("the subagent pool offers the catalog instead of a free-text model field", () => {
  const settings = source("src/components/NativeSettings.tsx");
  // The old fallback rendered a plain text input labelled 模型别名 when the
  // catalog was empty; a pool entry must always come from the official list.
  assert.equal(settings.includes("模型别名"), false, "the free-text model fallback is back");
  assert.ok(settings.includes("fetchModelOptions"), "the pool no longer reads the official catalog");
  assert.ok(settings.includes("还没有可选的模型，请先在 Kimi Code 设置 → 供应商中添加。"), "the empty state lost its pointer to the official page");
  assert.ok(settings.includes("打开官方供应商页"), "the empty state has no way to open the official page");
});

test("the pool and the chat page use one picker component", () => {
  for (const path of ["src/components/NativeSettings.tsx", "src/pages/ChatPage.tsx"]) {
    assert.ok(source(path).includes('from "./ModelSelect"') || source(path).includes('from "../components/ModelSelect"'), `${path} does not use the shared picker`);
  }
  const picker = source("src/components/ModelSelect.tsx");
  assert.ok(picker.includes("当前供应商中找不到这个模型"), "a saved model missing from the catalog must be marked, not dropped");
});
