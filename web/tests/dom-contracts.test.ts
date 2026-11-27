import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  createDOMContractState,
  evaluateDOMContracts,
  findChatHeaderTail,
  findOfficialSettingsPanel,
  findOfficialSettingsTab,
  findSidebarToggleButton,
  hasActiveSettingsSubpage,
  resolveSettingsFallbacks,
} from "../src/plugin-runtime/contracts.js";
import { createEntryMountManager, type EntryPlan, type SidebarNavPlan } from "../src/plugin-runtime/entries.js";
import { FakeDocument, FakeElement } from "./helpers/fake-dom.js";

const ICON = "M12 3.5a8.5 8.5 0 1 0 0 17 8.5 8.5 0 0 0 0-17Z";

function makeButton(doc: FakeDocument, className: string, label: string, attrs: Record<string, string> = {}): FakeElement {
  const button = doc.createElement("button");
  button.className = className;
  for (const [k, v] of Object.entries(attrs)) button.setAttribute(k, v);
  const svg = doc.createElement("svg");
  const span = doc.createElement("span");
  span.className = "sit-name";
  span.textContent = label;
  button.append(svg, span);
  return button;
}

interface SnapshotOptions {
  locale?: "zh" | "en";
  sidebarMode?: "standard" | "multi-tab";
  renameNewChatClass?: boolean;
  includeSettings?: boolean;
  activeTab?: "account" | "agent" | "providers" | "plugins" | "lab";
  omitTabs?: Array<"account" | "agent" | "providers" | "plugins" | "lab">;
  includeAccountMenu?: boolean;
}

