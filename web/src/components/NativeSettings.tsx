import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { asConfigObject, commitNumberField, configMaxSteps, configSection, configField, configTimeoutMinutes, dangerousGuardPatch, matchesModelPool, matchesPermissionRules, readDangerousGuard, readSubagentPoolState, useHost } from "../native";
import { Icon } from "./Icon";
import { Badge, ErrorNotice, Spinner } from "./ui";
import "../styles/workbench.css";
import "../styles/native-settings.css";
import { errorText } from "./workbench/pieces";
import { ModelSelect } from "./ModelSelect";
import { fetchModelOptions, type ModelOption } from "../lib/models";

type Timing = "now" | "derive" | "turn";

const TIMING: Record<Timing, { label: string; note: string }> = {
  now: { label: "即时", note: "保存后下一次操作就用新值，运行中的会话也适用。" },
  derive: { label: "下次派生", note: "已有的子代理保持旧值，新派生的规划/审查才用新值。" },
  turn: { label: "下一轮", note: "当前回合继续用旧上限，下一轮开始生效。" },
};

const READBACK_MISMATCH = "官方核心读回的值和刚提交的不一致，可能被这一版忽略。请核对后再改其他项。";

type Dic = Record<string, unknown>;

interface PoolEntry {
  id: string;
  key: string;
  desc: string;
}

interface PoolEditor {
  entries: PoolEntry[];
  existingDefaultModel: string;
  rest: Dic;
}

interface RuleRow {
  id: string;
  decision: string;
  scope: string;
  pattern: string;
  reason: string;
  rest: Dic;
}

const DECISIONS = [
  { value: "allow", label: "允许" },
  { value: "ask", label: "询问" },
  { value: "deny", label: "拒绝" },
];

const SCOPES = [
  { value: "user", label: "用户级" },
  { value: "project", label: "项目级" },
];

let seq = 0;

function nextId(prefix: string): string {
  seq += 1;
  return `${prefix}-${seq}`;
}

/**
 * The visible validation message for a number field. An unset setting arrives as
 * an empty `value`; that is a normal state, not an error. Only an edit that
 * erased a valid value needs the red hint, and an invalid partial edit needs the
 * range hint.
 */
export function numberFieldIssue(value: string, text: string, min: number, max: number): string | null {
  const trimmed = text.trim();
  if (value === "" && trimmed === "") return null;
  if (trimmed === "") return `不能留空，填写 ${min}–${max} 之间的整数；离开输入框会恢复上一个有效值。`;
  if (commitNumberField(text, min, max) === null) return `填写 ${min}–${max} 之间的整数。`;
  return null;
}

function omit(source: Dic, keys: string[]): Dic {
  const rest: Dic = { ...source };
  for (const key of keys) delete rest[key];
  return rest;
}

function readPool(cfg: Dic | null): PoolEditor {
  const parsed = readSubagentPoolState(cfg);
  return {
    entries: parsed.entries.map((entry) => ({
      id: nextId("pool"),
      key: entry.key,
      desc: entry.desc,
    })),
    existingDefaultModel: parsed.existingDefaultModel,
    rest: parsed.rest,
  };
}

function reorderPoolEntries(
  entries: PoolEntry[],
  sourceId: string,
  targetId: string,
  position: "before" | "after",
): PoolEntry[] {
  const fromIndex = entries.findIndex((item) => item.id === sourceId);
  const toIndex = entries.findIndex((item) => item.id === targetId);
  if (fromIndex === -1 || toIndex === -1 || fromIndex === toIndex) return entries;
  const next = [...entries];
  const [moved] = next.splice(fromIndex, 1);
  if (!moved) return entries;
  const rawTarget = position === "after" ? toIndex + 1 : toIndex;
  const insertIndex = fromIndex < rawTarget ? rawTarget - 1 : rawTarget;
  next.splice(Math.max(0, Math.min(next.length, insertIndex)), 0, moved);
  return next;
}

function readRules(raw: unknown): RuleRow[] {
  if (!Array.isArray(raw)) return [];
  const rows: RuleRow[] = [];
  for (const item of raw) {
    const dic = asConfigObject(item);
    if (!dic) continue;
    rows.push({
      id: nextId("rule"),
      decision: typeof dic.decision === "string" ? dic.decision : "ask",
      scope: typeof dic.scope === "string" ? dic.scope : "user",
      pattern: typeof dic.pattern === "string" ? dic.pattern : "",
      reason: typeof dic.reason === "string" ? dic.reason : "",
      rest: omit(dic, ["decision", "scope", "pattern", "reason"]),
    });
  }
  return rows;
}

