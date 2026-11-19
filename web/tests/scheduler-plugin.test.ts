import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { actionAllowed, createBridge } from "../src/plugin-runtime/bridge.js";
import { createPluginRuntime } from "../src/plugin-runtime/runtime.js";
import {
  ALLOWED_SCHEDULER_NOTIFY_EVENTS,
  dispatchSchedulerNotification,
  isOfficialNotificationEnabled,
  isOfficialNotificationSoundEnabled,
  shouldDispatchSchedulerNotification,
} from "../src/plugin-runtime/scheduler-notify.js";

interface SchedulerModule {
  PLUGIN_ID: string;
  SCHEDULER_ICON: string;
  READONLY_TEMPLATES: { id: string; label: string; name: string; prompt: string }[];
  PERMISSION_MODES: { value: string; label: string; note: string }[];
  RUN_STATUS_LABELS: Record<string, string>;
  FAILURE_KIND_LABELS: Record<string, string>;
  PROMPT_VARIABLES: { token: string; label: string; hint: string }[];
  formatModelOptionLabel(model: unknown): string;
  effortOptionsForModel(model: unknown): { value: string; label: string }[];
  keepAliveStatusText(task: unknown): string;
  hasPromptVariables(template: string): boolean;
  previewPromptTemplate(template: string, context?: { now?: Date; lastRunAt?: string; lastRunSummary?: string }): string;
  normalizeClock(value: string, fallback?: string): string;
  parseTimesList(timesOrText: unknown): string[];
  resolveDefaultCwd(prefillCwd: unknown, sessions: unknown[], targetSessionId?: string, activeSessionId?: string): string;
  formatSchedulePreview(draft: Record<string, unknown>): string;
  defaultDraft(prefill?: Record<string, unknown>): Record<string, unknown>;
  switchScheduleKind(draft: Record<string, unknown>, nextKind: string): Record<string, unknown>;
  buildScheduleRule(draft: Record<string, unknown>): Record<string, unknown>;
  activate(ctx: unknown): Promise<{ status: string }>;
}

const rendererSource = readFileSync("../plugins/scheduler/renderer.js", "utf8");
const scheduler = (await import(`data:text/javascript;base64,${Buffer.from(rendererSource).toString("base64")}`)) as SchedulerModule;

test("scheduler permission modes match KAP enum and exclude unsupported worktree option", () => {
  assert.deepEqual(
    scheduler.PERMISSION_MODES.map(m => m.value),
    ["manual", "auto", "yolo"],
  );
  assert.deepEqual(scheduler.PERMISSION_MODES, [
    { value: "manual", label: "始终询问", note: "工具执行与文件修改前需人工批准" },
    { value: "auto", label: "自动判断", note: "常规操作自动放行，高风险操作仍需人工批准" },
    { value: "yolo", label: "始终允许（免审批）", note: "读写文件与终端命令自动执行，仅交互式提问时暂停" },
  ]);
  assert.equal(rendererSource.includes("无人值守出现任何审批请求时绝不自动批准"), false);
  const draft = scheduler.defaultDraft();
  assert.equal("worktree" in draft, false);
  assert.equal(draft.permissionMode, "manual");
  assert.equal(scheduler.RUN_STATUS_LABELS.awaiting_approval, "等待批准");
});

test("scheduler readonly templates and trusted-types safety", () => {
  assert.ok(scheduler.READONLY_TEMPLATES.length >= 3);
  for (const tpl of scheduler.READONLY_TEMPLATES) {
    assert.ok(tpl.label.length > 0 && tpl.prompt.includes("不要"));
  }
  assert.equal(rendererSource.includes(".innerHTML"), false);
  assert.equal(rendererSource.includes(".outerHTML"), false);
  assert.equal(rendererSource.includes("insertAdjacentHTML"), false);
});

test("model label and effort options reflect capabilities and adaptive thinking", () => {
  const fullModel = {
    id: "kimi-k2.5",
    name: "Kimi K2.5",
    provider: "moonshot",
    capabilities: ["image_in", "video_in", "thinking"],
    support_efforts: ["low", "medium", "high"],
    adaptive_thinking: true,
  };
  assert.equal(scheduler.formatModelOptionLabel(fullModel), "Kimi K2.5 · moonshot · 图片/视频/思考");
  assert.deepEqual(
    scheduler.effortOptionsForModel(fullModel).map(o => o.value),
    ["", "off", "low", "medium", "high"],
  );

  const alwaysThinking = {
    id: "kimi-thinking",
    name: "Always Think",
    capabilities: ["always_thinking"],
    support_efforts: ["medium", "high"],
  };
  assert.deepEqual(
    scheduler.effortOptionsForModel(alwaysThinking).map(o => o.value),
    ["", "medium", "high"],
  );

  assert.deepEqual(scheduler.effortOptionsForModel({ id: "plain", name: "Plain" }), []);
});

test("keepAliveStatusText reports factual idle minutes without fabricating cache TTL", () => {
  assert.equal(scheduler.keepAliveStatusText({ mode: "new_session" }), "");
  assert.equal(scheduler.keepAliveStatusText({ mode: "bound_session", sessionIdleMinutes: null }), "尚无主代理请求记录");
  assert.equal(
    scheduler.keepAliveStatusText({
      mode: "bound_session",
      sessionIdleMinutes: 12,
      keepAliveMinutes: 30,
      withinKeepAlive: true,
    }),
    "距上次请求 12 分钟 · 仍在用户设置的 30 分钟保活窗口内",
  );
  assert.equal(
    scheduler.keepAliveStatusText({
      mode: "bound_session",
      sessionIdleMinutes: 45,
      keepAliveMinutes: 30,
      withinKeepAlive: false,
    }),
    "距上次请求 45 分钟 · 已超过用户设置的 30 分钟保活窗口",
  );
});