function buildOfficialSnapshot(opts: SnapshotOptions = {}): {
  doc: FakeDocument;
  sessionInfo: FakeElement;
  availableSection: FakeElement;
  rightPanel: FakeElement;
  sidebarFooter: FakeElement;
} {
  const zh = (opts.locale ?? "zh") === "zh";
  const doc = new FakeDocument();
  const app = doc.createElement("div");
  app.id = "app";
  doc.body.append(app);

  // Left sidebar (standard or lab multi-tab)
  const side = doc.createElement("div");
  side.className = "side";
  const col = doc.createElement("div");
  col.className = "col";
  const actions = doc.createElement("div");
  actions.className = "sidebar-actions";
  const newChatClass = opts.renameNewChatClass ? "primary-create-btn" : "btn-new-chat";
  const newChat = makeButton(doc, newChatClass, zh ? "新建会话" : "New Session");
  actions.append(newChat);
  col.append(actions);

  if (opts.sidebarMode === "multi-tab") {
    const statusTabs = doc.createElement("div");
    statusTabs.className = "status-tabs";
    col.append(statusTabs);
  }

  const footer = doc.createElement("div");
  footer.className = "side-footer";
  const settingsTrigger = makeButton(doc, "side-footer-settings", zh ? "设置" : "Settings");
  footer.append(settingsTrigger);
  if (opts.includeAccountMenu !== false) {
    const accountWrap = doc.createElement("div");
    accountWrap.className = "side-footer-account";
    const trigger = makeButton(doc, "user-menu-trigger", "Demo User");
    accountWrap.append(trigger);
    footer.append(accountWrap);
  }
  col.append(footer);
  side.append(col);
  app.append(side);

  // Session info sidebar
  const headerToggle = doc.createElement("button");
  headerToggle.className = "ch-session-info open";
  headerToggle.setAttribute("aria-pressed", "true");
  app.append(headerToggle);

  const sessionInfo = doc.createElement("aside");
  sessionInfo.className = "session-info is-sidebar";
  sessionInfo.setAttribute("role", "complementary");
  sessionInfo.setAttribute("aria-label", zh ? "会话信息" : "Session Info");
  const sit = doc.createElement("div");
  sit.className = "sit";
  const availableSection = doc.createElement("section");
  availableSection.className = "sit-section";
  const availLabel = doc.createElement("div");
  availLabel.className = "sit-label";
  availLabel.textContent = zh ? "可打开" : "Available to open";
  availableSection.append(
    availLabel,
    makeButton(doc, "sit-link sit-available", zh ? "侧边聊天" : "Side Chat"),
  );
  sit.append(availableSection);
  sessionInfo.append(sit);
  app.append(sessionInfo);

  // Right panel
  const ptShell = doc.createElement("div");
  ptShell.className = "pt-shell";
  const ptBody = doc.createElement("div");
  ptBody.className = "pt-body";
  const launcher = doc.createElement("div");
  launcher.className = "pl";
  launcher.append(makeButton(doc, "ui-menu-item", zh ? "侧边聊天" : "Side Chat"));
  ptBody.append(launcher);
  ptShell.append(ptBody);
  app.append(ptShell);

  // Settings dialog (optional)
  if (opts.includeSettings) {
    const dialog = doc.createElement("div");
    dialog.className = "settings-dialog";
    dialog.setAttribute("role", "dialog");
    const list = doc.createElement("div");
    list.className = "settings-tab-list";
    list.setAttribute("role", "tablist");

    const defs: Array<{ key: "account" | "agent" | "providers" | "plugins" | "lab" | "archived"; zh: string; en: string }> = [
      { key: "account", zh: "账户", en: "Account" },
      { key: "agent", zh: "智能体与会话", en: "Agent & Sessions" },
      { key: "providers", zh: "模型供应商", en: "Model Providers" },
      { key: "plugins", zh: "插件", en: "Plugins" },
      { key: "lab", zh: "实验室", en: "Lab" },
      { key: "archived", zh: "已归档的会话", en: "Archived Sessions" },
    ];
    for (const def of defs) {
      if (opts.omitTabs?.includes(def.key as "account" | "agent" | "providers" | "plugins" | "lab")) continue;
      const selected = opts.activeTab === def.key;
      const tab = makeButton(doc, selected ? "tab on" : "tab", zh ? def.zh : def.en, {
        role: "tab",
        "aria-selected": String(selected),
      });
      list.append(tab);
    }

    const region = doc.createElement("section");
    region.className = "settings-region";
    const body = doc.createElement("div");
    body.className = "body";
    if (opts.activeTab === "providers") {
      const form = doc.createElement("div");
      form.className = "pf";
      const table = doc.createElement("div");
      table.className = "pmt";
      const row = doc.createElement("div");
      row.className = "pmt-grid";
      table.append(row);
      form.append(table);
      body.append(form);
    } else if (opts.activeTab === "plugins") {
      const panel = doc.createElement("section");
      panel.className = "plugins-panel";
      body.append(panel);
    } else if (opts.activeTab) {
      const panel = doc.createElement("div");
      panel.className = "panel";
      const sections = doc.createElement("div");
      sections.className = "panel-sections";
      panel.append(sections);
      body.append(panel);
    }
    region.append(body);
    dialog.append(list, region);
    app.append(dialog);
  }

  return { doc, sessionInfo, availableSection, rightPanel: ptBody, sidebarFooter: footer };
}

test("DOM contract probes pass across 1.3.4/1.3.5/1.3.7/unknown snapshots in zh and en", () => {
  for (const locale of ["zh", "en"] as const) {
    let state = createDOMContractState();
    for (const activeTab of ["account", "agent", "providers", "plugins", "lab"] as const) {
      const { doc } = buildOfficialSnapshot({ locale, includeSettings: true, activeTab });
      const evaluation = evaluateDOMContracts(doc as unknown as Document, state);
      state = evaluation.state;
      assert.ok(findOfficialSettingsTab(doc as unknown as Document, activeTab));
      assert.ok(findOfficialSettingsPanel(doc as unknown as Document, activeTab));
    }
    const { doc } = buildOfficialSnapshot({ locale, includeSettings: true, activeTab: "account" });
    const finalEval = evaluateDOMContracts(doc as unknown as Document, state);
    for (const id of [
      "dom.session_info_available",
      "dom.right_panel",
      "dom.sidebar_nav",
      "dom.settings_dialog",
      "dom.settings_account",
      "dom.settings_agent",
      "dom.settings_providers",
      "dom.settings_plugins",
      "dom.settings_lab",
      "dom.sidebar_footer",
      "dom.account_menu",
    ]) {
      assert.equal(finalEval.byId[id]?.status, "available", `${locale}: probe ${id} should be available`);
    }
  }
});

