import { useEffect, useId, useRef, useState } from "react";
import type { KeepAliveEvent, NativeHost, SessionKeepAliveView } from "../native";
import { commitNumberField, tokenLabel } from "../native";
import { errorText } from "./workbench/pieces";
import "../styles/session-keepalive.css";

export function SessionKeepAliveMenu({ host, sessionId, onClose, onUpdated }: {
  host: NativeHost;
  sessionId: string;
  onClose: () => void;
  onUpdated?: (view: SessionKeepAliveView) => void;
}) {
  const countInputId = useId();
  const titleId = useId();
  const dialogRef = useRef<HTMLDivElement>(null);
  const [view, setView] = useState<SessionKeepAliveView | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [countDraft, setCountDraft] = useState("2");
  const [customizing, setCustomizing] = useState(false);

  useEffect(() => {
    let active = true;
    let receivedEvent = false;
    setLoading(true);
    setError("");
    setView(null);
    setCustomizing(false);
    const unsubscribe = host.subscribe<KeepAliveEvent>("keepalive", event => {
      if (event?.type !== "keepalive.session" || event.sessionId !== sessionId) return;
      receivedEvent = true;
      setView(event.view);
      setLoading(false);
    });
    void host.request<SessionKeepAliveView>("keepalive.session.get", { id: sessionId })
      .then(next => {
        if (!active || receivedEvent) return;
        setView(next);
        onUpdated?.(next);
      })
      .catch(cause => { if (active && !receivedEvent) setError(errorText(cause)); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; unsubscribe(); };
  }, [host, sessionId, onUpdated]);

  useEffect(() => {
    dialogRef.current?.focus();
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [onClose]);

  async function applyMode(mode: SessionKeepAliveView["mode"], targetCount?: number) {
    if (saving) return;
    setSaving(true);
    setError("");
    try {
      const next = await host.request<SessionKeepAliveView>("keepalive.session.set", { id: sessionId, mode, targetCount });
      onUpdated?.(next);
      onClose();
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setSaving(false);
    }
  }

  return <div ref={dialogRef} className="ekka-menu" role="dialog" aria-labelledby={titleId} tabIndex={-1}>
    <div className="ekka-header">
      <span id={titleId} className="ekka-title">缓存保活</span>
      {view ? <span className={`ekka-badge is-${view.statusTone}`} role="status">{view.statusLabel}</span> : null}
    </div>
    {loading ? <div className="ekka-loading" role="status">正在读取会话保活状态…</div> : null}
    {view ? <>
      <div className="ekka-options" role="group" aria-label="会话保活策略">
        <button type="button" className="ekka-item" aria-pressed={view.mode === "default"} disabled={saving} onClick={() => void applyMode("default")}>
          <span>跟随全局</span>
          <small>{view.globalEnabled ? `${view.globalCount} 次` : "全局已关闭"}</small>
        </button>
        <button type="button" className="ekka-item" aria-pressed={view.mode === "off"} disabled={saving} onClick={() => void applyMode("off")}>
          <span>关闭此会话</span>
        </button>
        <button type="button" className="ekka-item" aria-pressed={view.mode === "count"} aria-expanded={customizing} disabled={saving} onClick={() => {
          setCountDraft(String(view.mode === "count" ? view.targetCount : view.globalCount));
          setCustomizing(!customizing);
        }}>
          <span>指定次数</span>
          {view.mode === "count" ? <small>{view.targetCount} 次</small> : null}
        </button>
        {customizing ? <form className="ekka-inline-form" onSubmit={event => {
          event.preventDefault();
          const count = commitNumberField(countDraft, 1, 20);
          if (count === null) {
            setError("保活次数须为 1–20 次");
            return;
          }
          void applyMode("count", count);
        }}>
          <label htmlFor={countInputId}>保活次数</label>
          <input id={countInputId} inputMode="numeric" value={countDraft} disabled={saving} onChange={event => setCountDraft(event.target.value)} />
          <button type="submit" className="ekka-apply" disabled={saving}>保存</button>
        </form> : null}
      </div>
      <div className="ekka-footer">
        {view.stoppedDetail ? <p className="ekka-note">{view.stoppedDetail}</p> : null}
        {!view.globalEnabled && view.mode === "count" ? <p className="ekka-note">开启全局保活后，此会话的次数才会生效。</p> : null}
        {view.effectiveEnabled ? <dl className="ekka-metrics">
          <div className="ekka-meta-row"><dt>次数</dt><dd>{view.usedCount} / {view.effectiveLimit}</dd></div>
          {view.policy.intervalMinutes > 0 ? <div className="ekka-meta-row">
            <dt>间隔</dt><dd>{view.policy.intervalMinutes} 分钟 · {view.policy.source === "observed" ? "模型实测" : "样本不足，默认值"}</dd>
          </div> : null}
          <div className="ekka-meta-row"><dt>每次估算</dt><dd>{view.estimatedTokens > 0 ? `${tokenLabel(view.estimatedTokens)} Token` : "暂无用量记录"}</dd></div>
          <div className="ekka-meta-row"><dt>本轮 Token 预算</dt><dd>{tokenLabel(view.spentTokens)} / {tokenLabel(view.budgetTokens)}</dd></div>
        </dl> : null}
        {view.lastResult ? <p className="ekka-note"><strong>上次{view.lastResultLabel}</strong>{view.lastNote ? ` · ${view.lastNote}` : ""}</p> : null}
      </div>
    </> : null}
    {error ? <p className="ekka-error" role="alert">{error}</p> : null}
  </div>;
}
