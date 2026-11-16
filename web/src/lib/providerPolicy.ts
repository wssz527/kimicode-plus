export function validProviderId(id: string): boolean {
  return /^[a-z0-9][a-z0-9._-]{0,63}$/.test(id);
}

export function validProviderURL(raw: string): boolean {
  if (raw.length > 500 || /[\s\\]/.test(raw)) return false;
  try {
    const url = new URL(raw);
    if (!/^https?:\/\//.test(raw) || !url.hostname || url.username || url.password || raw.includes("?") || raw.includes("#")) return false;
    if (url.port && (Number(url.port) < 1 || Number(url.port) > 65535)) return false;
    const authority=raw.match(/^https?:\/\/([^/]+)/)?.[1] ?? "";
    const rawHost=authority.startsWith("[") ? authority.slice(0,authority.indexOf("]")+1) : authority.split(":")[0];
    const loopback = url.hostname === "localhost" || url.hostname === "[::1]" || (rawHost===url.hostname && /^127\.(\d{1,3}\.){2}\d{1,3}$/.test(url.hostname));
    return url.protocol === "https:" || (url.protocol === "http:" && loopback);
  } catch {
    return false;
  }
}

export interface EditDraft<T> {
  baseRevision: number;
  baseEntity: T;
  draft: T;
  touched: Set<keyof T>;
}

export function beginEdit<T>(entity: T, revision: number): EditDraft<T> {
  return { baseRevision: revision, baseEntity: structuredClone(entity), draft: structuredClone(entity), touched: new Set() };
}
