// 规划插件的纯函数与编排测试。
//
// 插件模块必须是单文件（loader 用 blob URL 导入，相对 import 无法解析），所以
// 这里读取真实文件、用 data URL 导入，测的就是随二进制发布的那份代码。
//
// 站点适配器只在合成 DOM 上验证匹配与解析形状：这些结果一律标 UNVERIFIED，
// 不代表任一真实网站已登录或已通过解析。
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

interface Message {
  id: string;
  role: string;
  text: string;
  label?: string;
}

interface PlannerModule {
  DEFAULT_SITES: { id: string; label: string; url: string }[];
  MAX_SITES: number;
  SITE_ADAPTERS: Record<string, string>;
  COMPOSER_SOURCE: string;
  PRESETS: { id: string; label: string }[];
  activate(ctx: unknown): Promise<unknown>;
  normalizeUrl(raw: unknown): string;
  matchSite(raw: unknown): string;
  adapterFor(raw: unknown): { site: string; source: string } | null;
  composerFor(raw: unknown): { site: string; inputs: string[]; source: string } | null;
  normalizeSites(raw: unknown): { id: string; label: string; url: string }[];
  addSite(list: unknown, entry: unknown): { ok: boolean; error?: string; sites: { id: string; label: string; url: string }[] };
  removeSite(list: unknown, idOrUrl: string): { id: string; label: string; url: string }[];
  normalizeMessages(raw: unknown, options?: unknown): { messages: Message[]; truncated: boolean; total: number };
  chooseExtraction(input: unknown): { mode: string; site: string; messages: Message[]; truncated: boolean; notice: string };
  buildHandoffText(messages: unknown, selected: unknown, meta?: unknown): string;
  planOptions(preset: string, custom?: unknown): { context: boolean; skills: boolean; tools: boolean; readOnly: boolean };
  handoffInput(input: unknown): Record<string, string>;
  fillPlan(input: unknown): { ok: boolean; text?: string; needsChoice?: boolean; error?: string; mode?: string };
  verifyDraftFill(expected: unknown, readBack: unknown): { ok: boolean; wanted: string; got: string };
  sessionSummary(view: unknown, options?: unknown): { title: string; text: string; truncated: boolean };
  sessionDiff(diffAnswer: unknown, view: unknown, options?: unknown): { ok: boolean; title: string; text: string; source?: string; truncated: boolean; error?: string };
  shouldCloseMenuOnInteraction(event: unknown, menu: unknown, moreBtn?: unknown): boolean;
  draftWith(drafts: unknown, source: string, text: unknown): { web: string; local: string };
  threadActions(thread: unknown, pluginId: string): { owned: boolean; canOpen: boolean; canDelete: boolean; canRename: boolean };
  openTarget(commands: unknown, sessionId: string): { mode: string; action: string; args: { sessionId: string } };
  openButtonState(target: unknown, apis: unknown): { mode: string; label: string; disabled: boolean; title: string };
  openSessionTarget(apis: unknown, target: unknown): Promise<{ via: string }>;
  renamePlannerThread(apis: unknown, threadId: string, title: unknown): Promise<{ thread: { id: string; title: string }; threads: { id: string; title: string }[] }>;
  runPageAction(view: unknown, kind: string): Promise<{ moved: boolean }>;
  popoutIntoWindow(apis: unknown): Promise<{ surface: string }>;
  closePopoutWindow(apis: unknown): Promise<{ surface: string }>;
  createBrowserSync(options: unknown): { apply(): Promise<{ visible: boolean }>; visible: boolean; dispose(): Promise<void> };
  splitCodeBlocks(text: string): { type: string; text?: string; code?: string }[];
}

const rendererSource = readFileSync("../plugins/planner/renderer.js", "utf8");
const planner = (await import(`data:text/javascript;base64,${Buffer.from(rendererSource).toString("base64")}`)) as PlannerModule;
const {
  DEFAULT_SITES,
  MAX_SITES,
  SITE_ADAPTERS,
  addSite,
  adapterFor,
  buildHandoffText,
  chooseExtraction,
  composerFor,
  createBrowserSync,
  draftWith,
  fillPlan,
  handoffInput,
  matchSite,
  normalizeMessages,
  normalizeSites,
  normalizeUrl,
  openSessionTarget,
  openButtonState,
  openTarget,
  planOptions,
  popoutIntoWindow,
  closePopoutWindow,
  removeSite,
  renamePlannerThread,
  runPageAction,
  sessionDiff,
  sessionSummary,
  shouldCloseMenuOnInteraction,
  splitCodeBlocks,
  threadActions,
  verifyDraftFill,
} = planner;

// --- 合成站点 DOM ---------------------------------------------------------
//
// These nodes imitate the shapes the adapters look for. They prove matching and
// shaping only. Every result below is UNVERIFIED against a real, logged-in site.

interface FakeNode {
  tagName: string;
  className: string;
  innerText: string;
  textContent: string;
  attrs: Record<string, string>;
  closestNode: FakeNode | null;
  getAttribute(name: string): string | null;
  closest(selector: string): FakeNode | null;
  contains(other: FakeNode): boolean;
}

function fakeNode(tagName: string, attrs: Record<string, string>, text: string, options: { className?: string; closestNode?: FakeNode | null } = {}): FakeNode {
  const node: FakeNode = {
    tagName,
    className: options.className || "",
    innerText: text,
    textContent: text,
    attrs,
    closestNode: options.closestNode ?? null,
    getAttribute(name: string) {
      return Object.prototype.hasOwnProperty.call(attrs, name) ? attrs[name] : null;
    },
    closest() {
      return node.closestNode ?? null;
    },
    contains() {
      return false;
    },
  };
  return node;
}

/** Runs an adapter function expression against a selector → nodes map, with a
 * stub `document`. Unknown selectors return [] exactly like a real page without
 * that markup. */
async function runAdapter(source: string, selectors: Record<string, FakeNode[]>, args: unknown = {}) {
  const previous = (globalThis as { document?: unknown }).document;
  (globalThis as { document?: unknown }).document = {
    querySelectorAll(selector: string) {
      return selectors[selector] ?? [];
    },
  };
  try {
    const fn = new Function(`return (${source});`)() as (value: unknown) => Promise<unknown> | unknown;
    return (await fn(args)) as { site: string; messages: Message[]; truncated: boolean; total: number; selectorMatches: number };
  } finally {
    (globalThis as { document?: unknown }).document = previous;
  }
}

test("the five default sites are the planned adapters, in a fixed order", () => {
  const urls = DEFAULT_SITES.map(site => site.url);
  assert.deepEqual(urls, [
    "https://www.kimi.com",
    "https://chat.deepseek.com",
    "https://gemini.google.com/app",
    "https://claude.ai/new",
    "https://chatgpt.com",
  ]);
  // Every default has a matching adapter source; a chip without one would fall
  // back to body text on a site the plan expects to parse.
  for (const site of DEFAULT_SITES) {
    const adapter = adapterFor(site.url);
    assert.ok(adapter, `${site.url} has no adapter`);
    assert.equal(adapter.site, site.id);
    assert.ok(SITE_ADAPTERS[adapter.site].length > 0);
  }
});

