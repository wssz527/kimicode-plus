import test from "node:test";
import assert from "node:assert/strict";
import { createPluginRuntime } from "../src/plugin-runtime/runtime.js";

test("planner rename uses the persistent core action with the thread id", async () => {
  let request: { path: string; body: unknown } | undefined;
  const runtime = createPluginRuntime({
    slots: {},
    request: async (path, body) => {
      request = { path, body };
      return { value: { id: "thread-1", title: "新标题", model: "synthetic", status: "idle", revision: 2, messages: [] } };
    },
  });
  await runtime.activate(
    { activate: async context => { await context.planner!.rename("thread-1", "新标题"); } },
    { id: "test.plugin", version: "2.0.0", granted: ["planner:run"], token: "t" },
  );
  assert.equal(request?.path, "/plugin-runtime/planner.rename");
  assert.deepEqual((request.body as { params: unknown }).params, { id: "thread-1", title: "新标题" });
  runtime.dispose();
});

test("opening an official session goes through the authorized host surface", async () => {
  const opened: string[] = [];
  const requested: unknown[] = [];
  const runtime = createPluginRuntime({
    slots: {},
    request: async (_path, body) => {
      requested.push((body as { params: unknown }).params);
      return { value: { id: "session-1" } };
    },
    openSession: async id => { opened.push(id); },
  });
  await runtime.activate(
    { activate: async context => { await context.sessions!.open("session-1"); } },
    { id: "test.plugin", version: "2.0.0", granted: ["sessions:read"], token: "t" },
  );
  assert.deepEqual(requested, [{ id: "session-1" }]);
  assert.deepEqual(opened, ["session-1"]);
  runtime.dispose();
});

test("a plugin popout is authorized before the host opens the window", async () => {
  const calls: string[] = [];
  const windows: string[] = [];
  const runtime = createPluginRuntime({
    slots: {},
    request: async path => { calls.push(path); return { value: { pluginId: "test.plugin" } }; },
    windowHost: {
      popout: async id => { windows.push(id); },
      close: async () => { windows.push("close"); },
    },
  });
  await runtime.activate(
    { activate: async context => { await context.window!.popout(); await context.window!.close(); } },
    { id: "test.plugin", version: "2.0.0", granted: ["ui:right-panel"], token: "t" },
  );
  assert.deepEqual(calls, ["/plugin-runtime/ui.popout"]);
  assert.deepEqual(windows, ["test.plugin", "close"]);
  runtime.dispose();
});

test("a command host routes invocation without executing a caller-window substitute", async () => {
  const calls: string[] = [];
  const corePaths: string[] = [];
  const runtime = createPluginRuntime({
    slots: {},
    request: async path => {
      corePaths.push(path);
      return { value: { name: "multiSession.open", pluginId: "test.plugin", pluginName: "Plugin" } };
    },
    commandHost: {
      register: async (id, name) => { calls.push(`register:${id}:${name}`); },
      unregister: async (id, name) => { calls.push(`unregister:${id}:${name}`); },
      invoke: async (id, name, args) => {
        calls.push(`invoke:${id}:${name}:${(args as { sessionId: string }).sessionId}`);
        return { opened: true };
      },
    },
  });
  let answer: unknown;
  await runtime.activate(
    { activate: async context => {
      await context.commands!.register("multiSession.open", { run: () => ({ opened: false }) });
      answer = await context.commands!.invoke("multiSession.open", { sessionId: "s1" });
    } },
    { id: "test.plugin", version: "2.0.0", granted: ["commands"], token: "t" },
  );
  assert.deepEqual(answer, { opened: true });
  assert.deepEqual(calls, ["register:test.plugin:multiSession.open", "invoke:test.plugin:multiSession.open:s1"]);
  assert.deepEqual(corePaths, ["/plugin-runtime/commands.register"]);
  runtime.dispose();
});

test("browser open rolls back the core record when host.open rejects", async () => {
  const calls: Array<{ path: string; params: unknown }> = [];
  const runtime = createPluginRuntime({
    slots: {},
    instanceId: "win-1",
    request: async (path, body) => {
      calls.push({ path, params: (body as { params?: unknown }).params });
      if (path === "/plugin-runtime/browser.open") {
        return { value: { viewId: "web", url: "https://example.com/" } };
      }
      return { value: { closed: true } };
    },
    browserHost: {
      open: async () => { throw new Error("load failed"); },
      navigate: async () => {},
      history: async () => false,
      external: async () => {},
      bounds: async () => {},
      extract: async () => "",
      adapter: async () => null,
      close: async () => {},
    },
  });
  await assert.rejects(
    runtime.activate(
      { activate: async context => {
        assert.equal(context.plugin.surface, "host");
        await context.browser!.open("web", "https://example.com/");
      } },
      { id: "test.plugin", version: "2.0.0", granted: ["browser:view"], token: "t" },
    ),
    /load failed/,
  );
  assert.deepEqual(calls, [
    { path: "/plugin-runtime/browser.open", params: { viewId: "web", url: "https://example.com/", instanceId: "win-1" } },
    { path: "/plugin-runtime/browser.close", params: { viewId: "web", instanceId: "win-1" } },
  ]);
  runtime.dispose();
});
