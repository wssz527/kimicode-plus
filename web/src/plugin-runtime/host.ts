/**
 * The bridge the renderer installs for the plugin surfaces it hosts. The
 * settings page lives in the same JS context as the plugin runtime (the host
 * bundle is mounted into the official renderer), so it can ask the runtime to
 * mount a plugin's own settings module into a container the page owns.
 *
 * `mountSettings` resolves to the release function, not an object with a
 * `dispose` method: the host builds it as a closure that disposes the module and
 * empties the container. The settings page calls it.
 *
 * Outside the native host — the web preview, a plain browser tab — the bridge is
 * absent, so callers get null instead of a broken panel.
 */
export interface PluginHostBridge {
  sync(): Promise<void>;
  list(): Array<{ id: string; enabled: boolean; entry?: { renderer?: string; settings?: string }; hasSettings?: boolean }>;
  mountSettings(id: string, element: HTMLElement): Promise<() => void>;
}

declare global {
  interface Window {
    __ekdPlugins?: PluginHostBridge;
  }
}

export function pluginHostBridge(): PluginHostBridge | null {
  if (typeof window === "undefined") return null;
  return window.__ekdPlugins ?? null;
}
