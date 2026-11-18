// 构建期由 build/bundle.mjs 的 define 换成内联的 data URL 表。
// 单独成文件是为了让 host.js 的求值顺序不依赖调用方的 import 顺序：
// 上游 pet-panel.js 在模块顶层就调用 chrome.runtime.getURL。
export const ASSETS = MONITOR_ASSETS;
