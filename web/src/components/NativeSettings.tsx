import { useEffect, useMemo, useState, type ReactNode } from "react";
import { getDemoHost } from "../native";
import { Icon } from "./Icon";
import { Badge, ErrorNotice, Spinner } from "./ui";
import "../styles/workbench.css";
import "../styles/native-settings.css";
import { errorText } from "./workbench/pieces";

type Timing = "now" | "derive" | "turn";

const TIMING: Record<Timing, { label: string; note: string }> = {
  now: { label: "即时", note: "保存后下一次操作就用新值，运行中的会话也适用。" },
  derive: { label: "下次派生", note: "已有的子代理保持旧值，新派生的规划/审查才用新值。" },
  turn: { label: "下一轮", note: "当前回合继续用旧上限，下一轮开始生效。" },
};

type Dic = Record<string, unknown>;

interface PoolEntry {
  id: string;
  key: string;
  desc: string;
}

interface PoolEditor {
  entries: PoolEntry[];
  defaultId: string;
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

function asDic(value: unknown): Dic | null {
  return value && typeof value === "object" && !Array.isArray(value) ? (value as Dic) : null;
}

function section(config: Dic | null, names: string[]): Dic | null {
  if (!config) return null;
  for (const name of names) {
    const found = asDic(config[name]);
    if (found) return found;
  }
  return null;
}

function field(source: Dic | null, names: string[]): unknown {
  if (!source) return undefined;
  for (const name of names) {
    if (source[name] !== undefined) return source[name];
  }
  return undefined;
}

function listText(value: unknown): string {
  if (Array.isArray(value)) return value.filter((item) => typeof item === "string").join(", ");
  return typeof value === "string" ? value : "";
}

function splitList(text: string): string[] {
  return text
    .split(/[,\n]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function omit(source: Dic, keys: string[]): Dic {
  const rest: Dic = { ...source };
  for (const key of keys) delete rest[key];
  return rest;
}

function readPool(raw: Dic | null): PoolEditor {
  const models = asDic(field(raw, ["models"]));
  const entries: PoolEntry[] = Object.entries(models ?? {}).map(([key, desc]) => ({
    id: nextId("pool"),
    key,
    desc: typeof desc === "string" ? desc : "",
  }));
  const single = field(raw, ["defaultModel", "default_model", "model"]);
  const defaultModel = typeof single === "string" ? single : "";
  if (entries.length === 0 && defaultModel) {
    entries.push({ id: nextId("pool"), key: defaultModel, desc: "" });
  }
  const chosen = entries.find((entry) => entry.key === defaultModel) ?? entries[0];
  return {
    entries,
    defaultId: chosen ? chosen.id : "",
    rest: omit(raw ?? {}, ["models", "defaultModel", "default_model", "model"]),
  };
}

function readRules(raw: unknown): RuleRow[] {
  if (!Array.isArray(raw)) return [];
  const rows: RuleRow[] = [];
  for (const item of raw) {
    const dic = asDic(item);
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

function TagsInput({
  value,
  onChange,
  placeholder,
  ariaLabel,
}: {
  value: string[];
  onChange: (next: string[]) => void;
  placeholder: string;
  ariaLabel: string;
}) {
  const [draft, setDraft] = useState("");

  function commit(text: string) {
    const added = splitList(text).filter((item) => !value.includes(item));
    if (added.length > 0) onChange([...value, ...added]);
    setDraft("");
  }

  return (
    <div className="ns-tags">
      {value.map((tag) => (
        <span className="ns-tag" key={tag}>
          {tag}
          <button type="button" title={`移除 ${tag}`} onClick={() => onChange(value.filter((item) => item !== tag))}>
            <Icon name="close" size={11} />
          </button>
        </span>
      ))}
      <input
        className="ns-tag-input"
        aria-label={ariaLabel}
        value={draft}
        placeholder={value.length === 0 ? placeholder : ""}
        onChange={(event) => {
          const text = event.target.value;
          if (/[,\n]$/.test(text)) commit(text);
          else setDraft(text);
        }}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.preventDefault();
            commit(draft);
          } else if (event.key === "Backspace" && draft === "" && value.length > 0) {
            onChange(value.slice(0, -1));
          }
        }}
        onBlur={() => {
          if (draft.trim()) commit(draft);
        }}
      />
    </div>
  );
}

function ModelSelect({
  aliases,
  value,
  onChange,
}: {
  aliases: string[];
  value: string;
  onChange: (next: string) => void;
}) {
  if (aliases.length === 0) {
    return (
      <input
        className="wb-input"
        aria-label="模型"
        value={value}
        placeholder="模型别名"
        onChange={(event) => onChange(event.target.value)}
      />
    );
  }
  const options = value && !aliases.includes(value) ? [...aliases, value] : aliases;
  return (
    <select className="wb-select" aria-label="模型" value={value} onChange={(event) => onChange(event.target.value)}>
      {value === "" ? <option value="">选择模型</option> : null}
      {options.map((alias) => (
        <option key={alias} value={alias}>
          {alias}
        </option>
      ))}
    </select>
  );
}

export function NativeSettings({ embedded = false }: { embedded?: boolean }) {
  const host = getDemoHost();
  const [config, setConfig] = useState<Dic | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const [toolsEnabled, setToolsEnabled] = useState<string[]>([]);
  const [toolsDisabled, setToolsDisabled] = useState<string[]>([]);
  const [pool, setPool] = useState<PoolEditor>({ entries: [], defaultId: "", rest: {} });
  const [timeoutMin, setTimeoutMin] = useState("");
  const [maxSteps, setMaxSteps] = useState("");
  const [rules, setRules] = useState<RuleRow[]>([]);
  const [guard, setGuard] = useState(false);

  const [saving, setSaving] = useState<string | null>(null);
  const [saved, setSaved] = useState<{ key: string; timing: Timing; note?: string } | null>(null);
  const [saveError, setSaveError] = useState<string | null>(null);

  useEffect(() => {
    if (!host) return;
    let alive = true;
    host
      .request<Dic>("config.get")
      .then((next) => {
        if (!alive) return;
        const cfg = asDic(next) ?? {};
        setConfig(cfg);
        const tools = section(cfg, ["tools"]);
        setToolsEnabled(splitList(listText(field(tools, ["enabled"]))));
        setToolsDisabled(splitList(listText(field(tools, ["disabled"]))));
        setPool(readPool(section(cfg, ["secondary_model", "secondaryModel"])));
        const timeoutMs = Number(field(section(cfg, ["subagent"]), ["timeout_ms", "timeoutMs"]));
        setTimeoutMin(Number.isFinite(timeoutMs) && timeoutMs > 0 ? String(Math.round(timeoutMs / 60000)) : "");
        const steps = field(section(cfg, ["loop_control", "loopControl"]), ["max_steps_per_turn", "maxStepsPerTurn"]);
        setMaxSteps(typeof steps === "number" || typeof steps === "string" ? String(steps) : "");
        const permission = section(cfg, ["permission"]);
        setRules(readRules(field(permission, ["rules"])));
        setGuard(field(permission, ["dangerous_command_guard", "dangerousCommandGuard"]) !== false);
      })
      .catch((cause) => {
        if (alive) setLoadError(errorText(cause));
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [host]);

  const aliases = useMemo(() => Object.keys(asDic(field(config, ["models"])) ?? {}), [config]);

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

  async function save(key: string, timing: Timing, patch: Dic | Dic[], note?: string) {
    if (!host) return;
    const steps = Array.isArray(patch) ? patch : [patch];
    setSaving(key);
    setSaveError(null);
    setSaved(null);
    let done = 0;
    try {
      let latest: Dic | null = null;
      for (const step of steps) {
        latest = asDic(await host.request<Dic>("config.save", { patch: step })) ?? latest;
        done += 1;
        if (latest) setConfig(latest);
      }
      setSaved({ key, timing, note });
    } catch (cause) {
      const message = errorText(cause);
      setSaveError(done === 0 ? message : `${message}（此前的写入已完成，请再保存一次补齐）`);
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
    if (poolKeys.length === 0) {
      void save("pool", "derive", { secondary_model: null }, "模型池已清空，子代理改为跟随调用方的模型。");
      return;
    }
    const chosen = pool.entries.find((entry) => entry.id === pool.defaultId);
    const defaultModel = chosen && models[chosen.key.trim()] !== undefined ? chosen.key.trim() : poolKeys[0]!;
    const full: Dic = { ...pool.rest, models, defaultModel };
    const current = section(config, ["secondary_model", "secondaryModel"]);
    const currentKeys = Object.keys(asDic(field(current, ["models"])) ?? {});
    const removed = currentKeys.filter((key) => models[key] === undefined);
    const legacy = field(current, ["model"]) !== undefined;
    if (removed.length === 0 && !legacy) {
      void save("pool", "derive", { secondary_model: full }, "下次派生生效，已在运行的子代理保持旧模型。");
      return;
    }
    void save(
      "pool",
      "derive",
      [{ secondary_model: null }, { secondary_model: full }],
      removed.length > 0
        ? `已整体替换模型池，移除的 ${removed.length} 个条目不再保留；下次派生生效。`
        : "已整体替换模型池，旧的单模型设置不再保留；下次派生生效。",
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
    void save(
      "permission",
      "now",
      { permission: { rules: payload, dangerous_command_guard: guard } },
      "即时生效，下一次权限判定就使用新规则。",
    );
  }

  if (!host) {
    return (
      <div className="wb ns">
        <header className="wb-head">
          <span className="wb-mark" aria-hidden="true" />
          <h1>原生设置</h1>
        </header>
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

  const timeoutValid = Number(timeoutMin) > 0;
  const stepsValid = Number(maxSteps) > 0 && Number.isInteger(Number(maxSteps));
  const scopeChoices = SCOPES.map((scope) => scope.value);

  return (
    <div className={`wb ns ${embedded ? "ns-embedded" : ""}`}>
      {embedded ? null : <header className="wb-head">
        <span className="wb-mark" aria-hidden="true" />
        <h1>原生设置</h1>
        <div className="wb-head-actions">
          <button type="button" className="wb-icon-btn" title="弹出独立窗口" onClick={() => void host.popout()}>
            <Icon name="external" size={15} />
          </button>
          <button type="button" className="wb-icon-btn" title="关闭" onClick={() => host.close()}>
            <Icon name="close" size={15} />
          </button>
        </div>
      </header>}

      <div className="wb-body ns-body">
        <p className="ns-lede">
          这五项管的是 Code 会话的工具、子代理的模型与时限、以及权限规则。保存后按每项标题旁的时机生效。
        </p>

        {loadError ? (
          <ErrorNotice title="配置没有读到" message={loadError} recovery="确认官方核心在运行后重新打开此页。" />
        ) : null}
        {saveError ? <ErrorNotice title="保存没有完成" message={saveError} /> : null}

        <SettingRow
          title="Code 工具开关"
          purpose="控制 Code 会话能用哪些工具。输入后回车或逗号确认，留空沿用官方默认。"
          timing="now"
          footer={
            <>
              <SaveButton
                busy={saving === "tools"}
                disabled={!config}
                onClick={() =>
                  void save(
                    "tools",
                    "now",
                    { tools: { enabled: toolsEnabled, disabled: toolsDisabled } },
                    "即时生效，下一次工具调用就按新列表判定。",
                  )
                }
              />
              {saved?.key === "tools" ? <span className="ns-saved">{saved.note}</span> : null}
            </>
          }
        >
          <div className="ns-field">
            <span className="wb-label">只启用这些工具</span>
            <TagsInput
              value={toolsEnabled}
              onChange={setToolsEnabled}
              ariaLabel="启用工具"
              placeholder="例如 read"
            />
          </div>
          <div className="ns-field">
            <span className="wb-label">停用这些工具</span>
            <TagsInput
              value={toolsDisabled}
              onChange={setToolsDisabled}
              ariaLabel="停用工具"
              placeholder="例如 bash"
            />
          </div>
        </SettingRow>

        <SettingRow
          title="子代理模型池"
          purpose="子代理可以从这些模型里选。用途说明写清它适合什么工作；选中的默认模型是所有子代理的落点。清空后子代理跟随调用方的模型。"
          timing="derive"
          footer={
            <>
              <SaveButton busy={saving === "pool"} disabled={!config || Boolean(poolIssue)} onClick={savePool} />
              {saved?.key === "pool" ? <span className="ns-saved">{saved.note}</span> : null}
            </>
          }
        >
          <div className="ns-list-head">
            <span className="wb-label">池内模型</span>
            <button
              type="button"
              className="wb-btn is-quiet is-compact"
              onClick={() =>
                setPool((current) => ({
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
            <p className="ns-empty-line">还没有池内模型，保存后子代理跟随调用方的模型。</p>
          ) : (
            <>
              <div className="ns-pool-headrow" aria-hidden="true">
                <span className="ns-head-cell">默认</span>
                <span className="ns-head-cell">模型</span>
                <span className="ns-head-cell">用途说明</span>
                <span />
              </div>
              <div className="ns-pool-list">
                {pool.entries.map((entry) => (
                  <div className="ns-pool-row" key={entry.id}>
                    <label className="ns-default">
                      <input
                        type="radio"
                        name="ns-pool-default"
                        checked={pool.defaultId === entry.id}
                        onChange={() => setPool((current) => ({ ...current, defaultId: entry.id }))}
                      />
                      默认
                    </label>
                    <div className="ns-pool-model">
                      <ModelSelect
                        aliases={aliases}
                        value={entry.key}
                        onChange={(key) =>
                          setPool((current) => ({
                            ...current,
                            entries: current.entries.map((item) => (item.id === entry.id ? { ...item, key } : item)),
                          }))
                        }
                      />
                    </div>
                    <div className="ns-pool-desc">
                      <input
                        className="wb-input"
                        aria-label={`${entry.key || "模型"} 的用途说明`}
                        value={entry.desc}
                        placeholder="例如 日常实现"
                        onChange={(event) =>
                          setPool((current) => ({
                            ...current,
                            entries: current.entries.map((item) =>
                              item.id === entry.id ? { ...item, desc: event.target.value } : item,
                            ),
                          }))
                        }
                      />
                    </div>
                    <button
                      type="button"
                      className="wb-icon-btn ns-row-remove"
                      title="移除这个模型"
                      onClick={() =>
                        setPool((current) => {
                          const entries = current.entries.filter((item) => item.id !== entry.id);
                          return {
                            ...current,
                            entries,
                            defaultId: current.defaultId === entry.id ? (entries[0]?.id ?? "") : current.defaultId,
                          };
                        })
                      }
                    >
                      <Icon name="trash" size={13} />
                    </button>
                  </div>
                ))}
              </div>
            </>
          )}

          {poolIssue ? <p className="ns-issue">{poolIssue}</p> : null}
          {pool.entries.length > 0 ? (
            <p className="wb-hint">默认模型必须来自池内。删除条目后保存会整体替换模型池。</p>
          ) : null}
        </SettingRow>

        <SettingRow
          title="子代理时限"
          purpose="单个子代理最长运行多久，按分钟填写。超时后该子代理会被终止。"
          timing="derive"
          footer={
            <>
              <SaveButton
                busy={saving === "timeout"}
                disabled={!timeoutValid}
                onClick={() =>
                  void save(
                    "timeout",
                    "derive",
                    { subagent: { timeout_ms: Math.round(Number(timeoutMin) * 60000) } },
                    "下次派生生效，已有的子代理继续用旧时限。",
                  )
                }
              />
              {saved?.key === "timeout" ? <span className="ns-saved">{saved.note}</span> : null}
            </>
          }
        >
          <label className="ns-field">
            <span className="wb-label">分钟</span>
            <input
              className="wb-input num"
              type="number"
              min={1}
              step={1}
              value={timeoutMin}
              onChange={(event) => setTimeoutMin(event.target.value)}
              placeholder="120"
            />
          </label>
          <p className="wb-hint">默认 120 分钟，对应 7200000 毫秒。</p>
        </SettingRow>

        <SettingRow
          title="单轮步数上限"
          purpose="一个回合里 agent 最多走多少步，用来限制长任务发散。留空则用官方默认。"
          timing="turn"
          footer={
            <>
              <SaveButton
                busy={saving === "steps"}
                disabled={!stepsValid}
                onClick={() =>
                  void save(
                    "steps",
                    "turn",
                    { loop_control: { max_steps_per_turn: Math.trunc(Number(maxSteps)) } },
                    "下一轮生效，当前回合继续用旧上限。",
                  )
                }
              />
              {saved?.key === "steps" ? <span className="ns-saved">{saved.note}</span> : null}
            </>
          }
        >
          <label className="ns-field">
            <span className="wb-label">步数</span>
            <input
              className="wb-input num"
              type="number"
              min={1}
              step={1}
              value={maxSteps}
              onChange={(event) => setMaxSteps(event.target.value)}
              placeholder="未设置"
            />
          </label>
        </SettingRow>

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
                setRules((current) => [
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
                            setRules((current) =>
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
                            setRules((current) =>
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
                            setRules((current) =>
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
                            setRules((current) =>
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
                        onClick={() => setRules((current) => current.filter((item) => item.id !== rule.id))}
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
            <input type="checkbox" checked={guard} onChange={(event) => setGuard(event.target.checked)} />
            <span className="wb-switch-track" aria-hidden="true">
              <span />
            </span>
            <span className="wb-switch-copy">
              危险命令护栏
              <small>对已知危险命令先询问，不会静默放行。</small>
            </span>
          </label>
        </SettingRow>

        {saved ? (
          <p className="ns-receipt" role="status">
            <Icon name="check" size={14} />
            已保存 · {TIMING[saved.timing].label}：{TIMING[saved.timing].note}
          </p>
        ) : null}
      </div>
    </div>
  );
}
