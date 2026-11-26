import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import type {
  CacheHistory,
  CachePolicySettings,
  CacheReceipt,
  CacheRetention,
  CacheSessionRow,
  CacheSummary,
  KeepAliveEvent,
  NativeHost,
} from "../native";
import {
  commitNumberField,
  CACHE_NOT_REPORTED as NOT_REPORTED,
  hitRateLabel,
  idleLabel,
  mergePolicy,
  shouldApplyPolicyEvent,
  speedLabel as formatSpeed,
  tokenLabel,
} from "../native";
import { ErrorNotice, Spinner, Toggle } from "./ui";
import { errorText } from "./workbench/pieces";
import "../styles/cache-settings.css";

const DEFAULTS: CachePolicySettings = {
  keepAlive: { enabled: false, minutes: 25 },
  idleKeepAlive: {
    enabled: false,
    defaultCount: 2,
    minQuotaPercent: 10,
  },
  compaction: { enabled: false, idleMinutes: 25, contextThreshold: 60 },
  recovery: { enabled: false, intervalSeconds: 60, entries: [] },
};

const RECOVERY_STATUS: Record<string, string> = {
  WATCHING: "已监听",
  WAITING: "等待额度恢复",
  PAUSED: "等待会话可操作",
  BLOCKED: "已被预算或权限阻止",
  ERROR: "检查失败",
  UNKNOWN: "结果未知",
};

/**
 * Receipt states separate "the official client accepted the request" from "the
 * official client finished it". Plan M2 section 5 requires the distinction.
 */
const RECEIPT_STATE: Record<string, { label: string; tone: "ok" | "pending" | "bad" | "muted" }> = {
  sent: { label: "已发送", tone: "pending" },
  hit: { label: "命中", tone: "ok" },
  miss: { label: "未命中", tone: "bad" },
  verified: { label: "历史命中", tone: "ok" },
  completed: { label: "官方已完成", tone: "ok" },
  acknowledged: { label: "官方已接收，等待完成", tone: "pending" },
  invalid: { label: "保活无效", tone: "bad" },
  failed: { label: "发送失败", tone: "bad" },
  cancelled: { label: "已取消", tone: "muted" },
  skipped: { label: "未发送", tone: "muted" },
  unknown: { label: "结果未知", tone: "muted" },
};

const KIND_LABEL: Record<string, string> = {
  keepalive: "后台保活",
  idle_keepalive: "缓存保活",
  compaction: "压缩",
  recovery: "额度恢复",
};

/**
 * The difference between the policy on screen and the one a caller built from
 * it. Only these fields are merged at write time, so a queued write never
 * resurrects a stale copy of the fields it did not touch.
 */
function policyPatch(base: CachePolicySettings, next: CachePolicySettings): Record<string, unknown> | null {
  const patch: Record<string, unknown> = {};
  if (next.keepAlive.enabled !== base.keepAlive.enabled) patch.keepAlive = { enabled: next.keepAlive.enabled };
  if (next.keepAlive.minutes !== base.keepAlive.minutes) {
    patch.keepAlive = { ...(patch.keepAlive as Record<string, unknown> | undefined), minutes: next.keepAlive.minutes };
  }
  const baseIdle = base.idleKeepAlive;
  const nextIdle = next.idleKeepAlive;
  const idle: Record<string, unknown> = {};
  if (nextIdle.enabled !== baseIdle.enabled) idle.enabled = nextIdle.enabled;
  if (nextIdle.defaultCount !== baseIdle.defaultCount) idle.defaultCount = nextIdle.defaultCount;
  if (nextIdle.minQuotaPercent !== baseIdle.minQuotaPercent) idle.minQuotaPercent = nextIdle.minQuotaPercent;
  if (Object.keys(idle).length > 0) patch.idleKeepAlive = idle;
  const baseCompaction = base.compaction;
  const nextCompaction = next.compaction;
  const compaction: Record<string, unknown> = {};
  if (nextCompaction.enabled !== baseCompaction.enabled) compaction.enabled = nextCompaction.enabled;
  if (nextCompaction.idleMinutes !== baseCompaction.idleMinutes) compaction.idleMinutes = nextCompaction.idleMinutes;
  if (nextCompaction.contextThreshold !== baseCompaction.contextThreshold) compaction.contextThreshold = nextCompaction.contextThreshold;
  // The core merges `compaction` key by key, so the patch above is enough; a
  // switch-only edit stays a one-field object.
  if (Object.keys(compaction).length > 0) patch.compaction = compaction;
  const baseRecovery = base.recovery;
  const nextRecovery = next.recovery;
  const recovery: Record<string, unknown> = {};
  if (nextRecovery.enabled !== baseRecovery.enabled) recovery.enabled = nextRecovery.enabled;
  if (nextRecovery.intervalSeconds !== baseRecovery.intervalSeconds) recovery.intervalSeconds = nextRecovery.intervalSeconds;
  if (Object.keys(recovery).length > 0) patch.recovery = recovery;
  return Object.keys(patch).length > 0 ? patch : null;
}

