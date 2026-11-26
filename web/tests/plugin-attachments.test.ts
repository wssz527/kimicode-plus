import test from "node:test";
import assert from "node:assert/strict";
import { createPluginRuntime } from "../src/plugin-runtime/runtime.js";

/**
 * A dropped File reaches the core as bytes. The runtime must not fabricate a
 * path: it reads the File the user dropped and hands the bytes to the core,
 * which owns the only write. These tests use a real File so the byte path is
 * exercised, not a stub that returns a string.
 */
function droppedFiles(): File[] {
  return [
    new File([new Uint8Array([1, 2, 3, 4])], "diagram.png", { type: "image/png" }),
    new File(["hello"], "notes.txt", { type: "text/plain" }),
  ];
}

test("dropped files are staged by the core and their bytes survive the trip", async () => {
  const staged: Array<{ name: string; data: string }> = [];
  let sent: Record<string, unknown> | null = null;
  const runtime = createPluginRuntime({
    slots: {},
    request: async (path, body) => {
      const params = (body as { params?: Record<string, unknown> }).params ?? {};
      if (path === "/plugin-runtime/attachments.stage") {
        for (const file of params.files as Array<{ name: string; data: string }>) staged.push(file);
        return { value: { files: [{ path: "/data/staged/test/a/00-diagram.png", name: "diagram.png" }], batch: "/data/staged/test/a" } };
      }
      if (path === "/plugin-runtime/attachments.send") {
        sent = params;
        return { value: { sessionId: "s1" } };
      }
      return { value: {} };
    },
  });
  let result: { files: Array<{ path: string; name: string }>; batch: string } | null = null;
  await runtime.activate(
    {
      activate: async context => {
        const files = droppedFiles();
        result = await context.attachments!.stage(files);
        await context.attachments!.send({ id: "s1", text: "see attached", paths: result.files.map(file => file.path), batch: result.batch });
      },
    },
    { id: "test.plugin", version: "2.0.0", granted: ["attachments"], token: "t" },
  );
  assert.equal(staged.length, 2);
  assert.equal(staged[0]!.name, "diagram.png");
  // base64 of 0x01 0x02 0x03 0x04
  assert.equal(staged[0]!.data, "AQIDBA==");
  assert.equal(staged[1]!.name, "notes.txt");
  assert.equal(Buffer.from(staged[1]!.data, "base64").toString("utf8"), "hello");
  assert.equal(result!.batch, "/data/staged/test/a");
  assert.equal((sent as unknown as { batch: string }).batch, "/data/staged/test/a");
  runtime.dispose();
});

test("a plugin without the attachments capability never receives the API", async () => {
  const called: string[] = [];
  const runtime = createPluginRuntime({
    slots: {},
    request: async path => {
      called.push(path);
      return { value: {} };
    },
  });
  let exposed = true;
  await runtime.activate(
    { activate: context => { exposed = context.attachments !== undefined; } },
    { id: "test.plugin", version: "2.0.0", granted: ["sessions:read"], token: "t" },
  );
  assert.equal(exposed, false, "an ungranted plugin still received attachments");
  assert.deepEqual(called, []);
  runtime.dispose();
});

test("staging nothing is refused locally, before the wire", async () => {
  const called: string[] = [];
  const runtime = createPluginRuntime({
    slots: {},
    request: async path => {
      called.push(path);
      return { value: {} };
    },
  });
  await runtime.activate(
    {
      activate: async context => {
        await assert.rejects(() => context.attachments!.stage([]), { code: "PLUGIN_ATTACHMENT_EMPTY" });
      },
    },
    { id: "test.plugin", version: "2.0.0", granted: ["attachments"], token: "t" },
  );
  assert.deepEqual(called, []);
  runtime.dispose();
});

test("staging a non-file is refused instead of being turned into bytes", async () => {
  const runtime = createPluginRuntime({ slots: {}, request: async () => ({ value: {} }) });
  await runtime.activate(
    {
      activate: async context => {
        await assert.rejects(() => context.attachments!.stage(["a.txt"] as unknown as File[]), { code: "PLUGIN_ATTACHMENT_INVALID" });
      },
    },
    { id: "test.plugin", version: "2.0.0", granted: ["attachments"], token: "t" },
  );
  runtime.dispose();
});

test("a released batch reaches the core with its name", async () => {
  let released: unknown = null;
  const runtime = createPluginRuntime({
    slots: {},
    request: async (path, body) => {
      if (path === "/plugin-runtime/attachments.release") released = (body as { params?: unknown }).params;
      return { value: { released: true } };
    },
  });
  await runtime.activate(
    { activate: async context => { await context.attachments!.release("/data/staged/test/a"); } },
    { id: "test.plugin", version: "2.0.0", granted: ["attachments"], token: "t" },
  );
  assert.deepEqual(released, { batch: "/data/staged/test/a" });
  runtime.dispose();
});

