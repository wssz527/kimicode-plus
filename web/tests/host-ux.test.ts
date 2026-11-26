import assert from "node:assert/strict";
import crypto from "node:crypto";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import http from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { pathToFileURL } from "node:url";
import ts from "typescript";
import { createEventChannel } from "../src/native.js";
import { syncPluginHost, type PluginHostBridge } from "../src/plugin-runtime/host.js";
import { createPluginRuntime } from "../src/plugin-runtime/runtime.js";

/**
 * These tests execute the real decision logic the components call, not a regex
 * over their source: each helper is located in the file, transpiled on its own,
 * and evaluated with the same inputs a click or a keystroke produces.
 */
function extract(file: string, names: string[], extra = ""): Record<string, unknown> {
  const source = readFileSync(file, "utf8");
  const parsed = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const found = new Set(names);
  const body = parsed.statements
    .filter((statement): statement is ts.FunctionDeclaration =>
      ts.isFunctionDeclaration(statement) && found.has(statement.name?.text ?? ""))
    .map(statement => statement.getText(parsed))
    .join("\n");
  assert.ok(body, `${file} no longer declares ${names.join(", ")}`);
  const { outputText } = ts.transpileModule(`${extra}\n${body}\nexport { ${names.join(", ")} };`, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports: Record<string, unknown> = {};
  new Function("exports", "require", outputText)(exports, (id: string) => {
    throw new Error(`unexpected require ${id}`);
  });
  return exports;
}

const approvalCardFile = "src/components/workbench/ApprovalCard.tsx";
const piecesFile = "src/components/workbench/pieces.tsx";
const nativeSettingsFile = "src/components/NativeSettings.tsx";
const cacheSettingsFile = "src/components/CacheSettings.tsx";

test("an in-flight plugin view keeps its own close callback when its plugin updates", async () => {
  const renderer = readFileSync("../internal/desktop/assets/renderer.js", "utf8");
  const start = renderer.indexOf("const pluginBrowserHost =");
  const end = renderer.indexOf("// `core.fetch` carries the request body", start);
  assert.ok(start >= 0 && end > start);
  const calls: Array<{ action: string; token: string }> = [];
  const pluginTokens = new Map([["test.planner", "old"]]);
  let completeOpen = () => {};
  const opening = new Promise<void>(resolve => { completeOpen = resolve; });
  const transport = {
    request(action: string, payload: { token: string }) {
      calls.push({ action, token: payload.token });
      return action === "browser.view.open" ? opening : Promise.resolve();
    },
  };
  const host = new Function("transport", "pluginToken", `${renderer.slice(start, end)}\nreturn pluginBrowserHost;`)(
    transport, (pluginId: string) => pluginTokens.get(pluginId) ?? "",
  );
  const pending = host.open("test.planner", "planning", "https://example.com");
  pluginTokens.set("test.planner", "new");
  completeOpen();
  const close = await pending;
  await close();
  await host.navigate("test.planner", "planning", "https://example.com/next");
  assert.deepEqual(calls, [
    { action: "browser.view.open", token: "old" },
    { action: "browser.view.close", token: "old" },
    { action: "browser.view.navigate", token: "new" },
  ]);
});

// ---------------------------------------------------------------- 同屏审批

type Option = { label: string; value: string };
type Decision = "approved" | "rejected" | "cancelled";
const approval = extract(approvalCardFile, ["approvalDecision", "approvalOptions"], `
const DEFAULT_APPROVAL_OPTIONS = [
  { label: "允许", value: "approved" },
  { label: "拒绝", value: "rejected" },
];
const APPROVAL_DECISIONS = {
  approved: "approved", allow: "approved", rejected: "rejected", deny: "rejected", denied: "rejected",
  cancelled: "cancelled", cancel: "cancelled",
};
`) as {
  approvalDecision(option: Option & { decision?: Decision }, explicit?: Decision): { decision: Decision; selectedLabel?: string } | null;
  approvalOptions(options?: Option[] | null): Option[];
};

test("only recognized and explicit decisions reply to an approval", () => {
  const decision = (value: string) => approval.approvalDecision({ value, label: value });
  for (const value of ["rejected", "deny", "denied"]) assert.deepEqual(decision(value), { decision: "rejected" });
  assert.deepEqual(decision("allow"), { decision: "approved" });
  assert.deepEqual(decision("cancel"), { decision: "cancelled" });
  for (const value of ["自定义选项", "constructor", ""]) assert.equal(decision(value), null);
  assert.deepEqual(approval.approvalDecision({ value: "revise", label: "修改", decision: "rejected" }), {
    decision: "rejected", selectedLabel: "revise",
  });
  assert.deepEqual(approval.approvalDecision({ value: "仅本次", label: "仅本次" }, "approved"), {
    decision: "approved", selectedLabel: "仅本次",
  });
});

test("an empty server option list still renders the allow and refuse buttons", () => {
  assert.deepEqual(approval.approvalOptions([]), [
    { label: "允许", value: "approved" },
    { label: "拒绝", value: "rejected" },
  ]);
  assert.deepEqual(approval.approvalOptions(undefined), [
    { label: "允许", value: "approved" },
    { label: "拒绝", value: "rejected" },
  ]);
  const custom = [{ label: "只允许本次", value: "once" }];
  assert.equal(approval.approvalOptions(custom), custom);
});

// ---------------------------------------------------------------- 链接协议

const links = extract(piecesFile, ["safeHref"], `
const SAFE_LINK_PROTOCOLS = new Set(["http:", "https:", "mailto:"]);
`) as { safeHref(href?: string): string | null };

test("only explicit http, https and mailto links survive markdown rendering", () => {
  assert.equal(links.safeHref("https://example.com/a?b=1"), "https://example.com/a?b=1");
  assert.equal(links.safeHref("http://127.0.0.1:8787/x"), "http://127.0.0.1:8787/x");
  assert.equal(links.safeHref("mailto:dev@example.com"), "mailto:dev@example.com");
  assert.equal(links.safeHref("  https://example.com  "), "https://example.com");

  for (const blocked of [
    "javascript:alert(1)",
    "JAVASCRIPT:alert(1)",
    "data:text/html,<script>alert(1)</script>",
    "vbscript:msgbox(1)",
    "file:///etc/passwd",
    "//evil.example.com/path",
    "/relative/path",
    "relative/path",
    "ftp://example.com/x",
    "",
    undefined,
  ]) {
    assert.equal(links.safeHref(blocked), null, `${String(blocked)} must not become a link`);
  }
});

// -------------------------------------------------------- 未配置数字不报红

const numbers = extract(nativeSettingsFile, ["numberFieldIssue"], `
function commitNumberField(text: string, min: number, max: number): number | null {
  const trimmed = text.trim();
  if (!/^-?\\d+$/.test(trimmed)) return null;
  const value = Number(trimmed);
  if (!Number.isInteger(value) || value < min || value > max) return null;
  return value;
}
`) as {
  numberFieldIssue(value: string, text: string, min: number, max: number): string | null;
};

test("an unset number setting opens clean while an erased one explains the range", () => {
  // The core omits an unset timeout/steps, so the field starts as "" and must
  // not look like a validation failure on open.
  assert.equal(numbers.numberFieldIssue("", "", 1, 10080), null);
  assert.equal(numbers.numberFieldIssue("", "", 0, 10000), null);
  // Erasing a value that was set is the case the hint exists for.
  assert.match(String(numbers.numberFieldIssue("120", "", 1, 10080)), /不能留空/);
  assert.match(String(numbers.numberFieldIssue("256", "   ", 0, 10000)), /不能留空/);
  // A partial or out-of-range edit explains the range instead.
  assert.match(String(numbers.numberFieldIssue("120", "abc", 1, 10080)), /1–10080/);
  assert.match(String(numbers.numberFieldIssue("120", "0", 1, 10080)), /1–10080/);
  assert.equal(numbers.numberFieldIssue("120", "30", 1, 10080), null);
  assert.equal(numbers.numberFieldIssue("0", "0", 0, 10000), null);
});

// ------------------------------------------------------ 缓存策略串行写入

type Policy = {
  keepAlive: { enabled: boolean; minutes: number };
  idleKeepAlive: {
    enabled: boolean;
    defaultCount: number;
    minQuotaPercent: number;
  };
  compaction: { enabled: boolean; idleMinutes: number; contextThreshold: number };
  recovery: { enabled: boolean; intervalSeconds: number; entries: unknown[] };
};

const policyHelpers = extract(cacheSettingsFile, ["policyPatch", "createPolicyQueue"], `
const DEFAULTS = {
  keepAlive: { enabled: false, minutes: 25 },
  idleKeepAlive: {
    enabled: false,
    defaultCount: 2,
    minQuotaPercent: 10,
  },
  compaction: { enabled: false, idleMinutes: 25, contextThreshold: 60 },
  recovery: { enabled: false, intervalSeconds: 60, entries: [] },
};
function mergePolicy(base, patch) {
  if (!patch || typeof patch !== "object") return base;
  const merge = (left, right) => {
    if (!right || typeof right !== "object") return left;
    const next = { ...left };
    for (const [key, value] of Object.entries(right)) {
      next[key] = value && typeof value === "object" && !Array.isArray(value) && left?.[key] && typeof left[key] === "object"
        ? merge(left[key], value)
        : value;
    }
    return next;
  };
  return merge(base, patch);
}
`) as {
  policyPatch(base: Policy, next: Policy): Record<string, unknown> | null;
  createPolicyQueue(options: {
    read: () => Policy;
    write: (payload: Record<string, unknown>) => Promise<Policy>;
    apply: (next: Policy) => void;
    fail: (cause: unknown) => void;
    busy?: (busy: boolean) => void;
  }): (next: Policy, enabled: boolean) => Promise<void>;
};

const basePolicy = (): Policy => JSON.parse(JSON.stringify({
  keepAlive: { enabled: false, minutes: 25 },
  idleKeepAlive: {
    enabled: false,
    defaultCount: 2,
    minQuotaPercent: 10,
  },
  compaction: { enabled: false, idleMinutes: 25, contextThreshold: 60 },
  recovery: { enabled: false, intervalSeconds: 60, entries: [] },
})) as Policy;

test("a policy patch carries only the fields the edit actually changed", () => {
  const base = basePolicy();
  const minutes = { ...base, keepAlive: { ...base.keepAlive, minutes: 40 } };
  assert.deepEqual(policyHelpers.policyPatch(base, minutes), { keepAlive: { minutes: 40 } });

  const quota = basePolicy();
  quota.idleKeepAlive.minQuotaPercent = 20;
  assert.deepEqual(policyHelpers.policyPatch(base, quota), { idleKeepAlive: { minQuotaPercent: 20 } });

  // A switch carries only its own flag, never the neighbours it was built from.
  const compaction = basePolicy();
  compaction.compaction.enabled = true;
  assert.deepEqual(policyHelpers.policyPatch(base, compaction), { compaction: { enabled: true } });

  // An unchanged copy is not a write at all.
  assert.equal(policyHelpers.policyPatch(base, basePolicy()), null);
});

test("a queued write is not dropped and never resurrects an older snapshot", async () => {
  let policy = basePolicy();
  const writes: Record<string, unknown>[] = [];
  const busyStates: boolean[] = [];
  const gate: { release?: () => void } = {};

  const enqueue = policyHelpers.createPolicyQueue({
    read: () => policy,
    write: async payload => {
      writes.push(payload);
      // The first write stays in flight long enough for the second edit to land.
      if (writes.length === 1) await new Promise<void>(resolve => { gate.release = resolve; });
      // The core merges a section key by key; the stub does the same so a
      // one-field switch patch cannot be mistaken for a whole-section rewrite.
      const merged = structuredClone(policy) as Record<string, unknown>;
      for (const [section, value] of Object.entries(payload)) {
        merged[section] = { ...(merged[section] as object), ...(value as object) };
      }
      policy = merged as unknown as Policy;
      return policy;
    },
    apply: next => { policy = next; },
    fail: cause => { throw cause; },
    busy: value => busyStates.push(value),
  });

  // A number edit followed immediately by a switch: both must reach the core.
  const withMinutes = { ...policy, keepAlive: { ...policy.keepAlive, minutes: 40 } };
  const first = enqueue(withMinutes, true);
  const withToggle = { ...withMinutes, compaction: { ...policy.compaction, enabled: true } };
  const second = enqueue(withToggle, true);
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.equal(writes.length, 1, "the second write waits for the first");
  gate.release?.();
  await Promise.all([first, second]);

  assert.equal(writes.length, 2, "the queued switch was not silently dropped");
  // The payload sent for the switch carries the merged section, and it was built
  // from the policy that already contains the minute edit.
  const secondCompaction = writes[1]?.compaction as { enabled: boolean; idleMinutes: number };
  assert.equal(secondCompaction.enabled, true);
  assert.equal(secondCompaction.idleMinutes, 25);
  const firstKeepAlive = writes[0]?.keepAlive as { enabled: boolean; minutes: number };
  assert.equal(firstKeepAlive.minutes, 40);
  assert.equal((policy.compaction as { enabled: boolean }).enabled, true);
  assert.equal(policy.keepAlive.minutes, 40, "the later write kept the earlier edit");
  // Busy turns on for each accepted edit and only turns off when the queue drains.
  assert.equal(busyStates[0], true);
  assert.equal(busyStates[busyStates.length - 1], false);
  assert.equal(busyStates.filter(value => value === false).length, 1);
});

test("a write is refused until the persisted policy has been read", async () => {
  let policy = basePolicy();
  const writes: unknown[] = [];
  const enqueue = policyHelpers.createPolicyQueue({
    read: () => policy,
    write: async payload => { writes.push(payload); return policy; },
    apply: next => { policy = next; },
    fail: () => {},
  });
  const next = { ...policy, keepAlive: { ...policy.keepAlive, enabled: true } };
  await enqueue(next, false);
  assert.deepEqual(writes, [], "nothing may be written before automation.get succeeds");
  await enqueue(next, true);
  assert.equal(writes.length, 1);
});

// ------------------------------------------------------ 长轮询退避

test("the event channel backs off to a ceiling between consecutive failures", async () => {
  const delays: number[] = [];
  const original = globalThis.setTimeout;
  let attempts = 0;
  const transport = {
    async request<T>(action: string): Promise<T> {
      assert.equal(action, "events.poll");
      attempts += 1;
      if (attempts <= 4) throw new Error("core unavailable");
      return { events: [], next: attempts } as T;
    },
  };
  (globalThis as unknown as { setTimeout: typeof setTimeout }).setTimeout = ((fn: () => void, ms?: number) => {
    if (ms && ms > 100) delays.push(ms);
    return original(fn, 0);
  }) as typeof setTimeout;
  try {
    const channel = createEventChannel(transport);
    const unsubscribe = channel.subscribe("session:a", () => {});
    const deadline = Date.now() + 3000;
    while (attempts < 5 && Date.now() < deadline) await new Promise(resolve => original(resolve, 5));
    unsubscribe();
    channel.close();
  } finally {
    (globalThis as unknown as { setTimeout: typeof setTimeout }).setTimeout = original;
  }
  assert.deepEqual(delays, [1500, 3000, 6000, 10000]);
});

// ------------------------------------------------------ 渲染器重入卸载无 TDZ

test("renderer cleanup before post-mount bindings never throws TDZ and tears down listeners and plugins", async () => {
  const renderer = readFileSync("../internal/desktop/assets/renderer.js", "utf8");
  assert.doesNotMatch(renderer, /installKeepAliveNotifySuppress|keepAliveSuppress|restoreNotifyCompletion|keepalive\.suppress/);

  const tmpDir = mkdtempSync(join(tmpdir(), "ekd-renderer-tdz-"));
  const testGatesKey = "__EKD_HOST_UX_TEST_GATES__";
  try {
    const docListeners = new Map<string, Set<unknown>>();
    const winListeners = new Map<string, Set<unknown>>();
    const trackAdd = (map: Map<string, Set<unknown>>, type: string, handler: unknown, options?: unknown) => {
      assert.ok(typeof handler === "function", `listener for ${type} must be a function`);
      const key = `${type}:${Boolean(options === true || (typeof options === "object" && options && (options as { capture?: boolean }).capture))}`;
      const set = map.get(key) ?? new Set();
      set.add(handler);
      map.set(key, set);
    };
    const trackRemove = (map: Map<string, Set<unknown>>, type: string, handler: unknown, options?: unknown) => {
      assert.ok(typeof handler === "function", `removed listener for ${type} must be a function`);
      const key = `${type}:${Boolean(options === true || (typeof options === "object" && options && (options as { capture?: boolean }).capture))}`;
      const set = map.get(key);
      if (!set) return;
      set.delete(handler);
      if (set.size === 0) map.delete(key);
    };
    const listenerCount = (map: Map<string, Set<unknown>>) => {
      let total = 0;
      for (const set of map.values()) total += set.size;
      return total;
    };

    let activeStyles = 0;
    let onHeadAppend: (() => void) | null = null;
    const makeNode = (tag: string) => {
      const node = {
        tagName: tag.toUpperCase(),
        style: { cssText: "" },
        dataset: {} as Record<string, string>,
        className: "",
        children: [] as unknown[],
        isConnected: false,
        textContent: "",
        append(...items: unknown[]) {
          for (const item of items) {
            this.children.push(item);
            if (item && typeof item === "object") {
              (item as { isConnected?: boolean }).isConnected = true;
              if ((item as { tagName?: string }).tagName === "STYLE") activeStyles += 1;
            }
          }
          if (this.tagName === "HEAD" && onHeadAppend) {
            const cb = onHeadAppend;
            onHeadAppend = null;
            cb();
          }
        },
        replaceChildren(...items: unknown[]) {
          this.children = [...items];
        },
        remove() {
          if (this.tagName === "STYLE" && this.isConnected) activeStyles -= 1;
          this.isConnected = false;
        },
        setAttribute() {},
        removeAttribute() {},
        getAttribute: () => null,
        querySelector: () => null,
        querySelectorAll: () => [],
        addEventListener() {},
        removeEventListener() {},
      };
      return node;
    };

    const head = makeNode("head");
    const body = makeNode("body");
    const documentElement = { dataset: {} as Record<string, string>, style: { colorScheme: "" } };
    const documentObj = {
      title: "",
      head,
      body,
      documentElement,
      createElement: (tag: string) => makeNode(tag),
      createTextNode: (text: string) => String(text),
      addEventListener: (type: string, handler: unknown, options?: unknown) => trackAdd(docListeners, type, handler, options),
      removeEventListener: (type: string, handler: unknown, options?: unknown) => trackRemove(docListeners, type, handler, options),
      querySelector: () => null,
      querySelectorAll: () => [],
    };

    let activeChannels = 0;
    let closedChannels = 0;
    let activePluginRuntimes = 0;
    let disposedPluginRuntimes = 0;
    let activeUpdateSubs = 0;
    let unsubscribedUpdates = 0;
    let holdFeatures = false;
    const gate: { release?: () => void; blocked?: () => void } = {};
    let featuresGate = new Promise<void>(resolve => { gate.blocked = resolve; });

    (globalThis as Record<string, unknown>)[testGatesKey] = {
      onChannelOpen() { activeChannels += 1; },
      onChannelClose() { activeChannels -= 1; closedChannels += 1; },
      onPluginCreate() { activePluginRuntimes += 1; },
      onPluginDispose() { activePluginRuntimes -= 1; disposedPluginRuntimes += 1; },
      onSubOpen(scope: string) { if (scope === "update") activeUpdateSubs += 1; },
      onSubClose(scope: string) { if (scope === "update") { activeUpdateSubs -= 1; unsubscribedUpdates += 1; } },
    };

    const moduleSource = [
      `const gates = globalThis.${testGatesKey};`,
      "export function createEventChannel() {",
      "  let closed = false;",
      "  gates.onChannelOpen();",
      "  return {",
      "    subscribe(scope) {",
      "      let active = true;",
      "      gates.onSubOpen(scope);",
      "      return () => { if (!active) return; active = false; gates.onSubClose(scope); };",
      "    },",
      "    close() { if (closed) return; closed = true; gates.onChannelClose(); },",
      "  };",
      "}",
      "export function createDOMContractState() { return {}; }",
      "export function evaluateDOMContracts() { return { probes: [], byId: {}, gates: {} }; }",
      "export function createEntryMountManager() { return { sync() {}, remove() {}, dispose() {} }; }",
      "export function createNativeHost(_transport, _hooks, channel) {",
      "  return { subscribe: (scope, listener) => channel.subscribe(scope, listener), close() {} };",
      "}",
      "export function findSettingsDialogParts() { return null; }",
      "export function findOfficialSettingsPanel() { return null; }",
      "export function findOfficialSettingsSection() { return null; }",
      "export function findOfficialSettingsTab() { return null; }",
      "export function findSessionInfoKeepAliveAnchor() { return null; }",
      "export function findChatHeaderTail() { return null; }",
      "export function createPluginRuntime() {",
      "  let disposed = false;",
      "  gates.onPluginCreate();",
      "  return {",
      "    activate: async () => {},",
      "    deactivate() {},",
      "    dispose() { if (disposed) return; disposed = true; gates.onPluginDispose(); },",
      "    get() { return null; },",
      "    stats() { return {}; },",
      "    eventLoops() { return 0; },",
      "    invokeLocal() {},",
      "  };",
      "}",
      "export function loadPluginModule() { return {}; }",
      "export function pluginEntryPath(p) { return p; }",
      "export function mountSurface() { return { unmount() {} }; }",
      "export function mountMainAreaSurface() { return { setWidth() {}, width: 0, dispose() {} }; }",
    ].join("\n");

    let blobCounter = 0;
    const revokedURLs: string[] = [];
    const fakeURL = {
      createObjectURL(blob: { __text: string }) {
        const file = join(tmpDir, `mod-${++blobCounter}.mjs`);
        writeFileSync(file, blob.__text, "utf8");
        return pathToFileURL(file).href;
      },
      revokeObjectURL(url: string) { revokedURLs.push(url); },
    };
    class FakeBlob {
      __text: string;
      constructor(parts: string[]) { this.__text = parts.join(""); }
    }
    class FakeMutationObserver {
      observe() {}
      disconnect() {}
    }

    const windowObj: Record<string, unknown> = {
      addEventListener: (type: string, handler: unknown, options?: unknown) => trackAdd(winListeners, type, handler, options),
      removeEventListener: (type: string, handler: unknown, options?: unknown) => trackRemove(winListeners, type, handler, options),
      dispatchEvent() {},
      location: { href: "app://renderer/index.html" },
      localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
      fetch: async (_url: string, init: { body: string }) => {
        const req = JSON.parse(init.body) as { id: string; action: string; payload?: { path?: string } };
        if (req.action === "features.get" && holdFeatures) {
          holdFeatures = false;
          const wait = new Promise<void>(resolve => { gate.release = resolve; });
          gate.blocked?.();
          await wait;
        }
        let result: unknown = {};
        if (req.action === "features.get") {
          result = { multiSession: false };
        } else if (req.action === "core.fetch") {
          const reqPath = req.payload?.path ?? "";
          const payload = reqPath.endsWith("/plugins")
            ? {
                items: [{
                  id: "test.tdz",
                  version: "1.0.0",
                  updatedAt: "2026-11-26T00:00:00Z",
                  enabled: true,
                  apiVersion: 2,
                  capabilities: [],
                  token: "tok-tdz",
                  entry: { renderer: "renderer.js" },
                }],
              }
            : {
                source: "export function activate() {}",
                version: "1.0.0",
                updatedAt: "2026-11-26T00:00:00Z",
                capabilities: [],
              };
          result = { status: 200, bodyBase64: Buffer.from(JSON.stringify(payload)).toString("base64") };
        }
        return { ok: true, status: 200, json: async () => ({ protocol: 1, id: req.id, result }) };
      },
    };

    const install = new Function(
      "window", "document", "MutationObserver", "URL", "Blob", "CustomEvent", "createProviderEnhance",
      `${renderer}\nreturn installNativeRenderer;`,
    )(
      windowObj,
      documentObj,
      FakeMutationObserver,
      fakeURL,
      FakeBlob,
      class CustomEvent {
        type: string;
        detail: unknown;
        constructor(type: string, init?: { detail?: unknown }) {
          this.type = type;
          this.detail = init?.detail;
        }
      },
      () => ({ install() {}, remove() {}, clearDecorations() {} }),
    ) as (options: Record<string, unknown>) => Promise<{ status: string }>;

    const baseOpts = {
      endpoint: "http://bridge",
      bridgeKey: "bridge-key",
      surface: "host",
      coreURL: "http://127.0.0.1:1001",
      moduleSource,
      cssSource: "",
    };

    const currentUI = () => windowObj.__ekdNativeUI as { remove(): Promise<void> } | undefined;

    // 1. Direct cleanup while first install is suspended inside refreshFeatures() (before watchChain/onConfigSaved).
    holdFeatures = true;
    const stalled = install(baseOpts);
    await featuresGate;
    const stalledUI = currentUI();
    assert.ok(stalledUI, "native surface is published before refreshFeatures resolves");
    await stalledUI.remove();
    gate.release?.();
    assert.equal((await stalled).status, "removed");
    assert.equal(listenerCount(docListeners), 0);
    assert.equal(listenerCount(winListeners), 0);
    assert.equal(activeChannels, 0);
    assert.equal(activeStyles, 0);
    assert.equal(revokedURLs.length, 1);

    // 2. Re-entrant install while first install is stalled in refreshFeatures(), followed by full cleanup.
    featuresGate = new Promise<void>(resolve => { gate.blocked = resolve; });
    holdFeatures = true;
    const first = install({ ...baseOpts, coreURL: "http://127.0.0.1:1001" });
    await featuresGate;
    const second = install({ ...baseOpts, coreURL: "http://127.0.0.1:1002" });
    gate.release?.();
    const [firstRes, secondRes] = await Promise.all([first, second]);
    assert.equal(firstRes.status, "removed");
    assert.equal(secondRes.status, "installed");
    await (windowObj.__ekdPlugins as { sync(): Promise<void> }).sync();
    assert.equal(activeChannels, 1);
    assert.equal(activeStyles, 1);
    assert.equal(activePluginRuntimes, 1);
    assert.equal(activeUpdateSubs, 1);
    await currentUI()!.remove();
    assert.equal(activeChannels, 0);
    assert.equal(activeStyles, 0);
    assert.equal(activePluginRuntimes, 0);
    assert.equal(disposedPluginRuntimes, 1);
    assert.equal(activeUpdateSubs, 0);
    assert.equal(unsubscribedUpdates, 1);
    assert.equal(listenerCount(docListeners), 0);
    assert.equal(listenerCount(winListeners), 0);
    assert.equal(currentUI(), undefined);

    // 3. Stale before nativeTabClick / sidebarResetClick / settingsTabClick / settingsScroll are initialized.
    onHeadAppend = () => {
      void currentUI()?.remove();
    };
    const earlyStale = await install(baseOpts);
    assert.equal(earlyStale.status, "removed");
    assert.equal(activeChannels, 0);
    assert.equal(activeStyles, 0);
    assert.equal(listenerCount(docListeners), 0);
    assert.equal(listenerCount(winListeners), 0);

    // 4. Standalone settings surface returns early before watchChain/onConfigSaved; cleanup must not throw TDZ.
    const settingsRes = await install({ ...baseOpts, surface: "settings" });
    assert.equal(settingsRes.status, "installed");
    await currentUI()!.remove();
    assert.equal(activeChannels, 0);
    assert.equal(activeStyles, 0);
    assert.equal(listenerCount(docListeners), 0);
    assert.equal(listenerCount(winListeners), 0);
    assert.equal(closedChannels, 5);
  } finally {
    delete (globalThis as Record<string, unknown>)[testGatesKey];
    rmSync(tmpDir, { recursive: true, force: true });
  }
});

// ------------------------------------------------ mountPluginSettings 卸载防泄漏

test("mountPluginSettings disposes release immediately if host unmounts or plugin replaces while awaiting", async () => {
  const renderer = readFileSync("../internal/desktop/assets/renderer.js", "utf8");
  const tmpDir = mkdtempSync(join(tmpdir(), "ekd-mount-settings-"));
  const testKey = "__EKD_MOUNT_SETTINGS_TEST__";
  try {
    const makeNode = (tag: string) => ({
      tagName: tag.toUpperCase(),
      style: { cssText: "" },
      dataset: {} as Record<string, string>,
      className: "",
      children: [] as unknown[],
      isConnected: false,
      textContent: "",
      append(...items: unknown[]) {
        this.children.push(...items);
      },
      replaceChildren(...items: unknown[]) {
        this.children = [...items];
      },
      remove() {
        this.isConnected = false;
      },
      setAttribute() {},
      removeAttribute() {},
      getAttribute: () => null,
      querySelector: () => null,
      querySelectorAll: () => [],
      addEventListener() {},
      removeEventListener() {},
    });

    const documentObj = {
      title: "",
      head: makeNode("head"),
      body: makeNode("body"),
      documentElement: { dataset: {} as Record<string, string>, style: { colorScheme: "" } },
      createElement: (tag: string) => makeNode(tag),
      createTextNode: (text: string) => String(text),
      addEventListener() {},
      removeEventListener() {},
      querySelector: () => null,
      querySelectorAll: () => [],
    };

    const disposedHandles: string[] = [];
    let mountCount = 0;
    let holdMountSettings = false;
    let holdLoadAsset = false;
    const gates: {
      mountStarted?: () => void;
      releaseMount?: () => void;
      assetStarted?: () => void;
      releaseAsset?: () => void;
    } = {};

    (globalThis as Record<string, unknown>)[testKey] = {
      async mountSettings() {
        const handleId = `handle-${++mountCount}`;
        if (holdMountSettings) {
          holdMountSettings = false;
          const gate = new Promise<void>(resolve => { gates.releaseMount = resolve; });
          gates.mountStarted?.();
          await gate;
        }
        return {
          dispose() {
            disposedHandles.push(handleId);
          },
        };
      },
      async loadPluginModule() {
        if (holdLoadAsset) {
          holdLoadAsset = false;
          const gate = new Promise<void>(resolve => { gates.releaseAsset = resolve; });
          gates.assetStarted?.();
          await gate;
        }
        return { renderSettings() {} };
      },
    };

    const moduleSource = [
      `const hooks = globalThis.${testKey};`,
      "export function createEventChannel() { return { subscribe() { return () => {}; }, close() {} }; }",
      "export function createDOMContractState() { return {}; }",
      "export function evaluateDOMContracts() { return { probes: [], byId: {}, gates: {} }; }",
      "export function createEntryMountManager() { return { sync() {}, remove() {}, dispose() {} }; }",
      "export function createNativeHost(_t, _h, channel) { return { subscribe: (s, l) => channel.subscribe(s, l), close() {} }; }",
      "export function findSettingsDialogParts() { return null; }",
      "export function findOfficialSettingsPanel() { return null; }",
      "export function findOfficialSettingsSection() { return null; }",
      "export function findOfficialSettingsTab() { return null; }",
      "export function findSessionInfoKeepAliveAnchor() { return null; }",
      "export function findChatHeaderTail() { return null; }",
      "export function createPluginRuntime() {",
      "  const active = new Map();",
      "  return {",
      "    async activate(_entry, meta) {",
      "      active.set(meta.id, { mountSettings: () => hooks.mountSettings() });",
      "    },",
      "    deactivate(id) { active.delete(id); },",
      "    dispose() { active.clear(); },",
      "    get(id) { return active.get(id) || null; },",
      "    stats() { return {}; },",
      "    eventLoops() { return 0; },",
      "    invokeLocal() {},",
      "  };",
      "}",
      "export function loadPluginModule() { return hooks.loadPluginModule(); }",
      "export function pluginEntryPath(p) { return p; }",
      "export function mountSurface() { return { unmount() {} }; }",
      "export function mountMainAreaSurface() { return { setWidth() {}, width: 0, dispose() {} }; }",
    ].join("\n");

    let blobCounter = 0;
    const fakeURL = {
      createObjectURL(blob: { __text: string }) {
        const file = join(tmpDir, `mod-${++blobCounter}.mjs`);
        writeFileSync(file, blob.__text, "utf8");
        return pathToFileURL(file).href;
      },
      revokeObjectURL() {},
    };
    class FakeBlob {
      __text: string;
      constructor(parts: string[]) { this.__text = parts.join(""); }
    }
    class FakeMutationObserver {
      observe() {}
      disconnect() {}
    }

    const pluginRecord = {
      id: "test.plugin",
      enabled: true,
      version: "1.0.0",
      updatedAt: "2026-11-26T00:00:00Z",
      apiVersion: 2,
      capabilities: [],
      token: "tok-1",
      entry: { renderer: "renderer.js", settings: "settings.js" },
    };

    const windowObj: Record<string, unknown> = {
      addEventListener() {},
      removeEventListener() {},
      dispatchEvent() {},
      location: { href: "app://renderer/index.html" },
      localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
      fetch: async (_url: string, init: { body: string }) => {
        const req = JSON.parse(init.body) as { id: string; action: string; payload?: { path?: string } };
        let result: unknown = {};
        if (req.action === "features.get") {
          result = { multiSession: false };
        } else if (req.action === "core.fetch") {
          const reqPath = req.payload?.path ?? "";
          const payload = reqPath.endsWith("/plugins")
            ? { items: [pluginRecord] }
            : {
                source: "export function activate() {} export function renderSettings() {}",
                version: pluginRecord.version,
                updatedAt: pluginRecord.updatedAt,
                capabilities: pluginRecord.capabilities,
              };
          result = { status: 200, bodyBase64: Buffer.from(JSON.stringify(payload)).toString("base64") };
        }
        return { ok: true, status: 200, json: async () => ({ protocol: 1, id: req.id, result }) };
      },
    };

    const install = new Function(
      "window", "document", "MutationObserver", "URL", "Blob", "CustomEvent", "createProviderEnhance",
      `${renderer}\nreturn installNativeRenderer;`,
    )(
      windowObj,
      documentObj,
      FakeMutationObserver,
      fakeURL,
      FakeBlob,
      class CustomEvent {
        type: string;
        detail: unknown;
        constructor(type: string, init?: { detail?: unknown }) {
          this.type = type;
          this.detail = init?.detail;
        }
      },
      () => ({ install() {}, remove() {}, clearDecorations() {} }),
    ) as (options: Record<string, unknown>) => Promise<{ status: string }>;

    const baseOpts = {
      endpoint: "http://bridge",
      bridgeKey: "bridge-key",
      surface: "host",
      coreURL: "http://127.0.0.1:1001",
      moduleSource,
      cssSource: "",
    };

    const currentUI = () => windowObj.__ekdNativeUI as { remove(): Promise<void> } | undefined;
    const currentPlugins = () => windowObj.__ekdPlugins as {
      sync(): Promise<void>;
      mountSettings(id: string, element: unknown): Promise<() => void>;
    };

    // 1. Replacing an already mounted settings handle disposes the old one first.
    await install(baseOpts);
    await currentPlugins().sync();
    await currentPlugins().mountSettings("test.plugin", makeNode("div"));
    assert.deepEqual(disposedHandles, []);
    await currentPlugins().mountSettings("test.plugin", makeNode("div"));
    assert.deepEqual(disposedHandles, ["handle-1"]);

    // 2. Host remove() while active.mountSettings is pending rejects immediately and disposes the late release once it arrives.
    const started = new Promise<void>(resolve => { gates.mountStarted = resolve; });
    holdMountSettings = true;
    const pendingMount = currentPlugins().mountSettings("test.plugin", makeNode("div"));
    const pendingMountRejected = assert.rejects(pendingMount, /插件未运行或宿主已卸载/);
    await started;
    await currentUI()!.remove();
    await pendingMountRejected;
    assert.deepEqual(disposedHandles, ["handle-1", "handle-2"]);
    gates.releaseMount?.();
    await new Promise(resolve => setTimeout(resolve, 0));
    assert.deepEqual(disposedHandles, ["handle-1", "handle-2", "handle-3"]);

    // 3. Host remove() while loadPluginAsset is pending never calls active.mountSettings.
    const mountsBefore = mountCount;
    await install(baseOpts);
    await currentPlugins().sync();
    const assetGate = new Promise<void>(resolve => { gates.assetStarted = resolve; });
    holdLoadAsset = true;
    const pendingAssetMount = currentPlugins().mountSettings("test.plugin", makeNode("div"));
    const pendingAssetRejected = assert.rejects(pendingAssetMount, /插件未运行或宿主已卸载/);
    await assetGate;
    await currentUI()!.remove();
    await pendingAssetRejected;
    gates.releaseAsset?.();
    await new Promise(resolve => setTimeout(resolve, 0));
    assert.equal(mountCount, mountsBefore, "must not invoke mountSettings after host removed during asset load");

    // 4. Plugin hot-upgrade / deactivation while active.mountSettings is pending rejects immediately and disposes the late handle once it arrives.
    await install(baseOpts);
    await currentPlugins().sync();
    const upgradeStarted = new Promise<void>(resolve => { gates.mountStarted = resolve; });
    holdMountSettings = true;
    const pendingUpgradeMount = currentPlugins().mountSettings("test.plugin", makeNode("div"));
    const pendingUpgradeRejected = assert.rejects(pendingUpgradeMount, /插件未运行或宿主已卸载/);
    await upgradeStarted;
    pluginRecord.version = "1.0.1";
    await currentPlugins().sync();
    await pendingUpgradeRejected;
    gates.releaseMount?.();
    await new Promise(resolve => setTimeout(resolve, 0));
    assert.equal(disposedHandles[disposedHandles.length - 1], `handle-${mountCount}`);
    await currentUI()!.remove();
  } finally {
    delete (globalThis as Record<string, unknown>)[testKey];
    rmSync(tmpDir, { recursive: true, force: true });
  }
});

// --------------------------------------------------- KCT 独立版重装二次迁移状态

type KctReportFixture = {
  migrated: boolean;
  plistPresent: boolean;
  needsConfirmation: boolean;
};

const kctMigration = extract("src/components/PluginSettings.tsx", ["resolveKctMigrationView", "bindKctMigrationReload"]) as {
  resolveKctMigrationView(input: {
    report: KctReportFixture | null;
    confirmResult: { report: KctReportFixture; message?: string } | null;
    confirmReportSnapshot: KctReportFixture | null;
    pluginEnabled: boolean;
    loading: boolean;
    hasError: boolean;
  }): {
    state: KctReportFixture | null;
    mode: "hidden" | "loading" | "error" | "confirm" | "migrated";
    reappeared: boolean;
    showConfirmMessage: boolean;
  };
  bindKctMigrationReload(
    win: Pick<EventTarget, "addEventListener" | "removeEventListener">,
    doc: Pick<EventTarget, "addEventListener" | "removeEventListener"> & { visibilityState: string },
    reload: () => void,
  ): () => void;
};

test("syncPlugins immediately cancels hanging plugin activation on disable, hot-upgrade, or host remove without reporting activation faults", async () => {
  const renderer = readFileSync("../internal/desktop/assets/renderer.js", "utf8");
  const tmpDir = mkdtempSync(join(tmpdir(), "ekd-hanging-activate-"));
  const testKey = "__EKD_HANGING_ACTIVATE_TEST__";
  try {
    const makeNode = (tag: string) => ({
      tagName: tag.toUpperCase(),
      style: { cssText: "" },
      dataset: {} as Record<string, string>,
      className: "",
      children: [] as unknown[],
      isConnected: false,
      textContent: "",
      append(...items: unknown[]) {
        this.children.push(...items);
      },
      replaceChildren(...items: unknown[]) {
        this.children = [...items];
      },
      remove() {
        this.isConnected = false;
      },
      setAttribute() {},
      removeAttribute() {},
      getAttribute: () => null,
      querySelector: () => null,
      querySelectorAll: () => [],
      addEventListener() {},
      removeEventListener() {},
    });

    const documentObj = {
      title: "",
      head: makeNode("head"),
      body: makeNode("body"),
      documentElement: { dataset: {} as Record<string, string>, style: { colorScheme: "" } },
      createElement: (tag: string) => makeNode(tag),
      createTextNode: (text: string) => String(text),
      addEventListener() {},
      removeEventListener() {},
      querySelector: () => null,
      querySelectorAll: () => [],
    };

    const mountedNodes: string[] = [];
    const deactivatedIds: string[] = [];
    const faultReports: Array<{ path: string; body: unknown }> = [];
    let hangVersions = new Set<string>(["1.0.0"]);
    let notifyMounted: ((version: string) => void) | null = null;

    (globalThis as Record<string, unknown>)[testKey] = {
      activate(meta: { id: string; version: string }, onDispose: (fn: () => void) => void) {
        const marker = `${meta.id}@${meta.version}`;
        mountedNodes.push(marker);
        onDispose(() => {
          const idx = mountedNodes.indexOf(marker);
          if (idx >= 0) mountedNodes.splice(idx, 1);
        });
        notifyMounted?.(meta.version);
        if (hangVersions.has(meta.version)) {
          return new Promise<void>(() => {});
        }
        return Promise.resolve();
      },
      onDeactivate(id: string) {
        deactivatedIds.push(id);
      },
    };

    const moduleSource = [
      `const hooks = globalThis.${testKey};`,
      "export function createEventChannel() { return { subscribe() { return () => {}; }, close() {} }; }",
      "export function createDOMContractState() { return {}; }",
      "export function evaluateDOMContracts() { return { probes: [], byId: {}, gates: {} }; }",
      "export function createEntryMountManager() { return { sync() {}, remove() {}, dispose() {} }; }",
      "export function createNativeHost(_t, _h, channel) { return { subscribe: (s, l) => channel.subscribe(s, l), close() {} }; }",
      "export function findSettingsDialogParts() { return null; }",
      "export function findOfficialSettingsPanel() { return null; }",
      "export function findOfficialSettingsSection() { return null; }",
      "export function findOfficialSettingsTab() { return null; }",
      "export function findSessionInfoKeepAliveAnchor() { return null; }",
      "export function findChatHeaderTail() { return null; }",
      "export function createPluginRuntime() {",
      "  const active = new Map();",
      "  const activating = new Map();",
      "  return {",
      "    activate(_entry, meta) {",
      "      const cleanups = [];",
      "      const dispose = () => { while (cleanups.length) cleanups.pop()(); };",
      "      let rejectPending;",
      "      const entry = { dispose, cancelled: false, reject: err => rejectPending?.(err) };",
      "      activating.set(meta.id, entry);",
      "      return new Promise((resolve, reject) => {",
      "        rejectPending = reject;",
      "        Promise.resolve(hooks.activate(meta, fn => cleanups.push(fn))).then(() => {",
      "          if (activating.get(meta.id) === entry) activating.delete(meta.id);",
      "          if (entry.cancelled) { dispose(); const e = new Error('插件已停用'); e.code = 'PLUGIN_DISABLED'; reject(e); return; }",
      "          active.set(meta.id, { dispose });",
      "          resolve(active.get(meta.id));",
      "        }, err => {",
      "          if (activating.get(meta.id) === entry) activating.delete(meta.id);",
      "          dispose();",
      "          reject(err);",
      "        });",
      "      });",
      "    },",
      "    deactivate(id) {",
      "      hooks.onDeactivate(id);",
      "      const inflight = activating.get(id);",
      "      if (inflight) {",
      "        inflight.cancelled = true;",
      "        activating.delete(id);",
      "        inflight.dispose();",
      "        const e = new Error('插件已停用');",
      "        e.code = 'PLUGIN_DISABLED';",
      "        inflight.reject(e);",
      "      }",
      "      active.get(id)?.dispose();",
      "      active.delete(id);",
      "    },",
      "    dispose() {",
      "      for (const id of [...activating.keys()]) this.deactivate(id);",
      "      for (const id of [...active.keys()]) this.deactivate(id);",
      "    },",
      "    get(id) { return active.get(id) || null; },",
      "    stats() { return {}; },",
      "    eventLoops() { return 0; },",
      "    invokeLocal() {},",
      "  };",
      "}",
      "export function loadPluginModule() { return Promise.resolve({ activate() {} }); }",
      "export function pluginEntryPath(p) { return p; }",
      "export function mountSurface() { return { unmount() {} }; }",
      "export function mountMainAreaSurface() { return { setWidth() {}, width: 0, dispose() {} }; }",
    ].join("\n");

    let blobCounter = 0;
    const fakeURL = {
      createObjectURL(blob: { __text: string }) {
        const file = join(tmpDir, `hang-mod-${++blobCounter}.mjs`);
        writeFileSync(file, blob.__text, "utf8");
        return pathToFileURL(file).href;
      },
      revokeObjectURL() {},
    };
    class FakeBlob {
      __text: string;
      constructor(parts: unknown[]) {
        this.__text = parts.map(String).join("");
      }
    }
    class FakeMutationObserver {
      observe() {}
      disconnect() {}
    }

    let pluginRecord = {
      id: "test.hanging",
      version: "1.0.0",
      updatedAt: "2026-11-26T00:00:00Z",
      enabled: true,
      apiVersion: 2,
      capabilities: ["ui:sidebar"],
      token: "tok-hang",
      entry: { renderer: "renderer.js" },
    };

    const windowObj: Record<string, unknown> = {
      addEventListener() {},
      removeEventListener() {},
      dispatchEvent() {},
      requestAnimationFrame: (cb: () => void) => setTimeout(cb, 0),
      cancelAnimationFrame: (id: ReturnType<typeof setTimeout>) => clearTimeout(id),
      fetch: async (_url: string, init?: { body?: string }) => {
        const req = JSON.parse(init?.body ?? "{}") as {
          id: string;
          action: string;
          payload?: { path?: string; method?: string; bodyBase64?: string };
        };
        let result: unknown = {};
        if (req.action === "core.fetch") {
          const reqPath = req.payload?.path ?? "";
          if (reqPath.includes("/fault")) {
            faultReports.push({
              path: reqPath,
              body: req.payload?.bodyBase64 ? JSON.parse(Buffer.from(req.payload.bodyBase64, "base64").toString("utf8")) : null,
            });
          }
          const payload = reqPath.endsWith("/plugins")
            ? { items: [{ ...pluginRecord }] }
            : {
                source: "export function activate() {}",
                version: pluginRecord.version,
                updatedAt: pluginRecord.updatedAt,
                capabilities: pluginRecord.capabilities,
              };
          result = { status: 200, bodyBase64: Buffer.from(JSON.stringify(payload)).toString("base64") };
        }
        return { ok: true, status: 200, json: async () => ({ protocol: 1, id: req.id, result }) };
      },
    };

    const install = new Function(
      "window", "document", "MutationObserver", "URL", "Blob", "CustomEvent", "createProviderEnhance",
      `${renderer}\nreturn installNativeRenderer;`,
    )(
      windowObj,
      documentObj,
      FakeMutationObserver,
      fakeURL,
      FakeBlob,
      class CustomEvent {
        type: string;
        detail: unknown;
        constructor(type: string, init?: { detail?: unknown }) {
          this.type = type;
          this.detail = init?.detail;
        }
      },
      () => ({ install() {}, remove() {}, clearDecorations() {} }),
    ) as (options: Record<string, unknown>) => Promise<{ status: string }>;

    const currentUI = () => windowObj.__ekdNativeUI as { remove(): Promise<void> } | undefined;
    const currentPlugins = () => windowObj.__ekdPlugins as { sync(): Promise<void> };
    const waitForMounted = (version: string) =>
      new Promise<void>(resolve => {
        if (mountedNodes.includes(`test.hanging@${version}`)) {
          resolve();
          return;
        }
        notifyMounted = mountedVersion => {
          if (mountedVersion === version) {
            notifyMounted = null;
            resolve();
          }
        };
      });

    // Case 1: Disable plugin while activate() is hanging after mounting UI.
    const mountedV1 = waitForMounted("1.0.0");
    await install({
      endpoint: "http://bridge",
      bridgeKey: "bridge-key",
      surface: "host",
      coreURL: "http://127.0.0.1:1001",
      moduleSource,
      cssSource: "",
    });
    await mountedV1;
    assert.deepEqual(mountedNodes, ["test.hanging@1.0.0"]);

    pluginRecord.enabled = false;
    await currentPlugins().sync();
    assert.deepEqual(mountedNodes, [], "disabled hanging plugin must release mounted DOM immediately");
    assert.ok(deactivatedIds.includes("test.hanging"));
    assert.deepEqual(faultReports, [], "cancelling activation on disable must not report activation fault");

    // Case 2: Hot-upgrade plugin version while activate() is hanging on old version.
    hangVersions = new Set(["1.0.0"]);
    pluginRecord = { ...pluginRecord, enabled: true, version: "1.0.0", updatedAt: "2026-11-26T00:01:00Z" };
    const mountedAgain = waitForMounted("1.0.0");
    const stuckSync = currentPlugins().sync();
    await mountedAgain;
    assert.deepEqual(mountedNodes, ["test.hanging@1.0.0"]);

    pluginRecord = { ...pluginRecord, enabled: true, version: "1.0.1", updatedAt: "2026-11-26T00:02:00Z" };
    await Promise.all([stuckSync, currentPlugins().sync()]);
    assert.deepEqual(mountedNodes, ["test.hanging@1.0.1"], "upgraded version must replace cancelled hanging activation");
    assert.deepEqual(faultReports, [], "cancelling activation on hot-upgrade must not report activation fault");

    // Case 3: Host remove() while another version is hanging cancels activation cleanly.
    hangVersions = new Set(["1.0.2"]);
    pluginRecord = { ...pluginRecord, enabled: true, version: "1.0.2", updatedAt: "2026-11-26T00:03:00Z" };
    const mountedV2 = waitForMounted("1.0.2");
    const removedSync = currentPlugins().sync();
    await mountedV2;
    assert.deepEqual(mountedNodes, ["test.hanging@1.0.2"]);
    await currentUI()!.remove();
    await removedSync;
    assert.deepEqual(mountedNodes, []);
    assert.deepEqual(faultReports, []);
  } finally {
    delete (globalThis as Record<string, unknown>)[testKey];
    rmSync(tmpDir, { recursive: true, force: true });
  }
});

test("bindKctMigrationReload re-checks on window focus and visible document transitions, and cleans up listeners", () => {
  const win = new EventTarget();
  const doc = Object.assign(new EventTarget(), { visibilityState: "hidden" });
  let reloads = 0;
  const dispose = kctMigration.bindKctMigrationReload(win, doc, () => {
    reloads += 1;
  });

  win.dispatchEvent(new Event("focus"));
  assert.equal(reloads, 1);

  doc.visibilityState = "hidden";
  doc.dispatchEvent(new Event("visibilitychange"));
  assert.equal(reloads, 1, "hidden transition must not trigger reload");

  doc.visibilityState = "visible";
  doc.dispatchEvent(new Event("visibilitychange"));
  assert.equal(reloads, 2, "visible transition must trigger reload");

  dispose();
  win.dispatchEvent(new Event("focus"));
  doc.dispatchEvent(new Event("visibilitychange"));
  assert.equal(reloads, 2, "disposed listeners must not fire");
});

test("KCT migration panel prioritizes needsConfirmation even when migrated or pluginEnabled, and never lets stale confirm result shadow fresh report", () => {
  const initialBlocked: KctReportFixture = { migrated: false, plistPresent: true, needsConfirmation: true };
  const afterFirstConfirm: KctReportFixture = { migrated: true, plistPresent: false, needsConfirmation: false };
  const reinstalledLoader: KctReportFixture = { migrated: true, plistPresent: true, needsConfirmation: true };

  // Reinstalled standalone KCT with marker + plist present: disabled plugin MUST show confirm, not migrated.
  const disabledReappear = kctMigration.resolveKctMigrationView({
    report: reinstalledLoader,
    confirmResult: null,
    confirmReportSnapshot: null,
    pluginEnabled: false,
    loading: false,
    hasError: false,
  });
  assert.equal(disabledReappear.mode, "confirm");
  assert.equal(disabledReappear.reappeared, true);

  // Reinstalled standalone KCT while plugin is already enabled MUST still show confirm alert!
  const enabledReappear = kctMigration.resolveKctMigrationView({
    report: reinstalledLoader,
    confirmResult: null,
    confirmReportSnapshot: null,
    pluginEnabled: true,
    loading: false,
    hasError: false,
  });
  assert.equal(enabledReappear.mode, "confirm");
  assert.equal(enabledReappear.reappeared, true);

  // Same open detail page after first confirm: fresh reloaded report with needsConfirmation:true MUST override stale confirmResult.
  const samePageReload = kctMigration.resolveKctMigrationView({
    report: reinstalledLoader,
    confirmResult: { report: afterFirstConfirm, message: "独立版后台加载器已停用。" },
    confirmReportSnapshot: initialBlocked,
    pluginEnabled: true,
    loading: false,
    hasError: false,
  });
  assert.equal(samePageReload.mode, "confirm");
  assert.equal(samePageReload.reappeared, true);
  assert.equal(samePageReload.showConfirmMessage, false);
  assert.equal(samePageReload.state, reinstalledLoader);

  // Immediately after confirm (before report.reload returns a new object), confirmResult.report avoids flashing back.
  const rightAfterConfirm = kctMigration.resolveKctMigrationView({
    report: initialBlocked,
    confirmResult: { report: afterFirstConfirm, message: "独立版后台加载器已停用。" },
    confirmReportSnapshot: initialBlocked,
    pluginEnabled: false,
    loading: true,
    hasError: false,
  });
  assert.equal(rightAfterConfirm.mode, "migrated");
  assert.equal(rightAfterConfirm.showConfirmMessage, true);

  // When no conflict exists and plugin is enabled, panel stays hidden to keep enabled UI clean.
  const cleanEnabled = kctMigration.resolveKctMigrationView({
    report: afterFirstConfirm,
    confirmResult: null,
    confirmReportSnapshot: null,
    pluginEnabled: true,
    loading: false,
    hasError: false,
  });
  assert.equal(cleanEnabled.mode, "hidden");

  // When disabled and cleanly migrated with no plist conflict, keep showing migrated notice.
  const cleanDisabledMigrated = kctMigration.resolveKctMigrationView({
    report: afterFirstConfirm,
    confirmResult: null,
    confirmReportSnapshot: null,
    pluginEnabled: false,
    loading: false,
    hasError: false,
  });
  assert.equal(cleanDisabledMigrated.mode, "migrated");
});

test("hanging activation of plugin A does not block disabling, hot-upgrading, or enabling plugin B", async () => {
  const renderer = readFileSync("../internal/desktop/assets/renderer.js", "utf8");
  const tmpDir = mkdtempSync(join(tmpdir(), "ekd-parallel-plugin-sync-"));
  const testKey = "__EKD_PARALLEL_PLUGIN_SYNC_TEST__";
  try {
    const makeNode = (tag: string) => ({
      tagName: tag.toUpperCase(),
      style: { cssText: "" },
      dataset: {} as Record<string, string>,
      className: "",
      children: [] as unknown[],
      isConnected: false,
      textContent: "",
      append(...items: unknown[]) {
        this.children.push(...items);
      },
      replaceChildren(...items: unknown[]) {
        this.children = [...items];
      },
      remove() {
        this.isConnected = false;
      },
      setAttribute() {},
      removeAttribute() {},
      getAttribute: () => null,
      querySelector: () => null,
      querySelectorAll: () => [],
      addEventListener() {},
      removeEventListener() {},
    });

    const documentObj = {
      title: "",
      head: makeNode("head"),
      body: makeNode("body"),
      documentElement: { dataset: {} as Record<string, string>, style: { colorScheme: "" } },
      createElement: (tag: string) => makeNode(tag),
      createTextNode: (text: string) => String(text),
      addEventListener() {},
      removeEventListener() {},
      querySelector: () => null,
      querySelectorAll: () => [],
    };

    const mountedNodes: string[] = [];
    const deactivatedIds: string[] = [];
    const faultReports: Array<{ path: string; body: unknown }> = [];
    const mountWaiters = new Map<string, () => void>();

    const waitForMarker = (marker: string) =>
      new Promise<void>(resolve => {
        if (mountedNodes.includes(marker)) {
          resolve();
          return;
        }
        mountWaiters.set(marker, resolve);
      });

    (globalThis as Record<string, unknown>)[testKey] = {
      activate(meta: { id: string; version: string }, onDispose: (fn: () => void) => void) {
        const marker = `${meta.id}@${meta.version}`;
        mountedNodes.push(marker);
        onDispose(() => {
          const idx = mountedNodes.indexOf(marker);
          if (idx >= 0) mountedNodes.splice(idx, 1);
        });
        const waiter = mountWaiters.get(marker);
        if (waiter) {
          mountWaiters.delete(marker);
          waiter();
        }
        if (meta.id === "test.pluginA") {
          return new Promise<void>(() => {});
        }
        return Promise.resolve();
      },
      onDeactivate(id: string) {
        deactivatedIds.push(id);
      },
    };

    const moduleSource = [
      `const hooks = globalThis.${testKey};`,
      "export function createEventChannel() { return { subscribe() { return () => {}; }, close() {} }; }",
      "export function createDOMContractState() { return {}; }",
      "export function evaluateDOMContracts() { return { probes: [], byId: {}, gates: {} }; }",
      "export function createEntryMountManager() { return { sync() {}, remove() {}, dispose() {} }; }",
      "export function createNativeHost(_t, _h, channel) { return { subscribe: (s, l) => channel.subscribe(s, l), close() {} }; }",
      "export function findSettingsDialogParts() { return null; }",
      "export function findOfficialSettingsPanel() { return null; }",
      "export function findOfficialSettingsSection() { return null; }",
      "export function findOfficialSettingsTab() { return null; }",
      "export function findSessionInfoKeepAliveAnchor() { return null; }",
      "export function findChatHeaderTail() { return null; }",
      "export function createPluginRuntime() {",
      "  const active = new Map();",
      "  const activating = new Map();",
      "  return {",
      "    activate(_entry, meta) {",
      "      const cleanups = [];",
      "      const dispose = () => { while (cleanups.length) cleanups.pop()(); };",
      "      let rejectPending;",
      "      const entry = { dispose, cancelled: false, reject: err => rejectPending?.(err) };",
      "      activating.set(meta.id, entry);",
      "      return new Promise((resolve, reject) => {",
      "        rejectPending = reject;",
      "        Promise.resolve(hooks.activate(meta, fn => cleanups.push(fn))).then(() => {",
      "          if (activating.get(meta.id) === entry) activating.delete(meta.id);",
      "          if (entry.cancelled) { dispose(); const e = new Error('插件已停用'); e.code = 'PLUGIN_DISABLED'; reject(e); return; }",
      "          active.set(meta.id, { dispose });",
      "          resolve(active.get(meta.id));",
      "        }, err => {",
      "          if (activating.get(meta.id) === entry) activating.delete(meta.id);",
      "          dispose();",
      "          reject(err);",
      "        });",
      "      });",
      "    },",
      "    deactivate(id) {",
      "      hooks.onDeactivate(id);",
      "      const inflight = activating.get(id);",
      "      if (inflight) {",
      "        inflight.cancelled = true;",
      "        activating.delete(id);",
      "        inflight.dispose();",
      "        const e = new Error('插件已停用');",
      "        e.code = 'PLUGIN_DISABLED';",
      "        inflight.reject(e);",
      "      }",
      "      active.get(id)?.dispose();",
      "      active.delete(id);",
      "    },",
      "    dispose() {",
      "      for (const id of [...activating.keys()]) this.deactivate(id);",
      "      for (const id of [...active.keys()]) this.deactivate(id);",
      "    },",
      "    get(id) { return active.get(id) || null; },",
      "    stats() { return {}; },",
      "    eventLoops() { return 0; },",
      "    invokeLocal() {},",
      "  };",
      "}",
      "export function loadPluginModule() { return Promise.resolve({ activate() {} }); }",
      "export function pluginEntryPath(p) { return p; }",
      "export function mountSurface() { return { unmount() {} }; }",
      "export function mountMainAreaSurface() { return { setWidth() {}, width: 0, dispose() {} }; }",
    ].join("\n");

    let blobCounter = 0;
    const fakeURL = {
      createObjectURL(blob: { __text: string }) {
        const file = join(tmpDir, `parallel-mod-${++blobCounter}.mjs`);
        writeFileSync(file, blob.__text, "utf8");
        return pathToFileURL(file).href;
      },
      revokeObjectURL() {},
    };
    class FakeBlob {
      __text: string;
      constructor(parts: unknown[]) {
        this.__text = parts.map(String).join("");
      }
    }
    class FakeMutationObserver {
      observe() {}
      disconnect() {}
    }

    let pluginRecords = [
      {
        id: "test.pluginA",
        version: "1.0.0",
        updatedAt: "2026-11-26T00:00:00Z",
        enabled: true,
        apiVersion: 2,
        capabilities: ["ui:sidebar"],
        token: "tok-a",
        entry: { renderer: "renderer.js" },
      },
      {
        id: "test.pluginB",
        version: "1.0.0",
        updatedAt: "2026-11-26T00:00:00Z",
        enabled: true,
        apiVersion: 2,
        capabilities: ["ui:sidebar"],
        token: "tok-b-1",
        entry: { renderer: "renderer.js" },
      },
    ];

    const windowObj: Record<string, unknown> = {
      addEventListener() {},
      removeEventListener() {},
      dispatchEvent() {},
      requestAnimationFrame: (cb: () => void) => setTimeout(cb, 0),
      cancelAnimationFrame: (id: ReturnType<typeof setTimeout>) => clearTimeout(id),
      location: { href: "app://renderer/index.html" },
      localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
      fetch: async (_url: string, init: { body: string }) => {
        const req = JSON.parse(init.body) as { id: string; action: string; payload?: { path?: string; bodyBase64?: string } };
        let result: unknown = {};
        if (req.action === "features.list") result = { multiSession: false };
        else if (req.action === "core.fetch") {
          const reqPath = req.payload?.path ?? "";
          if (reqPath.includes("/plugin-runtime/report.error")) {
            faultReports.push({
              path: reqPath,
              body: req.payload?.bodyBase64 ? JSON.parse(Buffer.from(req.payload.bodyBase64, "base64").toString("utf8")) : null,
            });
          }
          let payload: unknown;
          if (reqPath.endsWith("/plugins")) {
            payload = { items: pluginRecords.map(item => ({ ...item })) };
          } else {
            const match = reqPath.match(/\/plugins\/([^/]+)\/assets\//);
            const targetId = match ? decodeURIComponent(match[1]!) : "";
            const rec = pluginRecords.find(item => item.id === targetId) ?? pluginRecords[0]!;
            payload = {
              source: "export function activate() {}",
              version: rec.version,
              updatedAt: rec.updatedAt,
              capabilities: rec.capabilities,
            };
          }
          result = { status: 200, bodyBase64: Buffer.from(JSON.stringify(payload)).toString("base64") };
        }
        return { ok: true, status: 200, json: async () => ({ protocol: 1, id: req.id, result }) };
      },
    };

    const install = new Function(
      "window", "document", "MutationObserver", "URL", "Blob", "CustomEvent", "createProviderEnhance",
      `${renderer}\nreturn installNativeRenderer;`,
    )(
      windowObj,
      documentObj,
      FakeMutationObserver,
      fakeURL,
      FakeBlob,
      class CustomEvent {
        type: string;
        detail: unknown;
        constructor(type: string, init?: { detail?: unknown }) {
          this.type = type;
          this.detail = init?.detail;
        }
      },
      () => ({ install() {}, remove() {}, clearDecorations() {} }),
    ) as (options: Record<string, unknown>) => Promise<{ status: string }>;

    const currentUI = () => windowObj.__ekdNativeUI as { remove(): Promise<void> } | undefined;
    const currentPlugins = () => windowObj.__ekdPlugins as { sync(): Promise<void> };
    const withTimeout = async (promise: Promise<unknown>, label: string) => {
      let timer: ReturnType<typeof setTimeout> | null = null;
      try {
        await Promise.race([
          promise,
          new Promise((_, reject) => {
            timer = setTimeout(() => reject(new Error(`timed out waiting for ${label}`)), 1000);
          }),
        ]);
      } finally {
        if (timer) clearTimeout(timer);
      }
    };

    const mountedA = waitForMarker("test.pluginA@1.0.0");
    const mountedB = waitForMarker("test.pluginB@1.0.0");
    await install({
      endpoint: "http://bridge",
      bridgeKey: "bridge-key",
      surface: "host",
      coreURL: "http://127.0.0.1:1001",
      moduleSource,
      cssSource: "",
    });
    await Promise.all([mountedA, mountedB]);
    assert.deepEqual([...mountedNodes].sort(), ["test.pluginA@1.0.0", "test.pluginB@1.0.0"]);

    // 1. Disable running plugin B while plugin A is still hanging in activate().
    pluginRecords = [
      pluginRecords[0]!,
      { ...pluginRecords[1]!, enabled: false },
    ];
    await withTimeout(currentPlugins().sync(), "disable B while A hangs");
    assert.deepEqual(mountedNodes, ["test.pluginA@1.0.0"], "disabling B must complete immediately without being blocked by hanging A");
    assert.ok(deactivatedIds.includes("test.pluginB"));

    // 2. Re-enable and then hot-upgrade plugin B while plugin A is still hanging.
    pluginRecords = [
      pluginRecords[0]!,
      { ...pluginRecords[1]!, enabled: true, version: "1.0.0", updatedAt: "2026-11-26T00:01:00Z" },
    ];
    await withTimeout(currentPlugins().sync(), "re-enable B while A hangs");
    assert.deepEqual([...mountedNodes].sort(), ["test.pluginA@1.0.0", "test.pluginB@1.0.0"]);

    pluginRecords = [
      pluginRecords[0]!,
      { ...pluginRecords[1]!, enabled: true, version: "1.0.1", updatedAt: "2026-11-26T00:02:00Z", token: "tok-b-2" },
    ];
    await withTimeout(currentPlugins().sync(), "hot-upgrade B while A hangs");
    assert.deepEqual([...mountedNodes].sort(), ["test.pluginA@1.0.0", "test.pluginB@1.0.1"], "upgrading B must complete immediately while A hangs");

    // 3. Disabling hanging A cancels A cleanly while keeping upgraded B running.
    pluginRecords = [
      { ...pluginRecords[0]!, enabled: false },
      pluginRecords[1]!,
    ];
    await withTimeout(currentPlugins().sync(), "disable hanging A");
    assert.deepEqual(mountedNodes, ["test.pluginB@1.0.1"]);
    assert.deepEqual(faultReports, []);

    await currentUI()!.remove();
    assert.deepEqual(mountedNodes, []);
  } finally {
    delete (globalThis as Record<string, unknown>)[testKey];
    rmSync(tmpDir, { recursive: true, force: true });
  }
});

test("late completion of stale mountPluginSettings for the same plugin disposes only itself and leaves reopened panel B intact", async () => {
  const renderer = readFileSync("../internal/desktop/assets/renderer.js", "utf8");
  const tmpDir = mkdtempSync(join(tmpdir(), "ekd-settings-reopen-race-"));
  const testKey = "__EKD_SETTINGS_REOPEN_RACE_TEST__";
  try {
    interface MiniElem {
      tagName: string;
      style: { cssText: string };
      dataset: Record<string, string>;
      className: string;
      children: MiniElem[];
      isConnected: boolean;
      textContent: string;
      append(...items: MiniElem[]): void;
      replaceChildren(...items: MiniElem[]): void;
      remove(): void;
      setAttribute(): void;
      removeAttribute(): void;
      getAttribute(): null;
      querySelector(): null;
      querySelectorAll(): never[];
      addEventListener(): void;
      removeEventListener(): void;
    }

    const makeNode = (tag: string, text = ""): MiniElem => ({
      tagName: tag.toUpperCase(),
      style: { cssText: "" },
      dataset: {},
      className: "",
      children: [],
      isConnected: false,
      textContent: text,
      append(...items: MiniElem[]) {
        this.children.push(...items);
      },
      replaceChildren(...items: MiniElem[]) {
        this.children = [...items];
      },
      remove() {
        this.isConnected = false;
      },
      setAttribute() {},
      removeAttribute() {},
      getAttribute: () => null,
      querySelector: () => null,
      querySelectorAll: () => [],
      addEventListener() {},
      removeEventListener() {},
    });

    const documentObj = {
      title: "",
      head: makeNode("head"),
      body: makeNode("body"),
      documentElement: { dataset: {} as Record<string, string>, style: { colorScheme: "" } },
      createElement: (tag: string) => makeNode(tag),
      createTextNode: (text: string) => String(text),
      addEventListener() {},
      removeEventListener() {},
      querySelector: () => null,
      querySelectorAll: () => [],
    };

    const disposedHandles: string[] = [];
    const pendingMounts = new Map<string, { started: Promise<void>; release: () => void }>();
    let nextLabel = "";

    const holdNextMount = (label: string) => {
      let notifyStarted!: () => void;
      let release!: () => void;
      const started = new Promise<void>(resolve => { notifyStarted = resolve; });
      const gate = new Promise<void>(resolve => { release = resolve; });
      pendingMounts.set(label, { started, release });
      return {
        started,
        release,
        gate,
        notifyStarted,
      };
    };

    const holdGates = new Map<string, { gate: Promise<void>; notifyStarted: () => void }>();

    (globalThis as Record<string, unknown>)[testKey] = {
      async mountSettings(element: MiniElem) {
        const label = nextLabel;
        const root = makeNode("div", `settings-${label}`);
        element.append(root);
        const held = holdGates.get(label);
        if (held) {
          holdGates.delete(label);
          held.notifyStarted();
          await held.gate;
        }
        return {
          dispose() {
            disposedHandles.push(label);
            element.children = element.children.filter(child => child !== root);
          },
        };
      },
    };

    const moduleSource = [
      `const hooks = globalThis.${testKey};`,
      "export function createEventChannel() { return { subscribe() { return () => {}; }, close() {} }; }",
      "export function createDOMContractState() { return {}; }",
      "export function evaluateDOMContracts() { return { probes: [], byId: {}, gates: {} }; }",
      "export function createEntryMountManager() { return { sync() {}, remove() {}, dispose() {} }; }",
      "export function createNativeHost(_t, _h, channel) { return { subscribe: (s, l) => channel.subscribe(s, l), close() {} }; }",
      "export function findSettingsDialogParts() { return null; }",
      "export function findOfficialSettingsPanel() { return null; }",
      "export function findOfficialSettingsSection() { return null; }",
      "export function findOfficialSettingsTab() { return null; }",
      "export function findSessionInfoKeepAliveAnchor() { return null; }",
      "export function findChatHeaderTail() { return null; }",
      "export function createPluginRuntime() {",
      "  const active = new Map();",
      "  return {",
      "    async activate(_entry, meta) {",
      "      active.set(meta.id, { mountSettings: (_mod, el) => hooks.mountSettings(el) });",
      "    },",
      "    deactivate(id) { active.delete(id); },",
      "    dispose() { active.clear(); },",
      "    get(id) { return active.get(id) || null; },",
      "    stats() { return {}; },",
      "    eventLoops() { return 0; },",
      "    invokeLocal() {},",
      "  };",
      "}",
      "export function loadPluginModule() { return Promise.resolve({ activate() {}, renderSettings() {} }); }",
      "export function pluginEntryPath(p) { return p; }",
      "export function mountSurface() { return { unmount() {} }; }",
      "export function mountMainAreaSurface() { return { setWidth() {}, width: 0, dispose() {} }; }",
    ].join("\n");

    let blobCounter = 0;
    const fakeURL = {
      createObjectURL(blob: { __text: string }) {
        const file = join(tmpDir, `settings-race-${++blobCounter}.mjs`);
        writeFileSync(file, blob.__text, "utf8");
        return pathToFileURL(file).href;
      },
      revokeObjectURL() {},
    };
    class FakeBlob {
      __text: string;
      constructor(parts: unknown[]) {
        this.__text = parts.map(String).join("");
      }
    }
    class FakeMutationObserver {
      observe() {}
      disconnect() {}
    }

    const pluginRecord = {
      id: "test.plugin",
      version: "1.0.0",
      updatedAt: "2026-11-26T00:00:00Z",
      enabled: true,
      apiVersion: 2,
      capabilities: ["ui:settings"],
      token: "tok-1",
      entry: { renderer: "renderer.js", settings: "settings.js" },
    };

    const windowObj: Record<string, unknown> = {
      addEventListener() {},
      removeEventListener() {},
      dispatchEvent() {},
      requestAnimationFrame: (cb: () => void) => setTimeout(cb, 0),
      cancelAnimationFrame: (id: ReturnType<typeof setTimeout>) => clearTimeout(id),
      location: { href: "app://renderer/index.html" },
      localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
      fetch: async (_url: string, init: { body: string }) => {
        const req = JSON.parse(init.body) as { id: string; action: string; payload?: { path?: string } };
        let result: unknown = {};
        if (req.action === "features.list") result = { multiSession: false };
        else if (req.action === "core.fetch") {
          const reqPath = req.payload?.path ?? "";
          const payload = reqPath.endsWith("/plugins")
            ? { items: [pluginRecord] }
            : {
                source: "export function activate() {} export function renderSettings() {}",
                version: pluginRecord.version,
                updatedAt: pluginRecord.updatedAt,
                capabilities: pluginRecord.capabilities,
              };
          result = { status: 200, bodyBase64: Buffer.from(JSON.stringify(payload)).toString("base64") };
        }
        return { ok: true, status: 200, json: async () => ({ protocol: 1, id: req.id, result }) };
      },
    };

    const install = new Function(
      "window", "document", "MutationObserver", "URL", "Blob", "CustomEvent", "createProviderEnhance",
      `${renderer}\nreturn installNativeRenderer;`,
    )(
      windowObj,
      documentObj,
      FakeMutationObserver,
      fakeURL,
      FakeBlob,
      class CustomEvent {
        type: string;
        detail: unknown;
        constructor(type: string, init?: { detail?: unknown }) {
          this.type = type;
          this.detail = init?.detail;
        }
      },
      () => ({ install() {}, remove() {}, clearDecorations() {} }),
    ) as (options: Record<string, unknown>) => Promise<{ status: string }>;

    const currentUI = () => windowObj.__ekdNativeUI as { remove(): Promise<void> } | undefined;
    const currentPlugins = () => windowObj.__ekdPlugins as {
      sync(): Promise<void>;
      mountSettings(id: string, element: unknown, options?: { signal?: AbortSignal }): Promise<() => void>;
    };

    await install({
      endpoint: "http://bridge",
      bridgeKey: "bridge-key",
      surface: "host",
      coreURL: "http://127.0.0.1:1001",
      moduleSource,
      cssSource: "",
    });
    await currentPlugins().sync();

    // 1. With AbortController (matching PluginSettingsPanel): open slow A -> close A -> open fast B -> A resolves late.
    const gateA = holdNextMount("A");
    holdGates.set("A", { gate: gateA.gate, notifyStarted: gateA.notifyStarted });
    const elA = makeNode("div");
    const controllerA = new AbortController();
    nextLabel = "A";
    const mountA = currentPlugins().mountSettings("test.plugin", elA, { signal: controllerA.signal });
    const mountARejected = assert.rejects(mountA, /插件设置页挂载已取消/);
    await gateA.started;

    // Close panel A before its mount resolves, then reopen panel B for the same plugin.
    controllerA.abort();
    await mountARejected;
    const elB = makeNode("div");
    const controllerB = new AbortController();
    nextLabel = "B";
    const disposeB = await currentPlugins().mountSettings("test.plugin", elB, { signal: controllerB.signal });
    assert.deepEqual(elB.children.map(node => node.textContent), ["settings-B"]);

    // Late completion of panel A must dispose ONLY A and leave panel B completely untouched.
    gateA.release();
    await new Promise(resolve => setTimeout(resolve, 0));
    assert.deepEqual(disposedHandles, ["A"], "only stale panel A should be disposed");
    assert.deepEqual(elA.children, [], "stale panel A DOM should be cleared");
    assert.deepEqual(elB.children.map(node => node.textContent), ["settings-B"], "reopened panel B DOM must remain intact");

    // Closing panel B via controller.abort() + disposeB() disposes B cleanly exactly once.
    controllerB.abort();
    disposeB();
    assert.deepEqual(disposedHandles, ["A", "B"]);
    assert.deepEqual(elB.children, []);

    // 2. Without AbortSignal (backward-compatible 2-arg call): slow C superseded by fast D also keeps D intact.
    const gateC = holdNextMount("C");
    holdGates.set("C", { gate: gateC.gate, notifyStarted: gateC.notifyStarted });
    const elC = makeNode("div");
    nextLabel = "C";
    const mountC = currentPlugins().mountSettings("test.plugin", elC);
    const mountCRejected = assert.rejects(mountC, /插件设置页挂载已取消/);
    await gateC.started;

    const elD = makeNode("div");
    nextLabel = "D";
    const disposeD = await currentPlugins().mountSettings("test.plugin", elD);
    await mountCRejected;
    assert.deepEqual(elD.children.map(node => node.textContent), ["settings-D"]);

    gateC.release();
    await new Promise(resolve => setTimeout(resolve, 0));
    assert.deepEqual(disposedHandles, ["A", "B", "C"]);
    assert.deepEqual(elD.children.map(node => node.textContent), ["settings-D"]);

    disposeD();
    assert.deepEqual(disposedHandles, ["A", "B", "C", "D"]);
    await currentUI()!.remove();
  } finally {
    delete (globalThis as Record<string, unknown>)[testKey];
    rmSync(tmpDir, { recursive: true, force: true });
  }
});

test("enabling a hanging plugin from PluginSettings does not block disabling another running plugin and clears busy state in bounded time", async () => {
  const toggleGuards = extract("src/components/PluginSettings.tsx", ["canStartQuickToggle", "isPluginToggleDisabled"]) as {
    canStartQuickToggle(input: {
      pluginId: string;
      quickBusyIds: ReadonlySet<string> | readonly string[];
      updatingPluginId: string | null;
    }): boolean;
    isPluginToggleDisabled(input: {
      pluginId: string;
      enabled: boolean;
      safeMode: boolean;
      quickBusyIds: readonly string[];
      updatingPluginId: string | null;
      detailBusyForPlugin?: boolean;
    }): boolean;
  };

  const renderer = readFileSync("../internal/desktop/assets/renderer.js", "utf8");
  const tmpDir = mkdtempSync(join(tmpdir(), "ekd-settings-toggle-unblocked-"));
  const testKey = "__EKD_SETTINGS_TOGGLE_UNBLOCKED_TEST__";
  const previousWindow = (globalThis as { window?: unknown }).window;
  try {
    const makeNode = (tag: string) => ({
      tagName: tag.toUpperCase(),
      style: { cssText: "" },
      dataset: {} as Record<string, string>,
      className: "",
      children: [] as unknown[],
      isConnected: false,
      textContent: "",
      append(...items: unknown[]) {
        this.children.push(...items);
      },
      replaceChildren(...items: unknown[]) {
        this.children = [...items];
      },
      remove() {
        this.isConnected = false;
      },
      setAttribute() {},
      removeAttribute() {},
      getAttribute: () => null,
      querySelector: () => null,
      querySelectorAll: () => [],
      addEventListener() {},
      removeEventListener() {},
    });

    const documentObj = {
      title: "",
      head: makeNode("head"),
      body: makeNode("body"),
      documentElement: { dataset: {} as Record<string, string>, style: { colorScheme: "" } },
      createElement: (tag: string) => makeNode(tag),
      createTextNode: (text: string) => String(text),
      addEventListener() {},
      removeEventListener() {},
      querySelector: () => null,
      querySelectorAll: () => [],
    };

    const pluginStates: Record<string, boolean> = {
      "plugin.a": false,
      "plugin.b": true,
    };
    const apiActions: string[] = [];
    let pluginBDisposed = 0;
    let notifyPluginAStarted!: () => void;
    const pluginAStarted = new Promise<void>(resolve => {
      notifyPluginAStarted = resolve;
    });

    (globalThis as Record<string, unknown>)[testKey] = {
      createPluginRuntime,
      loadPluginModule(_source: string, _exportName: string) {
        return Promise.resolve({
          activate(ctx: { plugin: { id: string }; onDispose(fn: () => void): void }) {
            if (ctx.plugin.id === "plugin.a") {
              notifyPluginAStarted();
              return new Promise<void>(() => {});
            }
            ctx.onDispose(() => {
              pluginBDisposed += 1;
            });
          },
        });
      },
    };

    const moduleSource = [
      `const hooks = globalThis.${testKey};`,
      "export function createEventChannel() { return { subscribe() { return () => {}; }, close() {} }; }",
      "export function createDOMContractState() { return {}; }",
      "export function evaluateDOMContracts() { return { probes: [], byId: {}, gates: {} }; }",
      "export function createEntryMountManager() { return { sync() {}, remove() {}, dispose() {} }; }",
      "export function createNativeHost(_t, _h, channel) { return { subscribe: (s, l) => channel.subscribe(s, l), close() {} }; }",
      "export function findSettingsDialogParts() { return null; }",
      "export function findOfficialSettingsPanel() { return null; }",
      "export function findOfficialSettingsSection() { return null; }",
      "export function findOfficialSettingsTab() { return null; }",
      "export function findSessionInfoKeepAliveAnchor() { return null; }",
      "export function findChatHeaderTail() { return null; }",
      "export const createPluginRuntime = (...args) => hooks.createPluginRuntime(...args);",
      "export const loadPluginModule = (...args) => hooks.loadPluginModule(...args);",
      "export function pluginEntryPath(p) { return p; }",
      "export function mountSurface() { return { unmount() {} }; }",
      "export function mountMainAreaSurface() { return { setWidth() {}, width: 0, dispose() {} }; }",
    ].join("\n");

    let blobCounter = 0;
    const fakeURL = {
      createObjectURL(blob: { __text: string }) {
        const file = join(tmpDir, `toggle-unblocked-${++blobCounter}.mjs`);
        writeFileSync(file, blob.__text, "utf8");
        return pathToFileURL(file).href;
      },
      revokeObjectURL() {},
    };
    class FakeBlob {
      __text: string;
      constructor(parts: unknown[]) {
        this.__text = parts.map(String).join("");
      }
    }
    class FakeMutationObserver {
      observe() {}
      disconnect() {}
    }

    const windowObj: Record<string, unknown> = {
      addEventListener() {},
      removeEventListener() {},
      dispatchEvent() {},
      requestAnimationFrame: (cb: () => void) => setTimeout(cb, 0),
      cancelAnimationFrame: (id: ReturnType<typeof setTimeout>) => clearTimeout(id),
      location: { href: "app://renderer/index.html" },
      localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
      fetch: async (_url: string, init: { body: string }) => {
        const req = JSON.parse(init.body) as { id: string; action: string; payload?: { path?: string } };
        let result: unknown = {};
        if (req.action === "features.list") result = { multiSession: false };
        else if (req.action === "core.fetch") {
          const reqPath = req.payload?.path ?? "";
          let payload: unknown = {
            source: "export function activate() {}",
            version: "1.0.0",
            updatedAt: "2026-11-26T00:00:00Z",
            capabilities: [],
          };
          if (reqPath.endsWith("/plugins")) {
            payload = {
              items: [
                {
                  id: "plugin.a",
                  version: "1.0.0",
                  updatedAt: "2026-11-26T00:00:00Z",
                  enabled: pluginStates["plugin.a"],
                  apiVersion: 2,
                  capabilities: [],
                  token: "tok-a",
                  entry: { renderer: "a.js" },
                },
                {
                  id: "plugin.b",
                  version: "1.0.0",
                  updatedAt: "2026-11-26T00:00:00Z",
                  enabled: pluginStates["plugin.b"],
                  apiVersion: 2,
                  capabilities: [],
                  token: "tok-b",
                  entry: { renderer: "b.js" },
                },
              ],
            };
          }
          result = { status: 200, bodyBase64: Buffer.from(JSON.stringify(payload)).toString("base64") };
        }
        return { ok: true, status: 200, json: async () => ({ protocol: 1, id: req.id, result }) };
      },
    };
    (globalThis as { window?: unknown }).window = windowObj;

    const install = new Function(
      "window", "document", "MutationObserver", "URL", "Blob", "CustomEvent", "createProviderEnhance",
      `${renderer}\nreturn installNativeRenderer;`,
    )(
      windowObj,
      documentObj,
      FakeMutationObserver,
      fakeURL,
      FakeBlob,
      class CustomEvent {
        type: string;
        detail: unknown;
        constructor(type: string, init?: { detail?: unknown }) {
          this.type = type;
          this.detail = init?.detail;
        }
      },
      () => ({ install() {}, remove() {}, clearDecorations() {} }),
    ) as (options: Record<string, unknown>) => Promise<{ status: string }>;

    const currentUI = () => windowObj.__ekdNativeUI as { remove(): Promise<void> } | undefined;
    const currentPlugins = () => windowObj.__ekdPlugins as PluginHostBridge;

    await install({
      endpoint: "http://bridge",
      bridgeKey: "bridge-key",
      surface: "host",
      coreURL: "http://127.0.0.1:1001",
      moduleSource,
      cssSource: "",
    });
    await currentPlugins().sync();

    // Simulate PluginSettings quickToggle per-plugin busy set + real syncPluginHost().
    const quickBusySet = new Set<string>();
    const quickToggle = async (pluginId: string, next: boolean) => {
      if (!toggleGuards.canStartQuickToggle({ pluginId, quickBusyIds: quickBusySet, updatingPluginId: null })) {
        return false;
      }
      quickBusySet.add(pluginId);
      try {
        apiActions.push(`${next ? "enable" : "disable"}:${pluginId}`);
        pluginStates[pluginId] = next;
        const syncErr = await syncPluginHost();
        assert.equal(syncErr, null);
        return true;
      } finally {
        quickBusySet.delete(pluginId);
      }
    };

    // Start enabling hanging plugin A.
    const startedAt = Date.now();
    const enableAPromise = quickToggle("plugin.a", true);
    await pluginAStarted;

    // While plugin A is still in quickBusy (its activate() hangs), plugin B's switch MUST NOT be disabled or blocked.
    assert.equal(
      toggleGuards.isPluginToggleDisabled({
        pluginId: "plugin.a",
        enabled: true,
        safeMode: false,
        quickBusyIds: [...quickBusySet],
        updatingPluginId: null,
      }),
      true,
    );
    assert.equal(
      toggleGuards.isPluginToggleDisabled({
        pluginId: "plugin.b",
        enabled: true,
        safeMode: false,
        quickBusyIds: [...quickBusySet],
        updatingPluginId: null,
      }),
      false,
      "enabling hanging plugin A must not disable plugin B's toggle",
    );

    // Immediately disable plugin B while A's activation is still hanging.
    const disabledB = await quickToggle("plugin.b", false);
    assert.equal(disabledB, true);
    assert.deepEqual(apiActions, ["enable:plugin.a", "disable:plugin.b"]);
    assert.equal(pluginBDisposed, 1, "plugin B must be deactivated and cleaned up immediately in the host runtime");

    // Plugin A's own toggle must also settle and clear its busy state within bounded time (not hang forever).
    assert.equal(await enableAPromise, true);
    const elapsedMs = Date.now() - startedAt;
    assert.equal(quickBusySet.size, 0, "plugin A's busy state must clear after bounded wait");
    assert.ok(elapsedMs < 1500, `expected bounded quickToggle completion, got ${elapsedMs}ms`);

    // Even if a custom/legacy bridge ignores waitForActivationMs and hangs forever, syncPluginHost still returns in bounded time.
    const savedBridge = windowObj.__ekdPlugins;
    windowObj.__ekdPlugins = {
      sync: () => new Promise<void>(() => {}),
      list: () => [],
      mountSettings: async () => () => {},
    };
    const legacyStart = Date.now();
    const legacyErr = await syncPluginHost({ waitForActivationMs: 40 });
    assert.equal(legacyErr, null);
    assert.ok(Date.now() - legacyStart < 500, "syncPluginHost must bound legacy bridges that ignore waitForActivationMs");
    windowObj.__ekdPlugins = savedBridge;

    await currentUI()!.remove();
  } finally {
    if (previousWindow === undefined) delete (globalThis as { window?: unknown }).window;
    else (globalThis as { window?: unknown }).window = previousWindow;
    delete (globalThis as Record<string, unknown>)[testKey];
    rmSync(tmpDir, { recursive: true, force: true });
  }
});

test("renderer mountSettings propagates AbortSignal to real createPluginRuntime so closing panel A immediately removes KCT listeners, suppresses late faults, and keeps panel B intact", async () => {
  const renderer = readFileSync("../internal/desktop/assets/renderer.js", "utf8");
  const tmpDir = mkdtempSync(join(tmpdir(), "ekd-settings-abort-real-"));
  const testKey = "__EKD_SETTINGS_ABORT_REAL_TEST__";
  try {
    const makeNode = (tag: string): Record<string, unknown> => {
      const node: Record<string, unknown> = {
        tagName: tag.toUpperCase(),
        style: { cssText: "" },
        dataset: {} as Record<string, string>,
        className: "",
        children: [] as Record<string, unknown>[],
        parentElement: null as Record<string, unknown> | null,
        isConnected: false,
        textContent: "",
        ownerDocument: null as unknown,
        append(...items: Record<string, unknown>[]) {
          for (const item of items) {
            item.parentElement = node;
            (node.children as Record<string, unknown>[]).push(item);
          }
        },
        replaceChildren(...items: Record<string, unknown>[]) {
          for (const child of node.children as Record<string, unknown>[]) child.parentElement = null;
          node.children = [];
          (node.append as (...xs: Record<string, unknown>[]) => void)(...items);
        },
        remove() {
          const parent = node.parentElement as { children?: Record<string, unknown>[] } | null;
          if (parent?.children) {
            const idx = parent.children.indexOf(node);
            if (idx >= 0) parent.children.splice(idx, 1);
          }
          node.parentElement = null;
          node.isConnected = false;
        },
        setAttribute() {},
        removeAttribute() {},
        getAttribute: () => null,
        querySelector: () => null,
        querySelectorAll: () => [],
        addEventListener() {},
        removeEventListener() {},
      };
      return node;
    };

    const documentObj: Record<string, unknown> = {
      title: "",
      head: makeNode("head"),
      body: makeNode("body"),
      documentElement: { dataset: {} as Record<string, string>, style: { colorScheme: "" } },
      createElement(tag: string) {
        const el = makeNode(tag);
        el.ownerDocument = documentObj;
        return el;
      },
      createTextNode: (text: string) => String(text),
      addEventListener() {},
      removeEventListener() {},
      querySelector: () => null,
      querySelectorAll: () => [],
    };

    const windowEvents = new EventTarget();
    const faultPosts: Array<{ path: string; body: unknown }> = [];
    let panelAEvents = 0;
    let panelBEvents = 0;
    let renderCall = 0;
    let rejectPanelA!: (err: Error) => void;
    let notifyPanelAWaiting!: () => void;
    const panelAWaiting = new Promise<void>(resolve => { notifyPanelAWaiting = resolve; });

    (globalThis as Record<string, unknown>)[testKey] = {
      createPluginRuntime,
      loadPluginModule(_source: string, requiredExport: string) {
        if (requiredExport === "renderSettings") {
          return Promise.resolve({
            async renderSettings(
              ctx: { onDispose(fn: () => void): void },
              element: { append(node: unknown): void },
            ) {
              const callNo = ++renderCall;
              if (callNo === 1) {
                const card = (documentObj.createElement as (t: string) => Record<string, unknown>)("section");
                card.textContent = "kct-A";
                element.append(card);
                const onSettings = () => { panelAEvents += 1; };
                windowEvents.addEventListener("ekdp-kct:settings", onSettings);
                ctx.onDispose(() => {
                  windowEvents.removeEventListener("ekdp-kct:settings", onSettings);
                  (card.remove as () => void)();
                });
                notifyPanelAWaiting();
                await new Promise<void>((_resolve, reject) => { rejectPanelA = reject; });
              } else {
                const card = (documentObj.createElement as (t: string) => Record<string, unknown>)("section");
                card.textContent = "kct-B";
                element.append(card);
                const onSettings = () => { panelBEvents += 1; };
                windowEvents.addEventListener("ekdp-kct:settings", onSettings);
                ctx.onDispose(() => {
                  windowEvents.removeEventListener("ekdp-kct:settings", onSettings);
                  (card.remove as () => void)();
                });
              }
            },
          });
        }
        return Promise.resolve({ activate() {} });
      },
    };

    const moduleSource = [
      `const hooks = globalThis.${testKey};`,
      "export function createEventChannel() { return { subscribe() { return () => {}; }, close() {} }; }",
      "export function createDOMContractState() { return {}; }",
      "export function evaluateDOMContracts() { return { probes: [], byId: {}, gates: {} }; }",
      "export function createEntryMountManager() { return { sync() {}, remove() {}, dispose() {} }; }",
      "export function createNativeHost(_t, _h, channel) { return { subscribe: (s, l) => channel.subscribe(s, l), close() {} }; }",
      "export function findSettingsDialogParts() { return null; }",
      "export function findOfficialSettingsPanel() { return null; }",
      "export function findOfficialSettingsSection() { return null; }",
      "export function findOfficialSettingsTab() { return null; }",
      "export function findSessionInfoKeepAliveAnchor() { return null; }",
      "export function findChatHeaderTail() { return null; }",
      "export const createPluginRuntime = (...args) => hooks.createPluginRuntime(...args);",
      "export const loadPluginModule = (...args) => hooks.loadPluginModule(...args);",
      "export function pluginEntryPath(p) { return p; }",
      "export function mountSurface() { return { unmount() {} }; }",
      "export function mountMainAreaSurface() { return { setWidth() {}, width: 0, dispose() {} }; }",
    ].join("\n");

    let blobCounter = 0;
    const fakeURL = {
      createObjectURL(blob: { __text: string }) {
        const file = join(tmpDir, `settings-abort-real-${++blobCounter}.mjs`);
        writeFileSync(file, blob.__text, "utf8");
        return pathToFileURL(file).href;
      },
      revokeObjectURL() {},
    };
    class FakeBlob {
      __text: string;
      constructor(parts: unknown[]) {
        this.__text = parts.map(String).join("");
      }
    }
    class FakeMutationObserver {
      observe() {}
      disconnect() {}
    }

    const pluginRecord = {
      id: "kimicode-plus.kct",
      version: "1.0.4",
      updatedAt: "2026-11-26T00:00:00Z",
      enabled: true,
      apiVersion: 2,
      capabilities: ["ui:settings"],
      token: "tok-kct",
      entry: { renderer: "renderer.js", settings: "settings.js" },
    };

    const windowObj: Record<string, unknown> = {
      addEventListener: windowEvents.addEventListener.bind(windowEvents),
      removeEventListener: windowEvents.removeEventListener.bind(windowEvents),
      dispatchEvent: windowEvents.dispatchEvent.bind(windowEvents),
      requestAnimationFrame: (cb: () => void) => setTimeout(cb, 0),
      cancelAnimationFrame: (id: ReturnType<typeof setTimeout>) => clearTimeout(id),
      location: { href: "app://renderer/index.html" },
      localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
      fetch: async (_url: string, init: { body: string }) => {
        const req = JSON.parse(init.body) as { id: string; action: string; payload?: { path?: string; body?: unknown } };
        let result: unknown = {};
        if (req.action === "features.list") result = { multiSession: false };
        else if (req.action === "core.fetch") {
          const reqPath = req.payload?.path ?? "";
          if (reqPath.includes("/fault")) {
            faultPosts.push({ path: reqPath, body: req.payload?.body });
          }
          const payload = reqPath.endsWith("/plugins")
            ? { items: [pluginRecord] }
            : {
                source: "export function activate() {} export function renderSettings() {}",
                version: pluginRecord.version,
                updatedAt: pluginRecord.updatedAt,
                capabilities: pluginRecord.capabilities,
              };
          result = { status: 200, bodyBase64: Buffer.from(JSON.stringify(payload)).toString("base64") };
        }
        return { ok: true, status: 200, json: async () => ({ protocol: 1, id: req.id, result }) };
      },
    };

    const install = new Function(
      "window", "document", "MutationObserver", "URL", "Blob", "CustomEvent", "createProviderEnhance",
      `${renderer}\nreturn installNativeRenderer;`,
    )(
      windowObj,
      documentObj,
      FakeMutationObserver,
      fakeURL,
      FakeBlob,
      class CustomEvent {
        type: string;
        detail: unknown;
        constructor(type: string, init?: { detail?: unknown }) {
          this.type = type;
          this.detail = init?.detail;
        }
      },
      () => ({ install() {}, remove() {}, clearDecorations() {} }),
    ) as (options: Record<string, unknown>) => Promise<{ status: string }>;

    const currentUI = () => windowObj.__ekdNativeUI as { remove(): Promise<void> } | undefined;
    const currentPlugins = () => windowObj.__ekdPlugins as {
      sync(): Promise<void>;
      mountSettings(id: string, element: unknown, options?: { signal?: AbortSignal }): Promise<() => void>;
    };

    await install({
      endpoint: "http://bridge",
      bridgeKey: "bridge-key",
      surface: "host",
      coreURL: "http://127.0.0.1:1001",
      moduleSource,
      cssSource: "",
    });
    await currentPlugins().sync();

    const hostEl = (documentObj.createElement as (t: string) => Record<string, unknown>)("div");
    const controllerA = new AbortController();
    const mountA = currentPlugins().mountSettings("kimicode-plus.kct", hostEl, { signal: controllerA.signal });
    const mountARejected = assert.rejects(mountA, /插件设置页挂载已取消/);
    await panelAWaiting;

    windowEvents.dispatchEvent(new Event("ekdp-kct:settings"));
    assert.equal(panelAEvents, 1);

    // Close panel A before its async credentials call settles -> listener MUST be removed immediately and mountA rejects immediately.
    controllerA.abort();
    await mountARejected;
    windowEvents.dispatchEvent(new Event("ekdp-kct:settings"));
    assert.equal(panelAEvents, 1, "panel A listener must stop firing immediately on AbortSignal");

    // Reopen panel B for the same plugin.
    const controllerB = new AbortController();
    const disposeB = await currentPlugins().mountSettings("kimicode-plus.kct", hostEl, { signal: controllerB.signal });
    windowEvents.dispatchEvent(new Event("ekdp-kct:settings"));
    assert.equal(panelAEvents, 1);
    assert.equal(panelBEvents, 1);

    // Late rejection from panel A must NOT report any plugin fault and must NOT affect panel B.
    rejectPanelA(new Error("late credentials rejection"));
    await new Promise(resolve => setTimeout(resolve, 20));
    assert.deepEqual(faultPosts, [], "aborted panel A rejection must not report PLUGIN_SETTINGS_FAILED");
    windowEvents.dispatchEvent(new Event("ekdp-kct:settings"));
    assert.equal(panelBEvents, 2, "panel B must remain active and healthy after panel A's late rejection");

    disposeB();
    windowEvents.dispatchEvent(new Event("ekdp-kct:settings"));
    assert.equal(panelBEvents, 2);
    await currentUI()!.remove();
  } finally {
    delete (globalThis as Record<string, unknown>)[testKey];
    rmSync(tmpDir, { recursive: true, force: true });
  }
});

test("renderer.js transport.request and bridgeRequest honor AbortSignal across dispatch, in-flight fetch, and retry backoff", async () => {
  const renderer = readFileSync("../internal/desktop/assets/renderer.js", "utf8");
  const start = renderer.indexOf("async function installNativeRenderer(options) {");
  const end = renderer.indexOf("  const moduleURL =", start);
  assert.ok(start >= 0 && end > start, "renderer transport slice not found");
  const prefix = `${renderer.slice(start, end)}\n  return transport;\n}\nreturn installNativeRenderer;`;

  let fetchCalls = 0;
  let mode: "hang" | "refuse" | "kapError" = "hang";
  const windowObj = {
    fetch: async (_url: string, init: { body: string; signal?: AbortSignal }) => {
      fetchCalls += 1;
      const req = JSON.parse(init.body) as { id: string };
      if (mode === "refuse") {
        return {
          ok: false,
          status: 403,
          json: async () => ({ protocol: 1, id: req.id, error: { code: "RPC_DENIED", message: "refused" } }),
        };
      }
      if (mode === "kapError") {
        return {
          ok: true,
          status: 200,
          json: async () => ({
            protocol: 1,
            id: req.id,
            error: { code: "KAP_CONFLICT", message: "origin mismatch", status: 409, kapCode: 40901 },
          }),
        };
      }
      return new Promise((_, reject) => {
        if (init.signal?.aborted) {
          reject(init.signal.reason ?? new DOMException("The operation was aborted.", "AbortError"));
          return;
        }
        init.signal?.addEventListener(
          "abort",
          () => reject(init.signal?.reason ?? new DOMException("The operation was aborted.", "AbortError")),
          { once: true },
        );
      });
    },
  };

  const install = new Function("window", prefix)(windowObj) as (
    opts: Record<string, unknown>,
  ) => Promise<{ request(action: string, payload?: unknown, reqOptions?: { signal?: AbortSignal }): Promise<unknown> }>;
  const transport = await install({
    endpoint: "http://bridge",
    bridgeKey: "bridge-key",
    transport: "main-loopback-rpc",
  });

  // 1. Pre-aborted signal makes 0 fetch calls and rejects with AbortError.
  await assert.rejects(
    () => transport.request("providers.saveOfficialForm", {}, { signal: AbortSignal.abort() }),
    (err: unknown) => (err as { name?: string })?.name === "AbortError",
  );
  assert.equal(fetchCalls, 0);

  // 2. Aborting an in-flight fetch rejects immediately with AbortError (never BRIDGE_OUTCOME_UNKNOWN) without retrying.
  mode = "hang";
  const inFlightCtrl = new AbortController();
  const inFlightPromise = transport.request("providers.saveOfficialForm", {}, { signal: inFlightCtrl.signal });
  inFlightCtrl.abort();
  await assert.rejects(
    () => inFlightPromise,
    (err: unknown) => {
      const e = err as { name?: string; code?: string };
      return e?.name === "AbortError" && e?.code !== "BRIDGE_OUTCOME_UNKNOWN";
    },
  );
  assert.equal(fetchCalls, 1);

  // 3. Aborting during the 250ms retry backoff after RPC_DENIED rejects immediately and skips attempt #2.
  mode = "refuse";
  fetchCalls = 0;
  const backoffCtrl = new AbortController();
  const startedAt = Date.now();
  const retryPromise = transport.request("providers.saveOfficialForm", {}, { signal: backoffCtrl.signal });
  await new Promise(resolve => setTimeout(resolve, 25));
  assert.equal(fetchCalls, 1);
  backoffCtrl.abort();
  await assert.rejects(
    () => retryPromise,
    (err: unknown) => (err as { name?: string })?.name === "AbortError",
  );
  assert.ok(Date.now() - startedAt < 200, "aborting during retry backoff must reject immediately");
  assert.equal(fetchCalls, 1, "aborted retry must not dispatch attempt #2");

  // 4. Bridge error status and kapCode are preserved on the thrown Error.
  mode = "kapError";
  await assert.rejects(
    () => transport.request("providers.saveOfficialForm", {}),
    (err: unknown) => {
      const e = err as { status?: number; kapCode?: number; message?: string };
      return e?.status === 409 && e?.kapCode === 40901 && e?.message === "origin mismatch";
    },
  );
});

test("bootstrap.js /ekd/native/v1 aborts upstream coreFetch on client disconnect and forwards status and kapCode", async () => {
  const bootstrap = readFileSync("../internal/desktop/assets/bootstrap.js", "utf8");
  let notifySlowStarted!: () => void;
  const slowStarted = new Promise<void>(resolve => { notifySlowStarted = resolve; });
  let notifySlowAborted!: () => void;
  const slowAborted = new Promise<void>(resolve => { notifySlowAborted = resolve; });
  let slowRes: http.ServerResponse | null = null;

  const coreServer = http.createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", chunk => chunks.push(Buffer.from(chunk)));
    req.on("end", () => {
      if (req.url === "/enhance/v1/pair") {
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ sessionToken: "core-sess-tok", expiresAt: new Date(Date.now() + 3600_000).toISOString() }));
        return;
      }
      if (req.url === "/enhance/v1/desktop/actions/providers.saveOfficialForm") {
        const parsed = JSON.parse(Buffer.concat(chunks).toString("utf8")) as { mode?: string };
        if (parsed.mode === "slow") {
          slowRes = res;
          res.on("close", () => {
            if (!res.writableEnded) notifySlowAborted();
          });
          notifySlowStarted();
          return;
        }
        res.writeHead(409, { "Content-Type": "application/json" });
        res.end(JSON.stringify({
          error: { code: "ORIGIN_MISMATCH", message: "daemon origin mismatch", kapCode: 40900 },
        }));
        return;
      }
      res.writeHead(404, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ error: { code: "NOT_FOUND", message: "not found" } }));
    });
  });

  await new Promise<void>(resolve => coreServer.listen(0, "127.0.0.1", resolve));
  const coreAddr = coreServer.address();
  assert.ok(coreAddr && typeof coreAddr === "object");
  const coreURL = `http://127.0.0.1:${coreAddr.port}`;

  let capturedConfig: { endpoint: string; bridgeKey: string } | null = null;
  const mainWC = {
    id: 1,
    isDestroyed: () => false,
    getURL: () => "app://renderer/index.html",
    on() {},
    once() {},
    removeListener() {},
    async executeJavaScript(code: string) {
      const match = /\((?:\{[\s\S]*\})\)$/.exec(code.trim());
      if (match) {
        const parsed = JSON.parse(match[0].slice(1, -1)) as { endpoint?: string; bridgeKey?: string };
        if (parsed.endpoint && parsed.bridgeKey) {
          capturedConfig = { endpoint: parsed.endpoint, bridgeKey: parsed.bridgeKey };
        }
      }
      return { status: "installed" };
    },
  };
  const electronStub = {
    webContents: {
      getAllWebContents: () => [mainWC],
      fromId: (id: number) => (id === 1 ? mainWC : null),
    },
    BrowserWindow: { fromWebContents: () => null },
    WebContentsView: class {},
    session: { fromPartition: () => ({}) },
    app: { getVersion: () => "1.4.3", on() {}, once() {}, removeListener() {} },
    dialog: {},
    shell: {},
    ipcMain: { on() {}, removeListener() {}, prependListener() {} },
  };
  const customRequire = (id: string) => {
    if (id === "electron") return electronStub;
    if (id === "node:http") return http;
    if (id === "node:crypto") return crypto;
    throw new Error(`unexpected require: ${id}`);
  };

  try {
    const installBootstrap = new Function("require", `${bootstrap}\nreturn installNativeBootstrap;`)(customRequire) as (
      opts: Record<string, unknown>,
    ) => Promise<{ status: string }>;
    const installed = await installBootstrap({
      coreURL,
      pairingToken: "pair-tok",
      expectedApp: "1.4.3",
      expectedElectron: process.versions.electron,
      rendererSource: "function(c) { return c; }",
      moduleSource: "",
      cssSource: "",
    });
    assert.equal(installed.status, "installed");
    assert.ok(capturedConfig, "bootstrap must bind endpoint and bridgeKey");
    const { endpoint, bridgeKey } = capturedConfig;

    // 1. Aborting the client fetch to /ekd/native/v1 aborts the upstream coreFetch to Go core.
    const abortCtrl = new AbortController();
    const slowFetch = fetch(`${endpoint}/ekd/native/v1`, {
      method: "POST",
      headers: {
        Origin: "app://renderer",
        "Content-Type": "application/json",
        "X-EKD-Bridge": bridgeKey,
      },
      body: JSON.stringify({
        protocol: 1,
        id: "req-slow-1",
        action: "providers.saveOfficialForm",
        payload: { mode: "slow" },
      }),
      signal: abortCtrl.signal,
    });
    await slowStarted;
    abortCtrl.abort();
    await assert.rejects(() => slowFetch, (err: unknown) => (err as { name?: string })?.name === "AbortError");
    await Promise.race([
      slowAborted,
      new Promise<never>((_, reject) => setTimeout(() => reject(new Error("upstream coreFetch was not aborted")), 1000)),
    ]);

    // 2. Non-2xx error from core preserves HTTP status and kapCode in /ekd/native/v1 error JSON.
    const errRes = await fetch(`${endpoint}/ekd/native/v1`, {
      method: "POST",
      headers: {
        Origin: "app://renderer",
        "Content-Type": "application/json",
        "X-EKD-Bridge": bridgeKey,
      },
      body: JSON.stringify({
        protocol: 1,
        id: "req-409-1",
        action: "providers.saveOfficialForm",
        payload: { mode: "conflict" },
      }),
    });
    assert.equal(errRes.status, 200);
    const errJson = await errRes.json() as { error?: { code?: string; message?: string; status?: number; kapCode?: number } };
    assert.equal(errJson.error?.code, "ORIGIN_MISMATCH");
    assert.equal(errJson.error?.message, "daemon origin mismatch");
    assert.equal(errJson.error?.status, 409);
    assert.equal(errJson.error?.kapCode, 40900);
  } finally {
    if (slowRes && !(slowRes as http.ServerResponse).writableEnded) (slowRes as http.ServerResponse).destroy();
    const nativeState = (globalThis as { __ekdNative?: { cleanup?: () => Promise<void> } }).__ekdNative;
    if (nativeState?.cleanup) await nativeState.cleanup();
    delete (globalThis as { __ekdNativeBoot?: unknown }).__ekdNativeBoot;
    coreServer.closeAllConnections();
    await new Promise<void>(resolve => coreServer.close(() => resolve()));
  }
});

