import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { Icon, type IconName } from "../Icon";
import { Spinner } from "../ui";

export const ATTACHMENT_LIMIT = 16;
export const INLINE_IMAGE_LIMIT = 1024 * 1024;
const inlineImageTypes = new Set(["image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp"]);

export type Attachment = { id: string; name: string; path?: string; file?: File };
export type PermissionMode = "" | "manual" | "auto" | "yolo";
export type SendMode = "inherit" | "normal" | "plan" | "tower";
export type Thinking = "" | "on" | "off";

type Choice = { value: string; label: string };
const permissions: Choice[] = [
  { value: "", label: "跟随权限" }, { value: "manual", label: "始终询问" },
  { value: "auto", label: "自动判断" }, { value: "yolo", label: "始终允许" },
];
const modes: Choice[] = [
  { value: "inherit", label: "跟随模式" }, { value: "normal", label: "普通" },
  { value: "plan", label: "规划" }, { value: "tower", label: "控制塔" },
];
const thinkingOptions: Choice[] = [
  { value: "", label: "跟随思考" }, { value: "on", label: "开启思考" }, { value: "off", label: "关闭思考" },
];

export function addComposerAttachments(current: Attachment[], incoming: Attachment[]): Attachment[] {
  const next = new Map(current.map(item => [item.id, item]));
  for (const item of incoming) {
    if (next.has(item.id)) continue;
    if (!item.path && (!item.file || !inlineImageTypes.has(item.file.type))) {
      throw new Error(`无法读取「${item.name}」的本机路径，请使用附件按钮添加文件。`);
    }
    next.set(item.id, item);
  }
  const attachments = [...next.values()];
  if (attachments.length > ATTACHMENT_LIMIT) throw new Error("一次最多添加 16 个附件。");
  if (attachments.reduce((sum, item) => sum + (item.path ? 0 : item.file?.size ?? 0), 0) > INLINE_IMAGE_LIMIT) {
    throw new Error("剪贴板图片合计不能超过 1 MiB，请保存为文件后添加。");
  }
  return attachments;
}

export function newClientMessageId() {
  return crypto.randomUUID?.() ?? Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, "0")).join("");
}

export function browserAttachment(file: File, localFilePath?: (file: File) => string): Attachment {
  const path = localFilePath?.(file) || undefined;
  const name = file.name || `剪贴板图片.${file.type.split("/")[1] || "png"}`;
  return { id: path ?? newClientMessageId(), name, path, file };
}

export async function attachmentPayload(item: Attachment) {
  if (item.path) return item.path;
  const file = item.file!;
  const bytes = new Uint8Array(await file.arrayBuffer());
  let binary = "";
  for (let offset = 0; offset < bytes.length; offset += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000));
  }
  return { name: item.name, mediaType: file.type, data: btoa(binary) };
}

function ComposerChoice({ label, value, options, icon, onChange, disabled, tone }: {
  label: string; value: string; options: Choice[]; icon: IconName;
  onChange: (value: string) => void; disabled: boolean; tone?: PermissionMode;
}) {
  const selected = options.find(item => item.value === value);
  return <label className="wb-composer-choice" data-permission={tone} title={selected?.label ?? label}>
    <Icon name={icon} size={15} />
    <span className="wb-composer-choice-label">{selected?.label ?? value}</span>
    <Icon name="chevronDown" size={11} />
    <select aria-label={label} value={value} disabled={disabled} onChange={event => onChange(event.target.value)}>
      {!selected ? <option value={value}>{value}</option> : null}
      {options.map(item => <option key={item.value} value={item.value}>{item.label}</option>)}
    </select>
  </label>;
}

function AttachmentThumbnail({ file, name }: { file: File; name: string }) {
  const [url, setURL] = useState("");
  useEffect(() => {
    const next = URL.createObjectURL(file);
    setURL(next);
    return () => URL.revokeObjectURL(next);
  }, [file]);
  return url ? <img src={url} alt={name} /> : null;
}