function patternIssue(pattern: string): string | null {
  const trimmed = pattern.trim();
  if (!trimmed) return "填写工具名或匹配表达式。";
  const open = trimmed.indexOf("(");
  if (open === -1) return null;
  if (!trimmed.endsWith(")")) return "括号没有闭合。";
  if (open === 0) return "左括号前要写工具名。";
  return null;
}

function SettingRow({
  title,
  purpose,
  timing,
  children,
  footer,
}: {
  title: string;
  purpose: string;
  timing: Timing;
  children: ReactNode;
  footer?: ReactNode;
}) {
  return (
    <section className="ns-row">
      <header className="ns-row-head">
        <h2>{title}</h2>
        <Badge tone={timing === "now" ? "success" : "neutral"}>{TIMING[timing].label}</Badge>
      </header>
      <p className="ns-purpose">{purpose}</p>
      <div className="ns-controls">{children}</div>
      {footer ? <div className="ns-foot">{footer}</div> : null}
    </section>
  );
}

function SaveButton({ busy, disabled, onClick }: { busy: boolean; disabled: boolean; onClick: () => void }) {
  return (
    <button type="button" className="wb-btn is-primary is-compact" onClick={onClick} disabled={busy || disabled}>
      {busy ? <Spinner size={12} /> : null}
      保存
    </button>
  );
}

function GripIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor" aria-hidden="true" focusable="false">
      <circle cx="5.5" cy="3.5" r="1.15" />
      <circle cx="10.5" cy="3.5" r="1.15" />
      <circle cx="5.5" cy="8" r="1.15" />
      <circle cx="10.5" cy="8" r="1.15" />
      <circle cx="5.5" cy="12.5" r="1.15" />
      <circle cx="10.5" cy="12.5" r="1.15" />
    </svg>
  );
}

/**
 * A number input that only submits on blur or Enter, so a half-typed value never
 * reaches the core. The text is owned locally while focused; an incoming value
 * must not overwrite what the user is still typing.
 */
function NumberField({
  label,
  value,
  min,
  max,
  unit,
  hint,
  onCommit,
}: {
  label: string;
  value: string;
  min: number;
  max: number;
  unit: string;
  hint?: string;
  onCommit: (value: number) => void;
}) {
  const [text, setText] = useState(value);
  const focused = useRef(false);
  useEffect(() => {
    if (!focused.current) setText(value);
  }, [value]);
  const trimmed = text.trim();
  const parsed = trimmed === "" ? null : commitNumberField(text, min, max);
  const issue = numberFieldIssue(value, text, min, max);
  function commit() {
    if (parsed !== null && String(parsed) !== value) onCommit(parsed);
    else if (parsed === null) setText(value);
  }
  return (
    <label className="ns-field">
      <span className="wb-label">{label}</span>
      <span className="ns-number">
        <input
          className="wb-input num"
          type="number"
          min={min}
          max={max}
          step={1}
          value={text}
          aria-invalid={issue ? true : undefined}
          onFocus={() => {
            focused.current = true;
          }}
          onChange={(event) => setText(event.target.value)}
          onBlur={() => {
            focused.current = false;
            commit();
          }}
          onKeyDown={(event) => {
            if (event.key === "Enter") event.currentTarget.blur();
          }}
        />
        <span className="ns-unit">{unit}</span>
      </span>
      {issue ? <span className="ns-issue">{issue}</span> : null}
      {!issue && hint ? <span className="wb-hint">{hint}</span> : null}
    </label>
  );
}

export type NativeSettingsMode = "all" | "subagent" | "permissions";

