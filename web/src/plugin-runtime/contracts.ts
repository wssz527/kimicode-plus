export type ContractProbeStatus = "available" | "disabled" | "untested";

export type OfficialSettingsTabKey =
  | "general"
  | "account"
  | "agent"
  | "providers"
  | "plugins"
  | "lab"
  | "archived";

export interface DOMContractProbe {
  id: string;
  group: "dom";
  label: string;
  status: ContractProbeStatus;
  reason?: string;
  detail?: string;
  checkedAt?: string;
}

export interface DOMContractState {
  seen: Record<string, boolean>;
  disabledReasons: Record<string, string>;
}

export interface DOMContractEvaluation {
  probes: DOMContractProbe[];
  byId: Record<string, DOMContractProbe>;
  state: DOMContractState;
  sidebarMode: "standard" | "multi-tab" | "unknown";
}

export interface SettingsDialogParts {
  dialog: Element;
  tabList: Element | null;
  body: Element | null;
  officialTabs: Element[];
}

const ENTRY_ATTRIBUTE = "data-ekd-entry";
const NAV_ATTRIBUTE = "data-ekd-sidebar-nav";
const SETTINGS_TAB_ATTRIBUTE = "data-ekd-settings-tab";
const OFFICIAL_SECTION_ATTRIBUTE = "data-ekd-official-section";
const SETTINGS_MOUNT_CLASS = "ekd-settings-mount";

const OFFICIAL_KEY_FALLBACKS: Record<string, readonly string[]> = {
  "sidebar.newChat": ["新建会话", "New Session", "New Chat"],
  "header.sessionInfo": ["会话信息", "Session Info"],
  "header.sessionInfoAvailableTabs": ["可打开", "Available to open"],
  "header.toggleSideChat": ["侧边聊天", "Side chat", "Side Chat"],
  "settings.tabs.general": ["通用", "General"],
  "settings.tabs.account": ["账户", "Account"],
  "settings.tabs.agent": ["智能体与会话", "Agent & Sessions"],
  "settings.tabs.providers": ["模型供应商", "Model Providers"],
  "settings.tabs.plugins": ["插件", "Plugins"],
  "settings.tabs.lab": ["实验室", "Lab"],
  "settings.tabs.archived": ["已归档的会话", "已归档", "Archived Sessions", "Archived"],
  "settings.agentDefaults": ["Agent 默认值", "Agent Defaults", "智能体默认值"],
  "settings.secondaryModelSection": ["子智能体", "Subagents", "Secondary Model"],
  "settings.title": ["设置", "Settings"],
};

const TAB_PROBE_META: Record<
  Exclude<OfficialSettingsTabKey, "archived" | "general">,
  { id: string; label: string; title: string }
> = {
  account: { id: "dom.settings_account", label: "官方「账户」设置页内容区", title: "账户" },
  agent: { id: "dom.settings_agent", label: "官方「智能体与会话」设置页内容区", title: "智能体与会话" },
  providers: { id: "dom.settings_providers", label: "官方「模型供应商」设置页与模型行", title: "模型供应商" },
  plugins: { id: "dom.settings_plugins", label: "官方「插件」设置页内容区", title: "插件" },
  lab: { id: "dom.settings_lab", label: "官方「实验室」设置页内容区", title: "实验室" },
};

const PROBED_SETTINGS_TABS = ["account", "agent", "providers", "plugins", "lab"] as const;

export function createDOMContractState(): DOMContractState {
  return { seen: {}, disabledReasons: {} };
}

function normalizeLabel(value: string | null | undefined): string {
  return (value ?? "").replace(/\s+/g, " ").trim().toLowerCase();
}

function elementText(element: Element | null | undefined): string {
  if (!element) return "";
  const own = (element.textContent ?? "").trim();
  if (own) return own;
  const parts: string[] = [];
  for (const child of Array.from(element.children ?? [])) {
    const text = elementText(child);
    if (text) parts.push(text);
  }
  return parts.join(" ").trim();
}

function readVueTranslator(document: Document): ((key: string) => string | undefined) | null {
  try {
    const root = document.querySelector("#app") as (Element & {
      __vue_app__?: {
        config?: { globalProperties?: { $t?: (key: string) => unknown } };
      };
    }) | null;
    const translator = root?.__vue_app__?.config?.globalProperties?.$t;
    if (typeof translator === "function") {
      return (key: string) => {
        try {
          const translated = translator(key);
          if (typeof translated === "string" && translated.trim() && translated !== key) {
            return translated.trim();
          }
        } catch {
          // ignore i18n lookup failures on synthetic or evolving hosts
        }
        return undefined;
      };
    }
  } catch {
    // ignore
  }
  return null;
}

