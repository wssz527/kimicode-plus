import test from "node:test";
import assert from "node:assert/strict";
import { actionAllowed, createBridge } from "../src/plugin-runtime/bridge.js";
import { Scope } from "../src/plugin-runtime/scope.js";
import { createPluginRuntime, defaultTheme } from "../src/plugin-runtime/runtime.js";
import { pluginHostBridge, syncPluginHost } from "../src/plugin-runtime/host.js";
import { entryPath } from "../src/plugin-runtime/loader.js";
import type { RuntimeRequest } from "../src/plugin-runtime/context.js";

/** A transport that records what the runtime actually sent to the core. */
function recordingTransport(respond: (path: string, body: unknown) => unknown = () => ({ value: {} })) {
  const calls: Array<{ path: string; body: unknown }> = [];
  const request: RuntimeRequest = async (path, body) => {
    calls.push({ path, body });
    return respond(path, body);
  };
  return { request, calls };
}

test("the runtime refuses an action the plugin was not granted", async () => {
  const { request, calls } = recordingTransport();
  const bridge = createBridge({ token: "t", granted: ["storage"], request });
  await assert.rejects(() => bridge.call("config.save", {}), /PLUGIN_CAPABILITY_DENIED|没有执行/);
  // The refusal happens in the runtime, so nothing reached the core.
  assert.equal(calls.length, 0);
});

test("an action missing from the capability table is refused, not forwarded", () => {
  assert.equal(actionAllowed("root.everything", ["config:write", "storage"]), false);
  assert.equal(actionAllowed("config.save", ["config:write"]), true);
  assert.equal(actionAllowed("config.save", ["config:read"]), false);
});

test("network actions require a declared net host", () => {
  assert.equal(actionAllowed("net.fetch", ["ui:sidebar"]), false);
  assert.equal(actionAllowed("net.fetch", ["net:api.example.com"]), true);
});

test("credential actions require a declared credential name", () => {
  assert.equal(actionAllowed("credentials.list", ["storage"]), false);
  assert.equal(actionAllowed("credentials.save", ["credentials:translator"]), true);
  assert.equal(actionAllowed("credentials.save", ["net:api.example.com"]), false);
});

test("a granted storage call reaches the runtime route with the token", async () => {
  const { request, calls } = recordingTransport(() => ({ value: { saved: true } }));
  const bridge = createBridge({ token: "secret-token", granted: ["storage"], request });
  await bridge.call("storage.set", { key: "k", value: 1 });
  assert.equal(calls.length, 1);
  assert.equal(calls[0].path, "/plugin-runtime/storage.set");
  assert.deepEqual(calls[0].body, { token: "secret-token", params: { key: "k", value: 1 } });
});

test("a scope releases in reverse order and one failure does not strand the rest", () => {
  const order: string[] = [];
  const scope = new Scope();
  scope.add(() => order.push("first"));
  scope.add(() => {
    throw new Error("boom");
  });
  scope.add(() => order.push("last"));
  scope.dispose();
  assert.deepEqual(order, ["last", "first"]);
});

test("work registered after disposal runs immediately instead of leaking", () => {
  let ran = false;
  const scope = new Scope();
  scope.dispose();
  scope.add(() => {
    ran = true;
  });
  assert.equal(ran, true);
});

test("mounting an ungranted slot is refused by the runtime", () => {
  const runtime = createPluginRuntime({ slots: {}, request: async () => ({ value: {} }) });
  return runtime
    .activate({ activate: context => void context.ui.mount("sidebar", () => {}) }, {
      id: "test.plugin",
      version: "1.0.0",
      granted: [],
      token: "t",
    })
    .then(
      () => assert.fail("an ungranted slot was mounted"),
      error => {
        assert.match(String(error.message), /没有挂载 sidebar 的能力/);
        runtime.dispose();
      },
    );
});

