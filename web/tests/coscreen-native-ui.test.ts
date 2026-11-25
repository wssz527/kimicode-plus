import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import ts from "typescript";
import type { NativeMessage, NativeQuestion } from "../src/native.js";

function functions<T>(file: string, names: string[], globals = ""): T {
  const source = ts.createSourceFile(file, readFileSync(file, "utf8"), ts.ScriptTarget.ESNext, true, ts.ScriptKind.TSX);
  const matches = source.statements.filter((node): node is ts.FunctionDeclaration => ts.isFunctionDeclaration(node) && names.includes(node.name?.text ?? ""));
  assert.equal(matches.length, names.length);
  const { outputText } = ts.transpileModule(`${globals}\n${matches.map(node => node.getText(source)).join("\n")}`, {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  });
  const exports = {};
  new Function("exports", outputText)(exports);
  return exports as T;
}

const workbench = "src/components/workbench/";

test("closing the middle pane preserves the first and last ratio through storage", () => {
  const { readPaneWeights, removePaneWeight } = functions<{
    readPaneWeights(json: string | null, count: number): number[];
    removePaneWeight(weights: number[], index: number): number[];
  }>("src/components/SessionGrid.tsx", ["readPaneWeights", "removePaneWeight"]);
  const remaining = removePaneWeight([0.65, 1.15, 1.2], 1);
  assert.equal(remaining.length, 2);
  assert.ok(Math.abs(remaining[0]! / remaining[1]! - 0.65 / 1.2) < 0.0001);
  assert.deepEqual(readPaneWeights(JSON.stringify({ 2: remaining }), 2), remaining);
  for (const corrupt of [null, "null", "invalid", '{"2":[1,"2"]}', '{"2":[1,0]}', '{"2":[1]}']) {
    assert.deepEqual(readPaneWeights(corrupt, 2), [1, 1]);
  }
});

test("streaming sticks to the bottom only inside the 80 pixel threshold", () => {
  const { isNearThreadEnd } = functions<{
    isNearThreadEnd(scroll: { scrollHeight: number; scrollTop: number; clientHeight: number }): boolean;
  }>(`${workbench}SessionPanes.tsx`, ["isNearThreadEnd"]);
  assert.equal(isNearThreadEnd({ scrollHeight: 500, clientHeight: 200, scrollTop: 220 }), false);
  assert.equal(isNearThreadEnd({ scrollHeight: 500, clientHeight: 200, scrollTop: 221 }), true);
  assert.equal(isNearThreadEnd({ scrollHeight: 500, clientHeight: 200, scrollTop: 300 }), true);
});

test("message groups preserve stream order and legacy messages without a turn ID", () => {
  const { groupThread } = functions<{
    groupThread(messages: NativeMessage[]): { id: string; messages: NativeMessage[] }[];
  }>(`${workbench}pieces.tsx`, ["groupThread"]);
  const history = { id: "old", role: "assistant", text: "history" };
  const user = { id: "user", turnId: "run", role: "user", text: "question" };
  const reasoning = { id: "thinking", turnId: "run", role: "assistant", text: "", reasoning: "reason", streaming: true };
  const tool = { id: "tool", turnId: "run", role: "tool", text: "", toolCalls: [{ id: "tool", name: "Inspect", status: "running" }] };
  assert.deepEqual(groupThread([history, user, reasoning, tool]), [
    { id: "old", messages: [history] }, { id: "run", messages: [user, reasoning, tool] },
  ]);
});

test("attachment validation deduplicates paths and enforces the limit before encoding", async () => {
  type Attachment = { id: string; name: string; path?: string; file?: File };
  const { addComposerAttachments, attachmentPayload } = functions<{
    addComposerAttachments(current: Attachment[], next: Attachment[]): Attachment[];
    attachmentPayload(attachment: Attachment): Promise<unknown>;
  }>(`${workbench}NativeComposer.tsx`, ["addComposerAttachments", "attachmentPayload"], `
    const ATTACHMENT_LIMIT = 16;
    const INLINE_IMAGE_LIMIT = 1024 * 1024;
    const inlineImageTypes = new Set(["image/png"]);
  `);
  const paths = Array.from({ length: 16 }, (_, n) => ({ id: `/tmp/file-${n}`, path: `/tmp/file-${n}`, name: `file-${n}` }));
  assert.deepEqual(addComposerAttachments(paths, [paths[2]!]), paths);
  assert.throws(() => addComposerAttachments(paths, [{ id: "/tmp/new", path: "/tmp/new", name: "new" }]), /16/);
  assert.throws(() => addComposerAttachments([], [{ id: "no-path", name: "note", file: new File(["note"], "note", { type: "text/plain" }) }]), /本机路径/);
  const image = new File([new Uint8Array([1, 2, 3])], "sample.png", { type: "image/png" });
  const item = { id: "image", name: image.name, file: image };
  assert.deepEqual(addComposerAttachments([], [item]), [item]);
  assert.deepEqual(await attachmentPayload(item), { name: "sample.png", mediaType: "image/png", data: "AQID" });
  assert.equal(await attachmentPayload(paths[0]!), paths[0]!.path);
  assert.throws(() => addComposerAttachments([], [{ ...item, file: new File([new Uint8Array(1024 * 1024 + 1)], "sample.png", { type: "image/png" }) }]), /1 MiB/);
});

test("question form encodes single, multiple and free-text replies without guessing blank answers", () => {
  type Input = { selected: string[]; other: boolean; text: string };
  const { questionAnswer } = functions<{ questionAnswer(question: NativeQuestion, input: Input): unknown }>(`${workbench}ApprovalCard.tsx`, ["questionAnswer"]);
  const question: NativeQuestion = { id: "q", question: "Pick", options: [{ id: "one", label: "One" }] };
  const picked = { selected: ["one"], other: false, text: "" };
  assert.deepEqual(questionAnswer(question, picked), { kind: "single", option_id: "one" });
  const multiple = { ...question, multi_select: true, allow_other: true };
  assert.equal(questionAnswer(multiple, { ...picked, other: true }), null);
  assert.deepEqual(questionAnswer(multiple, { ...picked, other: true, text: "  next " }), {
    kind: "multi_with_other", option_ids: ["one"], other_text: "next",
  });
});
