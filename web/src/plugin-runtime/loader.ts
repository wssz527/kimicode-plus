import type { PluginModule } from "./context";

/**
 * Plugin modules are ES modules whose source the core serves from the plugin's
 * own directory. The renderer runs inside the official client under `app://`,
 * so a relative `import()` would resolve against that scheme and never reach the
 * core. The source is therefore fetched through the bridge and imported from a
 * blob URL, which is the same pattern the host bundle itself uses.
 *
 * A plugin module is a single file by contract: it receives everything it needs
 * through `ctx` and has no import graph of its own. The renderer entry exports
 * `activate`; the settings entry exports `renderSettings`. Which export is
 * required is decided by the caller, so one loader serves both.
 */
export async function loadModule(source: string, requiredExport: "activate" | "renderSettings" = "activate"): Promise<PluginModule> {
  if (!source.trim()) {
    throw new Error("插件入口是空文件");
  }
  const url = URL.createObjectURL(new Blob([source], { type: "text/javascript" }));
  try {
    const module = await import(/* @vite-ignore */ url);
    if (typeof (module as Record<string, unknown>)[requiredExport] !== "function") {
      throw new Error(`插件入口没有导出 ${requiredExport}(ctx)`);
    }
    return module as PluginModule;
  } finally {
    URL.revokeObjectURL(url);
  }
}

/**
 * Validates an entry path and returns the asset path used to fetch it. The
 * check is lexical because the path travels to the core, which repeats the
 * containment check against the real directory.
 */
export function entryPath(entry: string): string {
  const trimmed = entry.trim();
  if (!trimmed || trimmed.includes("\\") || trimmed.startsWith("/") || trimmed.includes("://") || trimmed.includes("..")) {
    throw new Error("插件入口必须是插件目录内的相对路径");
  }
  return trimmed;
}
