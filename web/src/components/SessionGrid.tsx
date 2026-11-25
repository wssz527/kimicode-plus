import { useCallback, useEffect, useRef, useState } from "react";
import { followNativeSession, type NativeHost, type NativeSession, type RecoveryState } from "../native";
import { ErrorNotice, Spinner } from "./ui";
import { SessionInputProvider, SessionPane } from "./workbench/SessionPanes";
import { useNativeBootstrap } from "./Surfaces";

const PANE_WIDTHS_KEY = ["ekd", "sessions", "pane-widths", "v1"].join(".");

export function readPaneWeights(json: string | null, count: number): number[] {
  try {
    const weights: unknown = JSON.parse(json ?? "{}")[count];
    if (Array.isArray(weights) && weights.length === count && weights.every(weight => typeof weight === "number" && Number.isFinite(weight) && weight > 0 && weight <= 3)) return weights;
  } catch {
    // A removed or invalid preference uses equal widths.
  }
  return Array.from({ length: count }, () => 1);
}

export function removePaneWeight(weights: number[], index: number): number[] {
  const remaining = weights.filter((_, current) => current !== index);
  const mean = remaining.reduce((total, weight) => total + weight, 0) / remaining.length;
  return remaining.map(weight => weight / mean);
}

function resizePair(weights: number[], index: number, delta: number) {
  const next = [...weights];
  const pair = weights[index]! + weights[index + 1]!;
  const min = Math.min(0.55, pair / 2);
  next[index] = Math.min(pair - min, Math.max(min, weights[index]! + delta));
  next[index + 1] = pair - next[index]!;
  return next;
}

function initialWeights(count: number) {
  try { return readPaneWeights(localStorage.getItem(PANE_WIDTHS_KEY), count); }
  catch { return readPaneWeights(null, count); }
}

