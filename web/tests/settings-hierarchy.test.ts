import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import {
  evaluateDOMContracts,
  findOfficialSettingsPanel,
  findOfficialSettingsSection,
  resolveSettingsFallbacks,
} from "../src/plugin-runtime/contracts.js";
import { FakeDocument } from "./helpers/fake-dom.js";

const source = (path: string) => readFileSync(path, "utf8");

test("KimiCode+ settings tab is slimmed to diagnostics, an entry summary and fallbacks", () => {
  const surface = source("src/components/SettingsSurface.tsx");
  const [main, cacheTab] = surface.split("export function CacheSettingsSurface");
  assert.ok(cacheTab, "CacheSettingsSurface must be exported for the standalone 缓存与用量 tab");
  assert.equal(surface.includes("const SECTIONS"), false, "5-row secondary list should be removed");
  assert.equal(surface.includes("Kimi Code 配置"), false, "native config entry should live in official agent tab");
  assert.equal(surface.includes("附加功能"), false, "addons entry should live in official lab tab");
  assert.equal(surface.includes("Monitor 与故障隔离"), false, "outdated Monitor copy should be removed");
  assert.equal(surface.includes("缓存与额度"), false, "cache section should be named 缓存与用量");

  assert.ok(main, "SettingsWindow must precede the standalone cache surface");
  assert.equal(main.includes('data-ekd-section="cache"'), false, "cache has its own standalone tab now");
  assert.equal(main.includes('data-ekd-section="plugins"'), false, "enhanced plugins mount into the official 插件 tab");
  assert.ok(main.includes('data-ekd-section="diagnostics"'));
  assert.ok(main.includes('data-ekd-section="entries"'));
  assert.ok(main.includes(">状态与诊断<"));
  assert.ok(main.includes(">功能入口<"));
  assert.ok(main.includes('data-ekd-entry-summary="true"'));
  for (const label of ["缓存与用量", "插件", "实验室", "账户", "智能体与会话"]) {
    assert.ok(main.includes(`label: "${label}"`), `entry summary must point at ${label}`);
  }

  assert.ok(cacheTab.includes('data-ekd-settings-root="cache"'));
  assert.ok(cacheTab.includes('data-ekd-section="cache"'));
  assert.ok(cacheTab.includes(">缓存与用量<"));
  assert.ok(cacheTab.includes("<CacheSettings host={host} />"));
});