export function resolveOfficialTexts(document: Document, key: string): string[] {
  const candidates: string[] = [];
  const translator = readVueTranslator(document);
  const translated = translator?.(key);
  if (translated) candidates.push(translated);
  for (const fallback of OFFICIAL_KEY_FALLBACKS[key] ?? []) {
    if (!candidates.includes(fallback)) candidates.push(fallback);
  }
  return candidates;
}

function matchesAnyLabel(text: string | null | undefined, candidates: readonly string[]): boolean {
  const normalized = normalizeLabel(text);
  if (!normalized) return false;
  return candidates.some(candidate => {
    const target = normalizeLabel(candidate);
    return target !== "" && (normalized === target || normalized.includes(target));
  });
}

function isOfficialEntryButton(button: Element): boolean {
  if (button.hasAttribute(ENTRY_ATTRIBUTE)) return false;
  if (button.classList.contains("sit-more") || button.classList.contains("sit-opened")) return false;
  return button.classList.contains("sit-link") || button.classList.contains("ui-menu-item");
}

export function findSessionInfoAside(document: Document): Element | null {
  const direct = document.querySelector("aside.session-info");
  if (direct) return direct;
  const labels = resolveOfficialTexts(document, "header.sessionInfo");
  for (const aside of Array.from(document.querySelectorAll("aside"))) {
    if (aside.getAttribute("role") === "complementary" && matchesAnyLabel(aside.getAttribute("aria-label"), labels)) {
      return aside;
    }
  }
  return null;
}

export function findSessionInfoKeepAliveAnchor(document: Document): { aside: Element; folders: Element | null; before: Element | null } | null {
  const aside = findSessionInfoAside(document);
  if (!aside) return null;
  const folders = aside.querySelector(".si-folders");
  const sit = aside.querySelector(".sit");
  return { aside, folders, before: sit };
}

export function findSessionInfoAvailableContainers(document: Document): Element[] {
  const aside = findSessionInfoAside(document);
  if (!aside) return [];
  const availableLabels = resolveOfficialTexts(document, "header.sessionInfoAvailableTabs");
  const containers: Element[] = [];
  const seen = new Set<Element>();
  const add = (node: Element) => {
    if (seen.has(node)) return;
    seen.add(node);
    containers.push(node);
  };
  for (const section of Array.from(aside.querySelectorAll(".sit-section"))) {
    if (section.querySelector(".sit-available")) {
      add(section);
      continue;
    }
    const label = section.querySelector(".sit-label");
    if (label && matchesAnyLabel(elementText(label), availableLabels)) {
      const hasTemplate = Array.from(section.querySelectorAll("button")).some(isOfficialEntryButton);
      if (hasTemplate) add(section);
    }
  }
  return containers;
}

export function findRightPanelContainers(document: Document): Element[] {
  const seen = new Set<Element>();
  const containers: Element[] = [];
  const add = (container: Element) => {
    if (seen.has(container)) return;
    seen.add(container);
    containers.push(container);
  };
  for (const sit of Array.from(document.querySelectorAll(".pt-body .sit"))) {
    const sections = Array.from(sit.querySelectorAll(".sit-section")).filter(section => section.querySelector(".sit-available"));
    if (sections.length > 0) sections.forEach(add);
    else add(sit);
  }
  for (const launcher of Array.from(document.querySelectorAll(".pt-body .pl"))) add(launcher);
  for (const menu of Array.from(document.querySelectorAll(".panel-add-menu"))) add(menu);
  return containers;
}

export function detectSidebarMode(document: Document): "standard" | "multi-tab" | "unknown" {
  if (document.querySelector(".status-tabs, .status-seg")) return "multi-tab";
  if (document.querySelector(".sidebar-actions, .side, button.btn-new-chat")) return "standard";
  return "unknown";
}