type SessionGridProps = { host: NativeHost; initialExtraIds: string[]; onNativeRestore: () => void };
export function SessionGrid(props: SessionGridProps) {
  return <SessionInputProvider><SessionGridBody {...props} /></SessionInputProvider>;
}
function SessionGridBody({ host, initialExtraIds, onNativeRestore }: SessionGridProps) {
  const { boot, error, reload } = useNativeBootstrap(host);
  const [{ ids, weights }, setLayout] = useState(() => {
    const ids = [...new Set([host.activeSession(), ...initialExtraIds.slice(0, 2)].filter(Boolean))];
    return { ids, weights: initialWeights(ids.length) };
  });
  const setIds = useCallback((update: (current: string[]) => string[]) => setLayout(current => {
    const ids = update(current.ids);
    if (ids === current.ids) return current;
    const weights = current.ids.length === 0 ? initialWeights(ids.length) : ids.map((_, index) => current.weights[index] ?? 1);
    return { ids, weights };
  }), []);
  const [resizing, setResizing] = useState(false);
  const [sessions, setSessions] = useState(boot?.sessions ?? []);
  const [recovery, setRecovery] = useState(boot?.recovery ?? null);
  const [actionError, setActionError] = useState("");
  const [adding, setAdding] = useState(false);
  const addingRef = useRef(false);
  const idsRef = useRef(ids);
  idsRef.current = ids;
  const gridRef = useRef<HTMLDivElement>(null);
  const listRequest = useRef(0);
  const pendingSessions = useRef(new Map<string, NativeSession>());

  useEffect(() => {
    if (!boot) return;
    setSessions(boot.sessions);
    setRecovery(boot.recovery);
    setIds(current => current.length ? current : boot.sessions[0] ? [boot.sessions[0].id] : []);
  }, [boot, setIds]);
  const refreshSessions = useCallback(async () => {
    const request = ++listRequest.current;
    try {
      const { items } = await host.request<{ items: NativeSession[] }>("sessions.list");
      if (request !== listRequest.current) return;
      for (const item of items) pendingSessions.current.delete(item.id);
      setSessions([...pendingSessions.current.values()].reverse().concat(items));
    } catch (cause) {
      if (request === listRequest.current) setActionError(cause instanceof Error ? cause.message : String(cause));
    }
  }, [host]);
  useEffect(() => host.onActiveSession?.(id => {
    if (!id) return;
    setIds(current => followNativeSession(current, id));
    void refreshSessions();
  }), [host, refreshSessions, setIds]);
  useEffect(() => host.subscribe<RecoveryState>("recovery", setRecovery), [host]);
  useEffect(() => {
    if (resizing || weights.length < 2) return;
    try {
      const encoded = localStorage.getItem(PANE_WIDTHS_KEY);
      const saved = { 2: readPaneWeights(encoded, 2), 3: readPaneWeights(encoded, 3), [weights.length]: weights };
      localStorage.setItem(PANE_WIDTHS_KEY, JSON.stringify(saved));
    } catch {
      // Opaque origins and disabled storage can still resize the current grid.
    }
  }, [resizing, weights]);

  const add = useCallback(async () => {
    if (addingRef.current || idsRef.current.length >= 3) return;
    addingRef.current = true;
    setAdding(true);
    setActionError("");
    try {
      const id = await host.pickSession(idsRef.current);
      if (id) {
        setIds(current => current.includes(id) || current.length >= 3 ? current : [...current, id]);
        void refreshSessions();
      }
    } catch (cause) {
      if (!(cause instanceof Error && cause.message === "已取消选择")) setActionError(cause instanceof Error ? cause.message : String(cause));
    } finally { addingRef.current = false; setAdding(false); }
  }, [host, refreshSessions, setIds]);
  useEffect(() => host.onAddSession?.(() => { void add(); }), [host, add]);

  function beginResize(event: React.PointerEvent<HTMLDivElement>, index: number) {
    event.preventDefault();
    const target = event.currentTarget;
    target.setPointerCapture(event.pointerId);
    const startX = event.clientX;
    const width = gridRef.current?.scrollWidth || 1;
    setResizing(true);
    const move = (next: globalThis.PointerEvent) => {
      const delta = (next.clientX - startX) / width * ids.length;
      setLayout(current => current.ids.length === ids.length ? { ...current, weights: resizePair(weights, index, delta) } : current);
    };
    const finish = () => {
      target.removeEventListener("pointermove", move);
      target.removeEventListener("lostpointercapture", finish);
      setResizing(false);
    };
    target.addEventListener("pointermove", move);
    target.addEventListener("lostpointercapture", finish);
  }
  function closePane(index: number) {
    setResizing(false);
    setLayout(current => ({ ids: current.ids.filter((_, item) => item !== index), weights: removePaneWeight(current.weights, index) }));
  }
  function changeSession(index: number, id: string) {
    if (ids.some((current, itemIndex) => itemIndex !== index && current === id)) { setActionError("这个会话已在同屏中，请选择其他会话。"); return; }
    setActionError("");
    if (index === 0) {
      void host.request("host.activateSession", { id }).catch(cause => setActionError(cause instanceof Error ? cause.message : String(cause)));
    } else setIds(current => current.map((item, itemIndex) => itemIndex === index ? id : item));
  }

  if (error) return <div className="session-grid surface-center"><ErrorNotice title="同屏会话没有连上" message={error} recovery="可以重试，或关闭同屏返回官方单会话。" /><div><button className="wb-btn" onClick={() => void reload()}>重试</button><button className="wb-btn" onClick={onNativeRestore}>返回官方单会话</button></div></div>;
  if (!boot) return <div className="session-grid surface-center"><Spinner size={16} />正在读取会话</div>;
  return <div className="session-grid wb">
    {actionError ? <p className="wb-error session-grid-error" role="alert">{actionError}</p> : null}
    <div className="session-grid-scroll"><div className="session-grid-panes" ref={gridRef} style={{ gridTemplateColumns: weights.map(weight => `minmax(300px, ${weight}fr)`).join(" ") }}>
      {ids.map((id, index) => {
        const waitingEntry = recovery?.entries.find(item => item.id === `session:${id}` && item.status === "WAITING");
        return <div className="session-grid-cell" key={id || `pane-${index}`}>{index > 0 ? <div className="session-resizer" role="separator" aria-label={`调整会话 ${index} 与 ${index + 1} 的宽度`} aria-orientation="vertical" tabIndex={0} onPointerDown={event => beginResize(event, index - 1)} onKeyDown={event => {
          if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return;
          event.preventDefault();
          const delta = event.key === "ArrowLeft" ? -0.1 : 0.1;
          setLayout(current => ({ ...current, weights: resizePair(current.weights, index - 1, delta) }));
        }} /> : null}<SessionPane host={host} index={index} sessions={sessions} models={boot.nativeModels} sessionId={id} recovery={waitingEntry ? { enabled: Boolean(recovery?.enabled), nextAt: waitingEntry.nextAt, note: waitingEntry.note } : null} onSessionId={next => changeSession(index, next)} onSessionCreated={created => {
          pendingSessions.current.set(created.id, created);
          setSessions(current => [created, ...current.filter(item => item.id !== created.id)]);
        }} onOpenSessions={() => void refreshSessions()} onAdd={index === 0 ? () => void add() : undefined} canAdd={ids.length < 3 && !adding} onRestore={index === 0 ? onNativeRestore : undefined} onClose={index > 0 ? () => closePane(index) : undefined} /></div>;
      })}
    </div></div>
  </div>;
}
