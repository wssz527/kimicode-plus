import { useState } from "react";
import { features } from "../features";
import { coreRequest } from "../api";
import { ActionButton, Field, Panel } from "./ui";
import { PageError } from "../AppShell";
import { useAction } from "../lib/useResource";
export function UsageImport({ onImported, monitor = false }: { onImported: () => void; monitor?: boolean }) {
  const [records, setRecords] = useState<unknown[]>([]);
  const [name, setName] = useState("");
  const action = useAction<{ added: number; updated: number; duplicates: number }>();
  return <Panel title={monitor ? "Monitor 兼容事件导入" : "导入用量记录"}><p className="field-hint">选择用户提供的 JSON 事件数组。每条需包含 providerId、model、requestId、attemptId、at、protocol 及 raw 用量对象。缺失计数保持 UNKNOWN。</p>
    <Field label="记录文件（最多 1 MiB）" htmlFor={monitor ? "monitor-records" : "usage-records"}><input id={monitor ? "monitor-records" : "usage-records"} type="file" accept=".json,application/json" disabled={action.busy} onChange={e => { const file = e.target.files?.[0]; setRecords([]); if (!file) return; void action.run(async () => { if (file.size > 1048576) throw new Error("文件超过 1 MiB"); const data: unknown = JSON.parse(await file.text()); if (!Array.isArray(data) || !data.every(v => v && typeof v === "object")) throw new Error("需要 JSON 事件对象数组"); setRecords(data); setName(file.name); return { added: 0, updated: 0, duplicates: 0 }; }); }} /></Field>
    <div className="form-actions"><span>{name} {records.length ? `${records.length} 条待导入` : ""}</span><ActionButton disabled={!records.length} busy={action.busy} onClick={() => { void action.run(async () => { const result = monitor ? await coreRequest<{ added: number; updated: number; duplicates: number }>("/plugins/monitor/import", { method: "POST", body: { events: records } }) : await features.importUsage(records.map(v => ({ ...(v as Record<string, unknown>), source: "manual" }))); setRecords([]); onImported(); return result; }); }}>确认导入{monitor ? " Monitor 记录" : "用户提供记录"}</ActionButton></div>
    {action.result ? <p role="status">新增 {action.result.added} · 更新 {action.result.updated} · 重复 {action.result.duplicates}</p> : null}<PageError error={action.error} />
  </Panel>;
}
