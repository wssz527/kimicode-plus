import { createContext, useCallback, useContext, useEffect, useLayoutEffect, useMemo, useRef, useState, type Dispatch, type ReactNode, type SetStateAction } from "react";
import { createSessionWatchBinding, type SessionWatchOutcome } from "../../api";
import type { DemoHost, NativeSession, SessionView } from "../../native";
import { Icon } from "../Icon";
import { Spinner } from "../ui";
import { ConversationTurn, errorText, fmtClock, groupThread, statusTone } from "./pieces";
import { ControlMenu } from "./ControlMenu";
import { ApprovalCard, type InteractionReply } from "./ApprovalCard";
import { addComposerAttachments, attachmentPayload, browserAttachment, NativeComposer, newClientMessageId,
  type Attachment, type PermissionMode, type SendMode, type Thinking } from "./NativeComposer";

type SessionRecoveryHint = { enabled: boolean; nextAt?: string; note?: string };
type SessionInput = { draft?: string; model?: string; permission?: PermissionMode; mode?: SendMode; thinking?: Thinking; attachments?: Attachment[] };
const NO_ATTACHMENTS: Attachment[] = [];

export function isNearThreadEnd(scroll: Pick<HTMLElement, "scrollHeight" | "scrollTop" | "clientHeight">) {
  return scroll.scrollHeight - scroll.scrollTop - scroll.clientHeight < 80;
}

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
  const [sending, setSending] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [awaitingStart, setAwaitingStart] = useState("");
  const sendAttempt = useRef<{ sid: string; key: string; id: string } | null>(null);
  const actionInFlight = useRef(false);
  const replyInFlight = useRef(false);
  const lastViewEvent = useRef(0);
  const inputRef = useRef(inputs);
  inputRef.current = inputs;
  const [error, setError] = useState<string | null>(null);
  // A live subscription that is not established is not an error: the history is
  // already on screen, and the pane says so in its own banner with a retry.
  const [syncNote, setSyncNote] = useState<string | null>(null);
  const [syncing, setSyncing] = useState(false);
  const [recoverTicket, setRecoverTicket] = useState(0);
  const [replying, setReplying] = useState<string | null>(null);
  const sid = sessionId;
  const draft = inputs[sid]?.draft ?? "";
  const model = inputs[sid]?.model ?? "";
  const permission = inputs[sid]?.permission ?? "";
  const attachments = inputs[sid]?.attachments ?? NO_ATTACHMENTS;
  const mode = inputs[sid]?.mode ?? "inherit";
  const thinking = inputs[sid]?.thinking ?? "";
  const updateInput = (value: Partial<SessionInput>) => setInputs(current => ({ ...current, [sid]: { ...current[sid], ...value } }));
  const quoteReply = useCallback((text: string) => setInputs(current => {
    const draft = current[sid]?.draft ?? "";
    const quote = text.split("\n").map(line => `> ${line}`).join("\n");
    return { ...current, [sid]: { ...current[sid], draft: `${draft}${draft ? "\n\n" : ""}${quote}\n\n` } };
  }), [setInputs, sid]);
  const scrollRef = useRef<HTMLDivElement>(null);
  const threadRef = useRef<HTMLOListElement>(null);
  const stickToBottom = useRef(true);
  const [showJump, setShowJump] = useState(false);
  const turns = useMemo(() => groupThread(view?.messages ?? []), [view?.messages]);

  // A session change is the only thing that empties the pane. A retry of the
  // live link keeps whatever history is already on screen.
  useEffect(() => {
    setView(null);
    setError(null);
    setSyncNote(null);
  }, [sid]);

  useEffect(() => {
    if (!sid) return;
    let alive = true;
    let receivedEvent = false;
    const unsubscribe = host.subscribe<SessionView>(`session:${sid}`, next => {
      if (!alive || next.session.id !== sid) return;
      receivedEvent = true;
      lastViewEvent.current++;
      setAwaitingStart(current => current === sid ? "" : current);
      setView(next);
      setSyncNote(null);
    });
    const binding = createSessionWatchBinding<SessionView>({
      watch: () => host.request<SessionWatchOutcome<SessionView>>("session.watch", { id: sid }),
      read: () => host.request<SessionView>("session.view", { id: sid }),
      unwatch: () => host.request("session.unwatch", { id: sid }),
      onView: initial => {
        if (receivedEvent || initial?.session?.id !== sid) return;
        setView(initial);
      },
      onNote: note => {
        setSyncNote(note);
        // The history is on screen, so a live-link note is not a blocking error.
        if (note) setError(null);
      },
      onError: message => {
        setError(message);
        setSyncNote(null);
      },
      describeError: errorText,
      attached: () => alive,
    });
    void binding.start().finally(() => {
      if (alive) setSyncing(false);
    });
    return () => {
      alive = false;
      unsubscribe();
      // One release per acquired reference: a failed subscribe holds none.
      void binding.dispose();
    };
  }, [host, sid, recoverTicket]);

  useLayoutEffect(() => {
    stickToBottom.current = true;
    setShowJump(false);
  }, [sid]);
  useLayoutEffect(() => {
    const scroll = scrollRef.current;
    if (!scroll) return;
    const follow = () => {
      if (stickToBottom.current) scroll.scrollTop = scroll.scrollHeight;
      setShowJump(!stickToBottom.current && !isNearThreadEnd(scroll));
    };
    follow();
    if (typeof ResizeObserver !== "function") return;
    const observer = new ResizeObserver(follow);
    observer.observe(scroll);
    if (threadRef.current) observer.observe(threadRef.current);
    return () => observer.disconnect();
  }, [sid, turns]);
  useEffect(() => {
    if (awaitingStart !== sid || !sid) return;
    let active = true;
    const timer = window.setTimeout(async () => {
      try {
        const next = await host.request<SessionView>("session.view", { id: sid });
        if (active && next.session.id === sid) setView(next);
      } catch (cause) {
        if (active) setError(errorText(cause));
      } finally {
        if (active) setAwaitingStart("");
      }
    }, 1000);
    return () => { active = false; window.clearTimeout(timer); };
  }, [awaitingStart, host, sid]);

  function onThreadScroll() {
    const scroll = scrollRef.current;
    if (!scroll) return;
    stickToBottom.current = isNearThreadEnd(scroll);
    setShowJump(!stickToBottom.current);
  }

  function jumpToBottom() {
    stickToBottom.current = true;
    const scroll = scrollRef.current;
    if (scroll) scroll.scrollTop = scroll.scrollHeight;
    setShowJump(false);
  }

  function retryLiveSync() {
    if (!sid || syncing) return;
    setSyncing(true);
    setError(null);
    setRecoverTicket(ticket => ticket + 1);
  }

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

  function addAttachments(next: Attachment[]) {
    if (!sid || !next.length) return;
    const current = inputRef.current[sid]?.attachments ?? NO_ATTACHMENTS;
    updateInput({ attachments: addComposerAttachments(current, next) });
    setError(null);
  }

  function attachFiles(files: File[]) {
    try {
      addAttachments(files.map(file => browserAttachment(file, host.localFilePath)));
    } catch (cause) {
      setError(errorText(cause));
    }
  }

  async function pickFiles() {
    setPickingFiles(true);
    setError(null);
    try {
      const picked = await host.request<{ path: string; name: string }[]>("host.pickFiles");
      addAttachments(picked.map(item => ({ ...item, id: item.path })));
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setPickingFiles(false);
    }
  }

  async function send() {
    const text = draft.trim();
    if ((!text && !attachments.length) || !sid || running || actionInFlight.current) return;
    actionInFlight.current = true;
    setSending(true);
    setError(null);
    const key = JSON.stringify([text, model, mode, permission, thinking, attachments.map(item => item.id)]);
    if (sendAttempt.current?.sid !== sid || sendAttempt.current.key !== key) {
      sendAttempt.current = { sid, key, id: newClientMessageId() };
    }
    const startedAt = lastViewEvent.current;
    try {
      await host.request("session.send", {
        id: sid, text, mode, clientMessageId: sendAttempt.current.id,
        ...(model ? { model } : {}),
        ...(permission ? { permissionMode: permission } : {}),
        ...(thinking ? { thinking } : {}),
        ...(attachments.length ? { attachments: await Promise.all(attachments.map(attachmentPayload)) } : {}),
      });
      sendAttempt.current = null;
      setInputs(current => {
        const input = current[sid] ?? {};
        return { ...current, [sid]: { ...input, draft: input.draft === draft ? "" : input.draft,
          attachments: input.attachments?.filter(item => !attachments.includes(item)) } };
      });
      if (startedAt === lastViewEvent.current) setAwaitingStart(sid);
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setSending(false);
      actionInFlight.current = false;
    }
  }

  async function abort() {
    if (!sid || actionInFlight.current) return;
    actionInFlight.current = true;
    setStopping(true);
    setError(null);
    try {
      await host.request("session.abort", { id: sid });
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setStopping(false);
      actionInFlight.current = false;
    }
  }

  async function reply(interactionId: string, value: InteractionReply) {
    if (!sid || replyInFlight.current) return;
    replyInFlight.current = true;
    setReplying(interactionId);
    setError(null);
    try {
      await host.request("session.reply", { id: sid, interactionId, value });
      setView(current => current?.session.id === sid ? { ...current, approvals: current.approvals.filter(item => item.id !== interactionId) } : current);
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setReplying(null);
      replyInFlight.current = false;
    }
  }

  const session = view?.session.id === sid ? view.session : sessions.find(item => item.id === sid) ?? null;
  const waiting = Boolean(view?.approvals.length || session?.pending_interaction && session.pending_interaction !== "none");
  const running = Boolean(session?.busy || session?.main_turn_active || waiting || awaitingStart && awaitingStart === sid);
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

      {syncNote && view ? (
        <p className="wb-exec-recovery" role="status" title={syncNote}>
          <Icon name="clock" size={12} />
          <span>历史已加载，实时同步暂未连接</span>
          <button type="button" className="wb-link" disabled={syncing} onClick={() => retryLiveSync()}>
            {syncing ? <Spinner size={11} /> : null}
            {syncing ? "正在重连" : "重试同步"}
          </button>
        </p>
      ) : null}

      <div className="wb-exec-timeline">
        <div className="wb-scroll wb-exec-body" ref={scrollRef} onScroll={onThreadScroll}>
          {!sid || view && turns.length === 0 && !running ? <div className="wb-session-welcome">
            <Icon name="chat" size={28} />
            <h2>{sid ? "开始对话" : "选择或新建会话"}</h2>
            {sid ? <p>在下方输入任务，即可开始这次会话。</p> : null}
          </div> : !view && !running && !error ? <div className="wb-session-welcome" role="status"><Spinner size={16} /><p>正在读取消息</p></div> : <ol className="wb-exec-thread" ref={threadRef}>
            {turns.map(turn => <ConversationTurn key={turn.id} messages={turn.messages} onReply={quoteReply} />)}
            {running && !view?.messages.some(message => message.streaming) ? <li className="wb-typing"><Spinner size={12} /> {waiting ? "等待你的确认" : "执行中"}</li> : null}
          </ol>}
        </div>
        {showJump ? <button type="button" className="wb-jump-bottom" onClick={jumpToBottom}><Icon name="chevronDown" size={15} />回到底部</button> : null}
      </div>

      {approvals.length > 0 ? <div className="wb-approvals">{approvals.map(approval =>
        <ApprovalCard key={approval.id} approval={approval} active={replying === approval.id} busy={replying !== null} onReply={value => void reply(approval.id, value)} />
      )}</div> : null}
      {error ? (
        <p className="wb-error" role="alert">
          {error}
        </p>
      ) : null}

      <div className="wb-exec-composer">
        <NativeComposer draft={draft} model={model} permission={permission} mode={mode} thinking={thinking}
          models={models} attachments={attachments} disabled={!sid} sending={sending} running={running}
          stopping={stopping} picking={pickingFiles} useFileInput={Boolean(host.localFilePath)}
          onDraft={value => updateInput({ draft: value })} onModel={value => updateInput({ model: value })}
          onPermission={value => updateInput({ permission: value })} onMode={value => updateInput({ mode: value })}
          onThinking={value => updateInput({ thinking: value })} onSend={() => void send()} onStop={() => void abort()}
          onPick={() => void pickFiles()} onFiles={attachFiles} onRemove={id => updateInput({ attachments: attachments.filter(item => item.id !== id) })} />
      </div>
    </section>
  );
}
