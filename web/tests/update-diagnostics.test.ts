import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import ts from "typescript";

type UpdateStatus = {
  state: string;
  currentVersion: string;
  latestVersion?: string;
  checkedAt?: string;
  releaseUrl?: string;
  releaseNotes?: string;
  updateAvailable: boolean;
  canApply: boolean;
  downloadedBytes: number;
  totalBytes: number;
  note?: string;
  error?: string;
};

type Host = { request<T>(action: string): Promise<T> };

const source = readFileSync("src/components/SettingsSurface.tsx", "utf8");
const parsed = ts.createSourceFile("SettingsSurface.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
const helpers = new Set(["byteText", "coreUpdateBusy", "coreUpdateText", "checkCoreUpdate", "watchCoreUpdate"]);
const logic = parsed.statements
  .filter((statement): statement is ts.FunctionDeclaration => ts.isFunctionDeclaration(statement) && helpers.has(statement.name?.text ?? ""))
  .map(statement => statement.getText(parsed)).join("\n");
const { outputText } = ts.transpileModule(logic, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } });
const update = {} as {
  coreUpdateText(status: UpdateStatus | null, reconnecting?: boolean): string;
  checkCoreUpdate(host: Host): Promise<UpdateStatus>;
  watchCoreUpdate(host: Host, observe: (status: UpdateStatus | null, reconnecting: boolean) => void, signal: AbortSignal, pause: () => Promise<void>, now?: () => number): Promise<UpdateStatus | null>;
};
new Function("exports", outputText)(update);

const idle: UpdateStatus = { state: "idle", currentVersion: "0.2.0", updateAvailable: false, canApply: false, downloadedBytes: 0, totalBytes: 0 };
const available: UpdateStatus = {
  ...idle,
  state: "available",
  latestVersion: "0.3.0",
  updateAvailable: true,
  canApply: true,
  checkedAt: "2026-01-02T03:04:05Z",
  releaseUrl: "https://github.com/wssz527/kimicode-plus/releases/tag/v0.3.0",
  releaseNotes: "Updated core",
};

function mockHost(actions: string[], responses: (UpdateStatus | Error)[]): Host {
  return {
    async request<T>(action: string): Promise<T> {
      actions.push(action);
      const next = responses.shift();
      assert.ok(next, "unexpected extra update request");
      if (next instanceof Error) throw next;
      return next as T;
    },
  };
}

test("manual update check retains release details and handles no release or manual downloads", async () => {
  const actions: string[] = [];
  assert.equal(update.coreUpdateText(idle), "尚未检查更新");
  const checked = await update.checkCoreUpdate(mockHost(actions, [available]));
  assert.deepEqual(actions, ["update.check"]);
  assert.deepEqual(checked, available);
  assert.equal(update.coreUpdateText(checked), "有新版本可更新");
  assert.equal(update.coreUpdateText({ ...checked, canApply: false }), "有新版本可手动下载");
  assert.equal(update.coreUpdateText({ ...idle, state: "up_to_date" }), "当前已是最新版本");
  assert.equal(update.coreUpdateText({ ...idle, state: "up_to_date", note: "仓库尚未发布正式 Release 版本" }), "仓库尚未发布正式 Release 版本");
});

test("update polling reports download, verification, restart, connection loss and the recovered version", async () => {
  const downloaded = { ...available, state: "downloading", downloadedBytes: 1024 };
  const recovered = { ...idle, state: "updated", currentVersion: "0.3.0" };
  const actions: string[] = [];
  const host = mockHost(actions, [
    downloaded,
    { ...downloaded, downloadedBytes: 2048, totalBytes: 4096 },
    { ...available, state: "verifying" },
    { ...available, state: "restarting" },
    new Error("connection temporarily closed"),
    { ...recovered, state: "restarting" },
    recovered,
  ]);
  const transitions: string[] = [];
  const status = await update.watchCoreUpdate(host, (next, reconnecting) => {
    transitions.push(update.coreUpdateText(next, reconnecting));
  }, new AbortController().signal, async () => {});
  assert.deepEqual(actions, Array(7).fill("update.status"));
  assert.deepEqual(transitions, [
    "正在下载安装包 · 1.0 KB",
    "正在下载安装包 · 50%",
    "正在校验并准备安装…",
    "正在重启并检查核心状态，请稍候…",
    "正在重启并检查核心状态，请稍候…",
    "正在重启并检查核心状态，请稍候…",
    "核心已更新至 0.3.0",
  ]);
  assert.deepEqual(status, recovered);
});

test("update polling stops at a checksum failure or when the panel unmounts", async () => {
  const failed = { ...available, state: "error", error: "安装包 SHA-256 校验失败" };
  const actions: string[] = [];
  const aborted = new AbortController();
  const result = await update.watchCoreUpdate(mockHost(actions, [failed]), () => {}, aborted.signal, async () => {
    assert.fail("failed download should not be retried");
  });
  assert.deepEqual(result, failed);
  assert.equal(update.coreUpdateText(failed), failed.error);
  aborted.abort();
  const afterAbort = await update.watchCoreUpdate(mockHost(actions, []), () => {}, aborted.signal, async () => {});
  assert.equal(afterAbort, null);
  assert.deepEqual(actions, ["update.status"]);
});


test("update polling returns a persisted rollback when the previous version recovers", async () => {
  const rolledBack = { ...idle, state: "rolled_back", error: "更新失败，已回滚：核心在通过健康检查前退出" };
  const actions: string[] = [];
  const status = await update.watchCoreUpdate(mockHost(actions, [
    { ...available, state: "restarting" }, new Error("connection temporarily closed"), rolledBack,
  ]), () => {}, new AbortController().signal, async () => {});
  assert.deepEqual(actions, Array(3).fill("update.status"));
  assert.deepEqual(status, rolledBack);
  assert.equal(update.coreUpdateText(status), rolledBack.error);
});


test("polling stays active through a long download and bounds a stalled restart", async () => {
  const downloading = { ...available, state: "downloading" };
  const restarting = { ...available, state: "restarting" };
  const recovered = { ...idle, state: "updated", currentVersion: "0.3.0" };
  let clock = 0;
  const prolonged = await update.watchCoreUpdate(mockHost([], [downloading, downloading, restarting, recovered]), () => {},
    new AbortController().signal, async () => { clock += 7 * 60_000; }, () => clock);
  assert.deepEqual(prolonged, recovered);
  const stalled = await update.watchCoreUpdate(mockHost([], [restarting, new Error("core unavailable")]), () => {},
    new AbortController().signal, async () => { clock += 2 * 60_000; }, () => clock);
  assert.equal(stalled, null);
});