test("a granted slot mounts into the provided element and unmount releases it", async () => {
  const host = fakeElement();
  const runtime = createPluginRuntime({ slots: { sidebar: () => host }, request: async () => ({ value: {} }) });
  await runtime.activate(
    {
      activate: context => {
        context.ui.mount("sidebar", element => {
          const row = element.ownerDocument.createElement("span") as {
            textContent: string;
            dataset: Record<string, string>;
            remove(): void;
          };
          row.textContent = "hello";
          element.append(row as never);
          row.dataset.ekdpPlugin = "test.plugin";
          return { dispose: () => row.remove() };
        });
      },
    },
    { id: "test.plugin", version: "1.0.0", granted: ["ui:sidebar"], token: "t" },
  );
  assert.equal(host.children.length, 1);
  runtime.deactivate("test.plugin");
  assert.equal(host.children.length, 0);
});

test("plugin entry paths cannot escape the plugin directory", () => {
  assert.throws(() => entryPath("../b/renderer.js"), /相对路径/);
  assert.throws(() => entryPath("/etc/passwd.js"), /相对路径/);
  assert.throws(() => entryPath("https://evil.test/x.js"), /相对路径/);
  assert.throws(() => entryPath("sub\\renderer.js"), /相对路径/);
  assert.equal(entryPath("sub/renderer.js"), "sub/renderer.js");
});

/**
 * A DOM-free stand-in for the one element shape the slot tests need. The runtime
 * only appends its own container and removes it, so a full DOM is unnecessary
 * and casting keeps the fake honest about being a test double.
 */
function fakeElement() {
  const children: FakeNode[] = [];
  const element = {
    ownerDocument: {
      createElement(): HTMLElement {
        const node: FakeNode = {
          dataset: {},
          className: "",
          // A real DOM child carries its ownerDocument, and the runtime relies on
          // it to create the plugin's container.
          ownerDocument: element.ownerDocument,
          append() {},
          remove() {
            const index = children.indexOf(node);
            if (index >= 0) children.splice(index, 1);
          },
        };
        return node as unknown as HTMLElement;
      },
    },
    append(node: HTMLElement) {
      children.push(node as unknown as FakeNode);
    },
    replaceChildren() {
      children.splice(0, children.length);
    },
    children,
  };
  return element;
}

interface FakeNode {
  dataset: Record<string, string>;
  className: string;
  ownerDocument: { createElement(tag: string): HTMLElement };
  append(child: HTMLElement): void;
  remove(): void;
}

test("disposing one mount removes its container and leaves the other mount alone", async () => {
  const host = fakeElement();
  let first: { dispose(): void } | null = null;
  let second: { dispose(): void } | null = null;
  const runtime = createPluginRuntime({ slots: { sidebar: () => host }, request: async () => ({ value: {} }) });
  await runtime.activate(
    {
      activate: context => {
        first = context.ui.mount("sidebar", () => {});
        second = context.ui.mount("sidebar", () => {});
      },
    },
    { id: "test.plugin", version: "1.0.0", granted: ["ui:sidebar"], token: "t" },
  );
  assert.equal(host.children.length, 2);
  first!.dispose();
  assert.equal(host.children.length, 1, "a released mount left its container behind");
  second!.dispose();
  assert.equal(host.children.length, 0);
  runtime.dispose();
});

test("deactivating a plugin removes every container it mounted", async () => {
  const host = fakeElement();
  const runtime = createPluginRuntime({ slots: { sidebar: () => host, thinking: () => host }, request: async () => ({ value: {} }) });
  await runtime.activate(
    {
      activate: context => {
        context.ui.mount("sidebar", () => {});
        context.ui.mount("thinking", () => {});
      },
    },
    { id: "test.plugin", version: "1.0.0", granted: ["ui:sidebar", "ui:thinking"], token: "t" },
  );
  assert.equal(host.children.length, 2);
  runtime.deactivate("test.plugin");
  assert.equal(host.children.length, 0);
  runtime.dispose();
});