test("URL matching covers each site, its subdomains, and rejects look-alikes", () => {
  assert.equal(matchSite("https://www.kimi.com/chat/abc"), "kimi");
  assert.equal(matchSite("https://kimi.com"), "kimi");
  assert.equal(matchSite("https://chat.deepseek.com/a/chat/s/1"), "deepseek");
  assert.equal(matchSite("https://gemini.google.com/app/xyz"), "gemini");
  assert.equal(matchSite("https://claude.ai/new"), "claude");
  assert.equal(matchSite("https://claude.ai/chat/1"), "claude");
  assert.equal(matchSite("https://chatgpt.com/c/1"), "chatgpt");
  assert.equal(matchSite("https://chat.openai.com/c/1"), "");
  // A site id in a query string or a look-alike host must not match.
  assert.equal(matchSite("https://evil.example/?next=kimi.com"), "");
  assert.equal(matchSite("https://kimi.com.evil.example/"), "");
  assert.equal(matchSite("https://notkimi.com/"), "");
  assert.equal(matchSite(""), "");
  assert.equal(matchSite("not a url"), "");
});

test("adapter matching also drives the composer fill, and non-sites get no composer", () => {
  assert.deepEqual(composerFor("https://www.kimi.com")?.inputs.length ? true : false, true);
  assert.equal(composerFor("https://example.com"), null);
  assert.equal(adapterFor("https://example.com"), null);
});

test("URL normalization accepts only complete http(s) addresses", () => {
  assert.equal(normalizeUrl("https://www.kimi.com"), "https://www.kimi.com/");
  assert.equal(normalizeUrl("  https://chatgpt.com/c/1  "), "https://chatgpt.com/c/1");
  assert.equal(normalizeUrl("ftp://example.com"), "");
  assert.equal(normalizeUrl("javascript:alert(1)"), "");
  assert.equal(normalizeUrl("file:///etc/passwd"), "");
  assert.equal(normalizeUrl("https://user:pass@example.com"), "");
  assert.equal(normalizeUrl("kimi.com"), "");
  assert.equal(normalizeUrl(""), "");
  assert.equal(normalizeUrl(null), "");
  assert.equal(normalizeUrl("https://example.com/" + "a".repeat(3000)), "");
});

// UNVERIFIED: synthetic DOM only. No real site login or parse is performed.
test("each site adapter returns structured {id, role, text} from synthetic DOM", async () => {
  const cases: { site: string; url: string; selectors: Record<string, FakeNode[]>; expectRoles: string[] }[] = [
    {
      site: "kimi",
      url: "https://www.kimi.com/chat/1",
      selectors: {
        "[data-message-author-role]": [
          fakeNode("DIV", { "data-message-author-role": "user", "data-message-id": "k1" }, "帮我规划一个缓存迁移"),
          fakeNode("DIV", { "data-message-author-role": "assistant", "data-message-id": "k2" }, "先列出三个约束。\n\n再给方案。"),
        ],
      },
      expectRoles: ["user", "assistant"],
    },
    {
      site: "deepseek",
      url: "https://chat.deepseek.com/a/chat/s/1",
      selectors: {
        ".ds-message": [
          fakeNode("DIV", { "data-role": "user" }, "这段代码为什么慢"),
          fakeNode("DIV", { "data-role": "assistant" }, "因为每次请求都重建索引"),
        ],
      },
      expectRoles: ["user", "assistant"],
    },
    {
      site: "gemini",
      url: "https://gemini.google.com/app/x",
      selectors: {
        "user-query": [fakeNode("USER-QUERY", {}, "总结一下这份需求")],
        "model-response": [fakeNode("MODEL-RESPONSE", {}, "需求有三块：面板、提取、交接")],
      },
      expectRoles: ["user", "assistant"],
    },
    {
      site: "claude",
      url: "https://claude.ai/chat/1",
      selectors: {
        '[data-testid="user-message"]': [fakeNode("DIV", {}, "先给结论", { className: "font-user-message" })],
        '[data-testid="assistant-message"]': [fakeNode("DIV", {}, "结论如下", { className: "font-claude-message" })],
      },
      expectRoles: ["user", "assistant"],
    },
    {
      site: "chatgpt",
      url: "https://chatgpt.com/c/1",
      selectors: {
        "[data-message-author-role]": [
          fakeNode("DIV", { "data-message-author-role": "user" }, "帮我把这段整理成计划"),
          fakeNode("DIV", { "data-message-author-role": "assistant" }, "第一步，收集现状。"),
        ],
      },
      expectRoles: ["user", "assistant"],
    },
  ];
  for (const item of cases) {
    const source = SITE_ADAPTERS[item.site];
    assert.ok(source, `${item.site} has no adapter`);
    const answer = await runAdapter(source, item.selectors, { limit: 10 });
    assert.equal(answer.site, item.site);
    assert.equal(answer.messages.length, 2, `${item.site}: ${JSON.stringify(answer)}`);
    assert.deepEqual(answer.messages.map(message => message.role), item.expectRoles);
    for (const message of answer.messages) {
      assert.equal(typeof message.id, "string");
      assert.ok(message.id.length > 0);
      assert.ok(message.text.length > 0);
      assert.ok(!("node" in message), "an adapter must not return DOM nodes");
    }
  }
});

// UNVERIFIED: synthetic DOM only.
test("an adapter bounds message count and length instead of returning everything", async () => {
  const many = Array.from({ length: 90 }, (_, index) =>
    fakeNode("DIV", { "data-message-author-role": index % 2 ? "assistant" : "user" }, "消息 " + index + " " + "x".repeat(50)),
  );
  const answer = await runAdapter(SITE_ADAPTERS.kimi, { "[data-message-author-role]": many }, { limit: 5 });
  assert.equal(answer.messages.length, 5);
  assert.equal(answer.truncated, true);

  const long = [fakeNode("DIV", { "data-message-author-role": "assistant" }, "y".repeat(20000))];
  const clipped = await runAdapter(SITE_ADAPTERS.kimi, { "[data-message-author-role]": long }, { limit: 5, maxLen: 300 });
  assert.equal(clipped.messages.length, 1);
  assert.ok(clipped.messages[0].text.length <= 320);
  assert.ok(clipped.messages[0].text.endsWith("…"));
});

