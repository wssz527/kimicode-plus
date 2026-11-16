import type { CapabilityField, ModelPreview, PreviewChange } from "../api";

export interface RenderedField {
  field: string;
  before: string;
  after: string;
  changed: boolean;
  beforeSource: string;
  afterSource: string;
}

export interface RenderedChange {
  key: string;
  providerId: string;
  id: string;
  name: string;
  state: string;
  stateLabel: string;
  fields: RenderedField[];
}

export interface RenderedPreview {
  revision: number;
  changes: RenderedChange[];
  summary: { created: number; updated: number; unchanged: number };
  totalChanges: number;
}

function valueText(field: CapabilityField | undefined | null): string {
  if (!field) return "UNKNOWN";
  if (field.value === null) return "UNKNOWN";
  if (typeof field.value === "boolean") return field.value ? "true" : "false";
  if (typeof field.value === "number") return String(field.value);
  return String(field.value);
}

function sourceText(field: CapabilityField | undefined | null): string {
  return field?.source ?? "UNKNOWN";
}

export function renderPreview(preview: ModelPreview): RenderedPreview {
  const changes: RenderedChange[] = (preview.changes ?? []).map((change: PreviewChange) => ({
    key: `${change.providerId}/${change.id}`,
    providerId: change.providerId,
    id: change.id,
    name: change.name || change.id,
    state: change.state,
    stateLabel:
      change.state === "created" ? "新增" : change.state === "updated" ? "更新" : change.state,
    fields: (change.fields ?? []).map((field) => ({
      field: field.field,
      before: valueText(field.before),
      after: valueText(field.after),
      changed: valueText(field.before) !== valueText(field.after) || sourceText(field.before) !== sourceText(field.after),
      beforeSource: sourceText(field.before),
      afterSource: sourceText(field.after),
    })),
  }));
  const summary = {
    created: preview.summary?.created ?? 0,
    updated: preview.summary?.updated ?? 0,
    unchanged: preview.summary?.unchanged ?? 0,
  };
  return {
    revision: preview.revision,
    changes,
    summary,
    totalChanges: changes.reduce((sum, change) => sum + Math.max(1, change.fields.length), 0),
  };
}

export function hasChanges(preview: ModelPreview | null | undefined): boolean {
  return Boolean(preview && Array.isArray(preview.changes) && preview.changes.length > 0);
}
