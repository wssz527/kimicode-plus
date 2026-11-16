import assert from "node:assert/strict";
import { test } from "node:test";
import {
  normalizePathname,
  notifyPathnameChange,
  pathnameSnapshot,
  routeForPath,
  subscribePathname,
} from "../src/routes.js";

test("normalizePathname strips trailing slashes but keeps the root", () => {
  assert.equal(normalizePathname("/"), "/");
  assert.equal(normalizePathname("/usage/"), "/usage");
  assert.equal(normalizePathname(""), "/");
});

test("routeForPath resolves every known page and falls back to overview", () => {
  for (const path of ["/", "/models", "/usage", "/chat", "/handoff", "/plugins", "/settings"]) {
    assert.equal(routeForPath(path).path, path);
  }
  assert.equal(routeForPath("/unknown").path, "/");
  assert.equal(routeForPath("/usage/").path, "/usage");
});

test("path snapshot is a stable read without a DOM (Node)", () => {
  // Node 环境没有 window：快照必须返回根路径而不是抛错。
  assert.equal(pathnameSnapshot(), "/");
});

test("subscribers are notified and unsubscribed deterministically", () => {
  let calls = 0;
  const unsubscribe = subscribePathname(() => {
    calls += 1;
  });
  notifyPathnameChange();
  notifyPathnameChange();
  assert.equal(calls, 2);
  unsubscribe();
  notifyPathnameChange();
  assert.equal(calls, 2, "after unsubscribe no further notifications are delivered");
});

test("multiple subscribers all receive one notification each", () => {
  const seen = { a: 0, b: 0 };
  const offA = subscribePathname(() => {
    seen.a += 1;
  });
  const offB = subscribePathname(() => {
    seen.b += 1;
  });
  notifyPathnameChange();
  offA();
  offB();
  assert.deepEqual(seen, { a: 1, b: 1 });
});