export function NativeComposer({ draft, model, permission, mode, thinking, attachments, models,
  disabled, sending, running, stopping, picking, useFileInput, onDraft, onModel, onPermission,
  onMode, onThinking, onSend, onStop, onPick, onFiles, onRemove,
}: {
  draft: string; model: string; permission: PermissionMode; mode: SendMode; thinking: Thinking;
  attachments: Attachment[]; models: { id: string; name: string }[];
  disabled: boolean; sending: boolean; running: boolean; stopping: boolean; picking: boolean; useFileInput: boolean;
  onDraft: (value: string) => void; onModel: (value: string) => void; onPermission: (value: PermissionMode) => void;
  onMode: (value: SendMode) => void; onThinking: (value: Thinking) => void;
  onSend: () => void; onStop: () => void; onPick: () => void; onFiles: (files: File[]) => void; onRemove: (id: string) => void;
}) {
  const inputId = useId();
  const text = useRef<HTMLTextAreaElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const dragDepth = useRef(0);
  const [dragging, setDragging] = useState(false);
  const canSend = !disabled && !sending && !running && !picking && (draft.trim() !== "" || attachments.length > 0);
  const atLimit = attachments.length >= ATTACHMENT_LIMIT;
  const canAttach = !disabled && !picking && !atLimit;

  useLayoutEffect(() => {
    const box = text.current;
    if (!box || CSS.supports("field-sizing", "content")) return;
    const fit = () => {
      box.style.height = "auto";
      box.style.height = `${Math.min(200, box.scrollHeight)}px`;
    };
    fit();
    if (typeof ResizeObserver !== "function" || !box.parentElement) return;
    let width = box.clientWidth;
    const observer = new ResizeObserver(() => {
      if (width === box.clientWidth) return;
      width = box.clientWidth;
      fit();
    });
    observer.observe(box.parentElement);
    return () => observer.disconnect();
  }, [draft]);

  const label = running ? "停止生成" : "发送";
  const fileDrag = (event: React.DragEvent) => event.dataTransfer.types.includes("Files");
  return <div className={`wb-native-composer${dragging ? " is-dragging" : ""}`} onDragEnter={event => {
    if (!fileDrag(event)) return;
    event.preventDefault();
    dragDepth.current++;
    if (canAttach) setDragging(true);
  }} onDragOver={event => {
    if (!fileDrag(event)) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = canAttach ? "copy" : "none";
  }} onDragLeave={() => {
    dragDepth.current = Math.max(0, dragDepth.current - 1);
    if (dragDepth.current === 0) setDragging(false);
  }} onDrop={event => {
    if (!fileDrag(event)) return;
    event.preventDefault();
    dragDepth.current = 0;
    setDragging(false);
    if (!disabled && !picking) onFiles(Array.from(event.dataTransfer.files));
  }}>
    {attachments.length ? <ul className="wb-native-attachments" aria-label="待发送附件">
      {attachments.map(item => {
        const image = item.file && inlineImageTypes.has(item.file.type);
        return <li key={item.id} className={image ? "is-image" : undefined} title={item.name}>
          {image ? <AttachmentThumbnail file={item.file!} name={item.name} /> : <><Icon name="file" size={15} /><span>{item.name}</span></>}
          <button type="button" aria-label={`移除附件 ${item.name}`} onClick={() => onRemove(item.id)}><Icon name="close" size={12} /></button>
        </li>;
      })}
    </ul> : null}
    <label className="wb-sr" htmlFor={inputId}>输入消息</label>
    <textarea ref={text} id={inputId} rows={1} className="wb-native-input" value={draft} disabled={disabled}
      placeholder={disabled ? "先选择或新建会话" : "输入消息"}
      onChange={event => onDraft(event.target.value)} onKeyDown={event => {
        if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing || event.keyCode === 229) return;
        event.preventDefault();
        if (canSend) onSend();
      }} onPaste={event => {
        const files = Array.from(event.clipboardData.files);
        if (!files.length || disabled || picking) return;
        event.preventDefault();
        onFiles(files);
      }} />
    <div className="wb-native-composer-bar">
      <div className="wb-native-composer-choices">
        <input hidden type="file" multiple ref={fileInput} onChange={event => {
          onFiles(Array.from(event.currentTarget.files ?? []));
          event.currentTarget.value = "";
        }} />
        <button type="button" className="wb-native-attach" aria-label="添加附件" title={atLimit ? "已达到 16 个附件上限" : "添加附件"}
          disabled={!canAttach} onClick={() => useFileInput ? fileInput.current?.click() : onPick()}>
          {picking ? <Spinner size={14} /> : <Icon name="plus" size={19} />}
        </button>
        <ComposerChoice label="发送权限" icon="shield" value={permission} options={permissions}
          onChange={value => onPermission(value as PermissionMode)} disabled={disabled} tone={permission} />
        <ComposerChoice label="会话模型" icon="models" value={model} options={[{ value: "", label: "跟随模型" }, ...models.map(item => ({ value: item.id, label: item.name }))]}
          onChange={onModel} disabled={disabled} />
        <ComposerChoice label="发送模式" icon="settings" value={mode} options={modes}
          onChange={value => onMode(value as SendMode)} disabled={disabled} />
        <ComposerChoice label="思考设置" icon="clock" value={thinking} options={thinkingOptions}
          onChange={value => onThinking(value as Thinking)} disabled={disabled} />
      </div>
      <button type="button" className="wb-send wb-native-send" aria-label={label} title={label}
        aria-busy={sending || stopping || undefined} disabled={running ? stopping : !canSend} onClick={running ? onStop : onSend}>
        {sending && !running ? <Spinner size={14} /> : <Icon name={running ? "stop" : "arrowUp"} size={18} />}
      </button>
    </div>
  </div>;
}