function NumberField({ label, value, min, max, unit, hideLabel = false, hint, validate, disabled = false, onCommit, onEditingChange }: {
  label: string;
  value: number;
  min: number;
  max: number;
  unit: string;
  hideLabel?: boolean;
  hint?: string;
  disabled?: boolean;
  /** Extra rule beyond the numeric range; returns a message when the value is
   * rejected. 0-versus-2..240 for the "other provider" TTL is one of these. */
  validate?: (value: number) => string | null;
  onCommit: (value: number | null) => void;
  onEditingChange?: (editing: boolean) => void;
}) {
  const id = useId();
  // The draft is local: a background automation event must never overwrite what the
  // user is typing. Only blur commits, and only when the value is valid.
  const [text, setText] = useState(String(value));
  const focused = useRef(false);
  useEffect(() => {
    if (!focused.current) setText(String(value));
  }, [value]);
  const parsed = commitNumberField(text, min, max);
  const ruleIssue = parsed !== null && validate ? validate(parsed) : null;
  const valid = parsed !== null && !ruleIssue;
  const issue = text.trim() === ""
    ? `不能留空，填写 ${min}–${max} 之间的整数；离开输入框会恢复上一个有效值。`
    : parsed === null
      ? `填写 ${min}–${max} 之间的整数。`
      : ruleIssue;
  return <span className="eks-field">
    <label htmlFor={id} className={hideLabel ? "eks-sr" : undefined}>{label}</label>
    <input
      id={id}
      className={`eks-num${valid ? "" : " is-invalid"}`}
      type="number"
      inputMode="numeric"
      min={min}
      max={max}
      aria-invalid={valid ? undefined : true}
      aria-describedby={issue ? `${id}-issue` : undefined}
      value={text}
      disabled={disabled}
      onFocus={() => { focused.current = true; onEditingChange?.(true); }}
      onChange={event => setText(event.target.value)}
      onBlur={() => {
        focused.current = false;
        onEditingChange?.(false);
        if (valid && parsed !== null) {
          // Committing an unchanged value would write the policy again and make
          // two idle tabs overwrite each other with stale closures.
          if (parsed !== value) onCommit(parsed);
          else setText(String(value));
        } else {
          setText(String(value));
        }
      }}
    />
    <span className="eks-unit">{unit}</span>
    {issue ? <span className="eks-issue" id={`${id}-issue`} role="status">{issue}</span> : hint ? <span className="eks-hint">{hint}</span> : null}
  </span>;
}

function Group({ title, hint, children }: { title: string; hint?: string; children: React.ReactNode }) {
  return <section className="eks-group-section">
    <h3 className="eks-group-title">{title}</h3>
    {hint ? <p className="eks-group-hint">{hint}</p> : null}
    <div className="eks-group">{children}</div>
  </section>;
}

