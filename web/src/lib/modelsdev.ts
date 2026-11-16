export interface ModelsDevSummary {
  providers: number;
  models: number;
  providerNames: string[];
}

interface ModelsDevProvider {
  id?: unknown;
  name?: unknown;
  models?: unknown;
}

const CATALOG_METADATA_KEYS = new Set([
  "source",
  "retrieved_at",
  "sha256",
  "bytes",
  "provider_count",
  "$schema",
]);

function stripMetadata(value: Record<string, unknown>): Record<string, unknown> {
  const copy: Record<string, unknown> = {};
  for (const [key, entry] of Object.entries(value)) {
    if (CATALOG_METADATA_KEYS.has(key)) continue;
    copy[key] = entry;
  }
  return copy;
}

/**
 * 接受两种已保存形态：完整导出对象 `{source, retrieved_at, providers:{...}}`，
 * 或直接以 provider ID 为键的映射（可带 retrieved_at 等元数据键）。
 * 元数据键必须先剔除，否则会被误判成 provider 而整体拒绝。
 */
export function isModelsDevCatalog(value: unknown): value is Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const record = value as Record<string, unknown>;
  if (isProviderMap(record.providers)) return true;
  const providers = stripMetadata(record);
  // 只有元数据、没有 provider 的对象不是目录。
  if (Object.keys(providers).length === 0) return false;
  return isProviderMap(providers);
}

function isProviderMap(value: unknown): boolean {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const entries = Object.values(value as Record<string, unknown>);
  return entries.length > 0 && entries.every((entry) => !!entry && typeof entry === "object" && !Array.isArray(entry));
}

function providerMap(catalog: Record<string, unknown>): Record<string, ModelsDevProvider> {
  const nested = catalog.providers;
  if (isProviderMap(nested)) return nested as Record<string, ModelsDevProvider>;
  return stripMetadata(catalog) as Record<string, ModelsDevProvider>;
}

export function summarizeModelsDev(catalog: unknown): ModelsDevSummary {
  if (!isModelsDevCatalog(catalog)) return { providers: 0, models: 0, providerNames: [] };
  const map = providerMap(catalog as Record<string, unknown>);
  let models = 0;
  const providerNames: string[] = [];
  for (const [key, provider] of Object.entries(map)) {
    providerNames.push(typeof provider.name === "string" && provider.name ? provider.name : key);
    const modelMap = provider.models;
    if (modelMap && typeof modelMap === "object" && !Array.isArray(modelMap)) {
      models += Object.keys(modelMap as Record<string, unknown>).length;
    }
  }
  return { providers: Object.keys(map).length, models, providerNames };
}

// 核心要求 retrieved_at 作为 fetchedAt；缺失时不能用“现在”伪造来源时间。
export function catalogRetrievedAt(catalog: unknown): string | null {
  if (!catalog || typeof catalog !== "object") return null;
  const retrieved = (catalog as Record<string, unknown>).retrieved_at;
  if (typeof retrieved !== "string") return null;
  const date = new Date(retrieved);
  return Number.isNaN(date.getTime()) ? null : date.toISOString();
}

export function providerKeys(catalog: unknown): string[] {
  if (!isModelsDevCatalog(catalog)) return [];
  return Object.keys(providerMap(catalog as Record<string, unknown>));
}
