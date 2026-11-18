import { PluginError, type ProxyRequestInit, type ProxyResponse, type RuntimeRequest } from "./context.js";

/**
 * The bridge is the only way a plugin reaches the core. Two rules are enforced
 * here rather than in the plugin: the action name must be in the granted list,
 * and the request path is fixed by the runtime so a plugin cannot address
 * another plugin's runtime route.
 */
export interface BridgeOptions {
  token: string;
  granted: string[];
  request: RuntimeRequest;
}

export interface Bridge {
  call<T = unknown>(action: string, params?: unknown): Promise<T>;
  fetch(url: string, init?: ProxyRequestInit): Promise<ProxyResponse>;
}

/** Actions that need no capability because they cannot read or change anything. */
const OPEN_ACTIONS = new Set(["runtime.info", "log", "events.poll"]);

export function actionAllowed(action: string, granted: string[]): boolean {
  if (OPEN_ACTIONS.has(action)) return true;
  if (action.startsWith("net.")) return granted.some(name => name.startsWith("net:"));
  if (action.startsWith("storage.")) return granted.includes("storage");
  // Credential names are declared as `credentials:<name>`, so the grant is a
  // pattern rather than a fixed entry. The core still checks the exact name on
  // every call; this check only refuses a plugin that declared no credential.
  if (action.startsWith("credentials.")) return granted.some(name => name.startsWith("credentials:"));
  const capability = ACTION_CAPABILITY[action];
  // An action with no entry in this table is unknown to the runtime, which must
  // refuse rather than forward and rely on the core to catch it.
  return capability !== undefined && granted.includes(capability);
}

const ACTION_CAPABILITY: Record<string, string> = {
  "cache.summary": "metrics:read",
  "cache.sessions": "metrics:read",
  "cache.history": "metrics:read",
  "cache.turns": "metrics:read",
  "quota.balances": "metrics:read",
  "cache.retention": "metrics:read",
  "cache.receipts": "metrics:read",
  "sessions.list": "sessions:read",
  "sessions.view": "sessions:read",
  "sessions.diff": "sessions:read",
  "sessions.open": "sessions:read",
  "sessions.watch": "sessions:read",
  "sessions.unwatch": "sessions:read",
  "sessions.send": "sessions:send",
  "sessions.create": "sessions:send",
  "config.get": "config:read",
  "config.save": "config:write",
  "ui.popout": "ui:right-panel",

  // API v2. The privilege check is in Go; the runtime only needs to know which
  // grant admits the call so a plugin fails locally instead of on the wire.
  "sessions.approve": "sessions:approve",
  "sessions.abort": "sessions:abort",
  "sessions.history": "sessions:history",
  "attachments.stage": "attachments",
  "attachments.release": "attachments",
  "attachments.send": "attachments",
  "browser.open": "browser:view",
  "browser.navigate": "browser:view",
  "browser.back": "browser:view",
  "browser.forward": "browser:view",
  "browser.reload": "browser:view",
  "browser.openExternal": "browser:view",
  "browser.bounds": "browser:view",
  "browser.visible": "browser:view",
  "browser.extract": "browser:view",
  "browser.adapter": "browser:view",
  "browser.close": "browser:view",
  "planner.threads": "planner:run",
  "planner.models": "planner:run",
  "planner.create": "planner:run",
  "planner.get": "planner:run",
  "planner.configure": "planner:run",
  "planner.rename": "planner:run",
  "planner.send": "planner:run",
  "planner.retry": "planner:run",
  "planner.stop": "planner:run",
  "planner.approve": "planner:run",
  "planner.delete": "planner:run",
  "handoff.preview": "handoff",
  "handoff.commit": "handoff",
  "models.list": "models:read",
  "commands.register": "commands",
  "commands.unregister": "commands",
  "commands.list": "commands",
  "commands.invoke": "commands",
};

export function createBridge(options: BridgeOptions): Bridge {
  const { token, granted, request } = options;

  async function send<T>(action: string, params?: unknown): Promise<T> {
    if (!actionAllowed(action, granted)) {
      throw new PluginError(403, "PLUGIN_CAPABILITY_DENIED", `插件没有执行 ${action} 的能力`);
    }
    const result = await request(`/plugin-runtime/${encodeURIComponent(action)}`, { token, params: params ?? {} });
    const value = (result as { value?: unknown } | null)?.value;
    return value as T;
  }

  return {
    call: send,
    async fetch(url: string, init?: ProxyRequestInit): Promise<ProxyResponse> {
      if (!granted.some(name => name.startsWith("net:"))) {
        throw new PluginError(403, "PLUGIN_CAPABILITY_DENIED", "插件没有网络访问能力");
      }
      return send<ProxyResponse>("net.fetch", { url, ...init });
    },
  };
}