test("sidebar probe and EntryMountManager cover standard, lab multi-tab, and renamed-class localized modes", () => {
  const nav: SidebarNavPlan = { action: "sidebar:scheduler:main", title: "定时任务", icon: ICON, open: () => {} };

  for (const scenario of [
    { sidebarMode: "standard" as const, locale: "zh" as const, renameNewChatClass: false, expectMode: "standard" as const },
    { sidebarMode: "multi-tab" as const, locale: "zh" as const, renameNewChatClass: false, expectMode: "multi-tab" as const },
    { sidebarMode: "multi-tab" as const, locale: "en" as const, renameNewChatClass: true, expectMode: "multi-tab" as const },
  ]) {
    const { doc } = buildOfficialSnapshot(scenario);
    const evaluation = evaluateDOMContracts(doc as unknown as Document);
    assert.equal(evaluation.sidebarMode, scenario.expectMode);
    assert.equal(evaluation.byId["dom.sidebar_nav"]?.status, "available");

    const manager = createEntryMountManager({ document: doc as unknown as Document });
    manager.sync([], [nav], { sidebarNav: true });
    assert.equal(manager.stats().navs, 1);
  }
});

test("removing session-info group disables only session-info entry while preserving other surfaces", () => {
  const { doc, sessionInfo, rightPanel } = buildOfficialSnapshot({ includeSettings: true, activeTab: "account" });
  const manager = createEntryMountManager({ document: doc as unknown as Document });
  const entry: EntryPlan = { action: "plugin:planner:main", title: "规划", icon: ICON, open: () => {} };
  const nav: SidebarNavPlan = { action: "sidebar:scheduler:main", title: "定时任务", icon: ICON, open: () => {} };

  const gatesOf = (ev: ReturnType<typeof evaluateDOMContracts>) => ({
    sessionInfo: ev.byId["dom.session_info_available"]?.status !== "disabled",
    rightPanel: ev.byId["dom.right_panel"]?.status !== "disabled",
    sidebarNav: ev.byId["dom.sidebar_nav"]?.status !== "disabled",
  });

  const eval1 = evaluateDOMContracts(doc as unknown as Document);
  assert.equal(eval1.byId["dom.session_info_available"]?.status, "available");
  manager.sync([entry], [nav], gatesOf(eval1));
  assert.equal(sessionInfo.querySelectorAll("[data-ekd-entry]").length, 1);
  assert.equal(rightPanel.querySelectorAll("[data-ekd-entry]").length, 1);
  assert.equal(manager.stats().navs, 1);

  // Delete the session-info "available" section at runtime.
  sessionInfo.remove();
  const eval2 = evaluateDOMContracts(doc as unknown as Document, eval1.state);
  assert.equal(eval2.byId["dom.session_info_available"]?.status, "disabled");
  assert.equal(eval2.byId["dom.right_panel"]?.status, "available");
  assert.equal(eval2.byId["dom.sidebar_nav"]?.status, "available");
  assert.equal(eval2.byId["dom.sidebar_footer"]?.status, "available");
  assert.equal(eval2.byId["dom.settings_dialog"]?.status, "available");

  manager.sync([entry], [nav], gatesOf(eval2));
  assert.equal(rightPanel.querySelectorAll("[data-ekd-entry]").length, 1);
  assert.equal(manager.stats().navs, 1);
});

test("omitting a single official settings tab marks only that tab probe disabled", () => {
  const { doc } = buildOfficialSnapshot({ includeSettings: true, activeTab: "account", omitTabs: ["lab"] });
  const evaluation = evaluateDOMContracts(doc as unknown as Document);
  assert.equal(evaluation.byId["dom.settings_dialog"]?.status, "available");
  assert.equal(evaluation.byId["dom.settings_account"]?.status, "available");
  assert.equal(evaluation.byId["dom.settings_lab"]?.status, "disabled");
  assert.equal(evaluation.byId["dom.settings_agent"]?.status, "untested");
  assert.equal(evaluation.byId["dom.settings_providers"]?.status, "untested");
});