test("renderer.js mountPluginSettings awaits slow plugin activation past syncPluginHost budget, honors AbortSignal on hung activation, and rejects when activation faults", async () => {
  const renderer = readFileSync("../internal/desktop/assets/renderer.js", "utf8");
  const tmpDir = mkdtempSync(join(tmpdir(), "ekd-slow-activate-settings-"));
  const testKey = "__ekdSlowActivateSettingsHooks";
  const prevWindow = (globalThis as { window?: unknown }).window;

  try {
    const makeNode = (tag: string): Record<string, unknown> => {
      const node: Record<string, unknown> = {
        tagName: tag.toUpperCase(),
        style: {} as Record<string, string>,
        dataset: {} as Record<string, string>,
        className: "",
        textContent: "",
        children: [] as Record<string, unknown>[],
        parentElement: null as Record<string, unknown> | null,
        isConnected: true,
        ownerDocument: null as unknown,
        append(...items: Record<string, unknown>[]) {
          for (const item of items) {
            item.parentElement = node;
            (node.children as Record<string, unknown>[]).push(item);
          }
        },
        replaceChildren(...items: Record<string, unknown>[]) {
          for (const child of node.children as Record<string, unknown>[]) child.parentElement = null;
          node.children = [];
          (node.append as (...xs: Record<string, unknown>[]) => void)(...items);
        },
        remove() {
          const parent = node.parentElement as { children?: Record<string, unknown>[] } | null;
          if (parent?.children) {
            const idx = parent.children.indexOf(node);
            if (idx >= 0) parent.children.splice(idx, 1);
          }
          node.parentElement = null;
          node.isConnected = false;
        },
        setAttribute() {},
        removeAttribute() {},
        getAttribute: () => null,
        querySelector: () => null,
        querySelectorAll: () => [],
        addEventListener() {},
        removeEventListener() {},
      };
      return node;
    };

    const documentObj: Record<string, unknown> = {
      title: "",
      head: makeNode("head"),
      body: makeNode("body"),
      documentElement: { dataset: {} as Record<string, string>, style: { colorScheme: "" } },
      createElement(tag: string) {
        const el = makeNode(tag);
        el.ownerDocument = documentObj;
        return el;
      },
      createTextNode: (text: string) => String(text),
      addEventListener() {},
      removeEventListener() {},
      querySelector: () => null,
      querySelectorAll: () => [],
    };

    const enabledMap: Record<string, boolean> = {
      "plugin.slow": false,
      "plugin.hung": false,
      "plugin.fault": false,
    };
    let slowActivated = false;
    let notifyHungStarted!: () => void;
    const hungStarted = new Promise<void>(resolve => { notifyHungStarted = resolve; });

    (globalThis as Record<string, unknown>)[testKey] = {
      createPluginRuntime,
      loadPluginModule(_source: string, requiredExport: string) {
        if (requiredExport === "renderSettings") {
          return Promise.resolve({
            renderSettings(
              ctx: { plugin: { id: string }; onDispose(fn: () => void): void },
              element: { append(node: unknown): void },
            ) {
              const card = (documentObj.createElement as (t: string) => Record<string, unknown>)("section");
              card.textContent = `${ctx.plugin.id}:settings-ready`;
              element.append(card);
              ctx.onDispose(() => {
                (card.remove as () => void)();
              });
            },
          });
        }
        return Promise.resolve({
          async activate(ctx: { plugin: { id: string } }) {
            if (ctx.plugin.id === "plugin.slow") {
              await new Promise(resolve => setTimeout(resolve, 350));
              slowActivated = true;
              return;
            }
            if (ctx.plugin.id === "plugin.hung") {
              notifyHungStarted();
              return new Promise<void>(() => {});
            }
            if (ctx.plugin.id === "plugin.fault") {
              await new Promise(resolve => setTimeout(resolve, 80));
              throw new Error("slow activation boom");
            }
          },
        });
      },
    };

    const moduleSource = [
      `const hooks = globalThis.${testKey};`,
      "export function createEventChannel() { return { subscribe() { return () => {}; }, close() {} }; }",
      "export function createDOMContractState() { return {}; }",
      "export function evaluateDOMContracts() { return { probes: [], byId: {}, gates: {} }; }",
      "export function createEntryMountManager() { return { sync() {}, remove() {}, dispose() {} }; }",
      "export function createNativeHost(_t, _h, channel) { return { subscribe: (s, l) => channel.subscribe(s, l), close() {} }; }",
      "export function findSettingsDialogParts() { return null; }",
      "export function findOfficialSettingsPanel() { return null; }",
      "export function findOfficialSettingsSection() { return null; }",
      "export function findOfficialSettingsTab() { return null; }",
      "export function findSessionInfoKeepAliveAnchor() { return null; }",
      "export function findChatHeaderTail() { return null; }",
      "export const createPluginRuntime = (...args) => hooks.createPluginRuntime(...args);",
      "export const loadPluginModule = (...args) => hooks.loadPluginModule(...args);",
      "export function pluginEntryPath(p) { return p; }",
      "export function mountSurface() { return { unmount() {} }; }",
      "export function mountMainAreaSurface() { return { setWidth() {}, width: 0, dispose() {} }; }",
    ].join("\n");

    let blobCounter = 0;
    const fakeURL = {
      createObjectURL(blob: { __text: string }) {
        const file = join(tmpDir, `slow-activate-${++blobCounter}.mjs`);
        writeFileSync(file, blob.__text, "utf8");
        return pathToFileURL(file).href;
      },
      revokeObjectURL() {},
    };
    class FakeBlob {
      __text: string;
      constructor(parts: unknown[]) {
        this.__text = parts.map(String).join("");
      }
    }
    class FakeMutationObserver {
      observe() {}
      disconnect() {}
    }

    const windowObj: Record<string, unknown> = {
      addEventListener() {},
      removeEventListener() {},
      dispatchEvent() {},
      requestAnimationFrame: (cb: () => void) => setTimeout(cb, 0),
      cancelAnimationFrame: (id: ReturnType<typeof setTimeout>) => clearTimeout(id),
      location: { href: "app://renderer/index.html" },
      localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
      fetch: async (_url: string, init: { body: string }) => {
        const req = JSON.parse(init.body) as { id: string; action: string; payload?: { path?: string } };
        let result: unknown = {};
        if (req.action === "features.list") result = { multiSession: false };
        else if (req.action === "core.fetch") {
          const reqPath = req.payload?.path ?? "";
          let payload: unknown = {
            source: "export function activate() {} export function renderSettings() {}",
            version: "1.0.0",
            updatedAt: "2026-11-26T00:00:00Z",
            capabilities: ["ui:settings"],
          };
          if (reqPath.endsWith("/plugins")) {
            payload = {
              items: ["plugin.slow", "plugin.hung", "plugin.fault"].map(id => ({
                id,
                version: "1.0.0",
                updatedAt: "2026-11-26T00:00:00Z",
                enabled: enabledMap[id],
                apiVersion: 2,
                capabilities: ["ui:settings"],
                token: `tok-${id}`,
                entry: { renderer: `${id}.js`, settings: `${id}.settings.js` },
              })),
            };
          }
          result = { status: 200, bodyBase64: Buffer.from(JSON.stringify(payload)).toString("base64") };
        }
        return { ok: true, status: 200, json: async () => ({ protocol: 1, id: req.id, result }) };
      },
    };
    (globalThis as { window?: unknown }).window = windowObj;

    const install = new Function(
      "window", "document", "MutationObserver", "URL", "Blob", "CustomEvent", "createProviderEnhance",
      `${renderer}\nreturn installNativeRenderer;`,
    )(
      windowObj,
      documentObj,
      FakeMutationObserver,
      fakeURL,
      FakeBlob,
      class CustomEvent {
        type: string;
        detail: unknown;
        constructor(type: string, init?: { detail?: unknown }) {
          this.type = type;
          this.detail = init?.detail;
        }
      },
      () => ({ install() {}, remove() {}, clearDecorations() {} }),
    ) as (options: Record<string, unknown>) => Promise<{ status: string }>;

    const currentUI = () => windowObj.__ekdNativeUI as { remove(): Promise<void> } | undefined;
    const currentPlugins = () => windowObj.__ekdPlugins as {
      sync(options?: { waitForActivationMs?: number }): Promise<void>;
      mountSettings(id: string, element: unknown, options?: { signal?: AbortSignal }): Promise<() => void>;
    };

    await install({
      endpoint: "http://bridge",
      bridgeKey: "bridge-key",
      surface: "host",
      coreURL: "http://127.0.0.1:1001",
      moduleSource,
      cssSource: "",
    });

    // (a) Plugin A takes 350ms (> 150ms syncPluginHost budget): syncPluginHost returns early around 150ms,
    //     and immediate mountPluginSettings awaits activation and mounts settings DOM cleanly once ready.
    enabledMap["plugin.slow"] = true;
    const syncStartedAt = Date.now();
    const syncErr = await syncPluginHost();
    const syncElapsed = Date.now() - syncStartedAt;
    assert.equal(syncErr, null);
    assert.ok(syncElapsed < 320, `syncPluginHost should return near 150ms budget, took ${syncElapsed}ms`);
    assert.equal(slowActivated, false, "plugin.slow should still be activating when syncPluginHost returns");

    const slowHost = (documentObj.createElement as (t: string) => Record<string, unknown>)("div");
    const slowCtrl = new AbortController();
    const releaseSlow = await currentPlugins().mountSettings("plugin.slow", slowHost, { signal: slowCtrl.signal });
    assert.equal(slowActivated, true, "mountPluginSettings must wait until slow activation finishes");
    const mountedChildren = slowHost.children as Array<{ children?: Array<{ textContent?: string }> }>;
    assert.equal(mountedChildren.length, 1);
    assert.equal(mountedChildren[0]?.children?.[0]?.textContent, "plugin.slow:settings-ready");
    releaseSlow();

    // (b) Aborting while mountPluginSettings waits on a hung activate() rejects immediately with SETTINGS_MOUNT_CANCELLED.
    enabledMap["plugin.hung"] = true;
    await currentPlugins().sync({ waitForActivationMs: 0 });
    await hungStarted;
    const hungHost = (documentObj.createElement as (t: string) => Record<string, unknown>)("div");
    const hungCtrl = new AbortController();
    const abortStart = Date.now();
    const hungMount = currentPlugins().mountSettings("plugin.hung", hungHost, { signal: hungCtrl.signal });
    hungCtrl.abort();
    await assert.rejects(
      () => hungMount,
      (err: unknown) => (err as { code?: string })?.code === "SETTINGS_MOUNT_CANCELLED",
    );
    assert.ok(Date.now() - abortStart < 150, "aborting while waiting on hung activation must reject immediately");

    // (c) If slow activate() faults while plugin stays enabled, mountPluginSettings preserves PLUGIN_ACTIVATION_FAILED (never PLUGIN_DISABLED).
    enabledMap["plugin.hung"] = false;
    enabledMap["plugin.fault"] = true;
    await currentPlugins().sync({ waitForActivationMs: 0 });
    const faultHost = (documentObj.createElement as (t: string) => Record<string, unknown>)("div");
    await assert.rejects(
      () => currentPlugins().mountSettings("plugin.fault", faultHost, { signal: new AbortController().signal }),
      (err: unknown) => {
        const e = err as { message?: string; code?: string; cause?: unknown };
        return /插件未运行，无法打开设置页/.test(e?.message || "")
          && e?.code === "PLUGIN_ACTIVATION_FAILED"
          && Boolean(e?.cause);
      },
    );

    await currentUI()!.remove();
  } finally {
    if (prevWindow === undefined) delete (globalThis as { window?: unknown }).window;
    else (globalThis as { window?: unknown }).window = prevWindow;
    delete (globalThis as Record<string, unknown>)[testKey];
    rmSync(tmpDir, { recursive: true, force: true });
  }
});

