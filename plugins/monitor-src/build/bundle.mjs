#!/usr/bin/env node
/**
 * 把 plugins/monitor 打成单文件 renderer.js / settings.js。
 *
 * 宿主的插件 loader 用 blob URL import 入口（web/src/plugin-runtime/loader.ts），
 * 模块内不能有相对 import；/assets 也只下发 manifest 点名的文件
 * （internal/plugins/manager.go）。所以面板的全部依赖必须在构建期合进一个文件：
 * - vendor/ 下的上游 ES 模块由 esbuild 打包进同一作用域；
 * - content.css 作为字符串常量（装配时注入 <style>）；
 * - rive.wasm 与 .riv 转 data URL（宿主 CSP 允许 data:，且不依赖扩展资源目录）；
 * - rive.js 是 UMD 包，求值后落到 globalThis.rive，由 pet-panel 读取。
 *
 * 产物上限 8 MiB（internal/plugins/manager.go 的 assetLimit），超出直接失败。
 */

import { readFileSync, statSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const sourceDir = resolve(here, "..");
// 发布目录：宿主只 embed plugins/monitor/，源码与 vendor 不进二进制
const pluginDir = resolve(sourceDir, "..", "monitor");
const repoRoot = resolve(sourceDir, "..", "..");
const assetLimit = 8 << 20;

// esbuild 已经随 web/ 的 devDependencies 装好（vite 的依赖），这里复用同一份，
// 不再为插件构建引入第二个打包器版本。找不到时明确报错，不静默跳过。
const esbuildEntry = join(repoRoot, "web", "node_modules", "esbuild", "lib", "main.js");
let build;
try {
  ({ build } = await import(pathToFileURL(esbuildEntry).href));
} catch (error) {
  throw new Error(
    `找不到 esbuild（期望 ${esbuildEntry}）。先在 web/ 下运行 npm ci 再构建插件。原始错误：${error?.message || error}`
  );
}

const readText = (relative) => readFileSync(join(sourceDir, relative), "utf8");
const dataURL = (relative, mime) =>
  `data:${mime};base64,${readFileSync(join(sourceDir, relative)).toString("base64")}`;

// rive.js 的 webpack 包装用 `this` 当全局对象；末尾的 sourceMappingURL 指向
// 未随包分发的 .map，留着只会在控制台报 404。
const riveSource = readText("vendor/rive/rive.js").replace(/\n\/\/# sourceMappingURL=.*\s*$/, "\n");

const assets = {
  "rive/rive.wasm": dataURL("vendor/rive/rive.wasm", "application/wasm"),
  "rive/kimi_avatar_web-PnsTWI-X.riv": dataURL("vendor/rive/kimi_avatar_web-PnsTWI-X.riv", "application/octet-stream")
};

// 上游 content.css 同时服务网页版：书签面板（content/bookmarks.js）与 Codex 桌宠
// （pet/pet-sprites.js）都只在浏览器里注入，桌面面板既没有对应 DOM 也没有对应
// 模块，样式留着只是死代码。按段落标题整段裁掉，标题对不上就报错——上游改版
// 时宁可构建失败，也不要静默内联一段没人用的 40 KiB。
const browserOnlySections = [
  "/* ===== Codex 桌面宠物（pet/pet-sprites.js 创建，position:fixed，可拖拽停放） =====",
  "/* ===== AI 回复收藏（content/bookmarks.js 注入，全部 kbm- 前缀） ====="
];
const stripSection = (css, header) => {
  const start = css.indexOf(header);
  if (start < 0) throw new Error(`content.css 缺少预期的浏览器专属段落：${header.slice(0, 40)}…`);
  const next = css.indexOf("/* ===== ", start + header.length);
  return css.slice(0, start) + css.slice(next < 0 ? css.length : next);
};
const contentCSS = browserOnlySections.reduce(stripSection, readText("vendor/content.css"));
if (/kbm-|codex-roam-pet/.test(contentCSS)) {
  throw new Error("裁掉段落之后仍然出现 kbm-/codex-roam-pet 选择器：段落边界与预期不符");
}
// 侧栏整理规则必须存活：它们和面板一起决定桌面侧栏的外观
const tidyRules = (contentCSS.match(/html\.ksb-sidebar-tidy/g) || []).length;
if (tidyRules !== 8) throw new Error(`侧栏整理规则只剩 ${tidyRules} 条，期望 8 条`);

const define = {
  MONITOR_CSS: JSON.stringify(contentCSS),
  MONITOR_ASSETS: JSON.stringify(assets)
};

// Rive 运行时作为一段先于业务模块求值的代码注入：pet-panel 在模块顶层拿
// globalThis.rive，注入必须排在它之前。
//
// UMD 包装用 `(this, factory)` 当全局对象，而 ES module 顶层是严格模式，
// `this` 为 undefined，`root["rive"] = factory()` 会抛 TypeError。
// 只在打包时把 UMD 的 root 实参换成 globalThis，源码本身不动。
const riveGlobal = riveSource.replace(/\}\)\(this, \(\) => \{/, "})(globalThis, () => {");
if (riveGlobal === riveSource) {
  throw new Error("rive.js 的 UMD 头部与预期不符：找不到 `})(this, () => {`");
}
const riveBanner = `/* Rive runtime (MIT) — see ../LICENSE */\n${riveGlobal}\n`;

const targets = [
  { entry: "src/main.js", outfile: "renderer.js", banner: riveBanner },
  { entry: "src/settings.js", outfile: "settings.js", banner: "" }
];

for (const target of targets) {
  await build({
    entryPoints: [join(sourceDir, target.entry)],
    outfile: join(pluginDir, target.outfile),
    bundle: true,
    format: "esm",
    platform: "browser",
    target: ["chrome110", "safari16"],
    treeShaking: true,
    minify: false,
    legalComments: "inline",
    charset: "utf8",
    define,
    banner: {
      js: `/* KimiCode+ Monitor · 结构源自 bfjnbvf/kimi-code-monitor (MIT)，见同目录 LICENSE */\n${target.banner}`
    },
    plugins: [
      {
        name: "vendor-pet-stub",
        setup(builder) {
          // 上游 pet-panel.js 引用浏览器独有的 page-level 桌宠素材模块
          builder.onResolve({ filter: /\/pet\/sprites\.js$/ }, () => ({
            path: join(sourceDir, "src", "pet-sprites-stub.js")
          }));
        }
      }
    ],
    logLevel: "warning"
  });
  const size = statSync(join(pluginDir, target.outfile)).size;
  if (size > assetLimit) {
    throw new Error(`${target.outfile} 有 ${size} 字节，超过宿主上限 ${assetLimit}`);
  }
  console.log(`${target.outfile.padEnd(14)} ${(size / 1024).toFixed(1)} KiB`);
}

const notice = `KimiCode+ Monitor
=================

界面结构、样式、交互与中文文案移植自 bfjnbvf/kimi-code-monitor
(commit 1d529f0b424a708971cfac097238df444f3b5beb, MIT License)。
上游版权声明见同目录的 LICENSE。

Rive 运行时（rive.js / rive.wasm）与形象资源
kimi_avatar_web-PnsTWI-X.riv 沿用上游的分发方式；素材为 Kimi 官方形象，
来源与上游相同。

余额数据层的读取模型移植自用户自有项目 Kimi Work+（internal/quota/），
端点、解析规则与测试数据见 internal/quota/ 的源码。
`;

const noticePath = join(pluginDir, "NOTICE");
try {
  if (readFileSync(noticePath, "utf8") === notice) process.exitCode = 0;
} catch {
  // 不存在时下面写入
}
writeFileSync(noticePath, notice);
console.log("NOTICE 已更新");
