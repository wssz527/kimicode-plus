import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { actionAllowed, createBridge } from "../src/plugin-runtime/bridge.js";
import { createPluginRuntime } from "../src/plugin-runtime/runtime.js";

interface SchedulerModule {
  PLUGIN_ID: string;
  SCHEDULER_ICON: string;
  READONLY_TEMPLATES: { id: string; label: string; name: string; prompt: string }[];
  PERMISSION_MODES: { value: string; label: string; note: string }[];
  RUN_STATUS_LABELS: Record<string, string>;
  formatModelOptionLabel(model: unknown): string;
  effortOptionsForModel(model: unknown): { value: string; label: string }[];
  keepAliveStatusText(task: unknown): string;
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
    ["manual", "yolo", "auto"],
  );
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
