import assert from "node:assert/strict";
import { test } from "node:test";
import {
  buildModelsDevBody,
  buildModelsImportBody,
  buildOverrideBody,
  buildPairBody,
  buildTaskSubmitBody,
  buildUsageQuery,
  buildModelPageQuery,
  buildProviderUpdateBody,
  chatSendHeaders,
  extractList,
  isBase64,
  isHttpsUrl,
  parseApiError,
  validateAttachment,
  validateTaskPackage,
} from "../src/lib/contract.js";
import type { TaskPackage } from "../../adapters/types.js";

const task: TaskPackage = {
  version: 1,
  taskId: "task-local-1",
  direction: "CHAT_TO_CODE",
  sessionId: "synthetic-session-bridge",
  projectId: "synthetic-project-workbench",
  source: {
    site: "kimi",
    url: "https://www.kimi.com/chat/synthetic-kimi-thread-01",
    threadId: "synthetic-kimi-thread-01",
    messageIds: ["kimi-m1", "kimi-m2"],
  },
  goal: "把模型目录的 revision 冲突处理补成可验证的流程",
  messages: [
    { id: "kimi-m1", role: "user", text: "先记录来源与时间" },
    { id: "kimi-m2", role: "assistant", text: "未知保持 UNKNOWN" },
  ],
  attachments: [],
  checkpoint: 2,
  parentCheckpoint: 0,
  mode: "PLAN",
};

test("pair body matches POST /pair contract", () => {
  assert.deepEqual(buildPairBody("abc"), { pairingToken: "abc" });
});

test("lists accept both {items,total} and bare arrays", () => {
  assert.deepEqual(extractList<number>({ items: [1, 2], total: 9 }), { items: [1, 2], total: 9 });
  assert.deepEqual(extractList<number>([1, 2, 3]), { items: [1, 2, 3], total: 3 });
  assert.deepEqual(extractList<number>(null), { items: [], total: 0 });
  assert.deepEqual(extractList<number>({ items: [1] }), { items: [1], total: 1 });
});

test("API error parsing preserves contract error shape", () => {
  assert.deepEqual(parseApiError(409, JSON.stringify({ error: { code: "CONFIG_CONFLICT", message: "设置已变化" } })), {
    code: "CONFIG_CONFLICT",
    message: "设置已变化",
  });
  assert.deepEqual(parseApiError(502, ""), { code: "HTTP_502", message: "请求未完成" });
  assert.equal(parseApiError(500, "oops").code, "HTTP_500");
});

test("usage query keeps offset/limit and trims filters", () => {
  assert.deepEqual(buildUsageQuery({ projectId: " p1 ", sessionId: "", model: "m1" }, 20, 50), {
    offset: 20,
    limit: 50,
    projectId: "p1",
    model: "m1",
  });
  assert.deepEqual(buildUsageQuery({}, 0, 100), { offset: 0, limit: 100 });
});

test("task submit body always confirms and includes optional mock behavior", () => {
  const body = buildTaskSubmitBody("intent-1", task, "TIMEOUT");
  assert.equal(body.confirmed, true);
  assert.equal(body.intent, "intent-1");
  assert.equal(body.mockBehavior, "TIMEOUT");
  assert.equal("mockBehavior" in buildTaskSubmitBody("intent-2", task), false);
  assert.throws(() => buildTaskSubmitBody("  ", task));
});

test("revision-carrying bodies validate integers", () => {
  assert.deepEqual(buildModelsImportBody([{ a: 1 }], 3), { records: [{ a: 1 }], revision: 3 });
  assert.throws(() => buildModelsImportBody([], -1));
  assert.throws(() => buildModelsImportBody([], 1.5));
  assert.deepEqual(
    buildOverrideBody({ providerId: "p", id: "m", field: "context", value: null, revision: 4 }),
    { providerId: "p", id: "m", field: "context", value: null, revision: 4 },
  );
  assert.throws(() => buildOverrideBody({ providerId: "p", id: "m", field: "context", value: Number.NaN, revision: 1 }));
  assert.deepEqual(buildModelsDevBody({ providers: {} }, 0, true), {
    catalog: { providers: {} },
    revision: 0,
    preview: true,
  });
});

test("provider update keeps id immutable and carries full revision", () => {
  const body = buildProviderUpdateBody({
    name: "DeepSeek API",
    protocol: "openai",
    baseUrl: "https://api.deepseek.com/v1",
    secretRef: "keychain://providers/deepseek",
    revision: 7,
  });
  assert.deepEqual(body, {
    name: "DeepSeek API",
    protocol: "openai",
    baseUrl: "https://api.deepseek.com/v1",
    secretRef: "keychain://providers/deepseek",
    revision: 7,
  });
  assert.equal("id" in body, false, "id must not be sent; the core treats it as immutable");
  assert.throws(() => buildProviderUpdateBody({ name: "x", protocol: "openai", baseUrl: "https://x.example.com", revision: -1 }));
  assert.deepEqual(
    buildProviderUpdateBody({ name: "x", protocol: "openai", baseUrl: "https://x.example.com", revision: 3, secretRef: null }),
    { name: "x", protocol: "openai", baseUrl: "https://x.example.com", secretRef: null, revision: 3 },
  );
});

