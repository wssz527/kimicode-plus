import test from "node:test";
import assert from "node:assert/strict";
import { createSessionWatchBinding, isBridgeCredentialError, BRIDGE_RECONNECT_TEXT, type SessionWatchOutcome } from "../src/api.js";

interface View {
  session: { id: string };
  messages: { id: string; text: string }[];
}

const VIEW: View = { session: { id: "s1" }, messages: [{ id: "m1", text: "已读到的历史" }] };

function harness(outcome: () => Promise<SessionWatchOutcome<View> | undefined>, read?: () => Promise<View | undefined>) {
  const views: View[] = [];
  const notes: Array<string | null> = [];
  const errors: string[] = [];
  let unwatches = 0;
  let attached = true;
  const binding = createSessionWatchBinding<View>({
    watch: outcome,
    read: read ?? (async () => VIEW),
    unwatch: async () => {
      unwatches += 1;
    },
    onView: view => views.push(view),
    onNote: note => notes.push(note),
    onError: message => errors.push(message),
    describeError: error => (error instanceof Error ? error.message : String(error)),
    attached: () => attached,
  });
  return {
    binding,
    views,
    notes,
    errors,
    unwatches: () => unwatches,
    detach: () => {
      attached = false;
    },
  };
}

test("a subscribed watch holds exactly one reference and releases it once", async () => {
  const h = harness(async () => ({ watching: true, view: VIEW }));
  await h.binding.start();
  assert.equal(h.binding.held(), true);
  assert.deepEqual(h.views, [VIEW]);
  assert.deepEqual(h.notes, [null]);
  await h.binding.dispose();
  await h.binding.dispose();
  assert.equal(h.unwatches(), 1);
  assert.equal(h.binding.held(), false);
});

test("a refused subscribe keeps the history and holds no reference", async () => {
  const h = harness(async () => ({ watching: false, view: VIEW, watchError: "官方事件流瞬时断开" }));
  await h.binding.start();
  assert.equal(h.binding.held(), false);
  assert.deepEqual(h.views, [VIEW], "the read history must still be rendered");
  assert.deepEqual(h.notes, ["官方事件流瞬时断开"]);
  assert.deepEqual(h.errors, []);
  // Releasing a reference the pane never held would drop another pane's
  // subscription, so nothing may reach the wire here.
  await h.binding.dispose();
  assert.equal(h.unwatches(), 0);
});

test("a watch request that throws falls back to session.view and reports only the live link", async () => {
  const h = harness(
    async () => {
      throw new Error("旧版核心不支持这个动作");
    },
    async () => VIEW,
  );
  await h.binding.start();
  assert.deepEqual(h.views, [VIEW], "the fallback read must render the history");
  assert.deepEqual(h.notes, ["旧版核心不支持这个动作"]);
  assert.deepEqual(h.errors, []);
  assert.equal(h.binding.held(), false);
  await h.binding.dispose();
  assert.equal(h.unwatches(), 0);
});

test("a failed watch and a failed fallback read surface the original error", async () => {
  const h = harness(async () => {
    throw new Error("桥已断开");
  }, async () => {
    throw new Error("读取也失败");
  });
  await h.binding.start();
  assert.deepEqual(h.views, []);
  assert.deepEqual(h.errors, ["桥已断开"]);
  // The live-link note is dropped so the pane shows the real failure only.
  assert.deepEqual(h.notes, ["桥已断开", null]);
});

test("a pane closed while the watch is in flight releases the reference it acquired", async () => {
  let resolveWatch: (value: SessionWatchOutcome<View>) => void = () => {};
  const pending = new Promise<SessionWatchOutcome<View>>(resolve => {
    resolveWatch = resolve;
  });
  const h = harness(() => pending);
  const started = h.binding.start();
  h.detach();
  resolveWatch({ watching: true, view: VIEW });
  await started;
  assert.equal(h.unwatches(), 1);
  assert.equal(h.binding.held(), false);
  // No late view is painted into a pane that is gone.
  assert.deepEqual(h.views, []);
});

test("a retry after a refusal can establish the link without clearing the view", async () => {
  let attempt = 0;
  const h = harness(async () => {
    attempt += 1;
    return attempt === 1
      ? { watching: false, view: VIEW, watchError: "瞬时断开" }
      : { watching: true, view: { session: { id: "s1" }, messages: [...VIEW.messages, { id: "m2", text: "新消息" }] } };
  });
  await h.binding.start();
  assert.equal(h.views.length, 1);
  await h.binding.dispose();
  assert.equal(h.unwatches(), 0);

  // The component remounts the effect for a retry; the view handed back by the
  // second answer replaces the first instead of blanking it.
  const retry = harness(async () => ({ watching: true, view: VIEW }));
  await retry.binding.start();
  assert.equal(retry.binding.held(), true);
  await retry.binding.dispose();
  assert.equal(retry.unwatches(), 1);
});

test("bridge credential codes are recognised and the core's own authorization is not", () => {
  for (const code of ["TOKEN_EXPIRED", "BRIDGE_SESSION_EXPIRED", "RPC_DENIED", "RPC_OWNER", "PAIRING_EXPIRED"]) {
    assert.equal(isBridgeCredentialError({ code }), true, code);
  }
  assert.equal(isBridgeCredentialError({ status: 401, code: "HTTP_401" }), true);
  assert.equal(isBridgeCredentialError({ status: 403 }), true);
  assert.equal(isBridgeCredentialError({ status: 403, code: "RPC_DENIED" }), true);
  // A real core authorization decision keeps its own message: it is not a
  // rotated local channel and must not tell the user their login is fine.
  assert.equal(isBridgeCredentialError({ status: 401, code: "AUTH_REQUIRED" }), false);
  assert.equal(isBridgeCredentialError({ status: 403, code: "PERMISSION_DENIED" }), false);
  assert.equal(isBridgeCredentialError({ status: 403, code: "PLUGIN_PRIVILEGE_DENIED" }), false);
  assert.equal(isBridgeCredentialError({ status: 500, code: "CORE_ERROR" }), false);
  assert.equal(isBridgeCredentialError(null), false);
  assert.match(BRIDGE_RECONNECT_TEXT, /官方 Kimi 账号登录未受影响/);
});