/**
 * The drop handler must refuse the browser default before anything else: an
 * unhandled drop makes the web view navigate to the dropped file. Node has no
 * DataTransfer, so the event carries the one shape the runtime reads; the real
 * DataTransfer path is exercised against Electron.
 */
test("dropping files refuses the default and stages what was dropped", async () => {
  const staged: Array<{ name: string; data: string }> = [];
  const runtime = createPluginRuntime({
    slots: {},
    request: async (path, body) => {
      if (path === "/plugin-runtime/attachments.stage") {
        for (const file of (body as { params: { files: Array<{ name: string; data: string }> } }).params.files) staged.push(file);
        return { value: { files: [{ path: "/data/staged/test/b/00-diagram.png", name: "diagram.png" }], batch: "/data/staged/test/b" } };
      }
      return { value: {} };
    },
  });
  let prevented = false;
  let dropped: { batch: string } | null = null;
  const event = dropEvent(droppedFiles(), () => { prevented = true; });
  await runtime.activate(
    { activate: async context => { dropped = await context.attachments!.stageFromDrop(event); } },
    { id: "test.plugin", version: "2.0.0", granted: ["attachments"], token: "t" },
  );
  assert.equal(prevented, true, "the drop default was not refused");
  assert.equal(staged.length, 2);
  assert.equal(dropped!.batch, "/data/staged/test/b");
  runtime.dispose();
});

test("an empty drop is refused and still refuses the default", async () => {
  const runtime = createPluginRuntime({ slots: {}, request: async () => ({ value: {} }) });
  let prevented = false;
  const event = dropEvent([], () => { prevented = true; });
  await runtime.activate(
    {
      activate: async context => {
        await assert.rejects(() => context.attachments!.stageFromDrop(event), { code: "PLUGIN_ATTACHMENT_EMPTY" });
      },
    },
    { id: "test.plugin", version: "2.0.0", granted: ["attachments"], token: "t" },
  );
  assert.equal(prevented, true, "an empty drop left the browser default in place");
  runtime.dispose();
});

function dropEvent(files: File[], onPrevent: () => void): DragEvent {
  return {
    preventDefault: onPrevent,
    dataTransfer: { files } as unknown as DataTransfer,
  } as unknown as DragEvent;
}


test("picked files follow the same staging contract as dropped files", async () => {
  const staged: Array<{ name: string; data: string }> = [];
  let picked = droppedFiles()[0]!;
  let pickerOpens = 0;
  const input = new EventTarget();
  Object.assign(input, { type: "", multiple: false, click() { pickerOpens += 1; }, remove() {}, files: [picked] });
  const runtime = globalThis as { document?: unknown };
  const previous = runtime.document;
  runtime.document = { createElement: () => input };
  try {
    const plugin = createPluginRuntime({
      slots: {},
      request: async (path, body) => {
        if (path === "/plugin-runtime/attachments.stage") {
          for (const file of (body as { params: { files: Array<{ name: string; data: string }> } }).params.files) staged.push(file);
          return { value: { files: [{ path: "/data/staged/test/p/00-diagram.png", name: "diagram.png" }], batch: "/data/staged/test/p" } };
        }
        return { value: {} };
      },
    });
    let result: { batch: string } | null = null;
    await plugin.activate({
      activate: async context => {
        const pending = context.attachments!.pick();
        input.dispatchEvent(new Event("change"));
        result = await pending;
        await context.attachments!.send({ id: "s1", text: "see attached", paths: ["/data/staged/test/p/00-diagram.png"], batch: result.batch });
      },
    }, { id: "test.plugin", version: "2.0.0", granted: ["attachments"], token: "t" });
    assert.equal(staged.length, 1);
    assert.equal(staged[0]!.name, picked.name);
    assert.equal(result!.batch, "/data/staged/test/p");
    assert.equal(pickerOpens, 1, "pick did not open the renderer file selector");
    picked = new File(["replacement"], "other.txt", { type: "text/plain" });
    await plugin.activate({ activate: async context => {
      const pending = context.attachments!.pick();
      input.dispatchEvent(new Event("cancel"));
      assert.deepEqual(await pending, { files: [], batch: "" });
    } }, { id: "test.cancel", version: "2.0.0", granted: ["attachments"], token: "t" });
    assert.equal(staged.length, 1);
    assert.equal(pickerOpens, 2, "cancelling did not open a fresh selection flow");
    plugin.dispose();
  } finally {
    runtime.document = previous;
  }
});
