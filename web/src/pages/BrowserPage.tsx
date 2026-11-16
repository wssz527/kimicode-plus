import { useState } from "react";
import { features, type Migration } from "../features";
import { ActionButton, Field, PageHeader, Panel, Toggle } from "../components/ui";
import { useAction, useResource } from "../lib/useResource";
import { PageError } from "../AppShell";
export function BrowserPage() {
  const history = useResource("browser/migrations", features.migrations);
  const permissions = useResource("browser/permissions", features.permissions);
  const [browser, setBrowser] = useState("chrome");
  const [target, setTarget] = useState("personal");
  const [mode, setMode] = useState("SITES");
  const [sites, setSites] = useState("www.kimi.com");
  const [cookies, setCookies] = useState<unknown[]>([]);
  const [fileName, setFileName] = useState("");
  const [consent, setConsent] = useState(false);
  const [preview, setPreview] = useState<Migration | null>(null);
  const [origin, setOrigin] = useState("https://www.kimi.com");
  const [allowed, setAllowed] = useState({ read: false, fillDraft: false, send: false, upload: false });
  const action = useAction<unknown>();
  const [message, setMessage] = useState("");
  return <div className="page"><PageHeader title="浏览器会话" description="导入用户主动提供的 Cookie JSON 到增强隔离加密库。不会读取 Chrome/Edge 的数据库、解密系统凭据或改动当前浏览器。原生会话恢复尚未连接。" />
    <PageError error={action.error ?? history.error ?? permissions.error} />{message ? <p role="status">{message}</p> : null}
    <Panel title="导出数据迁移"><fieldset disabled={action.busy} className="form-grid">
      <Field label="来源浏览器" htmlFor="migration-browser"><select id="migration-browser" className="select" value={browser} onChange={e => { setBrowser(e.target.value); setPreview(null); }}><option value="chrome">Chrome</option><option value="edge">Edge</option><option value="export">其他浏览器导出</option></select></Field>
      <Field label="隔离目标" htmlFor="migration-target"><input id="migration-target" className="input" value={target} onChange={e => { setTarget(e.target.value); setPreview(null); }} /></Field>
      <Field label="迁移范围" htmlFor="migration-mode"><select id="migration-mode" className="select" value={mode} onChange={e => { setMode(e.target.value); setPreview(null); }}><option value="SITES">指定网站</option><option value="ALL">全部可导入数据</option></select></Field>
      <Field label="网站域名（逗号分隔）" htmlFor="migration-sites"><input id="migration-sites" disabled={mode === "ALL"} className="input" value={sites} onChange={e => { setSites(e.target.value); setPreview(null); }} /></Field>
      <Field label="Cookie JSON 数组（最多 1 MiB）" htmlFor="migration-file" hint="字段：domain、path、name、value，可选 secure、httpOnly、sameSite、expires、partitionKey、hostOnly、storeId。"><input id="migration-file" type="file" accept=".json,application/json" onChange={e => { const file = e.target.files?.[0]; setPreview(null); setCookies([]); if (!file) return; void action.run(async () => { if (file.size > 1048576) throw new Error("文件超过 1 MiB"); const data: unknown = JSON.parse(await file.text()); if (!Array.isArray(data)) throw new Error("请提供 JSON 数组"); setCookies(data); setFileName(file.name); }); }} /></Field>
      <label><input type="checkbox" checked={consent} onChange={e => { setConsent(e.target.checked); setPreview(null); }} /> 同意将这个文件中的数据加密保存到所选隔离目标</label>
      <p>{fileName ? `${fileName} · ${cookies.length} 条` : "尚未选择文件"}</p>
      <ActionButton disabled={!consent || !cookies.length || !target} onClick={() => { void action.run(async () => { setPreview(await features.previewMigration({ browser, target, mode, sites: sites.split(",").map(s => s.trim()).filter(Boolean), cookies, consent })); }); }}>预览迁移</ActionButton>
    </fieldset>
    {preview ? <div className="migration-preview"><p>隔离目标 {preview.target} · 可导入 {preview.count} · 跳过 {preview.skipped} · 冲突 {preview.conflicts.length}</p><ul>{preview.conflicts.map(c => <li key={c}>{c}</li>)}</ul><div className="form-actions">{[ ["KEEP", "保留现有冲突项"], ["REPLACE", "替换冲突项"] ].map(([value, label]) => <ActionButton key={value} busy={action.busy} disabled={!consent} onClick={() => { void action.run(async () => { await features.applyMigration(preview.id, value!); setPreview(null); setCookies([]); setMessage("已加密保存到隔离会话库，未写入原生浏览器。"); history.reload(); }); }}>{label}并保存</ActionButton>)}</div></div> : null}
    </Panel>
    <Panel title="Agent 网站操作权限"><p className="field-hint">权限与登录数据分别保存。保存权限不会自动执行网站操作。</p><div className="form-grid"><Field label="网站来源" htmlFor="permission-origin"><input id="permission-origin" className="input" value={origin} onChange={e => setOrigin(e.target.value)} /></Field><div className="form-actions">{([ ["read", "读取消息"], ["fillDraft", "填写草稿"], ["send", "发送"], ["upload", "附件上传"] ] as const).map(([key, label]) => <Toggle key={key} label={label} checked={allowed[key]} onChange={value => setAllowed({ ...allowed, [key]: value })} />)}</div><ActionButton busy={action.busy} onClick={() => { void action.run(async () => { await features.savePermissions(origin, allowed); permissions.reload(); setMessage("网站权限已保存。"); }); }}>保存网站权限</ActionButton></div><ul className="reference-list">{permissions.data?.items.map(item => <li key={item.origin}><span>{item.origin}</span><span>{Object.entries(item.permissions).filter(([, value]) => value).map(([key]) => key).join(" / ") || "全部关闭"}</span></li>)}</ul></Panel>
    <Panel title="迁移记录"><ul className="reference-list">{history.data?.items.map(item => <li key={item.id}><span>{item.browser} · {item.target} · {item.count} 条</span><small>{item.state}</small></li>)}</ul></Panel>
  </div>;
}
