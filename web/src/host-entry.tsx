import { createRoot } from "react-dom/client";
import { useMemo, type ReactNode } from "react";
import { App } from "./App";
import { connectHostTransport, type CoreTransport } from "./api";
import { installHost, releaseHost, type HostConnection } from "./host";
import { useEmbeddedNavigation } from "./routes";
import { normalizeColorScheme, rewriteAssetURLs } from "./lib/embed";
import { createCoreTransport, createEventChannel, createNativeHost, HostScopeContext, type DemoHost, type NativeHost, type HostScopeValue } from "./native";
import { AuthProvider } from "./auth";
import { SettingsWindow, CacheSettingsSurface } from "./components/SettingsSurface";
import { AccountSettingsSection, LabSettingsSection, PluginsSettingsSection } from "./components/OfficialSettingsSections";
import { AccountSwitchModal } from "./components/AccountsSection";
import { SessionKeepAliveMenu } from "./components/SessionKeepAliveMenu";
import { NativeSettings } from "./components/NativeSettings";
import { SessionGrid } from "./components/SessionGrid";
import "./styles/surfaces.css";
import "./styles/plugin-surfaces.css";

export { createCoreTransport, createEventChannel, createNativeHost };
// The plugin runtime has exactly one implementation, exported here so the
// renderer loads it instead of shipping a copy.
export { createPluginRuntime } from "./plugin-runtime/runtime";
export { createEntryMountManager, mountMainAreaSurface } from "./plugin-runtime/entries";
export type { EntryMountManager, EntryPlan, EntrySurfaceGates, MainAreaMountOptions, SidebarNavPlan } from "./plugin-runtime/entries";
export {
  createDOMContractState,
  detectSidebarMode,
  evaluateDOMContracts,
  findAccountMenuAnchor,
  findChatHeaderTail,
  findOfficialSettingsPanel,
  findOfficialSettingsSection,
  findOfficialSettingsTab,
  findSessionInfoAside,
  findSessionInfoKeepAliveAnchor,
  findSettingsDialogParts,
  findSidebarFooter,
  findSidebarNewChatButtons,
  isOfficialSettingsTabSelected,
  resolveOfficialTexts,
} from "./plugin-runtime/contracts";
export type {
  ContractProbeStatus,
  DOMContractEvaluation,
  DOMContractProbe,
  DOMContractState,
  OfficialSettingsTabKey,
  SettingsDialogParts,
} from "./plugin-runtime/contracts";
export { loadModule as loadPluginModule, entryPath as pluginEntryPath } from "./plugin-runtime/loader";
export {
  ALLOWED_SCHEDULER_NOTIFY_EVENTS,
  dispatchSchedulerNotification,
  isOfficialNotificationEnabled,
  isOfficialNotificationSoundEnabled,
  shouldDispatchSchedulerNotification,
} from "./plugin-runtime/scheduler-notify";
export type { SchedulerNotificationPayload, SchedulerNotifyEnv } from "./plugin-runtime/scheduler-notify";
export type { ActivePlugin, PluginRuntime } from "./plugin-runtime/runtime";
import "./styles/workbench.css";

function HostScopeProvider({ host, children }: { host: NativeHost | null; children: ReactNode }) {
  const value = useMemo<HostScopeValue>(() => ({ host }), [host]);
  return <HostScopeContext.Provider value={value}>{children}</HostScopeContext.Provider>;
}

export interface MountOptions extends HostConnection {
  core: CoreTransport;
  permissions: string[];
  stylesheetURL: string;
  /** 由宿主设置面板开启：隐藏 EKD 二级顶栏/返回控件，内容铺满挂载容器。默认 false。 */
  embed?: boolean;
  /** 显式外观覆盖；缺省跟随宿主 documentElement 的 data-color-scheme。 */
  colorScheme?: string;
  /** 内联样式文本，用于宿主 CSP 不放行外链样式的场景；缺省用 stylesheetURL 的 link。 */
  stylesheetText?: string;
  /** 内联样式时把 / 开头的字体与图片地址改写到该基址。 */
  assetBaseURL?: string;
  demoHost?: DemoHost;
}

export interface EnhancementHandle {
  updateContext: (context: HostConnection["context"]) => void;
  unmount: () => void;
}

const mounts = new WeakMap<HTMLElement, EnhancementHandle>();

/**
 * The core transport behind `api.ts` is process-wide, so two mounts share it.
 * Reference counting keeps the last unmount from detaching a transport that a
 * still-open mount depends on.
 */
