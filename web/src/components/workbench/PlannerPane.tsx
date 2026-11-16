import { useEffect, useRef, useState } from "react";
import { preferPlannerThread, type DemoBootstrap, type DemoHost, type PlannerOptions, type PlannerThread } from "../../native";
import { Icon } from "../Icon";
import { EmptyState, Spinner } from "../ui";
import { Composer, HandoffForm, PlanMessage, errorText, type HandoffMode } from "./pieces";
import { ControlMenu } from "./ControlMenu";

type PresetId = "clean" | "review" | "full" | "custom";
const PRESETS: { id: PresetId; label: string; hint: string; options: PlannerOptions }[] = [
  { id: "clean", label: "纯净规划", hint: "不带上下文、不加载 skill、只读", options: { context: false, skills: false, tools: false, readOnly: true } },
  { id: "review", label: "项目审查", hint: "读项目上下文与 skill，工具只读", options: { context: true, skills: true, tools: true, readOnly: true } },
  { id: "full", label: "全功能", hint: "上下文、skill、可写工具全开", options: { context: true, skills: true, tools: true, readOnly: false } },
];
const CUSTOM_KEYS: { key: keyof PlannerOptions; label: string; hint: string }[] = [
  { key: "context", label: "注入项目上下文", hint: "带上当前工作区说明" },
  { key: "skills", label: "加载 skill", hint: "允许规划阶段读取技能说明" },
  { key: "tools", label: "启用工具", hint: "允许读取与检索类工具" },
  { key: "readOnly", label: "工具只读", hint: "关闭后可写工具仍需逐次批准" },
];
function presetOf(options: PlannerOptions): PresetId {
  return PRESETS.find(preset => CUSTOM_KEYS.every(({ key }) => preset.options[key] === options[key]))?.id ?? "custom";
}