test("renderer.js mountPluginSettings binds hot-updated revision across slow reconciliation, pre-sync mount, fault, and mid-wait disable", async () => {
  const renderer = readFileSync("../internal/desktop/assets/renderer.js", "utf8");
  const tmpDir = mkdtempSync(join(tmpdir(), "ekd-hot-update-settings-"));
  const testKey = `__ekdHotUpdateSettings_${Date.now()}_${Math.random().toString(36).slice(2)}`;
  const prevWindow = (globalThis as { window?: unknown }).window;

  try {
    const makeNode = (tag: string) => {
      const classSet = new Set<string>();
      const node: Record<string, unknown> = {
        tagName: tag.toUpperCase(),
        dataset: {} as Record<string, string>,
        style: { cssText: "", setProperty() {}, removeProperty() {} },
        children: [] as Array<Record<string, unknown>>,
        parentElement: null as Record<string, unknown> | null,
        attributes: new Map<string, string>(),
        className: "",
        textContent: "",
        innerHTML: "",
        hidden: false,
        classList: {
          add(...items: string[]) { items.forEach(i => classSet.add(i)); },
          remove(...items: string[]) { items.forEach(i => classSet.delete(i)); },
          contains(item: string) { return classSet.has(item); },
        },
        setAttribute(name: string, value: string) { (this.attributes as Map<string, string>).set(name, String(value)); },
        getAttribute(name: string) { return (this.attributes as Map<string, string>).get(name) ?? null; },
        removeAttribute(name: string) { (this.attributes as Map<string, string>).delete(name); },
        append(...items: Array<Record<string, unknown>>) {
          for (const item of items) {
            if (!item || typeof item !== "object") continue;
            if (item.parentElement) (item.remove as () => void)();
            item.parentElement = this;
            (this.children as Array<Record<string, unknown>>).push(item);
          }
        },
        replaceChildren(...items: Array<Record<string, unknown>>) {
          for (const child of [...(this.children as Array<Record<string, unknown>>)]) child.parentElement = null;
          this.children = [];
          (this.append as (...xs: Array<Record<string, unknown>>) => void)(...items);
        },
        remove() {
          const parent = this.parentElement as { children?: Array<Record<string, unknown>> } | null;
          if (!parent?.children) return;
          const idx = parent.children.indexOf(this);
          if (idx >= 0) parent.children.splice(idx, 1);
          this.parentElement = null;
        },
        querySelector: () => null,
        querySelectorAll: () => [],
        addEventListener() {},
        removeEventListener() {},
      };
      return node;
    };

    const documentObj: Record<string, unknown> = {
      title: "",
      head: makeNode("head"),
      body: makeNode("body"),
      documentElement: { dataset: {} as Record<string, string>, style: { colorScheme: "" } },
      createElement(tag: string) {
        const el = makeNode(tag);
        el.ownerDocument = documentObj;
        return el;
      },
      createTextNode: (text: string) => String(text),
      addEventListener() {},
      removeEventListener() {},
      querySelector: () => null,
      querySelectorAll: () => [],
    };

    type PluginState = {
      id: string;
      version: string;
      updatedAt: string;
      enabled: boolean;
      apiVersion: number;
      capabilities: string[];
      token: string;
      entry: { renderer: string; settings: string };
    };

    let currentRecord: PluginState = {
      id: "plugin.hot",
      version: "1.0.0",
      updatedAt: "t1",
      enabled: true,
      apiVersion: 2,
      capabilities: ["ui:settings"],
      token: "tok-v1",
      entry: { renderer: "renderer.js", settings: "settings.js" },
    };
    let companionEnabled = false;
    let pluginsFetchDelayMs = 0;
    let activateDelayByVersion: Record<string, number> = {};
    let faultVersion: string | null = null;
    const settingsMountsByVersion: Record<string, number> = {};
    const deactivatedVersions: string[] = [];
    const activatedVersions: string[] = [];

    (globalThis as Record<string, unknown>)[testKey] = {
      createPluginRuntime,
      loadPluginModule(source: string, requiredExport: string) {
        const match = /VERSION=([0-9.]+)/.exec(source);
        const ver = match ? match[1] : "unknown";
        if (requiredExport === "renderSettings") {
          return Promise.resolve({
            renderSettings(
              ctx: { onDispose(fn: () => void): void },
              element: { append(node: unknown): void },
            ) {
              settingsMountsByVersion[ver] = (settingsMountsByVersion[ver] || 0) + 1;
              const card = (documentObj.createElement as (t: string) => Record<string, unknown>)("section");
              card.textContent = `v${ver} settings`;
              element.append(card);
              ctx.onDispose(() => {
                (card.remove as () => void)();
              });
            },
          });
        }
        return Promise.resolve({
          async activate(ctx: { plugin: { version: string }; onDispose(fn: () => void): void }) {
            const delay = activateDelayByVersion[ctx.plugin.version] ?? 0;
            if (delay > 0) await new Promise(resolve => setTimeout(resolve, delay));
            if (faultVersion === ctx.plugin.version) {
              throw new Error(`activate fault v${ctx.plugin.version}`);
            }
            ctx.onDispose(() => {
              deactivatedVersions.push(ctx.plugin.version);
            });
            activatedVersions.push(ctx.plugin.version);
          },
        });
      },
    };

    const moduleSource = [
      `const hooks = globalThis.${testKey};`,
      "export function createEventChannel() { return { subscribe() { return () => {}; }, close() {} }; }",
      "export function createDOMContractState() { return {}; }",
      "export function evaluateDOMContracts() { return { probes: [], byId: {}, gates: {} }; }",
      "export function createEntryMountManager() { return { sync() {}, remove() {}, dispose() {} }; }",
      "export function createNativeHost(_t, _h, channel) { return { subscribe: (s, l) => channel.subscribe(s, l), close() {} }; }",
      "export function findSettingsDialogParts() { return null; }",
      "export function findOfficialSettingsPanel() { return null; }",
      "export function findOfficialSettingsSection() { return null; }",
      "export function findOfficialSettingsTab() { return null; }",
      "export function findSessionInfoKeepAliveAnchor() { return null; }",
      "export function findChatHeaderTail() { return null; }",
      "export const createPluginRuntime = (...args) => hooks.createPluginRuntime(...args);",
      "export const loadPluginModule = (...args) => hooks.loadPluginModule(...args);",
      "export function pluginEntryPath(p) { return p; }",
      "export function mountSurface() { return { unmount() {} }; }",
      "export function mountMainAreaSurface() { return { setWidth() {}, width: 0, dispose() {} }; }",
    ].join("\n");

    let blobCounter = 0;
    const fakeURL = {
      createObjectURL(blob: { __text: string }) {
        const file = join(tmpDir, `hot-update-${++blobCounter}.mjs`);
        writeFileSync(file, blob.__text, "utf8");
        return pathToFileURL(file).href;
      },
      revokeObjectURL() {},
    };
    class FakeBlob {
      __text: string;
      constructor(parts: unknown[]) {
        this.__text = parts.map(String).join("");
      }
    }
    class FakeMutationObserver {
      observe() {}
      disconnect() {}
    }

    const windowObj: Record<string, unknown> = {
      addEventListener() {},
      removeEventListener() {},
      dispatchEvent() { return true; },
      requestAnimationFrame: (cb: () => void) => setTimeout(cb, 0),
      cancelAnimationFrame: (id: ReturnType<typeof setTimeout>) => clearTimeout(id),
      location: { href: "app://renderer/index.html" },
      localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
      fetch: async (_url: string, init: { body: string }) => {
        const req = JSON.parse(init.body) as { id: string; action: string; payload?: { path?: string } };
        let result: unknown = {};
        if (req.action === "features.list") result = { multiSession: false };
        else if (req.action === "core.fetch") {
          const reqPath = req.payload?.path ?? "";
          let status = 200;
          let payload: unknown = {};
          const companionRecord: PluginState = {
            id: "plugin.companion",
            version: "1.0.0",
            updatedAt: "tc1",
            enabled: true,
            apiVersion: 2,
            capabilities: ["ui:settings"],
            token: "tok-companion",
            entry: { renderer: "renderer.js", settings: "settings.js" },
          };
          if (reqPath.endsWith("/plugins")) {
            if (pluginsFetchDelayMs > 0) {
              await new Promise(resolve => setTimeout(resolve, pluginsFetchDelayMs));
            }
            const items: PluginState[] = [{ ...currentRecord, capabilities: [...currentRecord.capabilities] }];
            if (companionEnabled) {
              items.push({ ...companionRecord, capabilities: [...companionRecord.capabilities] });
            }
            payload = { items };
          } else {
            const parsedUrl = new URL(reqPath, "http://core.local");
            const isCompanion = parsedUrl.pathname.includes("/plugins/plugin.companion/");
            const target = isCompanion ? companionRecord : currentRecord;
            const targetCaps = target.capabilities.join(",");
            const q = parsedUrl.searchParams;
            const mismatch = (q.has("version") && q.get("version") !== target.version)
              || (q.has("updatedAt") && q.get("updatedAt") !== target.updatedAt)
              || (q.has("capabilities") && q.get("capabilities") !== targetCaps);
            if (mismatch) {
              status = 409;
              payload = { error: { code: "PLUGIN_REVISION_MISMATCH", message: "插件版本已变更，请重新同步" } };
            } else {
              payload = {
                source: `// VERSION=${target.version}\nexport function activate() {} export function renderSettings() {}`,
                version: target.version,
                updatedAt: target.updatedAt,
                capabilities: [...target.capabilities],
              };
            }
          }
          result = { status, bodyBase64: Buffer.from(JSON.stringify(payload)).toString("base64") };
        }
        return { ok: true, status: 200, json: async () => ({ protocol: 1, id: req.id, result }) };
      },
    };
    (globalThis as { window?: unknown }).window = windowObj;

    const install = new Function(
      "window", "document", "MutationObserver", "URL", "Blob", "CustomEvent", "createProviderEnhance",
      `${renderer}\nreturn installNativeRenderer;`,
    )(
      windowObj,
      documentObj,
      FakeMutationObserver,
      fakeURL,
      FakeBlob,
      class CustomEvent {
        type: string;
        detail: unknown;
        constructor(type: string, init?: { detail?: unknown }) {
          this.type = type;
          this.detail = init?.detail;
        }
      },
      () => ({ install() {}, remove() {}, clearDecorations() {} }),
    ) as (options: Record<string, unknown>) => Promise<{ status: string }>;

    const currentUI = () => windowObj.__ekdNativeUI as { remove(): Promise<void> } | undefined;
    const currentPlugins = () => windowObj.__ekdPlugins as {
      sync(options?: { waitForActivationMs?: number }): Promise<void>;
      mountSettings(
        id: string,
        element: unknown,
        options?: { signal?: AbortSignal; version?: string; updatedAt?: string; capabilities?: string[] },
      ): Promise<() => void>;
    };

    const readPanelText = (el: Record<string, unknown>) => {
      const children = el.children as Array<{ children?: Array<{ textContent?: string }> }>;
      return children.map(c => c.children?.map(n => n.textContent ?? "").join("") ?? "").join("|");
    };

    await install({
      endpoint: "http://bridge",
      bridgeKey: "bridge-key",
      surface: "host",
      coreURL: "http://127.0.0.1:1001",
      moduleSource,
      cssSource: "",
    });
    await currentPlugins().sync();

    // 1. Mount v1 settings cleanly.
    const container = (documentObj.createElement as (t: string) => Record<string, unknown>)("div");
    const ctrlV1 = new AbortController();
    const releaseV1 = await currentPlugins().mountSettings("plugin.hot", container, {
      signal: ctrlV1.signal,
      version: "1.0.0",
      updatedAt: "t1",
      capabilities: ["ui:settings"],
    });
    assert.equal(readPanelText(container), "v1.0.0 settings");
    assert.equal(settingsMountsByVersion["1.0.0"], 1);

    // 2. Round-6 repro: Core hot-updates to v2 while reconciliation takes > 150ms sync budget,
    //    so syncPluginHost() returns while v1 is STILL registered in state.plugins.
    currentRecord = {
      ...currentRecord,
      version: "1.0.1",
      updatedAt: "t2",
      token: "tok-v2",
    };
    pluginsFetchDelayMs = 220;
    activateDelayByVersion = { "1.0.1": 100 };

    const syncErr = await syncPluginHost();
    assert.equal(syncErr, null);
    // v1 is STILL active right when syncPluginHost hits its 150ms budget.
    assert.deepEqual(deactivatedVersions, [], "v1 must still be registered when syncPluginHost returns early");
    assert.equal(activatedVersions.includes("1.0.1"), false);

    // Simulate PluginSettingsPanel re-rendering for v2: cleanup disposes v1, new effect mounts v2.
    ctrlV1.abort();
    releaseV1();
    const ctrlV2 = new AbortController();
    const releaseV2 = await currentPlugins().mountSettings("plugin.hot", container, {
      signal: ctrlV2.signal,
      version: "1.0.1",
      updatedAt: "t2",
      capabilities: ["ui:settings"],
    });

    assert.equal(settingsMountsByVersion["1.0.0"], 1, "must NOT remount v1 when v2 was requested");
    assert.equal(settingsMountsByVersion["1.0.1"], 1, "must mount v2 exactly once");
    assert.deepEqual(deactivatedVersions, ["1.0.0"]);
    assert.equal(activatedVersions.includes("1.0.1"), true);
    assert.equal(readPanelText(container), "v1.0.1 settings");

    // Wait briefly to confirm reconciliation does not subsequently wipe the mounted v2 panel.
    await new Promise(resolve => setTimeout(resolve, 40));
    assert.equal(readPanelText(container), "v1.0.1 settings");

    // 3. Hot-update to v3 (including new capability) where mountSettings is called BEFORE syncPluginHost starts.
    currentRecord = {
      ...currentRecord,
      version: "1.0.2",
      updatedAt: "t3",
      capabilities: ["ui:settings", "ui:entry"],
      token: "tok-v3",
    };
    pluginsFetchDelayMs = 20;
    activateDelayByVersion = { "1.0.2": 60 };

    ctrlV2.abort();
    releaseV2();
    const ctrlV3 = new AbortController();
    const releaseV3 = await currentPlugins().mountSettings("plugin.hot", container, {
      signal: ctrlV3.signal,
      version: "1.0.2",
      updatedAt: "t3",
      capabilities: ["ui:settings", "ui:entry"],
    });
    assert.equal(settingsMountsByVersion["1.0.1"], 1, "must NOT remount v2 when v3 was requested");
    assert.equal(settingsMountsByVersion["1.0.2"], 1);
    assert.equal(readPanelText(container), "v1.0.2 settings");

    // 4. Hot-update to v4 where v4 activate() faults -> preserves PLUGIN_ACTIVATION_FAILED and activation cause (never PLUGIN_DISABLED).
    currentRecord = {
      ...currentRecord,
      version: "1.0.3",
      updatedAt: "t4",
      token: "tok-v4",
    };
    pluginsFetchDelayMs = 10;
    activateDelayByVersion = { "1.0.3": 40 };
    faultVersion = "1.0.3";

    ctrlV3.abort();
    releaseV3();
    await assert.rejects(
      () => currentPlugins().mountSettings("plugin.hot", container, {
        signal: new AbortController().signal,
        version: "1.0.3",
        updatedAt: "t4",
        capabilities: ["ui:settings", "ui:entry"],
      }),
      (err: unknown) => {
        const e = err as { message?: string; code?: string; cause?: { message?: string } };
        return /插件未运行，无法打开设置页/.test(e?.message || "")
          && /activate fault v1\.0\.3/.test(e?.message || "")
          && e?.code === "PLUGIN_ACTIVATION_FAILED"
          && /activate fault v1\.0\.3/.test(e?.cause?.message || "");
      },
    );

    // 5a. Re-enable v5 alongside another enabled plugin, then disable plugin.hot mid-wait -> exercises pluginReleases loop disable path.
    faultVersion = null;
    companionEnabled = true;
    currentRecord = {
      ...currentRecord,
      version: "1.0.4",
      updatedAt: "t5",
      token: "tok-v5",
    };
    pluginsFetchDelayMs = 0;
    activateDelayByVersion = {};
    await currentPlugins().sync();

    currentRecord = { ...currentRecord, enabled: false };
    pluginsFetchDelayMs = 40;
    await assert.rejects(
      () => currentPlugins().mountSettings("plugin.hot", container, {
        signal: new AbortController().signal,
        version: "1.0.5",
        updatedAt: "t6",
        capabilities: ["ui:settings", "ui:entry"],
      }),
      (err: unknown) => {
        const e = err as { message?: string; code?: string };
        return /插件未运行，无法打开设置页/.test(e?.message || "") && e?.code === "PLUGIN_DISABLED";
      },
    );

    // 5b. Re-enable v5 as the sole plugin, then disable mid-wait -> exercises !enabled.length disable path.
    companionEnabled = false;
    currentRecord = { ...currentRecord, enabled: true };
    pluginsFetchDelayMs = 0;
    await currentPlugins().sync();

    currentRecord = { ...currentRecord, enabled: false };
    pluginsFetchDelayMs = 40;
    await assert.rejects(
      () => currentPlugins().mountSettings("plugin.hot", container, {
        signal: new AbortController().signal,
        version: "1.0.5",
        updatedAt: "t6",
        capabilities: ["ui:settings", "ui:entry"],
      }),
      (err: unknown) => {
        const e = err as { message?: string; code?: string };
        return /插件未运行，无法打开设置页/.test(e?.message || "") && e?.code === "PLUGIN_DISABLED";
      },
    );

    await currentUI()!.remove();
  } finally {
    if (prevWindow === undefined) delete (globalThis as { window?: unknown }).window;
    else (globalThis as { window?: unknown }).window = prevWindow;
    delete (globalThis as Record<string, unknown>)[testKey];
    rmSync(tmpDir, { recursive: true, force: true });
  }
});