test("buildScheduleRule serializes all schedule kinds cleanly", () => {
  assert.deepEqual(
    scheduler.buildScheduleRule({ scheduleKind: "once", date: "2026-12-01", time: "10:30" }),
    { kind: "once", date: "2026-12-01", time: "10:30" },
  );
  assert.deepEqual(
    scheduler.buildScheduleRule({ scheduleKind: "hourly", minute: 15 }),
    { kind: "hourly", minute: 15 },
  );
  assert.deepEqual(
    scheduler.buildScheduleRule({ scheduleKind: "monthly", time: "08:00", daysText: "1, 15, 32", lastDay: true }),
    { kind: "monthly", time: "08:00", daysOfMonth: [1, 15], lastDay: true },
  );
  assert.deepEqual(
    scheduler.buildScheduleRule({ scheduleKind: "interval", intervalValue: 2, intervalUnit: "minutes", testMode: true }),
    { kind: "interval", intervalValue: 2, intervalUnit: "minutes", testMode: true },
  );
  assert.deepEqual(
    scheduler.buildScheduleRule({
      scheduleKind: "times",
      timesText: "09:00, 18:30",
      weekdays: [1, 3, 5],
      startDate: "2026-11-18",
      endDate: "2026-12-31",
    }),
    {
      kind: "times",
      times: ["09:00", "18:30"],
      weekdays: [1, 3, 5],
      startDate: "2026-11-18",
      endDate: "2026-12-31",
    },
  );
});

test("runtime enforces schedule:manage capability, activeId, and extended model metadata", async () => {
  assert.equal(actionAllowed("schedule.list", ["sessions:read"]), false);
  assert.equal(actionAllowed("schedule.list", ["schedule:manage"]), true);
  assert.equal(actionAllowed("schedule.save", ["schedule:manage"]), true);
  assert.equal(actionAllowed("schedule.runNow", ["schedule:manage"]), true);

  const bridgeDenied = createBridge({ token: "t", granted: ["sessions:read"], request: async () => ({ value: {} }) });
  await assert.rejects(() => bridgeDenied.call("schedule.list", {}), /PLUGIN_CAPABILITY_DENIED|没有执行/);

  let currentSession = "sess-42";
  const runtime = createPluginRuntime({
    slots: {},
    activeSession: () => currentSession,
    request: async path => {
      if (path === "/plugin-runtime/models.list") {
        return {
          value: {
            items: [{
              id: "m1",
              name: "Model 1",
              provider: "synth",
              capabilities: ["thinking"],
              support_efforts: ["low", "high"],
              adaptive_thinking: true,
            }],
          },
        };
      }
      return { value: {} };
    },
  });

  await runtime.activate(
    {
      activate: async ctx => {
        assert.equal(ctx.sessions?.activeId?.(), "sess-42");
        currentSession = "sess-99";
        assert.equal(ctx.sessions?.activeId?.(), "sess-99");
        assert.throws(() => ctx.events.on("schedule", () => {}), /PLUGIN_CAPABILITY_DENIED|没有订阅/);
        const models = await ctx.models!.list();
        assert.equal(models[0]?.provider, "synth");
        assert.equal(models[0]?.adaptive_thinking, true);
        assert.deepEqual(models[0]?.support_efforts, ["low", "high"]);
      },
    },
    { id: "test.scheduler.caps", version: "1.0.0", granted: ["sessions:read", "models:read"], token: "t" },
  );
  runtime.dispose();
});

test("scheduler activate registers sidebarNav, rightPanel, entry, and cleans up on dispose", async () => {
  const calls: string[] = [];
  let disposed = false;
  const cleanupCallbacks: Array<() => void> = [];
  const ctx = {
    core: { request: async () => ({ items: [] }) },
    events: {
      on: (scope: string) => {
        calls.push(`events:${scope}`);
        return { dispose: () => { calls.push(`events:dispose:${scope}`); } };
      },
    },
    sidebarNav: {
      add: (def: { id: string; title: string }) => {
        calls.push(`sidebarNav:${def.id}:${def.title}`);
        return { dispose: () => {} };
      },
    },
    mainArea: {
      mount: () => ({ dispose: () => {} }),
    },
    rightPanel: {
      mount: (def: { title: string }) => {
        calls.push(`rightPanel:${def.title}`);
        return { open: () => {}, dispose: () => { disposed = true; } };
      },
    },
    entry: {
      add: (def: { id: string; title: string }) => {
        calls.push(`entry:${def.id}:${def.title}`);
        return { dispose: () => {} };
      },
    },
    sessions: {
      activeId: () => "sess-1",
      open: async () => {},
    },
    models: {
      list: async () => [],
    },
    onDispose: (fn: () => void) => {
      cleanupCallbacks.push(fn);
    },
  };

  const res = await scheduler.activate(ctx);
  assert.equal(res.status, "active");
  assert.deepEqual(calls, [
    "events:schedule",
    "sidebarNav:scheduler:定时任务",
    "rightPanel:定时任务",
    "entry:scheduler-session:定时任务",
  ]);
  for (const fn of cleanupCallbacks) fn();
  assert.equal(disposed, true);
  assert.ok(calls.includes("events:dispose:schedule"));
});

