import { createRoot } from "react-dom/client";
import { useMemo, type ReactNode } from "react";
import { App } from "./App";
import { connectHostTransport, type CoreTransport } from "./api";
import { installHost, releaseHost, type HostConnection } from "./host";
import { useEmbeddedNavigation } from "./routes";
import { normalizeColorScheme, rewriteAssetURLs } from "./lib/embed";
import { Workbench } from "./components/Workbench";
import { createCoreTransport, createEventChannel, createNativeHost, HostScopeContext, type DemoHost, type NativeHost, type HostScopeValue } from "./native";
import { AuthProvider } from "./auth";
import { MonitorSurface, PlannerSurface, SettingsWindow } from "./components/Surfaces";
import { SessionGrid } from "./components/SessionGrid";
import "./styles/surfaces.css";

export { createCoreTransport, createEventChannel, createNativeHost };
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

export function mountEnhancement(element: HTMLElement, options: MountOptions): EnhancementHandle {
  const embed = options.embed === true;
  mounts.get(element)?.unmount();
  const shadow = element.shadowRoot ?? element.attachShadow({ mode: "open" });
  for (const node of Array.from(shadow.children)) node.remove();
  const styles = createStyles(options);
  const container = document.createElement("div");
  container.className = `ekd-root ekd-mount${embed ? " is-embedded" : ""}`;
  const syncTheme = () => {
    container.dataset.colorScheme = normalizeColorScheme(options.colorScheme ?? document.documentElement.dataset.colorScheme);
  };
  syncTheme();
  const observer = new MutationObserver(syncTheme);
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-color-scheme"] });
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
      observer.disconnect();
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

export function mountWorkbench(element: HTMLElement, options: { host: DemoHost; stylesheetURL: string; stylesheetText?: string; assetBaseURL?: string; colorScheme?: string }): EnhancementHandle {
  mounts.get(element)?.unmount();
  const shadow = element.shadowRoot ?? element.attachShadow({ mode: "open" });
  shadow.replaceChildren();
  const styles = createStyles(options);
  const container = document.createElement("div");
  container.className = "ekd-root ekd-workbench";
  const syncTheme = () => { container.dataset.colorScheme = normalizeColorScheme(options.colorScheme ?? element.ownerDocument.documentElement.dataset.colorScheme); };
  syncTheme();
  const observer = new MutationObserver(syncTheme);
  observer.observe(element.ownerDocument.documentElement, { attributes: true, attributeFilter: ["data-color-scheme"] });
  shadow.append(styles, container);
  const root = createRoot(container);
  root.render(<HostScopeProvider host={options.host}><Workbench host={options.host} /></HostScopeProvider>);
  const handle: EnhancementHandle = {
    updateContext: () => {},
    unmount: () => { observer.disconnect(); root.unmount(); container.remove(); styles.remove(); mounts.delete(element); },
  };
  mounts.set(element, handle);
  return handle;
}

export type SurfaceName = "planner" | "sessions" | "settings" | "monitor";
export interface SurfaceMountOptions {
  host: NativeHost;
  stylesheetURL: string;
  stylesheetText?: string;
  assetBaseURL?: string;
  colorScheme?: string;
  initialExtraIds?: string[];
  initialSource?: "local" | "web";
  onNativeRestore?: () => void;
}

const SETTINGS_PERMISSIONS = ["core:read", "settings:write", "models:read", "models:write", "usage:read", "usage:write", "plugins:read", "plugins:write", "credentials:read", "credentials:write"];

export function mountSurface(element: HTMLElement, surface: SurfaceName, options: SurfaceMountOptions): EnhancementHandle {
  mounts.get(element)?.unmount();
  const shadow = element.shadowRoot ?? element.attachShadow({ mode: "open" });
  shadow.replaceChildren();
  const styles = createStyles(options);
  const container = document.createElement("div");
  container.className = `ekd-root ekd-surface ekd-surface-${surface}`;
  const syncTheme = () => { container.dataset.colorScheme = normalizeColorScheme(options.colorScheme ?? element.ownerDocument.documentElement.dataset.colorScheme); };
  syncTheme();
  const observer = new MutationObserver(syncTheme);
  observer.observe(element.ownerDocument.documentElement, { attributes: true, attributeFilter: ["data-color-scheme"] });
  shadow.append(styles, container);
  const releaseTransport = surface === "settings" ? acquireTransport(createCoreTransport(options.host), SETTINGS_PERMISSIONS) : null;
  const root = createRoot(container);
  const wrap = (children: React.ReactNode) => <HostScopeProvider host={options.host}>{children}</HostScopeProvider>;
  if (surface === "planner") root.render(wrap(<PlannerSurface host={options.host} initialSource={options.initialSource} />));
  else if (surface === "sessions") root.render(wrap(<SessionGrid host={options.host} initialExtraIds={options.initialExtraIds ?? []} onNativeRestore={options.onNativeRestore ?? (() => options.host.close())} />));
  else if (surface === "settings") root.render(wrap(<AuthProvider><SettingsWindow host={options.host} /></AuthProvider>));
  else root.render(wrap(<MonitorSurface host={options.host} />));
  let mounted = true;
  const handle: EnhancementHandle = {
    updateContext: () => {},
    unmount: () => {
      if (!mounted) return;
      mounted = false;
      observer.disconnect();
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