test("renderer.js rejects mismatched plugin asset revisions before import/cache, reconciles and retries settings mounts, and avoids faulting activation mismatches", async () => {
  const renderer = readFileSync("../internal/desktop/assets/renderer.js", "utf8");
  const tmpDir = mkdtempSync(join(tmpdir(), "ekd-asset-rev-bind-"));
  const testKey = `__ekdAssetRevBind_${Date.now()}_${Math.random().toString(36).slice(2)}`;
  const prevWindow = (globalThis as { window?: unknown }).window;

  try {
    const makeNode = (tag: string) => {
      const classSet = new Set<string>();
      const node: Record<string, unknown> = {
        tagName: tag.toUpperCase(),
        dataset: {} as Record<string, string>,
        style: { cssText: "", setProperty() {}, removeProperty() {} },
        children: [] as Array<Record<string, unknown>>,
        parentElement: null as Record<string, unknown> | null,
        attributes: new Map<string, string>(),
        className: "",
        textContent: "",
        innerHTML: "",
        hidden: false,
        classList: {
          add(...items: string[]) { items.forEach(i => classSet.add(i)); },
          remove(...items: string[]) { items.forEach(i => classSet.delete(i)); },
          contains(item: string) { return classSet.has(item); },
        },
        setAttribute(name: string, value: string) { (this.attributes as Map<string, string>).set(name, String(value)); },
        getAttribute(name: string) { return (this.attributes as Map<string, string>).get(name) ?? null; },
        removeAttribute(name: string) { (this.attributes as Map<string, string>).delete(name); },
        append(...items: Array<Record<string, unknown>>) {
          for (const item of items) {
            if (!item || typeof item !== "object") continue;
            if (item.parentElement) (item.remove as () => void)();
            item.parentElement = this;
            (this.children as Array<Record<string, unknown>>).push(item);
          }
        },
        replaceChildren(...items: Array<Record<string, unknown>>) {
          for (const child of [...(this.children as Array<Record<string, unknown>>)]) child.parentElement = null;
          this.children = [];
          (this.append as (...xs: Array<Record<string, unknown>>) => void)(...items);
        },
        remove() {
          const parent = this.parentElement as { children?: Array<Record<string, unknown>> } | null;
          if (!parent?.children) return;
          const idx = parent.children.indexOf(this);
          if (idx >= 0) parent.children.splice(idx, 1);
          this.parentElement = null;
        },
        querySelector: () => null,
        querySelectorAll: () => [],
        addEventListener() {},
        removeEventListener() {},
      };
      return node;
    };

    const documentObj: Record<string, unknown> = {
      title: "",
      head: makeNode("head"),
      body: makeNode("body"),
      documentElement: { dataset: {} as Record<string, string>, style: { colorScheme: "" } },
      createElement(tag: string) {
        const el = makeNode(tag);
        el.ownerDocument = documentObj;
        return el;
      },
      createTextNode: (text: string) => String(text),
      addEventListener() {},
      removeEventListener() {},
      querySelector: () => null,
      querySelectorAll: () => [],
    };

    type PluginState = {
      id: string;
      version: string;
      updatedAt: string;
      enabled: boolean;
      apiVersion: number;
      capabilities: string[];
      token: string;
      entry: { renderer: string; settings: string };
    };

    let currentRecord: PluginState = {
      id: "plugin.hot",
      version: "1.0.0",
      updatedAt: "t1",
      enabled: true,
      apiVersion: 2,
      capabilities: ["ui:settings"],
      token: "tok-v1",
      entry: { renderer: "renderer.js", settings: "settings.js" },
    };

    let force200MismatchedMeta = false;
    let bumpOnRendererAssetVersion: { from: string; to: PluginState } | null = null;
    let holdSettingsAssetForVersion: {
      version: string;
      onEntered: () => void;
      gate: Promise<void>;
    } | null = null;
    const requestedAssetPaths: string[] = [];
    const importedModules: Array<{ requiredExport: string; version: string }> = [];
    const mountedPairs: Array<{ moduleVersion: string; activeVersion: string }> = [];
    const activatedVersions: string[] = [];
    const faultReports: Array<{ path: string; body: unknown }> = [];
    const activationWaiters = new Map<string, Array<() => void>>();

    const waitForActivation = (ver: string) => new Promise<void>((resolve, reject) => {
      if (activatedVersions.includes(ver)) {
        resolve();
        return;
      }
      const timer = setTimeout(() => reject(new Error(`timed out waiting for activation of v${ver}`)), 1500);
      const list = activationWaiters.get(ver) ?? [];
      list.push(() => {
        clearTimeout(timer);
        resolve();
      });
      activationWaiters.set(ver, list);
    });

    (globalThis as Record<string, unknown>)[testKey] = {
      createPluginRuntime,
      loadPluginModule(source: string, requiredExport: string) {
        const match = /VERSION=([0-9.]+)/.exec(source);
        const ver = match ? match[1] : "unknown";
        importedModules.push({ requiredExport, version: ver });
        if (requiredExport === "renderSettings") {
          return Promise.resolve({
            renderSettings(
              ctx: { plugin: { version: string }; onDispose(fn: () => void): void },
              element: { append(node: unknown): void },
            ) {
              mountedPairs.push({ moduleVersion: ver, activeVersion: ctx.plugin.version });
              const card = (documentObj.createElement as (t: string) => Record<string, unknown>)("section");
              card.textContent = `v${ver}@inst-${ctx.plugin.version}`;
              element.append(card);
              ctx.onDispose(() => {
                (card.remove as () => void)();
              });
            },
          });
        }
        return Promise.resolve({
          async activate(ctx: { plugin: { version: string } }) {
            activatedVersions.push(ctx.plugin.version);
            const waiters = activationWaiters.get(ctx.plugin.version);
            if (waiters) {
              activationWaiters.delete(ctx.plugin.version);
              for (const fn of waiters) fn();
            }
          },
        });
      },
    };

    const moduleSource = [
      `const hooks = globalThis.${testKey};`,
      "export function createEventChannel() { return { subscribe() { return () => {}; }, close() {} }; }",
      "export function createDOMContractState() { return {}; }",
      "export function evaluateDOMContracts() { return { probes: [], byId: {}, gates: {} }; }",
      "export function createEntryMountManager() { return { sync() {}, remove() {}, dispose() {} }; }",
      "export function createNativeHost(_t, _h, channel) { return { subscribe: (s, l) => channel.subscribe(s, l), close() {} }; }",
      "export function findSettingsDialogParts() { return null; }",
      "export function findOfficialSettingsPanel() { return null; }",
      "export function findOfficialSettingsSection() { return null; }",
      "export function findOfficialSettingsTab() { return null; }",
      "export function findSessionInfoKeepAliveAnchor() { return null; }",
      "export function findChatHeaderTail() { return null; }",
      "export const createPluginRuntime = (...args) => hooks.createPluginRuntime(...args);",
      "export const loadPluginModule = (...args) => hooks.loadPluginModule(...args);",
      "export function pluginEntryPath(p) { return p; }",
      "export function mountSurface() { return { unmount() {} }; }",
      "export function mountMainAreaSurface() { return { setWidth() {}, width: 0, dispose() {} }; }",
    ].join("\n");

    let blobCounter = 0;
    const fakeURL = {
      createObjectURL(blob: { __text: string }) {
        const file = join(tmpDir, `asset-rev-${++blobCounter}.mjs`);
        writeFileSync(file, blob.__text, "utf8");
        return pathToFileURL(file).href;
      },
      revokeObjectURL() {},
    };
    class FakeBlob {
      __text: string;
      constructor(parts: unknown[]) {
        this.__text = parts.map(String).join("");
      }
    }
    class FakeMutationObserver {
      observe() {}
      disconnect() {}
    }

    const windowObj: Record<string, unknown> = {
      addEventListener() {},
      removeEventListener() {},
      dispatchEvent() { return true; },
      requestAnimationFrame: (cb: () => void) => setTimeout(cb, 0),
      cancelAnimationFrame: (id: ReturnType<typeof setTimeout>) => clearTimeout(id),
      location: { href: "app://renderer/index.html" },
      localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
      fetch: async (_url: string, init: { body: string }) => {
        const req = JSON.parse(init.body) as { id: string; action: string; payload?: { path?: string; bodyBase64?: string } };
        let result: unknown = {};
        if (req.action === "features.list") result = { multiSession: false };
        else if (req.action === "core.fetch") {
          const reqPath = req.payload?.path ?? "";
          let status = 200;
          let payload: unknown = {};
          if (reqPath.includes("/plugin-runtime/report.error")) {
            faultReports.push({
              path: reqPath,
              body: req.payload?.bodyBase64 ? JSON.parse(Buffer.from(req.payload.bodyBase64, "base64").toString("utf8")) : null,
            });
          } else if (reqPath.endsWith("/plugins")) {
            payload = { items: [{ ...currentRecord, capabilities: [...currentRecord.capabilities] }] };
          } else if (reqPath.includes("/assets/")) {
            requestedAssetPaths.push(reqPath);
            const parsedUrl = new URL(reqPath, "http://core.local");
            const q = parsedUrl.searchParams;
            if (
              bumpOnRendererAssetVersion
              && parsedUrl.pathname.endsWith("/assets/renderer.js")
              && q.get("version") === bumpOnRendererAssetVersion.from
            ) {
              currentRecord = bumpOnRendererAssetVersion.to;
              bumpOnRendererAssetVersion = null;
            }
            if (
              holdSettingsAssetForVersion
              && parsedUrl.pathname.endsWith("/assets/settings.js")
              && q.get("version") === holdSettingsAssetForVersion.version
            ) {
              const held = holdSettingsAssetForVersion;
              holdSettingsAssetForVersion = null;
              held.onEntered();
              await held.gate;
            }
            const targetCaps = currentRecord.capabilities.join(",");
            const mismatch = (q.has("version") && q.get("version") !== currentRecord.version)
              || (q.has("updatedAt") && q.get("updatedAt") !== currentRecord.updatedAt)
              || (q.has("capabilities") && q.get("capabilities") !== targetCaps);
            if (mismatch && !force200MismatchedMeta) {
              status = 409;
              payload = { error: { code: "PLUGIN_REVISION_MISMATCH", message: "插件版本已变更，请重新同步" } };
            } else {
              payload = {
                source: `// VERSION=${currentRecord.version}\nexport function activate() {} export function renderSettings() {}`,
                version: currentRecord.version,
                updatedAt: currentRecord.updatedAt,
                capabilities: [...currentRecord.capabilities],
              };
            }
          }
          result = { status, bodyBase64: Buffer.from(JSON.stringify(payload)).toString("base64") };
        }
        return { ok: true, status: 200, json: async () => ({ protocol: 1, id: req.id, result }) };
      },
    };
    (globalThis as { window?: unknown }).window = windowObj;

    const install = new Function(
      "window", "document", "MutationObserver", "URL", "Blob", "CustomEvent", "createProviderEnhance",
      `${renderer}\nreturn installNativeRenderer;`,
    )(
      windowObj,
      documentObj,
      FakeMutationObserver,
      fakeURL,
      FakeBlob,
      class CustomEvent {
        type: string;
        detail: unknown;
        constructor(type: string, init?: { detail?: unknown }) {
          this.type = type;
          this.detail = init?.detail;
        }
      },
      () => ({ install() {}, remove() {}, clearDecorations() {} }),
    ) as (options: Record<string, unknown>) => Promise<{ status: string }>;

    const currentUI = () => windowObj.__ekdNativeUI as { remove(): Promise<void> } | undefined;
    const currentPlugins = () => windowObj.__ekdPlugins as {
      sync(options?: { waitForActivationMs?: number }): Promise<void>;
      mountSettings(
        id: string,
        element: unknown,
        options?: { signal?: AbortSignal; version?: string; updatedAt?: string; capabilities?: string[] },
      ): Promise<() => void>;
    };
    const readPanelText = (el: Record<string, unknown>) => {
      const children = el.children as Array<{ children?: Array<{ textContent?: string }> }>;
      return children.map(c => c.children?.map(n => n.textContent ?? "").join("") ?? "").join("|");
    };

    await install({
      endpoint: "http://bridge",
      bridgeKey: "bridge-key",
      surface: "host",
      coreURL: "http://127.0.0.1:1001",
      moduleSource,
      cssSource: "",
    });
    await currentPlugins().sync();
    assert.deepEqual(activatedVersions, ["1.0.0"]);

    // 1. Round-7 repro (409 PLUGIN_REVISION_MISMATCH): 1.0.0 is active, core silently updates to 1.0.1 before settings.js read.
    currentRecord = {
      ...currentRecord,
      version: "1.0.1",
      updatedAt: "t2",
      token: "tok-v2",
    };
    const elPinnedV1 = (documentObj.createElement as (t: string) => Record<string, unknown>)("div");
    const v2Activated = waitForActivation("1.0.1");
    await assert.rejects(
      () => currentPlugins().mountSettings("plugin.hot", elPinnedV1, {
        signal: new AbortController().signal,
        version: "1.0.0",
        updatedAt: "t1",
        capabilities: ["ui:settings"],
      }),
      (err: unknown) => (err as { code?: string })?.code === "PLUGIN_REVISION_MISMATCH",
    );
    assert.ok(
      requestedAssetPaths.some(p => p.includes("/assets/settings.js?version=1.0.0&updatedAt=t1&capabilities=ui%3Asettings")),
      "settings asset request must carry bound revision query",
    );
    assert.deepEqual(
      importedModules.filter(m => m.requiredExport === "renderSettings"),
      [],
      "1.0.1 settings module must NOT be imported or cached under 1.0.0",
    );
    assert.equal(readPanelText(elPinnedV1), "");
    await v2Activated;

    // 2. Round-7 repro (fail-closed metadata check): even if a response returns 200 with newer metadata (1.0.2),
    //    loadPluginAsset must reject before loadPluginModule and before writing cache.
    currentRecord = {
      ...currentRecord,
      version: "1.0.2",
      updatedAt: "t3",
      token: "tok-v3",
    };
    force200MismatchedMeta = true;
    const elPinnedV2 = (documentObj.createElement as (t: string) => Record<string, unknown>)("div");
    const v3Activated = waitForActivation("1.0.2");
    await assert.rejects(
      () => currentPlugins().mountSettings("plugin.hot", elPinnedV2, {
        signal: new AbortController().signal,
        version: "1.0.1",
        updatedAt: "t2",
        capabilities: ["ui:settings"],
      }),
      (err: unknown) => (err as { code?: string })?.code === "PLUGIN_REVISION_MISMATCH",
    );
    assert.deepEqual(
      importedModules.filter(m => m.requiredExport === "renderSettings"),
      [],
      "mismatched 200 metadata must reject before loadPluginModule",
    );
    force200MismatchedMeta = false;
    await v3Activated;

    // 3. Unpinned mountSettings reconciles after 409 mismatch, waits for 1.0.3 activation, and mounts 1.0.3 cleanly.
    currentRecord = {
      ...currentRecord,
      version: "1.0.3",
      updatedAt: "t4",
      token: "tok-v4",
    };
    const elUnpinned = (documentObj.createElement as (t: string) => Record<string, unknown>)("div");
    const releaseV4 = await currentPlugins().mountSettings("plugin.hot", elUnpinned);
    assert.equal(readPanelText(elUnpinned), "v1.0.3@inst-1.0.3");
    assert.deepEqual(mountedPairs, [{ moduleVersion: "1.0.3", activeVersion: "1.0.3" }]);
    releaseV4();

    // 4. Activation hitting 409 PLUGIN_REVISION_MISMATCH on renderer.js must NOT report a plugin fault and must resync to 1.0.5.
    currentRecord = {
      ...currentRecord,
      version: "1.0.4",
      updatedAt: "t5",
      token: "tok-v5",
    };
    bumpOnRendererAssetVersion = {
      from: "1.0.4",
      to: {
        ...currentRecord,
        version: "1.0.5",
        updatedAt: "t6",
        token: "tok-v6",
      },
    };
    const v6Activated = waitForActivation("1.0.5");
    await currentPlugins().sync();
    await v6Activated;
    assert.deepEqual(faultReports, [], "activation revision mismatch must not report a fault");
    assert.equal(activatedVersions.includes("1.0.4"), false);
    assert.equal(activatedVersions.includes("1.0.5"), true);

    // 5. Concurrent syncPlugins() hot-updating 1.0.5 -> 1.0.6 while unpinned mountSettings is waiting on settings.js
    //    must NOT cancel the mount request; once settings.js returns 409, mountSettings retries and mounts 1.0.6.
    let releaseHeldSettings!: () => void;
    const heldGate = new Promise<void>(resolve => { releaseHeldSettings = resolve; });
    let notifyHeldEntered!: () => void;
    const heldEntered = new Promise<void>(resolve => { notifyHeldEntered = resolve; });
    holdSettingsAssetForVersion = {
      version: "1.0.5",
      onEntered: notifyHeldEntered,
      gate: heldGate,
    };

    const elConcurrent = (documentObj.createElement as (t: string) => Record<string, unknown>)("div");
    const pendingConcurrentMount = currentPlugins().mountSettings("plugin.hot", elConcurrent);
    await heldEntered;

    currentRecord = {
      ...currentRecord,
      version: "1.0.6",
      updatedAt: "t7",
      token: "tok-v7",
    };
    await currentPlugins().sync();
    releaseHeldSettings();

    const releaseV7 = await pendingConcurrentMount;
    assert.equal(readPanelText(elConcurrent), "v1.0.6@inst-1.0.6");
    assert.deepEqual(mountedPairs[mountedPairs.length - 1], { moduleVersion: "1.0.6", activeVersion: "1.0.6" });
    releaseV7();

    await currentUI()!.remove();
  } finally {
    if (prevWindow === undefined) delete (globalThis as { window?: unknown }).window;
    else (globalThis as { window?: unknown }).window = prevWindow;
    delete (globalThis as Record<string, unknown>)[testKey];
    rmSync(tmpDir, { recursive: true, force: true });
  }
});