let transportMounts = 0;

function acquireTransport(transport: CoreTransport, permissions: string[]): () => void {
  transportMounts += 1;
  connectHostTransport(transport, permissions);
  let released = false;
  return () => {
    if (released) return;
    released = true;
    transportMounts -= 1;
    if (transportMounts <= 0) {
      transportMounts = 0;
      connectHostTransport(null);
    }
  };
}

// Embedded navigation is also process-wide. Reset it only when the last
// enhancement mount goes away, so one panel closing keeps the others routed.
let navigationMounts = 0;

function acquireNavigation(): () => void {
  navigationMounts += 1;
  useEmbeddedNavigation("/");
  let released = false;
  return () => {
    if (released) return;
    released = true;
    navigationMounts -= 1;
    if (navigationMounts <= 0) {
      navigationMounts = 0;
      useEmbeddedNavigation(null);
    }
  };
}

/**
 * Resolve the theme onto the surface root. A Shadow DOM surface that kept
 * "system" would apply the light `--paper` fallback for the frame before the
 * host stylesheet resolves it, which reads as a white flash inside a dark host.
 * The media query is resolved here and watched, so the surface is dark from the
 * first paint; `dispose()` removes the listener and the observer.
 */
function watchColorScheme(
  container: HTMLElement,
  document: Document,
  explicit: string | undefined,
): () => void {
  const query = typeof window.matchMedia === "function"
    ? window.matchMedia("(prefers-color-scheme: dark)")
    : null;
  const syncTheme = () => {
    const scheme = normalizeColorScheme(
      explicit ?? document.documentElement.dataset.colorScheme ?? document.documentElement.dataset.theme,
    );
    container.dataset.colorScheme = scheme === "system"
      ? (query?.matches === true ? "dark" : "light")
      : scheme;
  };
  syncTheme();
  const onChange = () => syncTheme();
  if (query) {
    if (typeof query.addEventListener === "function") query.addEventListener("change", onChange);
    else query.addListener?.(onChange);
  }
  const observer = new MutationObserver(syncTheme);
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-color-scheme", "data-theme"] });
  return () => {
    observer.disconnect();
    if (!query) return;
    if (typeof query.removeEventListener === "function") query.removeEventListener("change", onChange);
    else query.removeListener?.(onChange);
  };
}

export function mountEnhancement(element: HTMLElement, options: MountOptions): EnhancementHandle {
  const embed = options.embed === true;
  mounts.get(element)?.unmount();
  const shadow = element.shadowRoot ?? element.attachShadow({ mode: "open" });
  for (const node of Array.from(shadow.children)) node.remove();
  const styles = createStyles(options);
  const container = document.createElement("div");
  container.className = `ekd-root ekd-mount${embed ? " is-embedded" : ""}`;
  const releaseTheme = watchColorScheme(container, document, options.colorScheme);
  const priorEmbeddedAttr = element.getAttribute("data-ekd-embedded");
  if (embed) element.dataset.ekdEmbedded = "true";
  shadow.append(styles, container);
  const hostConnection: HostConnection = options;
  installHost(hostConnection);
  const releaseNavigation = acquireNavigation();
  const releaseTransport = acquireTransport(options.core, options.permissions);
  const root = createRoot(container);
  root.render(
    <HostScopeProvider host={options.demoHost ?? null}>
      <App embed={embed} />
    </HostScopeProvider>,
  );
  let mounted = true;
  const handle: EnhancementHandle = {
    updateContext: (context: HostConnection["context"]) => {
      if (!mounted) return;
      installHost({ ...options, context });
    },
    unmount: () => {
      if (!mounted) return;
      mounted = false;
      releaseTheme();
      if (mounts.get(element) === handle) mounts.delete(element);
      try {
        root.unmount();
      } finally {
        container.remove();
        styles.remove();
        if (element.dataset.ekdEmbedded === "true") {
          if (priorEmbeddedAttr === null) delete element.dataset.ekdEmbedded;
          else element.dataset.ekdEmbedded = priorEmbeddedAttr;
        }
        releaseTransport();
        releaseNavigation();
        releaseHost(hostConnection);
      }
    },
  };
  mounts.set(element, handle);
  return handle;
}

export type SurfaceName =
  | "sessions"
  | "settings"
  | "settings-cache"
  | "settings-plugins"
  | "settings-account"
  | "settings-agent-permissions"
  | "settings-agent-subagent"
  | "settings-lab"
  | "account-switch-modal"
  | "session-keepalive-menu";