test("the settings module mounts into the caller's container and releases it", async () => {
  const host = fakeElement();
  const runtime = createPluginRuntime({ slots: {}, request: async () => ({ value: {} }) });
  const active = await runtime.activate(
    { activate: () => {} },
    { id: "test.plugin", version: "1.0.0", granted: ["ui:settings"], token: "t" },
  );
  const page = fakeElement();
  const release = await active.mountSettings(
    {
      renderSettings: (_context, element) => {
        element.append((element.ownerDocument as unknown as { createElement(tag: string): HTMLElement }).createElement("p"));
        return () => {};
      },
    },
    page as unknown as HTMLElement,
  );
  assert.equal(page.children.length, 1);
  release.dispose();
  assert.equal(page.children.length, 0, "closing the settings page left nodes behind");
  runtime.dispose();
  assert.equal(host.children.length, 0);
});

test("an ungranted settings module is refused instead of mounted", async () => {
  const runtime = createPluginRuntime({ slots: {}, request: async () => ({ value: {} }) });
  const active = await runtime.activate({ activate: () => {} }, { id: "test.plugin", version: "1.0.0", granted: [], token: "t" });
  const page = fakeElement();
  await assert.rejects(
    () => active.mountSettings({ renderSettings: () => {} }, page as unknown as HTMLElement),
    /没有提供设置页的能力/,
  );
  runtime.dispose();
});

test("the runtime runs one shared event loop across plugins, not one per plugin", async () => {
  let inFlight = 0;
  let peakInFlight = 0;
  const seenTokens = new Set<string>();
  const runtime = createPluginRuntime({
    slots: {},
    request: async (path, body) => {
      if (path !== "/plugin-runtime/events.poll") return { value: {} };
      const token = (body as { token: string }).token;
      inFlight += 1;
      peakInFlight = Math.max(peakInFlight, inFlight);
      try {
        // The real core long-polls; the double must too, or the loop spins.
        await new Promise(resolve => setTimeout(resolve, 120));
        return { value: { events: [], next: 1 } };
      } finally {
        inFlight -= 1;
        // Recorded after the poll returns, so the observation is "this plugin was
        // polled", not "this plugin happened to be first in the round".
        seenTokens.add(token);
      }
    },
  });
  try {
    const listener = () => {};
    await runtime.activate(
      { activate: context => { context.events.on("cache", listener); context.events.on("automation", listener); } },
      { id: "plugin.one", version: "1.0.0", granted: ["metrics:read"], token: "token-one" },
    );
    await runtime.activate(
      { activate: context => { context.events.on("cache", listener); context.events.on("session:s1", listener); } },
      { id: "plugin.two", version: "1.0.0", granted: ["metrics:read", "sessions:read"], token: "token-two" },
    );
    await new Promise(resolve => setTimeout(resolve, 400));
    // Four subscriptions across two plugins, but never more than one poll in
    // flight at a time and never more than one loop: the pump serializes.
    assert.equal(peakInFlight, 1, `the shared loop had ${peakInFlight} concurrent polls`);
    assert.equal(runtime.eventLoops(), 1, "more than one event loop is running");
    assert.equal(seenTokens.size, 2, "the loop did not cover both plugins");
  } finally {
    runtime.dispose();
  }
  assert.equal(runtime.eventLoops(), 0, "dispose left a loop running");
});

