import { useEffect, useRef, useState } from "react";
import { api, type ModelImportResult, type ModelPreview, type Provider } from "../api";
import { ActionButton, Field, InfoNote } from "./ui";
import { useAction, useResource, errorMessage } from "../lib/useResource";

export function PublicCatalogImport({ providers, onChanged }: { providers: Provider[]; onChanged: () => void }) {
  const status = useResource("public/catalog", () => api.models.catalog());
  const [source, setSource] = useState("moonshotai");
  const [target, setTarget] = useState("");
  const [preview, setPreview] = useState<{ data: ModelPreview & { catalogHash: string }; source: string; target: string } | null>(null);
  const requestId = useRef(0);
  const selection = useRef({ source, target });
  selection.current = { source, target };
  useEffect(() => () => { requestId.current++; }, []);
  const action = useAction<ModelImportResult | ModelPreview | object>();
  const [message, setMessage] = useState("");

  function invalidate() {
    requestId.current++;
    setPreview(null);
    setMessage("");
  }
  async function refresh() {
    invalidate();
    const out = await action.run(() => api.models.refreshCatalog());
    status.reload();
    if (out) setMessage("公共目录已读取，请选择本地供应商并预览。");
  }
  async function inspect() {
    const request = ++requestId.current;
    setPreview(null);
    const result = await action.run(() => api.models.catalogImport({ sourceProvider: source, targetProvider: target, revision: 0, preview: true }));
    if (request !== requestId.current || source !== selection.current.source || target !== selection.current.target) return;
    if (result && "catalogHash" in result) setPreview({ data: result as ModelPreview & { catalogHash: string }, source, target });
  }
  async function save() {
    if (!preview || preview.source !== source || preview.target !== target) return;
    const result = await action.run(() => api.models.catalogImport({ sourceProvider: preview.source, targetProvider: preview.target, revision: preview.data.revision, catalogHash: preview.data.catalogHash, preview: false }));
    if (result) {
      invalidate();
      setMessage("已保存到增强核心。官方模型配置尚未连接。");
      onChanged();
    }
  }
  return <section className="import-card">
    <h3>公共目录 · models.dev</h3>
    <p className="import-copy">读取公开模型元数据，无需 Key。人工覆盖会保留，不调用模型。</p>
    <p className="num field-hint">{status.data?.state ?? "读取本地缓存中"} · 抓取 {status.data?.retrievedAt || "尚未获取"}</p>
    {status.data?.lastError ? <InfoNote tone="warn"><p>上次刷新失败，旧缓存保留。可再次主动刷新。</p></InfoNote> : null}
    <ActionButton onClick={refresh} busy={action.busy}>获取公共目录</ActionButton>
    <div className="form-grid">
      <Field label="公共目录供应商" htmlFor="public-source"><select id="public-source" className="select" value={source} disabled={action.busy} onChange={e => { invalidate(); setSource(e.target.value); }}>{!status.data?.providers.length ? <option value="moonshotai">先获取公共目录</option> : status.data.providers.map(p => <option key={p.id} value={p.id}>{p.name || p.id} · {p.models}</option>)}</select></Field>
      <Field label="导入到本地供应商实例" htmlFor="public-target"><select id="public-target" className="select" value={target} disabled={action.busy} onChange={e => { invalidate(); setTarget(e.target.value); }}><option value="">请选择实例</option>{providers.map(p => <option key={p.id} value={p.id}>{p.name} · {p.id}</option>)}</select></Field>
    </div>
    <div className="form-actions"><ActionButton onClick={inspect} disabled={!target || !status.data?.providers.length || action.busy}>预览公共导入</ActionButton><ActionButton variant="primary" onClick={save} disabled={!preview || preview.target !== target || preview.source !== source || action.busy}>确认公共导入</ActionButton></div>
    {preview ? <details open><summary>导入到 {providers.find(p => p.id === preview.target)?.name ?? preview.target} · {preview.target}</summary><p>来源 {preview.source} · 新增 {preview.data.summary.created} · 更新 {preview.data.summary.updated} · 不变 {preview.data.summary.unchanged}</p><pre className="code-block">{JSON.stringify(preview.data.changes.slice(0, 20), null, 2)}</pre></details> : null}
    {action.error || status.error ? <p role="alert">{errorMessage(action.error ?? status.error)}</p> : null}
    {message ? <p role="status">{message}</p> : null}
  </section>;
}
