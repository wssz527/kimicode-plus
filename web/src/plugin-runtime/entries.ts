/**
 * Native entry injection for plugin `ui:entry` and `ui:sidebar-nav` grants.
 *
 * The official client builds its session-info "available" list, the right-panel
 * empty state, the `+` menu and the sidebar rows from its own components. An
 * injected row is therefore a clone of a real official row, so it carries the
 * exact structure, scoped `data-v-*` attributes and class names the official CSS
 * targets. Nothing here invents a second visual language.
 *
 * Reconciliation is per container and idempotent: every sync keeps at most one
 * node per action in each container, recreates a node the official renderer
 * removed, and drops nodes whose plan disappeared. The host owns the single
 * MutationObserver and calls `sync()`; this module never installs one, which is
 * what keeps a disabled plugin from leaving an observer behind.
 */

import type { Cleanup, MainAreaDefinition, MainAreaHandle } from "./context.js";
import {
  findRightPanelContainers,
  findSessionInfoAvailableContainers,
  findSidebarNewChatButtons,
} from "./contracts.js";

export interface EntryPlan {
  /** Stable action id; `plugin:<pluginId>:<entryId>` for plugin entries. */
  action: string;
  title: string;
  /** Single SVG path (`d`) drawn in the official icon slot. */
  icon?: string;
  pluginId?: string;
  open(): void | Promise<void>;
}

export interface SidebarNavPlan {
  action: string;
  title: string;
  icon?: string;
  pluginId?: string;
  open(): void | Promise<void>;
}

export interface EntryMountManagerOptions {
  document: Document;
  /** The official right-panel open path (the panel store's `showPanel`). */
  openPanel?: () => void;
  /** Called for an entry whose `open()` throws. `null` is a built-in entry. */
  onError?: (pluginId: string | null, error: unknown) => void;
}

export interface EntrySurfaceGates {
  sessionInfo?: boolean;
  rightPanel?: boolean;
  sidebarNav?: boolean;
}

export interface EntryMountManager {
  sync(entries: EntryPlan[], navs?: SidebarNavPlan[], gates?: EntrySurfaceGates): void;
  remove(action: string): void;
  dispose(): void;
  stats(): { entries: number; navs: number; observers: number };
}

// The three documented plugin entry surfaces plus the real 1.3.7 `+` menu. The
// `pl` launcher is the empty right-panel page; `panel-add-menu` is the dropdown
// the `+` button opens. Both are kept because the acceptance plan names the
// launcher and the live client renders both at different moments.
function entryContainers(document: Document, gates?: EntrySurfaceGates): Element[] {
  const seen = new Set<Element>();
  const containers: Element[] = [];
  const add = (container: Element) => {
    if (seen.has(container)) return;
    seen.add(container);
    containers.push(container);
  };
  if (gates?.sessionInfo !== false) {
    for (const container of findSessionInfoAvailableContainers(document)) add(container);
  }
  if (gates?.rightPanel !== false) {
    for (const container of findRightPanelContainers(document)) add(container);
  }
  return containers;
}

const ENTRY_ATTRIBUTE = "data-ekd-entry";
const NAV_ATTRIBUTE = "data-ekd-sidebar-nav";
const ICON_PATH = /^[MmLlHhVvCcSsQqTtAaZz0-9eE ,.+-]+$/;

function byClass(node: Element, name: string): boolean {
  return node.classList.contains(name);
}

function officialEntries(container: Element): Element[] {
  return Array.from(container.querySelectorAll("button")).filter(button => {
    if (button.hasAttribute(ENTRY_ATTRIBUTE)) return false;
    if (byClass(button, "sit-more") || byClass(button, "sit-opened")) return false;
    return byClass(button, "sit-link") || byClass(button, "ui-menu-item");
  });
}

function labelSlot(element: Element): Element | null {
  const spans = Array.from(element.querySelectorAll("span")).filter(span =>
    !span.classList.contains("ui-kbd") && !span.classList.contains("ptb-kbd") && !span.classList.contains("pl-kbd"));
  return spans.find(span => span.classList.contains("sit-name")) ?? spans[0] ?? null;
}

/** Replaces the official label without disturbing the cloned icon. */
function setLabel(document: Document, element: Element, title: string): void {
  const slot = labelSlot(element);
  if (slot) {
    slot.textContent = title;
    return;
  }
  // A menu item renders its label as a direct text node. Keep the icon element,
  // drop the old text and append a span so the new label is addressable too.
  const icon = element.querySelector("svg");
  element.textContent = "";
  if (icon) element.append(icon);
  const span = document.createElement("span");
  span.className = "sit-name";
  span.textContent = title;
  element.append(span);
}