test("times schedule keeps weekdays empty by default and switches cleanly with weekly", () => {
  const timesDraft = scheduler.defaultDraft({ schedule: { kind: "times", times: ["08:30", "17:45"] } });
  assert.deepEqual(timesDraft.weekdays, []);
  assert.deepEqual(scheduler.buildScheduleRule(timesDraft), {
    kind: "times",
    times: ["08:30", "17:45"],
  });

  // Switching times -> weekly applies default [1], and switching back to times does not keep [1].
  scheduler.switchScheduleKind(timesDraft, "weekly");
  assert.deepEqual(timesDraft.weekdays, [1]);
  scheduler.switchScheduleKind(timesDraft, "times");
  assert.deepEqual(timesDraft.weekdays, []);

  // Starting from weekly default [1] and switching to times does not force [1] into times.
  const weeklyDraft = scheduler.defaultDraft({ schedule: { kind: "weekly" } });
  assert.deepEqual(weeklyDraft.weekdays, [1]);
  scheduler.switchScheduleKind(weeklyDraft, "times");
  assert.deepEqual(weeklyDraft.weekdays, []);
  scheduler.switchScheduleKind(weeklyDraft, "weekly");
  assert.deepEqual(weeklyDraft.weekdays, [1]);

  // Explicit weekdays in times are preserved across switches.
  const customTimes = scheduler.defaultDraft({ schedule: { kind: "times", times: ["09:00"], weekdays: [2, 4] } });
  scheduler.switchScheduleKind(customTimes, "weekly");
  assert.deepEqual(customTimes.weekdays, [2, 4]);
  scheduler.switchScheduleKind(customTimes, "times");
  assert.deepEqual(customTimes.weekdays, [2, 4]);
});

test("scheduler opens mainArea in page mode and exposes back-to-chat exit", async () => {
  const mountDefs: Array<{ mode?: string }> = [];
  let mainDisposed = 0;
  let sidebarOpen: (() => void) | undefined;
  const ctx = {
    core: { request: async () => ({ items: [] }) },
    events: { on: () => ({ dispose: () => {} }) },
    sidebarNav: {
      add: (def: { open: () => void }) => {
        sidebarOpen = def.open;
        return { dispose: () => {} };
      },
    },
    mainArea: {
      mount: (def: { mode?: string }) => {
        mountDefs.push({ mode: def.mode });
        return {
          setWidth: () => {},
          width: () => 460,
          dispose: () => { mainDisposed += 1; },
        };
      },
    },
    sessions: { activeId: () => "", open: async () => {} },
    models: { list: async () => [] },
    onDispose: () => {},
  };

  await scheduler.activate(ctx);
  assert.ok(sidebarOpen);
  sidebarOpen!();
  assert.deepEqual(mountDefs, [{ mode: "page" }]);
  assert.equal(rendererSource.includes("返回聊天"), true);
  assert.equal(mainDisposed, 0);
});

test("formatSchedulePreview formats natural language plans and omits validity for once", () => {
  assert.equal(
    scheduler.formatSchedulePreview({ scheduleKind: "daily", time: "09:00" }),
    "执行计划：每天 09:00 自动运行 · 立即生效，长期持续有效",
  );
  assert.equal(
    scheduler.formatSchedulePreview({
      scheduleKind: "weekly",
      time: "09:30",
      weekdays: [5, 1, 3],
    }),
    "执行计划：每周一、周三、周五 09:30 自动运行 · 立即生效，长期持续有效",
  );
  assert.equal(
    scheduler.formatSchedulePreview({
      scheduleKind: "times",
      timesText: "18:00, 09:00, 14:00, 09:00",
      weekdays: [],
      startDate: "2026-11-20",
    }),
    "执行计划：每天 09:00、14:00、18:00 自动运行 · 2026-11-20 起生效，长期有效",
  );
  assert.equal(
    scheduler.formatSchedulePreview({
      scheduleKind: "interval",
      intervalValue: 120,
      intervalUnit: "minutes",
    }),
    "执行计划：每隔 120 分钟自动运行（每 2 小时） · 立即生效，长期持续有效",
  );
  assert.equal(
    scheduler.formatSchedulePreview({
      scheduleKind: "once",
      date: "2026-11-20",
      time: "14:30",
      startDate: "2026-11-01",
      endDate: "2026-12-31",
    }),
    "执行计划：2026-11-20 14:30 单次运行，执行后自动停用",
  );
  assert.deepEqual(
    scheduler.buildScheduleRule({
      scheduleKind: "once",
      date: "2026-11-20",
      time: "14:30",
      startDate: "2026-11-01",
      endDate: "2026-12-31",
    }),
    { kind: "once", date: "2026-11-20", time: "14:30" },
  );
  assert.deepEqual(scheduler.parseTimesList("18:00, 9:5, 18:00, invalid, 08:30"), ["08:30", "09:05", "18:00"]);
});

test("resolveDefaultCwd prefers explicit cwd, then active session, then first session", () => {
  const sessions = [
    { id: "s1", cwd: "/tmp/first" },
    { id: "s2", cwd: "/tmp/active" },
  ];
  assert.equal(scheduler.resolveDefaultCwd("/tmp/explicit", sessions, "", "s2"), "/tmp/explicit");
  assert.equal(scheduler.resolveDefaultCwd("", sessions, "s1", "s2"), "/tmp/first");
  assert.equal(scheduler.resolveDefaultCwd("", sessions, "", "s2"), "/tmp/active");
  assert.equal(scheduler.resolveDefaultCwd("", sessions, "", "missing"), "/tmp/first");
  assert.equal(scheduler.resolveDefaultCwd("", [], "", ""), "");
});

