import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import type { NativeHost, SessionKeepAliveView } from "../native";
import { errorText } from "./workbench/pieces";
import "../styles/session-keepalive.css";

function defaultTimeInputValue(untilAt?: string): string {
  const target = untilAt ? new Date(untilAt) : new Date(Date.now() + 60 * 60 * 1000);
  if (Number.isNaN(target.getTime())) {
    const fallback = new Date(Date.now() + 60 * 60 * 1000);
    return `${String(fallback.getHours()).padStart(2, "0")}:${String(fallback.getMinutes()).padStart(2, "0")}`;
  }
  return `${String(target.getHours()).padStart(2, "0")}:${String(target.getMinutes()).padStart(2, "0")}`;
}

function resolveUntilISO(hhmm: string): string | null {
  const match = /^(\d{1,2}):(\d{2})$/.exec(hhmm.trim());
  if (!match) return null;
  const hours = Number(match[1]);
  const minutes = Number(match[2]);
  if (!Number.isInteger(hours) || !Number.isInteger(minutes) || hours < 0 || hours > 23 || minutes < 0 || minutes > 59) {
    return null;
  }
  const now = new Date();
  const target = new Date(now.getFullYear(), now.getMonth(), now.getDate(), hours, minutes, 0, 0);
  if (target.getTime() <= now.getTime() + 30_000) {
    target.setDate(target.getDate() + 1);
  }
  return target.toISOString();
}