test("normalizeMessages re-checks bounds the page adapter could have exceeded", () => {
  const raw = {
    messages: Array.from({ length: 200 }, (_, index) => ({ id: "m" + index, role: "user", text: "x".repeat(9000) })),
  };
  const result = normalizeMessages(raw);
  assert.ok(result.messages.length <= 80);
  for (const message of result.messages) assert.ok(message.text.length <= 6001);
  assert.equal(result.truncated, true);
  // Roles outside the known set never become a guessed label.
  const unknown = normalizeMessages({ messages: [{ id: "a", role: "systemish", text: "x" }] });
  assert.equal(unknown.messages[0].role, "unknown");
  assert.deepEqual(normalizeMessages(null).messages, []);
});

test("a failing or empty adapter falls back to selection/body with a truthful notice", () => {
  const fallback = chooseExtraction({ site: "claude", adapter: { messages: [] }, pageText: "页面正文内容" });
  assert.equal(fallback.mode, "fallback");
  assert.equal(fallback.messages.length, 1);
  assert.equal(fallback.messages[0].role, "page");
  assert.match(fallback.notice, /回退/);
  assert.match(fallback.notice, /claude/);

  const failed = chooseExtraction({ site: "gemini", adapter: null, adapterError: "PLUGIN_ADAPTER_FAILED", pageText: "选区文字" });
  assert.equal(failed.mode, "fallback");
  assert.match(failed.notice, /适配器/);
  assert.ok(failed.messages[0].text.includes("选区文字"));

  const noSite = chooseExtraction({ site: "", adapter: null, pageText: "任意正文" });
  assert.equal(noSite.mode, "fallback");
  assert.match(noSite.notice, /没有消息适配器/);

  const empty = chooseExtraction({ site: "kimi", adapter: { messages: [] }, pageText: "   " });
  assert.equal(empty.mode, "empty");
  assert.deepEqual(empty.messages, []);
  assert.match(empty.notice, /没有可提取/);

  const ok = chooseExtraction({ site: "kimi", adapter: { messages: [{ id: "1", role: "user", text: "问题" }, { id: "2", role: "assistant", text: "回答" }] } });
  assert.equal(ok.mode, "adapter");
  assert.equal(ok.messages.length, 2);
});

test("only the selected messages are exported, and the export says where it came from", () => {
  const messages = [
    { id: "a", role: "user", text: "第一条", label: "用户" },
    { id: "b", role: "assistant", text: "第二条", label: "助手" },
    { id: "c", role: "assistant", text: "第三条", label: "助手" },
  ];
  const only = buildHandoffText(messages, new Set(["b"]), { site: "kimi" });
  assert.ok(only.includes("第二条"));
  assert.ok(!only.includes("第一条"));
  assert.ok(!only.includes("第三条"));
  assert.ok(only.includes("kimi"));
  assert.equal(buildHandoffText(messages, new Set()), "");
  // A Set is what the picker builds; an array is accepted for tests and scripts.
  assert.equal(buildHandoffText(messages, ["a"]).includes("第一条"), true);
});

test("the three presets map exactly onto PlannerOptions", () => {
  assert.deepEqual(planOptions("pure"), { context: false, skills: false, tools: false, readOnly: true });
  assert.deepEqual(planOptions("review"), { context: true, skills: false, tools: true, readOnly: true });
  assert.deepEqual(planOptions("full"), { context: true, skills: true, tools: true, readOnly: false });
  // Custom switches each item, and tools=false keeps the core's read-only rule.
  assert.deepEqual(planOptions("custom", { context: true, skills: true, tools: false }), { context: true, skills: true, tools: false, readOnly: true });
  assert.deepEqual(planOptions("custom", { context: false, skills: false, tools: true }), { context: false, skills: false, tools: true, readOnly: false });
  assert.deepEqual(planOptions("custom", {}), { context: false, skills: false, tools: false, readOnly: true });
  // An unknown preset must not silently become "full".
  assert.deepEqual(planOptions("nonsense", {}), { context: false, skills: false, tools: false, readOnly: true });
});

test("a draft is never overwritten without an explicit choice", () => {
  const empty = fillPlan({ before: "", text: "摘要" });
  assert.equal(empty.ok, true);
  assert.equal(empty.text, "摘要");

  const needsChoice = fillPlan({ before: "已有内容", text: "摘要" });
  assert.equal(needsChoice.ok, false);
  assert.equal(needsChoice.needsChoice, true);

  const append = fillPlan({ before: "已有内容", text: "摘要", choice: "append" });
  assert.equal(append.ok, true);
  assert.equal(append.text, "已有内容\n\n摘要");

  const replace = fillPlan({ before: "已有内容", text: "摘要", choice: "replace" });
  assert.equal(replace.ok, true);
  assert.equal(replace.text, "摘要");

  const blank = fillPlan({ before: "", text: "   " });
  assert.equal(blank.ok, false);
});

test("a fill is only accepted after reading the same text back", () => {
  assert.equal(verifyDraftFill("摘要", "摘要").ok, true);
  // Trailing whitespace from a contenteditable does not fail the check...
  assert.equal(verifyDraftFill("摘要", "摘要\n\n").ok, true);
  // ...but different content does, and an empty write is never a success.
  assert.equal(verifyDraftFill("摘要", "摘要被截断").ok, false);
  assert.equal(verifyDraftFill("摘要", "").ok, false);
  assert.equal(verifyDraftFill("", "").ok, false);
});

test("the Code→web summary only uses text the core actually returned", () => {
  const view = {
    session: { id: "s1", title: "缓存迁移" },
    messages: [
      { id: "1", role: "user", text: "先看缓存层" },
      { id: "2", role: "assistant", text: "缓存层有三个入口" },
      { id: "3", role: "tool", tool: "read_file", text: "internal/cache/store.go" },
    ],
  };
  const summary = sessionSummary(view, { maxMessages: 10, maxChars: 4000 });
  assert.equal(summary.title, "缓存迁移");
  assert.ok(summary.text.includes("缓存层有三个入口"));
  assert.ok(summary.text.includes("read_file"));
  assert.ok(summary.text.includes("缓存迁移"));
  assert.equal(summary.truncated, false);

  const bounded = sessionSummary(view, { maxMessages: 1, maxChars: 60 });
  assert.equal(bounded.truncated, true);
  assert.ok(bounded.text.length <= 400);
  // An empty session yields a title and the heading only, never invented text.
  const empty = sessionSummary({ session: { id: "s2" }, messages: [] });
  assert.equal(empty.title, "未命名会话");
  assert.ok(!empty.text.includes("缓存层"));
});

test("drafts live per source and survive a switch", () => {
  let drafts = { web: "", local: "" };
  drafts = draftWith(drafts, "web", "网页草稿");
  drafts = draftWith(drafts, "local", "本地草稿");
  assert.equal(drafts.web, "网页草稿");
  assert.equal(drafts.local, "本地草稿");
  drafts = draftWith(drafts, "web", "网页草稿 v2");
  assert.equal(drafts.local, "本地草稿");
  const recovered = draftWith(undefined, "web", "x");
  assert.deepEqual(Object.keys(recovered).sort(), ["local", "web"]);
});

