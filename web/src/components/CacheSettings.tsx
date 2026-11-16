import { useEffect, useId, useState } from "react";
import type { NativeHost } from "../native";
import { ErrorNotice, Spinner, Toggle } from "./ui";
import { errorText } from "./workbench/pieces";

interface AutomationSettings {
  keepAlive: { enabled: boolean; minutes: number };
  compaction: { enabled: boolean; idleMinutes: number; contextThreshold: number };
  recovery: { enabled: boolean; intervalSeconds: number; entries: { id: string; title: string; status: string; nextAt?: string; note?: string }[] };
}

const DEFAULTS: AutomationSettings = {
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

function NumberField({ label, value, min, max, unit, hideLabel = false, onChange }: {
  label: string;
  value: number;
  min: number;
  max: number;
  unit: string;
  hideLabel?: boolean;
  onChange: (value: number) => void;
}) {
  const id = useId();
  return <span className="ekds-field-inline">
    <label htmlFor={id} className={hideLabel ? "ekds-sr" : undefined}>{label}</label>
    <input id={id} className="ekds-num" type="number" min={min} max={max} value={value} onChange={event => onChange(Number(event.target.value))} />
    <span>{unit}</span>
  </span>;
}

export function CacheSettings({ host }: { host: NativeHost }) {
  const [saved, setSaved] = useState<AutomationSettings | null>(null);
  const [draft, setDraft] = useState<AutomationSettings>(DEFAULTS);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  useEffect(() => {
    let alive = true;
    host.request<AutomationSettings>("automation.get").then(value => {
      if (!alive) return;
      setSaved(value); setDraft(value);
    }).catch(cause => { if (alive) setError(errorText(cause)); });
    return () => { alive = false; };
  }, [host]);
  useEffect(() => host.subscribe<AutomationSettings>("automation", event => {
    if (event && typeof event === "object" && "keepAlive" in event) { setSaved(event); setDraft(event); }
  }), [host]);

  async function save() {
    setBusy(true); setError(""); setNotice("");
    try {
      await host.request<AutomationSettings>("automation.set", draft as unknown as Record<string, unknown>);
      const confirmed = await host.request<AutomationSettings>("automation.get");
      setSaved(confirmed); setDraft(confirmed); setNotice("设置已保存并读回。启用的项目将在后台运行，结果可在 Monitor 中查看。");
    } catch (cause) { setError(errorText(cause)); }
    finally { setBusy(false); }
  }
  const dirty = saved !== null && JSON.stringify(saved) !== JSON.stringify(draft);
  if (!saved && !error) return <div className="ekds-loading"><Spinner size={15} />正在读取缓存与额度设置</div>;
  return <>
    {error ? <ErrorNotice title="缓存设置不可用" message={error} recovery="修正后重新保存；未保存的编辑仍保留。" /> : null}
    <div className="ekds-group">
      <div className="ekds-row">
        <span className="ekds-row-label"><strong>缓存保活</strong><small>长任务超过设定时间没有响应时，发送一条继续消息，可能消耗额度。等待审批或有新活动时不会发送。</small></span>
        <Toggle label="启用缓存保活" checked={draft.keepAlive.enabled} onChange={value => setDraft({ ...draft, keepAlive: { ...draft.keepAlive, enabled: value } })} />
      </div>
      <div className="ekds-row">
        <span className="ekds-row-label"><strong>未回传阈值</strong><small>只有明确开启后才调度。</small></span>
        <NumberField label="未回传阈值" hideLabel value={draft.keepAlive.minutes} min={5} max={120} unit="分钟" onChange={value => setDraft({ ...draft, keepAlive: { ...draft.keepAlive, minutes: value } })} />
      </div>
      <div className="ekds-row">
        <span className="ekds-row-label"><strong>空闲自动压缩</strong><small>会话结束并空闲后，在上下文占用超过阈值时请求压缩。界面会记录是否完成，结果不明确时不会自动重试。</small></span>
        <Toggle label="启用空闲自动压缩" checked={draft.compaction.enabled} onChange={value => setDraft({ ...draft, compaction: { ...draft.compaction, enabled: value } })} />
      </div>
      <div className="ekds-row">
        <span className="ekds-row-label"><strong>压缩条件</strong><small>空闲时间与上下文阈值同时满足才提交。</small></span>
        <span className="ekds-row-controls">
          <NumberField label="空闲" value={draft.compaction.idleMinutes} min={5} max={1440} unit="分钟" onChange={value => setDraft({ ...draft, compaction: { ...draft.compaction, idleMinutes: value } })} />
          <NumberField label="上下文超过" value={draft.compaction.contextThreshold} min={1} max={100} unit="%" onChange={value => setDraft({ ...draft, compaction: { ...draft.compaction, contextThreshold: value } })} />
        </span>
      </div>
      <div className="ekds-row">
        <span className="ekds-row-label"><strong>额度恢复自动继续</strong><small>等待官方额度恢复后继续；自定义服务使用受控重试。遵守目标预算和当前权限。</small></span>
        <Toggle label="启用额度恢复自动继续" checked={draft.recovery.enabled} onChange={value => setDraft({ ...draft, recovery: { ...draft.recovery, enabled: value } })} />
      </div>
      <div className="ekds-row">
        <span className="ekds-row-label"><strong>检测间隔</strong><small>等待恢复的会话按此间隔重试。</small></span>
        <NumberField label="检测间隔" hideLabel value={draft.recovery.intervalSeconds} min={15} max={3600} unit="秒" onChange={value => setDraft({ ...draft, recovery: { ...draft.recovery, intervalSeconds: value } })} />
      </div>
      {draft.recovery.entries.length ? <div className="ekds-list">{draft.recovery.entries.map(entry => <div key={entry.id}>
        <strong>{entry.title}</strong>
        <span>{RECOVERY_STATUS[entry.status] ?? "未知状态"}</span>
        <small>{entry.note}</small>
      </div>)}</div> : <p className="ekds-empty">当前没有等待额度恢复的会话。</p>}
    </div>
    <div className="ekds-savebar">
      <span>{dirty ? "有未保存更改" : notice || "设置已与持久化状态同步"}</span>
      <button type="button" className="wb-btn" disabled={busy || !dirty} onClick={() => { if (saved) setDraft(saved); setNotice("已撤销未保存修改。"); }}>撤销修改</button>
      <button type="button" className="wb-btn" disabled={busy} onClick={() => { setDraft(DEFAULTS); setNotice("默认值尚未保存，请检查后确认。"); }}>恢复默认</button>
      <button type="button" className="wb-btn is-primary" disabled={busy || !dirty} onClick={() => void save()}>{busy ? <Spinner size={12} /> : null}保存本类设置</button>
    </div>
  </>;
}