test("scheduler dialog integrates pickDirectory, hides validity for once, and shows no-drag header", async () => {
  class FakeNode {
    tagName: string;
    className = "";
    textContent = "";
    value = "";
    checked = false;
    disabled = false;
    open = false;
    style: Record<string, string> = {};
    dataset: Record<string, string> = {};
    attributes: Record<string, string> = {};
    children: FakeNode[] = [];
    listeners: Record<string, Array<(e: unknown) => void>> = {};
    shadowRoot: FakeNode | null = null;
    isConnected = true;

    constructor(tagName: string) {
      this.tagName = tagName;
    }
    setAttribute(name: string, val: string) {
      this.attributes[name] = val;
    }
    getAttribute(name: string) {
      return this.attributes[name];
    }
    append(...nodes: Array<FakeNode | string>) {
      for (const n of nodes) {
        if (typeof n === "string") {
          const txt = new FakeNode("#text");
          txt.textContent = n;
          this.children.push(txt);
        } else if (n) {
          this.children.push(n);
        }
      }
    }
    replaceChildren(...nodes: Array<FakeNode | string>) {
      this.children = [];
      this.append(...nodes);
    }
    remove() {
      this.isConnected = false;
    }
    attachShadow() {
      this.shadowRoot = new FakeNode("#shadow");
      return this.shadowRoot;
    }
    addEventListener(type: string, fn: (e: unknown) => void) {
      if (!this.listeners[type]) this.listeners[type] = [];
      this.listeners[type].push(fn);
    }
    dispatchEvent(type: string, event: Record<string, unknown> = {}) {
      for (const fn of this.listeners[type] || []) {
        fn({ target: this, ...event });
      }
    }
    showModal() {
      this.open = true;
    }
    close() {
      this.open = false;
    }
  }

  function findAll(root: FakeNode, pred: (n: FakeNode) => boolean): FakeNode[] {
    const out: FakeNode[] = [];
    const visit = (node: FakeNode) => {
      if (pred(node)) out.push(node);
      if (node.shadowRoot) visit(node.shadowRoot);
      for (const child of node.children) visit(child);
    };
    visit(root);
    return out;
  }

  function allText(node: FakeNode): string {
    const parts: string[] = [];
    const visit = (n: FakeNode) => {
      if (n.textContent) parts.push(n.textContent);
      if (n.shadowRoot) visit(n.shadowRoot);
      for (const c of n.children) visit(c);
    };
    visit(node);
    return parts.join(" ");
  }

  const fakeHead = new FakeNode("head");
  const fakeBody = new FakeNode("body");
  const fakeDoc = {
    head: fakeHead,
    body: fakeBody,
    createElement: (tag: string) => new FakeNode(tag),
    createElementNS: (_ns: string, tag: string) => new FakeNode(tag),
    createTextNode: (text: string) => {
      const t = new FakeNode("#text");
      t.textContent = text;
      return t;
    },
  };

  const prevDocument = (globalThis as Record<string, unknown>).document;
  (globalThis as Record<string, unknown>).document = fakeDoc;

  try {
    let mountedRender: ((el: FakeNode) => () => void) | null = null;
    let sidebarOpen: (() => void) | null = null;
    const pickCalls: Array<Record<string, unknown> | undefined> = [];

    const ctx = {
      core: {
        request: async (action: string) => {
          if (action === "schedule.list") return { items: [], kapConnected: true };
          if (action === "schedule.runs") return { items: [] };
          if (action === "sessions.list") {
            return {
              items: [
                { id: "sess-1", title: "Session 1", cwd: "/Users/demo/first" },
                { id: "sess-2", title: "Session 2", cwd: "/Users/demo/active" },
              ],
            };
          }
          return {};
        },
      },
      events: { on: () => ({ dispose: () => {} }) },
      sidebarNav: {
        add: (def: { open: () => void }) => {
          sidebarOpen = def.open;
          return { dispose: () => {} };
        },
      },
      mainArea: {
        mount: (def: { render: (el: FakeNode) => () => void }) => {
          mountedRender = def.render;
          return { setWidth: () => {}, width: () => 800, dispose: () => {} };
        },
      },
      ui: {
        pickDirectory: async (opts?: Record<string, unknown>) => {
          pickCalls.push(opts);
          return "/Users/demo/picked-project";
        },
      },
      sessions: { activeId: () => "sess-2", open: async () => {} },
      models: { list: async () => [] },
      onDispose: () => {},
    };

    await scheduler.activate(ctx);
    sidebarOpen!();
    const mainHost = new FakeNode("div") as FakeNode & { ownerDocument: typeof fakeDoc };
    mainHost.ownerDocument = fakeDoc;
    const cleanupMain = mountedRender!(mainHost);
    await new Promise(resolve => setTimeout(resolve, 20));

    const newTaskBtn = findAll(mainHost, n => n.tagName === "button" && allText(n).includes("+ 新建任务"))[0];
    assert.ok(newTaskBtn);
    newTaskBtn.dispatchEvent("click");

    const dialog = fakeBody.children.find(n => n.tagName === "dialog");
    assert.ok(dialog);

    const cwdInputs = findAll(dialog, n => n.tagName === "input" && n.attributes.placeholder === "/path/to/project");
    assert.equal(cwdInputs[0]?.value, "/Users/demo/active");

    const pickBtn = findAll(dialog, n => n.tagName === "button" && allText(n).includes("选择文件夹…"))[0];
    assert.ok(pickBtn);
    pickBtn.dispatchEvent("click");
    await new Promise(resolve => setTimeout(resolve, 10));
    assert.deepEqual(pickCalls, [{ title: "选择定时任务工作目录", defaultPath: "/Users/demo/active" }]);
    const updatedCwdInputs = findAll(dialog, n => n.tagName === "input" && n.attributes.placeholder === "/path/to/project");
    assert.equal(updatedCwdInputs[0]?.value, "/Users/demo/picked-project");

    // Periodic schedule shows validity range; switching to once hides it.
    assert.equal(allText(dialog).includes("有效期范围（可选）"), true);
    const onceBtn = findAll(dialog, n => n.tagName === "button" && n.className === "ekd-sd-freq-btn" && allText(n).trim() === "单次执行")[0];
    assert.ok(onceBtn);
    onceBtn.dispatchEvent("click");
    assert.equal(allText(dialog).includes("有效期范围（可选）"), false);
    assert.equal(allText(dialog).includes("单次运行，执行后自动停用"), true);

    assert.equal(rendererSource.includes("-webkit-app-region:no-drag"), true);
    cleanupMain();
  } finally {
    (globalThis as Record<string, unknown>).document = prevDocument;
  }
});

