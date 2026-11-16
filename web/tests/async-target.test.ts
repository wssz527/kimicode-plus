import assert from "node:assert/strict";
import { test } from "node:test";
import { readFileSync } from "node:fs";
import * as ts from "typescript";
import { beginEdit } from "../src/lib/providerPolicy.js";

function handler(file: string, name: string): string {
  const source = readFileSync(file, "utf8");
  const ast = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  let found = "";
  function visit(node: ts.Node) {
    if (ts.isFunctionDeclaration(node) && node.name?.text === name) found = node.getText(ast);
    ts.forEachChild(node, visit);
  }
  visit(ast);
  assert.ok(found, `production handler ${name}`);
  return ts.transpileModule(found, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None } }).outputText;
}

test("model edit keeps latest target when snapshots return B then A and ignores closed editor", async () => {
  const pending: ((v: unknown) => void)[] = [];
  const api = { registry: { snapshot: () => new Promise(resolve => pending.push(resolve)) } };
  const generation = { current: 0 };
  let selected: { baseEntity: { id: string }; baseRevision: number } | null = null;
  const open = new Function("api", "editRequest", "setSelected", "setEditError", "beginEdit", `${handler("src/pages/ModelsPage.tsx", "openModel")} return openModel;`)(api, generation, (value: typeof selected) => { selected = value; }, (e: unknown) => { if (e) throw e; }, beginEdit);
  const a = open({ providerId: "p", id: "a" });
  const b = open({ providerId: "p", id: "b" });
  pending[1]!({ revision: 8, model: { providerId: "p", id: "b" } }); await b;
  pending[0]!({ revision: 7, model: { providerId: "p", id: "a" } }); await a;
  assert.deepEqual(selected, { ...beginEdit({ providerId: "p", id: "b" }, 8) });
  const c = open({ providerId: "p", id: "c" }); generation.current++;
  pending[2]!({ revision: 9, model: { id: "c" } }); await c;
  assert.equal(selected, null);
});

test("public import invalidates A preview after target changes to B", async () => {
  let finish!: (value: unknown) => void;
  const calls: unknown[] = [];
  const api = { models: { catalogImport: (payload: unknown) => { calls.push(payload); return new Promise(resolve => { finish = resolve; }); } } };
  const requestId = { current: 0 };
  const selection = { current: { source: "moonshotai", target: "a" } };
  let preview: unknown = null;
  const build = new Function("api", "action", "requestId", "selection", "source", "target", "setPreview", `${handler("src/components/PublicCatalogImport.tsx", "inspect")} return inspect;`);
  const inspect = build(api, { run: (fn: () => unknown) => fn() }, requestId, selection, "moonshotai", "a", (v: unknown) => { preview = v; });
  const waiting = inspect(); requestId.current++; selection.current.target = "b";
  finish({ catalogHash: "catalog", revision: 4 }); await waiting;
  assert.equal(preview, null);
  const save = new Function("api", "preview", "source", "target", `${handler("src/components/PublicCatalogImport.tsx", "save")} return save;`)(api, { target: "a", source: "moonshotai" }, "moonshotai", "b");
  await save(); assert.equal(calls.length, 1);
});