export function SessionKeepAliveMenu({
  host,
  sessionId,
  onClose,
  onUpdated,
}: {
  host: NativeHost;
  sessionId: string;
  onClose: () => void;
  onUpdated?: (view: SessionKeepAliveView) => void;
}) {
  const countInputId = useId();
  const timeInputId = useId();
  const [view, setView] = useState<SessionKeepAliveView | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [countDraft, setCountDraft] = useState("2");
  const [timeDraft, setTimeDraft] = useState(() => defaultTimeInputValue());
  const [expanded, setExpanded] = useState<"count" | "until" | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);

  const load = useCallback(async () => {
    if (!sessionId) return;
    setError("");
    try {
      const next = await host.request<SessionKeepAliveView>("keepalive.session.get", { id: sessionId });
      setView(next);
      setCountDraft(String(next.targetCount || next.globalCount || 2));
      setTimeDraft(defaultTimeInputValue(next.untilAt));
      if (next.mode === "count") setExpanded("count");
      else if (next.mode === "until") setExpanded("until");
      onUpdated?.(next);
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setLoading(false);
    }
  }, [host, sessionId, onUpdated]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [onClose]);

  const applyMode = useCallback(async (payload: Record<string, unknown>, closeAfter = true) => {
    if (!sessionId || saving) return;
    setSaving(true);
    setError("");
    try {
      const next = await host.request<SessionKeepAliveView>("keepalive.session.set", { id: sessionId, ...payload });
      setView(next);
      onUpdated?.(next);
      if (closeAfter) onClose();
    } catch (cause) {
      setError(errorText(cause));
    } finally {
      setSaving(false);
    }
  }, [host, sessionId, saving, onUpdated, onClose]);

  const defaultSubtitle = useMemo(() => {
    if (!view) return "";
    return view.globalEnabled ? `全局默认 ${view.globalCount} 次` : "全局默认关闭";
  }, [view]);

  const applyCustomCount = () => {
    const parsed = Number.parseInt(countDraft.trim(), 10);
    if (!Number.isInteger(parsed) || parsed < 1 || parsed > 20) {
      setError("保活次数须为 1–20 次");
      return;
    }
    void applyMode({ mode: "count", targetCount: parsed });
  };

  const applyRelativeMinutes = (minutes: number) => {
    const until = new Date(Date.now() + minutes * 60 * 1000).toISOString();
    void applyMode({ mode: "until", untilAt: until });
  };

  const applyCustomTime = () => {
    const iso = resolveUntilISO(timeDraft);
    if (!iso) {
      setError("请输入有效时刻（HH:MM）");
      return;
    }
    void applyMode({ mode: "until", untilAt: iso });
  };

  return (
    <div ref={menuRef} className="ekka-menu" role="menu" aria-label="缓存保活设置">
      <div className="ekka-header">
        <span className="ekka-title">缓存保活</span>
        {view ? <span className={`ekka-badge is-${view.statusTone}`}>{view.statusLabel}</span> : null}
      </div>

      {loading ? (
        <div className="ekka-loading">正在读取会话保活状态…</div>
      ) : (
        <>
          <div className="ekka-options">
            <button
              type="button"
              role="menuitemradio"
              aria-checked={view?.mode === "default"}
              className={`ekka-item${view?.mode === "default" ? " is-active" : ""}`}
              disabled={saving}
              onClick={() => void applyMode({ mode: "default" })}
            >
              <span className="ekka-item-main">
                <span>跟随默认</span>
                <small>{defaultSubtitle}</small>
              </span>
              {view?.mode === "default" ? <span className="ekka-check" aria-hidden="true">✓</span> : null}
            </button>

            <button
              type="button"
              role="menuitemradio"
              aria-checked={view?.mode === "off"}
              className={`ekka-item${view?.mode === "off" ? " is-active" : ""}`}
              disabled={saving}
              onClick={() => void applyMode({ mode: "off" })}
            >
              <span className="ekka-item-main">
                <span>关闭</span>
                <small>仅对当前会话关闭空闲保活</small>
              </span>
              {view?.mode === "off" ? <span className="ekka-check" aria-hidden="true">✓</span> : null}
            </button>

            <button
              type="button"
              role="menuitemradio"
              aria-checked={view?.mode === "count"}
              className={`ekka-item${view?.mode === "count" ? " is-active" : ""}`}
              disabled={saving}
              onClick={() => setExpanded(prev => (prev === "count" ? null : "count"))}
            >
              <span className="ekka-item-main">
                <span>保活指定次数</span>
                <small>{view?.mode === "count" ? `当前：${view.targetCount} 次` : "离开后自动保活 N 次"}</small>
              </span>
              {view?.mode === "count" ? <span className="ekka-check" aria-hidden="true">✓</span> : null}
            </button>

            {expanded === "count" ? (
              <div className="ekka-sub">
                <div className="ekka-presets">
                  {[1, 2, 3, 5].map(n => (
                    <button
                      key={n}
                      type="button"
                      className={`ekka-preset${countDraft === String(n) ? " is-selected" : ""}`}
                      disabled={saving}
                      onClick={() => {
                        setCountDraft(String(n));
                        void applyMode({ mode: "count", targetCount: n });
                      }}
                    >
                      {n} 次
                    </button>
                  ))}
                </div>
                <div className="ekka-inline-form">
                  <label htmlFor={countInputId} className="ekka-sr">自定义次数</label>
                  <input
                    id={countInputId}
                    type="number"
                    min={1}
                    max={20}
                    value={countDraft}
                    onChange={event => setCountDraft(event.target.value)}
                    onKeyDown={event => { if (event.key === "Enter") applyCustomCount(); }}
                  />
                  <span className="ekka-unit">次</span>
                  <button type="button" className="ekka-apply" disabled={saving} onClick={applyCustomCount}>确定</button>
                </div>
              </div>
            ) : null}

            <button
              type="button"
              role="menuitemradio"
              aria-checked={view?.mode === "until"}
              className={`ekka-item${view?.mode === "until" ? " is-active" : ""}`}
              disabled={saving}
              onClick={() => setExpanded(prev => (prev === "until" ? null : "until"))}
            >
              <span className="ekka-item-main">
                <span>保活到某个时刻</span>
                <small>
                  {view?.mode === "until" && view.untilAt
                    ? `当前截止：${new Date(view.untilAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`
                    : "在指定时刻前持续保活"}
                </small>
              </span>
              {view?.mode === "until" ? <span className="ekka-check" aria-hidden="true">✓</span> : null}
            </button>

            {expanded === "until" ? (
              <div className="ekka-sub">
                <div className="ekka-presets">
                  <button type="button" className="ekka-preset" disabled={saving} onClick={() => applyRelativeMinutes(30)}>+30 分钟</button>
                  <button type="button" className="ekka-preset" disabled={saving} onClick={() => applyRelativeMinutes(60)}>+1 小时</button>
                  <button type="button" className="ekka-preset" disabled={saving} onClick={() => applyRelativeMinutes(120)}>+2 小时</button>
                </div>
                <div className="ekka-inline-form">
                  <label htmlFor={timeInputId} className="ekka-sr">截止时刻</label>
                  <input
                    id={timeInputId}
                    type="time"
                    value={timeDraft}
                    onChange={event => setTimeDraft(event.target.value)}
                    onKeyDown={event => { if (event.key === "Enter") applyCustomTime(); }}
                  />
                  <button type="button" className="ekka-apply" disabled={saving} onClick={applyCustomTime}>确定</button>
                </div>
              </div>
            ) : null}
          </div>

          {error ? <p className="ekka-error" role="alert">{error}</p> : null}

          {view ? (
            <div className="ekka-footer">
              <div className="ekka-meta-row">
                <span>已保活次数</span>
                <strong>
                  {view.usedCount} 次
                  {view.remainingCount !== null && view.remainingCount !== undefined ? `（还剩 ${view.remainingCount} 次）` : ""}
                </strong>
              </div>
              <div className="ekka-meta-row">
                <span>上次结果</span>
                <strong>{view.lastResultLabel || "尚未触发"}</strong>
              </div>
              <div className="ekka-meta-row">
                <span>保活间隔</span>
                <strong>
                  {view.policy.ttlMinutes > 0
                    ? `${view.policy.providerLabel} · 每 ${view.policy.intervalMinutes} 分钟`
                    : `${view.policy.providerLabel} · 时长未知`}
                </strong>
              </div>
              {view.stoppedDetail ? <p className="ekka-note is-warn">{view.stoppedDetail}</p> : null}
              {!view.stoppedDetail && view.lastNote ? <p className="ekka-note">{view.lastNote}</p> : null}
              <p className="ekka-cost">预计每次消耗：{view.estimatedCost}</p>
            </div>
          ) : null}
        </>
      )}
    </div>
  );
}
