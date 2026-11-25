import { isValidElement, memo, useEffect, useRef, useState, type ReactNode } from "react";
import { BRIDGE_RECONNECT_TEXT, isBridgeCredentialError } from "../../api";
import type { NativeMessage, NativeToolCall } from "../../native";
import { Icon } from "../Icon";
import { Badge, Spinner } from "../ui";
import ReactMarkdown, { type Components } from "react-markdown";
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

const SAFE_LINK_PROTOCOLS = new Set(["http:", "https:", "mailto:"]);

/**
 * Markdown can carry `javascript:` and other schemes; a message from a model or
 * a tool must not turn that into a live link in the workbench. Only an explicit
 * http/https/mailto URL survives: a protocol-relative `//host` or a bare
 * relative path is refused too, because both resolve against whatever origin
 * the workbench happens to run on.
 */
export function safeHref(href?: string): string | null {
  if (!href) return null;
  const raw = href.trim();
  if (!raw || raw.startsWith("//")) return null;
  // The scheme is what authorizes the link, so it is read directly instead of
  // through URL: a relative path has no scheme and must stay text.
  const match = /^([a-z][a-z0-9+.-]*):/i.exec(raw);
  if (!match?.[1]) return null;
  const protocol = `${match[1].toLowerCase()}:`;
  return SAFE_LINK_PROTOCOLS.has(protocol) ? raw : null;
}

const markdownComponents: Components = {
  a: ({ children, href }) => {
    const safe = safeHref(href);
    return safe
      ? <a href={safe} target="_blank" rel="noopener noreferrer">{children}</a>
      : <span className="wb-link-blocked" title={href ? `已拦截不安全的链接协议：${href}` : undefined}>{children}</span>;
  },
  img: ({ alt }) => <span>{alt || "图片"}</span>,
  pre: ({ children }) => {
    if (!isValidElement<{ className?: string; children?: ReactNode }>(children) || typeof children.props.children !== "string") {
      return <pre>{children}</pre>;
    }
    const language = /(?:^|\s)language-([^\s]+)/.exec(children.props.className ?? "")?.[1];
    return <MarkdownCode language={language} text={children.props.children.replace(/\n$/, "")} />;
  },
};

export const MarkdownBlock = memo(function MarkdownBlock({ text }: { text: string }) {
  return <div className="wb-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownComponents}>{text}</ReactMarkdown></div>;
});

function useCopyStatus(text: string) {
  const [status, setStatus] = useState("");
  const timer = useRef<number | null>(null);
  useEffect(() => () => { if (timer.current !== null) window.clearTimeout(timer.current); }, []);
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setStatus("已复制");
    } catch {
      setStatus("复制失败，请选择文字后复制");
    }
    if (timer.current !== null) window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setStatus(""), 2400);
  }
  return { status, copy };
}

function MarkdownCode({ language, text }: { language?: string; text: string }) {
  const [wrap, setWrap] = useState(false);
  const { status, copy } = useCopyStatus(text);
  return <div className="wb-code-block">
    <div className="wb-code-head">
      <span>{language || "纯文本"}</span>
      <span role="status">{status}</span>
      <button type="button" aria-pressed={wrap} onClick={() => setWrap(value => !value)}>自动换行</button>
      <button type="button" aria-label="复制代码" title="复制代码" onClick={() => void copy()}><Icon name="copy" size={14} /></button>
    </div>
    <pre className={wrap ? "is-wrapped" : undefined}><code>{text}</code></pre>
  </div>;
}

export function ReasoningBlock({ text, streaming }: { text: string; streaming: boolean }) {
  return <details className="wb-reasoning">
    <summary><Icon name="chevronDown" size={14} /><span>{streaming ? "思考中" : "思考过程"}</span>
      {streaming ? <span className="wb-breath" aria-hidden="true" /> : null}
    </summary>
    <MarkdownBlock text={text} />
  </details>;
}

const toolStatuses: Record<string, { tone: string; label: string }> = {
  running: { tone: "info", label: "运行中" },
  waiting: { tone: "warn", label: "等待确认" },
  completed: { tone: "success", label: "已完成" },
  failed: { tone: "danger", label: "失败" },
  error: { tone: "danger", label: "失败" },
  cancelled: { tone: "muted", label: "已取消" },
  interrupted: { tone: "muted", label: "已中断" },
};

