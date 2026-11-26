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
const DEFAULT_SYNC_WAIT_MS = 150;

export interface PluginHostBridge {
  sync(options?: { waitForActivationMs?: number }): Promise<void>;
  list(): Array<{
    id: string;
    enabled: boolean;
    version?: string;
    updatedAt?: string;
    capabilities?: string[];
    entry?: { renderer?: string; settings?: string };
    hasSettings?: boolean;
  }>;
  subscribe?(listener: () => void): () => void;
  mountSettings(
    id: string,
    element: HTMLElement,
    options?: { signal?: AbortSignal; version?: string; updatedAt?: string; capabilities?: string[] },
  ): Promise<() => void>;
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

export async function syncPluginHost(options?: { waitForActivationMs?: number }): Promise<string | null> {
  const bridge = pluginHostBridge();
  if (!bridge) return null;
  const waitMs =
    typeof options?.waitForActivationMs === "number" && Number.isFinite(options.waitForActivationMs) && options.waitForActivationMs >= 0
      ? options.waitForActivationMs
      : DEFAULT_SYNC_WAIT_MS;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let timedOut = false;
  try {
    const syncPromise = Promise.resolve().then(() => bridge.sync({ waitForActivationMs: waitMs }));
    const guardedSync = syncPromise.catch(cause => {
      if (timedOut) return;
      throw cause;
    });
    const timeoutPromise = new Promise<void>(resolve => {
      timer = setTimeout(() => {
        timedOut = true;
        resolve();
      }, waitMs);
      (timer as { unref?: () => void } | undefined)?.unref?.();
    });
    await Promise.race([guardedSync, timeoutPromise]);
    return null;
  } catch (cause) {
    return cause instanceof Error ? cause.message : String(cause);
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}
