import { useId, useState, type ReactNode } from "react";
import type { DemoMessage, NativeSession } from "../../native";
import { Icon } from "../Icon";
import { Badge, Spinner } from "../ui";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

export type HandoffMode = "normal" | "plan" | "tower";

export const HANDOFF_MODES: { id: HandoffMode; label: string; hint: string }[] = [
  { id: "plan", label: "Plan", hint: "先出方案，不动文件" },
  { id: "normal", label: "普通", hint: "直接执行" },
  { id: "tower", label: "Tower", hint: "交给多代理编排" },
];

export function errorText(error: unknown): string {
  if (error instanceof Error && error.message) return error.message;
  if (typeof error === "string" && error) return error;
  return "操作没有完成，请稍后重试。";
}

export function fmtClock(value?: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function statusTone(status: string): "neutral" | "success" | "warn" | "danger" | "info" {
  switch (status) {
    case "running":
    case "busy":
      return "info";
    case "idle":
    case "done":
    case "ready":
      return "success";
    case "quota":
    case "waiting":
      return "warn";
    case "error":
    case "failed":
      return "danger";
    default:
      return "neutral";
  }
}

const STATUS_LABEL: Record<string, string> = {
  idle: "空闲",
  running: "生成中",
  quota: "额度用尽",
  error: "出错",
  ready: "就绪",
  waiting: "等待恢复",
  suspended: "已挂起",
  busy: "执行中",
};

export function statusLabel(status: string): string {
  return STATUS_LABEL[status] ?? status;
}

export function RunBadge({ status }: { status: string }) {
  return (
    <Badge tone={statusTone(status)}>
      {status === "running" ? <Spinner size={9} /> : null}
      {statusLabel(status)}
    </Badge>
  );
}

export function ClockBadge({ children }: { children: ReactNode }) {
  return <span className="wb-clock">{children}</span>;
}

export function PlanMessage({
  message,
  selected,
  onSelect,
}: {
  message: DemoMessage;
  selected: boolean;
  onSelect: () => void;
}) {
  const mine = message.role === "user";
  const toolCall = message.toolCalls?.[0];
  return (
    <li className={`wb-plan-message ${mine ? "is-mine" : ""}`}>
      <div className={`wb-msg ${mine ? "is-mine" : ""} ${selected ? "is-selected" : ""}`}>
        {selected || message.tool || toolCall ? <span className="wb-msg-meta">
          {message.tool || toolCall ? <span className="wb-tool-tag">{message.tool || toolCall?.function.name}</span> : null}
          {selected ? <><span>已选择，可交接</span><Icon name="check" size={13} /></> : null}
        </span> : null}
        {message.reasoning ? <span className="wb-msg-reasoning">推理：{message.reasoning}</span> : null}
        <div className={`wb-msg-text ${message.role === "tool" ? "is-tool" : ""}`}><MessageBody text={message.text || (toolCall ? "已请求工具调用。" : "")} /></div>
      </div>
      <div className="wb-plan-actions"><MessageActions message={message} /><button type="button" className="wb-link" aria-label={selected ? "取消选中消息" : "选择消息交接"} aria-pressed={selected} onClick={onSelect}>{selected ? "取消选择" : "选择交接"}</button></div>
    </li>
  );
}

export function MessageBody({ text }: { text: string }) {
  return <div className="wb-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]} components={{ a: ({ children, ...props }) => <a {...props} target="_blank" rel="noopener noreferrer">{children}</a>, img: ({ alt }) => <span>{alt || "图片"}</span> }}>{text}</ReactMarkdown></div>;
}

function MessageActions({ message, onReply }: { message: DemoMessage; onReply?: (text: string) => void }) {
  const [status, setStatus] = useState("");
  async function copy() {
    try { await navigator.clipboard.writeText(message.text); setStatus("已复制"); }
    catch { setStatus("复制失败，请选择文字后复制"); }
  }
  return <div className="wb-message-actions">
    <button type="button" className="wb-icon-btn" aria-label="复制消息" title="复制消息" onClick={() => void copy()}><Icon name="copy" size={14} /></button>
    {onReply ? <button type="button" className="wb-icon-btn" aria-label="引用回复" title="引用回复" onClick={() => onReply(message.text)}><Icon name="handoff" size={14} /></button> : null}
    {message.at ? <time dateTime={message.at}>{fmtClock(message.at)}</time> : null}
    {status ? <span role="status">{status}</span> : null}
  </div>;
}

function MessageAttachments({ message }: { message: DemoMessage }) {
  return message.attachments?.length ? <ul className="wb-attachments" aria-label="消息附件">{message.attachments.map((item, index) => <li key={index}><Icon name="file" size={13} /><span>{item.name || item.kind}</span></li>)}</ul> : null;
}

export function ExecMessage({ message, onReply }: { message: DemoMessage; onReply?: (text: string) => void }) {
  const mine = message.role === "user";
  return <li className={`wb-exec-msg ${mine ? "is-mine" : ""}`}>
    <MessageAttachments message={message} />
    {message.tool ? <details className="wb-tool"><summary><Icon name="file" size={14} /><span>{message.tool}</span><span className="wb-hint">工具输出</span></summary><pre>{message.text}</pre></details> : <div className="wb-msg-text"><MessageBody text={message.text} /></div>}
    {!message.tool ? <MessageActions message={message} onReply={onReply} /> : null}
  </li>;
}

export function Composer({
  value,
  onChange,
  onSend,
  busy,
  disabled,
  placeholder,
  hint,
  action,
  leading,
  attachments,
  allowEmpty,
}: {
  value: string;
  onChange: (next: string) => void;
  onSend: () => void;
  busy?: boolean;
  disabled?: boolean;
  placeholder: string;
  hint?: ReactNode;
  action?: ReactNode;
  leading?: ReactNode;
  attachments?: ReactNode;
  allowEmpty?: boolean;
}) {
  const id = useId();
  return (
    <div className="wb-composer">
      {attachments}
      <label className="wb-sr" htmlFor={id}>
        输入消息
      </label>
      <textarea
        id={id}
        className="wb-textarea"
        rows={3}
        value={value}
        placeholder={placeholder}
        disabled={disabled}
        onChange={(event) => onChange(event.target.value)}
        onKeyDown={(event) => {
          if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing) return;
          event.preventDefault();
          if ((value.trim() || allowEmpty) && !busy && !disabled) onSend();
        }}
      />
      <div className="wb-composer-foot">
        <div className="wb-composer-controls">{leading}{hint}</div>
        <div className="wb-composer-actions">
          {action}
          <button
            type="button"
            className="wb-send"
            onClick={onSend}
            disabled={disabled || busy || (!value.trim() && !allowEmpty)}
            aria-busy={busy || undefined}
            aria-label="发送"
            title="发送"
          >
            {busy ? <Spinner size={13} /> : <Icon name="arrowUp" size={16} />}
            <span className="wb-sr">发送</span>
          </button>
        </div>
      </div>
    </div>
  );
}