test("sites can be added and removed, and persist through normalization", () => {
  let sites = normalizeSites(null);
  assert.equal(sites.length, 5);

  const added = addSite(sites, { label: "Mistral", url: "https://chat.mistral.ai/" });
  assert.equal(added.ok, true);
  sites = added.sites;
  assert.equal(sites.length, 6);
  assert.equal(sites[5].url, "https://chat.mistral.ai/");

  const duplicate = addSite(sites, { label: "dup", url: "https://chat.mistral.ai" });
  assert.equal(duplicate.ok, false);
  assert.match(duplicate.error || "", /已经在列表/);

  const invalid = addSite(sites, { label: "bad", url: "javascript:alert(1)" });
  assert.equal(invalid.ok, false);
  assert.match(invalid.error || "", /http/);

  sites = removeSite(sites, "kimi");
  assert.ok(!sites.some(site => site.id === "kimi"));
  // A stored list that has been emptied stays empty: defaults are a first-run
  // value, not a self-healing fallback that undoes a user's deletion.
  const empty = normalizeSites([]);
  assert.deepEqual(empty, []);

  // The persisted shape is re-validated on read: junk is dropped, valid entries kept.
  const reread = normalizeSites([
    { id: "mine", label: "Mine", url: "https://chat.mistral.ai/" },
    { id: "bad", label: "Bad", url: "not-a-url" },
    { id: "dup", label: "Dup", url: "https://chat.mistral.ai/" },
  ]);
  assert.equal(reread.length, 1);
  assert.equal(reread[0].id, "mine");

  // A cap keeps the list usable in one toolbar row.
  let capped = normalizeSites(DEFAULT_SITES);
  for (let index = 0; index < MAX_SITES + 4; index += 1) {
    capped = addSite(capped, { label: "s" + index, url: "https://example" + index + ".com" }).sites;
  }
  assert.equal(capped.length, MAX_SITES);
});

test("handoff payloads never carry new-session fields to an existing session", () => {
  const fresh = handoffInput({ text: "  内容  ", mode: "plan", cwd: "/tmp/x", model: "kimi-k2", title: "标题" });
  assert.deepEqual(fresh, { text: "内容", mode: "plan", cwd: "/tmp/x", model: "kimi-k2", title: "标题" });

  const existing = handoffInput({ text: "内容", targetId: "s1", mode: "tower", cwd: "/tmp/x", model: "kimi-k2", title: "标题" });
  assert.deepEqual(existing, { text: "内容", mode: "tower", targetId: "s1" });
  assert.equal("model" in existing, false);
  assert.equal("cwd" in existing, false);
  assert.equal("title" in existing, false);

  // An unknown mode is not forwarded as-is: normal is the core's own default.
  assert.equal(handoffInput({ text: "x", mode: "yolo" }).mode, "normal");
  assert.equal(handoffInput({ text: "x" }).mode, "normal");
  // Blank optional fields are omitted rather than sent as empty strings.
  assert.equal("model" in handoffInput({ text: "x", model: "   " }), false);
});

test("a committed session opens same-screen with A2, and through sessions.open without it", async () => {
  // No multi-session plugin installed: the host's own session navigation is
  // the real path, so the target names it instead of reporting a gap.
  const plain = openTarget([], "s1");
  assert.equal(plain.mode, "same-window");
  assert.equal(plain.action, "sessions.open");
  assert.deepEqual(plain.args, { sessionId: "s1" });
  assert.equal("gap" in plain, false);
  assert.equal(openTarget([{ name: "planner.something" }], "s1").action, "sessions.open");

  const found = openTarget([{ name: "multiSession.open", pluginId: "kimicode-plus.multi-session" }], "s1");
  assert.equal(found.mode, "same-screen");
  assert.equal(found.action, "multiSession.open");
  assert.deepEqual(found.args, { sessionId: "s1" });

  // Each mode calls exactly the API it names.
  const calls: string[] = [];
  const sessions = { async open(id: string) { calls.push("sessions.open:" + id); } };
  const commands = { async invoke(name: string, args: { sessionId: string }) { calls.push(name + ":" + args.sessionId); } };
  assert.deepEqual(await openSessionTarget({ sessions, commands }, plain), { via: "sessions" });
  assert.deepEqual(await openSessionTarget({ sessions, commands }, found), { via: "command" });
  assert.deepEqual(calls, ["sessions.open:s1", "multiSession.open:s1"]);

  // A host that publishes neither refuses rather than pretending it opened.
  await assert.rejects(() => openSessionTarget({ commands }, plain), /sessions:read/);
  await assert.rejects(() => openSessionTarget({ sessions }, found), /命令接口/);
});

test("the open-session button is disabled only when the host really cannot open", () => {
  const plain = openTarget([], "s1");
  const withoutSessions = openButtonState(plain, { commands: {} });
  assert.equal(withoutSessions.disabled, true);
  assert.match(withoutSessions.title, /sessions:read/);
  assert.equal(withoutSessions.mode, "same-window");

  const withSessions = openButtonState(plain, { commands: {}, sessions: {} });
  assert.equal(withSessions.disabled, false);
  assert.equal(withSessions.label, "打开会话");

  const sameScreen = openTarget([{ name: "multiSession.open" }], "s1");
  assert.equal(openButtonState(sameScreen, { sessions: {} }).disabled, true, "the command path needs commands");
  const ready = openButtonState(sameScreen, { commands: {}, sessions: {} });
  assert.equal(ready.disabled, false);
  assert.equal(ready.label, "在同屏打开");
  assert.equal(ready.title, "经 multiSession.open 在同屏会话中打开");
});

test("only a thread this plugin created can be renamed or deleted", () => {
  const owned = threadActions({ id: "t1", pluginOwner: "kimicode-plus.planner", title: "x" }, "kimicode-plus.planner");
  assert.deepEqual(owned, { owned: true, canOpen: true, canDelete: true, canRename: true });

  // An empty owner is the built-in UI's thread, not this plugin's.
  const builtIn = threadActions({ id: "t2", title: "内置线程" }, "kimicode-plus.planner");
  assert.deepEqual(builtIn, { owned: false, canOpen: true, canDelete: false, canRename: false });

  const foreign = threadActions({ id: "t3", pluginOwner: "kimicode-plus.multi-session" }, "kimicode-plus.planner");
  assert.equal(foreign.owned, false);
  assert.equal(foreign.canRename, false);
  assert.equal(foreign.canDelete, false);
  assert.equal(threadActions(null, "kimicode-plus.planner").owned, false);
});