function setIcon(element: Element, icon: string | undefined): void {
  if (!icon || !ICON_PATH.test(icon)) return;
  const svg = element.querySelector("svg");
  if (!svg) return;
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("fill", "none");
  svg.setAttribute("stroke", "currentColor");
  svg.setAttribute("stroke-width", "1.6");
  svg.setAttribute("stroke-linecap", "round");
  svg.setAttribute("stroke-linejoin", "round");
  const path = element.ownerDocument.createElementNS("http://www.w3.org/2000/svg", "path");
  path.setAttribute("d", icon);
  svg.replaceChildren(path);
}

function removeShortcuts(element: Element): void {
  for (const shortcut of Array.from(element.querySelectorAll(".ui-kbd, .ptb-kbd, .pl-kbd"))) shortcut.remove();
}

function prepareClonedButton(template: Element): HTMLElement {
  const button = template.cloneNode(true) as HTMLElement;
  button.removeAttribute("disabled");
  button.removeAttribute("aria-disabled");
  button.removeAttribute("aria-current");
  button.removeAttribute("aria-expanded");
  button.removeAttribute("aria-haspopup");
  button.removeAttribute("data-state");
  button.setAttribute("type", "button");
  button.setAttribute("tabindex", "0");
  removeShortcuts(button);
  return button;
}

