import { useEffect } from "react";
import { coreRequest } from "../api";
import { errorMessage, useAction, useResource } from "../lib/useResource";
import type { NativeHost } from "../native";
import { ActionButton, Panel } from "./ui";

export type WindowsAttachReport = {
  platform?: string;
  host: { running?: boolean; canReopen?: boolean; attachState?: string };
  last?: { error?: string };
};

const attachCopy: Record<string, string> = {
  attached: "已接入 Kimi Code，独立插件界面可用。",
  compatibility: "当前以兼容模式接入。重新打开 Kimi Code 可恢复独立插件界面。",
  failed: "运行中接入失败。请等当前回合结束，再重新打开 Kimi Code。",
  unattached: "Kimi Code 已打开，增强尚未接入。",
  reopening: "正在重新打开 Kimi Code…",
};

export async function requestWindowsReopen(host?: NativeHost | null) {
  if (host) return host.request<{ reopening: boolean }>("attach.reopen");
  return (await coreRequest<{ value: { reopening: boolean } }>("/desktop/actions/attach.reopen", { method: "POST", body: {} })).value;
}

export function WindowsAttachStatusPanel({ status, host, refresh }: {
  status: WindowsAttachReport;
  host?: NativeHost;
  refresh: () => void;
}) {
  const action = useAction<{ reopening: boolean }>();
  const state = status.host.attachState ?? "unattached";
  const reopening = state === "reopening";
  useEffect(() => {
    // A reopen takes a few seconds. Without this poll the panel stays on
    // 正在重新打开 until the user presses 刷新; stop as soon as it settles.
    if (!reopening) return;
    const timer = window.setTimeout(refresh, 1800);
    return () => window.clearTimeout(timer);
  }, [reopening, refresh]);
  const copy = attachCopy[state] ?? (status.host.running
    ? `当前接入状态：${state}。若界面未生效，可重新打开 Kimi Code 后再试。`
    : "请打开 Kimi Code。");
  return <div data-ekd-windows-attach-state={state}>
    <p role="status">{status.host.running || reopening ? copy : "请打开 Kimi Code。"}</p>
    <div className="inline-actions">
      {status.host.canReopen && state !== "attached" ? <ActionButton
        busy={action.busy || reopening}
        onClick={() => void action.run(async () => {
          const value = await requestWindowsReopen(host);
          refresh();
          return value;
        })}
      >立即重新打开</ActionButton> : null}
      <ActionButton variant="quiet" onClick={refresh}>刷新接入状态</ActionButton>
    </div>
    {action.error ? <p role="alert">{errorMessage(action.error)}</p> : null}
    {state === "failed" && status.last?.error ? <details><summary>诊断详情</summary><p>{status.last.error}</p></details> : null}
  </div>;
}

export function StandaloneWindowsAttachStatus() {
  const status = useResource("settings/windows-attach", async () => {
    const response = await coreRequest<{ value: WindowsAttachReport }>("/desktop/actions/attach.status", { method: "POST", body: {} });
    return response.value;
  });
  const isReopening = status.data?.host.attachState === "reopening";
  useEffect(() => {
    if (!isReopening || status.loading) return;
    const timeout = window.setTimeout(status.reload, 1800);
    return () => window.clearTimeout(timeout);
  }, [isReopening, status]);
  if (status.data?.platform !== "windows") return null;
  return <Panel title="Kimi Code 接入">
    <WindowsAttachStatusPanel status={status.data} refresh={status.reload} />
    {status.error ? <p role="alert">{errorMessage(status.error)}</p> : null}
  </Panel>;
}