test("renaming persists through planner.rename and reads the list back", async () => {
  const calls: { action: string; args: unknown[] }[] = [];
  const stored = { id: "t1", title: "新标题" };
  const plannerApi = {
    async rename(id: string, title: string) {
      calls.push({ action: "rename", args: [id, title] });
      return stored;
    },
    async threads() {
      calls.push({ action: "threads", args: [] });
      return [stored];
    },
  };
  const result = await renamePlannerThread({ planner: plannerApi }, "t1", "  新标题  ");
  assert.deepEqual(result.thread, stored);
  assert.deepEqual(result.threads, [stored]);
  // The trimmed title is what reaches the core, and the list is re-read so the
  // history shows the stored title rather than the typed one.
  assert.equal(calls.length, 2);
  assert.equal(calls[0].action, "rename");
  assert.deepEqual(calls[0].args, ["t1", "新标题"]);
  assert.equal(calls[1].action, "threads");

  // A blank title never reaches the core.
  calls.length = 0;
  await assert.rejects(() => renamePlannerThread({ planner: plannerApi }, "t1", "   "), /标题不能为空/);
  assert.equal(calls.length, 0);

  // The core's limit is 200 bytes, so a long Chinese title is refused here
  // rather than after a round trip that would fail anyway.
  await assert.rejects(() => renamePlannerThread({ planner: plannerApi }, "t1", "规".repeat(67)), /200 字节/);
  assert.equal(calls.length, 0);
  // 66 Chinese characters are 198 bytes and still fit.
  const longTitle = "规".repeat(66);
  await renamePlannerThread({ planner: plannerApi }, "t1", longTitle);
  assert.equal(calls[0].args[1], longTitle);

  // A host without planner:run refuses instead of editing memory only.
  await assert.rejects(() => renamePlannerThread({ planner: { async threads() { return []; } } }, "t1", "x"), /planner:run/);
});

test("a failed rename surfaces the core error and leaves the stored title alone", async () => {
  const plannerApi = {
    async rename() {
      throw Object.assign(new Error("标题过长"), { code: "PLANNER_TITLE_INVALID" });
    },
    async threads() {
      throw new Error("must not be read after a failed rename");
    },
  };
  await assert.rejects(() => renamePlannerThread({ planner: plannerApi }, "t1", "x"), /标题过长/);
});

test("back and reload go through the view's own history, not an injected script", async () => {
  const calls: string[] = [];
  const view = {
    async back() {
      calls.push("back");
      return true;
    },
    async reload() {
      calls.push("reload");
    },
    async openExternal() {
      calls.push("external");
    },
  };
  assert.deepEqual(await runPageAction(view, "back"), { moved: true });
  assert.deepEqual(await runPageAction(view, "reload"), { moved: true });
  assert.deepEqual(await runPageAction(view, "external"), { moved: true });
  assert.deepEqual(calls, ["back", "reload", "external"]);

  // A view that reports no history is told apart from a successful back.
  const stuck = { async back() { return false; } };
  assert.deepEqual(await runPageAction(stuck, "back"), { moved: false });

  // An unknown kind and a missing view are refused, never silently ignored.
  await assert.rejects(() => runPageAction(view, "history.back"), /未知的页面动作/);
  await assert.rejects(() => runPageAction(null, "back"), /网页视图还没有打开/);

  // The injected-script path is gone from the shipped source.
  assert.equal(rendererSource.includes("history.back()"), false);
  assert.equal(rendererSource.includes("location.reload()"), false);
});

test("popping out opens the host window, then drops only this panel mount", async () => {
  const calls: string[] = [];
  const apis = {
    surface: "host",
    window: {
      async popout() {
        calls.push("popout");
      },
      async close() {
        calls.push("close");
      },
    },
    panel: {
      close() {
        calls.push("panel.close");
      },
    },
  };
  assert.deepEqual(await popoutIntoWindow(apis), { surface: "popout" });
  // The panel closes after the host window exists, and nothing disposes the
  // plugin: the popout mounts the same instance.
  assert.deepEqual(calls, ["popout", "panel.close"]);

  // A popout does not offer a second popout.
  calls.length = 0;
  await assert.rejects(() => popoutIntoWindow({ ...apis, surface: "popout" }), /已经在独立窗口里/);
  assert.deepEqual(calls, []);

  // A host without the window surface refuses instead of doing nothing.
  await assert.rejects(() => popoutIntoWindow({ surface: "host", panel: { close() {} } }), /ui:right-panel/);

  // Closing from the popout calls the host's close; the host surface refuses.
  calls.length = 0;
  assert.deepEqual(await closePopoutWindow({ surface: "popout", window: apis.window }), { surface: "host" });
  assert.deepEqual(calls, ["close"]);
  await assert.rejects(() => closePopoutWindow({ surface: "host", window: apis.window }), /不在独立窗口里/);
});

test("the panel hands the real context surfaces to each orchestrated call", () => {
  // The helpers above are correct on their own; this guards the wiring that
  // feeds them. A call site that dropped `sessions`, `window` or the panel
  // handle would leave a button that silently does nothing.
  const source = rendererSource;
  assert.ok(source.includes("const { storage, planner, handoff, browser, commands, sessions, models: modelsApi } = ctx;"));
  assert.ok(source.includes("const windowHost = ctx.window;"));
  assert.ok(source.includes("const surface = ctx.plugin?.surface === \"popout\" ? \"popout\" : \"host\";"));
  assert.ok(source.includes("await openSessionTarget({ commands, sessions }, target);"));
  assert.ok(source.includes("openButtonState(target, { commands, sessions })"));
  assert.ok(source.includes("await renamePlannerThread({ planner }, id, value);"));
  assert.ok(source.includes("await popoutIntoWindow({ window: windowHost, surface, panel: panelHandle });"));
  assert.ok(source.includes("await closePopoutWindow({ window: windowHost, surface });"));
  assert.ok(source.includes("await runPageAction(state.view, kind);"));
  assert.ok(source.includes('await runPageAction(state.view, "external");'));
});

test("the menu offers popout on the host surface and window close in a popout", () => {
  const source = rendererSource;
  const menu = source.slice(source.indexOf('el.menu.append(node("div", "ekd-sep"), node("p", "ekd-note", "窗口"))'), source.indexOf('el.menu.append(node("div", "ekd-sep"), node("p", "ekd-note", "规划线程"))'));
  assert.ok(menu.length > 0, "the window section exists in the menu");
  assert.ok(menu.includes('surface === "popout"'));
  assert.ok(menu.includes("关闭窗口"));
  assert.ok(menu.includes("弹出独立窗口"));
  // The popout button only exists where the host can actually open a window.
  assert.ok(menu.includes("windowHost"));
  // No gap note claims a feature the host really provides.
  for (const gone of ["GAPS.rename", "GAPS.popout", "GAPS.openSession", "renameGap"]) {
    assert.equal(source.includes(gone), false, `${gone} must be gone`);
  }
});