test("deactivating one plugin leaves the other plugin's event subscription working", async () => {
  const delivered: Array<{ token: string; value: unknown }> = [];
  let releaseTwo: (() => void) | null = null;
  const runtime = createPluginRuntime({
    slots: {},
    request: async (path, body) => {
      if (path !== "/plugin-runtime/events.poll") return { value: {} };
      // The core long-polls, so the double waits too; a poll that answers
      // instantly would make the shared loop spin.
      await new Promise(resolve => setTimeout(resolve, 60));
      const token = (body as { token: string }).token;
      const scopes = (body as { params?: { scopes?: string[] } }).params?.scopes ?? [];
      return { value: { events: [{ sequence: 1, scope: scopes[0], value: token }], next: 2 } };
    },
  });
  await runtime.activate(
    {
      activate: context => {
        context.events.on("cache", value => delivered.push({ token: "one", value }));
      },
    },
    { id: "plugin.one", version: "1.0.0", granted: ["metrics:read"], token: "token-one" },
  );
  try {
    await runtime.activate(
      {
        activate: context => {
          const disposable = context.events.on("cache", value => delivered.push({ token: "two", value }));
          releaseTwo = () => disposable.dispose();
        },
      },
      { id: "plugin.two", version: "1.0.0", granted: ["metrics:read"], token: "token-two" },
    );
    await new Promise(resolve => setTimeout(resolve, 200));
    runtime.deactivate("plugin.one");
    const afterOne = delivered.length;
    await new Promise(resolve => setTimeout(resolve, 400));
    const later = delivered.slice(afterOne);
    assert.ok(later.length > 0, "deactivating one plugin stopped the other plugin's events");
    assert.ok(later.every(item => item.token === "two"), "a deactivated plugin still received events");
    assert.ok(releaseTwo, "the surviving subscription was never registered");
  } finally {
    runtime.dispose();
  }
});

test("the last plugin stops the shared loop and a later activation restarts one loop", async () => {
  let polls = 0;
  const runtime = createPluginRuntime({
    slots: {},
    request: async path => {
      if (path === "/plugin-runtime/events.poll") {
        polls += 1;
        await new Promise(resolve => setTimeout(resolve, 50));
        return { value: { events: [], next: polls } };
      }
      return { value: {} };
    },
  });
  const waitForLoop = async (expected: number) => {
    const start = Date.now();
    while (runtime.eventLoops() !== expected) {
      if (Date.now() - start > 1500) throw new Error(`event loop did not become ${expected}`);
      await new Promise(resolve => setTimeout(resolve, 10));
    }
  };
  const install = { id: "plugin.one", version: "1.0.0", granted: ["metrics:read"], token: "t" };
  try {
    await runtime.activate({ activate: context => { context.events.on("cache", () => {}); } }, install);
    await waitForLoop(1);
    runtime.deactivate("plugin.one");
    await waitForLoop(0);
    await runtime.activate({ activate: context => { context.events.on("cache", () => {}); } }, install);
    await waitForLoop(1);
    assert.ok(polls >= 2, "reactivation did not resume polling");
  } finally {
    runtime.dispose();
  }
  assert.equal(runtime.eventLoops(), 0);
});

test("a throwing listener only removes its own subscription", async () => {
  let healthy = 0;
  const runtime = createPluginRuntime({
    slots: {},
    request: async (path, body) => {
      if (path !== "/plugin-runtime/events.poll") return { value: {} };
      await new Promise(resolve => setTimeout(resolve, 60));
      const scopes = (body as { params?: { scopes?: string[] } }).params?.scopes ?? [];
      return { value: { events: [{ sequence: 1, scope: scopes[0], value: 1 }], next: 2 } };
    },
  });
  await runtime.activate(
    {
      activate: context => {
        context.events.on("cache", () => { throw new Error("boom"); });
        context.events.on("cache", () => { healthy += 1; });
      },
    },
    { id: "plugin.one", version: "1.0.0", granted: ["metrics:read"], token: "t" },
  );
  await new Promise(resolve => setTimeout(resolve, 250));
  runtime.dispose();
  assert.ok(healthy > 0, "a throwing listener stopped the healthy one");
});

test("storage values come back as the value that was stored, not its JSON text", async () => {
  const runtime = createPluginRuntime({
    slots: {},
    request: async (path, body) => {
      if (path !== "/plugin-runtime/storage.get") return { value: {} };
      const key = (body as { params: { key: string } }).params.key;
      if (key === "missing") return { value: { found: false, value: null } };
      // The core returns the stored JSON text verbatim.
      return { value: { found: true, value: key === "count" ? "3" : JSON.stringify({ ok: true }) } };
    },
  });
  let read: { count: unknown; object: unknown; missing: unknown } | null = null;
  await runtime.activate(
    {
      activate: async context => {
        read = {
          count: await context.storage.get("count"),
          object: await context.storage.get("object"),
          missing: await context.storage.get("missing"),
        };
      },
    },
    { id: "test.plugin", version: "1.0.0", granted: ["storage"], token: "t" },
  );
  runtime.dispose();
  assert.equal(read!.count, 3);
  assert.deepEqual(read!.object, { ok: true });
  assert.equal(read!.missing, undefined);
});

