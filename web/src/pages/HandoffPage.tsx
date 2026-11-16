import { useEffect, useMemo, useRef, useState } from "react";
import { api, type OutboxRecord, type SessionReference, type TaskPreviewResult } from "../api";
import type { HandoffAttachment, HandoffDirection, HandoffMode, MockBehavior, TaskPackage } from "../../../adapters/types";
import { shortHash } from "../../../adapters/canonical";
import { Icon } from "../components/Icon";
import {
  ActionButton,
  Badge,
  CopyButton,
  EmptyState,
  ErrorNotice,
  Field,
  InfoNote,
  PageHeader,
  Panel,
  SkeletonRows,
  StateBadge,
  Unknown,
} from "../components/ui";
import { PageError } from "../AppShell";
import { useAction, useResource, errorMessage, errorRecovery } from "../lib/useResource";
import {
  MAX_ATTACHMENTS,
  MAX_TOTAL_ATTACHMENT_BYTES,
  readLocalFiles,
  scanSensitive,
  type LocalAttachment,
  type RejectedFile,
  toHandoffAttachment,
} from "../lib/attachments";
import { validateTaskPackage } from "../lib/contract";
import {
  fmtBytes,
  fmtDateTime,
  directionLabel,
  messageRoleLabel,
  modeLabel,
  stateLabel,
  stateTone,
  truncate,
} from "../lib/format";
import { syntheticSiteFixtures } from "../../../adapters/synthetic-chat";
import { diagnosticMode, useHostContext } from "../host";
import { features } from "../features";
import { navigate } from "../routes";

type PageStep = "compose" | "preview";

interface SelectedMessage {
  id: string;
  role: string;
  text: string;
  sequence: number;
}

const MOCK_BEHAVIORS: Array<{ value: MockBehavior; label: string; detail: string }> = [
  { value: "OK", label: "正常回执", detail: "Mock 宿主返回 DELIVERED 与回执" },
  { value: "TIMEOUT", label: "超时未确认", detail: "落 UNKNOWN，必须人工核对" },
  { value: "EMPTY", label: "空回执", detail: "落 UNKNOWN，不伪造成功" },
  { value: "OCCUPIED", label: "会话被占用", detail: "请求未被接受：REJECTED + 409，不产生回执" },
  { value: "PANIC", label: "模拟崩溃", detail: "落 UNKNOWN，重启后也不自动重发" },
];

let taskCounter = 0;

