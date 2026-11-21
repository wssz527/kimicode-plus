import { useId, useState, type ReactNode } from "react";
import { BRIDGE_RECONNECT_TEXT, isBridgeCredentialError } from "../../api";
import type { DemoMessage } from "../../native";
import { Icon } from "../Icon";
import { Badge, Spinner } from "../ui";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

export { BRIDGE_RECONNECT_TEXT, isBridgeCredentialError } from "../../api";

export function errorText(error: unknown): string {
  if (isBridgeCredentialError(error)) return BRIDGE_RECONNECT_TEXT;
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