test("a plugin that mounts and then throws leaves nothing behind", async () => {
  const host = fakeElement();
  let polls = 0;
  const runtime = createPluginRuntime({
    slots: { sidebar: () => host },
    request: async path => {
      if (path === "/plugin-runtime/events.poll") polls += 1;
      return { value: { events: [], next: 1 } };
    },
  });
  let attempts = 0;
  const broken = {
    activate: (context: Parameters<Parameters<typeof runtime.activate>[0]["activate"]>[0]) => {
      attempts += 1;
      context.ui.mount("sidebar", () => {});
      context.events.on("cache", () => {});
      throw new Error("boom after mount");
    },
  };
  for (let index = 0; index < 3; index += 1) {
    await assert.rejects(
      () => runtime.activate(broken, { id: "test.broken", version: "1.0.0", granted: ["ui:sidebar", "metrics:read"], token: "t" }),
      /boom after mount/,
    );
    // Finish only the poll that was already issued for this failed activation.
    // With no remaining subscribers, the next attempt must start a fresh loop.
    const deadline = Date.now() + 250;
    while (runtime.eventLoops() && Date.now() < deadline) await new Promise(resolve => setTimeout(resolve, 5));
    assert.equal(runtime.eventLoops(), 0, "a failed activation left the event loop running");
    assert.equal(polls, index + 1, "a failed activation left an event subscription behind");
  }
  assert.equal(host.children.length, 0, "a failed activation left its container behind");
  assert.equal(attempts, 3);
  runtime.dispose();
  assert.equal(runtime.eventLoops(), 0, "dispose left the event loop running");
});

test("the settings host bridge is absent outside the native host", () => {
  // A plain browser page has no plugin runtime; the settings panel must render
  // its explanation rather than throw while reading an undefined bridge.
  const original = (globalThis as { window?: unknown }).window;
  delete (globalThis as { window?: unknown }).window;
  try {
    assert.equal(pluginHostBridge(), null);
  } finally {
    if (original !== undefined) (globalThis as { window?: unknown }).window = original;
  }
});

test("the settings bridge resolves to the host's release function", async () => {
  const mounted: string[] = [];
  const released: string[] = [];
  // The native host builds the release as a closure (see mountPluginSettings in
  // internal/desktop/assets/renderer.js), so the bridge must be typed and used as
  // a function. Treating it as an object with .dispose() crashed the page.
  const bridge = {
    sync: async () => {},
    list: () => [],
    mountSettings: async (id: string) => {
      mounted.push(id);
      return () => released.push(id);
    },
  };
  const scope = globalThis as { window?: { __ekdPlugins?: typeof bridge } };
  const original = scope.window;
  scope.window = { __ekdPlugins: bridge };
  try {
    const found = pluginHostBridge();
    assert.ok(found);
    const release = await found.mountSettings("test.plugin", {} as HTMLElement);
    assert.equal(typeof release, "function");
    release();
    assert.deepEqual(mounted, ["test.plugin"]);
    assert.deepEqual(released, ["test.plugin"]);
  } finally {
    if (original === undefined) delete scope.window;
    else scope.window = original;
  }
});

