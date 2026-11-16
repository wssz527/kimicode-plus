export interface FileValidation {
  catalog: unknown | null;
  text: string;
  error: string | null;
  bytes: number;
}

// 核心对 /models/modelsdev 放宽到 16 MiB；其余 JSON 请求仍是 2 MiB。
export const MAX_CATALOG_BYTES = 16 * 1024 * 1024;

export async function validateModelsDevFile(file: File): Promise<FileValidation> {
  if (file.size === 0) return { catalog: null, text: "", error: "文件为空", bytes: 0 };
  if (file.size > MAX_CATALOG_BYTES) {
    return { catalog: null, text: "", error: "文件超过 16 MB，请先裁剪到需要的供应商", bytes: file.size };
  }
  let text: string;
  try {
    text = await file.text();
  } catch {
    return { catalog: null, text: "", error: "无法读取该文件", bytes: file.size };
  }
  try {
    const catalog = JSON.parse(text) as unknown;
    if (!catalog || typeof catalog !== "object" || Array.isArray(catalog)) {
      return { catalog: null, text, error: "JSON 顶层必须是对象", bytes: file.size };
    }
    return { catalog, text, error: null, bytes: file.size };
  } catch {
    return { catalog: null, text, error: "JSON 无法解析，请检查文件内容", bytes: file.size };
  }
}
