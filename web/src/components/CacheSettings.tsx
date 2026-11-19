import { useCallback, useEffect, useId, useRef, useState } from "react";
import type {
  CacheHistory,
  CachePolicySettings,
  CacheReceipt,
  CacheRetention,
  CacheSessionRow,
  CacheSummary,
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
  verified: { label: "后续请求命中", tone: "ok" },
  completed: { label: "官方已完成", tone: "ok" },
  acknowledged: { label: "官方已接收，等待完成", tone: "pending" },
  invalid: { label: "保活无效", tone: "bad" },
  failed: { label: "发送失败", tone: "bad" },
  cancelled: { label: "已取消", tone: "muted" },
  skipped: { label: "未发送", tone: "muted" },
  unknown: { label: "结果未知", tone: "muted" },
};

const KIND_LABEL: Record<string, string> = { keepalive: "保活", compaction: "压缩", recovery: "额度恢复" };

function NumberField({ label, value, min, max, unit, hideLabel = false, onCommit, onEditingChange }: {
  label: string;
  value: number;
  min: number;
  max: number;
  unit: string;
  hideLabel?: boolean;
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
  const valid = parsed !== null;
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
      value={text}
      onFocus={() => { focused.current = true; onEditingChange?.(true); }}
      onChange={event => setText(event.target.value)}
      onBlur={() => {
        focused.current = false;
        onEditingChange?.(false);
        if (parsed !== null) onCommit(parsed);
        else setText(String(value));
      }}
    />
    <span className="eks-unit">{unit}</span>
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
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
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
      try {
        await loadReadings();
        const nextPolicy = await host.request<CachePolicySettings>("automation.get");
        if (alive) setPolicy({ ...DEFAULTS, ...nextPolicy });
      } catch (cause) {
        if (alive) setError(errorText(cause));
      } finally {
        if (alive) setLoading(false);
      }
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

  useEffect(() => host.subscribe<CachePolicySettings>("automation", event => {
    // A background event refreshes the persisted policy, but never while the user has
    // an edit or write in flight.
    if (!shouldApplyPolicyEvent({ writing: writing.current, editing: editing.current })) return;
    if (event && typeof event === "object" && "keepAlive" in event) setPolicy(mergePolicy(DEFAULTS, event));
  }), [host]);

  const savePolicy = useCallback(async (next: CachePolicySettings, successNote: string) => {
    writing.current = true;
    setError("");
    setNotice("");
    try {
      const confirmed = await host.request<CachePolicySettings>("automation.set", next as unknown as Record<string, unknown>);
      setPolicy({ ...DEFAULTS, ...confirmed });
      setNotice(successNote);
    } catch (cause) {
      setError(errorText(cause));
      // Re-read the authoritative value so the UI does not keep showing a lie.
      try {
        setPolicy({ ...DEFAULTS, ...(await host.request<CachePolicySettings>("automation.get")) });
      } catch { /* the error notice already explains the state */ }
    } finally {
      writing.current = false;
    }
  }, [host]);

  if (loading && !summary && !error) {
    return <div className="eks-loading"><Spinner size={15} />正在读取缓存数据</div>;
  }

  const keepAliveOff = !policy.keepAlive.enabled;
  const compactionOff = !policy.compaction.enabled;
  const recoveryOff = !policy.recovery.enabled;
  const modelRetention = retention?.byModel?.find(item => item.model === retentionModel);
  const retentionBuckets = modelRetention?.idleBuckets ?? retention?.idleBuckets;

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

    <Group title="策略" hint="三项分别授权，默认关闭。同一会话不会同时保活和压缩：运行中只可能保活，空闲时只可能压缩。">
      <div className="eks-row">
        <span className="eks-row-label">
          <strong>后台等待保活</strong>
          <small>只在主会话等待后台子代理、且主代理自己超过设定时间没有发出模型请求时触发。每次保活消耗一次请求额度，并在主会话里留下一条消息。</small>
        </span>
        <Toggle
          label="启用后台等待保活"
          checked={policy.keepAlive.enabled}
          onChange={value => void savePolicy({ ...policy, keepAlive: { ...policy.keepAlive, enabled: value } }, value ? "后台等待保活已开启。" : "后台等待保活已关闭。")}
        />
      </div>
      {!keepAliveOff ? <div className="eks-row is-sub">
        <span className="eks-row-label"><strong>主代理静默阈值</strong><small>主代理距上一次模型请求达到这个时长才保活。5–60 分钟。</small></span>
        <NumberField label="主代理静默阈值" hideLabel value={policy.keepAlive.minutes} min={5} max={60} unit="分钟"
          onEditingChange={value => { editing.current = value; }} onCommit={value => { if (value !== null) void savePolicy(mergePolicy(policy, { keepAlive: { ...policy.keepAlive, minutes: value } }), "保活阈值已保存。"); }} />
      </div> : null}
      <div className="eks-row is-sub is-stacked">
        <span className="eks-row-label">
          <strong>可参考的实测保留情况</strong>
          <small>{retention?.note ?? (error ? "暂时无法读取观测结果。" : "正在读取观测结果。")}</small>
        </span>
        {retention?.byModel?.length ? <label className="eks-row-label">
          <small>统计模型</small>
          <select value={modelRetention?.model ?? ""} onChange={event => setRetentionModel(event.target.value)}>
            <option value="">全部模型</option>
            {retention.byModel.map(item => <option key={item.model} value={item.model}>{item.model}</option>)}
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
      {!keepAliveOff ? <p className="eks-row is-sub is-note">主代理处于阻塞式等待时跳过保活，执行记录会说明原因。</p> : null}

      <div className="eks-row">
        <span className="eks-row-label">
          <strong>空闲自动压缩</strong>
          <small>会话空闲达到设定时长且上下文超过阈值时请求官方压缩。这是空闲会话的替代方案，不会用来保活。</small>
        </span>
        <Toggle
          label="启用空闲自动压缩"
          checked={policy.compaction.enabled}
          onChange={value => void savePolicy({ ...policy, compaction: { ...policy.compaction, enabled: value } }, value ? "空闲自动压缩已开启。" : "空闲自动压缩已关闭。")}
        />
      </div>
      {!compactionOff ? <div className="eks-row is-sub">
        <span className="eks-row-label"><strong>压缩条件</strong><small>空闲时长与上下文阈值同时满足才提交。</small></span>
        <span className="eks-controls">
          <NumberField label="空闲" value={policy.compaction.idleMinutes} min={5} max={1440} unit="分钟"
            onEditingChange={value => { editing.current = value; }} onCommit={value => { if (value !== null) void savePolicy(mergePolicy(policy, { compaction: { ...policy.compaction, idleMinutes: value } }), "空闲阈值已保存。"); }} />
          <NumberField label="上下文超过" value={policy.compaction.contextThreshold} min={1} max={100} unit="%"
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
          onChange={value => void savePolicy({ ...policy, recovery: { ...policy.recovery, enabled: value } }, value ? "额度恢复已开启。" : "额度恢复已关闭。")}
        />
      </div>
      {!recoveryOff ? <div className="eks-row is-sub">
        <span className="eks-row-label"><strong>检测间隔</strong><small>等待恢复的会话按此间隔重试。</small></span>
        <NumberField label="检测间隔" hideLabel value={policy.recovery.intervalSeconds} min={15} max={3600} unit="秒"
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
            const peak = Math.max(...history.points.map(candidate => candidate.totals.inputOther + candidate.totals.inputCacheRead + candidate.totals.inputCacheCreation));
            const total = point.totals.inputOther + point.totals.inputCacheRead + point.totals.inputCacheCreation;
            const height = peak > 0 ? Math.max(2, Math.round((total / peak) * 100)) : 0;
            return <span key={point.bucket} className="eks-spark-bar" style={{ height: `${height}%` }}
              title={`${point.bucket}：命中 ${tokenLabel(point.totals.inputCacheRead)} · 未命中 ${tokenLabel(point.totals.inputOther)}`} />;
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

    <Group title="最近执行记录" hint="“官方已接收”不等于“官方已完成”；保活是否成功以下一步 input_cache_read 为准。">
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
