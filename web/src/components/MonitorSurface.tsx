// UI adapted from bfjnbvf/kimi-code-monitor@1d529f0b (MIT); see scripts/notices.py.
import { useCallback, useEffect, useRef, useState } from "react";
import { Icon } from "./Icon";
import { errorText } from "./workbench/pieces";
import type { NativeHost } from "../native";
import "../styles/monitor.css";

interface MonitorField {
  state?: string;
  source?: string;
  note?: string;
  value?: unknown;
}
interface MonitorSummary {
  speed?: MonitorField;
  cache?: MonitorField;
  quota?: MonitorField;
}
interface QuotaWindow { usedRatio?: number; resetAt?: string }
const KIND_LABELS: Record<string, string> = { keepalive: "保活", compaction: "压缩" };
const STATE_LABELS: Record<string, string> = { completed: "已完成", failed: "失败", pending: "进行中", running: "进行中", skipped: "已跳过" };

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
}
function asText(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}
function summarize(raw: unknown): MonitorSummary {
  const record = asRecord(raw);
  const fields: MonitorSummary = {};
  for (const key of ["speed", "cache", "quota"] as const) {
    const field = asRecord(record?.[key]);
    if (field) fields[key] = { state: asText(field.state), source: asText(field.source), note: asText(field.note), value: field.value };
  }
  return fields;
}
function quotaWindows(field?: MonitorField): Record<string, QuotaWindow> {
  const usages = asRecord(asRecord(asRecord(field?.value)?.quota)?.usages);
  const windows: Record<string, QuotaWindow> = {};
  for (const [key, raw] of Object.entries(usages ?? {})) {
    const record = asRecord(raw);
    windows[key] = {
      usedRatio: typeof record?.usedRatio === "number" && Number.isFinite(record.usedRatio) ? record.usedRatio : undefined,
      resetAt: asText(record?.resetAt),
    };
  }
  return windows;
}
function hhmm(at: Date): string {
  return at.toLocaleTimeString("zh-CN", { hour: "2-digit", minute: "2-digit" });
}
function resetInfo(resetAt?: string): { short: string; long: string } | undefined {
  if (!resetAt) return undefined;
  const at = new Date(resetAt);
  if (Number.isNaN(at.getTime())) return undefined;
  const minutes = Math.ceil((at.getTime() - Date.now()) / 60_000);
  if (minutes <= 0) return { short: "待刷新", long: "已到重置点，等待官方用量刷新" };
  const hours = Math.floor(minutes / 60), days = Math.floor(hours / 24);
  const short = days ? `${days}d${hours % 24}h` : hours ? `${hours}h${minutes % 60}m` : `${minutes}m`;
  return { short, long: `${at.toLocaleString("zh-CN")} 重置` };
}
function percentOf(ratio?: number): number | undefined {
  return typeof ratio === "number" && Number.isFinite(ratio) ? Math.round(Math.min(Math.max(ratio, 0), 1) * 100) : undefined;
}
function QuotaModule({ label, quotaWindow }: { label: string; quotaWindow?: QuotaWindow }) {
  const percent = percentOf(quotaWindow?.usedRatio);
  const level = percent !== undefined && percent >= 90 ? "high" : percent !== undefined && percent >= 60 ? "mid" : "low";
  const reset = resetInfo(quotaWindow?.resetAt);
  const detail = [percent !== undefined ? `已用 ${percent}%` : "已用比例未知", reset?.long].filter(Boolean).join("，");
  return <div className="ksb-quota-group" title={detail}>
    <div className="ksb-quota-head"><span className="ksb-quota-label">{label}</span><span className="ksb-reset">{reset?.short}</span><span className={`ksb-quota-pct ${level}`}>{percent !== undefined ? `${percent}%` : "--"}</span></div>
    <div className="ksb-progress" role="img" aria-label={`${label} ${detail}`}><div className={`ksb-progress-fill ${level}${percent === undefined ? " is-unknown" : ""}`} style={{ width: percent === undefined ? "100%" : `${percent}%` }} /></div>
  </div>;
}
function UnknownStat({ label }: { label: string }) {
  return <div className="ksb-stat"><span className="ksb-stat-label">{label}</span><span className="ksb-stat-value is-unknown" aria-label={`${label}：未知`}>--</span></div>;
}
function DiagRow({ term, detail }: { term: string; detail: string }) {
  return <div className="ksb-diag-row"><dt>{term}</dt><dd>{detail}</dd></div>;
}