export function PlannerPane({ host, boot, hidden }: { host: DemoHost; boot: DemoBootstrap; hidden?: boolean }) {
  const [threads, setThreads] = useState(boot.threads);
  const [thread, setThread] = useState<PlannerThread | null>(boot.threads[0] ?? null);
  const [activeId, setActiveId] = useState(boot.threads[0]?.id ?? "");
  const [model, setModel] = useState(boot.threads[0]?.model || boot.models[0]?.id || "");
  const [options, setOptions] = useState<PlannerOptions>(boot.threads[0]?.options ?? PRESETS[0]!.options);
  const [customOpen, setCustomOpen] = useState(false);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<string[]>([]);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [configNote, setConfigNote] = useState("");
  const [handoffText, setHandoffText] = useState("");
  const [handoffBusy, setHandoffBusy] = useState(false);
  const [approvalBusy, setApprovalBusy] = useState(false);
  const [handoffError, setHandoffError] = useState<string | null>(null);
  const [handoffResult, setHandoffResult] = useState<string | null>(null);
  const listRef = useRef<HTMLOListElement>(null);
  const locked = busy || thread?.status === "running" || thread?.status === "quota";

  function receive(next: PlannerThread) {
    setThread(current => preferPlannerThread(current, next) ? next : current);
    setThreads(current => current.map(item => item.id === next.id && preferPlannerThread(item, next) ? next : item));
  }
  useEffect(() => {
    if (!activeId) return;
    let alive = true;
    host.request<PlannerThread>("planner.get", { id: activeId }).then(next => { if (alive) receive(next); }).catch(cause => { if (alive) setError(errorText(cause)); });
    const unsubscribe = host.subscribe<PlannerThread>(`planner:${activeId}`, receive);
    return () => { alive = false; unsubscribe(); };
  }, [activeId, host]);
  useEffect(() => {
    if (!activeId || thread?.status !== "running") return;
    let alive = true;
    let pending = false;
    const refresh = async () => {
      if (pending) return;
      pending = true;
      try {
        const next = await host.request<PlannerThread>("planner.get", { id: activeId });
        if (alive) receive(next);
      } catch (cause) {
        if (alive) setError(errorText(cause));
      } finally {
        pending = false;
      }
    };
    void refresh();
    const timer = window.setInterval(() => void refresh(), 250);
    return () => { alive = false; window.clearInterval(timer); };
  }, [activeId, host, thread?.status]);
  useEffect(() => {
    listRef.current?.parentElement?.scrollTo({ top: listRef.current.parentElement.scrollHeight });
  }, [thread?.messages, thread?.status]);
  useEffect(() => {
    setSelected([]);
    setConfirmDelete(false);
    setCustomOpen(false);
    setConfigNote("");
    if (thread) { setOptions(thread.options); setModel(thread.model); }
  }, [thread?.id]);

  async function configure(nextModel: string, nextOptions: PlannerOptions) {
    setModel(nextModel);
    setOptions(nextOptions);
    setConfigNote("");
    if (!activeId) return;
    setBusy(true);
    setError(null);
    try {
      receive(await host.request<PlannerThread>("planner.configure", { id: activeId, model: nextModel, options: nextOptions }));
      setConfigNote("已应用到当前线程，下次发送生效");
    } catch (cause) {
      setError(errorText(cause));
      if (thread) { setModel(thread.model); setOptions(thread.options); }
    } finally { setBusy(false); }
  }
  async function open(id: string) {
    setError(null);
    try {
      const next = await host.request<PlannerThread>("planner.get", { id });
      setActiveId(id); receive(next);
    } catch (cause) { setError(errorText(cause)); }
  }
  async function newThread() {
    const created = await host.request<PlannerThread>("planner.create", { model, options });
    setThreads(current => [created, ...current]);
    setThread(created); setActiveId(created.id);
    return created.id;
  }
  async function create() {
    setBusy(true); setError(null);
    try { await newThread(); }
    catch (cause) { setError(errorText(cause)); }
    finally { setBusy(false); }
  }
  async function send() {
    const text = draft.trim();
    if (!text) return;
    setBusy(true); setError(null); setDraft("");
    try {
      const id = activeId || await newThread();
      receive(await host.request<PlannerThread>("planner.send", { id, text, model, options, sourceSessionId: host.activeSession() }));
    } catch (cause) { setError(errorText(cause)); setDraft(text); }
    finally { setBusy(false); }
  }
  async function stop() {
    try { receive(await host.request<PlannerThread>("planner.stop", { id: activeId })); }
    catch (cause) { setError(errorText(cause)); }
  }
  async function remove() {
    try {
      await host.request("planner.delete", { id: activeId });
      const rest = threads.filter(item => item.id !== activeId);
      setThreads(rest); setThread(rest[0] ?? null); setActiveId(rest[0]?.id ?? "");
      setConfirmDelete(false);
    } catch (cause) { setError(errorText(cause)); }
  }
  function pick(messageId: string) {
    const next = selected.includes(messageId) ? selected.filter(id => id !== messageId) : [...selected, messageId];
    setSelected(next);
    setHandoffText((thread?.messages ?? []).filter(message => next.includes(message.id)).map(message => message.text).join("\n\n"));
    setHandoffResult(null); setHandoffError(null);
  }
  async function submitHandoff(target: { sessionId?: string; mode: HandoffMode }) {
    setHandoffBusy(true); setHandoffError(null);
    try {
      const result = await host.request<{ sessionId: string }>("handoff", { targetId: target.sessionId, text: handoffText, mode: target.mode });
      setHandoffResult(result.sessionId);
    } catch (cause) { setHandoffError(errorText(cause)); }
    finally { setHandoffBusy(false); }
  }
  async function approve(allow: boolean) {
    if (!thread?.approval) return;
    setApprovalBusy(true); setError(null);
    try { receive(await host.request<PlannerThread>("planner.approve", { id: thread.id, approvalId: thread.approval.id, allow })); }
    catch (cause) { setError(errorText(cause)); }
    finally { setApprovalBusy(false); }
  }
  const messages = thread?.messages ?? [];

  return (
    <div className="wb-pane wb-plan" hidden={hidden}>
      <div className="wb-plan-tools"><span>{thread?.title || "新的规划"}</span><ControlMenu label="规划配置" icon="settings">
      <div className="wb-plan-config">
        <div className="wb-bar">
          <select className="wb-select" aria-label="历史规划线程" value={activeId} onChange={event => void open(event.target.value)}>
            {!threads.length ? <option value="">尚无规划线程</option> : null}
            {threads.map(item => <option key={item.id} value={item.id}>{item.title || "未命名规划"}</option>)}
          </select>
          <select className="wb-select wb-select-model" aria-label="规划模型" value={model} disabled={locked} onChange={event => void configure(event.target.value, options)}>
            {!boot.models.length ? <option value={model}>{model || "未提供模型"}</option> : null}
            {boot.models.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
          </select>
          <button type="button" className="wb-icon-btn" title="新建规划线程" onClick={() => void create()} disabled={busy}><Icon name="plus" size={15} /></button>
          <button type="button" className="wb-icon-btn" title="删除当前线程" onClick={() => setConfirmDelete(!confirmDelete)} disabled={!activeId || locked}><Icon name="trash" size={15} /></button>
        </div>
        <div className="wb-presets" role="group" aria-label="规划预设">
          {PRESETS.map(item => (
            <button type="button" key={item.id} className={`wb-preset ${presetOf(options) === item.id ? "is-active" : ""}`} aria-pressed={presetOf(options) === item.id} disabled={locked} title={item.hint} onClick={() => void configure(model, item.options)}>{item.label}</button>
          ))}
          <button type="button" className={`wb-preset ${presetOf(options) === "custom" ? "is-active" : ""}`} aria-expanded={customOpen} onClick={() => setCustomOpen(!customOpen)}>
            自定义 <Icon name={customOpen ? "chevronUp" : "chevronDown"} size={12} />
          </button>
        </div>
      </div>
      {confirmDelete ? (
        <div className="wb-bar" role="alert">
          <span className="wb-hint">删除“{thread?.title}”？此操作无法撤销。</span>
          <button className="wb-btn is-compact" onClick={() => void remove()}>确认删除</button>
          <button className="wb-btn is-compact" onClick={() => setConfirmDelete(false)}>取消</button>
        </div>
      ) : null}
      {customOpen ? (
        <div className="wb-custom">
          {CUSTOM_KEYS.map(item => (
            <label key={item.key} className="wb-switch">
              <input type="checkbox" checked={options[item.key]} disabled={locked || (item.key === "readOnly" && !options.tools)} onChange={event => void configure(model, { ...options, [item.key]: event.target.checked })} />
              <span className="wb-switch-track" aria-hidden="true"><span /></span>
              <span className="wb-switch-copy">{item.label}<small>{item.hint}</small></span>
            </label>
          ))}
        </div>
      ) : null}
      </ControlMenu></div>
      {configNote ? <p className="wb-note" role="status">{configNote}</p> : null}
      {error ? <p className="wb-error" role="alert">{error}</p> : null}
      {thread?.approval ? (
        <div className="wb-approvals">
          <strong>批准 {thread.approval.title}？</strong>
          <pre className="wb-approval-detail">{thread.approval.detail}</pre>
          <div className="wb-approval-actions">
            {[true, false].map(allow => <button key={String(allow)} className="wb-btn" disabled={approvalBusy} onClick={() => void approve(allow)}>{allow ? "允许一次" : "拒绝"}</button>)}
          </div>
        </div>
      ) : null}
      {thread?.error ? <p className={`wb-note ${thread.status === "error" ? "is-warn" : ""}`}>{thread.error}</p> : null}
      <div className="wb-scroll">
        {!thread ? (
          <EmptyState compact title="还没有规划线程" icon="chat">直接输入第一句，或从右上角规划配置选择模型与预设。</EmptyState>
        ) : !messages.length ? (
          <EmptyState compact title="干净的空白上下文" icon="chat">这个线程不 fork 任何 Code 会话，聊完再决定是否导入。</EmptyState>
        ) : (
          <ol className="wb-thread" ref={listRef}>
            {messages.map(message => <PlanMessage key={message.id} message={message} selected={selected.includes(message.id)} onSelect={() => pick(message.id)} />)}
            {thread.status === "running" ? <li className="wb-typing"><Spinner size={12} /> 正在生成</li> : null}
          </ol>
        )}
      </div>
      {selected.length ? (
        <div className="wb-handoff-selection">
          <p className="wb-hint">已选择 {selected.length} 条，按对话顺序交接</p>
          <HandoffForm text={handoffText} onText={setHandoffText} sessions={boot.sessions} busy={handoffBusy} error={handoffError} result={handoffResult} onSubmit={target => void submitHandoff(target)} onOpenSession={id => void host.openSession(id)} />
        </div>
      ) : null}
      <Composer value={draft} onChange={setDraft} onSend={() => void send()} busy={locked} placeholder={activeId ? "继续规划，Enter 发送" : "直接输入第一句，会自动新建线程"}
        action={thread?.status === "running" || thread?.status === "quota" ? <button type="button" className="wb-link" onClick={() => void stop()}>{thread.status === "quota" ? "解除等待" : "停止"}</button> : null}
        hint={thread?.status === "running" ? "正在接收流式回复" : undefined} />
    </div>
  );
}