test("bootstrap.js core.fetch validates pathname only, allows dotted credential capability queries end-to-end with renderer.js, and blocks traversal variants", async () => {
  const bootstrap = readFileSync("../internal/desktop/assets/bootstrap.js", "utf8");
  const renderer = readFileSync("../internal/desktop/assets/renderer.js", "utf8");
  const tmpDir = mkdtempSync(join(tmpdir(), "ekd-dotted-cred-query-"));
  const testKey = `__ekdDottedCred_${Date.now()}_${Math.random().toString(36).slice(2)}`;
  const prevWindow = (globalThis as { window?: unknown }).window;

  const coreRequests: string[] = [];
  const faultReports: unknown[] = [];
  const dottedRecord = {
    id: "test.plugin",
    version: "1.0.0",
    updatedAt: "t1",
    enabled: true,
    apiVersion: 2,
    capabilities: ["ui:settings", "credentials:api..token"],
    token: "tok-dotted",
    entry: { renderer: "renderer.js", settings: "settings.js" },
  };

  const coreServer = http.createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", chunk => chunks.push(Buffer.from(chunk)));
    req.on("end", () => {
      const reqUrl = req.url ?? "";
      coreRequests.push(reqUrl);
      if (reqUrl === "/enhance/v1/pair") {
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ sessionToken: "core-sess-tok", expiresAt: new Date(Date.now() + 3600_000).toISOString() }));
        return;
      }
      if (reqUrl === "/enhance/v1/desktop/actions/features.get") {
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ value: { multiSession: false } }));
        return;
      }
      if (reqUrl === "/enhance/v1/plugins") {
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ items: [dottedRecord] }));
        return;
      }
      if (reqUrl.startsWith("/enhance/v1/plugin-runtime/report.error")) {
        faultReports.push(Buffer.concat(chunks).toString("utf8"));
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ stopped: false }));
        return;
      }
      if (reqUrl.startsWith("/enhance/v1/plugins/test.plugin/assets/")) {
        const parsed = new URL(reqUrl, "http://core.local");
        const q = parsed.searchParams;
        if (
          q.get("version") === dottedRecord.version
          && q.get("updatedAt") === dottedRecord.updatedAt
          && q.get("capabilities") === dottedRecord.capabilities.join(",")
        ) {
          res.writeHead(200, { "Content-Type": "application/json" });
          res.end(JSON.stringify({
            path: parsed.pathname.slice("/enhance/v1/plugins/test.plugin/assets/".length),
            source: "export function activate() {} export function renderSettings() {}",
            version: dottedRecord.version,
            updatedAt: dottedRecord.updatedAt,
            capabilities: dottedRecord.capabilities,
          }));
          return;
        }
        res.writeHead(409, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ error: { code: "PLUGIN_REVISION_MISMATCH", message: "插件版本已变更，请重新同步" } }));
        return;
      }
      res.writeHead(404, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ error: { code: "NOT_FOUND", message: "not found" } }));
    });
  });

  await new Promise<void>(resolve => coreServer.listen(0, "127.0.0.1", resolve));
  const coreAddr = coreServer.address();
  assert.ok(coreAddr && typeof coreAddr === "object");
  const coreURL = `http://127.0.0.1:${coreAddr.port}`;

  let capturedConfig: { endpoint: string; bridgeKey: string } | null = null;
  const mainWC = {
    id: 1,
    isDestroyed: () => false,
    getURL: () => "app://renderer/index.html",
    on() {},
    once() {},
    removeListener() {},
    async executeJavaScript(code: string) {
      const match = /\((?:\{[\s\S]*\})\)$/.exec(code.trim());
      if (match) {
        const parsed = JSON.parse(match[0].slice(1, -1)) as { endpoint?: string; bridgeKey?: string };
        if (parsed.endpoint && parsed.bridgeKey) {
          capturedConfig = { endpoint: parsed.endpoint, bridgeKey: parsed.bridgeKey };
        }
      }
      return { status: "installed" };
    },
  };
  const electronStub = {
    webContents: {
      getAllWebContents: () => [mainWC],
      fromId: (id: number) => (id === 1 ? mainWC : null),
    },
    BrowserWindow: { fromWebContents: () => null },
    WebContentsView: class {},
    session: { fromPartition: () => ({}) },
    app: { getVersion: () => "1.4.3", on() {}, once() {}, removeListener() {} },
    dialog: {},
    shell: {},
    ipcMain: { on() {}, removeListener() {}, prependListener() {} },
  };
  const customRequire = (id: string) => {
    if (id === "electron") return electronStub;
    if (id === "node:http") return http;
    if (id === "node:crypto") return crypto;
    throw new Error(`unexpected require: ${id}`);
  };

  try {
    const installBootstrap = new Function("require", `${bootstrap}\nreturn installNativeBootstrap;`)(customRequire) as (
      opts: Record<string, unknown>,
    ) => Promise<{ status: string }>;
    const installed = await installBootstrap({
      coreURL,
      pairingToken: "pair-tok",
      expectedApp: "1.4.3",
      expectedElectron: process.versions.electron,
      rendererSource: "function(c) { return c; }",
      moduleSource: "",
      cssSource: "",
    });
    assert.equal(installed.status, "installed");
    assert.ok(capturedConfig);
    const { endpoint, bridgeKey } = capturedConfig;

    const callBridge = async (path: string) => {
      const res = await fetch(`${endpoint}/ekd/native/v1`, {
        method: "POST",
        headers: {
          Origin: "app://renderer",
          "Content-Type": "application/json",
          "X-EKD-Bridge": bridgeKey,
        },
        body: JSON.stringify({
          protocol: 1,
          id: `req-${Math.random().toString(36).slice(2)}`,
          action: "core.fetch",
          payload: { method: "GET", path },
        }),
      });
      assert.equal(res.status, 200);
      return await res.json() as { result?: { status: number; bodyBase64?: string }; error?: { code?: string; message?: string } };
    };

    // 1. Query string containing consecutive dots (`credentials:api..token`) must pass and forward intact.
    const allowedQueryPath = "/enhance/v1/plugins/test.plugin/assets/renderer.js?version=1.0.0&updatedAt=t1&capabilities=ui%3Asettings%2Ccredentials%3Aapi..token";
    const okResp = await callBridge(allowedQueryPath);
    assert.equal(okResp.error, undefined);
    assert.equal(okResp.result?.status, 200);
    assert.ok(coreRequests.includes(allowedQueryPath), "exact pathname and dotted query must reach core intact");

    // 2. Path traversal variants must be rejected before reaching core.
    const coreCountBeforeBlocked = coreRequests.length;
    for (const badPath of [
      "/enhance/v1/plugins/../secret",
      "/enhance/v1/plugins/%2e%2e/secret",
      "/enhance/v1/plugins/a%2fb",
      "/enhance/v1/plugins\\secret",
      "/enhance/v1/plugins/test.plugin/assets/renderer.js?version=1.0.0#frag",
    ]) {
      const blocked = await callBridge(badPath);
      assert.ok(blocked.error?.message?.includes("核心请求路径不被允许"), `expected ${badPath} to be rejected, got ${JSON.stringify(blocked)}`);
    }
    assert.equal(coreRequests.length, coreCountBeforeBlocked, "blocked traversal paths must never reach core");

    // 3. End-to-end renderer.js -> bootstrap.js -> core: plugin with `credentials:api..token` activates and mounts settings with zero faults.
    const makeNode = (tag: string) => {
      const classSet = new Set<string>();
      const node: Record<string, unknown> = {
        tagName: tag.toUpperCase(),
        dataset: {} as Record<string, string>,
        style: { cssText: "", setProperty() {}, removeProperty() {} },
        children: [] as Array<Record<string, unknown>>,
        parentElement: null as Record<string, unknown> | null,
        attributes: new Map<string, string>(),
        className: "",
        textContent: "",
        innerHTML: "",
        hidden: false,
        classList: {
          add(...items: string[]) { items.forEach(i => classSet.add(i)); },
          remove(...items: string[]) { items.forEach(i => classSet.delete(i)); },
          contains(item: string) { return classSet.has(item); },
        },
        setAttribute(name: string, value: string) { (this.attributes as Map<string, string>).set(name, String(value)); },
        getAttribute(name: string) { return (this.attributes as Map<string, string>).get(name) ?? null; },
        removeAttribute(name: string) { (this.attributes as Map<string, string>).delete(name); },
        append(...items: Array<Record<string, unknown>>) {
          for (const item of items) {
            if (!item || typeof item !== "object") continue;
            if (item.parentElement) (item.remove as () => void)();
            item.parentElement = this;
            (this.children as Array<Record<string, unknown>>).push(item);
          }
        },
        replaceChildren(...items: Array<Record<string, unknown>>) {
          for (const child of [...(this.children as Array<Record<string, unknown>>)]) child.parentElement = null;
          this.children = [];
          (this.append as (...xs: Array<Record<string, unknown>>) => void)(...items);
        },
        remove() {
          const parent = this.parentElement as { children?: Array<Record<string, unknown>> } | null;
          if (!parent?.children) return;
          const idx = parent.children.indexOf(this);
          if (idx >= 0) parent.children.splice(idx, 1);
          this.parentElement = null;
        },
        querySelector: () => null,
        querySelectorAll: () => [],
        addEventListener() {},
        removeEventListener() {},
      };
      return node;
    };
    const documentObj: Record<string, unknown> = {
      title: "",
      head: makeNode("head"),
      body: makeNode("body"),
      documentElement: { dataset: {} as Record<string, string>, style: { colorScheme: "" } },
      createElement(tag: string) {
        const el = makeNode(tag);
        el.ownerDocument = documentObj;
        return el;
      },
      createTextNode: (text: string) => String(text),
      addEventListener() {},
      removeEventListener() {},
      querySelector: () => null,
      querySelectorAll: () => [],
    };
    let activatedCount = 0;
    (globalThis as Record<string, unknown>)[testKey] = {
      createPluginRuntime,
      loadPluginModule(_source: string, requiredExport: string) {
        if (requiredExport === "renderSettings") {
          return Promise.resolve({
            renderSettings(ctx: { onDispose(fn: () => void): void }, element: { append(node: unknown): void }) {
              const node = (documentObj.createElement as (t: string) => Record<string, unknown>)("div");
              node.textContent = "dotted-settings-ok";
              element.append(node);
              ctx.onDispose(() => (node.remove as () => void)());
            },
          });
        }
        return Promise.resolve({
          activate() {
            activatedCount += 1;
          },
        });
      },
    };
    const moduleSource = [
      `const hooks = globalThis.${testKey};`,
      "export function createEventChannel() { return { subscribe() { return () => {}; }, close() {} }; }",
      "export function createDOMContractState() { return {}; }",
      "export function evaluateDOMContracts() { return { probes: [], byId: {}, gates: {} }; }",
      "export function createEntryMountManager() { return { sync() {}, remove() {}, dispose() {} }; }",
      "export function createNativeHost(_t, _h, channel) { return { subscribe: (s, l) => channel.subscribe(s, l), close() {} }; }",
      "export function findSettingsDialogParts() { return null; }",
      "export function findOfficialSettingsPanel() { return null; }",
      "export function findOfficialSettingsSection() { return null; }",
      "export function findOfficialSettingsTab() { return null; }",
      "export function findSessionInfoKeepAliveAnchor() { return null; }",
      "export function findChatHeaderTail() { return null; }",
      "export const createPluginRuntime = (...args) => hooks.createPluginRuntime(...args);",
      "export const loadPluginModule = (...args) => hooks.loadPluginModule(...args);",
      "export function pluginEntryPath(p) { return p; }",
      "export function mountSurface() { return { unmount() {} }; }",
      "export function mountMainAreaSurface() { return { setWidth() {}, width: 0, dispose() {} }; }",
    ].join("\n");
    let blobCounter = 0;
    const fakeURL = {
      createObjectURL(blob: { __text: string }) {
        const file = join(tmpDir, `dotted-${++blobCounter}.mjs`);
        writeFileSync(file, blob.__text, "utf8");
        return pathToFileURL(file).href;
      },
      revokeObjectURL() {},
    };
    class FakeBlob {
      __text: string;
      constructor(parts: unknown[]) { this.__text = parts.map(String).join(""); }
    }
    class FakeMutationObserver {
      observe() {}
      disconnect() {}
    }
    const windowObj: Record<string, unknown> = {
      addEventListener() {},
      removeEventListener() {},
      dispatchEvent() { return true; },
      requestAnimationFrame: (cb: () => void) => setTimeout(cb, 0),
      cancelAnimationFrame: (id: ReturnType<typeof setTimeout>) => clearTimeout(id),
      location: { href: "app://renderer/index.html" },
      localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
      fetch: (url: string, init?: RequestInit) => fetch(url, {
        ...init,
        headers: {
          ...(init?.headers as Record<string, string> | undefined),
          Origin: "app://renderer",
        },
      }),
    };
    (globalThis as { window?: unknown }).window = windowObj;
    const installRenderer = new Function(
      "window", "document", "MutationObserver", "URL", "Blob", "CustomEvent", "createProviderEnhance",
      `${renderer}\nreturn installNativeRenderer;`,
    )(
      windowObj,
      documentObj,
      FakeMutationObserver,
      fakeURL,
      FakeBlob,
      class CustomEvent {
        type: string;
        detail: unknown;
        constructor(type: string, init?: { detail?: unknown }) {
          this.type = type;
          this.detail = init?.detail;
        }
      },
      () => ({ install() {}, remove() {}, clearDecorations() {} }),
    ) as (opts: Record<string, unknown>) => Promise<{ status: string }>;

    await installRenderer({
      endpoint,
      bridgeKey,
      surface: "host",
      coreURL,
      moduleSource,
      cssSource: "",
    });
    const pluginsBridge = windowObj.__ekdPlugins as {
      sync(): Promise<void>;
      mountSettings(id: string, element: unknown, opts?: { version?: string; updatedAt?: string; capabilities?: string[] }): Promise<() => void>;
    };
    await pluginsBridge.sync();
    assert.equal(activatedCount, 1);

    const panelHost = (documentObj.createElement as (t: string) => Record<string, unknown>)("div");
    const release = await pluginsBridge.mountSettings("test.plugin", panelHost, {
      version: dottedRecord.version,
      updatedAt: dottedRecord.updatedAt,
      capabilities: dottedRecord.capabilities,
    });
    const text = (panelHost.children as Array<{ children?: Array<{ textContent?: string }> }>)
      .map(c => c.children?.map(n => n.textContent ?? "").join("") ?? "")
      .join("|");
    assert.equal(text, "dotted-settings-ok");
    assert.deepEqual(faultReports, []);
    release();
    await (windowObj.__ekdNativeUI as { remove(): Promise<void> }).remove();
  } finally {
    if (prevWindow === undefined) delete (globalThis as { window?: unknown }).window;
    else (globalThis as { window?: unknown }).window = prevWindow;
    delete (globalThis as Record<string, unknown>)[testKey];
    const nativeState = (globalThis as { __ekdNative?: { cleanup?: () => Promise<void> } }).__ekdNative;
    if (nativeState?.cleanup) await nativeState.cleanup();
    delete (globalThis as { __ekdNativeBoot?: unknown }).__ekdNativeBoot;
    coreServer.closeAllConnections();
    await new Promise<void>(resolve => coreServer.close(() => resolve()));
    rmSync(tmpDir, { recursive: true, force: true });
  }
});

