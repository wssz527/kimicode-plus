import { useEffect, useRef, useState } from "react";
import { api, type ChatThread } from "../api";
import { features, type ProviderThread } from "../features";
import { ActionButton, Field, PageHeader, Panel } from "../components/ui";
import { useAction, useResource } from "../lib/useResource";
import { PageError } from "../AppShell";
import { hostConnection, setHandoffContext, useHostContext } from "../host";
import { navigate } from "../routes";
export function ChatPage() {
  const sources = useResource("chat/sources", () => api.chat.sources());
  const settings = useResource("chat/settings", () => api.settings.get());
  const threads = useResource("chat/threads", () => api.chat.threads(0, 100));
  const providers = useResource("chat/providers", () => api.providers.list());
  const models = useResource("chat/models", () => api.models.list(0, 1000));
  const [source, setSource] = useState("kimi");
  const [initialized, setInitialized] = useState(false);
  const [active, setActive] = useState<string | null>(null);
  const thread = useResource(`chat/thread/${active}`, () => active ? api.chat.thread(active) : Promise.resolve(null), { enabled: !!active });
  const [title, setTitle] = useState("");
  const [provider, setProvider] = useState("");
  const [model, setModel] = useState("");
  const [text, setText] = useState("");
  const messageAttempt = useRef<{ thread: string; text: string; intent: string } | null>(null);
  const [selected, setSelected] = useState<string[]>([]);
  const [newSource, setNewSource] = useState({ id: "", name: "", url: "https://" });
  const [addingSource, setAddingSource] = useState(false);
  const action = useAction<unknown>();
  const context = useHostContext();
  const currentSource = sources.data?.items.find(s => s.id === source);
  const current = thread.data?.id === active ? thread.data as ProviderThread : null;
  useEffect(() => { if (!initialized && settings.data) { setSource(settings.data.defaultChatSource); setInitialized(true); } }, [settings.data, initialized]);
  useEffect(() => { setSelected([]); setText(""); }, [active]);
  const host = hostConnection();
  function handoff(t: ChatThread) {
    const messages = (t.messages ?? []).filter(m => selected.includes(m.id));
    setHandoffContext({ sessionId: context?.sessionId ?? "", projectId: context?.projectId ?? "", title: t.title, messages, source: { site: "custom-model", url: "https://enhanced-kimi.local/chat", threadId: t.id, messageIds: messages.map(m => m.id) }, diff: context?.diff });
    navigate("/handoff");
  }
  return <div className="page"><PageHeader title="聊天" description="默认使用 Kimi 官网。轻量模型聊天保存独立历史，每次仅发送最近 12 条消息；只有点击发送才请求所选供应商。" actions={<ActionButton onClick={() => setAddingSource(!addingSource)}>添加网站来源</ActionButton>} />
    <PageError error={action.error ?? sources.error ?? thread.error ?? providers.error ?? models.error} />
    {addingSource ? <Panel title="网站来源"><form className="form-grid" onSubmit={e => { e.preventDefault(); void action.run(async () => { await api.chat.addSource(newSource); sources.reload(); setAddingSource(false); }); }}>{([ ["id", "来源 ID"], ["name", "名称"], ["url", "HTTPS 地址"] ] as const).map(([key, label]) => <Field key={key} label={label} htmlFor={`source-${key}`}><input id={`source-${key}`} className="input" value={newSource[key]} onChange={e => setNewSource({ ...newSource, [key]: e.target.value })} /></Field>)}<ActionButton type="submit" busy={action.busy}>保存来源</ActionButton></form></Panel> : null}
    <div className="source-tabs" role="group" aria-label="聊天来源">{sources.data?.items.map(s => <button className={`btn ${source === s.id ? "btn-primary" : "btn-quiet"}`} key={s.id} onClick={() => { setSource(s.id); setActive(null); }}>{s.name}</button>)}</div>
    {currentSource?.url ? <Panel title={currentSource.name}><p>网站内容提取与消息发送尚未连接。打开网站只进行导航。</p><div className="form-actions">{host?.openBrowser ? <ActionButton busy={action.busy} onClick={() => { void action.run(() => host.openBrowser!(currentSource.url)); }}>在宿主浏览器打开</ActionButton> : <><span className="field-hint">宿主浏览器未连接</span><a className="btn btn-secondary" href={currentSource.url} target="_blank" rel="noopener noreferrer">使用系统浏览器打开</a></>}</div></Panel> : null}
    {source === "custom-model" ? <>
      <Panel title="独立轻量聊天"><form className="form-grid" onSubmit={e => { e.preventDefault(); void action.run(async () => { const created = await features.createThread({ title, sourceId: source, providerId: provider, model, projectId: context?.projectId ?? "" }); setActive(created.id); threads.reload(); setTitle(""); }); }}>
        <Field label="线程标题" htmlFor="chat-title"><input id="chat-title" className="input" value={title} onChange={e => setTitle(e.target.value)} /></Field>
        <Field label="供应商" htmlFor="chat-provider"><select id="chat-provider" className="select" value={provider} onChange={e => { setProvider(e.target.value); setModel(""); }}><option value="">请选择</option>{providers.data?.items.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}</select></Field>
        <Field label="模型 ID" htmlFor="chat-model"><input id="chat-model" className="input" list="chat-model-options" value={model} onChange={e => setModel(e.target.value)} /><datalist id="chat-model-options">{models.data?.items.filter(m => m.providerId === provider).map(m => <option key={m.id} value={m.id}>{m.name}</option>)}</datalist></Field>
        <ActionButton type="submit" busy={action.busy} disabled={!title || !provider || !model}>新建线程</ActionButton>
      </form></Panel>
      <div className="chat-history-layout"><aside><ul className="reference-list">{threads.data?.items.filter(t => t.sourceId === source).map(t => <li key={t.id}><button className="link-btn" aria-pressed={active === t.id} onClick={() => setActive(t.id)}>{t.title}{t.mode === "MOCK" ? "（旧模拟记录）" : ""}</button></li>)}</ul></aside>
      <Panel title={current?.title ?? "选择或新建线程"}>{current ? <>
        <p className="field-hint">{current.providerId} / {current.model}</p>
        <ol className="messages">{current.messages?.map(m => <li className={`message message-${m.role}`} key={m.id}><div className="message-meta"><label><input type="checkbox" checked={selected.includes(m.id)} onChange={e => setSelected(e.target.checked ? [...selected, m.id] : selected.filter(id => id !== m.id))} /> {m.role === "user" ? "你" : "助手"}</label></div><p>{m.text}</p></li>)}</ol>
        {current.lastRequest ? <p role="status" className="field-hint">{current.lastRequest.state} · {current.lastRequest.note}</p> : null}
        <form onSubmit={e => { e.preventDefault(); void action.run(async () => { if (!messageAttempt.current || messageAttempt.current.thread !== current.id || messageAttempt.current.text !== text) messageAttempt.current = { thread: current.id, text, intent: crypto.randomUUID() }; await api.chat.send(current.id, text, messageAttempt.current.intent); messageAttempt.current = null; setText(""); thread.reload(); threads.reload(); }); }}>
          <Field label="消息" htmlFor="chat-message"><textarea id="chat-message" className="textarea" rows={4} maxLength={32000} value={text} onChange={e => setText(e.target.value)} /></Field>
          <div className="form-actions"><ActionButton variant="primary" type="submit" busy={action.busy} disabled={!text.trim() || current.lastRequest?.state === "UNKNOWN" || current.lastRequest?.state === "INFLIGHT"}>{current.mode === "MOCK" ? "发送模拟消息" : "发送到所选模型"}</ActionButton><ActionButton disabled={!selected.length} onClick={() => handoff(current)}>将选中消息交接到 Code</ActionButton><ActionButton onClick={thread.reload}>核对请求状态</ActionButton></div>
        </form>
      </> : <p className="field-hint">历史仅属于增强程序，与官方会话分开。</p>}</Panel></div>
    </> : null}
  </div>;
}
