import { useSyncExternalStore } from "react";
import type { TaskPackage } from "../../adapters/types";
import { subscribePathname } from "./routes";

export interface HostContext {
  sessionId: string;
  projectId: string;
  title?: string;
  messages: TaskPackage["messages"];
  source: TaskPackage["source"];
  diff?: string;
}
export interface HostConnection {
  context?: HostContext;
  back: () => void;
  openBrowser?: (url: string) => Promise<void>;
}
let connection: HostConnection | null = null;
let context: HostContext | null = null;
const listeners = new Set<() => void>();
export function installHost(value: HostConnection | null) {
  connection = value;
  context = value?.context ?? null;
  for (const listener of listeners) listener();
}
/**
 * Clears the connection only when it is still the one this mount installed.
 * A panel that unmounts later must not detach a surface that mounted since.
 */
export function releaseHost(expected: HostConnection | null) {
  if (expected === null || connection !== expected) return;
  installHost(null);
}
export function setHandoffContext(value: HostContext) {
  context = value;
  for (const listener of listeners) listener();
}
export function useHostContext() {
  return useSyncExternalStore(listener => { listeners.add(listener); return () => { listeners.delete(listener); }; }, () => context, () => null);
}
export function hostConnection() { return connection; }
export function diagnosticMode() { return typeof window !== "undefined" && new URLSearchParams(window.location.search).get("mode") === "diagnostic"; }
export function useDiagnosticMode() {
  return useSyncExternalStore(subscribePathname, diagnosticMode, diagnosticMode);
}