export function findSidebarNewChatButtons(document: Document): Element[] {
  const found: Element[] = [];
  const seen = new Set<Element>();
  const accept = (button: Element) => {
    if (!button.parentElement) return;
    if (button.hasAttribute(ENTRY_ATTRIBUTE) || button.hasAttribute(NAV_ATTRIBUTE)) return;
    if (seen.has(button)) return;
    seen.add(button);
    found.push(button);
  };

  for (const button of Array.from(document.querySelectorAll("button.btn-new-chat"))) {
    accept(button);
  }
  if (found.length > 0) return found;

  const newChatLabels = resolveOfficialTexts(document, "sidebar.newChat");
  const roots = Array.from(document.querySelectorAll(".sidebar-actions, .side, aside, nav"));
  for (const root of roots) {
    if (root.classList.contains("session-info")) continue;
    for (const button of Array.from(root.querySelectorAll("button"))) {
      if (button.hasAttribute(ENTRY_ATTRIBUTE) || button.hasAttribute(NAV_ATTRIBUTE)) continue;
      if (
        matchesAnyLabel(elementText(button), newChatLabels) ||
        matchesAnyLabel(button.getAttribute("aria-label"), newChatLabels) ||
        matchesAnyLabel(button.getAttribute("title"), newChatLabels)
      ) {
        accept(button);
      }
    }
  }
  return found;
}

export function findSettingsDialogParts(document: Document): SettingsDialogParts | null {
  let dialog: Element | null = document.querySelector(".settings-dialog");
  if (!dialog) {
    for (const candidate of Array.from(document.querySelectorAll("[role]"))) {
      if (candidate.getAttribute("role") === "dialog" && candidate.querySelector(".settings-tab-list, .settings-region")) {
        dialog = candidate;
        break;
      }
    }
  }
  if (!dialog) return null;

  let tabList: Element | null = dialog.querySelector(".settings-tab-list");
  if (!tabList) {
    tabList = Array.from(dialog.querySelectorAll("[role]")).find(node => node.getAttribute("role") === "tablist") ?? null;
  }
  const body = dialog.querySelector("section.settings-region .body")
    ?? dialog.querySelector(".settings-region .body")
    ?? null;
  const officialTabs = tabList
    ? Array.from(tabList.querySelectorAll("button")).filter(button => {
      if (button.hasAttribute(SETTINGS_TAB_ATTRIBUTE)) return false;
      return button.getAttribute("role") === "tab" || button.classList.contains("tab");
    })
    : [];
  return { dialog, tabList, body, officialTabs };
}

export function findChatHeaderTail(document: Document): Element | null {
  const nested = document.querySelector("header.chat-header .ch-tail") ?? document.querySelector(".ch .ch-tail");
  if (nested) return nested;
  return document.querySelector(".ch-tail");
}

export function findOfficialSettingsTab(document: Document, key: OfficialSettingsTabKey): Element | null {
  const parts = findSettingsDialogParts(document);
  if (!parts) return null;
  const labels = resolveOfficialTexts(document, `settings.tabs.${key}`);
  for (const tab of parts.officialTabs) {
    if (
      matchesAnyLabel(elementText(tab), labels) ||
      matchesAnyLabel(tab.getAttribute("aria-label"), labels) ||
      tab.getAttribute("data-tab") === key
    ) {
      return tab;
    }
  }
  return null;
}

export function isOfficialSettingsTabSelected(tab: Element | null): boolean {
  if (!tab) return false;
  return tab.getAttribute("aria-selected") === "true" || tab.classList.contains("on");
}

function isInjectedSettingsNode(element: Element): boolean {
  if (
    element.hasAttribute(OFFICIAL_SECTION_ATTRIBUTE) ||
    element.classList.contains(SETTINGS_MOUNT_CLASS)
  ) {
    return true;
  }
  return Boolean(element.closest(`[${OFFICIAL_SECTION_ATTRIBUTE}], .${SETTINGS_MOUNT_CLASS}`));
}

function isElementHidden(element: Element, stopAt?: Element | null): boolean {
  let current: Element | null = element;
  while (current && current !== stopAt) {
    if (current.hasAttribute("hidden")) return true;
    const inlineDisplay = (current as HTMLElement).style?.display;
    if (inlineDisplay === "none") return true;
    const styleAttr = current.getAttribute("style");
    if (styleAttr && /(?:^|;)\s*display\s*:\s*none\b/i.test(styleAttr)) return true;
    const view = current.ownerDocument?.defaultView;
    if (view && typeof view.getComputedStyle === "function") {
      try {
        if (view.getComputedStyle(current).display === "none") return true;
      } catch {
        // ignore synthetic document errors
      }
    }
    current = current.parentElement;
  }
  return false;
}