export function NativeSettings({
  embedded = false,
  mode = "all",
}: {
  embedded?: boolean;
  mode?: NativeSettingsMode;
} = {}) {
  const host = useHost();
  const [config, setConfig] = useState<Dic | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const [pool, setPool] = useState<PoolEditor>({ entries: [], existingDefaultModel: "", rest: {} });
  const [draggingId, setDraggingId] = useState<string | null>(null);
  const [dropTarget, setDropTarget] = useState<{ id: string; position: "before" | "after" } | null>(null);
  const [dragBlockedId, setDragBlockedId] = useState<string | null>(null);

  const [timeoutMin, setTimeoutMin] = useState("");
  const [maxSteps, setMaxSteps] = useState("");
  const [rules, setRules] = useState<RuleRow[]>([]);
  const [guard, setGuard] = useState(false);

  const [saving, setSaving] = useState<string | null>(null);
  const [saved, setSaved] = useState<{ key: string; timing: Timing; note?: string } | null>(null);
  const [saveError, setSaveError] = useState<string | null>(null);
  const savingRef = useRef<string | null>(null);
  savingRef.current = saving;
  // A background config change must not erase an edit the user has not saved
  // yet. Each section marks itself dirty on the first local change and is only
  // cleared by a readback that includes that edit.
  const [poolDirty, setPoolDirty] = useState(false);
  const [rulesDirty, setRulesDirty] = useState(false);
  const dirtyRef = useRef({ pool: false, rules: false });
  dirtyRef.current = { pool: poolDirty, rules: rulesDirty };
  const markPoolDirty = useCallback((..._args: unknown[]) => {
    if (!dirtyRef.current.pool) setPoolDirty(true);
  }, []);
  const markRulesDirty = useCallback(() => {
    if (!dirtyRef.current.rules) setRulesDirty(true);
  }, []);
  // Edits go through these wrappers so the draft guard sees them; a config
  // readback writes the state setters directly and therefore never marks dirty.
  const editPool = useCallback((action: Parameters<typeof setPool>[0]) => {
    markPoolDirty();
    setPool(action);
  }, [markPoolDirty]);
  const editRules = useCallback((action: Parameters<typeof setRules>[0]) => {
    markRulesDirty();
    setRules(action);
  }, [markRulesDirty]);

  const applyConfig = useCallback((cfg: Dic) => {
    setConfig(cfg);
    setPool(readPool(cfg));
    setTimeoutMin(configTimeoutMinutes(cfg));
    setMaxSteps(configMaxSteps(cfg));
    const permission = configSection(cfg, ["permission"]);
    setRules(readRules(configField(permission, ["rules"])));
    setGuard(readDangerousGuard(cfg));
  }, []);

  useEffect(() => {
    if (!host) return;
    let alive = true;
    const refresh = () => {
      if (savingRef.current !== null) return;
      // A draft in either editor wins over an automatic refresh; the next save
      // or an explicit remount is what reconciles it.
      if (dirtyRef.current.pool || dirtyRef.current.rules) return;
      host
        .request<Dic>("config.get")
        .then((next) => {
          if (alive) {
            applyConfig(asConfigObject(next) ?? {});
            setLoadError(null);
          }
        })
        .catch((cause) => {
          if (alive) setLoadError(errorText(cause));
        })
        .finally(() => {
          if (alive) setLoading(false);
        });
    };
    refresh();
    const onOfficialChange = () => refresh();
    window.addEventListener("ekd:official-config-changed", onOfficialChange);
    return () => {
      alive = false;
      window.removeEventListener("ekd:official-config-changed", onOfficialChange);
    };
  }, [host, applyConfig]);

  // The pool is chosen from the official catalog, the same list handoff and the
  // chat page read. Keys are provider-qualified, so a bare alias can never be
  // written into secondary_model.
  const [modelOptions, setModelOptions] = useState<ModelOption[]>([]);
  const [modelOptionsError, setModelOptionsError] = useState<string | null>(null);
  useEffect(() => {
    if (!host) return;
    let alive = true;
    fetchModelOptions(host.request)
      .then((next) => {
        if (alive) {
          setModelOptions(next);
          setModelOptionsError(null);
        }
      })
      .catch((cause) => {
        if (alive) setModelOptionsError(errorText(cause));
      });
    return () => {
      alive = false;
    };
  }, [host]);

  const poolKeys = useMemo(
    () => pool.entries.map((entry) => entry.key.trim()).filter(Boolean),
    [pool.entries],
  );
  const poolIssue = useMemo(() => {
    if (poolKeys.length !== pool.entries.length) return "每一行都要选模型，或删掉空行。";
    if (new Set(poolKeys).size !== poolKeys.length) return "同一个模型只能出现一次。";
    return null;
  }, [pool.entries.length, poolKeys]);

  const ruleIssues = rules.map((rule) => patternIssue(rule.pattern));

  function movePoolEntry(id: string, delta: -1 | 1) {
    editPool((current) => {
      const index = current.entries.findIndex((item) => item.id === id);
      const target = index + delta;
      if (index === -1 || target < 0 || target >= current.entries.length) return current;
      const entries = [...current.entries];
      const [moved] = entries.splice(index, 1);
      if (!moved) return current;
      entries.splice(target, 0, moved);
      return { ...current, entries };
    });
  }

  function openProvidersSettings() {
    if (!host) return;
    void host.openSettings("providers").catch((cause: unknown) => setModelOptionsError(errorText(cause)));
  }

  /**
   * Writes are only reported as saved after a fresh GET agrees. A 200 from
   * config.save proves the request was accepted, not that the field changed:
   * the current KAP schema silently drops fields it does not know.
   */
  async function save(
    key: string,
    timing: Timing,
    patch: Dic | Dic[],
    options: { note?: string; verify?: (cfg: Dic) => boolean; apply?: (cfg: Dic) => void },
  ) {
    if (!host) return;
    const steps = Array.isArray(patch) ? patch : [patch];
    setSaving(key);
    setSaveError(null);
    setSaved(null);
    try {
      for (const step of steps) await host.request<Dic>("config.save", { patch: step });
      const latest = asConfigObject(await host.request<Dic>("config.get")) ?? {};
      (options.apply ?? applyConfig)(latest);
      if (options.verify && !options.verify(latest)) {
        setSaveError(READBACK_MISMATCH);
        return;
      }
      if (key === "pool") setPoolDirty(false);
      if (key === "permission") setRulesDirty(false);
      setSaved({ key, timing, note: options.note });
      if (typeof window !== "undefined") {
        window.dispatchEvent(new CustomEvent("ekd:config-saved", { detail: { key } }));
      }
    } catch (cause) {
      setSaveError(errorText(cause));
    } finally {
      setSaving(null);
    }
  }

  function savePool() {
    if (poolIssue) return;
    const models: Record<string, string> = {};
    for (const entry of pool.entries) {
      const key = entry.key.trim();
      if (key) models[key] = entry.desc.trim();
    }
    const apply = (cfg: Dic) => {
      setConfig(cfg);
      setPool(readPool(cfg));
    };
    if (poolKeys.length === 0) {
      void save("pool", "derive", { secondary_model: null }, {
        note: "模型池已清空，子代理改为跟随调用方的模型。",
        apply,
        verify: (cfg) => configSection(cfg, ["secondary_model", "secondaryModel"]) === null,
      });
      return;
    }
    const defaultModel =
      pool.existingDefaultModel && models[pool.existingDefaultModel] !== undefined
        ? pool.existingDefaultModel
        : poolKeys[0]!;
    const verify = (cfg: Dic) => matchesModelPool(cfg, { models, defaultModel });
    const full: Dic = { ...pool.rest, models, defaultModel };
    const current = configSection(config, ["secondary_model", "secondaryModel"]);
    const currentKeys = Object.keys(asConfigObject(configField(current, ["models"])) ?? {});
    const nextKeys = Object.keys(models);
    const removed = currentKeys.filter((key) => models[key] === undefined);
    const legacy = configField(current, ["model"]) !== undefined;
    const orderChanged =
      currentKeys.length !== nextKeys.length ||
      currentKeys.some((key, index) => key !== nextKeys[index]);
    if (removed.length === 0 && !legacy && !orderChanged) {
      void save("pool", "derive", { secondary_model: full }, {
        note: "下次派生生效，已在运行的子代理保持旧模型。",
        apply,
        verify,
      });
      return;
    }
    void save(
      "pool",
      "derive",
      [{ secondary_model: null }, { secondary_model: full }],
      {
        note:
          removed.length > 0
            ? `已整体替换模型池，移除的 ${removed.length} 个条目不再保留；下次派生生效。`
            : legacy
              ? "已整体替换模型池，旧的单模型设置不再保留；下次派生生效。"
              : "已按当前优先级顺序保存模型池；下次派生生效。",
        apply,
        verify,
      },
    );
  }

  function saveRules() {
    if (ruleIssues.some(Boolean)) return;
    const payload = rules.map((rule) => {
      const item: Dic = {
        ...rule.rest,
        decision: rule.decision,
        scope: rule.scope,
        pattern: rule.pattern.trim(),
      };
      const reason = rule.reason.trim();
      if (reason) item.reason = reason;
      return item;
    });
    // The guard has its own immediate save. Writing it here too would let a
    // stale switch value in this form overwrite a newer server-side setting.
    void save(
      "permission",
      "now",
      { permission: { rules: payload } },
      {
        note: "即时生效，下一次权限判定就使用新规则。",
        apply: (cfg) => {
          setConfig(cfg);
          const permission = configSection(cfg, ["permission"]);
          setRules(readRules(configField(permission, ["rules"])));
        },
        verify: (cfg) => matchesPermissionRules(cfg, payload),
      },
    );
  }

  /**
   * The dangerous-command guard is one boolean that takes effect immediately, so
   * it saves on its own. `permission` merges key by key on the core, so sending
   * only this field leaves whatever rules are stored there untouched — and the
   * in-progress rule rows above are never read, written or reset by this path.
   */
  async function saveGuard(next: boolean) {
    if (!host) return;
    const previous = guard;
    setGuard(next);
    setSaving("guard");
    setSaveError(null);
    setSaved(null);
    try {
      await host.request("config.save", { patch: dangerousGuardPatch(next) });
      const latest = asConfigObject(await host.request<Dic>("config.get")) ?? {};
      const actual = readDangerousGuard(latest);
      if (actual !== next) {
        setGuard(actual);
        setSaveError(READBACK_MISMATCH);
        return;
      }
      setSaved({ key: "guard", timing: "now", note: "护栏已即时更新，下一次权限判定就使用新值。" });
      if (typeof window !== "undefined") {
        window.dispatchEvent(new CustomEvent("ekd:config-saved", { detail: { key: "guard" } }));
      }
    } catch (cause) {
      setGuard(previous);
      setSaveError(errorText(cause));
    } finally {
      setSaving(null);
    }
  }

  if (!host) {
    return (
      <div className={embedded ? "wb ns ns-embedded" : "wb ns"}>
        {embedded ? null : <header className="wb-head">
          <span className="wb-mark" aria-hidden="true" />
          <h1>原生设置</h1>
        </header>}
        <div className="wb-body">
          <p className="ns-empty">请从 Kimi Code 的宿主入口打开此页面。独立预览拿不到原生配置。</p>
        </div>
      </div>
    );
  }

  if (loading) {
    return (
      <div className="wb is-booting" role="status" aria-live="polite">
        <Spinner size={16} />
        <span>正在读取原生配置</span>
      </div>
    );
  }

  const scopeChoices = SCOPES.map((scope) => scope.value);
  const showSubagent = mode === "all" || mode === "subagent";
  const showPermissions = mode === "all" || mode === "permissions";
  const officialSectionTitle =
    embedded && mode === "permissions"
      ? "权限规则"
      : embedded && mode === "subagent"
        ? "子智能体模型池与运行上限"
        : null;

  return (
    <div
      className={embedded ? "wb ns ns-embedded" : "wb ns"}
      data-ekd-native-mode={mode}
    >
      {embedded ? null : <header className="wb-head">
        <span className="wb-mark" aria-hidden="true" />
        <h1>原生设置</h1>
        <div className="wb-head-actions">
          <button type="button" className="wb-icon-btn" title="关闭" onClick={() => host.close()}>
            <Icon name="close" size={15} />
          </button>
        </div>
      </header>}

      <div className="wb-body ns-body">
        {officialSectionTitle ? (
          <div className="ekds-sec-head">
            <h3 className="ekds-sec-title">{officialSectionTitle}</h3>
          </div>
        ) : (
          <p className="ns-lede">
            管理子代理模型池与优先级、子代理时限、单轮步数上限及权限规则。保存后均会通过官方接口重新读回核对。
          </p>
        )}

        {loadError ? (
          <ErrorNotice title="配置没有读到" message={loadError} recovery="确认官方核心在运行后重新打开此页。" />
        ) : null}
        {saveError ? <ErrorNotice title="保存没有完成" message={saveError} recovery="读回的值以官方核心为准，可以再改一次。" /> : null}

        {showSubagent ? (
          <>
            <SettingRow
          title="子代理模型池"
          purpose="子代理可按优先级顺序从这些模型里选择，用途说明写清它适合什么工作。拖动或调整顺位后点击保存；清空后子代理跟随调用方的模型。"
          timing="derive"
          footer={
            <>
              <SaveButton busy={saving === "pool"} disabled={!config || Boolean(poolIssue)} onClick={savePool} />
              {saved?.key === "pool" ? <span className="ns-saved">{saved.note}</span> : null}
            </>
          }
        >
          <div className="ns-list-head">
            <span className="wb-label">池内模型（按优先级从高到低）</span>
            <button
              type="button"
              className="wb-btn is-quiet is-compact"
              onClick={() =>
                editPool((current) => ({
                  ...current,
                  entries: [...current.entries, { id: nextId("pool"), key: "", desc: "" }],
                }))
              }
            >
              <Icon name="plus" size={12} />
              添加模型
            </button>
          </div>

          {pool.entries.length === 0 ? (
            modelOptions.length === 0 ? (
              <div className="ns-empty-line">
                <p>还没有可选的模型，请先在 Kimi Code 设置 → 供应商中添加。</p>
                {modelOptionsError ? <p>读取失败：{modelOptionsError}</p> : null}
                <button
                  type="button"
                  className="wb-btn is-quiet is-compact"
                  onClick={openProvidersSettings}
                >
                  打开官方供应商页
                </button>
              </div>
            ) : (
              <p className="ns-empty-line">还没有池内模型，保存后子代理跟随调用方的模型。</p>
            )
          ) : (
            <div className="ns-pool-list" role="list" aria-label="子代理模型优先级列表">
              {pool.entries.map((entry, index) => (
                <div
                  role="listitem"
                  className={[
                    "ns-pool-card",
                    draggingId === entry.id ? "is-dragging" : "",
                    dropTarget?.id === entry.id && dropTarget.position === "before" ? "is-drop-before" : "",
                    dropTarget?.id === entry.id && dropTarget.position === "after" ? "is-drop-after" : "",
                  ]
                    .filter(Boolean)
                    .join(" ")}
                  key={entry.id}
                  draggable={dragBlockedId !== entry.id}
                  onDragStart={(event) => {
                    setDraggingId(entry.id);
                    event.dataTransfer.effectAllowed = "move";
                    event.dataTransfer.setData("text/plain", entry.id);
                  }}
                  onDragOver={(event) => {
                    if (!draggingId || draggingId === entry.id) return;
                    event.preventDefault();
                    event.dataTransfer.dropEffect = "move";
                    const rect = event.currentTarget.getBoundingClientRect();
                    const position = event.clientY < rect.top + rect.height / 2 ? "before" : "after";
                    setDropTarget((current) =>
                      current?.id === entry.id && current.position === position ? current : { id: entry.id, position },
                    );
                  }}
                  onDragLeave={(event) => {
                    if (!event.currentTarget.contains(event.relatedTarget as Node | null)) {
                      setDropTarget((current) => (current?.id === entry.id ? null : current));
                    }
                  }}
                  onDrop={(event) => {
                    event.preventDefault();
                    const sourceId = draggingId || event.dataTransfer.getData("text/plain");
                    const position = dropTarget?.id === entry.id ? dropTarget.position : "before";
                    if (sourceId && sourceId !== entry.id) {
                      editPool((current) => ({
                        ...current,
                        entries: reorderPoolEntries(current.entries, sourceId, entry.id, position),
                      }));
                    }
                    setDraggingId(null);
                    setDropTarget(null);
                  }}
                  onDragEnd={() => {
                    setDraggingId(null);
                    setDropTarget(null);
                  }}
                  onKeyDown={(event) => {
                    if (event.altKey && (event.key === "ArrowUp" || event.key === "ArrowDown")) {
                      event.preventDefault();
                      movePoolEntry(entry.id, event.key === "ArrowUp" ? -1 : 1);
                    }
                  }}
                >
                  <div className="ns-pool-handle">
                    <span className="ns-grip" title="拖动调整优先级顺序（也支持 Alt+↑ / ↓）" aria-hidden="true">
                      <GripIcon />
                    </span>
                    <span className="ns-rank" title={`优先级第 ${index + 1} 位`}>
                      {index + 1}
                    </span>
                  </div>

                  <div className="ns-pool-fields">
                    <div
                      className="ns-pool-model"
                      onMouseEnter={() => setDragBlockedId(entry.id)}
                      onMouseLeave={() => setDragBlockedId((current) => (current === entry.id ? null : current))}
                      onFocusCapture={() => setDragBlockedId(entry.id)}
                      onBlurCapture={() => setDragBlockedId((current) => (current === entry.id ? null : current))}
                    >
                      <ModelSelect
                        className="wb-select ns-pool-select"
                        options={modelOptions}
                        value={entry.key}
                        onChange={(key) =>
                          editPool((current) => ({
                            ...current,
                            entries: current.entries.map((item) => (item.id === entry.id ? { ...item, key } : item)),
                          }))
                        }
                      />
                    </div>
                    <div
                      className="ns-pool-desc"
                      onMouseEnter={() => setDragBlockedId(entry.id)}
                      onMouseLeave={() => setDragBlockedId((current) => (current === entry.id ? null : current))}
                      onFocusCapture={() => setDragBlockedId(entry.id)}
                      onBlurCapture={() => setDragBlockedId((current) => (current === entry.id ? null : current))}
                    >
                      <input
                        className="wb-input"
                        aria-label={`${entry.key || "模型"} 的用途说明`}
                        value={entry.desc}
                        placeholder="用途说明（例如 日常实现）"
                        onChange={(event) =>
                          editPool((current) => ({
                            ...current,
                            entries: current.entries.map((item) =>
                              item.id === entry.id ? { ...item, desc: event.target.value } : item,
                            ),
                          }))
                        }
                      />
                    </div>
                  </div>

                  <div className="ns-pool-actions">
                    <button
                      type="button"
                      className="wb-icon-btn ns-move-btn"
                      disabled={index === 0}
                      aria-label="上移优先级"
                      title="上移优先级 (Alt+↑)"
                      onClick={() => movePoolEntry(entry.id, -1)}
                    >
                      <Icon name="chevronUp" size={13} />
                    </button>
                    <button
                      type="button"
                      className="wb-icon-btn ns-move-btn"
                      disabled={index === pool.entries.length - 1}
                      aria-label="下移优先级"
                      title="下移优先级 (Alt+↓)"
                      onClick={() => movePoolEntry(entry.id, 1)}
                    >
                      <Icon name="chevronDown" size={13} />
                    </button>
                    <button
                      type="button"
                      className="wb-icon-btn ns-row-remove"
                      aria-label="移除这个模型"
                      title="移除这个模型"
                      onClick={() =>
                        editPool((current) => ({
                          ...current,
                          entries: current.entries.filter((item) => item.id !== entry.id),
                        }))
                      }
                    >
                      <Icon name="trash" size={13} />
                    </button>
                  </div>
                </div>
              ))}
            </div>
          )}

          {poolIssue ? <p className="ns-issue">{poolIssue}</p> : null}
          {pool.entries.length > 0 ? (
            <p className="wb-hint">
              支持直接拖拽卡片或点击上下箭头调整 1、2、3… 顺位；保存后按当前顺序重写配置。单个模型的能力、思考强度与上下文长度统一在{" "}
              <button type="button" className="ns-inline-link" onClick={openProvidersSettings}>
                模型供应商
              </button>{" "}
              对应模型行展开修改。
            </p>
          ) : null}
        </SettingRow>

        <SettingRow
          title="子代理时限"
          purpose="单个子代理最长运行多久。超时后该子代理会被终止。"
          timing="derive"
          footer={
            saving === "timeout" ? <span className="ns-saved"><Spinner size={12} /> 正在保存</span> : null
          }
        >
          <NumberField
            label="分钟"
            value={timeoutMin}
            min={1}
            max={10080}
            unit="分钟"
            hint="默认 120 分钟，对应 7200000 毫秒。离开输入框即保存。"
            onCommit={(value) => {
              void save("timeout", "derive", { subagent: { timeout_ms: Math.round(value * 60000) } }, {
                note: "下次派生生效，已有的子代理继续用旧时限。",
                apply: (cfg) => {
                  setConfig(cfg);
                  setTimeoutMin(configTimeoutMinutes(cfg));
                },
                verify: (cfg) => Number(configField(configSection(cfg, ["subagent"]), ["timeout_ms", "timeoutMs"])) === value * 60000,
              });
            }}
          />
          {saved?.key === "timeout" ? <p className="ns-saved">{saved.note}</p> : null}
        </SettingRow>

        <SettingRow
          title="单轮步数上限"
          purpose="一个回合里 agent 最多走多少步，用来限制长任务发散。0 表示不设上限。"
          timing="turn"
          footer={
            saving === "steps" ? <span className="ns-saved"><Spinner size={12} /> 正在保存</span> : null
          }
        >
          <NumberField
            label="步数"
            value={maxSteps}
            min={0}
            max={10000}
            unit="步"
            hint="离开输入框即保存；0 表示无上限。"
            onCommit={(value) => {
              void save("steps", "turn", { loop_control: { max_steps_per_turn: value } }, {
                note: "下一轮生效，当前回合继续用旧上限。",
                apply: (cfg) => {
                  setConfig(cfg);
                  setMaxSteps(configMaxSteps(cfg));
                },
                verify: (cfg) => Number(configField(configSection(cfg, ["loop_control", "loopControl"]), ["max_steps_per_turn", "maxStepsPerTurn"])) === value,
              });
            }}
          />
          {saved?.key === "steps" ? <p className="ns-saved">{saved.note}</p> : null}
        </SettingRow>
          </>
        ) : null}

        {showPermissions ? (
        <SettingRow
          title="权限与危险命令"
          purpose="规则按工具名或参数决定直接允许、先询问还是拒绝；从上往下逐条判定。危险命令护栏对命中名单的命令先拦一次。"
          timing="now"
          footer={
            <>
              <SaveButton
                busy={saving === "permission"}
                disabled={!config || ruleIssues.some(Boolean)}
                onClick={saveRules}
              />
              {saved?.key === "permission" ? <span className="ns-saved">{saved.note}</span> : null}
            </>
          }
        >
          <div className="ns-list-head">
            <span className="wb-label">权限规则</span>
            <button
              type="button"
              className="wb-btn is-quiet is-compact"
              onClick={() =>
                editRules((current) => [
                  ...current,
                  { id: nextId("rule"), decision: "ask", scope: "user", pattern: "", reason: "", rest: {} },
                ])
              }
            >
              <Icon name="plus" size={12} />
              添加规则
            </button>
          </div>

          {rules.length === 0 ? (
            <p className="ns-empty-line">还没有规则，工具调用按官方默认的权限流程处理。</p>
          ) : (
            <>
              <div className="ns-rule-headrow" aria-hidden="true">
                <span className="ns-head-cell">结论</span>
                <span className="ns-head-cell">匹配表达式</span>
                <span className="ns-head-cell">范围</span>
                <span className="ns-head-cell">备注</span>
                <span />
              </div>
              <div className="ns-rule-list">
                {rules.map((rule, index) => {
                  const issue = ruleIssues[index];
                  const scopes = scopeChoices.includes(rule.scope) ? SCOPES : [...SCOPES, { value: rule.scope, label: `${rule.scope}（原值）` }];
                  const decisions = DECISIONS.some((item) => item.value === rule.decision)
                    ? DECISIONS
                    : [...DECISIONS, { value: rule.decision, label: rule.decision }];
                  return (
                    <div className="ns-rule-row" key={rule.id}>
                      <div className="ns-rule-decision">
                        <select
                          className="wb-select"
                          aria-label="判定结果"
                          value={rule.decision}
                          onChange={(event) =>
                            editRules((current) =>
                              current.map((item) =>
                                item.id === rule.id ? { ...item, decision: event.target.value } : item,
                              ),
                            )
                          }
                        >
                          {decisions.map((item) => (
                            <option key={item.value} value={item.value}>
                              {item.label}
                            </option>
                          ))}
                        </select>
                      </div>
                      <div className="ns-rule-pattern">
                        <input
                          className="wb-input"
                          aria-label="匹配表达式"
                          value={rule.pattern}
                          placeholder="例如 bash(rm -rf*)"
                          onChange={(event) =>
                            editRules((current) =>
                              current.map((item) =>
                                item.id === rule.id ? { ...item, pattern: event.target.value } : item,
                              ),
                            )
                          }
                        />
                      </div>
                      <div className="ns-rule-scope">
                        <select
                          className="wb-select"
                          aria-label="生效范围"
                          value={rule.scope}
                          onChange={(event) =>
                            editRules((current) =>
                              current.map((item) =>
                                item.id === rule.id ? { ...item, scope: event.target.value } : item,
                              ),
                            )
                          }
                        >
                          {scopes.map((item) => (
                            <option key={item.value} value={item.value}>
                              {item.label}
                            </option>
                          ))}
                        </select>
                      </div>
                      <div className="ns-rule-reason">
                        <input
                          className="wb-input"
                          aria-label="备注"
                          value={rule.reason}
                          placeholder="备注（可选）"
                          onChange={(event) =>
                            editRules((current) =>
                              current.map((item) =>
                                item.id === rule.id ? { ...item, reason: event.target.value } : item,
                              ),
                            )
                          }
                        />
                      </div>
                      <button
                        type="button"
                        className="wb-icon-btn ns-row-remove"
                        title="删除这条规则"
                        onClick={() => editRules((current) => current.filter((item) => item.id !== rule.id))}
                      >
                        <Icon name="trash" size={13} />
                      </button>
                      {issue ? <p className="ns-issue">{issue}</p> : null}
                    </div>
                  );
                })}
              </div>
            </>
          )}

          <p className="wb-hint">
            匹配表达式写工具名，或“工具名(参数匹配)”，例如 read、bash(rm -rf*)、write(src/*)。
          </p>

          <label className="wb-switch">
            <input
              type="checkbox"
              checked={guard}
              disabled={saving === "guard"}
              onChange={(event) => void saveGuard(event.target.checked)}
            />
            <span className="wb-switch-track" aria-hidden="true">
              <span />
            </span>
            <span className="wb-switch-copy">
              危险命令护栏
              <small>对已知危险命令先询问，不会静默放行。切换即保存，不影响上面尚未保存的规则编辑。</small>
            </span>
          </label>
          {saved?.key === "guard" ? <p className="ns-saved">{saved.note}</p> : null}
        </SettingRow>
        ) : null}

        {saved ? (
          <p className="ns-receipt" role="status">
            <Icon name="check" size={14} />
            已保存并读回核对 · {TIMING[saved.timing].label}：{TIMING[saved.timing].note}
          </p>
        ) : null}
      </div>
    </div>
  );
}