test("renderer.js preserves PLUGIN_REVISION_MISMATCH across hot updates during async renderSettings on both resolve and abort-reject paths", async () => {
  const renderer = readFileSync("../internal/desktop/assets/renderer.js", "utf8");
  const tmpDir = mkdtempSync(join(tmpdir(), "ekd-async-render-hot-"));
  const testKey = `__ekdAsyncRenderHot_${Date.now()}_${Math.random().toString(36).slice(2)}`;
  const prevWindow = (globalThis as { window?: unknown }).window;

  try {
    const makeNode = (tag: string) => {
      const classSet = new Set<string>();
      const node: Record<string, unknown> = {
        tagName: tag.toUpperCase(),
        dataset: {} as Record<string, string>,
        style: { cssText: "", setProperty() {}, removeProperty() {} },
        children: [] as Array<Record<string, unknown>>,
        parentElement: null as Record<string, unknown> | null,
        attributes: new Map<string, string>(),
        className: "",
        textContent: "",
        innerHTML: "",
        hidden: false,
        classList: {
          add(...items: string[]) { items.forEach(i => classSet.add(i)); },
          remove(...items: string[]) { items.forEach(i => classSet.delete(i)); },
          contains(item: string) { return classSet.has(item); },
        },
        setAttribute(name: string, value: string) { (this.attributes as Map<string, string>).set(name, String(value)); },
        getAttribute(name: string) { return (this.attributes as Map<string, string>).get(name) ?? null; },
        removeAttribute(name: string) { (this.attributes as Map<string, string>).delete(name); },
        append(...items: Array<Record<string, unknown>>) {
          for (const item of items) {
            if (!item || typeof item !== "object") continue;
            if (item.parentElement) (item.remove as () => void)();
            item.parentElement = this;
            (this.children as Array<Record<string, unknown>>).push(item);
          }
        },
        replaceChildren(...items: Array<Record<string, unknown>>) {
          for (const child of [...(this.children as Array<Record<string, unknown>>)]) child.parentElement = null;
          this.children = [];
          (this.append as (...xs: Array<Record<string, unknown>>) => void)(...items);
        },
        remove() {
          const parent = this.parentElement as { children?: Array<Record<string, unknown>> } | null;
          if (!parent?.children) return;
          const idx = parent.children.indexOf(this);
          if (idx >= 0) parent.children.splice(idx, 1);
          this.parentElement = null;
        },
        querySelector: () => null,
        querySelectorAll: () => [],
        addEventListener() {},
        removeEventListener() {},
      };
      return node;
    };
    const documentObj: Record<string, unknown> = {
      title: "",
      head: makeNode("head"),
      body: makeNode("body"),
      documentElement: { dataset: {} as Record<string, string>, style: { colorScheme: "" } },
      createElement(tag: string) {
        const el = makeNode(tag);
        el.ownerDocument = documentObj;
        return el;
      },
      createTextNode: (text: string) => String(text),
      addEventListener() {},
      removeEventListener() {},
      querySelector: () => null,
      querySelectorAll: () => [],
    };

    let currentRecord = {
      id: "plugin.hot",
      version: "1.0.0",
      updatedAt: "t1",
      enabled: true,
      apiVersion: 2,
      capabilities: ["ui:settings"],
      token: "tok-v1",
      entry: { renderer: "renderer.js", settings: "settings.js" },
    };

    let renderBehaviorByVersion: Record<string, "resolveGate" | "abortReject" | "immediate"> = {
      "1.0.0": "resolveGate",
      "1.0.1": "immediate",
      "1.0.2": "immediate",
    };
    let notifyV1Entered!: () => void;
    const v1Entered = new Promise<void>(resolve => { notifyV1Entered = resolve; });
    let releaseV1Render!: () => void;
    const v1RenderGate = new Promise<void>(resolve => { releaseV1Render = resolve; });
    let notifyV2Entered!: () => void;
    const v2Entered = new Promise<void>(resolve => { notifyV2Entered = resolve; });

    (globalThis as Record<string, unknown>)[testKey] = {
      createPluginRuntime,
      loadPluginModule(source: string, requiredExport: string) {
        const match = /VERSION=([0-9.]+)/.exec(source);
        const ver = match ? match[1] : "unknown";
        if (requiredExport === "renderSettings") {
          return Promise.resolve({
            async renderSettings(
              ctx: { plugin: { version: string }; onDispose(fn: () => void): void },
              element: { append(node: unknown): void },
            ) {
              const mode = renderBehaviorByVersion[ver] ?? "immediate";
              const card = (documentObj.createElement as (t: string) => Record<string, unknown>)("section");
              card.textContent = `v${ver}@inst-${ctx.plugin.version}`;
              element.append(card);
              if (mode === "resolveGate") {
                ctx.onDispose(() => { (card.remove as () => void)(); });
                notifyV1Entered();
                await v1RenderGate;
                return;
              }
              if (mode === "abortReject") {
                await new Promise<void>((_, reject) => {
                  ctx.onDispose(() => {
                    (card.remove as () => void)();
                    const abortErr = Object.assign(new Error("aborted during hot update"), { name: "AbortError" });
                    reject(abortErr);
                  });
                  notifyV2Entered();
                });
                return;
              }
              ctx.onDispose(() => { (card.remove as () => void)(); });
            },
          });
        }
        return Promise.resolve({
          activate() {},
        });
      },
    };

    const moduleSource = [
      `const hooks = globalThis.${testKey};`,
      "export function createEventChannel() { return { subscribe() { return () => {}; }, close() {} }; }",
      "export function createDOMContractState() { return {}; }",
      "export function evaluateDOMContracts() { return { probes: [], byId: {}, gates: {} }; }",
      "export function createEntryMountManager() { return { sync() {}, remove() {}, dispose() {} }; }",
      "export function createNativeHost(_t, _h, channel) { return { subscribe: (s, l) => channel.subscribe(s, l), close() {} }; }",
      "export function findSettingsDialogParts() { return null; }",
      "export function findOfficialSettingsPanel() { return null; }",
      "export function findOfficialSettingsSection() { return null; }",
      "export function findOfficialSettingsTab() { return null; }",
      "export function findSessionInfoKeepAliveAnchor() { return null; }",
      "export function findChatHeaderTail() { return null; }",
      "export const createPluginRuntime = (...args) => hooks.createPluginRuntime(...args);",
      "export const loadPluginModule = (...args) => hooks.loadPluginModule(...args);",
      "export function pluginEntryPath(p) { return p; }",
      "export function mountSurface() { return { unmount() {} }; }",
      "export function mountMainAreaSurface() { return { setWidth() {}, width: 0, dispose() {} }; }",
    ].join("\n");

    let blobCounter = 0;
    const fakeURL = {
      createObjectURL(blob: { __text: string }) {
        const file = join(tmpDir, `async-render-${++blobCounter}.mjs`);
        writeFileSync(file, blob.__text, "utf8");
        return pathToFileURL(file).href;
      },
      revokeObjectURL() {},
    };
    class FakeBlob {
      __text: string;
      constructor(parts: unknown[]) { this.__text = parts.map(String).join(""); }
    }
    class FakeMutationObserver {
      observe() {}
      disconnect() {}
    }

    const windowObj: Record<string, unknown> = {
      addEventListener() {},
      removeEventListener() {},
      dispatchEvent() { return true; },
      requestAnimationFrame: (cb: () => void) => setTimeout(cb, 0),
      cancelAnimationFrame: (id: ReturnType<typeof setTimeout>) => clearTimeout(id),
      location: { href: "app://renderer/index.html" },
      localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
      fetch: async (_url: string, init: { body: string }) => {
        const req = JSON.parse(init.body) as { id: string; action: string; payload?: { path?: string } };
        let result: unknown = {};
        if (req.action === "features.get") result = { multiSession: false };
        else if (req.action === "core.fetch") {
          const reqPath = req.payload?.path ?? "";
          let status = 200;
          let payload: unknown = {};
          if (reqPath.endsWith("/plugins")) {
            payload = { items: [{ ...currentRecord, capabilities: [...currentRecord.capabilities] }] };
          } else if (reqPath.includes("/assets/")) {
            const parsed = new URL(reqPath, "http://core.local");
            const q = parsed.searchParams;
            const mismatch = (q.has("version") && q.get("version") !== currentRecord.version)
              || (q.has("updatedAt") && q.get("updatedAt") !== currentRecord.updatedAt)
              || (q.has("capabilities") && q.get("capabilities") !== currentRecord.capabilities.join(","));
            if (mismatch) {
              status = 409;
              payload = { error: { code: "PLUGIN_REVISION_MISMATCH", message: "插件版本已变更，请重新同步" } };
            } else {
              payload = {
                source: `// VERSION=${currentRecord.version}\nexport function activate() {} export function renderSettings() {}`,
                version: currentRecord.version,
                updatedAt: currentRecord.updatedAt,
                capabilities: [...currentRecord.capabilities],
              };
            }
          }
          result = { status, bodyBase64: Buffer.from(JSON.stringify(payload)).toString("base64") };
        }
        return { ok: true, status: 200, json: async () => ({ protocol: 1, id: req.id, result }) };
      },
    };
    (globalThis as { window?: unknown }).window = windowObj;

    const install = new Function(
      "window", "document", "MutationObserver", "URL", "Blob", "CustomEvent", "createProviderEnhance",
      `${renderer}\nreturn installNativeRenderer;`,
    )(
      windowObj,
      documentObj,
      FakeMutationObserver,
      fakeURL,
      FakeBlob,
      class CustomEvent {
        type: string;
        detail: unknown;
        constructor(type: string, init?: { detail?: unknown }) {
          this.type = type;
          this.detail = init?.detail;
        }
      },
      () => ({ install() {}, remove() {}, clearDecorations() {} }),
    ) as (options: Record<string, unknown>) => Promise<{ status: string }>;

    const currentUI = () => windowObj.__ekdNativeUI as { remove(): Promise<void> } | undefined;
    const currentPlugins = () => windowObj.__ekdPlugins as {
      sync(): Promise<void>;
      mountSettings(
        id: string,
        element: unknown,
        options?: { signal?: AbortSignal; version?: string; updatedAt?: string; capabilities?: string[] },
      ): Promise<() => void>;
    };
    const readPanelText = (el: Record<string, unknown>) => {
      const children = el.children as Array<{ children?: Array<{ textContent?: string }> }>;
      return children.map(c => c.children?.map(n => n.textContent ?? "").join("") ?? "").join("|");
    };

    await install({
      endpoint: "http://bridge",
      bridgeKey: "bridge-key",
      surface: "host",
      coreURL: "http://127.0.0.1:1001",
      moduleSource,
      cssSource: "",
    });
    await currentPlugins().sync();

    // Path A: async renderSettings resolves after hot update 1.0.0 -> 1.0.1.
    const containerA = (documentObj.createElement as (t: string) => Record<string, unknown>)("div");
    const pendingMountA = currentPlugins().mountSettings("plugin.hot", containerA, {
      signal: new AbortController().signal,
      version: "1.0.0",
      updatedAt: "t1",
      capabilities: ["ui:settings"],
    });
    const pendingMountARejected = assert.rejects(
      pendingMountA,
      (err: unknown) => (err as { code?: string })?.code === "PLUGIN_REVISION_MISMATCH",
    );
    await v1Entered;

    currentRecord = { ...currentRecord, version: "1.0.1", updatedAt: "t2", token: "tok-v2" };
    await currentPlugins().sync();
    await pendingMountARejected;
    releaseV1Render();
    await new Promise(resolve => setTimeout(resolve, 0));
    assert.equal(readPanelText(containerA), "", "old container must be cleared when async renderSettings finishes after hot update");

    // Panel reloads plugin record to 1.0.1 and remounts cleanly.
    const releaseV2 = await currentPlugins().mountSettings("plugin.hot", containerA, {
      signal: new AbortController().signal,
      version: "1.0.1",
      updatedAt: "t2",
      capabilities: ["ui:settings"],
    });
    assert.equal(readPanelText(containerA), "v1.0.1@inst-1.0.1");
    releaseV2();

    // Path B: async renderSettings rejects with AbortError on dispose/abort during hot update 1.0.1 -> 1.0.2.
    renderBehaviorByVersion["1.0.1"] = "abortReject";
    const containerB = (documentObj.createElement as (t: string) => Record<string, unknown>)("div");
    const pendingMountB = currentPlugins().mountSettings("plugin.hot", containerB, {
      signal: new AbortController().signal,
      version: "1.0.1",
      updatedAt: "t2",
      capabilities: ["ui:settings"],
    });
    const pendingMountBRejected = assert.rejects(
      pendingMountB,
      (err: unknown) => (err as { code?: string })?.code === "PLUGIN_REVISION_MISMATCH",
    );
    await v2Entered;

    currentRecord = { ...currentRecord, version: "1.0.2", updatedAt: "t3", token: "tok-v3" };
    await currentPlugins().sync();
    await pendingMountBRejected;
    assert.equal(readPanelText(containerB), "", "old container must be cleared on abort-reject path");

    // Panel reloads plugin record to 1.0.2 and remounts cleanly.
    const releaseV3 = await currentPlugins().mountSettings("plugin.hot", containerB, {
      signal: new AbortController().signal,
      version: "1.0.2",
      updatedAt: "t3",
      capabilities: ["ui:settings"],
    });
    assert.equal(readPanelText(containerB), "v1.0.2@inst-1.0.2");
    releaseV3();

    await currentUI()!.remove();
  } finally {
    if (prevWindow === undefined) delete (globalThis as { window?: unknown }).window;
    else (globalThis as { window?: unknown }).window = prevWindow;
    delete (globalThis as Record<string, unknown>)[testKey];
    rmSync(tmpDir, { recursive: true, force: true });
  }
});