test("N5 scheduler-notify filters official-covered events, follows official switches, deduplicates, and routes clicks", () => {
  assert.deepEqual([...ALLOWED_SCHEDULER_NOTIFY_EVENTS].sort(), [
    "preflight_failed",
    "run_failed",
    "run_skipped",
  ]);

  // Official Pinia store takes precedence over localStorage; defaults to true when unset.
  const makeStorage = (entries: Record<string, string>) => ({
    getItem: (k: string) => (k in entries ? entries[k] : null),
  });
  const makeDoc = (store?: { notifyEnabled?: unknown; notifySound?: unknown }) =>
    ({
      querySelector: (sel: string) =>
        sel === "#app" && store
          ? { __vue_app__: { config: { globalProperties: { $pinia: { _s: new Map([["kimi.notifications", store]]) } } } } }
          : null,
    }) as unknown as Document;

  assert.equal(isOfficialNotificationEnabled(makeDoc(), makeStorage({})), true);
  assert.equal(isOfficialNotificationEnabled(makeDoc(), makeStorage({ "kimi-web.notify-enabled": "0" })), false);
  assert.equal(
    isOfficialNotificationEnabled(makeDoc({ notifyEnabled: { value: false } }), makeStorage({ "kimi-web.notify-enabled": "1" })),
    false,
  );
  assert.equal(
    isOfficialNotificationSoundEnabled(makeDoc({ notifySound: false }), makeStorage({ "kimi-web.notify-sound": "1" })),
    false,
  );

  // Completed / awaiting_approval / pending_question are never dispatched.
  for (const kind of ["run_completed", "turn_completed", "awaiting_approval", "pending_question", ""]) {
    const decision = shouldDispatchSchedulerNotification(
      { runId: "r1", taskId: "t1", taskName: "T", eventKind: kind, title: "x", body: "y" },
      { officialEnabled: true },
    );
    assert.equal(decision.allowed, false);
  }

  // Suppressed when official notification switch is off.
  assert.equal(
    shouldDispatchSchedulerNotification(
      { runId: "r1", taskId: "t1", taskName: "T", eventKind: "run_failed", title: "x", body: "y" },
      { officialEnabled: false },
    ).reason,
    "official_notifications_disabled",
  );

  // Dispatch, sound flag, dedupe by tag, and click routing (session vs runs).
  const created: Array<{ title: string; options?: NotificationOptions; onclick: ((this: unknown, ev: unknown) => unknown) | null; closed: boolean }> = [];
  class FakeNotification {
    static permission: NotificationPermission = "granted";
    onclick: ((this: unknown, ev: unknown) => unknown) | null = null;
    closed = false;
    constructor(public title: string, public options?: NotificationOptions) {
      created.push(this);
    }
    close() {
      this.closed = true;
    }
  }

  const seenTags = new Set<string>();
  const openedSessions: string[] = [];
  const openedRuns: Array<{ taskId: string; runId: string }> = [];
  let shown = 0;
  let focused = 0;

  const r1 = dispatchSchedulerNotification(
    {
      runId: "run_1",
      taskId: "task_1",
      taskName: "每日巡检",
      sessionId: "sess_9",
      eventKind: "run_failed",
      title: "运行失败",
      body: "超时中止",
    },
    {
      document: makeDoc({ notifyEnabled: true, notifySound: false }),
      storage: makeStorage({}),
      NotificationCtor: FakeNotification,
      seenTags,
      showWindow: () => { shown += 1; },
      focusWindow: () => { focused += 1; },
      openSession: id => { openedSessions.push(id); },
      openRuns: target => { openedRuns.push(target); },
    },
  );
  assert.equal(r1.shown, true);
  assert.equal(created.length, 1);
  assert.equal(created[0]?.options?.silent, true);
  assert.equal(created[0]?.options?.tag, "ekd-sched-run_1");

  // Duplicate tag is ignored.
  const r1Dup = dispatchSchedulerNotification(
    {
      runId: "run_1",
      taskId: "task_1",
      taskName: "每日巡检",
      eventKind: "run_failed",
      title: "运行失败",
      body: "超时中止",
    },
    {
      document: makeDoc({ notifyEnabled: true }),
      storage: makeStorage({}),
      NotificationCtor: FakeNotification,
      seenTags,
    },
  );
  assert.equal(r1Dup.shown, false);
  assert.equal(r1Dup.reason, "duplicate");

  created[0]?.onclick?.call(null, {});
  assert.deepEqual(openedSessions, ["sess_9"]);
  assert.equal(shown, 1);
  assert.equal(focused, 1);
  assert.equal(created[0]?.closed, true);

  // Without sessionId, clicking routes to openRuns.
  const r2 = dispatchSchedulerNotification(
    {
      runId: "run_2",
      taskId: "task_2",
      taskName: "预检任务",
      eventKind: "preflight_failed",
      title: "运行前校验失败",
      body: "目录不存在",
    },
    {
      document: makeDoc({ notifyEnabled: true, notifySound: true }),
      storage: makeStorage({}),
      NotificationCtor: FakeNotification,
      seenTags,
      openSession: id => { openedSessions.push(id); },
      openRuns: target => { openedRuns.push(target); },
    },
  );
  assert.equal(r2.shown, true);
  assert.equal(created[1]?.options?.silent, false);
  created[1]?.onclick?.call(null, {});
  assert.deepEqual(openedRuns, [{ taskId: "task_2", runId: "run_2" }]);
});

