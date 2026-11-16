import { createContext, useContext, useEffect, useRef, useState, type Dispatch, type ReactNode, type SetStateAction } from "react";
import type { DemoHost, NativeSession, SessionView } from "../../native";
import { Icon } from "../Icon";
import { EmptyState, Spinner } from "../ui";
import { Composer, ExecMessage, errorText, fmtClock, statusTone } from "./pieces";
import { ControlMenu } from "./ControlMenu";

type Attachment = { path: string; name: string };
type PermissionMode = "" | "manual" | "auto" | "yolo";

export type SendMode = "normal" | "plan" | "tower";
type SessionInput = { draft?: string; mode?: SendMode; model?: string; permission?: PermissionMode; attachments?: Attachment[] };
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
  onSessionId,
  onSessions,
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
  onSessionId: (id: string) => void;
  onSessions: (next: NativeSession[]) => void;
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
  const mode = inputs[sid]?.mode ?? "normal";
  const model = inputs[sid]?.model ?? "";
  const permission = inputs[sid]?.permission ?? "";
  const attachments = inputs[sid]?.attachments ?? [];
  const updateInput = (value: Partial<SessionInput>) => setInputs(current => ({ ...current, [sid]: { ...current[sid], ...value } }));
  const setAttachments = (value: Attachment[]) => updateInput({ attachments: value });
  const setDraft = (value: string) => updateInput({ draft: value });
  const setMode = (value: SendMode) => updateInput({ mode: value });
  const setModel = (value: string) => updateInput({ model: value });
  const scrollRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    setView(null);
    setError(null);
    if (!sid) return;
    let alive = true;
    host.request<SessionView>("session.view", { id: sid }).then(next => {
      if (alive && next.session.id === sid) setView(next);
    }).catch(cause => { if (alive) setError(errorText(cause)); });
    const unsubscribe = host.subscribe<SessionView>(`session:${sid}`, next => {
      if (alive && next.session.id === sid) setView(next);
    });
    return () => { alive = false; unsubscribe(); };
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
      const next = [created, ...sessions];
      onSessions(next);
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
        mode,
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
        <div className="wb-session-title"><ControlMenu label="选择执行会话" icon="chat" trigger={<><Icon name="chat" size={15} /><span>{session?.title || "选择会话"}</span><Icon name="chevronDown" size={12} /></>}>
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
              <select
                className="wb-select wb-select-inline"
                aria-label="发送方式"
                value={mode}
                onChange={(event) => setMode(event.target.value as SendMode)}
              >
                <option value="normal">普通</option>
                <option value="plan">Plan</option>
                <option value="tower">Tower</option>
              </select>
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

export function RecoveryPanel({
  host,
  state,
  onState,
  watchSession,
}: {
  host: DemoHost;
  state: { enabled: boolean; intervalSeconds: number; entries: { id: string; kind: string; title: string; status: string; nextAt?: string }[] } | null;
  onState: (next: { enabled: boolean; intervalSeconds: number; entries: { id: string; kind: string; title: string; status: string; nextAt?: string }[] }) => void;
  watchSession: string;
}) {
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [checkedAt, setCheckedAt] = useState<string | null>(null);
  const [open, setOpen] = useState(false);

  useEffect(() => host.subscribe<NonNullable<typeof state>>("recovery", next => onState(next)), [host, onState]);

  async function toggle(enabled: boolean) {
    setBusy("toggle");
    setError(null);
    try {
      const next = await host.request<typeof state>("recovery.set", {
        enabled,
        intervalSeconds: state?.intervalSeconds ?? 60,
      });
      if (next) onState(next);
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusy(null);
    }
  }

  async function checkNow() {
    setBusy("check");
    setError(null);
    try {
      await host.request("recovery.check", {});
      const next = await host.request<typeof state>("recovery.get", {});
      if (next) onState(next);
      setCheckedAt(new Date().toISOString());
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusy(null);
    }
  }

  async function watch() {
    if (!watchSession) return;
    setBusy("watch");
    setError(null);
    try {
      await host.request("recovery.watch", { id: watchSession, kind: "session" });
      const next = await host.request<typeof state>("recovery.get", {});
      if (next) onState(next);
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusy(null);
    }
  }

  async function unwatch() {
    if (!watchSession) return;
    setBusy("watch");
    setError(null);
    try {
      const next = await host.request<typeof state>("recovery.unwatch", { id: watchSession, kind: "session" });
      if (next) onState(next);
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setBusy(null);
    }
  }

  const entries = state?.entries ?? [];
  const watching = entries.some(entry => entry.id === `session:${watchSession}`);
  const nextAt = entries.find((entry) => entry.nextAt)?.nextAt;

  return (
    <div className="wb-recovery">
      <button type="button" className="wb-recovery-head" aria-expanded={open} onClick={() => setOpen(!open)}>
        <Icon name="clock" size={14} />
        <span>额度恢复自动继续</span>
        <span className={`wb-state is-${state?.enabled ? "success" : "neutral"}`}>
          {state?.enabled ? "已开启" : "未开启"}
        </span>
        <Icon name={open ? "chevronUp" : "chevronDown"} size={14} />
      </button>
      {open ? (
        <div className="wb-recovery-body">
          <label className="wb-switch">
            <input
              type="checkbox"
              checked={Boolean(state?.enabled)}
              disabled={busy !== null}
              onChange={(event) => void toggle(event.target.checked)}
            />
            <span className="wb-switch-track" aria-hidden="true">
              <span />
            </span>
            <span className="wb-switch-copy">
              我明确开启自动继续
              <small>只针对账号额度，不突破你设定的目标预算。</small>
            </span>
          </label>
          <dl className="wb-facts">
            <div>
              <dt>等待项</dt>
              <dd>{entries.length ? `${entries.length} 个监听或等待项` : "当前没有监听或等待项"}</dd>
            </div>
            <div>
              <dt>检测间隔</dt>
              <dd className="num">{state?.intervalSeconds ?? 60} 秒</dd>
            </div>
            <div>
              <dt>下次检测</dt>
              <dd className="num">{nextAt ? fmtClock(nextAt) : "未排定"}</dd>
            </div>
            {checkedAt ? (
              <div>
                <dt>刚刚检查</dt>
                <dd className="num">{fmtClock(checkedAt)}</dd>
              </div>
            ) : null}
          </dl>
          <div className="wb-web-row">
            <button type="button" className="wb-btn" onClick={() => void checkNow()} disabled={busy !== null}>
              {busy === "check" ? <Spinner size={13} /> : <Icon name="refresh" size={14} />}
              立即检查
            </button>
            <button type="button" className="wb-btn is-quiet" onClick={() => void watch()} disabled={!watchSession || busy !== null}>
              {busy === "watch" ? <Spinner size={13} /> : null}
              监听当前会话
            </button>
            {watching ? <button type="button" className="wb-btn is-quiet" onClick={() => void unwatch()} disabled={busy !== null}>停止监听当前会话</button> : null}
          </div>
          {error ? (
            <p className="wb-error" role="alert">
              {error}
            </p>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