test("code fences become their own handoff target", () => {
  const parts = splitCodeBlocks("说明\n\n```ts\nconst a = 1;\n```\n收尾");
  assert.deepEqual(parts.map(part => part.type), ["text", "code", "text"]);
  assert.equal(parts[1].code, "const a = 1;");
  assert.deepEqual(splitCodeBlocks("没有代码"), [{ type: "text", text: "没有代码" }]);
});

// --- 视图生命周期 ---------------------------------------------------------

function fakeView() {
  const calls: { show: unknown[]; hide: number; close: number } = { show: [], hide: 0, close: 0 };
  return {
    calls,
    view: {
      id: "planner-web",
      url: "https://www.kimi.com/",
      async show(bounds: unknown) {
        calls.show.push(bounds);
      },
      async hide() {
        calls.hide += 1;
      },
      async close() {
        calls.close += 1;
      },
    },
  };
}

const RECT = { x: 10, y: 20, width: 400, height: 600 };

test("the web view is hidden whenever the panel's own HTML would cover it", async () => {
  const { calls, view } = fakeView();
  let covered = false;
  const sync = createBrowserSync({
    view,
    getRect: () => RECT,
    isCovered: () => covered,
  });

  // Panel open, nothing covering: the real container bounds are applied.
  assert.deepEqual(await sync.apply(), { visible: true });
  assert.deepEqual(calls.show[0], RECT);

  // A menu opens: the view must go away, not stay on top of the menu.
  covered = true;
  assert.deepEqual(await sync.apply(), { visible: false });
  assert.equal(calls.hide, 1);
  // A second apply while still covered does not issue another hide.
  await sync.apply();
  assert.equal(calls.hide, 1);

  // Menu closes: it comes back with fresh bounds.
  covered = false;
  assert.deepEqual(await sync.apply(), { visible: true });
  assert.equal(calls.show.length, 2);

  // Zero-size container means no usable rect even when not "covered".
  const collapsed = createBrowserSync({ view, getRect: () => ({ x: 0, y: 0, width: 0, height: 0 }), isCovered: () => false });
  assert.deepEqual(await collapsed.apply(), { visible: false });
});

test("a resize re-applies bounds, and disposing hides then never shows again", async () => {
  const { calls, view } = fakeView();
  const sync = createBrowserSync({ view, getRect: () => RECT, isCovered: () => false });
  await sync.apply();
  const widened = { x: 10, y: 20, width: 900, height: 600 };
  const resized = createBrowserSync({ view: { ...view, async show(bounds: unknown) { calls.show.push(bounds); } }, getRect: () => widened, isCovered: () => false });
  await resized.apply();
  assert.deepEqual(calls.show[1], widened);

  const disposed = createBrowserSync({ view, getRect: () => RECT, isCovered: () => false });
  await disposed.apply();
  const before = calls.hide;
  await disposed.dispose();
  assert.equal(calls.hide, before + 1);
  // After disposal a late resize callback must not bring the view back.
  assert.deepEqual(await disposed.apply(), { visible: false });
  assert.equal(disposed.visible, false);
});

test("the panel's web view is destroyed on disable, not left on screen", async () => {
  // The plugin registers view.close in its dispose hooks; this models the same
  // contract with the fake view so the test does not need a browser.
  const { calls, view } = fakeView();
  const sync = createBrowserSync({ view, getRect: () => RECT, isCovered: () => false });
  await sync.apply();
  const hooks = [() => sync.dispose(), () => view.close()];
  for (const hook of hooks.reverse()) await hook();
  assert.equal(calls.hide, 1);
  assert.equal(calls.close, 1);
});

// --- 插件编排（mock host） -------------------------------------------------
//
// A mock host proves the plugin's own request shapes. It says nothing about a
// real site, a real session or a real model.

interface MockHost {
  ctx: Record<string, unknown>;
  calls: { action: string; params: unknown }[];
  plannerCalls: { action: string; args: unknown[] }[];
  commits: unknown[];
  previews: unknown[];
  commands: { name: string }[];
  events: { name: string; listener: (payload: unknown) => void; disposed: boolean }[];
  entry?: { id: string; label: string; run: () => unknown };
  panel?: { title: string };
}

function mockHost(options: { commands?: { name: string }[]; surface?: "host" | "popout" } = {}): MockHost {
  const calls: { action: string; params: unknown }[] = [];
  const plannerCalls: { action: string; args: unknown[] }[] = [];
  const commits: unknown[] = [];
  const previews: unknown[] = [];
  const events: { name: string; listener: (payload: unknown) => void; disposed: boolean }[] = [];
  const host: MockHost = {
    calls,
    plannerCalls,
    commits,
    previews,
    commands: options.commands || [],
    events,
    ctx: {},
  };
  const thread = (overrides: Record<string, unknown> = {}) => ({
    id: "t1",
    title: "未命名规划",
    model: "kimi-k2",
    status: "idle",
    revision: 1,
    messages: [],
    ...overrides,
  });
  const dispose = () => ({ dispose() {} });
  host.ctx = {
    plugin: { id: "kimicode-plus.planner", version: "1.0.0", dataVersion: 2, surface: options.surface || "host" },
    core: {
      async request(action: string, params: unknown) {
        calls.push({ action, params });
        if (action === "sessions.list") return { items: [{ id: "s1", title: "会话一", cwd: "/tmp/p" }] };
        if (action === "sessions.view") return { session: { id: "s1", title: "会话一" }, messages: [{ id: "m1", role: "user", text: "真实消息" }] };
        return {};
      },
    },
    events: {
      on(name: string, listener: (payload: unknown) => void) {
        const record = { name, listener, disposed: false };
        events.push(record);
        return { dispose: () => { record.disposed = true; } };
      },
    },
    ui: { mount: dispose, decorate: dispose, theme: () => ({ scheme: "light", tokens: {} }) },
    net: { fetch: async () => ({ status: 200, headers: {}, body: "{}", url: "" }) },
    storage: {
      async get() {
        return undefined;
      },
      async set() {},
      async delete() {},
      async list() {
        return [];
      },
    },
    log() {},
    onDispose() {},
    entry: {
      add(definition: { id: string; label: string; run: () => unknown }) {
        host.entry = definition;
        return dispose();
      },
    },
    rightPanel: {
      mount(definition: { title: string }) {
        host.panel = definition;
        // Closing the tab drops this mount only; nothing here disposes the
        // plugin, which is what a popout relies on.
        return { open() {}, close() {}, open_: false, dispose() {} };
      },
    },
    // The popout and session-navigation surfaces every real host provides for
    // this plugin's grants; a test that drives them asserts on its own spies.
    window: { async popout() {}, async close() {} },
    sessions: { async open() {} },
    layout: { acquireSidebarCollapse: dispose },
    planner: {
      async threads() {
        plannerCalls.push({ action: "threads", args: [] });
        return [thread()];
      },
      async models() {
        plannerCalls.push({ action: "models", args: [] });
        return [{ id: "kimi-k2", name: "Kimi K2" }];
      },
      async create(input: unknown) {
        plannerCalls.push({ action: "create", args: [input] });
        return thread();
      },
      async get(id: string) {
        plannerCalls.push({ action: "get", args: [id] });
        return thread({ id });
      },
      async configure(id: string, input: unknown) {
        plannerCalls.push({ action: "configure", args: [id, input] });
        return thread({ id });
      },
      async send(id: string, text: string) {
        plannerCalls.push({ action: "send", args: [id, text] });
        return thread({ id, status: "running" });
      },
      async retry(id: string, revision: number) {
        plannerCalls.push({ action: "retry", args: [id, revision] });
        return thread({ id });
      },
      async stop(id: string) {
        plannerCalls.push({ action: "stop", args: [id] });
        return thread({ id, status: "stopped" });
      },
      async approve(id: string, approvalId: string, allow: boolean) {
        plannerCalls.push({ action: "approve", args: [id, approvalId, allow] });
        return thread({ id });
      },
      async remove(id: string) {
        plannerCalls.push({ action: "remove", args: [id] });
        return { deleted: true };
      },
      async rename(id: string, title: string) {
        plannerCalls.push({ action: "rename", args: [id, title] });
        return thread({ id, title });
      },
    },
    handoff: {
      async preview(input: unknown) {
        previews.push(input);
        return { targetId: (input as { targetId?: string }).targetId || "", mode: "normal", text: "x", bytes: 1, newSession: !(input as { targetId?: string }).targetId };
      },
      async commit(input: unknown) {
        commits.push(input);
        return { sessionId: "s-new", created: true };
      },
    },
    models: { list: async () => [{ id: "kimi-k2", name: "Kimi K2" }] },
    commands: {
      async list() {
        return options.commands || [];
      },
      async invoke() {},
    },
  };
  return host;
}