export function CacheSettings({ host }: { host: NativeHost }) {
  const [summary, setSummary] = useState<CacheSummary | null>(null);
  const [sessions, setSessions] = useState<CacheSessionRow[]>([]);
  const [retention, setRetention] = useState<CacheRetention | null>(null);
  const [retentionModel, setRetentionModel] = useState("");
  const [receipts, setReceipts] = useState<CacheReceipt[]>([]);
  const [history, setHistory] = useState<CacheHistory | null>(null);
  const [range, setRange] = useState<"24h" | "7d" | "30d">("24h");
  const [policy, setPolicy] = useState<CachePolicySettings>(DEFAULTS);
  // Switches stay disabled until the persisted policy has been read: writing a
  // toggle against `DEFAULTS` would erase whatever is already on disk.
  const [policyReady, setPolicyReady] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [keepAliveAlert, setKeepAliveAlert] = useState("");
  const [saving, setSaving] = useState(false);
  const [loading, setLoading] = useState(true);
  // While a policy write is in flight, incoming automation events are ignored so the
  // user's switches are not bounced back to the previous value. `editing` does the
  // same for a number field that currently holds focus.
  const writing = useRef(false);
  const editing = useRef(false);

  const loadReadings = useCallback(async () => {
    const [nextSummary, nextSessions, nextRetention, nextReceipts] = await Promise.all([
      host.request<CacheSummary>("cache.summary"),
      host.request<CacheSessionRow[]>("cache.sessions", { limit: 20 }),
      host.request<CacheRetention>("cache.retention"),
      host.request<CacheReceipt[]>("cache.receipts", { limit: 20 }),
    ]);
    setSummary(nextSummary);
    setSessions(Array.isArray(nextSessions) ? nextSessions : []);
    setRetention(nextRetention);
    setReceipts(Array.isArray(nextReceipts) ? nextReceipts : []);
  }, [host]);

  useEffect(() => {
    let alive = true;
    (async () => {
      // The read-only statistics and the editable policy are independent reads.
      // One failing must not blank the other or let `DEFAULTS` overwrite a
      // policy that is already on disk.
      const [readings, policyResult] = await Promise.allSettled([
        loadReadings(),
        host.request<CachePolicySettings>("automation.get"),
      ]);
      if (!alive) return;
      if (readings.status === "rejected") setError(errorText(readings.reason));
      if (policyResult.status === "fulfilled") {
        setPolicy(mergePolicy(DEFAULTS, policyResult.value));
        setPolicyReady(true);
      } else {
        setError(previous => previous || `读取自动化策略失败：${errorText(policyResult.reason)}`);
        setPolicyReady(false);
      }
      setLoading(false);
    })();
    return () => { alive = false; };
  }, [host, loadReadings]);

  useEffect(() => {
    let alive = true;
    host.request<CacheHistory>("cache.history", { range })
      .then(value => { if (alive) setHistory(value); })
      .catch(() => { if (alive) setHistory(null); });
    return () => { alive = false; };
  }, [host, range]);

  // A model that stops appearing in the observation list must not leave the
  // select showing a stale (or blank) option.
  useEffect(() => {
    if (!retentionModel) return;
    if (!retention?.byModel?.some(item => item.model === retentionModel)) setRetentionModel("");
  }, [retention, retentionModel]);

  useEffect(() => host.subscribe<CachePolicySettings>("automation", event => {
    // A background event refreshes the persisted policy, but never while the user has
    // an edit or write in flight.
    if (!shouldApplyPolicyEvent({ writing: writing.current, editing: editing.current })) return;
    if (event && typeof event === "object" && "keepAlive" in event) {
      setPolicy(mergePolicy(DEFAULTS, event));
      // An event proves the core answered, so the switches can be enabled even if
      // the initial read failed.
      setPolicyReady(true);
    }
  }), [host]);

  useEffect(() => host.subscribe<KeepAliveEvent>("keepalive", event => {
    if (event?.type === "keepalive.alert") setKeepAliveAlert(event.message);
  }), [host]);

  useEffect(() => {
    let alive = true;
    const unsubscribe = host.subscribe<{ kind?: string }>("automation", event => {
      if (event?.kind !== "idle_keepalive") return;
      void host.request<CacheReceipt[]>("cache.receipts", { limit: 20 })
        .then(next => { if (alive && Array.isArray(next)) setReceipts(next); })
        .catch(() => {});
    });
    return () => { alive = false; unsubscribe(); };
  }, [host]);

  const policyRef = useRef(policy);
  policyRef.current = policy;
  const policyReadyRef = useRef(policyReady);
  policyReadyRef.current = policyReady;

  const enqueuePolicy = useMemo(() => createPolicyQueue({
    read: () => policyRef.current,
    write: payload => host.request<CachePolicySettings>("automation.set", payload),
    apply: next => setPolicy(next),
    fail: cause => {
      setError(errorText(cause));
      // Re-read the authoritative value so the UI does not keep showing a lie.
      void host.request<CachePolicySettings>("automation.get")
        .then(authoritative => setPolicy(mergePolicy(DEFAULTS, authoritative)))
        .catch(() => {});
    },
    busy: value => { writing.current = value; setSaving(value); },
  }), [host]);

  const savePolicy = useCallback(async (next: CachePolicySettings, successNote: string) => {
    setError("");
    setNotice("");
    await enqueuePolicy(next, policyReadyRef.current);
    if (policyReadyRef.current) setNotice(successNote);
  }, [enqueuePolicy]);

  if (loading && !summary && !error) {
    return <div className="eks-loading"><Spinner size={15} />正在读取缓存数据</div>;
  }

  const idleKeepAlive = policy.idleKeepAlive;
  const idleKeepAliveOff = !idleKeepAlive.enabled;
  const compactionOff = !policy.compaction.enabled;
  const recoveryOff = !policy.recovery.enabled;
  const modelRetention = retention?.byModel?.find(item => item.model === retentionModel);
  const retentionBuckets = modelRetention?.idleBuckets ?? retention?.idleBuckets;
  const historyPoints = history?.points ?? [];
  const peakTokens = historyPoints.reduce(
    (max, point) => Math.max(max, point.totals.inputOther + point.totals.inputCacheRead + point.totals.inputCacheCreation),
    0,
  );

  return <div className="eks">
    {error ? <ErrorNotice title="缓存数据暂时不可用" message={error} recovery="可以继续使用官方客户端；修正后重新打开本页。" /> : null}

    <Group title="概览" hint="数字来自本机会话记录，只读，不改动官方文件。">
      <div className="eks-metrics">
        <div className="eks-metric">
          <span className="eks-metric-label">今日命中率</span>
          <strong className="eks-metric-value">{hitRateLabel(summary?.today)}</strong>
          {summary?.today?.reported === NOT_REPORTED
            ? <small className="eks-metric-note">供应商未上报缓存数据</small>
            : <small className="eks-metric-note">读取 {tokenLabel(summary?.today?.inputCacheRead ?? 0)} · 创建 {tokenLabel(summary?.today?.inputCacheCreation ?? 0)} · 未命中 {tokenLabel(summary?.today?.inputOther ?? 0)}</small>}
        </div>
        <div className="eks-metric">
          <span className="eks-metric-label">近 1 小时命中率</span>
          <strong className="eks-metric-value">{hitRateLabel(summary?.hour)}</strong>
          <small className="eks-metric-note">{summary?.hour?.reported === NOT_REPORTED ? "供应商未上报缓存数据" : `${summary?.hour?.steps ?? 0} 步`}</small>
        </div>
        <div className="eks-metric">
          <span className="eks-metric-label">平均生成速度</span>
          <strong className="eks-metric-value">{formatSpeed(summary?.speed)}</strong>
          <small className="eks-metric-note">按服务端解码时长计算</small>
        </div>
        <div className="eks-metric">
          <span className="eks-metric-label">额度</span>
          <strong className="eks-metric-value">{summary?.quota?.state === "AVAILABLE" ? "可用" : summary?.quota?.state === "EXHAUSTED" ? "已用尽" : "未知"}</strong>
          <small className="eks-metric-note">{summary?.quota?.note ?? summary?.quota?.source ?? "官方未返回额度"}</small>
        </div>
      </div>
      {summary?.reportingNote ? <p className="eks-inline-note">{summary.reportingNote}</p> : null}
      {summary?.models?.length ? <div className="eks-models">
        {summary.models.slice(0, 6).map(model => <div key={model.model} className="eks-model-row">
          <span className="eks-model-name" title={model.model}>{model.model === "UNKNOWN" ? "未关联到模型" : model.model}</span>
          <span className="eks-model-rate">{hitRateLabel(model)}</span>
          <span className="eks-model-speed">{formatSpeed(model.speed)}</span>
        </div>)}
      </div> : null}
    </Group>

    <Group title="会话" hint="“距上次请求”只统计主代理自己的模型请求，子代理活动不会重置它。">
      {sessions.length === 0
        ? <p className="eks-empty">还没有可读取的会话缓存记录。使用官方客户端发起一次对话后返回本页。</p>
        : <div className="eks-table" role="table" aria-label="活跃会话缓存状态">
          <div className="eks-table-head" role="row">
            <span role="columnheader">会话</span>
            <span role="columnheader">命中率</span>
            <span role="columnheader">距上次请求</span>
            <span role="columnheader">状态</span>
          </div>
          {sessions.map(row => <div key={row.sessionId} className="eks-table-row" role="row">
            <span role="cell" className="eks-cell-main">
              <strong>{row.title || "未命名会话"}</strong>
              <small title={row.model}>{row.model === "UNKNOWN" ? "模型未关联" : row.model}</small>
            </span>
            <span role="cell" className="eks-cell-rate">{hitRateLabel(row.recent)}</span>
            <span role="cell" className="eks-cell-idle">{idleLabel(row.idleMinutes)}</span>
            <span role="cell" className="eks-cell-state">
              {row.blocked
                ? <span className="eks-chip is-warn">阻塞等待中</span>
                : row.openBackground > 0
                  ? <span className="eks-chip is-active">{row.openBackground} 个后台子代理</span>
                  : <span className="eks-chip">空闲</span>}
            </span>
          </div>)}
        </div>}
    </Group>

    <Group title="策略" hint="各项分别开启。会话运行或等待后台任务时，压缩与缓存保活暂停。">
      <div className="eks-row">
        <span className="eks-row-label">
          <strong>缓存保活</strong>
          <small>只在空闲会话发送。间隔取自该模型的缓存观测，样本不足时使用默认值。</small>
        </span>
        <Toggle
          label="启用缓存保活"
          checked={idleKeepAlive.enabled}
          disabled={!policyReady || saving}
          onChange={value => void savePolicy(
            mergePolicy(policy, { idleKeepAlive: { ...idleKeepAlive, enabled: value } }),
            value ? "缓存保活已开启。" : "缓存保活已关闭。",
          )}
        />
      </div>
      {!idleKeepAliveOff ? <>
        <div className="eks-row is-sub">
          <span className="eks-row-label">
            <strong>默认保活次数</strong>
            <small>每次真实请求后重新计数。可在会话信息中单独调整次数或关闭。</small>
          </span>
          <NumberField
            label="默认保活次数"
            hideLabel
            value={idleKeepAlive.defaultCount}
            min={1}
            max={20}
            unit="次"
            disabled={!policyReady || saving}
            onEditingChange={value => { editing.current = value; }}
            onCommit={value => {
              if (value !== null) void savePolicy(mergePolicy(policy, { idleKeepAlive: { ...idleKeepAlive, defaultCount: value } }), "默认保活次数已保存。");
            }}
          />
        </div>
        <div className="eks-row is-sub">
          <span className="eks-row-label">
            <strong>订阅额度停止阈值</strong>
            <small>剩余额度低于阈值时停止。设为 0 则关闭这一项检查。</small>
          </span>
          <NumberField
            label="订阅额度停止阈值"
            hideLabel
            value={idleKeepAlive.minQuotaPercent}
            min={0}
            max={100}
            unit="%"
            disabled={!policyReady || saving}
            onEditingChange={value => { editing.current = value; }}
            onCommit={value => {
              if (value !== null) void savePolicy(mergePolicy(policy, { idleKeepAlive: { ...idleKeepAlive, minQuotaPercent: value } }), "额度停止阈值已保存。");
            }}
          />
        </div>
        <p className="eks-row is-sub is-note">
          所有供应商都按剩余 Token 预算决定是否发送。单次实际用量可能超过估算，连续两次未命中会停止。保活回合仅在安全检查通过后自动撤回，仍可能触发官方通知及会话钩子。
        </p>
      </> : null}
      {keepAliveAlert ? <p className="eks-row is-sub is-note" role="status">{keepAliveAlert}</p> : null}
      <div className="eks-row is-sub is-stacked">
        <span className="eks-row-label">
          <strong>可参考的实测保留情况</strong>
          <small>{retention?.note ?? (error ? "暂时无法读取观测结果。" : "正在读取观测结果。")}</small>
        </span>
        {retention?.byModel?.length ? <label className="eks-row-label">
          <small>统计模型</small>
          <select value={modelRetention?.model ?? ""} onChange={event => setRetentionModel(event.target.value)}>
            <option value="">全部模型</option>
            {retention.byModel.map(item => <option key={item.model} value={item.model}>
              {item.model}{typeof item.samples === "number" && item.samples > 0 ? `（${item.samples} 次）` : ""}
            </option>)}
          </select>
        </label> : null}
        {retentionBuckets?.length ? <div className="eks-retention">
          {retentionBuckets.map(bucket => <div key={bucket.label} className="eks-retention-row">
            <span className="eks-retention-label">{bucket.label}</span>
            <span className="eks-retention-bar" aria-hidden="true">
              <span className="eks-retention-fill" style={{ width: bucket.hitRate === null ? "0%" : `${Math.max(2, Math.min(100, bucket.hitRate * 100))}%` }} />
            </span>
            <span className="eks-retention-value">
              {bucket.reported === NOT_REPORTED ? "未上报" : bucket.hitRate === null ? "无样本" : `${(bucket.hitRate * 100).toFixed(0)}%（${bucket.samples} 次）`}
            </span>
          </div>)}
        </div> : null}
        <p className="eks-inline-note">这些历史记录反映请求命中情况。估算保活的实际收益需要对照实验。</p>
      </div>

      <div className="eks-row">
        <span className="eks-row-label">
          <strong>空闲自动压缩</strong>
          <small>会话空闲达到设定时长且上下文超过阈值时请求官方压缩。若开启了空闲会话保活，会在保活结束后再进入压缩。</small>
        </span>
        <Toggle
          label="启用空闲自动压缩"
          checked={policy.compaction.enabled}
          disabled={!policyReady || saving}
          onChange={value => void savePolicy({ ...policy, compaction: { ...policy.compaction, enabled: value } }, value ? "空闲自动压缩已开启。" : "空闲自动压缩已关闭。")}
        />
      </div>
      {!compactionOff ? <div className="eks-row is-sub">
        <span className="eks-row-label"><strong>压缩条件</strong><small>空闲时长与上下文阈值同时满足才提交。</small></span>
        <span className="eks-controls">
          <NumberField label="空闲" value={policy.compaction.idleMinutes} min={5} max={1440} unit="分钟"
              disabled={!policyReady || saving}
            onEditingChange={value => { editing.current = value; }} onCommit={value => { if (value !== null) void savePolicy(mergePolicy(policy, { compaction: { ...policy.compaction, idleMinutes: value } }), "空闲阈值已保存。"); }} />
          <NumberField label="上下文超过" value={policy.compaction.contextThreshold} min={1} max={100} unit="%"
              disabled={!policyReady || saving}
            onEditingChange={value => { editing.current = value; }} onCommit={value => { if (value !== null) void savePolicy(mergePolicy(policy, { compaction: { ...policy.compaction, contextThreshold: value } }), "上下文阈值已保存。"); }} />
        </span>
      </div> : null}

      <div className="eks-row">
        <span className="eks-row-label">
          <strong>额度恢复自动继续</strong>
          <small>等待官方额度恢复后继续；自定义服务使用受控重试。遵守目标预算和当前权限。</small>
        </span>
        <Toggle
          label="启用额度恢复自动继续"
          checked={policy.recovery.enabled}
          disabled={!policyReady || saving}
          onChange={value => void savePolicy({ ...policy, recovery: { ...policy.recovery, enabled: value } }, value ? "额度恢复已开启。" : "额度恢复已关闭。")}
        />
      </div>
      {!recoveryOff ? <div className="eks-row is-sub">
        <span className="eks-row-label"><strong>检测间隔</strong><small>等待恢复的会话按此间隔重试。</small></span>
        <NumberField label="检测间隔" hideLabel value={policy.recovery.intervalSeconds} min={15} max={3600} unit="秒"
              disabled={!policyReady || saving}
          onEditingChange={value => { editing.current = value; }} onCommit={value => { if (value !== null) void savePolicy(mergePolicy(policy, { recovery: { ...policy.recovery, intervalSeconds: value } }), "检测间隔已保存。"); }} />
      </div> : null}
    </Group>

    {policy.recovery.entries.length ? <Group title="等待额度恢复的会话">
      {policy.recovery.entries.map(entry => <div key={entry.id} className="eks-row is-sub">
        <span className="eks-row-label"><strong>{entry.title}</strong><small>{entry.note}</small></span>
        <span className="eks-chip">{RECOVERY_STATUS[entry.status] ?? "未知状态"}</span>
      </div>)}
    </Group> : null}

    <Group title="实际用量" hint="按小时或按天聚合的 token 与速度记录。">
      <div className="eks-row">
        <span className="eks-row-label"><strong>统计区间</strong></span>
        <span className="eks-segmented" role="group" aria-label="历史区间">
          {(["24h", "7d", "30d"] as const).map(item => <button
            key={item}
            type="button"
            className={`eks-segment${range === item ? " is-on" : ""}`}
            aria-pressed={range === item}
            onClick={() => setRange(item)}
          >{item}</button>)}
        </span>
      </div>
      {history?.points?.length
        ? <div className="eks-spark" aria-label={`${history.range} 输入 token 分布`}>
          {history.points.map(point => {
            const total = point.totals.inputOther + point.totals.inputCacheRead + point.totals.inputCacheCreation;
            const empty = total === 0;
            return <span
              key={point.bucket}
              className={`eks-spark-bar${empty ? " is-empty" : ""}`}
              style={{ height: empty ? "2px" : `${Math.max(2, Math.round((total / peakTokens) * 100))}%` }}
              title={empty
                ? `${point.bucket}：无记录`
                : `${point.bucket}：命中 ${tokenLabel(point.totals.inputCacheRead)} · 未命中 ${tokenLabel(point.totals.inputOther)}`}
            />;
          })}
        </div>
        : <p className="eks-empty">这个区间还没有记录。</p>}
      {history?.totals ? <div className="eks-totals">
        <span>区间命中率 <strong>{hitRateLabel(history.totals)}</strong></span>
        <span>读取 <strong>{tokenLabel(history.totals.inputCacheRead)}</strong></span>
        <span>创建 <strong>{tokenLabel(history.totals.inputCacheCreation)}</strong></span>
        <span>未命中 <strong>{tokenLabel(history.totals.inputOther)}</strong></span>
        <span>输出 <strong>{tokenLabel(history.totals.output)}</strong></span>
      </div> : null}
    </Group>

    <Group title="最近执行记录" hint="保活按探测回合的缓存用量记录发送、命中或未命中。压缩单独记录官方接收与完成。">
      {receipts.length === 0
        ? <p className="eks-empty">还没有执行记录。策略开启后，每次保活、压缩和恢复都会记录在这里。</p>
        : <div className="eks-receipts">
          {receipts.map((receipt, index) => {
            const meta = RECEIPT_STATE[receipt.state] ?? { label: receipt.state || "未知", tone: "muted" as const };
            return <div key={`${receipt.kind}-${receipt.sessionId}-${receipt.at}-${index}`} className="eks-receipt">
              <span className={`eks-chip is-${meta.tone}`}>{meta.label}</span>
              <span className="eks-receipt-kind">{KIND_LABEL[receipt.kind] ?? receipt.kind}</span>
              <span className="eks-receipt-session">{receipt.sessionTitle || receipt.sessionId.slice(0, 12)}</span>
              <span className="eks-receipt-note">{receipt.note}</span>
            </div>;
          })}
        </div>}
    </Group>

    {notice ? <p className="eks-notice" role="status">{notice}</p> : null}
  </div>;
}

