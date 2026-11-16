import { createRoot } from "react-dom/client";
import { App } from "./App";
import { connectHostTransport, type CoreTransport } from "./api";
import { installHost, type HostConnection } from "./host";
import { useEmbeddedNavigation } from "./routes";
import { normalizeColorScheme, rewriteAssetURLs } from "./lib/embed";
import { Workbench } from "./components/Workbench";
import { installDemoHost, createCoreTransport, createNativeHost, type DemoHost, type NativeHost } from "./native";
import { AuthProvider } from "./auth";
import { MonitorSurface, PlannerSurface, SettingsWindow } from "./components/Surfaces";
import { SessionGrid } from "./components/SessionGrid";
import "./styles/surfaces.css";

export { createCoreTransport, createNativeHost };
import "./styles/workbench.css";

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
  installHost(options);
  installDemoHost(options.demoHost ?? null);
  useEmbeddedNavigation("/");
  connectHostTransport(options.core, options.permissions);
  const root = createRoot(container);
  root.render(<App embed={embed} />);
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
        connectHostTransport(null);
        useEmbeddedNavigation(null);
        installHost(null);
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
  root.render(<Workbench host={options.host} />);
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
  installDemoHost(options.host);
  if (surface === "settings") connectHostTransport(createCoreTransport(options.host), ["core:read", "settings:write", "models:read", "models:write", "usage:read", "usage:write", "plugins:read", "plugins:write", "credentials:read", "credentials:write"]);
  const root = createRoot(container);
  if (surface === "planner") root.render(<PlannerSurface host={options.host} initialSource={options.initialSource} />);
  else if (surface === "sessions") root.render(<SessionGrid host={options.host} initialExtraIds={options.initialExtraIds ?? []} onNativeRestore={options.onNativeRestore ?? (() => options.host.close())} />);
  else if (surface === "settings") root.render(<AuthProvider><SettingsWindow host={options.host} /></AuthProvider>);
  else root.render(<MonitorSurface host={options.host} />);
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
      if (surface === "settings") connectHostTransport(null);
      installDemoHost(null);
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