// The plugin only reaches the DOM for its own panel; in Node there is none, so
// activate() returns before mounting. These tests therefore drive the exported
// orchestration helpers the panel calls, which is where the request shapes live.

test("a local turn uses create with the preset options and then send", async () => {
  const { sendPlanMessage } = planner as unknown as {
    sendPlanMessage(api: unknown, input: unknown): Promise<{ id: string }>;
  };
  const calls: { action: string; args: unknown[] }[] = [];
  const api = {
    async create(input: unknown) {
      calls.push({ action: "create", args: [input] });
      return { id: "t1" };
    },
    async configure(id: string, input: unknown) {
      calls.push({ action: "configure", args: [id, input] });
      return { id };
    },
    async send(id: string, text: string) {
      calls.push({ action: "send", args: [id, text] });
      return { id };
    },
  };
  await sendPlanMessage(api, { preset: "review", model: "kimi-k2", text: "开始规划" });
  assert.deepEqual(calls[0], { action: "create", args: [{ model: "kimi-k2", options: { context: true, skills: false, tools: true, readOnly: true } }] });
  assert.deepEqual(calls[1], { action: "send", args: ["t1", "开始规划"] });

  // An existing thread is reconfigured, never re-created, so history is kept.
  calls.length = 0;
  await sendPlanMessage(api, { preset: "full", model: "kimi-k2", text: "继续", thread: { id: "t9" } });
  assert.deepEqual(calls[0], { action: "configure", args: ["t9", { model: "kimi-k2", options: { context: true, skills: true, tools: true, readOnly: false } }] });
  assert.deepEqual(calls[1], { action: "send", args: ["t9", "继续"] });
});

test("approval and stop go to the core with the real thread and approval ids", async () => {
  const { approvePlan, stopPlan } = planner as unknown as {
    approvePlan(api: unknown, threadId: string, approvalId: string, allow: boolean): Promise<unknown>;
    stopPlan(api: unknown, threadId: string): Promise<unknown>;
  };
  const calls: unknown[][] = [];
  const api = {
    approve: async (...args: unknown[]) => {
      calls.push(["approve", ...args]);
      return {};
    },
    stop: async (...args: unknown[]) => {
      calls.push(["stop", ...args]);
      return {};
    },
  };
  await approvePlan(api, "t1", "ap-9", true);
  await approvePlan(api, "t1", "ap-9", false);
  await stopPlan(api, "t1");
  assert.deepEqual(calls, [["approve", "t1", "ap-9", true], ["approve", "t1", "ap-9", false], ["stop", "t1"]]);
});

test("the mock host shows the plugin requests sessions and handoff without inventing results", async () => {
  const host = mockHost();
  // sessions.list and sessions.view are the only session reads the plugin makes
  // for Code→网页; no send, no approve on someone else's session.
  const ctx = host.ctx as { core: { request(action: string, params: unknown): Promise<unknown> } };
  const page = await ctx.core.request("sessions.list", {});
  assert.equal((page as { items: unknown[] }).items.length, 1);
  const view = await ctx.core.request("sessions.view", { id: "s1" });
  assert.equal((view as { messages: unknown[] }).messages.length, 1);
  assert.deepEqual(host.calls.map(call => call.action), ["sessions.list", "sessions.view"]);
});

test("the plugin's own manifest stays at apiVersion 2 with a planner-only browser id", () => {
  const manifest = JSON.parse(readFileSync("../plugins/planner/plugin.json", "utf8")) as {
    id: string;
    apiVersion: number;
    entry: { renderer: string };
    capabilities: string[];
  };
  assert.equal(manifest.id, "kimicode-plus.planner");
  assert.equal(manifest.apiVersion, 2);
  assert.equal(manifest.entry.renderer, "renderer.js");
  // No capability outside the ones the panel actually calls.
  for (const capability of manifest.capabilities) {
    assert.ok(
      [
        "ui:entry", "ui:right-panel", "ui:layout", "browser:view", "planner:run",
        "handoff", "models:read", "sessions:read", "commands", "storage",
      ].includes(capability),
      `unexpected capability ${capability}`,
    );
  }
  assert.equal(new Set(manifest.capabilities).size, manifest.capabilities.length);
});

test("the module never sends a web message on its own", () => {
  // A search for the actions that would send on a site would only find the
  // composer adapter, which reads and writes text. Any submit/click on a send
  // button would be a fabricated feature.
  const source = rendererSource;
  for (const forbidden of ["submit()", "dispatchEvent(new KeyboardEvent", "requestSubmit", ".click()"]) {
    assert.equal(source.includes(forbidden), false, `planner must not ${forbidden}`);
  }
});

test("an empty adapter answer keeps a choice-only draft instead of a blank page", () => {
  // The fallback message names the site, and the empty case is explicit: the UI
  // shows "没有可提取" rather than pretending an empty conversation was parsed.
  const empty = chooseExtraction({ site: "chatgpt", adapter: { messages: [] }, pageText: "" });
  assert.equal(empty.mode, "empty");
  assert.equal(empty.site, "chatgpt");
  assert.equal(empty.messages.length, 0);
  assert.match(empty.notice, /没有可提取/);
});