test("settings actions await sidebar synchronization and retain a refresh failure", async () => {
  const scope = globalThis as { window?: { __ekdPlugins?: { sync: () => Promise<void> } } };
  const original = scope.window;
  let finish = () => {};
  scope.window = {
    __ekdPlugins: {
      sync: () => new Promise<void>(resolve => { finish = resolve; }),
    },
  };
  try {
    let settled = false;
    const pending = syncPluginHost().then(result => { settled = true; return result; });
    await Promise.resolve();
    assert.equal(settled, false, "a sidebar refresh was treated as complete before its Promise resolved");
    finish();
    assert.equal(await pending, null);
    scope.window = { __ekdPlugins: { sync: async () => { throw new Error("host unavailable"); } } };
    assert.equal(await syncPluginHost(), "host unavailable");
  } finally {
    if (original === undefined) delete scope.window;
    else scope.window = original;
  }
});

test("a failed activation is reported with a phase code the host can treat as fatal", async () => {
  const reported: Array<{ id: string; code?: string; message: string }> = [];
  const runtime = createPluginRuntime({
    slots: { sidebar: () => fakeElement() },
    request: async () => ({ value: {} }),
    onError: (id, error) => {
      reported.push({
        id,
        code: (error as { code?: string }).code,
        message: error instanceof Error ? error.message : String(error),
      });
    },
  });
  await assert.rejects(
    () =>
      runtime.activate(
        {
          activate: () => {
            throw new Error("fixture: activation always fails");
          },
        },
        { id: "test.throws", version: "1.0.0", granted: ["ui:sidebar"], token: "t" },
      ),
    /activation always fails/,
  );
  assert.equal(reported.length, 1, "the activation failure was not reported once");
  assert.equal(reported[0].id, "test.throws");
  assert.equal(
    reported[0].code,
    "PLUGIN_ACTIVATION_FAILED",
    "the host cannot tell a failed activation from a later mount error",
  );
  // Nothing was registered as active, so the plugin cannot be disposed twice or
  // keep a surface it never finished mounting.
  assert.equal(runtime.get("test.throws"), null);
  runtime.dispose();
});

test("a mount failure inside an activation is tagged with its own phase", async () => {
  const codes: string[] = [];
  const runtime = createPluginRuntime({
    slots: { sidebar: () => fakeElement() },
    request: async () => ({ value: {} }),
    onError: (_id, error) => codes.push((error as { code?: string }).code ?? "none"),
  });
  // A mount that throws propagates out of activate as well, so the assertion has
  // to let the rejection through and check what was reported alongside it.
  await assert.rejects(
    () =>
      runtime.activate(
        {
          activate: context => {
            context.ui.mount("sidebar", () => {
              throw new Error("render crashed");
            });
          },
        },
        { id: "test.mount", version: "1.0.0", granted: ["ui:sidebar"], token: "t" },
      ),
    /render crashed/,
  );
  // Exactly one report: the mount phase already named the failure, so activating
  // the same error again would count one broken render as two faults.
  assert.deepEqual(codes, ["PLUGIN_MOUNT_FAILED"]);
  runtime.dispose();
});

test("defaultTheme follows explicit schemes and the system media query", () => {
  const names = ["document", "getComputedStyle", "matchMedia"];
  const saved = new Map(names.map(name => [name, Object.getOwnPropertyDescriptor(globalThis, name)]));
  let light = false;
  try {
    for (const [configured, systemLight, expected] of [["light", false, "light"], ["dark", true, "dark"], ["system", true, "light"], ["system", false, "dark"]] as const) {
      Object.defineProperty(globalThis, "document", { configurable: true, value: { documentElement: { dataset: { colorScheme: configured } } } });
      Object.defineProperty(globalThis, "getComputedStyle", { configurable: true, value: () => ({ getPropertyValue: () => "" }) });
      Object.defineProperty(globalThis, "matchMedia", { configurable: true, value: (query: string) => ({ matches: query === "(prefers-color-scheme: light)" ? light : !light }) });
      light = systemLight;
      assert.equal(defaultTheme().scheme, expected);
    }
  } finally {
    for (const [name, descriptor] of saved) {
      if (descriptor) Object.defineProperty(globalThis, name, descriptor);
      else delete (globalThis as Record<string, unknown>)[name];
    }
  }
});
