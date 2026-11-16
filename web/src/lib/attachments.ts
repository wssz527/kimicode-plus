import type { HandoffAttachment } from "../../../adapters/types";
import { bytesToBase64, sha256Hex } from "../../../adapters/canonical";

export const MAX_ATTACHMENT_BYTES = 1_048_576;
export const MAX_ATTACHMENTS = 8;
export const MAX_TOTAL_ATTACHMENT_BYTES = 8_388_608;

export interface LocalAttachment {
  key: string;
  name: string;
  mime: string;
  size: number;
  sha256: string;
  dataBase64: string;
  addedAt: string;
}

export interface RejectedFile {
  name: string;
  reason: string;
}

export async function readLocalFiles(
  files: File[],
  existing: LocalAttachment[],
): Promise<{ accepted: LocalAttachment[]; rejected: RejectedFile[] }> {
  const accepted: LocalAttachment[] = [];
  const rejected: RejectedFile[] = [];
  let total = existing.reduce((sum, item) => sum + item.size, 0);
  let count = existing.length;
  for (const file of files) {
    if (count >= MAX_ATTACHMENTS) {
      rejected.push({ name: file.name, reason: `最多 ${MAX_ATTACHMENTS} 个附件` });
      continue;
    }
    if (file.size > MAX_ATTACHMENT_BYTES) {
      rejected.push({ name: file.name, reason: "单个附件超过 1 MiB" });
      continue;
    }
    if (total + file.size > MAX_TOTAL_ATTACHMENT_BYTES) {
      rejected.push({ name: file.name, reason: "附件总量超过 8 MiB（每个文件独立上传）" });
      continue;
    }
    if (!file.size) {
      rejected.push({ name: file.name, reason: "文件为空" });
      continue;
    }
    try {
      const buffer = await file.arrayBuffer();
      const bytes = new Uint8Array(buffer);
      const sha256 = await sha256Hex(bytes);
      accepted.push({
        key: `${file.name}-${sha256.slice(0, 12)}`,
        name: file.name,
        mime: file.type || "application/octet-stream",
        size: file.size,
        sha256,
        dataBase64: bytesToBase64(bytes),
        addedAt: new Date().toISOString(),
      });
      total += file.size;
      count += 1;
    } catch {
      rejected.push({ name: file.name, reason: "无法读取所选文件" });
    }
  }
  return { accepted, rejected };
}

export function toHandoffAttachment(attachment: LocalAttachment): HandoffAttachment {
  return {
    name: attachment.name,
    mime: attachment.mime,
    size: attachment.size,
    sha256: attachment.sha256,
    dataBase64: attachment.dataBase64,
    status: attachment.sha256.length === 64 ? "verified" : "UNKNOWN",
  };
}

export const SENSITIVE_PATTERNS: Array<{ id: string; label: string; pattern: RegExp }> = [
  { id: "openai-key", label: "疑似 API Key（sk-）", pattern: /\bsk-[A-Za-z0-9_-]{16,}\b/g },
  { id: "bearer", label: "Bearer 凭据", pattern: /\bBearer\s+[A-Za-z0-9._-]{16,}/gi },
  { id: "private-key", label: "私钥块", pattern: /-----BEGIN [A-Z ]*PRIVATE KEY-----/g },
  { id: "email", label: "邮箱地址", pattern: /\b[\w.+-]+@[\w-]+\.[\w.-]+\b/g },
  { id: "abs-path", label: "本机绝对路径", pattern: /(?:^|\s)(?:\/Users\/|\/home\/|[A-Za-z]:\\Users\\)[^\s"']+/g },
  { id: "github-token", label: "疑似 GitHub Token", pattern: /\bgh[pousr]_[A-Za-z0-9]{20,}\b/g },
];

export interface SensitiveHit {
  id: string;
  label: string;
  samples: string[];
}

export function scanSensitive(text: string): SensitiveHit[] {
  const hits: SensitiveHit[] = [];
  for (const rule of SENSITIVE_PATTERNS) {
    const pattern = new RegExp(rule.pattern.source, rule.pattern.flags);
    const samples = new Set<string>();
    let match: RegExpExecArray | null;
    while ((match = pattern.exec(text)) !== null) {
      const raw = match[0].trim();
      samples.add(maskSample(raw));
      if (samples.size >= 3) break;
    }
    if (samples.size > 0) hits.push({ id: rule.id, label: rule.label, samples: [...samples] });
  }
  return hits;
}

function maskSample(value: string): string {
  if (value.length <= 8) return "*".repeat(value.length);
  return `${value.slice(0, 4)}…${value.slice(-3)}`;
}

export function redactText(text: string): string {
  let output = text;
  for (const rule of SENSITIVE_PATTERNS) {
    output = output.replace(new RegExp(rule.pattern.source, rule.pattern.flags), (match) => {
      if (rule.id === "abs-path") return match.replace(/[^\s]+$/, "[路径已遮蔽]");
      return "[已遮蔽]";
    });
  }
  return output;
}