export interface SurfaceMountOptions {
  host: NativeHost;
  stylesheetURL: string;
  stylesheetText?: string;
  assetBaseURL?: string;
  colorScheme?: string;
  initialExtraIds?: string[];
  onNativeRestore?: () => void;
  accountTargetId?: string;
  sessionId?: string;
  onKeepAliveUpdated?: (view: unknown) => void;
  onCloseModal?: () => void;
}

const SETTINGS_PERMISSIONS = ["core:read", "settings:write", "models:read", "usage:read", "usage:write", "plugins:read", "plugins:write", "credentials:read", "credentials:write", "update:read", "update:write"];

export function mountSurface(element: HTMLElement, surface: SurfaceName, options: SurfaceMountOptions): EnhancementHandle {
  mounts.get(element)?.unmount();
  const shadow = element.shadowRoot ?? element.attachShadow({ mode: "open" });
  shadow.replaceChildren();
  const styles = createStyles(options);
  const container = document.createElement("div");
  container.className = `ekd-root ekd-surface ekd-surface-${surface}`;
  const releaseTheme = watchColorScheme(container, element.ownerDocument, options.colorScheme);
  shadow.append(styles, container);
  if (surface === "sessions") element.setAttribute("data-ekdp-thinking-root", "");
  const needsSettingsTransport = surface === "settings" || surface.startsWith("settings-") || surface === "account-switch-modal";
  const releaseTransport = needsSettingsTransport
    ? acquireTransport(createCoreTransport(options.host), SETTINGS_PERMISSIONS)
    : null;
  const root = createRoot(container);
  const wrap = (children: React.ReactNode) => <HostScopeProvider host={options.host}>{children}</HostScopeProvider>;
  if (surface === "sessions") {
    root.render(wrap(<SessionGrid host={options.host} initialExtraIds={options.initialExtraIds ?? []} onNativeRestore={options.onNativeRestore ?? (() => options.host.close())} />));
  } else if (surface === "settings-cache") {
    root.render(wrap(<CacheSettingsSurface host={options.host} />));
  } else if (surface === "settings-plugins") {
    root.render(wrap(<PluginsSettingsSection host={options.host} />));
  } else if (surface === "settings-account") {
    root.render(wrap(<AccountSettingsSection host={options.host} />));
  } else if (surface === "settings-agent-permissions") {
    root.render(wrap(<div className="ekds ekds-official-sec"><NativeSettings embedded mode="permissions" /></div>));
  } else if (surface === "settings-agent-subagent") {
    root.render(wrap(<div className="ekds ekds-official-sec"><NativeSettings embedded mode="subagent" /></div>));
  } else if (surface === "settings-lab") {
    root.render(wrap(<LabSettingsSection host={options.host} />));
  } else if (surface === "account-switch-modal") {
    root.render(wrap(<AccountSwitchModal host={options.host} targetId={options.accountTargetId ?? ""} onClose={options.onCloseModal ?? (() => options.host.close())} />));
  } else if (surface === "session-keepalive-menu") {
    root.render(wrap(<SessionKeepAliveMenu host={options.host} sessionId={options.sessionId ?? ""} onClose={options.onCloseModal ?? (() => options.host.close())} onUpdated={options.onKeepAliveUpdated} />));
  } else {
    root.render(wrap(<AuthProvider><SettingsWindow host={options.host} /></AuthProvider>));
  }
  let mounted = true;
  const handle: EnhancementHandle = {
    updateContext: () => {},
    unmount: () => {
      if (!mounted) return;
      mounted = false;
      if (surface === "sessions") element.removeAttribute("data-ekdp-thinking-root");
      releaseTheme();
      root.unmount();
      container.remove();
      styles.remove();
      if (mounts.get(element) === handle) mounts.delete(element);
      releaseTransport?.();
    },
  };
  mounts.set(element, handle);
  return handle;
}

function createStyles(options: Pick<MountOptions, "stylesheetURL" | "stylesheetText" | "assetBaseURL">): HTMLElement {
  if (options.stylesheetText) {
    const inline = document.createElement("style");
    inline.textContent = rewriteAssetURLs(options.stylesheetText, options.assetBaseURL);
    return inline;
  }
  const link = document.createElement("link");
  link.rel = "stylesheet";
  link.href = options.stylesheetURL;
  return link;
}