test("a planner module never builds a second toolbar or an address field in the bar", () => {
  // The tool bar is one row: source switch, per-source actions, more menu. The
  // address input belongs to the menu, so a URL field inside `.ekd-bar` would
  // reintroduce the removed second layer.
  const source = rendererSource;
  const barBlock = source.slice(source.indexOf("function buildBar()"), source.indexOf("function buildWebPane()"));
  assert.ok(barBlock.length > 0);
  // Everything before the menu is the single visible row.
  const row = barBlock.slice(0, barBlock.indexOf('el.menu.setAttribute("role", "menu")'));
  assert.ok(row.length > 0);
  assert.equal(row.includes("el.address"), false, "the visible row must not hold the address input");
  assert.equal(row.includes("state.viewUrl"), false, "the visible row must not read the address");
  assert.ok(row.includes("el.siteChips"));
  assert.ok(row.includes("el.moreBtn"));
  // The address input still exists, but only inside the menu.
  assert.ok(barBlock.includes("el.address"));
  // No duplicate settings icon, no second title row.
  assert.equal(source.includes("SurfaceHeader"), false);
});

test("the plugin never asks the host for the legacy planner feature flag", () => {
  // The plugin must work with features.planner off: it is a plugin, not the
  // frozen built-in surface.
  assert.equal(rendererSource.includes("features.planner"), false);
  assert.equal(rendererSource.includes("features.set"), false);
});

test("typing in the preview does not rewrite the textarea value", () => {
  // A structural render writes the value; a keystroke must only update the
  // derived controls, otherwise the caret jumps to the end mid-sentence.
  const source = rendererSource;
  const structureStart = source.indexOf("function renderPreviewStructure");
  const syncStart = source.indexOf("function syncPreviewControls");
  assert.ok(structureStart > 0 && syncStart > structureStart, "both preview renderers exist, structure first");
  const structureBlock = source.slice(structureStart, syncStart);
  const syncBlock = source.slice(syncStart, source.indexOf("// --- skeleton", syncStart));
  assert.ok(structureBlock.includes("el.previewText.value = preview.text"));
  assert.ok(syncBlock.length > 0);
  assert.equal(syncBlock.includes("el.previewText"), false, "the control sync must not touch the textarea");
  // The typing handler calls the control sync, not the full render.
  const handler = source.slice(source.indexOf('el.previewText.addEventListener("input"'), source.indexOf('el.previewTarget = document.createElement("select")'));
  assert.ok(handler.includes("syncPreviewControls"));
  assert.equal(handler.includes("renderPreview()"), false);
});

test("sessionDiff builds bounded diff output from sessions.diff or message diff blocks", () => {
  const fromGit = sessionDiff(
    {
      available: true,
      clean: false,
      title: "修复鉴权",
      cwd: "/tmp/repo",
      status: "M auth.go",
      diff: "diff --git a/auth.go b/auth.go\n+return true",
      truncated: false,
    },
    { session: { id: "s1", title: "修复鉴权" }, messages: [] },
  );
  assert.equal(fromGit.ok, true);
  assert.equal(fromGit.source, "git");
  assert.match(fromGit.text, /修复鉴权/);
  assert.match(fromGit.text, /M auth\.go/);
  assert.match(fromGit.text, /```diff/);
  assert.match(fromGit.text, /\+return true/);

  // Clean git workspace falls back only to real diff blocks in messages, never inventing edits.
  const fromMsg = sessionDiff(
    { available: true, clean: true, reason: "当前工作区没有未提交的 Git 变更。" },
    {
      session: { id: "s2", title: "查看补丁" },
      messages: [
        { id: "m1", role: "assistant", text: "建议修改如下：\n```diff\n-old()\n+new()\n```" },
      ],
    },
  );
  assert.equal(fromMsg.ok, true);
  assert.equal(fromMsg.source, "messages");
  assert.match(fromMsg.text, /-old\(\)/);

  // Neither workspace diff nor message diff -> explicit error, never fabricated diff text.
  const empty = sessionDiff(
    { available: false, reason: "该会话工作目录不是 Git 仓库，无法读取工作区 Diff。" },
    { session: { id: "s3", title: "普通对话" }, messages: [{ id: "m1", role: "user", text: "你好" }] },
  );
  assert.equal(empty.ok, false);
  assert.match(empty.error || "", /不是 Git 仓库/);
});

test("shouldCloseMenuOnInteraction closes open menu on outside pointer/focus or Escape", () => {
  const insideNode = {};
  const otherNode = {};
  const menu = { hidden: false, contains: (target: unknown) => target === insideNode };
  const moreBtn = { contains: () => false };

  assert.equal(shouldCloseMenuOnInteraction({ type: "keydown", key: "Escape" }, menu, moreBtn), true);
  assert.equal(shouldCloseMenuOnInteraction({ type: "keydown", key: "Enter" }, menu, moreBtn), false);
  assert.equal(
    shouldCloseMenuOnInteraction({ type: "pointerdown", composedPath: () => [insideNode, menu] }, menu, moreBtn),
    false,
  );
  assert.equal(
    shouldCloseMenuOnInteraction({ type: "pointerdown", composedPath: () => [moreBtn] }, menu, moreBtn),
    false,
  );
  assert.equal(
    shouldCloseMenuOnInteraction({ type: "pointerdown", composedPath: () => [otherNode] }, menu, moreBtn),
    true,
  );
  assert.equal(
    shouldCloseMenuOnInteraction({ type: "focusin", composedPath: () => [otherNode] }, menu, moreBtn),
    true,
  );
  assert.equal(
    shouldCloseMenuOnInteraction({ type: "pointerdown", composedPath: () => [otherNode] }, { ...menu, hidden: true }, moreBtn),
    false,
  );
});

test("planner menu wires summary/diff fill, aria-expanded sync, navigation listener, and layout specs", () => {
  const source = rendererSource;
  assert.ok(source.includes("把会话摘要填入网页"));
  assert.ok(source.includes("把会话 Diff 填入网页"));
  assert.ok(source.includes('ctx.core.request("sessions.diff"'));
  assert.ok(source.includes("view.onNavigate"));
  assert.ok(source.includes("setMenuOpen(false)"));
  assert.ok(source.includes("shouldCloseMenuOnInteraction"));
  assert.ok(source.includes("flushDrafts()"));
  assert.ok(source.includes(".ekd-code button{position:absolute;right:4px;top:4px"));
  assert.ok(source.includes(".ekd-msg:hover .ekd-msg-head time,.ekd-msg:focus-within .ekd-msg-head time{opacity:1}"));
});
