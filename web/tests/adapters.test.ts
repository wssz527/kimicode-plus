import assert from "node:assert/strict";
import { test } from "node:test";
import { packageHash, canonicalJson, sha256Hex } from "../../adapters/canonical.js";
import {
  SyntheticChatAdapter,
  fixtureForOrigin,
  normalizeDraft,
  siteDescriptors,
  syntheticSiteFixtures,
} from "../../adapters/synthetic-chat.js";
import { SyntheticDesktopAdapter, SYNTHETIC_SESSIONS } from "../../adapters/synthetic-desktop.js";
import { ExternalBrowserAdapter, isHttpsUrl, EXTERNAL_LINK_REL } from "../../adapters/browser.js";
import type { TaskPackage } from "../../adapters/types.js";

function kimiAdapter(): SyntheticChatAdapter {
  const fixture = fixtureForOrigin("https://www.kimi.com/");
  assert.ok(fixture);
  return new SyntheticChatAdapter(fixture);
}

const task: TaskPackage = {
  version: 1,
  taskId: "task-adapter-1",
  direction: "CHAT_TO_CODE",
  sessionId: "synthetic-session-bridge",
  projectId: "synthetic-project-workbench",
  source: {
    site: "kimi",
    url: "https://www.kimi.com/chat/synthetic-kimi-thread-01",
    threadId: "synthetic-kimi-thread-01",
    messageIds: ["kimi-m1"],
  },
  goal: "adapter test",
  messages: [{ id: "kimi-m1", role: "user", text: "hello" }],
  attachments: [],
  checkpoint: 1,
  parentCheckpoint: 0,
  mode: "PLAN",
};

test("canonical JSON is deterministic regardless of key order", () => {
  const a = canonicalJson({ b: 1, a: [true, null, "x"] });
  const b = canonicalJson({ a: [true, null, "x"], b: 1 });
  assert.equal(a, b);
  assert.equal(a, '{"a":[true,null,"x"],"b":1}');
});

test("sha256 matches a known digest", async () => {
  assert.equal(await sha256Hex("abc"), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
  assert.equal(await packageHash({ x: 1 }), await packageHash({ x: 1 }));
});

test("fixtures are synthetic and never claim a verified site", () => {
  const descriptors = siteDescriptors();
  assert.ok(descriptors.length >= 4);
  for (const descriptor of descriptors) {
    assert.equal(descriptor.status, "UNVERIFIED");
    assert.match(descriptor.note, /未验证/);
  }
  assert.ok(syntheticSiteFixtures().every((fixture) => fixture.origin.startsWith("https://")));
});

test("detect marks matching origin UNVERIFIED and other origins UNAVAILABLE", () => {
  const adapter = kimiAdapter();
  const match = adapter.detect({ origin: "https://www.kimi.com" });
  assert.equal(match.state, "UNVERIFIED");
  assert.equal(match.synthetic, true);
  const mismatch = adapter.detect({ origin: "https://example.com" });
  assert.equal(mismatch.state, "UNAVAILABLE");
});

test("enumerateMessage marks partial virtual lists without faking full history", async () => {
  const adapter = kimiAdapter();
  const page = await adapter.enumerateMessages();
  assert.equal(page.partial, true);
  assert.ok(page.loaded < (page.totalKnown ?? 0));
});

test("extract only returns selected message ids", async () => {
  const adapter = kimiAdapter();
  const conversation = await adapter.extract({ mode: "SELECTED", messageIds: ["kimi-m1", "kimi-m9"] });
  assert.deepEqual(conversation.messages.map((message) => message.id), ["kimi-m1"]);
  assert.equal(conversation.partial, true);
  assert.equal(conversation.trust, "UNTRUSTED_WEB");
});

test("prepareDraft requires expected hash match and verifies read-back", async () => {
  const adapter = kimiAdapter();
  const emptyHash = await sha256Hex(normalizeDraft(""));
  const ok = await adapter.prepareDraft("  准备写入的草稿  ", emptyHash);
  assert.equal(ok.guardMatched, true);
  assert.equal(ok.ok, true);
  assert.equal(ok.readBack, "  准备写入的草稿  ");
  assert.equal(ok.readBackMatched, true);

  const stale = await adapter.prepareDraft("另一份草稿", emptyHash);
  assert.equal(stale.ok, false);
  assert.equal(stale.guardMatched, false);
  assert.match(stale.reason ?? "", /预期哈希/);
  assert.equal(adapter.draftText(), "  准备写入的草稿  ");
});

test("observeReply can return UNKNOWN and attachments stay unavailable", async () => {
  const adapter = kimiAdapter();
  assert.equal(await adapter.observeReply(), "COMPLETE");
  assert.equal(await adapter.observeReply(), "UNKNOWN");
  const attachment = await adapter.obtainAttachment({ name: "x.bin", sourceUrl: "https://example.com/x" });
  assert.equal(attachment.status, "unavailable");
});

test("desktop adapter is idempotent per intent and never retries", async () => {
  const adapter = new SyntheticDesktopAdapter();
  const first = await adapter.submit({ intent: "i-1", task, confirmed: true });
  assert.equal(first.state, "DELIVERED");
  assert.equal(first.replayed, false);
  const replay = await adapter.submit({ intent: "i-1", task, confirmed: true });
  assert.equal(replay.replayed, true);
  assert.equal(replay.state, "DELIVERED");

  const changed = await adapter.submit({
    intent: "i-1",
    task: { ...task, goal: "different" },
    confirmed: true,
  }).catch((error: Error) => error);
  assert.ok(changed instanceof Error);

  const timeout = await adapter.submit({
    intent: "i-2",
    task: { ...task, taskId: "task-adapter-2" },
    confirmed: true,
    mockBehavior: "TIMEOUT",
  });
  assert.equal(timeout.state, "UNKNOWN");
  assert.equal(timeout.receipt, null);

  const reconcile = await adapter.reconcile({ taskId: "task-adapter-2" });
  assert.equal(reconcile.state, "UNKNOWN");
});

test("desktop adapter refuses unconfirmed tasks and changed mode", async () => {
  const adapter = new SyntheticDesktopAdapter();
  await assert.rejects(adapter.submit({ intent: "x", task, confirmed: false }));
  adapter.setMode("CHANGED");
  await assert.rejects(adapter.submit({ intent: "y", task, confirmed: true }));
  adapter.setMode("UNAVAILABLE");
  await assert.rejects(adapter.sessions());
  adapter.setMode("AVAILABLE");
  assert.equal((await adapter.sessions()).length, SYNTHETIC_SESSIONS.length);
});

test("external browser adapter only builds noopener noreferrer links for HTTPS", () => {
  const adapter = new ExternalBrowserAdapter();
  const link = adapter.link("https://www.kimi.com/");
  assert.equal(link.url, "https://www.kimi.com/");
  assert.equal(link.rel, EXTERNAL_LINK_REL);
  assert.equal(link.target, "_blank");
  assert.throws(() => adapter.link("http://www.kimi.com/"));
  assert.throws(() => adapter.link("javascript:alert(1)"));
  assert.equal(isHttpsUrl("https://chat.deepseek.com"), true);
});
