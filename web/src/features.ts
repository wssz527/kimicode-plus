import { coreRequest, type ChatThread } from "./api";
export interface WorkspaceSession { id: string; title: string; projectId: string; modelId: string; worktree: string; hostId: string; lastActivity: string; mainBusy: boolean; toolBusy: boolean; approvalPending: boolean; recovering: boolean; workerRunning: boolean; revision: number }
export interface Policy { id: string; scope: string; target: string; enabled: boolean; idleMinutes: number }
export interface WorkspaceLayout { columns: number; panes: { HostID: string; SessionID: string; ProjectID: string; Worktree: string }[]; reviewers: { ModelID: string; Role: string; Enforcement: string }[] }
export interface Migration { id: string; target: string; browser: string; mode: string; count: number; conflicts: string[]; skipped: number; state: string }
export interface ProviderThread extends ChatThread { providerId?: string; projectId?: string; lastRequest?: { id: string; state: string; note: string } }
export const features = {
  vault: () =>
    coreRequest<{ unlocked: boolean; refs: string[]; backend: string; osKeychain?: boolean; osKeychainAvailable?: boolean; osKeychainBackend?: string; legacyPasswordVault?: boolean }>("/credentials"),
  resetVault: () =>
    coreRequest<{ unlocked: boolean; refs: string[]; backend: string; osKeychain?: boolean; osKeychainAvailable?: boolean; osKeychainBackend?: string; legacyPasswordVault?: boolean }>("/credentials/reset", {
      method: "POST",
      body: { confirm: true },
    }),
  unlock: (password: string) => coreRequest("/credentials/unlock", { method: "POST", body: { password } }),
  lock: () => coreRequest("/credentials/lock", { method: "POST", body: {} }),
  saveSecret: (ref: string, value: string) => coreRequest("/credentials", { method: "PUT", body: { ref, value } }),
  deleteSecret: (ref: string) => coreRequest(`/credentials/${ref.split("/").map(encodeURIComponent).join("/")}`, { method: "DELETE" }),
  sessions: () => coreRequest<{ items: WorkspaceSession[] }>("/workspace/sessions"),
  saveSession: (s: WorkspaceSession) => coreRequest<WorkspaceSession>(`/workspace/sessions/${encodeURIComponent(s.id)}`, { method: "PUT", body: s }),
  compaction: () => coreRequest<{ policies: Policy[]; evaluations: { session: WorkspaceSession; policy: Policy; due: boolean; state: string; reason: string }[] }>("/workspace/compaction"),
  savePolicy: (p: Policy) => coreRequest<Policy>(`/workspace/compaction/${encodeURIComponent(p.id)}`, { method: "PUT", body: p }),
  layout: () => coreRequest<WorkspaceLayout>("/workspace/layout"),
  saveLayout: (layout: WorkspaceLayout) => coreRequest<WorkspaceLayout>("/workspace/layout", { method: "PUT", body: layout }),
  worktree: (repository: string, name: string, ref: string) => coreRequest<{ path: string }>("/workspace/worktrees", { method: "POST", body: { repository, name, ref } }),
  migrations: () => coreRequest<{ items: Migration[] }>("/browser/migrations"),
  previewMigration: (body: unknown) => coreRequest<Migration>("/browser/migrations/preview", { method: "POST", body }),
  applyMigration: (id: string, conflict: string) => coreRequest<Migration>(`/browser/migrations/${encodeURIComponent(id)}/apply`, { method: "POST", body: { conflict } }),
  permissions: () => coreRequest<{ items: { origin: string; permissions: Record<string, boolean> }[] }>("/browser/permissions"),
  savePermissions: (origin: string, permissions: Record<string, boolean>) => coreRequest("/browser/permissions", { method: "PUT", body: { origin, permissions } }),
  createThread: (body: { title: string; sourceId: string; model: string; providerId: string; projectId: string }) => coreRequest<ProviderThread>("/chat/threads", { method: "POST", body }),
  importUsage: (events: unknown[]) => coreRequest<{ added: number; updated: number; duplicates: number }>("/usage", { method: "POST", body: { events } }),
};