test("plugins tab probes the official plugins panel and feeds the KimiCode+ fallback", () => {
  const { doc } = buildOfficialSnapshot({ includeSettings: true, activeTab: "plugins" });
  const evaluation = evaluateDOMContracts(doc as unknown as Document);
  assert.equal(evaluation.byId["dom.settings_plugins"]?.status, "available");
  assert.equal(evaluation.byId["dom.settings_plugins"]?.label, "官方「插件」设置页内容区");
  const panel = findOfficialSettingsPanel(doc as unknown as Document, "plugins");
  assert.ok(panel?.classList.contains("plugins-panel"), "plugins panel should resolve to .plugins-panel");
});

test("a missing plugins tab disables only the plugins insertion and its fallback", () => {
  const { doc } = buildOfficialSnapshot({ includeSettings: true, activeTab: "account", omitTabs: ["plugins"] });
  const evaluation = evaluateDOMContracts(doc as unknown as Document);
  assert.equal(evaluation.byId["dom.settings_plugins"]?.status, "disabled");
  assert.equal(evaluation.byId["dom.settings_account"]?.status, "available");
  assert.equal(evaluation.byId["dom.settings_dialog"]?.status, "available");

  const fallbacks = resolveSettingsFallbacks(evaluation.probes);
  assert.equal(fallbacks.plugins?.id, "dom.settings_plugins");
  assert.equal(fallbacks.account, null);
  assert.equal(fallbacks.plugins?.reason?.includes("插件"), true);
});

test("settings fallbacks keep plugins null while the official panel is healthy", () => {
  const { doc } = buildOfficialSnapshot({ includeSettings: true, activeTab: "plugins" });
  const evaluation = evaluateDOMContracts(doc as unknown as Document);
  assert.equal(resolveSettingsFallbacks(evaluation.probes).plugins, null);
});

test("findChatHeaderTail prefers the chat header and falls back to a loose .ch-tail", () => {
  const loose = new FakeDocument();
  const side = loose.createElement("div");
  side.className = "side";
  const strayTail = loose.createElement("div");
  strayTail.className = "ch-tail";
  side.append(strayTail);
  loose.body.append(side);
  assert.equal(findChatHeaderTail(loose as unknown as Document), strayTail, "querySelector fallback covers a loose .ch-tail");

  const headed = new FakeDocument();
  const header = headed.createElement("header");
  header.className = "chat-header";
  const tail = headed.createElement("div");
  tail.className = "ch-tail";
  header.append(tail);
  headed.body.append(header);
  assert.equal(findChatHeaderTail(headed as unknown as Document), tail, "header.chat-header .ch-tail wins");

  const chat = new FakeDocument();
  const chatRoot = chat.createElement("div");
  chatRoot.className = "chat";
  const chatTail = chat.createElement("div");
  chatTail.className = "ch-tail";
  chatRoot.append(chatTail);
  chat.body.append(chatRoot);
  assert.equal(findChatHeaderTail(chat as unknown as Document), chatTail, ".ch .ch-tail covers hosts without a chat header");

  const empty = new FakeDocument();
  const unrelated = empty.createElement("div");
  unrelated.className = "side-footer";
  empty.body.append(unrelated);
  assert.equal(findChatHeaderTail(empty as unknown as Document), null);
});

