import { useEffect, useState } from "react";
import { features, type WorkspaceSession, type WorkspaceLayout, type Policy } from "../features";
import { ActionButton, Field, PageHeader, Panel } from "../components/ui";
import { useAction, useResource } from "../lib/useResource";
import { PageError } from "../AppShell";
const blank = (): WorkspaceSession => ({ id: "", title: "", projectId: "", modelId: "", worktree: "", hostId: "", lastActivity: new Date().toISOString(), mainBusy: false, toolBusy: false, approvalPending: false, recovering: false, workerRunning: false, revision: 0 });
export function WorkspacePage() {
  const sessions = useResource("workspace/sessions", features.sessions);
  const compaction = useResource("workspace/compaction", features.compaction);
  const layout = useResource("workspace/layout", features.layout);
  const [draft, setDraft] = useState(blank);
  const [policy, setPolicy] = useState<Policy>({ id: "default", scope: "global", target: "*", idleMinutes: 25, enabled: false });
  const [columns, setColumns] = useState(2);
  const [reviewModel, setReviewModel] = useState("");
  const [repository, setRepository] = useState("");
  const [treeName, setTreeName] = useState("");
  const [gitRef, setGitRef] = useState("HEAD");
  const [message, setMessage] = useState("");
  const action = useAction<unknown>();
  useEffect(() => { if (layout.data) { setColumns(layout.data.columns); setReviewModel(layout.data.reviewers[0]?.ModelID ?? ""); } }, [layout.data]);
  useEffect(() => { const timer = setInterval(compaction.reload, 30_000); return () => clearInterval(timer); }, [compaction.reload]);
  return <div className="page"><PageHeader title="会话与执行策略" description="保存本地会话引用、组织工作树并评估空闲条件。这里管理本地工作台偏好；原生同屏与自动化动作由对应真实功能执行。" actions={<ActionButton onClick={() => { sessions.reload(); compaction.reload(); }}>刷新状态</ActionButton>} />
    <PageError error={action.error ?? sessions.error ?? compaction.error ?? layout.error} />{message ? <p role="status">{message}</p> : null}
    <Panel title="会话引用"><ul className="reference-list">{sessions.data?.items.map(s => <li key={s.id}><button className="link-btn" onClick={() => setDraft(s)}>{s.title} · {s.id}</button><small>{s.projectId} {s.workerRunning ? "· Worker 运行中" : ""}</small></li>)}</ul>
      <form className="form-grid" onSubmit={e => { e.preventDefault(); void action.run(async () => { setDraft(await features.saveSession(draft)); sessions.reload(); compaction.reload(); setMessage("本地会话引用已保存。"); }); }}>
        {([ ["id", "引用 ID"], ["title", "标题"], ["projectId", "项目 ID"], ["modelId", "模型 ID"], ["worktree", "Worktree 路径"], ["hostId", "宿主 ID（可选）"] ] as const).map(([key, label]) => <Field key={key} label={label} htmlFor={`session-${key}`}><input id={`session-${key}`} className="input" value={draft[key]} disabled={key === "id" && draft.revision > 0} onChange={e => setDraft({ ...draft, [key]: e.target.value })} /></Field>)}
        <Field label="最近活动" htmlFor="session-activity"><input id="session-activity" type="datetime-local" className="input" value={draft.lastActivity ? new Date(new Date(draft.lastActivity).getTime() - new Date().getTimezoneOffset() * 60000).toISOString().slice(0, 16) : ""} onChange={e => { if (e.target.value) setDraft({ ...draft, lastActivity: new Date(e.target.value).toISOString() }); }} /></Field>
        <div className="form-actions">{([ ["mainBusy", "主控忙"], ["toolBusy", "工具运行"], ["approvalPending", "等待批准"], ["workerRunning", "Worker 运行"] ] as const).map(([key, label]) => <label key={key}><input type="checkbox" checked={draft[key]} onChange={e => setDraft({ ...draft, [key]: e.target.checked })} /> {label}</label>)}</div>
        <div className="form-actions"><ActionButton type="submit" busy={action.busy} disabled={!draft.id || !draft.title}>保存引用</ActionButton><ActionButton onClick={() => setDraft(blank())}>新建引用</ActionButton></div>
      </form>
    </Panel>
    <Panel title="空闲压缩策略"><p className="field-hint">优先级：会话、项目、模型、全局。默认 25 分钟，Worker、工具、恢复或批准等待会阻止压缩。这里只评估，不发送原生压缩请求。</p>
      <form className="form-grid" onSubmit={e => { e.preventDefault(); void action.run(async () => { await features.savePolicy(policy); compaction.reload(); setMessage("策略已保存，原生执行仍未连接。"); }); }}>
        <Field label="策略 ID" htmlFor="policy-id"><input id="policy-id" className="input" value={policy.id} onChange={e => setPolicy({ ...policy, id: e.target.value })} /></Field>
        <Field label="作用域" htmlFor="policy-scope"><select id="policy-scope" className="select" value={policy.scope} onChange={e => setPolicy({ ...policy, scope: e.target.value })}><option value="global">全局</option><option value="model">模型</option><option value="project">项目</option><option value="session">会话</option></select></Field>
        <Field label="目标 ID（全局为 *）" htmlFor="policy-target"><input id="policy-target" className="input" value={policy.target} onChange={e => setPolicy({ ...policy, target: e.target.value })} /></Field>
        <Field label="空闲分钟" htmlFor="policy-minutes"><input id="policy-minutes" type="number" min="1" max="1440" className="input" value={policy.idleMinutes} onChange={e => setPolicy({ ...policy, idleMinutes: Number(e.target.value) })} /></Field>
        <label><input type="checkbox" checked={policy.enabled} onChange={e => setPolicy({ ...policy, enabled: e.target.checked })} /> 启用条件评估</label><ActionButton type="submit" busy={action.busy}>保存策略</ActionButton>
      </form>
      <ul className="reference-list">{compaction.data?.policies.map(p => <li key={p.id}><button className="link-btn" onClick={() => setPolicy(p)}>{p.id} · {p.scope}/{p.target} · {p.idleMinutes} 分钟</button></li>)}{compaction.data?.evaluations.map(v => <li key={v.session.id}><span>{v.session.title}</span><span>{v.reason}</span></li>)}</ul>
    </Panel>
    <Panel title="布局与审查偏好"><div className="form-grid"><Field label="列数" htmlFor="layout-columns"><select id="layout-columns" className="select" value={columns} onChange={e => setColumns(Number(e.target.value))}>{[1, 2, 3].map(n => <option key={n}>{n}</option>)}</select></Field><Field label="Reviewer 模型" htmlFor="review-model"><input id="review-model" className="input" value={reviewModel} onChange={e => setReviewModel(e.target.value)} /></Field><ActionButton busy={action.busy} onClick={() => { void action.run(async () => { const next: WorkspaceLayout = { columns, panes: (sessions.data?.items ?? []).map(s => ({ HostID: s.hostId, SessionID: s.id, ProjectID: s.projectId, Worktree: s.worktree })), reviewers: reviewModel ? [{ ModelID: reviewModel, Role: "reviewer", Enforcement: "LOCAL_PREFERENCE_ONLY" }] : [] }; await features.saveLayout(next); layout.reload(); setMessage("布局与 Reviewer 偏好已保存，未接管官方视图。"); }); }}>保存布局</ActionButton></div>
      <div className="session-panes" data-columns={columns}>{(sessions.data?.items ?? []).map(s => <article className="session-pane" key={s.id}><h3>{s.title}</h3><p>{s.projectId}</p><code>{s.worktree || "未绑定 Worktree"}</code><p className="field-hint">会话引用 · 原生视图未连接</p></article>)}</div>
    </Panel>
    <Panel title="创建隔离 Worktree"><p className="field-hint">在指定仓库的 .ekd-worktrees/名称 创建独立工作树，使用 detached HEAD，不提交或推送。</p><div className="form-grid"><Field label="仓库绝对路径" htmlFor="tree-repository"><input id="tree-repository" className="input" value={repository} onChange={e => setRepository(e.target.value)} /></Field><Field label="名称" htmlFor="tree-name"><input id="tree-name" className="input" value={treeName} onChange={e => setTreeName(e.target.value)} /></Field><Field label="Git 引用" htmlFor="tree-ref"><input id="tree-ref" className="input" value={gitRef} onChange={e => setGitRef(e.target.value)} /></Field><ActionButton busy={action.busy} disabled={!repository || !treeName} onClick={() => { void action.run(async () => { const result = await features.worktree(repository, treeName, gitRef); setDraft({ ...draft, worktree: result.path }); setMessage(`已创建 ${result.path}，可保存到会话引用。`); }); }}>创建 Worktree</ActionButton></div></Panel>
  </div>;
}
