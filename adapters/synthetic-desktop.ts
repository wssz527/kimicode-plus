import type {
  AdapterStatus,
  DeliveryReceipt,
  DesktopAdapter,
  DesktopReconcileReference,
  DesktopReconcileResult,
  DesktopSubmitRequest,
  DesktopSubmitResult,
  MockBehavior,
  SessionReference,
} from "./types.js";
import { packageHash } from "./canonical.js";

export interface SyntheticSession extends SessionReference {
  id: string;
  title: string;
  projectId: string;
  state: string;
  owner: string;
  synthetic: true;
}

export const SYNTHETIC_SESSIONS: SyntheticSession[] = [
  {
    id: "synthetic-session-bridge",
    title: "合成 Code 会话 · 交接桥接",
    projectId: "synthetic-project-workbench",
    state: "READY",
    owner: "synthetic-host",
    synthetic: true,
  },
  {
    id: "synthetic-session-usage",
    title: "合成 Code 会话 · 用量核对",
    projectId: "synthetic-project-workbench",
    state: "READY",
    owner: "synthetic-host",
    synthetic: true,
  },
  {
    id: "synthetic-session-plugins",
    title: "合成 Code 会话 · 插件隔离",
    projectId: "synthetic-project-tools",
    state: "UNKNOWN",
    owner: "synthetic-host",
    synthetic: true,
  },
];

interface MockDelivery {
  state: "DELIVERED" | "UNKNOWN";
  receipt: DeliveryReceipt | null;
  intentHash: string;
  at: string;
}

export class SyntheticDesktopAdapter implements DesktopAdapter {
  readonly kind = "synthetic-desktop";
  private mode: "AVAILABLE" | "UNAVAILABLE" | "CHANGED" = "AVAILABLE";
  private deliveries = new Map<string, MockDelivery>();

  async status(): Promise<AdapterStatus> {
    return {
      mode: this.mode,
      capabilities: this.capabilities(),
      detail:
        this.mode === "AVAILABLE"
          ? "本地合成宿主，未连接官方客户端"
          : this.mode === "CHANGED"
            ? "合成宿主字段变化：提交已停用"
            : "合成宿主不可用：读取历史仍可用",
      synthetic: true,
    };
  }

  setMode(mode: "AVAILABLE" | "UNAVAILABLE" | "CHANGED"): void {
    this.mode = mode;
  }

  private capabilities(): Record<string, boolean> {
    const available = this.mode === "AVAILABLE";
    return {
      models: available,
      sessions: this.mode !== "UNAVAILABLE",
      submit: available,
    };
  }

  async sessions(): Promise<SessionReference[]> {
    if (this.mode === "UNAVAILABLE") throw new Error("合成宿主不可用，无法读取会话");
    return SYNTHETIC_SESSIONS.map((session) => ({ ...session }));
  }

  async submit(request: DesktopSubmitRequest): Promise<DesktopSubmitResult> {
    if (this.mode !== "AVAILABLE") {
      throw new Error("合成宿主当前不可提交，请把适配器切回 AVAILABLE");
    }
    if (!request.confirmed) throw new Error("未确认的任务不会提交");
    const hash = await packageHash(request.task);
    const existing = this.deliveries.get(request.intent);
    if (existing) {
      if (existing.intentHash !== hash) throw new Error("同一意图对应不同任务内容（合成冲突）");
      return {
        state: existing.state,
        taskId: request.task.taskId,
        receipt: existing.receipt,
        replayed: true,
        synthetic: true,
      };
    }
    const behavior: MockBehavior = request.mockBehavior ?? "OK";
    const at = new Date().toISOString();
    const delivered = behavior === "OK";
    const delivery: MockDelivery = {
      state: delivered ? "DELIVERED" : "UNKNOWN",
      receipt: delivered
        ? {
            receiptId: `synthetic-receipt-${request.task.taskId}`,
            taskId: request.task.taskId,
            sessionId: request.task.sessionId,
            checkpoint: request.task.checkpoint,
            deliveredAt: at,
            mock: true,
          }
        : null,
      intentHash: hash,
      at,
    };
    this.deliveries.set(request.intent, delivery);
    return {
      state: delivery.state,
      taskId: request.task.taskId,
      receipt: delivery.receipt,
      replayed: false,
      synthetic: true,
    };
  }

  async reconcile(reference: DesktopReconcileReference): Promise<DesktopReconcileResult> {
    const entry = [...this.deliveries.entries()].find(
      ([, delivery]) => delivery.receipt?.taskId === reference.taskId,
    );
    if (!entry) {
      return {
        state: "UNKNOWN",
        receipt: null,
        checkedAt: new Date().toISOString(),
        synthetic: true,
      };
    }
    return {
      state: entry[1].state,
      receipt: entry[1].receipt,
      checkedAt: new Date().toISOString(),
      synthetic: true,
    };
  }

  async dispose(): Promise<void> {
    this.deliveries.clear();
  }
}

export function createSyntheticDesktopAdapter(): SyntheticDesktopAdapter {
  return new SyntheticDesktopAdapter();
}