function toolValue(value: unknown) {
  return typeof value === "string" ? value : JSON.stringify(value, null, 2);
}

export function ToolCallCard({ tool }: { tool: NativeToolCall }) {
  const { tone, label } = toolStatuses[tool.status] ?? { tone: "muted", label: tool.status || "状态未知" };
  const duration = tool.duration === undefined ? "" : tool.duration < 1000 ? `${tool.duration} ms` : `${(tool.duration / 1000).toLocaleString("zh-CN", { maximumFractionDigits: 1 })} 秒`;
  return <details className="wb-tool">
    <summary><Icon name="chevronDown" size={14} /><span className="wb-tool-name">{tool.name || "工具调用"}</span>
      <span className="wb-tool-state" data-tone={tone}>{label}</span>{duration ? <span>{duration}</span> : null}
    </summary>
    {tool.input !== undefined ? <div className="wb-tool-detail"><span>输入</span><pre>{toolValue(tool.input)}</pre></div> : null}
    {tool.output !== undefined ? <div className="wb-tool-detail"><span>输出</span><pre>{toolValue(tool.output)}</pre></div> : null}
  </details>;
}

function MessageActions({ message, onReply }: { message: NativeMessage; onReply: (text: string) => void }) {
  const { status, copy } = useCopyStatus(message.text);
  return <div className="wb-message-actions">
    {message.text ? <>
      <button type="button" className="wb-icon-btn" aria-label="复制消息" title="复制消息" onClick={() => void copy()}><Icon name="copy" size={14} /></button>
      <button type="button" className="wb-icon-btn" aria-label="引用回复" title="引用回复" onClick={() => onReply(message.text)}><Icon name="handoff" size={14} /></button>
    </> : null}
    {message.at ? <time dateTime={message.at}>{fmtClock(message.at)}</time> : null}
    {status ? <span role="status">{status}</span> : null}
  </div>;
}

function ThreadMessage({ message, onReply }: { message: NativeMessage; onReply: (text: string) => void }) {
  const tools = message.toolCalls ?? (message.tool ? [{ id: message.id, name: message.tool, status: "", output: message.text }] : []);
  return <div className={`wb-exec-msg${message.role === "user" ? " is-mine" : ""}`}>
    {message.attachments?.length ? <ul className="wb-attachments" aria-label="消息附件">
      {message.attachments.map((item, index) => <li key={index}><Icon name="file" size={13} /><span>{item.name || item.kind}</span></li>)}
    </ul> : null}
    {message.reasoning ? <ReasoningBlock text={message.reasoning} streaming={Boolean(message.streaming)} /> : null}
    {tools.map(tool => <ToolCallCard key={tool.id} tool={tool} />)}
    {message.text && !message.tool ? <div className="wb-msg-text"><MarkdownBlock text={message.text} /></div> : null}
    {!message.tool && (message.text || message.attachments?.length) ? <MessageActions message={message} onReply={onReply} /> : null}
  </div>;
}

export function groupThread(messages: NativeMessage[]) {
  const turns: { id: string; messages: NativeMessage[] }[] = [];
  for (const message of messages) {
    const id = message.turnId ?? message.id;
    const last = turns.at(-1);
    if (last?.id === id) last.messages.push(message);
    else turns.push({ id, messages: [message] });
  }
  return turns;
}

function equalMessage(a: NativeMessage, b: NativeMessage) {
  return a === b || a.id === b.id && a.role === b.role && a.text === b.text && a.at === b.at
    && a.tool === b.tool && a.reasoning === b.reasoning && a.streaming === b.streaming
    && JSON.stringify(a.attachments) === JSON.stringify(b.attachments)
    && JSON.stringify(a.toolCalls) === JSON.stringify(b.toolCalls);
}

export const ConversationTurn = memo(function ConversationTurn({ messages, onReply }: {
  messages: NativeMessage[]; onReply: (text: string) => void;
}) {
  return <li className="wb-conversation-turn">{messages.map(message => <ThreadMessage key={message.id} message={message} onReply={onReply} />)}</li>;
}, (previous, next) => previous.onReply === next.onReply && previous.messages.length === next.messages.length
  && previous.messages.every((message, index) => equalMessage(message, next.messages[index]!)));