export function HandoffForm({
  text,
  onText,
  sessions,
  busy,
  error,
  result,
  onSubmit,
  onOpenSession,
  submitLabel = "交接给 Code",
}: {
  text: string;
  onText: (next: string) => void;
  sessions: NativeSession[];
  busy: boolean;
  error: string | null;
  result: string | null;
  onSubmit: (target: { sessionId?: string; mode: HandoffMode }) => void;
  onOpenSession: (id: string) => void;
  submitLabel?: string;
}) {
  const [target, setTarget] = useState<string>("new");
  const [mode, setMode] = useState<HandoffMode>("plan");
  const selectId = useId();
  return (
    <div className="wb-handoff">
      <div className="wb-handoff-head">
        <span>交接预览</span>
        <span className="wb-hint">可编辑，发送前只在本机</span>
      </div>
      <textarea
        className="wb-textarea"
        rows={6}
        value={text}
        onChange={(event) => onText(event.target.value)}
        placeholder="这里会填入选中的规划内容；也可以直接粘贴一段讨论。"
      />
      <div className="wb-handoff-grid">
        <label className="wb-field" htmlFor={selectId}>
          <span className="wb-label">目标会话</span>
          <select
            id={selectId}
            className="wb-select"
            value={target}
            onChange={(event) => setTarget(event.target.value)}
          >
            <option value="new">新建 Code 会话</option>
            {sessions.map((session) => (
              <option key={session.id} value={session.id}>
                {session.title || session.id}
              </option>
            ))}
          </select>
        </label>
        <div className="wb-field">
          <span className="wb-label">执行方式</span>
          <div className="wb-modes" role="group" aria-label="执行方式">
            {HANDOFF_MODES.map((item) => (
              <button
                key={item.id}
                type="button"
                className={`wb-mode ${mode === item.id ? "is-active" : ""}`}
                aria-pressed={mode === item.id}
                title={item.hint}
                onClick={() => setMode(item.id)}
              >
                {item.label}
              </button>
            ))}
          </div>
        </div>
      </div>
      {error ? <p className="wb-error" role="alert">{error}</p> : null}
      <div className="wb-handoff-foot">
        <button
          type="button"
          className="wb-btn is-primary"
          disabled={busy || !text.trim()}
          onClick={() => onSubmit({ sessionId: target === "new" ? undefined : target, mode })}
        >
          {busy ? <Spinner size={13} /> : <Icon name="handoff" size={14} />}
          {submitLabel}
        </button>
        {result ? (
          <button type="button" className="wb-link" onClick={() => onOpenSession(result)}>
            打开会话
            <Icon name="arrowRight" size={13} />
          </button>
        ) : null}
      </div>
      {result ? (
        <p className="wb-hint" role="status">
          已交给 Code 会话 <code>{result.slice(0, 8)}</code>，后续在本机执行。
        </p>
      ) : null}
    </div>
  );
}
