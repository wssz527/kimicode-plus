import { capabilitySummary, type ModelOption } from "../lib/models";

/**
 * The one model picker. Options come from the official catalog, so every
 * selection is a provider-qualified key; a stored value that is no longer in the
 * catalog stays visible and is marked instead of being silently dropped.
 */
export function ModelSelect({
  id,
  options,
  value,
  onChange,
  ariaLabel = "模型",
  placeholder = "选择模型",
  disabled = false,
  className,
}: {
  id?: string;
  options: ModelOption[];
  value: string;
  onChange: (next: string) => void;
  ariaLabel?: string;
  placeholder?: string;
  disabled?: boolean;
  className?: string;
}) {
  const known = options.some(option => option.key === value);
  const missing = value !== "" && !known;
  return (
    <select
      id={id}
      className={className ? `select ${className}` : "select"}
      style={{
        width: "100%",
        minWidth: 0,
        maxWidth: "100%",
        overflow: "hidden",
        textOverflow: "ellipsis",
      }}
      aria-label={ariaLabel}
      value={value}
      disabled={disabled}
      onChange={event => onChange(event.target.value)}
    >
      <option value="">{placeholder}</option>
      {missing ? <option value={value}>{value}（当前供应商中找不到这个模型）</option> : null}
      {options.map(option => (
        <option key={option.key} value={option.key}>
          {option.displayName}
          {option.provider ? ` · ${option.provider}` : ""}
          {capabilitySummary(option) ? ` · ${capabilitySummary(option)}` : ""}
        </option>
      ))}
    </select>
  );
}
