import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { createEventChannel } from "../src/native.js";

const source = (path: string) => readFileSync(path, "utf8");

const sessionPanes = source("src/components/workbench/SessionPanes.tsx");
const pieces = source("src/components/workbench/pieces.tsx");
const sessionGrid = source("src/components/SessionGrid.tsx");
const api = source("src/api.ts");
const native = source("src/native.ts");
const renderer = source("../internal/desktop/assets/renderer.js");
const bootstrap = source("../internal/desktop/assets/bootstrap.js");
const providerEnhance = source("../internal/desktop/assets/provider_enhance.js");
const nativeSettings = source("src/components/NativeSettings.tsx");
const cacheSettings = source("src/components/CacheSettings.tsx");
const settingsSurface = source("src/components/SettingsSurface.tsx");
const windowsAttach = source("src/components/WindowsAttachStatus.tsx");
const accounts = source("src/components/AccountsSection.tsx");
const pairing = source("src/PairingScreen.tsx");
const useResource = source("src/lib/useResource.ts");

test("an approval fallback never turns an unknown value into an approval", () => {
  // The pane used to send { decision: "approved" } for anything it did not
  // recognize, so a `deny` fallback button approved the tool call.
  assert.match(sessionPanes, /label: "允许", value: "approved"/);
  assert.match(sessionPanes, /label: "拒绝", value: "rejected"/);
  assert.match(sessionPanes, /allow: "approved"/);
  assert.match(sessionPanes, /deny: "rejected"/);
  assert.match(sessionPanes, /option\.value === "rejected" \|\| option\.value === "deny"/);
  assert.equal(/\[\s*\{ label: "允许", value: "allow" \}/.test(sessionPanes), false);

  // A server-supplied custom label keeps its copy, but only when the option came
  // from `approval.options`; an unrecognized fallback sends nothing at all.
  assert.match(sessionPanes, /fromServer \? \{ decision: "approved", selectedLabel: value \} : null/);
  assert.match(sessionPanes, /if \(!payload\) \{/);
  assert.match(sessionPanes, /const fromServer = Boolean\(approval\.options\?\.length\)/);
});

test("pane keys are stable and markdown links are protocol-checked", () => {
  assert.match(sessionGrid, /key=\{id \|\| `pane-\$\{index\}`\}/);
  assert.equal(/key=\{index\}/.test(sessionGrid), false);
  for (const protocol of ['"http:"', '"https:"', '"mailto:"']) {
    assert.ok(pieces.includes(protocol), `missing allowed protocol ${protocol}`);
  }
  assert.match(pieces, /SAFE_LINK_PROTOCOLS\.has\(protocol\) \? href : null/);
  assert.match(pieces, /wb-link-blocked/);
  // A copy confirmation is a short flash, not a permanent label.
  assert.match(pieces, /window\.setTimeout\(\(\) => \{ timer\.current = null; setStatus\(""\); \}, 2400\)/);
  assert.match(pieces, /if \(timer\.current !== null\) window\.clearTimeout\(timer\.current\)/);
});

test("a host bridge 401 keeps the transport and its error code", () => {
  // In bridge mode the transport belongs to the official host: clearing it would
  // drop every later request the surface still needs.
  assert.match(api, /if \(coreTransport === null\) \{\s*clearSession\(/);
  assert.match(api, /const candidate = error as \{ code\?: unknown; status\?: unknown; message\?: unknown \}/);
  assert.match(api, /throw new ApiError\(status, candidate\.code, message\)/);
});

test("the event channel backs off to a ceiling and stops its timer on close", () => {
  assert.match(native, /const retryCeilingMs = 10_000/);
  assert.match(native, /retryMs \* 2 \*\* Math\.min\(failures, 4\)/);
  assert.match(native, /clearTimeout\(wakeTimer\)/);
  assert.match(native, /wakeTimer = setTimeout\(release, delay\)/);
  assert.match(native, /signal\.addEventListener\("abort", onAbort, \{ once: true \}\)/);
});

test("provider enhance pauses its observer, caches config and writes the matched key", () => {
  assert.match(providerEnhance, /function setEnabled\(next\)/);
  assert.match(providerEnhance, /setEnabled,/);
  assert.match(providerEnhance, /const wasObserving = enabled && Boolean\(state\.observer\)/);
  assert.match(providerEnhance, /const CONFIG_TTL_MS = 2000/);
  assert.match(providerEnhance, /invalidateConfig\(\)/);
  assert.match(providerEnhance, /key = found\[0\]/);
  assert.match(providerEnhance, /new CustomEvent\('ekd:config-saved'/);
  // Explicit off / toggle / always plus the adaptive switch the old drawer lacked.
  assert.match(providerEnhance, /function setThinkingMode\(draft, mode\)/);
  assert.match(providerEnhance, /label: '始终思考'/);
  assert.match(providerEnhance, /'data-mode': mode\.value/);
  assert.match(providerEnhance, /text: '自适应思考（adaptive_thinking）'/);
  assert.match(renderer, /state\.providerEnhance\?\.setEnabled\?\.\(false\)/);
});

test("plugin hot updates drop assets and keep the token that opened each view", () => {
  // A revision change tears the plugin down and clears every cached asset entry,
  // so a repaired or upgraded bundle is always fetched again.
  assert.match(renderer, /if \(next && state\.pluginRevisions\.get\(id\) === pluginRevision\(next\)\) continue/);
  assert.match(renderer, /if \(key\.startsWith\(id \+ '\/'\)\) state\.pluginAssets\.delete\(key\)/);
  assert.match(renderer, /if \(key\.startsWith\(plugin\.id \+ '\/'\)\) state\.pluginAssets\.delete\(key\)/);
  assert.match(renderer, /state\.pluginRevisions\.set\(plugin\.id, pluginRevision\(plugin\)\)/);
  // The token a view was created with stays resolvable after the plugin loses it,
  // otherwise close cannot authorize and the WebContentsView is orphaned.
  assert.match(renderer, /return state\.pluginTokens\.get\(id\) \|\| state\.pluginRetiredTokens\?\.get\(id\) \|\| ''/);
  assert.match(renderer, /state\.pluginTokens\.delete\(id\);\s*\n\s*if \(retiredToken\) \(state\.pluginRetiredTokens \?\?= new Map\(\)\)\.set\(id, retiredToken\)/);
  assert.match(renderer, /"browser\.view\.close", \{ token: pluginToken\(pluginId\), viewId \}/);
});

test("official auth refreshes once and account switches reach the sidebar", () => {
  assert.match(renderer, /if \(state\.officialAuthRefresh\) return state\.officialAuthRefresh/);
  assert.match(renderer, /officialAuthRefresh = doRefreshOfficialAuth\(\)\.finally/);
  assert.match(renderer, /window\.dispatchEvent\(new CustomEvent\('ekd:account-switched', \{ detail: event \}\)\)/);
  assert.match(accounts, /dispatchAccountSwitched\(event\)/);
  assert.match(accounts, /setError\(null\);\s*\n\s*setNotice\("已完成账号切换。"\)/);
});

test("renderer observers coalesce, update popover patches in place, and links fall back", () => {
  assert.match(renderer, /state\.ensureFrame = typeof requestAnimationFrame === 'function'/);
  assert.match(renderer, /token !== 'scrolling' && token !== 'dragging' && token !== 'is-dragging'/);
  assert.match(renderer, /previous\.popover === popover && previous\.shape === shape/);
  assert.match(renderer, /if \(fill\) fill\.style\.width = `\$\{percent\}%`/);
  assert.match(renderer, /window\.localStorage\.setItem\(UPDATE_DISMISS_KEY, JSON\.stringify\(\[\.\.\.versions\]\)\)/);
  assert.match(renderer, /window\.open\(safe, '_blank', 'noopener,noreferrer'\)/);
  assert.match(renderer, /window\.open\(url, '_blank', 'noopener,noreferrer'\)/);
  assert.match(renderer, /const listed = Array\.isArray\(result\?\.items\) \? result\.items : \[\]/);
  assert.match(renderer, /watchKeepAliveMenuSize/);
  assert.match(renderer, /new ResizeObserver/);
  assert.match(bootstrap, /const attempts = 4/);
  assert.match(renderer, /wrapped\.code = 'BRIDGE_SESSION_EXPIRED'/);
});

test("settings drafts survive background refreshes and invalid numbers explain themselves", () => {
  assert.match(nativeSettings, /if \(dirtyRef\.current\.pool \|\| dirtyRef\.current\.rules\) return/);
  assert.match(nativeSettings, /if \(key === "pool"\) setPoolDirty\(false\)/);
  assert.match(nativeSettings, /if \(key === "permission"\) setRulesDirty\(false\)/);
  assert.match(nativeSettings, /不能留空，填写/);
  assert.match(nativeSettings, /else if \(parsed === null\) setText\(value\)/);

  assert.match(cacheSettings, /Promise\.allSettled\(\[/);
  assert.match(cacheSettings, /setPolicyReady\(true\)/);
  assert.match(cacheSettings, /disabled=\{!policyReady\}/);
  assert.match(cacheSettings, /if \(writing\.current\) return/);
  assert.match(cacheSettings, /if \(parsed !== value\) onCommit\(parsed\)/);
  assert.match(cacheSettings, /只接受 0（未知，不自动保活）或 2–240 分钟/);

  assert.match(useResource, /const reload = useCallback\(\(\) => setRevision/);
});

test("settings entries link straight to their official tabs and the panel polls a reopen", () => {
  assert.match(settingsSurface, /void host\.openSettings\(entry\.tab\)/);
  assert.match(settingsSurface, /if \(coreUpdateBusy\(next\.state\)\) setTracking\(true\)/);
  assert.match(windowsAttach, /if \(!reopening\) return/);
  assert.match(windowsAttach, /const copy = attachCopy\[state\] \?\?/);
  assert.match(pairing, /<strong>KimiCode\+<\/strong>/);
  assert.match(pairing, /pairing-steps/);
  assert.match(pairing, /连接本地增强核心/);
});

test("retry delay grows with consecutive failures", async () => {
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