export function createEntryMountManager(options: EntryMountManagerOptions): EntryMountManager {
  const { document } = options;
  let disposed = false;
  const listeners = new Map<Element, (event: Event) => void>();

  function report(pluginId: string | null, error: unknown): void {
    try {
      options.onError?.(pluginId, error);
    } catch {
      // Reporting a failure must not break reconciliation.
    }
  }

  function invoke(plan: EntryPlan | SidebarNavPlan, element: Element): void {
    const fromSessionInfo = element.closest("aside.session-info") !== null;
    void Promise.resolve()
      .then(() => {
        if (fromSessionInfo) options.openPanel?.();
        return plan.open();
      })
      .catch(error => report(plan.pluginId ?? null, error));
  }

  function bind(element: HTMLElement, plan: EntryPlan | SidebarNavPlan): void {
    const listener = (event: Event) => {
      event.preventDefault();
      event.stopPropagation();
      invoke(plan, element);
    };
    element.addEventListener("click", listener);
    listeners.set(element, listener);
  }

  function unbind(element: Element): void {
    const listener = listeners.get(element);
    if (!listener) return;
    (element as HTMLElement).removeEventListener("click", listener);
    listeners.delete(element);
  }

  function makeEntry(template: Element, plan: EntryPlan): HTMLElement {
    const button = prepareClonedButton(template);
    button.classList.add("ekd-native-entry");
    button.dataset.ekdEntry = plan.action;
    setIcon(button, plan.icon);
    setLabel(document, button, plan.title);
    button.setAttribute("title", plan.title);
    button.setAttribute("aria-label", plan.title);
    bind(button, plan);
    return button;
  }

  function makeNav(template: Element, plan: SidebarNavPlan): HTMLElement {
    const button = prepareClonedButton(template);
    button.classList.add("ekd-sidebar-nav");
    button.dataset.ekdSidebarNav = plan.action;
    setIcon(button, plan.icon);
    setLabel(document, button, plan.title);
    button.setAttribute("title", plan.title);
    button.setAttribute("aria-label", plan.title);
    bind(button, plan);
    return button;
  }

  function syncEntries(entries: EntryPlan[], gates?: EntrySurfaceGates): Set<Element> {
    const live = new Set<Element>();
    const plans = entries.filter(plan => plan
      && typeof plan.action === "string" && plan.action !== ""
      && typeof plan.title === "string" && plan.title.trim() !== ""
      && typeof plan.open === "function");
    for (const container of entryContainers(document, gates)) {
      const officials = officialEntries(container);
      if (officials.length === 0) continue;
      const template = officials[0]!;
      let anchor: Element = officials[officials.length - 1]!;
      for (const plan of plans) {
        const matches = Array.from(container.querySelectorAll("[" + ENTRY_ATTRIBUTE + "]"))
          .filter(node => node.getAttribute(ENTRY_ATTRIBUTE) === plan.action);
        let entry: Element | null = matches[0] ?? null;
        for (const extra of matches.slice(1)) {
          unbind(extra);
          extra.remove();
        }
        if (entry && entry.previousElementSibling !== anchor) {
          unbind(entry);
          entry.remove();
          entry = null;
        }
        if (!entry) {
          entry = makeEntry(template, plan);
          if (anchor.isConnected) anchor.insertAdjacentElement("afterend", entry);
          else container.append(entry);
        }
        anchor = entry;
        live.add(entry);
      }
    }
    return live;
  }

  function syncNavs(navs: SidebarNavPlan[], gates?: EntrySurfaceGates): Set<Element> {
    const live = new Set<Element>();
    if (gates?.sidebarNav === false) return live;
    const plans = navs.filter(plan => plan
      && typeof plan.action === "string" && plan.action !== ""
      && typeof plan.title === "string" && plan.title.trim() !== ""
      && typeof plan.open === "function");
    for (const native of findSidebarNewChatButtons(document)) {
      const container = native.parentElement;
      if (!container) continue;
      if (native.hasAttribute(ENTRY_ATTRIBUTE) || native.hasAttribute(NAV_ATTRIBUTE)) continue;
      let anchor: Element = native;
      for (const plan of plans) {
        const matches = Array.from(container.querySelectorAll("[" + NAV_ATTRIBUTE + "]"))
          .filter(node => node.getAttribute(NAV_ATTRIBUTE) === plan.action);
        let nav: Element | null = matches[0] ?? null;
        for (const extra of matches.slice(1)) {
          unbind(extra);
          extra.remove();
        }
        if (nav && nav.previousElementSibling !== anchor) {
          unbind(nav);
          nav.remove();
          nav = null;
        }
        if (!nav) {
          nav = makeNav(native, plan);
          anchor.insertAdjacentElement("afterend", nav);
        }
        anchor = nav;
        live.add(nav);
      }
    }
    return live;
  }

  function sweep(attribute: string, live: Set<Element>): void {
    for (const node of Array.from(document.querySelectorAll("[" + attribute + "]"))) {
      if (live.has(node)) continue;
      unbind(node);
      node.remove();
    }
  }

  return {
    sync(entries, navs = [], gates) {
      if (disposed) return;
      const liveEntries = syncEntries(entries, gates);
      sweep(ENTRY_ATTRIBUTE, liveEntries);
      const liveNavs = syncNavs(navs, gates);
      sweep(NAV_ATTRIBUTE, liveNavs);
    },
    remove(action) {
      for (const node of Array.from(document.querySelectorAll("[" + ENTRY_ATTRIBUTE + "]"))
        .filter(item => item.getAttribute(ENTRY_ATTRIBUTE) === action)) {
        unbind(node);
        node.remove();
      }
      for (const node of Array.from(document.querySelectorAll("[" + NAV_ATTRIBUTE + "]"))
        .filter(item => item.getAttribute(NAV_ATTRIBUTE) === action)) {
        unbind(node);
        node.remove();
      }
    },
    dispose() {
      disposed = true;
      for (const node of Array.from(document.querySelectorAll("[" + ENTRY_ATTRIBUTE + "],[" + NAV_ATTRIBUTE + "]"))) {
        unbind(node);
        node.remove();
      }
      listeners.clear();
    },
    stats() {
      return {
        entries: document.querySelectorAll("[" + ENTRY_ATTRIBUTE + "]").length,
        navs: document.querySelectorAll("[" + NAV_ATTRIBUTE + "]").length,
        // Reconciliation runs on the host's single MutationObserver. Keeping this
        // at zero is what the toggle test asserts after twenty enable/disable
        // cycles: a disabled plugin must not leave an observer of its own.
        observers: 0,
      };
    },
  };
}

export interface MainAreaMountOptions {
  document: Document;
  container: HTMLElement;
  pluginId: string;
  definition: MainAreaDefinition;
  window?: Window | null;
}

function runCleanup(cleanup: Cleanup | void): void {
  if (!cleanup) return;
  try {
    if (typeof cleanup === "function") cleanup();
    else cleanup.dispose();
  } catch {
    // a failing teardown must not strand DOM restoration
  }
}

const mainAreaReleases = new WeakMap<HTMLElement, () => void>();

