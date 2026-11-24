import { useSyncExternalStore } from "react";
import type { IconName } from "./components/iconNames";

export interface RouteDefinition {
  path: string;
  label: string;
  shortLabel: string;
  icon: IconName;
  summary: string;
}

export const ROUTES: RouteDefinition[] = [
  {
    path: "/",
    label: "增强设置",
    shortLabel: "设置入口",
    icon: "overview",
    summary: "核心状态、任务流与模块健康",
  },
  {
    path: "/usage",
    label: "用量",
    shortLabel: "用量",
    icon: "usage",
    summary: "实际、估算与订阅额度分开记录",
  },
  {
    path: "/chat",
    label: "聊天",
    shortLabel: "聊天",
    icon: "chat",
    summary: "独立模型聊天与官网快速入口",
  },
  {
    path: "/handoff",
    label: "任务交接",
    shortLabel: "交接",
    icon: "handoff",
    summary: "预览、确认与持久 Outbox",
  },
  {
    path: "/plugins",
    label: "增强插件",
    shortLabel: "增强插件",
    icon: "plugins",
    summary: "管理 KimiCode+ 增强插件、授权能力与运行状态",
  },
  {
    path: "/settings",
    label: "设置",
    shortLabel: "设置",
    icon: "settings",
    summary: "修订号、安全模式与模块开关",
  },
  { path: "/native-settings", label: "执行配置", shortLabel: "执行配置", icon: "settings", summary: "工具、子代理模型与时限、步数和权限护栏" },
  { path: "/workspace", label: "会话与执行策略", shortLabel: "会话", icon: "handoff", summary: "会话引用、Worktree、布局与空闲策略" },
  { path: "/browser", label: "浏览器会话", shortLabel: "浏览器", icon: "chat", summary: "隔离数据迁移与网站操作权限" },
];

export function routeForPath(pathname: string): RouteDefinition {
  const clean = pathname.replace(/\/+$/, "") || "/";
  return ROUTES.find((route) => route.path === clean) ?? ROUTES[0]!;
}

export function normalizePathname(pathname: string): string {
  return pathname.replace(/\/+$/, "") || "/";
}

const pathListeners = new Set<() => void>();
let popstateInstalled = false;

function notifyPathname(): void {
  for (const listener of pathListeners) listener();
}

function ensurePopstateListener(): void {
  if (popstateInstalled || typeof window === "undefined") return;
  popstateInstalled = true;
  window.addEventListener("popstate", notifyPathname);
}

/** 订阅路径变化；返回取消订阅函数。供 useSyncExternalStore 与测试使用。 */
export function subscribePathname(listener: () => void): () => void {
  pathListeners.add(listener);
  ensurePopstateListener();
  return () => {
    pathListeners.delete(listener);
  };
}

/** 当前路径快照。无 window（Node 测试）时返回根路径。 */
export function pathnameSnapshot(): string {
  if (embeddedPath !== null) return embeddedPath;
  if (typeof window === "undefined") return "/";
  return normalizePathname(window.location.pathname);
}

/** 内部导航后通知所有订阅者；不依赖一次性的 popstate 事件。 */
export function notifyPathnameChange(): void {
  notifyPathname();
}

let embeddedPath: string | null = null;
export function useEmbeddedNavigation(path: string | null) {
  embeddedPath = path;
  notifyPathname();
}
export function navigate(path: string, options?: { diagnostic: boolean }): void {
  const next = normalizePathname(path);
  if (embeddedPath !== null) { embeddedPath = next; notifyPathname(); return; }
  if (typeof window === "undefined") return;
  let search = window.location.search;
  if (options) {
    const params = new URLSearchParams(search);
    if (options.diagnostic) params.set("mode", "diagnostic");
    else params.delete("mode");
    search = params.size ? `?${params}` : "";
  }
  if (normalizePathname(window.location.pathname) === next && window.location.search === search) return;
  window.history.pushState(null, "", next + search);
  notifyPathname();
}

/**
 * 订阅 window.location：内部 navigate 与浏览器前进/后退都会触发重渲染。
 * 直接读 location 而不订阅会让 URL 与 DOM 脱节。
 */
export function usePathname(): string {
  return useSyncExternalStore(subscribePathname, pathnameSnapshot, pathnameSnapshot);
}