test("renderer.js races hung renderSettings against cancellation, disposes late releases, and recovers via bridge.subscribe after intermediate update disable", async () => {
  const renderer = readFileSync("../internal/desktop/assets/renderer.js", "utf8");
  const tmpDir = mkdtempSync(join(tmpdir(), "ekd-round9-recovery-"));
  const testKey = `__ekdRound9Recovery_${Date.now()}_${Math.random().toString(36).slice(2)}`;
  const prevWindow = (globalThis as { window?: unknown }).window;

  try {
    const makeNode = (tag: string) => {
      const classSet = new Set<string>();
      const node: Record<string, unknown> = {
        tagName: tag.toUpperCase(),
        dataset: {} as Record<string, string>,
        style: { cssText: "", setProperty() {}, removeProperty() {} },
        children: [] as Array<Record<string, unknown>>,
        parentElement: null as Record<string, unknown> | null,
        attributes: new Map<string, string>(),
        className: "",
        textContent: "",
        innerHTML: "",
        hidden: false,
        classList: {
          add(...items: string[]) { items.forEach(i => classSet.add(i)); },
          remove(...items: string[]) { items.forEach(i => classSet.delete(i)); },
          contains(item: string) { return classSet.has(item); },
        },
        setAttribute(name: string, value: string) { (this.attributes as Map<string, string>).set(name, String(value)); },
        getAttribute(name: string) { return (this.attributes as Map<string, string>).get(name) ?? null; },
        removeAttribute(name: string) { (this.attributes as Map<string, string>).delete(name); },
        append(...items: Array<Record<string, unknown>>) {
          for (const item of items) {
            if (!item || typeof item !== "object") continue;
            if (item.parentElement) (item.remove as () => void)();
            item.parentElement = this;
            (this.children as Array<Record<string, unknown>>).push(item);
          }
        },
        replaceChildren(...items: Array<Record<string, unknown>>) {
          for (const child of [...(this.children as Array<Record<string, unknown>>)]) child.parentElement = null;
          this.children = [];
          (this.append as (...xs: Array<Record<string, unknown>>) => void)(...items);
        },
        remove() {
          const parent = this.parentElement as { children?: Array<Record<string, unknown>> } | null;
          if (!parent?.children) return;
          const idx = parent.children.indexOf(this);
          if (idx >= 0) parent.children.splice(idx, 1);
          this.parentElement = null;
        },
        querySelector: () => null,
        querySelectorAll: () => [],
        addEventListener() {},
        removeEventListener() {},
      };
      return node;
    };
    const documentObj: Record<string, unknown> = {
      title: "",
      head: makeNode("head"),
      body: makeNode("body"),
      documentElement: { dataset: {} as Record<string, string>, style: { colorScheme: "" } },
      createElement(tag: string) {
        const el = makeNode(tag);
        el.ownerDocument = documentObj;
        return el;
      },
      createTextNode: (text: string) => String(text),
      addEventListener() {},
      removeEventListener() {},
      querySelector: () => null,
      querySelectorAll: () => [],
    };

    let currentRecord = {
      id: "plugin.hot",
      version: "1.0.0",
      updatedAt: "t1",
      enabled: true,
      apiVersion: 2,
      capabilities: ["ui:settings"],
      token: "tok-v1",
      entry: { renderer: "renderer.js", settings: "settings.js" },
    };

    let renderMode: "neverSettle" | "lateResolve" | "holdForDisable" | "immediate" = "neverSettle";
    let notifyEntered!: () => void;
    let entered = new Promise<void>(resolve => { notifyEntered = resolve; });
    const resetEntered = () => { entered = new Promise<void>(resolve => { notifyEntered = resolve; }); };
    let releaseLateResolve!: () => void;
    const lateResolveGate = new Promise<void>(resolve => { releaseLateResolve = resolve; });
    let lateDisposeCount = 0;

    (globalThis as Record<string, unknown>)[testKey] = {
      createPluginRuntime,
      loadPluginModule(source: string, requiredExport: string) {
        const match = /VERSION=([0-9.]+)/.exec(source);
        const ver = match ? match[1] : "unknown";
        if (requiredExport === "renderSettings") {
          return Promise.resolve({
            async renderSettings(
              ctx: { plugin: { version: string }; onDispose(fn: () => void): void },
              element: { append(node: unknown): void },
            ) {
              const card = (documentObj.createElement as (t: string) => Record<string, unknown>)("section");
              card.textContent = `v${ver}@inst-${ctx.plugin.version}`;
              element.append(card);
              ctx.onDispose(() => { (card.remove as () => void)(); });
              if (renderMode === "neverSettle") {
                notifyEntered();
                return await new Promise<never>(() => {});
              }
              if (renderMode === "lateResolve") {
                notifyEntered();
                await lateResolveGate;
                return { dispose() { lateDisposeCount += 1; } };
              }
              if (renderMode === "holdForDisable") {
                notifyEntered();
                return await new Promise<never>(() => {});
              }
              return undefined;
            },
          });
        }
        return Promise.resolve({ activate() {} });
      },
    };

    const moduleSource = [
      `const hooks = globalThis.${testKey};`,
      "export function createEventChannel() { return { subscribe() { return () => {}; }, close() {} }; }",
      "export function createDOMContractState() { return {}; }",
      "export function evaluateDOMContracts() { return { probes: [], byId: {}, gates: {} }; }",
      "export function createEntryMountManager() { return { sync() {}, remove() {}, dispose() {} }; }",
      "export function createNativeHost(_t, _h, channel) { return { subscribe: (s, l) => channel.subscribe(s, l), close() {} }; }",
      "export function findSettingsDialogParts() { return null; }",
      "export function findOfficialSettingsPanel() { return null; }",
      "export function findOfficialSettingsSection() { return null; }",
      "export function findOfficialSettingsTab() { return null; }",
      "export function findSessionInfoKeepAliveAnchor() { return null; }",
      "export function findChatHeaderTail() { return null; }",
      "export const createPluginRuntime = (...args) => hooks.createPluginRuntime(...args);",
      "export const loadPluginModule = (...args) => hooks.loadPluginModule(...args);",
      "export function pluginEntryPath(p) { return p; }",
      "export function mountSurface() { return { unmount() {} }; }",
      "export function mountMainAreaSurface() { return { setWidth() {}, width: 0, dispose() {} }; }",
    ].join("\n");

    let blobCounter = 0;
    const fakeURL = {
      createObjectURL(blob: { __text: string }) {
        const file = join(tmpDir, `round9-${++blobCounter}.mjs`);
        writeFileSync(file, blob.__text, "utf8");
        return pathToFileURL(file).href;
      },
      revokeObjectURL() {},
    };
    class FakeBlob {
      __text: string;
      constructor(parts: unknown[]) { this.__text = parts.map(String).join(""); }
    }
    class FakeMutationObserver {
      observe() {}
      disconnect() {}
    }

    const windowObj: Record<string, unknown> = {
      addEventListener() {},
      removeEventListener() {},
      dispatchEvent() { return true; },
      requestAnimationFrame: (cb: () => void) => setTimeout(cb, 0),
      cancelAnimationFrame: (id: ReturnType<typeof setTimeout>) => clearTimeout(id),
      location: { href: "app://renderer/index.html" },
      localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
      fetch: async (_url: string, init: { body: string }) => {
        const req = JSON.parse(init.body) as { id: string; action: string; payload?: { path?: string } };
        let result: unknown = {};
        if (req.action === "features.get") result = { multiSession: false };
        else if (req.action === "core.fetch") {
          const reqPath = req.payload?.path ?? "";
          let status = 200;
          let payload: unknown = {};
          if (reqPath.endsWith("/plugins")) {
            payload = { items: [{ ...currentRecord, capabilities: [...currentRecord.capabilities] }] };
          } else if (reqPath.includes("/assets/")) {
            const parsed = new URL(reqPath, "http://core.local");
            const q = parsed.searchParams;
            const mismatch = (q.has("version") && q.get("version") !== currentRecord.version)
              || (q.has("updatedAt") && q.get("updatedAt") !== currentRecord.updatedAt)
              || (q.has("capabilities") && q.get("capabilities") !== currentRecord.capabilities.join(","));
            if (mismatch) {
              status = 409;
              payload = { error: { code: "PLUGIN_REVISION_MISMATCH", message: "插件版本已变更，请重新同步" } };
            } else {
              payload = {
                source: `// VERSION=${currentRecord.version}\nexport function activate() {} export function renderSettings() {}`,
                version: currentRecord.version,
                updatedAt: currentRecord.updatedAt,
                capabilities: [...currentRecord.capabilities],
              };
            }
          }
          result = { status, bodyBase64: Buffer.from(JSON.stringify(payload)).toString("base64") };
        }
        return { ok: true, status: 200, json: async () => ({ protocol: 1, id: req.id, result }) };
      },
    };
    (globalThis as { window?: unknown }).window = windowObj;

    const install = new Function(
      "window", "document", "MutationObserver", "URL", "Blob", "CustomEvent", "createProviderEnhance",
      `${renderer}\nreturn installNativeRenderer;`,
    )(
      windowObj,
      documentObj,
      FakeMutationObserver,
      fakeURL,
      FakeBlob,
      class CustomEvent {
        type: string;
        detail: unknown;
        constructor(type: string, init?: { detail?: unknown }) {
          this.type = type;
          this.detail = init?.detail;
        }
      },
      () => ({ install() {}, remove() {}, clearDecorations() {} }),
    ) as (options: Record<string, unknown>) => Promise<{ status: string }>;

    const currentUI = () => windowObj.__ekdNativeUI as { remove(): Promise<void> } | undefined;
    const currentPlugins = () => windowObj.__ekdPlugins as {
      sync(): Promise<void>;
      list(): Array<{ id: string; enabled: boolean; version?: string; updatedAt?: string; capabilities?: string[] }>;
      subscribe(listener: () => void): () => void;
      mountSettings(
        id: string,
        element: unknown,
        options?: { signal?: AbortSignal; version?: string; updatedAt?: string; capabilities?: string[] },
      ): Promise<() => void>;
    };
    const readPanelText = (el: Record<string, unknown>) => {
      const children = el.children as Array<{ children?: Array<{ textContent?: string }> }>;
      return children.map(c => c.children?.map(n => n.textContent ?? "").join("") ?? "").join("|");
    };

    await install({
      endpoint: "http://bridge",
      bridgeKey: "bridge-key",
      surface: "host",
      coreURL: "http://127.0.0.1:1001",
      moduleSource,
      cssSource: "",
    });
    await currentPlugins().sync();

    // 1a. Never-settling renderSettings across hot update 1.0.0 -> 1.0.1 must reject immediately with PLUGIN_REVISION_MISMATCH.
    const container1 = (documentObj.createElement as (t: string) => Record<string, unknown>)("div");
    const hungMount = currentPlugins().mountSettings("plugin.hot", container1, {
      signal: new AbortController().signal,
      version: "1.0.0",
      updatedAt: "t1",
      capabilities: ["ui:settings"],
    });
    const hungRejected = assert.rejects(
      hungMount,
      (err: unknown) => (err as { code?: string })?.code === "PLUGIN_REVISION_MISMATCH",
    );
    await entered;

    currentRecord = { ...currentRecord, version: "1.0.1", updatedAt: "t2", token: "tok-v2" };
    await currentPlugins().sync();
    await Promise.race([
      hungRejected,
      new Promise<never>((_, reject) => setTimeout(() => reject(new Error("hung mountSettings did not reject immediately on hot update")), 500)),
    ]);
    assert.equal(readPanelText(container1), "", "old DOM must be cleared even when renderSettings never settles");

    renderMode = "immediate";
    const releaseV2 = await currentPlugins().mountSettings("plugin.hot", container1, {
      signal: new AbortController().signal,
      version: "1.0.1",
      updatedAt: "t2",
      capabilities: ["ui:settings"],
    });
    assert.equal(readPanelText(container1), "v1.0.1@inst-1.0.1");
    releaseV2();

    // 1b. Late-resolving renderSettings after cancellation must have its returned disposable disposed immediately.
    resetEntered();
    renderMode = "lateResolve";
    const containerLate = (documentObj.createElement as (t: string) => Record<string, unknown>)("div");
    const lateMount = currentPlugins().mountSettings("plugin.hot", containerLate, {
      signal: new AbortController().signal,
      version: "1.0.1",
      updatedAt: "t2",
      capabilities: ["ui:settings"],
    });
    const lateRejected = assert.rejects(
      lateMount,
      (err: unknown) => (err as { code?: string })?.code === "PLUGIN_REVISION_MISMATCH",
    );
    await entered;

    currentRecord = { ...currentRecord, version: "1.0.2", updatedAt: "t3", token: "tok-v3" };
    await currentPlugins().sync();
    await lateRejected;
    assert.equal(lateDisposeCount, 0);
    releaseLateResolve();
    await new Promise(resolve => setTimeout(resolve, 20));
    assert.equal(lateDisposeCount, 1, "late disposable returned after mount cancellation must be disposed immediately");

    // 2. Intermediate disabled state (!enabled.length) during official update vs deliberate disable:
    resetEntered();
    renderMode = "holdForDisable";
    let panelState: "loading" | "ready" | "failed" = "loading";
    let recoveryTriggers = 0;
    let recoveredSnapshot: { version?: string; updatedAt?: string; capabilities?: string[] } | null = null;
    const unsub = currentPlugins().subscribe(() => {
      const latest = currentPlugins().list().find(item => item.id === "plugin.hot");
      if (!latest || !latest.enabled) return;
      const revisionChanged = latest.version !== "1.0.2" || latest.updatedAt !== "t3";
      if (!revisionChanged && panelState !== "failed") return;
      recoveryTriggers += 1;
      recoveredSnapshot = { version: latest.version, updatedAt: latest.updatedAt, capabilities: latest.capabilities };
    });

    const container2 = (documentObj.createElement as (t: string) => Record<string, unknown>)("div");
    const disableMount = currentPlugins().mountSettings("plugin.hot", container2, {
      signal: new AbortController().signal,
      version: "1.0.2",
      updatedAt: "t3",
      capabilities: ["ui:settings"],
    });
    const disableRejected = assert.rejects(
      disableMount,
      (err: unknown) => {
        const e = err as { code?: string; name?: string; message?: string };
        return e?.code === "PLUGIN_DISABLED"
          && /插件未运行或宿主已卸载/.test(e?.message || "")
          && e?.name !== "AbortError";
      },
    );
    await entered;

    // Step 2a: sole enabled plugin enters intermediate disabled state (!enabled.length branch).
    currentRecord = { ...currentRecord, enabled: false };
    await currentPlugins().sync();
    await disableRejected;
    panelState = "failed";
    assert.equal(readPanelText(container2), "");
    assert.equal(currentPlugins().list().find(i => i.id === "plugin.hot")?.enabled, false);
    assert.equal(recoveryTriggers, 0, "deliberate/intermediate disable must not trigger recovery while disabled");

    // Step 2b: official update finishes and re-enables 1.0.3 -> bridge.subscribe triggers recovery and mounts 1.0.3.
    renderMode = "immediate";
    currentRecord = { ...currentRecord, enabled: true, version: "1.0.3", updatedAt: "t4", token: "tok-v4" };
    await currentPlugins().sync();
    assert.ok(recoveryTriggers >= 1, "re-enabling replacement revision must notify bridge subscribers");
    assert.deepEqual(recoveredSnapshot, { version: "1.0.3", updatedAt: "t4", capabilities: ["ui:settings"] });

    const releaseV4 = await currentPlugins().mountSettings("plugin.hot", container2, {
      signal: new AbortController().signal,
      version: "1.0.3",
      updatedAt: "t4",
      capabilities: ["ui:settings"],
    });
    assert.equal(readPanelText(container2), "v1.0.3@inst-1.0.3");
    releaseV4();
    unsub();

    // Step 2c: host remove() aborts an in-flight mount with HOST_UNMOUNTED (distinct from PLUGIN_DISABLED).
    resetEntered();
    renderMode = "neverSettle";
    const containerHostRemove = (documentObj.createElement as (t: string) => Record<string, unknown>)("div");
    const hostRemoveMount = currentPlugins().mountSettings("plugin.hot", containerHostRemove, {
      signal: new AbortController().signal,
      version: "1.0.3",
      updatedAt: "t4",
      capabilities: ["ui:settings"],
    });
    const hostRemoveRejected = assert.rejects(
      hostRemoveMount,
      (err: unknown) => {
        const e = err as { code?: string; message?: string };
        return e?.code === "HOST_UNMOUNTED"
          && /插件未运行或宿主已卸载/.test(e?.message || "");
      },
    );
    await entered;
    await currentUI()!.remove();
    await hostRemoveRejected;
  } finally {
    if (prevWindow === undefined) delete (globalThis as { window?: unknown }).window;
    else (globalThis as { window?: unknown }).window = prevWindow;
    delete (globalThis as Record<string, unknown>)[testKey];
    rmSync(tmpDir, { recursive: true, force: true });
  }
});