export function mountMainAreaSurface(options: MainAreaMountOptions): MainAreaHandle {
  const { document, container, pluginId, definition } = options;
  mainAreaReleases.get(container)?.();
  const hostWindow = options.window ?? null;
  const mode = definition?.mode === "page" ? "page" : "split";

  const row = document.createElement("div");
  row.className = "ekdp-main-split";
  row.dataset.ekdpPlugin = pluginId;
  row.dataset.ekdpMode = mode;

  const native = document.createElement("div");
  native.className = "ekdp-main-native";

  const splitter = document.createElement("div");
  splitter.className = "ekdp-main-splitter";
  splitter.setAttribute("role", "separator");
  splitter.setAttribute("aria-orientation", "vertical");
  splitter.setAttribute("aria-label", "调整同屏宽度");
  splitter.tabIndex = 0;

  const pane = document.createElement("div");
  pane.className = "ekdp-main-pane";
  pane.dataset.ekdpPlugin = pluginId;

  const nativeChildren = Array.from(container.childNodes);
  const { display, flexDirection } = container.style;
  for (const child of nativeChildren) native.append(child);
  row.append(native, splitter, pane);
  container.append(row);

  const min = Math.max(280, Number(definition?.minWidth) || 360);
  const max = Math.max(min, Number(definition?.maxWidth) || 900);
  let width = Math.min(max, Math.max(min, Number(definition?.width) || 420));

  const apply = () => {
    container.style.display = "flex";
    container.style.flexDirection = "column";
    pane.style.minWidth = "0";
    pane.style.minHeight = "0";
    pane.style.display = "flex";
    pane.style.flexDirection = "column";
    if (mode === "page") {
      container.dataset.ekdpPage = pluginId;
      delete container.dataset.ekdpSplit;
      native.hidden = true;
      native.style.display = "none";
      splitter.hidden = true;
      splitter.style.display = "none";
      pane.style.flex = "1 1 auto";
      pane.style.width = "100%";
      return;
    }
    container.dataset.ekdpSplit = pluginId;
    delete container.dataset.ekdpPage;
    native.hidden = false;
    native.style.flex = "1 1 auto";
    native.style.minWidth = "0";
    native.style.minHeight = "0";
    native.style.display = "flex";
    native.style.flexDirection = "column";
    splitter.hidden = false;
    splitter.style.removeProperty("display");
    pane.style.flex = "0 0 auto";
    pane.style.width = width + "px";
  };
  apply();

  let dragging: { startX: number; startWidth: number } | null = null;
  const onPointerMove = (event: PointerEvent) => {
    if (!dragging) return;
    const next = dragging.startWidth - (event.clientX - dragging.startX);
    width = Math.min(max, Math.max(min, next));
    apply();
  };
  const stopDrag = () => {
    dragging = null;
    splitter.classList.remove("dragging");
    if (hostWindow) {
      hostWindow.removeEventListener("pointermove", onPointerMove, true);
      hostWindow.removeEventListener("pointerup", stopDrag, true);
    }
  };
  const onPointerDown = (event: PointerEvent) => {
    if (mode === "page") return;
    event.preventDefault();
    dragging = { startX: event.clientX, startWidth: width };
    splitter.classList.add("dragging");
    if (hostWindow) {
      hostWindow.addEventListener("pointermove", onPointerMove, true);
      hostWindow.addEventListener("pointerup", stopDrag, true);
    }
  };
  const onKeyDown = (event: KeyboardEvent) => {
    if (mode === "page") return;
    if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
    event.preventDefault();
    width = Math.min(max, Math.max(min, width + (event.key === "ArrowLeft" ? 24 : -24)));
    apply();
  };
  if (mode === "split") {
    splitter.addEventListener("pointerdown", onPointerDown as EventListener);
    splitter.addEventListener("keydown", onKeyDown as EventListener);
  }

  let released = false;
  let renderCleanup: Cleanup | void;
  const release = () => {
    if (released) return;
    released = true;
    stopDrag();
    if (mode === "split") {
      splitter.removeEventListener("pointerdown", onPointerDown as EventListener);
      splitter.removeEventListener("keydown", onKeyDown as EventListener);
    }
    runCleanup(renderCleanup);
    for (const child of nativeChildren) {
      if (child.parentNode !== native) continue;
      if (row.parentNode === container) container.insertBefore(child, row);
      else container.append(child);
    }
    row.remove();
    container.style.removeProperty("display");
    container.style.removeProperty("flex-direction");
    if (display) container.style.display = display;
    if (flexDirection) container.style.flexDirection = flexDirection;
    delete container.dataset.ekdpSplit;
    delete container.dataset.ekdpPage;
    if (mainAreaReleases.get(container) === release) mainAreaReleases.delete(container);
  };
  mainAreaReleases.set(container, release);
  try {
    renderCleanup = definition.render(pane);
  } catch (error) {
    release();
    throw error;
  }

  return {
    setWidth(px: number) {
      if (released || mode === "page") return;
      width = Math.min(max, Math.max(min, Number(px) || width));
      apply();
    },
    width: () => width,
    dispose: release,
  };
}