/**
 * Serializes policy writes. Two edits made in quick succession used to race: the
 * slower answer restored the older snapshot, and a write discarded while another
 * was in flight made a number edit followed by a toggle look like a no-op.
 *
 * Each call captures only the fields it changed against the policy at that
 * moment, then merges that patch onto the latest policy when its turn comes.
 */
export function createPolicyQueue(options: {
  read: () => CachePolicySettings;
  write: (payload: Record<string, unknown>) => Promise<CachePolicySettings>;
  apply: (next: CachePolicySettings) => void;
  fail: (cause: unknown) => void;
  busy?: (busy: boolean) => void;
}) {
  let tail: Promise<void> = Promise.resolve();
  let pending = 0;
  return async function enqueue(next: CachePolicySettings, enabled: boolean): Promise<void> {
    if (!enabled) return;
    const patch = policyPatch(options.read(), next);
    if (!patch) return;
    pending += 1;
    options.busy?.(true);
    const run = async () => {
      const payload = mergePolicy(options.read(), patch);
      try {
        options.apply(mergePolicy(DEFAULTS, await options.write(payload as unknown as Record<string, unknown>)));
      } catch (cause) {
        options.fail(cause);
      } finally {
        pending = Math.max(0, pending - 1);
        if (pending === 0) options.busy?.(false);
      }
    };
    const chained = tail.then(run, run);
    tail = chained.catch(() => {});
    return chained;
  };
}