test("createDeferredPluginRefreshController queues external plugin notifications while busy and replays them when a failed local operation settles", () => {
  const { createDeferredPluginRefreshController } = extract("src/components/PluginSettings.tsx", [
    "createDeferredPluginRefreshController",
  ]) as {
    createDeferredPluginRefreshController(deps: {
      isBusy: () => boolean;
      reloadPlugins: () => unknown;
      reloadUpdates: () => unknown;
    }): {
      notify(withUpdates: boolean): void;
      flushIfIdle(): void;
      hasPending(): boolean;
    };
  };

  let busy = true;
  let reloadPluginsCalls = 0;
  let reloadUpdatesCalls = 0;
  const ctrl = createDeferredPluginRefreshController({
    isBusy: () => busy,
    reloadPlugins: () => { reloadPluginsCalls += 1; },
    reloadUpdates: () => { reloadUpdatesCalls += 1; },
  });

  // 1. While another plugin's local update is busy, bridge and host notifications are queued rather than dropped.
  ctrl.notify(false);
  ctrl.notify(true);
  assert.equal(reloadPluginsCalls, 0);
  assert.equal(reloadUpdatesCalls, 0);
  assert.equal(ctrl.hasPending(), true);
  ctrl.flushIfIdle();
  assert.equal(reloadPluginsCalls, 0, "must not flush while still busy");

  // 2. Local update fails and clears busy without manually reloading -> flushIfIdle replays both reloads once.
  busy = false;
  ctrl.flushIfIdle();
  assert.equal(reloadPluginsCalls, 1);
  assert.equal(reloadUpdatesCalls, 1);
  assert.equal(ctrl.hasPending(), false);

  // 3. Subsequent idle flush without pending events does not re-fetch.
  ctrl.flushIfIdle();
  assert.equal(reloadPluginsCalls, 1);
  assert.equal(reloadUpdatesCalls, 1);

  // 4. Bridge-only notification queued while busy replays only reloadPlugins on settle.
  busy = true;
  ctrl.notify(false);
  busy = false;
  ctrl.flushIfIdle();
  assert.equal(reloadPluginsCalls, 2);
  assert.equal(reloadUpdatesCalls, 1);

  // 5. Idle notification fires immediately without queuing.
  ctrl.notify(true);
  assert.equal(reloadPluginsCalls, 3);
  assert.equal(reloadUpdatesCalls, 2);
  assert.equal(ctrl.hasPending(), false);

  // 6. Verify PluginSettings.tsx wires subscriptions and idle flush to the controller.
  const pluginSettingsSource = readFileSync("src/components/PluginSettings.tsx", "utf8");
  assert.ok(pluginSettingsSource.includes('host?.subscribe("plugins", () => refreshCtrl.notify(true))'));
  assert.ok(pluginSettingsSource.includes("pluginHostBridge()?.subscribe?.(() => refreshCtrl.notify(false))"));
  assert.ok(pluginSettingsSource.includes("refreshCtrlRef.current?.flushIfIdle()"));
  assert.ok(pluginSettingsSource.includes("[localOpBusy, quickBusyIds.length]"));
});