test("N5 prompt template preview, capability gates, openSettings, and scheduler UI workflows", async () => {
  // 1. Prompt template single-pass preview and draft defaults.
  const now = new Date(2026, 10, 19, 14, 5, 0);
  const preview = scheduler.previewPromptTemplate(
    "日期={{date}} 时间={{time}} 上次={{last_run_at}} 摘要={{last_run_summary}} 未知={{other}}",
    { now, lastRunAt: "2026-11-18T08:30:00", lastRunSummary: "包含 {{date}} 不二次展开" },
  );
  assert.equal(preview, "日期=2026-11-19 时间=14:05 上次=2026-11-18 08:30 摘要=包含 {{date}} 不二次展开 未知={{other}}");
  assert.equal(scheduler.previewPromptTemplate("{{last_run_at}}/{{last_run_summary}}", { now }), "暂无/暂无");
  assert.equal(scheduler.hasPromptVariables("hello {{date}}"), true);
  assert.equal(scheduler.hasPromptVariables("hello world"), false);

  const freshDraft = scheduler.defaultDraft();
  assert.equal(freshDraft.archiveOnRead, true);
  assert.equal(freshDraft.timeoutMinutes, 30);
  assert.equal(freshDraft.maxTurns, 0);
  assert.equal(freshDraft.confirmDirtyYolo, false);

  // 2. Runtime & bridge N5 action capabilities + openSettings bridge.
  assert.equal(actionAllowed("schedule.checkGit", ["schedule:manage"]), true);
  assert.equal(actionAllowed("schedule.archiveRun", ["schedule:manage"]), true);
  assert.equal(actionAllowed("schedule.checkGit", ["sessions:read"]), false);
  assert.equal(actionAllowed("schedule.archiveRun", ["sessions:read"]), false);

  const openedSettingsTabs: string[] = [];
  const runtime = createPluginRuntime({
    slots: {},
    openSettings: async tab => { openedSettingsTabs.push(tab ?? ""); },
    request: async () => ({ value: {} }),
  });
  await runtime.activate(
    {
      activate: async ctx => {
        await ctx.ui.openSettings?.("general");
      },
    },
    { id: "test.sched.settings", version: "1.0.0", granted: ["schedule:manage"], token: "t" },
  );
  assert.deepEqual(openedSettingsTabs, ["general"]);
  runtime.dispose();

  // 3. PluginSettings auto-attach prompt wiring for scheduler plugin.
  const pluginSettingsSrc = readFileSync("src/components/PluginSettings.tsx", "utf8");
  assert.ok(pluginSettingsSrc.includes("kimicode-plus.scheduler"));
  assert.ok(pluginSettingsSrc.includes('data-ekd-autoattach-prompt="true"'));
  assert.ok(pluginSettingsSrc.includes("如果不开启，Kimi Code 重启后，定时任务要等您手动重新接入才会运行"));
  assert.ok(pluginSettingsSrc.includes('host.request("attach.enable"'));

  // 4. End-to-end scheduler renderer DOM flow: banners, guardrails, variables, yolo dirty check, archive.
  class FakeNode {
    tagName: string;
    className = "";
    textContent = "";
    value = "";
    checked = false;
    disabled = false;
    open = false;
    style: Record<string, string> = {};
    dataset: Record<string, string> = {};
    attributes: Record<string, string> = {};
    children: FakeNode[] = [];
    listeners: Record<string, Array<(e: unknown) => void>> = {};
    shadowRoot: FakeNode | null = null;
    isConnected = true;

    constructor(tagName: string) {
      this.tagName = tagName;
    }
    setAttribute(name: string, val: string) { this.attributes[name] = val; }
    getAttribute(name: string) { return this.attributes[name]; }
    append(...nodes: Array<FakeNode | string>) {
      for (const n of nodes) {
        if (typeof n === "string") {
          const txt = new FakeNode("#text");
          txt.textContent = n;
          this.children.push(txt);
        } else if (n) {
          this.children.push(n);
        }
      }
    }
    replaceChildren(...nodes: Array<FakeNode | string>) {
      this.children = [];
      this.append(...nodes);
    }
    remove() { this.isConnected = false; }
    attachShadow() {
      this.shadowRoot = new FakeNode("#shadow");
      return this.shadowRoot;
    }
    addEventListener(type: string, fn: (e: unknown) => void) {
      if (!this.listeners[type]) this.listeners[type] = [];
      this.listeners[type].push(fn);
    }
    dispatchEvent(type: string, event: Record<string, unknown> = {}) {
      for (const fn of this.listeners[type] || []) {
        fn({ target: this, ...event });
      }
    }
    showModal() { this.open = true; }
    close() { this.open = false; }
  }

  function findAll(root: FakeNode, pred: (n: FakeNode) => boolean): FakeNode[] {
    const out: FakeNode[] = [];
    const visit = (node: FakeNode) => {
      if (pred(node)) out.push(node);
      if (node.shadowRoot) visit(node.shadowRoot);
      for (const child of node.children) visit(child);
    };
    visit(root);
    return out;
  }

  function allText(node: FakeNode): string {
    const parts: string[] = [];
    const visit = (n: FakeNode) => {
      if (n.textContent) parts.push(n.textContent);
      if (n.shadowRoot) visit(n.shadowRoot);
      for (const c of n.children) visit(c);
    };
    visit(node);
    return parts.join(" ");
  }

  const fakeHead = new FakeNode("head");
  const fakeBody = new FakeNode("body");
  const fakeDoc = {
    head: fakeHead,
    body: fakeBody,
    createElement: (tag: string) => new FakeNode(tag),
    createElementNS: (_ns: string, tag: string) => new FakeNode(tag),
    createTextNode: (text: string) => {
      const t = new FakeNode("#text");
      t.textContent = text;
      return t;
    },
  };

  const windowListeners = new Map<string, Set<(ev: unknown) => void>>();
  const fakeWindow = {
    addEventListener: (type: string, fn: (ev: unknown) => void) => {
      if (!windowListeners.has(type)) windowListeners.set(type, new Set());
      windowListeners.get(type)!.add(fn);
    },
    removeEventListener: (type: string, fn: (ev: unknown) => void) => {
      windowListeners.get(type)?.delete(fn);
    },
    dispatchEvent: (ev: { type: string; detail?: unknown }) => {
      for (const fn of windowListeners.get(ev.type) || []) fn(ev);
    },
  };

  const prevDocument = (globalThis as Record<string, unknown>).document;
  const prevWindow = (globalThis as Record<string, unknown>).window;
  (globalThis as Record<string, unknown>).document = fakeDoc;
  (globalThis as Record<string, unknown>).window = fakeWindow;

  try {
    const calls: Array<{ action: string; payload: unknown }> = [];
    const settingsTabs: string[] = [];
    const cleanups: Array<() => void> = [];
    let mountedRender: ((el: FakeNode) => () => void) | null = null;
    let sidebarOpen: (() => void) | null = null;

    const ctx = {
      core: {
        request: async (action: string, payload: unknown) => {
          calls.push({ action, payload });
          if (action === "schedule.list") {
            return {
              items: [],
              kapConnected: true,
              unreadCount: 1,
              prefs: { autoMarkRead: false, maxConcurrent: 2, minQuotaPercent: 10 },
              autoAttach: { enabled: false, available: true, platform: "darwin" },
            };
          }
          if (action === "schedule.runs") {
            return {
              items: [
                {
                  id: "run_n5",
                  taskId: "task_1",
                  taskName: "每日巡检",
                  triggerKind: "scheduled",
                  mode: "new_session",
                  sessionId: "sess_arch",
                  sessionTitle: "[定时] 每日巡检",
                  status: "failed",
                  failureKind: "preflight_failed",
                  message: "项目目录不存在。下一步：请编辑任务并重新选择有效目录。",
                  unread: true,
                  archived: false,
                  startedAt: "2026-11-19T09:00:00Z",
                },
              ],
            };
          }
          if (action === "schedule.checkGit") {
            return { isGitRepo: true, clean: false, dirtyCount: 2, sample: [" M src/app.ts", "?? temp.txt"] };
          }
          if (action === "schedule.save") return { id: "task_saved" };
          if (action === "schedule.archiveRun") return { archived: true };
          if (action === "schedule.markRead") return { unreadCount: 0, archived: true };
          if (action === "schedule.prefs") return payload;
          if (action === "sessions.list") return { items: [{ id: "s1", title: "S1", cwd: "/tmp/repo" }] };
          return {};
        },
      },
      events: { on: () => ({ dispose: () => {} }) },
      sidebarNav: {
        add: (def: { open: () => void }) => {
          sidebarOpen = def.open;
          return { dispose: () => {} };
        },
      },
      mainArea: {
        mount: (def: { render: (el: FakeNode) => () => void }) => {
          mountedRender = def.render;
          return { setWidth: () => {}, width: () => 800, dispose: () => {} };
        },
      },
      ui: {
        openSettings: async (tab?: string) => { settingsTabs.push(tab ?? ""); },
      },
      sessions: { activeId: () => "s1", open: async () => {} },
      models: { list: async () => [] },
      onDispose: (fn: () => void) => { cleanups.push(fn); },
    };

    await scheduler.activate(ctx);
    sidebarOpen!();
    const mainHost = new FakeNode("div") as FakeNode & { ownerDocument: typeof fakeDoc };
    mainHost.ownerDocument = fakeDoc;
    const cleanupMain = mountedRender!(mainHost);
    await new Promise(resolve => setTimeout(resolve, 25));

    // Auto-attach warning banner & prevent-sleep/notification strip appear with settings links.
    const mainText = allText(mainHost);
    assert.ok(mainText.includes("尚未开启「随 Kimi Code 自动接入」"));
    assert.ok(mainText.includes("运行期间是否防止休眠，取决于官方设置里的「防止休眠」"));

    const openOfficialBtn = findAll(mainHost, n => n.tagName === "button" && allText(n).includes("打开官方设置"))[0];
    assert.ok(openOfficialBtn);
    openOfficialBtn.dispatchEvent("click");
    await new Promise(resolve => setTimeout(resolve, 5));
    assert.deepEqual(settingsTabs, ["general"]);

    // Global guardrails bar saves maxConcurrent & minQuotaPercent.
    const maxConcInput = findAll(mainHost, n => n.tagName === "input" && n.attributes["aria-label"] === "全局并发上限")[0];
    const minQuotaInput = findAll(mainHost, n => n.tagName === "input" && n.attributes["aria-label"] === "额度跳过阈值（%）")[0];
    assert.ok(maxConcInput && minQuotaInput);
    maxConcInput.value = "4";
    maxConcInput.dispatchEvent("input");
    minQuotaInput.value = "15";
    minQuotaInput.dispatchEvent("input");
    const saveGuardBtn = findAll(mainHost, n => n.tagName === "button" && allText(n).trim() === "保存护栏")[0];
    assert.ok(saveGuardBtn);
    saveGuardBtn.dispatchEvent("click");
    await new Promise(resolve => setTimeout(resolve, 10));
    const prefsCall = calls.find(c => c.action === "schedule.prefs");
    assert.deepEqual(prefsCall?.payload, { maxConcurrent: 4, minQuotaPercent: 15 });

    // Dialog: variable insertion updates preview, yolo dirty repo requires confirmation, save sends N5 fields.
    const newTaskBtn = findAll(mainHost, n => n.tagName === "button" && allText(n).includes("+ 新建任务"))[0];
    newTaskBtn!.dispatchEvent("click");
    const dialog = fakeBody.children.find(n => n.tagName === "dialog");
    assert.ok(dialog);

    const dateVarBtn = findAll(dialog, n => n.tagName === "button" && n.attributes["data-ekd-var-token"] === "{{date}}")[0];
    assert.ok(dateVarBtn);
    dateVarBtn.dispatchEvent("click");
    const previewEl = findAll(dialog, n => n.attributes["data-ekd-prompt-preview"] === "true")[0];
    assert.ok(previewEl && /\d{4}-\d{2}-\d{2}/.test(previewEl.textContent));

    const nameInput = findAll(dialog, n => n.tagName === "input" && n.attributes.placeholder === "例如：每日代码变更摘要")[0];
    nameInput!.value = "N5 任务";
    nameInput!.dispatchEvent("input");

    const timeoutInput = findAll(dialog, n => n.tagName === "input" && n.attributes["aria-label"] === "单次超时（分钟）")[0];
    const maxTurnsInput = findAll(dialog, n => n.tagName === "input" && n.attributes["aria-label"] === "最大回合数")[0];
    timeoutInput!.value = "45";
    timeoutInput!.dispatchEvent("input");
    maxTurnsInput!.value = "12";
    maxTurnsInput!.dispatchEvent("input");

    const permSelect = findAll(dialog, n => n.tagName === "select" && n.children.some(c => c.value === "yolo"))[0];
    assert.ok(permSelect);
    permSelect.value = "yolo";
    permSelect.dispatchEvent("change");
    await new Promise(resolve => setTimeout(resolve, 15));

    assert.ok(allText(dialog).includes("存在 2 处未提交改动"));

    // Submitting before confirming dirty yolo is blocked client-side.
    const saveBtnBefore = findAll(dialog, n => n.tagName === "button" && allText(n).trim() === "保存")[0];
    saveBtnBefore!.dispatchEvent("click");
    await new Promise(resolve => setTimeout(resolve, 10));
    assert.equal(calls.some(c => c.action === "schedule.save"), false);
    assert.ok(allText(dialog).includes("勾选上方风险确认"));

    // Confirm dirty yolo and submit -> sends N5 fields.
    const confirmBox = findAll(dialog, n => n.tagName === "input" && n.attributes["data-ekd-confirm-dirty-yolo"] === "true")[0];
    assert.ok(confirmBox);
    confirmBox.checked = true;
    confirmBox.dispatchEvent("change");
    const saveBtnAfter = findAll(dialog, n => n.tagName === "button" && allText(n).trim() === "保存")[0];
    saveBtnAfter!.dispatchEvent("click");
    await new Promise(resolve => setTimeout(resolve, 15));

    const saveCall = calls.find(c => c.action === "schedule.save");
    assert.ok(saveCall);
    const savedPayload = saveCall.payload as Record<string, unknown>;
    assert.equal(savedPayload.archiveOnRead, true);
    assert.equal(savedPayload.timeoutMinutes, 45);
    assert.equal(savedPayload.maxTurns, 12);
    assert.equal(savedPayload.confirmDirtyYolo, true);

    // Notification jump event switches to Runs tab, highlights run, displays failureKind & archive actions.
    fakeWindow.dispatchEvent({ type: "ekd:scheduler-open-runs", detail: { taskId: "task_1", runId: "run_n5" } });
    await new Promise(resolve => setTimeout(resolve, 20));
    const runsText = allText(mainHost);
    assert.ok(runsText.includes("校验失败"));
    assert.ok(runsText.includes("[定时] 每日巡检"));
    const highlightedCard = findAll(mainHost, n => n.attributes["data-ekd-run-id"] === "run_n5")[0];
    assert.ok(highlightedCard?.className.includes("highlighted"));

    const archiveBtn = findAll(mainHost, n => n.tagName === "button" && allText(n).trim() === "归档会话")[0];
    assert.ok(archiveBtn);
    archiveBtn.dispatchEvent("click");
    await new Promise(resolve => setTimeout(resolve, 10));
    assert.deepEqual(calls.find(c => c.action === "schedule.archiveRun")?.payload, { id: "run_n5" });

    cleanupMain();
    for (const fn of cleanups) fn();
    assert.equal(windowListeners.get("ekd:scheduler-open-runs")?.size ?? 0, 0);
  } finally {
    (globalThis as Record<string, unknown>).document = prevDocument;
    (globalThis as Record<string, unknown>).window = prevWindow;
  }
});
