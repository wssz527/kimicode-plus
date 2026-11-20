import { createContext, useContext, useEffect, useRef, useState, type Dispatch, type ReactNode, type SetStateAction } from "react";
import type { DemoHost, NativeSession, SessionView } from "../../native";
import { Icon } from "../Icon";
import { EmptyState, Spinner } from "../ui";
import { Composer, ExecMessage, errorText, fmtClock, statusTone } from "./pieces";
import { ControlMenu } from "./ControlMenu";

type Attachment = { path: string; name: string };
type PermissionMode = "" | "manual" | "auto" | "yolo";
type SessionRecoveryHint = { enabled: boolean; nextAt?: string; note?: string };

type SessionInput = { draft?: string; model?: string; permission?: PermissionMode; attachments?: Attachment[] };
type SessionInputs = Record<string, SessionInput>;
const SessionInputContext = createContext<[SessionInputs, Dispatch<SetStateAction<SessionInputs>>] | null>(null);
export function SessionInputProvider({ children }: { children: ReactNode }) {
  const value = useState<SessionInputs>({});
  return <SessionInputContext.Provider value={value}>{children}</SessionInputContext.Provider>;
}

export function SessionPane({
  host,
  index,
  sessions,
  models,
  sessionId,
  recovery,
  onSessionId,
  onSessionCreated,
  onOpenSessions,
  onAdd,
  canAdd = true,
  onRestore,
  onClose,
}: {
  host: DemoHost;
  index: number;
  sessions: NativeSession[];
  models: { id: string; name: string }[];
  sessionId: string;
  recovery?: SessionRecoveryHint | null;
  onSessionId: (id: string) => void;
  onSessionCreated: (session: NativeSession) => void;
  onOpenSessions: () => void;
  onAdd?: () => void;
  canAdd?: boolean;
  onRestore?: () => void;
  onClose?: () => void;
}) {
  const [view, setView] = useState<SessionView | null>(null);
  const sharedInputs = useContext(SessionInputContext);
  const localInputs = useState<SessionInputs>({});
  const [inputs, setInputs] = sharedInputs ?? localInputs;
  const [pickingFiles, setPickingFiles] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [replying, setReplying] = useState<string | null>(null);
  const sid = sessionId;
  const draft = inputs[sid]?.draft ?? "";
  const model = inputs[sid]?.model ?? "";
  const permission = inputs[sid]?.permission ?? "";
  const attachments = inputs[sid]?.attachments ?? [];
  const updateInput = (value: Partial<SessionInput>) => setInputs(current => ({ ...current, [sid]: { ...current[sid], ...value } }));
  const setAttachments = (value: Attachment[]) => updateInput({ attachments: value });
  const setDraft = (value: string) => updateInput({ draft: value });
  const setModel = (value: string) => updateInput({ model: value });
  const scrollRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    setView(null);
    setError(null);
    if (!sid) return;
    let alive = true;
    let receivedEvent = false;
    const unsubscribe = host.subscribe<SessionView>(`session:${sid}`, next => {
      if (!alive || next.session.id !== sid) return;
      receivedEvent = true;
      setView(next);
    });
    const watching = host.request<{ view: SessionView }>("session.watch", { id: sid });
    watching.then(({ view: initial }) => {
      if (alive && !receivedEvent && initial.session.id === sid) setView(initial);
    }).catch(cause => { if (alive) setError(errorText(cause)); });
    return () => {
      alive = false;
      unsubscribe();
      void watching.then(() => host.request("session.unwatch", { id: sid })).catch(() => {});
    };
  }, [host, sid]);

  useEffect(() => {
    if (!view) return;
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight });
  }, [view?.messages, view?.session.busy]);

  async function createSession() {
    setBusy(true);
    setError(null);
    try {
      const created = await host.request<NativeSession>("session.create", {});
      onSessionCreated(created);
      onSessionId(created.id);
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusy(false);
    }
  }

  async function pickFiles() {
    setPickingFiles(true);
    setError(null);
    try {
      const next = await host.request<Attachment[]>("host.pickFiles");
      setInputs(current => ({ ...current, [sid]: { ...current[sid], attachments: [...(current[sid]?.attachments ?? []), ...next].filter((item, index, all) => all.findIndex(other => other.path === item.path) === index) } }));
    } catch (cause) { setError(errorText(cause)); }
    finally { setPickingFiles(false); }
  }

  async function send() {
    const text = draft.trim();
    if ((!text && !attachments.length) || !sid) return;
    setBusy(true);
    setError(null);
    setDraft("");
    setAttachments([]);
    try {
      await host.request("session.send", {
        id: sid,
        text,
        mode: "normal",
        ...(model ? { model } : {}),
        ...(permission ? { permissionMode: permission } : {}),
        ...(attachments.length ? { attachments: attachments.map(item => item.path) } : {}),
      });
    } catch (cause) {
      setError(errorText(cause));
      setDraft(text);
      setAttachments(attachments);
    } finally {
      setBusy(false);
    }
  }

  async function abort() {
    if (!sid) return;
    try {
      await host.request("session.abort", { id: sid });
    } catch (cause) {
      setError(errorText(cause));
    }
  }

  async function reply(interactionId: string, value: string) {
    if (!sid) return;
    setReplying(interactionId);
    try {
      const payload = ["approved", "rejected", "cancelled"].includes(value) ? value : { decision: "approved", selectedLabel: value };
      await host.request("session.reply", { id: sid, interactionId, value: payload });
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setReplying(null);
    }
  }

  const session = view?.session ?? sessions.find((item) => item.id === sid) ?? null;
  const waiting = !!session?.pending_interaction && session.pending_interaction !== "none";
  const running = Boolean(session?.busy || session?.main_turn_active || waiting);
  const approvals = view?.approvals ?? [];

  return (
    <section className="wb-exec" data-pane={index} data-session-id={sid} aria-label={`执行会话 ${index + 1}`}>
      <div className="wb-exec-head">
        <div className="wb-session-title"><ControlMenu label="选择执行会话" icon="chat" onOpenChange={open => { if (open) onOpenSessions(); }} trigger={<><Icon name="chat" size={15} /><span>{session?.title || "选择会话"}</span><Icon name="chevronDown" size={12} /></>}>
          <div className="wb-menu-options">{sessions.map(item => <button type="button" key={item.id} aria-pressed={item.id === sid} onClick={event => { onSessionId(item.id); event.currentTarget.closest<HTMLElement>("[popover]")?.hidePopover(); }}><span>{item.title || item.id.slice(0, 8)}</span>{item.id === sid ? <Icon name="check" size={14} /> : null}</button>)}</div>
        </ControlMenu></div>
        {running ? <span className={`wb-state is-${statusTone(waiting ? "waiting" : "running")}`}>{waiting ? "等待确认" : "执行中"}</span> : null}
        <button type="button" className="wb-icon-btn" title="新建官方会话" aria-label="新建官方会话" onClick={() => void createSession()} disabled={busy}>
          <Icon name="plus" size={15} />
        </button>
        {onAdd ? <button type="button" className="wb-icon-btn" title="添加同屏会话" aria-label="添加同屏会话" onClick={onAdd} disabled={!canAdd}><Icon name="overview" size={15} /></button> : null}
        {onRestore ? <button type="button" className="wb-icon-btn" title="关闭同屏并返回官方单会话" aria-label="关闭全部同屏并返回官方单会话" onClick={onRestore}><Icon name="close" size={15} /></button> : null}
        {onClose ? <button type="button" className="wb-icon-btn" title="关闭这个同屏会话（不会结束会话）" aria-label="关闭这个同屏会话" onClick={onClose}><Icon name="close" size={15} /></button> : null}
      </div>

      {view?.goal ? (
        <p className="wb-goal">
          目标 <span className="wb-hint">{String(view.goal.status ?? "进行中")}</span>
          <span className="wb-hint"> · 消耗受目标预算限制</span>
        </p>
      ) : null}

      {recovery ? (
        <p className="wb-exec-recovery" role="status" title={recovery.note || undefined}>
          <Icon name="clock" size={12} />
          <span>
            {recovery.enabled
              ? `等待额度恢复自动继续${recovery.nextAt ? ` · 下次检测 ${fmtClock(recovery.nextAt)}` : ""}`
              : "因限流暂停，未开启自动继续"}
          </span>
        </p>
      ) : null}

      <div className="wb-scroll wb-exec-body" ref={scrollRef}>
        {!sid ? (
          <EmptyState compact title="选择或新建会话" icon="play">
            执行会话由官方 Agent 运行，这里的输入会作为真实 prompt 发出去。
          </EmptyState>
        ) : view && view.messages.length === 0 ? (
          <EmptyState compact title="会话还没有消息" icon="play">
            在下方输入任务，或从规划页导入。
          </EmptyState>
        ) : (
          <ol className="wb-exec-thread">
            {(view?.messages ?? []).map((message) => (
              <ExecMessage key={message.id} message={message} onReply={text => setDraft(`${draft}${draft ? "\n\n" : ""}${text.split("\n").map(line => `> ${line}`).join("\n")}\n\n`)} />
            ))}
            {running ? (
              <li className="wb-typing">
                <Spinner size={12} /> {waiting ? "等待你的确认" : "执行中"}
              </li>
            ) : null}
          </ol>
        )}
      </div>

      {approvals.length > 0 ? (
        <div className="wb-approvals">
          {approvals.map((approval) => (
            <div className="wb-approval" key={approval.id}>
              <div className="wb-approval-head">
                <Icon name="shield" size={14} />
                <span>{approval.title}</span>
              </div>
              {approval.detail ? <pre className="wb-approval-detail">{approval.detail}</pre> : null}
              <div className="wb-approval-actions">
                {(approval.options ?? [{ label: "允许", value: "allow" }, { label: "拒绝", value: "deny" }]).map((option) => (
                  <button
                    key={option.value}
                    type="button"
                    className={`wb-btn ${option.value === "deny" ? "is-quiet" : ""}`}
                    disabled={replying !== null}
                    onClick={() => void reply(approval.id, option.value)}
                  >
                    {replying === approval.id ? <Spinner size={12} /> : null}
                    {option.label}
                  </button>
                ))}
              </div>
            </div>
          ))}
        </div>
      ) : null}
      {error ? (
        <p className="wb-error" role="alert">
          {error}
        </p>
      ) : null}

      <div className="wb-exec-composer">
        <Composer
          value={draft}
          onChange={setDraft}
          onSend={() => void send()}
          busy={busy}
          disabled={!sid || pickingFiles}
          allowEmpty={attachments.length > 0}
          leading={<button type="button" className="wb-attach" aria-label="添加附件" title="添加附件" disabled={!sid || busy || pickingFiles} onClick={() => void pickFiles()}>{pickingFiles ? <Spinner size={14} /> : <Icon name="plus" size={17} />}</button>}
          attachments={attachments.length ? <ul className="wb-attachments" aria-label="待发送附件">{attachments.map(item => <li key={item.path}><Icon name="file" size={13} /><span title={item.path}>{item.name}</span><button type="button" className="wb-icon-btn" aria-label={`移除附件 ${item.name}`} onClick={() => setAttachments(attachments.filter(other => other.path !== item.path))}><Icon name="close" size={12} /></button></li>)}</ul> : null}
          placeholder={sid ? "输入任务，发送给官方会话" : "先选择或新建一个会话"}
          action={
            running ? (
              <button type="button" className="wb-link" onClick={() => void abort()}>
                取消执行
              </button>
            ) : null
          }
          hint={
            <>
              <label className="wb-permission" title="发送时应用所选权限；跟随权限会保留会话当前设置"><Icon name="shield" size={13} /><select className="wb-select wb-select-inline" aria-label="发送权限" value={permission} onChange={event => updateInput({ permission: event.target.value as PermissionMode })}><option value="">跟随权限</option><option value="manual">始终询问</option><option value="auto">自动判断</option><option value="yolo">始终允许</option></select></label>
              <label className="wb-model"><Icon name="models" size={13} /><select
                className="wb-select wb-select-inline"
                aria-label="会话模型"
                value={model}
                onChange={(event) => setModel(event.target.value)}
              >
                <option value="">跟随会话</option>
                {models.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.name}
                  </option>
                ))}
              </select></label>
            </>
          }
        />
      </div>
    </section>
  );
}