test("pagination query uses the caller's offset and has no silent cap", () => {
  assert.deepEqual(buildModelPageQuery(0, 100), { offset: 0, limit: 100 });
  assert.deepEqual(buildModelPageQuery(3, 100), { offset: 300, limit: 100 });
  assert.throws(() => buildModelPageQuery(0, 1001));
});

test("chat sends carry an idempotency key", () => {
  assert.deepEqual(chatSendHeaders("abc"), { "Idempotency-Key": "abc" });
});

test("valid task package passes and direction variants are enforced", () => {
  assert.deepEqual(validateTaskPackage(task), []);
  const bad = { ...task, direction: "SIDEWAYS", mode: "YOLO" };
  const issues = validateTaskPackage(bad).map((issue) => issue.path);
  assert.ok(issues.includes("direction"));
  assert.ok(issues.includes("mode"));

  const continuing = { ...task, direction: "CHAT_TO_CODE_CONTINUE" as const };
  const continueIssues = validateTaskPackage(continuing).map((issue) => issue.path);
  assert.ok(continueIssues.includes("linkedTaskId"));
  assert.ok(continueIssues.includes("parentHash"));
  assert.ok(continueIssues.includes("parentCheckpoint"));

  const linked = {
    ...continuing,
    linkedTaskId: "task-local-0",
    parentHash: "a".repeat(64),
    parentCheckpoint: 1,
    checkpoint: 2,
  };
  assert.deepEqual(validateTaskPackage(linked), []);

  const uppercaseHash = { ...linked, parentHash: "A".repeat(64) };
  assert.ok(validateTaskPackage(uppercaseHash).some((issue) => issue.path === "parentHash"));

  const outOfOrder = { ...linked, checkpoint: 1 };
  assert.ok(validateTaskPackage(outOfOrder).some((issue) => issue.path === "checkpoint"));

  const strayHash = { ...task, parentHash: "a".repeat(64) };
  assert.ok(validateTaskPackage(strayHash).some((issue) => issue.path === "parentHash"));
});

test("messages must use contract roles, be unique, and stay inside the selection", () => {
  const badRole = { ...task, messages: [{ id: "kimi-m1", role: "tool", text: "x" }] };
  assert.ok(validateTaskPackage(badRole).some((issue) => issue.path === "messages"));

  const duplicate = {
    ...task,
    messages: [
      { id: "kimi-m1", role: "user", text: "x" },
      { id: "kimi-m1", role: "assistant", text: "y" },
    ],
  };
  assert.ok(validateTaskPackage(duplicate).some((issue) => issue.message.includes("重复")));

  const outsideSelection = {
    ...task,
    messages: [
      { id: "kimi-m1", role: "user", text: "x" },
      { id: "kimi-m3", role: "assistant", text: "y" },
    ],
  };
  assert.ok(validateTaskPackage(outsideSelection).some((issue) => issue.message.includes("source.messageIds")));
});

test("attachments above 1 MiB are rejected before the request", () => {
  const tooLarge = {
    name: "big.bin",
    mime: "application/octet-stream",
    size: 1_048_577,
    sha256: "d".repeat(64),
    status: "verified" as const,
  };
  assert.match(validateAttachment(tooLarge) ?? "", /1 MiB/);
});

test("task package rejects non-https sources, empty selections, and bad checkpoints", () => {
  const issues = validateTaskPackage({
    ...task,
    source: { ...task.source, url: "http://example.com/x", messageIds: [] },
    messages: [],
    checkpoint: 0,
    parentCheckpoint: -1,
  }).map((issue) => issue.path);
  assert.ok(issues.includes("source.url"));
  assert.ok(issues.includes("source.messageIds"));
  assert.ok(issues.includes("messages"));
  assert.ok(issues.includes("checkpoint"));
  assert.ok(issues.includes("parentCheckpoint"));
});

test("attachments require verified bytes and a sha256", () => {
  const good = {
    name: "plan.txt",
    mime: "text/plain",
    size: 4,
    sha256: "b".repeat(64),
    dataBase64: btoa("test"),
    status: "verified",
  };
  assert.equal(validateAttachment(good), null);
  assert.match(validateAttachment({ ...good, sha256: "nope" }) ?? "", /SHA-256/);
  assert.match(validateAttachment({ ...good, status: "unverified" }) ?? "", /校验/);
  assert.match(validateAttachment({ ...good, dataBase64: "!!!not-base64!!!" }) ?? "", /base64/);
});

test("base64 and https helpers reject unsafe values", () => {
  assert.equal(isBase64(btoa("hello")), true);
  assert.equal(isBase64("!!"), false);
  assert.equal(isHttpsUrl("https://www.kimi.com/"), true);
  assert.equal(isHttpsUrl("http://www.kimi.com/"), false);
  assert.equal(isHttpsUrl("https://user:pass@example.com/"), false);
  assert.equal(isHttpsUrl("not a url"), false);
});

test("valid task package with attachment uses verified status", () => {
  const withAttachment = {
    ...task,
    attachments: [
      {
        name: "a.txt",
        mime: "text/plain",
        size: 1,
        sha256: "c".repeat(64),
        dataBase64: btoa("a"),
        status: "verified" as const,
      },
    ],
  };
  assert.deepEqual(validateTaskPackage(withAttachment), []);
});