test("enhanced plugins keep the 增强插件 name and now mount into the official plugins tab", () => {
  const pluginSettings = source("src/components/PluginSettings.tsx");
  assert.ok(pluginSettings.includes("返回增强插件"));
  assert.ok(pluginSettings.includes("安装本地增强插件"));
  assert.ok(pluginSettings.includes("还没有增强插件"));
  assert.ok(pluginSettings.includes("启用增强插件"));
  assert.ok(pluginSettings.includes("停用增强插件"));
  assert.ok(pluginSettings.includes("item.id !== PLANNER_PLUGIN_ID"), "planner belongs in lab, not enhanced plugins list");
  assert.equal(pluginSettings.includes("返回插件列表"), false);
  assert.equal(pluginSettings.includes("还没有插件"), false);
  assert.equal(pluginSettings.includes("安装本地插件"), false);

  const officialSections = source("src/components/OfficialSettingsSections.tsx");
  assert.ok(officialSections.includes('data-ekd-feature="planner"'));
  assert.ok(officialSections.includes("「规划」运行异常"));

  const pluginsPage = source("src/pages/PluginsPage.tsx");
  assert.ok(pluginsPage.includes('title="增强插件"'));

  const routes = source("src/routes.ts");
  assert.ok(routes.includes('label: "增强插件"'));

  const renderer = source("../internal/desktop/assets/renderer.js");
  assert.match(renderer, /findOfficialSettingsPanel\(\s*document,\s*['"]plugins['"]\s*\)/, "enhanced plugins mount into the official 插件 tab");
  assert.ok(renderer.includes("data-ekd-plugins-tab"), "official plugins toolbar should receive a dedicated KimiCode+ segmented tab");
  assert.ok(renderer.includes("settings-plugins"));
  assert.ok(renderer.includes("settings-cache"));
  assert.ok(renderer.includes("缓存与用量"));
  assert.ok(renderer.includes(".upd-pill") || renderer.includes("upd-pill"));
  assert.ok(renderer.includes("settings-account"));
  assert.ok(renderer.includes("settings-agent-permissions"));
  assert.ok(renderer.includes("settings-agent-subagent"));
  assert.ok(renderer.includes("settings-lab"));
});

test("resolveSettingsFallbacks activates only disabled official tab probes and keeps KimiCode+ fallback copy", () => {
  const healthy = resolveSettingsFallbacks([
    { id: "dom.settings_account", status: "available" },
    { id: "dom.settings_agent", status: "untested" },
    { id: "dom.settings_plugins", status: "available" },
    { id: "dom.settings_lab", status: "available" },
  ]);
  assert.equal(healthy.account, null);
  assert.equal(healthy.agent, null);
  assert.equal(healthy.plugins, null);
  assert.equal(healthy.lab, null);

  const degraded = resolveSettingsFallbacks([
    { id: "dom.settings_account", status: "disabled", reason: "账户页结构变化" },
    { id: "dom.settings_agent", status: "available" },
    { id: "dom.settings_plugins", status: "disabled", reason: "插件页结构变化" },
    { id: "dom.settings_lab", status: "disabled", reason: "实验室页结构变化" },
  ]);
  assert.equal(degraded.account?.reason, "账户页结构变化");
  assert.equal(degraded.agent, null);
  assert.equal(degraded.plugins?.reason, "插件页结构变化");
  assert.equal(degraded.lab?.reason, "实验室页结构变化");

  const officialSections = source("src/components/OfficialSettingsSections.tsx");
  assert.ok(officialSections.includes("官方页面结构已变，暂时显示在这里"));
  assert.ok(officialSections.includes("export function PluginsSettingsSection"), "enhanced plugins need a mount point for the official 插件 tab");
  assert.ok(officialSections.includes("export function LabSettingsSection"));
  const lab = officialSections.slice(officialSections.indexOf("export function LabSettingsSection"));
  assert.ok(lab.includes("multiSession"), "lab must own the 会话并排 switch");
  assert.ok(lab.includes("planner"), "lab must own the 规划器 switch");
  const surface = source("src/components/SettingsSurface.tsx");
  assert.ok(surface.includes('data-ekd-fallback="account"'));
  assert.ok(surface.includes('data-ekd-fallback="agent"'));
  assert.ok(surface.includes('data-ekd-fallback="plugins"'));
  assert.ok(surface.includes('data-ekd-fallback="lab"'));
  assert.ok(surface.includes("<PluginsSettingsSection"));
});

test("findOfficialSettingsPanel ignores hidden v-show panels and locates official agent sections", () => {
  const doc = new FakeDocument();
  const dialog = doc.createElement("div");
  dialog.className = "settings-dialog";
  dialog.setAttribute("role", "dialog");

  const list = doc.createElement("div");
  list.className = "settings-tab-list";
  list.setAttribute("role", "tablist");
  for (const [id, label] of [
    ["general", "通用"],
    ["account", "账户"],
    ["agent", "智能体与会话"],
    ["lab", "实验室"],
  ]) {
    const tab = doc.createElement("button");
    tab.className = id === "agent" ? "tab on" : "tab";
    tab.setAttribute("role", "tab");
    tab.setAttribute("aria-selected", id === "agent" ? "true" : "false");
    const span = doc.createElement("span");
    span.textContent = label;
    tab.append(span);
    list.append(tab);
  }

  const region = doc.createElement("section");
  region.className = "settings-region";
  const body = doc.createElement("div");
  body.className = "body";

  // Hidden general panel (appears FIRST in DOM, matching 1.3.7 v-show order)
  // Include a nested .panel-sections child to prove hidden ancestors exclude descendants.
  const generalPanel = doc.createElement("section");
  generalPanel.className = "panel";
  generalPanel.setAttribute("style", "display: none;");
  generalPanel.setAttribute("data-panel", "general");
  const generalNested = doc.createElement("div");
  generalNested.className = "panel-sections";
  generalPanel.append(generalNested);
  body.append(generalPanel);

  // Hidden account panel
  const accountPanel = doc.createElement("section");
  accountPanel.className = "panel";
  accountPanel.setAttribute("style", "display: none;");
  accountPanel.setAttribute("data-panel", "account");
  body.append(accountPanel);

  // Visible agent panel with official sections
  const agentPanel = doc.createElement("section");
  agentPanel.className = "panel";
  agentPanel.setAttribute("data-panel", "agent");
  const defaultsSec = doc.createElement("section");
  defaultsSec.className = "sec";
  const defaultsH3 = doc.createElement("h3");
  defaultsH3.className = "sec-title";
  defaultsH3.textContent = "Agent 默认值";
  defaultsSec.append(defaultsH3);

  const subagentSec = doc.createElement("section");
  subagentSec.className = "sec";
  const subagentH3 = doc.createElement("h3");
  subagentH3.className = "sec-title";
  subagentH3.textContent = "子智能体";
  subagentSec.append(subagentH3);

  agentPanel.append(defaultsSec, subagentSec);
  body.append(agentPanel);

  // Hidden lab panel
  const labPanel = doc.createElement("section");
  labPanel.className = "panel";
  labPanel.setAttribute("style", "display: none;");
  labPanel.setAttribute("data-panel", "lab");
  body.append(labPanel);

  region.append(body);
  dialog.append(list, region);
  doc.body.append(dialog);

  const resolved = findOfficialSettingsPanel(doc as unknown as Document, "agent");
  assert.equal(resolved, agentPanel, "must return visible agent panel, not hidden general panel");
  assert.equal(
    findOfficialSettingsSection(agentPanel as unknown as Element, doc as unknown as Document, "settings.agentDefaults"),
    defaultsSec,
  );
  assert.equal(
    findOfficialSettingsSection(agentPanel as unknown as Element, doc as unknown as Document, "settings.secondaryModelSection"),
    subagentSec,
  );

  const evaluation = evaluateDOMContracts(doc as unknown as Document);
  assert.equal(evaluation.byId["dom.settings_agent"]?.status, "available");
});

test("embedded plugin settings space actions from list, align row status columns, and explain unverified host versions", () => {
  const pluginSurfacesCss = source("src/styles/plugin-surfaces.css");
  assert.match(pluginSurfacesCss, /\.ekds-plugins\s*\{[^}]*display:\s*flex[^}]*flex-direction:\s*column[^}]*gap:\s*14px/s);

  const pagesCss = source("src/styles/pages.css");
  assert.match(pagesCss, /\.plugin-open\s*>\s*\.state\s*\{[^}]*min-width:\s*68px/s);
  assert.match(pagesCss, /\.plugin-count\s*\{[^}]*min-width:\s*5\.5em[^}]*text-align:\s*right/s);

  const surface = source("src/components/SettingsSurface.tsx");
  assert.ok(surface.includes("自动兼容"));
  assert.ok(surface.includes("无需额外操作"));
});
