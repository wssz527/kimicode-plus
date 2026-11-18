/**
 * 宿主替身：把上游面板依赖的浏览器扩展 API 接到插件 ctx 上。
 *
 * 上游内容脚本用三类浏览器私有通道：chrome.storage.local（面板配置、宠物位置、
 * 语言镜像）、页面 localStorage（mini 开关、引导标记、按天积累）、
 * chrome.runtime.getURL（Rive 的 wasm 与 riv 资产）。桌面插件里这些都不该存在：
 * 前两类会绕过插件作用域，第三类在 app:// 下没有扩展资源目录。
 *
 * 这里只做替换，不改上游语义：
 * - 两类存储共用一张内存表，键名保持原样，落盘统一走 ctx.storage；
 * - localStorage 是同步接口，内存表在装配前预取完成，写入先改内存再异步落盘；
 * - 资产在构建期内联成 data URL（见 build/bundle.mjs）。
 *
 * 求值顺序：上游 pet-panel.js 在模块顶层就调用 chrome.runtime.getURL，
 * 所以替身必须在业务模块求值之前装好。main.js 因此先 import 本文件
 * （模块体里安装），再 import 上游模块。
 */

// 同步镜像：localStorage 的调用方在渲染过程中读，等不了 Promise
import { ASSETS } from "./shim-assets.js";

const memory = new Map();
const pendingWrites = new Map();
let bridge = null;

function schedule(key) {
  if (!bridge || pendingWrites.has(key)) return;
  const ctx = bridge;
  const timer = setTimeout(() => {
    pendingWrites.delete(key);
    const value = memory.get(key);
    const write = value === undefined ? ctx.storage.delete(key) : ctx.storage.set(key, value);
    void Promise.resolve(write).catch((error) => {
      ctx.log("warn", `面板配置写入失败：${error?.message || error}`);
    });
  }, 30);
  pendingWrites.set(key, timer);
}

function readValue(key) {
  return memory.has(key) ? memory.get(key) : undefined;
}

/** 预取本插件的全部键，并把落盘通道接到 ctx。装配在任何读取之前 await 它。 */
export async function bindStorage(ctx) {
  bridge = ctx;
  memory.clear();
  let keys = [];
  try {
    keys = await ctx.storage.list();
  } catch (error) {
    ctx.log("warn", `面板配置读取失败：${error?.message || error}`);
    return;
  }
  for (const key of keys) {
    try {
      const value = await ctx.storage.get(key);
      if (value !== undefined) memory.set(key, value);
    } catch (error) {
      ctx.log("warn", `面板配置读取失败：${key}`);
    }
  }
}

function storageLocalGet(keys) {
  return new Promise((resolve) => {
    const want = keys == null
      ? [...memory.keys()]
      : Array.isArray(keys)
        ? keys
        : typeof keys === "object"
          ? Object.keys(keys)
          : [keys];
    const result = {};
    for (const key of want) {
      const value = readValue(key);
      if (value !== undefined) result[key] = value;
    }
    resolve(result);
  });
}

function storageLocalSet(items) {
  return new Promise((resolve) => {
    for (const [key, value] of Object.entries(items || {})) {
      memory.set(key, value);
      schedule(key);
    }
    resolve();
  });
}

function storageLocalRemove(keys) {
  return new Promise((resolve) => {
    const list = Array.isArray(keys) ? keys : keys == null ? [...memory.keys()] : [keys];
    for (const key of list) {
      memory.delete(key);
      schedule(key);
    }
    resolve();
  });
}

const noopListenerHub = {
  addListener() {},
  removeListener() {},
  hasListener: () => false
};

function assetURL(path) {
  const rel = String(path || "").replace(/^chrome-extension:\/\/[^/]+\//, "");
  return ASSETS[rel] || rel;
}

const chromeShim = {
  runtime: {
    id: "kimi-monitor",
    getURL: assetURL,
    // 扩展后台不存在：桌面漫游宠物与 CLI 设置入口据此静默失效
    sendMessage: () => Promise.resolve(undefined),
    onMessage: noopListenerHub,
    onConnect: noopListenerHub,
    openOptionsPage: () => Promise.resolve()
  },
  storage: {
    local: {
      get: storageLocalGet,
      set: storageLocalSet,
      remove: storageLocalRemove,
      clear: () => storageLocalRemove(null)
    },
    onChanged: noopListenerHub
  }
};

const localStorageShim = {
  getItem: (key) => {
    const value = readValue(String(key));
    return value === undefined ? null : String(value);
  },
  setItem: (key, value) => {
    memory.set(String(key), String(value));
    schedule(String(key));
  },
  removeItem: (key) => {
    memory.delete(String(key));
    schedule(String(key));
  },
  key: (index) => {
    const keys = [...memory.keys()];
    return index >= 0 && index < keys.length ? keys[index] : null;
  },
  get length() {
    return memory.size;
  },
  clear: () => {
    for (const key of [...memory.keys()]) {
      memory.delete(key);
      schedule(key);
    }
  }
};

// 模块求值即安装：上游模块顶层调用 chrome.runtime.getURL
globalThis.chrome = chromeShim;
const nativeLocalStorage = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
try {
  Object.defineProperty(globalThis, "localStorage", {
    value: localStorageShim,
    configurable: true,
    writable: true
  });
} catch (error) {
  // 无法覆盖时上游自己的 try/catch 兜底，面板仍可用，只是偏好不持久
}

/** 同步来自同窗口其他视图（如设置页）写入的 storage 键值。 */
export function syncStorageKey(key, value) {
  if (value === undefined) memory.delete(String(key));
  else memory.set(String(key), value);
}

/** 停用插件时恢复宿主原状，并把内存里未落盘的键写出去。 */
export function disposeHost() {
  for (const timer of pendingWrites.values()) clearTimeout(timer);
  if (bridge) {
    for (const [key, value] of memory) {
      void Promise.resolve(bridge.storage.set(key, value)).catch(() => {});
    }
  }
  pendingWrites.clear();
  if (nativeLocalStorage) Object.defineProperty(globalThis, "localStorage", nativeLocalStorage);
  else delete globalThis.localStorage;
  delete globalThis.chrome;
  bridge = null;
}
