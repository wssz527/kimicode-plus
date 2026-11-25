import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import ts from "typescript";
import { createEventChannel } from "../src/native.js";

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
    providerTTL: { subscriptionMinutes: number; openPlatformMinutes: number; otherMinutes: number };
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
    providerTTL: { subscriptionMinutes: 30, openPlatformMinutes: 5, otherMinutes: 0 },
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
    providerTTL: { subscriptionMinutes: 30, openPlatformMinutes: 5, otherMinutes: 0 },
  },
  compaction: { enabled: false, idleMinutes: 25, contextThreshold: 60 },
  recovery: { enabled: false, intervalSeconds: 60, entries: [] },
})) as Policy;

test("a policy patch carries only the fields the edit actually changed", () => {
  const base = basePolicy();
  const minutes = { ...base, keepAlive: { ...base.keepAlive, minutes: 40 } };
  assert.deepEqual(policyHelpers.policyPatch(base, minutes), { keepAlive: { minutes: 40 } });

  const other = basePolicy();
  other.idleKeepAlive.providerTTL.otherMinutes = 12;
  assert.equal(policyHelpers.policyPatch(base, other), null);

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