function firstVisibleCandidate(container: Element, selectors: readonly string[]): Element | null {
  for (const selector of selectors) {
    for (const candidate of Array.from(container.querySelectorAll(selector))) {
      if (isInjectedSettingsNode(candidate)) continue;
      if (!isElementHidden(candidate, container)) return candidate;
    }
  }
  return null;
}

export function findOfficialSettingsPanel(document: Document, key: Exclude<OfficialSettingsTabKey, "archived">): Element | null {
  const parts = findSettingsDialogParts(document);
  if (!parts?.body) return null;
  const tab = findOfficialSettingsTab(document, key);
  if (!isOfficialSettingsTabSelected(tab)) return null;
  if (key === "providers") {
    return firstVisibleCandidate(parts.body, [".pf", ".panel-sections", ".panel"]);
  }
  if (key === "plugins") {
    return firstVisibleCandidate(parts.body, [".plugins-panel", ".panel", ".panel-sections", ".sec"]);
  }
  return firstVisibleCandidate(parts.body, [".panel", ".panel-sections", ".sec"]);
}

export function findOfficialSettingsSection(
  panel: Element,
  document: Document,
  key: "settings.agentDefaults" | "settings.secondaryModelSection",
): Element | null {
  const candidates = resolveOfficialTexts(document, key);
  for (const section of Array.from(panel.querySelectorAll("section.sec, .sec"))) {
    if (isInjectedSettingsNode(section)) continue;
    if (isElementHidden(section, panel)) continue;
    const heading = section.querySelector(".sec-title, h3, h4");
    if (matchesAnyLabel(elementText(heading), candidates)) {
      return section;
    }
  }
  return null;
}

export function findSidebarFooter(document: Document): Element | null {
  const footer = document.querySelector(".side-footer");
  return footer && footer.parentElement ? footer : null;
}

export function findAccountMenuAnchor(document: Document): Element | null {
  return document.querySelector(".user-menu-trigger")
    ?? document.querySelector(".side-footer-account")
    ?? document.querySelector(".user-menu")
    ?? null;
}

export interface SettingsProbeSummary {
  id: string;
  status: string;
  reason?: string;
  detail?: string;
}

export interface SettingsFallbackState<T extends SettingsProbeSummary = SettingsProbeSummary> {
  account: T | null;
  agent: T | null;
  lab: T | null;
  plugins: T | null;
}

export function resolveSettingsFallbacks<T extends SettingsProbeSummary>(
  probes: readonly T[] | null | undefined,
): SettingsFallbackState<T> {
  const findDisabled = (id: string): T | null => {
    const probe = probes?.find(item => item.id === id) ?? null;
    return probe && probe.status === "disabled" ? probe : null;
  };
  return {
    account: findDisabled("dom.settings_account"),
    agent: findDisabled("dom.settings_agent"),
    lab: findDisabled("dom.settings_lab"),
    plugins: findDisabled("dom.settings_plugins"),
  };
}

