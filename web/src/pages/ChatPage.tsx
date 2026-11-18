import { useEffect, useRef, useState } from "react";
import { api, type ChatThread } from "../api";
import { features, type ProviderThread } from "../features";
import { ActionButton, Field, PageHeader, Panel } from "../components/ui";
import { useAction, useResource } from "../lib/useResource";
import { PageError } from "../AppShell";
import { hostConnection, setHandoffContext, useHostContext } from "../host";
import { capabilitySummary, fetchModelOptions } from "../lib/models";
import { ModelSelect } from "../components/ModelSelect";
import { useHost } from "../native";
import { navigate } from "../routes";
export function ChatPage() {
  const sources = useResource("chat/sources", () => api.chat.sources());
  const settings = useResource("chat/settings", () => api.settings.get());
  const threads = useResource("chat/threads", () => api.chat.threads(0, 100));
  // The same official catalog every other picker reads. A chat thread is still
  // created against a provider from the enhancement core, so the options are
  // grouped by the provider half of the official key.
  const nativeHost = useHost();
  const models = useResource("chat/models", async () => (nativeHost ? fetchModelOptions(nativeHost.request) : []), { enabled: Boolean(nativeHost) });
  const [source, setSource] = useState("kimi");
  const [initialized, setInitialized] = useState(false);
  const [active, setActive] = useState<string | null>(null);
  const thread = useResource(`chat/thread/${active}`, () => active ? api.chat.thread(active) : Promise.resolve(null), { enabled: !!active });
  const [title, setTitle] = useState("");
  const [modelKey, setModelKey] = useState("");
  const model = modelKey;
  const chosen = models.data?.find(option => option.key === modelKey) ?? null;
  const provider = chosen?.provider ?? "";
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
    <PageError error={action.error ?? sources.error ?? thread.error ?? models.error} />
    {addingSource ? <Panel title="网站来源"><form className="form-grid" onSubmit={e => { e.preventDefault(); void action.run(async () => { await api.chat.addSource(newSource); sources.reload(); setAddingSource(false); }); }}>{([ ["id", "来源 ID"], ["name", "名称"], ["url", "HTTPS 地址"] ] as const).map(([key, label]) => <Field key={key} label={label} htmlFor={`source-${key}`}><input id={`source-${key}`} className="input" value={newSource[key]} onChange={e => setNewSource({ ...newSource, [key]: e.target.value })} /></Field>)}<ActionButton type="submit" busy={action.busy}>保存来源</ActionButton></form></Panel> : null}
    <div className="source-tabs" role="group" aria-label="聊天来源">{sources.data?.items.map(s => <button className={`btn ${source === s.id ? "btn-primary" : "btn-quiet"}`} key={s.id} onClick={() => { setSource(s.id); setActive(null); }}>{s.name}</button>)}</div>
    {currentSource?.url ? <Panel title={currentSource.name}><p>网站内容提取与消息发送尚未连接。打开网站只进行导航。</p><div className="form-actions">{host?.openBrowser ? <ActionButton busy={action.busy} onClick={() => { void action.run(() => host.openBrowser!(currentSource.url)); }}>在宿主浏览器打开</ActionButton> : <><span className="field-hint">宿主浏览器未连接</span><a className="btn btn-secondary" href={currentSource.url} target="_blank" rel="noopener noreferrer">使用系统浏览器打开</a></>}</div></Panel> : null}
    {source === "custom-model" ? <>
      <Panel title="独立轻量聊天"><form className="form-grid" onSubmit={e => { e.preventDefault(); void action.run(async () => { const created = await features.createThread({ title, sourceId: source, providerId: provider, model, projectId: context?.projectId ?? "" }); setActive(created.id); threads.reload(); setTitle(""); }); }}>
        <Field label="线程标题" htmlFor="chat-title"><input id="chat-title" className="input" value={title} onChange={e => setTitle(e.target.value)} /></Field>
        <Field
          label="模型"
          htmlFor="chat-model"
          hint={
            (models.data?.length ?? 0) === 0
              ? "还没有可选的模型，请先在 Kimi Code 设置 → 供应商中添加。"
              : chosen
                ? `${chosen.provider} · ${capabilitySummary(chosen) || "未声明能力"}`
                : "模型来自官方供应商配置，与子代理池和交接使用同一份列表。"
          }
        ><ModelSelect
            id="chat-model"
            options={models.data ?? []}
            value={modelKey}
            onChange={setModelKey}
          /></Field>
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
