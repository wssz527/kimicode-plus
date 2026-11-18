// The one model catalog every picker reads. The desktop action `models.list`
// returns the official GET /models entries, whose `model` field is the
// provider-qualified key that config.models and secondary_model also use. A
// picker that invents its own key format writes configuration the official
// client accepts but never resolves, so all pickers go through here.

export interface ModelOption {
  /** Provider-qualified key: "provider/model". This is what gets written. */
  key: string;
  provider: string;
  model: string;
  displayName: string;
  maxContextSize?: number;
  capabilities: string[];
  supportEfforts: string[];
}

type RawModel = {
  provider?: unknown;
  model?: unknown;
  displayName?: unknown;
  display_name?: unknown;
  name?: unknown;
  id?: unknown;
  maxContextSize?: unknown;
  max_context_size?: unknown;
  capabilities?: unknown;
  supportEfforts?: unknown;
  support_efforts?: unknown;
};

function text(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

function numbers(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string" && item !== "") : [];
}

function integer(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? value : undefined;
}

/** Splits a provider-qualified key. A bare model id has no provider half. */
export function splitModelKey(key: string): { provider: string; model: string } {
  const index = key.indexOf("/");
  if (index <= 0 || index === key.length - 1) return { provider: "", model: key };
  return { provider: key.slice(0, index), model: key.slice(index + 1) };
}

export function modelOptionKey(item: ModelOption): string {
  return item.key;
}

export function normalizeModelOptions(payload: unknown): ModelOption[] {
  const items = Array.isArray(payload)
    ? payload
    : payload && typeof payload === "object" && Array.isArray((payload as { items?: unknown }).items)
      ? ((payload as { items: unknown[] }).items)
      : [];
  const out: ModelOption[] = [];
  const seen = new Set<string>();
  for (const raw of items as RawModel[]) {
    if (!raw || typeof raw !== "object") continue;
    const full = text(raw.model) || text(raw.id);
    if (!full) continue;
    const declared = text(raw.provider);
    const split = splitModelKey(full);
    // The official list repeats the provider inside `model`; a bare catalog only
    // carries the id. Both collapse onto the same provider-qualified key.
    const provider = declared || split.provider;
    const model = declared && full.startsWith(declared + "/") ? full.slice(declared.length + 1) : split.model;
    const key = provider ? `${provider}/${model}` : model;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push({
      key,
      provider,
      model,
      displayName: text(raw.displayName) || text(raw.display_name) || text(raw.name) || key,
      maxContextSize: integer(raw.maxContextSize) ?? integer(raw.max_context_size),
      capabilities: numbers(raw.capabilities),
      supportEfforts: numbers(raw.supportEfforts).length > 0 ? numbers(raw.supportEfforts) : numbers(raw.support_efforts),
    });
  }
  out.sort((left, right) => left.displayName.localeCompare(right.displayName, "zh-Hans-CN"));
  return out;
}

const CAPABILITY_LABELS: Record<string, string> = {
  image_in: "图片",
  video_in: "视频",
  tool_use: "工具",
  thinking: "思考",
  always_thinking: "始终思考",
};

/** A short capability summary such as "图片 · 工具 · 思考". */
export function capabilitySummary(model: ModelOption): string {
  const labels = model.capabilities
    .map(capability => CAPABILITY_LABELS[capability] ?? capability)
    .filter((label, index, all) => all.indexOf(label) === index);
  return labels.join(" · ");
}

export type ModelFetch = (action: string, payload?: Record<string, unknown>) => Promise<unknown>;

/** Reads the official catalog through the desktop action. */
export async function fetchModelOptions(request: ModelFetch): Promise<ModelOption[]> {
  return normalizeModelOptions(await request("models.list", {}));
}