export function evaluateDOMContracts(document: Document, previous?: DOMContractState): DOMContractEvaluation {
  const state: DOMContractState = {
    seen: { ...(previous?.seen ?? {}) },
    disabledReasons: { ...(previous?.disabledReasons ?? {}) },
  };
  const checkedAt = new Date().toISOString();
  const probes: DOMContractProbe[] = [];

  const push = (
    id: string,
    label: string,
    status: ContractProbeStatus,
    reason?: string,
    detail?: string,
  ) => {
    const probe: DOMContractProbe = { id, group: "dom", label, status, checkedAt };
    if (reason) probe.reason = reason;
    if (detail) probe.detail = detail;
    probes.push(probe);
  };

  // 1. Session info "available" group
  const sessionInfoAside = findSessionInfoAside(document);
  const sessionInfoContainers = findSessionInfoAvailableContainers(document);
  if (sessionInfoContainers.length > 0) {
    state.seen["dom.session_info_available"] = true;
    delete state.disabledReasons["dom.session_info_available"];
    push("dom.session_info_available", "会话信息「可打开」分组", "available");
  } else if (sessionInfoAside) {
    const reason = "会话信息栏内未找到「可打开」分组，该处插件入口已停用";
    state.disabledReasons["dom.session_info_available"] = reason;
    push("dom.session_info_available", "会话信息「可打开」分组", "disabled", reason);
  } else {
    const toggle = document.querySelector(".ch-session-info");
    const collapsedByOfficial = Boolean(
      toggle && (toggle.hasAttribute("disabled") || toggle.getAttribute("aria-pressed") === "false"),
    );
    if (collapsedByOfficial) {
      if (state.seen["dom.session_info_available"]) {
        push("dom.session_info_available", "会话信息「可打开」分组", "available", undefined, "会话信息栏当前折叠或由右侧面板隐藏");
      } else {
        push("dom.session_info_available", "会话信息「可打开」分组", "untested", "会话信息栏当前未展开");
      }
    } else if (state.seen["dom.session_info_available"] || Boolean(toggle) || Boolean(document.querySelector(".chat"))) {
      const reason = "未找到会话信息「可打开」分组，该处插件入口已停用";
      state.disabledReasons["dom.session_info_available"] = reason;
      push("dom.session_info_available", "会话信息「可打开」分组", "disabled", reason);
    } else {
      push("dom.session_info_available", "会话信息「可打开」分组", "untested", "打开任意会话后检测会话信息栏");
    }
  }

  // 2. Right panel empty list & + menu
  const ptBody = document.querySelector(".pt-body");
  const ptShell = document.querySelector(".pt-shell");
  const rightPanelContainers = findRightPanelContainers(document);
  const hasRightPanelStructure = Boolean(
    ptBody && (ptShell || rightPanelContainers.length > 0 || document.querySelector(".ptb-tabs")),
  );
  if (hasRightPanelStructure) {
    state.seen["dom.right_panel"] = true;
    delete state.disabledReasons["dom.right_panel"];
    push("dom.right_panel", "右侧面板空白列表与 + 菜单", "available");
  } else {
    const reason = "未找到右侧面板容器（.pt-body），右侧面板入口与插件标签已停用";
    state.disabledReasons["dom.right_panel"] = reason;
    push("dom.right_panel", "右侧面板空白列表与 + 菜单", "disabled", reason);
  }

  // 3. Left sidebar "new chat" row (both standard list and lab multi-tab mode)
  const sidebarMode = detectSidebarMode(document);
  const newChatButtons = findSidebarNewChatButtons(document);
  if (newChatButtons.length > 0) {
    state.seen["dom.sidebar_nav"] = true;
    delete state.disabledReasons["dom.sidebar_nav"];
    const detail = sidebarMode === "multi-tab"
      ? "多标签页会话列表模式（进行中 / 已完成 / 项目）"
      : "普通会话列表模式";
    push("dom.sidebar_nav", "左侧栏「新建会话」行", "available", undefined, detail);
  } else {
    const reason = "未找到左侧栏「新建会话」行，侧栏导航入口已停用";
    state.disabledReasons["dom.sidebar_nav"] = reason;
    push("dom.sidebar_nav", "左侧栏「新建会话」行", "disabled", reason);
  }

  // 4. Settings dialog & official tab content panels (used by N6 & provider enhance)
  const settings = findSettingsDialogParts(document);
  if (settings) {
    if (settings.tabList && settings.body && settings.officialTabs.length > 0) {
      state.seen["dom.settings_dialog"] = true;
      delete state.disabledReasons["dom.settings_dialog"];
      push("dom.settings_dialog", "设置弹层与标签栏", "available");
    } else {
      const reason = "设置弹层缺少标签栏或内容区，KimiCode+ 设置入口已停用";
      state.disabledReasons["dom.settings_dialog"] = reason;
      push("dom.settings_dialog", "设置弹层与标签栏", "disabled", reason);
    }

    for (const key of PROBED_SETTINGS_TABS) {
      const meta = TAB_PROBE_META[key];
      if (!settings.tabList || !settings.body) {
        const reason = `设置弹层容器缺失，无法定位「${meta.title}」页`;
        state.disabledReasons[meta.id] = reason;
        push(meta.id, meta.label, "disabled", reason);
        continue;
      }
      const tab = findOfficialSettingsTab(document, key);
      if (!tab) {
        const reason = `设置标签栏中未找到「${meta.title}」标签`;
        state.disabledReasons[meta.id] = reason;
        push(meta.id, meta.label, "disabled", reason);
        continue;
      }
      if (isOfficialSettingsTabSelected(tab)) {
        const panel = findOfficialSettingsPanel(document, key);
        if (key === "providers") {
          const form = settings.body.querySelector(".pf");
          if (form && form.querySelector(".pf-back") && !form.querySelector(".pmt")) {
            const reason = "模型供应商编辑页缺少模型列表（.pmt），行内能力增强已停用";
            state.disabledReasons[meta.id] = reason;
            push(meta.id, meta.label, "disabled", reason);
            continue;
          }
          if (panel) {
            state.seen[meta.id] = true;
            delete state.disabledReasons[meta.id];
            const hasRows = Boolean(settings.body.querySelector(".pf .pmt .pmt-grid"));
            push(meta.id, meta.label, "available", undefined, hasRows ? "已检测到模型供应商表单与模型行" : "已检测到模型供应商设置页内容区");
          } else {
            const reason = "「模型供应商」设置页缺少内容区容器";
            state.disabledReasons[meta.id] = reason;
            push(meta.id, meta.label, "disabled", reason);
          }
        } else if (panel) {
          state.seen[meta.id] = true;
          delete state.disabledReasons[meta.id];
          push(meta.id, meta.label, "available");
        } else {
          const reason = `「${meta.title}」设置页缺少内容区容器`;
          state.disabledReasons[meta.id] = reason;
          push(meta.id, meta.label, "disabled", reason);
        }
      } else if (state.seen[meta.id]) {
        delete state.disabledReasons[meta.id];
        push(meta.id, meta.label, "available", undefined, "已通过检测");
      } else {
        delete state.disabledReasons[meta.id];
        push(meta.id, meta.label, "untested", `已找到「${meta.title}」标签，切入该页后复核内容区`);
      }
    }
  } else {
    if (state.seen["dom.settings_dialog"]) {
      push("dom.settings_dialog", "设置弹层与标签栏", "available", undefined, "已通过检测，当前设置弹层未打开");
    } else if (document.querySelector("button.side-footer-settings, .side-footer, .user-menu-trigger")) {
      push("dom.settings_dialog", "设置弹层与标签栏", "untested", "打开官方设置后自动检测");
    } else {
      const reason = "未找到设置弹层或官方设置入口";
      state.disabledReasons["dom.settings_dialog"] = reason;
      push("dom.settings_dialog", "设置弹层与标签栏", "disabled", reason);
    }

    for (const key of PROBED_SETTINGS_TABS) {
      const meta = TAB_PROBE_META[key];
      if (state.seen[meta.id]) {
        push(meta.id, meta.label, "available", undefined, "已通过检测，当前设置弹层未打开");
      } else if (state.disabledReasons[meta.id]) {
        push(meta.id, meta.label, "disabled", state.disabledReasons[meta.id]);
      } else {
        push(meta.id, meta.label, "untested", "打开官方设置后自动检测");
      }
    }
  }

  // 5. Sidebar footer (Monitor mount point)
  if (findSidebarFooter(document)) {
    state.seen["dom.sidebar_footer"] = true;
    delete state.disabledReasons["dom.sidebar_footer"];
    push("dom.sidebar_footer", "会话侧栏底部（Monitor 挂载位）", "available");
  } else {
    const reason = "未找到侧栏底部（.side-footer），侧栏插件挂载位已停用";
    state.disabledReasons["dom.sidebar_footer"] = reason;
    push("dom.sidebar_footer", "会话侧栏底部（Monitor 挂载位）", "disabled", reason);
  }

  // 6. Official account menu (used by N3)
  const accountAnchor = findAccountMenuAnchor(document);
  if (accountAnchor) {
    state.seen["dom.account_menu"] = true;
    delete state.disabledReasons["dom.account_menu"];
    push(
      "dom.account_menu",
      "官方账号菜单",
      "available",
      undefined,
      accountAnchor.classList.contains("user-menu") ? "已检测到账号弹出菜单" : "已检测到侧栏账号菜单入口",
    );
  } else {
    const reason = "未找到官方侧栏账号菜单，账号切换仅在「账户」设置页提供";
    state.disabledReasons["dom.account_menu"] = reason;
    push("dom.account_menu", "官方账号菜单", "disabled", reason);
  }

  const byId: Record<string, DOMContractProbe> = {};
  for (const probe of probes) byId[probe.id] = probe;
  return { probes, byId, state, sidebarMode };
}
