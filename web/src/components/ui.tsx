import type { ReactNode } from "react";
import { Icon, type IconName } from "./Icon";

export function PageHeader({
  title,
  description,
  actions,
  meta,
}: {
  title: string;
  description: ReactNode;
  actions?: ReactNode;
  meta?: ReactNode;
}) {
  return (
    <header className="page-head">
      <div className="page-head-copy">
        <h1>{title}</h1>
        <p>{description}</p>
        {meta ? <div className="page-meta">{meta}</div> : null}
      </div>
      {actions ? <div className="page-actions">{actions}</div> : null}
    </header>
  );
}

export function Panel({
  title,
  aside,
  children,
  className = "",
  id,
  as: Tag = "section",
}: {
  title?: ReactNode;
  aside?: ReactNode;
  children: ReactNode;
  className?: string;
  id?: string;
  as?: "section" | "div" | "aside";
}) {
  return (
    <Tag className={`panel ${className}`.trim()} id={id}>
      {title || aside ? (
        <div className="panel-head">
          {title ? <h2 className="panel-title">{title}</h2> : <span />}
          {aside ? <div className="panel-aside">{aside}</div> : null}
        </div>
      ) : null}
      <div className="panel-body">{children}</div>
    </Tag>
  );
}

export function Badge({
  children,
  tone = "neutral",
  title,
}: {
  children: ReactNode;
  tone?: "neutral" | "success" | "warn" | "danger" | "info" | "accent";
  title?: string;
}) {
  return (
    <span className={`badge badge-${tone}`} title={title}>
      {children}
    </span>
  );
}

export function StateBadge({
  state,
  tone,
  label,
}: {
  state: string;
  tone: "neutral" | "success" | "warn" | "danger" | "info";
  label: string;
}) {
  const dot = tone === "warn" ? "diamond" : "dot";
  return (
    <span className={`state state-${tone}`} data-state={state}>
      <span className={`state-mark state-mark-${dot}`} aria-hidden="true" />
      {label}
    </span>
  );
}

export function Unknown({ label = "UNKNOWN", hint }: { label?: string; hint?: string }) {
  return (
    <span className="unknown" title={hint ?? "核心未提供该值或明确标记为未知"}>
      {label}
    </span>
  );
}

export function CopyButton({
  value,
  label = "复制",
  disabled,
  small,
}: {
  value: string;
  label?: string;
  disabled?: boolean;
  small?: boolean;
}) {
  return (
    <button
      type="button"
      className={`btn btn-quiet ${small ? "btn-sm" : ""}`}
      disabled={disabled || !value}
      onClick={() => {
        void navigator.clipboard?.writeText(value);
      }}
      title="复制到剪贴板"
    >
      <Icon name="copy" size={15} />
      {label}
    </button>
  );
}

export function ActionButton({
  children,
  onClick,
  icon,
  variant = "secondary",
  disabled,
  busy,
  type = "button",
  title,
  full,
}: {
  children?: ReactNode;
  onClick?: () => void;
  icon?: IconName;
  variant?: "primary" | "secondary" | "quiet" | "danger";
  disabled?: boolean;
  busy?: boolean;
  type?: "button" | "submit";
  title?: string;
  full?: boolean;
}) {
  return (
    <button
      type={type}
      className={`btn btn-${variant} ${full ? "btn-full" : ""}`}
      onClick={onClick}
      disabled={disabled || busy}
      title={title}
      aria-busy={busy || undefined}
    >
      {busy ? <Spinner size={15} /> : icon ? <Icon name={icon} size={16} /> : null}
      {children}
    </button>
  );
}

export function Spinner({ size = 16 }: { size?: number }) {
  return <span className="spinner" style={{ width: size, height: size }} aria-hidden="true" />;
}

export function EmptyState({
  title,
  children,
  action,
  icon = "info",
  compact,
}: {
  title: string;
  children?: ReactNode;
  action?: ReactNode;
  icon?: IconName;
  compact?: boolean;
}) {
  return (
    <div className={`empty ${compact ? "empty-compact" : ""}`}>
      <span className="empty-icon">
        <Icon name={icon} size={20} />
      </span>
      <div>
        <p className="empty-title">{title}</p>
        {children ? <div className="empty-body">{children}</div> : null}
      </div>
      {action ? <div className="empty-action">{action}</div> : null}
    </div>
  );
}

export function ErrorNotice({
  title = "操作未完成",
  message,
  recovery,
  onDismiss,
}: {
  title?: string;
  message: string;
  recovery?: string | null;
  onDismiss?: () => void;
}) {
  return (
    <div className="notice notice-error" role="alert">
      <Icon name="alert" size={18} />
      <div className="notice-copy">
        <strong>{title}</strong>
        <p>{message}</p>
        {recovery ? <p className="notice-recovery">{recovery}</p> : null}
      </div>
      {onDismiss ? (
        <button type="button" className="icon-btn" onClick={onDismiss} aria-label="关闭提示">
          <Icon name="close" size={16} />
        </button>
      ) : null}
    </div>
  );
}

export function InfoNote({ children, tone = "info" }: { children: ReactNode; tone?: "info" | "warn" }) {
  return (
    <div className={`notice notice-${tone}`}>
      <Icon name={tone === "warn" ? "alert" : "info"} size={17} />
      <div className="notice-copy">{children}</div>
    </div>
  );
}

export function SkeletonRows({ rows = 3, cols = 4 }: { rows?: number; cols?: number }) {
  return (
    <div className="skeleton" aria-hidden="true">
      {Array.from({ length: rows }, (_, row) => (
        <div className="skeleton-row" key={row}>
          {Array.from({ length: cols }, (_, col) => (
            <span key={col} className={col === 0 ? "skeleton-cell wide" : "skeleton-cell"} />
          ))}
        </div>
      ))}
    </div>
  );
}

export function Field({
  label,
  hint,
  children,
  htmlFor,
  required,
}: {
  label: string;
  hint?: ReactNode;
  children: ReactNode;
  htmlFor?: string;
  required?: boolean;
}) {
  return (
    <div className="field">
      <label className="field-label" htmlFor={htmlFor}>
        {label}
        {required ? <span className="req" aria-hidden="true"> *</span> : null}
      </label>
      {children}
      {hint ? <p className="field-hint">{hint}</p> : null}
    </div>
  );
}

export function Toggle({
  checked,
  onChange,
  label,
  disabled,
  hint,
}: {
  checked: boolean;
  onChange: (next: boolean) => void;
  label: string;
  disabled?: boolean;
  hint?: string;
}) {
  return (
    <label className={`toggle ${disabled ? "is-disabled" : ""}`}>
      <input
        type="checkbox"
        role="switch"
        aria-label={label}
        checked={checked}
        disabled={disabled}
        onChange={(event) => onChange(event.target.checked)}
      />
      <span className="toggle-track" aria-hidden="true">
        <span className="toggle-knob" />
      </span>
      <span className="toggle-copy">
        <span className="toggle-label">{label}</span>
        {hint ? <span className="toggle-hint">{hint}</span> : null}
      </span>
    </label>
  );
}

export function DataTable({
  caption,
  columns,
  children,
  empty,
  className,
}: {
  caption: ReactNode;
  columns: ReactNode;
  children: ReactNode;
  empty?: ReactNode;
  className?: string;
}) {
  return (
    <div className={`table-scroll ${className ?? ""}`.trim()}>
      <table className="data-table">
        <caption>{caption}</caption>
        <thead>
          <tr>{columns}</tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
      {empty}
    </div>
  );
}
