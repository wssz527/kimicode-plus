import assert from "node:assert/strict";
import { test } from "node:test";
import { readFileSync } from "node:fs";
import { validProviderId, validProviderURL, beginEdit } from "../src/lib/providerPolicy.js";
import { api, noteRegistryRevision } from "../src/api.js";

test("shared provider policy includes local preset and boundary IDs", () => {
  const fixtures = JSON.parse(readFileSync("../contracts/provider-policy.json", "utf8"));
  for (const f of fixtures.ids) assert.equal(validProviderId(f.value), f.valid, f.value);
  for (const f of fixtures.urls) assert.equal(validProviderURL(f.value), f.valid, f.value);
});

test("fixed edit baseline survives unrelated live snapshot updates", async () => {
  const calls: {path:string; body:Record<string,unknown>}[]=[];
  const original=globalThis.fetch;
  globalThis.fetch=async (input,init)=>{
    const path=String(input);const body=JSON.parse(String(init?.body ?? "{}"));calls.push({path,body});
    if(path.endsWith("/pair")) return Response.json({sessionToken:"synthetic",permissions:[]});
    if(path.includes("/registry/snapshot")) return Response.json({revision:7,model:{providerId:"p",id:"m",name:"saved",capabilities:{}}});
    if(path.endsWith("/models/override")) return Response.json({revision:8});
    return Response.json({revision:9});
  };
  try {
    await api.pair("synthetic-pair");
    const snapshot=await api.registry.snapshot("p","m");
    assert.ok(snapshot.model);
    const editor=beginEdit(snapshot.model,snapshot.revision);
    editor.draft.name="draft";editor.touched.add("name");
    noteRegistryRevision(99);
    await api.models.override({providerId:"p",id:"m",field:"tools",value:true,revision:editor.baseRevision});
    assert.equal(calls.at(-1)?.body.revision,7);
    assert.equal(editor.baseEntity.name,"saved");
    assert.equal(editor.draft.name,"draft");
    assert.equal(await api.models.revision(),7);
    assert.ok(calls.at(-1)?.path.includes("/registry/snapshot"));
    assert.ok(!calls.some(c=>c.path.includes("/model-changes")));
    await api.models.rollback(1,editor.baseRevision);
    assert.equal(calls.at(-1)?.body.revision,7);
  } finally {globalThis.fetch=original;api.auth.clear();}
});