export function MonitorSurface({ host }: { host: NativeHost }) {
  const [data, setData] = useState<MonitorSummary | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [loadedAt, setLoadedAt] = useState<Date | null>(null);
  const [diagOpen, setDiagOpen] = useState(false);
  const loading = useRef(false);
  const load = useCallback(async () => {
    if (loading.current) return;
    loading.current = true;
    setBusy(true);
    try { setData(summarize(await host.request("monitor.summary"))); setLoadedAt(new Date()); setError(""); }
    catch (cause) { setError(errorText(cause)); }
    finally { loading.current = false; setBusy(false); }
  }, [host]);
  useEffect(() => { void load(); const timer = window.setInterval(() => void load(), 60_000); return () => window.clearInterval(timer); }, [load]);
  const quota = data?.quota;
  const quotaState = quota?.state?.toUpperCase() ?? "UNKNOWN";
  const windows = quotaState === "AVAILABLE" && !error ? quotaWindows(quota) : {};
  const known = Object.values(windows).some(item => percentOf(item.usedRatio) !== undefined);
  const exhausted = Object.values(windows).some(item => (item.usedRatio ?? 0) >= 1);
  const receipt = asRecord(data?.cache?.value);
  const receiptKind = asText(receipt?.kind), receiptState = asText(receipt?.state), receiptAt = asText(receipt?.at);
  const statusWord = error ? "读取失败" : !data ? "读取中" : quotaState === "ERROR" ? "额度未知" : exhausted ? "等待重置" : known ? "已同步" : "未知";
  const quotaNote = error ? "监控暂不可用，点击刷新重试。" : quotaState === "ERROR" ? "额度暂不可用，详见诊断。" : !known ? "尚未取得额度数据。" : "显示已用比例";
  return <section className="ekd-mon" aria-label="Kimi Code Monitor" aria-busy={busy || undefined}>
    <header className="ksb-header">
      <span className={`ksb-status-dot ${error || quotaState === "ERROR" ? "ksb-dot-off" : exhausted ? "ksb-dot-warn" : "ksb-dot-idle"}`} aria-hidden="true" />
      <span className="ksb-title">Monitor</span><span className="ksb-agent-status">{statusWord}</span>
      <button type="button" className="ksb-header-stat" onClick={() => void load()} disabled={busy} title={`上次刷新 ${loadedAt ? hhmm(loadedAt) : "尚未完成"}`} aria-label="刷新 Monitor 数据"><span className={`ksb-refresh${busy ? " spinning" : ""}`}><Icon name="refresh" size={11} /></span><span className="ksb-header-stat-value">{loadedAt ? hhmm(loadedAt) : "--:--"}</span></button>
    </header>
    <div className="ksb-region ksb-region-full">
      <div className="ksb-module"><QuotaModule label="5h" quotaWindow={windows.limit5h} /></div>
      <div className="ksb-module"><QuotaModule label="本周" quotaWindow={windows.limit7d} /></div>
      <div className="ksb-module ksb-span-2"><p className="ksb-footnote" role={error ? "alert" : "status"}>{quotaNote}</p></div>
      <div className="ksb-module ksb-span-2"><div className="ksb-stat-grid"><UnknownStat label="输入" /><UnknownStat label="输出" /><UnknownStat label="缓存命中" /><UnknownStat label="速度" /></div></div>
      <div className="ksb-module ksb-span-2">
        <button type="button" className={`ksb-diag-toggle${diagOpen ? " is-open" : ""}`} aria-expanded={diagOpen} aria-controls="ekd-mon-diagnostics" onClick={() => setDiagOpen(value => !value)}><span className="ksb-chevron"><Icon name="chevronDown" size={11} /></span><span>用量与缓存详情</span><small>{diagOpen ? "收起" : "展开"}</small></button>
      </div>
      {diagOpen ? <div className="ksb-module ksb-span-2" id="ekd-mon-diagnostics">
        <div className="ksb-cli-lock"><span>暂无历史用量</span><small>柱图需要本地 CLI 统计样本。</small></div>
        <div className="ksb-agents"><div className="ksb-agents-head"><span>代理</span><span className="ksb-agent-metric">输入</span><span className="ksb-agent-metric">输出</span><span className="ksb-agent-metric">命中</span></div><p className="ksb-agents-empty">暂无代理用量样本</p></div>
        <div className="ksb-block"><div className="ksb-section-head"><span className="ksb-quota-label">自动化回执</span><span className="ksb-tag">{receipt ? "已记录" : "暂无"}</span></div>
          {receipt ? <><div className="ksb-receipt"><span>{KIND_LABELS[asText(receipt.kind) ?? ""] ?? receiptKind ?? "未知任务"}</span><span>{STATE_LABELS[receiptState ?? ""] ?? receiptState ?? "未知状态"}</span>{receiptAt ? <time>{new Date(receiptAt).toLocaleString("zh-CN")}</time> : null}</div><p className="ksb-receipt-note">{asText(receipt.note)}</p></> : null}
          <p className="ksb-footnote">保活、压缩回执不代表缓存命中率。缓存有效期尚未确认。</p>
        </div>
        <dl className="ksb-diag-list">
          {error ? <DiagRow term="连接" detail={error} /> : null}
          <DiagRow term="额度" detail={`${quotaState} · ${quota?.source ?? "无来源"}`} />
          {quota?.note ? <DiagRow term="说明" detail={quota.note} /> : null}
          {Object.entries(windows).map(([key, value]) => <DiagRow key={key} term={key} detail={`${percentOf(value.usedRatio) ?? "未知"}${percentOf(value.usedRatio) === undefined ? "" : "% 已用"} · ${resetInfo(value.resetAt)?.long ?? "无重置时间"}`} />)}
          <DiagRow term="速度" detail={data?.speed?.note ?? "缺少 token 与耗时样本"} />
          <DiagRow term="缓存" detail={data?.cache?.note ?? "缺少缓存命中样本"} />
          <DiagRow term="提醒" detail="25 / 29 / 30 分钟是社区经验，不是官方缓存有效期。" />
        </dl>
        <footer className="ksb-foot"><span>bfjnbvf/kimi-code-monitor · MIT</span><span>1d529f0b</span></footer>
      </div> : null}
    </div>
  </section>;
}