test("missing native composer radius reports a style fallback without gating the pane", () => {
  const renderer = readFileSync("../internal/desktop/assets/renderer.js", "utf8");
  const start = renderer.indexOf("function composerStyleProbe(composer)");
  const end = renderer.indexOf("function syncSessionHeader()", start);
  assert.ok(start >= 0 && end > start);
  const run = new Function("getComputedStyle", `${renderer.slice(start, end)}\nreturn composerStyleProbe;`);
  const probe = run((composer: { radius: string }) => ({
    getPropertyValue(name: string) { return name === "--radius-composer" ? composer.radius : ""; },
  }));
  assert.equal(probe(null).status, "untested");
  assert.deepEqual(probe({ radius: " 32px " }), {
    id: "dom.composer_style", group: "dom", label: "同屏输入框原生主题", status: "available", detail: "--radius-composer: 32px",
  });
  assert.equal(probe({ radius: "" }).status, "disabled");
  assert.match(probe({ radius: "" }).reason, /内置样式/);
  assert.match(renderer, /scheduleContractReport\(\[\.\.\.evaluation\.probes, composerStyleProbe\(composer\)\]/);
});

test("the same pane host follows the current header size and releases replaced observers", () => {
  const renderer = readFileSync("../internal/desktop/assets/renderer.js", "utf8");
  const start = renderer.indexOf("function syncSessionHeader()");
  const end = renderer.indexOf("function closeSessions()", start);
  assert.ok(start >= 0 && end > start);
  const changes: string[] = [];
  let header = { height: 46, getBoundingClientRect() { return { height: this.height }; } };
  let observer: FakeResizeObserver | undefined;
  class FakeResizeObserver {
    connected = false;
    constructor(readonly onResize: () => void) { observer = this; }
    observe(target: typeof header) { assert.equal(target, header); this.connected = true; }
    disconnect() { this.connected = false; }
  }
  const state = { sessionHostElement: { style: { setProperty(name: string, value: string) { changes.push(`${name}:${value}`); } } }, sessionHeader: null, sessionHeaderObserver: null };
  const run = new Function("document", "state", "ResizeObserver", `${renderer.slice(start, end)}\nreturn syncSessionHeader;`);
  const sync = run({ querySelector(selector: string) { assert.equal(selector, "header.chat-header"); return header; } }, state, FakeResizeObserver);
  sync();
  const first = observer!;
  header.height = 51;
  first.onResize();
  header = { ...header, height: 60 };
  sync();
  assert.equal(first.connected, false);
  assert.equal(observer?.connected, true);
  assert.deepEqual(changes, ["--ekd-header-h:46px", "--ekd-header-h:51px", "--ekd-header-h:60px"]);
});

test("1.5.0 providers edit form probe uses .pf-nav/.pf-head-sticky, supports .ap, detects subpages and multi-layout sidebar toggles", () => {
  // 1. 1.5.0 edit form without .pf-back: missing .pmt must mark dom.settings_providers disabled
  const { doc } = buildOfficialSnapshot({ locale: "en", includeSettings: true, activeTab: "providers" });
  const body = doc.querySelector(".settings-region .body")!;
  body.replaceChildren();
  const ap = doc.createElement("div");
  ap.className = "ap";
  const hiddenPf = doc.createElement("div");
  hiddenPf.className = "pf";
  hiddenPf.setAttribute("style", "display: none;");
  ap.append(hiddenPf);
  const brokenEdit = doc.createElement("div");
  brokenEdit.className = "pf";
  const brand = doc.createElement("div");
  brand.className = "pf-brand pf-head-sticky";
  const nav = doc.createElement("button");
  nav.className = "pf-nav";
  brand.append(nav);
  brokenEdit.append(brand);
  body.append(ap, brokenEdit);

  const disabledEval = evaluateDOMContracts(doc as unknown as Document);
  assert.equal(disabledEval.byId["dom.settings_providers"]?.status, "disabled");

  // Adding .pmt + .pmt-grid restores available status
  const pmt = doc.createElement("div");
  pmt.className = "pmt";
  const row = doc.createElement("div");
  row.className = "pmt-grid";
  pmt.append(row);
  brokenEdit.append(pmt);
  const healthyEval = evaluateDOMContracts(doc as unknown as Document);
  assert.equal(healthyEval.byId["dom.settings_providers"]?.status, "available");
  assert.match(healthyEval.byId["dom.settings_providers"]?.detail ?? "", /模型行/);

  // AddProviderPage (.ap) alone keeps panel discoverable and probe available
  brokenEdit.remove();
  assert.equal(findOfficialSettingsPanel(doc as unknown as Document, "providers"), ap);
  assert.equal(evaluateDOMContracts(doc as unknown as Document).byId["dom.settings_providers"]?.status, "available");

  // 2. SettingsSubpage visibility detection
  const panel = doc.createElement("section");
  panel.className = "panel";
  const subpage = doc.createElement("section");
  subpage.className = "settings-subpage";
  subpage.setAttribute("style", "display: none;");
  panel.append(subpage);
  assert.equal(hasActiveSettingsSubpage(panel as unknown as Element), false);
  subpage.setAttribute("style", "");
  assert.equal(hasActiveSettingsSubpage(panel as unknown as Element), true);

  // 3. Multi-layout & i18n sidebar toggle discovery
  const toggleDoc = new FakeDocument();
  const winCollapse = toggleDoc.createElement("button");
  winCollapse.className = "custom-sidebar-toggle";
  winCollapse.setAttribute("aria-label", "Collapse sidebar");
  toggleDoc.body.append(winCollapse);
  assert.equal(findSidebarToggleButton(toggleDoc as unknown as Document, "collapse"), winCollapse);
  assert.equal(findSidebarToggleButton(toggleDoc as unknown as Document, "expand"), null);

  winCollapse.setAttribute("aria-label", "Expand sidebar");
  assert.equal(findSidebarToggleButton(toggleDoc as unknown as Document, "expand"), winCollapse);

  winCollapse.remove();
  const sideCollapse = toggleDoc.createElement("button");
  sideCollapse.className = "ch-collapse";
  sideCollapse.setAttribute("aria-label", "收起侧边栏");
  toggleDoc.body.append(sideCollapse);
  assert.equal(findSidebarToggleButton(toggleDoc as unknown as Document, "collapse"), sideCollapse);
  assert.equal(findSidebarToggleButton(toggleDoc as unknown as Document, "expand"), null);
});

test("renderer.js placeOfficialSection inserts inside nested 1.5.0 wrappers and acquireSidebarCollapse uses localized fallback", () => {
  const renderer = readFileSync("../internal/desktop/assets/renderer.js", "utf8");
  const placeStart = renderer.indexOf("function placeOfficialSection(");
  const placeEnd = renderer.indexOf("function hasHiddenOfficialSectionsOnly(", placeStart);
  assert.ok(placeStart >= 0 && placeEnd > placeStart);
  const doc = new FakeDocument();
  const panel = doc.createElement("section");
  panel.className = "panel";
  const wrap = doc.createElement("div");
  const anchor = doc.createElement("section");
  anchor.className = "sec";
  wrap.append(anchor);
  const footer = doc.createElement("div");
  panel.append(wrap, footer);
  doc.body.append(panel);

  const state = { officialSections: new Map() };
  const makePlace = new Function(
    "document", "state", "mount", "makeHost",
    `${renderer.slice(placeStart, placeEnd)}\nreturn placeOfficialSection;`,
  );
  const placeOfficialSection = makePlace(doc, state, () => ({ unmount() {} }), () => ({}));
  const entry = placeOfficialSection(panel, "agent-permissions", "settings-agent-permissions", { kind: "after", anchor });
  assert.equal(entry.hostEl.parentElement, wrap, "must insert inside nested wrapper div rather than falling back to panel bottom");
  assert.equal(anchor.nextElementSibling, entry.hostEl);

  const sidebarStart = renderer.indexOf("function acquireSidebarCollapse()");
  const sidebarEnd = renderer.indexOf("function composerStyleProbe(", sidebarStart);
  assert.ok(sidebarStart >= 0 && sidebarEnd > sidebarStart);
  const clicks: string[] = [];
  const collapseBtn = { click() { clicks.push("collapse"); } };
  const expandBtn = { click() { clicks.push("expand"); } };
  const sidebarState = { sidebarRefs: 0, sidebarCollapsed: null };
  const makeAcquire = new Function(
    "document", "state", "module",
    `${renderer.slice(sidebarStart, sidebarEnd)}\nreturn acquireSidebarCollapse;`,
  );
  const acquireSidebarCollapse = makeAcquire(doc, sidebarState, {
    findSidebarToggleButton(_doc: unknown, action: string) {
      return action === "collapse" ? collapseBtn : expandBtn;
    },
  });
  const release = acquireSidebarCollapse();
  release();
  assert.deepEqual(clicks, ["collapse", "expand"]);
});