export function HandoffPage() {
  const context = useHostContext();
  const diagnostic = diagnosticMode();
  const localSessions = useResource("handoff/local-sessions", features.sessions);
  const sessions = useResource("handoff/sessions", () => api.sessions());
  const webTargets=useResource("handoff/web-targets",()=>api.webTargets());
  const [webTab,setWebTab]=useState("");
  const tasks = useResource("handoff/tasks", () => api.tasks.list(0, 50));

  const [fixtureId, setFixtureId] = useState("kimi");
  const [threadId, setThreadId] = useState("");
  const [selectedIds, setSelectedIds] = useState<string[]>([]);
  const [direction, setDirection] = useState<HandoffDirection>("CHAT_TO_CODE");
  const [mode, setMode] = useState<HandoffMode>("PLAN");
  const [sessionId, setSessionId] = useState("");
  const [projectId, setProjectId] = useState("");
  const [goal, setGoal] = useState("");
  const [diff, setDiff] = useState("");
  const [attachments, setAttachments] = useState<LocalAttachment[]>([]);
  const [rejected, setRejected] = useState<RejectedFile[]>([]);
  const [linkedTaskId, setLinkedTaskId] = useState("");
  const [mockBehavior, setMockBehavior] = useState<MockBehavior>("OK");
  const [step, setStep] = useState<PageStep>("compose");
  const [preview, setPreview] = useState<TaskPreviewResult | null>(null);
  // 原始规范任务只留在内存：提交时由核心重新校验附件字节与脱敏哈希。
  // 界面始终展示 preview.redacted，绝不把 bytes 或未脱敏文本渲染出来。
  const [submitPackage, setSubmitPackage] = useState<TaskPackage | null>(null);
  const [parentHash, setParentHash] = useState<string | null>(null);
  const [composeError, setComposeError] = useState<string | null>(null);
  const [receiptRecord, setReceiptRecord] = useState<OutboxRecord | null>(null);
  const [copiedId, setCopiedId] = useState<string | null>(null);

  const previewAction = useAction<TaskPreviewResult>();
  const submitAction = useAction<OutboxRecord>();
  const reconcile = useAction<OutboxRecord>();
  const fileInput = useRef<HTMLInputElement | null>(null);

  const fixtures = useMemo(() => context && !diagnostic ? [{ id: context.source.site, name: context.title || "选中内容", origin: new URL(context.source.url).origin, threadId: context.source.threadId, title: context.title || "当前内容", visibleCount: context.messages.length, replyText: "", messages: context.messages.map((m, i) => ({ ...m, sequence: i + 1 })) }] : syntheticSiteFixtures(), [context, diagnostic]);
  const fixture = fixtures.find((item) => item.id === fixtureId) ?? fixtures[0];

  const sessionOptions = useMemo<SessionReference[]>(() => diagnostic ? sessions.data?.items ?? [] : [...(context?.sessionId ? [{ id: context.sessionId, title: context.title, projectId: context.projectId, synthetic: false }] : []), ...(localSessions.data?.items ?? []).filter(s => s.id !== context?.sessionId).map(s => ({ id: s.id, title: s.title, projectId: s.projectId, synthetic: false }))], [sessions.data, localSessions.data, context, diagnostic]);

  useEffect(() => {
    if (!sessionId && sessionOptions[0]) setSessionId(sessionOptions[0].id);
  }, [sessionId, sessionOptions]);

  useEffect(() => {
    const current = sessionOptions.find((session) => session.id === sessionId);
    if (current?.projectId) setProjectId(String(current.projectId));
    // 只跟随会话变化，允许用户随后手动修改项目 ID
  }, [sessionId]);

  useEffect(() => {
    setSelectedIds(context?.messages.map(m => m.id) ?? []);
    if (context) { setSessionId(context.sessionId); setProjectId(context.projectId); setDiff(context.diff ?? ""); }
    setThreadId(fixture?.threadId ?? "");
    setPreview(null);
    setSubmitPackage(null);
    setStep("compose");
  }, [fixture, context]);

  const selected = useMemo<SelectedMessage[]>(() => {
    if (!fixture) return [];
    return fixture.messages
      .filter((message) => selectedIds.includes(message.id))
      .map((message) => ({ id: message.id, role: message.role, text: message.text, sequence: message.sequence }));
  }, [fixture, selectedIds]);

  // 意图键由预览摘要派生：同一份确认内容重试复用同键（幂等），
  // 内容变化会得到新的 confirmationHash，因此不会撞上旧意图。
  const taskIntent = useMemo(() => {
    if (!submitPackage || !preview) return "";
    return `${submitPackage.taskId}:${preview.confirmationHash.slice(0, 32)}`;
  }, [submitPackage, preview]);

  // linkedTaskId 保存的是 Outbox 记录 ID（不是链根 taskId），这样复制历史后仍能解析到记录。
  const linked = tasks.data?.items.find((task) => task.id === linkedTaskId) ?? null;

  // 续接候选：只有 DELIVERED 且带 checkpointHash 的记录才能作为父。
  const continuationCandidates = useMemo(() => {
    const items = (tasks.data?.items ?? []).filter(
      (record) => record.state === "DELIVERED" && Boolean(record.checkpointHash),
    );
    const latestByChain = new Map<string, number>();
    for (const record of items) {
      const chain = record.task.linkedTaskId || record.task.taskId;
      const best = latestByChain.get(chain);
      if (best === undefined || record.task.checkpoint > best) latestByChain.set(chain, record.task.checkpoint);
    }
    return items.map((record) => {
      const chain = record.task.linkedTaskId || record.task.taskId;
      const latest = latestByChain.get(chain) === record.task.checkpoint;
      return { record, chain, latest };
    });
  }, [tasks.data]);

  const latestDelivered = useMemo(() => {
    if (continuationCandidates.length === 0) return null;
    return [...continuationCandidates]
      .filter((item) => item.latest)
      .sort((a, b) => b.record.task.checkpoint - a.record.task.checkpoint)[0] ?? null;
  }, [continuationCandidates]);

  useEffect(() => {
    if (direction !== "CHAT_TO_CODE_CONTINUE" || linkedTaskId || !latestDelivered) return;
    // 默认选择最新已交付父链，避免一进入续接就指向已过期的父。
    setLinkedTaskId(latestDelivered.record.id);
    setParentHash(latestDelivered.record.checkpointHash ?? null);
    setSessionId(latestDelivered.record.task.sessionId);
    setProjectId(latestDelivered.record.task.projectId);
  }, [direction, linkedTaskId, latestDelivered]);

  const derived = useMemo(() => {
    const fromMessages = Math.max(1, ...selected.map((message) => message.sequence));
    if (direction === "CHAT_TO_CODE_CONTINUE" && linked) {
      const parent = linked.task.checkpoint;
      return { checkpoint: parent + 1, parentCheckpoint: parent };
    }
    return { checkpoint: fromMessages, parentCheckpoint: 0 };
  }, [selected, direction, linked]);

  async function copyPrevious(record: OutboxRecord) {
    const task = record.task;
    setGoal(task.goal);
    if (task.diff) setDiff(task.diff);
    setDirection("CHAT_TO_CODE_CONTINUE");
    setSessionId(task.sessionId);
    setProjectId(task.projectId);
    // 选择状态保存记录 ID；提交时再从该记录推导链根 taskId。
    setLinkedTaskId(record.id);
    setParentHash(record.checkpointHash || null);
    setSelectedIds(task.source.messageIds.filter((id) => fixture?.messages.some((message) => message.id === id)));
    setStep("compose");
    setPreview(null);
    setComposeError(null);
    setCopiedId(record.id);
    window.setTimeout(() => setCopiedId(null), 2400);
    if (!record.checkpointHash) {
      setComposeError("该记录没有 checkpointHash，无法作为续接父节点；请选择已送达的记录。");
    }
  }

  async function buildPackage(): Promise<TaskPackage | null> {
    if (!fixture) {
      setComposeError("没有可用的合成来源夹具。");
      return null;
    }
    if (selected.length === 0) {
      setComposeError("至少选择一条来源消息。");
      return null;
    }
    if (!goal.trim()) {
      setComposeError("请填写本次交接的目标。");
      return null;
    }
    if (!sessionId.trim() || !projectId.trim()) {
      setComposeError("请选择会话并确认项目 ID。");
      return null;
    }
    if (direction === "CHAT_TO_CODE_CONTINUE" && (!linked || !parentHash || linked.state !== "DELIVERED")) {
      setComposeError("续接只能使用已送达（DELIVERED）且带 checkpointHash 的历史记录；请重新选择。");
      return null;
    }
    taskCounter += 1;
    const taskId = `task-local-${Date.now()}-${taskCounter}`;
    let handoffAttachments: HandoffAttachment[];
    try {handoffAttachments=await Promise.all(attachments.map(async a=>({...(await api.attachments.upload(toHandoffAttachment(a))),name:a.name})));}
    catch(error){setComposeError(errorMessage(error));return null}
    const webTarget=webTargets.data?.items.find(t=>t.tabId===webTab);
    if(diagnostic && direction==="CODE_TO_CHAT" && !webTarget){setComposeError("请选择已授权的合成网页目标 tab/thread");return null}

    const task: TaskPackage = {
      version: 1,
      ...(direction==="CODE_TO_CHAT" ? {webTarget} : {}),
      taskId,
      direction,
      sessionId: sessionId.trim(),
      projectId: projectId.trim(),
      source: {
        site: fixture.id,
        url: context && !diagnostic ? context.source.url : `${fixture.origin}/chat/${threadId || fixture.threadId}`,
        threadId: threadId || fixture.threadId,
        messageIds: selected.map((message) => message.id),
      },
      goal: goal.trim(),
      messages: selected.map((message) => ({ id: message.id, text: message.text, role: message.role })),
      attachments: handoffAttachments,
      checkpoint: derived.checkpoint,
      parentCheckpoint: derived.parentCheckpoint,
      mode,
      ...(diff.trim() ? { diff: diff.trim() } : {}),
      ...(direction === "CHAT_TO_CODE_CONTINUE" && linked && parentHash
        ? { linkedTaskId: linked.task.linkedTaskId || linked.task.taskId, parentHash }
        : {}),
    };
    const issues = validateTaskPackage(task);
    if (issues.length > 0) {
      setComposeError(issues.map((issue) => `${issue.path || "任务包"}：${issue.message}`).join("；"));
      return null;
    }
    setComposeError(null);
    return task;
  }

  async function runPreview() {
    const task = await buildPackage();
    if (!task) return;
    setSubmitPackage(task);
    setPreview(null);
    const result = await previewAction.run(() => api.tasks.preview(task));
    if (result) {
      setPreview(result);
      setStep("preview");
    }
  }

  async function submit() {
    if (!diagnostic) { setComposeError("原生任务传输未连接。预览已生成，尚未发送到 Code 或网站。"); return; }
    if (!submitPackage || !preview) return;
    const record = await submitAction.run(() =>
      api.tasks.submit(
        taskIntent,
        submitPackage,
        {
          confirmationHash: preview.confirmationHash,
          mockBehavior,
          acknowledgeUnverifiedAttachments: false,
        },
      ),
    );
    if (record) {
      setReceiptRecord(record);
      setStep("compose");
      setPreview(null);
      setSubmitPackage(null);
      tasks.reload();
    }
  }

  const sensitiveHits = useMemo(() => {
    const body = [goal, diff, ...selected.map((message) => message.text)].join("\n");
    return scanSensitive(body);
  }, [goal, diff, selected]);

  const attachmentBytes = attachments.reduce((sum, item) => sum + item.size, 0);

  if (!diagnostic && !context) return <div className="page"><PageHeader title="任务交接" description="从聊天选中消息，或由宿主选中内容动作进入。原生传输尚未连接。" /><Panel title="选择内容来源"><ActionButton onClick={() => navigate("/chat")}>前往聊天选择消息</ActionButton><p className="field-hint">也可先在会话与执行策略中保存目标会话引用。</p></Panel></div>;
  return (
    <div className="page">
      <PageHeader
        title="任务交接"
        description="选择来源消息，编辑目标与附件，先预览脱敏结果和来源，再确认提交到持久 Outbox。回执和未知状态都会留在同一条记录上。"
        meta={
          <>
            <span>方向：{directionLabel(direction)}</span>
            <span>checkpoint {derived.checkpoint}</span>
            <span>{attachments.length} 个附件 · {fmtBytes(attachmentBytes)}</span>
          </>
        }
        actions={
          <>
            <ActionButton
              variant="primary"
              icon="play"
              onClick={runPreview}
              busy={previewAction.busy}
              disabled={step === "compose" && selected.length === 0}
            >
              {step === "preview" ? "重新预览" : "预览交接"}
            </ActionButton>
            <ActionButton
              icon="refresh"
              onClick={() => {
                sessions.reload();
                tasks.reload();
              }}
            >
              刷新
            </ActionButton>
          </>
        }
      />

      {receiptRecord ? (
        <ReceiptBanner record={receiptRecord} onClose={() => setReceiptRecord(null)} onReconcile={() => {
          void reconcile.run(() => api.tasks.reconcile(receiptRecord.id)).then(() => tasks.reload());
        }} />
      ) : null}

      <div className="handoff-layout">
        <div className="handoff-main">
          <Panel
            title="1 · 选择来源与消息"
            aside={
              <span className="num">
                {selected.length} / {fixture?.messages.length ?? 0} 条
              </span>
            }
          >
            <div className="handoff-source-row">
              <Field label="来源" htmlFor="handoff-source" hint={diagnostic ? "诊断合成数据" : "保留选中消息的原始来源"}>
                <select
                  id="handoff-source"
                  className="select"
                  value={fixtureId}
                  onChange={(event) => setFixtureId(event.target.value)}
                >
                  {fixtures.map((item) => (
                    <option key={item.id} value={item.id}>
                      {item.name} · {item.origin}
                    </option>
                  ))}
                </select>
              </Field>
              <Field label="线程 ID" htmlFor="handoff-thread" hint="写入任务包来源引用，不会访问该地址。">
                <input
                  id="handoff-thread"
                  className="input num"
                  value={threadId}
                  onChange={(event) => setThreadId(event.target.value)}
                  placeholder={fixture?.threadId}
                />
              </Field>
            </div>

            {fixture ? (
              <div className="message-picker">
                <div className="message-picker-head">
                  <span className="num">
                    {fixture.messages.length} 条来源消息 · 可见 {fixture.visibleCount} 条
                  </span>
                  <div className="message-picker-tools">
                    <button
                      type="button"
                      className="link-btn link-btn-dark"
                      onClick={() =>
                        setSelectedIds(
                          selectedIds.length === fixture.messages.length
                            ? []
                            : fixture.messages.map((message) => message.id),
                        )
                      }
                    >
                      {selectedIds.length === fixture.messages.length ? "取消全选" : "全选来源消息"}
                    </button>
                  </div>
                </div>
                <ul className="message-options">
                  {fixture.messages.map((message) => {
                    const checked = selectedIds.includes(message.id);
                    const hidden = message.sequence > fixture.visibleCount;
                    return (
                      <li key={message.id} className={checked ? "is-selected" : ""}>
                        <label className="message-option">
                          <input
                            type="checkbox"
                            checked={checked}
                            onChange={(event) =>
                              setSelectedIds((current) =>
                                event.target.checked
                                  ? [...current, message.id]
                                  : current.filter((id) => id !== message.id),
                              )
                            }
                          />
                          <span className="message-option-body">
                            <span className="message-option-meta">
                              <Badge tone={message.role === "user" ? "accent" : "neutral"}>
                                {messageRoleLabel(message.role)}
                              </Badge>
                              <span className="num">#{message.sequence}</span>
                              {hidden ? <Badge tone="warn">虚拟列表未加载</Badge> : null}
                            </span>
                            <span className="message-option-text">{message.text}</span>
                          </span>
                        </label>
                      </li>
                    );
                  })}
                </ul>
                <p className="message-picker-note">
                  <Icon name="info" size={14} />
                  {diagnostic ? "此区域使用诊断合成消息。" : "仅包含本次选择的来源消息，未提取网站完整历史。"}
                </p>
              </div>
            ) : null}
          </Panel>

          <Panel title="2 · 目标与方向">
            <div className="direction-picker" role="group" aria-label="交接方向">
              {(
                [
                  { value: "CHAT_TO_CODE", label: "聊天 → Code", detail: "把选中的讨论提交到 Code 会话" },
                  { value: "CODE_TO_CHAT", label: "Code → 聊天", detail: "把进度或 diff 带入聊天讨论" },
                  { value: "CHAT_TO_CODE_CONTINUE", label: "同会话续接", detail: "绑定原任务、父检查点与哈希" },
                ] as Array<{ value: HandoffDirection; label: string; detail: string }>
              ).map((option) => (
                <button
                  key={option.value}
                  type="button"
                  className={`direction-card ${direction === option.value ? "is-active" : ""}`}
                  aria-pressed={direction === option.value}
                  onClick={() => {
                    setDirection(option.value);
                    setPreview(null);
                    if (option.value !== "CHAT_TO_CODE_CONTINUE") setParentHash(null);
                  }}
                >
                  <span className="direction-name">{option.label}</span>
                  <span className="direction-detail">{option.detail}</span>
                </button>
              ))}
            </div>

            <Field label="目标" htmlFor="handoff-goal" required hint="这是交给 Code 的目标描述。来源消息会作为不可信引用单独保存。">
              <textarea
                id="handoff-goal"
                className="textarea"
                rows={3}
                maxLength={4000}
                value={goal}
                onChange={(event) => setGoal(event.target.value)}
                placeholder="例如：根据选中的讨论，把模型目录的 revision 冲突处理补成可验证的界面流程。"
              />
            </Field>

            <Field label="diff（可选）" htmlFor="handoff-diff" hint="粘贴统一 diff 或变更摘要。内容会在预览中显示并检查敏感片段。">
              <textarea
                id="handoff-diff"
                className="textarea textarea-code"
                rows={5}
                value={diff}
                onChange={(event) => setDiff(event.target.value)}
                placeholder="--- a/web/src/pages/ModelsPage.tsx\n+++ b/web/src/pages/ModelsPage.tsx"
              />
            </Field>

            <div className="form-grid">
              <Field
                label="会话"
                htmlFor="handoff-session"
                required
                hint={
                  sessions.error
                    ? "核心会话接口不可用；需要先恢复 Mock 适配器才能提交。"
                    : diagnostic ? "诊断合成会话" : "本地会话引用，不代表已连接官方会话。"
                }
              >
                <select
                  id="handoff-session"
                  className="select"
                  value={sessionId}
                  onChange={(event) => setSessionId(event.target.value)}
                  disabled={direction === "CHAT_TO_CODE_CONTINUE" && Boolean(linked)}
                >
                  {sessionOptions.length === 0 ? <option value="">没有可用会话</option> : null}
                  {sessionOptions.map((session) => (
                    <option key={session.id} value={session.id}>
                      {session.title ?? session.id} · {session.id}
                    </option>
                  ))}
                </select>
              </Field>
              <Field label="项目 ID" htmlFor="handoff-project" required>
                <input
                  id="handoff-project"
                  className="input num"
                  value={projectId}
                  onChange={(event) => setProjectId(event.target.value)}
                />
              </Field>
              <Field label="执行模式" htmlFor="handoff-mode">
                <select id="handoff-mode" className="select" value={mode} onChange={(event) => setMode(event.target.value as HandoffMode)}>
                  <option value="PLAN">计划 · 只读方案</option>
                  <option value="EXECUTE">执行 · 允许改动</option>
                  <option value="TOWER">Tower · 需官方开关配合</option>
                </select>
              </Field>
              <Field label="检查点" htmlFor="handoff-checkpoint" hint="由所选消息的最大序号推导。">
                <input id="handoff-checkpoint" className="input num" value={derived.checkpoint} readOnly />
              </Field>
            </div>

            {direction === "CHAT_TO_CODE_CONTINUE" ? (
              <div className="linked-panel">
                <Field
                  label="原任务"
                  htmlFor="handoff-linked"
                  hint="只列出 DELIVERED 且带 checkpointHash 的记录。核心只接受每条链最新已交付父；旧父会被拒绝为 CHECKPOINT_BRANCH。"
                >
                  <select
                    id="handoff-linked"
                    className="select"
                    value={linkedTaskId}
                    onChange={(event) => {
                      const id = event.target.value;
                      const record = tasks.data?.items.find((task) => task.id === id);
                      setLinkedTaskId(id);
                      setParentHash(record?.checkpointHash ?? null);
                      if (record) {
                        // 手动选择历史项时同步原会话与项目，避免交叉到错误会话。
                        setSessionId(record.task.sessionId);
                        setProjectId(record.task.projectId);
                      }
                    }}
                  >
                    <option value="">选择已送达的历史任务</option>
                    {continuationCandidates.map(({ record, latest }) => (
                      <option key={record.id} value={record.id}>
                        #{record.task.checkpoint} ·{" "}
                        {truncate(record.task.goal || record.intent, 40)}
                        {latest ? " · 该链最新已交付父" : " · 旧父（会被拒绝）"}
                      </option>
                    ))}
                  </select>
                </Field>
                <dl className="linked-facts">
                  <div>
                    <dt>parentCheckpoint</dt>
                    <dd className="num">{derived.parentCheckpoint}</dd>
                  </div>
                  <div>
                    <dt>parentHash</dt>
                    <dd className="num">{parentHash ? shortHash(parentHash, 20) : <Unknown />}</dd>
                  </div>
                  <div>
                    <dt>linkedTaskId</dt>
                    <dd className="num">{linked ? linked.task.linkedTaskId || linked.task.taskId : linkedTaskId || "UNKNOWN"}</dd>
                  </div>
                </dl>
                {continuationCandidates.length === 0 && !tasks.loading ? (
                  <InfoNote tone="warn">
                    <p>
                      还没有可续接的父记录：必须先有一条 DELIVERED 且已推进 checkpointHash 的任务。
                      旧父不是最新交付检查点时，核心会返回 CHECKPOINT_BRANCH。
                    </p>
                  </InfoNote>
                ) : null}
                {tasks.error ? (
                  <InfoNote tone="warn">
                    <p>读取历史任务失败：{errorMessage(tasks.error)}。续接需要核心 Outbox 可用。</p>
                  </InfoNote>
                ) : null}
              </div>
            ) : null}
          </Panel>

          <Panel
            title="3 · 附件"
            aside={
              <span className="num">
                ≤ {MAX_ATTACHMENTS} 个 · 单个 ≤ 1 MiB · 合计 ≤ {fmtBytes(MAX_TOTAL_ATTACHMENT_BYTES)}
              </span>
            }
          >
            {diagnostic && direction==="CODE_TO_CHAT" ? <Field label="网页交付目标（Mock）" htmlFor="web-target"><select id="web-target" className="select" value={webTab} onChange={e=>setWebTab(e.target.value)}><option value="">请选择 tab/thread</option>{webTargets.data?.items.map(t=><option key={t.tabId} value={t.tabId}>{t.site} · {t.origin} · {t.tabId} · {t.threadId}</option>)}</select><p className="field-hint">独立 Web Mock transport。尚未连接真实网页。</p></Field> : null}
            <div className="attachment-block">
              <input
                ref={fileInput}
                id="handoff-files"
                type="file"
                multiple
                className="sr-only"
                onChange={async (event) => {
                  const files = Array.from(event.target.files ?? []);
                  event.target.value = "";
                  if (files.length === 0) return;
                  const result = await readLocalFiles(files, attachments);
                  setAttachments((current) => [...current, ...result.accepted]);
                  setRejected(result.rejected);
                }}
              />
              <ActionButton icon="upload" onClick={() => fileInput.current?.click()}>
                选择本地文件
              </ActionButton>
              <p className="attachment-note">
                只读取主动选择的文件并计算 SHA-256。预览前逐个上传到本地私有存储，任务只携带 blob 引用。
                确认交接后，Mock 接收端会获得完整原字节；文本扫描不能保证附件安全。
              </p>
              {rejected.length > 0 ? (
                <ul className="rejected-list">
                  {rejected.map((item) => (
                    <li key={item.name}>
                      <Icon name="alert" size={14} />
                      {item.name}：{item.reason}
                    </li>
                  ))}
                </ul>
              ) : null}
              {attachments.length > 0 ? (
                <ul className="attachment-list">
                  {attachments.map((item) => (
                    <li key={item.key}>
                      <Icon name="file" size={16} />
                      <div className="attachment-main">
                        <span className="attachment-name">{item.name}</span>
                        <span className="num attachment-digest">
                          {fmtBytes(item.size)} · {item.sha256.slice(0, 16)}…
                        </span>
                      </div>
                      <Badge tone="success">已校验</Badge>
                      <button
                        type="button"
                        className="icon-btn"
                        aria-label={`移除附件 ${item.name}`}
                        onClick={() => setAttachments((current) => current.filter((entry) => entry.key !== item.key))}
                      >
                        <Icon name="trash" size={16} />
                      </button>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="attachment-empty">没有附件。没有附件时任务包 attachments 为空数组。</p>
              )}
            </div>
          </Panel>

          {step === "preview" && preview ? (
            <PreviewPanel
              task={preview.redacted}
              preview={preview}
              sensitiveHits={sensitiveHits}
              mockBehavior={mockBehavior}
              setMockBehavior={setMockBehavior}
              submit={submit}
              submitBusy={submitAction.busy}
              submitError={submitAction.error}
              onClearSubmitError={submitAction.clearError}
              onBack={() => setStep("compose")}
            />
          ) : null}

          {composeError ? <ErrorNotice message={composeError} onDismiss={() => setComposeError(null)} /> : null}
          {previewAction.error ? (
            <ErrorNotice
              message={errorMessage(previewAction.error)}
              recovery={errorRecovery(previewAction.error)}
              onDismiss={previewAction.clearError}
            />
          ) : null}
        </div>

        <aside className="handoff-rail">
          <Panel title="Outbox 历史" aside={<span className="num">{tasks.data?.total ?? 0}</span>}>
            {tasks.loading ? (
              <SkeletonRows rows={4} cols={3} />
            ) : tasks.error ? (
              <PageError error={tasks.error} />
            ) : (tasks.data?.items.length ?? 0) === 0 ? (
              <EmptyState compact icon="handoff" title="Outbox 为空">
                提交并确认后，记录会持久保存。未知回执不会自动重试。
              </EmptyState>
            ) : (
              <ul className="outbox-list">
                {(tasks.data?.items ?? []).map((record) => (
                  <li key={record.id} className="outbox-item">
                    <div className="outbox-head">
                      <StateBadge state={record.state} tone={stateTone(record.state)} label={stateLabel(record.state)} />
                      <span className="num outbox-id">{record.id.slice(0, 16)}</span>
                    </div>
                    <p className="outbox-goal">{truncate(record.task.goal || record.intent, 84)}</p>
                    <div className="outbox-meta num">
                      <span>{directionLabel(record.task.direction)}</span>
                      <span>{fmtDateTime(record.createdAt ?? record.receipt?.deliveredAt)}</span>
                    </div>
                    <div className="outbox-actions">
                      <button type="button" className="btn btn-quiet btn-sm" onClick={() => void copyPrevious(record)}>
                        <Icon name="copy" size={14} />
                        复制上一条信息
                      </button>
                      <CopyButton
                        value={JSON.stringify(
                          {
                            id: record.id,
                            intent: record.intent,
                            state: record.state,
                            checkpoint: record.task.checkpoint,
                            checkpointHash: record.checkpointHash,
                            linkedTaskId: record.task.linkedTaskId,
                            receipt: record.receipt ?? null,
                          },
                          null,
                          2,
                        )}
                        label="复制摘要"
                        small
                      />
                      <ActionButton
                        variant="quiet"
                        icon="reconcile"
                        onClick={() =>
                          void reconcile
                            .run(() => api.tasks.reconcile(record.id))
                            .then((updated) => {
                              if (updated && receiptRecord?.id === updated.id) setReceiptRecord(updated);
                              tasks.reload();
                            })
                        }
                        busy={reconcile.busy}
                      >
                        核对
                      </ActionButton>
                    </div>
                    {copiedId === record.id ? <p className="outbox-flash">已把该记录作为续接基础。</p> : null}
                    {record.receipt ? (
                      <dl className="outbox-receipt">
                        <div>
                          <dt>回执</dt>
                          <dd className="num">{String(record.receipt.receiptId ?? "UNKNOWN")}</dd>
                        </div>
                        <div>
                          <dt>检查点</dt>
                          <dd className="num">{String(record.receipt.checkpoint ?? "UNKNOWN")}</dd>
                        </div>
                      </dl>
                    ) : null}
                  </li>
                ))}
              </ul>
            )}
            {reconcile.error ? (
              <ErrorNotice message={errorMessage(reconcile.error)} recovery={errorRecovery(reconcile.error)} onDismiss={reconcile.clearError} />
            ) : null}
          </Panel>

          <Panel title="操作纪律">
            <ul className="limits-list">
              <li>预览与提交是两个动作；预览不写 Outbox。</li>
              <li>同一意图键对应不同内容会被核心拒绝，重试复用同一键。</li>
              <li>UNKNOWN 只能核对，不会因为刷新或重启自动重发。</li>
              <li>安全模式下提交会被拒绝；历史仍可读取。</li>
              <li>附件只来自你主动选择的本地文件，不扫描路径或 URL。</li>
            </ul>
          </Panel>

        </aside>
      </div>
    </div>
  );
}

function ReceiptBanner({ record, onClose, onReconcile }: { record: OutboxRecord; onClose: () => void; onReconcile: () => void }) {
  return (
    <div className="receipt-banner" role="status">
      <Icon name={record.state === "DELIVERED" ? "check" : "alert"} size={18} />
      <div>
        <strong>
          {record.state === "DELIVERED" ? "任务已写入 Outbox 并送达 Mock 宿主" : "任务已写入 Outbox，但回执状态为 UNKNOWN"}
        </strong>
        <p className="num">
          {record.id} · {stateLabel(record.state)} · checkpoint {record.task.checkpoint}
        </p>
      </div>
      <div className="receipt-actions">
        <button type="button" className="btn btn-quiet btn-sm" onClick={onReconcile}>
          <Icon name="reconcile" size={14} />
          核对回执
        </button>
        <button type="button" className="icon-btn" onClick={onClose} aria-label="关闭回执提示">
          <Icon name="close" size={16} />
        </button>
      </div>
    </div>
  );
}

function PreviewPanel({
  task,
  preview,
  sensitiveHits,
  mockBehavior,
  setMockBehavior,
  submit,
  submitBusy,
  submitError,
  onClearSubmitError,
  onBack,
}: {
  task: TaskPackage;
  preview: TaskPreviewResult;
  sensitiveHits: ReturnType<typeof scanSensitive>;
  mockBehavior: MockBehavior;
  setMockBehavior: (value: MockBehavior) => void;
  submit: () => void;
  submitBusy: boolean;
  submitError: unknown;
  onClearSubmitError: () => void;
  onBack: () => void;
}) {
  return (
    <Panel title="4 · 预览与确认" className="preview-panel">
      <div className="preview-head">
        <div>
          <StateBadge state="PENDING" tone="warn" label="尚未提交" />
          <span className="num preview-task-id">{task.taskId}</span>
          <span className="num preview-hash" title="提交时必须与核心核对">
            confirmation {preview.confirmationHash.slice(0, 16)}
          </span>
        </div>
        <CopyButton value={JSON.stringify(task, null, 2)} label="复制脱敏任务包" />
      </div>

      {preview.warning ? (
        <InfoNote tone="warn">
          <p>{preview.warning}</p>
        </InfoNote>
      ) : null}

      <dl className="preview-facts">
        <div>
          <dt>方向</dt>
          <dd>{directionLabel(task.direction)}</dd>
        </div>
        <div>
          <dt>模式</dt>
          <dd>{modeLabel(task.mode)}</dd>
        </div>
        <div>
          <dt>checkpoint</dt>
          <dd className="num">
            {task.checkpoint} / parent {task.parentCheckpoint}
          </dd>
        </div>
        <div>
          <dt>来源</dt>
          <dd className="num">{task.source.site}</dd>
        </div>
        <div>
          <dt>消息</dt>
          <dd className="num">{task.messages.length}</dd>
        </div>
        <div>
          <dt>附件</dt>
          <dd className="num">{task.attachments.length}</dd>
        </div>
      </dl>

      <div className="preview-goal">
        <h3>目标</h3>
        <p>{task.goal}</p>
      </div>

      <div className="preview-grid">
        <section aria-labelledby="preview-source-title">
          <h3 id="preview-source-title">来源（不可信网页内容）</h3>
          <p className="num preview-url">{task.source.url}</p>
          <ol className="preview-messages">
            {task.messages.map((message) => (
              <li key={message.id}>
                <span className="preview-message-meta num">
                  #{message.id} · {messageRoleLabel(message.role)}
                </span>
                <p>{message.text}</p>
              </li>
            ))}
          </ol>
        </section>
        <section aria-labelledby="preview-redact-title">
          <h3 id="preview-redact-title">核心脱敏结果</h3>
          <pre className="code-block preview-redacted">{JSON.stringify(preview.redacted, null, 2)}</pre>
          {sensitiveHits.length > 0 ? (
            <ul className="hit-list">
              {sensitiveHits.map((hit) => (
                <li key={hit.id}>
                  <Icon name="alert" size={14} />
                  <div>
                    <strong>{hit.label}</strong>
                    <span className="num">{hit.samples.join(" · ")}</span>
                  </div>
                </li>
              ))}
            </ul>
          ) : (
            <p className="preview-clean">
              <Icon name="check" size={15} />
              本地规则未发现常见密钥、Bearer、私钥、邮箱或路径。规则不能保证全量识别，请人工确认来源内容。
            </p>
          )}
          {(preview.findings ?? []).length > 0 ? (
            <div className="preview-findings">
              <h4>核心 findings</h4>
              <ul className="finding-list">
                {(preview.findings ?? []).map((finding, index) => (
                  <li key={index}>{finding}</li>
                ))}
              </ul>
            </div>
          ) : null}
        </section>
      </div>

      {task.diff ? (
        <div className="preview-diff">
          <h3>Diff</h3>
          <pre className="code-block">{task.diff}</pre>
        </div>
      ) : null}

      <div className="preview-submit">
        {diagnosticMode() ? <Field label="Mock 故障行为" htmlFor="mock-behavior" hint="用于验证 UNKNOWN 流程；正常提交请选择正常回执。">
          <select
            id="mock-behavior"
            className="select select-narrow"
            value={mockBehavior}
            onChange={(event) => setMockBehavior(event.target.value as MockBehavior)}
          >
            {MOCK_BEHAVIORS.map((behavior) => (
              <option key={behavior.value} value={behavior.value}>
                {behavior.label} · {behavior.detail}
              </option>
            ))}
          </select>
        </Field> : <p role="status">原生 Code / 网页传输未连接。当前仅生成交接预览，未发送。</p>}
        {diagnosticMode() && mockBehavior !== "OK" ? (
          <InfoNote tone={mockBehavior === "OCCUPIED" ? "warn" : "warn"}>
            <p>
              {mockBehavior === "OCCUPIED"
                ? "会话占用表示宿主根本没有接受任务：核心返回 409 SESSION_OCCUPIED 并把记录标为 REJECTED，没有回执，也没有检查点可核对。确认没有被接受后，可用新的意图重试。"
                : `选择“${MOCK_BEHAVIORS.find((item) => item.value === mockBehavior)?.label}”后，记录会以 UNKNOWN 落库并保留核对入口，不会自动重试。`}
            </p>
          </InfoNote>
        ) : null}
        {submitError ? (
          <ErrorNotice
            message={errorMessage(submitError)}
            recovery={errorRecovery(submitError)}
            onDismiss={onClearSubmitError}
          />
        ) : null}
        <div className="form-actions">
          <ActionButton variant="primary" icon="check" onClick={submit} busy={submitBusy} disabled={!diagnosticMode()}>
            {diagnosticMode() ? "提交到诊断 Mock Outbox" : "原生传输未连接"}
          </ActionButton>
          <ActionButton variant="quiet" onClick={onBack}>
            返回修改
          </ActionButton>
          <span className="preview-consent">提交即表示确认上述来源与脱敏预览。</span>
        </div>
      </div>
    </Panel>
  );
}
