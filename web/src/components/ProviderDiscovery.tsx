import { useState } from "react";
import { api, type ModelRecord, type ModelPreview, type Provider } from "../api";
import { features } from "../features";
import { ActionButton, Field } from "./ui";
import { useAction } from "../lib/useResource";
import { PageError } from "../AppShell";
export function ProviderDiscovery({ providers, onChanged }: { providers: Provider[]; onChanged: () => void }) {
  const [id, setId] = useState("");
  const [preview, setPreview] = useState<{ target: string; records: ModelRecord[]; result: ModelPreview } | null>(null);
  const action = useAction<unknown>();
  const [message, setMessage] = useState("");
  return <section className="import-card"><h3>从供应商发现模型</h3><p className="field-hint">只读取已配置地址的模型目录。能力保持 UNKNOWN，密钥来自已解锁凭据库。</p>
    <Field label="供应商实例" htmlFor="live-provider"><select id="live-provider" className="select" value={id} disabled={action.busy} onChange={e => { setId(e.target.value); setPreview(null); setMessage(""); }}><option value="">请选择</option>{providers.map(p => <option key={p.id} value={p.id}>{p.name} · {p.baseUrl}</option>)}</select></Field>
    <div className="form-actions"><ActionButton busy={action.busy} disabled={!id} onClick={() => { void action.run(async () => { setPreview(null); const data = await features.discover(id); if (!data.records.length) { setMessage("目录为空，没有导入。"); return; } const result = await api.models.preview(data.records); setPreview({ target: id, records: data.records, result }); }); }}>读取并预览目录</ActionButton>
      <ActionButton variant="primary" disabled={!preview || preview.target !== id} busy={action.busy} onClick={() => { if (!preview || preview.target !== id) return; void action.run(async () => { await api.models.import(preview.records, preview.result.revision); setPreview(null); setMessage("已保存到增强核心，官方模型菜单尚未同步。"); onChanged(); }); }}>确认导入</ActionButton></div>
    {preview ? <p>目标 {preview.target} · {preview.records.length} 个模型 · 新增 {preview.result.summary.created} · 更新 {preview.result.summary.updated}</p> : null}
    {message ? <p role="status">{message}</p> : null}<PageError error={action.error} />
  </section>;
}
