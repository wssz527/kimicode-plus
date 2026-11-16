import assert from "node:assert/strict";
import { test } from "node:test";
import { embedRootClassName, normalizeColorScheme, resolveAssetBase, rewriteAssetURLs } from "../src/lib/embed.js";

test("embedded mount adds a scoping class that standalone mounts never get", () => {
  assert.equal(embedRootClassName(true), "ekd-root ekd-embedded");
  assert.equal(embedRootClassName(false), "ekd-root");
});

test("color scheme falls back to system for unknown or missing host values", () => {
  assert.equal(normalizeColorScheme("light"), "light");
  assert.equal(normalizeColorScheme("dark"), "dark");
  assert.equal(normalizeColorScheme("system"), "system");
  assert.equal(normalizeColorScheme(undefined), "system");
  assert.equal(normalizeColorScheme(""), "system");
  assert.equal(normalizeColorScheme("solarized"), "system");
});

test("asset base keeps one trailing slash at most", () => {
  assert.equal(resolveAssetBase(undefined), null);
  assert.equal(resolveAssetBase("  "), null);
  assert.equal(resolveAssetBase("http://127.0.0.1:43125"), "http://127.0.0.1:43125");
  assert.equal(resolveAssetBase("http://127.0.0.1:43125/"), "http://127.0.0.1:43125");
});

test("asset rewriting only touches root-relative url() references", () => {
  const css = '@font-face{src:url("/fonts/a.woff2")}x{background:url(/img/b.png)}y{background:url(https://cdn/c.png)}z{background:url("//cdn/d.png")}';
  const out = rewriteAssetURLs(css, "http://127.0.0.1:43125");
  assert.match(out, /url\("http:\/\/127\.0\.0\.1:43125\/fonts\/a\.woff2"\)/);
  assert.match(out, /url\(http:\/\/127\.0\.0\.1:43125\/img\/b\.png\)/);
  assert.match(out, /url\(https:\/\/cdn\/c\.png\)/);
  assert.match(out, /url\("\/\/cdn\/d\.png"\)/);
  assert.equal(rewriteAssetURLs(css, undefined), css);
});
